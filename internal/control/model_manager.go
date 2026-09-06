package control

import (
	"sort"
	"strings"

	"github.com/konor123/Free-Model-Router/internal/model"
)

// ModelFilters is the stable UI-facing filter contract for the model manager.
type ModelFilters struct {
	Search     string
	Provider   string
	Access     string
	Status     string
	Capability string
}

// FilterModels applies model-manager filters without mutating the source slice.
// Supported statuses are selected, unselected, pinned, available, unavailable,
// and all. Capability filters use streaming, tools, vision, structured-output,
// or reasoning.
func FilterModels(models []ModelResponse, filters ModelFilters) []ModelResponse {
	search := strings.ToLower(strings.TrimSpace(filters.Search))
	provider := strings.ToLower(strings.TrimSpace(filters.Provider))
	access := normalizeFilter(filters.Access)
	status := normalizeFilter(filters.Status)
	capability := normalizeFilter(filters.Capability)
	out := make([]ModelResponse, 0, len(models))
	for _, candidate := range models {
		if search != "" && !modelSearchMatches(candidate, search) {
			continue
		}
		if provider != "" && !modelHasProvider(candidate, provider) {
			continue
		}
		if access != "" && !modelHasAccess(candidate, access) {
			continue
		}
		if status != "" && !modelHasStatus(candidate, status) {
			continue
		}
		if capability != "" && !modelHasAnyCapability(candidate, capability) {
			continue
		}
		copy := candidate
		copy.Routes = append([]RouteResponse(nil), candidate.Routes...)
		out = append(out, copy)
	}
	return out
}

func modelHasAnyCapability(candidate ModelResponse, capability string) bool {
	if modelHasCapability(candidate.Capabilities, capability) {
		return true
	}
	for _, route := range candidate.Routes {
		if modelHasCapability(route.Capabilities, capability) {
			return true
		}
	}
	return false
}

func modelSearchMatches(candidate ModelResponse, search string) bool {
	values := []string{candidate.ID, candidate.CanonicalKey, candidate.DisplayName, candidate.UpstreamID}
	for _, route := range candidate.Routes {
		values = append(values, route.Provider, route.ID, route.UpstreamModelID)
	}
	for _, value := range values {
		if strings.Contains(strings.ToLower(value), search) {
			return true
		}
	}
	return false
}

func modelHasProvider(candidate ModelResponse, provider string) bool {
	for _, route := range candidate.Routes {
		if strings.EqualFold(route.Provider, provider) {
			return true
		}
	}
	return false
}

func modelHasAccess(candidate ModelResponse, access string) bool {
	for _, route := range candidate.Routes {
		if normalizeFilter(string(route.Access)) == access {
			return true
		}
	}
	return false
}

func modelHasStatus(candidate ModelResponse, status string) bool {
	switch status {
	case "selected":
		return candidate.Selected
	case "unselected":
		return !candidate.Selected
	case "pinned":
		return candidate.Pinned
	case "available":
		for _, route := range candidate.Routes {
			if route.Health.Available && route.Enabled {
				return true
			}
		}
		return false
	case "unavailable":
		return !modelHasStatus(candidate, "available")
	case "all":
		return true
	default:
		return true
	}
}

func modelHasCapability(cap model.Capabilities, capability string) bool {
	switch capability {
	case "streaming", "stream":
		return cap.Streaming
	case "tools", "tool":
		return cap.Tools
	case "vision":
		return cap.Vision
	case "structured-output", "structuredoutput", "structured":
		return cap.StructuredOutput
	case "reasoning":
		return cap.Reasoning
	default:
		return false
	}
}

func normalizeFilter(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, "_", "-")
	value = strings.ReplaceAll(value, " ", "-")
	return value
}

// SortModelResponses gives desktop tables a deterministic display order.
func SortModelResponses(models []ModelResponse) {
	sort.SliceStable(models, func(i, j int) bool {
		return strings.ToLower(models[i].DisplayName+models[i].ID) < strings.ToLower(models[j].DisplayName+models[j].ID)
	})
}
