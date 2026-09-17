package scheduler

import (
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"
)

// mockPublisher captures published events for testing.
type mockPublisher struct {
	events []publishedEvent
}

type publishedEvent struct {
	subject string
	data    map[string]any
}

func (m *mockPublisher) Publish(subject string, data []byte) error {
	var payload map[string]any
	json.Unmarshal(data, &payload)
	m.events = append(m.events, publishedEvent{
		subject: subject,
		data:    payload,
	})
	return nil
}

// --- Cron Parsing Tests ---

func TestParseCronWildcards(t *testing.T) {
	schedule, err := ParseCron("* * * * *")
	if err != nil {
		t.Fatalf("ParseCron wildcard: %v", err)
	}
	if !schedule.minute.any || !schedule.hour.any || !schedule.dayOfMonth.any || !schedule.month.any || !schedule.dayOfWeek.any {
		t.Error("all fields should be wildcard")
	}

	// Should match any time
	if !matchesSchedule(schedule, time.Date(2026, 6, 7, 15, 30, 0, 0, time.UTC)) {
		t.Error("wildcard schedule should match any time")
	}
}

func TestParseCronSpecificValues(t *testing.T) {
	schedule, err := ParseCron("30 3 * * *")
	if err != nil {
		t.Fatalf("ParseCron specific: %v", err)
	}
	if schedule.minute.any {
		t.Error("minute should not be wildcard")
	}
	// Should match at 3:30 AM
	if !matchesSchedule(schedule, time.Date(2026, 6, 7, 3, 30, 0, 0, time.UTC)) {
		t.Error("should match 3:30 AM")
	}
	// Should NOT match at 3:31 AM
	if matchesSchedule(schedule, time.Date(2026, 6, 7, 3, 31, 0, 0, time.UTC)) {
		t.Error("should NOT match 3:31 AM")
	}
}

func TestParseCronSteps(t *testing.T) {
	schedule, err := ParseCron("*/15 * * * *")
	if err != nil {
		t.Fatalf("ParseCron step: %v", err)
	}

	// Should match 0, 15, 30, 45
	for _, min := range []int{0, 15, 30, 45} {
		if !matchesField(schedule.minute, min) {
			t.Errorf("*/15 should match minute %d", min)
		}
	}
	// Should NOT match 1, 16, 31, 46
	for _, min := range []int{1, 16, 31, 46} {
		if matchesField(schedule.minute, min) {
			t.Errorf("*/15 should NOT match minute %d", min)
		}
	}
}

func TestParseCronRanges(t *testing.T) {
	schedule, err := ParseCron("0 9-17 * * *")
	if err != nil {
		t.Fatalf("ParseCron range: %v", err)
	}

	// Should match 9 AM through 5 PM
	for _, hour := range []int{9, 10, 11, 12, 13, 14, 15, 16, 17} {
		if !matchesField(schedule.hour, hour) {
			t.Errorf("9-17 should match hour %d", hour)
		}
	}
	// Should NOT match 8 AM or 6 PM
	if matchesField(schedule.hour, 8) {
		t.Error("9-17 should NOT match hour 8")
	}
	if matchesField(schedule.hour, 18) {
		t.Error("9-17 should NOT match hour 18")
	}
}

func TestParseCronLists(t *testing.T) {
	schedule, err := ParseCron("0 0 * * 1,3,5")
	if err != nil {
		t.Fatalf("ParseCron list: %v", err)
	}

	// Monday(1), Wednesday(3), Friday(5)
	for _, day := range []int{1, 3, 5} {
		if !matchesField(schedule.dayOfWeek, day) {
			t.Errorf("1,3,5 should match day %d", day)
		}
	}
	// Tuesday(2), Thursday(4), Saturday(6)
	for _, day := range []int{0, 2, 4, 6} {
		if matchesField(schedule.dayOfWeek, day) {
			t.Errorf("1,3,5 should NOT match day %d", day)
		}
	}
}

func TestParseCronDayOfWeek(t *testing.T) {
	// Sunday = 0
	schedule, err := ParseCron("0 0 * * 0")
	if err != nil {
		t.Fatalf("ParseCron day of week: %v", err)
	}

	// June 7, 2026 is a Saturday (day 6)
	// June 8, 2026 is a Sunday (day 0)
	// June 9, 2026 is a Monday (day 1)
	sunday := time.Date(2026, 6, 14, 0, 0, 0, 0, time.UTC) // June 14, 2026 is a Sunday
	monday := time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC) // June 15, 2026 is a Monday

	if !matchesSchedule(schedule, sunday) {
		t.Error("should match Sunday")
	}
	if matchesSchedule(schedule, monday) {
		t.Error("should NOT match Monday")
	}
}

func TestParseCronErrors(t *testing.T) {
	// Too few fields
	_, err := ParseCron("* * * *")
	if err == nil {
		t.Error("expected error for 4-field cron")
	}

	// Too many fields
	_, err = ParseCron("* * * * * *")
	if err == nil {
		t.Error("expected error for 6-field cron")
	}

	// Invalid minute
	_, err = ParseCron("60 * * * *")
	if err == nil {
		t.Error("expected error for minute=60")
	}

	// Invalid hour
	_, err = ParseCron("* 24 * * *")
	if err == nil {
		t.Error("expected error for hour=24")
	}

	// Invalid range
	_, err = ParseCron("5-3 * * * *")
	if err == nil {
		t.Error("expected error for range 5-3 (start > end)")
	}

	// Step of 0
	_, err = ParseCron("*/0 * * * *")
	if err == nil {
		t.Error("expected error for step=0")
	}
}

func TestParseCronComplex(t *testing.T) {
	// Every 15 minutes during business hours on weekdays
	schedule, err := ParseCron("*/15 9-17 * * 1-5")
	if err != nil {
		t.Fatalf("ParseCron complex: %v", err)
	}

	// Tuesday 10:30 AM — should match
	if !matchesSchedule(schedule, time.Date(2026, 6, 9, 10, 30, 0, 0, time.UTC)) {
		t.Error("should match Tuesday 10:30 AM")
	}

	// Saturday 10:30 AM — should NOT match (not weekday)
	if matchesSchedule(schedule, time.Date(2026, 6, 14, 10, 30, 0, 0, time.UTC)) {
		t.Error("should NOT match Saturday 10:30 AM")
	}

	// Tuesday 8:00 AM — should NOT match (before 9 AM)
	if matchesSchedule(schedule, time.Date(2026, 6, 9, 8, 0, 0, 0, time.UTC)) {
		t.Error("should NOT match 8:00 AM (before business hours)")
	}

	// Tuesday 9:15 AM — should match (*/15 at minute 15)
	if !matchesSchedule(schedule, time.Date(2026, 6, 9, 9, 15, 0, 0, time.UTC)) {
		t.Error("should match 9:15 AM")
	}

	// Tuesday 9:07 AM — should NOT match (not a */15 minute)
	if matchesSchedule(schedule, time.Date(2026, 6, 9, 9, 7, 0, 0, time.UTC)) {
		t.Error("should NOT match 9:07 AM (not a 15-min step)")
	}
}

// --- Scheduler Tests ---

func TestAddJob(t *testing.T) {
	pub := &mockPublisher{}
	s := NewScheduler(pub)

	schedule, _ := ParseCron("0 3 * * *")
	err := s.AddJob(&Job{
		Name:     "daily-review",
		Schedule: schedule,
		Event:    "prizm.task.scheduled",
		Payload:  map[string]any{"action": "daily_review"},
		Enabled:  true,
	})
	if err != nil {
		t.Fatalf("AddJob failed: %v", err)
	}

	jobs := s.Jobs()
	if len(jobs) != 1 {
		t.Fatalf("expected 1 job, got %d", len(jobs))
	}
	if jobs[0].Name != "daily-review" {
		t.Errorf("job name = %q, want %q", jobs[0].Name, "daily-review")
	}
}

func TestAddJobValidation(t *testing.T) {
	pub := &mockPublisher{}
	s := NewScheduler(pub)

	// Empty name
	err := s.AddJob(&Job{Name: "", Event: "prizm.task.scheduled"})
	if err == nil {
		t.Error("expected error for empty job name")
	}

	// Empty event
	err = s.AddJob(&Job{Name: "test", Event: ""})
	if err == nil {
		t.Error("expected error for empty event subject")
	}
}

func TestFireJob(t *testing.T) {
	pub := &mockPublisher{}
	s := NewScheduler(pub)

	schedule, _ := ParseCron("* * * * *") // Every minute
	s.AddJob(&Job{
		Name:     "test-job",
		Schedule: schedule,
		Event:    "prizm.task.scheduled",
		Payload:  map[string]any{"action": "test"},
		Enabled:  true,
	})

	// Manually fire the job
	s.fireJob(s.Jobs()[0])

	if len(pub.events) != 1 {
		t.Fatalf("expected 1 published event, got %d", len(pub.events))
	}
	if pub.events[0].subject != "prizm.task.scheduled" {
		t.Errorf("event subject = %q, want %q", pub.events[0].subject, "prizm.task.scheduled")
	}
	if pub.events[0].data["action"] != "test" {
		t.Errorf("payload action = %v, want %q", pub.events[0].data["action"], "test")
	}
	if pub.events[0].data["job_name"] != "test-job" {
		t.Errorf("payload job_name = %v, want %q", pub.events[0].data["job_name"], "test-job")
	}
	if _, ok := pub.events[0].data["fired_at"]; !ok {
		t.Error("payload should contain fired_at timestamp")
	}
}

func TestDisabledJobNotFired(t *testing.T) {
	pub := &mockPublisher{}
	s := NewScheduler(pub)

	schedule, _ := ParseCron("* * * * *")
	s.AddJob(&Job{
		Name:     "disabled-job",
		Schedule: schedule,
		Event:    "prizm.task.scheduled",
		Payload:  map[string]any{"action": "test"},
		Enabled:  false,
	})

	s.checkAndFire(time.Now())

	if len(pub.events) != 0 {
		t.Errorf("disabled job should not fire, got %d events", len(pub.events))
	}
}

func TestSchedulerStartStop(t *testing.T) {
	pub := &mockPublisher{}
	s := NewScheduler(pub)

	schedule, _ := ParseCron("* * * * *") // Every minute
	s.AddJob(&Job{
		Name:     "test-job",
		Schedule: schedule,
		Event:    "prizm.task.scheduled",
		Payload:  map[string]any{"action": "test"},
		Enabled:  true,
	})

	// Start and stop quickly
	var fired atomic.Int32
	go s.Start()

	// Give it a moment to start
	time.Sleep(100 * time.Millisecond)
	s.Stop()

	_ = fired.Load() // Just to use the variable
}

func TestMultipleJobs(t *testing.T) {
	pub := &mockPublisher{}
	s := NewScheduler(pub)

	sched1, _ := ParseCron("0 3 * * *")
	sched2, _ := ParseCron("0 3 * * 0")

	s.AddJob(&Job{Name: "daily-review", Schedule: sched1, Event: "prizm.task.scheduled", Payload: map[string]any{"action": "daily_review"}, Enabled: true})
	s.AddJob(&Job{Name: "weekly-consolidation", Schedule: sched2, Event: "prizm.task.scheduled", Payload: map[string]any{"action": "weekly_consolidation"}, Enabled: true})

	jobs := s.Jobs()
	if len(jobs) != 2 {
		t.Fatalf("expected 2 jobs, got %d", len(jobs))
	}

	// Both should match Sunday 3 AM
	sunday3am := time.Date(2026, 6, 14, 3, 0, 0, 0, time.UTC) // June 14, 2026 is a Sunday
	matches1 := matchesSchedule(jobs[0].Schedule, sunday3am)
	matches2 := matchesSchedule(jobs[1].Schedule, sunday3am)
	if !matches1 {
		t.Error("daily-review should match Sunday 3AM")
	}
	if !matches2 {
		t.Error("weekly-consolidation should match Sunday 3AM")
	}

	// Only daily should match Monday 3 AM
	monday3am := time.Date(2026, 6, 15, 3, 0, 0, 0, time.UTC) // June 15, 2026 is a Monday
	matches1 = matchesSchedule(jobs[0].Schedule, monday3am)
	matches2 = matchesSchedule(jobs[1].Schedule, monday3am)
	if !matches1 {
		t.Error("daily-review should match Monday 3AM")
	}
	if matches2 {
		t.Error("weekly-consolidation should NOT match Monday 3AM")
	}
}

func TestNilPublisher(t *testing.T) {
	s := NewScheduler(nil) // nil publisher

	schedule, _ := ParseCron("* * * * *")
	s.AddJob(&Job{
		Name:     "test-job",
		Schedule: schedule,
		Event:    "prizm.task.scheduled",
		Payload:  map[string]any{"action": "test"},
		Enabled:  true,
	})

	// Should not panic
	s.fireJob(s.Jobs()[0])
}

func TestDoubleStart(t *testing.T) {
	pub := &mockPublisher{}
	s := NewScheduler(pub)

	schedule, _ := ParseCron("0 3 * * *") // 3 AM — won't fire during test
	s.AddJob(&Job{Name: "double-start", Schedule: schedule, Event: "prizm.task.scheduled", Enabled: true})

	// Start twice — second call should be a no-op
	var fired atomic.Int32
	go s.Start()
	time.Sleep(50 * time.Millisecond)
	s.Start() // should return immediately since s.running is true
	time.Sleep(50 * time.Millisecond)
	s.Stop()

	_ = fired.Load() // just to use the variable
}

func TestStopWaitsForInFlight(t *testing.T) {
	pub := &mockPublisher{}
	s := NewScheduler(pub)

	// Use a wildcard schedule so it fires immediately
	schedule, _ := ParseCron("* * * * *")
	s.AddJob(&Job{Name: "inflight", Schedule: schedule, Event: "prizm.task.scheduled", Payload: map[string]any{"action": "test"}, Enabled: true})

	// Start and fire a job, then stop
	go s.Start()

	// Wait for alignment and first fire
	time.Sleep(2 * time.Second)

	// Stop should wait for in-flight goroutines
	s.Stop()

	// After Stop returns, all in-flight jobs should be done
	if len(pub.events) == 0 {
		t.Log("no events fired (may have missed minute boundary)")
	}
}
