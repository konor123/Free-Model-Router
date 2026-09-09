package opencode

// verifiedAnonymousFree models are the OpenCode Zen model IDs listed as Free
// in the official pricing documentation, reviewed on 2026-09-09. Unknown
// additions intentionally fail closed until that policy is reviewed again.
var verifiedAnonymousFree = map[string]struct{}{
	"big-pickle":                        {},
	"mimo-v2.5-free":                    {},
	"ling-3.0-flash-fin-free":           {},
	"nemotron-3-ultra-free":             {},
	"nemotron-3.5-lightning-free":       {},
	"muse-spark-1.3-contributor-free": {},
}

func isVerifiedAnonymousFree(upstreamID string) bool {
	_, ok := verifiedAnonymousFree[upstreamID]
	return ok
}
