package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/konor123/Free-Model-Router/internal/latency"
	"github.com/konor123/Free-Model-Router/internal/model"
	"github.com/konor123/Free-Model-Router/internal/provider"
	"github.com/konor123/Free-Model-Router/internal/router"
)

type attemptCandidate struct {
	router.Candidate
	fallbackOnly bool
}

const (
	attemptBudgetOpen uint32 = iota
	attemptBudgetCommitted
	attemptBudgetExpired
)

type attemptBudget struct {
	ctx    context.Context
	cancel context.CancelFunc
	timer  *time.Timer
	state  atomic.Uint32
}

func newAttemptBudget(parent context.Context, deadline time.Time) *attemptBudget {
	ctx, cancel := context.WithCancel(parent)
	b := &attemptBudget{ctx: ctx, cancel: cancel}
	delay := time.Until(deadline)
	if delay < 0 {
		delay = 0
	}
	b.timer = time.AfterFunc(delay, func() {
		if b.state.CompareAndSwap(attemptBudgetOpen, attemptBudgetExpired) {
			b.cancel()
		}
	})
	return b
}

func (b *attemptBudget) Expired() bool {
	return b != nil && b.state.Load() == attemptBudgetExpired
}

// Commit stops the failover deadline without canceling an already committed
// streaming response. Once downstream has semantic data, failover is forbidden
// and the stream is allowed to finish under the client context.
//
// It returns false when the deadline won the race before the commit.
func (b *attemptBudget) Commit() bool {
	if b == nil || !b.state.CompareAndSwap(attemptBudgetOpen, attemptBudgetCommitted) {
		return false
	}
	if b.timer != nil {
		b.timer.Stop()
	}
	return true
}

func (b *attemptBudget) Close() {
	if b == nil {
		return
	}
	if b.timer != nil {
		b.timer.Stop()
	}
	b.cancel()
}

func failoverBudgetError() error {
	return provider.NewFailureError(
		model.NewFailure(model.FailureTimeout, model.ScopeRoute),
		errors.New("failover budget exhausted"),
	)
}

// ChatHandler serves POST /v1/chat/completions with bounded, failure-aware
// candidate progression.
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
	allowed := map[string]bool{
		"model": true, "messages": true, "stream": true, "max_tokens": true,
		"temperature": true, "tools": true, "tool_choice": true,
		"response_format": true, "reasoning": true,
	}
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

	candidates := g.resolveAttemptCandidates(req)
	if len(candidates) == 0 {
		if req.Model != "" && !isAutoModel(req.Model) {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("unknown or ineligible model %q", req.Model))
			return
		}
		writeError(w, http.StatusServiceUnavailable, "no models available")
		return
	}

	normalized := normalizeChatRequest(req)
	if req.Stream {
		if _, ok := w.(http.Flusher); !ok {
			writeError(w, http.StatusInternalServerError, "streaming unsupported")
			return
		}
	}

	policy := g.failover.normalized()
	deadline := time.Now().Add(policy.Budget)
	attempted := map[model.RouteID]bool{}
	var previous *router.Candidate
	action := model.ActionStop
	var lastErr error

	for attempt := 0; attempt < policy.MaxAttempts; attempt++ {
		if r.Context().Err() != nil {
			return
		}
		if attempt > 0 && !time.Now().Before(deadline) {
			break
		}
		candidate, ok := nextAttemptCandidate(candidates, attempted, previous, action)
		if !ok {
			break
		}
		attempted[candidate.Route.ID] = true
		current := candidate.Candidate
		previous = &current
		budget := newAttemptBudget(r.Context(), deadline)

		var handled bool
		if req.Stream {
			handled, err = g.runStreamingAttempt(w, r.Context(), budget, current, normalized)
		} else {
			handled, err = g.runNonStreamingAttempt(w, r.Context(), budget, current, normalized)
		}
		budget.Close()
		if handled {
			return
		}
		lastErr = err
		if err == nil {
			return
		}
		_, action = fallbackDecision(err)
		if action == model.ActionStop {
			break
		}
	}

	if r.Context().Err() != nil {
		return
	}
	if lastErr != nil {
		g.writeUpstreamError(w, lastErr)
		return
	}
	writeError(w, http.StatusServiceUnavailable, "no eligible fallback candidates")
}

func normalizeChatRequest(req chatCompletionRequest) provider.NormalizedRequest {
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
			Type: tool.Type, Name: tool.Function.Name, Description: tool.Function.Description,
			Schema: string(tool.Function.Parameters),
		})
	}
	return normalized
}

func (g *Gateway) resolveAttemptCandidates(req chatCompletionRequest) []attemptCandidate {
	g.mu.RLock()
	defer g.mu.RUnlock()
	if g.catalog == nil {
		return nil
	}

	base := g.filterInputLocked(req)
	var normal []router.Candidate
	if req.Model != "" && !isAutoModel(req.Model) {
		pinned := base
		pinned.ExplicitID = model.ProviderModelID(req.Model)
		normal = g.rankCandidates(router.Filter(pinned).Eligible)
		// An explicit model remains authoritative for eligibility. Its selected
		// pool fallback is added only after the pinned model has a valid route.
		if len(normal) == 0 {
			return nil
		}
		pool := base
		pool.ExplicitID = ""
		normal = append(normal, g.rankedUnique(router.Filter(pool).Eligible, normal)...)
	} else {
		normal = g.rankCandidates(router.Filter(base).Eligible)
	}

	result := make([]attemptCandidate, 0, len(normal)+len(normal))
	normalRoutes := make(map[model.RouteID]bool, len(normal))
	normalModels := make(map[model.ProviderModelID]bool, len(normal))
	for _, candidate := range normal {
		result = append(result, attemptCandidate{Candidate: candidate})
		normalRoutes[candidate.Route.ID] = true
		normalModels[candidate.Model.ID] = true
	}

	// Unknown-access routes are not normal automatic candidates. They become
	// fallback-only alternates only for a model that already has an eligible
	// route, which supports configured auth paths without making Unknown access
	// generally routable.
	unknownInput := base
	unknownInput.AllowUnknown = true
	var unknown []router.Candidate
	if req.Model != "" && !isAutoModel(req.Model) {
		pinned := unknownInput
		pinned.ExplicitID = model.ProviderModelID(req.Model)
		unknown = append(unknown, router.Filter(pinned).Eligible...)
		pool := unknownInput
		pool.ExplicitID = ""
		unknown = append(unknown, router.Filter(pool).Eligible...)
	} else {
		unknown = router.Filter(unknownInput).Eligible
	}
	unknown = g.rankCandidates(unknown)
	for _, candidate := range unknown {
		if normalRoutes[candidate.Route.ID] || !normalModels[candidate.Model.ID] || candidate.Route.EffectiveAccess() != model.AccessUnknown {
			continue
		}
		result = append(result, attemptCandidate{Candidate: candidate, fallbackOnly: true})
	}
	return result
}

func (g *Gateway) filterInputLocked(req chatCompletionRequest) router.FilterInput {
	in := router.FilterInput{
		Catalog: g.catalog,
		Routes:  g.routes,
		Pool:    g.pool.Snapshot().SelectedProviderModelIDs,
		Health:  g.health,
	}
	in.Reqs.Streaming = req.Stream
	in.Reqs.Tools = len(req.Tools) > 0 || len(req.ToolChoice) > 0
	in.Reqs.StructuredOutput = len(req.ResponseFormat) > 0
	in.Reqs.Reasoning = len(req.Reasoning) > 0
	in.Reqs.MaxOutputTokens = req.MaxTokens
	for _, message := range req.Messages {
		if parts, ok := message.Content.([]any); ok {
			for _, part := range parts {
				if raw, ok := part.(map[string]any); ok && raw["type"] == "image_url" {
					in.Reqs.Vision = true
				}
			}
		}
	}
	return in
}

func nextAttemptCandidate(candidates []attemptCandidate, attempted map[model.RouteID]bool, previous *router.Candidate, action model.FailureAction) (attemptCandidate, bool) {
	if previous == nil {
		for _, candidate := range candidates {
			if !candidate.fallbackOnly && !attempted[candidate.Route.ID] {
				return candidate, true
			}
		}
		return attemptCandidate{}, false
	}

	previousCredential := credentialKey(previous.Route)
	acceptable := func(candidate attemptCandidate) bool {
		if attempted[candidate.Route.ID] {
			return false
		}
		if candidate.fallbackOnly && candidate.Model.ID != previous.Model.ID {
			return false
		}
		switch action {
		case model.ActionNextRoute:
			return true
		case model.ActionNextModel:
			return !candidate.fallbackOnly && candidate.Model.ID != previous.Model.ID
		case model.ActionDisableCredential:
			return credentialKey(candidate.Route) != previousCredential
		case model.ActionDisableRoute:
			return candidate.Route.ID != previous.Route.ID
		case model.ActionCooldownProvider:
			return candidate.Route.Provider != previous.Route.Provider
		default:
			return false
		}
	}

	if action == model.ActionNextRoute {
		for _, candidate := range candidates {
			if acceptable(candidate) && candidate.Model.ID == previous.Model.ID {
				return candidate, true
			}
		}
	}
	for _, candidate := range candidates {
		if acceptable(candidate) {
			return candidate, true
		}
	}
	return attemptCandidate{}, false
}

func credentialKey(route model.ProviderRoute) string {
	if route.CredentialID != "" {
		return route.CredentialID
	}
	if i := strings.Index(string(route.ID), "::"); i > 0 {
		return string(route.ID[:i])
	}
	return route.Provider
}

func fallbackDecision(err error) (model.Failure, model.FailureAction) {
	var unsupported *provider.UnsupportedError
	if errors.As(err, &unsupported) {
		failure := model.NewFailure(model.FailureBadRequest, model.ScopeRoute)
		return failure, model.ActionNextModel
	}

	var failureErr *provider.FailureError
	if errors.As(err, &failureErr) {
		failure := failureErr.Failure
		action := model.DecideFailureAction(failure)
		// A route-specific 400 or protocol response cannot be safely retried
		// through the same ProviderModel, but another model may still work.
		if failure.Scope != model.ScopeRequest && (failure.Class == model.FailureBadRequest || failure.Class == model.FailureProtocol) {
			action = model.ActionNextModel
		}
		return failure, action
	}

	if errors.Is(err, context.Canceled) {
		failure := model.NewFailure(model.FailureCanceled, model.ScopeRequest)
		return failure, model.ActionStop
	}
	if errors.Is(err, context.DeadlineExceeded) {
		failure := model.NewFailure(model.FailureTimeout, model.ScopeRoute)
		return failure, model.ActionNextRoute
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		class := model.FailureNetwork
		if netErr.Timeout() {
			class = model.FailureTimeout
		}
		failure := model.NewFailure(class, model.ScopeRoute)
		return failure, model.ActionNextRoute
	}
	failure := model.NewFailure(model.FailureUnknown, model.ScopeRequest)
	return failure, model.ActionStop
}

func (g *Gateway) recordAttemptFailure(ctx context.Context, candidate router.Candidate, timer *latency.Timer, ttftMs float64, err error) {
	if !shouldRecordRouteFailure(ctx, err) {
		return
	}
	g.latency.RecordRequest(string(candidate.Route.ID), ttftMs, durationMilliseconds(timer.Elapsed()))
	g.health.RouteFailure(string(candidate.Route.ID), 0)
}

func (g *Gateway) runNonStreamingAttempt(w http.ResponseWriter, parent context.Context, budget *attemptBudget, candidate router.Candidate, req provider.NormalizedRequest) (bool, error) {
	ctx := budget.ctx
	release, err := g.health.AcquireSlot(ctx, candidate.Route.Provider)
	if err != nil {
		if budget.Expired() {
			return false, failoverBudgetError()
		}
		return false, err
	}
	defer release()
	timer := latency.Start()
	stream, err := g.Prov.ChatCompletion(ctx, candidate.Route, req)
	if err != nil {
		if budget.Expired() {
			err = failoverBudgetError()
		}
		g.recordAttemptFailure(parent, candidate, timer, 0, err)
		return false, err
	}
	if stream == nil {
		err = provider.NewFailureError(model.NewFailure(model.FailureProtocol, model.ScopeRoute), errors.New("provider returned nil chat stream"))
		g.recordAttemptFailure(parent, candidate, timer, 0, err)
		return false, err
	}
	defer stream.Close()

	var sb strings.Builder
	var reasoning strings.Builder
	var toolCalls []json.RawMessage
	var ttftMs float64
	recordedTTFT := false
	finish := ""
	for {
		ev, nextErr := stream.Next(ctx)
		if budget.Expired() {
			return false, g.failAttemptWithBudget(parent, candidate, timer, ttftMs)
		}
		if errors.Is(nextErr, io.EOF) {
			if !recordedTTFT {
				err = provider.NewFailureError(model.NewFailure(model.FailureProtocol, model.ScopeRoute), errors.New("upstream ended before first semantic event"))
				g.recordAttemptFailure(parent, candidate, timer, 0, err)
				return false, err
			}
			break
		}
		if nextErr != nil {
			g.recordAttemptFailure(parent, candidate, timer, ttftMs, nextErr)
			return false, nextErr
		}
		sb.WriteString(ev.DeltaText)
		toolCalls = append(toolCalls, ev.ToolCalls...)
		reasoning.WriteString(ev.ReasoningContent)
		if ev.Semantic() && !recordedTTFT {
			if d, ok := timer.OnSemanticEvent(); ok {
				ttftMs = durationMilliseconds(d)
				recordedTTFT = true
			}
		}
		if ev.FinishReason != "" {
			finish = ev.FinishReason
			break
		}
	}

	resp := chatCompletionResponse{ID: "chatcmpl-fmr", Object: "chat.completion", Model: string(candidate.Model.ID)}
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
	g.latency.RecordRequest(string(candidate.Route.ID), ttftMs, durationMilliseconds(timer.Elapsed()))
	g.health.RouteSuccess(string(candidate.Route.ID))
	return true, nil
}

func (g *Gateway) runStreamingAttempt(w http.ResponseWriter, parent context.Context, budget *attemptBudget, candidate router.Candidate, req provider.NormalizedRequest) (bool, error) {
	ctx := budget.ctx
	release, err := g.health.AcquireSlot(ctx, candidate.Route.Provider)
	if err != nil {
		if budget.Expired() {
			return false, failoverBudgetError()
		}
		return false, err
	}
	defer release()
	timer := latency.Start()
	stream, err := g.Prov.ChatCompletion(ctx, candidate.Route, req)
	if err != nil {
		if budget.Expired() {
			err = failoverBudgetError()
		}
		g.recordAttemptFailure(parent, candidate, timer, 0, err)
		return false, err
	}
	if stream == nil {
		err = provider.NewFailureError(model.NewFailure(model.FailureProtocol, model.ScopeRoute), errors.New("provider returned nil chat stream"))
		g.recordAttemptFailure(parent, candidate, timer, 0, err)
		return false, err
	}
	defer stream.Close()
	flusher := w.(http.Flusher)
	committed := false
	var ttftMs float64
	recordedTTFT := false
	commit := func() bool {
		if !budget.Commit() {
			return false
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		committed = true
		return true
	}
	writeChunk := func(ev provider.StreamEvent) {
		var chunk chatCompletionChunk
		chunk.ID = "chatcmpl-fmr"
		chunk.Object = "chat.completion.chunk"
		chunk.Model = string(candidate.Model.ID)
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
		_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
		flusher.Flush()
	}

	for {
		ev, nextErr := stream.Next(ctx)
		if budget.Expired() && !committed {
			return false, g.failAttemptWithBudget(parent, candidate, timer, ttftMs)
		}
		if errors.Is(nextErr, io.EOF) {
			if !committed {
				err = provider.NewFailureError(model.NewFailure(model.FailureProtocol, model.ScopeRoute), errors.New("upstream ended before first semantic event"))
				g.recordAttemptFailure(parent, candidate, timer, 0, err)
				return false, err
			}
			_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
			flusher.Flush()
			g.latency.RecordRequest(string(candidate.Route.ID), ttftMs, durationMilliseconds(timer.Elapsed()))
			g.health.RouteSuccess(string(candidate.Route.ID))
			return true, nil
		}
		if nextErr != nil {
			if !committed {
				g.recordAttemptFailure(parent, candidate, timer, ttftMs, nextErr)
				return false, nextErr
			}
			if isRequestCanceled(parent, nextErr) {
				return true, nil
			}
			g.recordAttemptFailure(parent, candidate, timer, ttftMs, nextErr)
			writeSSEError(w, nextErr)
			flusher.Flush()
			return true, nil
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
			if !commit() {
				return false, g.failAttemptWithBudget(parent, candidate, timer, ttftMs)
			}
		}
		writeChunk(ev)
		if ev.FinishReason != "" {
			_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
			flusher.Flush()
			g.latency.RecordRequest(string(candidate.Route.ID), ttftMs, durationMilliseconds(timer.Elapsed()))
			g.health.RouteSuccess(string(candidate.Route.ID))
			return true, nil
		}
	}
}

func (g *Gateway) failAttemptWithBudget(ctx context.Context, candidate router.Candidate, timer *latency.Timer, ttftMs float64) error {
	err := failoverBudgetError()
	g.recordAttemptFailure(ctx, candidate, timer, ttftMs, err)
	return err
}
