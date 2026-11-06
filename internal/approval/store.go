package approval

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/gofrs/flock"
)

// Store provides file-based persistence for approvals under
// runs/<run_id>/approvals/<approval_id>.json.
type Store struct {
	mu      sync.RWMutex
	runsDir string
}

// NewStore creates a new file-based approval store.
// runsDir is the base directory containing run directories (e.g., "./runs").
func NewStore(runsDir string) *Store {
	return &Store{runsDir: runsDir}
}

// approvalDir returns the approvals directory for a given run.
func (s *Store) approvalDir(runID string) string {
	return filepath.Join(s.runsDir, runID, "approvals")
}

// approvalPath returns the full path for a given approval file.
func (s *Store) approvalPath(runID, approvalID string) string {
	return filepath.Join(s.approvalDir(runID), fmt.Sprintf("%s.json", approvalID))
}

// Save persists an approval to disk.
func (s *Store) Save(a *Approval) error {
	if err := a.ValidateSubject(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	dir := s.approvalDir(a.RunID)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create approvals directory: %w", err)
	}
	lock, err := acquireApprovalStoreLock(dir)
	if err != nil {
		return err
	}
	defer lock.Unlock()
	path := s.approvalPath(a.RunID, a.ApprovalID)
	if priorBytes, err := os.ReadFile(path); err == nil {
		var prior Approval
		if json.Unmarshal(priorBytes, &prior) != nil || prior.ValidateSubject() != nil {
			return fmt.Errorf("existing approval record is invalid")
		}
		if prior.SubjectType == SubjectIntention && (a.SubjectType != SubjectIntention || prior.RunID != a.RunID ||
			prior.ApprovalID != a.ApprovalID || prior.ProposalID != a.ProposalID || prior.Intention == nil || a.Intention == nil ||
			!reflect.DeepEqual(prior.Intention, a.Intention) || prior.Status != StatusPending && prior.Status != a.Status) {
			return fmt.Errorf("intention approval identity is immutable")
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("failed to inspect existing approval: %w", err)
	}
	data, err := json.MarshalIndent(a, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal approval: %w", err)
	}

	if err := os.WriteFile(path, append(data, '\n'), 0644); err != nil {
		return fmt.Errorf("failed to write approval file: %w", err)
	}

	return nil
}

// SaveIdempotent atomically creates a host-keyed approval or returns the
// existing immutable record. Unlike Save, it never replaces a decision.
// Publication uses a per-run interprocess lock and same-directory atomic
// rename so crashes cannot leave a partially written final approval file.
func (s *Store) SaveIdempotent(a *Approval) (*Approval, bool, error) {
	if a == nil || strings.TrimSpace(a.IdempotencyKey) == "" || len(a.IdempotencyKey) > 256 {
		return nil, false, errors.New("approval idempotency key is required")
	}
	if err := a.ValidateSubject(); err != nil {
		return nil, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	dir := s.approvalDir(a.RunID)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, false, fmt.Errorf("failed to create approvals directory: %w", err)
	}
	lock, err := acquireApprovalStoreLock(dir)
	if err != nil {
		return nil, false, err
	}
	defer lock.Unlock()
	if err := removeStaleIdempotentTemps(dir); err != nil {
		return nil, false, err
	}
	path := s.approvalPath(a.RunID, a.ApprovalID)
	if prior, err := readApproval(path); err == nil {
		if err := sameIdempotentRequest(prior, a); err != nil {
			return nil, false, err
		}
		return prior, false, nil
	} else if !os.IsNotExist(err) {
		return nil, false, err
	}

	data, err := json.MarshalIndent(a, "", "  ")
	if err != nil {
		return nil, false, err
	}
	tmp, err := os.CreateTemp(dir, ".approval-idempotent-*.tmp")
	if err != nil {
		return nil, false, fmt.Errorf("failed to stage approval: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		_ = tmp.Close()
		return nil, false, fmt.Errorf("failed to write staged approval: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return nil, false, fmt.Errorf("failed to sync staged approval: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return nil, false, fmt.Errorf("failed to close staged approval: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return nil, false, fmt.Errorf("failed to publish idempotent approval: %w", err)
	}
	return a, true, nil
}

func acquireApprovalStoreLock(dir string) (*flock.Flock, error) {
	lock := flock.New(filepath.Join(dir, ".approval-store.lock"))
	if err := lock.Lock(); err != nil {
		return nil, fmt.Errorf("failed to lock approval store: %w", err)
	}
	return lock, nil
}

func removeStaleIdempotentTemps(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("failed to inspect approval staging files: %w", err)
	}
	cutoff := time.Now().Add(-24 * time.Hour)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), ".approval-idempotent-") || !strings.HasSuffix(entry.Name(), ".tmp") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		info, err := os.Lstat(path)
		if err != nil {
			return fmt.Errorf("failed to inspect approval staging file: %w", err)
		}
		if !info.Mode().IsRegular() || info.ModTime().After(cutoff) {
			continue
		}
		if err := os.Remove(path); err != nil {
			return fmt.Errorf("failed to remove stale approval staging file: %w", err)
		}
	}
	return nil
}

func readApproval(path string) (*Approval, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var prior Approval
	if err := json.Unmarshal(data, &prior); err != nil {
		return nil, fmt.Errorf("existing approval record is invalid")
	}
	if err := prior.ValidateSubject(); err != nil {
		return nil, fmt.Errorf("existing approval record is invalid")
	}
	return &prior, nil
}

func sameIdempotentRequest(prior, next *Approval) error {
	if prior.IdempotencyKey != next.IdempotencyKey || prior.ApprovalID != next.ApprovalID ||
		prior.ProposalID != next.ProposalID || prior.RunID != next.RunID ||
		prior.CorrelationID != next.CorrelationID || prior.RequestedBy != next.RequestedBy ||
		prior.Project != next.Project || prior.MutationType != next.MutationType ||
		prior.TargetPath != next.TargetPath || prior.Content != next.Content ||
		prior.ToolName != next.ToolName || !reflect.DeepEqual(prior.Input, next.Input) ||
		!reflect.DeepEqual(prior.PatchPlan, next.PatchPlan) || !reflect.DeepEqual(prior.SandboxBinding, next.SandboxBinding) {
		return errors.New("idempotent approval key is already bound to a different proposal")
	}
	return nil
}

// Load reads an approval from disk.
// Returns nil and an error if the approval does not exist.
func (s *Store) Load(runID, approvalID string) (*Approval, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	path := s.approvalPath(runID, approvalID)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("approval %s not found", approvalID)
		}
		return nil, fmt.Errorf("failed to read approval: %w", err)
	}

	var a Approval
	if err := json.Unmarshal(data, &a); err != nil {
		return nil, fmt.Errorf("failed to unmarshal approval: %w", err)
	}
	if err := a.ValidateSubject(); err != nil {
		return nil, fmt.Errorf("invalid approval subject")
	}

	return &a, nil
}

// List returns all approvals for a given run, sorted by creation time.
func (s *Store) List(runID string) ([]*Approval, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	dir := s.approvalDir(runID)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read approvals directory: %w", err)
	}

	var approvals []*Approval
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue
		}
		var a Approval
		if err := json.Unmarshal(data, &a); err != nil {
			continue
		}
		if err := a.ValidateSubject(); err != nil {
			continue
		}
		approvals = append(approvals, &a)
	}

	return approvals, nil
}
