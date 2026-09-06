// Package model defines the core identity types and domain entities (PLAN_V7 §2, §5).
//
// Identity hierarchy:
//
//	CanonicalModelKey  — benchmark/performance identity (e.g. "mimo-v2.5")
//	ProviderModelID    — user-facing model identity (e.g. "opencode/mimo-v2.5")
//	RouteID            — endpoint/auth path (e.g. "opencode-public::mimo-v2.5"), internal only
package model

import (
	"fmt"
	"regexp"
	"strings"
)

// CanonicalModelKey is the benchmark identity shared across providers.
type CanonicalModelKey string

// ProviderModelID is the user-visible model identity within one provider.
// Two variants of the same canonical model on the same provider have
// different ProviderModelIDs.
type ProviderModelID string

// RouteID identifies one concrete endpoint/auth path for a ProviderModel.
// RouteID is internal diagnostic identity and is never used as an external
// model= value (PLAN_V7 §2).
type RouteID string

var (
	keyRe    = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)
	provider = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)
)

// NewCanonicalModelKey validates and constructs a CanonicalModelKey.
func NewCanonicalModelKey(s string) (CanonicalModelKey, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return "", fmt.Errorf("canonical model key must not be empty")
	}
	if len(s) > 128 {
		return "", fmt.Errorf("canonical model key too long: %d chars", len(s))
	}
	if !keyRe.MatchString(s) {
		return "", fmt.Errorf("invalid canonical model key %q", s)
	}
	return CanonicalModelKey(s), nil
}

// NewProviderModelID validates and constructs a ProviderModelID of the form
// "<provider>/<model>". This is an internal/client-facing identifier, not the
// provider-native model identifier; use ProviderModel.UpstreamID for the latter.
// We enforce a single-level "<provider>/<model>" form to keep parsing unambiguous.
// Variants are expressed inside <model> using '.' or '-' (e.g. "glm-5.3-flash:free").
func NewProviderModelID(prov, mdl string) (ProviderModelID, error) {
	if !provider.MatchString(prov) {
		return "", fmt.Errorf("invalid provider segment %q", prov)
	}
	if mdl == "" {
		return "", fmt.Errorf("model segment must not be empty")
	}
	if strings.ContainsAny(mdl, "/\\") {
		return "", fmt.Errorf("model segment must not contain path separators: %q", mdl)
	}
	id := prov + "/" + mdl
	if len(id) > 256 {
		return "", fmt.Errorf("provider model id too long: %d chars", len(id))
	}
	return ProviderModelID(id), nil
}

// ParseProviderModelID splits a ProviderModelID into provider and model segments.
func (id ProviderModelID) Parse() (provider, model string, err error) {
	s := string(id)
	i := strings.IndexByte(s, '/')
	if i <= 0 || i == len(s)-1 {
		return "", "", fmt.Errorf("invalid ProviderModelID %q: want <provider>/<model>", s)
	}
	return s[:i], s[i+1:], nil
}

// NewRouteID validates and constructs a RouteID of the form "<route-name>::<model>".
func NewRouteID(routeName, mdl string) (RouteID, error) {
	if !provider.MatchString(routeName) {
		return "", fmt.Errorf("invalid route name %q", routeName)
	}
	if mdl == "" {
		return "", fmt.Errorf("route model part must not be empty")
	}
	if strings.Contains(mdl, "::") {
		return "", fmt.Errorf("route model part must not contain %q: %q", "::", mdl)
	}
	return RouteID(routeName + "::" + mdl), nil
}
