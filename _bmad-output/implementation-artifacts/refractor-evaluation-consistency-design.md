# Evaluation consistency — the torn-row verdict + auth-plane footprint validation (design)

**Status: ✅ Andrew-ratified (2026-07-27)**, with **§13 — the Increment 2 re-decision —
✅ Andrew-ratified (2026-07-29).** The ratified verdict, mechanism and Fire/Increment split all
stand; §13 re-decides the **scope predicate** (§4.4, rewritten in place and superseded by §13.3)
and fixes a **second defect the revert exposed** (§13.4). Increment 1 is shipped (`ea3f3852`);
**Increment 2 is build-ready — build it from §13.9, not from §4.4 or §10.1.**
Author: Winston (Designer fires, 2026-07-27 · 2026-07-29) · Lattice lane
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
     the fact that link reads today aren't even repeatable within one evaluation. (Shipped in
     Increment 1, `ea3f3852`. **The *comparison* granularity is refined by §13.4**: the adjacency
     document is a per-node bundle of every relation, so comparing its whole revision
     over-detects — the footprint compares the **relation-scoped edge set the walk actually
     consumed**, falling back to the whole-document revision whenever the walk's selector is not
     narrow enough to scope. Capture is unchanged; only the equality test narrows.) Negative
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
4. **Scope predicate, Fire 1 — SUPERSEDED by §13.3. Do not build the text struck below.**
   ~~`actorAggregate ∧ projection.IsAuthPlane` — every capability-kv envelope lens.~~ That
   two-way conjunction shipped, broke `make verify-package-service-location`, and was reverted
   (§10.1): it validates **census row 4** (`capabilityRoles`), which §3 had already cleared as
   single-key and tear-safe, and validating it turns an ordinary package install into sustained
   drift-requeue. The predicate is now the **three-way** conjunction
   `actorAggregate ∧ IsAuthPlane ∧ multi-binding conjunct unit`, where the third term is
   **derived from the lens's own cypher at compile time** — no author declaration, row 4 exempt
   by construction. Full mechanism, the classifier's rules, and its fail-closed defaults: **§13.3**.
   (Retained from the ratified reasoning: the bare `IsAuthPlane` predicate alone would also catch
   the unanchored grant scans and — via drift-on-every-scan — would have mass-revoked
   `actor_read_grants` through diff-retraction. Both surviving conjuncts are deliberate.)
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

**→ The re-decision this checkpoint asks for is now written: [§13](#13-increment-2-re-decision--the-scope-predicate-and-the-false-drift-mechanism-the-revert-exposed)
(📐 awaiting-Andrew, 2026-07-29). It re-shapes the predicate AND fixes a second defect this
checkpoint's root cause did not reach (§13.1). Build Increment 2 from §13.9, not from the paragraph
below — which is retained as the evidence record.**

**🏗️ CHECKPOINT — next increment (Fire 1 Increment 2), REVISED after a reverted attempt
(2026-07-28):** a full build of the §4.2-§4.6 validation seam (footprint capture exposed from the
engine, `actorAggregate ∧ projection.IsAuthPlane` scope predicate, verify/re-execute/requeue wired
into `executeFullForActor`, `evalDriftRetries`/`evalDriftRequeues` counters, the evaluator pause
hook, the `docs/components/refractor.md` paragraph) passed every unit/pipeline gate
(`go build`, `make vet`, `golangci-lint`, `lint-conventions`, `go test ./internal/refractor/...`
all green) but was **reverted off `main`** (commits `da1b4641`/`b709a62c`, reverted
`1e33a90f`/`72f9d7f6`) when CI's `stack-gates` job caught a real liveness regression the unit
tier cannot see: `make verify-package-service-location` failed 10 assertions —
`cap.roles.identity.<operator>` never reflected 10 permissions granted to one role in a tight
install-time loop within the gate's 10s window.

**Root cause (grounded, not guessed):** `projection.IsAuthPlane` gates on **bucket**
(`capability-kv`), so the scope predicate `actorAggregate ∧ IsAuthPlane` catches every lens
writing there — including **census row 4** (`capabilityRoles`, §3 table), which the census itself
already proved is single-key (`perm.data`) and **safe from tearing** ("ordinary bounded
staleness", no validation needed for correctness). The coarse predicate validates it anyway (by
design, §4.4: "forgetting is impossible"). During a package install, many permissions land
`grantedBy` the SAME role in rapid succession — a normal pattern (service-location seeds 10), not
a pathological one. Each permission's link event fans out to reproject every actor holding that
role; the fan-out's footprint includes the role's own adjacency document, which the NEXT
permission's `grantedBy` link also bumps. Under real install-time arrival rates the window between
footprint capture and validation is not always ms-scale relative to sibling writes to the SAME
adjacency node, so `maxFootprintRetries = 1` is exhausted and the evaluation requeues via
`ErrEvalDrift` — repeatedly, for a lens the census had already cleared. This is exactly the
unmeasured risk §5 named ("the role-queue hot path doubles an already-heavy walk... if the
drift-retry rate on hot queues is material, the branch's own shape is the thing to revisit") and
§11 named ("drift-retry rate under real churn is unmeasured") — now measured, and material enough
to break a routine package install, not just degrade its latency.

**What must change before Increment 2 re-attempts** (design work — do not just retry the same
scope predicate): narrow §4.4's predicate from the blanket `actorAggregate ∧ IsAuthPlane` to the
census's actual harmful set — rows 1-3 only (`matchEphemeralGrant`'s taskKey∧operationType∧target,
`matchServiceAccess`'s service∧allowedOperations, the RLS grant tables) — so a single-key-conjunct
auth-plane lens like `capabilityRoles` (row 4) is exempted by construction, not merely by
practice. This likely means predicate needs a per-lens declaration (e.g. a `Rule.NeedsFootprint`
/ conjunctive-columns marker set at compile time from the cypher's own MATCH shape, or an explicit
opt-in flag on the 3 known lenses) rather than a structural bucket test — **this is itself a
design decision**, not an implementation detail, since getting the boundary wrong either
re-admits row 4's regression or silently exempts a future row-1/2/3-shaped lens. Flagged for
`lattice-designer` to ground and re-decide before Increment 2 is re-attempted; the Steward should
not re-guess it. A shared-adjacency-fan-in stress scenario (N rapid grants to one role) belongs in
whatever tier replaces/extends §9 for the re-scoped predicate, since it is what caught this and the
unit tier alone did not.

Worktree for the reverted attempt (`.claude/worktrees/refractor-eval-footprint-fire2`) has been
removed. Increment 1's primitives (revision-bearing `Neighbors`, the edge memo, `nodeRef.revision`)
remain on `main`, unaffected by the revert — only Increment 2's additions (`ExecuteWithFootprint`,
the validation seam, the scope predicate, the counters, the pause hook) were reverted. A future
re-attempt still builds on Increment 1, opening a fresh worktree per the fresh-worktree-per-fire
convention.

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

---

## 13. Increment 2 re-decision — the scope predicate, and the false-drift mechanism the revert exposed

**Status: ✅ Andrew-ratified (2026-07-29)** — approved as recommended: the derived classifier
(Option A) over the opt-in flag, and both fixes shipping together. Author: Winston (Designer fire,
2026-07-29). Raised by §10.1's own hand-off: *"this is itself a design decision… flagged for
`lattice-designer` to ground and re-decide before Increment 2 is re-attempted; the Steward should
not re-guess it."* The Steward was right to stop. **Increment 2 is now build-ready — §13.9 is the
build order.**

### For Andrew (one-look ratification block)

**What it does (two lines).** Increment 2's scope predicate stops being a *bucket* test and becomes
a **property of the lens's own cypher**: a lens is footprint-validated iff some value-tuple it emits
conjoins fields read from **more than one graph binding** — derived at compile time, never declared
by an author. And the footprint stops comparing whole *adjacency documents* and starts comparing
**only the edges the walk actually followed**, so a sibling write of an unrelated relation to a
shared hub node no longer reads as drift.

**The one thing to understand before ratifying.** The revert (§10.1) was diagnosed as *one* defect —
an over-broad predicate that caught the harmless `capabilityRoles`. Re-grounding this fire found
**two**, and fixing only the first would have shipped Increment 2 with the same starvation still
live, just no longer standing on an assertion: `capabilityEphemeral` — census **row 1**, the lens
the whole mechanism exists for — traverses `(identity)-[:holdsRole]->(role)<-[:queuedFor]-(task3)`
(`orchestration-base/lenses.go` role-queue branch), so **it footprints the same role adjacency
document** that a package install bumps ten times in a few seconds. Narrowing the predicate removes
`capabilityRoles` from the blast radius; it does **not** remove the blast radius. §13.4 is the half
that does, and it is the half a predicate-only re-attempt would have missed.

**Fork — how a lens is classified. RESOLVED: derived, not declared (Option A).** §10.1 floated two
shapes; I am recommending the first and rejecting the second outright:
- **A. Derived from the cypher (recommended).** The compiler walks the RETURN clause and computes,
  per emitted value-tuple, how many distinct bindings its fields come from. Nothing to declare,
  nothing to forget, and row 4 is exempt *by construction* — which is precisely the property §10.1
  demanded. Cost: a classifier that is now security-relevant, mitigated by fail-closed defaults
  (§13.3) and a test that pins all four census verdicts.
- **B. An explicit opt-in flag on the three known lenses (rejected).** This is **default-open**: the
  next auth-plane lens author who forgets the flag ships a silent combination-grant, and nothing
  errors. That is the exact failure direction the D1 read-path pass caught in my own work
  (`no authzAnchor ⇒ public-read`). A security boundary whose omission grants is not a boundary.
  Rejected even though it is the smaller build.

**Frozen-contract change: NONE.** Contract #6 §6.2/§6.13 are built to, not edited. No board-visible
scope change either: this is still Fire 1 / Increment 2 of the ratified design, re-shaped.

**Also for your attention (not a fork).** §13.9 asks the Steward to write the fan-in stress test
**first, and watch it go red**, before either fix lands. The reverted attempt passed every unit gate
and was caught only by a stack gate that happened to assert on the affected lens; the regression
deserves a test that fails for the right reason, at the tier that can see it.

### 13.1 What the revert actually proved — two defects, not one

§10.1's root cause is correct and stands. What it under-states is the **generality** of the
mechanism it found. Separating them matters because they have different fixes:

| | Defect | Who it hits | Fix |
|---|---|---|---|
| **1** | The predicate validates a lens the census already cleared. Row 4 (`capabilityRoles`) emits `{operationType, scope, lanes}` all off one `perm` key — a torn *set* mixes individually-real entries, i.e. ordinary bounded staleness. Validating it buys nothing and costs everything, because a package install *legitimately* changes its answer ten times in a row. | Row 4 only | §13.3 — narrow the predicate |
| **2** | The footprint compares **whole adjacency documents**. `adj.<nodeId>` bundles *every* relation incident on that node, so any write to any relation invalidates every walk that touched the node — including walks that never follow that relation. A shared hub (a role, a building, an op-meta) under normal write pressure therefore drifts every evaluation that passes near it. | Rows **1–3** — the lenses that must keep validating | §13.4 — scope the comparison |

**Defect 2, demonstrated on the lens the mechanism exists for.** `capabilityEphemeral`'s role-queue
branch walks `(identity)-[:holdsRole]->(role:role)<-[:queuedFor]-(task3:task)`, so its footprint
contains `adj.<roleId>`. The service-location install grants ten permissions to one role in a tight
loop; each is a `grantedBy` link whose adjacency build bumps `adj.<roleId>`. `capabilityEphemeral`
follows **`queuedFor`** on that node and never looks at `grantedBy` — yet under whole-document
comparison every one of those writes is drift. Same install, same node, same starvation, different
lens. It did not break the build only because nothing asserts on `cap.ephemeral.*` during a package
install (no tasks exist yet); a stack with live queued work would have failed the same way, and
`ErrEvalDrift` → `CatTransient` → retry-queue → `MaxAttempts` → DLQ means the failure mode is an
auth doc that stops advancing, not merely one that lags.

This is the general shape of it: **the auth-plane lenses are exactly the lenses that walk through
shared, high-degree hub vertices** — roles, op-metas, locations. Whole-document adjacency comparison
is therefore not a small imprecision on this plane; it is anti-correlated with the workload.

### 13.2 Grounding ledger (verified `file:line` this fire)

| Fact | Where |
|---|---|
| The reverted predicate, verbatim | `da1b4641` → `evaluate.go` `needsFootprintValidation()`: `(p.envelopeFn != nil \|\| p.multiEnvelopeFn != nil) && p.authPlane` |
| `IsAuthPlane` is a pure bucket/target test — no cypher input, by design | `internal/refractor/projection/plan.go:97-104` (`nats_kv ∧ capability-kv`, or `postgres ∧ GrantTable`) |
| The full engine **retains its AST** on the compiled artifact — so a compile-time cypher analysis needs no re-parse and no new plumbing | `internal/refractor/ruleengine/full/ast.go:247` (`CompiledRule{Query *Query}`); node types `Return`/`ProjectionItem`/`MapLiteral`/`FunctionCall`/`VariableRef`/`PropertyAccess`/`PatternComprehension`/`CaseExpr`/`ListLiteral`/`BinaryOp` all present and ANTLR-free |
| The relation selector **is in hand at the adjacency read site** — the traversal filters `e.Name != rel.Type` and `directionMatches(e.Direction, rel.Direction)` immediately after `fetchEdges` | `full/executor.go:643-653` |
| The edge memo + revisions Increment 1 shipped are keyed by adjacency node id, the exact unit §13.4 refines | `full/executor.go:576-589` (`ex.edges` / `ex.edgeRevisions`) |
| `EdgeEntry` carries `EdgeID`, `Name`, `Direction`, `OtherNodeID` — enough to compare a relation-scoped edge set without re-deriving anything | `internal/refractor/adjacency/builder.go:21-28` |
| Row 4's entry is genuinely single-binding | `packages/rbac-domain/lenses.go:80-92` — `{operationType: perm.data.…, scope: perm.data.…, lanes: perm.data.…}` |
| Row 1's entry is genuinely 3-binding | `packages/orchestration-base/lenses.go` `capabilityEphemeralSpec` RETURN — `{taskKey: task.key, operationType: op.data.operationType, target: tgt.key, expiresAt: task.data.expiresAt}` |
| Row 2's entry is genuinely 2-binding before its comprehension is even counted | `packages/service-location/lenses.go` `capabilityServiceAccessSpec` — `{service: svc.key, resolvedVia: [loc.key], allowedOperations: [(svc)-[:permitsOperation]->(op) …]}` |
| `ErrEvalDrift` routes `CatTransient` → the actor-reproject retry closure (backoff, then DLQ at `MaxAttempts`) — so sustained drift is a *stalled auth doc*, not just latency | `da1b4641` → `failure/classify.go` §1.5, `failure/eval_drift.go`, `pipeline.go` `dispositionEvalErr(…, enumeratedActors)` |
| **Adversarial probe on the row-4 exemption:** nothing conjoins `doc.Roles` with `platformPermissions` on an authorizing path — `Roles` is read **only** on the denial path, for FR22 `actorRoles` response construction | `internal/processor/step3_auth_capability.go:280-284`; `internal/capabilitykv/read.go:73` unions the two arrays independently |

### 13.3 D-A — the scope predicate: a conjunct-unit classifier, derived at compile time

**The predicate becomes three-way:**

```
needsFootprintValidation  ⟺  actorAggregate  ∧  IsAuthPlane  ∧  hasMultiBindingConjunctUnit(rule)
```

The first two conjuncts are unchanged and still deliberate (§4.4): `IsAuthPlane` keeps business
lenses out, `actorAggregate` keeps the unanchored grant scans out (whole-evaluation validation is
vacuous there — §4.5, Fire 2). The third is new, and it is the one derived from the lens itself.

**Conjunct unit — the definition.** A *conjunct unit* is one value-tuple a consumer matches as a
whole. Computed from the RETURN clause:

- **U₀** = the set of bindings referenced by all **top-level, non-aggregate** projection items.
  (Aggregate/collection items are excluded here — they contribute their own entry units instead.)
- **Uᵢ** = for every `MapLiteral` appearing anywhere inside an aggregate expression (`collect(…)`,
  including through `+` concatenation of several collects, `CASE` arms and list literals), the set
  of bindings referenced by its field expressions.
- A binding is contributed by a `VariableRef`, or by the root `VariableRef` of a `PropertyAccess`
  chain. `Literal` and `ParameterRef` (`$now`, `$projectedAt`, `$actorKey`) contribute **nothing** —
  they are evaluation-constant, and counting them would classify every lens as multi-binding.
- A `PatternComprehension` contributes the bindings of its **outer** anchor plus its own internal
  bindings to the unit that contains it (row 2's `allowedOperations` is inside the same map literal
  as `service`, so that map is already multi-binding on `svc`+`loc` before the comprehension counts).

`hasMultiBindingConjunctUnit` is true iff **any** unit references ≥2 distinct bindings.

**Reproducing the ratified census — the classifier's acceptance criteria:**

| Census row | Lens | Units | Verdict | Matches §3? |
|---|---|---|---|---|
| 1 | `capabilityEphemeral` | entry `{task, op, tgt}` ×3 branches | **validate** | ✓ HARMFUL |
| 2 | `capabilityServiceAccess` | entry `{svc, loc, (op)}` | **validate** | ✓ HARMFUL |
| 3 | grant tables (`staffReadGrants`) | U₀ = `{identity, building}` | **validate** | ✓ HARMFUL |
| 4 | `capabilityRoles` | U₀ = `{identity}`; entry `{perm}`; entry `{role}` | **exempt** | ✓ safe |
| — | `capabilityRoleIndex` | U₀ = `{perm}`; entry `{role}` | **exempt** | ✓ (keyed by operationType, unguarded, documented safe) |

Row 4 falls out cleanly and needs **no anchor-column special case**: `identity.key AS actorKey` is
its only top-level scalar, and its two collections each carry single-binding entries. That is the
"exempt by construction, not merely by practice" §10.1 asked for.

**Fail-closed defaults — the classifier defaults to VALIDATE, never to exempt.** Getting this
backwards is a silent combination-grant, so every uncertainty resolves toward cost, not toward risk:

- No RETURN clause found, or the compiled artifact is not the full engine's → **validate**.
- Any expression form the walker does not recognise (a function call it cannot see through, a
  nested subquery, a future AST node) → **validate**.
- A projection item aliased through a `WITH` whose provenance cannot be traced back to bindings →
  **validate**.
- **Multiple RETURN branches** (engine `UNION`, if the shared-keyspace design lands — §13.10) →
  classify each branch and **union the verdicts**; an unclassifiable branch validates the whole.

**Where it lives.** `projection.Compile` already receives the `lens.Rule` with `CompiledRule`
attached and already computes `AuthPlane` (`plan.go:97-121`); the classifier is one more derived
field on `ProjectionPlan` (`RequiresFootprintValidation bool`), wired to the pipeline through the
existing `SetAuthPlane`-shaped seam. This is the smallest extension of machinery that already
exists — no new lifecycle, no new registration, no author-facing surface.

**No lint gate ships with this, and that is deliberate — stated so a reviewer knows it was
considered, not forgotten.** The house rule is that a design establishing a *convention* must ship
the gate that binds the next author, because a migration clears today's debt and nothing stops
tomorrow's. Here the design establishes **no author convention**: there is no idiom to write
correctly, no annotation to remember, and therefore nothing for a gate to default-deny. The
derivation *is* the gate. What ships instead is an **activation-time record** (each auth-plane
lens's derived verdict is logged and surfaced on its health entry, so an operator can see which
lenses are paying validation) and a **unit test pinning all five verdicts above**, so a cypher edit
that flips a lens's class shows up as a failing test in review rather than as a silent change in
security posture.

**The one direction a flip is safe in.** If an author later denormalises a validated lens into
single-binding entries — §7 alternative 4, "data-model colocation" — the classifier exempts it, and
that exemption is *correct*: colocation removes the tear at the source. The classifier rewards the
better data model instead of taxing it.

**Residual, named not buried.** The unit is the emitted value-tuple's *field* provenance. A tuple
whose **membership** was gated by a WHERE/pattern read at a different instant than its fields is
**not** counted as multi-binding. That is not an oversight: it is the ratified census's own line
between the two classes — *staleness serves a real past state; tearing fabricates a never-real one*
(§3 row 4's reasoning, which Andrew ratified). Counting membership provenance would classify every
lens as multi-binding (every traversal spans bindings) and put us straight back at the reverted
predicate. If that line is ever revisited, the classifier gains a rule — it does not need a
different shape.

### 13.4 D-B — the footprint compares the edges the walk followed, not the document that holds them

**The change.** Increment 1 memoizes, per adjacency node, `ex.edges[nodeID]` (the edge list) and
`ex.edgeRevisions[nodeID]` (the document revision). Validation currently re-reads and compares the
revision. Instead:

1. During traversal, record per adjacency node the **selectors consulted** — the
   `(rel.Type, rel.Direction)` pairs the walk filtered on, which `executor.go:648-653` already has
   in hand at that exact line. One node may accumulate several selectors across hops; keep the set.
2. The footprint entry for that node becomes the **selector set plus the identities of the edges
   that passed it** — `EdgeID` (globally unique: a Contract #1 link key) plus its `isDeleted`
   disposition, from the `EdgeEntry` fields already persisted.
3. Validation re-reads the adjacency document once (same single `KVGet` as today — **no extra
   reads**), re-applies the recorded selectors, and compares the resulting edge-identity set. Equal
   ⇒ no drift. Different ⇒ drift, exactly as before.

**Fail-closed fallbacks — any of these reverts that node to whole-document revision comparison:**
an untyped hop (`rel.Type == ""`, which consumes every edge on the node), a variable-length hop
whose expansion cannot be attributed to a single relation name, or any read of the node's edge list
that did not go through the selector-recording path. Coarser is always the safe direction here, and
the fallback is one branch.

**Why this is the right size.** It rides entirely on primitives Increment 1 already shipped, adds
zero KV round-trips, and removes the dominant false-drift class outright: `capabilityEphemeral`'s
footprint of `adj.<roleId>` becomes *"the `queuedFor` inbound edges"*, and ten `grantedBy` edges
landing on that node change nothing it recorded. Verified against the exact regression that caused
the revert.

**What it deliberately does not do.** It does not make row 4's drift go away, and it must not:
`capabilityRoles` follows `grantedBy` on that same node, so during an install its answer *genuinely*
changes and any honest comparison reports drift. Row 4's problem is that validation is pointless for
it — which is §13.3's job, not this one. **The two fixes are orthogonal and both are required**;
shipping either alone leaves a live starvation path (§13.1).

### 13.5 Alternatives considered — and why each loses to the pair above

1. **Raise `maxFootprintRetries` above 1.** Rejected: the churn outlasts the retries by design (an
   install burst is seconds; drift retries are inline), so this multiplies the doubled read cost
   without changing the outcome. It treats a precision defect as a patience defect.
2. **Quiesce validation during package install.** Rejected on the "no expedient-but-wrong-long-term
   option" rule: an install is just writes, and production churn on a shared role looks identical.
   A mode that is safe only because nothing is watching becomes the precedent every later agent
   copies.
3. **Per-column read attribution (validate only the keys whose values flow into a conjunct).** This
   is strictly more precise than §13.4 and it is exactly §4.5's Fire 2 problem, which the ratified
   design already established is **ill-defined at the read chokepoint** (seed scans attribute to the
   root scope; the memo hides first-readers; bindings clone and fold through grouping). Rejected as
   the wrong increment, not the wrong idea — §13.4 captures most of its benefit with a filter.
4. **Compare output instead of revisions** (re-execute; write if both executions agree). Tempting,
   and it converges beautifully under benign churn — but two executions agreeing does not establish
   that either was untorn, only that the tear was stable, and it doubles evaluation cost on the
   hottest path rather than doubling *read* cost. Rejected: weaker guarantee, higher price.
5. **Author opt-in flag (§10.1's Option B).** Rejected in the For-Andrew block: default-open.

### 13.6 Reconciliation with the existing mental model

- **"Didn't §10.1 already say what to do?"** It named the *symptom* precisely (row 4 over-caught)
  and correctly refused to guess the *shape*. It did not have §13.1's second defect, which is why
  it could not have been implemented as written without re-shipping the starvation.
- **"Doesn't a cypher-derived predicate contradict 'never route off the canonical name'?"** No — it
  is the same principle one level deeper. `RequiresGuard`/`IsAuthPlane` are already derived from the
  compiled plan rather than from a name list (`plan.go:56-104`); this derives from the compiled
  *query*. Deriving from the artifact is the house pattern; declaring is the deviation.
- **"Does this add new state?"** None durable. The classifier's verdict is a compile-time boolean on
  `ProjectionPlan`; the selector sets live inside one evaluation, in the memo Increment 1 already
  allocates.
- **"Does exempting a lens weaken Increment 1?"** No. Increment 1's edge memo and revision capture
  are unconditional and stay unconditional — repeatable-read within an evaluation is a correctness
  property for *every* lens. Only the post-evaluation validation is scoped.
- **"Is the census being re-opened?"** No. §13.3's acceptance criteria are that the classifier
  **reproduces** the ratified §3 verdicts, row for row. If it disagreed with the census, the
  classifier would be wrong — not the census.

### 13.7 Contract surface

**No frozen-contract edits, and none staged.** Contract #6 §6.2 (monotonic write guard) and §6.13
(actor-aggregate projection plan) are built to. The classifier reads the compiled cypher, which is
package-authored content, not contracted vocabulary — a package author gains no new field, no new
keyword, and no new obligation.

### 13.8 Test strategy

Extends §9; the additions are what the reverted attempt lacked.

- **Classifier unit (new, `projection`):** all five verdicts of §13.3's table pinned against the
  **real** shipped specs (import the package lens definitions — a hand-written cypher fixture would
  prove only itself). Plus the fail-closed vectors: no RETURN → validate; unknown expression node →
  validate; `$param`-only tuple → not multi-binding; two RETURN branches → union.
- **Selector-scoped footprint unit (new, `full`):** a walk following `queuedFor` on a node, an
  unrelated `grantedBy` edge added to that node mid-evaluation ⇒ **no drift**; a `queuedFor` edge
  added ⇒ **drift**; an untyped hop on the same node ⇒ falls back to revision comparison and drifts
  on either. Assert on the drift verdict, not on the final projection — a passing projection would
  also pass if validation never ran (the negative-test false-pass discipline).
- **Fan-in stress vector (new, the tier the revert needed) — write it FIRST and watch it go red.**
  N rapid `grantedBy` grants to one role, with a live queued task so `capabilityEphemeral` is
  non-trivially projecting, asserting *both* auth docs converge within the same window the stack
  gate allows. On unmodified `main` + the reverted Increment 2 it must fail; with §13.3 alone it must
  still fail (that is the proof defect 2 is real and independent); with both it passes. It belongs
  at the tier that can see it — the `verify-package-service-location`-shaped stack gate is what
  caught this, and `make verify-package-service-location` returning green is the acceptance signal.
- **Retained from §9 unchanged:** the executor memo units, the injected mid-evaluation commit
  vectors, the `capabilityEphemeral` role-queue e2e tear, the `capabilityServiceAccess`
  NOT-predicate vector, and the perf gate (now with the honest expectation that `capabilityRoles`
  pays **zero** validation cost, which is most of the §5 table's predicted regression).

### 13.9 Build order for the Steward (one fire, internal order)

Increment 2 re-attempts as **one fire** — the two fixes are correctness-coupled (§13.4 alone leaves
row 4 over-validated; §13.3 alone leaves rows 1–3 starving) and neither is independently shippable
as a behaviour change. Internal order:

1. **The fan-in stress vector, red.** Before any fix. Its failure is the specification.
2. **§13.3 the classifier** + its unit table + the activation-time record. Stress vector still red.
3. **§13.4 selector-scoped footprint** + its unit vectors. Stress vector goes green.
4. Re-land the reverted seam (`ExecuteWithFootprint`, the validation/re-execute/requeue path, the
   `evalDriftRetries`/`evalDriftRequeues` counters, the pause hook, the §8.5 doc paragraph) from
   `da1b4641` — it was sound; only its predicate and its comparison granularity were wrong. Recover
   it from the reverted commit rather than rewriting it.
5. Gates: the standard set, **plus** `make verify-package-service-location` and the full
   `stack-gates` job — the unit tier demonstrably cannot see this class.

Fresh worktree per the convention; `da1b4641`/`b709a62c` are the recovery source, not a base.

### 13.10 Risks, residuals, and cross-design coupling

- **The classifier is now security-relevant.** An under-detection is a silent combination-grant.
  Mitigated by fail-closed-on-unknown (§13.3), by the census-reproduction test, and by the fact that
  the only realistic drift — an author simplifying a lens toward colocation — flips it in the safe
  direction. Accepted, and named as the design's sharpest edge.
- **Selector-scoping narrows what counts as drift.** By construction it only ignores edges the walk
  never read, so it cannot mask a change the evaluation depended on — but it does move the fallback
  question into the executor, where an unattributed read path must default coarse. The
  untyped-hop/variable-length fallbacks are load-bearing, not hygiene.
- **Cross-design coupling — engine `UNION`** ([shared-keyspace arbitration](refractor-shared-keyspace-arbitration-design.md)
  §3.2, currently 🚧 blocked). It introduces multi-branch RETURN, which the classifier must handle;
  §13.3 specifies branch-union-with-fail-closed so the two can land in either order. Flagged so
  whichever ships second does not discover it.
- **Cross-design coupling — the actor-enumeration gap** (`reportsTo`-inherited grants, filed by this
  design's own §10 and still open on the board). Fixing it *widens* `capabilityEphemeral`'s fan-out,
  which raises validation load on exactly the hot path §5 priced. Not a blocker in either direction;
  worth sequencing after this so the counters measure the wider fan-out honestly.
- **Unchanged residuals:** §4.5's interim exposure on the unanchored grant scans (Fire 2), and §3's
  cross-document `ReadAndMerge` blend. Neither is touched by this re-decision.

### 13.11 Adversarial pass

Run **in-fire and self-directed** (no independent reviewer was available to this fire — stated
plainly rather than implied). Four probes, all grounded in code, all folded above:

1. *"Does exempting row 4 open a real hole through its `roles` array?"* — probed
   `step3_auth_capability.go:280-284` and `capabilitykv/read.go:73`: `doc.Roles` is consumed **only**
   on the denial path for FR22 `actorRoles`, never conjoined with `platformPermissions` to
   authorize. The census verdict survives the probe. (Ledger, §13.2.)
2. *"Does narrowing the predicate actually fix the regression?"* — it fixes the **assertion**, not
   the **mechanism**. This probe produced §13.1's defect 2 and is the reason this section is not a
   one-line predicate change.
3. *"Is the classifier's exemption forgeable by an author?"* — an author can flip a lens to exempt
   only by making its entries single-binding, which is the colocation fix (§7 alternative 4). The
   safe direction. Conversely, nothing an author writes can *accidentally* exempt: the default is
   validate.
4. *"Does the anchor/key column need a special case?"* — no, and the probe is why the unit rule
   splits top-level scalars from collection entries instead of exempting a named column. A special
   case for the anchor would have been a rule to get wrong later.

## 14. Build note — Fire 1 Increment 2 (Lattice Steward, 2026-07-29, `c80bfa00`)

**Shipped, per §13.9's build order, in one fire:** the fan-in stress test (written first, confirmed
RED against the freshly-cherry-picked `da1b4641` seam alone — `capabilityRoles` drift-retried once
on its own legitimate churn, exactly defect 1); the §13.3 conjunct-unit classifier
(`projection.hasMultiBindingConjunctUnit`, pinned against the REAL shipped
`capabilityEphemeral`/`capabilityServiceAccess`/`staffReadGrants`/`capabilityRoles`/`capabilityRoleIndex`
specs — all five verdicts match the census); the §13.4 selector-scoped footprint (adjacency
`DirectionMatches` relocated + exported so both the executor's capture side and the pipeline's
validation side share one vocabulary mapping, never duplicated); and the recovered validation/
re-execute/requeue seam from `da1b4641` unchanged except for the two corrections. Stress test
confirmed GREEN with both fixes landed, reproduced 3× incl. `-race`.

**Gates:** `go build ./...`, `make vet`, `golangci-lint run ./internal/refractor/...` (cache
cleared), `STRICT=1 lint-conventions.go`, `go test ./internal/refractor/...`, full
`go test ./... -p 4` (113 packages, wide-blast-radius — touches `adjacency`/`ruleengine`), and
`make verify-package-service-location` against the shared stack — 67/67 assertions, all 10
`cap.roles.<operator>` grants "projected live" (corroborating; the package was already installed,
so the primary proof is the Go-tier stress test, not this run).

**Residual, honestly named:** the stress test drives `pipeline.Reproject` (the same synchronous
per-actor path the sweep/control-RPC/retry-queue use) rather than the live consumer pump —
`substrate.ConsumerSupervisor.Add` derives its pump context from `context.Background()`, not from
`Run`'s caller context, so a footprint-captured-hook attached to `Run`'s context never reaches a
live-pump-triggered evaluation. This is a pre-existing pump/hook wiring gap, not something this
fire introduced or needs to fix — `Reproject` reaches the identical `executeFullForActor`/
`footprintValid` code path, so the mechanism proof is unaffected — but a future test wanting to
exercise footprint validation through the live CDC pump specifically will hit the same wall.
