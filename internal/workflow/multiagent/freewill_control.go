package multiagent

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/emaharmony/prizm/internal/freewill"
)

var ErrFreewillHalted = errors.New("multiagent: Freewill is halted by operator control")
var ErrFreewillControlUnavailable = errors.New("multiagent: durable Freewill operator control is unavailable")
var ErrFreewillApprovalRevoked = errors.New("multiagent: Freewill approval belongs to an earlier control generation")

// FreewillOperatorControl is the workflow-owned operator gate. It can stop or
// resume decision processing, but it does not grant permission to execute.
type FreewillOperatorControl struct {
	store            FreewillOperatorControlStore
	clock            func() time.Time
	mu               sync.Mutex
	activeSelections map[string]map[uint64]context.CancelFunc
	nextSelectionID  uint64
}

func NewFreewillOperatorControl(store DurableRunStore, clock func() time.Time) (*FreewillOperatorControl, error) {
	control, ok := store.(FreewillOperatorControlStore)
	if !ok {
		return nil, ErrFreewillControlUnavailable
	}
	if clock == nil {
		clock = time.Now
	}
	return &FreewillOperatorControl{store: control, clock: clock, activeSelections: make(map[string]map[uint64]context.CancelFunc)}, nil
}

func (c *FreewillOperatorControl) Status(ctx context.Context, agentID string, scope freewill.Scope) (FreewillOperatorControlState, error) {
	if err := validateFreewillControlIdentity(agentID, scope); err != nil {
		return FreewillOperatorControlState{}, err
	}
	return c.store.LoadFreewillOperatorControl(ctx, agentID, scope)
}

func (c *FreewillOperatorControl) SetHalted(ctx context.Context, agentID string, scope freewill.Scope, halted bool, actor string) (FreewillOperatorControlState, error) {
	if err := validateFreewillControlIdentity(agentID, scope); err != nil {
		return FreewillOperatorControlState{}, err
	}
	actor = strings.TrimSpace(actor)
	if actor == "" || len(actor) > 128 || strings.ContainsAny(actor, "\r\n\x00") {
		return FreewillOperatorControlState{}, errors.New("multiagent: Freewill operator actor is invalid")
	}
	state, err := c.store.SetFreewillOperatorHalt(ctx, agentID, scope, halted, actor, c.clock().UTC())
	if err != nil {
		return FreewillOperatorControlState{}, err
	}
	if halted {
		c.cancelActiveSelections(agentID, scope)
	}
	return state, nil
}

// RegisterActiveSelection lets an authenticated in-process halt cancel a
// selector immediately. The SQLite halt remains authoritative across process
// boundaries and is checked again before selection and checkpoint commits.
func (c *FreewillOperatorControl) RegisterActiveSelection(agentID string, scope freewill.Scope, cancel context.CancelFunc) (func(), error) {
	if c == nil || cancel == nil {
		return nil, ErrFreewillControlUnavailable
	}
	if err := validateFreewillControlIdentity(agentID, scope); err != nil {
		return nil, err
	}
	key := freewillOperatorControlKey(agentID, scope)
	c.mu.Lock()
	c.nextSelectionID++
	id := c.nextSelectionID
	if c.activeSelections[key] == nil {
		c.activeSelections[key] = make(map[uint64]context.CancelFunc)
	}
	c.activeSelections[key][id] = cancel
	c.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			c.mu.Lock()
			delete(c.activeSelections[key], id)
			if len(c.activeSelections[key]) == 0 {
				delete(c.activeSelections, key)
			}
			c.mu.Unlock()
		})
	}, nil
}

func (c *FreewillOperatorControl) cancelActiveSelections(agentID string, scope freewill.Scope) {
	key := freewillOperatorControlKey(agentID, scope)
	c.mu.Lock()
	cancels := make([]context.CancelFunc, 0, len(c.activeSelections[key]))
	for _, cancel := range c.activeSelections[key] {
		cancels = append(cancels, cancel)
	}
	c.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}

func validateFreewillControlIdentity(agentID string, scope freewill.Scope) error {
	if strings.TrimSpace(agentID) == "" || len(agentID) > 128 || strings.ContainsAny(agentID, "\r\n\x00") ||
		strings.TrimSpace(scope.ProjectID) == "" || len(scope.ProjectID) > 128 || strings.ContainsAny(scope.ProjectID, "\r\n\x00") ||
		strings.TrimSpace(scope.Workspace) == "" || len(scope.Workspace) > 512 || strings.ContainsAny(scope.Workspace, "\r\n\x00") {
		return errors.New("multiagent: Freewill operator control identity is invalid")
	}
	return nil
}

func ensureFreewillNotHalted(ctx context.Context, store DurableRunStore, agentID string, scope freewill.Scope) error {
	control, ok := store.(FreewillOperatorControlStore)
	if !ok {
		return ErrFreewillControlUnavailable
	}
	state, err := control.LoadFreewillOperatorControl(ctx, agentID, scope)
	if err != nil {
		return err
	}
	if state.Halted {
		return ErrFreewillHalted
	}
	return nil
}

func ensureFreewillApprovalGeneration(ctx context.Context, store DurableRunStore, record DurableRun) error {
	if record.Kind != DurableRunKindFreewill || record.Freewill == nil || record.State.RequestIdentity == nil {
		return ErrFreewillApprovalRevoked
	}
	control, ok := store.(FreewillOperatorControlStore)
	if !ok {
		return ErrFreewillControlUnavailable
	}
	state, err := control.LoadFreewillOperatorControl(ctx, record.State.RequestIdentity.RequesterID, record.Freewill.Scope)
	if err != nil {
		return err
	}
	if state.Halted {
		return ErrFreewillHalted
	}
	if record.Freewill.ApprovalGeneration != state.Generation {
		return ErrFreewillApprovalRevoked
	}
	return nil
}
