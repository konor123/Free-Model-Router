package packaging

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerateWritesValidatedSidecarMetadata(t *testing.T) {
	dir := t.TempDir()
	binaryPath := filepath.Join(dir, "Free-Model-Router.exe")
	metadataPath := filepath.Join(dir, "Free-Model-Router.exe.metadata.json")
	if err := os.WriteFile(binaryPath, []byte("gateway-binary"), 0o755); err != nil {
		t.Fatal(err)
	}

	metadata, err := Generate(binaryPath, metadataPath, "x86_64-pc-windows-msvc", "v0.18.0", "1.2", OwnershipDesktopManaged)
	if err != nil {
		t.Fatal(err)
	}
	if metadata.SchemaVersion != CurrentSchemaVersion || metadata.TargetTriple != "x86_64-pc-windows-msvc" || metadata.BinaryVersion != "v0.18.0" || metadata.APIMajor != "1" || metadata.Ownership != OwnershipDesktopManaged {
		t.Fatalf("metadata = %+v", metadata)
	}
	if len(metadata.SHA256) != 64 || metadata.SHA256 != strings.ToLower(metadata.SHA256) {
		t.Fatalf("metadata sha256 = %q", metadata.SHA256)
	}
	if err := metadata.VerifyFile(binaryPath); err != nil {
		t.Fatalf("verify generated metadata: %v", err)
	}
	loaded, err := Read(metadataPath)
	if err != nil {
		t.Fatal(err)
	}
	if loaded != metadata {
		t.Fatalf("loaded metadata = %+v, generated = %+v", loaded, metadata)
	}

	first, err := os.ReadFile(metadataPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Generate(binaryPath, metadataPath, "x86_64-pc-windows-msvc", "v0.18.0", "1.2", OwnershipDesktopManaged); err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(metadataPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatal("metadata output is not deterministic")
	}
}

func TestMetadataRejectsTamperingAndInvalidContracts(t *testing.T) {
	dir := t.TempDir()
	binaryPath := filepath.Join(dir, "sidecar.exe")
	metadataPath := filepath.Join(dir, "sidecar.metadata.json")
	if err := os.WriteFile(binaryPath, []byte("original"), 0o755); err != nil {
		t.Fatal(err)
	}
	metadata, err := Generate(binaryPath, metadataPath, "x86_64-pc-windows-msvc", "dev", "1", OwnershipExternal)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binaryPath, []byte("tampered"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(metadata.VerifyFile(binaryPath), ErrChecksumMismatch) {
		t.Fatalf("tampered binary error = %v", metadata.VerifyFile(binaryPath))
	}

	invalid := metadata
	invalid.Ownership = "unknown"
	if err := invalid.Validate(); !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("invalid ownership error = %v", err)
	}
	invalid = metadata
	invalid.APIVersion = "2"
	if err := invalid.Validate(); !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("API mismatch error = %v", err)
	}
	invalid = metadata
	invalid.SchemaVersion = CurrentSchemaVersion + 1
	if err := invalid.Validate(); !errors.Is(err, ErrUnsupportedSchema) {
		t.Fatalf("future schema error = %v", err)
	}
}

func TestReadRejectsMalformedMetadata(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metadata.json")
	if err := os.WriteFile(path, []byte(`{"schemaVersion":1,"unexpected":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(path); !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("malformed metadata error = %v", err)
	}

	data, err := json.Marshal(SidecarMetadata{SchemaVersion: CurrentSchemaVersion})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(path); !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("incomplete metadata error = %v", err)
	}
}
