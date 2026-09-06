// Command fmr-package generates and verifies release metadata for the Windows
// gateway sidecar. It is a build-time helper, not an end-user dependency.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/konor123/Free-Model-Router/internal/control"
	"github.com/konor123/Free-Model-Router/internal/packaging"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "fmr-package: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	if stderr == nil {
		stderr = io.Discard
	}
	if stdout == nil {
		stdout = io.Discard
	}
	flags := flag.NewFlagSet("fmr-package", flag.ContinueOnError)
	flags.SetOutput(stderr)
	binaryPath := flags.String("binary", "", "path to the gateway sidecar binary")
	metadataPath := flags.String("metadata", "", "path to sidecar metadata JSON")
	manifestPath := flags.String("manifest", "", "optional path to Windows package manifest JSON")
	desktopExecutable := flags.String("desktop-executable", packaging.DefaultDesktopExecutable, "desktop executable filename recorded in the manifest")
	desktopIncluded := flags.Bool("desktop-included", false, "record that the desktop executable is included in the package")
	targetTriple := flags.String("target", "x86_64-pc-windows-msvc", "release target triple")
	binaryVersion := flags.String("version", "dev", "sidecar binary version")
	apiVersion := flags.String("api-version", control.APIVersion, "control API version")
	ownership := flags.String("ownership", string(packaging.OwnershipDesktopManaged), "sidecar ownership mode")
	validate := flags.Bool("validate", false, "validate existing metadata and manifest instead of generating")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}
	if *metadataPath == "" && *binaryPath != "" {
		*metadataPath = *binaryPath + ".metadata.json"
	}
	if *metadataPath == "" {
		return errors.New("-metadata is required")
	}

	var metadata packaging.SidecarMetadata
	var manifest *packaging.WindowsManifest
	if *validate {
		var err error
		metadata, err = packaging.Read(*metadataPath)
		if err != nil {
			return err
		}
		if *binaryPath != "" {
			if err := metadata.VerifyFile(*binaryPath); err != nil {
				return err
			}
		}
		if *manifestPath != "" {
			loaded, err := packaging.ReadWindowsManifest(*manifestPath)
			if err != nil {
				return err
			}
			manifest = &loaded
		}
	} else {
		if *binaryPath == "" {
			return errors.New("-binary is required when generating metadata")
		}
		var err error
		metadata, err = packaging.Generate(*binaryPath, *metadataPath, *targetTriple, *binaryVersion, *apiVersion, packaging.Ownership(*ownership))
		if err != nil {
			return err
		}
		if *manifestPath != "" {
			generated := packaging.NewWindowsManifestWithDesktop(metadata, *desktopExecutable, *desktopIncluded)
			if err := packaging.WriteWindowsManifest(*manifestPath, generated); err != nil {
				return err
			}
			manifest = &generated
		}
	}

	result := struct {
		Metadata packaging.SidecarMetadata  `json:"metadata"`
		Manifest *packaging.WindowsManifest `json:"manifest,omitempty"`
	}{Metadata: metadata, Manifest: manifest}
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(result)
}
