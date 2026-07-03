// Graph explorer (design §7): the faceted/grouped/paged entity list, the
// linkified detail view, and the neighborhood (ego-graph) mode. List filter
// state is URL-carried on the bare route (#/graph?type=&q=&deleted=1);
// #/graph/<key> selects a detail, ?view=hood swaps the detail for the
// ego-graph stage. Every rendered key resolves through keyTarget.

import { $, el, api, setStatus } from "../api.js";
import { shortId, keyTarget, classifyKey } from "../logic/keys.js";
import {
  adaptiveRadius, ringPositions, sectorPositions,
  groupLinkItems, evictForBudget, hoodSentence,
} from "../logic/hood.js";
import { renderDoc, keyLinkEl } from "../render.js";
import { navigate } from "../router.js";

const PAGE = 500;
const GROUP_THRESHOLD = 8;
const HOOD_BUDGET = 60;
const TRAIL_CAP = 15;

// ---------------------------------------------------------------------------
// List state: filters mirror the URL params of the bare route; rows accumulate
// across "show next" pages and re-render as one grouped list.
const list = {
  loaded: false,
  seq: 0,
  type: "",
  q: "",
  deleted: false,
  prefix: "",
  rows: [],
  facets: {},
  total: 0,
};
const trail = [];
let currentDetailKey = null;
let selectedKeyRow = null;

// listHash builds the URL-carried form of the current filters. Values are
// encodeURIComponent-encoded (space → %20) so they round-trip through
// parseRoute's decodeURIComponent — URLSearchParams would emit "+" for a
// space, which decodeURIComponent does not undo.
function listHash(overrides) {
  const s = Object.assign({ type: list.type, q: list.q, deleted: list.deleted, prefix: list.prefix }, overrides || {});
  const parts = [];
  if (s.type) parts.push("type=" + encodeURIComponent(s.type));
  if (s.q) parts.push("q=" + encodeURIComponent(s.q));
  if (s.deleted) parts.push("deleted=1");
  if (s.prefix) parts.push("prefix=" + encodeURIComponent(s.prefix));
  return "#/graph" + (parts.length ? "?" + parts.join("&") : "");
}

// enter handles every #/graph route. Bare route: params are the list filters
// (URL-carried, so back/forward restore them). With an arg: the detail or
// hood mode for that key — list filters keep their in-memory state.
function enter(route) {
  if (route.arg && route.params.view === "hood") {
    showHood(route.arg);
    return;
  }
  showList();
  if (!route.arg) {
    const next = {
      type: route.params.type || "",
      q: route.params.q || "",
      deleted: route.params.deleted === "1",
      prefix: route.params.prefix || "",
    };
    const changed = next.type !== list.type || next.q !== list.q ||
      next.deleted !== list.deleted || next.prefix !== list.prefix;
    Object.assign(list, next);
    $("#graph-q").value = list.q;
    $("#graph-prefix").value = list.prefix;
    $("#graph-deleted").checked = list.deleted;
    if (changed || !list.loaded) loadList(false);
    list.loaded = true;
    return;
  }
  if (!list.loaded) {
    list.loaded = true;
    loadList(false);
  }
  loadVertexDetail(route.arg, route.params.aspect);
}

async function loadList(append) {
  const seq = ++list.seq;
  if (!append) list.rows = [];
  // The next window starts where the accumulated rows end — derived, so a
  // failed or superseded page never leaves a phantom gap in the sequence.
  const offset = list.rows.length;
  setStatus("graph-status", "loading…");
  const p = new URLSearchParams({ limit: String(PAGE), offset: String(offset) });
  if (list.type) p.set("type", list.type);
  if (list.q) p.set("q", list.q);
  if (list.deleted) p.set("includeDeleted", "1");
  if (list.prefix) p.set("prefix", list.prefix);
  const body = await api("/api/vertices?" + p.toString());
  if (seq !== list.seq) return; // a newer load superseded this one
  if (body.error) {
    setStatus("graph-status", body.error, true);
    if (append) renderRows(); // restore an enabled "show next" for retry
    return;
  }
  list.rows = list.rows.concat(body.vertices || []);
  list.facets = body.facets || {};
  list.total = body.total || 0;
  setStatus("graph-status", list.rows.length + " of " + list.total + " entities");
  renderFacets();
  renderRows();
}

// renderFacets paints the type chips with counts; the active chip filters via
// a real route change so the filtered view is shareable.
function renderFacets() {
  const box = $("#graph-facets");
  box.innerHTML = "";
  const types = Object.keys(list.facets).sort();
  let all = 0;
  types.forEach((t) => { all += list.facets[t]; });
  const chip = (label, count, type) => {
    const c = el("a", "facet-chip" + (list.type === type ? " active" : ""));
    c.appendChild(el("span", null, label));
    c.appendChild(el("span", "facet-count", String(count)));
    c.href = listHash({ type });
    box.appendChild(c);
  };
  chip("all", all, "");
  types.forEach((t) => chip(t, list.facets[t], t));
}

// renderTrail paints the session trail — the last ~15 visited keys, cheap
// re-entry into a walk. In-memory only; dies with the tab.
function renderTrail() {
  const box = $("#graph-trail");
  box.innerHTML = "";
  if (!trail.length) return;
  box.appendChild(el("span", "trail-label", "recently viewed"));
  trail.forEach((k) => {
    const a = el("a", "trail-chip", shortId(k) || k);
    a.title = k;
    a.href = keyTarget(k) || "#/graph";
    box.appendChild(a);
  });
}

// renderRows paints the accumulated rows grouped under sticky type headers,
// with the honest "show next" tail row while more pages remain.
function renderRows() {
  const box = $("#graph-keys");
  box.innerHTML = "";
  selectedKeyRow = null;
  if (!list.rows.length) {
    box.appendChild(el("div", "muted", "(no entities match)"));
    return;
  }
  const groups = new Map();
  list.rows.forEach((v) => {
    if (!groups.has(v.type)) groups.set(v.type, []);
    groups.get(v.type).push(v);
  });
  groups.forEach((rows, type) => {
    box.appendChild(el("div", "group-head", type + " · " + rows.length));
    rows.forEach((v) => box.appendChild(vertexRowEl(v)));
  });
  const remaining = list.total - list.rows.length;
  if (remaining > 0) {
    const more = el("button", "graph-more", "show next " + Math.min(PAGE, remaining) + " (" + remaining + " remaining)");
    more.addEventListener("click", () => {
      more.disabled = true; // one page in flight at a time
      loadList(true);
    });
    box.appendChild(more);
  }
  markSelected(currentDetailKey);
}

function vertexRowEl(v) {
  const row = el("div", "key-row vtx-row" + (v.isDeleted ? " tombstone" : ""));
  row.dataset.key = v.key;
  row.appendChild(el("span", "badge vtype", v.type));
  const main = el("span", "ktext");
  main.appendChild(el("span", "vtx-label", v.label || shortId(v.key)));
  if (v.label) main.appendChild(el("span", "vtx-id", shortId(v.key)));
  row.appendChild(main);
  if (v.isDeleted) row.appendChild(el("span", "deleted-flag", "del"));
  row.addEventListener("click", () => navigate(keyTarget(v.key)));
  return row;
}

// markSelected highlights the list row for key, when it is in the list.
function markSelected(key) {
  if (selectedKeyRow) { selectedKeyRow.classList.remove("selected"); selectedKeyRow = null; }
  if (!key) return;
  const row = $('#graph-keys .key-row[data-key="' + CSS.escape(key) + '"]');
  if (row) { row.classList.add("selected"); selectedKeyRow = row; }
}

function pushTrail(key) {
  const i = trail.indexOf(key);
  if (i >= 0) trail.splice(i, 1);
  trail.unshift(key);
  if (trail.length > TRAIL_CAP) trail.pop();
  renderTrail();
}

// ---------------------------------------------------------------------------
// Detail view (§7.2): provenance chips, the linkified document, aspects, and
// link rows rendered as sentences with the far end as the primary click.

async function loadVertexDetail(key, openAspect) {
  currentDetailKey = key;
  markSelected(key);
  pushTrail(key);
  const head = $("#graph-valuehead");
  const detail = $("#graph-detail");
  head.textContent = key;
  detail.innerHTML = "";
  detail.appendChild(el("div", "muted small", "loading…"));
  const body = await api("/api/vertex?key=" + encodeURIComponent(key));
  if (currentDetailKey !== key) return; // a newer selection superseded this one
  detail.innerHTML = "";
  if (body.error) {
    head.textContent = key;
    const card = el("div", "notfound-card");
    card.appendChild(el("div", "notfound-key", key));
    if (isOpTracker(key)) {
      card.appendChild(el("div", "muted", "not present in Core KV — op trackers expire after 24h (TTL), so this one may simply have aged out"));
    } else {
      card.appendChild(el("div", "muted", "not present in Core KV"));
    }
    const back = el("a", "key-link", "← back to Graph");
    back.href = "#/graph";
    card.appendChild(back);
    detail.appendChild(card);
    return;
  }

  head.textContent = key + " · r" + body.revision;
  if (isOpTracker(key)) head.appendChild(el("span", "badge vtype", "op tracker"));
  if (body.isDeleted) head.appendChild(el("span", "deleted-flag", "isDeleted"));

  // Actions row: hood mode (vertices only — a link's endpoints are already
  // the sentence below), copy, and the type-specific jumps.
  const actions = el("div", "detail-actions");
  if (classifyKey(key) !== "link") {
    const hoodBtn = el("button", "detail-action", "neighborhood view");
    hoodBtn.addEventListener("click", () => navigate("#/graph/" + key + "?view=hood"));
    actions.appendChild(hoodBtn);
  }
  const copyBtn = el("button", "detail-action", "copy key");
  copyBtn.addEventListener("click", async () => {
    try { await navigator.clipboard.writeText(key); copyBtn.textContent = "copied ✓"; }
    catch (_) { copyBtn.textContent = "copy failed"; }
    setTimeout(() => { copyBtn.textContent = "copy key"; }, 1200);
  });
  actions.appendChild(copyBtn);
  if (key.indexOf("vtx.task.") === 0) {
    const t = el("a", "detail-action-link", "open task inbox →");
    t.href = "#/tasks";
    actions.appendChild(t);
  }
  if (body.class === "meta.lens") {
    const lp = el("a", "detail-action-link", "lens page →");
    lp.href = "#/lens/" + shortId(key);
    actions.appendChild(lp);
  }
  detail.appendChild(actions);

  // Provenance chips: who/what created + last modified this entity, every id
  // a link (createdBy → the actor's vertex, *ByOp → the op tracker).
  const env = (body.envelope && typeof body.envelope === "object") ? body.envelope : {};
  const prov = el("div", "prov-chips");
  const chip = (label, val, isKey) => {
    if (!val) return;
    const c = el("span", "prov-chip");
    c.appendChild(el("span", "prov-k", label));
    c.appendChild(isKey ? keyLinkEl(val) : el("span", null, val));
    prov.appendChild(c);
  };
  chip("created by", env.createdBy, true);
  chip("via op", env.createdByOp, true);
  chip("at", env.createdAt, false);
  if (env.lastModifiedAt && env.lastModifiedAt !== env.createdAt) {
    chip("modified by", env.lastModifiedBy, true);
    chip("via op", env.lastModifiedByOp, true);
    chip("at", env.lastModifiedAt, false);
  }
  if (prov.children.length) detail.appendChild(prov);

  // A link key's detail leads with the sentence: both endpoints as links.
  if (classifyKey(key) === "link") {
    const segs = key.split(".");
    const sentence = el("div", "link-sentence");
    sentence.appendChild(keyLinkEl("vtx." + segs[1] + "." + segs[2]));
    sentence.appendChild(el("span", "link-rel", segs[3]));
    sentence.appendChild(keyLinkEl("vtx." + segs[4] + "." + segs[5]));
    detail.appendChild(sentence);
  }

  // Document — the linkifying renderer, not a text dump.
  detail.appendChild(el("div", "vtx-section-head", "document" + (body.class ? " · " + body.class : "")));
  detail.appendChild(renderDoc(body.envelope === null ? undefined : body.envelope));

  // Aspects.
  const aspects = body.aspects || [];
  detail.appendChild(el("div", "vtx-section-head", "aspects (" + aspects.length + ")"));
  if (!aspects.length) detail.appendChild(el("div", "muted small", "(none)"));
  let aspectFound = false;
  aspects.forEach((a) => {
    const row = expanderRow(a.localName, "aspect", a.key);
    detail.appendChild(row);
    if (openAspect && a.localName === openAspect) {
      aspectFound = true;
      $(".expander-head", row).click();
      row.scrollIntoView({ block: "nearest" });
    }
  });
  if (openAspect && !aspectFound) {
    detail.appendChild(el("div", "muted small", "(aspect “" + openAspect + "” not present on this vertex)"));
  }

  // Links (either direction): the far end is the row's primary click; the ⧉
  // expander still opens the link document in place.
  const links = body.links || [];
  detail.appendChild(el("div", "vtx-section-head", "links (" + links.length + ")"));
  if (!links.length) detail.appendChild(el("div", "muted small", "(none)"));
  links.forEach((l) => {
    const arrow = l.direction === "out" ? "→" : "←";
    const label = arrow + " " + l.relation + " " + l.otherType + " · " + shortId(l.otherKey);
    detail.appendChild(expanderRow(label, "link " + l.direction, l.key, l.otherKey));
  });
}

function isOpTracker(key) {
  return key.indexOf("vtx.op.") === 0 && classifyKey(key) === "vertex";
}

// expanderRow renders a collapsed row that lazy-loads the entry's document via
// /api/corekv/entry on toggle (rendered linkified). When farKey is given (a
// link row), the row label's click navigates to the far-end vertex and a
// trailing ⧉ toggles the document instead; the expanded body leads with the
// link's own key as a link to its detail view.
function expanderRow(label, badge, key, farKey) {
  const wrap = el("div", "expander");
  const headEl = el("div", "expander-head");
  const arrow = el("span", "expander-arrow", "▸");
  const bodyEl = el("div", "expander-body doc-body");
  bodyEl.style.display = "none";
  let docLoaded = false;

  const toggleDoc = async () => {
    const isOpen = bodyEl.style.display !== "none";
    bodyEl.style.display = isOpen ? "none" : "block";
    arrow.textContent = isOpen ? "▸" : "▾";
    if (!isOpen && !docLoaded) {
      docLoaded = true;
      bodyEl.textContent = "loading…";
      const e = await api("/api/corekv/entry?key=" + encodeURIComponent(key));
      bodyEl.innerHTML = "";
      if (e.error) {
        bodyEl.appendChild(el("div", "error-text", e.error));
        return;
      }
      if (farKey) {
        const keyLine = el("div", "doc-selfkey");
        keyLine.appendChild(keyLinkEl(key));
        bodyEl.appendChild(keyLine);
      }
      bodyEl.appendChild(renderDoc(e.envelope === null || e.envelope === undefined ? undefined : e.envelope));
    }
  };

  headEl.appendChild(arrow);
  const labelEl = el("span", "expander-label" + (farKey ? " far-link" : ""), label);
  headEl.appendChild(labelEl);
  if (badge) headEl.appendChild(el("span", "badge " + badge, badge));

  if (farKey) {
    labelEl.title = farKey;
    headEl.addEventListener("click", () => navigate(keyTarget(farKey)));
    const doc = el("span", "expander-doc-toggle", "⧉");
    doc.title = "link document (" + key + ")";
    doc.addEventListener("click", (e) => { e.stopPropagation(); toggleDoc(); });
    headEl.appendChild(doc);
  } else {
    headEl.addEventListener("click", toggleDoc);
  }

  wrap.appendChild(headEl);
  wrap.appendChild(bodyEl);
  return wrap;
}

// ---------------------------------------------------------------------------
// Neighborhood (ego-graph) mode (§7.4): DOM chips + an SVG edge layer, the
// system-map technique. The pure model (layout math, grouping, budget) lives
// in logic/hood.js; this layer fetches and paints.

const hood = { center: null, batches: [], nodes: new Map(), edges: [], seq: 0 };

function showList() {
  $("#graph-list-mode").style.display = "";
  $("#graph-hood").style.display = "none";
}

function showHood(centerKey) {
  $("#graph-list-mode").style.display = "none";
  $("#graph-hood").style.display = "";
  if (hood.center !== centerKey) {
    hood.center = centerKey;
    hood.batches = [];
    hood.nodes = new Map();
    hood.edges = [];
    hoodExpand(centerKey, null);
  }
}

// hoodExpand fetches key's neighbors and adds them as a new batch anchored on
// anchorId (null = this is the center's own ring, batch 0).
async function hoodExpand(key, anchorId) {
  const seq = ++hood.seq;
  setStatus("hood-status", "loading " + key + "…");
  const body = await api("/api/vertex?key=" + encodeURIComponent(key));
  if (seq !== hood.seq || hood.center === null) return;
  if (body.error) { setStatus("hood-status", body.error, true); return; }

  if (anchorId === null) {
    hood.nodes.set(key, {
      id: key, key, kind: "center", type: vertexTypeOf(key),
      isDeleted: !!body.isDeleted, batch: 0, angle: 0,
    });
  }
  const items = groupLinkItems(body.links || [], GROUP_THRESHOLD);
  const batchIdx = hood.batches.length;
  const batch = { anchor: anchorId === null ? key : anchorId, ids: [] };
  hood.batches.push(batch);

  items.forEach((item, i) => {
    if (item.kind === "single") {
      const far = item.link.otherKey;
      if (!hood.nodes.has(far)) {
        hood.nodes.set(far, {
          id: far, key: far, kind: "chip", type: item.link.otherType,
          batch: batchIdx, angle: 0,
        });
        batch.ids.push(far);
      }
      addEdge(item.link, key, far);
    } else {
      const gid = "grp:" + batchIdx + ":" + i;
      hood.nodes.set(gid, {
        id: gid, kind: "group", type: item.otherType, relation: item.relation,
        direction: item.direction, links: item.links, batch: batchIdx, angle: 0,
      });
      batch.ids.push(gid);
      hood.edges.push({
        id: gid + ":edge", from: key, to: gid, relation: item.relation,
        sentence: item.relation + " × " + item.links.length + " " + item.otherType,
      });
    }
  });

  applyBudget(batchIdx);
  setStatus("hood-status", hood.nodes.size + " nodes · click a chip to expand · double-click to re-center");
  renderHood();
}

function addEdge(link, fromKey, farKey) {
  if (hood.edges.some((e) => e.id === link.key)) return;
  hood.edges.push({
    id: link.key, from: fromKey, to: farKey, relation: link.relation,
    sentence: hoodSentence(shortLabel(fromKey), link, shortLabel(farKey)),
  });
}

function shortLabel(key) {
  return vertexTypeOf(key) + " · " + (shortId(key) || key).slice(0, 10) + "…";
}

function vertexTypeOf(key) {
  const segs = key.split(".");
  return segs.length > 1 ? segs[1] : "";
}

// applyBudget evicts the oldest off-path batches past the node budget and
// drops edges touching removed nodes. The full ancestor chain of the newest
// expansion is protected — the sector the user just opened, and every sector
// on the walk to it, can never be destroyed by its own click. Evicted batches
// are flagged (not removed) so node.batch indexes stay valid and the chip's
// "already expanded" test knows the sector is gone and re-fetchable.
function applyBudget(newestIdx) {
  const sizes = hood.batches.map((b, i) => (b.evicted ? 0 : b.ids.length + (i === 0 ? 1 : 0)));
  const protectedIdxs = [newestIdx];
  let anchor = hood.batches[newestIdx].anchor;
  let hops = 0;
  while (anchor && anchor !== hood.center && hops++ < hood.batches.length) {
    const idx = hood.batches.findIndex((b) => !b.evicted && b.ids.indexOf(anchor) >= 0);
    if (idx < 0) break;
    protectedIdxs.push(idx);
    anchor = hood.batches[idx].anchor;
  }
  const evict = evictForBudget(sizes, protectedIdxs, HOOD_BUDGET);
  if (!evict.length) return;
  const gone = new Set();
  const evictBatch = (b) => {
    b.ids.forEach((id) => { gone.add(id); hood.nodes.delete(id); });
    b.ids = [];
    b.anchor = null;
    b.evicted = true;
  };
  evict.forEach((i) => evictBatch(hood.batches[i]));
  // Cascade: a batch whose anchor vanished loses its nodes too. A protected
  // batch cannot cascade — its anchor lives in a protected ancestor batch.
  let changed = true;
  while (changed) {
    changed = false;
    hood.batches.forEach((b) => {
      if (!b.evicted && b.anchor && b.anchor !== hood.center && gone.has(b.anchor)) {
        evictBatch(b);
        changed = true;
      }
    });
  }
  hood.edges = hood.edges.filter((e) => !gone.has(e.from) && !gone.has(e.to));
}

// renderHood recomputes the full layout and repaints the stage — cheap at the
// ≤60-node budget, and it keeps eviction/re-layout trivially consistent.
function renderHood() {
  const stage = $("#hood-stage");
  const svg = $("#hood-edges");
  Array.from(stage.querySelectorAll(".hood-chip, .hood-center, .hood-popover")).forEach((n) => n.remove());
  svg.innerHTML = "";
  const center = hood.nodes.get(hood.center);
  if (!center) {
    setStatus("hood-status", "no data for " + hood.center, true);
    return;
  }

  const w = Math.max(stage.clientWidth, 640);
  const ring0 = hood.batches[0] ? hood.batches[0].ids : [];
  const r0 = adaptiveRadius(ring0.length, 150, 190);
  const h = Math.max(560, 2 * (r0 + 170));
  stage.style.height = h + "px";
  svg.setAttribute("width", w);
  svg.setAttribute("height", h);
  const cx = w / 2, cy = h / 2;
  center.x = cx; center.y = cy;

  // Ring 0 around the center; each later batch fans out in a sector past its
  // anchor, along the anchor's outward angle.
  const pos0 = ringPositions(ring0.length, cx, cy, r0);
  ring0.forEach((id, i) => {
    const n = hood.nodes.get(id);
    if (n) { n.x = pos0[i].x; n.y = pos0[i].y; n.angle = pos0[i].angle; }
  });
  for (let b = 1; b < hood.batches.length; b++) {
    const batch = hood.batches[b];
    if (!batch.ids.length) continue;
    const anchor = hood.nodes.get(batch.anchor);
    if (!anchor) continue;
    const pts = sectorPositions(batch.ids.length, anchor.x, anchor.y, anchor.angle, 160, Math.PI * 0.8);
    batch.ids.forEach((id, i) => {
      const n = hood.nodes.get(id);
      if (n) { n.x = pts[i].x; n.y = pts[i].y; n.angle = pts[i].angle; }
    });
  }

  // Edges first (under the chips).
  hood.edges.forEach((e) => {
    const a = hood.nodes.get(e.from), b = hood.nodes.get(e.to);
    if (!a || !b || a.x === undefined || b.x === undefined) return;
    const path = document.createElementNS("http://www.w3.org/2000/svg", "line");
    path.setAttribute("x1", a.x); path.setAttribute("y1", a.y);
    path.setAttribute("x2", b.x); path.setAttribute("y2", b.y);
    path.setAttribute("class", "hood-edge");
    const tip = document.createElementNS("http://www.w3.org/2000/svg", "title");
    tip.textContent = e.sentence;
    path.appendChild(tip);
    svg.appendChild(path);
    const label = document.createElementNS("http://www.w3.org/2000/svg", "text");
    label.setAttribute("x", (a.x + b.x) / 2);
    label.setAttribute("y", (a.y + b.y) / 2 - 4);
    label.setAttribute("class", "hood-edge-label");
    label.setAttribute("text-anchor", "middle");
    label.textContent = e.relation;
    const ltip = document.createElementNS("http://www.w3.org/2000/svg", "title");
    ltip.textContent = e.sentence;
    label.appendChild(ltip);
    svg.appendChild(label);
  });

  hood.nodes.forEach((n) => {
    if (n.x === undefined) return;
    stage.appendChild(n.kind === "center" ? centerCard(n) : n.kind === "group" ? groupChip(n) : nodeChip(n));
  });
}

function place(elm, n) {
  elm.style.left = n.x + "px";
  elm.style.top = n.y + "px";
}

function centerCard(n) {
  const card = el("div", "hood-center" + (n.isDeleted ? " tombstone" : ""));
  card.appendChild(el("span", "badge vtype", n.type));
  card.appendChild(el("span", "hood-center-key", shortId(n.key) || n.key));
  card.title = n.key + " — double-click any chip to re-center; “list view” returns to the detail page";
  place(card, n);
  return card;
}

function nodeChip(n) {
  const chip = el("div", "hood-chip");
  chip.appendChild(el("span", "badge vtype", n.type));
  chip.appendChild(el("span", "hood-chip-id", (shortId(n.key) || n.key)));
  chip.title = n.key + " — click to expand, double-click to open here";
  chip.addEventListener("click", () => {
    if (hood.batches.some((b) => !b.evicted && b.anchor === n.key)) return; // already expanded and still on stage
    hoodExpand(n.key, n.key);
  });
  chip.addEventListener("dblclick", () => navigate("#/graph/" + n.key + "?view=hood"));
  place(chip, n);
  return chip;
}

// groupChip renders "identity ×30 (holdsRole)"; clicking opens a paged
// popover mini-list, and picking an entry materializes it on the stage.
function groupChip(n) {
  const chip = el("div", "hood-chip hood-group");
  chip.appendChild(el("span", "hood-chip-id", n.type + " ×" + n.links.length));
  chip.appendChild(el("span", "hood-group-rel", "(" + n.relation + ")"));
  chip.title = n.links.length + " " + n.relation + " " + n.type + " links — click to browse";
  chip.addEventListener("click", (evt) => {
    evt.stopPropagation();
    openGroupPopover(n, chip);
  });
  place(chip, n);
  return chip;
}

function openGroupPopover(n, chipEl) {
  const stage = $("#hood-stage");
  const old = stage.querySelector(".hood-popover");
  if (old) old.remove();
  const pop = el("div", "hood-popover");
  const PAGE_POP = 20;
  let shown = 0;
  const listEl = el("div", "hood-popover-list");
  pop.appendChild(el("div", "hood-popover-head", n.relation + " → " + n.type + " (" + n.links.length + ")"));
  pop.appendChild(listEl);
  const renderPage = () => {
    n.links.slice(shown, shown + PAGE_POP).forEach((l) => {
      const row = el("div", "hood-popover-row");
      const a = el("span", "key-link", shortId(l.otherKey));
      a.title = "place on the stage";
      a.addEventListener("click", () => {
        materialize(n, l);
        pop.remove();
      });
      row.appendChild(a);
      const open = el("a", "hood-popover-open", "detail →");
      open.href = keyTarget(l.otherKey) || "#/graph";
      row.appendChild(open);
      listEl.appendChild(row);
    });
    shown = Math.min(shown + PAGE_POP, n.links.length);
    moreBtn.style.display = shown < n.links.length ? "" : "none";
  };
  const moreBtn = el("button", "hood-popover-more", "more…");
  moreBtn.addEventListener("click", renderPage);
  pop.appendChild(moreBtn);
  const close = el("button", "hood-popover-close", "close");
  close.addEventListener("click", () => pop.remove());
  pop.appendChild(close);
  pop.style.left = chipEl.style.left;
  pop.style.top = (parseFloat(chipEl.style.top) + 24) + "px";
  stage.appendChild(pop);
  renderPage();
}

// materialize adds one member of a group to the stage as a regular chip in a
// small batch anchored on the group chip. The edge belongs to the vertex the
// group was fetched for — the anchor of the group's own batch, not
// necessarily the center.
function materialize(groupNode, link) {
  const groupBatch = hood.batches[groupNode.batch];
  const from = (groupBatch && groupBatch.anchor) || hood.center;
  const far = link.otherKey;
  if (!hood.nodes.has(far)) {
    const batchIdx = hood.batches.length;
    hood.batches.push({ anchor: groupNode.id, ids: [far] });
    hood.nodes.set(far, { id: far, key: far, kind: "chip", type: link.otherType, batch: batchIdx, angle: 0 });
  }
  addEdge(link, from, far);
  renderHood();
}

// ---------------------------------------------------------------------------

function applyFilters() {
  const target = listHash({
    q: $("#graph-q").value.trim(),
    deleted: $("#graph-deleted").checked,
    prefix: $("#graph-prefix").value.trim(),
  });
  // Same filters → the hash won't change and enter() would skip the load, so
  // Search doubles as a live re-query of the bucket.
  if (location.hash === target) {
    Object.assign(list, { q: $("#graph-q").value.trim(), deleted: $("#graph-deleted").checked, prefix: $("#graph-prefix").value.trim() });
    loadList(false);
    return;
  }
  navigate(target);
}

function init() {
  $("#graph-load").addEventListener("click", applyFilters);
  $("#graph-q").addEventListener("keydown", (e) => { if (e.key === "Enter") applyFilters(); });
  $("#graph-prefix").addEventListener("keydown", (e) => { if (e.key === "Enter") applyFilters(); });
  $("#graph-deleted").addEventListener("change", applyFilters);
  $("#hood-list-btn").addEventListener("click", () => {
    navigate(hood.center ? "#/graph/" + hood.center : "#/graph");
  });
}

export { init, enter };
