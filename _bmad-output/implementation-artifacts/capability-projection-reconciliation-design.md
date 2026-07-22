# Auth-plane projection reconciliation — capability first-projection loss

**Status: ✅ RATIFIED (Andrew, 2026-07-21)** · Designer fire 2026-07-22 · board row:
[lattice.md → Component maintenance → Refractor capability first-projection loss](../planning-artifacts/backlog/lattice.md)

> **What ships.** The auth plane (Capability KV) gains a reconciliation loop: a per-actor
> `reproject` control verb (targeted heal, mirrors `personal.hydrate`) plus a bounded in-Refractor
> convergence sweep that detects and repairs graph↔Capability-KV divergence and surfaces it on
> Health KV — closing the class where a missed projection is permanent, invisible, silent grant
> loss. The defect is availability-gap-shaped, not a fan-out race (the board's original suspect is
> falsified, §2.1); live demo-box evidence in §2.3.
>
> **Ratification record.**
> - **§6.2 contract edit ratified and committed with this banner.** Contract #6 sanctions exactly
>   two non-CDC `projectionSeq` token classes: **reconciliation** = the pipeline's last-applied
>   stream sequence captured before re-evaluation (subordinate — loses every race against real CDC
>   under the `≤`-rejects guard, §3.4); **shred** = `MaxInt64` (terminal, always wins). Any third
>   class requires a contract change. Affected consumers: Refractor's guarded NATS-KV adapter only
>   (the Processor reads docs, never watermarks; Postgres targets are §6.2-exempt).
> - **Fires collapsed 3→2** (fewer-larger-fires): **Fire 1** = verb + sweep + health signal, one
>   `internal/refractor` fire with the verb built first (§3.1 = Fire 1a, §3.2 = Fire 1b);
>   **Fire 2** = seed readiness wait + demo runbook (§3.3, `scripts/` + `deploy/demo/` only).
> - The F20 framing was corrected pre-ratification: F20 is not blocked — it shipped (`0690381e`)
>   past this bug only via the lucky ~1h full rescan; the class is unhealed and re-opens on every
>   demo-world reset.
> - No Gateway / D1 / Vault / multi-cell / HA-NATS fork is touched.

---

## 1. Problem + intent

**Backlog row (★★★, M–L):** a fresh identity's first AssignRole can mint no `cap.roles.<actor>`
doc — absent 30+ minutes, surviving a Refractor restart, with nothing re-driving it — while a
later holdsRole on a doc-holding actor folds in seconds. Silent grant loss at the auth plane;
the F20 demo-operator enablement hit it live (73 step-3 denials, §2.3). F20 has since shipped
(`0690381e`) only because the ~1-hour full rescan happened to replay the lost events — the class
is unhealed, and every demo-world reset re-opens the window.

Intent traces to the architecture's own bar: *"Capability KV is a lens projection — projection
correctness = auth correctness"* (lattice-architecture.md; Contract #6). The write path defends
ordering (the §6.2 monotonic guard) and fabrication (overwrite-by-reprojection), but has **no
defense against omission** — a projection that never happened. On the security plane a missed
grant is deny-direction (bad: a live actor is dark); a missed *revocation* is an **over-grant**
(worse). Both are the same class: divergence between the graph and its auth projection with no
reconciler. This design closes the class, not just the observed instance.

## 2. The grounded mechanism (what actually fails)

### 2.1 What was suspected — and falsified

The board row's suspect was an ordering race between the actor-aggregate fan-out and the
adjacency build. Code walk (`internal/refractor/pipeline/evaluate.go`,
`actor_enumerator.go`, `adjacency/builder.go`):

- `evaluateLinkFanOut` **idempotently applies the link to adjacency KV itself** (both
  directional entries, CAS-with-retry `adjacency.Build`) *before* enumerating, exactly so the
  reprojection cypher can never race ahead of the edge that triggered it. The dedicated
  `consumer.Bootstrapper`'s later build of the same EdgeID is a no-op.
- Enumeration from the link's identity endpoint takes the actor fast path (singleton — no
  adjacency read at all); the role endpoint's `grantedBy` inbound edges are package-install-time
  adjacency, long since built.
- `adjacency.Build` is CAS-with-retry (no lost update); the guarded NATS-KV write is a bounded
  CAS loop (`adapter/natskv.go`). The e2e suite pins the path
  (`refractor_capability_linkfanout_e2e_test.go`, `..._aspectfanout_...`,
  `refractor_package_actoraggregate_proof_e2e_test.go`).

The projection *logic* is sound. The loss lives a layer up.

### 2.2 The real defect — availability gaps are permanent

The capability pipeline is a durable JetStream consumer over the `KV_core-kv` backing stream.
Three properties compose into the observed loss:

1. **Restart never replays.** `Pipeline.Run` deliberately stops the pump *without* deleting the
   durable — "its persisted position is the point of durability". Correct for steady state, but it
   means a restart can never recover an event that was acked (or whose delivery window was lost)
   without its write landing. "Survives Refractor restart" is by design.
2. **A recreated source stream orphans the pipeline.** The demo reset (`make down` — the dev
   compose keeps no volumes) destroys the stream *and its durables*. A Refractor that (re)starts
   against the fresh stream builds new durables with `DeliverLastPerSubject` — a **full rescan of
   every key for every one of the 67 lenses**. On the demo box that drain took **~40 minutes**
   (43k+ processed events, ~19 events/s on 2 vCPU), during which the auth plane serves nothing
   for new actors. AckWait overruns during the drain double-delivered link events (observed:
   identical `holdsRole` events processed twice, 2m25s apart — harmless under the guard, but it
   documents that drains overrun the ack window).
3. **Nothing audits the result.** The auth-plane health surface (`CapabilityLensPaused` /
   `CapabilityLensLagging`) watches consumer *status and lag* — a consumer that is healthy and
   caught-up but *missed* events reads as fine. Zero lag ≠ converged truth. The only repair
   instrument is a full per-lens `rebuild` (truncate + rescan): operator-driven, bucket-wide, and
   nothing tells the operator it is needed.

So the discriminator in the repro ("doc-holding actor folds in seconds, fresh actor never") is
simply *when the events landed relative to a pipeline-availability window*: events consumed by a
live, caught-up pipeline project in seconds; events falling into a gap (or behind a
multi-ten-minute drain) are invisible until an unrelated full rescan happens to replay them — or
forever.

### 2.3 Live evidence (demo box, 2026-07-22 00:21–01:42 UTC)

Timeline reconstructed read-only from `refractor.log`, `processor.log`, and the systemd journal:

- `00:21` nightly reset (`make down` — world + stream + durables wiped) → reboot `00:23` →
  `demo-up` full stack; bootstrap logs `readiness gate SKIPPED — seed pass only; cap.* projections
  NOT verified`.
- `00:40–00:53` the F20 enablement session assigns demoOperator/demoNudge roles to fresh
  identities, then submits `ctrl.weaver.read` as them on a 5s retry loop: **73 step-3 denials**
  (`no Capability KV entry … keys=[cap.roles.identity.…]`) across two actors before giving up.
  The key is *physically absent* — yet a healthy pipeline writes a `cap.roles` doc (empty-grants
  body; see the parallel inert-`emptyBehavior` row — the delete path never fires without a
  `realnessFilter`, so identity-create always produces a doc) the moment it consumes the
  identity-create event. Total absence therefore proves the pipeline consumed *nothing* for these
  actors in the window.
- `01:04:30` Refractor process replaced → fresh durables → full `DeliverLastPerSubject` rescan;
  17 lenses infra-pause/probe/recover during startup.
- `01:38–01:42` the seed's `holdsRole` links are finally processed by `capabilityRoles`
  (ruleId `7VGR…`) — **~an hour after assignment**, twice each (AckWait redelivery mid-drain).
  Yesterday's original repro (five identities, 30+ min, across a restart) is the same signature
  without the lucky rescan.

## 3. The shape

Three principles drive it: **Refractor owns projection correctness** (the reconciler lives inside
the component, not in Weaver or the Processor); **reuse the per-actor machinery that exists**
(`reprojectActors` / the `personal.hydrate` precedent — no new evaluation path); **the §6.2 guard
stays the single write-ordering authority** (reconciliation writes flow through the same guarded
adapter and must lose every race against real CDC writes).

### 3.1 Fire 1a — per-actor `reproject` control verb

A control-plane op `lattice.ctrl.refractor.<lensId>.reproject`, request `{actorKey}`, on any
**actor-aggregate pipeline** (`envelopeFn != nil`; structurally refused otherwise — plain lenses
already have filter-retraction/diff-retraction, and the Personal Lens keeps its own
`personal.hydrate`). Mirrors the `hydrate` wiring (`control.Service` → pipeline handle,
`cmd/refractor/main.go`), including the FR30 capability gating on ctrl verbs.

Semantics, per invocation:

1. Capture `seq := Pipeline.Progress().LastAppliedSeq` **before** evaluation (the same
   capture-before-reproject discipline `Hydrate` uses for its revision snapshot).
2. Re-execute via the existing `reprojectActors(ctx, []string{actorKey})` — every case falls out
   of machinery that already exists: roles present → real row (upsert); no real rows → whatever
   the envelope's empty semantics are (today an empty-grants doc — the separate ★★
   inert-`emptyBehavior` row fixes the delete path so this becomes the §6.8 tombstone; the
   reconciler deliberately *inherits* the envelope's semantics rather than defining its own, so
   the two fixes compose with zero coupling); actor vertex missing/tombstoned → the
   missing-actor `Delete`.
3. **Skip-if-identical:** read the stored row via `adapter.RowReader.GetRow` (exists on
   `NatsKVAdapter`) and drop the write when the computed body equals the stored one (modulo
   `projectionSeq`) — a converged actor costs zero KV writes, so the verb (and Fire 1b's sweep) is
   churn-free at steady state.
4. Divergent → write through the normal guarded path stamped with `seq` (§3.4).

CLI: `lattice lens reproject <lens> --actor <vertexKey>`. This alone turns the observed incident
from "wedge until someone rebuilds the world" into a one-command targeted heal.

### 3.2 Fire 1b — auth-plane convergence sweep + health signal

A periodic, rate-limited self-audit **per auth-plane actor-aggregate lens** (`IsAuthPlane` ∧
`envelopeFn != nil` — derived from the compiled plan, never a canonical-name list, mirroring
`projection.RequiresGuard`):

- **Coverage prefilter (cheap, both directions):** list the lens's anchor-type vertices from Core
  KV (`vtx.<anchorType>.` prefix — Refractor is the read side; lens evaluation already reads Core
  KV wholesale) and the target's live keys (`adapter.KeyLister.ListKeys`, exists). An anchor with
  no target key, or a target key with no live anchor, is *definite* divergence → heal immediately
  via Fire 1a's path.
- **Round-robin deep verify (bounded):** a persistent cursor walks all anchors, re-executing at
  most `sweepBatch` actors per `sweepInterval` tick (defaults: 25 actors / 60s — a 10k-actor cell
  fully re-verifies in ~7h; both deployment-overridable like the cap-lag threshold). The deep pass
  is what catches the over-grant direction (doc present, graph no longer grants it) that the
  prefilter cannot see. Skip-if-identical makes a converged pass write nothing.
- **Health signal:** every healed divergence increments a per-lens counter surfaced on the
  heartbeat (`metrics.capabilityLens.<name>.reconciled`) and raises a Contract #5 §5.5 issue
  `CapabilityCoverageDivergence` (`severity: warning`; escalating to `error` when divergence
  recurs across consecutive sweeps — the debounce/clear-band pattern the cap-lag alert already
  uses). Silent loss becomes a Lamplighter-classifiable signal even if the heal itself fails.
- **Suppression:** the sweep pauses while `rebuildInFlight` (a rebuild is a superset), while the
  pipeline is paused (operator intent wins), and never runs for personal (`nats_subject`),
  plain, convergence, or operation-aggregate lenses — structurally, by the two predicates above.

The sweep is Refractor-internal on purpose. A Weaver convergence lens was considered and
rejected (§6): remediation here is a *projection write*, which only Refractor may perform —
routing it through Weaver would either have Weaver nudge Refractor's control plane (a new
cross-engine reflex) or violate P2/engine-boundary rules outright.

### 3.3 Fire 2 — seed/deploy hardening (scripts + runbook only)

The demo box compounded the platform gap with two deploy-side defects, fixed where they live:

- **Seeding must wait for auth-plane convergence, not spin on denials.** Bootstrap already owns a
  cap.* readiness gate (`waiting for readiness gate`, skipped in the seed pass). Extend the same
  posture to the showcase seed: after role assignment, poll the *projection* (`cap.roles.<actor>`
  present via the sanctioned query surface) with a real deadline before submitting as that actor —
  replacing the blind 5s×N submit-retry that burned 73 denials.
- **`demo-reset`/`demo-up` runbook note:** after a world wipe, engine durables are rebuilt from
  scratch and the full rescan on this box takes tens of minutes; the demo is not "up" when the
  processes are — it is up when the readiness gate passes. (The AckWait-overrun double-delivery
  during drains is benign under the guard; documented, not "fixed".)

### 3.4 The reconciliation ordering token (the contract surface)

A reconciliation write has no triggering CDC message, so §6.2's definition doesn't cover it. The
token is the pipeline's **last-applied stream sequence, captured before re-evaluation**:

- **It always loses to later truth.** Any CDC event not yet reflected in the sweep's read carries
  a stream seq strictly greater than the captured `lastAppliedSeq`, so its reprojection overwrites
  the reconciliation write under the existing `≤`-rejects rule. Ties (`incoming == stored`) drop
  the reconciliation write — correct, the stored doc already reflects that event.
- **It cannot resurrect.** A revocation visible in Core KV at evaluation time is *in the
  re-executed result* (the cypher reads live KV, which is always ≥ the consumer's position — the
  sweep can only be fresher than the pipeline, never staler); a revocation not yet in KV will
  arrive as a higher-seq event and win.
- **It is not `MaxInt64`.** The shred-nullifier's always-wins stamp
  (`keyshredded/manager.go`) is a *terminal* authority; a reconciliation write is a *subordinate*
  one. Stamping reconciliation at `MaxInt64` would permanently freeze the key against all future
  CDC — the exact inversion of intent. The contract edit says so explicitly so no future fire
  borrows the wrong precedent.
- Guarded-create of an absent key with this token is safe: if the key is absent, the doc is
  either genuinely missing (the bug — create heals it) or hard-deleted-never-existed; a
  concurrent real event's CAS create simply wins the loop (`guardCASMaxAttempts` path, already
  tested).

**Staged edit (UNCOMMITTED, the proposal):** `docs/contracts/06-capability-kv.md` §6.2 amendment
gains one bullet defining the reconciliation write class, its token, its always-loses guarantee,
and the `MaxInt64` prohibition. No other contract text changes; §6.13/§6.14 are built-to.

## 4. Reconciliation with the existing mental model

*Didn't we already handle this?* Each near-miss named, and why the gap remained:

- **The §6.2 monotonic guard** orders writes that *happen*; it cannot conjure a write that never
  did. It is the reason reconciliation is cheap and safe — but it is ordering, not liveness.
- **Overwrite-by-reprojection** (the fabricated-write defense) heals *wrong* rows on the next
  event; a *missing* row has no next event — a fresh actor's first link was the one event.
- **The Capability-Lens health backstop** (paused/lagging alerts) watches the consumer, not the
  truth; the observed incident had a healthy-looking pipeline (or none logging at all).
- **`Rebuild`** is the whole-bucket hammer (forced truncate on guarded buckets, full rescan —
  ~40 min on the demo box) and operator-driven with no signal telling the operator to swing it.
- **`personal.hydrate`** is exactly this verb for the Personal Lens — per-actor, on-demand,
  capture-seq-then-reproject. Fire 1a is its auth-plane mirror; the precedent transfers because
  both are envelope pipelines with per-actor evaluation.
- **Bootstrap's readiness gate** verifies primordial cap.* projections at kernel bring-up; it
  never extended to package lenses or seeded actors — Fire 2 extends the posture, not new
  machinery.
- **The parallel ★★ inert-`emptyBehavior` row** (same-day finding: the envelope's delete path is
  dead code without a `realnessFilter`, so a last-role revocation leaves a stale doc) is a
  *sibling defect in the write that happens*, not in the write that never happens — fixed
  separately (S); the reconciler inherits whichever empty semantics the envelope has, so the two
  land independently and compose.
- **New state?** One cursor + counters per auth-plane lens (in-process; cursor persisted in the
  lens's existing Health-KV entry so a restart resumes rather than restarts the walk). No new
  bucket, no new stream.

## 5. Migration, compatibility, test strategy

**Migration:** none — additive verb + additive sweep + one additive §6.2 bullet. Existing docs,
watermarks, and consumers are untouched; the sweep's first pass over a healthy bucket writes
nothing (skip-if-identical).

**Tests (colocated, per house rule):**

- *Fire 1a:* unit — verb refuses non-envelope pipelines; skip-if-identical writes nothing;
  divergent actor heals (missing→create, stale→overwrite, role-less→the envelope's empty
  semantics, missing-actor→delete); token = captured `lastAppliedSeq`. E2E — **reproduce the incident**: seed identity +
  AssignRole with the lens consumer detached, confirm step-3 `NoCapabilityEntry`, invoke
  `reproject`, confirm the doc and an authorized submit. Race vector — reproject concurrent with
  a revocation event: final state is the revocation (guard order), `-race`.
- *Fire 1b:* unit — prefilter both directions; cursor bounds/resume; rebuild/pause suppression;
  debounced issue raise/clear. E2E — kill a doc under a live pipeline (fabricated divergence),
  sweep detects + heals + raises `CapabilityCoverageDivergence`; converged world sweeps clean
  (zero writes, pinned).
- *Fire 2:* deploy scripts exercised under the real systemd unit per
  the demo-box runbook (served-outcome acceptance), not just locally.

**Verification gates:** the standard set (`go build ./...`, `make vet`, `golangci-lint`,
`make verify-kernel`, full `go test ./...`) — the guard/adapter behavior is already pinned by
`internal/refractor/adapter` tests; new vectors land beside them.

## 6. Alternatives considered

- **Weaver convergence lens over a coverage projection.** Weaver is *the* gap-closing engine, so
  this was the reflex first shape. Rejected on layering: the remediation is a projection write
  only Refractor may perform; Weaver's remediation vocabulary is ops via the Processor (P2), and
  a `ctrl.refractor.*` dispatch from Weaver would make the auth plane's integrity depend on a
  second engine's health. A *variant* worth keeping: once Fire 1b's health signal exists, a Weaver
  target could watch it and nudge `reproject` — deferred until a consumer needs cross-engine
  escalation; the signal is designed so that adding it later requires no rework (the verb already
  exists as the actuation surface).
- **Processor denial-triggered auto-reproject** (step-3 `NoCapabilityEntry` → ctrl nudge).
  Attractive demand-driven heal; rejected for now: it couples the hot auth path to Refractor's
  control plane, amplifies denial storms into reprojection storms, and heals only the
  deny-direction (an over-granted actor never generates a denial). The verb leaves this open as a
  cheap future consumer.
- **DLQ replay as the re-drive.** Covers only events that *reached* a terminal disposition; the
  observed loss (never-delivered events) leaves no DLQ trace. Complementary, not sufficient.
- **Runbook-only** (document `rebuild` as the fix). Rejected: silent divergence on the security
  plane with a human-latency detector is exactly what the row was filed against.
- **Always-rescan on start** (delete durables every boot). Closes only the restart case, costs a
  ~40-minute auth-plane brownout per restart on constrained hosts, and still misses steady-state
  gaps. The targeted sweep dominates it.

## 7. Risks

- **Sweep read amplification.** Bounded by design: prefilter is two key listings; deep verify is
  `sweepBatch` cypher executions per tick with a cursor. At the multi-cell extreme the sweep is
  per-cell by construction (a Refractor instance sweeps only its own cell's bucket) — no global
  scan is ever introduced.
- **Watermark misuse by future fires.** The §6.2 edit names the two sanctioned non-CDC tokens
  (reconciliation = last-applied-seq, shred = `MaxInt64`) and prohibits new ones without a
  contract change — the precedent-borrowing failure mode is fenced in the contract itself.
- **Verb misuse.** `reproject` is capability-gated like every ctrl verb (FR30) and granted to
  `console-operator`/`control-authz` tiers only — never `demoOperator` (inspect-only stays
  inspect-only).
- **A healed symptom masking a delivery bug.** The `reconciled` counter + issue exists precisely
  so healing is *loud*: a nonzero rate is itself the "go find the delivery gap" signal, preserving
  the flake-may-be-a-real-bug discipline.

## 8. Adversarial pass (discharged 2026-07-22, this fire)

Run against the finished shape; findings folded in above: (1) sweep-vs-revocation race — resolved
by the always-loses token + live-KV-read argument (§3.4), added the `-race` vector; (2) sweep
churn on converged buckets — added skip-if-identical as a hard requirement, zero-write pinned by
test; (3) `MaxInt64` precedent bleed — added the explicit contract prohibition; (4) verb on
plain/personal pipelines — made the refusal structural (predicate, not docs); (5) rebuild
interleave — added `rebuildInFlight` suppression + steady-state-equivalence argument; (6) cursor
loss on restart — persisted in the existing Health-KV entry rather than new state; (7) demo seed
retry loop — moved from "platform should be faster" to Fire 2's readiness-gate posture (the
platform being temporarily behind must be *observable and waitable*, not raced).

## 9. Fire decomposition for the Steward

| Fire | Scope | Size | Ships green when |
|---|---|---|---|
| 1 | `reproject` ctrl verb + CLI (1a, built first) + convergence sweep + `CapabilityCoverageDivergence` health signal (1b) + incident-repro e2e | M–L | verb heals the reproduced loss end-to-end; fabricated divergence detected+healed+alerted; converged sweep writes nothing; all gates green |
| 2 | seed readiness wait + demo runbook | S | demo-up seed completes with zero step-3 denials on a fresh world |

Fire 1 is platform code (Lattice lane, `internal/refractor` + `cmd/refractor`;
`internal/substrate` untouched); Fire 2 is `scripts/` + `deploy/demo/` only. Fire 1's verb half
alone already turns the demo box's reset-window wedge into a one-command targeted heal; the sweep
half removes the class.
