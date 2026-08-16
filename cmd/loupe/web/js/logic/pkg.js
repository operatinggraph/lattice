// Pure package-view logic: the upload manifest pick (the JS twin of the Go
// manifestFromUpload rule), the one-line apply-reply summary, and the
// uninstall-confirm summary. No DOM, no fetch — goja-tested via
// cmd/loupe/web_logic_test.go (strip-export load).

// manifestCandidate picks the manifest out of a selected file list by name:
// an exact manifest.yaml / manifest.yml wins (case-insensitive); a single
// file of any name is accepted; anything else is ambiguous (null).
function manifestCandidate(names) {
  var list = names || [];
  for (var i = 0; i < list.length; i++) {
    var n = String(list[i] || "").toLowerCase();
    if (n === "manifest.yaml" || n === "manifest.yml") return list[i];
  }
  if (list.length === 1) return list[0];
  return null;
}

// RETENTION_HOLDER_PREFIX is the key shape of a retention-class holder — the
// vtx.retentionclass.<NanoID> root and its .retentionPolicy aspect. The
// installer never submits one for tombstone (only ShredRetentionClassKey may
// destroy the class DEK it custodies, and that verb refuses a tombstoned
// holder), so any tombstone-count the UI shows must net these out.
var RETENTION_HOLDER_PREFIX = "vtx.retentionclass.";

// applySummaryLine renders an install/upgrade reply as one human line:
// "preview — upgrade 1.0.0 → 1.1.0 — 3 created · 2 updated". A retention-class
// holder the diff declined to tombstone rides on the same line: the count the
// package asked to remove and the platform deliberately did not.
function applySummaryLine(res) {
  if (!res) return "";
  var prefix = res.dryRun ? "preview — " : "";
  if (res.skipped) {
    return prefix + "skipped — " + (res.reason || "already installed at this version");
  }
  var counts = [];
  if (res.created) counts.push(res.created + " created");
  if (res.updated) counts.push(res.updated + " updated");
  if (res.tombstoned) counts.push(res.tombstoned + " tombstoned");
  var delta = counts.length ? counts.join(" · ") : "no changes";
  // Counts, not key lists — the apply reply names these fields with a Count
  // suffix precisely because the uninstall reply's same-named concept is a
  // list of keys.
  if (res.retentionHoldersPreservedCount) {
    delta += " · " + res.retentionHoldersPreservedCount + " retention-class holder key(s) preserved";
  }
  if (res.retentionHoldersAlreadyStrandedCount) {
    delta += " · " + res.retentionHoldersAlreadyStrandedCount + " retention-class holder key(s) ALREADY tombstoned";
  }
  // The other edit the platform declined to make. A narrowed holderTypes
  // declaration is refused, and when it is the whole diff for its lens the
  // apply reports no mutations at all — so without this the operator reads
  // "no changes" over an edit that was rejected.
  if (res.secureColumnsWidened) {
    delta += " · " + res.secureColumnsWidened + " secure column(s) kept their committed holderTypes";
  }
  if (res.action === "upgrade") {
    return prefix + "upgrade " + (res.fromVersion || "?") + " → " + (res.toVersion || "?") + " — " + delta;
  }
  if (res.action === "install") {
    return prefix + "install" + (res.toVersion ? " v" + res.toVersion : "") + " — " + delta;
  }
  return prefix + (res.action || "apply") + " — " + delta;
}

// uninstallSummary tells the operator what an uninstall will tombstone: every
// declared key that still resolves, plus the manifest aspect and the package
// vertex itself (the server appends both), minus unresolved declared keys
// (the server skips those) and minus every retention-class holder key, which
// the installer excludes from the tombstone set entirely. The per-kind
// breakdown counts declared ITEMS (aspects fold into their parent), so it
// reads as a contents summary, not a key count — holder items are netted out
// of their section's count for the same reason.
//
// A holder root the graph could not resolve is already netted out by
// `unresolved`, so only a found root contributes its own key here; its folded
// aspects are counted either way, since `unresolved` never counts them.
function uninstallSummary(pkg) {
  var p = pkg || {};
  var declared = p.declaredCount || 0;
  var unresolved = p.unresolved || 0;
  var preserved = 0;
  var parts = [];
  var list = p.sections || [];
  for (var i = 0; i < list.length; i++) {
    var items = list[i].items || [];
    var holders = 0;
    for (var j = 0; j < items.length; j++) {
      var it = items[j] || {};
      if (String(it.key || "").indexOf(RETENTION_HOLDER_PREFIX) !== 0) continue;
      holders++;
      preserved += (it.found ? 1 : 0) + (it.aspects || 0);
    }
    var count = (list[i].count || 0) - holders;
    if (count > 0) parts.push(count + " " + list[i].kind);
  }
  // + the manifest aspect + the package vertex, − the holder keys held back.
  var total = declared - unresolved - preserved + 2;
  var line = "tombstones up to " + total + " key(s) incl. the manifest + package vertex";
  if (parts.length) line += " — " + parts.join(" · ");
  if (unresolved) line += "; " + unresolved + " unresolved skipped";
  if (preserved) {
    line += "; " + preserved + " retention-class holder key(s) left untouched (only ShredRetentionClassKey may destroy them)";
  }
  return line;
}

export { manifestCandidate, applySummaryLine, uninstallSummary };
