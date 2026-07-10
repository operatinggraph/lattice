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

// submitOp posts one operation to the Gateway, browser-direct, with the
// staff Bearer token — every café op (OpenTab/Charge/Settle) is
// grantsTo:[operator] scope:any, so the fixed staff identity covers every
// write (no per-resident login exists in this thin FE).
async function submitOp(body) {
  const [base, token] = await Promise.all([gatewayURL(), staffReadToken()]);
  return api(base + "/v1/operations", {
    method: "POST",
    headers: { "Content-Type": "application/json", Authorization: "Bearer " + token },
    body: JSON.stringify(body),
  });
}

async function opOrThrow(body, what) {
  const reply = await submitOp(body);
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

// parseDollars turns a user-entered dollar string ("4.50") into integer
// cents, or null when it isn't a positive amount.
function parseDollars(s) {
  const n = Number(s);
  if (!isFinite(n) || n <= 0) return null;
  return Math.round(n * 100);
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
          { operationType: "OpenTab", class: "tab", reads: [leaseAppKey], payload: { leaseAppKey } },
          "open the tab"
        );
        toast("Tab opened.", true);
        await renderPos();
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
      await renderPos();
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
      await renderPos();
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

  summary.textContent = tabs.length + " open tab" + (tabs.length === 1 ? "" : "s");
  if (!tabs.length) {
    grid.innerHTML = '<div class="empty">No open tabs.</div>';
    return;
  }
  grid.innerHTML = tabs.map((t) => frontDeskCard(t, bookingsByLease[t.leaseAppKey])).join("");
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
        await loadFrontDesk();
      } catch (e) {
        toast(e.message, false);
        btn.disabled = false;
      }
    });
  });
}

function frontDeskCard(t, booking) {
  const id = "settle-" + t.tabKey.replace(/[^a-zA-Z0-9]/g, "");
  const classBadge = booking
    ? '<div class="meta">🧘 Booked: ' + (booking.sessionName || "class") + " · " + (booking.startsAt || "?") + "</div>"
    : "";
  return (
    '<div class="card">' +
    '<span class="badge open">open</span>' +
    '<div class="who">' + shortKey(t.leaseAppKey) + "</div>" +
    '<div class="amount">' + money(t.totalCents) + "</div>" +
    '<div class="meta">Opened ' + (t.openedAt || "?") + "</div>" +
    classBadge +
    '<div class="card-actions"><button id="' + id + '" class="danger">Settle</button></div>' +
    "</div>"
  );
}

// ---- Resident view ------------------------------------------------

async function loadResident() {
  const select = document.getElementById("resident-lease");
  const leases = await loadLeases();
  fillLeaseSelect(select, leases);
  await renderResident();
}

async function renderResident() {
  const body = document.getElementById("resident-body");
  const leaseAppKey = document.getElementById("resident-lease").value;
  body.innerHTML = "";
  if (!leaseAppKey) {
    body.innerHTML = '<div class="empty">Pick a lease to view its house-tab history.</div>';
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
      '</p><p class="meta">Opened ' + (open.openedAt || "?") + " — not yet settled</p></div>"
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
        await renderResident();
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
  loadPos();
}

init();
