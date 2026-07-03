// Pure lens-page decision logic (design §6.3): the control-row model derived
// from the server-side renderedState, the typed-delete confirm rules, and the
// heartbeat-overlay formatting. No DOM, no fetch — goja-tested via
// cmd/loupe/web_logic_test.go (strip-export load).

// lensControls derives the validate/pause/resume/rebuild row from the lens's
// renderedState + its spec-side protected flag. Enablement follows §6.3:
// resume only when paused (operator pause or infra); pause only while
// projecting/lagging; rebuild is disabled while pending-readpath (nothing
// verified to project into) and carries a confirm + note on a protected lens
// (the table DDL/verify is out-of-band and untouched). validate is always
// available. Delete is NOT here — it is the separate destructive surface.
function lensControls(status, isProtected) {
  var pauseable = status === "projecting" || status === "lagging";
  var resumable = status === "paused";
  var rebuildable = status !== "pending-readpath";
  var rebuildNote = "";
  if (!rebuildable) {
    rebuildNote = "disabled while pending-readpath — the read path is unverified, a rebuild cannot help";
  } else if (isProtected) {
    rebuildNote = "re-projects rows into the verified protected table; the table DDL/verify is out-of-band and untouched";
  }
  return [
    { op: "validate", enabled: true, confirm: false, note: "" },
    { op: "pause", enabled: pauseable, confirm: false,
      note: pauseable ? "" : "pause applies to a projecting/lagging lens" },
    { op: "resume", enabled: resumable, confirm: false,
      note: resumable ? "" : "resume applies to a paused lens" },
    { op: "rebuild", enabled: rebuildable, confirm: rebuildable && !!isProtected, note: rebuildNote },
  ];
}

// deleteConfirmToken is the exact text the typed-delete modal requires: the
// canonicalName, falling back to the lens id when the lens is unnamed.
function deleteConfirmToken(canonicalName, id) {
  return canonicalName || id || "";
}

// deleteConfirmReady gates the modal's destructive button: exact match only,
// and never an empty token.
function deleteConfirmReady(input, token) {
  return typeof input === "string" && token !== "" && input === token;
}

// latencyLine formats a metrics.lensLatency entry ({count, meanNs, p95Ns,
// p99Ns} — nanoseconds) into the STATE panel's one-line stats string. Returns
// "" for a missing/empty entry.
function latencyLine(entry) {
  if (!entry || !entry.count) return "";
  function ms(ns) {
    if (typeof ns !== "number") return "?";
    var v = ns / 1e6;
    return (v >= 10 ? Math.round(v) : Math.round(v * 10) / 10) + "ms";
  }
  return "count " + entry.count + " · mean " + ms(entry.meanNs) +
    " · p95 " + ms(entry.p95Ns) + " · p99 " + ms(entry.p99Ns);
}

export { lensControls, deleteConfirmToken, deleteConfirmReady, latencyLine };
