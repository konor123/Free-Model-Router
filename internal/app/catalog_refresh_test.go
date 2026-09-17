package app

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/konor123/Free-Model-Router/internal/logging"
)

// TestStartCatalogRefreshStopsWithContext verifies the background refresher
// exits promptly on cancellation instead of leaking a goroutine per gateway.
func TestStartCatalogRefreshStopsWithContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	log, err := logging.New(logging.Error, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		startCatalogRefresh(ctx, nil, log)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("catalog refresher ignored context cancellation")
	}
}

// TestCatalogRefreshTimeoutBoundsDiscovery pins the per-attempt deadline so a
// hanging provider cannot stall the refresh cadence.
func TestCatalogRefreshTimeoutBoundsDiscovery(t *testing.T) {
	if CatalogRefreshTimeout <= 0 || CatalogRefreshTimeout >= CatalogRefreshInterval {
		t.Fatalf("timeout %v must be positive and below interval %v", CatalogRefreshTimeout, CatalogRefreshInterval)
	}
	if CatalogRefreshInterval != 30*time.Minute {
		t.Fatalf("interval = %v, want 30m", CatalogRefreshInterval)
	}
}
