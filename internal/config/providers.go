package config

import (
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
)

const OpenAICompatibleProtocol = "openai-compatible"

var providerIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// ProviderConfig is the non-secret configuration for one upstream provider.
// CredentialRef names a SecretStore entry; credential values never belong here.
type ProviderConfig struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Protocol      string `json:"protocol"`
	BaseURL       string `json:"baseUrl"`
	Enabled       bool   `json:"enabled"`
	CredentialRef string `json:"credentialRef,omitempty"`
}

func defaultProviders() []ProviderConfig {
	return []ProviderConfig{{
		ID:       "opencode",
		Name:     "OpenCode",
		Protocol: OpenAICompatibleProtocol,
		BaseURL:  "https://opencode.ai/zen/v1",
		Enabled:  true,
	}}
}

func cloneProviders(providers []ProviderConfig) []ProviderConfig {
	return append([]ProviderConfig(nil), providers...)
}

func validateProviders(providers []ProviderConfig) error {
	seen := make(map[string]struct{}, len(providers))
	for _, provider := range providers {
		if !providerIDPattern.MatchString(provider.ID) {
			return fmt.Errorf("config: invalid provider id %q", provider.ID)
		}
		if _, ok := seen[provider.ID]; ok {
			return fmt.Errorf("config: duplicate provider id %q", provider.ID)
		}
		seen[provider.ID] = struct{}{}
		if strings.TrimSpace(provider.Name) == "" {
			return fmt.Errorf("config: provider %q name must not be empty", provider.ID)
		}
		if provider.Protocol != OpenAICompatibleProtocol {
			return fmt.Errorf("config: provider %q has unsupported protocol %q", provider.ID, provider.Protocol)
		}
		endpoint, err := url.Parse(provider.BaseURL)
		if err != nil || endpoint.Scheme == "" || endpoint.Host == "" {
			return fmt.Errorf("config: provider %q baseUrl must be an absolute URL", provider.ID)
		}
		if endpoint.User != nil || endpoint.Fragment != "" || endpoint.RawQuery != "" {
			return fmt.Errorf("config: provider %q baseUrl must not contain credentials, query, or fragment", provider.ID)
		}
		if endpoint.Scheme != "https" && endpoint.Scheme != "http" {
			return fmt.Errorf("config: provider %q baseUrl must use http or https", provider.ID)
		}
		if endpoint.Scheme == "http" && !isLoopbackHost(endpoint.Hostname()) {
			return fmt.Errorf("config: provider %q baseUrl must use https unless it targets loopback", provider.ID)
		}
		if provider.CredentialRef != "" {
			prefix := "provider." + provider.ID + "."
			revision := strings.TrimPrefix(provider.CredentialRef, prefix)
			if revision == provider.CredentialRef || revision == "" || strings.Trim(revision, "0123456789") != "" {
				return fmt.Errorf("config: provider %q has an invalid credential reference", provider.ID)
			}
		}
	}
	return nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(strings.TrimSuffix(host, "."), "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
