package model

import "testing"

func TestSameCanonicalDifferentProviderDifferentID(t *testing.T) {
	a, err := NewProviderModelID("opencode", "mimo-v2.5")
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewProviderModelID("openrouter", "mimo-v2.5")
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatalf("expected different ProviderModelIDs, got %q == %q", a, b)
	}
}

func TestSameProviderDifferentVariantDifferentID(t *testing.T) {
	a, err := NewProviderModelID("opencode", "glm-5.3-flash")
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewProviderModelID("opencode", "glm-5.3-flash:free")
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatalf("variants must have different ProviderModelIDs, got %q == %q", a, b)
	}
}

func TestSameProviderModelDifferentRouteDifferentRouteID(t *testing.T) {
	pmid, _ := NewProviderModelID("opencode", "mimo-v2.5")
	r1, err := NewRouteID("opencode-public", "mimo-v2.5")
	if err != nil {
		t.Fatal(err)
	}
	r2, err := NewRouteID("opencode-zen", "mimo-v2.5")
	if err != nil {
		t.Fatal(err)
	}
	if r1 == r2 {
		t.Fatalf("different routes must have different RouteIDs: %q", r1)
	}
	_ = pmid
}

func TestProviderModelIDParsing(t *testing.T) {
	id, err := NewProviderModelID("openrouter", "mimo-v2.5")
	if err != nil {
		t.Fatal(err)
	}
	prov, mdl, err := id.Parse()
	if err != nil {
		t.Fatal(err)
	}
	if prov != "openrouter" || mdl != "mimo-v2.5" {
		t.Fatalf("parse mismatch: %q / %q", prov, mdl)
	}
}

func TestProviderModelIDRejectsPathSeparatorsInModel(t *testing.T) {
	if _, err := NewProviderModelID("opencode", "../etc/passwd"); err == nil {
		t.Fatal("expected error for path separators in model segment")
	}
}

func TestCanonicalKeyValidation(t *testing.T) {
	if _, err := NewCanonicalModelKey("mimo-v2.5"); err != nil {
		t.Fatalf("valid key rejected: %v", err)
	}
	if _, err := NewCanonicalModelKey(""); err == nil {
		t.Fatal("empty key accepted")
	}
	if _, err := NewCanonicalModelKey("BAD KEY!"); err == nil {
		t.Fatal("invalid key accepted")
	}
}

func TestRouteIDRejectsSeparatorInModelPart(t *testing.T) {
	if _, err := NewRouteID("opencode-public", "a::b"); err == nil {
		t.Fatal("expected error for '::' inside model part")
	}
}
