# V31 - Native Chat Streaming Gap

## Summary

V31 is documented as a gap, not a completed implementation.

Prizm has streaming support through the `provider.StreamingProvider` interface and `LLMStage` can deliver token chunks through `StreamCallback`. Mock, OpenAI chat completions, Anthropic, and Gemini have streaming implementations. Serve mode can use callback-based delivery for streaming provider paths.

The native ChatProvider tool loop introduced for Ollama chat tool calling still uses synchronous `/api/chat` responses with `stream: false`.

## Current State

Implemented:

- `provider.StreamingProvider`
- `provider.TokenChunk`
- `LLMStage.executeStreaming`
- Stream callbacks for delivery paths such as Discord placeholder edits
- SSE decoders for streaming providers
- Streaming implementations for supported non-chat-tool paths

Not implemented:

- Streaming native `/api/chat` tool calls.
- Incremental tool-call assembly for native chat responses.
- A unified streamed ChatProvider interface.
- Interleaving streamed assistant text with tool calls in the native tool loop.

## Why It Matters

Native tool calling fixed brittle text parsing, but the current native path waits for the full chat response before tool execution. For long local model responses, this delays visible progress compared with standard streaming provider paths.

## Acceptance Criteria for Future Work

A future implementation should:

- Add a streamed chat response type that can represent text deltas and tool call deltas.
- Preserve existing sync ChatProvider behavior for compatibility.
- Execute tool calls only after complete tool-call arguments are available.
- Keep malformed tool-call recovery behavior.
- Avoid exposing extra workspace data in tool schemas.
- Test content-only, tool-only, and mixed content/tool responses.

## Status

Tracked as a known gap. Do not mark native chat tool-loop streaming as shipped until the `/api/chat` tool path streams end to end.
