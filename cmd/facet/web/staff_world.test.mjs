// Unit vectors for the staff-world renderer beats (facet-staff-worlds-design.md
// §3.3): the two identity spines render apart, a role-queued task is claimable
// rather than openable, and a "standing" op dispatches with no authContext.
//
// Same harness as display_label.test.mjs — app.js is a plain browser script, so
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

test("splitAnchors separates the residence spine from the workplace spine", () => {
  const { splitAnchors } = loadApp();
  const { homes, workplaces } = splitAnchors({
    anchors: [
      { key: "vtx.unit.U1", name: "Unit 1", relation: "residesIn" },
      { key: "vtx.building.B1", name: "Riverside", relation: "worksAt" },
    ],
  });
  assert.deepEqual(homes.map((a) => a.key), ["vtx.unit.U1"]);
  assert.deepEqual(workplaces.map((a) => a.key), ["vtx.building.B1"]);
});

test("splitAnchors drops the degenerate null-key entry the non-matching spine emits", () => {
  // Both spines are OPTIONAL MATCHes collected into one array, so an actor who
  // matches only one of them still yields a {key: null} row for the other. It
  // must never reach the UI as an empty chip.
  const { splitAnchors } = loadApp();
  const { homes, workplaces } = splitAnchors({
    anchors: [
      { key: "vtx.building.B1", name: "Riverside", relation: "worksAt" },
      { key: null, relation: "residesIn" },
    ],
  });
  assert.equal(homes.length, 0);
  assert.deepEqual(workplaces.map((a) => a.key), ["vtx.building.B1"]);
});

test("an anchor with no relation stamp counts as a residence", () => {
  // Rows projected before the stamp existed carry no relation, and residence is
  // what they were — they must not silently migrate into the workplace group.
  const { splitAnchors } = loadApp();
  const { homes, workplaces } = splitAnchors({ anchors: [{ key: "vtx.unit.U1", name: "Unit 1" }] });
  assert.deepEqual(homes.map((a) => a.key), ["vtx.unit.U1"]);
  assert.equal(workplaces.length, 0);
});

test("a role-queued task renders a claim affordance, not a detail link", () => {
  const { taskRow } = loadApp();
  const html = taskRow({
    key: "manifest.task.T1",
    data: {
      taskKey: "vtx.task.T1",
      operationType: "FixLeak",
      queuedRole: "vtx.role.R1",
      queuedRoleName: "frontOfHouse",
    },
  });
  assert.match(html, /data-claim-task/);
  assert.match(html, /frontOfHouse/);
  // Nobody owns it yet, so opening a detail view to act on it would be wrong.
  assert.doesNotMatch(html, /data-goto="task"/);
});

test("a directly-assigned task stays an openable detail row", () => {
  const { taskRow } = loadApp();
  const html = taskRow({
    key: "manifest.task.T2",
    data: { taskKey: "vtx.task.T2", operationType: "SignLease" },
  });
  assert.match(html, /data-goto="task"/);
  assert.doesNotMatch(html, /data-claim-task/);
});

test("a standing op dispatches with no authContext", () => {
  // Standing authority is the role grant itself, so the envelope must carry no
  // authContext object at all — sending one would assert a relationship to the
  // target that a staff actor does not have.
  const { buildAuthContext } = loadApp();
  assert.equal(buildAuthContext("standing", {}), undefined);
});

test("self / service / task authContexts are unchanged by the standing addition", () => {
  // Field-by-field rather than deepEqual: the sandbox builds these objects in
  // its own realm, so their prototypes are not reference-equal to this file's.
  const { buildAuthContext } = loadApp();
  const svc = buildAuthContext("service", { serviceKey: "vtx.service.S1" });
  assert.equal(svc.service, "vtx.service.S1");
  const task = buildAuthContext("task", { taskKey: "vtx.task.T1", scopedTo: "vtx.leaseapp.L1" });
  assert.equal(task.task, "vtx.task.T1");
  assert.equal(task.target, "vtx.leaseapp.L1");
});
