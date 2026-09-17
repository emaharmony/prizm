package orchestrator

import (
	"testing"
)

func TestSchedulerConfigParsing(t *testing.T) {
	yaml := `
prizm:
  scheduler:
    enabled: true
    jobs:
      - name: "daily-review"
        schedule: "0 3 * * *"
        event: "prizm.task.scheduled"
        payload:
          action: "daily_review"
        enabled: true
      - name: "weekly-consolidation"
        schedule: "0 3 * * 0"
        event: "prizm.task.scheduled"
        payload:
          action: "memory_consolidation"
        enabled: true
      - name: "disabled-job"
        schedule: "*/5 * * * *"
        event: "prizm.task.scheduled"
        payload:
          action: "test"
        enabled: false
agents:
  - id: test
    role: test
    model: test:latest
`
	cfg, err := LoadConfigFromBytes([]byte(yaml))
	if err != nil {
		t.Fatalf("LoadConfigFromBytes failed: %v", err)
	}

	if !cfg.Prizm.Scheduler.Enabled {
		t.Error("scheduler should be enabled")
	}
	if len(cfg.Prizm.Scheduler.Jobs) != 3 {
		t.Fatalf("expected 3 scheduler jobs, got %d", len(cfg.Prizm.Scheduler.Jobs))
	}

	// Check first job
	job1 := cfg.Prizm.Scheduler.Jobs[0]
	if job1.Name != "daily-review" {
		t.Errorf("job1.Name = %q, want %q", job1.Name, "daily-review")
	}
	if job1.Schedule != "0 3 * * *" {
		t.Errorf("job1.Schedule = %q, want %q", job1.Schedule, "0 3 * * *")
	}
	if job1.Event != "prizm.task.scheduled" {
		t.Errorf("job1.Event = %q, want %q", job1.Event, "prizm.task.scheduled")
	}
	if !job1.Enabled {
		t.Error("job1 should be enabled")
	}
	if job1.Payload["action"] != "daily_review" {
		t.Errorf("job1.Payload[\"action\"] = %v, want %q", job1.Payload["action"], "daily_review")
	}

	// Check second job
	job2 := cfg.Prizm.Scheduler.Jobs[1]
	if job2.Name != "weekly-consolidation" {
		t.Errorf("job2.Name = %q, want %q", job2.Name, "weekly-consolidation")
	}
	if job2.Schedule != "0 3 * * 0" {
		t.Errorf("job2.Schedule = %q, want %q", job2.Schedule, "0 3 * * 0")
	}

	// Check disabled job
	job3 := cfg.Prizm.Scheduler.Jobs[2]
	if job3.Name != "disabled-job" {
		t.Errorf("job3.Name = %q, want %q", job3.Name, "disabled-job")
	}
	if job3.Enabled {
		t.Error("job3 should be disabled")
	}
}

func TestSchedulerConfigDefaults(t *testing.T) {
	// Config without scheduler — should default to disabled
	yaml := `
prizm:
  workspace: "/tmp/test"
agents:
  - id: test
    role: test
    model: test:latest
`
	cfg, err := LoadConfigFromBytes([]byte(yaml))
	if err != nil {
		t.Fatalf("LoadConfigFromBytes failed: %v", err)
	}

	if cfg.Prizm.Scheduler.Enabled {
		t.Error("scheduler should default to disabled")
	}
	if len(cfg.Prizm.Scheduler.Jobs) != 0 {
		t.Errorf("expected 0 scheduler jobs, got %d", len(cfg.Prizm.Scheduler.Jobs))
	}
}

func TestSchedulerJobConfigValidation(t *testing.T) {
	// Job without required fields
	yaml := `
prizm:
  scheduler:
    enabled: true
    jobs:
      - name: ""
        schedule: "0 3 * * *"
        event: "prizm.task.scheduled"
        enabled: true
agents:
  - id: test
    role: test
    model: test:latest
`
	cfg, err := LoadConfigFromBytes([]byte(yaml))
	if err != nil {
		t.Fatalf("LoadConfigFromBytes failed: %v", err)
	}
	// Config parsing doesn't validate — that happens at scheduler AddJob time
	if len(cfg.Prizm.Scheduler.Jobs) != 1 {
		t.Fatalf("expected 1 job, got %d", len(cfg.Prizm.Scheduler.Jobs))
	}
	if cfg.Prizm.Scheduler.Jobs[0].Name != "" {
		t.Error("empty name should parse as empty string")
	}
}
