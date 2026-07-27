// Regression test for facet-app-ux.md §3.3/§3.6's degraded-render contract
// ("Facet never crashes or blocks on an undescribed op; it degrades
// gracefully per the design's explicit contract") — edge-showcase-app-
// design.md §3's Fire-1 green-bar item ("an undescribed op degrades"),
// shipped without a regression test until Inc 4. No test framework exists
// elsewhere in this repo's JS surfaces, so this uses only Node's built-in
// test runner (`node --test`) — no new dependency.
//
// app.js is a plain browser script (function declarations + `const`s at
// module scope, no exports); vm.runInContext hoists its function
// declarations onto the sandbox object (const/let stay lexical and are NOT
// exposed — verified separately), which is enough to exercise opButton in
// isolation without a real DOM.

import { test } from "node:test";
import assert from "node:assert/strict";
import vm from "node:vm";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const appSrc = fs.readFileSync(path.join(__dirname, "app.js"), "utf8");

function loadApp() {
  const sandbox = {
    console,
    document: { addEventListener() {} },
  };
  vm.createContext(sandbox);
  vm.runInContext(appSrc, sandbox, { filename: "app.js" });
  return sandbox;
}

test("opButton degrades gracefully for an undescribed op (no dispatchClass)", () => {
  const { opButton } = loadApp();
  const html = opButton({ key: "manifest.op.abc", data: { operationType: "SomeUndescribedOp" } }, {});
  assert.match(html, /degraded-card/);
  assert.match(html, /Some Undescribed Op/);
  assert.match(html, /ask staff to help via the admin console/);
  assert.doesNotMatch(html, /data-open-op/);
});

test("opButton renders a normal submit button for a described op (has dispatchClass)", () => {
  const { opButton } = loadApp();
  const html = opButton(
    { key: "manifest.op.abc", data: { operationType: "RequestService", dispatchClass: "write", submitLabel: "Order" } },
    { serviceKey: "manifest.svc.xyz" }
  );
  assert.doesNotMatch(html, /degraded-card/);
  assert.match(html, /data-open-op="manifest\.op\.abc"/);
  assert.match(html, /data-service-key="manifest\.svc\.xyz"/);
  assert.match(html, />Order</);
});

// crossHatMismatch (facet-entity-browse-design.md / verticals.md "Entity
// detail attaches cross-hat ops"): a self-administer op (dispatchClass ===
// dispatchTargetType) reached via openEntityDetail's plain targetType match,
// not the hat surface that already proves ownership.

// selfAdministerOp is the SetProviderHours/SetInstructorProfile shape: no
// {me.<type>} ownership param at all, so the row IS the bound record.
function selfAdministerOp(type) {
  return {
    key: "manifest.op.hours",
    data: { operationType: "SetWorkingHours", dispatchClass: type, dispatchTargetType: type, submitLabel: "Save" },
  };
}

test("crossHatMismatch: viewing your OWN record renders live, not degraded", () => {
  const sandbox = loadApp();
  sandbox.me = () => ({ selfAnchors: [{ key: "vtx.provider.MINE", type: "provider" }] });
  const html = sandbox.opButton(selfAdministerOp("provider"), { entityKey: "vtx.provider.MINE" });
  assert.doesNotMatch(html, /degraded-card/);
  assert.match(html, /data-open-op/);
});

test("crossHatMismatch: a bound provider viewing a DIFFERENT provider's record degrades", () => {
  // The multi-hat case the item names: holding some anchor of the right KIND
  // is not proof this particular row is that one.
  const sandbox = loadApp();
  sandbox.me = () => ({ selfAnchors: [{ key: "vtx.provider.MINE", type: "provider" }] });
  const html = sandbox.opButton(selfAdministerOp("provider"), { entityKey: "vtx.provider.SOMEONE_ELSE" });
  assert.match(html, /degraded-card/);
  assert.match(html, /belongs to someone else/);
});

test("crossHatMismatch: holding NO anchor of that type at all does not degrade — most Class===targetType ops are confined some other way entirely (workplace, a private per-viewer walk), and this branch must not newly gate a type that was never hat-shaped", () => {
  const sandbox = loadApp();
  sandbox.me = () => ({ selfAnchors: [] });
  const html = sandbox.opButton(selfAdministerOp("visitseries"), { entityKey: "vtx.visitseries.ANY" });
  assert.doesNotMatch(html, /degraded-card/);
  assert.match(html, /data-open-op/);
});

test("crossHatMismatch: a {me.<type>} ownership param naming a DIFFERENT type than the target degrades when the row's own provenance column disagrees", () => {
  const sandbox = loadApp();
  sandbox.me = () => ({ selfAnchors: [{ key: "vtx.instructor.MINE", type: "instructor" }] });
  sandbox.entities = () => [{ data: { entityKey: "vtx.session.THEIRS", instructorKey: "vtx.instructor.SOMEONE_ELSE" } }];
  const op = {
    key: "manifest.op.cancel",
    data: {
      operationType: "TombstoneSession",
      dispatchClass: "session",
      dispatchTargetType: "session",
      dispatchContextParams: JSON.stringify({ instructor: "{me.instructor}", studio: "{entity.studioKey}" }),
      submitLabel: "Cancel",
    },
  };
  const html = sandbox.opButton(op, { entityKey: "vtx.session.THEIRS" });
  assert.match(html, /degraded-card/);
  assert.match(html, /belongs to someone else/);
});

test("crossHatMismatch: the same op renders live when the row's provenance column IS this identity's own anchor", () => {
  const sandbox = loadApp();
  sandbox.me = () => ({ selfAnchors: [{ key: "vtx.instructor.MINE", type: "instructor" }] });
  sandbox.entities = () => [{ data: { entityKey: "vtx.session.MINE", instructorKey: "vtx.instructor.MINE", studioKey: "vtx.studio.S1" } }];
  const op = {
    key: "manifest.op.cancel",
    data: {
      operationType: "TombstoneSession",
      dispatchClass: "session",
      dispatchTargetType: "session",
      dispatchContextParams: JSON.stringify({ instructor: "{me.instructor}", studio: "{entity.studioKey}" }),
      submitLabel: "Cancel",
    },
  };
  const html = sandbox.opButton(op, { entityKey: "vtx.session.MINE" });
  assert.doesNotMatch(html, /degraded-card/);
});

test("crossHatMismatch: a row not yet projecting the provenance column is left alone, not degraded", () => {
  const sandbox = loadApp();
  sandbox.me = () => ({ selfAnchors: [{ key: "vtx.instructor.MINE", type: "instructor" }] });
  sandbox.entities = () => [{ data: { entityKey: "vtx.session.UNPROJECTED" } }];
  const op = {
    key: "manifest.op.cancel",
    data: {
      operationType: "TombstoneSession",
      dispatchClass: "session",
      dispatchTargetType: "session",
      dispatchContextParams: JSON.stringify({ instructor: "{me.instructor}" }),
      submitLabel: "Cancel",
    },
  };
  const html = sandbox.opButton(op, { entityKey: "vtx.session.UNPROJECTED" });
  assert.doesNotMatch(html, /degraded-card/);
});

test("crossHatMismatch never engages for a task/service context (no entityKey) — the ClaimTask shape", () => {
  const sandbox = loadApp();
  sandbox.me = () => ({ selfAnchors: [] });
  const op = {
    key: "manifest.op.claim",
    data: { operationType: "ClaimTask", dispatchClass: "task", dispatchTargetType: "task", submitLabel: "Claim" },
  };
  const html = sandbox.opButton(op, { taskKey: "vtx.task.T1" });
  assert.doesNotMatch(html, /degraded-card/);
  assert.match(html, /data-open-op/);
});
