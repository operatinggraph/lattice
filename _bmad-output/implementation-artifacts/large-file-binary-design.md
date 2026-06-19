# Large-file / binary handling — design draft

**Status:** design-draft (for Andrew's review → then build with the normal review loop)
**Backlog item:** Large-file / binary handling (one of Andrew's picked "Now" items). See `backlog.md`.
**Why now:** an experience-business view/control app shows profile photos and lease PDFs; this is the natural companion to **Loupe** (the upload/view client) and exercises the off-graph blob plane. Carries the former architecture **OI-2**.

## The constraint (binding, from the architecture)
Blobs (photos, scanned docs, PDFs) **must not flow through the Refractor** — the CDC / projection plane is for graph *state*, not binary payloads (a Lens cannot and must not stream a PDF). Yet clients need first-class upload/download. So: **the graph holds a pointer + metadata; an object store holds the bytes; the Refractor projects only the reference, never the blob.**

## Substrate — NATS Object Store
Use the **NATS Object Store** (chunked, content-addressed; native to the existing NATS deployment — no new infrastructure). Add a thin substrate wrapper (mirrors the KV helpers), e.g. `internal/substrate/object.go`:
- `ObjectPut(ctx, bucket, key, reader) (digest, size, error)` — streams chunks; returns the sha256 digest + size.
- `ObjectGet(ctx, bucket, key) (reader, info, error)` — streams bytes back.
- `ObjectDelete(ctx, bucket, key) error`.
- One primordial bucket `core-objects` (provisioned at bootstrap alongside the KV buckets), `Storage=file`.
Keep it the minimal "architecturally-common" surface (substrate doctrine); the Processor / Refractor never touch it.

## Graph linkage — the pointer aspect
A blob is referenced from the graph by an **aspect** on its owning vertex (D5: business data in aspects, not the vertex root). Shape:
```
vtx.<type>.<id>.<localName>   e.g. vtx.identity.<id>.photo, vtx.leaseapp.<id>.signedLease
data: { objectKey, digest (sha256), contentType, size, filename, uploadedAt }
```
Content-addressed `digest` gives integrity + natural dedup. The Refractor projects this aspect's **reference fields** (objectKey/digest/contentType/size/filename) like any other aspect — never the bytes.

## Write path — upload-then-attach (Processor stays sole graph writer; blobs stay off CDC)
1. **Loupe uploads the bytes** to `core-objects` via `substrate.ObjectPut` → gets `{objectKey, digest, size}`. (Trusted client writes the store directly — the bytes never go through an op or the CDC plane.)
2. **Loupe submits an `AttachObject` op** (new, package DDL) whose Starlark writes the **pointer aspect** `{objectKey, digest, contentType, size, filename, uploadedAt}` on the owning vertex — through the normal Processor write path → Core KV → CDC → Refractor projects the reference.
- **Ordering:** bytes first (content-addressed), then the pointer op. A failed op leaves an orphan blob (GC'd later, dedup-friendly); a failed upload writes no pointer. No partial graph state.

## Read path
Loupe reads the pointer aspect (Core KV or a lens), then `substrate.ObjectGet(core-objects, objectKey)` and streams the bytes to its browser over HTTP. The Refractor is never in the byte path.

## Trusted-tool simplification (v1, with Loupe) — what makes this tractable NOW
Under Loupe's **trusted single-identity** model (no Gateway, no per-user authz — the agreed Loupe scope):
- **Transport** is just Loupe's own HTTP: `POST /api/objects` (multipart upload → ObjectPut → AttachObject op) and `GET /api/objects/<key>` (ObjectGet → stream). No Gateway, no signed URLs.
- **Authorization** is "trusted admin" — same posture as every other Loupe op. No per-user blob-access binding.
This is the whole reason large-file is a "Now" item rather than gated on the Gateway: the trusted model removes the hard parts.

## Lifecycle / GC
- **Orphan GC:** a sweep (or a Refractor-driven reconciler) deletes `core-objects` keys with no referencing pointer aspect (content-addressed digests make this safe — delete only when no live aspect references the digest). Run conservatively (warmup + grace window), mirroring Weaver's sweep discipline.
- **Tombstone cascade:** when an owning vertex is tombstoned, its pointer aspect tombstones too (existing cascade); the blob becomes orphan → GC.
- **Limits:** a max object size (config, e.g. 25 MB) enforced at `ObjectPut`; a content-type allow-list optional.

## Loupe integration (the demo)
A "Files" affordance: attach a profile photo to an `identity`, a signed lease PDF to a `leaseapp` (the Loftspace vertical). View/download inline. This makes Loupe visibly useful and dogfoods the blob plane end-to-end.

## Non-goals (stay Phase 3+)
- The **untrusted / multi-user** path: Gateway-mediated upload/download, signed-URL grants, per-user blob authorization bound to the owning vertex's capability grants. (Needs read-path auth + Gateway — both deferred.)
- Direct browser↔Object-Store handles (Loupe proxies the bytes for v1).
- Image transforms / thumbnails / virus scanning.

## Build plan + verification (when greenlit)
1. Substrate `object.go` + bootstrap provisioning of `core-objects` (+ verify-kernel enumeration if it's a primordial bucket — version bump, both enumerations in lockstep).
2. The `AttachObject` op DDL + the pointer-aspect shape (package data; or a small `objects-base` package).
3. Loupe `POST /api/objects` + `GET /api/objects/<key>` + a Files tab.
4. Orphan-GC sweep (can be a follow-on once the attach/read path works).
- Gates: build/vet/lint; substrate object round-trip test; an end-to-end upload→attach→read test; verify-kernel (if bucket added); the FR-style "blob never enters the Refractor" assertion (a Refractor projection of the pointer aspect never carries bytes).

## Open questions for Andrew
1. **Primordial bucket vs. package-provisioned?** `core-objects` as a primordial substrate bucket (bootstrap version bump) is cleanest; alternatively an `objects-base` package provisions it. Lean: primordial (it's substrate-level infra, like the KV buckets).
2. **One shared object bucket vs. per-type/per-cell?** Single `core-objects` for the single-cell MVP; cells/sharding is Phase 3.
3. **Max size + content-type policy** defaults.
4. **GC trigger:** a periodic sweep vs. a Refractor reconciler over pointer aspects — both viable; the sweep is simpler for v1.
