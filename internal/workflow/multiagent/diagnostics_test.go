package multiagent

import (
	"strings"
	"testing"
)

func TestSuggestClosestExactMatchExcluded(t *testing.T) {
	_, ok := suggestClosest("planner", []string{"planner"})
	if ok {
		t.Fatal("exact match should not be suggested (nothing to suggest)")
	}
}

func TestSuggestClosestTypoSuggested(t *testing.T) {
	got, ok := suggestClosest("plannre", []string{"planner", "developer", "tester", "reviewer"})
	if !ok {
		t.Fatal("expected a close suggestion for a typo")
	}
	if got != "planner" {
		t.Errorf("suggestion = %q, want %q", got, "planner")
	}
}

func TestSuggestClosestNoMatchWithinThreshold(t *testing.T) {
	_, ok := suggestClosest("zzzzzzzzzz", []string{"planner", "developer", "tester", "reviewer"})
	if ok {
		t.Fatal("expected no suggestion when nothing is close enough")
	}
}

func TestSuggestClosestNoCandidates(t *testing.T) {
	_, ok := suggestClosest("planner", nil)
	if ok {
		t.Fatal("expected no suggestion when there are no candidates")
	}
}

func TestLevenshteinDistance(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"", "", 0},
		{"abc", "abc", 0},
		{"abc", "", 3},
		{"kitten", "sitting", 3},
		{"planner", "plannre", 2},
	}
	for _, tc := range cases {
		got := levenshteinDistance(tc.a, tc.b)
		if got != tc.want {
			t.Errorf("levenshteinDistance(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestDiagnosticStringWithPosition(t *testing.T) {
	d := Diagnostic{
		Severity: SeverityError,
		Rule:     "schema.unknown-field",
		Message:  `unknown field "foo"`,
		File:     "workflow.yaml",
		Line:     12,
		Column:   5,
	}
	got := d.String()
	want := `workflow.yaml:12:5: schema.unknown-field: unknown field "foo"`
	if got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}

func TestDiagnosticStringWithoutPosition(t *testing.T) {
	d := Diagnostic{
		Severity: SeverityError,
		Rule:     "schema.unsupported-api-version",
		Message:  `unsupported apiVersion "v1"`,
	}
	got := d.String()
	want := `schema.unsupported-api-version: unsupported apiVersion "v1"`
	if got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}

func TestDiagnosticStringWithSuggestion(t *testing.T) {
	d := Diagnostic{
		Rule:       "routing.unknown-node",
		Message:    `edge references unknown node "developr"`,
		Suggestion: "developer",
	}
	got := d.String()
	if !strings.Contains(got, `did you mean "developer"?`) {
		t.Errorf("String() = %q, want it to include the suggestion", got)
	}
}

func TestDiagnosticStringFileOnlyNoLine(t *testing.T) {
	d := Diagnostic{
		Rule:    "parse.syntax-error",
		Message: "unexpected token",
		File:    "workflow.yaml",
	}
	got := d.String()
	want := "workflow.yaml: parse.syntax-error: unexpected token"
	if got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}

func TestDiagnosticsHasErrors(t *testing.T) {
	warningsOnly := Diagnostics{{Severity: SeverityWarning, Rule: "x", Message: "y"}}
	if warningsOnly.HasErrors() {
		t.Error("warnings-only Diagnostics should not report HasErrors")
	}

	withError := Diagnostics{
		{Severity: SeverityWarning, Rule: "x", Message: "y"},
		{Severity: SeverityError, Rule: "z", Message: "w"},
	}
	if !withError.HasErrors() {
		t.Error("Diagnostics containing an error should report HasErrors")
	}
}

func TestDiagnosticsErrorJoinsEntries(t *testing.T) {
	d := Diagnostics{
		{Rule: "a", Message: "first"},
		{Rule: "b", Message: "second"},
	}
	got := d.Error()
	if !strings.Contains(got, "a: first") || !strings.Contains(got, "b: second") {
		t.Errorf("Error() = %q, want it to contain both entries", got)
	}
	if strings.Count(got, "\n") != 1 {
		t.Errorf("Error() = %q, want exactly one newline joining two entries", got)
	}
}

func TestDiagnosticsEmptyError(t *testing.T) {
	var d Diagnostics
	if d.Error() != "" {
		t.Errorf("Error() on empty Diagnostics = %q, want empty string", d.Error())
	}
	if d.HasErrors() {
		t.Error("empty Diagnostics should not report HasErrors")
	}
}
