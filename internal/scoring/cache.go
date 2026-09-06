package scoring

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/konor123/Free-Model-Router/internal/matcher"
)

const (
	// DefaultCacheMaxAge is only used for the stale marker. A stale
	// last-known-good snapshot is still usable when the source is unavailable.
	DefaultCacheMaxAge = 7 * 24 * time.Hour
	maxSnapshotBytes   = 16 << 20
)

// Snapshot is a versioned local copy of benchmark data. The Models map is
// keyed by the benchmark source's stable model identifier.
type Snapshot struct {
	Version     string                   `json:"version"`
	Source      string                   `json:"source"`
	RetrievedAt time.Time                `json:"retrievedAt"`
	Benchmarks  []matcher.BenchmarkModel `json:"benchmarks,omitempty"`
	Models      map[string]Metrics       `json:"models"`
}

// Validate checks the metadata required for a last-known-good snapshot.
func (s Snapshot) Validate() error {
	if strings.TrimSpace(s.Version) == "" {
		return errors.New("snapshot version must not be empty")
	}
	if strings.TrimSpace(s.Source) == "" {
		return errors.New("snapshot source must not be empty")
	}
	if s.RetrievedAt.IsZero() {
		return errors.New("snapshot retrievedAt must not be zero")
	}
	if s.Models == nil {
		return errors.New("snapshot models map must not be nil")
	}
	seenBenchmarkIDs := make(map[string]struct{}, len(s.Benchmarks))
	for _, benchmark := range s.Benchmarks {
		id := strings.TrimSpace(benchmark.SourceModelID)
		if id == "" {
			return errors.New("snapshot benchmark source model id must not be empty")
		}
		if _, ok := seenBenchmarkIDs[id]; ok {
			return fmt.Errorf("duplicate snapshot benchmark source model id %q", id)
		}
		seenBenchmarkIDs[id] = struct{}{}
	}
	for id, metrics := range s.Models {
		if strings.TrimSpace(id) == "" {
			return errors.New("snapshot model id must not be empty")
		}
		for name, value := range map[string]*float64{
			"capability":   metrics.Capability,
			"availability": metrics.Availability,
			"intelligence": metrics.Intelligence,
		} {
			if value != nil && (math.IsNaN(*value) || math.IsInf(*value, 0)) {
				return fmt.Errorf("snapshot %s metric for %q is not finite", name, id)
			}
		}
	}
	return nil
}

// Clone returns a deep copy suitable for handing to a caller without sharing
// metric pointers or the model map with the cache's internal state.
func (s Snapshot) Clone() Snapshot {
	out := s
	if s.Benchmarks != nil {
		out.Benchmarks = make([]matcher.BenchmarkModel, len(s.Benchmarks))
		for i, benchmark := range s.Benchmarks {
			out.Benchmarks[i] = benchmark
			out.Benchmarks[i].Aliases = append([]string(nil), benchmark.Aliases...)
		}
	}
	if s.Models == nil {
		return out
	}
	out.Models = make(map[string]Metrics, len(s.Models))
	for id, metrics := range s.Models {
		out.Models[id] = cloneMetrics(metrics)
	}
	return out
}

// IsStale reports whether the snapshot is older than maxAge. Future timestamps
// are not treated as stale because clock skew should not discard good data.
func (s Snapshot) IsStale(now time.Time, maxAge time.Duration) bool {
	if s.RetrievedAt.IsZero() {
		return true
	}
	if maxAge <= 0 || now.Before(s.RetrievedAt) {
		return false
	}
	return now.Sub(s.RetrievedAt) > maxAge
}

// SourceKind explains where a resolved snapshot came from.
type SourceKind string

const (
	SourceLive    SourceKind = "live"
	SourceCache   SourceKind = "cache"
	SourceDefault SourceKind = "default"
)

// Resolution is the non-fatal result of trying to refresh benchmark data.
// LiveError and CacheError preserve diagnostics while callers can continue with
// cached or neutral data as required by Phase 9.
type Resolution struct {
	Snapshot   Snapshot   `json:"snapshot"`
	Source     SourceKind `json:"source"`
	UsedCache  bool       `json:"usedCache"`
	Stale      bool       `json:"stale"`
	LiveError  error      `json:"-"`
	CacheError error      `json:"-"`
}

// Cache stores the last-known-good benchmark snapshot using an atomic replace.
type Cache struct {
	mu     sync.Mutex
	path   string
	maxAge time.Duration
}

// NewCache constructs a file-backed snapshot cache. The cache directory is
// created lazily by Save so a read-only or unavailable cache never blocks use.
func NewCache(path string, maxAge time.Duration) *Cache {
	if maxAge <= 0 {
		maxAge = DefaultCacheMaxAge
	}
	return &Cache{path: path, maxAge: maxAge}
}

// Path returns the configured cache path.
func (c *Cache) Path() string {
	if c == nil {
		return ""
	}
	return c.path
}

// Save validates and atomically replaces the last-known-good snapshot.
func (c *Cache) Save(snapshot Snapshot) error {
	if c == nil {
		return errors.New("cache must not be nil")
	}
	if err := snapshot.Validate(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.saveLocked(snapshot.Clone())
}

// Load reads and validates the current last-known-good snapshot.
func (c *Cache) Load() (Snapshot, error) {
	if c == nil {
		return Snapshot{}, errors.New("cache must not be nil")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.loadLocked()
}

// LoadLastKnownGood is an explicit alias for Load at call sites that are
// resolving an unavailable live source.
func (c *Cache) LoadLastKnownGood() (Snapshot, error) { return c.Load() }

// Resolve tries a live benchmark fetch first. If it fails or returns an invalid
// snapshot, the last-known-good cache is used, including when it is stale. If
// neither source is available, a non-nil empty map is returned so scoring can
// safely produce P=50.
func (c *Cache) Resolve(fetch func() (Snapshot, error), now time.Time) Resolution {
	resolution := Resolution{Source: SourceDefault, Snapshot: emptySnapshot()}
	if c == nil {
		resolution.CacheError = errors.New("cache must not be nil")
		return resolution
	}
	if fetch == nil {
		resolution.LiveError = errors.New("benchmark fetch function must not be nil")
	} else {
		live, err := fetch()
		if err == nil {
			err = live.Validate()
		}
		if err == nil {
			resolution.Source = SourceLive
			resolution.Snapshot = live.Clone()
			if saveErr := c.Save(live); saveErr != nil {
				resolution.CacheError = saveErr
			}
			return resolution
		}
		resolution.LiveError = err
	}

	cached, err := c.Load()
	if err == nil {
		resolution.Source = SourceCache
		resolution.Snapshot = cached
		resolution.UsedCache = true
		resolution.Stale = cached.IsStale(now, c.maxAge)
		return resolution
	}
	resolution.CacheError = err
	return resolution
}

func (c *Cache) saveLocked(snapshot Snapshot) error {
	if strings.TrimSpace(c.path) == "" {
		return errors.New("cache path must not be empty")
	}
	dir := filepath.Dir(c.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create cache directory: %w", err)
	}
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return fmt.Errorf("encode snapshot: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".snapshot-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary snapshot: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("protect temporary snapshot: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temporary snapshot: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync temporary snapshot: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temporary snapshot: %w", err)
	}
	if err := replaceFile(tmpName, c.path); err != nil {
		return fmt.Errorf("replace snapshot: %w", err)
	}
	if err := os.Chmod(c.path, 0o600); err != nil {
		return fmt.Errorf("protect snapshot: %w", err)
	}
	return nil
}

func (c *Cache) loadLocked() (Snapshot, error) {
	if strings.TrimSpace(c.path) == "" {
		return Snapshot{}, errors.New("cache path must not be empty")
	}
	data, err := os.ReadFile(c.path)
	if err != nil && os.IsNotExist(err) {
		// A process can stop after moving the old snapshot to .bak but before
		// installing the new temporary file on Windows. Restore that last-known-
		// good copy before reporting a cache miss.
		if recoverErr := os.Rename(c.path+".bak", c.path); recoverErr == nil {
			data, err = os.ReadFile(c.path)
		}
	}
	if err != nil {
		return Snapshot{}, err
	}
	if len(data) > maxSnapshotBytes {
		return Snapshot{}, fmt.Errorf("snapshot exceeds %d bytes", maxSnapshotBytes)
	}
	var snapshot Snapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return Snapshot{}, fmt.Errorf("decode snapshot: %w", err)
	}
	if err := snapshot.Validate(); err != nil {
		return Snapshot{}, err
	}
	return snapshot.Clone(), nil
}

func replaceFile(tempName, targetName string) error {
	if err := os.Rename(tempName, targetName); err == nil {
		return nil
	}

	// Windows does not replace an existing file with Rename. Move the old
	// snapshot aside, install the complete temporary file, and restore on
	// failure. Unix normally takes the first branch and remains atomic.
	backupName := targetName + ".bak"
	_ = os.Remove(backupName)
	backupMoved := false
	if err := os.Rename(targetName, backupName); err != nil {
		if !os.IsNotExist(err) {
			return err
		}
	} else {
		backupMoved = true
	}
	if err := os.Rename(tempName, targetName); err != nil {
		if backupMoved {
			_ = os.Rename(backupName, targetName)
		}
		return err
	}
	if backupMoved {
		_ = os.Remove(backupName)
	}
	return nil
}

func cloneMetrics(metrics Metrics) Metrics {
	out := metrics
	if metrics.Capability != nil {
		value := *metrics.Capability
		out.Capability = &value
	}
	if metrics.Availability != nil {
		value := *metrics.Availability
		out.Availability = &value
	}
	if metrics.Intelligence != nil {
		value := *metrics.Intelligence
		out.Intelligence = &value
	}
	return out
}

func emptySnapshot() Snapshot {
	return Snapshot{Models: make(map[string]Metrics)}
}
