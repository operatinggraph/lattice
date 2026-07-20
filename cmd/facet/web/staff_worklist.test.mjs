// Unit vectors for the staff Worklist screen archetype
// (facet-staff-worlds-design.md §3.4): the pane is server-side and says so
// when it cannot be read, its visibility derives from the workplace spine, and
// a null display column costs a field rather than a row.
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

test("the Work tab derives from the workplace spine, not from curation", () => {
  const { isStaffMe } = loadApp();
  assert.equal(
    isStaffMe({ anchors: [{ key: "vtx.building.B1", name: "Riverside", relation: "worksAt" }] }),
    true,
  );
});

test("a resident with only a residence anchor is not staff", () => {
  const { isStaffMe } = loadApp();
  assert.equal(isStaffMe({ anchors: [{ key: "vtx.unit.U1", name: "Unit 1", relation: "residesIn" }] }), false);
  assert.equal(isStaffMe({ anchors: [] }), false);
  assert.equal(isStaffMe(null), false); // pre-hydration: no manifest yet
});

test("an unreadable pane reads as UNAVAILABLE, never as an empty worklist", () => {
  // The distinction is load-bearing: an empty worklist is a real answer about
  // the workplace ("nothing waiting"), and a front-desk actor acts on it. A
  // pane that simply could not be read must never render as that answer.
  const { worklistHTML } = loadApp();
  const html = worklistHTML({ status: "unavailable", applications: [], schedule: [], day: "" }, "Riverside");
  assert.match(html, /unavailable/i);
  assert.doesNotMatch(html, /No applications waiting/);
  assert.doesNotMatch(html, /Nothing scheduled today/);
});

test("a ready-but-empty pane does state the real answer", () => {
  const { worklistHTML } = loadApp();
  const html = worklistHTML({ status: "ready", applications: [], schedule: [], day: "2026-07-20" }, "Riverside");
  assert.match(html, /No applications waiting/);
  assert.match(html, /Nothing scheduled today/);
  assert.match(html, /Riverside/);
});

test("a null applicant name costs the label, never the row", () => {
  const { worklistApplicationRow } = loadApp();
  const html = worklistApplicationRow({ appId: "A1", applicantName: "", unitAddress: "", qualified: null });
  assert.match(html, /timeline-item/);
  assert.match(html, /Applicant/);
  assert.doesNotMatch(html, /A1/); // no bare NanoID as a label (display-names N2)
});

test("a null appointment time costs the prefix, never the row", () => {
  const { worklistAppointmentRow, timeOfDay } = loadApp();
  assert.equal(timeOfDay(null), "");
  assert.equal(timeOfDay("not-a-date"), "");
  assert.equal(timeOfDay("2026-07-20T14:30:00Z"), "14:30 — ");
  const html = worklistAppointmentRow({ appointmentId: "P1", startsAt: null, patientName: "Riley Chen" });
  assert.match(html, /Riley Chen/);
  assert.match(html, /Provider/);
});

test("the qualified flag renders three distinct states, not two", () => {
  // null (no readiness signal yet) must not render as "incomplete" — that
  // would assert a judgement about an application nobody has assessed.
  const { worklistApplicationRow } = loadApp();
  assert.match(worklistApplicationRow({ qualified: true }), /qualified/);
  assert.match(worklistApplicationRow({ qualified: false }), /incomplete/);
  const unknown = worklistApplicationRow({ qualified: null });
  assert.doesNotMatch(unknown, /qualified/);
  assert.doesNotMatch(unknown, /incomplete/);
});
