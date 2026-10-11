package v2

import (
	"encoding/json"
	"testing"
)

func TestParseToolRequestText(t *testing.T) {
	tool, input, ok := ParseToolRequestText(`sure, let me read it {"type":"tool_request","tool":"read_file","input":{"path":"a.go"}}`)
	if !ok {
		t.Fatal("expected a tool request")
	}
	if tool != "read_file" {
		t.Errorf("tool = %q", tool)
	}
	if input["path"] != "a.go" {
		t.Errorf("input = %+v", input)
	}

	if _, _, ok := ParseToolRequestText("no tool call here"); ok {
		t.Error("expected no tool request")
	}
}

func TestParseCanonicalToolRequestTextAcceptsFormattedAtomicPatch(t *testing.T) {
	patch := "diff --git a/a.go b/a.go\n@@ -1 +1 @@\n-func old() {}\n+func new() { println(`{ok}`) }\n"
	text := "{\n  \"input\": {\n    \"base_sha\": \"6de4579\",\n    \"patch\": " + mustJSON(t, patch) + "\n  },\n  \"tool\": \"apply_patch_proposal\",\n  \"type\": \"tool_request\"\n}"
	tool, input, ok := ParseCanonicalToolRequestText(text)
	if !ok || tool != "apply_patch_proposal" {
		t.Fatalf("parse failed: ok=%v tool=%q", ok, tool)
	}
	if input["patch"] != patch || input["base_sha"] != "6de4579" {
		t.Fatalf("input did not round-trip: %#v", input)
	}
}

func TestParseCanonicalToolRequestTextRejectsNonCanonicalObjects(t *testing.T) {
	inputs := []string{
		`{"schema_version":1,"summary":"role result"}`,
		`{"type":"developer_result","tool":"apply_patch_proposal","input":{"patch":"diff","base_sha":"abc"}}`,
		`{"type":"tool_request","tool":"apply_patch_proposal","input":{"patch":"diff"}}`,
		`{"type":"tool_request","tool":"apply_patch_proposal","input":{"patch":{},"base_sha":"abc"}}`,
		`{"type":"tool_request","tool":"apply_patch_proposal","input":{"patch":"diff","base_sha":"abc","extra":true}}`,
		`{"type":"tool_request","tool":"apply_patch_proposal","input":{"patch":"diff","base_sha":"abc"},"extra":true}`,
	}
	for _, input := range inputs {
		if tool, args, ok := ParseCanonicalToolRequestText(input); ok {
			t.Fatalf("accepted %s as tool=%q input=%#v", input, tool, args)
		}
	}
}

func mustJSON(t *testing.T, value string) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func TestParseFinalText(t *testing.T) {
	content, ok := ParseFinalText(`done: {"type":"final","content":"all set"}`)
	if !ok || content != "all set" {
		t.Fatalf("final parse failed: ok=%v content=%q", ok, content)
	}
	if _, ok := ParseFinalText("still working"); ok {
		t.Error("expected no final block")
	}
	if _, ok := ParseFinalText(`{"type":"final","content":""}`); ok {
		t.Error("empty-content final must remain incomplete")
	}
	if _, ok := ParseFinalText(`analysis mentions {"type":"final","content":`); ok {
		t.Error("malformed embedded marker must remain incomplete")
	}
	if _, ok := ParseFinalText(`{"type":"final","content":{"schema_version":1}}`); ok {
		t.Error("object content must remain incomplete; the final protocol requires a string")
	}
	if _, ok := ParseFinalText(`{"type":"final"}`); ok {
		t.Error("missing final content must remain incomplete")
	}
}

func TestParseCanonicalFinalText(t *testing.T) {
	content, ok := ParseCanonicalFinalText("{\n  \"content\": \" all set \",\n  \"type\": \"final\"\n}")
	if !ok || content != " all set " {
		t.Fatalf("canonical final parse failed: ok=%v content=%q", ok, content)
	}
	for _, input := range []string{
		`{"type":"final"}`,
		`{"type":"final","content":""}`,
		`{"type":"final","content":{"summary":"done"}}`,
		`{"type":"final","content":"done","extra":true}`,
		`{"type":"other","content":"done"}`,
	} {
		if content, ok := ParseCanonicalFinalText(input); ok {
			t.Fatalf("accepted %s with content %q", input, content)
		}
	}
}
