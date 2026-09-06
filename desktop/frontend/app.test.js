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
    querySelectorAll() {
      return [];
    }
  };
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
  const search = document.getElementById("model-search");
  assert.ok(search);
  const renderCount = document.app.renderCount;

  search.value = "gpt";
  search.dispatch("input");

  assert.strictEqual(document.getElementById("model-search"), search);
  assert.equal(document.app.renderCount, renderCount);
});
