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
