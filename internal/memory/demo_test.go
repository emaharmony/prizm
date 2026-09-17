package memory

import (
	"context"
	"fmt"
	"testing"
)

func TestDemoMemoryStore(t *testing.T) {
	// Use a temp dir so we don't pollute the real workspace
	store := NewMarkdownStore(t.TempDir())
	ctx := context.Background()

	// Write test memories
	memories := []Memory{
		{
			Content:   "Decided to use local models (nemotron-3-nano) for memory gate and extraction. Falls back to qwen3.5:4b, then session LLM. This saves ~62% tokens per memory write.",
			Category:  "decision",
			Tier:      "persist",
			Summary:   "Use local models for memory extraction",
			KeyTopics: []string{"memory", "local-models", "tokens", "architecture"},
			Source:    "prizm:lumi",
			AgentID:   "lumi",
			ProjectID: "prizm",
		},
		{
			Content:   "Ema prefers dark mode for all IDEs and interfaces. This applies to VSCode, terminal, and any web dashboards.",
			Category:  "preference",
			Tier:      "persist",
			Summary:   "Ema prefers dark mode everywhere",
			KeyTopics: []string{"preference", "dark-mode", "ui", "ema"},
			Source:    "prizm:lumi",
			AgentID:   "lumi",
			ProjectID: "prizm",
		},
		{
			Content:   "The coding cascade is: Mango (big tasks) → Junie (small tasks) → Lumi (last resort). Lumi reviews all code before push.",
			Category:  "fact",
			Tier:      "active",
			Summary:   "Coding cascade: Mango → Junie → Lumi",
			KeyTopics: []string{"coding-cascade", "mango", "junie", "lumi", "workflow"},
			Source:    "prizm:lumi",
			AgentID:   "lumi",
			ProjectID: "prizm",
		},
		{
			Content:   "Prizm uses NATS JetStream for event bus. Events follow the pattern prizm.<domain>.<action>. All memory operations emit events.",
			Category:  "fact",
			Tier:      "active",
			Summary:   "Prizm event bus uses NATS JetStream",
			KeyTopics: []string{"nats", "events", "architecture", "jetstream"},
			Source:    "prizm:lumi",
			AgentID:   "lumi",
			ProjectID: "prizm",
		},
		{
			Content:   "Recall (Remembrance v2) runs on port 18790. The embedding model is nomic-embed-text. REST API strips embedding blobs from responses.",
			Category:  "fact",
			Tier:      "active",
			Summary:   "Recall runs on port 18790 with nomic-embed-text",
			KeyTopics: []string{"recall", "remembrance", "embeddings", "port-18790"},
			Source:    "prizm:lumi",
			AgentID:   "lumi",
			ProjectID: "prizm",
		},
	}

	fmt.Println("\n=== Writing test memories ===")
	for i, m := range memories {
		id, err := store.Store(ctx, m)
		if err != nil {
			t.Errorf("  ✗ Memory %d: %v", i+1, err)
		} else {
			fmt.Printf("  ✓ Memory %d: %s (category=%s, tier=%s)\n", i+1, id, m.Category, m.Tier)
		}
	}

	fmt.Println("\n=== Searching for 'local models' ===")
	results, err := store.Search(ctx, "local models", 5)
	if err != nil {
		t.Errorf("  ✗ Search error: %v", err)
	} else {
		for _, r := range results {
			fmt.Printf("  • [%s] %s (category=%s, tier=%s)\n", r.ID[:8], r.Summary, r.Category, r.Tier)
		}
	}

	fmt.Println("\n=== Searching for 'dark mode' ===")
	results, err = store.Search(ctx, "dark mode", 5)
	if err != nil {
		t.Errorf("  ✗ Search error: %v", err)
	} else {
		if len(results) == 0 {
			t.Error("  ✗ Expected results for 'dark mode' search")
		}
		for _, r := range results {
			fmt.Printf("  • [%s] %s (category=%s)\n", r.ID[:8], r.Summary, r.Category)
		}
	}

	fmt.Println("\n=== Listing recent memories (limit 3) ===")
	recent, err := store.ListRecent(ctx, 3)
	if err != nil {
		t.Errorf("  ✗ List error: %v", err)
	} else {
		if len(recent) != 3 {
			t.Errorf("  ✗ Expected 3 recent memories, got %d", len(recent))
		}
		for _, r := range recent {
			fmt.Printf("  • [%s] %s\n", r.ID[:8], r.Summary)
		}
	}

	fmt.Println("\n=== Getting a specific memory by ID ===")
	results, _ = store.Search(ctx, "coding cascade", 1)
	if len(results) > 0 {
		got, err := store.Get(ctx, results[0].ID)
		if err != nil {
			t.Errorf("  ✗ Get error: %v", err)
		} else if got != nil {
			fmt.Printf("  ✓ Found: [%s] %s\n", got.ID[:8], got.Summary)
			fmt.Printf("    Content: %s\n", truncate(got.Content, 80))
			fmt.Printf("    Category: %s, Tier: %s\n", got.Category, got.Tier)
			fmt.Printf("    Key Topics: %v\n", got.KeyTopics)
		}
	}

	fmt.Println("\n=== All tests passed! ===")
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}