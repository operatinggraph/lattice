"use strict";

// LoftSpace applicant app — Browse & Apply (Increment A). Vanilla JS, no build
// step. The Go server does all NATS I/O; this view reads /api/listings +
// /api/identities and submits CreateLeaseApplication via /api/op.

const APPLICANT_KEY = "loftspace.applicant";
const MODE_KEY = "loftspace.mode";
const state = {
  listings: [], applications: [], tasks: [], docs: [], identities: [], units: [],
  applicant: null, current: null, currentTask: null, view: "browse", highlight: null,
  mode: "applicant",
  docScope: null,
  // sessionUploads maps an oid uploaded THIS session to the link it was created
  // with, so the doc can be detached. A listed doc from a prior session has no
  // linkName in the read model (the lens cannot project type(r)), so detach of
  // those is a documented follow-up.
  sessionUploads: {},
};

// DOC_SLOTS labels the upload "slot" (the link name) for display.
const DOC_SLOTS = {
  idDocument: "ID document",
  proofOfIncome: "Proof of income",
  signedLeasePdf: "Signed lease (PDF)",
};

// COMPLETIONS maps a userTask op to how the applicant completes it in-app. target
// is the op's primary key field, filled from the task's scopedTo — for a userTask
// the §10.5 invariant holds (assignee == scopedTo == the subject), so scopedTo is
// the entity the op acts on. class is the op's DDL-inference class; reads carry the
// scopedTo key. An op not listed here can't be completed in-app yet (the generic
// DDL-self-describing form needs an op-catalog read model — a Core-KV op-meta scan
// would violate P5 in a vertical app); its card links to Loupe instead.
const COMPLETIONS = {
  SignLease: {
    title: "Sign your lease",
    klass: "leaseapp",
    targetField: "leaseAppKey",
    fields: [],
    submitLabel: "Sign lease",
  },
  RecordIdentityPII: {
    title: "Provide your identity details",
    klass: "identity",
    targetField: "identityKey",
    sensitive: true,
    fields: [
      { name: "ssn", label: "Social Security Number", placeholder: "123-45-6789", required: true },
      { name: "dob", label: "Date of birth", type: "date", required: true },
    ],
    submitLabel: "Submit details",
  },
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

// ---- Applicant identity (the trusted-tool switcher) ----
//
// No per-user auth yet (P5/trust model): the applicant picks who they are from a
// human-readable roster (the applicantRoster lens read model, never Core KV). The
// selected key is persisted in localStorage so a refresh keeps context.

// nameFor resolves an identity key to its human name from the loaded roster,
// falling back to the short key when the roster has not loaded (or the key is an
// application/unit, not a person).
function nameFor(key) {
  const m = state.identities.find((i) => i.key === key);
  return m && m.name ? m.name : shortKey(key);
}

function restoreApplicant() {
  const saved = (localStorage.getItem(APPLICANT_KEY) || "").trim();
  state.applicant = saved || null;
}

// loadIdentities fetches the applicant roster (named identities) and rebuilds the
// top-right picker. Non-fatal on error — the picker just shows the empty hint.
async function loadIdentities() {
  try {
    const data = await api("/api/identities");
    state.identities = data.identities || [];
  } catch (_) {
    state.identities = [];
  }
  populateApplicantSelect();
}

// populateApplicantSelect rebuilds the #applicant <select>: a placeholder + one
// option per named identity (label = name, value = key), selecting the persisted
// applicant when it is in the roster.
function populateApplicantSelect() {
  const sel = $("#applicant");
  sel.innerHTML = "";
  const placeholder = document.createElement("option");
  placeholder.value = "";
  placeholder.textContent = state.identities.length
    ? "Select your identity…"
    : "No identities — create one via Loupe/CLI";
  sel.append(placeholder);
  for (const id of state.identities) {
    const o = document.createElement("option");
    o.value = id.key;
    o.textContent = id.name;
    sel.append(o);
  }
  const values = state.identities.map((i) => i.key);
  sel.value = state.applicant && values.includes(state.applicant) ? state.applicant : "";
}

function setApplicant(value) {
  const v = (value || "").trim();
  state.applicant = v || null;
  state.highlight = null; // a highlight belongs to the applicant who just applied
  if (v) localStorage.setItem(APPLICANT_KEY, v);
  else localStorage.removeItem(APPLICANT_KEY);
  renderListings(); // re-enable/disable Apply for the new applicant
  if (state.view === "apps") loadApplications(); // re-scope the tracker to the new applicant
  if (state.view === "tasks") loadTasks(); // re-scope the inbox to the new applicant
  if (state.view === "docs") loadDocsView(); // re-scope the documents to the new applicant
}

// ---- New applicant modal ----
//
// CreateUnclaimedIdentity (identity-domain) requires a name + at least one contact
// (email/phone) + a claimKeyHash = sha256-hex of a client-minted secret (Lattice
// never holds the plaintext). This trusted-tool app mints a random secret, hashes
// it in-browser (crypto.subtle — 127.0.0.1 is a secure context), and submits only
// the hash; the applicant is created directly (no claim ceremony in this demo) and
// becomes the active applicant. Mirrors clinic-app's in-app "New patient".

function openNewApplicant() {
  $("#applicant-form").reset();
  $("#applicant-overlay").hidden = false;
  $("#na-name").focus();
}

function closeNewApplicant() {
  $("#applicant-overlay").hidden = true;
}

// sha256Hex returns the lowercase hex sha256 of a string — the shape
// CreateUnclaimedIdentity stores for claimKeyHash.
async function sha256Hex(s) {
  const buf = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(s));
  return Array.from(new Uint8Array(buf)).map((b) => b.toString(16).padStart(2, "0")).join("");
}

// mintClaimSecret returns a random claim-secret plaintext. It is hashed and only the
// hash is sent; the plaintext never enters Lattice (and, in this demo, is discarded).
function mintClaimSecret() {
  const a = new Uint8Array(32);
  crypto.getRandomValues(a);
  return Array.from(a).map((b) => b.toString(16).padStart(2, "0")).join("");
}

async function submitNewApplicant(ev) {
  ev.preventDefault();
  const name = $("#na-name").value.trim();
  const email = $("#na-email").value.trim();
  const phone = $("#na-phone").value.trim();
  if (!name) {
    toast("A name is required.", "err");
    return;
  }
  if (!email && !phone) {
    toast("Enter an email or a phone number.", "err");
    return;
  }

  const submit = $("#applicant-submit");
  submit.disabled = true;
  try {
    const claimKeyHash = await sha256Hex(mintClaimSecret());
    const payload = { name, claimKeyHash };
    if (email) payload.email = email;
    if (phone) payload.phone = phone;
    const reply = await api("/api/op", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ operationType: "CreateUnclaimedIdentity", class: "identity", payload }),
    });
    if (reply && reply.status === "rejected") {
      const msg = reply.error ? `${reply.error.code}: ${reply.error.message}` : "rejected";
      toast("Could not create applicant — " + msg, "err");
      return;
    }
    const key = reply && reply.primaryKey ? reply.primaryKey : "";
    closeNewApplicant();
    toast("Applicant created.", "ok", key);
    // Make the new applicant active (the roster lens may take a moment to project;
    // select now + reload so the switcher shows it once projected).
    if (key) {
      state.applicant = key;
      localStorage.setItem(APPLICANT_KEY, key);
    }
    await loadIdentities();
    renderListings(); // re-enable Apply for the now-selected applicant
  } catch (e) {
    toast("Could not create applicant: " + e.message, "err");
  } finally {
    submit.disabled = false;
  }
}

// ---- Tabs (Browse & Apply / My Applications / Tasks / Documents) ----

const VIEWS = ["browse", "apps", "tasks", "docs"];

function showView(view) {
  state.view = view;
  for (const v of VIEWS) {
    const isV = v === view;
    $("#view-" + v).hidden = !isV;
    const tab = $("#tab-" + v);
    tab.classList.toggle("active", isV);
    tab.setAttribute("aria-selected", String(isV));
  }
  if (view === "apps") loadApplications();
  if (view === "tasks") loadTasks();
  if (view === "docs") loadDocsView();
}

// ---- Mode (Applicant / Landlord) ----
//
// The two sides of the marketplace share one trusted-tool app. Applicant mode is
// the default (Browse / My Applications / Tasks / Documents over the per-applicant
// identity); Landlord mode swaps to a my-units view over the by-unit aggregate. The
// chosen mode persists across reloads.

const MODES = ["applicant", "landlord"];

function restoreMode() {
  const saved = (localStorage.getItem(MODE_KEY) || "").trim();
  state.mode = MODES.includes(saved) ? saved : "applicant";
}

function setMode(mode) {
  state.mode = MODES.includes(mode) ? mode : "applicant";
  localStorage.setItem(MODE_KEY, state.mode);
  applyMode();
}

function applyMode() {
  const landlord = state.mode === "landlord";
  $("#mode-applicant").classList.toggle("active", !landlord);
  $("#mode-applicant").setAttribute("aria-selected", String(!landlord));
  $("#mode-landlord").classList.toggle("active", landlord);
  $("#mode-landlord").setAttribute("aria-selected", String(landlord));
  $("#applicant-tabs").hidden = landlord;
  $("#applicant-who").hidden = landlord;
  $("#brand-sub").textContent = landlord ? "manage your units" : "apply to lease";
  $("#view-landlord").hidden = !landlord;
  if (landlord) {
    for (const v of VIEWS) $("#view-" + v).hidden = true;
    loadLandlord();
  } else {
    showView(state.view);
  }
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
  $("#apply-applicant").textContent = nameFor(state.applicant);
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
      const errMsg = (reply.error && reply.error.message) || "";
      // The guard rejects a repeat application by the same applicant for the same
      // unit (script fail "DuplicateApplication: ..."); surface it plainly.
      if (errMsg.includes("DuplicateApplication")) {
        toast("You already have an active application for this unit.", "err");
        return;
      }
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
// carries: a closed gap is "done"; an open gap with a standing rejection (a failed
// check no retry has superseded) is "declined"; an open gap with a call in flight
// is "active" (In progress); an open gap with nothing in flight is "todo". A retry
// in flight (inflight) takes precedence over a standing rejection — the check is
// being re-run. The lens does not project a per-row "retries exhausted" signal
// (maxretries_<g> is a constant cap), so a stalled non-declined automated step
// reads as "todo".
function stepState(done, inflight, declined) {
  if (done) return "done";
  if (inflight) return "active";
  if (declined) return "declined";
  return "todo";
}

const STEP_LABEL = { done: "Done", active: "In progress", declined: "Declined", todo: "To do" };

function renderStep(num, title, st, note) {
  const step = document.createElement("li");
  step.className = "step " + st;
  const dot = document.createElement("span");
  dot.className = "step-dot";
  dot.textContent = st === "done" ? "✓" : st === "declined" ? "✕" : String(num);
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

  // Decision banner. Declined takes precedence: a standing rejection (a failed
  // verification OR an explicit landlord decline — both fold into row.declined) is a
  // terminal disposition, not a step still to complete. Finishing the four applicant
  // steps no longer means the application is done — the landlord still has to decide.
  // So "complete" requires BOTH the landlord approval AND the unit actually leased
  // (the genuine done state — an early approval on a not-yet-qualified application
  // does not read "complete"). Between the approval and the listing flip the lease
  // is being finalized (row.landlordApproved, unit not yet leased) — a short window
  // the directOp closes. A qualified-but-undecided application (row.missing_decision)
  // reads "awaiting landlord review."
  const banner = document.createElement("div");
  if (row.declined) {
    banner.className = "decision declined";
    banner.textContent = "Application declined.";
  } else if (row.landlordApproved && row.unitStatus === "leased") {
    banner.className = "decision ok";
    banner.textContent = "Application complete — all steps done.";
  } else if (row.landlordApproved) {
    banner.className = "decision ok";
    banner.textContent = "Approved — finalizing lease.";
  } else if (row.missing_decision) {
    banner.className = "decision pending";
    banner.textContent = "Qualified — awaiting landlord review.";
  } else {
    banner.className = "decision pending";
    banner.textContent = "In review — complete the open steps below.";
  }

  // Stepper (journey order)
  const steps = document.createElement("ol");
  steps.className = "stepper";
  steps.append(
    renderStep(1, "Onboarding (identity details)", stepState(!row.missing_onboarding, false, false)),
    renderStep(2, "Background check", stepState(!row.missing_bgcheck, row.inflight_bgcheck, row.declined_bgcheck)),
    renderStep(3, "Payment", stepState(!row.missing_payment, row.inflight_payment, row.declined_payment)),
    renderStep(4, "Sign lease", stepState(!row.missing_signature, false, false)),
  );

  card.append(head, banner, steps);

  // Withdraw: back out of an application before the landlord approves (frees the
  // applicant to re-apply to the same unit). Stays available while the application is
  // qualified-but-undecided (awaiting landlord review) — the applicant may still
  // change their mind. Hidden once the landlord approves — the unit is being leased.
  if (!row.landlordApproved && row.unitKey) {
    const actions = document.createElement("div");
    actions.className = "card-actions";
    const wd = document.createElement("button");
    wd.className = "ghost danger";
    wd.textContent = "Withdraw application";
    wd.addEventListener("click", () => withdrawApplication(row));
    actions.append(wd);
    card.append(actions);
  }
  return card;
}

// withdrawApplication submits WithdrawLeaseApplication (tombstones the leaseapp +
// prunes the unit's application index) after a confirm, then reloads — the
// withdrawn application drops from the tracker and the unit frees for re-apply.
async function withdrawApplication(row) {
  if (!confirm("Withdraw this application? You'll be able to apply to this unit again.")) return;
  try {
    const reply = await api("/api/op", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        operationType: "WithdrawLeaseApplication",
        class: "leaseapp",
        reads: [row.entityKey],
        payload: { leaseAppKey: row.entityKey, unit: row.unitKey },
      }),
    });
    if (reply && reply.status === "rejected") {
      const msg = reply.error ? `${reply.error.code}: ${reply.error.message}` : "rejected";
      toast("Could not withdraw — " + msg, "err");
      return;
    }
    toast("Application withdrawn.", "ok");
    loadApplications();
  } catch (e) {
    toast("Could not withdraw: " + e.message, "err");
  }
}

// ---- Tasks (inbox) ----
//
// The applicant's OPEN tasks, read from the `my-tasks` lens projection (P5: a
// vertical app reads a read-model, never Core KV — Loupe scans Core KV only as the
// inspector). Each task is self-describing (the lens aspect-hops the op name +
// description off the forOperation meta), and completion submits the bound op.

async function loadTasks() {
  const grid = $("#tasks");
  const empty = $("#tasks-empty");
  if (!state.applicant) {
    grid.innerHTML = "";
    state.tasks = [];
    empty.hidden = false;
    empty.textContent = "Select an applicant identity above to see their tasks.";
    $("#tasks-summary").textContent = "";
    return;
  }
  $("#tasks-summary").textContent = "loading…";
  try {
    const data = await api("/api/tasks?applicant=" + encodeURIComponent(state.applicant));
    state.tasks = data.tasks || [];
  } catch (e) {
    grid.innerHTML = "";
    empty.hidden = false;
    empty.textContent = "Could not load tasks: " + e.message;
    $("#tasks-summary").textContent = "";
    return;
  }
  renderTasks();
}

function renderTasks() {
  const grid = $("#tasks");
  const empty = $("#tasks-empty");
  grid.innerHTML = "";
  if (state.tasks.length === 0) {
    empty.hidden = false;
    empty.textContent = "No open tasks. When your application needs you to act, it will show up here.";
    $("#tasks-summary").textContent = "";
    return;
  }
  empty.hidden = true;
  for (const t of state.tasks) grid.append(renderTaskCard(t));
  const n = state.tasks.length;
  $("#tasks-summary").textContent = `${n} open task${n === 1 ? "" : "s"}`;
}

function renderTaskCard(t) {
  const card = document.createElement("div");
  card.className = "card task-card";

  const title = document.createElement("div");
  title.className = "addr";
  title.textContent = t.operationName || shortKey(t.operation) || "Task";

  const desc = document.createElement("div");
  desc.className = "addr-sub";
  desc.textContent = t.operationDescription || "";

  const scope = document.createElement("div");
  scope.className = "task-scope mono";
  scope.textContent = t.scopedTo ? shortKey(t.scopedTo) : shortKey(t.taskKey);

  const meta = document.createElement("div");
  meta.className = "meta";
  if (t.expiresAt) meta.textContent = "due " + fmtDate(t.expiresAt);

  const actions = document.createElement("div");
  actions.className = "card-actions";
  const badge = document.createElement("span");
  badge.className = "badge pending";
  badge.textContent = "open";
  const btn = document.createElement("button");
  const canComplete = !!COMPLETIONS[t.operationName];
  btn.textContent = canComplete ? "Complete" : "Complete in Loupe";
  btn.disabled = !canComplete;
  btn.title = canComplete ? "" : "This task type isn't completable in this app yet — use Loupe's Submit Op.";
  if (canComplete) btn.addEventListener("click", () => openComplete(t));
  actions.append(badge, btn);

  card.append(title);
  if (desc.textContent) card.append(desc);
  card.append(scope);
  if (meta.textContent) card.append(meta);
  card.append(actions);
  return card;
}

// ---- Complete task modal ----

function openComplete(task) {
  const desc = COMPLETIONS[task.operationName];
  if (!desc) return;
  state.currentTask = task;
  $("#complete-title").textContent = desc.title;
  $("#complete-desc").textContent = task.operationDescription || "";
  $("#tc-target").textContent = task.scopedTo || task.taskKey;
  $("#tc-sensitive").hidden = !desc.sensitive;
  $("#complete-submit").textContent = desc.submitLabel || "Complete";

  const host = $("#tc-fields");
  host.innerHTML = "";
  for (const f of desc.fields) {
    const wrap = document.createElement("div");
    wrap.className = "field";
    const label = document.createElement("label");
    label.setAttribute("for", "tc-" + f.name);
    label.textContent = f.label + (f.required ? "" : " (optional)");
    const input = document.createElement("input");
    input.id = "tc-" + f.name;
    input.type = f.type || "text";
    if (f.placeholder) input.placeholder = f.placeholder;
    wrap.append(label, input);
    host.append(wrap);
  }
  $("#complete-overlay").hidden = false;
  const first = host.querySelector("input");
  if (first) first.focus();
}

function closeComplete() {
  $("#complete-overlay").hidden = true;
  state.currentTask = null;
}

async function submitComplete(ev) {
  ev.preventDefault();
  const task = state.currentTask;
  if (!task) return;
  const desc = COMPLETIONS[task.operationName];
  if (!desc) return;

  const target = task.scopedTo || "";
  if (!target) {
    toast("This task has no target to act on.", "err");
    return;
  }
  const payload = {};
  payload[desc.targetField] = target;
  for (const f of desc.fields) {
    const v = ($("#tc-" + f.name).value || "").trim();
    if (!v) {
      if (f.required) {
        toast(f.label + " is required.", "err");
        return;
      }
      continue;
    }
    payload[f.name] = v;
  }

  const submit = $("#complete-submit");
  submit.disabled = true;
  try {
    const reply = await api("/api/op", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        operationType: task.operationName,
        class: desc.klass,
        reads: [target],
        payload,
      }),
    });
    if (reply && reply.status === "rejected") {
      const msg = reply.error ? `${reply.error.code}: ${reply.error.message}` : "rejected";
      toast("Could not complete — " + msg, "err");
      return;
    }
    // The bound op closed the gap; now retire the task. This app submits as the
    // trusted admin actor (a standing permission), NOT via the task's ephemeral
    // grant, so the Processor's task-path auto-complete (Contract #10 §10.7) does
    // not fire — we close the task ourselves through the contract's retained
    // out-of-band CompleteTask path. A benign rejection (the task already closed,
    // e.g. a double-submit) is non-fatal: the bound op already committed.
    await completeTask(task.taskKey);
    closeComplete();
    toast(desc.title + " — done.", "ok");
    loadTasks();
    loadApplications();
  } catch (e) {
    toast("Could not complete: " + e.message, "err");
  } finally {
    submit.disabled = false;
  }
}

// completeTask submits an explicit CompleteTask(taskKey) — the Contract #10 §10.7
// out-of-band completion path — to retire the task whose bound op just committed.
// Best-effort: a rejection (the task already closed) or a transport error is logged,
// never surfaced, because the gap-closing op has already succeeded.
async function completeTask(taskKey) {
  if (!taskKey) return;
  try {
    const reply = await api("/api/op", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        operationType: "CompleteTask",
        class: "task",
        reads: [taskKey],
        payload: { taskKey },
      }),
    });
    if (reply && reply.status === "rejected" && reply.error) {
      console.warn("CompleteTask not applied:", reply.error.code, reply.error.message);
    }
  } catch (e) {
    console.warn("CompleteTask request failed:", e.message);
  }
}

// ---- Documents (upload / view / list) ----
//
// The applicant's documents, read from the `objectAttachments` lens projection
// (P5: a vertical app reads a read-model, never Core KV). A document is attached
// to a "scope" — the applicant's identity (ID docs) or one of their applications
// (proof-of-income, signed lease) — chosen in the scope selector; uploads attach
// to that scope and the list shows that scope's documents. Bytes flow through the
// Go server's object endpoints, never the Refractor.

// loadDocsView refreshes the scope selector (identity + the applicant's
// applications) then loads the selected scope's documents.
async function loadDocsView() {
  const empty = $("#docs-empty");
  const grid = $("#docs");
  if (!state.applicant) {
    grid.innerHTML = "";
    state.docs = [];
    $("#doc-scope").innerHTML = "";
    empty.hidden = false;
    empty.textContent = "Select an applicant identity above to manage their documents.";
    $("#docs-summary").textContent = "";
    return;
  }
  // Refresh applications so the scope selector lists the applicant's current
  // applications; a failure is non-fatal (the identity scope still works).
  try {
    const data = await api("/api/applications?applicant=" + encodeURIComponent(state.applicant));
    state.applications = data.applications || [];
  } catch (_) {
    /* keep whatever applications we already had */
  }
  populateDocScope();
  loadDocuments();
}

// populateDocScope rebuilds the scope <select>: the applicant's identity first,
// then one option per application (value = the owner key the documents link to).
function populateDocScope() {
  const sel = $("#doc-scope");
  const prev = state.docScope;
  sel.innerHTML = "";
  const opt = (value, label) => {
    const o = document.createElement("option");
    o.value = value;
    o.textContent = label;
    sel.append(o);
  };
  opt(state.applicant, "Your identity (" + nameFor(state.applicant) + ")");
  for (const a of state.applications) {
    const label = a.unitAddress || (a.unitKey ? shortKey(a.unitKey) : shortKey(a.entityKey));
    opt(a.entityKey, "Application · " + label);
  }
  // Keep the previous selection if it still exists, else default to identity.
  const values = Array.from(sel.options).map((o) => o.value);
  state.docScope = prev && values.includes(prev) ? prev : state.applicant;
  sel.value = state.docScope;
}

async function loadDocuments() {
  const grid = $("#docs");
  const empty = $("#docs-empty");
  const scope = state.docScope;
  if (!scope) {
    grid.innerHTML = "";
    state.docs = [];
    return;
  }
  $("#docs-summary").textContent = "loading…";
  try {
    const data = await api("/api/objects?applicant=" + encodeURIComponent(scope));
    state.docs = data.documents || [];
  } catch (e) {
    grid.innerHTML = "";
    empty.hidden = false;
    empty.textContent = "Could not load documents: " + e.message;
    $("#docs-summary").textContent = "";
    return;
  }
  renderDocuments();
}

function renderDocuments() {
  const grid = $("#docs");
  const empty = $("#docs-empty");
  grid.innerHTML = "";
  if (state.docs.length === 0) {
    empty.hidden = false;
    empty.textContent = "No documents yet. Upload an ID, proof of income, or signed lease above.";
    $("#docs-summary").textContent = "";
    return;
  }
  empty.hidden = true;
  for (const d of state.docs) grid.append(renderDocCard(d));
  const n = state.docs.length;
  $("#docs-summary").textContent = `${n} document${n === 1 ? "" : "s"}`;
}

function fmtSize(n) {
  if (typeof n !== "number" || n < 0) return "";
  if (n < 1024) return n + " B";
  if (n < 1024 * 1024) return (n / 1024).toFixed(1) + " KB";
  return (n / (1024 * 1024)).toFixed(1) + " MB";
}

function renderDocCard(d) {
  const card = document.createElement("div");
  card.className = "card doc-card";

  const sess = state.sessionUploads[d.oid];
  const title = document.createElement("div");
  title.className = "addr";
  title.textContent = sess && DOC_SLOTS[sess.linkName] ? DOC_SLOTS[sess.linkName] : "Document";

  const meta = document.createElement("div");
  meta.className = "addr-sub";
  meta.textContent = [d.contentType || "file", fmtSize(d.size)].filter(Boolean).join("  ·  ");

  const ref = document.createElement("div");
  ref.className = "addr-sub mono";
  ref.textContent = d.oid;

  const actions = document.createElement("div");
  actions.className = "card-actions";
  const view = document.createElement("a");
  view.className = "ghost btn-link";
  view.textContent = "View";
  view.href = "/api/objects/" + encodeURIComponent(d.oid);
  view.target = "_blank";
  view.rel = "noopener";
  actions.append(view);

  // Detach is available for documents uploaded this session (the FE knows the
  // link name); a doc listed from a prior session has no link name in the read
  // model, so detach of those is a documented follow-up.
  if (sess) {
    const detach = document.createElement("button");
    detach.className = "ghost danger";
    detach.textContent = "Detach";
    detach.addEventListener("click", () => detachDoc(d.oid, sess));
    actions.append(detach);
  }

  card.append(title, meta, ref, actions);
  return card;
}

async function submitUpload(ev) {
  ev.preventDefault();
  if (!state.applicant) {
    toast("Select an applicant first.", "err");
    return;
  }
  const scope = state.docScope;
  if (!scope) {
    toast("Choose what to attach the document to.", "err");
    return;
  }
  const slot = $("#doc-slot").value;
  const fileInput = $("#doc-file");
  const file = fileInput.files && fileInput.files[0];
  if (!file) {
    toast("Choose a file to upload.", "err");
    return;
  }

  const fd = new FormData();
  fd.append("file", file);
  fd.append("targetKey", scope);
  fd.append("linkName", slot);

  const submit = $("#upload-submit");
  submit.disabled = true;
  try {
    const reply = await api("/api/objects", { method: "POST", body: fd });
    if (reply && reply.status === "rejected") {
      const msg = reply.error ? `${reply.error.code}: ${reply.error.message}` : "rejected";
      toast("Upload rejected — " + msg, "err");
      return;
    }
    if (reply && reply.oid) {
      state.sessionUploads[reply.oid] = { linkName: slot, ownerKey: scope };
    }
    fileInput.value = "";
    toast("Document uploaded.", "ok", reply && reply.oid ? reply.oid : "");
    // The lens may take a moment to project; a Refresh shows it once projected.
    loadDocuments();
  } catch (e) {
    toast("Could not upload: " + e.message, "err");
  } finally {
    submit.disabled = false;
  }
}

async function detachDoc(oid, sess) {
  if (!confirm("Detach this document? The file is removed from this record.")) return;
  try {
    const q = "?targetKey=" + encodeURIComponent(sess.ownerKey) + "&linkName=" + encodeURIComponent(sess.linkName);
    const reply = await api("/api/objects/" + encodeURIComponent(oid) + q, { method: "DELETE" });
    if (reply && reply.status === "rejected") {
      const msg = reply.error ? `${reply.error.code}: ${reply.error.message}` : "rejected";
      toast("Could not detach — " + msg, "err");
      return;
    }
    delete state.sessionUploads[oid];
    toast("Document detached.", "ok");
    loadDocuments();
  } catch (e) {
    toast("Could not detach: " + e.message, "err");
  }
}

// ---- Landlord — my units ----
//
// The by-unit aggregate from /api/unit-applications (P5: three lens read models,
// never Core KV): every listed unit and the live applications against it. The
// landlord posts a listing (a CreateLocation → SetUnitAddress → SetListing chain)
// and decides a qualified application (DecideLeaseApplication approve/decline) — the
// human-in-the-loop the convergence lens now gates the lease behind.

// DISPOSITION maps an applicantSummary.status to its badge label + class.
const DISPOSITION = {
  leased: { label: "Leased", cls: "leased" },
  approved: { label: "Approved — leasing", cls: "approved" },
  qualified: { label: "Qualified — awaiting decision", cls: "qualified" },
  declined: { label: "Declined", cls: "declined" },
  in_review: { label: "In review", cls: "review" },
};

// moneyAmount formats a bare rent number (the by-unit row carries no currency) as a
// USD-style figure; the listings in this demo are USD.
function moneyAmount(n) {
  return typeof n === "number" ? "$" + n.toLocaleString() : "—";
}

async function loadLandlord() {
  const grid = $("#units");
  const empty = $("#units-empty");
  $("#units-summary").textContent = "loading…";
  try {
    const data = await api("/api/unit-applications");
    state.units = data.units || [];
  } catch (e) {
    grid.innerHTML = "";
    empty.hidden = false;
    empty.textContent = "Could not load units: " + e.message;
    $("#units-summary").textContent = "";
    return;
  }
  renderUnits();
}

function renderUnits() {
  const grid = $("#units");
  const empty = $("#units-empty");
  grid.innerHTML = "";
  if (state.units.length === 0) {
    empty.hidden = false;
    empty.textContent = "No units listed yet. Post a listing to get started.";
    $("#units-summary").textContent = "";
    return;
  }
  empty.hidden = true;
  for (const u of state.units) grid.append(renderUnitCard(u));
  const n = state.units.length;
  $("#units-summary").textContent = `${n} unit${n === 1 ? "" : "s"}`;
}

function renderUnitCard(u) {
  const card = document.createElement("div");
  card.className = "card unit-card";

  const head = document.createElement("div");
  head.className = "unit-head";
  const addr = document.createElement("div");
  addr.className = "addr";
  addr.textContent = u.unitAddress || "Unit " + shortKey(u.unitKey);
  const sub = document.createElement("div");
  sub.className = "unit-sub";
  const rent = document.createElement("span");
  rent.textContent = u.unitRent != null ? moneyAmount(u.unitRent) + " / month" : "—";
  const status = u.unitStatus || "—";
  const badge = document.createElement("span");
  badge.className = "badge " + status;
  badge.textContent = status;
  sub.append(rent, badge);
  head.append(addr, sub);

  const count = document.createElement("div");
  count.className = "unit-count";
  count.textContent = u.applicationCount === 1 ? "1 application" : `${u.applicationCount} applications`;

  card.append(head, count);

  const list = document.createElement("div");
  list.className = "applicants";
  if (!u.applications || u.applications.length === 0) {
    const none = document.createElement("div");
    none.className = "applicant-none";
    none.textContent = "No applications yet.";
    list.append(none);
  } else {
    for (const a of u.applications) list.append(renderApplicantRow(a, u));
  }
  card.append(list);
  return card;
}

function renderApplicantRow(a, unit) {
  const row = document.createElement("div");
  row.className = "applicant";

  const info = document.createElement("div");
  info.className = "applicant-info";
  const name = document.createElement("span");
  name.className = "applicant-name";
  name.textContent = a.applicantName || shortKey(a.applicant);
  const disp = DISPOSITION[a.status] || { label: a.status || "—", cls: "review" };
  const badge = document.createElement("span");
  badge.className = "disp " + disp.cls;
  badge.textContent = disp.label;
  info.append(name, badge);
  if (a.signed) {
    const signed = document.createElement("span");
    signed.className = "signed";
    signed.textContent = "✓ signed";
    info.append(signed);
  }
  row.append(info);

  const unitLeased = unit.unitStatus === "leased";
  if (a.qualified && !unitLeased) {
    const actions = document.createElement("div");
    actions.className = "applicant-actions";
    const approve = document.createElement("button");
    approve.textContent = "Approve";
    approve.addEventListener("click", () => decideApplication(a, "approved"));
    const decline = document.createElement("button");
    decline.className = "ghost danger";
    decline.textContent = "Decline";
    decline.addEventListener("click", () => decideApplication(a, "declined"));
    actions.append(approve, decline);
    row.append(actions);
  } else if (unitLeased && a.status !== "leased" && a.status !== "declined") {
    const note = document.createElement("div");
    note.className = "applicant-note";
    note.textContent = "Unit leased to another applicant.";
    row.append(note);
  }
  return row;
}

// decideApplication records the landlord's approve/decline (DecideLeaseApplication)
// for a qualified application, then reloads after a beat so the new disposition (and
// any unit-leased flip the convergence lens drives) shows once reprojected.
async function decideApplication(a, decision) {
  const who = a.applicantName || shortKey(a.applicant);
  if (decision === "declined" && !confirm(`Decline ${who}'s application?`)) return;
  try {
    const reply = await api("/api/op", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        operationType: "DecideLeaseApplication",
        class: "leaseapp",
        reads: [a.leaseAppKey],
        payload: { leaseAppKey: a.leaseAppKey, decision },
      }),
    });
    if (reply && reply.status === "rejected") {
      const msg = reply.error ? `${reply.error.code}: ${reply.error.message}` : "rejected";
      toast("Decision rejected — " + msg, "err");
      return;
    }
    toast(decision === "approved" ? "Application approved." : "Application declined.", "ok");
    setTimeout(loadLandlord, 800);
  } catch (e) {
    toast("Could not record decision: " + e.message, "err");
  }
}

// ---- Post a listing (landlord) ----

function openPostListing() {
  $("#listing-form").reset();
  $("#li-currency").value = "USD";
  $("#listing-overlay").hidden = false;
  $("#li-line1").focus();
}

function closePostListing() {
  $("#listing-overlay").hidden = true;
}

// opOrThrow submits an op and throws on a rejection or transport error, so the
// post-a-listing chain stops at the first failure with a message naming the step.
async function opOrThrow(body, what) {
  const reply = await api("/api/op", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
  if (reply && reply.status === "rejected") {
    const msg = reply.error ? `${reply.error.code}: ${reply.error.message}` : "rejected";
    throw new Error(`Could not ${what} — ${msg}`);
  }
  return reply || {};
}

// submitPostListing runs the three-op chain that mints + lists a unit: CreateLocation
// (the reply's primaryKey is the new vtx.unit key) → SetUnitAddress → SetListing. Each
// step awaits the prior since the address/listing target the unit the first op mints.
async function submitPostListing(ev) {
  ev.preventDefault();
  const line1 = $("#li-line1").value.trim();
  const line2 = $("#li-line2").value.trim();
  const city = $("#li-city").value.trim();
  const region = $("#li-region").value.trim();
  const postal = $("#li-postal").value.trim();
  const rent = Number($("#li-rent").value);
  const currency = $("#li-currency").value.trim() || "USD";
  const bedrooms = $("#li-bedrooms").value;
  const bathrooms = $("#li-bathrooms").value;
  const sqft = $("#li-sqft").value;

  if (!line1 || !city || !region || !postal) {
    toast("Fill in the full address (line 1, city, region, postal).", "err");
    return;
  }
  if (!(rent > 0)) {
    toast("Enter a monthly rent greater than zero.", "err");
    return;
  }
  if (bedrooms === "") {
    toast("Enter the number of bedrooms.", "err");
    return;
  }

  const submit = $("#listing-submit");
  submit.disabled = true;
  try {
    const created = await opOrThrow(
      { operationType: "CreateLocation", class: "location", payload: { locationType: "unit" } },
      "create the unit",
    );
    const unitKey = created.primaryKey;
    if (!unitKey) {
      toast("The unit was created but returned no key; try Refresh.", "err");
      return;
    }

    const addr = { unit: unitKey, line1, city, region, postal };
    if (line2) addr.line2 = line2;
    await opOrThrow(
      { operationType: "SetUnitAddress", class: "loftspaceListing", reads: [unitKey], payload: addr },
      "set the address",
    );

    const listing = { unit: unitKey, rentAmount: rent, rentCurrency: currency, bedrooms: Number(bedrooms), status: "available" };
    if (bathrooms !== "") listing.bathrooms = Number(bathrooms);
    if (sqft !== "") listing.sqft = Number(sqft);
    await opOrThrow(
      { operationType: "SetListing", class: "loftspaceListing", reads: [unitKey], payload: listing },
      "create the listing",
    );

    closePostListing();
    toast("Listing posted.", "ok", unitKey);
    setTimeout(loadLandlord, 800);
  } catch (e) {
    toast(e.message, "err");
  } finally {
    submit.disabled = false;
  }
}

// ---- wire up ----

function init() {
  restoreApplicant();
  restoreMode();
  loadIdentities();
  $("#applicant").addEventListener("change", (e) => setApplicant(e.target.value));
  $("#new-applicant").addEventListener("click", openNewApplicant);
  $("#applicant-cancel").addEventListener("click", closeNewApplicant);
  $("#applicant-overlay").addEventListener("click", (e) => {
    if (e.target === $("#applicant-overlay")) closeNewApplicant();
  });
  $("#applicant-form").addEventListener("submit", submitNewApplicant);
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
  $("#tab-tasks").addEventListener("click", () => showView("tasks"));
  $("#tab-docs").addEventListener("click", () => showView("docs"));
  $("#reload-apps").addEventListener("click", loadApplications);
  $("#reload-tasks").addEventListener("click", loadTasks);
  $("#reload-docs").addEventListener("click", loadDocsView);
  $("#doc-scope").addEventListener("change", (e) => {
    state.docScope = e.target.value;
    loadDocuments();
  });
  $("#upload-form").addEventListener("submit", submitUpload);
  $("#complete-cancel").addEventListener("click", closeComplete);
  $("#complete-overlay").addEventListener("click", (e) => {
    if (e.target === $("#complete-overlay")) closeComplete();
  });
  $("#complete-form").addEventListener("submit", submitComplete);

  $("#mode-applicant").addEventListener("click", () => setMode("applicant"));
  $("#mode-landlord").addEventListener("click", () => setMode("landlord"));
  $("#post-listing").addEventListener("click", openPostListing);
  $("#reload-units").addEventListener("click", loadLandlord);
  $("#listing-cancel").addEventListener("click", closePostListing);
  $("#listing-overlay").addEventListener("click", (e) => {
    if (e.target === $("#listing-overlay")) closePostListing();
  });
  $("#listing-form").addEventListener("submit", submitPostListing);

  loadListings();
  applyMode();
}

document.addEventListener("DOMContentLoaded", init);
