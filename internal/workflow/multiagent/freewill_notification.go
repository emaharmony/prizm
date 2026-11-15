package multiagent

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/emaharmony/prizm/internal/event"
	prismsqlite "github.com/emaharmony/prizm/internal/sqlite"
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
	event.EventFreewillExecutionCompleted,
	event.EventFreewillExecutionFailed,
	event.EventFreewillExecutionCancelled,
	event.EventFreewillExecutionRecoveryUncertain,
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
	cursorDB   *sql.DB
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
	cursorDB, err := sql.Open(prismsqlite.DriverName, dbPath+"-notifications.db?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_txlock=immediate")
	if err != nil {
		_ = outbox.Close()
		return nil, fmt.Errorf("freewill notification: open cursor store: %w", err)
	}
	if _, err := cursorDB.Exec(`CREATE TABLE IF NOT EXISTS freewill_notification_cursor (
		id INTEGER PRIMARY KEY CHECK (id=1), row_id INTEGER NOT NULL, last_event_id TEXT NOT NULL)`); err != nil {
		_ = cursorDB.Close()
		_ = outbox.Close()
		return nil, fmt.Errorf("freewill notification: initialize cursor store: %w", err)
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
		events: events, storePath: dbPath, sink: sink, cursorDB: cursorDB,
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

// freewillNotifyCursorKey names the legacy cursor command row, retained only
// for one-time migration into the dedicated cursor table.
const (
	freewillNotifyCursorKey = "freewill-notify-cursor"
)

// Reconcile scans canonical freewill events after the last-processed row and
// enqueues any notify-worthy ones. The cursor (last seen rowid + event id) is
// stored separately from immutable notification commands, so a crash mid-scan
// replays at most one page and dedupe happens by canonical event id.
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

// loadCursor reads {row_id,last_event_id} from the dedicated cursor row. It
// migrates the original outbox-command cursor on first use after upgrade.
func (s *FreewillNotificationService) loadCursor(ctx context.Context) (int64, string, error) {
	var cur struct {
		RowID       int64  `json:"row_id"`
		LastEventID string `json:"last_event_id"`
	}
	err := s.cursorDB.QueryRowContext(ctx, `SELECT row_id,last_event_id FROM freewill_notification_cursor WHERE id=1`).Scan(&cur.RowID, &cur.LastEventID)
	if err == nil {
		return cur.RowID, cur.LastEventID, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, "", err
	}
	d, err := s.outbox.Get(ctx, freewillNotifyCursorKey)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, "", nil
	}
	if err != nil {
		return 0, "", err
	}
	if err := json.Unmarshal(d.Command.Payload, &cur); err != nil {
		return 0, "", nil
	}
	if err := s.saveCursor(ctx, cur.RowID, cur.LastEventID); err != nil {
		return 0, "", err
	}
	return cur.RowID, cur.LastEventID, nil
}

// saveCursor upserts the cursor checkpoint independently of the immutable
// notification outbox commands.
func (s *FreewillNotificationService) saveCursor(ctx context.Context, rowID int64, lastEventID string) error {
	_, err := s.cursorDB.ExecContext(ctx, `INSERT INTO freewill_notification_cursor(id,row_id,last_event_id)
		VALUES(1,?,?) ON CONFLICT(id) DO UPDATE SET row_id=excluded.row_id,last_event_id=excluded.last_event_id`, rowID, lastEventID)
	return err
}

func (s *FreewillNotificationService) Close() error {
	s.onceStop.Do(func() { close(s.stop) })
	select {
	case <-s.stopped:
	case <-time.After(3 * time.Second):
	}
	return errors.Join(s.outbox.Close(), s.cursorDB.Close())
}
