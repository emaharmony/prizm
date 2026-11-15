package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/emaharmony/prizm/internal/freewill"
	"github.com/emaharmony/prizm/internal/gitx"
	"github.com/emaharmony/prizm/internal/workflow/multiagent"
)

func TestFreewillAdmissionCleanupRequiresDefinitiveAbsence(t *testing.T) {
	if !freewillAdmissionConfirmedAbsent(nil, "") {
		t.Fatal("successful inspection with no run should permit unadmitted cleanup")
	}
	if freewillAdmissionConfirmedAbsent(errors.New("inspection unavailable"), "") {
		t.Fatal("inspection failure must preserve artifacts because admission is uncertain")
	}
	if freewillAdmissionConfirmedAbsent(nil, "run_child") {
		t.Fatal("existing durable child must preserve artifacts")
	}
}

func TestFreewillReferenceInputEnforcesCapabilityAndBudgetSubset(t *testing.T) {
	base := freewill.IntentionRequest{
		RequestID: "request-1", AgentID: "agent-1", Origin: freewill.OriginAgent, CandidateSource: freewill.SourceNew,
		Title: "Bounded implementation", Goal: "Make one small change", ReasonSummary: "The task is narrowly scoped.",
		RequestedCapabilities: []string{"code"}, Scope: freewill.Scope{ProjectID: "project-1", Workspace: `D:\repo`},
		IdempotencyKey: "idem-1", CorrelationID: "corr-1",
	}
	tokens, minutes := 500, 3
	base.Budget.MaxTokens, base.Budget.RuntimeMinutes = &tokens, &minutes
	input, err := freewillReferenceInput(base)
	if err != nil {
		t.Fatalf("supported code budget rejected: %v", err)
	}
	if input.Budgets.MaxTokens == nil || *input.Budgets.MaxTokens != multiagent.Limit(tokens) || input.Budgets.MaxDuration != "3m" ||
		input.Budgets.MaxTransitions == nil || input.Budgets.MaxLocalIterations == nil || *input.Budgets.MaxLocalIterations != 8 || input.Budgets.MaxRetries == nil ||
		*input.Budgets.MaxRetries != 1 {
		t.Fatalf("supported budget was not enforced in reference limits: %#v", input.Budgets)
	}

	for name, mutate := range map[string]func(*freewill.IntentionRequest){
		"decision-only":    func(r *freewill.IntentionRequest) { r.RequestedCapabilities = []string{"decision_only"} },
		"extra capability": func(r *freewill.IntentionRequest) { r.RequestedCapabilities = []string{"code", "network"} },
		"model calls":      func(r *freewill.IntentionRequest) { value := 2; r.Budget.MaxModelCalls = &value },
		"disk":             func(r *freewill.IntentionRequest) { value := 64; r.Budget.DiskMB = &value },
	} {
		t.Run(name, func(t *testing.T) {
			request := base
			request.RequestedCapabilities = append([]string(nil), base.RequestedCapabilities...)
			request.Budget = base.Budget
			mutate(&request)
			if _, err := freewillReferenceInput(request); err == nil {
				t.Fatal("unsupported capability or budget was silently accepted")
			}
		})
	}
}

func TestFreewillReferenceWorkspaceRecoversOwnedOrphanAndCleansIt(t *testing.T) {
	ctx := context.Background()
	source := newFreewillDispatchTestRepo(t)
	const runID = "run_freewill_workspace_recovery"

	// Model a process stopping after `git worktree add` but before its run manifest.
	firstPath, firstID, firstSource, err := isolatedReferenceWorkspace(ctx, source, runID)
	if err != nil {
		t.Fatalf("create detached workspace: %v", err)
	}
	recoveredPath, recoveredID, recoveredSource, worktreePath, err := recoverOrCreateFreewillReferenceWorkspace(ctx, source, runID)
	if err != nil {
		t.Fatalf("recover deterministic orphan: %v", err)
	}
	if recoveredPath != firstPath || recoveredID != firstID || recoveredSource != firstSource || worktreePath != firstPath {
		t.Fatalf("recovery changed workspace identity: first=(%q,%q,%q) recovered=(%q,%q,%q,%q)", firstPath, firstID, firstSource, recoveredPath, recoveredID, recoveredSource, worktreePath)
	}

	manifestPath := filepath.Join(t.TempDir(), runID, "multiagent_manifest.json")
	if err := cleanupUnadmittedFreewillWorkspace(ctx, recoveredSource, worktreePath, manifestPath); err != nil {
		t.Fatalf("clean unadmitted workspace: %v", err)
	}
	if _, err := os.Stat(worktreePath); !os.IsNotExist(err) {
		t.Fatalf("unadmitted worktree remains: stat err=%v", err)
	}
}

func TestFreewillReferenceWorkspaceDoesNotReuseForeignPath(t *testing.T) {
	source := newFreewillDispatchTestRepo(t)
	const runID = "run_freewill_foreign_workspace"
	worktreePath := filepath.Join(source, ".prizm", "worktrees", gitx.SafeID(runID, "run"))
	if err := os.MkdirAll(worktreePath, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, err := recoverOrCreateFreewillReferenceWorkspace(context.Background(), source, runID); err == nil {
		t.Fatal("reused a deterministic path that is not registered as this source's worktree")
	}
	if info, err := os.Stat(worktreePath); err != nil || !info.IsDir() {
		t.Fatalf("foreign path was modified during refusal: info=%v err=%v", info, err)
	}
}

func newFreewillDispatchTestRepo(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	t.Cleanup(func() {
		_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return nil
			}
			if info.IsDir() {
				_ = os.Chmod(path, 0o755)
			} else {
				_ = os.Chmod(path, 0o666)
			}
			return nil
		})
	})
	for _, args := range [][]string{{"init", "-b", "main"}, {"config", "user.email", "test@prizm.local"}, {"config", "user.name", "Prizm Test"}} {
		if _, err := gitx.RunCommand(ctx, root, "", "git", args...); err != nil {
			t.Fatalf("initialize test repository: %v", err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := gitx.RunCommand(ctx, root, "", "git", "add", "README.md"); err != nil {
		t.Fatal(err)
	}
	if _, err := gitx.RunCommand(ctx, root, "", "git", "commit", "-m", "base"); err != nil {
		t.Fatal(err)
	}
	return root
}
