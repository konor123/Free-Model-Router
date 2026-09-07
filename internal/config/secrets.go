package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const (
	secretSchemaVersion = 1
	maxSecretFileBytes  = 1 << 20

	// ManagementTokenKey is the local bearer credential for /_fmr endpoints.
	ManagementTokenKey = "FMR_MANAGEMENT_TOKEN"
	// InferenceTokenKey is the bearer credential required when inference binds
	// beyond loopback.
	InferenceTokenKey = "FMR_INFERENCE_TOKEN"
)

var (
	// ErrSecretNotFound indicates that no usable value exists in any source.
	ErrSecretNotFound = errors.New("secret not found")

	// ErrCredentialStoreUnavailable indicates that the OS credential backend is
	// not installed, cannot be reached, or rejected the operation.
	ErrCredentialStoreUnavailable = errors.New("OS credential store unavailable")

	// ErrSecretStoreCorrupt indicates malformed protected-file state. The raw
	// decoder error is intentionally not returned so secret material cannot leak
	// through an error string.
	ErrSecretStoreCorrupt = errors.New("secret store is corrupt")
)

// CredentialBackend is the small adapter required from an OS credential store.
// Implementations must keep values out of errors and logs.
type CredentialBackend interface {
	Get(key string) (string, error)
	Set(key, value string) error
	Delete(key string) error
}

// SecretBackend is a descriptive compatibility alias.
type SecretBackend = CredentialBackend

// SecretStore resolves secrets in this order:
// environment override, OS credential store, protected user-only file.
// Config, cache, runtime identity, and usage logs are intentionally separate.
type SecretStore struct {
	mu         sync.Mutex
	envLookup  func(string) (string, bool)
	credential CredentialBackend
	filePath   string
}

// NewSecretStore creates a SecretStore. An optional path is accepted for tests
// and embedders; without one, the OS-specific protected config directory is
// used. The OS credential adapter is best effort and the file remains the
// fallback when that adapter is unavailable.
func NewSecretStore(paths ...string) *SecretStore {
	path := ""
	if len(paths) > 0 {
		path = strings.TrimSpace(paths[0])
	}
	if path == "" {
		path, _ = DefaultSecretPath()
	}
	return &SecretStore{
		envLookup:  os.LookupEnv,
		credential: newPlatformCredentialBackend(),
		filePath:   path,
	}
}

// NewSecretStoreWithBackend creates a store with an injected credential
// backend. It is useful for deterministic tests and platform integrations.
func NewSecretStoreWithBackend(path string, backend CredentialBackend) *SecretStore {
	store := NewSecretStore(path)
	store.credential = backend
	return store
}

// NewSecretStoreWithCredentialBackend is an argument-order compatibility
// helper for callers that naturally provide the backend first.
func NewSecretStoreWithCredentialBackend(backend CredentialBackend, path string) *SecretStore {
	return NewSecretStoreWithBackend(path, backend)
}

// DefaultSecretPath returns the separate protected-file fallback path.
func DefaultSecretPath() (string, error) {
	dir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "secrets.json"), nil
}

// FilePath returns the configured fallback path without exposing any secret.
func (s *SecretStore) FilePath() string {
	if s == nil {
		return ""
	}
	return s.filePath
}

// Get resolves a secret without logging or returning source-specific errors.
// Empty environment values are treated as unset, matching provider fail-closed
// behavior while still allowing a credential/file value to be used.
func (s *SecretStore) Get(key string) (string, error) {
	if s == nil {
		return "", ErrSecretNotFound
	}
	key, err := normalizeSecretKey(key)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.envLookup != nil {
		if value, ok := s.envLookup(key); ok {
			if value = strings.TrimSpace(value); value != "" {
				return value, nil
			}
		}
	}
	if s.credential != nil {
		if value, backendErr := s.credential.Get(key); backendErr == nil {
			if value = strings.TrimSpace(value); value != "" {
				return value, nil
			}
		}
	}
	return readSecretFile(s.filePath, key)
}

// GetSecret is a descriptive alias for Get.
func (s *SecretStore) GetSecret(key string) (string, error) { return s.Get(key) }

// Set stores a secret in the OS credential store when available and otherwise
// atomically updates the protected user-only file. Environment overrides are
// read-only and are never copied into either persistent store.
func (s *SecretStore) Set(key, value string) error {
	if s == nil {
		return ErrCredentialStoreUnavailable
	}
	key, err := normalizeSecretKey(key)
	if err != nil {
		return err
	}
	value = strings.TrimSpace(value)
	if value == "" || strings.IndexByte(value, 0) >= 0 {
		return errors.New("secret value must not be empty")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.credential != nil {
		if backendErr := s.credential.Set(key, value); backendErr == nil {
			// A previous file fallback may contain stale material. Cleanup is
			// best-effort because the OS store is now authoritative.
			_ = deleteSecretFromFile(s.filePath, key)
			return nil
		}
	}
	return writeSecretToFile(s.filePath, key, value)
}

// SetSecret is a descriptive alias for Set.
func (s *SecretStore) SetSecret(key, value string) error { return s.Set(key, value) }

// Delete removes a secret from both persistent sources. Environment variables
// are controlled by the process environment and are intentionally untouched.
func (s *SecretStore) Delete(key string) error {
	if s == nil {
		return nil
	}
	key, err := normalizeSecretKey(key)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.credential != nil {
		// Not-found and backend failures are ignored so a stale fallback can
		// still be removed and deletion remains idempotent.
		_ = s.credential.Delete(key)
	}
	return deleteSecretFromFile(s.filePath, key)
}

// DeleteSecret is a descriptive alias for Delete.
func (s *SecretStore) DeleteSecret(key string) error { return s.Delete(key) }

// DeleteStrict removes a secret from both stores and reports native credential
// backend failures. Configuration mutation transactions use it when silently
// retaining stale credential material would be misleading.
func (s *SecretStore) DeleteStrict(key string) error {
	if s == nil {
		return ErrCredentialStoreUnavailable
	}
	key, err := normalizeSecretKey(key)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var backendErr error
	if s.credential != nil {
		backendErr = s.credential.Delete(key)
	}
	fileErr := deleteSecretFromFile(s.filePath, key)
	if backendErr != nil {
		return backendErr
	}
	return fileErr
}

type secretFile struct {
	SchemaVersion int               `json:"schemaVersion"`
	Secrets       map[string]string `json:"secrets"`
}

func normalizeSecretKey(key string) (string, error) {
	key = strings.TrimSpace(key)
	if key == "" || strings.IndexByte(key, 0) >= 0 {
		return "", errors.New("secret key must not be empty")
	}
	return key, nil
}

func readSecretFile(path, key string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", ErrSecretNotFound
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", ErrSecretNotFound
		}
		return "", ErrCredentialStoreUnavailable
	}
	if len(data) > maxSecretFileBytes {
		return "", ErrSecretStoreCorrupt
	}
	var file secretFile
	if err := json.Unmarshal(data, &file); err != nil || file.SchemaVersion > secretSchemaVersion || file.Secrets == nil {
		return "", ErrSecretStoreCorrupt
	}
	value := strings.TrimSpace(file.Secrets[key])
	if value == "" {
		return "", ErrSecretNotFound
	}
	return value, nil
}

func writeSecretToFile(path, key, value string) error {
	if strings.TrimSpace(path) == "" {
		return ErrCredentialStoreUnavailable
	}
	file := secretFile{SchemaVersion: secretSchemaVersion, Secrets: map[string]string{}}
	if data, err := os.ReadFile(path); err == nil {
		if len(data) > maxSecretFileBytes {
			return ErrSecretStoreCorrupt
		}
		if err := json.Unmarshal(data, &file); err != nil || file.SchemaVersion > secretSchemaVersion || file.Secrets == nil {
			return ErrSecretStoreCorrupt
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return ErrCredentialStoreUnavailable
	}
	if file.SchemaVersion == 0 {
		file.SchemaVersion = secretSchemaVersion
	}
	if file.Secrets == nil {
		file.Secrets = map[string]string{}
	}
	file.Secrets[key] = value
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return ErrCredentialStoreUnavailable
	}
	if err := atomicWriteFile(path, data, 0o600, ".fmr-secrets-*.tmp"); err != nil {
		return fmt.Errorf("save secret store: %w", err)
	}
	return nil
}

func deleteSecretFromFile(path, key string) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return ErrCredentialStoreUnavailable
	}
	if len(data) > maxSecretFileBytes {
		return ErrSecretStoreCorrupt
	}
	var file secretFile
	if err := json.Unmarshal(data, &file); err != nil || file.SchemaVersion > secretSchemaVersion || file.Secrets == nil {
		return ErrSecretStoreCorrupt
	}
	if _, exists := file.Secrets[key]; !exists {
		return nil
	}
	delete(file.Secrets, key)
	if len(file.Secrets) == 0 {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return ErrCredentialStoreUnavailable
		}
		return nil
	}
	file.SchemaVersion = secretSchemaVersion
	encoded, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return ErrCredentialStoreUnavailable
	}
	if err := atomicWriteFile(path, encoded, 0o600, ".fmr-secrets-*.tmp"); err != nil {
		return ErrCredentialStoreUnavailable
	}
	return nil
}
