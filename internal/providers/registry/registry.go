// Package registry dispatches model discovery and requests to configured
// provider instances without exposing credentials or falling back across owners.
package registry

import (
	"context"
	"fmt"
	"sort"

	"github.com/konor123/Free-Model-Router/internal/model"
	"github.com/konor123/Free-Model-Router/internal/provider"
)

// RouteBuilder produces executable routes for a discovered model.
type RouteBuilder func(model.ProviderModel) ([]model.ProviderRoute, error)

// Entry is one immutable configured provider instance.
type Entry struct {
	ID      string
	Backend provider.Provider
	Routes  RouteBuilder
}

// Registry implements provider.Provider for several isolated provider instances.
type Registry struct {
	entries map[string]Entry
}

// New validates and indexes immutable provider entries.
func New(entries []Entry) (*Registry, error) {
	r := &Registry{entries: make(map[string]Entry, len(entries))}
	for _, entry := range entries {
		if entry.ID == "" || entry.Backend == nil || entry.Routes == nil {
			return nil, fmt.Errorf("provider entry %q is incomplete", entry.ID)
		}
		if _, exists := r.entries[entry.ID]; exists {
			return nil, fmt.Errorf("duplicate provider entry %q", entry.ID)
		}
		r.entries[entry.ID] = entry
	}
	if len(r.entries) == 0 {
		return nil, fmt.Errorf("provider registry has no entries")
	}
	return r, nil
}

// DiscoverRoutedCatalog merges all entry catalogs and validates route ownership.
func (r *Registry) DiscoverRoutedCatalog(ctx context.Context) (*provider.RoutedCatalog, error) {
	ids := make([]string, 0, len(r.entries))
	for id := range r.entries {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := &provider.RoutedCatalog{Snapshot: &model.CatalogSnapshot{Models: make(map[model.ProviderModelID]model.ProviderModel)}, Routes: make(map[model.ProviderModelID][]model.ProviderRoute)}
	for _, id := range ids {
		entry := r.entries[id]
		snapshot, err := entry.Backend.DiscoverModels(ctx)
		if err != nil {
			return nil, fmt.Errorf("discover %s: %w", id, err)
		}
		if snapshot == nil {
			return nil, fmt.Errorf("discover %s: nil catalog", id)
		}
		if snapshot.CreatedAt.After(out.Snapshot.CreatedAt) {
			out.Snapshot.CreatedAt = snapshot.CreatedAt
		}
		for modelID, pm := range snapshot.Models {
			if pm.ID != modelID {
				return nil, fmt.Errorf("provider %s returned model %q with mismatched ID %q", id, modelID, pm.ID)
			}
			owner, _, err := modelID.Parse()
			if err != nil || owner != id {
				return nil, fmt.Errorf("provider %s returned model %q outside its namespace", id, modelID)
			}
			if _, exists := out.Snapshot.Models[modelID]; exists {
				return nil, fmt.Errorf("duplicate model %q", modelID)
			}
			routes, err := entry.Routes(pm)
			if err != nil {
				return nil, fmt.Errorf("routes for %s: %w", modelID, err)
			}
			if len(routes) == 0 {
				return nil, fmt.Errorf("provider %s returned no routes for %s", id, modelID)
			}
			for _, route := range routes {
				if route.ModelID != modelID || route.Provider != id {
					return nil, fmt.Errorf("route %q does not belong to %s", route.ID, modelID)
				}
			}
			out.Snapshot.Models[modelID] = pm
			out.Routes[modelID] = routes
		}
	}
	return out, nil
}

// DiscoverModels is the compatibility projection for callers that do not need routes.
func (r *Registry) DiscoverModels(ctx context.Context) (*model.CatalogSnapshot, error) {
	catalog, err := r.DiscoverRoutedCatalog(ctx)
	if err != nil {
		return nil, err
	}
	return catalog.Snapshot, nil
}

// ChatCompletion dispatches only to the provider named by the selected route.
func (r *Registry) ChatCompletion(ctx context.Context, route model.ProviderRoute, req provider.NormalizedRequest) (provider.ChatStream, error) {
	entry, ok := r.entries[route.Provider]
	if !ok {
		return nil, fmt.Errorf("unknown route provider %q", route.Provider)
	}
	owner, _, err := route.ModelID.Parse()
	if err != nil || owner != route.Provider {
		return nil, fmt.Errorf("route %q has invalid ownership", route.ID)
	}
	return entry.Backend.ChatCompletion(ctx, route, req)
}
