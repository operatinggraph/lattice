# Adjacency under the 1 MiB ceiling — and the multi-subject direct-get primitive

**Status: 📐 awaiting-Andrew (ratification — now a three-shape fork, §14)** · Designer fire
2026-08-09 (Winston); **redirected by Andrew 2026-08-09** after the multi-subject direct-get finding,
re-grounded with live measurements 2026-08-10 (§14). Board row:
`[Refractor] A node's whole adjacency list is one KV value, so a high-in-degree node cannot be indexed`
(★★★, M). Adversarial pass on the original per-edge shape: **run and folded** (§13); its
shape-independent findings carry into every candidate (§14.5).

## For Andrew

Two decisions live here now, one settled and one yours.

**Settled (you directed it; filed on the board): the multi-subject direct-get primitive.** One
request to `$JS.API.DIRECT.GET.KV_<bucket>` with `multi_last` returns last-value-per-subject for an
explicit key list *or* subject filters, computed and streamed **atomically under the stream read
lock**, no consumer involved, ≤1,024 subjects per request (413 above — hard, unpageable). Measured
on this host (§14.1): **~31 µs/key amortized at 10–100 keys vs 153 µs per sequential `kv.Get`
(~5×)**, and **4× faster than the ephemeral-consumer lister on the identical 180-key result set**.
First consumers: **step-4 hydration** (the exact-key read-set becomes one atomic round trip —
`step4_hydrate.go`'s own comment documents today's Get-straddle it removes) and the
**`ListKeysPrefix`-class enumerations** (~12 non-test files: pkgmgr censuses, Loupe browse, vertical
apps' P5 list endpoints, the full engine's whole-type scans). Filed as its own board row; ADR-31 is
the vendor authority and is silent on numbers, so the spike above is the record.

**Yours: what happens to the adjacency index.** Three shapes, §14.4 has the full matrix:

- **A — delete the index**: serve neighbors from Core KV directly (`lnk.*.<id>.>` outbound,
  `lnk.*.*.*.*.<id>` inbound; bodies come back, so soft-tombstone filtering is free). Removes the
  bucket, Bootstrapper, three writers, pre-apply, Ready gate. Measured cost: the inbound
  trailing-wildcard walk is ~0.4 ms over today's ~10 K link subjects and **scales with total links,
  not degree** (~35 µs/1K subjects), runs under the core-kv read lock against the Processor's write
  path, and both live hubs already 413 into a consumer fallback that costs ~100 ms-class at hub
  degree. The ActorEnumerator BFS multiplies that per visited node. Leanest; correct today; not the
  scale path.
- **B — keep the document, mark the hubs (my recommendation)**: docs unchanged for 99.9 % of nodes
  (one 153 µs get, zero migration); when a doc would cross a degree threshold (~1,000 edges),
  `Build` replaces it with a small **overflow mark** and stops absorbing that node's edges; a marked
  node's reads fall back to Core-KV enumeration — typed hops via `lnk.*.*.<rel>.*.<id>` usually
  come back under 1,024 and ride multi_last (~0.6 ms); untyped/BFS reads pay the consumer path.
  The jammed hub self-heals on first post-deploy touch. Smallest diff, no migration, and "fallback
  to ephemeral for the rare hubs" — your bottom line — is the whole change.
- **C — per-edge rekey** (the fully-specified §3–§6 body): uniform O(degree) prefix reads,
  strongest at scale, but the most machinery — v2-durable migration, TTL'd markers, the
  stability-verified fallback. Keep on the shelf as the scale successor unless you want it now.

**No frozen-contract change in any shape.** The bucket is Refractor-private operational state
(lattice-architecture P1; Contract #2 §kv.Links). Shape B needs no provisioning or permission edits
at all; C needs the `PerKeyTTL` registry row; A needs neither but deletes a component.

---

## 1. Problem and live demand

### 1.1 The mechanism

Every Contract #1 link CDC event is bridged into **two directional entries** — outbound under the
source node, inbound under the target node — by three writers that all call `adjacency.Build`:

- the dedicated adjacency consumer ([consumer/bootstrap.go:222-228](../../internal/refractor/consumer/bootstrap.go)),
- the actor-aware pipeline's link fan-out pre-apply ([pipeline/evaluate.go:828-832](../../internal/refractor/pipeline/evaluate.go)),
- the plain pipeline's link-reprojection pre-apply ([pipeline/pipeline.go:2494-2505](../../internal/refractor/pipeline/pipeline.go)).

`Build` upserts into a **single JSON document per node** — `AdjValue{Edges []EdgeEntry}` at
`adj.<nodeId>` — via a Get → unmarshal → append/remove → marshal → CAS loop
([adjacency/builder.go:89-139](../../internal/refractor/adjacency/builder.go)). Each entry is ~268
bytes marshaled. The document therefore grows linearly with the node's **total degree**, and the
write path re-reads and re-writes the whole document on every edge change.

NATS rejects any publish above the connection's negotiated `max_payload` — 1,048,576 bytes by default,
no override in `deploy/nats-server.conf` — client-side, before the wire
(`nats.go@v1.52.0/nats.go:4463`, `ErrMaxPayload` = `nats: maximum payload exceeded`). Nothing in the
KV bucket's own config binds first (`Maximum Value Size: unlimited`). So at ~3,900 edges the
document jams: every subsequent `Build` for that node fails with the same permanent error.

### 1.2 What the jam does (observed on this host, 2026-08-09)

- `adj.pFf8PviwpWugC6kepFf8` (the `leaseServiceInstance` meta) is **1,048,427 bytes, 3,912 edges,
  every one `(instanceOf, inbound)`** — pinned at the ceiling. Core KV holds 3,938 `vtx.service.*`
  roots and 3,919 `instanceOf` links targeting this one meta; each new service-instance registration
  adds one more link that can never be indexed.
- The dedicated consumer returns `substrate.Nak` on a `Build` error
  ([consumer/bootstrap.go:223-227](../../internal/refractor/consumer/bootstrap.go)), and its config
  leaves `MaxDeliver` unbounded with plain `Nak` = **immediate redelivery**
  (`internal/substrate/consumer.go:162-173`, `:307-330`). The live durable shows the cost: consumer
  sequence **1,675,481** against stream sequence **118,330** (≈14× lifetime delivery amplification),
  ack floor pinned 145 messages behind head, 7 messages cycling right now. Every redelivery re-reads
  the ~1 MiB document, re-marshals, and re-fails.
- Both pipeline pre-apply sites surface the same error into evaluation dispositions, so **every lens
  whose patterns react to `instanceOf` fails its evaluation on every such event** — the board row's
  "link fan-out fails for every lens traversing it".
- The quiet harm: `Neighbors` still *succeeds* on the jammed node, returning whichever 3,912 edges
  fit. Every walk through the hub computes on a silently frozen edge set — converged-but-wrong rows
  with no error on the read side.
- Rebuild is dead: a from-scratch replay (durable loss, `make down`) re-runs the same quadratic
  read-modify-write ladder — Σ(1..3912) × 268 B ≈ **2 GB of KV writes for this one document** — and
  then jams at the same edge and loops. The index cannot currently be rebuilt on this host.

### 1.3 The mechanism generalizes — this is not a service-registration quirk

Contract #1 §1.1 makes the **later-arriving vertex the source**, so popular, long-lived vertices
accumulate **inbound** links without bound while out-degree stays authorship-bounded (measured: max
out-degree on this host is 6). Ranking live in-degree:

| Hub | In-degree | Document | Driver |
|---|---|---|---|
| `vtx.meta.pFf8PviwpWugC6kepFf8` (leaseServiceInstance) | 3,919 | **1,048,427 B — jammed** | service registration |
| `vtx.identity.edu97ixj2CJB6auNi6L4` | 2,335 | **644,992 B — 61.5%** | 2,327 × `providedTo` — ordinary business activity |
| `vtx.identity.dzst9ZB6Q8Jhw4m9hHVG` | 601 | ~160 KB | business activity |
| `vtx.role.niR13mqp2r15q1iEge4P` | 180 | ~48 KB | `holdsRole`/`grantedBy` — the capability plane |

The next victim is an **identity** — the type the capability projection anchors on and the
ActorEnumerator BFS walks. Pruning service instances (the op-vertex-pruner backlog item) would reset
the first row and do nothing for the second; the fix has to remove the ceiling, not the instance.

Sibling census (is any other platform KV value an unbounded per-hub aggregate?): capability documents
are per-**actor** (`cap.roles.identity.<actorId>`, 233-355 B — bounded by that actor's own grants);
orchestration-history rows are per-event (279 B); the Personal Lens interest registry is already
**per-pair keyed** (`<identityId>.<deviceId>`, 61 B). The adjacency document is the platform's one
unbounded-aggregate value.

## 2. Grounding — what exists and what must survive

### 2.1 The package seam

`internal/refractor/adjacency` is 194 lines with exactly two entry points:

- `Build(ctx, kv, CoreKVEvent) error` — three writers (§1.1). Idempotent by EdgeID; the pipelines'
  pre-apply **before** enumerate/evaluate is a load-bearing ordering guarantee ("the reprojection
  never races ahead of the edge that triggered it", [evaluate.go:800-808](../../internal/refractor/pipeline/evaluate.go)).
- `Neighbors(ctx, kv, nodeID) ([]EdgeEntry, uint64, error)` — four readers: the full engine's
  memoized `fetchEdges` ([full/executor.go:903-916](../../internal/refractor/ruleengine/full/executor.go)),
  the anchor-derivation walk (read-capped, [pipeline/anchor_derivation.go:189-203](../../internal/refractor/pipeline/anchor_derivation.go)),
  the ActorEnumerator BFS ([pipeline/actor_enumerator.go:148,167](../../internal/refractor/pipeline/actor_enumerator.go)),
  and the footprint validator ([pipeline/evaluate.go:407,416](../../internal/refractor/pipeline/evaluate.go)).
  The `uint64` is the document's KV revision; derivation and BFS discard it.

### 2.2 The revision's one real consumer: §13.4 evaluation footprints

`EvalFootprint.EdgeRevisions` records, per adjacency node, the revision `Neighbors` returned;
validation re-reads and compares **for equality only** — typed hops instead re-apply their recorded
`(relType, direction)` selector to a fresh read and compare matched edge-ID *sets*
([evaluate.go:383-437](../../internal/refractor/pipeline/evaluate.go), `ruleengine.go:87-131`).
The whole-document revision is the comparison only for **untyped/fallback** hops. Nothing anywhere
does arithmetic on the value or persists it — the footprint lives and dies inside one evaluation
(`branchmerge.go`, `evaluate.go:327`). So any **monotonic-per-change, equality-comparable
fingerprint** can stand in for the document revision without touching the mechanism (with one
timing precondition, §3.3).

### 2.3 Everything else that touches the storage shape

From a repo-wide census: **61 test files** touch adjacency, of which all but three go through
`Build`/`Neighbors` (24 `packages/*/lens_cypher_test.go` fixtures, the pipeline/executor/e2e
families) and survive a storage rekey untouched. Exactly three parse the raw document JSON and must
migrate: `refractor_capability_multi_e2e_test.go:604` (hand-rolled `adjacencyNeighborsLocal`),
`refractor_capability_reproject_e2e_test.go:162` and `refractor_capability_sweep_e2e_test.go:163`
(poll raw `Get` + `bytes.Contains`). No non-test code outside the package reads `adj.*` keys
directly. The footprint-reduction campaign already deleted the only watcher on the bucket
(refractor-footprint-reduction-design.md — the per-pipeline `runAdjWatch`, −95 consumers); nothing
watches it today. Loupe never reads it (platform-private bucket; `pkgmgr` bucketguard +
`natsperm` enforce that only the `refractor` identity writes it — both keyed on the bucket *name*,
untouched here).

### 2.4 Bucket, consumer, and permission facts

`refractor-adjacency`: file-backed, history 1, no TTL, no size caps, `Direct Get: true` (live stream
info; provisioned via `internal/bootstrap/platform_buckets.go:84-87`). The dedicated durable
`refractor-adjacency` on `KV_core-kv`: DeliverAll, AckExplicit, AckWait 30 s, MaxAckPending 1,000,
MaxDeliver unlimited. Boot gate: rule consumers wait for the Bootstrapper's `Ready()` (pending = 0 —
the inherited materializer ADR-7/8 protocol).

**Permissions (adversarial-pass finding, verified):** the natsperm matrix denies the JetStream
stream-admin verbs — `STREAM.PURGE` included — on every platform bucket's backing stream to every
non-bootstrap component **including the bucket's owner** (`internal/natsperm/matrix.go:63-71` +
the registry loop at `:115-127`; rendered at `deploy/nats-server.conf:60`; conformance-tested by
`internal/natsperm/conf_test.go:985-1010`). Refractor may **publish** anything under
`$KV.refractor-adjacency.>` and may use `DIRECT.GET`, consumer create/delete, and INFO — but can
never call stream purge. Every mechanism in this design is chosen to fit inside that envelope; the
one thing per-key publishes cannot do is make a *subject* vanish, which is what the TTL'd markers
in §3.2/§6.5 are for.

## 3. Shape C — one key per directional edge (full spec; no longer the standing recommendation, see §14)

§3–§13 are the complete, adversarially-reviewed specification of the per-edge rekey. They remain the
build plan **if** Andrew picks Shape C; §14 holds the measured three-way comparison and the current
recommendation (Shape B).

### 3.1 Key and value

One KV key per directional edge, in the same bucket:

```
adj.<nodeId>.<relName>.<dir>.<otherId>        dir ∈ {out, in}
```

- `nodeId`, `otherId` — bare NanoIDs (the index stays keyed by bare NodeID; that choice is deliberate
  and unchanged — pattern labels resolve through `EdgeEntry.OtherType`, see
  [builder.go:14-22](../../internal/refractor/adjacency/builder.go)).
- `dir` — the storage encoding of `EdgeEntry.Direction`: `outbound` → `out`, `inbound` → `in`, fixed
  two-way mapping; the persisted `EdgeEntry.Direction` strings and `DirectionMatches` vocabulary are
  unchanged.
- `relName` — the link's relation token. Every segment originates from a Contract #1 Core KV subject,
  so all are inside the KV key charset by construction (`nats.go@v1.52.0/kv.go:369`,
  `^[-/_=\.a-zA-Z0-9]+$`); total key length ~90 chars, far under subject limits. That regex accepts
  *empty-adjacent* dots, so the builder validates each segment **non-empty + charset** before keying
  (§6.2) — a malformed segment is a `Term`, never a key.
- **Uniqueness:** a Contract #1 link key is `lnk.<tA>.<idA>.<rel>.<tB>.<idB>` and NanoIDs are
  vertex-unique, so `(nodeId, rel, dir, otherId)` identifies exactly one directional entry of exactly
  one link.
- **Value: the existing `EdgeEntry` JSON, unchanged** (~280 B). Not just convenience: the **ratified**
  relationship-data design (relationship-data-projection-design.md, ✅ 2026-08-06) binds `type(r)` and
  `r.key` from the `EdgeEntry.Name`/`CoreKvKey` the walk holds in-hand and resolves `r.data.<field>`
  by a point-read on that `CoreKvKey` — and `CoreKvKey` is *not* reconstructable from this key's
  segments (the node's own type is deliberately absent). The value carries it. (Key-only encoding
  with empty values was considered and rejected — §10.6.)
- Relation-first token order is what makes Increment 2's typed push-down (`adj.<node>.<rel>.>`)
  possible.

`subjects.AdjKey` is replaced by `subjects.AdjEdgeKey(nodeID, rel, dir, otherID)` and
`subjects.AdjPrefix(nodeID)` (= `"adj." + nodeID + "."`).

### 3.2 Write path — `Build` keeps its signature, loses the CAS loop

```
create/upsert:  kv.Put(AdjEdgeKey(...), marshal(EdgeEntry))          — unconditional, ~300 B
remove:         TTL'd per-key purge — publish to the key's subject with
                Nats-Rollup: sub + KV-Operation: PURGE + Nats-TTL: 24h
```

All four key segments are present in every `CoreKVEvent` the three writers already construct
(`NodeID`, `Name`, `Direction`, `OtherNodeID`), for the link-envelope path and the legacy body path
alike. Properties:

- **No read-modify-write, no CAS retry, no per-node serialization.** Concurrent writers of the same
  edge write byte-identical values (every `EdgeEntry` field derives from the link key + side), so
  last-writer-wins is value-neutral. The CAS-with-retry contention documented on hot shared hubs
  dissolves structurally.
- **Write cost is O(1) in degree**; the `max_payload` failure class is gone (the value is a constant
  ~300 B).
- **Removal is a publish, not an admin call** — per-key KV purge is implemented as a rollup publish
  (`nats.go@v1.52.0/jetstream/kv.go:1150-1156` sets `Nats-Rollup: sub` on an ordinary message;
  `Purge` = `Delete(..., purge())`), so it rides Refractor's `$KV.refractor-adjacency.>` grant.
  The rollup replaces the key's message with a single purge **marker**; the `Nats-TTL` header
  (per-message TTL, ADR-48, in our 2.14 pin; requires `AllowMsgTTL` on the stream — the §6.6
  provisioning edit) makes the marker itself expire, after which the *subject* is fully gone —
  no janitor, no `PurgeDeletes` sweep, no stream-purge permission. While the marker lives, snapshot
  reads see it (excluded from live entries, sequence folded into the fingerprint) — exactly the
  tombstone-visibility the footprint window needs.
- The pre-apply ordering guarantee survives verbatim: pre-apply still writes the edge before
  enumeration reads it (read-your-write on a single key), and idempotence-by-derived-key replaces
  idempotence-by-EdgeID with identical outcomes. Removal of a never-indexed edge publishes a marker
  that quietly expires (today it is a document no-op) — equivalent.
- `Build` classifies its errors: a key the server rejects (`substrate.IsInvalidKeyError`,
  `errors.go:58-67` — "can never succeed on retry") or a marshal failure is **permanent** and the
  callers `Term`; only transport errors remain `Nak`. This closes the class the adversarial pass
  flagged: without it, one malformed legacy event would re-create the §1.2 unbounded-immediate-Nak
  livelock with a different trigger.

### 3.3 Read path — `Neighbors` keeps its signature

```
Neighbors(ctx, kv, nodeID) ([]EdgeEntry, uint64, error)
```

enumerates `adj.<nodeId>.>` via a new substrate primitive (§6.3), unmarshals each live value
(skipping DEL/PURGE-marker entries), and returns the entries plus a **fingerprint**: the maximum
stream sequence over every last-per-subject message under the prefix, markers included.

- It moves on every edge add (new message, higher sequence) and every edge remove (marker, higher
  sequence) — the "did this node's edge set change" signal `footprintValid` compares. KV revisions
  *are* stream sequences, so both read paths yield the same quantity.
- It is equality-compared only (§2.2), so `evaluate.go:407`, `executor.go:913`, and the selector
  set-compare arm need **zero code changes**.
- **Timing precondition, stated honestly (adversarial-pass finding §13):** removing a *marker*
  (TTL expiry here; a stream purge by bootstrap/an operator in general) can *lower* the fingerprint,
  and a lowered fingerprint can in principle land back on a previously captured value and mask a
  drift. The algebra alone does not exclude it; the **timing** does: footprints live for one
  evaluation-plus-validation (sub-second to seconds, bounded by the pipeline handler's 30 s
  AckWait), while the marker TTL is 24 h — a ≥3-order-of-magnitude margin. The precondition is
  pinned in the marker-TTL constant's doc comment; a manual operator `nats kv purge` during a live
  evaluation is outside the model (it always was — today it would also yank the document from under
  a walk).
- Absent node: empty entries, fingerprint 0 — same absent semantics as today's rev 0. A node whose
  markers have all expired also reads 0 — indistinguishable from absent, which is correct (nothing
  is retained).

## 4. Convergence and edge-case table

| State | Today (whole doc) | Per-edge | Verdict |
|---|---|---|---|
| First create of an edge | CAS create/append | `Put` creates key | same |
| Redelivered create (dup event) | upsert replaces identical entry, revision bumps | `Put` rewrites identical value, sequence bumps | same (incl. spurious footprint move) |
| Tombstone (`isDeleted` link body) | entry removed from doc | TTL'd purge marker → subject expires | same visible semantics; better hygiene |
| Tombstone of never-indexed edge | remove is a no-op | marker created, expires in 24 h | equivalent |
| Create-after-tombstone (link re-created) | entry re-appended | `Put` over marker resurrects key | same |
| Hard-DEL of the link key (empty CDC body) | skipped — entry lingers ([bootstrap.go:132-135](../../internal/refractor/consumer/bootstrap.go)) | skipped — key lingers | unchanged; accepted by the hard-delete design. Per-edge makes the eventual fix cheap (the subject alone names both adjacency keys) — noted, not in scope |
| Crash between the pair's two writes | half-indexed until the durable redelivers (un-acked) → self-heals | identical | same |
| Lagging pipeline pre-applies stale event M after the dedicated consumer applied final N | stale entry state until *that pipeline* reaches N (same reactsTo filter) and rewrites; residual only if the pipeline dies first | identical interleaving, identical self-heal, identical residual | no regression (documented, pre-existing) |
| Self-loop (src = dst) | the two directional events share an EdgeID, so the **second overwrites the first** — a hop filtering the other direction finds nothing | two keys (`…out.<id>`, `…in.<id>`) — both directions traversable | **semantic fix**; live data holds at least one self-link. One honest caveat: the §13.4 footprint's identity unit is still `EdgeID`, so a self-loop's two entries collapse to one set member and a half-tombstoned pair is invisible to the *selector* arm (narrow, self-healing; the identity unit becomes `EdgeID+dir` in Increment 2's selector work) |
| Legacy body-path event (`CoreKVEvent` JSON with arbitrary fields) | keyed by EdgeID in the doc; junk tolerated | all four derived segments validated non-empty + charset at the event boundary → **`Term`** on failure (never a panic, never a Nak-loop); EdgeID rides in the value | Two distinct legacy EdgeIDs sharing `(node,rel,dir,other)` would coalesce — impossible for link-derived events; nothing in the tree produces legacy bodies (grep: no `"nodeId"`-bearing value writes in `internal/processor` or `packages/`). Accepted narrowing, now fail-closed |
| Mixed-version fleet (old + new binary concurrently) | — | old binary maintains 2-token docs via the old durable; new maintains ≥5-token keys via the v2 durable; **each version reads only its own shape** (prefix `adj.<id>.>` never matches a 2-token doc key; `Get adj.<id>` never sees per-edge keys) | safe-degraded coexistence; old side keeps the status-quo jam until retired |

## 5. What this deliberately does not change

Both directional entries per link; bare-NanoID keying; the `Ready()`/lag-zero boot gate; `Nak` for
genuinely transient errors (`Term` now covers the permanent classes, §3.2); the bucket's name,
ownership, privacy, ACLs. The natsperm matrix is untouched — verified against the owner-included
stream-admin denies (§2.4).

## 6. Build plan — Increment 1 (the fix) + Increment 2 (hub reads back on the fast path)

### 6.1 `adjacency` package rewrite

`Build` → validate segments, derive key, `Put` / TTL'd purge; `Neighbors` → snapshot prefix, filter
markers, unmarshal, fingerprint. `EdgeEntry`, `CoreKVEvent`, `DirectionMatches` unchanged; `AdjValue`
deleted. `Neighbors` documents what was always true: **no ordering guarantee** on the returned slice.
Consolidation while here: the two-directional-event construction is duplicated verbatim at all three
writer sites ([bootstrap.go:201-220](../../internal/refractor/consumer/bootstrap.go),
[evaluate.go:820-827](../../internal/refractor/pipeline/evaluate.go),
[pipeline.go:2494-2499](../../internal/refractor/pipeline/pipeline.go)) — hoist it into one
`adjacency.EventsForLink(key, isDeleted)` so the derivation that value-neutrality rests on has
exactly one author.

### 6.2 `subjects` — `AdjEdgeKey` / `AdjPrefix`

`AdjEdgeKey` validates **all four** segments (non-empty + token charset) and returns an error the
caller maps to `Term` — the existing `validateToken` panic stays only as the programmer-misuse guard
behind the error path. (Adversarial pass: a legacy event with an empty `Name` must not panic the
bootstrapper goroutine, and must not become a server-rejected key that Nak-loops.)

### 6.3 Substrate: one new read primitive — `KVSnapshotPrefix`

`Conn.KVSnapshotPrefix(ctx, bucket, prefix) (entries []KVEntry, fingerprint uint64, error)` (+ the
`KV` handle delegate), returning **live** entries only, marker sequences folded into the
fingerprint. Two internal paths:

1. **Fast path — multi-subject direct get** (`{"multi_last": ["$KV.<bucket>.<prefix>>"]}` to
   `$JS.API.DIRECT.GET.KV_<bucket>`): the server computes and streams the **entire response under
   the stream read lock** (`server/stream.go:5811-5813`) — a genuinely atomic point-in-time
   snapshot. Response messages carry `Nats-Subject`/`Nats-Sequence` (markers included, identified
   by the `KV-Operation` header), terminated by `204 EOB`. Protocol rules from the adversarial
   pass: **no continuation** — the request sets neither `batch` nor `max_bytes` (the server's
   default send budget is `MaxPending` = 64 MiB against a ≤1,024 × ~300 B ≈ 0.3 MiB worst-case
   legal response, so a short read is abnormal by construction); the client asserts
   `Nats-Num-Pending == 0` at EOB and treats any short read, or a mid-stream `404 Message Not
   Found`, as **retry-whole** (each attempt is individually atomic). `404 No Results` is the
   *normal* empty-prefix answer (`stream.go:5858-5862`) → empty entries, fingerprint 0. A
   cursor-based continuation was considered and rejected: under history-1, a concurrent `Put`
   removes a subject's only `≤ up_to_seq` message and `MultiLastSeqs` silently omits it
   (`filestore.go:3845-3880`) — the pinned cursor pins a ceiling, not a set.
2. **Fallback — stability-verified watcher snapshot**, triggered by the server's
   `413 Too Many Results` (hard cap: 1,024 matched subjects, checked before any send —
   `stream.go:5809` + `filestore.go:3891-3896`; paging cannot lift it). **The watcher's initial
   pass is NOT atomic** at history 1 — the server takes the `MaxMsgsPer == 1` short-circuit and
   builds no per-subject skip list (`server/consumer.go:6171-6198`), and nats.go terminates the
   init pass on a *count* (`jetstream/kv.go:1288-1296`), so a concurrent write can both hide a
   live key and blend instants (both reviewers, independently). The primitive therefore runs the
   drain **twice** and compares the two (key → revision) maps: equal ⇒ no write raced either pass
   ⇒ the set is a true point-in-time state; unequal ⇒ retry (bounded, short backoff), then a
   transient error (callers Nak/defer — never a silent partial). Hardening inherited from the
   in-repo key-lister guards (`substrate/kv.go:205-213`): an `InitialConsumerPending` failure is
   an error (nats.go's own path would otherwise declare init-done after **one** entry —
   `jetstream/kv.go:1344-1352` + `js.go:2062-2069`), and `ctx.Err()` is checked after the drain so
   a timeout is never mistaken for completion.

Substrate already hand-rolls raw JS API interactions where nats.go lacks a surface (`KVPutWithTTL`,
`KVUpdateWithTTL`); the multi-get request-reply and the TTL'd rollup publish (`KVPurgeWithTTL`)
follow that precedent. nats.go v1.52.0 has no `multi_last` client surface (grep: zero hits).

### 6.4 Migration — structural, stateless, no admin API

**The durable name is the format selector.** The new binary consumes Core KV through
`refractor-adjacency-v2`; the constant is the single source (`consumer/bootstrap.go:17`). First boot
of the new binary: the v2 durable does not exist ⇒ created at DeliverAll ⇒ **`NumPending` = the full
retained stream** ⇒ the existing `Ready()` gate holds every rule consumer until the per-edge index
is completely rebuilt — *structurally*, with no marker to race and no purge to be denied. (The
adversarial pass killed the draft's marker-plus-purge shape twice over: the purge is
permission-denied (§2.4), and a surviving old durable would have closed `Ready()` instantly over an
empty index. A fresh durable *name* has neither failure mode: it never pre-exists, and two new
instances sharing it split the replay exactly as two instances share the durable today.)

Cleanup, all publish-only and non-blocking: at boot the new binary best-effort deletes the old
`refractor-adjacency` durable (consumer delete is granted — the DurableJanitor's existing verb);
when a snapshot of `adj.>` finds 2-token document keys, it retires each with a TTL'd purge publish
(~5,059 publishes once, then the subjects expire away). Neither step gates boot; old documents are
invisible to every new-shape read regardless (positional prefix `adj.<id>.>` cannot match a 2-token
key — reviewer-verified).

**Rollback runbook (one line):** deploy the old binary — it recreates its old-name durable at
DeliverAll and rebuilds the document index from the same replay; the platform returns to the
status-quo-ante (including the hub jam). No state the new binary wrote is read by the old one.

Rebuild cost on this host: ~19.4 K links → ~39 K puts of ~300 B, linear, tens of seconds — versus
today's quadratic 2 GB ladder that jams and loops (§1.2).

### 6.5 Marker hygiene = the TTL, not a janitor

Delete markers self-expire (§3.2). No `PurgeDeletes` sweep exists in this design — the stock API is
a full-bucket watcher plus one *denied* stream-purge per marker (`jetstream/kv.go:1499-1546`), so
the adversarial pass's "the janitor never runs" finding is resolved by not needing one. Marker TTL
24 h: long enough that no footprint window ever spans it (§3.3), short enough that churn subjects
never accumulate against the 1,024 fast-path cap.

### 6.6 Provisioning: `PerKeyTTL: true` on the bucket registry row

One field in `internal/bootstrap/platform_buckets.go:84-87`. Enabling `AllowMsgTTL` on the existing
stream is legal at our pin (update rejects only the disable direction —
`nats-server@v2.14.0/stream.go:2301-2304`); if the provisioner reconciles by recreate instead, the
index is derived state and the v2 rebuild path (§6.4) covers it by construction. This is the
design's only provisioning change and its only file outside Refractor + substrate.

### 6.7 Detection canary — at BOTH write chokepoints

Warn (rate-limited per key) when a value exceeds half the negotiated `max_payload`: once in the
plain KV write family (`KVPut`/`KVCreate`/`KVUpdate` + TTL variants — covers Refractor's adapters
and every operational-bucket writer) and once in `checkBatchSize` (`substrate/batch.go:216-218`) —
because the Processor's Core KV writes go through `AtomicBatch`'s raw publishes and never touch the
KV family (adversarial pass). Today's failure incubated silently from 500 KB to 1 MiB over weeks;
this is the class-detector for the *next* aggregate-valued key, wherever it grows.

### 6.8 Test migration + regression proof

- The three raw-JSON tests (§2.3) move to `adjacency.Neighbors`; footprint tests keep their
  scenarios with fingerprint-opaque assertions.
- **Regression e2e proving the original failure**: fixture server with `max_payload` lowered
  (`natsfixture.StartServerWith(t, opts)`, `natsfixture.go:106`), one hub seeded past the old
  ceiling-equivalent — every edge indexed, fan-out green; the same test seeds past 1,024 keys to
  exercise the 413 → stability-verified fallback, and removes an edge to prove the marker/expiry
  retraction transport. Fails against the old shape by construction.
- Substrate primitive tests: fast-path atomicity, 404-empty, short-read retry, 413 fallback,
  stability-loop convergence under concurrent writes, `InitialConsumerPending`-failure and
  ctx-expiry hard errors, fingerprint equality across both paths.
- Convergence fixtures cross the real boundary (CDC through the Bootstrapper), extended with the
  tombstone-expiry and self-loop rows of §4.

### 6.9 Docs

Rewrite `docs/components/refractor.md` §"Refractor adjacency KV" (the current "Entry shape" row is
stale even for today's code) and add the multi-subject direct get row (ADR-31, introduced 2.11,
1,024-subject cap) to `docs/vendors.md`'s version-gated features table.

**Increment 1 exit gates:** `go build ./...`, `make vet`, `golangci-lint run ./...`,
`make verify-kernel`, full `go test ./...` (substrate is a shared default), and on the dev host: the
v2 boot observed once — rebuild completes, hub fully indexed, the durable's redelivery churn gone.

### 6.10 Increment 2 — typed-hop push-down (required, second fire)

With relation-first keys, a typed hop reads `adj.<node>.<rel>.>` (or `…<rel>.<dir>.>`) — and because
a hub's *per-relation* subsets are small (the identity hub's `holdsRole` set is ~5 keys against
2,327 `providedTo`), **typed reads on hubs drop back under the 1,024 cap onto the atomic fast
path**. This is not an optimization garnish; it is how the capability plane's hub crossings (lens
walks *and* `footprintValid`'s selector-arm re-reads, which are un-memoized and re-run on drift
retry) avoid living permanently on the watcher path — the adversarial pass is right that the two
live hubs are past the cap from day one, and §1.3's growth means more will follow. Scope: the
executor's memoization unit becomes (node, selector) with the per-unit certificate argument §13.4
already established; untyped/fallback hops keep the full-prefix read and fingerprint; the selector
set's identity unit becomes `EdgeID+direction` (closing the self-loop caveat in §4). The
ActorEnumerator BFS stays full-prefix by design (sound superset, Contract #6) — its hub cost is
bounded today by the same reads it already does, and its long-term arrest is the service-instance
pruner (backlog) plus the lens-decomposition brief's invalidation plans, both outside this design.

## 7. Vendor grounding (pinned versions; per docs/vendors.md)

| Claim | Source at pin |
|---|---|
| `max_payload` default 1 MiB; client rejects oversize pre-wire | `nats.go@v1.52.0/nats.go:123,4463`; no override in `deploy/nats-server.conf` |
| multi-subject direct get: request/response, headers, EOB; **whole response computed and sent under the stream read lock** | `nats-server@v2.14.0/server/stream.go:5798-5916` (`getDirectMulti`; RLock at `:5811-5813`); ADR-31 |
| **1,024-subject hard cap → `413`**, checked on the matched-subject count before any send — paging cannot lift it | `server/stream.go:5809,5847-5857`; `server/filestore.go` `MultiLastSeqs`: `len(seqs) > maxAllowed → ErrTooManyResults`; ADR-31 verbatim |
| `404 No Results` is the normal empty-set answer; `404 Message Not Found` can follow partial data | `server/stream.go:5858-5862`, `:5872-5877` |
| Cursor continuation unsound at history 1 (removed message ⇒ silent subject omission) | `server/filestore.go:3845-3880` |
| **Watcher init pass is not atomic at history 1**: `MaxMsgsPer==1` short-circuit builds no skip list; init ends on a count | `server/consumer.go:6171-6198`; `nats.go@v1.52.0/jetstream/kv.go:1288-1296`, `:1344-1352`; `js.go:2062-2069` |
| Delete/purge markers are retained last messages, returned by multi-get and watchers; per-key purge is a **rollup publish** | `server/stream.go:5865-5911`; `nats.go@v1.52.0/jetstream/kv.go:1150-1156`, `:460-476` |
| Per-message TTL (ADR-48) expires markers; `AllowMsgTTL` enable-on-update legal, disable rejected | `nats-server@v2.14.0/server/stream.go:2301-2304`, `:6321`; ADR-48 (introduced 2.11 ≤ our pin) |
| Stream-admin verbs (PURGE incl.) denied to every non-bootstrap component, owner included | `internal/natsperm/matrix.go:63-71,115-127`; `deploy/nats-server.conf:60`; `conf_test.go:985-1010` |
| KV key charset admits every derived token; accepts empty-adjacent dots (hence §6.2's non-empty validation) | `nats.go@v1.52.0/kv.go:369` |
| nats.go v1.52.0 has no `multi_last` client surface | grep of the pinned module: zero hits |

## 8. Increments and sequencing

**Fire 1 (Inc 1):** §6.1–§6.9 — the ceiling removed, migration structural, hygiene by TTL, canary at
both chokepoints. Independently shippable and green. **Fire 2 (Inc 2):** §6.10 — typed push-down,
selector identity `EdgeID+dir`, hub reads back on the atomic fast path. Both are this design; the
Steward builds them in order once ratified.

## 9. Reconciliation with the existing mental model

- **"Didn't §13.4 already fix the shared-hub problem?"** It fixed the *drift-comparison* granularity.
  The *storage* stayed one document — O(degree) CAS writes and a hard ceiling. This is the storage
  half; §13.4's mechanism survives byte-for-byte, and Inc 2 extends its per-unit certificate to
  per-selector *reads*.
- **"Didn't the footprint-reduction campaign just remove adjacency consumers?"** It removed 95
  *standing* per-pipeline watchers. This design adds **zero standing consumers**; the fallback's
  ephemeral drains are per-call, and Inc 2 confines them to untyped hub reads.
- **"Doesn't the write path already have a bounded-links answer?"** Yes — `kv.Links` (Contract #2
  §2.5) is the *op-time* bounded enumeration over Core KV. This is its read-side complement; the
  contract's "adjacency stays Refractor-private" language is untouched.
- **"Do we already keep per-item small keys somewhere?"** Refractor's own Personal Lens interest
  registry (`personal-lens-interest`, one 61 B key per pair) — the in-repo precedent, one shelf over.
- **"How does this sit with multi-cell?"** The ratified multi-cell design's Global Adjacency Index is
  a *vertex → cell routing* KV and cites today's bucket only as P1-operational-state precedent.
  Orthogonal; a bounded per-key shape is also the only one that could ever partition.
- **New state?** No new *kind* of state: same bucket, same derivation, same writers. The old format
  marker idea is gone — the durable name carries the version, which is state the platform already
  manages (create/delete/janitor) with a defined lifetime at every boundary.

## 10. Alternatives considered

1. **Chunked documents** (`adj.<node>.<chunk>`): keeps O(chunk) read-modify-write and CAS contention,
   adds rehash-on-overflow with multi-writer coordination, keeps a ceiling, and needs a
   head-revision guard against torn multi-chunk reads. Rejected.
2. **Cap + refuse**: a fail-closed cap rejects a legitimately high-degree hub and freezes its edge
   set *by policy*. Rejected (house reflex: prefer paging over caps).
3. **Prune the hub's sources** (service-instance/op-vertex pruner): bounds the reported instance;
   the 61%-full identity hub is un-prunable business state. Stays an independent backlog item — and
   post-fix it is *promoted* in importance, because the jam was accidentally capping the
   enumerator's BFS through the service hub (§6.10). Rejected as the fix.
4. **Raise `max_payload`**: global blast radius, ~8× ceiling move, keeps quadratic rebuilds; the
   platform treats `max_payload` as deployment headroom a design must not consume
   (`substrate/batch.go:23-29`). Rejected.
5. **No index — mid-token-wildcard scans of Core KV links**: soft tombstones force body reads per
   link; bare-NanoID seeds have no type to build a filter from; per-hop scan cost. Rejected (that
   primitive exists where it belongs: op-time `kv.Links`).
6. **Key-encoded entries, empty values**: freezes the entry schema into the subject and cannot carry
   `CoreKvKey`, which the ratified relationship-data design binds from the in-hand entry. Rejected.
7. **Watcher-only read path** (no multi-get): one mechanism, but every uncached node read
   platform-wide pays consumer churn + the double-drain stability protocol, where today it pays one
   `Get` — and the §13.4 validation re-reads pay it too. Kept strictly as the >1,024 fallback,
   which Inc 2 then shrinks to untyped hub reads. (The adversarial pass sharpened this honestly:
   *without* Inc 2 the two live hubs sit on the fallback for every read — which is why Inc 2 is
   required, not optional.)
8. **Marker cleanup via `PurgeDeletes` janitor**: full-bucket watcher per tick plus one
   permission-denied stream purge per marker. Replaced by the TTL'd markers (§6.5).

## 11. Risks and residuals (resolved or named)

- **Fallback-frequency creep**: nodes crossing 1,024 shift to the costlier path silently → the
  primitive logs the prefix + count on every 413 fallback (the degree signal an operator/Lamplighter
  can watch); Inc 2 returns typed traffic to the fast path; marker TTL keeps dead subjects from
  inflating counts.
- **Unbounded hub degree is now real** (the jam was an accidental cap): the enumerator BFS and
  untyped hub reads scale with true in-degree. Named arresters: the pruner (backlog, promoted) and
  the lens-decomposition invalidation plans. At current scale and growth (~190 service links/day)
  the watcher path stays well inside handler AckWait budgets; the 413 log line is the early-warning
  gauge.
- **Marker-TTL timing precondition** (§3.3): pinned in the TTL constant's doc comment; violated only
  by out-of-model manual purges.
- **Stability-loop liveness**: a prefix under *continuous* concurrent writes could retry repeatedly;
  bounded retries surface a transient error → Nak/defer, never a silent partial — and the write rate
  needed to starve two consecutive quiet drains on one node's prefix has no live analogue (the
  hub's peak is ~190 events/day).
- **Mixed-fleet window**: old binary keeps maintaining (and jamming) its document index until
  retired; disjoint keyspaces and durables make this safe-degraded, not wrong (§4).

## 12. Board / consumer surface

Board row → `📐 awaiting-Andrew` with this doc linked; on ratification the **Lattice Steward** builds
Fire 1 (§6.1–6.9) then Fire 2 (§6.10). The claim-ceremony flake row (`lattice.md:164`) is unaffected
(timing, not shape), but §6.8's migrated polls become the pattern that family should adopt.

## 13. Adversarial pass — run 2026-08-09, findings folded

Two independent read-only reviewers (mechanism-refuter + edge-case hunter) against the full draft.
Material outcomes, all folded above:

- **BLOCKER — natsperm denies stream purge to the owner** (matrix.go:63-71, conf_test.go:985-1010):
  the draft's marker-plus-purge migration and `PurgeDeletes` janitor were both permission-dead.
  → Migration reshaped to durable-name versioning with publish-only cleanup (§6.4); janitor replaced
  by TTL'd markers (§6.5); §2.4 records the permission envelope.
- **BLOCKER — `Ready()` over an empty index**: a surviving old durable (second instance, rolling
  restart) would satisfy the caught-up gate over a freshly-purged bucket. → The v2 durable name is
  the format selector; the race class no longer exists (§6.4).
- **MATERIAL — watcher init pass is not a snapshot at history 1** (server-side `MaxMsgsPer==1`
  short-circuit; count-terminated init; silent-omission and torn-blend scenarios; the silent
  one-entry truncation when `InitialConsumerPending` errors). → The stability-verified double-drain
  protocol + hard errors on init/ctx failures (§6.3.2).
- **MATERIAL — the two live hubs are already past the 1,024 cap**, so "hot-path parity" did not
  cover the nodes this design exists for. → Inc 2 promoted from optional to required (§6.10);
  honest framing in For-Andrew and §10.7.
- **MATERIAL — cursor continuation can silently drop a live subject** at history 1. → Continuation
  forbidden; retry-whole with `Nats-Num-Pending == 0` assertion (§6.3.1).
- **MATERIAL — fingerprint-lowering by marker removal can mask drift in principle.** → Stated as a
  timing precondition with the 24 h-vs-seconds margin pinned (§3.3).
- **MATERIAL — a malformed legacy event would panic the bootstrapper or Nak-loop forever**; "Build
  errors are transport-shaped" was false once keys derive from event fields. → Four-segment
  validation → `Term`; `IsInvalidKeyError` classified permanent (§3.2, §6.2).
- **MATERIAL — the size canary's chokepoint missed every Core KV write** (AtomicBatch publishes
  raw). → Canary at both chokepoints, rate-limited (§6.7).
- Minor: self-loop EdgeID aliasing in selector sets (noted in §4, closed by Inc 2's `EdgeID+dir`);
  `404` response shapes grounded (§7); `dir` vocabulary mapping defined (§3.1).

Claims that survived attack unchanged: fast-path RLock atomicity, the 1,024/413 grounding,
per-edge/whole-doc convergence equivalence, fingerprint monotonicity absent marker removal, the
natsperm admissibility of `DIRECT.GET`, and the key-charset legality of well-formed segments.

## 14. Redirect 2026-08-09/10 — the primitive vs the component, measured

Andrew's ratification response: the multi-subject direct get is the interesting object — file it as
a substrate function, check performance (vendor first, spike if unpublished), and reconsider whether
the adjacency index should exist at all, or stay as-is with an overflow fallback for rare hubs.

### 14.1 Measurements (this host, 2026-08-10)

No published vendor numbers exist: ADR-31 documents the *motivation* — serve KV reads from **all
stream replicas as a responder queue group** and "bypass administrative API overhead", trading away
read-after-write coherency on R>1 (moot at our R1; and today's `kv.Get` is already a direct get on
AllowDirect buckets, so batching is coherency-neutral vs the status quo). So: a read-only spike
(scratchpad `dgspike`, nats.go v1.52.0, `lattice.nk`) against the live core-kv — 50.7 K values,
~10 K link subjects, kernel processes running. p50/p95 of full request-drain:

| Case | Shape | p50 | p95 | Per-key |
|---|---|---|---|---|
| `single` | raw `last_by_subj` ×400 | 182 µs | 1.38 ms | 182 µs |
| `kvget` | jetstream `kv.Get` ×400 (today's path) | **153 µs** | 180 µs | 153 µs |
| `multi10` | `multi_last`, 10 explicit keys ×100 | 306 µs | 410 µs | **31 µs** |
| `multi100` | `multi_last`, 100 explicit keys ×100 | 3.16 ms | 9.43 ms | **32 µs** |
| `outpfx` | filter `lnk.permission.<id>.>` (5 matches) | 294 µs | 363 µs | — |
| `inwild` | filter `lnk.*.*.*.*.<id>` (180 matches) | 5.28 ms | 16.4 ms | — |
| `inwild1` | same shape, 1 match | **536 µs** | 605 µs | — |
| `cap413` | filter over the 3,919-subject instanceOf hub | 1.28 ms → **413** | — | — |
| `lister` | `ListKeysFiltered` (ephemeral consumer), same 180 keys | **22.9 ms** | 40.4 ms | — |

Readings: (1) **batched exact-key reads amortize ~5× better than sequential gets** (31 vs 153
µs/key) and are atomic. (2) `inwild1` isolates the trailing-literal **subject-tree walk: ~350–400 µs
over ~10 K link subjects — the term scales with *total links in the stream*, not the node's degree**
(~35 µs/1K subjects ⇒ ~4 ms at 100 K links, ~40 ms at 1 M), and it runs under the core-kv stream
read lock, i.e. against the Processor's commit path. Delivery adds ~30 µs/message. (3) The
ephemeral-consumer path costs **4.3×** the multi_last equivalent on the identical result set — and
that ratio grows as the set shrinks (consumer setup is the floor). (4) The 1,024 cap 413s on the
real hub today; any no-index or overflow shape needs the consumer fallback for hubs from day one.

### 14.2 Application: step-4 hydration (file with the primitive)

`step4_hydrate.go` issues one `KVGet` per declared key across three sequential loops
(`:240,:276,:310`), and its own comment (`:215-223`) documents that "the two live GETs straddle any
concurrent create or purge" — the straddle machinery (present-wins reconciliation) exists *because*
the reads are not atomic. One `multi_last` with the exact declared key list replaces N round trips
with one (a 10-key set: ~1.5 ms → ~0.3 ms), returns absent-by-omission and tombstones-by-marker
consistently from **one locked snapshot**, and consistent revisions mean fewer step-8 OCC retries.
Cap guard: read-sets ≫ 1,024 fall back to the loop (or split) — today's sets are nowhere near it.

### 14.3 Application: the ephemeral-enumeration corpus

~12 non-test files run watcher-backed enumerations per call today (`ListKeysPrefix` /
`ListKeysFiltered` / `KVListKeys`): pkgmgr installer censuses (10 sites), the Refractor NATS-KV
adapter, Loupe browse surfaces (weaver/review/vertex/lens/server), `cmd/wellness-app` +
`cmd/loftspace-app` P5 list endpoints, weaver state scans, and the full engine's whole-type anchor
scans (`executor.go`, 3 sites). Every ≤1,024 case is a one-round-trip multi_last candidate at ~4×+
measured savings — several also do ListKeys-then-Get-each (`personalinterest.IsRelevant`), which
collapses to the same single request *with values included*. The >1,024 cases keep the lister.

### 14.4 The three shapes for the adjacency index

| | **A — delete the index** | **B — doc + overflow mark (recommended)** | **C — per-edge rekey (§3–§13)** |
|---|---|---|---|
| Common-case read | multi_last on core-kv: outbound ~0.3 ms; inbound 0.5 ms+walk | **`kv.Get` doc, 153 µs — unchanged** | prefix multi_last ~0.3–1 ms |
| Hub read (>1,024°) | consumer fallback on core-kv, ~10² ms-class | same fallback, **only for marked nodes**; typed hops via `lnk.*.*.<rel>.*.<id>` ride multi_last (~0.6 ms) | prefix consumer fallback on the adjacency bucket (no full-tree walk); typed prefix push-down (Inc 2) |
| Scale behavior | inbound walk ∝ **total links**; BFS multiplies it per visited node (3,912-visit enumeration ≈ 0.6 s today → ~15 s at 100 K links) | non-hub reads flat; marked-hub fallback ∝ hub degree + walk | all reads ∝ degree — the only shape that stays flat |
| Write path | **none** (Core KV is the index) | unchanged CAS doc (≤ threshold ⇒ bounded ~270 KB rewrites); mark branch ends the jam | O(1) puts, no CAS |
| Machinery delta | −bucket −Bootstrapper −3 writers −pre-apply −Ready gate | +mark branch, +marked-node fallback read, +marked-node fingerprint | +v2 migration, +TTL markers, +stability fallback |
| Freshness | commit-fresh (no CDC lag, pre-apply obsolete) | doc lags as today; marked nodes commit-fresh | index lags as today |
| Core-kv coupling | every neighbor read locks the write stream | only marked-hub reads do | none (separate bucket) |
| Jam removal | total | total (mark swallows; jammed doc self-heals on first touch) | total |

**Recommendation: B now, C shelved as the scale successor, A rejected for the BFS/walk arithmetic.**
B is Andrew's "keep existing structure, mark the rare too-many-links vertices, fall back to
ephemeral" — measured, it keeps the 153 µs common case with zero migration, ends the jam and the
Nak loop (the overflow branch makes oversize unreachable), and the marked-hub fallback inherits
Shape A's one real virtue (commit-fresh reads straight off Core KV, no pre-apply needed on that
path). A's leanness is real but its inbound walk term prices every BFS crossing at O(total-links)
under the write lock — wrong direction for the one graph store everything shares. C's uniformity
is worth having when hub *counts* grow, not before; its spec stays ready.

### 14.5 What carries into Shape B from the §13 review (shape-independent findings)

- **The consumer fallback on core-kv is a history-1 watcher too** — the §6.3.2 tearing analysis and
  the stability-verified double-drain protocol apply verbatim to B's marked-node reads.
- **Term/Nak classification** (`IsInvalidKeyError` ⇒ permanent) and the four-segment validation at
  the event boundary: hygiene that ships regardless (B's mark branch removes the *size* trigger,
  not the class).
- **The ½-max_payload canary at both chokepoints** (KV family + `checkBatchSize`): B keeps
  aggregate documents, so the approach-warning matters *more* under B than C.
- **`EventsForLink` consolidation** (three duplicated constructors): still right.
- **Marked-node footprint fingerprint**: `EdgeRevisions` for a marked node comes from the fallback
  enumeration (max sequence over matched link subjects, markers included) — same mechanics §3.3
  specifies, scoped to marked nodes; unmarked nodes keep the doc revision untouched.
- **Mark lifecycle** (the new-state-lifetime discipline): the mark is a monotonic per-node latch,
  set by `Build` at the degree threshold, stored as the node's `adj.<nodeId>` value (a sentinel doc
  — no new keyspace), never unset (degree shrinking below threshold is not worth un-marking),
  reset only by the same environment wipes that reset the bucket, and replayed deterministically by
  the Bootstrapper (the rebuild crosses the threshold the same way). Threshold ~1,000 edges
  (~270 KB doc): well under both the 1 MiB jam and the 1,024 typed-read cap, high enough that
  marked nodes are genuinely hubs.

### 14.6 Filed on the board

The substrate primitive + first consumers is its own row (`[Substrate] multi-subject direct get`,
★★★): the `KVGetMulti` function (exact lists + filters, EOB/404/413/short-read handling per §6.3.1),
step-4 hydration adoption, and the enumeration-corpus conversions — valuable under every shape
above, and first regardless of the adjacency decision. The adjacency row stays 📐 on this doc
pending the A/B/C call.
