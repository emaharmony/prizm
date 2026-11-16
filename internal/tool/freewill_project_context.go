package tool

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/emaharmony/prizm/internal/freewill"
)

const (
	freewillReadmeLimit           = 12 << 10
	freewillStateLimit            = 8 << 10
	freewillTotalLimit            = 32 << 10
	freewillSelectedSourceLimit   = 4 << 10
	freewillSelectedTotalLimit    = 8 << 10
	freewillSelectedFileReadLimit = 128 << 10
	freewillSelectedMaxFiles      = 12
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

// BuildFreewillProjectContextForFiles builds a short, source-backed context
// from an exact host-configured allowlist of safe Markdown files.
// The files must be clean HEAD-tracked files, and only bounded excerpts leave
// the host. An empty allowlist is invalid; callers use the legacy builder when
// no project-level allowlist is configured.
func BuildFreewillProjectContextForFiles(ctx context.Context, projectID, objective, repoRoot string, files []string, sections map[string]string) (freewill.ProjectContextSnapshot, error) {
	if ctx == nil || strings.TrimSpace(projectID) == "" || !filepath.IsAbs(repoRoot) || len(files) == 0 || len(files) > freewillSelectedMaxFiles {
		return freewill.ProjectContextSnapshot{}, errors.New("tool: Freewill exact project context binding is invalid")
	}
	allowedFiles := make(map[string]struct{}, len(files))
	for _, rel := range files {
		allowedFiles[rel] = struct{}{}
	}
	for rel, heading := range sections {
		if _, allowed := allowedFiles[rel]; !allowed || strings.TrimSpace(heading) == "" || len(heading) > 256 {
			return freewill.ProjectContextSnapshot{}, errors.New("tool: Freewill Markdown section must bind an exact allowlisted file and heading")
		}
	}
	root, err := filepath.EvalSymlinks(repoRoot)
	if err != nil {
		return freewill.ProjectContextSnapshot{}, errors.New("tool: Freewill project root is unavailable")
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return freewill.ProjectContextSnapshot{}, errors.New("tool: Freewill project root is unavailable")
	}
	headSHA, err := readFreewillTrackedContextHead(ctx, root)
	if err != nil {
		return freewill.ProjectContextSnapshot{}, err
	}
	snapshot := freewill.NewProjectContextSnapshot(projectID, objective, time.Now().UTC())
	seen := make(map[string]struct{}, len(files))
	total := 0
	for _, rel := range files {
		id, ok := freewillSelectedContextSourceID(rel)
		if !ok {
			return freewill.ProjectContextSnapshot{}, errors.New("tool: Freewill exact project context path is not allowed")
		}
		if _, exists := seen[rel]; exists {
			return freewill.ProjectContextSnapshot{}, errors.New("tool: Freewill exact project context paths are duplicated")
		}
		seen[rel] = struct{}{}
		raw, err := readFreewillTrackedContextBlob(ctx, root, rel, headSHA)
		if err != nil {
			return freewill.ProjectContextSnapshot{}, err
		}
		content, err := extractFreewillSelectedMarkdown(raw, sections[rel])
		if err != nil || len(content) == 0 || len(content) > freewillSelectedSourceLimit || total+len(content) > freewillSelectedTotalLimit {
			return freewill.ProjectContextSnapshot{}, errors.New("tool: Freewill exact project context excerpt is invalid or exceeds its limit")
		}
		snapshot.Sources = append(snapshot.Sources, freewill.ProjectContextSource{ID: id, Path: rel, Revision: sha256Hex(raw), CommitSHA: headSHA,
			SourceType: "git_tracked_file", ObservedAt: snapshot.CapturedAt, Confidence: "direct_file_observation", Content: string(content)})
		total += len(content)
	}
	snapshot.Seal()
	return snapshot, nil
}

func freewillSelectedContextSourceID(rel string) (string, bool) {
	if rel == "" || len(rel) > 115 || filepath.IsAbs(rel) || strings.ContainsAny(rel, "\\*?[]!^") || strings.Contains(rel, ":") || filepath.ToSlash(filepath.Clean(rel)) != rel || !strings.HasSuffix(strings.ToLower(rel), ".md") {
		return "", false
	}
	for _, part := range strings.Split(strings.ToLower(rel), "/") {
		if part == "" || part == "." || part == ".." || strings.HasPrefix(part, ".") {
			return "", false
		}
		for _, denied := range []string{"config", "private", "secret", "credential", "password", "token"} {
			if strings.Contains(part, denied) {
				return "", false
			}
		}
	}
	if rel == "README.md" {
		return "project:readme", true
	}
	return "project:file:" + rel, true
}

func readFreewillTrackedContextHead(ctx context.Context, root string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	headOutput, err := freewillProjectGitCommand(ctx, root, false, "rev-parse", "--verify", "HEAD").Output()
	if err != nil {
		return "", errors.New("tool: Freewill exact project context requires a Git HEAD")
	}
	headSHA := strings.TrimSpace(string(headOutput))
	if !validGitSHA1(headSHA) {
		return "", errors.New("tool: Freewill exact project context Git HEAD is invalid")
	}
	return headSHA, nil
}

func readFreewillTrackedContextBlob(ctx context.Context, root, rel, headSHA string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !validGitSHA1(headSHA) {
		return nil, errors.New("tool: Freewill exact project context Git HEAD is invalid")
	}
	path := filepath.FromSlash(rel)
	current := root
	for _, part := range strings.Split(path, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("tool: Freewill exact project context rejects missing files and symlinks")
		}
	}
	entryOutput, err := freewillProjectGitCommand(ctx, root, true, "ls-tree", headSHA, "--", rel).Output()
	if err != nil {
		return nil, errors.New("tool: Freewill exact project context requires a HEAD-tracked file")
	}
	entryLine := strings.TrimSuffix(strings.TrimSuffix(string(entryOutput), "\n"), "\r")
	metadata, listedPath, ok := strings.Cut(entryLine, "\t")
	fields := strings.Fields(metadata)
	if !ok || listedPath != rel || len(fields) != 3 || (fields[0] != "100644" && fields[0] != "100755") || fields[1] != "blob" || !validGitSHA1(fields[2]) {
		return nil, errors.New("tool: Freewill exact project context requires a tracked regular Git blob")
	}
	ignored := freewillProjectGitCommand(ctx, root, false, "check-ignore", "--no-index", "-q", "--", rel)
	_, ignoreErr := ignored.Output()
	if ignoreErr == nil {
		return nil, errors.New("tool: Freewill exact project context rejects ignored files")
	} else if exit, ok := ignoreErr.(*exec.ExitError); !ok || exit.ExitCode() != 1 {
		if ok && len(exit.Stderr) > 0 {
			detail := string(exit.Stderr)
			if len(detail) > 256 {
				detail = detail[:256]
			}
			return nil, fmt.Errorf("tool: Freewill exact project context ignore check failed: %s", strings.TrimSpace(detail))
		}
		return nil, fmt.Errorf("tool: Freewill exact project context ignore check failed: %w", ignoreErr)
	}
	sizeOutput, err := freewillProjectGitCommand(ctx, root, false, "cat-file", "-s", fields[2]).Output()
	if err != nil {
		return nil, errors.New("tool: Freewill exact project context HEAD blob size is unavailable")
	}
	size, err := strconv.Atoi(strings.TrimSpace(string(sizeOutput)))
	if err != nil || size < 0 || size > freewillSelectedFileReadLimit {
		return nil, errors.New("tool: Freewill exact project context HEAD blob exceeds its read limit")
	}
	content, err := freewillProjectGitCommand(ctx, root, false, "cat-file", "blob", fields[2]).Output()
	if err != nil || len(content) != size || !utf8.Valid(content) || bytes.IndexByte(content, 0) >= 0 {
		return nil, errors.New("tool: Freewill exact project context HEAD blob is unavailable or unsafe")
	}
	working, _, err := readFreewillProjectFile(root, rel, freewillSelectedFileReadLimit)
	workingNormalized, workingText := normalizeMarkdownLineEndings(working)
	headNormalized, headText := normalizeMarkdownLineEndings(content)
	if err != nil || !workingText || !headText || !bytes.Equal(workingNormalized, headNormalized) {
		return nil, errors.New("tool: Freewill exact project context requires the working file to match HEAD")
	}
	return content, nil
}

func freewillProjectGitCommand(ctx context.Context, root string, literalPathspec bool, args ...string) *exec.Cmd {
	prefix := []string{"-C", root, "-c", "core.fsmonitor=false"}
	if literalPathspec {
		prefix = append([]string{"--literal-pathspecs"}, prefix...)
	}
	full := append(prefix, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0", "GIT_PAGER=cat")
	return cmd
}

func normalizeMarkdownLineEndings(content []byte) ([]byte, bool) {
	normalized := bytes.ReplaceAll(content, []byte("\r\n"), []byte("\n"))
	return normalized, bytes.IndexByte(normalized, '\r') < 0
}

func extractFreewillSelectedMarkdown(raw []byte, heading string) ([]byte, error) {
	lines := strings.Split(string(raw), "\n")
	start, end := 0, len(lines)
	if strings.TrimSpace(heading) != "" {
		start = -1
		var fence markdownFenceState
		for i, line := range lines {
			if fence.consume(line) || fence.active() {
				continue
			}
			if strings.TrimSpace(line) != strings.TrimSpace(heading) {
				continue
			}
			level, ok := markdownHeadingLevel(line)
			if !ok {
				return nil, errors.New("tool: configured Freewill section anchor is not a Markdown heading")
			}
			start, end = i, len(lines)
			var sectionFence markdownFenceState
			for j := i + 1; j < len(lines); j++ {
				if sectionFence.consume(lines[j]) || sectionFence.active() {
					continue
				}
				if next, isHeading := markdownHeadingLevel(lines[j]); isHeading && next <= level {
					end = j
					break
				}
			}
			break
		}
		if start < 0 {
			return nil, errors.New("tool: configured Freewill section anchor is absent from the HEAD blob")
		}
	}
	var excerpt strings.Builder
	for _, line := range lines[start:end] {
		if excerpt.Len()+len(line)+1 > freewillSelectedSourceLimit {
			break
		}
		excerpt.WriteString(line)
		excerpt.WriteByte('\n')
	}
	clean, err := redactFreewillProjectText([]byte(excerpt.String()))
	if err != nil || freewillTokenLike.Match(clean) {
		return nil, errors.New("tool: selected Freewill project context contains unsafe text")
	}
	return clean, nil
}

type markdownFenceState struct {
	marker byte
	length int
}

func (f *markdownFenceState) active() bool {
	return f.marker != 0
}

func (f *markdownFenceState) consume(line string) bool {
	marker, length, trailing, ok := markdownFenceLine(line)
	if !ok {
		return false
	}
	if !f.active() {
		f.marker, f.length = marker, length
		return true
	}
	if marker == f.marker && length >= f.length && strings.TrimSpace(trailing) == "" {
		f.marker, f.length = 0, 0
	}
	return true
}

func markdownFenceLine(line string) (byte, int, string, bool) {
	trimmed := strings.TrimLeft(line, " ")
	if len(line)-len(trimmed) > 3 || len(trimmed) < 3 || (trimmed[0] != '`' && trimmed[0] != '~') {
		return 0, 0, "", false
	}
	marker := trimmed[0]
	length := 0
	for length < len(trimmed) && trimmed[length] == marker {
		length++
	}
	if length < 3 {
		return 0, 0, "", false
	}
	return marker, length, trimmed[length:], true
}

func markdownHeadingLevel(line string) (int, bool) {
	trimmed := strings.TrimLeft(line, " ")
	indent := len(line) - len(trimmed)
	if indent > 3 || strings.HasPrefix(trimmed, "\t") {
		return 0, false
	}
	line = strings.TrimSpace(trimmed)
	level := 0
	for level < len(line) && line[level] == '#' {
		level++
	}
	if level == 0 || level > 6 || level == len(line) || line[level] != ' ' {
		return 0, false
	}
	return level, true
}

func validGitSHA1(value string) bool {
	if len(value) != 40 {
		return false
	}
	for _, char := range value {
		if !(char >= '0' && char <= '9' || char >= 'a' && char <= 'f') {
			return false
		}
	}
	return true
}

func sha256Hex(content []byte) string {
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:])
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
