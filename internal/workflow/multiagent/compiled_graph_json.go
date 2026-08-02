// compiled_graph_json.go provides the JSON-serializable snapshot view of a
// CompiledGraph (compiler.go, PR3), for later CLI (`prism graph
// compile`/`inspect`, PR5) and API (PR6) consumption. CompiledGraph itself
// deliberately has no MarshalJSON and no exported fields, to preserve its
// immutability guarantee; building a CompiledGraphView is the only
// sanctioned way to get JSON out of a CompiledGraph.
package multiagent

// CompiledGraphView is the JSON-serializable snapshot of a CompiledGraph.
type CompiledGraphView struct {
	WorkflowID      string         `json:"workflow_id"`
	WorkflowVersion string         `json:"workflow_version"`
	SchemaVersion   string         `json:"schema_version"`
	Fingerprint     string         `json:"fingerprint"`
	EntryNodeID     string         `json:"entry_node_id"`
	Nodes           []CompiledNode `json:"nodes"`
	Edges           []CompiledEdge `json:"edges"`
	TerminalNodeIDs []string       `json:"terminal_node_ids"`
	Loops           []CompiledLoop `json:"loops"`
	Budgets         BudgetLimits   `json:"budgets"`
}

// BuildCompiledGraphView builds the JSON-serializable snapshot of g. Every
// slice is an independent copy (via g's own defensive-copy accessors, plus a
// fresh loop slice built from g's internal loop order), so mutating the
// returned view can never affect g.
func BuildCompiledGraphView(g *CompiledGraph) CompiledGraphView {
	if g == nil {
		return CompiledGraphView{}
	}

	loops := make([]CompiledLoop, 0, len(g.loopOrder))
	for _, kind := range g.loopOrder {
		loops = append(loops, g.loops[kind])
	}

	return CompiledGraphView{
		WorkflowID:      g.WorkflowID(),
		WorkflowVersion: g.WorkflowVersion(),
		SchemaVersion:   g.SchemaVersion(),
		Fingerprint:     g.Fingerprint(),
		EntryNodeID:     g.EntryNodeID(),
		Nodes:           g.Nodes(),
		Edges:           g.Edges(),
		TerminalNodeIDs: g.TerminalNodeIDs(),
		Loops:           loops,
		Budgets:         g.Budgets(),
	}
}
