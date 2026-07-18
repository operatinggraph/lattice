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
  rejected: "review-state rejected",
  invalid: "review-state invalid",
};
function reviewStateClass(displayState) {
  return reviewStateClassMap[displayState] || "review-state unknown";
}

// confidenceBand buckets a 0..1 confidence score into low/med/high for the
// red→amber→green ramp (§5); an undefined/out-of-range score bands "unknown"
// (rendered dim, never a false-confident color).
function confidenceBand(score) {
  if (typeof score !== "number" || isNaN(score)) return "unknown";
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
      targetNewVersion: r.targetNewVersion || "",
      confidence: r.confidence,
      model: r.model || "",
      reasonedAt: r.reasonedAt || "",
      reviewedAt: r.reviewedAt || "",
      reviewState: r.reviewState || "",
      invalidReason: r.reviewInvalidReason || "",
      appliedAt: r.appliedAt || "",
      appliedByOp: r.appliedByOp || "",
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
// badge's data source (§2.2). Augur's count joins this in F16.3.
function pendingCount(list) {
  var n = 0;
  var rows = list || [];
  for (var i = 0; i < rows.length; i++) {
    if (isActionable(rows[i])) n++;
  }
  return n;
}

export { kindGlyph, proposalDisplayState, reviewStateClass, confidenceBand, isActionable, agoFrom, proposalRows, pendingCount };
