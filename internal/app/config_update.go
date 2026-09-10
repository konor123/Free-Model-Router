package app

import (
	"errors"
	"fmt"
	"strings"

	"github.com/konor123/Free-Model-Router/internal/config"
	"github.com/konor123/Free-Model-Router/internal/control"
)

func updateConfig(cfg *config.Config, secrets *config.SecretStore, request control.ConfigUpdateRequest) (control.ConfigUpdateResponse, error) {
	if cfg == nil || secrets == nil {
		return control.ConfigUpdateResponse{}, errors.New("config storage is unavailable")
	}
	if request.Revision != cfg.ConfigRevision {
		return control.ConfigUpdateResponse{}, control.ErrConfigRevisionConflict
	}
	candidate := cfg.Clone()
	candidate.Bind = strings.TrimSpace(request.Bind)
	candidate.ConfigRevision++
	existing := make(map[string]config.ProviderConfig, len(cfg.Providers))
	for _, item := range cfg.Providers {
		existing[item.ID] = item
	}
	stale := make([]string, 0)
	staged := make(map[string]string)
	candidate.Providers = make([]config.ProviderConfig, 0, len(request.Providers))
	for _, item := range request.Providers {
		providerConfig := config.ProviderConfig{ID: item.ID, Name: item.Name, Protocol: item.Protocol, BaseURL: item.BaseURL, Enabled: item.Enabled}
		if old, ok := existing[item.ID]; ok {
			providerConfig.CredentialRef = old.CredentialRef
			providerConfig.AutoProbe = old.AutoProbe
			providerConfig.ExcludedModelIDs = append([]string(nil), old.ExcludedModelIDs...)
			if old.BaseURL != item.BaseURL && old.CredentialRef != "" && item.APIKey == nil {
				return control.ConfigUpdateResponse{}, fmt.Errorf("provider %q endpoint changed; replace or clear its credential explicitly", item.ID)
			}
		}
		if item.AutoProbe != nil {
			value := *item.AutoProbe
			providerConfig.AutoProbe = &value
		} else if _, ok := existing[item.ID]; !ok {
			value := true
			providerConfig.AutoProbe = &value
		}
		if item.ExcludedModelIDs != nil {
			providerConfig.ExcludedModelIDs = append([]string(nil), (*item.ExcludedModelIDs)...)
		}
		if item.APIKey != nil {
			if providerConfig.CredentialRef != "" {
				stale = append(stale, providerConfig.CredentialRef)
			}
			providerConfig.CredentialRef = ""
			if value := strings.TrimSpace(*item.APIKey); value != "" {
				providerConfig.CredentialRef = fmt.Sprintf("provider.%s.%d", item.ID, candidate.ConfigRevision)
				staged[providerConfig.CredentialRef] = value
			}
		}
		candidate.Providers = append(candidate.Providers, providerConfig)
	}
	for id, old := range existing {
		found := false
		for _, item := range candidate.Providers {
			if item.ID == id {
				found = true
				break
			}
		}
		if !found && old.CredentialRef != "" {
			stale = append(stale, old.CredentialRef)
		}
	}
	enabled := 0
	for _, item := range candidate.Providers {
		if item.Enabled {
			enabled++
		}
	}
	if enabled == 0 {
		return control.ConfigUpdateResponse{}, errors.New("at least one provider must be enabled")
	}
	if err := candidate.Validate(); err != nil {
		return control.ConfigUpdateResponse{}, err
	}
	written := make([]string, 0, len(staged))
	for ref, value := range staged {
		if err := secrets.Set(ref, value); err != nil {
			return control.ConfigUpdateResponse{}, withCleanupError(fmt.Errorf("store provider credential: %w", err), cleanupStagedSecrets(secrets, written))
		}
		written = append(written, ref)
	}
	if candidate.SourcePath != "" {
		if err := config.Save(candidate.SourcePath, candidate); err != nil {
			return control.ConfigUpdateResponse{}, withCleanupError(err, cleanupStagedSecrets(secrets, written))
		}
	}
	generatedKey := ""
	if request.GenerateInferenceKey {
		request.InferenceKey = new(string)
		*request.InferenceKey = control.NewToken()
		generatedKey = *request.InferenceKey
	}
	if request.InferenceKey != nil {
		value := strings.TrimSpace(*request.InferenceKey)
		var secretErr error
		if value == "" {
			secretErr = secrets.DeleteStrict(config.InferenceTokenKey)
		} else {
			secretErr = secrets.Set(config.InferenceTokenKey, value)
		}
		if secretErr != nil {
			if cfg.SourcePath != "" {
				_ = config.Save(cfg.SourcePath, cfg)
			}
			return control.ConfigUpdateResponse{}, withCleanupError(fmt.Errorf("store inference key: %w", secretErr), cleanupStagedSecrets(secrets, written))
		}
	}
	*cfg = *candidate
	cleanupPending := false
	for _, ref := range stale {
		if ref != "" {
			if err := secrets.DeleteStrict(ref); err != nil {
				cleanupPending = true
			}
		}
	}
	return control.ConfigUpdateResponse{Config: configView(cfg, secrets), RestartRequired: true, CredentialCleanupPending: cleanupPending, GeneratedInferenceKey: generatedKey}, nil
}

func cleanupStagedSecrets(secrets *config.SecretStore, refs []string) error {
	var first error
	for _, ref := range refs {
		if err := secrets.DeleteStrict(ref); err != nil && first == nil {
			first = err
		}
	}
	return first
}

func withCleanupError(operationErr, cleanupErr error) error {
	if cleanupErr == nil {
		return operationErr
	}
	return fmt.Errorf("%w; staged credential cleanup failed: %v", operationErr, cleanupErr)
}
