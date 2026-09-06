# Handoff log: Phase 18 Windows packaging contract

- Date: 2026-09-06
- Scope: PLAN_V7.1 Phase 18
- Base: `21af964` (Phase 16-17 model manager and usage window)
- Status: implementation complete, review findings resolved, focused and representative validation passed; native desktop/clean-VM gates remain environment-blocked

## Delivered

- Added deterministic sidecar metadata with schema version, product/artifact identity,
  target triple, binary version, API version and major, SHA-256, and ownership mode.
- Added strict metadata validation, tamper detection, unknown-field rejection, and
  atomic JSON writes with the Windows replacement fallback.
- Added a Windows package manifest with an embedded sidecar identity, optional desktop
  executable declaration, runtime/build requirements, and upgrade/uninstall policy.
- Added a checked-in JSON schema for the package manifest.
- Added `cmd/fmr-package` as a build-time generator and validator. End-user packages
  do not require Go, Node, npm, or Rust.
- Added `scripts/package-windows.ps1` for reproducible Windows gateway/CLI builds,
  metadata generation, manifest generation, schema inclusion, and versioned ZIP output.
  The script validates the version as a safe filename component and rejects traversal.
- Added CI contract coverage and a Windows release workflow with a bounded 30-minute
  job. The release workflow passes the ref name through an environment variable rather
  than interpolating it into a PowerShell command.
- Updated README and packaging documentation to distinguish the portable sidecar
  contract from the not-yet-built native Tauri shell.

## TDD and review evidence

- Metadata tests were written first and failed before the implementation existed.
- Manifest tests were written first and failed before the implementation existed.
- Packaging CLI tests were written first and failed before the CLI implementation existed.
- A manifest extension test caught an implementation/schema mismatch and was fixed.
- Focused validation passed:
  - `go test -count=1 ./internal/packaging/... ./cmd/fmr-package/...`
  - `go vet ./internal/packaging/... ./cmd/fmr-package/...`
  - `git diff --check`
- Representative package validation passed on this Windows host:
  - `scripts/package-windows.ps1 -Version v0.0.0-test`
  - `go run ./cmd/fmr-package -validate` against the generated gateway,
    metadata, and manifest
  - generated ZIP contained the versioned Windows package contract
  - unsafe version `..\\unsafe` was rejected by parameter validation
  - manifest schema parsed successfully with PowerShell JSON parsing
- Corrected cross-build validation passed using explicit PowerShell `GOOS=windows`
  and `GOARCH=amd64` assignment for the gateway, control CLI, and packaging CLI.
- Whole-repository validation passed after the Phase 18 changes:
  - `go test -count=1 ./...`
  - `go vet ./...`
  - `go build ./...`
  - `go build ./cmd/fmr`
  - `go build ./cmd/fmr-package`
- The first cross-build command used invalid `cmd.exe` environment syntax and reported
  `unsupported GOOS/GOARCH pair windows /amd64`; this was a command invocation error,
  not a source failure. The corrected cross-build passed.
- OpenCodeReview initially found one low-severity reliability issue, missing
  `timeout-minutes`, which was fixed. A second review found a high-severity workflow
  interpolation issue, which was fixed by using `REF_NAME` through `env:` and by
  validating the version parameter. The final provider-backed review returned HTTP
  404 for every selected item, so it could not produce a final model review. Its
  scope and rules were collected with `ocr delegate preview` and `ocr delegate rule`;
  local review plus the earlier successful review and tests remain the available
  independent evidence.

## Explicit acceptance boundary

- The portable Go sidecar package, metadata integrity, package manifest, build-time CLI,
  and CI/release contract are implemented and validated.
- Native Tauri tray behavior, native single-instance integration, Windows installer
  signing, and clean Windows VM install/upgrade/uninstall tests were not executable in
  this environment because Rust/Cargo/Tauri, installer/signing tools, and a clean VM
  are unavailable. The repository documentation states this limitation rather than
  claiming those gates passed.

## Repository hygiene

- `.codegraph/` remains pre-existing and untracked; it is not part of this milestone.
- No credentials or secret values were written to this log.

## Next

- Commit and push the Phase 18 milestone.
- Run final whole-repository validation from the committed state and record the final
  acceptance boundary in a per-turn validation log.
