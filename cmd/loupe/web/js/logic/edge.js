// Pure decision logic for the Edge fleet view. No DOM, no fetch —
// goja-tested via cmd/loupe/web_logic_edge_test.go (strip-export load).
//
// The load-bearing rule throughout: a question the platform cannot answer must
// render as UNKNOWN, never as a clean all-clear. `gapped: null` means "no cursor to compare" or "no readable
// stream" — it never means "not gapped". Edge nodes structurally cannot
// self-report health (their per-identity permission set admits only their own
// sync subject and control RPCs), so every signal here is inferred
// server-side, and the view says which inference it made.

// gapVerdict classifies one device's sync-gap state.
//   "gapped"  — the SYNC stream's retention floor has overtaken this device's
//               ack floor; the deltas between them aged out and a warm resume
//               would silently miss them.
//   "current" — the device's ack floor is still inside the retention window.
//   "unknown" — unanswerable (the device has no SYNC durable, or the stream
//               could not be read).
function gapVerdict(device) {
  var d = device || {};
  if (d.gapped === null || d.gapped === undefined) {
    return { state: "unknown", behindBy: 0 };
  }
  return { state: d.gapped ? "gapped" : "current", behindBy: d.gapped ? d.behindBy || 0 : 0 };
}

// gapLabel is the human line for a verdict. An unknown verdict names WHY it is
// unknown, because "unknown" alone reads as a bug rather than a fact.
function gapLabel(device, streamKnown) {
  var d = device || {};
  var v = gapVerdict(d);
  if (v.state === "gapped") {
    // behindBy 0 is the retention boundary: the device's position predates the
    // window but nothing between it and the floor was actually lost. Saying
    // "0 messages aged out" alongside a red chip reads as a contradiction, so
    // the boundary case names itself.
    if (!v.behindBy) return "gapped · at the retention boundary";
    return "gapped · " + v.behindBy + " message" + (v.behindBy === 1 ? "" : "s") + " aged out";
  }
  if (v.state === "current") return "within retention window";
  if (!streamKnown) return "gap unknown — SYNC stream unreadable";
  // A lookup that BROKE is not evidence the device never attached; saying so
  // would state a fact about the device from a measurement that never
  // happened, which is the same lie as a fabricated all-clear.
  if (d.attachmentUnknown) return "gap unknown — consumer lookup failed";
  if (!d.subscribed) return "gap unknown — never attached to the stream";
  return "gap unknown";
}

// hydrationNote describes the device's last hydration checkpoint. This is the
// Refractor pipeline's own progress sequence, NOT a SYNC stream position — the
// two are different sequence spaces, so the label names which one it is rather
// than letting it read as a second, contradictory sync position.
function hydrationNote(device) {
  var d = device || {};
  if (!d.revisionCursor) return "";
  return "last hydrated at pipeline seq " + d.revisionCursor;
}

// interestSummary describes a device's Interest Set. An EMPTY filter is a
// wider subscription, not a narrower one — personalinterest's own rule is
// "absence is never a denial": no declared types and no declared anchors
// admits everything the identity is authorized for. Rendering that as "no
// interests" would invert its meaning.
function interestSummary(device) {
  var d = device || {};
  var types = d.types || [];
  var anchors = d.anchors || [];
  if (d.unreadable) {
    // The read broke; the document itself is not implicated.
    return "interest set unknown — registration could not be read";
  }
  if (d.malformed) {
    // An unparseable registration document tells us nothing about its filter.
    // Falling through would assert the WIDEST possible subscription about a
    // document nobody could read — a security-relevant claim from no evidence.
    return "interest set unknown — registration document unreadable";
  }
  if (!types.length && !anchors.length) {
    return "unfiltered — receives everything this identity is authorized for";
  }
  var parts = [];
  if (types.length) parts.push(types.length + " type" + (types.length === 1 ? "" : "s") + ": " + types.join(", "));
  if (anchors.length) parts.push(anchors.length + " anchor" + (anchors.length === 1 ? "" : "s"));
  return parts.join(" · ");
}

// groupByIdentity collapses the flat device list into per-identity groups,
// preserving the server's gapped-first ordering: a group sorts to the position
// of its first device, so an identity with a gapped device stays at the top.
function groupByIdentity(devices) {
  var list = devices || [];
  var order = [];
  var byID = {};
  for (var i = 0; i < list.length; i++) {
    var d = list[i];
    var id = d.identityId || "";
    if (!byID[id]) {
      byID[id] = { identityId: id, identityKey: d.identityKey || "", devices: [], gapped: 0 };
      order.push(byID[id]);
    }
    byID[id].devices.push(d);
    if (d.gapped === true) byID[id].gapped++;
  }
  return order;
}

// fleetHeadline is the one-line status summary above the roster. It reports
// what is KNOWN and separately what is unmeasured, rather than folding the
// unmeasured into a healthy count.
function fleetHeadline(fleet) {
  var f = fleet || {};
  var count = f.count || 0;
  if (!count) return "No devices registered.";
  var parts = [
    count + " device" + (count === 1 ? "" : "s") +
      " across " + (f.identities || 0) + " identit" + ((f.identities || 0) === 1 ? "y" : "ies"),
  ];
  if (!f.streamKnown) {
    parts.push("gap state unknown for all (no readable SYNC stream)");
    return parts.join(" · ");
  }
  var unknown = f.unknown || 0;
  // "0 gapped" alongside N unmeasured devices reads as an all-clear it has not
  // earned, so an all-unknown fleet never prints a gapped count at all, and a
  // partially-measured one always carries the unmeasured remainder.
  if (unknown >= count) {
    parts.push("gap state unknown for all " + count);
    return parts.join(" · ");
  }
  parts.push((f.gapped || 0) + " gapped");
  if (unknown) parts.push(unknown + " unknown");
  var unsub = f.unsubscribed || 0;
  if (unsub) parts.push(unsub + " not attached to " + (f.stream || "the stream"));
  return parts.join(" · ");
}

// retentionLine describes the window every gap verdict is measured against.
// Returns "" when there is no stream to describe.
function retentionLine(fleet) {
  var f = fleet || {};
  if (!f.streamKnown) return "";
  var first = f.firstSeq || 0;
  var last = f.lastSeq || 0;
  var name = f.stream || "?";
  // An empty stream reports firstSeq = lastSeq + 1 in NATS, and a brand-new one
  // reports 0/0. Printing either as a range ("101–100", "0–0") reads as
  // corruption, so an empty window says it is empty instead.
  if (last < first || last === 0) {
    return "Stream " + name + " holds no messages, so no device can be behind its retention window yet.";
  }
  var held = last - first + 1;
  return "Stream " + name + " retains sequences " + first + "–" + last +
    " (" + held + " message" + (held === 1 ? "" : "s") + "). A device is gapped once its position falls below " + first + ".";
}

// filterEmptyMessage is what the roster says when the "gapped only" filter
// leaves nothing to show. An empty filtered list is NOT an all-clear when some
// devices' gap state could not be determined — those were hidden by the filter,
// not cleared by it, so the count of hidden unknowns is stated.
function filterEmptyMessage(devices) {
  var list = devices || [];
  var unknown = 0;
  for (var i = 0; i < list.length; i++) {
    var g = list[i].gapped;
    if (g === null || g === undefined) unknown++;
  }
  if (!unknown) return "(no gapped devices)";
  return "(no gapped devices — but " + unknown + " device" + (unknown === 1 ? "'s" : "s'") +
    " gap state could not be determined, so this is not an all-clear)";
}

// staleWarning is the standing caveat this roster always carries: registration
// is durable and nothing garbage-collects it, so a device that vanished
// without a clean deregister keeps its row forever. This is a REGISTRATION
// roster, never a liveness view — no connection state is observable to any
// component today.
function staleWarning(fleet) {
  var f = fleet || {};
  if (!(f.count || 0)) return "";
  return "Registrations are durable and never expire — a device that disappeared without deregistering still lists here. " +
    "This is who is registered, not who is connected; edge nodes cannot self-report and no connection state is observable.";
}

// formatSpan renders a duration in seconds as its coarsest unit. Used for both
// directions of the clock (how long until the floor moves, how long since a
// device last acked), so it never carries a tense of its own.
function formatSpan(seconds) {
  var s = Math.floor(seconds);
  if (!(s > 0)) return "0s";
  if (s < 60) return s + "s";
  var m = Math.floor(s / 60);
  if (m < 60) return m + "m";
  var h = Math.floor(m / 60);
  if (h < 24) return h + "h";
  return Math.floor(h / 24) + "d";
}

// spanBetween is the seconds between two ISO-8601 instants, or null when either
// is missing or unparsable. Every clock in this module runs off the SERVER's
// `now` rather than the browser's: the sequences and the timestamps were read
// on that clock, and a skewed visitor clock would otherwise show a device as
// having headroom past the point it has any.
function spanBetween(fromIso, toIso) {
  if (!fromIso || !toIso) return null;
  var a = Date.parse(fromIso), b = Date.parse(toIso);
  if (isNaN(a) || isNaN(b)) return null;
  return (b - a) / 1000;
}

// estimatedPublishTime interpolates the publish instant of one stream sequence
// from the window's two known endpoints. It is an ESTIMATE and every caller
// labels it as one: it assumes messages were published at an even spacing
// across the retained window, which a bursty stream was not.
//
// Returns null rather than a guess whenever the window cannot support the
// interpolation: unreadable endpoints, or a sequence outside the window. A
// one-message window is NOT such a case — that message's own publish time is
// known outright, so the answer there is exact rather than interpolated.
function estimatedPublishTime(fleet, seq) {
  var f = fleet || {};
  if (!f.streamKnown || !f.firstTime || !f.lastTime) return null;
  var first = Date.parse(f.firstTime), last = Date.parse(f.lastTime);
  if (isNaN(first) || isNaN(last) || last < first) return null;
  var lo = f.firstSeq || 0, hi = f.lastSeq || 0;
  if (seq < lo || seq > hi) return null;
  if (hi === lo) return seq === lo ? first : null;
  return first + ((seq - lo) / (hi - lo)) * (last - first);
}

// floorClock is the one EXACT time fact this view has: the oldest retained
// message dies at its own publish time plus the stream's max age, and that is
// when the retention floor advances. Returns seconds (never negative — a floor
// past due is imminent, not overdue) or null when the stream is not age-limited
// and so has no deadline to report at all.
function floorClock(fleet) {
  var f = fleet || {};
  if (!f.streamKnown || !f.firstTime) return null;
  var maxAge = f.maxAgeSeconds || 0;
  if (maxAge <= 0) return null;
  var elapsed = spanBetween(f.firstTime, f.now);
  if (elapsed === null) return null;
  // Clamped at BOTH ends: a floor already past due is imminent, never negative,
  // and clock skew between Loupe and the stream's own timestamps must never
  // report more headroom than the retention period itself grants.
  return Math.min(maxAge, Math.max(0, maxAge - elapsed));
}

// floorClockLine states what moves the floor. A stream with no age limit gets
// an explicit refusal to predict rather than silence, because silence next to a
// headroom number reads as "and it will hold".
function floorClockLine(fleet) {
  var f = fleet || {};
  if (!f.streamKnown) return "";
  var maxAge = f.maxAgeSeconds || 0;
  if (maxAge <= 0) {
    return "Stream " + (f.stream || "?") + " has no age limit, so its floor advances only under size or " +
      "message-limit pressure — there is no deadline to report.";
  }
  var left = floorClock(f);
  if (left === null) return "";
  return "Retention is " + formatSpan(maxAge) + "; the oldest retained message ages out in " +
    (left > 0 ? formatSpan(left) : "under a second") + ", which is when the floor next advances.";
}

// timeHeadroom estimates how long before the retention floor reaches THIS
// device's position: the interpolated publish time of the message it has acked
// through, plus the stream's max age. Null whenever any input is missing — an
// unmeasurable deadline is stated as such, never rendered as a long one.
function timeHeadroom(device, fleet) {
  var d = device || {}, f = fleet || {};
  if (d.headroom === null || d.headroom === undefined) return null;
  var maxAge = f.maxAgeSeconds || 0;
  if (maxAge <= 0) return null;
  // A headroom of 0 means the device sits BELOW the oldest retained sequence,
  // so the next message it stands to lose is the floor itself — and the floor's
  // own death is exact, not interpolated. The tightest row on the fleet is the
  // one that most needs a deadline, so it gets the exact one.
  if (!d.headroom) return floorClock(f);
  var at = estimatedPublishTime(f, d.ackFloor || 0);
  if (at === null) return null;
  var now = Date.parse(f.now || "");
  if (isNaN(now)) return null;
  return Math.min(maxAge, Math.max(0, maxAge - (now - at) / 1000));
}

// headroomLabel is the triage number for a device that is NOT yet gapped: how
// far the floor can advance before it starts discarding deltas this device has
// not consumed, plus the estimated time that buys. The time half is always
// marked "~" — it rides estimatedPublishTime's even-spacing assumption, and an
// operator acting on it deserves to know which half is measured.
function headroomLabel(device, fleet) {
  var d = device || {};
  if (d.headroom === null || d.headroom === undefined) return "";
  var n = d.headroom;
  var line = n + " message" + (n === 1 ? "" : "s") + " of headroom";
  if (!n) line = "on the retention floor — the next discard starts costing it deltas";
  var secs = timeHeadroom(d, fleet);
  if (secs !== null) line += " · ~" + formatSpan(secs) + " at the window's observed message spacing";
  return line;
}

// lastAckLabel reports the device's ack activity — the only liveness-adjacent
// signal this roster carries at all, since edge nodes cannot self-report.
//
// Its ABSENCE is not evidence: JetStream keeps `ack_floor.last_active` in
// process-local consumer state that a server restart zeroes, so a missing
// timestamp means "no ack seen since this consumer's state was last reset",
// never "this device has never acked". The label says which one it is.
function lastAckLabel(device, fleet) {
  var d = device || {}, f = fleet || {};
  if (!d.subscribed) return "";
  if (!d.lastAckAt) return "no ack recorded since this consumer's state was last reset";
  var secs = spanBetween(d.lastAckAt, f.now);
  if (secs === null) return "last acked " + d.lastAckAt;
  return "last acked " + formatSpan(secs) + " ago";
}

// interestTerms flattens a device's Interest Set into renderable rows. An
// unreadable registration yields NOTHING rather than an empty (== unfiltered)
// set, for the same reason interestSummary refuses to call it unfiltered.
function interestTerms(device) {
  var d = device || {};
  if (d.malformed || d.unreadable) return [];
  var out = [];
  var types = d.types || [], anchors = d.anchors || [];
  for (var i = 0; i < types.length; i++) out.push({ kind: "type", value: types[i] });
  for (var j = 0; j < anchors.length; j++) out.push({ kind: "anchor", value: anchors[j] });
  return out;
}

// triageLine is the headline's second half: which device to look at first. It
// takes the RENDERED list rather than the whole fleet — under the "gapped only"
// filter a fleet-wide summary would name a device that is not on the page and
// cannot be clicked, which is worse than no summary at all.
//
// It names the head of each tier rather than re-deriving an order: the roster
// is already ordered worst-first, and a summary that disagreed with the list
// beneath it would send an operator to the wrong row.
function triageLine(fleet, devices) {
  var f = fleet || {};
  var list = devices || f.devices || [];
  if (!f.streamKnown || !list.length) return "";
  var worst = null, tightest = null;
  for (var i = 0; i < list.length; i++) {
    var d = list[i];
    if (d.gapped === true) {
      if (!worst || (d.behindBy || 0) > (worst.behindBy || 0)) worst = d;
    } else if (d.gapped === false && d.headroom !== null && d.headroom !== undefined) {
      if (!tightest || d.headroom < tightest.headroom) tightest = d;
    }
  }
  var parts = [];
  if (worst) {
    parts.push("worst: " + (worst.deviceId || "(unnamed)") + " lost " + (worst.behindBy || 0) +
      " message" + ((worst.behindBy || 0) === 1 ? "" : "s"));
  }
  if (tightest) {
    var secs = timeHeadroom(tightest, f);
    parts.push("tightest still-current: " + (tightest.deviceId || "(unnamed)") + " at " +
      tightest.headroom + " message" + (tightest.headroom === 1 ? "" : "s") +
      (secs === null ? "" : " (~" + formatSpan(secs) + ")"));
  }
  return parts.join(" · ");
}

// headroomSpread summarises the fleet by the thing the floor actually consumes
// — how much room its still-current devices have — instead of by identity
// order. It reports only the devices whose headroom was MEASURED, and says how
// many were not, so the range can never read as covering the whole fleet.
function headroomSpread(fleet, devices) {
  var f = fleet || {};
  var list = devices || f.devices || [];
  if (!f.streamKnown || !list.length) return "";
  var measured = [], unmeasured = 0;
  for (var i = 0; i < list.length; i++) {
    var h = list[i].headroom;
    if (h === null || h === undefined) {
      // Gapped devices are not "unmeasured" — their damage is reported as
      // messages lost, and folding them in here would double-count them.
      if (list[i].gapped !== true) unmeasured++;
      continue;
    }
    measured.push(h);
  }
  if (!measured.length) {
    return unmeasured ? "No still-current device has a measurable headroom." : "";
  }
  measured.sort(function (a, b) { return a - b; });
  var lo = measured[0], hi = measured[measured.length - 1];
  var line = measured.length + " still-current device" + (measured.length === 1 ? "" : "s") +
    (lo === hi ? " holding " + lo + " message" + (lo === 1 ? "" : "s") + " of headroom"
               : " holding between " + lo + " and " + hi + " messages of headroom");
  if (unmeasured) line += " · " + unmeasured + " more unmeasured";
  return line + ".";
}

// siblingLine describes the identity's OTHER registrations on a device panel.
// The distinction it draws is the point of the list: one gapped device beside
// current ones is a device that stopped attaching, whereas an identity gapped
// across the board is its lens.
function siblingLine(siblings) {
  var list = siblings || [];
  if (!list.length) return "This identity has no other registered device.";
  var gapped = 0, measured = 0;
  for (var i = 0; i < list.length; i++) {
    if (list[i].gapped === true) gapped++;
    if (list[i].gapped !== null && list[i].gapped !== undefined) measured++;
  }
  var line = list.length + " other device" + (list.length === 1 ? "" : "s") + " on this identity";
  if (!measured) return line + " · none of them measurable, so they cannot narrow this down";
  if (gapped === measured) return line + " · every measured one is gapped too, so this is the identity's lens, not one device";
  if (!gapped) return line + " · every measured one is current, so this looks device-local";
  return line + " · " + gapped + " of " + measured + " measured also gapped";
}

export { gapVerdict, gapLabel, hydrationNote, interestSummary, groupByIdentity, fleetHeadline, retentionLine, filterEmptyMessage, staleWarning, formatSpan, spanBetween, estimatedPublishTime, floorClock, floorClockLine, timeHeadroom, headroomLabel, lastAckLabel, interestTerms, triageLine, headroomSpread, siblingLine };
