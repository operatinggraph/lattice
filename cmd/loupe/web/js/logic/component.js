// Pure component-page shaping: the metrics line per engine, the events
// summary, and the control-surface selector. No DOM, no fetch — goja-tested
// via cmd/loupe/web_logic_test.go (strip-export load, ES6-conservative).

// num renders a metrics value: numbers as-is, null/undefined/unreadable as "?"
// (a lane whose backlog could not be read is reported null, never a fabricated
// zero — the "?" keeps that honesty on screen).
function num(v) {
  if (typeof v === "number") return String(v);
  if (typeof v === "string" && v !== "") return v;
  return "?";
}

// metricsLine derives the component-appropriate one-line metrics summary from
// a Contract #5 heartbeat doc. Unknown components (clients) get no line — the
// raw doc stays inspectable on the card.
function metricsLine(comp, doc) {
  var m = (doc && doc.metrics) || {};
  if (comp === "processor") {
    return "consumed " + num(m.ops_consumed_total) +
      " · committed " + num(m.ops_committed_total) +
      " · rejected " + num(m.ops_rejected_total) +
      " · lane lag " + num(m.lane_lag_total);
  }
  if (comp === "weaver") {
    var line = "targets " + num(m.targets) + " · marks in flight " + num(m.marksInFlight) +
      " · timers " + num(m.timersScheduled) + " scheduled / " + num(m.timersFired) + " fired";
    if (typeof m.sweepReclaims === "number") line += " · sweep reclaims " + m.sweepReclaims;
    return line;
  }
  if (comp === "loom") {
    return "running instances " + num(m.runningInstances);
  }
  if (comp === "gateway") {
    return "requests " + num(m.requests_total) +
      " · ops submitted " + num(m.ops_submitted_total) +
      " · auth failures " + num(m.auth_failures_total);
  }
  if (comp === "vault") {
    return "DEK cache " + num(m.dek_cache_size) +
      " · vault calls " + num(m.vault_calls_total) +
      " · shreds handled " + num(m.keyshredded_handled_total) +
      " · backend " + num(m.backend);
  }
  if (comp === "refractor") {
    var lags = m.lensLags;
    if (!lags || typeof lags !== "object") return "";
    var total = 0, lagging = 0;
    var ids = Object.keys(lags);
    for (var i = 0; i < ids.length; i++) {
      total++;
      if (typeof lags[ids[i]] === "number" && lags[ids[i]] > 0) lagging++;
    }
    return lagging + "/" + total + " lenses lagging";
  }
  return "";
}

// eventSummaryLabel names an event's summary bucket. claim-attempts events
// count by outcome (the tail segment after the kind — a small enum, per the
// component-page spec's "claim attempts by outcome"); other kinds count as
// the kind alone, since their qualifiers are unbounded ids.
function eventSummaryLabel(ev) {
  var kind = (ev && ev.kind) || "(unknown)";
  if (kind === "claim-attempts" && ev && ev.tail) {
    var segs = String(ev.tail).split(".");
    for (var i = 0; i < segs.length - 1; i++) {
      if (segs[i] === "claim-attempts") return kind + " · " + segs[i + 1];
    }
  }
  return kind;
}

// eventSummary rolls component events up into per-bucket counts, largest
// first (ties by name) — the processor page's de-facto observability panel.
// The counter map is prototype-free so a kind named like an Object.prototype
// member cannot corrupt a count.
function eventSummary(events) {
  var counts = Object.create(null);
  var list = events || [];
  for (var i = 0; i < list.length; i++) {
    var label = eventSummaryLabel(list[i]);
    counts[label] = (counts[label] || 0) + 1;
  }
  var labels = Object.keys(counts);
  var out = [];
  for (var j = 0; j < labels.length; j++) out.push({ kind: labels[j], count: counts[labels[j]] });
  out.sort(function (a, b) {
    if (a.count !== b.count) return b.count - a.count;
    return a.kind < b.kind ? -1 : 1;
  });
  return out;
}

// controlSurface selects the right-column widget set for a component page:
// the engines with a control plane get their row-level surfaces, the
// processor's panel hosts its events summary, the gateway's hosts the
// token-revocation kill-switch, everything else (bridge,
// object-store-manager, runtime-discovered clients) is read-only.
function controlSurface(comp) {
  if (comp === "loom" || comp === "weaver" || comp === "refractor") return comp;
  if (comp === "processor") return "events";
  if (comp === "gateway") return "gateway";
  return "none";
}

export { num, metricsLine, eventSummaryLabel, eventSummary, controlSurface };
