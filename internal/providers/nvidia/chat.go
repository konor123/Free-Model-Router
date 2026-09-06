package nvidia

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/konor123/Free-Model-Router/internal/model"
	"github.com/konor123/Free-Model-Router/internal/provider"
)

// chatRequest is the OpenAI-compatible chat completion payload.
type chatRequest struct {
	Model          string             `json:"model"`
	Messages       []provider.Message `json:"messages"`
	Stream         bool               `json:"stream,omitempty"`
	MaxTokens      int                `json:"max_tokens,omitempty"`
	Temperature    *float64           `json:"temperature,omitempty"`
	Tools          []chatTool         `json:"tools,omitempty"`
	ToolChoice     json.RawMessage    `json:"tool_choice,omitempty"`
	ResponseFormat json.RawMessage    `json:"response_format,omitempty"`
	Reasoning      json.RawMessage    `json:"reasoning,omitempty"`
}

type chatTool struct {
	Type     string       `json:"type"`
	Function chatToolFunc `json:"function"`
}

type chatToolFunc struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

// ChatCompletion posts the request to the hosted route and returns a ChatStream.
// All hosted routes share one credential: the NVIDIA_API_KEY bearer token.
func (p *Provider) ChatCompletion(ctx context.Context, route model.ProviderRoute, req provider.NormalizedRequest) (provider.ChatStream, error) {
	payload, err := buildPayload(route.UpstreamModelID, req)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(p.BaseURL, "/")+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return nil, provider.NewFailureError(model.NewFailure(model.FailureBadRequest, model.ScopeRequest), err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if key := APIKey(); key != "" {
		httpReq.Header.Set("Authorization", "Bearer "+key)
	}

	resp, err := p.HTTP.Do(httpReq)
	if err != nil {
		// Distinguish client cancellation from other network failures.
		if ctx.Err() != nil {
			return nil, provider.NewFailureError(model.NewFailure(model.FailureCanceled, model.ScopeRequest), ctx.Err())
		}
		return nil, provider.NewFailureError(model.NewFailure(model.FailureNetwork, model.ScopeRoute), err)
	}

	if resp.StatusCode == http.StatusTooManyRequests {
		drain(resp.Body)
		return nil, provider.NewFailureError(model.NewFailure(model.FailureRateLimited, model.ScopeRoute), errStatus(resp.StatusCode))
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		drain(resp.Body)
		return nil, provider.NewFailureError(model.NewFailure(model.FailureAuth, model.ScopeCredential), errStatus(resp.StatusCode))
	}
	if resp.StatusCode == http.StatusBadRequest {
		drain(resp.Body)
		return nil, provider.NewFailureError(model.NewFailure(model.FailureBadRequest, model.ScopeRoute), errStatus(resp.StatusCode))
	}
	if resp.StatusCode >= 500 {
		drain(resp.Body)
		return nil, provider.NewFailureError(model.NewFailure(model.FailureServerError, model.ScopeRoute), errStatus(resp.StatusCode))
	}
	if resp.StatusCode != http.StatusOK {
		drain(resp.Body)
		return nil, provider.NewFailureError(model.NewFailure(model.FailureProtocol, model.ScopeRoute), errStatus(resp.StatusCode))
	}

	if req.Stream {
		return &sseStream{sc: resp, rdr: bufio.NewReader(resp.Body)}, nil
	}

	// Non-streaming: parse whole body into one synthetic stream. The defer
	// guarantees drain/close on every error path below (OCR finding).
	defer drain(resp.Body)
	var out chatResponse
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, provider.NewFailureError(model.NewFailure(model.FailureProtocol, model.ScopeRoute), err)
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, provider.NewFailureError(model.NewFailure(model.FailureProtocol, model.ScopeRoute),
			fmt.Errorf("malformed chat response: %w", err))
	}
	if len(out.Choices) == 0 {
		return nil, provider.NewFailureError(model.NewFailure(model.FailureProtocol, model.ScopeRoute),
			fmt.Errorf("chat response has no choices"))
	}
	ev := provider.StreamEvent{DeltaText: out.Choices[0].Message.Content, FinishReason: out.Choices[0].FinishReason}
	ev.ReasoningContent = out.Choices[0].Message.ReasoningContent
	for _, toolCall := range out.Choices[0].Message.ToolCalls {
		raw, err := json.Marshal(toolCall)
		if err != nil {
			return nil, provider.NewFailureError(model.NewFailure(model.FailureProtocol, model.ScopeRoute), err)
		}
		ev.ToolCalls = append(ev.ToolCalls, raw)
	}
	return &oneShotStream{ev: ev, done: false}, nil
}

func buildPayload(mdl string, req provider.NormalizedRequest) ([]byte, error) {
	cr := chatRequest{
		Model:          mdl,
		Messages:       req.Messages,
		Stream:         req.Stream,
		MaxTokens:      req.MaxTokens,
		Temperature:    req.Temperature,
		ToolChoice:     req.ToolChoice,
		ResponseFormat: req.ResponseFormat,
		Reasoning:      req.Reasoning,
	}
	for _, t := range req.Tools {
		if t.Type != "" && t.Type != "function" {
			return nil, &provider.UnsupportedError{Parameter: "tools[].type", Reason: "only function tools are supported by the nvidia hosted route"}
		}
		cr.Tools = append(cr.Tools, chatTool{Type: "function", Function: chatToolFunc{
			Name: t.Name, Description: t.Description, Parameters: json.RawMessage(t.Schema),
		}})
	}
	for _, ext := range req.Extensions {
		// NVIDIA hosted NIM rejects unknown params explicitly (never silently drop).
		return nil, &provider.UnsupportedError{Parameter: ext.Name, Reason: "not supported by the nvidia hosted route"}
	}
	data, err := json.Marshal(cr)
	if err != nil {
		return nil, provider.NewFailureError(model.NewFailure(model.FailureBadRequest, model.ScopeRequest), err)
	}
	return data, nil
}

// chatResponse is the OpenAI-compatible non-streaming response.
type chatResponse struct {
	Choices []chatChoice `json:"choices"`
}

type chatChoice struct {
	Message      chatMessage `json:"message"`
	FinishReason string      `json:"finish_reason"`
}

type chatMessage struct {
	Content          string              `json:"content"`
	ToolCalls        []provider.ToolCall `json:"tool_calls"`
	ReasoningContent string              `json:"reasoning_content"`
}

func errStatus(code int) error { return fmt.Errorf("upstream status %d", code) }

func drain(body io.ReadCloser) {
	_, _ = io.Copy(io.Discard, io.LimitReader(body, 1<<20))
	_ = body.Close()
}

// sseStream iterates OpenAI-style SSE chunks.
type sseStream struct {
	sc   *http.Response
	rdr  *bufio.Reader
	done bool
}

type sseChunk struct {
	Choices []struct {
		Index int `json:"index"`
		Delta struct {
			Content          string            `json:"content"`
			ReasoningContent string            `json:"reasoning_content"`
			ToolCalls        []json.RawMessage `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
}

func (s *sseStream) Next(ctx context.Context) (provider.StreamEvent, error) {
	if s.done {
		return provider.StreamEvent{}, io.EOF
	}
	for {
		if err := ctx.Err(); err != nil {
			s.done = true
			return provider.StreamEvent{}, err
		}
		line, err := s.rdr.ReadString('\n')
		if err != nil {
			s.done = true
			if err == io.EOF {
				return provider.StreamEvent{}, io.EOF
			}
			if ctx.Err() != nil {
				return provider.StreamEvent{}, ctx.Err()
			}
			return provider.StreamEvent{}, provider.NewFailureError(
				model.NewFailure(model.FailureProtocol, model.ScopeRoute), err)
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" || strings.HasPrefix(line, ":") {
			continue // SSE comment / heartbeat: not semantic
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			s.done = true
			return provider.StreamEvent{}, io.EOF
		}
		var chunk sseChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			s.done = true
			return provider.StreamEvent{}, provider.NewFailureError(
				model.NewFailure(model.FailureProtocol, model.ScopeRoute), err)
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		c := chunk.Choices[0]
		ev := provider.StreamEvent{
			ChoiceIndex:      c.Index,
			DeltaText:        c.Delta.Content,
			ReasoningContent: c.Delta.ReasoningContent,
			FinishReason:     c.FinishReason,
		}
		if len(c.Delta.ToolCalls) > 0 {
			ev.ToolCalls = append(ev.ToolCalls, c.Delta.ToolCalls...)
		}
		if ev.Semantic() {
			return ev, nil
		}
		// Empty delta: not semantic, keep reading.
	}
}

func (s *sseStream) Close() error {
	if s.sc != nil && s.sc.Body != nil {
		s.sc.Body.Close()
	}
	return nil
}

// oneShotStream wraps a single parsed non-streaming response.
type oneShotStream struct {
	ev   provider.StreamEvent
	done bool
}

func (s *oneShotStream) Next(context.Context) (provider.StreamEvent, error) {
	if s.done {
		return provider.StreamEvent{}, io.EOF
	}
	s.done = true
	return s.ev, nil
}

func (s *oneShotStream) Close() error { return nil }
