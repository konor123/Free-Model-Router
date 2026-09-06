package xai

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/konor123/Free-Model-Router/internal/model"
	"github.com/konor123/Free-Model-Router/internal/provider"
)

// Provider implements provider.Provider for the xAI Grok catalog.
type Provider struct {
	// BaseURL of the OpenAI-compatible endpoint. Settable for tests.
	BaseURL string
	// HTTP client used for all calls.
	HTTP *http.Client
}

// New builds a Provider with sane defaults.
func New(baseURL string) *Provider {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return &Provider{
		BaseURL: baseURL,
		HTTP:    &http.Client{},
	}
}

// catalogResponse mirrors GET /v1/models on an OpenAI-compatible endpoint.
type catalogResponse struct {
	Data []catalogEntry `json:"data"`
}

type catalogEntry struct {
	ID      string `json:"id"`
	OwnedBy string `json:"owned_by,omitempty"`

	ContextLength int   `json:"context_length,omitempty"`
	MaxOutput     int   `json:"max_output_tokens,omitempty"`
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
	if key := APIKey(); key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
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

	now := time.Now().UTC()
	snap := &model.CatalogSnapshot{
		CreatedAt: now,
		Models:    map[model.ProviderModelID]model.ProviderModel{},
	}
	for _, e := range parsed.Data {
		if e.ID == "" {
			continue
		}
		pmid, err := model.NewProviderModelID(ProviderID, providerModelSegment(e.ID))
		if err != nil {
			continue // skip non-normalizable entries
		}
		key, _ := model.NewCanonicalModelKey(canonicalize(e.ID))
		base := model.Capabilities{
			Streaming:        boolOr(e.Streaming, true),
			Tools:            boolOr(e.Tools, false),
			Vision:           boolOr(e.Vision, false),
			StructuredOutput: boolOr(e.Structured, false),
			Reasoning:        boolOr(e.Reasoning, false),
			ContextLength:    e.ContextLength,
			MaxOutput:        e.MaxOutput,
		}
		// Streaming is guaranteed by the OpenAI-compatible route default. Other
		// feature bits remain unknown when the catalog omits their metadata.
		if e.Tools == nil {
			base.MarkUnknown(model.CapTools)
		}
		if e.Vision == nil {
			base.MarkUnknown(model.CapVision)
		}
		if e.Structured == nil {
			base.MarkUnknown(model.CapStructuredOutput)
		}
		if e.Reasoning == nil {
			base.MarkUnknown(model.CapReasoning)
		}
		pm := model.ProviderModel{
			ID:           pmid,
			CanonicalKey: key,
			DisplayName:  e.ID,
			UpstreamID:   e.ID,
			Base:         base,
		}
		if existing, exists := snap.Models[pmid]; exists && existing.UpstreamID != e.ID {
			return nil, provider.NewFailureError(model.NewFailure(model.FailureProtocol, model.ScopeProvider),
				fmt.Errorf("provider model ID collision for upstream IDs %q and %q", existing.UpstreamID, e.ID))
		}
		snap.Models[pmid] = pm
	}
	return snap, nil
}

// canonicalize maps a provider model id to a canonical benchmark key by
// stripping variant suffixes.
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
		return def
	}
	return *b
}

// providerModelSegment creates a stable slash-free internal segment while
// preserving the exact provider-native identifier separately on ProviderModel.
func providerModelSegment(upstreamID string) string {
	if !strings.ContainsAny(upstreamID, "/\\") {
		return upstreamID
	}
	return "b64-" + base64.RawURLEncoding.EncodeToString([]byte(upstreamID))
}
