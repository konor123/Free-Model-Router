// Package desktop contains dependency-free lifecycle contracts used by a desktop
// shell. Native tray rendering remains an integration concern for Tauri, while
// lock, attach, ownership, and autostart semantics are testable here.
package desktop

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/konor123/Free-Model-Router/internal/control"
)

// Ownership identifies who started and therefore may stop a gateway.
type Ownership string

const (
	OwnershipDesktopManaged Ownership = "desktop-managed"
	OwnershipExternal       Ownership = "external"
)

var (
	ErrAlreadyRunning      = errors.New("desktop instance already running")
	ErrGatewayUnavailable  = errors.New("gateway is unavailable")
	ErrGatewayUnauthorized = errors.New("gateway control authentication failed")
)

// Lease is the on-disk ownership record for one desktop profile.
type Lease struct {
	PID        int       `json:"pid"`
	InstanceID string    `json:"instanceId"`
	Address    string    `json:"address"`
	Ownership  Ownership `json:"ownership"`
	Profile    string    `json:"profile,omitempty"`
	StartedAt  time.Time `json:"startedAt"`
}

// AlreadyRunningError returns the live lease that prevented acquisition.
type AlreadyRunningError struct{ Lease Lease }

func (e *AlreadyRunningError) Error() string {
	if e == nil {
		return ErrAlreadyRunning.Error()
	}
	return fmt.Sprintf("%s: pid=%d instance=%s", ErrAlreadyRunning, e.Lease.PID, e.Lease.InstanceID)
}

func (e *AlreadyRunningError) Unwrap() error { return ErrAlreadyRunning }

// InstanceLock uses an exclusive lease file. ProcessAlive is injectable so
// stale-lock recovery and live-process non-stealing remain deterministic in
// tests and can be replaced by a platform-specific probe by the desktop shell.
type InstanceLock struct {
	Path         string
	Lease        Lease
	ProcessAlive func(int) bool
	Now          func() time.Time

	mu           sync.Mutex
	path         string
	processAlive func(int) bool
	file         *os.File
	lease        Lease
	acquired     bool
}

// NewInstanceLock creates a lock for path. The parent directory is created by
// Acquire, not by the constructor.
func NewInstanceLock(path string, initial ...Lease) *InstanceLock {
	var lease Lease
	if len(initial) > 0 {
		lease = initial[0]
	}
	return &InstanceLock{Path: strings.TrimSpace(path), Lease: lease, path: strings.TrimSpace(path), lease: lease, processAlive: defaultProcessAlive}
}

// SetProcessAlive replaces the process liveness probe used by future acquires.
func (l *InstanceLock) SetProcessAlive(probe func(int) bool) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if probe == nil {
		probe = defaultProcessAlive
	}
	l.ProcessAlive = probe
	l.processAlive = probe
}

// Acquire obtains the lease or returns the existing live owner. A stale lease
// is atomically renamed aside before a replacement is attempted.
func (l *InstanceLock) Acquire(initial ...Lease) (Lease, error) {
	if l == nil {
		return Lease{}, errors.New("instance lock must not be nil")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.acquired {
		return l.lease, nil
	}
	if len(initial) > 0 {
		l.Lease = initial[0]
		l.lease = initial[0]
	}
	if l.Path != "" {
		l.path = strings.TrimSpace(l.Path)
	}
	if l.path == "" {
		return Lease{}, errors.New("instance lock path must not be empty")
	}
	if err := os.MkdirAll(filepath.Dir(l.path), 0o700); err != nil {
		return Lease{}, fmt.Errorf("create instance lock directory: %w", err)
	}
	lease := l.lease
	if lease.StartedAt.IsZero() {
		now := l.Now
		if now == nil {
			now = time.Now
		}
		lease.StartedAt = now().UTC()
	}
	lease = normalizeLease(lease)
	for attempt := 0; attempt < 4; attempt++ {
		file, err := os.OpenFile(l.path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			data, marshalErr := json.MarshalIndent(lease, "", "  ")
			if marshalErr != nil {
				_ = file.Close()
				_ = os.Remove(l.path)
				return Lease{}, marshalErr
			}
			if _, writeErr := file.Write(append(data, '\n')); writeErr != nil {
				_ = file.Close()
				_ = os.Remove(l.path)
				return Lease{}, fmt.Errorf("write instance lease: %w", writeErr)
			}
			if syncErr := file.Sync(); syncErr != nil {
				_ = file.Close()
				_ = os.Remove(l.path)
				return Lease{}, fmt.Errorf("sync instance lease: %w", syncErr)
			}
			l.file, l.lease, l.Lease, l.acquired = file, lease, lease, true
			return lease, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return Lease{}, fmt.Errorf("create instance lease: %w", err)
		}
		existing, readErr := readLease(l.path)
		if readErr != nil {
			return Lease{}, fmt.Errorf("read existing instance lease: %w", readErr)
		}
		probe := l.ProcessAlive
		if probe == nil {
			probe = l.processAlive
		}
		if probe == nil {
			probe = defaultProcessAlive
		}
		if existing.PID > 0 && probe(existing.PID) {
			return existing, &AlreadyRunningError{Lease: existing}
		}
		stalePath := fmt.Sprintf("%s.stale-%d-%d", l.path, os.Getpid(), time.Now().UnixNano())
		if renameErr := os.Rename(l.path, stalePath); renameErr != nil {
			continue
		}
		_ = os.Remove(stalePath)
	}
	return Lease{}, errors.New("instance lease changed while recovering stale state")
}

// Release relinquishes only a lease acquired by this lock object.
func (l *InstanceLock) Release() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.acquired {
		return nil
	}
	var firstErr error
	if l.file != nil {
		firstErr = l.file.Close()
	}
	if current, readErr := readLease(l.path); readErr == nil {
		if current.PID == l.lease.PID && current.InstanceID == l.lease.InstanceID {
			if removeErr := os.Remove(l.path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) && firstErr == nil {
				firstErr = removeErr
			}
		}
	} else if !errors.Is(readErr, os.ErrNotExist) && firstErr == nil {
		firstErr = readErr
	}
	l.file, l.acquired = nil, false
	return firstErr
}

func normalizeLease(lease Lease) Lease {
	if lease.PID <= 0 {
		lease.PID = os.Getpid()
	}
	if lease.InstanceID == "" {
		lease.InstanceID = newDesktopInstanceID()
	}
	if lease.Ownership == "" {
		lease.Ownership = OwnershipDesktopManaged
	}
	if lease.StartedAt.IsZero() {
		lease.StartedAt = time.Now().UTC()
	}
	return lease
}

func readLease(path string) (Lease, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Lease{}, err
	}
	var lease Lease
	if err := json.Unmarshal(data, &lease); err != nil {
		return Lease{}, errors.New("instance lease is corrupt")
	}
	if lease.PID < 0 || lease.InstanceID == "" {
		return Lease{}, errors.New("instance lease is invalid")
	}
	return lease, nil
}

func defaultProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	if runtime.GOOS == "windows" {
		out, err := exec.Command("tasklist", "/FI", "PID eq "+strconv.Itoa(pid), "/NH").CombinedOutput()
		return err == nil && bytes.Contains(out, []byte(strconv.Itoa(pid)))
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return process.Signal(syscall.Signal(0)) == nil
}

func newDesktopInstanceID() string {
	var raw [12]byte
	if _, err := rand.Read(raw[:]); err == nil {
		return "desktop-" + hex.EncodeToString(raw[:])
	}
	return fmt.Sprintf("desktop-%d", time.Now().UnixNano())
}

// Process is a gateway process owned by the desktop lifecycle.
type Process interface {
	Stop() error
}

// Starter is the injectable boundary for launching a managed gateway.
type Starter interface {
	Start(context.Context, string) (Process, error)
}

// ProbeFunc and StartFunc are injectable lifecycle boundaries.
type ProbeFunc func(context.Context, string) (control.StatusResponse, error)
type StartFunc func(context.Context, string) (Process, error)

// GatewayConnector attaches to a compatible existing gateway or starts and
// polls a managed gateway. It never stops an external process.
type GatewayConnector struct {
	Address      string
	Token        string
	APIVersion   string
	Probe        ProbeFunc
	Starter      Starter
	PollInterval time.Duration
	ProbeTimeout time.Duration
}

// GatewayConnection is the ownership-aware result of AttachOrStart.
type GatewayConnection struct {
	Status    control.StatusResponse
	Address   string
	Ownership Ownership
	Process   Process
	closeOnce sync.Once
	closeErr  error
}

// VersionMismatchError prevents a desktop from attaching to an incompatible
// control API and deliberately carries only public status fields.
type VersionMismatchError struct {
	Expected string
	Actual   string
	Status   control.StatusResponse
}

func (e *VersionMismatchError) Error() string {
	if e == nil {
		return "gateway API version mismatch"
	}
	return fmt.Sprintf("gateway API major mismatch: desktop=%s gateway=%s", control.APIMajor(e.Expected), control.APIMajor(e.Actual))
}

// Stop stops only a desktop-managed process. External gateways are left
// untouched on desktop quit.
func (c *GatewayConnection) Stop() error {
	if c == nil {
		return nil
	}
	c.closeOnce.Do(func() {
		if c.Ownership == OwnershipDesktopManaged && c.Process != nil {
			c.closeErr = c.Process.Stop()
		}
	})
	return c.closeErr
}

// Close is a context-compatible alias for desktop shells.
func (c *GatewayConnection) Close(context.Context) error { return c.Stop() }

// AttachOrStart performs the Phase 15 attach/version/ownership state machine.
func (c GatewayConnector) AttachOrStart(ctx context.Context, address string) (*GatewayConnection, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	address = strings.TrimSpace(address)
	if address == "" {
		address = strings.TrimSpace(c.Address)
	}
	if address == "" {
		return nil, errors.New("gateway address is required")
	}
	status, err := c.probe(ctx, address)
	if err == nil {
		return c.externalConnection(address, status)
	}
	var httpErr *control.HTTPError
	if errors.As(err, &httpErr) && (httpErr.StatusCode == http.StatusUnauthorized || httpErr.StatusCode == http.StatusForbidden) {
		return nil, fmt.Errorf("%w: %w", ErrGatewayUnauthorized, err)
	}
	if c.Starter == nil {
		return nil, fmt.Errorf("%w: %v", ErrGatewayUnavailable, err)
	}
	process, startErr := c.Starter.Start(ctx, address)
	if startErr != nil {
		return nil, fmt.Errorf("start managed gateway: %w", startErr)
	}
	if process == nil {
		return nil, errors.New("start managed gateway: process is nil")
	}
	status, err = c.waitForStatus(ctx, address)
	if err != nil {
		_ = process.Stop()
		return nil, fmt.Errorf("managed gateway did not become ready: %w", err)
	}
	if !control.CompatibleAPIMajor(c.expectedAPIVersion(), status.APIVersion) {
		_ = process.Stop()
		return nil, &VersionMismatchError{Expected: c.expectedAPIVersion(), Actual: status.APIVersion, Status: status}
	}
	return &GatewayConnection{Status: status, Address: address, Ownership: OwnershipDesktopManaged, Process: process}, nil
}

func (c GatewayConnector) externalConnection(address string, status control.StatusResponse) (*GatewayConnection, error) {
	if !control.CompatibleAPIMajor(c.expectedAPIVersion(), status.APIVersion) {
		return nil, &VersionMismatchError{Expected: c.expectedAPIVersion(), Actual: status.APIVersion, Status: status}
	}
	return &GatewayConnection{Status: status, Address: address, Ownership: OwnershipExternal}, nil
}

func (c *GatewayConnector) expectedAPIVersion() string {
	if strings.TrimSpace(c.APIVersion) == "" {
		return control.APIVersion
	}
	return c.APIVersion
}

func (c GatewayConnector) probe(ctx context.Context, address string) (control.StatusResponse, error) {
	if c.Probe != nil {
		if c.ProbeTimeout <= 0 {
			return c.Probe(ctx, address)
		}
		probeCtx, cancel := context.WithTimeout(ctx, c.ProbeTimeout)
		defer cancel()
		return c.Probe(probeCtx, address)
	}
	client := control.NewClient(address, c.Token)
	body, err := client.Get(ctx, "/_fmr/status")
	if err != nil {
		return control.StatusResponse{}, err
	}
	var status control.StatusResponse
	if err := json.Unmarshal(body, &status); err != nil {
		return control.StatusResponse{}, fmt.Errorf("decode gateway status: %w", err)
	}
	return status, nil
}

func (c GatewayConnector) waitForStatus(ctx context.Context, address string) (control.StatusResponse, error) {
	interval := c.PollInterval
	if interval <= 0 {
		interval = 100 * time.Millisecond
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	var lastErr error
	for {
		status, err := c.probe(ctx, address)
		if err == nil {
			return status, nil
		}
		lastErr = err
		select {
		case <-ctx.Done():
			if lastErr == nil {
				lastErr = ctx.Err()
			}
			return control.StatusResponse{}, lastErr
		case <-ticker.C:
		}
	}
}

// CommandStarter starts the gateway executable as a desktop-managed child.
type CommandStarter struct {
	Binary string
	Args   []string
	Stdout io.Writer
	Stderr io.Writer
}

func (s CommandStarter) Start(ctx context.Context, _ string) (Process, error) {
	if strings.TrimSpace(s.Binary) == "" {
		return nil, errors.New("gateway binary must not be empty")
	}
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	command := exec.Command(s.Binary, s.Args...)
	command.Stdout, command.Stderr = s.Stdout, s.Stderr
	if err := command.Start(); err != nil {
		return nil, err
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	return &commandProcess{command: command, done: done}, nil
}

type commandProcess struct {
	command *exec.Cmd
	done    <-chan error
	once    sync.Once
	err     error
}

func (p *commandProcess) PID() int {
	if p == nil || p.command == nil || p.command.Process == nil {
		return 0
	}
	return p.command.Process.Pid
}

func (p *commandProcess) Stop() error {
	if p == nil || p.command == nil || p.command.Process == nil {
		return nil
	}
	p.once.Do(func() {
		if err := p.command.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			p.err = err
			return
		}
		err := <-p.done
		if err != nil && !errors.Is(err, os.ErrProcessDone) {
			p.err = err
		}
	})
	return p.err
}

// ProviderMenuEntry is a safe tray menu item derived from control data.
type ProviderMenuEntry struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	Models  int    `json:"models"`
	Routes  int    `json:"routes"`
	Enabled bool   `json:"enabled"`
}

// TraySnapshot is the shell-facing state for tooltip and provider menu
// rendering. It contains no token or provider secret.
type TraySnapshot struct {
	Tooltip      string              `json:"tooltip"`
	APIVersion   string              `json:"apiVersion"`
	BuildVersion string              `json:"buildVersion"`
	InstanceID   string              `json:"instanceId"`
	Ownership    Ownership           `json:"ownership"`
	Address      string              `json:"address"`
	Ready        bool                `json:"ready"`
	Providers    []ProviderMenuEntry `json:"providers"`
	ProviderMenu []ProviderMenuEntry `json:"providerMenu,omitempty"`
}

// BuildTraySnapshot turns public status/provider DTOs into deterministic shell
// state. Provider entries are sorted by ID for stable menus.
func BuildProviderMenu(providers []control.ProviderResponse) []ProviderMenuEntry {
	menu := make([]ProviderMenuEntry, 0, len(providers))
	for _, provider := range providers {
		label := fmt.Sprintf("%s (%d models, %d routes)", provider.ID, provider.Models, provider.Routes)
		if !provider.Enabled {
			label += " [disabled]"
		}
		menu = append(menu, ProviderMenuEntry{ID: provider.ID, Label: label, Models: provider.Models, Routes: provider.Routes, Enabled: provider.Enabled})
	}
	sort.Slice(menu, func(i, j int) bool { return menu[i].ID < menu[j].ID })
	return menu
}

// Tooltip returns a non-secret shell tooltip for the current connection.
func Tooltip(status control.StatusResponse, ownership Ownership) string {
	return fmt.Sprintf("Free-Model-Router | API %s | build %s | instance %s | ownership %s", status.APIVersion, status.BuildVersion, status.InstanceID, ownership)
}

func BuildTraySnapshot(status control.StatusResponse, providers []control.ProviderResponse, ownership Ownership, addresses ...string) TraySnapshot {
	menu := BuildProviderMenu(providers)
	address := ""
	if len(addresses) > 0 {
		address = addresses[0]
	}
	build := strings.TrimSpace(status.BuildVersion)
	if build == "" {
		build = "unknown"
	}
	ownershipText := string(ownership)
	if ownershipText == "" {
		ownershipText = string(OwnershipExternal)
	}
	return TraySnapshot{
		Tooltip:    Tooltip(status, Ownership(ownershipText)),
		APIVersion: status.APIVersion, BuildVersion: status.BuildVersion,
		InstanceID: status.InstanceID, Ownership: ownership, Address: address,
		Ready: status.InstanceID != "", Providers: menu, ProviderMenu: menu,
	}
}

// FileAutostart is a dependency-free startup entry adapter. On Windows the
// default path can point at the user's Startup directory; tests and other
// shells may inject any path.
type FileAutostart struct {
	StartupFilePath string
	Command         string
}

func NewFileAutostart(path, command string) *FileAutostart {
	return &FileAutostart{StartupFilePath: strings.TrimSpace(path), Command: strings.TrimSpace(command)}
}

func (a *FileAutostart) Path() string {
	if a == nil {
		return ""
	}
	return a.StartupFilePath
}

func (a *FileAutostart) Enabled() (bool, error) {
	if a == nil {
		return false, nil
	}
	path := strings.TrimSpace(a.StartupFilePath)
	if path == "" {
		return false, errors.New("startup file path is required")
	}
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, fmt.Errorf("check autostart: %w", err)
}

func (a *FileAutostart) Enable() error {
	if a == nil || a.StartupFilePath == "" || a.Command == "" {
		return errors.New("autostart path and command are required")
	}
	if err := os.MkdirAll(filepath.Dir(a.StartupFilePath), 0o700); err != nil {
		return fmt.Errorf("create autostart directory: %w", err)
	}
	content := "@echo off\r\nstart \"\" " + a.Command + "\r\n"
	if err := atomicWrite(a.StartupFilePath, []byte(content)); err != nil {
		return fmt.Errorf("write autostart entry: %w", err)
	}
	return nil
}

func (a *FileAutostart) Disable() error {
	if a == nil || a.StartupFilePath == "" {
		return nil
	}
	if err := os.Remove(a.StartupFilePath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove autostart entry: %w", err)
	}
	return nil
}

// DefaultAutostart returns a user-scoped startup script location. Native shell
// integration can use this path without requiring Node, npm, or Rust at runtime.
func DefaultAutostart(name, command string) (*FileAutostart, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("autostart name must not be empty")
	}
	base := ""
	if runtime.GOOS == "windows" {
		base = os.Getenv("APPDATA")
		if base == "" {
			return nil, errors.New("APPDATA is not set")
		}
		base = filepath.Join(base, "Microsoft", "Windows", "Start Menu", "Programs", "Startup")
	} else {
		var err error
		base, err = os.UserConfigDir()
		if err != nil {
			return nil, err
		}
		base = filepath.Join(base, "fmr", "autostart")
	}
	return NewFileAutostart(filepath.Join(base, name+".cmd"), command), nil
}

func atomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".fmr-autostart-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err == nil {
		return nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Rename(tmpName, path)
}
