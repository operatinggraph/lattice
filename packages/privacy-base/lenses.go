package privacybase

import "github.com/asolgan/lattice/internal/pkgmgr"

// ShredStatusBucket is the package-owned NATS-KV read model the shredStatus
// lens projects into — the P5 query surface for "which identities are
// shredded and how far along is each shred's async finalization". Loupe (or
// any operator tool) reads THIS bucket, never Core KV. Provisioned at
// package-install time by the Refractor on lens load, mirroring
// augur-proposals / my-tasks.
const ShredStatusBucket = "privacy-shreds"

// Lenses returns the package's single lens.
//
// shredStatus is the shred-finalization observability lens
// (vault-crypto-shredding-design.md §2.4 point 4): pure visibility —
// JetStream's durable at-least-once redelivery on both async consumers
// guarantees crash-survival, so this lens is the operator's window, not a
// correctness mechanism. One FLAT row per SHREDDED identity (the WHERE keeps
// un-shredded piiKey holders out — the read model is a shred ledger, not a
// key inventory):
//
//   - shredded / shreddedAt — the ShredIdentityKey commit (always true here).
//   - vaultKeyDestroyed / At — the privacy-worker's Vault.ShredKey record
//     (RecordShredFinalization step vaultKeyDestroyed).
//   - projectionsNullified / At — the Refractor keyshredded listener's
//     all-configured-targets-clean record (step projectionsNullified; it
//     attests the NullifyTarget configuration in force when the event was
//     handled — vacuously true under an empty target list).
//
// A row with shredded=true and either progress boolean still null/false is an
// in-flight or STUCK shred — exactly what an operator scans for; the
// remediation for a stuck row is re-submitting ShredIdentityKey (the re-shred
// resets the finalization cycle and re-drives both listeners). Both booleans
// only ever transition false→true and a row only ever ENTERS the WHERE set
// (shredded never unsets), so this lens needs no negative/filter-retraction
// machinery; the null-safe aspect reads project null for not-yet-recorded
// steps (the "in flight" rendering).
//
// Scope: the ledger covers LIVE identities. Tombstoning the identity vertex
// (e.g. an identity-hygiene merge) retracts its row like any other
// anchor-tombstone — shred visibility ends with the identity. Re-homing the
// ledger onto a longer-lived anchor is a design change deferred until a flow
// actually tombstones shredded identities.
func Lenses() []pkgmgr.LensSpec {
	return []pkgmgr.LensSpec{
		{
			CanonicalName: "shredStatus",
			Class:         "meta.lens",
			Adapter:       "nats-kv",
			Bucket:        ShredStatusBucket,
			Engine:        "full",
			Spec:          shredStatusSpec,
		},
	}
}

// shredStatusSpec projects one row per shredded identity, keyed by the
// identity vertex key (`key`, the IntoKey default — same flat shape as
// augurProposals / clinicPatients). All aspect reads are the null-safe
// node.<aspect>.data.<field> form: a not-yet-recorded finalization step
// projects null, distinguishing "in flight" from the recorded true.
const shredStatusSpec = `MATCH (i:identity)
WHERE i.piiKey.data.shredded = true
RETURN
  i.key AS key,
  i.key AS identityKey,
  i.piiKey.data.shredded AS shredded,
  i.piiKey.data.shreddedAt AS shreddedAt,
  i.piiKey.data.vaultKeyDestroyed AS vaultKeyDestroyed,
  i.piiKey.data.vaultKeyDestroyedAt AS vaultKeyDestroyedAt,
  i.piiKey.data.projectionsNullified AS projectionsNullified,
  i.piiKey.data.projectionsNullifiedAt AS projectionsNullifiedAt`
