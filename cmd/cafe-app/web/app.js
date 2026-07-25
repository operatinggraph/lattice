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

// ---- formatting --------------------------------------------------------

function money(cents) {
  const n = (cents || 0) / 100;
  return "$" + n.toFixed(2);
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

// isFrontDesk marks a session that works the café's front of house. The
// signal is the whoami `worksAt` anchor (persona-worlds-design.md §4);
// gating rides anchors, not roles — whoami's roles[] arrives as opaque role
// vertex keys, never canonical names, so it cannot name the frontOfHouse
// role FE-side. UX curation only: the graph's grants + this app's own
// server-side read scoping remain the authority.
function isFrontDesk() {
  return Array.isArray(state.anchors) && state.anchors.some((a) => a && a.relation === "worksAt");
}

function refreshMeBar() {
  const status = document.getElementById("me-status");
  const signOutBtn = document.getElementById("sign-out");
  status.textContent = state.identityId
    ? "Signed in as " + shortKey(state.identityId) + (isFrontDesk() ? " (front of house)" : " (resident)")
    : "";
  signOutBtn.hidden = !state.canSignOut;
}

function signOut() {
  fetch("/api/logout", { method: "POST", credentials: "same-origin" })
    .catch(() => {})
    .finally(() => location.replace("/login"));
}

// applyHatGating hides the staff-only tabs (POS, Front Desk) from a session
// that lacks the worksAt anchor, and bounces the active view to Resident if
// it just became disallowed. Idempotent; re-run whenever whoami resolves.
function applyHatGating() {
  const fd = isFrontDesk();
  document.getElementById("tab-pos").hidden = !fd;
  document.getElementById("tab-frontdesk").hidden = !fd;
  refreshMeBar();
  const active = document.querySelector(".tab.active");
  if (active && active.hidden) showView("resident");
}

// ---- view routing -------------------------------------------------

function showView(view) {
  if ((view === "pos" || view === "frontdesk") && !isFrontDesk()) view = "resident";
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

function fillLeaseSelect(select, leases) {
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
    opt.textContent = shortKey(l.leaseAppKey) + (l.accountKey ? "" : " (no café account yet)");
    select.appendChild(opt);
  }
  if (prev && leases.some((l) => l.leaseAppKey === prev)) select.value = prev;
}

// ---- POS view (staff only) --------------------------------------------

async function loadPos() {
  const select = document.getElementById("pos-lease");
  const leases = await loadLeases();
  fillLeaseSelect(select, leases);
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
  let tabs;
  try {
    const r = await appGet("/api/tabs?leaseAppKey=" + encodeURIComponent(leaseAppKey));
    tabs = r.tabs || [];
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
            optionalReads: [leaseAppKey + ".cafeOpenTab"],
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
  body.innerHTML = renderOpenTabCard(open);
  document.getElementById("charge-form").addEventListener("submit", async (ev) => {
    ev.preventDefault();
    const input = document.getElementById("charge-amount");
    const cents = parseDollars(input.value);
    if (cents === null) { toast("Enter a charge amount greater than $0.", false); return; }
    const btn = document.getElementById("charge-submit");
    btn.disabled = true;
    try {
      await opOrThrow(
        {
          operationType: "Charge", class: "tab",
          reads: [open.tabKey, open.tabKey + ".status"],
          payload: { tabKey: open.tabKey, amountCents: cents },
        },
        "add the charge"
      );
      toast("Charged " + money(cents) + ".", true);
      input.value = "";
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

function renderOpenTabCard(tab) {
  return (
    '<div class="panel">' +
    "<h2>Open tab</h2>" +
    '<p class="amount">' + money(tab.totalCents) + "</p>" +
    '<p class="meta">Opened ' + (tab.openedAt || "?") + "</p>" +
    '<form id="charge-form" class="field-row" style="margin-bottom:14px;">' +
    '<input id="charge-amount" type="number" step="0.01" min="0.01" placeholder="Amount ($)" required />' +
    '<button id="charge-submit" type="submit">Add Charge</button>' +
    "</form>" +
    '<div class="panel-actions"><button id="settle-btn" class="danger">Settle Tab</button></div>' +
    "</div>"
  );
}

// ---- Front Desk view (staff only) --------------------------------------

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
    (br.bookings || []).forEach((b) => { bookingsByLease[b.leaseAppKey] = b; });
  } catch (_) { /* front-desk not installed / unreachable — badges just don't show */ }

  // Same join, for the resident's applied-to unit rent/term — every open
  // tab's lease, not just those with a booked class (best-effort, same
  // degrade-to-hidden posture as bookingsByLease above).
  let leaseDetailsByLease = {};
  try {
    const ld = await appGet("/api/frontdesk-lease-details");
    (ld.leaseDetails || []).forEach((d) => { leaseDetailsByLease[d.leaseAppKey] = d; });
  } catch (_) { /* front-desk not installed / unreachable — lease details just don't show */ }

  // Same join, for the resident's own upcoming clinic visit — existence +
  // time only, never the visit reason (front-desk's frontDeskVisits lens
  // never projects it). Best-effort, same degrade-to-hidden posture as above.
  let visitsByLease = {};
  try {
    const vs = await appGet("/api/frontdesk-visits");
    (vs.visits || []).forEach((v) => { visitsByLease[v.leaseAppKey] = v; });
  } catch (_) { /* front-desk not installed / unreachable — visit badge just doesn't show */ }

  summary.textContent = tabs.length + " open tab" + (tabs.length === 1 ? "" : "s");
  if (!tabs.length) {
    grid.innerHTML = '<div class="empty">No open tabs.</div>';
    return;
  }
  grid.innerHTML = tabs.map((t) => frontDeskCard(t, bookingsByLease[t.leaseAppKey], leaseDetailsByLease[t.leaseAppKey], visitsByLease[t.leaseAppKey])).join("");
  tabs.forEach((t) => {
    const btn = document.getElementById("settle-" + t.tabKey.replace(/[^a-zA-Z0-9]/g, ""));
    if (!btn) return;
    btn.addEventListener("click", async () => {
      btn.disabled = true;
      try {
        await opOrThrow(
          { operationType: "Settle", class: "tab", reads: [t.tabKey, t.tabKey + ".status"], payload: { tabKey: t.tabKey } },
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

function frontDeskCard(t, booking, lease, visit) {
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
  return (
    '<div class="card">' +
    '<span class="badge open">open</span>' +
    '<div class="who">' + shortKey(t.leaseAppKey) + "</div>" +
    '<div class="amount">' + money(t.totalCents) + "</div>" +
    '<div class="meta">Opened ' + (t.openedAt || "?") + "</div>" +
    classBadge +
    leaseLine +
    visitBadge +
    '<div class="card-actions"><button id="' + id + '" class="danger">Settle</button></div>' +
    "</div>"
  );
}

// ---- Resident view ------------------------------------------------
//
// A resident's own lease is resolved from /api/leases, which the server
// already scopes to the signed-in identity (persona-worlds-design.md Fire W4
// §3) — no client-side identity picker needed. A staff session instead sees
// the full lease picker, for front-of-house lookups.

let residentOwnLeaseAppKey = "";

async function loadResident() {
  const select = document.getElementById("resident-lease");
  const label = document.getElementById("resident-lease-label");
  const leases = await loadLeases();
  if (isFrontDesk()) {
    label.hidden = false;
    select.hidden = false;
    fillLeaseSelect(select, leases);
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
    if (selfMode) fetches.push(appGet("/api/menu"));
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
  const parts = [];
  if (open) {
    parts.push(
      '<div class="panel"><h2>Open tab</h2><p class="amount">' + money(open.totalCents) +
      '</p><p class="meta">Opened ' + (open.openedAt || "?") + " — not yet settled</p></div>" +
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
      '</p><p class="meta">Settled ' + (pendingSettled.settledAt || "?") + " — posting to the ledger shortly</p></div>"
    );
  }
  const rows = ledger.transactions || [];
  parts.push(
    '<div class="panel" style="max-width:640px;">' +
    "<h2>Café ledger</h2>" +
    '<p class="ledger-balance">Balance: ' + money(ledger.balanceCents) + "</p>" +
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
    "</div>"
  );
  body.innerHTML = parts.join("");
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
              optionalReads: [leaseAppKey + ".cafeOpenTab", applicationForOptionalRead(leaseAppKey)],
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
              optionalReads: [applicationForOptionalRead(leaseAppKey)],
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
          toast("Added to your tab.", true);
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
  document.getElementById("resident-lease").addEventListener("change", renderResident);
  document.getElementById("resident-refresh").addEventListener("click", () => { leasesCache = null; loadResident(); });
  document.getElementById("sign-out").addEventListener("click", signOut);

  // Who signed in decides every derived affordance (staff vs. resident), so
  // it has to be known before the first render that reads it.
  loadWhoami().then(() => {
    applyHatGating();
    showView(isFrontDesk() ? "pos" : "resident");
  });
}

init();
