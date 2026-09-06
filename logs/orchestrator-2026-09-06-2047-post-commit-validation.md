# PLAN_V7.1 Post-Commit Public Acceptance Revalidation

- Date: 2026-09-06 UTC
- Scope: re-run the whole-result public-interface and integration validation after commit `320e1c5`.
- Starting HEAD: `320e1c5de982ef34efdc73f98956f82f2b7d2d89`.
- This turn changed no implementation code. `.codegraph/` remains pre-existing and untracked.

## Observed acceptance results

### Go application and public boundaries

- `go test -v -count=1 ./internal/app/... ./internal/control/... ./internal/gateway/... ./internal/security/...` **PASS**.
- The actual app integration path **PASS**:
  - `TestRunWithProviderWiresRealHTTPAppPath`
  - `TestRunWithProviderExposesAuthenticatedControlBoundary`
  - `TestRunWithProviderDoesNotLogAuthenticationTokens`
  - `TestRunWithProviderRequiresInferenceTokenForWildcardBind`
- Gateway fallback, streaming commit, TTFT/probe, score-based selection, and usage integration tests all **PASS** in the same run. The observed named tests included `TestStreamingFailureBeforeSemanticDoesNotCommit`, `TestStreamingFailureAfterSemanticCommitsAndRecordsMetrics`, `TestProbeCycleUsesLivePoolAndHTTPProvider`, `TestBenchmarkScoreChangesRuntimeCandidateSelection`, `TestUsageRecordsOneRequestAcrossFallbackAttempts`, and `TestUsageRecordsCommittedStreamingFailureAsPartial`.
- Control API tests **PASS** for bearer auth/status, available feature advertisement, loopback and JSON-body ID rules, filtered logs, optimistic pool mutation, typed client, live events, and model-manager filters.
- Security tests **PASS** for loopback management binding and token-required wildcard inference.

### Persistence, usage, routing, and providers

- `go test -v -count=1 ./internal/config/... ./internal/usage/... ./internal/model/... ./internal/catalog/... ./internal/router/... ./internal/matcher/... ./internal/scoring/... ./internal/providers/opencode/... ./internal/providers/nvidia/... ./internal/providers/gemini/... ./internal/providers/xai/...` **PASS**.
- Config observed **PASS** for atomic round trip, schema migration, corrupt recovery, secret priority/redaction, protected fallback, application paths, future schema rejection, concurrency, and public contracts.
- Usage observed **PASS** for request/attempt separation, redacted events, age/size retention, persistence/reopen, filtering, broadcaster lifecycle, and no duplicate fallback accounting.
- Router/catalog/model observed **PASS** for capability exclusion, fallback requirements, access policy, deterministic identity, catalog tombstones/restoration, automatic/manual pool behavior, and revision conflicts.
- OpenCode, NVIDIA, Gemini, and xAI mock suites observed **PASS** for catalog, malformed/server errors, normal and streaming completion, rate limits/auth failures, cancellation, unsupported parameters, route/access identity, API-key source, and SSE tool/reasoning preservation.

### CLI

- `go run ./cmd/fmr --help` **PASS**.
- The observed public help listed all planned commands: `start`, `stop`, `status`, `models`, `providers`, `model-pool`, `config`, `logs`, and `doctor`, plus `-addr`, `-token`, `-config`, and `-json` flags.

### Desktop and frontend

- `npm test --prefix desktop` **PASS**. The named test `model search keeps its input element while filtering` passed.
- `node --check desktop/frontend/app.js` **PASS**.
- Rust checks rerun with the installed explicit tool path (`C:\Users\USER\.cargo\bin`): `cargo fmt --manifest-path desktop\\src-tauri\\Cargo.toml -- --check`, `cargo test --manifest-path desktop\\src-tauri\\Cargo.toml`, and `cargo clippy --manifest-path desktop\\src-tauri\\Cargo.toml --all-targets -- -D warnings` all **PASS**.
- Native Rust test result: 7 passed, 0 failed, including compatible API-major attach, loopback control address, external ownership preservation, safe sidecar name, authentication failure no duplicate gateway, and atomic autostart command.
- Desktop lifecycle Go tests **PASS** for second acquire/release, stale recovery, live lease protection, external attach/mismatch, managed-only stop, tray/provider snapshot, and autostart.

### Packaging

- A first post-commit packaging invocation used `-SkipBuild` with a fresh empty output directory and correctly failed with `gateway sidecar not found`. This was an invocation error, not treated as a package pass.
- The corrected command ran `scripts\\package-windows.ps1` without `-SkipBuild`, built the Go sidecar, included the previously built native desktop executable, and returned `PACKAGE_OK`.
- `go run ./cmd/fmr-package -validate` against the generated sidecar metadata and manifest **PASS**.
- Archive extraction checked both `Free-Model-Router.exe` and `free-model-router-desktop.exe`; result **PASS** with `POSTCOMMIT_PACKAGE_OK`.
- Observed manifest fields include target `x86_64-pc-windows-msvc`, API major `1`, SHA-256 sidecar metadata, `desktopIncluded=true`, and runtime requirements `go=false`, `node=false`, `npm=false`, `rust=false`.
- The prior native Tauri release build also produced the Windows EXE, NSIS installer, and MSI bundle successfully from the unchanged implementation tree.

### Whole repository

- `go test -count=1 ./... && go vet ./... && go build ./cmd/Free-Model-Router ./cmd/fmr ./cmd/fmr-package` **PASS**.
- The full uncached test run passed all Go packages, including application, control, desktop, gateway, providers, routing, security, usage, and packaging.

## Corrected command failures

1. The chained frontend/Rust command returned exit 1 even though the Node test output was green. The failure was caused by `cargo` not being on the executor PATH and had no source diagnostic. Running the Node test separately and rerunning Rust with `C:\Users\USER\.cargo\bin` in PATH produced **PASS** for all frontend and Rust checks.
2. Portable packaging initially used `-SkipBuild` without placing the Go sidecar in the output directory. The script therefore reported the expected missing-sidecar error. Rerunning without `-SkipBuild` built the sidecar and the subsequent metadata validation and archive extraction **PASS**.
3. `set CGO_ENABLED=1 && go test -race ./...` was retried. `go env` confirmed `CGO_ENABLED=1` and `CC=gcc`, but the host has neither `gcc` nor `cl` (`GCC_NOT_FOUND`, `CL_NOT_FOUND`); the race build failed with `cgo: C compiler "gcc" not found`. Go race remains host-toolchain blocked and is not claimed as passed.

## Acceptance limits

These local checks exercise real project interfaces and integration boundaries, but they do not replace external Phase 18 acceptance gates. Clean Windows VM installation, first run on a clean profile, reboot-persistent autostart, upgrade/uninstall preservation, signing/update verification, and true multi-process desktop/tray races remain unverified because the required VM, signing credentials, and OS session are unavailable.

## Feedback-loop conclusion

The post-commit rerun confirms the implementation is better than an inspection-only result: public application/control HTTP paths, CLI help, persistence/usage contracts, desktop lifecycle, frontend behavior, native Rust tests, package generation, metadata verification, archive extraction, and full Go regression all produced observed passing results. The two command failures were corrected and recorded. The only remaining local test limitation is the absent C compiler for Go race tests, and the external VM/signing gates remain explicitly blocked rather than overstated.
