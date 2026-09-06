// Package scoring implements confidence-aware performance and routing scores.
// All scores are normalized to a 0..100 range so missing benchmark data can
// safely fall back to the neutral value instead of disabling routing.
package scoring

import (
	"math"
	"sort"

	"github.com/konor123/Free-Model-Router/internal/matcher"
)

const (
	// UnknownScore is the neutral score for missing or unusable performance data.
	UnknownScore = 50.0

	CapabilityWeight   = 0.55
	AvailabilityWeight = 0.30
	IntelligenceWeight = 0.15
	PerformanceWeight  = 0.70
	LatencyWeight      = 0.30
)

// Metrics are percentile-normalized benchmark metrics. A nil field means that
// the source did not provide that metric and contributes the neutral score.
type Metrics struct {
	Capability   *float64 `json:"capability,omitempty"`
	Availability *float64 `json:"availability,omitempty"`
	Intelligence *float64 `json:"intelligence,omitempty"`
}

// PerformanceMetrics is a descriptive alias for callers that prefer the longer
// name while keeping one JSON and calculation contract.
type PerformanceMetrics = Metrics

// Float makes it convenient to construct a known metric value in integrations
// and tests without taking the address of a temporary variable.
func Float(value float64) *float64 { return &value }

// Result contains the intermediate and final Phase 9 score values.
type Result struct {
	Performance          float64 `json:"performance"`
	EffectivePerformance float64 `json:"effectivePerformance"`
	Latency              float64 `json:"latency"`
	RoutingScore         float64 `json:"routingScore"`
	Confidence           float64 `json:"confidence"`
	HasPerformanceData   bool    `json:"hasPerformanceData"`
}

// Performance computes P = 0.55Cp + 0.30Ap + 0.15Ip. Every missing metric is
// replaced with UnknownScore, preserving partial benchmark records.
func Performance(metrics Metrics) float64 {
	capability, _ := metricOrNeutral(metrics.Capability)
	availability, _ := metricOrNeutral(metrics.Availability)
	intelligence, _ := metricOrNeutral(metrics.Intelligence)
	return clampScore(
		CapabilityWeight*capability +
			AvailabilityWeight*availability +
			IntelligenceWeight*intelligence,
	)
}

// HasPerformanceData reports whether at least one finite metric was supplied.
func HasPerformanceData(metrics Metrics) bool {
	_, capabilityOK := metricOrNeutral(metrics.Capability)
	_, availabilityOK := metricOrNeutral(metrics.Availability)
	_, intelligenceOK := metricOrNeutral(metrics.Intelligence)
	return capabilityOK || availabilityOK || intelligenceOK
}

// Effective applies P_effective = cP + (1-c)50.
func Effective(performance, confidence float64) float64 {
	if !finite(performance) {
		performance = UnknownScore
	}
	performance = clampScore(performance)
	confidence = clampUnit(confidence)
	return clampScore(confidence*performance + (1-confidence)*UnknownScore)
}

// Routing combines confidence-adjusted performance with a 0..100 latency score:
// R = 0.70P_effective + 0.30L.
func Routing(effectivePerformance, latencyScore float64) float64 {
	return clampScore(PerformanceWeight*clampScore(effectivePerformance) + LatencyWeight*clampScore(latencyScore))
}

// Score computes the complete confidence-aware routing score.
func Score(metrics Metrics, confidence, latencyScore float64) Result {
	hasData := HasPerformanceData(metrics)
	performance := Performance(metrics)
	if !hasData {
		confidence = 0
	}
	effective := Effective(performance, confidence)
	latencyScore = neutralIfNonFinite(latencyScore)
	return Result{
		Performance:          performance,
		EffectivePerformance: effective,
		Latency:              clampScore(latencyScore),
		RoutingScore:         Routing(effective, latencyScore),
		Confidence:           clampUnit(confidence),
		HasPerformanceData:   hasData,
	}
}

// ScoreBinding looks up a benchmark record by its stable source id. A missing
// binding or missing source record deliberately returns the neutral P=50 score.
func ScoreBinding(snapshot Snapshot, binding matcher.BenchmarkBinding, latencyScore float64) Result {
	if snapshot.Models == nil || binding.SourceModelID == "" {
		return Score(Metrics{}, 0, latencyScore)
	}
	metrics, ok := snapshot.Models[binding.SourceModelID]
	if !ok {
		return Score(Metrics{}, 0, latencyScore)
	}
	return Score(metrics, binding.Confidence, latencyScore)
}

// Percentile returns the stable mid-rank percentile of value in population.
// higherIsBetter controls whether larger raw values receive larger scores.
// A one-value or empty population has no comparative signal and returns 50.
func Percentile(value float64, population []float64, higherIsBetter bool) float64 {
	if !validSample(value) {
		return UnknownScore
	}
	clean := finiteValues(population)
	if len(clean) < 2 {
		return UnknownScore
	}
	sort.Float64s(clean)
	lower := sort.SearchFloat64s(clean, value)
	upper := sort.Search(len(clean), func(i int) bool { return clean[i] > value })
	if lower == upper {
		// The value is not present. Use its insertion rank, which keeps the
		// function useful for scoring an item against a reference population.
		upper = lower
	}
	rank := (float64(lower) + float64(upper) - 1) / 2
	percentile := 100 * rank / float64(len(clean)-1)
	if !higherIsBetter {
		percentile = 100 - percentile
	}
	return clampScore(percentile)
}

// PercentileRank is a descriptive alias for Percentile.
func PercentileRank(value float64, population []float64, higherIsBetter bool) float64 {
	return Percentile(value, population, higherIsBetter)
}

// Percentiles returns one stable percentile score per input value and never
// mutates the input slice.
func Percentiles(values []float64, higherIsBetter bool) []float64 {
	out := make([]float64, len(values))
	for i, value := range values {
		out[i] = Percentile(value, values, higherIsBetter)
	}
	return out
}

func metricOrNeutral(value *float64) (float64, bool) {
	if value == nil || !finite(*value) {
		return UnknownScore, false
	}
	return clampScore(*value), true
}

func finiteValues(values []float64) []float64 {
	clean := make([]float64, 0, len(values))
	for _, value := range values {
		if validSample(value) {
			clean = append(clean, value)
		}
	}
	return clean
}

func finite(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }

func validSample(value float64) bool { return finite(value) && value >= 0 }

func neutralIfNonFinite(value float64) float64 {
	if !finite(value) {
		return UnknownScore
	}
	return value
}

func clampUnit(value float64) float64 {
	if !finite(value) || value < 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}

func clampScore(value float64) float64 {
	if !finite(value) {
		return UnknownScore
	}
	if value < 0 {
		return 0
	}
	if value > 100 {
		return 100
	}
	return value
}
