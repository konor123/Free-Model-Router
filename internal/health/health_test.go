package health

import (
	"context"
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

	releaseCh := make(chan func(), 1)
	acquired := make(chan bool, 2)
	errCh := make(chan error, 2)

	// First acquisition: retains its release function.
	go func() {
		rel, err := m.AcquireSlot(context.Background(), "opencode")
		if err != nil {
			errCh <- err
			return
		}
		releaseCh <- rel
		acquired <- true
	}()
	<-acquired

	// Second goroutine waits under cap one, returns its release via channel.
	go func() {
		rel, err := m.AcquireSlot(context.Background(), "opencode")
		if err != nil {
			errCh <- err
			return
		}
		releaseCh <- rel
		acquired <- true
	}()

	select {
	case <-acquired:
		t.Fatal("second slot should block under cap 1")
	case <-time.After(50 * time.Millisecond):
	}

	// Check for errors from goroutines
	select {
	case err := <-errCh:
		t.Fatalf("acquire error: %v", err)
	default:
	}

	// First release unblocks the second.
	rel1 := <-releaseCh
	rel1()

	select {
	case <-acquired:
	case <-time.After(time.Second):
		t.Fatal("release must unblock the waiter")
	}

	// Check for errors from goroutines
	select {
	case err := <-errCh:
		t.Fatalf("acquire error: %v", err)
	default:
	}

	// Clean up second release.
	rel2 := <-releaseCh
	rel2()
}

func TestProviderConcurrencyCap_Cancellation(t *testing.T) {
	m := New()
	m.SetProviderCap("opencode", 1)

	releaseCh := make(chan func(), 1)
	acquired := make(chan bool, 1)
	errCh := make(chan error, 1)

	// First acquisition takes the only slot.
	go func() {
		rel, err := m.AcquireSlot(context.Background(), "opencode")
		if err != nil {
			errCh <- err
			return
		}
		releaseCh <- rel
		acquired <- true
	}()
	<-acquired

	// Check for error from first goroutine
	select {
	case err := <-errCh:
		t.Fatalf("first acquire: %v", err)
	default:
	}

	// Second acquisition with short timeout should fail promptly.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	go func() {
		_, err := m.AcquireSlot(ctx, "opencode")
		errCh <- err
	}()

	select {
	case err := <-errCh:
		if err != context.DeadlineExceeded && err != context.Canceled {
			t.Fatalf("expected deadline exceeded or canceled, got: %v", err)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("acquisition should have returned promptly on timeout")
	}

	// Release first slot; no other waiters should be blocked.
	rel := <-releaseCh
	rel()
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
