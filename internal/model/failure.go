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
	Class     FailureClass `json:"class"`
	Scope     FailureScope `json:"scope"`
	Retryable bool         `json:"retryable"`
}

// NewFailure builds a Failure, deriving retryability from class/scope when unset.
func NewFailure(class FailureClass, scope FailureScope) Failure {
	return Failure{Class: class, Scope: scope, Retryable: defaultRetryable(class)}
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

// defaultRetryable encodes the Phase 7 fallback policy at type level:
//   - canceled: never retryable (fallback forbidden)
//   - bad request / protocol error: same model must not be retried
//   - auth: retryable only at credential-scope exclusion level
//   - rate limited: retryable via alternate route
//   - timeout: retryable within failover budget
//   - 5xx: retryable, but prefer next ProviderModel
func defaultRetryable(class FailureClass) bool {
	switch class {
	case FailureCanceled, FailureBadRequest, FailureProtocol:
		return false
	case FailureAuth, FailureRateLimited, FailureTimeout, FailureServerError, FailureNetwork, FailureUnknown:
		return true
	default:
		return false
	}
}

// FallbackAllowed reports whether another candidate may be tried after this failure.
func (f Failure) FallbackAllowed() bool {
	return f.Class != FailureCanceled
}
