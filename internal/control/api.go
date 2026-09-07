// Package control exposes the authenticated, localhost-only management API.
package control

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/konor123/Free-Model-Router/internal/catalog"
	"github.com/konor123/Free-Model-Router/internal/gateway"
	"github.com/konor123/Free-Model-Router/internal/model"
	"github.com/konor123/Free-Model-Router/internal/usage"
)

const (
	// APIVersion is the management API protocol version. Desktop clients attach
	// only when the major version matches.
	APIVersion = "1"
	// DefaultBuildVersion is used by embedders that do not inject release data.
	DefaultBuildVersion = "dev"
)

var (
	ErrBackendRequired        = errors.New("control backend is required")
	ErrTokenRequired          = errors.New("management token is required")
	ErrConfigRevisionConflict = errors.New("config revision conflict")
)

// Backend is the narrow gateway state and mutation boundary used by Server.
// Implementations must return defensive snapshots.
type Backend interface {
	ControlSnapshot() gateway.ControlSnapshot
	UpdateModelPool(expectedRevision int64, mutation gateway.PoolMutation) (catalog.ModelPoolConfig, error)
	ReplaceModelPool(expectedRevision int64, selected []model.ProviderModelID, mode catalog.PoolMode) (catalog.ModelPoolConfig, error)
	PinModel(expectedRevision int64, id model.ProviderModelID) (catalog.ModelPoolConfig, error)
	AutoSelect(expectedRevision int64) (catalog.ModelPoolConfig, error)
}

// LogReader is the read-only usage log boundary exposed by GET /_fmr/logs.
type LogReader interface {
	List(usage.Query) ([]usage.RequestRecord, error)
}

// EventSubscriber is the redacted live usage stream consumed by a desktop
// usage window. Implementations must never publish request content or secrets.
type EventSubscriber interface {
	Subscribe() (<-chan usage.Event, func())
}

// Options configures the control server.
type Options struct {
	Token        string
	APIVersion   string
	BuildVersion string
	InstanceID   string
	Features     []string
	Logs         LogReader
	Events       EventSubscriber
	Config       func() ConfigResponse
	UpdateConfig func(ConfigUpdateRequest) (ConfigUpdateResponse, error)
	OnChange     func(gateway.ControlSnapshot) error
	Stop         func()
}

// StatusResponse is the desktop attach/version handshake.
type StatusResponse struct {
	APIVersion      string   `json:"apiVersion"`
	BuildVersion    string   `json:"buildVersion"`
	InstanceID      string   `json:"instanceId"`
	Features        []string `json:"features"`
	CatalogRevision int64    `json:"catalogRevision"`
	PoolRevision    int64    `json:"poolRevision"`
	PinnedModel     string   `json:"pinnedModel,omitempty"`
}

// ProviderResponse is a safe provider summary.
type ProviderResponse struct {
	ID      string `json:"id"`
	Models  int    `json:"models"`
	Routes  int    `json:"routes"`
	Enabled bool   `json:"enabled"`
}

// RouteResponse is a safe route detail with no credentials.
type RouteResponse struct {
	ID                   string              `json:"id"`
	ModelID              string              `json:"modelId"`
	Provider             string              `json:"provider"`
	UpstreamModelID      string              `json:"upstreamModelId"`
	CredentialID         string              `json:"credentialId,omitempty"`
	Access               model.AccessClass   `json:"access"`
	Enabled              bool                `json:"enabled"`
	Capabilities         model.Capabilities  `json:"capabilities"`
	Health               RouteHealthResponse `json:"health"`
	TTFTMs               float64             `json:"ttftMs,omitempty"`
	TTFTKnown            bool                `json:"ttftKnown"`
	Performance          float64             `json:"performance,omitempty"`
	EffectivePerformance float64             `json:"effectivePerformance,omitempty"`
	Confidence           float64             `json:"confidence,omitempty"`
	LatencyScore         float64             `json:"latencyScore,omitempty"`
	RoutingScore         float64             `json:"routingScore,omitempty"`
	RoutingScoreKnown    bool                `json:"routingScoreKnown"`
}

// RouteHealthResponse is the JSON-safe health status for one route.
type RouteHealthResponse struct {
	CoolingUntil        string `json:"coolingUntil,omitempty"`
	QuotaExhausted      bool   `json:"quotaExhausted"`
	ConsecutiveFailures int    `json:"consecutiveFailures"`
	Available           bool   `json:"available"`
}

// ModelResponse is a model manager row and its route details.
type ModelResponse struct {
	ID                string             `json:"id"`
	CanonicalKey      string             `json:"canonicalKey,omitempty"`
	DisplayName       string             `json:"displayName"`
	UpstreamID        string             `json:"upstreamId"`
	Capabilities      model.Capabilities `json:"capabilities"`
	Selected          bool               `json:"selected"`
	Pinned            bool               `json:"pinned"`
	RoutingScore      float64            `json:"routingScore,omitempty"`
	RoutingScoreKnown bool               `json:"routingScoreKnown"`
	Routes            []RouteResponse    `json:"routes"`
}

// PoolResponse is the optimistic model-pool state returned by control writes.
type PoolResponse struct {
	Revision                 int64    `json:"revision"`
	Mode                     string   `json:"mode"`
	SelectedProviderModelIDs []string `json:"selectedProviderModelIds"`
	PinnedModel              string   `json:"pinnedModel,omitempty"`
}

// ConfigResponse is deliberately non-secret and safe for CLI/UI display.
type ConfigResponse struct {
	Bind                   string                   `json:"bind"`
	ManagementBind         string                   `json:"managementBind"`
	LogLevel               string                   `json:"logLevel"`
	ModelPool              []string                 `json:"modelPool,omitempty"`
	ModelPoolMode          string                   `json:"modelPoolMode,omitempty"`
	PinnedModel            string                   `json:"pinnedModel,omitempty"`
	Revision               int64                    `json:"revision"`
	Providers              []ProviderConfigResponse `json:"providers,omitempty"`
	InferenceKeyConfigured bool                     `json:"inferenceKeyConfigured"`
}

// ProviderConfigResponse is safe for display and never includes a credential
// value or its backing SecretStore reference.
type ProviderConfigResponse struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Protocol      string `json:"protocol"`
	BaseURL       string `json:"baseUrl"`
	Enabled       bool   `json:"enabled"`
	HasCredential bool   `json:"hasCredential"`
}

// ProviderConfigUpdate carries desired provider state. APIKey is write-only:
// nil keeps the current key, empty clears it, and a value replaces it.
type ProviderConfigUpdate struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Protocol string  `json:"protocol"`
	BaseURL  string  `json:"baseUrl"`
	Enabled  bool    `json:"enabled"`
	APIKey   *string `json:"apiKey,omitempty"`
}

// ConfigUpdateRequest replaces desktop-editable settings optimistically.
type ConfigUpdateRequest struct {
	Revision             int64                  `json:"revision"`
	Bind                 string                 `json:"bind"`
	Providers            []ProviderConfigUpdate `json:"providers"`
	InferenceKey         *string                `json:"inferenceKey,omitempty"`
	GenerateInferenceKey bool                   `json:"generateInferenceKey,omitempty"`
}

type ConfigUpdateResponse struct {
	Config                   ConfigResponse `json:"config"`
	RestartRequired          bool           `json:"restartRequired"`
	CredentialCleanupPending bool           `json:"credentialCleanupPending,omitempty"`
	GeneratedInferenceKey    string         `json:"generatedInferenceKey,omitempty"`
}

// Status returns the configured API/build handshake without making a request.
func (o Options) Status(snapshot gateway.ControlSnapshot) StatusResponse {
	apiVersion := o.APIVersion
	if apiVersion == "" {
		apiVersion = APIVersion
	}
	buildVersion := o.BuildVersion
	if buildVersion == "" {
		buildVersion = DefaultBuildVersion
	}
	features := availableFeatures(o.Features, o.Events)
	return StatusResponse{
		APIVersion:      apiVersion,
		BuildVersion:    buildVersion,
		InstanceID:      o.InstanceID,
		Features:        features,
		CatalogRevision: int64(snapshot.CatalogRevision),
		PoolRevision:    snapshot.Pool.Revision,
		PinnedModel:     string(snapshot.PinnedModel),
	}
}

func availableFeatures(configured []string, events EventSubscriber) []string {
	if len(configured) == 0 {
		configured = []string{"control-api", "model-pool", "usage-logs"}
		if events != nil {
			configured = append(configured, "usage-events")
		}
		return configured
	}
	features := make([]string, 0, len(configured))
	for _, feature := range configured {
		if feature == "usage-events" && events == nil {
			continue
		}
		features = append(features, feature)
	}
	return features
}

// NewInstanceID creates a non-secret process identity for status and desktop
// ownership. It is not used as an authentication credential.
func NewInstanceID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err == nil {
		return "fmr-instance-" + hex.EncodeToString(raw[:])
	}
	return "fmr-instance-unknown"
}

// NewToken creates a high-entropy bearer token for local management or LAN
// inference. Callers must store it through config.SecretStore, never log it.
func NewToken() string {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err == nil {
		return hex.EncodeToString(raw[:])
	}
	return ""
}

// APIMajor returns the major component used by desktop attach compatibility.
func APIMajor(version string) string {
	version = strings.TrimSpace(version)
	if i := strings.IndexByte(version, '.'); i >= 0 {
		return version[:i]
	}
	return version
}

// CompatibleAPIMajor reports whether two API versions can safely attach.
func CompatibleAPIMajor(local, remote string) bool {
	localMajor, remoteMajor := APIMajor(local), APIMajor(remote)
	return localMajor != "" && localMajor == remoteMajor
}

func invalidRequest(format string, args ...any) error {
	return fmt.Errorf(format, args...)
}

var _ http.Handler = (*Server)(nil)
