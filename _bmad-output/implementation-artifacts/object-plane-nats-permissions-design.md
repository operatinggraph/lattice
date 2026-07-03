# Object-plane NATS permissions — fix the blob-plane transport grants

**Status: ✅ shipped (`9972fec`, 2026-07-03, Lattice Steward).** Winston self-ratified at build
time — the doc's own "For Andrew" section already established no fork / no contract change, so the
only open items were implementation-level (decide-don't-defer). One-fire correctness fix on the NATS
account-level write-restriction matrix (`deploy/gen-dev-nkeys/main.go` → `deploy/nats-server.conf`)
so the legitimate blob-plane writers can actually write under enforcement, plus the first `natsperm`
object-plane conformance vectors.

**Fire 1 CHECKPOINT (2026-07-03, Lattice Steward, `9972fec`).** Shipped: the three grant fixes (§3) +
bootstrap's `$OBJ.>`→`$O.>` typo; `TestObjectStoreWriteAccess` / `TestObjectStoreWriteIsolation` added
to `internal/natsperm/conf_test.go` and green (embedded server booted from the exact regenerated
conf — the authoritative conformance proof). NKey seeds unchanged (idempotent regen, 13 users).
**Live-stack verification narrowed from the §5 plan:** the pre-fix denial was reproduced against the
actual running (old-conf) stack via a throwaway, unpersisted probe (`loftspace-app` `ObjectPut` →
`Permissions Violation` on `$O.core-objects.M.<name>`), confirming the arch-review finding is real —
but applying the fix live would need a NATS-process restart to reload the mounted conf, which this
fire deferred: the shared dev stack was 14h up, backing concurrent fires + `loupe`/`loftspace-app`/
`clinic-app`, and restarting it unattended was judged out of proportion to a config-only fix already
proven by the embedded conformance suite. Applies automatically on the stack's next natural
`make down && make up(-full)` cycle. `go build`, `make vet`, `golangci-lint`, `STRICT=1
lint-conventions` all green; Gate 2/3 + `verify-kernel` skipped (they require a stack reset this fire
didn't take, and don't exercise this diff — the natsperm suite is the purpose-built proof here).
**Author:** Winston (Designer fire, 2026-07-02)
**Backlog:** Stream-2 arch-review intake — *object-plane-nats-permissions* (★★★, S) — arch-review correction #2.
**Owning artifacts:** `deploy/gen-dev-nkeys/main.go` (the matrix, single source of truth) → the generated
`deploy/nats-server.conf`; `internal/natsperm` (the offline conformance proof).
**Related (shared-file) row:** *natsperm-matrix-hygiene* (★★, arch #19, unbuilt) touches the same two
artifacts — see §7 sequencing.

---

## For Andrew

**What it does (two lines).** The NATS account write-restriction matrix (live on the restricted stack
since 2026-07-01) grants the object-store GC actor the wrong subject (`$OBJ.objects-base.>` — wrong
prefix *and* wrong bucket) and grants the two real blob uploaders (Loupe, loftspace-app) **no object-plane
grant at all** — so against the pinned nats.go every blob upload and every GC delete should be
**transport-denied on the live stack today**. This fixes the three grants to the vendor-correct
`$O.core-objects.>`, adds the first object-plane `natsperm` vectors (there are zero), and verifies a real
upload/GC round-trip on the restricted stack.

**Architectural fork: none.** This is a mechanical conformance fix within the already-ratified
write-restriction plane (Path A: static config + per-component NKey users). No new primitive, no new
bucket, no read/write-path change. The blob plane is the sanctioned off-graph byte-write path Contract #7
§7.2 already describes — granting Loupe/loftspace-app an `$O.` publish is **not** a P2 breach (P2 governs
Core-KV; blob bytes are a non-Core-KV plane, and the *graph* record of an object is still minted through
the Processor via an op).

**Frozen-contract change: none required.** The NKey matrix is **deploy config**, not a frozen contract;
Contract #7 §7.2 already sanctions the trusted-client → `core-objects` byte-write path and needs no edit.
Nothing is staged uncommitted for you. This is a pure build item once ratified.

**The one judgment call for you (not a fork).** I recommend **not** hardening the `OBJ_core-objects`
stream with the belt-and-suspenders stream-admin denies that protect `KV_core-kv`/`KV_capability-kv`,
because the pinned nats.go's own `ObjectPut`/`ObjectDelete` call `stream.Purge` in normal operation
(§4.3) — a `$JS.API.STREAM.PURGE.OBJ_core-objects` deny would break the legitimate write path. The blob
plane's integrity therefore rests on the publish allow-list + the same convention as the other
operational streams (`core-operations`, `core-events`, `core-schedules`, `loom-state`, …), none of which
are stream-admin-hardened. If you want the object stream hardened too, that is a **separate, larger**
design (it needs an owner-only carve-out for `PURGE`) — flagged in §8, not folded here.

---

## 1. Problem & intent

### 1.1 The symptom (grounded demand)

The NATS account-level write restriction (design `nats-account-write-restriction-design.md`, live since
`1f2f999`+`083b0ad`) makes every publish default-deny: `deploy/gen-dev-nkeys/main.go` renders a per-component
`publish { allow: [...] }` block into `deploy/nats-server.conf`, and **with an allow-list present, NATS
denies any publish that does not match an entry** (`deploy/gen-dev-nkeys/main.go:68-72`). The core-kv /
capability-kv protection this buys is airtight and proven by `internal/natsperm`.

The object plane was not verified against the live stack when enforcement turned on — the matrix comment
says so verbatim (`main.go:100-104`: *"Operational-bucket subjects (schedule / bridge / object store) …
verified against the live stack when enforcement turns on … getting them slightly off is a non-security
refinement"*). The 2026-07-02 arch review (`docs/reviews/arch-review-2026-07-02.md` §3.3, correction #2)
found they are not slightly off — they are mechanically wrong against the pinned vendor:

- **object-store-manager** is granted `$OBJ.objects-base.>` (`main.go:154`) — **wrong prefix** (`$OBJ`
  vs the vendor's `$O`) **and wrong bucket** (`objects-base` vs `core-objects`). It matches nothing the
  GC actor actually publishes.
- **Loupe** (`cmd/loupe/objects.go:143` `ObjectPut` on `CoreObjectsBucket`) and **loftspace-app**
  (`cmd/loftspace-app/lease_document.go:149`, `cmd/loftspace-app/objects.go:359`) — the two real blob
  uploaders — have **no `$O.` grant at all** (`main.go:176, 196`).

Consequence on the live restricted stack: **every blob upload (lease-PDF generation, ID-scan upload,
signature capture) and every GC delete should fail at the transport** with a permissions violation. It is
inert *only* because (a) embedded-NATS test fixtures don't enforce the matrix, so the whole suite is green,
and (b) `natsperm` has **zero** object-plane vectors, so the conformance proof never exercised it. This is
a shipped ★★★ correctness gap on a live security-plane artifact.

### 1.2 What the pinned vendor actually requires

From `nats.go v1.52.0` (`docs/vendors.md` pin; `jetstream/object.go`), the object store named
`core-objects` is backed by JetStream stream `OBJ_core-objects` with subjects:

| Operation | Subjects published (publish grant needed) | Stream-admin verbs (via `$JS.API.>`) |
|---|---|---|
| `ObjectPut` (upload) | chunks `$O.core-objects.C.<nuid>` (`objChunksPreTmpl`, line 483) + meta `$O.core-objects.M.<name>` (`objMetaPreTmpl`, line 484) | `PURGE.OBJ_core-objects` (purge partial / prior-name chunks, lines ~700, 560-561) |
| `ObjectDelete` (GC) | meta rollup marker `$O.core-objects.M.<name>` (`publishMeta`) | `PURGE.OBJ_core-objects` (purge chunks) |
| `ObjectGet` / `ObjectGetInfo` / `ObjectList` | *(none — reads)* | `MSG.GET`/`DIRECT.GET`/`INFO` (reads, allowed) |

So the only **missing publish grant** is `$O.core-objects.` (chunks `C` + meta `M`). The `PURGE` /
create / read verbs the object path also needs are already covered by the `$JS.API.>` grant every
blob-touching component already holds (`main.go:154,176,196` all include `$JS.API.>`).

## 2. Who needs what (least-privilege mapping)

| Component | Blob role (verified in code) | Needs `$O.core-objects.` publish? |
|---|---|---|
| `object-store-manager` | GC deleter — `internal/objectmanager/manager.go:148` `ObjectDelete` on tombstone cascade | **Yes** (publishes `M` rollup; purges via `$JS.API.>`) |
| `loupe` | Trusted-client uploader — `cmd/loupe/objects.go:143` `ObjectPut` (admin object surface) | **Yes** (`C`+`M`) |
| `loftspace-app` | Trusted-client uploader — lease-PDF + ID/signature `ObjectPut` | **Yes** (`C`+`M`) |
| `clinic-app` | **No** blob write (`grep` clean — zero `ObjectPut`/`CoreObjectsBucket` refs) | **No** — leave unchanged |
| `bootstrap` | Provisions the store via `$JS.API.STREAM.CREATE.OBJ_core-objects` (`$JS.API.>`); never `ObjectPut`s | Correct the dead `$OBJ.>` typo → `$O.>` (§3, cosmetic) |
| all others (processor, refractor, loom, weaver, bridge, gateway, lattice, lattice-pkg) | No blob write | **No** |

**Grant shape decision — `$O.core-objects.>` (bucket-scoped), uniform for all three writers.** It covers
both `.C.>` and `.M.>` under the single object bucket, which *is* the security boundary here (there is
exactly one object store on the platform). Rationale for uniform rather than per-verb least-privilege
(e.g. objmgr `.M.>`-only): `ObjectPut` needs both C and M; `ObjectDelete` needs M; a future nats.go
revision could shift which subjects each path touches; and a bucket-scoped grant is already the matrix's
idiom for owned operational buckets (`loom` → `$KV.loom-state.>`, `bridge` → `$KV.bridge-schedule.>`).
The `.C.>`/`.M.>` split buys no isolation *within* the one bucket. (Per-verb narrowing is noted as a
rejected alternative in §8.)

## 3. The shape — the matrix edits

All edits are to `deploy/gen-dev-nkeys/main.go` (the single source of truth), then regenerate
`deploy/nats-server.conf`. No read path, no write path, no lens, no op, no vertex/aspect/link — this is
transport config only.

```
// object-store-manager (line 154): $OBJ.objects-base.>  →  $O.core-objects.>
pubAllow: []string{bootstrap.OpsWildcardSubject, "$O.core-objects.>", "$KV.health-kv.>", "$JS.API.>", "$JS.ACK.>"},

// loupe (line 176): add $O.core-objects.>
pubAllow: []string{bootstrap.OpsWildcardSubject, "$KV.health-kv.>", "$O.core-objects.>", "$JS.API.>", "$JS.ACK.>", "lattice.ctrl.>"},

// loftspace-app (line 196): add $O.core-objects.>
pubAllow: []string{bootstrap.OpsWildcardSubject, "$KV.health-kv.>", "$O.core-objects.>", "$JS.API.>", "$JS.ACK.>"},

// bootstrap (line 162): $OBJ.>  →  $O.>   (dead typo; bootstrap never ObjectPuts today — cosmetic/future-proof)
pubAllow: []string{"$KV.>", "$O.>", "$JS.API.>", "$JS.ACK.>", bootstrap.EventsWildcardSubject, bootstrap.OpsWildcardSubject},
```

Update the matrix comment (`main.go:98-104`) so it no longer says the object-store subjects are an
unverified "non-security refinement" — they are now vendor-pinned and conformance-tested. Update each
touched component's `desc` only if wording is now stale (objmgr's *"writes the object store"* stays
correct). **No history/changelog comments** — the diff + commit message are the record.

Regeneration is idempotent and does **not** rotate any seed (`main.go:228-245` reuses existing `.nk`
files); it only re-renders the `publish.allow` lists in `deploy/nats-server.conf`. Commit the regenerated
conf alongside the matrix change (it is a committed generated artifact, `docker-compose.yml:20` mounts it
read-only).

## 4. Contract surface, read/write path, and the stream-admin decision

### 4.1 Contract surface — none

- **Frozen contracts:** untouched. Contract #7 §7.2 already sanctions the trusted-client →
  `core-objects` byte-write path ("*trusted clients stream binary blob bytes directly into the
  `core-objects` Object Store — the off-graph blob plane*"). The NKey matrix realizes that sanction at the
  transport; it is not itself a contract. **Nothing staged uncommitted for Andrew.**
- The `nats-account-write-restriction-design.md` §3.2 matrix description should gain the object-plane row
  when that doc is next touched, but it is a design doc, not a contract — optional, non-blocking.

### 4.2 Read/write-path invariants (P2/P5) — clean

Granting Loupe/loftspace-app `$O.core-objects.>` does **not** violate P2. P2 = *the Processor is the sole
writer to Core KV*. The object store is **not** Core KV — it is the off-graph blob plane (Decision #4;
Contract #7 §7.2; `internal/bootstrap/primordial.go:31-38` — a JetStream Object Store backed by stream
`OBJ_core-objects`, explicitly "NOT a KV bucket"). Blob bytes carry no graph state and never touch the
Capability Lens; the object's *graph* record (`vtx.object.<oid>` + `.content` aspect + links) is still
minted through the Processor via an op. Loupe's "gets no direct Core-KV write" property (`main.go:172`) is
preserved verbatim — this grant is object-plane, not Core-KV.

### 4.3 Why the `OBJ_core-objects` stream is NOT stream-admin-hardened (the recommendation)

`KV_core-kv` and `KV_capability-kv` get belt-and-suspenders `$JS.API.STREAM.{CREATE,UPDATE,DELETE,PURGE,
MSG.DELETE}` denies for non-owners (`protectedStreamDenies`, `main.go:45-53`) because their legitimate
writers **never** purge/mutate the stream in normal operation — so denying those verbs to non-owners is
free. The object plane is different: the pinned nats.go's own `ObjectPut` calls `stream.Purge` on the
happy path (purge-partial on error, and purge prior-name chunks on same-name overwrite), and
`ObjectDelete` calls `stream.Purge` to reclaim chunks (§1.2). **A `PURGE.OBJ_core-objects` deny would
break every legitimate upload and GC delete.** Therefore the object stream stays on the same footing as
`core-operations`/`core-events`/`core-schedules`/`loom-state` — protected by the publish allow-list, not
by stream-admin denies. Hardening it would require an owner-only `PURGE` carve-out (only objmgr + the
uploaders purge) and is out of scope for this correctness fix (§8, flagged for Andrew).

## 5. Test strategy — the first object-plane `natsperm` vectors

`internal/natsperm/conf_test.go` is the offline conformance proof: it boots an embedded JetStream server
**from the committed `deploy/nats-server.conf`** and asserts owner-writes succeed / rogue-writes
transport-deny. Add object-plane coverage mirroring the existing `TestLensTargetWriteIsolation` shape:

**New helper** `provisionObjectStore(t, bootConn)` — bootstrap creates the store (it holds `$O.>` +
`$JS.API.>`):
```go
_, err := bootConn.JetStream().CreateObjectStore(ctx, jetstream.ObjectStoreConfig{Bucket: "core-objects"})
```

**`TestObjectStoreWriteAccess`** (positive — the missing-grant regression guard):
- Provision `core-objects` as bootstrap.
- For each of `{loupe, loftspace-app, object-store-manager}`: `conn.ObjectPut(ctx, "core-objects",
  name, bytes.NewReader([...]), 0)` **succeeds** within the 5s owner-timeout. This is the assertion that
  fails today (grant missing/wrong) and passes after the fix — the direct proof of correction #2.
- `object-store-manager` additionally `ObjectDelete`s what it put (the GC verb) → nil.

**`TestObjectStoreWriteIsolation`** (negative — non-writers stay denied, proving the grant is scoped, not
a blanket `$O.>` leak):
- A non-object component (`clinic-app`, plus `gateway` and `weaver` as engine/app representatives)
  `ObjectPut` **times out** within `deniedTimeout` (2s) — the transport rejects the chunk publish, no
  PubAck, so the Put blocks to context deadline. Mirrors `assertDeniedPuts` exactly (an `ObjectPut`
  variant of the same "denied publish → deadline" mechanic).

**`TestConfigParses`** — NKey-user count stays **13** (no component added/removed); no change to that
assertion. The `publish allow` non-empty invariant still holds.

Note the existing suite can't have caught this: `TestObjectStore*` didn't exist, and the embedded-NATS
fixtures used elsewhere don't load the real conf. These two tests are the standing guard.

**Live-stack verification (the arch review's explicit ask — "verify a live upload").** On the restricted
stack (`make up-full`, enforcement ON):
1. Drive a real `ObjectPut` — the simplest is a loftspace lease-PDF generation (`make up-loftspace`,
   complete a lease so `lease_document.go` uploads) **or** a Loupe admin object upload
   (`cmd/loupe/objects.go`). Confirm HTTP 200 and no `Permissions Violation` in the NATS `-m 8222`
   monitor / server log.
2. Drive a GC delete — tombstone the owning vertex, confirm objmgr's `ObjectDelete` completes (no
   permissions violation) and the blob is reclaimed.
3. Record the before/after in the fire's commit message (before = the reproduced denial the arch review
   couldn't reproduce read-only; after = green).

## 6. Migration & rollout

- Pure config change: edit the matrix, `go run ./deploy/gen-dev-nkeys` (idempotent, reuses seeds),
  commit `main.go` + the regenerated `nats-server.conf`. Live stacks pick it up on the next `make up` /
  NATS restart (the conf is mounted read-only, `docker-compose.yml:20`).
- No data migration, no bucket change, no seed rotation, no bootstrap-version bump.
- Backward-compatible: unrestricted/dev stacks (embedded NATS, no conf enforcement) are unaffected; the
  restricted stack goes from *broken* (denied) to *working*.

## 7. Fire decomposition (for the Steward)

**One fire (S).** Coupled, must-ship-together, independently valuable — no split (fewer-larger-fires).

**Fire 1 — object-plane grants + vectors + live verify.** Green gates + a reproduced live round-trip.
1. `deploy/gen-dev-nkeys/main.go` — fix objmgr's grant, add loupe + loftspace-app grants, correct
   bootstrap's `$OBJ.>`→`$O.>`, refresh the stale matrix comment.
2. `go run ./deploy/gen-dev-nkeys` — regenerate `deploy/nats-server.conf`; commit both.
3. `internal/natsperm/conf_test.go` — add `provisionObjectStore`, `TestObjectStoreWriteAccess`,
   `TestObjectStoreWriteIsolation`.
4. Live-stack verification per §5 (upload + GC on the restricted stack); record before/after in the
   commit message.
5. Gates: `go build ./...`, `make vet`, `golangci-lint run ./...`, `go test ./internal/natsperm/...`,
   `make verify-kernel`, Gate 2 (`make test-bypass`, all BLOCKED), Gate 3
   (`make test-capability-adversarial`, all DEFENDED).

**Sequencing note (shared file).** `natsperm-matrix-hygiene` (arch #19, ★★, unbuilt) edits the **same**
`gen-dev-nkeys/main.go` + `internal/natsperm` (bridge phantom `$KV.bridge-external/schedule.>` buckets;
narrow Refractor's over-broad `$KV.>`). To avoid a shared-tree collision, the Steward should build these
two matrix touch-ups **in one sitting** (or this one first, then #19 rebases). They are orthogonal in
substance — no dependency, no dead scaffolding — but both regenerate `nats-server.conf`, so a naive
parallel build would conflict on the generated artifact.

## 8. Risks & alternatives

- **Rejected — stream-admin-harden `OBJ_core-objects` (deny `PURGE`/`DELETE` to non-owners).** Would
  break `ObjectPut`/`ObjectDelete`, which purge on the happy path (§4.3). A correct version needs an
  owner-only `PURGE` carve-out for the uploaders + objmgr — a separate, larger hardening; **flagged for
  Andrew** as a follow-up, not folded here. Blast radius today: the object stream can be purged/destroyed
  by any component holding `$JS.API.>` — the *same* exposure as every non-`core-kv`/`capability-kv`
  stream, i.e. accepted platform posture, not a new gap this fix introduces.
- **Rejected — per-verb least-privilege (objmgr `$O.core-objects.M.>` only; uploaders `.C.>`+`.M.>`).**
  No isolation gain within the single object bucket, more brittle against nats.go revisions, breaks the
  matrix's bucket-scoped idiom. Bucket-scoped `$O.core-objects.>` is the right grain (§2).
- **Risk — a fourth writer appears (e.g. clinic gains blob upload).** It would be silently transport-
  denied until granted. Mitigation: the negative `TestObjectStoreWriteIsolation` currently pins clinic-app
  as a *non-writer*; whoever adds clinic blob upload must move it to the positive set — the failing
  negative test is the tripwire that forces the grant. Documented here so the coupling is discoverable.
- **Risk — `natsperm` object tests are heavier** (object-store provisioning vs a KV bucket). Bounded:
  one small `ObjectPut`; the denied path uses the existing 2s `deniedTimeout` mechanic. Use `jsstore.Dir(t)`
  for `StoreDir` (the harness already does, `conf_test.go:59`) so the CI `-p 4` parallel teardown is safe
  (per the project's parallel-fixtures rule).

## 9. Definition of done

- objmgr/loupe/loftspace-app publish `$O.core-objects.>`; bootstrap's `$OBJ.>` corrected; clinic-app
  and all engines/CLIs unchanged; `nats-server.conf` regenerated + committed.
- `TestObjectStoreWriteAccess` (3 writers succeed) + `TestObjectStoreWriteIsolation` (≥1 non-writer
  denied) green; `TestConfigParses` still 13 users.
- A live upload + GC delete verified on the restricted stack, before/after recorded in the commit.
- All verification gates green (§7).
- No frozen-contract edit; nothing left uncommitted for Andrew.
