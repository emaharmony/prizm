// Package usage provides persistent, time-bucketed token-usage tracking for
// Prizm. Every LLM call routed through the provider registry is recorded here
// with its timestamp, agent, provider, model, and source, so the dashboard can
// graph usage over session / day / week / month / year / lifetime and surface
// where tokens are going (leakage detection).
//
// Storage is SQLite in WAL mode (via modernc.org/sqlite, the same CGO-free
// driver used by internal/event). Aggregation is done in SQL — GROUP BY on an
// integer-divided timestamp — so lifetime queries stay cheap regardless of how
// many rows accumulate.
package usage

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite" // SQLite driver
)

// Event is a single recorded LLM call.
type Event struct {
	TS               int64 // unix milliseconds
	Agent            string
	Provider         string
	Model            string
	Source           string // "invoke" | "chat" | "workflow" | "delegation"
	RunID            string
	ConversationID   string
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	CostUSD          float64
}

// Point is one time bucket in a usage series.
type Point struct {
	TS         int64   `json:"ts"` // bucket start, unix milliseconds
	Prompt     int     `json:"prompt"`
	Completion int     `json:"completion"`
	Total      int     `json:"total"`
	Cost       float64 `json:"cost"`
}

// DimCount is a per-dimension (agent/provider/model/source) rollup.
type DimCount struct {
	Key    string  `json:"key"`
	Total  int     `json:"total"`
	Cost   float64 `json:"cost"`
	Prompt int     `json:"prompt"`
}

// Totals is the grand total over a time window.
type Totals struct {
	Prompt     int     `json:"prompt"`
	Completion int     `json:"completion"`
	Total      int     `json:"total"`
	Cost       float64 `json:"cost"`
	Count      int     `json:"count"`
}

// Store persists token-usage events to SQLite.
type Store struct {
	db      *sql.DB
	startMs int64 // process start — defines the "session" window
}

// NewStore opens or creates a usage store at dbPath.
func NewStore(dbPath string) (*Store, error) {
	dir := filepath.Dir(dbPath)
	if dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("usage store: create dir: %w", err)
		}
	}

	db, err := sql.Open("sqlite", dbPath+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, fmt.Errorf("usage store: open: %w", err)
	}
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("usage store: enable WAL: %w", err)
	}

	createTable := `
	CREATE TABLE IF NOT EXISTS token_usage (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		ts INTEGER NOT NULL,
		agent TEXT DEFAULT '',
		provider TEXT DEFAULT '',
		model TEXT DEFAULT '',
		source TEXT DEFAULT '',
		run_id TEXT DEFAULT '',
		conversation_id TEXT DEFAULT '',
		prompt_tokens INTEGER DEFAULT 0,
		completion_tokens INTEGER DEFAULT 0,
		total_tokens INTEGER DEFAULT 0,
		cost_usd REAL DEFAULT 0
	);
	CREATE INDEX IF NOT EXISTS idx_usage_ts ON token_usage(ts);
	CREATE INDEX IF NOT EXISTS idx_usage_agent ON token_usage(agent);
	CREATE INDEX IF NOT EXISTS idx_usage_provider ON token_usage(provider);
	CREATE INDEX IF NOT EXISTS idx_usage_model ON token_usage(model);
	`
	if _, err := db.Exec(createTable); err != nil {
		db.Close()
		return nil, fmt.Errorf("usage store: create table: %w", err)
	}

	return &Store{db: db, startMs: time.Now().UnixMilli()}, nil
}

// SessionStart returns the process start time (unix ms) — the lower bound of the
// current "session" window.
func (s *Store) SessionStart() int64 { return s.startMs }

// Record inserts one usage event. A zero TS is filled with the current time, and
// a zero TotalTokens is derived from prompt+completion.
func (s *Store) Record(e Event) error {
	if e.TS == 0 {
		e.TS = time.Now().UnixMilli()
	}
	if e.TotalTokens == 0 {
		e.TotalTokens = e.PromptTokens + e.CompletionTokens
	}
	_, err := s.db.Exec(
		`INSERT INTO token_usage
		 (ts, agent, provider, model, source, run_id, conversation_id, prompt_tokens, completion_tokens, total_tokens, cost_usd)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.TS, e.Agent, e.Provider, e.Model, e.Source, e.RunID, e.ConversationID,
		e.PromptTokens, e.CompletionTokens, e.TotalTokens, e.CostUSD,
	)
	if err != nil {
		return fmt.Errorf("usage store: insert: %w", err)
	}
	return nil
}

// Series returns usage grouped into fixed-width time buckets of bucketMs
// milliseconds over [since, until). Each bucket's TS is its start time. Buckets
// with no usage are omitted (the caller fills gaps for rendering).
func (s *Store) Series(bucketMs, since, until int64) ([]Point, error) {
	if bucketMs <= 0 {
		return nil, fmt.Errorf("usage store: bucketMs must be > 0")
	}
	rows, err := s.db.Query(
		`SELECT (ts / ?) * ? AS bucket,
		        COALESCE(SUM(prompt_tokens), 0),
		        COALESCE(SUM(completion_tokens), 0),
		        COALESCE(SUM(total_tokens), 0),
		        COALESCE(SUM(cost_usd), 0)
		 FROM token_usage
		 WHERE ts >= ? AND ts < ?
		 GROUP BY bucket
		 ORDER BY bucket ASC`,
		bucketMs, bucketMs, since, until,
	)
	if err != nil {
		return nil, fmt.Errorf("usage store: series query: %w", err)
	}
	defer rows.Close()

	var points []Point
	for rows.Next() {
		var p Point
		if err := rows.Scan(&p.TS, &p.Prompt, &p.Completion, &p.Total, &p.Cost); err != nil {
			return nil, fmt.Errorf("usage store: series scan: %w", err)
		}
		points = append(points, p)
	}
	return points, rows.Err()
}

// SumBy returns per-dimension rollups over [since, until), ordered by total
// tokens descending. dim must be one of agent, provider, model, source.
func (s *Store) SumBy(dim string, since, until int64) ([]DimCount, error) {
	switch dim {
	case "agent", "provider", "model", "source":
	default:
		return nil, fmt.Errorf("usage store: invalid dimension %q", dim)
	}
	// dim is validated against a fixed allowlist above, so interpolating it into
	// the column position is safe (no user-controlled SQL).
	query := fmt.Sprintf(
		`SELECT %s AS k,
		        COALESCE(SUM(total_tokens), 0),
		        COALESCE(SUM(cost_usd), 0),
		        COALESCE(SUM(prompt_tokens), 0)
		 FROM token_usage
		 WHERE ts >= ? AND ts < ?
		 GROUP BY k
		 ORDER BY SUM(total_tokens) DESC`, dim)
	rows, err := s.db.Query(query, since, until)
	if err != nil {
		return nil, fmt.Errorf("usage store: sumby query: %w", err)
	}
	defer rows.Close()

	var out []DimCount
	for rows.Next() {
		var d DimCount
		if err := rows.Scan(&d.Key, &d.Total, &d.Cost, &d.Prompt); err != nil {
			return nil, fmt.Errorf("usage store: sumby scan: %w", err)
		}
		if d.Key == "" {
			d.Key = "(unknown)"
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// TotalsFor returns the grand totals over [since, until).
func (s *Store) TotalsFor(since, until int64) (Totals, error) {
	var t Totals
	err := s.db.QueryRow(
		`SELECT COALESCE(SUM(prompt_tokens), 0),
		        COALESCE(SUM(completion_tokens), 0),
		        COALESCE(SUM(total_tokens), 0),
		        COALESCE(SUM(cost_usd), 0),
		        COUNT(*)
		 FROM token_usage
		 WHERE ts >= ? AND ts < ?`,
		since, until,
	).Scan(&t.Prompt, &t.Completion, &t.Total, &t.Cost, &t.Count)
	if err != nil {
		return t, fmt.Errorf("usage store: totals query: %w", err)
	}
	return t, nil
}

// Close releases database resources.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}
