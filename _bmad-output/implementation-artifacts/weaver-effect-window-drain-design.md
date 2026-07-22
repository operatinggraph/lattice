# Weaver `__effect` window drain (`resetConfidence` control verb) — design

**Status: 📐 awaiting-Andrew (ratification)** · Designer fire 2026-07-21 · lane row: backlog/lattice.md → Component maintenance → *[Weaver] Drain the `__effect` windows polluted before the attempt-booking fix*

## For Andrew

A new narrow Weaver control-plane verb — `lattice.ctrl.weaver.<targetId>.resetConfidence` (+ `lattice
weaver reset-confidence <targetId>` CLI) — deletes a registered target's `<targetId>.__effect.*`
confidence windows and nothing else, so the fossil windows written by the pre-`5b58f66` bookkeeping
can be drained on the live stack and the standing false `LensEffectMismatch` warnings clear on the
next heartbeat. It fills the exact granularity gap between `disable` (deletes nothing) and `revoke`
(deletes *everything* under the target prefix, with duplicate-userTask fallout — §5.2).

**The board-flagged fork — durable window-age-out vs one-time reset — is resolved: no age-out.**
The ratified planner-mandate §3.2 posture (event-keyed ring, "never clock-sampled; deterministic")
stands untouched; §5.1 gives the four reasons an age-out is the wrong mechanism (dead scaffolding
post-fix, determinism break, true-positive signal weakening, pollution indistinguishable at rest).
**No frozen-contract change** — the verb is additive to the FR30 control plane (component-doc
surface); Contract #10 §10.3/§10.8 are built-to, not edited. One capability-boundary call to
confirm: the new `ctrl.weaver.resetConfidence` operationType is granted to `control-authz` +
`console-operator` **and deliberately NOT to `demo-operator`** (preserving the F20.3 inspect-only
boundary).

## 1. Problem + intent

The `__effect` confidence window (`weaver-state` key `<targetId>.__effect.<gapColumn>.<actionRef>`,
Contract #10 §10.3, ratified with the planner mandate 2026-07-04) is a K=20 FIFO ring of booleans:
`recordEffectDispatch` appends a pending `false` per attempt, `recordEffectClose` flips the oldest
pending slot to `true` when the gap closes, and a **full window with zero closes** raises the
`LensEffectMismatch` warning ("dispatches commit but closes never arrive" —
`internal/weaver/health.go` `flagEffectMismatches`), degrading the Weaver heartbeat.

Before `5b58f66` (2026-07-19) both sides of the bookkeeping were biased **toward that false alarm**:
the sweep's reclaim booked a pending episode for every *collapse-only* re-dispatch (an
assignTask/triggerLoom/proposedOp reclaim the consumer collapses onto the existing artifact — no new
attempt, no second close ever), and only lane-1 credited closes (every sweep-won close was dropped).
A human userTask held open across once-a-minute sweep passes filled its whole window with
unanswerable pendings in ~20 minutes. `5b58f66` + `446b3549` fixed both sides — **new** bookkeeping
is coherent — but neither fix can drain windows already full: the live stack still holds standing
20/0 windows under the `leaseApplicationComplete` target (the lease-signing playbook's
`missing_signature` assignTask et al.), each a permanent false `LensEffectMismatch`.

Named consumers of the drain: the Weaver heartbeat / **Lamplighter** (a standing false warning is
exactly the alarm-fatigue noise that masks real ones), and Loupe's F18 planner-diagnostics view,
which displays these windows and currently shows fossils as live mismatches.

## 2. Grounding — the mechanism and why the fossils stand

All in `internal/weaver` (`state.go`, `reconciler.go`, `health.go`, `control.go`):

- **Writers:** `recordEffectDispatch` — lane-1 episode fire + *genuine* sweep reclaims only
  (post-fix, gated by `collapseOnlyReclaim`); `recordEffectClose` — lane-1 `clearClosedMarks` + the
  sweep's *won* `gapClosed` delete (incl. the row-gone leg: an entity teardown credits a close).
- **Readers:** `flagEffectMismatches` (heartbeat-cadence scan → health issue, self-clearing once a
  scan no longer lists the window) and `planner_shadow.go`'s `effectCloseRate` — which requires
  `mode:"shadow"` **and** declared `candidates`, and **no installed target declares either** (grep
  of `packages/*`), so today the ring feeds no live decision. A missing key reads as "no data"
  (`ok=false`), never as a zero close-rate — deletion is safe by construction for every reader.
- **Existing GC:** `sweepEffect` deletes a window only when its *target is uninstalled* or its *gap
  column left the playbook* — "a live (target, gapColumn) pair is left untouched regardless of its
  window contents — level reconcile here never resets confidence." The no-reset posture is correct
  for honest windows; it is also exactly why fossils of a live pair have no exit path.
- **Why the fossil stands, precisely:** the alert condition is `len==20 && closes==0`. One genuine
  close for the pair *would* clear the alert (flip one slot), and full eviction takes 20 further
  bookings. But for a pair like `missing_signature` on a demo-seeded stack, no close ever arrives
  (nobody signs; the entities aren't torn down) and post-fix bookings are rare (one per genuine new
  episode) — so the window sits frozen at 20/0 indefinitely. "Self-heal" exists in theory and never
  fires in practice.
- **What is NOT polluted:** `…__count` dispatch counts were deliberately left ungated by `5b58f66`
  (they bound reclaim *effort*, which a repeat reclaim does spend) and reset on gap close — they are
  live per-episode state, not fossils. Marks and `__control` are untouched state. The drain must
  leave all three alone.

## 3. The shape — a fourth control verb, mirroring the existing three

Precedent mirrored: the FR30 control plane (`internal/weaver/control/service.go`,
`lattice.ctrl.weaver.<targetId>.<op>` micro-service endpoints; per-verb capability check via
`controlauth`; CLI group `cmd/lattice/weaver`).

- **Engine method** `Engine.ResetConfidence(ctx, targetID)`: requires the target registered (mirrors
  `Disable`/`Enable` — orphaned windows are already `sweepEffect`'s job); lists
  `weaver-state` keys, deletes every key containing `effectKeyMarker` under the `<targetID>.` prefix
  with **revision-conditioned deletes** (the read-this-pass revision, mirroring `deleteEffect`'s
  skip-on-conflict posture — a booking racing the reset survives as honest new history); returns the
  deleted count, one `Info` log line (mirrors `Revoke`'s style). Deletes **only** `__effect` keys:
  never marks, `__count`, `__control`, never the lane-1 durable.
- **Control endpoint** `resetConfidence` beside `list/disable/enable/revoke` (same 5-token subject
  shape, same actor-header verification), authorized as operationType **`ctrl.weaver.resetConfidence`**.
- **CLI** `lattice weaver reset-confidence <targetId>` in `cmd/lattice/weaver` (same `--actor` /
  `--actor-token` plumbing; prints the deleted count).
- **Grants (package work):** add the `ctrl.weaver.resetConfidence` operationType to
  `packages/control-authz` and `packages/console-operator` manifests **with version bumps** (a
  same-version edit no-ops on install). **Not** granted to `packages/demo-operator` — F20.3's
  inspect-only boundary keeps every write verb out of the public demo role.
- **Convergence of state:** after a reset, `flagEffectMismatches`' next scan lists nothing for the
  target → the standing issues + the `effectMismatches` metric clear via its existing reconciliation
  loop; windows rebuild honestly from the next genuine episode.

Read/write-path invariants: not applicable in the P2/P5 sense — `weaver-state` is engine-private
operational state (P1), and the control plane is its one sanctioned operator seam; no Core KV, no
lens, no op is involved.

## 4. Execution — the actual drain (the fire's closing step, not a separate migration)

The verb is the mechanism; the drain is one command per affected stack. A stack is affected iff its
`weaver-state` bucket survived from before `5b58f66` with Weaver running against showcase data — the
local dev stack (the filed observation), and the demo VPS if its bucket predates the fix. Steps:
read the Weaver heartbeat (`lattice health` / Loupe) and note the `LensEffectMismatch` issues →
`lattice weaver reset-confidence <targetId>` for each listed target (today: `leaseApplicationComplete`)
→ confirm the next heartbeat shows `effectMismatches: 0` and the issues gone. No startup migration,
no version-keyed sweep — state surgery stays operator-initiated, engine-executed, auditable.

## 5. Alternatives considered

### 5.1 Durable window age-out (the board's named alternative) — REJECTED

Four independent reasons, any one sufficient:

1. **Dead scaffolding:** the pollution *mechanism* is fixed; an age-out's ongoing consumer is a bug
   that no longer exists. A genuine long-open episode books exactly one pending slot post-fix — one
   slot cannot trip a 20-slot alert — so nothing refills windows the way the bug did.
2. **Determinism:** the ratified §3.2 ring is "event-keyed, never clock-sampled; deterministic", and
   §3.1 makes planner determinism load-bearing (re-deciding must reproduce the decision). Aging
   slots out by wall-clock makes `effectCloseRate` — a Fire-5 ranking input — time-dependent.
3. **Signal weakening:** a *real* lens/effect mismatch looks identical at rest (full-pending window,
   marks TTL'd out). An age-out would quietly clear true positives — the worst direction for the one
   loud signal §3.4 built.
4. **No discriminator:** pollution is indistinguishable from honest failed-attempt history at rest
   (a 5-attempt-then-success episode legitimately leaves 4 permanent pendings). Any automatic drain
   is either lossy or wrong; only an operator who knows *why* the corpus is corrupt can decide.

### 5.2 `revoke` + `enable` (the existing prefix-delete) — REJECTED as the drain

`Revoke` does wipe `__effect` keys (its `deleteByTargetPrefix` covers them), but it also deletes
every live mark and the lane-1 durable. The re-added consumer is `DeliverLastPerSubject`
(`engine.go`), so every currently-violating row replays and fires a **fresh episode with a fresh
claimId** — and §10.3's claimId-verbatim collapse means a *fresh* claimId does **not** collapse:
each open `missing_signature` gap would mint a **second live SignLease userTask** beside the first.
Operator-visible damage to drain advisory data. (Also fixed in passing: `reconciler.go`'s comment
claiming the prefix-delete runs "on Disable/Enable/Revoke" — only `Revoke` deletes; a one-line
comment truth-up rides the fire.)

### 5.3 Raw KV surgery (a `scripts/` one-shot or `nats` CLI against `weaver-state`) — REJECTED

Bypasses the engine's ownership of its bucket and the `controlauth` capability gate, and needs
Weaver's own NKey credential to even write (`natsperm` matrix: weaver owns `weaver-state`). It would
work once — and become the precedent every future agent copies for "just delete the engine's keys."
The control plane exists precisely so operator interventions on `weaver-state` are engine-executed
and authorized; extending it is the same size of change and leaves the right precedent.

### 5.4 Do nothing (wait for self-heal) — REJECTED

§2: the alert clears only on a close credit that, for these pairs on these stacks, never comes. A
standing false warning on the health plane is not a steady state to accept — it trains the
Lamplighter (and Andrew) to ignore `LensEffectMismatch`.

## 6. Contract surface

**None changed.** Contract #10 §10.3 documents the `__effect` key shape and §10.8 the planner
extension — both built-to; window *retention* is engine lifecycle (the sweep already deletes windows
on orphan legs), and this verb adds one more engine-owned deletion path. The control plane's verb
table lives in `docs/components/weaver.md` (FR30), which gains the `resetConfidence` row + a short
"confidence reset" paragraph. The ratified planner-mandate §3.2/§3.4 text stands byte-identical.

## 7. Reconciliation with the existing mental model

- *Didn't `5b58f66` already fix this?* It fixed the **flow** (new bookkeeping is honest); this
  drains the **stock** (windows the old code filled). Prevention landed; this is the detect-recover
  half.
- *Doesn't the sweep GC `__effect` keys?* Only orphans (target uninstalled / column dropped). A live
  pair's window is deliberately never content-reset by the sweep — that posture is correct and kept;
  the reset is operator-initiated precisely because no automatic rule can tell fossil from signal
  (§5.1 #4).
- *Does this duplicate an existing pattern?* It **completes** one: `disable` (pause, delete nothing)
  · `resetConfidence` (delete advisory confidence only) · `revoke` (delete everything + disable) —
  three points on one operator-severity ladder, same subject shape, same authz, same CLI group.
- *New state?* None — the verb only deletes; no new keys, buckets, events, or schemas.

## 8. Residual boundary (named so a returning alert isn't read as a regression)

On a stack where ≥20 *distinct* episodes of one (target, gap, action) accumulate and genuinely never
close — e.g. a demo stack seeding incomplete applications nobody ever signs — the window will
honestly refill to 20/0 and `LensEffectMismatch` will honestly re-fire. That is the ratified signal
telling the truth ("closes never arrive" — they don't); the remedy is demo-side (complete, expire,
or tear down the seeded tasks), not a platform-side weakening of the alert. Distinct from the fossil
case: it needs 20 real episodes, not one stuck task × 20 sweep passes.

## 9. Test strategy

- **Engine unit (`internal/weaver`):** `ResetConfidence` deletes all and only the target's
  `__effect` keys — marks, `__count`, `__control`, and a *second* target's windows survive; returns
  the count; unregistered target errors (mirroring `Disable`); a revision bumped between list and
  delete is skipped, not clobbered (fake-conn conflict injection, the `deleteEffect` test pattern).
- **Health convergence:** seed a full-pending window → `flagEffectMismatches` raises → reset →
  next scan clears the issue + zeroes the metric (extends the existing
  `health_internal_test.go` mismatch vectors).
- **Control + CLI:** endpoint subject parse/authz-deny/happy-path beside the existing
  `service_test.go` vectors; CLI test in `cmd/lattice/weaver/weaver_test.go`'s harness.
- **Package gates:** `make verify-package-control-authz` + `make verify-package-console-operator`
  (the out-of-band verify gates), plus the version-bump lint (`scripts/lint-package-version.go`)
  which the bumps in §3 satisfy.

## 10. Decomposition for the Steward

**One fire** (XS–S as boarded): engine method + control endpoint + CLI subcommand + the two manifest
grants with version bumps + tests (§9) + the `weaver.md` control-plane row + the §5.2 comment
truth-up — then the drain itself (§4) on the live stack with before/after heartbeat evidence in the
commit message. If the demo VPS bucket predates the fix, the same one command there is an ops
follow-through, not a second fire.

## 11. Adversarial pass (run this fire — no deferred gate)

Scope is XS–S and single-seam, so a full party-mode is disproportionate; an inline adversarial pass
was run against the four §5 alternatives and the failure modes: reset-vs-booking race (CAS-skip,
honest slot survives), reset-vs-close race (close no-ops on missing key — one advisory outcome lost,
bounded), partial failure (rerun idempotent; count reported), abuse surface (capability-gated,
deletes advisory data only, demo role excluded), and future Fire-5 interaction (a reset equalizes
candidate ranking until history rebuilds — operator-gated, acceptable, and logged). No open
questions remain; no pre-build gate is left for the Steward.
