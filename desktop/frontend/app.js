const tauriInvoke = window.__TAURI__?.core?.invoke;
const tauriListen = window.__TAURI__?.event?.listen;
const app = document.getElementById("app");
const state = {
  status: null, providers: [], models: [], pool: null, logs: [], error: "",
  searchQuery: "", modelFilters: { provider: "", access: "", status: "all", capability: "" },
  logFilters: { provider: "", result: "", model: "" }, expandedLog: "",
  activeTab: "provider", config: null, configDraft: null, configBaseRevision: 0, configDirty: false, configSaving: false,
  configGeneration: 0, configReadGeneration: 0, modelSort: { key: "name", direction: "asc" }, fetchedAt: "", notice: ""
};
// Provider credentials are deliberately not placed in state: test hooks expose state
// and a periodic render must never write a replacement secret back into HTML.
const providerSecretChanges = new Map();
if (window.__FMR_TEST__) Object.assign(window.__FMR_TEST__, { state, visibleModels: () => visibleModels(), visibleLogs: () => visibleLogs(), modelMetric: (model, key) => modelMetric(model, key) });

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
  const summaries = new Map(state.providers.map((provider) => [provider.id, provider]));
  const providers = editableProviders();
  if (!providers.length) return `<div class="empty">No providers configured.</div>`;
  return providers.map((provider) => {
    const summary = summaries.get(provider.id) || {};
    return `<fieldset class="provider-editor" data-provider-key="${escapeHtml(provider.clientKey)}"><legend>${escapeHtml(provider.name || provider.id)}</legend><div class="form-grid">
      <label>ID<input data-field="id" value="${escapeHtml(provider.id)}" ${provider.id === "opencode" ? "readonly" : ""}></label>
      <label>Name<input data-field="name" value="${escapeHtml(provider.name)}"></label>
      <label class="wide">Base URL<input data-field="baseUrl" value="${escapeHtml(provider.baseUrl)}"></label>
      <label>Protocol<select data-field="protocol"><option value="openai-compatible">OpenAI compatible</option></select></label>
      <label class="checkbox"><input data-field="enabled" type="checkbox" ${provider.enabled ? "checked" : ""}> Enabled</label>
      <label>API key<input data-field="apiKey" type="password" placeholder="${provider.hasCredential ? "Configured — enter to replace" : "Optional"}" autocomplete="new-password"></label>
      <label class="checkbox"><input data-field="clearKey" type="checkbox"> Clear key</label>
    </div><div class="muted">${summary.models ?? 0} models · ${summary.routes ?? 0} routes</div>${provider.id === "opencode" ? "" : `<button class="danger" data-remove-provider="${escapeHtml(provider.clientKey)}">Remove</button>`}</fieldset>`;
  }).join("");
}

function newClientKey() {
  return globalThis.crypto?.randomUUID?.() || `provider-${Date.now()}-${Math.random().toString(16).slice(2)}`;
}

function cloneProvider(provider) {
  return { id: provider.id || "", name: provider.name || "", protocol: provider.protocol || "openai-compatible", baseUrl: provider.baseUrl || "", enabled: provider.enabled !== false, hasCredential: Boolean(provider.hasCredential), clientKey: provider.clientKey || newClientKey() };
}

function beginProviderDraft(config = state.config) {
  if (!config || state.configDraft) return;
  state.configDraft = (config.providers || []).map(cloneProvider);
  state.configBaseRevision = config.revision || 0;
  state.configDirty = false;
}

function editableProviders() {
  beginProviderDraft();
  return state.configDraft || [];
}

function clearProviderSecrets() {
  providerSecretChanges.clear();
}

function providerDraftByKey(clientKey) {
  return editableProviders().find((provider) => provider.clientKey === clientKey);
}

function markProviderDirty() {
  state.configDirty = true;
}

function providerView() {
  return `<div class="toolbar"><div><h1>Provider</h1><p>연결된 Provider와 모델 탐색 상태를 확인합니다.</p></div><span class="badge">Tauri desktop</span></div>
    ${statusCard()}
    <div class="actions">
      <button data-action="attach">Attach / Start Gateway</button>
      <button class="danger" data-action="quit">Quit</button>
    </div>
    ${state.notice ? `<p class="status-ready">${escapeHtml(state.notice)}</p>` : ""}
    <section class="card"><div class="toolbar"><h2>Providers</h2><button class="secondary" data-action="add-provider">Add provider</button></div><div id="provider-list">${providerCards()}</div><div class="actions"><button data-action="save-providers" ${state.configSaving ? "disabled" : ""}>${state.configSaving ? "Saving…" : "Save providers"}</button><button class="secondary" data-action="discard-providers" ${state.configDirty ? "" : "disabled"}>Discard changes</button></div></section>`;
}

function endpointView() {
  const config = state.config || state.status || {};
  return `<div class="toolbar"><div><h1>Endpoint/key</h1><p>FMR inference endpoint와 인증 설정입니다.</p></div></div>
    ${state.notice ? `<p class="status-ready">${escapeHtml(state.notice)}</p>` : ""}
    <section class="card"><div class="form-grid"><label>Inference bind<input id="inference-bind" value="${escapeHtml(config.bind || "127.0.0.1:8787")}"></label><label>Replace API key<input id="inference-key" type="password" placeholder="${config.inferenceKeyConfigured ? "Configured — enter to replace" : "Optional on loopback"}" autocomplete="new-password"></label><label class="checkbox"><input id="clear-inference-key" type="checkbox"> Clear key</label></div>${state.generatedKey ? `<p class="generated-key">New key (shown once): <code>${escapeHtml(state.generatedKey)}</code></p>` : ""}<p class="muted">loopback endpoint는 API key 인증을 강제하지 않습니다. LAN/전체 인터페이스 bind에는 key가 필요합니다.</p><div class="actions"><button data-action="save-endpoint">Save</button><button class="secondary" data-action="generate-key">Generate new key</button><button class="secondary" data-action="refresh-config">Refresh</button></div></section>`;
}

function tabBar() {
  const tabs = [["provider", "Provider"], ["model", "Model"], ["usage", "Usage log"], ["endpoint", "Endpoint/key"]];
  return `<nav class="tabs" aria-label="Free-Model-Router views">${tabs.map(([id, label]) => `<button class="tab ${state.activeTab === id ? "active" : ""}" data-tab="${id}" aria-current="${state.activeTab === id ? "page" : "false"}">${label}</button>`).join("")}</nav>`;
}

function hasCapability(value, capability) {
  return !capability || value?.[capability] === true;
}

function representativeRoute(model) {
  return [...(model.routes || [])].sort((left, right) => {
    if (left.routingScoreKnown !== right.routingScoreKnown) return left.routingScoreKnown ? -1 : 1;
    if ((right.routingScore || 0) !== (left.routingScore || 0)) return (right.routingScore || 0) - (left.routingScore || 0);
    return String(left.id || "").localeCompare(String(right.id || ""));
  })[0];
}

function knownMetric(value, known) {
  return known && Number.isFinite(value) ? value : null;
}

function modelMetric(model, key) {
  const route = representativeRoute(model);
  if (key === "performance") return knownMetric(route?.effectivePerformance, route?.performanceKnown);
  if (key === "latency") return knownMetric(route?.latencyScore, route?.ttftKnown);
  if (key === "score") return knownMetric(model.routingScore, model.routingScoreKnown);
  return null;
}

function compareModels(left, right) {
  const { key, direction } = state.modelSort;
  if (key === "name") return (left.displayName || left.id).localeCompare(right.displayName || right.id) * (direction === "asc" ? 1 : -1);
  const leftValue = modelMetric(left, key);
  const rightValue = modelMetric(right, key);
  if (leftValue === null && rightValue === null) return String(left.id).localeCompare(String(right.id));
  if (leftValue === null) return 1;
  if (rightValue === null) return -1;
  const numeric = leftValue - rightValue;
  return (numeric || String(left.id).localeCompare(String(right.id))) * (direction === "asc" ? 1 : -1);
}

function visibleModels() {
  const query = state.searchQuery.toLowerCase();
  const filters = state.modelFilters;
  return state.models.filter((model) => {
    const routes = model.routes || [];
    if (query && ![model.id, model.displayName, model.upstreamId, model.canonicalKey].join(" ").toLowerCase().includes(query)) return false;
    if (filters.status === "selected" && !model.selected) return false;
    if (filters.status === "unselected" && model.selected) return false;
    if (filters.status === "pinned" && !model.pinned) return false;
    const matchingRoutes = routes.filter((route) =>
      (!filters.provider || route.provider === filters.provider) &&
      (!filters.access || String(route.access).toLowerCase() === filters.access) &&
      (filters.status !== "available" || (route.enabled && route.health?.available)) &&
      (filters.status !== "unavailable" || !(route.enabled && route.health?.available)) &&
      hasCapability(route.capabilities || model.capabilities, filters.capability)
    );
    const needsRouteMatch = filters.provider || filters.access || filters.status === "available" || filters.status === "unavailable" || filters.capability;
    return !needsRouteMatch || matchingRoutes.length > 0;
  }).sort(compareModels);
}

function modelRows() {
  const models = visibleModels();
  return models.map((model) => {
    const routes = (model.routes || []).map((route) => `<span class="badge ${route.enabled && route.health?.available ? "good" : "warn"}">${escapeHtml(route.provider)} · ${escapeHtml(route.access)}${route.ttftKnown ? ` · ${Math.round(route.ttftMs)}ms` : ""}</span>`).join("");
    const route = representativeRoute(model);
    const performance = modelMetric(model, "performance");
    const latency = modelMetric(model, "latency");
    const score = modelMetric(model, "score");
    return `<tr><td><label class="checkbox"><input type="checkbox" data-model-id="${escapeHtml(model.id)}" ${model.selected ? "checked" : ""} /> <code>${escapeHtml(model.id)}</code></label></td><td>${escapeHtml(model.displayName)}</td><td>${routes || "<span class=\"muted\">none</span>"}</td><td>${performance === null ? "-" : performance.toFixed(1)}</td><td title="${route?.ttftKnown ? `${Math.round(route.ttftMs)} ms TTFT` : "No TTFT measurement"}">${latency === null ? "-" : latency.toFixed(1)}</td><td><button class="secondary" data-pin-id="${escapeHtml(model.id)}">${model.pinned ? "Unpin" : "Pin"}</button></td><td>${score === null ? "-" : score.toFixed(1)}</td></tr>`;
  }).join("");
}

function modelView() {
  const rows = modelRows();
  const providers = [...new Set(state.models.flatMap((model) => (model.routes || []).map((route) => route.provider)))].sort();
  return `<div class="toolbar"><div><h1>Model</h1><p>Pool revision: ${escapeHtml(state.pool?.revision ?? "-")} · Last refreshed: ${escapeHtml(state.fetchedAt || "-")}</p></div><button class="secondary" data-action="rediscover" title="Rediscover provider catalogs">🔃 Rediscover</button></div>
    ${state.error ? `<p class="error">${escapeHtml(state.error)}</p>` : ""}
    <section class="card"><div class="filters"><input id="model-search" type="search" placeholder="Search models" value="${escapeHtml(state.searchQuery)}" /><select data-filter="provider"><option value="">All providers</option>${providers.map((provider) => `<option value="${escapeHtml(provider)}" ${state.modelFilters.provider === provider ? "selected" : ""}>${escapeHtml(provider)}</option>`).join("")}</select><select data-filter="access"><option value="">All access</option><option value="free" ${state.modelFilters.access === "free" ? "selected" : ""}>Free</option><option value="free-tier" ${state.modelFilters.access === "free-tier" ? "selected" : ""}>Free-tier</option><option value="paid" ${state.modelFilters.access === "paid" ? "selected" : ""}>Paid</option><option value="unknown" ${state.modelFilters.access === "unknown" ? "selected" : ""}>Unknown</option></select><select data-filter="status"><option value="all">All status</option><option value="selected" ${state.modelFilters.status === "selected" ? "selected" : ""}>Selected</option><option value="unselected" ${state.modelFilters.status === "unselected" ? "selected" : ""}>Unselected</option><option value="pinned" ${state.modelFilters.status === "pinned" ? "selected" : ""}>Pinned</option><option value="available" ${state.modelFilters.status === "available" ? "selected" : ""}>Available</option><option value="unavailable" ${state.modelFilters.status === "unavailable" ? "selected" : ""}>Unavailable</option></select><select data-filter="capability"><option value="">All capabilities</option><option value="streaming" ${state.modelFilters.capability === "streaming" ? "selected" : ""}>Streaming</option><option value="tools" ${state.modelFilters.capability === "tools" ? "selected" : ""}>Tools</option><option value="vision" ${state.modelFilters.capability === "vision" ? "selected" : ""}>Vision</option><option value="structuredOutput" ${state.modelFilters.capability === "structuredOutput" ? "selected" : ""}>Structured output</option><option value="reasoning" ${state.modelFilters.capability === "reasoning" ? "selected" : ""}>Reasoning</option></select><button class="secondary" data-action="refresh">Refresh</button></div><div class="actions"><button data-action="select-all">Select All</button><button class="secondary" data-action="clear-all">Clear All</button><button class="secondary" data-action="select-visible">Select Visible</button><button class="secondary" data-action="clear-visible">Clear Visible</button><button class="secondary" data-action="auto-select">Auto Select</button></div></section>
    <section class="card"><table><thead><tr><th>Selected / ID</th><th><button class="table-sort" data-sort="name">Name</button></th><th>Routes</th><th><button class="table-sort" data-sort="performance">Performance</button></th><th><button class="table-sort" data-sort="latency">Latency</button></th><th>Pin</th><th><button class="table-sort" data-sort="score">Routing score</button></th></tr></thead><tbody id="model-table-body">${rows || `<tr><td colspan="7" class="empty">No models match.</td></tr>`}</tbody></table></section>`;
}

function bindModelSelections() {
  document.querySelectorAll("[data-model-id]").forEach((checkbox) => checkbox.addEventListener("change", () => updateSelection(checkbox.dataset.modelId, checkbox.checked)));
  document.querySelectorAll("[data-pin-id]").forEach((button) => button.addEventListener("click", () => togglePin(button.dataset.pinId)));
}

function renderModelRows() {
  const body = document.getElementById("model-table-body");
  if (!body) return;
  const rows = modelRows();
  body.innerHTML = rows || `<tr><td colspan="7" class="empty">No models match.</td></tr>`;
  bindModelSelections();
  document.querySelectorAll("[data-remove-provider]").forEach((button) => button.addEventListener("click", () => removeProvider(button.dataset.removeProvider)));
}

function visibleLogs() {
  const filters = state.logFilters;
  return state.logs.filter((record) => {
    if (filters.provider && record.provider !== filters.provider) return false;
    if (filters.result && record.result !== filters.result) return false;
    return !filters.model || [record.finalModel, record.finalRoute].join(" ").toLowerCase().includes(filters.model.toLowerCase());
  });
}

function attemptRows(record) {
  return (record.attempts || []).map((attempt) => `<tr><td>${attempt.index ?? "-"}</td><td><code>${escapeHtml(attempt.providerModel || "-")}</code></td><td>${escapeHtml(attempt.route || "-")}</td><td>${escapeHtml(attempt.httpStatus ?? "-")}</td><td>${escapeHtml(attempt.failureClass || "-")}</td><td>${Math.round(attempt.ttftMs ?? 0)} / ${Math.round(attempt.totalLatencyMs ?? 0)} ms</td><td>${attempt.tokens?.total ?? "-"}</td></tr>`).join("");
}

function logView() {
  const providers = [...new Set(state.logs.map((record) => record.provider).filter(Boolean))].sort();
  const results = [...new Set(state.logs.map((record) => record.result).filter(Boolean))].sort();
  const rows = logRows();
  return `<div class="toolbar"><div><h1>Usage log</h1><p>Prompts, responses, tokens secrets, and authorization headers are never displayed here.</p></div></div>
    <section class="card"><div class="filters"><input data-log-filter="model" type="search" placeholder="Model or route" value="${escapeHtml(state.logFilters.model)}" /><select data-log-filter="provider"><option value="">All providers</option>${providers.map((provider) => `<option value="${escapeHtml(provider)}" ${state.logFilters.provider === provider ? "selected" : ""}>${escapeHtml(provider)}</option>`).join("")}</select><select data-log-filter="result"><option value="">All results</option>${results.map((result) => `<option value="${escapeHtml(result)}" ${state.logFilters.result === result ? "selected" : ""}>${escapeHtml(result)}</option>`).join("")}</select><button class="secondary" data-action="refresh">Refresh</button></div><table><thead><tr><th>Time</th><th>Final model</th><th>Route</th><th>Attempts</th><th>Result</th><th>Tokens</th></tr></thead><tbody id="log-table-body">${rows || `<tr><td colspan="6" class="empty">No usage records.</td></tr>`}</tbody></table></section>`;
}

function logRows() {
  return visibleLogs().map((record, index) => {
    const id = record.id || `${record.startedAt}-${index}`;
    const expanded = state.expandedLog === id;
    return `<tr><td>${escapeHtml(record.startedAt || record.completedAt || "-")}</td><td><code>${escapeHtml(record.finalModel || "-")}</code></td><td>${escapeHtml(record.finalRoute || "-")}</td><td><button class="secondary" data-log-id="${escapeHtml(id)}">${(record.attempts || []).length} ${expanded ? "Hide" : "Show"}</button></td><td>${escapeHtml(record.result || "-")}</td><td>${record.totalTokens?.total ?? "-"}</td></tr>${expanded ? `<tr class="attempts"><td colspan="6"><table><thead><tr><th>#</th><th>Model</th><th>Route</th><th>HTTP</th><th>Failure</th><th>TTFT / Total</th><th>Tokens</th></tr></thead><tbody>${attemptRows(record) || `<tr><td colspan="7" class="empty">No attempts recorded.</td></tr>`}</tbody></table></td></tr>` : ""}`;
  }).join("");
}

function bindLogExpansions() {
  document.querySelectorAll("[data-log-id]").forEach((button) => button.addEventListener("click", () => {
    state.expandedLog = state.expandedLog === button.dataset.logId ? "" : button.dataset.logId;
    renderLogRows();
  }));
}

function renderLogRows() {
  const body = document.getElementById("log-table-body");
  if (!body) return;
  const rows = logRows();
  body.innerHTML = rows || `<tr><td colspan="6" class="empty">No usage records.</td></tr>`;
  bindLogExpansions();
}

function render() {
  const content = { provider: providerView, model: modelView, usage: logView, endpoint: endpointView }[state.activeTab]();
  app.innerHTML = `<div class="app-shell">${tabBar()}${content}</div>`;
  document.querySelectorAll("[data-action]").forEach((button) => button.addEventListener("click", () => handleAction(button.dataset.action)));
  document.querySelectorAll("[data-tab]").forEach((button) => button.addEventListener("click", () => switchTab(button.dataset.tab)));
  document.querySelectorAll("[data-sort]").forEach((button) => button.addEventListener("click", () => {
    const key = button.dataset.sort;
    state.modelSort.direction = state.modelSort.key === key && state.modelSort.direction === "asc" ? "desc" : "asc";
    state.modelSort.key = key;
    renderModelRows();
  }));
  const search = document.getElementById("model-search");
  if (search) search.addEventListener("input", (event) => {
    state.searchQuery = event.currentTarget.value;
    renderModelRows();
  });
  document.querySelectorAll("[data-filter]").forEach((input) => input.addEventListener("change", (event) => {
    state.modelFilters[event.currentTarget.dataset.filter] = event.currentTarget.value;
    render();
  }));
  document.querySelectorAll("[data-log-filter]").forEach((input) => input.addEventListener(input.type === "search" ? "input" : "change", (event) => {
    state.logFilters[event.currentTarget.dataset.logFilter] = event.currentTarget.value;
    if (event.currentTarget.type === "search") renderLogRows(); else render();
  }));
  bindLogExpansions();
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
  bindProviderEditors();
}

function bindProviderEditors() {
  document.querySelectorAll(".provider-editor").forEach((editor) => {
    const clientKey = editor.dataset.providerKey;
    editor.querySelectorAll("[data-field]").forEach((input) => input.addEventListener(input.type === "checkbox" ? "change" : "input", (event) => {
      const field = event.currentTarget.dataset.field;
      const provider = providerDraftByKey(clientKey);
      if (!provider) return;
      if (field === "apiKey" || field === "clearKey") {
        const change = providerSecretChanges.get(clientKey) || {};
        if (field === "apiKey") change.value = event.currentTarget.value;
        else change.clear = event.currentTarget.checked;
        providerSecretChanges.set(clientKey, change);
      } else {
        provider[field] = event.currentTarget.type === "checkbox" ? event.currentTarget.checked : event.currentTarget.value;
      }
      markProviderDirty();
    }));
  });
  document.querySelectorAll("[data-remove-provider]").forEach((button) => button.addEventListener("click", () => removeProvider(button.dataset.removeProvider)));
}

function acceptServerConfig(config) {
  state.config = config;
  if (!state.configDirty && !state.configSaving) {
    state.configDraft = null;
    state.configBaseRevision = config?.revision || 0;
  }
}

function renderProviderCards() {
  const list = document.getElementById("provider-list");
  if (!list) return render();
  list.innerHTML = providerCards();
  bindProviderEditors();
}

function renderProviderStatus() {
  // Do not replace the editable form while a user is typing.
  const error = document.querySelector(".error");
  if (error && !state.error) error.remove();
}

async function refreshStatus() {
  try {
    state.status = await invoke("desktop_status");
    if (state.status?.ready) {
      const [providers, config] = await Promise.all([invoke("gateway_providers"), invoke("gateway_config")]);
      state.providers = providers.data || [];
      acceptServerConfig(config);
      await startUsageUpdates();
    }
    state.error = "";
  } catch (error) { state.error = error.message || String(error); }
  if (state.activeTab === "provider" && state.configDirty) renderProviderStatus(); else render();
}

async function refreshModels() {
  try {
    const [models, pool] = await Promise.all([invoke("gateway_models"), invoke("gateway_pool")]);
    state.models = models.data || [];
    state.pool = pool;
    state.fetchedAt = new Date().toLocaleTimeString();
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
  await mutatePool({ select: selected ? [id] : [], deselect: selected ? [] : [id], selectAll: false, clearAll: false });
}

async function refreshConfig() {
  const generation = ++state.configReadGeneration;
  try {
    const config = await invoke("gateway_config");
    if (generation === state.configReadGeneration) acceptServerConfig(config);
    state.error = "";
  } catch (error) { state.error = error.message || String(error); }
  if (state.activeTab === "provider" && state.configDirty) renderProviderStatus(); else render();
}

function collectProviders() {
  return editableProviders().map((provider) => {
    const update = { id: provider.id.trim(), name: provider.name.trim(), protocol: provider.protocol, baseUrl: provider.baseUrl.trim(), enabled: provider.enabled };
    const secret = providerSecretChanges.get(provider.clientKey);
    if (secret?.clear) update.apiKey = "";
    else if (secret?.value) update.apiKey = secret.value;
    return update;
  });
}

function addProvider() {
  const providers = editableProviders();
  let suffix = providers.length + 1;
  while (providers.some((provider) => provider.id === `provider-${suffix}`)) suffix++;
  providers.push({ id: `provider-${suffix}`, name: `Provider ${suffix}`, protocol: "openai-compatible", baseUrl: "https://example.com/v1", enabled: true, hasCredential: false, clientKey: newClientKey() });
  markProviderDirty();
  renderProviderCards();
}

function removeProvider(clientKey) {
  const providers = editableProviders();
  const index = providers.findIndex((provider) => provider.clientKey === clientKey);
  if (index < 0) return;
  providerSecretChanges.delete(clientKey);
  providers.splice(index, 1);
  markProviderDirty();
  renderProviderCards();
}

async function saveConfig(options = {}) {
  if (state.configSaving) return;
  const providers = options.providers || collectProviders();
  const ids = providers.map((provider) => provider.id);
  if (ids.some((id) => !id) || new Set(ids).size !== ids.length) throw new Error("Each provider needs a unique ID.");
  state.configSaving = true;
  state.configGeneration++;
  const bind = document.getElementById("inference-bind")?.value.trim() || state.config.bind;
  const request = { revision: state.configBaseRevision || state.config.revision, bind, providers };
  if (options.inferenceKey !== undefined) request.inferenceKey = options.inferenceKey;
  if (options.generateInferenceKey) request.generateInferenceKey = true;
  try {
    const response = await invoke("update_gateway_config", { request });
    state.config = response.config;
    state.configDraft = null;
    state.configBaseRevision = response.config.revision || 0;
    state.configDirty = false;
    clearProviderSecrets();
    state.generatedKey = response.generatedInferenceKey || "";
    state.notice = response.credentialCleanupPending ? "Saved, but an old credential could not be removed. Retry after checking the OS credential store." : response.restarted ? "Saved and gateway restarted." : response.restartRequired ? "Saved. External gateway restart required." : "Saved.";
  } finally {
    state.configSaving = false;
  }
  await refreshStatus();
}

function discardProviders() {
  state.configDraft = null;
  state.configBaseRevision = state.config?.revision || 0;
  state.configDirty = false;
  clearProviderSecrets();
  renderProviderCards();
}

async function switchTab(tab) {
  if (tab !== "endpoint") state.generatedKey = "";
  state.activeTab = tab;
  if (state.activeTab === "model" && document.getElementById("model-table-body")) renderModelRows(); else render();
  if (tab === "model") await refreshModels();
  else if (tab === "usage") await refreshLogs();
  else if (tab === "endpoint") await refreshConfig();
  else await refreshStatus();
}

async function startUsageUpdates() {
  if (!tauriListen) return;
  if (!state.usageListenerSubscribed) {
    await tauriListen("usage-event", () => {
      window.clearTimeout(state.usageRefreshTimer);
      state.usageRefreshTimer = window.setTimeout(() => refreshLogs(), 150);
    });
    state.usageListenerSubscribed = true;
  }
  await invoke("start_usage_stream");
}

async function mutatePool(change) {
  if (!state.pool) return;
  const request = { revision: state.pool.revision, select: [], deselect: [], selectAll: false, clearAll: false, ...change };
  try { state.pool = await invoke("patch_model_pool", { request }); await refreshModels(); }
  catch (error) {
    await refreshModels();
    state.error = `Pool update failed. Refresh and retry: ${error.message || String(error)}`;
    render();
  }
}

async function togglePin(id) {
  if (!state.pool) return;
  try {
    state.pool = await invoke("pin_model", { request: { revision: state.pool.revision, providerModelId: id } });
    await refreshModels();
  } catch (error) { setError(error); }
}

async function autoSelect() {
  if (!state.pool) return;
  try {
    state.pool = await invoke("auto_select", { request: { revision: state.pool.revision } });
    await refreshModels();
  } catch (error) { setError(error); }
}

async function handleAction(action) {
  try {
    if (action === "attach") { state.status = await invoke("attach_or_start", {}); await refreshStatus(); }
    else if (action === "quit") await invoke("shutdown");
    else if (action === "refresh") state.activeTab === "model" ? await refreshModels() : await refreshLogs();
    else if (action === "refresh-config") await refreshConfig();
    else if (action === "rediscover") { await invoke("refresh_gateway_catalog"); await refreshModels(); }
    else if (action === "add-provider") addProvider();
    else if (action === "save-providers") await saveConfig({ providers: collectProviders() });
    else if (action === "discard-providers") discardProviders();
    else if (action === "save-endpoint") {
      if (state.configDirty) throw new Error("Save or discard provider changes before changing the endpoint.");
      const clear = document.getElementById("clear-inference-key")?.checked;
      const key = document.getElementById("inference-key")?.value;
      await saveConfig({ inferenceKey: clear ? "" : (key || undefined) });
    }
    else if (action === "generate-key") {
      if (state.configDirty) throw new Error("Save or discard provider changes before generating a key.");
      await saveConfig({ generateInferenceKey: true });
    }
    else if (action === "select-all") await mutatePool({ selectAll: true });
    else if (action === "clear-all") await mutatePool({ clearAll: true });
    else if (action === "select-visible") await mutatePool({ select: visibleModels().map((model) => model.id) });
    else if (action === "clear-visible") await mutatePool({ deselect: visibleModels().map((model) => model.id) });
    else if (action === "auto-select") await autoSelect();
  } catch (error) { setError(error); }
}

render();
refreshStatus();
setInterval(() => {
  if (state.activeTab === "provider") refreshStatus();
  if (state.activeTab === "model") refreshModels();
}, 5000);
