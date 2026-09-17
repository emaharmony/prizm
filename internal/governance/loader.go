package governance

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/emaharmony/prizm/internal/context"
	"github.com/emaharmony/prizm/internal/policy"
	"gopkg.in/yaml.v3"
)

// Package governance provides deterministic enforcement of governance docs.
//
// Governance docs are workspace markdown files that contain rules the agent
// must enforce — not just instructions the agent should follow, but constraints
// the runtime blocks at the tool execution layer.
//
// Design (aligned with Claude Code's model):
//   - CLAUDE.md = instructions (prompt-level, model may ignore) → SOUL.md + context
//   - Permission rules = enforcement (code-level, deterministic) → this package

// GovernanceDoc represents a parsed governance document.
type GovernanceDoc struct {
	Path        string               // Full path to the .md file
	Name        string               // Filename (e.g., "BASSBOOK-SCHEMA-FREEZE.md")
	Frontmatter GovernanceFrontmatter // Parsed YAML frontmatter
}

// GovernanceFrontmatter is the YAML frontmatter structure for governance docs.
type GovernanceFrontmatter struct {
	Governance GovernanceSpec `yaml:"governance"`
}

// GovernanceSpec defines what the doc freezes and how.
type GovernanceSpec struct {
	Status               string   `yaml:"status"`
	FrozenPaths          []string `yaml:"frozen_paths"`
	AllowedChanges       []string `yaml:"allowed_changes"`
	Reason               string   `yaml:"reason"`
	RequiresApprovalFrom string   `yaml:"requires_approval_from"`
}

// Loader scans the workspace for governance docs and registers policy rules.
type Loader struct {
	workspaceRoot string
	registry      *policy.Registry
	mu            sync.Mutex
	docs          []GovernanceDoc
}

// NewLoader creates a governance loader for the given workspace.
func NewLoader(workspaceRoot string, registry *policy.Registry) *Loader {
	return &Loader{
		workspaceRoot: workspaceRoot,
		registry:      registry,
	}
}

// Load scans the workspace for governance docs, parses their frontmatter,
// and registers policy rules. Returns the number of rules registered.
func (l *Loader) Load() (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	docs, err := l.scanDocs()
	if err != nil {
		return 0, err
	}

	rulesRegistered := 0
	for _, doc := range docs {
		rules, err := l.docToRules(doc)
		if err != nil {
			continue
		}
		for _, rule := range rules {
			if l.registry != nil {
				if err := l.registry.Register(rule); err != nil {
					continue
				}
			}
			rulesRegistered++
		}
		l.docs = append(l.docs, doc)
	}

	return rulesRegistered, nil
}

// Docs returns the loaded governance docs.
func (l *Loader) Docs() []GovernanceDoc {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.docs
}

// scanDocs walks the workspace looking for .md files with governance markers.
func (l *Loader) scanDocs() ([]GovernanceDoc, error) {
	var docs []GovernanceDoc

	scanDirs := []string{
		l.workspaceRoot,
		filepath.Join(l.workspaceRoot, "docs"),
	}

	for _, dir := range scanDirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}

		var mdFiles []os.DirEntry
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(strings.ToLower(e.Name()), ".md") {
				mdFiles = append(mdFiles, e)
			}
		}
		for i := 1; i < len(mdFiles); i++ {
			for j := i; j > 0 && mdFiles[j].Name() < mdFiles[j-1].Name(); j-- {
				mdFiles[j], mdFiles[j-1] = mdFiles[j-1], mdFiles[j]
			}
		}

		for _, entry := range mdFiles {
			fullPath := filepath.Join(dir, entry.Name())
			data, err := os.ReadFile(fullPath)
			if err != nil {
				continue
			}
			content := string(data)

			fm, hasFM := parseFrontmatter(content)
			if hasFM && (fm.Governance.Status != "" || len(fm.Governance.FrozenPaths) > 0) {
				docs = append(docs, GovernanceDoc{
					Path:        fullPath,
					Name:        entry.Name(),
					Frontmatter: fm,
				})
				continue
			}

			if context.DetectGovernance(content) {
				docs = append(docs, GovernanceDoc{
					Path: fullPath,
					Name: entry.Name(),
					Frontmatter: GovernanceFrontmatter{
						Governance: GovernanceSpec{
							Status: "detected",
							Reason: "Governance markers detected but no structured frontmatter.",
						},
					},
				})
			}
		}
	}

	return docs, nil
}

// docToRules converts a governance doc into policy rules.
func (l *Loader) docToRules(doc GovernanceDoc) ([]policy.PolicyRule, error) {
	if len(doc.Frontmatter.Governance.FrozenPaths) == 0 {
		return nil, nil
	}

	var rules []policy.PolicyRule
	for i, frozenPath := range doc.Frontmatter.Governance.FrozenPaths {
		ruleID := fmt.Sprintf("gov-%s-%d", sanitizeID(doc.Name), i)
		reason := doc.Frontmatter.Governance.Reason
		if reason == "" {
			reason = fmt.Sprintf("Path %s is frozen per %s", frozenPath, doc.Name)
		}

		rules = append(rules, policy.PolicyRule{
			ID:          ruleID,
			Description: fmt.Sprintf("Governance freeze from %s: %s", doc.Name, frozenPath),
			Match: policy.MatchSpec{
				Action:       "tool.execute",
				ResourceType: "file",
			},
			Decision: policy.DecisionDenied,
			Reason:   reason,
			Severity: policy.SeverityCritical,
		})
	}

	return rules, nil
}

// parseFrontmatter extracts YAML frontmatter from markdown content.
func parseFrontmatter(content string) (GovernanceFrontmatter, bool) {
	if !strings.HasPrefix(content, "---\n") {
		return GovernanceFrontmatter{}, false
	}

	end := strings.Index(content[4:], "\n---\n")
	if end < 0 {
		return GovernanceFrontmatter{}, false
	}

	fmContent := content[4 : 4+end]
	var fm GovernanceFrontmatter
	if err := yaml.Unmarshal([]byte(fmContent), &fm); err != nil {
		return GovernanceFrontmatter{}, false
	}

	// Only return true if the frontmatter has governance-relevant content
	if fm.Governance.Status == "" && len(fm.Governance.FrozenPaths) == 0 {
		return GovernanceFrontmatter{}, false
	}

	return fm, true
}

// sanitizeID converts a filename to a safe rule ID component.
func sanitizeID(name string) string {
	s := strings.ToLower(name)
	s = strings.ReplaceAll(s, ".md", "")
	s = strings.ReplaceAll(s, "-", "_")
	s = strings.ReplaceAll(s, " ", "_")
	return s
}

// MatchesFrozenPath checks if a given path matches any frozen path pattern.
// Supports exact paths, directory prefixes (ending in /), and glob patterns.
func MatchesFrozenPath(target string, frozenPaths []string) (bool, string) {
	target = filepath.Clean(target)
	for _, frozenPath := range frozenPaths {
		// Check for directory pattern BEFORE Clean (Clean strips trailing /)
		isDir := strings.HasSuffix(frozenPath, "/")
		cleanPath := filepath.Clean(frozenPath)

		// Directory match
		if isDir {
			if strings.HasPrefix(target, cleanPath+string(filepath.Separator)) || target == cleanPath {
				return true, frozenPath
			}
			continue
		}

		// Exact match
		if target == cleanPath {
			return true, frozenPath
		}

		// Glob match
		if matched, err := filepath.Match(cleanPath, target); err == nil && matched {
			return true, frozenPath
		}

		// Glob match against basename (for patterns like *.sql)
		base := filepath.Base(target)
		if matched, err := filepath.Match(cleanPath, base); err == nil && matched {
			return true, frozenPath
		}
	}
	return false, ""
}