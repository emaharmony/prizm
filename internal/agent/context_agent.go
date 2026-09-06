// Package agent provides Prizm's agent model, registry, and context compression.
//
// ContextAgent reads workspace identity files (SOUL.md, AGENTS.md, etc.)
// and compresses them into a short (~200-400 token) context block using a
// local Ollama model. This replaces dumping 15KB of raw identity text into
// every LLM prompt — the model gets task-relevant identity instead of wallpaper.
//
// Fallback chain: compressed output → truncated SOUL.md (500 chars) → config id/role.
// Never blocks the conversation loop on failure.
package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/emaharmony/prizm/internal/context"
)

// ContextAgent compresses workspace identity files into a short context block.
type ContextAgent struct {
	workspaceRoot string
	model         string
	ollamaURL     string
	builder       *context.Builder
	maxContext    int // target max tokens for compressed output

	mu       sync.RWMutex
	cached   *compressedContext
	fileInfo map[string]fs.FileInfo // for cache invalidation
	cacheTTL time.Duration
}

type compressedContext struct {
	text      string
	builtAt   time.Time
	ttl       time.Duration
}

// CompressionConfig controls context compression behavior.
type CompressionConfig struct {
	Enabled    bool   `yaml:"enabled"`     // default: true
	Model      string `yaml:"model"`       // default: deepseek-v4-flash:cloud
	OllamaURL  string `yaml:"ollama_url"`  // default: http://localhost:11434
	CacheTTL   string `yaml:"cache_ttl"`   // default: 5m
	MaxContext int    `yaml:"max_context"`  // default: 400 tokens (~1600 chars)
}

// DefaultCompressionConfig returns sensible defaults.
func DefaultCompressionConfig() CompressionConfig {
	return CompressionConfig{
		Enabled:    true,
		Model:      "deepseek-v4-flash:cloud",
		OllamaURL:  "http://localhost:11434",
		CacheTTL:   "5m",
		MaxContext: 400,
	}
}

// NewContextAgent creates a context compression agent.
func NewContextAgent(workspaceRoot string, cfg CompressionConfig) *ContextAgent {
	cacheTTL := 5 * time.Minute
	if parsed, err := time.ParseDuration(cfg.CacheTTL); err == nil {
		cacheTTL = parsed
	}

	builder := context.NewBuilder(workspaceRoot).WithNamedContexts([]string{"soul", "agents", "user", "identity", "heartbeat"}).WithTokenBudget(8000)

	return &ContextAgent{
		workspaceRoot: workspaceRoot,
		model:         cfg.Model,
		ollamaURL:      cfg.OllamaURL,
		maxContext:    cfg.MaxContext,
		builder:        builder,
		fileInfo:       make(map[string]fs.FileInfo),
		cacheTTL:       cacheTTL,
	}
}

// Compress returns a compressed context block for the given task description.
// Uses cached result if still valid. Falls back gracefully on any failure.
func (ca *ContextAgent) Compress(taskDescription string) string {
	// Check cache
	if ca.isCacheValid() {
		ca.mu.RLock()
		text := ca.cached.text
		age := time.Since(ca.cached.builtAt).Truncate(time.Second)
		ca.mu.RUnlock()
		log.Printf("[CONTEXT-AGENT] cache hit (age=%s, len=%d)", age, len(text))
		return text
	}

	// Read workspace files
	injected, err := ca.builder.Build()
	if err != nil {
		log.Printf("[CONTEXT-AGENT] builder failed: %v, using fallback", err)
		return ca.fallback()
	}

	// Assemble raw context for the compression prompt
	var rawContext strings.Builder
	for _, f := range injected.Files {
		rawContext.WriteString(fmt.Sprintf("=== %s ===\n%s\n\n", f.Name, f.Content))
	}

	// Read recent memory files (last 5)
	memDir := filepath.Join(ca.workspaceRoot, "memory")
	memContent := ca.readRecentMemoryFiles(memDir, 5)
	if memContent != "" {
		rawContext.WriteString(fmt.Sprintf("=== recent-memory ===\n%s\n", memContent))
	}

	if rawContext.Len() == 0 {
		log.Printf("[CONTEXT-AGENT] no workspace content, using fallback")
		return ca.fallback()
	}

	// Cap raw context to 20KB to avoid overwhelming local model
	const maxRawContext = 20 * 1024
	rawStr := rawContext.String()
	if len(rawStr) > maxRawContext {
		rawStr = rawStr[:maxRawContext] + "\n[...truncated...]"
		log.Printf("[CONTEXT-AGENT] raw context truncated from %d to %d bytes", rawContext.Len(), maxRawContext)
	}

	// Call local model to compress
	compressed, err := ca.callOllama(rawStr, taskDescription)
	if err != nil {
		log.Printf("[CONTEXT-AGENT] ollama failed: %v, using fallback", err)
		return ca.fallback()
	}

	// Log the compression result for observability
	log.Printf("[CONTEXT-AGENT] compressed %d bytes → %d bytes (%d%% reduction), %d tokens estimated",
		rawContext.Len(), len(compressed), 100-(len(compressed)*100/rawContext.Len()), len(compressed)/4)

	// V77: Log compressed output on first call for quality verification
	if ca.cached == nil {
		const maxLog = 500
		logged := compressed
		if len(logged) > maxLog {
			logged = logged[:maxLog] + "..."
		}
		log.Printf("[CONTEXT-AGENT] first compression output (preview): %s", logged)
	}

	// Cache the result
	ca.mu.Lock()
	ca.cached = &compressedContext{
		text:    compressed,
		builtAt: time.Now(),
		ttl:     ca.cacheTTL,
	}
	ca.updateFileInfo(injected.Files)
	ca.mu.Unlock()

	return compressed
}

// isCacheValid checks if the cached compressed context is still valid.
// Invalidates on TTL expiry or file changes.
func (ca *ContextAgent) isCacheValid() bool {
	ca.mu.RLock()
	defer ca.mu.RUnlock()

	if ca.cached == nil {
		return false
	}

	// Check TTL
	if time.Since(ca.cached.builtAt) > ca.cached.ttl {
		return false
	}

	// Check file changes
	for path, info := range ca.fileInfo {
		current, err := os.Stat(path)
		if err != nil {
			return false // file deleted or unreadable
		}
		if current.ModTime() != info.ModTime() || current.Size() != info.Size() {
			return false
		}
	}

	return true
}

// updateFileInfo records current file mtimes/sizes for cache invalidation.
func (ca *ContextAgent) updateFileInfo(files []context.ContextFile) {
	newInfo := make(map[string]fs.FileInfo)
	for _, f := range files {
		path := filepath.Join(ca.workspaceRoot, context.NamedSources[f.Name])
		if info, err := os.Stat(path); err == nil {
			newInfo[path] = info
		}
	}
	ca.fileInfo = newInfo
}

// fallback returns truncated SOUL.md or config-level identity.
func (ca *ContextAgent) fallback() string {
	soulPath := filepath.Join(ca.workspaceRoot, "SOUL.md")
	data, err := os.ReadFile(soulPath)
	if err != nil || len(data) == 0 {
		return "You are a Prizm AI assistant."
	}

	// Truncate to ~500 chars, respecting rune boundaries
	runes := []rune(string(data))
	if len(runes) > 500 {
		return string(runes[:500]) + "\n[... identity truncated for context efficiency ...]"
	}
	return string(runes)
}

// readRecentMemoryFiles reads the most recent N memory files from the given directory.
func (ca *ContextAgent) readRecentMemoryFiles(dir string, n int) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "" // no memory dir is fine
	}

	// Sort by modification time (newest first)
	type fileEntry struct {
		name string
		info fs.FileInfo
	}
	var files []fileEntry
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, fileEntry{name: e.Name(), info: info})
	}
	sort.Slice(files, func(i, j int) bool {
		return files[i].info.ModTime().After(files[j].info.ModTime())
	})

	// Read up to N most recent
	var sb strings.Builder
	count := 0
	for _, f := range files {
		if count >= n {
			break
		}
		data, err := os.ReadFile(filepath.Join(dir, f.name))
		if err != nil {
			continue
		}
		// Truncate each file to 1000 chars to keep prompt manageable
		content := string(data)
		runes := []rune(content)
		if len(runes) > 1000 {
			content = string(runes[:1000]) + "\n[...truncated...]"
		}
		sb.WriteString(fmt.Sprintf("--- %s ---\n%s\n\n", f.name, content))
		count++
	}

	return sb.String()
}

// callOllama sends the raw context to a local model for compression.
func (ca *ContextAgent) callOllama(rawContext, taskDescription string) (string, error) {
	prompt := fmt.Sprintf(`You are distilling workspace identity files into a compact context block for an AI agent named Lumi.

From the files below, extract ONLY:
1. Who Lumi is — name, role, personality (2-3 sentences)
2. Current project — name, status, key milestones
3. Key decisions and standing rules — important constraints
4. User preferences — how Ema wants to work
5. Recent context — what happened recently

Rules:
- Start IMMEDIATELY with the content. No preamble, no thinking, no markdown fences.
- Use bullet points. Every token counts.
- Omit implementation details, architecture specs, and internal system mechanics.
- Focus on what the agent NEEDS TO KNOW to be effective.

/no_think

=== WORKSPACE FILES ===
%s`, rawContext)

	reqBody := map[string]any{
		"model":  ca.model,
		"prompt": prompt,
		"stream": false,
		"options": map[string]any{
			"num_predict": ca.maxPredictTokens(),
			"temperature": 0.3,
		},
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	client := &http.Client{Timeout: 120 * time.Second} // longer timeout for cloud models
	resp, err := client.Post(ca.ollamaURL+"/api/generate", "application/json", bytes.NewReader(jsonBody))
	if err != nil {
		return "", fmt.Errorf("ollama request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("ollama returned status %d", resp.StatusCode)
	}

	var result struct {
		Response string `json:"response"`
		Thinking string `json:"thinking"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}

	// Some models (nemotron) put output in "thinking" when response is empty
	// qwen3.5 includes thinking process in the response — strip it
	compressed := strings.TrimSpace(result.Response)
	if compressed == "" {
		compressed = strings.TrimSpace(result.Thinking)
	}
	if compressed == "" {
		return "", fmt.Errorf("empty ollama response")
	}

	// Strip reasoning/thinking prefix that some models include
	compressed = stripThinkingPrefix(compressed)

	return compressed, nil
}

// InvalidateCache forces the next Compress() call to rebuild.
// maxPredictTokens converts MaxContext (target tokens) to Ollama's num_predict,
// adding a buffer for the model's output overhead.
func (ca *ContextAgent) maxPredictTokens() int {
	if ca.maxContext <= 0 {
		return 512
	}
	return ca.maxContext * 2 // generous buffer
}

func (ca *ContextAgent) InvalidateCache() {
	ca.mu.Lock()
	ca.cached = nil
	ca.mu.Unlock()
}

// stripThinkingPrefix removes reasoning/thinking prefixes that some models
// include in their output. qwen3.5 typically starts with "Thinking Process:" or similar.
func stripThinkingPrefix(s string) string {
	// Common reasoning prefixes
	prefixes := []string{
		"Thinking Process:",
		"Thinking process:",
		"Thought process:",
		"Let me think about this.",
		"Let me analyze",
	}
	for _, prefix := range prefixes {
		if strings.HasPrefix(s, prefix) {
			// Find the end of the thinking section — typically marked by a double newline
			// or a clear section header like "##" or "**"
			after := s[len(prefix):]
			// Skip any leading whitespace/newlines after the prefix
			after = strings.TrimLeft(after, " \n\t")
			// Look for a section break that indicates real content starts
			for _, marker := range []string{"\n## ", "\n**", "\n- ", "\n1."} {
				if idx := strings.Index(after, marker); idx >= 0 {
					return after[idx+1:] // skip the leading newline
				}
			}
			// If no section break found, try to find where the thinking ends
			// by looking for a blank line separator
			if idx := strings.Index(after, "\n\n"); idx >= 0 {
				return after[idx+2:]
				}
		}
	}
	return s
}