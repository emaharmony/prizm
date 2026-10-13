package freewill

import "fmt"

// State is a descriptive lifecycle state, not a permission or execution token.
type State string

const (
	StateDisabled        State = "disabled"
	StateSleeping        State = "sleeping"
	StateWaking          State = "waking"
	StateSelecting       State = "selecting"
	StateProposed        State = "proposed"
	StateApprovalPending State = "approval_pending"
	StateDenied          State = "denied"
	StatePaused          State = "paused"
	StateRunning         State = "running"
	StateHalted          State = "halted"
)

// InitialState is the fail-closed state for every new Freewill lifecycle.
const InitialState = StateDisabled

var transitions = map[State]map[State]struct{}{
	StateDisabled:        {StateSleeping: {}},
	StateSleeping:        {StateWaking: {}},
	StateWaking:          {StateSelecting: {}, StateSleeping: {}, StateHalted: {}},
	StateSelecting:       {StateProposed: {}, StateSleeping: {}, StateHalted: {}},
	StateProposed:        {StateApprovalPending: {}, StateDenied: {}, StatePaused: {}, StateHalted: {}},
	StateApprovalPending: {StateRunning: {}, StateDenied: {}, StatePaused: {}, StateHalted: {}},
	StatePaused:          {StateWaking: {}, StateHalted: {}},
	StateRunning:         {StatePaused: {}, StateHalted: {}},
	StateDenied:          {},
	StateHalted:          {},
}

// ValidateTransition checks only whether an edge exists in the documented
// lifecycle. In particular, approval_pending -> running does not prove an
// approval; the existing runtime must independently authorize every effect.
func ValidateTransition(from, to State) error {
	if _, ok := transitions[from]; !ok {
		return fmt.Errorf("unknown Freewill state %q", from)
	}
	if _, ok := transitions[to]; !ok {
		return fmt.Errorf("unknown Freewill state %q", to)
	}
	if _, ok := transitions[from][to]; !ok {
		return fmt.Errorf("invalid Freewill transition %q -> %q", from, to)
	}
	return nil
}
