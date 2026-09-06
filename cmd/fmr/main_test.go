package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/konor123/Free-Model-Router/internal/control"
)

func TestUsageListsAllPlanCommands(t *testing.T) {
	var output strings.Builder
	run([]string{"--help"}, &output, io.Discard)
	for _, command := range commandNames {
		if !strings.Contains(output.String(), command) {
			t.Fatalf("help omitted command %q: %s", command, output.String())
		}
	}
}

func TestSubcommandHelpSucceeds(t *testing.T) {
	var output strings.Builder
	if err := run([]string{"status", "-h"}, &output, &output); err != nil {
		t.Fatalf("subcommand help failed: %v", err)
	}
	if !strings.Contains(output.String(), "Usage of fmr status") {
		t.Fatalf("subcommand help = %s", output.String())
	}
}

func TestJSONFlagControlsResponseFormatting(t *testing.T) {
	var pretty, compact strings.Builder
	body := []byte(`{"status":"ok","nested":{"value":1}}`)
	if err := printBody(&pretty, body, false); err != nil {
		t.Fatal(err)
	}
	if err := printBody(&compact, body, true); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(pretty.String(), `{"status":"ok"}`) || !strings.Contains(pretty.String(), "\n  \"status\"") {
		t.Fatalf("pretty output = %q", pretty.String())
	}
	if compact.String() != string(body)+"\n" {
		t.Fatalf("compact output = %q", compact.String())
	}
}

func TestWaitForGatewayPollsUntilReady(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			http.Error(w, "starting", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"apiVersion":"1"}`))
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := waitForGateway(ctx, control.NewClient(server.URL, "token")); err != nil {
		t.Fatal(err)
	}
	if requests.Load() < 2 {
		t.Fatalf("gateway was polled %d time(s)", requests.Load())
	}
}

func TestStatusCommandUsesAuthenticatedControlClient(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/_fmr/status" || r.Header.Get("Authorization") != "Bearer cli-secret" {
			t.Fatalf("request = %s %q", r.URL.Path, r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"apiVersion":"1","instanceId":"test"}`))
	}))
	defer server.Close()

	var output strings.Builder
	if err := run([]string{"status", "-addr", server.URL, "-token", "cli-secret", "-json"}, &output, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `"apiVersion":"1"`) {
		t.Fatalf("status output = %s", output.String())
	}
}

func TestUnknownCommandFails(t *testing.T) {
	if err := run([]string{"not-a-command"}, io.Discard, io.Discard); err == nil {
		t.Fatal("unknown command unexpectedly succeeded")
	}
}
