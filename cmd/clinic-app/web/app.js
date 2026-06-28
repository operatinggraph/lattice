"use strict";

// Clinic app — book · my appointments · provider schedule (Increment A). Vanilla
// JS, no build step. The Go server does all NATS I/O; this view reads
// /api/providers + /api/patients + /api/appointments and submits CreatePatient /
// CreateProvider / CreateAppointment / RescheduleAppointment / SetAppointmentStatus
// via /api/op.

const PATIENT_KEY = "clinic.patient";
const state = {
  patients: [],
  providers: [],
  appts: [],
  schedule: [],
  patient: null, // the selected patient key (the trusted-tool context)
  view: "book",
  highlight: null,
  rescheduling: null, // the appointment row being rescheduled (modal context)
  schedView: "week", // Schedule tab calendar mode: "week" | "day"
  schedAnchor: null, // a Date within the visible period (null → current week/day)
  schedSelected: null, // appointmentKey shown in the Schedule detail panel
  hoursDraft: [], // SetProviderHours windows being composed for the selected provider
  hoursProvider: null, // the provider key the draft is scoped to (reset on change)
  timeOffDraft: [], // SetProviderTimeOff ranges being edited (seeded from the provider's current ranges)
  timeOffProvider: null, // the provider key the time-off draft is scoped to (re-seeded on change)
  slotApptCache: {}, // providerKey -> existing appointments, for the booking slot picker (invalidated on book)
  slotPatientApptCache: {}, // patientKey -> the patient's appointments across all providers (cross-provider double-book exclusion; invalidated on book)
  slotCalAnchor: null, // UTC-midnight Date for the 1st of the month shown in the booking calendar (null → current UTC month)
};

const $ = (sel) => document.querySelector(sel);

// api issues a JSON request and throws Error(body.error) on an error response so
// callers can surface a single message.
async function api(path, opts) {
  const res = await fetch(path, opts);
  let body = null;
  try {
    body = await res.json();
  } catch (_) {
    /* empty/non-JSON body */
  }
  // A structured op reply carries `status` (accepted | rejected) and is returned
  // even on rejection — a rejected op is a domain outcome the caller branches on
  // via rejectionMessage()/friendlyBookingRejection(), not a transport error. Its
  // .error is an object {code, message}, which must NOT be thrown as-is (that
  // surfaces "[object Object]"). Only a real transport failure (!res.ok) or a
  // non-op error body throws — with a string message.
  if (body && typeof body.status === "string") {
    return body;
  }
  if (!res.ok || (body && body.error)) {
    const e = body && body.error;
    throw new Error((typeof e === "string" ? e : e && e.message) || `HTTP ${res.status}`);
  }
  return body;
}

function toast(msg, kind, extra) {
  const t = $("#toast");
  t.className = "toast " + (kind || "");
  t.innerHTML = "";
  t.append(document.createTextNode(msg));
  if (extra) {
    const span = document.createElement("span");
    span.className = "mono";
    span.textContent = " " + extra;
    t.append(span);
  }
  t.hidden = false;
  clearTimeout(toast._timer);
  toast._timer = setTimeout(() => (t.hidden = true), 6000);
}

function shortKey(key) {
  const i = (key || "").lastIndexOf(".");
  return i >= 0 ? key.slice(i + 1) : key || "—";
}

// ---- op submit helper ----
//
// submitOp posts an op and returns the reply, throwing on a transport error and
// returning the reply (with .status) so callers can branch on rejected.
async function submitOp(operationType, klass, payload, reads) {
  return api("/api/op", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ operationType, class: klass, payload, reads }),
  });
}

function rejectionMessage(reply) {
  if (reply && reply.status === "rejected") {
    return reply.error ? `${reply.error.code}: ${reply.error.message}` : "rejected";
  }
  return null;
}

// friendlyBookingRejection maps an op rejection message to operator-readable text
// for the booking / reschedule paths: a provider double-book (SlotConflict), a
// patient double-book across providers (PatientDoubleBook), an
// out-of-availability-window booking (OutsideHours), a date-specific time-off
// overlap (ProviderUnavailable), and a past-dated booking (ScheduleInPast) are the
// domain rejections CreateAppointment / RescheduleAppointment raise. Anything else
// passes through.
function friendlyBookingRejection(msg) {
  if (msg.indexOf("SlotConflict") !== -1) {
    return "That time overlaps another appointment for this provider. Pick another slot.";
  }
  if (msg.indexOf("PatientDoubleBook") !== -1) {
    return "This patient already has another appointment at that time. Pick a slot that does not overlap.";
  }
  if (msg.indexOf("ProviderUnavailable") !== -1) {
    return "The provider is on time-off (vacation / holiday / out) during that time. Pick a date outside their time-off.";
  }
  if (msg.indexOf("OutsideHours") !== -1) {
    return "That time is outside the provider's availability (UTC). Set hours under “Manage availability” or pick a time inside them.";
  }
  if (msg.indexOf("ScheduleInPast") !== -1) {
    return "That time is in the past. Pick a future date and time.";
  }
  return msg;
}

// ---- Patient context (the trusted-tool switcher) ----
//
// No per-user auth yet (P5/trust model): the user picks which patient they are
// from a human-readable roster (the clinicPatients lens read model, never Core
// KV). The selected key is persisted in localStorage so a refresh keeps context.

function nameForPatient(key) {
  const m = state.patients.find((p) => p.patientKey === key);
  return m && m.name ? m.name : shortKey(key);
}

function restorePatient() {
  const saved = (localStorage.getItem(PATIENT_KEY) || "").trim();
  state.patient = saved || null;
}

async function loadPatients() {
  try {
    const data = await api("/api/patients");
    state.patients = data.patients || [];
  } catch (_) {
    state.patients = [];
  }
  populatePatientSelect();
  syncBookPatient();
}

function populatePatientSelect() {
  const sel = $("#patient");
  sel.innerHTML = "";
  const placeholder = document.createElement("option");
  placeholder.value = "";
  placeholder.textContent = state.patients.length ? "Select patient…" : "No patients — add one →";
  sel.append(placeholder);
  for (const p of state.patients) {
    const o = document.createElement("option");
    o.value = p.patientKey;
    o.textContent = p.name;
    sel.append(o);
  }
  const values = state.patients.map((p) => p.patientKey);
  sel.value = state.patient && values.includes(state.patient) ? state.patient : "";
}

function setPatient(value) {
  const v = (value || "").trim();
  state.patient = v || null;
  state.highlight = null;
  if (v) localStorage.setItem(PATIENT_KEY, v);
  else localStorage.removeItem(PATIENT_KEY);
  syncBookPatient();
  // Re-render the slot picker so it excludes the newly-selected patient's existing
  // appointments (cross-provider double-book exclusion). Idempotent; bails if the
  // Book form has no provider/date yet.
  refreshSlots();
  if (state.view === "appts") loadAppts();
}

// syncBookPatient reflects the selected patient into the Book tab's read-only echo
// and enables/disables the Book button.
function syncBookPatient() {
  const echo = $("#book-patient");
  echo.textContent = state.patient ? nameForPatient(state.patient) : "Select a patient above first.";
  refreshBookEnabled();
}

// ---- New patient modal ----

function openNewPatient() {
  $("#patient-form").reset();
  $("#patient-overlay").hidden = false;
  $("#np-name").focus();
}

function closeNewPatient() {
  $("#patient-overlay").hidden = true;
}

async function submitNewPatient(ev) {
  ev.preventDefault();
  const name = $("#np-name").value.trim();
  if (!name) {
    toast("A patient name is required.", "err");
    return;
  }
  const payload = { fullName: name };
  const dob = $("#np-dob").value.trim();
  const email = $("#np-email").value.trim();
  const phone = $("#np-phone").value.trim();
  // The .demographics aspect stores dob as an RFC3339 instant.
  if (dob) payload.dob = dob.length === 10 ? dob + "T00:00:00Z" : dob;
  if (email) payload.email = email;
  if (phone) payload.phone = phone;

  const submit = $("#patient-submit");
  submit.disabled = true;
  try {
    const reply = await submitOp("CreatePatient", "patient", payload);
    const msg = rejectionMessage(reply);
    if (msg) {
      toast("Could not create patient — " + msg, "err");
      return;
    }
    const key = reply && reply.primaryKey ? reply.primaryKey : "";
    closeNewPatient();
    toast("Patient created.", "ok", key);
    // Make the new patient the active context (the lens may take a moment to
    // project; select it now and reload so the switcher shows it once projected).
    if (key) {
      state.patient = key;
      localStorage.setItem(PATIENT_KEY, key);
    }
    await loadPatients();
  } catch (e) {
    toast("Could not create patient: " + e.message, "err");
  } finally {
    submit.disabled = false;
  }
}

// ---- Providers (booking picker + inline add) ----

async function loadProviders() {
  try {
    const data = await api("/api/providers");
    state.providers = data.providers || [];
  } catch (_) {
    state.providers = [];
  }
  populateProviderSelect("#provider");
  populateProviderSelect("#sched-provider", { includeAll: true });
  populateProviderSelect("#avail-provider");
  refreshBookEnabled();
  renderSlotCalendar();
  renderAvailEditors();
}

// renderAvailEditors re-seeds (only if the selected provider changed) and renders
// both Availability-tab editors against #avail-provider. Safe to call any time —
// after a roster refresh the selection is preserved, so a just-saved draft is kept.
function renderAvailEditors() {
  hoursDraftForSelectedProvider();
  renderHoursDraft();
  timeOffDraftForSelectedProvider();
  renderTimeOffDraft();
}

function providerLabel(p) {
  return p.specialty ? `${p.name} · ${p.specialty}` : p.name;
}

// SCHED_ALL is the sentinel select value for the clinic-wide "All providers"
// schedule view (every provider's appointments on one grid). It is offered only on
// the Schedule picker (includeAll), never the booking picker — you book one provider.
const SCHED_ALL = "__all__";

function populateProviderSelect(sel, opts) {
  const el = $(sel);
  if (!el) return;
  const prev = el.value;
  el.innerHTML = "";
  const placeholder = document.createElement("option");
  placeholder.value = "";
  placeholder.textContent = state.providers.length ? "Select provider…" : "No providers — add one below";
  el.append(placeholder);
  if (opts && opts.includeAll && state.providers.length) {
    const all = document.createElement("option");
    all.value = SCHED_ALL;
    all.textContent = "All providers (clinic-wide)";
    el.append(all);
  }
  for (const p of state.providers) {
    const o = document.createElement("option");
    o.value = p.providerKey;
    o.textContent = providerLabel(p);
    el.append(o);
  }
  const values = state.providers.map((p) => p.providerKey);
  if (prev === SCHED_ALL && opts && opts.includeAll) {
    el.value = SCHED_ALL;
  } else {
    el.value = values.includes(prev) ? prev : "";
  }
}

async function submitAddProvider() {
  const name = $("#np-prov-name").value.trim();
  const specialty = $("#np-prov-specialty").value.trim();
  const credentials = $("#np-prov-credentials").value.trim();
  if (!name || !specialty) {
    toast("Provider name and specialty are required.", "err");
    return;
  }
  const payload = { fullName: name, specialty };
  if (credentials) payload.credentials = credentials;

  const btn = $("#add-provider-submit");
  btn.disabled = true;
  try {
    const reply = await submitOp("CreateProvider", "provider", payload);
    const msg = rejectionMessage(reply);
    if (msg) {
      toast("Could not add provider — " + msg, "err");
      return;
    }
    const key = reply && reply.primaryKey ? reply.primaryKey : "";
    $("#np-prov-name").value = "";
    $("#np-prov-specialty").value = "";
    $("#np-prov-credentials").value = "";
    $("#add-provider").open = false;
    toast("Provider added.", "ok", key);
    await loadProviders();
    // Pre-select the new provider in the booking form once projected.
    if (key) $("#provider").value = key;
  } catch (e) {
    toast("Could not add provider: " + e.message, "err");
  } finally {
    btn.disabled = false;
  }
}

// ---- Provider availability (SetProviderHours) ----
//
// The editor composes a UTC weekly-availability window list for the provider
// selected in the Book form and submits SetProviderHours (which REPLACES the
// provider's .hours aspect). Like the time-off manager this is READ-MODIFY-WRITE:
// the draft is SEEDED from the provider's currently-projected .hours windows (the
// clinicProviders lens carries them), so Add / Remove edits the live set and Save
// replaces the whole list — adding one window no longer silently wipes a provider's
// existing hours. Times are UTC to match the op's UTC weekday / seconds-of-day
// enforcement.

const DAY_NAMES = ["Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"];
const MONTH_NAMES = ["January", "February", "March", "April", "May", "June", "July", "August", "September", "October", "November", "December"];

function hmsToSeconds(hhmm) {
  const m = /^(\d{2}):(\d{2})$/.exec(hhmm || "");
  if (!m) return null;
  const h = Number(m[1]);
  const min = Number(m[2]);
  if (h > 23 || min > 59) return null;
  return h * 3600 + min * 60;
}

function secondsToHMS(sec) {
  const pad = (n) => String(n).padStart(2, "0");
  return `${pad(Math.floor(sec / 3600))}:${pad(Math.floor((sec % 3600) / 60))}`;
}

// hoursDraftForSelectedProvider returns the Availability tab's selected provider
// key, re-seeding the draft from that provider's currently-projected .hours windows
// whenever the selection changed (so one provider's edits can't be saved onto
// another, and so an Add/Remove edits the live set rather than starting blank).
function hoursDraftForSelectedProvider() {
  const prov = $("#avail-provider").value;
  if (prov !== state.hoursProvider) {
    state.hoursProvider = prov;
    const p = providerByKey(prov);
    // Clone so editing the draft doesn't mutate the loaded provider row.
    state.hoursDraft = p && Array.isArray(p.hours)
      ? p.hours.map((w) => ({ day: w.day, openSec: w.openSec, closeSec: w.closeSec }))
      : [];
  }
  return prov;
}

function renderHoursDraft() {
  const list = $("#hours-list");
  if (!list) return;
  list.innerHTML = "";
  if (!state.hoursDraft.length) {
    const p = document.createElement("p");
    p.className = "muted";
    p.textContent = "No windows added — saving clears this provider's hours (always available).";
    list.appendChild(p);
    return;
  }
  state.hoursDraft.forEach((w, i) => {
    const row = document.createElement("div");
    row.className = "hours-row";
    const label = document.createElement("span");
    label.textContent = `${DAY_NAMES[w.day]} ${secondsToHMS(w.openSec)}–${secondsToHMS(w.closeSec)} UTC`;
    const rm = document.createElement("button");
    rm.type = "button";
    rm.className = "ghost danger";
    rm.textContent = "Remove";
    rm.addEventListener("click", () => {
      state.hoursDraft.splice(i, 1);
      renderHoursDraft();
    });
    row.appendChild(label);
    row.appendChild(rm);
    list.appendChild(row);
  });
}

function addHoursWindow() {
  if (!hoursDraftForSelectedProvider()) {
    toast("Select a provider first.", "err");
    return;
  }
  const day = Number($("#hours-day").value);
  const openSec = hmsToSeconds($("#hours-open").value);
  const closeSec = hmsToSeconds($("#hours-close").value);
  if (openSec === null || closeSec === null) {
    toast("Enter valid open and close times.", "err");
    return;
  }
  if (openSec >= closeSec) {
    toast("Open time must be before close time.", "err");
    return;
  }
  state.hoursDraft.push({ day, openSec, closeSec });
  renderHoursDraft();
}

async function saveProviderHours() {
  const provider = hoursDraftForSelectedProvider();
  if (!provider) {
    toast("Select a provider first.", "err");
    return;
  }
  const btn = $("#hours-save");
  btn.disabled = true;
  try {
    const reply = await submitOp("SetProviderHours", "provider",
      { providerKey: provider, windows: state.hoursDraft }, [provider]);
    const msg = rejectionMessage(reply);
    if (msg) {
      toast("Could not set hours — " + msg, "err");
      return;
    }
    const n = state.hoursDraft.length;
    toast(n ? `Availability saved (${n} window${n === 1 ? "" : "s"}).` : "Availability cleared (always available).", "ok");
    // Refresh the roster so the persisted windows back the booking slot picker + the
    // editor (the lens may take a moment to project; selection is preserved). Mirrors
    // saveProviderTimeOff.
    loadProviders();
  } catch (e) {
    toast("Could not set hours: " + e.message, "err");
  } finally {
    btn.disabled = false;
  }
}

// ---- Provider time-off (SetProviderTimeOff) ----
//
// Date-specific blackout ranges on top of the recurring .hours. Unlike the hours
// editor, this is READ-MODIFY-WRITE: the draft is SEEDED from the provider's
// currently-projected .timeOff ranges (the clinicProviders lens now carries them),
// so Add / Remove edits the live set and Save replaces the whole list via
// SetProviderTimeOff. Ranges are whole-day, UTC, half-open [from, to): a single
// blocked day D is stored {from: D 00:00Z, to: (D+1) 00:00Z}.

function providerByKey(key) {
  return state.providers.find((p) => p.providerKey === key) || null;
}

// dayStartUTC turns a "YYYY-MM-DD" date input into the canonical UTC RFC3339
// instant at that day's start (00:00:00Z).
function dayStartUTC(dateStr) {
  if (!/^\d{4}-\d{2}-\d{2}$/.test(dateStr || "")) return "";
  return dateStr + "T00:00:00Z";
}

// nextDayStartUTC turns a "YYYY-MM-DD" into the start of the FOLLOWING day in UTC —
// the exclusive end of a whole-day block (so a To of D blocks all of day D).
function nextDayStartUTC(dateStr) {
  const start = dayStartUTC(dateStr);
  if (!start) return "";
  const d = new Date(start);
  if (isNaN(d)) return "";
  d.setUTCDate(d.getUTCDate() + 1);
  return d.toISOString().replace(/\.\d{3}Z$/, "Z");
}

// timeOffRangeLabel renders a stored {from, to} (to is the exclusive next-day start)
// as an inclusive human range: "Jul 1" for a single day, "Jul 1 – Jul 5" for a span.
function timeOffRangeLabel(r) {
  const from = new Date(r.from);
  const toExcl = new Date(r.to);
  if (isNaN(from) || isNaN(toExcl)) return `${r.from} → ${r.to}`;
  const incl = new Date(toExcl.getTime() - 86400000); // exclusive end − 1 day = inclusive last day
  const opts = { timeZone: "UTC", month: "short", day: "numeric", year: "numeric" };
  const f = from.toLocaleDateString(undefined, opts);
  const t = incl.toLocaleDateString(undefined, opts);
  return f === t ? f : `${f} – ${t}`;
}

// timeOffDraftForSelectedProvider returns the Availability tab's selected provider
// key, re-seeding the draft from that provider's currently-projected ranges whenever
// the selection changed (so one provider's edits can't be saved onto another).
function timeOffDraftForSelectedProvider() {
  const prov = $("#avail-provider").value;
  if (prov !== state.timeOffProvider) {
    state.timeOffProvider = prov;
    const p = providerByKey(prov);
    // Clone so editing the draft doesn't mutate the loaded provider row.
    state.timeOffDraft = p && Array.isArray(p.timeOff)
      ? p.timeOff.map((r) => ({ from: r.from, to: r.to, reason: r.reason }))
      : [];
  }
  return prov;
}

function renderTimeOffDraft() {
  const list = $("#timeoff-list");
  if (!list) return;
  list.innerHTML = "";
  if (!state.timeOffDraft.length) {
    const p = document.createElement("p");
    p.className = "muted";
    p.textContent = "No time-off — this provider has no blocked dates.";
    list.appendChild(p);
    return;
  }
  state.timeOffDraft.forEach((r, i) => {
    const row = document.createElement("div");
    row.className = "hours-row";
    const label = document.createElement("span");
    label.textContent = timeOffRangeLabel(r) + (r.reason ? ` · ${r.reason}` : "");
    const rm = document.createElement("button");
    rm.type = "button";
    rm.className = "ghost danger";
    rm.textContent = "Remove";
    rm.addEventListener("click", () => {
      state.timeOffDraft.splice(i, 1);
      renderTimeOffDraft();
    });
    row.appendChild(label);
    row.appendChild(rm);
    list.appendChild(row);
  });
}

function addTimeOffRange() {
  if (!timeOffDraftForSelectedProvider()) {
    toast("Select a provider first.", "err");
    return;
  }
  const fromStr = $("#timeoff-from").value;
  const toStr = $("#timeoff-to").value;
  if (!fromStr || !toStr) {
    toast("Pick a From and To date.", "err");
    return;
  }
  if (toStr < fromStr) {
    toast("The To date must be on or after the From date.", "err");
    return;
  }
  const from = dayStartUTC(fromStr);
  const to = nextDayStartUTC(toStr);
  if (!from || !to) {
    toast("Those dates are not valid.", "err");
    return;
  }
  const range = { from, to };
  const reason = $("#timeoff-reason").value.trim();
  if (reason) range.reason = reason;
  state.timeOffDraft.push(range);
  $("#timeoff-reason").value = "";
  renderTimeOffDraft();
}

async function saveProviderTimeOff() {
  const provider = timeOffDraftForSelectedProvider();
  if (!provider) {
    toast("Select a provider first.", "err");
    return;
  }
  const btn = $("#timeoff-save");
  btn.disabled = true;
  try {
    // SetProviderTimeOff replaces the whole .timeOff aspect; ranges=[] clears it.
    // The op re-normalizes from/to to canonical UTC and validates from < to.
    const reply = await submitOp("SetProviderTimeOff", "provider",
      { providerKey: provider, ranges: state.timeOffDraft }, [provider]);
    const msg = rejectionMessage(reply);
    if (msg) {
      toast("Could not set time-off — " + msg, "err");
      return;
    }
    const n = state.timeOffDraft.length;
    toast(n ? `Time-off saved (${n} range${n === 1 ? "" : "s"}).` : "Time-off cleared (no blocked dates).", "ok");
    // Refresh the roster so the persisted ranges back the booking warning + the
    // editor (the lens may take a moment to project; selection is preserved).
    loadProviders();
  } catch (e) {
    toast("Could not set time-off: " + e.message, "err");
  } finally {
    btn.disabled = false;
  }
}

// refreshTimeOffWarning shows a soft heads-up under the Date & time field when the
// chosen start falls inside the selected provider's projected time-off. The op
// (ProviderUnavailable) is the authority; this just warns before submit.
function refreshTimeOffWarning() {
  const el = $("#timeoff-warning");
  if (!el) return;
  el.hidden = true;
  el.textContent = "";
  const p = providerByKey($("#provider").value);
  const when = $("#startsAt").value;
  if (!p || !Array.isArray(p.timeOff) || !p.timeOff.length || !when) return;
  const start = new Date(toRFC3339(when));
  if (isNaN(start)) return;
  const hit = p.timeOff.find((r) => {
    const from = new Date(r.from);
    const to = new Date(r.to);
    return !isNaN(from) && !isNaN(to) && start >= from && start < to;
  });
  if (hit) {
    el.textContent = `Heads up: ${p.name} is on time-off then (${timeOffRangeLabel(hit)}). The booking will be rejected.`;
    el.hidden = false;
  }
}

// ---- Available-slot picker ----
// Suggests the provider's open appointment starts for a chosen date, computed from
// the same inputs the op enforces — the .hours availability windows (enforce_hours),
// the .timeOff blackouts (enforce_time_off), the provider's existing appointments
// (the double-book check), and the past-time guard (ScheduleInPast). A suggested
// slot is built so it passes those checks; the op stays the authority.

// providerAppointments fetches (and caches per provider) the provider's existing
// appointments. The cache is invalidated on a successful booking so a just-booked
// slot stops being offered.
async function providerAppointments(provider) {
  if (state.slotApptCache[provider]) return state.slotApptCache[provider];
  try {
    const data = await api("/api/appointments?provider=" + encodeURIComponent(provider));
    state.slotApptCache[provider] = data.appointments || [];
  } catch (e) {
    state.slotApptCache[provider] = [];
  }
  return state.slotApptCache[provider];
}

// patientAppointments fetches (and caches per patient) the selected patient's
// appointments across ALL providers — so the slot picker can exclude a time the
// patient is already booked elsewhere, which the op rejects as a PatientDoubleBook.
// Invalidated on a successful booking alongside the provider cache.
async function patientAppointments(patient) {
  if (state.slotPatientApptCache[patient]) return state.slotPatientApptCache[patient];
  try {
    const data = await api("/api/appointments?patient=" + encodeURIComponent(patient));
    state.slotPatientApptCache[patient] = data.appointments || [];
  } catch (e) {
    state.slotPatientApptCache[patient] = [];
  }
  return state.slotPatientApptCache[patient];
}

// apptBlocks reports whether an appointment still occupies its slot. A cancelled /
// no-show appointment is removed from the provider's .bookings conflict index, so
// the op would allow rebooking that time — exclude it from the picker's block set.
function apptBlocks(a) {
  return a.status !== "cancelled" && a.status !== "noShow";
}

// computeOpenSlots derives the provider's open appointment starts (UTC ms) for a
// calendar date (interpreted as a UTC day, matching how .hours windows are keyed by
// UTC weekday + seconds-of-day). durationMin is both the slot length and the step,
// so suggested slots are back-to-back at the appointment length. A slot is dropped
// when it is in the past, overlaps any time-off range, or overlaps a live
// appointment — the same conditions the op rejects. `appts` carries both the
// provider's appointments (the provider double-book / SlotConflict check) and the
// selected patient's appointments across all providers (the cross-provider
// PatientDoubleBook check), so the picker never offers a slot the op would reject.
function computeOpenSlots(p, dateStr, durationMin, appts, nowMs) {
  if (!p || !Array.isArray(p.hours) || !p.hours.length || !dateStr) return [];
  const dayStart = Date.parse(dateStr + "T00:00:00Z");
  if (isNaN(dayStart)) return [];
  const weekday = new Date(dayStart).getUTCDay();
  const durMs = durationMin * 60000;
  const stepSec = Math.max(durationMin, 15) * 60;
  const timeOff = Array.isArray(p.timeOff) ? p.timeOff : [];
  const blocking = (appts || [])
    .filter(apptBlocks)
    .map((a) => ({ s: Date.parse(a.startsAt), e: Date.parse(a.endsAt) }))
    .filter((x) => !isNaN(x.s) && !isNaN(x.e));
  const slots = [];
  const seen = new Set();
  for (const w of p.hours) {
    if (w.day !== weekday) continue;
    for (let sec = w.openSec; sec + durationMin * 60 <= w.closeSec; sec += stepSec) {
      const s = dayStart + sec * 1000;
      const e = s + durMs;
      if (s <= nowMs) continue; // past — matches ScheduleInPast (start <= submittedAt)
      const offHit = timeOff.some((r) => {
        const rf = Date.parse(r.from), rt = Date.parse(r.to);
        return !isNaN(rf) && !isNaN(rt) && s < rt && e > rf;
      });
      if (offHit) continue;
      if (blocking.some((b) => s < b.e && e > b.s)) continue;
      if (seen.has(s)) continue;
      seen.add(s);
      slots.push(s);
    }
  }
  slots.sort((a, b) => a - b);
  return slots;
}

// slotTimeLabel renders a slot's UTC instant as the local clock time the button
// shows; the click fills #startsAt (a local datetime-local value) with the same
// instant, which round-trips back to this UTC start on submit.
function slotTimeLabel(ms) {
  const d = new Date(ms);
  const pad = (n) => String(n).padStart(2, "0");
  let h = d.getHours();
  const m = pad(d.getMinutes());
  const ap = h < 12 ? "AM" : "PM";
  h = h % 12;
  if (h === 0) h = 12;
  return `${h}:${m} ${ap}`;
}

// noSlotsReason names why a date has no open slots, distinguishing the two
// "blocked date" cases the picker should call out — the provider doesn't work
// that weekday, or is on time-off that whole day — from the generic
// fully-booked / duration fallback (returns "" for that case so the caller
// keeps its default line). Mirrors computeOpenSlots' UTC-day interpretation so
// the reason matches the slots that would be shown. Time-off ranges are authored
// whole-day (.timeOff stores [from, (to+1day)) UTC), so a date is "on time-off"
// when the full UTC day falls inside one range.
function noSlotsReason(p, dateStr) {
  const dayStart = Date.parse(dateStr + "T00:00:00Z");
  if (isNaN(dayStart)) return "";
  const dayEnd = dayStart + 86400000;
  const weekday = new Date(dayStart).getUTCDay();
  const timeOff = Array.isArray(p.timeOff) ? p.timeOff : [];
  const cover = timeOff.find((r) => {
    const rf = Date.parse(r.from), rt = Date.parse(r.to);
    return !isNaN(rf) && !isNaN(rt) && rf <= dayStart && dayEnd <= rt;
  });
  if (cover) {
    return `${p.name} is on time-off that day` + (cover.reason ? ` (${cover.reason})` : "") + " — pick another date.";
  }
  const days = [...new Set(p.hours.map((w) => w.day))].sort((a, b) => a - b);
  if (!days.includes(weekday)) {
    const list = days.map((d) => DAY_NAMES[d].slice(0, 3)).join(", ");
    return `${p.name} doesn't see patients on ${DAY_NAMES[weekday]}s — available ${list}.`;
  }
  return "";
}

// ---- Booking date calendar ----
// A custom month grid for choosing the booking date, so days the provider can't be
// booked on are greyed out — the native <input type=date> can't exclude arbitrary
// dates. A date is interpreted as a UTC day (matching how .hours, .timeOff, and
// computeOpenSlots are keyed), so the grid is built in UTC: columns are UTC weekdays
// and each cell is a UTC calendar day. Blocking mirrors the op's rejections at the
// whole-day grain — a working day that happens to be fully booked stays enabled (the
// slots area then explains it). The op stays the booking authority.

// ymdUTC formats a UTC year/month/day as the "YYYY-MM-DD" string the slot picker and
// computeOpenSlots consume (parsed back as <date>T00:00:00Z).
function ymdUTC(y, m, d) {
  const pad = (n) => String(n).padStart(2, "0");
  return `${y}-${pad(m + 1)}-${pad(d)}`;
}

// dayBlockedReason reports why a whole UTC calendar day is unbookable for a provider,
// or "" when at least part of the day could be booked. Past days, a weekday the
// provider doesn't work, and a whole-day time-off range are blocked.
function dayBlockedReason(p, y, m, d, nowMs) {
  const dayStart = Date.UTC(y, m, d);
  const dayEnd = dayStart + 86400000;
  if (dayEnd <= nowMs) return "Past date.";
  const weekday = new Date(dayStart).getUTCDay();
  const days = [...new Set((p.hours || []).map((w) => w.day))];
  if (!days.includes(weekday)) return `${p.name} doesn't see patients on ${DAY_NAMES[weekday]}s.`;
  const timeOff = Array.isArray(p.timeOff) ? p.timeOff : [];
  const cover = timeOff.find((r) => {
    const rf = Date.parse(r.from), rt = Date.parse(r.to);
    return !isNaN(rf) && !isNaN(rt) && rf <= dayStart && dayEnd <= rt;
  });
  if (cover) return `Time off${cover.reason ? ` (${cover.reason})` : ""}.`;
  return "";
}

// renderSlotCalendar draws the month grid for the selected provider. Picking an
// enabled day sets #slot-date and refreshes the open-slot list below.
function renderSlotCalendar() {
  const box = $("#slot-calendar");
  if (!box) return;
  box.innerHTML = "";
  const p = providerByKey($("#provider").value);
  if (!p) {
    const m = document.createElement("p");
    m.className = "cal-empty";
    m.textContent = "Select a provider to see available dates.";
    box.appendChild(m);
    return;
  }
  if (!Array.isArray(p.hours) || !p.hours.length) {
    const m = document.createElement("p");
    m.className = "cal-empty";
    m.textContent = "This provider has set no availability hours — enter a date & time above directly.";
    box.appendChild(m);
    return;
  }

  const now = new Date();
  const nowMs = Date.now();
  const curMonthStart = Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), 1);
  if (!state.slotCalAnchor || state.slotCalAnchor.getTime() < curMonthStart) {
    state.slotCalAnchor = new Date(curMonthStart);
  }
  const anchor = state.slotCalAnchor;
  const y = anchor.getUTCFullYear();
  const m = anchor.getUTCMonth();
  const selected = $("#slot-date").value;
  const todayStr = ymdUTC(now.getUTCFullYear(), now.getUTCMonth(), now.getUTCDate());

  const head = document.createElement("div");
  head.className = "cal-head";
  const prev = document.createElement("button");
  prev.type = "button";
  prev.className = "cal-step";
  prev.textContent = "‹";
  prev.setAttribute("aria-label", "Previous month");
  prev.disabled = Date.UTC(y, m, 1) <= curMonthStart; // no fully-past months
  prev.addEventListener("click", () => {
    state.slotCalAnchor = new Date(Date.UTC(y, m - 1, 1));
    renderSlotCalendar();
  });
  const title = document.createElement("span");
  title.className = "cal-title";
  title.textContent = `${MONTH_NAMES[m]} ${y}`;
  const next = document.createElement("button");
  next.type = "button";
  next.className = "cal-step";
  next.textContent = "›";
  next.setAttribute("aria-label", "Next month");
  next.addEventListener("click", () => {
    state.slotCalAnchor = new Date(Date.UTC(y, m + 1, 1));
    renderSlotCalendar();
  });
  head.append(prev, title, next);
  box.appendChild(head);

  const grid = document.createElement("div");
  grid.className = "cal-grid";
  for (const dow of ["S", "M", "T", "W", "T", "F", "S"]) {
    const c = document.createElement("div");
    c.className = "cal-dow";
    c.textContent = dow;
    grid.appendChild(c);
  }
  const firstDow = new Date(Date.UTC(y, m, 1)).getUTCDay();
  const daysInMonth = new Date(Date.UTC(y, m + 1, 0)).getUTCDate();
  for (let i = 0; i < firstDow; i++) {
    const c = document.createElement("div");
    c.className = "cal-day empty";
    grid.appendChild(c);
  }
  for (let d = 1; d <= daysInMonth; d++) {
    const dateStr = ymdUTC(y, m, d);
    const reason = dayBlockedReason(p, y, m, d, nowMs);
    const cell = document.createElement("button");
    cell.type = "button";
    cell.className = "cal-day";
    cell.textContent = String(d);
    if (dateStr === todayStr) cell.classList.add("today");
    if (reason) {
      cell.classList.add("disabled");
      cell.disabled = true;
      cell.title = reason;
    } else {
      if (dateStr === selected) cell.classList.add("selected");
      cell.addEventListener("click", () => {
        $("#slot-date").value = dateStr;
        renderSlotCalendar();
        refreshSlots();
      });
    }
    grid.appendChild(cell);
  }
  box.appendChild(grid);
}

// refreshSlots re-renders the open-slot buttons for the selected provider + date +
// duration. Idempotent and safe to call on any of those changing.
async function refreshSlots() {
  const box = $("#slots");
  if (!box) return;
  box.innerHTML = "";
  const provider = $("#provider").value;
  const dateStr = $("#slot-date").value;
  if (!provider || !dateStr) return;
  const p = providerByKey(provider);
  if (!p) return;
  if (!Array.isArray(p.hours) || !p.hours.length) {
    const m = document.createElement("p");
    m.className = "muted";
    m.textContent = "This provider has set no availability hours — enter a date & time above directly.";
    box.appendChild(m);
    return;
  }
  const durationMin = Number($("#duration").value || 30);
  const provAppts = await providerAppointments(provider);
  // The provider's appointments AND the selected patient's appointments (across all
  // providers) both block a slot — the latter so the picker doesn't offer a time the
  // op rejects as a cross-provider PatientDoubleBook.
  const patAppts = state.patient ? await patientAppointments(state.patient) : [];
  // The provider/date/patient may have changed while awaiting the fetches — bail if so.
  if ($("#provider").value !== provider || $("#slot-date").value !== dateStr) return;
  const slots = computeOpenSlots(p, dateStr, durationMin, provAppts.concat(patAppts), Date.now());
  if (!slots.length) {
    const m = document.createElement("p");
    m.className = "muted";
    m.textContent = noSlotsReason(p, dateStr) || "No open slots that day — try another date or a shorter duration.";
    box.appendChild(m);
    return;
  }
  const chosen = $("#startsAt").value ? toRFC3339($("#startsAt").value) : "";
  for (const ms of slots) {
    const iso = new Date(ms).toISOString().replace(/\.\d{3}Z$/, "Z");
    const btn = document.createElement("button");
    btn.type = "button";
    btn.className = "slot-btn";
    btn.textContent = slotTimeLabel(ms);
    if (chosen && chosen === iso) btn.classList.add("selected");
    btn.addEventListener("click", () => {
      $("#startsAt").value = toLocalInputValue(iso);
      refreshTimeOffWarning();
      refreshSlots();
    });
    box.appendChild(btn);
  }
}

// ---- Book ----

function refreshBookEnabled() {
  const btn = $("#book-submit");
  if (!btn) return;
  const ready = !!state.patient && !!$("#provider").value;
  btn.disabled = !ready;
  btn.title = !state.patient ? "Select a patient first" : !$("#provider").value ? "Select a provider" : "";
}

// toRFC3339 converts a datetime-local value (local wall time, no zone) to a
// canonical UTC RFC3339 instant, as the .schedule aspect expects.
function toRFC3339(localValue) {
  const d = new Date(localValue);
  if (isNaN(d)) return "";
  return d.toISOString().replace(/\.\d{3}Z$/, "Z");
}

function addMinutesRFC3339(localValue, minutes) {
  const d = new Date(localValue);
  if (isNaN(d)) return "";
  d.setMinutes(d.getMinutes() + minutes);
  return d.toISOString().replace(/\.\d{3}Z$/, "Z");
}

// toLocalInputValue formats a stored RFC3339 (UTC) instant back into the local
// "YYYY-MM-DDTHH:MM" a <input type=datetime-local> expects, for prefilling the
// reschedule modal with the appointment's current time.
function toLocalInputValue(rfc3339) {
  const d = new Date(rfc3339);
  if (isNaN(d)) return "";
  const pad = (n) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

// nowLocalInputValue returns the current local wall time as the
// "YYYY-MM-DDTHH:MM" a <input type=datetime-local min=...> expects. Used as a
// first line of defence so the browser's own picker discourages a past time; the
// CreateAppointment / RescheduleAppointment op is the authority (ScheduleInPast).
function nowLocalInputValue() {
  return toLocalInputValue(new Date().toISOString());
}

// durationMinutes derives the appointment length (minutes) from its start/end so
// the reschedule modal can prefill the duration select.
function durationMinutes(startsAt, endsAt) {
  const s = new Date(startsAt);
  const e = new Date(endsAt);
  if (isNaN(s) || isNaN(e) || e <= s) return 30;
  return Math.round((e - s) / 60000);
}

async function submitBook(ev) {
  ev.preventDefault();
  if (!state.patient) {
    toast("Select a patient first.", "err");
    return;
  }
  const provider = $("#provider").value;
  if (!provider) {
    toast("Select a provider.", "err");
    return;
  }
  const when = $("#startsAt").value;
  if (!when) {
    toast("Pick a date and time.", "err");
    return;
  }
  const startsAt = toRFC3339(when);
  const endsAt = addMinutesRFC3339(when, Number($("#duration").value || 30));
  if (!startsAt || !endsAt) {
    toast("That date/time is not valid.", "err");
    return;
  }

  const payload = { patient: state.patient, provider, startsAt, endsAt };
  const reason = $("#reason").value.trim();
  if (reason) payload.reason = reason;

  const submit = $("#book-submit");
  submit.disabled = true;
  try {
    // The provider's AND the patient's .bookings indexes are declared reads so the
    // op can detect a provider double-book (SlotConflict) AND a patient double-book
    // across providers (PatientDoubleBook), and so the OCC checks serialize
    // concurrent bookings on both sides.
    const reply = await submitOp("CreateAppointment", "appointment", payload,
      [state.patient, provider, provider + ".bookings", state.patient + ".bookings"]);
    const msg = rejectionMessage(reply);
    if (msg) {
      toast("Booking rejected — " + friendlyBookingRejection(msg), "err");
      return;
    }
    const key = reply && reply.primaryKey ? reply.primaryKey : "";
    // The new appointment invalidates this provider's AND this patient's cached
    // slot sets (the patient now has one more appointment to exclude elsewhere).
    delete state.slotApptCache[provider];
    delete state.slotPatientApptCache[state.patient];
    $("#book-form").reset();
    refreshSlots();
    toast("Appointment booked.", "ok", key);
    // Route to My Appointments with the new appointment highlighted (the lens may
    // take a moment to project; a Refresh shows it once projected).
    state.highlight = key || null;
    showView("appts");
  } catch (e) {
    toast("Could not book: " + e.message, "err");
  } finally {
    refreshBookEnabled();
  }
}

// ---- My Appointments (scoped to the selected patient) ----

async function loadAppts() {
  const grid = $("#appts");
  const empty = $("#appts-empty");
  if (!state.patient) {
    grid.innerHTML = "";
    state.appts = [];
    empty.hidden = false;
    empty.textContent = "Select a patient above to see their appointments.";
    $("#appts-summary").textContent = "";
    return;
  }
  $("#appts-summary").textContent = "loading…";
  try {
    const data = await api("/api/appointments?patient=" + encodeURIComponent(state.patient));
    state.appts = data.appointments || [];
  } catch (e) {
    grid.innerHTML = "";
    empty.hidden = false;
    empty.textContent = "Could not load appointments: " + e.message;
    $("#appts-summary").textContent = "";
    return;
  }
  renderAppts();
}

// apptMatchesFilter applies the My Appointments status filter ("all" | "active" |
// a single status). "active" keeps the non-terminal lifecycle states.
function apptMatchesFilter(a, filter) {
  if (filter === "all") return true;
  if (filter === "active") return ACTIVE_STATUSES.includes(a.status);
  return a.status === filter;
}

function renderAppts() {
  const grid = $("#appts");
  const empty = $("#appts-empty");
  grid.innerHTML = "";
  if (state.appts.length === 0) {
    empty.hidden = false;
    empty.textContent = "No appointments yet. Book one on the Book tab.";
    $("#appts-summary").textContent = "";
    return;
  }

  const filter = ($("#appts-filter") && $("#appts-filter").value) || "all";
  const matched = state.appts.filter((a) => apptMatchesFilter(a, filter));

  // Split upcoming vs past so the patient's next appointment leads (the API sorts
  // ascending, which otherwise buries it under accumulated history). Upcoming reads
  // soonest-first; Past reads most-recent-first.
  const upcoming = matched.filter((a) => !isPast(a.startsAt));
  const past = matched.filter((a) => isPast(a.startsAt)).reverse();

  if (matched.length === 0) {
    empty.hidden = false;
    empty.textContent = "No appointments match this filter.";
    $("#appts-summary").textContent = `0 of ${state.appts.length}`;
    return;
  }
  empty.hidden = true;

  const section = (label, rows) => {
    if (rows.length === 0) return;
    const head = document.createElement("div");
    head.className = "appts-section-head";
    head.textContent = `${label} · ${rows.length}`;
    grid.append(head);
    for (const a of rows) grid.append(renderApptCard(a, { showProvider: true, cancelable: true }));
  };
  section("Upcoming", upcoming);
  section("Past", past);

  const n = matched.length;
  const suffix = filter === "all" ? "" : ` of ${state.appts.length}`;
  $("#appts-summary").textContent = `${n} appointment${n === 1 ? "" : "s"}${suffix}`;
}

// ---- Provider Schedule (read-only day/week calendar desk view) ----
//
// The Schedule tab is a positioned calendar grid: a time axis down the left, one
// column per day (7 in Week view, 1 in Day view), and each appointment rendered as
// a block sized to its duration and coloured by status. /api/appointments?provider=
// returns the provider's full history; the grid filters client-side to the visible
// period (no date-range query needed). Clicking a block opens a read-only detail
// panel — the desk view doesn't mutate (Cancel / Reschedule live on My Appointments).

const PX_PER_HOUR = 44;

// schedIsAll reports whether the Schedule tab is in clinic-wide "All providers"
// mode (every provider on one grid), vs scoped to a single provider.
function schedIsAll() {
  return $("#sched-provider").value === SCHED_ALL;
}

async function loadSchedule() {
  const provider = $("#sched-provider").value;
  const empty = $("#schedule-empty");
  hideSchedDetail();
  if (!provider) {
    $("#schedule").innerHTML = "";
    state.schedule = [];
    empty.hidden = false;
    empty.textContent = "Choose a provider — or All providers — to see the schedule.";
    $("#schedule-summary").textContent = "";
    $("#sched-range").textContent = "";
    return;
  }
  $("#schedule-summary").textContent = "loading…";
  try {
    // All-providers mode fetches the unfiltered read model (every appointment);
    // a single provider scopes server-side. Either way the grid filters to the
    // visible period client-side.
    const q = provider === SCHED_ALL ? "" : "?provider=" + encodeURIComponent(provider);
    const data = await api("/api/appointments" + q);
    state.schedule = data.appointments || [];
  } catch (e) {
    $("#schedule").innerHTML = "";
    empty.hidden = false;
    empty.textContent = "Could not load schedule: " + e.message;
    $("#schedule-summary").textContent = "";
    $("#sched-range").textContent = "";
    return;
  }
  renderSchedule();
}

// ---- date helpers (local wall-clock; the grid is laid out in the operator's zone) ----

function startOfDay(d) {
  const x = new Date(d);
  x.setHours(0, 0, 0, 0);
  return x;
}

// startOfWeek returns local midnight on the Monday of d's week (Mon-first columns).
function startOfWeek(d) {
  const x = startOfDay(d);
  const dow = (x.getDay() + 6) % 7; // 0=Mon … 6=Sun
  x.setDate(x.getDate() - dow);
  return x;
}

function addDays(d, n) {
  const x = new Date(d);
  x.setDate(x.getDate() + n);
  return x;
}

// schedPeriodStart is the local midnight that begins the visible period — the
// Monday of the anchored week, or the anchored day. Defaults to the current week/day
// when no period has been navigated to yet.
function schedPeriodStart() {
  const base = state.schedAnchor || new Date();
  return state.schedView === "week" ? startOfWeek(base) : startOfDay(base);
}

function fmtHour(h) {
  if (h % 24 === 0) return "12 AM";
  if (h === 12) return "12 PM";
  return h < 12 ? `${h} AM` : `${h - 12} PM`;
}

function rangeLabel(periodStart, days) {
  if (days === 1) {
    return periodStart.toLocaleDateString(undefined, {
      weekday: "long", month: "long", day: "numeric", year: "numeric",
    });
  }
  const end = addDays(periodStart, days - 1);
  const sMonth = periodStart.toLocaleDateString(undefined, { month: "short" });
  const eMonth = end.toLocaleDateString(undefined, { month: "short" });
  const y = end.getFullYear();
  return periodStart.getMonth() === end.getMonth()
    ? `${sMonth} ${periodStart.getDate()} – ${end.getDate()}, ${y}`
    : `${sMonth} ${periodStart.getDate()} – ${eMonth} ${end.getDate()}, ${y}`;
}

function renderSchedule() {
  const cal = $("#schedule");
  const empty = $("#schedule-empty");
  cal.innerHTML = "";
  if (!$("#sched-provider").value) {
    empty.hidden = false;
    empty.textContent = "Choose a provider — or All providers — to see the schedule.";
    $("#schedule-summary").textContent = "";
    $("#sched-range").textContent = "";
    return;
  }

  const days = state.schedView === "week" ? 7 : 1;
  const periodStart = schedPeriodStart();
  const periodEnd = addDays(periodStart, days);
  const visible = state.schedule.filter((a) => {
    const s = new Date(a.startsAt);
    return !isNaN(s) && s >= periodStart && s < periodEnd;
  });

  $("#sched-range").textContent = rangeLabel(periodStart, days);
  const n = visible.length;
  const total = state.schedule.length;
  $("#schedule-summary").textContent =
    `${n} this ${state.schedView}` + (total > n ? ` · ${total} total` : "");

  // The hour window fits the visible appointments, never narrower than 8 AM–6 PM.
  let startH = 8;
  let endH = 18;
  for (const a of visible) {
    const s = new Date(a.startsAt);
    const e = new Date(a.endsAt);
    if (!isNaN(s)) startH = Math.min(startH, s.getHours());
    if (!isNaN(e)) endH = Math.max(endH, e.getMinutes() > 0 ? e.getHours() + 1 : e.getHours());
  }
  startH = Math.max(0, startH);
  endH = Math.min(24, Math.max(endH, startH + 1));

  // The (possibly empty) grid is the source of truth — an empty period still
  // renders the week/day structure, with the summary reading "0 this week". The
  // dashed empty placeholder is reserved for "no provider chosen".
  empty.hidden = true;
  cal.append(buildCalendar(periodStart, days, startH, endH, visible));
}

function buildCalendar(periodStart, days, startH, endH, appts) {
  const wrap = document.createElement("div");
  wrap.className = "cal-wrap";
  wrap.style.setProperty("--cal-days", String(days));
  const bodyH = (endH - startH) * PX_PER_HOUR;
  const todayMid = startOfDay(new Date()).getTime();

  const head = document.createElement("div");
  head.className = "cal-head";
  head.append(document.createElement("div")); // empty corner over the time gutter
  for (let i = 0; i < days; i++) {
    const day = addDays(periodStart, i);
    const h = document.createElement("div");
    h.className = "cal-day-head";
    if (startOfDay(day).getTime() === todayMid) h.classList.add("today");
    const wd = document.createElement("span");
    wd.className = "cal-wd";
    wd.textContent = day.toLocaleDateString(undefined, { weekday: "short" });
    const dn = document.createElement("span");
    dn.className = "cal-dn";
    dn.textContent = day.toLocaleDateString(undefined, { month: "short", day: "numeric" });
    h.append(wd, dn);
    head.append(h);
  }
  wrap.append(head);

  const body = document.createElement("div");
  body.className = "cal-body";
  body.style.height = bodyH + "px";

  const gutter = document.createElement("div");
  gutter.className = "cal-gutter";
  for (let h = startH; h <= endH; h++) {
    const lab = document.createElement("div");
    lab.className = "cal-hour";
    lab.style.top = (h - startH) * PX_PER_HOUR + "px";
    lab.textContent = fmtHour(h);
    gutter.append(lab);
  }
  body.append(gutter);

  for (let i = 0; i < days; i++) {
    const dayStart = startOfDay(addDays(periodStart, i));
    const col = document.createElement("div");
    col.className = "cal-col";
    col.style.backgroundImage =
      `repeating-linear-gradient(to bottom, transparent 0, transparent ${PX_PER_HOUR - 1}px, ` +
      `var(--border) ${PX_PER_HOUR - 1}px, var(--border) ${PX_PER_HOUR}px)`;
    const dayAppts = appts.filter((a) => {
      const s = new Date(a.startsAt);
      return !isNaN(s) && startOfDay(s).getTime() === dayStart.getTime();
    });
    for (const placed of layoutDay(dayAppts)) {
      col.append(apptBlock(placed.a, dayStart, startH, endH, placed.lane, placed.lanes));
    }
    body.append(col);
  }
  wrap.append(body);
  return wrap;
}

// layoutDay greedily assigns overlapping appointments to side-by-side lanes so
// double-booked slots (booking enforces no conflict yet) render next to each other
// instead of stacking. Returns each appointment with its lane index and the day's
// total lane count (used for block width).
function layoutDay(appts) {
  const items = appts
    .map((a) => ({ a, s: new Date(a.startsAt).getTime(), e: new Date(a.endsAt).getTime() }))
    .filter((x) => !isNaN(x.s))
    .sort((x, y) => x.s - y.s || x.e - y.e);
  const laneEnds = [];
  for (const it of items) {
    const end = isNaN(it.e) || it.e <= it.s ? it.s + 30 * 60000 : it.e;
    let lane = laneEnds.findIndex((le) => le <= it.s);
    if (lane === -1) {
      lane = laneEnds.length;
      laneEnds.push(end);
    } else {
      laneEnds[lane] = end;
    }
    it.lane = lane;
  }
  const lanes = Math.max(1, laneEnds.length);
  return items.map((it) => ({ a: it.a, lane: it.lane, lanes }));
}

function apptBlock(a, dayStart, startH, endH, lane, lanes) {
  const s = new Date(a.startsAt);
  let e = new Date(a.endsAt);
  if (isNaN(e) || e <= s) e = new Date(s.getTime() + 30 * 60000);
  const winTop = startH * 60;
  const winBot = endH * 60;
  const startMin = (s - dayStart) / 60000;
  const endMin = (e - dayStart) / 60000;
  const top = ((Math.max(startMin, winTop) - winTop) / 60) * PX_PER_HOUR;
  const bottom = ((Math.min(endMin, winBot) - winTop) / 60) * PX_PER_HOUR;
  const height = Math.max(bottom - top, 20);
  const widthPct = 100 / lanes;

  const block = document.createElement("button");
  block.type = "button";
  block.className = "cal-appt " + statusClass(a.status);
  if (state.schedSelected === a.appointmentKey) block.classList.add("sel");
  if (isPast(a.startsAt) && ACTIVE_STATUSES.includes(a.status)) block.classList.add("past");
  block.style.top = top + "px";
  block.style.height = height + "px";
  block.style.left = `calc(${lane * widthPct}% + 2px)`;
  block.style.width = `calc(${widthPct}% - 4px)`;

  const t = document.createElement("span");
  t.className = "cal-appt-t";
  t.textContent = s.toLocaleTimeString(undefined, { hour: "numeric", minute: "2-digit" });
  const who = document.createElement("span");
  who.className = "cal-appt-who";
  who.textContent = a.patientName || shortKey(a.patientKey);
  block.append(t, who);
  // Clinic-wide view: the block's title is the patient, so name the provider too
  // (which provider's slot this is) — the one thing the single-provider grid implies
  // but the all-providers grid can't.
  if (schedIsAll()) {
    const prov = document.createElement("span");
    prov.className = "cal-appt-prov";
    prov.textContent = a.providerName || shortKey(a.providerKey);
    block.append(prov);
  }
  block.title =
    `${a.patientName || shortKey(a.patientKey)} · ${fmtWhen(a.startsAt, a.endsAt)}` +
    (schedIsAll() ? " · " + (a.providerName || shortKey(a.providerKey)) : "") +
    (a.reason ? " · " + a.reason : "") + ` · ${a.status}`;
  block.addEventListener("click", () => selectSchedAppt(a));
  return block;
}

// selectSchedAppt opens the read-only detail panel for a clicked block and marks it
// selected (re-render reflects the .sel highlight; the detail panel is a separate
// node the calendar rebuild leaves intact).
function selectSchedAppt(a) {
  state.schedSelected = a.appointmentKey;
  const d = $("#sched-detail");
  d.innerHTML = "";

  const close = document.createElement("button");
  close.className = "sched-detail-x";
  close.setAttribute("aria-label", "Close details");
  close.textContent = "×";
  close.addEventListener("click", hideSchedDetail);

  const who = document.createElement("div");
  who.className = "sd-who";
  who.textContent = a.patientName || shortKey(a.patientKey);

  const when = document.createElement("div");
  when.className = "sd-when";
  when.textContent = fmtWhen(a.startsAt, a.endsAt);

  const badge = document.createElement("span");
  badge.className = "badge " + statusClass(a.status);
  badge.textContent = a.status || "—";

  d.append(close, who, when, badge);
  if (schedIsAll()) {
    const prov = document.createElement("div");
    prov.className = "sd-meta";
    prov.textContent = "with " + (a.providerName || shortKey(a.providerKey)) +
      (a.providerSpecialty ? " · " + a.providerSpecialty : "");
    d.append(prov);
  }
  if (a.reason) {
    const meta = document.createElement("div");
    meta.className = "sd-meta";
    meta.textContent = a.reason;
    d.append(meta);
  }
  if (a.statusNote) {
    const note = document.createElement("div");
    note.className = "sd-meta status-note";
    note.textContent = "📝 " + a.statusNote;
    d.append(note);
  }
  if (a.reminderSentAt) {
    const r = new Date(a.reminderSentAt);
    const rem = document.createElement("div");
    rem.className = "sd-meta reminder-sent";
    rem.textContent = "🔔 Reminder sent" + (isNaN(r) ? "" : " · " + r.toLocaleString());
    d.append(rem);
  }
  // Day-of-visit transitions on the desk view (the front-desk / provider operational
  // surface the PO flagged). loadSchedule re-fetches + closes this panel (it calls
  // hideSchedDetail first), and the block re-colours to the new status.
  if (ACTIVE_STATUSES.includes(a.status)) {
    const acts = document.createElement("div");
    acts.className = "sd-actions";
    acts.append(lifecycleButtons(a, loadSchedule));
    const cancel = document.createElement("button");
    cancel.className = "ghost danger";
    cancel.textContent = "Cancel";
    cancel.addEventListener("click", () => setStatus(a, "cancelled", loadSchedule));
    acts.append(cancel);
    d.append(acts);
  }
  d.hidden = false;
  renderSchedule();
}

function hideSchedDetail() {
  state.schedSelected = null;
  const d = $("#sched-detail");
  d.hidden = true;
  d.innerHTML = "";
  document.querySelectorAll("#schedule .cal-appt.sel").forEach((el) => el.classList.remove("sel"));
}

// ---- Schedule navigation (period + view) ----

function schedNav(direction) {
  const step = state.schedView === "week" ? 7 : 1;
  state.schedAnchor = addDays(schedPeriodStart(), direction * step);
  hideSchedDetail();
  renderSchedule();
}

function schedToday() {
  state.schedAnchor = new Date();
  hideSchedDetail();
  renderSchedule();
}

function setSchedView(view) {
  if (state.schedView === view) return;
  state.schedView = view;
  for (const v of ["week", "day"]) {
    const btn = $("#sched-" + v);
    btn.classList.toggle("active", v === view);
    btn.setAttribute("aria-pressed", String(v === view));
  }
  hideSchedDetail();
  renderSchedule();
}

// ---- Appointment card (shared by both lists) ----

function fmtWhen(startsAt, endsAt) {
  const s = new Date(startsAt);
  if (isNaN(s)) return startsAt || "";
  const day = s.toLocaleDateString(undefined, { weekday: "short", year: "numeric", month: "short", day: "numeric" });
  const t1 = s.toLocaleTimeString(undefined, { hour: "numeric", minute: "2-digit" });
  const e = new Date(endsAt);
  const t2 = isNaN(e) ? "" : e.toLocaleTimeString(undefined, { hour: "numeric", minute: "2-digit" });
  return t2 ? `${day} · ${t1}–${t2}` : `${day} · ${t1}`;
}

function isPast(startsAt) {
  const s = new Date(startsAt);
  return !isNaN(s) && s.getTime() < Date.now();
}

const ACTIVE_STATUSES = ["scheduled", "confirmed", "checkedIn"];

function renderApptCard(a, opts) {
  const card = document.createElement("div");
  card.className = "card";
  if (state.highlight && a.appointmentKey === state.highlight) card.classList.add("highlight");
  if (isPast(a.startsAt) && ACTIVE_STATUSES.includes(a.status)) card.classList.add("past");

  const title = document.createElement("div");
  title.className = "addr";
  title.textContent = opts.showProvider
    ? a.providerName || shortKey(a.providerKey)
    : a.patientName || shortKey(a.patientKey);

  const sub = document.createElement("div");
  sub.className = "addr-sub";
  // My Appointments shows the provider's specialty under their name; the provider
  // Schedule's title is already the patient name, so no sub-label is needed.
  sub.textContent = opts.showProvider ? a.providerSpecialty || "" : "";

  const when = document.createElement("div");
  when.className = "when";
  when.textContent = fmtWhen(a.startsAt, a.endsAt);

  const reason = document.createElement("div");
  reason.className = "meta";
  reason.textContent = a.reason || "";

  // An audit note recorded with a cancel / no-show transition (clinicAppointments
  // lens statusNote column). Absent unless a note was supplied.
  const statusNote = document.createElement("div");
  statusNote.className = "meta status-note";
  statusNote.textContent = a.statusNote ? "📝 " + a.statusNote : "";

  // The ~24h reminder, once the clinic-reminders orchestration has fired it
  // (surfaced via the clinicAppointments lens's reminderSentAt column). Absent
  // until sent.
  const reminder = document.createElement("div");
  reminder.className = "meta reminder-sent";
  if (a.reminderSentAt) {
    const r = new Date(a.reminderSentAt);
    reminder.textContent = "🔔 Reminder sent" + (isNaN(r) ? "" : " · " + r.toLocaleString());
  }

  const actions = document.createElement("div");
  actions.className = "card-actions";
  const badge = document.createElement("span");
  badge.className = "badge " + statusClass(a.status);
  badge.textContent = a.status || "—";
  actions.append(badge);

  if (opts.cancelable && ACTIVE_STATUSES.includes(a.status)) {
    const btns = document.createElement("span");
    btns.className = "card-btns";

    btns.append(lifecycleButtons(a, loadAppts));

    const reschedule = document.createElement("button");
    reschedule.className = "ghost";
    reschedule.textContent = "Reschedule";
    reschedule.addEventListener("click", () => openReschedule(a));
    btns.append(reschedule);

    const cancel = document.createElement("button");
    cancel.className = "ghost danger";
    cancel.textContent = "Cancel";
    cancel.addEventListener("click", () => setStatus(a, "cancelled", loadAppts));
    btns.append(cancel);

    actions.append(btns);
  }

  card.append(title);
  if (sub.textContent) card.append(sub);
  card.append(when);
  if (reason.textContent) card.append(reason);
  if (statusNote.textContent) card.append(statusNote);
  if (reminder.textContent) card.append(reminder);
  card.append(actions);
  return card;
}

function statusClass(status) {
  return (status || "").toLowerCase() === "noshow" ? "noshow" : (status || "").toLowerCase();
}

// ---- Appointment lifecycle (the day-of-visit transitions) ----
//
// SetAppointmentStatus is unconditioned (re-runnable), so the transitions below are
// UI affordances, not server gates: a scheduled appointment can be confirmed /
// completed / no-showed; a confirmed one completed / no-showed; completed · cancelled
// · noShow are terminal. Cancel and the no-show transition prompt for confirmation;
// forward progress (confirm / complete) proceeds directly.

const TERMINAL_STATUSES = ["completed", "cancelled", "noshow"];
const STATUS_LABEL = { confirmed: "Confirm", checkedIn: "Check in", completed: "Complete", noShow: "No-show", cancelled: "Cancel" };
const STATUS_PAST = { confirmed: "confirmed", checkedIn: "checked in", completed: "completed", noShow: "marked no-show", cancelled: "cancelled" };

// lifecycleTransitions returns the SetAppointmentStatus targets reachable from the
// current status (excluding Cancel, which renders as its own button alongside).
// The day-of-visit flow is scheduled → confirmed → checkedIn → completed; check-in
// and complete/no-show stay reachable from the earlier active states too.
function lifecycleTransitions(status) {
  const s = (status || "").toLowerCase();
  if (s === "scheduled") return ["confirmed", "checkedIn", "completed", "noShow"];
  if (s === "confirmed") return ["checkedIn", "completed", "noShow"];
  if (s === "checkedin") return ["completed", "noShow"];
  return []; // completed / cancelled / noShow are terminal
}

// setStatus drives SetAppointmentStatus to the given status and reloads via onDone.
// noShow / cancelled prompt for an optional audit note (a reason recorded on the
// .status aspect for records / billing); cancelling the prompt aborts.
async function setStatus(a, status, onDone) {
  const payload = { appointmentKey: a.appointmentKey, status };
  if (status === "noShow" || status === "cancelled") {
    const verb = status === "noShow" ? "Mark as no-show" : "Cancel this appointment";
    const note = prompt(verb + ". Optional note (reason):", "");
    if (note === null) return; // prompt dismissed → abort
    const trimmed = note.trim();
    if (trimmed) payload.note = trimmed;
  }
  try {
    const reply = await submitOp(
      "SetAppointmentStatus",
      "appointment",
      payload,
      [a.appointmentKey],
    );
    const msg = rejectionMessage(reply);
    if (msg) {
      toast("Could not update status — " + msg, "err");
      return;
    }
    toast("Appointment " + (STATUS_PAST[status] || status) + ".", "ok");
    if (onDone) onDone();
  } catch (e) {
    toast("Could not update status: " + e.message, "err");
  }
}

// lifecycleButtons builds the status-transition buttons for an appointment, wired to
// reload via onDone. Returns a fragment (empty for a terminal appointment).
function lifecycleButtons(a, onDone) {
  const frag = document.createDocumentFragment();
  for (const st of lifecycleTransitions(a.status)) {
    const b = document.createElement("button");
    b.className = st === "noShow" ? "ghost danger" : "ghost";
    b.textContent = STATUS_LABEL[st];
    b.addEventListener("click", () => setStatus(a, st, onDone));
    frag.append(b);
  }
  return frag;
}

// ---- Reschedule (move an appointment to a new time) ----
//
// RescheduleAppointment rewrites the .schedule aspect with new times; the op
// re-derives remindAt = startsAt − 24h so the ~24h reminder re-arms for a
// not-yet-sent reminder, and rejects a move into a slot already booked for the
// provider (SlotConflict — the provider .bookings index is a declared read). The
// existing reason is round-tripped (the op clears it if omitted), and the
// provider / patient links + status are untouched server-side.

function openReschedule(a) {
  state.rescheduling = a;
  const who = a.providerName || shortKey(a.providerKey);
  $("#reschedule-context").textContent = `${who} · currently ${fmtWhen(a.startsAt, a.endsAt)}`;
  $("#rs-startsAt").value = toLocalInputValue(a.startsAt);
  $("#rs-startsAt").min = nowLocalInputValue();
  const dur = durationMinutes(a.startsAt, a.endsAt);
  const sel = $("#rs-duration");
  sel.value = String(dur);
  if (!sel.value) sel.value = "30"; // a non-standard length falls back to 30 min
  $("#reschedule-overlay").hidden = false;
  $("#rs-startsAt").focus();
}

function closeReschedule() {
  $("#reschedule-overlay").hidden = true;
  state.rescheduling = null;
}

async function submitReschedule(ev) {
  ev.preventDefault();
  const a = state.rescheduling;
  if (!a) {
    closeReschedule();
    return;
  }
  const when = $("#rs-startsAt").value;
  if (!when) {
    toast("Pick a new date and time.", "err");
    return;
  }
  const startsAt = toRFC3339(when);
  const endsAt = addMinutesRFC3339(when, Number($("#rs-duration").value || 30));
  if (!startsAt || !endsAt) {
    toast("That date/time is not valid.", "err");
    return;
  }

  const payload = { appointmentKey: a.appointmentKey, provider: a.providerKey, patient: a.patientKey, startsAt, endsAt };
  if (a.reason) payload.reason = a.reason; // round-trip the existing reason (omitted → cleared)

  const submit = $("#reschedule-submit");
  submit.disabled = true;
  try {
    // The provider's AND the patient's .bookings indexes are declared reads so the
    // op detects a provider double-book (SlotConflict) and a patient double-book
    // across providers (PatientDoubleBook) against the new time (RescheduleAppointment
    // skips this appointment on both sides).
    const reply = await submitOp("RescheduleAppointment", "appointment", payload,
      [a.appointmentKey, a.providerKey + ".bookings", a.patientKey + ".bookings"]);
    const msg = rejectionMessage(reply);
    if (msg) {
      toast("Could not reschedule — " + friendlyBookingRejection(msg), "err");
      return;
    }
    state.highlight = a.appointmentKey;
    // The moved appointment invalidates both providers' and the patient's cached
    // slot sets so the picker reflects the new time.
    delete state.slotApptCache[a.providerKey];
    delete state.slotPatientApptCache[a.patientKey];
    closeReschedule();
    toast("Appointment rescheduled.", "ok");
    loadAppts();
  } catch (e) {
    toast("Could not reschedule: " + e.message, "err");
  } finally {
    submit.disabled = false;
  }
}

// ---- Tabs ----

const VIEWS = ["book", "appts", "schedule", "availability"];

function showView(view) {
  state.view = view;
  for (const v of VIEWS) {
    const isV = v === view;
    $("#view-" + v).hidden = !isV;
    const tab = $("#tab-" + v);
    tab.classList.toggle("active", isV);
    tab.setAttribute("aria-selected", String(isV));
  }
  if (view === "appts") loadAppts();
  if (view === "schedule") loadSchedule();
  if (view === "availability") renderAvailEditors();
}

// ---- wire up ----

function init() {
  restorePatient();
  loadPatients();
  loadProviders();

  // Discourage a past booking from the picker itself; refresh on focus so a
  // long-open session never carries a stale floor. The op stays the authority.
  $("#startsAt").min = nowLocalInputValue();
  $("#startsAt").addEventListener("focus", () => {
    $("#startsAt").min = nowLocalInputValue();
  });
  $("#startsAt").addEventListener("change", () => {
    refreshTimeOffWarning();
    refreshSlots(); // keep the slot highlight in sync with a typed time
  });
  // #slot-date is a hidden value driven by the custom booking calendar
  // (renderSlotCalendar), which greys out unbookable days; the per-slot past check
  // remains the floor and the op stays the authority.

  $("#patient").addEventListener("change", (e) => setPatient(e.target.value));
  $("#new-patient").addEventListener("click", openNewPatient);
  $("#patient-cancel").addEventListener("click", closeNewPatient);
  $("#patient-overlay").addEventListener("click", (e) => {
    if (e.target === $("#patient-overlay")) closeNewPatient();
  });
  $("#patient-form").addEventListener("submit", submitNewPatient);

  $("#reschedule-cancel").addEventListener("click", closeReschedule);
  $("#reschedule-overlay").addEventListener("click", (e) => {
    if (e.target === $("#reschedule-overlay")) closeReschedule();
  });
  $("#reschedule-form").addEventListener("submit", submitReschedule);

  $("#provider").addEventListener("change", () => {
    refreshBookEnabled();
    refreshTimeOffWarning();
    // A new provider has different availability — clear the chosen date and rebuild
    // the calendar so the user re-picks against the new provider's open days.
    $("#slot-date").value = "";
    renderSlotCalendar();
    refreshSlots();
  });
  $("#duration").addEventListener("change", refreshSlots);
  $("#add-provider-submit").addEventListener("click", submitAddProvider);
  // Availability tab — its own provider picker drives both editors; a change
  // re-seeds each draft from the newly-selected provider's projected values.
  $("#avail-provider").addEventListener("change", renderAvailEditors);
  $("#hours-add").addEventListener("click", addHoursWindow);
  $("#hours-save").addEventListener("click", saveProviderHours);
  $("#timeoff-add").addEventListener("click", addTimeOffRange);
  $("#timeoff-save").addEventListener("click", saveProviderTimeOff);
  $("#book-form").addEventListener("submit", submitBook);

  $("#tab-book").addEventListener("click", () => showView("book"));
  $("#tab-appts").addEventListener("click", () => showView("appts"));
  $("#tab-schedule").addEventListener("click", () => showView("schedule"));
  $("#tab-availability").addEventListener("click", () => showView("availability"));
  // The Book form's pointer link jumps to the Availability tab, carrying the
  // provider the user was about to book so the editor opens on that provider.
  $("#go-availability").addEventListener("click", (e) => {
    e.preventDefault();
    const prov = $("#provider").value;
    if (prov) $("#avail-provider").value = prov;
    showView("availability");
  });
  $("#reload-appts").addEventListener("click", loadAppts);
  $("#appts-filter").addEventListener("change", renderAppts);
  $("#reload-schedule").addEventListener("click", loadSchedule);
  $("#sched-provider").addEventListener("change", loadSchedule);
  $("#sched-week").addEventListener("click", () => setSchedView("week"));
  $("#sched-day").addEventListener("click", () => setSchedView("day"));
  $("#sched-prev").addEventListener("click", () => schedNav(-1));
  $("#sched-next").addEventListener("click", () => schedNav(1));
  $("#sched-today").addEventListener("click", schedToday);
}

document.addEventListener("DOMContentLoaded", init);
