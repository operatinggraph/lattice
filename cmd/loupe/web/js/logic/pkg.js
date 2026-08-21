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
//   - secureColumnsErased: THIS run erased a lens whose committed secure
//     columns still held key-custody history from the destruction-readiness
//     oracle's view — warn. Unlike a retention holder, Uninstall never
//     protects a secure column, so there is no benign bucket for this concept
//     at all; every entry here carries an operator attestation, since the
//     server refuses the uninstall outright without one.
//   - secureColumnsAlreadyErased: the same shape of loss, for a lens the
//     oracle already could not see when this run read it — its vertex root
//     absent, not a `meta.lens`, or soft-deleted, or its spec an eventStream
//     source (B4: misattributing this to "secureColumnsErased" would blame a
//     run for damage that predates it) — warn, with wording that says "prior
//     run", not "this uninstall".
// The last two are kept as SEPARATE entries rather than merged into one
// count: merging them would be the exact misattribution bug this split
// exists to prevent.
//
//   - lensesTombstonedWithUnusableSpec: a meta.lens root tombstoned while the
//     oracle could not USE its `.spec` — absent, undecodable, or carrying no
//     targetConfig — warn. The oracle is fail-closed on such a spec, so it
//     counted that lens for EVERY holder type and now counts it for none; no
//     package can produce this state.
//
// Two more lines close it out, both about the attestation the server demanded
// before it would erase anything: how many custody records left on the
// operator's word (plain — it is what they asked for, said out loud because
// the platform verified nothing about the ciphertext), and any attestation
// that matched no erasure (warn — that is the shape of a misspelled lens
// name, and a clean uninstall would otherwise read as confirmation it was
// right).
function uninstallResultLines(body) {
  var b = body || {};
  var preserved = b.retentionHoldersPreserved || [];
  var stranded = b.retentionHoldersAlreadyStranded || [];
  var secureErased = b.secureColumnsErased || [];
  var secureAlreadyErased = b.secureColumnsAlreadyErased || [];
  var attested = b.secureLensRetirementsAttested || 0;
  var unusedAttestations = b.secureLensRetirementsUnused || [];
  var unusableSpec = b.lensesTombstonedWithUnusableSpec || [];
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
      text: secureAlreadyErased.length + " secure lens(es) with committed key-custody history were ALREADY invisible to the " +
        "destruction-readiness oracle from a prior run — vertex root absent, not a meta.lens, or soft-deleted, or an eventStream spec. " +
        "Pre-existing platform damage, not caused by this uninstall; escalate: " +
        secureAlreadyErased.map(function (e) { return (e && (e.lens || e.key)) || ""; }).join(", "),
    });
  }
  if (unusableSpec.length) {
    lines.push({
      warn: true,
      text: unusableSpec.length + " meta.lens root(s) were tombstoned while the destruction-readiness oracle could not use their .spec " +
        "— absent, undecodable, or carrying no targetConfig. The oracle counted each of these for every holder type and now counts " +
        "them for none. No package can produce this state; escalate: " + unusableSpec.join(", "),
    });
  }
  if (attested) {
    lines.push({
      warn: false,
      text: attested + " secure-column key-custody record(s) retired on your attestation — the destruction-readiness oracle no longer " +
        "sees them, and the platform did not verify their ciphertext is gone.",
    });
  }
  if (unusedAttestations.length) {
    lines.push({
      warn: true,
      text: unusedAttestations.length + " attestation(s) matched no erasure in this uninstall — check the canonicalName against the one " +
        "the installed package declares: " + unusedAttestations.join(", "),
    });
  }
  return lines;
}

// uninstallAttestationPrompts reads a REFUSED uninstall reply and returns what
// the confirm modal must ask for: one prompt per Secure Lens whose key-custody
// record this uninstall would erase from the destruction-readiness oracle's
// view without an operator attestation. Empty for every other reply — a
// success, or a failure of any other kind — so the caller's whole test is
// "did this come back with prompts".
//
// It reads the server's `unattestedSecureLenses` FIELD, never the refusal
// prose. The message is for a human; the lens names, keys, columns and holder
// types the operator is attesting ABOUT are data, and parsing them back out of
// a sentence is the defect the typed server-side error exists to prevent.
//
// `resolvable` is false when the server could not read a lens's canonicalName
// (its `.canonicalName` aspect was unreadable). Such a lens cannot be attested
// from this modal — the attestation is keyed by the name the installed package
// declares, and the console has nothing to key it with — so the view shows the
// CLI remedy instead of an input that would submit a name nobody verified.
function uninstallAttestationPrompts(body) {
  var list = (body || {}).unattestedSecureLenses || [];
  var prompts = [];
  for (var i = 0; i < list.length; i++) {
    var e = list[i] || {};
    var lens = String(e.lens || "");
    prompts.push({
      lens: lens,
      key: String(e.key || ""),
      columns: e.columns || [],
      declaredColumns: e.declaredColumns || 0,
      holderTypes: e.holderTypes || [],
      resolvable: lens !== "",
    });
  }
  return prompts;
}

// attestationSubjectLine renders what the operator is attesting ABOUT for one
// prompt: the secure columns and the holder types whose ciphertext stays in the
// target store after this erasure.
//
// The declared count is shown whenever it exceeds the number of columns that
// can be named, because those two differ exactly when a committed spec carries
// an entry with no column name — and a line reading "secure column(s): —"
// beside "holder type(s): —" names no subject at all, which is an attestation
// nobody can give.
function attestationSubjectLine(prompt) {
  var p = prompt || {};
  var columns = p.columns || [];
  var holders = p.holderTypes || [];
  var declared = p.declaredColumns || 0;
  var names = columns.length ? columns.join(", ") : "none could be named";
  var count = declared > columns.length ? declared + " declared secure column(s), " : "";
  return count + "secure column(s): " + names +
    " · holder type(s): " + (holders.length ? holders.join(", ") : "none declared");
}

// uninstallRetryBody builds the second POST from the operator's typed notes —
// notes[i] answering prompts[i] — or null when the modal is not ready to
// submit, which is the view's "keep the button disabled" test.
//
// Not ready means: no prompts at all (there is nothing to re-submit), a note
// left blank or all-whitespace (the Note is the whole content of an
// attestation — an empty one attests nothing and the server refuses it), or a
// lens whose canonicalName the server could not resolve (see
// uninstallAttestationPrompts). One rule, one place, so the disabled state and
// the payload can never disagree.
function uninstallRetryBody(name, prompts, notes) {
  var list = prompts || [];
  var typed = notes || [];
  if (!list.length) return null;
  var retired = [];
  for (var i = 0; i < list.length; i++) {
    if (!list[i].resolvable) return null;
    var note = String(typed[i] || "").trim();
    if (!note) return null;
    retired.push({ lens: list[i].lens, note: note });
  }
  return { name: name, retiredSecureLenses: retired };
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

export { manifestCandidate, applySummaryLine, uninstallSummary, uninstallResultLines, uninstallAttestationPrompts, attestationSubjectLine, uninstallRetryBody };
