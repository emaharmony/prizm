package v2

import "testing"

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
