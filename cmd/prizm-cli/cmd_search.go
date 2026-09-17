package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/emaharmony/prizm/internal/vector"
)

// searchCmd implements `prizm search --query "..." --top-k 5`
// Searches Prizm's vector store for semantically similar entries.
func searchCmd(args []string) {
	cmd := flag.NewFlagSet("search", flag.ExitOnError)
	queryFlag := cmd.String("query", "", "Text query to search for (required)")
	topKFlag := cmd.Int("top-k", 10, "Number of results to return")
	minScoreFlag := cmd.Float64("min-score", 0.5, "Minimum similarity score (0-1)")
	sourceFlag := cmd.String("source", "", "Filter by source type (event, run_summary, artifact)")
	providerFlag := cmd.String("provider", "mock", "Embedding provider (mock, openai, ollama)")
	cmd.Parse(args)

	if *queryFlag == "" {
		fmt.Fprintln(os.Stderr, "Error: --query is required")
		os.Exit(1)
	}

	ctx := context.Background()

	// Create embedding provider
	embedProvider, err := createEmbeddingProvider(*providerFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	// Create in-memory vector store (SQLite store coming in V15a)
	store := vector.NewMemoryVectorStore(embedProvider.Dimension())

	// Generate embedding for query
	queryVec, err := embedProvider.Embed(ctx, *queryFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error embedding query: %v\n", err)
		os.Exit(1)
	}

	// Search
	opts := vector.SearchOptions{
		TopK:         *topKFlag,
		MinScore:     *minScoreFlag,
		SourceFilter: *sourceFlag,
	}

	results, err := store.Search(ctx, queryVec, opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error searching: %v\n", err)
		os.Exit(1)
	}

	if len(results) == 0 {
		fmt.Println("No results found. (The in-memory store is empty — index data first.)")
		return
	}

	fmt.Printf("Found %d results for %q:\n\n", len(results), *queryFlag)
	for i, r := range results {
		fmt.Printf("%d. [%.4f] %s (source: %s, id: %s)\n",
			i+1, r.Score, truncate(r.Entry.Content, 80), r.Entry.Source, r.Entry.ID)
		if r.Entry.Metadata != nil {
			meta, _ := json.Marshal(r.Entry.Metadata)
			fmt.Printf("   metadata: %s\n", string(meta))
		}
	}
}

func createEmbeddingProvider(name string) (vector.EmbeddingProvider, error) {
	switch strings.ToLower(name) {
	case "mock":
		return vector.NewMockEmbeddingProvider(128), nil
	case "openai":
		apiKey := os.Getenv("OPENAI_API_KEY")
		if apiKey == "" {
			return nil, fmt.Errorf("OPENAI_API_KEY environment variable required for openai provider")
		}
		model := os.Getenv("OPENAI_EMBEDDING_MODEL")
		if model == "" {
			model = vector.OpenAIEmbeddingSmall
		}
		return vector.NewOpenAIEmbeddingProvider(apiKey, model, 0), nil
	case "ollama":
		baseURL := resolveOllamaURL("", "")
		model := os.Getenv("OLLAMA_EMBEDDING_MODEL")
		if model == "" {
			model = "nomic-embed-text"
		}
		return vector.NewOllamaEmbeddingProvider(baseURL, model), nil
	default:
		return nil, fmt.Errorf("unknown embedding provider: %s (use mock, openai, or ollama)", name)
	}
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
