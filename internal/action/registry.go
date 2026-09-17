// Package action provides the registered actions system for Prizm V20.
//
// Registered actions are event-triggered behaviors — when an event matches
// a trigger pattern, the corresponding action is executed. This is the
// webhook-style system that makes Prizm reactive without model commands.
//
// Example:
//
//	actions:
//	  - trigger: "*.tool.completed"
//	    action: "prizm.cost.track"
//	    enabled: true
//
//	When any agent emits a tool.completed event, the cost tracker
//	automatically fires — no model command needed.
package action

import (
	"fmt"
	"strings"
	"sync"

	"github.com/emaharmony/prizm/internal/event"
)

// Action defines an event-triggered behavior.
type Action struct {
	// Trigger is the event type pattern to match.
	// Supports wildcards: "*" matches any single namespace segment,
	// "**" matches multiple segments.
	// Examples: "lumi.agent.output", "*.tool.completed", "**.failed"
	Trigger string `yaml:"trigger" json:"trigger"`

	// Action is the handler to execute when the trigger fires.
	// Format: "<namespace>.<action>" e.g., "remembrance.gate.extract"
	Action string `yaml:"action" json:"action"`

	// Enabled controls whether this action is active.
	Enabled bool `yaml:"enabled" json:"enabled"`
}

// Handler is a function that processes a triggered action.
type Handler func(evt event.Event, action Action) error

// Registry manages registered actions and their handlers.
type Registry struct {
	mu       sync.RWMutex
	actions  []Action
	handlers map[string]Handler // keyed by action name
}

// NewRegistry creates an empty action registry.
func NewRegistry() *Registry {
	return &Registry{
		actions:  make([]Action, 0),
		handlers: make(map[string]Handler),
	}
}

// RegisterAction adds an action trigger to the registry.
func (r *Registry) RegisterAction(action Action) error {
	if action.Trigger == "" {
		return fmt.Errorf("action: trigger is required")
	}
	if action.Action == "" {
		return fmt.Errorf("action: action is required")
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.actions = append(r.actions, action)
	return nil
}

// RegisterHandler registers a handler function for an action name.
// When a trigger fires and the action matches, this handler is called.
func (r *Registry) RegisterHandler(actionName string, handler Handler) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.handlers[actionName]; exists {
		return fmt.Errorf("action: handler %q already registered", actionName)
	}
	r.handlers[actionName] = handler
	return nil
}

// ProcessEvent evaluates all registered actions against the given event.
// For each matching trigger, it calls the corresponding handler.
// Returns the number of actions triggered and any errors.
func (r *Registry) ProcessEvent(evt event.Event) (int, []error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	triggered := 0
	var errs []error

	for _, action := range r.actions {
		if !action.Enabled {
			continue
		}

		if matchTrigger(action.Trigger, evt.Type) {
			handler, ok := r.handlers[action.Action]
			if !ok {
				errs = append(errs, fmt.Errorf("action: no handler for %q", action.Action))
				continue
			}

			if err := handler(evt, action); err != nil {
				errs = append(errs, fmt.Errorf("action %q handler: %w", action.Action, err))
				continue
			}
			triggered++
		}
	}

	return triggered, errs
}

// List returns all registered actions.
func (r *Registry) List() []Action {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]Action, len(r.actions))
	copy(result, r.actions)
	return result
}

// matchTrigger checks if an event type matches a trigger pattern.
//
// Pattern syntax:
//   - Exact match: "lumi.agent.output" matches "lumi.agent.output"
//   - Single wildcard: "*.tool.completed" matches any single namespace + ".tool.completed"
//   - Double wildcard: "**.failed" matches any prefix + ".failed"
func matchTrigger(pattern, eventType string) bool {
	// Exact match
	if pattern == eventType {
		return true
	}

	// No wildcards → no match
	if !strings.Contains(pattern, "*") {
		return false
	}

	patternParts := strings.Split(pattern, ".")
	eventParts := strings.Split(eventType, ".")

	return matchParts(patternParts, eventParts)
}

// matchParts recursively matches pattern segments against event segments.
func matchParts(pattern, event []string) bool {
	for len(pattern) > 0 {
		switch pattern[0] {
		case "**":
			// ** matches zero or more segments
			if len(pattern) == 1 {
				return true // ** at end matches everything
			}
			// Try matching remaining pattern at each position
			for i := 0; i <= len(event); i++ {
				if matchParts(pattern[1:], event[i:]) {
					return true
				}
			}
			return false
		case "*":
			// * matches exactly one segment
			if len(event) == 0 {
				return false
			}
			pattern = pattern[1:]
			event = event[1:]
		default:
			// Exact segment match
			if len(event) == 0 || pattern[0] != event[0] {
				return false
			}
			pattern = pattern[1:]
			event = event[1:]
		}
	}

	return len(event) == 0
}
