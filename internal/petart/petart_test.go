package petart

import (
	"strings"
	"testing"
	"time"

	"github.com/emaharmony/prizm/internal/tracker"
)

func TestPickMoodFromState(t *testing.T) {
	cases := []struct {
		name           string
		snap           tracker.Snapshot
		connected      bool
		sinceLastEvent time.Duration
		want           string
	}{
		{"disconnected sleeps", tracker.Snapshot{Status: "running"}, false, 0, MoodSleeping},
		{"completed is happy", tracker.Snapshot{Status: "completed"}, true, 0, MoodHappy},
		{"paused worries", tracker.Snapshot{Status: "paused"}, true, 0, MoodWorried},
		{"blocked worries", tracker.Snapshot{Status: "blocked"}, true, 0, MoodWorried},
		{"budget worries", tracker.Snapshot{Status: "budget exhausted"}, true, 0, MoodWorried},
		{"stuck phase worries", tracker.Snapshot{Status: "running", Phases: []tracker.PhaseView{{Name: "EXECUTION", Status: "stuck"}}}, true, 100 * time.Millisecond, MoodWorried},
		{"running + recent event thinks", tracker.Snapshot{Status: "running"}, true, 500 * time.Millisecond, MoodThinking},
		{"running + stale event works", tracker.Snapshot{Status: "running"}, true, 10 * time.Second, MoodWorking},
		{"connected idle sleeps", tracker.Snapshot{Status: "connecting"}, true, 0, MoodSleeping},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := pickMood(c.snap, c.connected, c.sinceLastEvent); got != c.want {
				t.Fatalf("pickMood = %q, want %q", got, c.want)
			}
		})
	}
}

func TestArtAnimatesAndIsASCII(t *testing.T) {
	for _, mood := range []string{MoodSleeping, MoodThinking, MoodWorking, MoodHappy, MoodWorried} {
		f0 := Art(mood, 0)
		f1 := Art(mood, 1)
		if f0 == "" {
			t.Fatalf("mood %q has no art", mood)
		}
		if f0 == f1 {
			t.Fatalf("mood %q did not animate between frames", mood)
		}
		// negative and large frame indices must be safe.
		_ = Art(mood, -3)
		_ = Art(mood, 9999)
		for _, r := range f0 + f1 {
			if r > 127 {
				t.Fatalf("mood %q art contains non-ASCII rune %q", mood, r)
			}
		}
	}
}

func TestRenderCaptions(t *testing.T) {
	_, _, capThink := Render(tracker.Snapshot{Status: "running", Current: "PLAN"}, true, 200*time.Millisecond, 0)
	if !strings.Contains(capThink, "Making a plan") {
		t.Fatalf("thinking caption = %q", capThink)
	}
	_, _, capDown := Render(tracker.Snapshot{Status: "running"}, false, 0, 0)
	if !strings.Contains(strings.ToLower(capDown), "reach prizm") {
		t.Fatalf("disconnected caption = %q", capDown)
	}
	mood, _, capDone := Render(tracker.Snapshot{Status: "completed"}, true, 0, 0)
	if mood != MoodHappy || !strings.Contains(capDone, "done") {
		t.Fatalf("done render = %q / %q", mood, capDone)
	}
}
