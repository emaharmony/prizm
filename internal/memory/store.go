// Package memory provides local memory storage for Prizm agents.
// Phase 1: MarkdownStore — reads/writes memory/*.md files.
package memory

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"
)

// Memory is a single stored memory entry.
type Memory struct {
	ID         string            // ULID or derived from section title
	Content    string            // The memory text
	Category   string            // e.g., "decision", "preference", "fact"
	Tier       string            // "ephemeral", "active", "persist"
	Summary    string            // Short summary
	KeyTopics  []string          // Tags/topics
	Source     string            // e.g., "prizm:lumi", "recall"
	AgentID    string            // Agent that created it
	SessionID  string            // Session context
	ProjectID  string            // Project context
	Metadata   map[string]string // Extensible
	CreatedAt  time.Time
	AccessedAt time.Time
}

// MemoryStore is the abstract interface for memory operations.
type MemoryStore interface {
	Search(ctx context.Context, query string, limit int) ([]Memory, error)
	Get(ctx context.Context, id string) (*Memory, error)
	ListRecent(ctx context.Context, limit int) ([]Memory, error)
	Store(ctx context.Context, mem Memory) (string, error)
	Close() error
}

// MarkdownStore implements MemoryStore using memory/*.md files.
type MarkdownStore struct {
	root string    // workspace root (contains memory/ subdir)
	mu   sync.Map // per-date mutex for concurrent writes
}

// NewMarkdownStore creates a MarkdownStore rooted at the given workspace path.
func NewMarkdownStore(workspacePath string) *MarkdownStore {
	return &MarkdownStore{root: workspacePath}
}

func (s *MarkdownStore) datePath(t time.Time) string {
	return filepath.Join(s.root, "memory", t.Format("2006-01-02")+".md")
}

func (s *MarkdownStore) dateMu(t time.Time) *sync.Mutex {
	key := t.Format("2006-01-02")
	val, _ := s.mu.LoadOrStore(key, &sync.Mutex{})
	return val.(*sync.Mutex)
}

// Store appends a memory to today's markdown file.
func (s *MarkdownStore) Store(ctx context.Context, mem Memory) (string, error) {
	if mem.ID == "" {
		mem.ID = ulid.Make().String()
	}
	if mem.CreatedAt.IsZero() {
		mem.CreatedAt = time.Now()
	}
	if mem.AccessedAt.IsZero() {
		mem.AccessedAt = mem.CreatedAt
	}
	if mem.Category == "" {
		mem.Category = "fact"
	}
	if mem.Tier == "" {
		mem.Tier = "active"
	}

	path := s.datePath(mem.CreatedAt)
	mu := s.dateMu(mem.CreatedAt)
	mu.Lock()
	defer mu.Unlock()

	// Ensure directory exists
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return "", fmt.Errorf("create memory dir: %w", err)
	}

	// Append to file
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return "", fmt.Errorf("open memory file: %w", err)
	}
	defer f.Close()

	entry := formatMemoryEntry(mem)
	if _, err := f.WriteString("\n" + entry + "\n"); err != nil {
		return "", fmt.Errorf("write memory entry: %w", err)
	}

	return mem.ID, nil
}

// Get retrieves a memory by ID prefix match in markdown files.
func (s *MarkdownStore) Get(ctx context.Context, id string) (*Memory, error) {
	memDir := filepath.Join(s.root, "memory")
	entries, err := os.ReadDir(memDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read memory dir: %w", err)
	}

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		path := filepath.Join(memDir, e.Name())
		memories, err := parseMemoryFile(path)
		if err != nil {
			continue
		}
		for _, m := range memories {
			if strings.HasPrefix(m.ID, id) || m.ID == id {
				return &m, nil
			}
		}
	}
	return nil, nil // not found
}

// ListRecent returns the N most recent memories across all daily files.
func (s *MarkdownStore) ListRecent(ctx context.Context, limit int) ([]Memory, error) {
	memDir := filepath.Join(s.root, "memory")
	entries, err := os.ReadDir(memDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read memory dir: %w", err)
	}

	var all []Memory
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		path := filepath.Join(memDir, e.Name())
		memories, err := parseMemoryFile(path)
		if err != nil {
			continue
		}
		all = append(all, memories...)
	}

	// Sort by CreatedAt descending
	sort.Slice(all, func(i, j int) bool {
		return all[i].CreatedAt.After(all[j].CreatedAt)
	})

	if limit > 0 && len(all) > limit {
		all = all[:limit]
	}
	return all, nil
}

// Search performs keyword matching across memory files with recency boost.
func (s *MarkdownStore) Search(ctx context.Context, query string, limit int) ([]Memory, error) {
	all, err := s.ListRecent(ctx, 0) // get all
	if err != nil {
		return nil, err
	}

	if query == "" {
		if limit > 0 && len(all) > limit {
			all = all[:limit]
		}
		return all, nil
	}

	terms := strings.Fields(strings.ToLower(query))
	type scored struct {
		Memory Memory
		Score  float64
	}
	var results []scored

	for _, m := range all {
		score := scoreMemory(m, terms)
		if score > 0 {
			results = append(results, scored{Memory: m, Score: score})
		}
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})

	var out []Memory
	for i, r := range results {
		if limit > 0 && i >= limit {
			break
		}
		out = append(out, r.Memory)
	}
	return out, nil
}

// Close is a no-op for MarkdownStore.
func (s *MarkdownStore) Close() error { return nil }

// --- Scoring ---

func scoreMemory(m Memory, terms []string) float64 {
	text := strings.ToLower(m.Content + " " + m.Summary + " " + strings.Join(m.KeyTopics, " "))
	var score float64
	for _, term := range terms {
		count := strings.Count(text, term)
		if count > 0 {
			score += float64(count) * 2.0 // term frequency
		}
	}
	// Category/title match bonus
	lowerCat := strings.ToLower(m.Category)
	for _, term := range terms {
		if strings.Contains(lowerCat, term) {
			score += 3.0
		}
	}
	// Recency boost: newer memories score higher (max +5 for today, decaying over 30 days)
	daysSince := time.Since(m.CreatedAt).Hours() / 24
	if daysSince < 0 {
		daysSince = 0
	}
	recencyBoost := 5.0 * (1.0 / (1.0 + daysSince/7.0))
	score += recencyBoost
	return score
}

// --- Parsing ---
//
// parseMemoryFile uses a multi-strategy approach:
//   1. Structured: ### ID — Summary format with - **Key:** Value fields
//   2. Header-based: Any markdown header (# through ######) creates a section
//   3. Paragraph-based: If no headers, each blank-line-separated block is a memory
//
// This makes Prizm's memory system work with any markdown notes — Obsidian vaults,
// Logseq folders, plain daily notes, or the structured ### format.

var (
	headerRe = regexp.MustCompile(`(?m)^###\s+(\S+)\s+—\s+(.+)$`)
	fieldRe  = regexp.MustCompile(`^-\s+\*\*([^*]+):\*\*\s+(.+)$`)
)

// mdHeaderRe matches any markdown header line (# through ######)
var mdHeaderRe = regexp.MustCompile(`^#{1,6}\s+(.+)$`)

func parseMemoryFile(path string) ([]Memory, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	content := string(data)
	filename := filepath.Base(path)

	// Extract date from filename (e.g., "2026-07-27.md" → 2026-07-27)
	dateFromFilename := time.Time{}
	dateStr := strings.TrimSuffix(filename, ".md")
	if t, err := time.Parse("2006-01-02", dateStr); err == nil {
		dateFromFilename = t
	}

	// Strategy 1: Try structured ### ID — Summary format first
	if memories := parseStructured(content, dateFromFilename); len(memories) > 0 {
		return memories, nil
	}

	// Strategy 2: Header-based extraction (any # through ######)
	if memories := parseByHeaders(content, filename, dateFromFilename); len(memories) > 0 {
		return memories, nil
	}

	// Strategy 3: Paragraph-based (no headers — split on blank lines)
	if memories := parseByParagraphs(content, filename, dateFromFilename); len(memories) > 0 {
		return memories, nil
	}

	return nil, nil
}

// parseStructured extracts memories in the OpenClaw ### ID — Summary format.
func parseStructured(content string, date time.Time) []Memory {
	// Only use structured parsing if the content actually contains ### — lines
	if !headerRe.MatchString(content) {
		return nil
	}

	var memories []Memory
	var current *Memory
	var contentLines []string
	inContent := false

	for _, line := range strings.Split(content, "\n") {
		if m := headerRe.FindStringSubmatch(line); m != nil {
			if current != nil {
				current.Content = strings.TrimSpace(strings.Join(contentLines, "\n"))
				memories = append(memories, *current)
			}
			id := m[1]
			summary := m[2]
			current = &Memory{ID: id, Summary: summary}
			contentLines = nil
			inContent = false
			continue
		}

		if current == nil {
			continue
		}

		if m := fieldRe.FindStringSubmatch(line); m != nil {
			key := strings.TrimSpace(m[1])
			val := strings.TrimSpace(m[2])
			switch key {
			case "Category":
				current.Category = val
			case "Tier":
				current.Tier = val
			case "Source":
				current.Source = val
			case "Agent":
				current.AgentID = val
			case "Session":
				current.SessionID = val
			case "Project":
				current.ProjectID = val
			case "Key Topics":
				current.KeyTopics = strings.Split(val, ", ")
			}
			inContent = false
			continue
		}

		if line == "" && !inContent {
			inContent = true
			continue
		}

		if inContent {
			contentLines = append(contentLines, line)
		}
	}

	if current != nil {
		current.Content = strings.TrimSpace(strings.Join(contentLines, "\n"))
		memories = append(memories, *current)
	}

	// Set date from filename
	if !date.IsZero() {
		for i := range memories {
			if memories[i].CreatedAt.IsZero() {
				memories[i].CreatedAt = date
				memories[i].AccessedAt = date
			}
		}
	}

	return memories
}

// parseByHeaders extracts memories from any markdown header level (# through ######).
// Each header creates a new memory section. Headers at the same or higher level
// start a new memory. Deeper headers are included in the current section's content.
func parseByHeaders(content string, filename string, date time.Time) []Memory {
	var memories []Memory
	var current *sectionBuilder

	for _, line := range strings.Split(content, "\n") {
		if m := mdHeaderRe.FindStringSubmatch(line); m != nil {
			title := strings.TrimSpace(m[1])
			if title == "" {
				continue
			}
			// Save previous section
			if current != nil {
				mem := current.build(date)
				if mem.Content != "" || mem.Summary != "" {
					memories = append(memories, mem)
				}
			}
			current = newSectionBuilder(slugify(title), title)
			continue
		}

		if current != nil {
			current.addLine(line)
		}
	}

	// Save last section
	if current != nil {
		mem := current.build(date)
		if mem.Content != "" || mem.Summary != "" {
			memories = append(memories, mem)
		}
	}

	// If no headers were found, return nil (let paragraph parser handle it)
	if len(memories) == 0 {
		return nil
	}

	return memories
}

// parseByParagraphs extracts memories from plain text by splitting on blank lines.
// Each paragraph becomes a memory with the first line as summary.
// This handles files with no headers at all — just bullet lists or prose.
func parseByParagraphs(content string, filename string, date time.Time) []Memory {
	paragraphs := splitParagraphs(content)
	if len(paragraphs) == 0 {
		return nil
	}

	var memories []Memory
	for _, para := range paragraphs {
		para = strings.TrimSpace(para)
		if para == "" {
			continue
		}
		// Use first line as summary, rest as content
		lines := strings.SplitN(para, "\n", 2)
		summary := strings.TrimSpace(lines[0])
		// Strip leading list markers for summary
		summary = strings.TrimLeft(summary, "-•* ")
		// Truncate summary if too long
		if len(summary) > 100 {
			summary = summary[:97] + "..."
		}

		body := para
		if len(lines) > 1 {
			body = para
		}

		mem := Memory{
			ID:        slugify(summary),
			Summary:   summary,
			Content:   body,
			CreatedAt: date,
			AccessedAt: date,
		}
		memories = append(memories, mem)
	}

	return memories
}

// sectionBuilder helps construct a Memory from a header-based section.
type sectionBuilder struct {
	id       string
	summary  string
	lines    []string
	meta     map[string]string // key-value metadata from - **Key:** Value lines
}

func newSectionBuilder(id, summary string) *sectionBuilder {
	return &sectionBuilder{
		id:      id,
		summary: summary,
		meta:    make(map[string]string),
	}
}

func (sb *sectionBuilder) addLine(line string) {
	// Check for - **Key:** Value metadata lines
	if m := fieldRe.FindStringSubmatch(line); m != nil {
		sb.meta[strings.TrimSpace(m[1])] = strings.TrimSpace(m[2])
		return
	}
	sb.lines = append(sb.lines, line)
}

func (sb *sectionBuilder) build(date time.Time) Memory {
	content := strings.TrimSpace(strings.Join(sb.lines, "\n"))
	mem := Memory{
		ID:        sb.id,
		Summary:   sb.summary,
		Content:   content,
		CreatedAt: date,
		AccessedAt: date,
	}
	// Extract metadata
	if v, ok := sb.meta["Category"]; ok {
		mem.Category = v
	}
	if v, ok := sb.meta["Tier"]; ok {
		mem.Tier = v
	}
	if v, ok := sb.meta["Source"]; ok {
		mem.Source = v
	}
	if v, ok := sb.meta["Agent"]; ok {
		mem.AgentID = v
	}
	if v, ok := sb.meta["Key Topics"]; ok {
		mem.KeyTopics = strings.Split(v, ", ")
	}
	return mem
}

// slugify converts a title to a URL-friendly slug for use as a memory ID.
func slugify(s string) string {
	s = strings.ToLower(s)
	// Replace spaces with hyphens
	s = strings.ReplaceAll(s, " ", "-")
	// Remove non-alphanumeric characters except hyphens
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		}
	}
	result := b.String()
	// Collapse multiple hyphens
	for strings.Contains(result, "--") {
		result = strings.ReplaceAll(result, "--", "-")
	}
	return strings.Trim(result, "-")
}

// splitParagraphs splits text into paragraphs separated by blank lines.
func splitParagraphs(text string) []string {
	var paragraphs []string
	var current strings.Builder
	lines := strings.Split(text, "\n")

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			// End of paragraph
			if current.Len() > 0 {
				paragraphs = append(paragraphs, current.String())
				current.Reset()
			}
			continue
		}
		current.WriteString(line)
		current.WriteString("\n")
	}
	// Don't forget the last paragraph
	if current.Len() > 0 {
		paragraphs = append(paragraphs, current.String())
	}

	return paragraphs
}

// --- Formatting ---

func formatMemoryEntry(m Memory) string {
	var b strings.Builder
	fmt.Fprintf(&b, "### %s — %s", m.ID, m.Summary)
	fmt.Fprintf(&b, "\n- **Category:** %s", m.Category)
	fmt.Fprintf(&b, "\n- **Tier:** %s", m.Tier)
	if m.Source != "" {
		fmt.Fprintf(&b, "\n- **Source:** %s", m.Source)
	}
	if m.AgentID != "" {
		fmt.Fprintf(&b, "\n- **Agent:** %s", m.AgentID)
	}
	if m.SessionID != "" {
		fmt.Fprintf(&b, "\n- **Session:** %s", m.SessionID)
	}
	if m.ProjectID != "" {
		fmt.Fprintf(&b, "\n- **Project:** %s", m.ProjectID)
	}
	if len(m.KeyTopics) > 0 {
		fmt.Fprintf(&b, "\n- **Key Topics:** %s", strings.Join(m.KeyTopics, ", "))
	}
	fmt.Fprintf(&b, "\n\n%s", m.Content)
	return b.String()
}