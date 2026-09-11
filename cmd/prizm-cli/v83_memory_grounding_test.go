package main

import (
	"strings"
	"testing"
	"time"

	"github.com/emaharmony/prizm/internal/memory"
)

func TestFormatMemories_GroundingHeader(t *testing.T) {
	memories := []memory.Memory{
		{
			ID:        "test-1",
			Summary:   "Lumi's model is glm-5.1:cloud",
			Content:   "Current model stack: glm-5.1:cloud for Lumi",
			Category:  "fact",
			Source:    "lumi",
			CreatedAt: time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC),
		},
	}

	result := formatMemories(memories, "Relevant Memories", 300)

	if !strings.Contains(result, "TRUST THEM over your general training knowledge") {
		t.Error("formatMemories should include grounding instruction to trust memories over training knowledge")
	}
	if !strings.Contains(result, "verified local storage") {
		t.Error("formatMemories should reference verified local storage")
	}
}

func TestFormatMemories_GroundingFooter(t *testing.T) {
	memories := []memory.Memory{
		{
			ID:        "test-1",
			Summary:   "Test memory",
			Content:   "Test content",
			Category:  "fact",
			CreatedAt: time.Now(),
		},
	}

	result := formatMemories(memories, "Relevant Memories", 300)

	if !strings.Contains(result, "check these memories FIRST") {
		t.Error("formatMemories should include grounding footer with 'check these memories FIRST'")
	}
	if !strings.Contains(result, "I don't have that in my records") {
		t.Error("formatMemories should include anti-hallucination instruction to say 'I don't have that in my records'")
	}
}

func TestFormatMemories_DateStampAndConfidence(t *testing.T) {
	memories := []memory.Memory{
		{
			ID:        "test-1",
			Summary:   "Lumi's model is glm-5.1:cloud",
			Content:   "Current model: glm-5.1:cloud",
			Category:  "fact",
			Source:    "lumi",
			CreatedAt: time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC),
			Metadata:  map[string]string{"confidence": "9.0"},
		},
	}

	result := formatMemories(memories, "Relevant Memories", 300)

	if !strings.Contains(result, "2026-09-10") {
		t.Error("formatMemories should include date stamp")
	}
	if !strings.Contains(result, "high") {
		t.Error("formatMemories should include confidence label 'high' for confidence >= 8.0")
	}
	if !strings.Contains(result, "lumi") {
		t.Error("formatMemories should include source label")
	}
}

func TestFormatMemories_SupersededLabel(t *testing.T) {
	memories := []memory.Memory{
		{
			ID:        "old-model",
			Summary:   "Lumi uses qwen3-coder:480b-cloud",
			Content:   "Old model assignment",
			Category:  "fact",
			Source:    "lumi",
			CreatedAt: time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC),
			Metadata:  map[string]string{"superseded_by": "2026-09-10"},
		},
	}

	result := formatMemories(memories, "Relevant Memories", 300)

	if !strings.Contains(result, "SUPERSEDED") {
		t.Error("formatMemories should mark superseded memories with SUPERSEDED label")
	}
	if !strings.Contains(result, "no longer current") {
		t.Error("formatMemories should include 'no longer current' for superseded memories")
	}
}

func TestFormatMemories_ConfidenceLabels(t *testing.T) {
	tests := []struct {
		confidence string
		source     string
		want       string
	}{
		{"9.0", "", "high"},
		{"7.5", "", "medium"},
		{"3.0", "", "low"},
		{"", "user_stated", "high"},
		{"", "lumi", "high"},
		{"", "", "medium"},
	}

	for _, tc := range tests {
		m := memory.Memory{
			ID:        "test",
			Summary:   "Test",
			Content:   "Test content",
			Category:  "fact",
			Source:    tc.source,
			CreatedAt: time.Now(),
		}
		if tc.confidence != "" {
			m.Metadata = map[string]string{"confidence": tc.confidence}
		}
		got := confidenceLabel(m)
		if got != tc.want {
			t.Errorf("confidenceLabel(conf=%q, source=%q) = %q, want %q", tc.confidence, tc.source, got, tc.want)
		}
	}
}

func TestCoreIdentityBlock_Build(t *testing.T) {
	cib := NewCoreIdentityBlock("")

	result := cib.Build(nil)

	if !strings.Contains(result, "Core Identity Facts") {
		t.Error("CoreIdentityBlock should include 'Core Identity Facts' section")
	}
	if !strings.Contains(result, "Your name is Lumi") {
		t.Error("CoreIdentityBlock should state the agent's name")
	}
	if !strings.Contains(result, "glm-5.1:cloud") {
		t.Error("CoreIdentityBlock should include current model")
	}
	if !strings.Contains(result, "SUPERSDED") && !strings.Contains(result, "SUPERSeded") {
		// Check for the warning about superseded models
		if !strings.Contains(result, "Do NOT reference qwen3-coder") {
			t.Error("CoreIdentityBlock should warn against referencing superseded models")
		}
	}
	if !strings.Contains(result, "Memory Grounding Rules") {
		t.Error("CoreIdentityBlock should include 'Memory Grounding Rules' section")
	}
	if !strings.Contains(result, "TRUST YOUR MEMORIES") {
		t.Error("CoreIdentityBlock should instruct the model to trust memories over training knowledge")
	}
	if !strings.Contains(result, "I don't have that in my records") {
		t.Error("CoreIdentityBlock should include anti-hallucination instruction")
	}
}

func TestCoreIdentityBlock_Caching(t *testing.T) {
	cib := NewCoreIdentityBlock("")

	// First build
	result1 := cib.Build(nil)
	// Second build should return cached
	result2 := cib.Build(nil)

	if result1 != result2 {
		t.Error("Cached build should return identical result")
	}
}

func TestCoreIdentityBlock_Invalidate(t *testing.T) {
	cib := NewCoreIdentityBlock("")

	// Build and cache
	_ = cib.Build(nil)

	// Invalidate
	cib.Invalidate()

	// The cache should be cleared — next Build() will rebuild
	// (We can't easily test that it actually rebuilt, but we can verify it still works)
	result := cib.Build(nil)
	if result == "" {
		t.Error("Build after Invalidate should still produce output")
	}
}