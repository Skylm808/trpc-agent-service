package backend

import (
	"context"
	"errors"
	"sync"

	redis "github.com/redis/go-redis/v9"
)

// Redis adapts go-redis to session fencing and the shared run-event bus.
type Redis struct{ client *redis.Client }

// OpenRedis creates and verifies a Redis client from a redis:// URL.
func OpenRedis(ctx context.Context, rawURL string) (*Redis, error) {
	if ctx == nil || rawURL == "" {
		return nil, errors.New("backend: Redis context and URL are required")
	}
	options, err := redis.ParseURL(rawURL)
	if err != nil {
		return nil, errors.New("backend: invalid Redis URL")
	}
	client := redis.NewClient(options)
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		return nil, err
	}
	return &Redis{client: client}, nil
}

func (backend *Redis) Eval(ctx context.Context, script string, keys []string, args ...any) (any, error) {
	if backend == nil || backend.client == nil {
		return nil, errors.New("backend: nil Redis client")
	}
	return backend.client.Eval(ctx, script, keys, args...).Result()
}

func (backend *Redis) Publish(ctx context.Context, channel string, payload []byte) error {
	if backend == nil || backend.client == nil {
		return errors.New("backend: nil Redis client")
	}
	return backend.client.Publish(ctx, channel, payload).Err()
}

func (backend *Redis) Subscribe(ctx context.Context, channel string) (<-chan []byte, func() error, error) {
	if backend == nil || backend.client == nil || ctx == nil || channel == "" {
		return nil, nil, errors.New("backend: Redis subscription context and channel are required")
	}
	pubsub := backend.client.Subscribe(ctx, channel)
	if _, err := pubsub.Receive(ctx); err != nil {
		_ = pubsub.Close()
		return nil, nil, err
	}
	output := make(chan []byte, 64)
	done := make(chan struct{})
	var once sync.Once
	closeSubscription := func() error {
		var closeErr error
		once.Do(func() {
			close(done)
			closeErr = pubsub.Close()
		})
		return closeErr
	}
	go func() {
		defer close(output)
		defer closeSubscription()
		messages := pubsub.Channel()
		for {
			select {
			case <-done:
				return
			case message, ok := <-messages:
				if !ok {
					return
				}
				payload := []byte(message.Payload)
				select {
				case output <- payload:
				case <-done:
					return
				}
			}
		}
	}()
	return output, closeSubscription, nil
}

func (backend *Redis) Close() error {
	if backend == nil || backend.client == nil {
		return nil
	}
	return backend.client.Close()
}
