package multiagent

import (
	"os"
	"strings"
	"testing"
)

// TestLoadDefinitionValidYAMLPositions is the core "position tracking
// actually works" proof: it asserts exact line/column for several known
// fields in testdata/schema/valid/software-delivery.yaml. The expected
// values were captured directly from gopkg.in/yaml.v3's populated
// Node.Line/Node.Column for this exact fixture (not hand-counted), so a
// change to the fixture's layout requires regenerating these numbers.
func TestLoadDefinitionValidYAMLPositions(t *testing.T) {
	def, idx, diags, err := LoadDefinition("testdata/schema/valid/software-delivery.yaml")
	if err != nil {
		t.Fatalf("LoadDefinition: %v", err)
	}
	if diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if def.Metadata.Name != "software-delivery" {
		t.Fatalf("metadata.name = %q", def.Metadata.Name)
	}

	cases := []struct {
		name   string
		lookup func() (Position, bool)
		want   Position
	}{
		{
			name:   "apiVersion field path",
			lookup: func() (Position, bool) { return idx.Lookup("apiVersion") },
			want:   Position{Line: 1, Column: 13},
		},
		{
			name:   "kind field path",
			lookup: func() (Position, bool) { return idx.Lookup("kind") },
			want:   Position{Line: 2, Column: 7},
		},
		{
			name:   "node planner by ID",
			lookup: func() (Position, bool) { return idx.LookupNode("planner") },
			want:   Position{Line: 14, Column: 7},
		},
		{
			name:   "node tester by ID",
			lookup: func() (Position, bool) { return idx.LookupNode("tester") },
			want:   Position{Line: 26, Column: 7},
		},
		{
			name:   "node planner agentProfile field path",
			lookup: func() (Position, bool) { return idx.Lookup("spec.nodes[0].agentProfile") },
			want:   Position{Line: 17, Column: 21},
		},
		{
			name:   "edge tester-to-dev by ID",
			lookup: func() (Position, bool) { return idx.LookupEdge("tester-to-dev") },
			want:   Position{Line: 57, Column: 7},
		},
		{
			name:   "edge reviewer-to-completed by ID",
			lookup: func() (Position, bool) { return idx.LookupEdge("reviewer-to-completed") },
			want:   Position{Line: 78, Column: 7},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := tc.lookup()
			if !ok {
				t.Fatalf("lookup missed, want %+v", tc.want)
			}
			if got != tc.want {
				t.Fatalf("position = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestLoadDefinitionValidJSON(t *testing.T) {
	def, idx, diags, err := LoadDefinition("testdata/schema/valid/software-delivery.json")
	if err != nil {
		t.Fatalf("LoadDefinition: %v", err)
	}
	if diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if def.Metadata.Name != "software-delivery" {
		t.Fatalf("metadata.name = %q", def.Metadata.Name)
	}
	if len(def.Spec.Nodes) != 6 {
		t.Fatalf("nodes = %d, want 6", len(def.Spec.Nodes))
	}

	// JSON positions are documented as best-effort/approximate. We only
	// assert that a position was found at all (non-zero), not an exact
	// value, matching the loader's documented limitation for this format.
	if pos, ok := idx.LookupNode("planner"); !ok || pos.Line <= 0 {
		t.Errorf("LookupNode(planner) = %+v, ok=%v; want some positive line", pos, ok)
	}
	if pos, ok := idx.LookupEdge("tester-to-dev"); !ok || pos.Line <= 0 {
		t.Errorf("LookupEdge(tester-to-dev) = %+v, ok=%v; want some positive line", pos, ok)
	}
	if pos, ok := idx.Lookup("apiVersion"); !ok || pos.Line <= 0 {
		t.Errorf("Lookup(apiVersion) = %+v, ok=%v; want some positive line", pos, ok)
	}
}

func TestLoadDefinitionBadAPIVersion(t *testing.T) {
	def, idx, diags, err := LoadDefinition("testdata/schema/invalid/bad-api-version.yaml")
	if err != nil {
		t.Fatalf("LoadDefinition returned a Go error for a content problem: %v", err)
	}
	if !diags.HasErrors() {
		t.Fatal("expected diagnostics for unsupported apiVersion")
	}
	if len(diags) != 1 {
		t.Fatalf("expected exactly one diagnostic (no partial decode attempted), got %d: %v", len(diags), diags)
	}
	d := diags[0]
	if d.Rule != "schema.unsupported-api-version" {
		t.Errorf("rule = %q, want schema.unsupported-api-version", d.Rule)
	}
	if d.Severity != SeverityError {
		t.Errorf("severity = %q, want error", d.Severity)
	}
	if d.Line != 1 {
		t.Errorf("line = %d, want 1", d.Line)
	}
	if def.Kind != "" {
		t.Errorf("expected zero-value definition on rejection, got %+v", def)
	}
	_ = idx
}

func TestLoadDefinitionUnknownFieldYAML(t *testing.T) {
	_, _, diags, err := LoadDefinition("testdata/schema/invalid/unknown-field.yaml")
	if err != nil {
		t.Fatalf("LoadDefinition returned a Go error for a content problem: %v", err)
	}
	if !diags.HasErrors() {
		t.Fatal("expected diagnostics for unknown field")
	}
	found := false
	for _, d := range diags {
		if d.Rule == "schema.unknown-field" {
			found = true
			if !strings.Contains(d.Message, "notARealField") {
				t.Errorf("message = %q, want it to mention notARealField", d.Message)
			}
		}
	}
	if !found {
		t.Fatalf("expected a schema.unknown-field diagnostic, got %v", diags)
	}
}

func TestLoadDefinitionUnknownFieldJSON(t *testing.T) {
	_, _, diags, err := LoadDefinition("testdata/schema/invalid/unknown-field.json")
	if err != nil {
		t.Fatalf("LoadDefinition returned a Go error for a content problem: %v", err)
	}
	if !diags.HasErrors() {
		t.Fatal("expected diagnostics for unknown field")
	}
	found := false
	for _, d := range diags {
		if d.Rule == "schema.unknown-field" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a schema.unknown-field diagnostic, got %v", diags)
	}
}

func TestLoadDefinitionSyntaxError(t *testing.T) {
	_, _, diags, err := LoadDefinition("testdata/schema/invalid/syntax-error.yaml")
	if err != nil {
		t.Fatalf("LoadDefinition returned a Go error for a content problem: %v", err)
	}
	if !diags.HasErrors() {
		t.Fatal("expected diagnostics for a YAML syntax error")
	}
	if diags[0].Rule != "parse.syntax-error" {
		t.Errorf("rule = %q, want parse.syntax-error", diags[0].Rule)
	}
}

func TestLoadDefinitionFileNotFound(t *testing.T) {
	_, _, _, err := LoadDefinition("testdata/schema/does-not-exist.yaml")
	if err == nil {
		t.Fatal("expected a Go error for a missing file (I/O failure), got nil")
	}
	if !os.IsNotExist(err) {
		t.Errorf("expected an os.IsNotExist error, got %v", err)
	}
}

func TestLoadDefinitionBytesUnsupportedFormat(t *testing.T) {
	_, _, _, err := LoadDefinitionBytes([]byte("{}"), "toml", "x.toml")
	if err == nil {
		t.Fatal("expected an error for an unsupported format hint")
	}
}

func TestNormalizeDefaultsIDFallsBackToName(t *testing.T) {
	src := `apiVersion: prism.dev/v1alpha1
kind: MultiAgentWorkflow
metadata:
  name: my-workflow
  version: "1.0.0"
spec:
  entryNode: only
  budgets: {}
  nodes:
    - id: only
  edges: []
`
	def, _, diags, err := LoadDefinitionBytes([]byte(src), "yaml", "inline.yaml")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if def.Metadata.ID != "my-workflow" {
		t.Errorf("metadata.id = %q, want fallback to name %q", def.Metadata.ID, "my-workflow")
	}
	if def.Spec.Nodes[0].Type != "role" {
		t.Errorf("node type = %q, want default %q", def.Spec.Nodes[0].Type, "role")
	}
}

func TestOffsetToLineCol(t *testing.T) {
	data := []byte("abc\ndef\nghi")
	cases := []struct {
		offset   int
		wantLine int
		wantCol  int
	}{
		{0, 1, 1},
		{2, 1, 3},
		{4, 2, 1},
		{7, 2, 4},
		{8, 3, 1},
	}
	for _, tc := range cases {
		line, col := offsetToLineCol(data, tc.offset)
		if line != tc.wantLine || col != tc.wantCol {
			t.Errorf("offsetToLineCol(%d) = (%d,%d), want (%d,%d)", tc.offset, line, col, tc.wantLine, tc.wantCol)
		}
	}
}

func TestPositionIndexNilSafe(t *testing.T) {
	var idx *PositionIndex
	if _, ok := idx.Lookup("x"); ok {
		t.Error("Lookup on nil index should report not found")
	}
	if _, ok := idx.LookupNode("x"); ok {
		t.Error("LookupNode on nil index should report not found")
	}
	if _, ok := idx.LookupEdge("x"); ok {
		t.Error("LookupEdge on nil index should report not found")
	}
}
