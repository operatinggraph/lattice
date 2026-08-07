# Lens projection divergence audit — a correctness verdict that does not depend on a successful repair

**Status: ✅ RATIFIED 2026-08-06 (Winston, under delegated authority) — Fire 1 collapses with the sweep-supersession design into ONE fire**
**Author:** Winston (Designer fire, 2026-08-01)

## Ratification (Winston, 2026-08-06 — delegated by Andrew)

Andrew delegated this class of decision in the ratify session: *"Winston can ratify — do what is right long
term, do NOT make decisions based on how many lines of code need to be changed."*

**Ratified, and this design became load-bearing earlier the same session.** Andrew **held** the Contract #6
§6.2 tie-rule amendment that the auth-plane build had staged, on the grounds that the guard's repair path
already exists in the contract and the only reachable divergence-at-tie is provenance drift. What the hold
requires instead is exactly this design's Fire 1: a reconciliation that cannot write must **report that
honestly** rather than return the silent `nil` the sweep books as `Wrote: true` and logs as *"healed a
divergent projection"*. See `auth-plane-projection-latency-design.md` §15.7 for that resolution. So Fire 1
is not merely a health improvement — it is the mechanism a frozen-contract decision now rests on.

**Fire 1 is collapsed with `sweep-rule-snapshot-granularity-design.md` into one fire.** Both rewrite the
same `Reproject` write path and the same `sweep.go` branch structure: this design reshapes what counts as a
verdict, that one adds the `supersededRule` check the CDC path already has and `Reproject` lacks. Landing
them separately would mean two rebases over one another's branch table for no gain, and both are answering
the same underlying defect — a sweep write whose outcome is reported as success when it is not. Neither doc
knew about the other (this one predates that finding by two days), so the collapse is recorded here rather
than in either body.

**Fire 2 (the plain-lens Auditor) is ratified as designed**, and it is the right long-term shape rather
than a nice-to-have: lens health today is liveness-only, so a frozen row renders green — the grounded
incident is 12 `orphanedTaskGrants` rows sitting 12 days stale behind a healthy card (§1.1). A sampled
projected-vs-recomputed verdict is the only signal that can see per-row wrongness, and §4.4's fail-closed
enrolment (refuse with a published reason) is what keeps an unaudited lens from reading as an audited one.

**Two things the fire must carry.** The §1.2 census (65 of 84 lenses with no per-row verdict; 35
plain-nats-kv candidates) is dated 2026-08-01 and the corpus has grown since — re-census at build time, as
the design's own startup-log posture already anticipates. And per the retention-custody design's finding,
Fire 2's enrolment requires `adapter.RowReader`, whose sole implementor is the NATS-KV adapter, so the
GrantTable producers and the Protected secure lens land as `auditEnrolled: false` with a published refusal
— fail-closed and correct, and §4.4 already owns that as an observable follow-on rather than a guess.
**Backlog:** Stream-2 Component maintenance — *[Refractor] Lens health is liveness-only — a frozen row renders green* (★★, M)
**Owning components:** `internal/refractor/{pipeline,projection,health}`, `cmd/refractor/main.go`. Docs: `docs/components/refractor.md`, `docs/observability/health-kv-schema.md`.

---

## For Andrew

**What it does (two lines).** Every per-row correctness verdict the Refractor publishes today is *inferred from a
write that landed* — `healed = count(Reprojection.Wrote)` (`pipeline/sweep.go:385,424`) — so a divergence the
repair path has no transport for is byte-identical, in Health KV, to a converged lens; and 65 of the 84 shipped
lenses have no per-row verdict at all, because the convergence sweep is installed only for actor-aggregate lenses
(`projection/driver.go:417-433`). This design gives the Refractor a **detection verdict independent of repair**:
the sweep learns to say *"I could not verify this anchor"* instead of silently reporting convergence, and a new
read-only **Auditor** gives plain lenses their first correctness signal by re-running the already-shipped
D2-Phase-1 seeded evaluation (`pipeline.seedAnchorFor`) against a bounded page of anchors and comparing the result
with what is stored.

**Architectural fork: none.** No new bucket, no new op, no Core-KV write, no contract seam. Everything lands in
the Health-KV plane the Refractor already writes to (the sanctioned P2 exception) and reuses machinery that
shipped in the last three weeks.

**Frozen-contract change: none required.** Contract #5 §5.4 marks the Refractor metric list *"recommended (not
enforced)"* (`docs/contracts/05-health-kv.md:79`) and §5.5 marks issue codes *"Component-defined"* (`:124`). The
key-level detail lands in the non-frozen `docs/observability/health-kv-schema.md`. **No contract edit is staged
for this design.**

**The one judgment call for you (not a fork).** Increment 2 spends real work on a background truth-check:
per enrolled plain lens, one anchor-type key listing plus a bounded batch of seeded evaluations, on a slow clock.
At the proposed defaults (batch 10, interval 15 min), with a candidate pool of **35** lenses (§1.2) of which some
will refuse, that is **at most ~0.39 seeded evaluations/second cell-wide** — well under what the auth-plane sweep
already runs (25 *full* per-actor evaluations per 60 s, *per auth lens*). I recommend it defaults **on**, because the D1 filter narrowing that shipped 2026-08-01
(`3c7bf182`) deliberately *removes* the incidental recomputes that were the only thing quietly refreshing a stale
plain-lens row, and the ratified auth-plane-latency design names this row as the complement to exactly that
(`auth-plane-projection-latency-design.md:446`). If you would rather it default **off** and be armed per
deployment, that is a one-constant change called out in §6.3 — everything else is unchanged.

---

## 1. Problem & intent

### 1.1 The grounded symptom

Twelve `orphanedTaskGrants` rows sat stale for twelve days on the dev stack while the lens card rendered green.
`orphanedTaskGrants` is an actor-aggregate lens (`packages/orchestration-base/lenses.go:82-96`, anchor `task`,
key pattern `orphanedTaskGrants.{actorSuffix}`) on the shared `weaver-targets` bucket, and it was **enrolled in
the convergence sweep** the whole time — `sweepEnrolment` grants it a plan (derivable prefix, round-tripping
`AnchorFromKey`, a `PrefixKeyLister` target). The sweep ran, examined those anchors, and reported convergence.

The *repair* half of that incident is closed: `16d3b328` (2026-08-01) shipped doc-mode zero-row retraction, so an
anchor whose filtering `WHERE` stops matching now synthesises a Delete on the CDC, fan-out and sweep paths alike
(`pipeline/evaluate.go:521-531`).

**The reporting half is not closed, and it is the more durable defect.** Before that fix, the sweep's per-anchor
call went:

```
executeFullForActorOnce → zero result rows → results empty
  → Reproject's write loop never iterates
  → Reprojection{Wrote: false, Converged: false, Deleted: false}
  → sweep: `if res.Wrote { healed++ }` → healed stays 0
  → DivergentStreak reset to 0 → CapabilityCoverageDivergence never raised → green
```

The sweep did not fail to *find* the divergence so much as it had **no way to report finding one it could not
write**. Its detection signal is a side effect of its repair. That property survived the fix untouched: the next
divergence class whose repair transport is missing will be reported as health, in exactly the same way, and the
lens will read *healthier the more thoroughly it is broken*.

That inversion is already named in this file's own doc comment, one level up. `SweepStatus`'s repair fields exist
precisely because *"a failed repair heals nothing — folding it into the heal count would clear `DivergentStreak`
and publish a converged lens over a projection that is still broken"* (`sweep.go:58-64`). And `831b0da9` taught the
same lesson at pass granularity — *"a sweep that verifies nothing reports that, not the verdict of the last pass
that ran"* — which is why `LastPassAt` and `Suppression` exist. **Both corrections stop at the pass. An individual
anchor still has no way to say "unverified".** This design finishes that line of work rather than starting a new one.

### 1.2 The second face: 65 of 84 lenses have no per-row verdict at all

| Adapter × projection kind | Count | Per-row correctness verdict today |
|---|---:|---|
| `nats-kv` · actorAggregate | 19 | ✅ convergence sweep (`driver.go:417-433`) |
| `nats-kv` · plain | **35** | ❌ none |
| `nats-subject` · plain (Personal/Edge) | 15 | ❌ none (`Hydrate` is a client-driven refresh, not an audit) |
| `postgres` · plain | 15 | ❌ none |

*(Census over `packages/*/lenses.go`, 2026-08-01.)*

Every one of those 65 has a rich *liveness* surface — `lastProjectedAt`, `projectionLag`, `status`, `alert`,
`consumerLag`, the p95/p99 ring (`lens-projection-liveness-design.md`, shipped). Not one of them can see a **row
that is present and wrong**. The liveness plane answers *"is this lens still moving?"*; nothing answers *"is what
it already wrote still true?"*.

This gap just got wider, on purpose. D1 (`3c7bf182`, 2026-08-01) narrows an eligible lens's `FilterSubjects` to its
referenced labels, and D2 Phase 1 (`787ac1c1`) narrows a seed-eligible lens's *recompute* to one anchor. Both are
correct and both remove **incidental recomputation** — the accident that used to refresh a stale row when some
unrelated event happened to sweep past it. The ratified auth-plane-latency design says so in its own risk table:
*"Fewer incidental recomputes expose a latent lost projection … the broader gap is the separately-filed 'lens health
is liveness-only' row"* (`auth-plane-projection-latency-design.md:446`). This is that row.

### 1.3 Intent, and the vision it serves

Give the Refractor a **detection verdict that is not downstream of a repair**, publish it in the same Health-KV
shape the liveness plane already uses, and extend it from 19 lenses to as much of the corpus as can prove it is
auditable — refusing loudly, never silently, for the rest.

The vault frames the Weaver's role as **"Convergence Audit — identifying resource *Discrepancies*"**
(`Observability/Correction to Observability and Control.md`), and brainstorm **#96 — "Closed-loop Weaver auditor
(reads Health-KV, issues remediation Nudges)"** plus FR54's anomaly tier both *consume* Health KV. The liveness
design declared itself their substrate for **liveness**. They still have nothing honest to read for
**correctness** — today the only live catcher of a wrong row anywhere in the platform is Weaver's
`LensEffectMismatch` (`internal/weaver/health.go:349`), which sees only the handful of lenses that happen to be
Weaver convergence targets, and sees them through the lens of its own reconciler rather than as a projection audit.

---

## 2. Grounding — the machinery this extends

Nothing below is new. Each is read off shipped code, and the design's whole shape is "compose these differently".

| Mechanism | Where | What it gives this design |
|---|---|---|
| Convergence sweep, per-anchor deep verify | `pipeline/sweep.go:361-431` | The pass loop, batch/cursor/backoff discipline, and the actor-level detector for actor-aggregate lenses |
| `sweepEnrolment` fail-closed refusal | `projection/driver.go:309-324` | The enrolment posture to mirror: refuse with a *reason*, never sweep half-blind, and publish the refusal (`sweepEnrolled`) |
| `Reproject` + `rowsEquivalent` | `pipeline/reproject.go:99-235` | The comparison basis (canonical JSON, volatile fields stripped) and the three-caller contract |
| `Reprojection.Wrote / Converged / Deleted` | `pipeline/reproject.go:45-60` | The verdict fields that must become exhaustive |
| **D2 Phase 1 seeded evaluation** | `pipeline/pipeline.go:488-498`, `evaluate.go:174-175` | A **plain** lens's evaluation constrained to one anchor vertex — the primitive that makes a per-anchor plain audit affordable at all |
| `AnchorProjectionKey` / `AnchorDeleteResult` | `ruleengine/full/anchor_delete.go:40-61` | Read-free derivation of the row key an anchor owns — the should-not-exist direction |
| `fetchVertexProps` → `executeFullForActor` | `evaluate.go:864-910` | The already-proven "recompute an arbitrary anchor outside the CDC path" call shape |
| `NatsKVAdapter.GetRow` | `adapter/natskv.go:438-460` | Row read-back with `projectionSeq` stripped and `isDeleted` reported as absent |
| `KV.ListKeysFilter(filter, cursor, limit)` | `substrate/kv.go:276-300` | Cursored anchor enumeration |
| Liveness health plane | `health/lattice_heartbeater.go:157-260,700-830` | `LensLivenessStatus` + the `Lens*` issue family + the raise-after-N / clear-band debounce |
| `ReferencedLabels()` | `ruleengine/full/labels.go` | The precedent for a small, exhaustive-flagged accessor on `CompiledRule` |

Two properties matter enough to state on their own, because the design rests on them.

**A plain lens's projected row is byte-stable for an unchanged graph — with two named exceptions.** Its row goes
to the adapter unmodified: `executeFullForActorOnce` applies an envelope only when `envelopeFn`/`multiEnvelopeFn`
is installed (`evaluate.go:466-500`), which is the actor-aggregate branch, and the unguarded `NatsKVAdapter.upsert`
marshals the row verbatim (`natskv.go:174-193`). The exceptions are the two evaluation params:

- **`$now` is wall-clock** (`evaluate.go:449`). A cypher referencing it recomputes to a legitimately different row.
- **`$projectedAt` is derived from the *event vertex's* provenance, not the anchor's.**
  `projectedAtFromProvenance` reads `lastModifiedAt`/`createdAt` off the `nodeProps` it is handed
  (`evaluate.go:49-59`), and on the plain CDC path those are the **event** vertex's properties — which, for an
  aspect or link arm, is a *neighbor* of the anchor (`evaluatePlainFromVertex` passes the owner/endpoint vertex as
  the entry). A seeded audit recompute necessarily supplies the **anchor's** props, so a row last written by a
  neighbor event carries a `$projectedAt` the audit can never reproduce.

Both are refused by name in §4.4 rather than tolerated. (`rowsEquivalent` already strips a column literally named
`projectedAt` — `volatileEnvelopeFields`, `reproject.go:69` — but that only covers the un-aliased case, which is
why the refusal is on the *param reference*, not on the column name.)

**The comparison the audit needs already exists inside the adapter.** `NatsKVAdapter.upsert`'s unguarded branch
does read-before-write and reports `UpsertOutcome{Wrote:false}` on byte-identical content (`0ee30f6f`,
`natskv.go:189-191`). The audit is that comparison with the `Put` removed — it is not a new notion of "same".

---

## 3. Reconciliation with the existing mental model

**"Didn't we already build the convergence sweep for this?"** The sweep is a *healer*; its detection is a byproduct
of healing. It covers 19 of 84 lenses, and for those 19 it reports exactly what it managed to write. §1.1 is the
failure mode that leaves. This design does not replace the sweep — it makes the sweep's verdict exhaustive
(Inc 1) and gives the other 65 lenses a detector that never writes at all (Inc 2).

**"Didn't the liveness design close this?"** No, and it said so in its own For-Andrew block: the pure
silent-divergence case *"cannot be turned into a clean auto-alert without a model of expected output"*
(`lens-projection-liveness-design.md`, For Andrew). It resolved that honestly by shipping the **lag** backstop and
surfacing `lastProjectedAt` as an operator-visible freshness number, and explicitly deferred the closed-loop
correctness auditor. **The model of expected output is exactly what D2 Phase 1's seeded evaluation now supplies**,
for the plain corpus, at one-anchor cost — a primitive that did not exist when that design was written. This is
the follow-on it named, unblocked by a perf fire that had nothing to do with it.

**"Does this duplicate Weaver's `LensEffectMismatch`?"** No. Weaver's check compares its *reconciler's* expectation
of a convergence target against the target's rows, for the ~14 lenses that are Weaver targets, and it is scoped to
Weaver's own dispatch semantics (`weaver/health.go:328-352`). It is a consumer-side sanity check, not a projection
audit, and it cannot speak for a lens Weaver does not consume. The audit's verdict is *"the projection disagrees
with the graph"*; Weaver's is *"the target disagrees with what I dispatched against"*. Where both fire on the same
lens they corroborate, and the audit's `divergentRows` gives the Weaver issue something to point at.

**"Does this introduce new state, and do we already keep that state somewhere?"** One new piece of per-lens
in-process state (the audit cursor + counters), persisted exactly where the sweep already persists its own — the
lens's Health-KV entry, via `health.Reporter` (`SetSweepProgress`'s sibling). No new bucket, no Core-KV state, no
new durable.

**"Is this the design-of-record, or a Phase-1 simplification?"** It is a step toward the design-of-record. The
end-state in the vault + #96 is a **closed loop** — the auditor issues remediation Nudges. This design deliberately
stops at *detect and report* for the plain corpus (§8.1 explains why auto-repair on a shared, unguarded target is
the wrong first move), and the loop closes when #96's Weaver auditor consumes the signal. That is a sequencing
decision, not a permanent boundary; the Health-KV shape is chosen so closing it later adds a consumer, not a
producer.

---

## 4. The shape

### 4.1 Increment 1 — `Reproject` reaches an exhaustive verdict

Today `Reproject` returns three independent booleans and the sweep reads one of them. Three outcomes collapse into
two, and the collapse is **toward health**. Replace the inference with an explicit verdict:

```go
// Verdict is Reproject's conclusion for one actor. Every successful call
// reaches exactly one; the zero value is Unverified, so a path added later
// that forgets to conclude reports "I do not know" rather than "converged".
type Verdict uint8

const (
    VerdictUnverified Verdict = iota // no comparison was reached — see UnverifiedReason
    VerdictConverged                 // stored already equalled recomputed, or was correctly absent
    VerdictHealed                    // a divergence was found and written
)
```

`Reprojection` gains `Verdict Verdict` and `UnverifiedReason string`. `Converged`/`Deleted`/`Wrote` stay — the
control-plane RPC and the retry path read them and their meaning is unchanged.

The **fail-closed default is the whole point**: `VerdictUnverified` is the zero value, so the verdict is wrong only
if a future branch actively mislabels itself, never by omission. This is Andrew's *"omission must FAIL CLOSED"*
reflex applied to an observability boundary instead of an authz one, and it is the reason this increment is worth
its (small) size even though §4.2 shows it has **no live victim today**.

Verdict assignment, per branch of `Reproject`'s existing loop:

| Situation | Verdict | Note |
|---|---|---|
| Delete result, row already absent | `Converged` | today's `out.Converged = true` |
| Delete result, written | `Healed` | |
| Upsert result, `present && rowsEquivalent` | `Converged` | today's `out.Converged = true` |
| Upsert result, written | `Healed` | |
| **Zero results, `zeroRowRetraction` armed** | `Converged` | `zeroRowDeleteKey`'s presence check ran and proved absence correct (`evaluate.go:521-531`) |
| **Zero results, doc-mode, retraction not armed** | `Unverified` | reason `"zero rows and no retraction transport for emptyBehavior"` |
| **Zero results, perEntry** | `Converged` | `multiEntryRetractions` runs unconditionally (`evaluate.go:552-557`), so an empty entry set still yields tombstone results — silence here means the prefix diff found nothing to retract |
| Adapter is not a `RowReader` (comparison skipped, unconditional upsert) | `Unverified` | reason `"target cannot read rows back, so a write is not evidence of divergence"` — today unreachable for actor-aggregate (the §6.2 guard forces NATS-KV) but the branch exists in code |

Per-actor errors keep today's path exactly (`noteActorFailure` → `FailingActors` / `LensRepairFailing`); an
errored actor is neither converged nor unverified. `ErrNoOrderingToken` keeps abandoning the pass.

### 4.2 Increment 1 — the sweep counts verdicts, and the heartbeat raises the third one

`SweepStatus` gains, alongside `Reconciled` / `DivergentStreak` / `FailingActors` / `FailedStreak`:

```go
Unverified       int    // anchors in the last pass that reached no verdict
UnverifiedStreak int    // consecutive passes with at least one
LastUnverified   string // the governing reason, so the issue names a cause not a count
```

`pass()` counts `res.Verdict == VerdictUnverified` beside its `healed` counter and hands both to `record`. The
heartbeat gains one issue code per family, mirroring the existing pair exactly:

- `CapabilityAuditUnverified` (auth plane) / `LensAuditUnverified` (business).
- **Severity: `warning` on the first streak; `error` at `capabilityDivergenceErrorStreak` (2) on the auth plane only.**
  Business lenses stay `warning` at every streak length, matching the standing rule that a single frozen business
  lens must not fail the whole Refractor instance (`refractor.md:838`).
- Precedence: **above** `CapabilityCoverageDivergence`/`LensCoverageDivergence` and **below** `*RepairFailing`. An
  unverified anchor is worse than a healed divergence (the sweep does not know what it is looking at) and better
  than a confirmed unrepairable row (which it does know, and cannot fix).

Health-KV fields on both `metrics.capabilityLens.<name>` and `metrics.lensLiveness.<name>`: `unverified` (int),
`unverifiedReason` (string, `""` when zero). `alert` gains the value `unverified`.

**Honest scoping of Increment 1.** All 19 shipped actor-aggregate lenses carry `emptyBehavior: "delete"`
(`lens-projection-liveness-design.md` §15), so `zeroRowRetraction` is armed for every one of them and the
`Unverified` counter is **expected to read 0 across the current corpus**. That is deliberate and it is not
scaffolding: `skip` and `emptyDoc` are supported empty behaviours in `projection/empty.go` *today*, and a lens
author choosing one is one line away from re-creating the twelve-day silence with nothing to warn them. What this
increment buys is that the silence can no longer happen — not that it is happening. It is the smallest mechanism
that ends a class rather than an instance, and it is a prerequisite for Increment 2 (which reports its verdicts
through the same fields).

### 4.3 Increment 2 — the plain-lens Auditor

A per-pipeline `Auditor`, structurally parallel to `Sweeper`, installed by `projection` when a plain lens can prove
it is auditable. **It never writes to the target.**

```go
type AuditPlan struct {
    AnchorLabel string        // the seedable anchor pattern's label (Pipeline.seedAnchorLabel)
    Interval    time.Duration // zero → DefaultAuditInterval
    Batch       int           // zero → DefaultAuditBatch
}
```

**Every pass re-checks its own enrolment before doing anything.** The conjuncts in §4.4 are read off mutable
pipeline fields (`seedAnchorLabel`, `actorEnumerator`, `diffRetraction`, the current adapter), and a lens
hot-reload or an adapter swap can move them under an installed plan. So the Auditor does **not** trust the plan it
was installed with: at the top of each pass it re-evaluates the conjuncts against the live pipeline and, on any
failure, self-suppresses with that reason instead of auditing under a stale shape. This is the same posture
`RequireGuardedAdapter` takes — *"the requirement outlives this adapter instance"* (`driver.go:451-455`) — and it
costs one field read per tick.

**One pass** (`Auditor.pass`), for a batch of anchors starting at the persisted cursor:

1. `keys, next, err := coreKV.ListKeysFilter(ctx, substrate.VertexPrefix+"."+AnchorLabel+".*", cursor, batch)`.
   `next == ""` means the filter is exhausted → the cycle completed; record `CycleCompletedAt`, reset the cursor.
   Publish `auditListingSize` (the matching key count) so a pathologically large anchor type is visible rather
   than merely expensive.

   **This enumeration is by key type, and that is a real coverage boundary, not a formality.** The executor's
   `nodeMatches` admits a vertex whose *body* `class` or `label` equals the pattern label, not only one whose key
   type does (`ruleengine/full/executor.go:562-573`). An anchor bound that way is never enumerated here, so its
   rows are never audited. The consequence is **under-coverage, never a wrong verdict** — but it must be published,
   not assumed away: the pass reports `auditCoverageBasis: "key-type"` alongside `auditCycleCompletedAt`, so
   "audited clean" is readable as the bounded claim it is.
2. For each anchor key `k`:
   - `props, err := p.fetchVertexProps(ctx, k)`.
   - **Tombstoned anchor** (`props == nil`): derive the row key with
     `fullEngine.AnchorDeleteResult(fullCR, k, label, props)`. If it resolves and `GetRow` finds the row **present**,
     that is a divergence — the anchor-tombstone Delete was lost. If it does not resolve, this anchor contributes
     `Unverified` with reason `"tombstoned anchor whose row key is not derivable"`. Continue.
   - **Live anchor**: `results, err := p.executeFullForActor(ctx, k, props, k)` — the third argument is the seed,
     the same call the CDC path makes at `evaluate.go:174-175` with `seedAnchorFor` already having returned `k`.
   - **Should-exist direction**: for each non-delete result, `stored, present, _ := reader.GetRow(ctx, result.Keys)`.
     `!present` → divergence (`missing`). `present && !rowsEquivalent(stored, result.Row)` → divergence (`stale`).
     The comparison is `pipeline.rowsEquivalent` unchanged — canonical JSON with `volatileEnvelopeFields`
     stripped — so the audit and the sweep share one definition of "same row" rather than growing a second.
   - **Should-not-exist direction**: `keys, ok := fullEngine.AnchorProjectionKey(fullCR, k, label, props)`; if `ok`
     and `!resultsContainKeys(results, keys)` and `GetRow(keys)` reports **present** → divergence (`retained`).
     This is the same read-free derivation the CDC filter-retraction check uses (`evaluate.go:191-197`); when it
     returns `ok == false` the anchor is simply not checked in this direction, exactly as the CDC path is not.
   - An evaluation or read **error** contributes `Unverified` with the error text, never a divergence and never a
     clean anchor.
3. Record `Audited` (int), `Divergent` (a per-class map `{missing, stale, retained}`), `DivergentTotal` (int, the
   map's sum — the single number the alert debounce and the operator's first glance both key on), `Unverified`,
   `LastPassAt`, `Cursor`, `CycleCompletedAt`, `ListingSize`, and publish.

   The map and the total are both carried deliberately: a class that never fires must be readable as **absent**
   rather than as `0`, so a direction that silently stops detecting is distinguishable from a direction with
   nothing to find.

**No repair, by design.** §8.1 argues it; the operator's existing remediations are the control-plane `reproject`
RPC and `Rebuild`, and the closed loop is #96's job.

**Suppression** mirrors the sweep verbatim: suppressed while a rebuild is in flight, while the lens is paused, or
when the per-pass enrolment re-check fails — with the reason and its timestamp published, and `LastPassAt`
deliberately left ageing so a held audit does not republish a clean verdict forever (`sweep.go:441-454`).
**`LensAuditStalled`** uses the sweep's own staleness rule verbatim: past **10 audit intervals** with no verdict,
`error` at once when no *fresh* suppression reason explains it, `warning` when one does
(`health/lattice_heartbeater.go`'s `evalSweepStall`). The window is scaled off `Auditor.Interval()`, never a second
independently-tuned constant — the same coupling `sweepLastPassAt` already has.

### 4.4 Enrolment — fail closed, refuse with a reason, publish the refusal

`auditEnrolment(p, desc, adpt)` returns `(AuditPlan, refusal string)`. Every conjunct is a correctness
requirement read off already-shipped predicates, not a heuristic:

| Conjunct | Why | Source |
|---|---|---|
| `p.seedAnchorLabel != ""` | Single-branch, full-engine, derivable anchor pattern. A multi-walk lens has N anchors and one seed cannot speak for all of them. | `pipeline.go:481-493` |
| `actorEnumerator == nil && envelopeFn == nil && multiEnvelopeFn == nil` | An actor-aware/personal evaluation's "anchor" is the actor, not the event vertex; seeding it evaluates the wrong entity. Actor-aggregate lenses are the sweep's, not the audit's. | `seedAnchorFor`, `pipeline.go:496-498` |
| `!p.diffRetraction` | A single-anchor row set reads as "every other anchor's rows are gone" to `applyDiffRetraction`. The audit never writes, but it must not *compute* under a shape whose semantics it would misread. | `pipeline.go:500-502` |
| adapter implements `adapter.RowReader` | Without read-back there is nothing to compare against; an audit that cannot compare would report clean. | only `NatsKVAdapter` today (`natskv.go:438`) |
| `secureDecryptor == nil` | A Secure Lens's declared columns are decrypted before the results reach any write path (Contract #3 §3.10, `evaluate.go:64-69`). A background job with no request context must not re-derive plaintext to compare it. | `pipeline.go:159-161` |
| `!fullCR.ReferencesParam("now")` **and** `!fullCR.ReferencesParam("projectedAt")` | Both make the recompute legitimately differ from the stored row — `$now` because it is wall-clock, `$projectedAt` because the stored value may derive from a *neighbor* vertex's provenance (§2). Either would read divergent forever. | new accessor, §4.5 |

Every conjunct is re-evaluated **at the top of each pass**, not only at install (§4.3), so a hot reload cannot
leave a lens auditing under a shape it no longer has.

A refused lens publishes `auditEnrolled: false` + `auditRefusal: "<reason>"` and **raises no audit verdict and can
never read as audit-stalled** — the same shape `sweepEnrolled: false` already takes
(`health-kv-schema.md:518-520`). *"Not audited"* must be distinguishable from *"audited, clean"*, at every layer;
that is the same fail-closed direction as §4.1's zero value and it is what stops this design's own coverage from
quietly shrinking.

The design deliberately **does not predict how many of the 35 candidate lenses enrol.** The first act of the
increment is a startup census log line plus the per-lens `auditEnrolled`/`auditRefusal` field, so the number is
*observed* rather than asserted. A refusal reason that turns out to dominate is then a filed, grounded follow-on
rather than a guess made now.

### 4.5 The one new primitive: `CompiledRule.ReferencesParam`

```go
// ReferencesParam reports whether the compiled query references the named
// query parameter ($now, $actorKey, …), and whether the walk that decided
// was exhaustive. A non-exhaustive walk reports (false, false) and every
// caller must treat that as "assume it does".
func (cr *CompiledRule) ReferencesParam(name string) (referenced, exhaustive bool)
```

Mirrors `ReferencedLabels()` in shape, file family and exhaustive-flag discipline (`ruleengine/full/labels.go`),
including its lesson: the flag is only as good as the walk, so it must cover **every syntactic position a param can
appear in** — `WHERE`, `RETURN`, a `CASE`, a `WITH` projection, a pattern property map, a `NOT (…)` sub-expression
— and report `exhaustive: false` for any node kind it does not model. `auditEnrolment` treats
`(referenced=false, exhaustive=false)` as a refusal, never as a pass. Four package files reference `$now` today
(`packages/{orchestration-base,…}/lenses.go`), so the carve-out is small and its blast radius is a refusal, not a
wrong verdict.

The Secure-Lens conjunct in §4.4 is deliberately expressed against `secureDecryptor`, an installed-component
check, rather than against the spec — the component is what actually decrypts, and a spec-shaped check would be
the same read-the-declaration-not-the-matcher mistake this accessor's exhaustive flag exists to avoid.

### 4.6 Read path / write path posture

- **P2 (Processor is the sole Core-KV writer)**: honoured trivially. The audit performs **no** graph mutation and
  submits no operation. Its only writes are Health KV — the architecture's stated operational-self-reporting
  exception, through the existing `health.Reporter`, exactly as the sweep's `SetSweepProgress` does.
- **P5 (applications read lens projections)**: not engaged. The Refractor is the projector; reading Core KV to
  recompute a projection is its function, and the audit reuses the reader the sweep already uses (`p.coreKV`,
  `fetchVertexProps`). No `cmd/<app>` gains a Core-KV read.
- **No new engine Core-KV reads.** The standing rule is that Loom/Weaver must not read Core KV; the Refractor is
  not an addressee of it, and this design adds no Weaver/Loom read. #96's Weaver auditor consumes **Health KV**,
  which is precisely why the signal belongs there.
- **Contract #1 key shapes**: unchanged. The audit reads `vtx.<type>.<id>` roots via a subject filter and derives
  target keys through the lens's own compiled derivation. It mints no key.

---

## 5. Contract surface

| Contract | Section | Change or build-to |
|---|---|---|
| #5 Health KV | §5.4 recommended metrics | **Build to.** The list is *"recommended (not enforced)"* (`05-health-kv.md:79`); the new per-lens fields are Refractor discretion. |
| #5 Health KV | §5.5 issue records | **Build to.** `code` is *"Component-defined"* (`:124`). Four new PascalCase codes — `CapabilityAuditUnverified`, `LensAuditUnverified` (Fire 1), `LensProjectionDiverged`, `LensAuditStalled` (Fire 2) — fit the existing schema unchanged. |
| #6 Capability KV | §6.2 ordering token, §6.8 absence semantics | **Build to, untouched.** The audit writes nothing, so no ordering token is involved. `GetRow` already reports an `isDeleted` tombstone as absent, which is §6.8's equivalence. |
| #3 §3.10 Secure Lens | — | **Out of scope by refusal**, as a conjunct in §4.4's table (`secureDecryptor == nil`) — not a build-time addendum. |

**No frozen-contract edit is staged for this design.**

Two **non-frozen doc corrections** ride along, both found while grounding and both currently misleading:

1. `docs/observability/health-kv-schema.md:534` — *"`0` for a lens with no sweeper, which is every non-auth-plane
   target"*. False since `34b13ffd` (2026-07-25): every actor-aggregate lens that can name its rows is enrolled.
2. `internal/refractor/pipeline/sweep.go:265-266` — the *"every non-auth-plane lens"* comment, already flagged stale
   by `auth-plane-projection-latency-design.md:88` and still uncorrected.

---

## 6. Cost, bounds, and defaults

### 6.1 Per pass, per enrolled lens

| Work | Bound |
|---|---|
| Anchor key listing | One `ListKeysFilter` over `vtx.<label>.*` — a server-side subject filter, bounded by the anchor type's population, **not** the keyspace. Its size is published as `auditListingSize` |
| Seeded evaluations | `Batch` (default 10), each constrained to one anchor by D2 Phase 1 |
| Target reads | ≤ `Batch × (rows-per-anchor + 1)` `GetRow` calls |
| Target writes | **zero** |

**The listing is the honest caveat.** `KVListKeysFilter` pages *client-side*: it collects the full matching key set
from JetStream, then slices (`substrate/kv.go:285-299`). So the cursor bounds the expensive downstream work
(evaluations and value reads) but **not** the key enumeration, which costs a full anchor-type listing every tick.
The convergence sweep already pays exactly this, with `limit=0`, every 60 s on the auth plane and every 5 min on
business lenses (`sweep.go:598-601`). At a 15-minute audit interval this is strictly cheaper than machinery already
running, and it is the reason the interval is slow rather than the batch large.

### 6.2 Coverage time — say what the verdict is worth

A lens with `A` anchors completes a cycle in `⌈A/Batch⌉` ticks: at the defaults, 1,000 anchors ≈ 25 hours. The audit
is a **background truth check**, not a latency detector — the liveness plane is the fast signal and keeps its
seconds-scale cadence. `auditCycleCompletedAt` is published for exactly this reason: an operator reading
`divergentRows: 0` must be able to see whether that covers the whole lens or the last 10 anchors of it. A verdict
whose coverage is unstated is the same class of dishonesty this design exists to remove.

### 6.3 Defaults

```go
const (
    DefaultAuditInterval = 15 * time.Minute
    DefaultAuditBatch    = 10
    // AuditEnabledByDefault is the corpus-wide arming switch. False makes
    // auditEnrolment refuse every lens with reason "disabled by deployment",
    // which is a published refusal like any other — never a silent absence.
    AuditEnabledByDefault = true
)
```

Interval and batch are deployment-overridable through the same env path the sweep's interval takes; a zero
`AuditPlan.Interval`/`Batch` selects the default, exactly as `SweepPlan`'s do. **The kill switch is
`AuditEnabledByDefault`, not a zero batch** — a zero batch would resolve back to the default and disable nothing.
Flipping it to `false` is the one-constant "default off" lever the For-Andrew block offers, and because it routes
through `auditEnrolment` the disabled state is still visible per lens rather than looking like a clean audit.

---

## 7. Migration, compatibility, and test strategy

**Migration: none.** No stored shape changes. New Health-KV fields are additive and every consumer
(`cmd/loupe/health.go`, `lattice health summary`, the Lamplighter) reads the map defensively today. A Refractor
built without Increment 2 simply publishes `auditEnrolled: false` for every lens.

**Rollback** is symmetric for both increments: Increment 1 changes accounting only, Increment 2 adds a component
that performs no writes. Setting `DefaultAuditBatch = 0` (or the env override) is a full disable with no residue —
in deliberate contrast to the D1 narrowing, whose revert is asymmetric.

**Tests.**

*Increment 1 (unit, `internal/refractor/pipeline`):*
- Each row of §4.1's table gets a case asserting the verdict, including the two `Unverified` rows built from a
  descriptor with `emptyBehavior: "skip"`.
- **The regression that would have caught the incident:** a doc-mode actor-aggregate pipeline with
  `zeroRowRetraction` *disarmed* (an `emptyBehavior: "skip"` descriptor — the shape a lens author can choose
  today, and the shape every actor-aggregate lens had before `16d3b328`), an anchor whose cypher returns zero
  rows, and a live stored row → the pass publishes `Unverified ≥ 1` and raises `LensAuditUnverified`. The negative
  twin matters as much: the same fixture with retraction *armed* must publish `Unverified == 0` and heal, so the
  test proves the counter discriminates rather than merely fires.
- The zero value of `Verdict` is `Unverified` — a table test over a `Reprojection{}` literal, so a later branch that
  forgets to conclude is caught by the type, not by review.

*Increment 2 (unit + e2e):*
- Enrolment: one case per §4.4 conjunct asserting `(plan zero, refusal non-empty)`, plus a positive case.
- **Determinism pin:** evaluate a plain lens twice against an unchanged graph and assert `rowsEquivalent` on both
  results — the property §2 rests on. Then flip a source aspect and assert it reports divergent.
- Both divergence directions: a hand-corrupted stored row (`stale`), a hand-deleted stored row (`missing`), a row
  left behind after its anchor stopped matching (`retained`), and a row left behind after its anchor was tombstoned.
- `Unverified` on an evaluation error, and the assertion that it is **not** counted as either clean or divergent.
- `ReferencesParam`: `$now` in a `WHERE`, in a `RETURN`, inside a `CASE`, and inside a `NOT (…)`; plus an
  unmodelled node kind returning `exhaustive: false` and the enrolment refusing on it.
- Cursor/cycle: a lens with `3 × Batch` anchors reaches `CycleCompletedAt` after exactly three passes and resets;
  and a restart mid-cycle resumes at the persisted cursor rather than at the head.
- Enrolment re-check: mutate `diffRetraction` on a live pipeline between passes and assert the next pass
  self-suppresses with that reason instead of auditing.
- **Under-coverage is honest, not silent:** an anchor bound by body `class` (key type ≠ pattern label) is *not*
  audited, and the pass still publishes `auditCoverageBasis: "key-type"` — pinning §9.1's finding 2 so a later
  author cannot quietly upgrade the claim.
- **e2e (`internal/refractor`, ephemeral stack):** register a plain lens, project it, corrupt one row directly in
  the target bucket behind the pipeline's back, and assert the Refractor heartbeat raises `LensProjectionDiverged`
  with `divergentRows: 1` — **and that no target write occurred** (the row is still corrupt afterwards). That last
  assertion is the one that pins "detect, don't fix".

**Gates:** `go build ./...`, `make vet`, `golangci-lint run ./...`, `make verify-kernel`, `go test ./...`, and every
`scripts/lint-*.go` gate. No DDL or permission change, so `verify-package-*` is not engaged.

---

## 8. Risks and alternatives

### 8.1 Rejected: let the Auditor repair what it finds

The obvious symmetry with the sweep, and wrong here — for three reasons that compound.

A plain lens's target is **unguarded** (`RequiresGuard = AuthPlane || RequiresGuardedTombstone()`,
`projection/plan.go:82`), so a repair write is unconditional last-writer-wins with no ordering token to keep it
subordinate to a racing CDC event. The sweep's whole safety argument — *"the §6.2 guard keeps the write subordinate
to any real CDC event that races it"* (`reproject.go:73-84`) — is unavailable on this path.

Second, 35 of these lenses share buckets, and a repair derived from a seeded evaluation writes one anchor's rows
into a keyspace it does not exclusively own. The sweep needed `sweepEnrolment`'s three-way ownership proof before it
could write at all; an audit that only *reads* needs none of it, and buying repair means buying that proof back.

Third, and decisively: **the failure this design exists to fix is a repair path that concealed a detection gap.**
Coupling the new detector to a new writer would rebuild the exact structure whose collapse produced the twelve-day
silence. Detection must be able to stand alone or it is not detection.

The remediation path is not absent — it is the operator's existing control-plane `reproject` RPC and `Rebuild`, now
pointed at a lens by name, and it closes automatically when #96's auditor consumes the signal. **Could a variant of
repair beat this?** A *narrow* one might: repair restricted to the `retained` class (a row whose anchor provably
no longer projects), where the correct end state is an unambiguous Delete. I still reject it for now on the
dead-scaffolding test — the class has no observed instance on the plain corpus, and Increment 2's own
`divergentRows` counter is the instrument that would justify it. Deferred with a named trigger: **a plain lens
reporting a sustained non-zero `retained` count.**

### 8.2 Rejected: a lint gate instead of a runtime verdict for §4.1

The lint doctrine is real — *"lint is how agents are actually forced to do the right thing"* — and it applies when
a design establishes an **authored convention**. `emptyBehavior: "skip"` is not a convention violation; it is a
legitimate authoring choice with legitimate uses. What must never be silent is the **audit's inability to verify**
the resulting state, and that is a runtime property of a specific anchor at a specific moment, not a property of the
source text. Per [[enforcement point follows the threat]], the enforcement point is the runtime verdict. A lint here
would default-deny a valid authoring choice while still not catching the case where the transport exists but fails.

### 8.3 Rejected: audit every lens by re-executing the whole cypher

The straightforward reading of "recompute and compare", and unaffordable: a full re-execute is the corpus-wide scan
D2 Phase 1 exists to avoid, and running it per lens on a timer would multiply the cell's steady-state read load by
the lens count. The seeded evaluation is what makes a per-anchor audit cost one anchor's walk. This is also why the
audit's enrolment is *narrower* than "all plain lenses" and why that narrowness is published rather than hidden.

### 8.4 Risk table

| Risk | Mitigation |
|---|---|
| A `$now`-style non-determinism I did not model makes a lens report divergent forever | `ReferencesParam` refuses the known case; the determinism pin (§7) is a required test; and a lens whose `divergentRows` equals its `Audited` count every pass is a distinguishable signature the operator sees immediately — noise, not a wrong repair, because nothing is written |
| Audit load degrades projection latency | Bounded batch, slow clock, zero writes, and the whole thing is strictly cheaper than the sweep already running (§6.1). `DefaultAuditBatch = 0` is a full kill switch |
| The audit's own enrolment silently shrinks as lens shapes evolve | `auditEnrolled`/`auditRefusal` are published per lens and a refusal is never an omission (§4.4). The same failure for the sweep is exactly what `sweepEnrolled` was added for |
| A new `Unverified` warning is noisy on first deploy | It reads 0 across the current corpus by construction (§4.2); if it does not, that is a real finding on day one and precisely the point |
| `AnchorProjectionKey` returning `ok=false` silently drops the `retained` direction | Same posture as the CDC path it mirrors, and the anchor still gets the should-exist check. Not counted as clean *for that direction* — the published `Divergent` map carries per-class counts, so a class that never fires is visible as absent rather than as zero |
| Increment 2's coverage claim is over-read | `auditCycleCompletedAt` (§6.2) is a required field, not optional colour |
| An anchor bound by body `class`/`label` is never enumerated, so a clean verdict over-claims | `auditCoverageBasis: "key-type"` is published on every pass and pinned by a test (§7). Under-coverage never produces a wrong verdict — only a bounded one, stated as bounded. Widening it means a body-scan, which is the corpus-wide cost the seeded evaluation exists to avoid; the honest boundary is the right trade until a lens actually relies on body binding |

---

## 9. Adversarial pass

**Status: run — see §9.1.** Recorded here as discharged, per the Designer's own rule that a self-flagged pre-build
gate is the Designer's obligation and not the Steward's inheritance.

The pass attacked four load-bearing claims — (1) a plain lens's recompute is deterministic and comparable to the
stored row, (2) the seeded evaluation can be driven from outside the CDC path, (3) the enrolment conjuncts are
sound and fail closed, (4) the audit never needs to write — with every objection required to cite
`internal/refractor` code.

### 9.1 Material findings, folded in

Four survived. All four are corrected in the text above; none is left as future work.

1. **`$projectedAt` makes a plain recompute non-deterministic in a way the draft missed entirely.** The draft
   argued determinism from the fact that a plain row reaches the adapter verbatim, and checked only `$now`.
   But `projectedAtFromProvenance` reads provenance off *the props it is handed* (`evaluate.go:49-59`), and on the
   plain CDC path those are the **event** vertex's — a *neighbor* of the anchor for the aspect and link arms
   (`evaluatePlainFromVertex`). A seeded audit necessarily supplies the anchor's props, so any lens returning
   `$projectedAt` whose row was last written by a neighbor event would read divergent **forever**, on every pass.
   → §2 names both exceptions, §4.4 refuses on either param, §4.5's accessor makes the refusal derivable.
   *(The tell I walked past: "the row is written verbatim" is a claim about the write path, and I let it stand in
   for a claim about the evaluation's inputs.)*

2. **The anchor enumeration is unsound as a completeness claim — `nodeMatches` binds by body, not only by key
   type.** `vtx.<AnchorLabel>.*` was presented as "the lens's anchors". The executor also admits a vertex whose
   body `class` or `label` equals the pattern label (`ruleengine/full/executor.go:562-573`), so such an anchor is
   never enumerated and never audited, while `divergentRows: 0` reads as a clean bill of health for the lens.
   → §4.3 step 1 states the boundary, keeps the key-type enumeration (the failure is under-coverage, never a wrong
   verdict), and publishes `auditCoverageBasis` so the claim is readable as bounded. *(This is the recorded
   "a soundness claim needs the MATCHER, not the declaration" lesson, and I re-made it.)*

3. **The enrolment conjuncts are read off mutable pipeline state and were cached at install.**
   `seedAnchorLabel`, `actorEnumerator`, `diffRetraction` and the current adapter are all fields a lens hot-reload
   or a `HotReloadInto` can move (`useFullEngineBranches`, `pipeline.go:401`). A plan installed once would keep
   auditing under a shape the lens no longer has. → §4.3 re-evaluates every conjunct at the top of each pass and
   self-suppresses with the reason, mirroring `RequireGuardedAdapter`'s "the requirement outlives this adapter
   instance".

4. **The stated kill switch did not work.** §6.3 offered `DefaultAuditBatch = 0` as "default off" while
   `AuditPlan.Batch == 0` selects the default — the two resolve in a circle and disable nothing.
   → replaced with an explicit `AuditEnabledByDefault` routed through `auditEnrolment`, so the disabled state is
   *published per lens* rather than looking like a clean audit.

### 9.2 Editorial defects corrected

Also found and fixed: `divergentRows` was typed as an int in §6.2 and a per-class map in §10 (now an explicit map
plus `DivergentTotal`); the code count said "three" against four defined codes; `LensAuditStalled` had no threshold
(now the sweep's 10-interval rule, scaled off the audit's own cadence); the Secure-Lens conjunct was named in §5 but
deferred out of §4.4's table (now in the table); the cost model said "a 50-lens cell" against a 35-lens census; the
listing had no published size (now `auditListingSize`); and the Fire-1 regression test was described as a
before/after-commit proof when it is a descriptor-shape proof (now stated correctly, with its negative twin).

### 9.3 What the pass could not break

- **The core claim.** *The sweep's detection signal is a side effect of a successful write* held from three
  directions: `healed` increments only under `if res.Wrote` (`sweep.go:424`); `Wrote` is set only by an `Upsert` or
  `Delete` that returned nil (`reproject.go:170,203`); `DivergentStreak` resets to 0 whenever `healed == 0`
  (`sweep.go:959-963`). `FailingActors` is not a second path — it is fed by `noteActorFailure`, which requires a
  non-nil error, and a zero-result actor returns none.
- **Claim 4 (the audit never writes) survived a side-effect hunt.** `executeFullForActor` records into
  `p.latencyBuf` — which would have polluted the liveness plane's `metrics.lensLatency` with background evaluations
  — but `SetLatencyBuffer` is called only on the actor-aggregate (`driver.go:405`) and operation-aggregate
  (`cmd/refractor/main.go:933`) paths, so a **plain** pipeline carries no ring and the audit cannot pollute it.
  Footprint validation and its `RecordEvalDriftRequeue` are gated to `envelopeFn/multiEnvelopeFn && authPlane`
  (`needsFootprintValidation`), so neither fires on the audit path either. `writeResults` — the only writer — is
  not on the call path at all.
- **Claim 2 (drive the seeded evaluation from outside the CDC path).** `reprojectActors` already does exactly this
  shape — `fetchVertexProps` → `executeFullForActor` — from the sweep's goroutine (`evaluate.go:864-910`), so
  concurrency with the CDC consumer and the "arbitrary anchor, not an event" call are both existing, exercised
  behaviour rather than new.
- **The perEntry `Converged` row in §4.1.** `multiEntryRetractions` runs unconditionally at the end of
  `executeFullForActorOnce` when `multiEnvelopeFn != nil` (`evaluate.go:552-557`), so an emptied entry set still
  produces tombstone results. Silence there genuinely means the prefix diff found nothing.

---

## 10. Decomposition for the Steward

Two fires, each independently shippable and green. **Neither may start before Andrew ratifies this design.**

### Fire 1 — the verdict is exhaustive, and the third outcome is published

- `pipeline.Verdict` + `Reprojection.Verdict`/`UnverifiedReason`, assigned per §4.1's table, zero value `Unverified`.
- `SweepStatus.Unverified`/`UnverifiedStreak`/`LastUnverified`; `pass()` counts them; `record()` publishes them.
- `LensLivenessStatus` + `CapabilityLensStatus` carry them; `cmd/refractor/main.go`'s two providers populate them
  (both sites, `:551` and `:628`).
- Heartbeat raises `CapabilityAuditUnverified` / `LensAuditUnverified` at the precedence and severities in §4.2;
  `alert` gains `unverified`.
- `docs/observability/health-kv-schema.md` + `docs/components/refractor.md` gain the fields and the codes, **and the
  two stale claims in §5 are corrected in the same fire.**
- Tests per §7's Increment-1 list, including the pre-`16d3b328`-shaped regression.

*Green means:* the full suite passes, `Unverified` reads 0 across the shipped corpus on a live stack, and the
disarmed-retraction unit test proves the counter fires when it should.

### Fire 2 — the plain-lens Auditor

- `full.CompiledRule.ReferencesParam` (+ its exhaustive-flag tests over `WHERE` / `RETURN` / `CASE` / `WITH` /
  a pattern property map / `NOT (…)`).
- `pipeline.Auditor` + `AuditPlan` + `Pipeline.SetAuditPlan`/`Auditor()`, per §4.3, with the sweep's suppression
  and liveness-clock discipline **and the per-pass enrolment re-check**.
- Cursor persistence + restore: a `health.Reporter.SetAuditProgress(cursor, cycleCompletedAt)` beside the existing
  `SetSweepProgress`, and an `Auditor.restore` beside `Sweeper.restore` (`sweep.go:315-338`), so a redeploy resumes
  the cycle instead of re-walking the head forever.
- `projection.auditEnrolment` + installation for a plain lens in the driver's plain-registration path; refusal
  logged and published. `AuditEnabledByDefault` routed through it (§6.3).
- Health-KV fields `auditEnrolled`, `auditRefusal`, `audited`, `divergentRows` (per-class map), `divergentTotal`,
  `unverified`, `auditLastPassAt`, `auditCycleCompletedAt`, `auditCoverageBasis`, `auditListingSize`,
  `auditSuppression`; issue codes `LensProjectionDiverged` and `LensAuditStalled`; `alert` gains `diverged`.
- **`alert` precedence, written down once:** `paused` > `unreadable` > `repair-failing` > `sweep-stalled` >
  `unverified` > `diverged` > `lagging` > `ok`. The field is single-valued and two new values can co-apply, so the
  order ships as a table with a test, not as call-order.
- Startup census log line (§4.4), and a check that `cmd/loupe/health.go`'s lens row renders the two new `alert`
  values rather than falling through to a blank cell.
- Tests per §7's Increment-2 list, including the e2e that asserts the corrupt row is **still corrupt** afterwards.

*Green means:* the full suite passes, the e2e raises `LensProjectionDiverged` on a hand-corrupted row without
writing, and the startup census reports a non-zero enrolled count on the dev stack.

**Sequencing:** Fire 2 depends on Fire 1 (it reports `unverified` through Fire 1's fields). Fire 1 carries value on
its own and may ship alone.
