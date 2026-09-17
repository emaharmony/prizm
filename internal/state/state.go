// Package state provides persistent working state for Prizm agents.
// The Manager reads and writes JSON state files in <workspace>/state/:
//
//   - active-task.json: current task, plan, scope, status
//   - decisions.json: decision log with timestamps and reasoning
//   - blocked.json: items waiting on external input
//   - context.json: current working context (branch, files, PR)
//
// V32: Lumi Operating Environment — Phase 1
//
// State files are the "what was I doing?" layer between short-term
// session memory and long-term Remembrance recall. They're injected
// into the LLM prompt before MEMORY.md so the agent wakes up knowing
// its current task and context.
package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// ActiveTask holds the current task context — what the agent is doing right now.
type ActiveTask struct {
	Task      string    `json:"task"`       // What we're working on
	Plan      string    `json:"plan"`       // The plan (what we'll do, why, what we won't do)
	Scope     string    `json:"scope"`      // Scope boundary — what's explicitly out of scope
	StartedAt time.Time `json:"started_at"` // When this task began
	UpdatedAt time.Time `json:"updated_at"` // Last update
	Branch    string    `json:"branch"`     // Git branch if applicable
	PR        string    `json:"pr"`         // PR number if applicable
	Status    string    `json:"status"`     // "planning", "executing", "reviewing", "done", "blocked"
}

// Decision records a decision that was made, with context about why.
type Decision struct {
	ID           string    `json:"id"`           // Unique ID (e.g., "D-001")
	Decision     string    `json:"decision"`     // What was decided
	Reasoning    string    `json:"reasoning"`    // Why
	Alternatives string    `json:"alternatives"` // What was considered and rejected
	Author       string    `json:"author"`       // Who made it ("lumi", "ema", "guard")
	Timestamp    time.Time `json:"timestamp"`
}

// BlockedItem records something that's waiting on external input.
type BlockedItem struct {
	ID        string    `json:"id"`         // Unique ID (e.g., "B-001")
	Item      string    `json:"item"`       // What's blocked
	WaitingOn string    `json:"waiting_on"` // What it's waiting for ("ema approval", "mango review")
	Since     time.Time `json:"since"`      // When it became blocked
	TaskRef   string    `json:"task_ref"`   // Reference to active task
}

// WorkingContext holds the current working context — files, branch, recent actions.
type WorkingContext struct {
	Branch       string    `json:"branch"`      // Current git branch
	LastAction   string    `json:"last_action"` // What was just done
	LastActionAt time.Time `json:"last_action_at"`
	OpenFiles    []string  `json:"open_files"` // Files currently being worked on
	PR           string    `json:"pr"`         // Current PR if any
	Notes        string    `json:"notes"`      // Freeform context notes
}

// Manager handles reading and writing state files in a workspace directory.
type Manager struct {
	stateDir string
	mu       sync.RWMutex
}

// NewManager creates a state manager for the given workspace directory.
// State files are stored in <workspace>/state/
func NewManager(workspaceDir string) *Manager {
	return &Manager{
		stateDir: filepath.Join(workspaceDir, "state"),
	}
}

// EnsureDir creates the state directory if it doesn't exist.
func (m *Manager) EnsureDir() error {
	return os.MkdirAll(m.stateDir, 0755)
}

// StateDir returns the state directory path.
func (m *Manager) StateDir() string {
	return m.stateDir
}

// --- Active Task ---

// LoadActiveTask reads the current active task from state.
func (m *Manager) LoadActiveTask() (*ActiveTask, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return m.loadActiveTaskLocked()
}

func (m *Manager) loadActiveTaskLocked() (*ActiveTask, error) {
	data, err := os.ReadFile(filepath.Join(m.stateDir, "active-task.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // No active task — that's fine
		}
		return nil, fmt.Errorf("read active task: %w", err)
	}

	var task ActiveTask
	if err := json.Unmarshal(data, &task); err != nil {
		return nil, fmt.Errorf("parse active task: %w", err)
	}
	return &task, nil
}

// SaveActiveTask writes the current active task to state.
func (m *Manager) SaveActiveTask(task *ActiveTask) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := m.EnsureDir(); err != nil {
		return err
	}

	task.UpdatedAt = time.Now()
	if task.StartedAt.IsZero() {
		task.StartedAt = time.Now()
	}

	data, err := json.MarshalIndent(task, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal active task: %w", err)
	}

	return os.WriteFile(filepath.Join(m.stateDir, "active-task.json"), data, 0644)
}

// ClearActiveTask removes the active task (task is done).
func (m *Manager) ClearActiveTask() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	path := filepath.Join(m.stateDir, "active-task.json")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil // Already clear
	}
	return os.Remove(path)
}

// --- Decisions ---

// LoadDecisions reads all recorded decisions.
func (m *Manager) LoadDecisions() ([]Decision, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return m.loadDecisionsLocked()
}

// loadDecisionsLocked reads decisions without acquiring the lock.
// Caller must hold m.mu.
func (m *Manager) loadDecisionsLocked() ([]Decision, error) {
	data, err := os.ReadFile(filepath.Join(m.stateDir, "decisions.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read decisions: %w", err)
	}

	var decisions []Decision
	if err := json.Unmarshal(data, &decisions); err != nil {
		return nil, fmt.Errorf("parse decisions: %w", err)
	}
	return decisions, nil
}

// RecordDecision adds a new decision to the log.
func (m *Manager) RecordDecision(d Decision) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := m.EnsureDir(); err != nil {
		return err
	}

	decisions, _ := m.loadDecisionsLocked()
	if d.ID == "" {
		d.ID = fmt.Sprintf("D-%03d", len(decisions)+1)
	}
	if d.Timestamp.IsZero() {
		d.Timestamp = time.Now()
	}
	decisions = append(decisions, d)

	data, err := json.MarshalIndent(decisions, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal decisions: %w", err)
	}

	return os.WriteFile(filepath.Join(m.stateDir, "decisions.json"), data, 0644)
}

// --- Blocked Items ---

// LoadBlocked reads all blocked items.
func (m *Manager) LoadBlocked() ([]BlockedItem, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return m.loadBlockedLocked()
}

// loadBlockedLocked reads blocked items without acquiring the lock.
// Caller must hold m.mu.
func (m *Manager) loadBlockedLocked() ([]BlockedItem, error) {
	data, err := os.ReadFile(filepath.Join(m.stateDir, "blocked.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read blocked: %w", err)
	}

	var items []BlockedItem
	if err := json.Unmarshal(data, &items); err != nil {
		return nil, fmt.Errorf("parse blocked: %w", err)
	}
	return items, nil
}

// AddBlocked adds a new blocked item.
func (m *Manager) AddBlocked(item BlockedItem) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := m.EnsureDir(); err != nil {
		return err
	}

	items, _ := m.loadBlockedLocked()
	if item.ID == "" {
		item.ID = fmt.Sprintf("B-%03d", len(items)+1)
	}
	if item.Since.IsZero() {
		item.Since = time.Now()
	}
	items = append(items, item)

	data, err := json.MarshalIndent(items, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal blocked: %w", err)
	}

	return os.WriteFile(filepath.Join(m.stateDir, "blocked.json"), data, 0644)
}

// Unblock removes a blocked item by ID.
func (m *Manager) Unblock(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	items, err := m.loadBlockedLocked()
	if err != nil {
		return err
	}

	filtered := make([]BlockedItem, 0, len(items))
	for _, item := range items {
		if item.ID != id {
			filtered = append(filtered, item)
		}
	}

	data, err := json.MarshalIndent(filtered, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal blocked: %w", err)
	}

	return os.WriteFile(filepath.Join(m.stateDir, "blocked.json"), data, 0644)
}

// --- Working Context ---

// LoadContext reads the current working context.
func (m *Manager) LoadContext() (*WorkingContext, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return m.loadContextLocked()
}

// loadContextLocked reads context without acquiring the lock.
// Caller must hold m.mu.
func (m *Manager) loadContextLocked() (*WorkingContext, error) {
	data, err := os.ReadFile(filepath.Join(m.stateDir, "context.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read context: %w", err)
	}

	var ctx WorkingContext
	if err := json.Unmarshal(data, &ctx); err != nil {
		return nil, fmt.Errorf("parse context: %w", err)
	}
	return &ctx, nil
}

// SaveContext writes the current working context.
func (m *Manager) SaveContext(ctx *WorkingContext) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := m.EnsureDir(); err != nil {
		return err
	}

	data, err := json.MarshalIndent(ctx, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal context: %w", err)
	}

	return os.WriteFile(filepath.Join(m.stateDir, "context.json"), data, 0644)
}

// --- Human-Readable Summaries ---

// FormatStateForPrompt loads all state and formats it for injection into an LLM prompt.
// Uses atomic reads (single RLock) to ensure consistency across all state files.
// Returns an empty string if no state exists.
func (m *Manager) FormatStateForPrompt() string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	task, _ := m.loadActiveTaskLocked()
	decisions, _ := m.loadDecisionsLocked()
	blocked, _ := m.loadBlockedLocked()
	ctx, _ := m.loadContextLocked()

	var sb string
	if task != nil {
		sb += FormatActiveTask(task)
	}
	if len(decisions) > 0 {
		sb += "\n## Recent Decisions\n"
		sb += FormatDecisions(decisions, 5)
	}
	if len(blocked) > 0 {
		sb += "\n## Blocked\n"
		sb += FormatBlocked(blocked)
	}
	if ctx != nil {
		sb += "\n" + FormatContext(ctx)
	}

	return sb
}

// FormatActiveTask returns a human-readable summary of the active task.
func FormatActiveTask(task *ActiveTask) string {
	if task == nil {
		return ""
	}
	return fmt.Sprintf("## Active Task\n- **Task:** %s\n- **Status:** %s\n- **Plan:** %s\n- **Scope:** %s\n- **Branch:** %s\n- **Started:** %s\n- **Updated:** %s\n",
		task.Task, task.Status, task.Plan, task.Scope, task.Branch,
		task.StartedAt.Format("2006-01-02 15:04"), task.UpdatedAt.Format("2006-01-02 15:04"))
}

// FormatDecisions returns a human-readable summary of recent decisions.
func FormatDecisions(decisions []Decision, limit int) string {
	if len(decisions) == 0 {
		return ""
	}
	if limit > 0 && len(decisions) > limit {
		decisions = decisions[len(decisions)-limit:]
	}
	var result string
	for _, d := range decisions {
		result += fmt.Sprintf("- **%s** (%s, %s): %s\n", d.ID, d.Author, d.Timestamp.Format("2006-01-02"), d.Decision)
		if d.Reasoning != "" {
			result += fmt.Sprintf("  Reason: %s\n", d.Reasoning)
		}
	}
	return result
}

// FormatBlocked returns a human-readable summary of blocked items.
func FormatBlocked(items []BlockedItem) string {
	if len(items) == 0 {
		return ""
	}
	var result string
	for _, item := range items {
		result += fmt.Sprintf("- **%s**: %s (waiting on: %s, since %s)\n",
			item.ID, item.Item, item.WaitingOn, item.Since.Format("2006-01-02"))
	}
	return result
}

// FormatContext returns a human-readable summary of the working context.
func FormatContext(ctx *WorkingContext) string {
	if ctx == nil {
		return ""
	}
	result := fmt.Sprintf("## Working Context\n- **Branch:** %s\n- **Last action:** %s (%s)\n",
		ctx.Branch, ctx.LastAction, ctx.LastActionAt.Format("2006-01-02 15:04"))
	if ctx.PR != "" {
		result += fmt.Sprintf("- **PR:** %s\n", ctx.PR)
	}
	if len(ctx.OpenFiles) > 0 {
		result += "- **Open files:**\n"
		for _, f := range ctx.OpenFiles {
			result += fmt.Sprintf("  - %s\n", f)
		}
	}
	if ctx.Notes != "" {
		result += fmt.Sprintf("- **Notes:** %s\n", ctx.Notes)
	}
	return result
}
