// Package skill adds skill-use capabilities to Prizm: it discovers, parses, and
// registers SKILL.md-based skills (the Claude Code / Agent Skills convention, also
// used by OpenClaw) so agents can invoke them.
//
// A skill is a named, discoverable capability defined by a SKILL.md file:
//
//	---
//	name: pdf-processing
//	description: Extract text and tables from PDFs. Use when working with PDF files.
//	allowed-tools: [read_file, run_validation]   # optional
//	---
//	# PDF processing
//	<markdown instructions the agent follows when the skill is invoked>
//
// The frontmatter's name+description are cheap to list (so the model can decide
// when a skill is relevant); the body is the instruction payload loaded into
// context when the skill is invoked. Bundled scripts/resources live alongside the
// SKILL.md in the skill's directory and run through Prizm's normal tool/policy path.
package skill

import (
	"bufio"
	"fmt"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Skill is a parsed SKILL.md capability.
type Skill struct {
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	Body         string   `json:"body"`
	AllowedTools []string `json:"allowed_tools,omitempty"`
	Dir          string   `json:"dir,omitempty"`    // directory holding SKILL.md + resources
	Source       string   `json:"source,omitempty"` // e.g. "claude", "openclaw"
}

// Info is the cheap listing view (name + description) used to advertise skills to
// the model without loading every body.
type Info struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Source      string `json:"source,omitempty"`
}

// Info returns the listing view for a skill.
func (s *Skill) Info() Info {
	return Info{Name: s.Name, Description: s.Description, Source: s.Source}
}

// skillFrontmatter is the YAML header of a SKILL.md.
type skillFrontmatter struct {
	Name         string   `yaml:"name"`
	Description  string   `yaml:"description"`
	AllowedTools []string `yaml:"allowed-tools"`
}

// Parse parses SKILL.md content. dir is the skill's directory (used for the name
// fallback and resource resolution); source labels its origin. A missing name
// falls back to the directory's base name. Returns an error only when neither a
// frontmatter name nor a usable directory name is available.
func Parse(content []byte, dir, source string) (*Skill, error) {
	fm, body := splitFrontmatter(string(content))

	var meta skillFrontmatter
	if strings.TrimSpace(fm) != "" {
		if err := yaml.Unmarshal([]byte(fm), &meta); err != nil {
			return nil, fmt.Errorf("skill %s: parse frontmatter: %w", dir, err)
		}
	}

	name := strings.TrimSpace(meta.Name)
	if name == "" {
		name = strings.TrimSpace(filepath.Base(dir))
	}
	if name == "" || name == "." || name == string(filepath.Separator) {
		return nil, fmt.Errorf("skill in %q has no name (set 'name:' in frontmatter)", dir)
	}

	return &Skill{
		Name:         name,
		Description:  strings.TrimSpace(meta.Description),
		Body:         strings.TrimSpace(body),
		AllowedTools: meta.AllowedTools,
		Dir:          dir,
		Source:       source,
	}, nil
}

// splitFrontmatter separates a leading `---`-delimited YAML block from the body.
// When the content does not start with a frontmatter fence, the whole input is the
// body and the frontmatter is empty.
func splitFrontmatter(content string) (frontmatter, body string) {
	// Normalize CRLF so the fence check is line-ending agnostic.
	content = strings.ReplaceAll(content, "\r\n", "\n")
	sc := bufio.NewScanner(strings.NewReader(content))
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	if !sc.Scan() {
		return "", ""
	}
	if strings.TrimSpace(sc.Text()) != "---" {
		// No frontmatter — reconstruct the whole content as body.
		return "", content
	}
	var fm, bd []string
	inFM := true
	for sc.Scan() {
		line := sc.Text()
		if inFM && strings.TrimSpace(line) == "---" {
			inFM = false
			continue
		}
		if inFM {
			fm = append(fm, line)
		} else {
			bd = append(bd, line)
		}
	}
	if inFM {
		// Unterminated frontmatter — treat the whole thing as body to be safe.
		return "", content
	}
	return strings.Join(fm, "\n"), strings.Join(bd, "\n")
}

// Prompt renders the skill's invocation payload — the instructions the agent
// follows once it has decided to use the skill.
func (s *Skill) Prompt() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Skill: %s\n", s.Name)
	if s.Description != "" {
		fmt.Fprintf(&b, "%s\n", s.Description)
	}
	if len(s.AllowedTools) > 0 {
		fmt.Fprintf(&b, "\nAllowed tools for this skill: %s\n", strings.Join(s.AllowedTools, ", "))
	}
	if s.Dir != "" {
		fmt.Fprintf(&b, "Skill directory (bundled scripts/resources): %s\n", s.Dir)
	}
	if s.Body != "" {
		b.WriteString("\n" + s.Body + "\n")
	}
	return b.String()
}
