package multiagent

// transitionKey is deliberately typed; free-form runner content never
// participates in route selection.
type transitionKey struct {
	role    Role
	outcome TransitionOutcome
}

// TransitionResolver resolves validated definition edges deterministically.
type TransitionResolver struct {
	routes map[transitionKey]ResolvedTransition
}

// NewTransitionResolver builds an immutable lookup table from a validated
// definition.
func NewTransitionResolver(definition Definition) (*TransitionResolver, error) {
	if err := definition.Validate(); err != nil {
		return nil, err
	}

	routes := make(map[transitionKey]ResolvedTransition, len(definition.Transitions))
	for _, rule := range definition.Transitions {
		routes[transitionKey{role: rule.From, outcome: rule.Outcome}] = ResolvedTransition(rule)
	}
	return &TransitionResolver{routes: routes}, nil
}

// Resolve returns the single declared transition for role and outcome.
func (r *TransitionResolver) Resolve(role Role, outcome TransitionOutcome) (ResolvedTransition, error) {
	if r == nil {
		return ResolvedTransition{}, &InvalidTransitionError{Role: role, Outcome: outcome}
	}
	transition, ok := r.routes[transitionKey{role: role, outcome: outcome}]
	if !ok {
		return ResolvedTransition{}, &InvalidTransitionError{Role: role, Outcome: outcome}
	}
	return transition, nil
}
