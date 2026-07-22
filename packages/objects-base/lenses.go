package objectsbase

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// Lenses returns the package's Lens declarations: the single `objectLiveness`
// actorAggregate convergence lens (Contract #10 §10.2) — the v1b GC's orphan
// DETECTION. It anchors on each object vertex and projects a `missing_owner`
// gap flag (= the object's atomic `liveLinks` count is zero) + `violating` + the
// object's `linkEpoch` (the link-set version the reclaim op CASes against).
// Weaver's `objectLiveness` target dispatches `directOp(TombstoneObject)` over
// the orphaned rows.
//
// Liveness is read from `o.data.liveLinks` — the authoritative, lag-free
// live-link count maintained atomically by every attach/detach — NOT from the
// lagging refractor-adjacency projection (the §21 attach-lag reclaim-race fix;
// see objectLivenessSpec). The `OPTIONAL MATCH (o)-[r]->(owner)` + `count(owner)`
// is retained only to collapse the link fan to exactly one row per anchor (the
// §0.C guard) and to drive the actorAggregate reprojection on any link
// create/tombstone (AnchorType `object`); every attach/detach also rewrites the
// object vertex, so the anchor reprojects from the vertex CDC regardless. The
// dead-target dangling-link case is a deferred owner-cascade concern, not this
// lens's job (a bounded leak, never data loss — see objectLivenessSpec).
func Lenses() []pkgmgr.LensSpec {
	return []pkgmgr.LensSpec{
		{
			CanonicalName:  "objectLiveness",
			Class:          "meta.lens",
			Adapter:        "nats-kv",
			Bucket:         "weaver-targets",
			Engine:         "full",
			Spec:           objectLivenessSpec,
			ProjectionKind: "actorAggregate",
			Output: &pkgmgr.OutputDescriptorSpec{
				AnchorType:       "object",
				OutputKeyPattern: "objectLiveness.{actorSuffix}",
				BodyColumns:      []string{"violating", "missing_owner", "entityKey", "linkEpoch", "storeName"},
				EmptyBehavior:    "delete",
				KeyColumn:        "entityId",
			},
		},
		{
			CanonicalName:  "objectAttachments",
			Class:          "meta.lens",
			Adapter:        "nats-kv",
			Bucket:         "weaver-targets",
			Engine:         "full",
			Spec:           objectAttachmentsSpec,
			ProjectionKind: "actorAggregate",
			Output: &pkgmgr.OutputDescriptorSpec{
				AnchorType:       "object",
				OutputKeyPattern: "objectAttachments.{actorSuffix}",
				BodyColumns:      []string{"entityKey", "storeName", "contentType", "size", "owners", "sensitive", "governingIdentity", "encryption", "digest"},
				EmptyBehavior:    "delete",
				KeyColumn:        "entityId",
			},
		},
	}
}

// objectLivenessSpec is the one-row-per-anchor orphan-detection cypher.
//
//   - Liveness is decided on `o.data.liveLinks` — the object's AUTHORITATIVE,
//     adjacency-free live-link count, maintained atomically by every
//     AttachObject / DetachObject in the same batch as the link mutation
//     (objects-base ddls.go `write_vertex`). It is read directly off the vertex
//     root data, so it does NOT lag the commit. This is the §21 fix for the
//     attach-adjacency-lag reclaim race: a freshly-attached object commits its
//     link AND `liveLinks=1` atomically, so the lens never mis-sees it as
//     orphaned during the refractor-adjacency catch-up window (the old
//     adjacency-`count(owner.key)` decision DID — the link was in Core KV but not
//     yet in adjacency → liveOwners 0 → a premature, irreversible TombstoneObject
//     that the epoch-CAS could not catch because no re-link had bumped the
//     epoch).
//   - The single `OPTIONAL MATCH (o)-[r]->(owner)` + `count(owner.key)` is kept
//     ONLY to collapse the link fan-out to exactly one row per object anchor (the
//     §0.C guard) and to drive the actorAggregate reprojection; `liveOwners` is
//     NOT used in the orphan decision. The carry-no-filtering-WHERE / null-restore
//     grouping behaviour is the documented full-engine idiom.
//   - DEAD-TARGET tradeoff (§21): because `liveLinks` is only decremented by the
//     object's OWN attach/detach, an owner-tombstone (which never touches the
//     object) leaves a stale `liveLinks >= 1` → a dangling link to a dead owner no
//     longer reaps the object here. This is a BOUNDED, non-permanent byte LEAK,
//     not data loss, and is strictly preferable to the prior adjacency decision's
//     data-loss bug. Authoritative dead-target reclamation belongs to the
//     deferred owner-tombstone-cascade trigger (CC4 trigger-side, Andrew's GC
//     domain); the adjacency could no more distinguish a dead-target dangling
//     link from a not-yet-projected fresh attach anyway (the engine skips a
//     dead-neighbour edge entirely, executor.go:565-571 — both present as
//     `count(r)=0, count(owner)=0`).
//   - `linkEpoch` is the object's root-data link-set version (`o.data.linkEpoch`,
//     resolved directly off root data); Weaver templates it into the reclaim op's
//     expectedEpoch so a concurrent re-link (which bumps the epoch) aborts the
//     tombstone (§20 — the re-link race, distinct from the §21 attach-lag race).
//   - `= null` is not used here, but per the lease-lens convention any null test
//     would be `= null`, never the unsupported `IS NULL`.
const objectLivenessSpec = `
MATCH (o:object {key: $actorKey})
OPTIONAL MATCH (o)-[r]->(owner)
WITH
  o.key AS entityKey,
  o.data.linkEpoch AS linkEpoch,
  o.data.liveLinks AS liveLinks,
  o.content.data.storeName AS storeName,
  count(owner.key) AS liveOwners
RETURN
  entityKey AS actorKey,
  entityKey,
  linkEpoch,
  storeName,
  (liveLinks = 0) AS missing_owner,
  (liveLinks = 0) AS violating
`

// objectAttachmentsSpec is the per-object display read-model the vertical apps
// (LoftSpace's Documents tab) read in place of Core KV (P5): given an oid it
// resolves the byte-plane metadata (storeName / contentType / size) to stream a
// document, and given an owner it lists that owner's attached documents by
// filtering `owners`.
//
//   - One row per anchor object (the §0.C guard): the `OPTIONAL MATCH
//     (o)-[r]->(owner)` fan is collapsed by the single `collect`, so any number
//     of links produces one row. A zero-link object null-restores to one row
//     with `owners` carrying a degenerate `{ownerKey: null}` artifact (the
//     documented full-engine grouping behaviour, as in `myTasksSpec`); the app
//     drops null entries.
//   - The metadata columns are aspect-data reads off `.content` (the
//     `objectLiveness` storeName idiom), so they resolve directly in the full
//     engine. A tombstoned object does not bind (`fetchNode` returns nil for a
//     soft-deleted vertex), so it emits no row and `EmptyBehavior: delete`
//     reclaims its read-model key.
//   - The relationship NAME (the upload "slot" / linkName) is NOT projected —
//     the full engine cannot project `type(r)` — so `owners` carries only the
//     destination node key. Detach of a listed doc (which needs the linkName)
//     is therefore a documented follow-up.
//   - `sensitive` / `governingIdentity` / `encryption` (object-store-crypto-
//     shred-design.md §3.1/§9 Fire 4 Increment 2) are the same null-safe
//     `.content.data.<field>` reads as the metadata columns above — a
//     non-sensitive object simply never had these keys written, so they
//     project null (Cypher missing-property semantics), same as any absent
//     aspect field elsewhere in this codebase. `encryption` is returned as the
//     whole nested object verbatim (algo/nonce/wrappedCEK/keyId), the same
//     idiom `owners` already uses for a nested-map column — this is the
//     P5-compliant read seam a vertical app's decrypt-capable GET uses in
//     place of Loupe's direct Core-KV `.content` read.
//   - `digest` (the plaintext digest for a sensitive object; the byte-plane's
//     own for a non-sensitive one) is also projected so a decrypt-capable
//     read can re-verify the post-decrypt plaintext against it — the same
//     independent integrity check Loupe's `handleSensitiveObjectDecrypt`
//     already makes beyond the GCM tag alone.
const objectAttachmentsSpec = `
MATCH (o:object {key: $actorKey})
OPTIONAL MATCH (o)-[r]->(owner)
WITH
  o.key AS entityKey,
  o.content.data.storeName AS storeName,
  o.content.data.contentType AS contentType,
  o.content.data.size AS size,
  o.content.data.digest AS digest,
  o.content.data.sensitive AS sensitive,
  o.content.data.governingIdentity AS governingIdentity,
  o.content.data.encryption AS encryption,
  collect(DISTINCT { ownerKey: owner.key }) AS owners
RETURN
  entityKey AS actorKey,
  entityKey,
  storeName,
  contentType,
  size,
  digest,
  sensitive,
  governingIdentity,
  encryption,
  owners
`
