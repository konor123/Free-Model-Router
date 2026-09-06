// Package config loads and persists Free-Model-Router configuration and
// secrets-related state.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	// ApplicationName is the directory name used below the OS user data roots.
	ApplicationName = "Free-Model-Router"

	// CurrentSchemaVersion is the newest on-disk configuration schema understood
	// by this build. A zero version is accepted as the legacy pre-Phase 11 form.
	CurrentSchemaVersion = 1

	// CorruptConfigSuffix is appended to a config path when malformed state is
	// quarantined during recovery.
	CorruptConfigSuffix = ".corrupt"

	maxConfigBytes = 1 << 20
)

// PersistedConfig is the user-controlled, non-secret state written to disk.
// Secrets and runtime/usage state deliberately live in separate stores.
type PersistedConfig struct {
	SchemaVersion int `json:"schemaVersion"`

	// SourcePath is the file the config was loaded from (not persisted).
	SourcePath string `json:"-"`

	// Bind is the inference API bind address. Default 127.0.0.1 (PLAN_V7 §18).
	Bind string `json:"bind"`

	// ManagementBind is the localhost-only control API bind address. It is kept
	// separate from Bind so an inference API may be exposed deliberately without
	// exposing management operations.
	ManagementBind string `json:"managementBind"`

	// LogLevel: debug | info | warn | error.
	LogLevel string `json:"logLevel"`

	// ModelPool is the persisted list of selected provider-model identifiers.
	ModelPool []string `json:"modelPool,omitempty"`

	// ProviderEnabled records explicit provider enablement choices.
	ProviderEnabled map[string]bool `json:"providerEnabled,omitempty"`

	// PinnedModel is the optional primary model. When set, it must be in
	// ModelPool so a stale pin cannot bypass pool membership.
	PinnedModel string `json:"pinnedModel,omitempty"`

	// ModelPoolMode preserves whether the user wants automatic or manual pool
	// reconciliation. The empty legacy value is treated as Automatic.
	ModelPoolMode string `json:"modelPoolMode,omitempty"`
}

// Config is retained as a source-compatible name for callers from Phase 0.
// It is an alias so old and new persistence APIs share the same schema.
type Config = PersistedConfig

// Defaults returns built-in defaults in the current persistence schema.
func Defaults() *Config {
	return PersistedDefaults()
}

// PersistedDefaults returns built-in defaults for a persisted configuration.
func PersistedDefaults() *PersistedConfig {
	return &PersistedConfig{
		SchemaVersion:  CurrentSchemaVersion,
		Bind:           "127.0.0.1:8787",
		ManagementBind: "127.0.0.1:8788",
		LogLevel:       "info",
		ModelPoolMode:  "Automatic",
	}
}

// Clone returns a deep copy suitable for handing to another owner.
func (c *PersistedConfig) Clone() *PersistedConfig {
	if c == nil {
		return nil
	}
	out := *c
	out.ModelPool = append([]string(nil), c.ModelPool...)
	if c.ProviderEnabled != nil {
		out.ProviderEnabled = make(map[string]bool, len(c.ProviderEnabled))
		for provider, enabled := range c.ProviderEnabled {
			out.ProviderEnabled[provider] = enabled
		}
	}
	return &out
}

// ConfigDir returns the OS-specific per-user configuration directory.
func ConfigDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("user config dir: %w", err)
	}
	return filepath.Join(base, ApplicationName), nil
}

// DefaultConfigDir is a descriptive alias for ConfigDir.
func DefaultConfigDir() (string, error) { return ConfigDir() }

// CacheDir returns the OS-specific per-user cache directory.
func CacheDir() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("user cache dir: %w", err)
	}
	return filepath.Join(base, ApplicationName), nil
}

// DefaultCacheDir is a descriptive alias for CacheDir.
func DefaultCacheDir() (string, error) { return CacheDir() }

// DefaultPath returns the OS user config path for Free-Model-Router.
func DefaultPath() (string, error) {
	dir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.json"), nil
}

// DefaultCachePath returns a path below the OS-specific cache directory.
func DefaultCachePath(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "cache.json"
	}
	if name == "." || name == ".." || strings.ContainsAny(name, `/\\`) || filepath.Base(name) != name {
		return "", errors.New("cache file name must not contain a directory")
	}
	dir, err := CacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, name), nil
}

// DefaultUsagePath returns the user-scoped durable usage-log path. Usage logs
// are kept below a separate logs directory rather than mixing with config,
// secrets, or disposable cache snapshots.
func DefaultUsagePath() (string, error) {
	dir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "logs", "usage.jsonl"), nil
}

// ErrNotFound is retained for callers that want to classify an absent config.
// Load itself intentionally treats a missing config as a defaults case.
var ErrNotFound = errors.New("config file not found")

// Load reads the config file, applies defaults for missing fields, migrates a
// legacy schema, and recovers malformed files into a .corrupt quarantine file.
// A missing or recovered file returns defaults without an error so startup is
// not blocked by disposable local state.
func Load(path string) (*Config, error) {
	return LoadPersisted(path)
}

// LoadPersisted is the Phase 11 persistence API.
func LoadPersisted(path string) (*PersistedConfig, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("config path must not be empty")
	}

	cfg := PersistedDefaults()
	cfg.SourcePath = path
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cfg, nil
		}
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	if len(data) > maxConfigBytes {
		return recoverCorruptConfig(path)
	}
	// Keep zero as the in-memory marker while decoding so a missing field is
	// distinguishable from an already-current schema and can be migrated.
	cfg.SchemaVersion = 0

	if err := json.Unmarshal(data, cfg); err != nil {
		return recoverCorruptConfig(path)
	}
	if cfg.SchemaVersion > CurrentSchemaVersion {
		// A future schema must not be silently replaced by defaults. The caller
		// needs an upgrade rather than destructive recovery.
		return nil, fmt.Errorf("config schema version %d is newer than supported version %d", cfg.SchemaVersion, CurrentSchemaVersion)
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}

	migrated := cfg.SchemaVersion != CurrentSchemaVersion
	cfg.SchemaVersion = CurrentSchemaVersion
	if migrated {
		if err := SavePersisted(path, cfg); err != nil {
			return nil, fmt.Errorf("persist migrated config: %w", err)
		}
	}
	return cfg, nil
}

// Save writes the config atomically and protects the resulting file from other
// users. The input is not mutated; a zero schema version is upgraded on disk.
func Save(path string, cfg *Config) error {
	return SavePersisted(path, cfg)
}

// SavePersisted writes a PersistedConfig using a same-directory temp file and
// rename, so readers see either the old complete file or the new complete file.
func SavePersisted(path string, cfg *PersistedConfig) error {
	if cfg == nil {
		return errors.New("config must not be nil")
	}
	toSave := cfg.Clone()
	if toSave.SchemaVersion == 0 {
		toSave.SchemaVersion = CurrentSchemaVersion
	}
	defaults := PersistedDefaults()
	if toSave.ManagementBind == "" {
		toSave.ManagementBind = defaults.ManagementBind
	}
	if toSave.ModelPoolMode == "" {
		toSave.ModelPoolMode = defaults.ModelPoolMode
	}
	if err := toSave.validate(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(toSave, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	if err := atomicWriteFile(path, data, 0o600, ".fmr-config-*.tmp"); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	return nil
}

func (c *PersistedConfig) validate() error {
	if c == nil {
		return errors.New("config must not be nil")
	}
	if c.SchemaVersion < 0 {
		return fmt.Errorf("config: invalid schema version %d", c.SchemaVersion)
	}
	if c.SchemaVersion > CurrentSchemaVersion {
		return fmt.Errorf("config: unsupported schema version %d", c.SchemaVersion)
	}
	if c.Bind == "" {
		return errors.New("config: bind must not be empty")
	}
	if c.ManagementBind == "" {
		return errors.New("config: management bind must not be empty")
	}
	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("config: invalid logLevel %q", c.LogLevel)
	}
	if c.ModelPoolMode != "" && c.ModelPoolMode != "Automatic" && c.ModelPoolMode != "Manual" {
		return fmt.Errorf("config: invalid modelPoolMode %q", c.ModelPoolMode)
	}
	if c.PinnedModel != "" {
		found := false
		for _, id := range c.ModelPool {
			if id == c.PinnedModel {
				found = true
				break
			}
		}
		if !found {
			return errors.New("config: pinned model must be in model pool")
		}
	}
	return nil
}

func recoverCorruptConfig(path string) (*PersistedConfig, error) {
	_, err := quarantineCorruptConfig(path)
	if err != nil {
		return nil, fmt.Errorf("recover corrupt config: %w", err)
	}
	cfg := PersistedDefaults()
	cfg.SourcePath = path
	if err := SavePersisted(path, cfg); err != nil {
		return nil, fmt.Errorf("restore default config: %w", err)
	}
	return cfg, nil
}

func quarantineCorruptConfig(path string) (string, error) {
	backup := path + CorruptConfigSuffix
	if _, err := os.Stat(backup); err == nil {
		for n := 1; ; n++ {
			candidate := backup + "." + strconv.Itoa(n)
			_, statErr := os.Stat(candidate)
			if errors.Is(statErr, os.ErrNotExist) {
				backup = candidate
				break
			}
			if statErr != nil {
				return "", statErr
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err := os.Rename(path, backup); err != nil {
		return "", err
	}
	return backup, nil
}

// atomicWriteFile writes data with a protected file mode. The directory is
// also user-only, keeping fallback secret files separate from normal config.
func atomicWriteFile(path string, data []byte, mode os.FileMode, tempPattern string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("path must not be empty")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create directory: %w", err)
	}
	if dir != "." {
		if err := os.Chmod(dir, 0o700); err != nil {
			return fmt.Errorf("protect directory: %w", err)
		}
	}
	tmp, err := os.CreateTemp(dir, tempPattern)
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		_ = os.Remove(tmpName)
	}()
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("protect temp file: %w", err)
	}
	if err := writeAll(tmp, data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := replaceFile(tmpName, path); err != nil {
		return fmt.Errorf("replace file: %w", err)
	}
	if err := os.Chmod(path, mode); err != nil {
		return fmt.Errorf("protect file: %w", err)
	}
	return nil
}

func writeAll(w io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := w.Write(data)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}

func replaceFile(tempName, targetName string) error {
	if err := os.Rename(tempName, targetName); err == nil {
		return nil
	}

	// Some Windows filesystem providers reject replacing an existing file with
	// Rename. Keep a recoverable backup while installing the complete temp file.
	backupName := targetName + ".bak"
	_ = os.Remove(backupName)
	backupMoved := false
	if err := os.Rename(targetName, backupName); err != nil {
		if !os.IsNotExist(err) {
			return err
		}
	} else {
		backupMoved = true
	}
	if err := os.Rename(tempName, targetName); err != nil {
		if backupMoved {
			_ = os.Rename(backupName, targetName)
		}
		return err
	}
	if backupMoved {
		_ = os.Remove(backupName)
	}
	return nil
}
