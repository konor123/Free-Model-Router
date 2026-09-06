// Package router implements candidate eligibility and ranking.
// Phase 4 covers capability filtering: mismatched models are removed
// before any scoring happens (PLAN_V7 §8).
package router

import (
	"sort"

	"github.com/konor123/Free-Model-Router/internal/health"
	"github.com/konor123/Free-Model-Router/internal/model"
)

// Candidate is one routable (model, route) pair.
type Candidate struct {
	Model model.ProviderModel
	Route model.ProviderRoute
}

// EffectiveCaps computes the candidate's effective capabilities.
func (c Candidate) EffectiveCaps() model.Capabilities {
	return c.Route.EffectiveCapabilities(c.Model.Base)
}

// Exclusion explains why a candidate was filtered out.
type Exclusion struct {
	ID     model.ProviderModelID
	Reason string
}

// EligibilityResult separates eligible candidates from excluded ones.
type EligibilityResult struct {
	Eligible []Candidate
	Excluded []Exclusion
}

// FilterInput bundles everything eligibility needs.
type FilterInput struct {
	// ExplicitID is the pinned/external model id; it bypasses pool membership
	// but capability/access/health policies still apply (PLAN_V7 §3).
	ExplicitID model.ProviderModelID

	// Pool is the selected Model Pool.
	Pool []model.ProviderModelID

	// Catalog is the current snapshot.
	Catalog *model.CatalogSnapshot

	// Routes maps ProviderModelID to its enabled routes.
	Routes map[model.ProviderModelID][]model.ProviderRoute

	// Requirements is what the request needs.
	Reqs model.RequestRequirements

	// AllowPaid enables paid routes (explicit opt-in only).
	AllowPaid bool

	// AllowUnknown enables a controlled alternate-route lookup. Unknown access
	// remains excluded by default and should only be enabled for a route that is
	// already paired with an eligible model candidate.
	AllowUnknown bool

	// ProtocolCompatible optionally vetoes candidates whose protocol cannot
	// express the request (nil = no veto).
	ProtocolCompatible func(model.ProviderRoute) bool

	// Health optionally excludes cooling-down or quota-exhausted routes.
	Health *health.Manager
}

// Filter applies the eligibility chain:
//
//  1. explicit model intent  2. model pool  3. capability
//  4. protocol               5. access
//
// Capability mismatch is removed before scoring. Fallback callers must reuse
// the same FilterInput so every attempt keeps identical requirements.
func Filter(in FilterInput) EligibilityResult {
	var res EligibilityResult
	if in.Catalog == nil {
		return res
	}

	seen := map[model.ProviderModelID]bool{}
	for _, id := range ids(in) {
		if seen[id] {
			continue
		}
		seen[id] = true

		pm, ok := in.Catalog.Models[id]
		if !ok {
			res.Excluded = append(res.Excluded, Exclusion{ID: id, Reason: "not in catalog"})
			continue
		}

		routes, ok := in.Routes[id]
		if !ok || len(routes) == 0 {
			res.Excluded = append(res.Excluded, Exclusion{ID: id, Reason: "no enabled route"})
			continue
		}

		for _, route := range routes {
			if !route.Enabled {
				continue
			}
			caps := route.EffectiveCapabilities(pm.Base)
			if !caps.Supports(in.Reqs) {
				res.Excluded = append(res.Excluded, Exclusion{ID: id, Reason: "capability mismatch"})
				continue
			}
			if in.ProtocolCompatible != nil && !in.ProtocolCompatible(route) {
				res.Excluded = append(res.Excluded, Exclusion{ID: id, Reason: "protocol incompatible"})
				continue
			}
			if in.Health != nil && !in.Health.Available(string(route.ID)) {
				res.Excluded = append(res.Excluded, Exclusion{ID: id, Reason: "health unavailable"})
				continue
			}
			if !routeAccessAllowed(route.EffectiveAccess(), in.AllowPaid, in.AllowUnknown) {
				res.Excluded = append(res.Excluded, Exclusion{ID: id, Reason: "access not allowed"})
				continue
			}
			res.Eligible = append(res.Eligible, Candidate{Model: pm, Route: route})
		}
	}
	return res
}

func routeAccessAllowed(a model.AccessClass, allowPaid, allowUnknown bool) bool {
	if a.AutoRoutable() {
		return true
	}
	if a == model.AccessPaid {
		return allowPaid
	}
	if a == model.AccessUnknown {
		return allowUnknown
	}
	return false // Unclassified access never routes without an explicit policy.
}

// TTFTLookup returns the current routing latency estimate for a route.
// Returning ok=false means that no probe or request sample is available yet.
type TTFTLookup func(routeID string) (ms float64, ok bool)

// Rank returns a deterministic copy of candidates ordered by known TTFT,
// then ProviderModelID, then RouteID. Health and eligibility filtering happen
// before ranking, so unavailable routes are never resurrected by this helper.
func Rank(candidates []Candidate, ttft TTFTLookup) []Candidate {
	out := append([]Candidate(nil), candidates...)
	sort.SliceStable(out, func(i, j int) bool {
		left, leftOK := 0.0, false
		right, rightOK := 0.0, false
		if ttft != nil {
			left, leftOK = ttft(string(out[i].Route.ID))
			right, rightOK = ttft(string(out[j].Route.ID))
		}
		if leftOK != rightOK {
			return leftOK
		}
		if leftOK && left != right {
			return left < right
		}
		if out[i].Model.ID != out[j].Model.ID {
			return string(out[i].Model.ID) < string(out[j].Model.ID)
		}
		return string(out[i].Route.ID) < string(out[j].Route.ID)
	})
	return out
}

// ids resolves the candidate id list: explicit wins, else pool.
func ids(in FilterInput) []model.ProviderModelID {
	if in.ExplicitID != "" {
		return []model.ProviderModelID{in.ExplicitID}
	}
	return in.Pool
}
