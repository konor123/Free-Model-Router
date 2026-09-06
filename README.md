# Free-Model-Router

Free Model Router (FMR): local AI gateway (Go) + Tauri desktop UI that discovers free / free-tier
LLM providers and routes requests by capability, performance, TTFT, and health.

Plan of record: [PLAN_V7.md](PLAN_V7.md).

## Status

Phase 0–6.5 runtime integration complete.
Phase 7 ranked, failure-aware fallback implemented.
Phase 8 streaming commit guard validated.
Phase 9 benchmark matching, confidence-aware scoring, and runtime ranking integrated.

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

## Failover limits

The gateway bounds fallback per request. Defaults are four attempts and a
30-second failover window. Override them with:

```
FMR_MAX_ATTEMPTS=4
FMR_FAILOVER_BUDGET_MS=30000
```
