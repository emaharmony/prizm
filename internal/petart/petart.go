// Package petart renders a small ASCII "pet" whose mood tracks a running Prizm
// workflow. It is the friendly, non-developer-facing face of `prizm watch`: instead
// of phase jargon, a little creature reacts to what the agent is doing — sleeping
// when idle, thinking while the agent works, celebrating on success, worried when
// something needs a human.
//
// It is deliberately free of any GUI dependency (no Fyne) so the mood/frame logic is
// unit-testable and reusable. The desktop panel (cmd/prizm-panel) is a thin renderer
// on top of Render(). All output is strict ASCII.
package petart

import (
	"strings"
	"time"

	"github.com/emaharmony/prizm/internal/tracker"
)

// Mood names.
const (
	MoodSleeping = "sleeping"
	MoodThinking = "thinking"
	MoodWorking  = "working"
	MoodHappy    = "happy"
	MoodWorried  = "worried"
)

// ThinkingWindow is how recently an event must have arrived for a running workflow
// to count as actively "thinking" (vs. merely running/resting).
const ThinkingWindow = 2500 * time.Millisecond

// Render picks the pet's mood from live state and returns the mood name, the ASCII
// art for the given animation frame, and a plain-language caption a non-developer
// can read at a glance.
//
//   - connected:       whether the panel currently has an SSE connection.
//   - sinceLastEvent:  time since the last workflow event was applied.
//   - frame:           monotonically increasing animation counter.
func Render(s tracker.Snapshot, connected bool, sinceLastEvent time.Duration, frame int) (mood, art, caption string) {
	mood = pickMood(s, connected, sinceLastEvent)
	return mood, Art(mood, frame), caption_(mood, s, connected)
}

func pickMood(s tracker.Snapshot, connected bool, sinceLastEvent time.Duration) string {
	if !connected {
		return MoodSleeping
	}
	switch {
	case strings.Contains(s.Status, "complete"):
		return MoodHappy
	case s.Status == "paused" || s.Status == "blocked" || strings.Contains(s.Status, "budget"):
		return MoodWorried
	}
	if hasStuck(s) {
		return MoodWorried
	}
	if s.Status == "running" {
		if sinceLastEvent >= 0 && sinceLastEvent < ThinkingWindow {
			return MoodThinking
		}
		return MoodWorking
	}
	// connecting / empty / idle-but-connected
	return MoodSleeping
}

func hasStuck(s tracker.Snapshot) bool {
	for _, p := range s.Phases {
		if p.Status == "stuck" || p.Status == "blocked" {
			return true
		}
	}
	return false
}

// Art returns the ASCII creature for a mood at the given animation frame. frame is
// taken modulo the number of frames for that mood, so any int is valid.
func Art(mood string, frame int) string {
	frames := petFrames[mood]
	if len(frames) == 0 {
		frames = petFrames[MoodSleeping]
	}
	idx := frame % len(frames)
	if idx < 0 {
		idx += len(frames)
	}
	return frames[idx]
}

func caption_(mood string, s tracker.Snapshot, connected bool) string {
	switch mood {
	case MoodHappy:
		return "All done!"
	case MoodWorried:
		switch {
		case s.Status == "paused":
			return "Waiting for your approval."
		case s.Status == "blocked":
			return "Stuck - needs a human."
		case strings.Contains(s.Status, "budget"):
			return "Ran out of budget for now."
		default:
			return "Hmm, something needs a look."
		}
	case MoodThinking:
		return "Thinking - " + phaseFriendly(s.Current)
	case MoodWorking:
		return phaseFriendly(s.Current) + "..."
	default: // sleeping
		if !connected {
			return "Can't reach Prizm - retrying..."
		}
		return "Resting. Nothing running right now."
	}
}

// phaseFriendly maps engine phase names to plain language for non-developers.
func phaseFriendly(phase string) string {
	switch phase {
	case "PROBE":
		return "Looking around"
	case "RESEARCH":
		return "Doing research"
	case "PLAN":
		return "Making a plan"
	case "FEEDBACK_PRE":
		return "Checking the plan with you"
	case "EXECUTION":
		return "Doing the work"
	case "FEEDBACK_POST":
		return "Reviewing the results with you"
	case "REPORT":
		return "Writing up what happened"
	case "":
		return "Getting started"
	default:
		return phase
	}
}

// petFrames holds 2+ ASCII frames per mood. Frames within a mood must differ so the
// creature visibly animates. Edit these freely to redesign the pet.
var petFrames = map[string][]string{
	MoodSleeping: {
		"" +
			"     .-\"\"\"-.\n" +
			"    / .   . \\\n" +
			"   |  -   -  |   z\n" +
			"   |   ___   |  z\n" +
			"    \\ '---' /\n" +
			"     '-----'\n",
		"" +
			"     .-\"\"\"-.\n" +
			"    / .   . \\\n" +
			"   |  -   -  |    z\n" +
			"   |   ___   |   Z\n" +
			"    \\ '---' /  z\n" +
			"     '-----'\n",
	},
	MoodThinking: {
		"" +
			"          o O ?\n" +
			"     .-\"\"\"-.\n" +
			"    / ^   ^ \\\n" +
			"   |  o   o  |\n" +
			"   |    ^    |\n" +
			"    \\  \\_/  /\n" +
			"     '-----'\n",
		"" +
			"        . o O\n" +
			"     .-\"\"\"-.\n" +
			"    / ^   ^ \\\n" +
			"   |  O   O  |\n" +
			"   |    ^    |\n" +
			"    \\  \\_/  /\n" +
			"     '-----'\n",
		"" +
			"          O ? o\n" +
			"     .-\"\"\"-.\n" +
			"    / ^   ^ \\\n" +
			"   |  o   O  |\n" +
			"   |    ^    |\n" +
			"    \\  \\_/  /\n" +
			"     '-----'\n",
	},
	MoodWorking: {
		"" +
			"     .-\"\"\"-.\n" +
			"   \\/ o   o \\/\n" +
			"   /|  o   o  |\\\n" +
			"    |    o    |\n" +
			"    \\  \\___/  /\n" +
			"     '-----'\n",
		"" +
			"     .-\"\"\"-.\n" +
			"   /\\ o   o /\\\n" +
			"   \\|  o   o  |/\n" +
			"    |    o    |\n" +
			"    \\  \\___/  /\n" +
			"     '-----'\n",
	},
	MoodHappy: {
		"" +
			"    \\o/   .-\"\"\"-.\n" +
			"     |   / ^   ^ \\\n" +
			"    / \\ |  ^   ^  |\n" +
			"        |    u    |\n" +
			"         \\  \\_/  /\n" +
			"          '-----'\n",
		"" +
			"   \\o/    .-\"\"\"-.\n" +
			"    |    / ^   ^ \\\n" +
			"   / \\  |  ^   ^  |\n" +
			"        |   \\_/   |\n" +
			"         \\  \\_/  /\n" +
			"          '-----'\n",
	},
	MoodWorried: {
		"" +
			"          ?\n" +
			"     .-\"\"\"-.\n" +
			"    / o   o \\\n" +
			"   |  o   o  |\n" +
			"   |    .    |\n" +
			"    \\  /^\\  /\n" +
			"     '-----'\n",
		"" +
			"        ?\n" +
			"     .-\"\"\"-.\n" +
			"    / o   o \\\n" +
			"   |  o   o  |\n" +
			"   |    .    |\n" +
			"    \\  /^\\  /\n" +
			"     '-----'\n",
	},
}
