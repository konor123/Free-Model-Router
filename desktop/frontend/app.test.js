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
