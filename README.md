# Free-Model-Router

Free Model Router (FMR): local AI gateway (Go) + Tauri desktop UI that discovers free / free-tier
LLM providers and routes requests by capability, performance, TTFT, and health.

Plan of record: [PLAN_V7.1.md](PLAN_V7.1.md).

## Status

Phase 0–6.5 runtime integration complete.
Phase 7 ranked, failure-aware fallback implemented.
Phase 8 streaming commit guard validated.
Phase 9 benchmark matching, confidence-aware scoring, and runtime ranking integrated.
Phase 10 additional NVIDIA, Gemini, and xAI providers implemented.
Phase 11 persistence, schema migration, OS state directories, and secret storage implemented.
Phase 12 request-level usage logging with per-attempt fallback history, token metadata,
and bounded durable retention implemented.

## Layout

```
cmd/Free-Model-Router gateway binary entrypoint
internal/app           wiring
internal/model         core types (identity, failure)   [Phase 1+]
internal/provider      provider interfaces
internal/providers     concrete provider implementations [Phase 2+]
internal/router        eligibility + fallback           [Phase 4+]
internal/gateway       OpenAI-compatible HTTP surface   [Phase 2+]
internal/config        configuration + persistence + secrets [Phase 0+]
internal/usage         redacted request/attempt usage log [Phase 12+]
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

## Persistence and secrets

Configuration is stored atomically as a versioned `PersistedConfig` below the
OS user configuration directory. Cache state uses the corresponding OS user
cache directory. Secret lookup is fail-closed and ordered as follows:

1. matching environment variable, such as `GEMINI_API_KEY`
2. the native OS credential store
3. a separate user-only `secrets.json` fallback file

Secret values are never included in configuration or usage logs.

Usage logs contain one record per inference request and nested records for each
fallback attempt. They retain route metadata, failure classification, TTFT,
latency, commit state, HTTP status, and provider-reported token counts, but never
retain prompts, responses, API keys, or Authorization headers. Durable logs are
stored as protected JSONL below the user configuration directory and are bounded
to 20 MB and 30 days.
