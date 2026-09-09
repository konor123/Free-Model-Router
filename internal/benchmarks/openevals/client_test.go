package openevals

import (
	"testing"
	"time"
)

func floatPtr(value float64) *float64 { return &value }

func TestNormalizeUsesOnlyApprovedScoresAndKeepsMissingMetricsUnknown(t *testing.T) {
	board := leaderboard{}
	board.Metadata.Version = "1.0.0"
	board.Models = []leaderboardModel{
		{ID: "kimi-k2.5", Name: "moonshotai/Kimi-K2.5", Benchmarks: map[string]benchmarkResult{"gsm8k": {Score: floatPtr(80)}, "olmOcr": {Score: floatPtr(100)}}},
		{ID: "glm-4.7", Name: "zai-org/GLM-4.7", Benchmarks: map[string]benchmarkResult{"gsm8k": {Score: floatPtr(60)}, "sweVerified": {Score: floatPtr(70)}}},
	}
	snapshot, err := normalize(board, "0123456789012345678901234567890123456789", time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Source != Source || snapshot.Models["kimi-k2.5"].Intelligence == nil {
		t.Fatal("expected intelligence score")
	}
	if snapshot.Models["kimi-k2.5"].Capability != nil {
		t.Fatal("OCR must not become capability")
	}
	if snapshot.Models["glm-4.7"].Availability != nil {
		t.Fatal("availability must remain unknown")
	}
}

func TestNormalizeRejectsDuplicateAndInvalidScores(t *testing.T) {
	board := leaderboard{Models: []leaderboardModel{
		{ID: "same", Benchmarks: map[string]benchmarkResult{"gsm8k": {Score: floatPtr(50)}}},
		{ID: "same", Benchmarks: map[string]benchmarkResult{"gsm8k": {Score: floatPtr(60)}}},
	}}
	if _, err := normalize(board, "0123456789012345678901234567890123456789", time.Now()); err == nil {
		t.Fatal("expected duplicate rejection")
	}
	board.Models = []leaderboardModel{{ID: "bad", Benchmarks: map[string]benchmarkResult{"gsm8k": {Score: floatPtr(101)}}}}
	if _, err := normalize(board, "0123456789012345678901234567890123456789", time.Now()); err == nil {
		t.Fatal("expected range rejection")
	}
}
