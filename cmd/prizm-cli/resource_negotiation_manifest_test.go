package main

import (
	"encoding/json"
	"testing"

	"github.com/emaharmony/prizm/internal/workflow/multiagent"
)

func TestReferenceManifestRoundTripsResourceNegotiationScope(t *testing.T) {
	want := &multiagent.ResourceNegotiationConfig{
		Enabled: true, InitialAllowance: 1, Increment: 1, HardCeiling: 2, OwnerID: "synthetic-owner",
		ParentRunID: "parent-roundtrip", AgentID: "agent-roundtrip", ProjectID: "project-roundtrip",
		Workspace: "C:/synthetic/workspace", GrantGeneration: 9,
	}
	encoded, err := json.Marshal(referenceWorkflowManifest{RunID: "child-roundtrip", ResourceNegotiation: want})
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	var got referenceWorkflowManifest
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatalf("unmarshal manifest: %v", err)
	}
	if got.ResourceNegotiation == nil || *got.ResourceNegotiation != *want {
		t.Fatalf("resource negotiation config after manifest restart = %#v, want %#v", got.ResourceNegotiation, want)
	}
}

func TestResourceWaitRecoveryClearsResumeClaimForLaterOwnerDecision(t *testing.T) {
	resuming := map[string]bool{"child-race": true}
	finishedCalls := 0
	closeCalls := 0
	err := finishResourceWaitRecovery(func() error {
		closeCalls++
		return nil
	}, func() {
		finishedCalls++
		delete(resuming, "child-race")
	})
	if err != nil || closeCalls != 1 || finishedCalls != 1 || resuming["child-race"] {
		t.Fatalf("resource wait recovery cleanup err=%v closes=%d finished=%d claim=%v", err, closeCalls, finishedCalls, resuming["child-race"])
	}
	if resuming["child-race"] {
		t.Fatal("later owner decision cannot claim child after pending wait releases recovery marker")
	}
	resuming["child-race"] = true
}
