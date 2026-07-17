// Facet's entire UI is a reducer over one SSE stream (facet-app-ux.md §4):
// every manifest.* row arrives as a "manifest" frame, every write's
// lifecycle arrives as an "outbox" frame, and hydration-done arrives once
// as a "ready" frame. There is no other data path — a page refresh just
// reopens the stream and gets the same frames replayed as a snapshot.

// ---------------------------------------------------------------- helpers

function $(id) { return document.getElementById(id); }

function esc(s) {
  return String(s ?? "").replace(/[&<>"']/g, (c) => ({
    "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;",
  }[c]));
}

function lastSeg(key) { return (key || "").split(".").pop(); }

// maybeParseJSON handles a lens field that may arrive either as a native
// object (most aspects) or as a JSON-encoded string (op-meta's inputSchema
// aspect stores its `schema` sub-field as a string, not a nested object) —
// defensive for any other aspect that turns out to do the same.
function maybeParseJSON(v) {
  if (typeof v !== "string") return v;
  try { return JSON.parse(v); } catch (e) { return null; }
}

// prettify is the fallback label for any key the manifest didn't attach a
// human-readable name to (edge-manifest's own documented v1 scope-down:
// anchors/tasks carry only {key, ...}, no label — see packages/edge-manifest
// package doc comment).
function prettify(key) {
  const parts = (key || "").split(".");
  if (parts.length < 3) return key || "Unknown";
  const type = parts[1];
  return type.charAt(0).toUpperCase() + type.slice(1) + " " + parts[2].slice(0, 6);
}

function prettifyOpType(t) {
  return (t || "Operation").replace(/([a-z])([A-Z])/g, "$1 $2");
}

function titleCase(s) {
  return String(s).replace(/([a-z])([A-Z])/g, "$1 $2").replace(/^./, (c) => c.toUpperCase());
}

const ICONS = { laundry: "\u{1F9FA}", basket: "\u{1F9FA}", home: "\u{1F3E0}", wellness: "\u{1F9D8}", calendar: "\u{1F4C5}", doc: "\u{1F4C4}" };
function iconGlyph(name) { return ICONS[name] || "◆"; }

function toneClass(tone) { return tone === "destructive" ? "destructive" : (tone === "neutral" ? "neutral" : ""); }

function relativeTime(iso) {
  if (!iso) return "";
  const diffMs = new Date(iso).getTime() - Date.now();
  const abs = Math.abs(diffMs);
  const mins = Math.round(abs / 60000);
  if (mins < 60) return diffMs >= 0 ? `in ${mins}m` : `${mins}m ago`;
  const hrs = Math.round(mins / 60);
  if (hrs < 48) return diffMs >= 0 ? `in ${hrs}h` : `${hrs}h ago`;
  const days = Math.round(hrs / 24);
  return diffMs >= 0 ? `in ${days}d` : `${days}d ago`;
}

function isExpired(iso) { return !!iso && new Date(iso).getTime() < Date.now(); }

function byCreatedDesc(a, b) { return new Date(b.data.createdAt || 0) - new Date(a.data.createdAt || 0); }

let toastTimer = null;
function toast(msg, ok) {
  const el = $("toast");
  el.textContent = msg;
  el.className = "toast " + (ok ? "ok" : "err");
  el.hidden = false;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => { el.hidden = true; }, 4000);
}

// ------------------------------------------------------------------ state

const state = {
  rows: new Map(),   // manifest key -> {data, pending}
  outbox: new Map(), // requestId -> outbox entry (server shape, feed.go)
  view: "home",
  credentials: null, // null = not yet loaded; array once GET /api/credentials resolves
};

function me() { const r = state.rows.get("manifest.me"); return r && r.data; }

function rowsByNs(ns) {
  const out = [];
  for (const [k, v] of state.rows) {
    if (k === ns || k.startsWith(ns + ".")) out.push({ key: k, data: v.data, pending: v.pending });
  }
  return out;
}
function services() { return rowsByNs("manifest.svc"); }
function ops() { return rowsByNs("manifest.op"); }
function tasks() { return rowsByNs("manifest.task").filter((t) => !isExpired(t.data.expiresAt)); }
function instances() { return rowsByNs("manifest.inst"); }

function opByFullKey(fullKey) {
  if (!fullKey) return null;
  const key = "manifest.op." + lastSeg(fullKey);
  const row = state.rows.get(key);
  return row ? { key, data: row.data, pending: row.pending } : null;
}

// -------------------------------------------------------------- SSE feed

let hasBootstrapped = false;
let sseSilenceTimer = null;
let activeFeed = null; // the live EventSource — closed on sign-out (§4.4 purge)

function connectFeed() {
  const es = new EventSource("/api/feed");
  activeFeed = es;
  es.addEventListener("manifest", (e) => {
    const fr = JSON.parse(e.data);
    if (fr.deleted) state.rows.delete(fr.key);
    else state.rows.set(fr.key, { data: fr.data, pending: !!fr.pending });
    armSilenceFallback();
    scheduleRender();
  });
  es.addEventListener("outbox", (e) => {
    const fr = JSON.parse(e.data);
    applyOutboxFrame(fr.outbox);
  });
  es.addEventListener("ready", () => finishBoot());
  es.addEventListener("revoked", (e) => {
    let reason = "";
    try { reason = (JSON.parse(e.data) || {}).reason || ""; } catch (err) { /* keep the default copy */ }
    showRevocationBanner(reason);
    finishBoot(); // never strand a revoked session on the boot spinner
  });
  es.onopen = () => {
    if (!hasBootstrapped) {
      setBootLabel("Loading your world…", true);
      armSilenceFallback();
    } else {
      hideReconnectBanner();
    }
  };
  es.onerror = () => {
    if (hasBootstrapped) showReconnectBanner();
  };
}

function armSilenceFallback() {
  clearTimeout(sseSilenceTimer);
  sseSilenceTimer = setTimeout(finishBoot, 3000);
}

function finishBoot() {
  if (hasBootstrapped) return;
  hasBootstrapped = true;
  clearTimeout(sseSilenceTimer);
  $("boot").hidden = true;
  $("app").hidden = false;
  renderView(state.view);
}

function setBootLabel(text, showProgress) {
  $("boot-label").textContent = text;
  $("boot-progress").hidden = !showProgress;
}

function showReconnectBanner() { $("reconnect-banner").hidden = false; }
function hideReconnectBanner() { $("reconnect-banner").hidden = true; }

// ---------------------------------------------------------- sign-out (§4.4)

// signOut implements the design's "on confirmed revocation/sign-out the
// local mirror is purged" — clears every in-memory row/outbox entry and
// closes the live SSE connection before the cookie is cleared, so no stray
// frame repopulates state after logout starts, then hands off to /login.
function signOut() {
  if (activeFeed) { activeFeed.close(); activeFeed = null; }
  state.rows.clear();
  state.outbox.clear();
  state.credentials = null;
  fetch("/api/logout", { method: "POST" })
    .catch(() => {})
    .finally(() => { location.replace("/login"); });
}

function applyOutboxFrame(e) {
  if (!e) return;
  state.outbox.set(e.requestId, e);
  if (e.state === "confirmed") {
    setTimeout(() => {
      const cur = state.outbox.get(e.requestId);
      if (cur && cur.state === "confirmed") { state.outbox.delete(e.requestId); scheduleRender(); }
    }, 2000);
  }
  scheduleRender();
}

function showRevocationBanner(reason) {
  const el = $("revocation-banner");
  if (reason) $("revocation-reason").textContent = reason;
  el.hidden = false;
}

let renderScheduled = false;
function scheduleRender() {
  if (renderScheduled) return;
  renderScheduled = true;
  queueMicrotask(() => {
    renderScheduled = false;
    if (hasBootstrapped) renderView(state.view);
  });
}

// ------------------------------------------------------------------- nav

function setView(view) {
  state.view = view;
  document.querySelectorAll("main.screen > section").forEach((el) => { el.hidden = el.dataset.view !== view; });
  document.querySelectorAll(".bottom-nav button").forEach((b) => b.classList.toggle("active", b.dataset.view === view));
  renderView(view);
}

function renderView(view) {
  updateBadges();
  if (view === "home") renderHome();
  else if (view === "services") renderServices();
  else if (view === "tasks") renderTasks();
  else if (view === "activity") renderActivity();
  else if (view === "me") renderMe();
}

function updateBadges() {
  const nonTerminal = [...state.outbox.values()].filter((e) => e.state !== "confirmed").length;
  const ob = $("outbox-count");
  if (nonTerminal > 0) { ob.textContent = String(nonTerminal); ob.hidden = false; } else ob.hidden = true;

  const openTasks = tasks().length;
  const tb = $("tasks-badge");
  if (openTasks > 0) { tb.textContent = String(openTasks); tb.hidden = false; } else tb.hidden = true;

  // The manifest's displayName is the good label, but it is absent for an
  // identity with no name aspect yet (a freshly provisioned, not-yet-claimed
  // one — exactly the case the Me screen's claim card serves), and the whole
  // manifest is absent until hydration lands. whoami's identityId is always
  // available the moment the session resolves, so it backstops the header:
  // the signed-in user can always tell WHO they are signed in as (design
  // §7.2's whoami-driven header).
  const nm = me();
  $("identity-name").textContent = (nm && nm.displayName) || shortIdentityLabel();
}

// whoami is fetched once at boot; the header reads it until (and unless) a
// manifest.me row with a real displayName arrives.
let whoamiIdentityID = "";

function shortIdentityLabel() {
  return whoamiIdentityID ? whoamiIdentityID.slice(0, 8) + "…" : "";
}

function loadWhoami() {
  return fetch("/api/whoami")
    .then((r) => r.json())
    .then((body) => {
      if (body && body.loggedIn) whoamiIdentityID = body.identityId || "";
      // The boot-env fallback identity is authenticated by no cookie, so
      // there is nothing for a sign-out to end — offering it would clear a
      // cookie nobody used and bounce the browser straight back in.
      $("sign-out-btn").hidden = !(body && body.canSignOut);
      scheduleRender();
    })
    .catch(() => {});
}

// ----------------------------------------------------------------- Home

function renderHome() {
  const m = me() || {};
  const anchors = (m.anchors || []).filter((a) => a.key);
  const svcs = services();
  const tsks = tasks();
  $("view-home").innerHTML = `
    <section>
      <h2 class="section-title">My places</h2>
      ${anchors.length ? `<div class="chip-row">${anchors.map((a) => `<span class="chip">${esc(prettify(a.key))}</span>`).join("")}</div>` : `<div class="empty">No residence linked yet.</div>`}
    </section>
    <section>
      <h2 class="section-title">Services ${svcs.length > 4 ? `<a class="see-all" data-goto="services">See all &rarr;</a>` : ""}</h2>
      ${svcs.length ? `<div class="strip">${svcs.slice(0, 4).map(serviceCard).join("")}</div>` : `<div class="empty">No services available yet.</div>`}
    </section>
    <section>
      <h2 class="section-title">Tasks ${tsks.length > 3 ? `<a class="see-all" data-goto="tasks">See all &rarr;</a>` : ""}</h2>
      ${tsks.length ? tsks.slice(0, 3).map(taskRow).join("") : `<div class="empty">Nothing needs your attention.</div>`}
    </section>
  `;
}

function serviceCard(s) {
  const d = s.data;
  return `<div class="card" data-goto="service" data-key="${esc(s.key)}">
    <div class="icon">${iconGlyph(d.icon)}</div>
    <div class="title">${esc(d.name || "Service")}</div>
    <div class="subtitle">${esc(d.description || "")}</div>
  </div>`;
}

function taskRow(t) {
  const d = t.data;
  const opRow = opByFullKey(d.forOperationKey);
  const title = (opRow && opRow.data.title) || prettifyOpType(d.operationType);
  return `<div class="card" data-goto="task" data-key="${esc(t.key)}" style="flex-direction:row;align-items:center;gap:12px">
    <div style="flex:1">
      <div class="title">${esc(title)}</div>
      <div class="subtitle">${esc(prettify(d.scopedTo))} &middot; due ${esc(relativeTime(d.expiresAt))}</div>
    </div>
  </div>`;
}

// ------------------------------------------------------------- Services

function renderServices() {
  const svcs = services();
  if (!svcs.length) { $("view-services").innerHTML = `<div class="empty">No services available yet.</div>`; return; }
  const byCat = new Map();
  for (const s of svcs) {
    const cat = s.data.category || "other";
    if (!byCat.has(cat)) byCat.set(cat, []);
    byCat.get(cat).push(s);
  }
  let html = "";
  for (const [cat, list] of byCat) {
    html += `<h3 class="category-heading">${esc(cat)}</h3><div class="grid">${list.map(serviceCard).join("")}</div>`;
  }
  $("view-services").innerHTML = html;
}

function openServiceDetail(key) {
  const row = state.rows.get(key);
  if (!row) return;
  const d = row.data;
  const myOps = ops().filter((o) => (o.data.viaServices || []).includes(d.serviceKey));
  const myInstances = instances().filter((i) => i.data.templateKey === d.serviceKey).sort(byCreatedDesc);
  showModal(`
    <button class="close-x" data-close>&times;</button>
    <div class="icon" style="font-size:32px">${iconGlyph(d.icon)}</div>
    <h2>${esc(d.name || "Service")}</h2>
    <p class="lead">${esc(d.description || "")}${d.resolvedVia ? ` &middot; via ${esc(prettify(d.resolvedVia))}` : ""}</p>
    <h3 class="category-heading">Operations</h3>
    ${myOps.length ? myOps.map((o) => opButton(o, { serviceKey: d.serviceKey })).join("") : `<div class="empty">Nothing to do here yet.</div>`}
    <h3 class="category-heading">My instances of this service</h3>
    ${myInstances.length ? myInstances.map(instanceRow).join("") : `<div class="empty">No orders yet.</div>`}
  `);
}

function opButton(o, ctx) {
  const d = o.data;
  if (!d.dispatchClass) {
    return `<div class="degraded-card">${esc(prettifyOpType(d.operationType))} — This isn't completable here yet — ask staff to help via the admin console.</div>`;
  }
  const label = d.submitLabel || d.title || prettifyOpType(d.operationType);
  const attrs = [`data-open-op="${esc(o.key)}"`];
  if (ctx.serviceKey) attrs.push(`data-service-key="${esc(ctx.serviceKey)}"`);
  if (ctx.taskKey) attrs.push(`data-task-key="${esc(ctx.taskKey)}"`);
  if (ctx.scopedTo) attrs.push(`data-scoped-to="${esc(ctx.scopedTo)}"`);
  return `<button class="primary-btn ${toneClass(d.tone)}" style="margin-bottom:8px" ${attrs.join(" ")}>${esc(label)}</button>`;
}

function instanceRow(i) {
  const d = i.data;
  const status = d.status || "open";
  return `<div class="timeline-item">
    <div class="row1"><span class="title">${iconGlyph(d.templateIcon)} ${esc(d.templateName || "Service")}</span><span class="badge ${status === "open" ? "queued" : "confirmed"}">${esc(status)}</span></div>
    <div class="meta">${esc(relativeTime(d.completedAt))}</div>
  </div>`;
}

// ---------------------------------------------------------------- Tasks

function renderTasks() {
  const tsks = tasks().sort((a, b) => new Date(a.data.expiresAt || 0) - new Date(b.data.expiresAt || 0));
  $("view-tasks").innerHTML = tsks.length ? tsks.map(taskRow).join("") : `<div class="empty">Nothing needs your attention.</div>`;
}

function openTaskDetail(key) {
  const row = state.rows.get(key);
  if (!row) return;
  const d = row.data;
  const opRow = opByFullKey(d.forOperationKey);
  if (!opRow) {
    showModal(`<button class="close-x" data-close>&times;</button><div class="degraded-card">This task's operation isn't described yet — ask staff for help via the admin console.</div>`);
    return;
  }
  openDescriptorForm(opRow.key, { taskKey: d.taskKey, scopedTo: d.scopedTo });
}

// -------------------------------------------------------------- Activity

function renderActivity() {
  const pinned = [...state.outbox.values()]
    .filter((e) => e.state === "queued" || e.state === "submitting" || e.state === "rejected")
    .sort((a, b) => new Date(b.createdAt) - new Date(a.createdAt));
  const insts = instances().sort(byCreatedDesc);
  if (!pinned.length && !insts.length) {
    $("view-activity").innerHTML = `<div class="empty">Nothing yet.</div>`;
    return;
  }
  $("view-activity").innerHTML = pinned.map(outboxCard).join("") + insts.map(instanceRow).join("");
}

function titleForOutbox(e) {
  const match = ops().find((o) => o.data.operationType === e.operationType);
  return (match && match.data.title) || prettifyOpType(e.operationType);
}

function outboxCard(e) {
  const stateLabel = { queued: "Queued", submitting: "Sending…", confirmed: "Done", rejected: "Failed" }[e.state] || e.state;
  const errMsg = e.state === "rejected" && e.errorMessage ? `<div class="error-msg">${esc(e.errorCode || "")}: ${esc(e.errorMessage)}</div>` : "";
  const actions = e.state === "rejected"
    ? `<div class="actions"><button class="ghost-btn" data-dismiss="${esc(e.requestId)}">Dismiss</button><button class="ghost-btn" data-review="${esc(e.requestId)}">Review</button></div>`
    : "";
  return `<div class="timeline-item">
    <div class="row1"><span class="title">${esc(titleForOutbox(e))}</span><span class="badge ${esc(e.state)}">${esc(stateLabel)}</span></div>
    ${errMsg}
    ${actions}
  </div>`;
}

function dismissOutbox(reqId) { state.outbox.delete(reqId); scheduleRender(); }

function retryOutbox(reqId) {
  const e = state.outbox.get(reqId);
  if (!e) return;
  fetch("/api/enqueue", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ operationType: e.operationType, payload: e.payload, reads: e.reads, optionalReads: e.optionalReads, authContext: e.authContext }),
  }).then(() => { state.outbox.delete(reqId); scheduleRender(); }).catch((err) => toast(String(err), false));
}

function reviewOutbox(reqId) {
  const e = state.outbox.get(reqId);
  if (!e) return;
  const match = ops().find((o) => o.data.operationType === e.operationType);
  if (!match) { toast("Can't reopen this form — its operation is no longer described.", false); return; }
  const ctx = {
    serviceKey: e.authContext && e.authContext.service,
    taskKey: e.authContext && e.authContext.task,
    scopedTo: e.authContext && e.authContext.target,
  };
  renderDescriptorForm(match.data, match.key, ctx, e.payload || {}, "This changed since you started — review and resubmit if it still applies");
}

// -------------------------------------------------------------------- Me

function renderMe() {
  // Guard on the manifest ROW's presence, not on a falsy `claimed`: until
  // manifest.me hydrates (and it never does for a session whose sync is
  // dead), `me()` is undefined, so `claimed` reads falsy and a perfectly
  // claimed user would be shown "Claim your identity" — an affordance that
  // then fails with "Not signed in.", in exactly the state it's most likely
  // to appear.
  const m = me();
  if (!m) {
    $("view-me").innerHTML = `<div class="subtitle">Loading your identity…</div>`;
    return;
  }
  const roles = (m.roles || []).filter((r) => r.key);
  const anchors = (m.anchors || []).filter((a) => a.key);
  $("view-me").innerHTML = `
    <div class="card" style="cursor:default">
      <div class="title" style="font-size:18px">${esc(m.displayName || "Unnamed")}</div>
      <div class="subtitle">${m.claimed ? "Claimed identity" : "Not yet claimed"}</div>
    </div>
    <h3 class="category-heading">Roles</h3>
    <div class="chip-row">${roles.length ? roles.map((r) => `<span class="chip">${esc(r.name || prettify(r.key))}</span>`).join("") : '<span class="subtitle">None</span>'}</div>
    <h3 class="category-heading">Places</h3>
    <div class="chip-row">${anchors.length ? anchors.map((a) => `<span class="chip">${esc(prettify(a.key))}</span>`).join("") : '<span class="subtitle">None</span>'}</div>
    ${m.claimed ? renderCredentialsSection() : renderClaimCard()}
  `;
  if (m.claimed && state.credentials === null && !credentialsLoading && !credentialsError) loadCredentials();
}

// renderClaimCard is the Me screen's claim/link entry for a signed-in but
// not-yet-claimed identity (design §4.1's claim beat, reached here instead
// of first-run since Inc 2's dev-login session already got the browser IN —
// see edge-showcase-app-design.md §7.2's Inc 3 note). Submits the same
// POST /api/claim the /login page's "new here?" branch uses, targeting the
// signed-in identity's own key.
function renderClaimCard() {
  return `<div class="card" style="cursor:default;margin-top:18px">
    <div class="title">Claim your identity</div>
    <div class="subtitle" style="margin-bottom:10px">Enter the claim key you were given to activate this identity.</div>
    <div class="field" style="margin-bottom:8px">
      <input type="text" id="claim-key-input" placeholder="Claim key" autocomplete="off">
    </div>
    <button class="primary-btn" data-claim-submit>Claim</button>
  </div>`;
}

function submitSelfClaim() {
  const input = $("claim-key-input");
  const key = input && input.value.trim();
  if (!key) { toast("Enter your claim key first.", false); return; }
  const m = me();
  if (!m || !m.identityKey) { toast("Not signed in.", false); return; }
  fetch("/api/claim", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ targetIdentityKey: m.identityKey, claimKey: key }),
  })
    .then((r) => r.json().then((body) => ({ ok: r.ok, body })))
    .then(({ ok, body }) => {
      if (!ok) { toast((body && body.error) || "Claim failed", false); return; }
      toast("Claimed — your world is loading…", true);
    })
    .catch((err) => toast(String(err), false));
}

// ---- Manage sign-in methods (multi-credential-identity-linking-design.md
// §3/§8, edge-showcase-app-design.md §7.2 Inc 3) ----
//
// Lists the credentials bound to the signed-in identity (GET /api/credentials,
// the identityCredentialsRead Protected Postgres lens — mirrors
// cmd/loftspace-app's account-settings page, credentials.go), links a new one
// and removes one. Unlike loftspace's browser-direct dance, Facet's browser
// never talks to the Gateway itself, so link/unlink are each ONE backend
// call (credentials.go runs the Initiate/CompleteCredentialLink pair
// server-side, mirroring /api/claim's own mint-a-throwaway-device shape).

let credentialsLoading = false;

// credentialsError holds a failed read's message. It exists so a broken read
// model can never render as the affirmative claim "no sign-in methods" — an
// unconfigured FACET_PG_DSN, an unreachable Postgres, or an unprojected lens
// must look like a failure, not like an identity with zero ways to sign in
// (which would invite linking a credential to "fix" a list that isn't
// actually empty).
let credentialsError = "";

function loadCredentials() {
  if (credentialsLoading) return Promise.resolve();
  credentialsLoading = true;
  return fetch("/api/credentials")
    .then((r) => r.json().then((body) => ({ ok: r.ok, body })))
    .then(({ ok, body }) => {
      if (!ok) {
        credentialsError = (body && body.error) || "Could not load your sign-in methods.";
        return;
      }
      credentialsError = "";
      state.credentials = body.credentials || [];
    })
    .catch((err) => { credentialsError = String(err); })
    .finally(() => { credentialsLoading = false; scheduleRender(); });
}

// refreshCredentialsUntilChanged re-reads until the list actually moves.
// /api/credentials serves the identityCredentialsRead lens, fed by ASYNC CDC
// projection, so a read fired the instant link/unlink returns still sees the
// pre-mutation set — the write is accepted but not yet projected. Polling to
// a bounded ceiling keeps the "Linked."/"Removed." toast honest instead of
// leaving the card the user just deleted sitting on screen until a reload.
function refreshCredentialsUntilChanged(prevCount) {
  let attempts = 0;
  const poll = () => {
    loadCredentials().then(() => {
      attempts++;
      const changed = credentialsError || (state.credentials || []).length !== prevCount;
      if (!changed && attempts < 8) setTimeout(poll, 300);
    });
  };
  poll();
}

function renderCredentialsSection() {
  const creds = state.credentials;
  if (credentialsError) {
    return `<h3 class="category-heading">Sign-in methods</h3>
      <div class="degraded-card">${esc(credentialsError)}</div>
      <button class="ghost-btn" style="margin-top:8px" data-reload-credentials>Try again</button>`;
  }
  if (creds === null) {
    return `<h3 class="category-heading">Sign-in methods</h3><div class="subtitle">Loading…</div>`;
  }
  return `
    <h3 class="category-heading">Sign-in methods</h3>
    ${creds.length ? creds.map((c) => renderCredentialCard(c, creds.length)).join("") : `<div class="empty">No sign-in methods found.</div>`}
    <button class="ghost-btn" style="margin-top:8px" data-link-credential ${linkInFlight ? "disabled" : ""}>${linkInFlight ? "Linking…" : "+ Link a new sign-in method"}</button>
  `;
}

function renderCredentialCard(c, totalCount) {
  const short = lastSeg(c.actorKey || "").slice(0, 8);
  const disabled = totalCount <= 1;
  return `<div class="card" style="cursor:default">
    <div class="title">Sign-in method ${esc(short)}&hellip;</div>
    ${c.boundAt ? `<div class="subtitle">Linked ${esc(new Date(c.boundAt).toLocaleString())}</div>` : ""}
    <div class="card-actions">
      <button class="ghost-btn" data-unlink-credential="${esc(c.actorKey)}" ${disabled ? "disabled" : ""} title="${disabled ? "Cannot remove your last remaining sign-in method" : ""}">Remove</button>
    </div>
  </div>`;
}

// linkInFlight serializes the link ceremony per browser. Two overlapping
// links against the same identity corrupt each other: InitiateCredentialLink
// is create-or-overwrite on U.linkKey (the DDL is explicit that re-initiating
// overwrites), so B's arm replaces A's, and A's Complete then fails the hash
// check with the deliberately generic ClaimKeyInvalid — a rejection whose
// anti-enumeration wording gives the user no hint that their own double-click
// caused it.
let linkInFlight = false;

function linkCredential() {
  if (linkInFlight) return;
  linkInFlight = true;
  const prevCount = (state.credentials || []).length;
  scheduleRender();
  fetch("/api/credentials/link", { method: "POST" })
    .then((r) => r.json().then((body) => ({ ok: r.ok, body })))
    .then(({ ok, body }) => {
      if (!ok) { toast((body && body.error) || "Could not link a new sign-in method", false); return; }
      toast("New sign-in method linked.", true);
      refreshCredentialsUntilChanged(prevCount);
    })
    .catch((err) => toast(String(err), false))
    .finally(() => { linkInFlight = false; scheduleRender(); });
}

function unlinkCredentialAction(actorKey) {
  if (!confirm("Remove this sign-in method? It will no longer be able to sign in to this identity.")) return;
  const prevCount = (state.credentials || []).length;
  fetch("/api/credentials/unlink", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ credentialActorKey: actorKey }),
  })
    .then((r) => r.json().then((body) => ({ ok: r.ok, body })))
    .then(({ ok, body }) => {
      if (!ok) { toast((body && body.error) || "Could not remove sign-in method", false); return; }
      toast("Sign-in method removed.", true);
      refreshCredentialsUntilChanged(prevCount);
    })
    .catch((err) => toast(String(err), false));
}

// ------------------------------------------------------------------ modal

function showModal(innerHtml) {
  $("modal-root").innerHTML = `<div class="modal-overlay" data-close-overlay><div class="modal">${innerHtml}</div></div>`;
}
function hideModal() { $("modal-root").innerHTML = ""; }

function openDescriptorForm(opKey, ctx) {
  const row = state.rows.get(opKey);
  if (!row) return;
  renderDescriptorForm(row.data, opKey, ctx, {});
}

// --------------------------------------------- the descriptor form (§3.6)

function renderDescriptorForm(op, opKey, ctx, prefill, reviewBanner) {
  const schema = maybeParseJSON(op.inputSchema) || { type: "object", properties: {} };
  const props = schema.properties || {};
  const required = new Set(schema.required || []);
  const fieldDescs = maybeParseJSON(op.fieldDescriptions) || {};
  const contextParams = maybeParseJSON(op.dispatchContextParams) || {};
  const targetField = op.dispatchTargetField;
  const fieldNames = Object.keys(props).filter((f) => !(f in contextParams) && f !== targetField);

  const fieldsHtml = fieldNames.map((name) => renderField(name, props[name], fieldDescs[name], required.has(name), prefill[name], op.sensitive)).join("");

  showModal(`
    <button class="close-x" data-close>&times;</button>
    <h2>${esc(op.title || prettifyOpType(op.operationType))}</h2>
    ${op.description ? `<p class="lead">${esc(op.description)}</p>` : ""}
    ${reviewBanner ? `<div class="banner">${esc(reviewBanner)}</div>` : ""}
    <form id="descriptor-form">
      ${fieldsHtml}
      <button type="submit" class="primary-btn ${toneClass(op.tone)}">${esc(op.submitLabel || "Submit")}</button>
    </form>
  `);

  const form = $("descriptor-form");
  form.addEventListener("submit", (e) => {
    e.preventDefault();
    submitDescriptorForm(form, op, opKey, ctx, fieldNames, props, contextParams);
  });
}

function renderField(name, schema, help, isRequired, prefillVal, opSensitive) {
  schema = schema || {};
  const label = schema.title || name;
  const helpHtml = help ? `<div class="help">${esc(help)}</div>` : "";
  const reqAttr = isRequired ? "required" : "";
  const val = prefillVal !== undefined && prefillVal !== null ? prefillVal : "";
  let inputHtml;

  if (schema.type === "boolean") {
    inputHtml = `<div class="toggle-switch ${val === true ? "on" : ""}" data-toggle-field="${esc(name)}"><div class="knob"></div></div>`;
  } else if (schema.enum) {
    const opts = schema.enum;
    if (opts.length <= 4) {
      inputHtml = `<div class="segmented" data-segmented-field="${esc(name)}">${opts.map((o) => `<button type="button" data-value="${esc(o)}" class="${o === val ? "selected" : ""}">${esc((schema.enumLabels && schema.enumLabels[o]) || titleCase(o))}</button>`).join("")}</div>
        <input type="hidden" name="${esc(name)}" value="${esc(val)}" ${reqAttr}>`;
    } else {
      inputHtml = `<select name="${esc(name)}" ${reqAttr}><option value="">Choose…</option>${opts.map((o) => `<option value="${esc(o)}" ${o === val ? "selected" : ""}>${esc((schema.enumLabels && schema.enumLabels[o]) || titleCase(o))}</option>`).join("")}</select>`;
    }
  } else if ((schema.type === "integer" || schema.type === "number") && (schema["x-format"] === "money" || /Cents$/.test(name))) {
    const dollars = val ? (val / 100).toFixed(2) : "";
    inputHtml = `<input type="number" step="0.01" name="${esc(name)}" data-money-field value="${esc(dollars)}" ${reqAttr}>`;
  } else if (schema.type === "integer" || schema.type === "number") {
    const attrs = [];
    if (schema.minimum !== undefined) attrs.push(`min="${schema.minimum}"`);
    if (schema.maximum !== undefined) attrs.push(`max="${schema.maximum}"`);
    if (schema.type === "integer") attrs.push('step="1"');
    inputHtml = `<input type="number" name="${esc(name)}" value="${esc(val)}" ${attrs.join(" ")} ${reqAttr}>`;
  } else if (schema.type === "string" && schema.format === "date") {
    inputHtml = `<input type="date" name="${esc(name)}" value="${esc(val)}" ${reqAttr}>`;
  } else if (schema.type === "string" && schema.format === "date-time") {
    inputHtml = `<input type="datetime-local" name="${esc(name)}" value="${esc(val)}" ${reqAttr}>`;
  } else if (schema.type === "string" && schema["x-entityRef"]) {
    inputHtml = `<input type="text" data-entity-ref-visible="${esc(name)}" placeholder="Search…" autocomplete="off">
      <input type="hidden" name="${esc(name)}" value="${esc(val)}">
      <div class="entity-ref-results" data-entity-ref-results="${esc(name)}"></div>`;
  } else if (schema.type === "string" && (schema.maxLength || 0) > 120) {
    inputHtml = `<textarea name="${esc(name)}" ${schema.maxLength ? `maxlength="${schema.maxLength}"` : ""} ${reqAttr}>${esc(val)}</textarea>`;
  } else if (opSensitive) {
    inputHtml = `<input type="password" name="${esc(name)}" autocomplete="off" ${reqAttr}>`;
  } else {
    inputHtml = `<input type="text" name="${esc(name)}" value="${esc(val)}" ${schema.maxLength ? `maxlength="${schema.maxLength}"` : ""} ${reqAttr}>`;
  }

  return `<div class="field">
    <label>${esc(label)}${isRequired ? " *" : ""}</label>
    ${inputHtml}
    ${helpHtml}
  </div>`;
}

function substituteTemplate(str, ctx, payload) {
  if (typeof str !== "string") return str;
  return str.replace(/\{([^}]+)\}/g, (m, expr) => {
    if (expr === "actor") return (me() && me().identityKey) || "";
    if (expr === "service") return ctx.serviceKey || "";
    if (expr === "scopedTo") return ctx.scopedTo || "";
    if (expr.startsWith("payload.")) { const v = payload[expr.slice(8)]; return v === undefined ? "" : v; }
    return m;
  });
}

function buildAuthContext(kind, ctx) {
  const m = me();
  if (kind === "self") return { target: m && m.identityKey };
  if (kind === "service") return { service: ctx.serviceKey };
  if (kind === "task") return { task: ctx.taskKey, target: ctx.scopedTo };
  return undefined;
}

// targetFieldValue mirrors buildAuthContext's per-kind mapping but returns
// the single scalar a `dispatch.targetField` payload key should hold — the
// simpler sibling of contextParams template substitution for an op whose
// dispatch aspect names one field directly instead of a template string
// (e.g. RequestService's dispatch: {authContext:"service", targetField:"service"}
// with no contextParams at all).
function targetFieldValue(kind, ctx) {
  const m = me();
  if (kind === "self") return m && m.identityKey;
  if (kind === "service") return ctx.serviceKey;
  if (kind === "task") return ctx.scopedTo;
  return undefined;
}

function submitDescriptorForm(form, op, opKey, ctx, fieldNames, props, contextParams) {
  const payload = {};
  for (const name of fieldNames) {
    const schema = props[name] || {};
    if (schema.type === "boolean") {
      const el = form.querySelector('[data-toggle-field="' + name + '"]');
      payload[name] = el ? el.classList.contains("on") : false;
      continue;
    }
    const el = form.elements.namedItem(name);
    if (!el || el.value === "") continue;
    let v = el.value;
    if (el.dataset && el.dataset.moneyField !== undefined) v = Math.round(parseFloat(el.value) * 100);
    else if (schema.type === "integer") v = parseInt(v, 10);
    else if (schema.type === "number") v = parseFloat(v);
    payload[name] = v;
  }
  for (const [field, template] of Object.entries(contextParams)) {
    payload[field] = substituteTemplate(template, ctx, payload);
  }
  if (op.dispatchTargetField) {
    payload[op.dispatchTargetField] = targetFieldValue(op.dispatchAuthContext, ctx);
  }

  const reads = (op.dispatchReads || []).map((t) => substituteTemplate(t, ctx, payload));
  // A targetField value is a vertex key the script almost certainly needs
  // to read (it's what authContext resolves against) — include it even
  // when dispatch.reads doesn't name it explicitly, since an op meta that
  // sets targetField but not reads is a plausible under-annotation, not a
  // reason to fail a request the descriptor otherwise fully describes.
  if (op.dispatchTargetField) {
    const v = payload[op.dispatchTargetField];
    if (v && !reads.includes(v)) reads.push(v);
  }
  // The submitting identity's own key is read-free per Contract #2's model
  // (op.actor is always available to the script) in principle, but a
  // script that additionally validates the actor vertex itself (as
  // RequestService's real installed script does — "UnknownApplicant"
  // otherwise) needs it declared. Over-reading is harmless; under-reading
  // fails the request, so include it unconditionally.
  const selfKey = me() && me().identityKey;
  if (selfKey && !reads.includes(selfKey)) reads.push(selfKey);
  const authContext = buildAuthContext(op.dispatchAuthContext, ctx);

  fetch("/api/enqueue", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ operationType: op.operationType, class: op.dispatchClass || "", payload, reads, authContext }),
  })
    .then((r) => r.json())
    .then((body) => {
      if (body.error) { toast(body.error, false); return; }
      hideModal();
      toast("Submitted", true);
    })
    .catch((err) => toast(String(err), false));
}

// ------------------------------------------------------------ delegation

function selectSegmented(btn) {
  const wrap = btn.closest("[data-segmented-field]");
  wrap.querySelectorAll("button").forEach((b) => b.classList.remove("selected"));
  btn.classList.add("selected");
  const hidden = wrap.parentElement.querySelector('input[type="hidden"]');
  if (hidden) hidden.value = btn.dataset.value;
}

function onGlobalClick(e) {
  const signOutBtn = e.target.closest("#sign-out-btn, #revocation-signout");
  if (signOutBtn) { signOut(); return; }

  const navBtn = e.target.closest(".bottom-nav button, #outbox-btn");
  if (navBtn) { setView(navBtn.dataset.view); return; }

  const seg = e.target.closest("[data-segmented-field] button");
  if (seg) { selectSegmented(seg); return; }

  const toggle = e.target.closest("[data-toggle-field]");
  if (toggle) { toggle.classList.toggle("on"); return; }

  const pick = e.target.closest("[data-entity-ref-pick]");
  if (pick) {
    const name = pick.dataset.entityRefPick;
    const form = pick.closest("form");
    form.querySelector('input[type="hidden"][name="' + name + '"]').value = pick.dataset.entityRefValue;
    form.querySelector('[data-entity-ref-visible="' + name + '"]').value = pick.textContent;
    document.querySelector('[data-entity-ref-results="' + name + '"]').innerHTML = "";
    return;
  }

  const claimSubmit = e.target.closest("[data-claim-submit]");
  if (claimSubmit) { submitSelfClaim(); return; }

  const linkCred = e.target.closest("[data-link-credential]");
  if (linkCred) { linkCredential(); return; }

  const reloadCreds = e.target.closest("[data-reload-credentials]");
  if (reloadCreds) { credentialsError = ""; loadCredentials(); return; }

  const unlinkCred = e.target.closest("[data-unlink-credential]");
  if (unlinkCred) { unlinkCredentialAction(unlinkCred.dataset.unlinkCredential); return; }

  const openOp = e.target.closest("[data-open-op]");
  if (openOp) {
    openDescriptorForm(openOp.dataset.openOp, {
      serviceKey: openOp.dataset.serviceKey || null,
      taskKey: openOp.dataset.taskKey || null,
      scopedTo: openOp.dataset.scopedTo || null,
    });
    return;
  }

  const goto = e.target.closest("[data-goto]");
  if (goto) {
    const view = goto.dataset.goto;
    if (view === "service") { openServiceDetail(goto.dataset.key); return; }
    if (view === "task") { openTaskDetail(goto.dataset.key); return; }
    setView(view);
    return;
  }

  const retry = e.target.closest("[data-retry]");
  if (retry) { retryOutbox(retry.dataset.retry); return; }
  const review = e.target.closest("[data-review]");
  if (review) { reviewOutbox(review.dataset.review); return; }
  const dismiss = e.target.closest("[data-dismiss]");
  if (dismiss) { dismissOutbox(dismiss.dataset.dismiss); return; }

  const closeBtn = e.target.closest("[data-close]");
  if (closeBtn) { hideModal(); return; }

  if (e.target.hasAttribute("data-close-overlay")) { hideModal(); return; }
}

function onGlobalInput(e) {
  const vis = e.target.closest("[data-entity-ref-visible]");
  if (!vis) return;
  const name = vis.dataset.entityRefVisible;
  const q = vis.value.trim().toLowerCase();
  const results = document.querySelector('[data-entity-ref-results="' + name + '"]');
  if (!results) return;
  if (!q) { results.innerHTML = ""; return; }
  const candidates = [...services(), ...instances()].map((r) => ({
    key: r.data.serviceKey || r.data.instanceKey || r.key,
    label: r.data.name || r.data.templateName || prettify(r.key),
  }));
  const matches = candidates.filter((c) => c.label.toLowerCase().includes(q)).slice(0, 6);
  results.innerHTML = matches.map((m) => `<div class="chip" style="cursor:pointer;display:block;margin-bottom:4px" data-entity-ref-pick="${esc(name)}" data-entity-ref-value="${esc(m.key)}">${esc(m.label)}</div>`).join("");
}

// -------------------------------------------------------------------- init

document.addEventListener("DOMContentLoaded", () => {
  document.body.addEventListener("click", onGlobalClick);
  document.body.addEventListener("input", onGlobalInput);
  loadWhoami();
  connectFeed();
});
