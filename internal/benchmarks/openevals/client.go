// Package openevals imports the public OpenEvals leaderboard hosted on the
// Hugging Face Hub. It intentionally consumes one revision-pinned JSON
// artifact; it never scrapes HTML or merges the incompatible parquet export.
package openevals

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/konor123/Free-Model-Router/internal/matcher"
	"github.com/konor123/Free-Model-Router/internal/scoring"
)

const (
	Dataset     = "OpenEvals/leaderboard-data"
	Source      = "huggingface/openevals/leaderboard-data"
	Policy      = "openevals-v1"
	maxBodySize = 2 << 20
)

var shaRE = regexp.MustCompile(`^[0-9a-f]{40}$`)

// Client fetches a revision-pinned leaderboard snapshot.
type Client struct {
	HTTPClient *http.Client
	Now        func() time.Time
}

type datasetMetadata struct {
	SHA string `json:"sha"`
}

type leaderboard struct {
	Metadata struct {
		Version     string `json:"version"`
		LastUpdated string `json:"lastUpdated"`
	} `json:"metadata"`
	Models []leaderboardModel `json:"models"`
}

type leaderboardModel struct {
	ID         string                     `json:"id"`
	Name       string                     `json:"name"`
	Benchmarks map[string]benchmarkResult `json:"benchmarks"`
}

type benchmarkResult struct {
	Score *float64 `json:"score"`
	Date  string   `json:"date"`
}

// Fetch returns a validated local scoring snapshot. All scores are derived
// from source cohorts; omitted scores remain nil and are never imputed.
func (c Client) Fetch(ctx context.Context) (scoring.Snapshot, error) {
	client := c.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	metadataURL := "https://huggingface.co/api/datasets/" + Dataset
	var metadata datasetMetadata
	if err := c.getJSON(ctx, client, metadataURL, &metadata); err != nil {
		return scoring.Snapshot{}, fmt.Errorf("read OpenEvals metadata: %w", err)
	}
	sha := strings.ToLower(strings.TrimSpace(metadata.SHA))
	if !shaRE.MatchString(sha) {
		return scoring.Snapshot{}, fmt.Errorf("invalid OpenEvals revision")
	}
	url := "https://huggingface.co/datasets/" + Dataset + "/resolve/" + sha + "/leaderboard.json"
	var board leaderboard
	if err := c.getJSON(ctx, client, url, &board); err != nil {
		return scoring.Snapshot{}, fmt.Errorf("read OpenEvals leaderboard: %w", err)
	}
	now := time.Now()
	if c.Now != nil {
		now = c.Now()
	}
	return normalize(board, sha, now)
}

func (c Client) getJSON(ctx context.Context, client *http.Client, url string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("unexpected HTTP status %d", resp.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBodySize+1)).Decode(out); err != nil {
		return err
	}
	return nil
}

var capabilityBenchmarks = map[string]bool{"sweVerified": true, "swePro": true, "terminalBench": true}
var intelligenceBenchmarks = map[string]bool{"gsm8k": true, "mmluPro": true, "gpqa": true, "hle": true, "aime2026": true, "hmmt2026": true}

func normalize(board leaderboard, sha string, now time.Time) (scoring.Snapshot, error) {
	if len(board.Models) == 0 {
		return scoring.Snapshot{}, fmt.Errorf("OpenEvals has no models")
	}
	seen := make(map[string]bool, len(board.Models))
	values := make(map[string][]float64)
	for _, model := range board.Models {
		id := strings.TrimSpace(model.ID)
		if id == "" || seen[id] {
			return scoring.Snapshot{}, fmt.Errorf("duplicate or empty OpenEvals model id")
		}
		seen[id] = true
		for name, result := range model.Benchmarks {
			if !capabilityBenchmarks[name] && !intelligenceBenchmarks[name] {
				continue
			}
			if result.Score == nil {
				continue
			}
			if math.IsNaN(*result.Score) || math.IsInf(*result.Score, 0) || *result.Score < 0 || *result.Score > 100 {
				return scoring.Snapshot{}, fmt.Errorf("invalid %s score for %s", name, id)
			}
			values[name] = append(values[name], *result.Score)
		}
	}
	metrics := make(map[string]scoring.Metrics, len(board.Models))
	benchmarks := make([]matcher.BenchmarkModel, 0, len(board.Models))
	for _, sourceModel := range board.Models {
		capability := categoryPercentile(sourceModel.Benchmarks, values, capabilityBenchmarks)
		intelligence := categoryPercentile(sourceModel.Benchmarks, values, intelligenceBenchmarks)
		if capability == nil && intelligence == nil {
			continue
		}
		metrics[sourceModel.ID] = scoring.Metrics{Capability: capability, Intelligence: intelligence}
		aliases := uniqueAliases(sourceModel.ID, sourceModel.Name)
		benchmarks = append(benchmarks, matcher.BenchmarkModel{SourceModelID: sourceModel.ID, Name: sourceModel.Name, Aliases: aliases})
	}
	if len(metrics) == 0 {
		return scoring.Snapshot{}, fmt.Errorf("OpenEvals has no usable approved benchmark scores")
	}
	updatedAt, _ := time.Parse(time.RFC3339Nano, strings.TrimSpace(board.Metadata.LastUpdated))
	return scoring.Snapshot{Version: strings.TrimSpace(board.Metadata.Version) + "+" + sha + "+" + Policy, Source: Source, RetrievedAt: now.UTC(), Benchmarks: benchmarks, Models: metrics, Provenance: &scoring.Provenance{Revision: sha, UpstreamVersion: board.Metadata.Version, UpstreamUpdatedAt: updatedAt, PolicyVersion: Policy}}, nil
}

func categoryPercentile(results map[string]benchmarkResult, populations map[string][]float64, allowed map[string]bool) *float64 {
	scores := make([]float64, 0, len(allowed))
	for name := range allowed {
		result, ok := results[name]
		if !ok || result.Score == nil || len(populations[name]) < 2 {
			continue
		}
		scores = append(scores, scoring.Percentile(*result.Score, populations[name], true))
	}
	if len(scores) == 0 {
		return nil
	}
	sort.Float64s(scores)
	value := 0.0
	for _, score := range scores {
		value += score
	}
	value /= float64(len(scores))
	return &value
}

func uniqueAliases(id, name string) []string {
	values := []string{id, name}
	if slash := strings.LastIndex(name, "/"); slash >= 0 && slash+1 < len(name) {
		values = append(values, name[slash+1:])
	}
	seen := map[string]bool{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" && !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	return result
}
