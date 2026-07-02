// Pure ego-graph model for the Graph explorer's neighborhood mode (design
// §7.4): radial layout math, same-relation grouping, and the node-budget
// eviction that keeps a walk from becoming a hairball. Positions in, positions
// out — the DOM layer only paints. No DOM, no fetch; goja-tested.

// adaptiveRadius returns a ring radius that gives n chips of span chipSpan
// (width + gap, px) enough circumference, never below base.
function adaptiveRadius(n, chipSpan, base) {
  if (n <= 0) return base;
  var needed = (n * chipSpan) / (2 * Math.PI);
  return Math.max(base, Math.ceil(needed));
}

// ringPositions spreads n points evenly on a circle around (cx, cy), starting
// at 12 o'clock. Each point carries its angle so a later sector expansion can
// anchor to it.
function ringPositions(n, cx, cy, radius) {
  var pts = [];
  for (var i = 0; i < n; i++) {
    var a = (i / n) * 2 * Math.PI - Math.PI / 2;
    pts.push({ x: cx + radius * Math.cos(a), y: cy + radius * Math.sin(a), angle: a });
  }
  return pts;
}

// sectorPositions spreads n points across an arc of width spread (radians)
// centered on anchorAngle at the given radius from (cx, cy) — the "unfold an
// outer arc near the clicked chip" placement.
function sectorPositions(n, cx, cy, anchorAngle, radius, spread) {
  var pts = [];
  for (var i = 0; i < n; i++) {
    var a = n === 1 ? anchorAngle : anchorAngle - spread / 2 + (i / (n - 1)) * spread;
    pts.push({ x: cx + radius * Math.cos(a), y: cy + radius * Math.sin(a), angle: a });
  }
  return pts;
}

// groupLinkItems folds a vertex's link rows into display items: a bucket of
// more than threshold same-(relation, direction, far-type) links renders as
// one group chip ("identity ×30 (holdsRole)") instead of 30 chips. Buckets at
// or under threshold stay individual links, original order preserved; group
// items append after the singles in first-seen bucket order.
function groupLinkItems(links, threshold) {
  var buckets = {};
  var order = [];
  var i;
  for (i = 0; i < links.length; i++) {
    var l = links[i];
    var bk = l.relation + "|" + l.direction + "|" + l.otherType;
    if (!buckets[bk]) {
      buckets[bk] = [];
      order.push(bk);
    }
    buckets[bk].push(l);
  }
  var items = [];
  for (i = 0; i < links.length; i++) {
    var lk = links[i];
    if (buckets[lk.relation + "|" + lk.direction + "|" + lk.otherType].length <= threshold) {
      items.push({ kind: "single", link: lk });
    }
  }
  for (i = 0; i < order.length; i++) {
    var b = buckets[order[i]];
    if (b.length > threshold) {
      items.push({
        kind: "group",
        relation: b[0].relation,
        direction: b[0].direction,
        otherType: b[0].otherType,
        links: b,
      });
    }
  }
  return items;
}

// evictForBudget picks expansion batches to drop so total node count fits
// budget: oldest first, never batch 0 (the center's own ring) and never a
// protected batch (the newest expansion + its anchor's batch). sizes is the
// node count per batch in expansion order. Returns the indexes to evict; the
// result may still exceed budget when everything else is protected — the
// caller renders what remains (the budget is a soft cap against hairballs,
// not an invariant).
function evictForBudget(sizes, protectedIdxs, budget) {
  var total = 0;
  var i;
  for (i = 0; i < sizes.length; i++) total += sizes[i];
  var isProtected = {};
  for (i = 0; i < protectedIdxs.length; i++) isProtected[protectedIdxs[i]] = true;
  var evict = [];
  for (i = 1; i < sizes.length && total > budget; i++) {
    if (isProtected[i]) continue;
    evict.push(i);
    total -= sizes[i];
  }
  return evict;
}

// hoodSentence renders a link row as the Contract #1 §1.1 sentence
// "source <relation> target" from the perspective of the vertex the row was
// fetched for (direction "out" → that vertex is the source).
function hoodSentence(centerLabel, link, farLabel) {
  if (link.direction === "out") {
    return centerLabel + " " + link.relation + " " + farLabel;
  }
  return farLabel + " " + link.relation + " " + centerLabel;
}

export { adaptiveRadius, ringPositions, sectorPositions, groupLinkItems, evictForBudget, hoodSentence };
