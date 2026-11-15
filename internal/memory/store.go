// Package memory provides local memory storage for Prizm agents.
// Phase 1: MarkdownStore — reads/writes memory/*.md files.
package memory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gofrs/flock"
	"github.com/oklog/ulid/v2"
)

// Memory is a single stored memory entry.
type Memory struct {
	ID           string            // ULID or derived from section title
	Content      string            // The memory text
	Category     string            // e.g., "decision", "preference", "fact"
	Tier         string            // "ephemeral", "active", "persist"
	Summary      string            // Short summary
	KeyTopics    []string          // Tags/topics
	Source       string            // e.g., "prizm:lumi", "recall"
	AgentID      string            // Agent that created it
	SessionID    string            // Session context
	ProjectID    string            // Project context
	UserID       string            // Explicit user scope; never inferred globally
	TaskID       string            // Exact task scope for autonomous memory operations
	SupersedesID string            // Earlier memory replaced by this one, if any
	Metadata     map[string]string // Extensible
	CreatedAt    time.Time
	AccessedAt   time.Time
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
	root   string          // workspace root (contains memory/ subdir)
	mu     sync.Map        // per-date mutex for concurrent writes
	embIdx *EmbeddingIndex // V80: embedding index for semantic search
}

// NewMarkdownStore creates a MarkdownStore rooted at the given path.
// The path can be either a workspace root (containing a memory/ subdir)
// or the memory directory itself (containing .md files).
func NewMarkdownStore(path string) *MarkdownStore {
	// If path already contains .md files, use it directly as the memory dir.
	// Otherwise, path is a workspace root and memory files are in path/memory/.
	return &MarkdownStore{root: path}
}

// memDir returns the directory containing memory .md files.
// If root contains .md files, it's the memory dir itself.
// Otherwise, it's root/memory/.
func (s *MarkdownStore) memDir() string {
	entries, err := os.ReadDir(s.root)
	if err == nil {
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".md") {
				return s.root
			}
		}
	}
	return filepath.Join(s.root, "memory")
}

func (s *MarkdownStore) datePath(t time.Time) string {
	return filepath.Join(s.memDir(), t.Format("2006-01-02")+".md")
}

func (s *MarkdownStore) dateMu(t time.Time) *sync.Mutex {
	key := t.Format("2006-01-02")
	val, _ := s.mu.LoadOrStore(key, &sync.Mutex{})
	return val.(*sync.Mutex)
}

func (s *MarkdownStore) withStoreLock(fn func(string) error) error {
	dir := s.memDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create memory dir: %w", err)
	}
	lock := flock.New(filepath.Join(dir, ".memory-store.lock"))
	if err := lock.Lock(); err != nil {
		return fmt.Errorf("lock memory store: %w", err)
	}
	defer lock.Unlock()
	return fn(dir)
}

const redactedIDsFile = ".redacted-memory-ids.json"

type memoryRedactionLedger struct {
	IDs     map[string]bool              `json:"ids"`
	Scopes  []memoryRedactedScope        `json:"scopes,omitempty"`
	Records []memoryScopedRedactedRecord `json:"records,omitempty"`
}

type memoryRedactedScope struct {
	Scope   Scope            `json:"scope"`
	Records []MemoryIdentity `json:"records,omitempty"`
}

// memoryScopedRedactedRecord masks one record identity without withdrawing or
// blocking writes to its containing scope. The ID remains blocked in that
// scope after compaction so a deterministic capture cannot restore the body.
type memoryScopedRedactedRecord struct {
	Scope  Scope          `json:"scope"`
	Record MemoryIdentity `json:"record"`
}

// MemoryIdentity names one stored record without copying its body into the
// redaction ledger. Source/category and a content+summary digest prevent a
// duplicate ID in another record from being erased by a scoped request.
type MemoryIdentity struct {
	ID       string `json:"id"`
	Source   string `json:"source"`
	Category string `json:"category"`
	Digest   string `json:"digest"`
}

func IdentityOf(mem Memory) MemoryIdentity {
	encoded, _ := json.Marshal([2]string{mem.Content, mem.Summary})
	digest := sha256.Sum256(encoded)
	return MemoryIdentity{ID: mem.ID, Source: mem.Source, Category: mem.Category, Digest: hex.EncodeToString(digest[:])}
}

func identityMatches(mem Memory, identity MemoryIdentity) bool {
	return mem.ID == identity.ID && mem.Source == identity.Source && mem.Category == identity.Category && IdentityOf(mem).Digest == identity.Digest
}

func readRedactionLedger(dir string) (memoryRedactionLedger, error) {
	ledger := memoryRedactionLedger{}
	b, err := os.ReadFile(filepath.Join(dir, redactedIDsFile))
	if os.IsNotExist(err) {
		return memoryRedactionLedger{IDs: map[string]bool{}}, nil
	}
	if err != nil {
		return ledger, err
	}
	if err := json.Unmarshal(b, &ledger); err == nil && ledger.IDs != nil {
		return ledger, nil
	}
	legacy := map[string]bool{}
	if err := json.Unmarshal(b, &legacy); err != nil || legacy == nil {
		return memoryRedactionLedger{}, errors.New("memory redaction ledger is invalid")
	}
	return memoryRedactionLedger{IDs: legacy}, nil
}

func writeJSONAtomic(path string, value any) error {
	dir := filepath.Dir(path)
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".memory-redaction-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err = tmp.Write(append(b, '\n')); err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(name, path)
}

// EraseIDs durably masks and then physically removes only the supplied memory
// IDs. The ID-only ledger is retained to prevent resurrection after retries.
// A returned error means cleanup is pending; reads remain masked.
func (s *MarkdownStore) EraseIDs(ctx context.Context, ids []string) (int, error) {
	return s.eraseIDs(ctx, ids)
}

// EraseScopedIDs durably blocks writes and reads in the exact scope while
// erasing the supplied IDs. The scope and IDs are stored together so a crash
// before file compaction still fails closed after reopen.
func (s *MarkdownStore) EraseScopedRecords(ctx context.Context, scope Scope, records []MemoryIdentity) (int, error) {
	if err := scope.Validate(); err != nil {
		return 0, err
	}
	return s.eraseScopedRecords(ctx, scope, records, true)
}

// RedactScopedRecords durably masks and removes exact records in one scope,
// without making withdrawal permanent for that scope. A redacted deterministic
// record ID cannot be written again, while new IDs in the scope remain valid.
func (s *MarkdownStore) RedactScopedRecords(ctx context.Context, scope Scope, records []MemoryIdentity) (int, error) {
	if err := scope.Validate(); err != nil {
		return 0, err
	}
	return s.eraseScopedRecords(ctx, scope, records, false)
}

func (s *MarkdownStore) ErasedRecordsForScope(ctx context.Context, scope Scope) ([]MemoryIdentity, error) {
	if err := scope.Validate(); err != nil {
		return nil, err
	}
	var records []MemoryIdentity
	err := s.withStoreLock(func(dir string) error {
		ledger, err := readRedactionLedger(dir)
		if err != nil {
			return err
		}
		for _, candidate := range ledger.Scopes {
			if redactionScopeMatches(candidate.Scope, scope) {
				records = append(records, candidate.Records...)
			}
		}
		for _, candidate := range ledger.Records {
			if redactionScopeMatches(candidate.Scope, scope) {
				records = append(records, candidate.Record)
			}
		}
		sort.Slice(records, func(i, j int) bool { return records[i].ID < records[j].ID })
		return nil
	})
	return records, err
}

func (s *MarkdownStore) ScopeErased(ctx context.Context, scope Scope) (bool, error) {
	if err := scope.Validate(); err != nil {
		return false, err
	}
	var erased bool
	err := s.withStoreLock(func(dir string) error {
		ledger, err := readRedactionLedger(dir)
		if err != nil {
			return err
		}
		erased = scopeErased(ledger, scope)
		return nil
	})
	return erased, err
}

// ListScopedRecords returns every physically stored row in one exact memory
// scope without ID deduplication or tombstone filtering. Erasure selectors use
// this for exact allowlist selection and post-compaction verification.
func (s *MarkdownStore) ListScopedRecords(ctx context.Context, scope Scope) ([]Memory, error) {
	if err := scope.Validate(); err != nil {
		return nil, err
	}
	var result []Memory
	err := s.withStoreLock(func(dir string) error {
		rows, err := readAllMemoryRows(dir)
		if err != nil {
			return err
		}
		for _, row := range rows {
			if memoryMatchesScope(row, scope) {
				result = append(result, row)
			}
		}
		return nil
	})
	return result, err
}

func scopeErased(ledger memoryRedactionLedger, scope Scope) bool {
	for _, candidate := range ledger.Scopes {
		if redactionScopeMatches(candidate.Scope, scope) {
			return true
		}
	}
	return false
}

func memoryScopeErased(ledger memoryRedactionLedger, mem Memory) bool {
	for _, candidate := range ledger.Scopes {
		if memoryMatchesScope(mem, candidate.Scope) {
			return true
		}
	}
	return false
}

func redactionScopeMatches(a, b Scope) bool {
	return a.UserID == b.UserID && a.IncludeUserScope == b.IncludeUserScope && a.ProjectID == b.ProjectID && a.TaskID == b.TaskID && a.AgentID == b.AgentID && a.CorrelationID == b.CorrelationID
}

func memoryMatchesScope(mem Memory, scope Scope) bool {
	return mem.UserID == scope.UserID && mem.ProjectID == scope.ProjectID && mem.TaskID == scope.TaskID && mem.AgentID == scope.AgentID
}

func memoryRedacted(mem Memory, ledger memoryRedactionLedger) bool {
	if ledger.IDs[mem.ID] {
		return true
	}
	for _, scope := range ledger.Scopes {
		if memoryMatchesScope(mem, scope.Scope) {
			for _, record := range scope.Records {
				if identityMatches(mem, record) {
					return true
				}
			}
		}
	}
	for _, candidate := range ledger.Records {
		if memoryMatchesScope(mem, candidate.Scope) && mem.ID == candidate.Record.ID {
			return true
		}
	}
	return false
}

func memoryPhysicallyRedacted(mem Memory, ledger memoryRedactionLedger) bool {
	if ledger.IDs[mem.ID] {
		return true
	}
	for _, scope := range ledger.Scopes {
		if memoryMatchesScope(mem, scope.Scope) {
			for _, record := range scope.Records {
				if identityMatches(mem, record) {
					return true
				}
			}
		}
	}
	for _, candidate := range ledger.Records {
		if memoryMatchesScope(mem, candidate.Scope) && identityMatches(mem, candidate.Record) {
			return true
		}
	}
	return false
}

func memoryWriteBlocked(mem Memory, ledger memoryRedactionLedger) bool {
	if ledger.IDs[mem.ID] {
		return true
	}
	if memoryScopeErased(ledger, mem) {
		return true
	}
	for _, candidate := range ledger.Records {
		if memoryMatchesScope(mem, candidate.Scope) && mem.ID == candidate.Record.ID {
			return true
		}
	}
	return false
}

func readAllMemoryRows(dir string) ([]Memory, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var rows []Memory
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".md" {
			continue
		}
		parsed, err := parseMemoryFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		rows = append(rows, parsed...)
	}
	return rows, nil
}

func (s *MarkdownStore) eraseIDs(ctx context.Context, ids []string) (int, error) {
	requested := make(map[string]bool, len(ids))
	for _, id := range ids {
		if strings.TrimSpace(id) == "" || id != strings.TrimSpace(id) {
			return 0, errors.New("memory erasure contains invalid ID")
		}
		requested[id] = true
	}
	removed := 0
	err := s.withStoreLock(func(dir string) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		ledger, err := readRedactionLedger(dir)
		if err != nil {
			return err
		}
		for id := range requested {
			ledger.IDs[id] = true
		}
		if err := writeJSONAtomic(filepath.Join(dir, redactedIDsFile), ledger); err != nil {
			return fmt.Errorf("persist memory erasure marker: %w", err)
		}
		files, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		for _, file := range files {
			if file.IsDir() || !strings.HasSuffix(file.Name(), ".md") {
				continue
			}
			path := filepath.Join(dir, file.Name())
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			globalRedactions := memoryRedactionLedger{IDs: ledger.IDs}
			updated, count, err := eraseMarkdownRecords(string(data), globalRedactions)
			if err != nil {
				return err
			}
			if count == 0 {
				continue
			}
			if err := writeTextAtomic(path, updated); err != nil {
				return fmt.Errorf("rewrite memory file: %w", err)
			}
			removed += count
		}
		if s.embIdx != nil {
			if err := s.embIdx.DeleteIDs(ledger.IDs); err != nil {
				return fmt.Errorf("memory embedding cleanup pending: %w", err)
			}
		}
		return nil
	})
	return removed, err
}

func (s *MarkdownStore) eraseScopedRecords(ctx context.Context, scope Scope, records []MemoryIdentity, blockScopeWrites bool) (int, error) {
	for _, record := range records {
		if strings.TrimSpace(record.ID) == "" || record.Digest == "" {
			return 0, errors.New("memory erasure contains invalid record identity")
		}
	}
	removed := 0
	err := s.withStoreLock(func(dir string) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		ledger, err := readRedactionLedger(dir)
		if err != nil {
			return err
		}
		if blockScopeWrites {
			found := false
			for i := range ledger.Scopes {
				if redactionScopeMatches(ledger.Scopes[i].Scope, scope) {
					found = true
					for _, record := range records {
						if !containsMemoryIdentity(ledger.Scopes[i].Records, record) {
							ledger.Scopes[i].Records = append(ledger.Scopes[i].Records, record)
						}
					}
				}
			}
			if !found {
				ledger.Scopes = append(ledger.Scopes, memoryRedactedScope{Scope: scope, Records: append([]MemoryIdentity(nil), records...)})
			}
		} else {
			for _, record := range records {
				found := false
				for _, candidate := range ledger.Records {
					if redactionScopeMatches(candidate.Scope, scope) && candidate.Record.ID == record.ID {
						if candidate.Record != record {
							return errors.New("memory redaction identity changed for retained ID")
						}
						found = true
						break
					}
				}
				if !found {
					ledger.Records = append(ledger.Records, memoryScopedRedactedRecord{Scope: scope, Record: record})
				}
			}
		}
		if err := writeJSONAtomic(filepath.Join(dir, redactedIDsFile), ledger); err != nil {
			return fmt.Errorf("persist memory erasure marker: %w", err)
		}
		targetIDs := map[string]bool{}
		for _, record := range records {
			targetIDs[record.ID] = true
		}
		allRows, err := readAllMemoryRows(dir)
		if err != nil {
			return fmt.Errorf("memory erasure pending collision scan: %w", err)
		}
		collisionIDs := map[string]bool{}
		for _, row := range allRows {
			if !targetIDs[row.ID] {
				continue
			}
			target := false
			for _, record := range records {
				if memoryMatchesScope(row, scope) && identityMatches(row, record) {
					target = true
				}
			}
			if !target {
				collisionIDs[row.ID] = true
			}
		}
		files, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		for _, file := range files {
			if file.IsDir() || filepath.Ext(file.Name()) != ".md" {
				continue
			}
			path := filepath.Join(dir, file.Name())
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			updated, count, err := eraseMarkdownRecords(string(data), ledger)
			if err != nil {
				return err
			}
			if count == 0 {
				continue
			}
			if err := writeTextAtomic(path, updated); err != nil {
				return fmt.Errorf("rewrite memory file: %w", err)
			}
			removed += count
		}
		if s.embIdx != nil {
			indexIDs := make(map[string]bool, len(ledger.IDs))
			for id := range ledger.IDs {
				indexIDs[id] = true
			}
			for _, record := range records {
				if !collisionIDs[record.ID] {
					indexIDs[record.ID] = true
				}
			}
			if err := s.embIdx.DeleteIDs(indexIDs); err != nil {
				return fmt.Errorf("memory embedding cleanup pending: %w", err)
			}
		}
		if len(collisionIDs) > 0 && (!blockScopeWrites || s.embIdx != nil) {
			return errors.New("memory scoped erasure pending: erased ID is shared with another record")
		}
		return nil
	})
	return removed, err
}

func containsMemoryIdentity(records []MemoryIdentity, wanted MemoryIdentity) bool {
	for _, record := range records {
		if record == wanted {
			return true
		}
	}
	return false
}

func eraseMarkdownRecords(contents string, ledger memoryRedactionLedger) (string, int, error) {
	lines := strings.SplitAfter(contents, "\n")
	var out strings.Builder
	removed := 0
	var section strings.Builder
	flush := func() {
		if section.Len() == 0 {
			return
		}
		rows := parseStructured(section.String(), time.Time{})
		if len(rows) == 1 && memoryPhysicallyRedacted(rows[0], ledger) {
			removed++
		} else {
			out.WriteString(section.String())
		}
		section.Reset()
	}
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r"), "### ") {
			flush()
			section.WriteString(line)
		} else if section.Len() > 0 {
			section.WriteString(line)
		} else {
			out.WriteString(line)
		}
	}
	flush()
	return out.String(), removed, nil
}

func writeTextAtomic(path, contents string) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".memory-rewrite-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err = tmp.WriteString(contents); err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(name, path)
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

	var storeErr error
	err := s.withStoreLock(func(dir string) error {
		ledger, err := readRedactionLedger(dir)
		if err != nil {
			return err
		}
		if memoryWriteBlocked(mem, ledger) {
			return errors.New("memory scope or ID was erased and cannot be restored")
		}
		return s.storeUnlocked(mem)
	})
	if err != nil {
		storeErr = err
	}
	if storeErr != nil {
		return "", storeErr
	}
	return mem.ID, nil
}

func (s *MarkdownStore) storeUnlocked(mem Memory) error {
	path := s.datePath(mem.CreatedAt)
	mu := s.dateMu(mem.CreatedAt)
	mu.Lock()
	defer mu.Unlock()

	// Ensure directory exists
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("create memory dir: %w", err)
	}

	// Append to file
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("open memory file: %w", err)
	}
	defer f.Close()

	entry := formatMemoryEntry(mem)
	if _, err := f.WriteString("\n" + entry + "\n"); err != nil {
		return fmt.Errorf("write memory entry: %w", err)
	}
	return nil
}

// Get retrieves a memory by ID prefix match in markdown files.
func (s *MarkdownStore) Get(ctx context.Context, id string) (*Memory, error) {
	var result *Memory
	err := s.withStoreLock(func(dir string) error {
		ledger, err := readRedactionLedger(dir)
		if err != nil {
			return err
		}
		if ledger.IDs[id] {
			return nil
		}
		result, err = s.getUnlocked(dir, id, ledger)
		return err
	})
	return result, err
}

func (s *MarkdownStore) getUnlocked(memDir, id string, ledger memoryRedactionLedger) (*Memory, error) {
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
			if (strings.HasPrefix(m.ID, id) || m.ID == id) && !memoryRedacted(m, ledger) {
				return &m, nil
			}
		}
	}
	return nil, nil // not found
}

// ListRecent returns the N most recent memories across all daily files.
// V84: Deduplicates by ID (keeps first occurrence), filters out junk entries
// (dream candidates, raw conversation dumps), and caps content size.
func (s *MarkdownStore) ListRecent(ctx context.Context, limit int) ([]Memory, error) {
	var result []Memory
	err := s.withStoreLock(func(memDir string) error {
		ledger, err := readRedactionLedger(memDir)
		if err != nil {
			return err
		}
		result, err = s.listRecentUnlocked(memDir, limit, ledger)
		return err
	})
	return result, err
}

func (s *MarkdownStore) listRecentUnlocked(memDir string, limit int, ledger memoryRedactionLedger) ([]Memory, error) {
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

	// A replacement records the ID it supersedes. The markdown format is
	// append-only, so apply that relation while building the current view.
	supersededIDs := make(map[string]struct{})
	for _, m := range all {
		if m.SupersedesID != "" {
			supersededIDs[m.SupersedesID] = struct{}{}
		}
	}

	// V84: Deduplicate by ID (keep first occurrence)
	seen := make(map[string]bool)
	deduped := make([]Memory, 0, len(all))
	for _, m := range all {
		if memoryRedacted(m, ledger) {
			continue
		}
		if !seen[m.ID] {
			seen[m.ID] = true
			deduped = append(deduped, m)
		}
	}

	// V84: Filter out junk entries and superseded memories
	filtered := make([]Memory, 0, len(deduped))
	for _, m := range deduped {
		if _, replaced := supersededIDs[m.ID]; replaced {
			continue
		}
		if isSuperseded(m) {
			continue
		}
		if isJunkEntry(m) {
			continue
		}
		// V84: Cap content size to prevent massive entries from dominating search
		if len(m.Content) > maxMemoryContentLen {
			m.Content = m.Content[:maxMemoryContentLen] + "..."
		}
		filtered = append(filtered, m)
	}

	// Sort by CreatedAt descending
	sort.Slice(filtered, func(i, j int) bool {
		return filtered[i].CreatedAt.After(filtered[j].CreatedAt)
	})

	if limit > 0 && len(filtered) > limit {
		filtered = filtered[:limit]
	}
	return filtered, nil
}

// maxMemoryContentLen caps memory content to prevent massive entries from dominating search.
// 2KB is enough for any meaningful memory while preventing 2MB entries from matching every query.
const maxMemoryContentLen = 2048

// isJunkEntry filters out entries that shouldn't be in search results:
// dream cycle candidates, raw conversation dumps, and other noise.
func isJunkEntry(m Memory) bool {
	// Filter dream cycle candidates (from OpenClawDreams)
	if strings.HasPrefix(m.Summary, "Candidate:") || strings.HasPrefix(m.Content, "Candidate:") {
		return true
	}
	// Filter entries that are just raw conversation metadata
	if strings.HasPrefix(m.Content, "Conversation info (untrusted metadata)") {
		return true
	}
	// Filter entries with completely empty content and summary
	if strings.TrimSpace(m.Content) == "" && strings.TrimSpace(m.Summary) == "" {
		return true
	}
	return false
}

// --- V85: BM25 Scoring + RRF Fusion + Multiplicative Recency ---
//
// Replaces V80's raw keyword frequency with proper BM25 (IDF + length normalization),
// fuses keyword and embedding results via Reciprocal Rank Fusion,
// and switches recency from additive (+5) to multiplicative (capped at 1.2x).

// bm25K1 controls term frequency saturation (0.0 = no TF bonus, 2.0 = aggressive).
// Standard value is 1.2.
const bm25K1 = 1.2

// bm25B controls length normalization (0.0 = ignore length, 1.0 = full normalization).
// Standard value is 0.75.
const bm25B = 0.75

// rrfK is the constant in RRF formula: score = 1/(k + rank). Standard is 60.
const rrfK = 60.0

// maxRecencyMultiplier caps the multiplicative recency boost.
// A value of 1.2 means today's memories get at most a 20% boost over old ones.
const maxRecencyMultiplier = 1.2

// recencyHalfLifeDays controls how fast recency decays. After this many days,
// the multiplier is halfway between 1.0 and maxRecencyMultiplier.
const recencyHalfLifeDays = 14.0

// idfCache holds precomputed IDF values for terms across all memories.
// Updated when the memory store is rebuilt.
type idfCache struct {
	idf   map[string]float64 // term -> IDF value
	dl    map[string]int     // memory ID -> document length (in terms)
	avgDl float64            // average document length across all memories
	valid bool               // whether cache is populated
}

// computeIDF builds IDF values from all memories.
// IDF = log((N - df + 0.5) / (df + 0.5) + 1) where N = total docs, df = docs containing term.
func computeIDF(memories []Memory) idfCache {
	N := len(memories)
	docFreq := make(map[string]int) // how many documents contain each term
	docLengths := make(map[string]int)
	var totalLength float64

	for _, m := range memories {
		text := strings.ToLower(m.Content + " " + m.Summary + " " + strings.Join(m.KeyTopics, " "))
		terms := strings.Fields(text)
		docLengths[m.ID] = len(terms)
		totalLength += float64(len(terms))

		// Count unique terms per document for IDF
		seen := make(map[string]bool)
		for _, t := range terms {
			if !seen[t] {
				seen[t] = true
				docFreq[t]++
			}
		}
	}

	idfVals := make(map[string]float64)
	for term, df := range docFreq {
		idfVals[term] = math.Log((float64(N)-float64(df)+0.5)/(float64(df)+0.5) + 1)
	}

	avgDl := 0.0
	if N > 0 {
		avgDl = totalLength / float64(N)
	}

	return idfCache{
		idf:   idfVals,
		dl:    docLengths,
		avgDl: avgDl,
		valid: true,
	}
}

// scoreBM25 computes the BM25 score for a memory given query terms.
// BM25 formula: sum over terms of: IDF(t) * (tf * (k1+1)) / (tf + k1 * (1 - b + b * dl/avgdl))
func scoreBM25(m Memory, terms []string, cache idfCache) float64 {
	text := strings.ToLower(m.Content + " " + m.Summary + " " + strings.Join(m.KeyTopics, " "))
	docTerms := strings.Fields(text)
	dl := float64(len(docTerms))
	avgDl := cache.avgDl
	if avgDl == 0 {
		avgDl = 100 // sensible default
	}

	// Count term frequencies in this document
	tfMap := make(map[string]int)
	for _, t := range docTerms {
		tfMap[t]++
	}

	var score float64
	for _, term := range terms {
		term = strings.ToLower(term)
		idf, hasIDF := cache.idf[term]
		if !hasIDF {
			idf = math.Log((float64(len(cache.dl))+0.5)/(0.5) + 1) // IDF for unseen terms
		}

		tf := float64(tfMap[term]) // 0 if term not in doc
		if tf == 0 {
			continue // term not in this document, skip
		}

		// BM25 term score
		numerator := tf * (bm25K1 + 1)
		denominator := tf + bm25K1*(1-bm25B+bm25B*(dl/avgDl))
		score += idf * numerator / denominator
	}

	// Category/title match bonus (additive, small)
	lowerCat := strings.ToLower(m.Category)
	for _, term := range terms {
		term = strings.ToLower(term)
		if strings.Contains(lowerCat, term) {
			score += 0.5 // small bonus for category match (was +3, now tiny relative to BM25)
		}
	}

	// KeyTopics match bonus
	for _, topic := range m.KeyTopics {
		lowerTopic := strings.ToLower(topic)
		for _, term := range terms {
			term = strings.ToLower(term)
			if strings.Contains(lowerTopic, term) || strings.Contains(term, lowerTopic) {
				score += 0.5 // small bonus for topic match
			}
		}
	}

	return score
}

// recencyMultiplier computes a multiplicative recency boost (1.0 to maxRecencyMultiplier).
// Uses exponential decay with configurable half-life.
// A memory from today gets maxRecencyMultiplier (1.2x).
// A memory from halfLifeDays ago gets ~1.1x.
// Very old memories get ~1.0x (no boost).
func recencyMultiplier(createdAt time.Time) float64 {
	daysSince := time.Since(createdAt).Hours() / 24
	if daysSince < 0 {
		daysSince = 0
	}
	// Exponential decay: multiplier = 1.0 + (maxRecencyMultiplier - 1.0) * 0.5^(daysSince/halfLife)
	delta := maxRecencyMultiplier - 1.0
	return 1.0 + delta*math.Pow(0.5, daysSince/recencyHalfLifeDays)
}

// Search performs hybrid BM25 + embedding search with RRF fusion.
func (s *MarkdownStore) Search(ctx context.Context, query string, limit int) ([]Memory, error) {
	var embeddingResults []EmbeddingSearchResult
	if s.embIdx != nil && query != "" {
		embeddingResults = s.embIdx.Search(ctx, query, limit*3)
	}
	var result []Memory
	err := s.withStoreLock(func(dir string) error {
		ledger, err := readRedactionLedger(dir)
		if err != nil {
			return err
		}
		all, err := s.listRecentUnlocked(dir, 0, ledger)
		if err != nil {
			return err
		}
		result, err = s.searchRecent(query, limit, all, embeddingResults)
		return err
	})
	return result, err
}

func (s *MarkdownStore) searchRecent(query string, limit int, all []Memory, embRaw []EmbeddingSearchResult) ([]Memory, error) {

	if query == "" {
		if limit > 0 && len(all) > limit {
			all = all[:limit]
		}
		return all, nil
	}

	terms := strings.Fields(strings.ToLower(query))

	// Build IDF cache from all memories
	cache := computeIDF(all)

	// --- Arm 1: BM25 keyword search ---
	type scored struct {
		Memory Memory
		Score  float64
	}
	var keywordResults []scored
	for _, m := range all {
		if isSuperseded(m) {
			continue
		}
		score := scoreBM25(m, terms, cache)
		if score > 0 {
			// Apply multiplicative recency boost
			score *= recencyMultiplier(m.CreatedAt)
			keywordResults = append(keywordResults, scored{Memory: m, Score: score})
		}
	}
	sort.Slice(keywordResults, func(i, j int) bool {
		return keywordResults[i].Score > keywordResults[j].Score
	})

	// --- Arm 2: Embedding semantic search ---
	var embeddingResults []Memory
	if s.embIdx != nil {
		// Get more candidates than needed for RRF fusion
		idMap := make(map[string]Memory)
		for _, m := range all {
			idMap[m.ID] = m
		}
		for _, r := range embRaw {
			if m, ok := idMap[r.ID]; ok {
				if !isSuperseded(m) {
					embeddingResults = append(embeddingResults, m)
				}
			}
		}
	}

	// --- RRF Fusion ---
	// Reciprocal Rank Fusion: score = sum over both arms of 1/(k + rank)
	rrfScores := make(map[string]float64)
	memoryByID := make(map[string]Memory)

	for rank, r := range keywordResults {
		id := r.Memory.ID
		rrfScores[id] += 1.0 / (rrfK + float64(rank))
		memoryByID[id] = r.Memory
	}
	for rank, m := range embeddingResults {
		id := m.ID
		rrfScores[id] += 1.0 / (rrfK + float64(rank))
		memoryByID[id] = m
	}

	// Sort by RRF score
	type rrfResult struct {
		ID    string
		Score float64
	}
	var fused []rrfResult
	for id, score := range rrfScores {
		fused = append(fused, rrfResult{ID: id, Score: score})
	}
	sort.Slice(fused, func(i, j int) bool {
		return fused[i].Score > fused[j].Score
	})

	var out []Memory
	for i, r := range fused {
		if limit > 0 && i >= limit {
			break
		}
		out = append(out, memoryByID[r.ID])
	}
	return out, nil
}

// SetEmbeddingIndex sets the embedding index for semantic search.
func (s *MarkdownStore) SetEmbeddingIndex(idx *EmbeddingIndex) {
	s.embIdx = idx
}

// EmbeddingSearch performs semantic search using the embedding index.
// Returns memories sorted by cosine similarity to the query.
// Returns nil if embeddings are unavailable.
func (s *MarkdownStore) EmbeddingSearch(ctx context.Context, query string, limit int) ([]Memory, error) {
	var embeddingResults []EmbeddingSearchResult
	if s.embIdx != nil {
		embeddingResults = s.embIdx.Search(ctx, query, limit*2)
	}
	var result []Memory
	err := s.withStoreLock(func(dir string) error {
		ledger, err := readRedactionLedger(dir)
		if err != nil {
			return err
		}
		all, err := s.listRecentUnlocked(dir, 0, ledger)
		if err != nil {
			return err
		}
		result, err = s.embeddingSearchRecent(query, limit, all, embeddingResults)
		return err
	})
	return result, err
}

func (s *MarkdownStore) embeddingSearchRecent(query string, limit int, all []Memory, results []EmbeddingSearchResult) ([]Memory, error) {
	if s.embIdx == nil {
		return nil, nil
	}
	if len(results) == 0 {
		return nil, nil
	}

	// Look up full Memory objects by ID
	idMap := make(map[string]Memory)
	for _, m := range all {
		idMap[m.ID] = m
	}

	var out []Memory
	for _, r := range results {
		if m, ok := idMap[r.ID]; ok {
			// Skip superseded memories
			if isSuperseded(m) {
				continue
			}
			if m.Metadata == nil {
				m.Metadata = make(map[string]string)
			}
			m.Metadata["embedding_score"] = fmt.Sprintf("%.4f", r.Score)
			out = append(out, m)
			if len(out) >= limit {
				break
			}
		}
	}

	return out, nil
}

// Close is a no-op for MarkdownStore.
func (s *MarkdownStore) Close() error { return nil }

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

	// V80: Extract YAML front matter if present
	fileMeta, contentWithoutFM := parseFrontMatter(content)

	// Strategy 1: Try structured ### ID — Summary format first
	if memories := parseStructured(contentWithoutFM, dateFromFilename); len(memories) > 0 {
		// Apply front matter to all memories in this file
		if fileMeta != nil {
			for i := range memories {
				applyFrontMatter(&memories[i], fileMeta)
			}
		}
		return memories, nil
	}

	// Strategy 2: Header-based extraction (any # through ######)
	if memories := parseByHeaders(contentWithoutFM, filename, dateFromFilename); len(memories) > 0 {
		if fileMeta != nil {
			for i := range memories {
				applyFrontMatter(&memories[i], fileMeta)
			}
		}
		return memories, nil
	}

	// Strategy 3: Paragraph-based (no headers — split on blank lines)
	if memories := parseByParagraphs(contentWithoutFM, filename, dateFromFilename); len(memories) > 0 {
		if fileMeta != nil {
			for i := range memories {
				applyFrontMatter(&memories[i], fileMeta)
			}
		}
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
			case "User":
				current.UserID = val
			case "Task":
				current.TaskID = val
			case "Supersedes":
				current.SupersedesID = val
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
			ID:         slugify(summary),
			Summary:    summary,
			Content:    body,
			CreatedAt:  date,
			AccessedAt: date,
		}
		memories = append(memories, mem)
	}

	return memories
}

// sectionBuilder helps construct a Memory from a header-based section.
type sectionBuilder struct {
	id      string
	summary string
	lines   []string
	meta    map[string]string // key-value metadata from - **Key:** Value lines
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
		ID:         sb.id,
		Summary:    sb.summary,
		Content:    content,
		CreatedAt:  date,
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
	if m.UserID != "" {
		fmt.Fprintf(&b, "\n- **User:** %s", m.UserID)
	}
	if m.TaskID != "" {
		fmt.Fprintf(&b, "\n- **Task:** %s", m.TaskID)
	}
	if m.SupersedesID != "" {
		fmt.Fprintf(&b, "\n- **Supersedes:** %s", m.SupersedesID)
	}
	if len(m.KeyTopics) > 0 {
		fmt.Fprintf(&b, "\n- **Key Topics:** %s", strings.Join(m.KeyTopics, ", "))
	}
	fmt.Fprintf(&b, "\n\n%s", m.Content)
	return b.String()
}
