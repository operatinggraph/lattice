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

// --- identityErasure pattern progress (erasure-orchestration-design.md §12
// Fire B increment 4) — sourced from the shred row above plus the
// identityErasureComplete weaverTarget's residue row (GET
// /api/weaver/target/identityErasureComplete/entity/<id>,
// packages/privacy-base lenses.go's identityErasureResidue). Durable-state
// derived, not the Loom instance's own in-memory cursor, so it reads the same
// after a page reload as it does mid-run.

// erasureResidueOpen reports whether the pattern's convergent tail still has
// open work — a credential/dedup sweep gap or the final seal, each column
// false once its sweep/seal op has nothing left to do.
function erasureResidueOpen(residueRow) {
  return !!(residueRow && (residueRow.missing_credentialResidue || residueRow.missing_dedupResidue || residueRow.missing_erasureSeal));
}

// erasureInFlight combines the key-shred finalization (shredInFlight, only
// meaningful once shredRow.shredded) with the erasure pattern's convergent
// tail — either being open keeps the panel polling.
function erasureInFlight(shredRow, residueRow) {
  if (shredRow && shredRow.shredded && shredInFlight(shredRow)) return true;
  return erasureResidueOpen(residueRow);
}

// erasureSteps derives the identityErasure pattern's five-step progress list.
// Step 1 reads the shred row (may be null — the key may not be shredded yet);
// steps 2-5 read the residue row (may be null — the pattern may never have
// been started), each undone by default so an absent row shows nothing done
// past step 1 rather than false-completing a step no read model has recorded.
function erasureSteps(shredRow, residueRow) {
  var r = residueRow || {};
  return [
    { label: "Shred key", done: !!(shredRow && shredRow.shredded) },
    { label: "Seal for erasure", done: !!r.requestedAt },
    { label: "Unbind credentials", done: r.missing_credentialResidue === false },
    { label: "Purge dedup footprint", done: r.missing_dedupResidue === false },
    { label: "Erasure sealed", done: r.missing_erasureSeal === false },
  ];
}

export { shredInFlight, shredFleetSummary, shredFinalizationLine, erasureResidueOpen, erasureInFlight, erasureSteps };
