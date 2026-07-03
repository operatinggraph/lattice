// Component page (#/component/<id>): plural instance cards + component events
// on the left, the allow-list-shaped control surface on the right — row-level
// actions, no id-pasting. Data is GET /api/component/<id> (+ /api/lenses for
// the refractor roster). Op replies render into a persistent per-column reply
// box (loom inspect's reply IS the deliverable); a successful mutation
// re-renders only the rows, never the reply.

import { $, el, api, setStatus } from "../api.js";
import { replaceRoute } from "../router.js";
import { issueClass, lensStateDot, lensStateGlyph, pendingReadpathCopy } from "../logic/status.js";
import { metricsLine, eventSummary, controlSurface } from "../logic/component.js";
import { renderDoc, keyLinkEl } from "../render.js";

const state = { id: null };

function enter(route) {
  if (!route.arg) { replaceRoute("/map"); return; }
  state.id = route.arg;
  loadComponent(state.id);
}

async function loadComponent(id) {
  setStatus("comp-status", "loading…");
  const body = await api("/api/component/" + encodeURIComponent(id));
  if (id !== state.id) return; // navigated away while loading
  const head = $("#comp-head");
  const stateCol = $("#comp-state");
  const controlCol = $("#comp-control");
  head.innerHTML = "";
  stateCol.innerHTML = "";
  controlCol.innerHTML = "";
  if (body.error) {
    setStatus("comp-status", body.error, true);
    stateCol.appendChild(el("div", "error-text", body.error));
    const retry = el("button", null, "Retry");
    retry.addEventListener("click", () => loadComponent(id));
    stateCol.appendChild(retry);
    return;
  }
  setStatus("comp-status", "");
  renderHead(head, body);
  renderState(stateCol, body);
  renderControl(controlCol, body);
}

function renderHead(head, page) {
  head.appendChild(el("h2", "comp-title", page.label || page.component));
  head.appendChild(el("span", "state-tag comp-pill " + page.status, page.status));
  head.appendChild(el("span", "muted",
    page.instances.length + " instance" + (page.instances.length === 1 ? "" : "s")));
  if (!page.declared) head.appendChild(el("span", "state-tag", "runtime-discovered"));
  const onMap = el("a", "comp-onmap", "on map ↗");
  onMap.href = "#/map";
  head.appendChild(onMap);
  const refresh = el("button", null, "Refresh");
  refresh.addEventListener("click", () => loadComponent(page.component));
  head.appendChild(refresh);
}

// renderState fills the left column: one card per live instance, then the
// component-events section (grouped by kind, newest first within each group)
// for components that emit event keys.
function renderState(col, page) {
  col.appendChild(el("h3", "comp-section", "Instances"));
  if (!page.instances.length) {
    col.appendChild(el("div", "muted",
      "No live heartbeats — the component is absent from Health KV. Is it running? (make up-full)"));
  }
  page.instances.forEach((inst) => {
    const card = el("div", "card " + inst.status);
    card.appendChild(el("div", "card-key comp-instance", inst.instance));
    const meta = el("div", "card-meta");
    meta.appendChild(el("span", "card-status", inst.status));
    meta.appendChild(el("span", null, inst.freshness));
    const doc = inst.doc || {};
    if (doc.uptime) meta.appendChild(el("span", null, "up " + doc.uptime));
    if (doc.version) meta.appendChild(el("span", "muted", doc.version));
    card.appendChild(meta);
    const metrics = metricsLine(page.component, doc);
    if (metrics) card.appendChild(el("div", "comp-metrics", metrics));
    if (inst.issues && inst.issues.length) {
      const box = el("div", "card-issues");
      inst.issues.forEach((i) => box.appendChild(el("div", issueClass(i), i)));
      card.appendChild(box);
    }
    const raw = el("details", "comp-raw");
    raw.appendChild(el("summary", "muted small", "raw heartbeat"));
    raw.appendChild(renderDoc(doc));
    card.appendChild(raw);
    col.appendChild(card);
  });

  if (page.events && page.events.length) {
    col.appendChild(el("h3", "comp-section", "Events"));
    col.appendChild(el("p", "muted small",
      "Component-scoped Health KV event keys — grouped by kind, newest first; not part of any rollup."));
    // Group by kind, preserving the server's newest-first order both across
    // groups (a group sorts where its newest event sits) and within each.
    const groups = [];
    const byKind = {};
    page.events.forEach((ev) => {
      const kind = ev.kind || "(unknown)";
      if (!Object.prototype.hasOwnProperty.call(byKind, kind)) {
        byKind[kind] = [];
        groups.push(kind);
      }
      byKind[kind].push(ev);
    });
    groups.forEach((kind) => {
      col.appendChild(el("h4", "comp-subsection", kind + " (" + byKind[kind].length + ")"));
      byKind[kind].forEach((ev) => {
        const row = el("details", "comp-event");
        const sum = el("summary");
        sum.appendChild(el("span", "cid", ev.tail));
        if (ev.freshness) sum.appendChild(el("span", "muted small", ev.freshness));
        row.appendChild(sum);
        row.appendChild(renderDoc(ev.doc || {}));
        col.appendChild(row);
      });
    });
  }
}

// replyBox builds the column's persistent reply area: a collapsible details
// block that stays put while rows re-render around it.
function replyBox() {
  const box = el("details", "comp-reply");
  box.open = true;
  box.style.display = "none";
  box.appendChild(el("summary", "muted small", "reply"));
  const body = el("div", "comp-ctlout");
  box.appendChild(body);
  box.replyBody = body;
  return box;
}

function showReply(out, content) {
  out.style.display = "";
  out.open = true;
  out.replyBody.innerHTML = "";
  out.replyBody.appendChild(content);
}

// renderControl fills the right column with the component's control surface.
function renderControl(col, page) {
  const surface = controlSurface(page.component);
  col.appendChild(el("h3", "comp-section", "Control"));
  if (surface === "none") {
    col.appendChild(el("p", "muted small", "No operator control plane — state is above."));
    return;
  }
  if (surface === "events") {
    col.appendChild(el("p", "muted small",
      "No operator control plane. Event summary (the processor's observability surface):"));
    const counts = eventSummary(page.events);
    if (!counts.length) { col.appendChild(el("div", "muted", "(no events)")); return; }
    counts.forEach((c) => {
      const row = el("div", "control-item");
      row.appendChild(el("span", "cid", c.kind));
      row.appendChild(el("span", "state-tag", String(c.count)));
      col.appendChild(row);
    });
    return;
  }

  const rowsBox = el("div", "comp-ctlrows");
  const out = replyBox();
  col.appendChild(rowsBox);
  col.appendChild(out);

  if (surface === "refractor") {
    col.insertBefore(el("p", "muted small",
      "The lens roster — every live lens. Names link to the lens page."), rowsBox);
    const refresh = () => loadRoster(rowsBox, out);
    refresh();
    return;
  }
  const pageId = page.component;
  const refresh = async () => {
    const fresh = await api("/api/component/" + encodeURIComponent(pageId));
    if (state.id !== pageId || fresh.error) return;
    rowsBox.innerHTML = "";
    renderControlLists(rowsBox, fresh, out, refresh);
  };
  renderControlLists(rowsBox, page, out, refresh);
}

// renderControlLists renders loom/weaver list reads as rows with row-level
// action buttons — replies render into the shared reply box through the
// linkifying renderer (op-tracker keys in Weaver replies become links).
function renderControlLists(rowsBox, page, out, refresh) {
  const comp = page.component;
  // Shown-state logic keeps resume visible when paused, pause when running
  // (same for enable/disable).
  const listsByComp = {
    loom: [
      { read: "list", rowsField: "instances", idField: "instanceId", title: "Instances",
        actions: () => ["inspect"] },
      { read: "consumers", rowsField: "consumers", idField: "name", title: "Consumers",
        actions: (row) => [String(row.state || "").indexOf("paused") >= 0 ? "resume" : "pause"] },
    ],
    weaver: [
      { read: "list", rowsField: "targets", idField: "targetId", title: "Targets",
        actions: (row) => [String(row.state || "") === "disabled" ? "enable" : "disable", "revoke"] },
    ],
  };
  const control = page.control || {};
  (listsByComp[comp] || []).forEach((spec) => {
    rowsBox.appendChild(el("h4", "comp-subsection", spec.title));
    const reply = control[spec.read];
    if (!reply) { rowsBox.appendChild(el("div", "muted small", "(read unavailable)")); return; }
    if (reply.error) { rowsBox.appendChild(el("div", "error-text", reply.error)); return; }
    const rows = Array.isArray(reply[spec.rowsField]) ? reply[spec.rowsField] : [];
    if (!rows.length) { rowsBox.appendChild(el("div", "muted small", "(none)")); return; }
    rows.forEach((row) => {
      const id = row[spec.idField] || "";
      rowsBox.appendChild(controlRow(comp, id, row, spec.actions(row), out, refresh));
    });
  });
}

// controlRow builds one control-surface row: id · state tag · action buttons.
// revoke is terminal for a Weaver target, so it arms on first click and only
// fires on the confirming second click.
function controlRow(comp, id, row, ops, out, refresh) {
  const line = el("div", "control-item");
  line.appendChild(el("span", "cid", id || "(no id)"));
  const rowState = row.state || row.status || "";
  if (rowState) line.appendChild(el("span", "state-tag", String(rowState)));
  ops.forEach((op) => {
    const btn = el("button", "comp-ctlbtn", op);
    btn.addEventListener("click", () => {
      if (op === "revoke" && btn.dataset.armed !== "1") {
        btn.dataset.armed = "1";
        btn.textContent = "revoke — sure?";
        setTimeout(() => { btn.dataset.armed = ""; btn.textContent = "revoke"; }, 4000);
        return;
      }
      runControlOp(comp, id, op, line, out, refresh);
    });
    line.appendChild(btn);
  });
  return line;
}

// runControlOp POSTs the allow-listed mutate, renders the raw reply into the
// persistent reply box, then refreshes the rows so state changes show without
// destroying the reply. inspect/health are pure reads — no row refresh.
async function runControlOp(comp, id, op, line, out, refresh) {
  const btns = line.querySelectorAll("button");
  btns.forEach((b) => { b.disabled = true; });
  showReply(out, el("div", "muted small", comp + " " + op + " " + id + " …"));
  const body = await api("/api/control/" + comp + "/" + encodeURIComponent(id) + "/" + op, { method: "POST" });
  const reply = el("div");
  reply.appendChild(el("div", "muted small", comp + " " + op + " " + id + ":"));
  reply.appendChild(renderDoc(body));
  showReply(out, reply);
  btns.forEach((b) => { if (b.dataset.inert !== "1") b.disabled = false; });
  const isRead = op === "inspect" || op === "health";
  if (!body.error && !isRead && refresh) refresh();
}

// loadRoster fetches + renders the refractor lens roster into rowsBox — the
// directory of every live lens with quick per-row control actions; each name
// links to the lens page (#/lens/<id>), the full four-panel surface.
async function loadRoster(rowsBox, out) {
  rowsBox.innerHTML = "";
  rowsBox.appendChild(el("div", "muted small", "loading roster…"));
  const body = await api("/api/lenses");
  rowsBox.innerHTML = "";
  if (body.error) { rowsBox.appendChild(el("div", "error-text", body.error)); return; }
  const lenses = body.lenses || [];
  if (!lenses.length) { rowsBox.appendChild(el("div", "muted", "(no lenses projecting)")); return; }
  rowsBox.appendChild(el("div", "muted small", lenses.length + " lenses"));
  const refresh = () => loadRoster(rowsBox, out);
  // pending-readpath rows collect under a group footer so the roster's health
  // scan reads clean — they are expected fail-closed state, not degradation.
  const pending = lenses.filter((l) => l.status === "pending-readpath");
  lenses.filter((l) => l.status !== "pending-readpath")
    .forEach((lens) => rowsBox.appendChild(rosterRow(lens, out, refresh)));
  if (pending.length) {
    rowsBox.appendChild(el("h4", "comp-subsection", "pending read path (" + pending.length + ")"));
    rowsBox.appendChild(el("p", "muted small",
      pendingReadpathCopy + " — excluded from the health rollup."));
    pending.forEach((lens) => rowsBox.appendChild(rosterRow(lens, out, refresh)));
  }
}

// rosterRow builds one lens row: renderedState dot · name linked to the lens
// page · target chip · ◆ protected (spec-side, every state) · state text ·
// actions. rebuild is disabled while pending-readpath (the protected table's
// activation is out-of-band; a rebuild cannot help).
function rosterRow(lens, out, refresh) {
  const row = el("div", "control-item comp-lensrow");
  row.appendChild(el("span", "sysmap-dot " + (lensStateDot[lens.status] || "dim")));
  const label = el("a", "cid key-link", lens.canonicalName || lens.id);
  label.href = "#/lens/" + lens.id;
  row.appendChild(label);
  row.appendChild(el("span", "state-tag", lens.targetType === "postgres" ? "pg" : "kv"));
  if (lens.protected || lens.grantTable) row.appendChild(el("span", "state-tag comp-protected", "◆ protected"));
  const glyph = lensStateGlyph[lens.status];
  row.appendChild(el("span", "muted small", (glyph ? glyph + " " : "") + lens.status));
  ["health", "validate", "pause", "resume", "rebuild"].forEach((op) => {
    const btn = el("button", "comp-ctlbtn", op === "health" ? "inspect" : op);
    if (op === "rebuild" && lens.status === "pending-readpath") {
      // Permanently inert on this row — runControlOp's blanket re-enable must
      // not resurrect it as a dead button.
      btn.disabled = true;
      btn.dataset.inert = "1";
      btn.title = pendingReadpathCopy;
    } else {
      btn.addEventListener("click", () => runControlOp("refractor", lens.id, op, row, out, refresh));
    }
    row.appendChild(btn);
  });
  return row;
}

function init() {}

export { init, enter };
