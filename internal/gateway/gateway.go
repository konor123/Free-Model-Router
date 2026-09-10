// Package gateway exposes the OpenAI-compatible HTTP surface:
// GET /v1/models and POST /v1/chat/completions (PLAN_V7 §6).
package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/konor123/Free-Model-Router/internal/catalog"
	"github.com/konor123/Free-Model-Router/internal/health"
	"github.com/konor123/Free-Model-Router/internal/latency"
	"github.com/konor123/Free-Model-Router/internal/matcher"
	"github.com/konor123/Free-Model-Router/internal/model"
	"github.com/konor123/Free-Model-Router/internal/probe"
	"github.com/konor123/Free-Model-Router/internal/provider"
	"github.com/konor123/Free-Model-Router/internal/providers/opencode"
	"github.com/konor123/Free-Model-Router/internal/scoring"
	"github.com/konor123/Free-Model-Router/internal/usage"
)

// ExternalAutoModel is the external model id meaning "route over the whole pool".
const ExternalAutoModel = "fmr/auto"

// legacyExternalAutoModel remains accepted for clients using the pre-FMR
// identifier, but the legacy value is no longer advertised on the API.
const legacyExternalAutoModel = "afm/auto"

func isAutoModel(id string) bool {
	return id == ExternalAutoModel || id == legacyExternalAutoModel
}

// Gateway wires one provider behind an OpenAI-compatible API.
type Gateway struct {
	Prov provider.Provider

	mu                sync.RWMutex
	catalog           *model.CatalogSnapshot
	routes            map[model.ProviderModelID][]model.ProviderRoute
	providers         []provider.ProviderAvailability
	pool              *catalog.Store
	reconciler        *catalog.Reconciler
	health            *health.Manager
	latency           *latency.Registry
	probes            *probe.Scheduler
	autoPick          model.ProviderModelID
	autoRoute         model.ProviderRoute
	failover          FailoverPolicy
	benchmarkSnapshot scoring.Snapshot
	benchmarkBindings map[model.ProviderModelID]matcher.BenchmarkBinding
	benchmarkStatus   BenchmarkStatus
	usageSink         usage.Sink
	usageEvents       usage.EventPublisher
	pinnedModel       model.ProviderModelID
}

// SetUsageSink installs the append-only request usage sink. A nil sink
// disables durable usage recording without changing request behavior.
func (g *Gateway) SetUsageSink(sink usage.Sink) {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.usageSink = sink
}

// UsageSink returns the currently configured usage sink.
func (g *Gateway) UsageSink() usage.Sink {
	if g == nil {
		return nil
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.usageSink
}

// SetUsageEventPublisher installs the optional redacted live usage publisher.
func (g *Gateway) SetUsageEventPublisher(publisher usage.EventPublisher) {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.usageEvents = publisher
}

func (g *Gateway) publishUsageEvent(event usage.Event) {
	if g == nil {
		return
	}
	g.mu.RLock()
	publisher := g.usageEvents
	g.mu.RUnlock()
	if publisher != nil {
		publisher.Publish(event)
	}
}

func (g *Gateway) recordUsage(record usage.RequestRecord) {
	if sink := g.UsageSink(); sink != nil {
		// Usage logging is observational. A full or unavailable log must never
		// change the client's inference response or fallback behavior.
		_ = sink.Append(record)
	}
}

// NewGateway builds a Gateway and performs the initial catalog discovery.
func NewGateway(ctx context.Context, p provider.Provider) (*Gateway, error) {
	return NewGatewayWithFailoverPolicy(ctx, p, DefaultFailoverPolicy())
}

// NewGatewayWithFailoverPolicy builds a Gateway with explicit request failover
// limits. Production callers normally use NewGateway so environment defaults
// are applied, while tests and embedders can make the limits deterministic.
func NewGatewayWithFailoverPolicy(ctx context.Context, p provider.Provider, policy FailoverPolicy) (*Gateway, error) {
	if p == nil {
		return nil, errors.New("provider must not be nil")
	}
	g := &Gateway{
		Prov:              p,
		pool:              catalog.NewStore(catalog.NewPoolState(nil)),
		reconciler:        catalog.NewReconciler(),
		health:            health.New(),
		latency:           latency.NewRegistry(),
		failover:          policy.normalized(),
		benchmarkBindings: make(map[model.ProviderModelID]matcher.BenchmarkBinding),
		benchmarkStatus:   BenchmarkStatus{State: "never-fetched"},
	}
	g.probes = probe.New(g.latency, g.health, 30*time.Second)
	if err := g.RefreshCatalog(ctx); err != nil {
		return nil, err
	}
	return g, nil
}

// NewGatewayWithPolicy is a concise compatibility alias for callers that do
// not need to spell out the failover-specific constructor name.
func NewGatewayWithPolicy(ctx context.Context, p provider.Provider, policy FailoverPolicy) (*Gateway, error) {
	return NewGatewayWithFailoverPolicy(ctx, p, policy)
}

// RefreshCatalog re-discovers models and repicks fmr/auto target.
func (g *Gateway) RefreshCatalog(ctx context.Context) error {
	var (
		snap      *model.CatalogSnapshot
		routes    map[model.ProviderModelID][]model.ProviderRoute
		providers []provider.ProviderAvailability
		err       error
	)
	if routed, ok := g.Prov.(provider.RoutedCatalogProvider); ok {
		catalog, discoverErr := routed.DiscoverRoutedCatalog(ctx)
		if discoverErr != nil {
			return discoverErr
		}
		if catalog == nil || catalog.Snapshot == nil {
			return errors.New("routed provider returned an empty catalog")
		}
		snap, routes = catalog.Snapshot, catalog.Routes
		providers = append([]provider.ProviderAvailability(nil), catalog.Providers...)
	} else {
		snap, err = g.Prov.DiscoverModels(ctx)
		if err != nil {
			return err
		}
		if snap == nil {
			return errors.New("provider returned an empty catalog")
		}
		routes = make(map[model.ProviderModelID][]model.ProviderRoute, len(snap.Models))
		for id, pm := range snap.Models {
			routes[id] = opencode.Routes(id, pm.UpstreamID, pm.Base)
		}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if err := snap.Validate(); err != nil {
		return fmt.Errorf("catalog snapshot: %w", err)
	}
	candidatePool := g.pool.StateSnapshot()
	var reconcileErr error
	_, reconcileErr = g.reconciler.Reconcile(candidatePool, snap, routes)
	if reconcileErr != nil {
		return reconcileErr
	}
	snap, err = g.pool.CommitSnapshot(snap)
	if err != nil {
		return err
	}
	g.pool.ReplaceState(candidatePool)
	g.catalog, g.routes, g.providers = snap, routes, providers
	g.rebuildBenchmarkBindingsLocked()
	g.autoPick, g.autoRoute = "", model.ProviderRoute{}
	ids := make([]model.ProviderModelID, 0, len(snap.Models))
	for id := range snap.Models {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	if len(ids) > 0 {
		g.autoPick = ids[0]
		if candidates := routes[g.autoPick]; len(candidates) > 0 {
			g.autoRoute = candidates[0]
		}
	}
	return nil
}

// ModelsHandler serves GET /v1/models.
func (g *Gateway) ModelsHandler(w http.ResponseWriter, r *http.Request) {
	g.mu.RLock()
	defer g.mu.RUnlock()

	type modelEntry struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		OwnedBy string `json:"owned_by"`
	}
	out := struct {
		Object string       `json:"object"`
		Data   []modelEntry `json:"data"`
	}{Object: "list"}

	if g.autoPick != "" {
		out.Data = append(out.Data, modelEntry{ID: ExternalAutoModel, Object: "model", OwnedBy: "fmr"})
	}
	if g.catalog != nil {
		for _, id := range g.pool.Snapshot().SelectedProviderModelIDs {
			if _, ok := g.catalog.Models[id]; !ok {
				continue
			}
			out.Data = append(out.Data, modelEntry{ID: string(id), Object: "model", OwnedBy: "fmr"})
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// chatCompletionRequest mirrors the OpenAI wire format.
type chatCompletionRequest struct {
	Model          string             `json:"model"`
	Messages       []provider.Message `json:"messages"`
	Stream         bool               `json:"stream"`
	MaxTokens      int                `json:"max_tokens,omitempty"`
	Temperature    *float64           `json:"temperature,omitempty"`
	Tools          []gatewayTool      `json:"tools,omitempty"`
	ToolChoice     json.RawMessage    `json:"tool_choice,omitempty"`
	ResponseFormat json.RawMessage    `json:"response_format,omitempty"`
	Reasoning      json.RawMessage    `json:"reasoning,omitempty"`
}

// StartProbes starts periodic probing from the gateway's current immutable view.
func (g *Gateway) StartProbes(ctx context.Context) {
	g.probes.Start(ctx, g.currentProbeSnapshot)
}

// StopProbes waits for periodic probes to exit.
func (g *Gateway) StopProbes() { g.probes.Stop() }

// ProbeOnce runs one synchronous probe cycle against the gateway's current
// catalog, pool, and routes. It is useful for deterministic control-plane
// refreshes and integration tests; periodic scheduling uses the same source.
func (g *Gateway) ProbeOnce(ctx context.Context) {
	g.probes.RunOnce(ctx, g.currentProbeSnapshot)
}

func (g *Gateway) currentProbeSnapshot() *probe.Snapshot {
	g.mu.RLock()
	defer g.mu.RUnlock()
	if g.catalog == nil {
		return nil
	}
	routes := make(map[model.ProviderModelID][]model.ProviderRoute, len(g.routes))
	for id, rs := range g.routes {
		copied := make([]model.ProviderRoute, len(rs))
		for i, route := range rs {
			copied[i] = route
			if route.CapabilityOverride != nil {
				caps := *route.CapabilityOverride
				copied[i].CapabilityOverride = &caps
			}
		}
		routes[id] = copied
	}
	return &probe.Snapshot{
		Catalog:  g.catalog.Clone(),
		Pool:     append([]model.ProviderModelID(nil), g.pool.Snapshot().SelectedProviderModelIDs...),
		Routes:   routes,
		Provider: g.Prov,
	}
}

type gatewayTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description,omitempty"`
		Parameters  json.RawMessage `json:"parameters,omitempty"`
	} `json:"function"`
}

type chatCompletionChunk struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	Model   string `json:"model"`
	Choices []struct {
		Index int `json:"index"`
		Delta struct {
			Content          string            `json:"content,omitempty"`
			ToolCalls        []json.RawMessage `json:"tool_calls,omitempty"`
			ReasoningContent string            `json:"reasoning_content,omitempty"`
		} `json:"delta"`
		FinishReason any `json:"finish_reason"`
	} `json:"choices"`
}

type chatCompletionResponse struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	Model   string `json:"model"`
	Choices []struct {
		Index   int `json:"index"`
		Message struct {
			Role      string            `json:"role"`
			Content   string            `json:"content"`
			ToolCalls []json.RawMessage `json:"tool_calls,omitempty"`
			Reasoning string            `json:"reasoning_content,omitempty"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
}

func durationMilliseconds(d time.Duration) float64 {
	return float64(d) / float64(time.Millisecond)
}

func shouldRecordRouteFailure(ctx context.Context, err error) bool {
	if isRequestCanceled(ctx, err) {
		return false
	}
	var unsupported *provider.UnsupportedError
	if errors.As(err, &unsupported) {
		return false
	}
	var failureErr *provider.FailureError
	if errors.As(err, &failureErr) && failureErr.Failure.Scope == model.ScopeRequest {
		return false
	}
	return true
}

func isRequestCanceled(ctx context.Context, err error) bool {
	if ctx.Err() != nil || errors.Is(err, context.Canceled) {
		return true
	}
	// A provider-local deadline is retryable while the client request is still
	// alive. A request deadline is already covered by ctx.Err above.
	if errors.Is(err, context.DeadlineExceeded) {
		return ctx.Err() != nil
	}
	var failureErr *provider.FailureError
	return errors.As(err, &failureErr) && failureErr.Failure.Class == model.FailureCanceled
}

func writeSSEError(w http.ResponseWriter, err error) {
	payload, marshalErr := json.Marshal(map[string]any{"error": map[string]string{"message": err.Error()}})
	if marshalErr != nil {
		return
	}
	fmt.Fprintf(w, "data: %s\n\n", payload)
}

func (g *Gateway) resolveCandidate(req chatCompletionRequest) (model.ProviderModelID, model.ProviderRoute, bool) {
	candidates := g.resolveAttemptCandidates(req)
	if len(candidates) == 0 {
		return "", model.ProviderRoute{}, false
	}
	return candidates[0].Model.ID, candidates[0].Route, true
}

func (g *Gateway) writeUpstreamError(w http.ResponseWriter, err error) {
	var unsupported *provider.UnsupportedError
	if errors.As(err, &unsupported) {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	var fe *provider.FailureError
	if errors.As(err, &fe) {
		if fe.Failure.Class == model.FailureCanceled {
			// Client is gone; nothing to write.
			return
		}
		writeError(w, model.HTTPStatus(fe.Failure), err.Error())
		return
	}
	writeError(w, http.StatusBadGateway, err.Error())
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]any{
		"error": map[string]any{"message": msg, "type": "fmr_error"},
	})
}

// Handler builds the full HTTP mux.
func (g *Gateway) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/models", g.ModelsHandler)
	mux.HandleFunc("POST /v1/chat/completions", g.ChatHandler)
	return mux
}
