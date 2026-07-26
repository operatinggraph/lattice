// The Flows tab's decision logic: how a flow's severity is read, how the wall
// is ordered, and how it collapses into per-pattern groups. The view binds
// DOM; everything that decides anything lives here (goja-tested).

// flowSeverity ranks what an operator must look at first. The three liveness
// verdicts sit ABOVE a plain running row because each is a disagreement
// between the durable history and the engine — a flow that failed, one whose
// history is stale, and one the engine has forgotten are all findings; a flow
// that is simply running is not.
var flowSeverity = {
  failed: 5,
  "stale-history": 4,
  orphaned: 3,
  running: 1,
  complete: 0,
};

// flowClass is the row's status colour. A liveness disagreement outranks the
// row's own status: a row claiming "running" while the engine says otherwise
// must not render in the calm colour its own claim would earn.
var flowClass = {
  failed: "red",
  "stale-history": "red",
  orphaned: "red",
  running: "yellow",
  complete: "green",
};

// flowKind collapses a row's status and its liveness verdict into the single
// word everything else keys on. A terminal row is its status; a running row is
// its liveness when there is one, since that is the more specific truth.
function flowKind(row) {
  if (!row) return "";
  if (row.status === "running" && row.liveness) return row.liveness;
  return row.status || "";
}

// flowLabel is the badge text. "stale-history" reads as an accusation with no
// object, so it names both voices — the operator needs to know WHO says what
// to decide whether the flow or the projection is the problem.
function flowLabel(row) {
  var kind = flowKind(row);
  if (kind === "stale-history") {
    return "history stale · Loom says " + (row.engineStatus || "finished");
  }
  if (kind === "orphaned") return "orphaned · Loom has no record";
  if (kind === "live") return "live";
  return kind;
}

// patternLabel is what a card is titled. The read model stores a bare
// vtx.meta.<NanoID>, which makes every card look like every other card, so the
// resolved canonical name wins and the ref is the fallback rather than the
// default.
function patternLabel(row) {
  if (!row) return "";
  return row.patternName || row.patternRef || row.instanceId || "";
}

// flowRows stamps each row with its kind/severity/class and sorts
// exception-first, newest-started within a severity. The server returns a
// deterministic newest-first base order; the triage sort is this tier's job,
// the same split the capability review queue uses.
function flowRows(list) {
  var rows = (list || []).map(function (r) {
    var kind = flowKind(r);
    return Object.assign({}, r, {
      kind: kind,
      label: flowLabel(r),
      pattern: patternLabel(r),
      severity: flowSeverity[kind] === undefined ? 2 : flowSeverity[kind],
      cls: flowClass[kind] || "",
    });
  });
  rows.sort(function (a, b) {
    if (a.severity !== b.severity) return b.severity - a.severity;
    if (a.startedAt !== b.startedAt) return a.startedAt > b.startedAt ? -1 : 1;
    return a.instanceId < b.instanceId ? -1 : a.instanceId > b.instanceId ? 1 : 0;
  });
  return rows;
}

// groupFlowsByPattern collapses the wall into one group per pattern, ordered
// by the worst row each contains so a group holding the only failure sorts to
// the top however few rows it has. Rows keep the exception-first order they
// arrived in.
function groupFlowsByPattern(rows) {
  var groups = [];
  var byRef = {};
  (rows || []).forEach(function (r) {
    var ref = r.patternRef || "";
    if (!byRef[ref]) {
      byRef[ref] = { patternRef: ref, pattern: r.pattern, rows: [], worst: -1, counts: {} };
      groups.push(byRef[ref]);
    }
    var g = byRef[ref];
    g.rows.push(r);
    if (r.severity > g.worst) { g.worst = r.severity; g.worstKind = r.kind; }
    g.counts[r.kind] = (g.counts[r.kind] || 0) + 1;
  });
  groups.sort(function (a, b) {
    if (a.worst !== b.worst) return b.worst - a.worst;
    if (a.rows.length !== b.rows.length) return b.rows.length - a.rows.length;
    return a.pattern < b.pattern ? -1 : a.pattern > b.pattern ? 1 : 0;
  });
  return groups;
}

// groupSummary is a group header's count line, worst kind first so the header
// leads with the reason the group sorted where it did.
function groupSummary(group) {
  if (!group) return "";
  var kinds = Object.keys(group.counts || {});
  kinds.sort(function (a, b) {
    var sa = flowSeverity[a] === undefined ? 2 : flowSeverity[a];
    var sb = flowSeverity[b] === undefined ? 2 : flowSeverity[b];
    if (sa !== sb) return sb - sa;
    return a < b ? -1 : 1;
  });
  return kinds.map(function (k) { return group.counts[k] + " " + k; }).join(" · ");
}

// flowsHeadline is the status line above the wall. It leads with the count
// that needs attention, because "26 flows" answers a question nobody asked.
function flowsHeadline(rows) {
  var total = (rows || []).length;
  var attention = (rows || []).filter(function (r) { return r.severity >= 3; }).length;
  var word = total === 1 ? "flow" : "flows";
  if (!total) return "no flows";
  if (!attention) return total + " " + word + " · all healthy";
  return attention + " of " + total + " " + word + " need attention";
}

export { flowSeverity, flowKind, flowLabel, patternLabel, flowRows, groupFlowsByPattern, groupSummary, flowsHeadline };
