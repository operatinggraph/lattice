// Flows view: the Chronicler's durable Loom-flow history (the
// orchestration-history-read-model-design.md §2.7 Loupe surface). Each card
// is one instance's lifecycle: pattern, timestamps, and (for a still-running
// row) a live/orphaned badge cross-referenced against the live Loom control
// read server-side. Read-only — no control-plane op from here.

import { $, el, api, setStatus } from "../api.js";
import { keyLinkEl } from "../render.js";

const state = { loaded: false };

function enter() {
  if (state.loaded) return;
  state.loaded = true;
  loadFlows();
}

const STATUS_CLASS = { complete: "green", failed: "red", running: "yellow" };

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
  const flows = body.flows || [];
  setStatus("flows-status-msg", flows.length + " flow" + (flows.length === 1 ? "" : "s"));
  if (!flows.length) {
    cards.appendChild(el("div", "muted", "(no flows)"));
    return;
  }
  flows.forEach((f) => {
    const card = el("div", "card flow-card " + (STATUS_CLASS[f.status] || ""));
    const title = el("div", "card-key");
    title.appendChild(el("span", null, f.patternRef || f.instanceId));
    title.appendChild(el("span", "card-group", f.status));
    // f.live is a tri-state: true (live), false (confirmed orphaned), or
    // absent (the server's live control read failed — must not render as a
    // false "orphaned", so it's simply omitted).
    if (f.status === "running" && typeof f.live === "boolean") {
      title.appendChild(el("span", "card-group " + (f.live ? "green" : "red"), f.live ? "live" : "orphaned"));
    }
    card.appendChild(title);
    const idLine = el("div", "card-sub", f.instanceId);
    card.appendChild(idLine);
    if (f.subjectKey) {
      const sub = el("div", "card-sub");
      sub.appendChild(document.createTextNode("subject "));
      sub.appendChild(keyLinkEl(f.subjectKey));
      card.appendChild(sub);
    }
    const meta = el("div", "card-meta");
    if (f.startedAt) meta.appendChild(el("span", null, "started " + f.startedAt));
    if (f.endedAt) meta.appendChild(el("span", null, "ended " + f.endedAt));
    card.appendChild(meta);
    if (f.failureReason) card.appendChild(el("div", "card-issue bad", f.failureReason));
    cards.appendChild(card);
  });
}

function init() {
  $("#flows-load").addEventListener("click", loadFlows);
  $("#flows-status").addEventListener("change", loadFlows);
}

export { init, enter };
