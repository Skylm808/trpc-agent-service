package worker

import (
	"errors"
	"testing"

	"trpc.group/trpc-go/trpc-agent-go/event"
	"trpc.group/trpc-go/trpc-agent-go/model"
)

func TestEventProjectionSkipsZeroTokenUsage(t *testing.T) {
	called := false
	projection := eventProjection{onUsage: func(int64) error {
		called = true
		return errors.New("zero usage must not be reconciled")
	}}

	projection.Observe(&event.Event{Response: &model.Response{Usage: &model.Usage{}}})

	if called {
		t.Fatal("zero token usage invoked the budget reconciler")
	}
	if projection.policyErr != nil {
		t.Fatalf("policyErr=%v", projection.policyErr)
	}
}
