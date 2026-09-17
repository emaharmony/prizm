// Package projection provides state projections over Prizm event streams.
//
// A projection is a pure function of events: given a sequence of events,
// it computes a read-only snapshot of some aspect of system state.
// Projections are the "read side" of Prizm's event-sourced architecture.
//
// Why projections? Instead of querying a database, you project events
// into pre-computed indexes. Each projection subscribes to specific event
// types and accumulates state from them. Given the same events, you always
// get the same projection. No side effects, no external state.
//
// This is the CQRS/Event Sourcing pattern adapted for local-file-based
// systems. Prizm stores events (events.jsonl); projections are derived
// views of those events. You can always rebuild a projection from scratch
// by replaying the event stream.
//
// Usage:
//
//	runner := projection.NewRunner(
//	    runstatus.New(),
//	    approval.New(),
//	    toolhistory.New(),
//	)
//	err := runner.Run("runs/run_xxx/events.jsonl", "runs/run_xxx")
package projection

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/emaharmony/prizm/internal/event"
)

// Projection computes a read-only snapshot from a sequence of events.
//
// Implementations must be pure: given the same events in the same order,
// they must produce the same output. No side effects, no external state,
// no randomness.
//
// To create a projection, implement this interface and register it with
// a Runner. The Runner will filter events by Subscribe() and pass matching
// events to Apply() in order. After all events are processed, Snapshot()
// is called to get the serializable state.
type Projection interface {
	// Name returns the projection's unique identifier.
	// This is used as the filename: projections/<name>.json
	// Must be lowercase alphanumeric with hyphens (no dots, no underscores).
	Name() string

	// Subscribe returns the event types this projection cares about.
	// Only events matching these types will be passed to Apply.
	// Use "*" to subscribe to all event types.
	Subscribe() []string

	// Apply processes a single event and updates the projection state.
	// Apply must be idempotent: applying the same event twice should
	// produce the same state as applying it once.
	Apply(evt event.Event) error

	// Snapshot returns the current projection state as a serializable map.
	// This is what gets written to disk.
	Snapshot() map[string]any
}

// Runner applies a set of projections to an event stream.
// It reads events from an events.jsonl file, filters them by each
// projection's Subscribe() list, applies matching events, and writes
// the resulting snapshots to a projections/ directory.
type Runner struct {
	projections []Projection
}

// NewRunner creates a runner with the given projections.
func NewRunner(projections ...Projection) *Runner {
	return &Runner{projections: projections}
}

// Run reads events from an events.jsonl file and applies all projections.
// After processing, snapshots are written to <runDir>/projections/.
func (r *Runner) Run(eventsFile, runDir string) error {
	events, err := ReadEventsFromJSONL(eventsFile)
	if err != nil {
		return fmt.Errorf("projection: read events: %w", err)
	}

	return r.RunFromEvents(events, runDir)
}

// RunFromEvents applies projections to an already-loaded event slice.
// This is useful for testing and programmatic use where events are
// already in memory.
func (r *Runner) RunFromEvents(events []event.Event, runDir string) error {
	// Build a subscription index for fast filtering
	subIndex := make(map[string][]int) // event type → projection indices
	allSubs := []int{}                 // indices of projections that subscribe to "*"

	for i, p := range r.projections {
		subs := p.Subscribe()
		for _, s := range subs {
			if s == "*" {
				allSubs = append(allSubs, i)
			} else {
				subIndex[s] = append(subIndex[s], i)
			}
		}
	}

	// Apply each event to matching projections
	for _, evt := range events {
		// Projections subscribed to this event type
		indices := subIndex[evt.Type]
		// Plus projections subscribed to all events
		seen := make(map[int]bool)
		for _, idx := range indices {
			seen[idx] = true
			if err := r.projections[idx].Apply(evt); err != nil {
				return fmt.Errorf("projection %s: apply event %s: %w", r.projections[idx].Name(), evt.ID, err)
			}
		}
		for _, idx := range allSubs {
			if !seen[idx] {
				if err := r.projections[idx].Apply(evt); err != nil {
					return fmt.Errorf("projection %s: apply event %s: %w", r.projections[idx].Name(), evt.ID, err)
				}
			}
		}
	}

	// Write snapshots to disk
	projDir := filepath.Join(runDir, "projections")
	if err := os.MkdirAll(projDir, 0755); err != nil {
		return fmt.Errorf("projection: create projections dir: %w", err)
	}

	for _, p := range r.projections {
		snapshot := p.Snapshot()
		data, err := json.MarshalIndent(snapshot, "", "  ")
		if err != nil {
			return fmt.Errorf("projection %s: marshal snapshot: %w", p.Name(), err)
		}

		path := filepath.Join(projDir, p.Name()+".json")
		if err := os.WriteFile(path, data, 0644); err != nil {
			return fmt.Errorf("projection %s: write snapshot: %w", p.Name(), err)
		}
	}

	return nil
}

// List returns the names of all registered projections.
func (r *Runner) List() []string {
	names := make([]string, len(r.projections))
	for i, p := range r.projections {
		names[i] = p.Name()
	}
	return names
}

// readEventsFromJSONL reads events from an events.jsonl file.
// Each line is a JSON event. Lines that fail to parse are skipped
// with a warning (not an error), so that partial/corrupt logs
// don't prevent projection building.
func ReadEventsFromJSONL(path string) ([]event.Event, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read events file: %w", err)
	}

	if len(data) == 0 {
		return nil, nil
	}

	var events []event.Event
	lineNum := 0
	for _, line := range splitLines(data) {
		lineNum++
		line = trimSpace(line)
		if len(line) == 0 {
			continue
		}

		var evt event.Event
		if err := json.Unmarshal(line, &evt); err != nil {
			// Skip malformed lines instead of failing
			continue
		}
		events = append(events, evt)
	}

	return events, nil
}

// splitLines splits byte data into lines.
func splitLines(data []byte) [][]byte {
	var lines [][]byte
	start := 0
	for i, b := range data {
		if b == '\n' {
			lines = append(lines, data[start:i])
			start = i + 1
		}
	}
	if start < len(data) {
		lines = append(lines, data[start:])
	}
	return lines
}

// trimSpace trims whitespace from a byte slice.
func trimSpace(b []byte) []byte {
	start := 0
	for start < len(b) && (b[start] == ' ' || b[start] == '\t' || b[start] == '\r') {
		start++
	}
	end := len(b)
	for end > start && (b[end-1] == ' ' || b[end-1] == '\t' || b[end-1] == '\r') {
		end--
	}
	return b[start:end]
}
