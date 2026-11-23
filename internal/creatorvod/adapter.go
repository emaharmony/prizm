package creatorvod

import (
	"context"
	"errors"
	"reflect"

	"github.com/emaharmony/prizm/internal/workflow/multiagent"
)

// Owner is Lumi's logical control surface. Stage 1 supplies a durable fake.
// Lookup of a missing job returns a nil receipt. A transport error is unknown.
// Cancel must be idempotent and use the same execution key.
type Owner interface {
	Submit(context.Context, Job) (OwnerReceipt, error)
	Status(context.Context, string) (*OwnerReceipt, error)
	Cancel(context.Context, string) (*OwnerReceipt, error)
}

// ExportSink must verify the actual destination on Lookup and deduplicate
// Put on its key. Stage 1 supplies a fake sink that stores receipts only.
type ExportSink interface {
	Lookup(context.Context, string) (*ExportReceipt, error)
	Put(context.Context, string, Clip) (ExportReceipt, error)
}

type Adapter struct {
	store *Store
	owner Owner
	sink  ExportSink
}

func NewAdapter(store *Store, owner Owner, sink ExportSink) *Adapter {
	return &Adapter{store: store, owner: owner, sink: sink}
}

func (a *Adapter) Admit(ctx context.Context, input Ready) (Job, error) {
	if err := input.validate(); err != nil {
		return Job{}, err
	}
	if input.Owner != string(ownerRole) {
		return Job{}, ErrContract
	}
	return a.store.accept(ctx, input)
}

// Advance performs one step. On reboot, callers resume from the stored job
// ID. A submission intent is committed before making any worker call.
func (a *Adapter) Advance(ctx context.Context, id string) (Job, error) {
	job, err := a.store.Status(ctx, id)
	if err != nil || job.terminal() {
		return job, err
	}
	switch job.State {
	case Accepted:
		job.State = Dispatching
		job.DispatchRevision = job.Revision + 1
		job.ExecutionKey = job.ID + "-attempt-1"
		claimed, err := a.store.checkpoint(ctx, job, "process.requested", "command")
		if errors.Is(err, multiagent.ErrRevisionConflict) {
			return a.store.Status(ctx, id)
		}
		if err != nil {
			return Job{}, err
		}
		receipt, err := a.owner.Submit(ctx, claimed)
		if err != nil {
			return a.unconfirmed(ctx, id)
		}
		return a.ApplyReceipt(ctx, id, receipt)
	case Dispatching, Running, NeedsReview:
		return a.observe(ctx, job)
	case Exporting:
		return a.exportNext(ctx, job)
	default:
		return Job{}, ErrContract
	}
}

func (a *Adapter) observe(ctx context.Context, job Job) (Job, error) {
	receipt, err := a.owner.Status(ctx, job.ExecutionKey)
	if err != nil {
		// Keep previously known progress during a transient status outage.
		if job.State == Running {
			return job, err
		}
		return a.unconfirmed(ctx, job.ID)
	}
	if receipt == nil {
		if job.State == Running {
			return job, errors.New("creator VOD owner lost a previously confirmed execution")
		}
		return a.unconfirmed(ctx, job.ID)
	}
	current, err := a.ApplyReceipt(ctx, job.ID, *receipt)
	if err != nil || current.State != Running || !current.CancelRequested {
		return current, err
	}
	cancelled, err := a.owner.Cancel(ctx, current.ExecutionKey)
	if err != nil || cancelled == nil {
		// A cancel request is durable, but the owner has yet to confirm it.
		return current, err
	}
	return a.ApplyReceipt(ctx, current.ID, *cancelled)
}

func (a *Adapter) unconfirmed(ctx context.Context, id string) (Job, error) {
	current, err := a.store.Status(ctx, id)
	if err != nil || current.State != Dispatching {
		return current, err
	}
	current.State, current.Reason = NeedsReview, submissionUnconfirmed
	return a.saveOrReload(ctx, current, "needs_review", "fact")
}

// ApplyReceipt accepts polled or externally delivered typed status snapshots.
// The last sequence and dispatch identity are persisted across restarts.
func (a *Adapter) ApplyReceipt(ctx context.Context, id string, receipt OwnerReceipt) (Job, error) {
	for attempt := 0; attempt < 5; attempt++ {
		job, err := a.store.Status(ctx, id)
		if err != nil {
			return Job{}, err
		}
		if job.ExecutionKey == "" {
			return job, ErrContract
		}
		if err := receipt.validate(job); err != nil {
			return job, err
		}
		if prior := job.OwnerReceipt; prior != nil {
			if receipt.RemoteID != prior.RemoteID {
				return job, ErrContract
			}
			if receipt.Sequence < prior.Sequence {
				return job, nil
			}
			if receipt.Sequence == prior.Sequence {
				if !reflect.DeepEqual(receipt, *prior) {
					return job, ErrContract
				}
				return job, nil
			}
		}
		if job.terminal() || job.State == Exporting {
			return job, nil
		}

		job.OwnerReceipt = &receipt
		job.Reason = ""
		switch receipt.Status {
		case OwnerRunning:
			job.State = Running
		case OwnerFailed:
			job.State, job.Reason = Failed, ownerFailed
		case OwnerCancelled:
			job.State = Cancelled
		case OwnerSucceeded:
			if job.CancelRequested {
				job.State, job.Reason = NeedsReview, "CANCEL_COMPLETED_RACE"
				break
			}
			selected, uncertain := qualify(receipt)
			switch {
			case len(selected) > 0:
				job.State, job.Selected = Exporting, selected
			case uncertain:
				job.State, job.Reason = NeedsReview, qaUnverified
			default:
				job.State = NoQualifyingClips
			}
		}
		topic := string(job.State)
		if job.State == Running {
			topic = "processing"
		}
		// Commit the owner result and QA disposition together. Subsequent
		// export starts only from this checkpoint.
		saved, err := a.store.checkpoint(ctx, job, topic, "fact")
		if !errors.Is(err, multiagent.ErrRevisionConflict) {
			return saved, err
		}
	}
	return Job{}, multiagent.ErrRevisionConflict
}

// RequestCancel is the operator-side cancellation entry point for the mock.
// An already accepted but undispatched job needs no worker acknowledgment.
func (a *Adapter) RequestCancel(ctx context.Context, id string) (Job, error) {
	job, err := a.store.Status(ctx, id)
	if err != nil || job.terminal() || job.CancelRequested {
		return job, err
	}
	if job.State == Exporting {
		return job, errors.New("creator VOD release already started")
	}
	job.CancelRequested = true
	if job.State == Accepted {
		job.State = Cancelled
		return a.store.checkpoint(ctx, job, "cancelled", "fact")
	}
	return a.store.checkpoint(ctx, job, "cancel.requested", "command")
}

func (a *Adapter) exportNext(ctx context.Context, job Job) (Job, error) {
	if len(job.Selected) == 0 || len(job.Exports) >= len(job.Selected) {
		return job, ErrContract
	}
	clip := job.Selected[len(job.Exports)]
	key := digest([]byte(job.ID + "\x00" + clip.ID + "\x00" + clip.RenderHash))
	receipt, err := a.sink.Lookup(ctx, key)
	if err != nil {
		return job, err
	}
	if receipt == nil {
		_, err := a.sink.Put(ctx, key, clip)
		if err != nil {
			return job, err
		}
		// Verify at the destination even when Put reports success.
		receipt, err = a.sink.Lookup(ctx, key)
		if err != nil {
			return job, err
		}
	}
	if receipt == nil || !receipt.matches(key, clip) {
		job.State, job.Reason = NeedsReview, exportMismatch
		return a.saveOrReload(ctx, job, "needs_review", "fact")
	}
	for _, previous := range job.Exports {
		if previous.DestinationRef == receipt.DestinationRef {
			job.State, job.Reason = NeedsReview, exportMismatch
			return a.saveOrReload(ctx, job, "needs_review", "fact")
		}
	}
	if len(job.Exports)+1 == len(job.Selected) {
		for i, prior := range job.Exports {
			verified, err := a.sink.Lookup(ctx, prior.Key)
			if err != nil {
				return job, err
			}
			if verified == nil || *verified != prior || !verified.matches(prior.Key, job.Selected[i]) {
				job.State, job.Reason = NeedsReview, exportMismatch
				return a.saveOrReload(ctx, job, "needs_review", "fact")
			}
		}
		job.Exports = append(job.Exports, *receipt)
		job.State = Completed
		return a.saveOrReload(ctx, job, "completed", "fact")
	}
	job.Exports = append(job.Exports, *receipt)
	return a.saveOrReload(ctx, job, "export.verified", "fact")
}

func (a *Adapter) saveOrReload(ctx context.Context, job Job, topic, kind string) (Job, error) {
	saved, err := a.store.checkpoint(ctx, job, topic, kind)
	if errors.Is(err, multiagent.ErrRevisionConflict) {
		return a.store.Status(ctx, job.ID)
	}
	return saved, err
}
