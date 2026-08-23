// Pure Weaver Target Studio shaping (weaver-target-studio-design.md §4, the
// Observe stage): the target roster's worst-first order, the target map's
// layered node/edge layout, the gap and entity badge vocabulary, and the entity
// drill's per-gap state line. No DOM, no fetch — goja-tested via
// cmd/loupe/web_logic_weaver_test.go (strip-export load, ES6-conservative).
//
// The console's honesty convention governs throughout: a signal the platform
// does not carry reads as unknown, never as a clean zero or a green. Three
// places that matters here — a contraction class absent from every heartbeat
// means "not sampled yet", not "steady"; a gap column no live row has ever
// carried is "never observed", not "missing" (a lens with no candidate
// entities legitimately has no rows); and a retry budget nothing declares is
// unbounded, so a dispatch count under it can never read as "exhausted".

// contractionLabel renders a trajectory class. "" is the absent case — the
// Weaver omits a target from the trajectory map until its ring has samples.
// "mixed" is Loupe's own class for two live instances disagreeing (the ring is
// per-process and a restart zeroes it), which is a real state, not a fault.
function contractionLabel(v) {
  switch (v) {
    case "diverging": return { text: "diverging", cls: "bad", title: "open gaps only grow — remediating slower than falling behind" };
    case "shrinking": return { text: "shrinking", cls: "ok", title: "open gaps trending down" };
    case "steady": return { text: "steady", cls: "muted", title: "open-gap count flat across the sampled window" };
    case "mixed": return { text: "mixed", cls: "warn", title: "live Weaver instances disagree — the trajectory ring is per-process and resets on restart" };
    default: return null;
  }
}

// targetRank orders the roster worst-first: oscillation-frozen (the engine has
// stopped remediating), then diverging, then disabled (inert on purpose, but
// worth seeing above the quiet ones), then anything carrying issues.
function targetRank(t) {
  if (t.frozen) return 0;
  if (t.contraction === "diverging") return 1;
  if (t.state === "disabled") return 2;
  if (t.issues > 0) return 3;
  return 4;
}

// targetRows normalizes and orders the roster. Orphan `__control` markers are
// folded in as first-class rows: nothing engine-side sweeps one, and a stale
// marker silently disables a future target installed under that id, so it
// belongs in the operator's list rather than in a footnote.
function targetRows(body) {
  var rows = [];
  var list = (body && body.targets) || [];
  for (var i = 0; i < list.length; i++) {
    var t = list[i] || {};
    rows.push({
      targetId: String(t.targetId || ""),
      description: String(t.description || ""),
      lensRef: String(t.lensRef || ""),
      gaps: typeof t.gaps === "number" ? t.gaps : 0,
      state: String(t.state || ""),
      contraction: String(t.contraction || ""),
      frozen: !!t.frozen,
      issues: typeof t.issues === "number" ? t.issues : 0,
      registered: true,
      orphan: false,
    });
  }
  var orphans = (body && body.orphanControl) || [];
  for (var j = 0; j < orphans.length; j++) {
    rows.push({
      targetId: String(orphans[j]),
      description: "",
      lensRef: "",
      gaps: 0,
      state: "disabled",
      contraction: "",
      frozen: false,
      issues: 0,
      registered: false,
      orphan: true,
    });
  }
  rows.sort(function (a, b) {
    // An orphan marker sorts with the disabled band, not above the frozen
    // ones — it is a cleanup item, not an outage.
    var ra = a.orphan ? 2 : targetRank(a);
    var rb = b.orphan ? 2 : targetRank(b);
    if (ra !== rb) return ra - rb;
    return a.targetId < b.targetId ? -1 : a.targetId > b.targetId ? 1 : 0;
  });
  return rows;
}

// rosterHeadline states what the list is showing, exception-first. A control
// plane that did not answer is said so outright — an empty roster and a silent
// engine are different facts.
function rosterHeadline(body, rows) {
  if (body && body.listError) return "weaver control plane did not answer: " + body.listError;
  var registered = 0, frozen = 0, disabled = 0, diverging = 0, orphan = 0;
  for (var i = 0; i < rows.length; i++) {
    var r = rows[i];
    if (r.orphan) { orphan++; continue; }
    registered++;
    if (r.frozen) frozen++;
    if (r.state === "disabled") disabled++;
    if (r.contraction === "diverging") diverging++;
  }
  var parts = [registered + (registered === 1 ? " target" : " targets")];
  if (frozen) parts.push(frozen + " frozen");
  if (diverging) parts.push(diverging + " diverging");
  if (disabled) parts.push(disabled + " disabled");
  if (orphan) parts.push(orphan + " orphan __control marker" + (orphan === 1 ? "" : "s"));
  if (body && body.stateError) parts.push("orphan scan unavailable: " + body.stateError);
  // Prose is read separately from the control-plane summary, so its failure is
  // its own fact: without this line an unreadable core-kv would look exactly
  // like a corpus of targets nobody has described.
  if (body && body.descriptionError) parts.push("descriptions unavailable: " + body.descriptionError);
  return parts.join(" · ");
}

// gapBadges renders one gap node's live-state chips. A count of zero is shown
// only for `open` — the number an operator is watching converge — while
// in-flight and exhausted stay absent at zero rather than adding two quiet
// "0"s to every healthy gap.
function gapBadges(g) {
  var out = [{ text: g.open + " open", cls: g.open > 0 ? "warn" : "ok" }];
  if (g.inflight > 0) out.push({ text: g.inflight + " in flight", cls: "info" });
  if (g.exhausted > 0) out.push({ text: g.exhausted + " budget-exhausted", cls: "bad" });
  if (!g.observed) {
    out.push({
      text: "column never observed",
      cls: "warn",
      title: "no scanned row carries this column — either the lens does not project it, or the lens has no candidate entities yet",
    });
  }
  return out;
}

// dispatchLabel names how a gap resolves. The explicit action always wins over
// candidates (internal/weaver: candidates are consulted only when action is
// empty), so the label is the resolution order, not a list of what is present.
function dispatchLabel(g) {
  switch (g.dispatch) {
    case "action": return (g.action && g.action.action) || "action";
    case "candidates": return "planner · " + ((g.candidates || []).length) + " candidates";
    case "goal": return "planner · goal over " + ((g.actions || []).length) + " actions";
    default: return "(no dispatch bound)";
  }
}

// actionSummary is the one-line rendering of a dispatch binding.
function actionSummary(a) {
  if (!a) return "";
  switch (a.action) {
    case "triggerLoom": return "pattern " + (a.pattern || "?") + " on " + (a.subject || "?");
    case "assignTask": return (a.operation || "?") + " → " + (a.assignee || "?");
    case "directOp": return (a.operation || "?") + (a.target ? " on " + a.target : "");
    case "surface": return "raise " + (a.issueCode || "?") + " (" + (a.issueSeverity || "warning") + ")";
    case "proposedOp": return "row-carried proposed action";
    default: return a.action || "";
  }
}

// unboundBindings lists the `row.<column>` references an action makes that no
// scanned row carries. Reported as unbound, not as broken: absence of evidence
// over an empty candidate set is not evidence of a missing column, which is why
// the caller words it "not observed in the scanned rows".
function unboundBindings(a) {
  var out = [];
  var list = (a && a.bindings) || [];
  for (var i = 0; i < list.length; i++) {
    if (!list[i].observed) out.push(list[i]);
  }
  return out;
}

// --- the target map layout ---------------------------------------------

var MAP_COL_X = [20, 210, 430, 660];
var MAP_NODE_W = [160, 190, 210, 250];
var MAP_ROW_H = 64;
var MAP_NODE_H = 44;
var MAP_TOP = 20;
// Approximate advance width of the node label/sub fonts (12px / 10px semibold
// in the console's stack). SVG text does not wrap or clip to its box, so a long
// label runs straight over the next column — a target and its like-named lens
// collided on the first live render. Truncation happens HERE, in the layout, so
// the geometry stays testable without a DOM.
var MAP_LABEL_CHAR_W = 6.9;
var MAP_SUB_CHAR_W = 5.6;
var MAP_NODE_PAD = 20;

// fitLabel truncates text to what fits in width, ellipsing when it cuts. Text
// that already fits comes back untouched.
function fitLabel(text, width, charW) {
  var s = String(text == null ? "" : text);
  var max = Math.floor((width - MAP_NODE_PAD) / charW);
  if (max < 1) return "";
  if (s.length <= max) return s;
  if (max <= 1) return "\u2026";
  return s.slice(0, max - 1) + "\u2026";
}

// mapLayout lays the target's definition out as four layers — lens → target →
// gap → what the gap dispatches — and returns plain geometry so the renderer
// stays a dumb SVG binding. Gaps drive the vertical extent; the lens and target
// nodes centre against them.
function mapLayout(detail) {
  var gaps = (detail && detail.gaps) || [];
  var rows = Math.max(gaps.length, 1);
  var height = MAP_TOP * 2 + rows * MAP_ROW_H;
  var mid = MAP_TOP + (rows * MAP_ROW_H) / 2 - MAP_NODE_H / 2;
  var nodes = [];
  var edges = [];

  var lensLabel = (detail && (detail.lensName || detail.lensRef)) || "(no lens ref)";
  nodes.push({
    id: "lens", kind: "lens", x: MAP_COL_X[0], y: mid, w: MAP_NODE_W[0], h: MAP_NODE_H,
    label: fitLabel(lensLabel, MAP_NODE_W[0], MAP_LABEL_CHAR_W),
    full: lensLabel,
    sub: "violation lens",
    cls: detail && detail.lensRef ? "" : "unbound",
    href: detail && detail.lensRef ? "#/lens/" + detail.lensRef : "",
  });
  var targetLabel = (detail && detail.targetId) || "";
  nodes.push({
    id: "target", kind: "target", x: MAP_COL_X[1], y: mid, w: MAP_NODE_W[1], h: MAP_NODE_H,
    label: fitLabel(targetLabel, MAP_NODE_W[1], MAP_LABEL_CHAR_W),
    full: targetLabel,
    sub: (detail && detail.state) || (detail && detail.registered ? "active" : "not registered"),
    cls: detail && detail.state === "disabled" ? "disabled" : "",
    href: "",
  });
  // Edges carry no labels: the column gap between two nodes is 20-30px, so a
  // label centred there is clipped by both boxes (it rendered as "jects ro" on
  // the first live pass). Both relations the labels carried are already on the
  // nodes themselves — the lens node's own "violation lens" subtitle, and the
  // planner's involvement in the action node's dispatch label.
  edges.push({ from: "lens", to: "target" });

  for (var i = 0; i < gaps.length; i++) {
    var g = gaps[i];
    var y = MAP_TOP + i * MAP_ROW_H;
    var gid = "gap:" + g.column;
    var gapSub = g.open + " open" + (g.inflight ? " · " + g.inflight + " in flight" : "");
    nodes.push({
      id: gid, kind: "gap", x: MAP_COL_X[2], y: y, w: MAP_NODE_W[2], h: MAP_NODE_H,
      label: fitLabel(g.column, MAP_NODE_W[2], MAP_LABEL_CHAR_W),
      full: g.column,
      sub: fitLabel(gapSub, MAP_NODE_W[2], MAP_SUB_CHAR_W),
      cls: gapNodeCls(g),
      href: "",
    });
    edges.push({ from: "target", to: gid });

    var aid = "act:" + g.column;
    var actLabel = dispatchLabel(g);
    var actSub = g.dispatch === "action" ? actionSummary(g.action) : "";
    nodes.push({
      id: aid, kind: "action", x: MAP_COL_X[3], y: y, w: MAP_NODE_W[3], h: MAP_NODE_H,
      label: fitLabel(actLabel, MAP_NODE_W[3], MAP_LABEL_CHAR_W),
      full: actLabel + (actSub ? " \u2014 " + actSub : ""),
      sub: fitLabel(actSub, MAP_NODE_W[3], MAP_SUB_CHAR_W),
      cls: g.dispatch === "none" ? "unbound" : "",
      href: g.dispatch === "action" && g.action && g.action.patternKnown ? "#/graph/" + g.action.patternRef : "",
    });
    edges.push({ from: gid, to: aid });
  }

  return { width: MAP_COL_X[3] + MAP_NODE_W[3] + 20, height: height, nodes: nodes, edges: edges };
}

// gapNodeCls picks a gap node's severity class. Budget exhaustion outranks
// open count: a gap with open rows is converging, a gap that has spent its
// budget has stopped.
function gapNodeCls(g) {
  if (g.exhausted > 0) return "bad";
  if (!g.observed) return "unbound";
  if (g.open > 0) return "warn";
  return "ok";
}

// --- entity roster + drill ---------------------------------------------

// entityBadges renders one candidate row's chips on the target map's roster.
function entityBadges(e) {
  var out = [];
  if (e.exhausted && e.exhausted.length) out.push({ text: e.exhausted.length + " exhausted", cls: "bad" });
  if (e.open && e.open.length) out.push({ text: e.open.length + " open", cls: "warn" });
  if (e.inflight && e.inflight.length) out.push({ text: e.inflight.length + " in flight", cls: "info" });
  if (!out.length) out.push({ text: e.violating ? "violating · no open gap" : "converged", cls: e.violating ? "warn" : "ok" });
  return out;
}

// rosterNote states what the entity list under a target map is, and whether
// the counts above it are totals. A truncated scan says so — a partial count
// presented as a total is the false-green this surface exists to avoid.
function rosterNote(detail) {
  var d = detail || {};
  var shown = ((d.entities || []).length);
  var base = shown + " of " + (d.rows || 0) + " candidate rows · " + (d.violating || 0) + " violating";
  if (d.truncated) return base + " — scan capped, counts are over the scanned rows only, not the whole target";
  return base;
}

// gapStateLine renders one gap's state in the entity drill: the state word, its
// class, and the budget line. A gap with no declared ceiling reports its
// dispatch count with no denominator rather than inventing one.
function gapStateLine(g) {
  var cls = { open: "warn", inflight: "info", exhausted: "bad", closed: "ok" }[g.state] || "muted";
  var budget = "";
  var n = g.dispatches || 0;
  // A closed gap that never dispatched has no episode to report a budget
  // against — the mark and its count are deleted on close, so "0 / 3" there is
  // a denominator against nothing rather than a fact about this entity.
  if (g.state === "closed" && !n) budget = "";
  else if (g.budgetKnown) budget = n + " / " + g.budget + " dispatches";
  else if (n) budget = n + " dispatches (no declared budget)";
  return { state: g.state, cls: cls, budget: budget };
}

// markLine renders an in-flight mark's lease facts. claimId is the open-episode
// identity the dispatched artifact's id is seeded on, so it is shown rather
// than hidden — it is the join an operator would otherwise have to make by
// hand.
function markLine(m) {
  if (!m) return null;
  return {
    action: String(m.action || "(unrecorded)"),
    claimId: String(m.claimId || ""),
    claimedAt: String(m.claimedAt || ""),
    leaseExpiresAt: String(m.leaseExpiresAt || ""),
    heldBy: String(m.heldBy || ""),
  };
}

// artifactLine labels the dispatched artifact a live mark points at. `live` is
// about the ENGINE's own state, not about the link: a Loom instance that has
// terminated while its gap stayed open is a real and interesting state, and
// the Chronicler's flow history outlives Loom's live record, so the link stays
// offered and the label carries the caveat.
function artifactLine(a) {
  if (!a) return null;
  var kind = a.kind === "flow" ? "Loom instance" : "task";
  return {
    kind: kind,
    id: String(a.id || ""),
    href: String(a.href || ""),
    live: !!a.live,
    note: a.live
      ? "live in the engine"
      : "not in the engine's live state — it has terminated, or this id derivation no longer matches the engine's",
  };
}

// --- F25.2 Verify -------------------------------------------------------

// checksSummary states the Checks panel headline, exception-first: no
// findings reads as an explicit clean pass, never as silence about nothing
// having run. "blocking" counts severity "bad" — an install-time reject the
// engine would actually enforce, versus a "warn" advisory/observed finding.
function checksSummary(checks) {
  var list = checks || [];
  if (!list.length) return "no issues found by the static / install-verdict checks";
  var v1 = 0, v2 = 0, bad = 0;
  for (var i = 0; i < list.length; i++) {
    if (list[i].tier === "v2") v2++; else v1++;
    if (list[i].severity === "bad") bad++;
  }
  var parts = [list.length + (list.length === 1 ? " check flagged" : " checks flagged")];
  if (bad) parts.push(bad + " blocking");
  parts.push(v1 + " structural");
  if (v2) parts.push(v2 + " install-verdict");
  return parts.join(" · ");
}

// opCoverageNote states V3's honesty framing plainly: interference can only
// ever be found among ops that declare `.effects`, so the corpus's coverage
// is shown alongside every finding rather than implied by its absence.
function opCoverageNote(cov) {
  var c = cov || {};
  var referenced = c.referencedOps || 0;
  var declared = c.declaredEffectsOps || 0;
  var unanalyzable = (c.unanalyzableOps || []).length;
  if (!referenced) return "no installed target dispatches an operation-bound action";
  var note = declared + " of " + referenced + " referenced ops declare .effects";
  if (unanalyzable) note += " — " + unanalyzable + " unanalyzable (advisory nudge to declare them)";
  return note;
}

// interferenceHeadline states the V3 join's result count, or its clean-pass
// absence — the runtime oscillation detector stays authoritative either way.
function interferenceHeadline(list) {
  var n = (list || []).length;
  if (!n) return "no aspect path is asserted by two or more installed targets' declared effects";
  return n + (n === 1 ? " aspect path" : " aspect paths") + " asserted by two or more targets — advisory, not a gate";
}

// rejectedIssueLabel names the vertex a TargetRejected issue belongs to: its
// current targetId when that vertex is (again) registered, or the bare meta
// vertex id when it is not — the message names the vertex, never the
// targetId a rejected spec merely claimed.
function rejectedIssueLabel(iss) {
  var i = iss || {};
  if (i.targetId) return i.targetId + " (vtx.meta." + i.metaId + ")";
  return "vtx.meta." + (i.metaId || "?") + " — not currently registered under any targetId";
}

// editAffordance shapes the target map's editContext into the "Edit with AI"
// control: whether it is live, where it goes, and the sentence to show beside
// it. Null only when the document carries no editContext at all — an id that
// resolves to no meta-vertex, where there is nothing an edit could name.
//
// A target the console cannot edit still renders the control, disabled with its
// reason on screen. The reason is the whole point: it states the apply-time
// refusal before a model call is spent on a proposal that could never land, and
// an affordance that quietly disappears leaves an operator to guess at a rule
// nobody told them about.
//
// An ENABLED control is the server's editContext saying it found nothing in the
// way, which is not the same as the platform agreeing — that verdict is taken
// again on submit, from a different read. So the live title says the platform
// decides rather than promising the draft.
function editAffordance(d) {
  var ec = d && d.editContext;
  if (!ec) return null;
  var targetId = (d && d.targetId) || "";
  if (ec.editable) {
    return {
      enabled: true,
      href: "#/weaver/author?edit=" + encodeURIComponent(targetId),
      title: "describe a change to " + targetId + " in plain language — the platform decides on submit, " +
        "and an accepted request lands an AI draft in the review queue",
      reason: "",
    };
  }
  var reason = ec.reason || "this target cannot be re-described from the console";
  return { enabled: false, href: "", title: reason, reason: reason };
}

export { fitLabel, contractionLabel, targetRank, targetRows, rosterHeadline, gapBadges, dispatchLabel, actionSummary, unboundBindings, mapLayout, gapNodeCls, entityBadges, rosterNote, gapStateLine, markLine, artifactLine, checksSummary, opCoverageNote, interferenceHeadline, rejectedIssueLabel, editAffordance };
