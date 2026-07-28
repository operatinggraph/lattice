// Pins the boot screen's progress copy (facet-app-ux.md §3.0's spinner,
// given something real to say): the label counts distinct rows as they
// arrive, and a sublabel admits the wait once it outlasts the normal fast
// path. Same vm harness as sync_degraded_banner.test.mjs, plus a small fake
// timer queue (respects delay AND clearTimeout, unlike boot_gate.test.mjs's
// capture-only stub, since armBootTick's self-rescheduling loop needs real
// cancellation semantics to test). bootNow is a reassignable seam so
// elapsed time advances on command instead of a real wait — same shape as
// boot.mjs's createTokenRefresher(now).

import { test } from "node:test";
import assert from "node:assert/strict";
import vm from "node:vm";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const appSrc = fs.readFileSync(path.join(__dirname, "app.js"), "utf8");
const indexHtml = fs.readFileSync(path.join(__dirname, "index.html"), "utf8");

function loadApp() {
  const els = new Map();
  const el = (id) => {
    if (!els.has(id)) els.set(id, { id, hidden: true, textContent: "" });
    return els.get(id);
  };

  let fakeTime = 0;
  let nextHandle = 1;
  const timers = new Map(); // handle -> {fireAt, fn}
  const sandbox = {
    console,
    document: { addEventListener() {}, getElementById: el },
    setTimeout: (fn, ms) => {
      const handle = nextHandle++;
      timers.set(handle, { fireAt: fakeTime + ms, fn });
      return handle;
    },
    clearTimeout: (handle) => { timers.delete(handle); },
    // feedHandlers.manifest's scheduleRender() calls this — real
    // queueMicrotask is fine here since nothing in these tests depends on
    // renderView actually running (there is no real DOM to render into).
    queueMicrotask,
  };
  vm.createContext(sandbox);
  vm.runInContext(appSrc, sandbox, { filename: "app.js" });
  const evaluate = (src) => vm.runInContext(src, sandbox);
  evaluate("bootNow = () => __fakeNow; var __fakeNow = 0;");

  // advance jumps the fake clock forward ms and fires every timer due by
  // the new time (in fireAt order), including ones a fired callback
  // reschedules — armBootTick's self-reschedule always lands strictly
  // after the now-current fakeTime, so this terminates in one pass.
  function advance(ms) {
    fakeTime += ms;
    evaluate(`__fakeNow = ${fakeTime};`);
    for (;;) {
      let due = null;
      for (const [handle, t] of timers) {
        if (t.fireAt <= fakeTime && (!due || t.fireAt < due.t.fireAt)) due = { handle, t };
      }
      if (!due) break;
      timers.delete(due.handle);
      due.t.fn();
    }
  }

  // feedHandlers is a top-level `const` in app.js, so it never lands on the
  // sandbox object itself (same reason armSilenceFallback etc. are reached
  // by evaluating, not by property access) — pull the one live reference
  // out once, same object every call.
  const feedHandlers = evaluate("feedHandlers");
  return { sandbox, el, evaluate, advance, feedHandlers };
}

test("index.html declares the boot sublabel the ticker targets", () => {
  assert.match(indexHtml, /id="boot-sublabel"/);
});

test("the boot label stays generic until the first row lands", () => {
  const { el, feedHandlers } = loadApp();
  feedHandlers.open();
  assert.equal(el("boot-label").textContent, "Loading your world…");
  assert.equal(el("boot-sublabel").hidden, true, "8s have not passed yet");
});

test("the boot label counts distinct rows, not raw frames", () => {
  const { el, feedHandlers } = loadApp();
  feedHandlers.open();

  feedHandlers.manifest({ key: "manifest.me", data: {} });
  assert.equal(el("boot-label").textContent, "Loading your world… 1 item so far");

  feedHandlers.manifest({ key: "manifest.ent.a", data: {} });
  feedHandlers.manifest({ key: "manifest.ent.b", data: {} });
  assert.equal(el("boot-label").textContent, "Loading your world… 3 items so far");

  // A redelivered/duplicate frame for a key already seen — the per-actor
  // sync subject can replay a key many times over (an already-filed gap:
  // it retains every past hydrate burst, not just current rows) — must not
  // inflate the count the same way a genuinely new row does.
  feedHandlers.manifest({ key: "manifest.me", data: { changed: true } });
  assert.equal(el("boot-label").textContent, "Loading your world… 3 items so far",
    "re-delivery of a known key is not new progress");
});

test("a boot that runs long admits the wait instead of sitting silent", () => {
  const { el, evaluate, advance, feedHandlers } = loadApp();
  // Isolate from the gate's own release timing (boot_gate.test.mjs's job):
  // stub finishBoot so this test is only about what the screen SAYS while
  // still waiting, not about when the wait ends.
  evaluate("finishBoot = () => {};");
  feedHandlers.open();
  feedHandlers.manifest({ key: "manifest.me", data: {} });

  advance(8000);
  assert.equal(el("boot-sublabel").hidden, false);
  assert.match(el("boot-sublabel").textContent, /still syncing/i);
  assert.match(el("boot-sublabel").textContent, /8s/);

  // The row count already plateaued (one row, nothing new since), but the
  // tick loop keeps the clock moving on its own — the whole point being
  // that a long redundant-replay drain never again reads as a frozen tab.
  advance(5000);
  assert.match(el("boot-sublabel").textContent, /13s/);
});

test("a fast boot never shows the sublabel at all", () => {
  const { el, advance, feedHandlers } = loadApp();
  feedHandlers.open();
  feedHandlers.manifest({ key: "manifest.me", data: {} });
  advance(2000);
  assert.equal(el("boot-sublabel").hidden, true, "the common case finishes long before 8s");
});

test("finishBoot stops the tick loop — no post-boot writes to a hidden screen", () => {
  const { el, evaluate, advance, feedHandlers } = loadApp();
  feedHandlers.open();
  evaluate("finishBoot();");
  assert.equal(evaluate("hasBootstrapped"), true);

  const before = el("boot-label").textContent;
  advance(10000);
  assert.equal(el("boot-label").textContent, before, "a cancelled tick must not keep rendering");
  assert.equal(el("boot-sublabel").hidden, true, "and must not reveal the sublabel post-boot either");
});
