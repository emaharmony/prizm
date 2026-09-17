package main

import (
	"log"
	"sync/atomic"
	"time"

	remcli "github.com/emaharmony/prizm/internal/remembrance"
)

// dreamPersistCount tracks PERSIST captures for event-based dream triggering.
var dreamPersistCount int64

// startDreamScheduler runs the Remembrance dream cycle on a schedule.
//
// Two triggers:
//  1. Nightly cron at 3:00 AM local time — full maintenance cycle
//  2. Event trigger: after 10 PERSIST captures, run a partial cycle
//
// The scheduler runs in a goroutine and exits when the process stops.
func startDreamScheduler(client *remcli.Client) {
	persistThreshold := int64(10)

	// Calculate duration until next 3AM
	next3AM := func() time.Duration {
		now := time.Now()
		next := time.Date(now.Year(), now.Month(), now.Day(), 3, 0, 0, 0, now.Location())
		if now.After(next) {
			next = next.Add(24 * time.Hour)
		}
		return next.Sub(now)
	}

	// Nightly dream at 3AM
	nightlyTimer := time.NewTimer(next3AM())
	defer nightlyTimer.Stop()

	// Check every 5 minutes for event-based trigger
	checkTicker := time.NewTicker(5 * time.Minute)
	defer checkTicker.Stop()

	// Check health periodically
	healthTicker := time.NewTicker(2 * time.Minute)
	defer healthTicker.Stop()

	log.Printf("[DREAM] Scheduler started — nightly at 3:00 AM, event trigger after %d PERSIST captures", persistThreshold)

	for {
		select {
		case <-nightlyTimer.C:
			// Nightly full dream cycle
			log.Printf("[DREAM] Starting nightly dream cycle (3:00 AM)")
			runDreamCycle(client, nil)
			// Reset timer for next 3AM
			nightlyTimer.Reset(next3AM())

		case <-checkTicker.C:
			// Check if we've hit the PERSIST threshold
			count := atomic.LoadInt64(&dreamPersistCount)
			if count >= persistThreshold {
				log.Printf("[DREAM] Event trigger: %d PERSIST captures reached, running partial cycle", count)
				phases := []string{"entity_sweep", "backlink_audit", "embed"}
				runDreamCycle(client, phases)
				atomic.StoreInt64(&dreamPersistCount, 0) // reset counter
			}

		case <-healthTicker.C:
			// Periodic health check — if Remembrance goes down, log it
			if !client.IsAvailable() {
				log.Printf("[DREAM] Remembrance service not available, will retry")
			}
		}
	}
}

// runDreamCycle calls the Remembrance dream endpoint.
func runDreamCycle(client *remcli.Client, phases []string) {
	start := time.Now()
	result, err := client.Dream(phases, false)
	if err != nil {
		log.Printf("[DREAM] Dream cycle failed: %v", err)
		return
	}
	duration := time.Since(start).Round(time.Millisecond)

	if status, ok := result["status"].(string); ok {
		log.Printf("[DREAM] Dream cycle completed: status=%s, duration=%s", status, duration)
	} else {
		log.Printf("[DREAM] Dream cycle completed in %s", duration)
	}
}
