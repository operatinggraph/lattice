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
// staff Bearer token — every wellness-domain op (CreateBooking/CancelBooking/
// CreateSession/…) is grantsTo:[operator] scope:any, so the fixed staff
// identity covers every write (no per-resident login exists in this thin FE).
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
  grid.innerHTML = sessions.map(scheduleCard).join("");
  sessions.forEach((se) => {
    const id = domId(se.sessionKey);
    const bookBtn = document.getElementById("book-" + id);
    const select = document.getElementById("booker-" + id);
    fillResidentSelect(select, residents, true);
    if (!bookBtn) return;
    bookBtn.addEventListener("click", async () => {
      const bookerKey = select.value;
      if (!bookerKey) { toast("Pick a resident to book.", false); return; }
      const opt = select.options[select.selectedIndex];
      const leaseAppKey = opt ? opt.dataset.leaseAppKey : "";
      bookBtn.disabled = true;
      try {
        const payload = { session: se.sessionKey, booker: bookerKey };
        if (leaseAppKey) payload.leaseAppKey = leaseAppKey;
        await opOrThrow(
          { operationType: "CreateBooking", class: "booking", reads: [se.sessionKey, se.sessionKey + ".schedule"], payload },
          "book the class"
        );
        toast("Booked.", true);
        await renderSchedule();
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
  return (
    '<div class="card">' +
    '<span class="badge ' + (full ? "settled" : "open") + '">' + se.bookedCount + " / " + se.capacity + " seats</span>" +
    '<div class="who">' + (se.name || "?") + "</div>" +
    '<div class="meta">' + (se.studioName || shortKey(se.studioKey)) + "</div>" +
    '<div class="meta">' + fmtRange(se.startsAt, se.endsAt) + "</div>" +
    '<div class="field-row">' +
    '<select id="booker-' + id + '"></select>' +
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
        await opOrThrow(
          { operationType: "CancelBooking", class: "booking", reads: [b.bookingKey, b.bookingKey + ".status"], payload: { bookingKey: b.bookingKey, session: sessionKey } },
          "cancel the booking"
        );
        toast("Booking cancelled.", true);
        await renderRoster();
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
  const residents = await loadResidents();
  fillResidentSelect(select, residents, true);
  await renderMyClasses();
}

async function renderMyClasses() {
  const body = document.getElementById("myclasses-body");
  const bookerKey = document.getElementById("myclasses-resident").value;
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
        await opOrThrow(
          { operationType: "CancelBooking", class: "booking", reads: [b.bookingKey, b.bookingKey + ".status"], payload: { bookingKey: b.bookingKey, session: b.sessionKey } },
          "cancel the booking"
        );
        toast("Booking cancelled.", true);
        await renderMyClasses();
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
  loadSchedule();
}

init();
