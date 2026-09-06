package security_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/konor123/Free-Model-Router/internal/security"
)

func TestInferenceAuthOnlyRequiredForNonLoopbackBind(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })

	local, err := security.RequireInferenceAuth(next, "127.0.0.1:8787", "secret")
	if err != nil {
		t.Fatal(err)
	}
	localResponse := httptest.NewRecorder()
	local.ServeHTTP(localResponse, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	if localResponse.Code != http.StatusNoContent {
		t.Fatalf("loopback auth response = %d", localResponse.Code)
	}

	lan, err := security.RequireInferenceAuth(next, "0.0.0.0:8787", "secret")
	if err != nil {
		t.Fatal(err)
	}
	invalid := httptest.NewRecorder()
	lan.ServeHTTP(invalid, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	if invalid.Code != http.StatusUnauthorized {
		t.Fatalf("invalid LAN token response = %d, want 401", invalid.Code)
	}
	validRequest := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	validRequest.Header.Set("Authorization", "Bearer secret")
	valid := httptest.NewRecorder()
	lan.ServeHTTP(valid, validRequest)
	if valid.Code != http.StatusNoContent {
		t.Fatalf("valid LAN token response = %d", valid.Code)
	}
}

func TestManagementBindMustRemainLoopback(t *testing.T) {
	for _, bind := range []string{"0.0.0.0:8788", ":8788", "[::]:8788"} {
		if err := security.ValidateLoopbackBind(bind); err == nil {
			t.Fatalf("management bind %q was accepted", bind)
		}
	}
	for _, bind := range []string{"127.0.0.1:8788", "[::1]:8788", "localhost:8788"} {
		if err := security.ValidateLoopbackBind(bind); err != nil {
			t.Fatalf("loopback bind %q rejected: %v", bind, err)
		}
	}
}
