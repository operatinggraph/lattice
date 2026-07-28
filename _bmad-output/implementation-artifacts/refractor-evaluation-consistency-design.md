# Evaluation consistency — the torn-row verdict + auth-plane footprint validation (design)

**Status: ✅ Andrew-ratified (2026-07-27)** — scope as recommended (Fire 1 structural on
`actorAggregate ∧ IsAuthPlane`; Fire 2 returns to design; no snapshots). Ready for the Lattice
Steward. Author: Winston (Designer fire, 2026-07-27) · Lattice lane
(Stream 2, [Refractor]). Backlog row: *"Does a lens evaluation need a point-in-time snapshot?"*
(`lattice.md` Component maintenance, ★★ M, filed by `e8d78278`'s own commit message).
Adversarial pass **✅ RUN this fire** (independent read-only reviewer) — 3 blockers + 6 must-fixes
found against the first draft's mechanism; **all folded** (§12 lists them; §4 is the corrected shape).

> **For Andrew — two lines.** The row demanded proof before mechanism; the proof came back **split**:
> a torn row is harmless everywhere consumers re-validate through the Processor, but on the **auth
> plane the projection IS the authority and three step-3 shapes conjoin columns from different KV
> keys** (`matchEphemeralGrant`, `matchServiceAccess`, and the conjunctive RLS grant rows) — a torn
> row there is a **combination-grant no real instant ever authorized**, categorically outside the
> accepted bounded-staleness posture. Recommendation: **no snapshots** (substrate-impossible at
> `History: 1`, and wrong anyway); instead **footprint validation** on auth-plane evaluations —
> capture the read surface (vertices, aspects, **and adjacency entries**, absences included) with
> the revisions observed; on post-evaluation drift **re-execute, then requeue as a typed transient
> failure** — a torn document never lands, and the failure semantics are the loud, existing ones.
>
> **Fork (resolved, my recommendation):** scope. Fire 1 = **actor-aggregate auth-plane lenses,
> structural** (covers every step-3 `Authorized: true` source — the write-auth plane). The
> **unanchored grant-table scans return to design** (Fire 2): per-row read attribution is not
> M-sized (the adversarial pass demonstrated the stated chokepoint is ill-defined), so the
> read-path RLS tear window stays open **interim** — transient, ms-to-seconds, healed by the same
> events, and stated here rather than papered over. Option B (platform-wide opt-in flag) stays
> rejected: no business consumer needs it — dead scaffolding.
>
> **No frozen-contract change.** Two rows filed with this design (§10): the Weaver **value-copy**
> class (a staleness defect snapshots cannot fix) and the **`reportsTo` enumeration gap** (a live,
> pre-existing fan-out miss the pass surfaced).

---

## 1. Problem + demand

The evaluation read memo (`e8d78278`, 2026-07-26) made reads repeatable **per key** inside one
cypher execution, closing the live split-row defect (one anchor → two rows → output-key collision
→ actor dropped, `leaseApplicationComplete`). Its own commit message files the residual:

> The memo is per-key, not a snapshot: two DIFFERENT keys are still first-read at their own
> instants, so a row can still blend two moments. That is a single torn row, which no guard
> rejects and the next CDC event re-derives; whether it is reachable and harmful enough to want
> revision-pinned reads is filed for the Designer to prove before designing.

Sharpened: the Processor commits multi-key batches **atomically** (Contract #3), so the write side
guarantees no real moment held `old-A + new-B` — and a lens evaluation, reading lazily per key
during the walk, can still project exactly that pair. A torn row is not stale truth; it is
**fabricated truth**.

The board row set the bar: *prove a torn row is reachable AND that some consumer acts on it*
before designing revision-pinned reads.

## 2. Grounding — the mechanics that bound the problem

All verified in code this fire (NATS pin 2.14 per `docs/vendors.md`).

- **One evaluation = one `Engine.ExecuteWith` = one memo** (`ruleengine/full/executor.go:58-79`).
  Reads are lazy point-reads at "now": anchored seed → one `fetchNode`; unanchored seed →
  `ListKeys` + per-key fetch (a whole-bucket scan, `executor.go:465-500`); aspect access → live
  point-read (`:1470-1496`). Absences are memoized; read errors are not.
- **Adjacency reads are outside the memo entirely.** Relationship hops call
  `adjacency.Neighbors` (`executor.go:602` → `adjacency/store.go:16-30`), unmemoized — once per
  hop per frontier node — and the interface discards the entry revision it read. Link-derived
  state is therefore not even repeatable-read *within* one evaluation today. This matters
  because **every harmful conjunct in §3 is a link read** (adversarial finding #1).
- **The Core-KV side needs no interface change**: `ex.coreKV.Get` already returns
  `*substrate.KVEntry` carrying `Revision` (`substrate/kv.go:17-24`); `readNode` currently
  discards it. There is **no revision-only/header-only read** in `substrate` — a validation
  re-read is a full `KVGet` (adversarial finding #9; costs in §5).
- **Per-lens processing is strictly serial** (one pump; `substrate/consumer_supervisor_spec.go`).
  The retry queue replays the *captured row*, not a re-evaluation (`pipeline.go:1167-1202`) — on
  guarded targets a stale replay loses the seq guard; and a replayed row has already passed
  validation at emit, so the queue is not a bypass.
- **The healing matrix — a latency bound, not a correctness guarantee.** The tearing commit's own
  CDC events usually re-derive the affected row (actorAggregate fan-out; plain-unanchored
  re-scan; label-gated aspect/link reprojection), bounding the torn window at ms (NFR-P3
  ≤500ms). But the adversarial pass produced a live counterexample: `capabilityEphemeral`
  inherits grants through `(identity)<-[:reportsTo]-(report)<-[:assignedTo]-(task2)`
  (`orchestration-base/lenses.go:292`), and the actor enumerator **stops at the first actor and
  never traverses through one** (`pipeline/actor_enumerator.go:136-151`) — so a `task2` mutation
  enumerates only `report`, never the manager: keys can sit in a manager's read footprint whose
  events **never** re-derive the manager's doc. That is a live, pre-existing freshness/over-grant
  bug independent of tearing (filed, §10) — and it is why §4's mechanism does **not** lean on
  "the healing event is queued": it re-executes and requeues on its own schedule.
- **Revision-pinned reads are substrate-impossible as filed.** Every platform bucket, core-kv
  included, is provisioned at `History: 1` (`internal/bootstrap/primordial.go:107`) — NATS KV
  history is a per-subject retention cap (ADR-8); `GetRevision` cannot reach a discarded
  revision. Pinning "as of the trigger" would re-price the whole core-kv stream's retention for
  a read-side property. Rejected in §7.

## 3. The verdict — reachability and the consumer census

**Reachability: yes, by construction.** No mechanism orders cross-key reads (and link reads are
not even per-key stable, §2); the live split-row defect already exhibited two internally
consistent snapshots of one actor inside one execution. The widest windows are the unanchored
full-graph grant scans (`staffReadGrants` touches every identity/role/building per evaluation).

**Consumer census** (every class verified in code; the adversarial pass added rows 2, 6, and the
cross-document note):

| # | Consumer | Conjoins cross-key columns? | Re-validated downstream? | Verdict |
|---|---|---|---|---|
| 1 | **Processor step-3 `matchEphemeralGrant`** (`step3_auth_capability.go:326-368`) | **Yes** — one entry = `taskKey` ∧ `operationType` ∧ `target`, from 3 keys + 2 **link** traversals (`orchestration-base/lenses.go:280-330`) | **No** — `Authorized: true` returns directly | **HARMFUL — combination-grant.** The role-queue branch (`holdsRole` ∧ `queuedFor`, independent writers) can grant an actor a task no instant ever queued for a role they held |
| 2 | **Processor step-3 `matchServiceAccess`** (`step3_auth_capability.go:372-410`) | **Yes** — `service` ∧ `allowedOperations[].operationType`, assembled across a `containedIn*0..` walk + a `permitsOperation` comprehension **+ two negative link predicates** (`service-location/lenses.go:133-145`) | **No** | **HARMFUL** — and the `NOT (…)` predicates mean *absence is the grant direction* (§4.1's footprint must capture absent reads) |
| 3 | **RLS grant tables** (`staffReadGrants`; clinic provider/patient grants; `cap-read.root`) | **Yes** — `actor_id` from one key, `anchor_id` from another, gated on a third (`holdsRole` ∧ `worksAt` ∧ role name) | **No** — the policy is a bare set-membership lookup (`adapter/rls.go:194-210`); clinic anchors gate **PHI** | **HARMFUL — combination-grant** (a worksAt transfer racing a role revoke ⇒ a grant valid at neither instant). Interim-exposed until Fire 2 (§4.6) |
| 4 | Write-plane `capabilityRoles` triple (`rbac-domain/lenses.go:84-88`) | No — `{operationType, scope, lanes}` all from one `perm.data` key | n/a | Safe — a torn *set* only mixes individually-real entries: ordinary bounded staleness |
| 5 | **Weaver convergence → directOp** (`strategist.go:583-597`) | Yes — and **op params carry copied values** (`DebitAccount{amountCents}`; the DDLs document they cannot cross-check — `loftspace-ledger/ddls.go:112-117`) | Keys/liveness yes; **copied values no**; ledger append-only, gap self-closes | **HARMFUL but NOT a tear defect** — a *stale-consistent* row debits the same wrong amount. Snapshots don't fix it; §3.4 files the real fix |
| 6 | Control-plane capability checker (`internal/controlauth/checker.go:148`, `preflight.go:86`) | Single-key `{operationType, scope}` match over the same docs | n/a | Safe (single-key), listed for census completeness |
| 7 | Edge manifest rows → facet submit | Yes (dispatch block + operationType) | **Yes** — §6.8 + OCC + step-6; "manifest affects visibility, never permission" | Safe |
| 8 | Vertical app read boundaries | Yes (display flag derivation) | Human-mediated; mutations are ops | Accepted — transient display skew |
| 9 | Loom | Reads no lens projections (`loom/guard_eval.go`; params opaque, resolved Processor-side) | n/a | Immune |

*Out-of-scope note (census completeness):* `capabilitykv.ReadAndMerge` unions `cap.<actor>` +
`cap.roles.<actor>` read at two instants, and lane-gating can pair an entry from one doc with
doc-level `Lanes` from the other (`read.go:33-84`, `step3_auth_capability.go:468+`) — a
**cross-document** blend bounded to system actors. Per-evaluation validation cannot address a
cross-key *read*; noted as a distinct (small) residual, not silently absorbed.

**The bar is met, narrowly and precisely:** the consumers harmed *by tearing itself* are the
**three auth-plane conjunctive shapes** (rows 1–3), where the projection is the authority by
design. Everything else is Processor-re-validated, single-key, display-only — or harmed by
staleness in a way tearing does not worsen.

### 3.4 The misfiled harm: value-copying convergence ops (filed as its own row)

The ledger finding is the sharpest *practical* exposure (wrong money, append-only, never
self-heals) but its mechanism is **copying projection values into authoritative params**, and it
fires identically off a stale-consistent row. Fixing it belongs where the platform already
points: the op declares reads, the Processor hydrates, the DDL derives/validates the amount —
mirroring the externalTask params posture. Filed on the board (§10) rather than solved here.

## 4. The shape — footprint validation: verify, re-execute, requeue (auth-plane structural)

**Principle:** an auth-plane evaluation may only land writes derived from an unmoved read
surface. Not a snapshot; a **certificate**: if every key the evaluation read — vertices, aspects,
**and the adjacency entries backing every traversal, absences included** — is at the same
revision after row production as when first read, the result equals a point-in-time evaluation at
the end instant (for anchored evaluations, which perform no `ListKeys`). If the surface moved,
the result is discarded and the evaluation **re-runs against converged state**; a torn document
never reaches an adapter.

Mechanics (corrected per the adversarial pass — the first draft's memo-only footprint missed
every link conjunct, and its drop-on-drift was fail-open):

1. **Footprint capture — the full read surface.**
   - Core KV: `readNode` keeps the `Revision` already present on `*substrate.KVEntry`
     (absent = 0). No interface change.
   - **Adjacency: `Neighbors` gains a revision-bearing return, and traversal reads are memoized
     per evaluation** (`ex.edges`, the link twin of `ex.nodes`) — closing, in the same stroke,
     the fact that link reads today aren't even repeatable within one evaluation. Negative
     patterns (`existsAsPredicate`) footprint the adjacency entries they inspected, present
     *or absent* — which is what closes census row 2's NOT-predicates (an edge created
     mid-evaluation bumps exactly the adjacency key the NOT read as empty).
2. **Validation at the emit seam** — after `ExecuteWith` returns and before results reach the
   envelope/adapter: re-read each footprint entry, compare revisions. One seam covers pump,
   fan-out, sweep repair uniformly (`executeFullForActor` / `reprojectActors`).
3. **On drift: re-execute inline (default 1 retry), then requeue as a typed transient failure.**
   Drift is ms-scale, so one immediate re-execution against post-drift state converges in the
   common case — and its output *includes the narrowing* (a claimed task, a revoked role), so
   the fail-direction is correct by construction. Sustained churn: surface `ErrEvalDrift` as a
   **transient failure on the existing channels** — pump: retry-enqueue with a re-evaluation
   closure (the `RetryEntry.WriteFn` shape, running evaluate+validate+write rather than
   replaying a row) with the existing backoff/DLQ/health semantics; sweep: a repair *failure*
   (`CapabilityRepairFailing` accounting — retried next pass with backoff), never a converged
   pass. **Never an empty result set** — the first draft's silent drop read as "zero rows" to
   four downstream paths (presence-check Delete, diff-retraction mass-Delete, empty keyset
   frame, sweep false-convergence); a typed error reaches none of them.
4. **Scope predicate, Fire 1 — structural:** `actorAggregate ∧ projection.IsAuthPlane` — the
   capability-kv envelope lenses, i.e. **every source step-3 reads** (`cap.`, `cap.roles.`,
   `cap.ephemeral.`, `cap.svc.`). A new auth-plane envelope lens gets validation automatically;
   forgetting is impossible. (The bare `IsAuthPlane` predicate alone would also catch the
   unanchored grant scans and — via drift-on-every-scan — would have mass-revoked
   `actor_read_grants` through diff-retraction; the pass caught this. Fire 1's predicate is the
   conjunction, deliberately.)
5. **Fire 2 — the unanchored grant-table scans — returns to design.** Whole-evaluation
   validation is vacuous there (the footprint is the bucket; it always drifts), and per-row
   read attribution at the read chokepoint is **ill-defined** (seed scans attribute to the root
   scope; the memo hides first-readers; bindings clone and fold through grouping). The plausible
   shape is *post-hoc* per-row footprints (walk each emitted binding's bound `*nodeRef`s + the
   aspect keys resolved under them + the adjacency entries of its chain — ~5–10 keys for
   `staffReadGrants`), validating the **grant direction only** (absence is deny-direction). That
   is a design increment with real executor surface, not an M fire off this doc. **Interim
   exposure stated honestly:** census row 3's tear window stays open — transient, healed by the
   same events (plain-unanchored lenses re-scan on every vertex event; link/aspect events
   reproject), write-ordered by the always-guarded `GrantWriterAdapter`.
6. **Instrumentation ships with Fire 1:** per-lens `evalDriftRetries` / `evalDriftRequeues`
   counters on the existing health entry — both the loud-healing house pattern and the data that
   sizes Fire 2 (how often does the surface actually move mid-evaluation?).

## 5. Why not revision-pinned reads — and the honest cost of validation

- **Substrate:** `History: 1` (§2). Raising it re-prices core-kv retention platform-wide and
  re-opens `DeliverLastPerSubject`/atomic-batch interactions — a storage-plane change to avoid a
  validation loop.
- **Semantics:** pinning to the trigger instant makes projections *older*, not truer; the
  property consumers need is "no fabricated combination on the authority plane," which the
  certificate provides at read-now semantics.
- **Validation cost, stated without optimism** (finding #9): there is no header-only read — each
  validation is a full `KVGet`/adjacency get. Typical per-actor evaluations footprint tens of
  keys; the pathological case is real: `capabilityEphemeral`'s role-queue branch walks every
  task queued for any held role — a 500-task shared queue means ~1.5k Core-KV + ~1k adjacency
  reads per holder per event, and validation doubles it on the hottest path. That cost profile
  is pre-existing evaluation weight (the branch already reads all of it); Fire 1 ships the
  counters and the perf gate (§9) so the doubling is measured, not assumed. If the drift-retry
  rate on hot queues is material, the branch's own shape (a shared-queue walk inside a per-actor
  evaluation) is the thing to revisit — flagged for the Steward's build note, not designed here.

## 6. Contract surface

**No frozen-contract edits.** Contract #6 §6.2 (guard) is built to. §6.3's
`projectedFromRevisions` is **not** a precedent for this mechanism (first-draft error, corrected):
it scrapes graph-shaped strings out of the *output* row and re-reads them *after* evaluation
(`projection/freshness.go:23-48`, `cmd/refractor/main.go:565-572`) — a third instant over output
keys, unrelated to a read-set certificate. The bounded-staleness acceptance (no per-op freshness
gate, §6 "projectedAt … not a freshness ceiling", Story 1.5.4) is *narrowed in interpretation,
not edited*: staleness of real states remains accepted; fabrication of never-real states was
never inside it. §8.5 stages the component-doc posture paragraph.

## 7. Alternatives considered

1. **Do nothing / posture-only** — rejected: three proven conjunctive consumers on the plane
   whose correctness *is* auth correctness; §6.8's fail-closed doctrine does not admit
   "transient fabricated grants, attacker-timeable."
2. **Full snapshot / revision-pinned reads** — rejected, §5.
3. **Drop-on-drift (the first draft)** — rejected by the adversarial pass: dropping a narrowing
   write *preserves an over-grant* (fail-open), unboundedly so where the enumeration gap breaks
   the healing matrix; and an empty result is destructively ambiguous downstream. Re-execute +
   typed-failure requeue keeps every fail direction correct.
4. **Data-model colocation (single-key conjuncts per lens)** — works where applied
   (`capabilityRoles`, `objectLiveness`) but is author-dependent (forgetting = a silent
   combination-grant), and for `capabilityEphemeral` would denormalize op-meta/target state onto
   tasks. Kept as authoring guidance, not the mechanism.
5. **Platform-wide opt-in flag** — rejected: no business consumer (census); dead scaffolding.
6. **Cheaper drift oracle (stream-position watermark instead of per-key re-reads)** — considered:
   "did any core-kv event land between eval start/end?" is one read but invalidates on *any*
   write anywhere — a hot stream would starve every evaluation. Per-key is the discriminating
   check; costs are §5's table.

## 8. Reconciliation with the existing mental model

- **"Didn't the memo already fix this?"** It made one *vertex/aspect key* single-valued per
  evaluation. Link reads aren't memoized at all (§2), and cross-key ordering was explicitly left
  open by its own commit message.
- **"Don't the guards/sweep already catch it?"** The §6.2 guard orders *writes* per key; the
  sweep detects divergence from *current* truth. A row matching no truth at any instant is what
  re-derivation heals and neither rejects.
- **"Isn't this the v2 capability vector-clock fence?"** No — the fence addresses cross-plane
  *freshness* (auth read vs recent grant change); this is intra-row *fabrication*. Complementary;
  the fence stays deferred. (The cross-**document** union blend in §3's note is likewise
  freshness-family, not fixed here.)
- **"New state?"** None durable. The footprint lives inside the evaluation (the existing node
  memo + the new edge memo); validation adds reads; counters join existing health entries. The
  first draft's "adds one field" understated the adjacency interface work — corrected in §4.1.
- **§8.5 (documentation increment, rides Fire 1):** `docs/components/refractor.md` gains the
  consumer-contract paragraph: *business lens rows are convergent, not point-in-time — a consumer
  branching on a cross-key column pair must tolerate transient blends or go through the
  Processor; auth-plane envelope rows are footprint-validated.*

## 9. Test strategy

- **Executor unit:** node memo captures revisions (incl. absence = 0); the new edge memo makes
  link reads repeatable within one evaluation (a mid-evaluation adjacency mutation is invisible
  after first read — pinned); read errors still un-memoized; `existsAsPredicate` footprints the
  adjacency keys it inspected (present and absent).
- **Pipeline unit (Fire 1):** injected mid-evaluation commit on (a) a vertex aspect, (b) a link
  — assert: no adapter write of the blended doc (assert on the adapter call log, not final
  state — the negative-test false-pass discipline), one inline re-execution, converged doc
  lands; sustained-churn vector: requeue via retry queue as `ErrEvalDrift`, DLQ after max
  attempts, health counters advance; sweep vector: drift during `Reproject` reads as repair
  failure (not converged), retried next pass.
- **E2E (ephemeral stack):** the `capabilityEphemeral` role-queue tear — scripted interleave
  (role revoke committed between the task read and the link traversal, via an evaluator pause
  hook) ⇒ no cap doc ever contains the blended grant; step-3 denies throughout. A
  `capabilityServiceAccess` NOT-predicate vector: `unavailableAt` edge created mid-evaluation ⇒
  no grant lands.
- **Perf gate:** auth-plane p99 projection latency before/after on the seeded stack; the
  role-queue hot-path measured explicitly (§5's table is a prediction to check, not a hope).

## 10. Decomposition + the filed rows

- **Fire 1 (M)** — edge memo + revision-bearing `Neighbors` + footprint capture + verify/
  re-execute/requeue on `actorAggregate ∧ IsAuthPlane` + counters + §8.5 doc paragraph.
- **Fire 2 (returns to design)** — per-row footprints for the unanchored grant scans; designed
  against Fire 1's measured drift rates; the interim exposure is §4.5's stated residual.
- **Filed row 1 (board, this fire):** *[Weaver] Convergence directOps copy projected values into
  irreversible params* — ledger `DebitAccount{amountCents}` et al. must be Processor-derived
  from declared reads (the DDLs document the missing cross-check). ★★★, M.
- **Filed row 2 (board, this fire):** *[Refractor] Actor enumeration stops at the first actor —
  `reportsTo`-inherited grants never refresh* — a `task2` mutation enumerates only the direct
  report, never the manager whose `cap.ephemeral.*` doc embeds it
  (`actor_enumerator.go:136-151`); live staleness/over-grant window independent of tearing. ★★, S–M.

## 10.1 Build note — Fire 1 Increment 1 (Lattice Steward, 2026-07-28, `ea3f3852`)

**Scope-diff gate:** brief = §4.1's footprint-capture primitives only (revision-bearing
`Neighbors` + the edge memo + `nodeRef.revision`), deliberately narrower than all of §4's Fire 1 —
the validation/re-execute/requeue seam (§4.2-§4.4), the `actorAggregate ∧ IsAuthPlane` scope
predicate, the `evalDriftRetries`/`evalDriftRequeues` counters, and the §8.5 doc paragraph are
**not yet built**. Landed as its own green commit because it is independently correct and
independently valuable — it closes §2's stated pre-existing gap ("link reads aren't even
repeatable within one evaluation today") the same way `e8d78278` closed it for vertices — and
because the validation seam consumes exactly the primitives this increment ships (revisions on
`nodeRef` + the per-evaluation edge memo), so building it first de-risks the harder seam.

**What shipped:** `adjacency.Neighbors` returns the adjacency document's KV revision alongside
the edge list (0 = absent); `executor.nodeRef` carries the Core KV revision it was read at;
`executor.edges`/`edgeRevisions` memoize relationship-hop reads per evaluation exactly as `nodes`
already memoized vertex/aspect reads (`fetchEdges`, mirroring `fetchNode`). All 15 call sites of
`adjacency.Neighbors` (2 production: `executor.go` via the new `fetchEdges`, `actor_enumerator.go`
discarding the revision; 13 test) updated for the 3-value return. No behavior change to any
projection output — this is groundwork + a correctness fix for edge-read repeatability, not yet
the footprint-validation certificate itself.

**Tests:** `TestExec_EdgeReadIsRepeatableWithinOneEvaluation` (edge_memo_test.go) pins the
adjacency repeatable-read contract, mirroring `TestExec_AspectReadIsRepeatableWithinOneEvaluation`;
`TestExec_NodeRevisionCapturedOnRead` pins that a memoized `nodeRef`'s revision is stable within
one evaluation and moves in a fresh one.

**Gates run:** `go build ./...`, `make vet`, `golangci-lint run ./internal/refractor/...` (0
issues), `STRICT=1 go run ./scripts/lint-conventions.go` (0 issues), `go test
./internal/refractor/...` (all green, full package tree — the change's blast radius is contained
to `internal/refractor`, no cross-component caller exists).

**🏗️ CHECKPOINT — next increment (Fire 1 Increment 2):** wire the validation seam itself —
after `ExecuteWith` returns and before a result reaches the adapter/envelope, re-read each
footprint entry (`ex.nodes` + `ex.edges`/`edgeRevisions`) and compare revisions, scoped to
`actorAggregate ∧ projection.IsAuthPlane` (§4.4); on drift, one inline re-execution, then a typed
`ErrEvalDrift` on the existing pump retry-queue / sweep-repair failure channels (§4.3, never an
empty result set — the four downstream seams §12 #7 names); ship the `evalDriftRetries`/
`evalDriftRequeues` health counters (§4.6) and the `docs/components/refractor.md` §8.5 paragraph
in the same increment. Needs its own pipeline-unit + E2E tests per §9 (the scripted-interleave
`capabilityEphemeral` role-queue vector needs an evaluator pause hook the executor doesn't have
yet — build that hook as part of the increment, not a separate one). Worktree for Increment 1 was
`.claude/worktrees/refractor-eval-footprint-fire1` (merged, safe to remove); Increment 2 opens a
fresh worktree per the fresh-worktree-per-fire convention.

## 10.2 Build note — Fire 1 Increment 2 (Lattice Steward, 2026-07-28)

**Scope-diff gate:** brief = the full §4 validation seam Increment 1's checkpoint deferred —
footprint capture exposed from the engine, the `actorAggregate ∧ IsAuthPlane` scope predicate
(§4.4), the verify/re-execute/requeue mechanics (§4.2-§4.3) wired into `executeFullForActor`
(the one seam pump/fan-out/sweep repair all funnel through), the `evalDriftRetries`/
`evalDriftRequeues` health counters (§4.6), the evaluator pause hook (§9's E2E vector
prerequisite), and the `docs/components/refractor.md` §8.5 consumer-contract paragraph. All of
it landed. One adaptation from the checkpoint's literal wording: `ExecuteWith` itself was **not**
widened to a 3-return signature — grepping every call site turned up ~20 `packages/*` lens
cypher-test files calling `eng.ExecuteWith` directly (outside this fire's `internal/refractor`
scope, missed by the checkpoint's "two production callers" note, which only checked
`internal/`), so widening it would have forced changes across packages this increment is not
scoped to touch. Instead `full.Engine` gained a sibling method, `ExecuteWithFootprint`, that
returns the footprint alongside the rows; `ExecuteWith` is now a two-line wrapper that discards
it. Every pre-existing caller (production and test, in and out of `internal/refractor`) compiles
unchanged; only `pipeline.executeFullForActorOnce` calls the new method.

**What shipped:**
- `ruleengine.EvalFootprint{NodeRevisions, EdgeRevisions}` (`internal/refractor/ruleengine/ruleengine.go`)
  — the read-surface certificate type, shared by the engine and the pipeline.
- `full.Engine.ExecuteWithFootprint` (`ruleengine/full/executor.go`) — runs the same evaluation
  `ExecuteWith` always has, additionally returning the footprint built from the already-populated
  `ex.nodes`/`ex.edgeRevisions` memos (no extra reads). `ExecuteWith` is now a thin wrapper over it.
- `full.WithFootprintCapturedHook` (`ruleengine/full/footprint_hook.go`) — the evaluator pause hook:
  a context-carried callback `ExecuteWithFootprint` invokes exactly once, immediately after building
  the footprint and before returning. Test-only by convention (doc comment states it; nothing in
  production code calls it). This is the seam the E2E scripted-interleave test (deferred, see below)
  will use to commit a mutation mid-flight; this increment's own pipeline-unit tests use it to
  interleave a commit between one evaluation's footprint capture and the validation re-read (the
  §4.2 window this increment defends), and one `full`-package test pins the hook mechanism itself.
- `Pipeline.authPlane` + `SetAuthPlane` (`pipeline/pipeline.go`), wired from `plan.AuthPlane` in
  `projection/driver.go`'s `InstallActorAggregate` (the value it already computed for sweep-interval
  sizing, now also stored on the pipeline).
- `failure.ErrEvalDrift` (`failure/eval_drift.go`) + an explicit `Classify` branch routing it to
  `CatTransient` (`failure/classify.go`) — was already the default fallthrough category; the
  explicit branch documents the intent so a future default-category change can't silently reclassify it.
- The validation seam itself (`pipeline/evaluate.go`): `executeFullForActor` split into
  `executeFullForActorOnce` (one evaluation + envelope wrap + collision guard + retractions,
  returning results and footprint) and `executeFullForActorAttempt` (the attempt-bounded
  orchestrator: `needsFootprintValidation` gates on `actorAggregate ∧ authPlane`; in scope,
  `footprintValid` re-reads every footprint entry — `currentNodeRevision` mirrors the executor's
  own absence semantics, including the soft-delete/null-body case, so a key absent for the SAME
  reason at both reads never spuriously reads as drifted; on drift, one recursive retry with
  freshly re-fetched actor props, bounded by `maxFootprintRetries = 1`; sustained drift returns
  `(nil, failure.ErrEvalDrift)`, never an empty-but-non-nil slice).
- `dispositionEvalErr` (`pipeline/pipeline.go`) gained an `enumeratedActors []string` parameter
  (threaded through all its call sites) and a branch: an `ErrEvalDrift`-classified error on an
  actor-aggregate pipeline with a retry queue configured is routed through
  `enqueueActorReprojectRetry` — the SAME re-evaluate-and-write closure the write-stage transient
  branch already uses — rather than a bare `Nak`, so drift gets the queue's backoff/DLQ/health
  machinery instead of unbacked-off redelivery. The sweep path needed no change: `Reproject` →
  `reprojectActors` → `executeFullForActor` already propagates the wrapped error through to
  `sweep.go`'s `pass()`, which already treats any `Reproject` error as a repair failure
  (`noteActorFailure` → `CapabilityRepairFailing` accounting) — traced, not modified.
- `healthwire.Entry.EvalDriftRetries`/`EvalDriftRequeues` + `Reporter.RecordEvalDriftRetry`/
  `RecordEvalDriftRequeue` (read-modify-write under `writeMu`, mirroring `RecordError`), called from
  inside `executeFullForActorAttempt` at the retry and requeue decision points respectively.
- `docs/components/refractor.md`: the read-set-certificate-vs-`projectedFromRevisions` paragraph
  + consumer-contract sentence (Capability KV envelope section), and an `evalDriftRetries`/
  `evalDriftRequeues` row in the Capability-Lens health signal table.

**Tests** (`internal/refractor/ruleengine/full/footprint_test.go`,
`internal/refractor/pipeline/eval_drift_test.go`):
- `TestExecutor_Footprint_CapturesNodeAndEdgeRevisions` — `footprint()` renders the node/edge
  memos correctly, including a memoized-absent node as revision 0.
- `TestExecuteWithFootprint_HookFiresAfterFootprintBuilt` / `TestExecuteWith_StillInvokesTheHookThroughTheSharedCore`
  — the pause hook fires exactly once, after the footprint is captured (a mutation committed
  inside the hook is provably absent from the returned footprint), and both entry points share it.
- `TestFootprintValid_{UnchangedRevisionsMatch,RevisionChanged,NewlyAbsentKey,NewlyPresentKey,EdgeRevisionChanged}`
  — the validate-helper unit tier §9 asks for, node and edge.
- `TestExecuteFullForActor_InlineRetryConverges` — a role-name edit lands (via the hook) between
  the first footprint capture and validation; the retry re-reads and the returned row carries the
  POST-drift value, not the one first read (a vacuous-assertion check: without the fix this would
  read the pre-drift value instead, since no validation would ever trigger the retry).
- `TestExecuteFullForActor_SustainedDrift_ReturnsErrEvalDrift_NeverEmpty` — the role keeps moving
  on every attempt; returns `(nil, ErrEvalDrift)` after exactly `maxFootprintRetries` retries, with
  both health counters advancing.
- `TestHandle_SustainedDrift_NoAdapterWrite_RequeuesViaRetryQueue` — the same sustained-churn
  vector driven through the real `p.handle` pump entry point: asserts on the recording adapter's
  call log (never invoked) rather than final state, and that the actor lands in the retry queue —
  the negative-test-false-pass check: without the fix, `evaluateForEntryRaw` would succeed on its
  first (already-stale) execution and `handle` would proceed straight to `writeResults`, which
  WOULD call `adpt.Upsert`.
- `TestExecuteFullForActor_NonAuthPlaneLens_SkipsValidation` — the scope-predicate control: with
  `authPlane: false` the hook is never even consulted (fired once, not retried), zero cost.

**Deliberately deferred, honestly:** the full E2E ephemeral-stack scripted-interleave test (§9's
third tier — the `capabilityEphemeral` role-queue tear with a real lens install, and the
`capabilityServiceAccess` NOT-predicate vector) is **out of scope for this increment**. It needs
real lens installs against the ephemeral stack (`internal/refractor`'s own e2e harness,
`bootstrap_e2e_test.go`-style, but for a live `capabilityEphemeral`/`capabilityServiceAccess`
lens under `orchestration-base`/`service-location`), which is a materially larger build than this
increment's pipeline-unit tier — and the pause hook this increment ships is exactly what that
test will need, so nothing further has to be built for it to start. The perf gate (§9's last
bullet, measuring auth-plane p99 before/after and the role-queue hot path explicitly) is likewise
not run here — it needs a seeded stack, not a unit-test fixture, and belongs with the E2E fire or
a dedicated measurement pass.

**Fire 1 status:** structurally complete except for the E2E tier + perf gate named above. Every
other §10 Fire 1 scope item (edge memo, revision-bearing `Neighbors`, footprint capture,
verify/re-execute/requeue, the `actorAggregate ∧ IsAuthPlane` predicate, counters, the §8.5 doc
paragraph) has shipped and is test-pinned at the pipeline-unit tier. The E2E scripted-interleave
+ perf gate is the remaining checkpoint; Fire 2 (the unanchored grant-table scans, §4.5) stays a
separate, still-undesigned increment as ratified.

**Gates run:** `go build ./...`, `make vet`, `golangci-lint run ./internal/refractor/...` (0
issues), `STRICT=1 go run ./scripts/lint-conventions.go` (0 issues), `go test
./internal/refractor/...` (all green, full package tree). `go vet ./packages/...` was also run
manually (outside the stated gate list) specifically to confirm the `ExecuteWith`-unchanged
decision above left every `packages/*` lens test compiling — it does.

## 11. Risks

- **Drift-retry rate under real churn is unmeasured** — Fire 1 ships the instrument before
  Fire 2 widens scope; the requeue path degrades to *stale-real* (never torn), which is today's
  accepted posture.
- **The role-queue hot path doubles an already-heavy walk** (§5) — measured by the perf gate; a
  material result indicts the branch's shape, not the certificate.
- **Validation itself races** (drift landing between validation and adapter write) — the window
  shrinks from "whole evaluation" to "validate→write gap"; the requeue/healing semantics cover
  it; stated rather than claimed zero.
- **Fire 2's absence leaves census row 3 exposed interim** — transient and event-healed; the
  alternative (blocking Fire 1 on Fire 2's design) leaves rows 1–2 exposed too, which is
  strictly worse.

## 12. Adversarial pass — run, findings folded

Independent read-only reviewer, this fire. Verdict on the first draft: *"diagnosis sound;
mechanism as specified not buildable and not sufficient"* — accepted, and §4 was re-derived:

- **#1 (blocker):** the memo excludes adjacency — both harmful conjuncts are link reads →
  footprint now spans the full read surface; `Neighbors` gains revisions + an edge memo (§4.1).
- **#2 (blocker):** `capabilityServiceAccess`'s negative predicates invert absence-is-safe →
  absent adjacency reads are footprinted; census row 2 added (§3, §4.1).
- **#3 (blocker):** drop-on-drift is fail-open (preserves over-grants) → replaced with
  re-execute + typed-failure requeue (§4.3, §7.3).
- **#4:** `reportsTo` enumeration gap breaks the healing matrix → matrix demoted to a latency
  bound; live bug filed (§2, §10).
- **#5:** Fire 2's chokepoint attribution ill-defined → returned to design (§4.5).
- **#6:** `projectedFromRevisions` mis-cited as precedent → corrected (§6).
- **#7:** silent drop ambiguous at four downstream seams (incl. sweep false-convergence and a
  diff-retraction mass-delete) → typed error on existing failure channels (§4.3).
- **#8:** bare `IsAuthPlane` scope would have caught the grant scans and mass-revoked via
  diff-retraction → Fire 1 predicate is `actorAggregate ∧ IsAuthPlane` (§4.4).
- **#9:** no header-only read exists; hot-queue footprints are large → §5 rewritten with the
  honest cost table + perf gate.
- **#10/#11/#12:** census rows 2 and 6 added; the cross-document `ReadAndMerge` blend noted as
  an explicit out-of-scope residual (§3).
