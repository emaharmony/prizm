package creatorvod

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/emaharmony/prizm/internal/sqlite"
)

// Demo owns the plugin's isolated Stage 1 state and synthetic execution.
type Demo struct {
	store   *Store
	effects *demoEffects
	adapter *Adapter
}

func OpenDemo(dir string) (*Demo, error) {
	store, err := OpenStore(dir)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open(sqlite.DriverName, filepath.Join(dir, "demo-effects.db")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		_ = store.Close()
		return nil, err
	}
	effects := &demoEffects{db: db}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS owner_receipts (key TEXT PRIMARY KEY, receipt TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS export_receipts (key TEXT PRIMARY KEY, receipt TEXT NOT NULL);`); err != nil {
		_ = db.Close()
		_ = store.Close()
		return nil, err
	}
	return &Demo{store: store, effects: effects, adapter: NewAdapter(store, effects, effects)}, nil
}

func (d *Demo) Close() error { return errors.Join(d.store.Close(), d.effects.db.Close()) }

func (d *Demo) Run(ctx context.Context, generation string) (Job, error) {
	if generation == "" {
		generation = "demo"
	}
	source := digest([]byte("synthetic-pentakewl-vod"))
	job, err := d.adapter.Admit(ctx, Ready{
		Version: Version, Profile: "pentakewl", SourceRef: "synthetic-vod", Name: "demo.ready.mp4",
		SourceHash: source, ExecutionHash: source, Generation: generation, WorkflowVersion: "stage1",
		PolicyHash: digest([]byte("stage1-demo-policy")), Owner: "lumi",
		SizeBefore: 4096, SizeAfter: 4096, Stable: true, ProbeOK: true, DurationMS: 60_000,
	})
	for range 4 {
		if err != nil || job.terminal() {
			return job, err
		}
		previous := job.Revision
		job, err = d.adapter.Advance(ctx, job.ID)
		if job.Revision == previous {
			break
		}
	}
	return job, err
}

func (d *Demo) Status(ctx context.Context, id string) (Job, error) {
	sum, ok := strings.CutPrefix(id, "vod_")
	if !ok || !validDigest(sum) {
		return Job{}, fmt.Errorf("%w: invalid job ID", ErrContract)
	}
	return d.store.Status(ctx, id)
}

type demoEffects struct {
	db *sql.DB
}

func (e *demoEffects) Submit(ctx context.Context, job Job) (OwnerReceipt, error) {
	score := 850
	hash := digest([]byte("synthetic-render-" + job.ID))
	gates := make(map[string]Gate, len(mockCriticalGates))
	for _, code := range mockCriticalGates {
		gates[code] = Pass
	}
	receipt := OwnerReceipt{
		Version: Version, ExecutionKey: job.ExecutionKey, DispatchRevision: job.DispatchRevision,
		RemoteID: "demo-" + job.ID, Sequence: 1, Status: OwnerSucceeded, AnalysisComplete: true,
		Clips: []Clip{{
			ID: "demo-clip", StartMS: 10_000, EndMS: 30_000, SourceHash: job.Input.SourceHash,
			PolicyHash: job.Input.PolicyHash, ArtifactRef: "synthetic-render", RenderHash: hash,
			VerdictID: "synthetic-verdict", QAHash: hash, ScoreHundredths: &score,
			Gates: gates, QAEvidence: []string{"synthetic-frame"},
		}},
	}
	data, err := json.Marshal(receipt)
	if err != nil {
		return OwnerReceipt{}, err
	}
	if _, err := e.db.ExecContext(ctx, "INSERT OR IGNORE INTO owner_receipts VALUES (?, ?)", job.ExecutionKey, data); err != nil {
		return OwnerReceipt{}, err
	}
	saved, err := e.Status(ctx, job.ExecutionKey)
	if err != nil {
		return OwnerReceipt{}, err
	}
	if saved == nil {
		return OwnerReceipt{}, errors.New("synthetic owner receipt disappeared")
	}
	return *saved, nil
}

func (e *demoEffects) Status(ctx context.Context, key string) (*OwnerReceipt, error) {
	return readReceipt[OwnerReceipt](e.db.QueryRowContext(ctx, "SELECT receipt FROM owner_receipts WHERE key=?", key))
}

func (e *demoEffects) Cancel(ctx context.Context, key string) (*OwnerReceipt, error) {
	// The synthetic owner finishes atomically on submission.
	return e.Status(ctx, key)
}

func (e *demoEffects) Lookup(ctx context.Context, key string) (*ExportReceipt, error) {
	return readReceipt[ExportReceipt](e.db.QueryRowContext(ctx, "SELECT receipt FROM export_receipts WHERE key=?", key))
}

func (e *demoEffects) Put(ctx context.Context, key string, clip Clip) (ExportReceipt, error) {
	receipt := ExportReceipt{
		Version: Version, Key: key, ArtifactHash: clip.RenderHash,
		VerdictID: clip.VerdictID, DestinationRef: "demo-export-" + key,
	}
	data, err := json.Marshal(receipt)
	if err != nil {
		return ExportReceipt{}, err
	}
	if _, err := e.db.ExecContext(ctx, "INSERT OR IGNORE INTO export_receipts VALUES (?, ?)", key, data); err != nil {
		return ExportReceipt{}, err
	}
	saved, err := e.Lookup(ctx, key)
	if err != nil {
		return ExportReceipt{}, err
	}
	if saved == nil {
		return ExportReceipt{}, errors.New("synthetic export receipt disappeared")
	}
	return *saved, nil
}

func readReceipt[T any](row *sql.Row) (*T, error) {
	var data []byte
	err := row.Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var receipt T
	if err := decodeStrict(data, &receipt); err != nil {
		return nil, err
	}
	return &receipt, nil
}
