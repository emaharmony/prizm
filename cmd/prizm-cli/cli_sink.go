// Package main implements CLI-specific tool loop sink.
//
// CLISink prints tool calls and results to the terminal.
package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/emaharmony/prizm/internal/toolloop"
)

// CLISink prints tool loop events to the terminal.
type CLISink struct{}

var _ toolloop.Sink = (*CLISink)(nil)

func (s *CLISink) OnToolCall(name string, args map[string]any) {
	argsJSON, _ := json.Marshal(args)
	clearThinkingLine()
	fmt.Printf("  🔧 %s(%s)\n", name, string(argsJSON))
}

func (s *CLISink) OnToolResult(name string, result string, summary toolloop.CallSummary) {
	status := "✓"
	if summary.Status == "error" {
		status = "✗"
	}
	fmt.Printf("     → [%s] %s\n", status, truncateStr(result, 200))
}

func (s *CLISink) OnProgress(content string) {
	// CLI doesn't do progressive display — final content printed by caller
}

func (s *CLISink) OnComplete(content string, modelInfo toolloop.ModelInfo) {
	// Final content printed by caller after the loop returns
}

func (s *CLISink) OnError(err error) {
	fmt.Fprintf(os.Stderr, "Tool loop error: %v\n", err)
}