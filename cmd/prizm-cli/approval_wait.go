package main

// registerApprovalWaiter creates (or returns) the channel a tool loop blocks
// on for a given approval. Called by the loop right after persistApproval
// returns the approval_id.
func (cc *conversationContext) registerApprovalWaiter(runID, approvalID string) chan approvalOutcome {
	cc.approvalWaitMu.Lock()
	defer cc.approvalWaitMu.Unlock()
	key := runID + ":" + approvalID
	ch, ok := cc.approvalWait[key]
	if !ok {
		ch = make(chan approvalOutcome, 1)
		cc.approvalWait[key] = ch
	}
	return ch
}

// signalApproval delivers the outcome to any blocked loop and removes the
// waiter. Safe to call when no loop is waiting (e.g. approval resolved from
// the CLI or a different surface) — the buffered channel is simply dropped.
func (cc *conversationContext) signalApproval(runID, approvalID string, out approvalOutcome) {
	cc.approvalWaitMu.Lock()
	key := runID + ":" + approvalID
	ch, ok := cc.approvalWait[key]
	if ok {
		delete(cc.approvalWait, key)
	}
	cc.approvalWaitMu.Unlock()
	if ok {
		select {
		case ch <- out:
		default:
			// Channel already drained (loop timed out) — drop silently
		}
	}
}