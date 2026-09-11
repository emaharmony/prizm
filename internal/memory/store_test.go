package memory

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func tempStore(t *testing.T) *MarkdownStore {
	t.Helper()
	dir := t.TempDir()
	return NewMarkdownStore(dir)
}

func TestStoreAndRetrieve(t *testing.T) {
	s := tempStore(t)

	id, err := s.Store(context.Background(), Memory{
		Content:   "Decided to use local models for memory extraction",
		Category:  "decision",
		Tier:      "active",
		Summary:   "Use local models for memory extraction",
		KeyTopics: []string{"memory", "local-models", "architecture"},
		AgentID:   "lumi",
		Source:    "prizm:lumi",
	})
	if err != nil {
		t.Fatalf("Store: %v", err)
	}
	if id == "" {
		t.Fatal("expected non-empty ID")
	}

	got, err := s.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil {
		t.Fatal("expected memory, got nil")
	}
	if got.Content != "Decided to use local models for memory extraction" {
		t.Errorf("Content = %q, want matching", got.Content)
	}
	if got.Category != "decision" {
		t.Errorf("Category = %q, want decision", got.Category)
	}
	if got.AgentID != "lumi" {
		t.Errorf("AgentID = %q, want lumi", got.AgentID)
	}
}

func TestSearchByKeyword(t *testing.T) {
	s := tempStore(t)

	_, err := s.Store(context.Background(), Memory{
		Content:   "Decided to use nemotron for memory gate",
		Category:  "decision",
		Tier:      "active",
		Summary:   "Nemotron memory gate",
		KeyTopics: []string{"memory", "nemotron"},
		Source:    "prizm:lumi",
	})
	if err != nil {
		t.Fatalf("Store: %v", err)
	}

	_, err = s.Store(context.Background(), Memory{
		Content:   "Prizm uses event-driven architecture",
		Category:  "fact",
		Tier:      "persist",
		Summary:   "Event-driven architecture",
		KeyTopics: []string{"events", "architecture"},
		Source:    "prizm:lumi",
	})
	if err != nil {
		t.Fatalf("Store 2: %v", err)
	}

	results, err := s.Search(context.Background(), "nemotron", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected results for nemotron search")
	}

	// nemotron should be in top result
	found := false
	for _, r := range results {
		if r.Category == "decision" {
			found = true
		}
	}
	if !found {
		t.Error("nemotron search should find the decision memory")
	}
}

func TestListRecent(t *testing.T) {
	s := tempStore(t)

	for i := 0; i < 5; i++ {
		_, err := s.Store(context.Background(), Memory{
			Content:  "memory item",
			Category: "fact",
			Summary:  "test memory",
		})
		if err != nil {
			t.Fatalf("Store %d: %v", i, err)
		}
	}

	results, err := s.ListRecent(context.Background(), 3)
	if err != nil {
		t.Fatalf("ListRecent: %v", err)
	}
	if len(results) != 3 {
		t.Errorf("ListRecent returned %d items, want 3", len(results))
	}
}

func TestConcurrentWrites(t *testing.T) {
	s := tempStore(t)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var ids []string

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			id, err := s.Store(context.Background(), Memory{
				Content:  "concurrent memory",
				Category: "fact",
				Summary:  "test",
			})
			if err != nil {
				t.Errorf("Store %d: %v", n, err)
				return
			}
			mu.Lock()
			ids = append(ids, id)
			mu.Unlock()
		}(i)
	}
	wg.Wait()

	if len(ids) != 10 {
		t.Errorf("stored %d memories, want 10", len(ids))
	}
}

func TestGetNotFound(t *testing.T) {
	s := tempStore(t)
	got, err := s.Get(context.Background(), "nonexistent")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != nil {
		t.Error("expected nil for nonexistent ID")
	}
}

func TestSearchEmptyQuery(t *testing.T) {
	s := tempStore(t)
	_, err := s.Store(context.Background(), Memory{
		Content:  "test",
		Category: "fact",
		Summary:  "test",
	})
	if err != nil {
		t.Fatalf("Store: %v", err)
	}

	results, err := s.Search(context.Background(), "", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) == 0 {
		t.Error("empty query should return all memories")
	}
}

func TestParseExistingMarkdown(t *testing.T) {
	dir := t.TempDir()
	memDir := filepath.Join(dir, "memory")
	os.MkdirAll(memDir, 0755)

	content := `# 2026-08-10 Memory Log

### 01HXYZABC — Test summary

- **Category:** decision
- **Tier:** active
- **Source:** prizm:lumi
- **Agent:** lumi
- **Key Topics:** memory, test

This is the memory content.
`
	os.WriteFile(filepath.Join(memDir, "2026-08-10.md"), []byte(content), 0644)

	s := NewMarkdownStore(dir)
	got, err := s.Get(context.Background(), "01HXYZABC")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil {
		t.Fatal("expected to find existing memory")
	}
	if got.Category != "decision" {
		t.Errorf("Category = %q, want decision", got.Category)
	}
	if got.AgentID != "lumi" {
		t.Errorf("AgentID = %q, want lumi", got.AgentID)
	}
}
// --- V79: Multi-strategy parser tests ---

func TestParseByHeaders(t *testing.T) {
	dir := t.TempDir()
	memDir := filepath.Join(dir, "memory")
	os.MkdirAll(memDir, 0755)

	content := `## Lumi Name Origin

Kirbii asked me what I'd name myself. I said "Lumi — like luminescent."
Ema made it official.

## Active Projects

- Prism — Event-native agentic environment
- BassBook — ASP.NET Core + Next.js

## Model Stack

- Cloud: glm-5.1:cloud (Lumi)
- Local: nemotron-3-nano (memory extraction)
`
	os.WriteFile(filepath.Join(memDir, "2026-07-27.md"), []byte(content), 0644)

	s := NewMarkdownStore(dir)
	recent, err := s.ListRecent(context.Background(), 10)
	if err != nil {
		t.Fatalf("ListRecent: %v", err)
	}
	if len(recent) < 3 {
		t.Fatalf("expected at least 3 memories, got %d", len(recent))
	}
	// First memory should be the "Lumi Name Origin" section
	found := false
	for _, m := range recent {
		if strings.Contains(m.Summary, "Lumi Name Origin") {
			found = true
			if !strings.Contains(m.Content, "luminescent") {
				t.Errorf("Content should contain 'luminescent', got: %s", m.Content)
			}
			break
		}
	}
	if !found {
		t.Error("expected to find 'Lumi Name Origin' memory")
	}
}

func TestParseByParagraphs(t *testing.T) {
	dir := t.TempDir()
	memDir := filepath.Join(dir, "memory")
	os.MkdirAll(memDir, 0755)

	content := `This is the first paragraph about decisions.

This is the second paragraph about preferences.

- Bullet point one
- Bullet point two
`
	os.WriteFile(filepath.Join(memDir, "2026-09-01.md"), []byte(content), 0644)

	s := NewMarkdownStore(dir)
	recent, err := s.ListRecent(context.Background(), 10)
	if err != nil {
		t.Fatalf("ListRecent: %v", err)
	}
	if len(recent) < 2 {
		t.Fatalf("expected at least 2 memories, got %d", len(recent))
	}
}

func TestParseMixedFormats(t *testing.T) {
	dir := t.TempDir()
	memDir := filepath.Join(dir, "memory")
	os.MkdirAll(memDir, 0755)

	// File with structured format
	structured := `### 01HXYZ — Structured memory

- **Category:** decision

Content here.
`
	os.WriteFile(filepath.Join(memDir, "2026-09-05.md"), []byte(structured), 0644)

	// File with headers
	headers := `## Header Memory

Content with headers.
`
	os.WriteFile(filepath.Join(memDir, "2026-09-06.md"), []byte(headers), 0644)

	// File with plain paragraphs
	paragraphs := `Just a plain paragraph about something.

Another paragraph about something else.
`
	os.WriteFile(filepath.Join(memDir, "2026-09-07.md"), []byte(paragraphs), 0644)

	s := NewMarkdownStore(dir)
	recent, err := s.ListRecent(context.Background(), 10)
	if err != nil {
		t.Fatalf("ListRecent: %v", err)
	}
	if len(recent) < 3 {
		t.Fatalf("expected at least 3 memories from mixed formats, got %d", len(recent))
	}

	// Verify each type was parsed
	foundStructured := false
	foundHeader := false
	foundParagraph := false
	for _, m := range recent {
		if m.ID == "01HXYZ" {
			foundStructured = true
		}
		if strings.Contains(m.Summary, "Header Memory") {
			foundHeader = true
		}
		if strings.Contains(m.Summary, "Just a plain") {
			foundParagraph = true
		}
	}
	if !foundStructured {
		t.Error("expected to find structured memory")
	}
	if !foundHeader {
		t.Error("expected to find header-based memory")
	}
	if !foundParagraph {
		t.Error("expected to find paragraph-based memory")
	}
}

func TestSlugify(t *testing.T) {
	tests := []struct{ input, want string }{
		{"Lumi Name Origin", "lumi-name-origin"},
		{"Active Projects", "active-projects"},
		{"Model Stack (2026)", "model-stack-2026"},
		{"Hello   World", "hello-world"},
	}
	for _, tt := range tests {
		got := slugify(tt.input)
		if got != tt.want {
			t.Errorf("slugify(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// --- V84: Dedup and junk filter tests ---

func TestListRecentDedup(t *testing.T) {
	dir := t.TempDir()
	memDir := filepath.Join(dir, "memory")
	os.MkdirAll(memDir, 0755)

	// Create a file with the same ID appearing twice
	content := `### test-dup-id — First occurrence

- **Category:** fact
- **Tier:** active

First content.

### test-dup-id — Second occurrence

- **Category:** fact
- **Tier:** active

Second content.
`
	os.WriteFile(filepath.Join(memDir, "2026-09-11.md"), []byte(content), 0644)

	s := NewMarkdownStore(dir)
	results, err := s.ListRecent(context.Background(), 0)
	if err != nil {
		t.Fatalf("ListRecent: %v", err)
	}

	// Should only return 1 entry for the duplicate ID
	idCount := 0
	for _, m := range results {
		if m.ID == "test-dup-id" {
			idCount++
		}
	}
	if idCount > 1 {
		t.Errorf("expected dedup for test-dup-id, got %d occurrences", idCount)
	}
}

func TestIsJunkEntry(t *testing.T) {
	tests := []struct {
		name    string
		memory  Memory
		isJunk  bool
	}{
		{
			name:   "dream candidate summary",
			memory: Memory{Summary: "Candidate: Reflections: Theme: assistant", Content: "Some content here"},
			isJunk: true,
		},
		{
			name:   "dream candidate content",
			memory: Memory{Summary: "reflection", Content: "Candidate: something from dream cycle"},
			isJunk: true,
		},
		{
			name:   "conversation metadata",
			memory: Memory{Summary: "conversation", Content: "Conversation info (untrusted metadata): ..."},
			isJunk: true,
		},
		{
			name:   "empty content and summary",
			memory: Memory{Summary: "", Content: ""},
			isJunk: true,
		},
		{
			name:   "valid memory",
			memory: Memory{Summary: "Ema communication style", Content: "Direct, fast, decision-oriented."},
			isJunk: false,
		},
		{
			name:   "short but valid",
			memory: Memory{Summary: "test", Content: "test"},
			isJunk: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isJunkEntry(tt.memory)
			if got != tt.isJunk {
				t.Errorf("isJunkEntry(%+v) = %v, want %v", tt.memory, got, tt.isJunk)
			}
		})
	}
}

