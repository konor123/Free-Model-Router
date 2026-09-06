// Package config loads and persists FMR configuration.
// Phase 0 covers loading, defaults, and atomic writes; schema migration lands in Phase 11.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Config is the Phase 0 configuration surface. Later phases extend it;
// PersistedConfig with SchemaVersion arrives in Phase 11.
type Config struct {
	// SourcePath is the file the config was loaded from ("" for defaults).
	SourcePath string `json:"-"`

	// Bind is the inference API bind address. Default 127.0.0.1 (PLAN_V7 §18).
	Bind string `json:"bind"`

	// LogLevel: debug | info | warn | error.
	LogLevel string `json:"logLevel"`
}

// Defaults returns the built-in defaults.
func Defaults() *Config {
	return &Config{
		Bind:     "127.0.0.1:8787",
		LogLevel: "info",
	}
}

// DefaultPath returns the OS user config path for Free-Model-Router.
func DefaultPath() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("user config dir: %w", err)
	}
	return filepath.Join(base, "Free-Model-Router", "config.json"), nil
}

// ErrNotFound is returned when the config file does not exist and defaults are used.
var ErrNotFound = errors.New("config file not found")

// Load reads the config file and applies defaults for missing fields.
// A missing file is not an error source: defaults are returned with SourcePath set.
func Load(path string) (*Config, error) {
	cfg := Defaults()
	cfg.SourcePath = path

	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cfg, nil
		}
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// Save writes the config atomically (write temp file in same dir, then rename).
func Save(path string, cfg *Config) error {
	if err := cfg.validate(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".fmr-config-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after successful rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename config: %w", err)
	}
	return nil
}

func (c *Config) validate() error {
	if c.Bind == "" {
		return errors.New("config: bind must not be empty")
	}
	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("config: invalid logLevel %q", c.LogLevel)
	}
	return nil
}
