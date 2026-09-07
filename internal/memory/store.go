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
	ID         string            // ULID
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
// Phase 1 = MarkdownStore, Phase 2 = SQLiteStore (swap-in replacement).
type MemoryStore interface {
	Search(ctx context.Context, query string, limit int) ([]Memory, error)
	Get(ctx context.Context, id string) (*Memory, error)
	ListRecent(ctx context.Context, limit int) ([]Memory, error)
	Store(ctx context.Context, mem Memory) (string, error)
	Close() error
}

// MarkdownStore implements MemoryStore using memory/*.md files.
type MarkdownStore struct {
	root    string // workspace root (contains memory/ subdir)
	mu      sync.Map // per-date mutex for concurrent writes
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

var (
	headerRe  = regexp.MustCompile(`^###\s+(\S+)\s+—\s+(.+)$`)
	fieldRe   = regexp.MustCompile(`^-\s+\*\*([^*]+):\*\*\s+(.+)$`)
	separator = regexp.MustCompile(`^---+$`)
)

func parseMemoryFile(path string) ([]Memory, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var memories []Memory
	var current *Memory
	var contentLines []string
	inContent := false

	for _, line := range strings.Split(string(data), "\n") {
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

		// V79: Fallback for ## Title sections (plain markdown memories)
		// Each ## header becomes a memory with the title as both ID and summary.
		if strings.HasPrefix(line, "## ") {
			title := strings.TrimPrefix(line, "## ")
			title = strings.TrimSpace(title)
			if title != "" && current == nil {
				// First section in file — start a new memory
				current = &Memory{ID: strings.ReplaceAll(title, " ", "-"), Summary: title}
				contentLines = nil
				inContent = true
				continue
			}
			if current != nil && title != "" {
				// New section — save previous and start next
				current.Content = strings.TrimSpace(strings.Join(contentLines, "\n"))
				memories = append(memories, *current)
				current = &Memory{ID: strings.ReplaceAll(title, " ", "-"), Summary: title}
				contentLines = nil
				inContent = true
				continue
			}
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

	// Derive date from filename for CreatedAt
	base := filepath.Base(path)
	dateStr := strings.TrimSuffix(base, ".md")
	if t, err := time.Parse("2006-01-02", dateStr); err == nil {
		for i := range memories {
			if memories[i].CreatedAt.IsZero() {
				memories[i].CreatedAt = t
				memories[i].AccessedAt = t
			}
		}
	}

	return memories, nil
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