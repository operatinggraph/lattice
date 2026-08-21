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
  // The inverse of the line above, and the one that actually removes custody
  // history: the package DECLARED these erasures on its own attestation, and
  // the platform verified nothing about the ciphertext. On a dry run this
  // rides under the same "preview —" prefix as every other count on this
  // line, so it reads as hypothetical rather than as already landed.
  if (res.secureColumnsRetired) {
    delta += " · " + res.secureColumnsRetired + " secure-column key-custody record(s) retired";
  }
  // Advisory, not custody loss and not an error: a declared retirement this
  // run matched to no actual erasure is stale housekeeping the package is
  // still carrying (an edit that already landed in an earlier version, or a
  // typo in Lens/Column), worded lighter than the counts above it on purpose
  // — "unused" reads as hygiene to notice, not as something that went wrong.
  if (res.secureColumnRetirementsUnused && res.secureColumnRetirementsUnused.length) {
    delta += " · " + res.secureColumnRetirementsUnused.length + " declared retirement(s) unused";
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
// uninstallResultLines turns a POST /api/packages/uninstall reply into the
// ordered list of {warn, text} lines the confirm modal renders after the
// call returns — pure, so the exact same classification (which population
// reads as benign, which reads as escalate-this) is goja-testable without a
// DOM. `warn: true` is the "escalate this" class (rendered "warn-text");
// `warn: false` is plain.
//
// Four populations, in the order the operator should read them:
//   - retentionHoldersPreserved: a DELIBERATE, benign hold-back (only
//     ShredRetentionClassKey may destroy the class key) — plain.
//   - retentionHoldersAlreadyStranded: pre-existing damage this uninstall did
//     not cause and cannot undo — warn.
//   - secureColumnsErased: THIS run tombstoned a lens whose committed secure
//     columns still held key-custody history — warn. Unlike a retention
//     holder, Uninstall never protects a secure column, so there is no
//     benign bucket for this concept at all.
//   - secureColumnsAlreadyErased: the same shape of loss, but the spec was
//     ALREADY tombstoned before this run read it (B4: misattributing this to
//     "secureColumnsErased" would blame a run for damage that predates it) —
//     warn, with wording that says "prior run", not "this uninstall".
// The last two are kept as SEPARATE entries rather than merged into one
// count: merging them would be the exact misattribution bug this split
// exists to prevent.
function uninstallResultLines(body) {
  var b = body || {};
  var preserved = b.retentionHoldersPreserved || [];
  var stranded = b.retentionHoldersAlreadyStranded || [];
  var secureErased = b.secureColumnsErased || [];
  var secureAlreadyErased = b.secureColumnsAlreadyErased || [];
  var lines = [];
  if (preserved.length) {
    lines.push({
      warn: false,
      text: preserved.length + " retention-class holder key(s) preserved (not tombstoned) — " +
        "still destroyable via ShredRetentionClassKey: " + preserved.join(", "),
    });
  }
  if (stranded.length) {
    lines.push({
      warn: true,
      text: stranded.length + " retention-class holder key(s) are ALREADY tombstoned from a prior run — " +
        "their class key can never be destroyed by ShredRetentionClassKey. Pre-existing platform damage, not caused by this uninstall; escalate: " +
        stranded.join(", "),
    });
  }
  if (secureErased.length) {
    lines.push({
      warn: true,
      text: secureErased.length + " secure lens(es) whose committed secure columns still held key-custody history were tombstoned — " +
        "the destruction-readiness oracle no longer sees them; ciphertext those columns encrypted stays in the target store: " +
        secureErased.map(function (e) { return (e && (e.lens || e.key)) || ""; }).join(", "),
    });
  }
  if (secureAlreadyErased.length) {
    lines.push({
      warn: true,
      text: secureAlreadyErased.length + " secure lens(es) with committed key-custody history were ALREADY tombstoned from a prior run — " +
        "the destruction-readiness oracle already could not see them. Pre-existing platform damage, not caused by this uninstall; escalate: " +
        secureAlreadyErased.map(function (e) { return (e && (e.lens || e.key)) || ""; }).join(", "),
    });
  }
  return lines;
}

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

export { manifestCandidate, applySummaryLine, uninstallSummary, uninstallResultLines };
