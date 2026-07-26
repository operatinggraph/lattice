"use strict";

// Wellness app — Schedule · My Classes · Roster · Studios. Vanilla JS, no
// build step. The page is sign-in-first: one identity holds the whole
// session, the HttpOnly session cookie authenticates every same-origin read,
// and writes (CreateBooking / CancelBooking / CreateSession) go
// browser-direct to the Gateway's POST /v1/operations via submitOp() with
// that same session's bearer (real-actor-write-auth-e2e-design.md §3.1).
//
// The Go server does the NATS I/O behind the read endpoints and owns the
// boundary over them (persona-worlds-design.md Fire W3 §3): the class
// SCHEDULE is public-read, while My Classes is scoped server-side to the
// session's own subject and a Roster is served only to a `worksAt` staffer or
// to the instructor who leads that session. The hat gating below is UX
// curation over that boundary, never the boundary itself.

const state = {
  identityId: null, // the signed-in identity's bare NanoID (GET /api/whoami) — the one actor every read and write runs as
  canSignOut: false, // whether whoami reports a real cookie session
  anchors: [], // the signed-in identity's anchors (whoami hat hints, persona-worlds-design.md §4): `worksAt` marks studio staff, an `identifiedBy` vtx.instructor binding marks an instructor
};

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

// isAuthLapse reports whether a failed request failed because the caller has
// no valid session, as opposed to any other error.
function isAuthLapse(e) {
  return /HTTP 401|no signed-in identity|login required/i.test((e && e.message) || "");
}

// onSessionLapsed hands the browser back to the login page. Once only: several
// panels can load in parallel and would otherwise each fire their own navigation.
let sessionLapseHandled = false;
function onSessionLapsed() {
  if (sessionLapseHandled) return;
  sessionLapseHandled = true;
  location.replace("/login");
}

// appGet reads one of this app's own session-gated endpoints. The session
// cookie is HttpOnly and rides a same-origin request automatically; a 401
// means the session itself is over, and the only answer is to sign in again.
async function appGet(path) {
  try {
    return await api(path, { credentials: "same-origin" });
  } catch (e) {
    if (isAuthLapse(e)) onSessionLapsed();
    throw e;
  }
}

let gatewayURLCache = null;
async function gatewayURL() {
  if (gatewayURLCache) return gatewayURLCache;
  const body = await appGet("/api/config");
  gatewayURLCache = body.gatewayUrl;
  return gatewayURLCache;
}

// sessionWriteToken is the raw bearer the Gateway-direct write path needs. The
// cookie cannot serve it — an Authorization header takes the literal value and
// the cookie is unreadable from script — so POST /api/session/refresh hands the
// token back (while re-setting the cookie) for exactly this. Cached until
// shortly before its stated expiry; pass force to re-fetch after the Gateway
// rejects the cached one. There is no separate staff/self token cache — the
// signed-in session is the one actor every write submits as.
let writeTokenCache = { token: null, exp: 0 };

async function sessionWriteToken(force) {
  const now = Date.now();
  if (!force && writeTokenCache.token && now < writeTokenCache.exp - 5000) {
    return writeTokenCache.token;
  }
  const res = await fetch("/api/session/refresh", { method: "POST", credentials: "same-origin" });
  if (res.status === 401) {
    writeTokenCache = { token: null, exp: 0 };
    onSessionLapsed();
    throw new Error("your session has ended — sign in again");
  }
  if (!res.ok) {
    throw new Error("could not renew the session (HTTP " + res.status + ")");
  }
  const body = await res.json();
  writeTokenCache = { token: body.token, exp: Date.parse(body.expiresAt) || now + 5 * 60000 };
  return body.token;
}

// identityKey is the signed-in session's own full vertex key — the
// authContext.target a member's self-scoped write is checked against by
// wellness-domain's `consumer` scope=self grant
// (packages/wellness-domain/permissions.go).
//
// Throws rather than composing a key when whoami never answered. Reads keep
// working off the session cookie in that state, so the page looks signed in
// while state.identityId is null; a composed "vtx.identity.null" would reach
// the Gateway and come back as an opaque scope=self rejection. Failing here
// names the real problem instead.
function identityKey() {
  if (!state.identityId) {
    throw new Error("we could not confirm who you are signed in as — reload the page and try again");
  }
  return "vtx.identity." + state.identityId;
}

// submitOp posts one operation to the Gateway, browser-direct, under the
// signed-in session's own token. selfScoped marks a submit made under the
// MEMBER hat, and only then is authContext.target attached.
//
// A staff or instructor submit must NOT carry a target, even its own.
// wellness-domain's scripts branch on the mere PRESENCE of authContextTarget,
// not on whether the platform validated it: CreateBooking reads presence as
// "self-service — the booker must BE the target", and CancelBooking then
// requires the target to be the booking's own bookedBy identity
// (packages/wellness-domain/ddls.go). A staffer acting on a member's seat is
// not that member, so an unconditional target would deny it. The Processor
// passes the field through verbatim from the envelope
// (internal/processor/starlark_runner.go), so which grant authorized the op
// does not change what the script sees.
async function submitOp(body, selfScoped) {
  const [base, token] = await Promise.all([gatewayURL(), sessionWriteToken()]);
  const withAuth = selfScoped
    ? Object.assign({}, body, { authContext: { target: identityKey() } })
    : body;
  const post = (bearer) =>
    api(base + "/v1/operations", {
      method: "POST",
      headers: { "Content-Type": "application/json", Authorization: "Bearer " + bearer },
      body: JSON.stringify(withAuth),
    });
  try {
    return await post(token);
  } catch (e) {
    if (!isAuthLapse(e)) throw e;
    // The cached token aged out between the expiry check and this request;
    // one forced renewal settles it, and a second 401 is a session that is
    // genuinely over (sessionWriteToken hands off to /login).
    return post(await sessionWriteToken(true));
  }
}

async function opOrThrow(body, what, selfScoped) {
  const reply = await submitOp(body, selfScoped);
  if (reply && reply.status === "rejected") {
    const msg = reply.error ? `${reply.error.code}: ${reply.error.message}` : "rejected";
    throw new Error(`Could not ${what} — ${msg}`);
  }
  return reply || {};
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

// ---- session (whoami + hat gating) ---------------------------------

// whoamiRetryBackoffsMs bounds loadWhoami's retry: a transient failure at
// first paint must not permanently render a real cookie session as anonymous.
const whoamiRetryBackoffsMs = [200, 500, 1200];

// loadWhoami records who is signed in — the single actor every read and
// write runs as — before the first render that reads it.
async function loadWhoami() {
  for (let attempt = 0; ; attempt++) {
    try {
      const body = await api("/api/whoami", { credentials: "same-origin" });
      state.identityId = (body && body.loggedIn && body.identityId) || null;
      state.canSignOut = !!(body && body.canSignOut);
      state.anchors = (body && Array.isArray(body.anchors) && body.anchors) || [];
      return;
    } catch (_) {
      if (attempt >= whoamiRetryBackoffsMs.length) {
        state.identityId = null;
        state.canSignOut = false;
        state.anchors = [];
        return;
      }
      await new Promise((resolve) => setTimeout(resolve, whoamiRetryBackoffsMs[attempt]));
    }
  }
}

// anchorKey returns the key of the first anchor matching relation (and, when
// given, the vertex-type prefix), or "". The KEY must be present, not just
// the relation: identityAnchors stamps `relation` as a literal constant on
// every collected entry, so an identity with no such binding still yields a
// {key:null, relation:"..."} entry from the unmatched OPTIONAL MATCH
// (packages/identity-domain/lenses.go).
function anchorKey(relation, keyPrefix) {
  if (!Array.isArray(state.anchors)) return "";
  for (const a of state.anchors) {
    if (!a || a.relation !== relation) continue;
    const key = (a.key || "").trim();
    if (!key) continue;
    if (keyPrefix && key.indexOf(keyPrefix) !== 0) continue;
    return key;
  }
  return "";
}

// isStaff marks a session that works a studio. The signal is the whoami
// `worksAt` anchor (persona-worlds-design.md §4); gating rides anchors, not
// roles — whoami's roles[] arrives as opaque role vertex keys, never
// canonical names, so it cannot name the frontOfHouse role FE-side.
function isStaff() {
  return anchorKey("worksAt") !== "";
}

// instructorKey is the vtx.instructor entity this login is bound to, or "".
// A clinic provider and a wellness instructor are both `identifiedBy`
// bindings, so the vertex TYPE is what distinguishes them on a multi-hat
// human — the same test the server's own read boundary applies.
function instructorKey() {
  return anchorKey("identifiedBy", "vtx.instructor.");
}

// hatLabel names the hats this session holds, for the me-bar. One human can
// hold several at once (persona-worlds-design.md §3.4), so they are listed
// rather than resolved to a single winner.
function hatLabel() {
  const hats = [];
  if (isStaff()) hats.push("staff");
  if (instructorKey()) hats.push("instructor");
  hats.push("member");
  return hats.join(" · ");
}

function refreshMeBar() {
  const status = document.getElementById("me-status");
  const signOutBtn = document.getElementById("sign-out");
  status.textContent = state.identityId
    ? "Signed in as " + shortKey(state.identityId) + " (" + hatLabel() + ")"
    : "";
  signOutBtn.hidden = !state.canSignOut;
}

function signOut() {
  fetch("/api/logout", { method: "POST", credentials: "same-origin" })
    .catch(() => {})
    .finally(() => location.replace("/login"));
}

// applyHatGating hides the tabs a session has no hat for — Roster needs staff
// or an instructor binding, Studios needs staff (CreateSession grants
// frontOfHouse, packages/wellness-domain/permissions.go) — and bounces the
// active view if it just became disallowed. Idempotent; re-run whenever
// whoami resolves.
function applyHatGating() {
  document.getElementById("tab-roster").hidden = !(isStaff() || instructorKey());
  document.getElementById("tab-studios").hidden = !isStaff();
  refreshMeBar();
  const active = document.querySelector(".tab.active");
  if (active && active.hidden) showView("schedule");
}

// ---- formatting --------------------------------------------------------

// esc renders an untrusted string as text inside the innerHTML templates
// below. Class, studio and instructor names are operator/staff-entered free
// text (CreateStudio/CreateSession take a required_string with no charset
// restriction), and the schedule that carries them is public-read — so an
// unescaped name would be stored XSS in this app's own origin, where
// /api/session/refresh hands out the caller's raw Gateway bearer.
function esc(s) {
  const d = document.createElement("div");
  d.textContent = s == null ? "" : String(s);
  return d.innerHTML;
}

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
  if (view === "roster" && !(isStaff() || instructorKey())) view = "schedule";
  if (view === "studios" && !isStaff()) view = "schedule";
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

// ---- shared data ------------------------------------------------

let studiosCache = null;
async function loadStudios() {
  if (studiosCache) return studiosCache;
  const body = await appGet("/api/studios");
  studiosCache = body.studios || [];
  return studiosCache;
}

let sessionsCache = null;
async function loadSessions(force) {
  if (sessionsCache && !force) return sessionsCache;
  const body = await appGet("/api/sessions");
  sessionsCache = body.sessions || [];
  return sessionsCache;
}

let instructorsCache = null;
async function loadInstructors() {
  if (instructorsCache) return instructorsCache;
  const body = await appGet("/api/instructors");
  instructorsCache = body.instructors || [];
  return instructorsCache;
}

// ownLeaseAppKey is the signed-in member's own lease, CreateBooking's
// optional resident-rate hint. The server answers only for the caller
// (GET /api/my-residency), so there is nobody else's to pick. An approved
// lease wins over a merely-applied one; absence just means the standard rate,
// which is why a failed lookup is cached as "none" rather than thrown.
let residencyCache = null;
async function ownLeaseAppKey() {
  if (residencyCache === null) {
    // A FAILED lookup is deliberately not cached: absence just means the
    // standard rate, so a transient error must not silently cost the member
    // their resident rate for the life of the page. The next book retries.
    try {
      const body = await appGet("/api/my-residency");
      residencyCache = (body && body.leases) || [];
    } catch (_) {
      return "";
    }
  }
  const chosen = residencyCache.find((l) => l.approved) || residencyCache[0];
  return chosen ? chosen.leaseAppKey : "";
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
    sessions = await loadSessions(true);
  } catch (e) {
    grid.innerHTML = '<div class="empty">' + esc(e.message) + "</div>";
    return;
  }
  if (studioKey) sessions = sessions.filter((se) => se.studioKey === studioKey);
  summary.textContent = sessions.length + " session" + (sessions.length === 1 ? "" : "s");
  if (!sessions.length) {
    grid.innerHTML = '<div class="empty">No upcoming sessions.</div>';
    return;
  }
  const leaseAppKey = await ownLeaseAppKey();
  grid.innerHTML = sessions.map(scheduleCard).join("");
  sessions.forEach((se) => {
    const bookBtn = document.getElementById("book-" + domId(se.sessionKey));
    if (!bookBtn) return;
    bookBtn.addEventListener("click", async () => {
      bookBtn.disabled = true;
      try {
        // A member books for THEMSELVES — the booker is the signed-in
        // identity, and nothing in the page can name anyone else.
        const bookerKey = identityKey();
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
        // — CreateBooking fails UnknownEndpoint without it.
        await opOrThrow(
          { operationType: "CreateBooking", class: "booking", reads: [se.sessionKey, se.sessionKey + ".schedule", bookerKey], optionalReads, payload },
          "book the class",
          true,
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

function scheduleCard(se) {
  const id = domId(se.sessionKey);
  const full = se.bookedCount >= se.capacity;
  const led = se.instructorName ? '<div class="meta">with ' + esc(se.instructorName) + "</div>" : "";
  return (
    '<div class="card">' +
    '<span class="badge ' + (full ? "settled" : "open") + '">' + se.bookedCount + " / " + se.capacity + " seats</span>" +
    '<div class="who">' + esc(se.name || "?") + "</div>" +
    '<div class="meta">' + esc(se.studioName || shortKey(se.studioKey)) + "</div>" +
    led +
    '<div class="meta">' + esc(fmtRange(se.startsAt, se.endsAt)) + "</div>" +
    '<div class="field-row">' +
    '<button id="book-' + id + '"' + (full ? " disabled" : "") + ">Book</button>" +
    "</div>" +
    "</div>"
  );
}

// ---- My Classes view ------------------------------------------------
//
// The signed-in member's own bookings. GET /api/bookings with no sessionKey
// answers for the session's own subject and nobody else — there is no booker
// parameter to send, and no picker to send one from.

async function loadMyClasses() {
  await renderMyClasses();
}

async function renderMyClasses() {
  const body = document.getElementById("myclasses-body");
  const summary = document.getElementById("myclasses-summary");
  body.innerHTML = "";
  summary.textContent = "";
  let bookings;
  try {
    const r = await appGet("/api/bookings");
    bookings = r.bookings || [];
  } catch (e) {
    body.innerHTML = '<div class="empty">' + esc(e.message) + "</div>";
    return;
  }
  summary.textContent = bookings.length + " class" + (bookings.length === 1 ? "" : "es");
  if (!bookings.length) {
    body.innerHTML = '<div class="empty">No booked classes — book one from the Schedule.</div>';
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
        // The self-cancel guard needs the bookedBy link as a (d)-declared
        // optionalReads — the script checks it names THIS booker (ddls.go).
        const optionalReads = ["lnk.booking." + idOf(b.bookingKey) + ".bookedBy.identity." + idOf(identityKey())];
        await opOrThrow(
          { operationType: "CancelBooking", class: "booking", reads: [b.bookingKey, b.bookingKey + ".status", forSessionLnk], optionalReads, payload: { bookingKey: b.bookingKey, session: b.sessionKey } },
          "cancel the booking",
          true,
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
    '<span class="badge ' + (b.rate === "resident" ? "posted" : "open") + '">' + esc(b.rate || "standard") + "</span>" +
    '<div class="who">' + esc(b.sessionName || "?") + "</div>" +
    '<div class="meta">' + esc(fmtRange(b.startsAt, b.endsAt)) + "</div>" +
    '<div class="card-actions"><button id="mycancel-' + id + '" class="danger">Cancel</button></div>' +
    "</div>"
  );
}

// ---- Roster view --------------------------------------------------------
//
// Who is booked into one session. The server serves it to a `worksAt`
// staffer for sessions at a location they work at, and to an instructor only
// for the sessions their own binding leads (persona-worlds-design.md §7.3), so
// the picker below offers an instructor exactly their own classes. It offers a
// staffer every session on the schedule, so one at another building answers
// 403 rather than being filtered out of the picker — the schedule is the open
// member catalog and carries no location column to narrow by.
//
// Nobody cancels a seat from here: CancelBooking scope=any grants `operator`
// alone (packages/wellness-domain/permissions.go), so neither hat may cancel
// somebody else's seat — a member cancels their own from My Classes.
//
// What the roster CAN write is attendance. SetBookingAttendance grants
// `provider` scope=any, confined in-script to a booking on a session the
// caller both leads (ledBy) and is identifiedBy-bound to, so the bound
// instructor marks who showed — the "attendance" half of §7.3's instructor
// hat. Staff hold no such grant, so the controls appear for the bound
// instructor alone, and only once the class has started.
//
// The CLASS itself is different. TombstoneSession grants `provider`
// scope=any, confined in-script to a session the caller both leads (ledBy)
// and is identifiedBy-bound to, so a bound instructor may call off their own
// class — the "cancel-own-session" half of §7.3's instructor hat. Staff hold
// no such grant, so the control appears for the bound instructor alone.

async function loadRoster() {
  const select = document.getElementById("roster-session");
  if (!select.dataset.loaded) {
    let sessions;
    try {
      sessions = await loadSessions();
    } catch (e) {
      select.innerHTML = '<option value="">(' + esc(e.message) + ")</option>";
      return;
    }
    // An instructor sees their own classes; a staffer sees the house.
    const mine = instructorKey();
    if (!isStaff() && mine) sessions = sessions.filter((se) => se.instructorKey === mine);
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
  const summary = document.getElementById("roster-summary");
  const sessionKey = document.getElementById("roster-session").value;
  body.innerHTML = "";
  summary.textContent = "";
  if (!sessionKey) {
    body.innerHTML = '<div class="empty">No session selected.</div>';
    return;
  }
  let bookings;
  try {
    const r = await appGet("/api/bookings?sessionKey=" + encodeURIComponent(sessionKey));
    bookings = r.bookings || [];
  } catch (e) {
    body.innerHTML = '<div class="empty">' + esc(e.message) + "</div>";
    return;
  }
  // Marking attendance is the bound instructor's own beat, and only once the
  // class has begun — SetBookingAttendance answers SessionNotStarted before
  // that, so the control would only fail closed. The server re-derives both
  // facts and the Starlark guard re-derives them again at submit; this is the
  // affordance, not the authority.
  const se = (sessionsCache || []).find((x) => x.sessionKey === sessionKey);
  const mine = instructorKey();
  const isLeader = !!(se && mine && se.instructorKey === mine);
  const started = !!(se && se.startsAt && new Date(se.startsAt).getTime() <= Date.now());

  summary.textContent = bookings.length + " booked";
  body.innerHTML = bookings.length
    ? '<div class="grid">' + bookings.map((b) => rosterCard(b, isLeader && started, isStaff())).join("") + "</div>"
    : '<div class="empty">No one has booked this session yet.</div>';
  if (isLeader && !started && bookings.length) {
    summary.textContent += " — attendance opens when the class starts";
  }
  if (isLeader && started) bindAttendance(sessionKey, mine);
  if (isStaff() && bookings.length) bindSeatCancels(sessionKey);
  renderCancelClass(sessionKey);
}

// bindSeatCancels wires the front desk's release-a-seat control. CancelBooking
// grants frontOfHouse at scope=any and the script confines it to a session
// whose studio sits at a location the caller worksAt — so an instructor, who
// holds no such grant, never sees the control even on their own roster.
function bindSeatCancels(sessionKey) {
  const buttons = document.querySelectorAll("#roster-body [data-cancel-seat]");
  buttons.forEach((btn) => {
    btn.addEventListener("click", async () => {
      buttons.forEach((b) => { b.disabled = true; });
      try {
        await cancelSeat(btn.dataset.cancelSeat, sessionKey);
        toast("Seat released.", true);
        setTimeout(renderRoster, 700);
      } catch (e) {
        toast(e.message, false);
        buttons.forEach((b) => { b.disabled = false; });
      }
    });
  });
}

// cancelSeat submits CancelBooking for a member's seat as the front desk. It
// carries NO authContext.target — that field is the CONSUMER path's binding
// (the booking's own bookedBy identity), and a staffer releasing someone
// else's seat is never that identity; the workplace walk binds them instead.
async function cancelSeat(bookingKey, sessionKey) {
  await opOrThrow(
    {
      operationType: "CancelBooking",
      class: "booking",
      // The booking's own .status is an (a)-declared REQUIRED read: the script
      // rebuilds the seat cell it releases from the seat index it carries.
      reads: [bookingKey, bookingKey + ".status"],
      // The session-match probe. An absent link is a meaningful WrongSession
      // rejection, not a correctness error — and it must answer BEFORE the
      // workplace guard, since the session this names is what carries the
      // confinement (packages/wellness-domain/ddls.go).
      optionalReads: [
        "lnk.booking." + idOf(bookingKey) + ".forSession.session." + idOf(sessionKey),
      ],
      payload: { bookingKey, session: sessionKey },
    },
    "release the seat",
    false,
  );
}

// bindAttendance wires each card's Attended / No-show control. The whole set
// is disabled while one submit is in flight, then the roster is re-read so the
// badges reflect what actually committed rather than what was clicked.
function bindAttendance(sessionKey, mine) {
  const buttons = document.querySelectorAll("#roster-body [data-attend]");
  buttons.forEach((btn) => {
    btn.addEventListener("click", async () => {
      buttons.forEach((b) => (b.disabled = true));
      try {
        await markAttendance(btn.dataset.attend, sessionKey, btn.dataset.value, mine);
        toast("Attendance recorded.", true);
        await awaitProjectedStatus(sessionKey, btn.dataset.attend, btn.dataset.value);
        await renderRoster();
      } catch (e) {
        toast(e.message, false);
        buttons.forEach((b) => (b.disabled = b.dataset.wasDisabled === "1"));
      }
    });
    btn.dataset.wasDisabled = btn.disabled ? "1" : "0";
  });
}

// ATTENDANCE_PROJECTION_POLL_MS bounds how long the roster waits for the mark
// it just committed to appear in the read model. The write commits to Core KV,
// but the roster renders from the wellnessBookings LENS, and a lens reprojects
// a beat later — so an immediate re-read returns the pre-mark row and the click
// looks like it did nothing. Polling until the projection catches up (or the
// bound expires, after which whatever the lens says is what renders) keeps the
// affordance honest without ever showing a state the read model has not
// actually reached.
const ATTENDANCE_PROJECTION_POLL_MS = [250, 400, 600, 900, 1200, 1500];

async function awaitProjectedStatus(sessionKey, bookingKey, want) {
  for (const wait of ATTENDANCE_PROJECTION_POLL_MS) {
    await new Promise((r) => setTimeout(r, wait));
    try {
      const r = await appGet("/api/bookings?sessionKey=" + encodeURIComponent(sessionKey));
      const row = (r.bookings || []).find((b) => b.bookingKey === bookingKey);
      if (row && row.status === want) return;
    } catch (e) {
      return; // renderRoster surfaces the read failure on its own.
    }
  }
}

// markAttendance submits SetBookingAttendance for a booking on a class this
// instructor leads. It carries NO authContext.target — the grant is scope=any
// and the script confines it by the three links below, not by a caller-supplied
// target (packages/wellness-domain/ddls.go).
async function markAttendance(bookingKey, sessionKey, value, mine) {
  const bookId = idOf(bookingKey);
  const sessId = idOf(sessionKey);
  await opOrThrow(
    {
      operationType: "SetBookingAttendance",
      class: "booking",
      // The booking's own .status is required — the script carries its rate /
      // seat / booker forward onto this write, so its absence is a correctness
      // error, not a rejection. The session's .schedule is required for the
      // same reason: its startsAt is what answers SessionNotStarted. The
      // session-match and the two ownership probes are (d)-declared — an absent
      // link is a meaningful rejection, not a correctness error.
      reads: [bookingKey, bookingKey + ".status", sessionKey + ".schedule"],
      optionalReads: [
        "lnk.booking." + bookId + ".forSession.session." + sessId,
        "lnk.session." + sessId + ".ledBy.instructor." + idOf(mine),
        "lnk.instructor." + idOf(mine) + ".identifiedBy.identity." + idOf(identityKey()),
      ],
      payload: { bookingKey: bookingKey, session: sessionKey, status: value, instructor: mine },
    },
    "record attendance",
    false,
  );
}

// renderCancelClass appends the instructor's own "Call off this class"
// control, for a session THIS login is the bound instructor of. The server
// re-derives the same fact for the roster read, and the Starlark guard
// re-derives it again at submit — this is the affordance, not the authority.
function renderCancelClass(sessionKey) {
  const mine = instructorKey();
  if (!mine) return;
  const se = (sessionsCache || []).find((x) => x.sessionKey === sessionKey);
  if (!se || se.instructorKey !== mine) return;

  const wrap = document.createElement("div");
  wrap.className = "card-actions";
  wrap.innerHTML = '<button id="cancel-class" class="danger">Call off this class</button>';
  document.getElementById("roster-body").appendChild(wrap);
  document.getElementById("cancel-class").addEventListener("click", async () => {
    const btn = document.getElementById("cancel-class");
    btn.disabled = true;
    try {
      await cancelOwnClass(se, mine);
      toast("Class called off.", true);
      sessionsCache = null;
      document.getElementById("roster-session").dataset.loaded = "";
      setTimeout(loadRoster, 700);
    } catch (e) {
      toast(e.message, false);
      btn.disabled = false;
    }
  });
}

// cancelOwnClass submits TombstoneSession for a class this instructor leads.
// It carries NO authContext.target — the grant is scope=any and the script
// confines it by the two ownership links below, not by a caller-supplied
// target (packages/wellness-domain/ddls.go's TombstoneSession).
async function cancelOwnClass(se, mine) {
  const sessId = idOf(se.sessionKey);
  await opOrThrow(
    {
      operationType: "TombstoneSession",
      class: "session",
      // The session and its schedule are (a)-declared required reads — the
      // schedule is what the script releases the studio's held slot cells
      // from. The atStudio link proves the named studio is genuinely this
      // session's, and the two ownership probes are (d)-declared: an absent
      // link is a meaningful AuthDenied, not a correctness error.
      reads: [se.sessionKey, se.sessionKey + ".schedule"],
      optionalReads: [
        "lnk.session." + sessId + ".atStudio.studio." + idOf(se.studioKey),
        "lnk.session." + sessId + ".ledBy.instructor." + idOf(mine),
        "lnk.instructor." + idOf(mine) + ".identifiedBy.identity." + idOf(identityKey()),
      ],
      payload: { sessionKey: se.sessionKey, studio: se.studioKey, instructor: mine },
    },
    "call off the class",
    false,
  );
}

// ATTENDANCE_MARKS maps a booking's committed status to how the roster shows
// it. `booked` carries no badge — not-yet-marked is the resting state, and a
// badge on every card would drown the ones that say something.
const ATTENDANCE_MARKS = {
  attended: { badge: "posted", label: "attended" },
  noShow: { badge: "settled", label: "no-show" },
};

function rosterCard(b, markable, cancellable) {
  const mark = ATTENDANCE_MARKS[b.status];
  return (
    '<div class="card">' +
    '<span class="badge ' + (b.rate === "resident" ? "posted" : "open") + '">' + esc(b.rate || "standard") + "</span>" +
    (mark ? '<span class="badge ' + mark.badge + '">' + mark.label + "</span>" : "") +
    '<div class="who">' + esc(shortKey(b.bookerKey)) + "</div>" +
    (markable ? attendanceActions(b) : "") +
    (cancellable ? seatCancelAction(b) : "") +
    "</div>"
  );
}

// seatCancelAction is the front desk's per-seat release. It is its own row
// rather than a third button beside Attended / No-show: those two correct each
// other and this one removes the booking they are about, so pairing them would
// put a destructive control one mis-click from a corrective one.
function seatCancelAction(b) {
  return '<div class="card-actions"><button class="danger" data-cancel-seat="' +
    esc(b.bookingKey) + '">Release seat</button></div>';
}

// attendanceActions renders the two marks as a pair, with the one already
// recorded disabled — either value corrects the other, so the control the
// instructor needs is always the OTHER one.
function attendanceActions(b) {
  const btn = (value, label) =>
    '<button class="ghost" data-attend="' + esc(b.bookingKey) + '" data-value="' + value + '"' +
    (b.status === value ? " disabled" : "") + ">" + label + "</button>";
  return '<div class="card-actions">' + btn("attended", "Attended") + btn("noShow", "No-show") + "</div>";
}

// ---- Studios (staff) view ------------------------------------------
//
// The staff surface for opening a studio and scheduling classes into it.
// CreateStudio and CreateSession both grant `frontOfHouse` and both confine it
// in-script to a location the caller `worksAt` (packages/wellness-domain/
// ddls.go) — for the new studio, the location it is about to sit at; for a
// class, the location its studio already sits at.
//
// The new-studio form asks only for a NAME. The location is not the staffer's
// to choose: the script guards on the location the studio will be linked to,
// and the only one a staffer can name and pass is the building they work at,
// so the form fills it from the `worksAt` anchor and says where it is going
// rather than offering a picker whose every other option would fail closed.
// (The same call the CreateStudio op-meta makes for descriptor-driven clients,
// where the field is the `{me.workplace}` self-anchor.)

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
  renderNewStudioForm();
  await renderStudiosAdmin();
}

// renderNewStudioForm shows where the new studio will be opened, or hides the
// affordance entirely when this session has no workplace to open one at — the
// client-side mirror of the script's own denial, where an empty candidate list
// confines everyone but an operator.
function renderNewStudioForm() {
  const toggle = document.getElementById("studio-new-toggle");
  const form = document.getElementById("studio-new-form");
  const where = document.getElementById("studio-new-where");
  const workplace = anchorKey("worksAt");
  toggle.hidden = !workplace;
  if (!workplace) {
    form.hidden = true;
    return;
  }
  where.textContent = "Opens at " + shortKey(workplace) + " — where you work.";
}

async function createStudio() {
  const nameEl = document.getElementById("studio-new-name");
  const submit = document.getElementById("studio-new-create");
  const name = nameEl.value.trim();
  const workplace = anchorKey("worksAt");
  if (!name) { toast("Enter a studio name.", false); return; }
  if (!workplace) { toast("You have no workplace to open a studio at.", false); return; }
  submit.disabled = true;
  try {
    // A staff submit carries NO authContext.target: CreateStudio's
    // frontOfHouse grant is scope=any, confined in-script by the caller's own
    // worksAt walk rather than by a caller-supplied target.
    //
    // The location is an (a)-declared REQUIRED read — the script validates it
    // alive + typed (require_live_typed) before linking the studio to it.
    await opOrThrow(
      {
        operationType: "CreateStudio",
        class: "studio",
        reads: [workplace],
        payload: { name, location: workplace },
      },
      "open the studio",
      false,
    );
    toast("Studio opened.", true);
    nameEl.value = "";
    document.getElementById("studio-new-form").hidden = true;
    studiosCache = null;
    document.getElementById("schedule-studio").dataset.loaded = "";
    setTimeout(renderStudiosAdmin, 700);
  } catch (e) {
    toast(e.message, false);
  } finally {
    submit.disabled = false;
  }
}

async function renderStudiosAdmin() {
  const grid = document.getElementById("studios-grid");
  const summary = document.getElementById("studios-summary");
  grid.innerHTML = "";
  summary.textContent = "";
  let studios;
  try {
    studios = await loadStudios();
  } catch (e) {
    grid.innerHTML = '<div class="empty">' + esc(e.message) + "</div>";
    return;
  }
  summary.textContent = studios.length + " studio" + (studios.length === 1 ? "" : "s");
  if (!studios.length) {
    grid.innerHTML = '<div class="empty">No studios yet.</div>';
    return;
  }
  grid.innerHTML = studios.map(studioCard).join("");
  studios.forEach(wireStudioCard);
}

function studioCard(s) {
  const id = domId(s.studioKey);
  return (
    '<div class="card">' +
    '<div class="who">' + esc(s.name || "?") + "</div>" +
    '<div class="meta">' + esc(shortKey(s.studioKey)) + "</div>" +
    '<div class="card-actions"><button id="sess-toggle-' + id + '" class="ghost">Schedule a class</button></div>' +
    '<div id="sess-form-' + id + '" class="session-form" hidden>' +
    '<div class="field"><label>Class name</label><input type="text" id="sess-name-' + id + '" placeholder="e.g. Vinyasa Flow" maxlength="120" /></div>' +
    '<div class="field"><label>Starts</label><input type="datetime-local" id="sess-starts-' + id + '" step="900" /></div>' +
    '<div class="field"><label>Ends</label><input type="datetime-local" id="sess-ends-' + id + '" step="900" /></div>' +
    '<div class="field"><label>Capacity</label><input type="number" id="sess-cap-' + id + '" min="1" max="200" value="20" /></div>' +
    '<div class="field"><label>Led by</label><select id="sess-instr-' + id + '"></select></div>' +
    '<button id="sess-create-' + id + '">Schedule class</button>' +
    "</div>" +
    "</div>"
  );
}

function wireStudioCard(s) {
  const id = domId(s.studioKey);
  const form = document.getElementById("sess-form-" + id);
  const instrSelect = document.getElementById("sess-instr-" + id);
  document.getElementById("sess-toggle-" + id).addEventListener("click", async () => {
    form.hidden = !form.hidden;
    if (form.hidden || instrSelect.dataset.loaded) return;
    // Instructors who teach at THIS studio lead its classes; the rest stay
    // offerable, since teachesAt is optional and a stand-in is ordinary.
    let instructors = [];
    try {
      instructors = await loadInstructors();
    } catch (e) {
      toast(e.message, false);
    }
    const here = instructors.filter((i) => i.studioKey === s.studioKey);
    const elsewhere = instructors.filter((i) => i.studioKey !== s.studioKey);
    instrSelect.innerHTML = '<option value="">(no instructor)</option>';
    for (const i of here.concat(elsewhere)) {
      const opt = document.createElement("option");
      opt.value = i.instructorKey;
      opt.textContent = i.displayName + (i.studioKey === s.studioKey ? "" : " (visiting)");
      instrSelect.appendChild(opt);
    }
    instrSelect.dataset.loaded = "1";
  });
  document.getElementById("sess-create-" + id).addEventListener("click", () => {
    createSession(s.studioKey, {
      name: document.getElementById("sess-name-" + id),
      starts: document.getElementById("sess-starts-" + id),
      ends: document.getElementById("sess-ends-" + id),
      capacity: document.getElementById("sess-cap-" + id),
      instructor: instrSelect,
      submit: document.getElementById("sess-create-" + id),
    });
  });
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
  const instructor = els.instructor ? els.instructor.value : "";
  els.submit.disabled = true;
  try {
    const payload = { studio: studioKey, name, startsAt, endsAt, capacity };
    // The instructor endpoint is validated alive + typed by the script
    // (require_live_typed), so it is an (a)-declared REQUIRED read whenever
    // one is named — omitted entirely when the class has no instructor.
    const reads = [studioKey];
    if (instructor) {
      payload.instructor = instructor;
      reads.push(instructor);
    }
    // A staff submit carries NO authContext.target: CreateSession's
    // frontOfHouse grant is scope=any, confined in-script by the caller's own
    // worksAt walk rather than by a caller-supplied target.
    await opOrThrow(
      {
        operationType: "CreateSession",
        class: "session",
        reads,
        optionalReads: slotCellKeys(studioKey, startsAt, endsAt),
        payload,
      },
      "schedule the class",
      false,
    );
    toast("Class scheduled.", true);
    els.name.value = "";
    sessionsCache = null;
    document.getElementById("roster-session").dataset.loaded = "";
    setTimeout(renderStudiosAdmin, 700);
  } catch (e) {
    toast(e.message, false);
  } finally {
    els.submit.disabled = false;
  }
}

// ---- init --------------------------------------------------------

function init() {
  document.querySelectorAll(".tab").forEach((b) => {
    b.addEventListener("click", () => showView(b.dataset.view));
  });
  document.getElementById("schedule-studio").addEventListener("change", renderSchedule);
  document.getElementById("schedule-refresh").addEventListener("click", () => {
    studiosCache = null; sessionsCache = null; residencyCache = null;
    document.getElementById("schedule-studio").dataset.loaded = "";
    loadSchedule();
  });
  document.getElementById("myclasses-refresh").addEventListener("click", renderMyClasses);
  document.getElementById("roster-session").addEventListener("change", renderRoster);
  document.getElementById("roster-refresh").addEventListener("click", () => {
    sessionsCache = null;
    document.getElementById("roster-session").dataset.loaded = "";
    loadRoster();
  });
  document.getElementById("studios-refresh").addEventListener("click", () => {
    studiosCache = null; instructorsCache = null;
    renderStudiosAdmin();
  });
  document.getElementById("studio-new-toggle").addEventListener("click", () => {
    const form = document.getElementById("studio-new-form");
    form.hidden = !form.hidden;
  });
  document.getElementById("studio-new-create").addEventListener("click", createStudio);
  document.getElementById("sign-out").addEventListener("click", signOut);

  // Who signed in decides every derived affordance (which hats, which tabs),
  // so it has to be known before the first render that reads it.
  loadWhoami().then(() => {
    applyHatGating();
    showView("schedule");
  });
}

init();
