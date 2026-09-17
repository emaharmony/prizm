package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/emaharmony/prizm/internal/orchestrator"
	"github.com/emaharmony/prizm/internal/plan"
	"github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Prizm TUI — Plan Viewer
// Usage: prizm tui

var (
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#7D56F4")).
			MarginBottom(1)

	planIDStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#F4A261"))

	statusStyle = map[plan.StepStatus]lipgloss.Style{
		plan.StepCompleted:   lipgloss.NewStyle().Foreground(lipgloss.Color("#2ECC71")),
		plan.StepInProgress:  lipgloss.NewStyle().Foreground(lipgloss.Color("#3498DB")),
		plan.StepPending:     lipgloss.NewStyle().Foreground(lipgloss.Color("#95A5A6")),
		plan.StepBlocked:      lipgloss.NewStyle().Foreground(lipgloss.Color("#E74C3C")),
		plan.StepSkipped:     lipgloss.NewStyle().Foreground(lipgloss.Color("#F39C12")),
	}

	planStatusStyle = map[plan.PlanStatus]lipgloss.Style{
		plan.StatusAutoProceed:  lipgloss.NewStyle().Foreground(lipgloss.Color("#2ECC71")),
		plan.StatusPendingApproval: lipgloss.NewStyle().Foreground(lipgloss.Color("#F39C12")),
		plan.StatusCompleted:    lipgloss.NewStyle().Foreground(lipgloss.Color("#95A5A6")),
		plan.StatusAbandoned:    lipgloss.NewStyle().Foreground(lipgloss.Color("#E74C3C")),
	}

	dimStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#666666"))

	helpStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#555555")).
			MarginTop(1)
)

type planModel struct {
	plans   []plan.Plan
	cursor  int
	quitting bool
	expanded map[string]bool
}

func initialModel(mgr *plan.Manager) (planModel, error) {
	plans, err := mgr.LoadPlans()
	if err != nil {
		return planModel{}, fmt.Errorf("failed to load plans: %w", err)
	}
	if len(plans) == 0 {
		return planModel{}, fmt.Errorf("no plans found")
	}
	expanded := make(map[string]bool)
	return planModel{
		plans:    plans,
		cursor:   0,
		expanded: expanded,
	}, nil
}

func (m planModel) Init() tea.Cmd {
	return nil
}

func (m planModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			m.quitting = true
			return m, tea.Quit
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.plans)-1 {
				m.cursor++
			}
		case "enter", " ":
			id := m.plans[m.cursor].ID
			m.expanded[id] = !m.expanded[id]
		}
	}
	return m, nil
}

func (m planModel) View() string {
	if m.quitting {
		return ""
	}

	var b strings.Builder
	b.WriteString(titleStyle.Render("📋 Prizm Plan Viewer"))
	b.WriteString("\n\n")

	for i, p := range m.plans {
		cursor := "  "
		if i == m.cursor {
			cursor = "▸ "
		}

		st, ok := planStatusStyle[p.Status]
		if !ok {
			st = lipgloss.NewStyle()
		}

		completed, total := plan.StepProgress(&p)
		progress := ""
		if total > 0 {
			progress = dimStyle.Render(fmt.Sprintf(" (%d/%d steps)", completed, total))
		}

		line := fmt.Sprintf("%s%s %s%s", cursor, planIDStyle.Render(p.ID), st.Render(string(p.Status)), progress)

		if m.expanded[p.ID] {
			line += fmt.Sprintf("\n    %s", dimStyle.Render(p.Title))
			if p.Description != "" {
				line += fmt.Sprintf("\n    %s", dimStyle.Render(p.Description))
			}
			if p.Branch != "" {
				line += fmt.Sprintf("\n    branch: %s", dimStyle.Render(p.Branch))
			}
			if p.Reasoning != "" {
				line += fmt.Sprintf("\n    reasoning: %s", dimStyle.Render(p.Reasoning))
			}
			for _, s := range p.Steps {
				ss, ok := statusStyle[s.Status]
				if !ok {
					ss = lipgloss.NewStyle()
				}
				check := "⬜"
				switch s.Status {
				case plan.StepCompleted:
					check = "✅"
				case plan.StepInProgress:
					check = "🔄"
				case plan.StepBlocked:
					check = "🚫"
				case plan.StepSkipped:
					check = "⏭️"
				}
				notes := ""
				if s.Notes != "" {
					notes = dimStyle.Render(fmt.Sprintf(" — %s", s.Notes))
				}
				line += fmt.Sprintf("\n    %s %s: %s%s", check, ss.Render(s.ID), s.Title, notes)
			}
		}

		b.WriteString(line + "\n")
	}

	b.WriteString(helpStyle.Render("\n↑/k: up  ↓/j: down  enter/space: expand  q: quit"))
	return b.String()
}

func runTUI(mgr *plan.Manager) error {
	m, err := initialModel(mgr)
	if err != nil {
		return err
	}
	p := tea.NewProgram(m, tea.WithAltScreen())
	_, err = p.Run()
	return err
}

func executeTUI(args []string) {
	tuiCmd := flag.NewFlagSet("tui", flag.ExitOnError)
	configPath := tuiCmd.String("config", "prizm.yaml", "Path to prizm.yaml configuration file")
	tuiCmd.Parse(args)

	cfg, err := orchestrator.LoadConfig(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
		os.Exit(1)
	}

	workspaceRoot := cfg.Prizm.Workspace
	if workspaceRoot == "" {
		home, _ := os.UserHomeDir()
		workspaceRoot = filepath.Join(home, ".prizm")
	}

	planDir := workspaceRoot
	mgr := plan.NewManager(planDir)
	if err := mgr.EnsureDir(); err != nil {
		fmt.Fprintf(os.Stderr, "Error creating plans directory: %v\n", err)
		os.Exit(1)
	}

	if err := runTUI(mgr); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}