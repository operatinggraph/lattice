// Shell-level live status: the topbar rollup pill + the global alert strip,
// present on every view. One relaxed 30s /api/health poll drives both — the
// strip must work wherever the operator is, so it never depends on the map's
// own refresh clock.

import { $, el, api } from "./api.js";
import { shapeAlertLines } from "./logic/status.js";
import { pendingCount } from "./logic/review.js";

const POLL_MS = 30000;
const shell = { timer: null, expanded: false, fetchSeq: 0 };

// refreshReviewBadge drives the top-nav "Review" count badge (§2.2 — the
// number of proposals in review.state=pending, i.e. awaiting a human
// verdict). Best-effort: a fetch error just hides the badge rather than
// erroring the whole shell tick. Augur's count joins this in F16.3.
async function refreshReviewBadge() {
  const badge = $("#review-badge");
  if (!badge) return;
  const body = await api("/api/review/capability");
  const n = body.error ? 0 : pendingCount(body.proposals || []);
  if (n > 0) {
    badge.textContent = String(n);
    badge.classList.add("visible");
  } else {
    badge.textContent = "";
    badge.classList.remove("visible");
  }
}

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
  refreshReviewBadge();
  shell.timer = setInterval(() => {
    if (document.hidden) return;
    refreshShellHealth();
    refreshReviewBadge();
  }, POLL_MS);
  document.addEventListener("visibilitychange", () => {
    if (document.hidden) return;
    refreshShellHealth();
    refreshReviewBadge();
  });

  const logout = $("#topbar-logout");
  if (logout) {
    logout.addEventListener("click", async () => {
      await api("/api/operator/logout", { method: "POST" });
      location.href = "/login";
    });
  }
}

export { init };
