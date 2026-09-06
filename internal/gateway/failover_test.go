package gateway

import (
	"testing"
	"time"
)

func TestDefaultFailoverPolicyReadsEnvironmentOverrides(t *testing.T) {
	t.Setenv("FMR_MAX_ATTEMPTS", "7")
	t.Setenv("FMR_FAILOVER_BUDGET_MS", "1234")

	got := DefaultFailoverPolicy()
	if got.MaxAttempts != 7 || got.Budget != 1234*time.Millisecond {
		t.Fatalf("environment policy = %+v", got)
	}
}

func TestDefaultFailoverPolicyIgnoresInvalidEnvironmentOverrides(t *testing.T) {
	t.Setenv("FMR_MAX_ATTEMPTS", "0")
	t.Setenv("FMR_FAILOVER_BUDGET_MS", "not-a-duration")

	got := DefaultFailoverPolicy()
	if got.MaxAttempts != DefaultMaxAttempts || got.Budget != DefaultFailoverBudget {
		t.Fatalf("invalid environment policy = %+v", got)
	}
}

func TestFailoverPolicyClampsExplicitLimits(t *testing.T) {
	got := (FailoverPolicy{MaxAttempts: maxConfiguredAttempts + 1, Budget: maxConfiguredBudget + time.Second}).normalized()
	if got.MaxAttempts != maxConfiguredAttempts || got.Budget != maxConfiguredBudget {
		t.Fatalf("explicit policy limits = %+v, want bounded values", got)
	}
}
