package worker

import (
	"context"
	"errors"
	"testing"

	"trpc.group/trpc-go/trpc-agent-go/event"
	"trpc.group/trpc-go/trpc-agent-go/model"
)

func TestCancelScopesSameRequestIDByTenant(t *testing.T) {
	processor := &Processor{}
	ctxA, cancelA := context.WithCancel(context.Background())
	ctxB, cancelB := context.WithCancel(context.Background())
	defer cancelA()
	defer cancelB()
	processor.register("tenant-a", "same-request", cancelA)
	processor.register("tenant-b", "same-request", cancelB)

	if !processor.Cancel("tenant-a", "same-request") {
		t.Fatal("expected tenant-a request to be canceled")
	}
	select {
	case <-ctxA.Done():
	default:
		t.Fatal("tenant-a context was not canceled")
	}
	select {
	case <-ctxB.Done():
		t.Fatal("tenant-b request with the same ID was canceled")
	default:
	}
	if !processor.takeCanceled("tenant-a", "same-request") {
		t.Fatal("expected tenant-a cancellation marker")
	}
	if processor.takeCanceled("tenant-b", "same-request") {
		t.Fatal("tenant-b cancellation marker must remain false")
	}
}

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
