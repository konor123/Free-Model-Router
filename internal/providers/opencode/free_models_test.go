package opencode

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/konor123/Free-Model-Router/internal/model"
)

func TestNativeCatalogAndRoutesAllowOnlyVerifiedFreeModels(t *testing.T) {
	p := New(AuthBaseURL, WithCredentialResolver(func() (string, error) { return "stored-key", nil }))
	p.HTTP.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		body, _ := json.Marshal(catalogResponse{Data: []catalogEntry{
			{ID: "big-pickle"}, {ID: "mimo-v2.5-free"}, {ID: "gpt-5.5"},
			{ID: "deepseek-v4-flash-free"}, {ID: "muse-spark-1.2-contributor-free"},
		}})
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(body)))}, nil
	})

	snapshot, err := p.DiscoverModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Models) != 2 {
		t.Fatalf("native catalog models = %d, want only verified models", len(snapshot.Models))
	}
	for _, pm := range snapshot.Models {
		routes, err := p.Routes(pm)
		if err != nil || len(routes) != 2 {
			t.Fatalf("routes for %q = %+v, %v; want public and Zen", pm.UpstreamID, routes, err)
		}
		for _, route := range routes {
			if route.Access != model.AccessFree {
				t.Fatalf("route %q access = %q, want Free", route.ID, route.Access)
			}
		}
	}
}

func TestVerifiedAnonymousFreeRequiresExactRawID(t *testing.T) {
	for _, id := range []string{"big-pickle", "mimo-v2.5-free"} {
		if !isVerifiedAnonymousFree(id) {
			t.Fatalf("%q should be verified", id)
		}
	}
	for _, id := range []string{"BIG-PICKLE", " big-pickle", "big-pickle ", "big-pickle-free", "deepseek-v4-flash-free"} {
		if isVerifiedAnonymousFree(id) {
			t.Fatalf("%q must fail closed", id)
		}
	}
}
