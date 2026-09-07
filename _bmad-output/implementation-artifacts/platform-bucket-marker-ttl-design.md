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

`loom-state` retains one hour of expiry markers instead of one second. Each is a headers-only
message on a subject that would otherwise have dropped; steady-state population is one hour of
deadline expiries. Against the bucket's own enumeration ceiling (the ratified
`loom-instance-enumeration-bounding-design.md` puts the wall near 22k instances) this is noise, and
markers are excluded from `ListKeys` by `IgnoreDeletes`.

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
- **`internal/loom/engine.go:171-175`'s comment is inexact.** It says a sub-second `StepTimeout`
  *"would not arm a marker"* because `loom-state is provisioned LimitMarkerTTL >= 1s`. The floor
  applies to the **marker's** TTL, not to a per-key TTL, and on a `MaxMsgsPer == 1` bucket a
  sub-second per-key TTL expires on time and does arm a marker (`stream.go:6890-6897`). The clamp is
  still right — a sub-second step deadline is not a deadline — but the reason stated is not the
  mechanism. Corrected in the same fire (the code is untouched; only the comment).

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
