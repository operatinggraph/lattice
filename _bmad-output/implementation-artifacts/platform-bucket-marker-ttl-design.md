# Design — the marker TTL is a per-bucket decision, not the NATS floor

**Status: ✅ RATIFIED (Winston-adjudicated, per the 2026-08-20 delegation) — 2026-09-07.** No
architectural fork, no frozen-contract change (§6 proves both: no `docs/contracts/*` clause names
`LimitMarkerTTL`, `subject_delete_marker_ttl`, or `PerKeyTTL`). One Lattice fire.

Board row: `[Bootstrap/Loom] The 1 s marker TTL is the delivery window of every TTL-expiry signal`
(`_bmad-output/planning-artifacts/backlog/lattice.md`). Origin and grounding:
[`loom-state-tombstone-sweep-design.md`](loom-state-tombstone-sweep-design.md) §11, which filed the
row and declined to design it ("its fix is a bucket-provisioning decision with a blast radius on
every `PerKeyTTL` bucket").

## For Andrew

**What it does, in two lines.** `internal/bootstrap/primordial.go:121` gives every per-key-TTL bucket
the same `LimitMarkerTTL = 1 * time.Second` — chosen as *"NATS requires ≥ 1 second"*, i.e. the floor,
not a decision. That one second is the **entire window in which a consumer can observe that a key
expired**. Exactly one consumer in the platform reads an expiry as a signal — Loom's `loom-deadline`
watcher, whose `deadline.<id>` `MaxAge` marker is the only way Loom learns a step was rejected or
lost. This fire moves the value into the bucket registry as a per-bucket duration, and gives
`loom-state` **one hour** instead of one second. The other five buckets keep the floor, now
*declared* rather than inherited.

**Why one hour and not a day.** The window is bounded above by the evidence the deadline probe reads:
`processor.TrackerTTL` = 24 h. The probe judges *rejected-or-lost* from the absence of that tracker,
so a marker delivered after the tracker has aged out fails a healthy instance
(`loom-state-tombstone-sweep-design.md` §11.2 — a separate, unbuilt row). One hour keeps the widest
delivery this value permits at ≈ 1.02 h after the op (a 60 s deadline arm plus the window), a **24×
margin** inside the 24 h evidence lifetime, while buying 3600× over today for the named need: a
restart, a deploy, a reconnect. §3.2 states the invariant and §4 pins it with a test.

**One thing the value change makes dangerous, guarded here (§3.3).** NATS raises any per-message TTL
below the marker TTL up to the marker TTL — *except* when `MaxMsgsPer == 1`
(`server/stream.go:6892-6897`). KV buckets get `MaxMsgsPerSubject = History`, which defaults to 1
(`nats.go/jetstream/kv.go:619-625, 672`), so the exception holds today and the 60 s deadline arm is
safe. But it is an exception nothing in this repo asserts: set `History: 2` on `loom-state` and every
per-key TTL in the bucket silently becomes an hour — the 60 s step deadline included. At one second
that coupling was invisible; at one hour it is a live hazard. The fire pins `History == 1` on every
marker-TTL bucket.

**One false record found and corrected (§7).** `loom-state-tombstone-sweep-design.md` §11.2 says its
hazard was *"Filed as a ★★★ Lattice row (`📐 needs designer pass`)"*. No such row exists on
`backlog/lattice.md`. The row is filed by this fire's docs commit.

## 1. Grounding — what `LimitMarkerTTL` actually is

Version-matched to the pins in `go.mod` (`nats-server v2.14.0`, `nats.go v1.52.0`), read from the
upstream source per `docs/vendors.md`, not from secondary docs.

| Fact | Authority |
|---|---|
| `KeyValueConfig.LimitMarkerTTL` becomes the stream's `SubjectDeleteMarkerTTL` (and forces `AllowMsgTTL`). | `nats.go/jetstream/kv.go:656-690` |
| A KV bucket's `MaxMsgsPerSubject` is `History`, default **1**. | `nats.go/jetstream/kv.go:619-625, 672` |
| `SubjectDeleteMarkerTTL` is *"the TTL of delete marker messages left behind by subject delete markers"* — the marker's own lifetime. | `nats-server/server/stream.go:109-111` |
| Server refuses `SubjectDeleteMarkerTTL < 1s`. | `nats-server/server/stream.go:1768-1771` |
| A per-key TTL expiry writes a marker carrying `Nats-Marker-Reason: MaxAge`, `Nats-TTL: <sdmTTL>`, `Nats-Rollup: sub`. | `nats-server/server/filestore.go:6877-6893, 6948-6963` |
| The marker is itself a subject-delete-marker, so **its** expiry writes no successor — the subject drops silently. | `filestore.go:6892` (`sdm := last && !isSubjectDeleteMarker(...)`), `server/sdm.go:42-44` |
| A client `KV-Operation: PURGE` publish also counts as a subject-delete-marker → the tombstone sweep's TTL'd purges emit no `MaxAge` successor. | `server/sdm.go:42-44`; already pinned by `internal/substrate/kv_marker_provenance_test.go:164-173` |
| A per-message TTL below `SubjectDeleteMarkerTTL` is raised to it — **unless `MaxMsgsPer == 1`**. | `nats-server/server/stream.go:6890-6897` |
| `SubjectDeleteMarkerTTL` is **not** in the stream-update immutability set: it can be raised in place on a live stream. | `nats-server/server/stream.go:2255-2320` (no clause for it) |

**Consequence for a live stack:** `ProvisionBuckets` is `CreateOrUpdateKeyValue`, so the new value
lands on the next bootstrap run against an existing deployment — no wipe, no reprovision ceremony.

## 2. Who actually consumes an expiry — the census

Every TTL'd key in the platform, and whether anything reads its **expiry** (as opposed to its
presence). Verified live, file:line, 2026-09-07.

| Bucket | TTL'd keys | Expiry consumed as a signal? |
|---|---|---|
| `loom-state` | `deadline.<id>` (arm = `StepTimeout` / `CreateTaskTimeout`, `state.go:519-524`, `607-619`), plus the sweep's 1-min purge markers (`engine.go:55`) | **YES** — `loom-deadline`, durable, `DeliverAll`, filter `$KV.loom-state.deadline.>` (`engine.go:382-397`); `handleDeadline` acts only on `Nats-Marker-Reason: MaxAge` (`engine.go:1295-1320`) |
| `core-kv` | `vtx.op.<requestId>` tracker, `TrackerTTL = 24h` (`processor/tracker.go:19`) | No — read by presence via `lattice.op.status` (`opstatus/service.go:37-54`, `loom/engine.go:1577-1600`) |
| `health-kv` | every component heartbeat (`KVPutWithTTL`, six components) | No — read by presence |
| `weaver-state` | `mark.*` / `count.*` lease marks (`weaver/state.go:190, 270, 458, 475, 592, 610, 698`) | No — read by presence/value |
| `model-results` | in-flight marker + results (`modelrunner/engine.go:377, 517…`) | No — the TTL *is* the reaper; readers poll presence |
| `capability-kv` | none written with a per-key TTL today | n/a |

`loom-outbox-relay` (`engine.go:366-380`) watches `$KV.loom-state.outbox.>` but keys on the
`KV-Operation` removal headers, not on `MaxAge` — a client delete/purge it sees is published by
Loom itself and is not marker-TTL-gated.

**The table above is a census of TTL'd KEYS, not of every durable on these buckets** — read it as
"which expiries are consumed", which is the question that sets the value. Three further durables run
on `core-kv` and were checked separately (added 2026-09-07 by the cold pass): objectmanager's cascade
(`objectmanager/cascade.go:70`, `DeliverAll`, filter `$KV.core-kv.vtx.*.*` — which does match the
3-segment tracker key and so does receive its expiry markers), the outbox consumer
(`processor/outbox/consumer.go:50`, filter `vtx.op.*.events`), and Refractor's lens source
(`refractor/lens/corekv_source.go:726`). All three ack an empty body without acting, and `core-kv`
keeps `MinMarkerTTL` regardless, so none is affected — but a later fire re-using this section to
choose another bucket's value must start from the durables, not from this table.

**So the per-bucket split is not a hedge — it is the census.** One bucket needs a window; five need
the floor, and a longer marker lifetime on `core-kv` (the graph store) would be pure retained cost.

## 3. The shape — three increments, one fire

### 3.1 Inc 1 — the registry carries the value

`PlatformBucket.PerKeyTTL bool` → **`MarkerTTL time.Duration`** (zero = no per-key TTL support).
`ProvisionBuckets` assigns `cfg.LimitMarkerTTL = b.MarkerTTL` when non-zero. Two named constants in
`internal/bootstrap`:

- `MinMarkerTTL = 1 * time.Second` — the server's floor (`stream.go:1768-1771`). The value for every
  bucket whose expiry nobody consumes; carried explicitly so a future row states a decision rather
  than inheriting one.
- `LoomStateMarkerTTL = 1 * time.Hour` — §3.2.

The registry is *"the single place a platform bucket is born"*; the marker TTL is a property of the
bucket, so it belongs on the row next to `Owner` and `LensTarget`, not in a branch in the provisioner.
Each row's value carries a one-line reason.

Known consumers of the field: `primordial.go:117-122` and one test assertion at
`internal/modelrunner/engine_test.go:818`. `internal/natsperm` reads `Name` / `Owner` /
`SharedWrite` / `LensTarget` only — no change there. The ~39 test fixtures that hand-roll
`LimitMarkerTTL: time.Second` do not go through the registry and are **not** in scope: they provision
their own buckets to enable `AllowMsgTTL`, and a shorter marker lifetime in a test never makes a
production behaviour unreachable (the window only ever grows in production).

### 3.2 Inc 2 — `loom-state` gets an hour, under a stated invariant

    maxDeadlineArm + LoomStateMarkerTTL  <  processor.TrackerTTL

The deadline probe (`onUserTaskDeadline` / `onExternalTaskDeadline`) decides *rejected-or-lost* from
the absence of the tracker; the tracker is written at op submit and lives `TrackerTTL` = 24 h. A
marker written at deadline-fire may be consumed up to `LoomStateMarkerTTL` later, so the evidence
must still exist at `arm + window` after the op. Both arms default to 60 s (`engine.go:168-181`),
giving ≈ 1.02 h against 24 h.

**This is why the value is an hour and not a day.** A day would put the widest delivery at ~25 h —
past the evidence — trading §11's lost-signal unsoundness for §11.2's mis-fire unsoundness. Closing
*that* is the structural fix §11.2 filed (state the probe can read, on the instance record), which is
a designer pass on the cursor's shape, not this fire. An hour covers the outages §11 names — a
restart, a deploy, a reconnect — with 24× of headroom to spare.

### 3.3 Inc 3 — the four gates

The value change is what makes these load-bearing; at one second none of them could bite.

- **(a) Registry floor.** Every row's `MarkerTTL` is `0` or `>= MinMarkerTTL`. A sub-second value is
  refused by the *server* at boot (`stream.go:1768-1771`) — i.e. a bootstrap that cannot provision.
  Fail in a test instead. Pure-Go, no NATS.
- **(b) Provisioning parity.** For every registry row, the live stream's
  `Config.SubjectDeleteMarkerTTL` equals the row's `MarkerTTL` (and `AllowMsgTTL` iff non-zero).
  Proves the registry value actually reaches the server, across the idempotent re-provision.
- **(c) `History == 1` on every marker-TTL bucket.** The `stream.go:6892-6897` exception. Without it,
  every per-key TTL in the bucket is silently raised to the marker TTL — on `loom-state` at one hour
  that turns the 60 s step deadline into an hour, and the fire's own change is what makes it
  catastrophic. Assert `Config.MaxMsgsPerSubject == 1`.
- **(d) The §3.2 invariant.** `LoomStateMarkerTTL + maxDeadlineArm < processor.TrackerTTL`, in
  `bootstrap_test` (the external test package; `internal/processor` does not import
  `internal/bootstrap`, so there is no cycle). A future raise of the window cannot silently cross
  the §11.2 line.

Plus **(e)** a substrate-level provenance pin extending
`internal/substrate/kv_marker_provenance_test.go`: at a bucket `LimitMarkerTTL` above the floor, a
`MaxAge` marker is still readable well past one second and is gone after its own TTL — the property
`loom-state`'s hour depends on — and a per-key TTL *below* the marker TTL is **not** raised on a
`History = 1` bucket, which is (c)'s premise proven against the pinned server rather than asserted
from its source.

## 4. Consumer table — every reader of a marker-TTL bucket, on every state

| Reader | On a live key | On a lingering `MaxAge` marker | Changed by this fire? |
|---|---|---|---|
| `substrate.KVGet` | value | `ErrKeyNotFound` (`substrate/kv.go:26-56`) | No — a longer-lived marker still reads as absent |
| `KVListKeys*` | listed | dropped (`IgnoreDeletes`, `kv.go:184-300`) | No |
| `loom-deadline` (`MaxAge` branch) | n/a | acts — **this is the point** | Window 1 s → 1 h |
| `loom-outbox-relay` (`KV-Operation` branch) | n/a | ignores (no `KV-Operation` header on a `MaxAge` marker, `kv_marker_provenance_test.go:104-186`) | No |
| tombstone sweep's TTL'd purge | n/a | its expiry emits no successor marker (`sdm.go:42-44`) | No — `tombstoneTTL` is a per-message TTL on a `MaxMsgsPer==1` bucket, exempt from the `stream.go:6892` floor (gate (e)) |
| Core-KV CDC (`corekv_source.go`, `loom/source.go`) | event | `IsDeleted` | No — `core-kv` keeps the floor |

## 5. Cost

*Corrected 2026-09-07 by the cold pass — the first draft of this section implied expiries are a
rare-failure population. They are not.*

An expiry is **not** confined to the failure path. `onUserTaskDeadline` and `onExternalTaskDeadline`
both leave the expired creation-deadline standing, un-re-armed, when the dispatch committed normally
(`engine.go:1481-1487`, `1546-1553`) — so every userTask whose human takes longer than
`CreateTaskTimeout`, and every externalTask whose bridge exceeds it, mints a `deadline.<instanceId>`
marker on the healthy path. The steady-state marker population is therefore roughly **one subject per
in-flight async step**, held for an hour rather than a second.

That is still a bounded, cheap population — each marker is a headers-only message on a subject that
would otherwise have dropped, and it is self-expiring — but two consequences follow and both were
checked rather than assumed:

- **Enumeration is unaffected.** `ListInstances` (`control.go:109`) and the health count
  (`health.go:101`) filter server-side on `instance.`, which cannot match `deadline.*`; `KVGetMulti`
  takes explicit key lists; `KVListTombstones` returns DELETE-op entries only, and nats.go maps
  `Nats-Marker-Reason` to `KeyValuePurge` (`jetstream/kv.go:968-971`), so a `MaxAge` marker is neither
  listed nor swept; `ListKeys`/`ListKeysFiltered` carry `IgnoreDeletes`. The
  `loom-instance-enumeration-bounding-design.md` ceiling is untouched.
- **A rebuilt `loom-deadline` durable replays more.** It is `DeliverAll` (`engine.go:389`), so a
  rebuild now replays up to an hour of genuine `MaxAge` markers instead of a second's worth. This
  stays safe — a replayed marker is at most an hour old against 24 h of tracker, and `deadlineArmed`
  (`engine.go:1401`) short-circuits one whose instance re-armed — but the volume, not the outcome, is
  what changed, and `docs/components/loom.md`'s claim about a rebuilt durable is written against the
  new value.

## 6. Contract surface — none

No `docs/contracts/*` clause names `LimitMarkerTTL`, `subject_delete_marker_ttl`, or `PerKeyTTL`
(grepped 2026-09-07). Contract #4 §4.3 governs *per-key TTL support* on a bucket — unchanged: every
bucket that had it still has it. Contract #10 §10.6's deadline mechanism is unchanged in shape; only
the window in which its signal survives grows. No frozen-contract edit is staged by this fire.

## 7. Found here, resolved here

- **`loom-state-tombstone-sweep-design.md` §11.2's row does not exist.** The design records it as
  filed; `backlog/lattice.md` has no such row. Filed by this fire's docs commit as
  `📐 needs designer pass · no-pattern: an armed/disarmed deadline fact on the instance record,
  readable across redrive/replay`. This fire's §3.2 invariant is what keeps the gap out of reach at
  the chosen value; it does not close it.
- **~~`internal/loom/engine.go:171-175`'s comment is inexact.~~ — WITHDRAWN 2026-09-07, the claim in
  this bullet was itself the error.** It asserted that on a `MaxMsgsPer == 1` bucket a sub-second
  per-key TTL *"expires on time and does arm a marker"*, citing `stream.go:6890-6897`. That cite is
  the TTL-*raising* rule and does not govern here. The governing code is `parseMessageTTL`
  (`server/stream.go:5342-5351`): **any `Nats-TTL` below one second is refused outright** with
  `NewJSMessageTTLInvalidError()` (err_code 10165) — unconditionally, with no `MaxMsgsPer` exception
  and no dependence on the bucket's marker TTL. `substrate` renders the header as `ttl.String()`
  (`kv.go:358`, `batch.go:219`), so the value reaches that check verbatim; the cold pass confirmed all
  three write paths (`KVPutWithTTL`, `KVCreateWithTTL`, `AtomicBatch`) fail at 500 ms on a
  `History = 1`, one-hour-marker bucket.

  The original comment's **conclusion was right and its consequence understated**: the deadline arm
  rides inside `transition`'s all-or-nothing AtomicBatch, so a sub-second `StepTimeout` does not
  degrade the deadline — it fails **every** transition and wedges Loom. The clamp comment is rewritten
  to that mechanism (the code is untouched), which is also what `engine.go:86-87` and `:98` have said
  all along. **The lesson is the fire's, not the builder's:** a design that corrects an existing
  comment must verify the correction against the vendor source as hard as it verifies the change —
  §1's table did that for every claim the *feature* rests on and not for this one.

## 8. Fire brief (Phase 0, compiled 2026-09-07 — the committed brief for this item)

**Scope sentence, verbatim from the row:** *"`LimitMarkerTTL=1s` on all six `PerKeyTTL` buckets means
a `deadline.*` expiry (Loom's only rejected/lost-step signal) and a tracker expiry exist for one
second… Decide the value per bucket."*

**Touch list** (verified live, `file:line`):

- `internal/bootstrap/platform_buckets.go:13-14, 36, 42, 48, 55, 61, 94` — the field and its six rows.
- `internal/bootstrap/primordial.go:117-122` — the provisioning branch.
- `internal/modelrunner/engine_test.go:818` — the one non-bootstrap reader of the field.
- `internal/bootstrap/platform_buckets_test.go` — gates (a), (b), (c).
- `internal/bootstrap/loom_state_bucket_test.go:47-53` — extend with the loom-state value + `History`.
- `internal/substrate/kv_marker_provenance_test.go:104-186` — gate (e), beside the existing pin.
- `internal/loom/engine.go:171-175` — the §7 comment correction.

**Precedents to mirror:** `TestPlatformBuckets_OwnerOrSharedWrite` (pure-Go registry invariant) and
`TestProvisionBuckets_ProvisionsExactlyTheRegistry` (live registry↔server parity) are the two shapes
gates (a)–(c) take; `kv_marker_provenance_test.go`'s `awaitMarker` polling is the shape for (e) —
**no fixed `time.Sleep`** (CLAUDE.md).

**Increment order + green checks:** Inc 1 (registry + provisioner) → `go build ./...`,
`go test ./internal/bootstrap/... ./internal/modelrunner/...`; Inc 2 (the value) → same; Inc 3
(gates) → `go test ./internal/bootstrap/... ./internal/substrate/... ./internal/loom/...`. Then
`make vet`, `golangci-lint run ./...`, `STRICT=1 go run ./scripts/lint-conventions.go`,
`make verify-kernel`, and `go test ./... -p 4` with `POSTGRES_TEST_DSN` set (REMOTE.md §3 — without
it the suite is falsely green).

**In-scope gotchas:** the `stream.go:6892` floor exception (§3.3c) is the one that bites; embedded-NATS
fixtures come from `internal/natsfixture` only; a marker test must poll, never sleep; the shallow
clone makes every history-derived negative an artifact (REMOTE.md §8).

**Non-goals:** §11.2's structural fix (filed, §7); the ~39 hand-rolled test fixtures (§3.1); any
change to what a deadline *means* or to the probe's logic.

**Dossier — "Review keeps catching" for the touched components:** `docs/components/bootstrap.md` and
`docs/components/loom.md` entries are copied into the builder's brief at spawn time.

## 9. Close — what the reviews found, and the second unit they surfaced

**The cold adversarial pass returned one BLOCKING finding, and it was against this design, not the
build** (§7, withdrawn above): the design "corrected" a true comment using the wrong vendor rule. The
build had faithfully implemented the error. Classification: **design-gap**, and the sharpest lesson of
the fire — §1's vendor table was built for every claim the *feature* rests on, and the one claim the
design made about *existing* code was the only one that never went through it.

Findings by class: design-gap ×3 (the §7 error; §5's cost framing, which implied expiries are a
rare-failure population when a healthy long-running userTask mints one; §2/§4 presented as a consumer
census when they are a TTL'd-key census). Implementation-bug ×0. Convention ×1 (a test name encoding
the prior value — the no-history rule, in a permanent identifier). Test-robustness ×2 (a wall-clock
margin caught at lead review before it could redden CI, and a 2× discriminator widened at the cold
pass). Brief-gap ×1 (the touch list was compile-driven, so two `PerKeyTTL` references surviving only
in *comments* were missed).

**Second unit — the every-boot `AllowAtomicPublish` window.** The cold pass, checking whether a live
stack really picks the new value up in place, found that `nats.go`'s `prepareKeyValueConfig`
(`jetstream/kv.go:668-690`) does not carry `AllowAtomicPublish`, so every `CreateOrUpdateKeyValue`
against an existing bucket **clears** it before `enableAtomicPublish` re-sets it. Confirmed
independently against the pinned server: `true` after provisioning, `false` immediately after an
identical re-provision. `ProvisionBuckets` runs on every boot and documents itself as leaving existing
buckets unchanged, so on a live deployment there is a window each boot in which every Loom
`transition` and every Processor commit batch fails with *"atomic publish is disabled"*.

Pre-existing and not introduced here — and fixed in this run rather than filed, because it is a defect
in the same function this fire changes (§4: "pre-existing" is not an excuse). The fix converges by
reading first: an existing bucket whose live config already matches its registry row is not written at
all, so an idempotent re-run stops issuing the update that causes the flap, while a genuine change —
this fire's raised `MarkerTTL` among them — still lands. It ships as its own commit.

## 10. Dossier entries this fire mints

For `docs/components/bootstrap.md`:

- **A provisioning call that is "idempotent" still WRITES, and a write drops whatever the config type
  cannot express.** `CreateOrUpdateKeyValue` rebuilds a `StreamConfig` from `KeyValueConfig` alone, so
  every re-provision clears `AllowAtomicPublish` — a flag that exists only because the KV config type
  has no field for it — and re-sets it a call later. Minted: this fire's cold pass, while checking
  whether a raised `MarkerTTL` lands in place. Check: the re-provision tests asserting no stream update
  is issued when the live config already matches the registry row.

For `docs/components/loom.md`:

- **A constant whose only enforcement is a test of three constants is not enforced.** The marker
  window's soundness bound was written as `maxDeadlineArm + window < TrackerTTL` and gated by a test
  that hardcoded `maxDeadlineArm`, while `StepTimeout` was an exported field with only a lower clamp —
  so a deployment could violate the invariant silently and the gate stayed green. Minted: this fire's
  cold pass. Check: `MaxDeadlineArm` clamps both arms in `withDefaults`, and the bootstrap gate
  computes the invariant from that constant rather than restating it.
