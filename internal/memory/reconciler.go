package memory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"github.com/emaharmony/prizm/internal/event"
)

// Reconciler drains durable local capture facts after the primary memory
// backend recovers. Events are its recovery record; it keeps no second state
// database. Primary delivery is deliberately restricted to an idempotent
// backend so a crash after a remote success is safe to replay.
type Reconciler struct {
	Local       MemoryStore
	Primary     PrimaryBackend
	Events      event.EventStore
	Source      string
	MaxAttempts int
	RetryBase   time.Duration

	notify chan struct{}
	mu     sync.RWMutex
	status ReconcilerStatus
}

type ReconcilerStatus struct {
	LastRun time.Time
	Pending int
	LastErr string
}

func NewReconciler(local MemoryStore, primary PrimaryBackend, events event.EventStore, source string) *Reconciler {
	return &Reconciler{Local: local, Primary: primary, Events: events, Source: source, MaxAttempts: 3, RetryBase: time.Second, notify: make(chan struct{}, 1)}
}

// Notify wakes the consumer after a new pending fact is persisted. The
// periodic wake-up is only retry recovery; delivery is event-notified first.
func (r *Reconciler) Notify() {
	if r == nil {
		return
	}
	select {
	case r.notify <- struct{}{}:
	default:
	}
}

func (r *Reconciler) Status() ReconcilerStatus {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.status
}

func (r *Reconciler) Run(ctx context.Context) {
	r.RunOnce(ctx)
	for {
		wait := r.retryBase()
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-r.notify:
			timer.Stop()
		case <-timer.C:
		}
		r.RunOnce(ctx)
	}
}

// RunOnce is intentionally exported for startup recovery and deterministic
// tests. It scans durable pending facts and safely replays only unresolved
// deliveries.
func (r *Reconciler) RunOnce(ctx context.Context) {
	if r == nil || r.Local == nil || r.Events == nil {
		return
	}
	all, err := reconciliationEvents(ctx, r.Events)
	if err != nil {
		r.record(0, err)
		return
	}
	pending, _, retries := reconcileFacts(all)
	var runErr error
	for _, fact := range pending {
		if !r.readyForRetry(fact, retries[fact.syncKey]) {
			continue
		}
		if err := r.reconcileOne(ctx, fact, retries[fact.syncKey]); err != nil && runErr == nil {
			runErr = err
		}
	}
	// Refresh after delivery because this pass may have appended terminal facts.
	// Pending is the number of unresolved durable intents, not the raw number
	// of historical pending events.
	remaining := len(pending)
	refreshed, refreshErr := reconciliationEvents(ctx, r.Events)
	if refreshErr != nil {
		if runErr == nil {
			runErr = refreshErr
		}
	} else {
		unresolved, _, _ := reconcileFacts(refreshed)
		remaining = len(unresolved)
	}
	r.record(remaining, runErr)
}

type pendingCapture struct {
	memoryID string
	syncKey  string
	scope    Scope
	event    event.Event
}

// causalEventStore is optional because EventStore remains the compatibility
// seam. SQLite exposes this narrower ordering for replay consumers that need
// insertion order rather than its public ID-cursor order.
type causalEventStore interface {
	QueryInInsertionOrder(context.Context, event.EventFilter) ([]event.Event, error)
}

func reconciliationEvents(ctx context.Context, store event.EventStore) ([]event.Event, error) {
	filter := event.EventFilter{Type: "prizm.memory.capture.", Limit: 10000}
	if causal, ok := store.(causalEventStore); ok {
		return causal.QueryInInsertionOrder(ctx, filter)
	}
	// ID-sorted retrieval is not safe for replay: deterministic terminal IDs
	// can sort before or after the pending fact they close. Refuse delivery
	// until a store proves causal ordering rather than risking a stale replay.
	return nil, fmt.Errorf("memory reconciliation requires insertion-ordered event queries")
}

func reconcileFacts(events []event.Event) ([]pendingCapture, map[string]bool, map[string][]event.Event) {
	pendingByKey := map[string]pendingCapture{}
	terminal := map[string]bool{}
	retries := map[string][]event.Event{}
	for _, e := range events {
		memoryID, _ := e.Payload["memory_id"].(string)
		syncKey, _ := e.Payload["sync_key"].(string)
		if syncKey == "" {
			syncKey = memoryID
		}
		if memoryID == "" || syncKey == "" {
			continue
		}
		switch e.Type {
		case EventScopedCaptureSyncPending:
			// A later pending fact begins a new delivery attempt for the same
			// durable memory. It must not be hidden by an older terminal fact.
			delete(terminal, syncKey)
			// A new pending fact is a new delivery generation. Retry facts from
			// the finished generation must not delay or exhaust this one.
			delete(retries, syncKey)
			pendingByKey[syncKey] = pendingCapture{memoryID: memoryID, syncKey: syncKey, scope: scopeFromEvent(e), event: e}
		case EventScopedCaptureSynced, EventScopedCaptureSyncFailed:
			delete(pendingByKey, syncKey)
			terminal[syncKey] = true
		case EventScopedCaptureSyncRetry:
			retries[syncKey] = append(retries[syncKey], e)
		}
	}
	pending := make([]pendingCapture, 0, len(pendingByKey))
	for _, item := range pendingByKey {
		pending = append(pending, item)
	}
	return pending, terminal, retries
}

func scopeFromEvent(e event.Event) Scope {
	userID, _ := e.Payload["user_id"].(string)
	projectID, _ := e.Payload["project_id"].(string)
	taskID, _ := e.Payload["task_id"].(string)
	return Scope{UserID: userID, IncludeUserScope: userID != "", ProjectID: projectID, TaskID: taskID, SessionID: e.Metadata.SessionID, AgentID: e.Metadata.Agent, CorrelationID: e.CorrelationID}
}

func (r *Reconciler) reconcileOne(ctx context.Context, fact pendingCapture, retries []event.Event) error {
	mem, err := r.Local.Get(ctx, fact.memoryID)
	if err != nil || mem == nil {
		return r.terminal(ctx, fact, "local_memory_missing")
	}
	if err := fact.scope.Validate(); err != nil || !scopeMatches(*mem, fact.scope) {
		return r.terminal(ctx, fact, "scope_mismatch")
	}
	primary, ok := r.Primary.(IdempotentPrimaryBackend)
	if !ok {
		// Configuration and availability can change after startup. Retain the
		// durable intent until an eligible backend is present rather than
		// dead-lettering an otherwise valid local memory record.
		return fmt.Errorf("memory primary is not configured for idempotent delivery")
	}
	primaryID, err := primary.CaptureIdempotent(ctx, *mem, fact.syncKey)
	if err != nil {
		attempt := len(retries) + 1
		if attempt >= r.maxAttempts() {
			return r.terminal(ctx, fact, "retry_exhausted")
		}
		if retryErr := r.retry(ctx, fact, attempt, err); retryErr != nil {
			return retryErr
		}
		return err
	}
	return r.synced(ctx, fact, primaryID)
}

func (r *Reconciler) readyForRetry(fact pendingCapture, retries []event.Event) bool {
	if len(retries) == 0 {
		return true
	}
	last := retries[len(retries)-1]
	when, err := time.Parse(time.RFC3339Nano, last.Timestamp)
	if err != nil {
		return true
	}
	delay := r.retryBase()
	for i := 1; i < len(retries); i++ {
		delay *= 2
	}
	return !time.Now().UTC().Before(when.Add(delay))
}

func (r *Reconciler) retry(ctx context.Context, fact pendingCapture, attempt int, err error) error {
	return r.store(ctx, EventScopedCaptureSyncRetry, fact, map[string]any{"attempt": attempt, "reason": "primary_unavailable", "error": err.Error()}, false)
}

func (r *Reconciler) terminal(ctx context.Context, fact pendingCapture, reason string) error {
	return r.store(ctx, EventScopedCaptureSyncFailed, fact, map[string]any{"reason": reason}, true)
}

func (r *Reconciler) synced(ctx context.Context, fact pendingCapture, primaryID string) error {
	return r.store(ctx, EventScopedCaptureSynced, fact, map[string]any{"primary_id": primaryID}, true)
}

func (r *Reconciler) store(ctx context.Context, typ string, fact pendingCapture, extra map[string]any, terminal bool) error {
	payload := map[string]any{"memory_id": fact.memoryID, "sync_key": fact.syncKey, "project_id": fact.scope.ProjectID, "task_id": fact.scope.TaskID}
	if fact.scope.IncludeUserScope {
		payload["user_id"] = fact.scope.UserID
	}
	for key, value := range extra {
		payload[key] = value
	}
	e := event.NewEvent(typ, r.Source, payload).WithCorrelationID(fact.scope.CorrelationID).WithParentID(fact.event.ID).WithMetadata(event.EventMetadata{Project: fact.scope.ProjectID, SessionID: fact.scope.SessionID, Agent: fact.scope.AgentID})
	if terminal {
		// Terminal evidence is deduplicated per durable pending generation.
		// A later pending fact starts a new generation and needs its own
		// terminal evidence without allowing duplicate delivery for that fact.
		e.ID = stableSyncEventID(typ, fact.syncKey, fact.event.ID)
	}
	if err := r.Events.Store(ctx, e); err != nil {
		return err
	}
	return nil
}

func stableSyncEventID(typ, syncKey, pendingEventID string) string {
	sum := sha256.Sum256([]byte(typ + "\x00" + syncKey + "\x00" + pendingEventID))
	return "evt_memory_" + hex.EncodeToString(sum[:16])
}

func (r *Reconciler) retryBase() time.Duration {
	if r.RetryBase <= 0 {
		return time.Second
	}
	return r.RetryBase
}

func (r *Reconciler) maxAttempts() int {
	if r.MaxAttempts <= 0 {
		return 3
	}
	return r.MaxAttempts
}

func (r *Reconciler) record(pending int, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.status.LastRun = time.Now().UTC()
	r.status.Pending = pending
	if err != nil {
		r.status.LastErr = err.Error()
	} else {
		r.status.LastErr = ""
	}
}
