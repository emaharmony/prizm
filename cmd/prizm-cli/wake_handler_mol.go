package main

import (
	"fmt"
	"os/exec"
	"strings"
)

// molStatusReport generates a self-status report for the Machine Orchestration Loop.
// It reports Prizm's own health: agents, sessions, plans, memory, and observations.
// This is the simplest meaningful MOL action: schedule → execute → emit event.
func (wh *WakeHandler) molStatusReport() string {
	var sb strings.Builder

	title := "Prizm"
	if wh.cfg != nil && wh.cfg.Prizm.InstanceID != "" {
		title = wh.cfg.Prizm.InstanceID
	}
	sb.WriteString(fmt.Sprintf("🧠 **MOL Self-Status — %s**\n\n", title))

	// --- Agents ---
	if wh.cfg != nil {
		agents := wh.cfg.Agents
		sb.WriteString(fmt.Sprintf("**Agents:** %d configured\n", len(agents)))
		for _, a := range agents {
			model := a.Model
			if model == "" {
				model = "(default)"
			}
			sb.WriteString(fmt.Sprintf("  • %s (%s) → %s\n", a.ID, a.Role, model))
		}
		sb.WriteString("\n")
	}

	// --- Sessions ---
	if wh.sessMgr != nil {
		sessions, err := wh.sessMgr.ListActive()
		if err == nil && len(sessions) > 0 {
			sb.WriteString(fmt.Sprintf("**Sessions:** %d active\n", len(sessions)))
			for _, s := range sessions {
				agentLabel := s.AgentID
				if agentLabel == "" {
					agentLabel = "unknown"
				}
				sb.WriteString(fmt.Sprintf("  • %s (%s, %d msgs)\n", s.ID, agentLabel, len(s.Messages)))
			}
			sb.WriteString("\n")
		} else if err != nil {
			sb.WriteString("**Sessions:** unavailable\n\n")
		}
	}

	// --- Plans ---
	if wh.planMgr != nil {
		plans, err := wh.planMgr.LoadPlans()
		if err == nil {
			active := 0
			completed := 0
			autoProceed := 0
			pendingApproval := 0
			for _, p := range plans {
				switch p.Status {
				case "completed", "abandoned":
					completed++
				case "auto_proceed":
					autoProceed++
					active++
				case "pending_approval":
					pendingApproval++
					active++
				default:
					active++
				}
			}
			sb.WriteString(fmt.Sprintf("**Plans:** %d total (%d active, %d completed)\n", len(plans), active, completed))
			if autoProceed > 0 {
				sb.WriteString(fmt.Sprintf("  • %d auto-proceed, %d pending approval\n", autoProceed, pendingApproval))
			}
		}
		sb.WriteString("\n")
	}

	// --- Memory ---
	if wh.remClient != nil {
		sb.WriteString("**Memory:** Recall connected\n\n")
	} else {
		sb.WriteString("**Memory:** Recall not connected\n\n")
	}

	// --- Recent git activity ---
	repoPath := wh.statusReportRepoPath()
	if gitLog, err := exec.Command("git", "-C", repoPath, "log", "--oneline", "-5", "--format=%h %s (%cr)").Output(); err == nil && len(gitLog) > 0 {
		sb.WriteString("**Recent commits:**\n")
		for _, line := range strings.Split(strings.TrimSpace(string(gitLog)), "\n") {
			if line != "" {
				sb.WriteString(fmt.Sprintf("  • %s\n", line))
			}
		}
	}

	// --- Observations ---
	sb.WriteString("\n**Observations:**\n")
	if wh.cfg != nil {
		ttsStatus := "off"
		if wh.cfg.Prizm.TTS.Enabled {
			ttsStatus = "on"
		}
		sb.WriteString(fmt.Sprintf("  • TTS: %s\n", ttsStatus))
		enabledCrons := 0
		for _, cron := range wh.cfg.Prizm.Scheduler.Jobs {
			if cron.Enabled {
				enabledCrons++
			}
		}
		sb.WriteString(fmt.Sprintf("  • Cron jobs: %d enabled\n", enabledCrons))
	}

	return sb.String()
}