// Package packaging contains release-time contracts for desktop sidecars.
//
// The package is intentionally independent of Tauri and frontend tooling. It
// can generate and verify the metadata that a desktop shell consumes without
// adding Node, npm, Rust, or any other runtime dependency to the gateway.
package packaging

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/konor123/Free-Model-Router/internal/control"
)

const (
	// CurrentSchemaVersion is the sidecar metadata schema understood by this
	// release tooling and the future desktop shell.
	CurrentSchemaVersion = 1
	// ProductName is the stable product identifier embedded in release metadata.
	ProductName = "Free-Model-Router"
)

var (
	ErrInvalidMetadata   = errors.New("invalid sidecar metadata")
	ErrUnsupportedSchema = errors.New("unsupported sidecar metadata schema")
	ErrChecksumMismatch  = errors.New("sidecar checksum mismatch")
)

// Ownership records who owns the sidecar process lifecycle. An external
// gateway may be attached to but must never be stopped by the desktop shell.
type Ownership string

const (
	OwnershipDesktopManaged Ownership = "desktop-managed"
	OwnershipExternal       Ownership = "external"
)

// SidecarMetadata is the signed-release-independent identity contract for a Go
// gateway sidecar. The SHA-256 binds the metadata to the exact binary shipped
// alongside the desktop executable.
type SidecarMetadata struct {
	SchemaVersion int       `json:"schemaVersion"`
	Product       string    `json:"product"`
	Artifact      string    `json:"artifact"`
	TargetTriple  string    `json:"targetTriple"`
	BinaryVersion string    `json:"binaryVersion"`
	APIVersion    string    `json:"apiVersion"`
	APIMajor      string    `json:"apiMajor"`
	SHA256        string    `json:"sha256"`
	Ownership     Ownership `json:"ownership"`
}

// Generate hashes binaryPath, validates the release contract, and atomically
// writes metadataPath. The output is deterministic for identical inputs.
func Generate(binaryPath, metadataPath, targetTriple, binaryVersion, apiVersion string, ownership Ownership) (SidecarMetadata, error) {
	digest, err := SHA256File(binaryPath)
	if err != nil {
		return SidecarMetadata{}, fmt.Errorf("hash sidecar binary: %w", err)
	}
	metadata := SidecarMetadata{
		SchemaVersion: CurrentSchemaVersion,
		Product:       ProductName,
		Artifact:      filepath.Base(binaryPath),
		TargetTriple:  strings.TrimSpace(targetTriple),
		BinaryVersion: strings.TrimSpace(binaryVersion),
		APIVersion:    strings.TrimSpace(apiVersion),
		APIMajor:      control.APIMajor(apiVersion),
		SHA256:        digest,
		Ownership:     ownership,
	}
	if err := metadata.Validate(); err != nil {
		return SidecarMetadata{}, err
	}
	if err := Write(metadataPath, metadata); err != nil {
		return SidecarMetadata{}, err
	}
	return metadata, nil
}

// SHA256File returns the lowercase hexadecimal SHA-256 digest of a file.
func SHA256File(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// Validate checks schema, identity, API compatibility, ownership, and digest
// shape without reading the binary itself.
func (m SidecarMetadata) Validate() error {
	if m.SchemaVersion > CurrentSchemaVersion {
		return fmt.Errorf("%w: got %d, current %d", ErrUnsupportedSchema, m.SchemaVersion, CurrentSchemaVersion)
	}
	if m.SchemaVersion != CurrentSchemaVersion {
		return fmt.Errorf("%w: got %d, current %d", ErrInvalidMetadata, m.SchemaVersion, CurrentSchemaVersion)
	}
	if strings.TrimSpace(m.Product) != ProductName || !safeToken(m.Artifact) || !safeToken(m.TargetTriple) || !safeToken(m.BinaryVersion) {
		return fmt.Errorf("%w: invalid product, artifact, target, or binary version", ErrInvalidMetadata)
	}
	if strings.TrimSpace(m.APIVersion) == "" || !safeToken(m.APIVersion) || m.APIMajor == "" || !safeToken(m.APIMajor) || control.APIMajor(m.APIVersion) != m.APIMajor {
		return fmt.Errorf("%w: API version and major do not agree", ErrInvalidMetadata)
	}
	if m.Ownership != OwnershipDesktopManaged && m.Ownership != OwnershipExternal {
		return fmt.Errorf("%w: unknown ownership %q", ErrInvalidMetadata, m.Ownership)
	}
	digest, err := hex.DecodeString(m.SHA256)
	if err != nil || len(digest) != sha256.Size || strings.ToLower(m.SHA256) != m.SHA256 {
		return fmt.Errorf("%w: sha256 must be a lowercase 64-character digest", ErrInvalidMetadata)
	}
	return nil
}

// VerifyFile checks that path is exactly the binary represented by metadata.
func (m SidecarMetadata) VerifyFile(path string) error {
	if err := m.Validate(); err != nil {
		return err
	}
	digest, err := SHA256File(path)
	if err != nil {
		return fmt.Errorf("hash sidecar binary: %w", err)
	}
	if digest != m.SHA256 {
		return fmt.Errorf("%w: want %s, got %s", ErrChecksumMismatch, m.SHA256, digest)
	}
	return nil
}

// Read decodes and validates one metadata document. Unknown fields and trailing
// JSON values are rejected so schema drift cannot be silently ignored.
func Read(path string) (SidecarMetadata, error) {
	file, err := os.Open(path)
	if err != nil {
		return SidecarMetadata{}, err
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	var metadata SidecarMetadata
	if err := decoder.Decode(&metadata); err != nil {
		return SidecarMetadata{}, fmt.Errorf("%w: %v", ErrInvalidMetadata, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return SidecarMetadata{}, fmt.Errorf("%w: metadata must contain one JSON value", ErrInvalidMetadata)
		}
		return SidecarMetadata{}, fmt.Errorf("%w: %v", ErrInvalidMetadata, err)
	}
	if err := metadata.Validate(); err != nil {
		return SidecarMetadata{}, err
	}
	return metadata, nil
}

// Write validates and atomically writes one deterministic metadata document.
func Write(path string, metadata SidecarMetadata) error {
	if err := metadata.Validate(); err != nil {
		return err
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return fmt.Errorf("%w: metadata path is empty", ErrInvalidMetadata)
	}
	return writeAtomicJSON(path, metadata, ".fmr-sidecar-*.tmp", 0o644)
}

func writeAtomicJSON(path string, value any, pattern string, mode os.FileMode) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return errors.New("JSON output path is empty")
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal JSON: %w", err)
	}
	data = append(data, '\n')
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create JSON directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return fmt.Errorf("create JSON temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("protect JSON temp file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write JSON temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync JSON temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close JSON temp file: %w", err)
	}
	if err := replaceFile(tmpName, path); err != nil {
		return fmt.Errorf("replace JSON: %w", err)
	}
	return os.Chmod(path, mode)
}

func replaceFile(tempPath, targetPath string) error {
	if err := os.Rename(tempPath, targetPath); err == nil {
		return nil
	}
	backupPath := targetPath + ".bak"
	_ = os.Remove(backupPath)
	if err := os.Rename(targetPath, backupPath); err != nil {
		return err
	}
	if err := os.Rename(tempPath, targetPath); err != nil {
		_ = os.Rename(backupPath, targetPath)
		return err
	}
	_ = os.Remove(backupPath)
	return nil
}

func safeToken(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && !strings.ContainsAny(value, "\r\n/\\")
}
