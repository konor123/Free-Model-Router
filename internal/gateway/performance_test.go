package gateway

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/konor123/Free-Model-Router/internal/matcher"
	"github.com/konor123/Free-Model-Router/internal/model"
	"github.com/konor123/Free-Model-Router/internal/provider"
	"github.com/konor123/Free-Model-Router/internal/scoring"
)

func TestBenchmarkScoreChangesRuntimeCandidateSelection(t *testing.T) {
	p := &fallbackProvider{models: []string{"fast-model", "slow-model"}}
	p.behavior = func(_ context.Context, _ model.ProviderRoute, _ provider.NormalizedRequest) (provider.ChatStream, error) {
		return successFallbackStream("selected"), nil
	}
	g, err := NewGatewayWithFailoverPolicy(context.Background(), p, FailoverPolicy{MaxAttempts: 2, Budget: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := scoring.Snapshot{
		Version:     "v1",
		Source:      "test-benchmark",
		RetrievedAt: time.Unix(1, 0).UTC(),
		Benchmarks: []matcher.BenchmarkModel{
			{SourceModelID: "aa-fast", Name: "fast-model"},
			{SourceModelID: "aa-slow", Name: "slow-model"},
		},
		Models: map[string]scoring.Metrics{
			"aa-fast": {Capability: scoring.Float(10), Availability: scoring.Float(10), Intelligence: scoring.Float(10)},
			"aa-slow": {Capability: scoring.Float(100), Availability: scoring.Float(100), Intelligence: scoring.Float(100)},
		},
	}
	cache := scoring.NewCache(filepath.Join(t.TempDir(), "snapshot.json"), time.Hour)
	if err := cache.Save(snapshot); err != nil {
		t.Fatal(err)
	}
	resolution := g.ResolveBenchmarkSnapshot(cache, func() (scoring.Snapshot, error) {
		return scoring.Snapshot{}, errors.New("benchmark source unavailable")
	}, time.Unix(2, 0).UTC())
	if resolution.Source != scoring.SourceCache || !resolution.UsedCache {
		t.Fatalf("benchmark resolution = %+v", resolution)
	}
	if resolution.CacheError != nil {
		t.Fatal(resolution.CacheError)
	}

	w := doFallbackRequest(t, g, `{"model":"fmr/auto","messages":[{"role":"user","content":"x"}]}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "selected") {
		t.Fatalf("scored selection response = %d %s", w.Code, w.Body.String())
	}
	calls := p.Calls()
	if len(calls) == 0 || calls[0] != "opencode-public::slow-model" {
		t.Fatalf("scored selection calls = %v, want high-performance model first", calls)
	}
}
