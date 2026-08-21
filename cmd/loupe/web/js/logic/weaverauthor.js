// F25.3a — Author: pure decision logic for the draft target/lens editor
// (weaver-target-studio-design.md §6, steps 1-3). Dependency-free: no DOM, no
// imports (the goja logic-file convention) — views/weaverauthor.js binds this
// to the form.
//
// A draft is a plain object the view owns: { targetId, description, lensRef,
// gaps: { <col>: { action, pattern, subject, adapter, operation, assignee,
// target, issueCode, issueSeverity, paramsText, readsText } }, lens: {
// canonicalName, adapter, bucket, table, spec } }. paramsText/readsText are the
// textarea/ input strings the operator types; buildTargetContent parses them
// into the artifact's params/reads shape at build time, so the draft's OWN
// shape can stay simple strings throughout editing.

function emptyDraft() {
  return { targetId: "", description: "", lensRef: "", gaps: {}, lens: emptyLens(), rationale: "" };
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
// weaverTarget artifact both carry. `description` appears only when the
// operator actually typed prose (trimmed), so a description-less draft keeps
// the key-for-key shape the Go side's omitempty tag produces.
function buildTargetContent(draft) {
  const gaps = {};
  Object.keys(draft.gaps || {}).sort().forEach((col) => {
    gaps[col] = gapActionArtifact(draft.gaps[col]);
  });
  const content = { targetId: draft.targetId || "", lensRef: draft.lensRef || "", gaps };
  const description = (draft.description || "").trim();
  if (description) content.description = description;
  return content;
}

// hydrateFromProposal inverts buildTargetContent's shape — a capability
// proposal's own JSON-string artifact.content — back into a draft the Author
// form can render and re-edit ("Load into Author", design §3.4). Only a
// weaverTarget-kind proposal whose content decodes to a plausible object
// hydrates; any other kind, or content that fails to parse, returns null so
// the caller can show its own error rather than render a bogus draft. The
// lens panel is deliberately left untouched (emptyLens()) — the proposal
// bundle's separate "lens" artifact is not this function's input.
function hydrateFromProposal(row) {
  if (!row || row.kind !== "weaverTarget") return null;
  let parsed;
  try {
    parsed = JSON.parse(row.content);
  } catch (e) {
    return null;
  }
  if (!parsed || typeof parsed !== "object") return null;

  const gaps = {};
  const srcGaps = (parsed.gaps && typeof parsed.gaps === "object") ? parsed.gaps : {};
  Object.keys(srcGaps).forEach((col) => {
    const g = srcGaps[col] || {};
    gaps[col] = {
      action: g.action || "", pattern: g.pattern || "", subject: g.subject || "",
      adapter: g.adapter || "", operation: g.operation || "", assignee: g.assignee || "",
      target: g.target || "", issueCode: g.issueCode || "", issueSeverity: g.issueSeverity || "",
      paramsText: paramsToText(g.params || {}), readsText: readsToText(g.reads || []),
    };
  });

  const draft = emptyDraft();
  draft.targetId = parsed.targetId || "";
  draft.description = parsed.description || "";
  draft.lensRef = parsed.lensRef || "";
  draft.gaps = gaps;
  draft.lens = emptyLens();
  draft.rationale = row.rationale || "";
  return draft;
}

// draftHasLens reports whether the operator is authoring a NEW paired lens
// (a non-blank cypher spec) as opposed to binding the target to an
// already-installed lens by reference only (Studio apply-path fix — cold
// review of the "Load into Author" round trip: a hydrated AI draft binds an
// existing lens and has no lens artifact of its own, weaver-target-
// studio-design.md §6.4). Gates both the Check request's lens field and the
// propose/export bundle's artifact count — a blank spec can never produce a
// valid lens artifact, so treating it as "no lens intended" rather than
// "invalid lens forever" is the only reading that lets a target-only draft
// ever propose.
function draftHasLens(draft) {
  return !!((draft && draft.lens && draft.lens.spec) || "").trim();
}

// proposeBlockers lists, in the order an operator should fix them, why Propose
// is not available yet. Check verdicts are one reason; a missing description is
// the other — the server refuses a described-less weaverTarget outright
// (weaverauthor.go's proposedTargetDescription), so the button must not offer a
// submission the server will reject. Check itself stays shape-only, so a draft
// can be validated long before it has prose. A target-only draft (no lens
// being authored) is never blocked on a lens verdict it never asked for —
// buildLensContent's empty spec would otherwise read "lens is not valid"
// forever, permanently disabling Propose for exactly the draft this fire
// exists to unblock.
function proposeBlockers(draft, checkResult) {
  const out = [];
  const r = checkResult;
  const hasLens = draftHasLens(draft);
  if (!r) out.push("run checks first");
  else {
    if (!(r.targetValidation && r.targetValidation.valid)) out.push("target artifact is not valid");
    if (hasLens && !(r.lensValidation && r.lensValidation.valid)) out.push("lens artifact is not valid");
  }
  if (!((draft && draft.description) || "").trim()) out.push("describe what this target ensures");
  return out;
}

// buildLensContent builds the pkgmgr.LensArtifactContent-shaped object.
// canonicalName defaults to the target's lensRef when the operator has not
// typed one — the field the view itself displays as a placeholder default,
// kept in lockstep so Check/Export never diverge from what's shown on screen.
function buildLensContent(draft) {
  const l = draft.lens || emptyLens();
  return {
    canonicalName: l.canonicalName || draft.lensRef || "",
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
// lacks. gapKeys are the target's OWN gaps map keys — already full
// `missing_<gap>` column names (pkgmgr's WeaverTarget gaps-key convention,
// orchestrationguard.go: "gaps key %q does not match the missing_<gap> column
// convention" — the key is the column, not a bare suffix this template would
// need to re-prefix), sorted for a deterministic template.
function scaffoldLensSpec(targetId, gapKeys) {
  const id = targetId || "<targetId>";
  const missingCols = gapKeys.length ? gapKeys.join(" OR ") : "<at least one missing_<gap> expression>";
  const lines = [];
  lines.push("// Scaffold — §10.2 convergence row over one candidate entity per match.");
  lines.push("// Fill in the MATCH/WHERE for this target's candidate set, then the");
  lines.push("// boolean expression for each declared gap column.");
  lines.push("MATCH (e:<entityType>)");
  lines.push("WHERE <candidacy filter>");
  lines.push("RETURN '" + id + ".' + nanoIdFromKey(e.key) AS key,");
  lines.push("       (" + missingCols + ") AS violating,");
  (gapKeys.length ? gapKeys : ["<missing_gap>"]).forEach((g, i) => {
    const sep = i === gapKeys.length - 1 || !gapKeys.length ? "" : ",";
    lines.push("       <" + g + " expr> AS " + g + sep);
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

// applyPackagePrefix namespaces a Studio-proposed artifact's derived
// packageName by kind, so a co-authored {target+lens} bundle's two
// artifacts — each its own independent apply, SubmitCapabilityProposal
// mints one proposal per artifact (cmd/loupe/weaverauthor.go) — never derive
// the SAME packageName and collide with each other at apply time
// (internal/pkgmgr/capabilityapply.go's newPackage mode refuses an
// already-installed name, and the first of the pair to apply would install
// exactly that name out from under the second).
const applyPackagePrefix = { weaverTarget: "weaver-target", lens: "weaver-lens" };

// buildApplyTarget builds the {mode, packageName, newVersion} a Studio
// artifact proposes for internal/pkgmgr/capabilityapply.go's apply step
// (Studio apply-path fix — cold review of NL-2's "Load into Author" feature,
// which depends on apply actually working): always mode "newPackage" (the
// Studio never targets an existing package for upgrade — that is a
// different, not-yet-built operator workflow), with a packageName derived
// from the draft's targetId plus a caller-supplied freshness token, so
// re-proposing the SAME targetId (e.g. an edited re-propose after "Load into
// Author") never collides with a package name a PRIOR apply already
// installed — newPackage fails closed against an already-installed name
// (capabilityapply.go:186-189), and packageName is never shown to the
// operator, so there is no readability cost to making it fresh every time.
//
// fresh is never generated in here — the view supplies a real unique token
// per propose/export click — so this stays pure and deterministic for goja.
// The "weaver-target-"/"weaver-lens-" prefix can never collide with a
// PlatformProtectedPackage name (none of the twelve share it,
// capabilityapply.go's platformProtectedPackages), so that refusal can never
// fire here by construction — not merely by convention.
function buildApplyTarget(kind, targetId, fresh) {
  const prefix = applyPackagePrefix[kind] || "weaver-artifact";
  const slug = (targetId || "draft").toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-+|-+$/g, "") || "draft";
  const token = (fresh || "").toLowerCase().replace(/[^a-z0-9]+/g, "").slice(0, 12) || "0";
  return { mode: "newPackage", packageName: prefix + "-" + slug + "-" + token, newVersion: "0.1.0" };
}

// exportBundle builds the artifact bundle Export downloads / Propose submits
// — the shape SubmitCapabilityProposal needs per artifact (minus
// proposalId, minted at propose time): {kind, content, target, rationale,
// validation}. content is a JSON STRING (the wire shape
// SubmitCapabilityProposal's own `content` field carries, design §6.4 point
// 1), not a nested object.
//
// TARGET-ONLY (one artifact) when the draft binds an existing installed lens
// rather than authoring a new one (draftHasLens false — Studio apply-path
// fix, §L2): proposing an empty/placeholder lens artifact alongside it would
// just be a second doomed-to-be-invalid proposal nobody asked for. The
// existing two-artifact {target+lens} shape is preserved unchanged for the
// co-authoring case. fresh is threaded into buildApplyTarget for both
// artifacts so a single call derives a consistent, collision-avoiding pair.
function exportBundle(draft, checkResult, rationale, fresh) {
  const targetContent = buildTargetContent(draft);
  const tv = (checkResult && checkResult.targetValidation) || { valid: false, errors: [] };
  const artifacts = [
    {
      kind: "weaverTarget",
      content: JSON.stringify(targetContent),
      target: buildApplyTarget("weaverTarget", draft.targetId, fresh),
      rationale: rationale || "",
      validation: { state: tv.valid ? "valid" : "invalid", report: (tv.errors || []).join("; ") },
    },
  ];
  if (draftHasLens(draft)) {
    const lensContent = buildLensContent(draft);
    const lv = (checkResult && checkResult.lensValidation) || { valid: false, errors: [] };
    artifacts.push({
      kind: "lens",
      content: JSON.stringify(lensContent),
      target: buildApplyTarget("lens", draft.targetId, fresh),
      rationale: rationale || "",
      validation: { state: lv.valid ? "valid" : "invalid", report: (lv.errors || []).join("; ") },
    });
  }
  return { artifacts: artifacts };
}

// exportFilename derives a stable download name from the draft's targetId.
function exportFilename(targetId) {
  const safe = (targetId || "draft").replace(/[^A-Za-z0-9_-]+/g, "-");
  return "weaver-target-" + safe + ".json";
}

// buildAuthoringRequest builds POST /api/weaver/author/request's body from
// the Describe panel's own two fields — trims the intent and omits a blank
// contextRef — returning null when there is nothing to submit (a blank or
// whitespace-only intent), so the view can gate Submit on the exact same
// rule the server enforces.
function buildAuthoringRequest(text, contextRef) {
  const intent = (text || "").trim();
  if (!intent) return null;
  const payload = { intent: intent };
  const ref = (contextRef || "").trim();
  if (ref) payload.contextRef = ref;
  return payload;
}

export { emptyDraft, emptyGap, emptyLens, parseParamsText, paramsToText, parseReadsText, readsToText, buildTargetContent, buildLensContent, hydrateFromProposal, draftHasLens, proposeBlockers, scaffoldLensSpec, validationBadge, buildApplyTarget, exportBundle, exportFilename, buildAuthoringRequest };
