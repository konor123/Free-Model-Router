# Handoff: PLAN_V7.1 race and final acceptance validation

- Timestamp: 2026-09-06 20:55 UTC
- Repository: `G:\내 드라이브\Projects\Free-Model-Router`
- Starting pushed commit: `61de23b`
- Code-fix commit: `4b376a5` (`test(phase7): close fallback and SSE race coverage`), pushed to `origin/main`
- Scope safety: the pre-existing untracked `.codegraph/` directory was not staged, changed, or removed.

## Feedback-loop findings and corrections

The whole-result acceptance loop found two actionable gaps:

1. `go test -race` exposed a real data race in `internal/control/events_test.go`: the test read `httptest.ResponseRecorder.Body` concurrently with the SSE handler writing it. The test now uses a real `httptest.NewServer`, an HTTP client, a streaming scanner, explicit event/header/body/handler completion channels, and cancellation before inspecting the collected body.
2. The gateway fallback matrix lacked a dedicated public-behavior test proving that a provider-scoped upstream 5xx advances from one `ProviderModel` to the next. `TestFallbackServerErrorAdvancesToNextProviderModel` now asserts the 200 response, selected model, and exact attempt order.

Focused validation passed for both changes. The focused race run for the live SSE path and gateway/control boundary also passed.

## Whole-result validation

The corrected result was exercised through public and integration boundaries, not only inspection:

| Acceptance area | Concrete check | Result |
|---|---|---|
| Real application HTTP wiring | `TestRunWithProviderWiresRealHTTPAppPath` | PASS |
| Control authentication and security | `TestRunWithProviderExposesAuthenticatedControlBoundary`, `TestRunWithProviderDoesNotLogAuthenticationTokens`, `TestRunWithProviderRequiresInferenceTokenForWildcardBind` | PASS |
| Live usage SSE boundary | `TestControlLogsEventsStreamsSafeLiveUsage` through `httptest.NewServer` and an HTTP client | PASS, including `-race` |
| Fallback behavior | `TestFallbackServerErrorAdvancesToNextProviderModel` plus existing route, credential, protocol, streaming, cancellation, budget, and max-attempt tests | PASS |
| Persistence and secrets | defaults, atomic round trip, schema migration, corrupt recovery, secret redaction, OS/environment/file precedence tests | PASS |
| Model/catalog/routing behavior | verbose matcher, capability, catalog, scoring, router, provider-route, and identity tests | PASS |
| Desktop lifecycle and public contracts | Rust unit tests for ownership, stale/live locks, attach compatibility, auth failure, loopback control, safe sidecar naming, and atomic autostart | PASS |
| Frontend behavior | `npm test` / `node --test frontend/app.test.js` | PASS |
| CLI public surface | `go run ./cmd/fmr --help` listed `start`, `stop`, `status`, `models`, `providers`, `model-pool`, `config`, `logs`, `doctor` and the documented flags | PASS |
| Native Windows artifact path | Tauri release executable plus NSIS/MSI bundles, metadata/hash validation, and extraction check | PASS |
| Portable Windows package path | package build without `-SkipBuild`, `cmd/fmr-package -validate`, archive extraction of desktop executable and Go sidecar | PASS |

Final Go validation after the fixes:

```text
go test -race -count=1 ./...                                  PASS
go test -count=1 ./...                                       PASS
go vet ./...                                                  PASS
go build ./cmd/Free-Model-Router ./cmd/fmr ./cmd/fmr-package PASS
```

The race suite used the available WinLibs GCC toolchain at `C:\Users\USER\AppData\Local\Programs\WinLibs\16.2-ucrt-r1\mingw64\bin` with `CGO_ENABLED=1` and `CC=gcc`. The prior default-host failure was an unavailable cgo compiler, not a product failure. The uncached full race suite passed after the test synchronization fix.

## Review status

- Manual review re-read both modified tests in context.
- `git diff --check` passed.
- OpenCodeReview was invoked for the current workspace, but its configured selection excluded the changed test files and returned `skipped: no items were selected`; therefore no current OCR finding is claimed for those files. Earlier selected-scope reviews reported zero findings. The corrected tests were instead validated with real HTTP behavior, full regression, full race detection, and manual diff review.
- The requirement-to-check matrix for PLAN_V7.1 remains in `logs/orchestrator-2026-09-06-2043-plan-acceptance-matrix.md`.

## Explicit external gates not claimed as passed

The repository-side implementation and available automated acceptance paths are complete, but these require an external clean Windows environment or credentials and remain unverified:

- clean-VM installation, first-run profile creation, reboot-persistent autostart, upgrade/uninstall preservation, and signed update verification;
- manual GUI/tray behavior across a real desktop session, including OS-level single-instance/tray observation;
- production code-signing and update-feed verification;
- a separate process-level curl smoke test against a newly launched binary. The available app integration test does exercise the real TCP HTTP application path with a local provider.

The repeated request to remove a jcode OpenAI-compatible login was not applied to this repository because no jcode login/OAuth implementation exists here; the existing OpenAI-compatible/provider protocol adapters are separate functionality and were not removed.

## Handoff state

The code-fix commit `4b376a5` and this handoff log are pushed to `origin/main`. Final verification confirmed `HEAD` equals `origin/main`, `git diff --check` passed, and the only remaining workspace item is the pre-existing untracked `.codegraph/` directory.
