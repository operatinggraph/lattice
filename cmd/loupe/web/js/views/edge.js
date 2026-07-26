// Edge view: the Personal Lens / Edge Lattice device roster. One card per
// registered device, grouped by identity and ordered worst-first, with a
// per-device panel at #/edge/<identityId>.<deviceId>.
//
// All decision logic lives in logic/edge.js (goja-tested); this file is the
// DOM binding. Every identity is a keyLink into the Graph explorer (design
// §1.2 — no dead ends). Read-only throughout: the remedy for a gapped device
// is its own next attach, and the tab says so rather than offering a button
// the platform cannot honour (loupe-flows-edge-depth-ux.md §3.2).

import { $, el, api, setStatus } from "../api.js";
import { keyLinkEl } from "../render.js";
import { navigate } from "../router.js";
import {
  gapVerdict,
  gapLabel,
  hydrationNote,
  interestSummary,
  interestTerms,
  groupByIdentity,
  fleetHeadline,
  retentionLine,
  filterEmptyMessage,
  staleWarning,
  headroomLabel,
  lastAckLabel,
  floorClockLine,
  triageLine,
  headroomSpread,
  siblingLine,
  formatSpan,
  spanBetween,
} from "../logic/edge.js";

const state = { loaded: false, generation: 0, arg: "" };

// enter routes between the roster (#/edge) and one device's panel
// (#/edge/<key>) — the same one-panel/two-modes shape Flows uses, so a drill-in
// is a route and stays linkable.
function enter(route) {
  const arg = (route && route.arg) || "";
  state.arg = arg;
  if (arg) {
    loadDevice(arg);
    return;
  }
  $("#edge-detail").classList.remove("visible");
  $("#edge-list").classList.add("visible");
  if (state.loaded) return;
  state.loaded = true;
  loadFleet();
}

async function loadFleet() {
  // Two overlapping loads (a double-clicked Refresh, or Refresh racing the
  // filter's change handler) would otherwise repaint in completion order, so a
  // slow earlier response could overwrite a fast later one. Only the newest
  // request is allowed to touch the DOM.
  const generation = ++state.generation;
  $("#edge-detail").classList.remove("visible");
  $("#edge-list").classList.add("visible");
  setStatus("edge-status-msg", "loading…");
  const body = await api("/api/edge/fleet");
  if (generation !== state.generation) return;

  const host = $("#edge-groups");
  host.innerHTML = "";
  $("#edge-notes").innerHTML = "";
  $("#edge-retention").textContent = "";
  $("#edge-triage").textContent = "";
  if (body.error) {
    setStatus("edge-status-msg", body.error, true);
    return;
  }
  setStatus("edge-status-msg", fleetHeadline(body));

  // Notes are the server's reasons a verdict could not be produced — an
  // absent measurement is stated, never quietly rendered as healthy.
  const notes = (body.notes || []).slice();
  const stale = staleWarning(body);
  if (stale) notes.push(stale);
  notes.forEach((n) => $("#edge-notes").appendChild(el("div", "small muted", n)));

  const all = body.devices || [];
  const gappedOnly = $("#edge-gapped-only").checked;
  const devices = gappedOnly ? all.filter((d) => d.gapped === true) : all;

  // The retention window, what moves its floor, how much room the fleet has,
  // and which device to look at first. The last two read the RENDERED list: a
  // summary naming a device the filter has hidden points at a row that is not
  // on the page.
  $("#edge-retention").textContent = [retentionLine(body), floorClockLine(body)].filter(Boolean).join(" ");
  $("#edge-triage").textContent =
    [triageLine(body, devices), headroomSpread(body, devices)].filter(Boolean).join(" · ");

  if (!devices.length) {
    host.appendChild(el("div", "muted", gappedOnly ? filterEmptyMessage(all) : "(no devices registered)"));
    return;
  }

  groupByIdentity(devices).forEach((group) => {
    const section = el("div", "card edge-identity" + (group.gapped ? " red" : ""));
    const head = el("div", "card-key");
    head.appendChild(keyLinkEl(group.identityKey));
    // Under the filter this count is a subset, so it is labelled as one rather
    // than reading as the identity's whole device list.
    const n = group.devices.length;
    head.appendChild(
      el("span", "card-group", gappedOnly ? n + " shown" : n + " device" + (n === 1 ? "" : "s"))
    );
    if (group.gapped) head.appendChild(el("span", "card-group badge-stuck", group.gapped + " gapped"));
    section.appendChild(head);
    group.devices.forEach((d) => section.appendChild(deviceRow(d, body)));
    host.appendChild(section);
  });
}

// deviceRow renders one roster device. The gap chip is the triage signal; the
// meta line below shows the working, so an operator who distrusts the chip can
// see what it was computed from. The Interest Set expands in place — an
// operator scanning for "which devices even subscribe to leases" should not
// have to open every panel to find out.
function deviceRow(d, fleet) {
  const v = gapVerdict(d);
  const row = el("div", "edge-device");

  const title = el("div", "card-sub");
  const name = el("button", "edge-device-id linkish", d.deviceId || "(unnamed device)");
  name.addEventListener("click", () => navigate("#/edge/" + d.key));
  title.appendChild(name);
  const chipClass =
    v.state === "gapped" ? "badge-gapped" : v.state === "current" ? "badge-available" : "badge-unknown";
  title.appendChild(el("span", chipClass, gapLabel(d, fleet.streamKnown)));
  if (d.unreadable) title.appendChild(el("span", "badge-unknown", "registration read failed"));
  else if (d.malformed) title.appendChild(el("span", "badge-gapped", "unreadable registration"));
  row.appendChild(title);

  const terms = interestTerms(d);
  if (terms.length) {
    // Only a filter that HAS terms is worth expanding; "unfiltered" and
    // "unreadable" are already complete statements.
    const summary = el("div", "small muted");
    const toggle = el("button", "linkish", interestSummary(d) + " ▸");
    const list = el("div", "edge-interest-terms");
    terms.forEach((t) => list.appendChild(termRow(t)));
    toggle.addEventListener("click", () => {
      const open = list.classList.toggle("visible");
      toggle.textContent = interestSummary(d) + (open ? " ▾" : " ▸");
    });
    summary.appendChild(toggle);
    row.appendChild(summary);
    row.appendChild(list);
  } else {
    row.appendChild(el("div", "small muted", interestSummary(d)));
  }

  row.appendChild(deviceMeta(d, fleet));
  return row;
}

// termRow renders one Interest Set entry. An anchor is an entity key, so it is
// a keyLink — a filter naming an entity an operator cannot open is exactly the
// dead end design §1.2 rules out.
function termRow(t) {
  const line = el("div", "edge-interest-term");
  line.appendChild(el("span", "card-group", t.kind));
  line.appendChild(t.kind === "anchor" ? keyLinkEl(t.value) : el("span", null, t.value));
  return line;
}

// deviceMeta is the working behind the chip: attachment, the headroom the
// verdict was measured against, and the device's own ack activity.
function deviceMeta(d, fleet) {
  const meta = el("div", "card-meta small");
  if (!fleet.streamKnown) {
    // Attachment is read through the stream, so with no readable stream it is
    // unknown — not absent. Claiming "no SYNC consumer" here would contradict
    // the headline, which correctly reports the whole fleet as unmeasured.
    meta.appendChild(el("span", "muted", "attachment unknown"));
  } else if (d.subscribed) {
    meta.appendChild(el("span", null, "attached · acked through " + (d.ackFloor || 0)));
    if (d.pending) meta.appendChild(el("span", null, d.pending + " pending"));
    const headroom = headroomLabel(d, fleet);
    if (headroom) meta.appendChild(el("span", d.headroom ? null : "error-text", headroom));
    const ack = lastAckLabel(d, fleet);
    if (ack) meta.appendChild(el("span", "muted", ack));
  } else if (d.attachmentUnknown) {
    meta.appendChild(el("span", "muted", "attachment unmeasured — consumer lookup failed"));
  } else {
    meta.appendChild(el("span", "muted", "no SYNC consumer"));
  }
  const hydration = hydrationNote(d);
  if (hydration) meta.appendChild(el("span", "muted", hydration));
  if (d.registeredAt) meta.appendChild(el("span", "muted", "registered " + d.registeredAt));
  return meta;
}

// loadDevice renders one device's panel: its position against the retention
// window, its full Interest Set, its registration provenance, and the
// identity's other devices.
async function loadDevice(key) {
  $("#edge-list").classList.remove("visible");
  const panel = $("#edge-detail");
  panel.classList.add("visible");
  panel.innerHTML = "";
  panel.appendChild(el("div", "muted", "loading device…"));
  const body = await api("/api/edge/device?key=" + encodeURIComponent(key));
  if (state.arg !== key) return; // navigated away
  panel.innerHTML = "";
  if (body.error) {
    panel.appendChild(el("div", "card-issue bad", body.error));
    return;
  }
  const d = body.device || {};
  const v = gapVerdict(d);

  const head = el("div", "flow-detail-head");
  head.appendChild(el("span", "flow-group-name", d.deviceId || "(unnamed device)"));
  head.appendChild(keyLinkEl(d.identityKey));
  head.appendChild(el(
    "span",
    v.state === "gapped" ? "badge-gapped" : v.state === "current" ? "badge-available" : "badge-unknown",
    gapLabel(d, body.streamKnown),
  ));
  panel.appendChild(head);

  (body.notes || []).forEach((n) => panel.appendChild(el("div", "card-issue", n)));
  if (v.state === "gapped") {
    // The one place this panel must not overstate itself: there is no
    // operator-initiated hydration, so the remedy is named as the device's own.
    panel.appendChild(el("div", "card-issue",
      "Deltas this device had not consumed have aged out, so a warm resume would silently miss them — it needs a " +
      "cold personal.hydrate, which only the device itself can start on its next attach. The console cannot " +
      "trigger one: edge nodes cannot self-report and no connection state is observable."));
  }
  if (d.unreadable) {
    panel.appendChild(el("div", "card-issue",
      "This device's registration could not be read, so its Interest Set is unknown — not empty, and not " +
      "necessarily damaged: the read itself failed."));
  } else if (d.malformed) {
    panel.appendChild(el("div", "card-issue bad",
      "This device's registration document could not be parsed, so its Interest Set is unknown — not empty."));
  }

  panel.appendChild(factRow("registration", d.key || key));
  panel.appendChild(factRow("identity", keyLinkEl(d.identityKey)));
  if (d.registeredAt) panel.appendChild(factRow("registered", d.registeredAt + registeredAgo(d, body)));
  if (d.revisionCursor) panel.appendChild(factRow("hydration checkpoint", hydrationNote(d)));
  if (d.consumerCreatedAt) panel.appendChild(factRow("consumer created", d.consumerCreatedAt));

  panel.appendChild(positionPanel(d, body));
  panel.appendChild(interestPanel(d));
  panel.appendChild(siblingPanel(body));
}

// registeredAgo appends the elapsed time to a registration timestamp. A raw
// ISO instant does not answer "is this a stale row from months ago", which on a
// roster nothing garbage-collects is the question actually being asked.
function registeredAgo(d, fleet) {
  const secs = spanBetween(d.registeredAt, fleet.now);
  return secs === null ? "" : " (" + formatSpan(secs) + " ago)";
}

// positionPanel is the device's ack position against the retention floor, in
// messages and — where the stream is age-limited — in estimated time.
function positionPanel(d, fleet) {
  const box = el("div", "flow-steps");
  box.appendChild(el("div", "comp-section-head", "Position in the SYNC stream"));
  if (!fleet.streamKnown) {
    box.appendChild(el("div", "muted small",
      "No readable SYNC stream, so this device has no comparable position and no gap verdict."));
    return box;
  }
  const line = retentionLine(fleet);
  if (line) box.appendChild(el("div", "muted small", line));
  const clock = floorClockLine(fleet);
  if (clock) box.appendChild(el("div", "muted small", clock));
  if (!d.subscribed) {
    box.appendChild(el("div", "muted small", d.attachmentUnknown
      ? "The consumer lookup for this device failed, so whether it is attached to " +
        (fleet.stream || "the stream") + " — and therefore its gap state — went unmeasured. This is not " +
        "evidence that it never attached."
      : "This device has no durable consumer on " + (fleet.stream || "the stream") +
        ", so it has no position comparable to the floor — its gap state is unmeasured, not clear."));
    return box;
  }
  box.appendChild(factRow("acked through", String(d.ackFloor || 0)));
  if (d.delivered) box.appendChild(factRow("delivered through", String(d.delivered)));
  if (d.pending) box.appendChild(factRow("pending", String(d.pending)));
  if (d.gapped === true) {
    box.appendChild(factRow("messages aged out", String(d.behindBy || 0)));
  } else {
    const headroom = headroomLabel(d, fleet);
    if (headroom) box.appendChild(factRow("headroom", headroom));
  }
  const ack = lastAckLabel(d, fleet);
  if (ack) box.appendChild(factRow("ack activity", ack));
  return box;
}

// interestPanel lists the Interest Set in full — the terms themselves, not a
// count, which is the whole reason to open a device.
function interestPanel(d) {
  const box = el("div", "flow-steps");
  box.appendChild(el("div", "comp-section-head", "Interest Set"));
  box.appendChild(el("div", "muted small", interestSummary(d)));
  interestTerms(d).forEach((t) => box.appendChild(termRow(t)));
  return box;
}

// siblingPanel is the identity's other registrations, each drillable. It exists
// to answer one question the roster cannot: is this device broken, or is its
// identity's whole lens behind?
function siblingPanel(body) {
  const box = el("div", "flow-steps");
  const siblings = body.siblings || [];
  box.appendChild(el("div", "comp-section-head", "Other devices on this identity"));
  box.appendChild(el("div", "muted small", siblingLine(siblings)));
  siblings.forEach((s) => {
    const v = gapVerdict(s);
    const line = el("div", "edge-sibling");
    const name = el("button", "linkish", s.deviceId || "(unnamed device)");
    name.addEventListener("click", () => navigate("#/edge/" + s.key));
    line.appendChild(name);
    line.appendChild(el(
      "span",
      v.state === "gapped" ? "badge-gapped" : v.state === "current" ? "badge-available" : "badge-unknown",
      gapLabel(s, body.streamKnown),
    ));
    box.appendChild(line);
  });
  return box;
}

function factRow(k, v) {
  const r = el("div", "lens-kvrow");
  r.appendChild(el("span", "lens-k", k));
  if (typeof v === "string") r.appendChild(el("span", null, v));
  else r.appendChild(v);
  return r;
}

function init() {
  $("#edge-load").addEventListener("click", loadFleet);
  $("#edge-gapped-only").addEventListener("change", loadFleet);
}

export { init, enter };
