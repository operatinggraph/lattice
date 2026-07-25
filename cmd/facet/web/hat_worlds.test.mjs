// Unit vectors for the hat-grouped landing (persona-worlds-design.md §3.4 +
// §7.5): a bound provider's binding is a third anchor spine, it names the
// trade it confers, and its ops resolve against the binding's own vertex.
//
// Same harness as staff_world.test.mjs — app.js is a plain browser script, so
// vm.runInContext hoists its function declarations onto the sandbox.

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

// The accessors resolve off the global at call time, so overwriting them
// injects a manifest without a DOM or a feed (descriptor_autofill's pattern).
function loadWorld(meRow, opRows) {
  const sandbox = loadApp();
  sandbox.me = () => meRow;
  sandbox.ops = () => opRows.map((data, i) => ({ key: "manifest.op." + i, data }));
  return sandbox;
}

const OSEI = {
  identityKey: "vtx.identity.osei",
  anchors: [{ key: "vtx.provider.P1", name: "Dr. Amara Osei", type: "provider", relation: "identifiedBy" }],
};

// The §3.4 acceptance human: lives in a unit, works a desk, teaches yoga.
const MULTI_HAT = {
  identityKey: "vtx.identity.sam",
  anchors: [
    { key: "vtx.unit.U2", name: "Unit 2", relation: "residesIn" },
    { key: "vtx.building.B1", name: "Riverside", relation: "worksAt" },
    { key: "vtx.instructor.I1", name: "Sam Okafor", type: "instructor", relation: "identifiedBy" },
  ],
};

test("splitAnchors gives a provider binding its own spine, not the residence one", () => {
  // A binding is neither a place you live nor a place you work, and the
  // residence bucket is the deny-list default — so it must be claimed
  // explicitly or a clinician's practice renders as her home.
  const { splitAnchors } = loadApp();
  const { homes, workplaces, bindings } = splitAnchors(MULTI_HAT);
  assert.deepEqual(homes.map((a) => a.key), ["vtx.unit.U2"]);
  assert.deepEqual(workplaces.map((a) => a.key), ["vtx.building.B1"]);
  assert.deepEqual(bindings.map((a) => a.key), ["vtx.instructor.I1"]);
});

test("an anchor with no relation is still a residence, never a binding", () => {
  // The documented floor for rows projected before the stamp existed.
  const { splitAnchors } = loadApp();
  const { homes, bindings } = splitAnchors({ anchors: [{ key: "vtx.unit.U9", name: "Unit 9" }] });
  assert.deepEqual(homes.map((a) => a.key), ["vtx.unit.U9"]);
  assert.deepEqual(bindings, []);
});

test("splitAnchors drops the degenerate null-key entries the unmatched binding walks emit", () => {
  // Three OPTIONAL MATCHes collect into one array, so an identity bound as an
  // instructor still yields {key: null} for provider and serviceprovider.
  const { splitAnchors } = loadApp();
  const { bindings } = splitAnchors({
    anchors: [
      { key: null, type: "provider", relation: "identifiedBy" },
      { key: "vtx.instructor.I1", name: "Sam Okafor", type: "instructor", relation: "identifiedBy" },
      { key: null, type: "serviceprovider", relation: "identifiedBy" },
    ],
  });
  assert.deepEqual(bindings.map((a) => a.key), ["vtx.instructor.I1"]);
});

test("bindingLabel names the trade and the person, and keeps the trade when the profile is absent", () => {
  const { bindingLabel } = loadApp();
  assert.equal(bindingLabel({ type: "instructor", name: "Sam Okafor" }), "Instructor · Sam Okafor");
  assert.equal(bindingLabel({ type: "provider", name: "Dr. Amara Osei" }), "Clinician · Dr. Amara Osei");
  // A binding whose profile never resolved must not degrade to a bare NanoID.
  assert.equal(bindingLabel({ type: "serviceprovider", key: "vtx.serviceprovider.K1" }), "Service provider");
});

// The real dispatch shapes from packages/clinic-domain/opmetas.go — the only
// ops in the corpus that target a provider. Two administer the record; the
// third merely books with it, and telling them apart is the whole job.
const SET_PROVIDER_HOURS = {
  operationType: "SetProviderHours",
  dispatchClass: "provider", dispatchAuthContext: "standing",
  dispatchTargetField: "providerKey", dispatchTargetType: "provider",
};
const SET_PROVIDER_TIME_OFF = {
  operationType: "SetProviderTimeOff",
  dispatchClass: "provider", dispatchAuthContext: "standing",
  dispatchTargetField: "providerKey", dispatchTargetType: "provider",
};
const CREATE_APPOINTMENT = {
  operationType: "CreateAppointment",
  dispatchClass: "appointment", dispatchAuthContext: "self",
  dispatchTargetField: "provider", dispatchTargetType: "provider",
};

test("hatOps offers the ops that administer the bound record", () => {
  const app = loadWorld(OSEI, [SET_PROVIDER_HOURS, SET_PROVIDER_TIME_OFF]);
  const anchor = app.splitAnchors(app.me()).bindings[0];
  assert.deepEqual(app.hatOps(anchor).map((o) => o.data.operationType),
    ["SetProviderHours", "SetProviderTimeOff"]);
});

test("hatOps does not offer booking an appointment with yourself", () => {
  // CreateAppointment carries targetType "provider" exactly as the two
  // administrative ops do, so a targetType-only filter offers a clinician
  // "Book appointment" against her own record. Its dispatch CLASS is
  // "appointment" — the record is the counterparty, not the subject.
  const app = loadWorld(OSEI, [CREATE_APPOINTMENT]);
  const anchor = app.splitAnchors(app.me()).bindings[0];
  assert.deepEqual(app.hatOps(anchor), []);
});

test("a clinician who is also a resident keeps her patient ops off her clinician hat", () => {
  // §3.4's multi-hat human is the case that makes this bite: the catalog
  // carries CreateAppointment for every resident of the building, so binding
  // any resident as a provider hands her both op sets at once.
  const app = loadWorld(OSEI, [CREATE_APPOINTMENT, SET_PROVIDER_HOURS]);
  const anchor = app.splitAnchors(app.me()).bindings[0];
  assert.deepEqual(app.hatOps(anchor).map((o) => o.data.operationType), ["SetProviderHours"]);
});

test("hatOps does not offer an op targeting a different type entirely", () => {
  const app = loadWorld(OSEI, [{
    operationType: "TombstoneSession",
    dispatchClass: "session", dispatchTargetField: "session", dispatchTargetType: "session",
  }]);
  const anchor = app.splitAnchors(app.me()).bindings[0];
  assert.deepEqual(app.hatOps(anchor), []);
});

test("hatOps ignores an op that declares no dispatch target field", () => {
  // resolveTargetKey answers undefined without a targetField, so the op is not
  // submittable from anywhere — offering it here would be the degraded card.
  const app = loadWorld(OSEI, [{
    operationType: "ReportIssue", dispatchClass: "provider", dispatchTargetType: "provider",
  }]);
  const anchor = app.splitAnchors(app.me()).bindings[0];
  assert.deepEqual(app.hatOps(anchor), []);
});

test("an instructor and a service provider hat carry no ops in today's corpus", () => {
  // No op in packages/ declares dispatch class or targetType "instructor" or
  // "serviceprovider" — the wellness and service ops target a session and a
  // service instance instead. The binding types are declared symmetrically, so
  // this pins the asymmetry that actually exists rather than assuming parity.
  const app = loadWorld(
    {
      identityKey: "vtx.identity.sam",
      anchors: [
        { key: "vtx.instructor.I1", name: "Sam Okafor", type: "instructor", relation: "identifiedBy" },
        { key: "vtx.serviceprovider.K1", name: "Kai's Laundry", type: "serviceprovider", relation: "identifiedBy" },
      ],
    },
    [
      { operationType: "TombstoneSession", dispatchClass: "session", dispatchTargetField: "session", dispatchTargetType: "session" },
      { operationType: "RecordServiceOutcome", dispatchClass: "service", dispatchTargetField: "instanceKey", dispatchTargetType: "service" },
    ],
  );
  const [instructor, laundry] = app.splitAnchors(app.me()).bindings;
  assert.deepEqual(app.hatOps(instructor), []);
  assert.deepEqual(app.hatOps(laundry), []);
});

test("a hat with no ops renders an inert chip; a hat with ops renders a tappable one", () => {
  // A chip that invites a tap and then says "Nothing to do here yet" is worse
  // than one that plainly states what you are.
  const app = loadWorld(OSEI, [SET_PROVIDER_HOURS]);
  const { bindings } = app.splitAnchors(app.me());
  assert.match(app.bindingChipRow(bindings), /data-goto="hat"/);

  const empty = loadWorld(OSEI, [CREATE_APPOINTMENT]);
  const emptyRow = empty.bindingChipRow(empty.splitAnchors(empty.me()).bindings);
  assert.doesNotMatch(emptyRow, /data-goto="hat"/);
  assert.match(emptyRow, /Clinician · Dr\. Amara Osei/);
});
