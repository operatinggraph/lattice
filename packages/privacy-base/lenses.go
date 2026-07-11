package privacybase

import "github.com/asolgan/lattice/internal/pkgmgr"

// ShredStatusBucket is the package-owned NATS-KV read model the shredStatus
// lens projects into — the P5 query surface for "which identities are
// shredded and how far along is each shred's async finalization". Loupe (or
// any operator tool) reads THIS bucket, never Core KV. Provisioned at
// package-install time by the Refractor on lens load, mirroring
// augur-proposals / my-tasks.
const ShredStatusBucket = "privacy-shreds"

// PiiKeyEnvelopeBucket is the package-owned NATS-KV read model the
// piiKeyEnvelope lens projects into — the P5-compliant read seam
// (object-store-crypto-shred-design.md §9 Fire 4 Increment 1) that lets a
// vertical app (e.g. loftspace-app) fetch an identity's wrapped-DEK Envelope
// to drive the Vault's WrapKey/UnwrapKey RPCs, without the Loupe-only direct
// Core-KV read (P5 inspector exception) cmd/loupe/objects_crypto.go uses.
const PiiKeyEnvelopeBucket = "privacy-pii-key-envelopes"

// Lenses returns the package's lenses.
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
		{
			CanonicalName: "piiKeyEnvelope",
			Class:         "meta.lens",
			Adapter:       "nats-kv",
			Bucket:        PiiKeyEnvelopeBucket,
			Engine:        "full",
			Spec:          piiKeyEnvelopeSpec,
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

// piiKeyEnvelopeSpec projects one row per identity that has ever received a
// piiKey envelope (real or the ShredIdentityKey empty-wrappedDEK placeholder
// — the WHERE's `keyId <> null` aspect-presence guard admits both, since a
// vertical app's WrapKey/UnwrapKey call needs the same Envelope shape either
// way and a shredded placeholder correctly makes the Vault RPC fail closed).
// Only the wrapped-DEK Envelope's NON-SECRET fields are projected —
// wrappedDEK is ciphertext, inert without the Vault's master KEK, the same
// posture as shredStatus. keyId here is redundant with the row's own key but
// kept so the projected row is a complete, self-describing Envelope.
//
// shredded IS projected (unlike CreatedAt, which stays unprojected — a
// display nicety no consumer needs): ShredIdentityKey does NOT zero an
// already-minted identity's wrappedDEK (it only flips shredded=true, keeping
// the real bytes — packages/privacy-base/shred_identity_key.go), so
// LocalBackend's own in-memory shredded-set is the ONLY restart-proof signal
// for an envelope-lens consumer that omits this field — a genuine Vault
// process restart loses that in-memory set and a stale lens row would
// silently re-admit a shredded identity's PII (sensitive-param-egress-design
// §3.2/§3.5's live-envelope rule requires the CALLER-supplied envelope to
// carry the durable, CDC-refreshed truth). Every reader must map this into
// vault.Envelope.Shredded (Decrypt/Encrypt OR it with the backend's own
// check — internal/vault/local.go's checkAndDeriveDEK).
const piiKeyEnvelopeSpec = `MATCH (i:identity)
WHERE i.piiKey.data.keyId <> null
RETURN
  i.key AS key,
  i.piiKey.data.wrappedDEK AS wrappedDEK,
  i.piiKey.data.keyId AS keyId,
  i.piiKey.data.kekVersion AS kekVersion,
  i.piiKey.data.alg AS alg,
  i.piiKey.data.shredded AS shredded`
