package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/konor123/Free-Model-Router/internal/control"
)

func TestInstanceLockSecondAcquireAndRelease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "desktop.lease")
	first := NewInstanceLock(path)
	first.SetProcessAlive(func(int) bool { return true })
	owner := Lease{PID: 101, InstanceID: "one", Address: "127.0.0.1:8788", Ownership: OwnershipDesktopManaged, Profile: "default"}
	if _, err := first.Acquire(owner); err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	second := NewInstanceLock(path)
	second.SetProcessAlive(func(pid int) bool { return pid == owner.PID })
	_, err := second.Acquire(Lease{PID: 202, InstanceID: "two"})
	if !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second acquire error = %v, want ErrAlreadyRunning", err)
	}
	if err := first.Release(); err != nil {
		t.Fatalf("release: %v", err)
	}
	if _, err := second.Acquire(Lease{PID: 202, InstanceID: "two"}); err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	defer second.Release()
}

func TestInstanceLockRecoversStaleLease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "desktop.lease")
	stale := Lease{PID: 303, InstanceID: "stale", StartedAt: time.Now().Add(-time.Hour)}
	writeLeaseForTest(t, path, stale)
	lock := NewInstanceLock(path)
	lock.SetProcessAlive(func(int) bool { return false })
	if _, err := lock.Acquire(Lease{PID: 404, InstanceID: "fresh", Ownership: OwnershipDesktopManaged}); err != nil {
		t.Fatalf("stale acquire: %v", err)
	}
	defer lock.Release()
	got := readLeaseForTest(t, path)
	if got.InstanceID != "fresh" || got.PID != 404 {
		t.Fatalf("lease after recovery = %+v", got)
	}
}

func TestInstanceLockNeverStealsLiveLease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "desktop.lease")
	live := Lease{PID: 505, InstanceID: "live"}
	writeLeaseForTest(t, path, live)
	lock := NewInstanceLock(path)
	lock.SetProcessAlive(func(pid int) bool { return pid == 505 })
	_, err := lock.Acquire(Lease{PID: 606, InstanceID: "intruder"})
	var already *AlreadyRunningError
	if !errors.As(err, &already) || already.Lease.InstanceID != "live" {
		t.Fatalf("error = %v, want live lease details", err)
	}
	if got := readLeaseForTest(t, path); got.InstanceID != "live" {
		t.Fatalf("live lease was replaced: %+v", got)
	}
}

func TestAttachOrStartCompatibleExternal(t *testing.T) {
	var starts atomic.Int32
	connector := GatewayConnector{
		Address: "127.0.0.1:8788",
		Probe: func(context.Context, string) (control.StatusResponse, error) {
			return control.StatusResponse{APIVersion: "1.2", BuildVersion: "b", InstanceID: "external"}, nil
		},
		Starter: testStarterFunc(func(context.Context, string) (Process, error) {
			starts.Add(1)
			return fakeProcess{}, nil
		}),
	}
	connection, err := connector.AttachOrStart(context.Background(), "127.0.0.1:8788")
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	if connection.Ownership != OwnershipExternal || starts.Load() != 0 {
		t.Fatalf("connection = %+v, starts = %d", connection, starts.Load())
	}
	if err := connection.Close(context.Background()); err != nil {
		t.Fatalf("external close: %v", err)
	}
}

func TestAttachOrStartRejectsIncompatibleExternalWithoutStopping(t *testing.T) {
	stopped := atomic.Bool{}
	connector := GatewayConnector{Address: "127.0.0.1:8788", Probe: func(context.Context, string) (control.StatusResponse, error) {
		return control.StatusResponse{APIVersion: "2", InstanceID: "external"}, nil
	}, Starter: testStarterFunc(func(context.Context, string) (Process, error) {
		return fakeProcess{stop: func(context.Context) error { stopped.Store(true); return nil }}, nil
	})}
	_, err := connector.AttachOrStart(context.Background(), "127.0.0.1:8788")
	var mismatch *VersionMismatchError
	if !errors.As(err, &mismatch) || mismatch.Actual != "2" {
		t.Fatalf("error = %v, want version mismatch", err)
	}
	if stopped.Load() {
		t.Fatal("incompatible external gateway was stopped")
	}
}

func TestAttachOrStartManagedStopsOnlyManagedProcess(t *testing.T) {
	var probes atomic.Int32
	var stopped atomic.Bool
	process := fakeProcess{stop: func(context.Context) error { stopped.Store(true); return nil }}
	connector := GatewayConnector{Address: "127.0.0.1:8788", PollInterval: time.Millisecond, Probe: func(context.Context, string) (control.StatusResponse, error) {
		if probes.Add(1) == 1 {
			return control.StatusResponse{}, errors.New("not ready")
		}
		return control.StatusResponse{APIVersion: control.APIVersion, BuildVersion: "managed", InstanceID: "managed"}, nil
	}, Starter: testStarterFunc(func(context.Context, string) (Process, error) { return process, nil })}
	connection, err := connector.AttachOrStart(context.Background(), "127.0.0.1:8788")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if connection.Ownership != OwnershipDesktopManaged {
		t.Fatalf("ownership = %q", connection.Ownership)
	}
	if err := connection.Close(context.Background()); err != nil {
		t.Fatalf("managed close: %v", err)
	}
	if !stopped.Load() {
		t.Fatal("managed process was not stopped")
	}
}

func TestTraySnapshotAndProviderMenu(t *testing.T) {
	status := control.StatusResponse{APIVersion: "1.1", BuildVersion: "build-7", InstanceID: "instance-7"}
	providers := []control.ProviderResponse{{ID: "zeta", Models: 2, Routes: 3, Enabled: false}, {ID: "alpha", Models: 4, Routes: 5, Enabled: true}}
	snapshot := BuildTraySnapshot(status, providers, OwnershipExternal)
	for _, want := range []string{"API 1.1", "build-7", "instance-7", "external"} {
		if !strings.Contains(snapshot.Tooltip, want) {
			t.Fatalf("tooltip %q missing %q", snapshot.Tooltip, want)
		}
	}
	if len(snapshot.ProviderMenu) != 2 || snapshot.ProviderMenu[0].ID != "alpha" || snapshot.ProviderMenu[1].ID != "zeta" {
		t.Fatalf("providers = %+v, want sorted entries", snapshot.ProviderMenu)
	}
	if snapshot.ProviderMenu[1].Enabled {
		t.Fatal("disabled provider marked enabled")
	}
}

func TestFileAutostartEnableDisable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "startup", "fmr.cmd")
	autostart := NewFileAutostart(path, `"C:\\FMR\\fmr.exe" --desktop`)
	if enabled, err := autostart.Enabled(); err != nil || enabled {
		t.Fatal("autostart unexpectedly enabled")
	}
	if err := autostart.Enable(); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if enabled, err := autostart.Enabled(); err != nil || !enabled {
		t.Fatal("autostart was not enabled")
	}
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), "fmr.exe") {
		t.Fatalf("startup content = %q, err = %v", data, err)
	}
	if err := autostart.Disable(); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if enabled, err := autostart.Enabled(); err != nil || enabled {
		t.Fatal("autostart remained enabled")
	}
}

type fakeProcess struct {
	stop func(context.Context) error
}

func (p fakeProcess) PID() int { return 9876 }

func (p fakeProcess) Stop() error {
	if p.stop != nil {
		return p.stop(context.Background())
	}
	return nil
}

type testStarterFunc func(context.Context, string) (Process, error)

func (f testStarterFunc) Start(ctx context.Context, address string) (Process, error) {
	return f(ctx, address)
}

func writeLeaseForTest(t *testing.T, path string, lease Lease) {
	t.Helper()
	data, err := json.Marshal(lease)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func readLeaseForTest(t *testing.T, path string) Lease {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var lease Lease
	err = json.Unmarshal(data, &lease)
	if err != nil {
		t.Fatal(err)
	}
	return lease
}
