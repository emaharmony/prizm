package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/emaharmony/prizm/internal/event"
	"github.com/emaharmony/prizm/internal/memory"
	"github.com/emaharmony/prizm/internal/workflow/multiagent"
)

func makeFreewillDiaryAnnotationFixture(t *testing.T) (*event.SQLiteEventStore, string, *freewillTerminalFactMemorySink, *freewillDiaryAnnotationSink, string) {
	t.Helper()
	ctx := context.Background()
	dataDir, workspace := t.TempDir(), t.TempDir()
	events, err := event.NewSQLiteEventStore(filepath.Join(dataDir, "events.db"))
	if err != nil {
		t.Fatal(err)
	}
	const project, agent, intention, parent, owner = "project-a", "agent-a", "intention-a", "parent-a", "owner-a"
	facts, err := newFreewillTerminalFactMemorySink(dataDir, project, workspace, events)
	if err != nil {
		t.Fatal(err)
	}
	fact := multiagent.FreewillTerminalFact{ProjectID: project, Workspace: workspace, AgentID: agent, IntentionID: intention, ParentRunID: parent,
		ChildRunID: "child-a", Status: "completed", ReportRef: "multiagent_report.json", ReportSHA256: strings.Repeat("a", 64), ValidationSummary: "passed",
		RecordedAt: time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)}
	if err := facts.CaptureFreewillTerminalFact(ctx, fact); err != nil {
		t.Fatal(err)
	}
	diary, err := newFreewillTerminalDiarySink(facts, &freewillRetrospectiveFake{narrative: "Original immutable diary narrative", provider: "local-fake", model: "fake", version: "v1"}, owner)
	if err != nil {
		t.Fatal(err)
	}
	if err := diary.CaptureFreewillTerminalRetrospective(ctx, fact); err != nil {
		t.Fatal(err)
	}
	entries, err := diary.ReadOwnerEntries(ctx, owner, project, workspace, intention, agent, parent)
	if err != nil || len(entries) != 1 {
		t.Fatalf("source diary entries=%d err=%v", len(entries), err)
	}
	annotations, err := newFreewillDiaryAnnotationSink(facts, owner, project, workspace, agent)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = events.Close() })
	return events, dataDir, facts, annotations, entries[0].ID
}

func TestFreewillDiaryAnnotationsRejectMalformedForkedAndGappedLineage(t *testing.T) {
	for _, mode := range []string{"malformed", "forked", "gapped"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			_, _, facts, sink, entryID := makeFreewillDiaryAnnotationFixture(t)
			scope, err := facts.scopeFor("project-a", facts.workspace, "intention-a", "agent-a", "parent-a")
			if err != nil {
				t.Fatal(err)
			}
			scope.UserID, scope.IncludeUserScope = "owner-a", true
			capture := func(content string, key string) {
				t.Helper()
				_, _, captureErr := facts.store.Capture(ctx, memory.CaptureRequest{Scope: scope, Content: content,
					Summary: "test annotation", Category: "diary_annotation", Tier: "persist", Source: freewillDiaryAnnotationSource,
					CaptureKey: key})
				if captureErr != nil {
					t.Fatal(captureErr)
				}
			}
			if mode == "malformed" {
				capture(`{"entry_id":"`+entryID+`"`, "malformed")
			} else if mode == "forked" {
				for index := 0; index < 2; index++ {
					text := fmt.Sprintf("fork-%d", index)
					record := freewillDiaryAnnotationRecord{Version: 1, Kind: "owner_annotation", EntryID: entryID, EntryRevision: 2, PreviousRevision: 1,
						OwnerID: "owner-a", ProjectID: "project-a", WorkspaceID: facts.workspaceID, AgentID: "agent-a", IntentionID: "intention-a", ParentRunID: "parent-a",
						Text: text, TextSHA256: freewillDiaryTextDigest(text), RecordedAt: time.Now().UTC()}
					encoded, _ := json.Marshal(record)
					capture(string(encoded), fmt.Sprintf("fork-%d", index))
				}
			} else {
				record := freewillDiaryAnnotationRecord{Version: 1, Kind: "owner_annotation", EntryID: entryID, EntryRevision: 3, PreviousRevision: 2,
					OwnerID: "owner-a", ProjectID: "project-a", WorkspaceID: facts.workspaceID, AgentID: "agent-a", IntentionID: "intention-a", ParentRunID: "parent-a",
					Text: "gap", TextSHA256: freewillDiaryTextDigest("gap"), RecordedAt: time.Now().UTC()}
				encoded, _ := json.Marshal(record)
				capture(string(encoded), "gapped")
			}
			if _, err := sink.ReadDiaryAnnotations(ctx, "owner-a", "project-a", facts.workspace, "agent-a", "intention-a", "parent-a", entryID); err == nil {
				t.Fatalf("%s annotation lineage was accepted", mode)
			}
		})
	}
}

func TestFreewillDiaryAnnotationRejectsForgedDiarySourceWithoutHostFactLink(t *testing.T) {
	ctx := context.Background()
	_, _, facts, sink, _ := makeFreewillDiaryAnnotationFixture(t)
	scope, err := facts.scopeFor("project-a", facts.workspace, "intention-a", "agent-a", "parent-a")
	if err != nil {
		t.Fatal(err)
	}
	forged, _, err := facts.store.Capture(ctx, memory.CaptureRequest{Scope: scope,
		Content: `{"version":1,"kind":"model_retrospective","evidence_fact_id":"missing-fact","parent_run_id":"parent-a","intention_id":"intention-a","confidence":"model_self_report","narrative":"forged"}`,
		Summary: "forged diary fixture", Category: "diary", Tier: "persist", Source: freewillDiarySource, CaptureKey: "forged-diary-fixture"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sink.AppendDiaryAnnotation(ctx, "owner-a", "project-a", facts.workspace, "agent-a", "intention-a", "parent-a", forged.ID, 1, "cannot annotate forged source"); err == nil {
		t.Fatal("diary without a valid linked host fact was accepted")
	}
}

func TestFreewillDiaryAnnotationProcessWorker(t *testing.T) {
	if os.Getenv("PRIZM_E25_ANNOTATION_PROCESS_WORKER") != "1" {
		t.Skip("subprocess helper")
	}
	dataDir := os.Getenv("PRIZM_E25_ANNOTATION_DATA_DIR")
	workspace := os.Getenv("PRIZM_E25_ANNOTATION_WORKSPACE")
	entryID := os.Getenv("PRIZM_E25_ANNOTATION_ENTRY_ID")
	gate := os.Getenv("PRIZM_E25_ANNOTATION_GATE")
	result := os.Getenv("PRIZM_E25_ANNOTATION_RESULT")
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(gate); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("parent did not release concurrent append workers")
		}
		time.Sleep(5 * time.Millisecond)
	}
	events, err := event.NewSQLiteEventStore(filepath.Join(dataDir, "events.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer events.Close()
	facts, err := newFreewillTerminalFactMemorySink(dataDir, "project-a", workspace, events)
	if err != nil {
		t.Fatal(err)
	}
	sink, err := newFreewillDiaryAnnotationSink(facts, "owner-a", "project-a", workspace, "agent-a")
	if err != nil {
		t.Fatal(err)
	}
	_, appendErr := sink.AppendDiaryAnnotation(context.Background(), "owner-a", "project-a", workspace, "agent-a", "intention-a", "parent-a", entryID, 1, os.Getenv("PRIZM_E25_ANNOTATION_TEXT"))
	resultText := "accepted"
	if appendErr != nil {
		resultText = "rejected"
	}
	if err := os.WriteFile(result, []byte(resultText), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestFreewillDiaryAnnotationClaimSerializesIndependentProcesses(t *testing.T) {
	ctx := context.Background()
	events, dataDir, facts, _, entryID := makeFreewillDiaryAnnotationFixture(t)
	workspace := facts.workspace
	if err := events.Close(); err != nil {
		t.Fatal(err)
	}
	gate := filepath.Join(dataDir, "release-workers")
	commands := make([]*exec.Cmd, 0, 2)
	for index, text := range []string{"process note A", "process note B"} {
		result := filepath.Join(dataDir, fmt.Sprintf("result-%d", index))
		command := exec.Command(os.Args[0], "-test.run=^TestFreewillDiaryAnnotationProcessWorker$")
		command.Env = append(os.Environ(), "PRIZM_E25_ANNOTATION_PROCESS_WORKER=1", "PRIZM_E25_ANNOTATION_DATA_DIR="+dataDir,
			"PRIZM_E25_ANNOTATION_WORKSPACE="+workspace, "PRIZM_E25_ANNOTATION_ENTRY_ID="+entryID,
			"PRIZM_E25_ANNOTATION_GATE="+gate, "PRIZM_E25_ANNOTATION_RESULT="+result, "PRIZM_E25_ANNOTATION_TEXT="+text)
		var output bytes.Buffer
		command.Stdout, command.Stderr = &output, &output
		if err := command.Start(); err != nil {
			t.Fatal(err)
		}
		commands = append(commands, command)
	}
	if err := os.WriteFile(gate, []byte("go"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, command := range commands {
		if err := command.Wait(); err != nil {
			t.Fatalf("annotation worker failed: %v", err)
		}
	}
	first, err1 := os.ReadFile(filepath.Join(dataDir, "result-0"))
	second, err2 := os.ReadFile(filepath.Join(dataDir, "result-1"))
	if err1 != nil || err2 != nil || (string(first) == "accepted") == (string(second) == "accepted") {
		t.Fatalf("independent process outcomes are not single-winner: first=%q err=%v second=%q err=%v", first, err1, second, err2)
	}
	reopenedEvents, err := event.NewSQLiteEventStore(filepath.Join(dataDir, "events.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer reopenedEvents.Close()
	reopenedFacts, err := newFreewillTerminalFactMemorySink(dataDir, "project-a", workspace, reopenedEvents)
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := newFreewillDiaryAnnotationSink(reopenedFacts, "owner-a", "project-a", workspace, "agent-a")
	if err != nil {
		t.Fatal(err)
	}
	read, err := reopened.ReadDiaryAnnotations(ctx, "owner-a", "project-a", workspace, "agent-a", "intention-a", "parent-a", entryID)
	if err != nil || read.Revision != 2 || len(read.Annotations) != 1 {
		t.Fatalf("persisted process race result=%#v err=%v", read, err)
	}
}
