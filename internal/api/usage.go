package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/emaharmony/prizm/internal/usage"
)

// This file implements GET /api/v1/usage — the token-usage tracker surface. It
// reads the persistent usage store (every LLM call routed through the provider
// registry is recorded there) and returns a time-bucketed series plus
// by-agent / by-provider / by-model / by-source breakdowns for a chosen range.

const msPerHour = int64(time.Hour / time.Millisecond)
const msPerDay = 24 * msPerHour

// WindowSpec describes how one usage range maps to a query window and bucket.
// Kind selects how the lower bound is derived: "session" starts at process
// start, "lifetime" starts at 0, and "fixed" looks back WindowMs from now.
type WindowSpec struct {
	Kind     string // "session" | "lifetime" | "fixed"
	WindowMs int64  // lookback length for Kind == "fixed"
	BucketMs int64  // bucket width
	Bucket   string // human label for BucketMs (e.g. "6h")
}

// DefaultUsageWindows returns the built-in range→window/bucket mapping. Bucket
// widths are chosen to keep each range around 24–90 points. Callers get a fresh
// map they may mutate (e.g. to apply config overrides).
func DefaultUsageWindows() map[string]WindowSpec {
	return map[string]WindowSpec{
		"session":  {Kind: "session", BucketMs: 5 * 60 * 1000, Bucket: "5m"},
		"day":      {Kind: "fixed", WindowMs: msPerDay, BucketMs: msPerHour, Bucket: "1h"},
		"week":     {Kind: "fixed", WindowMs: 7 * msPerDay, BucketMs: 6 * msPerHour, Bucket: "6h"},
		"month":    {Kind: "fixed", WindowMs: 30 * msPerDay, BucketMs: msPerDay, Bucket: "1d"},
		"year":     {Kind: "fixed", WindowMs: 365 * msPerDay, BucketMs: 30 * msPerDay, Bucket: "30d"},
		"lifetime": {Kind: "lifetime", BucketMs: 30 * msPerDay, Bucket: "30d"},
	}
}

// usageWindow resolves a range keyword into a query window and bucket width,
// consulting the server's (possibly config-overridden) window map.
func (s *Server) usageWindow(rangeKey string, sessionStart, now int64) (since, until, bucketMs int64, bucket string) {
	specs := s.usageWindows
	if specs == nil {
		specs = DefaultUsageWindows()
	}
	spec, ok := specs[rangeKey]
	if !ok {
		spec = specs["lifetime"]
	}
	until = now + 1
	switch spec.Kind {
	case "session":
		since = sessionStart
	case "lifetime":
		since = 0
	default: // "fixed"
		since = now - spec.WindowMs
	}
	return since, until, spec.BucketMs, spec.Bucket
}

func (s *Server) handleUsage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.usage == nil {
		writeJSONError(w, "token-usage tracking not available", http.StatusServiceUnavailable)
		return
	}

	rangeKey := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("range")))
	if rangeKey == "" {
		rangeKey = "day"
	}
	switch rangeKey {
	case "session", "day", "week", "month", "year", "lifetime":
	default:
		writeJSONError(w, "range must be one of session|day|week|month|year|lifetime", http.StatusBadRequest)
		return
	}

	now := time.Now().UnixMilli()
	since, until, bucketMs, bucket := s.usageWindow(rangeKey, s.usage.SessionStart(), now)

	series, err := s.usage.Series(bucketMs, since, until)
	if err != nil {
		writeJSONError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	totals, err := s.usage.TotalsFor(since, until)
	if err != nil {
		writeJSONError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	byAgent, err := s.usage.SumBy("agent", since, until)
	if err != nil {
		writeJSONError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	byProvider, err := s.usage.SumBy("provider", since, until)
	if err != nil {
		writeJSONError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	byModel, err := s.usage.SumBy("model", since, until)
	if err != nil {
		writeJSONError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	bySource, err := s.usage.SumBy("source", since, until)
	if err != nil {
		writeJSONError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, map[string]any{
		"range":       rangeKey,
		"bucket":      bucket,
		"bucket_ms":   bucketMs,
		"since":       since,
		"until":       until,
		"series":      emptyPoints(series),
		"totals":      totals,
		"by_agent":    emptyDims(byAgent),
		"by_provider": emptyDims(byProvider),
		"by_model":    emptyDims(byModel),
		"by_source":   emptyDims(bySource),
		"timestamp":   time.Now().UTC().Format(time.RFC3339),
	})
}

// emptyPoints / emptyDims ensure JSON serializes [] instead of null for empty
// results, so the frontend can iterate without null checks.
func emptyPoints(p []usage.Point) []usage.Point {
	if p == nil {
		return []usage.Point{}
	}
	return p
}

func emptyDims(d []usage.DimCount) []usage.DimCount {
	if d == nil {
		return []usage.DimCount{}
	}
	return d
}
