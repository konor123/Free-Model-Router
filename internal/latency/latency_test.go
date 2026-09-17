package latency

import (
	"testing"
	"time"
)

func TestSnapshotExpiryFailureAndRecovery(t *testing.T) {
	r := NewRegistry()
	r.RecordProbe("route", 500)
	first := r.Snapshot("route", time.Now(), 90*time.Second)
	if !first.Fresh || first.ValueMs != 500 || first.Source != "probe" {
		t.Fatalf("first: %+v", first)
	}
	atExpiry := first.MeasuredAt.Add(90 * time.Second)
	if !r.Snapshot("route", atExpiry, 90*time.Second).Fresh {
		t.Fatal("boundary must be fresh")
	}
	r.MarkProbeFailure("route", "timeout")
	stale := r.Snapshot("route", atExpiry.Add(time.Nanosecond), 90*time.Second)
	if stale.Fresh || !stale.Known || stale.ValueMs != 500 || stale.MeasuredAt != first.MeasuredAt || stale.ProbeOutcome != "timeout" {
		t.Fatalf("stale: %+v", stale)
	}
	r.RecordProbe("route", 100)
	recovered := r.Snapshot("route", time.Now(), 90*time.Second)
	if !recovered.Fresh || recovered.ProbeOutcome != "success" || recovered.ValueMs != 400 {
		t.Fatalf("recovered: %+v", recovered)
	}
}

func TestSnapshotUnknownAndMatchingRequestTimestamp(t *testing.T) {
	r := NewRegistry()
	if r.Snapshot("missing", time.Now(), 90*time.Second).Known {
		t.Fatal("invented measurement")
	}
	r.RecordProbe("route", 500)
	s := r.For("route")
	s.mu.Lock()
	s.LastProbeAt = time.Now().Add(-5 * time.Minute)
	s.mu.Unlock()
	r.RecordRequest("route", 50, 100)
	view := r.Snapshot("route", time.Now(), 90*time.Second)
	if !view.Fresh || view.ValueMs != 50 || view.Source != "request" || view.MeasuredAt != s.LastRequestAt {
		t.Fatalf("view: %+v", view)
	}
}

func TestEWMAFirstSampleWins(t *testing.T) {
	e := New()
	e.Add(100)
	if v, ok := e.Value(); !ok || v != 100 {
		t.Fatalf("first sample should be value: %v %v", v, ok)
	}
}

func TestFreshTTFTFallsBackFromStaleProbeToRequest(t *testing.T) {
	r := NewRegistry()
	r.RecordProbe("route", 500)
	r.RecordRequest("route", 100, 500)
	s := r.For("route")
	s.mu.Lock()
	s.LastProbeAt = time.Now().Add(-2 * time.Minute)
	s.LastRequestAt = time.Now()
	s.mu.Unlock()
	if value, ok := r.FreshTTFT("route", time.Now(), 90*time.Second); !ok || value != 100 {
		t.Fatalf("want fresh request fallback, got %v %v", value, ok)
	}
}

func TestEWMAUpdateFormula(t *testing.T) {
	e := New()
	e.Add(100) // first sample: 100
	if v, _ := e.Value(); v != 100 {
		t.Fatalf("first sample must seed, got %v", v)
	}
	e.Add(200) // 0.25*200 + 0.75*100 = 125
	if v, _ := e.Value(); v != 125 {
		t.Fatalf("want 125, got %v", v)
	}
	e.Add(0) // 0.25*0 + 0.75*125 = 93.75
	if v, _ := e.Value(); v != 93.75 {
		t.Fatalf("want 93.75, got %v", v)
	}
}

func TestRegistryProbePreferredOverRequest(t *testing.T) {
	r := NewRegistry()
	r.RecordProbe("route-a", 500)
	r.RecordProbe("route-a", 600)
	if v, ok := r.EffectiveTTFT("route-a"); !ok || v < 499 || v > 601 {
		t.Fatalf("probe-only TTFT: %v", v)
	}
	r.RecordRequest("route-a", 200, 1500)
	if v, _ := r.EffectiveTTFT("route-a"); v < 499 || v > 601 {
		t.Fatalf("probe sample must win, got %v", v)
	}
}

func TestUnknownLatencyNoValue(t *testing.T) {
	r := NewRegistry()
	if _, ok := r.EffectiveTTFT("nonexistent"); ok {
		t.Fatal("no sample must report not-ok")
	}
}

func TestTimerOnlyFirstSemanticEvent(t *testing.T) {
	tm := Start()
	if d1, ok := tm.OnSemanticEvent(); !ok || d1 < 0 {
		t.Fatalf("first semantic event should record: %v %v", d1, ok)
	}
	if _, ok := tm.OnSemanticEvent(); ok {
		t.Fatal("second semantic event must not re-record")
	}
	if !tm.Recorded() {
		t.Fatal("recorded flag expected")
	}
}

func TestRequestTotalEWMA(t *testing.T) {
	r := NewRegistry()
	r.RecordRequest("rt", 100, 1000)
	r.RecordRequest("rt", 200, 2000)
	// total: 1000 seeded, then 0.25*2000 + 0.75*1000 = 1250
	s := r.For("rt")
	v, ok := s.RequestTotal.Value()
	if !ok || v != 1250 {
		t.Fatalf("total EWMA should be 1250, got %v", v)
	}
}
