// Tasks view: the task inbox. Each row is link-sourced server-side
// (GET /api/tasks): assignee / operation / target from the task's links, and
// the operation's human label (name + description) resolved from its
// forOperation meta-vertex. Every entity on a card is a keyLink into the
// Graph explorer (design §1.2 — no dead ends).

import { $, el, api, setStatus } from "../api.js";
import { navigate } from "../router.js";
import { keyLinkEl } from "../render.js";
import { prefillOp } from "./op.js";

const state = { loaded: false };

function enter() {
  if (state.loaded) return;
  state.loaded = true;
  loadTasks();
}

async function loadTasks() {
  setStatus("tasks-status-msg", "loading…");
  const status = $("#tasks-status").value;
  const body = await api("/api/tasks" + (status ? "?status=" + encodeURIComponent(status) : ""));
  const cards = $("#tasks-cards");
  cards.innerHTML = "";
  if (body.error) {
    setStatus("tasks-status-msg", body.error, true);
    return;
  }
  const tasks = body.tasks || [];
  setStatus("tasks-status-msg", tasks.length + " task" + (tasks.length === 1 ? "" : "s"));
  if (!tasks.length) {
    cards.appendChild(el("div", "muted", "(no tasks)"));
    return;
  }
  tasks.forEach((t) => {
    const op = t.operation || {};
    const card = el("div", "card task-card " + (t.status === "open" ? "green" : ""));
    const title = el("div", "card-key");
    if (op.name) title.appendChild(el("span", null, op.name));
    else if (op.key) title.appendChild(keyLinkEl(op.key));
    else title.appendChild(el("span", null, "task"));
    title.appendChild(el("span", "card-group", t.status));
    card.appendChild(title);
    if (op.description) card.appendChild(el("div", "card-sub", op.description));
    if (t.key) {
      const keyLine = el("div", "card-sub");
      keyLine.appendChild(keyLinkEl(t.key));
      card.appendChild(keyLine);
    }
    const meta = el("div", "card-meta");
    if (t.assignee) {
      const a = el("span", null, "assignee ");
      a.appendChild(keyLinkEl(t.assignee));
      meta.appendChild(a);
    }
    if (t.expiresAt) meta.appendChild(el("span", null, "expires " + t.expiresAt));
    card.appendChild(meta);
    if (t.scopedTo) {
      const sc = el("div", "task-scoped small muted", "scoped to ");
      sc.appendChild(keyLinkEl(t.scopedTo));
      card.appendChild(sc);
    }
    if (t.status === "open" && op.name) {
      const btn = el("button", "task-complete", "Complete in Submit Op →");
      btn.addEventListener("click", () => startTaskOp(op.name));
      card.appendChild(btn);
    }
    cards.appendChild(card);
  });
}

// startTaskOp jumps to Submit Op with the task's operation pre-selected (or
// the operationType override filled when it is not a catalog command), so the
// assignee completes the task through the existing op form.
function startTaskOp(opName) {
  navigate("#/op");
  prefillOp(opName);
}

function init() {
  $("#tasks-load").addEventListener("click", loadTasks);
  $("#tasks-status").addEventListener("change", loadTasks);
}

export { init, enter };
