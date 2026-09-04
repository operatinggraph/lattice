# Design — `loom-state`: a delete leaves nothing behind

**Status: ✅ RATIFIED (Winston-adjudicated, per the 2026-08-20 delegation) — 2026-09-03.** No
architectural fork, no frozen-contract change (§7 proves both). Adversarial gate run 2026-09-03 and folded into the body (§13: 0 blocking, 6 major, 9 minor).
Build: one Lattice fire (§12), driven by the Steward in the same session per Andrew's instruction.

Board row: `[Loom/Substrate] loom-state's delete tombstones are five sixths of its growth — sweep them`
(`_bmad-output/planning-artifacts/backlog/lattice.md`). Parent design and the row's origin:
[`loom-instance-enumeration-bounding-design.md`](loom-instance-enumeration-bounding-design.md) §8 alternative 9,
§9 (Andrew, 2026-09-02).

## For Andrew

**What it does, in two lines.** Loom's four ephemeral sub-keys (`instance.<id>.pattern`, `token.*`,
`outbox.*`, `deadline.*`) stop leaving a permanent DEL tombstone when Loom removes them: the removal becomes a
**TTL'd purge marker** — a publish Loom already holds, on the bucket it already owns — and the server drops the
subject entirely once the marker expires. The 61,731 tombstones already sitting in the live bucket are converted
to the same shape once, at Loom's next start, off the startup path, one CAS'd publish per marker.

**The cursor stays.** `instance.<id>` is never deleted today and is not touched by anything here — the sweep is
sound by construction, not by argument: it only ever acts on a subject whose last message is a DEL marker.

**No fork, no contract change.** Contract #10's *"a completed instance's record is permanent"* clause is
about the cursor; this design builds to it (§7). Which header Loom's delete carries is mechanism.

**One defect found on the way, fixed in the same fire (§3 Inc 2).** `redrive` cannot resume an instance that
failed by a step deadline: the failed step's token subject carries a DEL marker and the resumed step re-derives
the *same* deterministic token and writes it `CreateOnly` — refused, in production, whenever the resumed cursor
is the failed one (the common case; a guard-skip that moves the cursor is the only escape). The shipped
happy-path test never wrote the token it "deletes" (dossier #2's shape, on the next subject over). Left as is,
this design would have made redrive succeed or fail depending on how long ago the instance failed.

**One hazard found on the way, filed, not folded (§11).** The bucket's marker TTL of 1 s is also the delivery
window of the deadline signal Loom's §10.6 recovery depends on.

## 0. The ask, verbatim, clause by clause

The row records Andrew's decision from the parent design's ratification (2026-09-02): *"cursor alone is fine
once tombstones are swept"*, carried onto the board as *"Andrew, 2026-09-02: the cursor stays, the tombstones
go."*

| Clause | Where it is answered |
|---|---|
| *"the cursor stays"* | §3 — no increment reads, writes or expires `instance.<id>`; §6's census pins that Loom has exactly zero delete sites on it; the lint gate (§3 Inc 4) refuses a future one by construction (a bare delete of any kind in `internal/loom`). |
| *"the tombstones go"* | §3 Inc 1 (no new ones are made) + Inc 3 (the existing 61,731 are converted and expire). |
| *"once tombstones are swept"* — the parent's §9 ceiling arithmetic | §2.3: after this fire an instance costs **one** permanent subject, the cursor, and ~200 B; the 100,000-subject `StreamInfo` detail cap is then ~88,000 instances away at the ledger's real growth, not 4 days. |

The row's `no-pattern:` names *"a per-key purge on a protected bucket whose owner is denied stream-admin."*
§1.3 re-derives the need: the owner-denied admin verb is real, and it is not the primitive the sweep needs.

## 1. Grounding — what a delete is on this bucket, and who pays for it

### 1.1 The mechanism

`loom-state` is provisioned `max_msgs_per_subject=1`, `max_age=0`, `max_bytes=-1`, `allow_msg_ttl=true`,
`allow_rollup_hdrs=true`, `subject_delete_marker_ttl=1s` (`internal/bootstrap/platform_buckets.go:59-63`,
`primordial.go:117-121`; confirmed live 2026-09-03 with `nats stream info KV_loom-state`). Every Loom removal
is one of two shapes, and both leave a **permanent** message:

- inside a transition batch, `substrate.BatchOp{Delete: true}` renders an empty body with `KV-Operation: DEL`
  and nothing else (`internal/substrate/batch.go:141-148`);
- outside a batch, `Conn.KVDelete` → `nats.go` `kvs.Delete`, the same DEL marker (`jetstream/kv.go:1157`).

A DEL marker is a message. On a `max_msgs_per_subject=1` stream it evicts the value it replaces and then
occupies the subject itself, forever: no limit ages it, no `MaxAge` discards it, and nothing in this tree
sweeps it (`grep -rn PurgeDeletes --include='*.go' .` → zero production hits, re-run 2026-09-03).

The **other** removal shape on the same bucket already leaves nothing. A `deadline.<id>` mark carries a
`Nats-TTL`; when it expires as the subject's last message the server writes a marker of its own — `Nats-Marker-
Reason: MaxAge`, `Nats-Rollup: sub`, and `Nats-TTL` = the bucket's 1 s (`nats-server` v2.14.0
`server/filestore.go:6948-6963`) — and when *that* marker expires it is not re-marked, because
`isSubjectDeleteMarker` is true for it (`server/sdm.go:42-44`, `filestore.go:6827`). The subject vanishes. That
is why the census below shows **zero** tombstones from deadline *expiries* and 12,346 from deadline *deletes*:
the platform already has a removal that leaves nothing, on this bucket, on this key family. This design makes
Loom's explicit removals take the same shape.

### 1.2 The live census (2026-09-03, `nats:2.14-alpine`, stack up 8 days)

| Family | Subjects | Live keys | DEL tombstones | Who writes the delete |
|---|---|---|---|---|
| `instance.<id>` (the cursor) | 12,347 | 12,347 | **0** | nobody — never deleted |
| `instance.<id>.pattern` (the pin) | 12,347 | 1 | 12,346 | `transition`, terminal arm (`state.go:406-410`) |
| `token.*` (reverse pointer) | 12,347 | 1 | 12,346 | `transition` old-token arm (`:431-435`); `deleteToken` (`:568`) |
| `outbox.*` (command outbox) | 24,693 | 0 | 24,693 | the relay, on publish-ack (`actuator.go:124`) |
| `deadline.*` (step clock) | 12,346 | 0 | 12,346 | `transition` terminal arm (`:466-470`); `disarmDeadline` (`:556`) |
| **Total** | **74,080** | **12,349** | **61,731 (83.3%)** | |

13 MiB, `Deleted Messages: 86,427` (interior evicted slots). Derivation: §6 rows 1–2. Every tombstone sampled
is a `DELETE` operation (`nats kv history`, §6 row 3) — none is a `PURGE`.

**The growth premise moved, and the correction matters for urgency, not for the design.** The parent
measured ~1,000 instances/day and dated the 100,000-subject `JSMaxSubjectDetails` cap at 2026-09-05. The
cursor count is 12,339 → 12,347 in two days: **+8**. The thousand-a-day was the lease-signing background-check
re-check loop the same day's fire retired (`7e2ef6b2`, 2026-09-03). At the real rate the cap is years away;
at the *next* runaway it is days away again, and a runaway is exactly when an operator needs `nats stream
subjects` to be complete. The design stands on the mechanism, not on the date.

### 1.3 Re-deriving the row's `no-pattern:`

The row names *"a per-key purge on a protected bucket whose owner is denied stream-admin."* Both halves are
true and the conjunction is a false constraint:

- **Denied, yes.** `internal/natsperm/matrix.go:77-87` puts `$JS.API.STREAM.PURGE.KV_loom-state` on every
  non-bootstrap component's deny list, **Loom included** (`Deny`, `:373-384`, applies `protectedStreamDenies`
  matrix-wide; only `bootstrap` returns nil at `:374-376`). `nats.go`'s `PurgeDeletes` is that verb, once per
  marker (`jetstream/kv.go:1499-1551`, `kv.stream.Purge` with a subject filter). Loom cannot call it.
- **Not needed.** A KV purge is a **publish**: `KV-Operation: PURGE` + `Nats-Rollup: sub` on the key's own
  subject (`jetstream/kv.go:1153-1155`), and Loom holds `$KV.loom-state.>` publish as the bucket's owner. A
  purge may carry `Nats-TTL` (`:1160-1161` — `PurgeTTL`, which a plain delete refuses with
  `ErrTTLOnDeleteNotSupported`) and `Nats-Expected-Last-Subject-Sequence` (`:1166-1168`, `LastRevision`). The
  server accepts the combination on this bucket: rollups allowed, TTL allowed, no `DenyPurge`
  (`jetstream_batching.go:886-898`; `stream.go:6889-6898`, where the marker-TTL floor clamp is skipped for
  `MaxMsgsPer == 1`).

The primitive the sweep needs is therefore *"a purge with a TTL, in and out of a batch, revision-conditioned"*
— a header shape the substrate does not yet spell but the client and server both already support. Substrate
work, ~40 lines, no permission surface. (Andrew, 2026-08-09, filed as the standing lesson: *per-key KV purge is
a publish; TTL'd markers beat `PurgeDeletes`.*)

## 2. What the tombstone population costs — every consumer, priced

| Consumer of the subject set | Today (61,731 tombstones delivered) | After |
|---|---|---|
| `pinnedDomains` — whole-bucket `KVListKeys`, once per terminal instance (`state.go:334`; `reconcileConsumers` from both terminal arms) | 74,080 header-only messages per call | 12,349 + transients |
| `listInstances` — whole-bucket `KVListKeys` (`state.go:206`; Loupe's Flows tab, `lattice loom list`) | 74,080 per call | 12,349 + transients |
| `runningInstanceCounter` — `instance.>` prefix listing every 10 s (`health.go:101`) | 24,693 (cursors + pin tombstones) | 12,348 |
| The `loom-deadline` durable, `DeliverAll` (`engine.go:348-360`) — a **rebuilt** durable replays every retained `deadline.*` message | 12,346 markers replayed, each a no-op probe | 0 |
| `StreamInfo` subject details (`nats stream subjects`, Loupe's stream views) | capped at `JSMaxSubjectDetails = 100_000` (`server/jetstream_api.go:435`) | headroom ≈ 88,000 instances |
| The stream's in-memory subject index + file storage | ~6 subjects / ~1.1 KB per instance | 1 subject / ~200 B per instance (+ transients for the running set) |

The parent design's Inc 1–2 (ratified, sequenced behind a Loupe row) narrow the first two rows by *filtering*;
this design narrows every row by making the population it filters small. The two compose — after both, a
listing is bounded by the running set from both ends — and neither depends on the other.

**"After"** for a listing is *live keys + transients*: a TTL'd purge marker is still a subject for `tombstoneTTL`
(§3.1) and a subject-filtered listing still receives it (`ignoreDeletes` is client-side, `jetstream/kv.go:1280`).
At one minute and the ledger's observed rates that is single digits; at 1,000 instances/day it is ~3.

### 2.3 The ceiling, re-read with the sweep in place

The parent's §9 fork weighed a cursor-only ledger against the primitive that could prune it, and read the
stake as *"~6 subjects per instance, forever"*. With five of the six gone the permanent trace is exactly the
cursor: one subject, 195 B measured. The `JSMaxSubjectDetails` cap becomes a bound on **instances ever
created**, ~100,000, and the `ListInstances` `max_payload` wall the parent's Inc 2 removes is the only dated
bound left. The deferred Weaver-side primitive (parent §9) keeps its revive trigger unchanged; nothing here
brings it nearer or pushes it away.

## 3. The shape — four increments, one fire

### 3.1 Inc 1 — the removal is a TTL'd purge (substrate + Loom's six delete sites)

**Substrate.** `BatchOp` gains `Purge bool`, rendered as `KV-Operation: PURGE` + `Nats-Rollup: sub`, honoring
the existing `TTL` and `HasRevision/Revision` fields; `Delete` and `Purge` are mutually exclusive
(`AtomicBatch` refuses both set — a pre-flight error, never a NATS rejection, the `checkBatchSize` posture);
`CreateOnly` is meaningless for a purge and is ignored exactly as it is for a delete (`batch.go:64-69`), and a
purge carries no body, so it is exempt from the value-size check the way a delete is (`:208-211`).
And one non-batch method, `KVPurgeWithTTL(ctx, bucket, key, ttl, expectedRevision)`, wrapping
`kv.Purge(ctx, key, jetstream.PurgeTTL(ttl), jetstream.LastRevision(rev))` with `expectedRevision == 0`
meaning unconditioned. Nothing else in the substrate changes: `KVDelete`, `KVDeleteRevision`, `KVPurge` keep
their callers and their semantics.

Why the header shape is safe inside Loom's transition batch: the server permits a subject rollup on the
**first occurrence of the subject in the batch** (`jetstream_batching.go:893-898`) and evaluates the
per-subject expected-sequence check on the same message (`:741-800`). A transition batch names each subject at
most once — `instance.<id>`, at most one of the pin, `token.<new>`, `token.<old>` (guarded `oldToken !=
newToken`, `state.go:429`), `outbox.<req>`, and exactly one `deadline.<id>` op (`:447-471`).

**Loom.** Every removal becomes a purge carrying `tombstoneTTL`:

| Site | Today | After |
|---|---|---|
| `transition` terminal arm, the pin (`state.go:406-410`) | `Delete: true` | `Purge: true, TTL: tombstoneTTL` |
| `transition` old-token arm (`:431-435`) | `Delete: true` | same |
| `transition` terminal arm, the deadline (`:466-470`) | `Delete: true` | same |
| `disarmDeadline` (`:556`) | `KVDelete` | `KVPurgeWithTTL(…, tombstoneTTL, 0)` |
| `deleteToken` (`:568`) | `KVDelete` | same |
| the relay's outbox delete (`actuator.go:124`) | `KVDelete` | same |

`tombstoneTTL = time.Minute`, a package constant with its reasoning in the doc comment. Any value ≥ 1 s is
correct (the server's floor, `server/stream.go:5346-5348`; seconds granularity); nothing reads a marker for
its own sake (§4), so the minimum would do. One minute is chosen so that `nats kv history` on a key an operator
is looking at *right now* still shows the removal that just happened, and it costs single-digit transient
subjects (§2). It is deliberately **not** a `Config` field: there is no operational reason to tune it, and a
knob invites the belief that some consumer depends on the value.

**What the server does with it, cited.** The purge marker rolls up the subject's prior message (the value, or
a legacy DEL marker) and is stored with a TTL. A rollup is not free on the server side the way a DEL is: every
`Nats-Rollup: sub` publish runs the stream's purge path for that one subject (`server/stream.go:6976-6977` →
`purgeLocked` with `Keep: 1`, `:7055-7061`), which then visits **every consumer on `KV_loom-state`** and walks
that consumer's pending map, dropping entries whose message is gone (`stream.go:2864-2899`;
`consumer.go:6370-6440`). The two Loom durables (`loom-deadline`, `loom-outbox-relay`) and any live KV watcher
pay that walk once per removal — proportional to their *pending* entries, which on a healthy stack is a
handful — and a pending delivery on a *sibling* subject is untouched (the purge is subject-scoped). This is new
per-transition server work that a DEL never did; Inc 1's fixture pins the sibling-subject survival (§6 row 5),
and §3.3 prices the 61,731-fold instance of it. When it expires it is the subject's last message and it *is* a
subject-delete marker (`sdm.go:42-44`: `KV-Operation: PURGE` qualifies), so no `MaxAge` marker is written in
its place (`filestore.go:6827`, `:6892`). The subject is gone. §6 row 5 pins this end to end in a fixture: a
purge-with-TTL through `AtomicBatch`, then the subject absent from a `STREAM.INFO` subject listing after the TTL.

### 3.2 Inc 2 — `redrive` writes the resumed step's token as a put, under its own CAS

**The defect.** A step's token is `deriveRequestID(instanceID, cursor)` — deterministic, no attempt component
(`token.go:20-22`, `deriveID` `:50-58`). The deadline path fails an instance with `fail(ctx, inst, token, …)`
(`engine.go:1318`, `:1368`, `:1433`, and the pin-missing arms `:789`, `:1284`), whose transition deletes
`token.<token>` (`state.go:431-435`). `RedriveInstance` resumes **at the recorded cursor** (`control.go:340-
370`), so `submitStep` re-derives the identical token and `transition` writes `token.<token>` with
`CreateOnly: true` (`state.go:423-427`) — `Nats-Expected-Last-Subject-Sequence: 0` against a subject whose last
message is a marker. Refused, `err_code=10071`, after `redrive()`'s own batch has already flipped the record
to `running` (`:495-500`): the instance is left `running` with no pending token and no deadline — and a
second `RedriveInstance` refuses it on `status != failed` (`control.go:326`). Every deadline-failed instance
whose resumed cursor is the failed cursor is unredrivable today; `advanceToRunnableStep` runs before
`submitStep` (`control.go:348-352`), so a guard that now skips the failed step lands on a virgin token subject
and escapes — the uncommon case, and the only one. The dossier already records this exact shape for the pin
(`docs/components/loom.md`, dossier #1); the token is the same shape one key over.

**Why no test caught it.** `TestRedriveInstance_HappyPath_ResumesAtCursor` reaches `failed` with
`transition(ctx, inst, "", "", nil, 0)` (`control_internal_test.go:424`) — `oldToken == ""`, so no token
was ever written or deleted, and the resumed step's `CreateOnly` lands on an empty subject. Dossier #2, verbatim,
on the next subject.

**Why it is this fire's.** Inc 1 alone changes the defect from *always* to *until the marker expires*: a
redrive within `tombstoneTTL` of the failure refuses, a later one succeeds. A time-keyed outcome on the one
verb an operator reaches for under pressure is worse than the standing refusal, so the fix ships with the
marker change.

**The fix.** On the redrive path the resumed step's `token.<new>` write is an **unconditional put**; every
other path keeps `CreateOnly`. The guard it leans on already exists and is already documented as the redrive
guard: `redrive()`'s CAS on the instance record (`state.go:483-500`, whose doc comment moved the pin's guard
there for exactly this reason). After that CAS the engine is the only writer of this instance: the instance
was terminal (no deadline armed, `transition` deleted it on the fail arm), the old token is gone (a late
completion resolves nothing and drops, `advance` `:773-776`), and a second concurrent redrive lost the CAS and
returned before `submitStep`. The `CreateOnly` race the comment at `state.go:417-422` guards — two live
advancers deriving the same token — cannot arise on this path because the instance has no live advancer.

**One more writer can reach the window, and it is benign — say so rather than claim exclusivity.** A
redelivered `patternStarted` for the same `instanceId` (Weaver re-dispatches a stable id while its gap is
open; `loom-trigger` is `DeliverAll`) enters `handleTrigger`, whose resume gate is exactly *running with an
empty pending token* (`engine.go:432-443`) — the state `redrive()` leaves between its CAS batch and
`submitStep`. `resumeStepZero` then submits the same step with `CreateOnly`. While the fail arm's marker
stands (or the TTL'd purge marker that replaces it), that write is refused with `10071` → Nak → redelivered,
by which time the redrive's put has landed and the gate reads a pending token → Ack. If the marker has
already expired, the resume path's `CreateOnly` commits first and the redrive's put then rewrites the same
token pointer, cursor and outbox record with identical content; the doubled outbox publish collapses on the
Contract #4 tracker. Either order converges to one identical state. Inc 2's test (d) pins the first order (a
redelivered trigger inside the window is refused and the redrive completes) and the second (both writes
land, one op executes).

Plumbing: `submitStep` and the three `submit*` arms take a `tokenMode` (or equivalent) that `transition`
renders as `CreateOnly: mode != redrive`; `RedriveInstance` is the one caller that passes it. The requestId
stays the same — that is *correct*, not merely tolerated: a rejected op leaves no tracker (Contract #4 §4.4,
*"a failed commit lands no tracker"*), so the re-submitted op executes; an op that did commit collapses as a
`duplicate` and the resumed step's deadline probe then finds the tracker and advances (`engine.go:1298-1305`),
the same self-heal the normal path has.

**Its tests, both real-shaped.** (a) Reach `failed` through `fail()` with a token that was written by a real
`transition` — never `putInstance`, never `oldToken == ""` — assert the token subject carries a marker
(`resolveToken` → absent *and* `nats kv history` / a revision read shows the DEL/PURGE), then `RedriveInstance`
succeeds and `PendingToken` is the re-derived token. **Mutation test:** with `CreateOnly` restored on the
redrive path the same test fails with `10071`. (b) The existing concurrent-redrive CAS test keeps passing
(the loser must not reach `submitStep`). (c) `TestRedriveInstance_HappyPath_ResumesAtCursor` is rewritten to
fail through `fail()` so the happy path is the production shape; the `oldToken == ""` seeding is deleted. (d)
A `patternStarted` redelivered into the redrive window (drive `handleTrigger` directly on the post-`redrive()`
state) is refused on the token while the marker stands and converges after it expires — both orders asserted.

### 3.3 Inc 3 — the legacy residue converts once, at start, off the startup path

**Precedent.** The parent design's Inc 2 backfill, Andrew-ratified 2026-09-02 (§6 there): *"run once off the
engine's startup path (not on it), gated to run only while it has work … idempotent and convergent … one
summary log line; no health issue, no new control verb."* Same bucket, same lifecycle moment (the restart that
deploys the fix), same placement. The posture is **not** identical and the difference is named: the parent's
backfill is *capability-granting* (it only writes an index; nothing it does can be wrong) while this pass is a
mass conditional purge. What makes the automatic form acceptable here is that every one of its writes is
revision-conditioned on a DEL marker — the pass has no verdict to get wrong either, only a narrower
precondition, and a wrong precondition is a `10071` skip rather than a lost key (the outcome table below). The 2026-08-27 doctrine that an automatic O(everything) trigger needs
evidence-of-need at the trigger is honored the level-triggered way: the trigger is the **observed DEL-marker
population**, and once it is zero the per-start check is O(transients) — four filtered metadata listings over
the ephemeral families, a handful of subjects on a healthy stack.

**Enumeration.** A new substrate read, `KVListTombstones(ctx, bucket, filter) ([]KVTombstone, error)` — a
`WatchFiltered(filter, MetaOnly())` **without** `IgnoreDeletes`, drained to the init marker, returning
`{Key, Revision}` for every entry whose operation is **`KeyValueDelete`** (`jetstream/kv.go:1261-1275` decodes
the op from `KV-Operation` / `Nats-Marker-Reason` under `MetaOnly`; `HeadersOnly` keeps the headers). PURGE-op
entries are deliberately *not* returned: they are either this design's own TTL'd markers or the server's
`MaxAge` markers, both already expiring — returning them would make the sweep re-purge its own output every
pass. Four filters: `instance.*.pattern`, `token.>`, `outbox.>`, `deadline.>`. The cursor family is **never
listed** — not filtered out, not listed — so the sweep cannot address it.

A key listing is **count-bounded, not drain-bounded** (`docs/vendors.md`, the NATS row; `jetstream/kv.go:1297`):
a pass can come back short under concurrent rewrites. So the pass is **one pass per start, never a loop within
a start**: each start lists once per family, converts what it listed, and returns; a short listing converts fewer
and the next start converts the rest. Convergence is by restart, the parent's posture, and the check is
level-triggered — a start that lists zero DEL markers does nothing. Between starts the missed markers simply
remain what they are today.

**Conversion.** Per returned marker, `KVPurgeWithTTL(bucket, key, tombstoneTTL, marker.Revision)` — a
revision-conditioned rollup purge on the DEL marker's own sequence. The three outcomes, decided per key:

| State at publish | Outcome |
|---|---|
| Subject's last message is still that DEL marker | converted; expires in `tombstoneTTL` |
| Key was re-created since the listing (a redrive re-put the pin; a step re-armed a deadline), **or** the marker is already gone (another instance's pass; the TTL of an earlier conversion) | `10071` revision mismatch in both cases — the server compares the expected sequence against the subject's current last sequence, which is the new value's or `0` (`jetstream_batching.go:772-778`; `jetstream/kv.go:1166-1168` sets no not-found path) → **skipped**, counted once as `skipped-mismatch`, Debug-logged with the key; a live value is never touched |
| Any other publish error (a timeout, a disconnect) | the pass **stops** — logs the summary so far and returns; the next start resumes from the bucket (§5). Each publish runs under its own bounded context (a few seconds), so a stalled ack cannot hold the goroutine open for the life of the process |

Skip, never refuse the pass: availability of the sweep does not hinge on any one key (the *predicate vs
outcome* discipline).

**Placement and pacing — the pass publishes onto two live durables, and that is the cost that sets its
rate.** `Engine.Start` launches `go e.sweepLegacyTombstones(ctx)` after the consumers are attached and the
first reconcile has run (`engine.go:256-276`) — off the path, cancelled with the engine's context, never
blocking `Start`. Every converted marker is a new empty-body message on its subject, and two of the four
families are the filter subjects of `DeliverAll` durables: `outbox.>` feeds `loom-outbox-relay`
(`engine.go:334-344`), whose handler acks an empty body at once (`actuator.go:87-89`); `deadline.>` feeds
`loom-deadline` (`:348-361`), whose handler runs `onDeadline` — one `KVGet instance.<id>` and a return on
*terminal* (every legacy deadline marker's instance is terminal by construction, the deadline was deleted in
the terminal batch), or for the rare running-but-disarmed instance (a userTask waiting on its human) the
tracker RPC plus a re-disarm that finds the key absent. Unpaced, 12,346 deadline conversions would put a
multi-second backlog on `loom-deadline`, and §11 says a real expiry marker survives one second in the stream:
**the pass must never let that durable lag.** So the two CDC-filtered families are converted at a fixed pace,
`legacySweepRate` ≈ 100 publishes/s (a probe costs ~1 ms, so the durable drains faster than the pass feeds
it and its pending count stays in single digits), and the two families with no consumer (`token.>`,
`instance.*.pattern`) run unpaced. Order: the unpaced families first. Each publish runs under a bounded
per-call context; a publish error stops the pass (the outcome table below). Time: ~37,000 paced publishes ≈
six minutes, ~24,700 unpaced ≈ under a minute. One summary line at the end (`listed / converted /
skipped-mismatch / stopped-at`, per family). No health issue is raised and no control verb is added; the
live close (§12) reads `loom-deadline`'s pending count during the pass, which is the bound this paragraph
claims. A second engine instance running the same pass at the same time is safe: the CAS makes every key
first-writer-wins and the loser's outcome is the second row.

**Cost, stated in the unit that matters.** The four filtered listings deliver 61,731 + 2 header-only messages
on the first pass (§1.2) and ~4 × running thereafter. This is the one enumeration the design keeps large, paid
once at a restart the operator is already performing to deploy the fix — the parent's exact sentence.

### 3.4 Inc 4 — the gate, the docs, the dossier

**Gate (the lint doctrine: the convention ships with its enforcement, blocking, because the migration
leaves zero debt).** `scripts/lint-conventions.go` gains `checkLoomStateDelete`, scoped to
`internal/loom/**` non-test files, default-denying every removal idiom that leaves a permanent subject: a
`BatchOp` literal carrying `Delete: true`; a `BatchOp` literal carrying `Purge: true` **without** a `TTL:` in
the same literal (a TTL-less purge marker never expires and, being a subject-delete marker itself, is never
re-marked — and it is invisible to the conversion pass, which lists DEL ops only); and a `.KVDelete(` /
`.KVDeleteRevision(` / `.KVPurge(` call (`.KVPurgeWithTTL(` does not match `.KVPurge(` — the gate matches the
call token followed by `(`). The finding names the purge-with-TTL shape and this design. No annotation escape:
the design leaves no sanctioned bare removal in the package, and the one legitimate future exception (a
removal that *must* leave a permanent marker) would be a design decision, made by editing the gate with the
reason beside it. Self-test cases: bare `Delete: true` denied; `Purge: true` with `TTL:` passes; `Purge: true`
alone denied; `.KVDelete(` denied; `.KVPurge(` denied; `.KVPurgeWithTTL(` passes; a `_test.go` path is out
of scope (the e2e fixtures clean up with `KVDelete` legitimately, `guard_e2e_test.go:28`).

**Provisioning assertion (Inc 1).** After Inc 1 every terminal, old-token and disarm batch depends on the
bucket allowing rollups, message TTLs and purges — the same class of dependency `AllowAtomicPublish` already
earned an assertion for (`internal/bootstrap/verify.go:252-264`, mirrored in `scripts/verify-kernel.go:303-
310`). Both loops gain `AllowRollup && !DenyPurge && AllowMsgTTL` on `KV_loom-state`, so a bucket that would
refuse every transition fails `verify-kernel` instead of failing Loom.

**Docs and the comments this design falsifies.** `docs/components/loom.md` § *State & crash-safety*: the
*Provisioning + index posture* paragraph gains the removal shape (a removal is a TTL'd purge; the four ephemeral
families leave no subject behind; the cursor is the one permanent subject) and drops its *"(distinct from a
normal DEL)"* — after Inc 1 an ordinary removal also decodes as `KeyValuePurge`, and nothing branches on the
distinction (the handler keys on empty body); the *cursor's lifetime* paragraph gets its cross-reference; the
`redrive` sentence in *Implementation status* records the token-put guard beside the pin one. Three code
comments state the marker-permanence rationale this design removes and are rewritten to the §4 argument:
`createInstance`'s (`state.go:263-269`, *"that DEL marker is permanent, so the subject never accepts a
CreateOnly write again"*), `redrive()`'s (`:484-489`), and dossier #1's *"can never commit again"*
(`loom.md:497-503`) — each becomes *"a CreateOnly against a subject that still carries a marker is refused;
the guard sits on the instance CAS because the pin's marker is present for the marker's lifetime and the
guard must not depend on it"*. `internal/loom/doc.go:41-45` and `state.go:135-139` keep describing the
expiry as a `MaxAge` marker but stop implying that distinguishes it from a removal; `doc.go`'s outbox bullet
says *purges* where it says *deletes*.

**Dossier.** Two entries, the second retiring into the gate:
- *The deterministic step token is dossier #1 one key over: `redrive` re-derives the failed step's token and
  wrote it `CreateOnly` against a subject that carries the fail arm's marker. Check:* the real-shaped redrive
  test (§3.2 (a)).
- *A removal in `loom-state` is a TTL'd purge, never a DEL — a DEL is a permanent subject on a
  `max_msgs_per_subject=1` bucket. Check:* `lint-conventions`' `checkLoomStateDelete` (mechanized at ship; the
  entry records the why, the gate does the catching).

## 4. Consumer table — every reader of a removed key, on every state the key can be in

The state table first, then the predicate each consumer applies. States of one ephemeral subject:
**never-written · live · DEL (legacy) · PURGE+TTL (this design) · expired (subject absent) · re-created after
any of those**.

| Reader | What it does | never | live | DEL | PURGE+TTL | expired | re-created |
|---|---|---|---|---|---|---|---|
| `KVGet` (`resolveToken`, `outboxExists`, `getPinnedPattern`, `disarmDeadline`'s probe) | `nats.go` `Get` maps `ErrKeyDeleted` → `ErrKeyNotFound` for DEL **and** PURGE (`jetstream/kv.go:1004-1011`, `:941-946`) | absent | value | absent | absent | absent | value |
| `KVGetMulti` (`listInstances`, `pinnedDomains`) | direct-get marker parse treats `KV-Operation: DEL\|PURGE` and any `Nats-Marker-Reason` as absent (`kv_multi.go:656-661`, `:875-880`) | absent | value | absent | absent | absent | value |
| `KVListKeys*` (all three listing paths) | `IgnoreDeletes` drops DEL and PURGE ops alike (`jetstream/kv.go:1280`) | — | listed | not listed | not listed | not listed | listed |
| `loom-deadline` handler (`engine.go:1214-1228`) | **empty body ⇒ probe**, else ack; the probe no-ops on a non-running instance or an empty pending token (`:1263-1275`) | — | ack (re-arm PUT) | probe → no-op | probe → no-op | nothing delivered | as live |
| `loom-outbox-relay` (`actuator.go:81-83`) | empty body ⇒ ack | — | relay + purge | ack | ack | nothing delivered | relay |
| `createInstance` pin/cursor `CreateOnly` (`state.go:271-274`) | subject must be **empty** | commits | refuses | refuses | refuses | **commits** | refuses |
| `transition` new-token `CreateOnly` (`:423-427`) | subject must be empty | commits | refuses | refuses | refuses | **commits** | refuses |
| `redrive` pin put (`:493-500`) | unconditional, under the instance CAS | commits | commits | commits | commits | commits | commits |
| `handleTrigger` → `resumeStepZero` (`engine.go:432-443`) on a redelivered `patternStarted` | resumes a running instance with an empty pending token, `CreateOnly` on the step token | commits | refuses → Nak → Ack on redelivery | refuses → Nak → Ack | refuses → Nak → Ack | commits (converges with a redrive's put, §3.2) | refuses |
| `runningInstanceCounter` (`health.go:101-110`) | counts listed pin keys | — | counted | no | no | no | counted |
| `cmd/loupe`'s `weaverArtifactLive` (`weaver.go:1749`) | `KVGet instance.<id>` — the **cursor**, never an ephemeral key | n/a | | | | | |

**The two bold cells are the only behaviour this design changes for a reader, and both are correct.** A
`CreateOnly` that today refuses forever on a once-deleted subject commits again once the subject is genuinely
empty. Who can issue one against a formerly-deleted subject?

- `createInstance`: only for an `instanceId` with **no cursor** (`engine.go`'s trigger path checks the cursor
  first, `state.go:255-262`) — and the cursor is never deleted, so a formerly-live pin subject is never reached
  by this write. Unreachable.
- `transition`'s token: a stale advancer for step *N* after step *N+1* advanced. It re-reads the instance and
  drops on `PendingToken != token` (`engine.go:773-776`) before any write — the cursor is the guard, the
  marker never was. The concurrent-advancers race (`state.go:417-422`) is between two writers of a token that
  is *live* after the winner, which the marker's lifetime does not touch. And the redrive path, the one place a
  once-deleted token subject is legitimately re-targeted, stops using `CreateOnly` in Inc 2 (§3.2) — so it is
  correct on every row of this table, not only *expired*.

`Nats-Marker-Reason` markers the server writes on deadline expiry are unchanged by this design and stay the
§10.6 signal: the handler's predicate is *empty body*, which is what they are.

## 5. State-lifetime table — the new state

Two pieces of state are introduced; neither is a data structure.

| State | Created | Reset / cleared | Carried across | Ordered relative to |
|---|---|---|---|---|
| **A TTL'd purge marker** on an ephemeral subject | by the removal (batch or single publish) | by the server at `tombstoneTTL`; or evicted earlier by a re-create of the key (the value replaces it, `max_msgs_per_subject=1`) | a Loom restart (it is a stream message); a bucket recreation does not carry it (nothing in the bucket is) | the value it removes: the rollup evicts the value in the same store operation — a reader never sees value-after-marker on one subject |
| **The legacy-conversion pass's progress** | nowhere — there is no progress record | — | not carried: each start recomputes the DEL-marker set from the bucket (level-triggered) | a concurrent pass on another instance: per-key CAS, first writer wins, loser skips |

The absence of a progress record is the point: the bucket is the record. A pass interrupted by shutdown
resumes by re-listing. The boundaries the neighbouring state (the cursor, the deadline TTL) already honors —
crash, replay, reconnect, tombstone, upgrade — each collapse to "re-list on start" for the pass and "the
server owns it" for the marker.

## 6. Executable censuses

| Claim | Command | Result 2026-09-03 |
|---|---|---|
| Subject / live / tombstone counts by family | `nats --nkey=deploy/nkeys/lattice.nk stream subjects KV_loom-state --json` ∖ `nats … kv ls loom-state`, classified by prefix (script in the fire's Phase 0) | 74,080 / 12,349 / 61,731; per family as §1.2 |
| Stream posture | `nats … stream info KV_loom-state` | `Allows Per-Message TTL: true · Allows Purge: true · Subject Delete Markers TTL: 1.00s · Allows Rollups: true · Maximum Per Subject: 1` |
| Legacy markers are `DELETE`, not `PURGE` | `nats … kv history loom-state <key>` on one key from each of the pin / outbox / deadline families | all three `DELETE` |
| The cursor has zero delete sites | `grep -n 'instanceKey(' internal/loom/state.go` → every use is a put or a get; `grep -n 'Delete: true\|KVDelete' internal/loom/*.go \| grep -v _test` | six sites, none on `instanceKey`; the six are §3.1's table |
| A purge-with-TTL inside `AtomicBatch` removes the subject (the mechanism pin) | new fixture test in `internal/substrate`: batch `{Purge:true, TTL:1s}` on a written key → `KVGet` absent immediately → after ~2 s the subject is absent from the stream's `STREAM.INFO` subject details (`jetstream.WithSubjectFilter`), and an unacked pending delivery on a sibling `deadline.*` subject is still redelivered afterwards (the §3.1 rollup walk leaves it alone) | **owned by Inc 1** |
| `redrive` is refused today after a real deadline fail | the §3.2 (a) test with `CreateOnly` in place | **owned by Inc 2** (expected: `10071` before, pass after) |
| Nothing sweeps tombstones today | `grep -rn PurgeDeletes --include='*.go' .` | zero production hits |
| Readers of the ephemeral families outside `internal/loom` | `grep -rln 'loom-state\|LoomStateBucket' --include='*.go' . \| grep -v internal/loom` (tests included) | `cmd/loupe/weaver.go` (cursor `KVGet` only), `cmd/loupe/flows.go` (comment; reads the read model), `cmd/loom/main.go:114` (bucket name), `internal/bootstrap/{platform_buckets,primordial,verify}.go` + `scripts/verify-kernel.go:304-306` + `internal/natsperm/matrix.go` (provisioning/permissions), `internal/substrate/publish.go` (comment), three `packages/` doc comments, and `internal/leaseconvergence/harness_test.go:288,764` (a `leaseshortwindow`-tagged harness that constructs a real `Engine` and so runs the conversion pass — on an empty bucket, a no-op). **No functional reader of an ephemeral family outside `internal/loom`.** |
| Every transition batch names a subject at most once | read `transition`, `state.go:388-475` | five distinct key families, `oldToken != newToken` guarded at `:429` |

## 7. Contract surface — none

The row's parent already made the promise this design serves: Contract #10 (`10-orchestration-substrate.md`
§ *`loom-state` — Loom's instance promises*): *"A completed instance's record is permanent, and that is a
decision … Anything that would expire, discard or sweep it removes the guarantee that a re-emitted trigger
collapses."* The record is the cursor. This design sweeps everything **but** the cursor, and Inc 4's gate is
the mechanism that keeps the next author inside that sentence. **Builds to §10 — no edit.**

`redrive`'s clause has two halves. *"Concurrent redrives of the same instance are safe: at most one takes
effect"* is kept today by the instance CAS. *"Resumes at the instance's recorded cursor"* is not kept for a
deadline-failed instance whose resumed cursor is the failed one (§3.2); Inc 2 makes it true. **Builds to §10 —
no edit** (a clause coming true is not a contract change; Andrew, 2026-09-03).

The same section promises *"engine state is rebuildable (D3) with no startup scan"*, and Inc 3 runs four
listings at start. The clause is about **correctness dependence** — no replica needs a scan to answer a
completion or resume a step, and none does after this design: the pass is maintenance the engine is correct
without, launched off the startup path, and a stack that never runs it is merely larger. **Builds to §10 — no
edit**, argued rather than assumed.

Which header a removal carries, which key families exist, and what a marker's TTL is are mechanism — component
doc, not contract. The `10-orchestration-loom.md` shard mentions no marker, delete or purge (§6 grep).

## 8. Reconciliation with the mental model

**Didn't the parent design already decide this?** It decided the *direction* (alternative 9, "not rejected —
deliberately out of this fire") and named two costs as this design's questions: the owner-denied admin verb
(§1.3: real, and not the primitive needed) and the substrate purge primitive (§3.1: ~40 lines). It did not
decide the marker's TTL, the legacy conversion's placement, or the redrive interaction — those are here.

**Doesn't the bucket already expire markers?** Only the server's own `MaxAge` markers, on deadline *expiry*
— `subject_delete_marker_ttl=1s` governs those and nothing else. A client DEL carries no TTL and cannot
(`ErrTTLOnDeleteNotSupported`); the platform's TTL-marker precedent (Weaver's leased marks in `weaver-state`,
the Contract #4 tracker) is on *values*, not on removals. This is the first removal-with-TTL in the tree, which
is why it needs the substrate to spell it.

**Is this a new mechanism to patch the previous mechanism's gap?** No — it deletes the gap's cause. A DEL
marker is the wrong shape for a removal on a history-1 bucket; the right shape (a rollup with a TTL) has
existed on the server since 2.11 and in the client since `PurgeTTL`. Nothing is layered on top of the deletes;
the deletes become the thing they should have been.

**Is any state kept elsewhere that this duplicates?** The conversion pass keeps no state (§5). The marker TTL is
a constant. Nothing new to keep, reconcile or rebuild.

**Does this touch the retention direction Andrew withdrew?** No. `loom-terminal-instance-retention-design.md`
expired the *cursor* and was withdrawn (`c9a8e55c`); this design expires *markers on sub-keys the cursor's
own doc comment calls ephemeral*, and the gate in Inc 4 refuses a bare delete of the cursor as well as of
anything else.

## 9. Alternatives

| # | Alternative | Verdict |
|---|---|---|
| **1** | **Do nothing — accept the six-subject-per-instance ledger.** | **Rejected on the mechanism, not the date.** The growth premise moved (+8 instances in two days, §1.2), so the parent's 2026-09-05 cap date is gone — but the cap is reached at *every* runaway, precisely when the inspection surface it truncates is the one an operator needs, and every whole-bucket listing pays 83% overhead for keys that carry nothing. And Andrew's ask names the tombstones as the thing to go. Priced honestly: doing nothing costs zero build and leaves the redrive defect (§3.2) standing, which this fire would otherwise not have found. |
| **2** | **`PurgeDeletes` from bootstrap** (the only component allowed `STREAM.PURGE`), on `make up` / a bootstrap re-run. | **Rejected.** It is O(markers) `$JS.API.STREAM.PURGE` requests on the server's deprioritized API queue (one per marker, `jetstream/kv.go:1533-1545`), it is an *admin* verb doing a *data* job, and it changes nothing about the next delete — the population regrows at the same rate and bootstrap has to keep running it. It also keeps recent markers by default (`WithPurgeKeep(1)` under the threshold) — a policy over a bound where none is wanted. Alternative 3 is the same conversion as a publish, from the owner, with no queue-priority question. |
| **3** | **An operator control verb** (`lattice loom sweep`, `lattice.ctrl.loom.sweep`) for the legacy conversion, the 2026-08-27 "manual verb by default" doctrine. | **Rejected for this residue, on cost and on precedent.** A control verb is its own capability verb by the control-plane doctrine (`internal/controlauth/ops.go:38-43`: never fold under an existing one), which means `LoomOps` + both grant packages (`control-authz`, `console-operator`) + their lockstep tests + two manifest version bumps + a live reinstall to hold the grant — for a one-time conversion of a fixed set. The ratified sibling on the same bucket (the parent's Inc 2 backfill, 2026-09-02) chose start-time, off-path, gated on work, no verb; the doctrine's evidence-of-need is satisfied level-wise (§3.3). If a *standing* sweep were ever wanted, that would be a verb; nothing here is standing. |
| **4** | **A `MaxAge` / `MaxBytes` on the bucket.** | **Rejected by the parent (its alternative 8), unchanged:** a limit discriminates by age, not by meaning, and would discard the cursor. Recorded so the next reader does not reach for it. |
| **5** | **Restructure the keyspace so nothing is deleted** — one reusable `instance.<id>.outbox` / `.deadline` subject per instance, overwritten in place, instead of per-step `outbox.<token>` keys. | **Rejected — partial, and it moves state the design-of-record put where it is.** The token reverse index *must* be keyed by token (a completion arrives with the token, not the instance); the outbox record's presence is what `outboxExists` reads to tell "not yet relayed" from "rejected" (`engine.go:1308-1315`), so a status flip replaces one delete with a second write and a new predicate. It would still leave the token deletes, so Inc 1 is needed anyway, and Inc 1 alone reaches all five families. |
| **6** | **`tombstoneTTL` at the 1 s floor** (the strict minimum; §3.1). | **Not rejected — equivalent for every consumer (§4).** One minute is chosen for operator legibility of `kv history` at single-digit transient cost; the design says so rather than pretending the value is load-bearing. |
| **7** | **Fix `redrive` by folding the attempt (`RetryCount`) into every derived id** so the resumed step gets a fresh token subject and `CreateOnly` stays. | **Rejected — wider and less correct.** Nine derivation sites (`engine.go:880-1402`) and their tests; a fresh *requestId* re-executes a step whose op actually **committed** (the probe-error case) instead of collapsing on the tracker; a fresh *taskId* on a userTask redrive would mint a second human task where one may exist. The put-under-CAS (§3.2) keeps every idempotency handle stable and uses a guard the code already has and documents. |
| **8** | **Leave `redrive` alone; fold only the marker change.** | **Rejected — it ships a time-keyed refusal** (§3.2, *why it is this fire's*). A defect that exists today in one shape must not be converted into a subtler one by the fix for something else. |

**Priced in combination.** 2 + 3 is the only pairing that could stand in for Inc 3, and it inherits both
objections. 5 + 1 is strictly more work than 1 for the same population. 7 + 1 fixes the redrive by a different
route and loses the idempotency handles 1 does not touch. Nothing in the table beats the recommendation by
combination, and row 1 — the removal of the thing — is priced first.

## 10. Risks

| Risk | Disposition |
|---|---|
| **A purge inside the transition batch is refused by the server** (a rollup rule this design mis-read). | Pinned before any Loom edit: the §6 substrate fixture is Inc 1's first test, and it drives a real `AtomicBatch` against the embedded server. A refusal there stops the fire at the substrate, with the Loom sites untouched. |
| **The TTL'd purge marker's expiry writes a `MaxAge` marker after all**, re-firing the deadline handler once per removed key. | Read at source, not inferred: `sdm.go:42-44` counts `KV-Operation: PURGE` as a subject-delete marker and `filestore.go:6827` skips re-marking one. The same fixture asserts the subject is *absent from the subject listing* after the TTL, which a re-mark would falsify. Even if it fired, the handler's predicate no-ops on a terminal instance — a probe, not a fault. |
| **The conversion pass purges a live key** (the worst outcome, and the only unsafe one). | Structurally excluded twice: the listing returns DEL-op entries only, and every publish is conditioned on that marker's own revision. A re-create between list and publish is a `10071` skip (§3.3's table). No unconditioned purge exists anywhere in the pass. |
| **The conversion pass runs on every start forever** because a listing keeps coming back short. | It re-runs only while it finds DEL markers; a short listing converts fewer and the next start converts the rest. After the population is zero the per-start cost is four listings over the running set. Convergence, not a loop. |
| **A second engine instance's pass races the first.** | Per-key CAS: one converts, the other skips. Both log a summary. |
| **A reader somewhere depends on a subject staying non-empty after a delete.** | §4 walks every reader on every state; the only behaviour that changes is `CreateOnly` succeeding on a genuinely empty subject, and §4 shows no live path issues one. The census (§6, readers outside `internal/loom`) confirms no external reader touches an ephemeral family. |
| **Build-tagged harnesses stop compiling** — `runningInstanceReader` and the relay/state fakes. | No interface gains or loses a method in this design (`KVPurgeWithTTL` and `KVListTombstones` are added to `*substrate.Conn`, not to a narrow interface a fake implements). Phase 0 still enumerates the tagged tests (`grep -rl "^//go:build " --include=*_test.go internal/`) and runs the ones reaching `internal/loom` and `internal/substrate`. |
| **The redrive fix's put overwrites a token something else just wrote.** | Only the redrive path writes with put, after `redrive()`'s CAS, on an instance with no pending token and no armed deadline (§3.2). The concurrent-redrive test pins that the CAS loser never reaches the write. |

## 11. Found here, owned elsewhere — filed, not folded

**The bucket's marker TTL of 1 s is the delivery window of the step-deadline signal.** A `deadline.<id>`
expiry is the *only* way Loom learns a step was rejected or lost (§10.6; `doc.go:41-45`). The server delivers
that fact as a `MaxAge` marker whose own TTL is `subject_delete_marker_ttl` = 1 s (`filestore.go:6954-6955`;
`primordial.go:121` chose the floor as "NATS requires ≥ 1 second"). The `loom-deadline` consumer is durable and
`DeliverAll`, but a durable only replays messages the stream still **holds**: if Loom is down — a restart, a
deploy, a reconnect — across the second a deadline's marker exists, the marker is gone before the consumer
returns, and the instance waits on a rejected step forever, with no startup scan to notice (§10 *engine
replicas … rebuildable with no startup scan*). This is not this design's mechanism (this design's markers are
not signals) and its fix is a bucket-provisioning decision with a blast radius on every `PerKeyTTL` bucket
(`platform_buckets.go` sets the value in one loop for all six — the Contract #4 tracker's expiry signal has the
same 1 s window). **Filed as a ★★★ Lattice row** with this paragraph as its grounding; not designed here.

## 12. Decomposition for the Steward

**One Lattice fire; Inc 1–4 are its parts.** Inc 1 and 2 touch `state.go`'s `transition` from two sides; Inc 3
sits beside them in `engine.go`; Inc 4 is the gate over the result. Splitting buys a second review of the same
diff. **Posture-changing** — a write-shape change on an orchestration-state bucket, a start-time pass that
publishes at every ephemeral subject, and a fix on the operator's recovery verb → the full adversarial pass with
cold reviewers at close (`agents/steward/SKILL.md` §4 sizing; this is the recommendation, not a floor).

**Phase 0.** Re-run §6 rows 1–3 against the stack of the day; re-confirm the six delete sites and the nine
derivation sites are where §3 says. A disagreement is a scope change.

**Inc 1 — substrate + Loom's removals.** `BatchOp.Purge` + `KVPurgeWithTTL`; the six sites; `tombstoneTTL`;
the `verify.go` / `verify-kernel` provisioning assertion; the three rewritten comments (§3.4).
*Owns:* the §6 mechanism fixture (purge-with-TTL through `AtomicBatch`, subject absent after TTL) and its
single-publish twin; `AtomicBatch` refuses `Delete && Purge`; a revision-conditioned purge on a stale revision
returns the revision-conflict class; a `transition` terminal batch leaves the pin/deadline subjects carrying a
PURGE-op marker (not DEL) and absent after TTL; the deadline handler test that a purge marker still no-ops;
the sibling-subject pending-delivery survival (§3.1).

**Inc 2 — the redrive token.** The token-mode plumbing and the put on the redrive path. *Owns:* §3.2 tests
(a)–(d), including the mutation test that the old `CreateOnly` refuses with `10071`.

**Inc 3 — the legacy conversion.** `KVListTombstones`; `sweepLegacyTombstones` launched from `Start`. *Owns:*
a fixture that seeds DEL markers through real `transition`/`KVDelete` calls (never hand-written markers) across
all four families plus a live cursor, runs the pass, and asserts every DEL subject is absent after TTL while the
cursor and any live pin/deadline survive; a re-created-key skip test (list, re-put, pass → `10071` skip, value
intact); a second-pass-is-a-no-op test; the summary log shape; the pacing applied to the `deadline.>` / `outbox.>`
families and not to the other two (a test on the rate limiter's family selection, not on wall-clock).

**Inc 4 — gate + docs + dossier.** `checkLoomStateDelete` with its self-test cases; the two doc files; the two
dossier entries. *Owns:* the lint self-test (the gate runs in CI).

**Live close (MERGED ≠ RUNNING).** After merge: cycle Loom on the dev stack (`pkill -x loom` then
`make orchestration` — the Makefile's own matcher and recipe; there is no `cycle-loom` target), watch
`nats consumer info KV_loom-state loom-deadline` stay in single-digit pending during the paced pass, observe
the summary log line (`converted ≈ 61,731`), then `nats stream info KV_loom-state` — subjects ≈ live keys within `tombstoneTTL` +
`Deleted Messages` moved; and one deadline-failed instance redriven live (`lattice loom redrive`). Record the
numbers in the commit message, not the board.

**Gates.** `go build ./...` · `make vet` · `golangci-lint run ./...` · `STRICT=1 go run
./scripts/lint-conventions.go` · every other `scripts/lint-*.go` · `go run ./scripts/lint-board.go` · `make
verify-kernel` · `go test ./internal/loom/... ./internal/substrate/...` · full `go test ./...` with
`POSTGRES_TEST_DSN`. No `packages/` edits, so no manifest version bump. Build-tagged harnesses: enumerate and
run those reaching `internal/loom` / `internal/substrate` (§10).

## 13. Adversarial pass (2026-09-03)

Run before ratification, one cold reviewer over this document with the cited code open. Findings folded into
the body above; recorded here so the next reader knows which claims were *tested* rather than reasoned.

**0 blocking · 6 major · 9 minor**, all folded. What moved:

1. **§3.2 — a second writer can reach the redrive window.** A redelivered `patternStarted` enters
   `resumeStepZero` on exactly the running/empty-token state `redrive()` leaves. Proven benign in both orders
   and pinned by test (d); the "only writer" claim was replaced with the two-order argument.
2. **§3.3 — the conversion pass publishes onto two `DeliverAll` durables.** 12,346 `deadline.*` conversions
   each fire the deadline probe; unpaced, that backlog collides with §11's one-second marker window. The
   CDC-filtered families are now paced (`legacySweepRate`), the consumer-less families run first and
   unpaced, the "no health issue" sentence became a stated bound observed at the live close. (The sharper
   fix — keying the handler on `Nats-Marker-Reason` — needs headers on `substrate.Message`, which the consumer
   path does not carry; deliberately not widened into.)
3. **§3.1 — a rollup runs the server's per-subject purge path and walks every consumer's pending map.** New
   per-removal server work a DEL never did; stated, and the sibling-subject survival is pinned in Inc 1's fixture.
4. **§3.4 — the gate missed the two shapes that reintroduce a permanent subject**: a bare `KVPurge` and a
   `Purge: true` without a TTL. Both now denied, with self-tests.
5. **§3.4 — three comments state the rationale this design removes** (`createInstance`, `redrive()`, dossier
   #1) and three more say "distinct from a normal DEL". All in Inc 4's edit list now.
6. **§3.4 — `AllowRollup`/`AllowMsgTTL`/`!DenyPurge` become a hard dependency of every transition** and had no
   assertion; `verify.go` + `verify-kernel` gain it (Inc 1).
7. Minor: the precedent paragraph names the capability-granting vs. conditional-purge asymmetry; "run while
   it finds work" is one pass per start, not a loop; "every deadline-failed instance" is qualified by the
   guard-skip escape; the §6 external-reader census is closed (three more non-functional hits); §7 argues the
   no-startup-scan clause and splits the redrive clause into its kept and broken halves; §6 row 5 names one
   mechanism; a dozen citations re-anchored, including the Loom `$KV.loom-state.>` grant, which comes from
   `Allow`'s owner loop (`matrix.go:278-282`), not `ExtraPubAllow`.

**Verified correct by the pass (tested against the pinned sources, not reasoned):** the purge-with-TTL-with-CAS
publish is accepted as a single publish and inside an atomic batch; the batch commit replays each message
through `processJetStreamMsg` without re-checking the CAS mid-batch; a `PURGE` marker's expiry is not
re-marked and the subject leaves the subject set; `MaxAge` is the only marker reason 2.14.0 writes; a
revision-conditioned purge on a gone subject is `10071`, which `IsRevisionConflict` classifies; `MetaOnly`
watchers decode the op; `Get` maps both marker ops to not-found; the six delete sites and the zero cursor
delete sites; the §3.2 defect end to end, including that a second redrive then refuses on status; the §4
reader rows; and that no frozen contract changes.

---

### Appendix — grounding ledger

| Claim | Where it is decided (not described) |
|---|---|
| A batch delete is a bare `KV-Operation: DEL` | `internal/substrate/batch.go:141-148` |
| `KVDelete` / `KVPurge` / `KVDeleteRevision` shapes | `internal/substrate/kv.go:366-422` |
| Direct-get treats DEL / PURGE / any marker reason as absent | `internal/substrate/kv_multi.go:46-53`, `:656-661`, `:875-880` |
| Watch/CDC maps any non-put op to `IsDeleted` | `internal/substrate/subscribe.go:742-757`, `:822-832` |
| The six Loom delete sites | `internal/loom/state.go:406-410`, `:431-435`, `:466-470`, `:556`, `:568`; `internal/loom/actuator.go:124` |
| The cursor is never deleted; the pin/deadline/token are | `internal/loom/state.go:195-201` (doc), `:388-475` (`transition`) |
| Deadline handler predicate = empty body | `internal/loom/engine.go:1214-1228`; probe no-ops `:1263-1275` |
| Relay predicate = empty body ⇒ ack | `internal/loom/actuator.go:87-89` |
| Step token is deterministic in `(instanceId, cursor)` | `internal/loom/token.go:20-22`, `:57-75` |
| `fail` deletes the pending token; every deadline/pin-missing caller passes it | `internal/loom/engine.go:1166-1181`; callers `:789`, `:1284`, `:1318`, `:1368`, `:1433` |
| `redrive` resumes at the cursor and re-submits via `submitStep` → `transition` `CreateOnly` | `internal/loom/control.go:318-372`; `state.go:417-427` |
| `redrive()`'s guard is the instance CAS; the pin is a put for exactly this reason | `internal/loom/state.go:483-500` |
| The happy-path redrive test never writes the token it "deletes" | `internal/loom/control_internal_test.go:404-437` (`transition(ctx, inst, "", "", nil, 0)`) |
| `advance` drops a stale token on the cursor, not on the marker | `internal/loom/engine.go:768-776` |
| Loom may publish `$KV.loom-state.>`; may not `STREAM.PURGE` it | `internal/natsperm/matrix.go:278-282` (the owner loop in `Allow`), `:77-87`, `:373-384` |
| KV purge = publish with `PURGE` + `Nats-Rollup: sub`; TTL only on purge; `LastRevision` header | `nats.go@v1.52.0` `jetstream/kv.go:1125-1170`, `kv_options.go:105-115` |
| `PurgeDeletes` = `STREAM.PURGE` per marker, keeps recent by default | `jetstream/kv.go:1499-1551` |
| `Get` on a DEL/PURGE marker → `ErrKeyNotFound` | `jetstream/kv.go:1004-1011` |
| Watcher op decoding under `MetaOnly`; `IgnoreDeletes` drops DEL+PURGE; listing is count-bounded | `jetstream/kv.go:1261-1280`, `:1297`; `docs/vendors.md` NATS row |
| Bucket creation sets `AllowRollup: true`, `DenyDelete: true`, `MaxMsgsPerSubject: 1` | `jetstream/kv.go:672-689` |
| Server: rollup allowed on first occurrence per subject in a batch; per-subject CAS in a batch | `nats-server@v2.14.0` `server/jetstream_batching.go:741-800`, `:887-907` |
| Server: TTL floor 1 s; marker-TTL clamp skipped when `MaxMsgsPer == 1` | `server/stream.go:5347-5349`, `:6889-6898` |
| Server: an expiring PURGE marker is not re-marked; a value's expiry is | `server/sdm.go:41-44`; `server/filestore.go:6820-6826`, `:6892-6893`, `:6948-6963`; removal → `stream.go:5131-5142` |
| Server: a rollup runs the per-subject purge and walks every consumer's pending map | `server/stream.go:6976-6977`, `:7055-7061`, `:2864-2899`; `server/consumer.go:6370-6440` |
| `handleTrigger`'s resume gate (running + empty pending token) | `internal/loom/engine.go:432-443`, `:486-505` |
| `AllowAtomicPublish` provisioning assertion to extend | `internal/bootstrap/verify.go:252-264`; `scripts/verify-kernel.go:303-310` |
| `JSMaxSubjectDetails = 100_000` | `server/jetstream_api.go:435` |
| `loom-state` provisioning | `internal/bootstrap/platform_buckets.go:58-63`; `primordial.go:110-140` |
| Control-plane verb doctrine (never fold a mutate verb) | `internal/controlauth/ops.go:38-49`; grant pins `packages/{control-authz,console-operator}/package_test.go` |
| The parent's ratified start-time backfill shape | `loom-instance-enumeration-bounding-design.md` §6 |
| Contract #10's permanent-record and redrive promises | `docs/contracts/10-orchestration-substrate.md` § *loom-state — Loom's instance promises* |
| Contract #4: a rejected op lands no tracker | `docs/contracts/04-idempotency-tracker.md` §4.4 |
