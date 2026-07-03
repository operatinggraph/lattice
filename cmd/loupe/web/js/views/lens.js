// Lens page (#/lens/<id>, design §6): header + four stacked panels —
// DEFINITION (the DDL resolved from the graph), STATE (reporter truth + the
// Refractor heartbeat overlay + the reserved freshness slot), CONTROL
// (allow-list-shaped inline actions + delete behind a typed confirm), and
// CONTENTS (the read model itself — a nats_kv target's bucket, or a postgres
// target's table via the server's read-only LOUPE_PG_DSN seam, with the
// designed pg-pending state when no DSN is configured).
// Data: GET /api/lens/<id> + /api/lens/<id>/rows; mutations go through the
// allow-listed /api/control/refractor/<id>/<op> proxy.

import { $, el, api, setStatus, toast } from "../api.js";
import { navigate, replaceRoute } from "../router.js";
import { lensStateDot, lensStateGlyph, pendingReadpathCopy, issueClass } from "../logic/status.js";
import { lensControls, deleteConfirmToken, deleteConfirmReady, latencyLine } from "../logic/lens.js";
import { renderDoc, keyLinkEl } from "../render.js";

const state = { id: null, modal: null };

function enter(route) {
  closeModal();
  if (!route.arg) { replaceRoute("/component/refractor"); return; }
  state.id = route.arg;
  load(route.arg);
}

// leave closes a dangling delete modal so a route change can never leave a
// live destructive confirm floating over an unrelated view.
function leave() {
  closeModal();
}

function closeModal() {
  if (state.modal) { state.modal.close(); state.modal = null; }
}

// load renders the whole page. It clears the stale DOM BEFORE the fetch so a
// slow backend never leaves the previous lens's live control surface under
// the new lens's URL. preservedReply (a <details> element) carries the
// control panel's reply box across a post-mutation re-render, so the reply
// the operator was just shown survives the state re-fetch (§6.3:
// reply-inline AND re-fetch, not either/or).
async function load(id, preservedReply) {
  const head = $("#lens-head");
  const panels = $("#lens-panels");
  head.innerHTML = "";
  panels.innerHTML = "";
  panels.appendChild(el("div", "muted small", "loading…"));
  setStatus("lens-status", "loading…");
  const body = await api("/api/lens/" + encodeURIComponent(id));
  if (id !== state.id) return; // navigated away while loading
  head.innerHTML = "";
  panels.innerHTML = "";
  if (body.error) {
    setStatus("lens-status", body.error, true);
    const card = el("div", "notfound-card");
    card.appendChild(el("div", "notfound-key", id));
    card.appendChild(el("div", "muted", body.error));
    const back = el("a", "key-link", "← back to Refractor");
    back.href = "#/component/refractor";
    card.appendChild(back);
    panels.appendChild(card);
    return;
  }
  setStatus("lens-status", "");
  renderHead(head, body);
  panels.appendChild(definitionPanel(body));
  panels.appendChild(statePanel(body));
  panels.appendChild(controlPanel(body, preservedReply));
  panels.appendChild(contentsPanel(body));
}

// renderHead: canonicalName (falls back to id) · state pill · target chip ·
// the lens NanoID (mono, copyable).
function renderHead(head, lens) {
  head.appendChild(el("h2", "comp-title", lens.canonicalName || lens.id));
  const dot = el("span", "sysmap-dot " + (lensStateDot[lens.status] || "dim"));
  head.appendChild(dot);
  const glyph = lensStateGlyph[lens.status];
  head.appendChild(el("span", "state-tag lens-pill", (glyph ? glyph + " " : "") + lens.status));
  if (lens.targetType) {
    head.appendChild(el("span", "state-tag", lens.targetType === "postgres" ? "pg" : "kv"));
  }
  if (lens.isDeleted) head.appendChild(el("span", "deleted-flag", "isDeleted"));
  const idChip = el("button", "lens-id cid", lens.id);
  idChip.title = "copy lens id";
  idChip.addEventListener("click", async () => {
    try { await navigator.clipboard.writeText(lens.id); idChip.textContent = "copied ✓"; }
    catch (_) { idChip.textContent = "copy failed"; }
    setTimeout(() => { idChip.textContent = lens.id; }, 1200);
  });
  head.appendChild(idChip);
  const refresh = el("button", null, "Refresh");
  refresh.addEventListener("click", () => load(lens.id));
  head.appendChild(refresh);
}

function panel(title) {
  const box = el("section", "lens-panel");
  box.appendChild(el("h3", "comp-section", title));
  return box;
}

function kvRow(box, label, content) {
  const row = el("div", "lens-kvrow");
  row.appendChild(el("span", "lens-k", label));
  if (typeof content === "string") row.appendChild(el("span", null, content));
  else if (content) row.appendChild(content);
  box.appendChild(row);
}

// definitionPanel renders §6.1: identity, the honest target row, the cypher
// source, the output schema, and the owning package.
function definitionPanel(lens) {
  const box = panel("Definition");
  const def = lens.definition;
  if (!def) {
    box.appendChild(el("div", "muted",
      "no spec aspect on the meta-vertex — the reporter is live but the lens definition is not in the graph"));
    return box;
  }
  if (lens.description) kvRow(box, "description", lens.description);
  kvRow(box, "engine", def.engine || "simple→full fallback");
  if (def.projectionKind) kvRow(box, "projectionKind", def.projectionKind);

  const target = def.target || {};
  const tgt = el("span", "lens-target");
  tgt.appendChild(el("span", "state-tag", def.targetType || "?"));
  if (target.bucket) tgt.appendChild(el("span", null, "bucket " + target.bucket));
  if (target.table) tgt.appendChild(el("span", null, "table " + target.table));
  if (target.keyColumns) tgt.appendChild(el("span", null, "key (" + target.keyColumns.join(", ") + ")"));
  tgt.appendChild(el("span", null, "delete " + (target.deleteMode || "hard")));
  if (target.protected) tgt.appendChild(el("span", "state-tag comp-protected", "◆ protected"));
  if (target.public) tgt.appendChild(el("span", "state-tag", "public"));
  if (target.grantTable) tgt.appendChild(el("span", "state-tag comp-protected", "grant table"));
  if (def.targetType === "postgres") {
    tgt.appendChild(el("span", "muted small", target.dsnConfigured ? "dsn configured" : "dsn from environment"));
  }
  kvRow(box, "target", tgt);

  // The showcase artifact — the lens IS its query. Open by default under ~15
  // lines, collapsed above.
  const rule = def.cypherRule || "";
  const details = el("details");
  details.open = rule.split("\n").length <= 15;
  details.appendChild(el("summary", "muted small", "source query (cypher)"));
  details.appendChild(el("pre", "vtx-doc doc", rule || "(empty)"));
  box.appendChild(details);

  if (def.outputSchema !== undefined && def.outputSchema !== null) {
    const schema = el("details");
    schema.appendChild(el("summary", "muted small", "output schema"));
    schema.appendChild(renderDoc(def.outputSchema));
    box.appendChild(schema);
  }

  // Owning package: the chip resolves through keyTarget, so a package vertex
  // lands on its #/package page (raw envelope reachable from there).
  const owned = el("span", "lens-target");
  if (lens.package) {
    owned.appendChild(el("span", null, (lens.package.name || lens.package.key) +
      (lens.package.version ? " v" + lens.package.version : "")));
    owned.appendChild(keyLinkEl(lens.package.key, "small"));
  } else {
    owned.appendChild(el("span", null, "kernel (bootstrap-seeded)"));
  }
  const metaLink = el("a", "key-link small", "meta-vertex in Graph →");
  metaLink.href = "#/graph/" + lens.metaKey;
  owned.appendChild(metaLink);
  kvRow(box, "owned by", owned);
  return box;
}

// statePanel renders §6.2: reporter doc fields, the heartbeat overlay, and
// the reserved projection-freshness slot (a labeled row that lights up when
// the lens-projection-liveness platform design ships its signal).
function statePanel(lens) {
  const box = panel("State");
  const rep = lens.reporter || {};
  if (!rep.found) {
    box.appendChild(el("div", "muted",
      "no live reporter in Health KV — nothing is projecting this lens (is the Refractor running?)"));
  } else {
    kvRow(box, "reported status", String(rep.status || "?") +
      (rep.pauseReason ? " (" + rep.pauseReason + ")" : ""));
    if (lens.status === "pending-readpath") {
      box.appendChild(el("div", "muted small", pendingReadpathCopy + " — excluded from the health rollup."));
    }
    kvRow(box, "consumerLag", String(rep.consumerLag != null ? rep.consumerLag : "?"));
    kvRow(box, "errorCount", String(rep.errorCount != null ? rep.errorCount : "?"));
    if (rep.lastError) {
      const err = el("span", "error-text lens-lasterr", rep.lastError);
      kvRow(box, "lastError", err);
    }
    kvRow(box, "activeSequence", String(rep.activeSequence != null ? rep.activeSequence : "?"));
    if (rep.ruleEngine) kvRow(box, "engine (running)", rep.ruleEngine);
    kvRow(box, "lastUpdated", (rep.lastUpdated || "?") + (rep.freshness ? " · " + rep.freshness : ""));
  }
  (lens.issues || []).forEach((i) => box.appendChild(el("div", issueClass(i), i)));

  const rfx = lens.refractor;
  if (rfx) {
    if (rfx.lag != null) kvRow(box, "heartbeat lag", String(rfx.lag));
    const lat = latencyLine(rfx.latency);
    if (lat) kvRow(box, "projection latency", lat);
  }

  // The freshness slot: designed now, lights up when the platform signal
  // ships — the later fire is a data bind, not a redesign.
  const slot = el("span", "muted");
  slot.textContent = "— ";
  slot.appendChild(el("span", "muted small", "(pending: lens-projection-liveness — platform design)"));
  kvRow(box, "projection freshness", slot);
  return box;
}

// controlPanel renders §6.3: the allow-listed inline actions with reply-inline
// semantics, and delete apart from the others behind the typed confirm.
// preservedReply re-parents the previous render's reply box so a mutation's
// reply survives the post-mutation state re-fetch.
function controlPanel(lens, preservedReply) {
  const box = panel("Control");
  const isProtected = !!(lens.definition && lens.definition.target &&
    (lens.definition.target.protected || lens.definition.target.grantTable));
  const rowBox = el("div", "lens-ctlrow");
  let out = preservedReply;
  if (!out) {
    out = el("details", "comp-reply");
    out.style.display = "none";
    out.appendChild(el("summary", "muted small", "reply"));
    const outBody = el("div", "comp-ctlout");
    out.appendChild(outBody);
    out.replyBody = outBody;
  }

  const showReply = (content) => {
    out.style.display = "";
    out.open = true;
    out.replyBody.innerHTML = "";
    out.replyBody.appendChild(content);
  };

  lensControls(lens.status, isProtected).forEach((ctl) => {
    const btn = el("button", "comp-ctlbtn", ctl.op);
    if (ctl.note) btn.title = ctl.note;
    if (!ctl.enabled) {
      // Born-disabled by the enablement table — inert so the blanket
      // re-enable after a failed/read-only op cannot resurrect it.
      btn.disabled = true;
      btn.dataset.inert = "1";
    } else {
      btn.addEventListener("click", async () => {
        if (ctl.confirm && !window.confirm(ctl.op + " " + (lens.canonicalName || lens.id) + " — " + ctl.note)) return;
        rowBox.querySelectorAll("button").forEach((b) => { b.disabled = true; });
        showReply(el("div", "muted small", ctl.op + " " + lens.id + " …"));
        const body = await api("/api/control/refractor/" + encodeURIComponent(lens.id) + "/" + ctl.op, { method: "POST" });
        if (state.id !== lens.id) return; // navigated away mid-request
        const reply = el("div");
        reply.appendChild(el("div", "muted small", "refractor " + ctl.op + " " + lens.id + ":"));
        reply.appendChild(renderDoc(body));
        showReply(reply);
        if (!body.error && ctl.op !== "validate") {
          // A mutation changes state — re-render the page around the
          // preserved reply box.
          load(lens.id, out);
        } else {
          rowBox.querySelectorAll("button").forEach((b) => { if (!b.dataset.inert) b.disabled = false; });
        }
      });
    }
    rowBox.appendChild(btn);
  });
  box.appendChild(rowBox);
  box.appendChild(out);

  // delete — destructive, placed apart, typed confirm (§6.3).
  const delRow = el("div", "lens-delrow");
  const delBtn = el("button", "danger-btn", "delete lens…");
  delBtn.addEventListener("click", () => openDeleteModal(lens));
  delRow.appendChild(delBtn);
  delRow.appendChild(el("span", "muted small", "deletes the projection and its target rows"));
  box.appendChild(delRow);
  return box;
}

// openDeleteModal builds the typed-confirm modal: the destructive button
// stays disabled until the input exactly matches the lens canonicalName
// (falling back to the id for an unnamed lens). ESC or the backdrop close it
// (never while the delete is in flight); a failure reports into the modal
// itself; on success the page's subject no longer exists — toast + navigate
// back to the Refractor page. Route changes close it via leave()/enter().
function openDeleteModal(lens) {
  closeModal(); // never stack two confirms
  const token = deleteConfirmToken(lens.canonicalName, lens.id);
  let inFlight = false;
  const overlay = el("div", "modal-overlay");
  const modal = el("div", "modal");
  modal.appendChild(el("h3", null, "Delete lens"));
  modal.appendChild(el("p", "muted",
    "This deletes the projection and its target rows. Type the lens " +
    (lens.canonicalName ? "canonicalName" : "id") + " to confirm:"));
  modal.appendChild(el("div", "cid", token));
  const input = el("input");
  input.type = "text";
  input.placeholder = token;
  modal.appendChild(input);
  const actions = el("div", "modal-actions");
  const cancel = el("button", null, "Cancel");
  const confirm = el("button", "danger-btn", "Delete");
  confirm.disabled = true;
  actions.appendChild(cancel);
  actions.appendChild(confirm);
  modal.appendChild(actions);
  const msg = el("div", "small");
  modal.appendChild(msg);
  overlay.appendChild(modal);
  document.body.appendChild(overlay);
  input.focus();

  const close = () => {
    document.removeEventListener("keydown", onKey);
    overlay.remove();
    if (state.modal && state.modal.el === overlay) state.modal = null;
  };
  // Minimal focus trap + ESC (§12): Tab cycles the modal's focusables,
  // Escape closes unless the delete is in flight.
  const onKey = (e) => {
    if (e.key === "Escape" && !inFlight) { close(); return; }
    if (e.key === "Tab") {
      const focusables = [input, cancel, confirm].filter((f) => !f.disabled);
      if (!focusables.length) { e.preventDefault(); return; }
      const i = focusables.indexOf(document.activeElement);
      let next = i + (e.shiftKey ? -1 : 1);
      if (i === -1) next = 0;
      if (next < 0) next = focusables.length - 1;
      if (next >= focusables.length) next = 0;
      focusables[next].focus();
      e.preventDefault();
    }
  };
  document.addEventListener("keydown", onKey);
  state.modal = { el: overlay, close };

  cancel.addEventListener("click", () => { if (!inFlight) close(); });
  overlay.addEventListener("click", (e) => { if (e.target === overlay && !inFlight) close(); });
  input.addEventListener("input", () => {
    confirm.disabled = !deleteConfirmReady(input.value, token);
  });
  confirm.addEventListener("click", async () => {
    inFlight = true;
    confirm.disabled = true;
    cancel.disabled = true;
    input.disabled = true;
    msg.className = "muted small";
    msg.textContent = "deleting…";
    const body = await api("/api/control/refractor/" + encodeURIComponent(lens.id) + "/delete", { method: "POST" });
    inFlight = false;
    if (body.error) {
      // Report into the modal itself — the page underneath may have
      // re-rendered since it opened.
      msg.className = "error-text small";
      msg.textContent = "delete failed: " + body.error;
      cancel.disabled = false;
      input.disabled = false;
      return;
    }
    close();
    toast("lens " + (lens.canonicalName || lens.id) + " deleted");
    navigate("#/component/refractor");
  });
}

// contentsPanel renders §6.4: the read model itself. The panel, toolbar, and
// row rendering are identical for both target types — a postgres target
// browses through the read-only LOUPE_PG_DSN seam, or renders the designed
// pg-pending empty state when the server has no DSN configured.
function contentsPanel(lens) {
  const box = panel("Contents");
  const toolbar = el("div", "panel-controls");
  const q = el("input");
  q.type = "text";
  q.placeholder = "filter keys (substring)…";
  const go = el("button", null, "Filter");
  const status = el("span", "muted small");
  toolbar.appendChild(q);
  toolbar.appendChild(go);
  toolbar.appendChild(status);
  box.appendChild(toolbar);
  const rowsBox = el("div", "lens-rows");
  box.appendChild(rowsBox);

  // seq drops stale responses so two overlapping fetches (double-click,
  // Enter+click) can never render their rows twice into the same box.
  let seq = 0;
  const loadRows = async () => {
    const mySeq = ++seq;
    status.textContent = "loading…";
    const body = await api("/api/lens/" + encodeURIComponent(lens.id) + "/rows?limit=200&q=" + encodeURIComponent(q.value || ""));
    if (mySeq !== seq) return;
    rowsBox.innerHTML = "";
    status.textContent = "";
    if (body.error) { rowsBox.appendChild(el("div", "error-text", body.error)); return; }
    if (body.pgPending) {
      rowsBox.appendChild(el("div", "muted", "contents unavailable — postgres target; read seam not configured"));
      rowsBox.appendChild(el("div", "muted small",
        "this projection lives in a Postgres table; set LOUPE_PG_DSN (a read-only role) to browse it here"));
      return;
    }
    const rows = body.rows || [];
    status.textContent = rows.length + " of " + body.total + " row(s)" +
      (body.bucket ? " · bucket " + body.bucket : "") +
      (body.table ? " · table " + body.table : "") + (body.truncated ? " · truncated" : "");
    if (!rows.length) { rowsBox.appendChild(el("div", "muted", "(no rows)")); return; }
    rows.forEach((r) => {
      const row = el("details", "lens-row");
      row.appendChild(el("summary", "cid", r.key));
      // Every key-shaped string in the document walks back into the graph —
      // the read-path story told in one click.
      if (r.error) row.appendChild(el("div", "error-text small", "fetch failed: " + r.error));
      else row.appendChild(renderDoc(r.doc === undefined ? undefined : r.doc));
      rowsBox.appendChild(row);
    });
  };
  go.addEventListener("click", loadRows);
  q.addEventListener("keydown", (e) => { if (e.key === "Enter") loadRows(); });
  loadRows();
  return box;
}

function init() {}

export { init, enter, leave };
