package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/emaharmony/prizm/internal/event"
	"github.com/emaharmony/prizm/internal/workflow/multiagent"
)

func TestFreewillDiaryAnnotationsAppendAndReopenExactOwnerScope(t *testing.T) {
	ctx := context.Background()
	dataDir, workspace := t.TempDir(), t.TempDir()
	events, err := event.NewSQLiteEventStore(filepath.Join(dataDir, "events.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer events.Close()
	const project, agent, intention, parent, owner = "project-a", "agent-a", "intention-a", "parent-a", "owner-a"
	factSink, err := newFreewillTerminalFactMemorySink(dataDir, project, workspace, events)
	if err != nil {
		t.Fatal(err)
	}
	fact := multiagent.FreewillTerminalFact{ProjectID: project, Workspace: workspace, AgentID: agent, IntentionID: intention, ParentRunID: parent,
		ChildRunID: "child-a", Status: "completed", ReportRef: "multiagent_report.json", ReportSHA256: strings.Repeat("a", 64), ValidationSummary: "passed",
		RecordedAt: time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)}
	if err := factSink.CaptureFreewillTerminalFact(ctx, fact); err != nil {
		t.Fatal(err)
	}
	diarySink, err := newFreewillTerminalDiarySink(factSink, &freewillRetrospectiveFake{narrative: "Original immutable diary narrative", provider: "local-fake", model: "fake", version: "v1"}, owner)
	if err != nil {
		t.Fatal(err)
	}
	if err := diarySink.CaptureFreewillTerminalRetrospective(ctx, fact); err != nil {
		t.Fatal(err)
	}
	diaryEntries, err := diarySink.ReadOwnerEntries(ctx, owner, project, workspace, intention, agent, parent)
	if err != nil || len(diaryEntries) != 1 {
		t.Fatalf("source diary entries=%d err=%v", len(diaryEntries), err)
	}
	entryID := diaryEntries[0].ID
	var sourceRetrospective freewillDiaryRetrospective
	if err := json.Unmarshal([]byte(diaryEntries[0].Content), &sourceRetrospective); err != nil {
		t.Fatal(err)
	}
	hostFactBefore, err := factSink.store.Local.Get(ctx, sourceRetrospective.EvidenceFactID)
	if err != nil || hostFactBefore == nil {
		t.Fatalf("source host fact unavailable before annotation: %v", err)
	}
	annotationSink, err := newFreewillDiaryAnnotationSink(factSink, owner, project, workspace, agent)
	if err != nil {
		t.Fatal(err)
	}
	first, err := annotationSink.AppendDiaryAnnotation(ctx, owner, project, workspace, agent, intention, parent, entryID, 1, "First owner note")
	if err != nil || first.Revision != 2 || len(first.Annotations) != 1 || first.Annotations[0].Text != "First owner note" || first.OriginalText != "Original immutable diary narrative" {
		t.Fatalf("first annotation=%#v err=%v", first, err)
	}
	replay, err := annotationSink.AppendDiaryAnnotation(ctx, owner, project, workspace, agent, intention, parent, entryID, 1, "First owner note")
	if err != nil || replay.Revision != 2 || len(replay.Annotations) != 1 {
		t.Fatalf("exact replay=%#v err=%v", replay, err)
	}
	if _, err := annotationSink.AppendDiaryAnnotation(ctx, owner, project, workspace, agent, intention, parent, entryID, 1, "Different replay payload"); err == nil {
		t.Fatal("same expected revision accepted a different payload")
	}
	second, err := annotationSink.AppendDiaryAnnotation(ctx, owner, project, workspace, agent, intention, parent, entryID, 2, "Second owner note")
	if err != nil || second.Revision != 3 || len(second.Annotations) != 2 || second.Annotations[1].Previous != 2 {
		t.Fatalf("second annotation=%#v err=%v", second, err)
	}
	if _, err := annotationSink.AppendDiaryAnnotation(ctx, "other-owner", project, workspace, agent, intention, parent, entryID, 3, "wrong owner"); err == nil {
		t.Fatal("wrong owner was accepted")
	}
	if _, err := annotationSink.AppendDiaryAnnotation(ctx, owner, "other-project", workspace, agent, intention, parent, entryID, 3, "wrong project"); err == nil {
		t.Fatal("wrong project was accepted")
	}
	if _, err := annotationSink.AppendDiaryAnnotation(ctx, owner, project, workspace, "other-agent", intention, parent, entryID, 3, "wrong agent"); err == nil {
		t.Fatal("wrong agent was accepted")
	}
	if _, err := annotationSink.AppendDiaryAnnotation(ctx, owner, project, workspace, agent, intention, parent, "unknown-entry", 3, "unknown entry"); err == nil {
		t.Fatal("unknown entry was accepted")
	}
	if _, err := annotationSink.AppendDiaryAnnotation(ctx, owner, project, workspace, agent, intention, parent, entryID, 3, strings.Repeat("x", freewillDiaryAnnotationCap+1)); err == nil {
		t.Fatal("oversized annotation was accepted")
	}
	originalBefore, err := factSink.store.Local.Get(ctx, entryID)
	if err != nil || originalBefore == nil {
		t.Fatalf("original diary unavailable: %v", err)
	}
	var originalPayload freewillDiaryRetrospective
	if err := json.Unmarshal([]byte(originalBefore.Content), &originalPayload); err != nil || originalPayload.Narrative != "Original immutable diary narrative" {
		t.Fatalf("original diary changed: %#v err=%v", originalPayload, err)
	}
	wrongWorkspace := t.TempDir()
	if _, err := annotationSink.AppendDiaryAnnotation(ctx, owner, project, wrongWorkspace, agent, intention, parent, entryID, 3, "wrong workspace"); err == nil {
		t.Fatal("wrong workspace was accepted")
	}
	if _, err := annotationSink.AppendDiaryAnnotation(ctx, owner, project, workspace, agent, "other-intention", parent, entryID, 3, "wrong intention"); err == nil {
		t.Fatal("wrong intention was accepted")
	}
	if _, err := annotationSink.AppendDiaryAnnotation(ctx, owner, project, workspace, agent, intention, "other-parent", entryID, 3, "wrong parent run"); err == nil {
		t.Fatal("wrong parent run was accepted")
	}

	if err := events.Close(); err != nil {
		t.Fatal(err)
	}
	reopenedEvents, err := event.NewSQLiteEventStore(filepath.Join(dataDir, "events.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer reopenedEvents.Close()
	reopenedFacts, err := newFreewillTerminalFactMemorySink(dataDir, project, workspace, reopenedEvents)
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := newFreewillDiaryAnnotationSink(reopenedFacts, owner, project, workspace, agent)
	if err != nil {
		t.Fatal(err)
	}
	read, err := reopened.ReadDiaryAnnotations(ctx, owner, project, workspace, agent, intention, parent, entryID)
	if err != nil || read.Revision != 3 || len(read.Annotations) != 2 || read.Annotations[0].Text != "First owner note" || read.Annotations[1].Text != "Second owner note" || read.OriginalText != "Original immutable diary narrative" {
		t.Fatalf("reopened diary=%#v err=%v", read, err)
	}
	all, err := reopenedFacts.store.Local.ListRecent(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	var annotations, originalDiary, hostFacts int
	for _, item := range all {
		if item.Source == freewillDiaryAnnotationSource {
			annotations++
			if strings.Contains(item.Content, "Original immutable diary narrative") {
				t.Fatal("annotation capture duplicated the original diary text")
			}
		}
		if item.ID == entryID && item.Source == freewillDiarySource {
			originalDiary++
		}
		if item.Source == "prizm:freewill-terminal" && item.Category == "fact" {
			hostFacts++
		}
	}
	if annotations != 2 || originalDiary != 1 || hostFacts != 1 {
		t.Fatalf("durable counts annotations=%d original_diary=%d host_facts=%d", annotations, originalDiary, hostFacts)
	}
	hostFactAfter, err := reopenedFacts.store.Local.Get(ctx, hostFactBefore.ID)
	if err != nil || hostFactAfter == nil || hostFactAfter.Content != hostFactBefore.Content {
		t.Fatalf("host fact changed after annotation: before=%#v after=%#v err=%v", hostFactBefore, hostFactAfter, err)
	}
	if _, err := reopened.ReadDiaryAnnotations(ctx, "other-owner", project, workspace, agent, intention, parent, entryID); err == nil {
		t.Fatal("wrong-owner read was accepted")
	}
	if _, err := reopened.ReadDiaryAnnotations(ctx, owner, project, workspace, agent, intention, parent, "unknown-entry"); err == nil {
		t.Fatal("unknown-entry read was accepted")
	}
	secondFacts, err := newFreewillTerminalFactMemorySink(dataDir, project, workspace, reopenedEvents)
	if err != nil {
		t.Fatal(err)
	}
	secondSink, err := newFreewillDiaryAnnotationSink(secondFacts, owner, project, workspace, agent)
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, item := range []struct {
		sink *freewillDiaryAnnotationSink
		text string
	}{{reopened, "concurrent note A"}, {secondSink, "concurrent note B"}} {
		item := item
		go func() {
			<-start
			_, appendErr := item.sink.AppendDiaryAnnotation(ctx, owner, project, workspace, agent, intention, parent, entryID, 3, item.text)
			results <- appendErr
		}()
	}
	close(start)
	firstErr, secondErr := <-results, <-results
	if (firstErr == nil) == (secondErr == nil) {
		t.Fatalf("competing revisions must have exactly one winner: first=%v second=%v", firstErr, secondErr)
	}
	afterRace, err := reopened.ReadDiaryAnnotations(ctx, owner, project, workspace, agent, intention, parent, entryID)
	if err != nil || afterRace.Revision != 4 || len(afterRace.Annotations) != 3 {
		t.Fatalf("concurrent append result=%#v err=%v", afterRace, err)
	}
}
