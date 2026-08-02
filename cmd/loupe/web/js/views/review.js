// The AI review console (#/review, loupe-f16-ai-review-console-ux.md §0):
// two tabs, one shared card component. #/review and #/review/capability both
// land on the capability queue; #/review/augur lands on the Augur queue;
// #/review/<tab>/<id> drills into one proposal. The capability tab's approve
// re-validates the artifact against the live catalog server-side (a dedicated
// /approve endpoint) and installs it in a second step (/apply); its reject is
// a plain /api/op submit. The Augur tab's approve/reject are both plain
// /api/op submits — Augur re-validates entirely server-side, so it carries no
// client-computed validation payload and has no separate apply step.

import { $, el, demoHide, api, setStatus, toast } from "../api.js";
import { replaceRoute } from "../router.js";
import { renderDoc, keyLinkEl } from "../render.js";
import {
  kindGlyph, reviewStateClass, confidenceBand, hasConfidenceScore, agoFrom,
  proposalRows, proposalDisplayState, augurProposalRows, augurDisplayState,
  applyOutcome, opRejected, errorText,
} from "../logic/review.js";

const TABS = ["capability", "augur"];
const QUEUE_BLURB = {
  capability: "Capability-authoring proposals — an AI reasoned out a new DDL artifact " +
    "(lens, grant, weaverTarget, loomPattern, vertexTypeDDL, or opMeta) and parked it for a human " +
    "verdict. Nothing installs until you approve it here.",
  augur: "Augur escalations — the platform hit an orchestration gap it had no playbook for (or " +
    "exhausted one) and an AI proposed a remediation. Nothing dispatches until you approve it here.",
};

const state = { arg: null };

function enter(route) {
  const raw = route.arg || "";
  const parts = raw.split("/").filter(Boolean);
  const tab = parts[0] || "capability";
  if (TABS.indexOf(tab) === -1) {
    replaceRoute("/review/capability");
    return;
  }
  state.arg = raw;
  updateTabNav(tab);
  const id = parts[1] || null;
  toggleViews(!!id);
  if (id) loadDetail(tab, id, raw);
  else loadQueue(tab, raw);
}

function updateTabNav(tab) {
  $("#review-tab-capability").classList.toggle("active", tab === "capability");
  $("#review-tab-augur").classList.toggle("active", tab === "augur");
  $("#review-queue-blurb").textContent = QUEUE_BLURB[tab] || "";
  $("#review-detail-back").href = "#/review/" + tab;
}

function toggleViews(showDetail) {
  $("#review-queue-view").style.display = showDetail ? "none" : "";
  $("#review-detail-view").style.display = showDetail ? "" : "none";
}

// --- Queue -----------------------------------------------------------------

async function loadQueue(tab, raw) {
  const cards = $("#review-cards");
  setStatus("review-status", "loading…");
  const body = await api("/api/review/" + tab);
  if (state.arg !== raw) return; // navigated away
  cards.innerHTML = "";
  if (body.error) {
    setStatus("review-status", body.error, true);
    return;
  }
  if (body.unprovisioned) {
    // The read-model bucket doesn't exist because the package that provisions
    // it isn't installed on this stack — a benign "not enabled here" state, not
    // an error. Teach the operator how to light it up.
    setStatus("review-status", "not installed on this stack");
    cards.appendChild(reviewUnprovisionedCard(tab, body.packageName));
    return;
  }
  const rows = tab === "augur" ? augurProposalRows(body.proposals || []) : proposalRows(body.proposals || []);
  setStatus("review-status", rows.length + " proposal(s)");
  if (!rows.length) {
    cards.appendChild(el("div", "muted",
      tab === "augur"
        ? "No Augur escalations yet. When the platform hits an orchestration gap and an AI proposes a remediation, it lands here for your review."
        : "No capability proposals yet. When an AI authors a new lens, grant, or op, it lands here for your review."));
    return;
  }
  rows.forEach((row) => cards.appendChild(tab === "augur" ? augurQueueCard(row) : capabilityQueueCard(row)));
}

// reviewUnprovisionedCard is the empty-state shown when a tab's read-model
// bucket doesn't exist because its owning package isn't installed on this
// stack — a benign "not enabled here", distinct from both an error and a
// genuinely-empty-but-provisioned queue. It names the package and the bundle
// target that installs it, so the operator can light the tab up.
function reviewUnprovisionedCard(tab, packageName) {
  const pkg = packageName || (tab === "augur" ? "augur" : "capability-author");
  const card = el("div", "card review-card");
  card.appendChild(el("div", "card-key",
    tab === "augur"
      ? "The Augur escalation loop isn't installed on this stack."
      : "The capability-authoring loop isn't installed on this stack."));
  card.appendChild(el("div", "muted",
    "This tab reads the " + pkg + " package's proposal read-model, which doesn't exist here yet — " +
    "so there's nothing to review. This is expected on a stack that doesn't run the AI-native loop."));
  const how = el("div", "muted small");
  how.appendChild(document.createTextNode("Install it (with the other AI-loop package) via "));
  how.appendChild(el("code", null, "make install-ai"));
  how.appendChild(document.createTextNode(", then reload."));
  card.appendChild(how);
  return card;
}

// cardBorderClass reuses the existing .card left-border color vocabulary
// (green/yellow/red, from the Health-card family) rather than inventing a
// parallel one — the state chip inside the card is the precise signal.
const cardBorderClass = { pending: "yellow", approved: "green", applied: "green", dispatched: "green", invalid: "red" };

function capabilityQueueCard(row) {
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
  if (hasConfidenceScore(row.confidence)) {
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

function augurQueueCard(row) {
  const displayState = row.displayState;
  const card = el("a", "card review-card " + (cardBorderClass[displayState] || ""));
  card.href = "#/review/augur/" + encodeURIComponent(row.proposalId);
  card.appendChild(el("div", "card-key", (row.gapColumn || "(no gap recorded)") +
    (row.entityId ? " on " + row.entityId : "")));
  const meta = el("div", "review-card-meta");
  meta.appendChild(el("span", reviewStateClass(displayState), displayState));
  if (row.trigger) meta.appendChild(el("span", "review-glyph", row.trigger));
  if (row.proposedAction) meta.appendChild(el("span", null, row.proposedAction));
  if (hasConfidenceScore(row.confidence)) {
    const band = confidenceBand(row.confidence);
    meta.appendChild(el("span", "confidence-band " + band, "conf " + row.confidence.toFixed(2)));
  }
  if (row.model) meta.appendChild(el("span", null, row.model));
  const ago = agoFrom(row.reasonedAt, Date.now());
  if (ago) meta.appendChild(el("span", null, ago));
  card.appendChild(meta);
  if (row.entityId) {
    const ent = el("div", "muted small");
    ent.appendChild(document.createTextNode("candidate "));
    ent.appendChild(keyLinkEl(row.entityId));
    card.appendChild(ent);
  }
  return card;
}

// --- Detail ------------------------------------------------------------

async function loadDetail(tab, id, raw) {
  const body = $("#review-detail-body");
  body.innerHTML = "";
  body.appendChild(el("div", "muted small", "loading…"));
  setStatus("review-detail-status", "loading…");
  const proposal = await api("/api/review/" + tab + "/" + encodeURIComponent(id));
  if (state.arg !== raw) return; // navigated away while loading
  body.innerHTML = "";
  if (proposal.error) {
    setStatus("review-detail-status", proposal.error, true);
    const card = el("div", "notfound-card");
    card.appendChild(el("div", "notfound-key", id));
    card.appendChild(el("div", "muted", proposal.error));
    const back = el("a", "key-link", "← back to the queue");
    back.href = "#/review/" + tab;
    card.appendChild(back);
    body.appendChild(card);
    return;
  }
  setStatus("review-detail-status", "");
  if (tab === "augur") {
    body.appendChild(augurHeadSection(proposal));
    body.appendChild(rationaleSection(proposal));
    body.appendChild(proposedOpSection(proposal));
    body.appendChild(augurProvenanceSection(proposal));
    body.appendChild(augurActionSection(proposal, raw));
  } else {
    body.appendChild(headSection(proposal));
    body.appendChild(rationaleSection(proposal));
    body.appendChild(artifactSection(proposal));
    body.appendChild(deltaSection(proposal));
    body.appendChild(provenanceSection(proposal));
    body.appendChild(actionSection(proposal, raw));
  }
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
    "this preview was computed at author time — approving re-validates against the live catalog."));
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

function actionSection(p, raw) {
  const box = panel("Verdict");
  const displayState = proposalDisplayState(p);
  // Approved-but-not-applied: the human verdict landed; the artifact still
  // has to be installed. F16.2's apply endpoint drives the two-commit F-004
  // install in-process, so the card offers "Apply now" rather than a CLI
  // hand-off (design §3.3).
  if (displayState === "approved") {
    box.appendChild(el("div", "muted", outcomeLine(p, displayState)));
    box.appendChild(applyRow(p, raw));
    return box;
  }
  if (displayState !== "pending") {
    box.appendChild(el("div", "muted", outcomeLine(p, displayState)));
    return box;
  }
  const row = el("div", "lens-ctlrow");

  const approve = demoHide(el("button", null, "Approve & install…"));
  approve.title = "re-validates the artifact against the live catalog, then records your approval";
  approve.addEventListener("click", async () => {
    if (!window.confirm(
      "Approve this proposal? Loupe re-validates the artifact against the current catalog before recording " +
      "your approval; you then install it with a second click.")) return;
    row.querySelectorAll("button").forEach((b) => { b.disabled = true; });
    setStatus("review-detail-status", "re-validating + submitting approve…");
    const body = await api("/api/review/capability/" + encodeURIComponent(p.proposalId) + "/approve", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
    });
    if (state.arg !== raw) return; // navigated away
    if (body.error) {
      setStatus("review-detail-status", "approve failed: " + body.error, true);
      row.querySelectorAll("button").forEach((b) => { b.disabled = false; });
      return;
    }
    if (body.blocked) {
      // Re-validation against the live catalog failed — nothing was submitted.
      setStatus("review-detail-status",
        "this no longer validates against the current catalog: " + (body.validationReport || "no report"), true);
      row.querySelectorAll("button").forEach((b) => { b.disabled = false; });
      return;
    }
    if (body.status === "rejected") {
      setStatus("review-detail-status",
        "approve rejected: " + ((body.error && body.error.message) || body.decision || "see reply"), true);
      row.querySelectorAll("button").forEach((b) => { b.disabled = false; });
      return;
    }
    toast("proposal approved — install it below");
    loadDetail("capability", p.proposalId, raw);
  });
  row.appendChild(approve);

  const reject = demoHide(el("button", "danger-btn", "Reject"));
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
    if (state.arg !== raw) return; // navigated away
    if (body.error) {
      setStatus("review-detail-status", "reject failed: " + body.error, true);
      row.querySelectorAll("button").forEach((b) => { b.disabled = false; });
      return;
    }
    toast("proposal rejected");
    loadDetail("capability", p.proposalId, raw);
  });
  row.appendChild(reject);
  box.appendChild(row);
  return box;
}

// applyRow renders the controls for an approved-but-not-applied proposal
// (F16.2, design §3.3): "Apply now", the two-Processor-commit F-004 install
// flow the /apply endpoint drives server-side, and beside it the recovery for
// when only the first of those two commits landed.
//
// Recovery is a standing control, not one the failed apply reveals, because
// the state it repairs outlives the page: an operator who closes the tab after
// a failed apply comes back to a proposal that reads "approved, not applied",
// and from there Apply can only fail. The endpoint takes no payload and
// re-derives everything, so the button works on a cold load; when its
// precondition does not hold it answers with the reason (nothing installed at
// this proposal's target version → run Apply instead) rather than acting.
//
// deadEnd latches the case where the server has told us re-applying cannot
// work. It has to persist across a failed recovery attempt: re-enabling Apply
// on any recovery error would hand back a button whose next click earns the
// same refusal, and whose refusal re-enables it again.
function applyRow(p, raw) {
  const row = el("div", "lens-ctlrow");
  let deadEnd = false;
  const apply = demoHide(el("button", null, "Apply now"));
  apply.title = "installs the approved artifact (" + (p.targetMode || "install") +
    (p.targetPackageName ? " " + p.targetPackageName : "") + ") and closes the proposal";
  const recover = demoHide(el("button", "ghost-btn", "Mark applied (recover)"));
  recover.title = "closes a proposal whose package install already committed but whose closing op did not; " +
    "verifies that package is installed at this proposal's target version before submitting";

  apply.addEventListener("click", async () => {
    if (!window.confirm(
      "Install this approved artifact now? This runs a real package install/upgrade against the running system.")) return;
    apply.disabled = true;
    recover.disabled = true;
    setStatus("review-detail-status", "installing…");
    const body = await api("/api/review/capability/" + encodeURIComponent(p.proposalId) + "/apply", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
    });
    if (state.arg !== raw) return; // navigated away
    if (body.error || body.resumable) {
      const outcome = applyOutcome(body);
      setStatus("review-detail-status", outcome.message + (outcome.hint ? " — " + outcome.hint : ""), true);
      deadEnd = deadEnd || !outcome.retryable;
      apply.disabled = deadEnd;
      recover.disabled = false;
      return;
    }
    // The install can succeed while the closing op is REFUSED by the
    // Processor: that arrives as an ordinary reply, not an error, so a
    // success toast here would report the half-committed state as done.
    if (opRejected(body.markApplied)) {
      setStatus("review-detail-status",
        "the artifact installed, but closing the proposal was rejected: " +
        errorText(body.markApplied.error) + " — close it with “Mark applied (recover)”.", true);
      deadEnd = true;
      apply.disabled = true;
      recover.disabled = false;
      return;
    }
    toast("artifact installed — proposal applied");
    loadDetail("capability", p.proposalId, raw);
  });
  row.appendChild(apply);

  recover.addEventListener("click", async () => {
    if (!window.confirm(
      "Mark this proposal applied against the installed package named “" + (p.targetPackageName || "?") + "”? " +
      "Use this only when a previous install committed but failed to close the proposal. Loupe verifies that " +
      "package is installed at this proposal's target version before submitting, and installs nothing itself.")) return;
    apply.disabled = true;
    recover.disabled = true;
    setStatus("review-detail-status", "verifying the install + submitting mark-applied…");
    const body = await api("/api/review/capability/" + encodeURIComponent(p.proposalId) + "/mark-applied", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
    });
    if (state.arg !== raw) return; // navigated away
    if (body.error) {
      setStatus("review-detail-status", "mark applied failed: " + errorText(body.error), true);
      apply.disabled = deadEnd;
      recover.disabled = false;
      return;
    }
    if (opRejected(body.markApplied)) {
      setStatus("review-detail-status",
        "mark applied was rejected: " + errorText(body.markApplied.error), true);
      apply.disabled = deadEnd;
      recover.disabled = false;
      return;
    }
    toast("proposal closed — marked applied against " + (body.packageKey || "the installed package"));
    loadDetail("capability", p.proposalId, raw);
  });
  row.appendChild(recover);
  return row;
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

// --- Augur detail (F16.3) ---------------------------------------------

function augurHeadSection(p) {
  const box = panel(null);
  box.appendChild(el("h2", "comp-title", p.gapColumn || "(no gap recorded)"));
  const displayState = augurDisplayState(p);
  box.appendChild(el("span", reviewStateClass(displayState), displayState));
  if (displayState === "invalid" && p.invalidReason) {
    box.appendChild(el("div", "review-invalid-reason", p.invalidReason));
  }
  const timeline = el("div", "muted small");
  const bits = [];
  if (p.trigger) bits.push("trigger: " + p.trigger);
  if (p.reasonedAt) bits.push("reasoned " + agoFrom(p.reasonedAt, Date.now()) + " (" + p.reasonedAt + ")");
  if (p.reviewedAt) bits.push("reviewed " + agoFrom(p.reviewedAt, Date.now()) + " (" + p.reviewedAt + ")");
  if (p.dispatchedAt) bits.push("dispatched " + agoFrom(p.dispatchedAt, Date.now()) + " (" + p.dispatchedAt + ")");
  timeline.textContent = bits.join(" · ");
  box.appendChild(timeline);
  if (p.entityId) {
    const ent = el("div", "muted small");
    ent.appendChild(document.createTextNode("candidate "));
    ent.appendChild(keyLinkEl(p.entityId));
    box.appendChild(ent);
  }
  if (p.targetId) {
    const tgt = el("div", "muted small");
    tgt.appendChild(document.createTextNode("weaver target "));
    tgt.appendChild(keyLinkEl(p.targetId));
    box.appendChild(tgt);
  }
  return box;
}

function proposedOpSection(p) {
  const box = panel("The proposed remediation");
  if (!p.proposedAction) {
    box.appendChild(el("div", "muted", "reasoning still in flight — no proposal recorded yet"));
    return box;
  }
  const meta = el("div", "review-card-meta");
  meta.appendChild(el("span", "review-glyph", p.proposedAction));
  if (hasConfidenceScore(p.confidence)) {
    const band = confidenceBand(p.confidence);
    meta.appendChild(el("span", "confidence-band " + band, "conf " + p.confidence.toFixed(2)));
  }
  box.appendChild(meta);
  box.appendChild(p.proposedParams !== undefined && p.proposedParams !== null
    ? renderDoc(p.proposedParams)
    : el("div", "muted small", "(no params recorded)"));
  return box;
}

function augurProvenanceSection(p) {
  const details = el("details");
  details.appendChild(el("summary", "muted small", "provenance"));
  const inner = panel(null);
  inner.appendChild(el("div", "muted small", "model: " + (p.model || "?")));
  inner.appendChild(el("div", "muted small", "reasonedAt: " + (p.reasonedAt || "?")));
  details.appendChild(inner);
  return details;
}

function augurActionSection(p, raw) {
  const box = panel("Verdict");
  const displayState = augurDisplayState(p);
  if (displayState !== "pending") {
    box.appendChild(el("div", "muted", augurOutcomeLine(p, displayState)));
    return box;
  }
  const row = el("div", "lens-ctlrow");
  const proposalKey = "vtx.augurproposal." + p.proposalId;

  const approve = demoHide(el("button", null, "Approve & dispatch"));
  approve.addEventListener("click", async () => {
    if (!window.confirm(
      "Approving arms autonomous dispatch of this op against " + (p.entityId || "the escalated candidate") + ".")) return;
    row.querySelectorAll("button").forEach((b) => { b.disabled = true; });
    setStatus("review-detail-status", "submitting approve…");
    const body = await api("/api/op", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        operationType: "ReviewProposal",
        payload: { externalRef: p.proposalId, verdict: "approve" },
        reads: [proposalKey + ".review", proposalKey + ".proposed", proposalKey + ".confidence", proposalKey + ".gap"],
      }),
    });
    if (state.arg !== raw) return; // navigated away
    if (body.error) {
      setStatus("review-detail-status", "approve failed: " + body.error, true);
      row.querySelectorAll("button").forEach((b) => { b.disabled = false; });
      return;
    }
    toast("proposal approved — dispatch is now armed");
    loadDetail("augur", p.proposalId, raw);
  });
  row.appendChild(approve);

  const reject = demoHide(el("button", "danger-btn", "Reject"));
  reject.addEventListener("click", async () => {
    if (!window.confirm(
      "Reject this proposal? The AI's reasoning stays recorded for audit; the remediation will not dispatch.")) return;
    row.querySelectorAll("button").forEach((b) => { b.disabled = true; });
    setStatus("review-detail-status", "submitting reject…");
    const body = await api("/api/op", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        operationType: "ReviewProposal",
        payload: { externalRef: p.proposalId, verdict: "reject" },
        reads: [proposalKey + ".review"],
      }),
    });
    if (state.arg !== raw) return; // navigated away
    if (body.error) {
      setStatus("review-detail-status", "reject failed: " + body.error, true);
      row.querySelectorAll("button").forEach((b) => { b.disabled = false; });
      return;
    }
    toast("proposal rejected");
    loadDetail("augur", p.proposalId, raw);
  });
  row.appendChild(reject);
  box.appendChild(row);
  return box;
}

function augurOutcomeLine(p, displayState) {
  if (displayState === "dispatched") {
    return "approved & dispatched at " + (p.dispatchedAt || "?");
  }
  if (displayState === "approved") {
    return "approved at " + (p.reviewedAt || "?") + " — awaiting dispatch";
  }
  if (displayState === "rejected") {
    return "rejected at " + (p.reviewedAt || "?");
  }
  if (displayState === "invalid") {
    return "invalid — " + (p.invalidReason || "no reason recorded");
  }
  return "reasoning still in flight";
}

function init() {
  const back = $("#review-load");
  if (back) back.addEventListener("click", () => {
    const parts = (state.arg || "").split("/").filter(Boolean);
    loadQueue(parts[0] || "capability", state.arg);
  });
}

function leave() {}

export { init, enter, leave };
