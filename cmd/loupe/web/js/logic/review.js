// Pure AI-review-console logic (loupe-f16-ai-review-console-ux.md §2.3): row
// shaping/sorting, state→class, confidence banding, and the artifact-kind
// glyph map. No DOM, no fetch — goja-tested via cmd/loupe/web_logic_test.go
// (strip-export load). Decision logic (what's actionable, how a row sorts,
// how a verdict maps to a display state) lives here so it is unit-tested
// without a browser, per the house rule.

// kindGlyph maps a capability artifact's kind to the meta-line glyph (§3.1).
var kindGlyph = {
  lens: "▤",
  grant: "🔑",
  weaverTarget: "◇",
  loomPattern: "⛓",
  vertexTypeDDL: "▦",
  opMeta: "⚙",
};

// proposalDisplayState collapses a raw row's kind/reviewState/appliedAt into
// the one state the card renders: "authoring" (no artifact yet — reasoning in
// flight), "pending" (awaiting a human verdict), "approved", "applied"
// (approved AND appliedAt is set), "rejected", or "invalid".
function proposalDisplayState(row) {
  var r = row || {};
  if (!r.kind) return "authoring";
  var state = r.reviewState || "pending";
  if (state === "approved" && r.appliedAt) return "applied";
  return state;
}

// reviewStateClass maps a display state to its CSS state-chip class.
var reviewStateClassMap = {
  authoring: "review-state authoring",
  pending: "review-state pending",
  approved: "review-state approved",
  applied: "review-state applied",
  dispatched: "review-state dispatched",
  rejected: "review-state rejected",
  invalid: "review-state invalid",
};
function reviewStateClass(displayState) {
  return reviewStateClassMap[displayState] || "review-state unknown";
}

// hasConfidenceScore reports whether a row carries a real 0..1 model
// confidence. A proposal with no model behind it — one an operator authored
// directly, or one whose reasoning result failed to decode — records the -1.0
// absent-sentinel rather than a fabricated score, and a sentinel is not a
// score to render.
function hasConfidenceScore(score) {
  return typeof score === "number" && !isNaN(score) && score >= 0 && score <= 1;
}

// confidenceBand buckets a 0..1 confidence score into low/med/high for the
// red→amber→green ramp (§5); an undefined/out-of-range score bands "unknown"
// (rendered dim, never a false-confident color). The out-of-range arm is what
// keeps the -1.0 absent-sentinel from reading as a red "low confidence"
// verdict on a proposal nothing ever scored.
function confidenceBand(score) {
  if (!hasConfidenceScore(score)) return "unknown";
  if (score < 0.5) return "low";
  if (score < 0.8) return "med";
  return "high";
}

// isActionable reports whether a raw row awaits a human verdict — the badge
// + queue-ordering predicate. An in-flight "authoring…" row (reviewState
// empty, no kind yet) is deliberately NOT actionable: there is nothing to
// review until RecordCapabilityProposal lands an artifact.
function isActionable(row) {
  return !!(row && row.reviewState === "pending");
}

// sourceLabel classifies a proposal row's declared provenance.source into
// the review queue's origin badge (F25.3b — the studio is a second proposal
// source into the same queue). "operator" is the only value a human's direct
// SubmitCapabilityProposal ever stamps; everything else — the bridge-
// recorded 'ai' lane, and the null the lens projects for a proposal recorded
// before the field existed or whose reasoning is still in flight — reads as
// "ai": in both null cases no human authored an artifact yet, so there is
// nothing to badge as operator-originated. A declared field, never inferred
// from the presence of model-shaped provenance (lenses.go's own framing).
function sourceLabel(source) {
  return source === "operator" ? "operator" : "ai";
}

// agoFrom renders an ISO-8601 timestamp as a coarsest-unit "ago" string
// (mirrors cmd/loupe/health.go's humanizeAgo). nowMs is passed in rather than
// read from Date.now() so the function stays pure and goja-testable; an
// unparsable/empty iso renders "".
function agoFrom(iso, nowMs) {
  if (!iso) return "";
  var t = Date.parse(iso);
  if (isNaN(t)) return "";
  var deltaMs = nowMs - t;
  if (deltaMs < 0) deltaMs = 0;
  var s = Math.floor(deltaMs / 1000);
  if (s < 60) return s + "s ago";
  var m = Math.floor(s / 60);
  if (m < 60) return m + "m ago";
  var h = Math.floor(m / 60);
  if (h < 24) return h + "h ago";
  var d = Math.floor(h / 24);
  return d + "d ago";
}

// artifactTargetId reads the targetId out of a weaverTarget artifact's content
// (a JSON string per the DDL), or "" when the content is absent or does not
// parse — an AI-authored artifact is not guaranteed well-formed at record time,
// the same allowance views/review.js's prettyContent makes.
function artifactTargetId(content) {
  if (!content) return "";
  try {
    var parsed = JSON.parse(content);
    return (parsed && typeof parsed.targetId === "string") ? parsed.targetId : "";
  } catch (e) {
    return "";
  }
}

// installTargetLabel says what approving a proposal would DO, in one line.
//
// An upgradeExisting proposal changes an artifact that is already installed, in
// place — a different act from installing a new one, and a reviewer who reads
// it as a fresh install approves something they were not shown. So the edit
// shape is spelled out: which target, in which package, across which versions.
// A newPackage proposal keeps the mode-and-name line it has always had.
//
// Absent versions render "?" rather than being dropped: an upgrade whose base
// or target version is missing is refused at apply, and a label that quietly
// omitted them would read like a well-formed edit.
function installTargetLabel(row) {
  var r = row || {};
  var pkg = r.targetPackageName || "";
  if (!pkg) return "";
  if (r.targetMode !== "upgradeExisting") {
    return (r.targetMode || "?") + " " + pkg + (r.targetNewVersion ? "@" + r.targetNewVersion : "");
  }
  var targetId = artifactTargetId(r.content);
  var subject = targetId ? "edits " + targetId + " in " + pkg : "edits " + pkg;
  return subject + " " + (r.targetBaseVersion || "?") + " → " + (r.targetNewVersion || "?");
}

// proposalRows shapes the server's raw capability-proposals rows into the
// queue's view model and sorts them: actionable (pending) rows first, then
// newest reasonedAt first (ISO-8601 strings compare lexically), then
// proposalId for a stable tie-break.
function proposalRows(list) {
  var rows = (list || []).map(function (r) {
    return {
      proposalId: r.proposalId || "",
      intent: r.intent || "",
      requesterId: r.requesterId || "",
      kind: r.kind || "",
      targetMode: r.targetMode || "",
      targetPackageName: r.targetPackageName || "",
      targetBaseVersion: r.targetBaseVersion || "",
      targetNewVersion: r.targetNewVersion || "",
      installLabel: installTargetLabel(r),
      confidence: r.confidence,
      model: r.model || "",
      reasonedAt: r.reasonedAt || "",
      reviewedAt: r.reviewedAt || "",
      reviewState: r.reviewState || "",
      invalidReason: r.reviewInvalidReason || "",
      appliedAt: r.appliedAt || "",
      appliedByOp: r.appliedByOp || "",
      source: sourceLabel(r.source),
      displayState: proposalDisplayState(r),
      actionable: isActionable(r),
    };
  });
  rows.sort(function (a, b) {
    if (a.actionable !== b.actionable) return a.actionable ? -1 : 1;
    if (a.reasonedAt !== b.reasonedAt) return a.reasonedAt > b.reasonedAt ? -1 : 1;
    return a.proposalId < b.proposalId ? -1 : a.proposalId > b.proposalId ? 1 : 0;
  });
  return rows;
}

// pendingCount counts raw rows awaiting a human verdict — the shell nav
// badge's data source (§2.2), joined across both loops by the caller.
function pendingCount(list) {
  var n = 0;
  var rows = list || [];
  for (var i = 0; i < rows.length; i++) {
    if (isActionable(rows[i])) n++;
  }
  return n;
}

// augurDisplayState collapses a raw Augur row's reviewState/dispatchedAt
// into the one state the card renders: "authoring" (claim minted, reasoning
// still in flight — reviewState null, per lenses.go's "Loupe renders 'in
// flight' for a null state"), "pending" (awaiting a human verdict),
// "invalid" (machine- or re-validation-blocked, terminal), "approved"
// (armed, not yet dispatched), "dispatched" (approved AND dispatchedAt is
// set), or "rejected".
function augurDisplayState(row) {
  var r = row || {};
  var state = r.reviewState;
  if (!state) return "authoring";
  if (state === "approved" && r.dispatchedAt) return "dispatched";
  return state;
}

// augurProposalRows shapes the server's raw augur-proposals rows into the
// queue's view model. Sort (§4.1, adjudicated §8.4): actionable (pending)
// rows first, sorted by confidence DESCENDING within that group (credible
// proposals rise; an unscored/non-numeric confidence sorts as lowest, never
// hidden); non-pending rows sort newest reasonedAt first. Both groups
// tie-break on proposalId for stability.
function augurProposalRows(list) {
  var rows = (list || []).map(function (r) {
    return {
      proposalId: r.proposalId || "",
      targetId: r.targetId || "",
      entityId: r.entityId || "",
      gapColumn: r.gapColumn || "",
      trigger: r.trigger || "",
      proposedAction: r.proposedAction || "",
      proposedParams: r.proposedParams,
      rationale: r.rationale || "",
      confidence: r.confidence,
      model: r.model || "",
      reasonedAt: r.reasonedAt || "",
      reviewedAt: r.reviewedAt || "",
      reviewState: r.reviewState || "",
      invalidReason: r.invalidReason || "",
      dispatchedAt: r.dispatchedAt || "",
      displayState: augurDisplayState(r),
      actionable: isActionable(r),
    };
  });
  rows.sort(function (a, b) {
    if (a.actionable !== b.actionable) return a.actionable ? -1 : 1;
    if (a.actionable) {
      var ca = typeof a.confidence === "number" ? a.confidence : -1;
      var cb = typeof b.confidence === "number" ? b.confidence : -1;
      if (ca !== cb) return ca > cb ? -1 : 1;
      return a.proposalId < b.proposalId ? -1 : a.proposalId > b.proposalId ? 1 : 0;
    }
    if (a.reasonedAt !== b.reasonedAt) return a.reasonedAt > b.reasonedAt ? -1 : 1;
    return a.proposalId < b.proposalId ? -1 : a.proposalId > b.proposalId ? 1 : 0;
  });
  return rows;
}

// errorText renders a reply's error field. An op reply's error is an object
// ({code, message}) while a handler's is a plain string, and both shapes reach
// the same status lines — concatenating the object would print
// "[object Object]" where the reason belongs.
function errorText(err) {
  if (!err) return "";
  if (typeof err === "string") return err;
  return err.message || err.code || "see reply";
}

// applyOutcome classifies an /apply failure into what the operator should be
// told and which control should come back alive.
//
// The distinction that matters is `resumable`: the package this proposal would
// install is already installed at its target version, so the install half has
// committed and only the closing MarkCapabilityProposalApplied has not.
// Re-applying cannot finish it — for a newPackage target the server's plan
// builder refuses outright, and for an upgrade it would re-run an install that
// already landed — so re-arming "Apply now" only walks the operator back into
// the same wall. That case arms the recovery control instead, and the arming
// is a LATCH: it must survive a failed recovery attempt, or the first gateway
// hiccup hands the dead-end button back.
function applyOutcome(body) {
  var reason = errorText(body && body.error) || "unknown error";
  if (body && body.resumable) {
    return {
      message: "apply failed: " + reason,
      resumable: true,
      retryable: false,
      hint: "the package IS installed — close the proposal with “Mark applied (recover)”; re-applying cannot succeed now.",
    };
  }
  // A failure reply naming the packageKey the apply produced is saying the
  // INSTALL committed and only the close did not. Re-arming "Apply now" there
  // walks the operator into the plan builder's own refusal — for a newPackage
  // target the name is now claimed — so the control stays down even when the
  // recovery is unavailable too. The server's message carries the exit in that
  // case; this only stops the console from offering a worse one.
  if (body && body.packageKey) {
    return { message: "apply failed: " + reason, resumable: false, retryable: false, hint: "" };
  }
  return { message: "apply failed: " + reason, resumable: false, retryable: true, hint: "" };
}

// receiptNotice states what happened to the apply's install RECEIPT — the
// aspect binding this proposal to the package its install actually produced.
//
// It has to be said out loud on a SUCCESSFUL apply, not only on a failed one.
// A receipt the Processor refuses (a permission gap, a package the op cannot
// verify) leaves the apply itself green, and the binding is then unobtainable
// forever: the aspect is create-only and the op requires an approved proposal,
// which the mark-applied submit right behind it has already flipped to applied.
// With no notice, that failure looks exactly like a clean apply, and the
// console silently degrades to the name+version guess the receipt replaces.
//
// "not-applicable" is not a failure and gets no warning: that apply committed
// nothing, so there was never an install to bind.
function receiptNotice(body) {
  var state = body && body.receipt;
  if (!state || state === "recorded" || state === "not-applicable") return "";
  var reason = errorText(body && body.receiptFailure);
  return "the artifact installed, but recording this proposal's install receipt failed" +
    (reason ? ": " + reason : "") +
    " — the binding cannot be written afterwards, so any later recovery for this proposal " +
    "resolves its install by package name and version alone. Escalate it.";
}

// opRejected reports whether a relayed op reply came back refused by the
// Processor rather than committed. A rejection arrives as a well-formed reply
// with HTTP 200, not as an error, so a handler that branches on the error field
// alone reports the platform's refusal as success — which is exactly the
// half-committed state the recovery path exists to notice.
function opRejected(reply) {
  return !!(reply && reply.status === "rejected");
}

export { kindGlyph, proposalDisplayState, reviewStateClass, confidenceBand, hasConfidenceScore, isActionable, sourceLabel, agoFrom, artifactTargetId, installTargetLabel, proposalRows, pendingCount, augurDisplayState, augurProposalRows, applyOutcome, receiptNotice, opRejected, errorText };
