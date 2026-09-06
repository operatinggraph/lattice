# The authority-walk wall is blown by the per-listing unit cost of the substrate on a flood day, not by any walk's shape — a refutation of the "generalized authority-walk primitive" row, the reader the wall never had, one clinic dedup, and the three verticals rows re-measured

**Status: ✅ RATIFIED (Winston-adjudicated, per the 2026-08-20 delegation) — no fork, no frozen-contract
edit; one adversarial pass run, NOT READY, folded in full (§12)** · Designer fire 2026-09-05 · Winston
**Board row:** `[Processor] The confinement/authority-walk wall fix (row above) is café-only`
(lattice.md, ★★★ / M, `no-pattern: a generalized authority-walk primitive beyond café's shape`) ·
**Parent:** `kv-links-listing-leg-collapse-design.md` (ratified 2026-09-01; its Inc A is unbuilt) ·
**Relates:** `verticals.md` — the three `🚧 blocked-on` rows: *Every front-desk POS write fails about half
the time* (Café ★★), *The landlord's only renewal action fails 9 times in 12* (LoftSpace ★★★), *The front
desk cannot book, cancel or re-status a single appointment* (Clinic ★★★, "8/8 accepted live 2026-09-05")

---

## For Andrew (informational — Winston-adjudicated lane)

**What this concludes, in four lines.** The row asserts clinic's and LoftSpace's walks are "structurally
different" from café's and "still blow the wall once Inc A ships". Counted, they are *smaller* than café's
(LoftSpace: 2 listings + 3 reads). Measured from the Processor's own log, the same confined op by the same
actor ran at **139 ms → timeout → 95 ms → 30 ms** on four days, and a one-listing op swung **20 → 80 →
3.5 ms** — the wall was crossed on the days the Refractor was flooding (all three floods since fixed), and
at head every confined walk passes with 6–8× margin (clinic 8/8 and 30 ms on 2026-09-05). No walk
primitive can make two listings cheaper than two listings. What ships: the Processor publishes the number
it threw away (script wall time + live read/listing counts per op, and a `step5-latency` signal the health
summary actually rolls up), clinic drops its one doubled listing, and the three verticals rows close on a
re-measure at head rather than on a platform mechanism. No fork, no contract edit, no budget change.

---

## 1. The demand, clause by clause

The row was filed by the 2026-09-05 verticals blocked-item audit (`c1f50410`), quoted verbatim:

> *"Its ratified Inc A memoizes only café's `leaseapp_unit` double-call. Clinic's `workplace_exempt` walk
> and LoftSpace's renewal→leaseapp→unit→`manages` chain are structurally different, absent from Inc A's
> census, and still blow the wall once it ships."* — `no-pattern: a generalized authority-walk primitive
> beyond café's shape`

- **"memoizes only café's double-call"** — true (parent §5.1; unbuilt: `packages/cafe-domain/ddls.go`
  carries no memo, last touched `4f43d19d` 2026-09-04 for permittedCommands).
- **"structurally different"** — true in shape, false in cost: §2.2 counts them and both are *cheaper*
  than café's 16–19 post-Inc-A round trips.
- **"absent from Inc A's census"** — half true: the parent's §3.1 lists `clinic-domain appointment ops
  8–12`; `SetRenewalTerms` is absent, and it is the smallest walk of the three.
- **"still blow the wall once it ships"** — a hypothesis about a *quantity* the row never named: the cost
  of one listing on the live host that day. §2.3 measures it across eleven days: it swings 20×, and on
  the day the row was filed the walks it names passed 8/8 at 30 ms.
- **"a generalized authority-walk primitive"** — solution-shaped (`agents/designer/SKILL.md` §2.3 F).
  The parent already refuted every batched transport the pinned substrate offers (§4 there, two
  adversarial passes); this design re-derives the *need* and finds it is not a primitive.

The three verticals rows carry the symptom as of 2026-08-29/30 (`219116e3`, `01a2f3b8`): café staff
Charge 9/20 vs resident 2/20; LoftSpace `SetRenewalTerms` 9/12; clinic staff `CreateAppointment` /
`SetAppointmentStatus` 0/20 while the patient's self-book passed 4/6 and staff `ClinicDebitAccount` 4/4.
The clinic row already records the 2026-09-05 re-test (8/8 accepted). §9 makes the re-measure the
acceptance for all three.

## 2. Grounding — the mechanism

### 2.1 What the wall measures, and the two unit costs

`StarlarkRunner.Run` executes under `starlarksandbox.Budget{Wall: 250 ms}`
(`internal/processor/starlark_runner.go:20,102`); `starlarksandbox` derives `context.WithTimeout(ctx,
budget.Wall)` and stores it on the thread (`sandbox.go:201-208`), and every live `kv.Read` / `kv.Links`
binds to it via `ContextFromThread` (`starlark_kv.go:125,219`). **The wall is elapsed time including every
live round trip** — Contract #2 §2.5.1: *"Reads run under the per-invocation wall-budget context and count
against the script timeout"*. The handler context carries no deadline of its own
(`consumer_supervisor.go:125` `WithCancel`), so nothing else masquerades as `ScriptTimeout`. The
live-read *budget* (60,000, `live_read_budget.go:26`) counts links examined, never time (§2.5 "neither
substitutes for the other"). A declared enumeration still issues a live listing — `linksFn` has no
declared-enumeration short-circuit (`starlark_kv.go:150-245`; Contract #2 §2.5 "metadata, not a hydration
directive").

| Primitive | Transport (`internal/substrate`) | Live cost, this host, 2026-09-05 13:2x (n=20) |
|---|---|---|
| `kv.Read` | `Conn.KVGet` → cached bucket handle → one `kv.Get` (`kv.go:37`, `conn.go:550`) | **0.3 ms** median · p90 0.6 · max 3.1 |
| `kv.Links` listing leg, out-hub | `KVListKeysFilter` → nats.go `ListKeysFiltered` = an ephemeral ordered consumer per call (`kv.go:282`; `nats.go@v1.52.0/jetstream/kv.go:1432`) | **6.1 ms** median · p90 13.5 · max 17.6 |
| `kv.Links` listing leg, in-filter | same, leading-wildcard filter | **12.0 ms** median · p90 19.6 · max 25.0 |
| `kv.Links` value leg | `KVGetMulti` on the page (`starlark_kv.go:356-367`) | 0.4 ms median |

Recipe (re-run before any conclusion about a walk): an ~80-line `package main` in a temp dir inside the
module, `substrate.Connect` with `deploy/nkeys/processor.nk`, timing `KVGet(actor)`,
`KVListKeysFilter("lnk.identity.<actor>.worksAt.>")`, `KVGetMulti(page)` and
`KVListKeysFilter("lnk.*.*.containedIn.<loc>")` ×20 — the parent's §3.2 recipe. **A walk's wall cost is
its listing count × the day's listing cost; its reads are noise at 0.3 ms each.**

### 2.2 The walks, counted (executable: the `kv.Links` / `kv.Read` lines inside each helper)

`grep -n 'kv\.Links\|kv\.Read' packages/clinic-domain/ddls.go | awk -F: '$1>1944 && $1<2290'` and
`… packages/lease-signing/renewal_scripts.go | awk -F: '$1>150 && $1<245'`, then subtract every read the
op's `OpMetaSpec` declares (`packages/clinic-domain/opmetas.go`) or `derive_reads` derives — those are
snapshot-served at zero round trips (`starlark_kv.go:78-88`). L = one listing leg (+ its value leg), R =
one live `kv.Read`.

| Op (confined staff path) | Walk | L | R | Wall cost at §2.1 (today) |
|---|---|---|---|---|
| clinic `CreateAppointment` (`ddls.go:2653-2654`) | `actor_holds_operator` (1L + 1R per role held) → `sites_for_provider` (`vertex_live(provider)` **0** — `{payload.provider}` is in `Reads`, `opmetas.go:107`; `practicesAt` 1L) → per candidate site (`enforce_workplace_confined` loops **every** site the provider practises at, `:2154-2156`), `worksAt_covers`: 1R vertex + 1R worksAt link per node, +1L `containedIn` per level climbed | 2 + (levels × sites) | 3 + (2 × nodes) | 15–40 ms at one site, one level |
| clinic `SetAppointmentStatus` (`:2995-2997`; same shape at `:2850`, `:3121`, `:3308-3318`, `:3377`) | `actor_holds_operator` → `appointment_provider` (1L) → `actor_bound_to_appointment_provider` (**1R, live** — its `# read-posture: (d)` at `:2285` is wrong: no appointment op declares `lnk.provider.<id>.identifiedBy.identity.<id>` (`opmetas.go:245-249` declares the *patient* link); the read-drift baseline carries it, `read_drift_baseline.txt:121`; it is a class-(e) follow-up off `withProvider`, annotated as such in Inc B) → `appointment_sites` = `sites_for_provider(appointment_provider(...))` — **`withProvider` listed a second time** (`:2245`) → `worksAt_covers` as above | **4 + …** (3 + … after §4.2) | 5–7 | 25–65 ms |
| lease-signing `SetRenewalTerms` (`renewal_scripts.go:183-245`) | `renewal_unit`: `renews` 1L, `vertex_live(app)` 1R, `appliesToUnit` 1L, `vertex_live(unit)` 1R → `require_manages` 1R | 2 | 3 | 13–30 ms |
| café staff `Charge` with `menuItemKey` (parent §3.1) | two topology chains, `leaseapp_unit` twice | ~6–8 | ~12–15 | 40–110 ms |
| clinic patient self-book `CreateAppointment` (the "unconfined" comparator) | `applicationFor` 1L (`:2777`), `.hours` / `.timeOff` `(c)` 2R, `lease_doc` / `tenancy_doc` 2R (`:2759,2764`); slot cells snapshot-served since `883e2875` | 1 | ~5 | 7–15 ms |

All four confined walks are single-page (`RENEWAL_WALK_PAGE_LIMIT = 10`, `WORKPLACE_PARENT_PAGE_LIMIT =
20`, `ROLE_PAGE_LIMIT = 50`; the front-desk actor holds one role, Riverside is one building), so
round-trip-collapse Fire 2's multi-page term does not apply. The **count axis varies per actor and
topology** (a provider at two sites, a unit two levels below its building, café's second chain): 2–8
listings. It is bounded, declared, and small. **Nothing here is a walk that needs a primitive**:
SetRenewalTerms is two listings.

### 2.3 Where the elapsed time goes — the Processor's own log is a wall-time profile

`processor.log` writes `step 4: hydrated` and `step 5: executed` (or the `ScriptTimeout` warn) per
`requestId`; the interval is the script's wall time as the Processor saw it. Compile is forced *before*
the step-4 line (`step4_hydrate.go:227`, `compiled_script.go:88-128`); the residual non-wall terms in the
interval are the state→Starlark conversion and `parseScriptResult` (`starlark_runner.go:92-146`), sub-ms
for these ops. Appendix A reduces the log (2026-08-26 → 09-05, retry-safe) per op, day and actor.

**The same walk, the same actor, four days** (`ocZv1PtnocWiy37gcwbn`, the Riverside front desk):

| Op · actor | 08-29 | 08-30 | 09-01 | 09-05 |
|---|---|---|---|---|
| `CreateAppointment` · front desk (confined, §2.2 row 1) | 15 · median **139 ms** · max 228 · 2 timeouts | 17 · **17 timeouts** | 2 · 95 ms · 0 | 7 · **30 ms** · max 35 · 0 |
| `SetAppointmentStatus` · front desk (§2.2 row 2) | 33 · 138.5 ms · max 240 · 1 timeout | 4 · 4 timeouts | 2 · 141 ms · 0 | 8 · **33 ms** · max 51 · 0 |
| `CreateAppointment` · patient `kdEjQz9L` (self-book, §2.2 row 5) | 6 · **13 ms** · max 24 · 0 | 6 · 144.5 ms · **2 timeouts** | — | (09-02) 1 · 3 ms |

**One listing, one actor, eleven days** — `BackfillAppointmentSite` (a single `(e)` `practicesAt`
listing per run, actor `NL7CR5bx`, the Weaver's backfill), the cleanest per-listing-cost probe in the
log:

| 08-26 | 08-27 | 08-28 | 08-29 | 08-30 | 09-01 | 09-02 | **09-05** |
|---|---|---|---|---|---|---|---|
| 21 ms (n=7) | 20 (36) | 62 (11) | 72 (14) | 80 (1) | 72 (1) | 48.5 (4) | **3.5 (4)** |

**Zero live reads, every day** — `MarkExpired` (16,545 runs): median 0–1 ms, max 13 ms, no day above 2 ms
median. The floor never moves; everything with a listing moves together.

**Two café actors, one day** — `Charge`, 2026-08-29: `dTerZvij` 47 runs · median 111 ms · 4 timeouts;
`EEpzBUuC` 45 runs · median 215 ms · **26 timeouts**. Same op, same host, same hour: the *count* axis
differs per actor (which chain, which depth — parent §3.1), and on a day when a listing costs tens of
milliseconds that difference is the wall. 2026-09-04: `ojZ2QPAv` 7 runs · median 169.5 · 2 timeouts.

Read together: the per-listing unit cost was **~3–5 ms on 08-27 and 09-05, ~20–40 ms on 08-28…09-02, and
worse still on 08-30** (the day even the self-book path timed out twice, and the day all three PO probes
ran). The walk shapes did not change between those days; the substrate's unit cost did. This fire cannot
say *which* Refractor work drove each day's cost — the log has no per-day server sample — only that the
three floods the platform fixed since sit inside that window: `edgeInstances` at ~15 s/event
(`e5aa6ca2`, 09-03), the 12,245 runaway background-check instances (`689eb0c0`, 09-03), the perEntry
grant-table churn (~400 audited entries/s; design ratified 09-04, unbuilt), and the personal-lens delta
(`8a43aa9d`, 09-05). That gap in the record is the finding §4.1 closes.

### 2.4 What the server looks like today, while the walks pass

Sampled 2026-09-05 13:17–13:27 with no user traffic (`curl localhost:8222/connz`, two samples 10 s apart;
`/jsz?consumers=true`): `lattice-refractor` **19,512 msgs/s out (14 MB/s)** and 5,752 in; `loom` 1,237
out; `weaver` 1,038 out; `processor` 3. NATS CPU 75 %, 12 slow consumers, host load 5.2. `KV_core-kv`:
170,619 subjects / 72 MB / 136 consumers, three replaying after the 12:14 Refractor restart:
`edgeCatalog` 90,129 → 75,204 pending in 8 min (~31/s, ~40 min left); `edgeManifestStaffReadGrants`
8,762 → 8,641 and `edgeManifestReadGrants` 5,934 → 5,813 (**15/min each, ≈4 s per event** — the two
producers the `WITH`-scope refusal sends to a whole-corpus fallback; board row `📋 ready · revived
2026-09-01`, the design body's §13 Inc 2 still reads HELD from 08-27).

Ten minutes later, under that load, the front desk ran at 30–33 ms (§2.3). **So the backlog visible
today is not the flood that crossed the wall** — a busy Refractor is not by itself the hazard; a
*particular* kind of Refractor work was, and the platform never recorded which. Byte attribution per
lens was not measured (the connz sample is per connection); the design claims nothing about it.

### 2.5 Who controls the work inside the wall — per axis

`docs/components/processor.md`'s first dossier entry: for any defence expressed as a duration, name who
controls the work inside it, per axis. The 250 ms wall has two:

| Axis | Controlled by | Bounded by | Observed by |
|---|---|---|---|
| **count** of live round trips (2–8 listings, 3–15 reads) | the package author (declared posture, `(e)` annotations, page limits); the actor's topology | the live-read budget; the read-drift ratchet; `lint-conventions` | tests only (`ScriptReadObserver` is nil in production, `commit_path.go:363`) |
| **unit cost** of one listing (3.5 → 80 ms across days) | **the substrate, shared with the Refractor's CDC reads and rescans — nobody on the write path** | nothing | **nothing** (§2.6) |

A walk primitive acts on the first axis, which is bounded and already small. The failures lived on the
second, on days the platform can no longer reproduce. That is the whole finding.

### 2.6 The number nobody publishes

`step 5: executed` logs `mutations` and `events` (`step5_execute.go:53-58`); the timeout warn logs the
budget. Neither carries the wall time, the live-read count or the listing count. Health KV publishes
`step3-latency` (auth) from a ring buffer (`health.go:439-465`, `latency_ring.go`) — nothing for step 5,
the only step with an elapsed-time refusal — and even `step3-latency` is invisible to the operator
summary: `cmd/lattice/health/health.go:95` classifies every `health.processor.<instance>.<sub>` key as
`processor-event`, and `computeSummaryRollup` (`:255-490`) has no arm for that group, so
`lattice health summary` — the Lamplighter's plane (`agents/lamplighter/SKILL.md:28,132`) — never shows
it. The `ScriptReadRecord` already counts what a script read (`script_read_record.go`) — as *sets*,
observed only by a test-time guard (`internal/testutil/pipeline.go:336`). The PO measured the outage with
a stopwatch; the platform had the numbers and threw them away.

## 3. What this design decides

1. **The row's premise is refuted and the row is corrected** (§10): no generalized authority-walk
   primitive; the parent's refutation record stands unchanged.
2. **The wall's second axis becomes observable** (§4.1): per-op wall time + live read/listing counts on
   the step-5 log line and the timeout path; a `step5-latency` Health KV signal beside `step3-latency`;
   and the `processor-event` rollup arm that makes both visible to `lattice health summary`.
3. **Clinic's one mechanical dedup ships** (§4.2), with the `(d)`→`(e)` annotation correction beside it.
4. **The three verticals rows close on a re-measure at head** (§9, §10); the hazard family is recorded
   with the trigger that would revive a platform mechanism (§8 row 4).

## 4. The shape

### 4.1 Processor: the read record gains counters, the executor gains a clock, the summary gains an arm

- `scriptReadRecorder` adds two monotone counters — `liveReadCalls` (every lazy `kv.Read` that issued a
  GET, `starlark_kv.go:125`) and `listCalls` (every `kv.Links` that issued a listing, `:219`) — surfaced
  on `ScriptReadRecord` as `LiveReadCalls`, `ListCalls`. Every `ScriptReadRecord{…}` literal in the tree is
  field-keyed and the drift guard reads named fields (`read_drift_guard.go:96-141`), so nothing breaks.
  The existing sets stay: a doubled identical enumeration is invisible to the `Enumerations` set (keyed
  `{Hub, Relation, Direction}`, `script_read_record.go:99-107,170-174`) and visible to `ListCalls` — the
  observable §4.2's test needs, and the one the parent's Inc A test spec should have named (§10).
- `ExecutorImpl.Execute` times `Runner.Run` and logs `wallMs`, `liveReads`, `listings` on the step-5 line
  and, on the error path, on a `step 5: aborted` line before returning — reading the two counters
  directly off the recorder (nil-guarded as `commit_path.go:363` does; **not** via `record()`, which sorts
  every set). It records the duration into a `latencyRing` it owns and increments a **cumulative**
  `timeouts` counter when the error classifies `ScriptTimeout`.
- `HealthHeartbeater.AttachExecutor(*ExecutorImpl)` mirrors `AttachCapabilityAuthorizer`
  (`health.go:180-183`); each tick emits `health.processor.<instance>.step5-latency` with
  `step3-latency`'s shape (`count`, `meanNs`, `p95Ns`, `p99Ns`) **plus** `timeoutsTotal` (monotone since
  instance start — a reader diffs two ticks; never reset, so a failed emit loses nothing) and
  `meanLiveReads`, `meanListings` over the same ring window, **`null` when `count` is 0** (Contract #5 §5.4:
  an unmeasured metric reports `null`, never a fabricated `0`). Category A TTL, lock-step with the
  heartbeat. `cmd/processor/main.go` wires it beside `AttachBacklogReader` (`:213`).
- **Ring semantics, stated, not assumed** (`latency_ring.go:40-73`, read in full): capacity 128,
  overwrite-only, `Count` = occupancy (never resets, pins at 128), no time window — a burst's p99
  persists until 128 newer samples displace it; on an idle Processor `count` stays at its last value.
  `step3-latency` has exactly these semantics today and the mirror keeps them; the *timeouts* figure is
  therefore a counter, not a ring statistic.
- `cmd/lattice/health/health.go`: a `case "processor-event":` arm in `computeSummaryRollup` that renders
  `.step3-latency` and `.step5-latency` as informational rows (freshness from `observedAt`, the four
  latency figures and `timeoutsTotal`), green unless stale — so the summary the Lamplighter reads carries
  the auth and script latencies of every Processor instance. Other `processor-event` keys
  (`malformed-operation.*`, `claim-attempts.*`) stay as they are: the arm matches on the suffix.
- `docs/observability/health-kv-schema.md` gains the inventory row + document shape (Contract #5 §5.4
  makes the schema doc the owner — builds-to), and `internal/healthkv/completeness_test.go` gains the
  `step5-latency` entry beside `step3-latency`'s (`:145-154`; `make test-health-completeness`).

**Readers:** Loupe's component view already lists every `health.processor.<instance>.*` sub-key
(`cmd/loupe/component.go:112-118`, `health.go:58-63`); `lattice health summary` after the arm above; the
re-measure (§9) reads the step-5 line. The next flood day reads as "step5 p99 4×, timeouts +N, Refractor
lag +M" in one summary instead of 64 MB of log and a stopwatch.

### 4.2 Clinic: `appointment_sites` takes the provider the caller already resolved

`appointment_sites(appt_id)` re-runs `appointment_provider(appt_id)` (`ddls.go:2245`) at five sites whose
caller resolved `standing_provider` / `provider` one or two lines earlier (`:2851-2852`, `:2996-2997`,
`:3121-3123`, `:3308-3318`, `:3378-3379`) — same `appt_id`, same execution, no other caller. Change the
signature to `appointment_sites(appt_id, provider)` and pass it; the `atSite` fallback and
`sites_for_provider`'s `vertex_live` gate are unchanged. It is a pure resolver (no predicate), so the
parent's memo hazard (§5.1 there: mismatched predicates turning a DENY into an ALLOW) cannot arise, and
the change strictly *removes* a TOCTOU window between two listings of a set Contract #2 §2.5.1 says is
not snapshot-isolated. None of `appointment_provider` / `appointment_sites` / `sites_for_provider` is a
pinned shared guard helper (`lint-package-standard.go:447-450` lists seven; these are clinic-local), and
the read-drift guard has no unused-row check (`read_drift_guard.go:91-141`), so no gate or baseline row
moves. In the same edit, `actor_bound_to_appointment_provider`'s annotation at `:2285` becomes
`# read-posture: (e) per-candidate follow-up read off the withProvider enumeration` — the truthful class
(§2.2); its baseline row stays, the read is still live. Manifest `0.34.21` → bump + the `Version`
constant (`package.go:139`); `DIFF_BASE=<base> go run ./scripts/lint-package-version.go`.

−1 listing on every confined clinic write that is not a `CreateAppointment`: 3–13 ms at head, 40–80 ms
on a flood day — the margin between `SetAppointmentStatus`'s 240 ms max and the wall on 08-29.

### 4.3 What is deliberately NOT swept

- clinic `CreateAppointment`'s `vertex_live(provider)` inside `sites_for_provider`: `{payload.provider}`
  is a declared `Reads` key (`opmetas.go:107`), so the read is snapshot-served at zero round trips; and
  `vertex_live` is S10-pinned (`lint-package-standard.go:449`), so it is not touchable from one package
  anyway.
- `SetRenewalTerms`: two listings, both structural (`renews`, `appliesToUnit`), nothing doubled.
- The parent's §5.2 hazards (the five-package operator-gate sweep, clinic-reminders' resolver, the merged
  covering walk) stay refused for the reasons recorded there.

## 5. State-lifetime table — the step-5 ring and counter

| Boundary | Ring (`latencyRing`, capacity 128) | `timeoutsTotal` |
|---|---|---|
| created | with `ExecutorImpl` (`NewExecutor`) | 0 with the executor |
| reset | never — overwrite-only; `count` = occupancy, pins at 128 | never — monotone; readers diff ticks |
| carried across executions | yes; across a Processor restart no — new instance NanoID, new key (Contract #5 §5.1) | same |
| ordered | no ordering claim; stats are over the last 128 executions of this instance, whatever their age | — |
| replay / redelivery / retry | a re-executed operation is a new sample — it ran again (the commit path's retry re-enters step 4 and 5, `commit_path.go:323-367`) | each `ScriptTimeout` counts, retries included |
| never-written | before the first sample: `count: 0`, latency figures 0 (as `step3-latency` emits today), `meanLiveReads` / `meanListings` **`null`** | 0 |
| segment (multi-lane) | one ring per Executor, i.e. per process; a per-lane split is a `null` metric per §5.4 until a lane-scoped executor exists | same |

## 6. Executable censuses (the build's Phase-0 re-runs these)

| Claim | Command | Expected |
|---|---|---|
| five doubled `withProvider` listings in clinic | `grep -n 'enforce_workplace_confined(appointment_sites(' packages/clinic-domain/ddls.go` | 5 lines: 2852, 2997, 3123, 3318, 3379 |
| `appointment_sites` re-resolves the provider | `grep -n 'sites_for_provider(appointment_provider(' packages/clinic-domain/ddls.go` | 1 line (2245) |
| no clinic helper here is lint-pinned | `grep -n 'appointment_sites\|appointment_provider\|sites_for_provider' scripts/lint-package-standard.go` | 0 |
| the `identifiedBy` provider link is undeclared | `grep -n 'lnk.provider.*identifiedBy' packages/clinic-domain/opmetas.go` → only `SetProviderHours` / `SetProviderTimeOff` (`:339`, `:373`); `grep -n 'read SetAppointmentStatus lnk.provider' internal/testutil/read_drift_baseline.txt` | 2 opmetas hits, neither an appointment op; 1 baseline row (`:121`) |
| the production observer is nil | `grep -n 'ScriptReadObserver' cmd/processor/main.go` | 0 |
| step-5 emits no wall time today | `grep -n 'wallMs' internal/processor/step5_execute.go` | 0 (before Inc A) |
| the summary has no processor-event arm | `grep -n 'case "processor-event"' cmd/lattice/health/health.go` | 0 (before Inc A) |
| the unit cost, not the shape, moved | Appendix A against the live host's `processor.log` | front-desk `CreateAppointment`: 139 → timeout → 95 → 30 ms; `BackfillAppointmentSite` 20 → 80 → 3.5; `MarkExpired` ≤ 1 ms every day |
| the listing leg is the unit that moves | the §2.1 spike | `kv.Links` out ≥ 10× `KVGet`; re-run on another day and the ratio holds while both shift |

`processor.log` is gitignored (`.gitignore:15`) and rotates with `make up`; the last three rows are
re-runnable only on the host that holds the log — which is exactly why Inc A publishes the same numbers.

## 7. Contract surface — builds to, nothing changes

- Contract #2 §2.5 / §2.5.1: *"Reads run under the per-invocation wall-budget context and count against
  the script timeout (NFR-P4)"* — this design makes that sentence measurable; it changes neither the wall,
  the live-read budget, nor the "metadata, not a hydration directive" posture of `enumerations` (the
  alternative that would, §8 row 4, is priced and rejected).
- Contract #5 §5.1 (a `.step5-latency` sub-key beside `.step3-latency`), §5.4 ("the per-component metric
  inventory is owned by the schema doc"; `null` for unmeasured) — the new signal is a schema-doc row and
  obeys the null rule.
- Contract #3 / the op reply: untouched — the wall's refusal text is unchanged (the *named* refusal is
  Fire 3's shelf, §8 row 6). No consumer parses the step-5 line or the timeout text (`"step 5: executed"`
  only at `step5_execute.go:53`; `"script exceeded wall budget"` only at `sandbox.go:246,312` + a
  classify-test fixture).

## 8. Alternatives

| # | Alternative | Verdict |
|---|---|---|
| 1 | **Do nothing** — build café's Inc A, re-measure the three rows at head, close them. | **Retires the three rows today** (clinic already 8/8 at 30 ms) and is most of the design. What it leaves is the platform exactly as blind to the next flood day as it was to this one — three ★★★ rows were filed, diagnosed and blocked on a mechanism for a week because the one number that decides it was never published. §4.1 is the smallest reader that changes that; §4.2 is a free listing. Nothing else is added. |
| 2 | The row's **generalized authority-walk primitive** (a batched multi-hop read, a `kv.ReadMulti`, a declared-path hydrator) | **Refuted twice over.** The parent priced every batched transport against the pinned server and lost on a load axis each time (§4.1–4.5 there); `declared-path-reads-design.md` is shelved for the link-discovered-hub class. And a primitive acts on the count axis: two listings are two listings on any transport. |
| 3 | **Demand-side per package** (sweep the walks) | Done where mechanical (§4.2, café's Inc A); the rest are the parent's §5.2 recorded hazards. SetRenewalTerms has nothing to sweep. |
| 4 | **Hydrate dispatch-known enumerations at step 4** (`{actor} holdsRole`, declared on 31 ops since `descriptor-declared-enumerations-design.md` §8.8; `{payload.provider} practicesAt`; `{payload.renewalKey} renews`) so their listing runs outside the wall | **Rejected, priced honestly.** A **frozen-contract change** — §2.5 "metadata, not a hydration directive … never materialised", §2.5.1 "never eagerly pre-hydrated" — for a partial payoff: the data-derived hops (`containedIn`, `appliesToUnit`, the second `withProvider`) stay live, so a 40–80 ms/listing day still blows a 3-listing walk; the same listing cost moves ahead of the wall, where only the lane's 30 s `AckWait` bounds it (`cmd/processor/main.go:206`). **Revive trigger:** a `ScriptTimeout` on a confined write while `step5-latency` p99 is elevated **and** the Refractor heartbeat's lag / rebuild state is flat — a unit-cost rise the Refractor does not explain. Then this, not a walk primitive, is the candidate, and it goes to Andrew with the §2.5 edit staged. |
| 5 | **Widen the wall per op class** | Refused in the parent (§4.2) and by the processor dossier: a bigger constant on an axis the platform's bulk consumer controls holds nothing; under the causal saturation it taxes every co-resident op. |
| 6 | **Round-trip-collapse Fire 3** (the named round-trip refusal) | Does not trip: it aborts *before* the wall on a round-trip **count**; these walks are 5–15 round trips and die on unit cost. Its shelf and revive trigger are unchanged. Fire 2 (multi-page snapshot) does not apply: every walk here is single-page. |
| 7 | **Pace or prioritise the Refractor's bulk reads** (a QoS between the write path's listings and CDC rescans) | The pinned NATS server has no per-connection priority; a Refractor-side pacer is new machinery to patch a cost whose *sources* already have rows (`WITH`-scope Inc 2, perEntry withholding, the personal-lens delta that shipped today) — and §2.4 shows a busy Refractor is not by itself the hazard. Simplify the base first (`SKILL.md` §3.7); recorded as the hazard family in §9. |
| 8 | A `ScriptTimeout` that carries `wallMs`, `liveReads`, `listings` in `details` | Not now: a shipped error's `details` is wire; no dispatcher reads it; Fire 3 owns the named-refusal shape. The counts go to the log and Health KV (§4.1), where the operator and the Lamplighter look. |

## 9. The hazard recorded, and the acceptance

**Hazard (platform, recorded — no mechanism):** the write path's live listing leg (`kv.Links` → an
ephemeral consumer per call) shares one NATS server with the Refractor's CDC reads and rescans, with no
priority between them. On a flood day every confined write's wall cost rises by the number of listings it
carries while unconfined writes (self-service, operator) pass — the front desk fails first, the platform
looks healthy. Between 08-28 and 09-02 the per-listing cost sat at 20–80 ms; at head it is 3–13 ms; the
platform recorded neither. After Inc A the next such day is a `step5-latency` row beside the Refractor
heartbeat, and §8 row 4's revive trigger is a readable condition instead of a stopwatch.

**Acceptance (owned by Inc C):** the PO's paced probe, 1/s, 20 submits each, **at head, now** — clinic
front desk `CreateAppointment` + `SetAppointmentStatus` (actor `ocZv1PtnocWiy37gcwbn`, Riverside; already
8/8 on 2026-09-05), LoftSpace landlord `SetRenewalTerms` (`Vro7gNhE`), café staff `Charge` with
`menuItemKey` vs the resident — read from the step-4→step-5 interval today and from the step-5 line once
Inc A ships. **Pass:** zero timeouts and every walk under 100 ms median. The verticals rows then close as
*not reproducible at head; cause = the fixed flood days; tripwire = `step5-latency`*. **Fail** (a walk
still crosses the wall on a quiet Refractor): §8 row 4's trigger, with the numbers, to Andrew.

## 10. Board corrections

- **lattice.md — this row:** state → `✅ ratified (Winston-adjudicated) · [design]`, `What` corrected:
  *"Measured: the walks are 2–8 listings and the wall was crossed by the per-listing unit cost on the
  Refractor's flood days (same op, same actor 139 ms → timeout → 95 → 30 ms; a one-listing op 20 → 80 →
  3.5 ms). No walk primitive; ships the step-5 wall/read telemetry (log + `step5-latency` + the summary
  arm), clinic's doubled `withProvider` listing, and the re-measure at head."*
- **The parent's Inc A test spec** (`kv-links-listing-leg-collapse-design.md` §5.1 cites
  `ScriptReadRecord.Enumerations` as the observable): the set cannot see a doubled identical enumeration;
  the observable is `ListCalls` (§4.1) — a one-line pointer added to the parent's §5.1.
- **verticals.md — the three rows:** `🚧 blocked-on:` → `📋 re-measure at head (§9 of this design)`; the
  clinic row already carries its 8/8. The café row's "fork resolved PLATFORM" stays corrected as the
  parent did. A row whose probe passes closes to the Done log with the numbers; a row whose probe fails
  re-blocks on §8 row 4's trigger.

**§9 acceptance — recorded (Verticals steward, 2026-09-05, ~23:00Z, read from the step-4→step-5 interval;
Inc A unshipped).** Paced 1/s at head, one actor each, the Appendix A reduction over `processor.log`:
`SetRenewalTerms` landlord `Vro7gNhE` 20/20 accepted, 0 timeouts, median 28.5 ms, max 85; café staff `Charge`
`EEpzBUuC` with `menuItemKey` 20/20, 0 timeouts, median 86.5 ms, max 193 (the resident `dTerZvij` on the same
tab 10/10, median 41 ms; the same staffer's `VoidCharge` ×30 median 55 ms, max 136); clinic front desk
`ocZv1PtnocWiy37gcwbn` `CreateAppointment` 7/7 median 30 ms + `SetAppointmentStatus` 8/8 median 33 ms, 0
timeouts. **All three PASS** the criterion above; the café staff walk carries the thinnest margin (median at
0.87 of the 100 ms bar, max at 0.77 of the wall) and is the first to cross on the next flood day — the
tripwire is Inc A's `step5-latency`. The three verticals rows are closed to that lane's Done log with these
numbers; the probe tab (`vtx.tab.vV74vKvwmip9tHuxLrYz`) was voided line-by-line and settled at $0.

## 11. Decomposition for the Steward

| Inc | Scope | Size | Review depth |
|---|---|---|---|
| A | Processor: `LiveReadCalls` / `ListCalls` on the read record; `ExecutorImpl` timing + counts on the step-5 line and the aborted path; `latencyRing` + cumulative `timeoutsTotal`; `AttachExecutor` + `step5-latency` emit (null means at count 0); the `processor-event` rollup arm in `cmd/lattice/health`; schema-doc row + shape; completeness-harness entry; `cmd/processor/main.go` wiring. **Owned tests:** the record counters pin one call per `kv.Links` invocation (a doubled call reads 2 where `Enumerations` reads 1); the heartbeat test that pins `step3-latency`'s document pins `step5-latency`'s beside it, including the zero-sample tick with `null` means; a runner/executor test that a `ScriptTimeout` still records a sample and increments the counter; a `lattice health summary` test that a `.step5-latency` doc renders a row and a stale one goes yellow. | S–M | Standard |
| B | clinic-domain: `appointment_sites(appt_id, provider)` at the five sites + the `atSite` fallback; the `(d)`→`(e)` annotation at `:2285`; version bumps. **Owned test:** `frontdesk_confinement_test.go` / `provider_tombstone_confinement_test.go` stay green unmodified, plus one assertion via the pipeline harness's `ScriptReadObserver` (`testutil/pipeline.go:336`) that a confined `SetAppointmentStatus` at one site, one level, records `ListCalls == 3` (operator walk, `withProvider` once, `practicesAt`) — 4 before the change. | S | Standard |
| C | Board corrections (§10) + the §9 probes for LoftSpace and café at head (clinic's is recorded); the parent's §5.1 pointer. | XS | — |

Inc A and B are independent and order-free; C's probes need no code. Posture-changing: none
(observation only; a pure resolver refactor). Gates: `go build ./...`, `make vet`, `golangci-lint run`,
`make verify-kernel`, `go test ./internal/processor/ ./cmd/lattice/... ./packages/clinic-domain/`,
`make verify-package-clinic-domain` (`Makefile:569`), `make test-health-completeness` (`Makefile:1870`,
build-tagged — it does not run under `go test ./...`), `lint-package-version`, every `scripts/lint-*.go`.
`HealthHeartbeater` grows an `Attach*` method, not an interface method, so the other build-tagged
harnesses are unaffected — verify with CLAUDE.md's `grep -rl "^//go:build "` before declaring so.

## 12. Review record

**§2.3 walk (self, before the pass).** A — the row's "still blow the wall" was the unmeasured quantity;
measured three ways (log profile, live spike, load sample). B — the transport carrying the wall (the
sandbox context) was read; the permission envelope is untouched. C — the doubled-listing census (5) and
the pinned-helper census (0) were run. D — no new guard. E — lifetime table. F — row one of §8 is "do
nothing".

**Pass 1 (adversarial, opus, read-only, 2026-09-05) — NOT READY; every finding folded:**

- **B1 (blocking) — the design's own log refuted its load attribution.** The first draft blamed the
  backlog visible at 13:17 for the wall; the same front-desk ops ran at 13:37 under that backlog at 30–33
  ms with zero timeouts, and the acceptance it wrote ("after the `edgeManifest*` consumers drain, <100 ms
  median") was already met by nothing. Folded: §2.3 carries the 09-05 column and the eleven-day
  one-listing probe; §2.4 is reframed as "what the server looks like while the walks pass"; §9 is a
  re-measure at head; §10 re-points the verticals rows at the re-measure, not at Refractor rows.
- **B2 (blocking) — `actor_bound_to_appointment_provider` counted as a declared read.** It is live; the
  script's own `(d)` annotation is wrong for all five appointment ops. Folded: §2.2 R = 5–7; Inc B fixes
  the annotation; §6 census added.
- **B3 (blocking) — the self-book comparator mis-described** (it carries one listing + five live reads)
  and the "13 ms of reads + 125 ms of listings" subtraction was not derivable. Folded: the comparator is
  §2.2 row 5; the derivation is gone; the per-listing cost is read off the one-listing op instead.
- **B4 (blocking) — café `Charge` cells merged two actors** (4/47 vs 26/45 timeouts on 08-29) and the
  09-04 median was neither actor's. Folded: per-actor cells; the count-axis-varies-per-actor point in
  §2.2/§2.3.
- **M5 — no alerting reader:** `.step5-latency` classifies `processor-event` and the summary has no arm
  (nor for `step3-latency`). Folded: Inc A adds the arm; §2.6 records that `step3-latency` is invisible
  today.
- **M6/M7 — ring semantics:** `Count` is occupancy, never reset; a reset-on-emit `timeouts` loses data on
  a failed write; `meanLiveReads` at count 0 would fabricate a `0`. Folded: cumulative `timeoutsTotal`,
  `null` means, §5 rewritten from `latency_ring.go:40-73`.
- **M8 — the one-listing "control" was shown for four days and called flat;** the full series (21 → 80 →
  3.5) is the strongest per-listing datum in the log. Folded: it is now the probe, published in full;
  `MarkExpired` is the control.
- **M9 — listings per candidate site:** `enforce_workplace_confined` loops every site. Folded: §2.2.
- **M10 — the acceptance precondition was unreachable** (drain at 15/min; Inc 2 HELD in the body).
  Folded: no precondition; §2.4 cites the board row's state and the body's HELD line.
- **M11 — the review record was a dangling reference.** Folded: this section.
- **m12–m18:** `vertex_live(provider)` is snapshot-served and S10-pinned (§4.3); `:109` → `:102`; the
  "still backlogged" phrase is the board row's, not the design's; `make verify-kernel`,
  `verify-package-clinic-domain`, `test-health-completeness` added to the gates and the harness entry to
  Inc A; counters read directly, not via `record()`; Appendix A is retry- and midnight-safe; the log's
  gitignored, host-bound nature is stated in §6.
- **VERIFIED-OK by the pass (kept as grounding):** the wall includes live round trips and nothing else
  masquerades as it; compile precedes the step-4 line; a declared enumeration still lists live;
  `Enumerations` is a set and `ListCalls` is the right observable; the observer fires on the failure path
  and adding fields breaks nothing; the dedup is identical at all five sites and removes a TOCTOU window;
  the three grep censuses reproduce; the drift guard has no unused-row check; §8 row 4 is honestly a
  contract change; §7's surface is correct; no consumer parses the step-5 line or the timeout text;
  `SetRenewalTerms` is 2 L + 3 R and the core refutation stands — its "today" column was confirmed by
  the 09-05 measurements (30 / 33 ms).

**Post-fold check (self):** every number in §2.3 was regenerated by the corrected Appendix A after the
fold (per actor, retry-safe, with 09-05); the §2.2 counts were re-derived against `opmetas.go` for each
op; the §5 table was rewritten from the ring's code, not its name. The adjudication gate is discharged.

---

### Appendix A — the wall-time profile from `processor.log`

```python
# per-op, per-day, per-actor script wall time = t(step 5 | ScriptTimeout) - t(the LAST step 4 before it);
# retry-safe (the commit path re-enters step 4), midnight-safe (full timestamps), per actor (never pooled).
import re, collections, statistics, sys, datetime
ts = re.compile(r'^time=(\d{4}-\d\d-\d\d)T(\d\d):(\d\d):(\d\d)\.(\d+)')
def t(m): return datetime.datetime.fromisoformat(f"{m[1]}T{m[2]}:{m[3]}:{m[4]}.{m[5]}").timestamp()
req = {}; samples = collections.defaultdict(lambda: [0, 0, []])   # (op, day, actor) -> [submitted, timeouts, wall_ms]
for line in open('processor.log', errors='replace'):
    m = ts.match(line); r = re.search(r'requestId=(\S+)', line)
    if not (m and r): continue
    d = req.setdefault(r[1], {})
    if 'step 1: envelope parsed' in line:
        d['op'] = re.search(r'operationType=(\S+)', line)[1]; d['actor'] = re.search(r'actor=(\S+)', line)[1]; d['day'] = m[1]
        samples[(d['op'], d['day'], d['actor'])][0] += 1
    elif 'step 4: hydrated' in line: d['h'] = t(m)
    elif 'step 5: executed' in line or 'ScriptTimeout' in line:
        if 'op' not in d or 'h' not in d: continue
        a = samples[(d['op'], d['day'], d['actor'])]
        if 'ScriptTimeout' in line: a[1] += 1
        else: a[2].append((t(m) - d['h']) * 1000)
        d.pop('h', None)
ops = set(sys.argv[1].split(','))
for k in sorted(samples):
    if k[0] not in ops: continue
    n, to, v = samples[k]
    print(k, 'n', n, 'timeouts', to, 'median', round(statistics.median(v), 1) if v else None, 'max', round(max(v), 1) if v else None)
```

Inc A retires this script: the same numbers arrive on the step-5 line and in `step5-latency`.

---

### Fire brief (build note, 2026-09-06 — Lattice steward, remote)

**1. Scope sentence (verbatim, §3 + the board row).** *"The wall's second axis becomes observable: per-op
wall time + live read/listing counts on the step-5 log line and the timeout path; a `step5-latency` Health KV
signal beside `step3-latency`; and the `processor-event` rollup arm that makes both visible to `lattice
health summary`. Clinic's one mechanical dedup ships, with the `(d)`→`(e)` annotation correction beside it."*
Board: *"ships step-5 wall/read telemetry (log + `step5-latency` + summary arm), clinic's doubled
`withProvider` listing, the re-measure at head."* Green bar: §11's gates; Inc C's probes are already recorded
(§9, all three PASS) — C reduces to the parent's §5.1 pointer + the board flip.

**2. Verified touch-list (live at `9b053cd`).**
- `internal/processor/script_read_record.go:49-54` recorder struct; `:127-150` `record()`; `:179-189`
  `ScriptReadRecord`. Every literal in the tree is field-keyed (`:129,144` + 15 in
  `read_drift_guard_test.go`) — two int fields break nothing.
- `internal/processor/starlark_kv.go:125` live GET → `sc.ReadRecorder.recordLiveRead` (`:135,144`); `:219`
  listing → `recordEnumeration` (`:262`). Counters increment beside these calls.
- `internal/processor/step5_execute.go:11-14` `ExecutorImpl{Runner, Logger}`; `:45` `Runner.Run`; `:50` error
  return; `:53-58` the step-5 line. `ScriptTimeout` = `*ScriptError` with `Code=="ScriptTimeout"`
  (`starlark_runner.go:76,212`).
- `internal/processor/latency_ring.go:30,40,51,75` — `newLatencyRing`, `record`, `snapshot`, `ringPercentile`.
  Reused for the wall; the read/listing means need per-sample counts, so the executor owns a small
  three-column ring of its own with the same overwrite semantics (§5 table unchanged).
- `internal/processor/health.go:183` `AttachCapabilityAuthorizer`; `:191` `AttachDDLCache` ("attached by
  MakePipeline"); `:442-465` `emitCapabilityAuthSignals` — the emit shape to mirror.
- **Rotted citation:** §4.1 wires `AttachExecutor` in `cmd/processor/main.go:213`. The executor is built
  inside `MakePipeline` (`commit_path.go:1351`) and never reaches `main.go`; the heartbeater is built at
  `:1283` in the same function. **Decision:** attach in `MakePipeline`, mirroring `AttachDDLCache`. Body §4.1
  amended.
- `cmd/lattice/health/health.go:92-95` `classifyKey` → `processor-event`; `:256-504` `computeSummaryRollup`,
  no arm for the group; `:364-375` the generic green-unless-stale arm to mirror; tests
  `cmd/lattice/health/health_test.go` (`TestHealthSummary_Rollup_StaleYellow:171` the shape to mirror).
- `docs/observability/health-kv-schema.md:80` inventory row; `:502-515` document shape.
- `internal/healthkv/completeness_test.go:145-155` (`//go:build integration`; `make test-health-completeness`
  needs a live stack).
- `packages/clinic-domain/ddls.go:2239` `appointment_provider`; `:2299` `sites_for_provider`; `:2323-2344`
  `appointment_sites` (fallback `atSite` at `:2340`); `:2346-2362` `actor_bound_to_appointment_provider`,
  `(d)` annotation at `:2357-2360`; the five doubled sites `:2931, 3076, 3202, 3397, 3458`, each with the
  provider resolved 1–10 lines above (`:2929, 3074, 3200, 3387, 3456`). `BackfillAppointmentSite:3340-3341`
  calls `sites_for_provider` directly (not a site of the dedup).
- `packages/clinic-domain/manifest.yaml:2` + `package.go:141` `0.34.22` (the design's `0.34.21` moved once
  since; bump to `0.34.23`).
- `internal/testutil/pipeline.go:232` `PipelineConfig`; `:336-340` observers — no config hook exists, so Inc B
  adds an `ExtraScriptReadObservers []processor.ScriptReadObserver` field appended to the multi-observer
  (the `ListCalls` assertion cannot reach `cp.deps` from a package test).
- Censuses re-run: 5 doubled sites ✓ · 1 re-resolve ✓ · 0 lint-pinned helpers ✓ · `identifiedBy` provider
  link declared only by `SetProviderHours`/`SetProviderTimeOff` (`opmetas.go:339,373`) ✓ · baseline row
  `read_drift_baseline.txt:128` ✓ · `ScriptReadObserver` absent from `cmd/processor/main.go` ✓ · `wallMs` /
  `case "processor-event"` absent ✓.

**3. Precedents.** Counters: `recordLiveRead` / `recordEnumeration` (`script_read_record.go`). Ring:
`latency_ring.go` + `CapabilityAuthorizer.LatencyStats`. Emit: `emitCapabilityAuthSignals` (`health.go:442`).
Attach: `AttachDDLCache` (`health.go:191`). Summary arm: the `component-heartbeat` arm (`health.go:364`) with
`observedAt` in place of `heartbeatAt`. Heartbeat test: `TestEmitCapabilityAuthSignals_LiveKV`
(`health_alerts_test.go:196`). Timeout test: `TestKVRead_SlowReadHitsWallBudget` (`starlark_kv_test.go:206`).
Observer test: `TestScriptReadRecord_ObserverSeesOneRecordPerExecution` (`script_read_record_test.go:596`).
Clinic harness: `TestWorkplace_TombstonedProviderConfersNothing` (`provider_tombstone_confinement_test.go:162`).

**4. Increment order + green checks.**
- **A1 (processor, opus — new state):** counters + `ExecutorImpl` clock/ring/counter + step-5 line + aborted
  line + `AttachExecutor` + `step5-latency` emit + `MakePipeline` wiring + schema row/shape + completeness
  entry. `go test ./internal/processor/ -count=1`.
- **A2 (cmd/lattice, sonnet):** the `processor-event` arm + two tests. `go test ./cmd/lattice/... -count=1`.
- **B (clinic, sonnet):** signature + five sites + annotation + version + harness hook + `ListCalls == 3` test.
  `go test ./packages/clinic-domain/ -count=1`; `DIFF_BASE=9b053cd go run ./scripts/lint-package-version.go`.
- **Gates:** `go build ./...`, `make vet`, `golangci-lint run ./...`, every `STRICT=1 scripts/lint-*.go`, then
  against the native stack `make verify-kernel`, `make verify-package-clinic-domain`,
  `make test-health-completeness`; `go test ./... -p 4` with `POSTGRES_TEST_DSN`.

**5. In-scope gotchas.** Health-emission change ⇒ schema doc in the same change (SKILL §4). `null` means at
`count == 0` (Contract #5 §5.4). Counters read off the recorder directly, never `record()` (sorts). A package
content edit ⇒ manifest + `Version` bump. Dossier entries copied: Processor — *a gate's negative test must
first prove its positive vector reaches the gate* (seventh sighting's report shape: every guard beside the test
that reds when it is reverted); *Starlark WALL binds before the live-read budget* (CI masks the wall at 5 s —
the timeout test drives the runner with its own budget). Packages — *census the CHECK, not the wrapper*; *a
guard's OCC rests on whoever writes its read declaration* (the `(e)` annotation states the read stays live,
undeclared, not newly declared). Standing checklist: #1 lifetime table (§5 — carried across executions, never
reset, dies with the instance); #2 censuses re-run above; #3 revert-proof every counter, the ring record, the
timeout increment and the dedup (`ListCalls` 4 → 3); #4 nothing removed; #5 one ring, one writer (the
executor); #6 the `component-heartbeat` arm's `heartbeatAt` is NOT this doc's timestamp — use `observedAt`.

**6. Adjacent finds.** None outside the fire; the `(d)` annotation defect is Inc B's own scope.

**7. Non-goals.** No wall change, no live-read-budget change, no `details` on the `ScriptTimeout` error (§8
row 8), no per-lane split, no step-4 hydration of declared enumerations (§8 row 4), café's Inc A (the parent's
own row), `BackfillAppointmentSite`, `CreateAppointment`'s walk (§4.3).

**Scope-diff gate:** every touch above traces to the scope sentence; the one deviation (attach site) narrows
nothing and substitutes no mechanism. Dependencies: none declared; Inc A/B independent (§11) — verified, no
shared file.
