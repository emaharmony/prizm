package main

import (
	"context"
	"os"
	"time"

	"github.com/emaharmony/prizm/internal/event"
	"github.com/emaharmony/prizm/internal/workflow/multiagent"
)

const (
	graphRoleDelegationSubject  = "prizm.graph.role.delegation"
	graphRoleDelegationDeadline = 30 * time.Minute
	// graphRoleDelegationEnabledEnv remains opt-in until the CLI composes a
	// durable publisher and graph-role worker. Accepting commands without that
	// consumer leaves role runs paused until their deadline.
	graphRoleDelegationEnabledEnv = "PRIZM_GRAPH_ROLE_DELEGATION"
)

// graphDelegationOutbox is the CLI composition adapter between the graph's
// delegation contract and the canonical event outbox. SQLiteOutbox.Accept is
// accept-once by Command.IdempotencyKey, so replaying a checkpointed pending
// dispatch after restart is safe.
type graphDelegationOutbox struct {
	*event.SQLiteOutbox
}

func (o graphDelegationOutbox) Dispatch(ctx context.Context, subject string, command event.Command) error {
	_, err := o.Accept(ctx, subject, command)
	return err
}

func newGraphDelegationOutbox(path string) (graphDelegationOutbox, *multiagent.DurableDelegationOptions, error) {
	outbox, err := event.NewSQLiteOutbox(path)
	if err != nil {
		return graphDelegationOutbox{}, nil, err
	}
	adapter := graphDelegationOutbox{SQLiteOutbox: outbox}
	if os.Getenv(graphRoleDelegationEnabledEnv) != "1" {
		return adapter, nil, nil
	}
	return adapter, &multiagent.DurableDelegationOptions{
		Subject:    graphRoleDelegationSubject,
		Dispatcher: adapter,
		Outcomes:   adapter,
		Deadline:   graphRoleDelegationDeadline,
	}, nil
}
