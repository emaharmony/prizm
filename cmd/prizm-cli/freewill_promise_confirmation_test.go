package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emaharmony/prizm/internal/event"
	"github.com/emaharmony/prizm/internal/orchestrator"
)

func TestFreewillPromiseConfirmationAppendReplayAndReopen(t *testing.T) {
	ctx := context.Background()
	data, workspace := t.TempDir(), t.TempDir()
	events, err := event.NewSQLiteEventStore(filepath.Join(data, "events.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer events.Close()
	const project, agent, owner, promise = "project-a", "agent-a", "synthetic-owner", "promise-a"
	facts, err := newFreewillTerminalFactMemorySink(data, project, workspace, events)
	if err != nil {
		t.Fatal(err)
	}
	cfg := orchestrator.FreewillPromiseConfirmationConfig{Enabled: true, OwnerID: owner, PromiseID: promise, Scope: "review the requested files", Deadline: "2026-11-12T17:00:00Z"}
	s, err := newFreewillPromiseConfirmationSink(facts, project, workspace, agent, cfg)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := s.ProposePromiseRevision(ctx, owner, project, workspace, agent, promise, "request-1", 1, "review requested files and summarize findings", "2026-11-13T17:00:00Z", "The proposed deadline and scope may change what the user receives.")
	if err != nil {
		t.Fatal(err)
	}
	if pending.Current.Revision != 1 || pending.Current.Deadline != cfg.Deadline || len(pending.History) != 1 || len(pending.Proposals) != 1 || pending.Proposals[0].Status != "pending" {
		t.Fatalf("pending changed current promise: %#v", pending)
	}
	replay, err := s.ProposePromiseRevision(ctx, owner, project, workspace, agent, promise, "request-1", 1, "review requested files and summarize findings", "2026-11-13T17:00:00Z", "The proposed deadline and scope may change what the user receives.")
	if err != nil || len(replay.Proposals) != 1 {
		t.Fatalf("proposal replay=%#v err=%v", replay, err)
	}
	if _, err = s.ProposePromiseRevision(ctx, owner, project, workspace, agent, promise, "request-1", 1, "different scope", "2026-11-13T17:00:00Z", "different impact"); err == nil {
		t.Fatal("conflicting proposal accepted")
	}
	if _, err = s.ReadPromise(ctx, owner, project, workspace, "other-agent", promise); err == nil {
		t.Fatal("wrong scope read accepted")
	}
	rejectedFirst, err := s.DecidePromiseRevision(ctx, owner, project, workspace, agent, promise, pending.Proposals[0].ProposalID, "reject")
	if err != nil || rejectedFirst.Current.Revision != 1 || rejectedFirst.Proposals[0].Status != "reject" {
		t.Fatalf("first rejection changed current promise: %#v err=%v", rejectedFirst, err)
	}
	second, err := s.ProposePromiseRevision(ctx, owner, project, workspace, agent, promise, "request-2", 1, "review requested files and summarize findings", "2026-11-14T17:00:00Z", "A later deadline hypothesis may change the user-facing commitment.")
	if err != nil || len(second.Proposals) != 2 {
		t.Fatalf("distinct proposal after reject=%#v err=%v", second, err)
	}
	third, err := s.ProposePromiseRevision(ctx, owner, project, workspace, agent, promise, "request-3", 1, "review files and provide a summary", "2026-11-15T17:00:00Z", "This is another qualitative impact hypothesis.")
	if err != nil || len(third.Proposals) != 3 {
		t.Fatalf("second pending proposal=%#v err=%v", third, err)
	}
	decided, err := s.DecidePromiseRevision(ctx, owner, project, workspace, agent, promise, second.Proposals[1].ProposalID, "accept")
	if err != nil || decided.Current.Revision != 2 || len(decided.History) != 2 || decided.Proposals[2].Status != "stale" {
		t.Fatalf("accept or stale projection=%#v err=%v", decided, err)
	}
	if _, err = s.DecidePromiseRevision(ctx, owner, project, workspace, agent, promise, second.Proposals[1].ProposalID, "reject"); err == nil {
		t.Fatal("conflicting decision replay accepted")
	}
	if _, err = s.ProposePromiseRevision(ctx, owner, project, workspace, agent, promise, "request-4", 1, "stale scope", "2026-11-14T17:00:00Z", "hypothesis"); err == nil {
		t.Fatal("stale revision accepted")
	}
	reopenedFacts, err := newFreewillTerminalFactMemorySink(data, project, workspace, events)
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := newFreewillPromiseConfirmationSink(reopenedFacts, project, workspace, agent, cfg)
	if err != nil {
		t.Fatal(err)
	}
	after, err := reopened.ReadPromise(ctx, owner, project, workspace, agent, promise)
	if err != nil || after.Current.Revision != 2 || after.Current.Scope != "review requested files and summarize findings" || len(after.Decisions) != 2 || len(after.History) != 2 || after.Proposals[2].Status != "stale" {
		t.Fatalf("reopen=%#v err=%v", after, err)
	}
	if _, err = reopened.ReadPromise(ctx, owner, project, workspace, agent, "other-promise"); err == nil {
		t.Fatal("other promise read accepted")
	}
	fourth, err := reopened.ProposePromiseRevision(ctx, owner, project, workspace, agent, promise, "request-5", 2, "review the fixture and include an explicit handoff", "2026-11-14T17:00:00Z", "This hypothetical change could affect the promised handoff.")
	if err != nil {
		t.Fatal(err)
	}
	rejected, err := reopened.DecidePromiseRevision(ctx, owner, project, workspace, agent, promise, fourth.Proposals[3].ProposalID, "reject")
	if err != nil || rejected.Current.Revision != 2 || len(rejected.History) != 2 || rejected.Proposals[3].Status != "reject" {
		t.Fatalf("rejection changed current promise: %#v err=%v", rejected, err)
	}
	if strings.Contains(after.Current.Scope, "impact") {
		t.Fatal("impact hypothesis overwrote promise scope")
	}
}
