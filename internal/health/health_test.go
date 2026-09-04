package health

import (
	"testing"
	"time"
)

func TestCooldownSkipsAvailability(t *testing.T) {
	m := New()
	m.RouteFailure("rt", 50*time.Millisecond)
	if m.Available("rt") {
		t.Fatal("route in cooldown must be unavailable")
	}
	time.Sleep(60 * time.Millisecond)
	if !m.Available("rt") {
		t.Fatal("cooldown must expire")
	}
}

func TestQuotaExhaustedBlocksUntilReset(t *testing.T) {
	m := New()
	m.MarkQuotaExhausted("rt")
	if m.Available("rt") {
		t.Fatal("quota-exhausted must block")
	}
	m.ResetQuota("rt")
	if !m.Available("rt") {
		t.Fatal("reset must restore availability")
	}
}

func TestSuccessClearsCooldown(t *testing.T) {
	m := New()
	m.RouteFailure("rt", time.Hour)
	m.RouteSuccess("rt")
	if !m.Available("rt") {
		t.Fatal("success must clear cooldown")
	}
	if m.Snapshot("rt").ConsecutiveFailures != 0 {
		t.Fatal("success must reset failure counter")
	}
}

func TestProviderConcurrencyCap(t *testing.T) {
	m := New()
	m.SetProviderCap("opencode", 1)

	acquired := make(chan bool, 2)
	go func() { acquired <- m.AcquireSlot("opencode") }()
	<-acquired // first slot
	go func() {
		// Second acquire should block.
		m.AcquireSlot("opencode")
		acquired <- true
	}()
	select {
	case <-acquired:
		t.Fatal("second slot should block under cap 1")
	case <-time.After(50 * time.Millisecond):
	}
	m.ReleaseSlot("opencode")
	select {
	case <-acquired:
	case <-time.After(time.Second):
		t.Fatal("release must unblock the waiter")
	}
}

func TestUnknownRouteAvailable(t *testing.T) {
	m := New()
	if !m.Available("never-seen") {
		t.Fatal("unknown routes default to available")
	}
}

func TestFailureCounterIncrements(t *testing.T) {
	m := New()
	m.RouteFailure("rt", 10*time.Millisecond)
	m.RouteFailure("rt", 10*time.Millisecond)
	if m.Snapshot("rt").ConsecutiveFailures != 2 {
		t.Fatalf("want 2 failures, got %d", m.Snapshot("rt").ConsecutiveFailures)
	}
}
