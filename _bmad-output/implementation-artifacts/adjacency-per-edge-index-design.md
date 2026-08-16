# Adjacency under the 1 MiB ceiling — and the multi-subject direct-get primitive

**Status: ✅ Andrew-ratified 2026-08-10 — Shape B (overflow mark + Core-KV fallback), §15 is the
build plan.** Designer fire 2026-08-09 (Winston); redirected by Andrew 2026-08-09 after the
multi-subject direct-get finding; measured 2026-08-10 (§14); **ratified "go with B" 2026-08-10**.
Board row: `[Refractor] A node's whole adjacency list is one KV value…` (★★★). The **substrate
multi-subject direct-get primitive** is its own ratified board row (Andrew-directed) and builds
**first** — §15's fallback read consumes it.

## Ratification record (Andrew, 2026-08-10)

- **Shape B ratified**: keep the per-node document for ordinary nodes; when a node crosses the
  overflow threshold, `Build` latches a **mark** and stops absorbing its edges; a marked node's
  reads fall back to Core-KV link enumeration (commit-fresh). Build plan: **§15**.
- **Shape A (delete the index) rejected**: the inbound trailing-wildcard walk prices every read at
  O(total links) under the core-kv read lock, and the ActorEnumerator BFS multiplies it per visited
  node (§14.4).
- **Shape C (per-edge rekey, §3–§13) SHELVED**: the fully-specified, adversarially-reviewed scale
  successor. **Do not build without a new ratification** — its trigger is hub *count* growth
  (several marked nodes whose fallback reads dominate eval timings), not calendar time.
- **The multi-subject direct-get substrate primitive is ratified to build** (Andrew: "definitely
  file… as a new substrate function"): `KVGetMulti` per §6.3.1's protocol rules (no continuation,
  EOB `Nats-Num-Pending == 0` assertion, 404/413 handling) + the consumer-snapshot fallback with
  the §6.3.2 stability protocol, plus the first consumers (step-4 hydration, the
  enumeration-corpus conversions, §14.2–14.3). Measured record: §14.1.

**No frozen-contract change, no provisioning change, no permission change** in Shape B. The bucket
stays Refractor-private operational state (lattice-architecture P1; Contract #2 §kv.Links).

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

## 3. Shape C — one key per directional edge (SHELVED 2026-08-10 — do not build)

§3–§13 are the complete, adversarially-reviewed specification of the per-edge rekey, **shelved at
ratification in favor of Shape B (§15)**. They are retained as the scale successor's ready spec —
building them requires a new ratification. Nothing below this line is part of the ratified build.

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
| Common-case read | multi_last on core-kv: outbound ~0.3 ms; inbound 0.5 ms+walk | ~~**`kv.Get` doc, 153 µs — unchanged**~~ **a 2-key `multi_last` (doc + mark), ~300 µs — struck 2026-08-10 (Fire B1 close review)**: any shape that consults a mark on the read path pays a second read, so "unchanged" was never available. The ratified §15.1 cache does not avoid it either — it caches only *positives*, so an unmarked node misses on every read and pays two sequential `Get`s (~306 µs) for the same answer, non-atomically. Batching is the cheaper of the two, not free. | prefix multi_last ~0.3–1 ms |
| Hub read (>1,024°) | consumer fallback on core-kv, ~10² ms-class | same fallback, **only for marked nodes**; typed hops via `lnk.*.*.<rel>.*.<id>` ride multi_last (~0.6 ms) | prefix consumer fallback on the adjacency bucket (no full-tree walk); typed prefix push-down (Inc 2) |
| Scale behavior | inbound walk ∝ **total links**; BFS multiplies it per visited node (3,912-visit enumeration ≈ 0.6 s today → ~15 s at 100 K links) | non-hub reads flat; marked-hub fallback ∝ hub degree + walk | all reads ∝ degree — the only shape that stays flat |
| Write path | **none** (Core KV is the index) | unchanged CAS doc (≤ threshold ⇒ bounded ~270 KB rewrites); mark branch ends the jam | O(1) puts, no CAS |
| Machinery delta | −bucket −Bootstrapper −3 writers −pre-apply −Ready gate | +mark branch, +marked-node fallback read, +marked-node fingerprint | +v2 migration, +TTL markers, +stability fallback |
| Freshness | commit-fresh (no CDC lag, pre-apply obsolete) | doc lags as today; marked nodes commit-fresh | index lags as today |
| Core-kv coupling | every neighbor read locks the write stream | only marked-hub reads do | none (separate bucket) |
| Jam removal | total | total (mark swallows; jammed doc self-heals on first touch) | total |

**Outcome — RATIFIED: B** (Andrew, 2026-08-10: "go with B"); C shelved as the scale successor; A
rejected for the BFS/walk arithmetic. B keeps the 153 µs common case with zero migration, ends the
jam and the Nak loop (the overflow latch makes oversize unreachable), and the marked-hub fallback
inherits Shape A's one real virtue: commit-fresh reads straight off Core KV, needing no pre-apply
on that path. The build plan is §15.

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
  set by `Build` at the degree threshold, ~~stored as the node's `adj.<nodeId>` value (a sentinel doc
  — no new keyspace)~~ **stored at its own `adjmark.<nodeId>` key — struck 2026-08-10 (Fire B1 brief
  §16.4a): §15.1 supersedes this clause, because an old binary's `Build` unmarshals a sentinel doc to
  zero edges and silently un-marks the hub**, never unset (degree shrinking below threshold is not worth un-marking),
  reset only by the same environment wipes that reset the bucket, and replayed deterministically by
  the Bootstrapper (the rebuild crosses the threshold the same way). Threshold ~1,000 edges
  (~270 KB doc): well under both the 1 MiB jam and the 1,024 typed-read cap, high enough that
  marked nodes are genuinely hubs.

### 14.6 Filed on the board

The substrate primitive + first consumers is its own row (`[Substrate] multi-subject direct get`,
★★★): the `KVGetMulti` function (exact lists + filters, EOB/404/413/short-read handling per §6.3.1),
step-4 hydration adoption, and the enumeration-corpus conversions — valuable under every shape
above, and first regardless of the adjacency decision.

## 15. Shape B build plan — RATIFIED (builds after the substrate primitive row)

The whole change is: a per-node **overflow latch**, a **fallback read** for latched nodes, and
nothing else. No migration, no durable change, no provisioning or permission edits, and — unlike
Shape C — **zero test migration** (no fixture in the tree drives a node past the threshold, and the
three raw-document-parsing e2es read unmarked nodes).

### 15.1 The mark — a separate key, not a field in the document

`adjmark.<nodeId>` in the same bucket (tiny value, e.g. `{"at":<seq>}`), plus an in-process
monotonic cache (`map[nodeID]struct{}`, loaded lazily: first `Build`/`Neighbors` touch of a node
consults the cache, missing → one `Get`, result cached; marks are never unset, so the cache never
invalidates).
**Amended twice, 2026-08-10 — the cache is GONE. The close review found it BLOCKING and it was
deleted, not guarded.** A process-global cache that `Build` short-circuits on while `Neighbors` reads
the truth from KV disagrees with itself the moment the durable mark disappears under a live process —
and that is reachable: `lattice-nats` is ephemeral (`docker-compose.yml:31`), an operator may purge
the bucket to force an index rebuild, and the Refractor now *survives* a NATS outage rather than
restarting with it (`08ca1f23`, `MaxReconnects=-1`). A warm cache then no-ops every rebuild write
while `Neighbors` finds neither mark nor document and returns an **empty edge set as authoritative,
permanently, silently** — the frozen-read failure class again, on the same node, taking every
capability grant derived through that hub with it. The original state table answered this boundary
with "every environment that wipes the bucket restarts the Refractor with it", which is an
environmental assertion the preceding commit had already falsified on purpose. The cache was a pure
optimization — a miss performs the read `upsertEdge` makes anyway — so it bought nothing and risked
a silent wrong answer. Deleting state beat guarding it. The first amendment, kept below for the
mechanism it settled:

**(Fire B1 brief §16.4b): the cache is a WRITE-PATH optimization only.** As
specified above it holds *marked* nodes, so a miss means "not known to be marked" and costs an extra
`Get` on every read of an unmarked node — i.e. on essentially every read, permanently, contradicting
§14.4's "common-case read 153 µs — unchanged". Caching the negative is unavailable (another
instance's mark would never be observed). **`Neighbors` instead reads `adj.<id>` and `adjmark.<id>`
in ONE `KVGetMulti` call**: one round trip, and atomic — two sequential `Get`s would let a node
latching between them return the just-emptied document with no mark, an empty edge list presented as
authoritative. ~~The positive cache survives in `Build` only, and is never consulted to conclude absence.~~
**Struck 2026-08-10 (close pass) — the cache was deleted outright; see the amendment at the head of §15.1
for why a monotonic positive is still not safe to hold in process memory across a bucket wipe.** **Why not an `overflow` field inside `adj.<nodeId>`:** a mixed-fleet window would
corrupt it — an old binary's `Build` unmarshals the sentinel doc to zero edges, appends one, and
writes back a 1-edge document, silently *unmarking* the hub; the new binary then reads a
1-edge index as authoritative for a 3,900-edge node (converged-but-wrong) until degree re-crosses
the threshold weeks later. A separate key is invisible to the old binary, so the worst mixed-window
outcome is harmless doc churn the new binary ignores. Lifetime: created by `Build` at threshold,
read by `Build`/`Neighbors` through the cache, never unset (a node whose degree later shrinks keeps
paying the fallback — fine, marks are rare), wiped only with the bucket, deterministically
re-latched by the Bootstrapper replay (the rebuild crosses the same threshold).

### 15.2 `Build` — the latch branch

On the upsert path, after computing the post-upsert edge list: if the node is already marked → skip
the doc entirely (write nothing). Else if `len(edges) > adjOverflowDegree` **or**
`len(marshaled) > adjOverflowBytes` → create `adjmark.<nodeId>`, best-effort overwrite the document
with an empty-edges body (reclaims the jammed ~1 MiB and leaves a breadcrumb; the mark key is the
authority, so an old binary trampling the doc changes nothing), cache the mark, return nil. Removal
path on a marked node: no-op. Thresholds: **`adjOverflowDegree = 3072`, `adjOverflowBytes =
800 KiB`** (both, because degree alone does not bound bytes at variable entry size; 3,072 × ~268 B ≈
823 KB keeps unmarked documents comfortably under the 1 MiB jam). At these values exactly one live
node latches — the already-jammed `leaseServiceInstance` meta (3,919°), **on its first post-deploy
`Build` touch: the jam and the Nak loop end there**, structurally. The 2,335° identity hub stays on
its 645 KB document (~150 µs reads, unchanged) with ~10 months of headroom at its observed growth;
§15.6's trigger names what happens as it approaches. Thresholds are consts with a test override.
Riders in the same increment (§14.5): ~~the four-segment validation → `Term` at the event boundary~~
**struck 2026-08-10 (Fire B1 close review) — inapplicable under Shape B.** That rider came from a
Shape C finding about a 4-segment-derived `adj.` key; under B the key is a single token, and the
event boundary's reserved-character screen already matches `subjects.validateToken`
character-for-character, so the panic it guarded is unreachable. The remaining three shipped:
`Build`-error classification (`IsInvalidKeyError` ⇒ permanent ⇒ `Term`; transport ⇒ `Nak`), the
`EventsForLink` consolidation, and the ½-max_payload canary at both write chokepoints.

### 15.3 `Neighbors` — the fallback read for marked nodes

Unmarked (the 99.9 % path): today's single `Get`, byte-for-byte unchanged. Marked: enumerate Core
KV's canonical link keyspace with **both directional filters in one request** —
`lnk.*.<nodeId>.>` (outbound) and `lnk.*.*.*.*.<nodeId>` (inbound) — via the substrate primitive:

- **≤1,024 combined matched subjects**: one `multi_last` request — one atomic, stream-locked
  snapshot of both directions (§7). Measured shape: ~5 ms at 180 matches, walk term ~35 µs/1K link
  subjects (§14.1).
- **413**: the consumer-snapshot fallback — one ephemeral `DeliverLastPerSubject` consumer with
  `FilterSubjects: [both]` on `KV_core-kv`, ~~drained under the **§6.3.2 stability protocol**
  (double-drain, compare (subject → sequence) maps, bounded retries; `InitialConsumerPending`
  failure and ctx expiry are hard errors) — core-kv is history-1, so the tearing analysis applies
  verbatim~~ **drained ONCE — struck 2026-08-10 (Fire B1 close review), and this is the correction
  that keeps the fire from reinstating its own failure mode.**

  Two reviewers found it independently. A marked node has ~3,920 matching subjects, so it is
  **always** past the 1,024 fast-path cap (`nats-server@v2.14.0/server/stream.go:5809`) — the
  fallback is not the exception for a marked node, it is the only path. Under the double-drain, a
  single write to *any* of that node's ~3,900 incident links between the two passes fails the
  comparison, and three such attempts return a hard error the pipelines turn into a `Nak` →
  redelivery. The node that latches is by construction the one taking writes fastest, so the
  amplification loop this fire exists to end would return on the read side.

  **The atomicity was over-strong for this consumer, not merely expensive.** The stability protocol
  exists so a read-set backing a transactional decision cannot straddle a write. An adjacency edge
  set is not that: each edge is an independent fact, its consumers re-validate through the footprint
  (`footprintValid` re-reads and compares), and the document path this replaces was never atomic
  against the CDC lag either. Requiring more atomicity from the fallback than the document ever
  provided buys nothing and costs availability. One complete drain — keeping the
  `received != NumPending` short-read guard, which is what actually proves completeness — is the
  right guarantee. §14.1 reading 4 had already established that hubs take this path from day one;
  what it did not work through is what the path does while the graph is being written.
- Each matched message: parse the Contract #1 link key from the subject; drop hard-DEL markers
  (empty body / `KV-Operation` header) and soft tombstones (`isDeleted` in the returned body — the
  read is free because multi_last returns values); synthesize the `EdgeEntry` exactly as
  `processLinkEnvelope` derives it (EdgeID = link key; direction by which endpoint is `nodeId`;
  a self-link yields both directional entries — the §4 self-loop fix arrives for marked nodes).
- **Fingerprint** (the returned `uint64`): ~~max sequence over every matched subject, markers and
  soft-tombstones included — monotonic per change~~ **an order-independent 64-bit hash over the
  matched set's `(subject → revision)` pairs — struck 2026-08-10 (Fire B1 brief §16.5)**: markers are
  *unavailable*, since `KVGetMulti` drops them before returning (`kv_multi.go:273,308-313`), and a
  max-sequence over the surviving live set misses a hard delete whenever the deleted subject was not
  the maximum. The hash is strictly stronger and still equality-compared by `footprintValid`
  (`evaluate.go:389,411`), which never orders the value — so the struck "monotonic" property was
  never load-bearing. Unmarked nodes keep the document revision untouched. The §3.3 timing precondition is
  inherited only in the narrow purge case and core-kv has no marker-TTL machinery — nothing new.

**Ordering guarantee on the marked path needs no pre-apply**: the pipelines' link pre-apply exists
because the *index* could lag the CDC event; a marked node's read goes straight to Core KV, where
the link the event describes is **already committed** (the event *is* the commit's CDC). `Build`'s
marked-path no-op is therefore correct, not a gap. Unmarked nodes keep the pre-apply verbatim.

### 15.4 What this does to the §1.2 failure, day one

The dedicated consumer's next delivery touching the hub latches the mark; every queued and future
`instanceOf` event then costs a no-op ack — the 14×-amplified redelivery loop, the per-lens fan-out
failures, and the frozen-wrong 3,912-edge reads all end. Marked-hub reads return the **complete,
current** edge set (3,919 and counting) at consumer-fallback cost — correct-but-slower strictly
dominates frozen-wrong. Every eval memoizes per evaluation (`fetchEdges`), derivation and BFS
memoize per call, so each evaluation pays at most one fallback read per marked node it touches.

### 15.5 Tests (owned by Increment B1)

Regression e2e through the real Bootstrapper with the test-override threshold (e.g. 8): seed past
it → mark key exists, doc emptied, `Neighbors` returns *all* edges via fallback, link fan-out
evaluations green; soft-tombstone one link → it leaves the fallback read (retraction transport);
add an edge between footprint capture and validation on the marked node → drift detected;
concurrent `Build`s from three writers latch idempotently; an unmarked node's doc path byte-stable.
Unit: latch monotonicity, both threshold arms, marked-path no-ops, `EdgeEntry` synthesis incl.
self-loop, mark-cache lazy load. The substrate primitive's own tests (fast path, 413, stability
protocol) live with its row and are not re-owned here.

### 15.6 Increments and the B2 trigger

- **B1 (one Steward fire, after the substrate row ships): §15.1–15.5** + the component-doc rewrite
  (§6.9's scope, adjusted to B) + the `docs/vendors.md` version-gate row. Independently shippable
  and green.
- **B2 (typed fallback narrowing — S, gated on a named trigger)**: for marked nodes, typed hops
  read `lnk.*.<id>.<rel>.>` + `lnk.*.*.<rel>.*.<id>` — per-relation subsets ride multi_last
  (~0.6 ms) instead of the consumer drain. **Trigger: the mark latching on a node whose neighbors
  the capability plane walks** (concretely: the 2,335° identity hub approaching 3,072 — the §15.2
  mark log line is the alarm). Until then B2 is dead scaffolding: today's sole marked node is the
  service meta, whose traversals are not latency-coupled to the auth plane.

**Exit gates (B1):** `go build ./...`, `make vet`, `golangci-lint run ./...`, `make verify-kernel`,
full `go test ./...` (substrate + shared canary defaults), and on this host: one observed latch —
mark key present, doc emptied, consumer-info redelivery churn gone, `instanceOf` fan-out evals
green, marked-node `Neighbors` count ≥ 3,919.

### 14.7 `[Substrate] multi_last` fire brief (build note, 2026-08-10)

**Scope sentence (verbatim, board row):** "One locked, consumer-less round trip returns
last-per-subject for a key list or filters (≤1,024 subjects; 413 above). Measured ~31µs/key vs
153µs sequential gets; 4× the ephemeral lister on the same set. Consumers: step-4 hydration (atomic
read-set), the ~12-file `ListKeysPrefix`-class corpus."

**Verified touch-list** (checked live this fire, not from the design's citations alone):

- `internal/substrate/kv.go` — new file `kv_multi.go` alongside it (package `substrate`); mirrors
  `KVEntry` (`kv.go:18`), sentinel-error wrapping (`errors.go`), and the `Conn`/`KV` handle split
  (`kvhandle.go:18-99`, delegate pattern at `:37-39`).
- `internal/processor/step4_hydrate.go` — three `h.Conn.KVGet` loops confirmed at `:239-257`
  (`declared.Reads`), `:258-296` (`declared.OptionalReads`), `:297-327` (`declared.EgressReads`);
  the "two live GETs straddle" comment confirmed at `:216-223`; present-wins reconciliation
  (`markRequiredAbsent`/`markHydrated`) at `:224-236`.
- `internal/refractor/personalinterest/interest.go:213-244` — `IsRelevant`'s
  `ListKeysPrefix`-then-`Get`-each loop, confirmed verbatim (the design's own §14.3 exemplar).
- `docs/vendors.md:29-40` — version-gated NATS features table (row to add, mirroring the 4
  existing rows' shape).
- `docs/components/substrate.md` — `### KV operations` table (`:110-123`) and the file-map table
  (`:33-49`); `### Review keeps catching (dossier)` (`:484-498`, currently 3 entries, cap 12).

**Vendor grounding, done live (not paraphrased from the design doc):** downloaded
`nats-server@v2.14.0` + `nats.go@v1.52.0` into the module cache and read the real source.
Confirms every wire-protocol claim below to the byte:
`JSApiMsgGetRequest.MultiLastFor` → JSON tag **`multi_last`**
(`nats-server/server/jetstream_api.go:688`); handler `stream.getDirectMulti`
(`nats-server/server/stream.go:5807-5916`), RLock at `:5811-5813` (the atomicity claim);
1,024 cap → `413 Too Many Results` at `:5850-5851` via `store.MultiLastSeqs`'s `ErrTooManyResults`
(`filestore.go`/`memstore.go`); empty-set → `404 No Results` (`:5858-5862`); mid-stream short read
→ `404 Message Not Found` (`:5875-5877`); EOB header template `eobm` asserts `Nats-Num-Pending`
(`:5802-5803,5914`). Response headers: `Nats-Stream`/`Nats-Subject`/`Nats-Sequence`/
`Nats-Time-Stamp`/`Nats-Num-Pending`/`Nats-Last-Sequence`/`Nats-UpTo-Sequence`
(`nats-server/server/stream.go:669-677`). Marker detection: `KV-Operation: DEL|PURGE`
(`nats.go/jetstream/kv.go:494-496`) else `Nats-Marker-Reason: MaxAge|Purge|Remove`
(`nats-server/server/stream.go:641,687-689`; `nats.go/jetstream/kv.go:968-974`). Status/description
decode into synthetic `Status`/`Description` headers by `nats.go`'s own wire parser
(`nats.go/nats.go:4282-4327`) — no manual status-line parsing needed client-side. Subject shape:
request → `$JS.API.DIRECT.GET.KV_<bucket>` (`nats-server/server/jetstream_api.go:106-115`); each
listed subject → `$KV.<bucket>.<key-or-pattern>` (`nats.go/jetstream/kv.go:483-486`, matches
`KVPutWithTTL`'s existing `"$KV." + bucket + "." + key"` convention verbatim). `nats.go` v1.52.0 has
no client-side `multi_last` surface — confirmed by grep, zero hits — so this is genuinely a
hand-rolled raw-protocol addition, same posture as `AtomicBatch`/`KVPutWithTTL`.
Added to `docs/vendors.md` in this fire's commit.

**Precedents to mirror:**

- Raw-protocol hand-roll shape → `KVPutWithTTL`/`KVUpdateWithTTL` (`kv.go:334-357,124-155`): build
  the `$KV.<bucket>.<key>` subject by hand, publish via the raw `*nats.Conn`/`jetstream.JetStream`,
  no high-level KV client call.
- ctx-cancellable streamed-response loop → `WatchKVUpdates` (`subscribe.go:495-530`): `select` on
  `ctx.Done()` vs. a channel, never a bare blocking read with no ctx hook.
- Tombstone/marker classification → `decodeKVMessage`/`kvEventFromUpdate` (`subscribe.go:452-480,
  535-553`): empty body or a non-Put operation ⇒ excluded from "live", mirrored here for the
  fast-path's per-message `KV-Operation`/`Nats-Marker-Reason` check.
- Bounded-retry-with-backoff shape → `connectWithRetry` (`conn.go:209-231`, `connectAttempts`/
  `connectBackoff` consts at `:30-41`).
- Silent-partial-result guard → `KVListKeys`'s post-drain `ctx.Err()` check (`kv.go:204-213`) —
  mirrored in the fallback drain (§ below).
- "Absent by omission" batched-read shape → none in-repo (genuinely new: every existing KV read is
  single-key). No precedent to mirror for the map-keyed-by-resolved-key return shape; justified by
  what the two consumers need (§14.2/14.3: replace "one error per absent key" with "absent from the
  result").

**Increment order (single fire, S-sized; each with a runnable green check):**

1. `internal/substrate/kv_multi.go`: `(*Conn).KVGetMulti(ctx, bucket, keys) (map[string]*KVEntry,
   error)` — fast path (multi_last, retry-whole on short-read) + stability-verified double-drain
   fallback on 413. `(*KV).GetMulti` delegate. Green: `go test ./internal/substrate/... -run
   KVGetMulti -v`.
2. `internal/processor/step4_hydrate.go`: one `KVGetMulti` call over the union of the three declared
   lists, feeding all three loops from the shared snapshot. Green: `go test
   ./internal/processor/... -run TestHydrate -v` (existing tests, unchanged assertions).
3. `internal/refractor/personalinterest/interest.go`: `IsRelevant` via `KV.GetMulti`. Green: `go
   test ./internal/refractor/personalinterest/... -v` (existing tests, unchanged assertions).
4. Docs: `docs/vendors.md` version-gate row; `docs/components/substrate.md` KV-operations table +
   file map. Green: `go run scripts/lint-conventions.go` (doc-adjacent conventions), manual read.
5. Whole-tree gates (§ "Exit gates" below).

**In-scope gotchas** (touched components' dossier entries, copied in per the template):

- `docs/components/substrate.md` dossier (3 entries, cap 12): narrowing a consumer's filter strands
  its pending set (not touched here — no filter narrowing); the batch CAS is per-subject (not
  touched — no `AtomicBatch` use); the retired embedded-NATS-fixture entry (already mechanized —
  this fire's new tests use `internal/natsfixture` per the retired entry's gate).
- Standing checklist (`agents/fire-brief-template.md` part 5): #1 new-state-needs-a-lifetime — N/A,
  this primitive holds no state across calls. #2 every census is a premise — the "~12-file corpus"
  premise was re-run live this fire and found to be ~50 files / ~85 call sites (§14.3's estimate
  undercounted); resolved below, before the first edit, per the scope-diff gate. #3 a negative test
  needs its positive vector proven first — the 413-fallback test proves the fast path first (seed
  <1,024, assert fast-path values), then seeds past it. #5 one deterministic key, one writer — N/A,
  read-only primitive.

**Scope-diff gate result — the corpus count is falsified, scope narrowed:**

The design's "~12-file `ListKeysPrefix`-class corpus" is a premise, re-run live this fire (a
dedicated scout grep across the tree): the real count is **~50 files / ~85 call sites** spanning
`cmd/loupe/*` (12 files), all four Verticals-owned P5 apps (`wellness-app`/`cafe-app`/
`clinic-app`/`loftspace-app`, ~30 sites), `internal/pkgmgr` (10 sites), `internal/weaver`/
`internal/loom` state scans, the rule-engine's whole-type anchor scans, and others — of which
roughly 40-50 are genuine list-then-get-each conversion candidates (the rest only need key names,
not values, and gain nothing from batching). This is an L-sized cross-cutting sweep in its own
right, undercounted at design time, not the "~12-file" S-M-compatible tail the row's sizing
assumed — and a large fraction is Verticals-stream-owned code (`cmd/<x>-app`), not a Lattice-stream
mechanical mirror.

Per the scope-diff gate (`agents/fire-brief-template.md` §"before the first edit": "the brief may
narrow to the ratified scope; it may never widen it"), **this fire narrows to the substrate
primitive plus its two most concretely-specified consumers**: step-4 hydration (exact file:line
citations in the design, Lattice/Core, fixes a real atomicity gap) and `personalinterest.IsRelevant`
(the design's own named exemplar, Lattice/Refractor, small and self-contained). Both are enough to
ship "the primitive + first consumers" per §14.6's own phrase — a primitive proven by zero real
callers is not shipped; a primitive proven by two, in two different owning components, is.

The remaining ~80-site sweep is filed as its own board row (below) rather than rushed through
unreviewed in this fire or silently dropped — each site needs its own list-then-get-vs-keys-only
judgment call and, for the Verticals-owned quarter, arguably belongs to that stream's own steward
run. This is a sizing correction made explicit and acted on, not a deferred question.

**Adjacent finds:**

- The ~85-site enumeration-corpus conversion (above) → filed to `backlog/lattice.md` as its own
  ready row (📋 ready, mechanical, mirrors this fire's `KVGetMulti`/`GetMulti` pattern — see the
  board diff in this fire's commit).
- No other adjacent finds surfaced.

**Non-goals (this fire does not touch):**

- The adjacency `Neighbors` fallback / overflow mark (§15, Shape B) — sequenced after this row by
  the board's own `seq:` note; this fire's `KVGetMulti` is a dependency for it, not an overlap.
- The bulk enumeration-corpus conversion (filed, above).
- Any bucket provisioning change — every consumer bucket here (`core-kv`, `personal-lens-interest`)
  is already `AllowDirect`-eligible by the standard NATS KV default (confirmed live: no
  `AllowDirect: false` anywhere in the tree; today's `kv.Get` on these buckets already rides direct
  get).
- A `fingerprint`/rollup return value — the design's shelved `KVSnapshotPrefix` sketch (§6.3)
  returned one; neither of this fire's two consumers needs it (each entry's own `.Revision`
  suffices), and the future Shape B fallback (§15.3, out of scope here) can derive a fingerprint
  from the returned entries itself. Not built now (YAGNI); revisit if a caller needs it.

## 16. Fire B1 brief (build note, 2026-08-10)

Phase-0 compilation for the Shape B build fire. Scouts: three read-only `haiku` agents over the
adjacency write/read paths, the shipped `KVGetMulti` primitive, and the docs/dossier surface.
Everything below was verified live at compile time; the design's citations were treated as leads.

### 16.1 Scope sentence (verbatim, §15.6)

> **B1 (one Steward fire, after the substrate row ships): §15.1–15.5** + the component-doc rewrite
> (§6.9's scope, adjusted to B) + the `docs/vendors.md` version-gate row. Independently shippable
> and green.

**Green bar (verbatim, §15 "Exit gates (B1)"):** `go build ./...`, `make vet`,
`golangci-lint run ./...`, `make verify-kernel`, full `go test ./...`, and on this host: one
observed latch — mark key present, doc emptied, consumer-info redelivery churn gone, `instanceOf`
fan-out evals green, marked-node `Neighbors` count ≥ 3,919.

### 16.2 Census re-run live — the premise HOLDS, pinned

`nats --context lattice-cli stream subjects KV_core-kv '$KV.core-kv.lnk.>' --names`, endpoint IDs
counted per node (9,702 link subjects total):

| node | degree (live, 2026-08-10) | design said | latches at 3,072? |
|---|---|---|---|
| `pFf8PviwpWugC6kepFf8` (service-template meta) | **3,920** (3,919 in + 1 out) | 3,919 | **yes — the only one** |
| `edu97ixj2CJB6auNi6L4` (identity hub) | **2,337** | 2,335 | no (735 of headroom) |
| third-largest | **602** | — | no |

**§15.2's "exactly one live node latches" is confirmed live.** The gap between #2 and #3 is 3.9×, so
the threshold is not near any cliff.

### 16.3 Verified touch-list (`file:line`, checked live)

| file | what | design cite | verified? |
|---|---|---|---|
| `internal/refractor/subjects/subjects.go:48` | `AdjKey`; **no** `AdjMarkKey`/`AdjPrefix` exists | §15.1 | ✅ |
| `internal/refractor/adjacency/builder.go:76,89` | `Build` → `upsertEdge` CAS loop — the latch branch's home | §15.2 | ✅ |
| `internal/refractor/adjacency/store.go:19` | `Neighbors` single `kv.Get` — the fallback's home | §15.3 | ✅ |
| `internal/refractor/consumer/bootstrap.go:166,223` | the two `Build` call sites; **both `Nak` on any error** | §15.2 riders | ✅ |
| `internal/refractor/consumer/bootstrap.go:201-220` | inline directional-event pair #1 | `EventsForLink` | ✅ |
| `internal/refractor/pipeline/evaluate.go:816-829` | inline pair #2 (link fan-out pre-apply) | `EventsForLink` | ✅ |
| `internal/refractor/pipeline/pipeline.go:2913-2922` | inline pair #3 (plain-link reprojection) | `EventsForLink` | ✅ |
| `internal/substrate/kv_multi.go:168` | `KVGetMulti` — the fallback read's primitive | §15.3 | ✅ |
| `internal/substrate/batch.go:201-219` | `checkBatchSize` — reject-only, **no approach warning** | canary rider | ✅ |
| `internal/refractor/pipeline/evaluate.go:383-435` | `footprintValid` — the fingerprint's only consumer | §15.3 | ✅ |
| `docs/components/refractor.md:515-530` | the adjacency-shape section to rewrite | §6.9 | ✅ |

**Rotted / corrected citations:**

- **`EventsForLink` does not exist.** §14.5's "three duplicated constructors" describes three
  *inline* constructions, not three functions. The census of **three is confirmed** (the rows
  above). The rider creates the helper and folds all three.
- **`docs/vendors.md` already carries the `multi_last` version-gate row** (line 40), landed by the
  substrate fire. **Dependency re-verified and dropped from this fire's scope** — not load-bearing
  for this green bar.
- **`docs/components/refractor.md:515-530` is already wrong today**, independently of this change:
  it documents the key as `adj.<type1>.<id1>.<linkName>` holding a list of `<type2>.<id2>`, while
  the code stores `adj.<nodeId>` → `[]EdgeEntry`. The rewrite fixes the pre-existing defect in the
  same pass (it is inside the section this fire must rewrite anyway).
- **`refractor-adjacency` is `PerKeyTTL:false`** (`internal/bootstrap/platform_buckets.go:84`).
  §6.6's `PerKeyTTL:true` belonged to shelved Shape C; §15's "no provisioning change" is correct and
  binding. `adjmark.<id>` shares the bucket and collides with no `adj.`-prefixed scan (distinct
  prefix token).

### 16.4 Two design defects resolved here, before the first edit (Winston, §0)

**(a) §15.1 vs §14.5 contradict each other on where the mark lives.** §14.5 says the mark is
"stored as the node's `adj.<nodeId>` value (a sentinel doc — no new keyspace)"; §15.1 specifies a
separate `adjmark.<nodeId>` key and argues *why* (an old binary's `Build` unmarshals a sentinel doc
to zero edges and silently un-marks the hub). **§15.1 wins** — the ratification record names §15 as
the build plan, and §15.1's mixed-fleet argument is the sounder one. §14.5's clause is struck below.

**(b) §15.1's in-process mark cache contradicts §14.4's "common-case read unchanged".** The cache is
specified as `map[nodeID]struct{}` — a set of *marked* nodes — so a miss means "not known to be
marked" and costs one extra `Get`. Since almost every node is unmarked, almost every `Neighbors`
call would pay that `Get` **forever**, doubling the 153 µs common-case read the §14.4 table promises
is *unchanged*. Caching the negative instead is not available: a mark set by another instance would
never be observed.

**Resolution: `Neighbors` reads the doc and the mark in ONE `KVGetMulti` call** over
`["adj.<id>", "adjmark.<id>"]` on `refractor-adjacency`. This is the same mechanism (consult the
mark, read the doc) with a correct implementation, not a substituted one:

- **One round trip.** ~~so §14.4's common-case guarantee holds (§14.1 measured multi_last at ~31 µs/key
  against 153 µs for a sequential `Get`)~~ — **struck 2026-08-10 (close review): this misapplied the
  measurement and the guarantee it claimed does not exist.** §14.1's 31 µs is an *amortized per-key*
  figure; the request figures are `kvget` **153 µs** p50 and `multi10` **306 µs** p50, so a 2-key
  request pays roughly the request floor — about 2× a single `Get`. The correct claim is comparative,
  not absolute: batching costs the same as §15.1's two sequential `Get`s and is atomic, and no shape
  that reads a mark can match a bare `kv.Get`. §14.4's row is struck to match.
- **Atomic**, which closes a race two sequential `Get`s would open: a node latching *between* the doc
  read and the mark read returns the just-emptied document with no mark — an empty edge list
  presented as authoritative. That is precisely the silent-wrong answer this item exists to kill.
- **No negative-cache coherence rule needed.** The in-process positive cache survives as a pure
  write-path optimization in `Build` (a mark is monotonic, so a cached positive can never go stale);
  it is never consulted to conclude *absence*.

### 16.5 A third defect: the ratified fingerprint is not derivable from the primitive

§15.3 specifies the marked-node fingerprint as "max sequence over every matched subject, **markers
and soft-tombstones included**". **`KVGetMulti` strips hard-delete markers before returning**
(`kv_multi.go:273,308-313,461,488-493` — `parseDirectGetEntry` reports `isMarker` and the caller
drops those entries). Markers are therefore unavailable to any consumer of this primitive, and a
max-sequence fingerprint over the surviving live set **misses a hard delete** whenever the deleted
subject was not the maximum.

**Resolution: the fingerprint is a 64-bit hash over the matched set's `(subject → revision)` pairs**
(order-independent). Strictly stronger than max-sequence — a dropped-out subject changes it — and
sound against its only consumer: `footprintValid` compares the value by **equality**
(`evaluate.go:389,411`), never by ordering, so §15.3's "monotonic" property is not load-bearing.
Note the substrate fire's own non-goals already routed this here: *"the future Shape B fallback
(§15.3 …) can derive a fingerprint from the returned entries itself."*

**Builder obligation:** grep every consumer of `EvalFootprint.EdgeRevisions` and confirm none
requires ordering before landing the hash. Known consumers: `evaluate.go:393`,
`branchmerge.go:289-297`, `full/executor.go:305`.

### 16.6 What `footprintValid` actually does with a marked node (grounding §15.3)

`footprintValid` has **two arms** (`evaluate.go:405-434`), and the ratified text does not distinguish
them:

- **Selector arm** (the §13.4 common path): re-reads `Neighbors` and compares the *matched EdgeID
  set* per `(relation, direction)` selector. **It never touches the revision** — so for a marked
  node this arm is correct on the strength of the fallback read's completeness alone.
- **Fallback arm** (`!hasSelectors || sel.Fallback` — untyped or variable-length hops): compares the
  revision, i.e. the fingerprint. This is the *only* consumer of §16.5's value.

Consequence to accept knowingly: validation re-reads `Neighbors`, so a marked node touched by an
untyped hop pays a **second** fallback read per validated evaluation. Acceptable — one node, and
`fetchEdges` memoizes within an evaluation (`full/executor.go:900-906`).

### 16.7 Precedents to mirror

| edit | precedent (`file:line`) |
|---|---|
| `AdjMarkKey` (validation + panic-on-bad-token) | `subjects.AdjKey`, `subjects/subjects.go:48` + its tests `subjects_test.go:50-67` |
| two-key atomic read | `KVGetMulti` doc contract, `kv_multi.go:123-167`; consumer pattern `internal/processor/step4_hydrate.go` |
| wildcard filters as first-class input | `kv_multi.go:130-142` (the doc's explicit filter contract) |
| link-key parse → endpoints | `substrate.ParseLinkKey`, `internal/substrate/keys/keys.go:113` — **reuse, never re-implement** |
| `EdgeEntry` synthesis (incl. self-link ⇒ both directions) | `processLinkEnvelope`, `consumer/bootstrap.go:201-220` |
| soft-tombstone (`isDeleted`) probe | `consumer/bootstrap.go:191-197` |
| `Term` vs `Nak` classification | `consumer/bootstrap.go:147-164` (the existing `Term` arms) |
| size guard at a write chokepoint | `substrate/batch.go:201-219` (`checkBatchSize`, reject arm) |

### 16.8 Increment order + runnable green checks

1. **Inc 1 — the latch** *(posture-changing → `opus`)*. `subjects.AdjMarkKey`; the two-key atomic
   read helper; `Build`'s latch branch (both arms: `adjOverflowDegree = 3072`, `adjOverflowBytes =
   800 KiB`, consts with a test override); marked-path no-ops on upsert *and* removal; the
   ~~in-process positive cache (write path only, per §16.4b)~~ **— struck 2026-08-10 (close pass):
   no cache shipped at all, the mark is read from KV on every event.**
   `go test ./internal/refractor/adjacency/... ./internal/refractor/subjects/... -count=1`
2. **Inc 2 — the fallback read** *(posture-changing → `opus`)*. `Neighbors`' marked branch: one
   ~~`KVGetMulti`~~ **`KVGetMultiNoSnapshot`** on `core-kv` with both directional filters (`lnk.*.<id>.>` and
   `lnk.*.*.*.*.<id>`); `EdgeEntry` synthesis via `ParseLinkKey`; soft-tombstone drop; the §16.5
   hash fingerprint. **Amended 2026-08-10: this increment introduced a new public substrate primitive
   rather than consuming the existing one, and changed `drainDirectGetFallback`'s stopping condition
   for every caller — see §15.3's amendment and §16.12.**
   `go test ./internal/refractor/adjacency/... ./internal/refractor/pipeline/... -count=1`
3. **Inc 3 — the riders** *(mechanical → `sonnet`)*. `EventsForLink` folding all three inline sites;
   `Build`-error classification at the event boundary (`IsInvalidKeyError` ⇒ `Term`, transport ⇒
   `Nak`); the ½-`max_payload` approach warning at both write chokepoints.
   `go test ./internal/refractor/... ./internal/substrate/... -count=1`
4. **Inc 4 — proof + docs** *(`sonnet`)*. The §15.5 e2e through the real Bootstrapper at a
   test-override threshold; `docs/components/refractor.md:515-530` rewritten to Shape B (fixing the
   pre-existing shape error noted in §16.3).
   `go test ./internal/refractor/... -count=1` · `go build ./...` · `make vet` ·
   `golangci-lint run ./...` · `make verify-kernel` · `STRICT=1 go run ./scripts/lint-conventions.go`
5. **Inc 5 — live latch on this host** *(Winston, inline)*. Rebuild `bin/refractor` from `main`,
   start it against the running stack, observe: `adjmark.pFf8PviwpWugC6kepFf8` present, `adj.` doc
   emptied, `maximum payload exceeded` gone from the log, `lattice-nats` RSS stable,
   `Neighbors` returns ≥ 3,919 edges for the hub.

### 16.9 In-scope gotchas

**Standing checklist** (`agents/fire-brief-template.md`) — #1 and #5 bind hardest here:

1. **New state needs a LIFETIME.** The mark and its cache are new state. State table required, and
   §16.4b already forces part of it (a positive cache is monotonic; absence is never cached).
   Boundaries to answer explicitly: replay (the Bootstrapper re-crosses the threshold
   deterministically), reconnect, tombstone, restart, bucket wipe, mixed-binary window.
2. **Every census is a premise** — done, §16.2, pinned live.
3. **A negative test needs its positive vector proven first** — the latch tests must fail against
   un-latched code; revert-and-watch-it-fail before landing.
4. **Removal needs a transport AND an observer** — the marked path's *removal* arm is a no-op by
   design; the fallback read is what retracts (a soft-tombstoned link leaves the set). Prove that
   with a test, not by argument.
5. **One deterministic key, one writer** — `adjmark.<id>` is written from `Build`, which runs on
   three concurrent paths (bootstrap, fan-out pre-apply, plain-link reprojection). The latch must be
   **idempotent under concurrent create** (§15.5 names this test); use create-then-tolerate-exists,
   never create-only-or-fail.
6. **Precedent may carry debt** — `docs/components/refractor.md`'s adjacency section is a live
   example (§16.3).

**Refractor dossier entries this fire trips** (`docs/components/refractor.md:918-972`, verbatim
subjects): *"New pipeline state without a declared lifetime (registry / latch / armed flag) — reset,
carry, and order it at replay, reconnect, tombstone, and retry, or the review will"* — this fire
adds exactly such a latch; *"One latch guarding two states that commit at different times"* — the
mark and the emptied document commit separately, and §16.4b is the answer to the read side of it;
*"Site censuses derived from key shapes undercount"* — §16.2 derived degree from link subjects, which
is the matcher's own keyspace here, so it does not undercount.

**Substrate dossier** (`docs/components/substrate.md:486-501`): *"The batch CAS is per-subject, not
whole-stream"* — relevant to the latch's concurrency argument.

**Other obligations:** `MERGED ≠ RUNNING` — `bin/refractor` **and** every other binary linking
`internal/refractor/adjacency` must be rebuilt (derive mechanically with the §4 `go list -deps`
loop). No package version bump (no `packages/` edit). No `provision-readpath` (no new lens). No
contract edit — confirmed: the adjacency index appears in Contracts #2 and #6 only as a referenced
implementation detail, with no specified storage shape.

### 16.10 Adjacent finds

- **`internal/refractor/consumer/bootstrap.go:133`** acks any empty-bodied message as a KV tombstone
  *before* classifying the key, so a hard-deleted link never retracts its adjacency edge. Pre-existing,
  outside this fire's scope sentence, and **absorbed into this run's batch as its own unit** if the
  fire's own increments land clean — not filed as a deferral row.
- No other adjacent finds surfaced.

### 16.11 Non-goals (the drift fence)

- **B2** (typed fallback narrowing) — §15.6 gates it on a named trigger that has not fired: the
  identity hub is at 2,337 of 3,072 (§16.2). Dead scaffolding today.
- **Shape C** (per-edge rekey, §3–§13) — shelved, needs a new ratification.
- **Un-marking** a node whose degree later shrinks — §14.5 rules it out.
- **Any bucket provisioning, permission, durable, or contract change** — §15 and §16.3.
- **Health-KV emission** for the adjacency index — none exists today and none is in scope; the §15.2
  mark log line is this fire's only new operator signal.
- **The taxonomy item's Fire C** — sequenced behind this row by its own design doc (§14 item 6).

### 16.12 Close pass — what the four review rounds found, and how it classifies

Four cold reviews ran over this item: three in parallel over the initial build (correctness/capability-plane,
edge-cases/state-lifetime, acceptance/scope) and one cumulative pass over the whole diff after two fix rounds.
All four were read-only and none was the implementer.

**The ratified core mechanism survived every round intact** — the mark key, the batched two-key read, the
fallback enumeration, and the hash fingerprint were each attacked directly and none broke. **Every defect
found was in state the build added around that mechanism, or in the shared substrate the fix rounds reached
for.** That shape is the item's main lesson: the risk was never in the thing the design reasoned hardest
about.

**The two blocking findings.**

1. **A process-global mark cache could outlive the bucket it described.** `Build` short-circuited on it while
   `Neighbors` read KV, so a bucket wipe under a live process (the container is ephemeral; the Refractor now
   *survives* a NATS outage rather than restarting with it, `08ca1f23`) left the write path no-oping every
   rebuild while the read path returned an **empty edge set as authoritative, permanently and silently** —
   the frozen-read failure class again, taking every capability grant derived through the hub. The state
   table had named the boundary and answered it with an environmental assertion. Deleted, not guarded.
2. **The fallback would have reinstated the failure mode on the read side.** A marked node is always past the
   1,024-subject cap, so it always takes the consumer drain, whose double-drain stability check fails when
   *any* of its ~3,900 links moves between passes — on the node that by construction takes writes fastest.
   Resolved by recognizing the atomicity was over-strong for this consumer (§15.3's amendment).

**A defect in shipped substrate, found while fixing the second.** `drainDirectGetFallback` bounded its fetch
by an initial `NumPending`. On a history-1 stream that count is not a reliable stopping target — overwriting
a key erases the message the count was counting and appends a new one, so the counts can balance while a
subject is never observed. The drain now runs until the server reports nothing pending. This was live in
`d8cc803c` and reachable by `personalinterest`'s wildcard read, not only by this fire.

**And a defect the fix round itself introduced**, caught by the close pass: once the drain accumulated across
rounds, a tombstone delivered in a later round no longer retracted an entry collected in an earlier one — a
hard-deleted link returned as live, into the same BFS. **That is the same defect class this fire was fixing
one layer up** (`consumer/bootstrap.go` acking an empty body before classifying the key). Twice in one item,
at two layers, is what makes it worth mechanizing.

| component | design-gap | impl-bug | brief-gap | convention |
|---|---|---|---|---|
| `refractor/adjacency` | 2 | 2 | 1 | 3 |
| `substrate` | 0 | 3 | 0 | 2 |
| `refractor/consumer` | 0 | 0 | 1 | 0 |
| docs / process | 0 | 0 | 0 | 3 |

No finding across four rounds was review-over-reach; all six of the first round's findings landed as real
mechanism changes rather than comment edits. Two findings corrected the **brief** rather than the build:
§16.4b's cost justification misapplied §14.1's amortized per-key figure, and §16.2's census counted link
subjects in Core KV where the latch guard needs entries in the stored document (settled live at Inc 5).

**Promoted to the component dossiers** (`docs/components/{refractor,substrate}.md`): the process-local cache
of durable state without a named wipe/reconnect boundary; the multi-round read that skips tombstones instead
of retracting them; and the vendor-behaviour claim asserted in a comment with no pinned `file:line` behind it
(`kv_multi.go` cites the pinned server source for every constant — the one comment that did not was wrong).

## 17. Enumeration-corpus sweep, Lattice slice (fire brief, build note, 2026-08-15)

Phase-0 compilation for the `backlog/lattice.md` row *"[Perf] Convert the ~85-site `ListKeysPrefix`/
list-then-get corpus to `KVGetMulti`"* (§14.7 filed it; census there: ~50 files / ~85 sites, of which
`cmd/loupe` (12 files) and the four P5 vertical apps (~30 sites) are Verticals/Loupe-owned and out of this
fire — those streams pick their own share). Scout: one read-only `haiku` agent over `internal/pkgmgr`,
`internal/weaver`, `internal/loom`, and the Refractor rule-engine/health probes; every site below re-verified
live (`file:line` read directly) before this brief was written.

### 17.1 Scope sentence (verbatim)

> The Lattice-stream slice of the enumeration-corpus sweep: convert genuine list-then-get-each sites in
> `internal/pkgmgr`, `internal/weaver`, `internal/loom`, and Refractor's health probes to a single
> `KVGetMulti` (or, where the list itself is already subject-filtered and paginated, one `KVGetMulti` per
> page) — mirroring the shipped precedent (`step4_hydrate.go:259`, `personalinterest.IsRelevant` via
> `kv.GetMulti`, `pkgmgr/taxonomy.go:738`, `pkgmgr/installer.go:851`). Read-batching only: no change to
> list scope, filter semantics, error handling for a since-deleted key, or (where present) the
> read-revision-then-conditionally-delete pattern.

### 17.2 Scope-diff gate — two sites narrowed OUT, judgment-call sites named

The scout found 11 TRUE candidates (genuine value-reads, not key-name-only checks). Two are excluded from
this fire — each needs a design judgment call the mechanical mirror doesn't answer, not a mechanical port:

- **`internal/weaver/reconciler.go:155-206` (`sweeper.pass`)** — reads a mark key then its row key
  *sequentially per entity*, and the reconciliation invariant this loop enforces has not been checked
  against a joint-snapshot read (whether the mark and the row must be observed at the same beat, or may
  legitimately straddle two straight-line reads the way today's two independent `KVGet`s do). Runs every
  minute against the whole live mark set — the highest-traffic site in the corpus. Left for its own pass.
- **`internal/refractor/ruleengine/full/executor.go:833-890`** (anchor derivation) — already memoizes
  fetched nodes in `ex.nodes` (`:994`) and exits early on many paths before every listed key is ever
  touched; batching the full listed set up front could *increase* reads relative to the memoized,
  short-circuiting per-key path it would replace, not reduce them. Needs a read-count comparison against
  representative rule shapes, not a mirror of the precedent.

Both are filed to `backlog/lattice.md` as their own row (below), named, not silently dropped.

The remaining **9** sites are mechanical mirrors of the shipped precedent — this fire's scope.

### 17.3 Verified touch-list (`file:line` checked live)

| # | Site | Bucket | Shape |
|---|---|---|---|
| 1 | `internal/pkgmgr/installer.go:1191` `Installer.List` | `core-kv` | Lists all keys, filters to `vtx.package.*.manifest` in Go, `KVGet`s each match in a loop. |
| 2 | `internal/pkgmgr/opmetaretirement.go:62` `cancelOpenTasksForOpMeta` + `:103` `taskIsOpenReferent` | `core-kv` | Per 500-key page from `KVListKeysFilter`, **two** sequential `KVGet`s per referent (the link doc, then the task root). |
| 3 | `internal/weaver/state.go:589` `markStore.scanEffectMismatches` | `WeaverStateBucket` | Lists all keys, `KVGet`s every `__effect` match to parse its confidence window. |
| 4 | `internal/weaver/state.go:708` `markStore.deleteEffectWindows` | `WeaverStateBucket` | Lists all keys, `KVGet`s each `__effect` match under a target prefix **to read its revision**, then `KVDeleteRevision` conditioned on that revision — the conditional-delete-on-fresh-revision must survive the conversion unchanged; only the read side batches. |
| 5 | `internal/weaver/control.go:42` `Engine.seedDisabledTargets` | `WeaverStateBucket` | Lists all keys, reads each `__control` match once at boot to seed the in-memory disabled set. |
| 6 | `internal/loom/state.go:171` `stateStore.listInstances` | loom bucket | Lists all keys, `KVGet`s every `instance.<id>` match. |
| 7 | `internal/loom/state.go:287` `stateStore.pinnedDomains` | loom bucket | Lists all keys, `KVGet`s every `instance.*.patternPin` match. |
| 8 | `internal/loom/health.go:53` `runningInstanceCounter.count` | loom bucket | Lists all keys, `KVGet`s every `instance.<id>` match (mirrors #6, separate heartbeat consumer). |
| 9 | `internal/refractor/health/registry_probe.go:298` `RegistryProbe.declaredLensIDs` | `core-kv` | Lists `vtx.meta.` prefix, then **two** sequential `KVGet`s per candidate (the vertex root, then its `.spec` aspect) — batch the two aspects as two separate `KVGetMulti` calls (they're different key shapes), not one merged call. |

### 17.4 Precedents to mirror

`internal/processor/step4_hydrate.go:230-262` — validate every key's shape *before* the batch call (an
unrecognized string is a subject filter to `KVGetMulti`, not a rejected key — the single-key `KVGet` path
happened to reject `*`/`>` via nats.go's charset check; the batched path won't, so any key built from a
listed key name is already well-formed and safe, but a validation-worthy site should keep validating).
`internal/pkgmgr/installer.go:838-851` (`readMetaDocs`) and `internal/pkgmgr/taxonomy.go:641-738` (chunked
at `abstractGuardReadChunk`) — the two existing pkgmgr `KVGetMulti` call sites in this same file/component,
closest-precedent shape for sites #1 and #2. `KVEntry` carries `.Revision` (confirmed:
`internal/substrate/kv_multi.go`), so site #4's revision-then-delete keeps working off the batched entries.

### 17.5 Increment order + runnable green checks

1. `internal/pkgmgr` (sites 1-2) — `go test ./internal/pkgmgr/...`.
2. `internal/weaver` (sites 3-5) — `go test ./internal/weaver/...`.
3. `internal/loom` (sites 6-8) — `go test ./internal/loom/...`.
4. `internal/refractor/health` (site 9) — `go test ./internal/refractor/health/...`.
5. Full gates: `go build ./...`, `make vet`, `golangci-lint run ./...`, `STRICT=1 go run ./scripts/lint-conventions.go`, `go test ./...`.

### 17.6 In-scope gotchas

- Every site's existing skip-on-`ErrKeyNotFound` / skip-on-malformed-value error posture must be preserved
  exactly — a key absent from a `KVGetMulti` response (hard-deleted or never existed) is the batched
  equivalent of `ErrKeyNotFound` on the single-key path (`kv_multi.go`'s doc: dropped, not surfaced as an
  error) and should be handled the same "skip, don't fail" way each site already handles it today.
- Site #1's `KVListKeys(ctx, CoreBucket)` lists the **whole bucket**, not a `vtx.package.` prefix — that is
  pre-existing and out of this fire's scope (the fire batches the *get* side only; narrowing the *list* side
  is a separate, unscoped improvement — do not fold it in).
- Sites #2 and #9 read two different things per key (a link + a task; a vertex + its `.spec`) — each needs
  its own `KVGetMulti` call over its own key set, not one merged call across two key shapes.

### 17.7 Non-goals (this fire does not touch)

- `internal/weaver/reconciler.go` `sweeper.pass` and `internal/refractor/ruleengine/full/executor.go`
  anchor derivation — filed as their own row (§17.2).
- `cmd/loupe`, the four vertical apps, and the ~30 remaining sites the design's §14.7 census already
  attributed to Verticals/Loupe streams.
- Any change to list scope, filter semantics, or delete-conditioning — read-batching only.
