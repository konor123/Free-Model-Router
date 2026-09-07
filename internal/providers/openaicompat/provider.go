// Package openaicompat adapts a configured OpenAI-compatible endpoint without
// leaking credentials into the model catalog or route identities.
package openaicompat

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/konor123/Free-Model-Router/internal/model"
	"github.com/konor123/Free-Model-Router/internal/provider"
	"github.com/konor123/Free-Model-Router/internal/providers/opencode"
)

// ResolveCredential returns the current credential for this provider instance.
// It is deliberately injected so the adapter never owns persisted secret state.
type ResolveCredential func() (string, error)

// Provider exposes one configured OpenAI-compatible endpoint as a namespaced
// provider.Provider. It reuses the protocol-normalization implementation from
// the OpenCode public adapter, while transport authentication is instance-local.
type Provider struct {
	id      string
	backend *opencode.Provider
}

// New validates a provider instance and constructs a no-redirect HTTP client.
func New(id, baseURL string, resolve ResolveCredential) (*Provider, error) {
	if _, err := model.NewProviderModelID(id, "model"); err != nil {
		return nil, fmt.Errorf("provider id: %w", err)
	}
	if strings.TrimSpace(baseURL) == "" {
		return nil, fmt.Errorf("base URL must not be empty")
	}
	client := &http.Client{Transport: bearerTransport{base: http.DefaultTransport, resolve: resolve}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return &Provider{id: id, backend: &opencode.Provider{BaseURL: strings.TrimRight(baseURL, "/"), HTTP: client}}, nil
}

// DiscoverModels remaps the backend's fixed opencode namespace to the stable
// configured instance namespace while preserving upstream IDs.
func (p *Provider) DiscoverModels(ctx context.Context) (*model.CatalogSnapshot, error) {
	snapshot, err := p.backend.DiscoverModels(ctx)
	if err != nil {
		return nil, err
	}
	out := &model.CatalogSnapshot{CreatedAt: snapshot.CreatedAt, Models: make(map[model.ProviderModelID]model.ProviderModel, len(snapshot.Models))}
	for _, pm := range snapshot.Models {
		_, segment, err := pm.ID.Parse()
		if err != nil {
			return nil, fmt.Errorf("backend returned invalid model id %q", pm.ID)
		}
		id, err := model.NewProviderModelID(p.id, segment)
		if err != nil {
			return nil, err
		}
		pm.ID = id
		if _, exists := out.Models[id]; exists {
			return nil, fmt.Errorf("provider model ID collision for %q", id)
		}
		out.Models[id] = pm
	}
	return out, nil
}

// ChatCompletion forwards a validated configured route to the compatible wire adapter.
func (p *Provider) ChatCompletion(ctx context.Context, route model.ProviderRoute, req provider.NormalizedRequest) (provider.ChatStream, error) {
	if route.Provider != p.id {
		return nil, fmt.Errorf("route belongs to %q, not %q", route.Provider, p.id)
	}
	return p.backend.ChatCompletion(ctx, route, req)
}

type bearerTransport struct {
	base    http.RoundTripper
	resolve ResolveCredential
}

func (t bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.resolve == nil {
		return t.base.RoundTrip(req)
	}
	key, err := t.resolve()
	if err != nil {
		return nil, fmt.Errorf("resolve provider credential: %w", err)
	}
	if strings.TrimSpace(key) == "" {
		return t.base.RoundTrip(req)
	}
	clone := req.Clone(req.Context())
	clone.Header = req.Header.Clone()
	clone.Header.Set("Authorization", "Bearer "+key)
	return t.base.RoundTrip(clone)
}
