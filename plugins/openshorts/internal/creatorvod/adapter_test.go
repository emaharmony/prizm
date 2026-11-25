package creatorvod

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/emaharmony/prizm/internal/sqlite"
	"github.com/emaharmony/prizm/internal/workflow/multiagent"
)

var errLostReply = errors.New("simulated lost reply")

// The fake owner and sink have their own durable database. Their effects
// survive closing and reopening the adapter and can be counted independently.
type mockEffects struct {
	db              *sql.DB
	submitCalls     atomic.Int64
	exportCalls     atomic.Int64
	loseSubmitReply bool
	loseExportReply bool
	deferCancel     bool
	corruptExport   bool
}

func openEffects(t *testing.T, dir string) *mockEffects {
	t.Helper()
	db, err := sql.Open(sqlite.DriverName, filepath.Join(dir, "mock-effects.db")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS owner (key TEXT PRIMARY KEY, receipt TEXT NOT NULL);
	CREATE TABLE IF NOT EXISTS exports (key TEXT PRIMARY KEY, receipt TEXT NOT NULL);`)
	if err != nil {
		t.Fatal(err)
	}
	return &mockEffects{db: db}
}

func (m *mockEffects) Submit(ctx context.Context, job Job) (OwnerReceipt, error) {
	m.submitCalls.Add(1)
	result := OwnerReceipt{
		Version: Version, ExecutionKey: job.ExecutionKey, DispatchRevision: job.DispatchRevision,
		RemoteID: "mock-" + job.ID, Sequence: 1, Status: OwnerRunning,
	}
	data, err := json.Marshal(result)
	if err != nil {
		return OwnerReceipt{}, err
	}
	if _, err := m.db.ExecContext(ctx, "INSERT OR IGNORE INTO owner VALUES (?, ?)", job.ExecutionKey, data); err != nil {
		return OwnerReceipt{}, err
	}
	if m.loseSubmitReply {
		return OwnerReceipt{}, errLostReply
	}
	saved, err := m.Status(ctx, job.ExecutionKey)
	if err != nil {
		return OwnerReceipt{}, err
	}
	return *saved, nil
}

func (m *mockEffects) Status(ctx context.Context, key string) (*OwnerReceipt, error) {
	var data []byte
	err := m.db.QueryRowContext(ctx, "SELECT receipt FROM owner WHERE key=?", key).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var receipt OwnerReceipt
	return &receipt, json.Unmarshal(data, &receipt)
}

func (m *mockEffects) Cancel(ctx context.Context, key string) (*OwnerReceipt, error) {
	receipt, err := m.Status(ctx, key)
	if err != nil || receipt == nil || receipt.Status != OwnerRunning || m.deferCancel {
		return receipt, err
	}
	receipt.Status = OwnerCancelled
	receipt.Sequence++
	return receipt, m.saveReceipt(ctx, *receipt)
}

func (m *mockEffects) saveReceipt(ctx context.Context, receipt OwnerReceipt) error {
	data, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	_, err = m.db.ExecContext(ctx, "UPDATE owner SET receipt=? WHERE key=?", data, receipt.ExecutionKey)
	return err
}

func (m *mockEffects) Lookup(ctx context.Context, key string) (*ExportReceipt, error) {
	var data []byte
	err := m.db.QueryRowContext(ctx, "SELECT receipt FROM exports WHERE key=?", key).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var receipt ExportReceipt
	return &receipt, json.Unmarshal(data, &receipt)
}

func (m *mockEffects) Put(ctx context.Context, key string, clip Clip) (ExportReceipt, error) {
	m.exportCalls.Add(1)
	result := ExportReceipt{
		Version: Version, Key: key, ArtifactHash: clip.RenderHash,
		VerdictID: clip.VerdictID, DestinationRef: "mock-export-" + key,
	}
	if m.corruptExport {
		result.ArtifactHash = digest([]byte("incorrect bytes at destination"))
	}
	data, err := json.Marshal(result)
	if err != nil {
		return ExportReceipt{}, err
	}
	if _, err := m.db.ExecContext(ctx, "INSERT OR IGNORE INTO exports VALUES (?, ?)", key, data); err != nil {
		return ExportReceipt{}, err
	}
	if m.loseExportReply {
		return ExportReceipt{}, errLostReply
	}
	stored, err := m.Lookup(ctx, key)
	if err != nil {
		return ExportReceipt{}, err
	}
	return *stored, nil
}

func openTestStore(t *testing.T, dir string) *Store {
	t.Helper()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func fixtureReady(t *testing.T) Ready {
	t.Helper()
	raw, err := os.ReadFile("testdata/ready.json")
	if err != nil {
		t.Fatal(err)
	}
	var input Ready
	if err := decodeStrict(raw, &input); err != nil {
		t.Fatal(err)
	}
	return input
}

func fixtureClip(input Ready, index, score int) Clip {
	id := fmt.Sprintf("clip-%02d", index)
	return Clip{
		ID: id, StartMS: int64(index) * 10_000, EndMS: int64(index+1) * 10_000,
		SourceHash: input.SourceHash, ArtifactRef: "render-" + id,
		RenderHash: digest([]byte(id)), PolicyHash: input.PolicyHash,
		VerdictID: "qa-" + id, QAHash: digest([]byte(id)), ScoreHundredths: &score,
		Gates: map[string]Gate{
			"webcam": Pass, "gameplay": Pass, "audio": Pass, "boundaries": Pass,
			"caption_policy": Pass, "hook_policy": Pass,
		},
		QAEvidence: []string{"review-frame"},
	}
}

func admit(t *testing.T, a *Adapter) Job {
	t.Helper()
	job, err := a.Admit(t.Context(), fixtureReady(t))
	if err != nil {
		t.Fatal(err)
	}
	return job
}

func step(t *testing.T, a *Adapter, id string, expected State) Job {
	t.Helper()
	job, err := a.Advance(t.Context(), id)
	if err != nil || job.State != expected {
		t.Fatalf("advance: got %s (%v), want %s", job.State, err, expected)
	}
	return job
}

func TestMockWorkflowRecoversAfterLostSubmissionAndExportReplies(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	store := openTestStore(t, dir)
	mock := openEffects(t, dir)
	mock.loseSubmitReply = true
	a := NewAdapter(store, mock, mock)
	job := admit(t, a)
	acceptedID := job.ID
	job = step(t, a, job.ID, NeedsReview)
	if job.Reason != submissionUnconfirmed || mock.submitCalls.Load() != 1 {
		t.Fatalf("lost reply was not recorded: %+v", job)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := mock.db.Close(); err != nil {
		t.Fatal(err)
	}

	store, mock = openTestStore(t, dir), openEffects(t, dir)
	a = NewAdapter(store, mock, mock)
	job = step(t, a, job.ID, Running)
	if mock.submitCalls.Load() != 0 {
		t.Fatal("submission repeated after restart")
	}
	done := *job.OwnerReceipt
	done.Sequence += 2 // One progress event was missed.
	done.Status, done.AnalysisComplete = OwnerSucceeded, true
	done.Clips = []Clip{fixtureClip(job.Input, 1, 850)}
	if err := mock.saveReceipt(ctx, done); err != nil {
		t.Fatal(err)
	}
	job = step(t, a, job.ID, Exporting)
	lastRevision := job.Revision
	if j, err := a.ApplyReceipt(ctx, job.ID, done); err != nil || j.Revision != lastRevision {
		t.Fatalf("duplicate receipt changed revision: %+v %v", j, err)
	}
	stale := done
	stale.Sequence, stale.Status, stale.AnalysisComplete, stale.Clips = 2, OwnerRunning, false, nil
	if j, err := a.ApplyReceipt(ctx, job.ID, stale); err != nil || j.Revision != lastRevision || j.State != Exporting {
		t.Fatalf("out-of-order receipt changed state: %+v %v", j, err)
	}

	mock.loseExportReply = true
	if _, err := a.Advance(ctx, job.ID); !errors.Is(err, errLostReply) {
		t.Fatalf("expected interruption after mock export, got %v", err)
	}
	j, err := store.Status(ctx, job.ID)
	if err != nil || len(j.Exports) != 0 || j.State != Exporting {
		t.Fatalf("unverified export was committed: %+v %v", j, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := mock.db.Close(); err != nil {
		t.Fatal(err)
	}

	store, mock = openTestStore(t, dir), openEffects(t, dir)
	a = NewAdapter(store, mock, mock)
	job = step(t, a, job.ID, Completed)
	if job.ID != acceptedID || len(job.Exports) != 1 || mock.exportCalls.Load() != 0 {
		t.Fatalf("restart did not adopt the export: %+v", job)
	}
	again := step(t, a, job.ID, Completed)
	if again.Revision != job.Revision {
		t.Fatal("terminal replay changed the checkpoint")
	}
	outbox, err := store.runs.PendingEvents(ctx, job.ID, 100)
	want := []string{"ready", "process.requested", "needs_review", "processing", "exporting", "completed"}
	if err != nil || len(outbox) != len(want) {
		t.Fatalf("outbox has %d events, error %v", len(outbox), err)
	}
	for i, entry := range outbox {
		evt := entry.Event
		if evt.Type != eventPrefix+want[i]+".v1" || evt.CorrelationID != job.CorrelationID ||
			entry.Revision != int64(i+1) || evt.Payload["payload_ref"] == "" ||
			(i > 0 && evt.ParentID != outbox[i-1].Event.ID) {
			t.Fatalf("outbox[%d] = %+v", i, entry)
		}
		ref, ok := evt.Payload["payload_ref"].(string)
		if !ok {
			t.Errorf("missing artifact ref in outbox event %d", i)
			continue
		}
		data, err := os.ReadFile(filepath.Join(store.snapshotDir, ref))
		if err != nil || digest(data) != evt.Payload["payload_hash"] {
			t.Fatalf("event snapshot integrity: %v", err)
		}
	}
}

func TestConcurrentAdmissionAndAdvanceDeduplicateAcrossStores(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	left, right := openTestStore(t, dir), openTestStore(t, dir)
	mock := openEffects(t, dir)
	adapters := []*Adapter{NewAdapter(left, mock, mock), NewAdapter(right, mock, mock)}
	input := fixtureReady(t)
	type result struct {
		job Job
		err error
	}
	results := make(chan result, 8)
	var wg sync.WaitGroup
	for i := range cap(results) {
		wg.Go(func() {
			candidate := input
			candidate.Name = fmt.Sprintf("same-content-%d.ready.mp4", i)
			job, err := adapters[i%2].Admit(ctx, candidate)
			results <- result{job, err}
		})
	}
	wg.Wait()
	close(results)
	for item := range results {
		if item.err != nil || item.job.ID != input.jobID() || item.job.Revision != 1 {
			t.Fatalf("incorrect concurrent admission: %+v", item)
		}
	}
	var submissions [2]error
	for i := range adapters {
		wg.Go(func() { _, submissions[i] = adapters[i].Advance(ctx, input.jobID()) })
	}
	wg.Wait()
	for _, err := range submissions {
		if err != nil {
			t.Fatal(err)
		}
	}
	step(t, adapters[0], input.jobID(), Running)
	if mock.submitCalls.Load() != 1 {
		t.Fatalf("submit called %d times", mock.submitCalls.Load())
	}
	input.Generation = "approved-replay"
	replay, err := adapters[0].Admit(ctx, input)
	if err != nil || replay.ID == fixtureReady(t).jobID() || replay.Revision != 1 {
		t.Fatalf("deliberate replay did not create its own identity: %+v %v", replay, err)
	}
	input.Generation = fixtureReady(t).Generation
	input.SourceHash = digest([]byte("changed content under the same filename"))
	input.ExecutionHash = input.SourceHash
	newSource, err := adapters[0].Admit(ctx, input)
	if err != nil || newSource.ID == fixtureReady(t).jobID() {
		t.Fatalf("changed bytes were deduplicated: %+v %v", newSource, err)
	}
}

func TestPreSendCrashStaysUnconfirmed(t *testing.T) {
	dir := t.TempDir()
	store, mock := openTestStore(t, dir), openEffects(t, dir)
	a := NewAdapter(store, mock, mock)
	job := admit(t, a)
	job.State, job.ExecutionKey, job.DispatchRevision = Dispatching, job.ID+"-attempt-1", 2
	if _, err := store.checkpoint(t.Context(), job, "process.requested", "command"); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store = openTestStore(t, dir)
	a = NewAdapter(store, mock, mock)
	review := step(t, a, job.ID, NeedsReview)
	again := step(t, a, job.ID, NeedsReview)
	if review.Revision != again.Revision || mock.submitCalls.Load() != 0 {
		t.Fatal("ambiguous command was resent")
	}
	if _, err := store.checkpoint(t.Context(), job, "process.requested", "command"); !errors.Is(err, multiagent.ErrRevisionConflict) {
		t.Fatalf("stale checkpoint not rejected: %v", err)
	}
}

func TestReadinessAndStrictContract(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	store, mock := openTestStore(t, dir), openEffects(t, dir)
	a := NewAdapter(store, mock, mock)
	input := fixtureReady(t)
	for name, change := range map[string]func(*Ready){
		"unfinalized":            func(r *Ready) { r.Name = "file.mp4" },
		"processed":              func(r *Ready) { r.Name = "file_done.ready.mp4" },
		"traversal":              func(r *Ready) { r.Name = "../file.ready.mp4" },
		"unstable":               func(r *Ready) { r.Stable = false },
		"partial":                func(r *Ready) { r.SizeAfter++ },
		"probe failed":           func(r *Ready) { r.ProbeOK = false },
		"execution path missing": func(r *Ready) { r.ExecutionHash = "" },
		"execution copy differs": func(r *Ready) { r.ExecutionHash = strings.Repeat("a", 64) },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := input
			change(&candidate)
			if _, err := a.Admit(ctx, candidate); !errors.Is(err, ErrNotReady) {
				t.Errorf("got %v", err)
			}
		})
	}
	invalid := input
	invalid.Version = "clip-contract/v2"
	if _, err := a.Admit(ctx, invalid); !errors.Is(err, ErrContract) {
		t.Fatal(err)
	}
	if err := decodeStrict([]byte(`{"arbitrary":true}`), new(Ready)); !errors.Is(err, ErrContract) {
		t.Fatal(err)
	}
	if err := decodeStrict([]byte(`{} {}`), new(Ready)); !errors.Is(err, ErrContract) {
		t.Fatal(err)
	}
	if _, err := store.Status(ctx, input.jobID()); !errors.Is(err, multiagent.ErrRunNotFound) {
		t.Fatalf("rejected input persisted a job: %v", err)
	}
	if mock.submitCalls.Load() != 0 {
		t.Fatal("invalid input reached the mock owner")
	}
}

func TestQAGatesBlockRelease(t *testing.T) {
	input := fixtureReady(t)
	clip := fixtureClip(input, 1, 930)
	for _, tt := range []struct {
		name        string
		change      func(*Clip)
		wantUnknown bool
	}{
		{"critical failure despite high score", func(c *Clip) { c.Gates["webcam"] = Fail }, false},
		{"critical evidence unknown", func(c *Clip) { c.Gates["webcam"] = Unknown }, true},
		{"critical gate absent", func(c *Clip) { delete(c.Gates, "webcam") }, true},
		{"critical gate cannot be waived", func(c *Clip) { c.Gates["webcam"] = NotApplicable }, true},
		{"stale QA hash", func(c *Clip) { c.QAHash = digest([]byte("other render")) }, true},
		{"empty QA evidence", func(c *Clip) { c.QAEvidence = nil }, true},
		{"under clip floor", func(c *Clip) { score := 799; c.ScoreHundredths = &score }, false},
		{"under single-clip batch average", func(c *Clip) { score := 849; c.ScoreHundredths = &score }, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c := clip
			c.Gates = fixtureClip(input, 1, 930).Gates
			tt.change(&c)
			selected, unresolved := qualify(OwnerReceipt{AnalysisComplete: true, Clips: []Clip{c}})
			if len(selected) != 0 || unresolved != tt.wantUnknown {
				t.Fatalf("selected %d, unresolved=%v", len(selected), unresolved)
			}
		})
	}
	clips := []Clip{fixtureClip(input, 1, 900), fixtureClip(input, 2, 800)}
	selected, unresolved := qualify(OwnerReceipt{AnalysisComplete: true, Clips: clips})
	if len(selected) != 2 || unresolved {
		t.Fatal("batch with exact average of 8.5 should qualify")
	}
	for index := 3; index <= 15; index++ {
		clips = append(clips, fixtureClip(input, index, 900))
	}
	duplicate := fixtureClip(input, 50, 990)
	duplicate.StartMS, duplicate.EndMS = clips[0].StartMS, clips[0].EndMS
	clips = append(clips, duplicate)
	selected, _ = qualify(OwnerReceipt{AnalysisComplete: true, Clips: clips})
	if len(selected) != maxDelivered || selected[0].ID != duplicate.ID {
		t.Fatal("expected ranked batch capped at ten")
	}
	for _, c := range selected[1:] {
		if c.StartMS == duplicate.StartMS {
			t.Fatal("duplicated source window in selected batch")
		}
	}
}

func TestInvalidReceiptAndExportCannotComplete(t *testing.T) {
	dir := t.TempDir()
	store, mock := openTestStore(t, dir), openEffects(t, dir)
	mock.corruptExport = true
	a := NewAdapter(store, mock, mock)
	job := admit(t, a)
	job = step(t, a, job.ID, Running)
	receipt := *job.OwnerReceipt
	receipt.DispatchRevision--
	if _, err := a.ApplyReceipt(t.Context(), job.ID, receipt); !errors.Is(err, ErrContract) {
		t.Fatalf("stale owner attempt not rejected: %v", err)
	}
	receipt = *job.OwnerReceipt
	receipt.Status = OwnerFailed
	if _, err := a.ApplyReceipt(t.Context(), job.ID, receipt); !errors.Is(err, ErrContract) {
		t.Fatalf("conflicting sequence not rejected: %v", err)
	}
	receipt = *job.OwnerReceipt
	receipt.Sequence++
	receipt.Status, receipt.AnalysisComplete = OwnerSucceeded, true
	receipt.Clips = []Clip{fixtureClip(job.Input, 1, 900)}
	if _, err := a.ApplyReceipt(t.Context(), job.ID, receipt); err != nil {
		t.Fatal(err)
	}
	review := step(t, a, job.ID, NeedsReview)
	if review.Reason != exportMismatch || len(review.Exports) > 0 {
		t.Fatalf("invalid export was accepted: %+v", review)
	}
}

func TestCancellationRequiresConfirmation(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	store, mock := openTestStore(t, dir), openEffects(t, dir)
	mock.deferCancel = true
	a := NewAdapter(store, mock, mock)
	job := admit(t, a)
	job = step(t, a, job.ID, Running)
	pending, err := a.RequestCancel(ctx, job.ID)
	if err != nil || pending.State != Running || !pending.CancelRequested {
		t.Fatalf("cancel request not durably pending: %+v %v", pending, err)
	}
	step(t, a, job.ID, Running)
	mock.deferCancel = false
	step(t, a, job.ID, Cancelled)
	if mock.exportCalls.Load() != 0 {
		t.Fatal("cancelled job exported work")
	}
}

func TestNonqualifyingAndUncertainResultsStayUnreleased(t *testing.T) {
	for _, spec := range []struct {
		name     string
		coverage bool
		withClip bool
		score    int
		state    State
	}{
		{"no clips after complete analysis", true, false, 0, NoQualifyingClips},
		{"one clip below batch bar", true, true, 840, NoQualifyingClips},
		{"analysis incomplete", false, false, 0, NeedsReview},
	} {
		t.Run(spec.name, func(t *testing.T) {
			dir := t.TempDir()
			store, mock := openTestStore(t, dir), openEffects(t, dir)
			a := NewAdapter(store, mock, mock)
			job := admit(t, a)
			job = step(t, a, job.ID, Running)
			receipt := *job.OwnerReceipt
			receipt.Sequence++
			receipt.Status, receipt.AnalysisComplete = OwnerSucceeded, spec.coverage
			if spec.withClip {
				receipt.Clips = []Clip{fixtureClip(job.Input, 1, spec.score)}
			}
			result, err := a.ApplyReceipt(t.Context(), job.ID, receipt)
			if err != nil || result.State != spec.state || mock.exportCalls.Load() != 0 {
				t.Fatalf("got %s (%v), want %s and zero exports", result.State, err, spec.state)
			}
		})
	}
}

func TestCompletionRechecksPreviouslyVerifiedExport(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	store, mock := openTestStore(t, dir), openEffects(t, dir)
	a := NewAdapter(store, mock, mock)
	job := admit(t, a)
	job = step(t, a, job.ID, Running)
	receipt := *job.OwnerReceipt
	receipt.Sequence++
	receipt.Status, receipt.AnalysisComplete = OwnerSucceeded, true
	receipt.Clips = []Clip{fixtureClip(job.Input, 1, 900), fixtureClip(job.Input, 2, 800)}
	if _, err := a.ApplyReceipt(ctx, job.ID, receipt); err != nil {
		t.Fatal(err)
	}
	job = step(t, a, job.ID, Exporting)
	if len(job.Exports) != 1 {
		t.Fatal("expected one verified export")
	}
	if _, err := mock.db.ExecContext(ctx, "DELETE FROM exports WHERE key=?", job.Exports[0].Key); err != nil {
		t.Fatal(err)
	}
	job = step(t, a, job.ID, NeedsReview)
	if job.Reason != exportMismatch {
		t.Fatalf("deleted export did not block release: %+v", job)
	}
}

func TestTamperedManifestDoesNotLoad(t *testing.T) {
	dir := t.TempDir()
	store, mock := openTestStore(t, dir), openEffects(t, dir)
	job := admit(t, NewAdapter(store, mock, mock))
	record, err := store.runs.Load(t.Context(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	ref := record.State.RoleStates[ownerRole].Artifacts[0]
	if err := os.WriteFile(filepath.Join(store.snapshotDir, ref.URI), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Status(t.Context(), job.ID); !errors.Is(err, ErrContract) {
		t.Fatalf("tampered manifest was not rejected: %v", err)
	}
}
