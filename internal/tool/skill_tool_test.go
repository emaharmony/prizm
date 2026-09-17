package tool

import (
	"context"
	"strings"
	"testing"

	"github.com/emaharmony/prizm/internal/skill"
)

func skillReg(t *testing.T) *skill.Registry {
	t.Helper()
	r := skill.NewRegistry()
	if err := r.Register(&skill.Skill{Name: "pdf", Description: "process pdfs", Body: "step 1: extract", Source: "claude", Dir: "/s/pdf"}); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestUseSkillToolReturnsInstructions(t *testing.T) {
	tool := NewUseSkillTool(skillReg(t))
	if tool.Name() != "use_skill" {
		t.Fatalf("name = %q", tool.Name())
	}
	res, err := tool.Execute(context.Background(), map[string]any{"name": "pdf"})
	if err != nil || !res.Success {
		t.Fatalf("execute: %v %+v", err, res)
	}
	instr, _ := res.Output["instructions"].(string)
	if !strings.Contains(instr, "# Skill: pdf") || !strings.Contains(instr, "step 1: extract") {
		t.Fatalf("instructions wrong: %q", instr)
	}
	if res.Output["source"] != "claude" {
		t.Fatalf("source = %v", res.Output["source"])
	}
}

func TestUseSkillToolErrors(t *testing.T) {
	tool := NewUseSkillTool(skillReg(t))
	// missing name
	if res, _ := tool.Execute(context.Background(), map[string]any{}); res.Success {
		t.Fatal("empty name should fail")
	}
	// unknown skill lists available
	res, _ := tool.Execute(context.Background(), map[string]any{"name": "ghost"})
	if res.Success || !strings.Contains(res.Error, "pdf") {
		t.Fatalf("unknown skill should fail and list available, got %+v", res)
	}
	// nil registry
	if res, _ := (&UseSkillTool{}).Execute(context.Background(), map[string]any{"name": "x"}); res.Success {
		t.Fatal("nil registry should fail")
	}
}

func TestRegisterSkillToolOnlyWhenNonEmpty(t *testing.T) {
	reg := NewRegistry()
	RegisterSkillTool(reg, skill.NewRegistry()) // empty → not registered
	if _, err := reg.Resolve("use_skill"); err == nil {
		t.Fatal("use_skill should not register for empty skill registry")
	}
	RegisterSkillTool(reg, skillReg(t)) // non-empty → registered
	if _, err := reg.Resolve("use_skill"); err != nil {
		t.Fatalf("use_skill should be registered: %v", err)
	}
}

func TestUseSkillPolicyApproved(t *testing.T) {
	res := EvaluatePolicy(DefaultPolicyConfig(), "use_skill", map[string]any{"name": "pdf"})
	if res.Decision != PolicyApproved {
		t.Fatalf("use_skill should be auto-approved (read-only), got %s", res.Decision)
	}
}
