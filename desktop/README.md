# Free-Model-Router desktop shell

This directory contains the native Tauri v2 Windows shell. The shell owns the
single-instance boundary, tray menu, gateway attach/start decision, ownership-aware
shutdown, and the model/usage child windows. The Go gateway remains the sidecar.

## Local build

```powershell
$env:PATH = "$env:USERPROFILE\.cargo\bin;$env:PATH"
cargo test --manifest-path desktop/src-tauri/Cargo.toml
cargo build --manifest-path desktop/src-tauri/Cargo.toml --release
npm exec --yes --package=@tauri-apps/cli@2.0.0 -- tauri build
```

The Tauri bundle command produces NSIS and MSI installers under
`desktop/src-tauri/target/release/bundle`. The portable release package places the
resulting desktop executable next to the Go sidecar and its metadata. End users do
not need Rust, Node, npm, or Go.
