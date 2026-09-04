package opencode

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/konor123/Free-Model-Router/internal/model"
	"github.com/konor123/Free-Model-Router/internal/provider"
)

// Provider implements provider.Provider for OpenCode Public.
type Provider struct {
	// BaseURL of the OpenAI-compatible endpoint. Settable for tests.
	BaseURL string
	// HTTP client used for all calls.
	HTTP *http.Client
	// DefaultAccess assigned to discovered models (Public route => Free).
	Access model.AccessClass
}

// New builds a Provider with sane defaults.
func New(baseURL string) *Provider {
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	return &Provider{
		BaseURL: baseURL,
		HTTP:    &http.Client{},
		Access:  model.AccessFree,
	}
}

// catalogResponse mirrors GET /models on an OpenAI-compatible endpoint.
type catalogResponse struct {
	Data []catalogEntry `json:"data"`
}

type catalogEntry struct {
	ID      string `json:"id"`
	Object  string `json:"object,omitempty"`
	OwnedBy string `json:"owned_by,omitempty"`

	ContextLength int  `json:"context_length,omitempty"`
	MaxOutput     int  `json:"max_output_tokens,omitempty"`
	Streaming     *bool `json:"streaming,omitempty"`
	Tools         *bool `json:"tools,omitempty"`
	Vision        *bool `json:"vision,omitempty"`
	Structured    *bool `json:"structured_output,omitempty"`
	Reasoning     *bool `json:"reasoning,omitempty"`
}

// DiscoverModels fetches /models and normalizes into ProviderModels.
func (p *Provider) DiscoverModels(ctx context.Context) (*model.CatalogSnapshot, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(p.BaseURL, "/")+"/models", nil)
	if err != nil {
		return nil, fmt.Errorf("build catalog request: %w", err)
	}
	resp, err := p.HTTP.Do(req)
	if err != nil {
		return nil, provider.NewFailureError(model.NewFailure(model.FailureNetwork, model.ScopeProvider), err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 500 {
		return nil, provider.NewFailureError(model.NewFailure(model.FailureServerError, model.ScopeProvider),
			fmt.Errorf("catalog endpoint returned %d", resp.StatusCode))
	}
	if resp.StatusCode != http.StatusOK {
		return nil, provider.NewFailureError(model.NewFailure(model.FailureProtocol, model.ScopeProvider),
			fmt.Errorf("catalog endpoint returned %d", resp.StatusCode))
	}

	var parsed catalogResponse
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, provider.NewFailureError(model.NewFailure(model.FailureProtocol, model.ScopeProvider), err)
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, provider.NewFailureError(model.NewFailure(model.FailureProtocol, model.ScopeProvider),
			fmt.Errorf("malformed catalog response: %w", err))
	}

	snap := &model.CatalogSnapshot{
		Revision: model.SnapshotRevision(time.Now().UnixNano()),
		Models:   map[model.ProviderModelID]model.ProviderModel{},
	}
	for _, e := range parsed.Data {
		if e.ID == "" {
			continue
		}
		pmid, err := model.NewProviderModelID(ProviderID, e.ID)
		if err != nil {
			continue // skip non-normalizable entries
		}
		key, _ := model.NewCanonicalModelKey(canonicalize(e.ID))
		pm := model.ProviderModel{
			ID:           pmid,
			CanonicalKey: key,
			DisplayName:  e.ID,
			Access:       p.Access,
			Base: model.Capabilities{
				Streaming:        boolOr(e.Streaming, true),
				Tools:            boolOr(e.Tools, false),
				Vision:           boolOr(e.Vision, false),
				StructuredOutput: boolOr(e.Structured, false),
				Reasoning:        boolOr(e.Reasoning, false),
				ContextLength:    e.ContextLength,
				MaxOutput:        e.MaxOutput,
			},
		}
		snap.Models[pmid] = pm
	}
	return snap, nil
}

// canonicalize maps a provider model id to a canonical benchmark key by
// stripping free-tier/variant suffixes.
func canonicalize(id string) string {
	s := id
	for _, suffix := range []string{":free", ":paid", "-free", "-it", "-preview"} {
		if strings.HasSuffix(strings.ToLower(s), suffix) {
			s = s[:len(s)-len(suffix)]
		}
	}
	return strings.ToLower(s)
}

func boolOr(b *bool, def bool) bool {
	if b == nil {
		return false
	}
	return *b
}
