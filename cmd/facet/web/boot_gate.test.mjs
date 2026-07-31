// Pins the boot gate's release policy (facet-app-ux.md §10). The gate releases
// on sufficiency, and the two modes guard opposite failures: releasing the
// empty-mode gate early paints "No residence linked yet" as if it were the
// answer, while letting an arriving frame push the settle-mode deadline back
// holds the spinner for as long as the delta stream runs — which on a cold
// hydrate is minutes, long after the world itself is complete and renderable.
//
// Same vm harness as sync_degraded_banner.test.mjs: app.js is a plain browser
// script, so vm.runInContext hoists its function declarations onto the sandbox.
// Its `const` bindings (state, feedHandlers) are script-scoped rather than
// sandbox properties, so they are reached by evaluating in the same context.

import { test } from "node:test";
import assert from "node:assert/strict";
import vm from "node:vm";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const appSrc = fs.readFileSync(path.join(__dirname, "app.js"), "utf8");

function loadApp() {
  const scheduled = [];
  const cleared = [];
  const sandbox = {
    console,
    document: { addEventListener() {}, getElementById: () => ({ hidden: true, textContent: "" }) },
    setTimeout: (fn, ms) => { scheduled.push({ fn, ms }); return scheduled.length; },
    clearTimeout: (h) => { cleared.push(h); },
    queueMicrotask,
  };
  vm.createContext(sandbox);
  vm.runInContext(appSrc, sandbox, { filename: "app.js" });
  const evaluate = (src) => vm.runInContext(src, sandbox);
  // Only the gate's own timers matter here; the boot-label ticker also
  // schedules, so select by the delays the gate actually uses.
  const gateDelays = () => {
    const gate = [sandbox.bootGateDelay(true), sandbox.bootGateDelay(false)];
    return scheduled.filter((s) => gate.includes(s.ms)).map((s) => s.ms);
  };
  return {
    sandbox,
    scheduled,
    evaluate,
    gateDelays,
    lastGateDelay: () => gateDelays()[gateDelays().length - 1],
    feedHandlers: evaluate("feedHandlers"),
  };
}

test("the empty-mode delay outlasts a real hydration; the settle one does not wait", () => {
  const { sandbox } = loadApp();
  assert.equal(typeof sandbox.bootGateDelay, "function");

  const empty = sandbox.bootGateDelay(false);
  const settle = sandbox.bootGateDelay(true);

  assert.ok(settle < empty, "a world already on screen must not wait as long as one still arriving");
  assert.ok(empty >= 30000, `a fresh sign-in hydrates for ~20-30s; ${empty}ms would release mid-catch-up`);
  assert.ok(settle <= 2000, `a settle window of ${settle}ms is a stall, not a burst landing together`);
});

test("a fresh sign-in arms the empty gate — nothing has arrived yet", () => {
  const { sandbox, evaluate, lastGateDelay } = loadApp();
  assert.equal(evaluate("state.rows.size"), 0);
  assert.equal(evaluate("state.hydrated"), false, "an unheard-from engine is assumed still hydrating");

  sandbox.armBootGate();
  assert.equal(lastGateDelay(), sandbox.bootGateDelay(false));
});

test("a snapshot that carried rows arms the settle gate — a warm reload does not stall", () => {
  const { sandbox, evaluate, lastGateDelay } = loadApp();
  evaluate("state.rows.set('vtx.unit.aaaaaaaaaaaaaaaaaaaa.manifest', { data: {} })");

  sandbox.armBootGate();
  assert.equal(lastGateDelay(), sandbox.bootGateDelay(true),
    "rows on screen mean the only thing left to wait out is the rest of the burst");
});

test("a replayed ready arms the settle gate even with an empty world", () => {
  const { sandbox, evaluate, lastGateDelay } = loadApp();
  // The honest empty world: hydration finished and there is genuinely nothing.
  // finishBoot is stubbed out because the release itself is not under test here
  // — only that `ready` is what records hydration.
  evaluate("finishBoot = () => {}");
  evaluate("feedHandlers.ready()");
  assert.equal(evaluate("state.hydrated"), true);

  sandbox.armBootGate();
  assert.equal(lastGateDelay(), sandbox.bootGateDelay(true),
    "an engine that reported done must not be held behind the hydrating net");
});

test("the first row re-arms the empty gate down to settle — exactly once", () => {
  const { sandbox, evaluate, gateDelays, feedHandlers } = loadApp();
  evaluate("finishBoot = () => {}");

  feedHandlers.open();
  assert.deepEqual(gateDelays(), [sandbox.bootGateDelay(false)], "no rows yet — the long gate");

  feedHandlers.manifest({ key: "manifest.me", data: {} });
  assert.deepEqual(gateDelays(), [sandbox.bootGateDelay(false), sandbox.bootGateDelay(true)],
    "the first row is what makes the world renderable, so it shortens the deadline");
});

test("a continuous stream never postpones the settle deadline", () => {
  const { sandbox, evaluate, gateDelays, feedHandlers } = loadApp();
  evaluate("finishBoot = () => {}");

  feedHandlers.open();
  feedHandlers.manifest({ key: "manifest.me", data: {} });
  const afterFirstRow = gateDelays().length;

  // A cold hydrate streams deltas without pause. Every one of these would have
  // pushed the paint back under a per-frame re-arm; none may schedule anything.
  for (let i = 0; i < 500; i++) {
    feedHandlers.manifest({ key: "manifest.ent." + i, data: {} });
  }
  assert.equal(gateDelays().length, afterFirstRow,
    "a frame arriving while the settle gate is pending must not re-arm it");
});
