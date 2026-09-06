package app

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/konor123/Free-Model-Router/internal/config"
	"github.com/konor123/Free-Model-Router/internal/control"
	"github.com/konor123/Free-Model-Router/internal/logging"
	"github.com/konor123/Free-Model-Router/internal/providers/opencode"
)

func TestRunWithProviderDoesNotLogAuthenticationTokens(t *testing.T) {
	t.Setenv(config.ManagementTokenKey, "management-log-secret")
	t.Setenv(opencode.AuthRouteEnv, "")
	backend := newControlBackend(t)
	defer backend.Close()
	var logs bytes.Buffer
	log, err := logging.New(logging.Debug, &logs)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := &config.Config{Bind: freeBindAddress(t), ManagementBind: freeBindAddress(t), LogLevel: "debug"}
	errCh := make(chan error, 1)
	go func() { errCh <- RunWithProvider(ctx, cfg, log, opencode.New(backend.URL)) }()
	client := control.NewClient(cfg.ManagementBind, "management-log-secret")
	var status control.StatusResponse
	waitForControl(t, client, "/_fmr/status", &status)
	cancel()
	select {
	case runErr := <-errCh:
		if runErr != nil {
			t.Fatal(runErr)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("app did not stop")
	}
	if strings.Contains(logs.String(), "management-log-secret") || strings.Contains(logs.String(), "inference-token") {
		t.Fatalf("authentication token leaked to logs: %s", logs.String())
	}
}

func TestRunWithProviderRequiresInferenceTokenForWildcardBind(t *testing.T) {
	t.Setenv(config.ManagementTokenKey, "management-token")
	t.Setenv(config.InferenceTokenKey, "inference-token")
	t.Setenv(opencode.AuthRouteEnv, "")
	backend := newControlBackend(t)
	defer backend.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	log, err := logging.New(logging.Error, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Bind: freeWildcardAddress(t), ManagementBind: freeBindAddress(t), LogLevel: "error"}
	errCh := make(chan error, 1)
	go func() { errCh <- RunWithProvider(ctx, cfg, log, opencode.New(backend.URL)) }()

	_, port, err := net.SplitHostPort(cfg.Bind)
	if err != nil {
		t.Fatal(err)
	}
	baseURL := "http://127.0.0.1:" + port
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		response, requestErr := http.Get(baseURL + "/v1/models")
		if requestErr == nil {
			body, _ := io.ReadAll(response.Body)
			_ = response.Body.Close()
			if response.StatusCode == http.StatusUnauthorized {
				if requestErr := checkInferenceToken(t, baseURL); requestErr != nil {
					t.Fatal(requestErr)
				}
				cancel()
				select {
				case runErr := <-errCh:
					if runErr != nil {
						t.Fatalf("app shutdown error: %v", runErr)
					}
				case <-time.After(3 * time.Second):
					t.Fatal("app did not stop")
				}
				return
			}
			if response.StatusCode != http.StatusOK {
				t.Fatalf("wildcard inference response = %d %s", response.StatusCode, body)
			}
		} else {
			select {
			case runErr := <-errCh:
				t.Fatalf("app exited before wildcard listener became ready: %v", runErr)
			default:
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("wildcard inference server did not become ready")
}

func checkInferenceToken(t *testing.T, baseURL string) error {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, baseURL+"/v1/models", nil)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer inference-token")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return &statusError{code: response.StatusCode}
	}
	return nil
}

type statusError struct{ code int }

func (e *statusError) Error() string { return "unexpected HTTP status" }

func freeWildcardAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return address
}
