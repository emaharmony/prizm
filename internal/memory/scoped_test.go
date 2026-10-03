package memory

import (
	"context"
	"errors"
	"testing"

	"github.com/emaharmony/prizm/internal/event"
)

type scopedPrimary struct {
	memories []Memory
	err      error
}

func (p *scopedPrimary) Capture(_ context.Context, mem Memory) (string, error) {
	if p.err != nil {
		return "", p.err
	}
	p.memories = append(p.memories, mem)
	return "recall-" + mem.ID, nil
}
func (p *scopedPrimary) Search(_ context.Context, _ SearchRequest) ([]Memory, error) {
	if p.err != nil {
		return nil, p.err
	}
	return append([]Memory(nil), p.memories...), nil
}

type scopedEvents struct{ events []event.Event }

func (s *scopedEvents) Store(_ context.Context, e event.Event) error {
	s.events = append(s.events, e)
	return nil
}
func (s *scopedEvents) StoreBatch(_ context.Context, events []event.Event) error {
	s.events = append(s.events, events...)
	return nil
}
func (s *scopedEvents) Query(context.Context, event.EventFilter) ([]event.Event, error) {
	return append([]event.Event(nil), s.events...), nil
}
func (s *scopedEvents) Close() error { return nil }

func scopedTestScope() Scope {
	return Scope{ProjectID: "project-a", TaskID: "task-a", UserID: "user-a", IncludeUserScope: true, CorrelationID: "corr-a"}
}

func TestScopedCaptureIsIdempotentAndCorrelated(t *testing.T) {
	local := tempStore(t)
	events := &scopedEvents{}
	f := &Facade{Local: local, Events: events, Source: "test"}
	req := CaptureRequest{Scope: scopedTestScope(), Content: "Use SQLite for task state", CaptureKey: "delivery-1"}
	first, fallback, err := f.Capture(context.Background(), req)
	if err != nil || fallback {
		t.Fatalf("first capture = (%v, %v)", err, fallback)
	}
	second, _, err := f.Capture(context.Background(), req)
	if err != nil || first.ID != second.ID {
		t.Fatalf("duplicate capture = %#v, %v", second, err)
	}
	all, err := local.ListRecent(context.Background(), 0)
	if err != nil || len(all) != 1 {
		t.Fatalf("local captures = %d, %v", len(all), err)
	}
	if len(events.events) < 2 || events.events[0].CorrelationID != "corr-a" {
		t.Fatalf("events missing correlation: %#v", events.events)
	}
	for _, e := range events.events {
		if err := event.Validate(e); err != nil {
			t.Fatalf("invalid event %s: %v", e.Type, err)
		}
	}
}

func TestScopedSearchRejectsCrossProjectAndUserLeakage(t *testing.T) {
	local := tempStore(t)
	for _, mem := range []Memory{
		{ID: "same", Content: "visible task decision", Summary: "visible", ProjectID: "project-a", TaskID: "task-a", UserID: "user-a"},
		{ID: "other-project", Content: "visible task decision", Summary: "other project", ProjectID: "project-b", TaskID: "task-a", UserID: "user-a"},
		{ID: "other-task", Content: "visible task decision", Summary: "other task", ProjectID: "project-a", TaskID: "task-b", UserID: "user-a"},
		{ID: "other-user", Content: "visible task decision", Summary: "other user", ProjectID: "project-a", TaskID: "task-a", UserID: "user-b"},
	} {
		if _, err := local.Store(context.Background(), mem); err != nil {
			t.Fatal(err)
		}
	}
	f := &Facade{Local: local}
	results, _, err := f.Search(context.Background(), SearchRequest{Scope: scopedTestScope(), Query: "visible", Limit: 10})
	if err != nil || len(results) != 1 || results[0].ID != "same" {
		t.Fatalf("results = %#v, err=%v", results, err)
	}
	noUser := scopedTestScope()
	noUser.UserID = ""
	noUser.IncludeUserScope = false
	results, _, err = f.Search(context.Background(), SearchRequest{Scope: noUser, Query: "visible", Limit: 10})
	if err != nil || len(results) != 0 {
		t.Fatalf("non-user scoped results = %#v, err=%v", results, err)
	}
}

func TestScopedCaptureSupersedesAndPrimaryOutageFallsBack(t *testing.T) {
	local := tempStore(t)
	primary := &scopedPrimary{err: errors.New("offline")}
	f := &Facade{Local: local, Primary: primary}
	first, fallback, err := f.Capture(context.Background(), CaptureRequest{Scope: scopedTestScope(), Content: "old decision"})
	if err != nil || !fallback {
		t.Fatalf("fallback capture = %v, %v", fallback, err)
	}
	second, fallback, err := f.Capture(context.Background(), CaptureRequest{Scope: scopedTestScope(), Content: "new decision", SupersedesID: first.ID})
	if err != nil || !fallback {
		t.Fatalf("replacement = %#v, %v, %v", second, fallback, err)
	}
	results, fallback, err := f.Search(context.Background(), SearchRequest{Scope: scopedTestScope(), Query: "decision", Limit: 10})
	if err != nil || !fallback || len(results) != 1 || results[0].ID != second.ID {
		t.Fatalf("search = %#v fallback=%v err=%v", results, fallback, err)
	}
}

func TestScopedSearchMergesAndDeduplicatesPrimaryResults(t *testing.T) {
	local := tempStore(t)
	localMem := Memory{ID: "shared", Content: "same scoped fact", Summary: "same", ProjectID: "project-a", TaskID: "task-a", UserID: "user-a"}
	if _, err := local.Store(context.Background(), localMem); err != nil {
		t.Fatal(err)
	}
	primary := &scopedPrimary{memories: []Memory{
		localMem,
		{ID: "remote", Content: "remote scoped fact", Summary: "remote", ProjectID: "project-a", TaskID: "task-a", UserID: "user-a"},
		{ID: "leak", Content: "leaked fact", Summary: "leak", ProjectID: "project-b", TaskID: "task-a", UserID: "user-a"},
	}}
	results, fallback, err := (&Facade{Local: local, Primary: primary}).Search(context.Background(), SearchRequest{Scope: scopedTestScope(), Query: "fact", Limit: 10})
	if err != nil || fallback || len(results) != 2 {
		t.Fatalf("results = %#v fallback=%v err=%v", results, fallback, err)
	}
	for _, result := range results {
		if result.ID == "leak" {
			t.Fatal("cross-project primary result leaked")
		}
	}
}

func TestScopedScopeRequiresExplicitUserOptIn(t *testing.T) {
	f := &Facade{Local: tempStore(t)}
	_, _, err := f.Search(context.Background(), SearchRequest{Scope: Scope{ProjectID: "p", TaskID: "t", UserID: "u"}, Query: "x"})
	if err == nil {
		t.Fatal("expected explicit user scope error")
	}
}

func TestScopedCaptureKeyIsNamespacedByScope(t *testing.T) {
	local := tempStore(t)
	f := &Facade{Local: local}
	first, _, err := f.Capture(context.Background(), CaptureRequest{Scope: scopedTestScope(), CaptureKey: "provider-delivery-1", Content: "project a"})
	if err != nil {
		t.Fatal(err)
	}
	other := scopedTestScope()
	other.ProjectID = "project-b"
	second, _, err := f.Capture(context.Background(), CaptureRequest{Scope: other, CaptureKey: "provider-delivery-1", Content: "project b"})
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == second.ID {
		t.Fatalf("capture key collided across scopes: %s", first.ID)
	}
}

func TestScopedOperationsRequireCorrelationID(t *testing.T) {
	scope := scopedTestScope()
	scope.CorrelationID = ""
	_, _, err := (&Facade{Local: tempStore(t)}).Search(context.Background(), SearchRequest{Scope: scope, Query: "x"})
	if err == nil {
		t.Fatal("expected missing correlation rejection")
	}
}
