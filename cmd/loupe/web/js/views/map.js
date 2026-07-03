// System Map view: a hand-laid topology of the deployed components with the
// live Health KV overlay (GET /api/systemmap). Nodes are absolutely-positioned
// DOM in #sysmap-stage; edges are SVG paths measured from each node's box via
// getBoundingClientRect after layout. Rendering is kind-agnostic (driven by the
// status lookup tables in logic/status.js) so a future kind:"agent" node is a
// data change, not new rendering logic.

import { el, api, setStatus } from "../api.js";
import { componentStatusClass, designAheadCopy, designAheadPointer, lensStateDot, lensStateGlyph, pendingReadpathCopy, sysmapSummary, sysmapTier } from "../logic/status.js";
import { deriveTransitions, ledClass } from "../logic/feed.js";
import { keyTarget } from "../logic/keys.js";
import { navigate } from "../router.js";
import * as pulse from "../pulse.js";

// Tier rows 0-4, with the ingress band (tier -1: the external-actors marker +
// the Gateway — the door) above them.
const SYSMAP_INGRESS_Y = 40;
const SYSMAP_TIER_Y = [130, 240, 360, 490, 620];
const SYSMAP_NODE_H = 58;
const SVG_NS = "http://www.w3.org/2000/svg";
const refractorId = "refractor"; // the sole lens parent (see systemmap.go)

// sysmap holds the last-rendered data + transient render state. nodeEls maps a
// node id to its DOM element for edge measurement; tip is the single shared
// hover popover.
const sysmap = { data: null, lastNodes: null, nodeEls: new Map(), tip: null, autoTimer: null, resizeTimer: null, fetchSeq: 0, unsubPulse: null, feedRAF: 0 };

function enter() {
  refreshSystemMap();
  renderFeed();
  if (!sysmap.unsubPulse) {
    sysmap.unsubPulse = pulse.subscribe((evt) => {
      scheduleRenderFeed();
      if (evt.type === "row" && evt.row.kind === "event") pulseFlow();
    });
  }
}

// leave stops the auto-refresh poll so a hidden panel isn't polled, and drops
// the feed subscription (the stream itself stays open — pulse.js is global).
function leave() {
  stopSystemMapAuto();
  hideSysmapTip();
  if (sysmap.unsubPulse) { sysmap.unsubPulse(); sysmap.unsubPulse = null; }
}

// refreshSystemMap is the single clock: re-fetches /api/systemmap and
// re-renders without blanking a previously-good map until the new data
// arrives. The future agent-activity console extends this same function
// rather than adding a second interval.
async function refreshSystemMap() {
  const btn = document.getElementById("sysmap-refresh");
  const had = !!sysmap.data;
  const seq = ++sysmap.fetchSeq;
  if (btn) { btn.disabled = true; btn.textContent = "Loading…"; }
  setStatus("sysmap-status", "loading…");
  if (!had) sysmapStageMessage("loading the system map…");

  const body = await api("/api/systemmap");
  if (seq !== sysmap.fetchSeq) return; // a newer refresh superseded this one
  if (btn) { btn.disabled = false; btn.textContent = "Refresh"; }

  if (body.error) {
    sysmap.data = null;
    renderSysmapError(body.error);
    setStatus("sysmap-status", "error", true);
    setSysmapRollup(null);
    return;
  }
  // Poll-diff derived rows for the pulse feed: state transitions + rule
  // updates since the previous successful poll ride the feed marked "~"
  // (poll-derived, ≤ one poll interval of lag — honest about the mechanism).
  // lastNodes (not sysmap.data, which an error poll nulls) is the diff base,
  // so a transition spanning one failed poll still derives.
  if (sysmap.lastNodes) {
    pulse.addDerived(deriveTransitions(sysmap.lastNodes, body.nodes || []));
  }
  sysmap.lastNodes = body.nodes || [];
  sysmap.data = body;
  renderSystemMap(body);
  setStatus("sysmap-status", "updated just now");
  // The feed header's rows/min rides this same clock (§3.1's one-heartbeat
  // rule) so the rate decays to 0 after a burst instead of going stale.
  scheduleRenderFeed();
}

// sysmapStage returns the stage element, (re)creating its <svg> edge layer.
function sysmapStage() { return document.getElementById("sysmap-stage"); }

function sysmapStageMessage(msg) {
  const stage = sysmapStage();
  if (!stage) return;
  stage.innerHTML = "";
  stage.style.minHeight = "";
  const m = el("div", "sysmap-stage-msg muted", msg);
  stage.appendChild(m);
}

function renderSysmapError(err) {
  const stage = sysmapStage();
  if (!stage) return;
  stage.innerHTML = "";
  stage.style.minHeight = "";
  const box = el("div", "sysmap-stage-msg");
  box.appendChild(el("div", "error-text", err));
  const retry = el("button", null, "Retry");
  retry.addEventListener("click", refreshSystemMap);
  box.appendChild(retry);
  stage.appendChild(box);
}

// setSysmapRollup drives the overall banner + one-line plain-English summary
// and the red top-border cue. Called with null to clear (error state).
function setSysmapRollup(data) {
  const banner = document.getElementById("sysmap-overall");
  const summary = document.getElementById("sysmap-summary");
  const stage = sysmapStage();
  if (!banner || !summary) return;
  if (!data) {
    banner.textContent = "";
    banner.className = "rollup";
    summary.textContent = "";
    if (stage) stage.classList.remove("sysmap-red");
    return;
  }
  const overall = data.overall || "green";
  banner.textContent = overall.toUpperCase();
  banner.className = "rollup " + overall;
  // pending-readpath lenses are surfaced as their own count, never as
  // degraded — a stack that simply hasn't run read-path provisioning is not
  // crying wolf.
  const counts = sysmapSummary(data.nodes || []);
  let pendingSuffix = counts.pending
    ? " · " + counts.pending + " pending read path" : "";
  // design-ahead components are the roadmap, not degradation — their count
  // rides the informational suffix beside pending-readpath.
  if (counts.designAhead) {
    pendingSuffix += " · " + counts.designAhead + " design-ahead (not yet deployed)";
  }
  if (overall === "red") {
    const parts = [];
    if (counts.absent) parts.push(counts.absent + " absent");
    if (counts.unhealthy) parts.push(counts.unhealthy + " unhealthy");
    summary.textContent = (parts.length ? parts.join(", ") : "issues detected") + "." + pendingSuffix;
    if (stage) stage.classList.add("sysmap-red");
  } else if (overall === "yellow") {
    summary.textContent = counts.degraded + " component(s)/lens(es) degraded." + pendingSuffix;
    if (stage) stage.classList.remove("sysmap-red");
  } else {
    summary.textContent = "All components healthy." + pendingSuffix;
    if (stage) stage.classList.remove("sysmap-red");
  }
}

// renderSystemMap lays out the nodes (tiers 0-3 absolutely positioned, tier-4
// lenses in a flex-wrap shelf), then schedules an edge pass after layout.
function renderSystemMap(data) {
  const stage = sysmapStage();
  if (!stage) return;
  setSysmapRollup(data);
  stage.innerHTML = "";
  sysmap.nodeEls = new Map();

  const svg = document.createElementNS(SVG_NS, "svg");
  svg.id = "sysmap-edges";
  svg.setAttribute("xmlns", SVG_NS);
  stage.appendChild(svg);

  const nodes = data.nodes || [];
  const width = stage.clientWidth || 1100;

  // Tiers 0-3: absolutely positioned, evenly spaced across the stage width.
  // The ingress band (tier -1) sits above tier 0; a lateral component (Vault)
  // is placed beside its Core-KV anchor after the rows are laid out; tier-4
  // infra (the object store) joins the shelf. Lenses and runtime-discovered
  // clients render on shelves, not tiers.
  const tierMembers = [[], [], [], []];
  const ingressMembers = [];
  const lateralMembers = [];
  const lenses = [];
  const shelfInfra = [];
  const clients = [];
  nodes.forEach((n) => {
    if (n.kind === "client") { clients.push(n); return; }
    if (n.lateral) { lateralMembers.push(n); return; }
    const t = sysmapTier(n);
    if (t === -1) { ingressMembers.push(n); return; }
    if (t === 4) { (n.kind === "infra" ? shelfInfra : lenses).push(n); return; }
    tierMembers[t].push(n);
  });

  // Refractor is the left-most tier-3 slot so its project edges drop cleanly
  // into the shelf without crossing the other engines' return paths;
  // Chronicler is right-most — the history mirror opposite Refractor.
  tierMembers[3].sort((a, b) => {
    if (a.id === refractorId) return -1;
    if (b.id === refractorId) return 1;
    if (a.id === "chronicler") return 1;
    if (b.id === "chronicler") return -1;
    return 0;
  });

  ingressMembers.forEach((n, i) => {
    const node = buildSysmapNode(n);
    node.style.left = ((i + 1) / (ingressMembers.length + 1) * width) + "px";
    node.style.top = SYSMAP_INGRESS_Y + "px";
    node.style.transform = "translateX(-50%)";
    stage.appendChild(node);
    sysmap.nodeEls.set(n.id, node);
  });

  for (let t = 0; t < 4; t++) {
    const members = tierMembers[t];
    members.forEach((n, i) => {
      const node = buildSysmapNode(n);
      node.style.left = ((i + 1) / (members.length + 1) * width) + "px";
      node.style.top = SYSMAP_TIER_Y[t] + "px";
      node.style.transform = "translateX(-50%)";
      stage.appendChild(node);
      sysmap.nodeEls.set(n.id, node);
    });
  }

  // Lateral components sit beside their anchor (Vault to the right of
  // Core KV), not in a tier row. Edges are box-measured after layout, so no
  // edge-code change is needed wherever the node lands.
  lateralMembers.forEach((n) => {
    const node = buildSysmapNode(n);
    const anchor = sysmap.nodeEls.get("core-kv");
    if (anchor) {
      node.style.left = (anchor.offsetLeft + anchor.offsetWidth / 2 + 28) + "px";
      node.style.top = SYSMAP_TIER_Y[2] + "px";
    } else {
      // No anchor (empty stack) — fall back to the tier-3 row's right edge.
      node.style.left = (width - 60) + "px";
      node.style.top = SYSMAP_TIER_Y[3] + "px";
      node.style.transform = "translateX(-50%)";
    }
    stage.appendChild(node);
    sysmap.nodeEls.set(n.id, node);
  });

  // Tier 4: the lens shelf — flex-wrap chips, not per-node absolute placement.
  // Tier-4 infra (the object-store archive sink) chips onto the same shelf,
  // after the lenses, so the chronicler → object-store edge drops cleanly.
  const shelf = el("div", "sysmap-shelf");
  shelf.style.top = SYSMAP_TIER_Y[4] + "px";
  if (!lenses.length) {
    shelf.appendChild(el("div", "muted", "(no lenses projecting)"));
  } else {
    lenses.forEach((n) => {
      const chip = buildSysmapNode(n);
      shelf.appendChild(chip);
      sysmap.nodeEls.set(n.id, chip);
    });
  }
  shelfInfra.forEach((n) => {
    const chip = buildSysmapNode(n);
    shelf.appendChild(chip);
    sysmap.nodeEls.set(n.id, chip);
  });
  stage.appendChild(shelf);

  // The clients shelf: undeclared heartbeat groups (vertical apps etc.) —
  // chips only, no skeleton edges; click drills into their component page.
  if (clients.length) {
    const cshelf = el("div", "sysmap-shelf sysmap-clients");
    cshelf.appendChild(el("div", "muted small sysmap-shelf-head", "clients"));
    clients.forEach((n) => {
      const chip = buildSysmapNode(n);
      cshelf.appendChild(chip);
      sysmap.nodeEls.set(n.id, chip);
    });
    stage.appendChild(cshelf);
  }

  // Empty / no-health hint: every component absent (or design-ahead — also
  // heartbeatless) and zero lenses.
  const components = nodes.filter((n) => n.kind === "component");
  if (components.length && components.every((n) => n.status === "absent" || n.status === "design-ahead") && !lenses.length) {
    const hint = el("div", "muted sysmap-hint",
      "No live components reporting — is the stack running? (make up-full)");
    stage.appendChild(hint);
  }

  // Size the stage to fit the shelves (the clients shelf stacks under the
  // lens shelf once its height is measurable), then draw edges after layout.
  requestAnimationFrame(() => {
    const stageNow = sysmapStage();
    if (!stageNow) return;
    const shelves = stageNow.querySelectorAll(".sysmap-shelf");
    let bottom = SYSMAP_TIER_Y[4] + SYSMAP_NODE_H;
    if (shelves.length) {
      bottom = shelves[0].offsetTop + shelves[0].offsetHeight;
      if (shelves.length > 1) {
        shelves[1].style.top = (bottom + 14) + "px";
        bottom += 14 + shelves[1].offsetHeight;
      }
    }
    stageNow.style.minHeight = (bottom + 40) + "px";
    drawSysmapEdges(data);
  });
}

// buildSysmapNode renders one node element for its kind, with the status
// class, inline content, hover tooltip, and (for component/lens) the click
// drill-in.
function buildSysmapNode(n) {
  const node = el("div", "sysmap-node " + n.kind);
  node.dataset.status = n.status || "";
  node.dataset.id = n.id;

  if (n.kind === "component") {
    const cls = componentStatusClass[n.status] || "unknown";
    if (cls === "absent") node.classList.add("absent");
    if (cls === "stale") node.classList.add("stale");
    if (cls === "designahead") node.classList.add("designahead");
    if (n.status === "degraded") node.classList.add("degraded");
    if (n.status === "unhealthy") node.classList.add("unhealthy");
    const head = el("div", "sysmap-node-head");
    head.appendChild(el("span", "sysmap-dot " + cls));
    head.appendChild(el("span", "sysmap-label", n.label));
    if (n.status === "stale") head.appendChild(el("span", "sysmap-tag", "stale"));
    if (n.status === "degraded") head.appendChild(el("span", "sysmap-tag warn", "degraded"));
    if (n.status === "unhealthy") head.appendChild(el("span", "sysmap-tag bad", "unhealthy"));
    if (n.status === "design-ahead") head.appendChild(el("span", "sysmap-tag ahead", "◇ design-ahead"));
    if (n.instances && n.instances.length > 1) head.appendChild(el("span", "sysmap-tag", "×" + n.instances.length));
    if (n.issues && n.issues.length) head.appendChild(el("span", "sysmap-tag warn", "⚠ " + n.issues.length));
    node.appendChild(head);
    if (n.detail) {
      const d = el("div", "sysmap-detail", n.detail);
      node.appendChild(d);
    }
    if (n.freshness) node.appendChild(el("div", "sysmap-freshness", n.freshness));
  } else if (n.kind === "lens") {
    const cls = lensStateDot[n.status] || "dim";
    if (n.status === "pending-readpath") node.classList.add("pending-readpath");
    if (n.status === "fault") node.classList.add("fault");
    node.appendChild(el("span", "sysmap-dot " + cls));
    const g = lensStateGlyph[n.status];
    if (g) node.appendChild(el("span", "sysmap-glyph", g));
    node.appendChild(el("span", "sysmap-label", n.label));
    // A lagging chip pairs its yellow dot with the "lag N" tag (color never
    // stands alone) — N read from the consumerLag issue line.
    if (n.status === "lagging") {
      let lag = "lag";
      (n.issues || []).forEach((i) => {
        const m = /^consumerLag=(\d+)$/.exec(i);
        if (m) lag = "lag " + m[1];
      });
      node.appendChild(el("span", "sysmap-tag warn", lag));
    }
    // The ◆ protected tag is spec-side truth — it renders in EVERY state, not
    // just while pending (a verified protected lens keeps it).
    if (n.protected) node.appendChild(el("span", "sysmap-tag protected", "◆"));
  } else if (n.kind === "client") {
    const cls = componentStatusClass[n.status] || "unknown";
    node.appendChild(el("span", "sysmap-dot " + cls));
    node.appendChild(el("span", "sysmap-label", n.label));
    if (n.instances && n.instances.length > 1) node.appendChild(el("span", "sysmap-tag", "×" + n.instances.length));
  } else { // infra / ingress — a plain labelled chip, no dot, non-interactive
    node.appendChild(el("span", "sysmap-label", n.label));
  }

  if (n.kind === "component" || n.kind === "lens" || n.kind === "client") {
    node.addEventListener("mouseenter", (e) => showSysmapTip(n, e));
    node.addEventListener("mouseleave", hideSysmapTip);
    node.addEventListener("click", () => drillSysmapNode(n));
  }
  return node;
}

// drillSysmapNode routes a node click to the owning view: a component/client
// drills into its page; a lens lands on its lens page.
function drillSysmapNode(n) {
  hideSysmapTip();
  if (n.kind === "lens") {
    navigate("#/lens/" + n.id);
    return;
  }
  navigate("#/component/" + n.id);
}

// showSysmapTip places the shared popover near the hovered node with
// everything that doesn't fit inline: id, kind, status, detail, freshness,
// issues, and — for a lens — a "KV" affordance into Core KV.
function showSysmapTip(n, evt) {
  hideSysmapTip();
  const stage = sysmapStage();
  if (!stage) return;
  const tip = el("div", "sysmap-tip");
  tip.appendChild(el("div", "sysmap-tip-id", n.id));
  const line = (k, v) => { const r = el("div", "sysmap-tip-line"); r.appendChild(el("span", "sysmap-tip-k", k)); r.appendChild(el("span", null, v)); tip.appendChild(r); };
  line("kind", n.kind);
  line("status", n.status);
  if (n.kind === "lens" && n.protected) line("protected", "◆ read-path-authorized");
  if (n.status === "pending-readpath") {
    tip.appendChild(el("div", "sysmap-issue", pendingReadpathCopy));
  }
  if (n.status === "design-ahead") {
    tip.appendChild(el("div", "sysmap-tip-ahead", designAheadCopy));
    if (designAheadPointer[n.id]) {
      tip.appendChild(el("div", "sysmap-tip-ahead", designAheadPointer[n.id]));
    }
  }
  if (n.detail) line("detail", n.detail);
  if (n.freshness) line("freshness", n.freshness);
  (n.issues || []).forEach((i) => tip.appendChild(el("div", /^\[error\]/.test(i) ? "sysmap-issue bad" : "sysmap-issue", i)));
  if (n.kind === "lens") {
    // Primary jump: the lens page. The meta-vertex stays one hop away for the
    // full envelope/provenance view (§1.2 cross-link both ways).
    const page = el("a", "sysmap-tip-kv", "lens page");
    page.addEventListener("click", (e) => {
      e.stopPropagation();
      hideSysmapTip();
      navigate("#/lens/" + n.id);
    });
    tip.appendChild(page);
    const kv = el("a", "sysmap-tip-kv", "meta-vertex in Graph");
    kv.addEventListener("click", (e) => {
      e.stopPropagation();
      hideSysmapTip();
      navigate("#/graph/vtx.meta." + n.id);
    });
    tip.appendChild(kv);
  }

  const node = sysmap.nodeEls.get(n.id);
  if (node) {
    tip.style.left = (node.offsetLeft) + "px";
    tip.style.top = (node.offsetTop + node.offsetHeight + 6) + "px";
  } else if (evt) {
    tip.style.left = evt.offsetX + "px";
    tip.style.top = evt.offsetY + "px";
  }
  stage.appendChild(tip);
  sysmap.tip = tip;
}

function hideSysmapTip() {
  if (sysmap.tip) { sysmap.tip.remove(); sysmap.tip = null; }
}

// drawSysmapEdges measures each node box relative to the stage and draws an
// SVG path per edge. Cleared and rebuilt from boxes each pass (cheap at this
// scale).
function drawSysmapEdges(data) {
  const stage = sysmapStage();
  const svg = document.getElementById("sysmap-edges");
  if (!stage || !svg) return;
  const stageBox = stage.getBoundingClientRect();
  svg.setAttribute("width", stage.clientWidth);
  svg.setAttribute("height", stage.scrollHeight);
  svg.innerHTML = "";

  // One shared arrowhead marker.
  const defs = document.createElementNS(SVG_NS, "defs");
  defs.innerHTML =
    '<marker id="sysmap-arrow" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse">' +
    '<path d="M0,0 L10,5 L0,10 z" fill="var(--border)"/></marker>';
  svg.appendChild(defs);

  // Stage-local box for a node id.
  const box = (id) => {
    const e = sysmap.nodeEls.get(id);
    if (!e) return null;
    const r = e.getBoundingClientRect();
    return { l: r.left - stageBox.left, t: r.top - stageBox.top, w: r.width, h: r.height };
  };
  const tierOf = (id) => {
    const node = (data.nodes || []).find((x) => x.id === id);
    return node ? sysmapTier(node) : -1;
  };

  // The upward "submit ops" returns share one gutter lane near the right edge
  // (clamped inside the stage) so they read as a single secondary return bus
  // rather than fanning off-stage; the bus is labelled once.
  const returnGutter = Math.max(stageBox.width - 52, 0);
  let returnLabelled = false;

  (data.edges || []).forEach((edge) => {
    const a = box(edge.from), b = box(edge.to);
    if (!a || !b) return; // edge resolves only when both endpoints exist
    const ta = tierOf(edge.from), tb = tierOf(edge.to);
    let x1, y1, x2, y2, mx, my, path, secondary = false, suppressLabel = false;

    if (ta === tb) {
      // Same-tier: side-center → side-center, shallow arc.
      const leftFirst = a.l < b.l;
      x1 = leftFirst ? a.l + a.w : a.l;
      x2 = leftFirst ? b.l : b.l + b.w;
      y1 = a.t + a.h / 2; y2 = b.t + b.h / 2;
      const cx = (x1 + x2) / 2;
      path = `M${x1},${y1} C${cx},${y1 - 24} ${cx},${y2 - 24} ${x2},${y2}`;
      mx = cx; my = (y1 + y2) / 2 - 18;
    } else if (ta > tb) {
      // Upward return (submit ops): up the shared right-gutter bus, dimmer +
      // thinner. Labelled once (the lanes overlap into one visual return bus).
      secondary = true;
      x1 = a.l + a.w; y1 = a.t + a.h / 2;
      x2 = b.l + b.w; y2 = b.t + b.h / 2;
      const gutter = returnGutter;
      path = `M${x1},${y1} C${gutter},${y1} ${gutter},${y2} ${x2},${y2}`;
      mx = gutter; my = (y1 + y2) / 2;
      if (returnLabelled) suppressLabel = true;
      returnLabelled = true;
    } else {
      // Downward: bottom-center → top-center, cubic with vertical control.
      x1 = a.l + a.w / 2; y1 = a.t + a.h;
      x2 = b.l + b.w / 2; y2 = b.t;
      const dy = (y2 - y1) * 0.4;
      path = `M${x1},${y1} C${x1},${y1 + dy} ${x2},${y2 - dy} ${x2},${y2}`;
      mx = (x1 + x2) / 2; my = (y1 + y2) / 2;
    }

    const p = document.createElementNS(SVG_NS, "path");
    p.setAttribute("d", path);
    p.setAttribute("class", "sysmap-edge" + (secondary ? " secondary" : ""));
    p.setAttribute("marker-end", "url(#sysmap-arrow)");
    // from/to tags let the pulse animation find the flow path.
    p.dataset.from = edge.from;
    p.dataset.to = edge.to;
    svg.appendChild(p);

    if (edge.label && !suppressLabel) {
      const g = document.createElementNS(SVG_NS, "g");
      const t = document.createElementNS(SVG_NS, "text");
      t.setAttribute("x", mx);
      t.setAttribute("y", my);
      t.setAttribute("class", "sysmap-edge-label");
      t.setAttribute("text-anchor", "middle");
      t.textContent = edge.label;
      // Background rect sized from the text once it is in the DOM.
      const rect = document.createElementNS(SVG_NS, "rect");
      rect.setAttribute("class", "sysmap-edge-label-bg");
      rect.setAttribute("rx", "3");
      g.appendChild(rect);
      g.appendChild(t);
      svg.appendChild(g);
      let tb2 = null;
      try { tb2 = t.getBBox(); } catch (_) { /* not rendered yet */ }
      if (tb2 && tb2.width) {
        rect.setAttribute("x", tb2.x - 3);
        rect.setAttribute("y", tb2.y - 1);
        rect.setAttribute("width", tb2.width + 6);
        rect.setAttribute("height", tb2.height + 2);
      }
    }
  });
}

// ---- Live pulse: the rail feed + edge animation (design §8) ----

// scheduleRenderFeed coalesces feed rebuilds behind one animation frame — a
// chatty stream renders once per frame instead of once per event, and a
// hidden tab (where rAF is paused) defers the rebuild until visible.
function scheduleRenderFeed() {
  if (sysmap.feedRAF) return;
  sysmap.feedRAF = requestAnimationFrame(() => {
    sysmap.feedRAF = 0;
    renderFeed();
  });
}

// renderFeed rebuilds the rail feed from the shared pulse buffer: header
// (LED · rows/min · pause · clear), the degraded-mode note line, and the
// capped row list. A full rebuild is cheap at the 200-row cap; the scroll
// position survives it (an operator reading older rows isn't yanked to the
// top by every arrival).
function renderFeed() {
  const rowsEl = document.getElementById("pulse-rows");
  if (!rowsEl) return;
  const led = document.getElementById("pulse-led");
  if (led) led.className = "pulse-led " + ledClass(pulse.status());
  const rate = document.getElementById("pulse-rate");
  if (rate) rate.textContent = pulse.ratePerMin() + "/min";
  const pauseBtn = document.getElementById("pulse-pause");
  if (pauseBtn) pauseBtn.textContent = pulse.paused() ? "resume" : "pause";

  const note = document.getElementById("pulse-note");
  if (note) {
    if (pulse.status() === "error") {
      note.textContent = "no event stream available — " + (pulse.reason() || "unknown");
      note.className = "small error-text";
    } else if (pulse.status() === "retry") {
      note.textContent = "live tail disconnected — retrying…";
      note.className = "small pulse-note-warn";
    } else {
      note.textContent = "";
      note.className = "muted small";
    }
  }

  const keepScroll = rowsEl.scrollTop;
  rowsEl.innerHTML = "";
  const rows = pulse.rows();
  if (!rows.length) {
    if (pulse.status() === "live") {
      const empty = el("div", "muted small pulse-empty");
      empty.appendChild(document.createTextNode("listening — no events yet. "));
      const a = el("a", null, "Submit an op");
      a.href = "#/op";
      empty.appendChild(a);
      empty.appendChild(document.createTextNode(" to see the flow."));
      rowsEl.appendChild(empty);
    }
    return;
  }
  rows.forEach((r) => rowsEl.appendChild(feedRowEl(r)));
  if (keepScroll) rowsEl.scrollTop = keepScroll;
}

// feedLink renders a compact entity link (shortened display text, full key in
// the title) via the shared key resolver; a non-entity renders as plain text.
function feedLink(key, label) {
  const target = keyTarget(key);
  const text = label || (key.length > 26 ? key.slice(0, 24) + "…" : key);
  if (!target) return el("span", "pulse-key", text);
  const a = el("a", "pulse-key key-link", text);
  a.href = target;
  a.title = key;
  return a;
}

// feedRowEl renders one feed row. Event rows (push, real-time) carry the
// eventType + entity links; derived rows (poll-diff) render dim with the "~"
// honesty prefix. Hovering an event row re-fires the flow highlight.
function feedRowEl(r) {
  if (r.kind === "derived") {
    const row = el("div", "pulse-row derived");
    row.appendChild(el("span", "pulse-time", r.time || ""));
    if (r.href) {
      const a = el("a", "pulse-derived-text", "~ " + r.text);
      a.href = r.href;
      row.appendChild(a);
    } else {
      row.appendChild(el("span", "pulse-derived-text", "~ " + r.text));
    }
    return row;
  }
  const row = el("div", "pulse-row");
  row.appendChild(el("span", "pulse-time", r.time || ""));
  const body = el("span", "pulse-body");
  body.appendChild(el("span", "pulse-type", r.eventType));
  if (r.targetKey) body.appendChild(feedLink(r.targetKey));
  if (r.opKey) body.appendChild(feedLink(r.opKey, "op " + r.opKey.slice(7, 13) + "…"));
  row.appendChild(body);
  if (r.dropped) row.appendChild(el("span", "pulse-drop", "+" + r.dropped + " dropped"));
  row.addEventListener("mouseenter", pulseFlow);
  return row;
}

// pulseFlow animates the flow path on the map (§3.3): core-operations →
// processor, then processor → core-events, then the fan out of core-events —
// CSS-only per edge (a .pulse class), staggered by timeout. Runs only while
// the map is the active view and the document is visible.
function pulseFlow() {
  const panel = document.getElementById("panel-systemmap");
  if (!panel || !panel.classList.contains("active") || document.hidden) return;
  const svg = document.getElementById("sysmap-edges");
  if (!svg) return;
  flashPath(svg.querySelector('path[data-from="core-operations"][data-to="processor"]'), 0);
  flashPath(svg.querySelector('path[data-from="processor"][data-to="core-events"]'), 180);
  svg.querySelectorAll('path[data-from="core-events"]').forEach((p) => flashPath(p, 360));
}

// flashPath restarts the pulse class on one edge after delay ms.
function flashPath(p, delay) {
  if (!p) return;
  setTimeout(() => {
    p.classList.remove("pulse");
    void p.getBoundingClientRect(); // reflow so the animation re-fires
    p.classList.add("pulse");
    setTimeout(() => p.classList.remove("pulse"), 700);
  }, delay);
}

// Auto-refresh: opt-in 10s poll, paused while the tab is backgrounded and
// stopped when the operator leaves the System Map view.
function startSystemMapAuto() {
  if (sysmap.autoTimer) return;
  sysmap.autoTimer = setInterval(() => {
    if (document.hidden) return;
    refreshSystemMap();
  }, 10000);
}
function stopSystemMapAuto() {
  if (sysmap.autoTimer) { clearInterval(sysmap.autoTimer); sysmap.autoTimer = null; }
  const cb = document.getElementById("sysmap-auto");
  if (cb) cb.checked = false;
}

function init() {
  const refresh = document.getElementById("sysmap-refresh");
  if (refresh) refresh.addEventListener("click", refreshSystemMap);
  const auto = document.getElementById("sysmap-auto");
  if (auto) auto.addEventListener("change", () => { auto.checked ? startSystemMapAuto() : stopSystemMapAuto(); });

  const pauseBtn = document.getElementById("pulse-pause");
  if (pauseBtn) pauseBtn.addEventListener("click", () => pulse.setPaused(!pulse.paused()));
  const clearBtn = document.getElementById("pulse-clear");
  if (clearBtn) clearBtn.addEventListener("click", () => pulse.clearRows());

  // Re-measure + redraw on resize (debounced), only while the map is rendered.
  window.addEventListener("resize", () => {
    if (sysmap.resizeTimer) clearTimeout(sysmap.resizeTimer);
    sysmap.resizeTimer = setTimeout(() => {
      const panel = document.getElementById("panel-systemmap");
      if (sysmap.data && panel && panel.classList.contains("active")) {
        renderSystemMap(sysmap.data);
      }
    }, 120);
  });
}

export { init, enter, leave };
