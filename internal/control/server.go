package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/konor123/Free-Model-Router/internal/catalog"
	"github.com/konor123/Free-Model-Router/internal/gateway"
	"github.com/konor123/Free-Model-Router/internal/model"
	"github.com/konor123/Free-Model-Router/internal/security"
	"github.com/konor123/Free-Model-Router/internal/usage"
)

// Server serves the authenticated localhost management API.
type Server struct {
	backend Backend
	opts    Options
	mux     *http.ServeMux
	auth    http.Handler
}

// NewServer builds a control server. Management tokens are required even on
// loopback because desktop and CLI clients share this boundary.
func NewServer(backend Backend, opts Options) (*Server, error) {
	if backend == nil {
		return nil, ErrBackendRequired
	}
	opts.Token = strings.TrimSpace(opts.Token)
	if opts.Token == "" {
		return nil, ErrTokenRequired
	}
	if opts.APIVersion == "" {
		opts.APIVersion = APIVersion
	}
	if opts.BuildVersion == "" {
		opts.BuildVersion = DefaultBuildVersion
	}
	if opts.InstanceID == "" {
		opts.InstanceID = NewInstanceID()
	}
	opts.Features = availableFeatures(opts.Features, opts.Events)
	s := &Server{backend: backend, opts: opts, mux: http.NewServeMux()}
	s.mux.HandleFunc("GET /_fmr/status", s.handleStatus)
	s.mux.HandleFunc("GET /_fmr/providers", s.handleProviders)
	s.mux.HandleFunc("GET /_fmr/models", s.handleModels)
	s.mux.HandleFunc("POST /_fmr/catalog/refresh", s.handleCatalogRefresh)
	s.mux.HandleFunc("GET /_fmr/model-pool", s.handlePool)
	s.mux.HandleFunc("PUT /_fmr/model-pool", s.handlePoolPut)
	s.mux.HandleFunc("PATCH /_fmr/model-pool", s.handlePoolPatch)
	s.mux.HandleFunc("POST /_fmr/pin", s.handlePin)
	s.mux.HandleFunc("POST /_fmr/auto", s.handleAuto)
	s.mux.HandleFunc("GET /_fmr/logs", s.handleLogs)
	s.mux.HandleFunc("GET /_fmr/logs/events", s.handleLogEvents)
	s.mux.HandleFunc("GET /_fmr/config", s.handleConfig)
	s.mux.HandleFunc("PUT /_fmr/config", s.handleConfigUpdate)
	s.mux.HandleFunc("POST /_fmr/stop", s.handleStop)
	s.auth = security.RequireBearer(s.mux, opts.Token)
	return s, nil
}

// New is a concise constructor alias.
func New(backend Backend, opts Options) (*Server, error) { return NewServer(backend, opts) }

// NewHandler constructs the HTTP handler directly.
func NewHandler(backend Backend, opts Options) (http.Handler, error) {
	return NewServer(backend, opts)
}

// Handler returns the server as an http.Handler.
func (s *Server) Handler() http.Handler { return s }

// ServeHTTP enforces the localhost boundary before checking credentials.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s == nil || s.auth == nil {
		http.Error(w, "control server unavailable", http.StatusServiceUnavailable)
		return
	}
	if !security.IsLoopbackRemote(r.RemoteAddr) {
		writeError(w, http.StatusForbidden, "management API is localhost-only")
		return
	}
	s.auth.ServeHTTP(w, r)
}

func (s *Server) handleStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.opts.Status(s.backend.ControlSnapshot()))
}

func (s *Server) handleProviders(w http.ResponseWriter, _ *http.Request) {
	snapshot := s.backend.ControlSnapshot()
	data := make([]ProviderResponse, 0, len(snapshot.Providers))
	for _, provider := range snapshot.Providers {
		data = append(data, ProviderResponse{
			ID: provider.ID, Models: provider.Models, Routes: provider.Routes, Enabled: provider.Enabled,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": data})
}

func (s *Server) handleModels(w http.ResponseWriter, _ *http.Request) {
	snapshot := s.backend.ControlSnapshot()
	data := make([]ModelResponse, 0, len(snapshot.Models))
	for _, modelView := range snapshot.Models {
		modelResponse := ModelResponse{
			ID:                string(modelView.Model.ID),
			CanonicalKey:      string(modelView.Model.CanonicalKey),
			DisplayName:       modelView.Model.DisplayName,
			UpstreamID:        modelView.Model.UpstreamID,
			Capabilities:      modelView.Model.Base,
			Selected:          modelView.Selected,
			Pinned:            modelView.Pinned,
			RoutingScore:      modelView.RoutingScore,
			RoutingScoreKnown: modelView.RoutingScoreKnown,
			Routes:            make([]RouteResponse, 0, len(modelView.Routes)),
		}
		for _, route := range modelView.Routes {
			coolingUntil := ""
			if !route.Health.CoolingUntil.IsZero() {
				coolingUntil = route.Health.CoolingUntil.UTC().Format(time.RFC3339Nano)
			}
			available := !route.Health.QuotaExhausted && (route.Health.CoolingUntil.IsZero() || time.Now().After(route.Health.CoolingUntil))
			modelResponse.Routes = append(modelResponse.Routes, RouteResponse{
				ID:              string(route.ID),
				ModelID:         string(route.ModelID),
				Provider:        route.Provider,
				UpstreamModelID: route.UpstreamModelID,
				CredentialID:    route.CredentialID,
				Access:          route.Access,
				Enabled:         route.Enabled,
				Capabilities:    route.Capabilities,
				Health: RouteHealthResponse{
					CoolingUntil: coolingUntil, QuotaExhausted: route.Health.QuotaExhausted,
					ConsecutiveFailures: route.Health.ConsecutiveFailures, Available: available,
				},
				TTFTMs: route.TTFTMs, TTFTKnown: route.TTFTKnown,
				Performance: route.Performance, EffectivePerformance: route.EffectivePerformance,
				Confidence: route.Confidence, LatencyScore: route.LatencyScore,
				RoutingScore: route.RoutingScore, RoutingScoreKnown: route.RoutingScoreKnown,
			})
		}
		data = append(data, modelResponse)
	}
	writeJSON(w, http.StatusOK, map[string]any{"catalogRevision": snapshot.CatalogRevision, "data": data})
}

type catalogRefresher interface{ RefreshCatalog(context.Context) error }

func (s *Server) handleCatalogRefresh(w http.ResponseWriter, r *http.Request) {
	refresher, ok := s.backend.(catalogRefresher)
	if !ok {
		writeError(w, http.StatusNotFound, "catalog refresh is unavailable")
		return
	}
	if err := refresher.RefreshCatalog(r.Context()); err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "refreshed", "catalogRevision": s.backend.ControlSnapshot().CatalogRevision})
}

func (s *Server) handlePool(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, poolResponse(s.backend.ControlSnapshot()))
}

type poolPutRequest struct {
	Revision                 *int64                  `json:"revision"`
	Mode                     catalog.PoolMode        `json:"mode"`
	SelectedProviderModelIDs []model.ProviderModelID `json:"selectedProviderModelIds"`
}

type poolPatchRequest struct {
	Revision  *int64                  `json:"revision"`
	Select    []model.ProviderModelID `json:"select"`
	Deselect  []model.ProviderModelID `json:"deselect"`
	SelectAll bool                    `json:"selectAll"`
	ClearAll  bool                    `json:"clearAll"`
	Mode      *catalog.PoolMode       `json:"mode"`
}

type pinRequest struct {
	Revision        *int64                `json:"revision"`
	ProviderModelID model.ProviderModelID `json:"providerModelId"`
}

type revisionRequest struct {
	Revision *int64 `json:"revision"`
}

func (s *Server) handlePoolPut(w http.ResponseWriter, r *http.Request) {
	var request poolPutRequest
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if request.Revision == nil {
		writeError(w, http.StatusBadRequest, "revision is required")
		return
	}
	if err := validateIDs(request.SelectedProviderModelIDs); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	mode := request.Mode
	if mode == "" {
		mode = catalog.ModeManual
	}
	pool, err := s.backend.ReplaceModelPool(*request.Revision, request.SelectedProviderModelIDs, mode)
	if err != nil {
		s.writeMutationError(w, err)
		return
	}
	if err := s.changed(); err != nil {
		writeError(w, http.StatusInternalServerError, "persist control state: "+err.Error())
		return
	}
	s.writePool(w, pool)
}

func (s *Server) handlePoolPatch(w http.ResponseWriter, r *http.Request) {
	var request poolPatchRequest
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if request.Revision == nil {
		writeError(w, http.StatusBadRequest, "revision is required")
		return
	}
	if err := validateIDs(request.Select); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := validateIDs(request.Deselect); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	mutation := gateway.PoolMutation{
		Select: request.Select, Deselect: request.Deselect,
		SelectAll: request.SelectAll, ClearAll: request.ClearAll, Mode: request.Mode,
	}
	pool, err := s.backend.UpdateModelPool(*request.Revision, mutation)
	if err != nil {
		s.writeMutationError(w, err)
		return
	}
	if err := s.changed(); err != nil {
		writeError(w, http.StatusInternalServerError, "persist control state: "+err.Error())
		return
	}
	s.writePool(w, pool)
}

func (s *Server) handlePin(w http.ResponseWriter, r *http.Request) {
	var request pinRequest
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if request.Revision == nil {
		writeError(w, http.StatusBadRequest, "revision is required")
		return
	}
	if _, _, err := request.ProviderModelID.Parse(); err != nil {
		writeError(w, http.StatusBadRequest, "providerModelId: "+err.Error())
		return
	}
	pool, err := s.backend.PinModel(*request.Revision, request.ProviderModelID)
	if err != nil {
		s.writeMutationError(w, err)
		return
	}
	if err := s.changed(); err != nil {
		writeError(w, http.StatusInternalServerError, "persist control state: "+err.Error())
		return
	}
	s.writePool(w, pool)
}

func (s *Server) handleAuto(w http.ResponseWriter, r *http.Request) {
	var request revisionRequest
	if err := decodeJSONOptional(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if request.Revision == nil {
		writeError(w, http.StatusBadRequest, "revision is required")
		return
	}
	pool, err := s.backend.AutoSelect(*request.Revision)
	if err != nil {
		s.writeMutationError(w, err)
		return
	}
	if err := s.changed(); err != nil {
		writeError(w, http.StatusInternalServerError, "persist control state: "+err.Error())
		return
	}
	s.writePool(w, pool)
}

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	query, err := parseUsageQuery(r.URL.Query())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if s.opts.Logs == nil {
		writeJSON(w, http.StatusOK, map[string]any{"data": []usage.RequestRecord{}})
		return
	}
	records, err := s.opts.Logs.List(query)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "read usage logs: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": records})
}

func (s *Server) handleLogEvents(w http.ResponseWriter, r *http.Request) {
	if s.opts.Events == nil {
		writeError(w, http.StatusNotFound, "usage event stream is unavailable")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "usage event stream unsupported")
		return
	}
	events, unsubscribe := s.opts.Events.Subscribe()
	defer unsubscribe()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher.Flush()
	for {
		select {
		case <-r.Context().Done():
			return
		case event, ok := <-events:
			if !ok {
				return
			}
			payload, err := json.Marshal(event)
			if err != nil {
				return
			}
			if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event.Type, payload); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func (s *Server) handleConfig(w http.ResponseWriter, _ *http.Request) {
	if s.opts.Config == nil {
		writeError(w, http.StatusNotFound, "config endpoint is unavailable")
		return
	}
	writeJSON(w, http.StatusOK, s.opts.Config())
}

func (s *Server) handleConfigUpdate(w http.ResponseWriter, r *http.Request) {
	if s.opts.UpdateConfig == nil {
		writeError(w, http.StatusNotFound, "config update endpoint is unavailable")
		return
	}
	var request ConfigUpdateRequest
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	response, err := s.opts.UpdateConfig(request)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, ErrConfigRevisionConflict) {
			status = http.StatusConflict
		}
		writeError(w, status, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) handleStop(w http.ResponseWriter, _ *http.Request) {
	if s.opts.Stop == nil {
		writeError(w, http.StatusNotFound, "stop endpoint is unavailable")
		return
	}
	go s.opts.Stop()
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "stopping"})
}

func (s *Server) writePool(w http.ResponseWriter, pool catalog.ModelPoolConfig) {
	snapshot := s.backend.ControlSnapshot()
	snapshot.Pool = pool
	writeJSON(w, http.StatusOK, poolResponse(snapshot))
}

func (s *Server) changed() error {
	if s.opts.OnChange == nil {
		return nil
	}
	return s.opts.OnChange(s.backend.ControlSnapshot())
}

func (s *Server) writeMutationError(w http.ResponseWriter, err error) {
	status := http.StatusBadRequest
	switch {
	case errors.Is(err, gateway.ErrPoolRevisionConflict):
		status = http.StatusConflict
	case errors.Is(err, gateway.ErrControlModelNotFound):
		status = http.StatusNotFound
	}
	writeError(w, status, err.Error())
}

func poolResponse(snapshot gateway.ControlSnapshot) PoolResponse {
	selected := make([]string, 0, len(snapshot.Pool.SelectedProviderModelIDs))
	for _, id := range snapshot.Pool.SelectedProviderModelIDs {
		selected = append(selected, string(id))
	}
	return PoolResponse{
		Revision: snapshot.Pool.Revision, Mode: string(snapshot.Pool.Mode),
		SelectedProviderModelIDs: selected, PinnedModel: string(snapshot.PinnedModel),
	}
}

func decodeJSON(r *http.Request, dst any) error {
	if r == nil || r.Body == nil {
		return invalidRequest("request body is required")
	}
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return invalidRequest("invalid JSON: %v", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return invalidRequest("request body must contain one JSON value")
		}
		return invalidRequest("invalid JSON: %v", err)
	}
	return nil
}

func decodeJSONOptional(r *http.Request, dst any) error {
	if r == nil || r.Body == nil {
		return nil
	}
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err == io.EOF {
		return nil
	} else if err != nil {
		return invalidRequest("invalid JSON: %v", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return invalidRequest("request body must contain one JSON value")
		}
		return invalidRequest("invalid JSON: %v", err)
	}
	return nil
}

func validateIDs(ids []model.ProviderModelID) error {
	seen := make(map[model.ProviderModelID]bool, len(ids))
	for _, id := range ids {
		if _, _, err := id.Parse(); err != nil {
			return invalidRequest("invalid providerModelId %q: %v", id, err)
		}
		if seen[id] {
			return invalidRequest("duplicate providerModelId %q", id)
		}
		seen[id] = true
	}
	return nil
}

func parseUsageQuery(values url.Values) (usage.Query, error) {
	query := usage.Query{
		Provider: values.Get("provider"), Model: values.Get("model"),
		Result: usage.Result(values.Get("result")),
	}
	if raw := values.Get("from"); raw != "" {
		parsed, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			return query, invalidRequest("invalid from time")
		}
		query.From = parsed
	}
	if raw := values.Get("to"); raw != "" {
		parsed, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			return query, invalidRequest("invalid to time")
		}
		query.To = parsed
	}
	if raw := values.Get("fallback"); raw != "" {
		fallback, err := strconv.ParseBool(raw)
		if err != nil {
			return query, invalidRequest("invalid fallback filter")
		}
		query.Fallback = &fallback
	}
	var err error
	if raw := values.Get("offset"); raw != "" {
		query.Offset, err = strconv.Atoi(raw)
		if err != nil || query.Offset < 0 {
			return query, invalidRequest("invalid offset")
		}
	}
	if raw := values.Get("limit"); raw != "" {
		query.Limit, err = strconv.Atoi(raw)
		if err != nil || query.Limit < 0 {
			return query, invalidRequest("invalid limit")
		}
		if query.Limit > 1000 {
			query.Limit = 1000
		}
	}
	return query, nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"message": message, "type": "fmr_control_error"}})
}
