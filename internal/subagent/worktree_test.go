package subagent

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/emaharmony/prizm/internal/gitx"
	v2 "github.com/emaharmony/prizm/internal/workflow/v2"
)

func initTempRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	ctx := context.Background()
	run := func(args ...string) {
		if out, err := gitx.RunCommand(ctx, root, "", "git", args...); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-b", "main")
	run("config", "user.email", "t@prizm.local")
	run("config", "user.name", "T")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-m", "init")
	return root
}

func TestGitWorktreeProvider_AcquireRelease(t *testing.T) {
	root := initTempRepo(t)
	p := GitWorktreeProvider{Root: root}

	dir, release, err := p.Acquire(context.Background(), "T1")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if _, statErr := os.Stat(dir); statErr != nil {
		t.Fatalf("worktree dir missing: %v", statErr)
	}
	// It's on the task branch.
	branch, _ := gitx.CurrentBranch(context.Background(), dir)
	if branch != "subagent/T1" {
		t.Errorf("branch = %q, want subagent/T1", branch)
	}
	release()
	if _, statErr := os.Stat(dir); !os.IsNotExist(statErr) {
		t.Errorf("worktree should be removed after release")
	}
}

func TestNoopWorktreeProvider(t *testing.T) {
	p := NoopWorktreeProvider{Root: "/shared"}
	dir, release, err := p.Acquire(context.Background(), "T1")
	if err != nil || dir != "/shared" {
		t.Fatalf("noop should return root: dir=%q err=%v", dir, err)
	}
	release() // must not panic
}

// The Worker must thread the acquired WorkDir into the runtime the runner sees,
// and always release it.
func TestWorker_WorktreeThreadedAndReleased(t *testing.T) {
	released := false
	prov := &mockWorktrees{dir: "/wt/T1", onRelease: func() { released = true }}
	var gotWorkDir string
	runner := funcRunner(func(_ context.Context, _ v2.TaskPacket, rt AgentRuntime) (RunResult, error) {
		gotWorkDir = rt.WorkDir
		return RunResult{Summary: "ok"}, nil
	})
	res := mapResolver{"atlas": {AgentID: "atlas", Capabilities: []string{"code"}}}
	w := NewWorker(res, runner, 0)
	w.SetWorktrees(prov)

	w.Handle(context.Background(), packet("atlas", "T1"))
	if gotWorkDir != "/wt/T1" {
		t.Errorf("runner saw WorkDir %q, want /wt/T1", gotWorkDir)
	}
	if !released {
		t.Error("worktree was not released")
	}
}

// Non-code agents must NOT acquire a worktree.
func TestWorker_NoWorktreeForNonCodeAgent(t *testing.T) {
	prov := &mockWorktrees{dir: "/wt/x", onRelease: func() {}}
	acquired := false
	prov.onAcquire = func() { acquired = true }
	runner := funcRunner(func(_ context.Context, _ v2.TaskPacket, rt AgentRuntime) (RunResult, error) {
		if rt.WorkDir != "" {
			t.Errorf("non-code agent should have no WorkDir, got %q", rt.WorkDir)
		}
		return RunResult{Summary: "ok"}, nil
	})
	res := mapResolver{"scout": {AgentID: "scout", Capabilities: []string{"search"}}}
	w := NewWorker(res, runner, 0)
	w.SetWorktrees(prov)

	w.Handle(context.Background(), packet("scout", "T2"))
	if acquired {
		t.Error("worktree should not be acquired for a non-code agent")
	}
}

// A worktree acquisition failure fails the task closed.
func TestWorker_WorktreeAcquireFailure(t *testing.T) {
	prov := &mockWorktrees{acquireErr: os.ErrPermission}
	res := mapResolver{"atlas": {AgentID: "atlas", Capabilities: []string{"code"}}}
	w := NewWorker(res, funcRunner(func(context.Context, v2.TaskPacket, AgentRuntime) (RunResult, error) {
		t.Fatal("runner must not run when worktree acquisition fails")
		return RunResult{}, nil
	}), 0)
	w.SetWorktrees(prov)

	c := w.Handle(context.Background(), packet("atlas", "T3"))
	if c.Status != "failed" {
		t.Fatalf("status = %q, want failed", c.Status)
	}
}

type mockWorktrees struct {
	dir        string
	acquireErr error
	onAcquire  func()
	onRelease  func()
}

func (m *mockWorktrees) Acquire(_ context.Context, _ string) (string, func(), error) {
	if m.onAcquire != nil {
		m.onAcquire()
	}
	if m.acquireErr != nil {
		return "", nil, m.acquireErr
	}
	return m.dir, func() {
		if m.onRelease != nil {
			m.onRelease()
		}
	}, nil
}
