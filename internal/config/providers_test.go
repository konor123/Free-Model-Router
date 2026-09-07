package config

import "testing"

func TestPersistedDefaultsIncludeOpenCodeProvider(t *testing.T) {
	cfg := PersistedDefaults()
	if len(cfg.Providers) != 1 || cfg.Providers[0].ID != "opencode" || !cfg.Providers[0].Enabled {
		t.Fatalf("default providers = %#v", cfg.Providers)
	}
}

func TestValidateProvidersRejectsUnsafeEndpoint(t *testing.T) {
	err := validateProviders([]ProviderConfig{{
		ID: "unsafe", Name: "Unsafe", Protocol: OpenAICompatibleProtocol,
		BaseURL: "https://token@example.test/v1?redirect=1", Enabled: true,
	}})
	if err == nil {
		t.Fatal("validateProviders accepted unsafe endpoint")
	}
}

func TestValidateProvidersRequiresHTTPSOutsideLoopback(t *testing.T) {
	err := validateProviders([]ProviderConfig{{ID: "unsafe", Name: "Unsafe", Protocol: OpenAICompatibleProtocol, BaseURL: "http://example.test/v1", Enabled: true}})
	if err == nil {
		t.Fatal("accepted plaintext remote provider endpoint")
	}
	if err := validateProviders([]ProviderConfig{{ID: "local", Name: "Local", Protocol: OpenAICompatibleProtocol, BaseURL: "http://127.0.0.1:11434/v1", Enabled: true}}); err != nil {
		t.Fatalf("rejected loopback endpoint: %v", err)
	}
}

func TestValidateProvidersRejectsCrossPurposeSecretReference(t *testing.T) {
	err := validateProviders([]ProviderConfig{{ID: "local", Name: "Local", Protocol: OpenAICompatibleProtocol, BaseURL: "https://example.test/v1", Enabled: true, CredentialRef: ManagementTokenKey}})
	if err == nil {
		t.Fatal("accepted a non-provider credential reference")
	}
}
