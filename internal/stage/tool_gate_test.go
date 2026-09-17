package stage

import (
	"testing"
)

func TestToolRelevanceGate_ExcludeShortGreeting(t *testing.T) {
	gate := NewToolRelevanceGate(true)
	tools := []string{"read_file", "search_files", "git_status", "project_overview"}

	result := gate.Evaluate("Hey Lumi!", tools)
	if result.Decision != ToolDecisionExclude {
		t.Errorf("expected Exclude for greeting, got %v (reason: %s)", result.Decision, result.Reason)
	}
}

func TestToolRelevanceGate_ExcludeHello(t *testing.T) {
	gate := NewToolRelevanceGate(true)
	tools := []string{"read_file", "search_files", "git_status"}

	result := gate.Evaluate("Hello!", tools)
	if result.Decision != ToolDecisionExclude {
		t.Errorf("expected Exclude for 'Hello!', got %v", result.Decision)
	}
}

func TestToolRelevanceGate_ExcludeThanks(t *testing.T) {
	gate := NewToolRelevanceGate(true)
	tools := []string{"read_file", "search_files"}

	result := gate.Evaluate("Thanks!", tools)
	if result.Decision != ToolDecisionExclude {
		t.Errorf("expected Exclude for 'Thanks!', got %v", result.Decision)
	}
}

func TestToolRelevanceGate_ExcludeSayHello(t *testing.T) {
	gate := NewToolRelevanceGate(true)
	tools := []string{"read_file", "search_files", "git_status", "project_overview"}

	result := gate.Evaluate("Say hello in one sentence", tools)
	if result.Decision != ToolDecisionExclude {
		t.Errorf("expected Exclude for 'Say hello in one sentence', got %v (reason: %s)", result.Decision, result.Reason)
	}
}

func TestToolRelevanceGate_IncludeReadFile(t *testing.T) {
	gate := NewToolRelevanceGate(true)
	tools := []string{"read_file", "search_files", "git_status"}

	result := gate.Evaluate("Read the file at /Users/ema/projects/test.go", tools)
	if result.Decision != ToolDecisionInclude {
		t.Errorf("expected Include for file read request, got %v", result.Decision)
	}
}

func TestToolRelevanceGate_IncludeProjectOverview(t *testing.T) {
	gate := NewToolRelevanceGate(true)
	tools := []string{"read_file", "search_files", "project_overview"}

	result := gate.Evaluate("What's the project structure?", tools)
	if result.Decision != ToolDecisionInclude {
		t.Errorf("expected Include for project structure question, got %v", result.Decision)
	}
}

func TestToolRelevanceGate_IncludeSearch(t *testing.T) {
	gate := NewToolRelevanceGate(true)
	tools := []string{"read_file", "search_files"}

	result := gate.Evaluate("Search for TODO in the codebase", tools)
	if result.Decision != ToolDecisionInclude {
		t.Errorf("expected Include for search request, got %v", result.Decision)
	}
}

func TestToolRelevanceGate_SubsetGitOnly(t *testing.T) {
	gate := NewToolRelevanceGate(true)
	tools := []string{"read_file", "search_files", "git_status", "git_log", "git_diff"}

	result := gate.Evaluate("Can you check git status?", tools)
	if result.Decision != ToolDecisionSubset {
		t.Errorf("expected Subset for git-only request, got %v", result.Decision)
	}
	if len(result.ToolFilter) == 0 {
		t.Error("expected ToolFilter to contain git tools")
	}
	for _, tName := range result.ToolFilter {
		if tName != "git_status" && tName != "git_log" && tName != "git_diff" {
			t.Errorf("expected only git tools, got %s", tName)
		}
	}
}

func TestToolRelevanceGate_IncludeGreetingWithKeyword(t *testing.T) {
	gate := NewToolRelevanceGate(true)
	tools := []string{"read_file", "search_files"}

	// "Hey! Also can you read the config?" should Include because it has "read"
	result := gate.Evaluate("Hey! Also can you read the config?", tools)
	if result.Decision != ToolDecisionInclude {
		t.Errorf("expected Include for greeting + keyword, got %v", result.Decision)
	}
}

func TestToolRelevanceGate_IncludeFilePath(t *testing.T) {
	gate := NewToolRelevanceGate(true)
	tools := []string{"read_file", "search_files"}

	result := gate.Evaluate("Look at /Users/ema/projects/repos/prizm/main.go", tools)
	if result.Decision != ToolDecisionInclude {
		t.Errorf("expected Include for message with file path, got %v", result.Decision)
	}
}

func TestToolRelevanceGate_ExcludeOpinionQuestion(t *testing.T) {
	gate := NewToolRelevanceGate(true)
	tools := []string{"read_file", "search_files"}

	result := gate.Evaluate("What do you think about Rust as a language?", tools)
	if result.Decision != ToolDecisionExclude {
		t.Errorf("expected Exclude for opinion question, got %v (reason: %s)", result.Decision, result.Reason)
	}
}

func TestToolRelevanceGate_ConservativeDefault(t *testing.T) {
	gate := NewToolRelevanceGate(true)
	tools := []string{"read_file", "search_files"}

	// Ambiguous message — conservative default should include
	result := gate.Evaluate("Can you check that for me?", tools)
	if result.Decision != ToolDecisionInclude {
		t.Errorf("expected Include (conservative) for ambiguous message, got %v", result.Decision)
	}
}

func TestToolRelevanceGate_Disabled(t *testing.T) {
	gate := NewToolRelevanceGate(false)
	tools := []string{"read_file", "search_files"}

	result := gate.Evaluate("Hey!", tools)
	if result.Decision != ToolDecisionInclude {
		t.Errorf("expected Include when gate disabled, got %v", result.Decision)
	}
}

func TestToolRelevanceGate_IncludeFactualQuestion(t *testing.T) {
	gate := NewToolRelevanceGate(true)
	tools := []string{"read_file", "search_files", "project_overview"}

	result := gate.Evaluate("What does the project module do?", tools)
	if result.Decision != ToolDecisionInclude {
		t.Errorf("expected Include for factual question about code, got %v", result.Decision)
	}
}

func TestToolRelevanceGate_ExcludeAck(t *testing.T) {
	gate := NewToolRelevanceGate(true)
	tools := []string{"read_file", "search_files"}

	result := gate.Evaluate("Got it, thanks!", tools)
	if result.Decision != ToolDecisionExclude {
		t.Errorf("expected Exclude for acknowledgment, got %v (reason: %s)", result.Decision, result.Reason)
	}
}
func TestToolRelevanceGate_IncludeReferenceImageRequest(t *testing.T) {
	gate := NewToolRelevanceGate(true)
	tools := []string{"collect_reference_images", "web_search"}

	result := gate.Evaluate("collect reference images for a neon city", tools)
	if result.Decision != ToolDecisionInclude {
		t.Errorf("expected Include for reference image request, got %v (reason: %s)", result.Decision, result.Reason)
	}
}
