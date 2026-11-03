package main

import (
	"strings"
	"testing"

	"github.com/emaharmony/prizm/internal/event"
)

func TestFormatFreewillEventProposedWithoutStore(t *testing.T) {
	e := &freewillRunEnricher{}
	out := formatFreewillEvent(event.EventFreewillApprovalProposed, []byte(`{"approval_id":"appr_123","run_id":"freewill_abc"}`), e)
	if !strings.Contains(out, "Freewill proposal") || !strings.Contains(out, "appr_123") {
		t.Fatalf("expected fallback proposal message, got %q", out)
	}
}

func TestFormatFreewillEventDecided(t *testing.T) {
	e := &freewillRunEnricher{}
	out := formatFreewillEvent(event.EventFreewillApprovalDecided, []byte(`{"decision":"deny","run_id":"freewill_abc"}`), e)
	if !strings.Contains(out, "deny") {
		t.Fatalf("expected decision message, got %q", out)
	}
}

func TestFormatFreewillEventInvalidJSON(t *testing.T) {
	e := &freewillRunEnricher{}
	if out := formatFreewillEvent(event.EventFreewillApprovalProposed, []byte(`not-json`), e); out != "" {
		t.Fatalf("expected empty output for invalid payload, got %q", out)
	}
}

func TestFormatFreewillEventUnsubscribeUnknownSubject(t *testing.T) {
	e := &freewillRunEnricher{}
	if out := formatFreewillEvent("prism.freewill.wake.claimed", []byte(`{}`), e); out != "" {
		t.Fatalf("chatty subjects must not notify, got %q", out)
	}
}
