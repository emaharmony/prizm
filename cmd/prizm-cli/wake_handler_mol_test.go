package main

import (
	"strings"
	"testing"
	"time"

	"github.com/emaharmony/prizm/internal/orchestrator"
	"github.com/emaharmony/prizm/internal/plan"
	"github.com/emaharmony/prizm/internal/session"
)

func TestMolStatusReport_BasicOutput(t *testing.T) {
	cfg := &orchestrator.Config{
		Prizm: orchestrator.PrizmConfig{
			InstanceID: "test-prizm",
			TTS: orchestrator.TTSConfig{
				Enabled: true,
			},
			Scheduler: orchestrator.SchedulerConfig{
				Jobs: []orchestrator.SchedulerJobConfig{
					{Name: "mol-status", Schedule: "0 * * * *", Enabled: true},
					{Name: "status-report", Schedule: "0 */2 * * *", Enabled: false},
				},
			},
		},
		Agents: []orchestrator.AgentConfig{
			{ID: "lumi", Role: "lead", Model: "glm-5.1:cloud"},
			{ID: "mango", Role: "coder", Model: "deepseek-v4-flash:cloud"},
		},
	}

	sessMgr, err := session.NewManager(t.TempDir()+"/sessions.db", 100, 3600*time.Second, 0, "summarize")
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	planDir := t.TempDir()
	planMgr := plan.NewManager(planDir)
	if err := planMgr.EnsureDir(); err != nil {
		t.Fatalf("EnsureDir: %v", err)
	}

	wh := &WakeHandler{
		cfg:       cfg,
		sessMgr:   sessMgr,
		planMgr:   planMgr,
		remClient: nil,
	}

	report := wh.molStatusReport()

	// Verify report contains expected sections
	if !strings.Contains(report, "MOL Self-Status") {
		t.Error("expected 'MOL Self-Status' in report")
	}
	if !strings.Contains(report, "lumi (lead)") {
		t.Error("expected 'lumi (lead)' in report")
	}
	if !strings.Contains(report, "Agents:** 2") {
		t.Error("expected 'Agents:** 2' in report")
	}
	if !strings.Contains(report, "Recall not connected") {
		t.Error("expected 'Recall not connected' in report")
	}
	if !strings.Contains(report, "TTS: on") {
		t.Error("expected 'TTS: on' in report")
	}
	if !strings.Contains(report, "Cron jobs: 1") {
		t.Error("expected 'Cron jobs: 1' in report")
	}
}

func TestMolStatusReport_WithPlans(t *testing.T) {
	tmpDir := t.TempDir()
	planMgr := plan.NewManager(tmpDir)
	if err := planMgr.EnsureDir(); err != nil {
		t.Fatalf("EnsureDir: %v", err)
	}

	err := planMgr.CreatePlan(plan.Plan{
		Title:     "Test feature",
		Reasoning: "We need this",
		Scope:     "Just the feature",
	})
	if err != nil {
		t.Fatalf("CreatePlan: %v", err)
	}

	cfg := &orchestrator.Config{
		Prizm: orchestrator.PrizmConfig{
			InstanceID: "plan-test",
		},
	}

	wh := &WakeHandler{
		cfg:     cfg,
		planMgr: planMgr,
	}

	report := wh.molStatusReport()

	if !strings.Contains(report, "1 active") {
		t.Errorf("expected 1 active plan, got:\n%s", report)
	}
	if !strings.Contains(report, "auto-proceed") {
		t.Errorf("expected auto-proceed count, got:\n%s", report)
	}
}

func TestMolStatusReport_NoConfig(t *testing.T) {
	wh := &WakeHandler{}

	report := wh.molStatusReport()

	if !strings.Contains(report, "MOL Self-Status") {
		t.Errorf("expected default title, got:\n%s", report)
	}
}