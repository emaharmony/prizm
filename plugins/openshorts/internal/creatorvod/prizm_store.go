package creatorvod

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/emaharmony/prizm/internal/event"
	"github.com/emaharmony/prizm/internal/workflow/multiagent"
	sqlite "modernc.org/sqlite"
)

const (
	workflowID  = "creator-vod-stage1"
	ownerRole   = multiagent.Role("lumi")
	maxManifest = 4 << 20
	eventPrefix = "creator.vod."
)

// Store uses a dedicated Prizm durable run store. The native transaction
// commits the snapshot reference and its outbox event together.
type Store struct {
	runs        *multiagent.SQLiteDurableRunStore
	snapshotDir string
}

func OpenStore(dir string) (*Store, error) {
	if dir == "" {
		return nil, errors.New("creator VOD scratch directory is required")
	}
	snapshots := filepath.Join(dir, "manifests")
	if err := os.MkdirAll(snapshots, 0o700); err != nil {
		return nil, err
	}
	runs, err := multiagent.NewSQLiteDurableRunStore(filepath.Join(dir, "creator-vod.db"))
	if err != nil {
		return nil, err
	}
	return &Store{runs: runs, snapshotDir: snapshots}, nil
}

func (s *Store) Close() error { return s.runs.Close() }

func (s *Store) Status(ctx context.Context, id string) (Job, error) {
	record, err := s.runs.Load(ctx, id)
	if err != nil {
		return Job{}, err
	}
	return s.readManifest(record)
}

func (s *Store) accept(ctx context.Context, input Ready) (Job, error) {
	job := Job{
		ID: input.jobID(), Revision: 1, Input: input, State: Accepted,
		CorrelationID: event.NewCorrelationID(),
	}
	current, err := s.Status(ctx, job.ID)
	if err == nil {
		return current, nil
	}
	if !errors.Is(err, multiagent.ErrRunNotFound) {
		return Job{}, err
	}
	evt, artifact, err := s.prepare(&job, "ready", "fact")
	if err != nil {
		return Job{}, err
	}
	record := multiagent.DurableRun{
		SchemaVersion: multiagent.DurableRunSchemaVersion,
		State: multiagent.RunState{
			SchemaVersion: multiagent.RunStateSchemaVersion, RunID: job.ID,
			WorkflowID: workflowID, WorkflowVersion: 1,
			WorkflowUserVersion: input.WorkflowVersion,
			CurrentRole:         ownerRole,
			CurrentTask:         multiagent.TaskReference{ID: job.ID},
			CreatedAt:           time.Now().UTC(),
			RequestIdentity:     &multiagent.RequestIdentity{IdempotencyKey: job.ID, Source: workflowID},
		},
	}
	project(&record, job, artifact)

	// Concurrent deferred SQLite writers can get BUSY during a read-to-
	// write upgrade. Retry only the local transaction, with the same key.
	for attempt := 0; ; attempt++ {
		accepted, _, err := s.runs.AcceptRun(ctx, record, []event.Event{evt})
		if err == nil {
			return s.readManifest(accepted)
		}
		var sqlErr *sqlite.Error
		if !errors.As(err, &sqlErr) || sqlErr.Code()&255 != 5 || attempt == 4 {
			return Job{}, err
		}
		timer := time.NewTimer(time.Duration(attempt+1) * 10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return Job{}, ctx.Err()
		case <-timer.C:
		}
	}
}

func (s *Store) checkpoint(ctx context.Context, job Job, topic, kind string) (Job, error) {
	record, err := s.runs.Load(ctx, job.ID)
	if err != nil {
		return Job{}, err
	}
	expected := job.Revision
	if record.Revision != expected {
		return Job{}, multiagent.ErrRevisionConflict
	}
	job.Revision++
	evt, artifact, err := s.prepare(&job, topic, kind)
	if err != nil {
		return Job{}, err
	}
	project(&record, job, artifact)
	if _, err := s.runs.Checkpoint(ctx, expected, record, []event.Event{evt}); err != nil {
		return Job{}, err
	}
	return job, nil
}

func (s *Store) prepare(job *Job, topic, kind string) (event.Event, multiagent.ArtifactRef, error) {
	evt := event.NewEvent(eventPrefix+topic+".v1", workflowID, map[string]any{
		"contract_version": Version,
		"job_id":           job.ID,
		"task_id":          string(ownerRole),
		"attempt_id":       "attempt-1",
		"revision":         job.Revision,
		"policy_hash":      job.Input.PolicyHash,
		"kind":             kind,
		"state":            job.State,
		"reason":           job.Reason,
		"execution_key":    job.ExecutionKey,
	}).WithCorrelationID(job.CorrelationID).WithParentID(job.LastEventID).
		WithMetadata(event.EventMetadata{RunID: job.ID})
	job.LastEventID = evt.ID
	artifact, err := s.writeManifest(*job)
	if err != nil {
		return event.Event{}, multiagent.ArtifactRef{}, err
	}
	evt.Payload["payload_ref"] = artifact.URI
	evt.Payload["payload_hash"] = artifact.Digest
	return evt, artifact, nil
}

func project(record *multiagent.DurableRun, job Job, artifact multiagent.ArtifactRef) {
	now := time.Now().UTC()
	role := multiagent.RoleState{Role: ownerRole, UpdatedAt: now, Artifacts: []multiagent.ArtifactRef{artifact}}
	record.State.UpdatedAt = now
	record.State.Status = multiagent.RunStatusRunning
	role.Status = multiagent.RoleStatusRunning
	record.Phase = multiagent.CheckpointRoleRunning
	record.ActiveExecutionKey = job.ExecutionKey

	var condition multiagent.TerminalCondition
	switch job.State {
	case Accepted:
		record.Phase = multiagent.CheckpointPendingRole
		record.State.Status = multiagent.RunStatusCreated
		role.Status = multiagent.RoleStatusPending
	case Completed, NoQualifyingClips:
		record.State.Status = multiagent.RunStatusCompleted
		role.Status, condition = multiagent.RoleStatusCompleted, multiagent.TerminalConditionCompleted
	case Failed:
		record.State.Status = multiagent.RunStatusFailed
		role.Status, condition = multiagent.RoleStatusFailed, multiagent.TerminalConditionFailed
	case Cancelled:
		record.State.Status = multiagent.RunStatusCancelled
		role.Status, condition = multiagent.RoleStatusCancelled, multiagent.TerminalConditionCancelled
	case NeedsReview:
		record.State.Status = multiagent.RunStatusPaused
		record.Phase, role.Status = multiagent.CheckpointWaiting, multiagent.RoleStatusWaiting
		record.Waiting = &multiagent.WaitingState{Kind: "creator_vod_review", Reason: job.Reason, Since: now}
	}
	if job.State != NeedsReview {
		record.Waiting = nil
	}
	if condition != "" {
		record.Phase, record.ActiveExecutionKey = multiagent.CheckpointTerminal, ""
		record.State.CompletedAt, role.CompletedAt = &now, &now
		record.State.TerminalOutcome = &multiagent.TerminalOutcome{
			Condition: condition, Reason: string(job.State), At: now,
		}
	}
	record.State.RoleStates = map[multiagent.Role]multiagent.RoleState{ownerRole: role}
}

func (s *Store) writeManifest(job Job) (multiagent.ArtifactRef, error) {
	data, err := json.Marshal(job)
	if err != nil {
		return multiagent.ArtifactRef{}, err
	}
	if len(data) > maxManifest {
		return multiagent.ArtifactRef{}, fmt.Errorf("%w: manifest too large", ErrContract)
	}
	// Each writer gets a fresh file. Until a SQLite transaction references
	// it, even a partial file is unreachable from job state or the outbox.
	f, err := os.CreateTemp(s.snapshotDir, "manifest-*.json")
	if err != nil {
		return multiagent.ArtifactRef{}, err
	}
	_, writeErr := f.Write(data)
	if writeErr == nil {
		writeErr = f.Sync()
	}
	if err := errors.Join(writeErr, f.Close()); err != nil {
		return multiagent.ArtifactRef{}, err
	}
	return multiagent.ArtifactRef{
		Kind: multiagent.ArtifactReport, URI: filepath.Base(f.Name()), Digest: digest(data),
	}, nil
}

func (s *Store) readManifest(record multiagent.DurableRun) (Job, error) {
	refs := record.State.RoleStates[ownerRole].Artifacts
	if record.State.WorkflowID != workflowID || len(refs) != 1 ||
		refs[0].Kind != multiagent.ArtifactReport || !identifier.MatchString(refs[0].URI) ||
		!strings.HasPrefix(refs[0].URI, "manifest-") || !strings.HasSuffix(refs[0].URI, ".json") {
		return Job{}, fmt.Errorf("%w: invalid durable manifest reference", ErrContract)
	}
	root, err := os.OpenRoot(s.snapshotDir)
	if err != nil {
		return Job{}, err
	}
	defer root.Close()
	f, err := root.Open(refs[0].URI)
	if err != nil {
		return Job{}, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxManifest+1))
	if err != nil {
		return Job{}, err
	}
	if len(data) > maxManifest || digest(data) != refs[0].Digest {
		return Job{}, fmt.Errorf("%w: durable manifest hash mismatch", ErrContract)
	}
	var job Job
	if err := decodeStrict(data, &job); err != nil {
		return Job{}, err
	}
	if job.ID != record.State.RunID || job.Revision != record.Revision || job.ID != job.Input.jobID() {
		return Job{}, fmt.Errorf("%w: durable manifest identity mismatch", ErrContract)
	}
	return job, nil
}
