package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultsPassValidation(t *testing.T) {
	if err := Defaults().validate(); err != nil {
		t.Fatalf("defaults invalid: %v", err)
	}
}

func TestLoadMissingFileUsesDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nonexistent", "config.json")
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load on missing file: %v", err)
	}
	if cfg.Bind != "127.0.0.1:8787" {
		t.Fatalf("expected default bind, got %q", cfg.Bind)
	}
}

func TestSaveLoadRoundTripAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	in := &Config{Bind: "127.0.0.1:9999", LogLevel: "debug"}
	if err := Save(path, in); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// No temp leftovers.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected exactly 1 file after atomic save, got %d", len(entries))
	}

	out, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if out.Bind != in.Bind || out.LogLevel != in.LogLevel {
		t.Fatalf("round trip mismatch: %+v != %+v", out, in)
	}
}

func TestLoadRejectsInvalidLogLevel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"logLevel":"loud"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected error for invalid logLevel")
	}
}
