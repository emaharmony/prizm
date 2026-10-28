package multiagent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/emaharmony/prizm/internal/freewill"
)

var (
	ErrFreewillWakeLeaseHeld = errors.New("multiagent: Freewill wake lease is held")
	ErrStaleFreewillWakeLease = errors.New("multiagent: stale Freewill wake lease")
)

func selectedFreewillCandidate(state *FreewillRunState) (freewill.CandidateIntention, bool) {
	if state == nil { return freewill.CandidateIntention{}, false }
	for _, candidate := range state.Candidates {
		if candidate.CandidateID == state.ChoiceID { return candidate, true }
	}
	return freewill.CandidateIntention{}, false
}

func freewillRequestFingerprint(request freewill.IntentionRequest) (string, freewill.IntentionRequest) {
	request.RequestedCapabilities = append([]string(nil), request.RequestedCapabilities...)
	sort.Strings(request.RequestedCapabilities)
	data, _ := json.Marshal(request)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), request
}

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
