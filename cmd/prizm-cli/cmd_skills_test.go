package main

import (
	"strings"
	"testing"

	"github.com/emaharmony/prizm/internal/skill"
)

func TestRenderSkillList(t *testing.T) {
	out := renderSkillList([]skill.Info{
		{Name: "pdf", Source: "claude", Description: "process pdfs"},
		{Name: "fastapi", Source: "workspace", Description: "fastapi best practices"},
	})
	for _, want := range []string{"skills", "pdf", "claude", "process pdfs", "fastapi", "use_skill", "2 skill(s)"} {
		if !strings.Contains(out, want) {
			t.Fatalf("render missing %q:\n%s", want, out)
		}
	}
	if !strings.Contains(renderSkillList(nil), "none found") {
		t.Fatalf("empty list should guide the user")
	}
}

func TestCheckSkills(t *testing.T) {
	if c := checkSkills(0, nil); c.status != statusOK || !strings.Contains(c.detail, "none") {
		t.Fatalf("zero skills should be OK/none, got %s: %s", c.status, c.detail)
	}
	if c := checkSkills(3, nil); c.status != statusOK || !strings.Contains(c.detail, "3 skill") {
		t.Fatalf("3 skills should be OK with count, got %s: %s", c.status, c.detail)
	}
	if c := checkSkills(1, errInjectedDoctor); c.status != statusWarn {
		t.Fatalf("load error should WARN, got %s", c.status)
	}
}
