// Flows view: the Chronicler's durable Loom-flow history (the
// orchestration-history-read-model-design.md §2.7 Loupe surface), grouped by
// pattern and ordered exception-first. Each card is one instance's lifecycle;
// a still-running row also carries the verdict of a cross-reference against
// Loom's own instance state.
//
// All decision logic lives in logic/flows.js (goja-tested); this file binds
// DOM. Read-only — no control-plane op from here (Loom exposes no per-instance
// redrive; see loupe-flows-edge-depth-ux.md §2.2).

import { $, el, api, setStatus } from "../api.js";
import { keyLinkEl } from "../render.js";
import { navigate } from "../router.js";
import {
  flowRows, groupFlowsByPattern, groupSummary, flowsHeadline,
  stepRows, stepSummary, engineDisagreement, flowLabel, flowKind, patternLabel,
} from "../logic/flows.js";

const state = { loaded: false, arg: "" };

// enter routes between the list (#/flows) and one flow's detail
// (#/flows/<instanceId>), the same one-panel/two-modes shape the Review tab
// uses — a drill-in is a route, so it is linkable and Back works.
function enter(route) {
  const arg = (route && route.arg) || "";
  state.arg = arg;
  if (arg) {
    loadDetail(arg);
    return;
  }
  $("#flow-detail").classList.remove("visible");
  $("#flows-list").classList.add("visible");
  if (state.loaded) return;
  state.loaded = true;
  loadFlows();
}

async function loadFlows() {
  $("#flow-detail").classList.remove("visible");
  $("#flows-list").classList.add("visible");
  setStatus("flows-status-msg", "loading…");
  const status = $("#flows-status").value;
  const body = await api("/api/flows" + (status ? "?status=" + encodeURIComponent(status) : ""));
  const cards = $("#flows-cards");
  cards.innerHTML = "";
  if (body.error) {
    setStatus("flows-status-msg", body.error, true);
    return;
  }
  const rows = flowRows(body.flows);
  setStatus("flows-status-msg", flowsHeadline(rows));
  if (!rows.length) {
    cards.appendChild(el("div", "muted", "(no flows)"));
    return;
  }
  groupFlowsByPattern(rows).forEach((g) => cards.appendChild(patternGroup(g)));
}

// patternGroup renders one pattern's flows under a header carrying the
// pattern's own name and its worst-first count line — the grouping is what
// turns a wall of identically-titled cards into a short list of patterns, so
// the header has to be the thing that reads first.
function patternGroup(g) {
  const box = el("div", "flow-group");
  const head = el("div", "flow-group-head");
  head.appendChild(el("span", "flow-group-name", g.pattern || "(unnamed pattern)"));
  if (g.patternRef) head.appendChild(keyLinkEl(g.patternRef));
  head.appendChild(el("span", "muted small", groupSummary(g)));
  box.appendChild(head);
  const cards = el("div", "cards");
  g.rows.forEach((f) => cards.appendChild(flowCard(f)));
  box.appendChild(cards);
  return box;
}

function flowCard(f) {
  const card = el("div", "card flow-card drillable " + f.cls);
  card.addEventListener("click", () => navigate("#/flows/" + encodeURIComponent(f.instanceId)));
  // The id and the badges are separate rows: an instance id is a 20-char
  // NanoID with no break points, so sharing a line with two badges wraps it
  // mid-token and neither reads.
  card.appendChild(el("div", "card-key flow-id", f.instanceId));
  const badges = el("div", "flow-badges");
  badges.appendChild(el("span", "card-group", f.status));
  // A running row also carries the cross-reference verdict. It is omitted
  // entirely when the control read failed — an unknown must never render as a
  // confirmed one.
  if (f.liveness) {
    badges.appendChild(el("span", "card-group " + (f.liveness === "live" ? "green" : "red"), f.label));
  }
  card.appendChild(badges);
  if (f.subjectKey) {
    const sub = el("div", "card-sub");
    sub.appendChild(document.createTextNode("subject "));
    sub.appendChild(keyLinkEl(f.subjectKey));
    card.appendChild(sub);
  }
  const meta = el("div", "card-meta");
  if (f.startedAt) meta.appendChild(el("span", null, "started " + f.startedAt));
  // A stale-history row's endedAt belongs to a PREVIOUS run of the same
  // instance id and can precede its own start, so it is suppressed rather
  // than rendered as this flow's end (the carry-forward defect, design §1.2).
  if (f.endedAt && f.kind !== "stale-history") meta.appendChild(el("span", null, "ended " + f.endedAt));
  card.appendChild(meta);
  if (f.kind === "stale-history") {
    card.appendChild(el("div", "card-issue",
      "Loom reports this instance " + (f.engineStatus || "finished") +
      "; the history row still reads running and its end timestamp is a previous run's."));
  }
  if (f.failureReason) card.appendChild(el("div", "card-issue bad", f.failureReason));
  return card;
}

// loadDetail renders one flow from the three sources the endpoint keeps
// separate: the history row, the engine's own view, and the pinned pattern.
async function loadDetail(id) {
  $("#flows-list").classList.remove("visible");
  const panel = $("#flow-detail");
  panel.classList.add("visible");
  panel.innerHTML = "";
  setStatus("flows-status-msg", "loading flow…");
  const body = await api("/api/flows/" + encodeURIComponent(id));
  if (state.arg !== id) return; // navigated away
  if (body.error) {
    setStatus("flows-status-msg", body.error, true);
    panel.appendChild(el("div", "muted", body.error));
    return;
  }
  const row = body.row || {};
  setStatus("flows-status-msg", patternLabel(row) + " · " + row.instanceId);

  const head = el("div", "flow-detail-head");
  head.appendChild(el("span", "flow-group-name", patternLabel(row)));
  if (row.patternRef) head.appendChild(keyLinkEl(row.patternRef));
  head.appendChild(el("span", "card-group", row.status || "?"));
  if (row.liveness) {
    head.appendChild(el("span", "card-group " + (row.liveness === "live" ? "green" : "red"), flowLabel(row)));
  }
  panel.appendChild(head);

  const disagreement = engineDisagreement(row);
  if (disagreement) panel.appendChild(el("div", "card-issue", disagreement));
  if (body.engineError) {
    panel.appendChild(el("div", "card-issue",
      "Loom's own view is unavailable (" + body.engineError + "), so only the durable history is shown."));
  }
  if (row.failureReason) panel.appendChild(el("div", "card-issue bad", row.failureReason));

  panel.appendChild(factRow("instance", row.instanceId));
  if (row.subjectKey) panel.appendChild(factRow("subject", keyLinkEl(row.subjectKey)));
  const inst = (body.engine && body.engine.instance && body.engine.instance.instance) || {};
  if (body.pattern && body.pattern.subjectType) panel.appendChild(factRow("subject type", body.pattern.subjectType));
  if (typeof inst.cursor === "number") panel.appendChild(factRow("cursor", String(inst.cursor)));
  if (typeof inst.retryCount === "number") panel.appendChild(factRow("retries", String(inst.retryCount)));
  if (row.startedAt) panel.appendChild(factRow("started", row.startedAt));
  // Suppressed on a stale row for the same reason the card suppresses it: the
  // timestamp belongs to a previous run of this instance id.
  if (row.endedAt && flowKind(row) !== "stale-history") panel.appendChild(factRow("ended", row.endedAt));

  panel.appendChild(stepsPanel(body, row, inst));
}

function factRow(k, v) {
  const r = el("div", "lens-kvrow");
  r.appendChild(el("span", "lens-k", k));
  if (typeof v === "string") r.appendChild(el("span", null, v));
  else r.appendChild(v);
  return r;
}

// stepsPanel is the half `inspect` cannot give on its own: the control plane
// resolves only the CURRENT step, so the sequence comes from the pinned
// pattern and the cursor marks the instance's position in it.
function stepsPanel(body, row, inst) {
  const box = el("div", "flow-steps");
  const steps = (body.pattern && body.pattern.steps) || [];
  const cursor = typeof inst.cursor === "number" ? inst.cursor : -1;
  box.appendChild(el("div", "comp-section-head", "Steps"));
  if (!body.pattern) {
    box.appendChild(el("div", "muted small",
      "The pattern definition could not be read, so the step sequence is unavailable."));
    return box;
  }
  box.appendChild(el("div", "muted small", stepSummary(steps, cursor, row.status)));
  stepRows(steps, cursor, row.status).forEach((s) => {
    const line = el("div", "flow-step " + s.state);
    line.appendChild(el("span", "flow-step-n", String(s.index + 1)));
    line.appendChild(el("span", "flow-step-state", s.state));
    line.appendChild(el("span", "flow-step-label", s.label));
    if (s.kind !== "systemOp") line.appendChild(el("span", "card-group", s.kind));
    if (s.guarded) line.appendChild(el("span", "card-group", "guarded"));
    if (s.replyOp) line.appendChild(el("span", "muted small", "replies " + s.replyOp));
    box.appendChild(line);
  });
  return box;
}

function init() {
  $("#flows-load").addEventListener("click", loadFlows);
  $("#flows-status").addEventListener("change", loadFlows);
}

export { init, enter };
