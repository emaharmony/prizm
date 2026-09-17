package factory

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/emaharmony/prizm/internal/crossprizm"
)

func TestOperatorHandleCrossPrizmTaskWritesInboxTask(t *testing.T) {
	root := t.TempDir()
	projectPath := filepath.Join(root, "roblox-project")
	if err := os.MkdirAll(projectPath, 0755); err != nil {
		t.Fatalf("create project path: %v", err)
	}

	op, err := NewOperator(Config{
		Root:               root,
		Project:            "eggventura",
		ProjectPath:        projectPath,
		ApprovalMode:       "report_only",
		RunCodex:           false,
		VisionReview:       "none",
		PlaytestMode:       "none",
		UIGenerationDryRun: true,
	})
	if err != nil {
		t.Fatalf("new operator: %v", err)
	}

	msg := crossprizm.NewMessage("lumi-ceo", "astraea-manager", crossprizm.TypeTaskRequest)
	msg.CorrelationID = "factory-smoke"
	msg.Request = map[string]any{"task": "Validate the Eggventura factory pipeline."}

	resp, err := op.HandleCrossPrizmTask(context.Background(), msg)
	if err != nil {
		t.Fatalf("handle task: %v", err)
	}
	if resp == nil || resp.Response["status"] != "accepted" {
		t.Fatalf("expected accepted response, got %#v", resp)
	}

	taskPath := filepath.Join(root, "inbox", "prizm-factory-smoke.json")
	data, err := os.ReadFile(taskPath)
	if err != nil {
		t.Fatalf("read task: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("decode task: %v", err)
	}
	if payload["approval_mode"] != "report_only" {
		t.Fatalf("approval_mode = %v", payload["approval_mode"])
	}
	if payload["run_codex"] != false {
		t.Fatalf("run_codex = %v", payload["run_codex"])
	}
	if payload["project_path"] != projectPath {
		t.Fatalf("project_path = %v", payload["project_path"])
	}

	requestPath := filepath.Join(root, "prompts", "prizm-factory-smoke.md")
	body, err := os.ReadFile(requestPath)
	if err != nil {
		t.Fatalf("read request: %v", err)
	}
	if string(body) == "" {
		t.Fatal("expected request markdown")
	}
}
