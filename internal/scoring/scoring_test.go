package scoring

import (
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
	"time"

	"github.com/konor123/Free-Model-Router/internal/matcher"
)

func TestPerformanceUsesWeightsAndNeutralMissingMetrics(t *testing.T) {
	metrics := Metrics{
		Capability:   Float(80),
		Intelligence: Float(60),
	}
	want := 0.55*80 + 0.30*UnknownScore + 0.15*60
	if got := Performance(metrics); got != want {
		t.Fatalf("performance = %v, want %v", got, want)
	}
	if !HasPerformanceData(metrics) {
		t.Fatal("partial metrics should be marked as available")
	}
}

func TestConfidencePullsPerformanceTowardNeutral(t *testing.T) {
	got := Score(Metrics{
		Capability:   Float(90),
		Availability: Float(90),
		Intelligence: Float(90),
	}, 0.25, 50)
	if got.Performance != 90 {
		t.Fatalf("raw performance = %v, want 90", got.Performance)
	}
	if got.EffectivePerformance != 60 {
		t.Fatalf("effective performance = %v, want 60", got.EffectivePerformance)
	}
	if got.RoutingScore != 57 {
		t.Fatalf("routing score = %v, want 57", got.RoutingScore)
	}
}

func TestUnknownPerformanceDefaultsToNeutral(t *testing.T) {
	got := Score(Metrics{}, 1, 80)
	if got.HasPerformanceData {
		t.Fatal("empty metrics must not be marked as available")
	}
	if got.Performance != UnknownScore || got.EffectivePerformance != UnknownScore {
		t.Fatalf("unknown performance result = %+v", got)
	}
	if got.RoutingScore != 59 {
		t.Fatalf("unknown routing score = %v, want 59", got.RoutingScore)
	}
}

func TestPercentilesAreStableAndDoNotMutateInput(t *testing.T) {
	values := []float64{10, 20, 30}
	original := append([]float64(nil), values...)
	wantHigher := []float64{0, 50, 100}
	if got := Percentiles(values, true); !reflect.DeepEqual(got, wantHigher) {
		t.Fatalf("higher-is-better percentiles = %v, want %v", got, wantHigher)
	}
	wantLower := []float64{100, 50, 0}
	if got := Percentiles(values, false); !reflect.DeepEqual(got, wantLower) {
		t.Fatalf("lower-is-better percentiles = %v, want %v", got, wantLower)
	}
	if !reflect.DeepEqual(values, original) {
		t.Fatalf("percentile calculation mutated input: %v", values)
	}
	if first, second := Percentiles([]float64{10, 10, 30}, true), Percentiles([]float64{10, 10, 30}, true); !reflect.DeepEqual(first, second) {
		t.Fatalf("tied percentile output is unstable: %v vs %v", first, second)
	}
}

func TestPercentileIgnoresInvalidAndNegativeSamples(t *testing.T) {
	population := []float64{-1, math.NaN(), math.Inf(1), 10, 20}
	if got := Percentile(20, population, true); got != 100 {
		t.Fatalf("percentile with invalid samples = %v, want 100", got)
	}
	if got := Percentile(-1, population, true); got != UnknownScore {
		t.Fatalf("negative value percentile = %v, want %v", got, UnknownScore)
	}
}

func TestScoreBindingUsesSnapshotMetricsAndConfidence(t *testing.T) {
	snapshot := Snapshot{
		Version:     "2026-09-06",
		Source:      "artificial-analysis",
		RetrievedAt: time.Unix(100, 0).UTC(),
		Models: map[string]Metrics{
			"aa-gpt-4o": {
				Capability:   Float(80),
				Availability: Float(70),
				Intelligence: Float(60),
			},
		},
	}
	binding := matcher.BenchmarkBinding{
		CanonicalModelKey: "gpt-4o",
		SourceModelID:     "aa-gpt-4o",
		Confidence:        0.5,
		MatchMethod:       matcher.MatchFamily,
	}
	got := ScoreBinding(snapshot, binding, 40)
	if got.Performance != 74 {
		t.Fatalf("binding performance = %v, want 74", got.Performance)
	}
	if got.EffectivePerformance != 62 {
		t.Fatalf("binding effective performance = %v, want 62", got.EffectivePerformance)
	}
	if got.RoutingScore != 55.4 {
		t.Fatalf("binding routing score = %v, want 55.4", got.RoutingScore)
	}
}

func TestScoreBindingUnknownModelFallsBackToNeutral(t *testing.T) {
	got := ScoreBinding(Snapshot{}, matcher.BenchmarkBinding{SourceModelID: "missing", Confidence: 1}, 70)
	if got.HasPerformanceData || got.Performance != UnknownScore || got.EffectivePerformance != UnknownScore {
		t.Fatalf("missing binding score = %+v", got)
	}
}

func TestCacheRoundTripPreservesMetadataAndMetrics(t *testing.T) {
	cache := NewCache(filepath.Join(t.TempDir(), "aa", "snapshot.json"), time.Hour)
	want := Snapshot{
		Version:     "v1",
		Source:      "artificial-analysis",
		RetrievedAt: time.Unix(1234, 0).UTC(),
		Benchmarks: []matcher.BenchmarkModel{{
			SourceModelID: "aa-model",
			Name:          "model",
		}},
		Models: map[string]Metrics{
			"aa-model": {Capability: Float(88), Availability: Float(77)},
		},
	}
	if err := cache.Save(want); err != nil {
		t.Fatal(err)
	}
	got, err := cache.LoadLastKnownGood()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("cache round trip = %+v, want %+v", got, want)
	}
	info, err := os.Stat(cache.Path())
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("cache permissions = %o, want 600", info.Mode().Perm())
	}
}

func TestCacheUsesStaleLastKnownGoodWhenSourceUnavailable(t *testing.T) {
	now := time.Unix(10_000, 0).UTC()
	cache := NewCache(filepath.Join(t.TempDir(), "snapshot.json"), time.Hour)
	want := Snapshot{
		Version:     "v1",
		Source:      "artificial-analysis",
		RetrievedAt: now.Add(-2 * time.Hour),
		Models:      map[string]Metrics{"aa-model": {Intelligence: Float(91)}},
	}
	if err := cache.Save(want); err != nil {
		t.Fatal(err)
	}
	resolution := cache.Resolve(func() (Snapshot, error) {
		return Snapshot{}, errors.New("AA unavailable")
	}, now)
	if resolution.Source != SourceCache || !resolution.UsedCache || !resolution.Stale {
		t.Fatalf("stale cache resolution = %+v", resolution)
	}
	if !reflect.DeepEqual(resolution.Snapshot, want) {
		t.Fatalf("stale snapshot = %+v, want %+v", resolution.Snapshot, want)
	}
}

func TestUnavailableSourceWithoutCacheReturnsNeutralSnapshot(t *testing.T) {
	cache := NewCache(filepath.Join(t.TempDir(), "missing.json"), time.Hour)
	resolution := cache.Resolve(func() (Snapshot, error) {
		return Snapshot{}, errors.New("AA unavailable")
	}, time.Unix(10_000, 0).UTC())
	if resolution.Source != SourceDefault || resolution.UsedCache || resolution.Snapshot.Models == nil {
		t.Fatalf("empty cache resolution = %+v", resolution)
	}
	got := ScoreBinding(resolution.Snapshot, matcher.BenchmarkBinding{SourceModelID: "missing", Confidence: 1}, 50)
	if got.Performance != UnknownScore || got.EffectivePerformance != UnknownScore {
		t.Fatalf("empty cache score = %+v", got)
	}
}

func TestCacheReplacesPreviousSnapshotAtomically(t *testing.T) {
	cache := NewCache(filepath.Join(t.TempDir(), "snapshot.json"), time.Hour)
	first := Snapshot{Version: "v1", Source: "aa", RetrievedAt: time.Unix(1, 0).UTC(), Models: map[string]Metrics{"first": {Capability: Float(1)}}}
	second := Snapshot{Version: "v2", Source: "aa", RetrievedAt: time.Unix(2, 0).UTC(), Models: map[string]Metrics{"second": {Capability: Float(2)}}}
	if err := cache.Save(first); err != nil {
		t.Fatal(err)
	}
	if err := cache.Save(second); err != nil {
		t.Fatal(err)
	}
	got, err := cache.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, second) {
		t.Fatalf("replaced cache = %+v, want %+v", got, second)
	}
}

func TestCacheRecoversBackupAfterInterruptedReplacement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "snapshot.json")
	want := Snapshot{
		Version:     "v1",
		Source:      "aa",
		RetrievedAt: time.Unix(1, 0).UTC(),
		Models:      map[string]Metrics{"recovered": {Capability: Float(9)}},
	}
	data, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".bak", data, 0o600); err != nil {
		t.Fatal(err)
	}

	cache := NewCache(path, time.Hour)
	got, err := cache.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("recovered cache = %+v, want %+v", got, want)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("backup was not restored to cache path: %v", err)
	}
}

func TestSnapshotCloneDoesNotShareMetricPointers(t *testing.T) {
	snapshot := Snapshot{Models: map[string]Metrics{"m": {Capability: Float(10)}}}
	clone := snapshot.Clone()
	*clone.Models["m"].Capability = 20
	if *snapshot.Models["m"].Capability != 10 {
		t.Fatal("snapshot clone shares metric pointer")
	}
}

func TestInvalidSnapshotDoesNotBecomeLiveCache(t *testing.T) {
	cache := NewCache(filepath.Join(t.TempDir(), "snapshot.json"), time.Hour)
	resolution := cache.Resolve(func() (Snapshot, error) {
		return Snapshot{Models: map[string]Metrics{}}, nil
	}, time.Unix(10_000, 0).UTC())
	if resolution.Source != SourceDefault || resolution.LiveError == nil {
		t.Fatalf("invalid live snapshot resolution = %+v", resolution)
	}
	if _, err := cache.Load(); err == nil {
		t.Fatal("invalid live snapshot must not be persisted")
	}
}

func TestSnapshotValidationRejectsEmptyModelIDs(t *testing.T) {
	snapshot := Snapshot{Version: "v1", Source: "aa", RetrievedAt: time.Unix(1, 0).UTC(), Models: map[string]Metrics{"": {}}}
	if err := snapshot.Validate(); err == nil {
		t.Fatal("expected empty benchmark model id validation error")
	}
}
