// Package app wires config, logging, and (later) gateway components.
package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/konor123/Free-Model-Router/internal/config"
	"github.com/konor123/Free-Model-Router/internal/gateway"
	"github.com/konor123/Free-Model-Router/internal/logging"
	"github.com/konor123/Free-Model-Router/internal/providers/opencode"
)

// LoadConfig loads the gateway configuration from path, or the OS default
// location when path is empty.
func LoadConfig(path string) (*config.Config, error) {
	if path == "" {
		var err error
		path, err = config.DefaultPath()
		if err != nil {
			return nil, fmt.Errorf("resolve default config path: %w", err)
		}
	}
	return config.Load(path)
}

// NewLogger builds the process logger from configuration.
func NewLogger(cfg *config.Config) (*logging.Logger, error) {
	min, err := logging.ParseLevel(cfg.LogLevel)
	if err != nil {
		return nil, err
	}
	return logging.New(min, os.Stderr)
}

// Run boots the gateway HTTP server and blocks until ctx is canceled.
func Run(ctx context.Context, cfg *config.Config, log *logging.Logger) error {
	log.Info("Free-Model-Router starting (phase 2)")
	log.Info("config: %s", cfg.SourcePath)
	log.Info("log level: %s", cfg.LogLevel)
	log.Info("gateway bind: %s", cfg.Bind)

	prov := opencode.New("") // default OpenCode Public base URL
	gw, err := gateway.NewGateway(ctx, prov)
	if err != nil {
		return fmt.Errorf("init gateway: %w", err)
	}

	srv := &http.Server{Addr: cfg.Bind, Handler: gw.Handler()}
	errCh := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()
	log.Info("listening on http://%s", cfg.Bind)

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Warn("shutdown: %v", err)
	}
	return nil
}
