package opencode

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/konor123/Free-Model-Router/internal/model"
	"github.com/konor123/Free-Model-Router/internal/provider"
)

// TestAuthAndPublicRoutesTargetDifferentEndpoints verifies the wire-level
// route isolation: the two routes hit different base URLs and the auth route
// sends the Bearer header.
func TestAuthAndPublicRoutesTargetDifferentEndpoints(t *testing.T) {
	t.Setenv(AuthRouteEnv, "secret-key")

	var publicAuthHeader, zenAuthHeader atomic.Value
	var zenHits, publicHits atomic.Int32

	publicSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		publicHits.Add(1)
		publicAuthHeader.Store(r.Header.Get("Authorization"))
		w.Write([]byte(`{"choices":[{"delta":"public-ok","finish_reason":"stop"}]}`))
	}))
	defer publicSrv.Close()

	zenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		zenHits.Add(1)
		zenAuthHeader.Store(r.Header.Get("Authorization"))
		w.Write([]byte(`{"choices":[{"delta":"zen-ok","finish_reason":"stop"}]}`))
	}))
	defer zenSrv.Close()

	p := New(publicSrv.URL)
	p.AuthBaseOverride = zenSrv.URL
	pmid := mustID(t, "opencode", "mimo-v2.5")

	req := provider.NormalizedRequest{Messages: []provider.Message{{Role: "user", Content: "hi"}}}

	// Public route call.
	if _, err := p.ChatCompletion(context.Background(), PublicRoute(pmid, "mimo-v2.5", model.Capabilities{}), req); err != nil {
		t.Fatal(err)
	}
	// Zen auth route call.
	if _, err := p.ChatCompletion(context.Background(), AuthRoute(pmid, "mimo-v2.5"), req); err != nil {
		t.Fatal(err)
	}

	if publicHits.Load() != 1 || zenHits.Load() != 1 {
		t.Fatalf("each endpoint must receive exactly one call: public=%d zen=%d", publicHits.Load(), zenHits.Load())
	}
	if got := publicAuthHeader.Load().(string); got != "" {
		t.Fatalf("public route must not send credentials, got %q", got)
	}
	if got := zenAuthHeader.Load().(string); got != "Bearer secret-key" {
		t.Fatalf("zen route must send bearer, got %q", got)
	}
}

// TestAuth401DoesNotTouchPublic verifies a Zen 401 leaves the public route
// fully usable (route state isolation, PLAN_V7 §9).
func TestAuth401DoesNotAffectPublic(t *testing.T) {
	t.Setenv(AuthRouteEnv, "bad-key")

	publicSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"choices":[{"delta":"public-ok","finish_reason":"stop"}]}`))
	}))
	defer publicSrv.Close()

	zenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
	}))
	defer zenSrv.Close()

	p := New(publicSrv.URL)
	p.AuthBaseOverride = zenSrv.URL
	pmid := mustID(t, "opencode", "mimo-v2.5")
	req := provider.NormalizedRequest{Messages: []provider.Message{{Role: "user", Content: "hi"}}}

	// Auth route fails with credential-scoped failure.
	_, authErr := p.ChatCompletion(context.Background(), AuthRoute(pmid, "mimo-v2.5"), req)
	var fe *provider.FailureError
	if authErr == nil || !asFailureError(authErr, &fe) || fe.Failure.Class != model.FailureAuth {
		t.Fatalf("expected auth failure on zen route, got %v", authErr)
	}
	if fe.Failure.Scope != model.ScopeCredential {
		t.Fatalf("auth failure scope should be credential, got %q", fe.Failure.Scope)
	}

	// Public route still works afterwards.
	stream, err := p.ChatCompletion(context.Background(), PublicRoute(pmid, "mimo-v2.5", model.Capabilities{}), req)
	if err != nil {
		t.Fatalf("public route must be unaffected: %v", err)
	}
	ev, err := stream.Next(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ev.DeltaText != "public-ok" {
		t.Fatalf("unexpected content %q", ev.DeltaText)
	}
}

func asFailureError(err error, target **provider.FailureError) bool {
	if fe, ok := err.(*provider.FailureError); ok {
		*target = fe
		return true
	}
	return false
}
