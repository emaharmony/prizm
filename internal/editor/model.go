// Package editor provides the visual workflow editor model and config round-trip.
//
// V25: Visual Workflow Editor
//
// The editor model represents Prizm's agent configuration as a directed graph
// of nodes (agents, approvals, processes) and edges (delegation, review,
// approval, event flows). Changes to the graph write back to prizm.yaml.
package editor

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/emaharmony/prizm/internal/orchestrator"
	"gopkg.in/yaml.v3"
)

// Position represents a node's position on the canvas.
type Position struct {
	X int `json:"x"`
	Y int `json:"y"`
}

// EditorNode represents a node in the workflow editor.
type EditorNode struct {
	ID            string   `json:"id"`
	Type          string   `json:"type"` // "agent", "approval", "process"
	Label         string   `json:"label"`
	Role          string   `json:"role"` // lead, coder, researcher, orchestrator
	Model         string   `json:"model"`
	Provider      string   `json:"provider"`
	Primary       bool     `json:"primary"`
	Capabilities  []string `json:"capabilities"`
	Subscriptions []string `json:"subscriptions"`
	Context       []string `json:"context"`
	Position      Position `json:"position"`
}

// EditorEdge represents a connection between two nodes — a flow along which one
// system/agent communicates with another. An edge can carry an Action: the thing
// that happens when the flow is traversed (e.g. a registered action key like
// "remembrance.gate.extract", or a free-form label). This is what makes the canvas
// a behavior diagram, not just a topology picture.
type EditorEdge struct {
	ID     string `json:"id"`
	From   string `json:"from"`             // source node ID
	To     string `json:"to"`               // target node ID
	Type   string `json:"type"`             // "delegation", "review", "approval", "event"
	Label  string `json:"label"`            // optional display label
	Style  string `json:"style"`            // "solid", "dashed", "dotted", "bold"
	Action string `json:"action,omitempty"` // optional action invoked along this flow
}

// EditorState is the full state of the workflow editor.
type EditorState struct {
	Nodes []EditorNode `json:"nodes"`
	Edges []EditorEdge `json:"edges"`
}

// ConfigToEditorState converts Prizm agent config into an EditorState.
// It creates nodes for each agent and edges based on subscriptions.
func ConfigToEditorState(config *orchestrator.Config) *EditorState {
	state := &EditorState{}

	// Auto-layout positions
	cols := 3
	if len(config.Agents) > 6 {
		cols = 4
	}

	for i, agent := range config.Agents {
		col := i % cols
		row := i / cols
		x := 100 + col*250
		y := 100 + row*180

		node := EditorNode{
			ID:            agent.ID,
			Type:          "agent",
			Label:         strings.ToUpper(agent.ID),
			Role:          agent.Role,
			Model:         agent.Model,
			Provider:      agent.Provider,
			Primary:       agent.Primary,
			Capabilities:  agent.Capabilities,
			Subscriptions: agent.Subscriptions,
			Context:       agent.Context,
			Position:      Position{X: x, Y: y},
		}
		state.Nodes = append(state.Nodes, node)

		// Create delegation edges from subscriptions
		for _, sub := range agent.Subscriptions {
			// Parse subscription: "mango.task.completed" → from=mango
			parts := strings.SplitN(sub, ".", 2)
			fromAgent := parts[0]
			edgeType := "event"
			style := "solid"
			label := ""

			// Classify edge type based on subscription pattern
			if strings.Contains(sub, ".task.") || strings.Contains(sub, ".approval.") {
				edgeType = "delegation"
				style = "solid"
				label = sub
			} else if strings.Contains(sub, ".agent.output") {
				edgeType = "review"
				style = "dashed"
				label = "review"
			}

			state.Edges = append(state.Edges, EditorEdge{
				ID:    fmt.Sprintf("%s-%s-%s", fromAgent, agent.ID, edgeType),
				From:  fromAgent,
				To:    agent.ID,
				Type:  edgeType,
				Label: label,
				Style: style,
			})
		}

		// Primary agent gets delegation edges to all non-primary agents
		if agent.Primary {
			for j, other := range config.Agents {
				if other.ID == agent.ID {
					continue
				}
				// Check if delegation edge already exists
				edgeID := fmt.Sprintf("%s-%s-delegation", agent.ID, other.ID)
				exists := false
				for _, e := range state.Edges {
					if e.ID == edgeID {
						exists = true
						break
					}
				}
				if !exists {
					state.Edges = append(state.Edges, EditorEdge{
						ID:    edgeID,
						From:  agent.ID,
						To:    other.ID,
						Type:  "delegation",
						Label: "delegate",
						Style: "solid",
					})
				}
				_ = j
			}
		}
	}

	return state
}

// EditorStateToConfig converts an EditorState back into a Prizm config.
// Only agent-type nodes are written to the config.
func EditorStateToConfig(state *EditorState) *orchestrator.Config {
	config := &orchestrator.Config{}

	for _, node := range state.Nodes {
		if node.Type != "agent" {
			continue
		}

		agent := orchestrator.AgentConfig{
			ID:            node.ID,
			Role:          node.Role,
			Provider:      node.Provider,
			Model:         node.Model,
			Primary:       node.Primary,
			Capabilities:  node.Capabilities,
			Subscriptions: node.Subscriptions,
			Context:       node.Context,
		}

		// If subscriptions are empty, derive from edges
		if len(agent.Subscriptions) == 0 {
			for _, edge := range state.Edges {
				if edge.To == node.ID && edge.Type == "delegation" {
					agent.Subscriptions = append(agent.Subscriptions,
						fmt.Sprintf("%s.task.completed", edge.From))
				}
			}
		}

		config.Agents = append(config.Agents, agent)
	}

	return config
}

// WriteConfigYAML converts an EditorState to YAML string.
func WriteConfigYAML(state *EditorState) (string, error) {
	config := EditorStateToConfig(state)

	// Build a YAML-friendly structure
	type agentYAML struct {
		ID            string   `yaml:"id"`
		Role          string   `yaml:"role"`
		Provider      string   `yaml:"provider,omitempty"`
		Model         string   `yaml:"model"`
		Primary       bool     `yaml:"primary,omitempty"`
		Capabilities  []string `yaml:"capabilities,omitempty"`
		Subscriptions []string `yaml:"subscriptions,omitempty"`
		Context       []string `yaml:"context,omitempty"`
	}

	type configYAML struct {
		Agents []agentYAML `yaml:"agents"`
	}

	var agents []agentYAML
	for _, a := range config.Agents {
		agents = append(agents, agentYAML{
			ID:            a.ID,
			Role:          a.Role,
			Provider:      a.Provider,
			Model:         a.Model,
			Primary:       a.Primary,
			Capabilities:  a.Capabilities,
			Subscriptions: a.Subscriptions,
			Context:       a.Context,
		})
	}

	data := configYAML{Agents: agents}

	out, err := yaml.Marshal(data)
	if err != nil {
		return "", fmt.Errorf("marshal config: %w", err)
	}

	return string(out), nil
}

// ValidateEditorState checks an EditorState for common errors.
func ValidateEditorState(state *EditorState) []string {
	var errors []string

	// Check for duplicate node IDs
	seen := make(map[string]bool)
	for _, node := range state.Nodes {
		if seen[node.ID] {
			errors = append(errors, fmt.Sprintf("duplicate node ID: %s", node.ID))
		}
		seen[node.ID] = true
	}

	// Check that edges reference valid nodes
	for _, edge := range state.Edges {
		if !seen[edge.From] {
			errors = append(errors, fmt.Sprintf("edge %s references unknown source: %s", edge.ID, edge.From))
		}
		if !seen[edge.To] {
			errors = append(errors, fmt.Sprintf("edge %s references unknown target: %s", edge.ID, edge.To))
		}
	}

	// Check for exactly one primary agent
	primaryCount := 0
	for _, node := range state.Nodes {
		if node.Type == "agent" && node.Primary {
			primaryCount++
		}
	}
	if primaryCount == 0 && len(state.Nodes) > 0 {
		errors = append(errors, "no primary agent designated")
	} else if primaryCount > 1 {
		errors = append(errors, fmt.Sprintf("multiple primary agents (%d)", primaryCount))
	}

	// Check agent IDs match regex
	for _, node := range state.Nodes {
		if node.Type == "agent" {
			if !isValidAgentID(node.ID) {
				errors = append(errors, fmt.Sprintf("invalid agent ID: %s (must match [a-z0-9][a-z0-9-]*)", node.ID))
			}
		}
	}

	// Check valid edge types
	validEdgeTypes := map[string]bool{
		"delegation": true,
		"review":     true,
		"approval":   true,
		"event":      true,
	}
	for _, edge := range state.Edges {
		if !validEdgeTypes[edge.Type] {
			errors = append(errors, fmt.Sprintf("invalid edge type: %s (edge %s)", edge.Type, edge.ID))
		}
	}

	return errors
}

// isValidAgentID checks if an agent ID matches the config validation regex.
func isValidAgentID(id string) bool {
	if len(id) == 0 {
		return false
	}
	if id[0] < 'a' || id[0] > 'z' {
		if id[0] < '0' || id[0] > '9' {
			return false
		}
	}
	for _, c := range id {
		if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-') {
			return false
		}
	}
	return true
}

// AddNode adds a node to the editor state with validation.
func (s *EditorState) AddNode(node EditorNode) error {
	for _, existing := range s.Nodes {
		if existing.ID == node.ID {
			return fmt.Errorf("node with ID %q already exists", node.ID)
		}
	}
	if node.Type == "" {
		node.Type = "agent"
	}
	if node.Label == "" {
		node.Label = strings.ToUpper(node.ID)
	}
	s.Nodes = append(s.Nodes, node)
	return nil
}

// RemoveNode removes a node and all edges connected to it.
func (s *EditorState) RemoveNode(id string) error {
	found := false
	newNodes := make([]EditorNode, 0, len(s.Nodes))
	for _, n := range s.Nodes {
		if n.ID == id {
			found = true
			continue
		}
		newNodes = append(newNodes, n)
	}
	if !found {
		return fmt.Errorf("node %q not found", id)
	}
	s.Nodes = newNodes

	// Remove connected edges
	newEdges := make([]EditorEdge, 0, len(s.Edges))
	for _, e := range s.Edges {
		if e.From != id && e.To != id {
			newEdges = append(newEdges, e)
		}
	}
	s.Edges = newEdges
	return nil
}

// AddEdge adds an edge to the editor state with validation.
func (s *EditorState) AddEdge(edge EditorEdge) error {
	// Check source and target exist
	sourceFound := false
	targetFound := false
	for _, n := range s.Nodes {
		if n.ID == edge.From {
			sourceFound = true
		}
		if n.ID == edge.To {
			targetFound = true
		}
	}
	if !sourceFound {
		return fmt.Errorf("source node %q not found", edge.From)
	}
	if !targetFound {
		return fmt.Errorf("target node %q not found", edge.To)
	}

	// Check for duplicate edge
	for _, existing := range s.Edges {
		if existing.ID == edge.ID {
			return fmt.Errorf("edge with ID %q already exists", edge.ID)
		}
	}

	if edge.Type == "" {
		edge.Type = "delegation"
	}
	if edge.Style == "" {
		switch edge.Type {
		case "delegation":
			edge.Style = "solid"
		case "review":
			edge.Style = "dashed"
		case "approval":
			edge.Style = "bold"
		case "event":
			edge.Style = "dotted"
		}
	}

	s.Edges = append(s.Edges, edge)
	return nil
}

// RemoveEdge removes an edge by ID.
func (s *EditorState) RemoveEdge(id string) error {
	found := false
	newEdges := make([]EditorEdge, 0, len(s.Edges))
	for _, e := range s.Edges {
		if e.ID == id {
			found = true
			continue
		}
		newEdges = append(newEdges, e)
	}
	if !found {
		return fmt.Errorf("edge %q not found", id)
	}
	s.Edges = newEdges
	return nil
}

// EdgeUpdate represents partial updates to an edge. Only non-nil fields are
// applied, so the front-end can change just the action (or label/type) of a flow.
type EdgeUpdate struct {
	Label  *string `json:"label"`
	Type   *string `json:"type"`
	Style  *string `json:"style"`
	Action *string `json:"action"`
}

// UpdateEdge applies a partial update to the edge with the given ID. A changed
// Type is validated against the known edge types.
func (s *EditorState) UpdateEdge(id string, upd EdgeUpdate) error {
	validEdgeTypes := map[string]bool{"delegation": true, "review": true, "approval": true, "event": true}
	for i := range s.Edges {
		if s.Edges[i].ID != id {
			continue
		}
		if upd.Type != nil {
			if !validEdgeTypes[*upd.Type] {
				return fmt.Errorf("invalid edge type %q", *upd.Type)
			}
			s.Edges[i].Type = *upd.Type
		}
		if upd.Label != nil {
			s.Edges[i].Label = *upd.Label
		}
		if upd.Style != nil {
			s.Edges[i].Style = *upd.Style
		}
		if upd.Action != nil {
			s.Edges[i].Action = strings.TrimSpace(*upd.Action)
		}
		return nil
	}
	return fmt.Errorf("edge %q not found", id)
}

// NodeUpdate represents partial updates to a node.
// Only non-nil fields will be applied.
type NodeUpdate struct {
	Label         *string   `json:"label"`
	Role          *string   `json:"role"`
	Model         *string   `json:"model"`
	Provider      *string   `json:"provider"`
	Primary       *bool     `json:"primary"`
	Capabilities  []string  `json:"capabilities"`
	Subscriptions []string  `json:"subscriptions"`
	Context       []string  `json:"context"`
	Position      *Position `json:"position"`
}

// UpdateNode updates a node's properties by ID.
// Only non-nil/non-empty fields are applied.
func (s *EditorState) UpdateNode(id string, updates NodeUpdate) error {
	for i, n := range s.Nodes {
		if n.ID == id {
			if updates.Label != nil {
				s.Nodes[i].Label = *updates.Label
			}
			if updates.Role != nil {
				s.Nodes[i].Role = *updates.Role
			}
			if updates.Model != nil {
				s.Nodes[i].Model = *updates.Model
			}
			if updates.Provider != nil {
				s.Nodes[i].Provider = *updates.Provider
			}
			if updates.Primary != nil {
				s.Nodes[i].Primary = *updates.Primary
			}
			if updates.Capabilities != nil {
				s.Nodes[i].Capabilities = updates.Capabilities
			}
			if updates.Subscriptions != nil {
				s.Nodes[i].Subscriptions = updates.Subscriptions
			}
			if updates.Context != nil {
				s.Nodes[i].Context = updates.Context
			}
			if updates.Position != nil {
				s.Nodes[i].Position = *updates.Position
			}
			return nil
		}
	}
	return fmt.Errorf("node %q not found", id)
}

// WriteConfigToFile writes the editor state as YAML to a file on disk.
// This requires explicit path and should only be called with user confirmation.
// The path must be a .yaml or .yml file contained within allowedDir.
func WriteConfigToFile(state *EditorState, path string, allowedDir string) error {
	// Validate first
	errors := ValidateEditorState(state)
	if len(errors) > 0 {
		return fmt.Errorf("validation errors: %v", errors)
	}

	// Security: validate path is a YAML file jailed to the allowed directory
	if err := validateWritePath(path, allowedDir); err != nil {
		return fmt.Errorf("invalid path: %w", err)
	}

	yaml, err := WriteConfigYAML(state)
	if err != nil {
		return fmt.Errorf("yaml generation error: %w", err)
	}

	// Write atomically using os.CreateTemp in the target directory
	dir := filepath.Dir(path)
	tmpFile, err := os.CreateTemp(dir, "prizm-editor-*.yaml.tmp")
	if err != nil {
		return fmt.Errorf("create temp file error: %w", err)
	}
	tmpPath := tmpFile.Name()

	if _, err := tmpFile.WriteString(yaml); err != nil {
		os.Remove(tmpPath)
		tmpFile.Close()
		return fmt.Errorf("write temp file error: %w", err)
	}
	tmpFile.Close()

	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename error: %w", err)
	}

	return nil
}

// validateWritePath ensures the path is a .yaml/.yml file contained within
// allowedDir. It resolves symlinks on the parent directory so a symlink inside
// allowedDir cannot redirect the write outside it. An empty allowedDir falls
// back to the current working directory.
func validateWritePath(path string, allowedDir string) error {
	clean := filepath.Clean(path)

	// Must end in .yaml or .yml
	ext := strings.ToLower(filepath.Ext(clean))
	if ext != ".yaml" && ext != ".yml" {
		return fmt.Errorf("path must end in .yaml or .yml")
	}

	// Resolve the jail base.
	base := allowedDir
	if strings.TrimSpace(base) == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("cannot resolve working directory: %w", err)
		}
		base = cwd
	}
	absBase, err := filepath.Abs(base)
	if err != nil {
		return fmt.Errorf("invalid base directory: %w", err)
	}
	if resolved, rerr := filepath.EvalSymlinks(absBase); rerr == nil {
		absBase = resolved
	}

	// Resolve the target. A path is treated as absolute if it is absolute in
	// the native sense OR rooted POSIX-style ("/etc/..."), which on Windows
	// filepath.IsAbs would miss. Only genuinely relative paths are joined to
	// the jail base; rooted paths are left as-is so containment can reject them.
	absTarget := clean
	rootedPOSIX := strings.HasPrefix(filepath.ToSlash(clean), "/")
	if !filepath.IsAbs(absTarget) && !rootedPOSIX {
		absTarget = filepath.Join(absBase, absTarget)
	}
	absTarget, err = filepath.Abs(absTarget)
	if err != nil {
		return fmt.Errorf("invalid target path: %w", err)
	}
	absTarget = filepath.Clean(absTarget)

	// Resolve symlinks on the parent so a symlinked dir can't escape the jail.
	parent := filepath.Dir(absTarget)
	if resolved, rerr := filepath.EvalSymlinks(parent); rerr == nil {
		absTarget = filepath.Join(resolved, filepath.Base(absTarget))
	}

	rel, err := filepath.Rel(absBase, absTarget)
	lexicallyContained := err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
	if !lexicallyContained && !pathWithinExistingBase(absBase, filepath.Dir(absTarget)) {
		return fmt.Errorf("path %q must be within %q", path, absBase)
	}

	return nil
}

// pathWithinExistingBase compares filesystem identities instead of path text.
// This handles Windows short (8.3) aliases such as RUNNER~1 versus runneradmin
// without weakening containment: an ancestor must resolve to the exact allowed
// directory on disk. Non-existent target directories are walked upward first.
func pathWithinExistingBase(base, targetDir string) bool {
	baseInfo, err := os.Stat(base)
	if err != nil {
		return false
	}

	current := filepath.Clean(targetDir)
	for {
		if _, err := os.Stat(current); err == nil {
			break
		}
		parent := filepath.Dir(current)
		if parent == current {
			return false
		}
		current = parent
	}

	for {
		info, err := os.Stat(current)
		if err == nil && os.SameFile(baseInfo, info) {
			return true
		}
		parent := filepath.Dir(current)
		if parent == current {
			return false
		}
		current = parent
	}
}
