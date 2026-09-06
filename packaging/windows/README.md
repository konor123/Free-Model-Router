# Windows packaging contract

Phase 18 keeps the end-user package independent of Node, npm, Rust, and Go.
Those tools are build-time requirements only. The generated package contains:

- `Free-Model-Router-desktop.exe`, when a Tauri artifact is supplied and
  `desktopIncluded` is true in the manifest;
- `Free-Model-Router.exe`, the Go gateway sidecar;
- `fmr.exe`, the separate control CLI;
- `Free-Model-Router.exe.metadata.json`, with target, API, ownership, and SHA-256;
- `manifest.json`, validated against `manifest.schema.json`.

From a Windows checkout, run:

```powershell
.\scripts\package-windows.ps1 -Version v0.18.0
```

Build the native shell first, then include it in the portable package:

```powershell
cargo build --manifest-path desktop\src-tauri\Cargo.toml --release
.\scripts\package-windows.ps1 `
  -Version v0.18.0 `
  -DesktopExecutable desktop\src-tauri\target\release\free-model-router-desktop.exe
```

The script builds the Windows Go binaries, generates and verifies sidecar metadata,
writes the package manifest with `desktopIncluded: true`, and creates a portable ZIP.

To build the native installers locally, run from `desktop`:

```powershell
npm exec --yes --package=@tauri-apps/cli@2.0.0 -- tauri build
```

This produces both NSIS and MSI bundles under
`desktop\src-tauri\target\release\bundle`. The native Tauri shell, release
executable, and both local bundle formats are validated in this repository.
Clean Windows VM install, first-run, autostart, upgrade, and uninstall gates still
require a separate VM and installer/signing environment. The manifest records the
intended upgrade and uninstall policy without claiming those external gates passed.
