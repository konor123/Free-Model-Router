package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestDefaultsPassValidation(t *testing.T) {
	if err := Defaults().validate(); err != nil {
		t.Fatalf("defaults invalid: %v", err)
	}
}

func TestLoadMissingFileUsesDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nonexistent", "config.json")
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load on missing file: %v", err)
	}
	if cfg.Bind != "127.0.0.1:8787" {
		t.Fatalf("expected default bind, got %q", cfg.Bind)
	}
	if cfg.SchemaVersion != CurrentSchemaVersion {
		t.Fatalf("expected current schema version, got %d", cfg.SchemaVersion)
	}
}

func TestSaveLoadRoundTripAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	in := &Config{Bind: "127.0.0.1:9999", LogLevel: "debug"}
	if err := Save(path, in); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// No temp leftovers.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected exactly 1 file after atomic save, got %d", len(entries))
	}

	out, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if out.Bind != in.Bind || out.LogLevel != in.LogLevel {
		t.Fatalf("round trip mismatch: %+v != %+v", out, in)
	}

	if err := Save(path, &Config{Bind: "127.0.0.1:10000", LogLevel: "warn"}); err != nil {
		t.Fatalf("second Save: %v", err)
	}
	out, err = Load(path)
	if err != nil {
		t.Fatalf("Load after overwrite: %v", err)
	}
	if out.Bind != "127.0.0.1:10000" || out.LogLevel != "warn" {
		t.Fatalf("overwrite mismatch: %+v", out)
	}
	entries, err = os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir after overwrite: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected no backup/temp leftovers, got %d files", len(entries))
	}
}

func TestLoadRejectsInvalidLogLevel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"logLevel":"loud"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected error for invalid logLevel")
	}
}

func TestPersistedConfigSchemaMigration(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	legacy := []byte(`{"bind":"127.0.0.1:9090","logLevel":"debug"}`)
	if err := os.WriteFile(path, legacy, 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadPersisted(path)
	if err != nil {
		t.Fatalf("LoadPersisted legacy config: %v", err)
	}
	if cfg.SchemaVersion != CurrentSchemaVersion {
		t.Fatalf("schema version = %d, want %d", cfg.SchemaVersion, CurrentSchemaVersion)
	}
	if cfg.Bind != "127.0.0.1:9090" || cfg.LogLevel != "debug" {
		t.Fatalf("legacy values were not preserved: %+v", cfg)
	}

	var migrated map[string]any
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &migrated); err != nil {
		t.Fatalf("migrated config is invalid JSON: %v", err)
	}
	if got, ok := migrated["schemaVersion"].(float64); !ok || int(got) != CurrentSchemaVersion {
		t.Fatalf("migrated file schemaVersion = %#v, want %d", migrated["schemaVersion"], CurrentSchemaVersion)
	}
}

func TestLoadCorruptConfigRecoversDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	corrupt := []byte(`{"bind":`)
	if err := os.WriteFile(path, corrupt, 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load corrupt config: %v", err)
	}
	if cfg.Bind != Defaults().Bind || cfg.LogLevel != Defaults().LogLevel {
		t.Fatalf("recovery did not return defaults: %+v", cfg)
	}
	if _, err := os.Stat(path + CorruptConfigSuffix); err != nil {
		t.Fatalf("corrupt config backup missing: %v", err)
	}
}

type fakeCredentialBackend struct {
	value   string
	err     error
	setErr  error
	deleted bool
}

func (f *fakeCredentialBackend) Get(string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	if f.value == "" {
		return "", ErrSecretNotFound
	}
	return f.value, nil
}

func (f *fakeCredentialBackend) Set(_ string, value string) error {
	if f.setErr != nil {
		return f.setErr
	}
	f.value = value
	return nil
}

func (f *fakeCredentialBackend) Delete(string) error {
	f.deleted = true
	f.value = ""
	return nil
}

func TestSecretStorePriority(t *testing.T) {
	const key = "FMR_TEST_SECRET"
	const secret = "file-secret-value"
	path := filepath.Join(t.TempDir(), "secrets.json")
	if err := os.WriteFile(path, []byte(fmt.Sprintf(`{"schemaVersion":1,"secrets":{"%s":%q}}`, key, secret)), 0o600); err != nil {
		t.Fatal(err)
	}
	backend := &fakeCredentialBackend{value: "credential-secret-value"}
	store := NewSecretStoreWithBackend(path, backend)

	t.Setenv(key, "environment-secret-value")
	got, err := store.Get(key)
	if err != nil || got != "environment-secret-value" {
		t.Fatalf("environment priority: got %q, err %v", got, err)
	}

	t.Setenv(key, "")
	got, err = store.Get(key)
	if err != nil || got != "credential-secret-value" {
		t.Fatalf("credential priority: got %q, err %v", got, err)
	}

	backend.value = ""
	got, err = store.Get(key)
	if err != nil || got != secret {
		t.Fatalf("file fallback: got %q, err %v", got, err)
	}
}

func TestSecretStoreDoesNotExposeBackendSecretErrors(t *testing.T) {
	const secret = "must-not-appear-in-errors"
	backend := &fakeCredentialBackend{err: errors.New("backend failed: " + secret)}
	store := NewSecretStoreWithBackend(filepath.Join(t.TempDir(), "secrets.json"), backend)

	_, err := store.Get("FMR_TEST_SECRET")
	if err == nil {
		t.Fatal("expected missing secret error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("secret leaked through error: %v", err)
	}
}

func TestSecretStoreFileFallbackPersistsAtomically(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "secrets.json")
	store := NewSecretStoreWithBackend(path, nil)
	if err := store.Set("FMR_FILE_SECRET", "file-value"); err != nil {
		t.Fatalf("Set fallback secret: %v", err)
	}
	if got, err := store.Get("FMR_FILE_SECRET"); err != nil || got != "file-value" {
		t.Fatalf("Get fallback secret: got %q, err %v", got, err)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected only protected secret file, got %d entries", len(entries))
	}
	if err := store.Delete("FMR_FILE_SECRET"); err != nil {
		t.Fatalf("Delete fallback secret: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("secret file still exists after deleting last secret: %v", err)
	}
}

func TestDefaultStatePathsUseApplicationDirectory(t *testing.T) {
	configDir, err := ConfigDir()
	if err != nil {
		t.Fatalf("ConfigDir: %v", err)
	}
	cacheDir, err := CacheDir()
	if err != nil {
		t.Fatalf("CacheDir: %v", err)
	}
	if filepath.Base(configDir) != ApplicationName || filepath.Base(cacheDir) != ApplicationName {
		t.Fatalf("unexpected state dirs: config=%q cache=%q", configDir, cacheDir)
	}
	secretPath, err := DefaultSecretPath()
	if err != nil {
		t.Fatalf("DefaultSecretPath: %v", err)
	}
	if filepath.Dir(secretPath) != configDir || filepath.Base(secretPath) != "secrets.json" {
		t.Fatalf("unexpected secret path: %q", secretPath)
	}
}

func TestLoadRejectsFutureSchemaWithoutRecovery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	original := []byte(fmt.Sprintf(`{"schemaVersion":%d,"bind":"127.0.0.1:9393","logLevel":"info"}`, CurrentSchemaVersion+1))
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected future schema error")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(original) {
		t.Fatalf("future schema file was modified: %s", got)
	}
	if _, err := os.Stat(path + CorruptConfigSuffix); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("future schema was quarantined: %v", err)
	}
}

func TestDefaultCachePathRejectsTraversal(t *testing.T) {
	for _, name := range []string{"..", ".", "../escape.json", `..\escape.json`, "nested/cache.json"} {
		if _, err := DefaultCachePath(name); err == nil {
			t.Fatalf("DefaultCachePath accepted traversal name %q", name)
		}
	}
}

func TestSecretStoreReadsReadOnlyFallback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.json")
	if err := os.WriteFile(path, []byte(`{"schemaVersion":1,"secrets":{"FMR_READ_ONLY":"read-only-value"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatal(err)
	}
	store := NewSecretStoreWithBackend(path, nil)
	if got, err := store.Get("FMR_READ_ONLY"); err != nil || got != "read-only-value" {
		t.Fatalf("read-only fallback: got %q, err %v", got, err)
	}
}

func TestSecretStoreFallsBackWhenCredentialWriteFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.json")
	backend := &fakeCredentialBackend{setErr: errors.New("credential backend unavailable")}
	store := NewSecretStoreWithBackend(path, backend)
	if err := store.Set("FMR_WRITE_FALLBACK", "file-value"); err != nil {
		t.Fatalf("Set with unavailable backend: %v", err)
	}
	if got, err := store.Get("FMR_WRITE_FALLBACK"); err != nil || got != "file-value" {
		t.Fatalf("fallback after backend write failure: got %q, err %v", got, err)
	}
}

func TestSecretStoreConcurrentOperations(t *testing.T) {
	store := NewSecretStoreWithBackend(filepath.Join(t.TempDir(), "secrets.json"), nil)
	const workers = 8
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func(i int) {
			defer wg.Done()
			key := fmt.Sprintf("FMR_CONCURRENT_%d", i)
			value := fmt.Sprintf("value-%d", i)
			if err := store.Set(key, value); err != nil {
				t.Errorf("Set(%s): %v", key, err)
				return
			}
			if got, err := store.Get(key); err != nil || got != value {
				t.Errorf("Get(%s): got %q, err %v", key, got, err)
			}
		}(i)
	}
	wg.Wait()
}
