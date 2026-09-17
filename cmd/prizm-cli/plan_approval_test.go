package main

import (
	"testing"

	"github.com/emaharmony/prizm/internal/adapter/builtin/discordbot"
	"github.com/emaharmony/prizm/internal/orchestrator"
	"github.com/emaharmony/prizm/internal/plan"
	"os"
	"path/filepath"
)

func TestHandlePlanApproval_BasicApprove(t *testing.T) {
	tmpDir := t.TempDir()
	pm := plan.NewManager(tmpDir)
	if err := pm.EnsureDir(); err != nil {
		t.Fatal(err)
	}

	// Create a plan that needs approval
	p := plan.Plan{
		Title:         "Test plan",
		Description:   "A test plan",
		ApprovalLevel: plan.ApprovalRequired,
		Status:        plan.StatusPendingApproval,
	}
	if err := pm.CreatePlan(p); err != nil {
		t.Fatal(err)
	}
	plans, _ := pm.LoadPlans()
	planID := plans[0].ID

	cfg := &orchestrator.Config{
		ChannelRoles: []orchestrator.ChannelRole{
			{ID: "manager-room-id", Role: "manager-room", Tools: "all"},
		},
	}

	cc := &conversationContext{
		cfg:     cfg,
		planMgr: pm,
	}

	msg := &discordbot.InboundMessage{
		Content:   "approve " + planID,
		ChannelID: "manager-room-id",
		UserName:  "ema",
		UserID:    "164169326142816256",
	}

	handled := cc.handlePlanApproval(msg)
	if !handled {
		t.Error("expected plan approval to be handled")
	}

	// Verify plan is now approved
	plans, _ = pm.LoadPlans()
	if plans[0].Status != plan.StatusApproved {
		t.Errorf("expected plan status %s, got %s", plan.StatusApproved, plans[0].Status)
	}
}

func TestHandlePlanApproval_BasicReject(t *testing.T) {
	tmpDir := t.TempDir()
	pm := plan.NewManager(tmpDir)
	if err := pm.EnsureDir(); err != nil {
		t.Fatal(err)
	}

	p := plan.Plan{
		Title:         "Test plan",
		Description:   "A test plan",
		ApprovalLevel: plan.ApprovalRequired,
		Status:        plan.StatusPendingApproval,
	}
	if err := pm.CreatePlan(p); err != nil {
		t.Fatal(err)
	}
	plans, _ := pm.LoadPlans()
	planID := plans[0].ID

	cfg := &orchestrator.Config{
		ChannelRoles: []orchestrator.ChannelRole{
			{ID: "manager-room-id", Role: "manager-room", Tools: "all"},
		},
	}

	cc := &conversationContext{
		cfg:     cfg,
		planMgr: pm,
	}

	msg := &discordbot.InboundMessage{
		Content:   "reject " + planID,
		ChannelID: "manager-room-id",
		UserName:  "ema",
		UserID:    "164169326142816256",
	}

	handled := cc.handlePlanApproval(msg)
	if !handled {
		t.Error("expected plan rejection to be handled")
	}

	plans, _ = pm.LoadPlans()
	if plans[0].Status != plan.StatusAbandoned {
		t.Errorf("expected plan status %s, got %s", plan.StatusAbandoned, plans[0].Status)
	}
}

func TestHandlePlanApproval_NonManagerChannel(t *testing.T) {
	tmpDir := t.TempDir()
	pm := plan.NewManager(tmpDir)
	if err := pm.EnsureDir(); err != nil {
		t.Fatal(err)
	}

	p := plan.Plan{
		Title:         "Test plan",
		ApprovalLevel: plan.ApprovalRequired,
		Status:        plan.StatusPendingApproval,
	}
	if err := pm.CreatePlan(p); err != nil {
		t.Fatal(err)
	}
	plans, _ := pm.LoadPlans()
	planID := plans[0].ID

	cfg := &orchestrator.Config{
		ChannelRoles: []orchestrator.ChannelRole{
			{ID: "fun-room-id", Role: "fun", Tools: "none"},
		},
	}

	cc := &conversationContext{
		cfg:     cfg,
		planMgr: pm,
	}

	msg := &discordbot.InboundMessage{
		Content:   "approve " + planID,
		ChannelID: "fun-room-id",
		UserName:  "random_user",
		UserID:    "999",
	}

	handled := cc.handlePlanApproval(msg)
	if handled {
		t.Error("plan approval should be rejected in non-manager channel")
	}

	// Verify plan is still pending
	plans, _ = pm.LoadPlans()
	if plans[0].Status != plan.StatusPendingApproval {
		t.Errorf("expected plan status %s, got %s", plan.StatusPendingApproval, plans[0].Status)
	}
}

func TestHandlePlanApproval_InvalidFormat(t *testing.T) {
	tmpDir := t.TempDir()
	pm := plan.NewManager(tmpDir)
	_ = pm.EnsureDir()

	cfg := &orchestrator.Config{
		ChannelRoles: []orchestrator.ChannelRole{
			{ID: "manager-room-id", Role: "manager-room", Tools: "all"},
		},
	}

	cc := &conversationContext{
		cfg:     cfg,
		planMgr: pm,
	}

	tests := []struct {
		content string
		handled bool
	}{
		{"hello world", false},             // not an approval command
		{"approve", false},                 // no plan ID
		{"approve X-001", false},           // wrong prefix
		{"approve P-001 extra text", true}, // extra fields but valid prefix — should handle
	}

	for _, tt := range tests {
		msg := &discordbot.InboundMessage{
			Content:   tt.content,
			ChannelID: "manager-room-id",
			UserName:  "ema",
			UserID:    "164169326142816256",
		}
		handled := cc.handlePlanApproval(msg)
		if handled != tt.handled {
			t.Errorf("handlePlanApproval(%q) = %v, want %v", tt.content, handled, tt.handled)
		}
	}
	_ = filepath.Join(tmpDir, "test") // use tmpDir
}

func TestHandlePlanApproval_NonexistentPlan(t *testing.T) {
	tmpDir := t.TempDir()
	pm := plan.NewManager(tmpDir)
	_ = pm.EnsureDir()

	cfg := &orchestrator.Config{
		ChannelRoles: []orchestrator.ChannelRole{
			{ID: "manager-room-id", Role: "manager-room", Tools: "all"},
		},
	}

	cc := &conversationContext{
		cfg:     cfg,
		planMgr: pm,
	}

	msg := &discordbot.InboundMessage{
		Content:   "approve P-999",
		ChannelID: "manager-room-id",
		UserName:  "ema",
		UserID:    "164169326142816256",
	}

	// Should return true (command was recognized) even though plan doesn't exist
	// The error message will be sent to Discord by the handler
	handled := cc.handlePlanApproval(msg)
	if !handled {
		t.Error("expected nonexistent plan approval to be handled (with error response)")
	}
}

// Ensure tmpDir is used
func init() {
	_ = os.ReadFile
	_ = filepath.Join
}
