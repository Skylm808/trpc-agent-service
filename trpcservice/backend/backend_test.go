package backend

import (
	"context"
	"testing"
)

func TestBackendsRejectMissingConfiguration(t *testing.T) {
	if _, err := OpenPostgres(context.Background(), ""); err == nil {
		t.Fatal("OpenPostgres() error = nil")
	}
	if _, err := OpenRedis(context.Background(), ""); err == nil {
		t.Fatal("OpenRedis() error = nil")
	}
	if _, err := OpenRedis(context.Background(), "://bad"); err == nil {
		t.Fatal("OpenRedis(invalid) error = nil")
	}
}
