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

// Human labels for the vertex types that surface in Facet. The floor rule
// (display-name-convention-design.md §2) composes a typed label from the key
// when no projected name exists — "Lease application · Lh1ry1", never a bare
// NanoID. A type without an entry titleCases its own segment.
const TYPE_LABELS = {
  leaseapp: "Lease application",
  identity: "Resident",
  building: "Building",
  unit: "Unit",
  location: "Place",
  role: "Role",
  service: "Service",
  task: "Task",
  tab: "Tab",
  booking: "Booking",
  appointment: "Appointment",
  session: "Class session",
  studio: "Studio",
  menuitem: "Menu item",
};
function typeLabel(type) {
  return TYPE_LABELS[type] || (type ? titleCase(type) : "Item");
}

// indefinite prepends the right article — "an Appointment", "a Class
// session" — for copy that names a type mid-sentence.
function indefinite(label) {
  return (/^[aeiou]/i.test(label) ? "an " : "a ") + label;
}

// prettify is the last rung of the floor-rule ladder (design §2):
// displayName → composed relational label → "<Type> · <short-id>". It never
// returns a bare NanoID as a primary label; the full key stays reachable on
// inspect. Used for any key the manifest didn't attach a projected name to.
function prettify(key) {
  const parts = (key || "").split(".");
  if (parts.length < 3) return key || "Unknown";
  return typeLabel(parts[1]) + " · " + parts[2].slice(0, 6);
}

// anchorLabel names a residence anchor (class-2 location, design §2). N1
// projects the unit's own `.presentation` name plus its container's name onto
// the manifest.me anchor; compose "Unit 1 · Riverside Building", falling
// through name-only / container-only to the typed floor — never a bare NanoID.
function anchorLabel(a) {
  if (!a) return "";
  if (a.name && a.containerName) return a.name + " · " + a.containerName;
  if (a.name) return a.name;
  if (a.containerName) return a.containerName;
  return prettify(a.key);
}

// splitAnchors separates the two identity spines edgeIdentity projects. Both
// arrive in one `anchors` array carrying a `relation` stamp ("residesIn" /
// "worksAt") because the cypher engine has no UNION to give them separate
// columns. They mean different things to a person — where you live vs. where
// you work — so the UI never merges them into one undifferentiated "Places".
//
// An anchor with no relation is treated as a residence: rows projected before
// the stamp existed carry none, and residence is what they were.
function splitAnchors(m) {
  const all = ((m && m.anchors) || []).filter((a) => a && a.key);
  return {
    homes: all.filter((a) => a.relation !== "worksAt"),
    workplaces: all.filter((a) => a.relation === "worksAt"),
  };
}

// chipRow renders a list of anchors as chips, or a caller-supplied empty note.
function chipRow(anchors, emptyHTML) {
  if (!anchors.length) return emptyHTML;
  return `<div class="chip-row">${anchors.map((a) => `<span class="chip">${esc(anchorLabel(a))}</span>`).join("")}</div>`;
}

// design §2). The lens projects the target's subject name (scopedName) — a
// SignLease task scopedTo a leaseapp carries its applied-for unit's name — so
// compose "Unit 1 lease" from the subject + the target's type. Absent subject
// name falls through to the typed floor (prettify), never a bare NanoID.
const RELATIONAL_SUFFIX = { leaseapp: "lease" };
function scopedLabel(scopedTo, scopedName) {
  if (!scopedTo) return "";
  if (!scopedName) return prettify(scopedTo);
  const type = (scopedTo.split(".")[1]) || "";
  const suffix = RELATIONAL_SUFFIX[type];
  return suffix ? scopedName + " " + suffix : scopedName;
}

// identityLabel names the signed-in identity (class-3, design §2). The sealed
// self-name (displayName) arrives via N3's vault decrypt; until then the floor
// rule renders the typed fallback, never "Unnamed" — an absent name is a typed
// label, not a shrug (design §2 renderer floor).
function identityLabel(m) {
  if (m && m.displayName) return m.displayName;
  if (m && m.identityKey) return prettify(m.identityKey);
  return shortIdentityLabel() || "Resident";
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
  // outboxHistory holds entries that fell out of the pinned Outbox section
  // once confirmed (§3.4's "still logged, collapsed under 'Outbox history'
  // if the user expands it") — newest first, bounded so a long-running tab
  // doesn't grow this unboundedly.
  outboxHistory: [],
  view: "home",
  credentials: null, // null = not yet loaded; array once GET /api/credentials resolves
};

const maxOutboxHistory = 50;

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
function entities() { return rowsByNs("manifest.ent"); }
function entitiesByType(type) { return entities().filter((e) => e.data.entityType === type); }

function opByFullKey(fullKey) {
  if (!fullKey) return null;
  const key = "manifest.op." + lastSeg(fullKey);
  const row = state.rows.get(key);
  return row ? { key, data: row.data, pending: row.pending } : null;
}

// --------------------------------------------------------------- the feed
//
// The renderer is a reducer over one stream of frames (manifest/outbox/ready/
// revoked/connectivity). Where those frames come from is a *source*: the Go
// host serves them over SSE (`/api/feed`); the browser-native host (EDGE.5 W4)
// serves the identical frames in-page over the wasm engine's `onFrame`. The
// reducer below never sees the difference — a source is any object with
//
//   start(handlers) -> void | Promise   wire the live stream to feedHandlers
//   enqueue(request) -> Promise<body>   submit one write; body.error on reject
//   close()          -> void            tear the stream down (sign-out §4.4)
//
// so W4's "renderer swap" is a source swap, not a rewrite: the boot module
// (boot.mjs) hands the renderer an edge-source when the page is configured for
// the in-page engine, and the renderer falls back to the SSE source otherwise.

let hasBootstrapped = false;
let sseSilenceTimer = null;
let activeSource = null; // the live feed source — closed on sign-out (§4.4 purge)

// feedHandlers is the reducer's entry point, called by whichever source is
// live. Both sources deliver the same parsed frame shapes (cmd/facet/feed.go
// and internal/edge/browser/feed.go are the same struct), so these handlers
// are source-agnostic.
const feedHandlers = {
  manifest(fr) {
    if (fr.deleted) state.rows.delete(fr.key);
    else state.rows.set(fr.key, { data: fr.data, pending: !!fr.pending });
    armSilenceFallback();
    scheduleRender();
  },
  outbox(entry) { applyOutboxFrame(entry); },
  ready() { finishBoot(); },
  revoked(reason) {
    showRevocationBanner(reason);
    finishBoot(); // never strand a revoked session on the boot spinner
  },
  // The reconnect banner keys on the host's own NATS connectivity (this frame,
  // design §4.4), never on the transport's own open/error — that link can stay
  // open through a NATS outage (and vice versa). syncDegraded is the frame's
  // second axis: the socket is fine but the sync manager is crash-looping
  // (restart backoff), so rows render yet new deltas never apply. Offline
  // wins — while disconnected, the reconnect banner already says stale.
  connectivity(fr) {
    if (fr.connected) hideReconnectBanner(); else showReconnectBanner();
    setSyncDegradedBanner(!!fr.syncDegraded && !!fr.connected);
  },
  open() {
    if (!hasBootstrapped) {
      setBootLabel("Loading your world…", true);
      armSilenceFallback();
    }
  },
};

// sseSource is the Go-host feed: the SSE stream `/api/feed` for frames and
// POST `/api/enqueue` for writes — the shipped path, unchanged in behaviour.
function sseSource() {
  let es = null;
  return {
    start(h) {
      es = new EventSource("/api/feed");
      es.addEventListener("manifest", (e) => h.manifest(JSON.parse(e.data)));
      es.addEventListener("outbox", (e) => h.outbox(JSON.parse(e.data).outbox));
      es.addEventListener("ready", () => h.ready());
      es.addEventListener("revoked", (e) => {
        let reason = "";
        try { reason = (JSON.parse(e.data) || {}).reason || ""; } catch (err) { /* keep the default copy */ }
        h.revoked(reason);
      });
      es.addEventListener("connectivity", (e) => h.connectivity(JSON.parse(e.data)));
      es.onopen = () => h.open();
      es.onerror = () => {
        // EventSource retries a transport hiccup by itself and stays CONNECTING;
        // it gives up and goes CLOSED only when the server answered something it
        // will not retry — which is what an expired session cookie produces here
        // (requireSession's 401). Probe before reacting: CLOSED alone does not
        // prove the session died.
        if (es && es.readyState === 2 /* CLOSED */) probeSessionForAuthDeath();
      };
    },
    enqueue(request) {
      return fetch("/api/enqueue", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(request),
      }).then((r) => {
        // requireSession answers a script call with 401 (it only redirects a
        // top-level navigation), so this is the unambiguous "your session
        // ended" signal on the write path.
        if (r.status === 401) { onAuthDeath(); return {}; }
        return r.json();
      });
    },
    close() { if (es) { es.close(); es = null; } },
  };
}

// probeSessionForAuthDeath asks /api/whoami whether the session is actually
// gone before evicting the page. EventSource reports only "errored", never a
// status, so a dead cookie and a server restart look identical from onerror —
// and whoami separates them: it is auth-exempt (never 401s itself) and answers
// loggedIn:false only for a session that is genuinely over. It also keeps the
// boot-env single-user fallback out of the bounce, since that reports loggedIn
// with no cookie behind it and has no /login to be sent to.
function probeSessionForAuthDeath() {
  fetch("/api/whoami")
    .then((r) => r.json())
    .then((body) => { if (body && body.loggedIn === false) onAuthDeath(); })
    .catch(() => {});
}

// onAuthDeath is the terminal-expiry reaction both feed modes share: the
// session is over and no renewal brings it back, so close the live feed —
// which drops the Go host's engine holder, letting an abandoned tab's engine
// reap instead of re-minting behind it — and hand off to /login. Distinct from
// signOut (the user did not ask, so there is no /api/logout to call) and from
// the revocation banner (a credential killed out from under a session that is
// otherwise still valid).
let authDeathHandled = false;
function onAuthDeath() {
  if (authDeathHandled) return;
  authDeathHandled = true;
  if (activeSource) { activeSource.close(); activeSource = null; }
  location.replace("/login");
}

// Published to window by the init handler below (browser glue stays out of
// this file's top level, which the unit vectors load headless) for boot.mjs's
// token refresher — it reaches this same terminal signal from the
// browser-native side, a 401 from /api/session/refresh, but cannot import a
// classic script.

// startFeed selects the source and wires it to the reducer. window.__facetBoot
// is a Promise<source> the boot module sets when the page is configured for the
// in-page engine; absent (the shipped Go-host page, and any boot that failed to
// configure), the renderer uses the SSE source. A boot that resolves a source
// but then throws while starting falls back to SSE too, so the app always loads.
function startFeed() {
  const boot = window.__facetBoot;
  Promise.resolve(boot || sseSource())
    .then((src) => { activeSource = src; return src.start(feedHandlers); })
    .catch((err) => {
      console.warn("facet: edge feed source failed, falling back to SSE", err);
      activeSource = sseSource();
      activeSource.start(feedHandlers);
    });
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
function setSyncDegradedBanner(visible) { $("sync-degraded-banner").hidden = !visible; }

// ---------------------------------------------------------- sign-out (§4.4)

// signOut implements the design's "on confirmed revocation/sign-out the
// local mirror is purged" — clears every in-memory row/outbox entry and
// closes the live SSE connection before the cookie is cleared, so no stray
// frame repopulates state after logout starts, then hands off to /login.
function signOut() {
  if (activeSource) { activeSource.close(); activeSource = null; }
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
      if (cur && cur.state === "confirmed") {
        state.outbox.delete(e.requestId);
        state.outboxHistory.unshift(cur);
        state.outboxHistory.length = Math.min(state.outboxHistory.length, maxOutboxHistory);
        scheduleRender();
      }
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
  else if (view === "browse") renderBrowse();
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
  $("identity-name").textContent = nm ? identityLabel(nm) : shortIdentityLabel();
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
  const { homes, workplaces } = splitAnchors(m);
  const svcs = services();
  const tsks = tasks();
  $("view-home").innerHTML = `
    ${homes.length || !workplaces.length ? `<section>
      <h2 class="section-title">My places</h2>
      ${chipRow(homes, `<div class="empty">No residence linked yet.</div>`)}
    </section>` : ""}
    ${workplaces.length ? `<section>
      <h2 class="section-title">Where I work</h2>
      ${chipRow(workplaces, "")}
    </section>` : ""}
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
  const due = d.expiresAt ? " &middot; due " + esc(relativeTime(d.expiresAt)) : "";

  // A queued row (edgeTasksQueued) is work offered to a ROLE the signed-in
  // person holds — nobody owns it yet, so opening its detail view to act on it
  // would be wrong. It gets a claim affordance instead; claiming swaps
  // queuedFor→assignedTo at the Processor, after which the row re-projects
  // through edgeTasks and renders as ordinary owned work below.
  if (d.queuedRole) {
    const role = d.queuedRoleName || prettify(d.queuedRole);
    return `<div class="card" style="flex-direction:row;align-items:center;gap:12px;cursor:default">
      ${t.pending ? `<span class="pending-chip">Pending</span>` : ""}
      <div style="flex:1">
        <div class="title">${esc(title)}</div>
        <div class="subtitle">${esc(scopedLabel(d.scopedTo, d.scopedName))}${due}</div>
        <div class="subtitle">Queued to ${esc(role)}</div>
      </div>
      <button class="btn" data-claim-task data-key="${esc(t.key)}">Claim</button>
    </div>`;
  }

  return `<div class="card" data-goto="task" data-key="${esc(t.key)}" style="flex-direction:row;align-items:center;gap:12px">
    ${t.pending ? `<span class="pending-chip">Pending</span>` : ""}
    <div style="flex:1">
      <div class="title">${esc(title)}</div>
      <div class="subtitle">${esc(scopedLabel(d.scopedTo, d.scopedName))}${due}</div>
    </div>
  </div>`;
}

// claimTask submits the shipped ClaimTask op for a role-queued task.
//
// This is ClaimTask's FIRST production dispatcher (the DDL script's own
// read-posture notes say so), which means it owes the read declarations the
// script relies on: the task itself is a required read, and the claimant's own
// assignedTo link is an OPTIONAL read — the script probes it to tell an
// idempotent re-claim by the same actor from a TaskAlreadyClaimed race, and on
// a first claim it is legitimately absent.
//
// No authContext: authority here is the standing role grant, and the script
// takes the claimant from the trusted envelope actor rather than any payload
// field, so there is nothing for the client to assert.
function claimTask(taskKey) {
  const m = me() || {};
  const actorId = bareKeyId(m.identityKey);
  const taskId = bareKeyId(taskKey);
  if (!actorId || !taskId) { toast("Cannot claim: unresolved identity", false); return; }

  activeSource.enqueue({
    operationType: "ClaimTask",
    class: "task",
    payload: { taskKey },
    reads: [taskKey],
    optionalReads: ["lnk.task." + taskId + ".assignedTo.identity." + actorId],
    authContext: undefined,
    touchedKey: taskKey,
  })
    .then((body) => {
      if (body && body.error) { toast(body.error, false); return; }
      toast("Claimed", true);
    })
    .catch((err) => toast(String(err), false));
}

// scopedLabel names a task's scoped target (class-4 relational label,

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
  // An op whose declared dispatch.targetType can't be resolved from this
  // context is not submittable from here — offering it anyway is how "Book a
  // class" reached the Processor with a vtx.identity where a vtx.session was
  // required. Say what's missing — and when the Nearby view actually has
  // entities of that type, link straight to it instead of dead-ending.
  if (d.dispatchTargetField && !resolveTargetKey(d, ctx)) {
    const label = typeLabel(d.dispatchTargetType);
    const browsable = d.dispatchTargetType && entitiesByType(d.dispatchTargetType).length > 0;
    const hint = browsable
      ? `Open ${esc(indefinite(label))} to do this — <a href="#" data-goto="browse">browse ${esc(label)}s</a>.`
      : `Open ${esc(indefinite(label))} to do this; it can't be started from here.`;
    return `<div class="degraded-card">${esc(d.title || prettifyOpType(d.operationType))} — ${hint}</div>`;
  }
  // A {me.<type>} contextParam is filled from the identity's own declared
  // anchors and never rendered, so an unresolvable one has no field the
  // visitor could correct — the op simply isn't theirs to submit yet.
  const missing = unresolvableSelfAnchor(d);
  if (missing) {
    return `<div class="degraded-card">${esc(d.title || prettifyOpType(d.operationType))} — This needs your own ${esc(typeLabel(missing))}; you don't have one yet.</div>`;
  }
  // Same gate for a {entity.<column>} contextParam: it is filled from the
  // viewed row and never rendered, so a column this row doesn't carry has no
  // field the visitor could correct — submitting "" is strictly worse than
  // saying so.
  const missingColumn = unresolvableEntityColumn(d, ctx);
  if (missingColumn) {
    return `<div class="degraded-card">${esc(d.title || prettifyOpType(d.operationType))} — This record is missing its ${esc(missingColumn)}; it can't be completed here.</div>`;
  }
  const label = d.submitLabel || d.title || prettifyOpType(d.operationType);
  const attrs = [`data-open-op="${esc(o.key)}"`];
  if (ctx.serviceKey) attrs.push(`data-service-key="${esc(ctx.serviceKey)}"`);
  if (ctx.taskKey) attrs.push(`data-task-key="${esc(ctx.taskKey)}"`);
  if (ctx.scopedTo) attrs.push(`data-scoped-to="${esc(ctx.scopedTo)}"`);
  if (ctx.entityKey) attrs.push(`data-entity-key="${esc(ctx.entityKey)}"`);
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

// -------------------------------------------------------------- Nearby
//
// The browse surface (facet-entity-browse-design.md §4 step 5): typed,
// reachability-bounded manifest.ent rows, grouped by entityType. Selecting
// one opens the entity detail, whose ops render through the EXISTING
// opButton/resolveTargetKey path with ctx.entityKey set — this view only
// feeds dispatch resolution, it never changes it. Deliberately not a graph
// browser (§3 F3): only entities a declared dispatch.targetType names are
// ever projected.

function renderBrowse() {
  const ents = entities();
  if (!ents.length) { $("view-browse").innerHTML = `<div class="empty">Nothing nearby to book yet.</div>`; return; }
  const byType = new Map();
  for (const e of ents) {
    const t = e.data.entityType || "other";
    if (!byType.has(t)) byType.set(t, []);
    byType.get(t).push(e);
  }
  let html = "";
  for (const [t, list] of byType) {
    list.sort((a, b) => String(a.data.startsAt || a.data.title || "").localeCompare(String(b.data.startsAt || b.data.title || "")));
    html += `<h3 class="category-heading">${esc(typeLabel(t))}s</h3>` + list.map(entityRow).join("");
  }
  $("view-browse").innerHTML = html;
}

function entityRow(e) {
  const d = e.data;
  return `<div class="card" data-goto="entity" data-key="${esc(e.key)}" style="flex-direction:row;align-items:center;gap:12px;margin-bottom:8px">
    <div style="flex:1">
      <div class="title">${esc(d.title || prettify(d.entityKey))}</div>
      <div class="subtitle">${esc(d.subtitle || "")}${d.startsAt ? ` &middot; ${esc(relativeTime(d.startsAt))}` : ""}</div>
    </div>
  </div>`;
}

// openEntityDetail is the seam the dispatch gate has been waiting on
// (app.js resolveTargetKey's ctx.entityKey candidate): the entity's
// offerable ops are exactly those whose declared dispatch.targetType IS
// this entity's type.
function openEntityDetail(key) {
  const row = state.rows.get(key);
  if (!row) return;
  const d = row.data;
  const myOps = ops().filter((o) => o.data.dispatchTargetType === d.entityType);
  showModal(`
    <button class="close-x" data-close>&times;</button>
    <h2>${esc(d.title || prettify(d.entityKey))}</h2>
    <p class="lead">${esc(d.subtitle || "")}${d.startsAt ? ` &middot; ${esc(relativeTime(d.startsAt))}` : ""}</p>
    <h3 class="category-heading">Operations</h3>
    ${myOps.length ? myOps.map((o) => opButton(o, { entityKey: d.entityKey })).join("") : `<div class="empty">Nothing to do here yet.</div>`}
  `);
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

// outboxHistoryExpanded toggles the collapsed "Outbox history" section
// (§3.4's reconciliation note) — collapsed by default so a confirmed order
// doesn't clutter the feed once its own manifest.inst.* row has taken over.
let outboxHistoryExpanded = false;

function renderActivity() {
  const pinned = [...state.outbox.values()]
    .filter((e) => e.state === "queued" || e.state === "submitting" || e.state === "rejected")
    .sort((a, b) => new Date(b.createdAt) - new Date(a.createdAt));
  const insts = instances().sort(byCreatedDesc);
  const history = state.outboxHistory;
  if (!pinned.length && !insts.length && !history.length) {
    $("view-activity").innerHTML = `<div class="empty">Nothing yet.</div>`;
    return;
  }
  const historyHtml = history.length
    ? `<button class="ghost-btn" data-toggle-outbox-history style="margin:12px 0">${outboxHistoryExpanded ? "Hide" : "Show"} Outbox history (${history.length})</button>`
      + (outboxHistoryExpanded ? history.map(outboxHistoryCard).join("") : "")
    : "";
  $("view-activity").innerHTML = pinned.map(outboxCard).join("") + insts.map(instanceRow).join("") + historyHtml;
}

function titleForOutbox(e) {
  const match = ops().find((o) => o.data.operationType === e.operationType);
  return (match && match.data.title) || prettifyOpType(e.operationType);
}

function outboxHistoryCard(e) {
  const stateLabel = e.state === "confirmed" ? "Done" : e.state;
  return `<div class="timeline-item">
    <div class="row1"><span class="title">${esc(titleForOutbox(e))}</span><span class="badge ${esc(e.state)}">${esc(stateLabel)}</span></div>
  </div>`;
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
  const row = state.rows.get("manifest.me");
  const m = row && row.data;
  if (!m) {
    $("view-me").innerHTML = `<div class="subtitle">Loading your identity…</div>`;
    return;
  }
  const roles = (m.roles || []).filter((r) => r.key);
  const { homes, workplaces } = splitAnchors(m);
  $("view-me").innerHTML = `
    <div class="card" style="cursor:default">
      ${row.pending ? `<span class="pending-chip">Pending</span>` : ""}
      <div class="title" style="font-size:18px">${esc(identityLabel(m))}</div>
      <div class="subtitle">${m.claimed ? "Claimed identity" : "Not yet claimed"}</div>
    </div>
    <h3 class="category-heading">Roles</h3>
    <div class="chip-row">${roles.length ? roles.map((r) => `<span class="chip">${esc(r.name || prettify(r.key))}</span>`).join("") : '<span class="subtitle">None</span>'}</div>
    <h3 class="category-heading">Places</h3>
    ${chipRow(homes, '<div class="chip-row"><span class="subtitle">None</span></div>')}
    ${workplaces.length ? `<h3 class="category-heading">Workplaces</h3>${chipRow(workplaces, "")}` : ""}
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

// selfAnchoredKeys indexes, by Contract #1 vertex type, the entity keys the
// signed-in identity owns — the `selfAnchors` set the me-row declares
// ({type, key} per entry, the type stamped by the walk that found it in
// edge-manifest's edgeIdentity lens), not anything inferred from a key or a
// field name. Returns a Map of vtx type -> Set of keys. Degenerate
// {key: null} entries (the identity holds no vertex of that type) are the
// expected OPTIONAL MATCH shape and drop here.
function selfAnchoredKeys() {
  const byType = new Map();
  const m = me();
  for (const a of ((m && m.selfAnchors) || [])) {
    if (!a || !a.type || typeof a.key !== "string" || !a.key.startsWith("vtx.")) continue;
    if (!byType.has(a.type)) byType.set(a.type, new Set());
    byType.get(a.type).add(a.key);
  }
  return byType;
}

// selfAnchorKey answers "the signed-in identity's own <type>" — and only when
// that is unambiguous. Zero (no such vertex) or several (two leases) is not a
// value to guess at: the caller degrades the op instead, which is the point
// of the anchor being declared rather than inferred.
function selfAnchorKey(type) {
  const keys = type && selfAnchoredKeys().get(type);
  return keys && keys.size === 1 ? [...keys][0] : undefined;
}

// unresolvableSelfAnchor returns the first {me.<type>} an op's
// dispatch.contextParams declares that this identity cannot answer, or
// undefined when every one resolves. A contextParam is filled and never
// rendered, so an unresolvable one would otherwise reach the Processor as an
// empty string — the same failure dispatchTargetType's gate exists to prevent.
function unresolvableSelfAnchor(op) {
  const params = maybeParseJSON(op.dispatchContextParams) || {};
  for (const template of Object.values(params)) {
    if (typeof template !== "string") continue;
    for (const m of template.matchAll(/\{me\.([^}]+)\}/g)) {
      if (!selfAnchorKey(m[1])) return m[1];
    }
  }
  return undefined;
}

// entityColumn answers "a column of the manifest.ent row this op is being
// submitted from" — the seam that lets an op declare a payload field whose
// value is a projected property of the entity being viewed, rather than one
// the visitor types. ctx.entityKey is the row's identity; a column that is
// absent, null or empty is no value at all and returns undefined so the
// caller can degrade rather than substitute "".
function entityColumn(ctx, column) {
  if (!ctx || !ctx.entityKey || !column) return undefined;
  const row = entities().find((e) => e.data && e.data.entityKey === ctx.entityKey);
  const v = row && row.data ? row.data[column] : undefined;
  return v === undefined || v === null || v === "" ? undefined : v;
}

// unresolvableEntityColumn returns the first {entity.<column>} an op's
// dispatch.contextParams declares that the viewed row cannot answer, or
// undefined when every one resolves. Same fail-closed rationale as
// unresolvableSelfAnchor: a contextParam is filled and never rendered, so an
// unresolvable one would reach the Processor as an empty string.
function unresolvableEntityColumn(op, ctx) {
  const params = maybeParseJSON(op.dispatchContextParams) || {};
  for (const template of Object.values(params)) {
    if (typeof template !== "string") continue;
    for (const m of template.matchAll(/\{entity\.([^}]+)\}/g)) {
      const column = m[1].endsWith(":id") ? m[1].slice(0, -3) : m[1];
      if (!entityColumn(ctx, column)) return column;
    }
  }
  return undefined;
}

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
    // A trailing `:id` asks for the Contract #1 bare id instead of the full
    // vtx key — what makes a 6-segment link key expressible as a declared
    // read (dispatch.optionalReads' ownership probes are built from bare ids).
    let bareId = false;
    if (expr.endsWith(":id")) { bareId = true; expr = expr.slice(0, -3); }
    const out = (v) => (bareId ? bareKeyId(v) : v);
    if (expr === "actor") return out((me() && me().identityKey) || "");
    if (expr === "service") return out(ctx.serviceKey || "");
    if (expr === "scopedTo") return out(ctx.scopedTo || "");
    // {me.<type>} — the submitting identity's own vertex of that type, from
    // the me-row's declared selfAnchors. opButton has already refused to
    // offer an op whose {me.<type>} doesn't resolve, so reaching "" here
    // means the anchor set changed mid-form; the empty value fails at the
    // Processor rather than substituting some other identity's key.
    if (expr.startsWith("me.")) return out(selfAnchorKey(expr.slice(3)) || "");
    // {entity.<column>} — a projected column of the manifest.ent row being
    // viewed. opButton has already refused to offer an op whose column
    // doesn't resolve, so reaching "" here means the row changed mid-form.
    if (expr.startsWith("entity.")) return out(entityColumn(ctx, expr.slice(7)) || "");
    if (expr.startsWith("payload.")) { const v = payload[expr.slice(8)]; return v === undefined ? "" : out(v); }
    return m;
  });
}

// bareKeyId reads the Contract #1 id out of a `vtx.<type>.<id>` key. An
// unresolved placeholder substitutes "" upstream, so an empty input stays
// empty and the resulting key fails at the Processor rather than silently
// addressing some other vertex.
function bareKeyId(key) {
  if (typeof key !== "string" || key === "") return "";
  const parts = key.split(".");
  return parts.length >= 3 ? parts[2] : "";
}

function buildAuthContext(kind, ctx) {
  const m = me();
  if (kind === "self") return { target: m && m.identityKey };
  if (kind === "service") return { service: ctx.serviceKey };
  if (kind === "task") return { task: ctx.taskKey, target: ctx.scopedTo };
  // "standing": authority is a standing role grant (cap.roles), so the envelope
  // carries no authContext at all. Spelled out rather than left to the
  // undefined fallthrough, because for a staff op sending nothing is the
  // CORRECT submission — not an unrecognized descriptor value being degraded.
  if (kind === "standing") return undefined;
  return undefined;
}

// keyType reads the Contract #1 vertex type out of a `vtx.<type>.<id>` key,
// or undefined for anything that isn't one.
function keyType(k) {
  return typeof k === "string" && k.startsWith("vtx.") ? k.split(".")[1] : undefined;
}

// resolveTargetKey answers what a `dispatch.targetField` payload key should
// hold, by matching the op's declared `dispatch.targetType` (a Contract #1
// vertex type) against the keys this context actually carries.
//
// Resolving by TYPE is the point: authContext says which wire-envelope field
// the client populates, NOT where targetField's value comes from. Keying the
// source off authContext conflated the two and filled every
// `authContext:"self"` op's typed entity field with the actor's identity key
// — wellness CreateBooking asked for a vtx.session and got a vtx.identity.
//
// Candidates run most-specific first: the entity in view, the task's scopedTo
// target, the service. ctx.entityKey is the seam a browse surface fills in
// (open a session, then "Book a class" resolves); nothing populates it yet,
// which is precisely why those ops read as unofferable rather than broken.
// Falling back to a unique self-anchored key of the wanted type lets an op
// resolve against an entity the visitor demonstrably owns.
//
// undefined means "this op cannot be submitted from here" — opButton's gate,
// not a hole to paper over with a wrong-typed key.
function resolveTargetKey(op, ctx) {
  if (!op.dispatchTargetField) return undefined;
  const want = op.dispatchTargetType;

  // An op meta predating the targetType vocabulary keeps the original
  // authContext-keyed mapping, which is correct for the one shape it ever got
  // right: a service-context op whose target IS the service.
  if (!want) {
    const kind = op.dispatchAuthContext;
    if (kind === "service") return ctx.serviceKey;
    if (kind === "task") return ctx.scopedTo;
    return undefined;
  }

  for (const c of [ctx.entityKey, ctx.scopedTo, ctx.serviceKey]) {
    if (keyType(c) === want) return c;
  }
  if (want === "identity") {
    const m = me();
    return (m && m.identityKey) || undefined;
  }
  return selfAnchorKey(want);
}

// resolveTouchedKey picks the Contract #1 key this write's optimistic
// effect should overlay (design R3; server.go's enqueueRequest.TouchedKey)
// — only meaningful for an update to an ALREADY-KNOWN key: a task's own key
// (it disappears from the Tasks list on confirm, so the chip marks "this is
// about to complete"), or the signed-in identity for a self-scoped op. A
// create op (RequestService mints the new instance's key server-side) has
// no predictable target and gets none, per server.go's own doc comment.
function resolveTouchedKey(op, ctx) {
  if (ctx.taskKey) return ctx.taskKey;
  if (op.dispatchAuthContext === "self") { const m = me(); return (m && m.identityKey) || undefined; }
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
    payload[op.dispatchTargetField] = resolveTargetKey(op, ctx);
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
  // The absence-tolerant half (Contract #2 §2.5 class-(d)): a uniqueness
  // guard whose prior claim was released, an ownership link that may not
  // exist for this caller. An entry that failed to substitute is dropped
  // rather than declared — a half-built key names nothing, and declaring it
  // would only make the script's absent-branch look deliberate.
  const optionalReads = (op.dispatchOptionalReads || [])
    .map((t) => substituteTemplate(t, ctx, payload))
    .filter((k) => k && !k.includes("{") && !k.includes(".."));
  const authContext = buildAuthContext(op.dispatchAuthContext, ctx);
  const touchedKey = resolveTouchedKey(op, ctx);

  // enqueue through the live source — POST /api/enqueue on the Go host, the
  // wasm engine's api.enqueue in-page — so the same descriptor form drives
  // either host unchanged (the W4 swap contract).
  activeSource.enqueue({ operationType: op.operationType, class: op.dispatchClass || "", payload, reads, optionalReads, authContext, touchedKey })
    .then((body) => {
      if (body && body.error) { toast(body.error, false); return; }
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

  const claimTaskBtn = e.target.closest("[data-claim-task]");
  if (claimTaskBtn) { claimTask(claimTaskBtn.dataset.key); return; }

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
      entityKey: openOp.dataset.entityKey || null,
    });
    return;
  }

  const goto = e.target.closest("[data-goto]");
  if (goto) {
    e.preventDefault();
    const view = goto.dataset.goto;
    if (view === "service") { openServiceDetail(goto.dataset.key); return; }
    if (view === "task") { openTaskDetail(goto.dataset.key); return; }
    if (view === "entity") { openEntityDetail(goto.dataset.key); return; }
    // A plain view jump can originate inside a modal (the degraded card's
    // "browse" link) — close it so the target view is actually visible.
    hideModal();
    setView(view);
    return;
  }

  const review = e.target.closest("[data-review]");
  if (review) { reviewOutbox(review.dataset.review); return; }
  const dismiss = e.target.closest("[data-dismiss]");
  if (dismiss) { dismissOutbox(dismiss.dataset.dismiss); return; }
  const toggleHistory = e.target.closest("[data-toggle-outbox-history]");
  if (toggleHistory) { outboxHistoryExpanded = !outboxHistoryExpanded; scheduleRender(); return; }

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
  window.__facetAuthDeath = onAuthDeath;
  document.body.addEventListener("click", onGlobalClick);
  document.body.addEventListener("input", onGlobalInput);
  loadWhoami();
  startFeed();
});
