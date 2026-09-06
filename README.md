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
Phase 13 localhost control API and `fmr` CLI implemented.
Phase 14 loopback management and explicit LAN inference authentication implemented.
Phase 15 desktop lifecycle core implemented, including single-instance leases,
compatible attach/start ownership, provider menu state, and file-based autostart.
Phase 16 model-manager filtering, route performance metadata, routing scores, and
typed control-client mutations implemented.
Phase 17 live redacted usage events, SSE delivery, and usage-window filters implemented.

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
internal/control       authenticated desktop/CLI control API [Phase 13+]
internal/security      bind and bearer-token policy [Phase 14+]
internal/desktop       tray-shell lifecycle contracts [Phase 15+]
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

## Control API and CLI

Phase 13 adds a separate, authenticated localhost control plane. Configured
profiles listen on `127.0.0.1:8788` by default and expose:

```text
GET   /_fmr/status
GET   /_fmr/providers
GET   /_fmr/models
GET   /_fmr/model-pool
PUT   /_fmr/model-pool
PATCH /_fmr/model-pool
POST  /_fmr/pin
POST  /_fmr/auto
GET   /_fmr/logs
```

Every control request requires the local management bearer token. Model IDs
remain JSON fields, and model-pool writes use an optimistic `revision` value.
The status response includes `apiVersion`, `buildVersion`, `instanceId`, and
`features` so desktop clients can attach only to a compatible API major.

The dependency-free `fmr` client provides `start`, `stop`, `status`, `models`,
`providers`, `model-pool`, `config`, `logs`, and `doctor` commands. Use
`-token` for an explicit token or let the client resolve the configured secret.

## Network security

Phase 14 keeps inference on `127.0.0.1:8787` by default. To expose inference
outside the host, set `FMR_BIND` explicitly to a non-loopback address such as
`0.0.0.0:8787`; a separate `FMR_INFERENCE_TOKEN` is then required for bearer
authentication. The management listener is always validated as loopback-only,
regardless of the inference bind. Authentication tokens and other secrets are
never written to application logs or control responses.

The desktop lifecycle core deliberately keeps native tray rendering in a shell
adapter. It provides atomic profile leases, stale-owner recovery, API-major
compatible attach decisions, explicit `desktop-managed` versus `external`
ownership, and user-scoped file autostart without adding runtime Node, npm, or
Rust requirements. Native Tauri packaging is covered by the later desktop
scaffold and Windows packaging phase.

The model-manager API exposes model and route performance fields including TTFT,
latency score, benchmark performance, confidence, and routing score. The typed
control client and `FilterModels` helper support search, provider, access, status,
and capability filters without changing gateway state. Model-pool writes remain
optimistic and return revision conflicts instead of silently overwriting another
client's selection.

Usage windows can poll `GET /_fmr/logs` with time, provider, model, result, and
fallback filters, or subscribe to `GET /_fmr/logs/events` using the authenticated
localhost SSE stream. Events contain request IDs, route metadata, and terminal
results only. Prompts, responses, authorization headers, and secrets have no
representation in the stream.
