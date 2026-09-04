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
	"strings"
	"sync"

	"github.com/konor123/Free-Model-Router/internal/model"
	"github.com/konor123/Free-Model-Router/internal/provider"
)

// ExternalAutoModel is the external model id meaning "route over the whole pool".
// Phase 2 resolves afm/auto to the single discovered OpenCode model.
const ExternalAutoModel = "afm/auto"

// Gateway wires one provider behind an OpenAI-compatible API.
type Gateway struct {
	Prov provider.Provider

	mu         sync.RWMutex
	catalog    *model.CatalogSnapshot
	autoPick   model.ProviderModelID
	autoRoute  model.ProviderRoute
}

// NewGateway builds a Gateway and performs the initial catalog discovery.
func NewGateway(ctx context.Context, p provider.Provider) (*Gateway, error) {
	g := &Gateway{Prov: p}
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
	g.catalog = snap
	for id, pm := range snap.Models {
		g.autoPick = id
		g.autoRoute = routeFor(pm)
		break // Phase 2: single auto pick
	}
	if g.autoPick == "" {
		return errors.New("catalog empty: no routable models")
	}
	return nil
}

func routeFor(pm model.ProviderModel) model.ProviderRoute {
	mdl := ""
	if _, m, err := pm.ID.Parse(); err == nil {
		mdl = m
	}
	rid, _ := model.NewRouteID("opencode-public", mdl)
	return model.ProviderRoute{ID: rid, ModelID: pm.ID, Access: pm.Access, Enabled: true}
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
		for id := range g.catalog.Models {
			out.Data = append(out.Data, modelEntry{ID: string(id), Object: "model", OwnedBy: "afm"})
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// chatCompletionRequest mirrors the OpenAI wire format.
type chatCompletionRequest struct {
	Model       string          `json:"model"`
	Messages    []provider.Message `json:"messages"`
	Stream      bool            `json:"stream"`
	MaxTokens   int             `json:"max_tokens,omitempty"`
	Temperature *float64        `json:"temperature,omitempty"`
}

type chatCompletionChunk struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	Model   string `json:"model"`
	Choices []struct {
		Delta struct {
			Content string `json:"content,omitempty"`
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
		Message struct {
			Role    string `json:"role"`
			Content string `json:"content"`
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
	if len(req.Messages) == 0 {
		writeError(w, http.StatusBadRequest, "messages must not be empty")
		return
	}

	g.mu.RLock()
	pick, route := g.autoPick, g.autoRoute
	g.mu.RUnlock()
	if pick == "" {
		writeError(w, http.StatusServiceUnavailable, "no models available")
		return
	}
	if req.Model != "" && req.Model != ExternalAutoModel && req.Model != string(pick) {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("unknown model %q", req.Model))
		return
	}

	normalized := provider.NormalizedRequest{
		Messages:  req.Messages,
		Stream:    req.Stream,
		MaxTokens: req.MaxTokens,
	}
	stream, err := g.Prov.ChatCompletion(r.Context(), route, normalized)
	if err != nil {
		g.writeUpstreamError(w, err)
		return
	}
	defer stream.Close()

	if !req.Stream {
		// Aggregate the (one-shot) stream into a standard response.
		var sb strings.Builder
		finish := ""
		for {
			ev, err := stream.Next(r.Context())
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				g.writeUpstreamError(w, err)
				return
			}
			sb.WriteString(ev.DeltaText)
			if ev.FinishReason != "" {
				finish = ev.FinishReason
			}
		}
		resp := chatCompletionResponse{
			ID: "chatcmpl-afm", Object: "chat.completion", Model: string(pick),
		}
		resp.Choices = append(resp.Choices, struct {
			Message struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		}{})
		resp.Choices[0].Message.Role = "assistant"
		resp.Choices[0].Message.Content = sb.String()
		resp.Choices[0].FinishReason = finish
		writeJSON(w, http.StatusOK, resp)
		return
	}

	// Streaming: commit SSE downstream only once we can validate the candidate
	// (Phase 2 simple version: buffer nothing beyond headers; commit guard lands in Phase 8).
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}

	flush := func() {
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}
	_ = flusher

	for {
		ev, err := stream.Next(r.Context())
		if errors.Is(err, io.EOF) {
			fmt.Fprint(w, "data: [DONE]\n\n")
			flush()
			return
		}
		if err != nil {
			// After downstream commit we cannot fallback (Phase 8 formalizes this).
			fmt.Fprintf(w, "data: {\"error\": %q}\n\n", err.Error())
			flush()
			return
		}
		var chunk chatCompletionChunk
		chunk.ID = "chatcmpl-afm"
		chunk.Object = "chat.completion.chunk"
		chunk.Model = string(pick)
		var c chatCompletionChunk
		c.ID, c.Object, c.Model = "chatcmpl-afm", "chat.completion.chunk", string(pick)
		c.Choices = append(c.Choices, struct {
			Delta struct {
				Content string `json:"content,omitempty"`
			} `json:"delta"`
			FinishReason any `json:"finish_reason"`
		}{})
		c.Choices[0].Delta.Content = ev.DeltaText
		if ev.FinishReason != "" {
			c.Choices[0].FinishReason = ev.FinishReason
		}
		chunk = c
		data, _ := json.Marshal(chunk)
		fmt.Fprintf(w, "data: %s\n\n", data)
		flush()
		if ev.FinishReason != "" {
			fmt.Fprint(w, "data: [DONE]\n\n")
			flush()
			return
		}
	}
}

func (g *Gateway) writeUpstreamError(w http.ResponseWriter, err error) {
	var fe *provider.FailureError
	if errors.As(err, &fe) {
		switch fe.Failure.Class {
		case model.FailureCanceled:
			// Client is gone; nothing to write.
			return
		case model.FailureRateLimited:
			writeError(w, http.StatusTooManyRequests, err.Error())
		case model.FailureAuth:
			writeError(w, http.StatusBadGateway, err.Error())
		case model.FailureBadRequest:
			writeError(w, http.StatusBadRequest, err.Error())
		case model.FailureTimeout, model.FailureServerError:
			writeError(w, http.StatusBadGateway, err.Error())
		default:
			writeError(w, http.StatusBadGateway, err.Error())
		}
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
