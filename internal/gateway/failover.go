package gateway

import (
	"os"
	"strconv"
	"time"
)

const (
	DefaultMaxAttempts       = 4
	DefaultFailoverBudget    = 30 * time.Second
	maxConfiguredAttempts    = 100
	maxConfiguredBudget      = 10 * time.Minute
	maxConfiguredBudgetMilli = int64(maxConfiguredBudget / time.Millisecond)
)

// FailoverPolicy bounds one client request's candidate attempts.
// MaxAttempts includes the first attempt.
type FailoverPolicy struct {
	MaxAttempts int
	Budget      time.Duration
}

// DefaultFailoverPolicy returns the Phase 7 defaults, with optional
// FMR_MAX_ATTEMPTS and FMR_FAILOVER_BUDGET_MS environment overrides.
func DefaultFailoverPolicy() FailoverPolicy {
	policy := FailoverPolicy{MaxAttempts: DefaultMaxAttempts, Budget: DefaultFailoverBudget}
	if raw := os.Getenv("FMR_MAX_ATTEMPTS"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 && n <= maxConfiguredAttempts {
			policy.MaxAttempts = n
		}
	}
	if raw := os.Getenv("FMR_FAILOVER_BUDGET_MS"); raw != "" {
		if ms, err := strconv.ParseInt(raw, 10, 64); err == nil && ms > 0 && ms <= maxConfiguredBudgetMilli {
			policy.Budget = time.Duration(ms) * time.Millisecond
		}
	}
	return policy
}

func (p FailoverPolicy) normalized() FailoverPolicy {
	if p.MaxAttempts <= 0 {
		p.MaxAttempts = DefaultMaxAttempts
	} else if p.MaxAttempts > maxConfiguredAttempts {
		p.MaxAttempts = maxConfiguredAttempts
	}
	if p.Budget <= 0 {
		p.Budget = DefaultFailoverBudget
	} else if p.Budget > maxConfiguredBudget {
		p.Budget = maxConfiguredBudget
	}
	return p
}
