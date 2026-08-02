package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/emaharmony/prism/internal/workflow/multiagent"
)

// executeGraphInspect implements
// `prism graph inspect --file <path> [--format text|mermaid|json]`.
//
// For PR5, --file is the ONLY supported mode: the plan also describes a
// registry-backed --workflow/--version mode, but that requires PR6's
// DefinitionStore, which does not exist yet. Rather than stub out
// --workflow/--version flags that silently do nothing, omitting --file
// entirely is a clear, explicit error naming the reason.
func executeGraphInspect(args []string) error {
	fs := flag.NewFlagSet("graph inspect", flag.ExitOnError)
	file := fs.String("file", "", "Path to a local workflow definition file to inspect")
	format := fs.String("format", "text", "Output format: text|mermaid|json")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*file) == "" {
		return errors.New(
			"prism graph inspect requires --file <path> in this release; " +
				"registry-backed --workflow/--version inspection is not available " +
				"until the definition registry ships in a later release",
		)
	}

	graph, diags, err := loadAndCompileGraph(*file)
	if err != nil {
		printDiagnostics(os.Stderr, diags)
		return err
	}

	switch *format {
	case "text":
		return renderGraphText(os.Stdout, graph, diags, *file)
	case "mermaid":
		return renderGraphMermaid(os.Stdout, graph)
	case "json":
		return writeJSON(os.Stdout, multiagent.BuildCompiledGraphView(graph))
	default:
		return fmt.Errorf("unknown --format %q (want text|mermaid|json)", *format)
	}
}

// renderGraphText prints a human-readable summary: entry node, nodes (with
// role/kind/agent profile/max visits/local iterations), transitions grouped
// by source node (with priority/label when set), loop edges with their
// traversal limits and exhaustion behavior, global budgets,
// approval/validation gates per node, unreachable nodes, and the
// fingerprint.
//
// Unreachable-node detection reuses the validator's own
// "structural.unreachable-node" warning diagnostics (already present in
// diags — loadAndCompileGraph returns Compile's full diagnostics list, not
// just its errors) rather than recomputing graph reachability
// independently here: the validator is the single source of truth for that
// analysis, and re-deriving it a second way in the CLI risks the two
// disagreeing.
func renderGraphText(w io.Writer, graph *multiagent.CompiledGraph, diags multiagent.Diagnostics, sourcePath string) error {
	fmt.Fprintf(w, "Workflow: %s (version %s)\n", graph.WorkflowID(), graph.WorkflowVersion())
	fmt.Fprintf(w, "Source: %s\n", sourcePath)
	fmt.Fprintf(w, "Schema version: %s\n", graph.SchemaVersion())
	fmt.Fprintf(w, "Fingerprint: %s\n", graph.Fingerprint())
	fmt.Fprintf(w, "Entry node: %s\n\n", graph.EntryNodeID())

	budgets := graph.Budgets()

	fmt.Fprintln(w, "Nodes:")
	for _, n := range graph.Nodes() {
		if n.Kind == "terminal" {
			fmt.Fprintf(w, "  %s [terminal] condition=%s\n", n.ID, n.TerminalCondition)
			continue
		}
		fmt.Fprintf(w, "  %s [role] role=%s agentProfile=%s\n", n.ID, n.Role, valueOrDash(n.RoleConfig.AgentRef))
		if maxVisits, ok := budgets.MaxVisitsPerRole[n.Role]; ok {
			fmt.Fprintf(w, "      maxVisits=%s\n", limitString(maxVisits))
		}
		fmt.Fprintf(w, "      maxLocalIterations=%s tokenBudget=%s timeBudget=%s\n",
			limitString(n.RoleConfig.MaxLocalIterations), limitString(n.RoleConfig.TokenBudget), durationString(n.RoleConfig.TimeBudget))
		if n.RoleConfig.Approval.Required {
			fmt.Fprintf(w, "      approval: required, approvers=%v\n", n.RoleConfig.Approval.Approvers)
		}
		if len(n.RoleConfig.ValidationProfiles) > 0 {
			fmt.Fprintf(w, "      validation profiles=%v\n", n.RoleConfig.ValidationProfiles)
		}
	}

	fmt.Fprintln(w, "\nTransitions:")
	edges := graph.Edges()
	var fromOrder []string
	byFrom := map[string][]multiagent.CompiledEdge{}
	for _, e := range edges {
		key := string(e.From)
		if _, ok := byFrom[key]; !ok {
			fromOrder = append(fromOrder, key)
		}
		byFrom[key] = append(byFrom[key], e)
	}
	for _, from := range fromOrder {
		fmt.Fprintf(w, "  %s:\n", from)
		for _, e := range byFrom[from] {
			dest := string(e.To)
			if e.Terminal != "" {
				dest = "terminal:" + string(e.Terminal)
			}
			line := fmt.Sprintf("    -> %s on %q", dest, e.Outcome)
			if e.Priority != 0 {
				line += fmt.Sprintf(" (priority %d)", e.Priority)
			}
			if e.Label != "" {
				line += fmt.Sprintf(" [%s]", e.Label)
			}
			fmt.Fprintln(w, line)
		}
	}

	fmt.Fprintln(w, "\nLoops:")
	hasLoop := false
	for _, e := range edges {
		loop, ok := graph.LoopFor(multiagent.ResolvedTransition{From: e.From, Outcome: e.Outcome, To: e.To, Terminal: e.Terminal})
		if !ok {
			continue
		}
		hasLoop = true
		exhausted := string(loop.OnExhausted)
		if loop.OnExhausted == multiagent.LoopExhaustedRouteTo {
			exhausted += " -> " + string(loop.RouteTo)
		}
		fmt.Fprintf(w, "  %s: maxTraversals=%s onExhausted=%s\n", loop.EdgeID, limitString(loop.MaxTraversals), exhausted)
	}
	if !hasLoop {
		fmt.Fprintln(w, "  (none)")
	}

	fmt.Fprintln(w, "\nBudgets:")
	fmt.Fprintf(w, "  maxTransitions=%s maxTokens=%s maxLocalIterations=%s maxRetries=%s maxDuration=%s\n",
		limitString(budgets.MaxTransitions), limitString(budgets.MaxTokens),
		limitString(budgets.MaxLocalIterations), limitString(budgets.MaxRetries), durationString(budgets.MaxDuration))

	fmt.Fprintln(w, "\nUnreachable nodes:")
	foundUnreachable := false
	for _, d := range diags {
		if d.Rule != "structural.unreachable-node" {
			continue
		}
		foundUnreachable = true
		fmt.Fprintf(w, "  %s: %s\n", d.NodeID, d.Message)
	}
	if !foundUnreachable {
		fmt.Fprintln(w, "  (none)")
	}

	return nil
}

// renderGraphMermaid emits a `flowchart TD` Mermaid block: one
// `nodeID["Display Label"]` line per node, one
// `nodeIDA -->|outcome label| nodeIDB` line per non-loop edge, and a dashed
// `-.->` arrow with an " (loop)" suffix on the label for loop-bearing
// edges.
//
// Node/edge stable IDs (e.g. "node:terminal:completed",
// "edge:tester:tests_failed") contain characters Mermaid does not accept in
// a bare node identifier (":"), so mermaidID sanitizes them for use as the
// Mermaid-internal identifier while the human-readable label keeps the
// node's real display name/role/terminal condition.
func renderGraphMermaid(w io.Writer, graph *multiagent.CompiledGraph) error {
	nodes := graph.Nodes()
	nodeIDByRole := make(map[multiagent.Role]string, len(nodes))
	nodeIDByTerminal := make(map[multiagent.TerminalCondition]string, len(nodes))

	fmt.Fprintln(w, "```mermaid")
	fmt.Fprintln(w, "flowchart TD")
	for _, n := range nodes {
		if n.Kind == "terminal" {
			nodeIDByTerminal[n.TerminalCondition] = n.ID
		} else {
			nodeIDByRole[n.Role] = n.ID
		}
		label := n.DisplayName
		if label == "" {
			if n.Kind == "terminal" {
				label = string(n.TerminalCondition)
			} else {
				label = string(n.Role)
			}
		}
		fmt.Fprintf(w, "  %s[%q]\n", mermaidID(n.ID), label)
	}

	for _, e := range graph.Edges() {
		srcID, ok := nodeIDByRole[e.From]
		if !ok {
			continue
		}
		var dstID string
		if e.Terminal != "" {
			dstID, ok = nodeIDByTerminal[e.Terminal]
		} else {
			dstID, ok = nodeIDByRole[e.To]
		}
		if !ok {
			continue
		}

		_, isLoop := graph.LoopFor(multiagent.ResolvedTransition{From: e.From, Outcome: e.Outcome, To: e.To, Terminal: e.Terminal})
		arrow := "-->"
		label := string(e.Outcome)
		if isLoop {
			arrow = "-.->"
			label += " (loop)"
		}
		fmt.Fprintf(w, "  %s %s|%s| %s\n", mermaidID(srcID), arrow, label, mermaidID(dstID))
	}
	fmt.Fprintln(w, "```")
	return nil
}

func mermaidID(id string) string {
	return strings.NewReplacer(":", "_", ".", "_", "-", "_").Replace(id)
}

func valueOrDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}
