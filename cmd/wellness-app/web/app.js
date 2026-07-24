"use strict";

// ---- wire helpers -----------------------------------------------------

async function api(path, opts) {
  const res = await fetch(path, opts);
  let body = null;
  try { body = await res.json(); } catch (_) { /* no body */ }
  if (body && typeof body.status === "string") return body;
  if (!res.ok || (body && body.error)) {
    const e = body && body.error;
    throw new Error((typeof e === "string" ? e : e && e.message) || `HTTP ${res.status}`);
  }
  return body;
}

let gatewayURLCache = null;
async function gatewayURL() {
  if (gatewayURLCache) return gatewayURLCache;
  const body = await api("/api/config");
  gatewayURLCache = body.gatewayUrl;
  return gatewayURLCache;
}

let staffTokenCache = null;
async function staffReadToken() {
  if (staffTokenCache && Date.parse(staffTokenCache.expiresAt) - Date.now() > 5000) {
    return staffTokenCache.token;
  }
  const body = await api("/api/staff/dev-token", { method: "POST" });
  staffTokenCache = body;
  return body.token;
}

// ---- self-service session -------------------------------------------

// selfBookerKey is the signed-in resident's own identity key (the "Me" bar),
// persisted across reloads. CreateBooking/CancelBooking's consumer
// scope=self grant targets the identity vertex directly (no device-claim
// indirection — wellness-domain permissions.go), so signing in is just
// picking which resident you are, not a claim/link ceremony.
const SELF_BOOKER_STORAGE_KEY = "wellness.selfBookerKey";
let selfBookerKey = localStorage.getItem(SELF_BOOKER_STORAGE_KEY) || null;

function signInAsSelf(bookerKey) {
  selfBookerKey = bookerKey;
  selfTokenCache = null;
  localStorage.setItem(SELF_BOOKER_STORAGE_KEY, bookerKey);
}

function signOutSelf() {
  selfBookerKey = null;
  selfTokenCache = null;
  localStorage.removeItem(SELF_BOOKER_STORAGE_KEY);
}

let selfTokenCache = null;
async function selfWriteToken() {
  if (!selfBookerKey) throw new Error("not signed in");
  if (selfTokenCache && selfTokenCache.subject === selfBookerKey &&
      Date.parse(selfTokenCache.expiresAt) - Date.now() > 5000) {
    return selfTokenCache.token;
  }
  const body = await api("/api/dev-token", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ subject: idOf(selfBookerKey) }),
  });
  selfTokenCache = { subject: selfBookerKey, token: body.token, expiresAt: body.expiresAt };
  return body.token;
}

// submitOp posts one operation to the Gateway, browser-direct. By default it
// uses the staff Bearer token (operator scope:any covers most ops); passing
// opts.asSelf instead submits with a token minted for the signed-in
// resident's own identity, and stamps authContext.target so the platform's
// scope=self check (op.actor == authContext.target) and wellness-domain's
// own booker/bookedBy check both resolve to that resident.
async function submitOp(body, opts) {
  const asSelf = !!(opts && opts.asSelf);
  const [base, token] = await Promise.all([gatewayURL(), asSelf ? selfWriteToken() : staffReadToken()]);
  const withAuth = asSelf ? Object.assign({}, body, { authContext: { target: selfBookerKey } }) : body;
  return api(base + "/v1/operations", {
    method: "POST",
    headers: { "Content-Type": "application/json", Authorization: "Bearer " + token },
    body: JSON.stringify(withAuth),
  });
}

// idOf returns a key's raw trailing NanoID segment (unlike shortKey, which
// truncates for display) — used to compose a link key from two vertex keys.
function idOf(key) {
  const parts = (key || "").split(".");
  return parts[parts.length - 1];
}

// seatKeys enumerates a session's seat-claim aspect keys up to its capacity,
// mirroring the Starlark's claim_first_free_seat loop (ddls.go) so the
// dispatcher can declare each as an optionalReads (script-read-posture-
// design.md §13, class-d): an absent seat is the common case (open spot),
// never a required read.
function seatKeys(sessionKey, capacity) {
  const keys = [];
  for (let n = 1; n <= capacity; n++) keys.push(sessionKey + ".seat" + n);
  return keys;
}

async function opOrThrow(body, what, opts) {
  const reply = await submitOp(body, opts);
  if (reply && reply.status === "rejected") {
    const msg = reply.error ? `${reply.error.code}: ${reply.error.message}` : "rejected";
    throw new Error(`Could not ${what} — ${msg}`);
  }
  return reply || {};
}

// ---- formatting --------------------------------------------------------

function shortKey(key) {
  if (!key) return "";
  const parts = key.split(".");
  const id = parts[parts.length - 1];
  return id.length > 10 ? id.slice(0, 6) + "…" + id.slice(-4) : id;
}

function fmtRange(startsAt, endsAt) {
  return (startsAt || "?") + " → " + (endsAt || "?");
}

// ---- toast ---------------------------------------------------------

let toastTimer = null;
function toast(msg, ok) {
  const el = document.getElementById("toast");
  el.textContent = msg;
  el.className = "toast " + (ok ? "ok" : "err");
  el.hidden = false;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => { el.hidden = true; }, 5000);
}

// ---- view routing -------------------------------------------------

function showView(view) {
  document.querySelectorAll("[role=tabpanel]").forEach((s) => {
    s.hidden = s.id !== "view-" + view;
  });
  document.querySelectorAll(".tab").forEach((b) => {
    const active = b.dataset.view === view;
    b.classList.toggle("active", active);
    b.setAttribute("aria-selected", active ? "true" : "false");
  });
  if (view === "schedule") loadSchedule();
  else if (view === "roster") loadRoster();
  else if (view === "myclasses") loadMyClasses();
  else if (view === "studios") loadStudiosAdmin();
}

// ---- shared picker data ------------------------------------------------

let studiosCache = null;
async function loadStudios() {
  if (studiosCache) return studiosCache;
  const body = await api("/api/studios");
  studiosCache = body.studios || [];
  return studiosCache;
}

let residentsCache = null;
async function loadResidents() {
  if (residentsCache) return residentsCache;
  const body = await api("/api/residents");
  residentsCache = body.residents || [];
  return residentsCache;
}

function fillResidentSelect(select, residents, allowAll) {
  const prev = select.value;
  select.innerHTML = "";
  if (allowAll) {
    const opt = document.createElement("option");
    opt.value = "";
    opt.textContent = "(choose a resident)";
    select.appendChild(opt);
  }
  if (!residents.length) {
    const opt = document.createElement("option");
    opt.textContent = "(no residents)";
    opt.value = "";
    select.appendChild(opt);
    return;
  }
  for (const r of residents) {
    const opt = document.createElement("option");
    opt.value = r.bookerKey;
    opt.dataset.leaseAppKey = r.leaseAppKey;
    opt.textContent = shortKey(r.bookerKey) + (r.approved ? " (resident)" : " (applicant)");
    select.appendChild(opt);
  }
  if (prev && residents.some((r) => r.bookerKey === prev)) select.value = prev;
}

// leaseAppKeyForBooker looks up a booker's lease application key from the
// resident roster (the resident-rate hint payload param), uniformly for both
// the staff picker and a signed-in self-service booker.
function leaseAppKeyForBooker(bookerKey, residents) {
  const r = residents.find((x) => x.bookerKey === bookerKey);
  return r ? r.leaseAppKey : "";
}

// ---- Schedule view ------------------------------------------------

async function loadSchedule() {
  const studioSelect = document.getElementById("schedule-studio");
  if (!studioSelect.dataset.loaded) {
    const studios = await loadStudios();
    const prev = studioSelect.value;
    studioSelect.innerHTML = '<option value="">(all studios)</option>';
    for (const s of studios) {
      const opt = document.createElement("option");
      opt.value = s.studioKey;
      opt.textContent = s.name;
      studioSelect.appendChild(opt);
    }
    if (prev) studioSelect.value = prev;
    studioSelect.dataset.loaded = "1";
  }
  await renderSchedule();
}

async function renderSchedule() {
  const grid = document.getElementById("schedule-grid");
  const summary = document.getElementById("schedule-summary");
  const studioKey = document.getElementById("schedule-studio").value;
  grid.innerHTML = "";
  summary.textContent = "";
  let sessions;
  try {
    const r = await api("/api/sessions");
    sessions = r.sessions || [];
  } catch (e) {
    grid.innerHTML = '<div class="empty">' + e.message + "</div>";
    return;
  }
  if (studioKey) sessions = sessions.filter((se) => se.studioKey === studioKey);
  summary.textContent = sessions.length + " session" + (sessions.length === 1 ? "" : "s");
  if (!sessions.length) {
    grid.innerHTML = '<div class="empty">No upcoming sessions.</div>';
    return;
  }
  const residents = await loadResidents();
  const selfMode = !!selfBookerKey;
  grid.innerHTML = sessions.map((se) => scheduleCard(se, selfMode)).join("");
  sessions.forEach((se) => {
    const id = domId(se.sessionKey);
    const bookBtn = document.getElementById("book-" + id);
    const select = document.getElementById("booker-" + id);
    if (select) fillResidentSelect(select, residents, true);
    if (!bookBtn) return;
    bookBtn.addEventListener("click", async () => {
      const bookerKey = selfBookerKey || (select ? select.value : "");
      if (!bookerKey) { toast("Pick a resident to book.", false); return; }
      const leaseAppKey = leaseAppKeyForBooker(bookerKey, residents);
      bookBtn.disabled = true;
      try {
        const payload = { session: se.sessionKey, booker: bookerKey };
        if (leaseAppKey) payload.leaseAppKey = leaseAppKey;
        // Resident-rate lookup (leaseapp + .tenancy + applicationFor link) is
        // (d)-declared optionalReads — absence just falls through to the
        // standard rate (ddls.go, script-read-posture-design.md §13).
        const optionalReads = seatKeys(se.sessionKey, se.capacity);
        // The per-(session, booker) double-book guard (ddls.go). It MUST be
        // declared, not merely relied on via CreateOnly-at-commit like the
        // seats: the script reads its current state to tell absent (mint) from
        // tombstoned (OCC-revive a re-book) from alive (clean DoubleBooked
        // reject). Absence is the common case (first book), hence optionalReads.
        optionalReads.push(se.sessionKey + ".bkr" + idOf(bookerKey));
        if (leaseAppKey) {
          optionalReads.push(
            leaseAppKey,
            leaseAppKey + ".tenancy",
            "lnk.leaseapp." + idOf(leaseAppKey) + ".applicationFor.identity." + idOf(bookerKey),
          );
        }
        // booker is an (a)-declared required read (require_live_typed, ddls.go)
        // — CreateBooking fails UnknownEndpoint without it, staff or self alike.
        await opOrThrow(
          { operationType: "CreateBooking", class: "booking", reads: [se.sessionKey, se.sessionKey + ".schedule", bookerKey], optionalReads, payload },
          "book the class",
          { asSelf: selfMode }
        );
        toast("Booked.", true);
        setTimeout(renderSchedule, 700);
      } catch (e) {
        toast(e.message, false);
        bookBtn.disabled = false;
      }
    });
  });
}

function domId(key) {
  return key.replace(/[^a-zA-Z0-9]/g, "");
}

function scheduleCard(se, selfMode) {
  const id = domId(se.sessionKey);
  const full = se.bookedCount >= se.capacity;
  const picker = selfMode ? '<span class="me-inline">booking as you</span>' : '<select id="booker-' + id + '"></select>';
  return (
    '<div class="card">' +
    '<span class="badge ' + (full ? "settled" : "open") + '">' + se.bookedCount + " / " + se.capacity + " seats</span>" +
    '<div class="who">' + (se.name || "?") + "</div>" +
    '<div class="meta">' + (se.studioName || shortKey(se.studioKey)) + "</div>" +
    '<div class="meta">' + fmtRange(se.startsAt, se.endsAt) + "</div>" +
    '<div class="field-row">' +
    picker +
    '<button id="book-' + id + '"' + (full ? " disabled" : "") + ">Book</button>" +
    "</div>" +
    "</div>"
  );
}

// ---- Roster view --------------------------------------------------------

async function loadRoster() {
  const select = document.getElementById("roster-session");
  if (!select.dataset.loaded) {
    const r = await api("/api/sessions");
    const sessions = r.sessions || [];
    const prev = select.value;
    select.innerHTML = "";
    if (!sessions.length) {
      select.innerHTML = '<option value="">(no sessions)</option>';
    } else {
      for (const se of sessions) {
        const opt = document.createElement("option");
        opt.value = se.sessionKey;
        opt.textContent = (se.name || "?") + " — " + fmtRange(se.startsAt, se.endsAt);
        select.appendChild(opt);
      }
      if (prev && sessions.some((se) => se.sessionKey === prev)) select.value = prev;
    }
    select.dataset.loaded = "1";
  }
  await renderRoster();
}

async function renderRoster() {
  const body = document.getElementById("roster-body");
  const sessionKey = document.getElementById("roster-session").value;
  body.innerHTML = "";
  if (!sessionKey) {
    body.innerHTML = '<div class="empty">No session selected.</div>';
    return;
  }
  let bookings;
  try {
    const r = await api("/api/bookings?sessionKey=" + encodeURIComponent(sessionKey));
    bookings = r.bookings || [];
  } catch (e) {
    body.innerHTML = '<div class="empty">' + e.message + "</div>";
    return;
  }
  if (!bookings.length) {
    body.innerHTML = '<div class="empty">No one has booked this session yet.</div>';
    return;
  }
  body.innerHTML = '<div class="grid">' + bookings.map((b) => rosterCard(b, sessionKey)).join("") + "</div>";
  bookings.forEach((b) => {
    const btn = document.getElementById("cancel-" + domId(b.bookingKey));
    if (!btn) return;
    btn.addEventListener("click", async () => {
      btn.disabled = true;
      try {
        // forSession validation link is (a)-declared reads (require_matching_session,
        // ddls.go — absence means the caller named the wrong session).
        const forSessionLnk = "lnk.booking." + idOf(b.bookingKey) + ".forSession.session." + idOf(sessionKey);
        await opOrThrow(
          { operationType: "CancelBooking", class: "booking", reads: [b.bookingKey, b.bookingKey + ".status", forSessionLnk], payload: { bookingKey: b.bookingKey, session: sessionKey } },
          "cancel the booking"
        );
        toast("Booking cancelled.", true);
        setTimeout(renderRoster, 700);
      } catch (e) {
        toast(e.message, false);
        btn.disabled = false;
      }
    });
  });
}

function rosterCard(b, sessionKey) {
  const id = domId(b.bookingKey);
  return (
    '<div class="card">' +
    '<span class="badge ' + (b.rate === "resident" ? "posted" : "open") + '">' + (b.rate || "standard") + "</span>" +
    '<div class="who">' + shortKey(b.bookerKey) + "</div>" +
    '<div class="card-actions"><button id="cancel-' + id + '" class="danger">Cancel</button></div>' +
    "</div>"
  );
}

// ---- My Classes view ------------------------------------------------

async function loadMyClasses() {
  const select = document.getElementById("myclasses-resident");
  const label = document.getElementById("myclasses-resident-label");
  if (selfBookerKey) {
    select.hidden = true;
    label.hidden = true;
  } else {
    select.hidden = false;
    label.hidden = false;
    const residents = await loadResidents();
    fillResidentSelect(select, residents, true);
  }
  await renderMyClasses();
}

async function renderMyClasses() {
  const body = document.getElementById("myclasses-body");
  const bookerKey = selfBookerKey || document.getElementById("myclasses-resident").value;
  body.innerHTML = "";
  if (!bookerKey) {
    body.innerHTML = '<div class="empty">Pick a resident to see their booked classes.</div>';
    return;
  }
  let bookings;
  try {
    const r = await api("/api/bookings?bookerKey=" + encodeURIComponent(bookerKey));
    bookings = r.bookings || [];
  } catch (e) {
    body.innerHTML = '<div class="empty">' + e.message + "</div>";
    return;
  }
  if (!bookings.length) {
    body.innerHTML = '<div class="empty">No booked classes.</div>';
    return;
  }
  body.innerHTML = '<div class="grid">' + bookings.map(myClassCard).join("") + "</div>";
  bookings.forEach((b) => {
    const btn = document.getElementById("mycancel-" + domId(b.bookingKey));
    if (!btn) return;
    btn.addEventListener("click", async () => {
      btn.disabled = true;
      try {
        const forSessionLnk = "lnk.booking." + idOf(b.bookingKey) + ".forSession.session." + idOf(b.sessionKey);
        // asSelf's self-cancel guard needs the bookedBy link as a (d)-declared
        // optionalReads — the script checks it names THIS booker (ddls.go).
        const optionalReads = selfBookerKey
          ? ["lnk.booking." + idOf(b.bookingKey) + ".bookedBy.identity." + idOf(selfBookerKey)]
          : [];
        await opOrThrow(
          { operationType: "CancelBooking", class: "booking", reads: [b.bookingKey, b.bookingKey + ".status", forSessionLnk], optionalReads, payload: { bookingKey: b.bookingKey, session: b.sessionKey } },
          "cancel the booking",
          { asSelf: !!selfBookerKey }
        );
        toast("Booking cancelled.", true);
        setTimeout(renderMyClasses, 700);
      } catch (e) {
        toast(e.message, false);
        btn.disabled = false;
      }
    });
  });
}

function myClassCard(b) {
  const id = domId(b.bookingKey);
  return (
    '<div class="card">' +
    '<span class="badge ' + (b.rate === "resident" ? "posted" : "open") + '">' + (b.rate || "standard") + "</span>" +
    '<div class="who">' + (b.sessionName || "?") + "</div>" +
    '<div class="meta">' + fmtRange(b.startsAt, b.endsAt) + "</div>" +
    '<div class="card-actions"><button id="mycancel-' + id + '" class="danger">Cancel</button></div>' +
    "</div>"
  );
}

// ---- Studios (admin) view ------------------------------------------
//
// The operator surface for standing up the studios + classes the other three
// tabs consume — the wellness analog of LoftSpace's Landlord listing creation
// and Clinic's in-FE provider registration. CreateStudio/CreateSession are
// operator-scope (wellness-domain permissions.go), so this view submits with
// the default staff token, the same path Schedule/Roster already use.

// toUtcInstant canonicalizes a datetime-local field ("YYYY-MM-DDTHH:MM",
// grid-stepped) to the whole-second UTC RFC3339 instant CreateSession's grid
// expects. The wellness grid is UTC, and the Schedule view already displays
// session times as raw RFC3339 strings (fmtRange), so the entered wall-clock
// is stamped ":00Z" verbatim — never shifted by the browser's local zone.
function toUtcInstant(localValue) {
  if (!localValue) return "";
  return localValue.length === 16 ? localValue + ":00Z" : localValue + "Z";
}

// slotCellKeys mirrors sessionDDLScript's slot_cells + slot_cellcode
// (packages/wellness-domain/ddls.go): CreateSession reads-then-claims one
// studioSlotClaim aspect per covered 15-minute cell, so every covered cell is
// a class-(d) optionalReads the dispatcher must declare — the same "FE mirrors
// the Starlark read set" idiom seatKeys() uses for CreateBooking. Cells run
// [startsAt, endsAt) on the :00/:15/:30/:45 grid; the 96-cell cap matches the
// script's MAX_SLOT_CELLS (a longer span is rejected SessionTooLong server-side).
function slotCellKeys(studioKey, startsAt, endsAt) {
  const keys = [];
  const end = Date.parse(endsAt);
  let cur = Date.parse(startsAt);
  for (let i = 0; i < 96 && cur < end; i++) {
    const cc = new Date(cur).toISOString().slice(0, 19).replace(/[-:]/g, "").toLowerCase() + "z";
    keys.push(studioKey + ".slot" + cc);
    cur += 15 * 60 * 1000;
  }
  return keys;
}

async function loadStudiosAdmin() {
  await renderStudiosAdmin();
}

async function renderStudiosAdmin() {
  const grid = document.getElementById("studios-grid");
  const summary = document.getElementById("studios-summary");
  grid.innerHTML = "";
  summary.textContent = "";
  let studios;
  try {
    const r = await api("/api/studios");
    studios = r.studios || [];
  } catch (e) {
    grid.innerHTML = '<div class="empty">' + e.message + "</div>";
    return;
  }
  summary.textContent = studios.length + " studio" + (studios.length === 1 ? "" : "s");
  if (!studios.length) {
    grid.innerHTML = '<div class="empty">No studios yet — create one above.</div>';
    return;
  }
  grid.innerHTML = studios.map(studioCard).join("");
  studios.forEach(wireStudioCard);
}

function studioCard(s) {
  const id = domId(s.studioKey);
  return (
    '<div class="card">' +
    '<div class="who">' + (s.name || "?") + "</div>" +
    '<div class="meta">' + shortKey(s.studioKey) + "</div>" +
    '<div class="card-actions"><button id="sess-toggle-' + id + '" class="ghost">Schedule a class</button></div>' +
    '<div id="sess-form-' + id + '" class="session-form" hidden>' +
    '<div class="field"><label>Class name</label><input type="text" id="sess-name-' + id + '" placeholder="e.g. Vinyasa Flow" maxlength="120" /></div>' +
    '<div class="field"><label>Starts</label><input type="datetime-local" id="sess-starts-' + id + '" step="900" /></div>' +
    '<div class="field"><label>Ends</label><input type="datetime-local" id="sess-ends-' + id + '" step="900" /></div>' +
    '<div class="field"><label>Capacity</label><input type="number" id="sess-cap-' + id + '" min="1" max="200" value="20" /></div>' +
    '<button id="sess-create-' + id + '">Schedule class</button>' +
    "</div>" +
    "</div>"
  );
}

function wireStudioCard(s) {
  const id = domId(s.studioKey);
  const form = document.getElementById("sess-form-" + id);
  document.getElementById("sess-toggle-" + id).addEventListener("click", () => {
    form.hidden = !form.hidden;
  });
  document.getElementById("sess-create-" + id).addEventListener("click", () => {
    createSession(s.studioKey, {
      name: document.getElementById("sess-name-" + id),
      starts: document.getElementById("sess-starts-" + id),
      ends: document.getElementById("sess-ends-" + id),
      capacity: document.getElementById("sess-cap-" + id),
      submit: document.getElementById("sess-create-" + id),
    });
  });
}

async function createStudio() {
  const input = document.getElementById("studio-name");
  const name = input.value.trim();
  if (!name) { toast("Enter a studio name.", false); return; }
  const btn = document.getElementById("studio-create");
  btn.disabled = true;
  try {
    await opOrThrow(
      { operationType: "CreateStudio", class: "studio", reads: [], payload: { name } },
      "create the studio"
    );
    toast("Studio created.", true);
    input.value = "";
    studiosCache = null;
    // The Schedule tab's studio picker also lists studios — force it to
    // re-pull next time it loads so the new studio appears there too.
    document.getElementById("schedule-studio").dataset.loaded = "";
    setTimeout(renderStudiosAdmin, 700);
  } catch (e) {
    toast(e.message, false);
  } finally {
    btn.disabled = false;
  }
}

async function createSession(studioKey, els) {
  const name = els.name.value.trim();
  const startsAt = toUtcInstant(els.starts.value);
  const endsAt = toUtcInstant(els.ends.value);
  const capacity = parseInt(els.capacity.value, 10);
  if (!name) { toast("Enter a class name.", false); return; }
  if (!startsAt || !endsAt) { toast("Pick a start and end time.", false); return; }
  if (!(capacity >= 1 && capacity <= 200)) { toast("Capacity must be 1–200.", false); return; }
  if (!(Date.parse(startsAt) < Date.parse(endsAt))) { toast("End time must be after start time.", false); return; }
  els.submit.disabled = true;
  try {
    await opOrThrow(
      {
        operationType: "CreateSession",
        class: "session",
        reads: [studioKey],
        optionalReads: slotCellKeys(studioKey, startsAt, endsAt),
        payload: { studio: studioKey, name, startsAt, endsAt, capacity },
      },
      "schedule the class"
    );
    toast("Class scheduled.", true);
    els.name.value = "";
    setTimeout(renderStudiosAdmin, 700);
  } catch (e) {
    toast(e.message, false);
  } finally {
    els.submit.disabled = false;
  }
}

// ---- Me bar ---------------------------------------------------------

// refreshCurrentView re-renders whichever tab is active — called after
// signing in/out so the Schedule and My Classes views pick up the new
// self-service mode without a full page reload.
function refreshCurrentView() {
  const active = document.querySelector(".tab.active");
  if (active) showView(active.dataset.view);
}

async function initMeBar() {
  const status = document.getElementById("me-status");
  const select = document.getElementById("me-resident");
  const signinBtn = document.getElementById("me-signin");
  const signoutBtn = document.getElementById("me-signout");

  function refreshMeUI() {
    if (selfBookerKey) {
      status.textContent = "Signed in as " + shortKey(selfBookerKey);
      select.hidden = true;
      signinBtn.hidden = true;
      signoutBtn.hidden = false;
    } else {
      status.textContent = "Not signed in";
      select.hidden = false;
      signinBtn.hidden = false;
      signoutBtn.hidden = true;
    }
  }

  const residents = await loadResidents();
  fillResidentSelect(select, residents, true);
  refreshMeUI();

  signinBtn.addEventListener("click", () => {
    if (!select.value) { toast("Pick a resident first.", false); return; }
    signInAsSelf(select.value);
    refreshMeUI();
    refreshCurrentView();
  });
  signoutBtn.addEventListener("click", () => {
    signOutSelf();
    refreshMeUI();
    refreshCurrentView();
  });
}

// ---- init --------------------------------------------------------

function init() {
  document.querySelectorAll(".tab").forEach((b) => {
    b.addEventListener("click", () => showView(b.dataset.view));
  });
  document.getElementById("schedule-studio").addEventListener("change", renderSchedule);
  document.getElementById("schedule-refresh").addEventListener("click", () => {
    studiosCache = null; residentsCache = null;
    document.getElementById("schedule-studio").dataset.loaded = "";
    loadSchedule();
  });
  document.getElementById("roster-session").addEventListener("change", renderRoster);
  document.getElementById("roster-refresh").addEventListener("click", () => {
    document.getElementById("roster-session").dataset.loaded = "";
    loadRoster();
  });
  document.getElementById("myclasses-resident").addEventListener("change", renderMyClasses);
  document.getElementById("myclasses-refresh").addEventListener("click", () => { residentsCache = null; loadMyClasses(); });
  document.getElementById("studio-create").addEventListener("click", createStudio);
  document.getElementById("studios-refresh").addEventListener("click", () => { studiosCache = null; renderStudiosAdmin(); });
  initMeBar();
  loadSchedule();
}

init();
