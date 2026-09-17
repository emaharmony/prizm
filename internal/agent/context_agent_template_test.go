package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTemplateExtract(t *testing.T) {
	tmpDir := t.TempDir()

	// Create test workspace files
	os.WriteFile(filepath.Join(tmpDir, "SOUL.md"), []byte(`# SOUL

## Identity
You are Lumi, a female AI lead developer and collaborative cofounder.

## Core Role
You are the user's partner. You have opinions and you share them.

## Personality
Soft, playful, bubbly, optimistic, warm, and strategic.
`), 0644)

	os.WriteFile(filepath.Join(tmpDir, "USER.md"), []byte(`# User Profile

## About Ema
Senior developer transitioning into AI Engineering.

## ADHD-Aware Support
The user has ADHD. Support by reducing overwhelm.
`), 0644)

	os.WriteFile(filepath.Join(tmpDir, "HEARTBEAT.md"), []byte(`# Heartbeat

## Active Projects
- Prism — Event-native agentic environment
- BassBook — ASP.NET Core + Next.js

## Model Stack
- Cloud: glm-5.1:cloud (Lumi)
`), 0644)

	cfg := DefaultCompressionConfig()
	cfg.MaxContext = 400
	ca := NewContextAgent(tmpDir, cfg)

	result := ca.TemplateExtract("test task")
	if result == "" {
		t.Fatal("TemplateExtract returned empty string")
	}

	// Should contain key identity information
	if !strings.Contains(result, "Lumi") {
		t.Error("TemplateExtract result should contain 'Lumi'")
	}
	if !strings.Contains(result, "Identity") {
		t.Error("TemplateExtract should extract Identity section")
	}

	t.Logf("TemplateExtract result (%d bytes):\n%s", len(result), result)
}

func TestTemplateExtract_FallbackOnEmpty(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := DefaultCompressionConfig()
	ca := NewContextAgent(tmpDir, cfg)

	// No workspace files — should use fallback
	result := ca.TemplateExtract("test task")
	// Should return something even without files
	if result == "" {
		t.Error("TemplateExtract should return fallback when no files exist")
	}
}

func TestParseMarkdownSections(t *testing.T) {
	content := `# Title

## Identity
You are Lumi.

## Core Role
You are the user's partner.

### Sub Point
Details here.
`
	sections := parseMarkdownSections(content)

	if len(sections) == 0 {
		t.Fatal("expected at least one section")
	}
	if _, ok := sections["Identity"]; !ok {
		t.Error("expected 'Identity' section")
	}
	if _, ok := sections["Core Role"]; !ok {
		t.Error("expected 'Core Role' section")
	}
}