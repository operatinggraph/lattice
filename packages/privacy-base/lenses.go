package privacybase

import "github.com/operatinggraph/lattice/internal/pkgmgr"

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

// ErasureCompleteTarget is the §10.8 TargetID, and therefore the
// identityErasureResidue lens's weaver-targets row prefix — the §10.2↔§10.8
// binding the Weaver resolves a target through (internal/weaver/registry.go's
// Target doc: "the binding between a violation Lens's weaver-targets row prefix
// (targetId) and the gap → action remediation playbook"). The lens's canonical
// name and the target id differ here, which is allowed and is what §7.2 asks
// for; only the OutputKeyPattern prefix has to match the target id.
const ErasureCompleteTarget = "identityErasureComplete"

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
		{
			CanonicalName:  "identityErasureResidue",
			Class:          "meta.lens",
			Adapter:        "nats-kv",
			Bucket:         "weaver-targets",
			Engine:         "full",
			Spec:           identityErasureResidueSpec,
			ProjectionKind: "actorAggregate",
			Output: &pkgmgr.OutputDescriptorSpec{
				AnchorType:       "identity",
				OutputKeyPattern: ErasureCompleteTarget + ".{actorSuffix}",
				BodyColumns: []string{
					"violating", "entityKey",
					"missing_credentialResidue", "missing_dedupResidue",
					"missing_vaultDestruction", "missing_projectionNullify",
					"missing_erasureSeal",
					"inflight_credentialResidue", "inflight_dedupResidue", "inflight_erasureSeal",
					"boundInResidue", "boundOutResidue",
					"indexResidue", "duplicateOutResidue", "duplicateInResidue",
					"requestedAt", "requestedForShreddedAt", "shreddedAt", "sealedAt", "sealedForShreddedAt",
				},
				EmptyBehavior: "delete",
				KeyColumn:     "entityId",
				Freshness:     "auto",
			},
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

// identityErasureResidueSpec projects one row per ERASURE-REQUESTED identity —
// the anchor predicate is the `.erasureRequested` marker SealIdentityForErasure
// writes, which is why that aspect exists (erasure-orchestration-design.md §6).
// The row is the scheduler for the convergent tail: the
// identityErasureComplete weaverTarget dispatches a sweep op per open gap
// until every count reaches zero, then the seal.
//
// IT IS A CONVERGENCE LENS, so it takes that whole shape and not the private
// read model §7.1 specified. §7.1 put this row in a new `privacy-erasure`
// bucket; §7.2 then has the Weaver dispatch on its gaps. Those two cannot both
// be true. The Weaver consumes exactly ONE bucket — WeaverTargetsBucket,
// default `weaver-targets` (weaver/engine.go) — off the backing stream
// `$KV.<bucket>.<targetId>.>`, keyed `<targetId>.<entityId>`, and resolves a
// target through the row-key PREFIX (weaver/registry.go's Target doc). A row in
// a package-private bucket is a row the Weaver cannot see, so §7.2's entire gap
// table would have been undispatchable. Every one of the ~19 shipped
// convergence lenses carries this same quartet — weaver-targets +
// actorAggregate + an Output descriptor + a `{key: $actorKey}` anchor
// projecting actorKey/entityKey/entityId — and this one is no exception.
// P5 is unaffected: weaver-targets is a lens read-model target like any other,
// so an operator tool still reads a projection and never Core KV. The
// attestation an auditor reads (§7.4 "prove it") was always the identity's own
// `.erasure.coverage` aspect, not this row.
//
// FIVE FAN-OUT ARMS, ONE PER SWEEP DIRECTION. The counts exist to be driven to
// zero by the two sweep ops, so the lens must count exactly what those ops
// sweep — no more and no less:
//
//   - UnbindIdentityCredentials sweeps `boundTo` in BOTH directions (the
//     subject is the owner of every credential bound to it, and the credential
//     when it is itself someone else's) → boundInResidue + boundOutResidue.
//   - PurgeIdentityDedupFootprint sweeps `indexes` inbound and `duplicateOf` in
//     both directions → indexResidue + duplicateOutResidue + duplicateInResidue.
//
// An arm the lens omits is residue no gap ever reports, swept by an op the
// Weaver stops dispatching, under a seal written over it — a silent, permanent
// erasure failure carrying a success signal. §7.1's ratified spec counted
// `boundTo` inbound only and folded `duplicateOf` into `indexes`; both are
// corrected here.
//
// STAGED, NOT ONE STAGE — this is a measured constraint, not a preference.
// Written as five sibling OPTIONAL MATCH clauses in a single stage (the §7.1
// shape, and the shape lease-signing's 8-arm convergence lens uses), the engine
// builds the full binding cross product before aggregating: a subject with 64
// credentials, 300 index vertices and 300 duplicate pairs reaches 5.76M
// bindings against the 1M cap and the evaluation is REFUSED — no row at all,
// so no gap, so no dispatch, so the erasure never converges. That failure is
// silent and it lands on exactly the well-connected subjects this whole design
// exists to be able to erase. Interposing a WITH after each arm collapses the
// bindings back to one row per anchor before the next arm fans out, so peak
// bindings is the LARGEST SINGLE arm rather than the product; the same subject
// then projects in ~150ms. count(DISTINCT …) is belt-and-braces on top: with
// one arm per stage there is nothing to inflate a plain count, but it keeps the
// counts correct if a later edit ever adds a second arm to a stage.
//
// The WITH costs one thing, knowingly: `AnchorHopIndex` refuses any query
// carrying one (hopindex.go:86-90), so link events reach this lens's anchors by
// BFS rather than the affected-anchor index — the posture 14 shipped lenses
// already have, and fail-closed (a superset), never a missed reprojection.
// §7.1's own spec carries a WITH too, so this costs nothing the ratified shape
// did not already.
//
// EVERY ARM IS WRITTEN ANCHOR-FIRST, and that is not a style choice. matchPath
// seeds a path from `p.Nodes[0]` and takes the cheap adjacency walk only when
// that node is ALREADY BOUND; an unbound, unlabeled head falls through to
// `coreKV.ListKeys` — the whole bucket, then a point read per vertex (the
// generic seed path in executor.go, whose own comment says "an unlabeled
// pattern can bind any type and still lists everything"). The visitor stores
// nodes in TEXTUAL order and an arrowhead only sets the direction, so written
// `(c)-[:boundTo]->(i)` the head is the unbound neighbour: an inbound arm would
// scan the entire corpus on every reprojection, and past ~1M vertices refuse
// the evaluation on the binding cap — reintroducing the exact silent
// non-erasure the staging above exists to prevent, at a scale nothing about the
// subject bounds. `(i)<-[:boundTo]-(c)` is the same pattern with the same
// direction and no label added, seeded from the bound anchor. All 32 inbound
// arms in the shipped lens corpus are written this way.
//
// THE SEAL IS THE LAST GAP. missing_erasureSeal opens only once the other four
// are closed. The seal op re-verifies residue in its own commit and fails
// closed (§7.2), so an early dispatch would merely refuse — but it would refuse
// every reconcile pass for the whole life of the sweep, and, more importantly,
// the seal's in-commit verification covers the RESIDUE classes, not the two
// async halves (`vaultKeyDestroyed`, `projectionsNullified`). Ordering them
// here is what stops an attestation being written while the Vault still holds
// the key. The increment that builds SealIdentityForErasureComplete inherits
// that obligation: if it re-verifies the async halves itself, this gate becomes
// belt-and-braces; until it does, this gate IS the guarantee.
//
// THE CYCLE DISCRIMINATOR IS THE LIVE `piiKey.shreddedAt`, not the marker's
// copy of it — exactly as §5.5 writes it. The two look interchangeable because
// SealIdentityForErasure refreshes the marker, but only when it RUNS, and
// §5.1's ratified step-2 guard skips step 2 whenever the marker already carries
// a requestedAt, which on a re-triggered erasure it always does. Nothing else
// rewrites the marker. So a genuine re-shred of a completed erasure would leave
// the marker naming cycle 1: a marker-diff reads equal, the row goes quiet, and
// cycle 2 is never attested while cycle 1's sealedAt sits on it as though it
// were the answer. The live envelope cannot go stale that way. The marker's own
// shreddedAt stays projected as `requestedForShreddedAt` — provenance for the
// cycle the REQUEST was sealed against, not the completeness test.
//
// All aspect reads are the null-safe node.<aspect>.data.<field> form. The
// `.erasure` aspect does not exist until the seal op is built — it projects
// null, so missing_erasureSeal reads true, which is correct: an erasure with no
// attestation is not complete.
const identityErasureResidueSpec = `MATCH (i:identity {key: $actorKey})
WHERE i.erasureRequested.data.requestedAt <> null
OPTIONAL MATCH (i)<-[:boundTo]-(c)
WITH i, count(DISTINCT c.key) AS boundInResidue
OPTIONAL MATCH (i)-[:boundTo]->(o)
WITH i, boundInResidue, count(DISTINCT o.key) AS boundOutResidue
OPTIONAL MATCH (i)<-[:indexes]-(x)
WITH i, boundInResidue, boundOutResidue, count(DISTINCT x.key) AS indexResidue
OPTIONAL MATCH (i)-[:duplicateOf]->(dout)
WITH i, boundInResidue, boundOutResidue, indexResidue,
     count(DISTINCT dout.key) AS duplicateOutResidue
OPTIONAL MATCH (i)<-[:duplicateOf]-(din)
WITH i, boundInResidue, boundOutResidue, indexResidue, duplicateOutResidue,
     count(DISTINCT din.key) AS duplicateInResidue
RETURN
  i.key AS actorKey,
  i.key AS entityKey,
  nanoIdFromKey(i.key) AS entityId,
  i.erasureRequested.data.requestedAt AS requestedAt,
  i.erasureRequested.data.shreddedAt AS requestedForShreddedAt,
  i.piiKey.data.shreddedAt AS shreddedAt,
  i.erasure.data.sealedAt AS sealedAt,
  i.erasure.data.sealedForShreddedAt AS sealedForShreddedAt,
  i.piiKey.data.vaultKeyDestroyed AS vaultKeyDestroyed,
  i.piiKey.data.projectionsNullified AS projectionsNullified,
  boundInResidue,
  boundOutResidue,
  indexResidue,
  duplicateOutResidue,
  duplicateInResidue,
  ((boundInResidue > 0) OR (boundOutResidue > 0)) AS missing_credentialResidue,
  ((indexResidue > 0) OR (duplicateOutResidue > 0) OR (duplicateInResidue > 0)) AS missing_dedupResidue,
  (i.piiKey.data.vaultKeyDestroyed <> true) AS missing_vaultDestruction,
  (i.piiKey.data.projectionsNullified <> true) AS missing_projectionNullify,
  (
    (boundInResidue = 0) AND (boundOutResidue = 0)
    AND (indexResidue = 0) AND (duplicateOutResidue = 0) AND (duplicateInResidue = 0)
    AND (i.piiKey.data.vaultKeyDestroyed = true)
    AND (i.piiKey.data.projectionsNullified = true)
    AND (i.piiKey.data.shreddedAt <> null)
    AND (i.erasure.data.sealedForShreddedAt <> i.piiKey.data.shreddedAt)
  ) AS missing_erasureSeal,
  false AS inflight_credentialResidue,
  false AS inflight_dedupResidue,
  false AS inflight_erasureSeal,
  (
    (boundInResidue > 0) OR (boundOutResidue > 0)
    OR (indexResidue > 0) OR (duplicateOutResidue > 0) OR (duplicateInResidue > 0)
    OR (i.piiKey.data.vaultKeyDestroyed <> true)
    OR (i.piiKey.data.projectionsNullified <> true)
    OR ((i.piiKey.data.shreddedAt <> null)
        AND (i.erasure.data.sealedForShreddedAt <> i.piiKey.data.shreddedAt))
  ) AS violating`
