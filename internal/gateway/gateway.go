// Package gateway exposes the OpenAI-compatible HTTP surface:
// GET /v1/models and POST /v1/chat/completions (PLAN_V7 §6).
package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/konor123/Free-Model-Router/internal/catalog"
	"github.com/konor123/Free-Model-Router/internal/health"
	"github.com/konor123/Free-Model-Router/internal/latency"
	"github.com/konor123/Free-Model-Router/internal/model"
	"github.com/konor123/Free-Model-Router/internal/probe"
	"github.com/konor123/Free-Model-Router/internal/provider"
	"github.com/konor123/Free-Model-Router/internal/providers/opencode"
	"github.com/konor123/Free-Model-Router/internal/router"
)

// ExternalAutoModel is the external model id meaning "route over the whole pool".
// Phase 2 resolves afm/auto to the single discovered OpenCode model.
const ExternalAutoModel = "afm/auto"

// Gateway wires one provider behind an OpenAI-compatible API.
type Gateway struct {
	Prov provider.Provider

	mu         sync.RWMutex
	catalog    *model.CatalogSnapshot
	routes     map[model.ProviderModelID][]model.ProviderRoute
	pool       *catalog.Store
	reconciler *catalog.Reconciler
	health     *health.Manager
	latency    *latency.Registry
	probes     *probe.Scheduler
	autoPick   model.ProviderModelID
	autoRoute  model.ProviderRoute
}

// NewGateway builds a Gateway and performs the initial catalog discovery.
func NewGateway(ctx context.Context, p provider.Provider) (*Gateway, error) {
	g := &Gateway{
		Prov:       p,
		pool:       catalog.NewStore(catalog.NewPoolState(nil)),
		reconciler: catalog.NewReconciler(),
		health:     health.New(),
		latency:    latency.NewRegistry(),
	}
	g.probes = probe.New(g.latency, g.health, 30*time.Second)
	if err := g.RefreshCatalog(ctx); err != nil {
		return nil, err
	}
	return g, nil
}

// RefreshCatalog re-discovers models and repicks afm/auto target.
func (g *Gateway) RefreshCatalog(ctx context.Context) error {
	snap, err := g.Prov.DiscoverModels(ctx)
	if err != nil {
		return err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	snap, err = g.pool.CommitSnapshot(snap)
	if err != nil {
		return err
	}
	routes := make(map[model.ProviderModelID][]model.ProviderRoute, len(snap.Models))
	for id, pm := range snap.Models {
		routes[id] = opencode.Routes(id, pm.UpstreamID, pm.Base)
	}
	var reconcileErr error
	g.pool.Mutate(func(pool *catalog.PoolState) {
		_, reconcileErr = g.reconciler.Reconcile(pool, snap, routes)
	})
	if reconcileErr != nil {
		return reconcileErr
	}
	g.catalog, g.routes = snap, routes
	g.autoPick, g.autoRoute = "", model.ProviderRoute{}
	for id := range snap.Models {
		g.autoPick = id
		if candidates := routes[id]; len(candidates) > 0 {
			g.autoRoute = candidates[0]
		}
		break // Phase 2: single auto pick
	}
	if g.autoPick == "" {
		return errors.New("catalog empty: no routable models")
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
		out.Data = append(out.Data, modelEntry{ID: ExternalAutoModel, Object: "model", OwnedBy: "afm"})
	}
	if g.catalog != nil {
		for _, id := range g.pool.Snapshot().SelectedProviderModelIDs {
			if _, ok := g.catalog.Models[id]; !ok {
				continue
			}
			out.Data = append(out.Data, modelEntry{ID: string(id), Object: "model", OwnedBy: "afm"})
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

// ChatHandler serves POST /v1/chat/completions.
func (g *Gateway) ChatHandler(w http.ResponseWriter, r *http.Request) {
	var req chatCompletionRequest
	body, err := io.ReadAll(io.LimitReader(r.Body, 16<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, "read body: "+err.Error())
		return
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	allowed := map[string]bool{"model": true, "messages": true, "stream": true, "max_tokens": true, "temperature": true, "tools": true, "tool_choice": true, "response_format": true, "reasoning": true}
	for name := range fields {
		if !allowed[name] {
			writeError(w, http.StatusBadRequest, "unsupported parameter "+name)
			return
		}
	}
	if len(req.Messages) == 0 {
		writeError(w, http.StatusBadRequest, "messages must not be empty")
		return
	}

	pick, route, ok := g.resolveCandidate(req)
	if !ok {
		if req.Model != "" && req.Model != ExternalAutoModel {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("unknown or ineligible model %q", req.Model))
			return
		}
		writeError(w, http.StatusServiceUnavailable, "no models available")
		return
	}

	normalized := provider.NormalizedRequest{
		Messages:       req.Messages,
		Stream:         req.Stream,
		MaxTokens:      req.MaxTokens,
		Temperature:    req.Temperature,
		ToolChoice:     req.ToolChoice,
		ResponseFormat: req.ResponseFormat,
		Reasoning:      req.Reasoning,
	}
	for _, tool := range req.Tools {
		normalized.Tools = append(normalized.Tools, provider.ToolSpec{
			Type: tool.Type, Name: tool.Function.Name, Description: tool.Function.Description, Schema: string(tool.Function.Parameters),
		})
	}
	release, err := g.health.AcquireSlot(r.Context(), route.Provider)
	if err != nil {
		if r.Context().Err() != nil {
			return
		}
		writeError(w, http.StatusServiceUnavailable, "route slot unavailable")
		return
	}
	defer release()
	timer := latency.Start()
	stream, err := g.Prov.ChatCompletion(r.Context(), route, normalized)
	if err != nil {
		if r.Context().Err() != nil {
			return
		}
		if shouldRecordRouteFailure(r.Context(), err) {
			g.health.RouteFailure(string(route.ID), 0)
		}
		g.writeUpstreamError(w, err)
		return
	}
	defer stream.Close()

	if !req.Stream {
		// Aggregate the (one-shot) stream into a standard response.
		var sb strings.Builder
		recordedTTFT := false
		var ttftMs float64
		var toolCalls []json.RawMessage
		var reasoning strings.Builder
		finish := ""
		for {
			ev, err := stream.Next(r.Context())
			if errors.Is(err, io.EOF) {
				if !recordedTTFT {
					if shouldRecordRouteFailure(r.Context(), errors.New("upstream ended before first semantic event")) {
						g.latency.RecordRequest(string(route.ID), 0, durationMilliseconds(timer.Elapsed()))
						g.health.RouteFailure(string(route.ID), 0)
					}
					writeError(w, http.StatusBadGateway, "upstream ended before first semantic event")
					return
				}
				break
			}
			if err != nil {
				if r.Context().Err() != nil {
					return
				}
				if shouldRecordRouteFailure(r.Context(), err) {
					g.latency.RecordRequest(string(route.ID), ttftMs, durationMilliseconds(timer.Elapsed()))
					g.health.RouteFailure(string(route.ID), 0)
				}
				g.writeUpstreamError(w, err)
				return
			}
			sb.WriteString(ev.DeltaText)
			if ev.Semantic() && !recordedTTFT {
				if d, ok := timer.OnSemanticEvent(); ok {
					ttftMs = durationMilliseconds(d)
					recordedTTFT = true
				}
			}
			toolCalls = append(toolCalls, ev.ToolCalls...)
			reasoning.WriteString(ev.ReasoningContent)
			if ev.FinishReason != "" {
				finish = ev.FinishReason
			}
		}
		resp := chatCompletionResponse{
			ID: "chatcmpl-afm", Object: "chat.completion", Model: string(pick),
		}
		resp.Choices = append(resp.Choices, struct {
			Index   int `json:"index"`
			Message struct {
				Role      string            `json:"role"`
				Content   string            `json:"content"`
				ToolCalls []json.RawMessage `json:"tool_calls,omitempty"`
				Reasoning string            `json:"reasoning_content,omitempty"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		}{})
		resp.Choices[0].Message.Role = "assistant"
		resp.Choices[0].Message.Content = sb.String()
		resp.Choices[0].Message.ToolCalls = toolCalls
		resp.Choices[0].Message.Reasoning = reasoning.String()
		resp.Choices[0].FinishReason = finish
		writeJSON(w, http.StatusOK, resp)
		g.latency.RecordRequest(string(route.ID), ttftMs, durationMilliseconds(timer.Elapsed()))
		g.health.RouteSuccess(string(route.ID))
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}

	// Do not commit response headers until the first semantic upstream event.
	// This keeps a pre-semantic upstream failure representable as a normal HTTP
	// error and leaves the request path ready for Phase 7 fallback.
	committed := false
	recordedTTFT := false
	var ttftMs float64
	commit := func() {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		committed = true
	}
	flush := func() { flusher.Flush() }
	writeChunk := func(ev provider.StreamEvent) {
		var chunk chatCompletionChunk
		chunk.ID = "chatcmpl-afm"
		chunk.Object = "chat.completion.chunk"
		chunk.Model = string(pick)
		chunk.Choices = append(chunk.Choices, struct {
			Index int `json:"index"`
			Delta struct {
				Content          string            `json:"content,omitempty"`
				ToolCalls        []json.RawMessage `json:"tool_calls,omitempty"`
				ReasoningContent string            `json:"reasoning_content,omitempty"`
			} `json:"delta"`
			FinishReason any `json:"finish_reason"`
		}{})
		chunk.Choices[0].Index = ev.ChoiceIndex
		chunk.Choices[0].Delta.Content = ev.DeltaText
		chunk.Choices[0].Delta.ToolCalls = ev.ToolCalls
		chunk.Choices[0].Delta.ReasoningContent = ev.ReasoningContent
		if ev.FinishReason != "" {
			chunk.Choices[0].FinishReason = ev.FinishReason
		}
		data, _ := json.Marshal(chunk)
		fmt.Fprintf(w, "data: %s\n\n", data)
		flush()
	}

	for {
		ev, err := stream.Next(r.Context())
		if errors.Is(err, io.EOF) {
			if !committed {
				if r.Context().Err() != nil {
					return
				}
				if shouldRecordRouteFailure(r.Context(), errors.New("upstream ended before first semantic event")) {
					g.health.RouteFailure(string(route.ID), 0)
				}
				writeError(w, http.StatusBadGateway, "upstream ended before first semantic event")
				return
			}
			fmt.Fprint(w, "data: [DONE]\n\n")
			flush()
			g.latency.RecordRequest(string(route.ID), ttftMs, durationMilliseconds(timer.Elapsed()))
			g.health.RouteSuccess(string(route.ID))
			return
		}
		if err != nil {
			if !committed {
				if r.Context().Err() != nil {
					return
				}
				if shouldRecordRouteFailure(r.Context(), err) {
					g.health.RouteFailure(string(route.ID), 0)
				}
				g.writeUpstreamError(w, err)
				return
			}
			// After downstream commit we cannot fallback. A disconnected client
			// also must not poison route health.
			if isRequestCanceled(r.Context(), err) {
				return
			}
			if shouldRecordRouteFailure(r.Context(), err) {
				g.health.RouteFailure(string(route.ID), 0)
			}
			g.latency.RecordRequest(string(route.ID), ttftMs, durationMilliseconds(timer.Elapsed()))
			writeSSEError(w, err)
			flush()
			return
		}
		if !ev.Semantic() {
			continue
		}
		if !recordedTTFT {
			if d, ok := timer.OnSemanticEvent(); ok {
				ttftMs = durationMilliseconds(d)
				recordedTTFT = true
			}
		}
		if !committed {
			commit()
		}
		writeChunk(ev)
		if ev.FinishReason != "" {
			fmt.Fprint(w, "data: [DONE]\n\n")
			flush()
			g.latency.RecordRequest(string(route.ID), ttftMs, durationMilliseconds(timer.Elapsed()))
			g.health.RouteSuccess(string(route.ID))
			return
		}
	}
}

func durationMilliseconds(d time.Duration) float64 {
	return float64(d) / float64(time.Millisecond)
}

func shouldRecordRouteFailure(ctx context.Context, err error) bool {
	if isRequestCanceled(ctx, err) {
		return false
	}
	var unsupported *provider.UnsupportedError
	return !errors.As(err, &unsupported)
}

func isRequestCanceled(ctx context.Context, err error) bool {
	if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
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
	g.mu.RLock()
	defer g.mu.RUnlock()
	if g.catalog == nil {
		return "", model.ProviderRoute{}, false
	}
	in := router.FilterInput{Catalog: g.catalog, Routes: g.routes, Pool: g.pool.Snapshot().SelectedProviderModelIDs, Health: g.health}
	if req.Model != "" && req.Model != ExternalAutoModel {
		in.ExplicitID = model.ProviderModelID(req.Model)
	}
	in.Reqs.Streaming = req.Stream
	in.Reqs.Tools = len(req.Tools) > 0 || len(req.ToolChoice) > 0
	in.Reqs.StructuredOutput = len(req.ResponseFormat) > 0
	in.Reqs.Reasoning = len(req.Reasoning) > 0
	for _, message := range req.Messages {
		if parts, ok := message.Content.([]any); ok {
			for _, part := range parts {
				if raw, ok := part.(map[string]any); ok && raw["type"] == "image_url" {
					in.Reqs.Vision = true
				}
			}
		}
	}
	candidates := router.Filter(in).Eligible
	sort.Slice(candidates, func(i, j int) bool { return string(candidates[i].Route.ID) < string(candidates[j].Route.ID) })
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
		action := model.DecideFailureAction(fe.Failure)
		if fe.Failure.Class == model.FailureCanceled {
			// Client is gone; nothing to write.
			return
		}
		// Phase 6.5 has no candidate loop yet. The centralized action is still
		// evaluated here so Phase 7 can consume it without a second policy.
		_ = action
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
		"error": map[string]any{"message": msg, "type": "afm_error"},
	})
}

// Handler builds the full HTTP mux.
func (g *Gateway) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/models", g.ModelsHandler)
	mux.HandleFunc("POST /v1/chat/completions", g.ChatHandler)
	return mux
}
