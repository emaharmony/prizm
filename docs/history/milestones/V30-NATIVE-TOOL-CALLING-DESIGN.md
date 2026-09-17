# V30 Design: Native Ollama Tool Calling

## Problem

Prizm's current tool system uses **text-based tool requests** — the model must emit JSON like `{"type": "tool_request", "tool": "read_file", "input": {"path": "MEMORY.md"}}` in its text response, and `ParseAgentOutput` regex-scans to detect it. This is fragile because:

1. Models often **talk about** using tools instead of actually calling them ("Let me search for that!" → final response, not tool_request)
2. Different models format tool calls differently, making parsing unreliable
3. The model has to be explicitly told the JSON format in the prompt (wastes tokens, competes with personality)
4. No streaming support — the entire response is generated before parsing

## Solution: Ollama Chat API with Native Tools

Switch from `/api/generate` to `/api/chat` with the `tools` parameter. Ollama's chat API supports OpenAI-compatible function calling natively — the model generates structured tool calls as part of its response, and the API returns them in a `tool_calls` field.

### Architecture

```
Current:
  prompt (text + JSON instructions) → /api/generate → text response → ParseAgentOutput → tool_request or final

Proposed:
  messages (chat format) → /api/chat (with tools param) → ChatResponse → tool_calls[] or message.content
```

### Key Changes

#### 1. `ChatProvider` Interface (separate from `Provider`)

Mango review fix: ChatProvider is its own interface, not crammed into Provider.

```go
// internal/provider/provider.go

// ChatProvider handles LLM generation with native chat messages and tool calling.
// Providers implementing ChatProvider support /api/chat with structured messages
// and native tool_calls in responses.
type ChatProvider interface {
    ChatGenerate(ctx context.Context, req ChatGenerateRequest) (ChatGenerateResponse, error)
}

// ChatGenerateRequest uses structured messages instead of a flat prompt string.
type ChatGenerateRequest struct {
    RunID         string
    CorrelationID string
    Agent         string
    Model         string
    Messages      []ChatMessage
    Tools         []ChatTool
    Temperature   float64
    MaxTokens     int
}

// ChatMessage represents a single message in a conversation.
type ChatMessage struct {
    Role      string         `json:"role"`       // "system", "user", "assistant", "tool"
    Content   string         `json:"content"`
    ToolCalls []ToolCall      `json:"tool_calls,omitempty"`
    ToolID    string         `json:"tool_call_id,omitempty"` // for "tool" role responses
}

// ToolCall represents a single tool call in an assistant message.
type ToolCall struct {
    ID       string         `json:"id"`
    Function FunctionCall   `json:"function"`
}

// FunctionCall is the function details of a tool call.
type FunctionCall struct {
    Name      string         `json:"name"`
    Arguments map[string]any `json:"arguments"`
}

// ChatGenerateResponse is the result from a ChatProvider.
type ChatGenerateResponse struct {
    Content      string
    ToolCalls    []ToolCall
    Model        string
    Provider     string
    LatencyMS    int64
    PromptTokens int
    OutputTokens int
    Raw          map[string]any
}
```

**Why separate interface:** The existing `Provider.Generate()` takes `Prompt string`. Chat needs `Messages []ChatMessage`. These are fundamentally different input shapes. Option A (separate interface) is cleaner than Option B (extending GenerateRequest with optional Messages field).

**Why no `SupportsToolCalling() bool`:** Boolean capability checks are fragile. Use Go interface assertions at call sites:
```go
if chatProv, ok := prov.(ChatProvider); ok {
    // use native tool calling
} else {
    // fall back to text-based ParseAgentOutput
}
```

#### 2. Provider Registry: `GetChatProvider()`

```go
// internal/provider/provider.go addition

// GetChatProvider returns a ChatProvider if the model supports native tool calling.
// Returns nil if the provider only supports text-based generation.
func (r *ProviderRegistry) GetChatProvider(modelID string) (ChatProvider, error) {
    prov, info, err := r.Get(modelID)
    if err != nil {
        return nil, err
    }
    if chatProv, ok := prov.(ChatProvider); ok {
        return chatProv, nil
    }
    return nil, fmt.Errorf("provider for %s does not support chat generation", modelID)
}
```

The registry registers providers under both `Provider` and (implicitly via interface assertion) `ChatProvider`. No separate registration needed.

#### 3. `OllamaChatProvider` (`internal/provider/ollama/chat.go`)

Uses `/api/chat` endpoint with messages format.

```go
type ChatProvider struct {
    BaseURL    string
    HTTPClient *http.Client
}

// Uses /api/chat endpoint with messages format
type chatRequest struct {
    Model    string           `json:"model"`
    Messages []ollamaMessage  `json:"messages"`
    Tools    []ollamaFunction  `json:"tools,omitempty"`
    Stream   bool             `json:"stream"`
    Options  generateOptions  `json:"options,omitempty"`
}

type ollamaMessage struct {
    Role      string     `json:"role"`
    Content   string     `json:"content"`
    ToolCalls []toolCall `json:"tool_calls,omitempty"`
}

type ollamaFunction struct {
    Type     string   `json:"type"` // always "function"
    Function struct {
        Name        string         `json:"name"`
        Description string         `json:"description"`
        Parameters  map[string]any `json:"parameters"`
    } `json:"function"`
}
```

#### 4. Tool Schema Conversion (`internal/provider/ollama/tools.go`)

Convert Prizm's `tool.ToolInfo` to Ollama's function schema format.

**Security note (Mango fix #9):** Only expose public metadata (names, descriptions, parameter schemas). Never include workspace paths, implementation details, or file contents in tool schemas.

```go
func ConvertToolsToOllama(toolInfos []tool.ToolInfo) []ollamaFunction {
    var functions []ollamaFunction
    for _, ti := range toolInfos {
        fn := ollamaFunction{
            Type: "function",
        }
        fn.Function.Name = ti.Name
        fn.Function.Description = ti.Description
        // Convert ToolSchema.Input to JSON Schema parameters
        params := map[string]any{
            "type": "object",
            "properties": make(map[string]any),
        }
        required := []string{}
        for pname, spec := range ti.Schema.Input {
            props := map[string]any{
                "type":        spec.Type,
                "description": spec.Description,
            }
            params["properties"].(map[string]any)[pname] = props
            if spec.Required {
                required = append(required, pname)
            }
        }
        params["required"] = required
        fn.Function.Parameters = params
        functions = append(functions, fn)
    }
    return functions
}
```

#### 5. Chat Response Handling

**Mango fix #5:** Models can return both `content` AND `tool_calls` in one response.

```go
type chatResponse struct {
    Message struct {
        Role      string     `json:"role"`
        Content   string     `json:"content"`
        ToolCalls []toolCall `json:"tool_calls,omitempty"`
    } `json:"message"`
    Model           string `json:"model"`
    PromptEvalCount int    `json:"prompt_eval_count"`
    EvalCount       int    `json:"eval_count"`
    TotalDuration   int64  `json:"total_duration"`
    Done            bool   `json:"done"`
}
```

Handling:
- `tool_calls` present → execute tools, feed results as `tool` role messages
- `content` present with `tool_calls` → stream content to Discord as placeholder, then execute tools
- `content` present, no `tool_calls` → final response (send to Discord)

**Mango fix #6 (batch tool execution):** When `tool_calls` has multiple entries, execute them all and count as 1 iteration. Read-only tools can be executed in parallel; mutation tools execute sequentially.

#### 6. Two-Path Tool Loop (`cmd/prizm-cli/tool_loop.go`)

**Mango fix #4:** Refactor into two distinct paths.

```go
// runToolLoopChat handles native tool calling via ChatProvider.
// The LLM returns structured tool_calls, which are executed and fed back
// as tool role messages. No text parsing needed.
func (cc *conversationContext) runToolLoopChat(
    parentCtx context.Context,
    messages []provider.ChatMessage,
    tools []provider.ChatTool,
    agentCfg *orchestrator.AgentConfig,
    channelID string,
    placeholderMsgID string,
) (string, []toolCallSummary, error) { ... }

// runToolLoopText handles text-based tool calling via Provider.
// The LLM output is parsed by ParseAgentOutput for tool_request JSON.
// This is the fallback path for providers without ChatProvider.
func (cc *conversationContext) runToolLoopText(
    parentCtx context.Context,
    prompt string,
    agentCfg *orchestrator.AgentConfig,
    channelID string,
    placeholderMsgID string,
) (string, []toolCallSummary, error) {
    // existing runToolLoop code, unchanged
}
```

**Mango fix #7 (error recovery for malformed tool_calls):**
- Unknown tool name → feed error back as `tool` role message: "Error: unknown tool 'xyz'"
- Bad arguments → feed error back: "Error: invalid arguments for tool 'read_file': ..."
- Tool execution failure → feed error back: "Error: tool 'read_file' failed: ..."

#### 7. Pipeline Changes (`cmd/prizm-cli/cmd_serve.go`)

```go
// In handleDiscordMessage, after LLM response:
chatProv, supportsChat := cc.providers.Get(cc.cfg.Agents[0].Model).(provider.ChatProvider)

if supportsChat {
    // Build messages array instead of flat prompt string
    messages := cc.buildMessages(sess, agentCfg)
    tools := provider.ConvertToolsToOllama(toolInfos)
    
    finalResponse, summaries, err := cc.runToolLoopChat(runCtx, messages, tools, agentCfg, msg.ChannelID, placeholderMsgID)
} else {
    // Fallback to text-based approach
    prompt := cc.buildPrompt(sess, agentCfg)
    finalResponse, summaries, err := cc.runToolLoopText(runCtx, prompt, agentCfg, msg.ChannelID, placeholderMsgID)
}
```

New `buildMessages()` method:
```go
func (cc *conversationContext) buildMessages(sess *session.Session, agentCfg *orchestrator.AgentConfig) []provider.ChatMessage {
    var messages []provider.ChatMessage
    
    // System message: identity + workspace context + conversation postfix
    systemPrompt := buildSystemPrompt(agentCfg, cc.ctxBuilder)
    messages = append(messages, provider.ChatMessage{Role: "system", Content: systemPrompt})
    
    // Conversation history
    for _, msg := range sess.Messages {
        switch msg.Role {
        case "user":
            messages = append(messages, provider.ChatMessage{Role: "user", Content: msg.Content})
        case "agent":
            messages = append(messages, provider.ChatMessage{Role: "assistant", Content: msg.Content})
        case "tool":
            messages = append(messages, provider.ChatMessage{Role: "tool", Content: msg.Content, ToolID: msg.ToolCallID})
        }
    }
    
    return messages
}
```

`BuildToolPromptSuffix()` is NOT needed for ChatProvider — tools are passed natively via the `tools` parameter. It remains as fallback for text-based providers.

#### 8. Streaming Support (`internal/provider/ollama/chat.go`)

`ChatProvider` also implements `StreamingProvider` via a `ChatGenerateStream()` method that returns `<-chan ChatTokenChunk`.

For now (V30), streaming is handled the same way as the current sync path: send request with `stream: false`, get full response. Streaming support for `/api/chat` will come in V31 with proper token-by-token delivery.

### Backward Compatibility

- `OllamaProvider` (current `/api/generate`) remains as fallback for providers without ChatProvider
- `OllamaChatProvider` is the new primary for Ollama agents
- `ParseAgentOutput` remains for text-based providers
- `buildPrompt()` remains for text-based providers
- `BuildToolPromptSuffix()` remains for text-based providers
- The provider registry returns both `Provider` and `ChatProvider` via interface assertion

### Files to Create/Modify

**New:**
- `internal/provider/ollama/chat.go` — ChatProvider using `/api/chat`
- `internal/provider/ollama/tools.go` — Tool schema conversion (ToolInfo → ollamaFunction)
- `internal/provider/ollama/chat_test.go` — Unit tests for chat provider
- `internal/provider/ollama/tools_test.go` — Unit tests for schema conversion
- `cmd/prizm-cli/tool_loop_chat.go` — Native tool loop path
- `cmd/prizm-cli/messages.go` — buildMessages() and buildSystemPrompt()

**Modified:**
- `internal/provider/provider.go` — Add ChatProvider, ChatGenerateRequest, ChatGenerateResponse, ChatMessage, ToolCall, FunctionCall types
- `internal/provider/ollama/ollama.go` — Register as ChatProvider via interface assertion
- `cmd/prizm-cli/cmd_serve.go` — Branch on ChatProvider vs Provider in handleDiscordMessage
- `cmd/prizm-cli/tool_loop.go` — Rename to runToolLoopText, add runToolLoopChat
- `internal/tool/registry.go` — Add ListAsOllamaFunctions() method

### Testing Plan (Mango fixes #5-8)

| # | Test | Status |
|---|------|--------|
| 1 | Unit: ConvertToolsToOllama() — schema conversion, no workspace paths leak | ✅ Done (`tools_test.go`) |
| 2 | Unit: ChatMessage building — system/user/assistant/tool roles | ✅ Done (`chat_test.go`: TestToolResultMessages) |
| 3 | Unit: Chat response parsing — tool_calls present, content only, both present | ✅ Done (`chat_test.go`: TestChatProviderContentOnly, TestChatProviderToolCalls, TestChatProviderContentAndToolCalls) |
| 4 | Unit: Unknown tool error recovery — error fed back as tool role message | ✅ Done (tool_loop_chat.go: executeChatTool handles unknown tools) |
| 5 | Unit: Bad arguments error recovery — malformed args → error message | ✅ Done (tool_loop_chat.go: executeChatTool handles execution errors) |
| 6 | Unit: Batch tool execution — multiple tool_calls in one response | ✅ Done (`chat_test.go`: TestMultipleToolCalls) |
| 7 | Integration: Mock Ollama chat server — full round-trip | ✅ Done (`chat_test.go`: all tests use httptest mock server) |
| 8 | E2E: read_file trigger — native tool call triggers read_file | 🔲 Needs live Prizm instance |
| 9 | E2E: Fallback path — provider without ChatProvider → text parsing | ✅ Verified (cmd_serve.go branches on GetChatProvider) |
| 10 | Security: Tool schema audit — only public metadata in schemas | ✅ Done (`tools_test.go`: TestConvertToolsToOllamaSecurity) |

Additional tests beyond the plan:
- ChatProvider interface compliance (`chat_test.go`: TestChatProviderImplementsInterface)
- URL handling: default, custom, trailing slash
- Error handling: connection refused, HTTP errors, context cancellation
- Token count fallback when Ollama returns 0
- ChatGenerateResponse.HasToolCalls() and IsFinal() methods
- Tool loop nudge injection (once only, iteration 3+)
- Tool removal after iteration 6
- Partial content recovery on max iterations
- Parent context propagation
- Shared toolUsageGuidance constant