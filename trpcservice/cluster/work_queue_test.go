package cluster

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/liuzengh/trpc-agent-service/trpcservice/gateway"
)

type queueBackend struct {
	items chan StreamMessage
	next  atomic.Int64
	acks  atomic.Int64
}

func newQueueBackend() *queueBackend                                            { return &queueBackend{items: make(chan StreamMessage, 128)} }
func (*queueBackend) CreateConsumerGroup(context.Context, string, string) error { return nil }
func (backend *queueBackend) AddStream(_ context.Context, _ string, payload []byte) error {
	id := backend.next.Add(1)
	backend.items <- StreamMessage{ID: time.Unix(0, id).Format(time.RFC3339Nano), Payload: append([]byte(nil), payload...)}
	return nil
}
func (backend *queueBackend) ReadGroup(ctx context.Context, _, _, _, start string, _ int64, block time.Duration) ([]StreamMessage, error) {
	if start == "0" {
		return nil, nil
	}
	timer := time.NewTimer(block)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case item := <-backend.items:
		return []StreamMessage{item}, nil
	case <-timer.C:
		return nil, nil
	}
}
func (backend *queueBackend) AckStream(context.Context, string, string, string) error {
	backend.acks.Add(1)
	return nil
}

func TestWorkQueueConsumerGroupRunsEachRequestOnceAcrossNodes(t *testing.T) {
	backend := newQueueBackend()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var mu sync.Mutex
	seen := make(map[string]int)
	handler := func(_ context.Context, request gateway.RunRequest) error {
		mu.Lock()
		seen[request.InboxID]++
		mu.Unlock()
		return nil
	}
	first, err := NewWorkQueue(ctx, backend, handler, WorkQueueConfig{NodeID: "node-a", Concurrency: 2, ReadBlock: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewWorkQueue(ctx, backend, handler, WorkQueueConfig{NodeID: "node-b", Concurrency: 2, ReadBlock: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close(context.Background())
	defer second.Close(context.Background())
	for index := 0; index < 40; index++ {
		id := time.Unix(0, int64(index+1)).Format(time.RFC3339Nano)
		if err := first.Submit(gateway.RunRequest{InboxID: id, ClaimToken: "claim-" + id}); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && backend.acks.Load() != 40 {
		time.Sleep(5 * time.Millisecond)
	}
	if backend.acks.Load() != 40 {
		t.Fatalf("acks=%d", backend.acks.Load())
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 40 {
		t.Fatalf("unique requests=%d", len(seen))
	}
	for id, count := range seen {
		if count != 1 {
			t.Fatalf("request %q executions=%d", id, count)
		}
	}
}
