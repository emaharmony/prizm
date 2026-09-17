package memory

import (
	"fmt"
	"testing"
	"time"
)

func TestExtractKeywords(t *testing.T) {
	tests := []struct {
		name     string
		message  string
		wantMin  int // minimum expected keywords
		contains []string
		notContains []string
	}{
		{
			name:     "identity question",
			message:  "who am I?",
			wantMin:  1,
			contains: []string{},
			// All words are stop words, so the fallback returns the original
			// words minus single chars ("who", "am"). Only single chars are excluded.
			notContains: []string{"i"},
		},
		{
			name:     "naming question",
			message:  "why was I named Lumi?",
			wantMin:  2,
			contains: []string{"named", "lumi"},
			notContains: []string{"why", "was", "i"},
		},
		{
			name:     "technical question",
			message:  "how does the memory search system work?",
			wantMin:  3,
			contains: []string{"memory", "search", "system", "work"},
			notContains: []string{"how", "does", "the"},
		},
		{
			name:     "decision question",
			message:  "what did we decide about the memory architecture?",
			wantMin:  3,
			contains: []string{"decide", "memory", "architecture"},
			notContains: []string{"what", "did", "we", "about", "the"},
		},
		{
			name:     "short query",
			message:  "Ema preferences",
			wantMin:  2,
			contains: []string{"ema", "preferences"},
			notContains: []string{},
		},
		{
			name:     "punctuation handling",
			message:  "what's the status of Prizm???",
			wantMin:  2,
			contains: []string{"status", "prizm"},
			notContains: []string{"what's", "???"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			keywords := ExtractKeywords(tt.message)
			if len(keywords) < tt.wantMin {
				t.Errorf("ExtractKeywords(%q) = %v, want at least %d keywords", tt.message, keywords, tt.wantMin)
			}
			for _, w := range tt.contains {
				found := false
				for _, kw := range keywords {
					if kw == w {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("expected keyword %q in %v", w, keywords)
				}
			}
			for _, w := range tt.notContains {
				for _, kw := range keywords {
					if kw == w {
						t.Errorf("did not expect stop word %q in %v", w, keywords)
					}
				}
			}
		})
	}
}

func TestParseQueryPlanJSON(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		wantErr bool
		check   func(*QueryPlanResult) error
	}{
		{
			name: "clean JSON",
			raw:  `{"intent":"identity question","keywords":["identity","origin","name","lumi"],"search_type":"semantic","recency_preference":"all_time"}`,
			wantErr: false,
			check: func(r *QueryPlanResult) error {
				if len(r.Keywords) != 4 {
					return errFmt("keywords", 4, len(r.Keywords))
				}
				if r.SearchType != "semantic" {
					return errFmt("search_type", "semantic", r.SearchType)
				}
				return nil
			},
		},
		{
			name: "JSON in markdown fence",
			raw:  "```json\n{\"intent\":\"test\",\"keywords\":[\"foo\"],\"search_type\":\"keyword\",\"recency_preference\":\"recent\"}\n```",
			wantErr: false,
			check: func(r *QueryPlanResult) error {
				if len(r.Keywords) != 1 {
					return errFmt("keywords", 1, len(r.Keywords))
				}
				return nil
			},
		},
		{
			name: "JSON with surrounding text",
			raw:  "Here is the plan:\n{\"intent\":\"test\",\"keywords\":[\"bar\"],\"search_type\":\"hybrid\"}\nDone.",
			wantErr: false,
			check: func(r *QueryPlanResult) error {
				if len(r.Keywords) != 1 {
					return errFmt("keywords", 1, len(r.Keywords))
				}
				return nil
			},
		},
		{
			name:    "no JSON",
			raw:     "I don't know what to search for",
			wantErr: true,
		},
		{
			name:    "empty keywords",
			raw:     `{"intent":"test","keywords":[],"search_type":"keyword"}`,
			wantErr: true,
		},
		{
			name: "defaults for missing fields",
			raw:  `{"intent":"test","keywords":["test"]}`,
			wantErr: false,
			check: func(r *QueryPlanResult) error {
				if r.SearchType != "hybrid" {
					return errFmt("search_type default", "hybrid", r.SearchType)
				}
				if r.RecencyPreference != "all_time" {
					return errFmt("recency default", "all_time", r.RecencyPreference)
				}
				return nil
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := parseQueryPlanJSON(tt.raw)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseQueryPlanJSON() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if err == nil && tt.check != nil {
				if err := tt.check(result); err != nil {
					t.Error(err)
				}
			}
		})
	}
}

func TestHeuristicFallback(t *testing.T) {
	planner := NewQueryPlanner(DefaultQueryPlanConfig())

	result := planner.heuristicFallback("why was I named Lumi?", 1, 0*time.Second)
	if len(result.Keywords) < 1 {
		t.Errorf("expected at least 1 keyword, got %v", result.Keywords)
	}
	if result.SearchType != "semantic" {
		t.Errorf("expected semantic for short query, got %s", result.SearchType)
	}

	result = planner.heuristicFallback("how does the memory search system work?", 1, 0*time.Second)
	if len(result.Keywords) < 3 {
		t.Errorf("expected at least 3 keywords, got %v", result.Keywords)
	}

	// Continuation mode
	result = planner.heuristicFallback("yes that's right", 5, 10*time.Minute)
	if result.RecencyPreference != "recent" {
		t.Errorf("expected recent for continuation, got %s", result.RecencyPreference)
	}
}

func TestQueryPlannerCache(t *testing.T) {

	// Same message should get same cache key
	key1 := messageKey("hello world")
	key2 := messageKey("hello world")
	if key1 != key2 {
		t.Errorf("same message should produce same key")
	}

	// Different messages should produce different keys
	key3 := messageKey("goodbye world")
	if key1 == key3 {
		t.Errorf("different messages should produce different keys")
	}
}

func TestStripThinkingPrefix(t *testing.T) {
	tests := []struct {
		name string
		input string
		want  string
	}{
		{
			name:  "clean JSON",
			input: `{"intent":"test"}`,
			want:  `{"intent":"test"}`,
		},
		{
			name:  "thinking block",
			input: "<tool_call>I need to analyze this...</think>{\"intent\":\"test\"}",
			want:  `{"intent":"test"}`,
		},
		{
			name:  "markdown fence",
			input: "```json\n{\"intent\":\"test\"}\n```",
			want:  `{"intent":"test"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripThinkingPrefix(tt.input)
			if got != tt.want {
				t.Errorf("stripThinkingPrefix() = %q, want %q", got, tt.want)
			}
		})
	}
}

func errFmt(field string, want, got interface{}) error {
	return fmt.Errorf("%s: want %v, got %v", field, want, got)
}