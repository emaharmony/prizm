// Package factorymonitor watches a local Roblox Factory queue and emits status events.
package factorymonitor

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	EventStatusChanged = "prizm.factory.status.changed"
	EventStatusDigest  = "prizm.factory.status.digest"
	EventStatusStuck   = "prizm.factory.status.stuck"
)

var terminalStatuses = map[string]bool{
	"completed": true,
	"failed":    true,
	"cancelled": true,
}

// Publisher publishes monitor events to the Prizm bus.
type Publisher interface {
	Publish(subject string, data []byte) error
}

// Config controls Factory status monitoring.
type Config struct {
	Root       string
	PollEvery  time.Duration
	StuckAfter time.Duration
}

// Monitor polls Factory status files and publishes normalized events.
type Monitor struct {
	cfg       Config
	publisher Publisher

	mu            sync.Mutex
	seen          map[string]statusKey
	stuckNotified map[string]statusKey
}

type statusKey struct {
	Status    string
	UpdatedAt string
	Message   string
}

// TaskStatus is the normalized Factory task status payload.
type TaskStatus struct {
	TaskID     string         `json:"task_id"`
	Status     string         `json:"status"`
	Worker     string         `json:"worker,omitempty"`
	ActiveTask string         `json:"active_task,omitempty"`
	Message    string         `json:"message,omitempty"`
	UpdatedAt  string         `json:"updated_at,omitempty"`
	Details    map[string]any `json:"details,omitempty"`
	ResultPath string         `json:"result_path,omitempty"`
	ErrorPath  string         `json:"error_path,omitempty"`
	AgeSeconds int64          `json:"age_seconds,omitempty"`
}

// Snapshot summarizes the current Factory queue.
type Snapshot struct {
	Root       string       `json:"root"`
	CapturedAt string       `json:"captured_at"`
	Counts     QueueCounts  `json:"counts"`
	Tasks      []TaskStatus `json:"tasks"`
}

// QueueCounts tracks Factory queue directory state.
type QueueCounts struct {
	Inbox      int `json:"inbox"`
	Processing int `json:"processing"`
	Failed     int `json:"failed"`
	Archive    int `json:"archive"`
	Active     int `json:"active"`
	Completed  int `json:"completed"`
}

// New creates a Factory monitor with safe defaults.
func New(cfg Config, publisher Publisher) *Monitor {
	if cfg.PollEvery <= 0 {
		cfg.PollEvery = 30 * time.Second
	}
	if cfg.StuckAfter <= 0 {
		cfg.StuckAfter = 30 * time.Minute
	}
	return &Monitor{
		cfg:           cfg,
		publisher:     publisher,
		seen:          make(map[string]statusKey),
		stuckNotified: make(map[string]statusKey),
	}
}

// Start runs the monitor until ctx is cancelled.
func (m *Monitor) Start(ctx context.Context) {
	if m == nil {
		return
	}
	if snap, err := m.Snapshot(); err == nil {
		m.baseline(snap)
	}

	pollTicker := time.NewTicker(m.cfg.PollEvery)
	defer pollTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-pollTicker.C:
			m.poll()
		}
	}
}

// Snapshot reads the current Factory queue state.
func (m *Monitor) Snapshot() (Snapshot, error) {
	root, err := filepath.Abs(m.cfg.Root)
	if err != nil {
		return Snapshot{}, err
	}

	snap := Snapshot{
		Root:       root,
		CapturedAt: time.Now().UTC().Format(time.RFC3339),
		Counts: QueueCounts{
			Inbox:      countJSON(filepath.Join(root, "inbox")),
			Processing: countJSON(filepath.Join(root, "processing")),
			Failed:     countJSON(filepath.Join(root, "failed")),
			Archive:    countJSON(filepath.Join(root, "archive")),
		},
	}

	statusPaths, err := filepath.Glob(filepath.Join(root, "outbox", "*_status.json"))
	if err != nil {
		return Snapshot{}, err
	}
	now := time.Now()
	for _, path := range statusPaths {
		st, err := readStatus(root, path, now)
		if err != nil {
			continue
		}
		snap.Tasks = append(snap.Tasks, st)
		if terminalStatuses[st.Status] {
			if st.Status == "completed" {
				snap.Counts.Completed++
			}
		} else {
			snap.Counts.Active++
		}
	}
	sort.Slice(snap.Tasks, func(i, j int) bool {
		return snap.Tasks[i].TaskID < snap.Tasks[j].TaskID
	})
	return snap, nil
}

func (m *Monitor) baseline(snap Snapshot) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, st := range snap.Tasks {
		m.seen[st.TaskID] = makeStatusKey(st)
	}
}

func (m *Monitor) poll() {
	snap, err := m.Snapshot()
	if err != nil {
		return
	}

	var changed []TaskStatus
	var stuck []TaskStatus
	m.mu.Lock()
	for _, st := range snap.Tasks {
		key := makeStatusKey(st)
		if old, ok := m.seen[st.TaskID]; !ok || old != key {
			changed = append(changed, st)
			m.seen[st.TaskID] = key
			delete(m.stuckNotified, st.TaskID)
		}
		if !terminalStatuses[st.Status] && m.isStuck(st) {
			if old, ok := m.stuckNotified[st.TaskID]; !ok || old != key {
				stuck = append(stuck, st)
				m.stuckNotified[st.TaskID] = key
			}
		}
	}
	m.mu.Unlock()

	for _, st := range changed {
		_ = m.publish(EventStatusChanged, st)
	}
	for _, st := range stuck {
		_ = m.publish(EventStatusStuck, st)
	}
}

// PublishDigest publishes the current Factory summary and returns the formatted message.
func (m *Monitor) PublishDigest() string {
	snap, err := m.Snapshot()
	if err != nil {
		return fmt.Sprintf("Factory status unavailable: %v", err)
	}
	m.publishSnapshot(EventStatusDigest, snap)
	return FormatDigestMessage(snap)
}

func (m *Monitor) isStuck(st TaskStatus) bool {
	if m.cfg.StuckAfter <= 0 || st.UpdatedAt == "" {
		return false
	}
	return time.Duration(st.AgeSeconds)*time.Second >= m.cfg.StuckAfter
}

func (m *Monitor) publish(subject string, payload any) error {
	if m.publisher == nil {
		return nil
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return m.publisher.Publish(subject, data)
}

func (m *Monitor) publishSnapshot(subject string, snap Snapshot) {
	_ = m.publish(subject, snap)
}

func makeStatusKey(st TaskStatus) statusKey {
	return statusKey{Status: st.Status, UpdatedAt: st.UpdatedAt, Message: st.Message}
}

func readStatus(root, path string, now time.Time) (TaskStatus, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return TaskStatus{}, err
	}
	var st TaskStatus
	if err := json.Unmarshal(data, &st); err != nil {
		return TaskStatus{}, err
	}
	if st.TaskID == "" {
		base := filepath.Base(path)
		st.TaskID = strings.TrimSuffix(base, "_status.json")
	}
	if st.Status == "" {
		st.Status = "unknown"
	}
	if st.Details == nil {
		st.Details = map[string]any{}
	}
	if st.ResultPath == "" {
		if value, ok := st.Details["result_path"].(string); ok {
			st.ResultPath = value
		}
	}
	if st.ErrorPath == "" {
		if value, ok := st.Details["error_path"].(string); ok {
			st.ErrorPath = value
		}
	}
	resultPath := filepath.Join(root, "outbox", st.TaskID+"_result.md")
	if st.ResultPath == "" && fileExists(resultPath) {
		st.ResultPath = resultPath
	}
	errorPath := filepath.Join(root, "outbox", st.TaskID+"_error.md")
	if st.ErrorPath == "" && fileExists(errorPath) {
		st.ErrorPath = errorPath
	}
	if updated, err := time.Parse(time.RFC3339, st.UpdatedAt); err == nil {
		st.AgeSeconds = int64(now.Sub(updated).Seconds())
		if st.AgeSeconds < 0 {
			st.AgeSeconds = 0
		}
	}
	return st, nil
}

func countJSON(dir string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	count := 0
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasSuffix(name, ".json") && !strings.HasSuffix(name, ".lock") {
			count++
		}
	}
	return count
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
