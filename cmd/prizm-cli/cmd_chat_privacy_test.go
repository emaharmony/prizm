package main

import (
	"strings"
	"testing"
)

func TestSanitizeChatResponseExcludesReasoningBeforeSessionPersistence(t *testing.T) {
	got, err := sanitizeChatResponse("<ThInK>PRIVATE_CANARY</tHiNk>Public answer.")
	if err != nil || got != "Public answer." || strings.Contains(got, "PRIVATE_CANARY") {
		t.Fatalf("unexpected public response: %q err=%v", got, err)
	}
}

func TestSanitizeChatResponseRejectsIncompleteReasoningAnywhere(t *testing.T) {
	for _, input := range []string{"<think>PRIVATE_CANARY", "Public. <think>PRIVATE_CANARY"} {
		got, err := sanitizeChatResponse(input)
		if err == nil || got != "" || strings.Contains(err.Error(), "PRIVATE_CANARY") {
			t.Fatalf("expected privacy-safe failure, got=%q err=%v", got, err)
		}
	}
}
