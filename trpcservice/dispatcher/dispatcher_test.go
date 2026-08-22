package dispatcher

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/liuzengh/trpc-agent-service/trpcservice/gateway"
)

func TestDispatcherOrdersSameSession(t *testing.T) {
	var mu sync.Mutex
	var order []string
	done := make(chan struct{}, 10)
	dispatcher, _ := New(context.Background(), func(_ context.Context, request gateway.RunRequest) error {
		mu.Lock()
		order = append(order, request.InboxID)
		mu.Unlock()
		done <- struct{}{}
		return nil
	})
	for i := 0; i < 10; i++ {
		_ = dispatcher.Submit(gateway.RunRequest{InboxID: string(rune('a' + i)), TenantID: "t", AppID: "a", UserID: "u", SessionID: "s"})
	}
	for i := 0; i < 10; i++ {
		<-done
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := dispatcher.Close(ctx); err != nil {
		t.Fatal(err)
	}
	for i, value := range order {
		if value != string(rune('a'+i)) {
			t.Fatalf("order=%v", order)
		}
	}
}

func TestDispatcherRunsDifferentSessionsInParallel(t *testing.T) {
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	dispatcher, _ := New(context.Background(), func(context.Context, gateway.RunRequest) error { started <- struct{}{}; <-release; return nil })
	_ = dispatcher.Submit(gateway.RunRequest{TenantID: "t", AppID: "a", UserID: "u", SessionID: "one"})
	_ = dispatcher.Submit(gateway.RunRequest{TenantID: "t", AppID: "a", UserID: "u", SessionID: "two"})
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("sessions serialized")
		}
	}
	close(release)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := dispatcher.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := dispatcher.Submit(gateway.RunRequest{}); err == nil {
		t.Fatal("submit after close succeeded")
	}
}
