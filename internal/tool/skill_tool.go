package tool

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/emaharmony/prizm/internal/skill"
)

// UseSkillTool lets an agent invoke a registered skill by name: it returns the
// skill's instruction payload so the model "loads" the skill into context. It is
// read-only (it only returns text), so policy auto-approves it; any scripts the
// skill bundles are executed via the normal mutation tools under their own gates.
type UseSkillTool struct {
	Registry *skill.Registry
}

// NewUseSkillTool builds a use_skill tool backed by the given skill registry.
func NewUseSkillTool(reg *skill.Registry) *UseSkillTool {
	return &UseSkillTool{Registry: reg}
}

func (t *UseSkillTool) Name() string { return "use_skill" }

func (t *UseSkillTool) Description() string {
	return "Invoke a registered skill by name to load its instructions. Use when a skill's description matches the task."
}

func (t *UseSkillTool) Schema() ToolSchema {
	return ToolSchema{
		Input: map[string]ParamSpec{
			"name": {Type: "string", Description: "The skill name to invoke (see the available skills list)", Required: true},
		},
		Output: ParamSpec{Type: "object", Description: "The skill's instructions to follow"},
	}
}

func (t *UseSkillTool) Execute(_ context.Context, input map[string]any) (ToolResult, error) {
	if t.Registry == nil {
		return ToolResult{Success: false, Error: "no skills are available in this environment"}, nil
	}
	name, _ := input["name"].(string)
	name = strings.TrimSpace(name)
	if name == "" {
		return ToolResult{Success: false, Error: "use_skill requires a 'name'"}, nil
	}
	s, err := t.Registry.Resolve(name)
	if err != nil {
		return ToolResult{Success: false, Error: fmt.Sprintf("%v. Available: %s", err, availableSkillNames(t.Registry))}, nil
	}
	return ToolResult{Success: true, Output: map[string]any{
		"name":         s.Name,
		"source":       s.Source,
		"instructions": s.Prompt(),
		"dir":          s.Dir,
	}}, nil
}

func availableSkillNames(reg *skill.Registry) string {
	infos := reg.List()
	names := make([]string, 0, len(infos))
	for _, i := range infos {
		names = append(names, i.Name)
	}
	sort.Strings(names)
	if len(names) == 0 {
		return "(none)"
	}
	return strings.Join(names, ", ")
}

// RegisterSkillTool registers use_skill into the tool registry when the skill
// registry holds at least one skill (no point advertising an empty capability).
func RegisterSkillTool(registry *Registry, skills *skill.Registry) {
	if registry == nil || skills == nil || skills.Len() == 0 {
		return
	}
	_ = registry.Register(NewUseSkillTool(skills))
}
