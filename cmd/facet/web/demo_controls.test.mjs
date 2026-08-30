// Pins the Me screen's demo-only offline-pause toggle (facet-app-ux.md §11):
// hidden entirely unless the deployment feature-detected the control at boot
// (state.demoControlsEnabled), and the toggle posts to the endpoint and only
// updates state.demoPaused from the server's own response — never
// optimistically. Same vm harness as sync_degraded_banner.test.mjs: app.js
// is a plain browser script, so vm.runInContext hoists its function
// declarations onto the sandbox; `state` is a top-level const, reached by
// evaluating in the same context (ceremony.test.mjs's pattern).

import { test } from "node:test";
import assert from "node:assert/strict";
import vm from "node:vm";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const appSrc = fs.readFileSync(path.join(__dirname, "app.js"), "utf8");

function loadApp(fetchImpl) {
  const sandbox = {
    console,
    document: { addEventListener() {}, getElementById: () => ({ innerHTML: "", hidden: true }) },
    fetch: fetchImpl,
    queueMicrotask: (fn) => fn(),
  };
  vm.createContext(sandbox);
  vm.runInContext(appSrc, sandbox, { filename: "app.js" });
  return sandbox;
}

function setState(app, patch) {
  vm.runInContext(`Object.assign(state, ${JSON.stringify(patch)})`, app);
}

test("the demo controls card renders nothing when the deployment never opted in", () => {
  const app = loadApp(async () => { throw new Error("fetch must not be called"); });
  setState(app, { demoControlsEnabled: false, demoPaused: false });
  assert.equal(app.renderDemoControlsCard(), "");
});

test("the demo controls card offers a toggle once the deployment opted in", () => {
  const app = loadApp(async () => { throw new Error("fetch must not be called"); });
  setState(app, { demoControlsEnabled: true, demoPaused: false });
  const html = app.renderDemoControlsCard();
  assert.match(html, /data-demo-toggle/);
  assert.match(html, /Simulate offline/);
});

test("a paused device's card offers Reconnect instead", () => {
  const app = loadApp(async () => { throw new Error("fetch must not be called"); });
  setState(app, { demoControlsEnabled: true, demoPaused: true });
  const html = app.renderDemoControlsCard();
  assert.match(html, /Reconnect/);
});

test("toggling posts the flipped desired state and waits for the server's answer", async () => {
  const calls = [];
  const app = loadApp(async (url, opts) => {
    calls.push({ url, body: JSON.parse(opts.body) });
    return { ok: true, json: async () => ({ paused: true }) };
  });
  setState(app, { demoControlsEnabled: true, demoPaused: false });

  const p = app.toggleDemoConnectivity();
  assert.equal(vm.runInContext("state.demoPaused", app), false, "not optimistic: unchanged before the response resolves");
  await p;

  assert.equal(calls.length, 1);
  assert.equal(calls[0].url, "/api/demo/connectivity");
  assert.deepEqual(calls[0].body, { paused: true });
  assert.equal(vm.runInContext("state.demoPaused", app), true, "reconciled from the server's response");
});

test("a rejected toggle leaves state.demoPaused untouched", async () => {
  const toasts = [];
  const app = loadApp(async () => ({ ok: false, json: async () => ({ error: "nope" }) }));
  app.toast = (msg) => toasts.push(msg);
  setState(app, { demoControlsEnabled: true, demoPaused: false });

  await app.toggleDemoConnectivity();

  assert.equal(vm.runInContext("state.demoPaused", app), false);
  assert.equal(toasts.length, 1);
});
