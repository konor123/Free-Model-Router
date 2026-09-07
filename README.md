# Free-Model-Router

Free Model Router(FMR)는 무료·무료 티어 LLM 프로바이더를 검색하고 기능·성능·TTFT·상태를
기준으로 요청을 라우팅하는 로컬 AI 게이트웨이(Go)와 Tauri 데스크톱 앱입니다.

## 현황

Phase 0–6.5 런타임 통합, Phase 7 실패 인지형 폴백, Phase 8 스트리밍 커밋 가드,
Phase 9 벤치마크·신뢰도 기반 점수, Phase 10 NVIDIA·Gemini·xAI 프로바이더,
Phase 11 영속화·스키마 마이그레이션·OS 상태 디렉터리·SecretStore,
Phase 12 요청별 사용량 로그, Phase 13 localhost 제어 API·`fmr` CLI,
Phase 14 loopback 관리·LAN 추론 인증, Phase 15 데스크톱 수명주기,
Phase 16 모델 필터·성능 메타데이터·라우팅 점수, Phase 17 마스킹된 실시간 사용량 이벤트,
Phase 18 Windows 사이드카 메타데이터·매니페스트·Tauri 셸·NSIS/MSI·portable ZIP 빌드를
구현했습니다. 클린 VM 설치·첫 실행·자동 시작·업그레이드·제거 검증은 별도 환경이 필요합니다.

## 데스크톱 앱

앱은 보조 창 없이 하나의 메인 창에서 `Provider`, `Model`, `Usage log`, `Endpoint-key`
탭을 제공합니다. OpenAI 호환 프로바이더를 설정하고 키를 OS SecretStore에 저장할 수
있습니다. 모델을 다시 검색할 수 있으며 성능·지연 시간·라우팅 점수로 정렬할 수 있습니다.

트레이 메뉴는 `Start with Windows`와 `Exit`로 단순화되었습니다. 트레이 아이콘을 왼쪽
클릭하면 메인 창이 표시됩니다. 데스크톱이 관리하는 게이트웨이는 설정 변경 후 자동으로
재시작되며, 외부에서 실행 중인 게이트웨이는 `restart required` 상태를 표시하고 수동
재시작이 필요합니다.

## 구조

```
cmd/Free-Model-Router gateway binary entrypoint
internal/app           wiring
internal/model         core types
internal/provider      provider interfaces
internal/providers     concrete provider implementations
internal/router        eligibility + fallback
internal/gateway       OpenAI-compatible HTTP surface
internal/config        configuration + persistence + secrets
internal/usage         redacted request/attempt usage log
internal/control       authenticated desktop/CLI control API
internal/security      bind and bearer-token policy
internal/desktop       tray-shell lifecycle contracts
internal/packaging     sidecar metadata and release manifest
desktop/src-tauri      native Tauri tray and control shell
```

의존성 규칙: `router`는 프로바이더 인터페이스에만 의존하며, 구체 프로바이더 구현에는
의존하지 않습니다.

## 빌드·검증

```powershell
go test ./...
go vet ./...
go build ./cmd/Free-Model-Router
```

## 폴백 제한

요청별 폴백은 기본 4회, 30초로 제한됩니다.

```powershell
$env:FMR_MAX_ATTEMPTS="4"
$env:FMR_FAILOVER_BUDGET_MS="30000"
```

## 영속화와 보안 키

설정은 OS 사용자 설정 디렉터리의 버전 있는 `PersistedConfig`로 원자적으로 저장되고,
캐시는 OS 사용자 캐시 디렉터리를 사용합니다. 키 조회 순서는 다음과 같으며 실패 시
안전하게 거부합니다.

1. 일치하는 환경 변수(예: `GEMINI_API_KEY`)
2. 운영체제 기본 자격 증명 저장소(SecretStore)
3. 사용자만 읽을 수 있는 별도 `secrets.json` 폴백 파일

키 값은 설정이나 사용량 로그에 절대 포함되지 않습니다. 사용량 로그에는 경로 메타데이터,
실패 분류, TTFT, 지연 시간, 커밋 상태, HTTP 상태, 토큰 수가 저장되지만 프롬프트·응답·
API 키·Authorization 헤더는 저장되지 않습니다. 보호된 JSONL 로그는 20MB·30일로 제한됩니다.

## 제어 API와 CLI

인증된 localhost 제어면은 기본적으로 `127.0.0.1:8788`에서 수신합니다.

```text
GET   /_fmr/status       GET   /_fmr/providers   GET   /_fmr/models
GET   /_fmr/model-pool   PUT   /_fmr/model-pool  PATCH /_fmr/model-pool
POST  /_fmr/pin          POST  /_fmr/auto        GET   /_fmr/logs
GET   /_fmr/config       PUT   /_fmr/config      POST  /_fmr/catalog/refresh
```

모든 제어 요청에는 로컬 관리 bearer token이 필요합니다. `fmr` 클라이언트는 `start`,
`stop`, `status`, `models`, `providers`, `model-pool`, `config`, `logs`, `doctor` 명령을
제공하며, 명시적 토큰은 `-token`으로 전달할 수 있습니다.

## 네트워크 보안

추론은 기본적으로 `127.0.0.1:8787`에서만 동작합니다. 외부에 공개하려면 `FMR_BIND`를
`0.0.0.0:8787` 같은 non-loopback 주소로 명시해야 하며, 별도의 `FMR_INFERENCE_TOKEN`
bearer 인증이 필요합니다. 관리 리스너는 항상 loopback 전용입니다. 인증 토큰과 보안 키는
애플리케이션 로그나 제어 응답에 기록되지 않습니다.

## Windows 패키징

최종 사용자 런타임에는 Node, npm, Rust, Go가 필요하지 않습니다. Windows에서 다음을 실행합니다.

```powershell
.\scripts\package-windows.ps1 `
  -Version v0.8.0 `
  -DesktopExecutable desktop\src-tauri\target\release\free-model-router-desktop.exe
```

실행 전 네이티브 셸을 빌드합니다.

```powershell
cargo build --manifest-path desktop\src-tauri\Cargo.toml --release
```

워크플로는 사이드카 메타데이터와 매니페스트를 생성·검증합니다. 클린 VM 설치·첫 실행·
자동 시작·업그레이드·제거 검증은 별도 Windows VM이 필요하며, 이 저장소에서는 해당 검증을
수행했다고 주장하지 않습니다.

모델 관리자 API는 TTFT, 지연 시간 점수, 벤치마크 성능, 신뢰도, 라우팅 점수를 제공합니다.
사용량 로그는 `GET /_fmr/logs` 조회 또는 인증된 `GET /_fmr/logs/events` SSE 구독으로
확인할 수 있습니다. 스트림에도 프롬프트·응답·Authorization 헤더·보안 키는 나타나지 않습니다.
