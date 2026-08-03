// Unit vectors for the standing-op home section
// (client-ceremony-op-descriptors-design.md §18): an op whose authority is a
// standing role grant, naming no dispatch target and riding no service, is
// the one class this client discovers and then renders nowhere — every other
// surface filters on `dispatchTargetType` or on `viaServices`.
//
// Same harness as hat_worlds.test.mjs — app.js is a plain browser script, so
// vm.runInContext hoists its function declarations onto the sandbox, and the
// accessors resolve off the global at call time, so overwriting `ops`
// injects a manifest without a DOM or a feed.

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

function withOps(opRows) {
  const sandbox = loadApp();
  sandbox.ops = () => opRows.map((data, i) => ({ key: "manifest.op." + i, data }));
  return sandbox;
}

// identity-domain's CreateUnclaimedIdentity as the lens projects it: a
// standing role grant, an owning class, and no target of any kind.
const CREATE_IDENTITY = {
  operationType: "CreateUnclaimedIdentity",
  title: "Create an identity",
  submitLabel: "Create identity",
  dispatchClass: "identity",
  dispatchAuthContext: "standing",
  viaRole: "vtx.role.FOH",
  viaRoleName: "frontOfHouse",
  viaServices: [],
};

// A service-reachable op — the service-detail surface already offers it.
const BOOK_CLASS = {
  operationType: "CreateBooking",
  title: "Book a class",
  submitLabel: "Book",
  dispatchClass: "booking",
  dispatchAuthContext: "service",
  dispatchTargetField: "sessionKey",
  dispatchTargetType: "session",
  viaServices: ["vtx.service.YOGA"],
};

// A target-typed standing op — identity-domain's RotateClaimKey. The entity,
// pane, hat and task surfaces are where a typed target is resolvable.
const ROTATE_CLAIM_KEY = {
  operationType: "RotateClaimKey",
  title: "Re-issue a claim secret",
  submitLabel: "Re-issue secret",
  dispatchClass: "identity",
  dispatchAuthContext: "standing",
  dispatchTargetField: "identityKey",
  dispatchTargetType: "identity",
  viaRole: "vtx.role.FOH",
  viaRoleName: "frontOfHouse",
  viaServices: [],
};

test("standingOps takes the op no other surface can offer", () => {
  const { standingOps } = withOps([CREATE_IDENTITY, BOOK_CLASS, ROTATE_CLAIM_KEY]);
  assert.deepEqual(standingOps().map((o) => o.data.operationType), ["CreateUnclaimedIdentity"]);
});

test("a service-reachable op is not standing, however it was granted", () => {
  // The service-detail surface lists it by viaServices, so listing it here
  // too would offer the same op twice under two different authorities.
  const { standingOps } = withOps([{ ...CREATE_IDENTITY, viaServices: ["vtx.service.DESK"] }]);
  assert.deepEqual(standingOps(), []);
});

test("a target-typed op is not standing even with no service behind it", () => {
  // RotateClaimKey's target belongs to a row; offering it off a screen with
  // no row would hand over a form whose one required field cannot be filled.
  const { standingOps } = withOps([ROTATE_CLAIM_KEY]);
  assert.deepEqual(standingOps(), []);
});

test("a self-authContext op is not standing, however unreachable it looks", () => {
  // ClaimIdentity names no target and rides no service, and the Me screen
  // serves it through its own card gated on whether the identity is claimed
  // — offering it here too would hand an already-claimed person a Claim
  // button with no state behind it.
  const claim = {
    operationType: "ClaimIdentity",
    title: "Claim your identity",
    submitLabel: "Claim",
    dispatchClass: "identity",
    dispatchAuthContext: "self",
    viaRoleName: "consumer",
    viaServices: [],
  };
  const { standingOps } = withOps([claim]);
  assert.deepEqual(standingOps(), []);
});

test("the predicate reads reachability, not role provenance", () => {
  // An op with no target and no service is listed even when the manifest
  // carries no viaRole for it — a missing provenance column must not hide a
  // row the client has nowhere else to put.
  const { standingOps } = withOps([{ ...CREATE_IDENTITY, viaRole: undefined, viaRoleName: undefined }]);
  assert.deepEqual(standingOps().map((o) => o.data.operationType), ["CreateUnclaimedIdentity"]);
});

test("a missing viaServices column reads as no service, not as an error", () => {
  const noColumn = { ...CREATE_IDENTITY };
  delete noColumn.viaServices;
  const { standingOps } = withOps([noColumn]);
  assert.deepEqual(standingOps().map((o) => o.data.operationType), ["CreateUnclaimedIdentity"]);
});

test("the section groups under the role that grants the op", () => {
  const { standingOpsHTML, standingOps } = withOps([
    CREATE_IDENTITY,
    { ...CREATE_IDENTITY, operationType: "RegisterPatient", title: "Register a patient", submitLabel: "Register", viaRoleName: "operator" },
  ]);
  const html = standingOpsHTML(standingOps());
  assert.match(html, /What I can do/);
  assert.match(html, /<h3 class="category-heading">Front Of House<\/h3>/);
  assert.match(html, /<h3 class="category-heading">Operator<\/h3>/);
  assert.match(html, /Create identity/);
  assert.match(html, /Register/);
});

test("an op with no role column still gets a heading rather than a bare button", () => {
  const { standingOpsHTML, standingOps } = withOps([{ ...CREATE_IDENTITY, viaRole: undefined, viaRoleName: undefined }]);
  assert.match(standingOpsHTML(standingOps()), /<h3 class="category-heading">Operations<\/h3>/);
});

test("no standing op means no section at all", () => {
  const { standingOpsHTML, standingOps } = withOps([BOOK_CLASS]);
  assert.equal(standingOpsHTML(standingOps()), "");
});

test("a standing op renders as a real submit button, not a degraded card", () => {
  // The gates opButton runs — dispatchClass, authContext, target,
  // self-anchor, hat, column — all pass for a standing targetless op with an
  // empty context, which is what makes this section submittable and not just
  // visible.
  const { standingOpsHTML, standingOps } = withOps([CREATE_IDENTITY]);
  const html = standingOpsHTML(standingOps());
  assert.match(html, /<button class="primary-btn[^"]*" style="margin-bottom:8px" data-open-op="manifest\.op\.0">Create identity<\/button>/);
  assert.doesNotMatch(html, /degraded-card/);
});

test("the button carries no context attributes, so the form builds a standing envelope", () => {
  // buildAuthContext("standing") returns undefined, and a stray
  // data-service-key/data-task-key would have the form send an authContext
  // the grant does not name.
  const { standingOpsHTML, standingOps } = withOps([CREATE_IDENTITY]);
  const html = standingOpsHTML(standingOps());
  assert.doesNotMatch(html, /data-service-key|data-task-key|data-entity-key|data-scoped-to/);
});

test("a group whose every op is withheld drops its heading and the section", () => {
  // dispatchVisibleWhen with no row to read against makes opButton return ""
  // — absent, not degraded — and a heading over blank space would claim an
  // affordance that is not there.
  const gated = { ...CREATE_IDENTITY, dispatchVisibleWhen: { field: "unclaimed", equals: true } };
  const { standingOpsHTML, standingOps } = withOps([gated]);
  assert.equal(standingOpsHTML(standingOps()), "");
});

test("a withheld group does not suppress a sibling role's ops", () => {
  const gated = { ...CREATE_IDENTITY, viaRoleName: "backOfHouse", dispatchVisibleWhen: { field: "unclaimed", equals: true } };
  const { standingOpsHTML, standingOps } = withOps([gated, CREATE_IDENTITY]);
  const html = standingOpsHTML(standingOps());
  assert.doesNotMatch(html, /Back Of House/);
  assert.match(html, /<h3 class="category-heading">Front Of House<\/h3>/);
  assert.match(html, /Create identity/);
});

test("an op with no descriptor at all is not listed, not shown as degraded", () => {
  // Every dispatch column null is an op meta that never adopted the
  // vocabulary — it declares no standing authority, so listing it would be
  // the client inventing a claim out of an absence, and a home screen of
  // "ask staff to help" cards is what that looks like.
  const { standingOps, standingOpsHTML } = withOps([{ operationType: "SomeUndescribedOp", viaRoleName: "operator" }]);
  assert.deepEqual(standingOps(), []);
  assert.equal(standingOpsHTML(standingOps()), "");
});

test("a standing op whose self-anchor is missing degrades in place", () => {
  // ReportIssue/CreateStudio declare {me.workplace}: a role holder who works
  // nowhere cannot fill it, and no field on the form would let them. The
  // existing gate says so rather than offering a submit that denies.
  const reportIssue = {
    operationType: "ReportIssue",
    title: "Report an issue",
    submitLabel: "Report it",
    dispatchClass: "workOrder",
    dispatchAuthContext: "standing",
    dispatchContextParams: { location: "{me.workplace}" },
    viaRoleName: "frontOfHouse",
    viaServices: [],
  };
  const sandbox = withOps([reportIssue]);
  sandbox.me = () => ({ identityKey: "vtx.identity.NOWHERE", selfAnchors: [] });
  const html = sandbox.standingOpsHTML(sandbox.standingOps());
  assert.match(html, /degraded-card/);
  assert.match(html, /<h3 class="category-heading">Front Of House<\/h3>/);
});

test("the same op is a live button once the anchor resolves", () => {
  const reportIssue = {
    operationType: "ReportIssue",
    title: "Report an issue",
    submitLabel: "Report it",
    dispatchClass: "workOrder",
    dispatchAuthContext: "standing",
    dispatchContextParams: { location: "{me.workplace}" },
    viaRoleName: "frontOfHouse",
    viaServices: [],
  };
  const sandbox = withOps([reportIssue]);
  sandbox.me = () => ({
    identityKey: "vtx.identity.SOMEONE",
    selfAnchors: [{ key: "vtx.workplace.B1", type: "workplace" }],
  });
  const html = sandbox.standingOpsHTML(sandbox.standingOps());
  assert.doesNotMatch(html, /degraded-card/);
  assert.match(html, /Report it<\/button>/);
});
