package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/emaharmony/prizm/internal/claudeworker"
)

func TestReviewerEventPayloadExcludesPrivateStdout(t *testing.T) {
	verdict := claudeworker.ParseVerdict("<think>PRIVATE_CANARY</think>\nVERDICT: approved")
	payload := reviewerPayload("FEEDBACK_POST", "run-test", "claude", verdict)
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "PRIVATE_CANARY") {
		t.Fatalf("private reviewer output leaked into event: %s", encoded)
	}
	if payload["decision"] != "approved" || payload["notes"] != "Review approved." {
		t.Fatalf("concise public decision summary missing: %#v", payload)
	}
}
