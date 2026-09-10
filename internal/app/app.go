// Package app wires config, logging, and (later) gateway components.
package app

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/konor123/Free-Model-Router/internal/config"
	"github.com/konor123/Free-Model-Router/internal/control"
	"github.com/konor123/Free-Model-Router/internal/gateway"
	"github.com/konor123/Free-Model-Router/internal/logging"
	"github.com/konor123/Free-Model-Router/internal/model"
	"github.com/konor123/Free-Model-Router/internal/provider"
	"github.com/konor123/Free-Model-Router/internal/scoring"
	"github.com/konor123/Free-Model-Router/internal/security"
	"github.com/konor123/Free-Model-Router/internal/usage"
)

// BuildVersion is overridden by release builds through linker flags.
var BuildVersion = "dev"

// LoadConfig loads the gateway configuration from path, or the OS default
// location when path is empty.
func LoadConfig(path string) (*config.Config, error) {
	if path == "" {
		var err error
		path, err = config.DefaultPath()
		if err != nil {
			return nil, fmt.Errorf("resolve default config path: %w", err)
		}
	}
	return config.Load(path)
}

// NewLogger builds the process logger from configuration.
func NewLogger(cfg *config.Config) (*logging.Logger, error) {
	min, err := logging.ParseLevel(cfg.LogLevel)
	if err != nil {
		return nil, err
	}
	return logging.New(min, os.Stderr)
}

// Run boots the gateway HTTP server and blocks until ctx is canceled.
func Run(ctx context.Context, cfg *config.Config, log *logging.Logger) error {
	prov, err := configuredProvider(cfg, config.NewSecretStore())
	if err != nil {
		return err
	}
	return RunWithProvider(ctx, cfg, log, prov)
}

// RunWithProvider boots the same application wiring as Run with an injected
// provider. The injection keeps production defaults unchanged while allowing
// an actual HTTP app-to-provider integration test to use a local mock server.
func RunWithProvider(ctx context.Context, cfg *config.Config, log *logging.Logger, prov provider.Provider) error {
	if prov == nil {
		return errors.New("provider must not be nil")
	}
	if cfg == nil {
		return errors.New("config must not be nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if log == nil {
		return errors.New("logger must not be nil")
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	defaults := config.PersistedDefaults()
	inferenceBind := cfg.Bind
	if inferenceBind == "" {
		inferenceBind = defaults.Bind
	}
	if envBind := strings.TrimSpace(os.Getenv("FMR_BIND")); envBind != "" {
		inferenceBind = envBind
	}
	log.Info("Free-Model-Router starting (phase 2)")
	log.Info("config: %s", cfg.SourcePath)
	log.Info("log level: %s", cfg.LogLevel)
	log.Info("gateway bind: %s", inferenceBind)

	gw, err := gateway.NewGateway(runCtx, prov)
	if err != nil {
		return fmt.Errorf("init gateway: %w", err)
	}
	if err := gw.RestoreControlState(toProviderModelIDs(cfg.ModelPool), cfg.ModelPoolMode, providerModelID(cfg.PinnedModel)); err != nil {
		return fmt.Errorf("restore control state: %w", err)
	}
	usageStore := usage.NewMemory()
	if cfg.SourcePath != "" {
		if path, pathErr := config.DefaultUsagePath(); pathErr == nil {
			if persisted, storeErr := usage.New(path); storeErr == nil {
				usageStore = persisted
			}
		}
	}
	gw.SetUsageSink(usageStore)
	gw.SetUsageEventPublisher(usageStore)
	defer usageStore.Close()
	if benchmarkPath, pathErr := config.DefaultCachePath("openevals-benchmark.json"); pathErr == nil {
		startBenchmarkRefresh(runCtx, gw, scoring.NewCache(benchmarkPath, scoring.DefaultCacheMaxAge), log)
	} else {
		log.Warn("resolve OpenEvals cache path: %v", pathErr)
	}

	managementBind := strings.TrimSpace(cfg.ManagementBind)
	controlEnabled := managementBind != "" || cfg.SourcePath != ""
	if managementBind == "" && controlEnabled {
		managementBind = defaults.ManagementBind
	}
	var controlListener net.Listener
	var controlServer *http.Server
	var configMu sync.RWMutex
	if controlEnabled {
		if err := security.ValidateLoopbackBind(managementBind); err != nil {
			return fmt.Errorf("validate management bind: %w", err)
		}
		secretStore := config.NewSecretStore()
		managementToken, tokenErr := resolveToken(secretStore, config.ManagementTokenKey, cfg.SourcePath != "")
		if tokenErr != nil {
			return fmt.Errorf("resolve management token: %w", tokenErr)
		}
		controlOptions := control.Options{
			Token: managementToken, BuildVersion: BuildVersion,
			InstanceID: control.NewInstanceID(), Logs: usageStore, Events: usageStore,
			Config: func() control.ConfigResponse {
				configMu.RLock()
				defer configMu.RUnlock()
				return configView(cfg, secretStore)
			},
			UpdateConfig: func(request control.ConfigUpdateRequest) (control.ConfigUpdateResponse, error) {
				configMu.Lock()
				defer configMu.Unlock()
				return updateConfig(cfg, secretStore, request)
			},
			OnChange: func(snapshot gateway.ControlSnapshot) error {
				configMu.Lock()
				defer configMu.Unlock()
				return persistControlState(cfg, snapshot)
			},
			Stop: cancel,
		}
		controlHandler, controlErr := control.NewServer(gw, controlOptions)
		if controlErr != nil {
			return fmt.Errorf("init control API: %w", controlErr)
		}
		controlListener, err = net.Listen("tcp", managementBind)
		if err != nil {
			return fmt.Errorf("listen on management %s: %w", managementBind, err)
		}
		controlServer = &http.Server{Addr: managementBind, Handler: controlHandler}
		log.Info("management API listening on http://%s", managementBind)
	}

	// A non-empty placeholder validates the bind shape before any credential is
	// resolved. The real token is installed immediately below for wildcard/LAN
	// listeners, while loopback keeps the handler unwrapped.
	inferenceHandler, authErr := security.RequireInferenceAuth(gw.Handler(), inferenceBind, "configured")
	if !security.IsLoopbackBind(inferenceBind) {
		secretStore := config.NewSecretStore()
		inferenceToken, tokenErr := resolveToken(secretStore, config.InferenceTokenKey, cfg.SourcePath != "")
		if tokenErr != nil {
			if controlListener != nil {
				_ = controlListener.Close()
			}
			return fmt.Errorf("resolve inference token: %w", tokenErr)
		}
		inferenceHandler, authErr = security.RequireInferenceAuth(gw.Handler(), inferenceBind, inferenceToken)
	}
	if authErr != nil {
		if controlListener != nil {
			_ = controlListener.Close()
		}
		return fmt.Errorf("configure inference authentication: %w", authErr)
	}

	gw.StartProbes(runCtx)
	defer gw.StopProbes()

	listener, err := net.Listen("tcp", inferenceBind)
	if err != nil {
		if controlListener != nil {
			_ = controlListener.Close()
		}
		return fmt.Errorf("listen on %s: %w", inferenceBind, err)
	}
	defer listener.Close()
	srv := &http.Server{Addr: inferenceBind, Handler: inferenceHandler}
	errCh := make(chan error, 2)
	go func() {
		if err := srv.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()
	if controlServer != nil {
		go func() {
			if err := controlServer.Serve(controlListener); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errCh <- err
			}
		}()
	}
	log.Info("listening on http://%s", inferenceBind)

	select {
	case err := <-errCh:
		cancel()
		if controlServer != nil {
			_ = controlServer.Close()
		}
		return err
	case <-runCtx.Done():
	}
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Warn("shutdown: %v", err)
	}
	if controlServer != nil {
		if err := controlServer.Shutdown(shutdownCtx); err != nil {
			log.Warn("management shutdown: %v", err)
		}
	}
	return nil
}

func resolveToken(store *config.SecretStore, key string, persist bool) (string, error) {
	if store == nil {
		return "", errors.New("secret store must not be nil")
	}
	if token, err := store.Get(key); err == nil && strings.TrimSpace(token) != "" {
		return token, nil
	}
	token := control.NewToken()
	if token == "" {
		return "", errors.New("generate token")
	}
	if persist {
		if err := store.Set(key, token); err != nil {
			return "", err
		}
	}
	return token, nil
}

func toProviderModelIDs(ids []string) []model.ProviderModelID {
	converted := make([]model.ProviderModelID, 0, len(ids))
	for _, id := range ids {
		converted = append(converted, model.ProviderModelID(id))
	}
	return converted
}

func providerModelID(id string) model.ProviderModelID { return model.ProviderModelID(id) }

func persistControlState(cfg *config.Config, snapshot gateway.ControlSnapshot) error {
	if cfg == nil {
		return errors.New("config must not be nil")
	}
	cfg.ModelPool = make([]string, 0, len(snapshot.Pool.SelectedProviderModelIDs))
	for _, id := range snapshot.Pool.SelectedProviderModelIDs {
		cfg.ModelPool = append(cfg.ModelPool, string(id))
	}
	cfg.ModelPoolMode = string(snapshot.Pool.Mode)
	cfg.PinnedModel = string(snapshot.PinnedModel)
	if cfg.SourcePath == "" {
		return nil
	}
	return config.Save(cfg.SourcePath, cfg)
}

func configView(cfg *config.Config, secrets *config.SecretStore) control.ConfigResponse {
	if cfg == nil {
		return control.ConfigResponse{}
	}
	response := control.ConfigResponse{
		Bind: cfg.Bind, ManagementBind: cfg.ManagementBind, LogLevel: cfg.LogLevel,
		ModelPool: append([]string(nil), cfg.ModelPool...), ModelPoolMode: cfg.ModelPoolMode,
		PinnedModel: cfg.PinnedModel, Revision: cfg.ConfigRevision,
	}
	for _, item := range cfg.Providers {
		response.Providers = append(response.Providers, control.ProviderConfigResponse{ID: item.ID, Name: item.Name, Protocol: item.Protocol, BaseURL: item.BaseURL, Enabled: item.Enabled, HasCredential: item.CredentialRef != "", AutoProbe: item.AutoProbeEnabled(), ExcludedModelIDs: append([]string(nil), item.ExcludedModelIDs...)})
	}
	if secrets != nil {
		if key, err := secrets.Get(config.InferenceTokenKey); err == nil && strings.TrimSpace(key) != "" {
			response.InferenceKeyConfigured = true
		}
	}
	return response
}
