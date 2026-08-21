// F25.3a — Author view: the draft target/lens editor bound onto weaver.js's
// "#/weaver/author" sub-route (weaver-target-studio-design.md §6, steps
// 1-3). A structured form (mirrors op.js's schema-driven field pattern) for
// gaps/actions plus a scaffolded cypher textarea for the paired violation
// lens; "Run checks" posts the draft to F25.2's checker + the pkgmgr
// validators; "Export" downloads the checked artifact bundle client-side —
// no platform write of any kind.
//
// F25.3b (§6 steps 4-5) adds Propose — POST /api/weaver/author/propose turns
// the same checked bundle into two SubmitCapabilityProposal ops, one per
// artifact, entering the review queue F16 already builds/reviews/applies —
// and the Trial panel, a guided wrapper around primitives that already exist
// elsewhere in the console rather than new platform surface: revoke/enable
// are the same generic /api/control/weaver/<id>/<op> relay component.js's
// Weaver control rows already call (§6 step 5a/5e), "watch" links to F25.1's
// own target map (§6 step 5d), and "seed fixture entities" links to the
// generic op console (§6 step 5c) — this view adds no new op-submission path
// for any of the three.

import { $, el, api, demoHide, setStatus } from "../api.js";
import {
  emptyDraft, emptyGap, parseParamsText, paramsToText, parseReadsText, readsToText,
  buildTargetContent, buildLensContent, proposeBlockers, scaffoldLensSpec, validationBadge,
  exportBundle, exportFilename,
} from "../logic/weaverauthor.js";
import { checksSummary, interferenceHeadline, opCoverageNote } from "../logic/weaver.js";

let draft = emptyDraft();
let lastCheck = null;
let proposeResults = null;

function enterAuthor() {
  draft = emptyDraft();
  lastCheck = null;
  proposeResults = null;
  render();
}

function render() {
  const box = $("#weaver-author");
  box.innerHTML = "";
  box.appendChild(el("p", "muted small",
    "Compose a target + its paired violation lens as a capability artifact — the same shape the AI-authored " +
    "lane validates and the Review console applies. Browser-local until exported or proposed; nothing here " +
    "writes any platform state until Propose."));
  box.appendChild(targetFieldsBox());
  box.appendChild(gapsBox());
  box.appendChild(lensBox());
  box.appendChild(actionsBox());
  if (lastCheck) box.appendChild(checkResultBox(lastCheck));
  if (proposeResults) box.appendChild(proposeResultBox(proposeResults));
  if (draft.targetId) box.appendChild(trialBox());
}

function labeledInput(labelText, value, onInput, opts) {
  const wrap = el("label", "op-field");
  wrap.appendChild(el("span", "op-field-name", labelText));
  const input = document.createElement((opts && opts.textarea) ? "textarea" : "input");
  if (opts && opts.textarea) input.rows = opts.rows || 3;
  else input.type = "text";
  input.value = value || "";
  if (opts && opts.placeholder) input.placeholder = opts.placeholder;
  input.addEventListener("input", () => onInput(input.value));
  wrap.appendChild(input);
  return wrap;
}

function targetFieldsBox() {
  const box = el("div", "weaver-panel author-fields");
  box.appendChild(el("h3", null, "Target"));
  const row = el("div", "op-fields author-row");
  row.appendChild(labeledInput("targetId", draft.targetId, (v) => { draft.targetId = v; }));
  row.appendChild(labeledInput("lensRef (the paired lens's canonicalName)", draft.lensRef, (v) => { draft.lensRef = v; }));
  box.appendChild(row);
  // Prose, not a posture: it installs onto the target as its own aspect and is
  // what the roster and the review queue label this target by, which is why
  // Propose requires it while Check does not.
  box.appendChild(labeledInput(
    "description — what this target ensures, in plain language (persists onto the installed target)",
    draft.description, (v) => { draft.description = v; },
    { textarea: true, rows: 2, placeholder: "e.g. Every settled tab that owes money is posted to the resident's house account." }));
  return box;
}

function gapsBox() {
  const box = el("div", "weaver-panel");
  box.appendChild(el("h3", null, "Gaps"));
  const cols = Object.keys(draft.gaps).sort();
  if (!cols.length) box.appendChild(el("div", "muted small", "(no gaps yet — add one below)"));
  cols.forEach((col) => box.appendChild(gapCard(col)));

  const addRow = el("div", "author-add-row");
  const addInput = document.createElement("input");
  addInput.type = "text";
  addInput.placeholder = "gap key — the full missing_<gap> column name, e.g. missing_signature";
  addRow.appendChild(addInput);
  const addBtn = el("button", null, "Add gap");
  addBtn.addEventListener("click", () => {
    const name = addInput.value.trim();
    if (!name) return;
    if (draft.gaps[name]) { setStatus("weaver-author-status", "gap \"" + name + "\" already exists", true); return; }
    draft.gaps[name] = emptyGap();
    addInput.value = "";
    render();
  });
  addRow.appendChild(addBtn);
  box.appendChild(addRow);
  return box;
}

function gapCard(col) {
  const g = draft.gaps[col];
  const card = el("div", "card author-gap-card");
  const head = el("div", "weaver-card-head");
  head.appendChild(el("span", "weaver-gap-name", col));
  const rm = el("button", "author-remove", "remove");
  rm.addEventListener("click", () => { delete draft.gaps[col]; render(); });
  head.appendChild(rm);
  card.appendChild(head);

  const row = el("div", "op-fields author-row");
  const strField = (label, key, placeholder) =>
    row.appendChild(labeledInput(label, g[key], (v) => { g[key] = v; }, { placeholder }));
  strField("action *", "action", "directOp | triggerLoom | assignTask | surface");
  strField("pattern", "pattern", "(triggerLoom's meta.loomPattern patternId)");
  strField("subject", "subject", "row.<column>");
  strField("adapter", "adapter", "");
  strField("operation", "operation", "(directOp's operationType)");
  strField("assignee", "assignee", "(assignTask)");
  strField("target", "target", "");
  strField("issueCode", "issueCode", "(surface)");
  strField("issueSeverity", "issueSeverity", "(surface)");
  card.appendChild(row);

  card.appendChild(labeledInput("params (one key=value per line)", g.paramsText, (v) => { g.paramsText = v; }, { textarea: true, rows: 2 }));
  card.appendChild(labeledInput("reads (comma-separated row.<column> templates)", g.readsText, (v) => { g.readsText = v; }, { textarea: true, rows: 2 }));
  return card;
}

function lensBox() {
  const l = draft.lens;
  const box = el("div", "weaver-panel");
  const head = el("div", "weaver-card-head");
  head.appendChild(el("h3", null, "Paired violation lens"));
  const scaffoldBtn = el("button", null, "Scaffold cypher from gaps");
  scaffoldBtn.addEventListener("click", () => {
    l.spec = scaffoldLensSpec(draft.targetId, Object.keys(draft.gaps).sort());
    render();
  });
  head.appendChild(scaffoldBtn);
  box.appendChild(head);

  const row = el("div", "op-fields author-row");
  // Defaults to lensRef until the operator types their own — matched by
  // buildLensContent's own fallback, so Check/Export agree with what's shown
  // even before this field gains a keystroke.
  row.appendChild(labeledInput("canonicalName", l.canonicalName || draft.lensRef, (v) => { l.canonicalName = v; }));
  row.appendChild(labeledInput("adapter", l.adapter, (v) => { l.adapter = v; }));
  row.appendChild(labeledInput("bucket", l.bucket, (v) => { l.bucket = v; }));
  row.appendChild(labeledInput("table (postgres only)", l.table, (v) => { l.table = v; }));
  box.appendChild(row);
  box.appendChild(labeledInput("spec (openCypher)", l.spec, (v) => { l.spec = v; }, { textarea: true, rows: 10 }));
  return box;
}

function actionsBox() {
  const box = el("div", "weaver-panel author-actions");
  const checkBtn = demoHide(el("button", null, "Run checks"));
  checkBtn.addEventListener("click", runChecks);
  box.appendChild(checkBtn);

  box.appendChild(labeledInput("rationale (carried into the export bundle)", draft.rationale, (v) => { draft.rationale = v; }, { textarea: true, rows: 2 }));

  const exportBtn = el("button", null, "Export checked bundle");
  exportBtn.addEventListener("click", doExport);
  box.appendChild(exportBtn);

  // Propose needs a passing Check on BOTH artifacts and a description — an
  // operator can still Export an incomplete draft (a file has no review-queue
  // consequence), but entering the queue on a verdict the operator hasn't seen,
  // or under a row label nobody can read, leaves a proposal the reviewer cannot
  // act on. proposeBlockers names every unmet reason so the disabled button is
  // never a mystery.
  const blockers = proposeBlockers(draft, lastCheck);
  const proposeBtn = demoHide(el("button", null, "Propose"));
  proposeBtn.disabled = blockers.length > 0;
  if (blockers.length) proposeBtn.title = "propose needs: " + blockers.join("; ");
  proposeBtn.addEventListener("click", doPropose);
  box.appendChild(proposeBtn);
  if (blockers.length) box.appendChild(el("span", "muted small", "propose needs: " + blockers.join("; ")));

  const status = el("span", "muted", "");
  status.id = "weaver-author-status";
  box.appendChild(status);
  return box;
}

async function runChecks() {
  setStatus("weaver-author-status", "checking…");
  const body = await api("/api/weaver/author/check", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ target: buildTargetContent(draft), lens: buildLensContent(draft) }),
  });
  if (body.error) { setStatus("weaver-author-status", body.error, true); return; }
  lastCheck = body;
  render();
  setStatus("weaver-author-status", "checked");
}

function checkResultBox(r) {
  const box = el("div", "weaver-panel");
  box.appendChild(el("h3", null, "Check result"));

  const tv = validationBadge(r.targetValidation);
  const lv = validationBadge(r.lensValidation);
  const vRow = el("div", "author-validation-row");
  vRow.appendChild(chip("target artifact: " + tv.text, tv.cls));
  vRow.appendChild(chip("lens artifact: " + lv.text, lv.cls));
  box.appendChild(vRow);
  (r.targetValidation && r.targetValidation.errors || []).forEach((e) => box.appendChild(el("div", "weaver-issue bad", "target: " + e)));
  (r.lensValidation && r.lensValidation.errors || []).forEach((e) => box.appendChild(el("div", "weaver-issue bad", "lens: " + e)));

  box.appendChild(el("div", "muted small", "V1 lane checks — " + checksSummary(r.laneChecks)));
  (r.laneChecks || []).forEach((c) => {
    const row = el("div", "weaver-issue");
    row.appendChild(chip(c.code, c.severity === "bad" ? "bad" : "warn"));
    row.appendChild(el("span", null, c.message));
    box.appendChild(row);
  });

  box.appendChild(el("div", "muted small", "V3 interference — " + opCoverageNote(r.opCoverage) + " — " + interferenceHeadline(r.interference)));
  (r.interference || []).forEach((row) => {
    const line = el("div", "weaver-issue warn");
    line.appendChild(chip(row.path, "warn"));
    line.appendChild(el("span", "muted small", "targets: " + (row.targets || []).join(", ")));
    box.appendChild(line);
  });
  return box;
}

function doExport() {
  const bundle = exportBundle(draft, lastCheck, draft.rationale, "install");
  const blob = new Blob([JSON.stringify(bundle, null, 2)], { type: "application/json" });
  const url = URL.createObjectURL(blob);
  const a = el("a", null, "");
  a.href = url;
  a.setAttribute("download", exportFilename(draft.targetId));
  document.body.appendChild(a);
  a.click();
  a.remove();
  URL.revokeObjectURL(url);
  setStatus("weaver-author-status", "exported " + exportFilename(draft.targetId));
}

// doPropose submits the same two-artifact bundle Export downloads (§6.4:
// exportBundle's shape IS the wire shape SubmitCapabilityProposal needs per
// artifact, minus proposalId, which the server mints). Both artifacts are
// submitted regardless of whether one fails — they are independent
// proposals in the queue, and an operator who fixed only the lens shouldn't
// have the target's already-good submission withheld from them too.
async function doPropose() {
  setStatus("weaver-author-status", "proposing…");
  const bundle = exportBundle(draft, lastCheck, draft.rationale, "install");
  const body = await api("/api/weaver/author/propose", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(bundle),
  });
  if (body.error) { setStatus("weaver-author-status", body.error, true); return; }
  proposeResults = body.results || [];
  render();
  const failed = proposeResults.filter((r) => proposeFailed(r)).length;
  setStatus("weaver-author-status", failed ? failed + " of " + proposeResults.length + " proposals failed" : "proposed", !!failed);
}

// proposeFailed reports whether a propose result never entered the queue —
// a transport-level Error, or a reply the Processor rejected outright
// (opRejected's own distinction: a rejection is a well-formed 200 reply, not
// an error, so branching on .error alone would report it as success).
function proposeFailed(r) {
  if (r.error) return true;
  return !!(r.reply && r.reply.status === "rejected");
}

function proposeResultBox(results) {
  const box = el("div", "weaver-panel");
  box.appendChild(el("h3", null, "Propose result"));
  results.forEach((r) => {
    const row = el("div", "weaver-issue");
    row.appendChild(chip(r.kind, proposeFailed(r) ? "bad" : "ok"));
    if (proposeFailed(r)) {
      const reason = r.error || (r.reply && r.reply.error && (r.reply.error.message || r.reply.error.code)) || "rejected";
      row.appendChild(el("span", null, reason));
    } else {
      const link = el("a", "key-link", r.proposalId);
      link.href = "#/review/capability/" + encodeURIComponent(r.proposalId);
      row.appendChild(link);
      row.appendChild(el("span", "muted small", "→ review queue"));
    }
    box.appendChild(row);
  });
  return box;
}

// trialBox is the born-disabled trial choreography (§6 step 5), a guided
// wrapper around primitives that already exist: revoke/enable are the same
// /api/control/weaver/<targetId>/<op> relay the Weaver component control
// page uses, seeding fixture entities is the generic op console, and
// watching the map light up is F25.1's own target-detail route. Nothing
// here is gated on Propose having run — an operator revisiting a target
// already in the queue (or already approved) can still walk the sequence.
function trialBox() {
  const id = draft.targetId;
  const box = el("div", "weaver-panel");
  box.appendChild(el("h3", null, "Trial (dev stack)"));
  box.appendChild(el("p", "muted small",
    "Born-disabled: revoke first so a future registration of " + id + " starts disabled, propose + let the " +
    "review queue approve + apply it, seed a few fixture entities, watch the map, then enable when you actually " +
    "want dispatch on this stack."));

  const steps = el("div", "author-trial-steps");

  const revokeRow = el("div", "weaver-issue");
  const revokeBtn = demoHide(el("button", null, "1 · revoke (arm disabled)"));
  revokeBtn.addEventListener("click", () => {
    if (revokeBtn.dataset.armed !== "1") {
      revokeBtn.dataset.armed = "1";
      revokeBtn.textContent = "1 · revoke — sure?";
      setTimeout(() => { revokeBtn.dataset.armed = ""; revokeBtn.textContent = "1 · revoke (arm disabled)"; }, 4000);
      return;
    }
    runWeaverControl(id, "revoke", revokeRow);
  });
  revokeRow.appendChild(revokeBtn);
  steps.appendChild(revokeRow);

  const reviewLink = el("a", "key-link", "2 · review queue (approve + apply)");
  reviewLink.href = "#/review/capability";
  steps.appendChild(wrapStep(reviewLink));

  const opLink = el("a", "key-link", "3 · seed fixture entities (op console)");
  opLink.href = "#/op";
  steps.appendChild(wrapStep(opLink));

  const watchLink = el("a", "key-link", "4 · watch " + id);
  watchLink.href = "#/weaver/" + encodeURIComponent(id);
  steps.appendChild(wrapStep(watchLink));

  const enableRow = el("div", "weaver-issue");
  const enableBtn = demoHide(el("button", null, "5 · enable (arm live dispatch)"));
  enableBtn.addEventListener("click", () => runWeaverControl(id, "enable", enableRow));
  enableRow.appendChild(enableBtn);
  steps.appendChild(enableRow);

  box.appendChild(steps);
  return box;
}

function wrapStep(linkEl) {
  const row = el("div", "weaver-issue");
  row.appendChild(linkEl);
  return row;
}

async function runWeaverControl(id, op, row) {
  const status = el("span", "muted small", " " + op + "…");
  row.appendChild(status);
  const body = await api("/api/control/weaver/" + encodeURIComponent(id) + "/" + op, { method: "POST" });
  status.textContent = " " + (body.error ? op + " failed: " + body.error : op + " ok");
}

function chip(text, cls) {
  const c = el("span", "wchip " + (cls || ""), text);
  return c;
}

export { enterAuthor };
