// Package matcher binds provider model identities to external benchmark records.
// Matching is deliberately conservative: exact canonical identities win, family
// and variant matches carry reduced confidence, and weak aliases do not bind.
package matcher

import (
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/konor123/Free-Model-Router/internal/model"
)

// MatchMethod explains how a benchmark record was selected.
type MatchMethod string

const (
	MatchNone    MatchMethod = "none"
	MatchExact   MatchMethod = "exact"
	MatchFamily  MatchMethod = "family"
	MatchVariant MatchMethod = "variant"
)

const (
	// ExactConfidence is reserved for an exact canonical or normalized identity.
	ExactConfidence = 1.0
	// FamilyConfidence is used when deployment/date suffixes are the only mismatch.
	FamilyConfidence = 0.75
	// VariantConfidence is used for a conservative shared-prefix variant match.
	VariantConfidence = 0.50
)

// BenchmarkModel is one record from a benchmark source such as Artificial
// Analysis. SourceModelID should be the source's stable model identifier.
type BenchmarkModel struct {
	SourceModelID     string                  `json:"sourceModelId"`
	CanonicalModelKey model.CanonicalModelKey `json:"canonicalModelKey,omitempty"`
	Name              string                  `json:"name,omitempty"`
	Aliases           []string                `json:"aliases,omitempty"`
}

// BenchmarkBinding connects a provider model to a benchmark record.
type BenchmarkBinding struct {
	CanonicalModelKey model.CanonicalModelKey `json:"canonicalModelKey"`
	SourceModelID     string                  `json:"sourceModelId"`
	Confidence        float64                 `json:"confidence"`
	MatchMethod       MatchMethod             `json:"matchMethod"`
}

// Matcher controls confidence assigned to non-exact matches. The default is
// intentionally conservative and can be used directly for all providers.
type Matcher struct {
	ExactConfidence   float64
	FamilyConfidence  float64
	VariantConfidence float64
}

// New returns a matcher with the Phase 9 confidence defaults.
func New() Matcher {
	return Matcher{
		ExactConfidence:   ExactConfidence,
		FamilyConfidence:  FamilyConfidence,
		VariantConfidence: VariantConfidence,
	}
}

// Match binds one provider model to the strongest compatible benchmark record.
// An empty binding with MatchMethod MatchNone means that no safe match exists.
func Match(pm model.ProviderModel, benchmarks []BenchmarkModel) BenchmarkBinding {
	return New().Match(pm, benchmarks)
}

// MatchUnique returns the strongest compatible binding only when it is unique.
// Benchmark ingestion uses this stricter variant so a lexical tie can never
// silently assign one leaderboard record to the wrong provider model.
func MatchUnique(pm model.ProviderModel, benchmarks []BenchmarkModel) (BenchmarkBinding, bool) {
	return New().MatchUnique(pm, benchmarks)
}

// MatchUnique applies the normal confidence policy but rejects any equal-rank
// candidate. Existing Match retains deterministic tie breaking for callers
// that need the historical behaviour.
func (m Matcher) MatchUnique(pm model.ProviderModel, benchmarks []BenchmarkModel) (BenchmarkBinding, bool) {
	best := BenchmarkBinding{MatchMethod: MatchNone}
	bestRank := -1
	ambiguous := false
	for _, benchmark := range benchmarks {
		if strings.TrimSpace(benchmark.SourceModelID) == "" {
			continue
		}
		method, rank := matchMethod(pm, benchmark)
		if rank < 0 || rank < bestRank {
			continue
		}
		binding := BenchmarkBinding{CanonicalModelKey: bindingKey(pm, benchmark), SourceModelID: benchmark.SourceModelID, Confidence: m.confidence(method), MatchMethod: method}
		if rank > bestRank {
			best, bestRank, ambiguous = binding, rank, false
			continue
		}
		if binding.SourceModelID != best.SourceModelID {
			ambiguous = true
		}
	}
	return best, bestRank >= 0 && !ambiguous
}

// Match performs a single match using this matcher's confidence policy.
func (m Matcher) Match(pm model.ProviderModel, benchmarks []BenchmarkModel) BenchmarkBinding {
	best := BenchmarkBinding{MatchMethod: MatchNone}
	bestRank := -1
	for _, benchmark := range benchmarks {
		if strings.TrimSpace(benchmark.SourceModelID) == "" {
			continue
		}
		method, rank := matchMethod(pm, benchmark)
		if rank < bestRank {
			continue
		}
		if rank == -1 {
			continue
		}
		binding := BenchmarkBinding{
			CanonicalModelKey: bindingKey(pm, benchmark),
			SourceModelID:     benchmark.SourceModelID,
			Confidence:        m.confidence(method),
			MatchMethod:       method,
		}
		if rank > bestRank || betterTie(binding, best) {
			best = binding
			bestRank = rank
		}
	}
	return best
}

// MatchAll binds a list of provider models to a benchmark catalog. The result
// is keyed by the user-facing ProviderModelID.
func MatchAll(providerModels []model.ProviderModel, benchmarks []BenchmarkModel) map[model.ProviderModelID]BenchmarkBinding {
	result := make(map[model.ProviderModelID]BenchmarkBinding, len(providerModels))
	matcher := New()
	for _, pm := range providerModels {
		result[pm.ID] = matcher.Match(pm, benchmarks)
	}
	return result
}

func (m Matcher) confidence(method MatchMethod) float64 {
	value := 0.0
	switch method {
	case MatchExact:
		value = m.ExactConfidence
	case MatchFamily:
		value = m.FamilyConfidence
	case MatchVariant:
		value = m.VariantConfidence
	}
	if value < 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}

func betterTie(candidate, current BenchmarkBinding) bool {
	if current.MatchMethod == MatchNone {
		return true
	}
	if candidate.Confidence != current.Confidence {
		return candidate.Confidence > current.Confidence
	}
	return candidate.SourceModelID < current.SourceModelID
}

func matchMethod(pm model.ProviderModel, benchmark BenchmarkModel) (MatchMethod, int) {
	if pm.CanonicalKey != "" && benchmark.CanonicalModelKey != "" &&
		normalize(string(pm.CanonicalKey)) == normalize(string(benchmark.CanonicalModelKey)) {
		return MatchExact, 3
	}

	providerNames := providerNames(pm)
	benchmarkNames := benchmarkNames(benchmark)
	for _, left := range providerNames {
		for _, right := range benchmarkNames {
			if left != "" && left == right {
				return MatchExact, 3
			}
		}
	}

	for _, left := range providerNames {
		for _, right := range benchmarkNames {
			if left != "" && right != "" && familyKey(left) == familyKey(right) {
				return MatchFamily, 2
			}
		}
	}

	for _, left := range providerNames {
		for _, right := range benchmarkNames {
			if variantMatch(left, right) {
				return MatchVariant, 1
			}
		}
	}
	return MatchNone, -1
}

func bindingKey(pm model.ProviderModel, benchmark BenchmarkModel) model.CanonicalModelKey {
	if pm.CanonicalKey != "" {
		return pm.CanonicalKey
	}
	if benchmark.CanonicalModelKey != "" {
		return benchmark.CanonicalModelKey
	}
	for _, name := range providerNames(pm) {
		if key := familyKey(name); key != "" {
			return model.CanonicalModelKey(key)
		}
	}
	return ""
}

func providerNames(pm model.ProviderModel) []string {
	// ProviderModelID includes the provider namespace and is not a model alias.
	// Only the parsed model segment may participate in matching.
	values := []string{string(pm.CanonicalKey), pm.UpstreamID, pm.DisplayName}
	if _, segment, err := pm.ID.Parse(); err == nil {
		values = append(values, segment)
	}
	return normalizedUnique(values)
}

func benchmarkNames(benchmark BenchmarkModel) []string {
	// SourceModelID is retained as the benchmark record key, not treated as a
	// provider-facing alias. A coincidental source ID collision must not bind a
	// model without an explicit name, canonical key, or alias.
	values := []string{string(benchmark.CanonicalModelKey), benchmark.Name}
	values = append(values, benchmark.Aliases...)
	return normalizedUnique(values)
}

func normalizedUnique(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = normalize(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func normalize(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var b strings.Builder
	separator := false
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsNumber(r) || r == '.' {
			if separator && b.Len() > 0 {
				b.WriteByte('-')
			}
			b.WriteRune(r)
			separator = false
			continue
		}
		separator = true
	}
	return strings.Trim(b.String(), "-")
}

var (
	dateSuffixRe       = regexp.MustCompile(`(?:-\d{4}(?:-\d{2}){0,2})$`)
	deploymentSuffixRe = regexp.MustCompile(`(?:-(?:free|paid|preview|latest|it))+$`)
)

func familyKey(value string) string {
	value = normalize(value)
	for {
		trimmed := dateSuffixRe.ReplaceAllString(value, "")
		trimmed = deploymentSuffixRe.ReplaceAllString(trimmed, "")
		if trimmed == value {
			return value
		}
		value = trimmed
	}
}

func variantMatch(left, right string) bool {
	leftTokens := strings.Split(normalize(left), "-")
	rightTokens := strings.Split(normalize(right), "-")
	if len(leftTokens) < 2 || len(rightTokens) < 2 {
		return false
	}
	common := 0
	for common < len(leftTokens) && common < len(rightTokens) && leftTokens[common] == rightTokens[common] {
		common++
	}
	// A variant must be an explicit suffix of the other identifier. This keeps
	// size/tier changes such as 8b versus 70b from inheriting a benchmark score.
	shorter := len(leftTokens)
	if len(rightTokens) < shorter {
		shorter = len(rightTokens)
	}
	return common >= 2 && common == shorter && len(leftTokens) != len(rightTokens)
}

// StableBindings returns bindings ordered by ProviderModelID, useful when
// serializing a deterministic benchmark snapshot or diagnostic report.
func StableBindings(bindings map[model.ProviderModelID]BenchmarkBinding) []struct {
	ProviderModelID model.ProviderModelID
	Binding         BenchmarkBinding
} {
	keys := make([]model.ProviderModelID, 0, len(bindings))
	for key := range bindings {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	result := make([]struct {
		ProviderModelID model.ProviderModelID
		Binding         BenchmarkBinding
	}, 0, len(keys))
	for _, key := range keys {
		result = append(result, struct {
			ProviderModelID model.ProviderModelID
			Binding         BenchmarkBinding
		}{ProviderModelID: key, Binding: bindings[key]})
	}
	return result
}
