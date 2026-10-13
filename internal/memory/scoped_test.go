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
	captures int
}

func (p *scopedPrimary) Capture(_ context.Context, mem Memory) (string, error) {
	p.captures++
	if p.err != nil {
		return "", p.err
	}
	p.memories = append(p.memories, mem)
	return "recall-" + mem.ID, nil
}

func TestScopedCapturePersistsPendingSyncAndRetriesWithoutDuplicateLocalMemory(t *testing.T) {
	local := tempStore(t)
	primary := &idempotentPrimary{err: errors.New("offline"), deliveries: map[string]string{}}
	events := &scopedEvents{}
	f := &Facade{Local: local, Primary: primary, Events: events, Source: "test"}
	req := CaptureRequest{Scope: scopedTestScope(), CaptureKey: "sync-retry", Content: "durable sync candidate"}
	first, fallback, err := f.Capture(context.Background(), req)
	if err != nil || !fallback {
		t.Fatalf("failed primary capture = %#v fallback=%v err=%v", first, fallback, err)
	}
	foundPending := false
	for _, e := range events.events {
		if e.Type == EventScopedCaptureSyncPending && e.Payload["memory_id"] == first.ID && e.Payload["sync_key"] == first.ID {
			foundPending = true
		}
	}
	if !foundPending {
		t.Fatalf("pending sync event missing: %#v", events.events)
	}
	primary.err = nil
	second, fallback, err := f.Capture(context.Background(), req)
	if err != nil || fallback || second.ID != first.ID || primary.remoteCalls != 1 {
		t.Fatalf("idempotent sync retry = %#v fallback=%v remoteCalls=%d err=%v", second, fallback, primary.remoteCalls, err)
	}
	all, err := local.ListRecent(context.Background(), 0)
	if err != nil || len(all) != 1 {
		t.Fatalf("retry duplicated local memory: %#v err=%v", all, err)
	}
}

func TestScopedCapturePersistsPendingBeforePrimaryDelivery(t *testing.T) {
	local := tempStore(t)
	primary := &idempotentPrimary{deliveries: map[string]string{}}
	events := &scopedEvents{failType: EventScopedCaptureSyncPending, failNext: 1}
	f := &Facade{Local: local, Primary: primary, Events: events, Source: "test"}
	_, _, err := f.Capture(context.Background(), CaptureRequest{Scope: scopedTestScope(), CaptureKey: "pending-first", Content: "must not reach primary"})
	if err == nil {
		t.Fatal("expected pending-intent persistence failure")
	}
	if primary.remoteCalls != 0 {
		t.Fatalf("remote delivery occurred before durable pending intent: %d", primary.remoteCalls)
	}
}
func (p *scopedPrimary) Search(_ context.Context, _ SearchRequest) ([]Memory, error) {
	if p.err != nil {
		return nil, p.err
	}
	return append([]Memory(nil), p.memories...), nil
}

type scopedEvents struct {
	events   []event.Event
	failType string
	failNext int
}

func (s *scopedEvents) Store(_ context.Context, e event.Event) error {
	s.events = append(s.events, e)
	if s.failType == e.Type && s.failNext > 0 {
		s.failNext--
		return errors.New("event store unavailable")
	}
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

func TestScopedCaptureRejectsConflictingDeliveryKeyUnlessItSupersedes(t *testing.T) {
	local := tempStore(t)
	f := &Facade{Local: local}
	first, _, err := f.Capture(context.Background(), CaptureRequest{Scope: scopedTestScope(), CaptureKey: "delivery", Content: "first", Category: "decision"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.Capture(context.Background(), CaptureRequest{Scope: scopedTestScope(), CaptureKey: "delivery", Content: "changed", Category: "decision"}); err == nil {
		t.Fatal("conflicting capture key was accepted")
	}
	if _, _, err := f.Capture(context.Background(), CaptureRequest{Scope: scopedTestScope(), CaptureKey: "delivery", Content: "first", Category: "decision", Source: "changed-metadata"}); err == nil {
		t.Fatal("capture key with changed metadata was accepted")
	}
	replacement, _, err := f.Capture(context.Background(), CaptureRequest{Scope: scopedTestScope(), CaptureKey: "delivery", Content: "changed", Category: "decision", SupersedesID: first.ID})
	if err != nil || replacement.ID == first.ID {
		t.Fatalf("replacement = %#v, %v", replacement, err)
	}
	duplicate, _, err := f.Capture(context.Background(), CaptureRequest{Scope: scopedTestScope(), CaptureKey: "delivery", Content: "changed", Category: "decision", SupersedesID: first.ID})
	if err != nil || duplicate.ID != replacement.ID {
		t.Fatalf("replacement retry = %#v, %v", duplicate, err)
	}
}

func TestScopedCaptureSurfacesEventFailureAndRetryIsIdempotent(t *testing.T) {
	local := tempStore(t)
	events := &scopedEvents{failType: EventScopedCapturePersisted, failNext: 1}
	f := &Facade{Local: local, Events: events, Source: "test"}
	req := CaptureRequest{Scope: scopedTestScope(), CaptureKey: "event-retry", Content: "durable before event"}
	if _, _, err := f.Capture(context.Background(), req); err == nil {
		t.Fatal("expected lifecycle event persistence failure")
	}
	all, err := local.ListRecent(context.Background(), 0)
	if err != nil || len(all) != 1 {
		t.Fatalf("local record after failed event = %d, %v", len(all), err)
	}
	got, _, err := f.Capture(context.Background(), req)
	if err != nil || got.ID != all[0].ID {
		t.Fatalf("idempotent retry = %#v, %v", got, err)
	}
	all, err = local.ListRecent(context.Background(), 0)
	if err != nil || len(all) != 1 {
		t.Fatalf("retry duplicated memory: %d, %v", len(all), err)
	}
}

func TestScopedSearchSurfacesEventFailure(t *testing.T) {
	f := &Facade{Local: tempStore(t), Events: &scopedEvents{failType: EventScopedSearchRequested, failNext: 1}}
	if _, _, err := f.Search(context.Background(), SearchRequest{Scope: scopedTestScope(), Query: "anything"}); err == nil {
		t.Fatal("expected search lifecycle event persistence failure")
	}
}

func TestScopedPrimaryOutageThenRecoveryKeepsLocalRecordWithoutClaimingReconciliation(t *testing.T) {
	local := tempStore(t)
	primary := &scopedPrimary{err: errors.New("offline")}
	f := &Facade{Local: local, Primary: primary}
	stored, fallback, err := f.Capture(context.Background(), CaptureRequest{Scope: scopedTestScope(), CaptureKey: "outage", Content: "recoverable local record"})
	if err != nil || !fallback {
		t.Fatalf("outage capture = %#v fallback=%v err=%v", stored, fallback, err)
	}
	primary.err = nil
	results, fallback, err := f.Search(context.Background(), SearchRequest{Scope: scopedTestScope(), Query: "recoverable", Limit: 5})
	if err != nil || fallback || len(results) != 1 || results[0].ID != stored.ID {
		t.Fatalf("recovered search = %#v fallback=%v err=%v", results, fallback, err)
	}
	if len(primary.memories) != 0 {
		t.Fatalf("unexpected reconciliation: %#v", primary.memories)
	}
}

// TestScopedMemoryScoreGate is the deterministic R2 retrieval gate. It seeds
// a shared local store with allowed, denied, superseded, and remote candidates
// and requires every expected result without a cross-scope leak.
func TestScopedMemoryScoreGate(t *testing.T) {
	local := tempStore(t)
	base := scopedTestScope()
	seed := []Memory{
		{ID: "project", Content: "project-alpha architecture", ProjectID: "project-a", TaskID: "task-a"},
		{ID: "task", Content: "task-alpha decision", ProjectID: "project-a", TaskID: "task-a"},
		{ID: "user", Content: "user-alpha preference", ProjectID: "project-a", TaskID: "task-a", UserID: "user-a"},
		{ID: "other-user", Content: "other-user secret", ProjectID: "project-a", TaskID: "task-a", UserID: "user-b"},
		{ID: "other-project", Content: "other-project secret", ProjectID: "project-b", TaskID: "task-a"},
		{ID: "other-task", Content: "other-task secret", ProjectID: "project-a", TaskID: "task-b"},
		{ID: "old", Content: "superseded-alpha old", ProjectID: "project-a", TaskID: "task-a"},
		{ID: "new", Content: "superseded-alpha new", ProjectID: "project-a", TaskID: "task-a", SupersedesID: "old"},
	}
	for _, mem := range seed {
		mem.Summary = mem.Content
		if _, err := local.Store(context.Background(), mem); err != nil {
			t.Fatal(err)
		}
	}
	primary := &scopedPrimary{memories: []Memory{{ID: "remote", Content: "remote-alpha fact", ProjectID: "project-a", TaskID: "task-a"}, {ID: "remote-leak", Content: "remote-leak secret", ProjectID: "project-b", TaskID: "task-a"}}}
	f := &Facade{Local: local, Primary: primary}
	type scoreCase struct {
		name, query, want string
		scope             Scope
		absent            string
		outage            bool
	}
	noUser := base
	noUser.UserID = ""
	noUser.IncludeUserScope = false
	cases := []scoreCase{
		{"project", "architecture", "project", base, "", false},
		{"task", "decision", "task", base, "", false},
		{"user opt in", "preference", "user", base, "", false},
		{"user denied", "preference", "", noUser, "user", false},
		{"other user denied", "other-user", "", base, "other-user", false},
		{"other project denied", "other-project", "", base, "other-project", false},
		{"other task denied", "other-task", "", base, "other-task", false},
		{"supersession", "superseded-alpha", "new", base, "old", false},
		{"primary allowed", "remote-alpha", "remote", base, "", false},
		{"primary leak denied", "remote-leak", "", base, "remote-leak", false},
	}
	passed := 0
	for _, tc := range cases {
		results, _, err := f.Search(context.Background(), SearchRequest{Scope: tc.scope, Query: tc.query, Limit: 10})
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		foundWant, foundAbsent := tc.want == "", false
		for _, result := range results {
			if result.ID == tc.want {
				foundWant = true
			}
			if result.ID == tc.absent {
				foundAbsent = true
			}
		}
		if foundWant && !foundAbsent {
			passed++
		} else {
			t.Errorf("%s: want=%q absent=%q results=%#v", tc.name, tc.want, tc.absent, results)
		}
	}
	t.Logf("R2 scoped retrieval score: %d/%d; leakage=%t", passed, len(cases), passed != len(cases))
	if passed < 9 {
		t.Fatalf("R2 score gate failed: %d/%d", passed, len(cases))
	}
}
