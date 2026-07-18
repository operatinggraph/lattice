// The AI review console (#/review, loupe-f16-ai-review-console-ux.md §3):
// F16.1 ships the capability-proposal queue + detail card and the safe half
// of the action loop (reject) — approve + apply are F16.2. #/review and
// #/review/capability both land on the queue; #/review/capability/<id> drills
// into one proposal. An #/review/augur route is a dead end until F16.3.

import { $, el, api, setStatus, toast } from "../api.js";
import { replaceRoute } from "../router.js";
import { renderDoc, keyLinkEl } from "../render.js";
import {
  kindGlyph, reviewStateClass, confidenceBand, agoFrom, proposalRows, proposalDisplayState,
} from "../logic/review.js";

const state = { arg: null };

function enter(route) {
  const parts = (route.arg || "").split("/").filter(Boolean);
  const tab = parts[0] || "capability";
  if (tab !== "capability") {
    // Only the capability tab exists until F16.3 ships Augur.
    replaceRoute("/review/capability");
    return;
  }
  state.arg = route.arg;
  const id = parts[1] || null;
  toggleViews(!!id);
  if (id) loadDetail(id);
  else loadQueue();
}

function toggleViews(showDetail) {
  $("#review-queue-view").style.display = showDetail ? "none" : "";
  $("#review-detail-view").style.display = showDetail ? "" : "none";
}

// --- Queue -----------------------------------------------------------------

async function loadQueue() {
  const cards = $("#review-cards");
  setStatus("review-status", "loading…");
  const body = await api("/api/review/capability");
  if (state.arg !== "capability" && state.arg !== "") return; // navigated away
  cards.innerHTML = "";
  if (body.error) {
    setStatus("review-status", body.error, true);
    return;
  }
  const rows = proposalRows(body.proposals || []);
  setStatus("review-status", rows.length + " proposal(s)");
  if (!rows.length) {
    cards.appendChild(el("div", "muted",
      "No capability proposals yet. When an AI authors a new lens, grant, or op, it lands here for your review."));
    return;
  }
  rows.forEach((row) => cards.appendChild(queueCard(row)));
}

// cardBorderClass reuses the existing .card left-border color vocabulary
// (green/yellow/red, from the Health-card family) rather than inventing a
// parallel one — the state chip inside the card is the precise signal.
const cardBorderClass = { pending: "yellow", approved: "green", applied: "green", invalid: "red" };

function queueCard(row) {
  const displayState = row.displayState;
  const card = el("a", "card review-card " + (cardBorderClass[displayState] || ""));
  card.href = "#/review/capability/" + encodeURIComponent(row.proposalId);
  card.appendChild(el("div", "card-key", row.intent || "(no intent recorded)"));
  const meta = el("div", "review-card-meta");
  meta.appendChild(el("span", reviewStateClass(displayState), displayState));
  if (row.kind) {
    meta.appendChild(el("span", "review-glyph", (kindGlyph[row.kind] || "") + " " + row.kind));
  }
  if (row.targetPackageName) {
    meta.appendChild(el("span", null, row.targetMode + " " + row.targetPackageName +
      (row.targetNewVersion ? "@" + row.targetNewVersion : "")));
  }
  if (typeof row.confidence === "number") {
    const band = confidenceBand(row.confidence);
    meta.appendChild(el("span", "confidence-band " + band, "conf " + row.confidence.toFixed(2)));
  }
  if (row.model) meta.appendChild(el("span", null, row.model));
  const ago = agoFrom(row.reasonedAt, Date.now());
  if (ago) meta.appendChild(el("span", null, ago));
  card.appendChild(meta);
  if (row.requesterId) {
    const req = el("div", "muted small");
    req.appendChild(document.createTextNode("requested by "));
    req.appendChild(keyLinkEl(row.requesterId));
    card.appendChild(req);
  }
  return card;
}

// --- Detail ------------------------------------------------------------

async function loadDetail(id) {
  const body = $("#review-detail-body");
  body.innerHTML = "";
  body.appendChild(el("div", "muted small", "loading…"));
  setStatus("review-detail-status", "loading…");
  const proposal = await api("/api/review/capability/" + encodeURIComponent(id));
  if (state.arg !== "capability/" + id) return; // navigated away while loading
  body.innerHTML = "";
  if (proposal.error) {
    setStatus("review-detail-status", proposal.error, true);
    const card = el("div", "notfound-card");
    card.appendChild(el("div", "notfound-key", id));
    card.appendChild(el("div", "muted", proposal.error));
    const back = el("a", "key-link", "← back to the queue");
    back.href = "#/review/capability";
    card.appendChild(back);
    body.appendChild(card);
    return;
  }
  setStatus("review-detail-status", "");
  body.appendChild(headSection(proposal));
  body.appendChild(rationaleSection(proposal));
  body.appendChild(artifactSection(proposal));
  body.appendChild(deltaSection(proposal));
  body.appendChild(provenanceSection(proposal));
  body.appendChild(actionSection(proposal));
}

function panel(title) {
  const box = el("section", "lens-panel");
  if (title) box.appendChild(el("h3", "comp-section", title));
  return box;
}

function headSection(p) {
  const box = panel(null);
  box.appendChild(el("h2", "comp-title", p.intent || "(no intent recorded)"));
  const displayState = proposalDisplayState(p);
  box.appendChild(el("span", reviewStateClass(displayState), displayState));
  if (displayState === "invalid" && p.reviewInvalidReason) {
    box.appendChild(el("div", "review-invalid-reason", p.reviewInvalidReason));
  }
  const timeline = el("div", "muted small");
  const bits = [];
  if (p.reasonedAt) bits.push("reasoned " + agoFrom(p.reasonedAt, Date.now()) + " (" + p.reasonedAt + ")");
  if (p.reviewedAt) bits.push("reviewed " + agoFrom(p.reviewedAt, Date.now()) + " (" + p.reviewedAt + ")");
  timeline.textContent = bits.join(" · ");
  box.appendChild(timeline);
  if (p.requesterId) {
    const req = el("div", "muted small");
    req.appendChild(document.createTextNode("requested by "));
    req.appendChild(keyLinkEl(p.requesterId));
    box.appendChild(req);
  }
  return box;
}

function rationaleSection(p) {
  const box = panel("Rationale");
  box.appendChild(el("p", null, p.rationale || "(no rationale recorded — reasoning may still be in flight)"));
  return box;
}

function artifactSection(p) {
  const box = panel("The artifact");
  if (!p.kind) {
    box.appendChild(el("div", "muted", "reasoning still in flight — no artifact recorded yet"));
    return box;
  }
  const meta = el("div", "review-card-meta");
  meta.appendChild(el("span", "review-glyph", (kindGlyph[p.kind] || "") + " " + p.kind));
  meta.appendChild(el("span", null, (p.targetMode || "?") +
    (p.targetPackageName ? " " + p.targetPackageName : "") +
    (p.targetNewVersion ? "@" + p.targetNewVersion : "")));
  box.appendChild(meta);
  box.appendChild(prettyContent(p.content));
  return box;
}

// prettyContent renders the artifact's content field (a JSON string per the
// DDL) as a formatted doc when it parses, else the raw text verbatim — an
// AI-authored artifact isn't guaranteed well-formed JSON at record time.
function prettyContent(content) {
  if (!content) return el("div", "muted", "(empty)");
  try {
    return renderDoc(JSON.parse(content));
  } catch (_) {
    return el("pre", "vtx-doc doc", content);
  }
}

function deltaSection(p) {
  const box = panel("The delta — author-time preview");
  const meta = el("div", "review-card-meta");
  const validClass = p.validationState === "valid" ? "state-tag review-valid-ok" : "state-tag";
  meta.appendChild(el("span", validClass, p.validationState || "unrecorded"));
  if (p.validationCheckedAt) {
    meta.appendChild(el("span", "muted small", "checked " + agoFrom(p.validationCheckedAt, Date.now())));
  }
  box.appendChild(meta);
  if (p.validationReport) box.appendChild(el("div", "muted small", p.validationReport));
  box.appendChild(p.validationDeltaPreview !== undefined && p.validationDeltaPreview !== null && p.validationDeltaPreview !== ""
    ? renderDoc(p.validationDeltaPreview)
    : el("div", "muted small", "(no delta preview recorded)"));
  box.appendChild(el("div", "muted small",
    "this preview was computed at author time — approving re-validates against the live catalog (F16.2)."));
  return box;
}

function provenanceSection(p) {
  const details = el("details");
  details.appendChild(el("summary", "muted small", "provenance"));
  const inner = panel(null);
  inner.appendChild(el("div", "muted small", "model: " + (p.model || "?")));
  inner.appendChild(el("div", "muted small", "promptHash: " + (p.promptHash || "?")));
  inner.appendChild(el("div", "muted small", "catalogHash: " + (p.catalogHash || "?")));
  inner.appendChild(el("div", "muted small", "reasonedAt: " + (p.reasonedAt || "?")));
  details.appendChild(inner);
  return details;
}

function actionSection(p) {
  const box = panel("Verdict");
  const displayState = proposalDisplayState(p);
  if (displayState !== "pending") {
    box.appendChild(el("div", "muted", outcomeLine(p, displayState)));
    return box;
  }
  const row = el("div", "lens-ctlrow");

  const approve = el("button", null, "Approve & install…");
  approve.disabled = true;
  approve.title = "approve + apply ships in F16.2 (re-validation against the live catalog)";
  row.appendChild(approve);

  const reject = el("button", "danger-btn", "Reject");
  reject.addEventListener("click", async () => {
    if (!window.confirm(
      "Reject this proposal? The AI's authored artifact stays recorded for audit; it just won't be installed.")) return;
    row.querySelectorAll("button").forEach((b) => { b.disabled = true; });
    setStatus("review-detail-status", "submitting reject…");
    const proposalKey = "vtx.capabilityproposal." + p.proposalId;
    const body = await api("/api/op", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        operationType: "ReviewCapabilityProposal",
        payload: { proposalId: p.proposalId, verdict: "reject" },
        reads: [proposalKey + ".review"],
      }),
    });
    if (state.arg !== "capability/" + p.proposalId) return; // navigated away
    if (body.error) {
      setStatus("review-detail-status", "reject failed: " + body.error, true);
      row.querySelectorAll("button").forEach((b) => { if (b !== approve) b.disabled = false; });
      return;
    }
    toast("proposal rejected");
    loadDetail(p.proposalId);
  });
  row.appendChild(reject);
  box.appendChild(row);
  return box;
}

function outcomeLine(p, displayState) {
  if (displayState === "applied") {
    return "approved & applied by " + (p.appliedByOp || "?") + " at " + (p.appliedAt || "?");
  }
  if (displayState === "approved") {
    return "approved at " + (p.reviewedAt || "?") + " — not yet applied";
  }
  if (displayState === "rejected") {
    return "rejected at " + (p.reviewedAt || "?");
  }
  if (displayState === "invalid") {
    return "invalid — " + (p.reviewInvalidReason || "no reason recorded");
  }
  return "reasoning still in flight";
}

function init() {
  const back = $("#review-load");
  if (back) back.addEventListener("click", loadQueue);
}

function leave() {}

export { init, enter, leave };
