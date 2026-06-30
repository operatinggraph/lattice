# Design — Op-time bounded link enumeration (`kv.Links`) — retire the key-list-in-aspect guard indexes

**Status: ✅ Andrew-ratified (2026-06-28) — with a ratification revision (banner below).**
**Author: Winston (Designer fire, 2026-06-28)**
**Backlog row:** `planning-artifacts/backlog/lattice.md` → *Refinements & ops* → "Op-time bounded reverse-link / adjacency read".

---

> ## ⚠️ RATIFICATION REVISION (Andrew, 2026-06-28) — supersedes the source-side-only body where they differ
>
> The original draft enumerated **only outbound (source-side) prefixes** and therefore proposed an
> **inverted `hasBooking` (provider→appointment) link** to make the provider the prefix. Andrew corrected
> four things; the **revised shape below is authoritative** (the §2.5.1 contract edit is rewritten to match;
> body sections §2.1/§2.4/§3.3/§4.1/§6/§8/§9 that still say "source-side only", "`hasBooking`",
> "two-links-per-edge", or "fail-closed cap" are **superseded** by this banner):
>
> 1. **Both directions, bound to the hub's id.** `kv.Links(hubKey, relation, direction, cursor, limit)`.
>    The canonical key is `lnk.<srcType>.<srcId>.<rel>.<tgtType>.<tgtId>`. Server-side NATS subject filter,
>    bounded by the hub's degree in that direction (NATS `*` wildcards are valid at any token position):
>    - `"out"` (hub = source): `lnk.<hubType>.<hubId>.<rel>.>`
>    - `"in"`  (hub = target): `lnk.*.*.<rel>.<hubType>.<hubId>`
>    The draft's "target-side = unbounded whole-type scan" claim was **wrong** (it ignored mid-subject `*`).
> 2. **Drop `hasBooking`; keep the existing §1.1-correct links.** The clinic's shipped
>    `appointment withProvider provider` / `appointment forPatient patient` links are already correct
>    (appointment = later-arriving = source). The guards enumerate them **inbound**:
>    `kv.Links("vtx.provider.<p>", "withProvider", "in")` / `kv.Links("vtx.patient.<pt>", "forPatient", "in")`.
>    **No inverted link, no two-links-per-edge, no §1.1 violation** — there was no committed violation; the
>    violation existed only in the draft's proposed `hasBooking`. The §8 "author source-side" convention and
>    the §1.1 note are **withdrawn** (not needed).
> 3. **Paged, not fail-closed-capped.** A high-degree hub (a `service.template` ← many `instanceOf`
>    instances) **pages** via `cursor`/`nextCursor` instead of fail-closing at `MAX_LINK_ENUMERATION`.
>    Per-page `limit`; the OCC epoch catches any add/remove between pages on commit.
> 4. **Lazy.** Reads happen only when the script calls `kv.Links`, page by page — never pre-hydrated via
>    `contextHint.reads` (a wildcard filter has no exact-key form to declare). Fire 2 must NOT add the link
>    set to `contextHint.reads`.
>
> **Substrate seam (revised):** Fire 1 adds `Conn.KVListKeysFilter(bucket, filter)` (an arbitrary
> wildcard subject filter over `ListKeysFiltered`, with a cursor/limit for paging) — a generalization of the
> existing prefix-only `KVListKeysPrefix` (`internal/substrate/kv.go:220`, which already calls
> `ListKeysFiltered(prefix+">")`). The serialization **`bookingGuard.epoch`** token is unchanged (Andrew OK'd).

---

## For Andrew (one-look ratification)

**What it does (two lines).** Adds **one** new Starlark write-path read builtin — `kv.Links(hubKey, relation)` — a *bounded, fail-closed prefix enumeration* of a vertex's outbound canonical links (`lnk.<hubType>.<hubId>.<relation>.>`), so an op guard can read the **set** of a vertex's neighbors at write time without denormalizing that set into a key-list aspect. This retires the clinic `provider.bookings` / `patient.bookings` aspects — relationships stored as keys *in aspect data*, a Contract #1 violation we papered over — moving the topology into proper links read via the new builtin, and replaces the aspect's second (load-bearing) job, **OCC serialization**, with an explicit scalar epoch token.

**The one ratification decision (not a full architectural fork).** This **deliberately relaxes the write-path "known-key-reads-only / no-scans" invariant** by exactly one sanctioned primitive: a *bounded* (capped, fail-closed) prefix enumeration over the **Core-KV canonical link keyspace only** — never the Refractor Adjacency KV (which stays Refractor-private per the architecture), never a lens/read-model (P5 preserved). The bound + fail-closed + live-read-determinism guarantees (§3.4, §3.5) are what keep it from becoming the general scan hook `kv.Read`'s no-scans rule was written to prevent. **My recommendation: ratify the relaxation as specified.** It is the minimal honest fix for a class of guards (set/range constraints) that the existence-only guard-link precedent (`appliedToUnit`, shipped 3704324) provably cannot serve.

**Frozen-contract change (staged UNCOMMITTED in `main`).** `docs/contracts/02-operation-envelope.md` — a **new §2.5.1 "Bounded link enumeration (`kv.Links`)"** under the §2.5 context-hint semantics. It defines the builtin, the bound, the OCC/serialization contract, the live-read determinism caveat, and the Core-KV-links-only / Adjacency-KV-forbidden / P5-preserved scope. Affected consumers: the Processor (new builtin), package authors of set/range guards (a new authoring pattern), and the clinic-domain package (first consumer). No other contract section changes. The edit is the proposal — review the diff.

**No architectural fork** (Gateway / D1 read-path-auth / Vault / multi-cell / HA-NATS are untouched). No auth-surface change (this is a read primitive on Core KV the Processor already owns; step-3 auth is unchanged).

---

## 1. Problem and intent

### 1.1 The papered-over violation

The Starlark op write path is **known-key-reads-only**: `kv.Read(key)` is a single-key GET (Contract #2 §2.5), and `TestPackage_NoScans` forbids every prefix-scan helper (`KVListKeys`, `list_keys`, `scan(`, `keys_with_prefix`). This is a deliberate invariant — it keeps op cost bounded and the write path off the lagging read-models (P5).

But a guard that enforces a **set** or **range** constraint needs the *set* of a vertex's neighbors, not single-key existence. With no enumeration primitive, the clinic appointment guard denormalizes that set into a **key-list aspect**:

```
vtx.provider.<p>.bookings = {appts: ["vtx.appointment.<a>", "vtx.appointment.<b>", …]}
vtx.patient.<p>.bookings  = {appts: ["vtx.appointment.<a>", …]}
```

That is **relationships (vertex keys) stored in aspect `data`** — a direct violation of Contract #1 decision #2 / `lattice-architecture.md:587` ("Express every vertex→vertex relationship as a LINK. Root `data` holds only scalar attributes."). The graph can't traverse it, fan-out/adjacency can't see it, and the list grows unbounded (hence the manual prune-on-every-write the guard carries today).

**Honest provenance** (from the backlog row): this primitive was *named* inside the now-✅ verticals appointment-conflict and concurrent-application rows, but **never filed as Lattice demand** — it was papered over with the key-list aspects and the rows closed. This design is the corrected filing.

### 1.2 Why the existence-only guard-link precedent does not solve it

The sibling case — `unit.leaseApplications` (≤ 1 live application per applicant+unit) — was just retired (commit 3704324) by a **deterministic guard link** keyed on the constrained tuple: two concurrent first-applies collide on the *same* link key, the second `RevisionConflict`s, fail-closed. That works because the constraint is **fixed-tuple existence-uniqueness** — there is a single deterministic key both writers target.

A booking conflict is a **range/overlap** check: two concurrent bookings for the same provider at *different* (but overlapping) times have no shared deterministic key. There is no single link key both target, so the collide-on-the-key trick does not apply. Set/range guards are a genuinely distinct class that the guard-link precedent cannot serve — they need to *read the set*. (Discretizing time into fixed slots and keying a guard link per (provider, slot) is lossy for arbitrary-duration overlapping intervals; rejected — §6.)

### 1.3 Intent

Provide a **bounded, fail-closed op-time link enumeration** so set/range guards read neighbors from the canonical links (the topology lives in links, Contract #1-clean) instead of a key-list aspect — and give those guards an explicit, minimal **serialization token** to replace the OCC role the shared aspect was silently playing. Pairs with the just-ratified P7 framing: *relationships live in links, never in aspect/root data.*

---

## 2. The shape

### 2.1 The platform primitive — `kv.Links(hubKey, relation)`

A new builtin on the existing Starlark `kv` module (`internal/processor/starlark_kv.go`), a sibling of `kv.Read`:

```python
links = kv.Links("vtx.provider.<p>", "hasBooking")
# → a Starlark list of link-doc structs, one per live OR logically-deleted
#   link whose key matches  lnk.provider.<p>.hasBooking.>  , each carrying:
#     .key, .class, .isDeleted, .data, .revision,
#     .sourceVertex ("vtx.provider.<p>"), .targetVertex ("vtx.appointment.<a>")
# Tombstoned (isDeleted) links are RETURNED (like kv.Read) so the guard
# decides; hard-deleted/absent simply don't appear.
```

**Mechanism.** `kv.Links` does a **server-side subject-filtered prefix list** over the Core-KV stream — the substrate primitive `Conn.KVListKeysPrefix(ctx, coreBucket, "lnk.<hubType>.<hubId>.<relation>.")` (already implemented, `internal/substrate/kv.go:206`; already used on the link keyspace by the object-store-manager cascade, `internal/objectmanager/cascade.go:120`) — then a single-key `KVGet` per returned key to load each link doc. The prefix ends at a key-token boundary (trailing `.`), so the filter selects exactly the hub's links under that relation — **bounded by the hub's live neighbor count**, not the whole link space.

**Why the hub must be the link source.** A canonical link key is `lnk.<type1>.<id1>.<localName>.<type2>.<id2>` (Contract #1 §1.1) — the **source is the prefix**, the target is the suffix. So `lnk.provider.<p>.hasBooking.` is a clean, bounded prefix only when the **provider is the link source**. Enumerating from the *target* (today's appointment-source `withProvider`, where the provider is the suffix) requires a whole-`lnk.appointment.>`-space scan + suffix-filter — O(all appointments ever), exactly the object-manager's unbounded GC pattern, unacceptable for a hot write-path guard. **The bounded read therefore requires the enumerated hub to be the link source.** This is the load-bearing authoring rule (§2.4).

**Why not the Refractor Adjacency KV.** The architecture's general answer for *reverse/arbitrary* traversal is the Refractor Adjacency index — but `lattice-architecture.md:95` makes it **Refractor-private and explicitly prohibits direct access by non-Refractor components at MVP**. The Processor write path may not read it. `kv.Links` therefore reads **Core KV canonical links directly** (which the Processor owns — P2/P5 place no bar on the Processor reading Core KV). The cost is that the guard must author its enumeration link *source-side* on the hub; the benefit is no cross-component coupling and an OCC-coherent read against the same Core KV the commit writes.

### 2.2 The serialization token — replacing the aspect's hidden second job

The `.bookings` aspect did **two** jobs, and the second is easy to miss: its OCC-guarded rewrite on every booking is the **concurrency serialization point** (the clinic DDL says so verbatim, `ddls.go:457`). Two simultaneous bookings for one provider both rewrite `.bookings`; OCC lets exactly one commit, the other `RevisionConflict`s → retries → re-reads → sees the now-committed neighbor → conflict caught.

`kv.Links` recovers job #1 (the set). It does **not** recover job #2: two concurrent bookings each create their *own* appointment + their *own* `hasBooking` link (disjoint keys, no collision); each enumerates a snapshot that excludes the other's uncommitted link; both overlap-check clean; both commit → **double-book**. A prefix list is not a snapshot lock.

So a set/range guard built on `kv.Links` **must** also contend a shared key. The minimal honest one is a **per-hub-per-constraint scalar epoch**:

```
vtx.provider.<p>.bookingGuard = {epoch: <int>}   # class providerBookingGuard
```

The guard declares `provider.bookingGuard` in `contextHint.reads` (OCC-snapshotted at step 4) and writes `epoch+1` OCC-guarded (`make_aspect_upsert_occ`) on commit. Two concurrent bookings read epoch N, both try N→N+1; OCC commits one, the other `RevisionConflict`s → the Processor's existing step-8 retry re-hydrates at N+1, re-enumerates (the first booking's `hasBooking` link is now visible), and the overlap check fires. The epoch is a **scalar** (a count) — Contract #1-clean, *not* a relationship-in-data — and is contended *only* by that hub's bookings (no blast radius onto provider profile edits).

This is the precise decomposition: **`.bookings` conflated topology + lock; the redesign splits them — topology → `hasBooking` links (read via `kv.Links`), lock → a scalar `bookingGuard.epoch`.** That split *is* the architectural win; it is not a rename (§7 answers "isn't this the same thing?").

### 2.3 Read path / write path / orchestration

- **Read path (P5).** `kv.Links` reads **Core KV** canonical links — the write path's own substrate, never a lens/read-model. P5 (apps read lenses) is untouched: this is the Processor, not an application. The Refractor-private Adjacency KV is **not** read.
- **Write path (P2).** Unchanged in shape: the guard still emits a `MutationBatch` (the appointment vertex, its aspects, the `hasBooking` link, the `bookingGuard.epoch` bump) submitted as an operation; the Processor remains the sole Core-KV writer. The epoch bump rides the same atomic batch, so the read-decide-write stays one OCC-checked commit.
- **Orchestration.** None. This is a synchronous op-time guard — no Loom pattern, no Weaver convergence lens, no schedule. It mirrors the *existing* `kv.Read` precedent (the one non-pure builtin) — `kv.Links` is its set-valued sibling, same determinism posture (§3.5).

### 2.4 The authoring pattern (package-side, for any set/range guard)

A reusable pattern the design establishes (documented in §2.5.1 of the contract + the component docs):

1. **Author the enumeration link with the hub as source.** For "a provider's bookings", author `lnk.provider.<p>.hasBooking.appointment.<a>` (provider = source). The link reads as a sentence ("provider hasBooking appointment"). This bends the §1.1 *growth-order* convention (the appointment arrives later, so by convention would be source) — but §1.1 states the direction is "semantic, not algorithmic … the DDL author's choice." For an enumeration hub, **source = the vertex you enumerate from** is the correct authoring choice, and the design names it explicitly so it isn't re-litigated per package. (See §8 — this is also why we surface it as a documented convention, not a silent exception.)
2. **Enumerate + per-candidate read.** `kv.Links(hub, relation)` → for each non-tombstoned link, `kv.Read` the `.targetVertex` for its live status/schedule and apply the set/range check. (The per-candidate liveness loop is unchanged from today — `kv.Read` the vertex, skip `isDeleted`, skip terminal status, overlap-check the schedule.)
3. **Serialize on a scalar epoch.** Declare `hub.<constraint>Guard` in `contextHint.reads`; bump `epoch` OCC-guarded in the batch.
4. **Bound maintenance.** Tombstone the `hasBooking` link when the booking goes terminal/tombstoned (so the live prefix set stays bounded). This replaces the list-pruning the aspect carried; it can be lazy (tombstone on the next enumeration that observes a dead candidate) or eager (in Cancel/Tombstone ops). Recommend **eager** in the terminal-transition ops + a lazy backstop in the guard, mirroring today's prune-on-write.

---

## 3. Detailed semantics (the contract surface)

### 3.1 Signature and return

`kv.Links(hubKey: str, relation: str) -> list[linkDoc]`. Exactly two positional string args, both non-empty; `relation` must match the link `localName` grammar (`[a-z][a-zA-Z0-9]*`, no leading `_`). Each `linkDoc` is the same struct shape `kv.Read` returns for a link, plus `.sourceVertex` / `.targetVertex` (already on the link envelope, Contract #1 §1.3). Order is unspecified (the guard must not depend on order — it set/range-checks, it does not index positionally).

### 3.2 Cache-first, then bounded list (mirrors §2.5)

If the exact prefix's members were pre-fetched (a future optimization — not Phase 1), serve from the hydrated working set. Phase 1: always a live `KVListKeysPrefix` + per-key `KVGet`. The reads run under the per-invocation **wall-budget context** (`getExecCtx()`), so a slow/large enumeration counts against the script budget and surfaces as `StarlarkExecutionTimeout` — never an unbounded stall.

### 3.3 The bound (fail-closed)

A hard cap `MAX_LINK_ENUMERATION` (default **1024**, a Processor constant, overridable by deployment env like the existing lane/threshold knobs). If the prefix list exceeds the cap, `kv.Links` **raises a script error** (`StarlarkExecutionFailed`, detail `LinkEnumerationBoundExceeded`) — it does **not** silently truncate (a truncated set would make a guard pass a constraint it should fail → a correctness hole). Fail-closed is the load-bearing safety property: a hub too hot for an op-time set guard surfaces loudly, signalling the operator to move that constraint to an async/lens-based approach (the architecture's intended path for large-fan-out reads). With the §2.4 bound-maintenance (dead links tombstoned), a *live* booking set realistically sits in the single digits; 1024 is a generous backstop, not an expected ceiling.

### 3.4 OCC / serialization (explicit non-guarantee + the required token)

`kv.Links` returns the **currently-committed** matching links — it is **not** a snapshot-isolated read and is **not** itself a serialization point. A guard enforcing a constraint over the returned set **must** contend a shared key (the §2.2 epoch, or another OCC-guarded scalar both concurrent writers touch) to be correct under concurrency. The contract states this in bold so no future guard author assumes the enumeration alone serializes. (This is the same property `kv.Read` has — it reads live, the Processor is the idempotency/serialization authority — stated for the set case.)

### 3.5 Determinism

Like `kv.Read`, `kv.Links` reads **live** Core KV, so it is **not** replay-stable: two runs of the same `requestId` may observe different link sets and branch differently. That is intended — the consumer reads current state to decide, and the deterministic-id + OCC commit (steps 4/8) are the idempotency authority, not replay determinism. The contract reuses §2.5's existing determinism language verbatim for the set case.

### 3.6 Scope guard (Core-KV links only)

`kv.Links` accepts only a `lnk.`-shaped enumeration: `hubKey` must be a 3-segment vertex key and the constructed prefix is always `lnk.<hubType>.<hubId>.<relation>.`. It cannot enumerate `vtx.`/aspect prefixes, cannot target another bucket, and never touches the Adjacency KV or any lens target. This keeps the relaxation to *exactly* "bounded enumeration of a vertex's outbound links," nothing wider.

---

## 4. Migration / compatibility

### 4.1 Clinic-domain (the first + only current consumer)

The appointment guard (`packages/clinic-domain/ddls.go`) changes as follows:

| Today | After |
|---|---|
| `provider.bookings` / `patient.bookings` aspect = `{appts:[keys]}` (relationships-in-data) | **dropped**; topology in `lnk.provider.<p>.hasBooking.appointment.<a>` / `lnk.patient.<p>.hasBooking.appointment.<a>` links |
| `providerBookings` / `patientBookings` aspectType DDLs | **dropped** (the links are permissive — no DDL, like every other clinic link) |
| `CreateProvider`/`CreatePatient` init `.bookings` empty | init `bookingGuard {epoch:0}` empty (a declared-present serialization key) |
| Guard reads `provider+".bookings"` (declared) → iterate `appts` list | Guard `kv.Links(provider, "hasBooking")` → iterate link `.targetVertex` |
| Guard rewrites pruned+appended `.bookings` OCC (index + lock) | Guard writes the new `hasBooking` link + bumps `bookingGuard.epoch` OCC (lock); dead-candidate links tombstoned (bound) |
| `contextHint.reads` lists `provider+'.bookings'`, `patient+'.bookings'` | lists `provider+'.bookingGuard'`, `patient+'.bookingGuard'` (the epoch keys) |

The per-candidate inner loop (kv.Read vertex → skip tombstoned → kv.Read `.status` → skip terminal → kv.Read `.schedule` → half-open overlap → `SlotConflict`/`PatientDoubleBook`) is **unchanged**. The `withProvider`/`forPatient` appointment-source links **stay** (they serve the appointment's own projections + the reschedule `WrongProvider`/`WrongPatient` validation via link-walk); the new `hasBooking` hub-source links are added for enumeration. (Two links per edge: provider↔appointment now has `withProvider` *and* `hasBooking`. Acceptable — each has a distinct job. A future consolidation to a single hub-source link with the appointment lens walking it undirected is possible but out of scope; it would require re-pointing the lens MATCH and the reschedule validation, larger blast radius for no platform benefit. Recommend keeping both.)

### 4.2 Live-stack migration

Per F-004, a same-version reinstall won't hot-upgrade and an in-flight stack carrying OLD `.bookings`-mechanism providers/patients would not see the new `bookingGuard`/`hasBooking` shape (the guard would HydrationMiss the absent `bookingGuard` key — fail-closed, never a silent wrong answer). The clinic package version bumps (e.g. `0.x → 0.(x+1)`), and a fresh stack (`make down && make up-clinic`) seeds the new shape. This matches the migration note pattern the `appliedToUnit` refactor (3704324) already set; the design adds a migration note to the package, no platform migration machinery.

### 4.3 Backward compatibility of the platform primitive

`kv.Links` is purely additive (a new builtin; `kv.Read` and every existing script are byte-identical). `TestPackage_NoScans` stays valid — it forbids the *raw* scan helpers (`KVListKeys`, `list_keys`, `scan(`, `keys_with_prefix`); `kv.Links` is the *one sanctioned* enumeration and is not on that list. No existing package references `kv.Links`, so no other consumer is affected.

---

## 5. Test strategy

- **Processor unit (`internal/processor/starlark_kv_test.go` sibling).** `kv.Links`: arg validation (arity, non-string, empty, bad relation grammar); empty result (no links) → `[]`; multiple links returned with correct `.targetVertex`/`.isDeleted`; tombstoned links returned (not silently dropped); the **bound** — at `MAX_LINK_ENUMERATION+1` matching links the call raises `LinkEnumerationBoundExceeded` (fail-closed, no truncation); wall-budget threading (a slow list/get over budget → timeout, mirroring the existing `kv.Read` budget test); scope guard (a non-`lnk` or cross-bucket attempt rejected).
- **Substrate.** `KVListKeysPrefix` is already covered; add a case asserting the link-prefix boundary (`lnk.provider.<p>.hasBooking.` does not match `lnk.provider.<p>.hasBookingExtra.…` — i.e. the trailing-dot token boundary holds).
- **Clinic package unit (`package_test.go`).** Update `TestPackage_ScriptGuards`: assert `kv.Links(provider, "hasBooking")` / `kv.Links(patient, "hasBooking")` present, the `bookingGuard` epoch OCC bump present, the `.bookings` references gone; `TestPackage_NoScans` still green (kv.Links not a forbidden helper). Assert the `providerBookings`/`patientBookings` DDLs are dropped.
- **Clinic integration (`integration_test.go`).** The existing double-book / patient-double-book / reschedule / cancel-frees-slot suites are the real proof — they must pass against the new mechanism with `contextHint.reads` listing the `bookingGuard` keys. Add a **concurrency** assertion: two CreateAppointment ops racing the same overlapping slot → exactly one Accepted, one Rejected (`SlotConflict`) via the epoch OCC — the property `.bookings` OCC gave, now on the epoch.
- **Ephemeral-stack e2e.** A clinic e2e (`make up-clinic`) that creates a provider, books, attempts an overlapping double-book (rejected), cancels, re-books the freed slot (accepted) — the executable PO-reproduction the change must preserve.

---

## 6. Alternatives considered (earn the recommendation)

| # | Alternative | Verdict |
|---|---|---|
| **A** | **Keep the key-list aspects** (status quo) | Rejected — the Contract #1 violation + unbounded list + manual prune is exactly the debt this retires. It is the thing being fixed, not an option. |
| **B** | **Whole-type scan + suffix-filter** (enumerate `lnk.appointment.>`, keep `withProvider` appointment-source, filter by provider suffix) — no new link direction | Rejected — O(all appointments ever), not bounded by the hub's neighbors; it is the object-manager GC pattern, fine for a background sweep, unacceptable for a hot op-time guard. The bound is the whole point. |
| **C** | **Processor-maintained reverse-adjacency index in Core KV** (auto-write a target-keyed mirror entry per link) | Rejected — solves enumeration but adds write amplification + a new index-consistency surface on every link commit, for no benefit over choosing the enumerable direction (the §2.4 hub-source rule makes the *canonical link itself* the index). A clever new mechanism where the simplest extension — author the link source-side — suffices. (Revisit only if a domain genuinely needs *both* directions enumerable at write time, which none does today.) |
| **D** | **Discretize time into fixed slots + a deterministic guard link per (provider, slot)** — reuse the `appliedToUnit` existence-link trick | Rejected — lossy for arbitrary-duration overlapping intervals (a 90-min appointment spanning slot boundaries, back-to-back exact-touch rules). It would force a slot granularity into the domain. The guard-link trick fits *fixed-tuple* existence, not *range overlap*. |
| **E** | **Serialize on the hub vertex root** (bump a scalar on `vtx.provider.<p>` instead of a dedicated `bookingGuard` aspect) — no new aspect/DDL | Rejected (but closest runner-up) — over-serializes: every booking would now OCC-collide with legitimate provider edits (profile, time-off), and a booking "modifying the provider" is semantically muddy. The dedicated `bookingGuard` aspect contends *only* bookings. The one extra aspect write is worth the isolated blast radius. *(If Andrew prefers zero new aspect types, E is the fallback — it needs no DDL since the provider vertex is already hydrated — at the cost of the wider contention. I recommend the dedicated epoch.)* |
| **F** | **A maintained `count`/`epoch` only, no link enumeration** — keep a denormalized count, drop the keys | Rejected — a count can't answer a *range* query (you need the actual schedules to overlap-check). Enumeration is irreducible for range/set guards. |

**Could a variant of a rejected option beat the recommendation?** The honest re-test: **C** (auto-maintained reverse index) *would* let domains keep their natural link direction and still enumerate — tempting. But it pays a permanent per-link write-amplification + index-consistency tax to avoid a one-line authoring choice (source the link on the hub). The simplest extension of what exists — the canonical link *is* the index when sourced correctly — wins. **E** genuinely competes on "fewer new entities"; the design recommends the dedicated epoch for blast-radius isolation but explicitly offers E as a ratifiable fallback. No other rejected option survives re-test.

**Dead-scaffolding test.** Does `kv.Links` realize value before a consumer exists? **Yes — the clinic-domain refactor is the consumer and ships in the same initiative** (Fire 2 directly below Fire 1). The primitive is not built dark: Fire 1 (the builtin) is immediately exercised by Fire 2 (clinic). No stubbed security, no absent consumer. Sequenced, not deferred.

---

## 7. Reconciliation with the existing mental model (pre-empting "but didn't we…?")

- **"Didn't `kv.Read` + contextHint already cover write-path reads?"** Only *known-key* reads. `kv.Read` is single-key; contextHint pre-fetches a *named* set. Neither can answer "give me the *unknown-membership* set of a vertex's neighbors." That gap is exactly why the key-list aspect exists — and why a new primitive (not a reuse) is warranted.
- **"Didn't the `appliedToUnit` guard-link already retire the key-list aspects?"** It retired the *existence-only* one (`unit.leaseApplications`). The clinic `.bookings` indexes are *range/set* guards — a distinct class the guard-link trick provably can't serve (§1.2). This row is the corrected filing for that residual.
- **"Doesn't this duplicate the Refractor Adjacency KV?"** No — that index is Refractor-private and forbidden to the write path (`lattice-architecture.md:95`). `kv.Links` reads Core KV canonical links directly. It is the *write-path*, OCC-coherent, bounded sibling of adjacency, not a second copy of it. (The Adjacency KV remains the read-side/fan-out mechanism; this is the write-side bounded read.)
- **"Does this introduce new state?"** It removes state (the unbounded key-list aspect) and adds one scalar per hub (`bookingGuard.epoch`) whose *only* job is serialization — a count, not relationships. Net: less denormalized state, and the topology now lives where it belongs (links).
- **"Doesn't this break no-scans?"** It relaxes it by *exactly one* bounded, fail-closed, Core-KV-links-only primitive — flagged as the single ratification decision (top block). The raw-scan prohibition (`TestPackage_NoScans`) stands.

---

## 8. Convention surfaced for Andrew (a documented rule, not a silent exception)

The §2.4 authoring rule — *an enumeration hub is authored as the link **source*** — bends the §1.1 "later-arriving vertex = source" *growth-order* convention (an appointment arrives after its provider, yet the `hasBooking` link is provider-sourced). §1.1 already says the direction is "semantic, not algorithmic … the DDL author's choice," so this is *permitted* today — but it is load-bearing enough (it's what makes the read bounded) that it should be a **named convention**, not folk knowledge rediscovered per package. **Recommendation:** add a one-line note to Contract #1 §1.1 (or the §2.5.1 prose) — *"When a vertex's neighbors must be enumerated at write time, author the relation with that vertex as the link source so the enumeration is a bounded prefix; this is a deliberate, sanctioned use of the author's directional discretion."* This is a *clarifying* note, not a contract behavior change — staged with the §2.5.1 edit for Andrew's call on whether it lands in §1.1 or §2.5.1. (I placed the actual normative text in §2.5.1; flag if you'd rather it live in §1.1.)

---

## 9. Risks

- **Hot-hub cost.** A provider with thousands of *live* (non-terminal) bookings would push the per-op enumeration toward the bound. Mitigated by eager bound-maintenance (tombstone dead links) + the fail-closed cap (a too-hot hub surfaces loudly, not silently slow). Realistic live sets are small; the cap is a backstop.
- **Two-links-per-edge drift (clinic).** Keeping both `withProvider` and `hasBooking` means both must be tombstoned together on cancel/tombstone. Mitigated by a package test asserting both links' lifecycle. (The single-link consolidation is deferred as not worth the lens/validation re-point.)
- **Convention erosion.** Without §8's named rule, a future package author could source an enumeration link target-side and silently fall back to the unbounded suffix-scan (option B) or re-introduce a key-list aspect. Mitigated by documenting the rule + (optionally, a follow-on) a `lint-conventions` check that a guard using `kv.Links` is paired with a hub-source link — noted as a possible hardening, not required for ratification.
- **Determinism surprise.** A script author might assume `kv.Links` is replay-stable. Mitigated by the §3.5 contract language (reused from `kv.Read`) + a doc-comment on the builtin.

---

## 10. Decomposition for the Lattice Steward (fire-by-fire, each independently shippable + green)

> **Checkpoint — Fire 1 SHIPPED (`cc2613f`, 2026-06-30, CI green).** Per the ratification
> revision, the built shape is the paged form: `kv.Links(hubKey, relation, direction, cursor, limit)
> -> (page, nextCursor)` with both directions and a substrate `Conn.KVListKeysFilter` seam (the
> superseded `MAX_LINK_ENUMERATION` total-cap was replaced by paging + a per-page clamp). Full
> 3-layer adversarial review run; it caught two real substrate bugs in `KVListKeysFilter` — a silent
> partial set on context-cancel (the keyLister has no error channel; now returns `ctx.Err()`) and a
> missing de-dup against the pinned-NATS "may report duplicate keys under concurrent writes" warning
> (a boundary duplicate would skip a distinct key across pages) — both fixed + unit-tested
> (`pageFilteredKeys`, `TestKVListKeysFilter_CancelledContext`). **Next = Fire 2 (clinic consumer).**

**Fire 1 — the platform primitive (`kv.Links`).** Add the builtin to `internal/processor/starlark_kv.go` (wrap `KVListKeysPrefix` + per-key `KVGet`, wall-budget context, the `MAX_LINK_ENUMERATION` fail-closed cap, the `lnk`-prefix scope guard, the link-doc struct return). Processor unit tests (§5). The §2.5.1 contract edit is committed by Andrew at ratification (staged uncommitted now). **No consumer yet in this fire, but Fire 2 lands in the same initiative — not dead scaffolding.** A *new write-path read capability that relaxes an invariant* ⇒ **full 3-layer adversarial review** (attack the bound, the fail-closed path, the determinism caveat, the scope guard, the no-snapshot-serialization claim). Green on its own (additive builtin, existing tests pass).

**Fire 2 — the clinic-domain consumer.** Re-author the appointment guard per §4.1: `hasBooking` links + `bookingGuard` epoch, drop `.bookings` + the two aspectType DDLs, update `contextHint.reads`, eager link-tombstone on terminal transitions. Update `package_test.go` + `integration_test.go` (incl. the concurrency assertion). Bump the clinic package version. The existing double-book/reschedule/cancel suites are the regression proof. **Full review** (it's the security-of-correctness guard for double-booking) — but scoped to the package; the platform risk was discharged in Fire 1. Green: the clinic suites pass against the new mechanism.

**Fire 3 (optional) — ephemeral-stack e2e + the convention hardening.** The `make up-clinic` e2e (§5) as the executable PO-reproduction, plus (if Andrew wants it) the §8 `lint-conventions` check that a `kv.Links` use is paired with a hub-source link. Independently shippable; green.

(Fire 1 and Fire 2 are the substance; Fire 3 is proof + optional guardrail. The order is firm — Fire 2 depends on Fire 1's builtin.)

---

## 11. Ratification checklist (for Andrew)

1. **Ratify the no-scans relaxation** as specified (the one bounded, fail-closed, Core-KV-links-only enumeration primitive) — top block. *(The single substantive decision.)*
2. **Confirm the serialization choice** — dedicated `bookingGuard` epoch aspect (recommended) vs. option E (bump the hub vertex root, no new aspect). §2.2 / §6-E.
3. **Confirm the §2.5.1 contract edit** (staged uncommitted in `main`) and whether the §8 directional-convention note lands in §1.1 or §2.5.1.
4. **Confirm the clinic two-links-per-edge** decision (keep both `withProvider`+`hasBooking`, recommended) vs. the single-link consolidation.

Once ✅ Andrew-ratified, the Lattice Steward builds Fire 1 → Fire 2 (→ Fire 3).
