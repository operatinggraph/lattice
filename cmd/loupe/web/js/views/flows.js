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
import { flowRows, groupFlowsByPattern, groupSummary, flowsHeadline } from "../logic/flows.js";

const state = { loaded: false };

function enter() {
  if (state.loaded) return;
  state.loaded = true;
  loadFlows();
}

async function loadFlows() {
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
  const card = el("div", "card flow-card " + f.cls);
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

function init() {
  $("#flows-load").addEventListener("click", loadFlows);
  $("#flows-status").addEventListener("change", loadFlows);
}

export { init, enter };
