package main

import "testing"

func TestEncodeDecodeFeedbackButtonID(t *testing.T) {
	id := encodeFeedbackButtonID("pre", "approve", "gl-123")
	if id != "prizmfb:pre:approve:gl-123" {
		t.Fatalf("unexpected id: %q", id)
	}
	gate, action, runID, ok := decodeFeedbackButtonID(id)
	if !ok || gate != "pre" || action != "approve" || runID != "gl-123" {
		t.Fatalf("decode mismatch: %s/%s/%s ok=%v", gate, action, runID, ok)
	}
}

func TestDecodeFeedbackButtonIDRejectsForeign(t *testing.T) {
	for _, bad := range []string{"", "other:pre:approve:gl-1", "prizmfb:pre:approve", "prizmfb::approve:gl-1", "prizmfb:pre:approve:"} {
		if _, _, _, ok := decodeFeedbackButtonID(bad); ok {
			t.Fatalf("expected %q to be rejected", bad)
		}
	}
}

func TestBuildFeedbackButtons(t *testing.T) {
	pre := buildFeedbackButtons("FEEDBACK_PRE", "gl-1")
	if len(pre) != 3 { // approve, changes, reject
		t.Fatalf("pre gate should have 3 buttons, got %d", len(pre))
	}
	post := buildFeedbackButtons("FEEDBACK_POST", "gl-1")
	if len(post) != 2 { // approve, changes (no reject)
		t.Fatalf("post gate should have 2 buttons, got %d", len(post))
	}
	if buildFeedbackButtons("EXECUTION", "gl-1") != nil {
		t.Fatal("non-feedback phase should have no buttons")
	}
	// Each button's custom ID round-trips.
	for _, b := range pre {
		if _, _, runID, ok := decodeFeedbackButtonID(b.CustomID); !ok || runID != "gl-1" {
			t.Fatalf("button id does not round-trip: %q", b.CustomID)
		}
	}
}

func TestFeedbackButtonPayload(t *testing.T) {
	// pre/approve → feedback_response/approved
	p, ok := feedbackButtonPayload("prizmfb:pre:approve:gl-9", "ema")
	if !ok || p["type"] != "feedback_response" || p["decision"] != "approved" || p["workflow_id"] != "gl-9" || p["reviewer"] != "ema" {
		t.Fatalf("pre/approve payload wrong: %v ok=%v", p, ok)
	}
	// post/changes → review_response/changes_requested
	p, ok = feedbackButtonPayload("prizmfb:post:changes:gl-9", "")
	if !ok || p["type"] != "review_response" || p["decision"] != "changes_requested" || p["reviewer"] != "discord" {
		t.Fatalf("post/changes payload wrong: %v ok=%v", p, ok)
	}
	// pre/reject → rejected
	if p, ok = feedbackButtonPayload("prizmfb:pre:reject:gl-9", "x"); !ok || p["decision"] != "rejected" {
		t.Fatalf("reject payload wrong: %v ok=%v", p, ok)
	}
}

func TestFeedbackButtonPayloadGuards(t *testing.T) {
	// foreign id
	if _, ok := feedbackButtonPayload("other:pre:approve:gl-1", "x"); ok {
		t.Fatal("foreign id should be rejected")
	}
	// non-gated-loop run id
	if _, ok := feedbackButtonPayload("prizmfb:pre:approve:run-1", "x"); ok {
		t.Fatal("non gl- run id should be rejected")
	}
	// unknown action
	if _, ok := feedbackButtonPayload("prizmfb:pre:explode:gl-1", "x"); ok {
		t.Fatal("unknown action should be rejected")
	}
}
