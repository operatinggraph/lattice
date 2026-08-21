// Weaver view: the Target Studio's Observe stage — the convergence plane
// rendered target-shaped instead of as tables (weaver-target-studio-design.md
// §4). Three modes on one panel, all route-addressable:
//
//   #/weaver                          the target roster, worst-first
//   #/weaver/<targetId>               the target map + its candidate roster
//   #/weaver/<targetId>/<entityId>    one candidate's per-gap drill
//
// All decision logic lives in logic/weaver.js (goja-tested); this file binds
// DOM. Read-only: every write to this plane is a control-plane op that already
// has its home on the Weaver component page, and the Author stage is a later
// fire. Cross-links, not replacements — the map links out to the lens page, the
// pattern's meta vertex, the flow detail and the task vertex, because the
// join is the thing this view adds.

import { $, el, api, setStatus } from "../api.js";
import { keyLinkEl } from "../render.js";
import { navigate } from "../router.js";
import {
  contractionLabel, targetRows, rosterHeadline,
  gapBadges, dispatchLabel, actionSummary, unboundBindings,
  mapLayout, entityBadges, rosterNote,
  gapStateLine, markLine, artifactLine,
  checksSummary, opCoverageNote, interferenceHeadline, rejectedIssueLabel,
} from "../logic/weaver.js";
import { enterAuthor } from "./weaverauthor.js";

const SVG_NS = "http://www.w3.org/2000/svg";

const state = { loaded: false, verifyLoaded: false, arg: "" };

// "#/weaver/verify" and "#/weaver/author" are reserved top-level routes
// (F25.2, F25.3a), checked before the generic single-segment targetId
// branch — a target literally named "verify" or "author" would collide, an
// accepted convention the same way "targets" already is on the API side.
function enter(route) {
  const arg = (route && route.arg) || "";
  state.arg = arg;
  const parts = arg ? arg.split("/") : [];
  if (parts.length === 1 && parts[0] === "verify") {
    showOnly("weaver-verify");
    loadVerify();
    return;
  }
  if (parts.length === 1 && parts[0] === "author") {
    showOnly("weaver-author");
    enterAuthor();
    return;
  }
  if (parts.length >= 2) { showOnly("weaver-entity"); loadEntity(parts[0], parts[1]); return; }
  if (parts.length === 1) { showOnly("weaver-target"); loadTarget(parts[0]); return; }
  showOnly("weaver-list");
  if (state.loaded) return;
  state.loaded = true;
  loadRoster();
}

function showOnly(id) {
  ["weaver-list", "weaver-target", "weaver-entity", "weaver-verify", "weaver-author"].forEach((s) => {
    $("#" + s).classList.toggle("visible", s === id);
  });
}

function init() {
  $("#weaver-reload").addEventListener("click", () => {
    const parts = state.arg ? state.arg.split("/") : [];
    if (parts.length === 1 && parts[0] === "verify") loadVerify();
    else if (parts.length === 1 && parts[0] === "author") return; // local draft — nothing to re-fetch
    else if (parts.length >= 2) loadEntity(parts[0], parts[1]);
    else if (parts.length === 1) loadTarget(parts[0]);
    else loadRoster();
  });
}

// --- roster --------------------------------------------------------------

async function loadRoster() {
  setStatus("weaver-status-msg", "loading…");
  const box = $("#weaver-cards");
  box.innerHTML = "";
  const body = await api("/api/weaver/targets");
  if (body.error) { setStatus("weaver-status-msg", body.error, true); return; }
  const rows = targetRows(body);
  setStatus("weaver-status-msg", rosterHeadline(body, rows));
  if (!rows.length) { box.appendChild(el("div", "muted", "(no registered targets)")); return; }
  rows.forEach((t) => box.appendChild(targetCard(t)));
}

function targetCard(t) {
  const card = el("div", "card weaver-target-card" + (t.orphan ? " orphan" : " drillable"));
  const head = el("div", "weaver-card-head");
  head.appendChild(el("span", "weaver-target-name", t.targetId));
  if (t.orphan) {
    head.appendChild(chip("orphan __control marker", "warn",
      "a disabled marker with no registered target — nothing sweeps it, and a future target " +
      "installed under this id would start disabled"));
  } else {
    head.appendChild(chip(t.state, t.state === "disabled" ? "warn" : "ok",
      t.state === "disabled" ? "remediation-inert; rows still project and marks still clear" : ""));
    if (t.frozen) head.appendChild(chip("oscillation-frozen", "bad", "the runtime detector has frozen this target"));
    const c = contractionLabel(t.contraction);
    if (c) head.appendChild(chip(c.text, c.cls, c.title));
    if (t.issues) head.appendChild(chip(t.issues + " issue" + (t.issues === 1 ? "" : "s"), "warn", ""));
  }
  card.appendChild(head);

  // The authored purpose line sits directly under the id: it is what the card
  // is FOR, so it reads before the counts. Absent when the target's package
  // declared none, rather than showing a placeholder.
  if (t.description) card.appendChild(el("div", "weaver-target-desc muted small", t.description));

  const meta = el("div", "weaver-card-meta muted small");
  if (t.orphan) {
    meta.appendChild(el("span", null,
      "clear it by installing a target under this id and enabling it — `enable` errors on an unregistered target"));
  } else {
    meta.appendChild(el("span", null, t.gaps + (t.gaps === 1 ? " gap" : " gaps")));
    if (t.lensRef) { meta.appendChild(el("span", "weaver-sep", "·")); meta.appendChild(keyLinkEl("vtx.meta." + t.lensRef)); }
  }
  card.appendChild(meta);
  if (!t.orphan) card.addEventListener("click", () => navigate("/weaver/" + encodeURIComponent(t.targetId)));
  return card;
}

// --- target map ----------------------------------------------------------

async function loadTarget(targetId) {
  const box = $("#weaver-target");
  box.innerHTML = "";
  box.appendChild(el("div", "muted", "loading " + targetId + "…"));
  const body = await api("/api/weaver/target/" + encodeURIComponent(targetId));
  box.innerHTML = "";
  if (body.error) { box.appendChild(el("div", "error", body.error)); return; }

  box.appendChild(targetHeader(body));
  box.appendChild(mapSvg(body));
  box.appendChild(checksPanel(body.checks));
  box.appendChild(gapPanel(body));
  if (body.unhandled && body.unhandled.length) box.appendChild(unhandledPanel(body));
  if (body.issues && body.issues.length) box.appendChild(issuePanel(body.issues));
  box.appendChild(entityPanel(body));
}

// checksPanel renders F25.2's per-target Verify findings (V1 structural + V2
// TargetRejected attribution) — exception-first: an empty list states the
// clean pass explicitly rather than rendering nothing at all.
function checksPanel(checks) {
  const box = el("div", "weaver-panel");
  box.appendChild(el("h3", null, "Checks"));
  box.appendChild(el("div", "muted small", checksSummary(checks)));
  (checks || []).forEach((c) => box.appendChild(checkRow(c)));
  return box;
}

function checkRow(c) {
  const row = el("div", "weaver-issue");
  row.appendChild(chip(c.tier, "muted", ""));
  row.appendChild(chip(c.code, c.severity === "bad" ? "bad" : "warn", "evidence: " + c.evidence));
  row.appendChild(el("span", null, c.message));
  return row;
}

function targetHeader(d) {
  const head = el("div", "weaver-detail-head");
  const title = el("div", "weaver-card-head");
  title.appendChild(el("span", "weaver-target-name", d.targetId));
  if (!d.registered) {
    title.appendChild(chip("not registered", "warn",
      "the engine's control plane does not list this target — its package may be uninstalled while its rows remain"));
  } else {
    title.appendChild(chip(d.state, d.state === "disabled" ? "warn" : "ok", ""));
  }
  if (d.mode) title.appendChild(chip("mode " + d.mode, "info", ""));
  const c = contractionLabel(d.contraction);
  if (c) title.appendChild(chip(c.text, c.cls, c.title));
  head.appendChild(title);

  const meta = el("div", "weaver-card-meta muted small");
  if (d.metaKey) meta.appendChild(keyLinkEl(d.metaKey));
  if (d.lensRef) {
    meta.appendChild(el("span", "weaver-sep", "·"));
    const a = el("a", "weaver-link", d.lensName || d.lensRef);
    a.href = "#/lens/" + d.lensRef;
    meta.appendChild(a);
  }
  head.appendChild(meta);

  if (d.description) head.appendChild(el("div", "weaver-target-desc muted small", d.description));

  if (d.admission || d.augur) {
    const policy = el("div", "weaver-policy small");
    if (d.admission) policy.appendChild(policyBox("admission", d.admission));
    if (d.augur) policy.appendChild(policyBox("augur", d.augur));
    head.appendChild(policy);
  }
  return head;
}

function policyBox(name, body) {
  const box = el("div", "weaver-policy-box");
  box.appendChild(el("span", "weaver-policy-name", name));
  box.appendChild(el("pre", "weaver-policy-body", JSON.stringify(body, null, 2)));
  return box;
}

// mapSvg draws the layered definition diagram. The layout is computed in
// logic/weaver.js and this only paints it, so the geometry is testable without
// a DOM.
function mapSvg(d) {
  const layout = mapLayout(d);
  const wrap = el("div", "weaver-map");
  const svg = document.createElementNS(SVG_NS, "svg");
  svg.setAttribute("viewBox", "0 0 " + layout.width + " " + layout.height);
  svg.setAttribute("class", "weaver-map-svg");
  svg.setAttribute("width", String(layout.width));
  svg.setAttribute("height", String(layout.height));

  const byId = {};
  layout.nodes.forEach((n) => { byId[n.id] = n; });
  layout.edges.forEach((e) => {
    const a = byId[e.from], b = byId[e.to];
    if (!a || !b) return;
    const path = document.createElementNS(SVG_NS, "path");
    const x1 = a.x + a.w, y1 = a.y + a.h / 2, x2 = b.x, y2 = b.y + b.h / 2;
    const mx = (x1 + x2) / 2;
    path.setAttribute("d", "M" + x1 + " " + y1 + " C" + mx + " " + y1 + " " + mx + " " + y2 + " " + x2 + " " + y2);
    path.setAttribute("class", "weaver-edge");
    svg.appendChild(path);
  });
  layout.nodes.forEach((n) => svg.appendChild(mapNode(n)));
  wrap.appendChild(svg);
  return wrap;
}

function mapNode(n) {
  const g = document.createElementNS(SVG_NS, "g");
  g.setAttribute("class", "weaver-node weaver-node-" + n.kind + (n.cls ? " " + n.cls : ""));
  // The layout ellipses a label that would overrun its box (SVG text neither
  // wraps nor clips), so the untruncated text lives on a hover title.
  if (n.full && n.full !== n.label) {
    const title = document.createElementNS(SVG_NS, "title");
    title.textContent = n.full;
    g.appendChild(title);
  }
  const rect = document.createElementNS(SVG_NS, "rect");
  rect.setAttribute("x", String(n.x));
  rect.setAttribute("y", String(n.y));
  rect.setAttribute("width", String(n.w));
  rect.setAttribute("height", String(n.h));
  rect.setAttribute("rx", "6");
  g.appendChild(rect);
  const label = document.createElementNS(SVG_NS, "text");
  label.setAttribute("x", String(n.x + 10));
  label.setAttribute("y", String(n.y + (n.sub ? 19 : 27)));
  label.setAttribute("class", "weaver-node-label");
  label.textContent = n.label;
  g.appendChild(label);
  if (n.sub) {
    const sub = document.createElementNS(SVG_NS, "text");
    sub.setAttribute("x", String(n.x + 10));
    sub.setAttribute("y", String(n.y + 34));
    sub.setAttribute("class", "weaver-node-sub");
    sub.textContent = n.sub;
    g.appendChild(sub);
  }
  if (n.href) {
    g.classList.add("clickable");
    g.addEventListener("click", () => { window.location.hash = n.href; });
  }
  return g;
}

function gapPanel(d) {
  const box = el("div", "weaver-panel");
  box.appendChild(el("h3", null, "Gaps"));
  if (!d.gaps.length) { box.appendChild(el("div", "muted", "(no playbook gaps readable)")); return box; }
  d.gaps.forEach((g) => box.appendChild(gapCard(d, g)));
  return box;
}

function gapCard(d, g) {
  const card = el("div", "card weaver-gap-card");
  const head = el("div", "weaver-card-head");
  head.appendChild(el("span", "weaver-gap-name", g.column));
  gapBadges(g).forEach((b) => head.appendChild(chip(b.text, b.cls, b.title || "")));
  card.appendChild(head);
  card.appendChild(el("div", "muted small", dispatchLabel(g)));

  if (g.action) card.appendChild(actionBox(g.action));
  // Ranked by the server (Cost ascending, Ref lexicographic on ties — the
  // planner's own tie-break, F25.2 V2): the ordinal shown here is that rank,
  // never a live pick — the studio never dispatches.
  (g.candidates || []).forEach((c, i) => card.appendChild(actionBox(c, "candidate #" + (i + 1))));
  (g.actions || []).forEach((c, i) => card.appendChild(actionBox(c, "catalog #" + (i + 1))));
  if (g.goal) {
    const goal = el("details", "weaver-goal");
    goal.appendChild(el("summary", "muted small", "goal (guard body — read-only)"));
    goal.appendChild(el("pre", "weaver-policy-body", JSON.stringify(g.goal, null, 2)));
    card.appendChild(goal);
  }
  return card;
}

function actionBox(a, tag) {
  const box = el("div", "weaver-action");
  const head = el("div", "weaver-action-head");
  if (tag) head.appendChild(chip(tag + (a.ref ? " " + a.ref : ""), "muted", ""));
  head.appendChild(el("span", "weaver-action-kind", a.action || "(no action)"));
  head.appendChild(el("span", "muted small", actionSummary(a)));
  box.appendChild(head);
  if (a.pattern) {
    const line = el("div", "muted small");
    if (a.patternKnown) { line.appendChild(el("span", null, "pattern ")); line.appendChild(keyLinkEl(a.patternRef)); }
    else line.appendChild(chip("pattern “" + a.pattern + "” not installed", "bad", "no meta.loomPattern answers to this name"));
    box.appendChild(line);
  }
  const unbound = unboundBindings(a);
  if (unbound.length) {
    const line = el("div", "small");
    line.appendChild(chip("unobserved bindings", "warn",
      "these row.<column> references were not observed in the scanned rows — the lens may not project them, " +
      "or the lens may have no candidate entities yet"));
    unbound.forEach((b) => line.appendChild(el("code", "weaver-binding", b.param + " → row." + b.column)));
    box.appendChild(line);
  }
  // Pre/Effects (F25.2 V2): the planner-facing triple a candidate or catalog
  // entry carries — rendered structurally, same as the goal block, never
  // re-evaluated here.
  if (a.pre) box.appendChild(guardDetails("pre (eligibility guard)", a.pre));
  if (a.effects && a.effects.length) box.appendChild(guardDetails("effects (declared commit)", a.effects));
  return box;
}

function guardDetails(label, body) {
  const details = el("details", "weaver-goal");
  details.appendChild(el("summary", "muted small", label + " — read-only"));
  details.appendChild(el("pre", "weaver-policy-body", JSON.stringify(body, null, 2)));
  return details;
}

function unhandledPanel(d) {
  const box = el("div", "weaver-panel");
  box.appendChild(el("h3", null, "Columns with no playbook entry"));
  box.appendChild(el("p", "muted small",
    "the live rows carry these missing_* columns but the target's gaps map does not — the engine raises " +
    "GapWithoutPlaybook for each, never silently skipping them"));
  const line = el("div", null);
  d.unhandled.forEach((c) => line.appendChild(chip(c, "bad", "")));
  box.appendChild(line);
  return box;
}

function issuePanel(issues) {
  const box = el("div", "weaver-panel");
  box.appendChild(el("h3", null, "Health issues naming this target"));
  box.appendChild(el("p", "muted small",
    "the Weaver heartbeat's standing issues whose message names this id — the engine authors these " +
    "for exactly this surface, so the text is rendered verbatim rather than parsed"));
  issues.forEach((iss) => {
    const row = el("div", "weaver-issue " + (iss.severity === "error" ? "bad" : "warn"));
    row.appendChild(chip(iss.code, iss.severity === "error" ? "bad" : "warn", ""));
    row.appendChild(el("span", null, iss.message));
    if (iss.since) row.appendChild(el("span", "muted small", "since " + iss.since));
    if (iss.instance) row.appendChild(el("span", "muted small", "@" + iss.instance));
    box.appendChild(row);
  });
  return box;
}

function entityPanel(d) {
  const box = el("div", "weaver-panel");
  box.appendChild(el("h3", null, "Candidate entities"));
  box.appendChild(el("div", "muted small", rosterNote(d)));
  if (!d.entities.length) { box.appendChild(el("div", "muted", "(no rows under this target)")); return box; }
  const list = el("div", "cards");
  d.entities.forEach((e) => list.appendChild(entityCard(d, e)));
  box.appendChild(list);
  return box;
}

function entityCard(d, e) {
  const card = el("div", "card weaver-entity-card drillable");
  const head = el("div", "weaver-card-head");
  head.appendChild(el("span", "weaver-entity-id", e.entityId));
  entityBadges(e).forEach((b) => head.appendChild(chip(b.text, b.cls, "")));
  card.appendChild(head);
  if (e.entityKey) {
    const meta = el("div", "weaver-card-meta muted small");
    meta.appendChild(keyLinkEl(e.entityKey));
    card.appendChild(meta);
  }
  card.addEventListener("click", (ev) => {
    if (ev.target.closest("a")) return;
    navigate("/weaver/" + encodeURIComponent(d.targetId) + "/" + encodeURIComponent(e.entityId));
  });
  return card;
}

// --- entity drill --------------------------------------------------------

async function loadEntity(targetId, entityId) {
  const box = $("#weaver-entity");
  box.innerHTML = "";
  box.appendChild(el("div", "muted", "loading " + entityId + "…"));
  const body = await api("/api/weaver/target/" + encodeURIComponent(targetId) + "/entity/" + encodeURIComponent(entityId));
  box.innerHTML = "";
  if (body.error) { box.appendChild(el("div", "error", body.error)); return; }

  const head = el("div", "weaver-detail-head");
  const title = el("div", "weaver-card-head");
  title.appendChild(el("span", "weaver-entity-id", body.entityId));
  title.appendChild(chip(body.violating ? "violating" : "not violating", body.violating ? "warn" : "ok",
    "the lens-projected flag the engine dispatches on — not an implicit OR of the gaps"));
  head.appendChild(title);
  const meta = el("div", "weaver-card-meta muted small");
  const up = el("a", "weaver-link", body.targetId);
  up.href = "#/weaver/" + encodeURIComponent(body.targetId);
  meta.appendChild(up);
  if (body.entityKey) { meta.appendChild(el("span", "weaver-sep", "·")); meta.appendChild(keyLinkEl(body.entityKey)); }
  head.appendChild(meta);
  box.appendChild(head);

  const gaps = el("div", "weaver-panel");
  gaps.appendChild(el("h3", null, "Gaps"));
  if (!body.gaps.length) gaps.appendChild(el("div", "muted", "(no gaps)"));
  body.gaps.forEach((g) => gaps.appendChild(entityGapCard(g)));
  box.appendChild(gaps);

  if (body.issues && body.issues.length) box.appendChild(issuePanel(body.issues));

  const raw = el("details", "weaver-panel");
  raw.appendChild(el("summary", null, "Row document"));
  raw.appendChild(el("pre", "weaver-policy-body", JSON.stringify(body.row || {}, null, 2)));
  box.appendChild(raw);
}

function entityGapCard(g) {
  const card = el("div", "card weaver-gap-card");
  const line = gapStateLine(g);
  const head = el("div", "weaver-card-head");
  head.appendChild(el("span", "weaver-gap-name", g.column));
  head.appendChild(chip(line.state, line.cls, ""));
  if (line.budget) head.appendChild(chip(line.budget, g.state === "exhausted" ? "bad" : "muted", ""));
  card.appendChild(head);
  if (g.action) card.appendChild(actionBox(g.action));

  const m = markLine(g.mark);
  if (m) {
    const box = el("div", "weaver-mark small");
    box.appendChild(el("span", "muted", "in-flight mark"));
    box.appendChild(el("code", "weaver-binding", "action " + m.action));
    if (m.claimId) box.appendChild(el("code", "weaver-binding", "claim " + m.claimId));
    if (m.leaseExpiresAt) box.appendChild(el("span", "muted", "lease to " + m.leaseExpiresAt));
    if (m.heldBy) box.appendChild(el("span", "muted", "held by " + m.heldBy));
    card.appendChild(box);
  }
  const a = artifactLine(g.artifact);
  if (a) {
    const box = el("div", "weaver-artifact small");
    const link = el("a", "weaver-link", a.kind + " " + a.id);
    link.href = a.href;
    box.appendChild(link);
    box.appendChild(chip(a.live ? "live" : "not in live state", a.live ? "ok" : "warn", a.note));
    card.appendChild(box);
  }
  return card;
}

// --- F25.2 lane-wide Verify (#/weaver/verify) -----------------------------

async function loadVerify() {
  const box = $("#weaver-verify");
  box.innerHTML = "";
  box.appendChild(el("div", "muted", "loading verify summary…"));
  const body = await api("/api/weaver/verify");
  box.innerHTML = "";
  if (body.error) { box.appendChild(el("div", "error", body.error)); return; }

  box.appendChild(el("p", "muted small",
    "Read-only, view-time checks — a static/structural pass over every installed target's body (V1), " +
    "the TargetRejected roundup (V2), and the static twin of the runtime oscillation detector's join over " +
    "declared op effects (V3, advisory — the runtime detector stays authoritative)."));

  box.appendChild(verifyTargetsPanel(body));
  box.appendChild(verifyRejectedPanel(body.rejectedIssues));
  box.appendChild(verifyInterferencePanel(body.interference, body.opCoverage));
}

function verifyTargetsPanel(body) {
  const box = el("div", "weaver-panel");
  box.appendChild(el("h3", null, "V1 — structural, per target"));
  const targets = body.targets || [];
  if (!targets.length) { box.appendChild(el("div", "muted", "(no installed targets)")); return box; }
  targets.forEach((t) => box.appendChild(verifyTargetRow(t)));
  return box;
}

function verifyTargetRow(t) {
  const card = el("div", "card weaver-target-card drillable");
  const head = el("div", "weaver-card-head");
  head.appendChild(el("span", "weaver-target-name", t.targetId));
  if (!t.registered) head.appendChild(chip("not registered", "warn", ""));
  head.appendChild(el("span", "muted small", checksSummary(t.checks)));
  card.appendChild(head);
  card.addEventListener("click", () => navigate("/weaver/" + encodeURIComponent(t.targetId)));
  return card;
}

function verifyRejectedPanel(issues) {
  const box = el("div", "weaver-panel");
  box.appendChild(el("h3", null, "V2 — TargetRejected roundup"));
  const list = issues || [];
  if (!list.length) { box.appendChild(el("div", "muted", "no standing TargetRejected issue on any live instance")); return box; }
  list.forEach((iss) => {
    const row = el("div", "weaver-issue bad");
    row.appendChild(chip("TargetRejected", "bad", ""));
    row.appendChild(el("span", null, rejectedIssueLabel(iss)));
    row.appendChild(el("span", "muted small", iss.message));
    if (iss.instance) row.appendChild(el("span", "muted small", "@" + iss.instance));
    box.appendChild(row);
  });
  return box;
}

function verifyInterferencePanel(interference, opCoverage) {
  const box = el("div", "weaver-panel");
  box.appendChild(el("h3", null, "V3 — interference (advisory)"));
  box.appendChild(el("div", "muted small", opCoverageNote(opCoverage)));
  box.appendChild(el("div", "muted small", interferenceHeadline(interference)));
  (interference || []).forEach((row) => {
    const line = el("div", "weaver-issue warn");
    line.appendChild(chip(row.path, "warn", "the concrete aspect path two or more targets' declared effects both assert"));
    line.appendChild(el("span", "muted small", "targets: " + (row.targets || []).join(", ")));
    line.appendChild(el("span", "muted small", "ops: " + (row.ops || []).join(", ")));
    box.appendChild(line);
  });
  return box;
}

function chip(text, cls, title) {
  const c = el("span", "wchip " + (cls || ""), text);
  if (title) c.title = title;
  return c;
}

export { init, enter };
