package governance

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseFrontmatter(t *testing.T) {
	content := `---
governance:
  status: frozen
  frozen_paths:
    - schema.prizma
    - internal/db/
  reason: "Production freeze"
---

# Schema Freeze
Body text here.
`
	fm, ok := parseFrontmatter(content)
	if !ok {
		t.Fatal("expected frontmatter to parse")
	}
	if fm.Governance.Status != "frozen" {
		t.Errorf("expected status 'frozen', got %q", fm.Governance.Status)
	}
	if len(fm.Governance.FrozenPaths) != 2 {
		t.Fatalf("expected 2 frozen paths, got %d", len(fm.Governance.FrozenPaths))
	}
	if fm.Governance.FrozenPaths[0] != "schema.prizma" {
		t.Errorf("expected first path 'schema.prizma', got %q", fm.Governance.FrozenPaths[0])
	}
	if fm.Governance.FrozenPaths[1] != "internal/db/" {
		t.Errorf("expected second path 'internal/db/', got %q", fm.Governance.FrozenPaths[1])
	}
	if fm.Governance.Reason != "Production freeze" {
		t.Errorf("expected reason 'Production freeze', got %q", fm.Governance.Reason)
	}
}

func TestParseFrontmatter_NoFrontmatter(t *testing.T) {
	content := "# Just a doc\nNo frontmatter here."
	_, ok := parseFrontmatter(content)
	if ok {
		t.Error("expected no frontmatter")
	}
}

func TestParseFrontmatter_NoGovernanceKey(t *testing.T) {
	content := `---
title: "Some doc"
---

# Doc
`
	_, ok := parseFrontmatter(content)
	// Frontmatter exists but no governance key — should return false
	if ok {
		t.Error("expected false when no governance key in frontmatter")
	}
}

func TestMatchesFrozenPath_ExactMatch(t *testing.T) {
	matched, pattern := MatchesFrozenPath("schema.prizma", []string{"schema.prizma"})
	if !matched {
		t.Error("expected exact match")
	}
	if pattern != "schema.prizma" {
		t.Errorf("expected pattern 'schema.prizma', got %q", pattern)
	}
}

func TestMatchesFrozenPath_DirectoryMatch(t *testing.T) {
	matched, _ := MatchesFrozenPath("internal/db/schema.go", []string{"internal/db/"})
	if !matched {
		t.Error("expected directory match")
	}
}

func TestMatchesFrozenPath_DirectoryExactMatch(t *testing.T) {
	matched, _ := MatchesFrozenPath("internal/db", []string{"internal/db/"})
	if !matched {
		t.Error("expected directory exact match")
	}
}

func TestMatchesFrozenPath_GlobMatch(t *testing.T) {
	matched, _ := MatchesFrozenPath("migrations/001_init.sql", []string{"migrations/*.sql"})
	if !matched {
		t.Error("expected glob match")
	}
}

func TestMatchesFrozenPath_BasenameGlob(t *testing.T) {
	matched, _ := MatchesFrozenPath("some/dir/init.sql", []string{"*.sql"})
	if !matched {
		t.Error("expected basename glob match")
	}
}

func TestMatchesFrozenPath_NoMatch(t *testing.T) {
	matched, _ := MatchesFrozenPath("README.md", []string{"schema.prizma", "internal/db/"})
	if matched {
		t.Error("expected no match")
	}
}

func TestDetectGovernance_WithMarkers(t *testing.T) {
	content := "# Doc\n\nStatus: Frozen — no changes without approval"
	if !DetectGovernance(content) {
		t.Error("expected governance detection")
	}
}

func TestDetectGovernance_WithoutMarkers(t *testing.T) {
	content := "# Doc\n\nThis is a regular document about architecture."
	if DetectGovernance(content) {
		t.Error("expected no governance detection")
	}
}

func TestLoader_LoadWithFrontmatter(t *testing.T) {
	workspace := t.TempDir()
	docsDir := filepath.Join(workspace, "docs")
	os.MkdirAll(docsDir, 0755)

	docContent := `---
governance:
  status: frozen
  frozen_paths:
    - schema.prizma
    - internal/db/
  reason: "Production freeze"
  requires_approval_from: ema
---

# Schema Freeze
This schema is frozen.
`
	os.WriteFile(filepath.Join(docsDir, "SCHEMA-FREEZE.md"), []byte(docContent), 0644)

	loader := NewLoader(workspace, nil)
	docs, err := loader.scanDocs()
	if err != nil {
		t.Fatalf("scanDocs error: %v", err)
	}
	if len(docs) != 1 {
		t.Fatalf("expected 1 governance doc, got %d", len(docs))
	}
	if docs[0].Frontmatter.Governance.Status != "frozen" {
		t.Errorf("expected status 'frozen', got %q", docs[0].Frontmatter.Governance.Status)
	}
	if len(docs[0].Frontmatter.Governance.FrozenPaths) != 2 {
		t.Errorf("expected 2 frozen paths, got %d", len(docs[0].Frontmatter.Governance.FrozenPaths))
	}
}

func TestLoader_LoadWithMarkersOnly(t *testing.T) {
	workspace := t.TempDir()
	docsDir := filepath.Join(workspace, "docs")
	os.MkdirAll(docsDir, 0755)

	docContent := `# Old Doc

Status: Frozen — do not modify
No frontmatter here.
`
	os.WriteFile(filepath.Join(docsDir, "OLD-FREEZE.md"), []byte(docContent), 0644)

	loader := NewLoader(workspace, nil)
	docs, err := loader.scanDocs()
	if err != nil {
		t.Fatalf("scanDocs error: %v", err)
	}
	if len(docs) != 1 {
		t.Fatalf("expected 1 governance doc (markers only), got %d", len(docs))
	}
	// Should have status "detected" since no frontmatter
	if docs[0].Frontmatter.Governance.Status != "detected" {
		t.Errorf("expected status 'detected', got %q", docs[0].Frontmatter.Governance.Status)
	}
}

func TestLoader_NoGovernanceDocs(t *testing.T) {
	workspace := t.TempDir()
	docsDir := filepath.Join(workspace, "docs")
	os.MkdirAll(docsDir, 0755)

	os.WriteFile(filepath.Join(docsDir, "README.md"), []byte("# README\nJust a readme."), 0644)

	loader := NewLoader(workspace, nil)
	docs, err := loader.scanDocs()
	if err != nil {
		t.Fatalf("scanDocs error: %v", err)
	}
	if len(docs) != 0 {
		t.Errorf("expected 0 governance docs, got %d", len(docs))
	}
}