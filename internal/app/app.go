// Package app wires config, logging, and (later) gateway components.
package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/konor123/Free-Model-Router/internal/config"
	"github.com/konor123/Free-Model-Router/internal/logging"
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

// Run is the Phase 0 application loop: it validates the environment,
// logs the resolved configuration summary, and blocks until ctx is canceled.
func Run(ctx context.Context, cfg *config.Config, log *logging.Logger) error {
	log.Info("Free-Model-Router starting (phase 0)")
	log.Info("config: %s", cfg.SourcePath)
	log.Info("log level: %s", cfg.LogLevel)
	log.Info("gateway bind: %s", cfg.Bind)

	<-ctx.Done()
	return nil
}

var _ = filepath.Join // keep import stable until wiring grows
