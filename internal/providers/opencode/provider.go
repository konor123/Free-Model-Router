package opencode

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/konor123/Free-Model-Router/internal/model"
	"github.com/konor123/Free-Model-Router/internal/provider"
)

// DefaultHTTPTimeout bounds catalog and non-streaming completion requests when
// callers do not provide a request context with an earlier deadline.
const DefaultHTTPTimeout = 2 * time.Minute

// Provider implements provider.Provider for OpenCode Public.
type Provider struct {
	// BaseURL of the OpenAI-compatible endpoint. Settable for tests.
	BaseURL string
	// AuthBaseOverride overrides the Zen auth base URL (tests only).
	AuthBaseOverride string
	// HTTP client used for all calls.
	HTTP              *http.Client
	resolveCredential ResolveCredential
	catalogFilter     CatalogFilter
	nativeFreeOnly    bool
}

// ResolveCredential retrieves an instance-local Zen credential immediately
// before an authenticated request. It must not retain plaintext credentials.
type ResolveCredential func() (string, error)

// CatalogFilter decides whether a discovered model belongs in the catalog.
type CatalogFilter func(id string, pricing map[string]json.RawMessage) bool

type option func(*Provider)

// WithCredentialResolver configures the native Zen credential source.
func WithCredentialResolver(resolve ResolveCredential) option {
	return func(p *Provider) { p.resolveCredential = resolve }
}

// WithCatalogFilter applies an endpoint-specific catalog policy after parsing.
func WithCatalogFilter(filter CatalogFilter) option {
	return func(p *Provider) { p.catalogFilter = filter }
}

// New builds a Provider with sane defaults.
func New(baseURL string, options ...option) *Provider {
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	p := &Provider{
		BaseURL: baseURL,
		// Stream lifetimes are governed by the caller's context. A client-wide
		// timeout would terminate valid long-running SSE responses.
		HTTP:           &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		nativeFreeOnly: strings.TrimRight(baseURL, "/") == AuthBaseURL,
	}
	for _, apply := range options {
		apply(p)
	}
	return p
}

// catalogResponse mirrors GET /models on an OpenAI-compatible endpoint.
type catalogResponse struct {
	Data []catalogEntry `json:"data"`
}

type catalogEntry struct {
	ID      string `json:"id"`
	Object  string `json:"object,omitempty"`
	OwnedBy string `json:"owned_by,omitempty"`

	ContextLength int                        `json:"context_length,omitempty"`
	MaxOutput     int                        `json:"max_output_tokens,omitempty"`
	Streaming     *bool                      `json:"streaming,omitempty"`
	Tools         *bool                      `json:"tools,omitempty"`
	Vision        *bool                      `json:"vision,omitempty"`
	Structured    *bool                      `json:"structured_output,omitempty"`
	Reasoning     *bool                      `json:"reasoning,omitempty"`
	Pricing       map[string]json.RawMessage `json:"pricing,omitempty"`
}

// UnmarshalJSON accepts vendor-specific metadata shapes while preserving the
// common OpenAI-compatible model identity. Unsupported metadata stays unknown
// instead of rejecting the provider's entire catalog.
func (e *catalogEntry) UnmarshalJSON(data []byte) error {
	var raw struct {
		ID, Object, OwnedBy                             string
		ContextLength, MaxOutput                        json.RawMessage
		Streaming, Tools, Vision, Structured, Reasoning json.RawMessage
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	_ = json.Unmarshal(fields["id"], &raw.ID)
	_ = json.Unmarshal(fields["object"], &raw.Object)
	_ = json.Unmarshal(fields["owned_by"], &raw.OwnedBy)
	raw.ContextLength, raw.MaxOutput = fields["context_length"], fields["max_output_tokens"]
	raw.Streaming, raw.Tools = fields["streaming"], fields["tools"]
	raw.Vision, raw.Structured, raw.Reasoning = fields["vision"], fields["structured_output"], fields["reasoning"]
	var pricing map[string]json.RawMessage
	_ = json.Unmarshal(fields["pricing"], &pricing)
	*e = catalogEntry{ID: raw.ID, Object: raw.Object, OwnedBy: raw.OwnedBy, ContextLength: optionalCatalogInt(raw.ContextLength), MaxOutput: optionalCatalogInt(raw.MaxOutput), Streaming: optionalCatalogBool(raw.Streaming), Tools: optionalCatalogBool(raw.Tools), Vision: optionalCatalogBool(raw.Vision), Structured: optionalCatalogBool(raw.Structured), Reasoning: optionalCatalogBool(raw.Reasoning), Pricing: pricing}
	return nil
}

func optionalCatalogBool(raw json.RawMessage) *bool {
	var value bool
	if len(raw) == 0 || json.Unmarshal(raw, &value) != nil {
		return nil
	}
	return &value
}

func optionalCatalogInt(raw json.RawMessage) int {
	var value int
	_ = json.Unmarshal(raw, &value)
	return value
}

// DiscoverModels fetches /models and normalizes into ProviderModels.
func (p *Provider) DiscoverModels(ctx context.Context) (*model.CatalogSnapshot, error) {
	requestCtx, cancel := context.WithTimeout(ctx, DefaultHTTPTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, strings.TrimRight(p.BaseURL, "/")+"/models", nil)
	if err != nil {
		return nil, fmt.Errorf("build catalog request: %w", err)
	}
	resp, err := p.HTTP.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, provider.NewFailureError(model.NewFailure(model.FailureCanceled, model.ScopeRequest), ctx.Err())
		}
		if requestCtx.Err() == context.DeadlineExceeded || errors.Is(err, context.DeadlineExceeded) {
			return nil, provider.NewFailureError(model.NewFailure(model.FailureTimeout, model.ScopeProvider), err)
		}
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
		if ctx.Err() != nil {
			return nil, provider.NewFailureError(model.NewFailure(model.FailureCanceled, model.ScopeRequest), ctx.Err())
		}
		if requestCtx.Err() == context.DeadlineExceeded || errors.Is(err, context.DeadlineExceeded) {
			return nil, provider.NewFailureError(model.NewFailure(model.FailureTimeout, model.ScopeProvider), err)
		}
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
		if p.catalogFilter != nil && !p.catalogFilter(e.ID, e.Pricing) {
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
	if p.nativeFreeOnly {
		for id, pm := range snap.Models {
			if !isVerifiedAnonymousFree(pm.UpstreamID) {
				delete(snap.Models, id)
			}
		}
	}
	return snap, nil
}

// Routes builds routes for one native OpenCode model. Public and Zen routes
// are limited to the same verified-free model policy.
func (p *Provider) Routes(pm model.ProviderModel) ([]model.ProviderRoute, error) {
	if p.nativeFreeOnly && !isVerifiedAnonymousFree(pm.UpstreamID) {
		return nil, nil
	}
	routes := []model.ProviderRoute{PublicRoute(pm.ID, pm.UpstreamID, pm.Base)}
	if p.hasCredential() {
		routes = append(routes, AuthRoute(pm.ID, pm.UpstreamID))
	}
	return routes, nil
}

func (p *Provider) hasCredential() bool {
	if p.resolveCredential != nil {
		key, err := p.resolveCredential()
		return err == nil && strings.TrimSpace(key) != ""
	}
	return AuthKey() != ""
}

func (p *Provider) credential() (string, error) {
	if p.resolveCredential != nil {
		return p.resolveCredential()
	}
	return AuthKey(), nil
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
