# Auto Free Models — PLAN V7

> 상태: **Phased Build & Validation Plan**
>
> 목표:
>
> **무료/무료티어 LLM을 자동 탐색하고, capability + performance + TTFT + health를 기준으로 최적 모델을 선택·fallback하는 Go 기반 로컬 AI gateway와 Tauri desktop UI를 단계적으로 구축한다.**
>
> 원칙:
>
> - 각 Phase는 **독립적으로 빌드·테스트·판정 가능**해야 한다.
> - 한 Phase에서 새로운 domain abstraction과 새로운 provider를 동시에 과도하게 추가하지 않는다.
> - 다음 Phase는 이전 Phase의 gate를 통과한 뒤 진행한다.
> - Desktop/UI는 core router가 안정된 뒤 시작한다.

---

# 1. 최종 구조

```text
Tauri Desktop
  ├─ Tray
  ├─ Model Manager
  └─ Usage Log
          │
          ▼
 Local Control API
          │
          ▼
     AFM Gateway
        (Go)
          │
          ├─ OpenCode
          ├─ OpenRouter
          ├─ NVIDIA
          ├─ Gemini
          └─ xAI
```

omfm은 fork base로 사용하지 않는다.

```text
omfm
→ reference implementation only
```

---

# 2. 핵심 Domain Contract

V7에서 다음 identity를 고정한다.

```text
CanonicalModelKey
ProviderModelID
RouteID
```

## CanonicalModelKey

성능 benchmark identity.

예:

```text
mimo-v2.5
glm-5.3-flash
```

## ProviderModelID

사용자가 실제 Model Pool에서 선택하는 모델 identity.

예:

```text
opencode/mimo-v2.5
openrouter/mimo-v2.5
```

같은 provider 안의 variant도 별도 ID를 가진다.

## RouteID

실제 endpoint/auth path.

예:

```text
opencode-public::mimo-v2.5
opencode-zen::mimo-v2.5
```

RouteID는 내부 diagnostic용이며 외부 `model=` 값으로 사용하지 않는다.

---

# 3. 핵심 정책

## Model Pool

```text
[✓] ProviderModel 포함
[ ] ProviderModel 제외
```

Source of truth:

```go
SelectedProviderModelIDs []ProviderModelID
```

지원:

```text
Select All Eligible
Clear All
Select Visible
Clear Visible
Select Provider
Clear Provider
```

---

## External model IDs

```text
afm/auto
opencode/mimo-v2.5
openrouter/mimo-v2.5
```

의미:

```text
afm/auto
→ Model Pool 전체 자동 routing

ProviderModelID
→ 해당 모델을 primary로 명시
→ pool membership은 우회
→ capability/access/health/paid policy는 여전히 적용
→ 실패 시 selected pool로 fallback
```

Pinned model은 반드시 Model Pool 안에 있어야 한다.

---

## Access

Authoritative source는 Route.

```text
Unknown
Free
Free-tier
Paid
```

기본 자동 routing:

```text
Free
Free-tier
```

```text
Unknown
Paid
```

는 자동 routing에서 제외.

Paid 사용은 explicit opt-in.

---

## Capability

실제 eligibility는:

```text
ProviderModel base capability
∩
Route/protocol capability
```

로 계산한다.

필수 초기 capability:

```text
Streaming
Tools
Vision
Structured Output
Reasoning
Context Length
Max Output
```

Capability mismatch는 scoring 전에 제외한다.

---

# 4. Phase 0 — Repository Bootstrap

## 구현

```text
new Git repository
Go module
basic config package
logging
CI
lint/test/build scripts
```

권장 구조:

```text
cmd/afm
internal/app
internal/model
internal/provider
internal/providers
internal/router
internal/gateway
internal/config
```

Dependency rule:

```text
router → provider interfaces
provider implementations → provider interfaces

router ✕ provider implementations
```

## Gate

```bash
go test ./...
go vet ./...
go build ./cmd/afm
```

## 완료 조건

- 빈 Gateway binary가 빌드된다.
- CI가 통과한다.
- package dependency 방향이 정리되어 있다.

---

# 5. Phase 1 — Identity & Core Types

## 구현

```text
CanonicalModelKey
ProviderModelID
RouteID

CanonicalModel
ProviderModel
ProviderRoute

AccessClass
Capabilities
RequestRequirements

FailureClass
FailureScope
```

Failure:

```go
type Failure struct {
    Class     FailureClass
    Scope     FailureScope
    Retryable bool
}
```

Scope:

```text
Request
Model
Route
Credential
Provider
```

`FailureCanceled`도 포함한다.

```text
client disconnect
context canceled
→ upstream cancel
→ fallback 금지
```

## 테스트

```text
same canonical / different provider
→ different ProviderModelID

same provider / different variant
→ different ProviderModelID

same ProviderModel / different route
→ different RouteID
```

## Gate

```bash
go test ./internal/model/... ./internal/provider/...
```

## 완료 조건

- ID collision이 없다.
- variant를 안전하게 표현한다.
- Error scope와 retryability가 정의되어 있다.

---

# 6. Phase 2 — Single Provider End-to-End

먼저 **OpenCode Public만** 구현한다.

아직 scoring도 multi-provider도 넣지 않는다.

## 구현

```text
OpenCode catalog discovery
ProviderModel normalization
Public Route
OpenAI-compatible request
streaming relay
```

기본 API:

```text
GET  /v1/models
POST /v1/chat/completions
```

외부 model ID:

```text
afm/auto
opencode/<model>
```

이 Phase에서는 `afm/auto`가 선택 가능한 OpenCode 모델 하나를 선택해도 된다.

## Protocol translation

Provider adapter는:

```text
OpenAI request
→ normalized request
→ provider request
```

변환한다.

Unsupported parameter는 조용히 삭제하지 않는다.

```text
unsupported
→ explicit error
```

## 테스트

Mock server로:

```text
catalog
normal response
streaming
malformed response
timeout
client cancel
```

## Gate

```bash
go test ./internal/providers/opencode/... ./internal/gateway/...
```

실사용 smoke test:

```text
curl → AFM → OpenCode Public
```

## 완료 조건

- 단일 provider proxy가 안정적으로 동작한다.
- stream cancellation이 정상 동작한다.
- OpenAI client가 `/v1/models`와 chat completion을 사용할 수 있다.

---

# 7. Phase 3 — Model Pool & Catalog State

## 구현

Model Pool:

```go
type ModelPoolConfig struct {
    Revision int64
    SelectedProviderModelIDs []ProviderModelID
}
```

Catalog는 immutable snapshot으로 관리한다.

```go
type CatalogSnapshot struct {
    Revision  int64
    CreatedAt time.Time
    Models    map[ProviderModelID]ProviderModel
}
```

새 catalog는 완성 후 atomic swap한다.

## Catalog reconciliation

```text
selected model disappears
→ config에서 즉시 삭제하지 않음
→ unavailable/tombstone

model returns
→ 이전 selection 복구

Free → Paid
→ selection은 유지
→ paid policy 때문에 auto routing 제외
```

Pool mode:

```text
Automatic
Manual
```

권장:

```text
Automatic
→ 새 free/free-tier 모델 자동 포함

Manual
→ 사용자가 선택한 모델만 포함
```

사용자가 checkbox를 직접 바꾸면 Manual mode로 전환.

## 테스트

```text
new model appears
model disappears
model returns
access changes
revision conflict
```

## Gate

```bash
go test ./internal/catalog/... ./internal/config/...
```

## 완료 조건

- catalog refresh가 selection을 파괴하지 않는다.
- 새 모델 등장/삭제에 안정적이다.
- revision conflict를 감지한다.

---

# 8. Phase 4 — Capability Filtering

## 구현

```text
ProviderModel capability
Route capability override
EffectiveCapabilities()
RequestRequirements
```

Eligibility:

```text
request intent
↓
model pool
↓
effective capability
↓
protocol compatibility
↓
access
```

## 테스트

```text
vision → text-only 제외
tools → no-tools 제외
structured output → incompatible 제외
context overflow → 제외
streaming unsupported → 제외
```

## Gate

```bash
go test ./internal/router/... -run Capability
```

## 완료 조건

- capability mismatch가 score 계산 전에 제거된다.
- fallback도 동일 capability requirements를 유지한다.

---

# 9. Phase 5 — OpenCode Auth Dual-Route

이제 같은 ProviderModel 아래 두 route를 만든다.

```text
opencode/mimo-v2.5
├─ Public
└─ Zen Auth
```

## 구현

```text
OPENCODE_API_KEY optional
Public/Auth route discovery
route-specific access
route-specific status
```

Access 예:

```text
Public → Free
Auth   → Free-tier / Paid / Unknown
```

## Route isolation

다음은 독립:

```text
access
auth status
health
latency
cooldown
quota
```

## 테스트

```text
API key 없음
→ Public only

API key 있음
→ Auth route 추가

Auth 401
→ Public unaffected
```

## Gate

```bash
go test ./internal/providers/opencode/... -run Route
```

## 완료 조건

- 동일 ProviderModel에 Public/Auth route가 공존한다.
- route state가 서로 오염되지 않는다.

---

# 10. Phase 6 — TTFT Probe & Health

## Probe metric

Primary:

```text
TTFT
= Time To First Semantic Event
```

HTTP response header 시간은 사용하지 않는다.

## 구현

```text
ProbeTTFTEWMA
RequestTTFTEWMA
RequestTotalEWMA

cooldown
quota
health
jitter
provider concurrency cap
```

EWMA:

\[
EWMA_{new}
=
0.25 \cdot sample
+
0.75 \cdot EWMA_{old}
\]

Probe 대상:

```text
selected pool
enabled route
Free / Free-tier
not cooling down
```

Paid / Unknown은 기본 auto probe 제외.

## 테스트

```text
first semantic event timing
cooldown skip
provider concurrency cap
EWMA update
unknown latency
```

## Gate

```bash
go test ./internal/latency/... ./internal/health/...
```

## 완료 조건

- probe가 quota를 과도하게 소모하지 않는다.
- route별 TTFT가 독립 유지된다.

---

# 11. Phase 7 — Basic Routing & Failure-Aware Fallback

아직 Artificial Analysis는 넣지 않는다.

우선:

```text
health + TTFT
```

만으로 router를 완성한다.

## Eligibility order

```text
1. explicit model intent
2. model pool
3. capability
4. protocol
5. access
6. enabled state
7. health/quota/cooldown
8. rank
```

## Failure handling

```text
429 / quota
→ same ProviderModel alternate route 우선

timeout
→ budget 내 alternate route 가능

400 / unsupported / protocol
→ same model retry 금지
→ next ProviderModel

credential auth failure
→ same credential candidates 제외

5xx
→ next ProviderModel 우선
```

## Failover budget

```text
AFM_MAX_ATTEMPTS=4
AFM_FAILOVER_BUDGET_MS=30000
```

## 테스트

```text
Public 429 → Auth
unsupported → next model
credential failure scope
all candidates fail
budget exhausted
client canceled → no fallback
```

## Gate

```bash
go test ./internal/router/...
```

## 완료 조건

- Artificial Analysis 없이도 router가 안정적으로 동작한다.
- 잘못된 request는 다른 모델에 반복 전송하지 않는다.

---

# 12. Phase 8 — Streaming Commit Guard

Fallback과 streaming을 별도 Phase로 검증한다.

## State

```go
type AttemptState struct {
    WireCommitted     bool
    SemanticCommitted bool
}
```

## 규칙

Fallback 허용:

```text
WireCommitted == false
AND
SemanticCommitted == false
```

권장 흐름:

```text
upstream 연결
↓
first semantic event buffer
↓
candidate valid 확인
↓
HTTP/SSE downstream commit
↓
first semantic event forward
↓
fallback forbidden
```

다음도 commit 전에 client로 flush하지 않는다.

```text
heartbeat
SSE comment
empty delta
upstream headers
```

## 테스트

```text
fail before first semantic event
→ fallback

fail after text delta
→ no fallback

fail after tool-call delta
→ no fallback

headers만 받고 upstream fail
→ fallback 가능

client disconnected
→ no fallback
```

## Gate

```bash
go test ./internal/gateway/... -run Streaming
```

## 완료 조건

- 두 모델의 stream이 섞이지 않는다.
- fallback 가능한 시점이 명확하다.

---

# 13. Phase 9 — Performance Scoring

Core router가 먼저 완성된 뒤 Artificial Analysis를 추가한다.

## Benchmark identity

```text
CanonicalModelKey
```

Binding:

```go
type BenchmarkBinding struct {
    CanonicalModelKey CanonicalModelKey
    SourceModelID     string
    Confidence        float64
    MatchMethod       MatchMethod
}
```

`SourceModelID`에는 가능하면 Artificial Analysis stable model ID 저장.

## Score

\[
P = 0.55C_p + 0.30A_p + 0.15I_p
\]

Unknown:

```text
50
```

Confidence:

\[
P_{effective}
=
cP + (1-c)50
\]

Final:

\[
R = 0.70P + 0.30L
\]

## Cache

```text
local snapshot
last-known-good
retrievedAt
source
version
```

AA unavailable:

```text
cache 사용
없으면 P=50
```

## 테스트

```text
exact match
family match
variant confidence
missing metrics
unknown model
stale cache
percentile stability
```

## Gate

```bash
go test ./internal/matcher/... ./internal/scoring/...
```

## 완료 조건

- performance 데이터가 없어도 router는 계속 동작한다.
- alias mismatch가 높은 score를 과도하게 상속하지 않는다.

---

# 14. Phase 10 — Additional Providers

한 번에 하나씩 추가한다.

순서:

```text
NVIDIA
Gemini
xAI
```

각 provider마다 반드시 같은 checklist를 통과한다.

```text
catalog
ProviderModel identity
Route identity
Access
Capabilities
Auth
Streaming
Errors
Quota
Mock tests
```

한 provider 완료 후 다음 provider 진행.

## Gate

각 provider:

```bash
go test ./internal/providers/<provider>/...
```

## 완료 조건

- provider-specific if/else가 router에 추가되지 않는다.
- Provider interface만으로 연결된다.

---

# 15. Phase 11 — Persistence & Secret Storage

운영 state를 분리한다.

```text
config
model pool
provider enabled
pinned model

secrets
API keys
management token

cache
catalog
AA snapshot
TTFT EWMA

runtime
PID
instance ID

logs
usage
```

## Config

```go
type PersistedConfig struct {
    SchemaVersion int
}
```

Atomic write 사용.

OS별 config/cache directory 사용.

## SecretStore

우선순위:

```text
environment override
OS credential store
protected user-only file fallback
```

## 테스트

```text
atomic write
schema migration
corrupt config recovery
secret not logged
```

## Gate

```bash
go test ./internal/config/...
```

---

# 16. Phase 12 — Usage Logging

## Request / Attempt 분리

```text
Request
└─ Attempt 1
└─ Attempt 2
└─ Attempt 3
```

기록:

```text
ProviderModel
Route
FailureClass
FailureScope
TTFT
Total latency
Committed
HTTP status
tokens
```

저장 금지:

```text
prompt
response
API key
Authorization header
```

Retention:

```text
20 MB
30 days
```

## Gate

```bash
go test ./internal/usage/...
```

## 완료 조건

- fallback 때문에 token/usage 통계가 중복되지 않는다.

---

# 17. Phase 13 — Control API & CLI

Management API:

```text
127.0.0.1 only
```

local management token 사용.

## API

```text
GET /_afm/status
GET /_afm/providers
GET /_afm/models
GET /_afm/model-pool
PUT /_afm/model-pool
PATCH /_afm/model-pool
POST /_afm/pin
POST /_afm/auto
GET /_afm/logs
```

`ProviderModelID`는 path parameter로 넣지 않는다.

Pin:

```json
{
  "providerModelId": "opencode/mimo-v2.5",
  "revision": 12
}
```

## Version handshake

`/_afm/status`:

```json
{
  "apiVersion": "1",
  "buildVersion": "0.7.0",
  "instanceId": "...",
  "features": []
}
```

Desktop attach 정책:

```text
compatible API major
→ attach

incompatible API major
→ reject
```

## CLI

```text
afm start
afm stop
afm status
afm models
afm providers
afm model-pool
afm config
afm logs
afm doctor
```

## Gate

```bash
go test ./internal/control/... ./cmd/afm/...
```

---

# 18. Phase 14 — Security

Inference API 기본 bind:

```text
127.0.0.1
```

LAN 공개는 explicit 설정:

```text
AFM_BIND=0.0.0.0
```

LAN bind 시 token auth 사용.

Management API는 항상:

```text
127.0.0.1
```

## Gate

```text
LAN에서 management endpoint 접근 불가
invalid token 거부
secrets log 미노출
```

---

# 19. Phase 15 — Tauri Tray

Core가 끝난 뒤 Desktop 시작.

구현:

```text
Tray
Tooltip
Provider menu
Gateway start/attach
Single Instance
Autostart
```

Gateway ownership:

```text
desktop-managed
external
```

external Gateway는 Desktop Quit으로 종료하지 않는다.

Gateway 중복 실행 방지:

```text
PID
lock
port
instance ID
```

## Gate

```text
second Desktop launch → tray duplication 없음
existing Gateway attach
version mismatch 표시
autostart launch
```

---

# 20. Phase 16 — Model Manager

별도 window.

기능:

```text
checkbox pool
전체 선택/해제
현재 필터 선택/해제
Provider별 선택/해제

search
Provider filter
Access filter
Status filter
Capability filter

Performance
TTFT
Routing Score
Route details

Pinned model
Auto Select
```

Route detail:

```text
MiMo V2.5 / OpenCode

Public
  Free
  TTFT 640 ms

Zen Auth
  Free-tier
  TTFT 510 ms
```

## Gate

```text
checkbox → Gateway pool 반영
bulk select 정확성
provider selection 독립성
revision conflict 처리
```

---

# 21. Phase 17 — Usage Log Window

별도 window.

기본 행:

```text
Time
Final Model
Provider
Attempts
TTFT
Total latency
Result
```

Expand:

```text
Attempt 1
Attempt 2
...
```

필터:

```text
Time
Provider
Model
Result
Fallback
```

## Gate

```text
live request 표시
fallback chain 표시
log filtering
prompt/response 미노출
```

---

# 22. Phase 18 — Packaging

정식 Desktop:

```text
Tauri executable
+
Go Gateway sidecar
```

사용자에게 Node/npm 필요 없음.

별도 CLI binary도 제공.

초기 우선 플랫폼:

```text
Windows
```

후속:

```text
macOS
Linux
```

## Gate

```text
clean Windows VM install
first run
autostart
upgrade
uninstall
```

---

# 23. Phase별 공통 종료 조건

각 Phase는 다음을 모두 만족해야 완료다.

```text
1. 코드 빌드
2. 해당 Phase unit tests 통과
3. 기존 전체 test regression 없음
4. config/schema 변경 기록
5. 문서 업데이트
6. 다음 Phase에 필요한 public contract 확정
```

공통 gate:

```bash
go test ./...
go vet ./...
go build ./cmd/afm
```

Desktop 시작 후 추가:

```text
Tauri build
frontend test
```

---

# 24. Milestone 요약

## Milestone A — Working Gateway

Phase 0–8

```text
Go
OpenCode Public/Auth
OpenRouter
Model Pool
Capabilities
TTFT
Fallback
Streaming guard
```

목표:

```text
실제 coding client가 안정적으로 사용 가능
```

---

## Milestone B — Smart Router

Phase 9–10

```text
Artificial Analysis
Performance scoring
NVIDIA
Gemini
xAI
```

목표:

```text
성능 + TTFT 기반 자동 모델 선택
```

---

## Milestone C — Operable Gateway

Phase 11–14

```text
Persistence
Secrets
Usage
Control API
CLI
Security
```

목표:

```text
장기 실행 가능한 로컬 서비스
```

---

## Milestone D — Desktop Product

Phase 15–18

```text
Tray
Model Manager
Usage Log
Autostart
Installer
```

목표:

```text
일반 사용자가 CLI 없이 제어 가능
```

---

# 25. 최종 핵심 원칙

```text
Identity first
Capability before score
Route-specific state
Unknown access is not free
Fallback only when safe
No fallback after downstream commit
Catalog refresh must preserve user intent
Explicit model IDs are deterministic
Provider details stay out of router
Core before Desktop
Every Phase must be independently testable
```
