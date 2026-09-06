# Free-Model-Router Phase 0–6 Review

**Repository:** `konor123/Free-Model-Router`  
**Reviewed branch:** `main`  
**Reviewed HEAD:** `efdf16b917f68ebeb2b12cca08d765872852ac69`  
**Scope:** PLAN V7 Phase 0–6 implementation review

## 1. Summary

현재 구현은 커밋 기준으로 Phase 6까지 진행되었고, GitHub Actions에서 다음 gate가 모두 통과했다.

```text
go test ./... -race
go vet ./...
go build ./cmd/Free-Model-Router
```

테스트 커버리지도 초기 단계치고 양호하다.

```text
catalog             52.0%
config              68.1%
gateway             78.2%
health              94.9%
latency             92.9%
logging             68.0%
model               69.5%
probe               20.2%
opencode            79.2%
router              81.4%
```

Architecture 방향과 package 분리는 좋다. 특히 identity 타입 분리, FailureScope, route별 health/latency state, catalog tombstone, Automatic/Manual pool, route capability override, mock-server tests, race detector를 CI에 포함한 점은 좋은 출발이다.

그러나 현재 Phase 3–6은 대부분 **독립 package 구현 상태**이고 실제 `app.Run()` runtime에는 아직 통합되지 않았다. 실제 실행 binary는 여전히 OpenCode Public 단일 provider + 단일 `autoPick`/`autoRoute` 기반 Phase 2 수준이다.

**판정:** Phase 7 진입 전 수정 및 Integration Gate 필요.

---

## 2. P0 — Phase 7 전에 반드시 수정

### 2.1 Route-level Access 원칙 위반

PLAN V7에서는 Access의 authoritative source를 `ProviderRoute`로 정했지만 현재 `ProviderModel`에도 `Access`가 존재한다.

현재 catalog reconciliation도 `pm.Access`를 기준으로 Automatic Pool에 모델을 추가한다.

이 구조는 다음을 표현하기 어렵다.

```text
MiMo V2.5
├─ Public → Free
└─ Auth   → Paid
```

수정:

```go
type ProviderModel struct {
    ID           ProviderModelID
    CanonicalKey CanonicalModelKey
    DisplayName  string
    Base         Capabilities
}
```

Pool 자동 포함 여부는 route 기준으로 계산한다.

```text
ProviderModel이 auto-eligible
=
enabled route 중 하나 이상이 Free 또는 Free-tier
```

### 2.2 OpenCode Zen Auth를 Free-tier로 하드코딩하지 말 것

현재 `AuthRoute()`는 `AccessFreeTier`로 고정되어 있다.

이는 PLAN V7의 핵심 안전 규칙과 충돌한다.

```text
API key configured
≠
free-tier confirmed
≠
paid usage authorized
```

확실한 가격/무료티어 판정 전에는:

```go
Access: model.AccessUnknown
```

을 기본값으로 한다.

확인된 metadata가 있을 때만 `Free-tier` 또는 `Paid`로 승격한다.

### 2.3 ProviderModelID와 upstream model ID 분리

현재 `ProviderModelID`는 `<provider>/<model>` 형식이며 model segment에 `/`가 들어가는 것을 금지한다.

그러나 일부 provider는 upstream model ID로 다음 같은 형태를 사용한다.

```text
creator/model-name
org/model/version
```

AFM 내부 identity와 upstream identity를 분리해야 한다.

```go
type ProviderModel struct {
    ID         ProviderModelID
    UpstreamID string

    CanonicalKey CanonicalModelKey
    DisplayName  string
    Base         Capabilities
}

type ProviderRoute struct {
    ID              RouteID
    ModelID         ProviderModelID
    UpstreamModelID string
}
```

### 2.4 OpenAI-compatible protocol이 coding-agent 요청을 보존하지 못함

현재 Gateway request parser는 사실상 다음만 읽는다.

```text
model
messages
stream
max_tokens
temperature
```

따라서 coding client가 보내는 `tools`, `tool_choice`, `response_format`, JSON schema, multimodal content, reasoning parameters 등이 Gateway 단계에서 조용히 유실될 수 있다.

Provider layer에 `Tools` 타입이 있어도 Gateway에서 이를 채우지 않으므로 현재 tool calling은 end-to-end로 동작하지 않는다.

응답도 실제 tool call의 `id`, `name`, `arguments`, `index`를 보존하지 않고 boolean으로 축약한다.

Phase 7 전에 normalized protocol을 최소한 다음까지 확장한다.

```text
MessageContentPart
TextPart
ImagePart

ToolCallDelta
  Index
  ID
  Name
  ArgumentsDelta

ToolChoice
ToolResult
ResponseFormat
JSONSchema
ReasoningDelta string
```

지원하지 않는 client parameter는 parse 단계에서 조용히 버리지 말고 explicit unsupported error로 처리한다.

### 2.5 Failure model의 Retryable / FallbackAllowed 모순

현재 일부 failure는 `Retryable=false`인데 `FallbackAllowed()`는 Canceled만 아니면 true가 될 수 있다.

Router 정책을 명시적 action으로 표현하는 것이 안전하다.

```go
type FailureAction int

const (
    ActionStop FailureAction = iota
    ActionNextRoute
    ActionNextModel
    ActionDisableCredential
    ActionDisableRoute
    ActionCooldownProvider
)
```

```go
DecideFailureAction(f Failure) FailureAction
```

한 곳에서 정책을 결정한다.

### 2.6 Probe가 provider-neutral하지 않음

현재 probe scheduler에서 provider concurrency slot이 `"opencode"`로 하드코딩되어 있다.

`ProviderRoute`에 Provider identity를 포함하고:

```go
AcquireSlot(ctx, route.Provider)
```

로 변경한다.

---

## 3. P1 — 가능한 한 Phase 7 전에 수정

### 3.1 Probe Scheduler가 static snapshot 사용

scheduler 시작 시 전달받은 `catalog`, `pool`, `routes`를 계속 사용한다.

실행 중 새 모델 발견, checkbox 변경, provider disable, route 변경이 일어나도 probe 대상이 갱신되지 않는다.

매 tick 현재 state를 조회하도록 바꾼다.

### 3.2 Probe jitter 미구현

고정 ticker 대신 jitter를 추가해 provider/API에 probe가 동시에 몰리지 않도록 한다.

### 3.3 AcquireSlot busy wait 제거

현재 slot 부족 시 polling loop를 사용한다. Context cancellation이 반영되지 않는다.

```go
AcquireSlot(ctx context.Context, provider ProviderID) error
```

형식으로 바꾸고 semaphore/channel 기반으로 구현한다.

### 3.4 `boolOr()` 구현 버그

현재 `def` 인자를 사용하지 않는다.

```go
func boolOr(b *bool, def bool) bool {
    if b == nil {
        return def
    }
    return *b
}
```

로 수정하고 regression test를 추가한다.

### 3.5 Capability Unknown 상태

현재 capability bool은 실제 미지원과 catalog metadata 부재를 구분하지 못한다.

초기에는 provider capability registry 또는 verified/inferred flag를 두고, 장기적으로 tri-state를 고려한다.

### 3.6 TTFT source priority

현재 real request TTFT가 probe TTFT보다 우선한다.

Routing용은:

```text
ProbeTTFT 우선
RequestTTFT fallback
```

이 더 적절하다.

Real request TTFT는 telemetry용으로 유지한다.

### 3.7 Catalog metadata 보정

`CatalogSnapshot.CreatedAt`을 채우고, revision은 wall-clock nano 대신 Store에서 monotonic sequence로 생성하는 것이 명확하다.

### 3.8 `SelectAll()` 즉시 반영

`SelectAll()`은 Automatic mode 전환과 동시에 현재 catalog의 eligible 모델을 즉시 selection에 반영해야 한다.

### 3.9 Revision conflict 의미 명확화

현재 revision conflict test는 catalog revision 역행을 검사한다.

Control API에서는 실제 optimistic concurrency CAS:

```text
expectedRevision
vs
currentRevision
```

을 구현해야 한다.

---

## 4. Integration 문제

현재 runtime wiring은 사실상:

```text
app.Run
  ↓
OpenCode Public
  ↓
Gateway

Gateway
  ↓
single catalog
single autoPick
single autoRoute
```

Phase 3–6에서 만든:

```text
Model Pool
Capability Filter
Auth Route
Health Manager
TTFT Registry
Probe Scheduler
```

는 아직 실제 Gateway request path에 연결되지 않았다.

단계별 package 개발 자체는 PLAN V7 취지에 맞다.

하지만 이 상태에서 계속 Phase 7, 8, 9를 독립 package로 추가하면 integration 오류가 뒤늦게 한꺼번에 발견될 가능성이 크다.

---

## 5. Phase 6.5 — Integration Gate 추가 권장

Phase 7 ranked fallback 전에 작은 Integration Phase를 추가한다.

### 목표

Fallback/performance scoring은 아직 구현하지 않는다.

Phase 0–6 부품들이 실제 binary에서 하나의 request path로 연결되는지만 확인한다.

### Wiring

```text
App
 ↓
Provider Registry
 ↓
Catalog Store
 ↓
Model Pool
 ↓
Route Registry
 ↓
Capability Filter
 ↓
Health Manager
 ↓
TTFT Registry
 ↓
Probe Scheduler
 ↓
Gateway
```

### 반드시 실제 binary에서 검증

```text
GET /v1/models
→ 현재 catalog + pool 상태 반영

OPENCODE_API_KEY 없음
→ Public route만

OPENCODE_API_KEY 있음
→ Public + Auth route
```

Auth access는 기본 `Unknown`.

```text
Public failure
→ Auth health state unaffected

Auth 401
→ Public unaffected
```

```text
pool 변경
→ probe target 즉시 변경
```

```text
tools request
→ tools-capable candidate만 통과

vision request
→ vision-capable candidate만 통과
```

Mock OpenCode server를 사용해 actual app wiring부터 HTTP request/response까지 end-to-end integration test를 추가한다.

### Gate

```bash
go test ./... -race
go vet ./...
go build ./cmd/Free-Model-Router
```

---

## 6. Phase 7 진입 조건

- [ ] `ProviderModel.Access` 제거
- [ ] OpenCode Auth 기본 Access = Unknown
- [ ] ProviderModelID / UpstreamModelID 분리
- [ ] Tools/tool calls가 end-to-end 보존됨
- [ ] multimodal request를 최소한 손실 없이 표현할 구조 확보
- [ ] FailureAction 정책 정의
- [ ] Probe provider hardcoding 제거
- [ ] Probe live snapshot 사용
- [ ] AcquireSlot context-aware
- [ ] `boolOr()` bug 수정
- [ ] routing용 ProbeTTFT 우선
- [ ] Phase 6.5 integration test 통과
- [ ] 전체 race/vet/build gate 통과

---

## 7. README 상태

현재 README에는 아직:

```text
Phase 0 — Repository Bootstrap (in progress)
```

라고 되어 있다.

실제 상태에 맞게 다음과 같이 갱신하는 것을 권장한다.

```text
Current implementation:
Phase 0–6 package implementation complete
Phase 6.5 integration review/fix in progress
```

---

## 8. 종합 평가

```text
Architecture direction       9/10
Unit-test discipline         8.5/10
Implementation completeness  4/10
Runtime integration          2.5/10
Phase 0–6 package quality     7/10
```

현재 코드를 갈아엎을 필요는 없다.

가장 큰 위험은 코드 품질 자체가 아니라 **Phase별 package 구현 속도가 runtime integration 검증보다 앞서고 있다는 점**이다.

권장 진행 순서:

```text
1. Access model 수정
2. Upstream identity 분리
3. coding-agent protocol 보존
4. FailureAction 정리
5. Probe 보정
6. Phase 6.5 Integration Gate
7. Phase 7 Ranked Fallback
```

이 시점에서 integration을 한 번 고정하면 이후 Phase 7–18의 재작업 가능성을 크게 낮출 수 있다.
