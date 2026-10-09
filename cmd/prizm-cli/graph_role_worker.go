package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/emaharmony/prizm/internal/event"
	"github.com/emaharmony/prizm/internal/workflow/multiagent"
	"github.com/nats-io/nats.go"
)

const graphRoleOutcomeSubject = graphRoleDelegationSubject + ".outcome"

type natsCommandPublisher struct{ nc *nats.Conn }

func (p natsCommandPublisher) Publish(ctx context.Context, subject string, payload []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if p.nc == nil || !p.nc.IsConnected() {
		return errors.New("graph role NATS publisher is disconnected")
	}
	if err := p.nc.Publish(subject, payload); err != nil {
		return err
	}
	return p.nc.FlushWithContext(ctx)
}

type graphRoleWorker struct {
	nc          *nats.Conn
	runDir      string
	configPath  string
	resume      func(context.Context, string) error
	executeRole func(context.Context, multiagent.GraphRoleCommand) (multiagent.RoleRunResult, error)
	sem         chan struct{}
	mu          sync.Mutex
	running     map[string]struct{}
	workerID    string
	subs        []*nats.Subscription
	cancel      context.CancelFunc
	done        chan struct{}
	publish     func(context.Context, string, []byte) error
}

func startGraphRoleWorker(nc *nats.Conn, runDir, configPath string, resume func(context.Context, string) error) (*graphRoleWorker, error) {
	return startGraphRoleWorkerWithRunner(nc, runDir, configPath, resume, nil)
}

func startGraphRoleWorkerWithRunner(nc *nats.Conn, runDir, configPath string, resume func(context.Context, string) error, executeRole func(context.Context, multiagent.GraphRoleCommand) (multiagent.RoleRunResult, error)) (*graphRoleWorker, error) {
	if nc == nil || !nc.IsConnected() {
		return nil, errors.New("graph role worker requires a connected NATS bus")
	}
	w := &graphRoleWorker{nc: nc, runDir: runDir, configPath: configPath, resume: resume, executeRole: executeRole,
		sem: make(chan struct{}, subAgentMaxConcurrency), running: make(map[string]struct{}), workerID: event.NewID()}
	commands, err := nc.QueueSubscribe(graphRoleDelegationSubject, "prizm-graph-role-workers", w.handleCommand)
	if err != nil {
		return nil, err
	}
	outcomes, err := nc.Subscribe(graphRoleOutcomeSubject, w.handleOutcome)
	if err != nil {
		commands.Unsubscribe()
		return nil, err
	}
	w.subs = []*nats.Subscription{commands, outcomes}
	if err := nc.Flush(); err != nil {
		w.Close()
		return nil, err
	}
	wakeCtx, cancel := context.WithCancel(context.Background())
	w.cancel, w.done = cancel, make(chan struct{})
	go w.deadlineLoop(wakeCtx)
	return w, nil
}

func (w *graphRoleWorker) Close() error {
	if w.cancel != nil {
		w.cancel()
		<-w.done
	}
	var errs []error
	for _, sub := range w.subs {
		errs = append(errs, sub.Unsubscribe())
	}
	return errors.Join(errs...)
}

func (w *graphRoleWorker) deadlineLoop(ctx context.Context) {
	defer close(w.done)
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.wakeExpired(ctx, time.Now().UTC())
		}
	}
}

func (w *graphRoleWorker) wakeExpired(ctx context.Context, now time.Time) {
	entries, err := os.ReadDir(w.runDir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		store, openErr := multiagent.NewSQLiteDurableRunStore(filepath.Join(w.runDir, entry.Name(), "multiagent.db"))
		if openErr != nil {
			continue
		}
		record, loadErr := store.Load(ctx, entry.Name())
		store.Close()
		if loadErr != nil || record.Waiting == nil || record.Waiting.Kind != "delegation_outcome" || now.Before(record.Waiting.Deadline) {
			continue
		}
		w.resumeRun(record.State.RunID, "deadline wake")
	}
}

func (w *graphRoleWorker) resumeRun(runID, reason string) {
	if w.resume == nil {
		return
	}
	key := "resume:" + runID
	w.mu.Lock()
	if w.running == nil {
		w.running = make(map[string]struct{})
	}
	if _, exists := w.running[key]; exists {
		w.mu.Unlock()
		return
	}
	w.running[key] = struct{}{}
	w.mu.Unlock()
	defer func() {
		w.mu.Lock()
		delete(w.running, key)
		w.mu.Unlock()
	}()
	if err := w.resume(context.Background(), runID); err != nil {
		var waiting *multiagent.RunWaitingError
		if !errors.As(err, &waiting) {
			log.Printf("[GRAPH-WORKER] %s %s: %v", reason, runID, err)
		}
	}
}

func (w *graphRoleWorker) handleCommand(msg *nats.Msg) {
	var command multiagent.GraphRoleCommand
	if err := json.Unmarshal(msg.Data, &command); err != nil {
		log.Printf("[GRAPH-WORKER] rejected undecodable command: %v", err)
		return
	}
	ledgerPath, err := w.verifyDurableCommand(context.Background(), command, msg.Data)
	if err != nil {
		log.Printf("[GRAPH-WORKER] rejected untrusted command %s: %v", command.DeliveryKey, err)
		return
	}
	if err := w.validateCommand(command); err != nil {
		if claim, _, claimErr := w.claimExecution(context.Background(), ledgerPath, command); claimErr == nil && claim == "execute" {
			if publishErr := w.persistAndPublishTerminal(context.Background(), ledgerPath, command, event.OutcomeRejected, map[string]string{"message": err.Error()}); publishErr != nil {
				log.Printf("[GRAPH-WORKER] reject terminal %s: %v", command.DeliveryKey, publishErr)
			}
		}
		return
	}
	w.mu.Lock()
	if _, exists := w.running[command.DeliveryKey]; exists {
		w.mu.Unlock()
		return
	}
	w.running[command.DeliveryKey] = struct{}{}
	w.mu.Unlock()
	go func() {
		w.sem <- struct{}{}
		defer func() {
			<-w.sem
			w.mu.Lock()
			delete(w.running, command.DeliveryKey)
			w.mu.Unlock()
		}()
		w.execute(command)
	}()
}

func (w *graphRoleWorker) verifyDurableCommand(ctx context.Context, command multiagent.GraphRoleCommand, wire []byte) (string, error) {
	if strings.TrimSpace(command.RunID) == "" || strings.TrimSpace(command.DeliveryKey) == "" {
		return "", errors.New("command lacks run or delivery identity")
	}
	manifest, err := loadReferenceManifest(w.runDir, command.RunID)
	if err != nil {
		return "", err
	}
	path := filepath.Join(w.runDir, manifest.RunID, "multiagent.db")
	box, err := event.NewSQLiteOutbox(path)
	if err != nil {
		return "", err
	}
	stored, err := box.Get(ctx, command.DeliveryKey)
	box.Close()
	if err != nil {
		return "", err
	}
	cmd := stored.Command
	if stored.Subject != graphRoleDelegationSubject || cmd.Type != multiagent.GraphRoleDelegationCommandType ||
		cmd.EventID != command.CommandEventID || cmd.RunID != command.RunID || cmd.TaskID != command.ChildID ||
		cmd.DelegationID != command.DelegationID || cmd.CorrelationID != command.CorrelationID ||
		cmd.IdempotencyKey != command.DeliveryKey || !cmd.Deadline.Equal(command.Deadline) || !bytes.Equal(cmd.Payload, wire) {
		return "", errors.New("wire command does not match canonical outbox row")
	}
	return path, nil
}

func (w *graphRoleWorker) validateCommand(command multiagent.GraphRoleCommand) error {
	if strings.TrimSpace(command.RunID) == "" || strings.TrimSpace(command.ChildID) == "" ||
		strings.TrimSpace(command.DelegationID) == "" || strings.TrimSpace(command.DeliveryKey) == "" ||
		strings.TrimSpace(command.CommandEventID) == "" || strings.TrimSpace(command.ExecutionKey) == "" ||
		command.Deadline.IsZero() {
		return errors.New("graph role command identity is incomplete")
	}
	if command.Request.Run.RunID != command.RunID || command.Request.Run.Task.ID != command.Task.ID ||
		command.Request.Run.CurrentRole != command.Role || command.Request.Run.ExecutionKey != command.ExecutionKey ||
		command.Request.Run.WorkspaceID != command.WorkspaceID || command.CommandEventID != event.CommandEventID(command.DeliveryKey) {
		return errors.New("graph role command context does not match its durable identity")
	}
	return nil
}

func (w *graphRoleWorker) execute(command multiagent.GraphRoleCommand) {
	ctx := context.Background()
	manifest, err := loadReferenceManifest(w.runDir, command.RunID)
	if err != nil {
		return
	}
	ledgerPath := filepath.Join(w.runDir, manifest.RunID, "multiagent.db")
	claim, replay, err := w.claimExecution(ctx, ledgerPath, command)
	if err != nil {
		log.Printf("[GRAPH-WORKER] claim %s: %v", command.DeliveryKey, err)
		return
	}
	switch claim {
	case "ignore":
		return
	case "replay":
		_ = w.publishMessage(ctx, graphRoleOutcomeSubject, replay)
		return
	case "ambiguous":
		outcome, encoded, buildErr := w.buildOutcome(command, event.OutcomeFailed, 2, map[string]string{"message": "worker ownership changed during role execution; outcome is ambiguous and was failed closed"})
		if buildErr == nil && w.storeTerminalOutcome(ctx, ledgerPath, command.DeliveryKey, outcome, encoded) == nil {
			_ = w.nc.Publish(graphRoleOutcomeSubject, encoded)
			_ = w.nc.Flush()
		}
		return
	}
	if err := w.persistAcceptedAndPublish(ctx, ledgerPath, command); err != nil {
		// Acceptance is durable before publication. Continue the single claimed
		// execution so a terminal result can still close the ledger when NATS is
		// temporarily unavailable; a later redelivery will replay that result.
		log.Printf("[GRAPH-WORKER] accept publication %s: %v", command.DeliveryKey, err)
	}
	if !time.Now().UTC().Before(command.Deadline) {
		if publishErr := w.persistAndPublishTerminal(ctx, ledgerPath, command, event.OutcomeTimedOut, map[string]string{"message": "command deadline exceeded before worker acceptance"}); publishErr != nil {
			log.Printf("[GRAPH-WORKER] terminal %s: %v", command.DeliveryKey, publishErr)
		}
		return
	}
	if manifest.WorkspaceID != command.WorkspaceID {
		err = fmt.Errorf("persisted workspace %q does not match command workspace %q", manifest.WorkspaceID, command.WorkspaceID)
	}
	var result multiagent.RoleRunResult
	if err == nil {
		execCtx, cancel := context.WithDeadline(ctx, command.Deadline)
		if w.executeRole != nil {
			result, err = w.executeRole(execCtx, command)
		} else {
			var components *liveReferenceComponents
			components, err = buildLiveReferenceComponents(w.runDir, w.configPath, manifest)
			if err == nil {
				result, err = components.runner.RunRole(execCtx, command.Request)
			}
		}
		cancel()
	}
	if err != nil {
		status := event.OutcomeFailed
		if errors.Is(err, context.DeadlineExceeded) || !time.Now().UTC().Before(command.Deadline) {
			status = event.OutcomeTimedOut
		}
		if publishErr := w.persistAndPublishTerminal(ctx, ledgerPath, command, status, map[string]string{"message": err.Error()}); publishErr != nil {
			log.Printf("[GRAPH-WORKER] terminal %s: %v", command.DeliveryKey, publishErr)
		}
		return
	}
	if !command.Deadline.IsZero() && !time.Now().UTC().Before(command.Deadline) {
		if publishErr := w.persistAndPublishTerminal(ctx, ledgerPath, command, event.OutcomeTimedOut, map[string]string{"message": "role execution exceeded command deadline"}); publishErr != nil {
			log.Printf("[GRAPH-WORKER] terminal %s: %v", command.DeliveryKey, publishErr)
		}
		return
	}
	if err := w.persistAndPublishTerminal(ctx, ledgerPath, command, event.OutcomeSucceeded, multiagent.GraphRoleOutcome{Result: result}); err != nil {
		log.Printf("[GRAPH-WORKER] terminal %s: %v", command.DeliveryKey, err)
	}
}

func (w *graphRoleWorker) publishWithRetry(command multiagent.GraphRoleCommand, status event.OutcomeStatus, sequence int64, payload any) error {
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		if err = w.publishOutcome(command, status, sequence, payload); err == nil {
			return nil
		}
		time.Sleep(time.Duration(attempt+1) * 50 * time.Millisecond)
	}
	return err
}

func (w *graphRoleWorker) persistAcceptedAndPublish(ctx context.Context, ledgerPath string, command multiagent.GraphRoleCommand) error {
	_, encoded, err := w.buildOutcome(command, event.OutcomeAccepted, 1, nil)
	if err != nil {
		return err
	}
	db, err := openGraphRoleLedger(ctx, ledgerPath)
	if err != nil {
		return err
	}
	defer db.Close()
	res, err := db.ExecContext(ctx, `UPDATE graph_role_executions SET accepted_json=?,updated_at=? WHERE delivery_key=? AND state='running' AND owner=?`,
		encoded, time.Now().UTC().Format(time.RFC3339Nano), command.DeliveryKey, w.workerID)
	if err != nil {
		return err
	}
	if count, _ := res.RowsAffected(); count != 1 {
		return errors.New("graph role acceptance was not bound to the active execution claim")
	}
	var publishErr error
	for attempt := 0; attempt < 3; attempt++ {
		publishErr = w.publishMessage(ctx, graphRoleOutcomeSubject, encoded)
		if publishErr == nil {
			return nil
		}
		time.Sleep(time.Duration(attempt+1) * 50 * time.Millisecond)
	}
	return publishErr
}

func (w *graphRoleWorker) persistAndPublishTerminal(ctx context.Context, ledgerPath string, command multiagent.GraphRoleCommand, status event.OutcomeStatus, payload any) error {
	outcome, encoded, err := w.buildOutcome(command, status, 2, payload)
	if err != nil {
		return err
	}
	if err := w.storeTerminalOutcome(ctx, ledgerPath, command.DeliveryKey, outcome, encoded); err != nil {
		return err
	}
	return w.publishMessage(ctx, graphRoleOutcomeSubject, encoded)
}

func (w *graphRoleWorker) publishOutcome(command multiagent.GraphRoleCommand, status event.OutcomeStatus, sequence int64, payload any) error {
	_, data, err := w.buildOutcome(command, status, sequence, payload)
	if err != nil {
		return err
	}
	return w.publishMessage(context.Background(), graphRoleOutcomeSubject, data)
}

func (w *graphRoleWorker) publishMessage(ctx context.Context, subject string, payload []byte) error {
	if w.publish != nil {
		return w.publish(ctx, subject, payload)
	}
	if w.nc == nil || !w.nc.IsConnected() {
		return errors.New("graph role NATS publisher is disconnected")
	}
	if err := w.nc.Publish(subject, payload); err != nil {
		return err
	}
	if _, hasDeadline := ctx.Deadline(); hasDeadline {
		return w.nc.FlushWithContext(ctx)
	}
	return w.nc.Flush()
}

func (w *graphRoleWorker) buildOutcome(command multiagent.GraphRoleCommand, status event.OutcomeStatus, sequence int64, payload any) (event.Outcome, []byte, error) {
	var raw json.RawMessage
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return event.Outcome{}, nil, err
		}
		raw = encoded
	}
	outcome := event.Outcome{EventID: event.CommandEventID(command.DeliveryKey + ":" + string(status)),
		CommandEventID: command.CommandEventID, RunID: command.RunID, TaskID: command.ChildID,
		DelegationID: command.DelegationID, CorrelationID: command.CorrelationID,
		CausationID: command.CommandEventID, DeliveryKey: command.DeliveryKey,
		Status: status, Sequence: sequence, OccurredAt: time.Now().UTC(), Payload: raw}
	data, err := json.Marshal(outcome)
	if err != nil {
		return event.Outcome{}, nil, err
	}
	return outcome, data, nil
}

func (w *graphRoleWorker) claimExecution(ctx context.Context, path string, command multiagent.GraphRoleCommand) (string, []byte, error) {
	db, err := openGraphRoleLedger(ctx, path)
	if err != nil {
		return "", nil, err
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS graph_role_executions (
		delivery_key TEXT PRIMARY KEY, command_json BLOB NOT NULL, owner TEXT NOT NULL,
		state TEXT NOT NULL, accepted_json BLOB, outcome_json BLOB, updated_at TEXT NOT NULL)`); err != nil {
		return "", nil, err
	}
	_, _ = db.ExecContext(ctx, `ALTER TABLE graph_role_executions ADD COLUMN accepted_json BLOB`)
	commandJSON, err := json.Marshal(command)
	if err != nil {
		return "", nil, err
	}
	res, err := db.ExecContext(ctx, `INSERT OR IGNORE INTO graph_role_executions
		(delivery_key,command_json,owner,state,updated_at) VALUES(?,?,?,?,?)`,
		command.DeliveryKey, commandJSON, w.workerID, "running", time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return "", nil, err
	}
	if count, _ := res.RowsAffected(); count == 1 {
		return "execute", nil, nil
	}
	var existing, outcome []byte
	var owner, state string
	if err := db.QueryRowContext(ctx, `SELECT command_json,owner,state,outcome_json FROM graph_role_executions WHERE delivery_key=?`, command.DeliveryKey).
		Scan(&existing, &owner, &state, &outcome); err != nil {
		return "", nil, err
	}
	if !bytes.Equal(existing, commandJSON) {
		return "", nil, errors.New("delivery key reused with different graph role command")
	}
	if state == "terminal" && len(outcome) != 0 {
		return "replay", outcome, nil
	}
	if owner == w.workerID {
		return "ignore", nil, nil
	}
	return "ambiguous", nil, nil
}

func (w *graphRoleWorker) storeTerminalOutcome(ctx context.Context, path, key string, outcome event.Outcome, encoded []byte) error {
	db, err := openGraphRoleLedger(ctx, path)
	if err != nil {
		return err
	}
	defer db.Close()
	res, err := db.ExecContext(ctx, `UPDATE graph_role_executions SET state='terminal',outcome_json=?,updated_at=?
		WHERE delivery_key=? AND state='running'`, encoded, outcome.OccurredAt.Format(time.RFC3339Nano), key)
	if err != nil {
		return err
	}
	if count, _ := res.RowsAffected(); count == 1 {
		return nil
	}
	var existing []byte
	if err := db.QueryRowContext(ctx, `SELECT outcome_json FROM graph_role_executions WHERE delivery_key=? AND state='terminal'`, key).Scan(&existing); err != nil {
		return err
	}
	if !bytes.Equal(existing, encoded) {
		return errors.New("graph role execution already has a different terminal outcome")
	}
	return nil
}

func openGraphRoleLedger(ctx context.Context, path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, `PRAGMA busy_timeout = 5000`); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func (w *graphRoleWorker) handleOutcome(msg *nats.Msg) {
	var outcome event.Outcome
	if json.Unmarshal(msg.Data, &outcome) != nil || outcome.Validate() != nil {
		return
	}
	manifest, err := loadReferenceManifest(w.runDir, outcome.RunID)
	if err != nil {
		return
	}
	ledgerPath := filepath.Join(w.runDir, manifest.RunID, "multiagent.db")
	if err := w.verifyPersistedOutcome(context.Background(), ledgerPath, outcome, msg.Data); err != nil {
		log.Printf("[GRAPH-WORKER] rejected untrusted outcome %s: %v", outcome.EventID, err)
		return
	}
	box, err := event.NewSQLiteOutbox(ledgerPath)
	if err != nil {
		return
	}
	inserted, recordErr := box.RecordOutcome(context.Background(), outcome)
	box.Close()
	if recordErr != nil {
		log.Printf("[GRAPH-WORKER] reject outcome %s: %v", outcome.EventID, recordErr)
		return
	}
	if inserted {
		w.resumeRun(outcome.RunID, "outcome resume")
	}
}

func (w *graphRoleWorker) verifyPersistedOutcome(ctx context.Context, path string, outcome event.Outcome, wire []byte) error {
	db, err := openGraphRoleLedger(ctx, path)
	if err != nil {
		return err
	}
	defer db.Close()
	var accepted, terminal []byte
	if err := db.QueryRowContext(ctx, `SELECT accepted_json,outcome_json FROM graph_role_executions WHERE delivery_key=?`, outcome.DeliveryKey).
		Scan(&accepted, &terminal); err != nil {
		return err
	}
	expected := terminal
	if outcome.Status == event.OutcomeAccepted {
		expected = accepted
	}
	if len(expected) == 0 || !bytes.Equal(expected, wire) {
		return errors.New("outcome does not match trusted worker ledger")
	}
	return nil
}
