const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const test = require("node:test");
const vm = require("node:vm");

class FakeElement {
  constructor() {
    this.listeners = new Map();
    this.innerHTML = "";
  }

  addEventListener(type, listener) {
    this.listeners.set(type, listener);
  }

  dispatch(type) {
    this.listeners.get(type)?.({ currentTarget: this });
  }
}

class FakeInput extends FakeElement {
  constructor(value) {
    super();
    this.value = value;
  }
}

function createDocument() {
  const app = new FakeElement();
  let search = null;
  let tableBody = null;
  let tabs = [];

  Object.defineProperty(app, "innerHTML", {
    get() {
      return this._innerHTML || "";
    },
    set(value) {
      this._innerHTML = value;
      this.renderCount = (this.renderCount || 0) + 1;
      const searchMatch = value.match(/id="model-search"[^>]*value="([^"]*)"/);
      search = searchMatch ? new FakeInput(searchMatch[1]) : null;
      tableBody = value.includes('id="model-table-body"') ? new FakeElement() : null;
      tabs = [...value.matchAll(/data-tab="([^"]+)"/g)].map(([, id]) => {
        const tab = new FakeElement();
        tab.dataset = { tab: id };
        return tab;
      });
    }
  });

  return {
    app,
    getElementById(id) {
      if (id === "app") return app;
      if (id === "model-search") return search;
      if (id === "model-table-body") return tableBody;
      return null;
    },
    querySelector(selector) {
      return selector === "#model-table-body" ? tableBody : null;
    },
    querySelectorAll(selector) {
      return selector === "[data-tab]" ? tabs : [];
    }
  };
}

function clickTab(document, id) {
  const tab = document.querySelectorAll("[data-tab]").find((candidate) => candidate.dataset.tab === id);
  assert.ok(tab);
  tab.dispatch("click");
}

test("model search keeps its input element while filtering", () => {
  const document = createDocument();
  const source = fs.readFileSync(path.join(__dirname, "app.js"), "utf8");
  const context = {
    console,
    document,
    URLSearchParams,
    setInterval() {},
    window: {
      __TAURI__: {
        core: {
          invoke: async () => ({ data: [], revision: 0 })
        }
      },
      location: { search: "?view=models" }
    }
  };

  vm.runInNewContext(source, context, { filename: "app.js" });
  clickTab(document, "model");
  const search = document.getElementById("model-search");
  assert.ok(search);
  const renderCount = document.app.renderCount;

  search.value = "gpt";
  search.dispatch("input");

  assert.strictEqual(document.getElementById("model-search"), search);
  assert.equal(document.app.renderCount, renderCount);
});

test("main window exposes all views and renders the model tab", () => {
  const document = createDocument();
  const source = fs.readFileSync(path.join(__dirname, "app.js"), "utf8");
  const hooks = {};
  const context = {
    console,
    document,
    URLSearchParams,
    setInterval() {},
    window: {
      __FMR_TEST__: hooks,
      __TAURI__: { core: { invoke: async () => ({ data: [] }) } },
      location: { search: "" }
    }
  };

  vm.runInNewContext(source, context, { filename: "app.js" });
  assert.deepEqual(document.querySelectorAll("[data-tab]").map((tab) => tab.dataset.tab), ["provider", "model", "usage", "endpoint"]);
  clickTab(document, "model");
  assert.match(document.app.innerHTML, /<h1>Model<\/h1>/);
});

test("model filters combine provider, status, and capability requirements", () => {
  const document = createDocument();
  const source = fs.readFileSync(path.join(__dirname, "app.js"), "utf8");
  const hooks = {};
  const context = {
    console,
    document,
    URLSearchParams,
    setInterval() {},
    window: {
      __FMR_TEST__: hooks,
      __TAURI__: { core: { invoke: async () => ({ data: [], revision: 0 }) } },
      location: { search: "?view=models" }
    }
  };

  vm.runInNewContext(source, context, { filename: "app.js" });
  clickTab(document, "model");
  hooks.state.models = [
    { id: "a", displayName: "Alpha", selected: true, capabilities: { tools: true }, routes: [{ provider: "one", access: "Free", enabled: true, health: { available: true } }] },
    { id: "b", displayName: "Beta", selected: false, capabilities: { vision: true }, routes: [{ provider: "two", access: "Paid", enabled: true, health: { available: true } }] }
  ];
  hooks.state.modelFilters = { provider: "one", access: "free", status: "selected", capability: "tools" };

  assert.deepEqual(hooks.visibleModels().map((model) => model.id), ["a"]);
});

test("usage log filters do not expose request content while filtering metadata", () => {
  const document = createDocument();
  const source = fs.readFileSync(path.join(__dirname, "app.js"), "utf8");
  const hooks = {};
  const context = { console, document, URLSearchParams, setInterval() {}, window: { __FMR_TEST__: hooks, __TAURI__: { core: { invoke: async () => ({ data: [] }) } }, location: { search: "?view=logs" } } };

  vm.runInNewContext(source, context, { filename: "app.js" });
  hooks.state.logs = [
    { id: "one", provider: "openai", finalModel: "openai/gpt", result: "success", attempts: [] },
    { id: "two", provider: "gemini", finalModel: "gemini/flash", result: "failure", attempts: [] }
  ];
  hooks.state.logFilters = { provider: "gemini", result: "failure", model: "flash" };

  assert.deepEqual(hooks.visibleLogs().map((record) => record.id), ["two"]);
});

test("unknown model metrics remain unknown instead of estimated", () => {
  const document = createDocument();
  const source = fs.readFileSync(path.join(__dirname, "app.js"), "utf8");
  const hooks = {};
  vm.runInNewContext(source, { console, document, URLSearchParams, setInterval() {}, window: { __FMR_TEST__: hooks, __TAURI__: { core: { invoke: async () => ({ data: [] }) } }, location: { search: "" } } }, { filename: "app.js" });
  const model = { routingScore: 50, routingScoreKnown: false, routes: [{ effectivePerformance: 50, latencyScore: 50, routingScoreKnown: false, ttftKnown: false }] };
  assert.equal(hooks.modelMetric(model, "performance"), null);
  assert.equal(hooks.modelMetric(model, "latency"), null);
  assert.equal(hooks.modelMetric(model, "score"), null);
});

test("benchmark performance requires an explicit performance signal", () => {
  const document = createDocument();
  const source = fs.readFileSync(path.join(__dirname, "app.js"), "utf8");
  const hooks = {};
  vm.runInNewContext(source, { console, document, URLSearchParams, setInterval() {}, window: { __FMR_TEST__: hooks, __TAURI__: { core: { invoke: async () => ({ data: [] }) } }, location: { search: "" } } }, { filename: "app.js" });
  const latencyOnly = { routes: [{ effectivePerformance: 50, routingScoreKnown: true, performanceKnown: false, ttftKnown: true }] };
  const benchmarked = { routes: [{ effectivePerformance: 73.4, routingScoreKnown: true, performanceKnown: true, ttftKnown: false }] };
  assert.equal(hooks.modelMetric(latencyOnly, "performance"), null);
  assert.equal(hooks.modelMetric(benchmarked, "performance"), 73.4);
});

test("provider secrets remain outside the frontend state", () => {
  const source = fs.readFileSync(path.join(__dirname, "app.js"), "utf8");
  assert.match(source, /const providerSecretChanges = new Map\(\)/);
  assert.match(source, /not placed in state/);
  assert.doesNotMatch(source, /configDraft[^\n]*apiKey/);
});

test("unavailable provider cards remain visible with escaped availability errors", () => {
  const document = createDocument();
  const source = fs.readFileSync(path.join(__dirname, "app.js"), "utf8");
  const hooks = {};
  vm.runInNewContext(source, { console, document, URLSearchParams, setInterval() {}, window: { __FMR_TEST__: hooks, __TAURI__: { core: { invoke: async () => ({ data: [] }) } }, location: { search: "" } } }, { filename: "app.js" });
  hooks.state.config = { providers: [
    { id: "opencode", name: "OpenCode", protocol: "openai-compatible", baseUrl: "http://localhost", enabled: true, clientKey: "opencode" },
    { id: "openrouter", name: "OpenRouter", protocol: "openai-compatible", baseUrl: "https://example.com", enabled: true, clientKey: "openrouter" }
  ] };
  hooks.state.providers = [{ id: "openrouter", available: false, errorCode: "provider_unavailable", message: "Configured &lt;message&gt;" }];

  const html = hooks.providerCards();
  assert.match(html, /OpenCode/);
  assert.match(html, /OpenRouter/);
  assert.match(html, /provider-error/);
  assert.match(html, /Configured &amp;lt;message&amp;gt;/);
  assert.doesNotMatch(html, /Configured &lt;message&gt;<\/p>/);
});

test("older config refresh cannot replace an acknowledged provider configuration", () => {
  const document = createDocument();
  const source = fs.readFileSync(path.join(__dirname, "app.js"), "utf8");
  const hooks = {};
  vm.runInNewContext(source, { console, document, URLSearchParams, setInterval() {}, window: { __FMR_TEST__: hooks, __TAURI__: { core: { invoke: async () => ({ data: [] }) } }, location: { search: "" } } }, { filename: "app.js" });
  hooks.state.latestAcknowledgedRevision = 8;
  hooks.state.config = { revision: 8, providers: [{ id: "saved" }] };
  const accepted = hooks.acceptServerConfig({ revision: 7, providers: [] });
  assert.equal(accepted, false);
  assert.deepEqual(hooks.state.config.providers, [{ id: "saved" }]);
});

test("provider save validation requires an enabled provider", () => {
  const source = fs.readFileSync(path.join(__dirname, "app.js"), "utf8");
  assert.match(source, /providers\.some\(\(provider\) => provider\.enabled\)/);
  assert.match(source, /At least one provider must be enabled/);
});

test("generic model deselection persists an exact provider exclusion", async () => {
  const document = createDocument();
  const source = fs.readFileSync(path.join(__dirname, "app.js"), "utf8");
  const hooks = {};
  let savedRequest;
  const provider = { id: "custom", name: "Custom", protocol: "openai-compatible", baseUrl: "https://example.test/v1", enabled: true, autoProbe: true, excludedModelIds: [] };
  const invoke = async (command, args) => {
    if (command === "update_gateway_config") {
      savedRequest = args.request;
      return { config: { revision: 3, bind: "127.0.0.1:8787", providers: args.request.providers }, restarted: true, activationState: "running" };
    }
    if (command === "desktop_status") return { ready: false };
    if (command === "gateway_models") return { data: [] };
    if (command === "gateway_pool") return { revision: 1, mode: "Manual", selectedProviderModelIds: [] };
    return { data: [] };
  };
  vm.runInNewContext(source, { console, document, URLSearchParams, setInterval() {}, window: { __FMR_TEST__: hooks, __TAURI__: { core: { invoke } }, location: { search: "" } } }, { filename: "app.js" });
  hooks.acceptServerConfig({ revision: 2, bind: "127.0.0.1:8787", providers: [provider] });
  hooks.state.models = [{ id: "custom/model-a", selected: true, routes: [{ provider: "custom", upstreamModelId: "vendor/model-a", enabled: true }] }];
  await hooks.updateSelection("custom/model-a", false);
  assert.deepEqual(JSON.parse(JSON.stringify(savedRequest.providers[0].excludedModelIds)), ["vendor/model-a"]);
  assert.equal(savedRequest.providers[0].autoProbe, true);
});

test("dirty provider edits block model selection without persisting exclusions", async () => {
  const document = createDocument();
  const source = fs.readFileSync(path.join(__dirname, "app.js"), "utf8");
  const hooks = {};
  let updateCalls = 0;
  const invoke = async (command) => {
    if (command === "update_gateway_config") updateCalls++;
    return command === "desktop_status" ? { ready: false } : { data: [] };
  };
  vm.runInNewContext(source, { console, document, URLSearchParams, setInterval() {}, window: { __FMR_TEST__: hooks, __TAURI__: { core: { invoke } }, location: { search: "" } } }, { filename: "app.js" });
  hooks.state.activeTab = "model";
  hooks.state.configDirty = true;
  hooks.state.configDraft = [{ id: "custom", excludedModelIds: [] }];
  hooks.state.models = [{ id: "custom/model-a", selected: true, routes: [{ provider: "custom", upstreamModelId: "vendor/model-a" }] }];

  await hooks.updateSelection("custom/model-a", false);

  assert.equal(updateCalls, 0);
  assert.deepEqual(hooks.state.configDraft[0].excludedModelIds, []);
  assert.match(document.app.innerHTML, /Save or discard provider edits before changing model selection\./);
});

test("unknown metrics render deterministic diagnostic reasons", () => {
  const document = createDocument();
  const source = fs.readFileSync(path.join(__dirname, "app.js"), "utf8");
  const hooks = {};
  vm.runInNewContext(source, { console, document, URLSearchParams, setInterval() {}, window: { __FMR_TEST__: hooks, __TAURI__: { core: { invoke: async () => ({ data: [] }) } }, location: { search: "" } } }, { filename: "app.js" });
  hooks.state.models = [{ id: "custom/model", displayName: "Model", selected: true, routingScoreKnown: false, routes: [{ provider: "custom", access: "Unknown", enabled: true, performanceKnown: false, performanceReason: "no_unique_match", ttftKnown: false, latencyReason: "no_sample", routingScoreKnown: false, scoreReason: "no_performance_and_latency" }] }];
  const rows = hooks.modelRows();
  assert.match(rows, /title="no_unique_match">-/);
  assert.match(rows, /title="no_sample">-/);
  assert.match(rows, /title="no_performance_and_latency">-/);
});
