package main

import (
	"strings"
	"testing"

	"github.com/emaharmony/prizm/internal/event"
)

func TestFormatFreewillEventProposedWithoutStore(t *testing.T) {
	e := &freewillRunEnricher{}
	out, _ := formatFreewillEvent(event.EventFreewillApprovalProposed, []byte(`{"approval_id":"appr_123","run_id":"freewill_abc"}`), e)
	if !strings.Contains(out, "Freewill proposal") || !strings.Contains(out, "appr_123") {
		t.Fatalf("expected fallback proposal message, got %q", out)
	}
}

func TestFormatFreewillEventDecided(t *testing.T) {
	e := &freewillRunEnricher{}
	out, _ := formatFreewillEvent(event.EventFreewillApprovalDecided, []byte(`{"decision":"deny","run_id":"freewill_abc"}`), e)
	if !strings.Contains(out, "deny") {
		t.Fatalf("expected decision message, got %q", out)
	}
}

func TestFormatFreewillEventInvalidJSON(t *testing.T) {
	e := &freewillRunEnricher{}
	if out, _ := formatFreewillEvent(event.EventFreewillApprovalProposed, []byte(`not-json`), e); out != "" {
		t.Fatalf("expected empty output for invalid payload, got %q", out)
	}
}

func TestFormatFreewillEventUnsubscribeUnknownSubject(t *testing.T) {
	e := &freewillRunEnricher{}
	if out, _ := formatFreewillEvent("prism.freewill.wake.claimed", []byte(`{}`), e); out != "" {
		t.Fatalf("chatty subjects must not notify, got %q", out)
	}
}

func TestFreewillButtonCodec(t *testing.T) {
	id := encodeFreewillButtonID("appr_1", "freewill_run", "grant")
	a, r, act, ok := decodeFreewillButtonID(id)
	if !ok || a != "appr_1" || r != "freewill_run" || act != "grant" {
		t.Fatalf("roundtrip failed: %q -> %q %q %q %v", id, a, r, act, ok)
	}
	if _, _, _, ok := decodeFreewillButtonID("prizmapprove:a:r:approve"); ok {
		t.Fatal("prizmapprove ids must not match freewill decoder")
	}
	if _, _, _, ok := decodeFreewillButtonID("freewillbtn:a:r:approve"); ok {
		t.Fatal("invalid action must be rejected")
	}
}

func TestButtonsOnlyWithAllowlist(t *testing.T) {
	e := &freewillRunEnricher{}
	if b := e.buttonsFor("appr_1", "run_1"); b != nil {
		t.Fatal("empty allowlist must render no buttons")
	}
	e2 := &freewillRunEnricher{allowUserIDs: []string{"164169326142816256"}}
	if b := e2.buttonsFor("appr_1", "run_1"); len(b) != 2 {
		t.Fatalf("allowlist set must render 2 buttons, got %d", len(b))
	}
	if b := e2.buttonsFor("", "run_1"); b != nil {
		t.Fatal("missing approval id must render no buttons")
	}
}
