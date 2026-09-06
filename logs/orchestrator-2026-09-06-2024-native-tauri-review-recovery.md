# Handoff log: native Tauri review recovery and Windows bundle validation

- Request: continue `PLAN_V7.1.md`, leave a handoff log for this turn, and push the corrected phase gate without staging the pre-existing `.codegraph/` directory.
- Base revision: `0408260689b6faffd90f2941d6d85cbbcc686c6d`.

## Changes completed this turn

- Reproduced the OpenCodeReview frontend finding with a red Node regression test. The model search previously replaced the whole application DOM on every keystroke, losing focus and cursor position.
- Added `desktop/frontend/app.test.js` and the `npm test` script.
- Moved model filtering into state, rendered only `#model-table-body` during search, and rebound model-selection checkboxes after row updates. The search input is no longer destroyed per keystroke.
- Added the frontend regression test to the Windows release workflow.
- Added the required `icons/icon.ico` to the Tauri bundle configuration. The initial local `tauri build` exposed this missing configuration; the corrected build succeeded.
- Corrected the desktop `tauri` npm script to use the standard `tauri` executable and updated native packaging/release documentation.
- Updated the Windows workflow to build native Tauri bundles and upload NSIS/MSI artifacts.

## Validation evidence

- Red test before the fix: `npm test --prefix desktop` failed because the search element was replaced.
- Focused test after the fix: `npm test --prefix desktop` passed.
- Frontend syntax: `node --check desktop/frontend/app.js` passed.
- Desktop JSON: package, Tauri config, and capabilities JSON parsed successfully.
- Native Rust: `cargo fmt -- --check`, `cargo test`, and strict `cargo clippy --all-targets -- -D warnings` passed. Native unit tests: 7 passed.
- Go regression: `go test -count=1 ./...`, `go vet ./...`, and `go build ./...` passed.
- Native Tauri bundling: produced both `Free-Model-Router_0.7.1_x64-setup.exe` and `Free-Model-Router_0.7.1_x64_en-US.msi` locally.
- Portable integration: the Windows package script, `fmr-package -validate`, manifest checks, ZIP extraction, and presence of the native desktop executable all passed with `NATIVE_PACKAGE_OK`.
- Independent review: the earlier OCR pass reported one medium frontend usability finding, which was reproduced and fixed. Two corrected-diff OCR retries failed before review completion because the local review provider returned HTTP 404/429 for all selected items. `ocr delegate preview` and `ocr delegate rule .` were collected, and the corrected diff was manually reviewed against the repository correctness, security, performance, maintainability, and test-coverage rules with no additional actionable findings observed.

## Acceptance boundary

- Local native executable, Tauri configuration, NSIS/MSI bundle generation, portable package integration, frontend behavior, and repository regression checks are validated.
- Clean Windows VM install, first run, autostart persistence, upgrade, uninstall, installer signing, and multi-process race acceptance still require a separate clean VM/signing environment. They are not claimed as passed here.
- `.codegraph/` and its generated database/log files were not staged or modified by this work.
