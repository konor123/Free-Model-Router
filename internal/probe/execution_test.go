package probe

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/konor123/Free-Model-Router/internal/health"
	"github.com/konor123/Free-Model-Router/internal/latency"
	"github.com/konor123/Free-Model-Router/internal/model"
	"github.com/konor123/Free-Model-Router/internal/provider"
)

type probeProvider struct {
	stream *probeStream
	err    error
	calls  int
	onCall func(context.Context)
}

func (p *probeProvider) DiscoverModels(context.Context) (*model.CatalogSnapshot, error) {
	return nil, nil
}
func (p *probeProvider) ChatCompletion(ctx context.Context, _ model.ProviderRoute, _ provider.NormalizedRequest) (provider.ChatStream, error) {
	p.calls++
	if p.onCall != nil {
		p.onCall(ctx)
	}
	return p.stream, p.err
}

type probeStream struct {
	events []provider.StreamEvent
	reads  int
	closed bool
	err    error
}

func (s *probeStream) Next(context.Context) (provider.StreamEvent, error) {
	s.reads++
	if len(s.events) == 0 {
		if s.err != nil {
			return provider.StreamEvent{}, s.err
		}
		return provider.StreamEvent{}, io.EOF
	}
	e := s.events[0]
	s.events = s.events[1:]
	return e, nil
}
func (s *probeStream) Close() error { s.closed = true; return nil }
func testProbe() (*Scheduler, Target) {
	return New(latency.NewRegistry(), health.New(), time.Minute), Target{Route: model.ProviderRoute{ID: "test::m", Provider: "test"}}
}

func TestProbeClosesAtFirstDelta(t *testing.T) {
	for name, event := range map[string]provider.StreamEvent{
		"text": {DeltaText: "pong"}, "reasoning": {ReasoningContent: "thinking"},
		"tool":            {ToolCalls: []json.RawMessage{json.RawMessage(`{"index":0}`)}},
		"text_and_finish": {DeltaText: "pong", FinishReason: "stop"},
	} {
		t.Run(name, func(t *testing.T) {
			s, target := testProbe()
			stream := &probeStream{events: []provider.StreamEvent{{}, event}, err: errors.New("must not read past delta")}
			p := &probeProvider{stream: stream, onCall: func(context.Context) {
				view := s.Reg.Snapshot("test::m", time.Now(), time.Minute)
				if view.ProbeOutcome != "probing" || view.ProbeAttemptAt.IsZero() {
					t.Fatalf("attempt not tracked: %+v", view)
				}
			}}
			s.ProbeOnce(context.Background(), target, p)
			view := s.Reg.Snapshot("test::m", time.Now(), time.Minute)
			if !view.Fresh || view.ValueMs <= 0 || view.ProbeOutcome != "success" || !stream.closed || stream.reads != 2 {
				t.Fatalf("view=%+v stream=%+v", view, stream)
			}
		})
	}
}

func TestProbeFailuresBackoffAndRecovery(t *testing.T) {
	for name, p := range map[string]*probeProvider{
		"error":       {err: errors.New("upstream unavailable")},
		"timeout":     {err: context.DeadlineExceeded},
		"no_semantic": {stream: &probeStream{events: []provider.StreamEvent{{FinishReason: "stop"}}}},
	} {
		t.Run(name, func(t *testing.T) {
			s, target := testProbe()
			s.Reg.RecordProbe("test::m", 12)
			before := s.Reg.Snapshot("test::m", time.Now(), time.Minute)
			s.ProbeOnce(context.Background(), target, p)
			view := s.Reg.Snapshot("test::m", time.Now(), time.Minute)
			if view.ProbeOutcome != name || view.ValueMs != 12 || !view.MeasuredAt.Equal(before.MeasuredAt) {
				t.Fatalf("failure altered sample: %+v", view)
			}
			s.ProbeOnce(context.Background(), target, p)
			if p.calls != 1 {
				t.Fatal("backoff did not suppress retry")
			}
			if s.backoffs["test::m"].delay != baseProbeBackoff {
				t.Fatal("incorrect initial backoff")
			}
			for i := 0; i < 10; i++ {
				s.failProbe("test::m", name)
			}
			if s.backoffs["test::m"].delay != maxProbeBackoff {
				t.Fatal("backoff not capped")
			}
			state := s.backoffs["test::m"]
			state.nextAt = time.Now().Add(-time.Second)
			s.backoffs["test::m"] = state
			success := &probeProvider{stream: &probeStream{events: []provider.StreamEvent{{DeltaText: "ok"}}}}
			s.ProbeOnce(context.Background(), target, success)
			if _, ok := s.backoffs["test::m"]; ok {
				t.Fatal("success did not reset backoff")
			}
			if !s.Health.Available("test::m") {
				t.Fatal("success did not reset health")
			}
		})
	}
}

func TestProbeEOFIsNoSemantic(t *testing.T) {
	s, target := testProbe()
	p := &probeProvider{stream: &probeStream{}}
	s.ProbeOnce(context.Background(), target, p)
	view := s.Reg.Snapshot("test::m", time.Now(), time.Minute)
	if view.Known || view.ProbeOutcome != "no_semantic" || !p.stream.closed {
		t.Fatalf("view=%+v", view)
	}
}

func TestProbeCancellationIsNotFailure(t *testing.T) {
	s, target := testProbe()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := &probeProvider{err: context.Canceled, onCall: func(context.Context) { cancel() }}
	s.ProbeOnce(ctx, target, p)
	view := s.Reg.Snapshot("test::m", time.Now(), time.Minute)
	if view.ProbeOutcome != "canceled" || len(s.backoffs) != 0 || !s.Health.Available("test::m") {
		t.Fatalf("cancellation penalized route: %+v", view)
	}
	s.ProbeOnce(ctx, target, p)
	if p.calls != 1 {
		t.Fatal("canceled parent started another request")
	}
}

func TestProbeConcurrentAttemptSuppressed(t *testing.T) {
	s, target := testProbe()
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	p := &probeProvider{stream: &probeStream{events: []provider.StreamEvent{{DeltaText: "ok"}}}, onCall: func(context.Context) { close(entered); <-release }}
	go func() { defer close(done); s.ProbeOnce(context.Background(), target, p) }()
	<-entered
	second := &probeProvider{err: errors.New("must not call")}
	s.ProbeOnce(context.Background(), target, second)
	close(release)
	<-done
	if second.calls != 0 {
		t.Fatal("concurrent attempt not suppressed")
	}
}
