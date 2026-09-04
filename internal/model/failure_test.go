package model

import "testing"

func TestCanceledFailureForbidsFallback(t *testing.T) {
	f := NewFailure(FailureCanceled, ScopeRequest)
	if f.Retryable {
		t.Fatal("canceled must not be retryable")
	}
	if f.FallbackAllowed() {
		t.Fatal("canceled must forbid fallback")
	}
	if err := f.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
}

func TestBadRequestNotRetryableOnSameModel(t *testing.T) {
	f := NewFailure(FailureBadRequest, ScopeRoute)
	if f.Retryable {
		t.Fatal("bad request must not be retryable")
	}
}

func TestAuthFailureRetryableWithCredentialScope(t *testing.T) {
	f := NewFailure(FailureAuth, ScopeCredential)
	if !f.Retryable {
		t.Fatal("auth failure should allow fallback excluding same credential")
	}
}

func TestRateLimitedFallbackAllowed(t *testing.T) {
	f := NewFailure(FailureRateLimited, ScopeRoute)
	if !f.FallbackAllowed() || !f.Retryable {
		t.Fatal("429 must allow fallback via alternate route")
	}
}

func TestFailureValidationRejectsBadScope(t *testing.T) {
	f := Failure{Class: FailureTimeout, Scope: "Galaxy"}
	if err := f.Validate(); err == nil {
		t.Fatal("invalid scope accepted")
	}
}

func TestAllFailureClassesDefaulted(t *testing.T) {
	classes := []FailureClass{
		FailureCanceled, FailureTimeout, FailureRateLimited, FailureAuth,
		FailureBadRequest, FailureServerError, FailureProtocol, FailureNetwork, FailureUnknown,
	}
	scopes := []FailureScope{ScopeRequest, ScopeModel, ScopeRoute, ScopeCredential, ScopeProvider}
	for _, c := range classes {
		for _, s := range scopes {
			f := NewFailure(c, s)
			if err := f.Validate(); err != nil {
				t.Fatalf("NewFailure(%s,%s) invalid: %v", c, s, err)
			}
		}
	}
}
