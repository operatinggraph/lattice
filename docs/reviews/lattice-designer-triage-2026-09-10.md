# Lattice designer triage — the two `📐 needs designer pass` rows (2026-09-10)

**2026-09-10, Winston (Andrew-directed session).** Mandate, verbatim: *"lattice backlog. Address all items
marked for designer pass."* Method: the [2026-08-27 pass](lattice-designer-triage-2026-08-27.md)'s and the
[verticals pass](verticals-designer-triage-2026-09-10.md)'s — every row's `no-pattern:` re-run against today's
tree, briefed to falsify; each candidate fix **run as a spike** in a detached scratch worktree against the
shipped census harnesses (never committed); the vendor premise read in the pinned server source
(`nats-server v2.14.0`, `go.mod:12`); one live read of the dev stack's `KV_core-kv` consumer report.

**Outcome: neither row needs the mechanism its `no-pattern:` names.** One dissolves into a **package edit
with an in-file precedent** (`📋 ready`, S). The other's filed mechanism has **zero payoff against the one
scenario the shipped invariant does not cover**; the structural close is a stale-evidence guard on the probe's
fail verdict, which reads state the substrate already stamps (`📋 ready`, S–M, re-sized ★). No architectural
fork, no frozen-contract change — the pass is Winston-adjudicated under the 2026-08-20 delegation (§4).

| Row | Filed `no-pattern:` | Verdict |
|---|---|---|
| [Refractor/edge-manifest] `edgeManifestReadGrants` strands `op` (§2) | per-walk anchor-variable scoping in `generateProducerSpec` | The two `op` walks belong to two **authorization bases**; move the own-task walk into its own `ReadGrantDomain` (the `domainProvider` precedent, same file) and the base producer indexes — spike-proven → **📋 ready · S · ★★** |
| [Loom] The deadline probe's evidence is shorter-lived than the wait it backstops (§3) | an armed/disarmed deadline fact on the instance record | A flag written at first probe cannot see a **late-minted** marker — the only scenario the `arm + window < TrackerTTL` invariant leaves open; the close is a **stale-evidence guard** (token epoch vs the evidence horizon) → **📋 ready · S–M · ★** |

## 1. The recurring cause, named once

Both rows were filed by a fire's **close pass** — by me, days apart — and each carried a claim the filer never
ran. The edge-manifest row said per-walk scoping was *"a real mechanism question rather than a rename"* because
`edgeCatalogTail` binds `op` and `role` by name; but the producer generator never emits the tail
([anchorwalk.go:548-580](../../internal/pkgmgr/anchorwalk.go)), so the tail's names are irrelevant to the
producer, and the *declaration* already has a partition axis (`GrantDomain`) that separates the two walks
without renaming anything. The Loom row was filed **after** its sibling design had already re-derived and
**declined** the identical `no-pattern:` (`loom-deadline-marker-provenance-design.md` §3 carrier 2, §11 row 4,
shipped `1982952e` three days earlier), and the filing's own caveat — *"held out of reach by
`MaxDeadlineArm`+marker window (≤ 2 h), not closed"* — names the bound that covers every **replay**; the one
delivery the bound does not cover is a marker **minted late**, which no flag written at first probe can see. A
row filed from a close pass gets the least scrutiny of anything on the board; brief its falsification as you
would a stranger's, and price the named mechanism against the one scenario the shipped bound leaves open.

## 2. [Refractor/edge-manifest] `edgeManifestReadGrants` strands `op`, bound over unrelated chains

**Filed (`1b9e7346`, the varlength Inc 2 close):** *"Three `edgeCatalog` walks anchor `op:meta` over different
chains and two land in the base producer, so a stage boundary strands a name no engine narrowing may re-admit.
The shared `edgeCatalogTail` binds `op`/`role` by name, so per-walk scoping is a mechanism, not a rename."* —
`no-pattern: per-walk anchor-variable scoping in generateProducerSpec`.

### 2.1 Grounding

- **The collision is real and the refusal is correct.** `domainBase` collects ten walks
  ([lenses.go:110-380](../../packages/edge-manifest/lenses.go)); two bind `op` to a `meta` over different
  chains — `(tpl)-[:permitsOperation]->(op:meta)` (`edgeCatalog` walk 0, `:133-141`) and
  `(task)-[:forOperation]->(op:meta)` (walk 2, `:151-159`). `generateProducerSpec` stages one `WITH … AS
  grantSliceN` per walk, so `op` is dropped at walk 0's boundary and re-bound at walk 2 over a different chain.
  `withScopeReject` refuses exactly that ([withscope.go:53](../../internal/refractor/ruleengine/full/withscope.go);
  the vector *"two walks binding one name over different relations are refused"*,
  [hopindex_test.go:725-748](../../internal/refractor/ruleengine/full/hopindex_test.go)) — merging the two
  occurrences would assert a hop no row walks, the under-approximation the auth plane must never take. The
  census pins the verdict: `"edgeManifestReadGrants": hopWithDropped`
  ([anchor_hopindex_corpus_census_test.go:139-150](../../internal/refractor/anchor_hopindex_corpus_census_test.go)).
- **The row's mechanism claim is false.** The generator renders each walk's clauses plus a `collect(…)` of the
  walk's `AnchorVar` ([anchorwalk.go:548-580](../../internal/pkgmgr/anchorwalk.go)) and **never the lens tail**;
  `edgeCatalogTail`'s `WITH op, role` lives only in the data lens (`composeDataLensSpec`, `:470-485`). What
  makes a *declaration* rename impossible is `parseWalks`' rule that every walk of one lens shares its
  `AnchorVar` (`:300-306`) — true, and beside the point: the collision is between two walks of one **domain**,
  and the domain is a declared partition of the producer (`ReadGrantDomainSpec`, `:49-62`: *"the §6.14 grouping
  and blast-radius unit … splitting reachability paths that not every actor has into their own domain"*).
- **The demand is measured, not hypothetical.** The base producer is the larger of the two `cap-read` auth-plane
  producers the BFS fallback re-executes per actor: ≈4 s per event with a 5.9k-event replay backlog
  ([authority-walk-wall-unit-cost-design.md §2.4](../../_bmad-output/implementation-artifacts/authority-walk-wall-unit-cost-design.md)),
  a 106 k backlog at the personal-lens cost fire's read
  ([personal-lens-whole-actor-cost-design.md §4.4](../../_bmad-output/implementation-artifacts/personal-lens-whole-actor-cost-design.md)),
  and a 41,963-document rebuild draining at 0.235/s — ~50 h
  ([capability-plane-rebuild-throughput-design.md](../../_bmad-output/implementation-artifacts/capability-plane-rebuild-throughput-design.md)).
  Today's live read (`nats consumer report KV_core-kv`, 139 consumers): every consumer at 0 unprocessed — a quiet
  host with no user traffic, which is a floor, not a refutation of the loaded-day figures.
- **The D1 reader unions every domain**, so a new slice changes no effective grant: `IsReadable` lists
  `cap-read.*.<actorSuffix>.<anchorId>` with the domain as a wildcard
  ([capabilityread.go:56-62](../../internal/refractor/capabilityread/capabilityread.go)); `IsRelevant` reads the
  row's `anchorType` audit field, never the domain
  ([interest.go:353](../../internal/refractor/personalinterest/interest.go)). The package's own test file states
  the doctrine: *"§6.14 unions slices, so a new slice costs nothing, while folding its branches into the base
  producer would multiply that lens's existing cross-product fan-out"*
  ([package_test.go:56-64](../../packages/edge-manifest/package_test.go)).
- **The own-task path is a distinct authorization basis.** `edgeCatalogTail`'s comment says so
  ([lenses.go:600-618](../../packages/edge-manifest/lenses.go)): a task-scoped submission *"is authorized
  independently of any standing role or service reachability — the actor's `cap.ephemeral.*` task grant, not a
  `cap.roles.*` permission"*. The residence chain and the held-role chain each have a domain; the task chain was
  lumped into base by accident of the fold (refractor-shared-keyspace-arbitration-design.md §13.7 (c)).

### 2.2 The spike — two candidate fixes, run

Detached worktrees at HEAD `d600377e`, the census harnesses run as-is (only the one moved pin patched so the
loop continues):

| | **A — domain split** (package) | **B — per-stage rename in the generator** (platform) |
|---|---|---|
| Edit | `domainTask = "edgeManifestTask"` + `ReadGrantDomains()` entry + walk 2's `GrantDomain` (3 lines, `lenses.go`) | `generateProducerSpec` renames every walk-bound name `<v>_w<N>` via the existing `render(rename)` (12 lines, `anchorwalk.go`) |
| `edgeManifestReadGrants` hop index | **`hopIndexed`** (was `hopWithDropped`) | **`hopIndexed`** |
| one-key verdict (`actor_onekey`) | `walk: actor type binds off-anchor` — the staff producer's own verdict, the benign direction | same |
| `ActorTypeBindsAnchorOnly` T8 pin (`pipeline`) | **passes** | **fails** — `edgeManifestStaffReadGrants`'s `identity` positions 3 → 5: renaming un-merges the re-opened chains the Inc 2 narrowing merged, so the index graph gains duplicate unlabeled positions and the derivation seeds each |
| `ruleengine/full` | 6 enumeration pins list the three producers by name | green |
| `pkgmgr` | green | 2 pins (the golden emission, `CollidingWalkVariablesAreStagedApart`) |
| `packages/edge-manifest` | 3 pins (19 → 20 lenses, `manifest.yaml`, the namespaced-keys map) | green |
| `lint-cap-read-producers` | 0 issues | 0 issues |
| Refractor census pins moved (lower bound; each harness stops at its first) | 8, all *"a new lens needs a verdict"* or *"the base producer's binding set shrank"* | 3, plus digest churn in every producer's variable names |

Both convert the base producer. B changes the shape of every generated producer's index graph — measured
on the staff producer, whose T8 pin exists precisely so a cypher change cannot re-arm a second mechanism
silently ([varlength design §6.4](../../_bmad-output/implementation-artifacts/varlength-anchor-derivation-design.md))
— and pays it on the platform side for a single consumer. A changes one package's partition.

### 2.3 Alternatives

| # | Alternative | Verdict |
|---|---|---|
| 1 | **Do not have this thing** — the BFS fallback is sound; leave the row | Rejected. "Slower, never wrong" is the posture the varlength design already rejected on this producer's measured cost (§2.1); the fix below is three lines |
| 2 | **A — a fourth `ReadGrantDomain` for the own-task walk** | **Adopted.** Precedent in the same file (`domainProvider`, persona-worlds Fire W0); the split states a true authorization boundary; the effective grant set is unchanged (union); task events now re-execute a one-walk producer instead of the ten-walk base |
| 3 | **B — rename every walk variable per stage** | Rejected: un-merges the narrowing's merged positions on every producer (staff `identity` positions 3 → 5), churns every producer's pins, and fixes a class with one member |
| 4 | **C — rename only the `AnchorVar` per stage** | Not adopted; **the recorded class fix.** Six lines in `generateProducerSpec`; adds no position (each anchor is already its own) and states a truth (each stage's anchor is that stage's binding). Revive trigger: a second package's generated producer pinned `hopWithDropped` on an anchor name bound over different chains |
| 5 | Widen the engine's narrowing to admit the two `op` occurrences | Refused on soundness — they are two bindings; merging asserts a hop no row walks (§2.1) |
| 6 | Group the two subset-related base walks (`edgeServices` ⊂ `edgeCatalog#0`) | Orthogonal cost item, already sequenced by the varlength design §13 behind a measured trigger; unchanged |

### 2.4 Verdict — `📋 ready · S · ★★`: the own-task op walk gets its own read-grant domain

**The increment (package, Steward-built, review depth per `agents/steward/SKILL.md` §4 — a `cap-read`
producer partition is on the auth plane; the adversarial layer applies):**

1. `packages/edge-manifest/lenses.go`: `domainTask = "edgeManifestTask"`; `ReadGrantDomains()` gains
   `{Name: domainTask}`; `edgeCatalog` walk 2 (`(identity)<-[:assignedTo]-(task:task)`,
   `(task)-[:forOperation]->(op:meta)`) declares `GrantDomain: domainTask`. `manifest.yaml` + the `Version`
   bump + the `Description`'s "three generated producers" sentence (`package.go:26-27`);
   `DIFF_BASE=<base> go run ./scripts/lint-package-version.go`.
2. Pins that move, each with its argument in the commit message (a row moving **to** indexable is the
   direction the harness says needs one): `anchor_hopindex_corpus_census_test.go` (base → `hopIndexed`; new
   `edgeManifestTaskReadGrants` → `hopIndexed`), `actor_onekey_corpus_census_test.go` (base →
   `walkMultiPosition`), `actor_walk_scope_corpus_census_test.go`, `branch_decomposition_corpus_census_pins_test.go`
   (base loses `g1/o2[op,task]`; the new producer joins the decomposing population),
   `auth_plane_narrowing_census_test.go`, `grouping_reduction_corpus_census_test.go`,
   `label_derivation_corpus_census_test.go` (new: `identity meta task`, exhaustive), `footprint_classifier_test.go`;
   `ruleengine/full`'s three producer-name lists (`branch_decomposition_equivalence_test.go:1195`,
   `grouping_equivalence_test.go:356`, `grouping_producer_gate_test.go:108`); `packages/edge-manifest/package_test.go`
   (`TestPackage_NineteenLenses` → twenty, `readGrantLensNames`).
3. **Coverage proof for the new domain** (owned here): `coverage_proof_test.go`'s resident world proves
   `edgeCatalog` branch 0 only (`:171-188`); add a task-world case proving branch 2's anchors are covered by
   `edgeManifestTaskReadGrants` — a task `assignedTo` the actor with a `forOperation` link, the op meta in the
   task slice and **not** in the base slice.
4. `docs/components/edge-manifest.md`: the domain tables (`:55-100`) gain the task domain and its producer row.
5. Phase 0 re-runs §2.5's censuses; a disagreement is a scope change.

**Non-goals (the drift fence):** no generator change (C is recorded, not built); no change to `edgeCatalog`'s
data-lens cypher or the tail; no touch to the Inc 2 narrowing, which keeps its only corpus consumer
(`edgeManifestStaffReadGrants`) and its 41-vector pin.

### 2.5 Executable censuses

```
# the collision, today (expect: hopWithDropped pinned, and the two `op` chains in domainBase)
grep -n '"edgeManifestReadGrants"' internal/refractor/anchor_hopindex_corpus_census_test.go   # → hopWithDropped
grep -n 'AnchorVar:   "op"' -B2 -A6 packages/edge-manifest/lenses.go | grep -c 'domainBase'   # → 2
# the reader unions domains (expect: the wildcard form)
grep -n 'KeyPrefix + "\*\."' internal/refractor/capabilityread/capabilityread.go               # → 1 hit
# after the edit (expect: base and task producers both "" = indexed)
go test ./internal/refractor/ -run TestCorpusAnchorHopIndex_PinnedConjuncts -count=1
```

## 3. [Loom] The deadline probe's evidence is shorter-lived than the wait it backstops

**Filed (`cefa0390`, the marker-TTL fire's close):** *"The probe judges rejected-or-lost from the 24 h
tracker's absence while the wait is unbounded, so a `deadline.<id>` marker delivered past 24 h fails a healthy
parked instance."* — `no-pattern: an armed/disarmed deadline fact on the instance record · held out of reach by
MaxDeadlineArm+marker window (≤ 2 h), not closed`.

**Already declined once.** The provenance design re-derived this `no-pattern:` and rejected it
([§3 carrier 2, §11 row 4](../../_bmad-output/implementation-artifacts/loom-deadline-marker-provenance-design.md)):
*"cannot tell a replayed removal from an expiry when the instance is on a later armed step … a field on 12,351
records; needs record and key to agree across `rearmDeadline`"*. Its first objection is discharged by its own Inc 1
(removals are acked on provenance, never probed); the row was re-filed regardless, three days after that design
shipped, by a fire whose §3.2 chose its window *"precisely so that raising it cannot reach this gap"*.

### 3.1 Grounding — what the invariant proves, and the one delivery it cannot see

- **The shipped probe** ([engine.go:1310-1471](../../internal/loom/engine.go)): acts only on the server's
  `Nats-Marker-Reason: MaxAge` marker (provenance), only when `deadline.<id>` is absent (currency), and its
  `fail` is revision-conditioned (the write window). It reads rejected-or-lost from two absences — the
  `lattice.op.status` tracker (`processor.TrackerTTL = 24h`, [tracker.go:17-19](../../internal/processor/tracker.go))
  and the outbox record — in all three step kinds (`:1471`, `:1525`, `:1595`).
- **The invariant** `MaxDeadlineArm + LoomStateMarkerTTL < TrackerTTL` (1 h + 1 h < 24 h, 10× margin) is pinned
  by [platform_buckets_test.go:71-100](../../internal/bootstrap/platform_buckets_test.go), and `withDefaults`
  clamps both arms to the ceiling (`engine.go:195-209`). It proves: **a marker minted on time is consumed while
  the tracker lives**, replays included — a rebuilt `DeliverAll` durable replays only markers still in the
  stream, at most one window old ([platform-bucket-marker-ttl-design.md §5](../../_bmad-output/implementation-artifacts/platform-bucket-marker-ttl-design.md)).
- **What it cannot see: a marker minted late.** The marker is written when the server *processes* the expiry,
  not when the TTL falls due. In the pinned server, with subject-delete markers configured,
  `expireMsgsOnRecover` returns before expiring anything (`filestore.go:2521-2523`: *"If subject delete markers is
  configured, can't expire on recover"*), `recoverTTLState` rebuilds the timed hash wheel and schedules the
  age check at once (`:2148-2173`, `defer fs.resetAgeChk(0)`), and `expireMsgs` then drains every past-due
  entry, emitting a `MaxAge` marker for each subject it empties (`:6790-6905`, `handleRemovalOrSdm`). So after a
  server outage — or a host clock jump, which the live path (`ats.AccessTime()`) handles the same way — every
  past-due per-message TTL is processed **in the same pass, on the same clock**, in `KV_loom-state` and
  `KV_core-kv` alike. If the outage exceeds the tracker's remaining life, the deadline marker is minted and the
  tracker removed within milliseconds of each other; Loom's durable delivers the marker; the probe finds no
  tracker, no outbox ⇒ **`fail`** of an instance whose creation committed and whose human task is live.
- **The population.** Instances whose arm was live at outage start (≤ `CreateTaskTimeout`/`StepTimeout`, 60 s by
  default, ≤ 1 h clamped) and whose op had committed — plus any marker minted but unconsumed at outage start
  that the durable delivers before the stream expires it. Small, DR-scale (an outage longer than ~23 h), and
  nobody has observed one. But its outcome is a **false terminal with a misleading reason** (*"step N CreateTask
  rejected"*) on an instance whose task the human can still complete; the completion then lands on a terminal
  instance and is dropped. Recovery is `lattice loom redrive`, if anyone notices.
- **The filed mechanism retires none of it.** An armed/disarmed fact is written when Loom *learns* the creation
  committed, which is at the first probe (`onUserTaskDeadline` case 1, `:1497-1508`) — Loom consumes no
  task-created event (its consumers: the trigger, the completion domains, the outbox relay, the deadline;
  `engine.go:370-416`). In the late-minted case the first probe **is** the late one: no flag exists to read.
  The flag would short-circuit replays — which the invariant already bounds — and nothing else. Payoff: zero.
- **What the probe would need is the age of its own evidence.** The substrate stamps every KV entry
  ([`KVEntry.Timestamp`](../../internal/substrate/kv.go), `entry.Created()`, `kv.go:18-54`), and the pending
  step's token pointer `token.<pendingToken>` is written create-only in the step's own transition batch
  ([state.go:452-530](../../internal/loom/state.go)) and re-put by `redrive` (`tokenPutUnderRedriveCAS`,
  `:376-387`) — its timestamp is the **epoch of the current step**, untouched by re-arms (`rearmDeadline` writes
  only the deadline key, `:610-620`) and by any note the probe itself writes on the record. The horizon the
  evidence lives for is the op-status surface's own: `opstatus` is a leaf package (deps: `substrate` only;
  importers: `cmd/processor`, `cmd/lattice`, `bridge`, `gateway`, `loom`), the natural single home for the
  constant the Processor writes with and the probe compares against.

### 3.2 The shape — a stale-evidence guard on the fail verdict

**Rule.** *Absence is evidence only inside the evidence's lifetime.* When the probe reaches "tracker absent and
outbox absent", it reads the token pointer's timestamp; if `now − epoch ≥ opstatus.TrackerTTL`, the verdict is
**inconclusive**: Warn (the §10.6 alert carrier Loom already uses for "advanced and alerted",
`engine.go:1452`), a CAS-written `deadlineProbe` note on the record, Ack — the instance stays running with its
token. Otherwise the verdict is `fail`, exactly as today. The pattern-pin-missing `fail` stays unguarded (an
invariant break is evidence in itself).

**State table — every wake-up × every epoch age** (states as the provenance design §5 defines them):

| Wake-up | Epoch age | Tracker | Outbox | Today | After |
|---|---|---|---|---|---|
| on-time `MaxAge` (≤ arm + window ≤ 2 h) | < 24 h | present | — | committed → no-op / advance | same |
| on-time `MaxAge` | < 24 h | absent | present | re-arm | same |
| on-time `MaxAge` | < 24 h | absent | absent | **fail** (correct) | same |
| replayed `MaxAge` (rebuilt durable, ≤ 1 h old) | < 24 h | present | — | no-op | same |
| **late-minted `MaxAge`** (server outage / clock jump > tracker life) | **≥ 24 h** | absent (aged out) | absent | **false fail** — the row | **inconclusive**: Warn + note + Ack |
| late-minted `MaxAge`, op genuinely rejected before the outage | ≥ 24 h | absent (never written) | absent | fail (correct by luck) | inconclusive — the evidence cannot distinguish the two; the alert names both readings |
| re-arm chain > 24 h (relay down a day), then rejected | ≥ 24 h | absent | absent | fail | inconclusive — stated, accepted: the day-long relay outage is the loud fault |
| redriven step, late marker | reset by the re-put | as above | | | epoch restarts at redrive; a redrive re-submits, so the tracker is fresh |
| two replicas, late-minted | ≥ 24 h | absent | absent | A fails, B's CAS refused | A notes, B's CAS refused ⇒ Ack |
| redelivered marker after a note | ≥ 24 h | absent | absent | — | inconclusive again; note re-written at the new revision (idempotent, bounded by the window) |
| never-written token pointer for a running instance | — | | | (unreachable: same batch) | `probeFail("token pointer missing")` — an invariant break, mirrors the pin-missing arm |

**Lifetime of the note (`Instance.DeadlineProbe *probeNote{At, Reason}`):** created by the inconclusive
verdict (record-only CAS put at the probe's revision — **not** `transition`, whose no-TTL branch purges the
already-expired deadline key and would mint a stray marker); reset by `transition` whenever a new token is
written or the status leaves running (`advance`, `fail`) and by `RedriveInstance`; carried across restart on the
record; ordered by the CAS; surfaced by `InspectInstance` (`InstanceSummary`, [control.go:10-30](../../internal/loom/control.go),
which copies fields and must copy this one). An older binary ignores and drops it on its next write — a
downgrade clears a note, acceptable.

**Consumer table.**

| Reader | Change |
|---|---|
| `onDeadline` / `onUserTaskDeadline` / `onExternalTaskDeadline` (`engine.go:1471`, `:1525`, `:1595`) | the three rejected-or-lost `probeFail` calls route through one helper that reads the epoch and branches |
| `stateStore` | `tokenEpoch(ctx, token) (time.Time, error)` (one `KVGet` on the fail branch only); `noteDeadlineProbe(ctx, inst, revision)` |
| `transition` (`state.go:452`), `RedriveInstance` (`control.go`) | clear the note |
| `InstanceSummary` / Loupe's `#/flows` detail | carries the note (rendering is a Loupe follow-on, not this fire) |
| `processor.TrackerTTL` (`tracker.go:19`) | becomes an alias of `opstatus.TrackerTTL`; `bootstrap`'s invariant test reads it unchanged |
| `MaxDeadlineArm`'s comment (`engine.go:64-78`), `TestLoomStateMarkerTTL_FitsInsideTheTrackerLifetime`'s (`platform_buckets_test.go:87-91`), `docs/components/loom.md:351,363,587` | the *"past it, the probe fails a healthy instance"* / *"structural fix §11.2"* sentences are rewritten to the guard |

### 3.3 Alternatives

| # | Alternative | Verdict |
|---|---|---|
| 1 | **Do not build; document the DR residual + runbook** (after a day-long outage, redrive instances failed with the rejected reason at resume time) | Rejected, narrowly. The harm is a false terminal with a misleading reason on the orchestration plane, self-healing never, and every design in this lineage plus two code comments point at "the structural fix"; the guard is ~100 lines on existing seams. Imp ★ says how rare it is |
| 2 | **The row's mechanism** — an armed/disarmed fact on the record | Rejected (§3.1): written at first probe, it cannot see the late-minted first delivery; retires nothing the invariant does not |
| 3 | **RPC-side verdict** — the request carries `asOf`, the reply answers `beyondHorizon` | Rejected for now: one consumer. The bridge's `resultAlreadyLanded` ([dispatch.go:373-400](../../internal/bridge/dispatch.go)) also reads this surface, but its "not found past 24 h ⇒ re-dispatch" **is** Contract #4 §4.3's contracted horizon, not a hazard. Revive trigger: a second consumer that reads absence as a terminal verdict |
| 4 | **Raise `TrackerTTL`** to outlive any outage | Rejected — Contract #4 §4.3 fixes 24 h platform-wide (the provenance design's row 3) |
| 5 | **Loom `Config` field wired from `bootstrap`/`processor`** | Rejected: `loom` imports neither (`go list -deps`), and a duplicated constant enforced by a three-constant test is the Loom dossier's own entry; `opstatus` is the leaf both already import |
| 6 | **Epoch from the instance record's timestamp** (+ re-arm through `transition` so re-arms refresh it) | Rejected: the note write would refresh the same timestamp and a redelivery would then read a fresh epoch and **fail**; the token pointer is the step's epoch by construction |
| 7 | **A TTL'd Health-KV key per inconclusive instance** (the auth-trace shape) | Rejected: a standing fact needs a lifecycle, and clearing it at every transition is an unconditional purge on a usually-absent subject — the marker-minting hazard `deleteToken` documents (`state.go:631-645`) |
| 8 | **Nak / re-arm instead of alert** | Rejected: a Nak asks for evidence that cannot appear; a re-arm loops every arm with the same verdict |

### 3.4 Contract surface — builds to Contract #10 §10.6, no edit

§10.6's failure-detection clause ([10-orchestration-loom.md:182-195](../../docs/contracts/10-orchestration-loom.md)):
*"the engine then distinguishes, **by evidence**, and (a)… (b)… (c) a genuinely rejected/lost op fails the
instance … with an alert — never a silent wedge"*; *"A rejected or lost creation fails the instance with an
alert instead of parking forever"*. The guard makes the first sentence true past the evidence's lifetime — the
engine refuses to *distinguish* without evidence — and keeps the last: the inconclusive path is alerted, not
silent, and the operator verb (`redrive`) is the same one a false fail needs today. **The one consequence a
reader could observe:** in the day-long-outage scenario only, a creation that was genuinely rejected before the
outage is alerted-and-parked rather than failed. Today that same delivery fails healthy instances too; no
runtime can tell the two apart once the tracker is gone, and the contract nowhere promises a verdict without
evidence. Stated for Andrew's one look (§4); no `docs/contracts/*` edit.

### 3.5 Verdict — `📋 ready · S–M · ★`: the probe refuses to read absence as rejection past the evidence horizon

**One increment (Steward-built; posture-changing on the orchestration plane → the adversarial layer):**
`opstatus.TrackerTTL` + the `processor` alias; `tokenEpoch` / `noteDeadlineProbe` in `state.go`; the shared
rejected-or-lost helper in `engine.go` with the note field, its two clearing sites and the `InstanceSummary`
copy; the four comment/doc rewrites in §3.2's last row; a `docs/components/loom.md` dossier entry.

**Tests, each owned here:** (T1) an epoch older than the horizon ⇒ inconclusive — Warn, note at the read
revision, Ack, instance running with its token — driven by an **injected clock** on the engine, never a sleep
and never a backdated entry (the server stamps it); (T2) **mutation:** delete the comparison ⇒ T1 reds; (T3) a
fresh epoch ⇒ `fail` — the four shipped e2es with real 2 s expiries pass unchanged; (T4) the note is cleared by
advance, by fail, by redrive (three rows); (T5) `InspectInstance` carries it; (T6) two replicas: the second CAS
is refused ⇒ Ack (the provenance design's race-test shape); (T7) a redelivered marker after a note re-notes at
the new revision and fails nothing; (T8) the vendor premise: on the pinned server with markers configured, a
per-message TTL past due at recovery is expired **with** a `MaxAge` marker on the next age check — as a case of
the substrate's marker-provenance fixture if `natsfixture` can restart a file-backed server, else the
`filestore.go` cites above stand as the pin and the fixture gap is filed.

**Gates:** the standard set (`CLAUDE.md`); `internal/loom`, `internal/opstatus`, `internal/processor`,
`internal/bootstrap` under `go test`; `make test-lease-convergence` (Loom's tagged harness); no `packages/`
edit. **Live close:** cycle Loom (`pkill -x loom` + `make orchestration`), confirm `loom-deadline` at 0 pending
and the heartbeat healthy; the late-minted path has no live population to exercise and is pinned by T1/T8.

### 3.6 Executable censuses

```
# the invariant and both bounds (expect: 1h, 1h, 24h; the test present)
grep -n 'MaxDeadlineArm = \|LoomStateMarkerTTL = \|TrackerTTL = ' internal/loom/engine.go internal/bootstrap/platform_buckets.go internal/processor/tracker.go
grep -n 'func TestLoomStateMarkerTTL_FitsInsideTheTrackerLifetime' internal/bootstrap/platform_buckets_test.go
# the three unguarded rejected-or-lost verdicts (expect: 3 probeFail calls carrying "rejected")
grep -n 'probeFail(ctx, inst, .*rejected' internal/loom/engine.go
# opstatus is a leaf and loom already imports it (expect: substrate only; loom in the importer list)
go list -deps ./internal/opstatus | grep 'lattice/internal/' ; grep -rl '"github.com/operatinggraph/lattice/internal/opstatus"' internal/loom
# the vendor premise (pinned source; expect the early return under SubjectDeleteMarkerTTL > 0)
sed -n 2515,2523p $(go env GOMODCACHE)/github.com/nats-io/nats-server/v2@v2.14.0/server/filestore.go
```

## 4. Adjudication

Per the 2026-08-20 delegation both verdicts are Winston-adjudicated: no architectural fork (a package partition
with an in-file precedent; a guard inside Loom's own probe reading a substrate stamp), no frozen-contract change
(§2 touches no contract; §3 builds to §10.6, with its one observable consequence stated in §3.4 for Andrew's
one look). Board rows rewritten in this commit; `STRICT=1 go run ./scripts/lint-board.go` green. The Steward
builds §2 and §3 as `📋 ready` units (S and S–M; §3 with the adversarial layer).

## 5. Falsification record

| Claim | How it was tested | Verdict |
|---|---|---|
| E1 — the producer generator never emits the lens tail, so `edgeCatalogTail`'s names cannot make the fix "a mechanism" | `generateProducerSpec` read end to end; `render(nil)` + `collectBranch` only | **holds** — the row's premise was false |
| E2 — a domain split converts the base producer under the shipped engine | spike A, `TestCorpusAnchorHopIndex_PinnedConjuncts` + eight sibling harnesses + `ruleengine/full`, `pipeline`, `projection`, `pkgmgr`, `packages/edge-manifest`, `lint-cap-read-producers` | **holds**; only enumeration pins move |
| E3 — a generator rename is the cleaner platform fix | spike B, same harnesses | **falsified**: T8 reds on the staff producer (positions 3 → 5); the narrowing's merge is what the rename undoes |
| E4 — the D1 reader unions domains | `capabilityread.go:62` wildcard; `IsRelevant` signature | **holds** |
| L1 — the shipped invariant covers every replay | `platform_buckets_test.go`, marker-TTL design §5, provenance design §5 | **holds** |
| L2 — the invariant covers a late-minted marker | pinned `filestore.go` (`expireMsgsOnRecover`, `recoverTTLState`, `expireMsgs`) | **falsified** — the residual is real, DR-scale, in the vendor's own code |
| L3 — the filed armed/disarmed fact closes the residual | Loom's consumer set (`engine.go:370-416`); the first-probe write site | **falsified** — payoff zero |
| L4 — the bridge is a second consumer with the same hazard | `dispatch.go:373-400` + Contract #4 §4.3 | **falsified** — its horizon is the contracted dedup horizon |
| L5 — an epoch exists without new state | `KVEntry.Timestamp`; the token pointer's write modes | **holds** |
| L6 — `loom` can read the horizon without a cycle or a duplicate | `go list -deps` on `loom`, `bootstrap`, `opstatus`; the importer list | **holds** via `opstatus` (processor aliases) |

Net: two rows filed by my own close passes, both with a claim I had not run. One dissolved to a package edit
the file already had a precedent for; the other's named mechanism would have shipped a field on every instance
record that closes nothing, while the actual residual sat one function away in the vendor's recovery path.
