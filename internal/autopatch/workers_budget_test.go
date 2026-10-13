package autopatch

import (
	"context"
	"testing"

	"github.com/emaharmony/prizm/internal/provider"
)

type captureGenerateProvider struct {
	request provider.GenerateRequest
}

func (p *captureGenerateProvider) Generate(_ context.Context, req provider.GenerateRequest) (provider.GenerateResponse, error) {
	p.request = req
	return provider.GenerateResponse{Text: "no patch returned"}, nil
}

func TestLocalAgentWorkerDoesNotSetDefaultOutputTokenCap(t *testing.T) {
	p := &captureGenerateProvider{}
	worktree := t.TempDir()
	artifactDir := t.TempDir()
	worker := NewLocalAgentWorker(p, "model", "agent")

	_, _ = worker.Run(context.Background(), WorkerRequest{
		TaskID: "task", Worktree: worktree, ArtifactDir: artifactDir,
		Description: "make a small change",
	})

	if p.request.MaxTokens != 0 {
		t.Fatalf("MaxTokens = %d, want 0 (provider default)", p.request.MaxTokens)
	}
}
