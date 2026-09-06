// Package usage records request-level usage and per-attempt routing history.
//
// Usage records intentionally contain operational metadata only. Prompts,
// responses, authorization headers, and secret values have no representation in
// this package, which keeps the durable log safe to expose to the control API.
package usage

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/konor123/Free-Model-Router/internal/model"
)

const (
	// MaxBytes is the maximum retained encoded usage-log size.
	MaxBytes = 20 * 1024 * 1024
	// MaxAge is the maximum age of a retained request record.
	MaxAge         = 30 * 24 * time.Hour
	maxRecordBytes = MaxBytes
)

var (
	// ErrRecordTooLarge means one record cannot fit in the retention budget.
	ErrRecordTooLarge = errors.New("usage record exceeds retention size")
	// ErrCorrupt means a durable usage log contains malformed JSON.
	ErrCorrupt = errors.New("usage log is corrupt")
)

// Result is the terminal request result exposed to the control plane.
type Result string

const (
	ResultSuccess  Result = "success"
	ResultFailure  Result = "failure"
	ResultCanceled Result = "canceled"
	ResultPartial  Result = "partial"
)

// TokenUsage contains aggregate token counts reported by an upstream provider.
// A zero value means that the provider did not report token usage.
type TokenUsage struct {
	Prompt     int `json:"prompt,omitempty"`
	Completion int `json:"completion,omitempty"`
	Total      int `json:"total,omitempty"`
}

// AttemptRecord is one route execution within a request. It deliberately does
// not contain request content or provider error text.
type AttemptRecord struct {
	Index          int                   `json:"index"`
	ProviderModel  model.ProviderModelID `json:"providerModel,omitempty"`
	Route          model.RouteID         `json:"route,omitempty"`
	FailureClass   model.FailureClass    `json:"failureClass,omitempty"`
	FailureScope   model.FailureScope    `json:"failureScope,omitempty"`
	TTFTMs         float64               `json:"ttftMs,omitempty"`
	TotalLatencyMs float64               `json:"totalLatencyMs,omitempty"`
	Committed      bool                  `json:"committed"`
	HTTPStatus     int                   `json:"httpStatus,omitempty"`
	Tokens         TokenUsage            `json:"tokens,omitempty"`
}

// RequestRecord is the one durable record for one client request. Fallbacks are
// represented by Attempts inside this record, so fallback never duplicates
// request-level token or usage totals.
type RequestRecord struct {
	ID          string                `json:"id"`
	StartedAt   time.Time             `json:"startedAt"`
	CompletedAt time.Time             `json:"completedAt"`
	Provider    string                `json:"provider,omitempty"`
	FinalModel  model.ProviderModelID `json:"finalModel,omitempty"`
	FinalRoute  model.RouteID         `json:"finalRoute,omitempty"`
	Attempts    []AttemptRecord       `json:"attempts,omitempty"`
	Result      Result                `json:"result"`
	Fallback    bool                  `json:"fallback,omitempty"`
	TotalTokens TokenUsage            `json:"tokens,omitempty"`
}

// Record and Attempt are concise compatibility names for API consumers.
type Record = RequestRecord
type Attempt = AttemptRecord

// Sink is the gateway-facing append-only usage boundary.
type Sink interface {
	Append(RequestRecord) error
}

// Recorder is an alias retained for callers that prefer the lifecycle name.
type Recorder = Sink

// Query filters the records returned by Store.List. Results are returned newest
// first, with Offset and Limit applied after filtering.
type Query struct {
	From     time.Time
	To       time.Time
	Provider string
	Model    string
	Result   Result
	Fallback *bool
	Offset   int
	Limit    int
}

// Store is a concurrency-safe in-memory usage index with an optional JSONL
// backing file. Every durable update rewrites the bounded retained set through
// a same-directory temporary file, so readers never observe a partial record.
type Store struct {
	mu      sync.RWMutex
	path    string
	records []RequestRecord
	closed  bool
	events  *Broadcaster
}

// New opens or creates a durable usage store at path. An absent file is treated
// as an empty store. Use NewMemory for a process-only store.
func New(path string) (*Store, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return NewMemory(), nil
	}
	s := &Store{path: path, events: NewBroadcaster(64)}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return s, nil
		}
		return nil, fmt.Errorf("read usage log: %w", err)
	}
	if err := decodeJSONL(data, &s.records); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	if s.pruneLocked(now) {
		if err := s.persistLocked(); err != nil {
			return nil, fmt.Errorf("rewrite retained usage log: %w", err)
		}
	}
	return s, nil
}

// Open is a descriptive alias for New.
func Open(path string) (*Store, error) { return New(path) }

// NewMemory creates a non-durable usage store.
func NewMemory() *Store { return &Store{events: NewBroadcaster(64)} }

// Publish forwards a redacted lifecycle event to live desktop subscribers.
func (s *Store) Publish(event Event) {
	if s == nil {
		return
	}
	s.mu.RLock()
	events := s.events
	s.mu.RUnlock()
	if events != nil {
		events.Publish(event)
	}
}

// Subscribe returns a live usage stream and an idempotent unsubscribe function.
func (s *Store) Subscribe() (<-chan Event, func()) {
	if s == nil {
		return NewBroadcaster(1).Subscribe()
	}
	s.mu.Lock()
	if s.events == nil {
		s.events = NewBroadcaster(64)
	}
	events := s.events
	s.mu.Unlock()
	return events.Subscribe()
}

// Append stores one immutable request record and applies age and size
// retention. The input is cloned and normalized before storage.
func (s *Store) Append(record RequestRecord) error {
	if s == nil {
		return errors.New("usage store must not be nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("usage store is closed")
	}
	now := time.Now().UTC()
	record = normalizeRecord(record, now)
	encoded, err := encodeRecord(record)
	if err != nil {
		return err
	}
	if len(encoded) > maxRecordBytes {
		return ErrRecordTooLarge
	}
	previous := append([]RequestRecord(nil), s.records...)
	s.records = append(s.records, record)
	s.pruneLocked(now)
	if err := s.persistLocked(); err != nil {
		s.records = previous
		return err
	}
	if s.events != nil {
		s.events.Publish(Event{Type: EventCompleted, RequestID: record.ID, Record: &record})
	}
	return nil
}

// List returns immutable copies matching q, newest first.
func (s *Store) List(q Query) ([]RequestRecord, error) {
	if s == nil {
		return nil, errors.New("usage store must not be nil")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return nil, errors.New("usage store is closed")
	}
	if q.Offset < 0 || q.Limit < 0 {
		return nil, errors.New("usage query offset and limit must not be negative")
	}
	result := make([]RequestRecord, 0, len(s.records))
	for i := len(s.records) - 1; i >= 0; i-- {
		record := s.records[i]
		if !matches(record, q) {
			continue
		}
		result = append(result, cloneRecord(record))
	}
	if q.Offset >= len(result) {
		return []RequestRecord{}, nil
	}
	result = result[q.Offset:]
	if q.Limit > 0 && q.Limit < len(result) {
		result = result[:q.Limit]
	}
	return result, nil
}

// Snapshot returns all retained records newest first.
func (s *Store) Snapshot() ([]RequestRecord, error) { return s.List(Query{}) }

// Prune applies both retention limits at the supplied point in time.
func (s *Store) Prune(now time.Time) error {
	if s == nil {
		return errors.New("usage store must not be nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("usage store is closed")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	changed := s.pruneLocked(now)
	if changed {
		return s.persistLocked()
	}
	return nil
}

// Count reports the current retained record count.
func (s *Store) Count() int {
	if s == nil {
		return 0
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.records)
}

// Size returns the encoded size of the retained JSONL data.
func (s *Store) Size() int64 {
	if s == nil {
		return 0
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return int64(len(s.encodedLocked()))
}

// Path returns the backing file path, or an empty string for memory stores.
func (s *Store) Path() string {
	if s == nil {
		return ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.path
}

// Close prevents further writes. It does not remove the backing file.
func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

func (s *Store) pruneLocked(now time.Time) bool {
	changed := false
	cutoff := now.Add(-MaxAge)
	kept := s.records[:0]
	for _, record := range s.records {
		at := record.CompletedAt
		if at.IsZero() {
			at = record.StartedAt
		}
		if !at.IsZero() && at.Before(cutoff) {
			changed = true
			continue
		}
		kept = append(kept, record)
	}
	s.records = kept
	if len(s.records) == 0 {
		return changed
	}
	sizes := make([]int, len(s.records))
	total := 0
	for i, record := range s.records {
		line, err := encodeRecord(record)
		if err != nil {
			continue
		}
		sizes[i] = len(line) + 1
		total += sizes[i]
	}
	drop := 0
	for drop < len(s.records) && total > MaxBytes {
		total -= sizes[drop]
		drop++
	}
	if drop > 0 {
		s.records = append([]RequestRecord(nil), s.records[drop:]...)
		changed = true
	}
	return changed
}

func (s *Store) persistLocked() error {
	if s.path == "" {
		return nil
	}
	data := s.encodedLocked()
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create usage log directory: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("protect usage log directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".fmr-usage-*.tmp")
	if err != nil {
		return fmt.Errorf("create usage temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("protect usage temp file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write usage temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync usage temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close usage temp file: %w", err)
	}
	if err := replaceUsageFile(tmpName, s.path); err != nil {
		return fmt.Errorf("replace usage log: %w", err)
	}
	return os.Chmod(s.path, 0o600)
}

func replaceUsageFile(tempName, targetName string) error {
	if err := os.Rename(tempName, targetName); err == nil {
		return nil
	}
	backupName := targetName + ".bak"
	_ = os.Remove(backupName)
	if err := os.Rename(targetName, backupName); err != nil {
		return err
	}
	if err := os.Rename(tempName, targetName); err != nil {
		_ = os.Rename(backupName, targetName)
		return err
	}
	_ = os.Remove(backupName)
	return nil
}

func (s *Store) encodedLocked() []byte {
	var out []byte
	for _, record := range s.records {
		line, err := encodeRecord(record)
		if err != nil {
			continue
		}
		out = append(out, line...)
		out = append(out, '\n')
	}
	return out
}

func encodeRecord(record RequestRecord) ([]byte, error) {
	return json.Marshal(record)
}

func decodeJSONL(data []byte, dst *[]RequestRecord) error {
	for len(data) > 0 {
		line, rest, found := cutLine(data)
		data = rest
		line = []byte(strings.TrimSpace(string(line)))
		if len(line) == 0 {
			if !found {
				break
			}
			continue
		}
		var record RequestRecord
		if err := json.Unmarshal(line, &record); err != nil {
			return fmt.Errorf("%w: %v", ErrCorrupt, err)
		}
		*dst = append(*dst, normalizeRecord(record, time.Time{}))
		if !found {
			break
		}
	}
	return nil
}

func cutLine(data []byte) (line, rest []byte, found bool) {
	for i, b := range data {
		if b == '\n' {
			return data[:i], data[i+1:], true
		}
	}
	return data, nil, false
}

func normalizeRecord(record RequestRecord, now time.Time) RequestRecord {
	if record.ID == "" {
		record.ID = NewRequestID()
	}
	if record.StartedAt.IsZero() {
		if record.CompletedAt.IsZero() {
			record.StartedAt = now
		} else {
			record.StartedAt = record.CompletedAt
		}
	}
	if record.CompletedAt.IsZero() {
		record.CompletedAt = record.StartedAt
	}
	record.StartedAt = record.StartedAt.UTC()
	record.CompletedAt = record.CompletedAt.UTC()
	record.Attempts = append([]AttemptRecord(nil), record.Attempts...)
	for i := range record.Attempts {
		if record.Attempts[i].Index <= 0 {
			record.Attempts[i].Index = i + 1
		}
	}
	if len(record.Attempts) > 1 {
		record.Fallback = true
	}
	if record.TotalTokens == (TokenUsage{}) {
		for i := len(record.Attempts) - 1; i >= 0; i-- {
			if record.Attempts[i].Committed && record.Attempts[i].Tokens != (TokenUsage{}) {
				record.TotalTokens = record.Attempts[i].Tokens
				break
			}
		}
	}
	return record
}

func cloneRecord(record RequestRecord) RequestRecord {
	record.Attempts = append([]AttemptRecord(nil), record.Attempts...)
	return record
}

func matches(record RequestRecord, q Query) bool {
	at := record.StartedAt
	if !q.From.IsZero() && at.Before(q.From) {
		return false
	}
	if !q.To.IsZero() && at.After(q.To) {
		return false
	}
	if q.Provider != "" && !strings.EqualFold(record.Provider, q.Provider) {
		return false
	}
	if q.Model != "" && string(record.FinalModel) != q.Model {
		return false
	}
	if q.Result != "" && record.Result != q.Result {
		return false
	}
	if q.Fallback != nil && record.Fallback != *q.Fallback {
		return false
	}
	return true
}

var requestSequence atomic.Uint64

// NewRequestID creates a non-secret, process-unique request identifier.
func NewRequestID() string {
	var raw [12]byte
	if _, err := io.ReadFull(rand.Reader, raw[:]); err == nil {
		return "fmr-" + hex.EncodeToString(raw[:])
	}
	return fmt.Sprintf("fmr-%x-%d", raw[:], requestSequence.Add(1))
}
