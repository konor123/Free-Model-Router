package app

import (
	"context"
	"errors"
	"time"

	"github.com/konor123/Free-Model-Router/internal/benchmarks/openevals"
	"github.com/konor123/Free-Model-Router/internal/gateway"
	"github.com/konor123/Free-Model-Router/internal/logging"
	"github.com/konor123/Free-Model-Router/internal/scoring"
)

// startBenchmarkRefresh installs a usable local cache before serving and keeps
// it fresh in the background. Fetch failures deliberately retain the current
// in-memory last-known-good snapshot.
func startBenchmarkRefresh(ctx context.Context, gw *gateway.Gateway, cache *scoring.Cache, log *logging.Logger) {
	if cached, err := cache.LoadLastKnownGood(); err == nil {
		if err := gw.SetBenchmarkSnapshot(cached); err != nil && log != nil {
			log.Warn("install benchmark cache: %v", err)
		} else if err == nil {
			gw.SetBenchmarkSource("cache", time.Now())
		}
	}
	refresh := func() {
		attemptedAt := time.Now()
		gw.MarkBenchmarkFetching(attemptedAt)
		fetchCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		snapshot, err := (openevals.Client{}).Fetch(fetchCtx)
		if err != nil {
			gw.MarkBenchmarkFailure(attemptedAt, benchmarkErrorCode(err))
			if log != nil {
				log.Warn("refresh OpenEvals benchmark: %v", err)
			}
			return
		}
		if err := cache.Save(snapshot); err != nil {
			if log != nil {
				log.Warn("save OpenEvals benchmark: %v", err)
			}
		}
		if err := gw.SetBenchmarkSnapshot(snapshot); err != nil {
			gw.MarkBenchmarkFailure(attemptedAt, "invalid_snapshot")
			if log != nil {
				log.Warn("install OpenEvals benchmark: %v", err)
			}
			return
		}
		gw.SetBenchmarkSource("live", time.Now())
	}
	go func() {
		refresh()
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				refresh()
			}
		}
	}()
}

func benchmarkErrorCode(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "fetch_timeout"
	}
	if errors.Is(err, context.Canceled) {
		return "fetch_canceled"
	}
	return "fetch_failed"
}
