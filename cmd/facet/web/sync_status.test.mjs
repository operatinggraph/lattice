// Pins the top bar's sync-status verdict: the one label a person reads to
// answer "is this live, and has it caught up". Three independent feed signals
// fold into it (host<->NATS connectivity, sync-manager health, hydration), and
// the precedence between them is the whole point — each state would be
// actively misdescribed by the ones below it, so a wrong fold is a lie about
// freshness rather than a cosmetic slip.
//
// Same vm harness as sync_degraded_banner.test.mjs.

import { test } from "node:test";
import assert from "node:assert/strict";
import vm from "node:vm";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const appSrc = fs.readFileSync(path.join(__dirname, "app.js"), "utf8");
const indexHtml = fs.readFileSync(path.join(__dirname, "index.html"), "utf8");
const styleCss = fs.readFileSync(path.join(__dirname, "style.css"), "utf8");

function loadApp() {
  const els = new Map();
  const el = (id) => {
    if (!els.has(id)) els.set(id, { id, hidden: true, textContent: "", className: "", title: "" });
    return els.get(id);
  };
  const sandbox = {
    console,
    document: { addEventListener() {}, getElementById: el, querySelectorAll: () => [] },
    setTimeout: () => 1,
    clearTimeout: () => {},
    queueMicrotask,
  };
  vm.createContext(sandbox);
  vm.runInContext(appSrc, sandbox, { filename: "app.js" });
  return { sandbox, el, evaluate: (src) => vm.runInContext(src, sandbox) };
}

const base = { connected: true, syncDegraded: false, hydrated: true, lastFrameAt: 1000 };

test("index.html and style.css declare what the renderer targets", () => {
  assert.match(indexHtml, /id="sync-status"/);
  assert.match(indexHtml, /id="sync-status-dot"/);
  assert.match(indexHtml, /id="sync-status-label"/);
  for (const kind of ["synced", "syncing", "stale", "offline"]) {
    assert.match(styleCss, new RegExp(`\\.sync-dot\\.${kind}`), `no dot colour for ${kind}`);
  }
});

test("a caught-up healthy engine reads as synced", () => {
  const { sandbox } = loadApp();
  assert.equal(sandbox.syncStatus(base).kind, "synced");
});

test("still hydrating reads as syncing, not synced", () => {
  const { sandbox } = loadApp();
  assert.equal(sandbox.syncStatus({ ...base, hydrated: false }).kind, "syncing");
});

test("a lost NATS connection wins over every other signal", () => {
  const { sandbox } = loadApp();
  // Offline while also degraded and un-hydrated: the connection is the
  // fact that explains the other two, so it is what the pill must say.
  const st = sandbox.syncStatus({ ...base, connected: false, syncDegraded: true, hydrated: false });
  assert.equal(st.kind, "offline");
});

test("a healthy socket with a wedged sync manager reads stale, never synced", () => {
  const { sandbox } = loadApp();
  // The regression this guards: syncDegraded is invisible on the connectivity
  // axis, so folding on `connected` alone would paint a confident green over a
  // world that stopped applying deltas.
  const st = sandbox.syncStatus({ ...base, syncDegraded: true });
  assert.equal(st.kind, "stale");
  assert.notEqual(st.kind, "synced");
});

test("hydration does not outrank a degraded sync manager", () => {
  const { sandbox } = loadApp();
  assert.equal(sandbox.syncStatus({ ...base, syncDegraded: true, hydrated: true }).kind, "stale");
});

test("a status with no delta behind it claims no freshness", () => {
  const { sandbox } = loadApp();
  const st = sandbox.syncStatus({ ...base, hydrated: false, lastFrameAt: null });
  const detail = sandbox.syncStatusDetail(st, 5000);
  assert.match(detail, /waiting for your first rows/i);
  assert.doesNotMatch(detail, /\bago\b/, "no arrival means no 'ago' to state");
});

test("the detail dates the last change against a supplied clock", () => {
  const { sandbox } = loadApp();
  const st = sandbox.syncStatus({ ...base, lastFrameAt: 10_000 });
  assert.match(sandbox.syncStatusDetail(st, 15_000), /5s ago/);
});

test("renderSyncStatus drives the dot, label and title together", () => {
  const { sandbox, el, evaluate } = loadApp();
  evaluate("state.connected = false; state.lastFrameAt = 0;");
  sandbox.renderSyncStatus();

  assert.equal(el("sync-status-label").textContent, "Offline");
  assert.match(el("sync-status-dot").className, /\boffline\b/);
  assert.match(el("sync-status").className, /\boffline\b/);
  assert.equal(el("sync-status").hidden, false);
  assert.match(el("sync-status").title, /last change/i);
});

test("the connectivity frame records both of its axes", () => {
  const { evaluate } = loadApp();
  const feedHandlers = evaluate("feedHandlers");

  feedHandlers.connectivity({ connected: true, syncDegraded: true });
  assert.equal(evaluate("state.connected"), true);
  assert.equal(evaluate("state.syncDegraded"), true,
    "the degraded axis must reach state, not only its banner — the pill reads it");

  feedHandlers.connectivity({ connected: true, syncDegraded: false });
  assert.equal(evaluate("state.syncDegraded"), false);
});

test("a manifest frame stamps freshness", () => {
  const { evaluate } = loadApp();
  const feedHandlers = evaluate("feedHandlers");
  assert.equal(evaluate("state.lastFrameAt"), null);

  feedHandlers.manifest({ key: "manifest.me", data: {} });
  assert.ok(evaluate("state.lastFrameAt") > 0, "an applied delta is what freshness is measured from");
});
