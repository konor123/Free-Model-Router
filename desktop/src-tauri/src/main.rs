#![cfg_attr(not(debug_assertions), windows_subsystem = "windows")]

use rand::RngCore;
use serde::{Deserialize, Serialize};
use serde_json::Value;
use sha2::{Digest, Sha256};
use std::env;
use std::fs::{self};
use std::io::{BufRead, BufReader, Read, Write};
use std::net::{TcpStream, ToSocketAddrs};
use std::path::{Path, PathBuf};
use std::process::{Child, Command, Stdio};
use std::sync::{
    atomic::{AtomicBool, Ordering},
    Mutex,
};
use std::thread;
use std::time::{Duration, Instant};
use tauri::menu::{CheckMenuItemBuilder, MenuBuilder, MenuItemBuilder};
use tauri::tray::{MouseButton, MouseButtonState, TrayIconBuilder, TrayIconEvent};
use tauri::{AppHandle, Emitter, Manager, State, WindowEvent};
#[cfg(windows)]
use winreg::enums::{HKEY_CURRENT_USER, KEY_READ, KEY_WRITE};
#[cfg(windows)]
use winreg::RegKey;

const API_VERSION: &str = "1";
const DEFAULT_GATEWAY_ADDRESS: &str = "127.0.0.1:8788";
const DEFAULT_SIDECAR_NAME: &str = "Free-Model-Router.exe";
const DEFAULT_METADATA_SUFFIX: &str = ".metadata.json";
const AUTOSTART_VALUE_NAME: &str = "Free-Model-Router";
#[cfg(windows)]
const AUTOSTART_REGISTRY_PATH: &str = "Software\\Microsoft\\Windows\\CurrentVersion\\Run";

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
    usage_streaming: AtomicBool,
}

impl Default for AppState {
    fn default() -> Self {
        Self {
            runtime: Mutex::new(RuntimeState::default()),
            usage_streaming: AtomicBool::new(false),
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
    if !valid_control_token(token) {
        return Err("gateway control token contains invalid control characters".to_string());
    }
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
    let body = decode_http_body(&headers, &response[separator + 4..])?;
    if !(200..300).contains(&status) {
        return Err(format!("gateway returned HTTP {status}"));
    }
    serde_json::from_slice(&body).map_err(|error| format!("decode gateway response: {error}"))
}

fn decode_http_body(headers: &str, body: &[u8]) -> Result<Vec<u8>, String> {
    let transfer_encodings: Vec<&str> = headers
        .lines()
        .filter_map(|line| line.split_once(':'))
        .filter(|(name, _)| name.trim().eq_ignore_ascii_case("transfer-encoding"))
        .flat_map(|(_, value)| value.split(','))
        .map(str::trim)
        .filter(|value| !value.is_empty())
        .collect();
    if transfer_encodings.is_empty() {
        return Ok(body.to_vec());
    }
    if transfer_encodings.len() != 1 || !transfer_encodings[0].eq_ignore_ascii_case("chunked") {
        return Err("gateway response uses unsupported transfer encoding".to_string());
    }
    decode_chunked_body(body)
}

fn decode_chunked_body(mut body: &[u8]) -> Result<Vec<u8>, String> {
    let mut decoded = Vec::new();
    loop {
        let line_end = body
            .windows(2)
            .position(|window| window == b"\r\n")
            .ok_or_else(|| "gateway chunk size is truncated".to_string())?;
        let size_bytes = body[..line_end]
            .split(|byte| *byte == b';')
            .next()
            .unwrap_or_default();
        if size_bytes.is_empty() || !size_bytes.iter().all(u8::is_ascii_hexdigit) {
            return Err("gateway chunk size is invalid".to_string());
        }
        let size_text = std::str::from_utf8(size_bytes)
            .map_err(|_| "gateway chunk size is invalid".to_string())?;
        let size = usize::from_str_radix(size_text, 16)
            .map_err(|_| "gateway chunk size is invalid".to_string())?;
        body = &body[line_end + 2..];
        if size == 0 {
            loop {
                let trailer_end = body
                    .windows(2)
                    .position(|window| window == b"\r\n")
                    .ok_or_else(|| "gateway chunk trailers are truncated".to_string())?;
                if trailer_end == 0 {
                    return Ok(decoded);
                }
                body = &body[trailer_end + 2..];
            }
        }
        let framed_length = size
            .checked_add(2)
            .ok_or_else(|| "gateway chunk size is invalid".to_string())?;
        if body.len() < framed_length || &body[size..framed_length] != b"\r\n" {
            return Err("gateway chunk data is truncated or malformed".to_string());
        }
        decoded.extend_from_slice(&body[..size]);
        body = &body[framed_length..];
    }
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
    configure_managed_sidecar_command(&mut command);
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
    if !valid_control_token(&runtime.token) {
        return Err("gateway control token contains invalid control characters".to_string());
    }
    Ok((runtime.address.clone(), runtime.token.clone()))
}

fn valid_control_token(token: &str) -> bool {
    !token.chars().any(|character| character.is_control())
}

fn control_request(
    state: &AppState,
    method: &str,
    path: &str,
    body: Option<&Value>,
) -> Result<Value, String> {
    let (address, token) = current_connection(state)?;
    http_json(&address, &token, method, path, body)
}

fn sse_payload(line: &str) -> Option<Value> {
    line.strip_prefix("data: ")
        .and_then(|payload| serde_json::from_str(payload.trim()).ok())
}

fn stream_usage_events(app: &AppHandle, address: &str, token: &str) -> Result<(), String> {
    let address = normalize_address(address)?;
    let endpoint = address
        .to_socket_addrs()
        .map_err(|error| format!("resolve usage event stream: {error}"))?
        .next()
        .ok_or_else(|| "usage event stream has no socket endpoint".to_string())?;
    let mut stream = TcpStream::connect_timeout(&endpoint, Duration::from_secs(2))
        .map_err(|error| format!("connect usage event stream: {error}"))?;
    stream
        .set_read_timeout(Some(Duration::from_secs(1)))
        .map_err(|error| format!("configure usage event stream read timeout: {error}"))?;
    stream
        .set_write_timeout(Some(Duration::from_secs(5)))
        .map_err(|error| format!("configure usage event stream: {error}"))?;
    let request = format!(
        "GET /_fmr/logs/events HTTP/1.0\r\nHost: {address}\r\nAuthorization: Bearer {}\r\nAccept: text/event-stream\r\n\r\n",
        token.trim()
    );
    stream
        .write_all(request.as_bytes())
        .map_err(|error| format!("send usage event stream request: {error}"))?;

    let mut reader = BufReader::new(stream);
    let mut status = String::new();
    reader
        .read_line(&mut status)
        .map_err(|error| format!("read usage event stream status: {error}"))?;
    if !status.contains(" 200 ") {
        return Err(format!("usage event stream returned {}", status.trim()));
    }
    loop {
        let mut line = String::new();
        if reader
            .read_line(&mut line)
            .map_err(|error| format!("read usage event stream headers: {error}"))?
            == 0
        {
            return Err("usage event stream ended before headers".to_string());
        }
        if line == "\r\n" {
            break;
        }
    }
    loop {
        let mut line = String::new();
        if reader
            .read_line(&mut line)
            .map_err(|error| format!("read usage event: {error}"))?
            == 0
        {
            return Ok(());
        }
        if let Some(payload) = sse_payload(&line) {
            let successful_model = payload.get("record").and_then(|record| {
                if record.get("result").and_then(Value::as_str) == Some("success") {
                    record.get("finalModel").and_then(Value::as_str)
                } else {
                    None
                }
            });
            if let Some(model) = successful_model {
                if let Some(tray) = app.tray_by_id("main-tray") {
                    let _ = tray.set_tooltip(Some(format!("Free-Model-Router · {model}")));
                }
            }
            app.emit("usage-event", payload)
                .map_err(|error| format!("emit usage event: {error}"))?;
        }
    }
}

#[tauri::command]
fn start_usage_stream(app: AppHandle, state: State<'_, AppState>) -> Result<(), String> {
    let (address, token) = current_connection(&state)?;
    if state.usage_streaming.swap(true, Ordering::AcqRel) {
        return Ok(());
    }
    let handle = app.clone();
    thread::spawn(move || {
        while handle
            .state::<AppState>()
            .usage_streaming
            .load(Ordering::Acquire)
        {
            let _ = stream_usage_events(&handle, &address, &token);
            if handle
                .state::<AppState>()
                .usage_streaming
                .load(Ordering::Acquire)
            {
                thread::sleep(Duration::from_secs(1));
            }
        }
    });
    Ok(())
}

fn stop_managed(state: &AppState) -> Result<(), String> {
    state.usage_streaming.store(false, Ordering::Release);
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

fn legacy_autostart_path() -> Result<PathBuf, String> {
    let app_data = env::var("APPDATA").map_err(|_| "APPDATA is not set".to_string())?;
    Ok(PathBuf::from(app_data)
        .join("Microsoft")
        .join("Windows")
        .join("Start Menu")
        .join("Programs")
        .join("Startup")
        .join("Free-Model-Router.cmd"))
}

#[cfg(windows)]
fn autostart_registry() -> Result<RegKey, String> {
    RegKey::predef(HKEY_CURRENT_USER)
        .open_subkey_with_flags(AUTOSTART_REGISTRY_PATH, KEY_READ | KEY_WRITE)
        .map_err(|error| format!("open Windows startup registry: {error}"))
}

#[cfg(windows)]
fn create_autostart_registry() -> Result<RegKey, String> {
    RegKey::predef(HKEY_CURRENT_USER)
        .create_subkey(AUTOSTART_REGISTRY_PATH)
        .map(|(key, _)| key)
        .map_err(|error| format!("create Windows startup registry: {error}"))
}

#[cfg(windows)]
fn autostart_enabled(executable: &Path) -> Result<bool, String> {
    let registry = match autostart_registry() {
        Ok(registry) => registry,
        Err(_) => return Ok(false),
    };
    let value: String = match registry.get_value(AUTOSTART_VALUE_NAME) {
        Ok(value) => value,
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => return Ok(false),
        Err(error) => return Err(format!("read Windows startup registry: {error}")),
    };
    Ok(value == autostart_value(executable))
}

#[cfg(windows)]
fn set_autostart_registry(executable: &Path, enabled: bool) -> Result<(), String> {
    if enabled {
        let registry = create_autostart_registry()?;
        registry
            .set_value(AUTOSTART_VALUE_NAME, &autostart_value(executable))
            .map_err(|error| format!("write Windows startup registry: {error}"))?;
    } else if let Err(error) = autostart_registry()?.delete_value(AUTOSTART_VALUE_NAME) {
        if error.kind() != std::io::ErrorKind::NotFound {
            return Err(format!("remove Windows startup registry: {error}"));
        }
    }
    Ok(())
}

fn autostart_value(executable: &Path) -> String {
    format!("\"{}\"", executable.display())
}

#[cfg(not(windows))]
fn autostart_enabled(_: &Path) -> Result<bool, String> {
    Ok(false)
}

#[cfg(not(windows))]
fn set_autostart_registry(_: &Path, _: bool) -> Result<(), String> {
    Err("Start with Windows is only available on Windows".to_string())
}

fn configure_managed_sidecar_command(command: &mut Command) {
    #[cfg(windows)]
    {
        use std::os::windows::process::CommandExt;
        command.creation_flags(0x08000000); // CREATE_NO_WINDOW
    }
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
    control_request(&state, "GET", "/_fmr/providers", None)
}

#[tauri::command]
fn gateway_models(state: State<'_, AppState>) -> Result<Value, String> {
    control_request(&state, "GET", "/_fmr/models", None)
}

#[tauri::command]
fn refresh_gateway_catalog(state: State<'_, AppState>) -> Result<Value, String> {
    control_request(
        &state,
        "POST",
        "/_fmr/catalog/refresh",
        Some(&serde_json::json!({})),
    )
}

#[tauri::command]
fn gateway_pool(state: State<'_, AppState>) -> Result<Value, String> {
    control_request(&state, "GET", "/_fmr/model-pool", None)
}

#[tauri::command]
fn patch_model_pool(state: State<'_, AppState>, request: Value) -> Result<Value, String> {
    control_request(&state, "PATCH", "/_fmr/model-pool", Some(&request))
}

#[tauri::command]
fn replace_model_pool(state: State<'_, AppState>, request: Value) -> Result<Value, String> {
    control_request(&state, "PUT", "/_fmr/model-pool", Some(&request))
}

#[tauri::command]
fn pin_model(state: State<'_, AppState>, request: Value) -> Result<Value, String> {
    control_request(&state, "POST", "/_fmr/pin", Some(&request))
}

#[tauri::command]
fn auto_select(state: State<'_, AppState>, request: Value) -> Result<Value, String> {
    control_request(&state, "POST", "/_fmr/auto", Some(&request))
}

#[tauri::command]
fn gateway_config(state: State<'_, AppState>) -> Result<Value, String> {
    control_request(&state, "GET", "/_fmr/config", None)
}

#[tauri::command]
fn update_gateway_config(state: State<'_, AppState>, request: Value) -> Result<Value, String> {
    let managed = {
        let runtime = state
            .runtime
            .lock()
            .map_err(|_| "desktop runtime lock is poisoned".to_string())?;
        runtime.ownership == Ownership::DesktopManaged
    };
    let mut response = control_request(&state, "PUT", "/_fmr/config", Some(&request))?;
    if managed {
        stop_managed(&state)?;
        attach_or_start_internal(&state, None)?;
        if let Some(object) = response.as_object_mut() {
            object.insert("restarted".to_string(), Value::Bool(true));
            object.insert("restartRequired".to_string(), Value::Bool(false));
        }
    }
    Ok(response)
}

#[tauri::command]
fn gateway_logs(state: State<'_, AppState>) -> Result<Value, String> {
    control_request(&state, "GET", "/_fmr/logs?limit=100", None)
}

#[tauri::command]
fn set_autostart(enabled: bool) -> Result<bool, String> {
    let executable =
        env::current_exe().map_err(|error| format!("resolve desktop executable: {error}"))?;
    set_autostart_registry(&executable, enabled)?;
    if let Ok(legacy) = legacy_autostart_path() {
        if legacy.exists() {
            let _ = fs::remove_file(&legacy);
        }
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
    let executable =
        env::current_exe().map_err(|error| format!("resolve desktop executable: {error}"))?;
    let autostart = CheckMenuItemBuilder::with_id("autostart", "Start with Windows")
        .checked(autostart_enabled(&executable).unwrap_or(false))
        .build(app)?;
    let quit = MenuItemBuilder::with_id("quit", "Exit").build(app)?;
    let menu = MenuBuilder::new(app).items(&[&autostart, &quit]).build()?;
    let mut tray = TrayIconBuilder::with_id("main-tray")
        .menu(&menu)
        .tooltip("Free-Model-Router");
    if let Some(icon) = app.default_window_icon() {
        tray = tray.icon(icon.clone());
    }
    let autostart_item = autostart.clone();
    tray = tray.on_tray_icon_event(|tray, event| {
        if let TrayIconEvent::Click {
            button: MouseButton::Left,
            button_state: MouseButtonState::Up,
            ..
        } = event
        {
            let app = tray.app_handle();
            if let Some(window) = app.get_webview_window("main") {
                let _ = window.show();
                let _ = window.set_focus();
            }
        }
    });
    tray.on_menu_event(move |app, event| match event.id.as_ref() {
        "autostart" => {
            if let Ok(checked) = autostart_item.is_checked() {
                if set_autostart(checked).is_err() {
                    let _ = autostart_item.set_checked(!checked);
                }
            }
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
            refresh_gateway_catalog,
            gateway_pool,
            patch_model_pool,
            replace_model_pool,
            pin_model,
            auto_select,
            gateway_config,
            update_gateway_config,
            gateway_logs,
            start_usage_stream,
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

    fn read_test_request(stream: &mut TcpStream) -> (String, Vec<u8>) {
        use std::io::{BufRead, BufReader};

        stream
            .set_read_timeout(Some(Duration::from_secs(5)))
            .unwrap();
        let mut reader = BufReader::new(stream);
        let mut headers = String::new();
        let mut content_length = None;
        loop {
            let mut line = String::new();
            assert_ne!(
                reader.read_line(&mut line).unwrap(),
                0,
                "EOF before complete request headers"
            );
            headers.push_str(&line);
            if line == "\r\n" {
                break;
            }
            if let Some((name, value)) = line.split_once(':') {
                if name.eq_ignore_ascii_case("Content-Length") {
                    assert!(content_length.is_none(), "duplicate Content-Length");
                    content_length = Some(value.trim().parse::<usize>().unwrap());
                }
            }
        }
        let mut body = vec![0; content_length.expect("mutation must send Content-Length")];
        reader.read_exact(&mut body).unwrap();
        (headers, body)
    }

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
    fn autostart_registry_value_quotes_executable_path() {
        assert_eq!(
            autostart_value(Path::new(r"C:\FMR App\free-model-router-desktop.exe")),
            r#""C:\FMR App\free-model-router-desktop.exe""#
        );
    }

    #[test]
    fn control_request_forwards_authenticated_mutations() {
        use std::net::TcpListener;

        let listener = TcpListener::bind("127.0.0.1:0").unwrap();
        let address = listener.local_addr().unwrap();
        let worker = thread::spawn(move || {
            let (mut stream, _) = listener.accept().unwrap();
            let (headers, body) = read_test_request(&mut stream);
            assert!(headers.starts_with("POST /_fmr/pin HTTP/1.1"));
            assert!(headers.contains("Authorization: Bearer test-token"));
            let body: Value = serde_json::from_slice(&body).unwrap();
            assert_eq!(body["providerModelId"], "opencode/model");
            stream
                .write_all(b"HTTP/1.1 200 OK\r\nContent-Length: 14\r\nConnection: close\r\n\r\n{\"revision\":2}")
                .unwrap();
        });
        let state = AppState::default();
        {
            let mut runtime = state.runtime.lock().unwrap();
            runtime.address = address.to_string();
            runtime.token = "test-token".to_string();
            runtime.status = Some(GatewayStatus::default());
        }
        let request = serde_json::json!({"revision": 1, "providerModelId": "opencode/model"});
        let response = control_request(&state, "POST", "/_fmr/pin", Some(&request)).unwrap();
        assert_eq!(response["revision"], 2);
        worker.join().unwrap();
    }

    #[test]
    fn sse_payload_parses_only_data_lines() {
        assert_eq!(
            sse_payload("data: {\"type\":\"request_completed\"}\n"),
            Some(serde_json::json!({ "type": "request_completed" }))
        );
        assert_eq!(sse_payload("event: request_completed\n"), None);
    }

    #[test]
    fn control_tokens_reject_header_injection_characters() {
        assert!(valid_control_token("safe-token"));
        assert!(!valid_control_token("bad\r\nInjected: value"));
    }

    #[test]
    fn http_json_rejects_invalid_control_tokens_before_connecting() {
        let error = http_json(
            "127.0.0.1:1",
            "bad\r\nInjected: value",
            "GET",
            "/_fmr/status",
            None,
        )
        .unwrap_err();
        assert_eq!(
            error,
            "gateway control token contains invalid control characters"
        );
    }

    #[test]
    fn chunked_gateway_responses_are_decoded_before_json_parsing() {
        let headers = "HTTP/1.1 200 OK\r\ntRaNsFeR-EnCoDiNg: ChUnKeD\r\n";
        let body = b"4;source=test\r\n{\"a\"\r\n3\r\n:1}\r\n0\r\nX-Test: yes\r\n\r\n";
        assert_eq!(decode_http_body(headers, body).unwrap(), br#"{"a":1}"#);
    }

    #[test]
    fn malformed_or_unsupported_gateway_framing_is_rejected() {
        assert!(decode_chunked_body(b"5\r\nabc").is_err());
        assert!(decode_chunked_body(b"0\r\n").is_err());
        assert!(decode_http_body(
            "HTTP/1.1 200 OK\r\nTransfer-Encoding: gzip, chunked\r\n",
            b"0\r\n\r\n"
        )
        .is_err());
    }
}
