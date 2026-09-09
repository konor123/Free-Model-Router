package matcher

import (
	"testing"

	"github.com/konor123/Free-Model-Router/internal/model"
)

func TestExactMatchUsesCanonicalIdentity(t *testing.T) {
	pm := model.ProviderModel{
		ID:           "opencode/gpt-4o",
		CanonicalKey: "gpt-4o",
		UpstreamID:   "gpt-4o-latest",
	}
	got := Match(pm, []BenchmarkModel{{
		SourceModelID:     "aa-gpt-4o",
		CanonicalModelKey: "gpt-4o",
		Name:              "GPT-4o",
	}})
	if got.MatchMethod != MatchExact || got.Confidence != ExactConfidence {
		t.Fatalf("exact binding = %+v", got)
	}
	if got.SourceModelID != "aa-gpt-4o" || got.CanonicalModelKey != "gpt-4o" {
		t.Fatalf("exact binding identity = %+v", got)
	}
}

func TestMatchUniqueRejectsEqualStrengthCandidates(t *testing.T) {
	pm := model.ProviderModel{ID: "opencode/kimi-k2.5-free", UpstreamID: "kimi-k2.5-free"}
	_, ok := MatchUnique(pm, []BenchmarkModel{
		{SourceModelID: "kimi-a", Name: "Kimi-K2.5"},
		{SourceModelID: "kimi-b", Name: "Kimi K2.5"},
	})
	if ok {
		t.Fatal("ambiguous benchmark aliases must not bind automatically")
	}
}

func TestFamilyMatchStripsDeploymentDate(t *testing.T) {
	pm := model.ProviderModel{
		ID:         "openrouter/gpt-4o-2024-08-06",
		UpstreamID: "gpt-4o-2024-08-06",
	}
	got := Match(pm, []BenchmarkModel{{
		SourceModelID: "aa-gpt-4o",
		Name:          "gpt-4o-2024-05-13",
	}})
	if got.MatchMethod != MatchFamily {
		t.Fatalf("family binding = %+v", got)
	}
	if got.Confidence != FamilyConfidence {
		t.Fatalf("family confidence = %v, want %v", got.Confidence, FamilyConfidence)
	}
}

func TestVariantMatchHasReducedConfidence(t *testing.T) {
	pm := model.ProviderModel{
		ID:         "ollama/llama-3.1-8b-instruct",
		UpstreamID: "llama-3.1-8b-instruct",
	}
	got := Match(pm, []BenchmarkModel{{
		SourceModelID: "aa-llama-3.1-8b",
		Name:          "llama-3.1-8b",
	}})
	if got.MatchMethod != MatchVariant {
		t.Fatalf("variant binding = %+v", got)
	}
	if got.Confidence <= 0 || got.Confidence >= FamilyConfidence {
		t.Fatalf("variant confidence = %v, want between zero and family confidence", got.Confidence)
	}
}

func TestShortBaseVariantMatchHasReducedConfidence(t *testing.T) {
	pm := model.ProviderModel{
		ID:         "openai/gpt-4o-mini",
		UpstreamID: "gpt-4o-mini",
	}
	got := Match(pm, []BenchmarkModel{{
		SourceModelID: "aa-gpt-4o",
		Name:          "gpt-4o",
	}})
	if got.MatchMethod != MatchVariant || got.Confidence != VariantConfidence {
		t.Fatalf("short-base variant binding = %+v", got)
	}
}

func TestAliasMismatchDoesNotCreateBinding(t *testing.T) {
	pm := model.ProviderModel{
		ID:         "provider/unrelated-model",
		UpstreamID: "unrelated-model",
	}
	got := Match(pm, []BenchmarkModel{{
		SourceModelID: "aa-gpt-4o",
		Name:          "gpt-4o",
		Aliases:       []string{"chatgpt-4o", "gpt4o"},
	}})
	if got.MatchMethod != MatchNone || got.SourceModelID != "" || got.Confidence != 0 {
		t.Fatalf("mismatched binding = %+v", got)
	}
}

func TestSourceModelIDIsNotAnImplicitProviderAlias(t *testing.T) {
	pm := model.ProviderModel{
		ID:         "openrouter/aa-gpt-4o",
		UpstreamID: "unrelated-model",
	}
	got := Match(pm, []BenchmarkModel{{
		SourceModelID: "aa-gpt-4o",
		Name:          "gpt-4o",
	}})
	if got.MatchMethod != MatchNone {
		t.Fatalf("source ID collision created binding = %+v", got)
	}
}

func TestProviderNamespaceIsNotAnImplicitBenchmarkAlias(t *testing.T) {
	pm := model.ProviderModel{
		ID:         "openrouter/gpt-4o",
		UpstreamID: "unrelated-model",
	}
	got := Match(pm, []BenchmarkModel{{
		SourceModelID: "aa-gpt-4o",
		Name:          "openrouter/gpt-4o",
	}})
	if got.MatchMethod != MatchNone {
		t.Fatalf("provider namespace collision created binding = %+v", got)
	}
}

func TestDivergentSizeVariantsDoNotBind(t *testing.T) {
	pm := model.ProviderModel{
		ID:         "ollama/llama-3.1-8b-instruct",
		UpstreamID: "llama-3.1-8b-instruct",
	}
	got := Match(pm, []BenchmarkModel{{
		SourceModelID: "aa-llama-3.1-70b-instruct",
		Name:          "llama-3.1-70b-instruct",
	}})
	if got.MatchMethod != MatchNone {
		t.Fatalf("different model sizes created binding = %+v", got)
	}
}

func TestMatchAllReturnsStableBindings(t *testing.T) {
	models := []model.ProviderModel{
		{ID: "p/b", UpstreamID: "model-b"},
		{ID: "p/a", UpstreamID: "model-a"},
	}
	benchmarks := []BenchmarkModel{
		{SourceModelID: "aa-a", Name: "model-a"},
		{SourceModelID: "aa-b", Name: "model-b"},
	}
	first := MatchAll(models, benchmarks)
	second := MatchAll(models, benchmarks)
	if first["p/a"] != second["p/a"] || first["p/b"] != second["p/b"] {
		t.Fatalf("bindings changed between runs: first=%v second=%v", first, second)
	}
}
