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
  if (!res.ok || (body && body.error)) {
    throw new Error((body && body.error) || `HTTP ${res.status}`);
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
// for the booking / reschedule paths: a double-book (SlotConflict) and an
// out-of-availability-window booking (OutsideHours) are the two domain rejections
// CreateAppointment / RescheduleAppointment raise. Anything else passes through.
function friendlyBookingRejection(msg) {
  if (msg.indexOf("SlotConflict") !== -1) {
    return "That time overlaps another appointment for this provider. Pick another slot.";
  }
  if (msg.indexOf("OutsideHours") !== -1) {
    return "That time is outside the provider's availability (UTC). Set hours under “Manage availability” or pick a time inside them.";
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
  populateProviderSelect("#sched-provider");
  refreshBookEnabled();
}

function providerLabel(p) {
  return p.specialty ? `${p.name} · ${p.specialty}` : p.name;
}

function populateProviderSelect(sel) {
  const el = $(sel);
  if (!el) return;
  const prev = el.value;
  el.innerHTML = "";
  const placeholder = document.createElement("option");
  placeholder.value = "";
  placeholder.textContent = state.providers.length ? "Select provider…" : "No providers — add one below";
  el.append(placeholder);
  for (const p of state.providers) {
    const o = document.createElement("option");
    o.value = p.providerKey;
    o.textContent = providerLabel(p);
    el.append(o);
  }
  const values = state.providers.map((p) => p.providerKey);
  el.value = values.includes(prev) ? prev : "";
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
// provider's .hours aspect). Write-mostly: the currently-persisted windows are not
// shown here (that needs a clinicProviders lens projection of .hours — a follow-up);
// the draft list shows exactly what "Save availability" will set. Times are UTC to
// match the op's UTC weekday / seconds-of-day enforcement.

const DAY_NAMES = ["Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"];

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

// hoursDraftForSelectedProvider returns the Book form's selected provider key,
// resetting the draft when the selection changed (so one provider's draft can't
// be saved onto another).
function hoursDraftForSelectedProvider() {
  const prov = $("#provider").value;
  if (prov !== state.hoursProvider) {
    state.hoursProvider = prov;
    state.hoursDraft = [];
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
    $("#manage-hours").open = false;
  } catch (e) {
    toast("Could not set hours: " + e.message, "err");
  } finally {
    btn.disabled = false;
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
    // The provider's .bookings index is a declared read so the op can detect a
    // double-book (and so its OCC check serializes concurrent bookings).
    const reply = await submitOp("CreateAppointment", "appointment", payload,
      [state.patient, provider, provider + ".bookings"]);
    const msg = rejectionMessage(reply);
    if (msg) {
      toast("Booking rejected — " + friendlyBookingRejection(msg), "err");
      return;
    }
    const key = reply && reply.primaryKey ? reply.primaryKey : "";
    $("#book-form").reset();
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
  empty.hidden = true;
  for (const a of state.appts) grid.append(renderApptCard(a, { showProvider: true, cancelable: true }));
  const n = state.appts.length;
  $("#appts-summary").textContent = `${n} appointment${n === 1 ? "" : "s"}`;
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

async function loadSchedule() {
  const provider = $("#sched-provider").value;
  const empty = $("#schedule-empty");
  hideSchedDetail();
  if (!provider) {
    $("#schedule").innerHTML = "";
    state.schedule = [];
    empty.hidden = false;
    empty.textContent = "Choose a provider to see their schedule.";
    $("#schedule-summary").textContent = "";
    $("#sched-range").textContent = "";
    return;
  }
  $("#schedule-summary").textContent = "loading…";
  try {
    const data = await api("/api/appointments?provider=" + encodeURIComponent(provider));
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
    empty.textContent = "Choose a provider to see their schedule.";
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
  block.title =
    `${a.patientName || shortKey(a.patientKey)} · ${fmtWhen(a.startsAt, a.endsAt)}` +
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
  if (a.reason) {
    const meta = document.createElement("div");
    meta.className = "sd-meta";
    meta.textContent = a.reason;
    d.append(meta);
  }
  if (a.reminderSentAt) {
    const r = new Date(a.reminderSentAt);
    const rem = document.createElement("div");
    rem.className = "sd-meta reminder-sent";
    rem.textContent = "🔔 Reminder sent" + (isNaN(r) ? "" : " · " + r.toLocaleString());
    d.append(rem);
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

const ACTIVE_STATUSES = ["scheduled", "confirmed"];

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

    const reschedule = document.createElement("button");
    reschedule.className = "ghost";
    reschedule.textContent = "Reschedule";
    reschedule.addEventListener("click", () => openReschedule(a));
    btns.append(reschedule);

    const cancel = document.createElement("button");
    cancel.className = "ghost danger";
    cancel.textContent = "Cancel";
    cancel.addEventListener("click", () => cancelAppt(a));
    btns.append(cancel);

    actions.append(btns);
  }

  card.append(title);
  if (sub.textContent) card.append(sub);
  card.append(when);
  if (reason.textContent) card.append(reason);
  if (reminder.textContent) card.append(reminder);
  card.append(actions);
  return card;
}

function statusClass(status) {
  return (status || "").toLowerCase() === "noshow" ? "noshow" : (status || "").toLowerCase();
}

async function cancelAppt(a) {
  if (!confirm("Cancel this appointment?")) return;
  try {
    const reply = await submitOp(
      "SetAppointmentStatus",
      "appointment",
      { appointmentKey: a.appointmentKey, status: "cancelled" },
      [a.appointmentKey],
    );
    const msg = rejectionMessage(reply);
    if (msg) {
      toast("Could not cancel — " + msg, "err");
      return;
    }
    toast("Appointment cancelled.", "ok");
    loadAppts();
  } catch (e) {
    toast("Could not cancel: " + e.message, "err");
  }
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

  const payload = { appointmentKey: a.appointmentKey, provider: a.providerKey, startsAt, endsAt };
  if (a.reason) payload.reason = a.reason; // round-trip the existing reason (omitted → cleared)

  const submit = $("#reschedule-submit");
  submit.disabled = true;
  try {
    // The provider's .bookings index is a declared read so the op detects a
    // double-book against the new time (RescheduleAppointment skips this appointment).
    const reply = await submitOp("RescheduleAppointment", "appointment", payload,
      [a.appointmentKey, a.providerKey + ".bookings"]);
    const msg = rejectionMessage(reply);
    if (msg) {
      toast("Could not reschedule — " + friendlyBookingRejection(msg), "err");
      return;
    }
    state.highlight = a.appointmentKey;
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

const VIEWS = ["book", "appts", "schedule"];

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
}

// ---- wire up ----

function init() {
  restorePatient();
  loadPatients();
  loadProviders();

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
    // A provider change invalidates the availability draft (it is scoped per
    // provider); re-render so an open editor reflects the new provider's empty draft.
    if ($("#manage-hours").open) {
      hoursDraftForSelectedProvider();
      renderHoursDraft();
    }
  });
  $("#add-provider-submit").addEventListener("click", submitAddProvider);
  $("#manage-hours").addEventListener("toggle", () => {
    if ($("#manage-hours").open) {
      hoursDraftForSelectedProvider();
      renderHoursDraft();
    }
  });
  $("#hours-add").addEventListener("click", addHoursWindow);
  $("#hours-save").addEventListener("click", saveProviderHours);
  $("#book-form").addEventListener("submit", submitBook);

  $("#tab-book").addEventListener("click", () => showView("book"));
  $("#tab-appts").addEventListener("click", () => showView("appts"));
  $("#tab-schedule").addEventListener("click", () => showView("schedule"));
  $("#reload-appts").addEventListener("click", loadAppts);
  $("#reload-schedule").addEventListener("click", loadSchedule);
  $("#sched-provider").addEventListener("change", loadSchedule);
  $("#sched-week").addEventListener("click", () => setSchedView("week"));
  $("#sched-day").addEventListener("click", () => setSchedView("day"));
  $("#sched-prev").addEventListener("click", () => schedNav(-1));
  $("#sched-next").addEventListener("click", () => schedNav(1));
  $("#sched-today").addEventListener("click", schedToday);
}

document.addEventListener("DOMContentLoaded", init);
