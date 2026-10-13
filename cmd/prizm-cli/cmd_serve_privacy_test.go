package main

import (
	"strings"
	"testing"
)

func TestSanitizeServeResponseRemovesPrivateReasoning(t *testing.T) {
	got, err := sanitizeServeResponse("<ThInK>PRIVATE_CANARY</tHiNk>Public answer.")
	if err != nil || got != "Public answer." || strings.Contains(got, "PRIVATE_CANARY") {
		t.Fatalf("unexpected public response: %q err=%v", got, err)
	}
}

func TestSanitizeServeResponseRejectsIncompleteReasoning(t *testing.T) {
	for _, input := range []string{"<think>PRIVATE_CANARY", "Public. <think>PRIVATE_CANARY"} {
		got, err := sanitizeServeResponse(input)
		if err == nil || got != "" || strings.Contains(err.Error(), "PRIVATE_CANARY") {
			t.Fatalf("expected privacy-safe failure, got=%q err=%v", got, err)
		}
	}
}

func TestSanitizeServeResponsePreservesFencedPatch(t *testing.T) {
	input := "<think>PRIVATE_CANARY</think>Apply this patch:\n```diff\n+literal <think>patch text</think>\n```"
	got, err := sanitizeServeResponse(input)
	want := "Apply this patch:\n```diff\n+literal <think>patch text</think>\n```"
	if err != nil || got != want || strings.Contains(got, "PRIVATE_CANARY") {
		t.Fatalf("fenced patch changed or private text remained: %q err=%v", got, err)
	}
}
