// Package tracker holds the I/O-free live model of a running gated-loop workflow.
//
// It accumulates state from the engine's event stream, then exposes it as:
//
//   - Render() - a terminal string view.
//   - Snapshot() - a plain, concurrency-safe value copy for GUI consumers such
//     as the desktop panel (`cmd/prizm-panel`).
//
// The model is decoupled from any transport so update and render logic stays
// unit-testable and reusable across terminal and GUI front-ends. All exported
// methods are safe for concurrent use.
package tracker

import (
	"strings"
	"sync"
)

// PhaseView is the accumulated live state of a single workflow phase.
type PhaseView struct {
	Name       string
	Status     string // pending | running | paused | passed | fallback | stuck
	GateScore  float64
	GatePassed bool
	GateSeen   bool
	LastTool   string
	ToolRetry  int
	VerifyText string
}

// Model accumulates workflow state from the event stream.
type Model struct {
	mu sync.Mutex

	workflow    string
	order       []string // phase names in the order first seen
	phases      map[string]*PhaseView
	current     string
	status      string // connecting | running | paused | completed | blocked | ...
	tokTotal    int
	tokMax      int
	delegations map[string]string // task_id -> "agent:status"
	lastEvent   string
	events      int
}

// New returns an empty model in the "connecting" state.
func New() *Model {
	return &Model{
		phases:      map[string]*PhaseView{},
		delegations: map[string]string{},
		status:      "connecting",
	}
}

// Apply updates the model from one workflow event.
func (m *Model) Apply(evType string, payload map[string]any) {
	m.mu.Lock()
	defer m.mu.Unlock()

	evType = normalizeEventType(evType)
	m.events++
	m.lastEvent = evType

	switch evType {
	case "workflow.started":
		m.workflow = str(payload, "workflow")
		m.status = "running"
	case "phase.entered":
		name := str(payload, "phase")
		m.current = name
		pv := m.phase(name)
		pv.Status = "running"
		if m.status == "connecting" {
			m.status = "running"
		}
	case "phase.tokens":
		m.tokTotal = int(num(payload, "total"))
		if mx := int(num(payload, "max")); mx > 0 {
			m.tokMax = mx
		}
		if name := str(payload, "phase"); name != "" {
			m.current = name
		}
	case "phase.gate_check":
		pv := m.phase(str(payload, "phase"))
		pv.GateSeen = true
		pv.GateScore = num(payload, "score")
		pv.GatePassed, _ = payload["passed"].(bool)
		if pv.GatePassed {
			pv.Status = "passed"
		}
	case "phase.verification":
		pv := m.phase(str(payload, "phase"))
		passed, _ := payload["passed"].(bool)
		mark := "FAIL"
		if passed {
			mark = "pass"
		}
		pv.VerifyText = verifyText(str(payload, "profile"), mark, int(num(payload, "exit_code")))
	case "phase.fallback":
		m.phase(str(payload, "phase")).Status = "fallback"
	case "phase.stuck":
		pv := m.phase(str(payload, "phase"))
		pv.Status = "stuck"
		pv.LastTool = str(payload, "tool")
	case "tool.retry":
		pv := m.phase(str(payload, "phase"))
		pv.LastTool = str(payload, "tool")
		pv.ToolRetry = int(num(payload, "attempt"))
	case "workflow.paused":
		m.status = "paused"
		if name := str(payload, "phase"); name != "" {
			m.phase(name).Status = "paused"
		}
	case "workflow.budget_exhausted":
		m.status = "budget exhausted"
	case "phase.budget_exhausted":
		if name := str(payload, "phase"); name != "" {
			m.phase(name).Status = "budget"
		}
	case "workflow.completed":
		// The terminal event carries the real outcome so a budget-killed run
		// isn't masked as "completed" in the live view.
		if s := str(payload, "status"); s != "" && s != "completed" {
			m.status = strings.ReplaceAll(s, "_", " ")
		} else {
			m.status = "completed"
		}
	case "workflow.blocked":
		m.status = "blocked"
	case "task.delegated":
		if id := str(payload, "task_id"); id != "" {
			m.delegations[id] = str(payload, "agent") + ":sent"
		}
	case "delegation.retry":
		m.markDelegations(payload, "retrying")
	case "delegation.timeout":
		m.markDelegations(payload, "timed_out")
	}
}

func normalizeEventType(evType string) string {
	switch evType {
	case "workflow.phase.entered":
		return "phase.entered"
	case "workflow.phase.gate_check":
		return "phase.gate_check"
	case "workflow.phase.tokens":
		return "phase.tokens"
	case "workflow.phase.verification":
		return "phase.verification"
	case "workflow.phase.fallback":
		return "phase.fallback"
	case "workflow.phase.stuck":
		return "phase.stuck"
	case "workflow.tool.retry":
		return "tool.retry"
	case "workflow.paused.waiting_approval":
		return "workflow.paused"
	case "workflow.task.delegated":
		return "task.delegated"
	default:
		return evType
	}
}

// phase returns the phase view for name, creating it if needed. Callers must
// already hold m.mu.
func (m *Model) phase(name string) *PhaseView {
	if name == "" {
		return &PhaseView{}
	}
	pv, ok := m.phases[name]
	if !ok {
		pv = &PhaseView{Name: name, Status: "pending"}
		m.phases[name] = pv
		m.order = append(m.order, name)
	}
	return pv
}

func num(p map[string]any, key string) float64 {
	switch v := p[key].(type) {
	case float64:
		return v
	case float32:
		return float64(v)
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case uint64:
		return float64(v)
	default:
		return 0
	}
}

func str(p map[string]any, key string) string {
	if v, ok := p[key].(string); ok {
		return v
	}
	return ""
}

// markDelegations updates the status of every task id named in payload["tasks"].
// Callers must already hold m.mu.
func (m *Model) markDelegations(payload map[string]any, status string) {
	for _, id := range taskIDList(payload["tasks"]) {
		agent := "?"
		if prev, ok := m.delegations[id]; ok {
			agent = strings.SplitN(prev, ":", 2)[0]
		}
		m.delegations[id] = agent + ":" + status
	}
}

func taskIDList(v any) []string {
	switch tasks := v.(type) {
	case []string:
		return tasks
	case []any:
		out := make([]string, 0, len(tasks))
		for _, task := range tasks {
			id, _ := task.(string)
			if id != "" {
				out = append(out, id)
			}
		}
		return out
	default:
		return nil
	}
}

// Snapshot is a concurrency-safe, plain-value copy of the model for GUI
// rendering.
type Snapshot struct {
	Workflow    string
	Status      string
	Current     string
	TokTotal    int
	TokMax      int
	Events      int
	LastEvent   string
	Phases      []PhaseView // in first-seen order
	Delegations map[string]string
}

// Snapshot returns an immutable copy of the current model state.
func (m *Model) Snapshot() Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()

	s := Snapshot{
		Workflow:    m.workflow,
		Status:      m.status,
		Current:     m.current,
		TokTotal:    m.tokTotal,
		TokMax:      m.tokMax,
		Events:      m.events,
		LastEvent:   m.lastEvent,
		Phases:      make([]PhaseView, 0, len(m.order)),
		Delegations: make(map[string]string, len(m.delegations)),
	}
	for _, name := range m.order {
		if pv := m.phases[name]; pv != nil {
			s.Phases = append(s.Phases, *pv)
		}
	}
	for k, v := range m.delegations {
		s.Delegations[k] = v
	}
	return s
}
