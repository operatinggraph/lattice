"use strict";

// Café app — POS · Front Desk · Resident. Vanilla JS, no build step. The page
// is sign-in-first: one identity holds the whole session, the HttpOnly
// session cookie authenticates every same-origin read, and writes (OpenTab /
// Charge / Settle) go browser-direct to the Gateway's POST /v1/operations via
// submitOp() with that same session's bearer (real-actor-write-auth-e2e-
// design.md §3.1). The Go server does the NATS I/O behind the read
// endpoints, scoping every read to the signed-in session
// (persona-worlds-design.md Fire W4 §3): a `worksAt` staffer sees the house
// (POS + Front Desk + any lease in Resident lookup mode); a resident sees
// only their own lease's rows, in the Resident view alone.

const state = {
  identityId: null, // the signed-in identity's bare NanoID (GET /api/whoami) — the one actor every read and write runs as
  canSignOut: false, // whether whoami reports a real cookie session
  anchors: [], // the signed-in identity's residence/workplace anchors (whoami hat hints, persona-worlds-design.md §4). A `worksAt` anchor marks front-of-house staff.
  frontOfHouse: false, // server-resolved frontOfHouse role (GET /api/staff-hats) — the conjunct isFrontDesk composes with the worksAt anchor above. Fails closed: false until proven true.
  identities: [], // the protected cafeIdentitiesRead roster (loadIdentities) — at minimum the signed-in actor's own row, resolved by name
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
// authContext.target a resident's self-scoped write is checked against by
// cafe-domain's `consumer` scope=self grant (packages/cafe-domain/permissions.go).
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
// RESIDENT hat, and only then is authContext.target attached.
//
// A staff submit must NOT carry a target, even its own. cafe-domain's
// scripts branch on the mere PRESENCE of authContextTarget, not on whether
// the platform validated it: Charge reads presence as "self-order — take the
// price from the catalog, ignore the caller's amountCents", and OpenTab and
// Settle require the target to be that lease's own applicant
// (packages/cafe-domain/ddls.go). A staffer acting on a resident's lease is
// not that applicant, so an unconditional target would deny every POS and
// front-desk write — and silently drop the staff-entered amount first.
// The Processor passes the field through verbatim from the envelope
// (internal/processor/starlark_runner.go:645-648), so which grant authorized
// the op does not change what the script sees.
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

// applicationForOptionalRead returns the OpenTab/Charge/Settle self-scope
// guard's declared read (packages/cafe-domain/ddls.go): a resident's own
// submit declares the lease's applicationFor→identity link so the Starlark
// script can confirm the lease is theirs without a live GET. Only a
// self-scoped submit declares it — a staff submit carries no
// authContext.target, so the script's ownership branch never runs.
function applicationForOptionalRead(leaseAppKey) {
  return "lnk.leaseapp." + idOf(leaseAppKey) + ".applicationFor.identity." + idOf(state.identityId);
}

// chargedToOptionalRead returns Settle's class-(d) dedup read for its own
// chargedTo backfill (packages/cafe-domain/ddls.go): every Settle submission
// declares whether the tab already carries its permanent chargedTo link, so
// the script can create it without a live GET when a tab is missing it —
// keeping cafeTabSettlement's post-settlement money-gap anchor always
// present.
function chargedToOptionalRead(tabKey, leaseAppKey) {
  return "lnk.tab." + idOf(tabKey) + ".chargedTo.leaseapp." + idOf(leaseAppKey);
}

// ---- op catalog + shared op-form renderer (staff-descriptor-rendering-design.md §15) ----
//
// loadOpCatalog fetches the op-catalog descriptors (GET /api/op-catalog,
// proxying the edge-manifest opCatalog lens) once and caches them in
// opCatalogCache. A migrated form's dispatch shape (class, targetField,
// reads, authContext) is read off the matching row instead of hardcoded
// here, so a descriptor edit changes the form with no app rebuild. A FAILED
// load is not cached — opCatalogPromise is cleared — so a transient outage
// retries on the next call instead of poisoning the page for its whole
// session. Mirrors clinic-app/web/app.js's and wellness-app/web/app.js's own
// loadOpCatalog.
let opCatalogPromise = null;
let opCatalogCache = null;
async function loadOpCatalog() {
  if (!opCatalogPromise) {
    opCatalogPromise = appGet("/api/op-catalog").then(
      (data) => (data && data.catalog) || {},
      (e) => {
        opCatalogPromise = null;
        throw e;
      },
    );
  }
  opCatalogCache = await opCatalogPromise;
  return opCatalogCache;
}

// loadOpCatalogQuiet keeps a catalog outage from taking a whole panel down: an
// op whose descriptor did not arrive is simply NOT OFFERED — the same
// fail-closed answer as an op that carries no descriptor at all.
async function loadOpCatalogQuiet() {
  try {
    await loadOpCatalog();
  } catch (_) {
    /* not offered, rather than offered and broken */
  }
}

// loadDescriptorform imports the shared op-form renderer exactly once —
// served at /shared/form.mjs by this app's server.go, beside its own
// embedded web/ FileServer. A dynamic import() keeps app.js a plain
// (non-module) script, unchanged for every other caller.
// descriptorformModule caches the resolved module itself (not just the
// promise); a rejected import clears BOTH so a transient load failure is
// retried on the next call rather than poisoning the session. Mirrors
// clinic-app/web/app.js's and wellness-app/web/app.js's own
// loadDescriptorform.
let descriptorformPromise = null;
let descriptorformModule = null;
function loadDescriptorform() {
  if (!descriptorformPromise) {
    descriptorformPromise = import("/shared/form.mjs").then(
      (mod) => {
        descriptorformModule = mod;
        return mod;
      },
      (e) => {
        descriptorformPromise = null;
        descriptorformModule = null;
        throw e;
      },
    );
  }
  return descriptorformPromise;
}

// revealCeremonySecret narrates, in this app's toast vocabulary, whatever the
// module's own revealCeremonySecret did with a minted plaintext. The DECISION
// — descriptorform's ceremony rule 3, an affirmative `status === "accepted"`
// and never the weaker "not rejected" — is the module's, so all four staff
// apps enforce one implementation of it rather than four re-derivations of
// "does this reply confirm the write landed?". Two of them do not: a
// `duplicate` reply says an earlier submission claimed this requestId, and a
// Processor reply timeout answers a status-less HTTP 202, and neither
// confirms the envelope carrying this secret's hash committed.
//
// It reads the already-resolved descriptorformModule rather than awaiting
// loadDescriptorform() again. Holding a reveal means a form rendered, which
// means the module is loaded — so the await would buy nothing, and could only
// invent a way to lose the single copy of the secret in the window between
// the write landing and its display.
function revealCeremonySecret(reveal, reply) {
  // Nothing was minted for an ordinary op, so the module is never reached for
  // one — its absence must not surface as a lost-secret warning.
  if (!reveal) return;
  let outcome;
  try {
    outcome = descriptorformModule.revealCeremonySecret(reveal, reply);
  } catch (e) {
    // Contained, and reported as a landed write, because the only thing that
    // can throw in there is the display, which the module reaches only on a
    // confirmed commit. Every caller runs this inside the same try whose catch
    // reports the submission as failed, so an uncaught throw would tell the
    // person the opposite of what happened and hide the fact that matters —
    // the target is armed with a secret nobody now holds, which is only
    // fixable by issuing a fresh one.
    console.error("ceremony reveal failed", e);
    toast("The write landed but its one-time secret could not be shown — issue a fresh one.", false);
    return;
  }
  if (outcome === "withheld") {
    toast("The write was not confirmed, so its one-time secret was not shown — check whether it landed, and issue a fresh one.", false);
  }
}

// isTransientAuthLag reports whether a rejected reply is the known,
// architecturally-expected async-projection race — the Capability Lens or
// the credential-bindings materializer (both eventually-consistent CDC
// projections, lattice-architecture.md's documented <500ms p99 lag) still
// catching up on an actor's first touch, not yet visible to THIS
// immediately-following request. Distinguishes it from a genuine, persistent
// authorization denial, which should surface immediately rather than retry.
// Mirrors clinic-app/web/app.js's and wellness-app/web/app.js's own
// isTransientAuthLag verbatim.
function isTransientAuthLag(reply) {
  if (!reply || reply.status !== "rejected" || !reply.error) return false;
  if (reply.error.code !== "AuthDenied") return false;
  const reason = reply.error.details && reply.error.details.reason;
  return reason === "NoCapabilityEntry" || reason === "OperationNotPermitted";
}

// retryBackoffsMs is the bounded backoff schedule the isTransientAuthLag
// retry loop uses — ~3s total, mirrors clinic-app/web/app.js's and
// wellness-app/web/app.js's own.
const retryBackoffsMs = [200, 400, 800, 1600];

// submitCatalogOp posts the envelope a descriptorform handle's submit()
// returns — {operationType, class, payload, reads, optionalReads,
// authContext} — to the same endpoint every hand-built write already uses
// (submitOp), applying the bounded isTransientAuthLag retry before throwing a
// friendly "Could not <what> — <reason>" Error on a still-rejected reply.
// Needed specifically for CreditCafeAccount's self-pay path: a resident
// identity signing in for the first time can outrun its own capability
// projection, the same race Inc 3a/3b's adversarial passes fixed for
// clinic's/wellness's self-scoped and staff-standing pairs. Applied to every
// migrated op rather than only the self-scoped ones — harmless for a
// staff-standing submission, which never races a just-opened grant, since
// the retry only ever fires on the specific AuthDenied/reason signature
// above, never on an ordinary validation rejection. Mirrors
// wellness-app/web/app.js's own submitCatalogOp(envelope, what) shape
// (clinic-app's own submitCatalogOp(envelope) takes no "what" and lets the
// caller format the rejection; this app's own opOrThrow already formats
// "Could not <what> — <reason>", so this mirrors wellness's shape to keep
// that idiom for every migrated call site too).
async function submitCatalogOp(envelope, what) {
  let reply;
  for (let attempt = 0; ; attempt++) {
    reply = await submitOp(envelope, false);
    if (!isTransientAuthLag(reply) || attempt >= retryBackoffsMs.length) break;
    await new Promise((resolve) => setTimeout(resolve, retryBackoffsMs[attempt]));
  }
  if (reply && reply.status === "rejected") {
    const msg = reply.error ? `${reply.error.code}: ${reply.error.message}` : "rejected";
    throw new Error(`Could not ${what} — ${msg}`);
  }
  return reply || {};
}

// ---- formatting --------------------------------------------------------

function money(cents) {
  const n = (cents || 0) / 100;
  return "$" + n.toFixed(2);
}

// ledgerBalanceLine renders a signed balanceCents (debits − credits) as
// owed/credit/paid-in-full, mirroring loftspace-app's refreshLedgerBody —
// money() alone reads a negative balance as "$-21.59", which says nothing
// about whether it's money owed or money already paid ahead.
function ledgerBalanceLine(balanceCents) {
  const cents = balanceCents || 0;
  if (cents > 0) return "Balance owed: " + money(cents);
  if (cents < 0) return "Credit balance: " + money(-cents);
  return "Balance: $0.00 (paid in full)";
}

// statementLine renders the ledger's dueDate/isOverdue/daysOverdue fields (a
// FIFO-aged statement, cmd/cafe-app/ledger.go's deriveStatement) as a due-by
// note or a red overdue banner — "" when there's nothing owed to age.
function statementLine(ledger) {
  if (!ledger.dueDate) return "";
  const due = new Date(ledger.dueDate).toLocaleDateString();
  if (ledger.isOverdue) {
    const days = ledger.daysOverdue || 0;
    return (
      '<p class="ledger-overdue" style="color:#b00020;font-weight:600;">' +
      "OVERDUE — " + days + (days === 1 ? " day" : " days") + " past due (was due " + due + ")" +
      "</p>"
    );
  }
  return '<p class="meta">Due ' + due + "</p>";
}

// itemsMemoLine renders a tab's running itemsMemo (cafe-domain's tabStatus
// aspect — comma-joined names, "" on a fresh tab) as its own meta line, or
// nothing at all when there is nothing charged yet to show.
function itemsMemoLine(memo) {
  return memo ? '<p class="meta">Items: ' + escapeHtml(memo) + "</p>" : "";
}

// orderedByLabel resolves a lines entry's orderedBy (cafe-domain's op.actor,
// full vtx.identity.<NanoID>, ddls.go) to a short "by <name>" tag via the
// same nameForIdentity/idOf roster lookup the lease picker already uses for
// bookerKey — "" when the line predates the field or the roster can't
// resolve it (a resident's own view never holds a staffer's row), so a shared
// house tab's receipt distinguishes each resident's self-order and a staff
// ring-up instead of reading identically either way.
function orderedByLabel(orderedBy) {
  if (!orderedBy) return "";
  return " · by " + escapeHtml(nameForIdentity(idOf(orderedBy)));
}

// chargeLinesBlock renders a tab's itemized .status.lines (cafe-domain's
// tabStatus aspect — one {id, description, amountCents, voided, orderedBy}
// entry per Charge, the structured twin of the flat itemsMemo string) as a
// priced list, a voided line struck through and labeled rather than hidden. A
// tab whose lines is empty or absent (predates the field, or nothing charged
// yet) falls back to the flat itemsMemo line — the only place old and new
// tabs still look the same. voidableTabKey, when given, adds a per-line
// Void action (wired by the caller after insertion) — staff POS only, since
// VoidCharge grants no self-service scope. A synthetic {pending: true} line
// (renderResident's own optimistic overlay, not real cafeTabs data) renders
// muted and labeled instead of getting a Void button.
function chargeLinesBlock(lines, memo, voidableTabKey) {
  if (!lines || !lines.length) return itemsMemoLine(memo);
  return (
    '<ul class="items-list">' +
    lines
      .map(
        (l) =>
          '<li class="item-line' + (l.voided ? " voided" : "") + (l.pending ? " pending" : "") + '">' +
          '<span class="item-desc">' + escapeHtml(l.description) + orderedByLabel(l.orderedBy) + "</span>" +
          '<span class="item-amount">' + money(l.amountCents) + "</span>" +
          (l.voided
            ? '<span class="meta">(voided)</span>'
            : l.pending
            ? '<span class="meta">(pending)</span>'
            : voidableTabKey
            ? '<button type="button" class="ghost" data-void-line="' + escapeHtml(l.id) + '">Void</button>'
            : "") +
          "</li>"
      )
      .join("") +
    "</ul>"
  );
}

// parseDollars turns a user-entered dollar string ("4.50") into integer
// cents, or null when it isn't a positive amount.
function parseDollars(s) {
  const n = Number(s);
  if (!isFinite(n) || n <= 0) return null;
  return Math.round(n * 100);
}

// rentAmount formats a lease's unit rent — a plain dollar amount (not
// cents, unlike money()'s café-ledger amounts) with its currency code.
function rentAmount(amount, currency) {
  return "$" + Number(amount).toFixed(0) + " " + (currency || "");
}

function shortKey(key) {
  if (!key) return "";
  const parts = key.split(".");
  const id = parts[parts.length - 1];
  return id.length > 10 ? id.slice(0, 6) + "…" + id.slice(-4) : id;
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
      await loadStaffHats();
      return;
    } catch (_) {
      if (attempt >= whoamiRetryBackoffsMs.length) {
        state.identityId = null;
        state.canSignOut = false;
        state.anchors = [];
        state.frontOfHouse = false;
        return;
      }
      await new Promise((resolve) => setTimeout(resolve, whoamiRetryBackoffsMs[attempt]));
    }
  }
}

// loadStaffHats reads the server-resolved frontOfHouse hat (GET
// /api/staff-hats) — the app-side mirror of resolveSubjectHats
// (cmd/cafe-app/readauth.go), which whoami's opaque anchors/roles cannot
// express FE-side. Fails CLOSED: any fetch error (network, 401, malformed
// body) leaves state.frontOfHouse false, hiding the staff-only tabs rather
// than showing a surface the server would only 403 on every write.
async function loadStaffHats() {
  try {
    const body = await api("/api/staff-hats", { credentials: "same-origin" });
    state.frontOfHouse = !!(body && body.frontOfHouse);
  } catch (_) {
    state.frontOfHouse = false;
  }
}

// isFrontDesk marks a session that works the café's front of house AND holds
// the frontOfHouse role — the FE mirror of the write side's own
// `GrantsTo: [operator, frontOfHouse]` and the read side's own isFrontDesk
// (cmd/cafe-app/readauth.go): a worksAt-only, role-less caller holds neither
// a POS grant nor a PII-read grant, so gating on the worksAt anchor alone
// showed staff tabs that would only 403 on every click. UX curation only:
// the graph's grants + this app's own server-side read scoping remain the
// authority.
function isFrontDesk() {
  return (
    Array.isArray(state.anchors) &&
    state.anchors.some((a) => a && a.relation === "worksAt") &&
    !!state.frontOfHouse
  );
}

// nameForIdentity resolves a bare identity NanoID to its display name via the
// loaded protected roster (state.identities), falling back to the truncated
// key when the roster hasn't loaded yet or carries no matching row — mirrors
// cmd/clinic-app's nameForIdentity / cmd/loftspace-app's nameFor.
function nameForIdentity(key) {
  const m = state.identities.find((i) => idOf(i.identityKey) === key);
  return m && m.name ? m.name : shortKey(key);
}

// loadIdentities reads the protected, RLS-scoped identity-name roster
// (cafeIdentitiesRead) as the signed-in session — at minimum the caller's own
// self-anchored row, plus every named identity for a WildcardAnchor holder.
async function loadIdentities() {
  try {
    const data = await api("/api/identities", { credentials: "same-origin" });
    state.identities = (data && data.identities) || [];
  } catch (e) {
    console.warn("identities roster unavailable:", e);
    state.identities = [];
  }
  refreshMeBar();
}

function refreshMeBar() {
  const status = document.getElementById("me-status");
  const signOutBtn = document.getElementById("sign-out");
  status.textContent = state.identityId
    ? "Signed in as " + nameForIdentity(state.identityId) + (isFrontDesk() ? " (front of house)" : " (resident)")
    : "";
  signOutBtn.hidden = !state.canSignOut;
}

function signOut() {
  fetch("/api/logout", { method: "POST", credentials: "same-origin" })
    .catch(() => {})
    .finally(() => location.replace("/login"));
}

// applyHatGating hides the staff-only tabs (POS, Front Desk, Manage Menu)
// from a session that lacks isFrontDesk (worksAt AND frontOfHouse), and
// bounces the active view to Resident if it just became disallowed.
// Idempotent; re-run whenever whoami/staff-hats resolve.
function applyHatGating() {
  const fd = isFrontDesk();
  document.getElementById("tab-pos").hidden = !fd;
  document.getElementById("tab-frontdesk").hidden = !fd;
  document.getElementById("tab-menu").hidden = !fd;
  refreshMeBar();
  const active = document.querySelector(".tab.active");
  if (active && active.hidden) showView("resident");
}

// ---- view routing -------------------------------------------------

function showView(view) {
  if ((view === "pos" || view === "frontdesk" || view === "menu") && !isFrontDesk()) view = "resident";
  document.querySelectorAll("[role=tabpanel]").forEach((s) => {
    s.hidden = s.id !== "view-" + view;
  });
  document.querySelectorAll(".tab").forEach((b) => {
    const active = b.dataset.view === view;
    b.classList.toggle("active", active);
    b.setAttribute("aria-selected", active ? "true" : "false");
  });
  if (view === "pos") loadPos();
  else if (view === "frontdesk") loadFrontDesk();
  else if (view === "menu") loadManageMenu();
  else if (view === "resident") loadResident();
}

// ---- leases (shared picker data — staff-visible: every lease; resident: their own) ---

let leasesCache = null;
async function loadLeases() {
  if (leasesCache) return leasesCache;
  const body = await appGet("/api/leases");
  leasesCache = body.leases || [];
  return leasesCache;
}

// loadLeasePickerContext resolves each lease to its resident's name +
// landlord-approval status (/api/residents) and unit address
// (/api/frontdesk-lease-details) for the staff-facing lease pickers (POS +
// front desk's resident-view picker) — the same best-effort,
// degrade-to-lease-key join frontDeskCard already does.
async function loadLeasePickerContext() {
  let residentsByLease = {};
  let approvedByLease = {};
  try {
    const rs = await appGet("/api/residents");
    (rs.residents || []).forEach((r) => {
      residentsByLease[r.leaseAppKey] = r.bookerKey;
      approvedByLease[r.leaseAppKey] = r.approved;
    });
  } catch (_) { /* residents roster unreachable — picker falls back to the lease key */ }
  let leaseDetailsByLease = {};
  try {
    const ld = await appGet("/api/frontdesk-lease-details");
    (ld.leaseDetails || []).forEach((d) => { leaseDetailsByLease[d.leaseAppKey] = d; });
  } catch (_) { /* front-desk not installed / unreachable — unit address just doesn't show */ }
  return { residentsByLease, approvedByLease, leaseDetailsByLease };
}

// fillLeaseSelect renders every pickable lease, disabling one the landlord
// hasn't approved yet (approvedByLease[leaseAppKey] === false) — OpenTab
// itself now rejects LeaseNotApproved, but a disabled, badged option tells
// staff why before they even try, instead of a raw error toast. A lease
// this app can't resolve approval for (roster unreachable, or a lease absent
// from /api/residents entirely) stays selectable — this picker only blocks
// on POSITIVE evidence of non-approval, never on its absence.
function fillLeaseSelect(select, leases, residentsByLease, leaseDetailsByLease, approvedByLease) {
  const prev = select.value;
  select.innerHTML = "";
  if (!leases.length) {
    const opt = document.createElement("option");
    opt.textContent = "(no leases)";
    opt.value = "";
    select.appendChild(opt);
    return;
  }
  for (const l of leases) {
    const opt = document.createElement("option");
    opt.value = l.leaseAppKey;
    const bookerKey = residentsByLease && residentsByLease[l.leaseAppKey];
    const who = bookerKey ? nameForIdentity(idOf(bookerKey)) : shortKey(l.leaseAppKey);
    const detail = leaseDetailsByLease && leaseDetailsByLease[l.leaseAppKey];
    const unit = detail && detail.unitAddress ? " — " + detail.unitAddress : "";
    const approved = approvedByLease && approvedByLease[l.leaseAppKey];
    if (approved === false) {
      opt.disabled = true;
      opt.textContent = who + unit + " (awaiting landlord approval)";
    } else {
      opt.textContent = who + unit + (l.accountKey ? "" : " (no café account yet)");
    }
    select.appendChild(opt);
  }
  if (prev && leases.some((l) => l.leaseAppKey === prev)) select.value = prev;
}

// ---- POS view (staff only) --------------------------------------------

async function loadPos() {
  const select = document.getElementById("pos-lease");
  const [leases, ctx] = await Promise.all([loadLeases(), loadLeasePickerContext()]);
  fillLeaseSelect(select, leases, ctx.residentsByLease, ctx.leaseDetailsByLease, ctx.approvedByLease);
  await renderPos();
}

async function renderPos() {
  const body = document.getElementById("pos-body");
  const summary = document.getElementById("pos-summary");
  const leaseAppKey = document.getElementById("pos-lease").value;
  body.innerHTML = "";
  summary.textContent = "";
  if (!leaseAppKey) {
    body.innerHTML = '<div class="empty">Pick a lease to open or manage its tab.</div>';
    return;
  }
  let tabs, menu;
  try {
    const results = await Promise.all([
      appGet("/api/tabs?leaseAppKey=" + encodeURIComponent(leaseAppKey)),
      appGet("/api/menu?leaseAppKey=" + encodeURIComponent(leaseAppKey)),
    ]);
    tabs = results[0].tabs || [];
    menu = results[1];
  } catch (e) {
    body.innerHTML = '<div class="empty">' + e.message + "</div>";
    return;
  }
  const open = tabs.find((t) => t.status === "open");
  if (!open) {
    body.innerHTML = renderOpenTabForm();
    document.getElementById("open-tab-btn").addEventListener("click", async () => {
      const btn = document.getElementById("open-tab-btn");
      btn.disabled = true;
      try {
        await opOrThrow(
          {
            operationType: "OpenTab",
            class: "tab",
            reads: [leaseAppKey],
            optionalReads: [leaseAppKey + ".cafeOpenTab", leaseAppKey + ".decision"],
            payload: { leaseAppKey },
          },
          "open the tab"
        );
        toast("Tab opened.", true);
        setTimeout(renderPos, 700);
      } catch (e) {
        toast(e.message, false);
        btn.disabled = false;
      }
    });
    return;
  }
  const items = (menu && menu.menu) || [];
  body.innerHTML = renderOpenTabCard(open, items);
  body.querySelectorAll("[data-void-line]").forEach((btn) => {
    btn.addEventListener("click", async () => {
      const lineId = btn.dataset.voidLine;
      btn.disabled = true;
      try {
        await opOrThrow(
          {
            operationType: "VoidCharge", class: "tab",
            reads: [open.tabKey, open.tabKey + ".status"],
            payload: { tabKey: open.tabKey, lineId },
          },
          "void the charge"
        );
        toast("Voided.", true);
        setTimeout(renderPos, 700);
      } catch (e) {
        toast(e.message, false);
        btn.disabled = false;
      }
    });
  });
  const catalogForm = document.getElementById("pos-catalog-form");
  if (catalogForm) {
    catalogForm.addEventListener("submit", async (ev) => {
      ev.preventDefault();
      const menuItemKey = document.getElementById("pos-catalog-item").value;
      if (!menuItemKey) { toast("Pick an item first.", false); return; }
      const btn = document.getElementById("pos-catalog-submit");
      btn.disabled = true;
      try {
        await opOrThrow(
          {
            operationType: "Charge", class: "tab",
            reads: [open.tabKey, open.tabKey + ".status", menuItemKey, menuItemKey + ".price"],
            payload: { tabKey: open.tabKey, menuItemKey },
          },
          "ring up the item"
        );
        toast("Added to the tab.", true);
        setTimeout(renderPos, 700);
      } catch (e) {
        toast(e.message, false);
        btn.disabled = false;
      }
    });
  }
  document.getElementById("charge-form").addEventListener("submit", async (ev) => {
    ev.preventDefault();
    const input = document.getElementById("charge-amount");
    const descInput = document.getElementById("charge-desc");
    const cents = parseDollars(input.value);
    if (cents === null) { toast("Enter a charge amount greater than $0.", false); return; }
    const btn = document.getElementById("charge-submit");
    btn.disabled = true;
    try {
      await opOrThrow(
        {
          operationType: "Charge", class: "tab",
          reads: [open.tabKey, open.tabKey + ".status"],
          payload: { tabKey: open.tabKey, amountCents: cents, description: descInput.value.trim() || undefined },
        },
        "add the charge"
      );
      toast("Charged " + money(cents) + ".", true);
      input.value = "";
      descInput.value = "";
      setTimeout(renderPos, 700);
    } catch (e) {
      toast(e.message, false);
    } finally {
      btn.disabled = false;
    }
  });
  document.getElementById("settle-btn").addEventListener("click", async () => {
    const btn = document.getElementById("settle-btn");
    btn.disabled = true;
    try {
      await opOrThrow(
        {
          operationType: "Settle", class: "tab",
          reads: [open.tabKey, open.tabKey + ".status"],
          optionalReads: [chargedToOptionalRead(open.tabKey, leaseAppKey)],
          payload: { tabKey: open.tabKey },
        },
        "settle the tab"
      );
      toast("Tab settled — posting to the café ledger shortly.", true);
      setTimeout(renderPos, 700);
    } catch (e) {
      toast(e.message, false);
      btn.disabled = false;
    }
  });
  // Wired AFTER settle-btn's own listener, and not awaited: this function's
  // own catalog/module load is the slow part, and Settle Tab must not sit
  // inert (rendered enabled, non-functional) while it's in flight. Safe to
  // fire-and-forget — wireVoidChargeForm resolves its own #void-form/
  // #void-fields/#void-submit elements before its first await, so a second
  // concurrent renderPos (e.g. the #pos-lease change handler firing while a
  // pending setTimeout(renderPos, 700) is still in flight) leaves it
  // operating on its own already-captured, still-live DOM nodes rather than
  // a stale reference.
  wireVoidChargeForm(open.tabKey);
}

function renderOpenTabForm() {
  return (
    '<div class="panel">' +
    "<h2>No open tab</h2>" +
    '<p class="lead">This lease has no open house tab.</p>' +
    '<div class="panel-actions"><button id="open-tab-btn">Open Tab</button></div>' +
    "</div>"
  );
}

function renderOpenTabCard(tab, items) {
  const catalog = items || [];
  return (
    '<div class="panel">' +
    "<h2>Open tab</h2>" +
    '<p class="amount">' + money(tab.totalCents) + "</p>" +
    '<p class="meta">Opened ' + (tab.openedAt || "?") + "</p>" +
    chargeLinesBlock(tab.lines, tab.itemsMemo, tab.tabKey) +
    (catalog.length
      ? '<form id="pos-catalog-form" class="field-row" style="margin-bottom:14px;">' +
        '<select id="pos-catalog-item">' +
        catalog
          .map((it) => '<option value="' + it.menuItemKey + '">' + escapeHtml(it.name) + " — " + money(it.priceCents) + "</option>")
          .join("") +
        "</select>" +
        '<button id="pos-catalog-submit" type="submit">Ring Up</button>' +
        "</form>"
      : "") +
    '<form id="charge-form" class="field-row" style="margin-bottom:14px;">' +
    '<input id="charge-amount" type="number" step="0.01" min="0.01" placeholder="Off-menu amount ($)" required />' +
    '<input id="charge-desc" type="text" placeholder="Description (optional)" />' +
    '<button id="charge-submit" type="submit">Add Charge</button>' +
    "</form>" +
    '<form id="void-form" class="field-row" style="margin-bottom:14px;">' +
    '<div id="void-fields" style="flex:1"></div>' +
    '<button type="submit" id="void-submit" class="ghost" disabled>Void</button>' +
    "</form>" +
    '<div class="panel-actions"><button id="settle-btn" class="danger">Settle Tab</button></div>' +
    "</div>"
  );
}

// wireVoidChargeForm mounts VoidCharge's descriptor form into the POS tab
// panel's #void-form and wires its submit (the surrounding <form> + a
// type="submit" button, not a bare click handler, so the ordinary
// type-amount-then-Enter POS gesture keeps working — renderOpForm itself
// only ever appends field <div>s into the mount it's given, never its own
// <form> wrapper, so wrapping #void-fields in one here is safe). VoidCharge
// is staff-standing (AuthContext "standing" — packages/cafe-domain/
// opmetas.go — no self-scope grant at all, no ownership probe declared), so
// this needs no context.me/selfVoice wiring: a POS correction is always a
// staff decision, the same straightforward standing-authContext shape
// SetInstructorProfile's own edit form uses in wellness-app.
//
// Only the amount-based void (payload {tabKey, amountCents}, the old
// #void-form) migrates. The per-line "Void" button rendered on each charge
// line (chargeLinesBlock's data-void-line buttons, wired separately above)
// submits {tabKey, lineId} — a shape VoidCharge's own InputSchema does not
// declare at all (it names tabKey/amountCents only) — the same "one-click
// list-row action, parameter already known, no dedicated form to migrate"
// category as RetireMenuItem/RemoveProviderSite/TombstoneStudio, so it stays
// hand-built (and, not coincidentally, already follows the
// success-leaves-it-disabled pattern below).
//
// A load/render failure renders its message INLINE into #void-fields rather
// than toasting: this function reruns on every renderPos (POS tab render),
// including the setTimeout(renderPos, 700) re-render after a successful
// Charge/Settle, so a toast here would silently stomp the just-shown green
// success toast 700ms later on a catalog outage.
async function wireVoidChargeForm(tabKey) {
  const form = document.getElementById("void-form");
  const mount = document.getElementById("void-fields");
  const btn = document.getElementById("void-submit");
  if (!form || !mount || !btn) return;
  btn.disabled = true;
  await loadOpCatalogQuiet();
  let renderOpForm;
  try {
    ({ renderOpForm } = await loadDescriptorform());
  } catch (e) {
    mount.innerHTML = '<p class="meta">Void form unavailable — ' + escapeHtml(e.message) + "</p>";
    return;
  }
  const row = opCatalogCache && opCatalogCache.VoidCharge;
  const handle = row && renderOpForm(row, { target: tabKey }, mount);
  if (!handle) {
    mount.innerHTML = '<p class="meta">The void form is unavailable.</p>';
    return;
  }
  btn.textContent = handle.descriptor.submitLabel;
  btn.disabled = false;
  form.addEventListener("submit", async (ev) => {
    ev.preventDefault();
    btn.disabled = true;
    let envelope, reveal;
    try {
      ({ envelope, reveal } = await handle.submit());
    } catch (e) {
      toast(e.message || String(e), false);
      btn.disabled = false;
      return;
    }
    // Left disabled on success rather than re-enabled in a `finally`: the
    // amount field lives inside the descriptor-owned mount, which this
    // function does not clear on success (unlike the old hand-built
    // #void-amount input's own input.value = ""), so a re-enabled button
    // would let a staffer double-click inside the 700ms setTimeout(renderPos,
    // 700) window and resubmit the SAME {tabKey, amountCents} envelope —
    // VoidCharge's amount branch has no dedup/idempotency key, so it just
    // subtracts again. renderPos's own re-render 700ms later mounts a fresh
    // form with a fresh (disabled-until-loaded) button, the same pattern the
    // adjacent per-line void button and every other POS submit above already
    // use.
    try {
      const amountCents = envelope.payload && envelope.payload.amountCents;
      const reply = await submitCatalogOp(envelope, "void the charge");
      revealCeremonySecret(reveal, reply);
      toast("Voided" + (amountCents ? " " + money(amountCents) : "") + ".", true);
      setTimeout(renderPos, 700);
    } catch (e) {
      toast(e.message, false);
      btn.disabled = false;
    }
  });
}

// ---- Front Desk view (staff only) --------------------------------------

// keepSoonest reduces a lease's badge candidates (bookings or visits) to the
// single soonest-upcoming one, mirroring the new Date(x.startsAt).getTime()
// idiom clinic-app/wellness-app already use for upcoming/past sorting — a
// lease's second same-day booking must not overwrite its first.
function keepSoonest(byLease, item) {
  const prev = byLease[item.leaseAppKey];
  if (!prev) { byLease[item.leaseAppKey] = item; return; }
  const prevAt = prev.startsAt ? new Date(prev.startsAt).getTime() : Infinity;
  const itemAt = item.startsAt ? new Date(item.startsAt).getTime() : Infinity;
  if (itemAt < prevAt) byLease[item.leaseAppKey] = item;
}

async function loadFrontDesk() {
  const grid = document.getElementById("frontdesk-grid");
  const summary = document.getElementById("frontdesk-summary");
  grid.innerHTML = "";
  summary.textContent = "";
  let tabs;
  try {
    const r = await appGet("/api/tabs");
    tabs = (r.tabs || []).filter((t) => t.status === "open");
  } catch (e) {
    grid.innerHTML = '<div class="empty">' + e.message + "</div>";
    return;
  }
  // The unified resident context: join each open tab to the resident's own
  // booked wellness class (if any) client-side by leaseAppKey — the front-desk
  // package (if installed) is the ONLY source for this, the café ledger has
  // no notion of a class booking. Best-effort: an unreachable/uninstalled
  // front-desk still renders the tabs, just without class badges.
  let bookingsByLease = {};
  try {
    const br = await appGet("/api/frontdesk-bookings");
    (br.bookings || []).forEach((b) => keepSoonest(bookingsByLease, b));
  } catch (_) { /* front-desk not installed / unreachable — badges just don't show */ }

  // Same join, for the resident's own upcoming clinic visit — existence +
  // time only, never the visit reason (front-desk's frontDeskVisits lens
  // never projects it). Best-effort, same degrade-to-hidden posture as above.
  let visitsByLease = {};
  try {
    const vs = await appGet("/api/frontdesk-visits");
    (vs.visits || []).forEach((v) => keepSoonest(visitsByLease, v));
  } catch (_) { /* front-desk not installed / unreachable — visit badge just doesn't show */ }

  // Same resident-name/unit-address join the lease pickers use
  // (loadLeasePickerContext) — the card's "who" + rent/term lines.
  const { residentsByLease, leaseDetailsByLease } = await loadLeasePickerContext();

  summary.textContent = tabs.length + " open tab" + (tabs.length === 1 ? "" : "s");
  if (!tabs.length) {
    grid.innerHTML = '<div class="empty">No open tabs.</div>';
    return;
  }
  grid.innerHTML = tabs
    .map((t) => frontDeskCard(t, bookingsByLease[t.leaseAppKey], leaseDetailsByLease[t.leaseAppKey], visitsByLease[t.leaseAppKey], residentsByLease[t.leaseAppKey]))
    .join("");
  tabs.forEach((t) => {
    const btn = document.getElementById("settle-" + t.tabKey.replace(/[^a-zA-Z0-9]/g, ""));
    if (!btn) return;
    btn.addEventListener("click", async () => {
      btn.disabled = true;
      try {
        await opOrThrow(
          {
            operationType: "Settle", class: "tab",
            reads: [t.tabKey, t.tabKey + ".status"],
            optionalReads: [chargedToOptionalRead(t.tabKey, t.leaseAppKey)],
            payload: { tabKey: t.tabKey },
          },
          "settle the tab"
        );
        toast("Tab settled.", true);
        setTimeout(loadFrontDesk, 700);
      } catch (e) {
        toast(e.message, false);
        btn.disabled = false;
      }
    });
  });
}

function frontDeskCard(t, booking, lease, visit, bookerKey) {
  const id = "settle-" + t.tabKey.replace(/[^a-zA-Z0-9]/g, "");
  const classBadge = booking
    ? '<div class="meta">🧘 Booked: ' + (booking.sessionName || "class") + " · " + (booking.startsAt || "?") + "</div>"
    : "";
  const leaseLine = lease && lease.unitRent
    ? '<div class="meta">🏠 ' + rentAmount(lease.unitRent, lease.unitCurrency) + "/mo" +
      (lease.unitLeaseTermMonths ? " · " + lease.unitLeaseTermMonths + "mo term" : "") + "</div>"
    : "";
  // Existence + time only — never a visit reason (front-desk staff see "a
  // visit is scheduled," not why or with whom).
  const visitBadge = visit
    ? '<div class="meta">🩺 Visit: ' + (visit.startsAt || "?") + "</div>"
    : "";
  // The lease's applicant, resolved to a name via the protected roster
  // (nameForIdentity) — falls back to the truncated lease key when the
  // applicant is unknown or unresolved, the same degrade the "who" title
  // always showed before this join existed.
  const who = bookerKey ? nameForIdentity(idOf(bookerKey)) : shortKey(t.leaseAppKey);
  return (
    '<div class="card">' +
    '<span class="badge open">open</span>' +
    '<div class="who">' + escapeHtml(who) + "</div>" +
    '<div class="amount">' + money(t.totalCents) + "</div>" +
    '<div class="meta">Opened ' + (t.openedAt || "?") + "</div>" +
    chargeLinesBlock(t.lines, t.itemsMemo, null) +
    classBadge +
    leaseLine +
    visitBadge +
    '<div class="card-actions"><button id="' + id + '" class="danger">Settle</button></div>' +
    "</div>"
  );
}

// ---- Manage Menu view (staff only) ---------------------------------

// workplaceLocationKey returns the staffer's own worksAt location — the
// only servedAt anchor a staff session can name for a new item without a
// building picker this app has no roster for. Mirrors isFrontDesk()'s own
// anchors walk. Returns "" when the session carries no worksAt anchor.
function workplaceLocationKey() {
  const a = Array.isArray(state.anchors) && state.anchors.find((x) => x && x.relation === "worksAt");
  return (a && a.key) || "";
}

// menuItemCard renders a Manage Menu grid item. missingLocation (the
// menuCatalog lens's own flag for an item whose servedAt link is gone — the
// place was retired out from under it) badges the item and offers Relocate
// instead of Retire being the only aim staff has on it; a live item just
// shows Retire, same as before.
function menuItemCard(it) {
  const badge = it.missingLocation
    ? '<span class="badge" style="background:#b00020;color:#fff;">no location</span>'
    : "";
  const relocate = it.missingLocation
    ? '<button type="button" data-relocate="' + escapeHtml(it.menuItemKey) + '">Relocate here</button>'
    : "";
  return (
    '<div class="card">' +
    badge +
    '<div class="who">' + escapeHtml(it.name) + "</div>" +
    '<div class="amount">' + money(it.priceCents) + "</div>" +
    '<div class="card-actions">' + relocate +
    '<button type="button" class="danger" data-retire="' +
    escapeHtml(it.menuItemKey) +
    '">Retire</button></div>' +
    "</div>"
  );
}

async function loadManageMenu() {
  const summary = document.getElementById("menu-summary");
  const body = document.getElementById("menu-body");
  summary.textContent = "";
  body.innerHTML = "";
  let items;
  try {
    const data = await appGet("/api/menu");
    items = data.menu || [];
  } catch (e) {
    body.innerHTML = '<div class="empty">' + e.message + "</div>";
    return;
  }
  summary.textContent = items.length + " item" + (items.length === 1 ? "" : "s");
  body.innerHTML = items.length
    ? '<div class="grid">' + items.map(menuItemCard).join("") + "</div>"
    : '<div class="empty">No menu items yet — add one above.</div>';
  body.querySelectorAll("[data-retire]").forEach((btn) => {
    btn.addEventListener("click", async () => {
      const menuItemKey = btn.dataset.retire;
      btn.disabled = true;
      try {
        await opOrThrow(
          {
            operationType: "RetireMenuItem", class: "menuitem",
            reads: [menuItemKey],
            payload: { menuItemKey },
          },
          "retire the item"
        );
        toast("Item retired.", true);
        setTimeout(loadManageMenu, 700);
      } catch (e) {
        toast(e.message, false);
        btn.disabled = false;
      }
    });
  });
  body.querySelectorAll("[data-relocate]").forEach((btn) => {
    btn.addEventListener("click", async () => {
      const menuItemKey = btn.dataset.relocate;
      const locationKey = workplaceLocationKey();
      if (!locationKey) { toast("Your session carries no workplace to relocate this item to.", false); return; }
      btn.disabled = true;
      try {
        await opOrThrow(
          {
            operationType: "SetMenuItemLocation", class: "menuitem",
            reads: [menuItemKey, locationKey],
            payload: { menuItemKey, newLocation: locationKey },
          },
          "relocate the item"
        );
        toast("Item relocated to your workplace.", true);
        setTimeout(loadManageMenu, 700);
      } catch (e) {
        toast(e.message, false);
        btn.disabled = false;
      }
    });
  });
}

// ---- Resident view ------------------------------------------------
//
// A resident's own lease is resolved from /api/leases, which the server
// already scopes to the signed-in identity (persona-worlds-design.md Fire W4
// §3) — no client-side identity picker needed. A staff session instead sees
// the full lease picker, for front-of-house lookups.

let residentOwnLeaseAppKey = "";

// pendingCafeCharges holds Charge ops this session just submitted whose
// cafeTabs projection hasn't landed yet (measured live at up to ~40s) — a
// naive re-fetch right after would show the tab's pre-charge totalCents
// unchanged, so a resident sees their new item vanish for the price of a
// misleading $0.00-look flash right after the success toast. Each entry
// clears itself the next time renderResident() observes the tab's real
// totalCents move past what it was when the charge went in.
let pendingCafeCharges = []; // { tabKey, name, priceCents, baselineTotalCents }

async function loadResident() {
  const select = document.getElementById("resident-lease");
  const label = document.getElementById("resident-lease-label");
  const leases = await loadLeases();
  if (isFrontDesk()) {
    label.hidden = false;
    select.hidden = false;
    const ctx = await loadLeasePickerContext();
    fillLeaseSelect(select, leases, ctx.residentsByLease, ctx.leaseDetailsByLease, ctx.approvedByLease);
  } else {
    label.hidden = true;
    select.hidden = true;
    residentOwnLeaseAppKey = leases.length ? leases[0].leaseAppKey : "";
  }
  await renderResident();
}

async function renderResident() {
  const body = document.getElementById("resident-body");
  const selfMode = !isFrontDesk();
  const leaseAppKey = selfMode ? residentOwnLeaseAppKey : document.getElementById("resident-lease").value;
  body.innerHTML = "";
  if (!leaseAppKey) {
    body.innerHTML = selfMode
      ? '<div class="empty">No lease found for your identity yet.</div>'
      : '<div class="empty">Pick a lease to view its house-tab history.</div>';
    return;
  }
  let ledger, tabs, menu;
  try {
    const fetches = [
      appGet("/api/ledger?leaseAppKey=" + encodeURIComponent(leaseAppKey)),
      appGet("/api/tabs?leaseAppKey=" + encodeURIComponent(leaseAppKey)),
    ];
    if (selfMode) fetches.push(appGet("/api/menu?leaseAppKey=" + encodeURIComponent(leaseAppKey)));
    const results = await Promise.all(fetches);
    ledger = results[0];
    tabs = results[1];
    menu = results[2];
  } catch (e) {
    body.innerHTML = '<div class="empty">' + e.message + "</div>";
    return;
  }
  const open = (tabs.tabs || []).find((t) => t.status === "open");
  const pendingSettled = (tabs.tabs || []).find((t) => t.status === "settled" && !t.posted);
  pendingCafeCharges = open
    ? pendingCafeCharges.filter((p) => p.tabKey === open.tabKey && p.baselineTotalCents === open.totalCents)
    : [];
  const openDisplayTotal = open ? open.totalCents + pendingCafeCharges.reduce((s, p) => s + p.priceCents, 0) : 0;
  const openDisplayLines = open
    ? (open.lines || []).concat(
        pendingCafeCharges.map((p, i) => ({ id: "pending-" + i, description: p.name, amountCents: p.priceCents, pending: true }))
      )
    : [];
  const parts = [];
  if (open) {
    parts.push(
      '<div class="panel"><h2>Open tab</h2><p class="amount">' + money(openDisplayTotal) +
      '</p><p class="meta">Opened ' + (open.openedAt || "?") + " — not yet settled</p>" +
      chargeLinesBlock(openDisplayLines, open.itemsMemo, null) + "</div>" +
      (selfMode ? '<div class="panel-actions" style="margin-top:-8px;"><button id="resident-settle-btn" class="danger">Settle My Tab</button></div>' : "")
    );
    if (selfMode) {
      const items = (menu && menu.menu) || [];
      parts.push(
        '<div class="panel" style="max-width:640px;">' +
        "<h2>Order</h2>" +
        (items.length
          ? '<form id="self-order-form" class="field-row">' +
            '<select id="self-order-item">' +
            items
              .map((it) => '<option value="' + it.menuItemKey + '">' + escapeHtml(it.name) + " — " + money(it.priceCents) + "</option>")
              .join("") +
            "</select>" +
            '<button id="self-order-submit" type="submit">Add to Tab</button>' +
            "</form>"
          : '<p class="meta">No menu items available yet.</p>') +
        "</div>"
      );
    }
  } else if (selfMode) {
    parts.push(
      '<div class="panel">' +
      "<h2>No open tab</h2>" +
      '<p class="lead">Start a house tab for your own lease.</p>' +
      '<div class="panel-actions"><button id="resident-open-tab-btn">Open Tab</button></div>' +
      "</div>"
    );
  }
  if (pendingSettled) {
    parts.push(
      '<div class="panel"><h2>Pending posting</h2><p class="amount">' + money(pendingSettled.totalCents) +
      '</p><p class="meta">Settled ' + (pendingSettled.settledAt || "?") + " — posting to the ledger shortly</p>" +
      chargeLinesBlock(pendingSettled.lines, pendingSettled.itemsMemo, null) + "</div>"
    );
  }
  const rows = ledger.transactions || [];
  parts.push(
    '<div class="panel" style="max-width:640px;">' +
    "<h2>Café ledger</h2>" +
    '<p class="ledger-balance">' + ledgerBalanceLine(ledger.balanceCents) + "</p>" +
    statementLine(ledger) +
    (rows.length
      ? '<ul class="ledger-list">' +
        rows
          .map(
            (r) =>
              '<li class="ledger-entry ' + r.type + '">' +
              (r.type === "debit" ? "+" : "−") + money(r.amountCents) +
              (r.memo ? " — " + escapeHtml(r.memo) : "") +
              " (" + r.postedAt + ")</li>"
          )
          .join("") +
        "</ul>"
      : '<p class="meta">No posted café charges yet.</p>') +
    // Front desk records a payment handed over at the counter (staff form,
    // unconfined by amount — cash/card is witnessed in person). A resident
    // instead pays down their OWN balance self-service (self-scoped
    // CreditCafeAccount — packages/cafe-ledger's consumer scope=self grant):
    // ownership + the amount cap are proven server-side against the
    // account's own heldFor→leaseapp→applicationFor topology and postedTo
    // history, so a forged accountKey or an over-balance amount only fails
    // closed — nothing here needs to be trusted client-side. Neither form
    // needs the account to exist first; that only matters for what to pay.
    (selfMode
      ? ledger.accountKey && (ledger.balanceCents || 0) > 0
        ? '<form id="self-pay-form" class="field-row" style="margin-top:14px;">' +
          '<input id="self-pay-amount" type="number" step="0.01" min="0.01" max="' +
          (ledger.balanceCents / 100).toFixed(2) +
          '" placeholder="Payment ($)" value="' + (ledger.balanceCents / 100).toFixed(2) + '" required />' +
          '<button id="self-pay-submit" type="submit">Pay</button>' +
          "</form>"
        : ""
      : ledger.accountKey
        ? '<form id="record-payment-form" class="field-row" style="margin-top:14px;">' +
          '<input id="record-payment-amount" type="number" step="0.01" min="0.01" placeholder="Payment ($)" required />' +
          '<input id="record-payment-memo" type="text" placeholder="Memo (optional)" />' +
          '<button id="record-payment-submit" type="submit">Record Payment</button>' +
          "</form>"
        : '<p class="meta" style="margin-top:14px;">No café account for this lease yet — nothing to credit.</p>') +
    "</div>"
  );
  body.innerHTML = parts.join("");
  // CreditCafeAccount is dual-grant (operator/frontOfHouse scope=any PLUS
  // resident scope=self — packages/cafe-ledger's own doc comment) and
  // carries ONE descriptor written in the SELF voice — but that does NOT
  // mean the descriptor can only ever drive the self leg: clinic-app's own
  // ClinicCreditAccount migration (Inc 3a, submitLedgerEntry) drives BOTH
  // legs off the SAME descriptor, toggling context.selfVoice per caller
  // (true -> {target: context.me}, false -> no authContext at all — exactly
  // what a staff submission already sends, buildAuthContext in
  // internal/descriptorform/form.mjs). This front-desk leg mirrors that
  // shape: selfVoice is always false here (this form only renders when
  // !selfMode, i.e., definitively staff — café has no shared self/staff
  // panel the way clinic's single patient-context view does, so there is no
  // per-click actingAsSelf() to compute the way clinic's does). context.me
  // is left unset: buildAuthContext ignores it whenever selfVoice is false,
  // and the only other thing it feeds is form.mjs's own targetField/me
  // read-fallback — harmless to skip since CreditCafeAccount declares no
  // OptionalReads probe that would need it
  // (packages/cafe-ledger/opmetas.go: Reads is {payload.accountKey} alone),
  // unlike clinic's patientIdentityKey(), café has no already-loaded,
  // cheap way to resolve "the lease's own resident identity" from this view.
  const paymentForm = document.getElementById("record-payment-form");
  if (paymentForm) {
    paymentForm.addEventListener("submit", async (ev) => {
      ev.preventDefault();
      const amountInput = document.getElementById("record-payment-amount");
      const memoInput = document.getElementById("record-payment-memo");
      const cents = parseDollars(amountInput.value);
      if (cents === null) { toast("Enter a payment amount greater than $0.", false); return; }
      const memo = memoInput.value.trim();
      const btn = document.getElementById("record-payment-submit");
      btn.disabled = true;
      try {
        await loadOpCatalogQuiet();
        const { renderOpForm } = await loadDescriptorform();
        const row = opCatalogCache && opCatalogCache.CreditCafeAccount;
        if (!row) throw new Error("this action is unavailable");
        const context = {
          target: ledger.accountKey,
          selfVoice: false,
          prefill: { amountCents: cents, memo: memo || undefined },
        };
        const handle = renderOpForm(row, context, document.createElement("div"));
        if (!handle) throw new Error("this action is unavailable");
        const { envelope, reveal } = await handle.submit();
        const reply = await submitCatalogOp(envelope, "record the payment");
        revealCeremonySecret(reveal, reply);
        toast("Recorded " + money(cents) + ".", true);
        setTimeout(renderResident, 700);
      } catch (e) {
        toast(e.message, false);
        btn.disabled = false;
      }
    });
  }
  // The self-pay path below (self-pay-form) uses the SAME descriptor with
  // selfVoice: true and context.me set — see the front-desk leg's own
  // comment above for how the two legs share it. The visible
  // #self-pay-amount input (its balance-capped `max` and prefilled value)
  // stays exactly as built above — unlike a plain migrated form, this
  // renders the descriptor into a detached mount that is never shown,
  // purely to assemble the envelope (payload, reads, authContext per
  // dispatch) from what was already typed into the visible field, mirroring
  // clinic-app's own submitLedgerEntry / wellness-app's own
  // submitBillingEntry.
  const selfPayForm = document.getElementById("self-pay-form");
  if (selfPayForm) {
    selfPayForm.addEventListener("submit", async (ev) => {
      ev.preventDefault();
      const amountInput = document.getElementById("self-pay-amount");
      const cents = parseDollars(amountInput.value);
      if (cents === null) { toast("Enter a payment amount greater than $0.", false); return; }
      const btn = document.getElementById("self-pay-submit");
      btn.disabled = true;
      try {
        await loadOpCatalogQuiet();
        const { renderOpForm } = await loadDescriptorform();
        const row = opCatalogCache && opCatalogCache.CreditCafeAccount;
        if (!row) throw new Error("this action is unavailable");
        const context = {
          target: ledger.accountKey,
          me: identityKey(),
          selfVoice: true,
          prefill: { amountCents: cents },
        };
        const handle = renderOpForm(row, context, document.createElement("div"));
        if (!handle) throw new Error("this action is unavailable");
        const { envelope, reveal } = await handle.submit();
        const reply = await submitCatalogOp(envelope, "pay your balance");
        revealCeremonySecret(reveal, reply);
        toast("Paid " + money(cents) + ".", true);
        setTimeout(renderResident, 700);
      } catch (e) {
        toast(e.message, false);
        btn.disabled = false;
      }
    });
  }
  if (selfMode) {
    const openBtn = document.getElementById("resident-open-tab-btn");
    if (openBtn) {
      openBtn.addEventListener("click", async () => {
        openBtn.disabled = true;
        try {
          await opOrThrow(
            {
              operationType: "OpenTab",
              class: "tab",
              reads: [leaseAppKey],
              optionalReads: [leaseAppKey + ".cafeOpenTab", applicationForOptionalRead(leaseAppKey), leaseAppKey + ".decision"],
              payload: { leaseAppKey },
            },
            "open the tab",
            true
          );
          toast("Tab opened.", true);
          setTimeout(renderResident, 700);
        } catch (e) {
          toast(e.message, false);
          openBtn.disabled = false;
        }
      });
    }
    const settleBtn = document.getElementById("resident-settle-btn");
    if (settleBtn) {
      settleBtn.addEventListener("click", async () => {
        settleBtn.disabled = true;
        try {
          await opOrThrow(
            {
              operationType: "Settle",
              class: "tab",
              reads: [open.tabKey, open.tabKey + ".status"],
              optionalReads: [applicationForOptionalRead(leaseAppKey), chargedToOptionalRead(open.tabKey, leaseAppKey)],
              payload: { tabKey: open.tabKey },
            },
            "settle the tab",
            true
          );
          toast("Tab settled — posting to the café ledger shortly.", true);
          setTimeout(renderResident, 700);
        } catch (e) {
          toast(e.message, false);
          settleBtn.disabled = false;
        }
      });
    }
    const orderForm = document.getElementById("self-order-form");
    if (orderForm) {
      orderForm.addEventListener("submit", async (ev) => {
        ev.preventDefault();
        const menuItemKey = document.getElementById("self-order-item").value;
        if (!menuItemKey) { toast("Pick an item first.", false); return; }
        const btn = document.getElementById("self-order-submit");
        btn.disabled = true;
        try {
          await opOrThrow(
            {
              operationType: "Charge",
              class: "tab",
              reads: [open.tabKey, open.tabKey + ".status", menuItemKey, menuItemKey + ".price"],
              optionalReads: [applicationForOptionalRead(leaseAppKey)],
              payload: { tabKey: open.tabKey, menuItemKey },
            },
            "add the item to your tab",
            true
          );
          const chosen = ((menu && menu.menu) || []).find((it) => it.menuItemKey === menuItemKey);
          pendingCafeCharges.push({
            tabKey: open.tabKey,
            name: chosen ? chosen.name : "New item",
            priceCents: chosen ? chosen.priceCents : 0,
            baselineTotalCents: open.totalCents,
          });
          toast("Added to your tab.", true);
          await renderResident();
          setTimeout(renderResident, 700);
        } catch (e) {
          toast(e.message, false);
          btn.disabled = false;
        }
      });
    }
  }
}

function escapeHtml(s) {
  const d = document.createElement("div");
  d.textContent = s;
  return d.innerHTML;
}

// ---- init --------------------------------------------------------

function init() {
  document.querySelectorAll(".tab").forEach((b) => {
    b.addEventListener("click", () => showView(b.dataset.view));
  });
  document.getElementById("pos-lease").addEventListener("change", renderPos);
  document.getElementById("pos-refresh").addEventListener("click", () => { leasesCache = null; loadPos(); });
  document.getElementById("frontdesk-refresh").addEventListener("click", loadFrontDesk);
  document.getElementById("menu-refresh").addEventListener("click", loadManageMenu);
  document.getElementById("add-menu-item-form").addEventListener("submit", async (ev) => {
    ev.preventDefault();
    const nameInput = document.getElementById("mi-name");
    const priceInput = document.getElementById("mi-price");
    const name = nameInput.value.trim();
    const cents = parseDollars(priceInput.value);
    if (!name) { toast("Enter a name for the item.", false); return; }
    if (cents === null) { toast("Enter a price greater than $0.", false); return; }
    const locationKey = workplaceLocationKey();
    if (!locationKey) { toast("Your session carries no workplace to serve this item from.", false); return; }
    const btn = document.getElementById("add-menu-item-submit");
    btn.disabled = true;
    try {
      await opOrThrow(
        {
          operationType: "CreateMenuItem", class: "menuitem",
          reads: [locationKey],
          payload: { name, priceCents: cents, locationKey },
        },
        "add the item"
      );
      toast("Added " + name + ".", true);
      nameInput.value = "";
      priceInput.value = "";
      setTimeout(loadManageMenu, 700);
    } catch (e) {
      toast(e.message, false);
    } finally {
      btn.disabled = false;
    }
  });
  document.getElementById("resident-lease").addEventListener("change", renderResident);
  document.getElementById("resident-refresh").addEventListener("click", () => { leasesCache = null; loadResident(); });
  document.getElementById("sign-out").addEventListener("click", signOut);

  // Who signed in decides every derived affordance (staff vs. resident), so
  // it has to be known before the first render that reads it.
  loadWhoami().then(() => {
    applyHatGating();
    showView(isFrontDesk() ? "pos" : "resident");
    if (state.identityId) loadIdentities();
    // Prefetched in parallel so the first descriptor-form render (void
    // charge / pay balance) does not itself pay for the catalog + module
    // round trips — both cache themselves for the page's lifetime. Mirrors
    // clinic-app/web/app.js's and wellness-app/web/app.js's own prefetch.
    loadOpCatalogQuiet();
    loadDescriptorform().catch(() => {});
  });
}

init();
