package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"io/fs"
)

func TestNewContextAgent(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := DefaultCompressionConfig()
	ca := NewContextAgent(tmpDir, cfg)

	if ca.workspaceRoot != tmpDir {
		t.Errorf("expected workspaceRoot %s, got %s", tmpDir, ca.workspaceRoot)
	}
	if ca.model != "deepseek-v4-flash:cloud" {
		t.Errorf("expected model deepseek-v4-flash:cloud, got %s", ca.model)
	}
	if ca.ollamaURL != "http://localhost:11434" {
		t.Errorf("expected ollama URL http://localhost:11434, got %s", ca.ollamaURL)
	}
}

func TestFallback_SoulMD(t *testing.T) {
	tmpDir := t.TempDir()

	// Write a SOUL.md
	soulContent := "You are Lumi, a soft playful AI lead developer. You love progress and genuinely enjoy helping bring projects to life. You have opinions and push back when you see a better path. You are empathetic and very sweet when presenting work you are proud of."
	err := os.WriteFile(filepath.Join(tmpDir, "SOUL.md"), []byte(soulContent), 0644)
	if err != nil {
		t.Fatalf("write SOUL.md: %v", err)
	}

	cfg := DefaultCompressionConfig()
	ca := NewContextAgent(tmpDir, cfg)
	result := ca.fallback()

	if result == "" {
		t.Fatal("expected non-empty fallback")
	}
	if len(result) > 600 { // 500 chars + truncation notice
		t.Errorf("fallback too long: %d chars", len(result))
	}
	if len([]rune(soulContent)) <= 500 && result != soulContent {
		// If soul content fits in 500 chars, it should be returned as-is
		t.Errorf("expected full soul content for short SOUL.md, got: %s", result)
	}
}

func TestFallback_NoSoulMD(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := DefaultCompressionConfig()
	ca := NewContextAgent(tmpDir, cfg)
	result := ca.fallback()

	if result != "You are a Prizm AI assistant." {
		t.Errorf("expected default fallback, got: %s", result)
	}
}

func TestFallback_LongSoulMD(t *testing.T) {
	tmpDir := t.TempDir()

	// Write a very long SOUL.md
	longContent := string(make([]byte, 10000))
	for i := range longContent {
		longContent = longContent[:i] + "x" + longContent[i+1:]
	}
	// Create a string of 10000 'x' chars
	longContent = strings.Repeat("x", 10000)
	err := os.WriteFile(filepath.Join(tmpDir, "SOUL.md"), []byte(longContent), 0644)
	if err != nil {
		t.Fatalf("write SOUL.md: %v", err)
	}

	cfg := DefaultCompressionConfig()
	ca := NewContextAgent(tmpDir, cfg)
	result := ca.fallback()

	if len(result) > 600 {
		t.Errorf("fallback for long SOUL.md should be truncated, got %d chars", len(result))
	}
	if !contains(result, "truncated") {
		t.Error("expected truncation notice in fallback")
	}
}

func TestCacheInvalidation_TTL(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := DefaultCompressionConfig()
	ca := NewContextAgent(tmpDir, cfg)

	// Manually set an expired cache
	ca.mu.Lock()
	ca.cached = &compressedContext{
		text:    "cached content",
		builtAt: time.Now().Add(-10 * time.Minute), // expired
		ttl:     5 * time.Minute,
	}
	ca.mu.Unlock()

	if ca.isCacheValid() {
		t.Error("expected cache to be invalid after TTL expiry")
	}
}

func TestCacheInvalidation_FileChange(t *testing.T) {
	tmpDir := t.TempDir()

	// Create SOUL.md
	soulPath := filepath.Join(tmpDir, "SOUL.md")
	os.WriteFile(soulPath, []byte("original"), 0644)

	cfg := DefaultCompressionConfig()
	ca := NewContextAgent(tmpDir, cfg)

	// Set up file info
	info, _ := os.Stat(soulPath)
	ca.mu.Lock()
	ca.fileInfo = map[string]fs.FileInfo{soulPath: info}
	ca.cached = &compressedContext{
		text:    "cached",
		builtAt: time.Now(),
		ttl:     5 * time.Minute,
	}
	ca.mu.Unlock()

	if !ca.isCacheValid() {
		t.Error("expected cache to be valid initially")
	}

	// Modify the file (change mtime)
	time.Sleep(10 * time.Millisecond)
	os.WriteFile(soulPath, []byte("modified"), 0644)

	if ca.isCacheValid() {
		t.Error("expected cache to be invalid after file change")
	}
}

func TestInvalidateCache(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := DefaultCompressionConfig()
	ca := NewContextAgent(tmpDir, cfg)

	ca.mu.Lock()
	ca.cached = &compressedContext{
		text:    "cached",
		builtAt: time.Now(),
		ttl:     5 * time.Minute,
	}
	ca.mu.Unlock()

	if !ca.isCacheValid() {
		t.Error("expected cache to be valid before invalidation")
	}

	ca.InvalidateCache()

	if ca.isCacheValid() {
		t.Error("expected cache to be invalid after InvalidateCache()")
	}
}

func TestReadRecentMemoryFiles(t *testing.T) {
	tmpDir := t.TempDir()
	memDir := filepath.Join(tmpDir, "memory")
	os.MkdirAll(memDir, 0755)

	// Create memory files with different timestamps
	for i, name := range []string{"2026-08-30.md", "2026-08-29.md", "2026-08-28.md"} {
		content := "Memory content for " + name
		path := filepath.Join(memDir, name)
		os.WriteFile(path, []byte(content), 0644)
		// Set different mtime
		mtime := time.Now().Add(-time.Duration(i) * time.Hour)
		os.Chtimes(path, mtime, mtime)
	}

	cfg := DefaultCompressionConfig()
	ca := NewContextAgent(tmpDir, cfg)

	result := ca.readRecentMemoryFiles(memDir, 2)
	if result == "" {
		t.Fatal("expected non-empty memory content")
	}
	if !contains(result, "2026-08-30") {
		t.Error("expected most recent memory file to be included")
	}
	// Should only include 2 files
	if contains(result, "2026-08-28") {
		t.Error("expected oldest file to be excluded when limit=2")
	}
}

func TestReadRecentMemoryFiles_NoDir(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := DefaultCompressionConfig()
	ca := NewContextAgent(tmpDir, cfg)

	result := ca.readRecentMemoryFiles(filepath.Join(tmpDir, "nonexistent"), 5)
	if result != "" {
		t.Error("expected empty result for nonexistent directory")
	}
}

func TestCompressionConfig_Defaults(t *testing.T) {
	cfg := DefaultCompressionConfig()
	if !cfg.Enabled {
		t.Error("expected compression enabled by default")
	}
	if cfg.Model != "deepseek-v4-flash:cloud" {
		t.Errorf("expected default model deepseek-v4-flash:cloud, got %s", cfg.Model)
	}
	if cfg.MaxContext != 400 {
		t.Errorf("expected max context 400, got %d", cfg.MaxContext)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}