# The adjacency index orders its edge writes — a per-edge sequence floor on both arms

**Status: ✅ RATIFIED (Winston-adjudicated, in-line per `agents/steward/SKILL.md` §2.5) — 2026-09-07.** No
architectural fork, no frozen-contract change (§0.1). Small, in-component, revertible; the design decision
is which ordering key and where the floor is kept, and both are settled here against the code.
**Component:** Refractor — `internal/refractor/adjacency`, `internal/refractor/consumer`,
`internal/refractor/pipeline`.
**Backlog row:** [lattice.md](../planning-artifacts/backlog/lattice.md) → *Component maintenance* →
*[Refractor] `adjacency.upsertEdge` removes an edge by `EdgeID` with no ordering guard* (★★★, S–M).
**Filed by:** [perentry-unchanged-entry-withholding-design.md](perentry-unchanged-entry-withholding-design.md)
§13 / §2 row 20 / §4.4 (S-wrong); that design's row 7s is the first named victim.

---

## 0. For Andrew (one-look block)

**What it does (two lines).** `adjacency.upsertEdge` applies whatever event it is handed: a removal drops the
entry matching `EdgeID`, an upsert replaces it, neither compares anything. Link keys are deterministic under
Contract #1, so a revoke→re-grant **reuses the `EdgeID`** — and three separate writers index the same link with
no cross-consumer ordering guarantee. This design stamps every entry with the Core KV backing-stream sequence
of the message that wrote it and refuses any write whose sequence is below the floor that `EdgeID` already
carries, on **both** arms.

### 0.1 Fork / contract check — honest answer: neither

- **No fork.** No Gateway, read-path-auth, Vault, multi-cell or HA-NATS surface. The index's read contract is
  unchanged: `Neighbors` / `NeighborsScoped` / `PrefetchNodes` return `doc.Edges` exactly as today (§3.3 is
  what buys that).
- **No contract change.** Contract #1's link-key shape is what the design *relies on* (a link key is its own
  `EdgeID`), not what it edits. No wire shape leaves the process: the sequence is transport-derived and
  `json:"-"` on the event (§3.1), and the two persisted fields are additive with `omitempty`.
- **Two-way door.** An older binary reading a new document ignores the unknown `removals` field and drops
  `seq` when it rewrites — degrading to exactly today's behaviour, never worse (§6).

**The one call that deserves your eye:** the floor for an edge that is *absent* has to live somewhere, or half
the bug stays open (§2.2). It is kept in a **capped per-node map beside `Edges`**, not as a tombstone entry
*inside* `Edges` — which is what keeps every reader, the overflow degree accounting and `edgesWithoutLinkKeys`
untouched (§3.3).

---

## 1. The defect, and why it is ★★★

`upsertEdge` (`internal/refractor/adjacency/builder.go:150-217`) branches on one boolean:

```go
current := st.doc
if remove {
    current.Edges = removeEdge(current.Edges, edge.EdgeID)   // builder.go:290-298 — matches EdgeID, nothing else
} else {
    current.Edges = upsertEntry(current.Edges, edge)         // builder.go:279-287 — matches EdgeID, nothing else
}
```

Neither helper compares anything but `EdgeID`, and under Contract #1 a link key **is** its own `EdgeID`
(`EventsForLink`, `builder.go:104-127`), globally unique **and stable across a revoke→re-grant**. So the
identity that makes the index writable is exactly the identity that makes it reorderable.

**Two independent sources of out-of-order delivery, both live at head:**

1. **Redelivery.** The adjacency durable is configured with no `MaxAckPending`
   (`consumer/bootstrap.go:95-99` — `Stream` / `FilterSubject` / `Durable` only), so the server default
   (~1000) applies and many messages are in flight. `processMsg` Naks on a `Build` error
   (`bootstrap.go:367`); the Nak'd message is redelivered *after* messages that were behind it.
2. **Three concurrent writers, no cross-consumer ordering — the routine case, not the rare one.** The
   dedicated bootstrapper (`consumer/bootstrap.go:411`), the actor-aware link fan-out's pre-apply
   (`pipeline/evaluate.go:1287`) and plain-link reprojection (`pipeline/dispatch.go:296`) all call
   `adjacency.Build` for the same link. Both pipeline sites say so in their own comments
   ("no cross-consumer ordering guarantee"). A lens pipeline running behind the bootstrapper pre-applies its
   own *older* view of a link over the newer one.

**Both directions are reachable, and they fail opposite ways:**

| # | Interleaving | Result today | Plane |
|---|---|---|---|
| D1 | revoke@100 delayed; re-grant@200 applied; revoke@100 arrives | the live edge is **deleted** | **under-grant** — the link is absent to every executor walk, the enumerator, the CDC prefix diff and the derived-anchor retraction walk until the next apply |
| D2 | create@100 delayed; revoke@200 applied (a no-op on an absent entry); create@100 arrives | the revoked edge is **resurrected** | **over-grant** — a revoked relationship reads as live |

D1 is the filed bug (`perentry-…-design.md` §2 row 20, §4.4 S-wrong). D2 is its mirror through the identical
root cause, and it is the **more dangerous direction**: a revocation that does not stick. A guard that closed
only D1 would leave the board row reading "ordering guard shipped" over a live over-grant path, so this fire
closes both. Blast radius of the index either way: 19 lenses and 5 PII tables (the filing design's census).

---

## 2. The two decisions

### 2.1 The ordering key — the Core KV backing-stream sequence

`substrate.Message.Sequence` is the backing-stream sequence (`internal/substrate/consumer.go:95`, populated at
`:491` from `meta.Sequence.Stream`). For a NATS KV bucket the backing stream *is* the bucket, so this number is
also the **KV revision of the link key** — per-key monotone by construction, and the exact quantity "which
version of this link am I looking at" wants.

**It is comparable across all three writers, and this is the design's load-bearing premise — verified live:**

| writer | stream it consumes | site |
|---|---|---|
| adjacency bootstrapper | `subjects.CoreKVStream(coreKVBucket)` | `consumer/bootstrap.go:66`, `:96` |
| every lens pipeline (both link arms) | `subjects.CoreKVStream(coreKVBucket)` | `cmd/refractor/main.go:2458` (`lensConsumerSpec`) |

One stream, one sequence space. Rejected alternatives: a wall-clock stamp (no ordering guarantee across
processes, and the repo's own determinism rule refuses clock-as-correctness); a per-writer counter (not
comparable across the three writers, which is the whole problem); a live Core KV read of the link key per event
(one extra round trip on the hottest path in the index — the cost axis three shipped designs have been
fighting).

### 2.2 Where the floor lives for an ABSENT edge — a capped map beside `Edges`

A stored entry carries its own floor. An **absent** one does not, and D2 is exactly the absent case: the
removal that should have blocked the stale create left nothing behind. So a removal has to record something.

**Chosen: `AdjValue.Removals map[string]uint64` — EdgeID → the sequence of the removal that dropped it —
capped, beside `Edges`.**

Rejected: **a tombstone entry inside `Edges`** (a `Removed bool` field). It is the obvious shape and it is the
expensive one: every reader of `doc.Edges` (`store.go:64` `Neighbors`, `store.go:119` `NeighborsScoped`,
`prefetch.go:135` `PrefetchNodes`) would have to learn to filter it, the overflow **degree** threshold
(`builder.go:174`) would count tombstones toward the latch, and `edgesWithoutLinkKeys` (`builder.go:268-276`)
would classify them. A separate map keeps all four of those untouched — the read path of 19 lenses does not
change at all — and the byte threshold still sees the map's cost, because it measures the marshalled document.

---

## 3. The mechanism

### 3.1 The sequence reaches `Build` as transport data, never as payload

```go
type CoreKVEvent struct {
    …
    // Seq is the backing-stream sequence of the Core KV message this event was
    // derived from … `json:"-"`: transport-derived, never wire-carried.
    Seq uint64 `json:"-"`
}
```

`json:"-"` is not cosmetic. The legacy non-link arm (`consumer/bootstrap.go:340-358`) unmarshals a
`CoreKVEvent` straight out of a message **body**; a wire-visible field would let the body choose its own
ordering floor. The tag makes that unrepresentable, and the arm assigns `evt.Seq = msg.Sequence` after the
unmarshal.

`EventsForLink` takes the sequence as a parameter and stamps both directional events. Three production call
sites supply `msg.Sequence`; all three have it in scope:

| site | how it gets there |
|---|---|
| `consumer/bootstrap.go:411` (`processLinkEnvelope`) | `msg substrate.Message` parameter |
| `pipeline/dispatch.go:296` (`evalPlainLinkReprojection`) | `msg substrate.Message` parameter |
| `pipeline/evaluate.go:1287` (`evaluateLinkFanOut`) | new `seq uint64` parameter, threaded from `evalLinkFanOut`'s own `msg` (`dispatch.go:192`) |

### 3.2 The floor, and the `>=` that makes this backward-compatible

```
floor(EdgeID) = max( stored entry's Seq (0 if absent), Removals[EdgeID] (0 if absent) )
apply iff evt.Seq >= floor(EdgeID)
```

**`>=`, not `>`, and that is the whole compatibility story.** An unsequenced event (`Seq == 0`) meets a floor of
0 and applies — so every path that does not yet carry a sequence, every legacy document written before this
change, and every existing test that constructs a bare `CoreKVEvent{…}` literal behaves **bit-identically to
today**. The guard only ever engages between two events that both carry real sequences, which is precisely the
population it reasons about. Equal sequences are the same message redelivered; applying it is idempotent.

A removal records `Removals[EdgeID] = Seq` **only when `Seq > 0`.** An unsequenced removal stays a pure drop,
exactly as today: an event with no ordering information has no ordering claim to record, and recording 0 would
be indistinguishable from "no floor". This confines every byte of new state to the sequenced paths.

A successful upsert **clears** `Removals[EdgeID]` — the entry's own `Seq` is the floor from then on, and
carrying both would double-count.

### 3.3 Bounding the map

`Removals` is capped at `maxRemovalFloors = 128` per node; on overflow the **lowest** sequences are dropped
first, because a floor's only job is to refuse a *staler* event and the stalest floors are the ones whose
racing events can no longer be in flight (redelivery is bounded by `AckWait × MaxDeliver`; a lens pipeline's
lag is bounded by its own consumer). Dropping a floor re-opens D2 for that one edge only, and only after 128
further removals on the same node — a strictly smaller hole than the unbounded one it replaces, and a bounded
document is the non-negotiable half (`latch` exists because unbounded documents are how this index fails).

### 3.4 State table for `Removals` (standing checklist #1 — a lifetime, not a data structure)

| boundary | behaviour |
|---|---|
| created | first **sequenced** removal of an `EdgeID` on that node |
| cleared | a later applied upsert of that `EdgeID` (§3.2) |
| carried | inside the node document; re-read from KV on **every** CAS retry pass, so a concurrent writer's floors are never clobbered |
| ordered by | the shared Core KV backing-stream sequence (§2.1) |
| crash / replay | durable in the adjacency bucket; a replay re-presents the same sequences and the guard makes re-application idempotent |
| bucket rebuild | starts from an absent document — every floor is 0, the replay applies in stream order, and the end state is the same as a fresh index |
| overflow latch | unreachable: `upsertEdge` returns at `st.marked` (`builder.go:158`) before any of this; `latch` empties `Edges` **and** `Removals` together |
| upgrade / downgrade | old→new: floors absent, read as 0, everything applies (today's behaviour). new→old: `removals` is an unknown JSON field an old binary ignores and drops on rewrite — degrades to today, never worse |

---

## 4. What this does NOT close

- **A pruned floor** (§3.3) — bounded, stated, and strictly better than head.
- **An overflow-marked node** — it keeps no document at all, and its reads enumerate Core KV live
  (`neighborsFromCoreKV`, `store.go:257-320`), which is authoritative and needs no ordering guard.
- **The `Reproject` presence refusals** of `perentry-unchanged-entry-withholding-design.md` §4.4 — those stay
  as they are. This design removes the S-wrong *cause* they were written to survive; it does not remove them,
  and the filing design's row 7s is narrowed rather than deleted.

---

## 5. Green bar

```sh
go test ./internal/refractor/adjacency/... -count=1
go test ./internal/refractor/consumer/... ./internal/refractor/pipeline/... -count=1
go build ./... && make vet && golangci-lint run ./... && STRICT=1 go run ./scripts/lint-conventions.go
go test ./... -p 4            # with POSTGRES_TEST_DSN set (REMOTE.md §3)
```

New pins, each **revert-proved** (standing checklist #3 — the guard is removed and the test must red):

| test | pins |
|---|---|
| `TestBuild_StaleRemovalCannotDeleteALiveEdge` | D1: create@200 then remove@100 ⇒ edge survives |
| `TestBuild_StaleCreateCannotResurrectARemovedEdge` | D2: remove@200 (edge absent) then create@100 ⇒ edge stays absent |
| `TestBuild_NewerRemovalAndNewerCreateStillApply` | the positive vector for both — @300 over a @200 floor applies on each arm |
| `TestBuild_UnsequencedEventsBehaveExactlyAsBefore` | `Seq == 0` on both arms is today's behaviour, and records no floor |
| `TestBuild_RemovalFloorsAreCappedAndDropTheStalest` | `Removals` never exceeds `maxRemovalFloors`; the survivors are the highest sequences |
| `TestEventsForLink_StampsTheSequenceOnBothDirections` | the stamp reaches both directional events |
| `TestBuild_UpsertClearsTheRemovalFloor` | a floor does not outlive the entry that supersedes it |
| `TestCoreKVEvent_SequenceIsNotWireCarried` | `json:"-"` — a message body cannot choose its own floor |

---

## 6. Rollout

Deploy-order-free and revertible in both directions. The index self-heals into the guarded steady state as
events arrive: an entry written before this change has `Seq == 0` and is superseded by the first real event
that touches it. No rebuild, no migration, no package version bump (nothing under `packages/` changes).

---

## Ordering-guard fire brief (build note, 2026-09-07)

**1. Scope sentence** (verbatim, board row): *`adjacency.upsertEdge` removes an edge by `EdgeID` with no
ordering guard … Fix: seq-guard removal + upsert per edge.* Green bar = §5.

**2. Verified touch-list** (every anchor re-checked live, 2026-09-07):

| file:line | edit |
|---|---|
| `internal/refractor/adjacency/builder.go:24-31` | `EdgeEntry` `+ Seq uint64 \`json:"seq,omitempty"\`` |
| `internal/refractor/adjacency/builder.go:34-36` | `AdjValue` `+ Removals map[string]uint64 \`json:"removals,omitempty"\`` |
| `internal/refractor/adjacency/builder.go:64-73` | `CoreKVEvent` `+ Seq uint64 \`json:"-"\`` |
| `internal/refractor/adjacency/builder.go:104-127` | `EventsForLink` takes `seq uint64`, stamps both events |
| `internal/refractor/adjacency/builder.go:138-148` | `Build` carries `evt.Seq` onto the `EdgeEntry` |
| `internal/refractor/adjacency/builder.go:150-217` | `upsertEdge`: floor computed per CAS pass; both arms guarded |
| `internal/refractor/adjacency/builder.go:254` | `latch` empties `Removals` with `Edges` |
| `internal/refractor/adjacency/builder.go:279-298` | `upsertEntry` / `removeEdge` become floor-aware |
| `internal/refractor/consumer/bootstrap.go:340-358` | legacy arm: `evt.Seq = msg.Sequence` after unmarshal |
| `internal/refractor/consumer/bootstrap.go:411` | `EventsForLink(…, msg.Sequence)` |
| `internal/refractor/pipeline/dispatch.go:192-204` | `evalLinkFanOut` passes `msg.Sequence` down |
| `internal/refractor/pipeline/dispatch.go:296` | `EventsForLink(…, msg.Sequence)` |
| `internal/refractor/pipeline/evaluate.go:1276`, `:1287` | `evaluateLinkFanOut(…, seq uint64)`; stamps its pre-apply |

**3. Precedents to mirror.** The monotone-guard idiom is `internal/refractor/adapter`'s `SeqGuarded`
grant-writer — a `projectionSeq` on every write, the stale write **declined** rather than erroring
(`read_path_adapters.go:110-120`, `:173-212`, `DeclinedByWatermark`). This is that idiom applied per edge
instead of per grant row. The test-fixture precedent is `builder_test.go`'s `startKVs(t)` + `natsfixture`;
the shape precedent for an ordering pin is `TestBuild_UpsertReplacesExistingEdge`.

**4. Increment order.** (1) `adjacency` — fields, `EventsForLink` signature, the guard, `latch`, and the eight
pins; `go test ./internal/refractor/adjacency/... -count=1`. (2) thread the sequence through the three call
sites + their test callers; `go test ./internal/refractor/consumer/... ./internal/refractor/pipeline/...
-count=1`. (3) full gates (§5).

**5. In-scope gotchas.** No `// Story …` / "was:" comments (CLAUDE.md). Deterministic sync only — no
`time.Sleep`; ordering in the pins is driven by explicit sequence arguments, not timing. `natsfixture` only.
Nothing under `packages/` changes, so no manifest/`Version` bump. Build-tagged harnesses: `EventsForLink`'s
signature change reaches any tagged test that calls it — enumerate and run them.
**Refractor dossier entries this fire is built against:** *a removal verdict's premises are the whole
mechanism — check the probed artifact, not the precedent's shape* (here: the floor is the premise, and §3.4
enumerates its lifetime rather than inheriting the grant-writer's); *a soundness claim's stated REASON is
load-bearing* (§3.2's `>=` is load-bearing for compatibility — state the direction: `>` would make every
unsequenced path stop applying, failing toward a silently shrinking index); *an index whose entries are read
from one place and gated from another must agree about absence* (§2.2 is exactly that — an absent entry and a
removed one must not be the same answer, which is why `Removals` exists). Standing checklist #1 → §3.4; #3 →
§5's revert-proof column; #4 → §4; #5 → the three writers are arbitrated by the sequence, which is the
explicit arbitration a deterministic key with three writers requires.

**6. Adjacent finds.** D2 (§1) — absorbed into this fire, same root cause, same code. Nothing else surfaced.

**7. Non-goals.** No reader changes (`Neighbors` / `NeighborsScoped` / `PrefetchNodes` untouched — §2.2 is what
buys that). No change to the overflow latch policy, to `neighborsFromCoreKV`, to `Reproject`'s §4.4 refusals,
or to any consumer configuration (`MaxAckPending` stays unset — this design orders the writes rather than
serialising the delivery).

**Scope-diff gate.** Every touch in part 2 traces to "seq-guard removal + upsert per edge": the fields and
`EventsForLink` carry the sequence, `upsertEdge` is the guard, `Removals` is the guard's floor for the absent
case, `latch` keeps the new state consistent with the old, and the three call sites are what make the guard
non-trivial. No adjacent mechanism substituted; no widening. Premise re-verified both ways: the "one shared
stream" claim (§2.1) is re-read live at `consumer/bootstrap.go:66` and `cmd/refractor/main.go:2458` — it is
load-bearing, and it holds.
