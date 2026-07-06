// Pure Vault-page shred-status shaping: the fleet summary line and each
// row's finalization-progress line, sourced from GET /api/vault/shreds (the
// privacy-shreds lens bucket). No DOM, no fetch — goja-tested via
// cmd/loupe/web_logic_test.go.

// shredInFlight reports whether a shredStatus row's async finalization has
// not yet fully propagated — either the Vault's key-destruction record or
// the Refractor's projection-nullification record is still pending
// (packages/privacy-base Lenses(): both flip false→true, never back).
function shredInFlight(row) {
  return !(row && row.vaultKeyDestroyed && row.projectionsNullified);
}

// shredFleetSummary renders the Vault page's shred-status headline: total
// shredded identities and how many are still finalizing.
function shredFleetSummary(rows) {
  var list = rows || [];
  var inFlight = 0;
  for (var i = 0; i < list.length; i++) {
    if (shredInFlight(list[i])) inFlight++;
  }
  return list.length + " identit" + (list.length === 1 ? "y" : "ies") + " shredded · " +
    inFlight + " shred" + (inFlight === 1 ? "" : "s") + " in flight (finalization pending)";
}

// shredFinalizationLine renders one row's two-step finalization progress —
// the vault design's Fire 4b observability lens.
function shredFinalizationLine(row) {
  var r = row || {};
  return (r.vaultKeyDestroyed ? "vaultKeyDestroyed ✓" : "vaultKeyDestroyed …") + " · " +
    (r.projectionsNullified ? "projectionsNullified ✓" : "projectionsNullified …");
}

export { shredInFlight, shredFleetSummary, shredFinalizationLine };
