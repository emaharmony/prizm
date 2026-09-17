package workstart

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/emaharmony/prizm/internal/orchestrator"
	"github.com/emaharmony/prizm/internal/safety"
)

// Request is the shared contract for starting agentic project work from
// Discord, the API, or the CLI.
type Request struct {
	Project         string `json:"project,omitempty"`
	Prompt          string `json:"prompt"`
	Channel         string `json:"channel,omitempty"`
	RepoPath        string `json:"repo_path,omitempty"`
	Source          string `json:"source,omitempty"`
	RequestedBy     string `json:"requested_by,omitempty"`
	Bootstrap       bool   `json:"bootstrap,omitempty"`
	RequireLocation bool   `json:"require_location,omitempty"`
}

// Resolution describes the result of resolving a work-start request.
type Resolution struct {
	Request        Request                     `json:"request"`
	ProjectID      string                      `json:"project,omitempty"`
	RepoPath       string                      `json:"repo_path,omitempty"`
	Channel        string                      `json:"channel,omitempty"`
	Bootstrap      bool                        `json:"bootstrap,omitempty"`
	NeedsLocation  bool                        `json:"needs_location"`
	Recommendation string                      `json:"recommendation,omitempty"`
	Question       string                      `json:"question,omitempty"`
	Reason         string                      `json:"reason,omitempty"`
	Project        *orchestrator.ProjectConfig `json:"-"`
}

var projectNameUnsafe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// SafeProjectName turns a project label into a single path segment suitable for
// a recommendation. It never returns path separators or traversal segments.
func SafeProjectName(name string) string {
	cleaned := projectNameUnsafe.ReplaceAllString(strings.TrimSpace(name), "-")
	cleaned = strings.Trim(cleaned, ".-_")
	if cleaned == "" || cleaned == "." || cleaned == ".." {
		return "project"
	}
	return cleaned
}

// ConfiguredWriteRoots returns the effective configured roots used for
// approval-gated writes. New write_roots values take precedence in Config.
func ConfiguredWriteRoots(cfg *orchestrator.Config) []string {
	if cfg == nil {
		return nil
	}
	roots := cfg.EffectiveWriteRoots()
	out := make([]string, 0, len(roots))
	for _, root := range roots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		abs, err := filepath.Abs(root)
		if err != nil {
			continue
		}
		out = append(out, abs)
	}
	return out
}

// PreferredWriteRoot returns the first configured write root, if any.
func PreferredWriteRoot(cfg *orchestrator.Config) string {
	roots := ConfiguredWriteRoots(cfg)
	if len(roots) == 0 {
		return ""
	}
	return roots[0]
}

// ResolveRepoPath validates an explicit repository path against configured
// write roots and returns its absolute form.
func ResolveRepoPath(cfg *orchestrator.Config, repoPath string) (string, error) {
	repoPath = strings.TrimSpace(repoPath)
	if repoPath == "" {
		return "", fmt.Errorf("repo_path is required")
	}
	roots := ConfiguredWriteRoots(cfg)
	if len(roots) == 0 {
		return "", fmt.Errorf("no configured write roots; set prizm.write_roots before starting work in an explicit path")
	}
	resolved, err := safety.ResolveAndContainMulti(roots, repoPath)
	if err != nil {
		return "", err
	}
	return resolved, nil
}

// Resolve applies Prizm's project-location rules:
// configured project first, explicit repo_path second, otherwise ask with a
// recommendation from the first configured write root.
func Resolve(cfg *orchestrator.Config, req Request) (*Resolution, error) {
	req.Project = strings.TrimSpace(req.Project)
	req.Prompt = strings.TrimSpace(req.Prompt)
	req.Channel = strings.TrimSpace(req.Channel)
	req.RepoPath = strings.TrimSpace(req.RepoPath)

	res := &Resolution{
		Request:   req,
		ProjectID: req.Project,
		Channel:   req.Channel,
		Bootstrap: req.Bootstrap,
	}

	if req.RequireLocation && req.Project == "" && req.RepoPath == "" {
		return locationQuestion(cfg, res, "explicit project location required"), nil
	}

	if req.Project != "" && cfg != nil {
		if project := cfg.FindProject(req.Project); project != nil {
			res.Project = project
			res.ProjectID = project.ID
			if res.Channel == "" {
				res.Channel = project.Channel
			}
			if project.RepoPath != "" {
				repoPath, err := ResolveRepoPath(cfg, project.RepoPath)
				if err != nil {
					return nil, fmt.Errorf("configured project %q repo_path is outside configured write roots: %w", project.ID, err)
				}
				res.RepoPath = repoPath
				return res, nil
			}
			return locationQuestion(cfg, res, "configured project has no repo_path"), nil
		}
	}

	if req.RepoPath != "" {
		repoPath, err := ResolveRepoPath(cfg, req.RepoPath)
		if err != nil {
			return nil, err
		}
		res.RepoPath = repoPath
		if res.ProjectID == "" {
			res.ProjectID = SafeProjectName(filepath.Base(repoPath))
		}
		return res, nil
	}

	if req.Project == "" && cfg != nil {
		if project := cfg.DefaultProject(); project != nil && project.RepoPath != "" {
			res.Project = project
			res.ProjectID = project.ID
			if res.Channel == "" {
				res.Channel = project.Channel
			}
			repoPath, err := ResolveRepoPath(cfg, project.RepoPath)
			if err != nil {
				return nil, fmt.Errorf("configured default project %q repo_path is outside configured write roots: %w", project.ID, err)
			}
			res.RepoPath = repoPath
			return res, nil
		}
	}

	reason := "no configured project or explicit repo_path"
	if req.Project != "" {
		reason = "project is not configured and no repo_path was provided"
	}
	return locationQuestion(cfg, res, reason), nil
}

func locationQuestion(cfg *orchestrator.Config, res *Resolution, reason string) *Resolution {
	res.NeedsLocation = true
	res.Reason = reason
	name := res.ProjectID
	if name == "" {
		name = "project"
	}
	if root := PreferredWriteRoot(cfg); root != "" {
		res.Recommendation = filepath.Join(root, SafeProjectName(name))
		res.Question = fmt.Sprintf("I need a confirmed project folder before creating files. Recommended path: %s", res.Recommendation)
	} else {
		res.Question = "I need a confirmed project folder before creating files, but no prizm.write_roots are configured."
	}
	return res
}
