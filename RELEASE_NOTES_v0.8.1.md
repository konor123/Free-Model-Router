# Free-Model-Router v0.8.1

이번 버전은 OpenCode 무료 모델 정책과 Windows 데스크톱 실행 안정성을 개선했습니다.

## 주요 변경 사항

- OpenCode Zen의 전체 모델 목록에서 유료 모델이 무료 익명 카탈로그에 포함되지 않도록 수정했습니다.
- 공식적으로 확인된 무료 모델만 키 유무와 관계없이 표시합니다.
- OpenCode Zen 키를 설정해도 동일한 무료 모델에 Public 및 Zen 경로를 함께 사용할 수 있습니다.
- Desktop managed gateway 실행 시 CMD/콘솔 창이 표시되지 않습니다.
- Windows 자동 시작을 CMD 파일 대신 사용자 레지스트리의 직접 실행 항목으로 변경했습니다.
- OpenCode native 요청의 redirect를 차단해 인증 키가 다른 주소로 전달되지 않도록 했습니다.

## 무료 모델 정책

OpenCode Zen의 `/models` 응답은 무료 전용 목록이 아닙니다. FMR은 공식 가격 문서에서 무료로 확인된 모델만 익명 카탈로그에 포함하며, 새 모델은 검증 전까지 자동으로 제외합니다.

## 보안 안내

- Public route에는 Zen 인증 키를 보내지 않습니다.
- Zen 키는 SecretStore에서 요청 시점에 읽으며 설정 파일과 카탈로그에 저장하지 않습니다.
- 기존 CLI 및 standalone gateway의 콘솔 동작은 유지됩니다.
