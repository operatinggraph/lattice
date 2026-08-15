// Regression tests for the mint-and-reveal ceremony
// (client-ceremony-op-descriptors-design.md §4.3): an op whose descriptor
// carries an OpCeremonySpec has its minted-hash field FILLED by the client
// rather than rendered, and its plaintext revealed exactly once — only after
// the write is confirmed.
//
// Every assertion here is written in the direction that can regress. The
// failure modes this ceremony exists to prevent are all fail-OPEN ones: a
// client that renders the hash field accepts whatever a person types and arms
// a secret nobody holds; a client that reveals eagerly hands out a secret for
// a write that was rejected. So the tests assert absence and non-reveal, not
// just the happy path.
//
// Same harness as degraded_render.test.mjs / descriptor_autofill.test.mjs:
// app.js is a plain browser script, so vm.runInContext hoists its function
// declarations onto the sandbox global (top-level const/let stay lexical).
// That is why the reveal lifecycle is exercised through holdCeremonyReveal /
// settleCeremonyReveal rather than by reaching into the pendingReveals map.

import { test } from "node:test";
import assert from "node:assert/strict";
import vm from "node:vm";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { webcrypto } from "node:crypto";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const appSrc = fs.readFileSync(path.join(__dirname, "app.js"), "utf8");

// loadApp evaluates app.js with a crypto implementation of the caller's
// choosing — `webcrypto` for a capable runtime, `undefined` for the insecure
// origin where crypto.subtle does not exist — and, optionally, a sessionStorage
// to persist ceremony holds into. Omitting the storage is the runtime that has
// none: every test that does not name one is also the assertion that the app
// works unchanged without it.
// `extra` overlays the sandbox for the vectors that drive more of app.js than
// the reveal lifecycle alone — timers, a microtask, a DOM node to render into.
function loadApp(crypto, sessionStorage, extra) {
  const sandbox = {
    console,
    document: { addEventListener() {} },
    crypto,
    TextEncoder,
    // scheduleRender's scheduler. Synchronous here, and it renders nothing —
    // hasBootstrapped is false in a headless load — so it is just the global
    // the call needs to resolve.
    queueMicrotask: (fn) => fn(),
    ...extra,
  };
  if (sessionStorage) sandbox.sessionStorage = sessionStorage;
  vm.createContext(sandbox);
  vm.runInContext(appSrc, sandbox, { filename: "app.js" });
  return sandbox;
}

// fakeSessionStorage is the Web Storage surface app.js actually uses. Handing
// the SAME object to two loadApp calls is what makes a "reload" testable: two
// independent script evaluations, one tab's storage.
function fakeSessionStorage() {
  const store = new Map();
  return {
    getItem: (k) => (store.has(k) ? store.get(k) : null),
    setItem: (k, v) => { store.set(k, String(v)); },
    removeItem: (k) => { store.delete(k); },
  };
}

const REVEALS_KEY = "facet.pendingReveals";

// frameHarness drives the real applyOutboxFrame / renderActivity, rather than
// settleCeremonyReveal on its own. That distinction is the point: every part of
// this signal that regressed under review — which object carries the loss,
// whether the note is ever rendered — lives in the WIRING between them, and a
// test that calls settle directly cannot see any of it.
//
// The sandbox supplies only what those two paths reach for: a capturable
// setTimeout (the 2-second archive hand-off, driven by hand here), a
// synchronous microtask for scheduleRender, and one element for Activity to
// render into. The rest are function declarations, so they are stubbed on the
// sandbox global after load.
function frameHarness() {
  const timers = [];
  const activity = { innerHTML: "" };
  const toasts = [];
  const app = loadApp(webcrypto, undefined, {
    setTimeout: (fn) => timers.push(fn),
    clearTimeout: () => {},
    queueMicrotask: (fn) => fn(),
    document: {
      addEventListener() {},
      getElementById: (id) => (id === "view-activity" ? activity : { innerHTML: "", hidden: true }),
    },
  });
  app.toast = (msg) => toasts.push(msg);
  app.showModal = () => {};
  app.ops = () => [];
  app.paneRows = () => [];
  return {
    app, toasts,
    // The 2s archive timer, fired on demand — the only way to assert what it
    // does and does not sweep without waiting on a real clock.
    runTimers: () => { timers.splice(0).forEach((fn) => fn()); },
    render: () => { app.renderActivity(); return activity.innerHTML; },
    // `state` is a top-level const, so it is lexical to the context rather than
    // a sandbox property — reached by evaluating in the same context. Round
    // -tripped through JSON so the array carries THIS realm's prototype and
    // deepEqual compares by structure.
    pinnedIds: () => JSON.parse(vm.runInContext("JSON.stringify([...state.outbox.keys()])", app)),
    historyLen: () => vm.runInContext("state.outboxHistory.length", app),
  };
}

// frame builds an outbox frame in feed.go's shape. Each call returns a NEW
// object, which is the shape of the hazard: the host marshals from a shared
// pointer, so several distinct objects arrive for one requestId.
function frame(requestId, state) {
  return {
    requestId,
    operationType: "CreateUnclaimedIdentity",
    state,
    createdAt: "2026-01-01T00:00:00.000Z",
  };
}

const ceremonyOp = {
  operationType: "CreateUnclaimedIdentity",
  title: "Create an identity",
  submitLabel: "Create identity",
  dispatchClass: "identity",
  dispatchAuthContext: "standing",
  ceremonyMintedSecretHashField: "claimKeyHash",
  ceremonyRevealTitle: "Their claim secret — shown once",
  ceremonyRevealHelp: "Give this to them now.",
  inputSchema: JSON.stringify({
    type: "object",
    properties: {
      name: { type: "string", title: "Full name" },
      email: { type: "string", title: "Email" },
      claimKeyHash: { type: "string", title: "Claim secret hash" },
    },
    required: ["name", "claimKeyHash"],
  }),
};

// ---- rule 1: no ceremony support ⇒ the op is not offered ----

test("opButton withholds a ceremony op when the runtime cannot mint", () => {
  const app = loadApp(undefined); // no crypto at all — an insecure origin
  assert.equal(app.ceremonySupported(), false);

  const html = app.opButton({ key: "vtx.meta.AAAAAAAAAAAAAAAAAAAA", data: ceremonyOp }, {});
  assert.match(html, /degraded-card/);
  assert.match(html, /Create an identity/);
  // The whole point: no button, and no rendered form behind it.
  assert.doesNotMatch(html, /data-open-op/);
});

test("opButton withholds a ceremony op when subtle crypto is missing but getRandomValues is not", () => {
  // The realistic half-capable shape — an insecure origin still exposes
  // getRandomValues. Minting without a hash is not a ceremony.
  const app = loadApp({ getRandomValues: () => {} });
  assert.equal(app.ceremonySupported(), false);
  assert.match(app.opButton({ key: "vtx.meta.AAAAAAAAAAAAAAAAAAAA", data: ceremonyOp }, {}), /degraded-card/);
});

test("opButton offers the same op on a runtime that can mint", () => {
  const app = loadApp(webcrypto);
  assert.equal(app.ceremonySupported(), true);
  const html = app.opButton({ key: "vtx.meta.AAAAAAAAAAAAAAAAAAAA", data: ceremonyOp }, {});
  assert.match(html, /data-open-op="vtx\.meta\.AAAAAAAAAAAAAAAAAAAA"/);
  assert.doesNotMatch(html, /degraded-card/);
});

test("an op declaring no ceremony is unaffected by ceremony support", () => {
  const plain = { ...ceremonyOp };
  delete plain.ceremonyMintedSecretHashField;
  const app = loadApp(undefined);
  assert.equal(app.ceremonyField(plain), "");
  assert.doesNotMatch(app.opButton({ key: "vtx.meta.BBBBBBBBBBBBBBBBBBBB", data: plain }, {}), /degraded-card/);
});

// ---- rule 2: the hash field is filled, never rendered ----

// renderReal drives the REAL renderDescriptorForm and returns the HTML it
// produced. `$` resolves through document.getElementById, so a stub document
// is enough to satisfy the form-wiring tail without a DOM — this asserts
// against what renderDescriptorForm actually emits rather than against a copy
// of its filter written in the test, which would pass even if the renderer
// stopped filtering.
function renderReal(app, op) {
  let html = "";
  app.showModal = (h) => { html = h; };
  app.document.getElementById = () => ({ addEventListener() {} });
  app.renderDescriptorForm(op, "vtx.meta.AAAAAAAAAAAAAAAAAAAA", {}, {});
  return html;
}

test("renderDescriptorForm omits the minted-hash field and keeps the rest", () => {
  const app = loadApp(webcrypto);
  const html = renderReal(app, ceremonyOp);

  // The ordinary fields still render...
  assert.match(html, /name="name"/);
  assert.match(html, /name="email"/);
  // ...and the minted one does not, in any control shape.
  assert.doesNotMatch(html, /name="claimKeyHash"/,
    "a rendered claimKeyHash is a text box asking a human to type a hash whose preimage nobody holds");
  assert.doesNotMatch(html, /Claim secret hash/);
});

test("renderDescriptorForm DOES render that same field when no ceremony claims it", () => {
  // The control for the assertion above: without the ceremony the field is an
  // ordinary property and renders, so the test above is pinning the ceremony's
  // effect rather than some unrelated reason the field never appears.
  const app = loadApp(webcrypto);
  const noCeremony = { ...ceremonyOp };
  delete noCeremony.ceremonyMintedSecretHashField;
  assert.match(renderReal(app, noCeremony), /name="claimKeyHash"/);
});

test("mintSecret produces a 32-byte hex plaintext and its sha256 hex, and never repeats", async () => {
  const app = loadApp(webcrypto);
  const [plaintext, hash] = await app.mintSecret();
  assert.match(plaintext, /^[0-9a-f]{64}$/); // 32 bytes, hex
  assert.match(hash, /^[0-9a-f]{64}$/);      // sha256, hex
  assert.notEqual(plaintext, hash, "the hash must not be the plaintext");

  // The hash is genuinely sha256 OF the plaintext — the property the whole
  // ceremony rests on, since ClaimIdentity later compares exactly this.
  const expected = Buffer.from(
    await webcrypto.subtle.digest("SHA-256", new TextEncoder().encode(plaintext))).toString("hex");
  assert.equal(hash, expected);

  const [second] = await app.mintSecret();
  assert.notEqual(plaintext, second);
});

// ---- rule 2, the other half: the hash IS submitted, and it is the hash OF
// the plaintext this submission will reveal ----
//
// This is the coupling with no other check anywhere. If the two ever diverge —
// a refactor that re-mints, or reveals a stale object — the person is handed a
// secret that does not match the stored hash and the identity becomes
// permanently unclaimable, with no error on any surface.

// submitCapture drives the real submitDescriptorForm against a stubbed write
// seam and a stubbed form, and returns the enqueued request plus whatever was
// revealed once the request is confirmed.
async function submitCapture(app, op, formValues) {
  let enqueued = null;
  app.enqueueOperation = (req) => { enqueued = req; return Promise.resolve({ requestId: "req-capture" }); };
  app.me = () => ({ identityKey: "vtx.identity.DDDDDDDDDDDDDDDDDDDD", selfAnchors: [] });
  app.toast = () => {};
  app.hideModal = () => {};
  const shown = [];
  app.showModal = (h) => shown.push(h);

  const schema = JSON.parse(op.inputSchema);
  const fieldNames = Object.keys(schema.properties).filter((f) => f !== app.ceremonyField(op));
  const form = {
    elements: { namedItem: (n) => (n in formValues ? { value: formValues[n] } : null) },
    querySelector: () => null,
  };
  await app.submitDescriptorForm(form, op, "vtx.meta.AAAAAAAAAAAAAAAAAAAA", {},
    fieldNames, schema.properties, {}, new Set(schema.required || []));
  return { enqueued, shown };
}

test("the submitted payload carries the hash OF the plaintext that gets revealed", async () => {
  const app = loadApp(webcrypto);
  const { enqueued, shown } = await submitCapture(app, ceremonyOp, { name: "Ada", email: "ada@example.com" });

  assert.ok(enqueued, "the form must have enqueued a request");
  assert.equal(enqueued.operationType, "CreateUnclaimedIdentity");
  // The ordinary fields went through...
  assert.equal(enqueued.payload.name, "Ada");
  // ...and the minted field is present in the PAYLOAD even though it was
  // never in the form.
  assert.match(enqueued.payload.claimKeyHash, /^[0-9a-f]{64}$/);

  // Nothing has been revealed yet — the write has not settled.
  assert.deepEqual(shown, []);
  app.settleCeremonyReveal("req-capture", "confirmed");
  assert.equal(shown.length, 1);

  // The revealed plaintext must hash to exactly what was submitted. This is
  // the assertion the whole ceremony rests on.
  const revealed = shown[0].match(/data-reveal-secret>([0-9a-f]{64})</);
  assert.ok(revealed, "the reveal must show a 64-hex plaintext");
  const expected = Buffer.from(
    await webcrypto.subtle.digest("SHA-256", new TextEncoder().encode(revealed[1]))).toString("hex");
  assert.equal(enqueued.payload.claimKeyHash, expected,
    "the stored hash must be the hash of the secret handed over, or the identity is unclaimable");

  // And the plaintext itself never left the browser.
  assert.ok(!JSON.stringify(enqueued).includes(revealed[1]),
    "the minted plaintext must not appear anywhere in the enqueued request");
});

test("a descriptor naming one field as both target and minted secret is refused", async () => {
  const app = loadApp(webcrypto);
  const broken = { ...ceremonyOp, dispatchTargetField: "claimKeyHash", dispatchTargetType: "identity" };
  const { enqueued } = await submitCapture(app, broken, { name: "Ada" });
  assert.equal(enqueued, null, "the mint would overwrite the target and be declared as a read key");
});

// ---- rule 3: reveal on confirmed, discard silently on anything else ----

// revealCapture swaps showModal for a recorder, so the reveal path can be
// driven without a DOM. showModal is a function declaration, so it is on the
// sandbox global and reassignable.
function revealCapture(app) {
  const shown = [];
  app.showModal = (html) => shown.push(html);
  // A rejected settle now TELLS the user no secret was issued, rather than
  // leaving them waiting on a modal that will never arrive.
  app.toast = () => {};
  return shown;
}

test("a confirmed write reveals the plaintext exactly once", () => {
  const app = loadApp(webcrypto);
  const shown = revealCapture(app);

  app.holdCeremonyReveal("req-1", {
    plaintext: "abc123", title: "Their claim secret", help: "Give this to them now.",
  });
  app.settleCeremonyReveal("req-1", "confirmed");

  assert.equal(shown.length, 1);
  assert.match(shown[0], /abc123/);
  assert.match(shown[0], /Their claim secret/);
  assert.match(shown[0], /Give this to them now\./);

  // A second frame for the same request must not re-reveal: the plaintext is
  // gone from the hold, so a duplicate or replayed frame shows nothing.
  app.settleCeremonyReveal("req-1", "confirmed");
  assert.equal(shown.length, 1);
});

test("a rejected write reveals NOTHING and drops the plaintext", () => {
  const app = loadApp(webcrypto);
  const shown = revealCapture(app);

  app.holdCeremonyReveal("req-2", { plaintext: "shouldNeverAppear", title: "t", help: "h" });
  app.settleCeremonyReveal("req-2", "rejected");
  assert.deepEqual(shown, [], "a secret for a write that did not land must never be handed over");

  // And it is dropped, not merely withheld — a later confirmed frame for the
  // same request cannot resurrect it.
  app.settleCeremonyReveal("req-2", "confirmed");
  assert.deepEqual(shown, []);
});

test("an in-flight write reveals nothing until it settles", () => {
  const app = loadApp(webcrypto);
  const shown = revealCapture(app);

  app.holdCeremonyReveal("req-3", { plaintext: "notYet", title: "t", help: "h" });
  for (const interim of ["queued", "submitting"]) {
    app.settleCeremonyReveal("req-3", interim);
    assert.deepEqual(shown, [], `${interim} is not an outcome`);
  }
  // Still held, so the eventual confirmation still reveals.
  app.settleCeremonyReveal("req-3", "confirmed");
  assert.equal(shown.length, 1);
  assert.match(shown[0], /notYet/);
});

test("a second reveal does not destroy an un-dismissed first", () => {
  // showModal replaces #modal-root wholesale, so a naive second reveal would
  // erase the first — the same "the next one clears it" failure that ruled
  // out a toast. Two confirmations can land back to back (two creates, or a
  // double submit), and the person must still be able to read both.
  const app = loadApp(webcrypto);
  const shown = revealCapture(app);

  app.holdCeremonyReveal("req-a", { plaintext: "aaaa1111", title: "First", help: "h" });
  app.holdCeremonyReveal("req-b", { plaintext: "bbbb2222", title: "Second", help: "h" });
  app.settleCeremonyReveal("req-a", "confirmed");
  app.settleCeremonyReveal("req-b", "confirmed");

  const latest = shown[shown.length - 1];
  assert.match(latest, /aaaa1111/, "the first secret must survive the second reveal");
  assert.match(latest, /bbbb2222/);
});

test("an outbox frame for a request with no held secret is inert", () => {
  const app = loadApp(webcrypto);
  const shown = revealCapture(app);
  app.settleCeremonyReveal("some-ordinary-write", "confirmed");
  assert.deepEqual(shown, []);
});

// ---- rule 4: a secret that is confirmed but unrecoverable is never silent,
// and an ordinary reload does not make one ----
//
// The failure these pin is the quietest one in the ceremony: the write is
// durable in the device's queue the moment it is enqueued, so it lands whether
// or not this browser still holds the plaintext. A settlement that finds
// nothing held therefore has two utterly different meanings — an ordinary
// write (inert, pinned above) and a ceremony write whose secret is gone (the
// target is armed with a secret nobody has). They must not look alike.

test("a purged reveal that later CONFIRMS reports the loss instead of going quiet", () => {
  const app = loadApp(webcrypto);
  const shown = revealCapture(app);

  app.holdCeremonyReveal("req-purged", { plaintext: "goneForever", title: "Their claim secret", help: "h" });
  app.purgeCeremonyReveals(); // the §4.4 sign-out wipe
  const outcome = app.settleCeremonyReveal("req-purged", "confirmed");

  assert.equal(outcome, "lost", "a confirmed write with no recoverable secret must announce itself");
  assert.deepEqual(shown, [], "there is no plaintext left to show — and the purge is not weakened");
  assert.ok(!JSON.stringify(shown).includes("goneForever"));

  // Reported once. A replayed frame for the same request has nothing to add.
  assert.equal(app.settleCeremonyReveal("req-purged", "confirmed"), undefined);
});

test("a purged reveal that later REJECTS is quiet — nothing was ever going to be needed", () => {
  const app = loadApp(webcrypto);
  const shown = revealCapture(app);
  const toasts = [];
  app.toast = (msg) => toasts.push(msg);

  app.holdCeremonyReveal("req-purged-rej", { plaintext: "goneForever", title: "t", help: "h" });
  app.purgeCeremonyReveals();

  assert.equal(app.settleCeremonyReveal("req-purged-rej", "rejected"), undefined);
  assert.deepEqual(shown, []);
  assert.deepEqual(toasts, [], "no secret was issued and none was owed — there is nothing to say");
  // And the marker is spent: a later frame cannot resurrect a report either.
  assert.equal(app.settleCeremonyReveal("req-purged-rej", "confirmed"), undefined);
});

test("a purged reveal still held in a non-terminal state keeps its marker", () => {
  const app = loadApp(webcrypto);
  revealCapture(app);
  app.holdCeremonyReveal("req-purged-interim", { plaintext: "goneForever", title: "t", help: "h" });
  app.purgeCeremonyReveals();
  assert.equal(app.settleCeremonyReveal("req-purged-interim", "queued"), undefined);
  // The eventual confirmation still reports, which is the whole point of
  // holding the marker rather than deleting it.
  assert.equal(app.settleCeremonyReveal("req-purged-interim", "confirmed"), "lost");
});

test("a reload recovers a hold and still reveals the original plaintext", () => {
  // Two evaluations of app.js over one tab's storage — a page refresh between
  // enqueue and confirmation. The write survived it in the device queue and its
  // confirm frame replays onto the new feed connection; the secret must survive
  // it too, or the reload alone silently arms an unusable identity.
  const storage = fakeSessionStorage();

  const first = loadApp(webcrypto, storage);
  revealCapture(first);
  first.holdCeremonyReveal("req-reload", {
    plaintext: "survivesTheReload", title: "Their claim secret", help: "Give this to them now.",
  });
  assert.ok(storage.getItem("facet.pendingReveals"), "the hold must be mirrored where a reload can find it");

  const second = loadApp(webcrypto, storage);
  const shown = revealCapture(second);
  second.settleCeremonyReveal("req-reload", "confirmed");

  assert.equal(shown.length, 1, "the confirmation after the reload must still reveal");
  assert.match(shown[0], /survivesTheReload/);
  assert.match(shown[0], /Their claim secret/);
  // Settled means spent, in storage as well as in memory: the next load must
  // not re-reveal a secret already handed over.
  assert.equal(storage.getItem("facet.pendingReveals"), null);
  const third = loadApp(webcrypto, storage);
  const shownThird = revealCapture(third);
  third.settleCeremonyReveal("req-reload", "confirmed");
  assert.deepEqual(shownThird, []);
});

test("a reload after a sign-out purge recovers the marker, not the plaintext", () => {
  const storage = fakeSessionStorage();
  const first = loadApp(webcrypto, storage);
  revealCapture(first);
  first.holdCeremonyReveal("req-purged-reload", { plaintext: "mustNotSurvive", title: "T", help: "h" });
  first.purgeCeremonyReveals();
  assert.ok(!(storage.getItem("facet.pendingReveals") || "").includes("mustNotSurvive"),
    "the §4.4 purge must drop the plaintext from storage too, not only from memory");

  const second = loadApp(webcrypto, storage);
  const shown = revealCapture(second);
  assert.equal(second.settleCeremonyReveal("req-purged-reload", "confirmed"), "lost");
  assert.deepEqual(shown, []);
});

test("the reveal lifecycle works, and persistence is inert, with no sessionStorage at all", () => {
  // The sandbox defines none, which is also every other test in this file.
  const app = loadApp(webcrypto);
  const shown = revealCapture(app);
  app.persistPendingReveals(); // must not throw with nothing to persist...
  app.holdCeremonyReveal("req-nostore", { plaintext: "stillWorks", title: "t", help: "h" });
  app.persistPendingReveals(); // ...nor with something to persist and nowhere to put it.
  app.loadStoredReveals();
  app.settleCeremonyReveal("req-nostore", "confirmed");
  assert.equal(shown.length, 1);
  assert.match(shown[0], /stillWorks/);
});

test("the outbox cards carry a lost-secret note only when the REQUEST is marked", () => {
  const app = loadApp(webcrypto);
  app.ops = () => [];
  app.markSecretLost("r-lost");
  // Two DIFFERENT objects for the marked request, one of them a bare frame with
  // no ceremony field on it at all: the note follows the requestId, so both
  // carry it. That is the property a flag on the entry object cannot have.
  const marked = { requestId: "r-lost", operationType: "CreateUnclaimedIdentity", state: "confirmed" };
  const markedDup = { requestId: "r-lost", operationType: "CreateUnclaimedIdentity", state: "queued" };
  const plain = { requestId: "r-ok", operationType: "CreateUnclaimedIdentity", state: "confirmed" };
  for (const card of [app.outboxCard, app.outboxHistoryCard]) {
    assert.match(card(marked), /Secret was not shown — issue a new one\./);
    assert.match(card(markedDup), /Secret was not shown — issue a new one\./);
    assert.doesNotMatch(card(plain), /Secret was not shown/);
  }
  // Dismissing is the operator answering the notice, and it takes the mark with
  // it — otherwise the next duplicate frame re-pins what they just cleared.
  app.dismissOutbox("r-lost");
  assert.doesNotMatch(app.outboxCard(marked), /Secret was not shown/);
});

// ---- rule 5: the loss notice survives the wiring ----
//
// Everything above proves settleCeremonyReveal REPORTS a loss. These prove the
// report reaches a person, which is a claim about applyOutboxFrame and
// renderActivity that calling settle directly cannot make. Two ways to hold a
// correct report and still show nobody: attach it to a frame object the host
// routinely replaces, or render it only on a card the main Activity view never
// draws. Both are silent, and both look fine read off the settle path alone.

test("a duplicate outbox frame cannot erase the lost-secret notice", () => {
  const h = frameHarness();
  h.app.holdCeremonyReveal("req-burst", { plaintext: "goneForever", title: "T", help: "h" });
  h.app.purgeCeremonyReveals();

  h.app.applyOutboxFrame(frame("req-burst", "confirmed"));
  assert.equal(h.toasts.length, 1, "the confirmation must announce the loss once");
  assert.match(h.render(), /Secret was not shown/);

  // The burst feed.go actually sends: more frames for the SAME request,
  // distinct objects, marshaled from the shared pointer around the flip. Each
  // replaces the entry in state.outbox, and settle has nothing left to say
  // about any of them — the marker was spent on the first.
  h.app.applyOutboxFrame(frame("req-burst", "queued"));
  h.app.applyOutboxFrame(frame("req-burst", "confirmed"));
  assert.equal(h.toasts.length, 1, "and exactly once — a duplicate is not a second loss");
  assert.match(h.render(), /Secret was not shown/,
    "the notice belongs to the request; a replacement frame object must not take it down");
});

test("a lost-secret entry stays pinned in Activity instead of being archived", () => {
  const h = frameHarness();
  h.app.holdCeremonyReveal("req-pinned", { plaintext: "goneForever", title: "T", help: "h" });
  h.app.purgeCeremonyReveals();
  h.app.applyOutboxFrame(frame("req-pinned", "confirmed"));

  // The 2s hand-off that sweeps an ordinary confirmed entry into the collapsed
  // history section. It must leave this one alone: history is behind a click
  // nobody has a reason to make, and this is the only standing record that
  // somebody is armed with a secret no one holds.
  h.runTimers();
  assert.deepEqual(h.pinnedIds(), ["req-pinned"], "a lost secret must not be swept out of the pinned set");
  assert.equal(h.historyLen(), 0);

  const html = h.render();
  assert.match(html, /Secret was not shown — issue a new one\./);
  assert.match(html, /data-dismiss="req-pinned"/, "and it must be answerable, the way a failed write is");
  assert.doesNotMatch(html, /data-review="req-pinned"/,
    "but not resubmittable — the write LANDED; reopening its payload as a draft would misdescribe it");

  // Dismiss is the operator saying they have issued a fresh secret. It clears.
  h.app.dismissOutbox("req-pinned");
  assert.deepEqual(h.pinnedIds(), []);
  assert.doesNotMatch(h.render(), /Secret was not shown/);
});

test("an ordinary confirmed write is still archived on the timer", () => {
  // The control for the test above: without this, a filter that pinned
  // everything would pass it just as well.
  const h = frameHarness();
  h.app.applyOutboxFrame(frame("req-ordinary", "confirmed"));
  h.runTimers();
  assert.deepEqual(h.pinnedIds(), [], "nothing was lost, so nothing stays pinned");
  assert.equal(h.historyLen(), 1);
  assert.doesNotMatch(h.render(), /Secret was not shown/);
});

// ---- rule 6: the purge follows the ACCESS, and the storage cannot brick the
// page ----

test("an auth-death exit purges the held plaintext, not just the sign-out button", () => {
  // onAuthDeath is the exit nobody chose — an expired session, a 401. It never
  // calls /api/logout, so the queued write survives on the host and WILL
  // confirm later; what must not survive is this identity's plaintext. And
  // sessionStorage is same-TAB, so the location.replace it ends with does not
  // clear it: without the purge the secret rehydrates onto whoever signs in
  // next on a shared device.
  const storage = fakeSessionStorage();
  const replaced = [];
  const first = loadApp(webcrypto, storage, { location: { replace: (u) => replaced.push(u) } });
  revealCapture(first);
  first.holdCeremonyReveal("req-authdeath", { plaintext: "mustNotSurvive", title: "T", help: "h" });

  first.onAuthDeath();
  assert.deepEqual(replaced, ["/login"], "the exit itself still happens");
  assert.ok(!(storage.getItem(REVEALS_KEY) || "").includes("mustNotSurvive"),
    "an expired session must drop the plaintext exactly as a deliberate sign-out does");

  // The next load of this tab — the next identity on a kiosk — finds no secret,
  // and the marker still reports the loss when the write confirms.
  const second = loadApp(webcrypto, storage);
  const shown = revealCapture(second);
  assert.equal(second.settleCeremonyReveal("req-authdeath", "confirmed"), "lost");
  assert.deepEqual(shown, []);
});

test("a sessionStorage whose getter THROWS does not stop the app booting", () => {
  // Site data blocked, or a partitioned frame: `sessionStorage` resolves and
  // its getter raises SecurityError, so `typeof sessionStorage` throws rather
  // than answering "undefined". Defined on the context's own globalThis, since
  // that is where the identifier resolves — a plain sandbox property is read
  // through the vm's global proxy, which swallows the throw.
  //
  // loadStoredReveals runs at the top level of a classic script, so an escaping
  // throw does not merely lose reload recovery: the rest of app.js never
  // evaluates and the app does not boot.
  const sandbox = { console, document: { addEventListener() {} }, crypto: webcrypto, TextEncoder };
  vm.createContext(sandbox);
  vm.runInContext(
    `Object.defineProperty(globalThis, "sessionStorage", { get() { throw new Error("SecurityError"); }, configurable: true });`,
    sandbox,
  );
  assert.doesNotThrow(() => vm.runInContext(appSrc, sandbox, { filename: "app.js" }));
  // Top-level state declared AFTER loadStoredReveals runs — its existence is
  // the proof that evaluation reached the end of the file.
  assert.equal(vm.runInContext("shownReveals.length", sandbox), 0);

  // And the write paths stay callable, which is what keeps sign-out working:
  // purgeCeremonyReveals persists, and signOut calls it before /api/logout, so
  // a throw here would abandon the sign-out with the session still live.
  const shown = revealCapture(sandbox);
  assert.doesNotThrow(() => {
    sandbox.holdCeremonyReveal("req-nostorage", { plaintext: "stillWorks", title: "t", help: "h" });
    sandbox.purgeCeremonyReveals();
  });
  assert.equal(sandbox.settleCeremonyReveal("req-nostorage", "confirmed"), "lost");
  assert.deepEqual(shown, []);
});

// ---- rule 7: a hold is bounded, and only a hold this file wrote is honoured ----

test("a hold older than the bound is reaped from storage, not carried forever", () => {
  // Nothing else reaps one. The host's outbox is in-memory and does not survive
  // a restart, so a write can stop having any terminal frame left to send — and
  // its plaintext would otherwise sit in the tab for as long as it stays open.
  const storage = fakeSessionStorage();
  storage.setItem(REVEALS_KEY, JSON.stringify({
    "req-stale": { plaintext: "tooOldToBeWaitedOn", title: "t", help: "h", at: Date.now() - 25 * 60 * 60 * 1000 },
    "req-fresh": { plaintext: "stillWanted", title: "t", help: "h", at: Date.now() - 60 * 1000 },
  }));

  const app = loadApp(webcrypto, storage);
  const shown = revealCapture(app);
  assert.ok(!(storage.getItem(REVEALS_KEY) || "").includes("tooOldToBeWaitedOn"),
    "an aged-out plaintext must leave STORAGE, not merely be skipped in memory");

  app.settleCeremonyReveal("req-stale", "confirmed");
  assert.deepEqual(shown, [], "and it is gone: nothing to reveal");
  app.settleCeremonyReveal("req-fresh", "confirmed");
  assert.equal(shown.length, 1, "a hold inside the bound is untouched");
  assert.match(shown[0], /stillWanted/);
});

test("a corrupt stored entry is never presented as somebody's secret", () => {
  // A truncated or foreign entry that merely happens to be an object must not
  // rehydrate as a live hold: its confirmation would open an empty reveal modal
  // captioned as the secret, which is worse than showing nothing — it reads as
  // the ceremony having succeeded.
  const storage = fakeSessionStorage();
  storage.setItem(REVEALS_KEY, JSON.stringify({
    "req-empty": {},
    "req-truncated": { title: "t", help: "h" },
    "req-marker": { lost: true, title: "t" },
    "req-real": { plaintext: "realSecret", title: "t", help: "h" },
  }));

  const app = loadApp(webcrypto, storage);
  const shown = revealCapture(app);
  for (const id of ["req-empty", "req-truncated"]) {
    assert.equal(app.settleCeremonyReveal(id, "confirmed"), undefined, `${id} must not rehydrate as a hold`);
  }
  assert.deepEqual(shown, []);

  // The two legitimate shapes still rehydrate — a stamp-less entry included,
  // since dropping one on sight would be the silent loss this all exists to
  // prevent.
  assert.equal(app.settleCeremonyReveal("req-marker", "confirmed"), "lost");
  app.settleCeremonyReveal("req-real", "confirmed");
  assert.equal(shown.length, 1);
  assert.match(shown[0], /realSecret/);
});

test("the lost-request record is bounded", () => {
  const app = loadApp(webcrypto);
  for (let i = 0; i < 250; i++) app.markSecretLost("req-" + i);
  assert.equal(vm.runInContext("lostSecretRequestIds.size", app), 200,
    "a long-lived tab must not accumulate these without limit");
  // Eviction is oldest-first, so the notices still standing are the recent ones.
  assert.ok(!vm.runInContext(`lostSecretRequestIds.has("req-0")`, app));
  assert.ok(vm.runInContext(`lostSecretRequestIds.has("req-249")`, app));
});

test("the reveal escapes its content and is not a toast", () => {
  const app = loadApp(webcrypto);
  const shown = revealCapture(app);
  app.holdCeremonyReveal("req-4", {
    plaintext: "<script>x</script>", title: "<b>t</b>", help: "h",
  });
  app.settleCeremonyReveal("req-4", "confirmed");
  assert.doesNotMatch(shown[0], /<script>/);
  assert.match(shown[0], /&lt;script&gt;/);
  // A toast auto-hides and the next toast clears it — either would destroy the
  // only copy of the secret that will ever exist.
  assert.match(shown[0], /reveal-secret/);
});
