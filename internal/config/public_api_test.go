package config_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/konor123/Free-Model-Router/internal/app"
	"github.com/konor123/Free-Model-Router/internal/config"
)

type publicCredentialBackend struct {
	value string
}

func (b *publicCredentialBackend) Get(string) (string, error) {
	if b.value == "" {
		return "", config.ErrSecretNotFound
	}
	return b.value, nil
}

func (b *publicCredentialBackend) Set(_ string, value string) error {
	b.value = value
	return nil
}

func (b *publicCredentialBackend) Delete(string) error {
	b.value = ""
	return nil
}

func TestPublicPersistenceAndSecretContracts(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	cfg := &config.PersistedConfig{
		Bind:            "127.0.0.1:9191",
		LogLevel:        "debug",
		ModelPool:       []string{"opencode/model"},
		ProviderEnabled: map[string]bool{"opencode": true},
		PinnedModel:     "opencode/model",
	}
	if err := config.SavePersisted(configPath, cfg); err != nil {
		t.Fatalf("SavePersisted: %v", err)
	}
	loaded, err := config.LoadPersisted(configPath)
	if err != nil {
		t.Fatalf("LoadPersisted: %v", err)
	}
	if loaded.SchemaVersion != config.CurrentSchemaVersion || loaded.PinnedModel != cfg.PinnedModel || !loaded.ProviderEnabled["opencode"] {
		t.Fatalf("public config contract mismatch: %+v", loaded)
	}

	secretPath := filepath.Join(dir, "secrets.json")
	backend := &publicCredentialBackend{value: "credential-value"}
	secrets := config.NewSecretStoreWithBackend(secretPath, backend)
	const key = "FMR_PUBLIC_API_SECRET"
	t.Setenv(key, "environment-value")
	if got, err := secrets.GetSecret(key); err != nil || got != "environment-value" {
		t.Fatalf("environment override contract: got %q, err %v", got, err)
	}
	t.Setenv(key, "")
	if got, err := secrets.GetSecret(key); err != nil || got != "credential-value" {
		t.Fatalf("credential fallback contract: got %q, err %v", got, err)
	}
	if err := secrets.SetSecret(key, "updated-value"); err != nil {
		t.Fatalf("SetSecret: %v", err)
	}
	if got, err := secrets.Get(key); err != nil || got != "updated-value" {
		t.Fatalf("updated credential contract: got %q, err %v", got, err)
	}
	if err := secrets.DeleteSecret(key); err != nil {
		t.Fatalf("DeleteSecret: %v", err)
	}
}

func TestAppLoadConfigUsesPersistedConfigContract(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"bind":"127.0.0.1:9292","logLevel":"warn"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := app.LoadConfig(path)
	if err != nil {
		t.Fatalf("app.LoadConfig: %v", err)
	}
	if cfg.Bind != "127.0.0.1:9292" || cfg.LogLevel != "warn" || cfg.SchemaVersion != config.CurrentSchemaVersion {
		t.Fatalf("app/config integration mismatch: %+v", cfg)
	}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		t.Fatal("app.LoadConfig removed migrated config")
	}
}

func TestPublicDefaultLocationsAndSecretEnvironmentOverride(t *testing.T) {
	configRoot := t.TempDir()
	cacheRoot := t.TempDir()
	// Set all supported user-data variables. Only the variables relevant to the
	// current OS are consulted by os.UserConfigDir/UserCacheDir.
	t.Setenv("APPDATA", configRoot)
	t.Setenv("LOCALAPPDATA", cacheRoot)
	t.Setenv("XDG_CONFIG_HOME", configRoot)
	t.Setenv("XDG_CACHE_HOME", cacheRoot)

	configPath, err := config.DefaultPath()
	if err != nil {
		t.Fatalf("DefaultPath: %v", err)
	}
	cachePath, err := config.DefaultCachePath("snapshot.json")
	if err != nil {
		t.Fatalf("DefaultCachePath: %v", err)
	}
	if filepath.Base(filepath.Dir(configPath)) != config.ApplicationName || filepath.Base(filepath.Dir(cachePath)) != config.ApplicationName {
		t.Fatalf("unexpected default state paths: config=%q cache=%q", configPath, cachePath)
	}
	loaded, err := app.LoadConfig("")
	if err != nil {
		t.Fatalf("app.LoadConfig default path: %v", err)
	}
	if loaded.Bind == "" || loaded.SchemaVersion != config.CurrentSchemaVersion {
		t.Fatalf("default config contract mismatch: %+v", loaded)
	}

	const key = "FMR_DEFAULT_SECRET_ENV"
	t.Setenv(key, "environment-only-value")
	store := config.NewSecretStore()
	if got, err := store.Get(key); err != nil || got != "environment-only-value" {
		t.Fatalf("default SecretStore environment override: got %q, err %v", got, err)
	}
}
