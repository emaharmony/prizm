package main

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/emaharmony/prizm/internal/event"
	"github.com/emaharmony/prizm/internal/workflow/multiagent"
)

var graphRoleTransport struct {
	sync.RWMutex
	publisher event.CommandPublisher
}

func configureGraphRolePublisher(publisher event.CommandPublisher) func() {
	graphRoleTransport.Lock()
	previous := graphRoleTransport.publisher
	graphRoleTransport.publisher = publisher
	graphRoleTransport.Unlock()
	return func() {
		graphRoleTransport.Lock()
		graphRoleTransport.publisher = previous
		graphRoleTransport.Unlock()
	}
}

func currentGraphRolePublisher() event.CommandPublisher {
	graphRoleTransport.RLock()
	defer graphRoleTransport.RUnlock()
	return graphRoleTransport.publisher
}

const (
	graphRoleDelegationSubject  = "prizm.graph.role.delegation"
	graphRoleDelegationDeadline = 30 * time.Minute
	// graphRoleDelegationRequestedEnv deliberately fails until the CLI composes
	// a durable publisher and graph-role worker. Accepting commands without
	// that consumer leaves role runs paused until their deadline.
	graphRoleDelegationRequestedEnv = "PRIZM_GRAPH_ROLE_DELEGATION"
)

// graphDelegationOutbox is the CLI composition adapter between the graph's
// delegation contract and the canonical event outbox. SQLiteOutbox.Accept is
// accept-once by Command.IdempotencyKey, so replaying a checkpointed pending
// dispatch after restart is safe.
type graphDelegationOutbox struct {
	*event.SQLiteOutbox
	publisher    event.CommandPublisher
	injectedOnly bool
}

func (o graphDelegationOutbox) Dispatch(ctx context.Context, subject string, command event.Command) error {
	if _, err := o.Accept(ctx, subject, command); err != nil {
		return err
	}
	if o.publisher == nil {
		if o.injectedOnly {
			return nil
		}
		return fmt.Errorf("graph role delegation publisher is unavailable")
	}
	dispatcher := event.Dispatcher{Outbox: o.SQLiteOutbox, Publisher: o.publisher,
		Lease: 30 * time.Second, MaxAttempts: 3, RetryAfter: 250 * time.Millisecond, DeferTerminalOnPublishFailure: true}
	var lastErr error
	for attempt := 0; attempt < dispatcher.MaxAttempts; attempt++ {
		processed, err := dispatcher.DispatchOne(ctx)
		if err == nil && processed {
			return nil
		}
		if err != nil {
			lastErr = err
		}
		timer := time.NewTimer(dispatcher.RetryAfter)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return lastErr
}

func newGraphDelegationOutbox(path string) (graphDelegationOutbox, *multiagent.DurableDelegationOptions, error) {
	return newGraphDelegationOutboxForComposition(path, false)
}

// newGraphDelegationOutboxForTest enables the otherwise unavailable delegation
// seam only for package tests that inject durable outcomes. Production callers
// must use newGraphDelegationOutbox.
func newGraphDelegationOutboxForTest(path string) (graphDelegationOutbox, *multiagent.DurableDelegationOptions, error) {
	return newGraphDelegationOutboxForComposition(path, true)
}

func newGraphDelegationOutboxForComposition(path string, enableDelegation bool) (graphDelegationOutbox, *multiagent.DurableDelegationOptions, error) {
	requested := os.Getenv(graphRoleDelegationRequestedEnv) == "1"
	publisher := currentGraphRolePublisher()
	if requested && publisher == nil {
		return graphDelegationOutbox{}, nil, fmt.Errorf("graph role delegation is unavailable: no durable graph-role worker is composed")
	}
	outbox, err := event.NewSQLiteOutbox(path)
	if err != nil {
		return graphDelegationOutbox{}, nil, err
	}
	adapter := graphDelegationOutbox{SQLiteOutbox: outbox, publisher: publisher, injectedOnly: enableDelegation && !requested}
	if !enableDelegation && !requested {
		return adapter, nil, nil
	}
	return adapter, &multiagent.DurableDelegationOptions{
		Subject:            graphRoleDelegationSubject,
		Dispatcher:         adapter,
		Outcomes:           adapter,
		Deadline:           graphRoleDelegationDeadline,
		RequireWorkspaceID: requested,
	}, nil
}
