// Package tool provides the memory_write tool for persisting agent memories.
// It gates conversation turns through the local model, extracts structured
// memories, and writes them to the local MarkdownStore.
package tool

import (
	"context"
	"fmt"
	"log"

	"github.com/emaharmony/prizm/internal/memory"
)

// MemoryWriteTool persists agent memories through gate → extract → store.
// It uses local Ollama models to decide what's worth remembering and
// to extract structured data, then writes to the local MarkdownStore.
type MemoryWriteTool struct {
	Gate   *memory.GateExtractor
	Store  memory.MemoryStore
	Events *memory.EventEmitter
}

func (t *MemoryWriteTool) Name() string { return "memory_write" }
func (t *MemoryWriteTool) Description() string {
	return "Persists a conversation turn as a long-term memory. The gate model decides if it's worth remembering."
}
func (t *MemoryWriteTool) Schema() ToolSchema {
	return ToolSchema{
		Input: map[string]ParamSpec{
			"content":    {Type: "string", Description: "The conversation turn or fact to remember", Required: true},
			"category":   {Type: "string", Description: "Memory category: decision, preference, fact, observation (auto-detected if empty)", Required: false},
			"tier":       {Type: "string", Description: "Memory tier: ephemeral, active, persist (auto-detected if empty)", Required: false},
			"source":     {Type: "string", Description: "Source agent (e.g., prizm:lumi)", Required: false},
			"agent_id":   {Type: "string", Description: "Agent that created this memory", Required: false},
			"session_id": {Type: "string", Description: "Session context", Required: false},
			"project_id": {Type: "string", Description: "Project context", Required: false},
		},
		Output: ParamSpec{Type: "object", Description: "The persisted memory with id, or rejection reason"},
	}
}

func (t *MemoryWriteTool) Execute(ctx context.Context, input map[string]any) (ToolResult, error) {
	content, ok := input["content"].(string)
	if !ok || content == "" {
		return ToolResult{Success: false, Error: "required parameter 'content' must be a non-empty string"}, nil
	}

	// Step 1: Gate — is this worth remembering?
	if t.Gate != nil {
		result, err := t.Gate.Gate(ctx, content)
		if err != nil {
			log.Printf("[MEMORY-WRITE] gate error: %v", err)
			// Gate failure is non-fatal; proceed without gate check
		} else if !result.ShouldRemember {
			log.Printf("[MEMORY-WRITE] gate rejected: %s", result.Reasoning)
			if t.Events != nil {
				t.Events.EmitGateRejected(result.Reasoning, "")
			}
			return ToolResult{
				Success: true,
				Output: map[string]any{
					"gate":           "rejected",
					"reasoning":      result.Reasoning,
					"original_input": content,
				},
			}, nil
		}
	}

	// Step 2: Extract — structure the memory
	var extractResult *memory.ExtractResult
	if t.Gate != nil {
		result, err := t.Gate.Extract(ctx, content)
		if err != nil {
			log.Printf("[MEMORY-WRITE] extract error: %v, using raw content", err)
		} else {
			extractResult = result
		}
	}

	// Build the memory struct
	mem := memory.Memory{
		Content: content,
		Source:   strVal(input, "source"),
		AgentID:  strVal(input, "agent_id"),
		SessionID: strVal(input, "session_id"),
		ProjectID: strVal(input, "project_id"),
	}

	// Override with extracted values if available
	if extractResult != nil {
		mem.Category = extractResult.Category
		mem.Tier = extractResult.Tier
		mem.Summary = extractResult.Summary
		mem.KeyTopics = extractResult.KeyTopics
		if extractResult.Content != "" {
			mem.Content = extractResult.Content
		}
	}

	// Allow explicit overrides from input
	if v := strVal(input, "category"); v != "" {
		mem.Category = v
	}
	if v := strVal(input, "tier"); v != "" {
		mem.Tier = v
	}

	// Defaults
	if mem.Category == "" {
		mem.Category = "fact"
	}
	if mem.Tier == "" {
		mem.Tier = "active"
	}
	if mem.Summary == "" {
		if len(mem.Content) > 200 {
			mem.Summary = mem.Content[:200] + "..."
		} else {
			mem.Summary = mem.Content
		}
	}

	// Step 3: Store
	id, err := t.Store.Store(ctx, mem)
	if err != nil {
		return ToolResult{Success: false, Error: fmt.Sprintf("failed to store memory: %v", err)}, nil
	}
	mem.ID = id

	// Step 4: Emit events
	if t.Events != nil {
		modelName := ""
		if t.Gate != nil && len(t.Gate.Models) > 0 {
			modelName = t.Gate.Models[0]
		}
		t.Events.EmitMemoryWrite(mem, modelName)
	}

	return ToolResult{
		Success: true,
		Output: map[string]any{
			"gate":      "passed",
			"memory_id": id,
			"category":  mem.Category,
			"tier":      mem.Tier,
			"summary":   mem.Summary,
		},
	}, nil
}

func strVal(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}