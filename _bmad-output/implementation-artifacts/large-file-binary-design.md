# Large-file / binary handling — design spec

**Status:** design-spec (ratified with Andrew through four design rounds; ready for the 2-lens design review → build). Supersedes the earlier "pointer-aspect" draft.
**Backlog item:** Large-file / binary handling (Andrew's picked "Now" set; companion to Loupe). Carries the former architecture **OI-2**.
**Why now:** an experience-business view/control app shows profile photos and lease PDFs; this is the natural companion to **Loupe** (the upload/view client) and exercises the off-graph blob plane. The trusted single-identity model removes the hard parts (no Gateway, no per-user blob authz).

---

## 1. The binding constraint + the architectural framing

Blobs (photos, scanned docs, PDFs) **must not flow through the Refractor** — the CDC/projection plane is for graph *state*, not binary payloads. Yet clients need first-class upload/download.

**Framing (locked):** the object store is a **third *write* plane**, exactly parallel to Health-KV being a third *state* plane (architecture Decision #4). There are now two sanctioned non-Processor write paths:

| Plane | Writer | Carries | Goes through Processor/CDC? |
|---|---|---|---|
| Core KV (graph state) | Processor (sole) | vertices, aspects, links | yes |
| Health KV | each component | heartbeats | no (Decision #4) |
| **`core-objects` (bytes)** | **trusted clients (Loupe)** | **blob bytes** | **no** |

"Blobs never enter the Refractor" is therefore not a special-case rule — it falls out of *bytes aren't graph state*. The **graph** holds an object **vertex + links + metadata aspect** (normal CDC-projected state); the **object store** holds the bytes; the Refractor projects only the reference metadata, never the bytes.

---

## 2. Decision ledger (what's locked, and why)

| # | Decision | Rationale |
|---|---|---|
| D1 | **Object modeled as a first-class vertex** `vtx.object.<oid>`, related to owners by **links** (`object -photoOf-> identity`), not as a pointer aspect on the owner. | Makes the reference a real graph edge → "is it still referenced?" becomes **adjacency** (the platform's existing `refractor-adjacency` index), which dissolves the bespoke GC reference-index. Honors Decision #2 (relationships are links). |
| D2 | **Content-addressed.** The object's identity derives from its SHA-256 digest. | Canonical local-first/edge design (git/IPFS-style): dedup, offline integrity verification, no cross-node key collisions when Loupe grows into a real Edge node. |
| D3 | **Vertex id = `crypto.sha256NanoID("object:" + digest)`**, *not* the raw digest. | `substrate.ClassifyKey` requires the 3rd key segment to be a valid 20-char NanoID; a 64-char hex digest fails step-6 `keyPattern`. The platform already ships `crypto.sha256NanoID` for exactly this ("deterministic index-vertex keys that satisfy ClassifyKey"). Content-derived → same bytes ⇒ same vertex ⇒ 1:1 vertex↔content ⇒ dedup + index-free GC. The full digest is stored as a field for integrity; a 20-char-NanoID collision is ~2⁻⁶⁰ **and detectable** (vertex stores the full digest). |
| D4 | **Store object streamed under a provisional NanoID name**, *not* a digest-named key. | NATS computes the digest *during* Put and returns it *after*, while the object name is chosen *before* streaming. Naming-by-digest would force a pre-hash pass; a provisional name lets large/multipart uploads **stream straight through** NATS's native 128 KB chunking with constant memory. Content-addressing lives at the **graph** layer (the vertex id), where dedup + GC need it. |
| D5 | **Metadata splits by what it describes:** content-intrinsic facts (`digest`, `size`, `contentType`, `storeName`, `firstSeenAt`) on the **object vertex**; attachment-specific facts (`filename`, `attachedAt`) on the **link**. | A deduped vertex is shared across owners — owner A's "resume.pdf" and B's "lease.pdf" can be identical bytes, so `filename` can't live on the shared vertex. Matches D5 (business data in aspects/links, root minimal). |
| D6 | **Object vertices are immutable.** "New photo" = new bytes = new digest = a *different* vertex. Updates only ever **re-point links**; the old object is reclaimed by link-refcount GC. | The content-addressed/immutable mental model (git). No "edit bytes in place" path; no special update-cleanup path. |
| D7 | **Generic `AttachObject`/`DetachObject` ops in a new `objects-base` package.** Type-agnostic: `(targetKey, linkName, digest, …)`. | Honors "don't hardwire generic components to concrete types" (the bridge precedent). identity-domain / lease-signing never learn about blobs. |
| D8 | **Idempotency via a deterministic `requestId`** (`substrate`-shared `deriveID`), collapsing on the Contract #4 `vtx.op.<requestId>` 24h tracker — the same idiom the bridge uses (`deriveReplyRequestID`). | The op write path is retry-prone (multi-step); the existing tracker gives exactly-once commit when the requestId is deterministic. |
| D9 | **`core-objects` is a primordial substrate bucket** (an Object Store), provisioned in `bootstrap.ProvisionBuckets`; **not** package-provisioned. | `pkgmgr` writes Core-KV manifests only — it has no path to provision a JetStream Object Store; infra-in-a-package would be a layering break. The *bucket* is substrate; the *ops + vertex DDL* are package (graph content). |
| D10 | **Single `core-objects` bucket** for the single-cell MVP. | Decision #8 (single-cell MVP); per-cell/sharding is Phase 3. |
| D11 | **GC = per-object deferred point-check** on `core-schedules` + an **adjacency** lookup, not a global sweep and not a Refractor reconciler. | Andrew's call. O(1) per object, dedup-safe, reuses Weaver's proven temporal-timer machinery. See §11. |
| D12 | **Limits:** 25 MB per-upload cap (env-config) enforced at upload; no content-type allow-list in v1 (trusted operator), `contentType` recorded. | v1 simplification; raise/relax later. |

---

## 3. Substrate layer — `internal/substrate/object.go`

A thin wrapper mirroring the KV helpers (`kv.go`), keeping the "architecturally-common" surface. The Processor / Refractor never import it; only trusted clients (Loupe) and the GC worker do.

```go
// ObjectInfo echoes the durable properties clients need (mirrors KVEntry).
type ObjectInfo struct {
    Bucket   string
    Name     string // the store object name (our provisional NanoID)
    Digest   string // "SHA-256=<base64url>" exactly as NATS computes it
    Size     uint64
    Chunks   uint32
}

// ObjectPut streams r into bucket under name (chunked, 128 KB, constant
// memory). Returns the NATS-computed digest + size. name is caller-chosen.
func (c *Conn) ObjectPut(ctx context.Context, bucket, name string, r io.Reader) (ObjectInfo, error)

// ObjectGet streams the bytes back. The returned ReadCloser is the chunk
// stream (NATS verifies the digest on read → ErrDigestMismatch).
func (c *Conn) ObjectGet(ctx context.Context, bucket, name string) (io.ReadCloser, ObjectInfo, error)

// ObjectGetInfo reads metadata without the bytes (existence + digest probe).
func (c *Conn) ObjectGetInfo(ctx context.Context, bucket, name string) (ObjectInfo, error)

// ObjectDelete removes the object (idempotent at the GC layer).
func (c *Conn) ObjectDelete(ctx context.Context, bucket, name string) error
```

Implemented over the vendored `jetstream.ObjectStore` (`Put(ObjectMeta{Name}, reader)`, `Get`, `GetInfo`, `Delete`), with a per-bucket handle cache like `Conn.bucket()`. nats.go **v1.52.0** already ships this API — no new dependency.

**Bucket provisioning** — in `bootstrap.ProvisionBuckets`, after the KV buckets:

```go
const CoreObjectsBucket = "core-objects" // backed by stream OBJ_core-objects

_, err := s.js.CreateOrUpdateObjectStore(ctx, jetstream.ObjectStoreConfig{
    Bucket:      CoreObjectsBucket,
    Description: "Lattice Core Objects — off-graph binary blob store",
    Storage:     jetstream.FileStorage,
    MaxBytes:    coreObjectsMaxBytes, // store-level cap (config; default e.g. 5 GiB)
})
```

**`verify-kernel.go`** — add an Object Store assertion (note: it is **not** a KV bucket, so it's a new check shape, not a line in the existing KV-bucket loop):

```go
if _, err := js.ObjectStore(ctx, bootstrap.CoreObjectsBucket); err != nil {
    failures = append(failures, fmt.Sprintf("MISSING Object Store: %s (%v)", bootstrap.CoreObjectsBucket, err))
}
```

**Bootstrap version bump `"9"` → `"10"`** (`internal/bootstrap/nanoid.go`): add `case "10": return nil` to `checkVersion`, set `Version: "10"`, update the want-string in the mismatch error, and append a version-history note ("10": core-objects Object Store added — provisioning a new store changes kernel topology a stale file would not match). This mirrors the existing "9" precedent (a bucket *retirement* bumped 8→9). `generate()` is **unchanged** — the object store adds **no** primordial NanoIDs and **no** Core-KV keys, so `PrimordialVertexKeyCount` (29) is unaffected; the bump exists only to force `make down && make up` so the new store is present.

---

## 4. Graph model

For a photo on identity `<idId>` whose bytes hash to `digest`, with `oid = crypto.sha256NanoID("object:" + digest)`:

```
vtx.object.<oid>                                         class="object", data={}  (root minimal, D5)
vtx.object.<oid>.content                                 aspect, data={
    digest:      "SHA-256=<base64url>",                   # integrity (NATS format)
    size:        184213,
    contentType: "image/jpeg",
    storeName:   "<provisional-nanoid>",                  # the core-objects object name
    firstSeenAt: "2026-06-19T18:00:00Z"
}
lnk.object.<oid>.photoOf.identity.<idId>                 link, data={ filename:"me.jpg", attachedAt:"…" }
```

- **Direction (Contract #1 §1.1):** the object arrives *after* the owner, so object = **source**, owner = **target**. Reads "object photoOf identity." For a lease PDF: `lnk.object.<oid>.signedLeaseOf.leaseapp.<id>`.
- **`linkName`** (`photoOf`, `signedLeaseOf`) is caller-supplied and validated only as a localName (`[a-z][a-zA-Z0-9]*`). No per-linkName DDL (permissive default, Contract #1 §1.5/§1.6) — the type-agnostic op stays generic.
- **Single-valued vs multi-valued** is a *client* decision expressed via `replaceObjectId` (§8), not a schema constraint.

---

## 5. The `objects-base` package

`packages/objects-base/` — same layout as `service-domain`: `manifest.yaml`, `package.go`, `ddls.go`, `permissions.go`, `package_test.go`.

```go
var Package = pkgmgr.Definition{
    Name:        "objects-base",
    Version:     "0.1.0",
    Description: "Generic large-object vertex type + attach/detach/tombstone ops; the graph side of the off-graph blob plane.",
    Depends:     nil, // type-agnostic; depends on nothing
    DDLs:        DDLs(),
    Permissions: Permissions(),
    OpMetas:     OpMetas(),
}
```

**DDLs() — one vertex DDL** (`pkgmgr.DDLSpec`), `CanonicalName: "object"`, `Class: "meta.ddl.vertexType"`, carrying **three ops** as `PermittedCommands` and the Starlark that branches on them, plus the required self-description aspects (`InputSchema`, `OutputSchema`, `FieldDescription`, `Examples`):

| Op | What its Starlark emits | Who submits it |
|---|---|---|
| `AttachObject` | upsert `vtx.object.<oid>` (+ `.content` aspect) `CreateOnly`; create `lnk.object.<oid>.<linkName>.<tgtType>.<tgtId>` with `{filename, attachedAt}`; if `replaceObjectId` set, **tombstone** `lnk.object.<replaceObjectId>.<linkName>.<tgtType>.<tgtId>` + emit `ObjectLinkRemoved`. Returns `{primaryKey: lnk-key}`. | Loupe (user attach/replace) |
| `DetachObject` | tombstone `lnk.object.<oid>.<linkName>.<tgtType>.<tgtId>` + emit `ObjectLinkRemoved{oid, storeName}`. Returns `{primaryKey: lnk-key}`. | Loupe (user remove) |
| `TombstoneObject` | tombstone `vtx.object.<oid>` + `.content` aspect (OCC on the vertex revision). Returns `{primaryKey: vtx-key}`. | the GC worker only (§11) |

`InputSchema` (AttachObject) — required `{digest, size, contentType, storeName, targetKey, linkName}`, optional `{filename, replaceObjectId}`. The script:
1. validates `linkName` matches `[a-z][a-zA-Z0-9]*` and `targetKey` parses + is a **live** vertex (declared in `contextHint.reads`, validated per the §2.5 read-validation contract);
2. computes `oid = crypto.sha256NanoID("object:" + digest)` (the existing single-arg builtin);
3. emits the mutations above with `CreateOnly` (dedup: an existing `vtx.object.<oid>` / `.content` is skipped, the link is the only new key on a fresh attach).

**Permissions()** — grant the three ops to `operator` (scope `any`), the same idiom service-domain uses:

```go
[]pkgmgr.PermissionSpec{
    {OperationType: "AttachObject",    Scope: "any", GrantsTo: []string{"operator"}, Note: "…"},
    {OperationType: "DetachObject",    Scope: "any", GrantsTo: []string{"operator"}, Note: "…"},
    {OperationType: "TombstoneObject", Scope: "any", GrantsTo: []string{"operator"}, Note: "GC-internal."},
}
```

**OpMetas()** — `AttachObject`, `DetachObject` made `forOperation`-resolvable (so a future Loom step could bind them). **One eventType DDL** `ObjectLinkRemoved` (`Class: "meta.ddl.eventType"`) — emitted by DetachObject and the replace-leg of AttachObject; consumed by the GC worker (§11).

**Deterministic requestId derivation** — factor the bridge's `deriveID(namespace, input)` into a shared `substrate.DeriveNanoID(namespace, input string) string` (today it's duplicated, unexported, in `internal/bridge/token.go`; Loom has its own). Loupe and the GC worker call it Go-side; the Starlark `oid` uses the existing `crypto.sha256NanoID`.

---

## 6. Write path — attach a photo to identity `<idId>`

```
1. Loupe receives POST /api/objects  (multipart: file part + targetKey=vtx.identity.<idId> + linkName=photoOf)
   — streams the file part through a 25 MB-capped reader; generates storeName = substrate.NewNanoID().

2. Loupe: ObjectPut("core-objects", storeName, fileReader)
   → ObjectInfo{ Digest:"SHA-256=…", Size }.        # NATS chunks at 128 KB, constant memory
   — oid = substrate.DeriveNanoID("object:", digest)  (Go-side mirror of crypto.sha256NanoID)
   — bytes now exist; NO graph record yet → the orphan window.

3. Loupe arms the GC check (belt-and-suspenders for "op never lands"):
   Publish to core-schedules — see the exact message in §11.

3a. Dedup probe (single pass, optional): KVGet vtx.object.<oid>.
    If it already exists → ObjectDelete("core-objects", storeName) the just-uploaded dup,
    and submit AttachObject without minting a new vertex (CreateOnly skips it anyway).

4. Loupe submits AttachObject by publishing the envelope below to ops.default.
```

**The exact message published to `core-operations`** (subject `ops.default`, JetStream publish, header `Lattice-Reply-Inbox: <inbox>`; this is `processor.OperationEnvelope`):

```json
{
  "requestId":     "<DeriveNanoID(\"object:attach:\", digest|targetKey|linkName)>",
  "lane":          "default",
  "operationType": "AttachObject",
  "actor":         "vtx.identity.<adminId>",
  "submittedAt":   "2026-06-19T18:00:00Z",
  "class":         "object",
  "contextHint":   { "reads": ["vtx.identity.<idId>"] },
  "payload": {
    "digest":      "SHA-256=<base64url>",
    "size":        184213,
    "contentType": "image/jpeg",
    "storeName":   "<provisional-nanoid>",
    "filename":    "me.jpg",
    "targetKey":   "vtx.identity.<idId>",
    "linkName":    "photoOf"
  }
}
```

```
5. Processor commits atomically (one batch): upsert vtx.object.<oid> + .content (CreateOnly),
   create lnk.object.<oid>.photoOf.identity.<idId>. → Core KV → CDC → Refractor projects the
   vertex's reference metadata + the link (adjacency updated). Bytes never enter this path.

6. Reply on the inbox (processor.OperationReply): { status:"accepted", primaryKey:"lnk.object.<oid>.photoOf.identity.<idId>", revisions:{…} }.
   The object vertex now has a live link → when the GC check fires at now+grace it is a no-op.
```

**Ordering invariant:** bytes first, then graph. A failed op leaves only collectable bytes; a failed upload writes no graph. Never partial graph state.

---

## 7. Read path

```
GET /api/objects/<oid>
  1. KVGet vtx.object.<oid>.content → { storeName, contentType, size, digest }.
  2. ObjectGet("core-objects", storeName) → io.ReadCloser.
  3. Stream to the HTTP response: Content-Type from the aspect, Content-Length=size.
     (NATS verifies the digest as it streams → a corrupt blob surfaces as a read error.)
```

The Refractor is never in the byte path. Loupe proxies the bytes (no direct browser↔Object-Store handle in v1).

---

## 8. Update — "here's my new photo"

New bytes → new `digest D2` → new `oid2`; the prior photo was `oid1`. Loupe already knows `oid1` (it's displaying the current photo via the `photoOf` link) and passes it as `replaceObjectId`.

```
1–3. Upload D2, arm GC for oid2.  (steps 6.1–6.3 for the new bytes)
4.   Submit AttachObject with payload.replaceObjectId = "<oid1>" (everything else as §6).
5.   Processor commits atomically:
       upsert vtx.object.<oid2> + .content; create lnk.object.<oid2>.photoOf.identity.<idId>;
       TOMBSTONE lnk.object.<oid1>.photoOf.identity.<idId>  (key reconstructed deterministically
       from replaceObjectId+linkName+targetKey — no scan); emit ObjectLinkRemoved{oid1, storeName1}.
6.   The ObjectLinkRemoved event arms a GC check for oid1 (§11, trigger T2). When it fires:
       if oid1 has 0 live links → TombstoneObject(oid1) + ObjectDelete(storeName1).
       if another owner still links oid1 (dedup) → kept.
```

The old photo's bytes are reclaimed by the **same** refcount machinery — no special update-cleanup path (D6). Re-uploading the *identical* photo (`D2==D1`) is a near no-op (ObjectPut dedups, the link already exists). **Replace** (`replaceObjectId` set) and **append** (omitted, multi-valued, e.g. many lease documents) are the same op.

---

## 9. Detach — explicit remove

```
DetachObject { targetKey, linkName, oid }  → tombstone the one link + emit ObjectLinkRemoved{oid, storeName}.
GC reclaims the object iff that was its last live link.
```

---

## 10. Idempotency

**Mechanism (Contract #4):** the envelope carries `requestId` (a valid 20-char NanoID); Processor **step 2** looks up `vtx.op.<requestId>`; a hit returns the prior reply (`BuildDuplicateReply`) and does **not** re-commit. The tracker is committed atomically with a **24 h TTL** (`TrackerTTL`, architecture-locked).

**How we get it here:** Loupe derives `requestId = DeriveNanoID("object:attach:", digest|targetKey|linkName)` (and includes `replaceObjectId` in the seed for a replace, so a replace ≠ a plain attach). Any retry from any layer — browser→Loupe, Loupe→core-operations, JetStream redelivery — recomputes the same id and collapses on the tracker; the retry gets the original link back. Same idiom as the bridge's `deriveReplyRequestID`.

**Three independent layers (defense in depth):**
1. **Op layer (primary):** deterministic `requestId` → 24 h tracker dedup → exactly-once commit.
2. **Graph layer:** `AttachObject` is naturally idempotent — `CreateOnly` upsert of the vertex/aspect (conflict = exists) + `CreateOnly` link (conflict = already linked). Even a tracker *miss* (>24 h) can't double-create.
3. **Byte layer:** the `ObjectPut` under a provisional name is the one non-idempotent write; it self-heals via the §6.3a dedup probe + GC. Content-addressing + GC make the bytes *convergent* while the requestId makes the op *exactly-once*.

**The one wrinkle — detach→re-attach within 24 h.** For attach/replace, content-deriving the requestId is perfect (retries dedupe; a redundant re-attach is a harmless no-op since the link already exists; a replace gets a new id because D2≠D1). The single corner: detach the same object from the same slot, then re-attach it *within 24 h* — the tracker is still live, so the content-derived id would be deduped and the link wouldn't be recreated. **Resolution:** a re-attach after a detach is a new user intent, so Loupe **salts** that requestId (include the prior link's tombstone revision, or fall back to `substrate.NewNanoID()` for a user-initiated re-attach) — content-derive for *automatic retries of one action*, mint fresh for a *distinct action*.

---

## 11. Garbage collection — Weaver convergence + a minimal byte-janitor

GC is **vertex-centric, event-driven, and built on the existing engines** — not a bespoke scanner, not an adjacency read. "An object vertex with zero live links should not exist" is a **declarative convergence / retention invariant**, which the architecture assigns to **Weaver** ("Retention policy enforcement via Weaver", arch L280/L296; P4). So orphan reclamation is a **Weaver convergence target**, and the *only* new component is a minimal byte-janitor for the one off-graph side effect.

```
   (link tombstoned: detach / replace / owner-detach) ─► link CDC ─► Refractor
                                                                          │
   [Refractor]  objectLiveness TARGET LENS  ───────────────────────────────┘
       projects each object vertex with an `orphaned` gap flag (0 live links);
       computed from Refractor's OWN internal adjacency (never read externally)
                                                                          │  weaver-targets row
                                                                          ▼
   [Weaver]  meta.weaverTarget (objects-base) — orphaned row = a GAP
       GapAction: triggerLoom(reclaimObject, Subject: row.objectKey)        ── Contract #10 §10.8
                                                                          │
                                                                          ▼
   [Loom]   reclaimObject pattern → submits TombstoneObject(oid, expectedRev=R)
                                                                          │
                                                                          ▼
   [Processor]  soft-deletes vtx.object.<oid> (OCC on rev R) → emits ObjectTombstoned{oid, storeName}
                                                                          │  core-events
                                                                          ▼
   [object-store-manager]  on ObjectTombstoned: KVGet vtx.object.<oid>
       still tombstoned? → substrate.ObjectDelete(storeName)    revived? → skip (bytes stay)
```

**Loop A (orphan → tombstone) = Weaver + Loom + Processor** — the convergence/retention engine detecting the gap and remediating it through the ledger, exactly as it does for the lease vertical (the `WeaverTargetSpec{Gaps: {…: triggerLoom}}` shape lease-signing already ships). objects-base just **declares** the `objectLiveness` target lens + the `meta.weaverTarget` + the one-step `reclaimObject` Loom pattern. **No bespoke orphan-scanner.**

**Loop B (tombstone → byte delete) = the object-store-manager** — the sole new component, and genuinely minimal: it consumes `ObjectTombstoned` from `core-events` and calls `substrate.ObjectDelete`. This is the *only* off-graph action (Weaver / Loom / Processor never touch the object store), so it needs a dedicated always-on consumer — the role Andrew named it for. It is the **universal** byte-reclaim path: *any* op that tombstones an object vertex (Weaver-driven, or a future direct tombstone) triggers it.

**Race-safety (resolves the reviewers' C1/C2), entirely within existing discipline:**
- **The target lens lags (it's a projection)** — fine: Weaver already handles lag via its mark/lease/deterministic-dispatch discipline, and the **TombstoneObject op carries OCC on the vertex revision**. Every `AttachObject`/`DetachObject` writes the object vertex (create / revive / touch) in the *same atomic batch* as its link mutation, so the vertex revision tracks link-set changes; a concurrent re-link moves `R` → the Loom-submitted `TombstoneObject(expectedRev=R)` aborts. A lagging target can never tombstone a live-and-linked object.
- **Byte delete is irreversible → last + double-guarded:** only on a *committed* `ObjectTombstoned` event **and** a final `KVGet` showing the vertex still tombstoned.
- **Revive brings fresh bytes (CC2):** `AttachObject` on a tombstoned `oid` revives the vertex and never dedup-skips the upload, re-pointing `.content.storeName` to a freshly uploaded blob — so a deleted-then-re-added object is always restored. **No data loss.**

**Triggers** (what tombstones a link so the lens reprojects an orphan): `DetachObject` and the replace-leg of `AttachObject` directly. **Owner-tombstone** has no platform cascade (verified, §18 CC4) — closed type-agnostically by giving the `objectLiveness` lens **dead-target awareness** (a link counts as live only if the link *and its target vertex* are non-`isDeleted`), so a dangling link to a tombstoned owner reprojects the object as orphaned without any extra consumer.

**Never-attached bytes** (upload succeeded, `AttachObject` never landed → bytes with no vertex) are invisible to a vertex-search. Handled by a **secondary reconcile in the object-store-manager**: low-cadence, list `core-objects`, and for any object whose digest-derived `oid` has no live referring vertex and is older than a grace window, `ObjectDelete` it. The *only* store-side pass — bounded (object store only, never graph×store), explicitly a backstop, v1b.

**Everything is via `substrate`** (Andrew's point 3): the manager's `core-events` subscription (a `substrate` durable consumer) + `substrate.ObjectDelete` + the reconcile's object listing. Loom/Weaver already submit ops via `substrate`. No direct `nats.go` / `jetstream` handles anywhere in this work.

---

## 12. Limits / config

- `OBJECTS_MAX_UPLOAD_BYTES` (default **25 MiB**) — enforced by a capped reader at upload; a larger part is rejected before any ObjectPut.
- `coreObjectsMaxBytes` — the store-level cap on `core-objects` (default e.g. 5 GiB).
- `OBJECTS_GC_GRACE` (default e.g. **15 m**) — the `@at` offset for the deferred check; must exceed worst-case attach latency.
- No content-type allow-list in v1 (trusted operator); `contentType` is recorded on the vertex. Resumable / very-large uploads (stage-then-finalize the digest) are a later increment.

---

## 13. Loupe integration

- `POST /api/objects` — multipart (file + `targetKey` + `linkName` + optional `replaceObjectId`) → §6 write path. Returns `{oid, primaryKey}`.
- `GET /api/objects/<oid>` — §7 read path (streams bytes).
- `DELETE /api/objects/<oid>?targetKey=&linkName=` — DetachObject.
- A **Files** affordance: attach a profile photo to an `identity`, a signed-lease PDF to a `leaseapp` (the Loftspace vertical); view/download inline. Dogfoods the blob plane end-to-end.
- Loupe already streams ops via `output.SubmitOp` and reads Core KV via the substrate helpers; this adds the object substrate calls + the two handlers + the deterministic-requestId derivation.

---

## 14. Contract touch (flag for Andrew — per the autonomous mandate)

Contract #7 §7.1 currently states primordial seeding is the **sole** sanctioned non-Processor write path. The object store makes that **two** (trusted clients writing bytes). I'll edit §7.1 **in-place, uncommitted**, framing `core-objects` as the bytes plane parallel to Health-KV (Decision #4), for your ratification before commit. No other frozen contract changes (the op envelope, schedule message, and tracker are all used as-is).

---

## 15. Build plan + gates

**v1a — attach / read / limits (no GC yet; orphans accumulate, harmless):**
1. `substrate/object.go` + `CoreObjectsBucket` provisioning in `ProvisionBuckets` + `verify-kernel` Object-Store check + bootstrap version `9→10`.
2. `substrate.DeriveNanoID` (factor out of `bridge/token.go`).
3. `packages/objects-base/` — `object` DDL (Attach/Detach/Tombstone) + permissions + opMetas + `ObjectLinkRemoved` eventType.
4. Loupe `POST /api/objects` + `GET /api/objects/<oid>` + `DELETE` + Files tab; deterministic requestId.

**v1b — GC:**
5. `cmd/objgc` — the fired-`schedule.objgc.fired.>` consumer (the check) + the `ObjectLinkRemoved` consumer (T2); Loupe arms T1 at upload.
6. Owner-tombstone cascade: verify platform semantics, wire the remaining trigger.

**Gates:** build/vet/lint; a substrate object round-trip test (Put→Get digest match, Delete); an end-to-end **upload→AttachObject→read** test; the FR-style **"blob never enters the Refractor"** assertion (a Refractor projection of the object vertex carries reference metadata only, never bytes); `verify-kernel` (Object Store added); **`make verify-package-objects-base`** (new — a package touching DDL/permissions needs its install-verify script per the house rule); an idempotency test (same `requestId` ⇒ one link, duplicate reply); a GC test (never-attached → reclaimed after grace; referenced → kept; replaced → old reclaimed).

---

## 16. Non-goals (stay Phase 3+)

- The **untrusted / multi-user** path: Gateway-mediated upload/download, signed-URL grants, per-user blob authorization. (Needs read-path auth + Gateway.)
- Direct browser↔Object-Store handles (Loupe proxies bytes for v1).
- Image transforms / thumbnails / virus scanning.
- Client-side pre-hash to skip the dedup-path upload; resumable multi-request uploads; byte-layer cross-node dedup/sync (the Edge-node payoff of content addressing).

---

## 17. Original open questions — resolved

1. **Primordial vs package bucket** → **primordial** (D9; `pkgmgr` can't provision an Object Store).
2. **One bucket vs per-type/cell** → **single `core-objects`** (D10).
3. **Max size + content-type** → 25 MiB env-config cap; no allow-list in v1 (D12).
4. **GC trigger** → **per-object deferred point-check** on `core-schedules`, hosted by `cmd/objgc` (D11) — not a sweep, not a Refractor reconciler. (Liveness is read authoritatively from Core KV, **not** adjacency — see CC1.)

---

## 18. Design-review corrections (BINDING — supersede the sections named)

The 2-lens design review (architecture/feasibility + adversarial/security) found **two CRITICAL data-loss bugs in the GC** and several correctness gaps, all pre-code. Winston's adjudication below is binding; where a correction conflicts with an earlier section, the correction wins. Each is grounded in the real code.

**CC1 (CRITICAL — supersedes §11 step 2; resolves C2 + MAJOR-1). GC liveness is read authoritatively from Core KV, never from the lagging adjacency projection.** `refractor-adjacency` is a CDC projection the Refractor builds asynchronously (`internal/refractor/consumer/bootstrap.go`); it lags commits and can stall, so "0 edges in adjacency" does **not** mean "0 live links" — trusting it reaps freshly-attached objects. The GC instead lists `lnk.object.<oid>.>` from `core-kv` (a bounded, subject-filtered list), `KVGet`s each, and counts those with `isDeleted == false`. Adjacency is dropped from the delete decision (it may still be used lag-tolerantly as a *trigger* only, CC4). **VERIFIED (build-blocker cleared):** nats.go v1.52.0 KV exposes `ListKeysFiltered(ctx, "lnk.object.<oid>.>")` (`jetstream/kv.go:1432`) → `substrate.KVListKeysPrefix(bucket, prefix)` is a thin wrapper over it. It uses `IgnoreDeletes` (NATS hard-delete markers) but our link tombstones are **soft** (in-body `isDeleted:true`, still live KV entries), so they DO appear in the filtered list and the GC `KVGet`s each to read `isDeleted` — exactly what we want. `cmd/objgc` reading the public `core-kv` is fine (no private-bucket coupling). **Dead-target awareness (CC4):** a link counts as live only if the link **and its target vertex** are both `isDeleted == false`, so a tombstoned owner's dangling link never holds an object alive.

**CC2 (CRITICAL — supersedes §11 steps 3–4; resolves C1). Bytes are deleted LAST, behind a two-phase guard; `AttachObject` revives a tombstoned object vertex.** The old design's `ObjectDelete` ran unconditionally after a `TombstoneObject` whose OCC was on the *vertex* revision — but a re-attach only adds a *link* (the vertex rev is unchanged), so the OCC could succeed against a now-live object and the byte delete ran regardless → data loss on "remove then re-add the same photo." Corrected reclaim:
- **Phase 1** (fired check): authoritative live-link scan (CC1). `>0` → ack. `==0` → `TombstoneObject(oid)` (OCC on the vertex rev), then arm a **phase-2** schedule `schedule.objgc.sweep.<oid>` at `now + grace2`.
- **Phase 2** (fired): re-run the authoritative live-link scan **and** confirm the vertex is still tombstoned. Only then `ObjectDelete` (target read from the vertex's current `.content.storeName`, never a stale payload — CC3) and hard-remove the vertex. Any live link or a revived vertex → abort.
- **Revive:** when `AttachObject` finds `vtx.object.<oid>` present-and-tombstoned, it emits an `update` setting `isDeleted:false` (+ the new link) instead of a create, landing a new vertex revision so phase 2 aborts. **VERIFIED (build-blocker cleared):** `op:update` reliably revives a soft-tombstoned vertex — there is **no** guard against updating a tombstoned key; the committer applies a revision-conditioned `KVUpdate` and defaults `isDeleted:false` on `update` (`step8_commit.go:298,314`). Loupe pre-reads the tombstoned vertex (`KVGet` returns soft-tombstoned entries by design — `kv.go` doc) so the script has its revision for the OCC update. The `.reclaiming`-marker fallback is **not needed.** Invariant: **bytes are irrecoverable, so they are deleted only after a `grace2` quiet window with an authoritative re-check.**

**CC3 (MAJOR — supersedes §6 step 3 + §11 T1/payload; resolves M3a, M4, M5-keying, m5). T1 is keyed on `storeName` and armed BEFORE the `ObjectPut`.** Keying T1 on `oid` can't catch a crash between `ObjectPut` and arming (the bytes exist but nothing recorded them). Corrected: Loupe generates `storeName`, arms `schedule.objgc.byname.<storeName>` at `now+grace`, **then** `ObjectPut`s, then submits the op. The T1 check: `ObjectGetInfo(storeName)` → absent ⇒ ack (Put never completed); else derive `oid` from the returned digest and reclaim the bytes unless a **live** `vtx.object.<oid>` references *exactly this* `storeName`. This is crash-safe and also cleans the dedup-duplicate upload and partial-upload cases. The GC always resolves its delete target from the vertex's current `.content.storeName` (T2) or the `storeName` under test (T1) — never a stale schedule payload. *Build:* `cmd/objgc` needs a Go-side `oidFromDigest(digest)` that reproduces the Starlark `crypto.sha256NanoID` exactly — factor the PCG-seeded `DeterministicNanoID` into a shared helper both call (it is **not** the bridge `deriveID` algorithm — m1).

**CC4 (resolves M3b; now a v1b GC concern, NOT a v1a blocker). VERIFIED: there is no platform auto-cascade — the spec's "existing cascade" assumption was wrong.** Tombstoning a vertex does **not** tombstone its links; ops that remove links do it **explicitly** (identity-hygiene's merge enumerates a secondary identity's incident inbound/outbound links via a lens and tombstones each — `packages/identity-hygiene/{lenses,ddls}.go`; orchestration-base's reassign tombstones the old `assignedTo` link inline — `ddls.go:269`). So an owner-tombstone with a photo still attached leaves a non-`isDeleted` `photoOf` link pointing at a dead owner. Two-part resolution, fully type-agnostic:
- **Check side (lands with the GC, CC1):** the live-link count requires the link **and its target vertex** both non-`isDeleted` — so a dangling link to a tombstoned owner never holds an object alive. This is the correctness fix and it's cheap (one extra `KVGet` of the target per link, usually 1).
- **Trigger side (v1b):** `cmd/objgc` consumes vertex-tombstone CDC (a durable consumer on `KV_core-kv` filtered to `$KV.core-kv.vtx.>`, handler acts only when `isDeleted` flips true) and arms a GC check for each object with an inbound link to the dead vertex (lag-tolerant adjacency read, trigger-only). Event-driven, not a sweep. **Alternative if the CDC volume is a concern:** a self-re-arming per-object heartbeat (each live object re-arms its own slow check) — catches every orphan class without a CDC firehose, at the cost of N standing timers. *Trigger style is Andrew's call (his GC domain); the check-side fix makes the interim a bounded, non-permanent leak regardless.* Because v1a ships no GC, this does not block v1a.

**CC5 (MAJOR — supersedes §5 AttachObject + §10 layer-2; resolves M2 + m1). Idempotent-upsert + collision detection via the identity-domain precedent.** Loupe pre-reads `vtx.object.<oid>` and conditionally declares it in `contextHint.reads` (a `reads` *miss* is fatal — step4:152 — so a maybe-absent key is never declared). The script branches on `oid in state`: **present+live** → compare `state[oid]` content digest to the submitted digest (mismatch ⇒ reject `DigestCollision`, m1) and emit **link-only** (dedup); **present+tombstoned** → revive + link (CC2); **absent** → create vertex + `.content` aspect + link. Concurrent same-digest, different owners: the loser's vertex `create` `RevisionConflict`s and the whole op is rejected — Loupe **retries** (re-reading `oid`, which now exists → link-only branch) and converges; the failed attempt wrote no tracker, so the **same deterministic `requestId`** is reused. This is exactly identity-domain's `vtx.identityindex.<sha256NanoID>` create-or-skip pattern (`packages/identity-domain/ddls.go:382`).

**CC6 (MAJOR — supersedes §6 requestId + §10 wrinkle; resolves M1, M5). Deterministic `requestId` uses the `\x00`-separated, namespace-prefixed `deriveID` idiom — not `|`.** Pin the field sets: attach `requestId = DeriveNanoID("object:attach:", join0(digest, targetKey, linkName, replaceObjectId?))`; GC ops similarly with `join0`. The detach→reattach-within-24h salt is the **detached link's tombstone revision** (deterministic, Loupe can read it) — **never** a random `NewNanoID()` (that destroys retry dedup). `join0` = `\x00`-join (the byte can't appear in any field), mirroring `internal/weaver/actuator.go:131` / `internal/bridge/token.go`.

**CC7 (MAJOR — supersedes §5 step 1; resolves MAJOR-2 + m4). `targetKey` liveness + type-safety are SCRIPT obligations.** The platform only guarantees a `reads` key was *present* at hydrate — it never checks `isDeleted`/class. So `AttachObject` must itself assert `state[targetKey].isDeleted == false` (mirror service-domain's `vertex_alive`, `ddls.go:276`), and must **reject** a `targetKey` whose root is `data.protected == true` or under `vtx.meta.>` (no attaching blobs to kernel/system vertices — the existing protected guard covers update/tombstone of the protected key, not a link targeting it).

**CC8 (MAJOR — supersedes §3/§15; resolves MAJOR-3). Both kernel-verify surfaces get the Object-Store check.** Add the `core-objects` Object-Store assertion to **`scripts/verify-kernel.go`** *and* **`internal/bootstrap/verify.go::VerifyKernel`** (the in-process boot check, which also enumerates buckets). Add a `Conn.ObjectStoreExists` helper to `substrate/object.go` since `VerifyKernel` takes a `*substrate.Conn`.

**CC9 (MINOR — supersedes §12; resolves m2, m3). Enforce the 25 MB cap inside `substrate.ObjectPut`** (it owns the stream) so it isn't bypassable by a non-Loupe writer, not only at Loupe's reader. Document explicitly: `digest`/`size`/`storeName` in the op are **client-asserted and Processor-unverifiable** (the Starlark sandbox can't do I/O) — **Loupe is the trust boundary**; the untrusted/Phase-3 path must derive `oid` from the **server-computed** `ObjectGetInfo(storeName).Digest`, never the client's claim.

**CC10 (MINOR — supersedes §11 T2; resolves M3c). The T2 (and owner-cascade) consumer must publish-the-schedule-THEN-ack** (at-least-once), never ack-then-arm — else a crash between drops the re-arm and the object leaks. Outbox events are durable/at-least-once (`internal/processor/outbox/consumer.go`), so the §11 "best-effort event" worry was wrong — but the consumer's own ordering must be publish-then-ack.

**CC11 (MINOR — supersedes §14/§5/§11; resolves MINOR-2/3/1).** (a) The contract edit targets **Contract #7 §7.2** (the "sole sanctioned non-Processor write path" sentence), not §7.1; optionally add a §7.1 note that bytes-plane writes carry no graph state and don't touch the Capability Lens. (b) The `ObjectLinkRemoved` eventType DDL is **self-documentation only** — package events aren't validated against eventType DDLs at commit; emission/consumption work regardless (keep it for docs). (c) `<oid>` is always a 20-char NanoID, so it can never equal the reserved `fired`/`sweep` tokens in the schedule subject space — no guard needed (noted, cf. Weaver's `firedToken`).

**Build-blocking acceptance criteria (added to §15):** (1) a test proving the GC **never deletes bytes a live link references**, including a re-attach landing during reclaim; (2) the concurrent-same-digest convergence test (CC5); (3) the crash-after-`ObjectPut` orphan is reclaimed (CC3); (4) a dead-target/owner-tombstoned link does not hold an object alive (CC4 check-side); (5) `DigestCollision` rejection on a digest mismatch over an existing `oid` (CC5/m1).

**Pre-build verifies — ALL CLEARED (2026-06-19):** (a) `op:update` revives a soft-tombstoned vertex ✓ (CC2, no commit-path guard, `step8_commit.go`); (b) no platform link-cascade on vertex-tombstone ✓ — explicit-tombstone is the precedent, resolved in CC4; (c) subject-filtered KV list ✓ (`ListKeysFiltered`) — now needed only for the never-attached reconcile backstop, since GC no longer scans Core KV for liveness (§19). v1a is unblocked.

---

## 19. GC reframe + boundary corrections (Andrew, round 5 — BINDING; supersede §11 as written and §18 CC1/CC4)

Three corrections from Andrew. They **supersede** the earlier scan/adjacency/deferred-check GC: §11 is rewritten above to match, and §18 **CC1 and CC4's adjacency/Core-KV-scan mechanics are withdrawn** (CC1's `KVListKeysPrefix` survives only as the never-attached reconcile backstop).

1. **GC is vertex-centric + event-driven, not a scan.** The blob is reclaimed when its **object vertex** is soft-deleted by an op, which publishes a `core-events` event the **object-store-manager** consumes to delete the bytes. Orphan detection = "a search of vertices that are not linked" = a **Lens**, never a Core-KV `lnk.object.<oid>.>` scan at delete time. (New §11 model: lens → `TombstoneObject` op → `ObjectTombstoned` event → `ObjectDelete`.)
2. **`refractor-adjacency` is Refractor-private — no external reads.** The manager reads the **sanctioned Lens output bucket**; the Refractor uses its own adjacency *internally* to build that lens. Every adjacency read in the old CC1/CC4 (including the "lag-tolerant trigger" read) is removed.
3. **Only `substrate` touches `nats.go`.** All manager / Loupe / package-client NATS access goes through `substrate`. **Merged `main` (`1ae8f60`, fast-forward)** so this builds on the shipped Refractor→substrate migration; new helpers now available: `KVPurge`, `KVStatus`, `OpenKV` (a `*KV` bucket handle), `WatchKVUpdates`. `substrate/object.go` (this work) adds the object-store wrapper in the same style.

**Added guard (race-safety in the new model):** every `AttachObject`/`DetachObject` writes the object vertex (create / revive / touch) in the *same atomic batch* as its link mutation, so the vertex revision tracks link-set changes and the lens-driven `TombstoneObject` OCC is authoritative. Byte delete is gated on a committed `ObjectTombstoned` event + a final still-tombstoned `KVGet`.

**Still valid from §18:** CC2 (revive — verified), CC3 (deterministic-requestId idempotency; the storeName-keyed *deferred check* is withdrawn but storeName-on-the-vertex + the reconcile backstop remain), CC5 (idempotent-upsert + `DigestCollision`), CC6 (`\x00`-`deriveID`), CC7 (script-side `targetKey` liveness + protected-target reject), CC8 (both verify-kernel surfaces), CC9–CC11.

**`objectLiveness` lens feasibility — CONFIRMED (round 6 verify).** It is an **actorAggregate convergence lens** mapping directly onto `leaseApplicationComplete` (`packages/lease-signing/lenses.go`): anchor `MATCH (o:object {key:$actorKey})` (AnchorType `object`), `OPTIONAL MATCH (o)-[r]->(owner)`, project `orphaned = (count(live links) = 0)` and `violating = orphaned` — the same `OPTIONAL MATCH … count()=0 → missing_*` idiom, so we use the supported "one row per candidate + flag" pattern and **avoid** the deferred true-retraction projection. The generic `linkName` is fine: an **untyped** relationship `[r]` matches any edge (`executor.go:548` — empty `rel.Type` skips the type filter). Tombstoned links are already absent from adjacency (`removeEdge`); a tombstoned **owner** (dead-target, CC4) is excluded via an `owner.isDeleted` CASE test; a detach reprojects the object anchor through the link fan-out (1.5.6/1.5.8). *No engine capability is missing.* Caveat: authoring the cypher is a genuine v1b task with the documented grouping/null-restore subtleties (`= null` not `IS NULL`, no filtering `WHERE` on the orphan optional, one-row-per-anchor guard) — it needs its own `lens_cypher_test`.

**v1b uses the existing engines for Loop A (corrected — round 6, Andrew):** orphan reclamation is **Weaver convergence**, not a bespoke component (arch L280/L296: "Retention policy enforcement via Weaver"; P4). objects-base **declares** (a) the `objectLiveness` target **Lens** (each object vertex + an `orphaned` gap flag, with dead-target awareness), (b) a `meta.weaverTarget` whose gap → `triggerLoom(reclaimObject)` (Contract #10 §10.8, the lease-signing `WeaverTargetSpec` shape), (c) the one-step `reclaimObject` **Loom** pattern that submits `TombstoneObject`, (d) the `TombstoneObject` op (OCC on vertex rev) + the `ObjectTombstoned` eventType. **The only NEW component is `cmd/object-store-manager` (+ `internal/objectmanager`) = Loop B alone:** consume `ObjectTombstoned` → `substrate.ObjectDelete`, plus the never-attached reconcile backstop. It exists solely because byte deletion is the one off-graph side effect Weaver/Loom/Processor cannot perform.

---

## 20. v1b GC — design-review outcome (2-lens, 2026-06-19; BINDING — supersedes §11/§19 GC dispatch where they conflict)

The v1b GC design (§11/§19) went through the 2-lens design review (architecture/feasibility + adversarial/data-loss) **before any v1b code**. The review verdict was **NEEDS-DESIGN-FIX**: the detection half is sound and feasible, but the **dispatch half (Loop A: orphan → `TombstoneObject`) is not buildable as written**, and there is one **architectural decision that is Andrew's to make** (it touches a frozen Weaver surface and/or reverses the round-6 "use Weaver, not a bespoke component" call). Every claim below is grounded in real engine code (`file:line`), verified during the review.

### 20.1 Binding corrections to §11/§19 (apply on build — not decisions, just fixes)

- **C-a — the gap column MUST be `missing_owner`, not `orphaned`.** Weaver only dispatches columns with the `missing_` prefix (`internal/weaver/state.go:17`; `evaluator.go` `openGapColumns`). A column named `orphaned` is invisible → the row stays `violating` forever and nothing fires. The lens projects `missing_owner` (the gap) + a separate `violating` bool + `entityKey` + a `KeyColumn` (Weaver requires the key echo + `splitRowKey` acceptance, exactly as `lease-signing/lenses.go:44-51`).
- **C-b — dead-target awareness is `count(owner) = 0`, NOT an `owner.isDeleted` CASE.** *(⚠ SUPERSEDED by §21: deciding orphan-ness from `count(owner) = 0` over the lagging adjacency reaped freshly-attached objects during projection catch-up — data loss. The lens now decides on the atomic `o.data.liveLinks` counter; dead-target reclaim moves to the deferred owner-cascade trigger. Read §21.)* §19's "`owner.isDeleted` CASE" is impossible and unnecessary: `fetchNode` returns nil for a soft-deleted vertex (`executor.go:467-488`) and `traverseRel` skips a nil neighbour, so a tombstoned owner **never binds** — `owner` is the null sentinel, `owner.isDeleted` is always null, never true. Count the **bound neighbour** instead: `OPTIONAL MATCH (o)-[r]->(owner)` then `count(owner) = 0` (the engine's `count()` skips nulls). A dead owner → null → excluded automatically; a live owner counts. **`count(r)` would be WRONG** (the link's adjacency edge survives an owner-only tombstone), so this is the single most important `lens_cypher_test` assertion. (`o.data.<field>` and `count(owner)` are both verified accessible — see 20.3.)
- **C-c — event naming.** The shipped v1a emitter uses class `object.tombstoned` → subject `events.object.tombstoned` (and `object.detached`). Drop the `ObjectTombstoned`/`ObjectLinkRemoved` "eventType DDL" language (CC11b: package events aren't validated against eventType DDLs). The manager's `FilterSubject` is `events.object.tombstoned`.
- **C-d — `substrate.ObjectList` (DONE in this changeset).** The never-attached reconcile cannot enumerate the store without it, and §19 forbids a raw `jetstream` handle outside substrate. `substrate.ObjectList(ctx, bucket) ([]ObjectInfo, error)` (with `ObjectInfo.ModTime` for the grace basis) is **built + tested** now (`internal/substrate/object.go`, `object_test.go::TestObject_List`).
- **C-e — the reconcile decision keys on "exactly this storeName," not on `oid`.** A live deduped object has ONE canonical `storeName` on its vertex; a redundant duplicate upload of the same content produced a DIFFERENT provisional `storeName` (the §6.3a dup). The reconcile must delete `storeName S` **iff** no live, non-tombstoned `vtx.object.<oidFromDigest(GetInfo(S).Digest)>` has `.content.storeName == S` **AND** `S`'s store-object `ModTime < now - grace`. Without the exact-storeName match it can delete a live object's canonical bytes (CRITICAL). Grace is a backstop for crash-orphans; it must exceed the tracker-TTL-bounded retry horizon, not just "attach latency."

### 20.2 The data-loss analysis (why a naive OCC is insufficient — and the sound fix)

The race the GC must survive: object X loses its last link → lens flags orphaned → Weaver dispatches `TombstoneObject` → **concurrently a new `AttachObject` re-links X via the live-dedup branch** (X still alive, `.content.storeName` unchanged) → if X is tombstoned anyway, the byte-janitor deletes bytes a live link references. **Irreversible data loss.**

- **Self-OCC is INSUFFICIENT.** If `TombstoneObject` asserts only the revision it hydrated *itself*, a re-link that committed *before* the op hydrates is seen as the current state and asserted → the op tombstones a re-linked X. The OCC MUST assert the **orphan-detection** revision (the link-set version the lens saw), not the op's own.
- **The v1a vertex-touch is correct and present.** Every `AttachObject`/`DetachObject` branch (incl. the live-dedup branch) OCC-touches the object vertex (`packages/objects-base/ddls.go` `touch_vertex`; confirmed by the v1a correctness review and `TestObject_Lifecycle`'s revision-bump assertion). So the object vertex revision DOES move on every link-set change — the substrate the OCC needs exists. (The design *prose* in §11/§18-CC5 said "link-only" for dedup; the **code** does link + touch. Prose is stale; code is right.)
- **The sound mechanism — an epoch-in-data CAS.** Carry `data.linkEpoch` on the object vertex, bumped by every `touch_vertex`/create/revive. The lens projects `o.data.linkEpoch` (VERIFIED accessible — 20.3). `TombstoneObject` receives the lens-projected epoch as `expectedEpoch` and asserts `state[objKey].data.linkEpoch == expectedEpoch`, failing `Stale` on mismatch. A re-link between lens-projection and tombstone-commit bumps the epoch → mismatch → the tombstone aborts. This is the only mechanism that closes the live-dedup race.
- **Byte-delete remains gated** on a *committed* `object.tombstoned` event **and** a final authoritative `KVGet objKey` showing `isDeleted == true` (the janitor reads core-kv, not the lagging lens). A revive (CC2) flips `isDeleted:false` with a FRESH `storeName`, so the janitor skips, and even a racing revive is harmless because the event's `storeName` is the abandoned one. **Delete the event's storeName, never re-read `.content.storeName`** (a revive re-points it).

### 20.3 Feasibility — VERIFIED

- `o.data.linkEpoch` (a vertex **root-data** field) is cypher-accessible: `fetchNode` unmarshals the full envelope into `nr.props` and `resolveProperty` checks `nr.props[key]` *before* the aspect fallback (`executor.go:1297-1317`, `467-488`). So `o.data` → the root data map → `.linkEpoch`.
- `count(owner)=0` dead-target exclusion: `fetchNode` returns nil on `isDeleted` → neighbour never binds → `count` skips it.
- The actorAggregate one-row-per-anchor + null-restore idiom is exactly `leaseApplicationComplete` (`lease-signing/lenses.go`); the per-anchor reprojection on a link create/tombstone is real (`projection/driver.go:186` wires `AnchorType:"object"`; `evalLinkFanOut` seeds from both endpoints).

### 20.4 THE DECISION — how Loop A dispatches `TombstoneObject` (RESOLVED 2026-06-19: Option A, ratified by Andrew + built; see §20.5)

**The blocker:** **no Weaver gap action declares `contextHint.reads` for the op it dispatches** — only `assignTask` has hardcoded reads (`strategist.go:145`); `triggerLoom`→`StartLoomPattern` and `directOp` pass none, and Loom's `submitSystemOp` passes `reads: nil` + a `{subjectKey}` payload (`internal/loom/engine.go:853-863`). `TombstoneObject` MUST hydrate the object vertex (to read `linkEpoch` + to OCC), so it cannot be hydrated through any current Weaver/Loom dispatch. Additionally, `directOp`'s auto-injected `expectedRevision` is the **weaver-targets row** revision (`evaluator.go:176` `rowRevision`), not the object vertex revision — unusable as a vertex OCC (hence the epoch-CAS in 20.2, which threads `expectedEpoch` as a `row.<col>` param instead).

**Two options (both touch a frozen surface — Andrew's call, per the contract-change mandate):**

- **Option A — minimal Weaver enhancement (RECOMMENDED).** Give `directOp` a way to declare reads: add `Reads []string` to `GapActionSpec`/`GapAction` (row-templated, e.g. `["row.entityKey"]`), and have `buildPlan`'s `directOp` branch resolve + set `plan.reads`. Then the `meta.weaverTarget` gap is `directOp(TombstoneObject, params:{oid: row.oid, expectedEpoch: row.linkEpoch}, reads:[row.entityKey])`. **Keeps Loop A in Weaver** (honours round-6), no Loom, no bespoke component — Loop B's `object-store-manager` stays the only new component. Cost: a small, additive, frozen-Weaver change (Contract #10 §10.8 gains an optional `reads` on the action) — flag for ratification, build uncommitted.
- **Option B — bespoke reclaimer.** A small reclaimer (in `object-store-manager`) watches the `objectLiveness` lens output bucket (the sanctioned surface, §19) and submits `TombstoneObject` itself with proper `reads` + `expectedEpoch`. No engine change, fully buildable today — but **reverses the round-6 "use Weaver convergence, not a bespoke component" decision** for Loop A.

**Recommendation: Option A.** It is the smaller change, keeps the architecture as Andrew framed it (Weaver owns convergence/retention), and the enhancement (a gap action declaring its op's reads) is generally useful (any future `directOp` that reads its candidate needs it). Option B is the fallback if the Weaver change is unwanted.

### 20.5 Build state — v1b SHIPPED via Option A (2026-06-19; e2e-validated; 3-layer-reviewed)

Andrew ratified the direction (point 1 of his reply: "the vertex id is already in the target lens — supply it"), confirming **Option A**. v1b is **built, unit-tested, and proven end-to-end** (uncommitted on the worktree branch).

**Built:**
- `substrate.ObjectList` (+ `ObjectInfo.ModTime`) + test (C-d).
- objects-base: `data.linkEpoch` on the object root (bumped by `touch_vertex`/create/revive; the one D5 scalar exception); `TombstoneObject` gains the `expectedEpoch` CAS + a hydrated-revision self-OCC + reads `storeName` from the payload + tombstones `.content` **unconditionally** (a mutation needs no hydration — it rides the vertex tombstone's atomic batch).
- The `objectLiveness` lens (`count(owner.key)=0` → `missing_owner`/`violating`, projecting `entityKey`/`linkEpoch`/`storeName`; `AnchorType:"object"`) + `lens_cypher_test` (5 cases incl. the `count(r)` dead-target regression guard). **NOTE the lens projects `storeName` (`o.content.data.storeName`)** so Weaver templates it into the reclaim op — the GC dispatch hydrates only the vertex, and the engine has no string-concat to build the `.content` key for reads.
- The `meta.weaverTarget`: `directOp(TombstoneObject, params:{objectKey:row.entityKey, expectedEpoch:row.linkEpoch, storeName:row.storeName}, reads:[row.entityKey])`.
- Weaver **directOp-reads enhancement** (Option A): `GapActionSpec`/`GapAction` gain a row-templated `Reads []string`; `buildPlan`'s directOp branch resolves + sets `plan.reads`. **Flagged for Andrew** — a §10.8 frozen-surface touch, uncommitted pending ratification (`docs/contracts/10-*.md` unedited).
- `cmd/object-store-manager` + `internal/objectmanager` (Loop B): the `events.object.tombstoned` durable consumer (authoritative `KVGet` re-check → `ObjectDelete` the **event's** storeName; a revived vertex → skip) + the never-attached reconcile (C-e exact-storeName predicate; grace **> 24h** tracker TTL).

**Tested:** the build-blocking invariants — (1) the epoch-CAS aborts a stale-epoch tombstone deterministically (`TestObject_TombstoneEpochCAS_AbortsOnRelink`, the #1 data-loss guard); (2) reconcile spares canonical / reclaims dup+never-attached / spares young; (3/4) revive monotonic epoch + fresh bytes; (5) the lens dead-target matrix; (6) the janitor reads core-kv + skips a revived vertex. **Plus the full Loop A+B convergence e2e** (`internal/objectgc`, `-tags objectgc`, in CI via `make test-object-gc`): attach → detach → the objectLiveness lens → Weaver `directOp(TombstoneObject)` → soft-delete → `object.tombstoned` → manager byte-reclaim, all in-process — **closing the directOp-dispatch + linkEpoch-round-trip + live-event-consume seams** the unit tests could not.

**Pending Andrew (uncommitted):** (a) the `directOp.reads` §10.8 ratification; (b) the Contract #7 §7.2 amendment (v1a's bytes-plane write path). The deterministic concurrent-race-during-a-running-stack remains covered by the deterministic epoch-CAS unit test (a true wall-clock race is non-deterministic; the deterministic abort proof is stronger).

---

## 21. v1b GC — attach-adjacency-lag reclaim race (2026-06-25; BINDING — supersedes §20.1 C-b and §20's adjacency liveness decision)

**The bug (verified live while shipping the LoftSpace Documents tab).** Freshly-attached objects were GC-reclaimed under rapid/concurrent uploads (3/5; a lone unhurried upload survived). The shipped §20 lens decided orphan-ness from `count(owner.key) = 0` over the **refractor-adjacency** projection — a CDC read model that lags the atomic `AttachObject` commit. So a fresh attach commits its link to Core KV, but for a window the link is not yet in adjacency → `count(owner.key) = 0` → the lens flags `missing_owner` → Weaver dispatches `directOp(TombstoneObject, expectedEpoch = <the lens-projected epoch>)`. The §20 epoch-CAS could **not** catch this: no re-link happened, so the current epoch equals the projected epoch → the CAS matches → the tombstone proceeds → the object's bytes are reclaimed. Irreversible data loss.

This is a **distinct race from §20's re-link race.** §20 closes "orphan → concurrent *re-link* → must abort" (a real state change, caught by the epoch bump). §21 is "fresh attach → adjacency *hasn't caught up* → falsely seen orphaned" (no state change at all — the lens simply misreads a lagging projection).

**Why the adjacency signal cannot be salvaged.** The dead-target case (an owner tombstoned while a link still points at it, CC4) and the attach-lag case are **indistinguishable** to the lens: the full engine *skips a dead-neighbour edge entirely* (`executor.go` `fetchNode`→nil→`continue`), so a dangling link to a dead owner and a not-yet-projected fresh link both present as `count(r) = 0, count(owner) = 0`. One must be reaped (dead-target), the other must not (attach-lag) — and no adjacency-derived signal separates them. The original CC1 (§18) had this right ("0 edges in adjacency ≠ 0 live links"); §19 withdrew the Core-KV liveness scan (correctly — the Refractor-private adjacency must not be read externally), leaving the lens trusting adjacency. §21 finishes the job: **stop trusting any lagging projection for the reclaim decision.**

### 21.1 The fix — an authoritative, lag-free live-link counter (BINDING)

- **`data.liveLinks` on the object vertex** — an integer count of the object's live links, the SECOND GC scalar alongside `linkEpoch` (both justified D5 exceptions). It is maintained **atomically in the same mutation batch as every link change** (`objects-base/ddls.go` `write_vertex`): AttachObject `+1` when a new/revived link lands (`+0` on an already-alive idempotent re-attach), DetachObject `−1`, the replace leg `−1` on the old object, revive sets it to this attach's delta. Because it is written in the link's own atomic batch and OCC-touches the vertex, concurrent attaches/detaches serialize through the vertex revision (no lost update) and the count never lags.
- **The `objectLiveness` lens decides on `liveLinks`:** `missing_owner = (o.data.liveLinks = 0)`. The `OPTIONAL MATCH (o)-[r]->(owner)` + `count(owner.key)` is retained **only** to collapse the link fan to one row per anchor and to drive the actorAggregate reprojection; every attach/detach also rewrites the object vertex, so the anchor reprojects from the vertex CDC regardless. `liveOwners` is no longer in the orphan decision.
- **Authoritative op-layer backstop:** `TombstoneObject` refuses (`Stale`) when the hydrated `liveLinks > 0` — it never reaps an object that atomically still has live links, independent of what the lens projected. This makes the data-loss invariant authoritative at the op layer (not merely lens-trusting), closes any future mis-dispatch, and makes the force-tombstone-a-live-object hazard impossible (so the revive path's reset-to-this-attach's-count can never undercount a live object back into reclaim).
- **§20 preserved:** `linkEpoch` is still bumped on every attach/detach and the `expectedEpoch` CAS is unchanged — the re-link race guard stays. The new backstop + the existing self-OCC are additionally redundant for that race.

### 21.2 The dead-target tradeoff (supersedes §20.1 C-b)

§20.1 C-b declared `count(owner) = 0` "the single most important `lens_cypher_test` assertion" — that a dangling link to a tombstoned owner reaps the object. **§21 reverses this.** Because `liveLinks` is decremented only by the object's OWN attach/detach, an owner-tombstone (which never touches the object) leaves a stale `liveLinks ≥ 1`, so a dead-target dangling link **no longer reaps the object here**. This is a **bounded, non-permanent byte LEAK, never data loss** — and strictly preferable to the §20 decision's data-loss bug. Authoritative dead-target reclamation belongs to the **deferred owner-tombstone-cascade trigger** (CC4 trigger-side, already "Andrew's GC domain" per §19) — which must decrement/recompute `liveLinks` (or otherwise drive a reclaim) when an owner dies. Until it lands, dead-target objects leak; the backlog tracks this. No frozen contract is touched (the lens output BodyColumns, the `TombstoneObject` op signature, and the Weaver `directOp` dispatch are all unchanged).

### 21.3 Tested

- `TestObjectLiveness_AttachLag_NotOrphaned` — the #1 regression guard: an object with `liveLinks = 1` and NO adjacency edge built (the lag reproduced directly) is NOT flagged orphaned. It FAILS against the pre-fix `(liveOwners = 0)` cypher (verified by reverting).
- `TestObjectLiveness_DeadTargetOwner_LeakedNotReaped` — pins the §21.2 tradeoff (dead-target → leak, not reaped here).
- `TestObject_ReplaceLeg_DecrementsOldObject` — the replace-leg counter accounting (old object → liveLinks 0, new → 1).
- `TestObject_Lifecycle` — liveLinks 1→2 (dedup) →1 (detach) →0 (last detach); the `liveLinks > 0` backstop refuses to reap a still-linked object; tombstone of the orphan proceeds.
- `TestObject_TombstoneEpochCAS_AbortsOnRelink` — restructured to orphan-before-tombstone so the §20 epoch-CAS demonstration coexists with the §21 backstop; the "proceeds" leg reaps a genuine `liveLinks = 0` orphan.
- The full Loop A+B convergence e2e (`make test-object-gc`) still converges (the orphan has `liveLinks = 0` → backstop satisfied → reaped → bytes reclaimed).

---

## 22. v1b GC — owner-tombstone-cascade trigger (2026-06-26; 📐 PROPOSAL — awaiting Andrew ratification)

> **Status: design proposal, not yet built.** Resolves the deferred dead-target byte LEAK that §21.2
> left open ("Authoritative dead-target reclamation belongs to the deferred owner-tombstone-cascade
> trigger … already 'Andrew's GC domain' per §19"). It is the trigger-side of CC4 (§18). The mechanism
> below touches **no frozen contract**, but it adds a **fourth kernel-seeded service actor** (a graph-
> mutating authority) — a trust-surface expansion that is Andrew's to ratify. Everything else is an
> additive, revertible component change following the Bridge-actor precedent exactly.

### 22.1 The leak, precisely (recap + grounding)

After §21, the `objectLiveness` lens decides orphan-ness from the object vertex's **atomic
`data.liveLinks` counter** (`packages/objects-base/ddls.go` `write_vertex` / `cur_live_links`), not the
lagging adjacency projection — which closed the §21 attach-lag **data-loss** race. The cost (§21.2): an
owner-tombstone never touches the object (it only writes the owner's own root), so `liveLinks` stays
stale `≥ 1` and **nothing ever reaps the object**:

- The `objectLiveness` lens sees `liveLinks ≥ 1` → not orphaned → Weaver never dispatches
  `TombstoneObject`.
- The `object-store-manager` reconcile (`internal/objectmanager/manager.go` `referencedByLiveVertex`)
  spares any object whose **vertex** is live and names the storeName — and the object vertex *is* live
  (stale `liveLinks`), so its bytes are spared **forever**.

Net: an owner vertex tombstoned with an object still attached → the object vertex stays live + its bytes
stay resident, indefinitely. Bounded (only attached-then-orphaned-by-owner-death objects), non-permanent
in principle, never data loss — but a genuine reliability leak. Pinned today by
`TestObjectLiveness_DeadTargetOwner_LeakedNotReaped` (§21.5), which this section's work will flip.

**Why no existing mechanism closes it.** The fix requires a **graph mutation** — the dangling link
(`lnk.object.<oid>.<linkName>.<deadOwnerType>.<deadOwnerId>`, still `isDeleted:false` per CC4: vertex
tombstone does not cascade to links) must be tombstoned so `liveLinks` decrements. The byte-janitor is
deliberately op-free ("It submits NO ops … so it needs no actor key", `cmd/object-store-manager/main.go`),
and the lens cannot see owner-death without an adjacency/owner read that reintroduces the §21 lag hazard
(§21: "no adjacency-derived signal separates" attach-lag from dead-target). So the trigger must read
owner-liveness **authoritatively** (the owner's own core-kv tombstone) and **drive an op**.

### 22.2 Mechanism options

| Option | Sketch | Verdict |
|---|---|---|
| **A — core-kv vertex-tombstone consumer → `DetachObject` (RECOMMENDED)** | A durable consumer on the core-kv KV-stream subject space detects a vertex transitioning to `isDeleted`; for each dead owner it enumerates the live object→owner links and submits `DetachObject` per link. The existing Loop A+B (objectLiveness `liveLinks=0` → Weaver `directOp(TombstoneObject)` → manager byte-reclaim) does the rest. | **Chosen.** Reacts to the *authoritative* state change (no projection lag → no §21-class hazard). Adds **zero** new reap path — only detaches; reuses every existing guard (epoch-CAS §20, `liveLinks>0` op backstop §21). Mirrors the established CC4 precedent (identity-hygiene merge enumerates a secondary identity's incident links and tombstones each — `packages/identity-hygiene/{lenses,ddls}.go`), generalized type-agnostically. |
| B — lens + Weaver convergence (dead-target lens column) | Project "owner is dead" on the object row, gap → `directOp(DetachObject/TombstoneObject)`. | **Rejected — reintroduces §21.** "Owner is dead" can only be read from the lagging adjacency/owner projection; a not-yet-projected fresh attach is indistinguishable from a dead owner (§21). The whole point of §21 was to stop trusting a lagging projection for an irreversible decision. |
| C — self-re-arming per-object heartbeat | Each object periodically re-checks its owners' liveness. | **Rejected — polling.** O(objects) timer churn on the temporal lane for a rare event; no authoritative trigger; latency/cost both worse than A. |

### 22.3 Recommended design (Option A) — detail

**Component home.** Extend `object-store-manager` (`internal/objectmanager`) with a second durable
consumer — the **cascade**. It already owns the off-graph GC's Loop B and the reconcile; the cascade is
the on-graph trigger that *feeds* Loop A for the dead-owner case. One always-on GC component, two loops
+ a trigger, is cleaner than a new binary.

**1. Trigger — authoritative owner-tombstone detection.**
- A durable consumer over the core-kv KV stream (`subjects.CoreKVStream(coreKVBucket)` = `KV_core-kv`) —
  the same stream the Refractor's CDC consumer reads (`internal/refractor/consumer/bootstrap.go` uses
  `subjects.CoreKVFilter(bucket)` = `$KV.<bucket>.>`). Core-kv keys map to KV-stream subjects
  (`$KV.<bucket>.<key>`, key dots → subject tokens — `substrate/batch.go:177`). Two grounded filter
  choices: the broad `$KV.core-kv.>` (verbatim Refractor parity, with in-handler root-narrowing), or the
  tighter **`$KV.core-kv.vtx.*.*`** — `*` matches exactly one token, so it selects the 3-segment vertex
  roots `vtx.<type>.<id>` and excludes 4-segment aspects (`vtx.T.id.aspect`) and `lnk.*` links (NanoIDs
  and type names carry no dots, so a root is always exactly 3 segments). Prefer the tighter filter to cut
  wake-up volume. `substrate.RunDurableConsumer` (`internal/substrate/consumer.go`) takes the
  `FilterSubject` directly.
- The handler strips the `$KV.<bucket>.` prefix to recover the vertex key (mirroring the Refractor's
  `subjectPrefx`), decodes the root doc, and acts **only** when `isDeleted == true` (a tombstone). A
  non-deleted update (create/revive/touch) is Ack'd and ignored. *(Optimization, not required for
  correctness: track the prior value to act only on the false→true transition; at-least-once redelivery of
  a tombstone is already idempotent — see step 3 — so re-processing a still-deleted root is harmless.)*
- The dead vertex's **own type is irrelevant** (type-agnostic, D7): we never learn or care what kind of
  owner it was. Object vertices themselves (`vtx.object.<id>`) appearing here are a no-op (objects are
  link *sources*, never targets — see §22.4 edge case).

**2. Enumerate the dangling links to the dead owner.**
- **New thin substrate helper to build:** `KVListKeysPrefix(ctx, bucket, prefix)` over the nats.go
  `ListKeysFiltered(ctx, prefix+">")` primitive (**verified present**, `nats.go@v1.52.0/jetstream/kv.go:188`;
  §18 CC1 noted it but no substrate wrapper exists today — only the heavy *unfiltered* `KVListKeys`,
  `internal/substrate/kv.go:189`). The cascade calls `KVListKeysPrefix(core-kv, "lnk.object.")` to list the
  object-link space — bounded by the count of *attached objects*, not the whole graph. *(Fallback if the
  wrapper is deferred: the existing full `KVListKeys` + a `strings.HasPrefix` filter — correct but heavier;
  prefer the wrapper.)* `ListKeysFiltered` uses `IgnoreDeletes` for NATS hard-delete markers, but link
  tombstones are **soft** (in-body `isDeleted:true`, still live KV entries — §18 CC1), so a soft-tombstoned
  link still appears in the list and is filtered by the per-key `KVGet` below.
- A link `lnk.object.<oid>.<linkName>.<tgtType>.<tgtId>` targets the dead owner iff its trailing
  `.<tgtType>.<tgtId>` equals the dead root's `<type>.<id>`. For each match the handler `KVGet`s the link
  and acts only on **live** links (`isDeleted == false`) — a stale match (already detached, or a dead
  owner's link from a prior cascade) is skipped.

**3. Act — submit `DetachObject` per live dangling link.**
- The cascade submits `DetachObject{oid, targetKey=deadOwnerKey, linkName}` to `ops.<lane>` in the
  Contract #2 §2.1 envelope shape (the same wire format `internal/weaver/actuator.go` publishes), under a
  new service actor (§22.5), with:
  - `requestId` = a **deterministic** NanoID derived from `(objectKey, linkKey, ownerTombstoneRevision)`
    (mirroring `deriveID`) so an at-least-once redelivery re-publishes the SAME requestId and collapses on
    the Contract #4 `vtx.op.<requestId>` tracker — no duplicate detach.
  - `contextHint.reads` = `[linkKey, objectVertexKey]` (the keys `DetachObject`'s DDL hydrates:
    `ddls.go detach_object` reads the link for liveness + the object vertex for the `liveLinks` decrement).
  - `authContext.target` = the object vertex key (the operator grant is scope:any, so any live target
    authorizes; the object vertex is the op's primaryKey subject).
- `DetachObject` (existing op) tombstones the link + decrements `liveLinks` + OCC-touches the object vertex
  → `objectLiveness` reprojects; when that was the object's **last** live link, `liveLinks` hits 0 → the
  unchanged Loop A dispatches `TombstoneObject` → Loop B reclaims the bytes. **The cascade adds no reap
  logic; it only detaches.**
- Idempotency / already-done: if the link is no longer live, `DetachObject` fails `UnknownLink`
  (`ddls.go:478`). The cascade treats a parse-confirmed `UnknownLink` reply as success (the link is
  already detached — the desired end state) and Acks. *(The deterministic-requestId tracker collapse makes
  a true redelivery a no-op before it even reaches the script; the `UnknownLink` path covers the case
  where a concurrent explicit `DetachObject` won the race.)*

**4. CC10 ordering (at-least-once).** The consumer submits the detach op(s) **before** acking the
tombstone trigger message — publish-then-ack, never ack-then-act — so a crash between leaves the trigger
re-deliverable. Combined with the deterministic requestId + `UnknownLink`-is-success, the cascade is
exactly-once in effect over an at-least-once transport.

### 22.4 Safety analysis

- **No §21-class data loss.** The §21 bug reaped bytes off a *lagging projection* misread. The cascade
  fires off the owner's *own authoritative core-kv tombstone* — the source of truth, zero lag. A fresh
  attach to a **live** owner is never in the dead set (the owner is alive → never delivered as a
  tombstone), so the attach-lag scenario cannot trigger a cascade detach. And even if it somehow did, the
  worst case is a wrongful *detach* (reversible — re-attachable), not a wrongful *byte delete*; the
  irreversible `TombstoneObject` is still gated by the §20 epoch-CAS + the §21 `liveLinks>0` op backstop,
  which the cascade does not weaken.
- **Owner-revival race.** A soft-tombstoned owner can be revived (`op:update`, no commit-path guard, CC2).
  If the cascade detached an object's link and the owner is then revived, the object is wrongly detached
  (and may subsequently be GC'd). This is the **identical** property identity-hygiene's merge already has
  (it tombstones incident links on merge; a revived secondary identity loses them) and is acceptable:
  owner-revival is rare, the detach is reversible by re-attach, and "the owner came back to life after we
  reacted to its death" is an inherent at-least-once-on-tombstone semantics. Documented, not closed.
- **Multi-owner object — only a last-link death reaps (the whole point of `liveLinks`).** An object
  linked to owners X (dead) and Y (alive): the cascade detaches only the O→X link → `liveLinks` 2→1 →
  `objectLiveness` still sees `liveLinks ≥ 1` → not orphaned → **not reaped** (Y's attachment is intact).
  Reclamation happens only when the cascade detaches the object's *last* live link. An object with several
  links to the *same* dead owner under different `linkName`s has each enumerated + detached independently;
  the per-detach OCC on the object vertex serializes the decrements. A concurrent re-attach of the same
  object to a *new* live owner races the detach through the same object-vertex revision (both OCC-touch it),
  so `liveLinks` nets correctly and the object is never wrongly reaped.
- **Object-as-target edge.** `AttachObject` forbids only a `meta` target (`ddls.go:379`), so an object
  *could* be linked to another object (`targetKey = vtx.object.<id2>`). If that owner-object is
  tombstoned, the cascade correctly detaches the inbound link. An object vertex tombstoned that owns no
  inbound object-links (the common case) yields an empty enumeration → no-op. No special-casing needed.
- **Protected / system vertices.** A `meta`/protected vertex is never an object owner (`AttachObject`
  rejects it at attach), so a `meta.*` tombstone (if it ever occurred) enumerates zero object-links →
  no-op. The cascade also never needs to read or mutate the dead owner itself.
- **Volume.** The trigger consumer wakes on every vertex-root mutation (high), but only decodes
  `isDeleted` and, for the rare tombstone, runs one bounded `lnk.object.>` enumeration. At demo/single-cell
  scale this is fine. **Scale follow-up (noted, not built):** a per-owner reverse index (owner → object
  links) would replace the `lnk.object.>` scan, and a tighter trigger (a `vertex.tombstoned` core-event,
  were one emitted — none exists today, §22 grounding) would replace the all-roots watch. Both are
  optimizations of a correct mechanism, not correctness gaps.

### 22.5 The kernel change (the Andrew ratification gate)

The cascade submits graph ops, so `object-store-manager` needs an **actor that holds the operator role**
(operator is already granted `DetachObject` — `packages/objects-base/permissions.go` — so **no permission
change is required**). This follows the Bridge service-actor precedent **exactly**:

- Add a primordial `object-store-manager` service identity to the bootstrap seed
  (`internal/bootstrap/nanoid.go`): a new `PrimordialIDsRaw` field + `ObjmgrIdentityID/Key`, a
  `ObjmgrHoldsRoleLinkKey = lnk.identity.<id>.holdsRole.role.<operator>`, both appended to
  `PrimordialVertexKeys()`, and `PrimordialVertexKeyCount` 29 → 31 (+1 identity, +1 holdsRole link).
- Bump the bootstrap file `Version` "10" → "11" and the version-history comment (arch §92 service-actor
  list: Loom + Weaver + Bridge + **object-store-manager**).
- `cmd/object-store-manager/main.go` resolves `bootstrap.ObjmgrIdentityKey` as its actor and passes it to
  the manager; `manager.go` gains the actor + an op-publish path (a thin `ops.<lane>` publish, copied from
  the actuator shape — the manager must not import `internal/weaver`).

**Why this is the ratification point.** It expands the kernel's set of root-equivalent, graph-mutating
service actors from three to four. The *mechanics* are a verbatim Bridge-template addition (additive,
revertible, no frozen contract), but the *decision* — "the GC byte-janitor should also be able to mutate
the graph (detach links)" — is a trust-surface call in Andrew's GC domain. The alternative (a separate
cascade binary with its own actor) has the same trust footprint with more moving parts; folding it into
the existing always-on GC component is the lean choice. **Recommendation: ratify the fold-in.**

### 22.6 Test plan (on build)

- **Unit (`internal/objectmanager`):** the cascade handler (a) on a dead-owner root with N live object→owner
  links, submits N `DetachObject` with the right `oid/targetKey/linkName` + deterministic requestId; (b)
  skips non-`isDeleted` roots; (c) skips already-dead/non-matching links; (d) is a no-op for an object-root
  tombstone with no inbound object-links; (e) redelivery re-submits the same requestId (idempotent).
- **Heavy e2e (`make test-object-gc`, `internal/objectgc`, `-tags objectgc`):** extend with a
  dead-owner→cascade→reclaim leg — attach an object to a throwaway owner, tombstone the **owner**, assert
  the cascade detaches the link → `objectLiveness` projects `liveLinks=0` → Weaver `directOp(TombstoneObject)`
  → manager reclaims the bytes. This closes the trigger→Loop-A→Loop-B seam the unit tests cannot.
- **Flip `TestObjectLiveness_DeadTargetOwner_LeakedNotReaped` (§21.5):** the leak it pins is now closed via
  the cascade, so it becomes a cascade-reclaim assertion (the lens still leaks *in isolation* — the cascade
  is what closes the loop — so the lens-only test may stay as-is with a comment, and the e2e proves the
  end-to-end reclaim; decide on build).
- **Substrate (`internal/substrate`):** a `TestKVListKeysPrefix` proving the new wrapper returns only the
  prefixed keys (incl. soft-tombstoned link keys, which must appear so the per-key `KVGet` can filter them).
- **Gates:** `make verify-package-objects-base`, STRICT-P5/conventions, golangci, plus the bootstrap-seed
  count/version tests (which move with the +2 entries) and verify-kernel surfaces.

### 22.7 Status / ownership

- **Andrew gate:** the §22.5 kernel service-actor addition (ratify the fold-in vs. a separate binary).
- **Everything else is L2 (Winston):** the cascade consumer, the deterministic-requestId op submit, the
  tests, and the board flip — built in a worktree, 3-layer-reviewed, gates green, merged on ratification.
- **No frozen contract is touched** (§21.2 holds: lens BodyColumns, `TombstoneObject`/`DetachObject`
  signatures, and the Weaver `directOp` dispatch are all unchanged; the new actor is additive seed).
