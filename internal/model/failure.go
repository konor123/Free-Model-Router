package model

import (
	"fmt"
)

// FailureClass categorizes an upstream failure (PLAN_V7 §5, §11).
type FailureClass string

const (
	FailureCanceled     FailureClass = "Canceled"      // client disconnect / ctx canceled
	FailureTimeout      FailureClass = "Timeout"
	FailureRateLimited  FailureClass = "RateLimited"   // 429 / quota
	FailureAuth         FailureClass = "Auth"          // credential failure (401/403)
	FailureBadRequest   FailureClass = "BadRequest"    // 400 / unsupported / protocol
	FailureServerError  FailureClass = "ServerError"   // 5xx
	FailureProtocol     FailureClass = "Protocol"      // malformed upstream response
	FailureNetwork      FailureClass = "Network"
	FailureUnknown      FailureClass = "Unknown"
)

// FailureScope bounds the blast radius of a failure.
type FailureScope string

const (
	ScopeRequest    FailureScope = "Request"
	ScopeModel      FailureScope = "Model"
	ScopeRoute      FailureScope = "Route"
	ScopeCredential FailureScope = "Credential"
	ScopeProvider   FailureScope = "Provider"
)

// Failure is a classified upstream failure.
type Failure struct {
	Class FailureClass `json:"class"`
	Scope FailureScope `json:"scope"`
}

// NewFailure builds a classified failure. Routing policy is decided separately.
func NewFailure(class FailureClass, scope FailureScope) Failure {
	return Failure{Class: class, Scope: scope}
}

// Validate checks the failure is well-formed.
func (f Failure) Validate() error {
	if f.Class == "" {
		return fmt.Errorf("failure class must not be empty")
	}
	switch f.Scope {
	case ScopeRequest, ScopeModel, ScopeRoute, ScopeCredential, ScopeProvider:
	default:
		return fmt.Errorf("invalid failure scope %q", f.Scope)
	}
	return nil
}

// FailureAction is the sole routing-policy outcome for a classified failure.
type FailureAction string

const (
	ActionStop              FailureAction = "Stop"
	ActionNextRoute         FailureAction = "NextRoute"
	ActionNextModel         FailureAction = "NextModel"
	ActionDisableCredential FailureAction = "DisableCredential"
	ActionDisableRoute      FailureAction = "DisableRoute"
	ActionCooldownProvider  FailureAction = "CooldownProvider"
)

// DecideFailureAction centralizes the conservative pre-fallback policy.
// Request-scoped failures are client errors and must never be retried elsewhere.
func DecideFailureAction(f Failure) FailureAction {
	if f.Scope == ScopeRequest || f.Class == FailureCanceled || f.Class == FailureUnknown {
		return ActionStop
	}
	switch f.Class {
	case FailureAuth:
		if f.Scope == ScopeCredential {
			return ActionDisableCredential
		}
		return ActionStop
	case FailureRateLimited:
		if f.Scope == ScopeProvider {
			return ActionCooldownProvider
		}
		return ActionNextRoute
	case FailureTimeout, FailureNetwork:
		return ActionNextRoute
	case FailureServerError:
		return ActionNextModel
	case FailureProtocol:
		return ActionDisableRoute
	case FailureBadRequest:
		return ActionStop
	default:
		return ActionStop
	}
}

// HTTPStatus maps a terminal classified failure to the public API response.
// Candidate progression is always decided first by DecideFailureAction.
func HTTPStatus(f Failure) int {
	switch f.Class {
	case FailureRateLimited:
		return 429
	case FailureBadRequest:
		return 400
	default:
		return 502
	}
}
