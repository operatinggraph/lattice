// Pure status lookups + rollup/strip shaping shared by the shell, the map,
// and the component/lens pages — the §4 status vocabulary's render side. The
// lens states are server-derived (renderedState); these tables only map them
// to visuals. No DOM, no fetch.

// componentStatusClass maps a component/client status to the CSS class that
// drives its color. Unknown statuses fall back to a neutral dot.
var componentStatusClass = {
  green: "green", stale: "stale", absent: "absent", unknown: "unknown",
  degraded: "yellow", unhealthy: "red",
};

// lensStateDot / lensStateGlyph render a lens's renderedState. pending-readpath
// deliberately uses the accent (informational) family, not yellow — it is the
// expected fail-closed state of a protected lens before its out-of-band
// read-path verify, not degradation. Color always pairs with a glyph or tag.
var lensStateDot = {
  fault: "red", paused: "yellow", "pending-readpath": "accent",
  rebuilding: "yellow", lagging: "yellow", projecting: "green", unknown: "dim",
};

var lensStateGlyph = { fault: "⚠", paused: "⏸", rebuilding: "⟳" };

// The operator copy for the pending-readpath state, shared by tips + rosters.
var pendingReadpathCopy = "awaiting read-path provisioning (out-of-band verify)";

// issueClass colors a flattened "[severity] code: message" issue line: an
// [error] line is red, everything else (warnings, stale notes) stays yellow.
function issueClass(text) {
  return /^\[error\]/.test(text) ? "card-issue bad" : "card-issue";
}

// alertLineClass colors a verbatim "[severity] key: message" alert-strip line.
function alertLineClass(text) {
  return /^\[error\]/.test(text) ? "alertstrip-line bad" : "alertstrip-line warn";
}

// shapeAlertLines orders the global strip's lines from a /api/health body:
// the red bootstrap-incomplete line first (when the kernel-seed marker is
// absent), then the alert lines verbatim, errors before warnings (stable
// within each). An empty result hides the strip.
function shapeAlertLines(health) {
  var lines = [];
  if (health && health.bootstrap === false) {
    lines.push({
      text: "bootstrap incomplete — kernel seed not verified (make up)",
      cls: "alertstrip-line bad",
    });
  }
  var alerts = (health && health.alerts) || [];
  var errs = [], warns = [];
  for (var i = 0; i < alerts.length; i++) {
    var entry = { text: alerts[i], cls: alertLineClass(alerts[i]) };
    if (/^\[error\]/.test(alerts[i])) { errs.push(entry); } else { warns.push(entry); }
  }
  return lines.concat(errs, warns);
}

// sysmapSummary counts the map banner's plain-English rollup. healthy covers
// every green-family status; a pending-readpath lens is counted separately
// and NEVER as degraded (the "7 degraded" fix); absent/unhealthy also count
// into degraded so the yellow line's total matches what the eye finds.
function sysmapSummary(nodes) {
  var healthy = { green: 1, present: 1, projecting: 1 };
  var out = { absent: 0, unhealthy: 0, degraded: 0, pending: 0 };
  var list = nodes || [];
  for (var i = 0; i < list.length; i++) {
    var st = list[i].status || "";
    if (st === "pending-readpath") { out.pending++; continue; }
    if (st === "absent") out.absent++;
    if (st === "unhealthy") out.unhealthy++;
    if (!healthy[st]) out.degraded++;
  }
  return out;
}

// sysmapTier derives a node's tier (0..4) from its kind + id, never hardcoded
// x/y — so the layout survives backend node-set changes.
function sysmapTier(node) {
  if (node.kind === "lens") return 4;
  if (node.kind === "infra") {
    return node.id === "core-operations" ? 0 : 2; // core-kv / core-events = spine
  }
  // component
  return node.id === "processor" ? 1 : 3;
}

export { componentStatusClass, lensStateDot, lensStateGlyph, pendingReadpathCopy, issueClass, alertLineClass, shapeAlertLines, sysmapSummary, sysmapTier };
