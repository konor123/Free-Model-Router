package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/konor123/Free-Model-Router/internal/packaging"
)

func TestRunGeneratesAndValidatesWindowsPackageMetadata(t *testing.T) {
	dir := t.TempDir()
	binaryPath := filepath.Join(dir, "Free-Model-Router.exe")
	metadataPath := filepath.Join(dir, "Free-Model-Router.exe.metadata.json")
	manifestPath := filepath.Join(dir, "manifest.json")
	if err := os.WriteFile(binaryPath, []byte("gateway"), 0o755); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	if err := run([]string{
		"-binary", binaryPath,
		"-metadata", metadataPath,
		"-manifest", manifestPath,
		"-target", "x86_64-pc-windows-msvc",
		"-version", "v0.18.0",
		"-api-version", "1",
		"-ownership", "desktop-managed",
		"-desktop-included",
	}, &output, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `"sha256"`) {
		t.Fatalf("generation output = %s", output.String())
	}
	if err := run([]string{
		"-validate",
		"-binary", binaryPath,
		"-metadata", metadataPath,
		"-manifest", manifestPath,
	}, &output, &output); err != nil {
		t.Fatal(err)
	}
	manifest, err := packaging.ReadWindowsManifest(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if !manifest.DesktopIncluded {
		t.Fatal("CLI did not record the supplied desktop artifact")
	}
}

func TestRunRequiresBinaryAndMetadataPaths(t *testing.T) {
	if err := run(nil, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
		t.Fatal("expected missing argument error")
	}
}
