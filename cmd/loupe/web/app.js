"use strict";

// Loupe UI — vanilla fetch, no framework. The Go server does all NATS I/O; this
// renders its JSON. Every /api/* response may carry {"error": ...}; renderError
// surfaces it inline rather than throwing.

function $(sel, root) { return (root || document).querySelector(sel); }
function $all(sel, root) { return Array.from((root || document).querySelectorAll(sel)); }

function el(tag, cls, text) {
  const e = document.createElement(tag);
  if (cls) e.className = cls;
  if (text !== undefined) e.textContent = text;
  return e;
}

function pretty(v) {
  try { return JSON.stringify(v, null, 2); }
  catch (_) { return String(v); }
}

// api GETs/POSTs JSON and returns the parsed body. A non-2xx with a JSON body is
// returned as-is (it carries {"error":...}); a transport failure is mapped to a
// synthetic {error} object so callers always get an object.
async function api(path, opts) {
  try {
    const res = await fetch(path, opts);
    const text = await res.text();
    let body;
    try { body = text ? JSON.parse(text) : {}; }
    catch (_) { body = { error: "non-JSON response: " + text.slice(0, 200) }; }
    return body;
  } catch (e) {
    return { error: "request failed: " + e.message };
  }
}

function setStatus(id, msg, isError) {
  const e = document.getElementById(id);
  if (!e) return;
  e.textContent = msg || "";
  e.className = "muted" + (isError ? " error-text" : "");
}

// ---- Tabs ----
// switchTab activates a tab+panel by name and lazy-loads it. Every drill-in
// (the System Map's node clicks included) routes through this one path.
function switchTab(tabName) {
  // Leaving the System Map (via a tab button or a node drill-in) stops its
  // auto-refresh poll so a hidden panel isn't polled.
  const leaving = $(".tab.active");
  if (leaving && leaving.dataset.tab === "systemmap" && tabName !== "systemmap") {
    stopSystemMapAuto();
  }
  $all(".tab").forEach((b) => b.classList.remove("active"));
  $all(".panel").forEach((p) => p.classList.remove("active"));
  const tab = $('.tab[data-tab="' + tabName + '"]');
  const panel = document.getElementById("panel-" + tabName);
  if (!tab || !panel) return;
  tab.classList.add("active");
  panel.classList.add("active");
  lazyLoad(tabName);
}

$all(".tab").forEach((btn) => {
  btn.addEventListener("click", () => switchTab(btn.dataset.tab));
});

const loaded = {};
function lazyLoad(tab) {
  if (tab === "systemmap") { loadSystemMap(); return; }
  if (tab === "corekv" && !loaded.corekv) { loadCoreKV(); loaded.corekv = true; return; }
  if (loaded[tab]) return;
  loaded[tab] = true;
  if (tab === "health") loadHealth();
  if (tab === "control") loadControl();
  if (tab === "packages") loadPackages();
  if (tab === "files") loadFiles();
  if (tab === "op") loadOps();
}

// ---- Core KV ----
let selectedKeyRow = null;

// shortId drops the "vtx.<type>." prefix, leaving the id (+ any trailing segs).
function shortId(key) { return key.split(".").slice(2).join("."); }

async function loadCoreKV() {
  const prefix = $("#corekv-prefix").value.trim();
  const limit = $("#corekv-limit").value.trim() || "500";
  setStatus("corekv-status", "loading…");
  const q = new URLSearchParams({ prefix, limit });
  const body = await api("/api/vertices?" + q.toString());
  const list = $("#corekv-keys");
  list.innerHTML = "";
  if (body.error) { setStatus("corekv-status", body.error, true); return; }
  setStatus("corekv-status", body.count + " vertices" + (body.truncated ? " (capped at " + body.limit + ")" : ""));
  (body.vertices || []).forEach((v) => {
    const row = el("div", "key-row vtx-row");
    row.appendChild(el("span", "badge vtype", v.type));
    const main = el("span", "ktext");
    main.appendChild(el("span", "vtx-label", v.label || shortId(v.key)));
    if (v.label) main.appendChild(el("span", "vtx-id", shortId(v.key)));
    row.appendChild(main);
    if (v.isDeleted) row.appendChild(el("span", "deleted-flag", "del"));
    row.addEventListener("click", () => {
      if (selectedKeyRow) selectedKeyRow.classList.remove("selected");
      row.classList.add("selected");
      selectedKeyRow = row;
      loadVertexDetail(v.key);
    });
    list.appendChild(row);
  });
  if (!body.vertices || !body.vertices.length) list.appendChild(el("div", "muted", "(no vertices)"));
}

async function loadVertexDetail(key) {
  const head = $("#corekv-valuehead");
  const detail = $("#corekv-detail");
  head.textContent = key;
  detail.innerHTML = "";
  detail.appendChild(el("div", "muted small", "loading…"));
  const body = await api("/api/vertex?key=" + encodeURIComponent(key));
  detail.innerHTML = "";
  if (body.error) { detail.appendChild(el("div", "error-text", body.error)); return; }

  head.textContent = key + " · r" + body.revision;
  if (body.isDeleted) head.appendChild(el("span", "deleted-flag", "isDeleted"));

  // Vertex document.
  detail.appendChild(el("div", "vtx-section-head", "document" + (body.class ? " · " + body.class : "")));
  const doc = el("pre", "vtx-doc");
  doc.textContent = body.envelope ? pretty(body.envelope) : "(non-JSON value)";
  detail.appendChild(doc);

  // Aspects.
  const aspects = body.aspects || [];
  detail.appendChild(el("div", "vtx-section-head", "aspects (" + aspects.length + ")"));
  if (!aspects.length) detail.appendChild(el("div", "muted small", "(none)"));
  aspects.forEach((a) => detail.appendChild(expanderRow(a.localName, "aspect", a.key)));

  // Links (either direction).
  const links = body.links || [];
  detail.appendChild(el("div", "vtx-section-head", "links (" + links.length + ")"));
  if (!links.length) detail.appendChild(el("div", "muted small", "(none)"));
  links.forEach((l) => {
    const arrow = l.direction === "out" ? "→" : "←";
    const label = arrow + " " + l.relation + " " + l.otherType + " · " + shortId(l.otherKey);
    detail.appendChild(expanderRow(label, "link " + l.direction, l.key));
  });
}

// expanderRow renders a collapsed row that lazy-loads the entry's document via
// /api/corekv/entry on first expand and toggles it thereafter.
function expanderRow(label, badge, key) {
  const wrap = el("div", "expander");
  const headEl = el("div", "expander-head");
  const arrow = el("span", "expander-arrow", "▸");
  headEl.appendChild(arrow);
  headEl.appendChild(el("span", "expander-label", label));
  if (badge) headEl.appendChild(el("span", "badge " + badge, badge));
  const bodyEl = el("pre", "expander-body");
  bodyEl.style.display = "none";
  let loaded = false;
  headEl.addEventListener("click", async () => {
    const isOpen = bodyEl.style.display !== "none";
    bodyEl.style.display = isOpen ? "none" : "block";
    arrow.textContent = isOpen ? "▸" : "▾";
    if (!isOpen && !loaded) {
      loaded = true;
      bodyEl.textContent = "loading…";
      const e = await api("/api/corekv/entry?key=" + encodeURIComponent(key));
      bodyEl.className = "expander-body" + (e.error ? " error-text" : "");
      bodyEl.textContent = e.error ? e.error : (e.envelope ? pretty(e.envelope) : "(non-JSON value)");
    }
  });
  wrap.appendChild(headEl);
  wrap.appendChild(bodyEl);
  return wrap;
}

$("#corekv-load").addEventListener("click", loadCoreKV);
$("#corekv-prefix").addEventListener("keydown", (e) => { if (e.key === "Enter") loadCoreKV(); });

// ---- Health ----
async function loadHealth() {
  setStatus("health-status", "loading…");
  const body = await api("/api/health");
  const cards = $("#health-cards");
  const alerts = $("#health-alerts");
  cards.innerHTML = "";
  alerts.innerHTML = "";
  const overall = $("#health-overall");
  if (body.error) {
    setStatus("health-status", body.error, true);
    overall.textContent = "";
    overall.className = "rollup";
    return;
  }
  setStatus("health-status", "");
  overall.textContent = body.overall;
  overall.className = "rollup " + body.overall;
  (body.components || []).forEach((c) => {
    const card = el("div", "card " + c.status);
    const title = el("div", "card-key", c.name || c.key);
    if (c.group && c.group !== c.name) title.appendChild(el("span", "card-group", c.group));
    card.appendChild(title);
    if (c.detail) card.appendChild(el("div", "card-sub", c.detail));
    const meta = el("div", "card-meta");
    meta.appendChild(el("span", "card-status", c.status));
    meta.appendChild(el("span", null, c.freshness));
    card.appendChild(meta);
    if (c.issues && c.issues.length) {
      const box = el("div", "card-issues");
      c.issues.forEach((i) => box.appendChild(el("div", "card-issue", i)));
      card.appendChild(box);
    }
    cards.appendChild(card);
  });
  if (!body.components || !body.components.length) {
    cards.appendChild(el("div", "muted", "(no health entries)"));
  }
  (body.alerts || []).forEach((a) => alerts.appendChild(el("div", "alert-line", a)));
}
$("#health-load").addEventListener("click", loadHealth);

// ---- Control ----
async function loadControl() {
  setStatus("control-status", "loading…");
  await Promise.all([
    loadControlReads("weaver"),
    loadControlReads("loom"),
  ]);
  setStatus("control-status", "");
}

// loadControlReads fetches a component's read lists and renders them. Refractor
// has no list endpoint (per-lens only) so its column is action-only.
async function loadControlReads(comp) {
  const col = $('.control-col[data-comp="' + comp + '"]');
  const listBox = $(".control-list", col);
  if (!listBox) return;
  const body = await api("/api/control/" + comp);
  listBox.innerHTML = "";
  if (body.error) { listBox.appendChild(el("div", "error-text", body.error)); return; }
  const reads = body.reads || {};
  Object.keys(reads).forEach((name) => {
    const reply = reads[name];
    listBox.appendChild(el("div", "muted small", name + ":"));
    renderControlList(comp, listBox, reply);
  });
}

// renderControlList renders a control plane's raw reply. Loupe forwards bytes
// verbatim, so the UI inspects loosely: render known list shapes (instances /
// targets / consumers) as clickable rows, else dump the JSON.
function renderControlList(comp, box, reply) {
  if (reply && reply.error) { box.appendChild(el("div", "error-text", reply.error)); return; }
  let rows = null;
  let idField = null;
  if (Array.isArray(reply)) rows = reply;
  else if (reply && Array.isArray(reply.instances)) { rows = reply.instances; idField = "instanceId"; }
  else if (reply && Array.isArray(reply.targets)) { rows = reply.targets; idField = "targetId"; }
  else if (reply && Array.isArray(reply.consumers)) { rows = reply.consumers; idField = "name"; }

  if (!rows) { box.appendChild(Object.assign(el("pre"), { textContent: pretty(reply) })); return; }
  if (!rows.length) { box.appendChild(el("div", "muted small", "(none)")); return; }
  rows.forEach((r) => {
    const item = el("div", "control-item");
    const id = idField ? r[idField] : (r.instanceId || r.targetId || r.name || r.id || "");
    const idSpan = el("span", "cid", id || "(no id)");
    if (id) idSpan.addEventListener("click", () => { $(".control-name", box.closest(".control-col")).value = id; });
    item.appendChild(idSpan);
    const state = r.state || r.status || (r.State || r.Status) || "";
    if (state) item.appendChild(el("span", "state-tag", String(state)));
    box.appendChild(item);
  });
}

// Wire every control column's action buttons.
$all(".control-col").forEach((col) => {
  const comp = col.dataset.comp;
  const nameInput = $(".control-name", col);
  const out = $(".control-out", col);
  $all(".control-action button", col).forEach((btn) => {
    btn.addEventListener("click", async () => {
      const name = nameInput.value.trim();
      if (!name) { out.textContent = "enter a name/id first"; out.className = "control-out error-text"; return; }
      out.className = "control-out";
      out.textContent = comp + " " + btn.dataset.op + " " + name + " …";
      const body = await api("/api/control/" + comp + "/" + encodeURIComponent(name) + "/" + btn.dataset.op, { method: "POST" });
      out.textContent = pretty(body);
      out.className = "control-out" + (body.error ? " error-text" : "");
      // Refresh lists so a state change shows immediately.
      if (comp === "weaver" || comp === "loom") loadControlReads(comp);
    });
  });
});
$("#control-load").addEventListener("click", loadControl);

// ---- Packages ----
async function loadPackages() {
  setStatus("packages-status", "loading…");
  const body = await api("/api/packages");
  const tbody = $("#packages-table tbody");
  tbody.innerHTML = "";
  if (body.error) { setStatus("packages-status", body.error, true); return; }
  setStatus("packages-status", body.count + " installed");
  (body.packages || []).forEach((p) => {
    const tr = el("tr");
    tr.appendChild(el("td", null, p.name));
    tr.appendChild(el("td", null, p.version));
    tr.appendChild(el("td", null, p.key));
    tbody.appendChild(tr);
  });
  if (!body.packages || !body.packages.length) {
    const tr = el("tr");
    const td = el("td", "muted", "(no packages installed)");
    td.colSpan = 3;
    tr.appendChild(td);
    tbody.appendChild(tr);
  }
}
$("#packages-load").addEventListener("click", loadPackages);

// ---- Submit Op ----
// opCatalog maps an operationType to its group (service), input schema, and
// description, built from GET /api/ops. The op picker drives a schema form.
let opCatalog = {};

async function loadOps() {
  setStatus("op-catalog-status", "loading…");
  const sel = $("#op-select");
  const body = await api("/api/ops");
  if (body.error) { setStatus("op-catalog-status", body.error, true); return; }
  opCatalog = {};
  sel.innerHTML = '<option value="">— choose an operation —</option>';
  (body.groups || []).forEach((g) => {
    const og = document.createElement("optgroup");
    og.label = g.name + (g.commands.length > 1 ? " (" + g.commands.length + ")" : "");
    (g.commands || []).forEach((cmd) => {
      opCatalog[cmd] = { group: g.name, schema: g.inputSchema || null, description: g.description || "" };
      const opt = el("option", null, cmd);
      opt.value = cmd;
      og.appendChild(opt);
    });
    sel.appendChild(og);
  });
  setStatus("op-catalog-status", (body.count || 0) + " service(s), " + Object.keys(opCatalog).length + " ops");
}

// renderOpForm builds one input per top-level property of a JSON-Schema object.
function renderOpForm(schema) {
  const host = $("#op-fields");
  host.innerHTML = "";
  if (!schema || schema.type !== "object" || !schema.properties) {
    host.appendChild(el("div", "muted small",
      "(no field schema for this op — use the raw payload under Advanced)"));
    return;
  }
  const required = new Set(schema.required || []);
  Object.keys(schema.properties).forEach((name) => {
    const p = schema.properties[name] || {};
    const isReq = required.has(name);
    const wrap = el("label", "op-field");
    const head = el("span", "op-field-name", name + (isReq ? " *" : ""));
    head.appendChild(el("span", "op-field-type", schemaTypeLabel(p)));
    wrap.appendChild(head);
    wrap.appendChild(buildInput(name, p, isReq));
    if (p.description) wrap.appendChild(el("span", "op-field-desc", p.description));
    host.appendChild(wrap);
  });
}

function schemaTypeLabel(p) {
  if (p.enum) return "enum";
  return Array.isArray(p.type) ? p.type.join("|") : (p.type || "any");
}

// buildInput maps a JSON-Schema property to a form control, tagging it with the
// field name + type so collectOpForm can coerce the value back.
function buildInput(name, p, isReq) {
  const type = Array.isArray(p.type) ? p.type[0] : p.type;
  let input;
  if (p.enum) {
    input = document.createElement("select");
    if (!isReq) input.appendChild(el("option", null, ""));
    p.enum.forEach((v) => { const o = el("option", null, String(v)); o.value = String(v); input.appendChild(o); });
  } else if (type === "boolean") {
    input = document.createElement("input"); input.type = "checkbox";
  } else if (type === "integer" || type === "number") {
    input = document.createElement("input"); input.type = "number";
  } else if (type === "array" || type === "object") {
    input = document.createElement("textarea"); input.rows = 3;
    input.placeholder = (type === "array" ? "[ … ]" : "{ … }") + " JSON";
  } else {
    input = document.createElement("input"); input.type = "text";
  }
  input.dataset.field = name;
  input.dataset.type = type || "string";
  if (isReq) input.dataset.required = "1";
  return input;
}

// deriveReads walks a payload and collects every key-shaped string value
// (vtx.* / lnk.*). A read-dependent op (Tombstone/Update/Assign/Grant…) must
// declare the keys it reads, and those keys are exactly the target references in
// its payload — so the form can supply ContextHint.Reads automatically.
function deriveReads(payload) {
  const out = [];
  const isKey = (s) => typeof s === "string" && (s.startsWith("vtx.") || s.startsWith("lnk."));
  const walk = (v) => {
    if (isKey(v)) out.push(v);
    else if (Array.isArray(v)) v.forEach(walk);
    else if (v && typeof v === "object") Object.values(v).forEach(walk);
  };
  walk(payload);
  return out;
}

// collectOpForm reads the rendered fields into a payload object. Empty optional
// fields are omitted; numbers/booleans/JSON are coerced. Throws on a malformed
// JSON field or a missing required field.
function collectOpForm() {
  const out = {};
  $all("#op-fields [data-field]").forEach((inp) => {
    const name = inp.dataset.field, type = inp.dataset.type, req = inp.dataset.required;
    if (type === "boolean") {
      if (inp.checked) out[name] = true; else if (req) out[name] = false;
      return;
    }
    const raw = inp.value.trim();
    if (raw === "") { if (req) throw new Error(name + " is required"); return; }
    if (type === "integer" || type === "number") {
      const n = Number(raw);
      if (Number.isNaN(n)) throw new Error(name + ": not a number");
      out[name] = n;
    } else if (type === "array" || type === "object") {
      try { out[name] = JSON.parse(raw); }
      catch (e) { throw new Error(name + ": invalid JSON — " + e.message); }
    } else {
      out[name] = raw;
    }
  });
  return out;
}

$("#op-select").addEventListener("change", () => {
  const entry = opCatalog[$("#op-select").value];
  $("#op-desc").textContent = entry ? (entry.group + (entry.description ? " — " + entry.description : "")) : "";
  renderOpForm(entry ? entry.schema : null);
  $("#op-payload").value = ""; // start from the form, not a stale raw payload
});
$("#op-reload").addEventListener("click", loadOps);

$("#op-submit").addEventListener("click", async () => {
  const override = $("#op-type").value.trim();
  const operationType = override || $("#op-select").value;
  const lane = $("#op-lane").value;
  const klass = $("#op-class").value.trim();
  const rawPayload = $("#op-payload").value.trim();
  const reply = $("#op-reply");

  if (!operationType) { setStatus("op-status", "choose an operation (or set an override)", true); return; }

  let payload;
  if (rawPayload) {
    try { payload = JSON.parse(rawPayload); }
    catch (e) { setStatus("op-status", "raw payload is not valid JSON: " + e.message, true); return; }
  } else {
    try { payload = collectOpForm(); }
    catch (e) { setStatus("op-status", e.message, true); return; }
  }

  setStatus("op-status", "submitting…");
  reply.textContent = "";
  reply.className = "";
  const manualReads = $("#op-reads").value.split(/[\s,]+/).map((s) => s.trim()).filter(Boolean);
  const reads = deriveReads(payload).concat(manualReads);
  const req = { operationType, lane, payload };
  if (klass) req.class = klass;
  if (reads.length) req.reads = reads;
  const body = await api("/api/op", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(req),
  });
  reply.textContent = pretty(body);
  if (body.error) {
    setStatus("op-status", "error", true);
    reply.className = "error-text";
  } else {
    setStatus("op-status", body.status || "done");
    reply.className = body.status === "accepted" ? "ok-text" : "";
  }
});

// ---- System Map ----
// The landing view: a hand-laid topology of the deployed components with the
// live Health KV overlay (GET /api/systemmap). Nodes are absolutely-positioned
// DOM in #sysmap-stage; edges are SVG paths measured from each node's box via
// getBoundingClientRect after layout. Rendering is kind-agnostic (driven by the
// status lookup tables) so a future kind:"agent" node is a data change, not new
// rendering logic.

const SYSMAP_TIER_Y = [40, 150, 270, 400, 530];
const SYSMAP_NODE_H = 58;
const SVG_NS = "http://www.w3.org/2000/svg";
const refractorId = "refractor"; // the sole lens parent (see systemmap.go)

// componentStatusClass / lensDotClass map a backend status string to the CSS
// class that drives its color. Unknown statuses fall back to a neutral dot.
const componentStatusClass = {
  green: "green", stale: "stale", absent: "absent", unknown: "unknown",
};
const lensDotClass = {
  active: "green", yellow: "yellow", paused: "yellow", rebuilding: "yellow", unknown: "dim",
};
const lensGlyph = { paused: "⏸", rebuilding: "⟳" };

// sysmap holds the last-rendered data + transient render state. nodeEls maps a
// node id to its DOM element for edge measurement; tip is the single shared
// hover popover.
const sysmap = { data: null, nodeEls: new Map(), tip: null, autoTimer: null, resizeTimer: null, fetchSeq: 0 };

// sysmapTier derives a node's tier (0..4) from its kind + id, never hardcoded
// x/y — so the layout survives backend node-set changes.
function sysmapTier(node) {
  if (node.kind === "lens") return 4;
  if (node.kind === "infra") {
    return node.id === "core-operations" ? 0 : 2; // core-kv / core-events = spine
  }
  // component
  return node.id === "processor" ? 1 : 3;
}

async function loadSystemMap() { return refreshSystemMap(); }

// refreshSystemMap is the single clock: re-fetches /api/systemmap and re-renders
// without blanking a previously-good map until the new data arrives. The future
// agent-activity console extends this same function rather than adding a second
// interval.
async function refreshSystemMap() {
  const btn = document.getElementById("sysmap-refresh");
  const had = !!sysmap.data;
  const seq = ++sysmap.fetchSeq;
  if (btn) { btn.disabled = true; btn.textContent = "Loading…"; }
  setStatus("sysmap-status", "loading…");
  if (!had) sysmapStageMessage("loading the system map…");

  const body = await api("/api/systemmap");
  if (seq !== sysmap.fetchSeq) return; // a newer refresh superseded this one
  if (btn) { btn.disabled = false; btn.textContent = "Refresh"; }

  if (body.error) {
    sysmap.data = null;
    renderSysmapError(body.error);
    setStatus("sysmap-status", "error", true);
    setSysmapRollup(null);
    return;
  }
  sysmap.data = body;
  renderSystemMap(body);
  setStatus("sysmap-status", "updated just now");
}

// sysmapStage returns the stage element, (re)creating its <svg> edge layer.
function sysmapStage() { return document.getElementById("sysmap-stage"); }

function sysmapStageMessage(msg) {
  const stage = sysmapStage();
  if (!stage) return;
  stage.innerHTML = "";
  stage.style.minHeight = "";
  const m = el("div", "sysmap-stage-msg muted", msg);
  stage.appendChild(m);
}

function renderSysmapError(err) {
  const stage = sysmapStage();
  if (!stage) return;
  stage.innerHTML = "";
  stage.style.minHeight = "";
  const box = el("div", "sysmap-stage-msg");
  box.appendChild(el("div", "error-text", err));
  const retry = el("button", null, "Retry");
  retry.addEventListener("click", refreshSystemMap);
  box.appendChild(retry);
  stage.appendChild(box);
}

// setSysmapRollup drives the overall banner + one-line plain-English summary and
// the red top-border cue. Called with null to clear (error state).
function setSysmapRollup(data) {
  const banner = document.getElementById("sysmap-overall");
  const summary = document.getElementById("sysmap-summary");
  const stage = sysmapStage();
  if (!banner || !summary) return;
  if (!data) {
    banner.textContent = "";
    banner.className = "rollup";
    summary.textContent = "";
    if (stage) stage.classList.remove("sysmap-red");
    return;
  }
  const overall = data.overall || "green";
  banner.textContent = overall.toUpperCase();
  banner.className = "rollup " + overall;
  const nodes = data.nodes || [];
  const healthy = new Set(["green", "active", "present"]);
  if (overall === "red") {
    const absent = nodes.filter((n) => n.status === "absent").length;
    summary.textContent = absent + " component(s) absent.";
    if (stage) stage.classList.add("sysmap-red");
  } else if (overall === "yellow") {
    const degraded = nodes.filter((n) => !healthy.has(n.status)).length;
    summary.textContent = degraded + " component(s)/lens(es) degraded.";
    if (stage) stage.classList.remove("sysmap-red");
  } else {
    summary.textContent = "All components healthy.";
    if (stage) stage.classList.remove("sysmap-red");
  }
}

// renderSystemMap lays out the nodes (tiers 0-3 absolutely positioned, tier-4
// lenses in a flex-wrap shelf), then schedules an edge pass after layout.
function renderSystemMap(data) {
  const stage = sysmapStage();
  if (!stage) return;
  setSysmapRollup(data);
  stage.innerHTML = "";
  sysmap.nodeEls = new Map();

  const svg = document.createElementNS(SVG_NS, "svg");
  svg.id = "sysmap-edges";
  svg.setAttribute("xmlns", SVG_NS);
  stage.appendChild(svg);

  const nodes = data.nodes || [];
  const width = stage.clientWidth || 1100;

  // Tiers 0-3: absolutely positioned, evenly spaced across the stage width.
  const tierMembers = [[], [], [], []];
  const lenses = [];
  nodes.forEach((n) => {
    const t = sysmapTier(n);
    if (t === 4) { lenses.push(n); return; }
    tierMembers[t].push(n);
  });

  // Refractor is the left-most tier-3 slot so its project edges drop cleanly
  // into the shelf without crossing the other engines' return paths.
  tierMembers[3].sort((a, b) => {
    if (a.id === refractorId) return -1;
    if (b.id === refractorId) return 1;
    return 0;
  });

  for (let t = 0; t < 4; t++) {
    const members = tierMembers[t];
    members.forEach((n, i) => {
      const node = buildSysmapNode(n);
      node.style.left = ((i + 1) / (members.length + 1) * width) + "px";
      node.style.top = SYSMAP_TIER_Y[t] + "px";
      node.style.transform = "translateX(-50%)";
      stage.appendChild(node);
      sysmap.nodeEls.set(n.id, node);
    });
  }

  // Tier 4: the lens shelf — flex-wrap chips, not per-node absolute placement.
  const shelf = el("div", "sysmap-shelf");
  shelf.style.top = SYSMAP_TIER_Y[4] + "px";
  if (!lenses.length) {
    shelf.appendChild(el("div", "muted", "(no lenses projecting)"));
  } else {
    lenses.forEach((n) => {
      const chip = buildSysmapNode(n);
      shelf.appendChild(chip);
      sysmap.nodeEls.set(n.id, chip);
    });
  }
  stage.appendChild(shelf);

  // Empty / no-health hint: every component absent and zero lenses.
  const components = nodes.filter((n) => n.kind === "component");
  if (components.length && components.every((n) => n.status === "absent") && !lenses.length) {
    const hint = el("div", "muted sysmap-hint",
      "No live components reporting — is the stack running? (make up-full)");
    stage.appendChild(hint);
  }

  // Size the stage to fit the shelf, then measure + draw edges after layout.
  requestAnimationFrame(() => {
    const stageNow = sysmapStage();
    if (!stageNow) return;
    const shelfEl = $(".sysmap-shelf", stageNow);
    const bottom = shelfEl ? shelfEl.offsetTop + shelfEl.offsetHeight : SYSMAP_TIER_Y[4] + SYSMAP_NODE_H;
    stageNow.style.minHeight = (bottom + 40) + "px";
    drawSysmapEdges(data);
  });
}

// buildSysmapNode renders one node element for its kind, with the status class,
// inline content, hover tooltip, and (for component/lens) the click drill-in.
function buildSysmapNode(n) {
  const node = el("div", "sysmap-node " + n.kind);
  node.dataset.status = n.status || "";
  node.dataset.id = n.id;

  if (n.kind === "component") {
    const cls = componentStatusClass[n.status] || "unknown";
    if (cls === "absent") node.classList.add("absent");
    if (cls === "stale") node.classList.add("stale");
    const head = el("div", "sysmap-node-head");
    head.appendChild(el("span", "sysmap-dot " + cls));
    head.appendChild(el("span", "sysmap-label", n.label));
    if (n.status === "stale") head.appendChild(el("span", "sysmap-tag", "stale"));
    if (n.issues && n.issues.length) head.appendChild(el("span", "sysmap-tag warn", "⚠ " + n.issues.length));
    node.appendChild(head);
    if (n.detail) {
      const d = el("div", "sysmap-detail", n.detail);
      node.appendChild(d);
    }
    if (n.freshness) node.appendChild(el("div", "sysmap-freshness", n.freshness));
  } else if (n.kind === "lens") {
    const cls = lensDotClass[n.status] || "dim";
    node.appendChild(el("span", "sysmap-dot " + cls));
    const g = lensGlyph[n.status];
    if (g) node.appendChild(el("span", "sysmap-glyph", g));
    node.appendChild(el("span", "sysmap-label", n.label));
  } else { // infra
    node.appendChild(el("span", "sysmap-label", n.label));
  }

  if (n.kind === "component" || n.kind === "lens") {
    node.addEventListener("mouseenter", (e) => showSysmapTip(n, e));
    node.addEventListener("mouseleave", hideSysmapTip);
    node.addEventListener("click", () => drillSysmapNode(n));
  }
  return node;
}

// drillSysmapNode routes a node click through the shared switchTab() helper.
// Components with a Control column (refractor/weaver/loom) go to Control; the
// others go to Health (where their heartbeat card lives). A lens goes to Control
// with the Refractor column prefilled with its id.
const sysmapControlComponents = new Set(["refractor", "weaver", "loom"]);
function drillSysmapNode(n) {
  hideSysmapTip();
  if (n.kind === "lens") {
    switchTab("control");
    const input = $('.control-col[data-comp="refractor"] .control-name');
    if (input) { input.value = n.id; input.focus(); }
    return;
  }
  if (sysmapControlComponents.has(n.id)) {
    switchTab("control");
    const col = $('.control-col[data-comp="' + n.id + '"]');
    if (col && col.scrollIntoView) col.scrollIntoView({ block: "nearest" });
    return;
  }
  switchTab("health");
}

// showSysmapTip places the shared popover near the hovered node with everything
// that doesn't fit inline (§3.3): id, kind, status, detail, freshness, issues,
// and — for a lens — a "KV" affordance into Core KV.
function showSysmapTip(n, evt) {
  hideSysmapTip();
  const stage = sysmapStage();
  if (!stage) return;
  const tip = el("div", "sysmap-tip");
  tip.appendChild(el("div", "sysmap-tip-id", n.id));
  const line = (k, v) => { const r = el("div", "sysmap-tip-line"); r.appendChild(el("span", "sysmap-tip-k", k)); r.appendChild(el("span", null, v)); tip.appendChild(r); };
  line("kind", n.kind);
  line("status", n.status);
  if (n.detail) line("detail", n.detail);
  if (n.freshness) line("freshness", n.freshness);
  (n.issues || []).forEach((i) => tip.appendChild(el("div", "sysmap-issue", i)));
  if (n.kind === "lens") {
    const kv = el("a", "sysmap-tip-kv", "view in Core KV");
    kv.addEventListener("click", (e) => {
      e.stopPropagation();
      hideSysmapTip();
      const prefix = $("#corekv-prefix");
      if (prefix) prefix.value = n.id;
      loaded.corekv = true; // suppress lazyLoad's default load — we load with the prefix below
      switchTab("corekv");
      loadCoreKV();
    });
    tip.appendChild(kv);
  }

  const node = sysmap.nodeEls.get(n.id);
  if (node) {
    tip.style.left = (node.offsetLeft) + "px";
    tip.style.top = (node.offsetTop + node.offsetHeight + 6) + "px";
  } else if (evt) {
    tip.style.left = evt.offsetX + "px";
    tip.style.top = evt.offsetY + "px";
  }
  stage.appendChild(tip);
  sysmap.tip = tip;
}

function hideSysmapTip() {
  if (sysmap.tip) { sysmap.tip.remove(); sysmap.tip = null; }
}

// drawSysmapEdges measures each node box relative to the stage and draws an SVG
// path per edge. Cleared and rebuilt from boxes each pass (cheap at this scale).
function drawSysmapEdges(data) {
  const stage = sysmapStage();
  const svg = document.getElementById("sysmap-edges");
  if (!stage || !svg) return;
  const stageBox = stage.getBoundingClientRect();
  svg.setAttribute("width", stage.clientWidth);
  svg.setAttribute("height", stage.scrollHeight);
  svg.innerHTML = "";

  // One shared arrowhead marker.
  const defs = document.createElementNS(SVG_NS, "defs");
  defs.innerHTML =
    '<marker id="sysmap-arrow" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse">' +
    '<path d="M0,0 L10,5 L0,10 z" fill="var(--border)"/></marker>';
  svg.appendChild(defs);

  // Stage-local box for a node id.
  const box = (id) => {
    const e = sysmap.nodeEls.get(id);
    if (!e) return null;
    const r = e.getBoundingClientRect();
    return { l: r.left - stageBox.left, t: r.top - stageBox.top, w: r.width, h: r.height };
  };
  const tierOf = (id) => {
    const node = (data.nodes || []).find((x) => x.id === id);
    return node ? sysmapTier(node) : -1;
  };

  // The upward "submit ops" returns share one gutter lane near the right edge
  // (clamped inside the stage) so they read as a single secondary return bus
  // rather than fanning off-stage; the bus is labelled once.
  const returnGutter = Math.max(stageBox.width - 52, 0);
  let returnLabelled = false;

  (data.edges || []).forEach((edge) => {
    const a = box(edge.from), b = box(edge.to);
    if (!a || !b) return; // edge resolves only when both endpoints exist
    const ta = tierOf(edge.from), tb = tierOf(edge.to);
    let x1, y1, x2, y2, mx, my, path, secondary = false, suppressLabel = false;

    if (ta === tb) {
      // Same-tier: side-center → side-center, shallow arc.
      const leftFirst = a.l < b.l;
      x1 = leftFirst ? a.l + a.w : a.l;
      x2 = leftFirst ? b.l : b.l + b.w;
      y1 = a.t + a.h / 2; y2 = b.t + b.h / 2;
      const cx = (x1 + x2) / 2;
      path = `M${x1},${y1} C${cx},${y1 - 24} ${cx},${y2 - 24} ${x2},${y2}`;
      mx = cx; my = (y1 + y2) / 2 - 18;
    } else if (ta > tb) {
      // Upward return (submit ops): up the shared right-gutter bus, dimmer +
      // thinner. Labelled once (the lanes overlap into one visual return bus).
      secondary = true;
      x1 = a.l + a.w; y1 = a.t + a.h / 2;
      x2 = b.l + b.w; y2 = b.t + b.h / 2;
      const gutter = returnGutter;
      path = `M${x1},${y1} C${gutter},${y1} ${gutter},${y2} ${x2},${y2}`;
      mx = gutter; my = (y1 + y2) / 2;
      if (returnLabelled) suppressLabel = true;
      returnLabelled = true;
    } else {
      // Downward: bottom-center → top-center, cubic with vertical control.
      x1 = a.l + a.w / 2; y1 = a.t + a.h;
      x2 = b.l + b.w / 2; y2 = b.t;
      const dy = (y2 - y1) * 0.4;
      path = `M${x1},${y1} C${x1},${y1 + dy} ${x2},${y2 - dy} ${x2},${y2}`;
      mx = (x1 + x2) / 2; my = (y1 + y2) / 2;
    }

    const p = document.createElementNS(SVG_NS, "path");
    p.setAttribute("d", path);
    p.setAttribute("class", "sysmap-edge" + (secondary ? " secondary" : ""));
    p.setAttribute("marker-end", "url(#sysmap-arrow)");
    svg.appendChild(p);

    if (edge.label && !suppressLabel) {
      const g = document.createElementNS(SVG_NS, "g");
      const t = document.createElementNS(SVG_NS, "text");
      t.setAttribute("x", mx);
      t.setAttribute("y", my);
      t.setAttribute("class", "sysmap-edge-label");
      t.setAttribute("text-anchor", "middle");
      t.textContent = edge.label;
      // Background rect sized from the text once it is in the DOM.
      const rect = document.createElementNS(SVG_NS, "rect");
      rect.setAttribute("class", "sysmap-edge-label-bg");
      rect.setAttribute("rx", "3");
      g.appendChild(rect);
      g.appendChild(t);
      svg.appendChild(g);
      let tb2 = null;
      try { tb2 = t.getBBox(); } catch (_) { /* not rendered yet */ }
      if (tb2 && tb2.width) {
        rect.setAttribute("x", tb2.x - 3);
        rect.setAttribute("y", tb2.y - 1);
        rect.setAttribute("width", tb2.width + 6);
        rect.setAttribute("height", tb2.height + 2);
      }
    }
  });
}

// Auto-refresh: opt-in 10s poll, paused while the tab is backgrounded and
// stopped when the operator leaves the System Map tab.
function startSystemMapAuto() {
  if (sysmap.autoTimer) return;
  sysmap.autoTimer = setInterval(() => {
    if (document.hidden) return;
    refreshSystemMap();
  }, 10000);
}
function stopSystemMapAuto() {
  if (sysmap.autoTimer) { clearInterval(sysmap.autoTimer); sysmap.autoTimer = null; }
  const cb = document.getElementById("sysmap-auto");
  if (cb) cb.checked = false;
}

(function wireSystemMap() {
  const refresh = document.getElementById("sysmap-refresh");
  if (refresh) refresh.addEventListener("click", refreshSystemMap);
  const auto = document.getElementById("sysmap-auto");
  if (auto) auto.addEventListener("change", () => { auto.checked ? startSystemMapAuto() : stopSystemMapAuto(); });

  // Re-measure + redraw on resize (debounced), only while the map is rendered.
  window.addEventListener("resize", () => {
    if (sysmap.resizeTimer) clearTimeout(sysmap.resizeTimer);
    sysmap.resizeTimer = setTimeout(() => {
      const panel = document.getElementById("panel-systemmap");
      if (sysmap.data && panel && panel.classList.contains("active")) {
        renderSystemMap(sysmap.data);
      }
    }, 120);
  });
})();

// ---- Files (off-graph blob plane) ----

// uploadObject POSTs the multipart form to /api/objects. Uses fetch directly
// (not api()) because the body is FormData, not JSON.
async function uploadObject() {
  const target = $("#files-target").value.trim();
  const linkName = $("#files-linkname").value.trim();
  const replace = $("#files-replace").value.trim();
  const fileInput = $("#files-file");
  const reply = $("#files-upload-reply");
  if (!target || !linkName) { setStatus("files-upload-status", "target key and link name are required", true); return; }
  if (!fileInput.files || !fileInput.files.length) { setStatus("files-upload-status", "choose a file first", true); return; }

  const fd = new FormData();
  fd.append("file", fileInput.files[0]);
  fd.append("targetKey", target);
  fd.append("linkName", linkName);
  if (replace) fd.append("replaceObjectId", replace);

  setStatus("files-upload-status", "uploading…");
  reply.textContent = "";
  reply.className = "";
  let body;
  try {
    const res = await fetch("/api/objects", { method: "POST", body: fd });
    const text = await res.text();
    try { body = text ? JSON.parse(text) : {}; }
    catch (_) { body = { error: "non-JSON response: " + text.slice(0, 200) }; }
  } catch (e) {
    body = { error: "request failed: " + e.message };
  }
  reply.textContent = pretty(body);
  if (body.error || (body.status && body.status === "rejected")) {
    setStatus("files-upload-status", "failed", true);
    reply.className = "error-text";
    return;
  }
  setStatus("files-upload-status", "attached " + (body.oid || ""));
  reply.className = "ok-text";
  fileInput.value = "";
  loadFiles();
}

// loadFiles lists object→owner links (a lnk.object.* prefix scan) and renders a
// card per link: an inline thumbnail (for image objects), a download link, and
// a detach button. v1a has no object-listing lens, so this scans Core KV keys
// directly (a Loupe-only inspection path, P5 debug exception).
async function loadFiles() {
  setStatus("files-status", "loading…");
  const grid = $("#files-grid");
  grid.innerHTML = "";
  const body = await api("/api/corekv?prefix=lnk.object.&limit=500");
  if (body.error) { setStatus("files-status", body.error, true); return; }
  const links = (body.keys || []).filter((k) => k.class === "link");
  if (!links.length) { grid.appendChild(el("div", "muted", "(no attached objects)")); setStatus("files-status", "0 links"); return; }
  setStatus("files-status", links.length + " link(s)" + (body.truncated ? " (capped)" : ""));

  for (const k of links) {
    // lnk.object.<oid>.<linkName>.<tgtType>.<tgtId>
    const parts = k.key.split(".");
    if (parts.length !== 6) continue;
    const oid = parts[2], linkName = parts[3];
    const targetKey = "vtx." + parts[4] + "." + parts[5];

    const entry = await api("/api/corekv/entry?key=" + encodeURIComponent(k.key));
    if (entry.isDeleted) continue; // detached — skip

    const card = el("div", "file-card");
    const thumb = el("img", "file-thumb");
    thumb.src = "/api/objects/" + encodeURIComponent(oid);
    thumb.alt = oid;
    thumb.addEventListener("error", () => { thumb.replaceWith(el("div", "file-thumb file-thumb-none", "no preview")); });
    card.appendChild(thumb);

    const meta = el("div", "file-meta");
    meta.appendChild(el("div", "file-oid", oid));
    meta.appendChild(el("div", "muted small", linkName + " → " + targetKey));
    const actions = el("div", "file-actions");
    const dl = el("a", "file-link", "download");
    dl.href = "/api/objects/" + encodeURIComponent(oid);
    dl.setAttribute("download", "");
    actions.appendChild(dl);
    const detach = el("button", "file-detach", "detach");
    detach.addEventListener("click", () => detachObject(oid, targetKey, linkName));
    actions.appendChild(detach);
    meta.appendChild(actions);
    card.appendChild(meta);
    grid.appendChild(card);
  }
  if (!grid.children.length) grid.appendChild(el("div", "muted", "(no live attached objects)"));
}

async function detachObject(oid, targetKey, linkName) {
  setStatus("files-status", "detaching " + oid + "…");
  const q = new URLSearchParams({ targetKey, linkName });
  const body = await api("/api/objects/" + encodeURIComponent(oid) + "?" + q.toString(), { method: "DELETE" });
  if (body.error || body.status === "rejected") {
    setStatus("files-status", "detach failed: " + (body.error || pretty(body.error)), true);
    return;
  }
  loadFiles();
}

$("#files-upload-btn").addEventListener("click", uploadObject);
$("#files-load").addEventListener("click", loadFiles);

// Load the default (System Map) tab on first paint.
loadSystemMap();
loaded.systemmap = true;
