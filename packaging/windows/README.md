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

The script builds the Windows Go binaries, generates and verifies sidecar
metadata, writes the package manifest, and creates a portable ZIP. A future
Tauri build can be included with `-DesktopExecutable path\to\desktop.exe`.

The repository currently cannot run the native Tauri build or clean Windows VM
install, first-run, autostart, upgrade, and uninstall gates because the Rust /
Tauri toolchain, signing/installer tools, and a clean VM are not available in
the development environment. The manifest records the intended upgrade and
uninstall policy without claiming those gates have passed.
