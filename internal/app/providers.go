package app

import (
	"fmt"
	"strings"

	"github.com/konor123/Free-Model-Router/internal/config"
	"github.com/konor123/Free-Model-Router/internal/model"
	"github.com/konor123/Free-Model-Router/internal/provider"
	"github.com/konor123/Free-Model-Router/internal/providers/openaicompat"
	"github.com/konor123/Free-Model-Router/internal/providers/opencode"
	"github.com/konor123/Free-Model-Router/internal/providers/registry"
)

func configuredProvider(cfg *config.Config, secrets *config.SecretStore) (provider.Provider, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config must not be nil")
	}
	entries := make([]registry.Entry, 0, len(cfg.Providers))
	for _, item := range cfg.Providers {
		if !item.Enabled {
			continue
		}
		if item.Protocol != config.OpenAICompatibleProtocol {
			return nil, fmt.Errorf("provider %q uses unsupported protocol %q", item.ID, item.Protocol)
		}
		if item.ID == "opencode" && strings.TrimRight(item.BaseURL, "/") == opencode.AuthBaseURL {
			credentialRef := item.CredentialRef
			backend := opencode.New(item.BaseURL, opencode.WithCredentialResolver(func() (string, error) {
				if strings.TrimSpace(credentialRef) == "" {
					return opencode.AuthKey(), nil
				}
				if secrets == nil {
					return "", fmt.Errorf("credential store unavailable")
				}
				key, err := secrets.Get(credentialRef)
				if err == config.ErrSecretNotFound {
					return "", fmt.Errorf("credential is not configured")
				}
				return key, err
			}))
			entries = append(entries, registry.Entry{ID: item.ID, Backend: backend, Routes: func(pm model.ProviderModel) ([]model.ProviderRoute, error) {
				return backend.Routes(pm)
			}})
			continue
		}
		credentialRef := item.CredentialRef
		backend, err := openaicompat.New(item.ID, item.BaseURL, func() (string, error) {
			if strings.TrimSpace(credentialRef) == "" {
				return "", nil
			}
			if secrets == nil {
				return "", fmt.Errorf("credential store unavailable")
			}
			key, err := secrets.Get(credentialRef)
			if err == config.ErrSecretNotFound {
				return "", fmt.Errorf("credential %q is not configured", credentialRef)
			}
			return key, err
		})
		if err != nil {
			return nil, fmt.Errorf("configure provider %q: %w", item.ID, err)
		}
		entries = append(entries, registry.Entry{ID: item.ID, Backend: backend, Routes: genericRoutes(item.ID)})
	}
	return registry.New(entries)
}

func genericRoutes(id string) registry.RouteBuilder {
	return func(pm model.ProviderModel) ([]model.ProviderRoute, error) {
		routeID, err := model.NewRouteID(id, pm.UpstreamID)
		if err != nil {
			return nil, err
		}
		return []model.ProviderRoute{{ID: routeID, ModelID: pm.ID, Provider: id, UpstreamModelID: pm.UpstreamID, CredentialID: id, Access: model.AccessUnknown, Enabled: true, CapabilityOverride: &model.Capabilities{Streaming: true, Tools: true, StructuredOutput: true, Vision: true, Reasoning: true}}}, nil
	}
}
