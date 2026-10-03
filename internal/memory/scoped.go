package memory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/emaharmony/prizm/internal/event"
)

// Scope is the authorization boundary for autonomous memory operations.
// Project and task are always exact. User memory is included only when the
// caller explicitly opts in and supplies a user identity.
type Scope struct {
	UserID           string
	ProjectID        string
	TaskID           string
	SessionID        string
	AgentID          string
	IncludeUserScope bool
	CorrelationID    string
}

func (s Scope) Validate() error {
	if strings.TrimSpace(s.ProjectID) == "" || strings.TrimSpace(s.TaskID) == "" {
		return errors.New("memory scope requires exact project and task IDs")
	}
	if strings.TrimSpace(s.CorrelationID) == "" {
		return errors.New("memory scope requires a canonical correlation ID")
	}
	if s.IncludeUserScope && strings.TrimSpace(s.UserID) == "" {
		return errors.New("user memory scope requires a user ID")
	}
	if !s.IncludeUserScope && strings.TrimSpace(s.UserID) != "" {
		return errors.New("user ID requires explicit user-scope opt-in")
	}
	return nil
}

// CaptureRequest describes a scoped write. CaptureKey optionally makes a
// producer's delivery key explicit; otherwise a stable key is derived.
type CaptureRequest struct {
	Scope        Scope
	Content      string
	Summary      string
	Category     string
	Tier         string
	Topics       []string
	Source       string
	CaptureKey   string
	SupersedesID string
}

// SearchRequest describes a scoped retrieval.
type SearchRequest struct {
	Scope Scope
	Query string
	Limit int
}

// PrimaryBackend is a domain-neutral memory integration. Its implementation
// may be Recall/Remembrance, but that protocol stays at the integration edge.
type PrimaryBackend interface {
	Capture(context.Context, Memory) (string, error)
	Search(context.Context, SearchRequest) ([]Memory, error)
}

// Facade composes the primary memory backend with local durable fallback.
// The local store remains the recovery source while the primary backend is
// unavailable or returns incomplete results.
type Facade struct {
	Local   MemoryStore
	Primary PrimaryBackend
	Events  event.EventStore
	Source  string
}

const (
	EventScopedCaptureStarted   = "prizm.memory.capture.started"
	EventScopedCapturePersisted = "prizm.memory.capture.persisted"
	EventScopedCaptureSynced    = "prizm.memory.capture.synced"
	EventScopedCaptureFallback  = "prizm.memory.capture.fallback"
	EventScopedSuperseded       = "prizm.memory.superseded"
	EventScopedSearchRequested  = "prizm.memory.search.requested"
	EventScopedSearchCompleted  = "prizm.memory.search.completed"
	EventScopedSearchFallback   = "prizm.memory.search.fallback"
)

func (f *Facade) Capture(ctx context.Context, req CaptureRequest) (Memory, bool, error) {
	if f == nil || f.Local == nil {
		return Memory{}, false, errors.New("local memory store is not configured")
	}
	if err := req.Scope.Validate(); err != nil {
		return Memory{}, false, err
	}
	if strings.TrimSpace(req.Content) == "" {
		return Memory{}, false, errors.New("memory content is required")
	}
	if req.SupersedesID != "" {
		prior, err := f.Local.Get(ctx, req.SupersedesID)
		if err != nil || prior == nil || !scopeMatches(*prior, req.Scope) {
			return Memory{}, false, errors.New("superseded memory is unavailable in this scope")
		}
	}
	key := req.CaptureKey
	if key == "" {
		key = stableCaptureKey(req)
	}
	mem := Memory{ID: scopedCaptureID(req.Scope, key), Content: req.Content, Summary: req.Summary,
		Category: req.Category, Tier: req.Tier, KeyTopics: append([]string(nil), req.Topics...),
		Source: req.Source, UserID: req.Scope.UserID, ProjectID: req.Scope.ProjectID,
		TaskID: req.Scope.TaskID, SessionID: req.Scope.SessionID, AgentID: req.Scope.AgentID,
		SupersedesID: req.SupersedesID, CreatedAt: time.Now().UTC(), AccessedAt: time.Now().UTC()}
	if mem.Category == "" {
		mem.Category = "fact"
	}
	if mem.Tier == "" {
		mem.Tier = "active"
	}
	if mem.Summary == "" {
		mem.Summary = truncateScopeText(mem.Content, 200)
	}

	f.emit(req.Scope, EventScopedCaptureStarted, map[string]any{"memory_id": mem.ID, "capture_key": key})
	if existing, err := f.Local.Get(ctx, mem.ID); err != nil {
		return Memory{}, false, err
	} else if existing != nil {
		f.emit(req.Scope, EventScopedCapturePersisted, map[string]any{"memory_id": existing.ID, "capture_key": key, "duplicate": true})
		return *existing, false, nil
	}
	if _, err := f.Local.Store(ctx, mem); err != nil {
		return Memory{}, false, err
	}
	f.emit(req.Scope, EventScopedCapturePersisted, map[string]any{"memory_id": mem.ID, "capture_key": key})
	if req.SupersedesID != "" {
		f.emit(req.Scope, EventScopedSuperseded, map[string]any{"memory_id": mem.ID, "supersedes_id": req.SupersedesID})
	}
	if f.Primary == nil {
		return mem, false, nil
	}
	remoteID, err := f.Primary.Capture(ctx, mem)
	if err != nil {
		f.emit(req.Scope, EventScopedCaptureFallback, map[string]any{"memory_id": mem.ID, "reason": "primary_unavailable"})
		return mem, true, nil
	}
	f.emit(req.Scope, EventScopedCaptureSynced, map[string]any{"memory_id": mem.ID, "primary_id": remoteID})
	return mem, false, nil
}

func (f *Facade) Search(ctx context.Context, req SearchRequest) ([]Memory, bool, error) {
	if f == nil || f.Local == nil {
		return nil, false, errors.New("local memory store is not configured")
	}
	if err := req.Scope.Validate(); err != nil {
		return nil, false, err
	}
	if strings.TrimSpace(req.Query) == "" {
		return nil, false, errors.New("memory query is required")
	}
	if req.Limit <= 0 {
		req.Limit = 5
	}
	f.emit(req.Scope, EventScopedSearchRequested, map[string]any{"query": req.Query, "limit": req.Limit})
	var combined []Memory
	fallback := false
	if f.Primary != nil {
		remote, err := f.Primary.Search(ctx, req)
		if err != nil {
			fallback = true
			f.emit(req.Scope, EventScopedSearchFallback, map[string]any{"reason": "primary_unavailable"})
		} else {
			combined = append(combined, remote...)
		}
	}
	local, err := f.Local.Search(ctx, req.Query, req.Limit*3)
	if err != nil {
		return nil, fallback, err
	}
	combined = append(combined, local...)
	results := filterAndDedupe(combined, req.Scope, req.Limit)
	f.emit(req.Scope, EventScopedSearchCompleted, map[string]any{"query": req.Query, "count": len(results), "fallback": fallback})
	return results, fallback, nil
}

func stableCaptureKey(req CaptureRequest) string {
	h := sha256.Sum256([]byte(strings.Join([]string{req.Scope.UserID, req.Scope.ProjectID, req.Scope.TaskID, req.Scope.SessionID, req.Scope.AgentID, req.Content, req.Summary, req.Category, req.SupersedesID}, "\x00")))
	return hex.EncodeToString(h[:16])
}

// scopedCaptureID isolates producer-supplied delivery keys. A producer may
// reuse its key in another task, project, or user scope without retrieving or
// overwriting a memory from that other authorization boundary.
func scopedCaptureID(scope Scope, key string) string {
	canonicalUser := ""
	if scope.IncludeUserScope {
		canonicalUser = strings.TrimSpace(scope.UserID)
	}
	h := sha256.Sum256([]byte(strings.Join([]string{canonicalUser, strings.TrimSpace(scope.ProjectID), strings.TrimSpace(scope.TaskID), key}, "\x00")))
	return "mem_" + hex.EncodeToString(h[:16])
}

func scopeMatches(mem Memory, scope Scope) bool {
	if mem.ProjectID != scope.ProjectID || mem.TaskID != scope.TaskID {
		return false
	}
	return mem.UserID == "" || (scope.IncludeUserScope && mem.UserID == scope.UserID)
}

func filterAndDedupe(memories []Memory, scope Scope, limit int) []Memory {
	superseded := make(map[string]struct{})
	seen := make(map[string]struct{})
	out := make([]Memory, 0, len(memories))
	for _, m := range memories {
		if m.SupersedesID != "" {
			superseded[m.SupersedesID] = struct{}{}
		}
	}
	for _, m := range memories {
		if !scopeMatches(m, scope) {
			continue
		}
		if _, old := superseded[m.ID]; old {
			continue
		}
		key := m.ID
		if key == "" {
			key = m.ProjectID + "\x00" + m.TaskID + "\x00" + m.Summary + "\x00" + m.Content
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, m)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

func (f *Facade) emit(scope Scope, typ string, payload map[string]any) {
	if f.Events == nil {
		return
	}
	payload["project_id"] = scope.ProjectID
	payload["task_id"] = scope.TaskID
	if scope.IncludeUserScope {
		payload["user_id"] = scope.UserID
	}
	e := event.NewEvent(typ, f.Source, payload).WithCorrelationID(scope.CorrelationID).WithMetadata(event.EventMetadata{Project: scope.ProjectID, SessionID: scope.SessionID, Agent: scope.AgentID})
	_ = f.Events.Store(context.Background(), e)
}

func truncateScopeText(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return fmt.Sprintf("%s...", s[:max])
}
