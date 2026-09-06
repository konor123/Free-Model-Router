package model

import "testing"

func TestCanceledFailureForbidsFallback(t *testing.T) {
	f := NewFailure(FailureCanceled, ScopeRequest)
	if DecideFailureAction(f) != ActionStop {
		t.Fatal("canceled must stop")
	}
	if err := f.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
}

func TestBadRequestStops(t *testing.T) {
	f := NewFailure(FailureBadRequest, ScopeRoute)
	if DecideFailureAction(f) != ActionStop {
		t.Fatal("bad request must not fallback")
	}
}

func TestAuthFailureDisablesCredential(t *testing.T) {
	f := NewFailure(FailureAuth, ScopeCredential)
	if DecideFailureAction(f) != ActionDisableCredential {
		t.Fatal("auth failure should disable its credential")
	}
}

func TestRateLimitedMovesToNextRoute(t *testing.T) {
	f := NewFailure(FailureRateLimited, ScopeRoute)
	if DecideFailureAction(f) != ActionNextRoute {
		t.Fatal("429 must move to the next route")
	}
}

func TestFailureDecisionScopeAndClassMatrix(t *testing.T) {
	cases := []struct {
		failure Failure
		want    FailureAction
	}{
		{NewFailure(FailureProtocol, ScopeRoute), ActionDisableRoute},
		{NewFailure(FailureTimeout, ScopeRoute), ActionNextRoute},
		{NewFailure(FailureNetwork, ScopeProvider), ActionNextRoute},
		{NewFailure(FailureServerError, ScopeProvider), ActionNextModel},
		{NewFailure(FailureRateLimited, ScopeProvider), ActionCooldownProvider},
		{NewFailure(FailureUnknown, ScopeRoute), ActionStop},
		{NewFailure(FailureServerError, ScopeRequest), ActionStop},
	}
	for _, tc := range cases {
		if got := DecideFailureAction(tc.failure); got != tc.want {
			t.Fatalf("DecideFailureAction(%+v) = %s, want %s", tc.failure, got, tc.want)
		}
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
