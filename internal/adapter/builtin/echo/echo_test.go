package echo

import (
	"context"
	"testing"

	"github.com/emaharmony/prizm/internal/adapter"
)

func TestEchoAdapter_Name(t *testing.T) {
	e := &EchoAdapter{}
	if e.Name() != "echo" {
		t.Errorf("Name() = %q, want %q", e.Name(), "echo")
	}
}

func TestEchoAdapter_Version(t *testing.T) {
	e := &EchoAdapter{}
	if e.Version() != "1.0" {
		t.Errorf("Version() = %q, want %q", e.Version(), "1.0")
	}
}

func TestEchoAdapter_Capabilities(t *testing.T) {
	e := &EchoAdapter{}
	caps := e.Capabilities()
	if len(caps) != 1 {
		t.Fatalf("Capabilities() length = %d, want 1", len(caps))
	}
	if caps[0].Action != "echo" {
		t.Errorf("Capability Action = %q, want %q", caps[0].Action, "echo")
	}
}

func TestEchoAdapter_Execute_Echo(t *testing.T) {
	e := &EchoAdapter{}
	ctx := context.Background()

	input := map[string]any{"message": "hello"}
	result, err := e.Execute(ctx, "echo", input)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !result.Success {
		t.Error("Execute() Success = false, want true")
	}
	if result.Output["message"] != "hello" {
		t.Errorf("Execute() Output[\"message\"] = %v, want %v", result.Output["message"], "hello")
	}
}

func TestEchoAdapter_Execute_DefaultAction(t *testing.T) {
	e := &EchoAdapter{}
	ctx := context.Background()

	input := map[string]any{"key": "value"}
	result, err := e.Execute(ctx, "", input)
	if err != nil {
		t.Fatalf("Execute() with empty action error = %v", err)
	}
	if !result.Success {
		t.Error("Execute() with empty action Success = false, want true")
	}
}

func TestEchoAdapter_Execute_UnknownAction(t *testing.T) {
	e := &EchoAdapter{}
	ctx := context.Background()

	_, err := e.Execute(ctx, "unknown", map[string]any{})
	if err == nil {
		t.Error("Execute() with unknown action should return error")
	}
}

func TestEchoAdapter_Health(t *testing.T) {
	e := &EchoAdapter{}
	ctx := context.Background()

	health, err := e.Health(ctx)
	if err != nil {
		t.Fatalf("Health() error = %v", err)
	}
	if !health.Ready {
		t.Error("Health() Ready = false, want true")
	}
}

func TestEchoAdapter_RegistryIntegration(t *testing.T) {
	r := adapter.NewRegistry()
	e := &EchoAdapter{}

	if err := r.Register(e); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	resolved, err := r.Resolve("echo")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if resolved.Name() != "echo" {
		t.Errorf("Resolved Name = %q, want %q", resolved.Name(), "echo")
	}

	caps, err := r.Capabilities("echo")
	if err != nil {
		t.Fatalf("Capabilities() error = %v", err)
	}
	if len(caps) != 1 {
		t.Errorf("Capabilities() length = %d, want 1", len(caps))
	}
}
