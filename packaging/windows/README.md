# Windows 패키징 계약

최종 사용자 패키지는 Node, npm, Rust, Go에 의존하지 않습니다. 이 도구들은 빌드 시에만
필요합니다. 생성되는 패키지에는 다음이 포함됩니다.

- `Free-Model-Router-desktop.exe`(Tauri 아티팩트를 공급하고 `desktopIncluded`가 true인 경우)
- `Free-Model-Router.exe`(Go 게이트웨이 사이드카)
- `fmr.exe`(별도 제어 CLI)
- `Free-Model-Router.exe.metadata.json`(target, API, ownership, SHA-256)
- `manifest.json`(`manifest.schema.json`으로 검증)

Windows 체크아웃에서 다음을 실행합니다.

```powershell
.\scripts\package-windows.ps1 -Version v0.8.0
```

네이티브 셸을 먼저 빌드한 뒤 portable 패키지에 포함합니다.

```powershell
cargo build --manifest-path desktop\src-tauri\Cargo.toml --release
.\scripts\package-windows.ps1 `
  -Version v0.8.0 `
  -DesktopExecutable desktop\src-tauri\target\release\free-model-router-desktop.exe
```

스크립트는 Windows Go 바이너리와 사이드카 메타데이터를 생성·검증하고,
`desktopIncluded: true`인 매니페스트와 portable ZIP을 만듭니다.

## 네이티브 설치 파일

`desktop`에서 다음을 실행합니다.

```powershell
npm exec --yes --package=@tauri-apps/cli@2.0.0 -- tauri build
```

`desktop\src-tauri\target\release\bundle` 아래에 NSIS·MSI 번들이 생성됩니다. 로컬 셸,
릴리스 실행 파일, 번들 생성은 이 저장소에서 확인할 수 있지만, 클린 Windows VM 설치·첫
실행·자동 시작·업그레이드·제거 검증은 별도 VM과 설치·서명 환경이 필요합니다. 매니페스트는
의도한 업그레이드·제거 정책을 기록할 뿐, 외부 게이트 통과를 의미하지 않습니다.
