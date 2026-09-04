// Package provider defines the interfaces the router and gateway depend on.
// Provider implementations live under internal/providers and must only depend
// on this package plus internal/model (PLAN_V7 §4 dependency rule).
package provider

import (
	"context"
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
	// ToolCallDelta is set when the event carries tool-call fragments.
	ToolCallDelta bool
	// ReasoningDelta is set when the event carries reasoning fragments.
	ReasoningDelta bool
	// FinishReason is set on the final event ("stop", "tool_calls", ...).
	FinishReason string
}

// Semantic reports whether this event carries user-visible semantics.
// Heartbeats/comments/empty deltas are not semantic (PLAN_V7 §12).
func (e StreamEvent) Semantic() bool {
	return e.DeltaText != "" || e.ToolCallDelta || e.ReasoningDelta || e.FinishReason != ""
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
	Stream      bool                 `json:"stream"`
	MaxTokens   int                  `json:"maxTokens,omitempty"`
	Temperature *float64             `json:"temperature,omitempty"`
	Extensions  map[string]Extension `json:"-"`
}

// Message is one normalized chat message.
type Message struct {
	Role    string `json:"role"` // system | user | assistant | tool
	Content string `json:"content"`
}

// ToolSpec declares a tool the client exposes.
type ToolSpec struct {
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
