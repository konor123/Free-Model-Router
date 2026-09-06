package packaging

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestWindowsManifestRoundTripAndRuntimeContract(t *testing.T) {
	dir := t.TempDir()
	binaryPath := filepath.Join(dir, "Free-Model-Router.exe")
	metadataPath := filepath.Join(dir, "sidecar.metadata.json")
	manifestPath := filepath.Join(dir, "manifest.json")
	if err := os.WriteFile(binaryPath, []byte("sidecar"), 0o755); err != nil {
		t.Fatal(err)
	}
	metadata, err := Generate(binaryPath, metadataPath, "x86_64-pc-windows-msvc", "v0.18.0", "1", OwnershipDesktopManaged)
	if err != nil {
		t.Fatal(err)
	}
	manifest := NewWindowsManifestWithDesktop(metadata, "Free-Model-Router-desktop.exe", true)
	if err := manifest.Validate(); err != nil {
		t.Fatal(err)
	}
	if !manifest.DesktopIncluded {
		t.Fatal("manifest did not record the supplied desktop artifact")
	}
	if manifest.RuntimeRequirements.Node || manifest.RuntimeRequirements.NPM || manifest.RuntimeRequirements.Rust {
		t.Fatalf("runtime requirements = %+v", manifest.RuntimeRequirements)
	}
	if !manifest.BuildRequirements.Node || !manifest.BuildRequirements.NPM || !manifest.BuildRequirements.Rust {
		t.Fatalf("build requirements = %+v", manifest.BuildRequirements)
	}
	if err := WriteWindowsManifest(manifestPath, manifest); err != nil {
		t.Fatal(err)
	}
	loaded, err := ReadWindowsManifest(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if loaded != manifest {
		t.Fatalf("loaded manifest = %+v, generated = %+v", loaded, manifest)
	}
}

func TestWindowsManifestRejectsMismatchedSidecar(t *testing.T) {
	metadata := SidecarMetadata{
		SchemaVersion: CurrentSchemaVersion,
		Product:       ProductName,
		Artifact:      "Free-Model-Router.exe",
		TargetTriple:  "x86_64-pc-windows-msvc",
		BinaryVersion: "dev",
		APIVersion:    "1",
		APIMajor:      "1",
		SHA256:        "0000000000000000000000000000000000000000000000000000000000000000",
		Ownership:     OwnershipDesktopManaged,
	}
	manifest := NewWindowsManifestWithDesktop(metadata, "Free-Model-Router-desktop.exe", true)
	manifest.Sidecar.Artifact = "other.exe"
	if err := manifest.Validate(); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("mismatched artifact error = %v", err)
	}
	manifest = NewWindowsManifestWithDesktop(metadata, "Free-Model-Router-desktop.exe", true)
	manifest.Platform = "linux"
	if err := manifest.Validate(); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("wrong platform error = %v", err)
	}
	manifest = NewWindowsManifestWithDesktop(metadata, "desktop.bin", true)
	if err := manifest.Validate(); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("wrong desktop extension error = %v", err)
	}
}

func TestCheckedInWindowsManifestSchemaIsValid(t *testing.T) {
	data, err := os.ReadFile("../../packaging/windows/manifest.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatalf("manifest schema JSON: %v", err)
	}
	for _, want := range []string{"schemaVersion", "desktopIncluded", "sidecar", "runtimeRequirements", "buildRequirements"} {
		found := false
		for _, name := range schema.Required {
			if name == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("manifest schema required fields missing %q: %v", want, schema.Required)
		}
	}
}
