package prizm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// RemembranceClient is the Go interface for calling the Remembrance memory service.
// This is what the Prizm agent runtime uses before executing an agent.
type RemembranceClient interface {
	BuildContext(ctx context.Context, req BuildContextRequest) (*BuildContextResponse, error)
	SearchMemory(ctx context.Context, req SearchMemoryRequest) (*SearchMemoryResponse, error)
	IngestMemory(ctx context.Context, req IngestMemoryRequest) (*IngestMemoryResponse, error)
}

// HTTPRemembranceClient implements RemembranceClient using HTTP calls to the Python service.
type HTTPRemembranceClient struct {
	baseURL    string
	httpClient *http.Client
}

// NewRemembranceClient creates a new HTTP client for the Remembrance service.
func NewRemembranceClient(baseURL string) *HTTPRemembranceClient {
	return &HTTPRemembranceClient{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// ── Request/Response Types ──────────────────────────────────────────

type BuildContextRequest struct {
	ProjectID         string `json:"project_id"`
	AgentID           string `json:"agent_id"`
	Task              string `json:"task"`
	MaxTokens         int    `json:"max_tokens"`
	IncludeUserMemory bool   `json:"include_user_memory"`
	UserID            string `json:"user_id,omitempty"`
	OutputFormat      string `json:"output_format"`
}

type BuildContextResponse struct {
	ProjectID        string   `json:"project_id"`
	AgentID          string   `json:"agent_id"`
	Task             string   `json:"task"`
	SelectedMemories []string `json:"selected_memories"`
	ContextMarkdown  string   `json:"context_markdown,omitempty"`
	ContextJSON      any      `json:"context_json,omitempty"`
	Warnings         []string `json:"warnings"`
	TokenCount       int      `json:"token_count"`
}

type SearchMemoryRequest struct {
	ProjectID         string `json:"project_id"`
	Query             string `json:"query"`
	Scope             string `json:"scope"`
	Limit             int    `json:"limit"`
	IncludeUserMemory bool   `json:"include_user_memory"`
	UserID            string `json:"user_id,omitempty"`
}

type SearchMemoryResult struct {
	MemoryID string  `json:"memory_id"`
	Title    string  `json:"title"`
	Summary  string  `json:"summary"`
	Score    float64 `json:"score"`
	Reason   string  `json:"reason,omitempty"`
}

type SearchMemoryResponse struct {
	Results []SearchMemoryResult `json:"results"`
	Total   int                  `json:"total"`
	Query   string               `json:"query"`
}

type IngestMemoryRequest struct {
	ProjectID       string   `json:"project_id"`
	UserID          string   `json:"user_id,omitempty"`
	Scope           string   `json:"scope"`
	Category        string   `json:"category"`
	Title           string   `json:"title"`
	Summary         string   `json:"summary"`
	Content         string   `json:"content"`
	Tags            []string `json:"tags"`
	ImportanceScore float64  `json:"importance_score"`
	ConfidenceScore float64  `json:"confidence_score"`
	SourceType      string   `json:"source_type"`
	SourceRef       string   `json:"source_ref,omitempty"`
	SourceAgent     string   `json:"source_agent"`
}

type IngestMemoryResponse struct {
	MemoryID string `json:"memory_id"`
	Status   string `json:"status"`
}

// ── Implementation ──────────────────────────────────────────────────

func (c *HTTPRemembranceClient) BuildContext(ctx context.Context, req BuildContextRequest) (*BuildContextResponse, error) {
	var resp BuildContextResponse
	err := c.doPost(ctx, "/v1/context/build", req, &resp)
	if err != nil {
		return nil, fmt.Errorf("build context failed: %w", err)
	}
	return &resp, nil
}

func (c *HTTPRemembranceClient) SearchMemory(ctx context.Context, req SearchMemoryRequest) (*SearchMemoryResponse, error) {
	var resp SearchMemoryResponse
	err := c.doPost(ctx, "/v1/memory/search", req, &resp)
	if err != nil {
		return nil, fmt.Errorf("search memory failed: %w", err)
	}
	return &resp, nil
}

func (c *HTTPRemembranceClient) IngestMemory(ctx context.Context, req IngestMemoryRequest) (*IngestMemoryResponse, error) {
	var resp IngestMemoryResponse
	err := c.doPost(ctx, "/v1/memory/ingest", req, &resp)
	if err != nil {
		return nil, fmt.Errorf("ingest memory failed: %w", err)
	}
	return &resp, nil
}

func (c *HTTPRemembranceClient) doPost(ctx context.Context, path string, body any, result any) error {
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+path, bytes.NewReader(jsonBody))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("server error %d: %s", resp.StatusCode, string(body))
	}

	return json.NewDecoder(resp.Body).Decode(result)
}
