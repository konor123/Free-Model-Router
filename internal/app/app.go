// Package app wires config, logging, and (later) gateway components.
package app

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/konor123/Free-Model-Router/internal/config"
	"github.com/konor123/Free-Model-Router/internal/gateway"
	"github.com/konor123/Free-Model-Router/internal/logging"
	"github.com/konor123/Free-Model-Router/internal/provider"
	"github.com/konor123/Free-Model-Router/internal/providers/opencode"
	"github.com/konor123/Free-Model-Router/internal/usage"
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
	return RunWithProvider(ctx, cfg, log, opencode.New(""))
}

// RunWithProvider boots the same application wiring as Run with an injected
// provider. The injection keeps production defaults unchanged while allowing
// an actual HTTP app-to-provider integration test to use a local mock server.
func RunWithProvider(ctx context.Context, cfg *config.Config, log *logging.Logger, prov provider.Provider) error {
	if prov == nil {
		return errors.New("provider must not be nil")
	}
	log.Info("Free-Model-Router starting (phase 2)")
	log.Info("config: %s", cfg.SourcePath)
	log.Info("log level: %s", cfg.LogLevel)
	log.Info("gateway bind: %s", cfg.Bind)

	gw, err := gateway.NewGateway(ctx, prov)
	if err != nil {
		return fmt.Errorf("init gateway: %w", err)
	}
	usageStore := usage.NewMemory()
	if cfg.SourcePath != "" {
		if path, pathErr := config.DefaultUsagePath(); pathErr == nil {
			if persisted, storeErr := usage.New(path); storeErr == nil {
				usageStore = persisted
			}
		}
	}
	gw.SetUsageSink(usageStore)
	defer usageStore.Close()
	gw.StartProbes(ctx)
	defer gw.StopProbes()

	listener, err := net.Listen("tcp", cfg.Bind)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.Bind, err)
	}
	defer listener.Close()
	srv := &http.Server{Addr: cfg.Bind, Handler: gw.Handler()}
	errCh := make(chan error, 1)
	go func() {
		if err := srv.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
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
