package model

import (
	"fmt"
	"time"
)

// AccessClass is the authoritative access classification of a Route (PLAN_V7 §3).
type AccessClass string

const (
	AccessUnknown  AccessClass = "Unknown"
	AccessFree     AccessClass = "Free"
	AccessFreeTier AccessClass = "Free-tier"
	AccessPaid     AccessClass = "Paid"
)

// AutoRoutable reports whether the access class is eligible for automatic routing.
// Only Free and Free-tier are auto-routable; Unknown and Paid require explicit opt-in.
func (a AccessClass) AutoRoutable() bool {
	return a == AccessFree || a == AccessFreeTier
}

// Probeable reports whether the class is included in automatic TTFT probing.
func (a AccessClass) Probeable() bool {
	return a.AutoRoutable()
}

// Capability is a named model capability bit.
type Capability string

const (
	CapStreaming        Capability = "Streaming"
	CapTools            Capability = "Tools"
	CapVision           Capability = "Vision"
	CapStructuredOutput Capability = "Structured Output"
	CapReasoning        Capability = "Reasoning"
)

// CapabilityState distinguishes a verified unsupported feature from metadata
// that was not supplied by a provider. Unknown capabilities are represented
// separately so callers can apply a deliberate policy instead of treating
// missing metadata as a negative answer.
type CapabilityState uint8

const (
	CapabilityUnknown CapabilityState = iota
	CapabilityUnsupported
	CapabilitySupported
)

// CapabilitySet is a bit set used to mark feature capabilities whose state is
// unknown. The boolean fields below remain the wire-compatible representation
// of positive/negative capability metadata.
type CapabilitySet uint16

const (
	capStreamingBit CapabilitySet = 1 << iota
	capToolsBit
	capVisionBit
	capStructuredOutputBit
	capReasoningBit
)

// Capabilities is a set of capabilities plus numeric limits.
type Capabilities struct {
	Streaming        bool `json:"streaming"`
	Tools            bool `json:"tools"`
	Vision           bool `json:"vision"`
	StructuredOutput bool `json:"structuredOutput"`
	Reasoning        bool `json:"reasoning"`

	// Unknown marks feature fields whose provider metadata was omitted. A
	// marked feature is neither supported nor unsupported until verified.
	Unknown CapabilitySet `json:"unknown,omitempty"`

	// ContextLength is the maximum input context window in tokens (0 = unknown).
	ContextLength int `json:"contextLength,omitempty"`
	// MaxOutput is the maximum output tokens (0 = unknown).
	MaxOutput int `json:"maxOutput,omitempty"`
}

// Intersect returns the intersection of two capability sets
// (model base ∩ route/protocol override, PLAN_V7 §3).
func (c Capabilities) Intersect(o Capabilities) Capabilities {
	streaming, streamingUnknown := intersectFeature(c.Streaming, c.IsUnknown(CapStreaming), o.Streaming, o.IsUnknown(CapStreaming))
	tools, toolsUnknown := intersectFeature(c.Tools, c.IsUnknown(CapTools), o.Tools, o.IsUnknown(CapTools))
	vision, visionUnknown := intersectFeature(c.Vision, c.IsUnknown(CapVision), o.Vision, o.IsUnknown(CapVision))
	structured, structuredUnknown := intersectFeature(c.StructuredOutput, c.IsUnknown(CapStructuredOutput), o.StructuredOutput, o.IsUnknown(CapStructuredOutput))
	reasoning, reasoningUnknown := intersectFeature(c.Reasoning, c.IsUnknown(CapReasoning), o.Reasoning, o.IsUnknown(CapReasoning))
	var unknown CapabilitySet
	if streamingUnknown {
		unknown |= capStreamingBit
	}
	if toolsUnknown {
		unknown |= capToolsBit
	}
	if visionUnknown {
		unknown |= capVisionBit
	}
	if structuredUnknown {
		unknown |= capStructuredOutputBit
	}
	if reasoningUnknown {
		unknown |= capReasoningBit
	}
	out := Capabilities{
		Streaming:        streaming,
		Tools:            tools,
		Vision:           vision,
		StructuredOutput: structured,
		Reasoning:        reasoning,
		Unknown:          unknown,
		ContextLength:    minPositive(c.ContextLength, o.ContextLength),
		MaxOutput:        minPositive(c.MaxOutput, o.MaxOutput),
	}
	return out
}

// State returns the three-valued state of a feature capability.
func (c Capabilities) State(cap Capability) CapabilityState {
	if c.IsUnknown(cap) {
		return CapabilityUnknown
	}
	switch cap {
	case CapStreaming:
		if c.Streaming {
			return CapabilitySupported
		}
	case CapTools:
		if c.Tools {
			return CapabilitySupported
		}
	case CapVision:
		if c.Vision {
			return CapabilitySupported
		}
	case CapStructuredOutput:
		if c.StructuredOutput {
			return CapabilitySupported
		}
	case CapReasoning:
		if c.Reasoning {
			return CapabilitySupported
		}
	default:
		return CapabilityUnknown
	}
	return CapabilityUnsupported
}

// IsUnknown reports whether provider metadata for cap was omitted.
func (c Capabilities) IsUnknown(cap Capability) bool {
	return c.Unknown&capabilityBit(cap) != 0
}

// MarkUnknown marks cap as having unknown provider metadata.
func (c *Capabilities) MarkUnknown(cap Capability) {
	if c != nil {
		c.Unknown |= capabilityBit(cap)
	}
}

func capabilityBit(cap Capability) CapabilitySet {
	switch cap {
	case CapStreaming:
		return capStreamingBit
	case CapTools:
		return capToolsBit
	case CapVision:
		return capVisionBit
	case CapStructuredOutput:
		return capStructuredOutputBit
	case CapReasoning:
		return capReasoningBit
	default:
		return 0
	}
}

// intersectFeature is conservative: a definitive unsupported side wins over
// unknown metadata, while unknown remains visible when every side could still
// support the feature.
func intersectFeature(a bool, aUnknown bool, b bool, bUnknown bool) (value bool, unknown bool) {
	if (!aUnknown && !a) || (!bUnknown && !b) {
		return false, false
	}
	if aUnknown || bUnknown {
		return a && b, true
	}
	return a && b, false
}

// Supports reports whether this capability set satisfies a single requirement.
func (c Capabilities) Supports(req RequestRequirements) bool {
	if req.Streaming && c.State(CapStreaming) != CapabilitySupported {
		return false
	}
	if req.Tools && c.State(CapTools) != CapabilitySupported {
		return false
	}
	if req.Vision && c.State(CapVision) != CapabilitySupported {
		return false
	}
	if req.StructuredOutput && c.State(CapStructuredOutput) != CapabilitySupported {
		return false
	}
	if req.Reasoning && c.State(CapReasoning) != CapabilitySupported {
		return false
	}
	if req.MinContextLength > 0 && c.ContextLength > 0 && c.ContextLength < req.MinContextLength {
		return false
	}
	if req.MaxOutputTokens > 0 && c.MaxOutput > 0 && c.MaxOutput < req.MaxOutputTokens {
		return false
	}
	return true
}

func minPositive(a, b int) int {
	switch {
	case a <= 0:
		return b
	case b <= 0:
		return a
	default:
		if a < b {
			return a
		}
		return b
	}
}

// RequestRequirements captures what a request needs from a model.
type RequestRequirements struct {
	Streaming        bool `json:"streaming,omitempty"`
	Tools            bool `json:"tools,omitempty"`
	Vision           bool `json:"vision,omitempty"`
	StructuredOutput bool `json:"structuredOutput,omitempty"`
	Reasoning        bool `json:"reasoning,omitempty"`
	MinContextLength int  `json:"minContextLength,omitempty"`
	MaxOutputTokens  int  `json:"maxOutputTokens,omitempty"`
}

// CanonicalModel is the provider-independent model identity used for benchmarks.
type CanonicalModel struct {
	Key    CanonicalModelKey `json:"key"`
	Alias  string            `json:"alias,omitempty"`
	Source string            `json:"source,omitempty"`
}

// ProviderModel is a model offered by one provider; many ProviderModels may map
// to one CanonicalModelKey.
type ProviderModel struct {
	ID           ProviderModelID   `json:"id"`
	CanonicalKey CanonicalModelKey `json:"canonicalKey"`
	DisplayName  string            `json:"displayName"`
	Base         Capabilities      `json:"baseCapabilities"`
	// UpstreamID is the opaque identifier sent to the provider. It is kept
	// separate from ID because provider-native identifiers may contain '/'.
	UpstreamID string `json:"upstreamId"`
}

// ProviderRoute is one concrete endpoint/auth path of a ProviderModel.
// Route state (health, TTFT, cooldown, quota) is per-route (PLAN_V7 §9).
type ProviderRoute struct {
	ID              RouteID         `json:"id"`
	ModelID         ProviderModelID `json:"modelId"`
	Provider        string          `json:"provider"`
	UpstreamModelID string          `json:"upstreamModelId"`
	// CredentialID groups routes that share the same credential. An empty
	// value is allowed for legacy callers and is derived from RouteID by the
	// gateway fallback policy.
	CredentialID string      `json:"credentialId,omitempty"`
	Access       AccessClass `json:"access"`

	// Capability overrides intersect with the model base capabilities.
	CapabilityOverride *Capabilities `json:"capabilityOverride,omitempty"`

	// Enabled is the user-facing enable/disable state of this route.
	Enabled bool `json:"enabled"`
}

// EffectiveCapabilities computes base ∩ route override.
func (r *ProviderRoute) EffectiveCapabilities(base Capabilities) Capabilities {
	if r.CapabilityOverride == nil {
		return base
	}
	return base.Intersect(*r.CapabilityOverride)
}

// EffectiveAccess resolves the authoritative access of the route itself.
// An omitted access classification is deliberately fail-closed as Unknown.
func (r *ProviderRoute) EffectiveAccess() AccessClass {
	if r.Access == "" {
		return AccessUnknown
	}
	return r.Access
}

// RouteAccess resolves the authoritative access of the route itself.
func (r *ProviderRoute) RouteAccess() AccessClass { return r.EffectiveAccess() }

// Validate performs basic entity invariants.
func (r *ProviderRoute) Validate() error {
	if r.ID == "" {
		return fmt.Errorf("route id must not be empty")
	}
	if r.ModelID == "" {
		return fmt.Errorf("route model id must not be empty")
	}
	if r.Provider == "" {
		return fmt.Errorf("route provider must not be empty")
	}
	if r.UpstreamModelID == "" {
		return fmt.Errorf("route upstream model id must not be empty")
	}
	if r.EffectiveAccess() != AccessUnknown && r.EffectiveAccess() != AccessFree && r.EffectiveAccess() != AccessFreeTier && r.EffectiveAccess() != AccessPaid {
		return fmt.Errorf("invalid route access %q", r.Access)
	}
	return nil
}

// SnapshotRevision guards optimistic concurrency on catalog snapshots (Phase 3).
type SnapshotRevision int64

// CatalogSnapshot is an immutable catalog snapshot; swap atomically.
type CatalogSnapshot struct {
	Revision  SnapshotRevision                  `json:"revision"`
	CreatedAt time.Time                         `json:"createdAt"`
	Models    map[ProviderModelID]ProviderModel `json:"models"`
}

// Clone returns an immutable-view copy of the snapshot and its model map.
func (s *CatalogSnapshot) Clone() *CatalogSnapshot {
	if s == nil {
		return nil
	}
	out := *s
	if s.Models != nil {
		out.Models = make(map[ProviderModelID]ProviderModel, len(s.Models))
		for id, pm := range s.Models {
			out.Models[id] = pm
		}
	}
	return &out
}

// Validate ensures the snapshot is well-formed.
func (s *CatalogSnapshot) Validate() error {
	if s.Revision < 0 {
		return fmt.Errorf("negative snapshot revision %d", s.Revision)
	}
	if s.Models == nil {
		return fmt.Errorf("snapshot models map must not be nil")
	}
	return nil
}
