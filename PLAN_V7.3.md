# V7.3 구현 계획 — OpenEvals 성능 데이터 및 경로별 TTFT

## 목표

- Provider 편집 draft가 비동기 상태 새로고침으로 사라지지 않게 한다.
- Hugging Face의 `OpenEvals/leaderboard-data`를 유일한 benchmark 데이터셋으로 수집한다.
- Performance는 실제 OpenEvals 데이터와 안전한 모델 매칭이 있을 때만 표시한다.
- TTFT를 `ProviderModelID × RouteID` 단위로 관측하고, 최신 상태만 라우팅에 사용한다.

## 작업 순서

1. Desktop provider 편집 상태를 서버 확정 설정, 비밀정보를 제외한 draft, 일시적 API key 입력으로 분리한다.
2. revision-pinned OpenEvals `leaderboard.json` fetcher, 정규화, provenance, cache lifecycle을 추가한다.
3. 모호하지 않은 exact/family benchmark binding만 자동 적용하고 UI에 source·stale·evidence를 표시한다.
4. route별 probe/request TTFT 관측, 실패 분류, freshness, control 응답, model-tab polling을 추가한다.
5. 단위·통합·Desktop 검증, 리뷰, 한국어 로그, 커밋과 push를 완료한다.

## 안전 정책

- benchmark 없는 모델은 `-`이며 추정 점수를 만들지 않는다.
- OpenEvals `aggregateScore`는 사용하지 않고 승인된 benchmark percentile만 사용한다.
- Availability benchmark는 만들지 않으며 nil로 둔다.
- API key는 frontend state, HTML, 로그, 테스트 hook에 저장하지 않는다.
- 자동 probe는 선택·활성화된 Free/Free-tier route만 대상으로 하며 유료/unknown route는 제외한다.
- stale TTFT는 표시할 수 있지만 routing score에는 사용하지 않는다.
