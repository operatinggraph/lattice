// Pure Vault-page retention-key shaping: the fleet summary line and each
// row's status line, sourced from GET /api/vault/retention-keys (the
// privacy-retention-keys lens bucket). No DOM, no fetch — goja-tested via
// cmd/loupe/web_logic_test.go.

// retentionKeyShredded reports whether a retentionKeyStatus row's class key
// has been destroyed (ShredRetentionClassKey has run) — mirroring shred.js's
// shredInFlight naming, but for a class the destruction is a one-shot
// operator action, not something to poll toward completion (a retention class
// stays UN-shredded until an operator explicitly destroys it, so unlike a
// shredded identity there is no "will resolve on its own" state here).
function retentionKeyShredded(row) {
  return !!(row && row.shredded);
}

// retentionKeyInFlight reports whether a shredded class's async finalization
// (vault key destruction, secure-lens rebuild) has not yet fully propagated —
// the retention-class analog of shred.js's shredInFlight. A never-shredded
// class is not "in flight"; it simply has nothing pending.
function retentionKeyInFlight(row) {
  return retentionKeyShredded(row) && !(row && row.vaultKeyDestroyed && row.projectionsRebuilt);
}

// retentionFleetSummary renders the Vault page's retention-key headline:
// total declared classes, how many have been shredded, and how many shreds
// are still finalizing.
function retentionFleetSummary(rows) {
  var list = rows || [];
  var shredded = 0, inFlight = 0;
  for (var i = 0; i < list.length; i++) {
    if (retentionKeyShredded(list[i])) shredded++;
    if (retentionKeyInFlight(list[i])) inFlight++;
  }
  return list.length + " retention class" + (list.length === 1 ? "" : "es") + " declared · " +
    shredded + " shredded · " + inFlight + " finalization" + (inFlight === 1 ? "" : "s") + " in flight";
}

// retentionKeyStatusLine renders one row's declaration plus its shred
// progress (blank once shredded=false — a live class has no progress to
// show, only its policy).
function retentionKeyStatusLine(row) {
  var r = row || {};
  if (!r.shredded) return r.policy + " · " + r.retentionPeriod;
  return (r.vaultKeyDestroyed ? "vaultKeyDestroyed ✓" : "vaultKeyDestroyed …") + " · " +
    (r.projectionsRebuilt ? "projectionsRebuilt ✓" : "projectionsRebuilt …");
}

export { retentionKeyShredded, retentionKeyInFlight, retentionFleetSummary, retentionKeyStatusLine };
