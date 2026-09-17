package tracker

import (
	"fmt"
	"sort"
	"strings"
)

var spinnerFrames = [...]string{">", "/", "-", "\\"}

// Render produces the static terminal view for the current model state.
func (m *Model) Render() string {
	return RenderFrame(m.Snapshot(), 0)
}

// RenderFrame renders one ASCII animation frame from a snapshot.
func RenderFrame(s Snapshot, frame int) string {
	var b strings.Builder

	fmt.Fprintf(&b, "PRIZM PANEL")
	if s.Workflow != "" {
		fmt.Fprintf(&b, " - %s", s.Workflow)
	}
	fmt.Fprintf(&b, " [%s]\n", emptyDefault(s.Status, "connecting"))
	b.WriteString(strings.Repeat("-", 64) + "\n")

	if s.TokTotal > 0 || s.TokMax > 0 {
		b.WriteString("Budget  " + TokenMeter(s.TokTotal, s.TokMax) + "\n\n")
	}

	b.WriteString("Phases\n")
	if len(s.Phases) == 0 {
		b.WriteString("  . waiting for workflow events\n")
	}
	for _, pv := range s.Phases {
		isCurrent := pv.Name == s.Current
		fmt.Fprintf(&b, "  %s %-18s", PhaseGlyph(pv, isCurrent, frame), pv.Name)
		if pv.GateSeen {
			fmt.Fprintf(&b, " gate %.2f", pv.GateScore)
		}
		if pv.VerifyText != "" {
			fmt.Fprintf(&b, " | verify %s", pv.VerifyText)
		}
		if pv.LastTool != "" {
			fmt.Fprintf(&b, " | tool %s", pv.LastTool)
			if pv.ToolRetry > 0 {
				fmt.Fprintf(&b, " retry %d", pv.ToolRetry)
			}
		}
		b.WriteString("\n")
	}

	if len(s.Delegations) > 0 {
		b.WriteString("\nDelegations\n")
		ids := make([]string, 0, len(s.Delegations))
		for id := range s.Delegations {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			fmt.Fprintf(&b, "  %-12s %s\n", id, s.Delegations[id])
		}
	}

	last := emptyDefault(s.LastEvent, "none")
	fmt.Fprintf(&b, "\n%d events | last: %s\n", s.Events, last)
	return b.String()
}

// PhaseGlyph returns the ASCII status glyph for a phase.
func PhaseGlyph(pv PhaseView, isCurrent bool, frame int) string {
	switch pv.Status {
	case "passed":
		return "ok"
	case "fallback":
		return "!!"
	case "stuck", "blocked":
		return "xx"
	case "paused":
		return "||"
	}
	if isCurrent {
		idx := frame % len(spinnerFrames)
		if idx < 0 {
			idx += len(spinnerFrames)
		}
		return spinnerFrames[idx] + " "
	}
	return ". "
}

// TokenMeter renders a 20-cell ASCII progress bar for token usage.
func TokenMeter(total, max int) string {
	if max <= 0 {
		return fmt.Sprintf("%s tokens (no ceiling)", HumanInt(total))
	}

	const width = 20
	frac := float64(total) / float64(max)
	if frac < 0 {
		frac = 0
	}
	if frac > 1 {
		frac = 1
	}

	filled := int(frac * width)
	bar := strings.Repeat("#", filled) + strings.Repeat(".", width-filled)
	return fmt.Sprintf("[%s] %s/%s (%.0f%%)", bar, HumanInt(total), HumanInt(max), frac*100)
}

// HumanInt renders an integer with a "k" suffix for thousands.
func HumanInt(n int) string {
	if n >= 1000 {
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	return fmt.Sprintf("%d", n)
}

func verifyText(profile, mark string, exitCode int) string {
	if profile == "" {
		return fmt.Sprintf("%s(exit %d)", mark, exitCode)
	}
	return fmt.Sprintf("%s %s(exit %d)", profile, mark, exitCode)
}

func emptyDefault(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}
