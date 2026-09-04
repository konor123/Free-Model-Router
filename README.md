# Free-Model-Router

Auto Free Models (AFM): local AI gateway (Go) + Tauri desktop UI that discovers free / free-tier
LLM providers and routes requests by capability, performance, TTFT, and health.

Plan of record: [PLAN_V7.md](PLAN_V7.md).

## Status

Phase 0 — Repository Bootstrap (in progress).

## Layout

```
cmd/Free-Model-Router gateway binary entrypoint
internal/app           wiring
internal/model         core types (identity, failure)   [Phase 1+]
internal/provider      provider interfaces
internal/providers     concrete provider implementations [Phase 2+]
internal/router        eligibility + fallback           [Phase 4+]
internal/gateway       OpenAI-compatible HTTP surface   [Phase 2+]
internal/config        configuration + persistence      [Phase 0+]
```

Dependency rule (see PLAN_V7 §4):

```
router → provider interfaces
provider implementations → provider interfaces
router ✕ provider implementations
```

## Build / gate

```
go test ./...
go vet ./...
go build ./cmd/Free-Model-Router
```
