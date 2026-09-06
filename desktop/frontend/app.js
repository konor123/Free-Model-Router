const tauriInvoke = window.__TAURI__?.core?.invoke;
const app = document.getElementById("app");
const view = new URLSearchParams(window.location.search).get("view") || "main";
const state = { status: null, providers: [], models: [], pool: null, logs: [], error: "", searchQuery: "" };

function escapeHtml(value) {
  return String(value ?? "").replace(/[&<>\"']/g, (character) => ({
    "&": "&amp;", "<": "&lt;", ">": "&gt;", "\"": "&quot;", "'": "&#39;"
  })[character]);
}

function invoke(command, args) {
  if (!tauriInvoke) return Promise.reject(new Error("This page must run inside the Tauri desktop shell."));
  return tauriInvoke(command, args);
}

function setError(error) {
  state.error = error?.message || String(error || "");
  render();
}

function statusCard() {
  const status = state.status || {};
  const ready = status.ready;
  return `<section class="card">
    <div class="grid">
      <div class="stat"><small>Connection</small><strong class="${ready ? "status-ready" : "status-warn"}">${ready ? "Ready" : "Not connected"}</strong></div>
      <div class="stat"><small>Ownership</small><strong>${escapeHtml(status.ownership || "unknown")}</strong></div>
      <div class="stat"><small>API / Build</small><strong>${escapeHtml(status.apiVersion || "-")} / ${escapeHtml(status.buildVersion || "-")}</strong></div>
      <div class="stat"><small>Instance</small><code>${escapeHtml(status.instanceId || "-")}</code></div>
    </div>
    ${status.error ? `<p class="error">${escapeHtml(status.error)}</p>` : ""}
  </section>`;
}

function providerCards() {
  if (!state.providers.length) return `<div class="empty">No provider data available.</div>`;
  return state.providers.map((provider) => `<div class="stat">
    <strong>${escapeHtml(provider.id)}</strong>
    <div class="muted">${provider.models} models, ${provider.routes} routes</div>
    <span class="badge ${provider.enabled ? "good" : "warn"}">${provider.enabled ? "enabled" : "disabled"}</span>
  </div>`).join("");
}

function mainView() {
  return `<div class="toolbar"><div><h1>Free-Model-Router</h1><p>Local gateway control and model routing.</p></div><span class="badge">Tauri desktop</span></div>
    ${statusCard()}
    <div class="actions">
      <button data-action="attach">Attach / Start Gateway</button>
      <button class="secondary" data-action="models">Model Manager</button>
      <button class="secondary" data-action="logs">Usage Log</button>
      <button class="danger" data-action="quit">Quit</button>
      <label class="checkbox"><input id="autostart" type="checkbox" /> Start with Windows</label>
    </div>
    <section class="card"><h2>Providers</h2><div class="grid">${providerCards()}</div></section>`;
}

function modelRows() {
  const query = state.searchQuery.toLowerCase();
  const models = state.models.filter((model) => !query || [model.id, model.displayName, model.upstreamId].join(" ").toLowerCase().includes(query));
  return models.map((model) => {
    const routes = (model.routes || []).map((route) => `<span class="badge">${escapeHtml(route.provider)}${route.ttftKnown ? ` ${Math.round(route.ttftMs)}ms` : ""}</span>`).join("");
    return `<tr><td><label class="checkbox"><input type="checkbox" data-model-id="${escapeHtml(model.id)}" ${model.selected ? "checked" : ""} /> <code>${escapeHtml(model.id)}</code></label></td><td>${escapeHtml(model.displayName)}</td><td>${routes || "<span class=\"muted\">none</span>"}</td><td>${model.pinned ? "<span class=\"badge good\">pinned</span>" : ""}</td><td>${model.routingScoreKnown ? model.routingScore.toFixed(3) : "-"}</td></tr>`;
  }).join("");
}

function modelView() {
  const rows = modelRows();
  return `<div class="toolbar"><div><h1>Model Manager</h1><p>Pool revision: ${escapeHtml(state.pool?.revision ?? "-")}</p></div><button class="secondary" data-action="back">Back</button></div>
    ${state.error ? `<p class="error">${escapeHtml(state.error)}</p>` : ""}
    <section class="card"><div class="filters"><input id="model-search" type="search" placeholder="Search models" value="${escapeHtml(state.searchQuery)}" /><button class="secondary" data-action="refresh">Refresh</button></div></section>
    <section class="card"><table><thead><tr><th>Selected / ID</th><th>Name</th><th>Routes</th><th>Pin</th><th>Score</th></tr></thead><tbody id="model-table-body">${rows || `<tr><td colspan="5" class="empty">No models match.</td></tr>`}</tbody></table></section>`;
}

function bindModelSelections() {
  document.querySelectorAll("[data-model-id]").forEach((checkbox) => checkbox.addEventListener("change", () => updateSelection(checkbox.dataset.modelId, checkbox.checked)));
}

function renderModelRows() {
  const body = document.getElementById("model-table-body");
  if (!body) return;
  const rows = modelRows();
  body.innerHTML = rows || `<tr><td colspan="5" class="empty">No models match.</td></tr>`;
  bindModelSelections();
}

function logView() {
  const rows = state.logs.map((record) => `<tr><td>${escapeHtml(record.startedAt || record.completedAt || "-")}</td><td><code>${escapeHtml(record.finalModel || "-")}</code></td><td>${escapeHtml(record.finalRoute || "-")}</td><td>${(record.attempts || []).length}</td><td>${escapeHtml(record.result || "-")}</td><td>${record.totalTokens?.total ?? "-"}</td></tr>`).join("");
  return `<div class="toolbar"><div><h1>Usage Log</h1><p>Prompts, responses, tokens secrets, and authorization headers are never displayed here.</p></div><button class="secondary" data-action="back">Back</button></div>
    <section class="card"><div class="actions"><button class="secondary" data-action="refresh">Refresh</button></div><table><thead><tr><th>Time</th><th>Final model</th><th>Route</th><th>Attempts</th><th>Result</th><th>Tokens</th></tr></thead><tbody>${rows || `<tr><td colspan="6" class="empty">No usage records.</td></tr>`}</tbody></table></section>`;
}

function render() {
  app.innerHTML = view === "models" ? modelView() : view === "logs" ? logView() : mainView();
  document.querySelectorAll("[data-action]").forEach((button) => button.addEventListener("click", () => handleAction(button.dataset.action)));
  const search = document.getElementById("model-search");
  if (search) search.addEventListener("input", (event) => {
    state.searchQuery = event.currentTarget.value;
    renderModelRows();
  });
  const autostart = document.getElementById("autostart");
  if (autostart) autostart.addEventListener("change", async () => {
    try {
      await invoke("set_autostart", { enabled: autostart.checked });
    } catch (error) {
      state.error = error.message || String(error);
      render();
    }
  });
  bindModelSelections();
}

async function refreshStatus() {
  try {
    state.status = await invoke("desktop_status");
    if (state.status?.ready) {
      const providers = await invoke("gateway_providers");
      state.providers = providers.data || [];
    }
    state.error = "";
  } catch (error) { state.error = error.message || String(error); }
  render();
}

async function refreshModels() {
  try {
    const [models, pool] = await Promise.all([invoke("gateway_models"), invoke("gateway_pool")]);
    state.models = models.data || [];
    state.pool = pool;
    state.error = "";
  } catch (error) { state.error = error.message || String(error); }
  render();
}

async function refreshLogs() {
  try { const logs = await invoke("gateway_logs"); state.logs = logs.data || []; state.error = ""; }
  catch (error) { state.error = error.message || String(error); }
  render();
}

async function updateSelection(id, selected) {
  if (!state.pool) return;
  const request = { revision: state.pool.revision, select: selected ? [id] : [], deselect: selected ? [] : [id], selectAll: false, clearAll: false };
  try { state.pool = await invoke("patch_model_pool", { request }); await refreshModels(); }
  catch (error) { state.error = error.message || String(error); render(); }
}

async function handleAction(action) {
  try {
    if (action === "attach") { state.status = await invoke("attach_or_start", {}); await refreshStatus(); }
    else if (action === "models") await invoke("open_model_manager");
    else if (action === "logs") await invoke("open_usage_log");
    else if (action === "quit") await invoke("shutdown");
    else if (action === "refresh") view === "models" ? await refreshModels() : await refreshLogs();
    else if (action === "back") window.location.href = "index.html";
  } catch (error) { setError(error); }
}

render();
if (view === "models") refreshModels();
else if (view === "logs") refreshLogs();
else refreshStatus();
setInterval(() => { if (view === "main") refreshStatus(); }, 5000);
