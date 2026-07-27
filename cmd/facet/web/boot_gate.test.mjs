// Pins the boot gate's release policy (facet-app-ux.md §10). The gate closes
// on silence, and silence means two opposite things: after the world has
// arrived it says the snapshot burst is over, but on a fresh sign-in it says
// the engine is still hydrating — tens of seconds before its first row. The
// regression this guards is releasing on the short window in the second case,
// which paints an empty Home as if it were the answer.
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
  const sandbox = {
    console,
    document: { addEventListener() {}, getElementById: () => ({ hidden: true, textContent: "" }) },
    setTimeout: (fn, ms) => { scheduled.push({ fn, ms }); return scheduled.length; },
    clearTimeout: () => {},
  };
  vm.createContext(sandbox);
  vm.runInContext(appSrc, sandbox, { filename: "app.js" });
  const evaluate = (src) => vm.runInContext(src, sandbox);
  return { sandbox, scheduled, evaluate, lastDelay: () => scheduled[scheduled.length - 1].ms };
}

test("the cold-boot delay outlasts a real hydration; the warm one does not wait", () => {
  const { sandbox } = loadApp();
  assert.equal(typeof sandbox.bootGateDelay, "function");

  const cold = sandbox.bootGateDelay(false);
  const warm = sandbox.bootGateDelay(true);

  assert.ok(warm < cold, "a world already on screen must not wait as long as one still arriving");
  assert.ok(cold >= 30000, `a fresh sign-in hydrates for ~20-30s; ${cold}ms would release mid-catch-up`);
  assert.ok(warm <= 5000, `a burst-over window of ${warm}ms is a stall, not a quiet period`);
});

test("a fresh sign-in arms the long gate — nothing has arrived yet", () => {
  const { sandbox, lastDelay, evaluate } = loadApp();
  assert.equal(evaluate("state.rows.size"), 0);
  assert.equal(evaluate("state.hydrated"), false, "an unheard-from engine is assumed still hydrating");

  sandbox.armSilenceFallback();
  assert.equal(lastDelay(), sandbox.bootGateDelay(false));
});

test("a snapshot that carried rows arms the short gate — a warm reload does not stall", () => {
  const { sandbox, lastDelay, evaluate } = loadApp();
  evaluate("state.rows.set('vtx.unit.aaaaaaaaaaaaaaaaaaaa.manifest', { data: {} })");

  sandbox.armSilenceFallback();
  assert.equal(lastDelay(), sandbox.bootGateDelay(true),
    "rows on screen mean the only thing left to wait out is the rest of the burst");
});

test("a replayed ready arms the short gate even with an empty world", () => {
  const { sandbox, lastDelay, evaluate } = loadApp();
  // The honest empty world: hydration finished and there is genuinely nothing.
  // finishBoot is stubbed out because the release itself is not under test here
  // — only that `ready` is what records hydration.
  evaluate("finishBoot = () => {}");
  evaluate("feedHandlers.ready()");
  assert.equal(evaluate("state.hydrated"), true);

  sandbox.armSilenceFallback();
  assert.equal(lastDelay(), sandbox.bootGateDelay(true),
    "an engine that reported done must not be held behind the hydrating net");
});
