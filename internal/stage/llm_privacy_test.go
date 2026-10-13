package stage

import (
	"context"
	"testing"

	"github.com/emaharmony/prizm/internal/provider"
)

type fixedLLMResponse string

func (p fixedLLMResponse) Generate(context.Context, provider.GenerateRequest) (provider.GenerateResponse, error) {
	return provider.GenerateResponse{
		Text:     string(p),
		Model:    "test-model",
		Provider: "test-provider",
	}, nil
}

func TestLLMStageResultExcludesPrivateReasoningAndPreservesFencedPatch(t *testing.T) {
	const response = "<think>PRIVATE_CANARY</think>Apply this patch:\n```diff\n+literal <think>patch text</think>\n```"
	const want = "Apply this patch:\n```diff\n+literal <think>patch text</think>\n```"

	rc, result := executeFixedLLMResponse(t, response)
	if got := result.Data["response"]; got != want {
		t.Fatalf("public stage response = %q, want %q", got, want)
	}
	if rc.LLMResponse != response {
		t.Fatalf("raw response changed for downstream structured parsing: %q", rc.LLMResponse)
	}
}

func TestLLMStageResultFailsClosedOnIncompleteReasoning(t *testing.T) {
	const response = "User-facing preface. <think>PRIVATE_CANARY"
	rc, result := executeFixedLLMResponse(t, response)
	if got := result.Data["response"]; got != "" {
		t.Fatalf("incomplete private reasoning was exposed in result data: %q", got)
	}
	if rc.LLMResponse != response {
		t.Fatalf("raw response changed for downstream parser: %q", rc.LLMResponse)
	}
}

func TestLLMStageResultOmitsToolPayloadButPreservesRawRequest(t *testing.T) {
	const response = `{"type":"tool_request","tool":"write_file","input":{"content":"<think>literal code</think>"}}`
	rc, result := executeFixedLLMResponse(t, response)
	if got := result.Data["response"]; got != "" {
		t.Fatalf("structured tool payload was exposed as final text: %q", got)
	}
	if rc.LLMResponse != response {
		t.Fatalf("structured tool payload changed: %q", rc.LLMResponse)
	}
}

func executeFixedLLMResponse(t *testing.T, response string) (*RunContext, *StageResult) {
	t.Helper()
	rc := &RunContext{
		RunID:        "privacy-test",
		Task:         "test",
		Agent:        "test-agent",
		Provider:     fixedLLMResponse(response),
		ProviderName: "test-provider",
		Model:        "test-model",
	}
	finalRC, result, err := (&LLMStage{}).Execute(context.Background(), rc)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !result.Success {
		t.Fatalf("Execute() failed: %s", result.Error)
	}
	return finalRC, result
}
