// F25.3a — Author view: the draft target/lens editor bound onto weaver.js's
// "#/weaver/author" sub-route (weaver-target-studio-design.md §6, steps
// 1-3). A structured form (mirrors op.js's schema-driven field pattern) for
// gaps/actions plus a scaffolded cypher textarea for the paired violation
// lens; "Run checks" posts the draft to F25.2's checker + the pkgmgr
// validators; "Export" downloads the checked artifact bundle client-side —
// no propose step (F25.3b), no platform write of any kind.

import { $, el, api, demoHide, setStatus } from "../api.js";
import {
  emptyDraft, emptyGap, parseParamsText, paramsToText, parseReadsText, readsToText,
  buildTargetContent, buildLensContent, scaffoldLensSpec, validationBadge,
  exportBundle, exportFilename,
} from "../logic/weaverauthor.js";
import { checksSummary, interferenceHeadline, opCoverageNote } from "../logic/weaver.js";

let draft = emptyDraft();
let lastCheck = null;

function enterAuthor() {
  draft = emptyDraft();
  lastCheck = null;
  render();
}

function render() {
  const box = $("#weaver-author");
  box.innerHTML = "";
  box.appendChild(el("p", "muted small",
    "Compose a target + its paired violation lens as a capability artifact — the same shape the AI-authored " +
    "lane validates and the Review console applies. Browser-local until exported; nothing here writes any " +
    "platform state. Export downloads the checked bundle; submitting it into the review queue is a later fire."));
  box.appendChild(targetFieldsBox());
  box.appendChild(gapsBox());
  box.appendChild(lensBox());
  box.appendChild(actionsBox());
  if (lastCheck) box.appendChild(checkResultBox(lastCheck));
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
  row.appendChild(labeledInput("lensRef (the paired lens's canonicalName)", draft.lensRef, (v) => { draft.lensRef = v; draft.lens.canonicalName = v; render(); }));
  box.appendChild(row);
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
  addInput.placeholder = "gap column name (e.g. signature, for missing_signature)";
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
  row.appendChild(labeledInput("canonicalName", l.canonicalName, (v) => { l.canonicalName = v; }));
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

function chip(text, cls) {
  const c = el("span", "wchip " + (cls || ""), text);
  return c;
}

export { enterAuthor };
