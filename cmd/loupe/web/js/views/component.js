// Component page (#/component/<id>): plural instance cards + component events
// on the left, the allow-list-shaped control surface on the right — row-level
// actions, no id-pasting. Data is GET /api/component/<id> (+ /api/lenses for
// the refractor roster). Op replies render into a persistent per-column reply
// box (loom inspect's reply IS the deliverable); a successful mutation
// re-renders only the rows, never the reply.

import { $, el, api, setStatus, toast } from "../api.js";
import { replaceRoute } from "../router.js";
import { offlineComponentCopy, offlineComponentPointer, issueClass, lensStateDot, lensStateGlyph, pendingReadpathCopy } from "../logic/status.js";
import { metricsLine, eventSummary, controlSurface } from "../logic/component.js";
import { authFailureRate, pctLabel, jwksRows, revocationStatus, revokeActorValid, revokeConfirmReady } from "../logic/gateway.js";
import { shredFleetSummary, shredFinalizationLine, shredInFlight } from "../logic/shred.js";
import { renderDoc, keyLinkEl } from "../render.js";

const state = { id: null, modal: null, revokeTimers: [] };

// statusTextClass maps a logic-tier status class (ok/warn/muted) to the
// console's text-color classes.
const statusTextClass = { ok: "ok-text", warn: "warn-text", muted: "muted" };

function closeModal() {
  if (state.modal) { state.modal.close(); state.modal = null; }
}

// clearRevokeTimers cancels any pending revocation-list refreshes — they
// close over the render that scheduled them, so they must never outlive it.
function clearRevokeTimers() {
  state.revokeTimers.forEach(clearTimeout);
  state.revokeTimers = [];
}

// leave closes a dangling revoke modal and its refresh timers so a route
// change can never leave a live destructive confirm floating over an
// unrelated view or fire fetches into a detached list.
function leave() {
  closeModal();
  clearRevokeTimers();
}

function enter(route) {
  if (!route.arg) { replaceRoute("/map"); return; }
  state.id = route.arg;
  loadComponent(state.id);
}

async function loadComponent(id) {
  // A full re-render invalidates everything a previous render captured: an
  // open confirm modal and any pending list refreshes point at nodes this
  // wipe is about to detach.
  closeModal();
  clearRevokeTimers();
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
    // An offline optional component (up-full only) is EXPECTED to be
    // heartbeatless here — name where it runs instead of alarming (the same
    // rule as its map node).
    if (page.status === "offline") {
      col.appendChild(el("div", "muted", offlineComponentCopy + "."));
      if (offlineComponentPointer[page.component]) {
        col.appendChild(el("div", "muted", offlineComponentPointer[page.component]));
      }
    } else {
      col.appendChild(el("div", "muted",
        "No live heartbeats — the component is absent from Health KV. Is it running? (make up-full)"));
    }
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

  if (page.component === "gateway" && page.instances.length) {
    renderGatewaySecurity(col, page);
  }

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

// renderGatewaySecurity fills the Gateway page's left-column security panels:
// the auth-failure ratio (the security headline) and the trusted JWKS key
// set. Both read the first instance's heartbeat — the dev deployment runs one
// Gateway; with several, the panel names the instance it reflects.
// liveInstance picks the heartbeat the Gateway security surfaces reflect: the
// first green instance, falling back to the first by sort order — a dead
// alphabetically-first instance must not present its stale state as the
// page's security headline while a healthy sibling serves traffic.
function liveInstance(page) {
  return page.instances.find((i) => i.status === "green") || page.instances[0];
}

function renderGatewaySecurity(col, page) {
  const inst = liveInstance(page);
  const doc = inst.doc || {};
  const m = doc.metrics || {};
  const suffix = page.instances.length > 1 ? " — instance " + inst.instance : "";

  col.appendChild(el("h3", "comp-section", "Auth failures" + suffix));
  const rate = authFailureRate(m);
  const line = el("div", "comp-metrics");
  line.appendChild(el("span", statusTextClass[rate.cls] || "muted", pctLabel(rate) + " failing"));
  line.appendChild(el("span", "muted", " · lifetime: " +
    (typeof m.auth_failures_total === "number" ? m.auth_failures_total : "?") + " of " +
    (typeof m.requests_total === "number" ? m.requests_total : "?") + " requests · " +
    (typeof m.ops_submitted_total === "number" ? m.ops_submitted_total : "?") + " ops submitted"));
  col.appendChild(line);

  col.appendChild(el("h3", "comp-section", "Trusted keys (JWKS)" + suffix));
  const jwks = jwksRows(doc);
  if (!jwks) {
    col.appendChild(el("div", "muted small",
      "JWKS state not reported by this Gateway build — the trusted key set appears when the heartbeat carries it."));
    return;
  }
  if (!jwks.keys.length) {
    col.appendChild(el("div", "warn-text small", "No trusted keys — every JWT fails verification."));
  }
  jwks.keys.forEach((k) => {
    const row = el("div", "control-item");
    row.appendChild(el("span", "cid", k.kid));
    row.appendChild(el("span", "state-tag", k.source));
    row.appendChild(el("span", "state-tag", k.alg));
    if (k.addedAt) row.appendChild(el("span", "muted small", "added " + k.addedAt));
    col.appendChild(row);
  });
  col.appendChild(el("div", (statusTextClass[jwks.poll.cls] || "muted") + " small", jwks.poll.line));
  if (jwks.swaps.length) {
    const hist = el("details", "comp-raw");
    hist.appendChild(el("summary", "muted small", "key-set changes (" + jwks.swaps.length + ")"));
    jwks.swaps.forEach((s) => hist.appendChild(el("div", "muted small", s)));
    col.appendChild(hist);
  }
}

// renderVaultInfo fills the Vault page's right column with the shred-status
// fleet view: Vault custody is not operator-mutable (loupe-platform-edges-
// ux.md §3.1), so this column is the shred ledger's summary, not a control
// surface — every in-flight identity links into the Graph explorer.
function renderVaultInfo(col) {
  col.appendChild(el("p", "muted small",
    "Vault custody is not operator-mutable — the shred surface is per-identity, in the Graph explorer."));
  col.appendChild(el("h4", "comp-subsection", "Shred status"));
  const box = el("div");
  col.appendChild(box);
  loadVaultShreds(box);
}

// loadVaultShreds fetches the privacy-shreds bucket rows and renders the
// fleet summary plus every still-finalizing identity.
async function loadVaultShreds(box) {
  box.appendChild(el("div", "muted small", "loading…"));
  const body = await api("/api/vault/shreds");
  box.innerHTML = "";
  if (body.error) { box.appendChild(el("div", "error-text small", body.error)); return; }
  const rows = body.shreds || [];
  box.appendChild(el("div", "comp-metrics", shredFleetSummary(rows)));
  rows.filter(shredInFlight).forEach((r) => {
    const line = el("div", "control-item");
    line.appendChild(keyLinkEl(r.identityKey, "cid"));
    line.appendChild(el("span", "muted small", shredFinalizationLine(r)));
    box.appendChild(line);
  });
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
    if (page.component === "vault") renderVaultInfo(col);
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
  if (surface === "gateway") {
    renderRevokeSurface(col, rowsBox, page, out);
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

// renderRevokeSurface fills the Gateway page's right column: the
// token-revocation kill-switch. Revoking submits the RevokeActor op via the
// standard op path (P2 — Loupe never writes the bucket); the Gateway's own
// materializer folds the resulting event into the token-revocation set, so
// the list refreshes on a short delay to ride out that hop.
function renderRevokeSurface(col, rowsBox, page, out) {
  const inst = liveInstance(page);
  // No live instance ≠ an old build: the op still commits durably and the
  // materializer folds it when a Gateway starts — say exactly that.
  const status = inst ? revocationStatus(inst.doc) : {
    line: "no live Gateway instance — a revocation commits durably and takes effect when a Gateway starts",
    cls: "warn",
  };
  col.insertBefore(el("p", (statusTextClass[status.cls] || "muted") + " small", status.line), rowsBox);

  const listBox = el("div");
  const refresh = () => loadRevocations(listBox, out, refreshLater);
  // The revoke path is async (op → event → materializer → bucket): one quick
  // refresh catches the common case, a second catches a slow fold. Timers
  // register on the view state so navigation/re-render cancels them.
  const refreshLater = () => {
    clearRevokeTimers();
    state.revokeTimers = [setTimeout(refresh, 700), setTimeout(refresh, 2500)];
  };

  rowsBox.appendChild(el("h4", "comp-subsection", "Revoke an actor"));
  const form = el("div", "control-item");
  const input = el("input");
  input.type = "text";
  input.placeholder = "vtx.identity.<id>";
  const reason = el("input");
  reason.type = "text";
  reason.placeholder = "reason (optional)";
  const btn = el("button", "danger-btn", "Revoke…");
  btn.disabled = true;
  input.addEventListener("input", () => { btn.disabled = !revokeActorValid(input.value); });
  btn.addEventListener("click", () => {
    openRevokeModal(input.value.trim(), reason.value.trim(), out, () => {
      input.value = "";
      reason.value = "";
      btn.disabled = true;
      refreshLater();
    });
  });
  form.appendChild(input);
  form.appendChild(reason);
  form.appendChild(btn);
  rowsBox.appendChild(form);
  rowsBox.appendChild(el("p", "muted small",
    "Revoked actors are refused at the Gateway (403) before any op is published. " +
    "Mint a test token (bin/gateway dev-token -sub <id>), revoke it here, and the next " +
    "POST /v1/operations returns 403."));

  rowsBox.appendChild(el("h4", "comp-subsection", "Currently revoked"));
  rowsBox.appendChild(listBox);
  refresh();
}

// loadRevocations fetches + renders the revoked-actor list: each actor key
// links into the Graph explorer, the audit fields (by/at/reason) ride along,
// and every row carries an Un-revoke (armed on first click — reversal is not
// destructive enough for a typed confirm).
async function loadRevocations(listBox, out, refreshLater) {
  // Stamp a sequence on the box so a slow response can never overwrite a
  // newer one (the 700ms/2500ms refreshes may resolve out of order), and a
  // response for a detached box (the page re-rendered mid-flight) is dropped.
  const seq = (listBox.revocationsSeq || 0) + 1;
  listBox.revocationsSeq = seq;
  const body = await api("/api/gateway/revocations");
  if (!listBox.isConnected || listBox.revocationsSeq !== seq) return;
  listBox.innerHTML = "";
  if (body.error) { listBox.appendChild(el("div", "error-text small", body.error)); return; }
  const rows = body.revocations || [];
  if (!rows.length) { listBox.appendChild(el("div", "muted small", "(no revoked actors)")); return; }
  rows.forEach((row) => {
    const line = el("div", "control-item");
    line.appendChild(keyLinkEl(row.actor, "cid"));
    const audit = [];
    if (row.revokedAt) audit.push(row.revokedAt);
    if (row.by) audit.push("by " + row.by);
    if (row.reason) audit.push(row.reason);
    if (audit.length) line.appendChild(el("span", "muted small", audit.join(" · ")));
    const un = el("button", "comp-ctlbtn", "un-revoke");
    un.addEventListener("click", async () => {
      if (un.dataset.armed !== "1") {
        un.dataset.armed = "1";
        un.textContent = "un-revoke — sure?";
        setTimeout(() => { un.dataset.armed = ""; un.textContent = "un-revoke"; }, 4000);
        return;
      }
      un.disabled = true;
      const reply = await submitRevocationOp("UnrevokeActor", { actor: row.actor }, out);
      un.disabled = false;
      if (!reply.error && reply.status !== "rejected") refreshLater();
    });
    line.appendChild(un);
    listBox.appendChild(line);
  });
}

// submitRevocationOp POSTs the event-only kill-switch op through the standard
// op path and renders the reply into the column's persistent reply box.
async function submitRevocationOp(operationType, payload, out) {
  showReply(out, el("div", "muted small", operationType + " " + payload.actor + " …"));
  const body = await api("/api/op", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ operationType, payload }),
  });
  const reply = el("div");
  reply.appendChild(el("div", "muted small", operationType + " " + payload.actor + ":"));
  reply.appendChild(renderDoc(body));
  showReply(out, reply);
  return body;
}

// openRevokeModal is the typed confirm on the kill-switch: the destructive
// button stays disabled until the input exactly matches the actor key. On a
// committed op the modal closes and onDone schedules the list refreshes.
function openRevokeModal(actor, reasonText, out, onDone) {
  closeModal(); // never stack two confirms
  let inFlight = false;
  const overlay = el("div", "modal-overlay");
  const modal = el("div", "modal");
  modal.appendChild(el("h3", null, "Revoke actor"));
  modal.appendChild(el("p", "muted",
    "This revokes the actor at the Gateway — every future request bearing its token is refused with 403 " +
    "until un-revoked. Type the identity key to confirm:"));
  modal.appendChild(el("div", "cid", actor));
  const input = el("input");
  input.type = "text";
  input.placeholder = actor;
  modal.appendChild(input);
  const actions = el("div", "modal-actions");
  const cancel = el("button", null, "Cancel");
  const confirm = el("button", "danger-btn", "Revoke");
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
    confirm.disabled = !revokeConfirmReady(input.value, actor);
  });
  confirm.addEventListener("click", async () => {
    inFlight = true;
    confirm.disabled = true;
    cancel.disabled = true;
    input.disabled = true;
    msg.className = "muted small";
    msg.textContent = "revoking…";
    const payload = { actor };
    if (reasonText) payload.reason = reasonText;
    const body = await submitRevocationOp("RevokeActor", payload, out);
    inFlight = false;
    if (body.error || body.status === "rejected") {
      // A transport failure carries a string error; a Processor rejection
      // carries the structured ReplyError object.
      const detail = typeof body.error === "string" ? body.error
        : (body.error && body.error.message) || "rejected";
      msg.className = "error-text small";
      msg.textContent = "revoke failed: " + detail;
      cancel.disabled = false;
      input.disabled = false;
      // The typed confirmation still matches — a transient failure must not
      // force the operator to retype the key to retry.
      confirm.disabled = !revokeConfirmReady(input.value, actor);
      return;
    }
    close();
    toast("revoked " + actor);
    if (onDone) onDone();
  });
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

export { init, enter, leave };
