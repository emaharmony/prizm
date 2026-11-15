package tool

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/emaharmony/prizm/internal/freewill"
)

const (
	freewillReadmeLimit = 12 << 10
	freewillStateLimit  = 8 << 10
	freewillTotalLimit  = 32 << 10
)

// ApplyFreewillPrivateProfile reads only the deterministic ignored local host
// export for this project. It contains normalized claims, never source snippets.
func ApplyFreewillPrivateProfile(ctx context.Context, snapshot freewill.ProjectContextSnapshot, repoRoot, repositoryURL, commitSHA string) (freewill.ProjectContextSnapshot, error) {
	if ctx == nil || !filepath.IsAbs(repoRoot) || !freewillPrivateProfileID.MatchString(snapshot.ProjectID) || strings.TrimSpace(repositoryURL) == "" || strings.TrimSpace(commitSHA) == "" {
		return freewill.ProjectContextSnapshot{}, errors.New("tool: private profile binding is invalid")
	}
	if err := ctx.Err(); err != nil {
		return freewill.ProjectContextSnapshot{}, err
	}
	root, err := filepath.EvalSymlinks(repoRoot)
	if err != nil {
		return freewill.ProjectContextSnapshot{}, errors.New("tool: project root unavailable")
	}
	profilePath := filepath.Join(root, ".codex", "private-context", snapshot.ProjectID+".json")
	resolved, err := filepath.EvalSymlinks(profilePath)
	if err != nil {
		return freewill.ProjectContextSnapshot{}, errors.New("tool: private profile unavailable")
	}
	if filepath.Dir(resolved) != filepath.Join(root, ".codex", "private-context") {
		return freewill.ProjectContextSnapshot{}, errors.New("tool: private profile escaped its scoped directory")
	}
	info, err := os.Stat(resolved)
	if err != nil || info.Size() > 64<<10 {
		return freewill.ProjectContextSnapshot{}, errors.New("tool: private profile is unavailable or oversized")
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		return freewill.ProjectContextSnapshot{}, err
	}
	var profile freewill.ImportedProjectProfile
	if err := json.Unmarshal(data, &profile); err != nil {
		return freewill.ProjectContextSnapshot{}, errors.New("tool: private profile is malformed")
	}
	return freewill.ApplyImportedProjectProfile(snapshot, profile, repositoryURL, commitSHA)
}

var freewillPrivateProfileID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

var freewillSensitiveLine = regexp.MustCompile(`(?i)(api[_ -]?key|access[_ -]?token|password|secret|credential|authorization|private[_ -]?key)\s*[:=]`)
var freewillTokenLike = regexp.MustCompile(`(?i)(?:gh[pousr]_[a-z0-9]{20,}|github_pat_[a-z0-9_]{20,}|sk-[a-z0-9_-]{16,}|xox[baprs]-[a-z0-9-]{10,}|eyJ[a-z0-9_-]{12,}\.[a-z0-9_-]{8,}\.[a-z0-9_-]{8,})`)

// BuildFreewillProjectContext reads only approved, low-risk project summaries
// beneath the exact host-configured repository root. The caller must first
// establish that root is covered by the existing project read authorization.
func BuildFreewillProjectContext(ctx context.Context, projectID, objective, repoRoot, stateFile string) (freewill.ProjectContextSnapshot, error) {
	if ctx == nil || strings.TrimSpace(projectID) == "" || !filepath.IsAbs(repoRoot) {
		return freewill.ProjectContextSnapshot{}, errors.New("tool: Freewill project context binding is invalid")
	}
	root, err := filepath.EvalSymlinks(repoRoot)
	if err != nil {
		return freewill.ProjectContextSnapshot{}, errors.New("tool: Freewill project root is unavailable")
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return freewill.ProjectContextSnapshot{}, errors.New("tool: Freewill project root is unavailable")
	}
	snapshot := freewill.NewProjectContextSnapshot(projectID, objective, time.Now().UTC())
	total := 0
	add := func(id, rel string, limit int64, transform func([]byte) ([]byte, error)) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		content, resolved, err := readFreewillProjectFile(root, rel, limit)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if transform != nil {
			content, err = transform(content)
			if err != nil {
				return err
			}
		}
		if len(content) == 0 || total+len(content) > freewillTotalLimit {
			return errors.New("tool: Freewill project context exceeds its total limit")
		}
		relative, err := filepath.Rel(root, resolved)
		if err != nil || filepath.IsAbs(relative) || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return errors.New("tool: Freewill project source escaped its root")
		}
		snapshot.Sources = append(snapshot.Sources, freewill.ProjectContextSource{ID: id,
			Path: filepath.ToSlash(relative), Revision: "", Digest: "", ObservedAt: snapshot.CapturedAt,
			Confidence: "direct_file_observation", Content: string(content)})
		total += len(content)
		return nil
	}

	for _, name := range []string{"README.md", "README.rst", "README.txt", "README"} {
		if err := add("project:readme", name, freewillReadmeLimit, redactFreewillProjectText); err != nil {
			return freewill.ProjectContextSnapshot{}, err
		}
		if len(snapshot.Sources) > 0 && snapshot.Sources[len(snapshot.Sources)-1].ID == "project:readme" {
			break
		}
	}
	snapshot.RoadmapActions = freewillReadmeRoadmapActions(snapshot.Sources)
	for _, name := range []string{"go.mod", "package.json", "Cargo.toml", "pyproject.toml"} {
		if err := add("project:manifest:"+name, name, 16<<10, summarizeFreewillManifest(name)); err != nil {
			return freewill.ProjectContextSnapshot{}, err
		}
	}
	if strings.TrimSpace(stateFile) != "" {
		clean := filepath.Clean(stateFile)
		if filepath.IsAbs(clean) {
			rel, err := filepath.Rel(root, clean)
			if err != nil || filepath.IsAbs(rel) || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				return freewill.ProjectContextSnapshot{}, errors.New("tool: configured Freewill state file is outside its project root")
			}
			clean = rel
		}
		if clean == "." || strings.HasPrefix(clean, "..") {
			return freewill.ProjectContextSnapshot{}, errors.New("tool: configured Freewill state file is outside its project root")
		}
		if err := add("project:state", clean, freewillStateLimit, summarizeFreewillState); err != nil {
			return freewill.ProjectContextSnapshot{}, err
		}
	}
	snapshot.Seal()
	return snapshot, nil
}

// freewillReadmeRoadmapActions extracts exact Scope cells from an explicitly
// headed milestone table. These IDs bind candidate tasks to observed text;
// they do not turn the project README into instructions or grant authority.
func freewillReadmeRoadmapActions(sources []freewill.ProjectContextSource) []freewill.ProjectRoadmapAction {
	for _, source := range sources {
		if source.ID != "project:readme" || source.Path == "" {
			continue
		}
		lines := strings.Split(source.Content, "\n")
		snapshotRoadmapActions := make([]freewill.ProjectRoadmapAction, 0, 8)
		inRoadmap := false
		idColumn, scopeColumn, statusColumn := -1, -1, -1
		for _, raw := range lines {
			line := strings.TrimSpace(raw)
			lower := strings.ToLower(line)
			if strings.HasPrefix(line, "#") {
				inRoadmap = strings.Contains(lower, "milestone roadmap")
				idColumn, scopeColumn, statusColumn = -1, -1, -1
				continue
			}
			if !inRoadmap || !strings.HasPrefix(line, "|") {
				continue
			}
			cells := markdownTableCells(line)
			if scopeColumn < 0 || statusColumn < 0 {
				for i, cell := range cells {
					switch strings.ToLower(strings.TrimSpace(cell)) {
					case "milestone":
						idColumn = i
					case "scope":
						scopeColumn = i
					case "status":
						statusColumn = i
					}
				}
				continue
			}
			if idColumn < 0 || len(cells) <= idColumn || len(cells) <= scopeColumn || len(cells) <= statusColumn {
				continue
			}
			milestone := strings.TrimSpace(cells[idColumn])
			scope := strings.TrimSpace(cells[scopeColumn])
			status := strings.ToLower(strings.TrimSpace(cells[statusColumn]))
			if milestone == "" || strings.Trim(milestone, "-: ") == "" || scope == "" || strings.Trim(scope, "-: ") == "" ||
				len(scope) > 512 || strings.ContainsAny(scope, "\r\n\x00") || strings.Contains(status, "done") || strings.Contains(status, "complete") {
				continue
			}
			if len(milestone) > 32 || strings.IndexFunc(milestone, func(r rune) bool { return !(r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '.') }) >= 0 {
				continue
			}
			id := "project:roadmap:" + milestone
			duplicate := false
			for _, existing := range snapshotRoadmapActions {
				if existing.ID == id {
					duplicate = true
					break
				}
			}
			if !duplicate {
				snapshotRoadmapActions = append(snapshotRoadmapActions, freewill.ProjectRoadmapAction{ID: id, Text: scope, SourceID: source.ID})
			}
			if len(snapshotRoadmapActions) == 16 {
				return snapshotRoadmapActions
			}
		}
		return snapshotRoadmapActions
	}
	return nil
}

func markdownTableCells(line string) []string {
	line = strings.TrimSpace(strings.Trim(line, "|"))
	cells := strings.Split(line, "|")
	for i := range cells {
		cells[i] = strings.TrimSpace(cells[i])
	}
	return cells
}

func readFreewillProjectFile(root, rel string, limit int64) ([]byte, string, error) {
	if filepath.IsAbs(rel) || rel == "" || ContainsPathTraversal(rel) {
		return nil, "", errors.New("tool: invalid Freewill project source path")
	}
	path := filepath.Join(root, rel)
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, "", err
	}
	relative, err := filepath.Rel(root, resolved)
	if err != nil || filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return nil, "", errors.New("tool: Freewill project source escaped its root")
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() {
		return nil, "", errors.New("tool: Freewill project source is not a regular file")
	}
	f, err := os.Open(resolved)
	if err != nil {
		return nil, "", errors.New("tool: Freewill project source is unavailable")
	}
	defer f.Close()
	openedInfo, err := f.Stat()
	// Compare the opened descriptor with the file resolved and contained above.
	// If the path changed between containment validation and open, fail before
	// reading; the deferred close releases the rejected descriptor.
	if err != nil || !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) {
		return nil, "", errors.New("tool: Freewill project source changed during validation")
	}
	content, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(content)) > limit || !utf8.Valid(content) || bytes.IndexByte(content, 0) >= 0 {
		return nil, "", errors.New("tool: Freewill project source exceeds its limit or is not text")
	}
	return content, resolved, nil
}

// State files are host-selected but may contain arbitrary private notes. Accept
// only short, explicitly named profile fields; unknown formats fail closed.
func summarizeFreewillState(raw []byte) ([]byte, error) {
	allowed := map[string]bool{
		"identity": true, "purpose": true, "domain": true, "users": true,
		"architecture": true, "capabilities": true, "goals": true,
		"exclusions": true, "current focus": true, "status": true, "history": true,
	}
	var output strings.Builder
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 1024), freewillStateLimit)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		key = strings.ToLower(strings.TrimSpace(strings.TrimLeft(key, "#*- ")))
		value = strings.TrimSpace(value)
		if !ok || !allowed[key] || value == "" || len(value) > 512 || strings.ContainsAny(value, "\\\x00") || freewillSensitiveLine.MatchString(line) || freewillTokenLike.MatchString(value) {
			return nil, errors.New("tool: configured Freewill state file contains unsupported or unsafe content")
		}
		output.WriteString(key)
		output.WriteString(": ")
		output.WriteString(value)
		output.WriteByte('\n')
	}
	if err := scanner.Err(); err != nil {
		return nil, errors.New("tool: configured Freewill state file could not be read")
	}
	return []byte(output.String()), nil
}

func redactFreewillProjectText(raw []byte) ([]byte, error) {
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 1024), freewillReadmeLimit)
	var output strings.Builder
	privateBlock := false
	for scanner.Scan() {
		line := scanner.Text()
		if strings.Contains(line, "-----BEGIN ") && strings.Contains(line, "PRIVATE KEY-----") {
			privateBlock = true
			output.WriteString("[redacted private-key block]\n")
			continue
		}
		if privateBlock {
			if strings.Contains(line, "-----END ") {
				privateBlock = false
			}
			continue
		}
		if freewillSensitiveLine.MatchString(line) {
			output.WriteString("[redacted credential-like line]\n")
			continue
		}
		output.WriteString(line)
		output.WriteByte('\n')
	}
	if err := scanner.Err(); err != nil {
		return nil, errors.New("tool: Freewill project text could not be read")
	}
	return []byte(output.String()), nil
}

func summarizeFreewillManifest(name string) func([]byte) ([]byte, error) {
	return func(raw []byte) ([]byte, error) {
		values := make(map[string]string)
		switch name {
		case "package.json":
			var parsed struct{ Name, Version string }
			if err := json.Unmarshal(raw, &parsed); err != nil {
				return nil, errors.New("tool: package manifest is invalid")
			}
			values["name"], values["version"] = parsed.Name, parsed.Version
		case "go.mod":
			for _, line := range strings.Split(string(raw), "\n") {
				fields := strings.Fields(line)
				if len(fields) == 2 && (fields[0] == "module" || fields[0] == "go") {
					values[fields[0]] = fields[1]
				}
			}
		case "Cargo.toml", "pyproject.toml":
			section := ""
			for _, line := range strings.Split(string(raw), "\n") {
				line = strings.TrimSpace(line)
				if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
					section = strings.Trim(line, "[]")
					continue
				}
				if (section == "package" || section == "project") && (strings.HasPrefix(line, "name =") || strings.HasPrefix(line, "version =")) {
					key, value, _ := strings.Cut(line, "=")
					values[strings.TrimSpace(key)] = strings.Trim(strings.TrimSpace(value), "\"'")
				}
			}
		default:
			return nil, fmt.Errorf("tool: unsupported Freewill manifest %q", name)
		}
		var fields []string
		for _, key := range []string{"module", "name", "version", "go"} {
			if value := strings.TrimSpace(values[key]); value != "" {
				fields = append(fields, key+"="+value)
			}
		}
		return redactFreewillProjectText([]byte(strings.Join(fields, "\n")))
	}
}
