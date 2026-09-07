# Free-Model-Router 데스크톱 셸

이 디렉터리는 Tauri v2 기반 Windows 셸을 포함합니다. 셸은 단일 인스턴스, 트레이 메뉴,
게이트웨이 attach/start 결정, 소유권을 고려한 종료를 관리하며 Provider/Model/Usage log/
Endpoint-key 탭을 가진 하나의 메인 창을 표시합니다. Go 게이트웨이는 사이드카로 동작합니다.

OpenAI 호환 프로바이더 설정, 모델 재검색, 성능·지연 시간·라우팅 점수 정렬을 지원합니다.
키는 OS SecretStore에 저장됩니다. 트레이 메뉴는 `Start with Windows`와 `Exit`만 제공하며,
트레이를 왼쪽 클릭하면 메인 창이 열립니다. 셸이 관리하는 게이트웨이는 설정 변경 후
재시작하고, 외부 게이트웨이는 재시작 필요 상태를 표시합니다.

## 로컬 빌드

```powershell
$env:PATH = "$env:USERPROFILE\.cargo\bin;$env:PATH"
cargo test --manifest-path desktop/src-tauri/Cargo.toml
cargo build --manifest-path desktop/src-tauri/Cargo.toml --release
npm exec --yes --package=@tauri-apps/cli@2.0.0 -- tauri build
```

Tauri bundle 명령은 `desktop/src-tauri/target/release/bundle` 아래에 NSIS·MSI 설치 파일을
생성합니다. portable 패키지는 데스크톱 실행 파일을 Go 사이드카 및 메타데이터와 함께
배치합니다. 최종 사용자에게 Rust, Node, npm, Go는 필요하지 않습니다.
