package app

import (
	"context"
	"errors"
	"time"

	"github.com/konor123/Free-Model-Router/internal/gateway"
	"github.com/konor123/Free-Model-Router/internal/logging"
)

// CatalogRefreshInterval is the background cadence for re-discovering the
// provider catalog so new/removed models appear without a gateway restart.
const CatalogRefreshInterval = 30 * time.Minute

// CatalogRefreshTimeout bounds one discovery attempt; a slow provider must not
// pin the loop past the next scheduled tick.
const CatalogRefreshTimeout = 60 * time.Second

// startCatalogRefresh keeps the catalog fresh in the background. Refresh
// failures retain the current last-known-good snapshot, matching the
// benchmark refresh behavior.
func startCatalogRefresh(ctx context.Context, gw *gateway.Gateway, log *logging.Logger) {
	go func() {
		ticker := time.NewTicker(CatalogRefreshInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				refreshCtx, cancel := context.WithTimeout(ctx, CatalogRefreshTimeout)
				err := gw.RefreshCatalog(refreshCtx)
				cancel()
				switch {
				case err == nil:
					if log != nil {
						log.Info("background catalog refresh completed (revision %d)", gw.ControlSnapshot().CatalogRevision)
					}
				case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
					if log != nil {
						log.Warn("background catalog refresh interrupted: %v", err)
					}
				default:
					if log != nil {
						log.Warn("background catalog refresh: %v", err)
					}
				}
			}
		}
	}()
}
