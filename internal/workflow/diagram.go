// Package workflow generates visual SVG diagrams of Prizm workflows.
//
// V24: Visual Workflow Representations
//
// Generates SVG diagrams from Prizm configuration and runtime state:
//   - Agent Topology: agents, roles, capabilities, delegation paths
//   - Feedback Loops: Lumi×Mango review cycles
//   - Delegation Flow: task lifecycle pipeline
//   - Approval Gates: approval request → grant/deny flow
//   - Event Flow: per-agent namespaces and registered actions
package workflow

import (
	"fmt"
	"io"
	"strings"

	svg "github.com/ajstarks/svgo"
	"github.com/emaharmony/prizm/internal/orchestrator"
)

// DiagramConfig holds configuration for diagram generation.
type DiagramConfig struct {
	// Width is the SVG canvas width in pixels.
	Width int

	// Height is the SVG canvas height in pixels.
	Height int

	// DarkTheme uses the dark color palette matching the dashboard.
	DarkTheme bool

	// ShowCapabilities shows capability badges on agent boxes.
	ShowCapabilities bool

	// ShowEvents shows event subscriptions on agent boxes.
	ShowEvents bool
}

// DefaultConfig returns sensible defaults for diagram generation.
func DefaultConfig() DiagramConfig {
	return DiagramConfig{
		Width:            800,
		Height:           600,
		DarkTheme:        true,
		ShowCapabilities: true,
		ShowEvents:       false,
	}
}

// Color palette for dark theme.
type palette struct {
	bg           string
	box          string
	boxBorder    string
	text         string
	textDim      string
	arrow        string
	arrowDash    string
	arrowDot     string
	arrowBold    string
	lead         string
	coder        string
	researcher   string
	orchestrator string
	approval     string
	system       string
}

func darkPalette() palette {
	return palette{
		bg:           "#0a0a0f",
		box:          "#16161f",
		boxBorder:    "#2a2a3a",
		text:         "#e0e0e8",
		textDim:      "#888",
		arrow:        "#a78bfa",
		arrowDash:    "#6366f1",
		arrowDot:     "#818cf8",
		arrowBold:    "#f59e0b",
		lead:         "#3b82f6",
		coder:        "#10b981",
		researcher:   "#f97316",
		orchestrator: "#8b5cf6",
		approval:     "#f59e0b",
		system:       "#6b7280",
	}
}

func lightPalette() palette {
	return palette{
		bg:           "#ffffff",
		box:          "#f8fafc",
		boxBorder:    "#e2e8f0",
		text:         "#1e293b",
		textDim:      "#64748b",
		arrow:        "#7c3aed",
		arrowDash:    "#6366f1",
		arrowDot:     "#818cf8",
		arrowBold:    "#d97706",
		lead:         "#2563eb",
		coder:        "#059669",
		researcher:   "#ea580c",
		orchestrator: "#7c3aed",
		approval:     "#d97706",
		system:       "#94a3b8",
	}
}

func roleColor(role string, p palette) string {
	switch strings.ToLower(role) {
	case "lead":
		return p.lead
	case "coder", "developer":
		return p.coder
	case "researcher":
		return p.researcher
	case "orchestrator":
		return p.orchestrator
	default:
		return p.system
	}
}

// AgentTopology generates an SVG diagram showing all agents and their relationships.
func AgentTopology(w io.Writer, agents []orchestrator.AgentConfig, cfg DiagramConfig) {
	p := darkPalette()
	if !cfg.DarkTheme {
		p = lightPalette()
	}

	canvas := svg.New(w)
	canvas.Start(cfg.Width, cfg.Height)
	canvas.Style("text/css",
		fmt.Sprintf(".box{fill:%s;stroke:%s;stroke-width:2}", p.box, p.boxBorder),
		fmt.Sprintf(".text{fill:%s;font-family:system-ui,sans-serif;font-size:14px}", p.text),
		fmt.Sprintf(".text-dim{fill:%s;font-family:system-ui,sans-serif;font-size:11px}", p.textDim),
		".badge{rx:4;ry:4;stroke-width:1}",
		".badge-text{font-family:system-ui,sans-serif;font-size:10px}",
		fmt.Sprintf(".primary{stroke:%s;stroke-width:3}", p.lead),
	)

	// Background
	canvas.Rect(0, 0, cfg.Width, cfg.Height, fmt.Sprintf("fill:%s", p.bg))

	// Title
	canvas.Text(cfg.Width/2, 30, "Agent Topology",
		fmt.Sprintf("text-anchor:middle;font-family:system-ui,sans-serif;font-size:20px;font-weight:bold;fill:%s", p.text))

	// Calculate agent positions
	cols := 2
	if len(agents) > 4 {
		cols = 3
	}
	boxW := 280
	boxH := 120
	gapX := 40
	gapY := 30
	startX := (cfg.Width - (cols*(boxW+gapX) - gapX)) / 2
	startY := 60

	for i, agent := range agents {
		col := i % cols
		row := i / cols
		x := startX + col*(boxW+gapX)
		y := startY + row*(boxH+gapY)

		borderColor := roleColor(agent.Role, p)
		if agent.Primary {
			borderColor = p.lead
		}

		// Agent box
		strokeWidth := 2
		if agent.Primary {
			strokeWidth = 3
		}
		canvas.Rect(x, y, boxW, boxH,
			fmt.Sprintf("fill:%s;stroke:%s;stroke-width:%d;rx:8", p.box, borderColor, strokeWidth))

		// Agent name
		canvas.Text(x+boxW/2, y+25, strings.ToUpper(agent.ID),
			fmt.Sprintf("text-anchor:middle;font-family:system-ui,sans-serif;font-size:16px;font-weight:bold;fill:%s", p.text))

		// Role badge
		canvas.Rect(x+10, y+40, 70, 20,
			fmt.Sprintf("fill:%s;rx:4;ry:4", borderColor))
		canvas.Text(x+45, y+54, agent.Role,
			"text-anchor:middle;font-family:system-ui,sans-serif;font-size:11px;fill:white")

		// Model
		canvas.Text(x+10, y+75, fmt.Sprintf("Model: %s", agent.Model),
			fmt.Sprintf("font-family:monospace;font-size:11px;fill:%s", p.textDim))

		// Capabilities (if enabled)
		if cfg.ShowCapabilities && len(agent.Capabilities) > 0 {
			caps := strings.Join(agent.Capabilities, ", ")
			if len(caps) > 35 {
				caps = caps[:32] + "..."
			}
			canvas.Text(x+10, y+95, fmt.Sprintf("Caps: %s", caps),
				fmt.Sprintf("font-family:monospace;font-size:10px;fill:%s", p.textDim))
		}

		// Primary marker
		if agent.Primary {
			canvas.Text(x+boxW-10, y+15, "★",
				fmt.Sprintf("text-anchor:end;font-size:16px;fill:%s", p.lead))
		}
	}

	// Draw delegation arrows between agents
	arrowY := startY - 15
	if len(agents) > 1 {
		// Delegation arrows from primary to others
		primaryX := -1
		for i, agent := range agents {
			if agent.Primary {
				primaryX = startX + (i%cols)*(boxW+gapX) + boxW/2
				break
			}
		}

		if primaryX >= 0 && len(agents) > 1 {
			canvas.Text(cfg.Width/2, arrowY, "→ delegates →",
				fmt.Sprintf("text-anchor:middle;font-family:system-ui,sans-serif;font-size:12px;fill:%s;font-style:italic", p.arrow))
		}
	}

	canvas.End()
}

// FeedbackLoops generates an SVG diagram showing the Three Feedback Loops.
func FeedbackLoops(w io.Writer, cfg DiagramConfig) {
	p := darkPalette()
	if !cfg.DarkTheme {
		p = lightPalette()
	}

	canvas := svg.New(w)
	canvas.Start(cfg.Width, cfg.Height)

	// Background
	canvas.Rect(0, 0, cfg.Width, cfg.Height, fmt.Sprintf("fill:%s", p.bg))

	// Title
	canvas.Text(cfg.Width/2, 30, "Three Feedback Loops",
		fmt.Sprintf("text-anchor:middle;font-family:system-ui,sans-serif;font-size:20px;font-weight:bold;fill:%s", p.text))
	canvas.Text(cfg.Width/2, 50, "Lumi × Mango Review Cycles",
		fmt.Sprintf("text-anchor:middle;font-family:system-ui,sans-serif;font-size:12px;fill:%s", p.textDim))

	// Agent boxes
	lumiX, lumiY := 150, 100
	mangoX, mangoY := cfg.Width-350, 100
	boxW, boxH := 200, 80

	// Lumi box
	canvas.Rect(lumiX, lumiY, boxW, boxH,
		fmt.Sprintf("fill:%s;stroke:%s;stroke-width:3;rx:8", p.box, p.lead))
	canvas.Text(lumiX+boxW/2, lumiY+30, "LUMI",
		fmt.Sprintf("text-anchor:middle;font-family:system-ui,sans-serif;font-size:16px;font-weight:bold;fill:%s", p.text))
	canvas.Text(lumiX+boxW/2, lumiY+50, "Lead Developer",
		fmt.Sprintf("text-anchor:middle;font-family:system-ui,sans-serif;font-size:11px;fill:%s", p.textDim))
	canvas.Text(lumiX+boxW-10, lumiY+15, "★",
		fmt.Sprintf("text-anchor:end;font-size:14px;fill:%s", p.lead))

	// Mango box
	canvas.Rect(mangoX, mangoY, boxW, boxH,
		fmt.Sprintf("fill:%s;stroke:%s;stroke-width:2;rx:8", p.box, p.coder))
	canvas.Text(mangoX+boxW/2, mangoY+30, "MANGO",
		fmt.Sprintf("text-anchor:middle;font-family:system-ui,sans-serif;font-size:16px;font-weight:bold;fill:%s", p.text))
	canvas.Text(mangoX+boxW/2, mangoY+50, "Coder",
		fmt.Sprintf("text-anchor:middle;font-family:system-ui,sans-serif;font-size:11px;fill:%s", p.textDim))

	// Loop 1: Pre-Dev Architecture Check (solid arrow)
	loopY := 220
	canvas.Text(cfg.Width/2, loopY, "1. Pre-Dev Architecture Check",
		fmt.Sprintf("text-anchor:middle;font-family:system-ui,sans-serif;font-size:13px;font-weight:bold;fill:%s", p.text))
	canvas.Text(cfg.Width/2, loopY+18, "Lumi plans, Mango reviews approach before coding",
		fmt.Sprintf("text-anchor:middle;font-family:system-ui,sans-serif;font-size:11px;fill:%s", p.textDim))
	// Solid arrow: Lumi → Mango
	canvas.Line(lumiX+boxW, loopY+35, mangoX, loopY+35,
		fmt.Sprintf("stroke:%s;stroke-width:2", p.arrow))
	canvas.Polygon([]int{mangoX - 10, mangoX, mangoX - 10},
		[]int{loopY + 28, loopY + 35, loopY + 42},
		fmt.Sprintf("fill:%s", p.arrow))

	// Loop 2: Mid-Dev Correctness Check (dashed arrow)
	loopY2 := 310
	canvas.Text(cfg.Width/2, loopY2, "2. Mid-Dev Correctness Check",
		fmt.Sprintf("text-anchor:middle;font-family:system-ui,sans-serif;font-size:13px;font-weight:bold;fill:%s", p.text))
	canvas.Text(cfg.Width/2, loopY2+18, "Mango implements, Lumi reviews for correctness",
		fmt.Sprintf("text-anchor:middle;font-family:system-ui,sans-serif;font-size:11px;fill:%s", p.textDim))
	// Dashed arrow: Mango → Lumi
	canvas.Line(mangoX, loopY2+35, lumiX+boxW, loopY2+35,
		fmt.Sprintf("stroke:%s;stroke-width:2;stroke-dasharray:8,4", p.arrowDash))
	canvas.Polygon([]int{lumiX + boxW + 10, lumiX + boxW, lumiX + boxW + 10},
		[]int{loopY2 + 28, loopY2 + 35, loopY2 + 42},
		fmt.Sprintf("fill:%s", p.arrowDash))

	// Loop 3: Post-Dev Vulnerability Analysis (dotted arrow)
	loopY3 := 400
	canvas.Text(cfg.Width/2, loopY3, "3. Post-Dev Vulnerability Analysis",
		fmt.Sprintf("text-anchor:middle;font-family:system-ui,sans-serif;font-size:13px;font-weight:bold;fill:%s", p.text))
	canvas.Text(cfg.Width/2, loopY3+18, "Mango reviews for security, Lumi fixes if needed",
		fmt.Sprintf("text-anchor:middle;font-family:system-ui,sans-serif;font-size:11px;fill:%s", p.textDim))
	// Dotted arrow: Mango → Lumi
	canvas.Line(mangoX, loopY3+35, lumiX+boxW, loopY3+35,
		fmt.Sprintf("stroke:%s;stroke-width:2;stroke-dasharray:3,3", p.arrowDot))
	canvas.Polygon([]int{lumiX + boxW + 10, lumiX + boxW, lumiX + boxW + 10},
		[]int{loopY3 + 28, loopY3 + 35, loopY3 + 42},
		fmt.Sprintf("fill:%s", p.arrowDot))

	// Legend
	legendY := cfg.Height - 80
	canvas.Text(50, legendY, "Legend:",
		fmt.Sprintf("font-family:system-ui,sans-serif;font-size:12px;font-weight:bold;fill:%s", p.text))
	canvas.Line(50, legendY+18, 120, legendY+18,
		fmt.Sprintf("stroke:%s;stroke-width:2", p.arrow))
	canvas.Text(130, legendY+22, "Direct delegation",
		fmt.Sprintf("font-family:system-ui,sans-serif;font-size:11px;fill:%s", p.textDim))
	canvas.Line(250, legendY+18, 320, legendY+18,
		fmt.Sprintf("stroke:%s;stroke-width:2;stroke-dasharray:8,4", p.arrowDash))
	canvas.Text(330, legendY+22, "Review (dashed)",
		fmt.Sprintf("font-family:system-ui,sans-serif;font-size:11px;fill:%s", p.textDim))
	canvas.Line(450, legendY+18, 520, legendY+18,
		fmt.Sprintf("stroke:%s;stroke-width:2;stroke-dasharray:3,3", p.arrowDot))
	canvas.Text(530, legendY+22, "Security review (dotted)",
		fmt.Sprintf("font-family:system-ui,sans-serif;font-size:11px;fill:%s", p.textDim))

	canvas.End()
}

// DelegationFlow generates an SVG diagram showing the task delegation pipeline.
func DelegationFlow(w io.Writer, agents []orchestrator.AgentConfig, cfg DiagramConfig) {
	p := darkPalette()
	if !cfg.DarkTheme {
		p = lightPalette()
	}

	canvas := svg.New(w)
	canvas.Start(cfg.Width, cfg.Height)

	// Background
	canvas.Rect(0, 0, cfg.Width, cfg.Height, fmt.Sprintf("fill:%s", p.bg))

	// Title
	canvas.Text(cfg.Width/2, 30, "Delegation Flow",
		fmt.Sprintf("text-anchor:middle;font-family:system-ui,sans-serif;font-size:20px;font-weight:bold;fill:%s", p.text))

	// Pipeline stages
	stages := []struct {
		name  string
		desc  string
		color string
	}{
		{"LLMStage", "LLM call +\nstreaming", p.arrow},
		{"Delegation\nStage", "Capability\ncheck", p.arrowDash},
		{"Persistence\nStage", "Save to\nrun dir", p.system},
		{"Event\nPublish", "NATS\nevents", p.orchestrator},
	}

	boxW := 140
	boxH := 70
	gapX := 30
	startX := (cfg.Width - (len(stages)*(boxW+gapX) - gapX)) / 2
	stageY := 80

	for i, stage := range stages {
		x := startX + i*(boxW+gapX)
		canvas.Rect(x, stageY, boxW, boxH,
			fmt.Sprintf("fill:%s;stroke:%s;stroke-width:2;rx:8", p.box, stage.color))
		canvas.Text(x+boxW/2, stageY+25, stage.name,
			fmt.Sprintf("text-anchor:middle;font-family:system-ui,sans-serif;font-size:12px;font-weight:bold;fill:%s", p.text))
		canvas.Text(x+boxW/2, stageY+45, stage.desc,
			fmt.Sprintf("text-anchor:middle;font-family:system-ui,sans-serif;font-size:10px;fill:%s", p.textDim))

		// Arrow to next stage
		if i < len(stages)-1 {
			arrowStartX := x + boxW + 2
			arrowEndX := x + boxW + gapX - 2
			canvas.Line(arrowStartX, stageY+boxH/2, arrowEndX, stageY+boxH/2,
				fmt.Sprintf("stroke:%s;stroke-width:2", p.arrow))
			canvas.Polygon([]int{arrowEndX, arrowEndX + 8, arrowEndX + 8},
				[]int{stageY + boxH/2 - 5, stageY + boxH/2, stageY + boxH/2 + 5},
				fmt.Sprintf("fill:%s", p.arrow))
		}
	}

	// Task lifecycle
	lifecycleY := 200
	canvas.Text(cfg.Width/2, lifecycleY, "Task Lifecycle",
		fmt.Sprintf("text-anchor:middle;font-family:system-ui,sans-serif;font-size:16px;font-weight:bold;fill:%s", p.text))

	states := []struct {
		name  string
		color string
	}{
		{"created", p.textDim},
		{"assigned", p.arrowDash},
		{"in_progress", p.lead},
		{"completed", p.coder},
		{"failed", "#f87171"},
		{"cancelled", p.system},
	}

	stateW := 100
	stateH := 40
	stateGapX := 20
	stateStartX := (cfg.Width - (len(states)*(stateW+stateGapX) - stateGapX)) / 2

	for i, state := range states {
		x := stateStartX + i*(stateW+stateGapX)
		canvas.Rect(x, lifecycleY+20, stateW, stateH,
			fmt.Sprintf("fill:%s;stroke:%s;stroke-width:1;rx:4;opacity:0.3", state.color, state.color))
		canvas.Rect(x, lifecycleY+20, stateW, stateH,
			fmt.Sprintf("fill:transparent;stroke:%s;stroke-width:1;rx:4", state.color))
		canvas.Text(x+stateW/2, lifecycleY+45, state.name,
			fmt.Sprintf("text-anchor:middle;font-family:monospace;font-size:11px;fill:%s", p.text))

		if i < len(states)-1 {
			canvas.Text(x+stateW+stateGapX/2, lifecycleY+40, "→",
				fmt.Sprintf("text-anchor:middle;font-size:12px;fill:%s", p.textDim))
		}
	}

	// Approval gate
	gateY := 310
	canvas.Text(cfg.Width/2, gateY, "Approval Gate",
		fmt.Sprintf("text-anchor:middle;font-family:system-ui,sans-serif;font-size:16px;font-weight:bold;fill:%s", p.approval))

	// Diamond shape for decision
	diamondX := cfg.Width / 2
	diamondY := gateY + 40
	canvas.Polygon([]int{diamondX, diamondX + 60, diamondX, diamondX - 60},
		[]int{diamondY - 25, diamondY + 15, diamondY + 55, diamondY + 15},
		fmt.Sprintf("fill:%s;stroke:%s;stroke-width:2", p.box, p.approval))
	canvas.Text(diamondX, diamondY+18, "Approve?",
		fmt.Sprintf("text-anchor:middle;font-family:system-ui,sans-serif;font-size:12px;font-weight:bold;fill:%s", p.text))

	// ✅ path
	canvas.Text(diamondX-100, diamondY+55, "✅ Yes",
		fmt.Sprintf("text-anchor:middle;font-family:system-ui,sans-serif;font-size:12px;fill:%s", p.coder))
	// ❌ path
	canvas.Text(diamondX+100, diamondY+55, "❌ No",
		fmt.Sprintf("text-anchor:middle;font-family:system-ui,sans-serif;font-size:12px;fill:%s", "#f87171"))

	canvas.End()
}

// ApprovalGate generates an SVG diagram showing the approval flow.
func ApprovalGate(w io.Writer, cfg DiagramConfig) {
	p := darkPalette()
	if !cfg.DarkTheme {
		p = lightPalette()
	}

	canvas := svg.New(w)
	canvas.Start(cfg.Width, cfg.Height)

	// Background
	canvas.Rect(0, 0, cfg.Width, cfg.Height, fmt.Sprintf("fill:%s", p.bg))

	// Title
	canvas.Text(cfg.Width/2, 30, "Approval Gate Flow",
		fmt.Sprintf("text-anchor:middle;font-family:system-ui,sans-serif;font-size:20px;font-weight:bold;fill:%s", p.text))

	// Agent requests approval
	agentX := 100
	agentY := 80
	canvas.Rect(agentX, agentY, 160, 60,
		fmt.Sprintf("fill:%s;stroke:%s;stroke-width:2;rx:8", p.box, p.lead))
	canvas.Text(agentX+80, agentY+25, "Agent",
		fmt.Sprintf("text-anchor:middle;font-family:system-ui,sans-serif;font-size:14px;font-weight:bold;fill:%s", p.text))
	canvas.Text(agentX+80, agentY+45, "RequestApproval()",
		fmt.Sprintf("text-anchor:middle;font-family:monospace;font-size:10px;fill:%s", p.textDim))

	// Arrow to approval
	canvas.Line(agentX+160, agentY+30, cfg.Width/2-60, agentY+30,
		fmt.Sprintf("stroke:%s;stroke-width:2", p.arrow))
	canvas.Text((agentX+160+cfg.Width/2-60)/2, agentY+20, "approval.requested",
		fmt.Sprintf("text-anchor:middle;font-family:monospace;font-size:10px;fill:%s", p.textDim))

	// Approval diamond
	diamondX := cfg.Width / 2
	diamondY := agentY + 30
	canvas.Polygon([]int{diamondX, diamondX + 60, diamondX, diamondX - 60},
		[]int{diamondY - 25, diamondY + 15, diamondY + 55, diamondY + 15},
		fmt.Sprintf("fill:%s;stroke:%s;stroke-width:3;rx:4", p.box, p.approval))
	canvas.Text(diamondX, diamondY+18, "Approve?",
		fmt.Sprintf("text-anchor:middle;font-family:system-ui,sans-serif;font-size:13px;font-weight:bold;fill:%s", p.text))

	// User decision
	userY := 250
	canvas.Rect(cfg.Width/2-80, userY, 160, 60,
		fmt.Sprintf("fill:%s;stroke:%s;stroke-width:2;rx:8", p.box, p.approval))
	canvas.Text(cfg.Width/2, userY+25, "User",
		fmt.Sprintf("text-anchor:middle;font-family:system-ui,sans-serif;font-size:14px;font-weight:bold;fill:%s", p.text))
	canvas.Text(cfg.Width/2, userY+45, "✅ or ❌",
		fmt.Sprintf("text-anchor:middle;font-family:system-ui,sans-serif;font-size:12px;fill:%s", p.approval))

	// Arrow from diamond to user
	canvas.Line(diamondX, diamondY+55, diamondX, userY,
		fmt.Sprintf("stroke:%s;stroke-width:2;stroke-dasharray:8,4", p.approval))

	// Grant path
	grantX := 150
	grantY := 380
	canvas.Rect(grantX, grantY, 180, 60,
		fmt.Sprintf("fill:%s;stroke:%s;stroke-width:2;rx:8", p.box, "#4ade80"))
	canvas.Text(grantX+90, grantY+25, "GrantApproval()",
		fmt.Sprintf("text-anchor:middle;font-family:monospace;font-size:12px;font-weight:bold;fill:%s", "#4ade80"))
	canvas.Text(grantX+90, grantY+45, "✅ Task completed",
		fmt.Sprintf("text-anchor:middle;font-family:system-ui,sans-serif;font-size:11px;fill:%s", p.textDim))

	// Deny path
	denyX := cfg.Width - 330
	denyY := 380
	canvas.Rect(denyX, denyY, 180, 60,
		fmt.Sprintf("fill:%s;stroke:%s;stroke-width:2;rx:8", p.box, "#f87171"))
	canvas.Text(denyX+90, denyY+25, "DenyApproval()",
		fmt.Sprintf("text-anchor:middle;font-family:monospace;font-size:12px;font-weight:bold;fill:%s", "#f87171"))
	canvas.Text(denyX+90, denyY+45, "❌ Task failed",
		fmt.Sprintf("text-anchor:middle;font-family:system-ui,sans-serif;font-size:11px;fill:%s", p.textDim))

	// Arrows from user to grant/deny
	canvas.Line(cfg.Width/2-80, userY+30, grantX+180, grantY+30,
		fmt.Sprintf("stroke:%s;stroke-width:2", "#4ade80"))
	canvas.Text(grantX+180+40, (userY+30+grantY+30)/2, "✅ granted",
		fmt.Sprintf("text-anchor:middle;font-family:system-ui,sans-serif;font-size:11px;fill:%s", "#4ade80"))

	canvas.Line(cfg.Width/2+80, userY+30, denyX, denyY+30,
		fmt.Sprintf("stroke:%s;stroke-width:2", "#f87171"))
	canvas.Text(denyX-40, (userY+30+denyY+30)/2, "❌ denied",
		fmt.Sprintf("text-anchor:middle;font-family:system-ui,sans-serif;font-size:11px;fill:%s", "#f87171"))

	// Events
	eventY := 490
	canvas.Text(cfg.Width/2, eventY, "Events: <agent>.approval.requested → <agent>.approval.granted / <agent>.approval.denied",
		fmt.Sprintf("text-anchor:middle;font-family:monospace;font-size:10px;fill:%s", p.textDim))

	canvas.End()
}

// EventFlow generates an SVG diagram showing the event bus topology.
func EventFlow(w io.Writer, agents []orchestrator.AgentConfig, cfg DiagramConfig) {
	p := darkPalette()
	if !cfg.DarkTheme {
		p = lightPalette()
	}

	canvas := svg.New(w)
	canvas.Start(cfg.Width, cfg.Height)

	// Background
	canvas.Rect(0, 0, cfg.Width, cfg.Height, fmt.Sprintf("fill:%s", p.bg))

	// Title
	canvas.Text(cfg.Width/2, 30, "Event Flow Topology",
		fmt.Sprintf("text-anchor:middle;font-family:system-ui,sans-serif;font-size:20px;font-weight:bold;fill:%s", p.text))

	// Central NATS bus
	busY := cfg.Height / 2
	canvas.Rect(100, busY-20, cfg.Width-200, 40,
		fmt.Sprintf("fill:%s;stroke:%s;stroke-width:2;rx:4", "#1e1e2e", p.orchestrator))
	canvas.Text(cfg.Width/2, busY+5, "NATS Event Bus",
		fmt.Sprintf("text-anchor:middle;font-family:system-ui,sans-serif;font-size:14px;font-weight:bold;fill:%s", p.text))

	// Agent namespaces above the bus
	agentBoxW := 140
	agentBoxH := 60
	agentStartX := (cfg.Width - len(agents)*(agentBoxW+20) + 20) / 2

	for i, agent := range agents {
		x := agentStartX + i*(agentBoxW+20)
		y := busY - 100

		color := roleColor(agent.Role, p)
		canvas.Rect(x, y, agentBoxW, agentBoxH,
			fmt.Sprintf("fill:%s;stroke:%s;stroke-width:2;rx:8", p.box, color))
		canvas.Text(x+agentBoxW/2, y+25, agent.ID,
			fmt.Sprintf("text-anchor:middle;font-family:system-ui,sans-serif;font-size:14px;font-weight:bold;fill:%s", p.text))
		canvas.Text(x+agentBoxW/2, y+42, fmt.Sprintf("%s.*", agent.ID),
			fmt.Sprintf("text-anchor:middle;font-family:monospace;font-size:10px;fill:%s", p.textDim))

		// Arrow from agent to bus
		canvas.Line(x+agentBoxW/2, y+agentBoxH, x+agentBoxW/2, busY-20,
			fmt.Sprintf("stroke:%s;stroke-width:1;stroke-dasharray:4,2", color))
	}

	// System events below the bus
	sysY := busY + 60
	systemEvents := []string{
		"prizm.task.created",
		"prizm.cost.tracked",
		"prizm.session.created",
		"prizm.channel.received",
	}

	canvas.Rect(50, sysY, cfg.Width-100, 80,
		fmt.Sprintf("fill:%s;stroke:%s;stroke-width:1;rx:8", p.box, p.system))
	canvas.Text(cfg.Width/2, sysY+20, "System Events (prizm.*)",
		fmt.Sprintf("text-anchor:middle;font-family:system-ui,sans-serif;font-size:12px;font-weight:bold;fill:%s", p.text))

	for i, event := range systemEvents {
		x := 80 + i*(agentBoxW+20)
		canvas.Text(x, sysY+45, event,
			fmt.Sprintf("font-family:monospace;font-size:9px;fill:%s", p.textDim))
	}

	// Arrow from bus to system events
	canvas.Line(cfg.Width/2, busY+20, cfg.Width/2, sysY,
		fmt.Sprintf("stroke:%s;stroke-width:1;stroke-dasharray:4,2", p.system))

	canvas.End()
}

// GenerateWorkflow creates a workflow diagram by type name.
func GenerateWorkflow(w io.Writer, diagramType string, agents []orchestrator.AgentConfig, cfg DiagramConfig) {
	switch diagramType {
	case "topology", "agents":
		AgentTopology(w, agents, cfg)
	case "feedback", "feedback-loops", "loops":
		FeedbackLoops(w, cfg)
	case "delegation", "delegation-flow", "pipeline":
		DelegationFlow(w, agents, cfg)
	case "approval", "approval-gate":
		ApprovalGate(w, cfg)
	case "events", "event-flow":
		EventFlow(w, agents, cfg)
	default:
		// Default to topology
		AgentTopology(w, agents, cfg)
	}
}

// GenerateWorkflowWithCapabilities creates a topology diagram including capability enforcement.
func GenerateWorkflowWithCapabilities(w io.Writer, agents []orchestrator.AgentConfig, roleDefaults map[string][]string, cfg DiagramConfig) {
	// For now, delegate to AgentTopology with capabilities shown
	cfg.ShowCapabilities = true
	AgentTopology(w, agents, cfg)
}
