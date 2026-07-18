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
// persisted across reloads. OpenTab/Settle's consumer scope=self grant
// requires authContext.target to name that identity (packages/cafe-domain/
// permissions.go), so signing in is just picking which resident you are —
// mirrors cmd/wellness-app's own Me bar.
const SELF_BOOKER_STORAGE_KEY = "cafe.selfBookerKey";
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
// uses the staff Bearer token (Charge's operator scope:any covers it, and
// so does the operator half of OpenTab/Settle); passing opts.asSelf instead
// submits with a token minted for the signed-in resident's own identity,
// and stamps authContext.target so the platform's scope=self check
// (op.actor == authContext.target) and cafe-domain's own applicationFor
// indirection check both resolve to that resident.
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

async function opOrThrow(body, what, opts) {
  const reply = await submitOp(body, opts);
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

// applicationForOptionalRead returns the OpenTab/Settle self-scope guard's
// declared read (packages/cafe-domain/ddls.go): a resident submitting
// asSelf must declare the lease's applicationFor→identity link so the
// Starlark script can confirm the lease is theirs without a live GET.
function applicationForOptionalRead(leaseAppKey, bookerKey) {
  return "lnk.leaseapp." + idOf(leaseAppKey) + ".applicationFor.identity." + idOf(bookerKey);
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
  if (view === "pos") loadPos();
  else if (view === "frontdesk") loadFrontDesk();
  else if (view === "resident") loadResident();
}

// ---- leases (shared picker data) -----------------------------------

let leasesCache = null;
async function loadLeases() {
  if (leasesCache) return leasesCache;
  const body = await api("/api/leases");
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

// ---- residents (Me bar picker data) ---------------------------------

let residentsCache = null;
async function loadResidents() {
  if (residentsCache) return residentsCache;
  const body = await api("/api/residents");
  residentsCache = body.residents || [];
  return residentsCache;
}

function fillResidentSelect(select, residents) {
  select.innerHTML = "";
  if (!residents.length) {
    const opt = document.createElement("option");
    opt.textContent = "(no residents)";
    opt.value = "";
    select.appendChild(opt);
    return;
  }
  const opt0 = document.createElement("option");
  opt0.value = "";
  opt0.textContent = "(choose a resident)";
  select.appendChild(opt0);
  for (const r of residents) {
    const opt = document.createElement("option");
    opt.value = r.bookerKey;
    opt.textContent = shortKey(r.bookerKey) + (r.approved ? " (resident)" : " (applicant)");
    select.appendChild(opt);
  }
}

// leaseAppKeyForBooker looks up a signed-in resident's own lease from the
// resident roster, so the Resident view can resolve which lease is
// "theirs" without a protected read model.
function leaseAppKeyForBooker(bookerKey, residents) {
  const r = residents.find((x) => x.bookerKey === bookerKey);
  return r ? r.leaseAppKey : "";
}

// ---- POS view --------------------------------------------------------

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
    const r = await api("/api/tabs?leaseAppKey=" + encodeURIComponent(leaseAppKey));
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
        { operationType: "Settle", class: "tab", reads: [open.tabKey, open.tabKey + ".status"], payload: { tabKey: open.tabKey } },
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

// ---- Front Desk view --------------------------------------------------

async function loadFrontDesk() {
  const grid = document.getElementById("frontdesk-grid");
  const summary = document.getElementById("frontdesk-summary");
  grid.innerHTML = "";
  summary.textContent = "";
  let tabs;
  try {
    const r = await api("/api/tabs");
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
    const br = await api("/api/frontdesk-bookings");
    (br.bookings || []).forEach((b) => { bookingsByLease[b.leaseAppKey] = b; });
  } catch (_) { /* front-desk not installed / unreachable — badges just don't show */ }

  // Same join, for the resident's applied-to unit rent/term — every open
  // tab's lease, not just those with a booked class (best-effort, same
  // degrade-to-hidden posture as bookingsByLease above).
  let leaseDetailsByLease = {};
  try {
    const ld = await api("/api/frontdesk-lease-details");
    (ld.leaseDetails || []).forEach((d) => { leaseDetailsByLease[d.leaseAppKey] = d; });
  } catch (_) { /* front-desk not installed / unreachable — lease details just don't show */ }

  // Same join, for the resident's own upcoming clinic visit (Inc 5) — existence
  // + time only, never the visit reason (front-desk's frontDeskVisits lens
  // never projects it). Best-effort, same degrade-to-hidden posture as above.
  let visitsByLease = {};
  try {
    const vs = await api("/api/frontdesk-visits");
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
  // Existence + time only — never a visit reason (front-desk's frontDeskVisits
  // lens never projects it; front desk staff see "a visit is scheduled," not
  // why or with whom).
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

// residentOwnLeaseAppKey caches the signed-in resident's own lease across a
// loadResident/renderResident pass — resolved once per load from the
// residents roster (leaseAppKeyForBooker), since the lease-picker select is
// hidden (not the value source) in self mode.
let residentOwnLeaseAppKey = "";

async function loadResident() {
  const select = document.getElementById("resident-lease");
  const label = document.getElementById("resident-lease-label");
  if (selfBookerKey) {
    label.hidden = true;
    select.hidden = true;
    const residents = await loadResidents();
    residentOwnLeaseAppKey = leaseAppKeyForBooker(selfBookerKey, residents);
  } else {
    label.hidden = false;
    select.hidden = false;
    const leases = await loadLeases();
    fillLeaseSelect(select, leases);
  }
  await renderResident();
}

async function renderResident() {
  const body = document.getElementById("resident-body");
  const selfMode = !!selfBookerKey;
  const leaseAppKey = selfMode ? residentOwnLeaseAppKey : document.getElementById("resident-lease").value;
  body.innerHTML = "";
  if (!leaseAppKey) {
    body.innerHTML = selfMode
      ? '<div class="empty">No lease found for your identity yet.</div>'
      : '<div class="empty">Pick a lease to view its house-tab history.</div>';
    return;
  }
  let ledger, tabs;
  try {
    [ledger, tabs] = await Promise.all([
      api("/api/ledger?leaseAppKey=" + encodeURIComponent(leaseAppKey)),
      api("/api/tabs?leaseAppKey=" + encodeURIComponent(leaseAppKey)),
    ]);
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
    (ledger.accountKey
      ? '<form id="payment-form" class="field-row" style="margin:10px 0 16px;">' +
        '<input id="payment-amount" type="number" step="0.01" min="0.01" placeholder="Payment amount ($)" required />' +
        '<button id="payment-submit" type="submit">Record Payment</button>' +
        "</form>"
      : "") +
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
              optionalReads: [leaseAppKey + ".cafeOpenTab", applicationForOptionalRead(leaseAppKey, selfBookerKey)],
              payload: { leaseAppKey },
            },
            "open the tab",
            { asSelf: true }
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
              optionalReads: [applicationForOptionalRead(leaseAppKey, selfBookerKey)],
              payload: { tabKey: open.tabKey },
            },
            "settle the tab",
            { asSelf: true }
          );
          toast("Tab settled — posting to the café ledger shortly.", true);
          setTimeout(renderResident, 700);
        } catch (e) {
          toast(e.message, false);
          settleBtn.disabled = false;
        }
      });
    }
  }
  const paymentForm = document.getElementById("payment-form");
  if (paymentForm) {
    paymentForm.addEventListener("submit", async (ev) => {
      ev.preventDefault();
      const input = document.getElementById("payment-amount");
      const cents = parseDollars(input.value);
      if (cents === null) { toast("Enter a payment amount greater than $0.", false); return; }
      const btn = document.getElementById("payment-submit");
      btn.disabled = true;
      try {
        await opOrThrow(
          {
            operationType: "CreditAccount", class: "cafetransaction",
            reads: [ledger.accountKey],
            payload: { accountKey: ledger.accountKey, amountCents: cents, memo: "House tab payment" },
          },
          "record the payment"
        );
        toast("Payment of " + money(cents) + " recorded.", true);
        setTimeout(renderResident, 700);
      } catch (e) {
        toast(e.message, false);
        btn.disabled = false;
      }
    });
  }
}

function escapeHtml(s) {
  const d = document.createElement("div");
  d.textContent = s;
  return d.innerHTML;
}

// ---- Me bar ---------------------------------------------------------

// refreshCurrentView re-renders whichever tab is active — called after
// signing in/out so the Resident view picks up the new self-service mode
// without a full page reload.
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
  fillResidentSelect(select, residents);
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
  document.getElementById("pos-lease").addEventListener("change", renderPos);
  document.getElementById("pos-refresh").addEventListener("click", () => { leasesCache = null; loadPos(); });
  document.getElementById("frontdesk-refresh").addEventListener("click", loadFrontDesk);
  document.getElementById("resident-lease").addEventListener("change", renderResident);
  document.getElementById("resident-refresh").addEventListener("click", () => { leasesCache = null; loadResident(); });
  initMeBar();
  loadPos();
}

init();
