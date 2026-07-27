// Regression vectors for typed dispatch-target resolution (edge-showcase-app
// -design.md §3.3). A `dispatch.targetField` is answered by matching the op's
// declared `dispatch.targetType` against the keys the context carries — NOT by
// keying off authContext, which says something else entirely (which wire-
// envelope field the client populates).
//
// The bug these pin: every `authContext:"self"` op with a targetField used to
// resolve to the actor's identity key, so wellness CreateBooking submitted a
// vtx.identity where the script required a vtx.session and the Processor
// rejected it live. Six of the seven shipped op metas had this shape.
//
// Same harness as descriptor_autofill.test.mjs: app.js is a plain browser
// script, so vm.runInContext hoists its function declarations onto the
// sandbox. resolveTargetKey / keyType are declarations; the resolver's
// me()/tasks() fallbacks read the module-scoped `state`, which starts empty —
// so an unresolvable target correctly comes back undefined here.

import { test } from "node:test";
import assert from "node:assert/strict";
import vm from "node:vm";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const appSrc = fs.readFileSync(path.join(__dirname, "app.js"), "utf8");

function loadApp() {
  const sandbox = { console, document: { addEventListener() {} } };
  vm.createContext(sandbox);
  vm.runInContext(appSrc, sandbox, { filename: "app.js" });
  return sandbox;
}

// signIn seeds the manifest.me row the resolver's me() reads. app.js's `state`
// is a top-level `const`, which lands in the context's global LEXICAL scope
// rather than on the sandbox object — a later script in the SAME context still
// sees it, which is the only way to give these vectors a signed-in actor.
// Without one, "does not resolve to the submitter" would pass on an absent
// identity rather than on the rule under test.
function signIn(sandbox, identityKey) {
  vm.runInContext(
    `state.rows.set("manifest.me", { data: { identityKey: ${JSON.stringify(identityKey)} }, pending: false })`,
    sandbox);
}

const SESSION = "vtx.session.AAAAAAAAAAAAAAAAAAAA";
const APPT = "vtx.appointment.BBBBBBBBBBBBBBBBBBBB";
const SERVICE = "vtx.service.CCCCCCCCCCCCCCCCCCCC";
const TASK = "vtx.task.DDDDDDDDDDDDDDDDDDDD";
const IDENTITY = "vtx.identity.EEEEEEEEEEEEEEEEEEEE";
const OTHER_IDENTITY = "vtx.identity.FFFFFFFFFFFFFFFFFFFF";

test("keyType reads the Contract #1 type out of a vtx key", () => {
  const { keyType } = loadApp();
  assert.equal(keyType(SESSION), "session");
  assert.equal(keyType("manifest.op.x"), undefined);
  assert.equal(keyType(""), undefined);
  assert.equal(keyType(undefined), undefined);
});

// The live failure, pinned: "Book a class" off a service card has no session
// anywhere in context, so it must resolve to nothing — never to the actor.
test("a self-authContext op does not resolve its typed target to the identity", () => {
  const { resolveTargetKey } = loadApp();
  const createBooking = {
    dispatchAuthContext: "self",
    dispatchTargetField: "session",
    dispatchTargetType: "session",
  };
  assert.equal(resolveTargetKey(createBooking, { serviceKey: SERVICE }), undefined);
});

test("a typed target resolves from the entity in view", () => {
  const { resolveTargetKey } = loadApp();
  const createBooking = {
    dispatchAuthContext: "self",
    dispatchTargetField: "session",
    dispatchTargetType: "session",
  };
  assert.equal(resolveTargetKey(createBooking, { entityKey: SESSION }), SESSION);
});

// A task scopedTo the appointment is what makes clinic's Reschedule/Cancel
// submittable — the same ops that previously sent an identity key.
test("a typed target resolves from the task's scopedTo target", () => {
  const { resolveTargetKey } = loadApp();
  const reschedule = {
    dispatchAuthContext: "self",
    dispatchTargetField: "appointmentKey",
    dispatchTargetType: "appointment",
  };
  assert.equal(resolveTargetKey(reschedule, { taskKey: "manifest.task.x", scopedTo: APPT }), APPT);
});

test("a context key of the wrong type does not satisfy a typed target", () => {
  const { resolveTargetKey } = loadApp();
  const createBooking = {
    dispatchAuthContext: "self",
    dispatchTargetField: "session",
    dispatchTargetType: "session",
  };
  assert.equal(resolveTargetKey(createBooking, { scopedTo: APPT, serviceKey: SERVICE }), undefined);
});

// RequestService — the one shape the authContext mapping always got right,
// and the one that must keep working now that it is declared.
test("a service-typed target resolves from the service in context", () => {
  const { resolveTargetKey } = loadApp();
  const requestService = {
    dispatchAuthContext: "service",
    dispatchTargetField: "service",
    dispatchTargetType: "service",
  };
  assert.equal(resolveTargetKey(requestService, { serviceKey: SERVICE }), SERVICE);
});

test("an op meta with no declared targetType keeps the authContext fallback", () => {
  const { resolveTargetKey } = loadApp();
  const legacyService = { dispatchAuthContext: "service", dispatchTargetField: "service" };
  assert.equal(resolveTargetKey(legacyService, { serviceKey: SERVICE }), SERVICE);

  const legacyTask = { dispatchAuthContext: "task", dispatchTargetField: "target" };
  assert.equal(resolveTargetKey(legacyTask, { scopedTo: APPT }), APPT);

  // The fallback deliberately has no "self" arm: that arm was the bug.
  const legacySelf = { dispatchAuthContext: "self", dispatchTargetField: "session" };
  assert.equal(resolveTargetKey(legacySelf, { serviceKey: SERVICE }), undefined);
});

// An op whose target IS the task rather than the task's subject — ClaimTask's
// shape. openTaskDetail is the surface that populates ctx.taskKey.
test("a task-typed target resolves from the task in context", () => {
  const { resolveTargetKey } = loadApp();
  const claimTask = {
    dispatchAuthContext: "task",
    dispatchTargetField: "taskKey",
    dispatchTargetType: "task",
  };
  assert.equal(resolveTargetKey(claimTask, { taskKey: TASK, scopedTo: APPT }), TASK);
});

// The task's scopedTo subject still wins for an op about that subject — a
// Reschedule offered from the same row must not start claiming the task.
test("the task key does not outrank the scopedTo subject for a subject-typed op", () => {
  const { resolveTargetKey } = loadApp();
  const reschedule = {
    dispatchAuthContext: "task",
    dispatchTargetField: "appointmentKey",
    dispatchTargetType: "appointment",
  };
  assert.equal(resolveTargetKey(reschedule, { taskKey: TASK, scopedTo: APPT }), APPT);
});

// An identity target is resolved, never inferred. RecordIdentityPII arrives on
// a task scopedTo the subject; offered anywhere else it must degrade, because
// substituting the signed-in operator would write that operator's own SSN/DOB
// row — and an operator's guard exemption means nothing downstream refuses it.
test("an identity-typed target resolves from the task's subject, not the submitter", () => {
  const sandbox = loadApp();
  signIn(sandbox, IDENTITY);
  const { resolveTargetKey } = sandbox;
  const recordPII = {
    dispatchAuthContext: "task",
    dispatchTargetField: "identityKey",
    dispatchTargetType: "identity",
  };
  assert.equal(resolveTargetKey(recordPII, { taskKey: TASK, scopedTo: OTHER_IDENTITY }), OTHER_IDENTITY);
});

test("an identity-typed target with nothing to resolve against degrades", () => {
  const sandbox = loadApp();
  signIn(sandbox, IDENTITY);
  const { resolveTargetKey, me } = sandbox;
  assert.equal(me().identityKey, IDENTITY, "the actor must be signed in, or this vector proves nothing");
  const recordPII = {
    dispatchAuthContext: "task",
    dispatchTargetField: "identityKey",
    dispatchTargetType: "identity",
  };
  assert.equal(resolveTargetKey(recordPII, { serviceKey: SERVICE }), undefined);
});

test("an op with no targetField resolves to nothing at all", () => {
  const { resolveTargetKey } = loadApp();
  const openTab = { dispatchAuthContext: "self", dispatchClass: "tab" };
  assert.equal(resolveTargetKey(openTab, { serviceKey: SERVICE }), undefined);
});

// The browse view's whole contract: with ctx.entityKey set (an entity
// detail is open), the SAME op that degrades on a service card renders a
// real, submittable button.
test("opButton offers a typed-target op when an entity of that type is in view", () => {
  const { opButton } = loadApp();
  const createBooking = {
    key: "manifest.op.x",
    data: {
      title: "Book a class",
      operationType: "CreateBooking",
      dispatchClass: "booking",
      dispatchAuthContext: "self",
      dispatchTargetField: "session",
      dispatchTargetType: "session",
    },
  };
  const degraded = opButton(createBooking, { serviceKey: SERVICE });
  assert.match(degraded, /degraded-card/);
  assert.doesNotMatch(degraded, /<button/);

  const offered = opButton(createBooking, { entityKey: SESSION });
  assert.match(offered, /<button/);
  assert.match(offered, /data-entity-key="vtx\.session\./);
});

// The café ownership probe rides in optionalReads, not contextParams. A
// resident holding two lease applications cannot answer {me.leaseapp}, so the
// probe would substitute a hole, never be declared, and the script would refuse
// the visitor's own tab with an AuthDenied they cannot act on.
test("an unresolvable {me.<type>} in optionalReads degrades the op", () => {
  const sandbox = loadApp();
  const { unresolvableSelfAnchor } = sandbox;
  const settle = {
    dispatchAuthContext: "self",
    dispatchTargetField: "tabKey",
    dispatchTargetType: "tab",
    dispatchOptionalReads: ["lnk.leaseapp.{me.leaseapp:id}.applicationFor.identity.{actor:id}"],
  };
  // no me-row at all: the anchor is unanswerable, and the `:id` modifier must
  // not be mistaken for part of the anchor type
  assert.equal(unresolvableSelfAnchor(settle), "leaseapp");

  vm.runInContext(
    `state.rows.set("manifest.me", { data: { identityKey: ${JSON.stringify(IDENTITY)}, selfAnchors: [
       { type: "leaseapp", key: "vtx.leaseapp.GGGGGGGGGGGGGGGGGGGG" }] }, pending: false })`,
    sandbox);
  assert.equal(unresolvableSelfAnchor(settle), undefined, "one lease answers the probe");

  vm.runInContext(
    `state.rows.get("manifest.me").data.selfAnchors.push(
       { type: "leaseapp", key: "vtx.leaseapp.HHHHHHHHHHHHHHHHHHHH" })`,
    sandbox);
  assert.equal(unresolvableSelfAnchor(settle), "leaseapp", "two leases are not a value to guess at");
});

// The browse row's secondary line reads COLUMNS, not entityType — so a lens
// that projects a running total gets it rendered without the renderer learning
// what a café tab is.
test("an entity row's meta line renders a projected *Cents column as money", () => {
  const { entityMeta } = loadApp();
  assert.equal(entityMeta({ subtitle: "Unit 1", totalCents: 450 }), "Unit 1 &middot; $4.50");
  assert.equal(entityMeta({ totalCents: 0 }), "$0.00");
  assert.equal(entityMeta({ subtitle: "Riverside Building" }), "Riverside Building");
  assert.equal(entityMeta({}), "");
  // a non-numeric lookalike is not an amount
  assert.equal(entityMeta({ subtitle: "x", someCents: "450" }), "x");
});

test("indefinite article follows the label's leading sound", () => {
  const { indefinite } = loadApp();
  assert.equal(indefinite("Appointment"), "an Appointment");
  assert.equal(indefinite("Class session"), "a Class session");
  assert.equal(indefinite("Provider"), "a Provider");
});

// An op that declares dispatchVisibleWhen is offered only against a resolved
// target ROW carrying the declared state — the ctx.row seam. Every rowless
// context (a service card, a hat detail) fails closed rather than offering a
// state-machine op whose state nobody can see.
test("opButton gates a visibleWhen op on the ctx row's declared state", () => {
  const { opButton } = loadApp();
  const pause = {
    key: "manifest.op.p",
    data: {
      operationType: "PauseVisitSeries",
      dispatchClass: "visitseries", dispatchAuthContext: "standing",
      dispatchTargetField: "seriesKey", dispatchTargetType: "visitseries",
      submitLabel: "Pause series",
      dispatchVisibleWhen: { field: "active", equals: true },
    },
  };
  const SERIES = "vtx.visitseries.KKKKKKKKKKKKKKKKKKKK";
  assert.match(opButton(pause, { entityKey: SERIES, row: { active: true } }), /<button/);
  assert.equal(opButton(pause, { entityKey: SERIES, row: { active: false } }), "");
  assert.equal(opButton(pause, { entityKey: SERIES, row: {} }), ""); // no field: fail closed
  assert.equal(opButton(pause, { entityKey: SERIES }), "");          // no row at all
});

// ---- picker-path vectors (facet-discovery-restoration follow-on) ----
// An op whose declared targetType is unresolvable from context is still
// OFFERABLE when its target field is a declared x-entityRef picker with
// candidates in the mirror: the form collects the target; context resolution
// was only ever the auto-fill convenience. No candidates, or no picker ⇒ the
// degraded hint stands.

const PICKER_OP = {
  dispatchClass: "appointment",
  dispatchAuthContext: "self",
  dispatchTargetField: "providerKey",
  dispatchTargetType: "provider",
  operationType: "CreateAppointment",
  title: "Book appointment",
  inputSchema: JSON.stringify({
    type: "object",
    properties: { providerKey: { type: "string", "x-entityRef": "provider" }, startsAt: { type: "string" } },
    required: ["providerKey", "startsAt"],
  }),
};

function seedProvider(sandbox) {
  vm.runInContext(
    `state.rows.set("manifest.ent.p1", { data: { ns: "manifest.ent", entityKey: "vtx.provider.GGGGGGGGGGGGGGGGGGGG", entityType: "provider", title: "Dr. P" }, pending: false })`,
    sandbox);
}

test("pickerFillsTargetField needs both the declared picker and a candidate", () => {
  const sandbox = loadApp();
  assert.equal(sandbox.pickerFillsTargetField(PICKER_OP), false, "no candidates in the mirror yet");
  seedProvider(sandbox);
  assert.equal(sandbox.pickerFillsTargetField(PICKER_OP), true, "picker + candidate = fillable");
  const noPicker = { ...PICKER_OP, inputSchema: JSON.stringify({ type: "object", properties: { providerKey: { type: "string" } } }) };
  assert.equal(sandbox.pickerFillsTargetField(noPicker), false, "no x-entityRef = not fillable");
});

test("an unresolvable-but-pickered op renders a live button, not a degraded card", () => {
  const sandbox = loadApp();
  seedProvider(sandbox);
  const html = sandbox.opButton({ data: PICKER_OP }, { serviceKey: SERVICE });
  assert.match(html, /op-btn|<button/, "offerable: the picker collects the target");
  assert.doesNotMatch(html, /degraded-card/);
  const bare = sandbox.opButton({ data: { ...PICKER_OP, inputSchema: "{}" } }, { serviceKey: SERVICE });
  assert.match(bare, /degraded-card/, "without the picker the degraded hint stands");
});
