// Unit vectors for what the descriptor-driven submit path DECLARES
// (Contract #2 §2.5): `reads` carries the descriptor's own `dispatch.reads`
// templates substituted against this form, and nothing else.
//
// The rule is asymmetric, which is why it is pinned rather than left to
// judgement. A declared key the operation's descriptor does not name REFUSES a
// closed-set operation outright — the Processor's closed declared-read set
// (internal/processor/descriptor_floor.go's refuseUndeclaredContextHint) faults
// ClaimIdentity and CompleteCredentialLink at the head of step 4 for one extra
// key — while an UNDECLARED key the script reads still resolves, through a live
// on-demand GET that costs latency and nothing else. So a padded declaration is
// a broken submission and a short one is a slow one.
//
// Same harness as ceremony.test.mjs: app.js is a plain browser script, so
// vm.runInContext hoists its function declarations onto the sandbox global, and
// the write seam (enqueueOperation) plus the session accessor (me) are stubbed
// on that global to drive the real submitDescriptorForm without a DOM or a feed.

import { test } from "node:test";
import assert from "node:assert/strict";
import vm from "node:vm";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const appSrc = fs.readFileSync(path.join(__dirname, "app.js"), "utf8");

const SELF = "vtx.identity.DDDDDDDDDDDDDDDDDDDD";
const TARGET = "vtx.identity.AAAAAAAAAAAAAAAAAAAA";

function loadApp() {
  const sandbox = { console, document: { addEventListener() {} }, queueMicrotask: (fn) => fn() };
  vm.createContext(sandbox);
  vm.runInContext(appSrc, sandbox, { filename: "app.js" });
  return sandbox;
}

// submitCapture drives the real submitDescriptorForm against a stubbed write
// seam and a stubbed form, and returns the request it enqueued.
async function submitCapture(op, formValues, ctx) {
  const app = loadApp();
  let enqueued = null;
  app.enqueueOperation = (req) => { enqueued = req; return Promise.resolve({ requestId: "req-capture" }); };
  app.me = () => ({ identityKey: SELF, selfAnchors: [] });
  app.toast = () => {};
  app.hideModal = () => {};
  app.showModal = () => {};

  const schema = JSON.parse(op.inputSchema);
  const fieldNames = Object.keys(schema.properties);
  const form = {
    elements: { namedItem: (n) => (n in formValues ? { value: formValues[n] } : null) },
    querySelector: () => null,
  };
  await app.submitDescriptorForm(form, op, "vtx.meta.BBBBBBBBBBBBBBBBBBBB", ctx || {},
    fieldNames, schema.properties, op.dispatchContextParams || {}, new Set(schema.required || []));
  return enqueued;
}

// A descriptor naming three `reads` templates: one payload-rooted, one aspect
// off the same payload field, one rooted in the session. Three templates, so
// "exactly these three" is a statement a fourth entry of any provenance breaks.
const THREE_TEMPLATES = {
  operationType: "InspectIdentity",
  title: "Inspect an identity",
  submitLabel: "Inspect",
  dispatchClass: "identity",
  dispatchAuthContext: "self",
  inputSchema: JSON.stringify({
    type: "object",
    properties: { targetIdentityKey: { type: "string" }, note: { type: "string" } },
    required: ["targetIdentityKey"],
  }),
  dispatchReads: ["{payload.targetIdentityKey}", "{payload.targetIdentityKey}.state", "{actor}.state"],
};

test("`reads` is exactly the descriptor's substituted read templates", async () => {
  const enqueued = await submitCapture(THREE_TEMPLATES, { targetIdentityKey: TARGET, note: "hi" });
  assert.ok(enqueued, "the form must have enqueued a request");
  assert.deepEqual([...enqueued.reads], [TARGET, TARGET + ".state", SELF + ".state"]);
});

test("the submitting identity's own key is not added on top", async () => {
  // Over-reading is not harmless on this path: one key the descriptor does not
  // name refuses a closed-set operation outright, while a script that wants the
  // actor vertex is served by the undeclared-read fallthrough for one GET.
  const noSelfTemplate = { ...THREE_TEMPLATES, dispatchReads: ["{payload.targetIdentityKey}"] };
  const enqueued = await submitCapture(noSelfTemplate, { targetIdentityKey: TARGET });
  assert.deepEqual([...enqueued.reads], [TARGET]);
  assert.ok(!enqueued.reads.includes(SELF), "the actor's own key was declared without a template naming it");
});

test("a descriptor naming no read templates declares no reads at all", async () => {
  // ClaimIdentity's shape: every key it needs is optionalReads (NFR-S6
  // anti-enumeration) or derived by the DDL, so a `reads` entry of any kind is
  // a key its descriptor does not name.
  const noReads = { ...THREE_TEMPLATES, dispatchReads: [] };
  const enqueued = await submitCapture(noReads, { targetIdentityKey: TARGET });
  assert.deepEqual([...enqueued.reads], []);
});

test("a missing dispatchReads column reads as no templates, not as an error", async () => {
  const noColumn = { ...THREE_TEMPLATES };
  delete noColumn.dispatchReads;
  const enqueued = await submitCapture(noColumn, { targetIdentityKey: TARGET });
  assert.deepEqual([...enqueued.reads], []);
});

test("a template that failed to substitute is dropped, not declared half-built", async () => {
  // The whole-key filter: an omitted optional payload field leaves a hole, and
  // a key with an empty segment names nothing the Processor can resolve.
  const enqueued = await submitCapture(THREE_TEMPLATES, { targetIdentityKey: TARGET });
  const withHole = { ...THREE_TEMPLATES, dispatchReads: ["{payload.note}.state", "{payload.targetIdentityKey}"] };
  const holed = await submitCapture(withHole, { targetIdentityKey: TARGET });
  assert.deepEqual([...holed.reads], [TARGET]);
  // ...and the well-formed sibling above is unaffected by the filter.
  assert.equal(enqueued.reads.length, 3);
});

test("optionalReads is its own list and never spills into reads", async () => {
  const both = {
    ...THREE_TEMPLATES,
    dispatchReads: ["{payload.targetIdentityKey}"],
    dispatchOptionalReads: ["{payload.targetIdentityKey}.state", "{payload.targetIdentityKey}.claimKey"],
  };
  const enqueued = await submitCapture(both, { targetIdentityKey: TARGET });
  assert.deepEqual([...enqueued.reads], [TARGET]);
  assert.deepEqual([...enqueued.optionalReads], [TARGET + ".state", TARGET + ".claimKey"]);
});
