package memory

import (
	"testing"
	"time"
)

func TestParseFrontMatter(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantMeta map[string]string
		wantRest string
	}{
		{
			name: "full front matter",
			input: `---
id: mem-001
category: decision
confidence: "0.9"
provenance: prizm:lumi
keywords: [lumi, origin, name]
tier: active
created: 2026-07-27
---

Lumi's name origin: Kirbii asked what to name the AI.`,
			wantMeta: map[string]string{
				"id":         "mem-001",
				"category":   "decision",
				"confidence":  "0.9",
				"provenance":  "prizm:lumi",
				"keywords":    "lumi, origin, name",
				"tier":        "active",
				"created":     "2026-07-27",
			},
			wantRest: "Lumi's name origin: Kirbii asked what to name the AI.",
		},
		{
			name:     "no front matter",
			input:    "Just regular content here",
			wantMeta: nil,
			wantRest: "Just regular content here",
		},
		{
			name: "superseded memory",
			input: `---
id: mem-old
superseded_by: mem-new
confidence: "0.3"
---

This is outdated information.`,
			wantMeta: map[string]string{
				"id":            "mem-old",
				"superseded_by": "mem-new",
				"confidence":    "0.3",
			},
			wantRest: "This is outdated information.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			meta, rest := parseFrontMatter(tt.input)
			if tt.wantMeta == nil {
				if meta != nil {
					t.Errorf("expected nil meta, got %v", meta)
				}
			} else {
				for key, wantVal := range tt.wantMeta {
					gotVal, ok := meta[key]
					if !ok {
						t.Errorf("missing key %q in meta", key)
					} else if gotVal != wantVal {
						t.Errorf("meta[%q] = %q, want %q", key, gotVal, wantVal)
					}
				}
			}
			if rest != tt.wantRest {
				t.Errorf("rest = %q, want %q", rest, tt.wantRest)
			}
		})
	}
}

func TestApplyFrontMatter(t *testing.T) {
	meta := map[string]string{
		"id":           "mem-001",
		"category":     "decision",
		"confidence":   "0.9",
		"provenance":   "prizm:lumi",
		"keywords":     "lumi, origin, name",
		"tier":         "active",
		"created":      "2026-07-27",
		"superseded_by": "",
		"recall_count": "3",
	}

	mem := &Memory{}
	applyFrontMatter(mem, meta)

	if mem.ID != "mem-001" {
		t.Errorf("ID = %q, want mem-001", mem.ID)
	}
	if mem.Category != "decision" {
		t.Errorf("Category = %q, want decision", mem.Category)
	}
	if mem.Source != "prizm:lumi" {
		t.Errorf("Source = %q, want prizm:lumi", mem.Source)
	}
	if mem.Tier != "active" {
		t.Errorf("Tier = %q, want active", mem.Tier)
	}
	if len(mem.KeyTopics) != 3 {
		t.Errorf("KeyTopics = %v, want 3 items", mem.KeyTopics)
	}
	if mem.Metadata["confidence"] != "0.9" {
		t.Errorf("confidence = %q, want 0.9", mem.Metadata["confidence"])
	}
	if mem.Metadata["recall_count"] != "3" {
		t.Errorf("recall_count = %q, want 3", mem.Metadata["recall_count"])
	}
	if !mem.CreatedAt.Equal(time.Date(2026, 7, 27, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("CreatedAt = %v, want 2026-07-27", mem.CreatedAt)
	}
}

func TestIsSuperseded(t *testing.T) {
	tests := []struct {
		name string
		mem  Memory
		want bool
	}{
		{
			name: "superseded",
			mem:  Memory{Metadata: map[string]string{"superseded_by": "mem-new"}},
			want: true,
		},
		{
			name: "not superseded",
			mem:  Memory{Metadata: map[string]string{"superseded_by": ""}},
			want: false,
		},
		{
			name: "no metadata",
			mem:  Memory{},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isSuperseded(tt.mem); got != tt.want {
				t.Errorf("isSuperseded() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFormatFrontMatter(t *testing.T) {
	mem := Memory{
		ID:        "mem-001",
		Category:  "decision",
		Tier:      "active",
		Source:    "prizm:lumi",
		KeyTopics: []string{"lumi", "origin", "name"},
		Summary:   "Lumi's name origin story",
		Metadata: map[string]string{
			"confidence":   "0.9",
			"recall_count": "3",
		},
		CreatedAt: time.Date(2026, 7, 27, 0, 0, 0, 0, time.UTC),
	}

	fm := formatFrontMatter(mem)

	// Should contain all the fields
	checks := []string{
		"id: mem-001",
		"category: decision",
		"tier: active",
		"provenance: prizm:lumi",
		"keywords: [lumi, origin, name]",
		"confidence: 0.9",
		"recall_count: 3",
		"created: 2026-07-27",
		"---",
	}

	for _, check := range checks {
		if !contains(fm, check) {
			t.Errorf("formatFrontMatter missing: %q\nGot:\n%s", check, fm)
		}
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 || containsStr(s, sub))
}

func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}