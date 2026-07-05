// Package objectsbase is the objects-base Capability Package — the graph side
// of the off-graph blob plane (the large-file / binary handling vertical).
//
// It declares:
//
//   - One DDL (`object`) defining the generic large-object vertex type + three
//     lifecycle ops. An object vertex is content-addressed:
//     vtx.object.<oid> where oid = crypto.sha256NanoID("object:" + digest), so
//     identical bytes map to a single vertex (dedup). Root data is minimal ({});
//     the content's reference metadata (digest, size, contentType, storeName)
//     lives on the `.content` aspect (D5); the bytes live in the core-objects
//     Object Store and never enter the Processor/CDC path. Relationships are
//     LINKS: object -<linkName>-> owner (Contract #1 §1.1):
//
//     vtx.object.<oid>.content                          # digest/size/contentType/storeName
//     lnk.object.<oid>.<linkName>.<tgtType>.<tgtId>     # object → its owner, data { filename? }
//
//     AttachObject mints-or-dedups the object + creates the link to a live,
//     non-protected target (type-agnostic, D7); DetachObject tombstones one
//     link; TombstoneObject (the v1b GC's reclaim op) soft-deletes the object
//     under an OCC revision guard and emits object.tombstoned (the byte-reclaim
//     trigger). Every attach/detach OCC-touches the object vertex so its
//     revision tracks the link set (the GC race guard, §19).
//
//   - Permissions granting the three lifecycle ops to `operator` (scope: any) —
//     Loupe (the trusted single-identity client) and the v1b GC submit them.
//
//   - Op-metas making the three ops `forOperation`-resolvable.
//
//   - The `objectLiveness` actorAggregate convergence lens (v1b GC detection):
//     it projects each object vertex with a `missing_owner` gap (zero live
//     links, dead-target aware) + `violating` + the object's `linkEpoch`.
//
//   - The `objectLiveness` meta.weaverTarget (v1b GC Loop A): the `missing_owner`
//     gap dispatches directOp(TombstoneObject), templating the object key +
//     linkEpoch from the lens row and routing the object key into the op's reads
//     so the reclaim hydrates the vertex for its epoch-CAS.
//
//   - The `objectAttachments` actorAggregate display lens: per object it projects
//     the byte-plane metadata (storeName / contentType / size) + the owner keys
//     it links to. It drives no convergence — it is the read model a vertical app
//     reads (P5) to stream a document and list an owner's documents, in place of
//     scanning Core KV.
//
// It is type-agnostic (it never learns concrete owner types) and depends on
// nothing. The only NEW runtime component v1b adds is the object-store-manager
// (Loop B): it consumes object.tombstoned and deletes the bytes. Install via the
// InstallPackage kernel op. See docs/components/_packages.md.
package objectsbase

import "github.com/asolgan/lattice/internal/pkgmgr"

// Package is the static, install-time bundle.
var Package = pkgmgr.Definition{
	Name:          "objects-base",
	Version:       "0.2.0",
	Description:   "Generic large-object vertex type (object DDL + attach/detach/tombstone ops) + the objectLiveness GC convergence lens + meta.weaverTarget + the objectAttachments display lens (the apps' P5-clean byte-plane read model); the graph side of the off-graph blob plane. Content-addressed, type-agnostic, content metadata in the .content aspect (D5).",
	Depends:       nil,
	DDLs:          DDLs(),
	Lenses:        Lenses(),
	Permissions:   Permissions(),
	WeaverTargets: WeaverTargets(),
	OpMetas:       OpMetas(),
}
