// F25.3a — Author: pure decision logic for the draft target/lens editor
// (weaver-target-studio-design.md §6, steps 1-3). Dependency-free: no DOM, no
// imports (the goja logic-file convention) — views/weaverauthor.js binds this
// to the form.
//
// A draft is a plain object the view owns: { targetId, lensRef, gaps: {
// <col>: { action, pattern, subject, adapter, operation, assignee, target,
// issueCode, issueSeverity, paramsText, readsText } }, lens: { canonicalName,
// adapter, bucket, table, spec } }. paramsText/readsText are the textarea/
// input strings the operator types; buildTargetContent parses them into the
// artifact's params/reads shape at build time, so the draft's OWN shape can
// stay simple strings throughout editing.

function emptyDraft() {
  return { targetId: "", lensRef: "", gaps: {}, lens: emptyLens(), rationale: "" };
}

function emptyGap() {
  return {
    action: "", pattern: "", subject: "", adapter: "", operation: "",
    assignee: "", target: "", issueCode: "", issueSeverity: "",
    paramsText: "", readsText: "",
  };
}

function emptyLens() {
  return { canonicalName: "", adapter: "nats-kv", bucket: "weaver-targets", table: "", spec: "" };
}

// parseParamsText parses a "key=value" per-line textarea into an object.
// Blank lines and lines without "=" are skipped rather than thrown — the
// server-side pkgmgr validator is the source of truth for what a malformed
// params map means; this is just a lossless text<->object round trip.
function parseParamsText(text) {
  const out = {};
  (text || "").split("\n").forEach((line) => {
    const i = line.indexOf("=");
    if (i < 0) return;
    const k = line.slice(0, i).trim();
    const v = line.slice(i + 1).trim();
    if (k) out[k] = v;
  });
  return out;
}

// paramsToText is parseParamsText's inverse, key-sorted for a stable
// round-trip (an object's own key order is not meaningful here).
function paramsToText(params) {
  const keys = Object.keys(params || {}).sort();
  return keys.map((k) => k + "=" + params[k]).join("\n");
}

// parseReadsText splits a comma/whitespace-separated reads list, trimming and
// dropping empties — mirrors logic/reads.js's deriveReads tokenizing spirit
// without importing it (logic files are dependency-free).
function parseReadsText(text) {
  return (text || "").split(/[\s,]+/).map((s) => s.trim()).filter(Boolean);
}

function readsToText(reads) {
  return (reads || []).join(", ");
}

// gapActionArtifact converts one draft gap into the wire shape
// pkgmgr.GapActionArtifact expects, omitting empty optional fields (the
// server-side unknownWeaverTargetFields/knownGapActionFields check treats an
// extra key as a validation failure, never an empty one — but a clean
// artifact is also just easier to read on export).
function gapActionArtifact(g) {
  const out = { action: g.action || "" };
  const strFields = ["pattern", "subject", "adapter", "operation", "assignee", "target", "issueCode", "issueSeverity"];
  strFields.forEach((f) => { if (g[f]) out[f] = g[f]; });
  const params = parseParamsText(g.paramsText);
  if (Object.keys(params).length) out.params = params;
  const reads = parseReadsText(g.readsText);
  if (reads.length) out.reads = reads;
  return out;
}

// buildTargetContent builds the pkgmgr.WeaverTargetArtifactContent-shaped
// object a draft's target fields describe — exactly what
// POST /api/weaver/author/check's `target` field and the export bundle's
// weaverTarget artifact both carry.
function buildTargetContent(draft) {
  const gaps = {};
  Object.keys(draft.gaps || {}).sort().forEach((col) => {
    gaps[col] = gapActionArtifact(draft.gaps[col]);
  });
  return { targetId: draft.targetId || "", lensRef: draft.lensRef || "", gaps };
}

// buildLensContent builds the pkgmgr.LensArtifactContent-shaped object.
function buildLensContent(draft) {
  const l = draft.lens || emptyLens();
  return {
    canonicalName: l.canonicalName || "",
    adapter: l.adapter || "nats-kv",
    bucket: l.bucket || "",
    table: l.table || "",
    spec: l.spec || "",
  };
}

// scaffoldLensSpec generates a starting cypher template for the paired
// violation lens (§6 step 1's "the §10.2 conventions scaffolded"). A plain
// (non-actorAggregate) nats-kv lens needs no Output descriptor — the RETURN's
// own `AS key` column becomes the literal storage key when the lens declares
// no IntoKey (pkgmgr's nats-kv default, build.go), so the composite
// `<targetId>.<entityId>` key the §10.2 row convention requires is produced
// directly in the RETURN, not via any field this restricted artifact kind
// lacks. gapKeys should be sorted for a deterministic template.
function scaffoldLensSpec(targetId, gapKeys) {
  const id = targetId || "<targetId>";
  const missingCols = gapKeys.length
    ? gapKeys.map((g) => "missing_" + g).join(" OR ")
    : "<at least one missing_<gap> expression>";
  const lines = [];
  lines.push("// Scaffold — §10.2 convergence row over one candidate entity per match.");
  lines.push("// Fill in the MATCH/WHERE for this target's candidate set, then the");
  lines.push("// missing_<gap> boolean expression for each declared gap.");
  lines.push("MATCH (e:<entityType>)");
  lines.push("WHERE <candidacy filter>");
  lines.push("RETURN '" + id + ".' + nanoIdFromKey(e.key) AS key,");
  lines.push("       (" + missingCols + ") AS violating,");
  (gapKeys.length ? gapKeys : ["<gap>"]).forEach((g, i) => {
    const sep = i === gapKeys.length - 1 || !gapKeys.length ? "" : ",";
    lines.push("       <missing " + g + " expr> AS missing_" + g + sep);
  });
  lines.push("       e.key AS entityKey");
  return lines.join("\n");
}

// validationBadge summarizes a {valid, errors} verdict for a chip.
function validationBadge(report) {
  if (!report) return { text: "not checked", cls: "muted" };
  if (report.valid) return { text: "valid", cls: "ok" };
  const n = (report.errors || []).length;
  return { text: n + " error" + (n === 1 ? "" : "s"), cls: "bad" };
}

// exportBundle builds the two-artifact JSON document Export downloads — the
// shape a future F25.3b SubmitCapabilityProposal call needs per artifact
// (minus proposalId, minted at propose time): {kind, content, target,
// rationale, validation}. content is a JSON STRING (the wire shape
// SubmitCapabilityProposal's own `content` field carries, design §6.4 point
// 1), not a nested object.
function exportBundle(draft, checkResult, rationale, targetMode) {
  const targetContent = buildTargetContent(draft);
  const lensContent = buildLensContent(draft);
  const tv = (checkResult && checkResult.targetValidation) || { valid: false, errors: [] };
  const lv = (checkResult && checkResult.lensValidation) || { valid: false, errors: [] };
  return {
    artifacts: [
      {
        kind: "weaverTarget",
        content: JSON.stringify(targetContent),
        target: { mode: targetMode || "install" },
        rationale: rationale || "",
        validation: { state: tv.valid ? "valid" : "invalid", report: (tv.errors || []).join("; ") },
      },
      {
        kind: "lens",
        content: JSON.stringify(lensContent),
        target: { mode: targetMode || "install" },
        rationale: rationale || "",
        validation: { state: lv.valid ? "valid" : "invalid", report: (lv.errors || []).join("; ") },
      },
    ],
  };
}

// exportFilename derives a stable download name from the draft's targetId.
function exportFilename(targetId) {
  const safe = (targetId || "draft").replace(/[^A-Za-z0-9_-]+/g, "-");
  return "weaver-target-" + safe + ".json";
}

export { emptyDraft, emptyGap, emptyLens, parseParamsText, paramsToText, parseReadsText, readsToText, buildTargetContent, buildLensContent, scaffoldLensSpec, validationBadge, exportBundle, exportFilename };
