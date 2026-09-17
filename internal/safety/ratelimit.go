// Package safety provides Prizm's rate limiting infrastructure.
//
// Rate limiting prevents abuse from:
//   - Users spamming the bot with rapid-fire messages
//   - Tool loops that run out of control
//   - Denial-of-service attacks
//
// Design: Token bucket algorithm with per-user and global limits.
// Per-user limits prevent one user from monopolizing the bot.
// Global limits protect the system even if many users hit at once.
package safety

import (
	"sync"
	"time"
)

// RateLimiter implements a token bucket rate limiter.
type RateLimiter struct {
	mu         sync.Mutex
	tokens     int
	maxTokens  int
	refillRate int // tokens per second
	lastRefill time.Time
}

// NewRateLimiter creates a token bucket limiter.
// maxTokens is the burst capacity, refillRate is tokens added per second.
func NewRateLimiter(maxTokens, refillRate int) *RateLimiter {
	return &RateLimiter{
		tokens:     maxTokens,
		maxTokens:  maxTokens,
		refillRate: refillRate,
		lastRefill: time.Now(),
	}
}

// Allow checks if a request is allowed under the rate limit.
// Returns true if the request should proceed, false if it should be rejected.
func (r *RateLimiter) Allow() bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now()
	elapsed := now.Sub(r.lastRefill).Seconds()
	r.tokens += int(elapsed * float64(r.refillRate))
	if r.tokens > r.maxTokens {
		r.tokens = r.maxTokens
	}
	r.lastRefill = now

	if r.tokens <= 0 {
		return false
	}

	r.tokens--
	return true
}

// UserRateLimiter provides per-user rate limiting with a global fallback.
type UserRateLimiter struct {
	mu          sync.RWMutex
	userLimits  map[string]*RateLimiter
	globalLimit *RateLimiter

	// Config
	maxTokensPerUser int
	refillPerUser    int // tokens per second per user
}

// NewUserRateLimiter creates a per-user rate limiter with a global cap.
func NewUserRateLimiter(maxPerUser, refillPerUser, globalMax, globalRefill int) *UserRateLimiter {
	return &UserRateLimiter{
		userLimits:       make(map[string]*RateLimiter),
		globalLimit:      NewRateLimiter(globalMax, globalRefill),
		maxTokensPerUser: maxPerUser,
		refillPerUser:    refillPerUser,
	}
}

// Allow checks if a user's request is allowed. Checks both per-user and global limits.
func (u *UserRateLimiter) Allow(userID string) bool {
	// Check global limit first
	if !u.globalLimit.Allow() {
		return false
	}

	// Check per-user limit
	u.mu.Lock()
	limiter, ok := u.userLimits[userID]
	if !ok {
		limiter = NewRateLimiter(u.maxTokensPerUser, u.refillPerUser)
		u.userLimits[userID] = limiter
	}
	u.mu.Unlock()

	return limiter.Allow()
}

// CleanupStale removes rate limiters for users who haven't been active.
// Call this periodically (e.g., every 10 minutes) to prevent memory leaks.
func (u *UserRateLimiter) CleanupStale(maxAge time.Duration) {
	u.mu.Lock()
	defer u.mu.Unlock()

	// For simplicity, remove limiters that have full tokens (idle users)
	// A more sophisticated approach would track last activity time
	for id, limiter := range u.userLimits {
		limiter.mu.Lock()
		if limiter.tokens >= limiter.maxTokens {
			delete(u.userLimits, id)
		}
		limiter.mu.Unlock()
	}
}
