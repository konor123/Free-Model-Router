# Free-Model-Router v0.8.4

## 주요 변경 사항

- 임의 ID·이름·Base URL을 사용하는 OpenAI-compatible Provider를 공급자별 전용 코드 없이 지원합니다.
- 발견된 compatible 모델은 기본적으로 모델 풀에 선택되고 자동 라우팅과 TTFT probe 대상이 됩니다.
- Provider별 자동 probe를 끄거나 정확한 upstream model ID를 지정해 개별 모델을 제외할 수 있습니다.
- 제외된 모델도 카탈로그에는 표시되어 언제든 다시 선택할 수 있습니다.
- 일부 또는 모든 Provider의 모델 검색이 실패해도 관리 화면과 사용 가능한 Provider는 계속 동작합니다.
- 모델의 Performance·Latency·Score가 없는 이유와 benchmark 갱신 상태를 화면에서 확인할 수 있습니다.
- TTFT는 90초 이내의 신선한 측정값만 표시하며 오래된 값은 별도로 구분합니다.
- Provider 편집 내용이 저장되지 않은 상태에서 모델 선택을 바꿔 편집 내용이 의도치 않게 저장되는 문제를 방지했습니다.
- Gateway 시작·재시작·종료 작업을 UI 이벤트 스레드 밖에서 처리해 화면 응답성을 개선했습니다.

## 설치 파일

- 일반 설치: NSIS 설치 파일
- 기업·관리자 배포: MSI 설치 파일
- 압축 파일: 실행 파일과 필요한 구성 요소가 포함된 ZIP 파일
