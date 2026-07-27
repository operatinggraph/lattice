// Unit vectors for the generic server-pane renderer (facet-staff-worlds-
// design.md §3.4 + facet-discovery-restoration-design.md §2.1): a pane is
// described entirely by its manifest.pane.* descriptor row — sections, source
// columns with display roles/kinds, empty copy, dispatch target — and the
// renderer interprets that vocabulary with zero knowledge of any vertical.
// Fixtures here are deliberately domain-shaped (the real staff worklist pane):
// tests are exempt from the source vocabulary ban, and a realistic descriptor
// is the best proof the generic renderer covers the shipped shapes.
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

// The accessors resolve off the global at call time, so overwriting ops
// injects op descriptors without a DOM or a feed (hat_worlds' pattern).
function loadWorld(opRows) {
  const sandbox = loadApp();
  sandbox.ops = () => (opRows || []).map((data, i) => ({ key: "manifest.op." + i, data }));
  return sandbox;
}

// A descriptor shaped like the real staffWorklist pane: three sections over
// three Protected tables, per-column roles/kinds, valueLabels, money in both
// units, a day-scoped section, and dispatch targets of two vertex types.
const PANE = {
  paneId: "staffWorklist",
  title: "Worklist",
  icon: "doc",
  sections: JSON.stringify([
    {
      id: "applications",
      title: "Applications to review",
      emptyCopy: "No applications waiting.",
      source: {
        table: "read_landlord_lease_applications",
        columns: [
          { name: "app_id", kind: "text", role: "id" },
          { name: "applicant_name", kind: "text", role: "title", fallback: "Applicant" },
          { name: "unit_address", kind: "text", role: "subtitle", fallback: "Unit" },
          { name: "unit_city", kind: "text", role: "subtitle" },
          { name: "terms_move_in_date", label: "Move-in", kind: "date", role: "meta" },
          { name: "qualified", kind: "badge", role: "badge", valueLabels: { "true": "qualified", "false": "incomplete" } },
          { name: "terms_requested_rent", label: "Rent", kind: "money", unit: "dollars", role: "meta" },
        ],
        limit: 200,
      },
    },
    {
      id: "schedule",
      title: "Today's schedule",
      emptyCopy: "Nothing scheduled today.",
      source: {
        table: "read_clinic_appointments",
        columns: [
          { name: "appointment_id", kind: "text", role: "id" },
          { name: "patient_key", kind: "text", role: "target" },
          { name: "starts_at", kind: "datetime", role: "time" },
          { name: "ends_at", kind: "datetime", role: "timeEnd" },
          { name: "status", kind: "badge", role: "badge" },
          { name: "patient_name", kind: "text", role: "title", fallback: "Patient" },
          { name: "provider_name", kind: "text", role: "subtitle", fallback: "Provider" },
          { name: "provider_specialty", kind: "text", role: "subtitle" },
        ],
        limit: 200,
      },
      dispatch: { targetColumn: "patient_key", targetType: "patient" },
    },
    {
      id: "visitSeries",
      title: "Recurring visit series",
      emptyCopy: "No recurring series at this workplace.",
      source: {
        table: "read_visit_series",
        columns: [
          { name: "entity_key", kind: "text", role: "target" },
          { name: "patient_key", kind: "text", role: "hidden" },
          { name: "patient_name", kind: "text", role: "title", fallback: "Patient" },
          { name: "interval_days", label: "Every", kind: "number", role: "meta", suffix: "d" },
          { name: "balance_cents", label: "Balance", kind: "money", unit: "cents", role: "meta" },
          { name: "series_status", kind: "badge", role: "state", default: "ended" },
        ],
        limit: 200,
      },
      dispatch: { targetColumn: "entity_key", targetType: "visitseries" },
    },
  ]),
};

const emptyLoad = {
  status: "ready",
  sections: [
    { id: "applications", rows: [] },
    { id: "schedule", rows: [], day: "2026-07-20" },
    { id: "visitSeries", rows: [] },
  ],
};

function loadedWith(sectionId, rows, extra) {
  return {
    status: "ready",
    sections: [{ id: sectionId, rows, ...(extra || {}) }],
  };
}

const PATIENT = "vtx.patient.AAAAAAAAAAAAAAAAAAAA";
const SERIES = "vtx.visitseries.BBBBBBBBBBBBBBBBBBBB";

// The real dispatch shapes: one op targets the schedule section's target
// type unconditionally; a state-machine pair targets the series type, gated
// by visibleWhen on the row's own state column.
const startOp = {
  operationType: "StartVisitSeries", dispatchClass: "visitseries",
  dispatchTargetField: "patientKey", dispatchTargetType: "patient",
  title: "Start a visit series", submitLabel: "Start series",
};
const pauseOp = {
  operationType: "PauseVisitSeries", dispatchClass: "visitseries",
  dispatchTargetField: "seriesKey", dispatchTargetType: "visitseries",
  submitLabel: "Pause series", dispatchVisibleWhen: { field: "series_status", equals: "active" },
};
const resumeOp = {
  operationType: "ResumeVisitSeries", dispatchClass: "visitseries",
  dispatchTargetField: "seriesKey", dispatchTargetType: "visitseries",
  submitLabel: "Resume series", dispatchVisibleWhen: { field: "series_status", equals: "paused" },
};

test("the Work tab derives from the workplace spine, not from curation", () => {
  const { isStaffMe } = loadApp();
  assert.equal(
    isStaffMe({ anchors: [{ key: "vtx.building.B1", name: "North Building", relation: "worksAt" }] }),
    true,
  );
});

test("an actor with only a residence anchor holds no Work tab", () => {
  const { isStaffMe } = loadApp();
  assert.equal(isStaffMe({ anchors: [{ key: "vtx.unit.U1", name: "Unit 1", relation: "residesIn" }] }), false);
  assert.equal(isStaffMe({ anchors: [] }), false);
  assert.equal(isStaffMe(null), false); // pre-hydration: no manifest yet
});

test("an unreadable pane reads as UNAVAILABLE, never as an empty pane", () => {
  // The distinction is load-bearing: an empty section is a real answer about
  // the workplace, and the reader acts on it. A pane that simply could not be
  // read must never render as that answer.
  const { paneHTML } = loadApp();
  const html = paneHTML(PANE, { status: "unavailable", sections: [] });
  assert.match(html, /unavailable/i);
  assert.doesNotMatch(html, /No applications waiting/);
  assert.doesNotMatch(html, /Nothing scheduled today/);
});

test("a ready-but-empty pane states each section's own empty copy", () => {
  const { paneHTML } = loadWorld([]);
  const html = paneHTML(PANE, emptyLoad);
  assert.match(html, /Worklist/); // the pane's declared title renders
  assert.match(html, /No applications waiting/);
  assert.match(html, /Nothing scheduled today/);
  assert.match(html, /No recurring series at this workplace/);
});

test("sections render in descriptor order with their declared titles", () => {
  const { paneHTML } = loadWorld([]);
  const html = paneHTML(PANE, emptyLoad);
  const apps = html.indexOf("Applications to review");
  const sched = html.indexOf("Today&#39;s schedule"); // esc() encodes the apostrophe
  const series = html.indexOf("Recurring visit series");
  assert.ok(apps >= 0 && sched > apps && series > sched);
});

test("a day-scoped section carries the resolved day in its heading", () => {
  const { paneHTML } = loadWorld([]);
  const html = paneHTML(PANE, emptyLoad);
  assert.match(html, /Today&#39;s schedule · 2026-07-20/);
});

test("a still-loading pane says so rather than answering", () => {
  const { paneHTML } = loadApp();
  assert.match(paneHTML(PANE, { status: "loading", sections: [] }), /Loading/);
  assert.match(paneHTML(PANE, { status: "idle", sections: [] }), /Loading/);
});

test("a null title column costs the label, never the row, and never a bare id", () => {
  const { paneHTML } = loadWorld([]);
  const html = paneHTML(PANE, loadedWith("applications", [
    { app_id: "AAAAAAAAAAAAAAAAAAAA", applicant_name: "", unit_address: "", qualified: null },
  ]));
  assert.match(html, /timeline-item/);
  assert.match(html, /Applicant/); // the column's declared fallback
  assert.match(html, /Unit/);      // the subtitle column's declared fallback
  assert.doesNotMatch(html, /AAAAAAAAAAAAAAAAAAAA/); // id columns never render
});

test("a badge column maps raw values through its declared valueLabels", () => {
  const { paneHTML } = loadWorld([]);
  const yes = paneHTML(PANE, loadedWith("applications", [{ applicant_name: "Row", qualified: true }]));
  assert.match(yes, /badge confirmed/);
  assert.match(yes, /qualified/);
  const no = paneHTML(PANE, loadedWith("applications", [{ applicant_name: "Row", qualified: false }]));
  assert.match(no, /badge queued/);
  assert.match(no, /incomplete/);
});

test("a null badge value renders no badge at all — three states, not two", () => {
  // null (no signal yet) must not render as either mapped label: that would
  // assert a judgement nobody has made.
  const { paneHTML } = loadWorld([]);
  const unknown = paneHTML(PANE, loadedWith("applications", [{ applicant_name: "Row", qualified: null }]));
  assert.doesNotMatch(unknown, /qualified/);
  assert.doesNotMatch(unknown, /incomplete/);
});

test("a badge column without valueLabels shows the raw value", () => {
  const { paneHTML } = loadWorld([]);
  const html = paneHTML(PANE, loadedWith("schedule", [{ patient_name: "Row", status: "booked" }]));
  assert.match(html, /badge queued/);
  assert.match(html, /booked/);
});

test("money renders at the declared unit's scale, never guessed from a name", () => {
  const { paneHTML } = loadWorld([]);
  const dollars = paneHTML(PANE, loadedWith("applications", [
    { applicant_name: "Row", terms_requested_rent: 1250.5 },
  ]));
  assert.match(dollars, /Rent \$1250\.50/);
  const cents = paneHTML(PANE, loadedWith("visitSeries", [
    { patient_name: "Row", balance_cents: 450, series_status: "active" },
  ]));
  assert.match(cents, /Balance \$4\.50/);
});

test("a meta column composes label + value + suffix", () => {
  const { paneHTML } = loadWorld([]);
  const html = paneHTML(PANE, loadedWith("visitSeries", [
    { patient_name: "Row", interval_days: 5, series_status: "active" },
  ]));
  assert.match(html, /Every 5d/);
});

test("subtitle parts join with a separator and drop empties", () => {
  const { paneHTML } = loadWorld([]);
  const html = paneHTML(PANE, loadedWith("schedule", [
    { patient_name: "Row", provider_name: "On duty", provider_specialty: "General" },
  ]));
  assert.match(html, /On duty · General/);
});

test("a time-role column renders as an HH:MM title prefix; null costs only the prefix", () => {
  const { paneHTML, timeOfDay } = loadWorld([]);
  assert.equal(timeOfDay(null), "");
  assert.equal(timeOfDay("not-a-date"), "");
  assert.equal(timeOfDay("2026-07-20T14:30:00Z"), "14:30 — ");
  const timed = paneHTML(PANE, loadedWith("schedule", [
    { patient_name: "Row", starts_at: "2026-07-20T14:30:00Z" },
  ]));
  assert.match(timed, /14:30 — Row/);
  const untimed = paneHTML(PANE, loadedWith("schedule", [
    { patient_name: "Row", starts_at: null },
  ]));
  assert.match(untimed, /Row/);
});

test("time + timeEnd render as a range", () => {
  const { paneHTML } = loadWorld([]);
  const html = paneHTML(PANE, loadedWith("schedule", [
    { patient_name: "Row", starts_at: "2026-07-20T14:30:00Z", ends_at: "2026-07-20T15:00:00Z" },
  ]));
  assert.match(html, /14:30–15:00 — Row/);
});

test("a section's dispatch declaration offers ops by targetType match", () => {
  const { paneHTML } = loadWorld([startOp, pauseOp, resumeOp]);
  const html = paneHTML(PANE, loadedWith("schedule", [
    { patient_name: "Row", patient_key: PATIENT },
  ]));
  assert.match(html, /data-open-op="manifest\.op\.0"/); // the patient-typed op
  assert.match(html, new RegExp(`data-entity-key="${PATIENT.replace(/\./g, "\\.")}"`));
  assert.match(html, /Start series/);
  // The series-typed pair does not attach to a schedule row.
  assert.doesNotMatch(html, /Pause series/);
  assert.doesNotMatch(html, /Resume series/);
});

test("a row without its target key carries no affordance", () => {
  const { paneHTML } = loadWorld([startOp]);
  const html = paneHTML(PANE, loadedWith("schedule", [
    { patient_name: "Row", patient_key: null },
  ]));
  assert.match(html, /Row/);
  assert.doesNotMatch(html, /data-open-op/);
});

test("no ops in the manifest means rows render with no buttons", () => {
  // An actor without the grant never received the op descriptor — the row
  // degrades by omitting the affordance, never by inventing one.
  const { paneHTML } = loadWorld([]);
  const html = paneHTML(PANE, loadedWith("schedule", [
    { patient_name: "Row", patient_key: PATIENT },
  ]));
  assert.doesNotMatch(html, /data-open-op/);
});

test("visibleWhen offers exactly the half of a state pair the row's state earns", () => {
  const { paneHTML } = loadWorld([startOp, pauseOp, resumeOp]);
  const active = paneHTML(PANE, loadedWith("visitSeries", [
    { patient_name: "Row", entity_key: SERIES, series_status: "active" },
  ]));
  assert.match(active, /Pause series/);
  assert.doesNotMatch(active, /Resume series/);
  assert.match(active, new RegExp(`data-entity-key="${SERIES.replace(/\./g, "\\.")}"`));

  const paused = paneHTML(PANE, loadedWith("visitSeries", [
    { patient_name: "Row", entity_key: SERIES, series_status: "paused" },
  ]));
  assert.match(paused, /Resume series/);
  assert.doesNotMatch(paused, /Pause series/);

  // The third state neither half earns: a naturally-ended series (never
  // paused, but past its own activeUntil) offers NEITHER Pause nor Resume —
  // the exact fix for the fused boolean that could not tell this apart from
  // "paused" (verticals.md).
  const ended = paneHTML(PANE, loadedWith("visitSeries", [
    { patient_name: "Row", entity_key: SERIES, series_status: "ended" },
  ]));
  assert.doesNotMatch(ended, /Pause series/);
  assert.doesNotMatch(ended, /Resume series/);
});

test("visibleWhen against a row lacking the field offers neither half — fail closed", () => {
  const { paneHTML } = loadWorld([pauseOp, resumeOp]);
  const html = paneHTML(PANE, loadedWith("visitSeries", [
    { patient_name: "Row", entity_key: SERIES },
  ]));
  assert.doesNotMatch(html, /Pause series/);
  assert.doesNotMatch(html, /Resume series/);
});

test("a state column with kind badge renders the state; hidden columns never render", () => {
  const { paneHTML } = loadWorld([]);
  const html = paneHTML(PANE, loadedWith("visitSeries", [
    { patient_name: "Row", patient_key: PATIENT, series_status: "active" },
  ]));
  assert.match(html, /badge queued/);
  assert.match(html, /active/);
  assert.doesNotMatch(html, new RegExp(PATIENT.replace(/\./g, "\\."))); // hidden role
  // A null state falls to the column's declared default before rendering.
  const nulled = paneHTML(PANE, loadedWith("visitSeries", [
    { patient_name: "Row", series_status: null },
  ]));
  assert.match(nulled, /ended/);
});

test("opVisibleForRow evaluates strictly, both arrival shapes, fail-closed", () => {
  const { opVisibleForRow } = loadApp();
  // no declaration: offered unconditionally
  assert.equal(opVisibleForRow({}, undefined), true);
  assert.equal(opVisibleForRow({ dispatchVisibleWhen: null }, { active: true }), true);
  // object arrival
  assert.equal(opVisibleForRow({ dispatchVisibleWhen: { field: "active", equals: true } }, { active: true }), true);
  assert.equal(opVisibleForRow({ dispatchVisibleWhen: { field: "active", equals: true } }, { active: false }), false);
  // JSON-string arrival (the inputSchema convention)
  assert.equal(opVisibleForRow({ dispatchVisibleWhen: '{"field":"active","equals":false}' }, { active: false }), true);
  // strict scalar equality — no coercion across JSON types
  assert.equal(opVisibleForRow({ dispatchVisibleWhen: { field: "active", equals: true } }, { active: "true" }), false);
  // missing row / missing field / null value: fail closed
  assert.equal(opVisibleForRow({ dispatchVisibleWhen: { field: "active", equals: true } }, undefined), false);
  assert.equal(opVisibleForRow({ dispatchVisibleWhen: { field: "active", equals: true } }, {}), false);
  assert.equal(opVisibleForRow({ dispatchVisibleWhen: { field: "active", equals: false } }, { active: null }), false);
  // a present-but-malformed declaration never fails open
  assert.equal(opVisibleForRow({ dispatchVisibleWhen: "not json" }, { active: true }), false);
  assert.equal(opVisibleForRow({ dispatchVisibleWhen: {} }, { active: true }), false);
});

test("a descriptor with an unparseable sections column renders no sections, no crash", () => {
  const { paneHTML } = loadApp();
  const html = paneHTML({ paneId: "p", title: "Broken", sections: "not json" }, { status: "ready", sections: [] });
  assert.match(html, /Broken/);
  assert.doesNotMatch(html, /timeline-item/);
});
