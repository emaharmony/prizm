package multiagent

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/emaharmony/prizm/internal/event"
)

// FreewillNotifyWorthyEventTypes are the curated lifecycle events worth
// surfacing to humans. Anything not listed is internal chatter (wake claims,
// selection attempts) and never enqueued.
var FreewillNotifyWorthyEventTypes = []string{
	event.EventFreewillApprovalProposed,
	event.EventFreewillApprovalDecided,
	event.EventFreewillApprovalReconciled,
	event.EventFreewillPolicyDenied,
	event.EventFreewillSelectionHalted,
}

func freewillNotifyWorthy(t string) bool {
	for _, w := range FreewillNotifyWorthyEventTypes {
		if t == w {
			return true
		}
	}
	return false
}

// FreewillNotification is one delivered notification command. Payloads carry
// the enriched fields written at the emit point (freewillEventContext), so
// consumers render cards without reading the durable run store.
type FreewillNotification struct {
	EventID   string         `json:"event_id"`
	EventType string         `json:"event_type"`
	RunID     string         `json:"run_id"`
	Timestamp string         `json:"timestamp"`
	Payload   map[string]any `json:"payload"`
}

// FreewillNotificationSink consumes one notification. Implementations must be
// idempotent per EventID: the dispatcher retries with backoff on error.
type FreewillNotificationSink interface {
	NotifyFreewill(ctx context.Context, n FreewillNotification) error
}

// freewillNotificationPublisher adapts a Sink to event.CommandPublisher for
// the shared Dispatcher. The subject is informational; the sink gets decoded
// notification structs, not raw NATS topics.
type freewillNotificationPublisher struct {
	sink FreewillNotificationSink
}

func (p freewillNotificationPublisher) Publish(ctx context.Context, subject string, data []byte) error {
	// Cursor bookkeeping rows share the outbox; they never reach the sink.
	var probe struct {
		EventType string `json:"event_type"`
	}
	if err := json.Unmarshal(data, &probe); err == nil && probe.EventType == "" {
		return nil
	}
	var n FreewillNotification
	if err := json.Unmarshal(data, &n); err != nil {
		return fmt.Errorf("freewill notification: decode command: %w", err)
	}
	if n.EventID == "" || n.EventType == "" {
		return nil
	}
	return p.sink.NotifyFreewill(ctx, n)
}

// FreewillNotificationService persists notification commands in a dedicated
// durable outbox and drains them with the shared Dispatcher. Delivery is
// crash-safe: an enqueued command survives restarts, and a crash between
// notify and mark-delivered replays the same EventID (idempotent consumers).
// A reconciler scans the canonical freewill event store with a persisted
// cursor so events emitted while the process was down are never lost.
type FreewillNotificationService struct {
	outbox     *event.SQLiteOutbox
	dispatcher event.Dispatcher
	events     *event.SQLiteEventStore
	storePath  string
	sink       FreewillNotificationSink

	scanEvery time.Duration
	stop      chan struct{}
	stopped   chan struct{}

	mu       sync.Mutex
	once     sync.Once
	onceStop sync.Once
}

// NewFreewillNotificationService opens the durable notification outbox next
// to the canonical freewill.db named <dbPath>-notifications.db.
func NewFreewillNotificationService(dbPath string, events *event.SQLiteEventStore, sink FreewillNotificationSink) (*FreewillNotificationService, error) {
	if sink == nil {
		return nil, fmt.Errorf("freewill notification: sink is required")
	}
	if dbPath == "" {
		return nil, fmt.Errorf("freewill notification: durable db path is required")
	}
	outbox, err := event.NewSQLiteOutbox(dbPath + "-notifications.db")
	if err != nil {
		return nil, fmt.Errorf("freewill notification: open outbox: %w", err)
	}
	return &FreewillNotificationService{
		outbox: outbox,
		dispatcher: event.Dispatcher{
			Outbox:      outbox,
			Publisher:   freewillNotificationPublisher{sink: sink},
			Lease:       30 * time.Second,
			MaxAttempts: 8,
			RetryAfter:  10 * time.Second,
		},
		events: events, storePath: dbPath, sink: sink,
		scanEvery: 15 * time.Second,
		stop:      make(chan struct{}), stopped: make(chan struct{}),
	}, nil
}

// Enqueue stores one notification command keyed by canonical event ID so
// replays dedupe naturally.
func (s *FreewillNotificationService) Enqueue(ctx context.Context, evt event.Event) error {
	n := FreewillNotification{EventID: evt.ID, EventType: evt.Type, RunID: evt.Metadata.RunID, Timestamp: evt.Timestamp, Payload: evt.Payload}
	if n.RunID == "" {
		n.RunID, _ = evt.Payload["run_id"].(string)
	}
	encoded, err := json.Marshal(n)
	if err != nil {
		return err
	}
	if strings.TrimSpace(n.RunID) == "" {
		return fmt.Errorf("freewill notification: event %s (%s) has no run_id", evt.ID, evt.Type)
	}
	if strings.TrimSpace(evt.CorrelationID) == "" {
		evt.CorrelationID = evt.ID
	}
	cmd := event.Command{
		SchemaVersion:  event.CommandSchemaVersion,
		EventID:        evt.ID,
		Type:           "freewill.notify." + evt.Type,
		RunID:          n.RunID,
		CorrelationID:  evt.CorrelationID,
		IdempotencyKey: "freewill-notify:" + evt.ID,
		Payload:        encoded,
	}
	_, err = s.outbox.Accept(ctx, "freewill.notification", cmd)
	return err
}

// Start launches the dispatcher drain loop and the canonical-event
// reconciler. Safe to call once; extra calls are no-ops.
func (s *FreewillNotificationService) Start(ctx context.Context) {
	s.once.Do(func() {
		go s.dispatchLoop(ctx)
		go s.reconcileLoop(ctx)
	})
}

func (s *FreewillNotificationService) dispatchLoop(ctx context.Context) {
	defer close(s.stopped)
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.stop:
			return
		case <-t.C:
			// DispatchOne claims exactly one command; loop until quiet.
			for {
				more, err := s.dispatcher.DispatchOne(ctx)
				if err != nil {
					log.Printf("[FREEWILL-NOTIFY] WARN dispatch: %v", err)
					break
				}
				if !more {
					break
				}
			}
		}
	}
}

// reconcileLoop polls the canonical event store for notify-worthy events the
// outbox has not enqueued (cursor-safe, crash-tolerant).
func (s *FreewillNotificationService) reconcileLoop(ctx context.Context) {
	t := time.NewTicker(s.scanEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.stop:
			return
		case <-t.C:
			if err := s.Reconcile(ctx); err != nil {
				log.Printf("[FREEWILL-NOTIFY] WARN reconcile: %v", err)
			}
		}
	}
}

// freewillNotifyCursorKey names the durable reconciler cursor, stored as a
// regular outbox command row so delivery of the cursor itself survives
// restarts with the same SQLite guarantees as notifications.
const (
	freewillNotifyCursorKey   = "freewill-notify-cursor"
	freewillNotifyCursorRunID = "freewill-notification-cursor"
)

// Reconcile scans canonical freewill events after the last-processed row and
// enqueues any notify-worthy ones. The cursor (last seen rowid + event id) is
// a durable outbox row, so a crash mid-scan replays at most one page and
// dedupe happens at delivery (outbox idempotency by event id).
func (s *FreewillNotificationService) Reconcile(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	afterRowID, _, err := s.loadCursor(ctx)
	if err != nil {
		return err
	}
	const pageSize = 200
	for {
		evts, nextRow, err := s.events.QueryInInsertionOrderPage(ctx, event.EventFilter{Type: "prism.freewill.", Limit: pageSize}, afterRowID)
		if err != nil {
			return fmt.Errorf("freewill notification: scan events: %w", err)
		}
		if len(evts) == 0 {
			return nil
		}
		last := evts[len(evts)-1]
		for _, evt := range evts {
			if !freewillNotifyWorthy(evt.Type) {
				continue
			}
			if err := s.Enqueue(ctx, evt); err != nil {
				return fmt.Errorf("freewill notification: enqueue %s: %w", evt.ID, err)
			}
		}
		if err := s.saveCursor(ctx, nextRow, last.ID); err != nil {
			return fmt.Errorf("freewill notification: save cursor: %w", err)
		}
		afterRowID = nextRow
		if len(evts) < pageSize {
			return nil
		}
	}
}

// loadCursor reads {row_id,last_event_id} from the cursor command row.
func (s *FreewillNotificationService) loadCursor(ctx context.Context) (int64, string, error) {
	d, err := s.outbox.Get(ctx, freewillNotifyCursorKey)
	if err != nil || d == nil {
		if err != nil && !strings.Contains(err.Error(), "no rows") {
			return 0, "", err
		}
		return 0, "", nil
	}
	var cur struct {
		RowID       int64  `json:"row_id"`
		LastEventID string `json:"last_event_id"`
	}
	if err := json.Unmarshal(d.Command.Payload, &cur); err != nil {
		return 0, "", nil
	}
	return cur.RowID, cur.LastEventID, nil
}

// saveCursor upserts the cursor command row (Accept dedupes by key; a second
// accept with the same EventID is rejected, so the cursor event id rotates).
func (s *FreewillNotificationService) saveCursor(ctx context.Context, rowID int64, lastEventID string) error {
	encoded, err := json.Marshal(map[string]any{"row_id": rowID, "last_event_id": lastEventID})
	if err != nil {
		return err
	}
	_, err = s.outbox.Accept(ctx, "freewill.notification.cursor", event.Command{
		SchemaVersion:  event.CommandSchemaVersion,
		EventID:        event.NewID(),
		Type:           "freewill.notify.cursor",
		RunID:          freewillNotifyCursorRunID,
		CorrelationID:  "freewill-notify-cursor",
		IdempotencyKey: freewillNotifyCursorKey,
		Payload:        encoded,
	})
	return err
}

func (s *FreewillNotificationService) Close() error {
	s.onceStop.Do(func() { close(s.stop) })
	select {
	case <-s.stopped:
	case <-time.After(3 * time.Second):
	}
	return s.outbox.Close()
}
