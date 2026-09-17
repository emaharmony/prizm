// Package improve implements the V32 Self-Improvement Loop.
//
// The self-improvement loop observes Prizm's own behavior and creates
// improvement proposals when it detects patterns:
//   - Error pattern detection: same error 3+ times → create fix proposal
//   - Process violation logging: skipped reviews, scope drift
//   - Self-review at end of task: was the plan followed? was scope maintained?
//   - Auto-PR creation: guard rail creates a branch, writes fix, opens PR
//
// This is NOT a second LLM that reviews everything. It's a lightweight event
// subscriber that:
//  1. Counts error occurrences (no LLM needed)
//  2. Flags repeated patterns (simple counting)
//  3. Creates improvement proposals (JSON state, not LLM output)
//  4. Optionally triggers a wake event for LLM review
package improve

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/emaharmony/prizm/internal/plan"
)

// ImprovementCategory classifies the type of improvement.
type ImprovementCategory string

const (
	CategoryBugFix       ImprovementCategory = "bug_fix"
	CategoryRefactor     ImprovementCategory = "refactor"
	CategoryPerformance  ImprovementCategory = "performance"
	CategoryProcessFix   ImprovementCategory = "process_fix"
	CategoryErrorPattern ImprovementCategory = "error_pattern"
	CategoryTestCoverage ImprovementCategory = "test_coverage"
	CategoryDocUpdate    ImprovementCategory = "doc_update"
)

// ImprovementStatus tracks the lifecycle of an improvement proposal.
type ImprovementStatus string

const (
	StatusProposed   ImprovementStatus = "proposed"
	StatusInReview   ImprovementStatus = "in_review"
	StatusInProgress ImprovementStatus = "in_progress"
	StatusCompleted  ImprovementStatus = "completed"
	StatusDismissed  ImprovementStatus = "dismissed"
	StatusDuplicate  ImprovementStatus = "duplicate"
)

// ErrorPattern tracks recurring errors for pattern detection.
type ErrorPattern struct {
	ErrorType string    `json:"error_type"` // Short identifier (e.g., "map_iteration_bug")
	Message   string    `json:"message"`    // Error message pattern
	Count     int       `json:"count"`      // Number of occurrences
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
	Source    string    `json:"source"` // Which tool/component generated it
}

// Improvement represents a proposed improvement.
type Improvement struct {
	ID          string              `json:"id"`
	Title       string              `json:"title"`
	Description string              `json:"description"`
	Category    ImprovementCategory `json:"category"`
	Status      ImprovementStatus   `json:"status"`
	Priority    int                 `json:"priority"`          // 1=critical, 2=high, 3=medium, 4=low
	Source      string              `json:"source"`            // What triggered this (error_pattern, self_review, process_violation)
	PlanID      string              `json:"plan_id,omitempty"` // Linked plan if one was created
	CreatedAt   time.Time           `json:"created_at"`
	UpdatedAt   time.Time           `json:"updated_at"`
}

// ProcessViolation records a process rule violation.
type ProcessViolation struct {
	ID          string    `json:"id"`
	Rule        string    `json:"rule"`        // Which rule was violated
	Description string    `json:"description"` // What happened
	Severity    string    `json:"severity"`    // low, medium, high
	Timestamp   time.Time `json:"timestamp"`
}

// Manager manages error patterns, improvements, and process violations.
type Manager struct {
	stateDir      string
	mu            sync.RWMutex
	errorPatterns map[string]*ErrorPattern // keyed by error_type
	improvements  []Improvement
	violations    []ProcessViolation
}

// NewManager creates an improvement manager.
func NewManager(workspaceDir string) *Manager {
	return &Manager{
		stateDir:      filepath.Join(workspaceDir, "state"),
		errorPatterns: make(map[string]*ErrorPattern),
	}
}

// EnsureDir creates the state directory if it doesn't exist.
func (m *Manager) EnsureDir() error {
	return os.MkdirAll(m.stateDir, 0755)
}

// RecordError tracks an error occurrence. Returns the current count.
// If the same error type occurs 3+ times, it returns a non-nil improvement proposal.
func (m *Manager) RecordError(errorType, message, source string) *Improvement {
	m.mu.Lock()
	defer m.mu.Unlock()

	pattern, exists := m.errorPatterns[errorType]
	if !exists {
		pattern = &ErrorPattern{
			ErrorType: errorType,
			Message:   message,
			Source:    source,
			FirstSeen: time.Now(),
		}
		m.errorPatterns[errorType] = pattern
	}
	pattern.Count++
	pattern.LastSeen = time.Now()
	if message != "" {
		pattern.Message = message
	}

	// Threshold: 3 occurrences → create improvement proposal
	if pattern.Count == 3 {
		improvement := &Improvement{
			ID:          fmt.Sprintf("IMP-%03d", len(m.improvements)+1),
			Title:       fmt.Sprintf("Fix recurring error: %s", errorType),
			Description: fmt.Sprintf("Error '%s' has occurred %d times since %s. Most recent: %s. Source: %s", errorType, pattern.Count, pattern.FirstSeen.Format("Jan 02"), message, source),
			Category:    CategoryErrorPattern,
			Status:      StatusProposed,
			Priority:    2, // High
			Source:      "error_pattern",
			CreatedAt:   time.Now(),
			UpdatedAt:   time.Now(),
		}
		m.improvements = append(m.improvements, *improvement)
		m.saveImprovementsLocked()
		return improvement
	}

	return nil
}

// RecordViolation records a process violation.
func (m *Manager) RecordViolation(rule, description, severity string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	violation := ProcessViolation{
		ID:          fmt.Sprintf("PV-%03d", len(m.violations)+1),
		Rule:        rule,
		Description: description,
		Severity:    severity,
		Timestamp:   time.Now(),
	}
	m.violations = append(m.violations, violation)
	m.saveViolationsLocked()
}

// GetImprovements returns all improvement proposals.
func (m *Manager) GetImprovements() []Improvement {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return append([]Improvement(nil), m.improvements...)
}

// GetActiveImprovements returns non-completed, non-dismissed improvements.
func (m *Manager) GetActiveImprovements() []Improvement {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var active []Improvement
	for _, imp := range m.improvements {
		if imp.Status != StatusCompleted && imp.Status != StatusDismissed && imp.Status != StatusDuplicate {
			active = append(active, imp)
		}
	}
	return active
}

// GetErrorPatterns returns all tracked error patterns.
func (m *Manager) GetErrorPatterns() []ErrorPattern {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var patterns []ErrorPattern
	for _, p := range m.errorPatterns {
		patterns = append(patterns, *p)
	}
	return patterns
}

// GetViolations returns all process violations.
func (m *Manager) GetViolations() []ProcessViolation {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return append([]ProcessViolation(nil), m.violations...)
}

// DismissImprovement marks an improvement as dismissed.
func (m *Manager) DismissImprovement(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for i := range m.improvements {
		if m.improvements[i].ID == id {
			m.improvements[i].Status = StatusDismissed
			m.improvements[i].UpdatedAt = time.Now()
			return m.saveImprovementsLocked()
		}
	}
	return fmt.Errorf("improvement %s not found", id)
}

// LinkPlanToImprovement links a plan to an improvement proposal.
func (m *Manager) LinkPlanToImprovement(improvementID, planID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for i := range m.improvements {
		if m.improvements[i].ID == improvementID {
			m.improvements[i].PlanID = planID
			m.improvements[i].UpdatedAt = time.Now()
			return m.saveImprovementsLocked()
		}
	}
	return fmt.Errorf("improvement %s not found", improvementID)
}

// ShouldAutoPR determines if an improvement should automatically create a PR.
// This implements Ema's V32 decisions:
//   - bugs → auto-proceed
//   - improvements → auto-proceed
//   - architecture changes → require approval
func ShouldAutoPR(improvement Improvement) bool {
	switch improvement.Category {
	case CategoryBugFix, CategoryErrorPattern, CategoryTestCoverage, CategoryDocUpdate:
		return true
	case CategoryRefactor, CategoryPerformance:
		// Only auto-PR if priority is low or medium
		return improvement.Priority >= 3
	case CategoryProcessFix:
		return false // Process fixes always need approval
	default:
		return false
	}
}

// ApprovalLevelForImprovement maps an improvement to a plan approval level.
func ApprovalLevelForImprovement(improvement Improvement) plan.ApprovalLevel {
	switch improvement.Category {
	case CategoryBugFix, CategoryErrorPattern, CategoryTestCoverage, CategoryDocUpdate:
		return plan.ApprovalAuto // Auto-proceed for bugs and small fixes
	case CategoryRefactor, CategoryPerformance:
		if improvement.Priority <= 2 {
			return plan.ApprovalRequired // High-priority refactors need approval
		}
		return plan.ApprovalAuto
	case CategoryProcessFix:
		return plan.ApprovalRequired // Process changes always need approval
	default:
		return plan.ApprovalAuto
	}
}

// FormatImprovementsForPrompt formats active improvements for LLM injection.
func FormatImprovementsForPrompt(improvements []Improvement) string {
	if len(improvements) == 0 {
		return "No active improvement proposals."
	}

	var result string
	result = "## Active Improvement Proposals\n"
	for _, imp := range improvements {
		result += fmt.Sprintf("- **%s** [%s]: %s (priority: %d, auto-PR: %v)\n",
			imp.ID, imp.Category, imp.Title, imp.Priority, ShouldAutoPR(imp))
		if imp.PlanID != "" {
			result += fmt.Sprintf("  Linked plan: %s\n", imp.PlanID)
		}
	}
	return result
}

// FormatErrorPatternsForPrompt formats error patterns for LLM injection.
func FormatErrorPatternsForPrompt(patterns []ErrorPattern) string {
	if len(patterns) == 0 {
		return "No recurring error patterns detected."
	}

	var result string
	result = "## Error Patterns\n"
	for _, p := range patterns {
		if p.Count >= 2 {
			result += fmt.Sprintf("- **%s**: %d occurrences (first: %s, last: %s) — %s\n",
				p.ErrorType, p.Count,
				p.FirstSeen.Format("Jan 02 15:04"),
				p.LastSeen.Format("Jan 02 15:04"),
				p.Message)
		}
	}
	return result
}

// saveImprovementsLocked persists improvements. Caller must hold m.mu.
func (m *Manager) saveImprovementsLocked() error {
	data, err := json.MarshalIndent(m.improvements, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal improvements: %w", err)
	}
	return os.WriteFile(filepath.Join(m.stateDir, "improvements.json"), data, 0644)
}

// saveViolationsLocked persists violations. Caller must hold m.mu.
func (m *Manager) saveViolationsLocked() error {
	data, err := json.MarshalIndent(m.violations, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal violations: %w", err)
	}
	return os.WriteFile(filepath.Join(m.stateDir, "violations.json"), data, 0644)
}

// LoadState loads improvements and violations from disk.
func (m *Manager) LoadState() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Load improvements
	impData, err := os.ReadFile(filepath.Join(m.stateDir, "improvements.json"))
	if err == nil {
		json.Unmarshal(impData, &m.improvements)
	}

	// Load violations
	violData, err := os.ReadFile(filepath.Join(m.stateDir, "violations.json"))
	if err == nil {
		json.Unmarshal(violData, &m.violations)
	}

	// Error patterns are ephemeral — not persisted
	return nil
}
