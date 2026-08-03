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
// origin where crypto.subtle does not exist.
function loadApp(crypto) {
  const sandbox = {
    console,
    document: { addEventListener() {} },
    crypto,
    TextEncoder,
  };
  vm.createContext(sandbox);
  vm.runInContext(appSrc, sandbox, { filename: "app.js" });
  return sandbox;
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

// ---- rule 3: reveal on confirmed, discard silently on anything else ----

// revealCapture swaps showModal for a recorder, so the reveal path can be
// driven without a DOM. showModal is a function declaration, so it is on the
// sandbox global and reassignable.
function revealCapture(app) {
  const shown = [];
  app.showModal = (html) => shown.push(html);
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

test("an outbox frame for a request with no held secret is inert", () => {
  const app = loadApp(webcrypto);
  const shown = revealCapture(app);
  app.settleCeremonyReveal("some-ordinary-write", "confirmed");
  assert.deepEqual(shown, []);
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
