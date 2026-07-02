// Shell-level live status: the topbar rollup pill + the global alert strip,
// present on every view. One relaxed 30s /api/health poll drives both — the
// strip must work wherever the operator is, so it never depends on the map's
// own refresh clock.

import { $, el, api } from "./api.js";
import { shapeAlertLines } from "./logic/status.js";

const POLL_MS = 30000;
const shell = { timer: null, expanded: false, fetchSeq: 0 };

async function refreshShellHealth() {
  const seq = ++shell.fetchSeq;
  const body = await api("/api/health");
  if (seq !== shell.fetchSeq) return; // a newer refresh superseded this one
  const pill = $("#topbar-pill");
  if (pill) {
    if (body.error) {
      pill.textContent = "?";
      pill.className = "rollup";
      pill.title = body.error;
    } else {
      pill.textContent = body.overall || "";
      pill.className = "rollup " + (body.overall || "");
      pill.title = "platform rollup — open the map";
    }
  }
  renderStrip(body);
}

// renderStrip shows the worst line plus a "＋N more" expander; it is not
// dismissible — it reflects live state and disappears when the alert keys do.
function renderStrip(body) {
  const strip = $("#alertstrip");
  if (!strip) return;
  strip.innerHTML = "";
  const lines = body.error ? [] : shapeAlertLines(body);
  if (!lines.length) {
    strip.classList.remove("visible");
    shell.expanded = false;
    return;
  }
  strip.classList.add("visible");
  (shell.expanded ? lines : lines.slice(0, 1)).forEach((l) => strip.appendChild(el("div", l.cls, l.text)));
  if (lines.length > 1) {
    const more = el("button", "alertstrip-more",
      shell.expanded ? "collapse" : "＋" + (lines.length - 1) + " more");
    more.addEventListener("click", () => { shell.expanded = !shell.expanded; renderStrip(body); });
    strip.appendChild(more);
  }
}

function init() {
  refreshShellHealth();
  shell.timer = setInterval(() => { if (!document.hidden) refreshShellHealth(); }, POLL_MS);
  document.addEventListener("visibilitychange", () => { if (!document.hidden) refreshShellHealth(); });
}

export { init };
