// Package provider defines the interfaces the router and gateway depend on.
// Provider implementations live under internal/providers and must only depend
// on this package plus internal/model (PLAN_V7 §4 dependency rule).
package provider

import (
	"context"
	"encoding/json"
	"io"

	"github.com/konor123/Free-Model-Router/internal/model"
)

// CatalogProvider discovers the models a provider currently offers.
type CatalogProvider interface {
	// DiscoverModels returns an immutable snapshot of the provider's models.
	DiscoverModels(ctx context.Context) (*model.CatalogSnapshot, error)
}

// ChatStream is the normalized streaming response from a provider.
type ChatStream interface {
	// Next returns the next semantic event, or io.EOF when the stream ends.
	Next(ctx context.Context) (StreamEvent, error)
	// Close releases upstream resources.
	Close() error
}

// StreamEvent is one semantic event from the provider.
type StreamEvent struct {
	// DeltaText is an incremental content delta ("" if not a text event).
	DeltaText string
	// ChoiceIndex identifies the response choice carrying this delta.
	ChoiceIndex int
	// ToolCalls preserves the complete upstream tool-call delta objects.
	ToolCalls []json.RawMessage
	// ReasoningContent is the incremental reasoning delta.
	ReasoningContent string
	// FinishReason is set on the final event ("stop", "tool_calls", ...).
	FinishReason string
}

// Semantic reports whether this event carries user-visible semantics.
// Heartbeats/comments/empty deltas are not semantic (PLAN_V7 §12).
func (e StreamEvent) Semantic() bool {
	return e.DeltaText != "" || len(e.ToolCalls) > 0 || e.ReasoningContent != "" || e.FinishReason != ""
}

// ChatProvider executes a chat completion against one route.
type ChatProvider interface {
	// ChatCompletion sends a normalized request to the given route and returns
	// a streaming response. Errors are returned as *model.Failure-wrapped types
	// via FailureError.
	ChatCompletion(ctx context.Context, route model.ProviderRoute, req NormalizedRequest) (ChatStream, error)
}

// Provider combines discovery and chat execution for one provider backend.
type Provider interface {
	CatalogProvider
	ChatProvider
}

// NormalizedRequest is the provider-agnostic chat request.
type NormalizedRequest struct {
	Messages    []Message            `json:"messages"`
	Tools       []ToolSpec           `json:"tools,omitempty"`
	ToolChoice  json.RawMessage      `json:"tool_choice,omitempty"`
	ResponseFormat json.RawMessage   `json:"response_format,omitempty"`
	Reasoning   json.RawMessage      `json:"reasoning,omitempty"`
	Stream      bool                 `json:"stream"`
	MaxTokens   int                  `json:"maxTokens,omitempty"`
	Temperature *float64             `json:"temperature,omitempty"`
	Extensions  map[string]Extension `json:"-"`
}

// Message is one normalized chat message.
type Message struct {
	Role       string          `json:"role"` // system | user | assistant | tool
	Content    any             `json:"content"`
	ToolCalls  []ToolCall      `json:"tool_calls,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
	Name       string          `json:"name,omitempty"`
}

// ContentPart is one supported multimodal content fragment.
type ContentPart struct {
	Type     string    `json:"type"`
	Text     string    `json:"text,omitempty"`
	ImageURL *ImageURL `json:"image_url,omitempty"`
}

type ImageURL struct {
	URL    string `json:"url"`
	Detail string `json:"detail,omitempty"`
}

// ToolCall is a complete assistant tool call or a streaming fragment.
type ToolCall struct {
	ID       string       `json:"id,omitempty"`
	Type     string       `json:"type,omitempty"`
	Function FunctionCall `json:"function"`
}

type FunctionCall struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

// ToolSpec declares a tool the client exposes.
type ToolSpec struct {
	Type        string `json:"type,omitempty"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Schema is the raw JSON schema for parameters.
	Schema string `json:"schema,omitempty"`
}

// Extension is a request parameter that may not be supported by every provider.
type Extension struct {
	Name  string
	Value any
}

// UnsupportedError reports a parameter the provider cannot handle.
// Providers must surface these explicitly, not silently drop (PLAN_V7 §6).
type UnsupportedError struct {
	Parameter string
	Reason    string
}

func (e *UnsupportedError) Error() string {
	return "unsupported parameter " + e.Parameter + ": " + e.Reason
}

// FailureError wraps a model.Failure as an error.
type FailureError struct {
	Failure model.Failure
	Cause   error
}

func (e *FailureError) Error() string {
	if e.Cause != nil {
		return string(e.Failure.Class) + ": " + e.Cause.Error()
	}
	return string(e.Failure.Class)
}

func (e *FailureError) Unwrap() error { return e.Cause }

// NewFailureError builds a FailureError.
func NewFailureError(f model.Failure, cause error) *FailureError {
	return &FailureError{Failure: f, Cause: cause}
}

// Ensure interfaces are usable.
var _ io.Closer = (ChatStream)(nil)
