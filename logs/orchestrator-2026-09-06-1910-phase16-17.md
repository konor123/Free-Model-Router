# Handoff log: Phase 16-17 model manager and usage window

- Date: 2026-09-06
- Scope: PLAN_V7.1 Phases 16-17
- Base: `a9da4fd` (Phase 15 process-stop safety)
- Status: implementation complete, review passed, focused and whole-repository validation passed, ready for commit/push

## Delivered

- Added model-manager filtering for search, provider, access, status, and capability.
- Added deterministic model sorting and safe route-detail views.
- Exposed TTFT, latency score, benchmark performance, confidence, and routing score metadata.
- Added typed control-client reads and optimistic pool/pin/auto mutation methods.
- Added a bounded redacted usage-event broadcaster with request-start, attempt-start,
  and completion event types.
- Wired request and attempt lifecycle events from the gateway to the application usage store.
- Added authenticated localhost SSE at `GET /_fmr/logs/events`.
- Kept prompts, responses, authorization headers, API keys, and other secrets out of
  usage events and SSE payloads.
- Added tests for model filters, typed client contracts, broadcaster delivery,
  completion events, end-to-end request lifecycle events, SSE cancellation,
  filtering, and redaction.

## Review and test evidence

- TDD red tests failed before adding the broadcaster, SSE, filter, and typed-client implementations.
- Final review found and fixed a handshake inconsistency where `usage-events`
  could be advertised without an event source; a regression test now covers both
  available and unavailable event streams.
- Focused tests passed:
  - `go test -count=1 ./internal/control/... ./internal/usage/... ./internal/gateway/... ./internal/app/...`
- Focused static analysis passed:
  - `go vet ./internal/control/... ./internal/usage/... ./internal/gateway/... ./internal/app/...`
- Whole-repository validation passed:
  - `go test -count=1 ./...`
  - `go vet ./...`
  - `go build ./... && go build ./cmd/fmr`
- Independent OpenCodeReview completed with zero findings for the groups that
  returned results. The local-gateway provider rate-limited 7 of 10 selected
  groups with HTTP 429, so that review is advisory and partial rather than a
  substitute for the local review and validation above.
- The public contract remains dependency-free and is consumable by a future desktop shell.

## Repository hygiene

- `.codegraph/` remains pre-existing and untracked.
- No credentials or secret values were written to this log.

## Next

- Commit and push the Phase 16-17 milestone.
- Implement Phase 18 Windows sidecar metadata and packaging checks.
