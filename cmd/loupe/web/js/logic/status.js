// Pure status lookups + rollup/strip shaping shared by the shell, the map,
// and the component/lens pages — the §4 status vocabulary's render side. The
// lens states are server-derived (renderedState); these tables only map them
// to visuals. No DOM, no fetch.

// componentStatusClass maps a component/client status to the CSS class that
// drives its color. Unknown statuses fall back to a neutral dot. design-ahead
// (surface built, backend not yet deployed) uses the accent (informational)
// family — the component analog of a pending-readpath lens, never red.
// offline (a F14 declared app with no heartbeat) is the plain dim family —
// verticals are optional workloads, never absent-red.
var componentStatusClass = {
  green: "green", stale: "stale", absent: "absent", unknown: "unknown",
  degraded: "yellow", unhealthy: "red", "design-ahead": "designahead", offline: "dim",
};

// The operator copy for a design-ahead node's hover tip, plus the per-component
// pointer that tells the roadmap instead of alarming about it.
var designAheadCopy = "design-ahead — surface built, backend not yet deployed";
var designAheadPointer = {
  gateway: "Gateway: the external write-path door — behind the up-full deploy (lattice lane)",
  vault: "Vault: crypto-shred key custody — behind the Lattice-lane Vault build",
  chronicler: "Chronicler: append-only history — behind the Lattice-lane Chronicler build",
};

// appPointerCopy is the curated hover copy for a F14 declared-app door-band
// node — the migration story: today's direct-submit wart vs the ratified
// Gateway end-state (gateway-external-trust-boundary-design.md F5).
var appPointerCopy = "product front-end — verifies user JWTs for reads (RLS); today submits ops directly to core-operations (self-asserted actor — known wart); end-state routes user writes through the Gateway's strip-and-stamp front (gateway design F5).";

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
// and NEVER as degraded (the "7 degraded" fix); a design-ahead component
// likewise gets its own informational bucket; absent/unhealthy also count
// into degraded so the yellow line's total matches what the eye finds.
function sysmapSummary(nodes) {
  var healthy = { green: 1, present: 1, projecting: 1 };
  var out = { absent: 0, unhealthy: 0, degraded: 0, pending: 0, designAhead: 0 };
  var list = nodes || [];
  for (var i = 0; i < list.length; i++) {
    var st = list[i].status || "";
    if (st === "pending-readpath") { out.pending++; continue; }
    if (st === "design-ahead") { out.designAhead++; continue; }
    if (st === "offline") continue; // F14 declared app, no heartbeat — zero rollup contribution
    if (st === "absent") out.absent++;
    if (st === "unhealthy") out.unhealthy++;
    if (!healthy[st]) out.degraded++;
  }
  return out;
}

// sysmapTier derives a node's tier (-1..4) from its kind + id, never hardcoded
// x/y — so the layout survives backend node-set changes. Tier -1 is the door
// band: the external-actors marker on its own line, the Gateway + F14
// declared apps (clinic-app, loftspace-app) on the doors line under it, above
// core-operations. object-store is the archive sink, bottom band with the
// read-models.
function sysmapTier(node) {
  if (node.kind === "ingress") return -1;
  if (node.kind === "app") return -1;
  if (node.kind === "lens") return 4;
  if (node.kind === "infra") {
    if (node.id === "object-store") return 4;
    return node.id === "core-operations" ? 0 : 2; // core-kv / core-events = spine
  }
  // component — the id checks are kind-scoped so a stray non-component node
  // that happens to carry a component's id can never claim its placement.
  // (vault falls through to 3 by dependency depth — its lateral beside-Core-KV
  // placement is a render-layer concern, not a tier)
  if (node.id === "gateway") return -1; // the door, above the spine
  return node.id === "processor" ? 1 : 3;
}

// lensSeverity ranks a lens renderedState for a F14 cluster header's
// worst-of dot — informational/degraded states outrank the healthy
// "projecting" default, so one sick lens surfaces its whole card even while
// most chips inside are collapsed (exception-first density).
var lensSeverity = {
  fault: 5, paused: 4, rebuilding: 4, lagging: 3, "pending-readpath": 2,
  unknown: 1, projecting: 0,
};

// groupLenses buckets the map's lens nodes by their server-stamped pkg field
// (loupe-map-scale-ux.md §1) into one card model per group, sorted by group
// name: worst-of status (the header dot), total count, protected count, and
// every member chip in server order (label-sorted) — so the view's
// exception-first density rule (only non-"projecting" chips render, the rest
// collapse into "+N projecting") is a plain filter over pure, goja-tested
// data, not a fresh pass over raw nodes.
function groupLenses(nodes) {
  var groups = {};
  var order = [];
  var list = nodes || [];
  for (var i = 0; i < list.length; i++) {
    var n = list[i];
    if (n.kind !== "lens") continue;
    var key = n.pkg || "kernel";
    if (!groups[key]) {
      groups[key] = { group: key, pkgKey: n.pkgKey || "", worst: "projecting", count: 0, protected: 0, chips: [] };
      order.push(key);
    }
    var g = groups[key];
    g.count++;
    if (n.protected) g.protected++;
    g.chips.push(n);
    var sev = lensSeverity[n.status] || 0;
    if (sev > (lensSeverity[g.worst] || 0)) g.worst = n.status;
  }
  var out = [];
  for (var j = 0; j < order.length; j++) out.push(groups[order[j]]);
  out.sort(function (a, b) { return a.group < b.group ? -1 : a.group > b.group ? 1 : 0; });
  return out;
}

export { appPointerCopy, componentStatusClass, designAheadCopy, designAheadPointer, lensStateDot, lensStateGlyph, pendingReadpathCopy, issueClass, alertLineClass, shapeAlertLines, sysmapSummary, sysmapTier, groupLenses };
