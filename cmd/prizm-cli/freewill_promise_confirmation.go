package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/emaharmony/prizm/internal/api"
	"github.com/emaharmony/prizm/internal/memory"
	"github.com/emaharmony/prizm/internal/orchestrator"
)

const freewillPromiseSource = "prizm:freewill-promise-confirmation"

type freewillPromiseConfirmationSink struct {
	facts                            *freewillTerminalFactMemorySink
	owner, project, workspace, agent string
	seed                             api.FreewillPromiseRevision
}

type freewillPromiseEvent struct {
	Version          int       `json:"version"`
	Kind             string    `json:"kind"`
	PromiseID        string    `json:"promise_id"`
	Revision         int       `json:"revision,omitempty"`
	PreviousRevision int       `json:"previous_revision,omitempty"`
	ProposalID       string    `json:"proposal_id,omitempty"`
	RequestID        string    `json:"request_id,omitempty"`
	Proposer         string    `json:"proposer,omitempty"`
	OwnerID          string    `json:"owner_id"`
	ProjectID        string    `json:"project_id"`
	WorkspaceID      string    `json:"workspace_id"`
	AgentID          string    `json:"agent_id"`
	Scope            string    `json:"scope,omitempty"`
	Deadline         string    `json:"deadline,omitempty"`
	ExpectedRevision int       `json:"expected_revision,omitempty"`
	ImpactHypothesis string    `json:"impact_hypothesis,omitempty"`
	Decision         string    `json:"decision,omitempty"`
	RecordedAt       time.Time `json:"recorded_at"`
}

func newFreewillPromiseConfirmationSink(f *freewillTerminalFactMemorySink, project, workspace, agent string, cfg orchestrator.FreewillPromiseConfirmationConfig) (*freewillPromiseConfirmationSink, error) {
	if f == nil || f.store == nil || f.claimer == nil || cfg.OwnerID == "" || cfg.PromiseID == "" || project != f.projectID || agent == "" {
		return nil, errors.New("freewill: promise confirmation requires exact local binding")
	}
	canonical, err := canonicalFreewillWorkspace(workspace)
	if err != nil || canonical != f.workspace {
		return nil, errors.New("freewill: promise workspace binding mismatch")
	}
	deadline, err := time.Parse(time.RFC3339, cfg.Deadline)
	if err != nil {
		return nil, errors.New("freewill: promise deadline is invalid")
	}
	s := &freewillPromiseConfirmationSink{facts: f, owner: cfg.OwnerID, project: project, workspace: canonical, agent: agent}
	s.seed = api.FreewillPromiseRevision{PromiseID: cfg.PromiseID, Revision: 1, OwnerID: cfg.OwnerID, ProjectID: project, WorkspaceID: f.workspaceID, AgentID: agent, Scope: cfg.Scope, Deadline: deadline.UTC().Format(time.RFC3339)}
	ctx := context.Background()
	claim, err := f.acquireCaptureClaim(ctx, freewillBindingDigest("promise-chain", s.seed.PromiseID))
	if err != nil {
		return nil, err
	}
	defer claim.Release()
	events, err := s.load(ctx)
	if err != nil {
		return nil, err
	}
	if len(events) == 0 {
		seed := freewillPromiseEvent{Version: 1, Kind: "seed", PromiseID: s.seed.PromiseID, Revision: 1, OwnerID: s.owner, ProjectID: s.project, WorkspaceID: f.workspaceID, AgentID: s.agent, Scope: s.seed.Scope, Deadline: s.seed.Deadline, RecordedAt: time.Now().UTC()}
		if err = s.append(ctx, seed, "promise-seed:"+s.seed.PromiseID); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func (s *freewillPromiseConfirmationSink) scope() memory.Scope {
	return memory.Scope{UserID: s.owner, IncludeUserScope: true, ProjectID: s.project, TaskID: "freewill-promise:" + s.seed.PromiseID, AgentID: s.agent, CorrelationID: "freewill-promise:" + s.seed.PromiseID}
}
func (s *freewillPromiseConfirmationSink) load(ctx context.Context) ([]freewillPromiseEvent, error) {
	all, err := s.facts.store.Local.ListRecent(ctx, 0)
	if err != nil {
		return nil, err
	}
	out := []freewillPromiseEvent{}
	for _, m := range all {
		if m.Source != freewillPromiseSource || m.Category != "promise_confirmation" || m.ProjectID != s.project || m.UserID != s.owner || m.TaskID != "freewill-promise:"+s.seed.PromiseID {
			continue
		}
		var e freewillPromiseEvent
		if json.Unmarshal([]byte(m.Content), &e) != nil || e.Version != 1 || e.PromiseID != s.seed.PromiseID || e.OwnerID != s.owner || e.ProjectID != s.project || e.WorkspaceID != s.facts.workspaceID || e.AgentID != s.agent || e.RecordedAt.IsZero() {
			return nil, errors.New("freewill: promise history is invalid")
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RecordedAt.Before(out[j].RecordedAt) })
	return out, nil
}
func (s *freewillPromiseConfirmationSink) append(ctx context.Context, e freewillPromiseEvent, key string) error {
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	_, _, err = s.facts.store.Capture(ctx, memory.CaptureRequest{Scope: s.scope(), Content: string(b), Summary: "Synthetic Freewill promise history record", Category: "promise_confirmation", Tier: "persist", Source: freewillPromiseSource, CaptureKey: key})
	return err
}
func (s *freewillPromiseConfirmationSink) ReadPromise(ctx context.Context, owner, project, workspace, agent, promise string) (api.FreewillPromiseConfirmation, error) {
	if err := s.check(owner, project, workspace, agent, promise); err != nil {
		return api.FreewillPromiseConfirmation{}, err
	}
	events, err := s.load(ctx)
	if err != nil {
		return api.FreewillPromiseConfirmation{}, err
	}
	return s.view(events), nil
}
func (s *freewillPromiseConfirmationSink) ProposePromiseRevision(ctx context.Context, owner, project, workspace, agent, promise, requestID string, expected int, scope, deadline, impact string) (api.FreewillPromiseConfirmation, error) {
	if err := s.check(owner, project, workspace, agent, promise); err != nil {
		return api.FreewillPromiseConfirmation{}, err
	}
	scope = strings.TrimSpace(scope)
	impact = strings.TrimSpace(impact)
	d, err := time.Parse(time.RFC3339, deadline)
	requestID = strings.TrimSpace(requestID)
	if err != nil || requestID == "" || len(requestID) > 100 || scope == "" || len(scope) > 1000 || impact == "" || len(impact) > 500 {
		return api.FreewillPromiseConfirmation{}, errors.New("freewill: bounded proposal fields are invalid")
	}
	claim, err := s.facts.acquireCaptureClaim(ctx, freewillBindingDigest("promise-chain", promise))
	if err != nil {
		return api.FreewillPromiseConfirmation{}, err
	}
	defer claim.Release()
	events, err := s.load(ctx)
	if err != nil {
		return api.FreewillPromiseConfirmation{}, err
	}
	v := s.view(events)
	for _, p := range v.Proposals {
		if p.RequestID == requestID {
			if p.ExpectedRevision != expected || p.ProposedScope != scope || p.ProposedDeadline != d.UTC().Format(time.RFC3339) || p.ImpactHypothesis != impact {
				return api.FreewillPromiseConfirmation{}, errors.New("freewill: conflicting proposal for promise revision")
			}
			return v, nil
		}
	}
	if expected != v.Current.Revision {
		return api.FreewillPromiseConfirmation{}, errors.New("freewill: stale promise revision")
	}
	if scope == v.Current.Scope && d.UTC().Format(time.RFC3339) == v.Current.Deadline {
		return api.FreewillPromiseConfirmation{}, errors.New("freewill: proposal does not change the promise")
	}
	e := freewillPromiseEvent{Version: 1, Kind: "proposal", PromiseID: promise, RequestID: requestID, Proposer: "local_synthetic_fake", ExpectedRevision: expected, ProposalID: promiseID(promise, requestID, expected, scope, d.UTC().Format(time.RFC3339), impact), OwnerID: owner, ProjectID: project, WorkspaceID: s.facts.workspaceID, AgentID: agent, Scope: scope, Deadline: d.UTC().Format(time.RFC3339), ImpactHypothesis: impact, RecordedAt: time.Now().UTC()}
	if err = s.append(ctx, e, "promise-proposal:"+e.ProposalID); err != nil {
		return api.FreewillPromiseConfirmation{}, err
	}
	events, err = s.load(ctx)
	return s.view(events), err
}
func (s *freewillPromiseConfirmationSink) DecidePromiseRevision(ctx context.Context, owner, project, workspace, agent, promise, proposal, decision string) (api.FreewillPromiseConfirmation, error) {
	if err := s.check(owner, project, workspace, agent, promise); err != nil {
		return api.FreewillPromiseConfirmation{}, err
	}
	if decision != "accept" && decision != "reject" {
		return api.FreewillPromiseConfirmation{}, errors.New("freewill: decision must be accept or reject")
	}
	claim, err := s.facts.acquireCaptureClaim(ctx, freewillBindingDigest("promise-chain", promise))
	if err != nil {
		return api.FreewillPromiseConfirmation{}, err
	}
	defer claim.Release()
	events, err := s.load(ctx)
	if err != nil {
		return api.FreewillPromiseConfirmation{}, err
	}
	v := s.view(events)
	var p *api.FreewillPromiseProposal
	for i := range v.Proposals {
		if v.Proposals[i].ProposalID == proposal {
			p = &v.Proposals[i]
			break
		}
	}
	if p == nil {
		return api.FreewillPromiseConfirmation{}, errors.New("freewill: proposal is unavailable")
	}
	for _, d := range v.Decisions {
		if d.ProposalID == proposal {
			if d.Decision != decision {
				return api.FreewillPromiseConfirmation{}, errors.New("freewill: conflicting replay")
			}
			return v, nil
		}
	}
	if p.ExpectedRevision != v.Current.Revision {
		return api.FreewillPromiseConfirmation{}, errors.New("freewill: promise revision changed while proposal pending")
	}
	e := freewillPromiseEvent{Version: 1, Kind: "decision", PromiseID: promise, ProposalID: proposal, Decision: decision, OwnerID: owner, ProjectID: project, WorkspaceID: s.facts.workspaceID, AgentID: agent, RecordedAt: time.Now().UTC()}
	if decision == "accept" {
		e.Revision, e.PreviousRevision, e.Scope, e.Deadline = v.Current.Revision+1, v.Current.Revision, p.ProposedScope, p.ProposedDeadline
	}
	if err = s.append(ctx, e, "promise-decision:"+proposal); err != nil {
		return api.FreewillPromiseConfirmation{}, err
	}
	events, err = s.load(ctx)
	return s.view(events), err
}
func (s *freewillPromiseConfirmationSink) check(owner, project, workspace, agent, promise string) error {
	canonical, err := canonicalFreewillWorkspace(workspace)
	if err != nil || owner != s.owner || project != s.project || agent != s.agent || canonical != s.workspace || promise != s.seed.PromiseID {
		return errors.New("freewill: promise scope mismatch")
	}
	return nil
}
func (s *freewillPromiseConfirmationSink) view(events []freewillPromiseEvent) api.FreewillPromiseConfirmation {
	v := api.FreewillPromiseConfirmation{Current: s.seed, Proposals: []api.FreewillPromiseProposal{}, Decisions: []api.FreewillPromiseDecision{}, History: []api.FreewillPromiseRevision{s.seed}}
	for _, e := range events {
		switch e.Kind {
		case "proposal":
			v.Proposals = append(v.Proposals, api.FreewillPromiseProposal{ProposalID: e.ProposalID, PromiseID: e.PromiseID, RequestID: e.RequestID, Proposer: e.Proposer, ExpectedRevision: e.ExpectedRevision, ProposedScope: e.Scope, ProposedDeadline: e.Deadline, ImpactHypothesis: e.ImpactHypothesis, Status: "pending", RecordedAt: e.RecordedAt.Format(time.RFC3339Nano)})
		case "decision":
			v.Decisions = append(v.Decisions, api.FreewillPromiseDecision{DecisionID: promiseID(e.ProposalID, e.Decision), ProposalID: e.ProposalID, Decision: e.Decision, RecordedAt: e.RecordedAt.Format(time.RFC3339Nano)})
			if e.Decision == "accept" {
				r := api.FreewillPromiseRevision{PromiseID: e.PromiseID, Revision: e.Revision, OwnerID: e.OwnerID, ProjectID: e.ProjectID, WorkspaceID: e.WorkspaceID, AgentID: e.AgentID, Scope: e.Scope, Deadline: e.Deadline, RecordedAt: e.RecordedAt.Format(time.RFC3339Nano)}
				v.History = append(v.History, r)
				v.Current = r
			}
		case "revision":
			r := api.FreewillPromiseRevision{PromiseID: e.PromiseID, Revision: e.Revision, OwnerID: e.OwnerID, ProjectID: e.ProjectID, WorkspaceID: e.WorkspaceID, AgentID: e.AgentID, Scope: e.Scope, Deadline: e.Deadline, RecordedAt: e.RecordedAt.Format(time.RFC3339Nano)}
			v.History = append(v.History, r)
			v.Current = r
		}
	}
	for i := range v.Proposals {
		if v.Proposals[i].ExpectedRevision != v.Current.Revision {
			v.Proposals[i].Status = "stale"
		}
		for _, d := range v.Decisions {
			if d.ProposalID == v.Proposals[i].ProposalID {
				v.Proposals[i].Status = d.Decision
			}
		}
	}
	return v
}
func promiseID(parts ...any) string {
	h := sha256.New()
	for _, p := range parts {
		fmt.Fprintf(h, "%v\x00", p)
	}
	return hex.EncodeToString(h.Sum(nil))
}
