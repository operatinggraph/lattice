# Structural pause — a self-adjudicating verdict, a preserved cause, and an operable resume path

**Status: Inc 1 + Inc 3 ✅ BUILT (`bb027dc5`, 2026-08-03) · Inc 2 (§4.2) ✅ Andrew-ratified 2026-08-06 — build-ready**
**Author:** Winston (Designer fire, 2026-08-01)

## Ratification of Increment 2 (Andrew, ratify session 2026-08-06)

**Ratified as designed.** §4.2's judgment call is settled in favour of the self-adjudicating verdict: a
structural pause whose own probe can decide the condition must re-probe, on the pattern
`VerifyProtectedTable` already runs on a loop for *infra* pauses. A tier that means "pause until
reconciled" and then never re-checks is a permanent outage wearing a transient's name.

**The board's second row is folded in, not left standing.** *"[Refractor] A structural pause outlives its
own cause with nothing to re-probe or announce it"* (★★, S–M, `📋 ready`) described this same defect from
the symptom side, and its evidence is the necessity argument for this increment: `leaseApplicationsRead`
dead ~23 h after its cause was gone, 10,317 events stranded, and cleared only when a human resumed the
lens. One defect, one fire, one row — the duplicate is struck and its live evidence recorded here.

**Scope reminder for the fire.** §4.2's re-probe is opt-in to `Protected || GrantTable` consumers, the
relapse latch is what keeps a re-probe from flapping, and the recovered-pause health issue is what makes
the recovery visible rather than silent. `StructuralProbe` does not exist in the tree today, so nothing
here is partially built.

> **What is decided and what is not.** §4.1 and §4.3 carry the doc's own verdict *"architectural fork: none,
> frozen-contract change: none required"*, and the For-Andrew block names them as closing the diagnostic and
> operability halves on their own — so the Steward built them (§11). **§4.2 is the one judgment call flagged for
> Andrew** and is NOT built: whether a protected/grant lens may auto-resume once its own probe verifies clean.
> `ConsumerSpec.StructuralProbe` does not exist; every structural pause is still operator-cleared. Ratifying §4.2
> is a self-contained follow-on — nothing in it is needed for what shipped, and nothing that shipped presumes it.

**Backlog:** Stream-2 Component maintenance — *[Refractor] A structurally-paused protected lens has no operable resume path + no diagnostic* (★★)
**Owning components:** `internal/substrate` (ConsumerSupervisor), `internal/refractor/{adapter,health,failure,pipeline}`, `cmd/refractor`, `cmd/lattice/lens`. Docs: `docs/components/refractor-failure-tiers.md`, `docs/components/refractor.md`, `docs/observability/health-kv-schema.md`.

---

## For Andrew

**What it does (two lines).** A lens that structurally pauses today is dead until a human resumes it — but for a
**protected or grant** lens the platform already owns a read-only check that adjudicates exactly the condition that
paused it, and it already runs that check on a loop, for **infra** pauses and at activation only. This design lets a
structural pause run the same probe (with a relapse latch that hands back to the operator after three failed
self-heals), stops the pause's recorded cause from being erased, and ships the `lattice lens resume` that does not
exist today.

**Architectural fork: none.** No new bucket, no op, no Core-KV read or write, no orchestration. One boolean on
`substrate.ConsumerSpec`, one branch in the supervisor's pause machine, two guards in a health setter, one new
Postgres catalog check, one CLI command group mirroring `lattice loom pause|resume`.

**Frozen-contract change: none required.** The failure-tier taxonomy lives in the non-frozen
`docs/components/refractor-failure-tiers.md`, whose Structural row already reads *"pause the affected Lens **until
reconciled**"* (`:16`) — a probe that proves reconciliation is what that sentence always described. Contract #6
§6.14's verify-and-pause posture is **strengthened**: §4.2(e) makes the verifier check a constraint the write path
has always depended on and nobody has ever verified. **No contract edit is staged.**

**One correction to the filed row you should see.** The row asserts *"no bootstrap-class actor holds the
`ctrl.refractor.<lensId>.resume` grant."* That is literally true and **is not the blocker** — `control-operator`
(`packages/control-authz/permissions.go:40`) and `consoleOperator` (`packages/console-operator/manifest.yaml:82`)
both carry it, the dev/demo stacks provision a `consoleOperator` identity and persist its key (`Makefile:738`), and
Loupe ships a working resume button (`cmd/loupe/control.go:67`, `web/js/logic/lens.js:27`). What actually made
resume unreachable **on the demo box** is that `resume` is correctly *not* in Loupe's demo-posture `readOnlyOps`,
and there is no CLI. **I recommend we do not widen the grant to a bootstrap-class actor** — `console-operator`
exists precisely so root is not the routine console actor (`loupe-operator-auth-lift-design.md` mechanism B) — and
instead ship the CLI (§4.3), which stamps that same operator identity. §1.2 grounds this claim by claim.

**The one judgment call for you (not a fork).** §4.2 lets a protected/grant lens **auto-resume** once its RLS and
schema posture verifies clean. Two facts make that a smaller step than it sounds: the probe *is* the fail-closed
security gate (a failing probe keeps the lens dark), and — as §2 G9b establishes — a *manual* resume of a
structurally-paused protected lens **already** re-enters that same gate on the restart path today. So §4.2 removes
the human from a check the human was not performing, rather than removing a check. If you would still rather
protected lenses always await a person, it is one line — `StructuralProbe: false` in `cmd/refractor/main.go` — and
Inc 1 + Inc 3 close the diagnostic and operability halves on their own.

---

## 1. Problem & intent

### 1.1 The live symptom

Filed live (board entry dated 2026-08-02) on the demo box: `clinicAppointmentsRead` and `providerAppointmentsRead`
— both **protected** Postgres read-path lenses, i.e. the clinic vertical's entire appointments read surface — sat
at `status:"paused", pauseReason:"structural", lastError:null`. The clinic app's views were dark, the health card
was red, the card explained itself only with the single word `structural`, and nothing in the platform was going to
change that state.

Three distinct defects compose into that one screenshot. Only one of them is the one the row named.

### 1.2 The filed row, claim by claim

| Filed claim | Verdict | Evidence |
|---|---|---|
| `pauseReason:"structural"` with `lastError:null` — no cause recorded | **TRUE** | `handleDrainOutcome` *does* persist the cause (`consumer_supervisor_pump.go:376`, `persistDominant(..., errString(drainErr))`, and `drainErr` is non-nil on every path that reaches it). It is erased afterwards — §3.1. |
| No bootstrap-class actor holds `ctrl.refractor.<lensId>.resume` | **TRUE but not the blocker** | Granted to `control-operator` (`packages/control-authz/permissions.go:40`) and `consoleOperator` (`packages/console-operator/permissions.go:91`); `make dev-seed-console-operator` provisions an identity holding the latter and persists its key (`Makefile:707-740`). `CapabilityKVChecker.Authorize` is an exact `ctrl.refractor.resume` + `scope=="any"` match with **no wildcard branch** (`internal/controlauth/checker.go:155-161`), so root would need an explicit grant too — a widening this design declines (§7-A2). |
| No CLI subcommand wraps `resume` | **TRUE** | `cmd/lattice/lens` registers `list, activate, deactivate, lag, emit-ddl, reproject` (`lens.go:27-32`) — no `pause`/`resume`/`rebuild`/`health` — while `lattice loom` has both (`loom/loom.go:267,308`). Loupe exposes all six (`cmd/loupe/control.go:67`), but `resume` is deliberately outside `readOnlyOps` (`:68`), so the hosted demo posture refuses it (`demo.go:66`) — correctly. |
| *(not filed)* The structural tier never re-checks a condition the platform can already adjudicate | **The real defect** | §3.2. |

### 1.3 Intent

Make the structural tier mean what `refractor-failure-tiers.md:16` says it means — *"pause the affected Lens until
reconciled"* — by giving "reconciled" a mechanism where one is soundly available, keeping the operator in the loop
for the causes no probe can settle, and keeping the cause legible in both cases.

---

## 2. Grounding ledger

Every row cites the code that **does** the thing, not a comment describing it. Rows marked ⚠ were wrong in the
first draft and are corrected here by the §10 adversarial pass.

| # | Fact | Evidence |
|---|---|---|
| G1 | A structural pause blocks on an operator Resume and **never probes**. | `consumer_supervisor_pump.go:288` — `waitWhilePaused` routes to `runProbeLoop` only when `hasReason(PauseInfra) && !hasReason(PauseManual) && !hasReason(PauseStructural)`; otherwise it blocks on `resumeCh` (`:291-300`). |
| G2 | A restart re-enters the structural pause and still never probes. | `restoreState` (`:450-459`): `PauseStructural` → `waitWhilePaused`, which per G1 blocks. |
| G2b ⚠ | **But** an operator Resume *does* fall through into the activation gate — on the restart path. | `restoreState` is called from `runPump:217`, **before** the `InitialPause` seeding at `:226` (guarded `!st.anyReason()`). `operatorResume` deletes the structural reason (`:120-127`), `waitWhilePaused` returns false, and execution reaches `:226` with an empty reason set — so a protected/grant lens seeds `PauseInfra` and runs `VerifyProtectedTable` before its first projection. The seeding sits **outside** the `for` loop, so this holds only for a pause restored at boot, never for one raised later in the same process. |
| G3 | The structural cause **is** written on the pause. | `handleDrainOutcome` `case ClassStructural` (`:372-376`) → `persistDominant` → `Reporter.SetPaused` writes `LastError` non-nil (`health/reporter.go:146-157`). |
| G4 | …and is then unconditionally nilled on the next clean registration. | `registerWithFilterFallback` calls `reporter.ClearLastError` whenever the first `register()` succeeds (`pipeline/pipeline.go:879-886`); `ClearLastError` nils `LastError` for **any** entry, paused included (`health/reporter.go:268-287`). It runs on every `Pipeline.Run` (`:932-935`) and every `Rebuild` reset. |
| G5 | `ClearLastError`'s own justification is falsified twice. | Its doc claims *"nothing else ever revisits a LastError … RecordError only ever appends"* (`reporter.go:250-256`) — but `SetPaused` writes the field (G3) **and** `SetActive` already nils it (`:110`). It shipped `d040e00a` (2026-08-01), and it is the **only** code path that can produce `paused/structural` with `lastError:null`: the other two nillers (`SetActive:110`, `SetRebuilding:199`) both write a non-paused status, and every other setter (`RecordError`, `SetConsumerLag`, `SetProjectionProgress`, `SetSweepProgress`) preserves the field. |
| G6 | For a **protected** lens, the structural condition is what its `Probe` verifies — for the *provisioned* schema. | `ProtectedAdapter.Probe` → `VerifyProtectedTable` (`adapter/read_path_adapters.go:273-275`), which asserts the table exists, RLS is ENABLEd+FORCEd, every key column and every **declared** body column (`r.Into.Columns`) is present with the platform types, and the §6.14 SELECT policy is intact (`adapter/rls.go:468-563`). The structural classes that pause it are `42P01` / `42703` (`failure/classify.go:177-178`). Boundary in G6b. |
| G6b ⚠ | The write path's column set comes from the **runtime row**, not the declared body. | `postgres.go:148-156` builds `nonKeyCols` by ranging the `row` map. So the probe is complete only while `keys(row) ⊆ keyOrder ∪ Columns` — an invariant nothing enforces. This is a **precondition, not a proof**, and §4.2(c)'s latch is its backstop. |
| G6c ⚠ | Neither verifier checks the unique constraint both write paths depend on. | `postgres.go:230` writes `ON CONFLICT (<keyOrder>)`; `rls.go`'s `UpsertGrant` writes `ON CONFLICT (actor_id, anchor_id, grant_source)`. `VerifyProtectedTable` (`rls.go:468-563`) and `VerifyGrantTable` (`:576-600`) check tables, columns, types and policy — never a constraint or index. A table re-provisioned without it raises `42P10`, which is **not** in the structural set (`classify.go:177-181`) → `CatTransient` → an unbounded Nak loop while health reads `active`. Pre-existing; §4.2(e) closes it. |
| G7 | The grant lens and both NATS adapters have probes that do adjudicate their structural class. | `GrantWriterAdapter.Probe` → `VerifyGrantTable` (`read_path_adapters.go:155`); `NatsKVAdapter.Probe` → `KV.Status` (`natskv.go:516`) → `Conn.KVStatus` (`kv.go:422-435`), which maps `jetstream.ErrBucketNotFound`/`ErrStreamNotFound` to the exact `substrate.ErrBucketNotFound` sentinel `classify.go:167` tests; `NatsSubjectAdapter.Probe` → `js.Stream()` (`natssubject.go:377`). |
| G8 | **Not** the plain Postgres adapter — its Probe is `pool.Ping` and nothing else. | `postgres.go:95-97`. |
| G8b ⚠ | And it cannot be completed cheaply: a plain lens declares no body columns. | `lens/schema.go:79` — `Columns … // declared business columns to provision (**protected only**)`; `cmd/refractor/main.go:728` builds the plain adapter without them, `:735` passes them only on the protected path. **This is why §4.2 opts in `Protected \|\| GrantTable` and nothing else.** |
| G9 | The same absent-table condition at **activation** is infra, and auto-recovers. | `cmd/refractor/main.go:1002-1005` sets `InitialPause: PauseInfra` for `Protected \|\| GrantTable`; `VerifyProtectedTable` returns deliberately **untagged** errors so `Classify` defaults them to transient (`rls.go:462-467`). Table missing at boot → probe loop → auto-resume. Table dropped after boot → write-time `42P01` → structural → permanent (G1, G2). |
| G10 ⚠ | A paused lens is escalated to Health KV with its reason, from **two** sites, and both drop the cause. | Capability-lens path: `lattice_heartbeater.go:687-693` → `issueCapabilityLensPaused` (`:843-847`). Business-lens path — the one `clinicAppointmentsRead` takes: `evalLenses` (`:1066`) → `:1121-1128` → `issueLensProjectionPaused` (`:1171`). Both format `"%s (%s)"` of name + reason only. |
| G11 | Resume is wired end-to-end over NATS. | `Service.ResumeRule` (`control/service.go:576-587`) → `Pipeline.Resume` (`pipeline.go:1820-1828`) → `supervisor.Resume` → `operatorResume` clears manual **and** structural and force-exits a probe (`consumer_supervisor_pump.go:118-127`). Subject `lattice.ctrl.refractor.<lensId>.resume`; response `ResumeResult{Resumed bool}` (`control/controlwire/controlwire.go:132-134`). |
| G12 | `CatPrivacyCritical` has no case in the supervisor adapter and falls through to `ClassTransient`. | `pipeline/supervisor_adapt.go:102-113`. Harmless today — privacy-critical errors are raised at `keyshredded/manager.go:382,399` and routed through `control.PauseRule` → a **manual** pause, never the pump handler — but it is a trap for the next author. Hygiene item (§9 Inc 2f). |
| G13 ⚠ | A structural failure leaves its message **un-acked and un-Nak'd**, so redelivery waits for `AckWait` = **5 minutes**. | `processMsg` returns `disposed=false` for infra/structural and never reaches `applyDecision` (`consumer_supervisor_pump.go:349-360`); `lensAckWait = 5 * time.Minute` (`cmd/refractor/main.go:64`, applied `:1021`); `MaxDeliver` is never set (`consumer_supervisor.go:408`). **This governs the entire self-heal cadence** — §4.2(b). |
| G14 ⚠ | `Reporter.put` is unconditional last-writer-wins. | `reporter.go:444-453` — marshal + `kv.Put`, no CAS, no revision. And every setter stamps `LastUpdated: time.Now()`, so no two writes are ever byte-identical. |
| G15 ⚠ | An operator `Pause` erases whatever cause was there. | `ConsumerSupervisor.Pause` calls `persistPaused(ctx, mc.spec, PauseManual, "")` (`consumer_supervisor.go:281`); `SetPaused` with an empty `lastError` writes `LastError: nil` (`reporter.go:146-149`). `dominantReason` ranks manual above structural (`consumer_supervisor_pump.go:184-189`), so pausing an already-structurally-paused lens destroys its diagnosis. Fourth eraser; §4.1 covers it. |
| G16 | `failure.Classify` tests the **bare** `substrate.ErrBucketNotFound` sentinel, not the `IsBucketNotFound` helper. | `classify.go:167` vs `substrate/errors.go:76-78`. The eval path's Core-KV reads go through `Conn.KVGet` (`kv.go:37-41`), which returns the raw jetstream error — so **no structural error reaches the pump handler from the read path**, and the §4.2 per-adapter framing is sound. Swapping in the helper would silently widen the tier; pinned by a test in Inc 2g. |

---

## 3. The three defects

### 3.1 The pause's cause is erased — by four different paths (G3–G5, G15)

`d040e00a` shipped a genuinely good fix: a lens whose narrowed-filter registration once fell back to the broad
filter latched a `lastError` nothing ever retired, so 31 live lenses rendered fault-red forever. Clearing it on a
clean registration is right.

What it did not model is that `LastError` has other writers. `SetPaused` writes the structural cause (G3), and
`ClearLastError` nils it on the next `Pipeline.Run` or `Rebuild` — deterministically, because registration succeeds
regardless of the pause (`supervisor.Add` creates the durable and spawns the pump; the pause lives in the pump). A
separate path (G15) destroys it whenever an operator pauses a lens that was already structurally paused.

This is the *"a changed component carries silent obligations"* shape: the clear was designed against one producer
of the field and shipped against three.

### 3.2 The structural tier is terminal even where the condition is self-adjudicating (G1, G2, G6–G9)

`refractor-failure-tiers.md:16` says *"pause the affected Lens until reconciled."* The implementation has no notion
of reconciliation: `waitWhilePaused` blocks on `resumeCh` and nothing else, across restarts.

Meanwhile the platform owns a read-only, side-effect-free check that decides whether the condition still holds —
`VerifyProtectedTable`, `VerifyGrantTable`, `kv.Status`, `js.Stream()` (G6, G7) — and already runs it on a loop, for
**infra** pauses.

The asymmetry that produces (G9) is what reads as a bug rather than a policy: a protected lens whose table is
absent **at activation** enters the infra probe loop and self-heals the moment the operator runs
`make provision-readpath`. The *same lens*, if the table is dropped, re-provisioned, or gains a body column after
activation, takes a write-time `42P01`/`42703`, pauses structurally, and stays down through restarts and through
the operator fixing the table. A package upgrade that adds a body column to a protected lens without a matching
`provision-readpath` run is the most likely way to reach that state, and it is a documented footgun already.

The honest scope of the fix is narrower than "structural pauses should probe": it is *"a pause whose probe
adjudicates its own cause should probe"*, which today means **protected and grant lenses** (G8, G8b).

### 3.3 Resume is reachable, but not where it is needed (G11, §1.2)

The op exists, is granted, and Loupe drives it. The gap is the two places an operator actually is when a lens is
down: a terminal (no CLI verb) and a hosted demo console (mutations refused by design, correctly).

---

## 4. The shape

Three increments, each independently shippable and green. No Core-KV read or write is added; every state this
touches is **operational** state in Health KV — the sanctioned P1/P2 exception. Nothing here is a lens, so P5 is not
engaged.

### 4.1 Increment 1 — the pause keeps its cause

Two guards in `internal/refractor/health/reporter.go`, one each for the two erasers that matter:

```go
// ClearLastError, after readExisting:
if existing.PauseReason != nil && *existing.PauseReason == health.PauseReasonStructural {
    return nil // a structural pause's LastError IS its diagnosis
}
```

```go
// SetPaused, when building the entry:
if lastError == "" && existing.Status == "paused" && existing.LastError != nil {
    lastErrPtr = existing.LastError // an empty cause means "no new cause", not "forget the old one"
}
```

**Why `pauseReason == "structural"` and not `status == "paused"`.** A *manual* pause never carries a cause (G15
writes `""` → `nil`), and an *infra* pause at activation is the "not yet provisioned" state the probe will resolve
on its own. For both, the only thing that can be sitting in `LastError` is exactly the stale narrowed-filter
message `d040e00a` exists to retire — so a status-wide skip would reintroduce that regression for every paused
lens. Structural is the one reason whose `LastError` is load-bearing.

**The race is closed by an existing mutex, and the predicate is what needed care.** `spec.Health` wraps the *same*
`*health.Reporter` the pipeline holds (`pipeline.go:925` → `newHealthSink(p.reporter, …)`), so the pump's
`SetPaused`/`SetActive` and `Run`'s `ClearLastError` contend on one `writeMu` (`reporter.go:94,137,183,225,269`) —
read-decide-write is atomic within the one process that owns the lens, and across processes the predecessor is
dead. `put` being last-writer-wins (G14) does not matter here: the serialization is the mutex, not the KV.

**The cause reaches the operator.** Both heartbeater paused branches (G10) append the entry's `lastError`,
truncated — `:693` (capability lenses) and `:1127` (business lenses, the path
`clinicAppointmentsRead` actually takes). `lattice health summary` and Loupe's lens card then render
`clinicAppointmentsRead (structural: ERROR: column "reason" of relation … does not exist (SQLSTATE 42703))`.

*Explicitly not done:* re-persisting the restored cause from `restoreState`. `substrate.HealthSink.Load` returns
`(HealthStatus, PauseReason, error)` with no `lastError` (`consumer_supervisor_spec.go:87`), and widening it would
touch five implementations across bridge, weaver, loom, healthkv and refractor for a belt-and-braces write. The two
guards above close every eraser without it.

### 4.2 Increment 2 — a structural pause probes where the probe can settle it

**(a) Substrate: one field, one branch.**

```go
// ConsumerSpec
// StructuralProbe lets a structural-only pause run the recovery probe loop
// instead of blocking until an operator Resume. Set it ONLY when Probe
// adjudicates the same condition Classify calls structural for this consumer —
// a probe that can pass while the condition holds produces resume/re-pause
// churn instead of recovery. Omission keeps the operator-only behaviour.
StructuralProbe bool
```

`waitWhilePaused` gains one clause: a structural-only pause set (no `PauseManual`) whose spec sets
`StructuralProbe` and a non-nil `Probe` runs `runProbeLoop`. `runProbeLoop` is parameterised with the reason it is
probing so it clears `PauseStructural` rather than the hard-coded `PauseInfra` (`consumer_supervisor_pump.go:401,415`)
— the only refactor that file needs. A manual pause always wins (already dominant, `:181-194`), so an operator
Pause is never probed away.

**Default is off**, which is the strictly-safe direction: omission cannot silently convert an operator-gated pause
into a self-clearing one. Loom and Weaver are untouched — one `false` field they never set.

**(b) The failing message re-tests promptly — the cadence fix (G13).** Left alone, a probe-driven resume would
publish `active` and then wait up to **5 minutes** for `AckWait` redelivery before learning whether the fix took.
That is worse than the honest `paused/structural` it replaces. So: when `StructuralProbe` is set, `processMsg`
**Naks the structural message with a short delay** (the spec's `ProbeInterval`, 10 s) instead of leaving it
silently pending. Nak does not ack and does not advance the ack floor — it only asks for earlier redelivery, and
the pump is paused meanwhile, so nothing is consumed early. The verdict is then re-tested within ~10 s of the
resume, and the window in which the entry reads `active` while still broken shrinks from ~5 min to ~10 s.
Consumers that leave `StructuralProbe` false keep today's leave-pending behaviour byte-for-byte.

**(c) The relapse latch — fail-closed against the probe's residual incompleteness (G6b).** `VerifyProtectedTable`
covers the *provisioned* schema; it cannot see a column the evaluator emits but the lens never declared, nor a
`23502` NOT-NULL / `42804` type / `22P02` parse fault, which are row-data problems no shape check can settle
(`classify.go:179-181`). The latch bounds all of them without classifying any of them:

- `pumpState` counts consecutive structural pauses that followed a **probe-driven** structural resume.
- At `structuralRelapseLimit` (3 — with (b), ≈30–60 s), the pump **latches**: `StructuralProbe` is treated as false
  for the rest of this **worker's** life, the pause becomes operator-only, and the persisted cause is prefixed
  `structural pause latched after 3 self-heal attempts: <err>`, so the operator gets the cause *and* the fact that
  the platform tried and failed.
- An operator `Resume` clears the latch and the counter (`operatorResume`), so a human fix always gets a fresh
  chance.
- The latch is **in-process and per-worker**, not persisted. Per-worker matters only if `Workers > 1` ever reaches
  a lens (today only `internal/processor/lanes.go:105` sets it; every Refractor lens leaves it zero), in which case
  the bound is 3×N and the doc says so. A restart re-arms it: deliberate — a restart is a deploy or an operator
  act, the bound is 3 per restart, and persisting it would need a health-schema field for a state whose whole job
  is to be transient.

**(d) Refractor opts in exactly the set whose probe is a posture verification.**
`cmd/refractor/main.go` sets `StructuralProbe: r.Into.Protected || r.Into.GrantTable`, next to `InitialPause` and
derived from the same `Into` shape. **The plain `PostgresAdapter` is deliberately excluded** (G8, G8b): its probe
is `pool.Ping`, and completing it is not an extraction — a plain lens declares no body columns at all, so
verification would be key-columns-only and would still pass through a `42703`. Opting it in before that is fixed
*is* the churn failure mode. NATS-KV and NATS-subject lenses are also left out for now: their structural class
(bucket/stream absent) is real but has never been observed live, and the dead-scaffolding test says wait for the
consumer.

**(e) The verifiers gain the constraint the write path has always assumed (G6c).** `VerifyProtectedTable` and
`VerifyGrantTable` additionally assert a unique index/constraint exactly covering the `ON CONFLICT` column list
(`pg_index`/`pg_constraint` against `to_regclass`), and `42P10` joins the structural set in `failure/classify.go`.
Without this, the design's own motivating scenario — *the table is dropped and re-provisioned* — can re-provision
without the constraint and produce an unbounded transient Nak loop while health reads `active`: a failure mode the
completeness property must cover for the security argument in the next paragraph to mean anything. This is a
pre-existing hole; it is folded here because it is the same mechanism.

**(f) The recovery is visible, not silent.** A structural pause that self-heals emits a Health-KV issue
`structural-pause-auto-recovered` carrying the lens id, the cause, and the attempt count, live for one heartbeat
cycle. It is emitted **after** the post-recovery activation gate clears, not at the structural clear — because on
the restart path a probe-driven structural resume falls through to `runPump:226` and re-enters the `InitialPause`
infra gate (G2b), which is *correct* (the lens re-verifies before its first projection) but would otherwise show
the operator an `active`→`paused/infra`→`active` flicker. An auto-heal nobody can see is how the *"a frozen row
renders green"* problem in the [divergence-audit design](lens-projection-divergence-audit-design.md) starts; this
design does not add a second instance of it.

**(g) Two hygiene items in the same files.** `classifyForSupervisor` gets an explicit `CatPrivacyCritical` case
(G12) so the fall-through cannot become a real defect later; and a test pins that `failure.Classify` matches the
**bare** `substrate.ErrBucketNotFound` sentinel rather than the `IsBucketNotFound` helper (G16), since swapping
those would silently widen the structural tier to the read path and invalidate the per-adapter framing above.

**Security posture.** A protected lens can only auto-resume by passing `VerifyProtectedTable` — table present, RLS
**ENABLEd and FORCEd**, the §6.14 set-membership policy intact (`rls.go:483-563`), and now the unique constraint.
If the RLS posture regressed, the probe fails and the lens stays dark. The delta versus today is **not** "more
verification than a manual resume" — G2b shows a boot-path manual resume already re-enters the same gate. The
delta is that **no human is required**, and that a same-process structural pause (which G2b does *not* cover,
because the seeding sits outside the `for` loop) gets the gate for the first time.

**Grant-lens safety.** A dropped-and-recreated `actor_read_grants` is empty, so every protected table's §6.14
membership subquery matches nothing → reads fail **closed** (under-grant), never over-grant. Resume replays only
from the un-acked message forward, so prior grants are not restored without a rebuild — a persistent under-grant,
which is the safe direction and is exactly what a manual resume produces today. `DiffRetraction` cannot
over-retract either: `GrantWriterAdapter.ListKeys` refuses an unscoped enumeration outright
(`read_path_adapters.go:144-149`) and otherwise reads the empty table. The `structural-pause-auto-recovered` issue
(f) is what tells the operator a rebuild is still owed.

### 4.3 Increment 3 — `lattice lens pause | resume | rebuild | health`

Four subcommands in `cmd/lattice/lens`, mirroring `cmd/lattice/loom`'s shape and reusing `reproject.go`'s
Refractor request path verbatim (`controlauth.NewActorRequestMsg`, subject
`lattice.ctrl.refractor.<lensId>.<op>`, `control.ControlResponse` decode, `--actor` / `--actor-token` flags
defaulting to the credential file's actor). `reproject.go:102-107`'s `resolveReprojectActorHeader` and
`loom.go:72-77`'s `resolveActorHeader` are already the same six lines twice; Inc 3 lifts one copy into
`cmd/lattice/output` and both groups call it.

Rendering: `resume` prints `lens %q resumed`; `pause` prints `lens %q paused (persists across restart until
resume)`; `rebuild` takes `--truncate` and prints the `RebuildResult`; `health` prints the embedded
`healthwire.Entry` — including `pauseReason` **and** `lastError`, which after Inc 1 makes it the one command that
answers "why is this lens down" without a browser.

**Demo posture is left alone.** `resume` stays out of Loupe's `readOnlyOps` (`cmd/loupe/control.go:28-34` —
omission denies, deliberately). A hosted read-only console should not gain a mutate verb; the operator with NATS
credentials uses the CLI. **No grant change either** — the CLI stamps whichever actor the operator names, and
`consoleOperator`/`control-operator` already carry `ctrl.refractor.resume` (§1.2).

---

## 5. Contract surface

| Surface | Change | Why |
|---|---|---|
| `docs/contracts/06-capability-kv.md` §6.14 | **build to** | The verify-and-pause posture is reused and strengthened (§4.2e adds a constraint check); the §6.14 clauses themselves are unchanged. |
| `docs/contracts/05-health-kv.md` | **build to** | §5.5 issue codes are "component-defined"; `structural-pause-auto-recovered` needs no edit. |
| `docs/contracts/10-orchestration-substrate.md` | **build to** | Carries no pause-tier taxonomy (its only two uses of "structural", `:288` and `:315`, are unrelated). |
| `docs/components/refractor-failure-tiers.md` | **edit (non-frozen)** | The Structural row's "until reconciled" gains its mechanism, the probe-completeness rule, the opt-in set, and the relapse latch. |
| `docs/components/refractor.md` §protected-provisioning, `docs/observability/health-kv-schema.md` | **edit (non-frozen)** | Post-activation recovery parity; the constraint check; the `lastError`-on-structural-pause guarantee. |

**No frozen-contract edit is staged for this design.**

---

## 6. Reconciliation with the existing mental model

**"Didn't we already handle this — the pause machine is three fires old?"** The machine is complete for *infra*
(probe → auto-resume) and for *manual* (operator-only, correctly). Structural was specified as "until reconciled"
and implemented as "until a human". The gap stayed invisible because every structural condition anyone hit in dev
was fixed by re-running `make provision-readpath` **before** the lens activated — which lands in the infra path
(G9), not this one.

**"Doesn't `d040e00a` already own the lastError story?"** It owns the *stale-fault* half and does it well. It has
two other writers it did not model (G5, G15). Inc 1 keeps its behaviour for every non-structural entry.

**"Isn't auto-resuming a security lens exactly what we refuse to do?"** The refusal we ratified is
auto-***provisioning*** — Refractor issues no DDL, ever. Auto-***verifying*** is the mechanism that refusal rests
on: `VerifyProtectedTable` is why an unprovisioned protected lens is dark instead of world-readable. §4.2 runs the
same gate at the same fail-closed polarity in one more place.

**"Does this introduce new state?"** One counter and one latch bit on `pumpState`, beside the `reasons` set and
`reopenFailures` already there. Nothing persisted, nothing in Core KV.

**"Does it collide with another in-flight design?"** Checked against the three open ones. The
[divergence-audit design](lens-projection-divergence-audit-design.md) also writes Health-KV lens issues but is
about *per-row* correctness on **active** lenses; a paused lens is out of its scope by construction (the sweep is
suppressed while paused, `lattice_heartbeater.go:743-744`). The ratified
[auth-plane-latency design](auth-plane-projection-latency-design.md) reshapes auth-lens *consumer filters* — it
touches `ConsumerFilter`, not the pause machine. [edge-cold-signin](edge-cold-signin-delivery-position-design.md)
changes a delivery policy on the SYNC consumer, which sets no `Probe`. No overlap; no consolidation needed.

---

## 7. Alternatives considered

**A1 — Reclassify `42P01`/`42703` as infra instead of structural.** One line, and the demo-box case self-heals
immediately. Rejected: it *lies about the tier* (infra means "the target store is temporarily unavailable"), and it
would apply to plain Postgres lenses too, where `pool.Ping` cannot settle it (G8) — an unbounded probe spin with no
relapse bound and no operator signal. *Could a variant beat §4.2?* Only if every Postgres probe were completed
first, at which point the reclassification buys nothing and loses the operator-visible distinction between
"outage" and "misconfiguration".

**A2 — Grant `ctrl.refractor.resume` to a bootstrap-class actor**, as the filed row implies. Rejected: it widens
root's privilege to fix an availability problem that a CLI plus an already-granted operator identity solves, and it
cuts against `loupe-operator-auth-lift-design.md`'s ratified mechanism B (root is for rare, explicit
pkg-lifecycle acts, not routine console use). If Andrew wants a break-glass path anyway, the cheaper form is
documenting `make dev-seed-console-operator`'s persisted key as *the* CLI operator actor — which Inc 3's help text
does.

**A3 — Add `resume` to Loupe's demo `readOnlyOps`.** Rejected on sight: `resume` mutates, the demo posture's whole
contract is that mutations are refused, and the omission is a deliberate default-deny (`cmd/loupe/control.go:28-34`).
A demo box that can resume a lens can also pause one.

**A4 — Re-tier `23502`/`42804`/`22P02` as Terminal (DLQ)** so only schema-absence stays structural and the probe
is trivially complete. Tempting and possibly right long-term, but it silently *drops* rows: a NOT-NULL violation on
a projected row is far more often a lens/DDL mismatch affecting every row than one bad datum, and DLQ-ing the
stream a row at a time would hide that. The relapse latch handles them with no tier change and no data loss. Filed
as a separate question, not folded in.

**A5 — Persist the relapse latch.** Rejected: a new schema field for a state already bounded at 3 attempts per
process, and re-arming on restart is what an operator restarting a component expects.

**A6 — Skip the whole probe branch: on restore, convert a persisted structural pause into the existing
`InitialPause` gate.** Genuinely tempting — G2b shows the fall-through at `runPump:226` already runs
`VerifyProtectedTable` after a resume, so clearing the structural reason during `restoreState` would be ~3 lines
and would fix the live demo-box case. Rejected on three counts: the seeding sits **outside** the `for` loop, so a
structural pause raised later in the same process is still permanent (the majority case once the box stops
restarting nightly); it silently erases the structural verdict, collapsing "misconfigured" into "not yet
provisioned"; and it inherits no relapse bound, so an unfixable cause loops at the probe interval forever with no
latch and no operator hand-back.

**Dead-scaffolding test.** Every increment has a live consumer today: Inc 1's is the two dark clinic lenses; Inc
2's is the same pair plus every protected lens the next `provision-readpath` drift will pause; Inc 3's is the
operator at a terminal. Nothing here waits on a dependency that does not exist. The deliberate *exclusions* —
plain-Postgres and NATS-target opt-in (§4.2d) — are the same test applied in the other direction.

---

## 8. Migration, compatibility, test strategy

**Migration:** none. No schema, no data, no op. `StructuralProbe` defaults false, so Loom, Weaver, and every
Refractor consumer that does not opt in behave byte-identically — including the Nak-on-structural change in
§4.2(b), which is gated on the same flag. The two currently-paused demo lenses recover on the first Refractor
restart after Inc 2 (restore → structural → probe → `VerifyProtectedTable`), or stay paused with a named cause if
the table is genuinely still wrong — the correct outcome, now legible.

**Tests.**

- *Inc 1, unit* (`health/reporter_test.go`): `ClearLastError` is a no-op on a `structural`-paused entry carrying a
  `lastError`; still clears on `active`, `rebuilding`, `manual`-paused and `infra`-paused entries — the
  `d040e00a` regression guard, extended to the two pause reasons a status-wide guard would have broken.
  `SetPaused("manual", "")` over a structural entry preserves the existing `LastError` (G15).
- *Inc 1, unit* (`pipeline/narrowed_filter_internal_test.go`): a clean registration against a structurally-paused
  entry leaves `lastError` intact — the exact live sequence, at the seam that produced it.
- *Inc 1, unit* (`health/lattice_heartbeater_test.go`): **both** paused branches (`:693` capability, `:1127`
  business) render the truncated cause.
- *Inc 2, unit* (`substrate/consumer_supervisor_structural_test.go`): structural pause + `StructuralProbe:true` +
  a probe failing twice then passing → resumes, and the Nak'd message is redelivered and acked; with
  `StructuralProbe:false` → blocks until `Resume` (today's behaviour, pinned) **and the message is left pending,
  not Nak'd**; a manual pause held alongside a structural one never probes; three probe→resume→re-pause cycles
  latch, and a later `Resume` un-latches. Deterministic sync throughout — channels and condition polling, never
  `time.Sleep`.
- *Inc 2, unit* (`adapter/rls_test.go`): `VerifyProtectedTable`/`VerifyGrantTable` refuse a table whose unique
  constraint on the `ON CONFLICT` columns is absent (§4.2e), and `failure.Classify` maps `42P10` to structural.
- *Inc 2, unit* (`failure/classify_test.go`): `CatPrivacyCritical` maps explicitly (G12); the bare-sentinel
  bucket-error assertion (G16).
- *Inc 2, e2e* (ephemeral stack, `internal/refractor`): a protected lens projects; `DROP` one body column; the next
  event structurally pauses it with a named `lastError`; restore the column; the lens resumes with no operator
  action and the pending row lands **within ~2 probe intervals** (the §4.2b Nak is what makes that assertion
  legitimate — without it the test would have to wait out a 5-minute `AckWait`). Then the negative: leave the
  column dropped, and assert the lens is latched, still paused, and carrying the latch message after the third
  relapse.
- *Inc 3* (`cmd/lattice/lens/lens_test.go`): mirrors `loom_test.go` — a stub responder asserts the subject, the
  `Lattice-Actor` header, and the rendered output for each verb, plus lens-id validation.
- *Gates:* `go build ./...`, `make vet`, `golangci-lint run ./...`, all `scripts/lint-*.go` gates,
  `make verify-kernel`. Inc 2 touches a shared default-path file (`consumer_supervisor_pump.go`) consumed by Loom,
  Weaver, Bridge and the Processor, so per the wide-blast-radius rule it runs the **full** `go test ./...`, not
  just the owning packages.

---

## 9. Decomposition for the Steward

Build in order; each increment is independently shippable and green.

**Inc 1 — the pause keeps its cause (S).** The two `reporter.go` guards; both heartbeater paused branches carry the
truncated cause; docs (`health-kv-schema.md`'s `lastError` row). *Value alone:* the next structural pause is
diagnosable even if nothing else is ever built. Ships without Inc 2.

**Inc 2 — self-adjudicating structural pause (M).** Build order **inside** the fire — do not split (b) from (a),
an opt-in shipped without the cadence fix is a lens that reports `active` for 5 minutes out of every 5 while
broken:
(a) `ConsumerSpec.StructuralProbe`, the `waitWhilePaused` branch, `runProbeLoop` parameterised by reason;
(b) Nak-with-delay on a structural failure when `StructuralProbe` is set (G13);
(c) the relapse counter + latch;
(d) `cmd/refractor/main.go` opts in exactly `Protected || GrantTable` (**not** plain Postgres — G8b);
(e) unique-constraint verification in both verifiers + `42P10` into the structural set (G6c);
(f) the `structural-pause-auto-recovered` health issue, emitted after the post-recovery activation gate clears;
(g) explicit `CatPrivacyCritical` case + the bare-sentinel pin (G12, G16);
(h) docs.

**Inc 3 — `lattice lens pause|resume|rebuild|health` (S).** Mirror `cmd/lattice/loom`; lift the duplicated
`resolveActorHeader` into `cmd/lattice/output`; help text names the operator identity
(`make dev-seed-console-operator`'s persisted key) as the `--actor` to use.

**Revised size: M** (filed as S — the filed row scoped only the diagnostic and the CLI; §3.2 is the larger half).

---

## 10. Adversarial pass — run, and what it changed

Run in-fire against the complete first draft (read-only sub-agent, instructed to refute every ledger row and every
mechanism claim). **The design's own pre-build gate is discharged; nothing is deferred to the Steward.** It
returned two blockers, seven majors and five minors; all are folded above. The four that changed the *shape*, not
just the prose:

1. **The latch bound was wrong by ~30×** and the design would have made the operator signal *worse*. A structural
   failure leaves its message un-acked, and `lensAckWait` is **5 minutes** (G13) — so a probe-driven resume would
   publish `active` and learn nothing for 5 minutes, three times over, ≈15 minutes of a lens reading healthy while
   dark. §4.2(b), the Nak-with-delay, exists entirely because of this finding.
2. **Inc 2's plain-Postgres step was not implementable.** The first draft proposed extracting a `VerifyTableShape`
   and plumbing declared body columns into `NewPostgresAdapter` — but `Into.Columns` is populated for **protected
   lenses only** (G8b), so the completed probe would still have been key-columns-only, i.e. still incapable of
   refusing a `42703`. The opt-in set narrowed to `Protected || GrantTable` and the extraction was dropped.
3. **"A manual resume checks nothing" was false** in exactly the scenario the design is about: on the restart path
   `operatorResume` falls through to the `InitialPause` gate and runs `VerifyProtectedTable` before the first
   projection (G2b). The security argument was rewritten from *"more verification"* to the honest delta, *"no
   human required, plus the same-process pause gets the gate for the first time"* — and G2b also produced
   alternative A6, which had to be argued down rather than ignored.
4. **Two probes verify no unique constraint** although both write paths issue `ON CONFLICT`, and `42P10` is not in
   the structural set (G6c) — so the design's own motivating scenario could re-provision into an unbounded
   transient loop reporting `active`. §4.2(e) folds the fix in.

Findings that were prose-level but material, all corrected in place: `ClearLastError`'s guard had to key on
`pauseReason == "structural"`, not `status == "paused"`, or it reintroduces the `d040e00a` regression for
manually-paused lenses; the `restoreState` re-persist would have widened `HealthSink.Load` across five
implementations and was cut; `Reporter.put` is unconditional last-writer-wins, not a collapsing write (G14); the
heartbeater has **two** paused branches and the business one is the one this design's own example takes (G10); an
operator `Pause` is a fourth eraser (G15); the latch is per-**worker** (G12-adjacent); several line cites were off
by one.

Claims that survived the attack unchanged, verified line by line: G1, G3, G4, G5, G7, G8, G9, G11, G12, G16; the
§4.1 in-process serialization argument; the `NatsKVAdapter` probe-completeness claim; the absence of any
non-adapter structural source on the handler path; every §5 contract verdict; every §1.2 board-row verdict; and the
grant-lens under-grant-not-over-grant analysis in §4.2.

---

## 11. CHECKPOINT — Inc 1 + Inc 3 shipped; Inc 2 is Andrew's

**Shipped** `bb027dc5` (CI green): the two `reporter.go` guards, `LastError` on both heartbeater lens snapshots
carried into the `LensProjectionPaused` message via a shared `pausedLabel`, and
`lattice lens pause | resume | rebuild | health`. The three `resolveActorHeader` copies §4.3 expected to find in
two places turned out to be **three** — `loom`, `weaver` and `lens/reproject` — and all three now call
`output.ResolveActorHeader` / `output.AddActorFlags`. No grant was widened, no op was added to Loupe's
`readOnlyOps`, and `internal/controlauth` is untouched.

**What the live stack proved, and the one thing it could not.** The three read models this design was filed
against — `clinicAppointmentsRead`, `providerAppointmentsRead`, `identityCredentialBindingsRead` — were resumed
from a terminal for the first time and are **active, lag 0, 35 / 35 / 6 rows**, with Refractor reporting zero
health issues where a `LensProjectionPaused` warning had stood. `make provision-readpath` was re-run first, so the
resume met a converged table and stuck rather than re-pausing on the next write.

What it could **not** show is Inc 1's own payload: all three entries render `lastError` absent, because their
causes were erased by the pre-fix `ClearLastError` before the guard existed. **The guard prevents the next
erasure; it cannot recover a destroyed one.** The first structurally paused lens after this commit is where the
preserved cause is observable end-to-end — worth confirming opportunistically rather than manufacturing.

**Two facts for whoever takes §4.2.** The `identityCredentialBindingsRead` resume unblocks
[client-ceremony Inc 2b-3](client-ceremony-op-descriptors-design.md) §16.8, whose pane read was waiting on exactly
this. And the *other* live status defect is untouched and re-confirmed at a second restart: three
`edgeManifest*ReadGrants` lenses still report `rebuilding` at lag 0 — its own board row, not this design's.

---

## 12. Inc 2 fire brief (build note, 2026-08-11)

Phase 0 of the Inc 2 build fire. Two read-only scouts re-verified every §2 ledger cite live; the design was
written against `bb027dc5` (2026-08-03) and the pump file has moved twice since (`e2cff298`, `e107083a`), so
**most pump line numbers in §2 have rotted**. The corrected anchors below are authoritative for this fire.

### 1. Scope sentence (verbatim, §9)

> **Inc 2 — self-adjudicating structural pause (M).** Build order **inside** the fire — do not split (b) from
> (a), an opt-in shipped without the cadence fix is a lens that reports `active` for 5 minutes out of every 5
> while broken: (a) `ConsumerSpec.StructuralProbe`, the `waitWhilePaused` branch, `runProbeLoop` parameterised
> by reason; (b) Nak-with-delay on a structural failure when `StructuralProbe` is set (G13); (c) the relapse
> counter + latch; (d) `cmd/refractor/main.go` opts in exactly `Protected || GrantTable` (**not** plain
> Postgres — G8b); (e) unique-constraint verification in both verifiers + `42P10` into the structural set
> (G6c); (f) the `structural-pause-auto-recovered` health issue, emitted after the post-recovery activation
> gate clears; (g) explicit `CatPrivacyCritical` case + the bare-sentinel pin (G12, G16); (h) docs.

**Green bar:** §8's Inc-2 test list, plus the full `go test ./...` the wide-blast-radius rule mandates
(`consumer_supervisor_pump.go` is consumed by Loom, Weaver, Bridge and the Processor).

### 2. Verified touch-list (checked live 2026-08-11; ✅ = design cite held, ⟳ = rotted, corrected)

| File | Anchor | Design said | Live |
|---|---|---|---|
| `internal/substrate/consumer_supervisor_spec.go` | `ConsumerSpec` struct | — | `:123-198`; `Probe` `:182`, `InitialPause` `:189`, `Health` `:195` |
| ″ | `PauseInfra/Structural/Manual` consts | — | `:51`, `:53`, `:55` |
| ″ | `HealthSink` interface | five implementations (§10) | `:79-88` — confirmed 5: refractor/loom/weaver/bridge + tests |
| `internal/substrate/consumer_supervisor_pump.go` | `pumpState` | `reasons` + `reopenFailures` present ✅ | `:59-112` (`reasons` `:62`, `reopenFailures` `:111`) |
| ″ | `operatorResume` | `:118-127` ⟳ | `:149-156` — deletes `PauseManual` + `PauseStructural` only |
| ″ | `runPump` | `:217`/`:226` ⟳ | `:267`; `restoreState` call `:271`; `InitialPause` seeding `:280-283`, **outside** the `for` at `:285` — G2b holds |
| ″ | `waitWhilePaused` | `:288`/`:291-300` ⟳ | `:346-362`; the infra-only route is `:350-352` |
| ″ | `processMsg` | `:349-360` ⟳ | `:410-423`; structural arm returns `(class, herr, false)` at `:416-418` |
| ″ | `keepAckAlive` | not in design (post-dates it) | `:444-452` — **new since `e2cff298`** |
| ″ | `handleDrainOutcome` `ClassStructural` | `:372-376` ⟳ | `:478-501`, structural case `:486-490` |
| ″ | `runProbeLoop` | `:401`,`:415` ⟳ | `:503-546`; `clearReason(PauseInfra)` at **three** sites `:515` (force-resume), `:528` (probe pass), `:537` (structural escalation) |
| ″ | `restoreState` | `:450-459` ⟳ | `:551-579` |
| `internal/refractor/failure/classify.go` | structural SQLSTATE set | `:177-181` ⟳ | `:171-184` — `42P01,42703,23502,42804,22P02`; **`42P10` absent, confirmed** |
| ″ | bare `ErrBucketNotFound` sentinel | `:167` ✅ | `:167`, `errors.Is`, no helper |
| ″ | `CatPrivacyCritical` const | — | `:16-36` |
| `internal/refractor/pipeline/supervisor_adapt.go` | `classifyForSupervisor` | `:102-113` ✅ | `:101-113` — no `CatPrivacyCritical` case, `default:` → `ClassTransient` |
| ″ | `newHealthSink` | — | `:32` (adapts `*health.Reporter` to `substrate.HealthSink`) |
| `internal/refractor/adapter/rls.go` | `VerifyProtectedTable` | `:468-563` ⟳ | `:514-611` — `pg_class` `:527-551`, `pg_attribute` `:553-589`, `pg_policy` `:591-610` |
| ″ | `VerifyGrantTable` | `:576-600` ⟳ | `:622-649` (method on `*PostgresGrantWriter`) |
| ″ | `UpsertGrant` `ON CONFLICT` | `(actor_id, anchor_id, grant_source)` ✅ | `:341-354`; `RevokeGrant` `:362-375`, same target |
| ″ | no constraint/index check | ✅ | confirmed — `pg_index`/`pg_constraint` appear **nowhere** in `internal/` |
| `internal/refractor/adapter/postgres.go` | protected `ON CONFLICT (<keyOrder>)` | `:230` ✅ | conflict cols built `:186-191`, SQL `:229-236` |
| ″ | plain `Probe` = `pool.Ping` (G8) | `:95-97` ✅ | `:95-97` |
| `internal/refractor/adapter/read_path_adapters.go` | `ProtectedAdapter.Probe`, `GrantWriterAdapter.Probe` | `:273-275`, `:155` ✅ | `:273-275`, `:155` |
| `internal/refractor/health/reporter.go` | `SetPaused` / `SetActive` / `put` | `:146-157`,`:110`,`:444-453` ⟳ | `:193-269`, `:114-188`, `put` `:690-699` |
| `internal/refractor/health/lattice_heartbeater.go` | issue-code consts | — | `:209-225`; business paused issue raised `:1380-1386` in `evalLenses` `:1280` |
| `cmd/refractor/main.go` | `InitialPause` opt-in block | `:1002-1005` ⟳ | `:1450-1457`; spec literal `:1489-1497` |
| ″ | `lensAckWait = 5 * time.Minute` | `:64` ⟳ | `:69`, applied `:1496` |
| ″ | `Into.Columns` protected-only (G8b) | `:728`/`:735` ⟳ | `:1018` plain base, `:1022` returns unwrapped when `!Protected`, `:1025` `NewProtectedAdapter(..., r.Into.Columns)` |
| `internal/refractor/lens/schema.go` | `IntoConfig` / `Columns` comment | `:79` ✅ | `:63-116`, `Columns` `:79` |

**Censuses re-run live (every design count is a premise):** `StructuralProbe` — **0 occurrences** repo-wide, so
nothing is partially built ✅. `42P10` — **0 occurrences** ✅. `pg_index`/`pg_constraint` — **0** ✅.
`Workers:` set on a production `ConsumerSpec` — **exactly one site**, `internal/processor/lanes.go:105`; every
Refractor lens leaves it zero, so the latch's per-worker bound is 3, not 3×N, in this fire's blast radius ✅.

**One scout claim rejected.** The substrate scout's closing note ("a new `StructuralProbe` field would require
updates at 12 production + 28 test construction sites") is wrong: every site is a keyed struct literal, so a
new bool defaults false and compiles untouched. That is precisely why §4.2's default-off is the safe
direction. Its second note — that the field is redundant because `Probe` already escalates — misreads the
mechanism: escalation is *into* structural, this fire is recovery *out of* it.

### 3. Precedents to mirror

- **(a) the probe branch** → `waitWhilePaused:350-352`'s existing infra-only route, extended with one clause;
  `runProbeLoop` already takes the whole `pumpState`, so the reason parameter is a signature change, not a
  restructure.
- **(b) Nak-with-delay** → `substrate.NakWithDelay` + `applyDecision` (`consumer.go:448-462`) already exist and
  are already the pump's transient cadence; `spec.RedeliveryDelay` is the existing floor field. Not greenfield.
- **(c) the latch** → `pumpState.reopenFailures` (`:111`) + `nextReopenDelay`/`resetReopenBackoff`
  (`:191`,`:210`) are the shipped shape for "a per-worker counter reset by success, consulted by the loop".
- **(d) the opt-in** → the `InitialPause` block at `cmd/refractor/main.go:1450-1457`, same `r.Into` derivation,
  same literal.
- **(e) the constraint check** → `VerifyProtectedTable`'s own `pg_attribute` block (`:553-589`) is the
  catalog-query shape; `to_regclass($1)` parameterisation is already established there.
- **(f) the health issue** → `issueLensSecureRedaction` (`lattice_heartbeater.go:209-225` + `:1394`) is the
  closest sibling: a per-lens condition surfaced from a reporter-derived snapshot field into `evalLenses`'s
  `active` map, with an `alert` token.
- **(g)** → the existing `case` arms in `classifyForSupervisor:101-113`.

### 4. Increment order + runnable green checks

Run from the worktree. `POSTGRES_TEST_DSN` is exported by all four CI unit shards
(`.github/workflows/ci.yml:367,407,446,483`), so the Postgres-gated tests are real gates, not local-only.

1. **(a)+(b) together** — the field, the `waitWhilePaused` clause, `runProbeLoop(reason)`, Nak-with-delay.
   `go test ./internal/substrate/ -run 'Supervisor|Structural|Nak' -count=1`
2. **(c)** latch + counter on `pumpState`.  `go test ./internal/substrate/ -count=1`
3. **(e)** constraint verification + `42P10`.
   `go test ./internal/refractor/adapter/ ./internal/refractor/failure/ -count=1`
4. **(g)** `CatPrivacyCritical` case + bare-sentinel pin.  `go test ./internal/refractor/... -count=1`
5. **(d)+(f)** opt-in + the recovered issue.  `go test ./internal/refractor/... ./cmd/refractor/... -count=1`
6. **e2e** (see gotcha 3).  `go test ./internal/refractor/ -run StructuralPause -count=1`
7. **(h)** docs.
8. **Gates:** `go build ./...` · `make vet` · `golangci-lint run ./...` · every `scripts/lint-*.go`
   (`STRICT=1 go run ./scripts/lint-conventions.go` and the eight siblings) · `make verify-kernel` ·
   **full `go test ./... -p 4`**.

### 5. In-scope gotchas

**Design-specific**

1. **G13's premise moved under `e2cff298`, its conclusion did not.** `processMsg` now holds the ack window open
   with `InProgress` heartbeats *while the handler runs* (`keepAckAlive:444-452`). On a structural failure the
   handler has already **returned**, `stopHeartbeat()` fires, and the message then sits un-acked for the full
   `lensAckWait` = 5 min. So §4.2(b) is still exactly the right fix. Same commit dropped the pull prefetch to
   **1 message per worker**, which makes the Nak strictly cleaner: there is no client-side buffer for the
   redelivered message to queue behind.
2. **Do not widen `HealthSink`.** §10 already cut a `Load` widening for this reason (five implementations).
   (f)'s seam is an **optional interface** asserted on `spec.Health` in substrate — only Refractor's
   `newHealthSink` (`supervisor_adapt.go:32`) implements it; Loom/Weaver/Bridge compile untouched. The pump
   latches `pendingAutoRecovered` when a **probe-driven** structural clear happens and fires the callback at
   the next `persistActive`, which is what makes "after the post-recovery activation gate clears" fall out for
   free: on the restart path `runPump:280-283` re-seeds `PauseInfra` before any `persistActive`, so the single
   emission lands after the second probe; on the same-process path there is only one.
3. **The §8 e2e has no existing shape to copy — this is the one greenfield.** Every e2e in `internal/refractor`
   uses `natsfixture` and **no Postgres**; every Postgres test lives in `internal/refractor/adapter` behind
   `skipIfNoPostgres`. The Inc-2 e2e is the first to need both. Build it as
   `internal/refractor/structural_pause_recovery_e2e_test.go`: embedded NATS via `natsfixture`, a real
   protected table via `POSTGRES_TEST_DSN`, the real `ProtectedAdapter.Probe` + real `classifyForSupervisor`
   through a real `ConsumerSupervisor`. Skip when the DSN is absent (CI always has it). Deterministic sync
   only — condition polling, never `time.Sleep`.
4. **Issue-code naming deviates from the design's prose, deliberately.** The design writes
   `structural-pause-auto-recovered`; the codebase splits these two ways (`issueLensProjectionPaused =
   "LensProjectionPaused"` vs the lowercase per-lens `alert` token `secure-redaction`). This fire ships issue
   code **`LensStructuralPauseAutoRecovered`** + alert token **`structural-pause-auto-recovered`**.
5. **The freshness window is 2 heartbeat cycles, not 1.** The design says "live for one heartbeat cycle"; a
   strict one-cycle window can be straddled by jitter and emit **nothing**, which is the silent-auto-heal the
   design exists to refuse. Two cycles guarantees at least one emission and at most two — mirroring the
   shipped `capabilitySweepSuppressionFreshnessCycles = 2` (`lattice_heartbeater.go:200`).
6. **Docs (h) are load-bearing, not decoration.** `refractor-failure-tiers.md:16` (the Structural row) and
   `:81-88` (the "an operator must reach it by hand" paragraph) both become **false** the moment (d) lands —
   amend them in the same commit. Plus `docs/components/refractor.md` protected-provisioning and
   `health-kv-schema.md` (the new issue code + the entry fields (f) adds).
7. **`persistDominant`/`dominantReason` rank manual above structural** (`:235`): a manual pause held alongside
   a structural one must never probe. The clause in (a) tests the whole reason set, not just structural.

**Dossier — `docs/components/substrate.md` (copied in)**

- *A process-local memo of server-owned state must name its invalidation boundary* — the latch is
  process-local by design; §4.2(c) already states the restart re-arm. Say it in the code comment too.
- *A reopen/retry loop needs a connection-closed exit of its own* — the probe loop is a round trip; it already
  exits on ctx, and this fire adds no new loop, but the latch must not convert a connection-closed probe
  failure into a permanent latch. `VerifyProtectedTable` returns **untagged** errors for absence (`rls.go`),
  which `Classify` defaults to transient — the latch counts **structural relapses**, not probe failures.
- *A vendor-behaviour claim in a comment needs a pinned `file:line`* — any AckWait/Nak claim in a new comment
  cites `nats-server`/`nats.go` at our pin.

**Dossier — `docs/components/refractor.md` (copied in)**

- *New pipeline state without a declared lifetime* — the relapse counter and the `pendingAutoRecovered` latch
  are exactly this class. Write the state table (created/reset/carried/ordered at crash, replay, reconnect,
  operator Resume, worker restart) before the first edit.
- *A soundness claim's stated REASON is load-bearing* — §4.2's security argument is "the probe **is** the
  fail-closed gate". State the failure direction explicitly for the constraint check in (e): a missing unique
  index must make the probe **fail** (lens stays dark), never pass.
- *A fail-closed posture proved on the DELIVERY axis is not proved on the PROJECTION axis* — (b) changes
  delivery (Nak) and (a) changes projection (resume). Prove both.
- *One latch guarding two states that commit at different times* — the relapse counter is reset by an operator
  Resume and by a successful drain; pin which, and prove the compared baseline and the acted-on state share a
  commit point.

**Standing checklist** (all six apply; #1, #3 and #4 are the live ones here): new state needs a **lifetime**
(the counter + the latch + `pendingAutoRecovered`); every census is a premise (§2 above re-ran all of them); a
negative test needs its positive vector first (the latch test only means something once the recovery test
passes, and each fix is proven by reverting it and watching its test fail); removal needs a transport and an
observer (nothing is removed here, but (b) changes what happens to a message that was previously left pending
— enumerate every consumer of that behaviour, i.e. every `StructuralProbe:false` consumer must be
byte-identical); one deterministic key one writer (n/a); precedent may carry debt (the `InitialPause` block
being mirrored in (d) is clean — verified above).

### 6. Adjacent finds

- **No `internal/refractor` e2e combines embedded NATS with Postgres.** Absorbed into this fire (gotcha 3) —
  it is the design's own mandated proof, not a new scope.
- **`classifyForSupervisor`'s `default:` arm is the only thing standing between `CatPrivacyCritical` and
  `ClassTransient`.** Absorbed — it is (g).
- Nothing else surfaced that needs Andrew or a designer pass.

### 7. Non-goals (the drift fence)

Plain `PostgresAdapter` opt-in (G8b — explicitly excluded); NATS-KV / NATS-subject opt-in (no live consumer);
any behaviour change for a `StructuralProbe:false` consumer, i.e. Loom, Weaver, Bridge, the Processor and every
non-protected lens (byte-identical, asserted); persisting the latch or the counter; any Core-KV read or write;
any op, bucket or orchestration; any `docs/contracts/*` edit (§5: none required); Loupe's `readOnlyOps`; any
grant widening; the three `edgeManifest*ReadGrants` lenses stuck at `rebuilding` (§11 — their own row).

### Scope-diff gate — PASSED

Parts 2–4 diffed item-by-item against part 1. Every touch traces to a lettered sub-item of §9's Inc 2: (a)→
spec + pump; (b)→`processMsg`/`applyDecision`; (c)→`pumpState`; (d)→`cmd/refractor/main.go:1450`; (e)→`rls.go`
+ `classify.go`; (f)→`supervisor_adapt.go` + `reporter.go` + `lattice_heartbeater.go`; (g)→
`supervisor_adapt.go` + `classify_test.go`; (h)→ the four docs. **No widening and no substituted mechanism.**
Two **narrowings**, both recorded above: the e2e is scoped to the real-probe/real-supervisor seam rather than a
full stack (gotcha 3), and the issue code follows the codebase's naming convention rather than the design's
prose (gotcha 4). Declared dependencies re-verified both ways: Inc 1 and Inc 3 are shipped (`bb027dc5`) and
neither is load-bearing for this green bar; no unlisted dependency surfaced.
