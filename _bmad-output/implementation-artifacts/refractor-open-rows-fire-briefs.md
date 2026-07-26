# Refractor open rows — compiled fire briefs (scouting pass, 2026-07-25)

> ## ✅ All six fires shipped, 2026-07-25
>
> | Fire | Shipped | Outcome against the brief |
> |---|---|---|
> | B | `90d79ff8` | As briefed. `SetKeyPrefix` mirrored `SetGuarded`; bound via `ApplyTruncateScope` at activation **and** on every hot-reload rebuild, which the brief did not call out — a replacement adapter that lost the scoping reaches the same wipe through the swap. |
> | D | `4de52240` | **Premise disproven, as the brief predicted it might be.** The falsification test passed: `Rebuild(truncate=false)` already re-derives absent rows. Collapsed from S–M to a test + the corrected operator warning + docs. No repair mechanism was built. |
> | A | `33a6cc61` | As briefed, no new substrate primitive. The tail-fairness test was **vacuous when first written** — it asserted on the selection, which the deep verify reaches independently, so it passed with the coverage cursor frozen. Rewritten onto the cursor. |
> | C | `7f183d69` | As briefed. The count was already in hand and discarded; the carve-out now keys on *draining* rather than on *being a rebuild*. |
> | F | `e5268c2f` | Reshaped during the build. An enrolled business lens is **guarded**, and the §6.2 guard refuses a token-less write — so the "never projected" fixture the auth-plane precedent uses cannot be healed at all. The test runs the consumer and loses the row out-of-band instead. |
> | E | `a0a4bb34` | Fork resolved to **(a) refuse-and-report**, as the brief recommended. The Refractor half already existed (`298ef8ed` records refusals on health); the built half is the apply-time predictor, constrained by the standing rule that pkgmgr must not import `internal/refractor/lens`. |
>
> **Not built, correctly:** the `cap-read` size bound (Andrew ratified the design `08166be8` and shelved it
> behind showcase completion) and the cross-instance rollup (HA-NATS). Both were named non-buildable here.
>
> The brief below is kept as written, including the parts the build corrected — a scouting pass whose
> misses are edited out teaches nothing about how far ahead of a build one can usefully see.

**Status:** scouting artifact, not a design. Every buildable row below is already ratified or `📋 ready` on
the Lattice board; nothing here proposes new architecture or needs Andrew's signature. The two rows that
*do* need him are named as such and carry no build brief.

**Why this exists.** Nine `[Refractor]` rows are open on [backlog/lattice.md](../planning-artifacts/backlog/lattice.md),
filed as residuals by Fires 6–10 of [lens-projection-liveness-design.md](lens-projection-liveness-design.md).
They kept multiplying because each shipped fire filed its own tail and no pass ever compiled them into
buildable units. This is that pass: the nine rows resolve into **six fires**, of which **five are buildable
today**. Compiled per [agents/fire-brief-template.md](../../agents/fire-brief-template.md) — verified
touch-lists, precedents to mirror, increment order with runnable green checks.

**Grounding basis.** All `file:line` anchors below were re-read live against `main` during this pass, not
copied from the design doc. Anchors that had rotted are flagged where they appear. Baseline `9a4836c8` —
verified to touch no file under `internal/refractor/`, `internal/substrate/` or `cmd/refractor/`, so every
anchor holds as written.

---

## Summary — nine rows, six fires

| # | Fire | Rows folded in | Imp | Size | Buildable? |
|---|---|---|---|---|---|
| A | The sweep costs what it examines | anchor-listing unscoped · `anchorLive` coverage walk | ★★ | S–M | ✅ now |
| B | A shared target is truncated by what the lens owns | shared-bucket rebuild truncate | ★★ | S | ✅ now |
| C | A rebuild says how far it has got | rebuild progress signal | ★★ | S–M | ✅ now |
| D | A diverged grant table — *falsify first* | `actor_read_grants` repair path | ★★ | XS–M | ⚠️ premise check first |
| E | A refused reload leaves a way through | hot-reload package upgrade unapplied | ★★ | M | ✅ now |
| F | A business lens proves it heals | business-lens detect-and-heal e2e | ★★ | S | ✅ now |
| — | `cap-read` size bound | — | ★★ | M | 🚫 Andrew ratification |
| — | Cross-instance latency rollup | — | ★ | S | 🚫 no consumer (HA-NATS) |

**Recommended order: B → A → C → F → E**, with **D's falsification run before D is scheduled at all**.
B first because it is the only live *correctness/outage* hazard in the set; the rest are cost, observability
and proof. D may collapse to a doc+test fire — see below.

---

## Fire B — a shared target is truncated by what the lens owns

**Scope sentence (verbatim, board row).** *"Guarded rebuild forces truncate (`pipeline.go:577`) and
`NatsKVAdapter.Truncate` purges the whole bucket, so rebuilding ONE lens wipes every sibling's keys in the
shared `capability` bucket — auth outage until sweeps heal (~25 actors/min). Fix: prefix-scoped truncate
(keys the lens's `AnchorFromKey` claims)."*

**Sequencing note.** The row reads `seq: subsumed by cap-read design §4.5 Fire 1 if ratified`. That is a
**scheduling** relationship, not a block: [cap-read §4.5](cap-read-per-anchor-grant-keys-design.md) prescribes
exactly this mechanism and would inherit it verbatim. Since cap-read is `📐 awaiting-Andrew` and this is the
one live auth-outage hazard among the nine, **build it standalone now**; §4.5 then has nothing left to do.
Recommend the Steward note that on the cap-read row when B ships.

**Grounded mechanism (verified).**

- `Pipeline.Rebuild` ([pipeline.go:558](../../internal/refractor/pipeline/pipeline.go:558)) forces
  `truncate=true` for any guarded, truncatable adapter at
  [pipeline.go:585-594](../../internal/refractor/pipeline/pipeline.go:585) — **the row's cited
  `pipeline.go:577` has rotted by 8 lines**, the mechanism is unchanged.
- [`NatsKVAdapter.Truncate` (natskv.go:407)](../../internal/refractor/adapter/natskv.go:407) calls
  `a.kv.ListKeys(ctx)` — the **whole bucket, unscoped** — and purges every key returned. On the shared
  `capability` bucket that is every sibling lens's keys.
- The Postgres side already embodies the correct rule: `GrantWriterAdapter` deliberately implements no
  `Truncater` ([read_path_adapters.go:14-16](../../internal/refractor/adapter/read_path_adapters.go:14))
  *because* `actor_read_grants` is shared. The KV side is the same problem with the opposite answer.

**The pieces already exist — this is wiring, not invention.**

- `NatsKVAdapter.ListKeysPrefix` already exists
  ([natskv.go:329](../../internal/refractor/adapter/natskv.go:329)) — the scoped listing Truncate needs.
- `OutputDescriptor.KeyPrefix()` already derives the literal prefix, refusing an unusable one (non-empty,
  `.`-terminated, no NATS wildcard) — [output.go:216-232](../../internal/refractor/projection/output.go:216).
  It is already what `sweepEnrolment` gates on
  ([driver.go:166-181](../../internal/refractor/projection/driver.go:166)).

**Precedent to mirror.** `SetGuarded` — a per-lens value pushed onto the adapter from the lens's own compiled
projection plan ([natskv.go:55-60](../../internal/refractor/adapter/natskv.go:55)). Add `SetKeyPrefix` the
same way, set from the same `desc.KeyPrefix()` the sweep plan already uses
([driver.go:246](../../internal/refractor/projection/driver.go:246)). Do **not** invent a new plumbing route.

**Increment order + green checks.**

- **Inc 1 — the adapter knows what it owns.** `SetKeyPrefix` on `NatsKVAdapter`; `Truncate` uses
  `ListKeysPrefix(prefix)` when a prefix is set, `ListKeys` when it is not (a dedicated bucket keeps today's
  behavior). Green:
  ```bash
  go test ./internal/refractor/adapter/... -run 'Truncate|ListKeysPrefix' -count=1
  ```
- **Inc 2 — wire it at install + pin the invariant.** Set the prefix wherever `SetGuarded` is set. New test:
  two lenses in one bucket, rebuild lens A, assert **B's keys survive** and A's are gone. Green:
  ```bash
  go test ./internal/refractor/... -count=1
  ```

**In-scope gotchas.**

- A prefix-less lens must keep full-bucket truncate — a **dedicated** target legitimately owns everything.
  Silently truncating nothing there would turn a working rebuild into a no-op.
- `Purge` leaves a delete marker as the latest revision, which is what makes the guarded rebuild take the
  absent→Create path ([natskv.go:400-406](../../internal/refractor/adapter/natskv.go:400)). Scoping must not
  change that for the keys it does purge.
- The honest-warning branch at [pipeline.go:590-593](../../internal/refractor/pipeline/pipeline.go:590) is
  about *non-truncatable* guarded targets (the grant family). Leave it alone — it is not this fire.

**Non-goals.** No change to the guard itself, to `Rebuild`'s force logic, or to the Postgres/grant path. Not
the cap-read key split (that is Andrew's to ratify).

---

## Fire A — the sweep costs what it examines

Two rows, one file, one test suite, one cost model — **one fire** per
[[feedback_fewer_larger_fires]]. Both are cost, not correctness; neither is a regression.

**Scope sentences (verbatim, board rows).**
1. *"`ListKeysPrefix("vtx.<type>.")` returns every aspect key as well as every root, once per lens per tick,
   with five lenses sharing `anchorType: identity` and no sharing between them. A `vtx.<type>.*` single-token
   filter drops the aspect keys at the substrate. Cost, not correctness."*
2. *"`anchorLive` is a Core-KV read per *examined* row-less anchor, but only a *selected* one counts against
   the budget — so a large tombstone population is walked and read every tick while selecting nothing."*

**Grounded mechanism (verified).**

- **Row 1.** [`survey` (sweep.go:579-594)](../../internal/refractor/pipeline/sweep.go:579) lists
  `vtx.<AnchorType>.` via `coreKV.ListKeysPrefix`, then discards aspect keys **in Go** at
  [sweep.go:589-592](../../internal/refractor/pipeline/sweep.go:589) via `ParseVertexKey`. Every aspect key
  crosses the wire to be thrown away, once per lens per tick.
- **Row 2.** The coverage walk breaks only when the batch fills
  ([sweep.go:744-746](../../internal/refractor/pipeline/sweep.go:744)); a **tombstoned** row-less anchor
  `continue`s at [sweep.go:753](../../internal/refractor/pipeline/sweep.go:753) **after** paying an
  `anchorLive` Core-KV read ([sweep.go:890](../../internal/refractor/pipeline/sweep.go:890)) without taking a
  slot. So a population that never fills the batch is walked *and read* in full, every tick. Claim confirmed.

**The primitive row 1 needs already exists — this is wiring, not invention.**
`Conn.KVListKeysFilter` ([kv.go:276](../../internal/substrate/kv.go:276)) takes an **arbitrary NATS subject
filter** where `*` matches exactly one token — the general form of `KVListKeysPrefix`, explicitly documented
as such at [kv.go:251-261](../../internal/substrate/kv.go:251). It is already exposed on the handle the sweep
holds: `substrate.KV.ListKeysFilter` ([kvhandle.go:77](../../internal/substrate/kvhandle.go:77)), and
`p.coreKV` is a `*substrate.KV` ([pipeline.go:41](../../internal/refractor/pipeline/pipeline.go:41)).
So row 1 is: `ListKeysPrefix(prefix)` → `ListKeysFilter(ctx, "vtx.<type>.*", "", 0)`. **No new substrate
primitive** — do not file one.

**Precedent to mirror.** The target listing on the very next lines
([sweep.go:597-616](../../internal/refractor/pipeline/sweep.go:597)) is *already* substrate-scoped, with its
narrowing invariant reasoned out in the `survey` doc comment
([sweep.go:563-578](../../internal/refractor/pipeline/sweep.go:563)). Extend that comment to cover the anchor
side; keep the `ParseVertexKey` check as the second gate exactly as `AnchorFromKey` was kept.

**Increment order + green checks.**

- **Inc 1 — list only roots.** Swap `survey`'s anchor listing to the `*` filter. Keep `ParseVertexKey`
  (defence in depth; the filter is a cost mechanism, not an ownership test — the same posture
  [driver.go](../../internal/refractor/projection/driver.go) already took for the target side). Green: a
  real-substrate test proving an aspect key is absent from the returned anchors **and** that the selected
  anchor set is byte-for-byte unchanged.
  ```bash
  go test ./internal/refractor/pipeline/... -run Sweep -count=1
  ```
- **Inc 2 — pay for what you select.** Bound the `anchorLive` walk: cap *examinations* per tick, not just
  selections, and persist the walk cursor so the next tick resumes rather than restarting. Green: a test with
  N tombstoned row-less anchors asserting a bounded read count per tick, and that every anchor is still
  reached across ticks.
  ```bash
  go test ./internal/refractor/... -count=1
  ```

**In-scope gotchas — read these before Inc 2.**

- ⚠️ **[[feedback_randomness_may_be_the_fairness]] applies directly here, and this exact mistake was already
  made once on this exact code.** §11 decision 4 sorted the orphan set and the security review quantified the
  result: a newly-lost grant row went to **~20% chance of ever being retracted**
  ([§11.1](lens-projection-liveness-design.md), lines 477-489). A capped walk **must** carry a cursor that
  advances, or the cap starves the tail the same way. The coverage direction already has `coverage.cursor`
  ([sweep.go:741](../../internal/refractor/pipeline/sweep.go:741)) — use it; do not introduce a fresh
  head-walk.
- `KVListKeysFilter` **sorts and pages**; `KVListKeysPrefix` returns unspecified order. `survey` sorts anchors
  itself at [sweep.go:595](../../internal/refractor/pipeline/sweep.go:595), so ordering is preserved — but
  `limit<=0` returns everything in one page, which is what you want here. Do not half-page it.
- Both listings deliberately return tombstones (`IgnoreDeletes` drops only NATS hard-delete markers, which the
  Processor never writes — [kv.go:271-275](../../internal/substrate/kv.go:271)). The prefilter's liveness
  handling depends on that. **Do not add substrate-level tombstone filtering** — that is the very cost
  `survey`'s comment says it is avoiding.

**Non-goals.** No change to prefilter hint selection, earned-share, or reservations (all shipped `7e6030aa`,
with two of their own premises withdrawn at review — do not re-open). No change to the auth-plane clock.

---

## Fire C — a rebuild says how far it has got

**Scope sentence (verbatim, board row).** *"A rebuild suppresses the sweep, so `CapabilitySweepStalled`
reports it — but only as elapsed time, and cannot tell a rebuild that is draining from one wedged forever
(`watchRebuildCompletion` retries an erroring `OutstandingForConsumer` indefinitely). A rebuild-progress
signal (outstanding count, monotonic) would let the stall detector escalate a wedged rebuild."*

**Grounded mechanism (verified).**
[`watchRebuildCompletion` (pipeline.go:638-671)](../../internal/refractor/pipeline/pipeline.go:638) polls
`OutstandingForConsumer` ([consumer_supervisor.go:362](../../internal/substrate/consumer_supervisor.go:362))
at [pipeline.go:650](../../internal/refractor/pipeline/pipeline.go:650). **The number is already in hand and
is thrown away** — the watcher branches only on `outstanding == 0`
([pipeline.go:658](../../internal/refractor/pipeline/pipeline.go:658)). The error branch
([pipeline.go:651-657](../../internal/refractor/pipeline/pipeline.go:651)) `continue`s forever with no
attempt counter, exactly as the row says. This is the cheapest of the six fires: the signal exists, it is
simply not published.

**Precedent to mirror.** The sweep's own health publication — `Sweeper.record` persists cursor + cumulative
counts to the lens's Health KV entry ([sweep.go:905+](../../internal/refractor/pipeline/sweep.go:905)); the
stall clock and its suppression-cause reporting shipped in `831b0da9`. Publish rebuild progress onto the same
entry and let the existing stall evaluator read it. Do **not** invent a second health channel.

**Increment order + green checks.**

- **Inc 1 — publish the number.** Record `outstanding` (plus a monotonic "last decreased at") on each poll.
  Green: `go test ./internal/refractor/pipeline/... -count=1`
- **Inc 2 — let the stall detector use it.** `CapabilitySweepStalled`'s rebuild carve-out escalates when
  outstanding has not decreased across a threshold window; a draining rebuild stays exempt. Green: mirror
  [caplens_sweep_stall_test.go](../../internal/refractor/health/caplens_sweep_stall_test.go).
  ```bash
  go test ./internal/refractor/health/... -count=1
  ```

**In-scope gotchas.**

- ⚠️ **The stall suite has a proven vacuous-test trap.** `ALensWithNoSweeperIsNeverStalled` and
  `APausedLensIsExemptFromTheStallClock` both passed vacuously because **a single beat stamps the staleness
  baseline at the instant it measures from**, so `elapsed` is 0 whatever the guard does — deleting the guarded
  arm left them green ([§15.1](lens-projection-liveness-design.md), lines 1166-1174). Use the **two-beat**
  sequence, and **mutation-test every new assertion** before declaring it green.
- The no-escalation carve-out is deliberate ([§5.4](lens-projection-liveness-design.md)) — this fire narrows
  it to *wedged*, it does not remove it. A draining rebuild must stay exempt.
- Business-lens sweep verdicts are **`warning`-only, always** (§15 decision 4). If this signal reaches the
  business path, it inherits that ceiling — do not let a wedged business rebuild escalate to `error`.

**Non-goals.** No change to what triggers a rebuild, to `Rebuild`'s truncate logic (that is Fire B), or to the
auth-plane thresholds.

---

## Fire D — a diverged grant table: **falsify the premise before building**

**Board row claim.** *"A grant lens cannot truncate on rebuild (shared table, correctly no `Truncater`) and
the adj-watch bulk re-insert is gone, so rows lost to an out-of-band restore/wipe are re-derived by nothing."*

**⚠️ This scouting pass could not confirm that claim, and the code reads the other way.** Per
[[feedback_verify_blocked_label]] and [[feedback_ground_mechanism_before_premise]], this is stated as a
**hypothesis to test first**, not a finding — nothing here was executed.

**What the code says.**

- `UpsertGrant` ([rls.go:295-308](../../internal/refractor/adapter/rls.go:295)) is
  `INSERT … ON CONFLICT (actor_id, anchor_id, grant_source) DO UPDATE … WHERE EXCLUDED.projection_seq >
  stored`. The monotonic guard lives entirely in the **`DO UPDATE` arm**. For an **absent** row there is no
  conflict, so the plain `INSERT` succeeds **at any seq**.
- A rebuild's replay carries the **real stream sequence**, not 0:
  `results[i].ProjectionSeq = msg.Sequence` ([pipeline.go:1045](../../internal/refractor/pipeline/pipeline.go:1045)).
  So the seq-0 refusal that shipped in `82f52fc4` (reconciliation writes) does not apply to a replay.
- `Rebuild` is reachable for a grant lens: `RegisterRebuilder(r.ID, p)` is called **unconditionally for every
  rule** ([main.go:827](../../cmd/refractor/main.go:827)), and `rebuildRule` dispatches it
  ([control/service.go:792-807](../../internal/refractor/control/service.go:792)).

**Therefore the hypothesis:** `Rebuild(truncate=false)` on a grant lens **already re-derives rows missing from
`actor_read_grants`** — absent rows take the unguarded `INSERT` path, while rows still present replay at their
own seq and are correctly no-ops (`>` is strict). If so, the repair path exists and was never documented.

**Falsification step — run this before scheduling any build.** Write one integration test against a real
Postgres: project a grant lens, `DELETE` a subset of its rows out-of-band, call `Rebuild(ctx, false)`, assert
the deleted rows return and untouched siblings' rows are unchanged.

- **If it passes** → the row is **not** a missing mechanism. The fire collapses to **XS–S**: keep the test as
  the regression pin, document the repair path in
  [docs/components/refractor.md](../../docs/components/refractor.md), and correct the misleading operator
  warning at [pipeline.go:590-593](../../internal/refractor/pipeline/pipeline.go:590) — it tells the operator
  rows "survive the rebuild", which is true for *present* rows and false for *absent* ones, i.e. it actively
  discourages the very action that repairs the table. Then close the row.
- **If it fails** → the test has just isolated the real blocker (most likely candidate: the durable's
  `DeliverLastPerSubjectPolicy` replay not reaching every subject after `supervisor.Reset`). Build the repair
  path against *that*, with the test as the acceptance bar.

Either way the first artifact is the same test. **Do not build a new repair mechanism before running it** —
that is how an adjacent mechanism gets substituted for the real one.

---

## Fire F — a business lens proves it heals

**Scope sentence (verbatim, board row).** *"The auth-plane sweep has one and the plane-independent path is
covered at the pipeline level, but nothing proves the whole chain — enrolled, scoped, healed, siblings
untouched — for a business lens against a real substrate."*

**Precedent to mirror — this is a near-copy, and that is the point.**
[`refractor_capability_sweep_e2e_test.go`](../../internal/refractor/refractor_capability_sweep_e2e_test.go)
is the auth-plane equivalent: same harness, same substrate fixture, same assertions. Port it to a business
actor-aggregate lens on `weaver-targets`. Greenfield is **not** justified here.

**What the test must prove (the four the row names).** Enrolled (a business lens gets a `SweepPlan` — and a
pattern-less one does not) · scoped (its listing returns only its own prefix) · healed (a deleted row is
re-projected within a tick) · **siblings untouched** (a co-tenant lens's rows in the same bucket are
byte-identical afterwards).

**Green check.**
```bash
go test ./internal/refractor/... -run 'Sweep.*E2E' -count=1
```

**In-scope gotchas.**

- ⚠️ **The pipeline fake's `HasPrefix` is subtly more permissive than the substrate filter it stands in for** —
  §15.1 added a real-substrate `ListKeysPrefix` test for exactly this reason (lines 1172-1174). This e2e must
  run against a **real** substrate or it proves nothing about scoping.
- Embedded-NATS fixtures **must** use `jsstore.Dir(t)` ([[project_ci_test_parallelism]]) — and note the open
  `Embedded-NATS shard flakes under parallel load` row: if this new e2e flakes, **tighten, never loosen**
  ([[feedback_flake_may_be_a_real_bug]]).
- Business sweeps run on a **5-minute** interval against the auth plane's 60s (§15 decision 3). Drive the tick
  directly; **never** `time.Sleep` toward it (CLAUDE.md determinism rule).
- Each lens's first tick is **offset by a hash of its rule ID** (§15.1) — a test assuming tick-0 alignment
  will be flaky by construction.

---

## Fire E — a refused reload leaves a way through

**Scope sentence (verbatim, board row).** *"A lens ID is version-independent, so a package upgrade updates the
spec in place — and re-authoring an actorAggregate lens's `Output` is refused whole (correctly; the
alternative half-applied it). Nothing re-activates the lens, so `lattice-pkg apply` reports success while
Refractor serves the old spec until restart."*

**Grounded mechanism (verified).**

- The refusal is [reload.go:100-102](../../cmd/refractor/reload.go:100), one of six sharing
  `reactivateRemedy` ([reload.go:20](../../cmd/refractor/reload.go:20)) — whose text is *"the lens must be
  re-activated (restart Refractor, or delete and re-create the lens definition)"*. The refusal is **correct**
  and this fire does not weaken it.
- The gap is that **nothing performs that re-activation**, and nothing tells `lattice-pkg apply` it is needed —
  so the operator gets a success report over a lens still serving its old spec. The same class of silent pin
  is already called out for the cypher path at
  [main.go:961-964](../../cmd/refractor/main.go:961).
- The shipped shape that triggers it is real, not hypothetical: `b425c25b` re-authored
  `appointmentReminders`' cypher and `BodyColumns` together ([§14.5](lens-projection-liveness-design.md),
  lines 988-998).

**⚠️ This is the one fire in the set that needs a design decision, and it is the lead's, not the builder's.**
§14.5 states the durable fix is *"a new mechanism, not an extension of this one"*. The fork —
**(a)** refuse-and-report (surface the pin to `lattice-pkg apply` so the operator is told, restart stays
manual), vs **(b)** an update the pipeline re-activates *through* (delete-and-reinstall the lens in place).

Per [[feedback_escalate_dont_stop]] this is an impl-level fork Winston resolves, not an Andrew question — but
**resolve it before the builder opens a worktree**, and record it in
[lens-projection-liveness-design.md](lens-projection-liveness-design.md) rather than in a board cell.
Scouting recommendation: **(a) first**, as its own increment. It is strictly smaller, it removes the actual
harm (a false success report), and it is a precondition for (b) either way — (b) without (a) still leaves the
operator unable to tell whether a re-activation happened.

**In-scope gotchas.**

- ⚠️ **The refusal set is closed and was closed at review, twice.** All four guard sources — auth-plane bucket,
  tombstone empty-behavior, `grantTable`, `protected` — are pinned, and `protected` has **no backstop**
  underneath it ([reload.go:112-128](../../cmd/refractor/reload.go:112)). Re-activation must not become a
  path that lands a spec change the refusal set would have rejected. **This is a capability-plane change ⇒
  full 3-layer adversarial review**, per the Steward's own gate.
- `CoreKVSource` records `s.known[lensID] = rule` **before** invoking the callback — a poisoned-baseline bug
  that already shipped once and was fixed by removing the `old` parameter entirely
  ([§14.5](lens-projection-liveness-design.md), lines 953-958). Any re-activation path must not reintroduce a
  last-*seen*-vs-last-*applied* baseline.
- Lens hot-reload needs **no** Refractor restart in general ([[project_lens_hotreload_no_restart]]) — the
  restart in `reactivateRemedy` is specific to the refused set. Do not generalize it.
- A package edit needs a **version bump** or the install no-ops ([[reference_package_edit_needs_version_bump]]) —
  load-bearing when testing this end-to-end via `lattice-pkg apply`.

---

## The two rows with no build brief

**`cap-read` document has no size bound** (★★ M, `📐 awaiting-Andrew`). Design is complete
([cap-read-per-anchor-grant-keys-design.md](cap-read-per-anchor-grant-keys-design.md)) and its adversarial
pass is discharged (§11). It carries a **Contract #6 §6.13/§6.14 edit staged uncommitted** — per CLAUDE.md
that diff *is* the proposal, and it stays uncommitted until Andrew ratifies. **No build brief: this is a
ratification gate, not a readiness gap.** Note that its §4.5 is Fire B above, which is why Fire B is
recommended standalone.

**Cross-instance projection-latency rollup** (★ S, `🚧 seq behind HA-NATS multi-instance`). Block **verified
real**, not stale: Refractor is single-instance today, so per-instance rollup is per-component and the
aggregation has nothing to aggregate. HA-NATS clustering is itself `✅ ratified · 🚧 shelved (prod-HA driver)`.
Its link-tombstone half is genuinely **subsumed** by the link-aspect reprojection design. **Leave parked** —
per [[feedback_verify_blocked_label]] this one earns its label.

---

## Adjacent finds (this pass, not built)

- **`applyDiffRetraction` has no ownership filter at all**
  ([evaluate.go:614](../../internal/refractor/pipeline/evaluate.go:614)) — already filed as a row by §15.1
  and confirmed still open. No live victim (every DiffRetraction lens today has a dedicated target or a
  source-scoped `GrantWriterAdapter`), but nothing *gates* it. **Already filed; not re-filed.**
- **`pipeline.go:590-593`'s operator warning is misleading for absent rows** — folded into Fire D rather than
  filed separately, since D's falsification decides its wording.
- **The board row for Fire B cites `pipeline.go:577`, which has rotted to `:585`.** Cosmetic; the Steward
  should refresh it when B ships rather than spend a row edit now.

No new board rows are needed from this pass — every finding lands inside an existing row or an existing filed
residual.
