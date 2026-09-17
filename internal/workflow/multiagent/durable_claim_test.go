package multiagent

import (
	"context"
	"errors"
	"testing"
)

func TestFileRunClaimerExclusiveOwnershipAndSafeTakeover(t *testing.T) {
	claimer := FileRunClaimer{Root: t.TempDir()}
	first, err := claimer.Acquire(context.Background(), "run-claim-test")
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	secondClaimer := FileRunClaimer{Root: claimer.Root}
	if _, err := secondClaimer.Acquire(
		context.Background(), "run-claim-test"); !errors.Is(err, ErrRunClaimed) {
		t.Fatalf("expected duplicate claim rejection, got %v", err)
	}
	if err := first.Release(); err != nil {
		t.Fatalf("release first claim: %v", err)
	}

	takeover, err := secondClaimer.Acquire(
		context.Background(), "run-claim-test")
	if err != nil {
		t.Fatalf("safe takeover: %v", err)
	}
	if err := takeover.Release(); err != nil {
		t.Fatalf("release takeover claim: %v", err)
	}
}

func TestFileRunClaimerRejectsUnsafeRunID(t *testing.T) {
	claimer := FileRunClaimer{Root: t.TempDir()}
	for _, runID := range []string{"", ".", "..", "../escape", `..\escape`} {
		if _, err := claimer.Acquire(context.Background(), runID); err == nil {
			t.Errorf("unsafe run id %q was accepted", runID)
		}
	}
}
