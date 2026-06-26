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
    const reply = await submitOp("CreateAppointment", "appointment", payload, [state.patient, provider]);
    const msg = rejectionMessage(reply);
    if (msg) {
      toast("Booking rejected — " + msg, "err");
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

// ---- Provider Schedule (read-only desk view) ----

async function loadSchedule() {
  const provider = $("#sched-provider").value;
  const grid = $("#schedule");
  const empty = $("#schedule-empty");
  if (!provider) {
    grid.innerHTML = "";
    state.schedule = [];
    empty.hidden = false;
    empty.textContent = "Choose a provider to see their schedule.";
    $("#schedule-summary").textContent = "";
    return;
  }
  $("#schedule-summary").textContent = "loading…";
  try {
    const data = await api("/api/appointments?provider=" + encodeURIComponent(provider));
    state.schedule = data.appointments || [];
  } catch (e) {
    grid.innerHTML = "";
    empty.hidden = false;
    empty.textContent = "Could not load schedule: " + e.message;
    $("#schedule-summary").textContent = "";
    return;
  }
  renderSchedule();
}

function renderSchedule() {
  const grid = $("#schedule");
  const empty = $("#schedule-empty");
  grid.innerHTML = "";
  if (state.schedule.length === 0) {
    empty.hidden = false;
    empty.textContent = "No appointments on this provider's schedule.";
    $("#schedule-summary").textContent = "";
    return;
  }
  empty.hidden = true;
  for (const a of state.schedule) grid.append(renderApptCard(a, { showProvider: false, cancelable: false }));
  const n = state.schedule.length;
  $("#schedule-summary").textContent = `${n} appointment${n === 1 ? "" : "s"}`;
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
// not-yet-sent reminder. The existing reason is round-tripped (the op clears it if
// omitted), and the provider / patient links + status are untouched server-side.

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

  const payload = { appointmentKey: a.appointmentKey, startsAt, endsAt };
  if (a.reason) payload.reason = a.reason; // round-trip the existing reason (omitted → cleared)

  const submit = $("#reschedule-submit");
  submit.disabled = true;
  try {
    const reply = await submitOp("RescheduleAppointment", "appointment", payload, [a.appointmentKey]);
    const msg = rejectionMessage(reply);
    if (msg) {
      toast("Could not reschedule — " + msg, "err");
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

  $("#provider").addEventListener("change", refreshBookEnabled);
  $("#add-provider-submit").addEventListener("click", submitAddProvider);
  $("#book-form").addEventListener("submit", submitBook);

  $("#tab-book").addEventListener("click", () => showView("book"));
  $("#tab-appts").addEventListener("click", () => showView("appts"));
  $("#tab-schedule").addEventListener("click", () => showView("schedule"));
  $("#reload-appts").addEventListener("click", loadAppts);
  $("#reload-schedule").addEventListener("click", loadSchedule);
  $("#sched-provider").addEventListener("change", loadSchedule);
}

document.addEventListener("DOMContentLoaded", init);
