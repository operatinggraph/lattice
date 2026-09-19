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

// isOwnAccount answers whether the account held by bookerKey (a lease's
// resident, the bookerKey column of /api/residents) belongs to the signed-in
// viewer. Pure: reads state.identityId (the raw NanoID) and nothing else, and
// answers false when either side is missing — an unresolved roster row or a
// whoami that never answered must not hide a button from a staffer who is
// entitled to it. The courtesy half of packages/cafe-ledger's SelfClearing
// refusal: nobody clears their own debt from the desk, so the clearing verbs
// (Write off, Refund, Pay out) are withheld on the viewer's own row; the
// enforcement is the script's own heldFor → applicationFor walk, never this.
function isOwnAccount(bookerKey) {
  if (!bookerKey || !state.identityId) return false;
  return idOf(bookerKey) === state.identityId;
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

// settlePayEnvelope builds the exact opOrThrow envelope both desk "Settle &
// pay" buttons (the POS card and the Front Desk card) submit — one shared
// declaration so the two sites can never drift: paidCents is always the
// card's own totalCents, never a typed amount, which is the courtesy that
// keeps PaidMismatchesTab a stale-card refusal rather than something a
// visitor could construct.
// refusal-courtesy: Settle/TabNotOpen: hide — settlePayEnvelope is only called from the settle-pay-btn/settle-pay-<tabKey> click handlers, themselves only wired inside renderOpenTabCard/frontDeskCard, both only rendered on the `open` branch
// refusal-courtesy: Settle/PaidMismatchesTab: cap — paidCents is always the totalCents argument, the card's own total, so a mismatch only arises from a stale card (a self-order or void since render) and the refusal toast is the courtesy
// refusal-courtesy: Settle/UnservedLines: disable — both callers (renderOpenTabCard's settle-pay-btn and frontDeskCard's settle-pay-<tabKey>) render the button disabled while unservedLineCount(tab) > 0
function settlePayEnvelope(tabKey, leaseAppKey, totalCents) {
  return {
    operationType: "Settle", class: "tab",
    reads: [tabKey, tabKey + ".status"],
    optionalReads: [chargedToOptionalRead(tabKey, leaseAppKey)],
    payload: { tabKey: tabKey, paidCents: totalCents },
  };
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
//
// KNOWN_CATALOG_OPS lists every operationType this app ever reads off
// opCatalogCache — passed as `?types=` so the server point-reads just these
// rows instead of the whole cross-vertical bucket (~100 ops from every
// installed package, unrelated to café). A name missing here simply never
// appears in the cache, the same "not offered" outcome as a package that
// hasn't declared the op yet — a silent failure, so
// TestKnownCatalogOpsCoversEveryCacheRead (op_catalog_test.go) reads this
// file and fails the build when an `opCatalogCache.<Op>` read has no entry.
const KNOWN_CATALOG_OPS = ["CreditCafeAccount", "RefundCafeCharge", "PayoutCafeCredit"];
let opCatalogPromise = null;
let opCatalogCache = null;
async function loadOpCatalog() {
  if (!opCatalogPromise) {
    opCatalogPromise = appGet("/api/op-catalog?types=" + encodeURIComponent(KNOWN_CATALOG_OPS.join(","))).then(
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
    const err = new Error(`Could not ${what} — ${msg}`);
    // rejected marks this as a CONFIRMED non-commit — the Processor answered
    // and refused — as opposed to a transport/unknown throw, whose landing
    // is ambiguous (the write may have reached the Processor and committed
    // even though the reply never arrived). A caller that narrates the
    // throw path branches on it: a rejection's own message already says
    // exactly what happened and needs no "may have landed" hedge.
    err.rejected = true;
    throw err;
  }
  return reply || {};
}

// ---- formatting --------------------------------------------------------

function money(cents) {
  const n = (cents || 0) / 100;
  return "$" + n.toFixed(2);
}

// tabLimitOf reads a lease's effective house tab limit off its /api/residents
// row (tabLimitCents: the minimum policy over the lease's covering
// locations, null when none is recorded) — null means no limit, never $0.
function tabLimitOf(row) {
  return row && typeof row.tabLimitCents === "number" ? row.tabLimitCents : null;
}

// tabLimitRemaining is the room left under the house limit for a resident's
// open EXPOSURE at the house — the recorded ledger balance (balanceCents,
// signed: a credit widens the room) plus the tab's own totalCents, so a tab
// settled without paying still counts once its debit posts. null when the
// house records no limit. Clamped at 0:
// an exposure the desk rang past the limit leaves the resident no room, not
// a negative.
function tabLimitRemaining(limitCents, totalCents, balanceCents) {
  if (typeof limitCents !== "number") return null;
  return Math.max(0, limitCents - (balanceCents || 0) - (totalCents || 0));
}

// houseLimitLine is the one sentence every tab card says about the house
// limit: the limit, the recorded balance when it is non-zero ("owes …" for
// a debt, "in credit …" for a credit that widens the room), and the room
// left under it — or "at" / "over" the limit once the balance plus the tab
// (the same exposure tabLimitRemaining bounds) reaches or passes it (the
// desk can ring past it; the resident cannot). "" when the house records no
// limit.
function houseLimitLine(limitCents, totalCents, balanceCents) {
  if (typeof limitCents !== "number") return "";
  const total = totalCents || 0;
  const balance = balanceCents || 0;
  const exposure = balance + total;
  const balancePhrase = balance > 0 ? "owes " + money(balance) + " · " : balance < 0 ? "in credit " + money(-balance) + " · " : "";
  if (exposure > limitCents) return "Over the house limit of " + money(limitCents) + " — self-order is closed";
  if (exposure === limitCents) return "At the house limit of " + money(limitCents) + " — self-order is closed";
  return "House limit " + money(limitCents) + " · " + balancePhrase + money(limitCents - exposure) + " left";
}

// counterPaymentLine renders a settled tab's paidAtSettleCents (cash the
// desk took at the counter when it settled the tab — absent on every other
// settle) as the phrase every surface showing that tab tags it with, or ""
// when there is nothing to say: absent, or (defensively) zero.
function counterPaymentLine(tab) {
  const cents = tab && tab.paidAtSettleCents;
  if (!cents) return "";
  return "paid " + money(cents) + " at the counter";
}

// customerMemo strips a raw entity key from a ledger memo before it reaches
// a customer surface — a memo is free text an operator typed, so nothing
// stops one from embedding a bare NanoID (2026-08-29: a remediation memo did
// exactly that on a sibling app's statement). No ledger op can amend a
// posted memo (append-only entry, D5), so this is the durable fix even for
// already-posted lines.
function customerMemo(memo) {
  if (!memo) return memo;
  // derived-key: not a key derivation — this alphabet builds a regex to
  // recognize and STRIP a raw entity key from customer-facing text, no
  // hash/digest is computed and no key is produced.
  const nanoid = "[ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz123456789]{20}";
  return memo
    .replace(new RegExp("\\b(?:Appt|Session|Booking|Visit|Tab|Order)\\s+" + nanoid + "\\b\\.?", "gi"), "")
    .replace(new RegExp("\\b" + nanoid + "\\b", "g"), "")
    .replace(/\s{2,}/g, " ")
    .trim();
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
// note or a red overdue banner — "" when there's nothing owed to age. An
// overdue banner also says whether the arrears reminder (the Weaver-
// dispatched convergence job that reaches the resident, joined onto this
// row's reminderSentAt) has gone out yet.
function statementLine(ledger) {
  if (!ledger.dueDate) return "";
  const due = new Date(ledger.dueDate).toLocaleDateString();
  if (ledger.isOverdue) {
    const days = Number(ledger.daysOverdue) || 0;
    const reminder = ledger.reminderSentAt
      ? " · reminder sent " + new Date(ledger.reminderSentAt).toLocaleDateString()
      : " · no reminder sent yet";
    return (
      '<p class="ledger-overdue" style="color:#b00020;font-weight:600;">' +
      "OVERDUE — " + days + (days === 1 ? " day" : " days") + " past due (was due " + due + ")" + reminder +
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
// tabStatus aspect — one {id, description, amountCents, voided, orderedBy,
// orderedAt, servedAt?, servedBy?} entry per Charge, the structured twin of
// the flat itemsMemo string) as a
// priced list, a voided line struck through and labeled rather than hidden. A
// tab whose lines is empty or absent (predates the field, or nothing charged
// yet) falls back to the flat itemsMemo line — the only place old and new
// tabs still look the same. voidableTabKey, when given, adds a per-line
// Void action (wired by the caller after insertion) — staff POS only, since
// VoidCharge grants no self-service scope — and, on a to-make line while the
// tab is open, a Mark served action before it (renderPos wires both). A
// synthetic {pending: true} line (renderResident's own optimistic overlay,
// not real cafeTabs data) renders muted and labeled instead of getting either
// button. A live (not voided, not pending) line also carries a state tag —
// "to make" while orderedAt is set and servedAt isn't AND the tab is still
// open (tabOpen; a settled tab's unserved line is done, whatever its stamps
// say, so a receipt never asks for it to be made), "served" once servedAt
// lands, nothing for a line that predates both fields — so a resident sees
// their own order's state on their own card, same as the desk does on
// POS/Front Desk. A voided line whose voidedReason is "unserved" (the 24 h
// sweep's own void, never a desk void) reads "(voided — never made)" instead
// of "(voided)".
function chargeLinesBlock(lines, memo, voidableTabKey, tabOpen) {
  if (!lines || !lines.length) return itemsMemoLine(memo);
  return (
    '<ul class="items-list">' +
    lines
      .map(
        (l) =>
          '<li class="item-line' + (l.voided ? " voided" : "") + (l.pending ? " pending" : "") + '">' +
          '<span class="item-desc">' + escapeHtml(l.description) + orderedByLabel(l.orderedBy) +
          (tabOpen && !l.voided && !l.pending && l.orderedAt && !l.servedAt ? ' <span class="meta">· to make</span>' : "") +
          (!l.voided && !l.pending && l.servedAt ? ' <span class="meta">· served</span>' : "") +
          "</span>" +
          '<span class="item-amount">' + money(l.amountCents) + "</span>" +
          (l.voided
            ? '<span class="meta">' + (l.voidedReason === "unserved" ? "(voided — never made)" : "(voided)") + "</span>"
            : l.pending
            ? '<span class="meta">(pending)</span>'
            : voidableTabKey
            ? (tabOpen && l.orderedAt && !l.servedAt
                ? '<button type="button" class="ghost" data-serve-line="' + escapeHtml(l.id) + '">Mark served</button>'
                : "") +
              '<button type="button" class="ghost" data-void-line="' + escapeHtml(l.id) + '">Void</button>'
            : "") +
          "</li>"
      )
      .join("") +
    "</ul>"
  );
}

// menuOptions renders a picker's <option>/<optgroup> markup from a catalog
// row list (/api/menu's own shape — menuItemKey/name/priceCents/available):
// every available item first (so the default selection a browser picks is
// always one that can actually be ordered), then every sold-out item as a
// disabled option inside one "Sold out today" optgroup, labeled " — sold
// out". Shared by the self-order and POS pickers (renderResident,
// renderOpenTabCard) so the two forms never drift.
// menuOptions renders the picker's options. remainingCents, when a number,
// is the room left under the house tab limit for the resident's open
// EXPOSURE (tabLimitRemaining: limit − balance − total): an available item
// priced above it is rendered disabled inside an "Over your tab limit"
// optgroup — the resident-self Charge's own TabLimitExceeded conjunct
// (balanceCents + totalCents + amountCents > limit, packages/cafe-domain/
// ddls.go), applied to the same item price the op derives. The POS picker
// passes no bound: the staff leg is never limited.
function menuOptions(items, remainingCents) {
  const bounded = typeof remainingCents === "number";
  const available = items.filter((it) => it.available !== false && !(bounded && it.priceCents > remainingCents));
  const overLimit = items.filter((it) => it.available !== false && bounded && it.priceCents > remainingCents);
  const soldOut = items.filter((it) => it.available === false);
  let html = available
    .map((it) => '<option value="' + escapeHtml(it.menuItemKey) + '">' + escapeHtml(it.name) + " — " + money(it.priceCents) + "</option>")
    .join("");
  if (overLimit.length) {
    html +=
      '<optgroup label="Over your tab limit">' +
      overLimit
        .map((it) => '<option value="' + escapeHtml(it.menuItemKey) + '" disabled>' + escapeHtml(it.name) + " — " + money(it.priceCents) + " — over the limit</option>")
        .join("") +
      "</optgroup>";
  }
  if (soldOut.length) {
    html +=
      '<optgroup label="Sold out today">' +
      soldOut
        .map((it) => '<option value="' + escapeHtml(it.menuItemKey) + '" disabled>' + escapeHtml(it.name) + " — " + money(it.priceCents) + " — sold out</option>")
        .join("") +
      "</optgroup>";
  }
  return html;
}

// receiptLines joins a ledger row to the settled tab it came from (by
// tabKey, keyed into tabByKey from /api/tabs) so a posted charge can show
// what it was for — {lines, memo} for chargeLinesBlock, or null when there
// is nothing to join (no tabKey, or the tab hasn't resolved yet: a
// projection still catching up, or a tab whose row was pruned). The join is
// by KEY alone, never by row type: the counter payment the settlement
// playbook posts (a credit carrying the same settles hop, so cafeLedgerHistory
// projects its tabKey too) joins its tab's lines the same way, and the
// statement shows what the cash paid for.
function receiptLines(row, tabByKey) {
  if (!row || !row.tabKey) return null;
  const tab = tabByKey[row.tabKey];
  if (!tab) return null;
  return { lines: tab.lines || [], memo: tab.itemsMemo || "" };
}

// parseDollars turns a user-entered dollar string ("4.50") into integer
// cents, or null when it isn't a positive amount.
function parseDollars(s) {
  const n = Number(s);
  if (!isFinite(n) || n <= 0) return null;
  return Math.round(n * 100);
}

// parseDollarsOrZero is parseDollars for a field where 0 is a value, not a
// blank — the house tab limit, where $0 closes self-service tabs. A blank,
// non-numeric or negative entry is null.
function parseDollarsOrZero(s) {
  if (typeof s !== "string" || s.trim() === "") return null;
  const n = Number(s);
  if (!isFinite(n) || n < 0) return null;
  const cents = Math.round(n * 100);
  // The op takes whole cents; a fraction of a cent is refused here rather
  // than rounded into a limit the desk never typed.
  if (Math.abs(n * 100 - cents) > 1e-6) return null;
  return cents;
}

// rentAmount formats a lease's unit rent — a plain dollar amount (not
// cents, unlike money()'s café-ledger amounts) with its currency code.
function rentAmount(amount, currency) {
  return "$" + Number(amount).toFixed(0) + " " + escapeHtml(currency || "");
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

// frontDeskOrdersTimer re-reads /api/tabs and repaints the Orders panel
// every 15 s while the Front Desk view is showing (loadFrontDesk itself only
// runs on entry, on Refresh, and after an op) — cleared whenever the view
// changes, so it never stacks. frontDeskServesInFlight counts the Mark served
// clicks awaiting a reply; while it is non-zero the poll holds its repaint, so
// it cannot replace a disabled button with a fresh one mid-submit.
// frontDeskOrdersPainted is the signature of the rows last painted: a poll
// that reads the same queue leaves the DOM (and keyboard focus) alone.
let frontDeskOrdersTimer = null;
let frontDeskServesInFlight = 0;
let frontDeskOrdersPainted = "";

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
  if (frontDeskOrdersTimer) {
    clearInterval(frontDeskOrdersTimer);
    frontDeskOrdersTimer = null;
  }
  if (view === "pos") loadPos();
  else if (view === "frontdesk") {
    loadFrontDesk();
    frontDeskOrdersTimer = setInterval(async () => {
      if (frontDeskServesInFlight || document.hidden) return;
      try {
        const r = await appGet("/api/tabs");
        if (!frontDeskServesInFlight) renderFrontDeskOrders(r.tabs || []);
      } catch (_) { /* transient poll failure — the next tick or a Refresh recovers */ }
    }, 15000);
  } else if (view === "menu") loadManageMenu();
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
// degrade-to-lease-key join frontDeskCard already does. Also resolves
// openTabByLease (/api/tabs, no leaseAppKey — every tab this hat can see) so
// fillLeaseSelect's rankLeaseOptions can put a lease with money on the
// counter right now at the top of the picker; loaded here, before either
// caller renders, so the picker is ranked on its very first paint.
async function loadLeasePickerContext() {
  let residentsByLease = {};
  let approvedByLease = {};
  const tabLimitByLease = {};
  try {
    const rs = await appGet("/api/residents");
    (rs.residents || []).forEach((r) => {
      residentsByLease[r.leaseAppKey] = r.bookerKey;
      approvedByLease[r.leaseAppKey] = r.approved;
      tabLimitByLease[r.leaseAppKey] = tabLimitOf(r);
    });
  } catch (_) { /* residents roster unreachable — picker falls back to the lease key */ }
  let leaseDetailsByLease = {};
  try {
    const ld = await appGet("/api/frontdesk-lease-details");
    (ld.leaseDetails || []).forEach((d) => { leaseDetailsByLease[d.leaseAppKey] = d; });
  } catch (_) { /* front-desk not installed / unreachable — unit address just doesn't show */ }
  const openTabByLease = {};
  try {
    const ts = await appGet("/api/tabs");
    (ts.tabs || []).forEach((t) => { if (t.status === "open") openTabByLease[t.leaseAppKey] = true; });
  } catch (_) { /* tabs unreachable — picker just isn't ranked by open-tab */ }
  // Each lease's house-tab balance (/api/frontdesk-balances — at most one row
  // per leaseAppKey, only non-zero balances), the picker's arrears badge and
  // the Open Tab gate's input. Best-effort, same degrade-to-hidden posture.
  const { balances, balancesByLease } = await loadBalances();
  return { residentsByLease, approvedByLease, leaseDetailsByLease, balances, balancesByLease, tabLimitByLease, openTabByLease };
}

// loadBalances reads /api/frontdesk-balances into both the list the arrears
// panel renders and a by-lease map for the pickers and the Open Tab gate. An
// unreachable endpoint yields empty structures rather than throwing: a
// balance that cannot be read is not evidence of a debt, so no badge shows
// and no tab is held client-side (the op still refuses a real hold).
async function loadBalances() {
  let balances = [];
  const balancesByLease = {};
  try {
    const bal = await appGet("/api/frontdesk-balances");
    balances = bal.balances || [];
    balances.forEach((b) => { balancesByLease[b.leaseAppKey] = b; });
  } catch (_) { /* balances unreachable — badge + arrears list just don't show */ }
  return { balances, balancesByLease };
}

// rankLeaseOptions orders a lease picker's rows the way a desk wants to scan
// them: a lease with an open tab first (there is money on the counter right
// now), then every other billable lease, and every non-billable lease
// (disabled — awaiting landlord approval, or a lease that ended) trailing,
// never mixed with the leases the desk can actually act on. Within each of
// those three groups, sorted by the rendered resident name (who,
// case-insensitive) and then by lease key, so two residents sharing a name
// still land in a stable order. Pure — no DOM — so a goja pin can prove the
// ranking without fillLeaseSelect's markup. rows: {leaseAppKey, who,
// disabled, open, label}.
function rankLeaseOptions(rows) {
  const bucket = (r) => (r.disabled ? 2 : r.open ? 0 : 1);
  return rows.slice().sort((a, b) => {
    const ba = bucket(a);
    const bb = bucket(b);
    if (ba !== bb) return ba - bb;
    const wa = (a.who || "").toLowerCase();
    const wb = (b.who || "").toLowerCase();
    if (wa !== wb) return wa < wb ? -1 : 1;
    if (a.leaseAppKey === b.leaseAppKey) return 0;
    return a.leaseAppKey < b.leaseAppKey ? -1 : 1;
  });
}

// renderLeaseOptions rebuilds a lease picker's <option> elements from an
// already-ranked row list ({leaseAppKey, who, disabled, open, label}) —
// shared by fillLeaseSelect (the full roster) and applyLeaseSearch (a
// query-narrowed subset), so search renders the SAME markup a plain load
// would: an "● " marker on an open-tab option (already folded into the
// row's own label), every disabled row inside a trailing "Not billable"
// optgroup. prevValue is reselected when it is still among rows; otherwise
// no option is marked selected, and the select's own default-selection
// behavior (the HTML spec's own rule for a <select> with no <option
// selected>) picks the first non-disabled option.
function renderLeaseOptions(select, rows, prevValue) {
  select.innerHTML = "";
  if (!rows.length) {
    const opt = document.createElement("option");
    opt.textContent = "(no leases)";
    opt.value = "";
    select.appendChild(opt);
    return;
  }
  let group = null;
  for (const row of rows) {
    const opt = document.createElement("option");
    opt.value = row.leaseAppKey;
    opt.textContent = row.label;
    opt.disabled = row.disabled;
    if (row.disabled) {
      if (!group) {
        group = document.createElement("optgroup");
        group.label = "Not billable";
        select.appendChild(group);
      }
      group.appendChild(opt);
    } else {
      select.appendChild(opt);
    }
  }
  if (prevValue && rows.some((r) => r.leaseAppKey === prevValue)) select.value = prevValue;
}

// fillLeaseSelect renders every pickable lease, ranked by rankLeaseOptions:
// a lease with an open tab (openTabByLease[leaseAppKey] true) first, marked
// with a leading "● ", then every other billable lease by resident name,
// then every disabled lease inside a trailing "Not billable" optgroup, so
// the common case (act on a lease) never has to be scrolled past the rare
// one (a lease nothing can be done with yet). The ranked rows are stashed
// on the select itself (select._leaseRows) so a type-to-find query
// (applyLeaseSearch) can narrow them without re-fetching, and — when the
// picker already carries a query (a Refresh mid-search) — that query is
// re-applied immediately rather than silently dropping back to the
// unfiltered list.
//
// When gateOnApproval is true a lease the landlord hasn't approved yet
// (approvedByLease[leaseAppKey] === false) is disabled — OpenTab itself
// rejects LeaseNotApproved, but a disabled, badged option tells staff why
// before they even try, instead of a raw error toast. A lease this app
// can't resolve approval for (roster unreachable, or a lease absent from
// /api/residents entirely) stays selectable — this picker only blocks on
// POSITIVE evidence of non-approval, never on its absence. Callers that
// don't gate on OpenTab (e.g. the Resident tab's front-desk picker, which
// only reaches ledger viewing and Record Payment) pass gateOnApproval=false
// so an unapproved lease's debt stays collectable. The same gate disables a
// lease whose tenancy has ended (tenancyEnded — OpenTab refuses TenancyEnded
// past the term the front-desk lens projects), again only on positive
// evidence: a lease with no projected term stays selectable. Every option
// also carries arrearsBadge(balancesByLease[leaseAppKey]) — a debtor is
// never disabled here, since picking the lease is how the desk sees why it
// is held and where to take the payment.
function fillLeaseSelect(select, leases, residentsByLease, leaseDetailsByLease, approvedByLease, gateOnApproval, balancesByLease, openTabByLease) {
  const prev = select.value;
  const rows = leases.map((l) => {
    const bookerKey = residentsByLease && residentsByLease[l.leaseAppKey];
    const who = bookerKey ? nameForIdentity(idOf(bookerKey)) : shortKey(l.leaseAppKey);
    const detail = leaseDetailsByLease && leaseDetailsByLease[l.leaseAppKey];
    const unit = detail && detail.unitAddress ? " — " + detail.unitAddress : "";
    const approved = approvedByLease && approvedByLease[l.leaseAppKey];
    let disabled = false;
    let label;
    if (gateOnApproval && approved === false) {
      disabled = true;
      label = who + unit + " (awaiting landlord approval)";
    } else if (gateOnApproval && tenancyEnded(detail, new Date())) {
      disabled = true;
      // The calendar date of the UTC stamp, the same slice the refusal
      // toasts — a local rendering of a midnight-UTC term end names the
      // day before in every zone west of Greenwich. Names the recorded
      // endedAt (an early move-out, or the term run out) when set; else
      // falls back to the projected leaseEnd.
      const endDate = detail.endedAt || detail.leaseEnd;
      label = who + unit + " (lease ended " + endDate.slice(0, 10) + ")";
    } else {
      label = who + unit + (l.accountKey ? "" : " (no café account yet)");
    }
    label += arrearsBadge(balancesByLease && balancesByLease[l.leaseAppKey]);
    const open = !!(openTabByLease && openTabByLease[l.leaseAppKey]);
    if (open) label = "● " + label;
    return { leaseAppKey: l.leaseAppKey, who, disabled, open, label };
  });
  const ranked = rankLeaseOptions(rows);
  select._leaseRows = ranked;
  renderLeaseOptions(select, ranked, prev);
  const input = leaseSearchInputBySelect.get(select);
  if (input && input.value.trim()) applyLeaseSearch(select, input);
}

// tenancyEnded reports whether a lease-details row's tenancy has ended —
// the same fact OpenTab's TenancyEnded guard refuses on
// (packages/cafe-domain/ddls.go). A recorded endedAt (an early move-out via
// GiveNotice, or the term simply running out) is the FACT the tenancy
// ended, checked first and unconditionally — it is only ever written once
// already reached, so its mere presence is evidence enough, no `now`
// comparison needed. Absent that, falls back to leaseEnd having been
// reached by `now`, the same inclusive boundary the op applies for a lease
// EndTenancy has not caught up with yet. A row with neither, or one that
// does not parse, is not evidence the term ended.
function tenancyEnded(detail, now) {
  if (!detail) return false;
  if (detail.endedAt) return true;
  if (!detail.leaseEnd) return false;
  const end = new Date(detail.leaseEnd).getTime();
  if (isNaN(end)) return false;
  return now.getTime() >= end;
}

// leaseSearchQuery narrows a ranked row list to the rows whose rendered
// label contains `query` as a case-insensitive substring (name or unit are
// both part of the label) — an empty or whitespace-only query matches
// every row. Pure — no DOM — so a goja pin can prove the filter without
// exercising renderLeaseOptions.
function leaseSearchQuery(rows, query) {
  const q = (query || "").trim().toLowerCase();
  if (!q) return rows;
  return rows.filter((r) => r.label.toLowerCase().includes(q));
}

// leaseSearchInputBySelect associates each lease picker's <select> with its
// own type-to-find <input>, so fillLeaseSelect (a fresh load or a Refresh)
// can re-apply whatever query is already typed instead of silently
// dropping back to the unfiltered list.
const leaseSearchInputBySelect = new Map();

// applyLeaseSearch rebuilds `select`'s options from its own ranked row list
// (select._leaseRows, stashed by fillLeaseSelect) narrowed by `input`'s
// current query, via renderLeaseOptions — rebuilding the option elements
// rather than hiding them, since `option.hidden` / `optgroup.hidden` is a
// no-op inside a native <select> popup on WebKit. When the option that was
// selected is not among the filtered rows, renderLeaseOptions leaves no
// option marked selected, so the select's own default-selection behavior
// (no <option selected> ⇒ the first non-disabled option) picks the next
// one — detected here by comparing select.value before and after, which
// then fires the select's own "change" handler so the rendered card never
// lags what the picker shows.
function applyLeaseSearch(select, input) {
  const rows = select._leaseRows || [];
  const filtered = leaseSearchQuery(rows, input.value);
  const prev = select.value;
  renderLeaseOptions(select, filtered, prev);
  if (select.value !== prev) select.dispatchEvent(new Event("change"));
}

// wireLeaseSearch wires a lease picker's type-to-find input to narrow its
// select by name or unit as the desk types — no debounce: a client-side
// filter over at most a few hundred already-ranked rows runs well within a
// keystroke.
function wireLeaseSearch(input, select) {
  leaseSearchInputBySelect.set(select, input);
  input.addEventListener("input", () => applyLeaseSearch(select, input));
}

// openTabGate decides what a lease's Open Tab control does from its balance
// row — /api/frontdesk-balances and /api/ledger carry the same
// balanceCents/isOverdue/daysOverdue/reminderSentAt fields, so the POS and
// the resident view share this one rule:
//   "hold"    — a reminder has gone out for the current arrears episode
//               (reminderSentAt, cafe-ledger's .arrears.sentAt). OpenTab
//               refuses CreditHold on exactly this condition, on the staff
//               and resident legs alike, so the control renders the reason
//               instead of a button that could only toast the refusal.
//               Binds regardless of isOverdue: a part-payment can move the
//               FIFO head inside its term while the episode's reminder
//               stands, and the op holds until the balance clears.
//   "confirm" — overdue, not yet reminded. The op accepts; the desk (or the
//               resident) is asked first, the wellness/clinic desk courtesy.
//   "open"    — nothing owed, or owed inside its term. No row at all is a
//               lease with a zero balance.
function openTabGate(balance) {
  if (balance && balance.reminderSentAt) return "hold";
  if (balance && balance.isOverdue) return "confirm";
  return "open";
}

// overdueDaysPhrase renders a balance row's daysOverdue as "N days overdue"
// (singular at one), the phrase the pickers, confirms and hold panels share.
function overdueDaysPhrase(balance) {
  const days = Number(balance && balance.daysOverdue) || 0;
  return days + (days === 1 ? " day" : " days") + " overdue";
}

// arrearsBadge is the lease-picker suffix for a balance row: "" when the
// gate is open, else " · owes $X" + " · N days overdue" (when overdue) +
// " · credit hold" (when held). Plain text — it lands in option.textContent.
function arrearsBadge(balance) {
  const gate = openTabGate(balance);
  if (gate === "open") return "";
  return (
    " · owes " + money(balance.balanceCents) +
    (balance.isOverdue ? " · " + overdueDaysPhrase(balance) : "") +
    (gate === "hold" ? " · credit hold" : "")
  );
}

// localDateTime renders an RFC3339 instant (a tab's openedAt / settledAt, a
// class or visit's startsAt) as the viewer's local date and time — the same
// localization the due dates on the same cards already get, so a card never
// mixes a localized due date with a raw UTC timestamp. "?" when the stamp is
// absent or does not parse.
function localDateTime(iso) {
  if (!iso) return "?";
  const d = new Date(iso);
  return isNaN(d.getTime()) ? "?" : d.toLocaleString([], { dateStyle: "medium", timeStyle: "short" });
}

// reminderSentDate renders a balance row's reminderSentAt as a local
// calendar date for the hold copy ("?" when the stamp does not parse).
function reminderSentDate(balance) {
  const d = new Date(balance.reminderSentAt);
  return isNaN(d.getTime()) ? "?" : d.toLocaleDateString();
}

// confirmOverdueOpen asks before opening a tab on an overdue-but-unreminded
// balance: true when the caller may proceed (the gate is not "confirm", or
// the desk/resident accepted). `who` is "You" on the resident's own leg.
function confirmOverdueOpen(who, balance) {
  if (openTabGate(balance) !== "confirm") return true;
  const owes = who === "You" ? "You owe " : who + " owes ";
  return window.confirm(owes + money(balance.balanceCents) + ", " + overdueDaysPhrase(balance) + ". Open a tab anyway?");
}

// ---- POS view (staff only) --------------------------------------------

// posResidentsByLease is the POS picker's lease → resident identity join,
// kept from the last loadPos so renderPos can name the debtor in the hold
// panel and the overdue confirm without re-reading the roster per render.
let posResidentsByLease = {};

async function loadPos() {
  const select = document.getElementById("pos-lease");
  const [leases, ctx] = await Promise.all([loadLeases(), loadLeasePickerContext()]);
  posResidentsByLease = ctx.residentsByLease;
  posTabLimitByLease = ctx.tabLimitByLease;
  fillLeaseSelect(select, leases, ctx.residentsByLease, ctx.leaseDetailsByLease, ctx.approvedByLease, true, ctx.balancesByLease, ctx.openTabByLease);
  await renderPos();
}

// refusal-courtesy: OpenTab/CreditHold: hide — renderCreditHoldPanel replaces the Open Tab button when openTabGate(balance) === "hold"
// refusal-courtesy: OpenTab/InvalidState: none — the arrears aspect's wrong class is a data-integrity fault (require_no_credit_hold, packages/cafe-domain/ddls.go), not state loadBalances exposes
// refusal-courtesy: Charge/InvalidState: none — the café account's balance aspect of the wrong class is a data-integrity fault (house_exposure_balance, packages/cafe-domain/ddls.go), not state loadBalances exposes
// refusal-courtesy: OpenTab/LeaseNotApproved, TenancyEnded: disable — fillLeaseSelect (loadPos, gateOnApproval=true) disables the lease option when approvedByLease is false or tenancyEnded(detail, now) is true
// refusal-courtesy: OpenTab/OpenTabAlreadyExists: hide — the Open Tab button only renders on the `!open` branch (tabs.find(t => t.status === "open") absent)
// refusal-courtesy: Charge/ItemUnavailable: disable — menuOptions renders a sold-out item (available === false) inside a disabled "Sold out today" optgroup
// refusal-courtesy: Charge/TabNotOpen: hide — the catalog/off-menu charge forms only render inside renderOpenTabCard, itself only rendered on the `open` branch
// refusal-courtesy: Charge/TabLimitExceeded: unreachable — the POS forms submit on the staff leg (no authContext), and the script applies the exposure bound (balanceCents + totalCents + amountCents > limit) only when is_self (packages/cafe-domain/ddls.go); the desk is warned by renderOpenTabCard's houseLimitLine, never refused
// refusal-courtesy: OpenTab/TabLimitExceeded: unreachable — open-tab-btn submits on the staff leg (no authContext), and the script applies the closed-house (limit 0) and over-exposure (balanceCents >= limit) checks only inside the authContextTarget branch (packages/cafe-domain/ddls.go)
// refusal-courtesy: Settle/TabNotOpen: hide — settle-btn only renders inside renderOpenTabCard, itself only rendered on the `open` branch
// refusal-courtesy: Settle/PaidMismatchesTab: see settlePayEnvelope
// refusal-courtesy: Settle/UnservedLines: disable — renderOpenTabCard renders settle-btn/settle-pay-btn disabled while unservedLineCount(tab) > 0, with the count as the hint
// refusal-courtesy: VoidCharge/TabNotOpen: hide — the void buttons (chargeLinesBlock's data-void-line) only render inside renderOpenTabCard, itself only rendered on the `open` branch
// refusal-courtesy: MarkLineServed/TabNotOpen: hide — the Mark served buttons (chargeLinesBlock's data-serve-line) only render inside renderOpenTabCard, itself only rendered on the `open` branch
// refusal-courtesy: MarkLineServed/LineVoided, LineAlreadyServed: hide — chargeLinesBlock renders data-serve-line only on a line with !voided && orderedAt && !servedAt
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
  // A selection restored onto an option the picker disabled (awaiting
  // approval, lease ended) shows why instead of an Open Tab form the server
  // would refuse.
  const picked = document.getElementById("pos-lease").selectedOptions[0];
  if (picked && picked.disabled) {
    body.innerHTML = '<div class="empty">' + escapeHtml(picked.textContent) + "</div>";
    return;
  }
  let tabs, menu, balances;
  try {
    // The balance is re-read per render (not taken from the picker's load)
    // so a payment just taken on the Front Desk tab lifts the hold here on
    // the next render without a picker refresh.
    const results = await Promise.all([
      appGet("/api/tabs?leaseAppKey=" + encodeURIComponent(leaseAppKey)),
      appGet("/api/menu?leaseAppKey=" + encodeURIComponent(leaseAppKey)),
      loadBalances(),
    ]);
    tabs = results[0].tabs || [];
    menu = results[1];
    balances = results[2].balancesByLease;
  } catch (e) {
    body.innerHTML = '<div class="empty">' + escapeHtml(e.message) + "</div>";
    return;
  }
  const open = tabs.find((t) => t.status === "open");
  if (!open) {
    const balance = balances[leaseAppKey];
    const bookerKey = posResidentsByLease[leaseAppKey];
    const who = bookerKey ? nameForIdentity(idOf(bookerKey)) : shortKey(leaseAppKey);
    if (openTabGate(balance) === "hold") {
      body.innerHTML = renderCreditHoldPanel(who, balance);
      return;
    }
    body.innerHTML = renderOpenTabForm();
    document.getElementById("open-tab-btn").addEventListener("click", async () => {
      if (!confirmOverdueOpen(who, balance)) return;
      const btn = document.getElementById("open-tab-btn");
      btn.disabled = true;
      try {
        await opOrThrow(
          {
            operationType: "OpenTab",
            class: "tab",
            reads: [leaseAppKey],
            optionalReads: [leaseAppKey + ".cafeOpenTab", leaseAppKey + ".decision", leaseAppKey + ".tenancy"],
            enumerations: [{ hub: leaseAppKey, relation: "heldFor", direction: "in" }],
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
  const openBalance = balances[leaseAppKey];
  body.innerHTML = renderOpenTabCard(open, items, posTabLimitByLease[leaseAppKey], (openBalance && openBalance.balanceCents) || 0);
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
  body.querySelectorAll("[data-serve-line]").forEach((btn) => {
    btn.addEventListener("click", async () => {
      if (btn.disabled) return;
      const lineId = btn.dataset.serveLine;
      btn.disabled = true;
      try {
        await opOrThrow(
          {
            operationType: "MarkLineServed", class: "tab",
            reads: [open.tabKey, open.tabKey + ".status"],
            payload: { tabKey: open.tabKey, lineId },
          },
          "mark the order served"
        );
        toast("Served.", true);
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
    if (btn.disabled) return;
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
      // An UnservedLines refusal means a self-order landed since this card
      // rendered — the same stale-card idiom as the settle-pay-btn's own
      // catch below: re-render so the desk sees the Mark served button /
      // hint instead of a dead click that refuses the same way again.
      if (e.message && e.message.indexOf("UnservedLines") !== -1) {
        setTimeout(renderPos, 700);
      }
    }
  });
  const settlePayBtn = document.getElementById("settle-pay-btn");
  if (settlePayBtn) {
    settlePayBtn.addEventListener("click", async () => {
      if (settlePayBtn.disabled) return;
      settlePayBtn.disabled = true;
      try {
        await opOrThrow(settlePayEnvelope(open.tabKey, leaseAppKey, open.totalCents), "settle the tab");
        toast("Tab settled — " + money(open.totalCents) + " taken at the counter; posting to the café ledger shortly.", true);
        setTimeout(renderPos, 700);
      } catch (e) {
        toast(e.message, false);
        settlePayBtn.disabled = false;
        // A PaidMismatchesTab, TabNotOpen, or UnservedLines refusal means the
        // card is stale (a self-order or void changed the total, someone else
        // settled the tab since this render, or a self-order landed since the
        // count was drawn) — the toast already says why, so the only courtesy
        // left is showing the current card rather than leaving the stale one
        // on screen. opOrThrow throws a bare Error for a rejection and a
        // transport failure alike, so the refusal is read off its message.
        if (
          e.message &&
          (e.message.indexOf("PaidMismatchesTab") !== -1 ||
            e.message.indexOf("TabNotOpen") !== -1 ||
            e.message.indexOf("UnservedLines") !== -1)
        ) {
          setTimeout(renderPos, 700);
        }
      }
    });
  }
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

// renderCreditHoldPanel is the POS's no-open-tab panel for a held lease: who
// owes what, for how long, when the reminder went out, and where the desk
// clears it — no Open Tab button, since the op refuses CreditHold until a
// payment or a write-off on the Front Desk tab's arrears row ends the
// episode.
function renderCreditHoldPanel(who, balance) {
  return (
    '<div class="panel">' +
    "<h2>Credit hold</h2>" +
    '<p class="lead">' + escapeHtml(who) + " owes " + escapeHtml(money(balance.balanceCents)) +
    (balance.isOverdue ? " · " + escapeHtml(overdueDaysPhrase(balance)) : "") +
    " · reminder sent " + escapeHtml(reminderSentDate(balance)) + "</p>" +
    '<p class="meta">A new tab cannot be opened while the reminded balance stands. ' +
    "Take a payment or write the balance off from the Front Desk tab's arrears row.</p>" +
    "</div>"
  );
}

// renderOpenTabCard is the POS's open-tab card. limitCents is the lease's
// effective house tab limit (tabLimitOf, off /api/residents), balanceCents
// its recorded ledger balance — the card says the limit and the balance
// when non-zero, and flags the resident's open EXPOSURE (balance + tab) at
// or over it, so the desk knows the resident can no longer self-order; the
// desk's own Ring Up / Add Charge stay offered, since the staff leg is
// never limited.
function renderOpenTabCard(tab, items, limitCents, balanceCents) {
  const catalog = items || [];
  const catalogHasAvailable = catalog.some((it) => it.available !== false);
  const limitLine = houseLimitLine(limitCents, tab.totalCents, balanceCents);
  const atLimit = typeof limitCents === "number" && (balanceCents || 0) + (tab.totalCents || 0) >= limitCents;
  const unserved = unservedLineCount(tab);
  const settleAttrs = settleButtonAttrs(unserved, "Mark served or void every unserved order first");
  return (
    '<div class="panel">' +
    "<h2>Open tab</h2>" +
    '<p class="amount">' + money(tab.totalCents) + "</p>" +
    '<p class="meta">Opened ' + escapeHtml(localDateTime(tab.openedAt)) + "</p>" +
    (limitLine ? '<p class="meta' + (atLimit ? " house-limit-warn" : "") + '" id="pos-house-limit">' + escapeHtml(limitLine) + "</p>" : "") +
    chargeLinesBlock(tab.lines, tab.itemsMemo, tab.tabKey, true) +
    (catalog.length
      ? '<form id="pos-catalog-form" class="field-row" style="margin-bottom:14px;">' +
        '<select id="pos-catalog-item">' +
        menuOptions(catalog) +
        "</select>" +
        (catalogHasAvailable ? '<button id="pos-catalog-submit" type="submit">Ring Up</button>' : "") +
        "</form>" +
        (catalogHasAvailable ? "" : '<p class="meta">Nothing on the menu right now.</p>')
      : "") +
    '<form id="charge-form" class="field-row" style="margin-bottom:14px;">' +
    '<input id="charge-amount" type="number" step="0.01" min="0.01" placeholder="Off-menu amount ($)" required />' +
    '<input id="charge-desc" type="text" placeholder="Description (optional)" />' +
    '<button id="charge-submit" type="submit">Add Charge</button>' +
    "</form>" +
    '<div class="panel-actions"><button id="settle-btn" class="danger"' + settleAttrs + ">Settle Tab</button>" +
    (tab.totalCents > 0
      ? '<button id="settle-pay-btn" class="danger"' + settleAttrs + ">Settle &amp; pay " + money(tab.totalCents) + "</button>"
      : "") +
    "</div>" +
    (unserved > 0 ? '<p class="meta">' + escapeHtml(unservedSettleHint(unserved)) + "</p>" : "") +
    "</div>"
  );
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

// summarizeToday folds the tabs settled on `now`'s local calendar day into
// the desk's Today panel: how many tabs settled, the gross they took, what
// sold (per line description, non-voided lines only) and what was voided.
// Gross is the sum of each tab's frozen totalCents — the figure the ledger
// was charged — not a re-sum of its lines: every void is by line, so on a
// tab settled under the current package the two agree, and any difference
// can only come from a tab settled under an earlier package version or
// charged before .status.lines existed; that difference (if any) is
// reported as an unitemized remainder rather than hidden inside an item. A tab is "today's" by its settledAt, the
// instant the money moved; open tabs are the grid's, not this panel's.
function summarizeToday(tabs, now) {
  // Next-midnight via the date constructor, not +24h: a DST day is 23 or 25
  // hours long and a fixed span would drop or borrow its last hour.
  const dayStart = new Date(now.getFullYear(), now.getMonth(), now.getDate()).getTime();
  const dayEnd = new Date(now.getFullYear(), now.getMonth(), now.getDate() + 1).getTime();
  const byItem = new Map();
  const out = { tabs: 0, grossCents: 0, items: [], voidCount: 0, voidCents: 0, unitemizedCents: 0, counterPaidCents: 0 };
  let itemizedCents = 0;
  for (const t of tabs || []) {
    if (t.status !== "settled" || !t.settledAt) continue;
    const at = new Date(t.settledAt).getTime();
    if (isNaN(at) || at < dayStart || at >= dayEnd) continue;
    // A tab opened and settled with nothing ever rung up is not a sale —
    // not a tab count, not a $0.00 line. One whose every line was voided
    // still counts: the voids are the story.
    if (!(t.totalCents || 0) && !(t.lines || []).length) continue;
    out.tabs += 1;
    out.grossCents += t.totalCents || 0;
    // A tab carrying no paidAtSettleCents (no counter payment at settle, or
    // a tab settled before this field existed) contributes nothing to the
    // sum — absent is not zero cash counted, it's nothing recorded.
    out.counterPaidCents += t.paidAtSettleCents || 0;
    for (const l of t.lines || []) {
      const cents = l.amountCents || 0;
      if (l.voided) {
        out.voidCount += 1;
        out.voidCents += cents;
        continue;
      }
      itemizedCents += cents;
      const name = l.description || "(unnamed)";
      const row = byItem.get(name) || { description: name, count: 0, cents: 0 };
      row.count += 1;
      row.cents += cents;
      byItem.set(name, row);
    }
  }
  out.items = Array.from(byItem.values()).sort((a, b) => b.cents - a.cents || b.count - a.count || (a.description < b.description ? -1 : 1));
  out.unitemizedCents = out.grossCents - itemizedCents;
  return out;
}

// renderFrontDeskToday paints summarizeToday's fold into #frontdesk-today.
// The panel hides on a day with no settled tab rather than announcing $0.00.
function renderFrontDeskToday(summary) {
  const panel = document.getElementById("frontdesk-today");
  if (!panel) return;
  if (!summary.tabs) {
    panel.hidden = true;
    return;
  }
  panel.hidden = false;
  document.getElementById("frontdesk-today-gross").textContent = money(summary.grossCents);
  document.getElementById("frontdesk-today-tabs").textContent =
    summary.tabs + " tab" + (summary.tabs === 1 ? "" : "s") + " settled";
  const list = document.getElementById("frontdesk-today-items");
  list.innerHTML = "";
  // Item descriptions are staff-typed (an off-menu Charge) — built as text
  // nodes, never markup.
  const itemRow = (label, qty, cents) => {
    const li = document.createElement("li");
    li.append(label + " ");
    const q = document.createElement("span");
    q.className = "qty";
    q.textContent = qty;
    li.append(q, " " + (cents < 0 ? "\u2212" : "") + money(Math.abs(cents)));
    list.append(li);
  };
  for (const it of summary.items) itemRow(it.description, "×" + it.count, it.cents);
  if (summary.unitemizedCents) itemRow("Unitemized", "(amount-only charges/voids)", summary.unitemizedCents);
  const voids = document.getElementById("frontdesk-today-voids");
  voids.textContent = summary.voidCount
    ? summary.voidCount + " line" + (summary.voidCount === 1 ? "" : "s") + " voided · " + money(summary.voidCents) + " not charged"
    : "No voids.";
  const counterPaid = document.getElementById("frontdesk-today-counterpaid");
  if (counterPaid) {
    if (summary.counterPaidCents) {
      counterPaid.hidden = false;
      counterPaid.textContent = "of which " + money(summary.counterPaidCents) + " taken at the counter";
    } else {
      counterPaid.hidden = true;
      counterPaid.textContent = "";
    }
  }
}

// ordersQueue is the desk's orders queue: every line, across every open tab,
// that still needs making — not voided, carrying an orderedAt, and not yet
// servedAt — oldest orderedAt first (ties broken by tabKey then lineId so
// the order is stable render to render). A line's state is one of three:
// "to make" (orderedAt set, servedAt absent — queued here), "served"
// (servedAt set — not queued), or unknown (orderedAt absent — the line
// predates this field, and is never queued since its ordering time can't be
// read). A settled or otherwise non-open tab contributes nothing: its lines
// are done, whatever their stamps say.
function ordersQueue(tabs) {
  const rows = [];
  for (const t of tabs || []) {
    if (t.status !== "open") continue;
    for (const l of t.lines || []) {
      if (l.voided || !l.orderedAt || l.servedAt) continue;
      rows.push({
        tabKey: t.tabKey,
        leaseAppKey: t.leaseAppKey,
        lineId: l.id,
        description: l.description,
        amountCents: l.amountCents,
        orderedBy: l.orderedBy,
        orderedAt: l.orderedAt,
      });
    }
  }
  // lineId is "line-" + a 1-based position, so the tie within one tab is
  // broken on that number (line-2 before line-10), never on the string.
  const lineNo = (id) => parseInt(String(id || "").replace(/^line-/, ""), 10) || 0;
  rows.sort((a, b) => {
    if (a.orderedAt !== b.orderedAt) return a.orderedAt < b.orderedAt ? -1 : 1;
    if (a.tabKey !== b.tabKey) return a.tabKey < b.tabKey ? -1 : 1;
    return lineNo(a.lineId) - lineNo(b.lineId);
  });
  return rows;
}

// unservedLineCount applies Settle's own refusal predicate
// (packages/cafe-domain's unserved_line_ids: not voided, orderedAt set,
// servedAt absent) to one tab's own lines — the same three-state read
// ordersQueue uses, so a settle button's courtesy can never drift from what
// the op actually refuses. A legacy line (neither field), a voided line, and
// a served line count for nothing. A synthetic {pending: true} line
// (renderResident's own optimistic overlay for a self-order this session
// just submitted, shown before the tab's own lines catch up) counts as
// unserved too — it IS an accepted self-order with no servedAt, and settling
// over it right now would meet the Processor's own UnservedLines refusal a
// moment later once the lens catches up. ordersQueue never sees a pending
// line (only renderResident ever constructs one), so the desk queue is
// unaffected by this.
function unservedLineCount(tab) {
  const lines = (tab && tab.lines) || [];
  let n = 0;
  for (const l of lines) {
    if (l.pending) { n++; continue; }
    if (l.voided || !l.orderedAt || l.servedAt) continue;
    n++;
  }
  return n;
}

// unservedSettleHint is the one-line note the POS and Front Desk cards show
// under a settle button disabled by unservedLineCount — one shared string so
// the two cards never drift on wording.
function unservedSettleHint(count) {
  return count + (count === 1 ? " order" : " orders") + " to make — mark served or void first";
}

// settleButtonAttrs is the exact disabled/title markup every settle button —
// the POS card's, the Front Desk card's, and the resident panel's — renders
// while its own unservedLineCount is positive; one shared function so a
// single pin over it is a pin over what every settle button actually
// renders, not three copies of the same ternary. count <= 0 renders nothing
// (an enabled button).
function settleButtonAttrs(count, title) {
  return count > 0 ? ' disabled title="' + escapeHtml(title) + '"' : "";
}

// residentSettlePanelMarkup is renderResident's own "Settle My Tab" action —
// pulled out of its template so a pin can check the exact button + hint
// markup a resident's own open-tab panel emits, not a copy of the ternary.
function residentSettlePanelMarkup(count) {
  const hint = "Your order is still being made — the desk can settle or cancel it";
  return (
    '<div class="panel-actions" style="margin-top:-8px;"><button id="resident-settle-btn" class="danger"' +
    settleButtonAttrs(count, hint) +
    ">Settle My Tab</button></div>" +
    (count > 0 ? '<p class="meta">' + escapeHtml(hint) + "</p>" : "")
  );
}

// orderedAgoLabel renders how long ago a queued line's orderedAt was,
// relative to now: "just now" under a minute, "N min ago" under an hour,
// "N h ago" beyond that. orderedAt or now failing to parse renders "?"
// rather than a garbage duration.
function orderedAgoLabel(orderedAt, now) {
  const then = new Date(orderedAt).getTime();
  const at = now instanceof Date ? now.getTime() : new Date(now).getTime();
  const ms = at - then;
  if (isNaN(ms)) return "?";
  const minutes = Math.floor(ms / 60000);
  if (minutes < 1) return "just now";
  if (minutes < 60) return minutes + " min ago";
  return Math.floor(minutes / 60) + " h ago";
}

// refusal-courtesy: MarkLineServed/TabNotOpen: hide — ordersQueue only lists lines of tabs whose status === "open"
// refusal-courtesy: MarkLineServed/LineVoided, LineAlreadyServed: hide — ordersQueue only lists lines with !voided && orderedAt && !servedAt, so a voided or served line never gets a Mark served button
// renderFrontDeskOrders paints the Orders panel (#frontdesk-orders) from
// ordersQueue(tabs) — hidden when nothing is queued, otherwise a header
// count plus one row per line: description, who ordered it, how long ago,
// and a Mark served button that submits MarkLineServed and refreshes the
// desk on success. Who ordered resolves from the line's own orderedBy
// through the identity roster (nameForIdentity), so no lease join is needed.
function renderFrontDeskOrders(tabs) {
  const section = document.getElementById("frontdesk-orders");
  const count = document.getElementById("frontdesk-orders-count");
  const list = document.getElementById("frontdesk-orders-items");
  if (!section || !count || !list) return;
  const rows = ordersQueue(tabs);
  if (!rows.length) {
    section.hidden = true;
    list.innerHTML = "";
    frontDeskOrdersPainted = "";
    return;
  }
  const now = new Date();
  const signature = rows.map((r) => r.tabKey + "/" + r.lineId + "@" + orderedAgoLabel(r.orderedAt, now)).join("|");
  if (!section.hidden && signature === frontDeskOrdersPainted) return;
  frontDeskOrdersPainted = signature;
  section.hidden = false;
  count.textContent = rows.length + " to make";
  list.innerHTML = rows
    .map((row) => {
      const who = row.orderedBy ? " · " + escapeHtml(nameForIdentity(idOf(row.orderedBy))) : "";
      return (
        "<li>" +
        '<span class="item-desc">' + escapeHtml(row.description) + who +
        " · " + escapeHtml(orderedAgoLabel(row.orderedAt, now)) + "</span>" +
        '<button type="button" class="ghost" data-serve-tab="' + escapeHtml(row.tabKey) +
        '" data-serve-line="' + escapeHtml(row.lineId) + '">Mark served</button>' +
        "</li>"
      );
    })
    .join("");
  list.querySelectorAll("[data-serve-tab]").forEach((btn) => {
    btn.addEventListener("click", async () => {
      const tabKey = btn.dataset.serveTab;
      const lineId = btn.dataset.serveLine;
      btn.disabled = true;
      frontDeskServesInFlight++;
      try {
        await opOrThrow(
          {
            operationType: "MarkLineServed", class: "tab",
            reads: [tabKey, tabKey + ".status"],
            payload: { tabKey, lineId },
          },
          "mark the order served"
        );
        toast("Served.", true);
        setTimeout(loadFrontDesk, 700);
      } catch (e) {
        toast(e.message, false);
        btn.disabled = false;
      } finally {
        frontDeskServesInFlight--;
      }
    });
  });
}

// refusal-courtesy: Settle/TabNotOpen: hide — loadFrontDesk filters to tabs whose status === "open" (tabs = (r.tabs || []).filter(...)) before drawing a settle-<tabKey> button per one
// refusal-courtesy: Settle/PaidMismatchesTab: see settlePayEnvelope
// refusal-courtesy: Settle/UnservedLines: disable — frontDeskCard renders settle-<tabKey>/settle-pay-<tabKey> disabled while unservedLineCount(t) > 0
async function loadFrontDesk() {
  const grid = document.getElementById("frontdesk-grid");
  const summary = document.getElementById("frontdesk-summary");
  grid.innerHTML = "";
  summary.textContent = "";
  // A failed refresh must not leave the previous load's sales painted
  // beside the error, so the Today panel resets with the grid, and the
  // Orders panel resets with them (empty tabs hides it).
  renderFrontDeskToday(summarizeToday([], new Date()));
  renderFrontDeskOrders([]);
  let tabs, allTabs;
  try {
    const r = await appGet("/api/tabs");
    renderFrontDeskToday(summarizeToday(r.tabs || [], new Date()));
    allTabs = r.tabs || [];
    tabs = allTabs.filter((t) => t.status === "open");
  } catch (e) {
    grid.innerHTML = '<div class="empty">' + escapeHtml(e.message) + "</div>";
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

  // Same join, for the resident's own house-tab balance — the front desk's
  // own counterpart to the overdue banner the resident already sees on
  // their own ledger view (ledger.go's deriveStatement). The endpoint
  // already returns at most one row per leaseAppKey, so no keepSoonest
  // reduction is needed here. Best-effort, same degrade-to-hidden posture
  // as the joins above.
  // Same resident-name/unit-address join the lease pickers use
  // (loadLeasePickerContext) — the card's "who" + rent/term lines, and the
  // standalone arrears list's own resident-name resolution below; the
  // balances ride along in the same context. Called once and reused for
  // both, rather than once per renderer.
  const { residentsByLease, leaseDetailsByLease, balances: balancesList, balancesByLease } = await loadLeasePickerContext();

  // The standalone arrears list — every lease that owes money, whether or
  // not it currently has an open tab. Rendered regardless of tabs.length:
  // arrears must stay visible even when every tab has settled, unlike the
  // per-tab balance badge below which only exists on an open tab's card.
  renderFrontDeskArrears(balancesList, residentsByLease);
  renderFrontDeskOrders(allTabs);

  summary.textContent = tabs.length + " open tab" + (tabs.length === 1 ? "" : "s");
  if (!tabs.length) {
    grid.innerHTML = '<div class="empty">No open tabs.</div>';
    return;
  }
  grid.innerHTML = tabs
    .map((t) => frontDeskCard(t, bookingsByLease[t.leaseAppKey], leaseDetailsByLease[t.leaseAppKey], visitsByLease[t.leaseAppKey], residentsByLease[t.leaseAppKey], balancesByLease[t.leaseAppKey]))
    .join("");
  tabs.forEach((t) => {
    const sanitized = t.tabKey.replace(/[^a-zA-Z0-9]/g, "");
    const btn = document.getElementById("settle-" + sanitized);
    if (btn) {
      btn.addEventListener("click", async () => {
        if (btn.disabled) return;
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
          // See settle-pay-<tabKey>'s own catch below: an UnservedLines
          // refusal means a self-order landed since this card rendered, so
          // the courtesy is refreshing it rather than leaving a dead click.
          if (e.message && e.message.indexOf("UnservedLines") !== -1) {
            setTimeout(loadFrontDesk, 700);
          }
        }
      });
    }
    const payBtn = document.getElementById("settle-pay-" + sanitized);
    if (payBtn) {
      payBtn.addEventListener("click", async () => {
        if (payBtn.disabled) return;
        payBtn.disabled = true;
        try {
          await opOrThrow(settlePayEnvelope(t.tabKey, t.leaseAppKey, t.totalCents), "settle the tab");
          toast("Tab settled — " + money(t.totalCents) + " taken at the counter; posting to the café ledger shortly.", true);
          setTimeout(loadFrontDesk, 700);
        } catch (e) {
          toast(e.message, false);
          payBtn.disabled = false;
          // See the POS settle-pay-btn's own catch (renderPos): a
          // PaidMismatchesTab, TabNotOpen, or UnservedLines refusal means the
          // card is stale, and the courtesy is refreshing it rather than
          // leaving it up.
          if (
            e.message &&
            (e.message.indexOf("PaidMismatchesTab") !== -1 ||
              e.message.indexOf("TabNotOpen") !== -1 ||
              e.message.indexOf("UnservedLines") !== -1)
          ) {
            setTimeout(loadFrontDesk, 700);
          }
        }
      });
    }
  });
}

// frontDeskBalanceBadge renders a joined balance row's overdue/owed/credit
// state for the front-desk card — the staff counterpart to the resident's
// own overdue banner (ledger.go's deriveStatement, this file's
// statementLine()). An overdue lease reuses statementLine() unchanged (same
// field names — dueDate/isOverdue/daysOverdue — so the red banner reads
// identically on both surfaces); a lease that owes something but isn't
// overdue yet gets a neutral due-by note with the amount; a lease left in
// CREDIT (balanceCents negative — handleFrontDeskBalances serves these rows,
// ledger.go's computeLedgerBalances) gets an "In credit" note; a lease with
// nothing owed and nothing in credit (no joined row at all —
// handleFrontDeskBalances omits only exact-zero-balance leases) renders
// nothing.
function frontDeskBalanceBadge(balance) {
  if (!balance) return "";
  if (balance.isOverdue) return statementLine(balance);
  if (balance.balanceCents > 0) {
    const due = balance.dueDate ? new Date(balance.dueDate).toLocaleDateString() : "?";
    return '<div class="meta">Owes ' + money(balance.balanceCents) + ", due " + due + "</div>";
  }
  if (balance.balanceCents < 0) {
    return '<div class="meta">In credit ' + money(-balance.balanceCents) + "</div>";
  }
  return "";
}

// frontDeskArrearsLine renders one arrears row's dueDate/isOverdue/
// daysOverdue fields as a compact inline banner for the standalone arrears
// list — mirrors wellness-app's arrearsLine(), a small inline <span> rather
// than statementLine()'s <p> block, since a list row needs one line, not a
// paragraph. An overdue row also says whether the reminder has gone out
// (row.reminderSentAt, joined server-side onto the same balance row).
function frontDeskArrearsLine(row) {
  const due = row.dueDate ? new Date(row.dueDate).toLocaleDateString() : "?";
  if (row.isOverdue) {
    const days = Number(row.daysOverdue) || 0;
    const reminder = row.reminderSentAt
      ? " · reminder sent " + new Date(row.reminderSentAt).toLocaleDateString()
      : " · no reminder sent yet";
    return '<span class="arrears-overdue">OVERDUE — ' + days + (days === 1 ? " day" : " days") + reminder + "</span>";
  }
  return "Due " + due;
}

// renderFrontDeskArrears populates the front desk's standalone arrears list
// (#frontdesk-arrears) — every lease with a non-zero balance
// (/api/frontdesk-balances), whether or not it has an open tab. A resident
// who has settled their tab but still owes the house money would otherwise
// be invisible the moment the tab closes (frontDeskBalanceBadge only
// renders on an open tab's card) — this list is the fix. Sorted client-side
// (café's Go handler is unchanged) with the same comparator wellness-app's
// handleFrontDeskArrears uses server-side: isOverdue desc, then daysOverdue
// desc, then balanceCents desc — a credit lease's balanceCents is negative,
// so this single sort already places every debtor ahead of every credit
// lease with no separate pass. A debtor row gets a Take payment button
// (confirm → CreditCafeAccount reason: payment, capped at the balance shown
// — cash collected at the desk) beside a Write off button (confirm →
// CreditCafeAccount reason: waiver, capped at the balance shown — the debt
// forgiven instead); a credit row renders "in credit $X" with a Pay out
// button (confirm → PayoutCafeCredit, capped at the credit shown) —
// wireArrearsActions binds all three, once, via one delegated click handler
// on the list itself.
function renderFrontDeskArrears(balances, residentsByLease) {
  const list = document.getElementById("frontdesk-arrears");
  const empty = document.getElementById("frontdesk-arrears-empty");
  if (!list || !empty) return;
  wireArrearsActions(list);
  const rows = (balances || []).slice().sort((a, b) => {
    if (a.isOverdue !== b.isOverdue) return a.isOverdue ? -1 : 1;
    if (a.daysOverdue !== b.daysOverdue) return (b.daysOverdue || 0) - (a.daysOverdue || 0);
    return (b.balanceCents || 0) - (a.balanceCents || 0);
  });
  list.innerHTML = "";
  if (!rows.length) {
    empty.hidden = false;
    return;
  }
  empty.hidden = true;
  for (const row of rows) {
    const bookerKey = residentsByLease[row.leaseAppKey];
    const who = bookerKey ? nameForIdentity(idOf(bookerKey)) : shortKey(row.leaseAppKey);
    const li = document.createElement("li");
    li.className = "ledger-entry arrears-row";
    li.innerHTML = arrearsRowMarkup(row, who, isOwnAccount(bookerKey));
    list.append(li);
  }
}

// arrearsRowMarkup renders one arrears row's inner HTML: the resident, the
// balance and its due/overdue line, and the desk's buttons for it. Pure (no
// DOM), so the goja pins can run the shipped source. On the viewer's OWN row
// (own === true) the clearing verbs — Write off on a debtor row, Pay out on a
// credit row — are withheld and replaced by a note naming the rule, because
// packages/cafe-ledger refuses them SelfClearing; Take payment stays, since a
// payment is money coming in and the script accepts it from anyone standing.
function arrearsRowMarkup(row, who, own) {
  const ownNote = '<span class="meta">your own account — another staffer clears it</span>';
  if (row.balanceCents < 0) {
    return (
      escapeHtml(who) + " — in credit " + money(-row.balanceCents) +
      ' <span class="ledger-entry-actions">' +
      (own
        ? ownNote
        : '<button type="button" class="payout-credit-btn" data-account="' +
          escapeHtml(row.accountKey || "") + '" data-amount="' + (-row.balanceCents) +
          '" data-who="' + escapeHtml(who) + '">Pay out</button>') +
      "</span>"
    );
  }
  return (
    escapeHtml(who) + " — " + money(row.balanceCents) + " · " + frontDeskArrearsLine(row) +
    ' <span class="ledger-entry-actions"><button type="button" class="take-payment-btn" data-account="' +
    escapeHtml(row.accountKey || "") + '" data-amount="' + (+row.balanceCents) +
    '" data-who="' + escapeHtml(who) + '">Take payment</button>' +
    (own
      ? ownNote
      : '<button type="button" class="writeoff-debt-btn" data-account="' +
        escapeHtml(row.accountKey || "") + '" data-amount="' + (+row.balanceCents) +
        '" data-who="' + escapeHtml(who) + '">Write off</button>') +
    "</span>"
  );
}

// wireArrearsActions binds ONE delegated click handler to the arrears list
// (#frontdesk-arrears), covering the Take payment, Write off, and Pay out
// buttons renderFrontDeskArrears draws into it — bound once ever
// (list.dataset.wired guards re-registration across every
// renderFrontDeskArrears call, since the list element itself persists across
// renders while its rows are replaced). Delegating onto the persistent list,
// rather than a listener per button, means a re-render never has to
// re-wire, and every row's data-account/data-amount/data-who attributes
// carry everything the handler needs — no closure over the render's own
// data.
function wireArrearsActions(list) {
  if (list.dataset.wired) return;
  list.dataset.wired = "1";
  list.addEventListener("click", (ev) => {
    const takePaymentBtn = ev.target.closest(".take-payment-btn");
    if (takePaymentBtn) { handleTakePayment(takePaymentBtn); return; }
    const writeoffBtn = ev.target.closest(".writeoff-debt-btn");
    if (writeoffBtn) { handleWriteOffDebt(writeoffBtn); return; }
    const payoutBtn = ev.target.closest(".payout-credit-btn");
    if (payoutBtn) { handlePayoutCredit(payoutBtn); return; }
  });
}

// handleTakePayment records cash collected at the desk against a debtor's
// balance — a staff-only CreditCafeAccount submitted with reason: "payment",
// capped at the balance shown (mirrors handleWriteOffDebt exactly, reason
// aside: same confirm-then-dispatch shape, same landed-ambiguity throw path
// narration, lint-ceremony-throw-path.go).
// refusal-courtesy: CreditCafeAccount/InvalidState: none — the account's .balance aspect being a foreign class is a data-integrity fault (post_entry, packages/cafe-ledger/scripts.go), not state any read model exposes
// refusal-courtesy: CreditCafeAccount/NoCreditToPayOut, PayoutExceedsCash, PayoutExceedsCredit: unreachable — CreditCafeAccount calls post_entry(entry_type="credit", ...) (packages/cafe-ledger/scripts.go); is_payout requires entry_type == "debit", so the payout branch never runs
// refusal-courtesy: CreditCafeAccount/RefundExceedsCharge, RefundExceedsPaid: unreachable — CreditCafeAccount calls post_entry(..., allow_reverses_ref=False, ...) (packages/cafe-ledger/scripts.go); the reversesRef branch (reversed_charge / the cash-floor check) only runs when allow_reverses_ref is True
// refusal-courtesy: CreditCafeAccount/NoBalanceToPay: hide — renderFrontDeskArrears only lists non-zero-balance leases (frontdesk-balances) and only draws the take-payment-btn on the debtor branch (row.balanceCents >= 0); a zero-balance lease never appears in the arrears list at all
// refusal-courtesy: CreditCafeAccount/PaymentExceedsBalance: cap — amountCents is prefilled to exactly row.balanceCents (the balance shown) into a detached, never-rendered descriptor mount (renderOpForm(row, context, document.createElement("div")))
// refusal-courtesy: CreditCafeAccount/WriteOffExceedsBalance: unreachable — this function always submits reason: "payment" (post_entry, packages/cafe-ledger/scripts.go); the reason=="waiver" branch that raises WriteOffExceedsBalance never runs from here
// refusal-courtesy: CreditCafeAccount/SelfClearing: unreachable — this function always submits reason: "payment", and require_not_own_account (post_entry, packages/cafe-ledger/scripts.go) runs only for a waiver, refund or payout; a staffer's payment on their own account is accepted, so the Take payment button is drawn on the viewer's own row too
// refusal-courtesy: CreditCafeAccount/CounterPaymentAlreadyPosted, CounterPaymentMismatch, NoCounterPayment, TabNotSettled: unreachable — this function's context.prefill never sets tabRef (the Weaver-only field only cafeTabSettlement's missing_payment dispatch sets); require_counter_payment (packages/cafe-ledger/scripts.go) only runs when payload.tabRef is present
async function handleTakePayment(btn) {
  const accountKey = btn.getAttribute("data-account");
  const amountCents = parseInt(btn.getAttribute("data-amount"), 10);
  const who = btn.getAttribute("data-who");
  if (!accountKey || !amountCents) { toast("This lease has no café account to take a payment against.", false); return; }
  if (!confirm("Take a payment of " + money(amountCents) + " from " + who + "? This records cash collected at the desk.")) return;
  btn.disabled = true;
  try {
    await loadOpCatalogQuiet();
    const { renderOpForm } = await loadDescriptorform();
    const row = opCatalogCache && opCatalogCache.CreditCafeAccount;
    if (!row) throw new Error("this action is unavailable");
    // me is set because CreditCafeAccount's descriptor declares an
    // `{actor} holdsRole out` enumeration (the workplace-confinement walk
    // the script runs) — not for buildAuthContext, which ignores context.me
    // entirely once selfVoice is false.
    const context = {
      target: accountKey,
      me: identityKey(),
      selfVoice: false,
      prefill: { amountCents: amountCents, reason: "payment", memo: "Paid at the desk" },
    };
    const handle = renderOpForm(row, context, document.createElement("div"));
    if (!handle) throw new Error("this action is unavailable");
    const { envelope, reveal } = await handle.submit();
    const reply = await submitCatalogOp(envelope, "take the payment");
    revealCeremonySecret(reveal, reply);
    toast("Took " + money(amountCents) + " from " + who + ".", true);
    setTimeout(loadFrontDesk, 700);
  } catch (e) {
    // See handleWriteOffDebt's own catch: e.rejected (submitCatalogOp) marks
    // a CONFIRMED non-commit — the payment cap, AuthDenied — whose message
    // already says exactly why, toasted bare. Anything else is a
    // transport/unknown throw with an ambiguous landing.
    if (e.rejected) {
      toast(e.message, false);
    } else {
      toast("Could not confirm the payment reached the server — it may have landed; check the ledger before trying again. " + e.message, false);
    }
    btn.disabled = false;
  }
}

// handleWriteOffDebt forgives a debtor's balance — a staff-only
// CreditCafeAccount submitted with reason: "waiver", capped at the balance
// shown (the op's own cap, PayoutCafeCredit's mirror on the credit side).
// This is an irreversible money-adjacent decision (the debt is gone, not
// collected), so it is confirmed before it dispatches, and its throw path
// narrates the landed-ambiguity vocabulary (lint-ceremony-throw-path.go) —
// a failed write here may already have forgiven the debt.
// refusal-courtesy: CreditCafeAccount/InvalidState: none — the account's .balance aspect being a foreign class is a data-integrity fault (post_entry, packages/cafe-ledger/scripts.go), not state any read model exposes
// refusal-courtesy: CreditCafeAccount/NoCreditToPayOut, PayoutExceedsCash, PayoutExceedsCredit: unreachable — CreditCafeAccount calls post_entry(entry_type="credit", ...) (packages/cafe-ledger/scripts.go); is_payout requires entry_type == "debit", so the payout branch never runs
// refusal-courtesy: CreditCafeAccount/RefundExceedsCharge, RefundExceedsPaid: unreachable — CreditCafeAccount calls post_entry(..., allow_reverses_ref=False, ...) (packages/cafe-ledger/scripts.go); the reversesRef branch (reversed_charge / the cash-floor check) only runs when allow_reverses_ref is True
// refusal-courtesy: CreditCafeAccount/NoBalanceToPay: hide — renderFrontDeskArrears only lists non-zero-balance leases (frontdesk-balances) and only draws the writeoff-debt-btn on the debtor branch (row.balanceCents >= 0); a zero-balance lease never appears in the arrears list at all
// refusal-courtesy: CreditCafeAccount/WriteOffExceedsBalance: cap — amountCents is prefilled to exactly row.balanceCents (the balance shown) into a detached, never-rendered descriptor mount (renderOpForm(row, context, document.createElement("div")))
// refusal-courtesy: CreditCafeAccount/PaymentExceedsBalance: unreachable — this function always submits reason: "waiver" (post_entry, packages/cafe-ledger/scripts.go); the reason=="waiver" branch raises WriteOffExceedsBalance first, so the plain PaymentExceedsBalance fail below it is never reached from here
// refusal-courtesy: CreditCafeAccount/SelfClearing: hide — arrearsRowMarkup withholds the writeoff-debt-btn on the viewer's own row (isOwnAccount(residentsByLease[row.leaseAppKey]), the roster's bookerKey against state.identityId) and renders the "your own account — another staffer clears it" note in its place
// refusal-courtesy: CreditCafeAccount/CounterPaymentAlreadyPosted, CounterPaymentMismatch, NoCounterPayment, TabNotSettled: unreachable — this function's context.prefill never sets tabRef (the Weaver-only field only cafeTabSettlement's missing_payment dispatch sets); require_counter_payment (packages/cafe-ledger/scripts.go) only runs when payload.tabRef is present
async function handleWriteOffDebt(btn) {
  const accountKey = btn.getAttribute("data-account");
  const amountCents = parseInt(btn.getAttribute("data-amount"), 10);
  const who = btn.getAttribute("data-who");
  if (!accountKey || !amountCents) { toast("This lease has no café account to write off.", false); return; }
  if (!confirm("Write off " + money(amountCents) + " owed by " + who + "? This forgives the debt — it is not cash collected.")) return;
  btn.disabled = true;
  try {
    await loadOpCatalogQuiet();
    const { renderOpForm } = await loadDescriptorform();
    const row = opCatalogCache && opCatalogCache.CreditCafeAccount;
    if (!row) throw new Error("this action is unavailable");
    // me is set because CreditCafeAccount's descriptor declares an
    // `{actor} holdsRole out` enumeration (the workplace-confinement walk
    // the script runs) — not for buildAuthContext, which ignores context.me
    // entirely once selfVoice is false.
    const context = {
      target: accountKey,
      me: identityKey(),
      selfVoice: false,
      prefill: { amountCents: amountCents, reason: "waiver", memo: "Written off" },
    };
    const handle = renderOpForm(row, context, document.createElement("div"));
    if (!handle) throw new Error("this action is unavailable");
    const { envelope, reveal } = await handle.submit();
    const reply = await submitCatalogOp(envelope, "write off the balance");
    revealCeremonySecret(reveal, reply);
    toast("Wrote off " + money(amountCents) + ".", true);
    setTimeout(loadFrontDesk, 700);
  } catch (e) {
    // A rejected reply (e.rejected, submitCatalogOp) is a CONFIRMED
    // non-commit — NoCreditToPayOut/PayoutExceedsCharge-shaped refusals, the
    // write-off cap, AuthDenied — and its own message already says exactly
    // why, so it toasts bare. Anything else is a transport/unknown throw
    // whose landing is ambiguous, so it gets the landed-ambiguity wording.
    if (e.rejected) {
      toast(e.message, false);
    } else {
      toast("Could not confirm the write-off reached the server — it may have landed; check the ledger before trying again. " + e.message, false);
    }
    btn.disabled = false;
  }
}

// handlePayoutCredit hands a resident's credit back as cash — a staff-only
// PayoutCafeCredit submitted as a debit capped at the credit shown (the
// op's own cap, CreditCafeAccount's waiver mirror on the debt side). Same
// irreversibility posture as handleWriteOffDebt: confirmed before dispatch,
// landed-ambiguity wording on the throw path.
// refusal-courtesy: PayoutCafeCredit/InvalidState: none — the account's .balance aspect being a foreign class is a data-integrity fault (post_entry, packages/cafe-ledger/scripts.go), not state any read model exposes
// refusal-courtesy: PayoutCafeCredit/NoCreditToPayOut: hide — renderFrontDeskArrears (wireArrearsActions) only draws the payout-credit-btn on a row with balanceCents < 0; a debtor/square row gets the Write off button instead
// refusal-courtesy: PayoutCafeCredit/SelfClearing: hide — arrearsRowMarkup withholds the payout-credit-btn on the viewer's own row (isOwnAccount(residentsByLease[row.leaseAppKey]), the roster's bookerKey against state.identityId) and renders the "your own account — another staffer clears it" note in its place
// refusal-courtesy: PayoutCafeCredit/PayoutExceedsCredit, PayoutExceedsCash: cap — amountCents is prefilled to exactly -row.balanceCents (the credit shown) into a detached, never-rendered descriptor mount (renderOpForm(row, context, document.createElement("div"))), so the submitted amount never exceeds the credit; and the script holds credit <= cashCents (a credit is only ever posted from cash paid in), so an amount within the credit is within the cash too
// refusal-courtesy: PayoutCafeCredit/RefundExceedsCharge, RefundExceedsPaid: unreachable — PayoutCafeCredit calls post_entry(..., allow_reverses_ref=False, ...) (packages/cafe-ledger/scripts.go); the reversesRef branch only runs when allow_reverses_ref is True
// refusal-courtesy: PayoutCafeCredit/NoBalanceToPay, PaymentExceedsBalance, WriteOffExceedsBalance: unreachable — PayoutCafeCredit calls post_entry(state, op, "debit", ...) (packages/cafe-ledger/scripts.go); is_payment requires entry_type == "credit", so the whole is_payment block these codes live in never runs for a debit
// refusal-courtesy: PayoutCafeCredit/CounterPaymentAlreadyPosted, CounterPaymentMismatch, NoCounterPayment, TabNotSettled: unreachable — PayoutCafeCredit calls post_entry(..., allow_tab_ref=False, ...) and refuses any payload.tabRef before reaching post_entry at all (packages/cafe-ledger/scripts.go); require_counter_payment never runs
async function handlePayoutCredit(btn) {
  const accountKey = btn.getAttribute("data-account");
  const amountCents = parseInt(btn.getAttribute("data-amount"), 10);
  const who = btn.getAttribute("data-who");
  if (!accountKey || !amountCents) { toast("This lease has no café account to pay out.", false); return; }
  if (!confirm("Pay out " + money(amountCents) + " in credit to " + who + "? This is cash handed over, not spent as credit.")) return;
  btn.disabled = true;
  try {
    await loadOpCatalogQuiet();
    const { renderOpForm } = await loadDescriptorform();
    const row = opCatalogCache && opCatalogCache.PayoutCafeCredit;
    if (!row) throw new Error("this action is unavailable");
    // me is set because PayoutCafeCredit's descriptor declares BOTH
    // `{actor} holdsRole out` (the workplace-confinement walk) and
    // `{payload.accountKey} postedTo in` (the backfill replay) — not for
    // buildAuthContext, which ignores context.me entirely once selfVoice is
    // false.
    const context = {
      target: accountKey,
      me: identityKey(),
      selfVoice: false,
      prefill: { amountCents: amountCents, memo: "Paid out in cash" },
    };
    const handle = renderOpForm(row, context, document.createElement("div"));
    if (!handle) throw new Error("this action is unavailable");
    const { envelope, reveal } = await handle.submit();
    const reply = await submitCatalogOp(envelope, "pay out the credit");
    revealCeremonySecret(reveal, reply);
    toast("Paid out " + money(amountCents) + ".", true);
    setTimeout(loadFrontDesk, 700);
  } catch (e) {
    // See handleWriteOffDebt's own catch: e.rejected (submitCatalogOp) marks
    // a CONFIRMED non-commit — NoCreditToPayOut, PayoutExceedsCredit,
    // AuthDenied — whose message already says exactly why, toasted bare.
    // Anything else is a transport/unknown throw with an ambiguous landing.
    if (e.rejected) {
      toast(e.message, false);
    } else {
      toast("Could not confirm the payout reached the server — it may have landed; check the ledger before trying again. " + e.message, false);
    }
    btn.disabled = false;
  }
}

function frontDeskCard(t, booking, lease, visit, bookerKey, balance) {
  const sanitized = t.tabKey.replace(/[^a-zA-Z0-9]/g, ""); // markup-safe: stripped to [a-zA-Z0-9], nothing else survives
  const id = "settle-" + sanitized;
  const payId = "settle-pay-" + sanitized;
  const balanceBadge = frontDeskBalanceBadge(balance);
  const classBadge = booking
    ? '<div class="meta">🧘 Booked: ' + escapeHtml(booking.sessionName || "class") + " · " + escapeHtml(localDateTime(booking.startsAt)) + "</div>"
    : "";
  const leaseLine = lease && lease.unitRent
    ? '<div class="meta">🏠 ' + rentAmount(lease.unitRent, lease.unitCurrency) + "/mo" +
      (lease.unitLeaseTermMonths ? " · " + escapeHtml(lease.unitLeaseTermMonths) + "mo term" : "") + "</div>"
    : "";
  // Existence + time only — never a visit reason (front-desk staff see "a
  // visit is scheduled," not why or with whom).
  const visitBadge = visit
    ? '<div class="meta">🩺 Visit: ' + escapeHtml(localDateTime(visit.startsAt)) + "</div>"
    : "";
  // The lease's applicant, resolved to a name via the protected roster
  // (nameForIdentity) — falls back to the truncated lease key when the
  // applicant is unknown or unresolved, the same degrade the "who" title
  // always showed before this join existed.
  const who = bookerKey ? nameForIdentity(idOf(bookerKey)) : shortKey(t.leaseAppKey);
  const unserved = unservedLineCount(t);
  const settleAttrs = settleButtonAttrs(unserved, "Mark served or void every unserved order first");
  return (
    '<div class="card">' +
    '<span class="badge open">open</span>' +
    '<div class="who">' + escapeHtml(who) + "</div>" +
    '<div class="amount">' + money(t.totalCents) + "</div>" +
    '<div class="meta">Opened ' + escapeHtml(localDateTime(t.openedAt)) + "</div>" +
    balanceBadge +
    chargeLinesBlock(t.lines, t.itemsMemo, null, true) +
    classBadge +
    leaseLine +
    visitBadge +
    '<div class="card-actions"><button id="' + id + '" class="danger"' + settleAttrs + ">Settle</button>" +
    (t.totalCents > 0
      ? '<button id="' + payId + '" class="danger"' + settleAttrs + ">Settle &amp; pay " + money(t.totalCents) + "</button>"
      : "") +
    "</div>" +
    (unserved > 0 ? '<p class="meta">' + escapeHtml(unservedSettleHint(unserved)) + "</p>" : "") +
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
// shows Retire, same as before. available (default true) badges a sold-out
// item and swaps the row's availability button between "Sold out today" and
// "Back on menu" — the desk's toggle for taking an item off the menu for the
// day without retiring it.
function menuItemCard(it) {
  const available = it.available !== false;
  const badge = it.missingLocation
    ? '<span class="badge" style="background:#b00020;color:#fff;">no location</span>'
    : "";
  const soldOutBadge = available ? "" : '<span class="badge" style="background:var(--text-dim);color:var(--bg);">sold out</span>';
  const relocate = it.missingLocation
    ? '<button type="button" data-relocate="' + escapeHtml(it.menuItemKey) + '">Relocate here</button>'
    : "";
  return (
    '<div class="card" data-item="' + escapeHtml(it.menuItemKey) + '">' +
    badge + soldOutBadge +
    '<div class="who" data-field="who">' + escapeHtml(it.name) + "</div>" +
    '<div class="amount" data-field="amount">' + money(it.priceCents) + "</div>" +
    '<div class="card-actions" data-field="actions">' + relocate +
    '<button type="button" data-availability="' + escapeHtml(it.menuItemKey) +
    '" data-available="' + (available ? "true" : "false") + '">' +
    (available ? "Sold out today" : "Back on menu") + "</button>" +
    '<button type="button" data-edit="' + escapeHtml(it.menuItemKey) +
    '" data-name="' + escapeHtml(it.name) + '" data-price-cents="' + (Number(it.priceCents) || 0) + '">Edit</button>' +
    '<button type="button" class="danger" data-retire="' +
    escapeHtml(it.menuItemKey) +
    '">Retire</button></div>' +
    "</div>"
  );
}

// editMenuItemForm renders the inline rename/reprice form that swaps into a
// Manage Menu card when its Edit button is clicked — the name/price inputs
// prefill from the card's own current row (data-name/data-price-cents),
// mirroring the add-item form's own field-row/panel-actions layout so the
// swapped-in card reads as the same UI, not a different surface.
function editMenuItemForm(currentName, currentPriceCents) {
  return (
    '<form class="edit-menu-item-form">' +
    '<div class="field-row">' +
    '<div class="field"><label>Name</label><input type="text" class="edit-name" value="' +
    escapeHtml(currentName) + '" /></div>' +
    '<div class="field"><label>Price</label><input type="text" class="edit-price" inputmode="decimal" value="' +
    ((currentPriceCents || 0) / 100).toFixed(2) + '" /></div>' +
    "</div>" +
    '<div class="panel-actions">' +
    '<button type="submit">Save</button>' +
    '<button type="button" class="ghost edit-cancel">Cancel</button>' +
    "</div>" +
    "</form>"
  );
}

// refusal-courtesy: SetMenuItemLocation/InvalidState: none — the already-live servedAt link race (make_link_create_or_revive, packages/cafe-domain/ddls.go) is a same-item/same-location double-submit fault; no read-model row exposes it for the Relocate button to check
async function loadManageMenu() {
  const summary = document.getElementById("menu-summary");
  const body = document.getElementById("menu-body");
  summary.textContent = "";
  body.innerHTML = "";
  loadHousePolicy().catch(() => {});
  let items;
  try {
    const data = await appGet("/api/menu");
    items = data.menu || [];
  } catch (e) {
    body.innerHTML = '<div class="empty">' + escapeHtml(e.message) + "</div>";
    return;
  }
  summary.textContent = items.length + " item" + (items.length === 1 ? "" : "s");
  body.innerHTML = items.length
    ? '<div class="grid">' + items.map(menuItemCard).join("") + "</div>"
    : '<div class="empty">No menu items yet — add one above.</div>';
  body.querySelectorAll("[data-availability]").forEach((btn) => {
    btn.addEventListener("click", async () => {
      const menuItemKey = btn.dataset.availability;
      const current = btn.dataset.available === "true";
      btn.disabled = true;
      try {
        await opOrThrow(
          {
            operationType: "SetMenuItemAvailability", class: "menuitem",
            reads: [menuItemKey, menuItemKey + ".price"],
            payload: { menuItemKey, available: !current },
          },
          "update the item's availability"
        );
        toast(current ? "Marked sold out for today." : "Back on the menu.", true);
        setTimeout(loadManageMenu, 700);
      } catch (e) {
        toast(e.message, false);
        btn.disabled = false;
      }
    });
  });
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
  body.querySelectorAll("[data-edit]").forEach((btn) => {
    btn.addEventListener("click", () => {
      const menuItemKey = btn.dataset.edit;
      const card = btn.closest(".card");
      const currentName = btn.dataset.name;
      const currentPriceCents = Number(btn.dataset.priceCents) || 0;
      card.innerHTML = editMenuItemForm(currentName, currentPriceCents);
      card.querySelector(".edit-cancel").addEventListener("click", () => loadManageMenu());
      card.querySelector("form").addEventListener("submit", async (ev) => {
        ev.preventDefault();
        const name = card.querySelector(".edit-name").value.trim();
        const cents = parseDollars(card.querySelector(".edit-price").value);
        if (!name) { toast("Enter a name for the item.", false); return; }
        if (cents === null) { toast("Enter a price greater than $0.", false); return; }
        const submitBtn = card.querySelector('button[type="submit"]');
        submitBtn.disabled = true;
        try {
          await opOrThrow(
            {
              operationType: "UpdateMenuItem", class: "menuitem",
              reads: [menuItemKey, menuItemKey + ".price"],
              payload: { menuItemKey, name, priceCents: cents },
            },
            "save the item"
          );
          toast("Saved " + name + ".", true);
          setTimeout(loadManageMenu, 700);
        } catch (e) {
          toast(e.message, false);
          submitBtn.disabled = false;
        }
      });
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

// residentOwnLeaseRow mirrors, for the resident's own single lease, the
// /api/residents row fillLeaseSelect's picker reads for every lease on the
// POS/front-desk side — what fillLeaseSelect's gateOnApproval and
// tenancyEnded checks disable an OPTION on there (LeaseNotApproved /
// TenancyEnded — OpenTab itself refuses both): the self-service Open Tab
// flow picks no lease from a list, so there is no option to disable, only
// the one button to withhold. Resolved by loadResident before the first
// render, off /api/residents (a resident-readable roster — unlike
// /api/frontdesk-lease-details, which is staff-only). Stays null while
// unresolved (or after a transient fetch failure), the same fail-open state
// residentOpenTabAllowed(null, now) treats as allowed.
let residentOwnLeaseRow = null;

// residentOpenTabAllowed decides whether the resident's own Open Tab button
// renders, from the /api/residents row for their lease (or none, if the
// roster hasn't resolved) and the caller's current time. Blocks only on
// POSITIVE evidence — row.approved === false (LeaseNotApproved, the posture
// fillLeaseSelect's own `approved === false` check takes) or the row's own
// endedAt/leaseEnd having ended the tenancy (TenancyEnded, tenancyEnded
// applied above) — so a roster a resident's session cannot yet join, or a
// lease with no projected term, never wrongly withholds their own tab.
// housePolicyCurrentLine is the House tab limit panel's sentence about the
// staffer's own workplace: its recorded limit, "closed" at $0, or that no
// limit is recorded — and, because the op applies the TIGHTEST policy on a
// resident's chain, a second clause naming any other policy in `policies`
// (the chains of the leases this workplace covers, /api/house-policies)
// that binds tighter than the workplace's own, so the panel never promises
// "any total" under a property-wide cap. "" when the session carries no
// workplace at all (the panel then says so and offers no form).
function housePolicyCurrentLine(policy, locationKey, policies) {
  if (!locationKey) return "";
  const where = policy && policy.name ? policy.name : shortKey(locationKey);
  let line;
  if (!policy) line = "No house tab limit recorded at " + where + " — residents may self-order any total.";
  else if (policy.tabLimitCents === 0) line = "Self-service tabs are closed at " + where + " (limit $0.00).";
  else line = "House tab limit at " + where + ": " + money(policy.tabLimitCents) + " on what a resident may owe on self-service — balance plus open tab.";
  const own = policy ? policy.tabLimitCents : Infinity;
  let tighter = null;
  (policies || []).forEach((p) => {
    if (p.locationKey === locationKey || typeof p.tabLimitCents !== "number" || p.tabLimitCents >= own) return;
    if (!tighter || p.tabLimitCents < tighter.tabLimitCents) tighter = p;
  });
  if (tighter) {
    const from = tighter.name || shortKey(tighter.locationKey);
    line += tighter.tabLimitCents === 0
      ? " Self-service tabs are closed by the policy at " + from + " (limit $0.00), which binds here."
      : " A tighter limit of " + money(tighter.tabLimitCents) + " at " + from + " binds here.";
  }
  return line;
}

// loadHousePolicy fills the Manage Menu view's House tab limit panel with
// the staffer's own workplace's recorded policy (/api/house-policies, the
// cafeHousePolicies lens, P5) and wires the form that records one
// (SetCafePolicy, confined to a location the staffer worksAt — the same
// workplace CreateMenuItem serves a new item from).
async function loadHousePolicy() {
  const current = document.getElementById("house-policy-current");
  const form = document.getElementById("house-limit-form");
  const locationKey = workplaceLocationKey();
  if (!locationKey) {
    current.textContent = "Your session carries no workplace, so there is no house here to set a limit for.";
    form.hidden = true;
    return;
  }
  let policy = null;
  let policies = [];
  try {
    const data = await appGet("/api/house-policies");
    policies = data.policies || [];
    policy = policies.find((p) => p.locationKey === locationKey) || null;
  } catch (e) {
    current.textContent = e.message;
    form.hidden = true;
    return;
  }
  current.textContent = housePolicyCurrentLine(policy, locationKey, policies);
  form.hidden = false;
  const input = document.getElementById("house-limit-dollars");
  if (policy && !input.value) input.value = (policy.tabLimitCents / 100).toFixed(2);
}

function residentOpenTabAllowed(row, now) {
  return !row || (row.approved !== false && !tenancyEnded(row, now));
}

// pendingCafeCharges holds Charge ops this session just submitted whose
// cafeTabs projection hasn't landed yet (measured live at up to ~40s) — a
// naive re-fetch right after would show the tab's pre-charge totalCents
// unchanged, so a resident sees their new item vanish for the price of a
// misleading $0.00-look flash right after the success toast. Each entry
// clears itself the next time renderResident() observes the tab's real
// totalCents move past what it was when the charge went in.
let pendingCafeCharges = []; // { tabKey, name, priceCents, baselineTotalCents }

// posTabLimitByLease is the POS/Resident-view lease picker's own copy of
// each lease's house tab limit (tabLimitOf per /api/residents row), filled by
// loadLeasePickerContext — the desk's cards read it; the resident's own view
// reads residentOwnLeaseRow instead.
let posTabLimitByLease = {};

// residentViewResidentsByLease is the Resident-view lease picker's own copy
// of the lease → resident identity join (/api/residents bookerKey per
// leaseAppKey), filled by loadLeasePickerContext alongside posTabLimitByLease
// — what renderResident asks isOwnAccount about when the desk is viewing a
// lease here. The resident's own view never reads it (selfMode draws no
// refund buttons at all).
let residentViewResidentsByLease = {};

async function loadResident() {
  const select = document.getElementById("resident-lease");
  const label = document.getElementById("resident-lease-label");
  const search = document.getElementById("resident-lease-search");
  const leases = await loadLeases();
  if (isFrontDesk()) {
    label.hidden = false;
    select.hidden = false;
    search.hidden = false;
    const ctx = await loadLeasePickerContext();
    posTabLimitByLease = ctx.tabLimitByLease;
    residentViewResidentsByLease = ctx.residentsByLease;
    fillLeaseSelect(select, leases, ctx.residentsByLease, ctx.leaseDetailsByLease, ctx.approvedByLease, false, ctx.balancesByLease, ctx.openTabByLease);
  } else {
    label.hidden = true;
    select.hidden = true;
    search.hidden = true;
    residentOwnLeaseAppKey = leases.length ? leases[0].leaseAppKey : "";
    residentOwnLeaseRow = null;
    if (residentOwnLeaseAppKey) {
      try {
        const rs = await appGet("/api/residents");
        residentOwnLeaseRow = (rs.residents || []).find((r) => r.leaseAppKey === residentOwnLeaseAppKey) || null;
      } catch (_) { /* roster unreachable — stays null, never wrongly withheld */ }
    }
  }
  await renderResident();
}

// refusal-courtesy: OpenTab/CreditHold: hide — renderResident shows the "account is on hold" panel instead of the Open Tab button when openTabGate(ledger) === "hold"
// refusal-courtesy: OpenTab/InvalidState: none — the arrears aspect's wrong class is a data-integrity fault (require_no_credit_hold, packages/cafe-domain/ddls.go), not state /api/ledger exposes
// refusal-courtesy: Charge/InvalidState: none — the café account's balance aspect of the wrong class is a data-integrity fault (house_exposure_balance, packages/cafe-domain/ddls.go), not state /api/ledger exposes
// refusal-courtesy: OpenTab/LeaseNotApproved: hide — loadResident resolves residentOwnLeaseRow from /api/residents before the first render, and this function renders the "awaiting landlord approval" panel, no button, when its approved field is false
// refusal-courtesy: OpenTab/OpenTabAlreadyExists: hide — the Open Tab button only renders on the `!open` branch (tabs.tabs.find(t => t.status === "open") absent)
// refusal-courtesy: OpenTab/TenancyEnded: hide — residentOpenTabAllowed hides Open Tab once residentOwnLeaseRow's endedAt is recorded or its leaseEnd column (cafeLeaseWorkplaces, packages/cafe-domain/lenses.go, joined onto /api/residents by cmd/cafe-app/residents.go) is past, and this function renders the "your lease ended" panel, no button, in its place
// refusal-courtesy: Charge/ItemUnavailable: disable — menuOptions renders a sold-out item (available === false) inside a disabled "Sold out today" optgroup on the self-order-form select
// refusal-courtesy: Charge/TabLimitExceeded: disable — menuOptions(items, tabLimitRemaining(limitCents, openDisplayTotal, balanceCents)) renders an item priced above the room left under the resident's open EXPOSURE (the recorded ledger balance plus the tab's own total, against the lease's tabLimitCents from /api/residents — the same bound the op's Charge refusal applies) inside a disabled "Over your tab limit" optgroup on the self-order-form select, and hides the Add to Tab button once nothing fits. The FE's balanceCents is the statement sum (/api/ledger.balanceCents, cafeLedgerHistory); the op reads the account's recorded .balance cache (house_exposure_balance, packages/cafe-domain/ddls.go) — equal on every account a cafe-ledger >= 0.4.0 install minted, and on a legacy account carrying no .balance the FE reads 0 (withholds, over-refuses) while the op also reads 0 (allows): the FE's disagreement, when it happens, is always the safe direction, never a false "allowed"
// refusal-courtesy: OpenTab/TabLimitExceeded: hide — this function renders the "self-service tabs are closed" / "pay at the desk" panel, no button, when the lease's tabLimitCents (residentOwnLeaseRow, /api/residents) is 0 or the ledger's balanceCents is at or past it, the two values the script refuses OpenTab's self leg on
// refusal-courtesy: Charge/TabNotOpen: hide — self-order-form only renders inside the `if (open)` branch
// refusal-courtesy: Settle/TabNotOpen: hide — resident-settle-btn only renders inside the `if (open)` branch
// refusal-courtesy: Settle/PaidMismatchesTab: unreachable — resident-settle-btn submits no paidCents, and the script only raises the code when the field is present
// refusal-courtesy: Settle/UnservedLines: disable — resident-settle-btn renders disabled while unservedLineCount({ lines: openDisplayLines }) > 0 (counting the just-submitted {pending: true} overlay too), the hint naming the desk
// refusal-courtesy: CreditCafeAccount/InvalidState, NoCreditToPayOut, PayoutExceedsCash, PayoutExceedsCredit, RefundExceedsCharge, RefundExceedsPaid: see handleWriteOffDebt
// refusal-courtesy: CreditCafeAccount/NoBalanceToPay: hide — both #record-payment-form (desk) and #self-pay-form (resident) render only when ledger.accountKey exists and (ledger.balanceCents||0) > 0
// refusal-courtesy: CreditCafeAccount/PaymentExceedsBalance: cap — both forms' amount input is prefilled to ledger.balanceCents/100 and its `max` is set to the same value
// refusal-courtesy: CreditCafeAccount/WriteOffExceedsBalance: unreachable — neither #record-payment-form's nor #self-pay-form's submit handler ever sets prefill.reason, so it defaults server-side to "payment" (post_entry, packages/cafe-ledger/scripts.go); the reason=="waiver" branch that raises WriteOffExceedsBalance never runs from either form here
// refusal-courtesy: CreditCafeAccount/SelfClearing: unreachable — neither #record-payment-form's nor #self-pay-form's submit handler ever sets prefill.reason, so both post a "payment" credit, and require_not_own_account (post_entry, packages/cafe-ledger/scripts.go) runs only for a waiver, refund or payout
// refusal-courtesy: CreditCafeAccount/CounterPaymentAlreadyPosted, CounterPaymentMismatch, NoCounterPayment, TabNotSettled: unreachable — neither #record-payment-form's nor #self-pay-form's submit handler ever sets prefill.tabRef (the Weaver-only field only cafeTabSettlement's missing_payment dispatch sets); require_counter_payment (packages/cafe-ledger/scripts.go) only runs when payload.tabRef is present
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
    body.innerHTML = '<div class="empty">' + escapeHtml(e.message) + "</div>";
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
  // The lease's house tab limit: the resident's own /api/residents row in
  // self mode, the picker's copy when the desk is viewing a lease here.
  const limitCents = selfMode ? tabLimitOf(residentOwnLeaseRow) : tabLimitOf({ tabLimitCents: posTabLimitByLease[leaseAppKey] });
  // The recorded ledger balance — the other half of the resident's open
  // EXPOSURE the house limit bounds, alongside the tab's own total.
  const balanceCents = (ledger && ledger.balanceCents) || 0;
  const parts = [];
  if (open) {
    const limitLine = houseLimitLine(limitCents, openDisplayTotal, balanceCents);
    // Counted over openDisplayLines, not open — a self-order this session
    // just submitted shows as a {pending: true} overlay before the tab's own
    // lines catch up, and unservedLineCount counts a pending line as
    // unserved: Settle My Tab must not stay enabled over an order the
    // Processor would refuse to settle a moment later.
    const residentUnserved = unservedLineCount({ lines: openDisplayLines });
    parts.push(
      '<div class="panel"><h2>Open tab</h2><p class="amount">' + money(openDisplayTotal) +
      '</p><p class="meta">Opened ' + escapeHtml(localDateTime(open.openedAt)) + " — not yet settled</p>" +
      (limitLine ? '<p class="meta" id="resident-house-limit">' + escapeHtml(limitLine) + "</p>" : "") +
      chargeLinesBlock(openDisplayLines, open.itemsMemo, null, true) + "</div>" +
      (selfMode ? residentSettlePanelMarkup(residentUnserved) : "")
    );
    if (selfMode) {
      const items = (menu && menu.menu) || [];
      // The room left under the house limit bounds the picker the way the
      // op bounds the Charge: an item that would take the resident's open
      // exposure (balance + tab) past the limit is offered disabled, and
      // once nothing fits the button goes too.
      const remaining = tabLimitRemaining(limitCents, openDisplayTotal, balanceCents);
      const itemsHasAvailable = items.some((it) => it.available !== false && !(remaining !== null && it.priceCents > remaining));
      const itemsHasOnMenu = items.some((it) => it.available !== false);
      parts.push(
        '<div class="panel" style="max-width:640px;">' +
        "<h2>Order</h2>" +
        (items.length
          ? '<form id="self-order-form" class="field-row">' +
            '<select id="self-order-item">' +
            menuOptions(items, remaining) +
            "</select>" +
            (itemsHasAvailable ? '<button id="self-order-submit" type="submit">Add to Tab</button>' : "") +
            "</form>" +
            (itemsHasAvailable ? "" : (itemsHasOnMenu
              // The room is exhausted by the balance alone when the tab
              // itself is still empty (openDisplayTotal === 0) and the
              // balance is what ate it (balanceCents > 0) — "your tab" would
              // name the wrong thing to pay down.
              ? (openDisplayTotal === 0 && balanceCents > 0
                  ? '<p class="meta">Your balance is at the house limit of ' + escapeHtml(money(limitCents)) + " — ask the desk.</p>"
                  : '<p class="meta">Your tab is at the house limit of ' + escapeHtml(money(limitCents)) + " — ask the desk.</p>")
              : '<p class="meta">Nothing on the menu right now.</p>'))
          : '<p class="meta">No menu items available yet.</p>') +
        "</div>"
      );
    }
  } else if (selfMode && openTabGate(ledger) === "hold") {
    // The resident's own copy of the POS hold panel: the ledger response
    // carries the same balance fields, and the op refuses the resident's
    // OpenTab on the same condition — the button would only toast.
    parts.push(
      '<div class="panel">' +
      "<h2>Your café account is on hold</h2>" +
      '<p class="lead">You owe ' + escapeHtml(money(ledger.balanceCents)) +
      (ledger.isOverdue ? " (" + escapeHtml(overdueDaysPhrase(ledger)) + "; a reminder was sent " : " (a reminder was sent ") +
      escapeHtml(reminderSentDate(ledger)) + "). Pay your balance below to open a new tab.</p>" +
      "</div>"
    );
  } else if (selfMode && residentOwnLeaseRow && residentOwnLeaseRow.approved === false) {
    // refusal-courtesy: OpenTab/LeaseNotApproved: hide — this panel replaces
    // the Open Tab button with the reason, mirroring fillLeaseSelect's own
    // disabled-option copy on the POS/front-desk picker (loadResident resolves
    // residentOwnLeaseRow from /api/residents before the first render).
    parts.push(
      '<div class="panel">' +
      "<h2>No open tab</h2>" +
      '<p class="lead">Your lease is awaiting landlord approval — a house tab cannot open until then.</p>' +
      "</div>"
    );
  } else if (selfMode && !residentOpenTabAllowed(residentOwnLeaseRow, new Date())) {
    // refusal-courtesy: OpenTab/TenancyEnded: hide — this panel replaces the
    // Open Tab button once residentOpenTabAllowed(residentOwnLeaseRow, now)
    // is false for a reason other than approval (checked above), i.e. the
    // lease's own endedAt is recorded or its leaseEnd column has been
    // reached. Names endedAt (an early move-out, or the term run out) when
    // recorded, else leaseEnd. The calendar date of the UTC stamp, the same
    // slice fillLeaseSelect's own disabled-option copy uses — a local
    // rendering of a midnight-UTC term end names the day before in every
    // zone west of Greenwich.
    parts.push(
      '<div class="panel">' +
      "<h2>No open tab</h2>" +
      '<p class="lead">Your lease ended ' + escapeHtml((residentOwnLeaseRow.endedAt || residentOwnLeaseRow.leaseEnd).slice(0, 10)) + " — a house tab cannot open once your tenancy has ended.</p>" +
      "</div>"
    );
  } else if (selfMode && limitCents === 0) {
    // The house closed self-service tabs (a recorded $0 limit): the op
    // refuses the resident's OpenTab, so the button would only toast; the
    // desk can still open and ring a tab for them.
    parts.push(
      '<div class="panel">' +
      "<h2>No open tab</h2>" +
      '<p class="lead">Self-service tabs are closed at this house — ask the desk to open a tab for you.</p>' +
      "</div>"
    );
  } else if (selfMode && limitCents !== null && balanceCents >= limitCents) {
    // The resident's recorded balance alone already meets or exceeds the
    // house limit (no line could join): the op refuses OpenTab on the same
    // balanceCents >= limit conjunct, so the button would only toast; the
    // desk can still open and ring a tab for them; the resident's own
    // self-pay form further down this view, or a counter payment, lifts the
    // balance below the limit first.
    parts.push(
      '<div class="panel">' +
      "<h2>No open tab</h2>" +
      '<p class="lead">Your account owes ' + escapeHtml(money(balanceCents)) + " against a house limit of " + escapeHtml(money(limitCents)) + " — pay your balance (below, or at the desk) to open a tab.</p>" +
      "</div>"
    );
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
    const paidLine = counterPaymentLine(pendingSettled);
    parts.push(
      '<div class="panel"><h2>Pending posting</h2><p class="amount">' + money(pendingSettled.totalCents) +
      '</p><p class="meta">Settled ' + escapeHtml(localDateTime(pendingSettled.settledAt)) + " — posting to the ledger shortly" +
      (paidLine ? " · " + escapeHtml(paidLine) : "") + "</p>" +
      chargeLinesBlock(pendingSettled.lines, pendingSettled.itemsMemo, null, false) + "</div>"
    );
  }
  const rows = ledger.transactions || [];
  // Every tab this lease has ever held (open, pending, settled) keyed by its
  // own tabKey — receiptLines joins a ledger row onto the one it names, so a
  // posted charge can show what it was for. Both the resident and the desk
  // use renderResident, so one join covers both.
  const tabByKey = {};
  (tabs.tabs || []).forEach((t) => { tabByKey[t.tabKey] = t; });
  // A refund's own row names the charge it gives back (reversesKey — the
  // reverses link cafe-ledger writes, projected by cafeLedgerHistory), so the
  // statement can say WHICH charge rather than showing a credit that reads
  // exactly like cash the resident handed over. The reversed charge is on the
  // same account, so it is in this very list; a row whose reversesKey does not
  // resolve here (a projection still catching up) degrades to the generic
  // phrasing rather than printing a raw transaction key at the resident.
  const rowByKey = {};
  rows.forEach((r) => { rowByKey[r.transactionKey] = r; });
  // How much of each charge has already been given back, summed from the
  // refunds themselves — the same arithmetic the script's own ceiling uses
  // (the charge's amountCents minus what reverses it), read off rows this view
  // already holds rather than a column the lens would have to project. Every
  // refund of a charge is postedTo that charge's own account, so this list is
  // the whole population: a partial refund cannot be hiding on another
  // statement. A refund the projection has not caught up with yet simply is
  // not counted, which leaves the button offering MORE than the script will
  // accept — the submit is refused (RefundExceedsCharge) rather than
  // over-refunding, so the lag costs a retry, never money.
  const refundedByCharge = {};
  rows.forEach((r) => {
    if (!r.reversesKey) return;
    refundedByCharge[r.reversesKey] = (refundedByCharge[r.reversesKey] || 0) + (r.amountCents || 0);
  });
  parts.push(
    '<div class="panel" style="max-width:640px;">' +
    "<h2>Café ledger</h2>" +
    '<p class="ledger-balance">' + ledgerBalanceLine(ledger.balanceCents) + "</p>" +
    statementLine(ledger) +
    (rows.length
      ? '<ul class="ledger-list">' +
        rows
          .map((r) => {
            const reversed = r.reversesKey ? rowByKey[r.reversesKey] : null;
            const refunded = refundedByCharge[r.transactionKey] || 0;
            const remaining = (r.amountCents || 0) - refunded;
            // A tabKey is no longer proof of refundability by itself: the
            // tab-settlement playbook posts it on BOTH sides of a settle
            // that took cash — the debit (missing_charge) AND the counter
            // payment credit (missing_payment) — so the type === "debit"
            // guard below is load-bearing, not redundant with !!r.tabKey. A
            // hand-posted debit carries no tabKey (no counter transaction
            // behind it) and so is excluded the same way. Staff-only:
            // RefundCafeCharge is granted to operator/frontOfHouse and to no
            // consumer at any scope. A charge already given back in full is
            // not offered again: the remaining amount is what the script
            // will accept, so an exhausted charge has no refund left to
            // start. A PayoutCafeCredit debit carries no tabKey either (it
            // settles a credit in cash, not a café purchase), so this
            // predicate already excludes it — a payout is not itself
            // refundable. And never on the viewer's own account: the script
            // refuses a staffer's refund of their own charge SelfClearing,
            // so the button is withheld when the picked lease's resident is
            // the viewer (residentViewResidentsByLease, the desk picker's
            // roster join).
            const refundable = !selfMode && !isOwnAccount(residentViewResidentsByLease[leaseAppKey]) &&
              r.type === "debit" && !!r.tabKey && remaining > 0;
            const receipt = receiptLines(r, tabByKey);
            return (
              '<li class="ledger-entry ' + escapeHtml(r.type) + (r.reversesKey ? " refund" : "") + '">' +
              (r.reversesKey ? '<span class="badge-refund">Refund</span>' : "") +
              (r.type === "credit" && r.reason === "waiver" ? '<span class="badge-refund">Waived</span>' : "") +
              (r.type === "debit" && r.reason === "payout" ? '<span class="badge-refund">Paid out</span>' : "") +
              (r.type === "debit" ? "+" : "−") + money(r.amountCents) +
              (r.memo ? " — " + escapeHtml(customerMemo(r.memo)) : "") +
              " (" + escapeHtml(localDateTime(r.postedAt)) + ")" +
              (r.reversesKey
                ? ' <span class="refund-of">reverses the charge of ' +
                  (reversed ? escapeHtml(localDateTime(reversed.postedAt)) : "an earlier charge") +
                  "</span>"
                : "") +
              (refunded > 0
                ? ' <span class="refund-note">' +
                  (remaining > 0
                    ? "refunded " + money(refunded) + " of " + money(r.amountCents)
                    : "fully refunded") +
                  "</span>"
                : "") +
              (refundable
                ? '<span class="ledger-entry-actions"><button type="button" class="refund-charge-btn" data-tx="' +
                  escapeHtml(r.transactionKey) + '" data-amount="' + remaining +
                  '" data-posted="' + escapeHtml(r.postedAt || "") +
                  '" data-memo="' + escapeHtml(r.memo || "") + '">Refund</button></span>'
                : "") +
              (receipt
                ? '<details class="receipt"><summary>Receipt</summary>' +
                  chargeLinesBlock(receipt.lines, receipt.memo, null, false) +
                  "</details>"
                : "") +
              "</li>"
            );
          })
          .join("") +
        "</ul>" +
        (selfMode ? "" : '<div id="refund-form-host"></div>')
      : '<p class="meta">No posted café charges yet.</p>') +
    // Front desk records a payment handed over at the counter; a resident
    // instead pays down their OWN balance self-service (self-scoped
    // CreditCafeAccount — packages/cafe-ledger's consumer scope=self grant).
    // Both legs are bounded server-side and identically: packages/cafe-ledger
    // caps a payment at the account's own maintained .balance whoever
    // submitted it, and proves the self leg's ownership against the account's
    // own heldFor→leaseapp→applicationFor topology — so a forged accountKey
    // or an over-balance amount only fails closed, from either form, and
    // nothing here needs to be trusted client-side. Both forms therefore
    // render on the same condition — an account that exists AND owes
    // something — prefilled with what is owed and capped there by `max`. That
    // cap is a courtesy on the typing, never the enforcement; what it buys is
    // that neither a resident nor a staffer is offered a payment field on a
    // settled tab, where every submit would be refused.
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
        ? (ledger.balanceCents || 0) > 0
          ? '<form id="record-payment-form" class="field-row" style="margin-top:14px;">' +
            '<input id="record-payment-amount" type="number" step="0.01" min="0.01" max="' +
            (ledger.balanceCents / 100).toFixed(2) +
            '" placeholder="Payment ($)" value="' + (ledger.balanceCents / 100).toFixed(2) + '" required />' +
            '<input id="record-payment-memo" type="text" placeholder="Memo (optional — shown to the resident)" />' +
            '<button id="record-payment-submit" type="submit">Record Payment</button>' +
            "</form>"
          : '<p class="meta" style="margin-top:14px;">Nothing owed on this tab — no payment to record.</p>'
        : '<p class="meta" style="margin-top:14px;">No café account for this lease yet — nothing to credit.</p>') +
    "</div>"
  );
  body.innerHTML = parts.join("");
  wireRefundCharge(ledger.accountKey, renderResident);
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
  // per-click actingAsSelf() to compute the way clinic's does).
  //
  // context.me is still set on this staff leg, and buildAuthContext is not
  // why: with selfVoice false it ignores context.me entirely and attaches no
  // authContext at all, exactly as a staff submission must. What needs it is
  // the descriptor's ENUMERATION declaration — CreditCafeAccount declares
  // `{actor} holdsRole out` (packages/cafe-ledger/opmetas.go), the walk the
  // script's workplace_exempt short-circuit actually runs, and form.mjs's
  // substituteEnumerations resolves `{actor}` off context.me and drops the
  // entry outright when it is empty. An unset me therefore ships an envelope
  // declaring no enumerations while the script walks anyway — an undeclared
  // read (Contract #2 §2.5), invisible from this side.
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
          me: identityKey(),
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
        if (!confirmOverdueOpen("You", ledger)) return;
        openBtn.disabled = true;
        try {
          await opOrThrow(
            {
              operationType: "OpenTab",
              class: "tab",
              reads: [leaseAppKey],
              optionalReads: [leaseAppKey + ".cafeOpenTab", applicationForOptionalRead(leaseAppKey), leaseAppKey + ".decision", leaseAppKey + ".tenancy"],
              enumerations: [{ hub: leaseAppKey, relation: "heldFor", direction: "in" }],
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
        if (settleBtn.disabled) return;
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
          // The count this button disabled on can go stale between renders
          // (a pending self-order lands, or the desk marks/voids a line) —
          // an UnservedLines refusal re-renders so the card catches up
          // rather than leaving a dead click behind.
          if (e.message && e.message.indexOf("UnservedLines") !== -1) {
            setTimeout(renderResident, 700);
          }
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
              // The self leg's exposure bound walks the lease's café
              // account the same way OpenTab's own envelope above does
              // (house_exposure_balance, packages/cafe-domain/ddls.go).
              enumerations: [{ hub: leaseAppKey, relation: "heldFor", direction: "in" }],
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

// wireRefundCharge wires the front desk's per-row "Refund" buttons on the café
// statement. Clicking one opens RefundCafeCharge's descriptor form in the
// visible #refund-form-host mount beneath the ledger list, prefilled with the
// charge being reversed and its full amount.
//
// A VISIBLE mount, unlike the payment form's detached one, because there is
// nothing already typed for a detached mount to assemble from: a refund is
// often only part of a charge, and its memo is written for the resident to
// read on their own statement, so both are the staffer's to set. A load or
// render failure renders its message INLINE into the mount rather than
// toasting — this runs on every renderResident, the 700ms re-render after a
// successful submit included, so a toast on a catalog outage would stomp the
// green success toast still on screen.
//
// RefundCafeCharge is staff-standing: packages/cafe-ledger grants it to
// operator/frontOfHouse at scope=any and to nobody at scope=self, and its
// dispatch declares AuthContext "standing", so buildAuthContext sends no
// authContext object at all. Hence no context.me / selfVoice wiring here —
// and the script refuses outright any submit that does carry a target, so a
// resident cannot reach this op even by hand.
//
// Nothing here is trusted: the reversed charge must be a live DEBIT posted to
// this same account, and the amount may not exceed what that charge still has
// un-refunded, both proven server-side against the charge's own aspects and
// links. An edited reversesRef or an inflated amount only fails closed.
// refusal-courtesy: RefundCafeCharge/InvalidState: none — the account's .balance aspect being a foreign class is a data-integrity fault (post_entry, packages/cafe-ledger/scripts.go), not state any read model exposes
// refusal-courtesy: RefundCafeCharge/NoCreditToPayOut, PayoutExceedsCash, PayoutExceedsCredit: unreachable — RefundCafeCharge calls post_entry(entry_type="credit", ...) (packages/cafe-ledger/scripts.go); is_payout requires entry_type == "debit", so the payout branch never runs
// refusal-courtesy: RefundCafeCharge/RefundExceedsCharge: cap — renderResident only draws the refund-charge-btn when remaining (a charge's amountCents minus refundedByCharge, both read off this same ledger list) is > 0, and this function prefills amountCents to that remaining value and sets the field's max to it, so a larger typed amount never leaves the form
// refusal-courtesy: RefundCafeCharge/RefundExceedsPaid: none — cashCents (the account's cash-floor) is never projected to cmd/cafe-app's read models (ledger.go), so no field here can bound a refund against it
// refusal-courtesy: RefundCafeCharge/SelfClearing: hide — renderResident draws the refund-charge-btn this function wires only when !isOwnAccount(residentViewResidentsByLease[leaseAppKey]), the picked lease's roster bookerKey against state.identityId, so no Refund button exists on the viewer's own ledger for this function to bind
// refusal-courtesy: RefundCafeCharge/NoBalanceToPay, PaymentExceedsBalance, WriteOffExceedsBalance: unreachable — RefundCafeCharge calls post_entry(..., allow_reverses_ref=True, ...) (packages/cafe-ledger/scripts.go); is_payment requires not allow_reverses_ref, so the whole is_payment block these codes live in never runs
// refusal-courtesy: RefundCafeCharge/CounterPaymentAlreadyPosted, CounterPaymentMismatch, NoCounterPayment, TabNotSettled: unreachable — RefundCafeCharge calls post_entry(..., allow_tab_ref=False, ...) and refuses any payload.tabRef before reaching post_entry at all (packages/cafe-ledger/scripts.go); require_counter_payment never runs
async function wireRefundCharge(accountKey, onDone) {
  const host = document.getElementById("refund-form-host");
  if (!host) return;
  const buttons = Array.prototype.slice.call(document.querySelectorAll(".refund-charge-btn"));
  if (!buttons.length) return;
  await loadOpCatalogQuiet();
  let renderOpForm;
  try {
    ({ renderOpForm } = await loadDescriptorform());
  } catch (e) {
    host.innerHTML = '<p class="meta">Refund form unavailable — ' + escapeHtml(e.message) + "</p>";
    return;
  }
  const catalogRow = opCatalogCache && opCatalogCache.RefundCafeCharge;
  if (!catalogRow) {
    host.innerHTML = '<p class="meta">The refund form is unavailable.</p>';
    return;
  }
  // The signed-in staffer's own vertex key, which is what the descriptor's
  // `{actor}` holdsRole enumeration resolves against: form.mjs's
  // substituteTemplate reads `{actor}` (and `{me}`) straight off context.me,
  // and substituteEnumerations DROPS any entry whose hub does not resolve to a
  // whole key. Leaving it unset therefore does not fall back to anything — it
  // sends an envelope declaring no enumerations at all, and the script's
  // actor_holds_operator kv.Links then runs as an UNDECLARED walk (Contract #2
  // §2.5). The value is the full vtx.identity.<NanoID>, the same form the
  // self-pay leg below and the descriptor's hub template both expect.
  let me;
  try {
    me = identityKey();
  } catch (e) {
    host.innerHTML = '<p class="meta">Refund form unavailable — ' + escapeHtml(e.message) + "</p>";
    return;
  }
  buttons.forEach((btn) => {
    btn.addEventListener("click", () => {
      host.innerHTML =
        '<form id="refund-form" style="margin-top:12px;">' +
        '<p class="meta">Refunding the charge of ' + escapeHtml(localDateTime(btn.getAttribute("data-posted"))) + ".</p>" +
        '<div id="refund-fields"></div>' +
        '<div class="panel-actions">' +
        '<button id="refund-submit" type="submit">Refund</button>' +
        '<button id="refund-cancel" type="button">Cancel</button>' +
        "</div></form>";
      const mount = document.getElementById("refund-fields");
      const submitBtn = document.getElementById("refund-submit");
      // The charge's own memo carries forward as the refund's default reason,
      // so the credit line lands beside the charge saying what it was for
      // rather than an unlabelled sum. The staffer overwrites it when the real
      // reason is something else.
      const handle = renderOpForm(
        catalogRow,
        {
          target: accountKey,
          me: me,
          selfVoice: false,
          prefill: {
            reversesRef: btn.getAttribute("data-tx"),
            amountCents: parseInt(btn.getAttribute("data-amount"), 10),
            memo: btn.getAttribute("data-memo") || undefined,
          },
        },
        mount
      );
      if (!handle) {
        host.innerHTML = '<p class="meta">The refund form is unavailable.</p>';
        return;
      }
      // The remaining amount is the most that can still be refunded on this
      // charge (RefundCafeCharge's cumulative cap), so the control is bound
      // to it the way the self-pay amount is bound to the balance: a larger
      // typed amount never leaves the form.
      const amountInput = mount.querySelector('[name="amountCents"]');
      const remaining = parseInt(btn.getAttribute("data-amount"), 10);
      if (amountInput && remaining > 0) amountInput.max = String(remaining);
      submitBtn.textContent = handle.descriptor.submitLabel;
      document.getElementById("refund-cancel").addEventListener("click", () => { host.innerHTML = ""; });
      document.getElementById("refund-form").addEventListener("submit", async (ev) => {
        ev.preventDefault();
        // Left disabled on success rather than re-enabled in a finally: the
        // amount lives inside the descriptor-owned mount this function does
        // not clear, so a re-enabled button would let a double-click inside
        // the 700ms re-render window post a SECOND refund for the same
        // amount. The cumulative cap catches the ones that would overshoot
        // the charge, but a half-refund submitted twice is exactly at the
        // cap and would land.
        submitBtn.disabled = true;
        try {
          const { envelope, reveal } = await handle.submit();
          const amountCents = envelope.payload && envelope.payload.amountCents;
          const reply = await submitCatalogOp(envelope, "refund the charge");
          revealCeremonySecret(reveal, reply);
          toast("Refunded" + (amountCents ? " " + money(amountCents) : "") + ".", true);
          setTimeout(onDone, 700);
        } catch (e) {
          toast(e.message, false);
          submitBtn.disabled = false;
        }
      });
    });
  });
}

// escapeHtml renders one untrusted string safe at BOTH interpolation sites
// this file uses: element text, and a quoted attribute value (every
// `data-…="…"` built by string concatenation above). The quote characters
// carry that second site: free text reaches this DOM from menu item names and
// off-menu charge descriptions a person types, so a memo containing `"` must
// not be able to close the attribute it sits in and open a new one. Escaping
// all five is safe for text content too — a browser renders the entities back
// to the literal characters — so there is one helper, not two.
function escapeHtml(s) {
  const map = { "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" };
  const str = s === null || s === undefined ? "" : String(s);
  return str.replace(/[&<>"']/g, (c) => map[c]);
}

// ---- init --------------------------------------------------------

function init() {
  document.querySelectorAll(".tab").forEach((b) => {
    b.addEventListener("click", () => showView(b.dataset.view));
  });
  document.getElementById("pos-lease").addEventListener("change", renderPos);
  document.getElementById("pos-refresh").addEventListener("click", () => { leasesCache = null; loadPos(); });
  wireLeaseSearch(document.getElementById("pos-lease-search"), document.getElementById("pos-lease"));
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
  document.getElementById("house-limit-form").addEventListener("submit", async (ev) => {
    ev.preventDefault();
    const input = document.getElementById("house-limit-dollars");
    const cents = parseDollarsOrZero(input.value);
    if (cents === null) { toast("Enter a limit in dollars (0 closes self-service tabs).", false); return; }
    const locationKey = workplaceLocationKey();
    if (!locationKey) { toast("Your session carries no workplace to set a limit for.", false); return; }
    const btn = document.getElementById("house-limit-submit");
    btn.disabled = true;
    try {
      // A staff submit on the standing leg: SetCafePolicy is confined
      // in-script by the caller's own worksAt walk over locationKey (the
      // `{actor} holdsRole out` enumeration its descriptor declares), the
      // location is a required read, and its .cafePolicy an optional one —
      // absent mints the aspect, present OCC-upserts it.
      await opOrThrow(
        {
          operationType: "SetCafePolicy", class: "menuitem",
          reads: [locationKey],
          optionalReads: [locationKey + ".cafePolicy"],
          enumerations: [{ hub: identityKey(), relation: "holdsRole", direction: "out" }],
          payload: { locationKey, tabLimitCents: cents },
        },
        "set the house tab limit"
      );
      toast(cents === 0 ? "Self-service tabs closed at this house." : "House tab limit set to " + money(cents) + ".", true);
      setTimeout(loadHousePolicy, 700);
    } catch (e) {
      toast(e.message, false);
    } finally {
      btn.disabled = false;
    }
  });
  document.getElementById("resident-lease").addEventListener("change", renderResident);
  document.getElementById("resident-refresh").addEventListener("click", () => { leasesCache = null; loadResident(); });
  wireLeaseSearch(document.getElementById("resident-lease-search"), document.getElementById("resident-lease"));
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
