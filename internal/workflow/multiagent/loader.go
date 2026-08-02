// loader.go reads an authored WorkflowDefinition (schema_types.go) from YAML
// or JSON, producing source-position-aware diagnostics (diagnostics.go,
// position.go) instead of a bare error wherever the problem is about the
// content of the file rather than reading it from disk. It performs no
// business-rule validation (structural/routing/cycle/governance/budget
// rules are the validator's job, PR2) — only parsing, schema-version
// detection, and unknown-field rejection.
package multiagent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// LoadDefinition reads a YAML or JSON workflow definition file (format
// detected by extension: .yaml/.yml decode as YAML, everything else as
// JSON) and returns the decoded definition, a position index for
// diagnostics, any diagnostics produced during loading itself (e.g. an
// unsupported schema version or an unknown field), and a Go error only for
// I/O failures (file not found, permission denied). Schema/decode problems
// are reported as Diagnostics with a nil error, so callers can always
// inspect what went wrong even when loading "fails" from a user's
// perspective.
func LoadDefinition(path string) (WorkflowDefinition, *PositionIndex, Diagnostics, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return WorkflowDefinition{}, nil, nil, err
	}

	format := "json"
	if ext := strings.ToLower(filepath.Ext(path)); ext == ".yaml" || ext == ".yml" {
		format = "yaml"
	}
	return LoadDefinitionBytes(data, format, path)
}

// LoadDefinitionBytes is the format-agnostic core LoadDefinition delegates
// to, taking already-read bytes and an explicit format hint ("yaml" or
// "json") plus a filename used only to attribute diagnostics (it need not
// exist on disk). Exported so later PRs (the validator, compiler, CLI, and
// storage/API layers) can load from non-file sources, e.g. an HTTP request
// body, without needing a real file.
func LoadDefinitionBytes(data []byte, format string, filename string) (WorkflowDefinition, *PositionIndex, Diagnostics, error) {
	switch format {
	case "yaml":
		def, idx, diags := loadYAML(data, filename)
		return def, idx, diags, nil
	case "json":
		def, idx, diags := loadJSON(data, filename)
		return def, idx, diags, nil
	default:
		return WorkflowDefinition{}, nil, nil, fmt.Errorf("multiagent: unsupported format %q (want \"yaml\" or \"json\")", format)
	}
}

// loadYAML decodes the same bytes twice: once into a yaml.Node tree, walked
// to build a *PositionIndex with exact line/column positions; once through
// the internal/workflow/v2-style YAML->generic-map->JSON bridge, so the
// single json:"..." tag set on WorkflowDefinition drives decoding for both
// formats without a duplicate yaml:"..." tag set to keep in sync.
func loadYAML(data []byte, filename string) (WorkflowDefinition, *PositionIndex, Diagnostics) {
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return WorkflowDefinition{}, nil, Diagnostics{syntaxErrorDiagnostic(err, filename)}
	}
	idx := buildYAMLPositionIndex(&root)

	var raw any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return WorkflowDefinition{}, idx, Diagnostics{syntaxErrorDiagnostic(err, filename)}
	}

	normalized, ok := normalizeYAMLValue(raw).(map[string]any)
	if !ok {
		return WorkflowDefinition{}, idx, Diagnostics{invalidDocumentDiagnostic(filename)}
	}

	if diags, ok := checkAPIVersionKind(normalized, filename, idx); !ok {
		return WorkflowDefinition{}, idx, diags
	}

	jsonData, err := json.Marshal(normalized)
	if err != nil {
		return WorkflowDefinition{}, idx, Diagnostics{{
			Severity: SeverityError,
			Rule:     "parse.encode-error",
			Message:  fmt.Sprintf("convert yaml to json: %v", err),
			File:     filename,
		}}
	}

	def, diags := decodeStrictWorkflowJSON(jsonData, filename, idx)
	if diags.HasErrors() {
		return WorkflowDefinition{}, idx, diags
	}

	normalizeDefaults(&def)
	return def, idx, diags
}

// loadJSON decodes the raw bytes twice: once into a generic map (to check
// apiVersion/kind before committing to a full typed decode, and to build a
// best-effort *PositionIndex by scanning the raw bytes — see
// buildJSONPositionIndex), once strictly into WorkflowDefinition.
func loadJSON(data []byte, filename string) (WorkflowDefinition, *PositionIndex, Diagnostics) {
	var raw any
	if err := json.Unmarshal(data, &raw); err != nil {
		return WorkflowDefinition{}, nil, Diagnostics{syntaxErrorDiagnostic(err, filename)}
	}
	idx := buildJSONPositionIndex(data)

	m, ok := raw.(map[string]any)
	if !ok {
		return WorkflowDefinition{}, idx, Diagnostics{invalidDocumentDiagnostic(filename)}
	}

	if diags, ok := checkAPIVersionKind(m, filename, idx); !ok {
		return WorkflowDefinition{}, idx, diags
	}

	def, diags := decodeStrictWorkflowJSON(data, filename, idx)
	if diags.HasErrors() {
		return WorkflowDefinition{}, idx, diags
	}

	normalizeDefaults(&def)
	return def, idx, diags
}

// checkAPIVersionKind inspects a raw decoded document's apiVersion/kind
// fields before any typed decode is attempted. A mismatch on either produces
// a single diagnostic and (false) — the caller must not attempt a partial
// decode of an unrecognized schema version.
func checkAPIVersionKind(m map[string]any, filename string, idx *PositionIndex) (Diagnostics, bool) {
	apiVersion, _ := m["apiVersion"].(string)
	if apiVersion != SchemaAPIVersion {
		pos, _ := idx.Lookup("apiVersion")
		return Diagnostics{{
			Severity: SeverityError,
			Rule:     "schema.unsupported-api-version",
			Message:  fmt.Sprintf("unsupported apiVersion %q (want %q)", apiVersion, SchemaAPIVersion),
			File:     filename,
			Line:     pos.Line,
			Column:   pos.Column,
		}}, false
	}

	kind, _ := m["kind"].(string)
	if kind != SchemaKind {
		pos, _ := idx.Lookup("kind")
		return Diagnostics{{
			Severity: SeverityError,
			Rule:     "schema.unsupported-kind",
			Message:  fmt.Sprintf("unsupported kind %q (want %q)", kind, SchemaKind),
			File:     filename,
			Line:     pos.Line,
			Column:   pos.Column,
		}}, false
	}

	return nil, true
}

// decodeStrictWorkflowJSON decodes jsonData into a WorkflowDefinition with
// DisallowUnknownFields, so an unrecognized field anywhere in the structure
// is a hard error, not a silently dropped value. (Named distinctly from
// structured_output.go's own decodeStrictJSON helper, which decodes agent
// role-output payloads into a different set of target types — same
// technique, unrelated domain, to avoid a same-package name collision.)
func decodeStrictWorkflowJSON(jsonData []byte, filename string, idx *PositionIndex) (WorkflowDefinition, Diagnostics) {
	var def WorkflowDefinition
	dec := json.NewDecoder(bytes.NewReader(jsonData))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&def); err != nil {
		return WorkflowDefinition{}, Diagnostics{diagnosticFromDecodeError(err, filename, idx)}
	}
	return def, nil
}

// diagnosticFromDecodeError converts a stdlib json decode error into a
// Diagnostic. Go's DisallowUnknownFields error message has the form
// `json: unknown field "foo"` and, notably, does not include a full field
// path — only the leaf field name — so position lookup is necessarily
// best-effort (see PositionIndex.lookupFieldSuffix).
func diagnosticFromDecodeError(err error, filename string, idx *PositionIndex) Diagnostic {
	msg := err.Error()
	if field := extractUnknownField(msg); field != "" {
		d := Diagnostic{
			Severity:  SeverityError,
			Rule:      "schema.unknown-field",
			Message:   msg,
			File:      filename,
			FieldPath: field,
		}
		if pos, ok := idx.lookupFieldSuffix(field); ok {
			d.Line, d.Column = pos.Line, pos.Column
		}
		return d
	}
	return Diagnostic{
		Severity: SeverityError,
		Rule:     "schema.decode-error",
		Message:  msg,
		File:     filename,
	}
}

// extractUnknownField parses the field name out of Go's json decoder
// unknown-field error message, e.g. `json: unknown field "foo"`. Returns ""
// if the message does not match that shape.
func extractUnknownField(msg string) string {
	const marker = `unknown field "`
	i := strings.Index(msg, marker)
	if i < 0 {
		return ""
	}
	rest := msg[i+len(marker):]
	end := strings.Index(rest, `"`)
	if end < 0 {
		return ""
	}
	return rest[:end]
}

// syntaxErrorDiagnostic wraps a raw parse error (malformed YAML/JSON) as a
// Diagnostic. No bespoke Go error type is introduced for this per the Phase
// 3 plan's error model: raw syntax errors surface as a
// Diagnostic{Rule: "parse.syntax-error"}, not a typed error.
func syntaxErrorDiagnostic(err error, filename string) Diagnostic {
	return Diagnostic{
		Severity: SeverityError,
		Rule:     "parse.syntax-error",
		Message:  err.Error(),
		File:     filename,
	}
}

// invalidDocumentDiagnostic reports that the decoded document's root was not
// a mapping/object at all (e.g. the file is a YAML list or a bare scalar).
func invalidDocumentDiagnostic(filename string) Diagnostic {
	return Diagnostic{
		Severity: SeverityError,
		Rule:     "schema.invalid-document",
		Message:  "document root must be a mapping/object with apiVersion, kind, metadata, and spec",
		File:     filename,
	}
}

// normalizeYAMLValue converts yaml.v3's map[string]any/map[any]any mix into
// a plain map[string]any tree so the result can be marshaled to JSON. This
// mirrors internal/workflow/v2/config.go's normalizeYAML exactly (that
// function is unexported in package v2, so it cannot be imported directly;
// this is the same technique reused, not a divergent one).
func normalizeYAMLValue(v any) any {
	switch val := v.(type) {
	case map[string]any:
		m := make(map[string]any, len(val))
		for k, item := range val {
			m[k] = normalizeYAMLValue(item)
		}
		return m
	case map[any]any:
		m := make(map[string]any, len(val))
		for k, item := range val {
			m[fmt.Sprintf("%v", k)] = normalizeYAMLValue(item)
		}
		return m
	case []any:
		s := make([]any, len(val))
		for i, item := range val {
			s[i] = normalizeYAMLValue(item)
		}
		return s
	default:
		return v
	}
}

// normalizeDefaults applies identity-level defaults that require no
// cross-field reasoning: metadata.id falls back to metadata.name when empty,
// and each node's type falls back to "role" when empty. Defaulting that
// depends on relationships between fields (e.g. cascading spec.defaults onto
// specific nodes/edges) is deliberately left to the validator (PR2), which
// needs to reason about the fully-authored graph before deciding what to
// fill in; this pass only fills in values with an unambiguous, purely local
// meaning so the validator always sees these normalized.
func normalizeDefaults(def *WorkflowDefinition) {
	if def == nil {
		return
	}
	if strings.TrimSpace(def.Metadata.ID) == "" {
		def.Metadata.ID = def.Metadata.Name
	}
	for i := range def.Spec.Nodes {
		if strings.TrimSpace(def.Spec.Nodes[i].Type) == "" {
			def.Spec.Nodes[i].Type = "role"
		}
	}
}
