package packaging

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

const (
	CurrentManifestSchemaVersion = 1
	WindowsPlatform              = "windows"
	DefaultDesktopExecutable     = "Free-Model-Router-desktop.exe"
	DefaultGatewaySidecar        = "Free-Model-Router.exe"
	DefaultControlCLI            = "fmr.exe"
)

var (
	ErrInvalidManifest           = errors.New("invalid Windows package manifest")
	ErrUnsupportedManifestSchema = errors.New("unsupported Windows package manifest schema")
)

// ToolRequirements distinguishes build-time tools from end-user runtime
// requirements. Node, npm, Rust, and Go are all build-time only.
type ToolRequirements struct {
	Go   bool `json:"go"`
	Node bool `json:"node"`
	NPM  bool `json:"npm"`
	Rust bool `json:"rust"`
}

// WindowsInstallContract records the delivery behavior a future installer
// must preserve. The current repository produces a portable ZIP contract until
// a Tauri installer toolchain is available.
type WindowsInstallContract struct {
	Format    string `json:"format"`
	Upgrade   string `json:"upgrade"`
	Uninstall string `json:"uninstall"`
}

// WindowsManifest is the release contract consumed by a desktop shell and by
// packaging automation. It embeds the sidecar metadata so one manifest is
// sufficient to verify target, API compatibility, ownership, and hash.
type WindowsManifest struct {
	SchemaVersion       int                    `json:"schemaVersion"`
	Product             string                 `json:"product"`
	Platform            string                 `json:"platform"`
	TargetTriple        string                 `json:"targetTriple"`
	DesktopExecutable   string                 `json:"desktopExecutable"`
	DesktopIncluded     bool                   `json:"desktopIncluded"`
	GatewaySidecar      string                 `json:"gatewaySidecar"`
	ControlCLI          string                 `json:"controlCli"`
	SidecarMetadata     string                 `json:"sidecarMetadata"`
	Sidecar             SidecarMetadata        `json:"sidecar"`
	RuntimeRequirements ToolRequirements       `json:"runtimeRequirements"`
	BuildRequirements   ToolRequirements       `json:"buildRequirements"`
	Installer           WindowsInstallContract `json:"installer"`
}

// NewWindowsManifest builds the stable manifest around one verified sidecar.
func NewWindowsManifest(metadata SidecarMetadata, desktopExecutable string) WindowsManifest {
	return NewWindowsManifestWithDesktop(metadata, desktopExecutable, false)
}

// NewWindowsManifestWithDesktop builds a manifest and records whether the
// desktop executable is present in the package directory.
func NewWindowsManifestWithDesktop(metadata SidecarMetadata, desktopExecutable string, included bool) WindowsManifest {
	if strings.TrimSpace(desktopExecutable) == "" {
		desktopExecutable = DefaultDesktopExecutable
	}
	return WindowsManifest{
		SchemaVersion:       CurrentManifestSchemaVersion,
		Product:             ProductName,
		Platform:            WindowsPlatform,
		TargetTriple:        metadata.TargetTriple,
		DesktopExecutable:   strings.TrimSpace(desktopExecutable),
		DesktopIncluded:     included,
		GatewaySidecar:      metadata.Artifact,
		ControlCLI:          DefaultControlCLI,
		SidecarMetadata:     metadata.Artifact + ".metadata.json",
		Sidecar:             metadata,
		RuntimeRequirements: ToolRequirements{},
		BuildRequirements:   ToolRequirements{Go: true, Node: true, NPM: true, Rust: true},
		Installer: WindowsInstallContract{
			Format:    "zip",
			Upgrade:   "replace-versioned-install-and-preserve-user-data",
			Uninstall: "remove-installed-binaries-and-preserve-user-data",
		},
	}
}

// Validate checks the package identity and ensures the embedded sidecar is
// exactly the artifact described by the manifest.
func (m WindowsManifest) Validate() error {
	if m.SchemaVersion > CurrentManifestSchemaVersion {
		return fmt.Errorf("%w: got %d, current %d", ErrUnsupportedManifestSchema, m.SchemaVersion, CurrentManifestSchemaVersion)
	}
	if m.SchemaVersion != CurrentManifestSchemaVersion {
		return fmt.Errorf("%w: got %d, current %d", ErrInvalidManifest, m.SchemaVersion, CurrentManifestSchemaVersion)
	}
	if m.Product != ProductName || m.Platform != WindowsPlatform || !safeWindowsExecutable(m.DesktopExecutable) || !safeWindowsExecutable(m.GatewaySidecar) || !safeWindowsExecutable(m.ControlCLI) || !safeJSONFile(m.SidecarMetadata) || m.TargetTriple == "" {
		return fmt.Errorf("%w: invalid product, platform, or artifact names", ErrInvalidManifest)
	}
	if err := m.Sidecar.Validate(); err != nil {
		return fmt.Errorf("%w: sidecar: %v", ErrInvalidManifest, err)
	}
	if m.TargetTriple != m.Sidecar.TargetTriple || m.GatewaySidecar != m.Sidecar.Artifact || m.SidecarMetadata != m.Sidecar.Artifact+".metadata.json" {
		return fmt.Errorf("%w: manifest and sidecar identity disagree", ErrInvalidManifest)
	}
	if m.RuntimeRequirements != (ToolRequirements{}) {
		return fmt.Errorf("%w: package runtime must not require build tools", ErrInvalidManifest)
	}
	wantBuild := ToolRequirements{Go: true, Node: true, NPM: true, Rust: true}
	if m.BuildRequirements != wantBuild {
		return fmt.Errorf("%w: build requirements are incomplete", ErrInvalidManifest)
	}
	wantInstaller := WindowsInstallContract{
		Format:    "zip",
		Upgrade:   "replace-versioned-install-and-preserve-user-data",
		Uninstall: "remove-installed-binaries-and-preserve-user-data",
	}
	if m.Installer != wantInstaller {
		return fmt.Errorf("%w: installer contract is invalid", ErrInvalidManifest)
	}
	return nil
}

// WriteWindowsManifest atomically writes one deterministic manifest.
func WriteWindowsManifest(path string, manifest WindowsManifest) error {
	if err := manifest.Validate(); err != nil {
		return err
	}
	return writeAtomicJSON(path, manifest, ".fmr-manifest-*.tmp", 0o644)
}

// ReadWindowsManifest reads and validates one manifest document.
func ReadWindowsManifest(path string) (WindowsManifest, error) {
	file, err := os.Open(path)
	if err != nil {
		return WindowsManifest{}, err
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	var manifest WindowsManifest
	if err := decoder.Decode(&manifest); err != nil {
		return WindowsManifest{}, fmt.Errorf("%w: %v", ErrInvalidManifest, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return WindowsManifest{}, fmt.Errorf("%w: manifest must contain one JSON value", ErrInvalidManifest)
		}
		return WindowsManifest{}, fmt.Errorf("%w: %v", ErrInvalidManifest, err)
	}
	if err := manifest.Validate(); err != nil {
		return WindowsManifest{}, err
	}
	return manifest, nil
}

func safeFileName(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && value != "." && value != ".." && !strings.ContainsAny(value, "\r\n/\\")
}

func safeWindowsExecutable(value string) bool {
	return safeFileName(value) && strings.HasSuffix(strings.ToLower(strings.TrimSpace(value)), ".exe")
}

func safeJSONFile(value string) bool {
	return safeFileName(value) && strings.HasSuffix(strings.ToLower(strings.TrimSpace(value)), ".json")
}
