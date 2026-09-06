#![cfg_attr(not(debug_assertions), windows_subsystem = "windows")]

use rand::RngCore;
use serde::{Deserialize, Serialize};
use serde_json::Value;
use sha2::{Digest, Sha256};
use std::env;
use std::fs::{self, File};
use std::io::{Read, Write};
use std::net::{TcpStream, ToSocketAddrs};
use std::path::{Path, PathBuf};
use std::process::{Child, Command, Stdio};
use std::sync::Mutex;
use std::thread;
use std::time::{Duration, Instant};
use tauri::menu::{MenuBuilder, MenuItemBuilder};
use tauri::tray::TrayIconBuilder;
use tauri::{AppHandle, Manager, State, WebviewUrl, WebviewWindowBuilder, WindowEvent};

const API_VERSION: &str = "1";
const DEFAULT_GATEWAY_ADDRESS: &str = "127.0.0.1:8788";
const DEFAULT_SIDECAR_NAME: &str = "Free-Model-Router.exe";
const DEFAULT_METADATA_SUFFIX: &str = ".metadata.json";

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize)]
#[serde(rename_all = "kebab-case")]
enum Ownership {
    DesktopManaged,
    External,
}

#[derive(Debug, Clone, Serialize, Deserialize, Default)]
#[serde(rename_all = "camelCase")]
struct GatewayStatus {
    #[serde(default)]
    api_version: String,
    #[serde(default)]
    build_version: String,
    #[serde(default)]
    instance_id: String,
    #[serde(default)]
    features: Vec<String>,
    #[serde(default)]
    catalog_revision: i64,
    #[serde(default)]
    pool_revision: i64,
    #[serde(default)]
    pinned_model: Option<String>,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
struct ShellStatus {
    api_version: String,
    build_version: String,
    instance_id: String,
    ownership: Ownership,
    address: String,
    ready: bool,
    error: Option<String>,
}

#[derive(Debug, Deserialize)]
#[serde(rename_all = "camelCase")]
struct SidecarMetadata {
    schema_version: u32,
    product: String,
    artifact: String,
    target_triple: String,
    binary_version: String,
    api_version: String,
    api_major: String,
    sha256: String,
    ownership: String,
}

struct RuntimeState {
    address: String,
    token: String,
    ownership: Ownership,
    child: Option<Child>,
    status: Option<GatewayStatus>,
    error: Option<String>,
}

impl Default for RuntimeState {
    fn default() -> Self {
        Self {
            address: DEFAULT_GATEWAY_ADDRESS.to_string(),
            token: String::new(),
            ownership: Ownership::External,
            child: None,
            status: None,
            error: None,
        }
    }
}

struct AppState {
    runtime: Mutex<RuntimeState>,
}

impl Default for AppState {
    fn default() -> Self {
        Self {
            runtime: Mutex::new(RuntimeState::default()),
        }
    }
}

fn api_major_compatible(expected: &str, actual: &str) -> bool {
    let expected = expected.trim().split('.').next().unwrap_or("");
    let actual = actual.trim().split('.').next().unwrap_or("");
    !expected.is_empty() && expected == actual
}

fn safe_filename_component(value: &str) -> bool {
    let value = value.trim();
    !value.is_empty()
        && value != "."
        && value != ".."
        && !value.chars().any(|character| {
            character == '\0'
                || character == '/'
                || character == '\\'
                || character == ':'
                || character.is_control()
        })
}

fn ownership_after_probe(external_found: bool, compatible: bool) -> Ownership {
    if external_found && compatible {
        Ownership::External
    } else {
        Ownership::DesktopManaged
    }
}

fn should_stop(ownership: Ownership) -> bool {
    ownership == Ownership::DesktopManaged
}

fn is_auth_failure(error: &str) -> bool {
    error.contains("HTTP 401") || error.contains("HTTP 403")
}

fn shell_status(runtime: &RuntimeState) -> ShellStatus {
    let status = runtime.status.clone().unwrap_or_default();
    ShellStatus {
        api_version: status.api_version,
        build_version: status.build_version,
        instance_id: status.instance_id,
        ownership: runtime.ownership,
        address: runtime.address.clone(),
        ready: runtime.status.is_some() && runtime.error.is_none(),
        error: runtime.error.clone(),
    }
}

fn api_major(value: &str) -> &str {
    value.trim().split('.').next().unwrap_or("")
}

fn random_token() -> String {
    let mut bytes = [0u8; 32];
    rand::rng().fill_bytes(&mut bytes);
    bytes.iter().map(|byte| format!("{byte:02x}")).collect()
}

fn normalize_address(address: &str) -> Result<String, String> {
    let mut address = address.trim().to_string();
    if let Some(stripped) = address.strip_prefix("http://") {
        address = stripped.to_string();
    }
    if address.starts_with("https://")
        || address.is_empty()
        || address.contains('/')
        || address.contains('\\')
        || address.contains('@')
    {
        return Err("gateway address must be a local host:port address".to_string());
    }
    let host = address.rsplit_once(':').map(|(host, _)| host).unwrap_or("");
    let loopback = host == "127.0.0.1" || host == "localhost" || host == "[::1]";
    if !loopback {
        return Err("desktop control traffic must remain on loopback".to_string());
    }
    Ok(address)
}

fn http_json(
    address: &str,
    token: &str,
    method: &str,
    path: &str,
    body: Option<&Value>,
) -> Result<Value, String> {
    let address = normalize_address(address)?;
    let endpoint = address
        .to_socket_addrs()
        .map_err(|error| format!("resolve gateway address: {error}"))?
        .next()
        .ok_or_else(|| "gateway address has no socket endpoint".to_string())?;
    let mut stream = TcpStream::connect_timeout(&endpoint, Duration::from_secs(2))
        .map_err(|error| format!("connect to gateway: {error}"))?;
    stream
        .set_read_timeout(Some(Duration::from_secs(5)))
        .map_err(|error| format!("configure gateway read timeout: {error}"))?;
    stream
        .set_write_timeout(Some(Duration::from_secs(5)))
        .map_err(|error| format!("configure gateway write timeout: {error}"))?;

    let body_bytes = body
        .map(|value| {
            serde_json::to_vec(value).map_err(|error| format!("encode gateway request: {error}"))
        })
        .transpose()?;
    let body_bytes = body_bytes.unwrap_or_default();
    let mut request =
        format!("{method} {path} HTTP/1.1\r\nHost: {address}\r\nConnection: close\r\n");
    if !token.trim().is_empty() {
        request.push_str(&format!("Authorization: Bearer {}\r\n", token.trim()));
    }
    if !body_bytes.is_empty() {
        request.push_str("Content-Type: application/json\r\n");
        request.push_str(&format!("Content-Length: {}\r\n", body_bytes.len()));
    }
    request.push_str("\r\n");
    stream
        .write_all(request.as_bytes())
        .and_then(|_| stream.write_all(&body_bytes))
        .map_err(|error| format!("send gateway request: {error}"))?;

    let mut response = Vec::new();
    stream
        .read_to_end(&mut response)
        .map_err(|error| format!("read gateway response: {error}"))?;
    let separator = response
        .windows(4)
        .position(|window| window == b"\r\n\r\n")
        .ok_or_else(|| "gateway response did not contain headers".to_string())?;
    let headers = String::from_utf8_lossy(&response[..separator]);
    let status = headers
        .lines()
        .next()
        .and_then(|line| line.split_whitespace().nth(1))
        .and_then(|value| value.parse::<u16>().ok())
        .ok_or_else(|| "gateway response had no HTTP status".to_string())?;
    let body = &response[separator + 4..];
    if !(200..300).contains(&status) {
        return Err(format!("gateway returned HTTP {status}"));
    }
    serde_json::from_slice(body).map_err(|error| format!("decode gateway response: {error}"))
}

fn sidecar_path() -> Result<PathBuf, String> {
    if let Ok(path) = env::var("FMR_SIDECAR_PATH") {
        let path = PathBuf::from(path);
        if path.as_os_str().is_empty() {
            return Err("FMR_SIDECAR_PATH is empty".to_string());
        }
        return Ok(path);
    }
    let executable =
        env::current_exe().map_err(|error| format!("resolve desktop executable: {error}"))?;
    let parent = executable
        .parent()
        .ok_or_else(|| "desktop executable has no parent directory".to_string())?;
    Ok(parent.join(DEFAULT_SIDECAR_NAME))
}

fn hex_digest(bytes: &[u8]) -> String {
    bytes.iter().map(|byte| format!("{byte:02x}")).collect()
}

fn verify_sidecar(path: &Path) -> Result<(), String> {
    let artifact = path
        .file_name()
        .and_then(|name| name.to_str())
        .ok_or_else(|| "sidecar path has no valid filename".to_string())?;
    if !safe_filename_component(artifact) || !artifact.ends_with(".exe") {
        return Err("sidecar filename is unsafe".to_string());
    }
    let metadata_path = PathBuf::from(format!("{}{}", path.display(), DEFAULT_METADATA_SUFFIX));
    let metadata_data =
        fs::read(&metadata_path).map_err(|error| format!("read sidecar metadata: {error}"))?;
    let metadata: SidecarMetadata = serde_json::from_slice(&metadata_data)
        .map_err(|error| format!("decode sidecar metadata: {error}"))?;
    if metadata.schema_version != 1
        || metadata.product != "Free-Model-Router"
        || metadata.artifact != artifact
        || metadata.binary_version.trim().is_empty()
        || metadata.target_triple.trim().is_empty()
        || metadata.api_version.trim().is_empty()
        || api_major(&metadata.api_version) != metadata.api_major
        || (metadata.ownership != "desktop-managed" && metadata.ownership != "external")
        || metadata.sha256.len() != 64
        || metadata.sha256 != metadata.sha256.to_ascii_lowercase()
        || !metadata
            .sha256
            .chars()
            .all(|character| character.is_ascii_hexdigit())
    {
        return Err("sidecar metadata identity is invalid".to_string());
    }
    let binary = fs::read(path).map_err(|error| format!("read sidecar binary: {error}"))?;
    let digest = hex_digest(Sha256::digest(&binary).as_slice());
    if digest != metadata.sha256.to_ascii_lowercase() {
        return Err("sidecar checksum does not match metadata".to_string());
    }
    Ok(())
}

fn snapshot(state: &AppState) -> Result<ShellStatus, String> {
    let runtime = state
        .runtime
        .lock()
        .map_err(|_| "desktop runtime lock is poisoned".to_string())?;
    Ok(shell_status(&runtime))
}

fn attach_or_start_internal(
    state: &AppState,
    requested_address: Option<String>,
) -> Result<ShellStatus, String> {
    let address_input = requested_address
        .or_else(|| env::var("FMR_GATEWAY_ADDRESS").ok())
        .unwrap_or_else(|| DEFAULT_GATEWAY_ADDRESS.to_string());
    let address = normalize_address(&address_input)?;
    let token = env::var("FMR_MANAGEMENT_TOKEN").unwrap_or_else(|_| random_token());

    {
        let runtime = state
            .runtime
            .lock()
            .map_err(|_| "desktop runtime lock is poisoned".to_string())?;
        if runtime.child.is_some() && runtime.address == address && runtime.status.is_some() {
            return Ok(shell_status(&runtime));
        }
    }

    let probe = match http_json(&address, &token, "GET", "/_fmr/status", None) {
        Ok(value) => Some(value),
        Err(error) if is_auth_failure(&error) => {
            return Err("gateway control authentication failed".to_string());
        }
        Err(_) => None,
    };
    if let Some(value) = probe {
        let status: GatewayStatus = serde_json::from_value(value)
            .map_err(|error| format!("decode gateway status: {error}"))?;
        if !api_major_compatible(API_VERSION, &status.api_version) {
            return Err(format!(
                "gateway API major mismatch: desktop={} gateway={}",
                api_major(API_VERSION),
                api_major(&status.api_version)
            ));
        }
        let mut runtime = state
            .runtime
            .lock()
            .map_err(|_| "desktop runtime lock is poisoned".to_string())?;
        runtime.address = address;
        runtime.token = token;
        runtime.ownership = ownership_after_probe(true, true);
        runtime.child = None;
        runtime.status = Some(status);
        runtime.error = None;
        return Ok(shell_status(&runtime));
    }

    let sidecar = sidecar_path()?;
    verify_sidecar(&sidecar)?;
    let mut command = Command::new(&sidecar);
    command
        .env("FMR_MANAGEMENT_TOKEN", &token)
        .stdout(Stdio::null())
        .stderr(Stdio::null());
    if let Ok(config_path) = env::var("FMR_CONFIG_PATH") {
        if !config_path.trim().is_empty() {
            command.args(["-config", config_path.trim()]);
        }
    }
    let mut child = command
        .spawn()
        .map_err(|error| format!("start managed gateway: {error}"))?;
    let deadline = Instant::now() + Duration::from_secs(20);
    let status = loop {
        if Instant::now() >= deadline {
            let _ = child.kill();
            let _ = child.wait();
            return Err("managed gateway did not become ready within 20 seconds".to_string());
        }
        match http_json(&address, &token, "GET", "/_fmr/status", None) {
            Ok(value) => {
                let status: GatewayStatus = serde_json::from_value(value)
                    .map_err(|error| format!("decode managed gateway status: {error}"))?;
                break status;
            }
            Err(_) => {
                if let Ok(Some(exit)) = child.try_wait() {
                    return Err(format!(
                        "managed gateway exited before becoming ready: {exit}"
                    ));
                }
                thread::sleep(Duration::from_millis(150));
            }
        }
    };
    if !api_major_compatible(API_VERSION, &status.api_version) {
        let _ = child.kill();
        let _ = child.wait();
        return Err(format!(
            "managed gateway API major mismatch: desktop={} gateway={}",
            api_major(API_VERSION),
            api_major(&status.api_version)
        ));
    }
    let mut runtime = state
        .runtime
        .lock()
        .map_err(|_| "desktop runtime lock is poisoned".to_string())?;
    runtime.address = address;
    runtime.token = token;
    runtime.ownership = ownership_after_probe(false, true);
    runtime.child = Some(child);
    runtime.status = Some(status);
    runtime.error = None;
    Ok(shell_status(&runtime))
}

fn current_connection(state: &AppState) -> Result<(String, String), String> {
    let runtime = state
        .runtime
        .lock()
        .map_err(|_| "desktop runtime lock is poisoned".to_string())?;
    if runtime.status.is_none() {
        return Err(runtime
            .error
            .clone()
            .unwrap_or_else(|| "gateway is not connected".to_string()));
    }
    Ok((runtime.address.clone(), runtime.token.clone()))
}

fn stop_managed(state: &AppState) -> Result<(), String> {
    let mut runtime = state
        .runtime
        .lock()
        .map_err(|_| "desktop runtime lock is poisoned".to_string())?;
    if should_stop(runtime.ownership) {
        if let Some(mut child) = runtime.child.take() {
            let _ = child.kill();
            let _ = child.wait();
        }
    } else {
        runtime.child = None;
    }
    runtime.status = None;
    runtime.error = None;
    Ok(())
}

fn open_auxiliary_window(
    app: &AppHandle,
    label: &str,
    title: &str,
    view: &str,
) -> Result<(), String> {
    if let Some(window) = app.get_webview_window(label) {
        window.show().map_err(|error| error.to_string())?;
        window.set_focus().map_err(|error| error.to_string())?;
        return Ok(());
    }
    WebviewWindowBuilder::new(
        app,
        label,
        WebviewUrl::App(format!("index.html?view={view}").into()),
    )
    .title(title)
    .inner_size(980.0, 680.0)
    .min_inner_size(640.0, 420.0)
    .build()
    .map(|_| ())
    .map_err(|error| error.to_string())
}

fn autostart_path() -> Result<PathBuf, String> {
    let app_data = env::var("APPDATA").map_err(|_| "APPDATA is not set".to_string())?;
    Ok(PathBuf::from(app_data)
        .join("Microsoft")
        .join("Windows")
        .join("Start Menu")
        .join("Programs")
        .join("Startup")
        .join("Free-Model-Router.cmd"))
}

fn write_autostart_file(path: &Path, command: &str) -> Result<(), String> {
    let parent = path
        .parent()
        .ok_or_else(|| "autostart path has no parent".to_string())?;
    fs::create_dir_all(parent).map_err(|error| format!("create startup directory: {error}"))?;
    let temp = parent.join(format!(".fmr-autostart-{}.tmp", std::process::id()));
    let mut file =
        File::create(&temp).map_err(|error| format!("create autostart temp file: {error}"))?;
    write!(file, "@echo off\r\nstart \"\" \"{}\"\r\n", command)
        .map_err(|error| format!("write autostart file: {error}"))?;
    file.sync_all()
        .map_err(|error| format!("sync autostart file: {error}"))?;
    drop(file);
    if path.exists() {
        fs::remove_file(path).map_err(|error| format!("replace autostart file: {error}"))?;
    }
    fs::rename(&temp, path).map_err(|error| format!("install autostart file: {error}"))
}

#[tauri::command]
fn desktop_status(state: State<'_, AppState>) -> Result<ShellStatus, String> {
    snapshot(&state)
}

#[tauri::command]
fn attach_or_start(
    state: State<'_, AppState>,
    address: Option<String>,
) -> Result<ShellStatus, String> {
    attach_or_start_internal(&state, address)
}

#[tauri::command]
fn gateway_providers(state: State<'_, AppState>) -> Result<Value, String> {
    let (address, token) = current_connection(&state)?;
    http_json(&address, &token, "GET", "/_fmr/providers", None)
}

#[tauri::command]
fn gateway_models(state: State<'_, AppState>) -> Result<Value, String> {
    let (address, token) = current_connection(&state)?;
    http_json(&address, &token, "GET", "/_fmr/models", None)
}

#[tauri::command]
fn gateway_pool(state: State<'_, AppState>) -> Result<Value, String> {
    let (address, token) = current_connection(&state)?;
    http_json(&address, &token, "GET", "/_fmr/model-pool", None)
}

#[tauri::command]
fn patch_model_pool(state: State<'_, AppState>, request: Value) -> Result<Value, String> {
    let (address, token) = current_connection(&state)?;
    http_json(
        &address,
        &token,
        "PATCH",
        "/_fmr/model-pool",
        Some(&request),
    )
}

#[tauri::command]
fn gateway_logs(state: State<'_, AppState>) -> Result<Value, String> {
    let (address, token) = current_connection(&state)?;
    http_json(&address, &token, "GET", "/_fmr/logs?limit=100", None)
}

#[tauri::command]
fn open_model_manager(app: AppHandle) -> Result<(), String> {
    open_auxiliary_window(&app, "model-manager", "Model Manager", "models")
}

#[tauri::command]
fn open_usage_log(app: AppHandle) -> Result<(), String> {
    open_auxiliary_window(&app, "usage-log", "Usage Log", "logs")
}

#[tauri::command]
fn set_autostart(enabled: bool) -> Result<bool, String> {
    let path = autostart_path()?;
    if enabled {
        let executable =
            env::current_exe().map_err(|error| format!("resolve desktop executable: {error}"))?;
        write_autostart_file(&path, &executable.display().to_string())?;
    } else if path.exists() {
        fs::remove_file(&path).map_err(|error| format!("remove autostart file: {error}"))?;
    }
    Ok(enabled)
}

#[tauri::command]
fn shutdown(app: AppHandle, state: State<'_, AppState>) -> Result<(), String> {
    stop_managed(&state)?;
    app.exit(0);
    Ok(())
}

fn setup_tray(app: &AppHandle) -> Result<(), Box<dyn std::error::Error>> {
    let show = MenuItemBuilder::with_id("show", "Show").build(app)?;
    let models = MenuItemBuilder::with_id("models", "Model Manager").build(app)?;
    let logs = MenuItemBuilder::with_id("logs", "Usage Log").build(app)?;
    let quit = MenuItemBuilder::with_id("quit", "Quit").build(app)?;
    let menu = MenuBuilder::new(app)
        .items(&[&show, &models, &logs, &quit])
        .build()?;
    let mut tray = TrayIconBuilder::new().menu(&menu);
    if let Some(icon) = app.default_window_icon() {
        tray = tray.icon(icon.clone());
    }
    tray.on_menu_event(|app, event| match event.id.as_ref() {
        "show" => {
            if let Some(window) = app.get_webview_window("main") {
                let _ = window.show();
                let _ = window.set_focus();
            }
        }
        "models" => {
            let _ = open_auxiliary_window(app, "model-manager", "Model Manager", "models");
        }
        "logs" => {
            let _ = open_auxiliary_window(app, "usage-log", "Usage Log", "logs");
        }
        "quit" => {
            let state = app.state::<AppState>();
            let _ = stop_managed(&state);
            app.exit(0);
        }
        _ => {}
    })
    .build(app)?;
    Ok(())
}

fn main() {
    tauri::Builder::default()
        .plugin(tauri_plugin_single_instance::init(|app, _args, _cwd| {
            if let Some(window) = app.get_webview_window("main") {
                let _ = window.show();
                let _ = window.set_focus();
            }
        }))
        .manage(AppState::default())
        .setup(|app| {
            setup_tray(app.handle())?;
            let state = app.state::<AppState>();
            if let Err(error) = attach_or_start_internal(&state, None) {
                if let Ok(mut runtime) = state.runtime.lock() {
                    runtime.error = Some(error);
                }
            }
            Ok(())
        })
        .invoke_handler(tauri::generate_handler![
            desktop_status,
            attach_or_start,
            gateway_providers,
            gateway_models,
            gateway_pool,
            patch_model_pool,
            gateway_logs,
            open_model_manager,
            open_usage_log,
            set_autostart,
            shutdown
        ])
        .on_window_event(|window, event| {
            if window.label() == "main" {
                if let WindowEvent::CloseRequested { api, .. } = event {
                    api.prevent_close();
                    let _ = window.hide();
                }
            }
        })
        .run(tauri::generate_context!())
        .expect("error while running Free-Model-Router desktop");
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn compatible_api_majors_attach() {
        assert!(api_major_compatible("1", "1.4"));
        assert!(api_major_compatible("1.4", "1"));
        assert!(!api_major_compatible("1", "2"));
        assert!(!api_major_compatible("", "1"));
    }

    #[test]
    fn sidecar_filename_is_safe() {
        assert!(safe_filename_component("Free-Model-Router.exe"));
        assert!(!safe_filename_component("..\\Free-Model-Router.exe"));
        assert!(!safe_filename_component("Free-Model-Router/evil.exe"));
        assert!(!safe_filename_component(""));
    }

    #[test]
    fn ownership_transition_never_stops_external_gateway() {
        assert_eq!(ownership_after_probe(true, true), Ownership::External);
        assert_eq!(
            ownership_after_probe(false, true),
            Ownership::DesktopManaged
        );
        assert!(should_stop(Ownership::DesktopManaged));
        assert!(!should_stop(Ownership::External));
    }

    #[test]
    fn ownership_json_uses_public_kebab_case() {
        assert_eq!(
            serde_json::to_string(&Ownership::DesktopManaged).unwrap(),
            "\"desktop-managed\""
        );
        assert_eq!(
            serde_json::to_string(&Ownership::External).unwrap(),
            "\"external\""
        );
    }

    #[test]
    fn authentication_failures_do_not_start_a_second_gateway() {
        assert!(is_auth_failure("gateway returned HTTP 401"));
        assert!(is_auth_failure("gateway returned HTTP 403"));
        assert!(!is_auth_failure("connect to gateway: refused"));
    }

    #[test]
    fn control_address_is_loopback_only() {
        assert!(normalize_address("127.0.0.1:8788").is_ok());
        assert!(normalize_address("http://localhost:8788").is_ok());
        assert!(normalize_address("0.0.0.0:8788").is_err());
        assert!(normalize_address("https://127.0.0.1:8788").is_err());
    }

    #[test]
    fn autostart_file_is_written_atomically_with_expected_command() {
        let path = std::env::temp_dir().join(format!("fmr-autostart-{}.cmd", random_token()));
        write_autostart_file(&path, r"C:\FMR\free-model-router-desktop.exe").unwrap();
        let content = std::fs::read_to_string(&path).unwrap();
        assert!(content.contains("@echo off"));
        assert!(content.contains("free-model-router-desktop.exe"));
        std::fs::remove_file(path).unwrap();
    }
}
