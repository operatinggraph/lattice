"use strict";

// LoftSpace applicant app — Browse & Apply (Increment A). Vanilla JS, no build
// step. The Go server does all NATS I/O; this view reads /api/listings +
// /api/identities and submits CreateLeaseApplication via /api/op.

const APPLICANT_KEY = "loftspace.applicant";
const state = { listings: [], applications: [], applicant: null, current: null, view: "browse", highlight: null };

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

// ---- Applicant identity (the trusted-tool switcher) ----
//
// No per-user auth yet (P5/trust model): the applicant names who they are. The
// key is persisted in localStorage so a refresh keeps context. The proper
// roster lens read model is a tracked follow-up.

function restoreApplicant() {
  const saved = (localStorage.getItem(APPLICANT_KEY) || "").trim();
  state.applicant = saved || null;
  $("#applicant").value = saved;
}

function setApplicant(value) {
  const v = (value || "").trim();
  state.applicant = v || null;
  state.highlight = null; // a highlight belongs to the applicant who just applied
  if (v) localStorage.setItem(APPLICANT_KEY, v);
  else localStorage.removeItem(APPLICANT_KEY);
  renderListings(); // re-enable/disable Apply for the new applicant
  if (state.view === "apps") loadApplications(); // re-scope the tracker to the new applicant
}

// ---- Tabs (Browse & Apply / My Applications) ----

function showView(view) {
  state.view = view;
  const isBrowse = view === "browse";
  $("#view-browse").hidden = !isBrowse;
  $("#view-apps").hidden = isBrowse;
  $("#tab-browse").classList.toggle("active", isBrowse);
  $("#tab-apps").classList.toggle("active", !isBrowse);
  $("#tab-browse").setAttribute("aria-selected", String(isBrowse));
  $("#tab-apps").setAttribute("aria-selected", String(!isBrowse));
  if (!isBrowse) loadApplications();
}

// ---- Listings (Browse & Apply) ----

async function loadListings() {
  const status = $("#status").value;
  const grid = $("#listings");
  const empty = $("#empty");
  $("#summary").textContent = "loading…";
  try {
    const data = await api("/api/listings?status=" + encodeURIComponent(status));
    state.listings = data.listings || [];
  } catch (e) {
    grid.innerHTML = "";
    empty.hidden = false;
    empty.textContent = "Could not load listings: " + e.message;
    $("#summary").textContent = "";
    return;
  }
  renderListings();
}

function renderListings() {
  const grid = $("#listings");
  const empty = $("#empty");
  grid.innerHTML = "";
  if (state.listings.length === 0) {
    empty.hidden = false;
    empty.textContent = "No units are listed for lease right now.";
    $("#summary").textContent = "";
    return;
  }
  empty.hidden = true;
  for (const row of state.listings) grid.append(renderCard(row));
  $("#summary").textContent = `${state.listings.length} listing${state.listings.length === 1 ? "" : "s"}`;
}

function money(listing) {
  const amt = listing && typeof listing.rentAmount === "number" ? listing.rentAmount : null;
  if (amt === null) return "—";
  const cur = listing.rentCurrency || "";
  const n = amt.toLocaleString();
  return cur === "USD" ? `$${n}` : `${n} ${cur}`.trim();
}

function fmtDate(s) {
  if (!s) return "";
  const d = new Date(s);
  return isNaN(d) ? s : d.toLocaleDateString(undefined, { year: "numeric", month: "short", day: "numeric" });
}

function renderCard(row) {
  const L = row.listing || {};
  const A = row.address || {};
  const card = document.createElement("div");
  card.className = "card";

  const addr = document.createElement("div");
  addr.className = "addr";
  addr.textContent = A.line1 || row.unitKey;
  const addrSub = document.createElement("div");
  addrSub.className = "addr-sub";
  addrSub.textContent = [A.line2, [A.city, A.region].filter(Boolean).join(", "), A.postal].filter(Boolean).join(" · ");

  const rent = document.createElement("div");
  rent.className = "rent";
  rent.innerHTML = `${money(L)} <span>/ month</span>`;

  const facts = document.createElement("div");
  facts.className = "facts";
  const f = [];
  if (typeof L.bedrooms === "number") f.push(`${L.bedrooms} bd`);
  if (typeof L.bathrooms === "number") f.push(`${L.bathrooms} ba`);
  if (typeof L.sqft === "number") f.push(`${L.sqft.toLocaleString()} sqft`);
  facts.textContent = f.join("  ·  ");

  const meta = document.createElement("div");
  meta.className = "meta";
  const m = [];
  if (L.availableFrom) m.push("available " + fmtDate(L.availableFrom));
  if (typeof L.leaseTermMonths === "number") m.push(`${L.leaseTermMonths}-mo term`);
  meta.textContent = m.join("  ·  ");

  const actions = document.createElement("div");
  actions.className = "card-actions";
  const badge = document.createElement("span");
  badge.className = "badge " + (row.status || "");
  badge.textContent = row.status || "—";
  const apply = document.createElement("button");
  apply.textContent = "Apply";
  const leasable = row.status === "available";
  apply.disabled = !leasable || !state.applicant;
  apply.title = !state.applicant ? "Select an applicant first" : !leasable ? "Not available" : "";
  apply.addEventListener("click", () => openApply(row));
  actions.append(badge, apply);

  card.append(addr, addrSub, rent);
  if (facts.textContent) card.append(facts);
  if (meta.textContent) card.append(meta);
  card.append(actions);
  return card;
}

// ---- Apply modal ----

function openApply(row) {
  if (!state.applicant) {
    toast("Select an applicant first.", "err");
    return;
  }
  state.current = row;
  const A = row.address || {};
  $("#apply-unit").textContent = (A.line1 ? A.line1 + " · " : "") + row.unitKey;
  $("#apply-applicant").textContent = state.applicant;
  $("#apply-form").reset();
  syncTermRequirement();
  $("#apply-overlay").hidden = false;
  $("#moveInDate").focus();
}

function closeApply() {
  $("#apply-overlay").hidden = true;
  state.current = null;
}

// A move-in date makes the lease term required (the DDL rejects a half-specified
// terms block).
function syncTermRequirement() {
  const hasDate = !!$("#moveInDate").value;
  $("#term-hint").hidden = !hasDate;
  $("#term-opt").textContent = hasDate ? "(required)" : "(optional)";
}

async function submitApply(ev) {
  ev.preventDefault();
  const row = state.current;
  if (!row || !state.applicant) return;

  const moveIn = $("#moveInDate").value;
  const term = $("#leaseTermMonths").value;
  const rent = $("#requestedRent").value;

  if (moveIn && !term) {
    toast("Enter a lease term to go with the move-in date.", "err");
    return;
  }

  const payload = { applicant: state.applicant, unit: row.unitKey };
  if (moveIn) {
    // The .terms aspect stores moveInDate verbatim; normalize the date input to
    // an RFC3339 instant.
    payload.moveInDate = moveIn.length === 10 ? moveIn + "T00:00:00Z" : moveIn;
    payload.leaseTermMonths = Number(term);
  }
  if (rent) payload.requestedRent = Number(rent);

  const submit = $("#apply-submit");
  submit.disabled = true;
  try {
    const reply = await api("/api/op", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        operationType: "CreateLeaseApplication",
        class: "leaseapp",
        reads: [state.applicant, row.unitKey],
        payload,
      }),
    });
    if (reply && reply.status === "rejected") {
      const msg = reply.error ? `${reply.error.code}: ${reply.error.message}` : "rejected";
      toast("Application rejected — " + msg, "err");
      return;
    }
    const key = reply && reply.primaryKey ? reply.primaryKey : "";
    closeApply();
    toast("Application submitted.", "ok", key);
    loadListings();
    // Route to My Applications with the new application highlighted (the lens
    // may take a moment to project, so an empty/late row is normal on first load;
    // a Refresh shows it once projected). showView triggers the scoped load.
    state.highlight = key || null;
    showView("apps");
  } catch (e) {
    toast("Could not submit: " + e.message, "err");
  } finally {
    submit.disabled = false;
  }
}

// ---- My Applications (status tracker) ----

async function loadApplications() {
  const grid = $("#apps");
  const empty = $("#apps-empty");
  if (!state.applicant) {
    grid.innerHTML = "";
    state.applications = [];
    empty.hidden = false;
    empty.textContent = "Select an applicant identity above to see their applications.";
    $("#apps-summary").textContent = "";
    return;
  }
  $("#apps-summary").textContent = "loading…";
  try {
    const data = await api("/api/applications?applicant=" + encodeURIComponent(state.applicant));
    state.applications = data.applications || [];
  } catch (e) {
    grid.innerHTML = "";
    empty.hidden = false;
    empty.textContent = "Could not load applications: " + e.message;
    $("#apps-summary").textContent = "";
    return;
  }
  renderApplications();
}

function renderApplications() {
  const highlight = state.highlight;
  const grid = $("#apps");
  const empty = $("#apps-empty");
  grid.innerHTML = "";
  if (state.applications.length === 0) {
    empty.hidden = false;
    empty.textContent = "No applications yet. Browse a listing and apply to get started.";
    $("#apps-summary").textContent = "";
    return;
  }
  empty.hidden = true;
  for (const row of state.applications) grid.append(renderApplicationCard(row, highlight));
  const n = state.applications.length;
  $("#apps-summary").textContent = `${n} application${n === 1 ? "" : "s"}`;
}

// Each step is one gap dimension, derived from the lens columns the row actually
// carries: a closed gap is "done"; an open gap with a call in flight is "active"
// (In progress); an open gap with nothing in flight is "todo". The lens does not
// project a per-row "retries exhausted" signal (maxretries_<g> is a constant cap),
// so there is no "action needed" state to derive here — a stalled automated step
// reads as "todo".
function stepState(done, inflight) {
  if (done) return "done";
  if (inflight) return "active";
  return "todo";
}

const STEP_LABEL = { done: "Done", active: "In progress", todo: "To do" };

function renderStep(num, title, st, note) {
  const step = document.createElement("li");
  step.className = "step " + st;
  const dot = document.createElement("span");
  dot.className = "step-dot";
  dot.textContent = st === "done" ? "✓" : String(num);
  const body = document.createElement("div");
  body.className = "step-body";
  const t = document.createElement("div");
  t.className = "step-title";
  t.textContent = title;
  const s = document.createElement("div");
  s.className = "step-status";
  s.textContent = note ? `${STEP_LABEL[st]} · ${note}` : STEP_LABEL[st];
  body.append(t, s);
  step.append(dot, body);
  return step;
}

function shortKey(key) {
  const i = (key || "").lastIndexOf(".");
  return i >= 0 ? key.slice(i + 1) : key || "—";
}

function renderApplicationCard(row, highlight) {
  const card = document.createElement("div");
  card.className = "card app-card";
  if (highlight && row.entityKey === highlight) card.classList.add("highlight");

  // Header: what am I leasing
  const head = document.createElement("div");
  head.className = "app-head";
  const addr = document.createElement("div");
  addr.className = "addr";
  addr.textContent = row.unitAddress || (row.unitKey ? shortKey(row.unitKey) : "Application");
  head.append(addr);
  if (typeof row.unitRent === "number") {
    const rent = document.createElement("div");
    rent.className = "rent";
    rent.innerHTML = `$${row.unitRent.toLocaleString()} <span>/ month</span>`;
    head.append(rent);
  }
  const ref = document.createElement("div");
  ref.className = "addr-sub mono";
  ref.textContent = shortKey(row.entityKey);
  head.append(ref);

  // Decision banner
  const banner = document.createElement("div");
  if (!row.violating) {
    banner.className = "decision ok";
    banner.textContent = "Application complete — all steps done.";
  } else {
    banner.className = "decision pending";
    banner.textContent = "In review — complete the open steps below.";
  }

  // Stepper (journey order)
  const steps = document.createElement("ol");
  steps.className = "stepper";
  steps.append(
    renderStep(1, "Onboarding (identity details)", stepState(!row.missing_onboarding, false)),
    renderStep(2, "Background check", stepState(!row.missing_bgcheck, row.inflight_bgcheck)),
    renderStep(3, "Payment", stepState(!row.missing_payment, row.inflight_payment)),
    renderStep(4, "Sign lease", stepState(!row.missing_signature, false)),
  );

  card.append(head, banner, steps);
  return card;
}

// ---- wire up ----

function init() {
  restoreApplicant();
  $("#applicant").addEventListener("input", (e) => setApplicant(e.target.value));
  $("#status").addEventListener("change", loadListings);
  $("#reload-listings").addEventListener("click", loadListings);
  $("#apply-cancel").addEventListener("click", closeApply);
  $("#apply-overlay").addEventListener("click", (e) => {
    if (e.target === $("#apply-overlay")) closeApply();
  });
  $("#moveInDate").addEventListener("input", syncTermRequirement);
  $("#apply-form").addEventListener("submit", submitApply);
  $("#tab-browse").addEventListener("click", () => showView("browse"));
  $("#tab-apps").addEventListener("click", () => showView("apps"));
  $("#reload-apps").addEventListener("click", loadApplications);

  loadListings();
}

document.addEventListener("DOMContentLoaded", init);
