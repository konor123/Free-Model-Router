package app

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/konor123/Free-Model-Router/internal/config"
	"github.com/konor123/Free-Model-Router/internal/control"
)

type unavailableCredentials struct{}

func (unavailableCredentials) Get(string) (string, error) {
	return "", config.ErrCredentialStoreUnavailable
}
func (unavailableCredentials) Set(string, string) error { return config.ErrCredentialStoreUnavailable }
func (unavailableCredentials) Delete(string) error      { return config.ErrCredentialStoreUnavailable }

func TestUpdateConfigPersistsSecretReferenceNotValue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	secretPath := filepath.Join(t.TempDir(), "secrets.json")
	secrets := config.NewSecretStoreWithBackend(secretPath, unavailableCredentials{})
	cfg := config.PersistedDefaults()
	cfg.SourcePath = path
	key := "top-secret"
	response, err := updateConfig(cfg, secrets, control.ConfigUpdateRequest{Revision: 0, Bind: "127.0.0.1:9999", Providers: []control.ProviderConfigUpdate{{ID: "local", Name: "Local", Protocol: config.OpenAICompatibleProtocol, BaseURL: "https://example.test/v1", Enabled: true, APIKey: &key}}})
	if err != nil {
		t.Fatal(err)
	}
	if !response.RestartRequired || !response.Config.Providers[0].HasCredential {
		t.Fatalf("response = %#v", response)
	}
	loaded, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Providers[0].CredentialRef == "" || loaded.Providers[0].CredentialRef == key {
		t.Fatalf("credential ref = %q", loaded.Providers[0].CredentialRef)
	}
	if value, err := secrets.Get(loaded.Providers[0].CredentialRef); err != nil || value != key {
		t.Fatalf("secret value=%q err=%v", value, err)
	}
}

func TestUpdateConfigRejectsStaleRevision(t *testing.T) {
	cfg := config.PersistedDefaults()
	cfg.ConfigRevision = 2
	_, err := updateConfig(cfg, config.NewSecretStore(filepath.Join(t.TempDir(), "secrets.json")), control.ConfigUpdateRequest{Revision: 1})
	if !errors.Is(err, control.ErrConfigRevisionConflict) {
		t.Fatalf("error = %v", err)
	}
}

func TestUpdateConfigRequiresCredentialDecisionWhenEndpointChanges(t *testing.T) {
	cfg := config.PersistedDefaults()
	cfg.Providers = []config.ProviderConfig{{ID: "local", Name: "Local", Protocol: config.OpenAICompatibleProtocol, BaseURL: "https://old.example/v1", Enabled: true, CredentialRef: "provider.local.1"}}
	_, err := updateConfig(cfg, config.NewSecretStore(filepath.Join(t.TempDir(), "secrets.json")), control.ConfigUpdateRequest{Revision: 0, Bind: cfg.Bind, Providers: []control.ProviderConfigUpdate{{ID: "local", Name: "Local", Protocol: config.OpenAICompatibleProtocol, BaseURL: "https://new.example/v1", Enabled: true}}})
	if err == nil {
		t.Fatal("endpoint change reused an existing credential")
	}
}

func TestUpdateConfigValidatesBeforeWritingCredential(t *testing.T) {
	secrets := config.NewSecretStoreWithBackend(filepath.Join(t.TempDir(), "secrets.json"), unavailableCredentials{})
	cfg := config.PersistedDefaults()
	key := "must-not-be-written"
	_, err := updateConfig(cfg, secrets, control.ConfigUpdateRequest{Revision: 0, Bind: cfg.Bind, Providers: []control.ProviderConfigUpdate{{ID: "bad id", Name: "Bad", Protocol: config.OpenAICompatibleProtocol, BaseURL: "https://example.test/v1", Enabled: true, APIKey: &key}}})
	if err == nil {
		t.Fatal("accepted invalid provider")
	}
	if _, err := secrets.Get("provider.bad id.1"); err == nil {
		t.Fatal("wrote credential before validation")
	}
}

func TestUpdateConfigCleansStagedCredentialWhenSaveFails(t *testing.T) {
	secretPath := filepath.Join(t.TempDir(), "secrets.json")
	secrets := config.NewSecretStoreWithBackend(secretPath, unavailableCredentials{})
	cfg := config.PersistedDefaults()
	cfg.SourcePath = filepath.Join(t.TempDir(), "bad\x00.json")
	key := "staged"
	_, err := updateConfig(cfg, secrets, control.ConfigUpdateRequest{Revision: 0, Bind: cfg.Bind, Providers: []control.ProviderConfigUpdate{{ID: "local", Name: "Local", Protocol: config.OpenAICompatibleProtocol, BaseURL: "https://example.test/v1", Enabled: true, APIKey: &key}}})
	if err == nil {
		t.Fatal("expected save failure")
	}
	if _, err := secrets.Get("provider.local.1"); err == nil {
		t.Fatal("staged credential survived rollback")
	}
}

func TestUpdateConfigReturnsGeneratedInferenceKeyOnce(t *testing.T) {
	secrets := config.NewSecretStoreWithBackend(filepath.Join(t.TempDir(), "secrets.json"), unavailableCredentials{})
	cfg := config.PersistedDefaults()
	providers := []control.ProviderConfigUpdate{{ID: "opencode", Name: "OpenCode", Protocol: config.OpenAICompatibleProtocol, BaseURL: "https://opencode.ai/zen/v1", Enabled: true}}
	response, err := updateConfig(cfg, secrets, control.ConfigUpdateRequest{Revision: 0, Bind: cfg.Bind, Providers: providers, GenerateInferenceKey: true})
	if err != nil {
		t.Fatal(err)
	}
	if response.GeneratedInferenceKey == "" {
		t.Fatal("generated key was not returned")
	}
	stored, err := secrets.Get(config.InferenceTokenKey)
	if err != nil || stored != response.GeneratedInferenceKey {
		t.Fatalf("stored key mismatch: %v", err)
	}
	if response.Config.InferenceKeyConfigured != true {
		t.Fatal("masked config did not report configured key")
	}
}

func TestUpdateConfigPreservesOmittedGenericPolicyAndClearsExplicitExclusions(t *testing.T) {
	probe := false
	cfg := config.PersistedDefaults()
	cfg.Providers = []config.ProviderConfig{{ID: "local", Name: "Local", Protocol: config.OpenAICompatibleProtocol, BaseURL: "https://example.test/v1", Enabled: true, AutoProbe: &probe, ExcludedModelIDs: []string{"vendor/model"}}}
	secrets := config.NewSecretStore(filepath.Join(t.TempDir(), "secrets.json"))
	request := control.ProviderConfigUpdate{ID: "local", Name: "Local", Protocol: config.OpenAICompatibleProtocol, BaseURL: "https://example.test/v1", Enabled: true}
	response, err := updateConfig(cfg, secrets, control.ConfigUpdateRequest{Revision: 0, Bind: cfg.Bind, Providers: []control.ProviderConfigUpdate{request}})
	if err != nil {
		t.Fatal(err)
	}
	if response.Config.Providers[0].AutoProbe || len(response.Config.Providers[0].ExcludedModelIDs) != 1 {
		t.Fatalf("omitted policy was reset: %#v", response.Config.Providers[0])
	}
	empty := []string{}
	request.ExcludedModelIDs = &empty
	_, err = updateConfig(cfg, secrets, control.ConfigUpdateRequest{Revision: 1, Bind: cfg.Bind, Providers: []control.ProviderConfigUpdate{request}})
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Providers[0].ExcludedModelIDs) != 0 {
		t.Fatalf("explicit empty exclusions were not cleared: %#v", cfg.Providers[0])
	}
}
