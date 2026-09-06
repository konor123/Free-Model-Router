package config_test

import (
	"path/filepath"
	"testing"

	"github.com/konor123/Free-Model-Router/internal/config"
)

func TestControlAndSecurityDefaults(t *testing.T) {
	defaults := config.Defaults()
	if defaults.ManagementBind != "127.0.0.1:8788" {
		t.Fatalf("management bind = %q", defaults.ManagementBind)
	}
	if defaults.Bind != "127.0.0.1:8787" {
		t.Fatalf("inference bind = %q", defaults.Bind)
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := config.Save(path, defaults); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ManagementBind != defaults.ManagementBind {
		t.Fatalf("management bind round trip = %q", loaded.ManagementBind)
	}
}
