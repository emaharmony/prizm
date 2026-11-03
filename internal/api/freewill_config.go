package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/emaharmony/prizm/internal/orchestrator"
)

// Freewill settings editing with a mandatory approval gate.
//
// Flow: PUT with confirm=false validates the patch, computes the resulting
// YAML, and stores it server-side as a pending draft (15-minute window).
// Nothing on disk changes. A later PUT referencing the same draft_id with
// confirm=true is the only path that writes prizm.yaml, re-validating the
// stored bytes and recording the confirming actor. Any mismatch between the
// requested change and the stored draft invalidates the draft.

const (
	freewillDraftWindow = 15 * time.Minute
	freewillDraftMax    = 8
)

// freewillConfigPatch is a partial update of the freewill: block. Only
// non-nil fields are written; every other key in prizm.yaml is untouched.
type freewillConfigPatch struct {
	Enabled         *bool   `json:"enabled"`
	AgentID         *string `json:"agent_id"`
	ProjectID       *string `json:"project_id"`
	Model           *string `json:"model"`
	Objective       *string `json:"objective"`
	ReasoningEffort *string `json:"reasoning_effort"`
	PolicyDir       *string `json:"policy_dir"`
	// Scheduling rewrites freewill.scheduling wholesale when provided;
	// enabled=false disables timed wakes, and when enabled=true a schedule
	// is mandatory per config validation.
	Scheduling *freewillSchedulingPatch `json:"scheduling"`
	Confirm    bool                     `json:"confirm"`
	DraftID    string                   `json:"draft_id"`
	Actor      string                   `json:"actor"`
}

type freewillSchedulingPatch struct {
	Enabled  *bool   `json:"enabled"`
	Schedule *string `json:"schedule"`
}

// freewillDraftEntry persists validated draft bytes between the preview and
// the confirming request, so the confirm step cannot be replayed with
// different content than what was previewed.
type freewillDraftEntry struct {
	created time.Time
	bytes   []byte
	summary map[string]any
}

type freewillDraftStore struct {
	mu   sync.Mutex
	byID map[string]*freewillDraftEntry
}

func (d *freewillDraftStore) put(entry *freewillDraftEntry) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.byID == nil {
		d.byID = map[string]*freewillDraftEntry{}
	}
	// Expire old drafts opportunistically.
	now := time.Now()
	for id, existing := range d.byID {
		if now.Sub(existing.created) > freewillDraftWindow {
			delete(d.byID, id)
		}
	}
	// Bound total drafts: oldest evicted first.
	for len(d.byID) >= freewillDraftMax {
		oldestID, oldestAt := "", now
		for id, existing := range d.byID {
			if oldestID == "" || existing.created.Before(oldestAt) {
				oldestID, oldestAt = id, existing.created
			}
		}
		delete(d.byID, oldestID)
	}
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return ""
	}
	id := "fwcfg-" + hex.EncodeToString(raw[:8])
	entry.created = now
	d.byID[id] = entry
	return id
}

func (d *freewillDraftStore) take(id string) (*freewillDraftEntry, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	entry, ok := d.byID[id]
	if ok {
		delete(d.byID, id)
	}
	return entry, ok
}

func (s *Server) freewillCurrentConfig() (orchestrator.FreewillConfig, error) {
	if s.configPath == "" {
		return orchestrator.FreewillConfig{}, fmt.Errorf("config editing not available (no config path)")
	}
	cfg, err := orchestrator.LoadConfig(s.configPath)
	if err != nil {
		return orchestrator.FreewillConfig{}, err
	}
	return cfg.Freewill, nil
}

// GET /api/v1/config/freewill — current freewill settings.
func (s *Server) handleConfigFreewillGet(w http.ResponseWriter, r *http.Request) {
	current, err := s.freewillCurrentConfig()
	if err != nil {
		writeJSONError(w, "load config: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{
		"enabled":          current.Enabled,
		"agent_id":         current.AgentID,
		"project_id":       current.ProjectID,
		"model":            current.Model,
		"objective":        current.Objective,
		"reasoning_effort": current.ReasoningEffort,
		"policy_dir":       current.PolicyDir,
		"scheduling": map[string]any{
			"enabled":  current.Scheduling.Enabled,
			"schedule": current.Scheduling.Schedule,
		},
	})
}

// PUT /api/v1/config/freewill — draft or confirm. Draft mode: no disk write.
// Confirm mode: matches the exact stored draft, writes atomically, and
// reports the agent that confirmed.
func (s *Server) handleConfigFreewillPut(w http.ResponseWriter, r *http.Request, patch freewillConfigPatch) {
	if !patch.Confirm {
		s.handleFreewillDraft(w, r, patch)
		return
	}
	if strings.TrimSpace(patch.DraftID) == "" {
		writeJSONError(w, "confirmation requires the draft_id returned by the preview step", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(patch.Actor) == "" {
		writeJSONError(w, "confirmation requires an actor (recorded on the write)", http.StatusBadRequest)
		return
	}
	if s.freewillDrafts == nil {
		writeJSONError(w, "no pending drafts", http.StatusConflict)
		return
	}
	entry, ok := s.freewillDrafts.take(patch.DraftID)
	if !ok {
		writeJSONError(w, "no pending draft with that id — preview the change again", http.StatusConflict)
		return
	}
	if time.Since(entry.created) > freewillDraftWindow {
		writeJSONError(w, "pending draft expired — preview the change again", http.StatusConflict)
		return
	}
	if err := orchestrator.ValidateAndWrite(s.configPath, entry.bytes); err != nil {
		writeJSONError(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{"status": "saved", "path": s.configPath, "confirmed_by": patch.Actor, "restart_required": true, "summary": entry.summary})
}

func (s *Server) handleFreewillDraft(w http.ResponseWriter, r *http.Request, patch freewillConfigPatch) {
	if patch.DraftID != "" {
		writeJSONError(w, "draft_id is only valid with confirm=true", http.StatusBadRequest)
		return
	}
	if s.configPath == "" {
		writeJSONError(w, "config editing not available (no config path)", http.StatusBadRequest)
		return
	}
	src, err := os.ReadFile(s.configPath)
	if err != nil {
		writeJSONError(w, "read config: "+err.Error(), http.StatusInternalServerError)
		return
	}
	summary, err := applyFreewillPatch(&src, patch)
	if err != nil {
		writeJSONError(w, err.Error(), http.StatusBadRequest)
		return
	}
	// Validation without writing: the confirm step re-validates the same bytes.
	cfg, err := orchestrator.LoadConfigFromBytes(src)
	if err != nil {
		writeJSONError(w, fmt.Sprintf("proposed change is invalid: %v", err), http.StatusBadRequest)
		return
	}
	if cfg.Freewill.Enabled {
		if err := cfg.Freewill.ValidateHostBinding(cfg.Agents, cfg.Projects); err != nil {
			writeJSONError(w, fmt.Sprintf("freewill binding invalid: %v", err), http.StatusBadRequest)
			return
		}
	}
	draft := &freewillDraftEntry{bytes: src, summary: summary}
	if s.freewillDrafts == nil {
		s.freewillDrafts = &freewillDraftStore{}
	}
	id := s.freewillDrafts.put(draft)
	if id == "" {
		writeJSONError(w, "could not allocate draft id", http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{
		"status":     "draft",
		"draft_id":   id,
		"summary":    summary,
		"expires_in": int((freewillDraftWindow).Seconds()),
		"next":       "review the summary, then resubmit the same change with confirm=true, draft_id, and actor",
	})
}

func applyFreewillPatch(src *[]byte, patch freewillConfigPatch) (map[string]any, error) {
	current, err := s_freewillCurrent(*src)
	if err != nil {
		return nil, err
	}
	if patch.Enabled == nil && patch.AgentID == nil && patch.ProjectID == nil && patch.Model == nil &&
		patch.Objective == nil && patch.ReasoningEffort == nil && patch.PolicyDir == nil && patch.Scheduling == nil {
		return nil, fmt.Errorf("no freewill changes requested")
	}
	var summary map[string]any = map[string]any{}
	if patch.Enabled != nil && *patch.Enabled != current.Enabled {
		summary["enabled"] = map[string]any{"from": current.Enabled, "to": *patch.Enabled}
	}
	if patch.AgentID != nil && strings.TrimSpace(*patch.AgentID) != current.AgentID {
		summary["agent_id"] = map[string]any{"from": current.AgentID, "to": strings.TrimSpace(*patch.AgentID)}
	}
	if patch.ProjectID != nil && strings.TrimSpace(*patch.ProjectID) != current.ProjectID {
		summary["project_id"] = map[string]any{"from": current.ProjectID, "to": strings.TrimSpace(*patch.ProjectID)}
	}
	if patch.Model != nil && strings.TrimSpace(*patch.Model) != current.Model {
		summary["model"] = map[string]any{"from": current.Model, "to": strings.TrimSpace(*patch.Model)}
	}
	if patch.Objective != nil && *patch.Objective != current.Objective {
		summary["objective"] = map[string]any{"from": current.Objective, "to": *patch.Objective}
	}
	if patch.ReasoningEffort != nil && strings.TrimSpace(*patch.ReasoningEffort) != current.ReasoningEffort {
		summary["reasoning_effort"] = map[string]any{"from": current.ReasoningEffort, "to": strings.TrimSpace(*patch.ReasoningEffort)}
	}
	if patch.PolicyDir != nil && strings.TrimSpace(*patch.PolicyDir) != current.PolicyDir {
		summary["policy_dir"] = map[string]any{"from": current.PolicyDir, "to": strings.TrimSpace(*patch.PolicyDir)}
	}
	if patch.Scheduling != nil {
		sched := map[string]any{}
		if patch.Scheduling.Enabled != nil && *patch.Scheduling.Enabled != current.Scheduling.Enabled {
			sched["enabled"] = map[string]any{"from": current.Scheduling.Enabled, "to": *patch.Scheduling.Enabled}
		}
		if patch.Scheduling.Schedule != nil {
			next := strings.TrimSpace(*patch.Scheduling.Schedule)
			if next != current.Scheduling.Schedule {
				sched["schedule"] = map[string]any{"from": current.Scheduling.Schedule, "to": next}
			}
		}
		if len(sched) > 0 {
			summary["scheduling"] = sched
		}
	}
	if len(summary) == 0 {
		return nil, fmt.Errorf("no effective changes requested (values already set)")
	}

	// Surgical edits, mirroring the settings editor's SetYAMLPath pattern.
	set := func(path []string, val any) error {
		next, err := orchestrator.SetYAMLPath(*src, path, val)
		if err != nil {
			return fmt.Errorf("edit %s: %v", strings.Join(path, "."), err)
		}
		*src = next
		return nil
	}
	if patch.Enabled != nil {
		if err := set([]string{"freewill", "enabled"}, *patch.Enabled); err != nil {
			return nil, err
		}
	}
	if patch.AgentID != nil {
		if err := set([]string{"freewill", "agent_id"}, strings.TrimSpace(*patch.AgentID)); err != nil {
			return nil, err
		}
	}
	if patch.ProjectID != nil {
		if err := set([]string{"freewill", "project_id"}, strings.TrimSpace(*patch.ProjectID)); err != nil {
			return nil, err
		}
	}
	if patch.Model != nil {
		if err := set([]string{"freewill", "model"}, strings.TrimSpace(*patch.Model)); err != nil {
			return nil, err
		}
	}
	if patch.Objective != nil {
		if err := set([]string{"freewill", "objective"}, *patch.Objective); err != nil {
			return nil, err
		}
	}
	if patch.ReasoningEffort != nil {
		if err := set([]string{"freewill", "reasoning_effort"}, strings.TrimSpace(*patch.ReasoningEffort)); err != nil {
			return nil, err
		}
	}
	if patch.PolicyDir != nil {
		if err := set([]string{"freewill", "policy_dir"}, strings.TrimSpace(*patch.PolicyDir)); err != nil {
			return nil, err
		}
	}
	if patch.Scheduling != nil {
		if patch.Scheduling.Enabled != nil {
			if err := set([]string{"freewill", "scheduling", "enabled"}, *patch.Scheduling.Enabled); err != nil {
				return nil, err
			}
		}
		if patch.Scheduling.Schedule != nil {
			if err := set([]string{"freewill", "scheduling", "schedule"}, strings.TrimSpace(*patch.Scheduling.Schedule)); err != nil {
				return nil, err
			}
		}
	}
	return summary, nil
}

func s_freewillCurrent(src []byte) (orchestrator.FreewillConfig, error) {
	cfg, err := orchestrator.LoadConfigFromBytes(src)
	if err != nil {
		return orchestrator.FreewillConfig{}, err
	}
	return cfg.Freewill, nil
}

// handleConfigFreewill routes both methods on /api/v1/config/freewill.
func (s *Server) handleConfigFreewill(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		s.handleConfigFreewillGet(w, r)
		return
	}
	if r.Method != http.MethodPut {
		w.Header().Set("Allow", "GET, PUT")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.configPath == "" {
		writeJSONError(w, "config editing not available (no config path)", http.StatusBadRequest)
		return
	}
	var patch freewillConfigPatch
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, s.maxRequestBytes)).Decode(&patch); err != nil {
		writeJSONError(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	s.handleConfigFreewillPut(w, r, patch)
}
