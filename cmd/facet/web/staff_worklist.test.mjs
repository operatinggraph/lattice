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

// The Protected-pane dispatch wiring: a worklist row supplies ctx.entityKey
// itself, straight to the SAME opButton/resolveTargetKey seam
// the mirror browse view uses.
const startSeriesOp = {
  key: "vtx.meta.startvisitseriesop",
  data: {
    operationType: "StartVisitSeries",
    dispatchClass: "visitseries",
    dispatchTargetField: "patientKey",
    dispatchTargetType: "patient",
    title: "Start a visit series",
    submitLabel: "Start series",
    tone: "primary",
  },
};
const pauseOp = {
  key: "vtx.meta.pausevisitseriesop",
  data: {
    operationType: "PauseVisitSeries",
    dispatchClass: "visitseries",
    dispatchTargetField: "seriesKey",
    dispatchTargetType: "visitseries",
    title: "Pause visit series",
    submitLabel: "Pause series",
    tone: "neutral",
  },
};
const resumeOp = {
  key: "vtx.meta.resumevisitseriesop",
  data: {
    operationType: "ResumeVisitSeries",
    dispatchClass: "visitseries",
    dispatchTargetField: "seriesKey",
    dispatchTargetType: "visitseries",
    title: "Resume visit series",
    submitLabel: "Resume series",
    tone: "primary",
  },
};

test("a schedule row with a patient key and the op offers Start series", () => {
  const { worklistAppointmentRow } = loadApp();
  const html = worklistAppointmentRow(
    { appointmentId: "P1", patientKey: "vtx.patient.abc123", patientName: "Riley Chen" },
    startSeriesOp,
  );
  assert.match(html, /data-open-op="vtx\.meta\.startvisitseriesop"/);
  assert.match(html, /data-entity-key="vtx\.patient\.abc123"/);
  assert.match(html, /Start series/);
});

test("a schedule row renders no Start-series button when the actor holds no such op", () => {
  // startSeriesOp is undefined for an actor without the grant — the row must
  // degrade by omitting the button, never by calling opButton with nothing.
  const { worklistAppointmentRow } = loadApp();
  const html = worklistAppointmentRow({ appointmentId: "P1", patientKey: "vtx.patient.abc123", patientName: "Riley Chen" });
  assert.doesNotMatch(html, /data-open-op/);
});

test("an active series offers Pause, a paused series offers Resume, never both", () => {
  const { worklistVisitSeriesRow } = loadApp();
  const active = worklistVisitSeriesRow(
    { entityKey: "vtx.visitseries.s1", patientName: "Riley Chen", active: true },
    pauseOp, resumeOp,
  );
  assert.match(active, /Pause series/);
  assert.doesNotMatch(active, /Resume series/);
  assert.match(active, /data-entity-key="vtx\.visitseries\.s1"/);

  const paused = worklistVisitSeriesRow(
    { entityKey: "vtx.visitseries.s2", patientName: "Riley Chen", active: false },
    pauseOp, resumeOp,
  );
  assert.match(paused, /Resume series/);
  assert.doesNotMatch(paused, /Pause series/);
});

test("a visit series row with no patient name costs the label, never the row, and never a bare NanoID as the label", () => {
  // entityKey legitimately appears in data-entity-key (the dispatch
  // attribute); the display-label floor rule is about the VISIBLE title, so
  // this checks that span specifically rather than the whole markup.
  const { worklistVisitSeriesRow } = loadApp();
  const html = worklistVisitSeriesRow({ entityKey: "vtx.visitseries.s3", active: true }, pauseOp, resumeOp);
  assert.match(html, /timeline-item/);
  const title = html.match(/<span class="title">([^<]*)<\/span>/);
  assert.equal(title && title[1], "Patient");
});

test("the worklist pane lists a Recurring visit series category", () => {
  const { worklistHTML } = loadApp();
  const html = worklistHTML(
    { status: "ready", applications: [], schedule: [], visitSeries: [{ entityKey: "vtx.visitseries.s1", patientName: "Riley Chen", active: true }], day: "2026-07-20" },
    "Riverside",
  );
  assert.match(html, /Recurring visit series/);
  assert.match(html, /Riley Chen/);
});

test("a worklist with no visitSeries field (back-compat) still renders", () => {
  const { worklistHTML } = loadApp();
  const html = worklistHTML({ status: "ready", applications: [], schedule: [], day: "2026-07-20" }, "Riverside");
  assert.match(html, /No recurring series at this workplace/);
});
