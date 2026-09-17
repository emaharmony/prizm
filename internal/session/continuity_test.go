package session

import (
	"strings"
	"testing"
)

func TestSessionMemory_Format(t *testing.T) {
	sm := &SessionMemory{
		Title:        "Implement auto-extraction",
		CurrentState: "Writing tests for AutoExtractor",
		Task:         "Add memory auto-extraction to Prizm",
		Files:        []string{"/path/to/autoextract.go", "/path/to/gate.go"},
		Errors:       []string{"database is locked"},
		Learnings:    []string{"normalizeCategory order matters"},
		Worklog:      []string{"Created AutoExtractor", "Updated gate prompt", "Wired into serve"},
		KeyResults:   "21 tests passing",
	}
	out := sm.Format()
	if !strings.Contains(out, "Session Continuity") {
		t.Error("expected header")
	}
	if !strings.Contains(out, "Implement auto-extraction") {
		t.Error("expected title")
	}
	if !strings.Contains(out, "autoextract.go") {
		t.Error("expected file path")
	}
	if !strings.Contains(out, "database is locked") {
		t.Error("expected error")
	}
	if !strings.Contains(out, "Continue from Current State") {
		t.Error("expected continuation directive")
	}
}

func TestBuildSessionMemoryFromMessages(t *testing.T) {
	msgs := []Message{
		{Role: "user", Content: "Please add auto-extraction to the memory system"},
		{Role: "agent", Content: "I'll create the AutoExtractor in /internal/memory/autoextract.go"},
		{Role: "tool", Content: "[Tool: write_file] wrote /internal/memory/autoextract.go"},
		{Role: "agent", Content: "Now updating gate.go with the new 4-type taxonomy"},
		{Role: "tool", Content: "[Tool: write_file] error: database is locked"},
	}
	sm := BuildSessionMemoryFromMessages(msgs)
	if sm.Title == "" {
		t.Error("expected title from first user message")
	}
	if sm.Task == "" {
		t.Error("expected task from first user message")
	}
	if len(sm.Files) == 0 {
		t.Error("expected files from tool messages")
	}
	if len(sm.Errors) == 0 {
		t.Error("expected errors from failed tool call")
	}
	if len(sm.Worklog) == 0 {
		t.Error("expected worklog from agent messages")
	}
}

func TestTruncateMsg(t *testing.T) {
	if got := truncateMsg("hello", 10); got != "hello" {
		t.Errorf("truncateMsg(hello, 10) = %q", got)
	}
	if got := truncateMsg("hello world this is long", 5); got != "hello..." {
		t.Errorf("truncateMsg(long, 5) = %q", got)
	}
}

func TestExtractFiles(t *testing.T) {
	var files []string
	extractFiles("Wrote file at /path/to/code.go and /other/file_test.go", &files)
	if len(files) < 1 {
		t.Error("expected at least 1 file")
	}
	found := false
	for _, f := range files {
		if strings.Contains(f, "code.go") {
			found = true
		}
	}
	if !found {
		t.Error("expected code.go in files")
	}
}

func TestExtractErrors(t *testing.T) {
	var errors []string
	extractErrors("error: database is locked\nsome other line", &errors)
	if len(errors) != 1 {
		t.Fatalf("expected 1 error, got %d", len(errors))
	}
	if !strings.Contains(errors[0], "database is locked") {
		t.Errorf("expected 'database is locked' in error, got %q", errors[0])
	}
}