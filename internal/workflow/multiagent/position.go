// position.go maps a decoded workflow definition's fields and node/edge IDs
// back to their source position, so diagnostics (diagnostics.go) can report
// an actionable file:line:column instead of just a message. YAML positions
// come directly from gopkg.in/yaml.v3's already-populated Node.Line/Column
// (no manual recomputation needed); JSON positions are a best-effort
// approximation built by scanning raw bytes, since encoding/json has no
// Node-equivalent (see buildJSONPositionIndex).
package multiagent

import (
	"bytes"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// Position is a 1-indexed line/column location in a source file.
type Position struct {
	Line   int
	Column int
}

// PositionIndex maps field paths (e.g. "spec.nodes[2].agentProfile") and,
// once IDs are known, node/edge IDs, to source positions. It is built once
// per loaded file. All lookup methods are nil-safe: a nil *PositionIndex
// (e.g. when validating an in-memory definition with no source file) simply
// reports every lookup as not found, so callers never need their own nil
// checks.
type PositionIndex struct {
	byFieldPath map[string]Position
	byNodeID    map[string]Position
	byEdgeID    map[string]Position
}

// Lookup returns the position recorded for the given dotted/indexed field
// path, if any.
func (p *PositionIndex) Lookup(fieldPath string) (Position, bool) {
	if p == nil {
		return Position{}, false
	}
	pos, ok := p.byFieldPath[fieldPath]
	return pos, ok
}

// LookupNode returns the position of the node whose id equals nodeID.
func (p *PositionIndex) LookupNode(nodeID string) (Position, bool) {
	if p == nil {
		return Position{}, false
	}
	pos, ok := p.byNodeID[nodeID]
	return pos, ok
}

// LookupEdge returns the position of the edge whose id equals edgeID.
func (p *PositionIndex) LookupEdge(edgeID string) (Position, bool) {
	if p == nil {
		return Position{}, false
	}
	pos, ok := p.byEdgeID[edgeID]
	return pos, ok
}

// lookupFieldSuffix is a best-effort fallback for callers that only have a
// bare field name (e.g. parsed out of a stdlib json decode error, which
// gives no full path) rather than a full field path. It first tries an exact
// match, then falls back to any indexed path ending in "."+field. Map
// iteration order makes the fallback non-deterministic when more than one
// path shares the same suffix; callers should treat it as advisory.
func (p *PositionIndex) lookupFieldSuffix(field string) (Position, bool) {
	if p == nil || field == "" {
		return Position{}, false
	}
	if pos, ok := p.byFieldPath[field]; ok {
		return pos, true
	}
	suffix := "." + field
	for path, pos := range p.byFieldPath {
		if strings.HasSuffix(path, suffix) {
			return pos, true
		}
	}
	return Position{}, false
}

// offsetToLineCol converts a byte offset within data into a 1-indexed
// line/column pair by scanning for newlines. Used for JSON's best-effort
// position tracking, where the stdlib decoder has no Node-equivalent to read
// positions from directly.
func offsetToLineCol(data []byte, offset int) (line, col int) {
	if offset < 0 {
		offset = 0
	}
	if offset > len(data) {
		offset = len(data)
	}
	line = 1
	lastNewline := -1
	for i := 0; i < offset; i++ {
		if data[i] == '\n' {
			line++
			lastNewline = i
		}
	}
	return line, offset - lastNewline
}

// buildYAMLPositionIndex walks a decoded yaml.Node tree and records the
// position of the document's top-level keys, metadata's keys, spec's
// top-level keys, and every field within each spec.nodes[N]/spec.edges[N]
// element. yaml.Node's Line/Column fields are already populated after
// yaml.Unmarshal, so this only reads them — it never recomputes a position.
//
// Node/edge ID indexing does not require a second pass correlating against
// the typed struct decode: workflow/node/edge IDs are plain YAML scalar
// strings, so the "id" key's value is read directly off the raw tree in the
// same walk that records field-path positions.
func buildYAMLPositionIndex(root *yaml.Node) *PositionIndex {
	idx := &PositionIndex{
		byFieldPath: make(map[string]Position),
		byNodeID:    make(map[string]Position),
		byEdgeID:    make(map[string]Position),
	}
	if root == nil {
		return idx
	}

	docRoot := root
	if root.Kind == yaml.DocumentNode && len(root.Content) > 0 {
		docRoot = root.Content[0]
	}
	if docRoot.Kind != yaml.MappingNode {
		return idx
	}

	indexYAMLMappingShallow(docRoot, "", idx.byFieldPath)

	if _, metaNode := findYAMLMapEntry(docRoot, "metadata"); metaNode != nil && metaNode.Kind == yaml.MappingNode {
		indexYAMLMappingShallow(metaNode, "metadata", idx.byFieldPath)
	}

	_, specNode := findYAMLMapEntry(docRoot, "spec")
	if specNode == nil || specNode.Kind != yaml.MappingNode {
		return idx
	}
	indexYAMLMappingShallow(specNode, "spec", idx.byFieldPath)

	indexYAMLSequence(specNode, "nodes", "spec.nodes", idx.byNodeID, idx.byFieldPath)
	indexYAMLSequence(specNode, "edges", "spec.edges", idx.byEdgeID, idx.byFieldPath)

	return idx
}

// indexYAMLMappingShallow records the position of each top-level key's value
// within mapping, keyed by "<prefix>.<key>" (or just "<key>" when prefix is
// empty). It does not recurse — callers index nested mappings explicitly so
// the depth of indexing stays intentional rather than unbounded.
func indexYAMLMappingShallow(mapping *yaml.Node, prefix string, byFieldPath map[string]Position) {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		keyNode := mapping.Content[i]
		valNode := mapping.Content[i+1]
		path := keyNode.Value
		if prefix != "" {
			path = prefix + "." + keyNode.Value
		}
		byFieldPath[path] = Position{Line: valNode.Line, Column: valNode.Column}
	}
}

// indexYAMLSequence walks specNode's key-named sequence (e.g. "nodes" or
// "edges"), recording each element's own position (keyed by
// "<fieldPrefix>[i]"), every field within that element (keyed by
// "<fieldPrefix>[i].<field>"), and — when the element has an "id" field —
// that element's position keyed by the ID's own string value into byID.
func indexYAMLSequence(specNode *yaml.Node, key, fieldPrefix string, byID map[string]Position, byFieldPath map[string]Position) {
	_, seqNode := findYAMLMapEntry(specNode, key)
	if seqNode == nil || seqNode.Kind != yaml.SequenceNode {
		return
	}
	for i, item := range seqNode.Content {
		if item.Kind != yaml.MappingNode {
			continue
		}
		itemPath := fmt.Sprintf("%s[%d]", fieldPrefix, i)
		itemPos := Position{Line: item.Line, Column: item.Column}
		byFieldPath[itemPath] = itemPos

		for j := 0; j+1 < len(item.Content); j += 2 {
			keyNode := item.Content[j]
			valNode := item.Content[j+1]
			byFieldPath[itemPath+"."+keyNode.Value] = Position{Line: valNode.Line, Column: valNode.Column}
			if keyNode.Value == "id" && valNode.Kind == yaml.ScalarNode {
				byID[valNode.Value] = itemPos
			}
		}
	}
}

// findYAMLMapEntry returns the key node and value node for key in a mapping
// node, or (nil, nil) if absent. Mapping content alternates key,value pairs
// (the same layout internal/orchestrator/configedit.go's findMapEntry
// assumes for the same reason: that is how yaml.v3 represents a MappingNode).
func findYAMLMapEntry(mapping *yaml.Node, key string) (*yaml.Node, *yaml.Node) {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i], mapping.Content[i+1]
		}
	}
	return nil, nil
}

// buildJSONPositionIndex builds a best-effort PositionIndex for JSON input.
// encoding/json has no yaml.Node equivalent with populated line/column info,
// so this scans the raw bytes directly: it locates the top-level
// "apiVersion"/"kind" keys, and for spec.nodes/spec.edges it splits the
// array into its top-level object elements (depth-tracking braces while
// skipping over string contents) and records each element's start position,
// plus the position of any "id" key found within it, indexed by that key's
// string value.
//
// Positions are therefore approximate: they mark where a key's text starts
// in the byte stream, not a value's exact position, and are not defended
// against pathological input (e.g. a string value that itself contains the
// literal text `"id"`). YAML remains the primary authored format and gets
// exact positions from yaml.Node (see buildYAMLPositionIndex); this JSON
// path exists so the loader always returns *some* position information
// regardless of input format, which is acceptable per the Phase 3 plan.
func buildJSONPositionIndex(data []byte) *PositionIndex {
	idx := &PositionIndex{
		byFieldPath: make(map[string]Position),
		byNodeID:    make(map[string]Position),
		byEdgeID:    make(map[string]Position),
	}

	if off := bytes.Index(data, []byte(`"apiVersion"`)); off >= 0 {
		line, col := offsetToLineCol(data, off)
		idx.byFieldPath["apiVersion"] = Position{Line: line, Column: col}
	}
	if off := bytes.Index(data, []byte(`"kind"`)); off >= 0 {
		line, col := offsetToLineCol(data, off)
		idx.byFieldPath["kind"] = Position{Line: line, Column: col}
	}

	indexJSONArray(data, `"nodes"`, "spec.nodes", idx.byNodeID, idx.byFieldPath)
	indexJSONArray(data, `"edges"`, "spec.edges", idx.byEdgeID, idx.byFieldPath)

	return idx
}

type jsonByteSpan struct{ start, end int }

// indexJSONArray locates the array introduced by keyLiteral (e.g. `"nodes"`)
// and records the position of each of its top-level object elements, plus
// each element's "id" value (when present) indexed by byID.
func indexJSONArray(data []byte, keyLiteral, fieldPrefix string, byID map[string]Position, byFieldPath map[string]Position) {
	keyOff := bytes.Index(data, []byte(keyLiteral))
	if keyOff < 0 {
		return
	}
	relArrStart := bytes.IndexByte(data[keyOff:], '[')
	if relArrStart < 0 {
		return
	}
	arrStart := keyOff + relArrStart

	for i, span := range splitJSONArrayObjects(data, arrStart) {
		itemPath := fmt.Sprintf("%s[%d]", fieldPrefix, i)
		line, col := offsetToLineCol(data, span.start)
		itemPos := Position{Line: line, Column: col}
		byFieldPath[itemPath] = itemPos

		idKeyOff := bytes.Index(data[span.start:span.end], []byte(`"id"`))
		if idKeyOff < 0 {
			continue
		}
		valueOff := span.start + idKeyOff + len(`"id"`)
		if id, ok := extractJSONStringValue(data, valueOff); ok {
			byID[id] = itemPos
		}
	}
}

// splitJSONArrayObjects returns the [start,end) byte spans of each top-level
// object element in a JSON array literal beginning at arrStart (the index of
// the array's '[' character). It depth-tracks braces while skipping over
// string contents (including escaped quotes) so braces inside string values
// do not confuse the split.
func splitJSONArrayObjects(data []byte, arrStart int) []jsonByteSpan {
	var spans []jsonByteSpan
	depth := 0
	inString := false
	escaped := false
	objStart := 0

	for i := arrStart; i < len(data); i++ {
		c := data[i]
		if inString {
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '{':
			if depth == 0 {
				objStart = i
			}
			depth++
		case '}':
			depth--
			if depth == 0 {
				spans = append(spans, jsonByteSpan{start: objStart, end: i + 1})
			}
		case ']':
			if depth == 0 {
				return spans
			}
		}
	}
	return spans
}

// extractJSONStringValue reads a JSON string literal beginning at or after
// offset off (skipping whitespace and a leading ':'), returning its raw
// (not-unescaped) contents. Sufficient for simple identifiers such as
// workflow/node/edge IDs, which are not expected to contain escape
// sequences.
func extractJSONStringValue(data []byte, off int) (string, bool) {
	i := off
	for i < len(data) {
		switch data[i] {
		case ' ', '\t', '\n', '\r', ':':
			i++
			continue
		}
		break
	}
	if i >= len(data) || data[i] != '"' {
		return "", false
	}
	i++
	start := i
	for i < len(data) {
		if data[i] == '\\' {
			i += 2
			continue
		}
		if data[i] == '"' {
			return string(data[start:i]), true
		}
		i++
	}
	return "", false
}
