package runtrack

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestNewRun(t *testing.T) {
	run := NewRun("lumi", "sess-1", "glm-5.1:cloud", "ollama")
	if run.ID == "" {
		t.Error("expected run ID to be set")
	}
	if run.AgentID != "lumi" {
		t.Errorf("expected agent lumi, got %s", run.AgentID)
	}
	if run.Model != "glm-5.1:cloud" {
		t.Errorf("expected model glm-5.1:cloud, got %s", run.Model)
	}
	if run.Provider != "ollama" {
		t.Errorf("expected provider ollama, got %s", run.Provider)
	}
	if run.Cancel == nil {
		t.Error("expected cancel function to be set")
	}
	if run.MaxTokens != DefaultMaxTokens {
		t.Errorf("expected default max tokens %d, got %d", DefaultMaxTokens, run.MaxTokens)
	}
	if run.Deadline.Before(run.StartedAt) {
		t.Error("expected deadline after start time")
	}
}

func TestRunWithContext(t *testing.T) {
	ctx := context.Background()
	run := NewRunWithContext(ctx, "mango", "sess-2", "deepseek-v4-pro:cloud", "ollama")
	if run.AgentID != "mango" {
		t.Errorf("expected agent mango, got %s", run.AgentID)
	}
	if run.SessionID != "sess-2" {
		t.Errorf("expected session sess-2, got %s", run.SessionID)
	}
	if run.MaxTokens != DefaultMaxTokens {
		t.Errorf("expected default max tokens %d, got %d", DefaultMaxTokens, run.MaxTokens)
	}
}

func TestElapsed(t *testing.T) {
	run := NewRun("lumi", "sess-1", "glm-5.1:cloud", "ollama")
	elapsed := run.Elapsed()
	if elapsed < 0 {
		t.Errorf("expected non-negative elapsed time, got %s", elapsed)
	}
	if elapsed > time.Second {
		t.Errorf("expected elapsed < 1s for newly created run, got %s", elapsed)
	}
}

func TestCancelRegistry(t *testing.T) {
	reg := NewCancelRegistry()

	ctx, cancel := context.WithCancel(context.Background())
	reg.Register("sess-1", cancel)

	// Context should not be done yet
	select {
	case <-ctx.Done():
		t.Error("context should not be done yet")
	default:
	}

	// Cancel the session
	found := reg.Cancel("sess-1")
	if !found {
		t.Error("expected to find cancel for sess-1")
	}

	// Context should now be done
	select {
	case <-ctx.Done():
		// expected
	case <-time.After(time.Second):
		t.Error("expected context to be cancelled")
	}

	// Unregister
	reg.Unregister("sess-1")
	found = reg.Cancel("sess-1")
	if found {
		t.Error("expected cancel to not be found after unregister")
	}
}

func TestCancelRegistryConcurrent(t *testing.T) {
	reg := NewCancelRegistry()

	// Register many sessions concurrently
	done := make(chan bool, 100)
	for i := 0; i < 100; i++ {
		go func(n int) {
			ctx, cancel := context.WithCancel(context.Background())
			id := fmt.Sprintf("session-%d", n)
			reg.Register(id, cancel)
			reg.Cancel(id)
			<-ctx.Done()
			reg.Unregister(id)
			done <- true
		}(i)
	}

	for i := 0; i < 100; i++ {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("concurrent cancel registry test timed out")
		}
	}
}

func TestEventLogger(t *testing.T) {
	logger := &EventLogger{}
	// Should not panic
	logger.Log("run.started", "run-1", "lumi", map[string]any{
		"model":    "glm-5.1:cloud",
		"provider": "ollama",
	})
}

func TestDefaultTimeout(t *testing.T) {
	if DefaultTimeout != 20*time.Minute {
		t.Errorf("expected default timeout 20m, got %s", DefaultTimeout)
	}
}
