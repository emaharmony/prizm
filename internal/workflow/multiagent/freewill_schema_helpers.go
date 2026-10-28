package multiagent

import (
	"crypto/sha256"
	"errors"
	"fmt"

	"github.com/emaharmony/prizm/internal/freewill"
)

var (
	ErrFreewillWakeLeaseHeld = errors.New("multiagent: Freewill wake lease is held")
	ErrStaleFreewillWakeLease = errors.New("multiagent: stale Freewill wake lease")
)

func freewillOperatorControlKey(agentID string, scope freewill.Scope) string {
	key := agentID + "\x00" + scope.ProjectID + "\x00" + scope.Workspace
	sum := sha256.Sum256([]byte(key))
	return fmt.Sprintf("%x", sum[:])
}
func freewillActorAdmissionKey(agentID string) string {
	sum := sha256.Sum256([]byte("freewill_actor\x00" + agentID))
	return "actor:" + fmt.Sprintf("%x", sum[:])
}

func freewillRunAgent(record DurableRun) (string, bool) {
	if record.Kind != DurableRunKindFreewill || record.Freewill == nil || record.State.RequestIdentity == nil || record.State.RequestIdentity.RequesterID == "" {
		return "", false
	}
	return record.State.RequestIdentity.RequesterID, true
}

func freewillAdmissionActive(record DurableRun) bool {
	if record.Kind != DurableRunKindFreewill || record.Freewill == nil {
		return false
	}
	switch record.Freewill.Lifecycle {
	case freewill.StateWaking, freewill.StateSelecting, freewill.StateProposed, freewill.StateApprovalPending, freewill.StateRunning:
		return true
	case freewill.StateSleeping, freewill.StateDenied, freewill.StateHalted:
		return false
	case freewill.StatePaused:
		return !validFreewillApprovalReceipt(record.Freewill, FreewillApprovalGranted)
	default:
		return true
	}
}

func validFreewillApprovalReceipt(state *FreewillRunState, decision FreewillApprovalDecision) bool {
	if state == nil || state.ApprovalReceipt == nil || state.ApprovalReceipt.ApprovalID == "" ||
		state.ApprovalRef != state.ApprovalReceipt.ApprovalID || state.ApprovalFingerprint != state.ApprovalReceipt.RequestFingerprint ||
		state.ApprovalGeneration != state.ApprovalReceipt.ControlGeneration {
		return false
	}
	selected, ok := selectedFreewillCandidate(state)
	if !ok { return false }
	fingerprint, _ := freewillRequestFingerprint(selected.Request)
	if fingerprint != state.ApprovalReceipt.RequestFingerprint { return false }
	return state.ApprovalReceipt.Decision == decision
}
