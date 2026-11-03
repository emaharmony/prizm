package main

import (
	"strings"
	"testing"

	"github.com/emaharmony/prizm/internal/workflow/multiagent"
)

func TestFormatFreewillNotificationProposedFallback(t *testing.T) {
	out, btns := formatFreewillNotification(multiagent.FreewillNotification{
		EventType: "prism.freewill.approval.proposed", RunID: "freewill_abc",
		Payload: map[string]any{"approval_ref": "appr_123"},
	}, nil)
	if !strings.Contains(out, "Freewill proposal") || !strings.Contains(out, "appr_123") {
		t.Fatalf("expected fallback proposal message, got %q", out)
	}
	if btns != nil {
		t.Fatal("no allowlist -> no buttons")
	}
}

func TestFormatFreewillNotificationProposedEnriched(t *testing.T) {
	out, btns := formatFreewillNotification(multiagent.FreewillNotification{
		EventType: "prism.freewill.approval.proposed", RunID: "freewill_abc",
		Payload: map[string]any{
			"selected_title": "Tag songs", "selected_score": 74.0,
			"agent_id": "lumi-selector", "project_id": "bassbook",
			"workspace":    "/Users/ema/projects/repos/BassBook",
			"approval_ref": "appr_9", "choice_id": "cand-3",
			"candidates": []any{
				map[string]any{"candidate_id": "cand-1", "title": "Practice timer", "score": 82.0},
				map[string]any{"candidate_id": "cand-3", "title": "Tag songs", "score": 74.0,
					"reason": "Improves navigation Requires a small schema migration Moderately reviewable"},
			},
		},
	}, []string{"164169326142816256"})
	for _, want := range []string{
		"Tag songs", "74/100", "lumi-selector", "bassbook",
		"Project:", "Why (selector reasoning)", "Improves navigation",
		"Alternatives considered", "Practice timer (82)", "decision-only",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("card missing %q:\n%s", want, out)
		}
	}
	if len(btns) != 2 {
		t.Fatalf("allowlist set must render 2 buttons, got %d", len(btns))
	}
}

func TestFwBulletsSplitsClauses(t *testing.T) {
	got := fwBullets("Improves navigation for libraries Requires a small migration but is backward-compatible Moderate scope")
	if !strings.Contains(got, "- Improves navigation") || !strings.Contains(got, "- Requires a small migration") || !strings.Contains(got, "- Moderate scope") {
		t.Fatalf("expected clause bullets, got:\n%s", got)
	}
}

func TestFormatFreewillNotificationDecided(t *testing.T) {
	out, _ := formatFreewillNotification(multiagent.FreewillNotification{
		EventType: "prism.freewill.approval.decided", RunID: "freewill_abc",
		Payload: map[string]any{"decision": "deny", "selected_title": "Tag songs"},
	}, nil)
	if !strings.Contains(out, "deny") {
		t.Fatalf("expected decision message, got %q", out)
	}
}

func TestFormatFreewillNotificationChattyIsEmpty(t *testing.T) {
	if out, _ := formatFreewillNotification(multiagent.FreewillNotification{
		EventType: "prism.freewill.wake.claimed", RunID: "r", Payload: map[string]any{},
	}, nil); out != "" {
		t.Fatalf("chatty types must not notify, got %q", out)
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
	if b := buttonsFor(nil, "appr_1", "run_1"); b != nil {
		t.Fatal("empty allowlist must render no buttons")
	}
	e2 := []string{"164169326142816256"}
	if b := buttonsFor(e2, "appr_1", "run_1"); len(b) != 2 {
		t.Fatalf("allowlist set must render 2 buttons, got %d", len(b))
	}
	if b := buttonsFor(e2, "", "run_1"); b != nil {
		t.Fatal("missing approval id must render no buttons")
	}
}
