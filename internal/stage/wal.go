// Package stage provides Prizm's pipeline execution engine (V14a+).
//
// V14b adds crash recovery via Write-Ahead Log (WAL). Before every stage
// transition, the pipeline writes a WAL entry to runs/<id>/wal.jsonl.
// On crash, prizm run --recover <id> replays the WAL and resumes from the
// last completed stage.
//
// WAL entries use the same Event struct with wal.* type namespace,
// making them queryable by the same projections and dashboard.
package stage

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// WALEntry represents a single write-ahead log entry.
// It records stage transitions so the pipeline can recover from crashes.
type WALEntry struct {
	Type      string         `json:"type"`      // wal.stage.entered, wal.stage.completed, wal.mutation.applied
	Source    string         `json:"source"`    // "pipeline"
	Timestamp string         `json:"timestamp"` // ISO 8601
	Payload   map[string]any `json:"payload"`
}

// WALWriter writes WAL entries to a file with fsync guarantees.
// Each entry is a JSON line, making the WAL append-only and recoverable.
type WALWriter struct {
	mu    sync.Mutex
	file  *os.File
	path  string
	runID string
}

// NewWALWriter creates a new WAL writer for a run.
// The WAL file is created at <runDir>/wal.jsonl.
func NewWALWriter(runDir, runID string) (*WALWriter, error) {
	path := filepath.Join(runDir, "wal.jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, fmt.Errorf("wal: open file: %w", err)
	}
	return &WALWriter{
		file:  f,
		path:  path,
		runID: runID,
	}, nil
}

// WriteEntry writes a WAL entry and fsyncs to disk.
// This ensures the entry is durable before the stage proceeds.
func (w *WALWriter) WriteEntry(entryType string, payload map[string]any) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	entry := WALEntry{
		Type:      entryType,
		Source:    "pipeline",
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		Payload:   payload,
	}

	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("wal: marshal: %w", err)
	}

	if _, err := w.file.Write(data); err != nil {
		return fmt.Errorf("wal: write: %w", err)
	}
	if _, err := w.file.Write([]byte("\n")); err != nil {
		return fmt.Errorf("wal: newline: %w", err)
	}

	// fsync to ensure durability before stage proceeds
	if err := w.file.Sync(); err != nil {
		return fmt.Errorf("wal: fsync: %w", err)
	}

	return nil
}

// StageEntered records that a stage is about to start executing.
func (w *WALWriter) StageEntered(stageName string, stageIndex int) error {
	return w.WriteEntry("wal.stage.entered", map[string]any{
		"run_id":      w.runID,
		"stage":       stageName,
		"stage_index": stageIndex,
	})
}

// StageCompleted records that a stage has finished executing.
func (w *WALWriter) StageCompleted(stageName string, stageIndex int, success bool) error {
	return w.WriteEntry("wal.stage.completed", map[string]any{
		"run_id":      w.runID,
		"stage":       stageName,
		"stage_index": stageIndex,
		"success":     success,
	})
}

// MutationApplied records that a mutation has been applied (for idempotency).
func (w *WALWriter) MutationApplied(mutationKey string, targetPath string) error {
	return w.WriteEntry("wal.mutation.applied", map[string]any{
		"run_id":       w.runID,
		"mutation_key": mutationKey,
		"target_path":  targetPath,
	})
}

// Close flushes and closes the WAL file.
func (w *WALWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.file.Close()
}

// WALReader reads WAL entries for crash recovery.
type WALReader struct {
	path string
}

// NewWALReader creates a new WAL reader for a run directory.
func NewWALReader(runDir string) *WALReader {
	return &WALReader{
		path: filepath.Join(runDir, "wal.jsonl"),
	}
}

// ReadEntries reads all WAL entries from the file.
// Returns entries in order (oldest first).
func (r *WALReader) ReadEntries() ([]WALEntry, error) {
	data, err := os.ReadFile(r.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // No WAL file = first run, not an error
		}
		return nil, fmt.Errorf("wal: read: %w", err)
	}

	var entries []WALEntry
	lines := splitLines(data)
	for _, line := range lines {
		if len(line) == 0 {
			continue
		}
		var entry WALEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			continue // Skip malformed entries (crash during write)
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

// LastCompletedStage returns the name and index of the last completed stage.
// Returns ("", -1) if no stage has completed (crash before first stage).
func (r *WALReader) LastCompletedStage() (string, int, error) {
	entries, err := r.ReadEntries()
	if err != nil {
		return "", -1, err
	}

	var lastStage string
	var lastIndex int = -1

	for _, entry := range entries {
		if entry.Type == "wal.stage.completed" {
			stage, ok := entry.Payload["stage"].(string)
			if !ok {
				continue
			}
			index, ok := entry.Payload["stage_index"].(float64)
			if !ok {
				continue
			}
			success, _ := entry.Payload["success"].(bool)
			if success {
				lastStage = stage
				lastIndex = int(index)
			}
		}
	}

	return lastStage, lastIndex, nil
}

// MutationKeys returns all mutation keys that have been applied.
// Used for idempotency checking during recovery.
func (r *WALReader) MutationKeys() (map[string]bool, error) {
	entries, err := r.ReadEntries()
	if err != nil {
		return nil, err
	}

	keys := make(map[string]bool)
	for _, entry := range entries {
		if entry.Type == "wal.mutation.applied" {
			key, ok := entry.Payload["mutation_key"].(string)
			if ok {
				keys[key] = true
			}
		}
	}
	return keys, nil
}

// splitLines splits byte data into lines, handling both \n and \r\n.
func splitLines(data []byte) [][]byte {
	var lines [][]byte
	start := 0
	for i, b := range data {
		if b == '\n' {
			line := data[start:i]
			if len(line) > 0 && line[len(line)-1] == '\r' {
				line = line[:len(line)-1]
			}
			if len(line) > 0 {
				lines = append(lines, line)
			}
			start = i + 1
		}
	}
	// Handle last line without newline
	if start < len(data) {
		line := data[start:]
		if len(line) > 0 {
			lines = append(lines, line)
		}
	}
	return lines
}

// ComputeMutationKey generates a deterministic idempotency key for a mutation.
// The key is SHA256(runID + stage + targetPath + contentHash).
// This means: same run + same stage + same target + same content = same key.
// Different content = different key (safe to re-apply).
// Same content after crash = same key (idempotent, will be skipped).
func ComputeMutationKey(runID, stage, targetPath string, content []byte) string {
	h := sha256.New()
	h.Write([]byte(runID))
	h.Write([]byte{0}) // separator
	h.Write([]byte(stage))
	h.Write([]byte{0})
	h.Write([]byte(targetPath))
	h.Write([]byte{0})
	h.Write(content)
	return fmt.Sprintf("sha256:%x", h.Sum(nil))
}

// ComputeMutationKeyWithTimestamp includes a timestamp nonce for cases where
// the same mutation should be allowed to run again (e.g., re-running a task
// that intentionally modifies the same file).
func ComputeMutationKeyWithTimestamp(runID, stage, targetPath string, content []byte, ts time.Time) string {
	h := sha256.New()
	h.Write([]byte(runID))
	h.Write([]byte{0})
	h.Write([]byte(stage))
	h.Write([]byte{0})
	h.Write([]byte(targetPath))
	h.Write([]byte{0})
	h.Write(content)
	h.Write([]byte{0})
	h.Write([]byte(ts.UTC().Format(time.RFC3339Nano)))
	return fmt.Sprintf("sha256:%x", h.Sum(nil))
}

// IsMutationApplied checks if a mutation key has already been applied
// by checking against the WAL entries.
func IsMutationApplied(walEntries []WALEntry, key string) bool {
	for _, entry := range walEntries {
		if entry.Type == "wal.mutation.applied" {
			entryKey, ok := entry.Payload["mutation_key"].(string)
			if ok && entryKey == key {
				return true
			}
		}
	}
	return false
}
