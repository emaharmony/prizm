package main

import (
	"strings"
	"testing"

	"github.com/emaharmony/prizm/internal/remembrance"
)

func TestRemembranceMemoryBlock_StructuredWithScores(t *testing.T) {
	ctx := &remembrance.ContextPackResponse{
		ContextJSON: &remembrance.ContextDetail{
			Memories: []remembrance.ContextMemory{
				{
					Title:   "BassBook schema",
					Summary: "The TalentProfile table has columns for id, userId, displayName...",
					Score:   0.85,
					Reason:  "strong keyword match",
				},
				{
					Title:   "Prizm config",
					Summary: "The prizm.yaml file configures agents and channels...",
					Score:   0.42,
					Reason:  "partial keyword overlap",
				},
			},
		},
	}

	block := remembranceMemoryBlock(ctx)
	if block == "" {
		t.Fatal("expected non-empty block")
	}
	if !strings.Contains(block, "score: 0.85") {
		t.Error("expected high score to be visible")
	}
	if !strings.Contains(block, "score: 0.42") {
		t.Error("expected lower score to be visible")
	}
	if !strings.Contains(block, "strong keyword match") {
		t.Error("expected reason to be visible")
	}
	// High score → no nudge
	if strings.Contains(block, "⚠️") {
		t.Error("expected no low-confidence nudge when scores are above threshold")
	}
}

func TestRemembranceMemoryBlock_LowConfidenceNudge(t *testing.T) {
	ctx := &remembrance.ContextPackResponse{
		ContextJSON: &remembrance.ContextDetail{
			Memories: []remembrance.ContextMemory{
				{
					Title:   "BassBook conversation",
					Summary: "Hey Lumi, what do you know about the Prizm project?",
					Score:   0.21,
					Reason:  "loosely related; partial keyword overlap",
				},
				{
					Title:   "Another fragment",
					Summary: "Some random auto-captured conversation...",
					Score:   0.18,
					Reason:  "loosely related",
				},
			},
		},
	}

	block := remembranceMemoryBlock(ctx)
	if block == "" {
		t.Fatal("expected non-empty block")
	}
	if !strings.Contains(block, "⚠️") {
		t.Error("expected low-confidence nudge when all scores < 0.30")
	}
	if !strings.Contains(block, "memory_search") {
		t.Error("expected nudge to mention memory_search tool")
	}
	if !strings.Contains(block, "0.21") {
		t.Error("expected best score to be visible in nudge")
	}
}

func TestRemembranceMemoryBlock_MarkdownFallback(t *testing.T) {
	ctx := &remembrance.ContextPackResponse{
		ContextMarkdown: "# Retrieved Context\nSome markdown content here.",
	}

	block := remembranceMemoryBlock(ctx)
	if block == "" {
		t.Fatal("expected non-empty block from markdown fallback")
	}
	if !strings.Contains(block, "Some markdown content here") {
		t.Error("expected markdown content to be passed through")
	}
}

func TestRemembranceMemoryBlock_NilContext(t *testing.T) {
	block := remembranceMemoryBlock(nil)
	if block != "" {
		t.Error("expected empty block for nil context")
	}
}

func TestRemembranceMemoryBlock_EmptyMemories(t *testing.T) {
	ctx := &remembrance.ContextPackResponse{
		ContextJSON: &remembrance.ContextDetail{
			Memories: []remembrance.ContextMemory{},
		},
	}

	block := remembranceMemoryBlock(ctx)
	if block != "" {
		t.Error("expected empty block for no memories")
	}
}

func TestRemembranceMemoryBlock_MixedConfidenceNoNudge(t *testing.T) {
	// When at least one result is above threshold, no nudge
	ctx := &remembrance.ContextPackResponse{
		ContextJSON: &remembrance.ContextDetail{
			Memories: []remembrance.ContextMemory{
				{
					Title:   "High relevance",
					Summary: "Directly relevant memory...",
					Score:   0.55,
					Reason:  "strong match",
				},
				{
					Title:   "Low relevance",
					Summary: "Tangentially related...",
					Score:   0.15,
					Reason:  "loosely related",
				},
			},
		},
	}

	block := remembranceMemoryBlock(ctx)
	if strings.Contains(block, "⚠️") {
		t.Error("expected no nudge when at least one score is above threshold")
	}
}