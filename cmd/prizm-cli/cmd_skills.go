// Package main implements the `prizm skills` subcommand: discover and inspect the
// SKILL.md skills (Claude Code / OpenClaw) available to agents.
//
// Usage:
//
//	prizm skills [--root .] [--json]      List discovered skills
//	prizm skills show <name> [--root .]   Print a skill's full instructions
//
// It scans the conventional skill dirs (.claude/skills, .openclaw/skills, skills/)
// under --root, the same discovery `prizm serve` uses, so what you see here is what
// agents can invoke via the use_skill tool.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/emaharmony/prizm/internal/skill"
)

// renderSkillList formats discovered skills (pure, testable).
func renderSkillList(infos []skill.Info) string {
	var b strings.Builder
	b.WriteString("🧠 skills\n")
	b.WriteString(strings.Repeat("─", 60) + "\n")
	if len(infos) == 0 {
		b.WriteString("  (none found — add SKILL.md skills under .claude/skills, .openclaw/skills, or skills/)\n")
		return b.String()
	}
	for _, i := range infos {
		desc := i.Description
		if len(desc) > 70 {
			desc = desc[:70] + "…"
		}
		fmt.Fprintf(&b, "  • %-20s [%-9s] %s\n", i.Name, i.Source, desc)
	}
	b.WriteString(strings.Repeat("─", 60) + "\n")
	fmt.Fprintf(&b, "%d skill(s). Agents invoke one with the use_skill tool. `prizm skills show <name>` for details.\n", len(infos))
	return b.String()
}

// executeSkills is the `prizm skills` entry point.
func executeSkills(args []string) {
	if len(args) >= 1 && args[0] == "show" {
		rest := args[1:]
		var name string
		if len(rest) >= 1 && !strings.HasPrefix(rest[0], "-") {
			name = rest[0]
			rest = rest[1:]
		}
		skillsShow(name, rest)
		return
	}

	fs := flag.NewFlagSet("skills", flag.ExitOnError)
	root := fs.String("root", ".", "Workspace root to scan for skills")
	asJSON := fs.Bool("json", false, "Emit the skill list as JSON")
	fs.Parse(args)

	reg := skill.NewRegistry()
	if _, err := reg.LoadDefault(*root); err != nil {
		fmt.Fprintf(os.Stderr, "note: %v\n", err) // non-fatal: still show what loaded
	}
	infos := reg.List()
	if *asJSON {
		data, mErr := json.MarshalIndent(infos, "", "  ")
		if mErr != nil {
			fmt.Fprintf(os.Stderr, "❌ %v\n", mErr)
			os.Exit(1)
		}
		fmt.Println(string(data))
		return
	}
	fmt.Print(renderSkillList(infos))
}

// skillsShow prints a single skill's full instruction payload.
func skillsShow(name string, args []string) {
	fs := flag.NewFlagSet("skills show", flag.ExitOnError)
	root := fs.String("root", ".", "Workspace root to scan for skills")
	fs.Parse(args)

	if name == "" {
		fmt.Fprintln(os.Stderr, "❌ usage: prizm skills show <name>")
		os.Exit(1)
	}
	reg := skill.NewRegistry()
	_, _ = reg.LoadDefault(*root)
	s, err := reg.Resolve(name)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ %v\n", err)
		os.Exit(1)
	}
	fmt.Print(s.Prompt())
}
