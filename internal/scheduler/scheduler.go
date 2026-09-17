// Package scheduler provides cron-style task scheduling that publishes NATS events.
//
// Each job has a name, a cron schedule, and an event payload. When the schedule
// fires, the job publishes a NATS event that downstream handlers (wake handlers)
// can subscribe to and act on.
//
// Schedules use standard cron expressions:
//   - "*/30 * * * *" — every 30 minutes
//   - "0 3 * * *"    — daily at 3:00 AM
//   - "0 3 * * 0"    — weekly Sunday at 3:00 AM
//
// V32: Lumi Operating Environment — event-driven wake replaces heartbeat babysitting.
package scheduler

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
)

// NatsPublisher is the interface for publishing events. Matches the same
// interface used in cmd_serve.go (natsPublisherAdapter).
type NatsPublisher interface {
	Publish(subject string, data []byte) error
}

// Job defines a scheduled task that fires a NATS event.
type Job struct {
	Name     string         // Human-readable job name (e.g., "daily-review")
	Schedule CronSchedule   // When to run
	Event    string         // NATS subject to publish (e.g., "prizm.task.scheduled")
	Payload  map[string]any // JSON payload for the event
	Enabled  bool           // Whether this job is active
}

// CronSchedule parses and evaluates cron expressions.
// Format: minute hour dayOfMonth month dayOfWeek
// Supports: *, ranges (1-5), steps (*/15), and lists (1,3,5)
type CronSchedule struct {
	minute     field
	hour       field
	dayOfMonth field
	month      field
	dayOfWeek  field
}

// field represents a cron field with bit-set matching.
type field struct {
	bits uint64 // bit N is set if value N matches
	any  bool   // true if * (matches any)
}

// Scheduler manages scheduled jobs and fires them at the right time.
type Scheduler struct {
	jobs      []*Job
	publisher NatsPublisher
	mu        sync.RWMutex
	stopCh    chan struct{}
	wg        sync.WaitGroup // tracks in-flight fireJob goroutines
	running   bool
}

// NewScheduler creates a scheduler with the given NATS publisher.
func NewScheduler(publisher NatsPublisher) *Scheduler {
	return &Scheduler{
		publisher: publisher,
		stopCh:    make(chan struct{}),
	}
}

// AddJob adds a scheduled job to the scheduler.
func (s *Scheduler) AddJob(job *Job) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if job.Name == "" {
		return fmt.Errorf("job name is required")
	}
	if job.Event == "" {
		return fmt.Errorf("job event subject is required")
	}

	s.jobs = append(s.jobs, job)
	log.Printf("[SCHEDULER] added job %q: event=%s enabled=%v", job.Name, job.Event, job.Enabled)
	return nil
}

// Start begins the scheduler loop. It checks jobs every minute.
// This is a blocking call — run it in a goroutine or use StartInBackground.
func (s *Scheduler) Start() {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return
	}
	s.running = true
	s.mu.Unlock()

	log.Printf("[SCHEDULER] starting, %d job(s) registered", len(s.jobs))

	// Align to the next minute boundary for consistent ticking.
	now := time.Now()
	nextMinute := now.Truncate(time.Minute).Add(time.Minute)
	initialDelay := nextMinute.Sub(now)

	// Wait until the next minute boundary before starting the ticker.
	// This ensures we fire at consistent minute boundaries, not at
	// whatever second the scheduler happened to start.
	time.Sleep(initialDelay)

	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	// Fire immediately at the aligned minute
	s.checkAndFire(time.Now())

	for {
		select {
		case <-s.stopCh:
			log.Printf("[SCHEDULER] stopped")
			return
		case t := <-ticker.C:
			s.checkAndFire(t)
		}
	}
}

// Stop gracefully stops the scheduler and waits for in-flight jobs to complete.
func (s *Scheduler) Stop() {
	s.mu.Lock()
	if s.running {
		s.running = false
		close(s.stopCh)
	}
	s.mu.Unlock()

	// Wait for all in-flight fireJob goroutines to finish.
	// This prevents log errors from publishing after shutdown.
	s.wg.Wait()
	log.Printf("[SCHEDULER] all in-flight jobs completed")
}

// StartInBackground starts the scheduler in a goroutine.
// This is a convenience method for the common pattern: go sched.Start().
func (s *Scheduler) StartInBackground() {
	go s.Start()
}

// Jobs returns a copy of the current job list.
func (s *Scheduler) Jobs() []*Job {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]*Job, len(s.jobs))
	copy(result, s.jobs)
	return result
}

// checkAndFire checks all enabled jobs against the given time and fires
// any that match.
func (s *Scheduler) checkAndFire(t time.Time) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, job := range s.jobs {
		if !job.Enabled {
			continue
		}
		if matchesSchedule(job.Schedule, t) {
			s.wg.Add(1)
			go func(j *Job) {
				defer s.wg.Done()
				s.fireJob(j)
			}(job)
		}
	}
}

// fireJob publishes the job's event to NATS.
func (s *Scheduler) fireJob(job *Job) {
	payload := make(map[string]any)
	for k, v := range job.Payload {
		payload[k] = v
	}
	payload["job_name"] = job.Name
	payload["fired_at"] = time.Now().Format(time.RFC3339)

	data, err := json.Marshal(payload)
	if err != nil {
		log.Printf("[SCHEDULER] ERROR marshaling job %q payload: %v", job.Name, err)
		return
	}

	if s.publisher == nil {
		log.Printf("[SCHEDULER] WARN no publisher, skipping job %q", job.Name)
		return
	}

	if err := s.publisher.Publish(job.Event, data); err != nil {
		log.Printf("[SCHEDULER] ERROR publishing job %q: %v", job.Name, err)
		return
	}

	log.Printf("[SCHEDULER] fired job %q → %s", job.Name, job.Event)
}

// --- Cron Expression Parsing ---

// matchesSchedule checks if the given time matches the cron schedule.
func matchesSchedule(schedule CronSchedule, t time.Time) bool {
	return matchesField(schedule.minute, t.Minute()) &&
		matchesField(schedule.hour, t.Hour()) &&
		matchesField(schedule.dayOfMonth, t.Day()) &&
		matchesField(schedule.month, int(t.Month())) &&
		matchesField(schedule.dayOfWeek, int(t.Weekday()))
}

// matchesField checks if a value matches a cron field.
func matchesField(f field, value int) bool {
	if f.any {
		return true
	}
	if value >= 0 && value < 64 {
		return (f.bits>>uint(value))&1 == 1
	}
	return false
}

// ParseCron parses a cron expression into a CronSchedule.
// Format: minute hour dayOfMonth month dayOfWeek
// Supports: * (any), */N (step), N-M (range), N,M,K (list)
func ParseCron(expr string) (CronSchedule, error) {
	parts := strings.Fields(expr) // Use Fields to handle multiple spaces
	if len(parts) != 5 {
		return CronSchedule{}, fmt.Errorf("cron expression must have 5 fields, got %d: %q", len(parts), expr)
	}

	minute, err := parseField(parts[0], 0, 59)
	if err != nil {
		return CronSchedule{}, fmt.Errorf("minute field: %w", err)
	}
	hour, err := parseField(parts[1], 0, 23)
	if err != nil {
		return CronSchedule{}, fmt.Errorf("hour field: %w", err)
	}
	dayOfMonth, err := parseField(parts[2], 1, 31)
	if err != nil {
		return CronSchedule{}, fmt.Errorf("day of month field: %w", err)
	}
	month, err := parseField(parts[3], 1, 12)
	if err != nil {
		return CronSchedule{}, fmt.Errorf("month field: %w", err)
	}
	dayOfWeek, err := parseField(parts[4], 0, 6)
	if err != nil {
		return CronSchedule{}, fmt.Errorf("day of week field: %w", err)
	}

	return CronSchedule{
		minute:     minute,
		hour:       hour,
		dayOfMonth: dayOfMonth,
		month:      month,
		dayOfWeek:  dayOfWeek,
	}, nil
}

// parseField parses a single cron field.
func parseField(s string, min, max int) (field, error) {
	if s == "*" {
		return field{any: true}, nil
	}

	var f field

	parts := strings.Split(s, ",")
	for _, part := range parts {
		if strings.Contains(part, "/") {
			stepParts := strings.SplitN(part, "/", 2)
			rangePart := stepParts[0]
			step, err := atoi(stepParts[1])
			if err != nil {
				return field{}, fmt.Errorf("invalid step %q: %w", stepParts[1], err)
			}
			if step <= 0 {
				return field{}, fmt.Errorf("step must be positive, got %d", step)
			}

			var rangeMin, rangeMax int
			if rangePart == "*" {
				rangeMin = min
				rangeMax = max
			} else if strings.Contains(rangePart, "-") {
				rp, err := parseRange(rangePart)
				if err != nil {
					return field{}, err
				}
				rangeMin = rp[0]
				rangeMax = rp[1]
			} else {
				v, err := atoi(rangePart)
				if err != nil {
					return field{}, fmt.Errorf("invalid range start %q: %w", rangePart, err)
				}
				rangeMin = v
				rangeMax = max
			}

			for i := rangeMin; i <= rangeMax; i += step {
				if i >= min && i <= max {
					f.bits |= 1 << uint(i)
				}
			}
			continue
		}

		if strings.Contains(part, "-") {
			rp, err := parseRange(part)
			if err != nil {
				return field{}, err
			}
			for i := rp[0]; i <= rp[1]; i++ {
				f.bits |= 1 << uint(i)
			}
			continue
		}

		val, err := atoi(part)
		if err != nil {
			return field{}, fmt.Errorf("invalid value %q: %w", part, err)
		}
		if val < min || val > max {
			return field{}, fmt.Errorf("value %d out of range [%d, %d]", val, min, max)
		}
		f.bits |= 1 << uint(val)
	}

	return f, nil
}

// parseRange parses a range like "1-5" into [1, 5].
func parseRange(s string) ([2]int, error) {
	parts := strings.SplitN(s, "-", 2)
	start, err := atoi(parts[0])
	if err != nil {
		return [2]int{}, fmt.Errorf("invalid range start %q: %w", parts[0], err)
	}
	end, err := atoi(parts[1])
	if err != nil {
		return [2]int{}, fmt.Errorf("invalid range end %q: %w", parts[1], err)
	}
	if start > end {
		return [2]int{}, fmt.Errorf("range start %d > end %d", start, end)
	}
	return [2]int{start, end}, nil
}

// atoi is a simple integer parser that avoids importing strconv.
func atoi(s string) (int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty string")
	}
	neg := false
	if s[0] == '-' {
		neg = true
		s = s[1:]
	}
	if len(s) == 0 {
		return 0, fmt.Errorf("just a minus sign")
	}
	var n int
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			return 0, fmt.Errorf("invalid character %q", ch)
		}
		n = n*10 + int(ch-'0')
	}
	if neg {
		n = -n
	}
	return n, nil
}
