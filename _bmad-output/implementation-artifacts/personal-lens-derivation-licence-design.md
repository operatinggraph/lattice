# The personal plane earns the pattern-directed derivation — three refusals, one licence

**Status: ✅ RATIFIED — Andrew, 2026-09-01.** One condition folded at ratification and now part of the
design: the licence must **revoke itself** when Refractor becomes multi-instance rather than relying on
prose (§4.4 conjunct 5 + the build-time gate, §9 R4). Author: Winston (Designer fire, 2026-09-01).
**§13 adversarial pass ✅ RUN + findings FOLDED** (3 blocking, 8 major, 6 minor; none deferred). No pre-build
gate is left open — **the Steward may build this now.**
**Component:** Refractor — `internal/refractor/{pipeline,projection,grantchange,control,personalinterest}` + `cmd/refractor` wiring.
**Backlog row:** [lattice.md](../planning-artifacts/backlog/lattice.md) → *Edge & personal lenses* →
*[Refractor] Derivation licence for personal lenses — clear §4.4's two out-of-pattern inputs* (★★★, M).
**Extends:** [auth-plane-projection-latency-design.md](auth-plane-projection-latency-design.md) §4.4 / §4.7 / §17.2
(the refusal), [personal-lens-grant-change-trigger-design.md](personal-lens-grant-change-trigger-design.md)
(the D1 edge + the PersonalSweeper), [refractor-hub-walk-and-periodic-load-design.md](refractor-hub-walk-and-periodic-load-design.md)
§5.1/§8 (the walk scope, and the residual that filed this row).
**Frozen-contract change: NONE.** See §7.

---

## For Andrew (one-look ratification block)

**What it does (two lines).** Refractor's pattern-directed anchor derivation — the mechanism that replaced an
undirected BFS with an exact "which actors did this event move?" walk — refuses every **personal lens** on the
grounds that a personal row also depends on two inputs its pattern does not bind (the D1 read gate and the
Interest Set). One of those inputs has since been given a real change edge; the other has not. This design
builds the missing edge, replaces the refusal with a **named licence** that states what it actually requires,
and removes the second, silent refusal sitting behind it (a multi-walk personal lens is handed **no hop index
at all**) — which is where the 128k-message backlog actually lives.

**No architectural fork. No frozen-contract change.** Three calls I made that deserve your eye:

1. **I did not set `patternClosedOutput = true` for personal lenses.** That flag is a factual claim — *"this
   lens's row is a function solely of its pattern-bound subgraph"* — and for a personal lens it is simply
   false. Setting it would also silently arm a **second** consumer (the delivery-side consumer-filter
   narrowing, `rulestate.go:312`), whose rollback is a consumer reset rather than an env knob. The licence is
   a separate, named predicate read only by the derivation gate.
2. **The row's stated premise was wrong, and I corrected it rather than building on it.** The hub-walk build
   note asserts *"both [inputs] now have their own change edges (the grant-change edge + PersonalSweeper;
   **hydration for the Interest Set**)"*. Hydration is a **device-initiated pull on attach**; the one client
   API that changes interest (`sync.go:516-520` `UpdateInterest`) documents that it deliberately does **not**
   hydrate. The Interest Set has no edge at all today — its only coverage is the 60 s/5-identity
   `PersonalSweeper` (~33 h per cycle at 10k identities) plus the very BFS accident this narrowing removes.
   So the licence is not available on today's tree; §4.2 builds the edge that makes it available.
3. **The licence reads a LIVE healer VERDICT, not an install-time boolean and not a progress stamp.**
   `personalPlaneHealer` already exists as a `bool` set at registration. The plain-lens licence learned this
   the hard way — enrolment alone is not enough, you must read live suppression/staleness
   (`anchor_derivation_plain.go:203-214`, `:315-320`). The adversarial pass (§13) broke my first draft of this
   conjunct, which read a *progress* stamp: the sweeper advances it every 60 s even while every reprojection
   in the batch is failing. It is now a pass verdict, on the plain licence's own terms.
4. **The producer set gets a gate, because it is open by construction.** The D1 gate answers by a **wildcard
   listing** over `cap-read.*` (`capabilityread.go:108`) — "package names are not enumerable statically", says
   its own doc — so "every producer announces" is a standing claim no runtime conjunct can make. §4.3b closes
   the set at install instead: a lens writing a `cap-read.` key that is not a sink-armed read-grant producer
   is **refused**, which is what makes conjunct 1 mean anything.

**The judgement, and how it was resolved (2026-09-01).** This narrowing trades an *accidental* reprojection
for a *mechanised* one on the Edge/personal plane — user-visible convergence latency for a device — and §4.2
exists precisely so the trade is edge-for-edge rather than edge-for-sweeper. The open question put to Andrew
was whether to narrow the personal plane at all before HA/multi-instance Refractor lands, given the
grant-change edge is **in-process only**.

**Resolved: narrow now, and make the licence revoke itself at the transition.** His question — *"will some of
this need redoing when Refractor runs multiple instances?"* — exposed that my conjunct 1 tested *"a reprojector
is wired in this process"*, which stays **true on every instance** while the edge stops spanning the
deployment: a fail-open at exactly the moment the premise expires. Shelving the design was the wrong answer
because the payoff is real today and most of it is transport-independent (below); the right answer was to stop
narrating the hazard in a risk row and enforce it. Hence §4.4's conjunct 5 and the build-time gate.

**What survives multi-instance, and what does not** — the reason "shelve until HA" was not the answer:

| Increment | Delivers the performance win? | At multi-instance |
|---|---|---|
| 1a shred announces · 1b Interest Set edge | no — safety preconditions | **re-plumbed, not redesigned**: the decisions survive, the sink under them changes |
| 1c/1d the two gates | no | **survive untouched** — transport-independent |
| 2 the licence | yes — 12 single-walk personal lenses | machinery survives; conjuncts 1 and 3 are re-derived, and conjunct 5 turns the whole thing off until they are |
| 3 the multi-walk union | yes — `edgeCatalog` **128 k**, `edgeTasks`, `edgeEntitySessions` | **entirely unaffected** — per-process computation over shared adjacency |

The largest single win is multi-instance-neutral, and nothing is thrown away. Note also that the debt is
**pre-existing**: the in-process edge shipped 2026-08-14 and HA already owed it a durable-signal replacement.
What this design changes is that the edge stops being an improvement and becomes load-bearing for a
narrowing — which is exactly why conjunct 5 exists.

---

## 1. Problem + intent

### 1.1 What was measured

`refractor-hub-walk-and-periodic-load-design.md` §1 (live stack, 2026-09-01) found 24 lens consumers with
`Ack Pending = 1` and 10k–128k unprocessed messages, each spending **20 s – 190 s per message** inside the
relation-blind actor walk. §5.1 shipped a pattern-*scoped* walk that drained most of them. Its §8 residual
names what did not drain, and why:

| Stuck lens | Backlog | Named closer in §8 |
|---|---|---|
| `edgeCatalog` | 128 k | *"the pattern genuinely crosses a descriptor / same-label hub — only the pattern-directed derivation removes it; personal lenses are refused it by §4.4"* |
| `edgeInstances` | 75 k (3 msg/min) | same |
| `objectLiveness` / `objectAttachments` | 40 k each | *"same 📐 row's territory"* |
| `edgeManifest{,Staff}ReadGrants` | 90 k / 57 k | the varlength design's Inc 2 (own row, 📋 ready) |

**Two of those attributions are wrong, and this design corrects them** (§3 census):

- `objectLiveness` / `objectAttachments` are **not personal lenses**. They are `actorAggregate` plain lenses
  (`packages/objects-base/lenses.go:32`, `:48`) writing `weaver-targets`, for which
  `projection/driver.go:502` **does** set `patternClosedOutput = true`. They are refused on query shape —
  `"pattern carries an untyped relationship"` (`hopindex.go:724`, from `OPTIONAL MATCH (o)-[r]->(owner)`) —
  a refusal this licence does not touch and cannot touch. Their closer is a different row.
- `edgeCatalog` is refused by the personal exclusion **and, independently, by being multi-walk**. Lifting only
  the exclusion delivers it nothing (§3.2). That is where the headline 128 k lives.

### 1.2 The refusal, stated exactly

`derivationIndexForAct` (`anchor_derivation_mode.go:233-250`) is the gate; it adds two act-only conjuncts on
top of `derivationIndex`'s three:

| # | Conjunct | file:line | Refuses |
|---|---|---|---|
| 1 | `patternClosedOutput` | `anchor_derivation_mode.go:240` | **all 15 personal lenses** |
| 2 | `sweeper != nil` | `anchor_derivation_mode.go:246` | **all 15 personal lenses** (structural — `pipeline.go:524-527`: *"A Personal Lens never receives a SweepPlan"*) |
| 3 | `actorEnumerator != nil` | `anchor_derivation.go:138` | plain lenses (own arm) |
| 4 | `anchorHops.Complete` | `anchor_derivation.go:141` | untyped hops, `WITH`-drops, **and every multi-walk lens** |
| 5 | `UnresolvedExpansionPosition() < 0` | `anchor_derivation.go:144` | an unresolved `*` taxonomy position |
| 6 | `Labels[Anchor] == actorEnumerator.actorType` | `anchor_derivation.go:147` | anchor label ≠ actor type |

Conjunct 1 is the row's subject. **Conjuncts 2 and 4 are the two refusals hiding behind it**, and both bite
100 % / 20 % of the same population. The reason switch (`anchor_derivation_mode.go:202-215`) evaluates
`!patternClosedOutput` **first**, so the live log — *"the lens's row depends on inputs outside its compiled
pattern"*, 15 personal lenses — reports conjunct 1 and masks the rest. This is the
root-cause-names-the-instance-the-harness-observed shape: the reported reason is true and it is not the only
one.

### 1.3 Intent

Make the personal plane's derivation refusal say what it actually requires, satisfy those requirements, and
remove the two masked refusals — so a personal-lens event costs a handful of relation-filtered adjacency reads
instead of an undirected expansion through a 3,913-degree descriptor hub.

---

## 2. Grounding ledger (verified in code this fire)

| # | Fact | Citation |
|---|---|---|
| G1 | `patternClosedOutput` has exactly one non-test `true` setter: `InstallActorAggregate`. `InstallPersonalLens` refuses **by omission**, riding the field's `false` zero value. | `projection/driver.go:502`; `projection/personal.go:106-135`; `pipeline.go:375-378` |
| G2 | The two out-of-pattern inputs are both in `personalEnvelopeFn`: D1 at `personal.go:183`, Interest Set at `personal.go:194`. D1 wins over relevance (the deny returns at `:188`, before the Interest block opens at `:192`). | `projection/personal.go:157-210` |
| G3 | **The design's own citations have drifted.** §4.4 cites `personal.go:172-184` / `:186-195`; a 6-line posture comment inserted at `:177-182` moved everything — `:186-190` is still D1, and the Interest Set is `:192-201`. The §4.4 Interest-Set citation currently points at D1 code. | as above |
| G4 | The D1 grant-change edge is an **in-process function call**: producer `guardedWrite` → `transitionFrom` → `notifyGrantChange` → `Reprojector.GrantChanged` → coalesced dirty set → `ReprojectPersonalActor` on every registered personal lens. | `adapter/natskv.go:346-353`, `:419-443`; `pipeline/grantchange.go:135`, `:188`; `grantchange/reprojector.go:312`, `:450`; `pipeline/reproject_personal.go:143` |
| G5 | All four `cap-read.` producers qualify for the sink (`plan.AuthPlane` ∧ `EntryKeyColumn != ""` ∧ `cap-read.` prefix ∧ `KeyOwnershipRoundTrips()`): `capabilityRead` + the three generated `edgeManifest*ReadGrants`. | `projection/driver.go:377-389`, `:446-447`; `bootstrap/lenses.go:216`, `:227`; `pkgmgr/anchorwalk.go:501-521` |
| G6 | **The identity key-shred path emits no grant-change signal, on BOTH its arms.** `DeleteAllForActor` calls the plain `adpt.Delete` (perEntry targets); `Pipeline.Delete` behind `Control.NullifyRow` calls `p.currentAdapter().Delete` (doc-mode targets). Both are reached from the same key-shred loop. `DeleteWithOutcome` exists and reports `Transition`, and neither uses it. *(Corrected after §13 M10: this is one identity's own `cap-read.*` children, NOT the largest revocation in the system — `truncateTarget` clears a producer's whole key space and already announces, `pipeline/grantchange.go:169-198`.)* | `pipeline/pipeline.go:1336`, `:1383`; `keyshredded/manager.go:360`, `:363`; `adapter/natskv.go:267`; `adapter/adapter.go:229-231`, `:295-305` |
| G7 | **The Interest Set has no change edge.** `personal.register` / `personal.deregister` write the bucket and return, touching no hydrator/reprojector/pipeline; `InterestReconciler` deletes registrations and references no reprojector; and deleting the last device **widens** `IsRelevant` (absence admits everything). | `control/service.go:1213-1229`, `:1232-1248`; `health/interest_reconciler.go:29`, `:47`; `personalinterest/interest.go:290-292` |
| G8 | **"Hydration for the Interest Set" is false as a change edge.** `personal.hydrate` runs only when a device calls it; `Manager.UpdateInterest` calls `registerInterest` alone and its own doc says it *"does not retroactively hydrate a newly-widened scope's backlog"*. | `edge/sync/sync.go:506-520`, `:993`; `control/service.go:1266` |
| G9 | The `PersonalSweeper` re-runs `ReprojectPersonalActor`, i.e. the **whole envelope** — so it re-asks *both* `IsReadable` and `IsRelevant`. 60 s tick, 5 identities/batch, unpaged `vtx.identity.*` population, cursor not persisted across restart. Cycle = N/5 minutes (~33 h at 10 k identities, its own arithmetic). | `grantchange/sweeper.go:14-31`, `:151-162`, `:177`, `:200-215`, `:246-272` |
| G10 | There is a blocking lint gate default-denying an undeclared `capabilityread.IsReadable` call site (`grant-change-posture: (subscribed|swept|none-justified) <why>`). There is **no equivalent for `personalinterest.IsRelevant`** — the asymmetry is structural, not accidental. | `scripts/lint-conventions.go:565`, `:3119-3155`; `projection/personal.go:177-182` |
| G11 | `standingHealerInstalled()` — `p.sweeper != nil \|\| p.personalPlaneHealer.Load()` — **already exists** and is already used by the §5.1 walk scope. The derivation's healer conjunct reads `p.sweeper` alone and has not adopted it. Two readers of "has this lens a standing healer", disagreeing. | `pipeline/walkscope.go:56-69`, `:339-351`; `anchor_derivation_mode.go:246` |
| G12 | `ruleinstall.go` builds **no** `anchorHops` when `len(branches) > 1`, on the stated reason *"each branch carries its own anchor, and one graph cannot speak for all of them"*. The §5.1 walk scope, in the very next paragraph of the same function, is *deliberately* derived over **every** branch. | `pipeline/ruleinstall.go:405-432`, `:448-455` |
| G13 | Today `CompiledBranches` is populated only for a multi-walk Personal lens, so G12's exclusion governs only personal lenses — the class conjunct 1 already refuses, and it has never had a live consumer. **But that is a property of the corpus, not a structural fact** (§13 minor): the gate is `len(spec.CypherBranches) > 0`, which nothing restricts to `Personal`, and `branchmerge.go`'s own doc contemplates a hand-authored branches spec. Inc 3 therefore arms `anchorHops` for *whatever* carries branches, and its refusals must be written for that population, not for these three lenses. | `lens/schema.go:167-174`; `lens/corekv_source.go:1389`, `:1560-1596`; `pipeline/branchmerge.go:31-35` |
| G14 | Every branch of a walk-generated personal lens is compiled from the **same** constant head `MATCH (identity:identity {key: $actorKey})`. So G12's stated reason ("each branch carries its own anchor") is false for the only population it governs. | `pkgmgr/anchorwalk.go:71-72`, `:474-483` |
| G15 | A multi-walk lens's `anchorHops` is the **zero** `HopIndex`: `Complete == false` **and `Incomplete == ""`**. So `noteStaticDerivationRefusal` would log an **empty reason**. Masked today only because conjunct 1 is evaluated first. | `ruleinstall.go:411`; `anchor_derivation_mode.go:205-215`; `hopindex.go:324` |
| G16 | The **actor's own tombstone retraction is not derivation-gated** — only *peer* anchors go through `affectedAnchors`. An identity tombstone still retracts every key via `emitPersonalFrames`'s empty frame. | `evaluate.go:181-213`, `:370-392` |
| G17 | The enumerated-actor list is what `emitPersonalFrames` publishes frames from. On a personal lens an under-approximated anchor set does not merely skip a reprojection — it changes which **frames** a device receives. | `evaluate.go:1190`; `results.go:213`; `reproject_personal.go:197-222` |
| G19 | **A producer's own convergence sweep announces.** Both heal legs call `notifyGrantChange` with the outcome's rendered key, and the code says why: *"a retraction either of them heals is as real a grant withdrawal as one the CDC path writes."* This is what closes the composition hazard in §9 R7. | `pipeline/reproject.go:535-545`, `:643-645` |
| G20 | **A personal lens cannot carry a secure decryptor**, structurally: `TargetNATSSubjectConfig` has no `secureColumns`, the nats_kv/nats-subject branch refuses the field outright, and `validateSecureColumns` requires `Protected`. So no `piiKey` shred can reach one, and the plain licence's decryptor conjunct has no analogue to build here. | `lens/corekv_source.go:1491`, `:1514`, `:1615`; `adapter/natssubject.go:383-405` |
| G21 | **`control` imports `health`**, and **`grantchange` imports `projection` → `pipeline`**. So a sink type declared in `control` cannot be used by `health.InterestReconciler`, and `pipeline` cannot reference `grantchange` types. Both edges must be injected by `cmd/refractor` as bare function/interface values (§13 M5/M6). | `go list -deps ./internal/refractor/control`, `./internal/refractor/grantchange` |
| G22 | There is a **third** reader of "has this lens a standing healer": `oneKeyAnswerSound`, which reads `p.sweeper == nil` with the same stated intent as the other two and whose doc reasons explicitly about the personal case. | `pipeline/actor_enumerator.go:362-369`, `:394-402` |
| G18 | `PersonalSweeper` exposes `Cursor()` and `CycleCompletedAt()`; `Reprojector` exposes `QueueDepth()`. A live healer signal is available — but **not a verdict**: `publishProgress` runs unconditionally after the batch loop, and `reprojectActor` logs-and-continues on a per-lens failure, so the existing surface advances through a total healer failure (§13 B3). §4.4 adds the verdict. | `grantchange/sweeper.go:177`, `:187`, `:287-308`, `:312`, `:320`; `reprojector.go:348`, `:437-461` |

---

## 3. Executable censuses

Each ships as the command plus the expected result, so the build's Phase 0 re-runs it mechanically.

### 3.1 The personal-lens population is 15, all in one file, and none is generated

```sh
grep -c 'Personal:      true' packages/edge-manifest/lenses.go        # expect 15
grep -rn 'Personal:' packages/ internal/pkgmgr/ --include='*.go' | grep -v edge-manifest | grep true   # expect 0
```

*(Quote every `--include` glob: an unquoted one is expanded by zsh and the census dies with `no matches
found` instead of running — §13 minor.)*

`ExpandReadGrantWalks` hardcodes `Adapter: "nats-kv"` and never sets `Personal` (`pkgmgr/anchorwalk.go:505`),
as does the capability materializer (`capabilitymaterializer.go:911-923`) — so unlike the label censuses that
had to reach the generated corpus, **this population is entirely source-visible**. Names:
`edgeIdentity, edgeServices, edgeCatalog, edgeTasks, edgeInstances, edgeEntitySessions, edgeEntityProviders,
edgeEntityBookings, edgeEntityTabs, edgeEntityStudios, edgeEntityMenuItems, edgeStaffPanes,
edgeStaffWorkOrders, edgeProviderSchedule, edgeProviderQueue`.

### 3.2 Which increment unlocks which lens — the payoff, traced conjunct by conjunct

Derived from the shipped corpus census (`anchor_hopindex_corpus_census_test.go:79-155`), which already pins
each cypher's index verdict. **This table is the design's payoff claim, and Inc 2 alone does not deliver the
headline.**

| Lens | Walks | Index verdict | Refused today by | Unlocked by |
|---|---|---|---|---|
| `edgeInstances` | 1 | `hopIndexed` | 1 + 2 | **Inc 2** |
| `edgeIdentity` (hand-authored, no `Walks`), `edgeServices`, `edgeEntityProviders`, `edgeEntityBookings`, `edgeEntityTabs`, `edgeEntityStudios`, `edgeEntityMenuItems`, `edgeStaffPanes`, `edgeStaffWorkOrders`, `edgeProviderSchedule`, `edgeProviderQueue` | 1 | `hopIndexed` | 1 + 2 | **Inc 2** |
| `edgeCatalog` (**128 k**) | 3 | per-branch `hopIndexed` (#0/#1/#2) | 1 + 2 + **4** | **Inc 2 + Inc 3** |
| `edgeTasks` | 2 | per-branch `hopIndexed` | 1 + 2 + **4** | **Inc 2 + Inc 3** |
| `edgeEntitySessions` | 2 | per-branch `hopIndexed` | 1 + 2 + **4** | **Inc 2 + Inc 3** |
| `objectLiveness`, `objectAttachments` (40 k each) | 1 | `hopUntypedHop` | 4 only (**not personal**) | neither — §4.5 non-goal, own row |
| `edgeManifest{,Staff}ReadGrants` (90 k / 57 k) | — | `hopWithDropped` | 4 only (**actorAggregate**) | varlength Inc 2 (own 📋 row) |

Pinning command:

```sh
go test ./internal/refractor/ -run TestCorpusAnchorHopIndex -count=1
```

### 3.3 The `IsReadable` / `IsRelevant` call-site asymmetry

```sh
grep -rn 'capabilityread.IsReadable(' --include='*.go' internal/ cmd/ | grep -v _test   # expect 1 (personal.go:183)
grep -rn 'personalinterest.IsRelevant(' --include='*.go' internal/ cmd/ | grep -v _test # expect 1 (personal.go:194)
grep -c 'grant-change-posture' scripts/lint-conventions.go                              # expect >0
grep -c 'interest-change-posture' scripts/lint-conventions.go                           # expect 0 today, >0 after Inc 1
```

One call site each: the Inc 1c migration leaves **zero** debt, so the new gate is blocking from day one.

### 3.4 Every `cap-read` retraction path, and whether it announces

The first draft of this census matched `adpt.Delete(` and so was **blind by construction** to
`p.currentAdapter().Delete(...)` — the doc-mode arm of the very hole it was written to find (§13 M10). Match
the *call*, not one receiver spelling:

```sh
grep -rnE 'notifyGrantChange|DeleteWithOutcome|\.Delete\(ctx' internal/refractor/pipeline/*.go | grep -v _test
```

Expected: announcements at `results.go:143`, `reproject.go:544`, `:644`, `grantchange.go:188`; **silent**
retractions at `pipeline.go:1383` (`DeleteAllForActor`, perEntry) and `pipeline.go:1336` (`Pipeline.Delete`
behind `Control.NullifyRow`, doc-mode) — both reached from `keyshredded/manager.go:360`/`:363`. Both are the
G6 hole; Inc 1a closes both. `hydrate.go:86`, `reproject.go:548` and `reproject_personal.go:200` also appear
and are **not** holes: each is the non-guarded fallback of a leg whose guarded arm announces immediately
above it. Classify every hit; do not count lines.

**And the producer-closure census Inc 1d gates on** — every lens whose output key space is `cap-read.`, and
whether each is a sink-armed read-grant producer:

```sh
grep -rn 'cap-read\.' --include='*.go' packages/ internal/ | grep -v _test | grep -iE 'key|prefix|into'
```

Expected today: the base `capabilityRead` (`bootstrap/lenses.go:216`) and the three generated
`edgeManifest*ReadGrants` (`pkgmgr/anchorwalk.go:501-521`) — four, all `actorAggregate`, all satisfying
`IsReadGrantProducer`. A fifth hit that is not is what Inc 1d must refuse.

---

## 4. The shape

### 4.1 Increment 1a — the key-shred path announces, on both its arms

The identity key-shred loop retracts an actor's `cap-read.*` grants through two siblings — `DeleteAllForActor`
for a `perEntry` target and `Pipeline.Delete` (behind `Control.NullifyRow`) for a doc-mode one — and **neither
announces**. Today the accident masks it. The licence removes the accident, so this closes first.

**The discriminator is liveness derivation, not the `OutcomeDeleter` interface.** `DeleteOutcome.Transition`
is documented as *"always `TransitionNone` for an unguarded adapter"* (`adapter/adapter.go:229-231`), and
`GrantWriterAdapter` satisfies `OutcomeDeleter` while leaving `Transition` at its zero value
(`read_path_adapters.go:166-171`). A design keyed on the interface would route through `DeleteWithOutcome`,
receive `TransitionNone`, announce nothing, and report the hole closed — which is the failure mode this whole
design exists to refuse. Key on the **sequence-guarded** adapter, the one that actually reads the stored body
to derive the transition.

- **Guarded adapter:** `DeleteWithOutcome`, announce per key on `TransitionRevoked`, using the outcome's own
  rendered `Key` — never re-derived at the call site (`adapter.go:222-227`).
- **Every other adapter:** announce **once per actor**, directly on the sink, with the `actorKey` the shred
  call already holds. This is the answer to "the fallback has no key to announce with" (§13 M4): the sink's
  consumer wants an identity key — `Reprojector.GrantChanged(actorKey)` parses exactly that
  (`reprojector.go:312-318`) — and `notifyGrantChange`'s key-pattern inverse exists only to *recover* one.
  Announcing per actor is coarser than per key and strictly safe: the reprojection is per-actor anyway.

### 4.2 Increment 1b — the Interest Set gets the edge it never had

Three writers change what `IsRelevant` answers. Each gets the same edge, onto machinery that already exists:

| Writer | Site | Direction | Edge |
|---|---|---|---|
| `personal.register` | `control/service.go:1224` | widens or narrows | enqueue the registering identity |
| `personal.deregister` | `control/service.go:1244` | widens (last device removed ⇒ absence admits everything) | enqueue the identity |
| `InterestReconciler` orphan delete | `health/interest_reconciler.go:47` | widens | enqueue each identity whose registration it deleted |

**The transport is a bare `func(identityID string)`, injected by `cmd/refractor`.** My first draft declared a
sink interface in `control` and had `health.InterestReconciler` take "the same sink" — which is an import
cycle: `control` imports `health` (§13 M5, G21). A named interface in a third package would be a second
spelling of a one-method contract, which §4.3 argues against for the gate. So both writers take a nullable
callback field, `cmd/refractor` supplies `reprojector.GrantChanged`-shaped closures at the same site it
already wires `registerPersonalHealer`, and nil = today's behaviour.

**Why enqueue rather than reproject directly:** the Reprojector owns coalescing, the registry-ready hold, the
bounded dirty set and the drop accounting. A second path would be a second lifetime to reason about (§5) and
an unbounded fan-out under a device that flaps its registration.

**One fix the transport requires.** `reprojectActor` deliberately does not re-enqueue on failure — *"the actor
has a standing healer behind it"* (`reprojector.go:450-459`) — and `take` has already removed the actor from
`dirty`. But `ReprojectPersonalActor` refuses with `ErrNoOrderingToken` while a pipeline's ack floor is
unseeded, and its own comment calls that *"reachable, not theoretical: the drain worker's ticker runs before
every personal pipeline's consumer has seeded its ack floor"*. A device that narrows its interest inside that
window loses the retraction to the sweeper — up to a full cycle (§13 M7). **Fix at the latch, not the retry:**
extend `registryIsReady` (`reprojector.go:218`) to additionally require every *registered* personal pipeline
to report a non-zero `LastAppliedSeq`, so the drain does not run until a reprojection can succeed. That reuses
the existing hold (and its 2-minute bound) rather than adding a retry queue with its own lifetime.

**Retraction is the direction that matters.** A device that narrows must stop receiving the excluded keys; the
frame is authoritative and prunes, so one reprojection retracts them (G17). Widening is benign and was already
unpromised by `UpdateInterest`'s own contract (G8) — the edge improves it, and this design claims no more.

### 4.3 Increment 1c/1d — the two gates that bind the next author

**(c) The reader side.** Extend `checkGrantChangePosture` (`scripts/lint-conventions.go:3119-3155`) to a second
symbol, `personalinterest.IsRelevant`, with an `interest-change-posture: (subscribed|swept|none-justified)
<why>` annotation and the identical default-deny / unknown-shape / missing-`<why>` findings and self-tests.
One call site exists and Inc 1b makes its honest answer `(subscribed)`, so the gate is **blocking from day
one**. The two symbols share one check driven by a symbol→annotation table rather than being copy-pasted: the
rule is about the *shape* — a projection read as a decision input by another projection needs its own change
edge — not about `capabilityread`.

**(d) The producer side — the one that makes conjunct 1 mean anything.** The D1 gate answers by a **wildcard
listing** over `cap-read.*.<actor>.<anchor>` (`capabilityread/capabilityread.go:108`), because, as its own
package doc says, package names are not statically enumerable. So the producer set is **open**, and "every
`cap-read` producer announces" is a standing claim, not a census result (§13 B2). Close the set where it can
be closed:

- **At install:** `InstallActorAggregate` already computes `IsReadGrantProducer` (`driver.go:377-389`). Add
  the converse refusal — a lens whose declared output key space begins `cap-read.` and which does **not**
  qualify (wrong projection kind, no `entryKeyColumn`, no key-ownership round trip) is **REFUSED at
  registration**, loudly, rather than installed as a silent grant writer no plane hears from.
  *(Amended at build, 2026-09-02 — two corrections from the Inc 1 cold reviews.)* **"No sink wired" is not a
  registration refusal.** Refusing the install would turn a host with no reprojector into an auth-plane
  outage on the primordial `capabilityRead` lens, and the sweep-without-edge posture is a shipped, tested
  invariant; a qualifying producer with no sink logs a Warn, and conjunct 1 of the licence (§4.4c) is where
  the narrowing refuses. **And the declared key space is not the only key space:** a descriptor-less plain
  `nats_kv` lens on `capability-kv` renders its key from RETURN columns, so a string literal `'cap-read.x'`
  mints a live D1 grant no install-time descriptor check can see. The closure is therefore at the **write**:
  the NATS-KV adapter carries a read-grant licence, bound from the *rule* at the single adapter-construction
  point (activation and INTO hot-reload alike), and an unlicensed adapter refuses to write, delete or purge
  any `cap-read.`-prefixed key — terminal, health-reported, fail-closed. The install refusal and the authoring
  gate remain, sharing one predicate (`CapReadWriterRefusal`, which the gate calls on an AST-built rule).
- **At authoring:** the matching `lint-conventions` check over `packages/**` + `internal/bootstrap`, so the
  refusal is discovered by the author rather than at a stack's boot.

Without 1d, a vertical shipping `cap-read.billing.<actor>` through a plain `nats_kv` lens writes live grants
that the reader's wildcard finds, gets no sink and no refusal, and a licensed personal lens keeps publishing
a revoked row for up to a sweeper cycle. That is the concrete exploit the adversarial pass produced, and it is
the reason 1d is in this fire and not filed as defense-in-depth.

### 4.4 Increment 2 — the licence

**Three edits.**

**(a) The gate itself.** `derivationIndexForAct`'s first conjunct becomes a disjunction — the edit my first
draft described everywhere and specified nowhere (§13 M9):

```
if !p.patternClosedOutput && !p.personalDerivationLicensed() { return full.HopIndex{}, false }
```

and `noteStaticDerivationRefusal`'s switch (`anchor_derivation_mode.go:202-215`) gains the licence's own
refusal strings ahead of the `patternClosedOutput` default, or a licensed lens keeps printing *"the lens's row
depends on inputs outside its compiled pattern"* forever and §11's acceptance criterion is unreachable.

**(b) The healer conjunct converges on the accessor that already exists.** `p.sweeper == nil` becomes
`!p.standingHealerInstalled()` (`walkscope.go:350-351`) — the reader the §5.1 walk scope already uses. Today
two readers of the same question disagree and the walk scope's is the correct one.

**(c) `personalDerivationLicence` — the predicate.** Mirroring `plainDerivationLicence`'s two-halves shape
(index = *can it answer*; licence = *is a smaller answer safe here*), with a stable refusal string per
conjunct so the static-refusal log can latch one.

| # | Conjunct | Why | Refusal string |
|---|---|---|---|
| 0 | **this pipeline is a personal lens** (personal envelope + key-set target), asserted at the registration site | the plain licence opens with `p.authPlane` for the same reason: a licence with only wiring conjuncts says nothing about the class the argument was made about (§13 M9) | `"not a personal lens"` |
| 1 | the D1 read gate is wired for this pipeline (`capKV != nil`) **and** a grant-change reprojector is wired in this process | an unwired gate means the lens runs open; no reprojector means input 1 has no edge *here*. The claim that every **producer** announces is not a runtime conjunct — Inc 1d makes it an install-time property | `"the D1 read gate is not wired"` / `"no grant-change reprojector is wired"` |
| 2 | the Interest Set edge is armed **or** `interestKV == nil` | a lens with no interest filter has only one out-of-pattern input | `"the Interest Set has no change edge"` |
| 3 | the personal-plane healer's **last pass verdict is clean and recent** | see below | `"the personal-plane healer has never completed a pass"` / `"…last pass failed"` / `"…could not enumerate its population"` / `"…last pass is older than N intervals"` |
| 5 | **the deployment is single-instance** — a live count of Refractor instances ≤ 1, and the count itself readable | the whole edge is an in-process function call (G4). On a second instance a producer's announcement never reaches a personal pipeline hosted elsewhere, and **conjunct 1 stays true on every instance while the edge silently stops spanning the deployment** — a fail-open at exactly the transition (Andrew, at ratification). See below for why the staleness direction is the whole difficulty | `"more than one Refractor instance is live"` / `"the instance count is unreadable"` |
| 4 | the compiled rule references neither `$now` nor `$projectedAt`, and its label set is exhaustive enough to prove it | a row that moves with wall-clock alone is the purest out-of-pattern input there is, and after this increment only the sweeper refreshes it. No shipped personal lens uses either, so the conjunct is latent — which is exactly why it must exist before one does (§13 M11) | `"the lens's row depends on $now"` |

**Conjunct 3 is a VERDICT, not a progress stamp.** My first draft read a `LastProgressAt`, and the adversarial
pass broke it three ways (§13 B3): `publishProgress` runs unconditionally after the batch loop
(`sweeper.go:187`), and `reprojectActor` logs-and-continues on a per-lens failure
(`reprojector.go:450-459`) — so a Capability-KV outage that fails **every** reprojection of **every** actor
still advances the stamp every 60 s, and the licence reads "healthy" through a total healer failure. The plain
licence's analogue is a verdict (`AuditStatus.LastPassAt` + suppression + staleness,
`anchor_derivation_plain.go:292-320`), and that is what this needs:

- `PersonalSweeper` records, per pass, `{completedAt, attempted, failed, populationReadable}` and publishes it
  the way `AuditStatus` is published;
- the licence refuses on **never passed**, on `failed > 0`, on `!populationReadable`, and on
  `completedAt` older than `K × SweepInterval`, with `K` pinned by a cross-package test the way
  `IdleSweepBackoffEvery` is pinned at half `DefaultCapabilitySweepStallCycles`;
- **`Run` performs one pass immediately instead of waiting for the first tick** (`sweeper.go:129-144` uses a
  bare `NewTicker`), or the whole personal plane runs on the enumerator for ≥ 60 s after every restart —
  precisely when the backlog is deepest.

Two residues stay, named rather than closed. **(i)** An identity created mid-cycle is outside the cached
population until the walk wraps (`sweeper.go:204-243`, `:265-271`). That gap is benign in the direction that
matters: a new identity has no projected rows yet, so a lost signal there under-delivers rather than
over-grants. **(ii)** A population that cannot be listed refuses the licence for the *whole* plane, dropping
all 15 lenses back onto the enumerator. That is the correct direction and it is a real availability cliff —
§9 R6.

**Conjunct 5's difficulty is the STALENESS DIRECTION, not the count.** Health KV is keyed
`health.<component>.<instance>`, so an instance count is derivable today with no new state and no vendor
question. But the two staleness directions are not symmetric:

- a **crashed** instance leaving a stale entry over-counts ⇒ refuses the licence ⇒ pessimisation, safe;
- a **newly started** second instance that has not yet written its entry under-counts ⇒ the licence stays on
  while the edge no longer spans the deployment ⇒ **fail open**, which is the exact hazard the conjunct
  exists to close.

So the count is a **backstop with a bounded exposure window**, not the primary defence, and the design must
say which is which rather than letting the runtime read carry an argument it cannot hold. Read it off the
same clock as conjunct 3's verdict (never per event), refuse on unreadable, and state the window.

**The primary defence is a build-time gate**, per the standing lint doctrine — the thing that binds the
author who makes Refractor multi-instance, who will not be reading this document. It fails the moment the
deployment gains a second-instance affordance (a queue-group consumer spec, a replica count, an instance-id
config) while the personal licence's edge is still process-local, and its message names this design and the
durable-signal alternative (§8 #6) as the precondition. A narrowing that revokes itself when its premise
expires is worth more than a risk row asking someone to remember.

*(A sharper runtime signal may exist — a pipeline could ask the server how many consumers are bound to its
own durable — but that is a claim about NATS behaviour at our pinned version, so the builder grounds it in
`docs/vendors.md`'s upstream sources before substituting it. The Health-KV count is the shape that needs no
vendor question; treat the consumer-bound route as a permitted sharpening, not an open fork.)*

**Where it is asserted.** `InstallPersonalLens` cannot know what the host wired, so — exactly like
`SetPersonalPlaneHealer` — the host asserts conjuncts 0–2 at `registerPersonalHealer`, and the zero value is
refusal. Conjuncts 3–4 are read live at each gate evaluation.

*(Amended at build, 2026-09-03 — what two cold reviews added to the conjuncts as built.)* Conjunct 1's sink
half and conjunct 2 are **live accessors**, not registration-time samples (a sink-less producer installed
after the last personal lens registered must revoke on the next evaluation; the `InterestReconciler` is
the fourth Interest Set writer and is counted from construction). Conjunct 3 additionally refuses until a
pass **began after this lens registered** (`StartedAt > RegisteredAt`) — a lens registered into an
already-swept plane must not inherit a verdict from a pass that never drove it. Conjunct 5 refuses a
**readable count of zero** on both the sweeper side and the licence side: the lister is itself a live
instance, so zero is a broken census, not an empty deployment. Conjunct 4 is derived once at rule
publication onto `ruleState`, and the gate asks the **index before the licence**, mirroring the plain arm's
documented order — a multi-walk lens is refused on its zero `HopIndex` (a named reason,
`DerivationNoBranchIndexRefusal`, closing G15) before any census mutex is touched. The exposure window
for a not-yet-heartbeating second instance is up to one **sweep** interval past its first heartbeat, not
one heartbeat. Conjuncts 0 and 1 are structurally vacuous in the process `cmd/refractor` builds; only 2
(transiently), 3, 4 (once an author writes a clock) and 5 can refuse a shipped deployment.

**Why `patternClosedOutput` stays false.** It is a *claim about the lens*, read by two predicates with
different tolerances and different rollback shapes. §17.2 of the parent design already refused to conflate
"may an event be withheld" with "which anchors can an arriving event affect"; this is that refusal from the
other side. The delivery-side narrowing is §4.6's named follow-on.

**The knob.** `REFRACTOR_ANCHOR_DERIVATION=off` already restores the enumerator on the next event
(§17.3/§17.5); no new operator surface. Note §17.5's asymmetry: the knob bounds the damage, the healer
finishes the job, and for a personal lens that healer is conjunct 3's sweeper.

### 4.5 Increment 3 — the multi-walk refusal

`ruleinstall.go:411` hands a multi-walk lens no hop index, on a reason that is false for the population it
governs (G12/G14). The fix mirrors what the same function already does for the walk scope two paragraphs
below — derive over **every** branch:

- build `AnchorHopIndex().WithLabelExpansion(...)` **per branch**, keeping the slice on `ruleState`;
- refuse the lens whole if **any** branch is not `Complete`, if **any** branch has
  `UnresolvedExpansionPosition() >= 0`, or if the branches' anchor labels are not all equal — the last being
  the checkable form of the reason G12 asserted. Write these refusals for *any* branch-carrying lens, not for
  today's three: nothing restricts `CypherBranches` to personal lenses (G13);
- on derive, run `walkToAnchors` per branch from that branch's own seeds and **union** the results. The union
  is a superset of each branch's superset, which is §4.7's invariant. The adversarial pass verified this
  end-to-end for `edgeCatalog` — the shared tail is byte-identical across branches, so the RETURN
  pattern-comprehension is indexed as a non-binding hop in every branch and `AnchorSideSeeds` seeds both
  endpoints where either lacks a binding distance; `mergeBranchRows` can make a row depend on a sibling
  branch, but `executeBranches` re-runs **every** branch per derived actor, so the merge is not a hole;
- **the read budget must be threaded, not assumed.** `reads`, `work` and the `neighbours` memo are per-call
  locals and `errDerivationTooWide` is swallowed inside `walkToAnchors` (`anchor_derivation.go:216-236`,
  `:342`, `:359`), so "one shared cap" is not expressible today: as written, three branches pay 3× the reads
  and 3× the memo misses (§13 minor). Inc 3 threads an explicit budget + shared memo through the per-branch
  calls, so a wide lens declines once rather than N times.
- **`seedAnchorLabels` and `rootHops` stay single-walk**, gated on the same arm. A seed label that must speak
  for one evaluation is a different question from an anchor set that is a union, and this design does not
  answer it.

*(Amended at build, 2026-09-03.)* As built, the pair `anchorHopsPerBranch` + `anchorHopsPerBranchRefusal`
is published on `ruleState` (mirroring `walkScope`/`walkScopeRefusal`) so the static conjuncts are decided
once per publication; the refusals are lens-wide and named per conjunct (`DerivationBranchIncompleteRefusal`,
`DerivationBranchUnresolvedExpansionRefusal`, `DerivationBranchAnchorDisagreementRefusal`, with
`DerivationNoBranchIndexRefusal` kept for a non-full branch set and a distinct unnamed-index belt for the
single-walk arm); the anchor-label conjunct is read live because the enumerator is installed after
publication; the budget is one `derivationBudget` per event (reads, work, ranged reads, one shared memo)
threaded through every branch, so a wide lens declines once; and the control-plane `health` RPC reports
`{licensed, refusal, indexReady, indexRefusal}` from one `ruleState` snapshot so "licensed but the index
refuses" is an operator-readable state. The differential runs the real corpus with the merged recompute
(the personal arm) as ground truth, per branch and unioned; a cold review mutated the union to one branch
and watched it fail.

**Give the exclusion a reason.** G15: a multi-walk lens's zero `HopIndex` has `Complete == false` and an
**empty** `Incomplete`, so the operator log prints a blank reason the moment conjunct 1 stops masking it.
Whatever survives this increment gets a named constant in the closed vocabulary the corpus census
default-denies.

### 4.6 Non-goals, each with its owner

- **The delivery-side consumer-filter narrowing for personal lenses** (`rulestate.go:301-358`). Its safety
  argument is the *same* one — both consumers lose exactly the same set of incidental reprojections, and it
  would be dishonest to claim the derivation is intrinsically safer. What differs is operational: the
  derivation is pure client-side computation revertible by an env knob, while the filter is a consumer-subject
  change whose rollback is a reset (§15.6). **Trigger for the follow-on:** the derivation licence live for one
  full sweeper cycle with no `DivergentEvents` on the personal arm. Note it would also be largely inert: most
  personal lenses carry `containedIn*0..`, hence a non-exhaustive label set, hence conjunct 2 of the delivery
  predicate refuses them anyway.
- **The untyped-hop refusal** — `objectLiveness` / `objectAttachments`, 40 k each. Not personal, not this
  licence (§1.1). A separate row; the shape is `OPTIONAL MATCH (o)-[r]->(owner)` at an unlabeled position,
  which is deliberate (objects attach over several relations).
- **`edgeManifest{,Staff}ReadGrants`** — the `WITH`-scope refusal, already a 📋 ready row.
- **The THIRD healer reader, `oneKeyAnswerSound`** (`actor_enumerator.go:362-369`, `:394-402`), which reads
  `p.sweeper == nil` with the same stated intent as the two §4.4(b) converges. Deliberately left alone (G22):
  converging it would arm the one-key narrowing (`actor_enumerator.go:331`) for the personal corpus as a side
  effect of this edit, with no licence review of its own. It is named here so the next author finds it as a
  decision rather than as an oversight, and so §4.4(b)'s "the walk scope's reader is the correct one" is not
  read as a licence to sweep all three.
- **Multi-instance / HA Refractor.** The grant-change edge is in-process (G4). See §9 R4.

---

## 5. New state, and its lifetime

| State | Created | Reset | Carried | Ordered |
|---|---|---|---|---|
| `PersonalSweeper` pass verdict `{completedAt, attempted, failed, populationReadable}` | first completed pass — which `Run` now performs immediately rather than on the first 60 s tick | zero at process start, and **zero refuses the licence** (the plain licence's zero-`LastPassAt` rule) | not persisted; a restart re-earns the licence on the first pass | replaced wholesale per pass under the sweeper's own lock; read live at every gate evaluation, never snapshotted |
| the interest-change callback (`func(identityID string)`) on `control.Service` and `health.InterestReconciler` | `cmd/refractor` wiring, before `Run` | never | process lifetime | nil = today's behaviour; set once, never mutated. A bare func rather than an interface because `control` imports `health` (G21) |
| the healer-verdict accessor on `Pipeline` | injected at `registerPersonalHealer`, beside the existing `SetPersonalPlaneHealer` | never | across hot reload (host wiring, not rule state) | read live; `pipeline` cannot import `grantchange` (G21), so this is a one-method value the host supplies, not a typed dependency |
| `personalLicence` (conjuncts 0–2, per pipeline) | at `registerPersonalHealer` — once, at activation (*amended at build 2026-09-03: a hot reload never re-runs it, and that is correct — every asserted member is a property of the process; the two that can move are live accessors, and conjunct 4 rides `ruleState`, re-derived by the very publication a reload performs*) | never | across reload | read **live** at each gate evaluation, not snapshotted onto `ruleState` — both healer arms are installed after `useFullEngineBranches` runs, the same reason `standingHealerInstalled` is read live (`walkscope.go:76-84`) |
| `anchorHopsPerBranch []full.HopIndex` | `useFullEngineBranches`, per rule publication | replaced wholesale on every publication, so a reload can never leave a previous body's indexes armed | published on `ruleState` under `ruleMu` with the rest of the compiled rule | never mutated after publication; readers alias |
| the per-derivation shared read budget + neighbour memo (Inc 3) | per `affectedAnchors` call | per call — it must **not** outlive one event, or one wide event poisons the next | not carried | threaded through the per-branch `walkToAnchors` calls in order; a breach declines the whole lens once |

The `registryIsReady` latch (`reprojector.go:146`, `:186-215`) gains one conjunct — every registered personal
pipeline reports a non-zero `LastAppliedSeq` — and keeps its existing lifetime and its 2-minute bound
unchanged. No new latch.

The interest edge adds **no** new queue, latch or registry: it enqueues onto the existing dirty set, whose
lifetime (`reprojector.go:97-101`: created at boot, deliberately not persisted, drained on a 1 s ticker behind
a latching 2-minute registry-ready hold) is unchanged and already reviewed.

---

## 6. Reconciliation with the existing mental model

**"Didn't we already fix the personal plane's grant staleness?"** Yes — for input 1. The
personal-lens-grant-change-trigger design shipped the D1 edge and the PersonalSweeper (2026-08-14). It never
claimed to cover input 2, and it did not. What is new here is that a *narrowing* now depends on that coverage,
which raises the bar from "an improvement" to "a precondition".

**"Doesn't the sweeper already cover both?"** It does re-ask both gates (G9) — that is why the walk-scope
narrowing was allowed to count it. But a 60 s/5-identity round-robin is a **backstop**, not an edge, and it is
the only thing standing behind input 2. The parent design's own framing — *narrowing removes an accident, not
a mechanism* — is the test, and for the Interest Set the honest answer today is that it removes an accident
and leaves a backstop. §4.2 makes it a mechanism.

**"Does this duplicate the walk scope?"** No — it makes the walk scope's own residual reachable. §5.1's note
says plainly that a per-type scope cannot remove a same-label `instanceOf` leg and that *"only the
pattern-DIRECTED walk … answers 'is V in this anchor's bound subgraph' exactly"*. This is that walk.

**"Does this introduce new state we already keep?"** Only `LastProgressAt`, and the cursor it derives from is
already published (`sweeper.go:287`). The licence itself is a boolean the host asserts at a site that already
asserts a sibling boolean.

**"Is the derivation's answer the right granularity for a personal lens?"** Yes: `ReprojectPersonalActor`
recomputes the **whole actor** and publishes an authoritative keyset frame (`reproject_personal.go:143-222`),
and the derivation's output at the anchor position *is* the actor set (`anchorWalkHead` pins
`(identity:identity {key: $actorKey})`, G14). Over-approximation costs an extra frame; under-approximation
costs a stale device (G17), which is why every unresolvable shape falls back.

---

## 7. Contract surface

**No frozen-contract change.** Contract #6 §6.14 defines the `cap-read.*` wire shape and the *"no entry = no
read"* promise, both untouched — this design changes when Refractor *recomputes* a projection, never what the
projection means or what a reader may conclude from it. No contract mentions personal lenses, the Interest
Set, or the anchor derivation, and none should: they are mechanism.

Docs owed in-fire (`docs/`, not contracts): `docs/components/refractor.md`'s derivation + personal-plane rows,
and the corrected §4.4 citation drift (G3) in the parent design.

---

## 8. Alternatives

**Row one, always: do not have this thing.**

| # | Alternative | Verdict |
|---|---|---|
| 0 | **Delete the personal derivation question — do nothing.** | Rejected on arithmetic. `edgeCatalog` is 128 k messages draining at ~3/min ≈ 700 h, and the cost is *per message*, so a busier graph does not amortise it. But note honestly what "do nothing" costs *after* §5.1: the walk is now pattern-scoped, so the floor is lower than the measurement that filed this row. Re-measure at Phase 0 before sizing Inc 3. |
| 0b | **Delete `patternClosedOutput` as a shared flag** and give each consumer its own predicate. | Partially adopted, and it is the shape of §4.4(b): the flag stays (it is a true, useful claim for actorAggregate lenses) and the *personal* licence is separate. Collapsing them into one flag would arm two consumers on one decision, which §17.2 already refused once. |
| 1 | **Rewrite the 15 personal lenses** to avoid the hub (the demand-side fix). | Rejected, but priced — this is the mandatory alternative whenever a consumer census is small. It fails on two counts: the census is 15, not single-digit, and the hub they cross is `instanceOf → vtx.meta`, i.e. Contract #1's type-descriptor edge, which every instance of every type carries. There is no rewrite that keeps `edgeInstances`' semantics (an actor's service instances *and their templates*) without traversing it. The lenses are correct; the refusal is the defect. |
| 2 | **Set `patternClosedOutput = true` for personal lenses.** | Rejected. The flag's definition is a factual claim that would become false, and it silently arms the delivery-side narrowing (a consumer reset to roll back) in the same edit. §4.6 sequences that deliberately. |
| 3 | **Merge each multi-walk lens back into one query** so the existing single-walk index applies. | Rejected — and this is a re-proposal of a ratified rejection, so quoting it: `refractor-shared-keyspace-arbitration-design.md` §13 found that *UNION concatenates row sets; it does not merge them*, so a per-actor multi-source lens emits two rows under one IntoKey — the exact last-writer-wins flap the per-walk-plus-merge shape exists to remove. The walks are the ratified design. |
| 4 | **Accept the sweeper as the Interest Set's coverage and skip Inc 1b.** | Rejected. It is the option that makes the licence's own argument refutable by measurement: *"the Interest Set is covered"* would rest on a 33 h-at-10 k-identities round-robin, and the next fire that reads it would conclude the plane is edge-covered when it is not. It is also cheap not to: the reprojector, keyed by identity, already exists and the three writers all know the identity. |
| 5 | **Raise `DefaultDerivationReadCap`** so hub-crossing walks succeed. | Irrelevant — the cap is not what refuses today; conjuncts 1/2/4 are, and none of them is a budget. |
| 6 | **Build a durable notification stream for both inputs** instead of in-process sinks. | Rejected here for the same reason the parent design rejected it (§8.2 of the grant-change design): a new stream, a new consumer and a new ordering problem to replace a synchronous call, with the multi-instance benefit only realisable once Refractor runs multi-instance at all. It is the right answer *at* that trigger — see §9 R4. |
| 7 | **Wire the interest edge straight to `ReprojectPersonalActor`**, bypassing the reprojector. | Rejected — a second path to the same effect with no coalescing and no drop accounting, i.e. a second lifetime (§5) and an unbounded fan-out under a flapping device. |

**Running each rejection back against the recommendation.** #4's objection is "an argument refutable by
measurement"; the recommendation's conjunct 3 is itself a measurement (sweeper progress), so it must be a
*liveness* test with a named bound rather than a claim — which is why it refuses on staleness instead of
asserting health. #1's objection is "the lenses are correct, the refusal is the defect"; Inc 3 must therefore
not smuggle in a lens-shape requirement — and it does not: it refuses on branch disagreement, a property of
the compiled indexes, not of the author's cypher style.

---

## 9. Risks

| # | Risk | Direction | Mitigation |
|---|---|---|---|
| R1 | The derivation under-approximates on a personal lens → a device keeps stale rows. | The bad direction, and on the personal plane a stale row is a **read the actor may no longer be entitled to**. | Every unresolvable shape falls back (§4.7's superset invariant, unchanged); conjunct 3's healer is the standing repair; the knob bounds the window. The differential test (§10) is the acceptance. |
| R2 | Inc 3's per-branch union has a branch-specific seeding bug that the single-walk tests cannot see. | Missed anchors on exactly the three biggest lenses. | The differential test runs **per branch and for the union**, against the enumerator, over the real corpus — not against a hand-built graph. |
| R3 | Inc 1b makes a flapping device's registrations a reprojection storm. | Availability, not correctness. | The dirty set coalesces per identity and is bounded at 10 000 with drop accounting (`reprojector.go:53`, `:338-341`); an interest change enqueues the *same* key a grant change would. |
| R4 | **The grant-change edge is in-process only** — with multiple Refractor instances a producer on instance A never reaches a personal pipeline on instance B. My first draft named this in prose and left conjunct 1 testing *"a reprojector is wired in this process"*, **which stays true on every instance while the edge stops spanning the deployment**. That is a fail-open at exactly the transition, in a design whose entire argument is fail-closed-by-default. | Silent, and in the over-grant direction. | **Now enforced, not narrated** (Andrew, at ratification — the same failure class the §13 adversarial pass caught in conjunct 3, which I did not then run back across its siblings). Conjunct 5 refuses the licence above one live instance; the build-time gate refuses the *transition* while the edge is still process-local. When multi-instance lands, alternative #6 (a durable signal) is a precondition of re-licensing, not an optimisation. Record it in `docs/components/refractor.md` too, not only here. |
| R5 | The 2-minute registry-ready hold means the first grant-change signal after every boot waits (`reprojector.go:186-198`). | Latency at boot. | Pre-existing, unchanged, and now shared by the interest edge. Worth one line in the operator doc; not worth a mechanism. |
| R6 | **Conjunct 3 is an availability cliff for the whole plane.** A Core-KV listing blip makes `ensurePopulation` fail, `Sweep` returns before recording a pass, and after `K` intervals **all 15** personal lenses drop back onto the relation-blind enumerator — the 20 s–190 s/message pathology §1.1 measured. | Pessimisation, in the safe direction, but abrupt and system-wide. | The direction is correct and must not be softened: a licence that survives its own healer being blind is not a licence. Bound the blast radius instead — `K` is generous (a listing blip is seconds, `K × 60 s` is minutes), the `off` knob is unaffected, and the cliff is an *operator-visible* refusal string, not a silent one. Named here rather than discovered live (§13 B3). |
| R7 | **Two narrowings compose.** The D1 gate's own producer, `capabilityRead`, is *itself* acting on this derivation today (it clears every conjunct of `derivationIndexForAct`). If the producer under-approximates, no grant is rewritten, so no transition fires, so the personal edge is silent — and after Inc 2 the personal lens's BFS accident is no longer there to re-ask the gate. | The accident was quietly serving as the other plane's second line of defence. | **Closed, but only because the producer's own convergence sweep announces** — both heal legs call `notifyGrantChange` with the outcome key, and the code says why: *"a retraction either of them heals is as real a grant withdrawal as one the CDC path writes"* (G19, `reproject.go:535-545`, `:643-645`). So the chain is producer-sweep-heals → guarded write → transition → personal reprojection. What genuinely changes is the **latency**: the repair is now paced by the producer's sweep rather than by the personal lens's next unrelated event. Measure it at close; it is a number the fire owes, not an unknown. |

---

## 10. Test strategy

- **Licence conjunct table** — one knock-out per conjunct, mirroring `TestPlainDerivationLicence_Conjuncts`
  (`anchor_derivation_plain_licence_internal_test.go:145`), including the never-progressed and stale-progress
  vectors and the stability of each refusal string across the window. *Owner: Inc 2.*
- **`TestCorpusPersonalDerivation`** — the standing rule: run the **real** analysis over every corpus cypher
  via `forEachCorpusCypher`, pin per lens `(licensed, indexReady, refusalReason)`, assert the population is
  exactly the 15 names with a floor on the count. A `refused → licensed` move on a lens you edited is the
  direction that needs §4.4's argument re-read. *Owner: Inc 2.*
- **Differential (superset) test, per branch and unioned** — the derived anchor set ⊇ the enumerator's, over
  the corpus, for all three multi-walk lenses. *Owner: Inc 3.*
- **Empty-reason regression** — a multi-walk lens's static refusal logs a **named** reason (G15). *Owner: Inc 3.*
- **Interest-edge e2e** — register → narrow interest → assert the device's next frame prunes the excluded key
  **without** any unrelated Core-KV event; deregister → assert the widening reprojection. Barrier on the
  **effect** (the row's own revision advancing), never on consumer pending (the dossier's settled-≠-finished
  entry). *Owner: Inc 1b.*
- **Shred announcement** — `DeleteAllForActor` over a live per-anchor set announces once per revoked key and
  zero times for already-tombstoned keys. *Owner: Inc 1a.*
- **Lint gate self-tests** — the `interest-change-posture` gate's own positive/negative vectors, run on every
  invocation, as `checkGrantChangePosture` already does. *Owner: Inc 1c.*
- **Producer-closure refusal** — a lens declaring a `cap-read.` output key space that does not satisfy
  `IsReadGrantProducer` is refused at registration, with the reason; plus the authoring-time gate's own
  vectors. *Owner: Inc 1d.*
- **Healer-verdict knock-outs** — the conjunct-3 vectors that broke the first draft: a pass in which every
  reprojection failed must NOT license (drive it by making `IsReadable` error); an unlistable population must
  NOT license; a never-run sweeper must NOT license; and `Run`'s immediate first pass must be observable
  rather than 60 s late. *Owner: Inc 2.*
- **Ordering-token latch** — the drain does not run while any registered personal pipeline's `LastAppliedSeq`
  is zero, proven by the narrowing scenario §4.2 names (register a narrowed filter 30 s after restart; assert
  the retraction lands rather than being consumed and dropped). *Owner: Inc 1b.*
- **Doc-mode shred announcement** — the `Control.NullifyRow` arm, not only `DeleteAllForActor`; and an
  unguarded adapter announces once per actor rather than not at all. *Owner: Inc 1a.*
- **`$now` conjunct** — a synthetic personal lens referencing `$now` is refused the licence (no corpus
  instance exists, so the vector is authored). *Owner: Inc 2.*
- **Cardinality conjunct, both staleness directions** — two live instances refuse; an unreadable count
  refuses; a stale entry from a crashed instance refuses (and that is asserted as *correct*, so a later
  "optimisation" that trusts freshness has a test standing in front of it). *Owner: Inc 2.*
- **The build-time gate's own vectors** — a synthetic queue-group/replica affordance fails the gate while the
  edge is process-local, and passes once the durable signal replaces it. Run on every invocation, since the
  tree ships no violating configuration for it to catch (the `lint-lens-anchors` precedent). *Owner: Inc 2.*
- **Build-tagged harnesses**: adding a method to the healer/sink interfaces reaches them — enumerate with
  `grep -rl "^//go:build " --include=*_test.go internal/` and run the ones the touched interfaces reach.

---

## 11. Decomposition for the Steward

One fire, four increments, each landing on `main` when green and each fail-closed to prior behaviour.

| Inc | Content | Posture-changing? | Review |
|---|---|---|---|
| 1 | **1a** shred announcement (both arms) · **1b** the Interest Set change edge + the ordering-token latch · **1c** the `interest-change-posture` gate · **1d** the producer-closure refusal | 1a–1c no (each *adds* a reprojection trigger — the safe direction); **1d yes** (it refuses an install that succeeds today) | lead, except **1d: cold adversarial** |
| 2 | The licence: healer conjunct → `standingHealerInstalled()`; `personalDerivationLicence`; `LastProgressAt`; corpus census | **yes** — a narrowing on the read-authorization plane | **full cold adversarial pass** |
| 3 | Multi-walk per-branch indexes + union + the named refusal reason | **yes** | **full cold adversarial pass** |
| close | Live verification + measurement + dossier classification | — | cumulative cold pass over the whole diff |

**Sequencing is not negotiable:** Inc 1 — *all four parts* — before Inc 2. The licence's conjuncts 1 and 2 assert edges that Inc 1
builds; landing Inc 2 first would license a narrowing on an edge that does not exist, which is the exact
fail-open this design was written to avoid. Inc 3 after Inc 2 because until the licence lands, Inc 3's change
is unreachable — but note it is *not* dead scaffolding in the harmful sense only because Inc 2 lands in the
same fire; if the fire is cut, cut it after Inc 2, never between 2 and 3.

**Live verification (MERGED ≠ RUNNING).** `make cycle-refractor` from the main checkout, then: the
`anchor derivation cannot act` log line no longer names the 12 single-walk personal lenses (Inc 2) nor the
three multi-walk ones (Inc 3); `nats consumer report KV_core-kv` shows `edgeCatalog` and `edgeInstances`
draining at ≥ 1 msg/s; the `anchor-derivation tally` line reports `acted > 0` per personal lens; a goroutine
profile shows no personal-lens handler inside `neighborsFromCoreKV` for a descriptor hub.

---

## 12. Board + doc corrections — APPLIED in this fire

All three are done, in the same commit as this design, because a correction that lives only in the newest
document is one the next reader will not find:

1. **The lane row's residual attribution.** `objectLiveness` / `objectAttachments` are **not** this row's
   territory (§1.1); the row is re-scoped and they get their own `📐` row for the untyped-hop refusal.
2. **`refractor-hub-walk-and-periodic-load-design.md` §5.1/§8's *"hydration for the Interest Set"*** is false
   (G8) and is struck where it stands, not merely superseded here — a later design grounding in that sentence
   would license a narrowing on an edge that does not exist. §8's `objectLiveness` line is corrected too.
3. **`auth-plane-projection-latency-design.md` §4.4's `personal.go` citations** had drifted far enough that
   the Interest-Set claim pointed at D1's own code (G3); both ranges are re-pinned with their call lines.

---

## 13. The adversarial pass — RUN (2026-09-01), and what it changed

A cold reviewer was briefed to break the draft, not to agree with it: the soundness claim, the two proposed
edges, Increment 3's union, the healer conjunct, every declared non-goal, and a spot-check of the §2 ledger.
It returned **3 blocking + 8 major + 6 minor**. All are folded above; none is deferred. What it changed:

| # | Finding | Fold |
|---|---|---|
| B1 | Two narrowings compose — `capabilityRead` is itself acting on this derivation, so the personal BFS accident was silently backstopping the *other* plane's narrowing | §9 R7. **Closed on the producer's own sweep**, which announces (G19); the residue is latency, and the fire owes the number |
| B2 | Conjunct 1 was structurally unsatisfiable read literally (a personal pipeline never holds a grant-change sink) and byte-identical to conjunct 3 read loosely — and the producer set is **open by construction**, discovered by a wildcard listing | Conjunct 1 rewritten (§4.4c) + **Inc 1d added**: the producer set is closed at install and at authoring. This is the finding that most changed the design's scope |
| B3 | Conjunct 3 was satisfiable while the healer healed nothing (unconditional progress stamp over a log-and-continue loop), through a population that excludes new actors, and zero for ≥ 60 s after every boot | Conjunct 3 became a **pass verdict** (§4.4c) with `failed`/`populationReadable`; `Run` sweeps immediately; the two residues named; R6 rewritten as the cliff it is |
| M4 | Inc 1a keyed on the wrong predicate (`OutcomeDeleter` ≠ liveness-deriving) and its fallback had no key to announce with | §4.1 rewritten: key on the guarded adapter; the fallback announces **once per actor** with the key the shred call already holds |
| M5 | `InterestReconciler takes the same sink` is an import cycle (`control` imports `health`) | §4.2: a bare `func(identityID string)` injected by `cmd/refractor`; G21 |
| M6 | Conjunct 3's plumbing is also a cycle (`grantchange` → `projection` → `pipeline`) | §4.4c + §5: a host-injected accessor, listed as new pipeline state |
| M7 | Inc 1b's transport drops the **narrowing** direction permanently in the post-restart window — `ErrNoOrderingToken`, no re-enqueue, actor already taken from `dirty` | §4.2: fix at the **latch** (`registryIsReady` gains an ordering-token conjunct), not with a retry queue |
| M8 | Three healer readers, not two — `oneKeyAnswerSound` is the third | §4.6: named as a deliberate non-goal, with why converging it here would arm a different narrowing unreviewed |
| M9 | The design never stated the edit that actually lifts the refusal, and the licence had no positive class conjunct — so §11's acceptance criterion was unreachable | §4.4a states the `:240` edit and the reason-switch reorder; conjunct 0 added |
| M10 | G6 overclaimed ("largest bulk revocation"), and §3.4's census pattern was blind by construction to the doc-mode sibling it existed to find | G6 corrected; §3.4 matches the call, not one receiver spelling, and now classifies its hits |
| M11 | No `$now`/`$projectedAt` conjunct, which the plain licence has | Conjunct 4 added, latent by design |
| minor ×6 | G13 stated a corpus property as structural · Inc 3's "shared read cap" not expressible against per-call locals · census globs unquoted (zsh) · G2 cited a closing brace · `edgeIdentity` mis-listed as walk-generated · G15's citation exact | all folded in place |

**Sections the pass could not break**, recorded so the next fire does not re-litigate them:

- **§4.6's delivery-side inertness claim is correct** — `labels.go:133-137` clears exhaustiveness on any
  `MinHops != 1 || MaxHops != 1`, which sets `reprojectAll` and makes `actorAwareNarrowingLabels` refuse at
  `rulestate.go:308`. Five `containedIn*0..` occurrences in the personal corpus.
- **The secure-lens / `piiKey` axis is structurally inert** — a personal lens cannot carry a decryptor at all
  (G20). The licence still owes no decryptor conjunct, but the *reason* is unreachability, which the draft had
  not stated and now does.
- **Conjunct 6 (anchor label == actor type) holds for all 15** — one hand-authored head, one generated head,
  both `MATCH (identity:identity {key: $actorKey})`.
- **Inc 3's per-branch union is a genuine superset for `edgeCatalog`**, verified through `AnchorSideSeeds`'
  both-endpoint seeding and `executeBranches`' per-actor re-run of every branch.
- **G1, G5 (as a census of today), G7, G8, G9, G12, G16, G17, G18** check out against the cited code.

---

## 14. Ratification (Andrew, 2026-09-01)

**Ratified, with one condition, folded above rather than filed.**

The question that carried it: *"this design, if implemented today, will help with performance of personal
lenses — but some (all?) of it will need to be redone when Refractor runs multiple instances?"* The answer is
in the For-Andrew table: it helps today, the perf-delivering half is largely transport-independent, and the
re-derivation is two licence conjuncts plus three announcement sites.

But answering it surfaced the defect. §9 R4 **named** the multi-instance hazard and conjunct 1 **did not test
for it** — it asserted a reprojector wired *in this process*, which every instance of a multi-instance
deployment satisfies while the edge reaches none of the others. A fail-open, in the over-grant direction, in a
design whose whole argument is fail-closed-by-default.

**What is now true:** conjunct 5 refuses the licence above one live instance (Health-KV derived, fail-closed
on unreadable, with the two staleness directions distinguished — the fail-open one is why the count is only a
backstop), and a **build-time gate** refuses the transition itself while the edge is still process-local. The
narrowing revokes itself when its premise expires instead of depending on a future author reading §9.

**The generalized lesson**, folded into `agents/designer/SKILL.md` §2 in the same commit: the §13 adversarial
pass had already caught this exact class in conjunct 3 — a predicate that reads healthy through the very
condition it exists to detect — and I fixed that one conjunct without running the finding back across its
siblings in the same table. A finding is a claim about a *class*; the fold is not done until every sibling
predicate has been evaluated against it.

---

## 15. Fire brief (build note, 2026-09-02)

**Fire branch:** `fire/personal-lens-derivation-licence` · worktree under `/tmp/lattice-worktrees/`. **Landing
shape:** each increment lands on `main` when green (§11) — every boundary is fail-closed to prior behaviour:
Inc 1a–1c only *add* reprojection triggers, 1d refuses an install shape the tree does not ship, Inc 2's
licence is refused until the host asserts conjuncts 0–2 and the healer reaches a verdict, and Inc 3 is
unreachable until Inc 2 lands. Never cut between Inc 2 and Inc 3.

### 15.1 Scope sentence (verbatim, §1.3 + §11)

> Make the personal plane's derivation refusal say what it actually requires, satisfy those requirements, and
> remove the two masked refusals — so a personal-lens event costs a handful of relation-filtered adjacency
> reads instead of an undirected expansion through a 3,913-degree descriptor hub. One fire, four increments,
> each landing on `main` when green and each fail-closed to prior behaviour.

Green bar: the `anchor derivation cannot act` log line no longer names the 12 single-walk personal lenses
(Inc 2) nor the three multi-walk ones (Inc 3); `edgeCatalog` / `edgeInstances` drain at ≥ 1 msg/s; the
`anchor-derivation tally` reports `acted > 0` per personal lens; no personal-lens handler sits inside
`neighborsFromCoreKV` for a descriptor hub.

### 15.2 Verified touch-list (two haiku scouts, live at `ec3058d8`; every §2 claim holds — line drift only)

| Inc | File | Anchor (live) | Edit |
|---|---|---|---|
| 1a | `internal/refractor/pipeline/pipeline.go` | `Delete` :1335-1336 (`p.currentAdapter().Delete`), `DeleteAllForActor` :1366-1383 (`adpt.Delete`) | guarded adapter → `DeleteWithOutcome` + `notifyGrantChange(outcome.Key, outcome.Transition)` per key; any other adapter → announce **once per actor** on `p.grantSink` with the shred call's `actorKey` (`Delete`'s doc-mode arm needs the actor: thread it from `Control.NullifyRow`'s caller, `keyshredded/manager.go:363`, which holds `ev.Payload.IdentityKey`) |
| 1a | `internal/refractor/adapter/{adapter.go,natskv.go}` | `DeleteOutcome` :220-233, `OutcomeDeleter` :307-318, `DeleteWithOutcome` natskv :267, `guardedWrite` :354 | discriminator = the **sequence-guarded** adapter (derives `Transition` from the stored body), NOT `OutcomeDeleter` — `GrantWriterAdapter` (`read_path_adapters.go:37`, `:166-171`, `:199`) satisfies the interface with `Transition` zero |
| 1a | `internal/refractor/pipeline/grantchange.go` | `notifyGrantChange` :135 (`p.grantSink == nil` guard :136), `truncateTarget` :184-199 | add the per-actor announce sibling; sink wired at `cmd/refractor/main.go:1446` only for `IsReadGrantProducer` |
| 1b | `internal/refractor/control/service.go` | `personalRegister` :1214-1229, `personalDeregister` :1234-1248 | nullable `func(identityID string)` field; call after the KV write succeeds |
| 1b | `internal/refractor/health/interest_reconciler.go` | orphan delete around :47+ | same nullable field; call per deleted registration's identity |
| 1b | `internal/refractor/grantchange/reprojector.go` | `GrantChanged` :312 (parses an identity **key**), `registryIsReady` :218-263, `SetRegistryReady` :195, `dirty` :97 / `DefaultMaxDirtyActors` :53, `reprojectActor` :437-471 (no re-enqueue), `ReprojectNow` :472 | add an identity-**ID** enqueue entry point onto the same coalescing dirty set (`GrantChanged` minus the key parse); extend the ready check so every registered personal pipeline reports `Progress().LastAppliedSeq != 0` (`reproject_personal.go:177`, `ErrNoOrderingToken` :188-193), keeping the 2-min `holdMax` |
| 1b | `cmd/refractor/main.go` | `grantchange.New()` :1371, `NewPersonalSweeper` :1382, `registerPersonalHealer` :1641, `cmd/refractor/personal_healer.go:20-22` | wire the two closures + the ready-check conjunct |
| 1c | `scripts/lint-conventions.go` | `grantChangePostureShape` :565, `checkGrantChangePosture` :3119-3155 | symbol→annotation table `{capabilityread.IsReadable( → grant-change-posture, personalinterest.IsRelevant( → interest-change-posture}`; identical findings + self-tests |
| 1c | `internal/refractor/projection/personal.go` | annotation :177-182, `IsReadable` :183, `IsRelevant` :194 | add `// interest-change-posture: (subscribed) …` above :194 |
| 1d | `internal/refractor/projection/driver.go` + `cmd/refractor/main.go:1446` | `IsReadGrantProducer` :377-389, :446-447; `patternClosedOutput = true` :502 | converse refusal at registration: output key space begins `cap-read.` ∧ ¬`IsReadGrantProducer` (or no sink wired) ⇒ install error naming the reason. Authoring gate: new `scripts/lint-cap-read-producers.go` (mirror `lint-lens-anchors.go`'s corpus enumeration + self-vectors), wired in `ci.yml` `lint-build` + `Makefile` beside `lint-lens-anchors` |
| 2 | `internal/refractor/pipeline/anchor_derivation_mode.go` | `derivationIndexForAct` :233-250; reason switch :202-215 | `if !p.patternClosedOutput && !p.personalDerivationLicensed()`; `p.sweeper == nil` → `!p.standingHealerInstalled()` (`walkscope.go:350-352`); licence refusal strings enter the switch **before** the `patternClosedOutput` default |
| 2 | new `internal/refractor/pipeline/anchor_derivation_personal.go` (+ `_internal_test.go`) | mirror `plainDerivationLicence` `anchor_derivation_plain.go:282-345` + `TestPlainDerivationLicence_Conjuncts` (`…_plain_licence_internal_test.go:145`) | conjuncts 0–5 of §4.4(c), stable refusal strings, read live (never snapshotted onto `ruleState`); `$now`/`$projectedAt` via `fullCR.ReferencesParam` exactly as the plain licence (:330-338) |
| 2 | `cmd/refractor/personal_healer.go` | `registerPersonalHealer` | assert conjuncts 0–2 + inject the verdict accessor (a `pipeline`-declared one-method value; `pipeline` cannot import `grantchange`, G21) |
| 2 | `internal/refractor/grantchange/sweeper.go` | `Run` :129-144 (bare ticker), `Sweep` :150-188, `publishProgress` :287-308, `ensurePopulation` :200-243 | per-pass verdict `{completedAt, attempted, failed, populationReadable, instanceCount, instanceCountReadable}`; `ReprojectNow` must report failure (today `reprojectActor` logs-and-continues); `Run` sweeps once immediately; instance count from a Health-KV listing of `health.refractor.*` (reader precedent `cmd/loupe/component.go:192`, key shape `health/lattice_heartbeater.go:2657`) on the sweep clock |
| 2 | `internal/refractor/health/idle_sweep_backoff_test.go:29` | `IdleSweepBackoffEvery*2 <= DefaultCapabilitySweepStallCycles` | pin `K` (verdict staleness intervals) the same way |
| 2 | new `scripts/lint-refractor-single-instance.go` | greenfield — scout item 16 found no gate inspecting deploy/config for a multi-instance affordance | fails on a Refractor replica/scale/queue-group affordance (`docker-compose.yml`, `Makefile`, `deploy/`, `cmd/refractor` consumer specs) while `grantchange` declares its edge process-local (a named exported constant the durable-signal build flips); self-vectors every run; wired in `ci.yml` + `Makefile` |
| 2 | `internal/refractor/personal_derivation_corpus_census_test.go` (new) | `forEachCorpusCypher` (`label_derivation_corpus_census_test.go:570`), verdict vocabulary `anchor_hopindex_corpus_census_test.go:45-66` | `TestCorpusPersonalDerivation`: per lens `(personal, staticLicence, indexReady, refusalReason)`; population exactly the 15 names, floor 15 |
| 3 | `internal/refractor/pipeline/ruleinstall.go` | exclusion :400-433 (`len(branches) <= 1` arm), walk scope over `all` :448-455 | `anchorHopsPerBranch []full.HopIndex` on `ruleState` (`rulestate.go:16-80`, `anchorHops` :28), built per branch; refuse whole if any branch `!Complete` / unresolved expansion / anchor labels differ; named reason constant added to the census vocabulary |
| 3 | `internal/refractor/pipeline/anchor_derivation.go` | `derivationIndex` :137-151, `walkToAnchors` :185-407 (locals `neighbours` :209, `reads` :211, `work` :244; `errDerivationTooWide` swallowed :376-378, :394-396), `affectedAnchors` :136 | thread one budget + shared memo through per-branch `walkToAnchors`; union; `seedAnchorLabels` / `rootHops` stay single-walk |
| 3 | `internal/refractor/pipeline/branchmerge.go` | `executeBranches` :78-106 | unchanged — every branch re-runs per derived actor (the union's soundness rests on this; pin it) |
| docs | `docs/components/refractor.md` :160-189, `auth-plane-projection-latency-design.md` §4.4 citations (G3), `refractor-hub-walk-and-periodic-load-design.md` | derivation + personal-plane rows; R4 recorded in the component doc |

**Census corrections pinned at Phase 0:** §3.1's second command needs `| grep -v _test` — the 5 hits it
returns today are all `internal/pkgmgr/*_test.go` fixtures; production population = 15, generated = 0. §3.2
census green at head. §3.3 as designed (1 / 1 / 18 / 0). §3.4 hits classified exactly as the design lists
(announce ×4, silent ×2, non-guarded fallback ×3, plus `results.go:71/:236` guarded and `:74/:239` fallbacks).

### 15.3 Precedents to mirror

- 1a announce-after-guarded-delete: `reproject.go:521-548` (the "as real a grant withdrawal" block).
- 1b nullable host-injected callback: `Reprojector.SetRegistryReady` (`reprojector.go:195`) + `SetPersonalPlaneHealer` (`walkscope.go:342`).
- 1c: `checkGrantChangePosture` itself — generalize, don't copy.
- 1d refusal-at-registration: the `secureColumns`-on-nats_kv refusal `lens/corekv_source.go:1515` (loud, named); authoring gate: `scripts/lint-lens-anchors.go` (self-vectors on every invocation).
- 2 licence + tests: `anchor_derivation_plain.go:282-345`, `anchor_derivation_plain_licence_internal_test.go:145`; refusal-latch `noteStaticPlainDerivationRefusal`.
- 2 cross-package constant pin: `health/idle_sweep_backoff_test.go:29`.
- 3 per-branch derivation: the walk scope's own `deriveWalkScope(…, all, …)` two paragraphs below the exclusion.

### 15.4 Increment order + green checks

1. **Inc 1 (a→b→c→d, one builder, opus — 1d is posture-changing):** `go test ./internal/refractor/... ./cmd/refractor/... -count=1`; `STRICT=1 go run ./scripts/lint-conventions.go` (self-tests incl. the new symbol); `go run ./scripts/lint-cap-read-producers.go`; tests per §10 rows *Shred announcement* (both arms + unguarded once-per-actor), *Interest-edge e2e*, *Ordering-token latch*, *Lint gate self-tests*, *Producer-closure refusal*. Cold adversarial review (opus) over the whole Inc 1 diff, briefed on 1d + the 1b latch.
2. **Inc 2 (opus):** licence conjunct table, healer-verdict knock-outs, `$now` vector, cardinality both staleness directions, build-time gate vectors, `TestCorpusPersonalDerivation`; `go test ./internal/refractor/... -count=1`; all lint gates. Full cold adversarial pass.
3. **Inc 3 (opus):** differential superset per branch + union over the corpus, empty-reason regression, `TestCorpusAnchorHopIndex` re-pinned; full cold pass.
4. **Close:** cumulative cold pass over the whole diff; `make cycle-refractor` (or the Makefile's recipe) from main; live §11 verification + R7 latency number; dossier classification.

Every increment: `go build ./...`, `make vet`, `golangci-lint run ./...`, `STRICT=1 go run ./scripts/lint-conventions.go`, plus every other `scripts/lint-*.go` the `ci.yml` `lint-build` job runs. Build-tagged harnesses reached: `internal/edge/store/grant_retraction_frame_test.go` (personal frames) — run its tag if `PersonalPipeline`/frame shapes change.

### 15.5 In-scope gotchas

- CLAUDE.md: no history comments; keys 4/6-segment; `natsfixture` only; no `time.Sleep`; the `# read-posture` posture applies to Starlark only (none here).
- `pipeline` ⇏ `grantchange`, `control` ⇒ `health` (G21): every cross-edge is a bare func/one-method value injected by `cmd/refractor`.
- The licence is read **live** at every gate evaluation, never snapshotted onto `ruleState` (`walkscope.go:76-84`'s reasoning).
- Refusal strings STABLE (no interpolated durations) — the latch logs once per reason.
- A verdict, not a progress stamp (B3); `failed > 0` must be observable from `ReprojectNow`.
- `oneKeyAnswerSound` (`actor_enumerator.go:394-402`) is the THIRD healer reader — **do not converge it** (§4.6, G22).
- `patternClosedOutput` stays false for personal lenses (never set it).
- New lint scripts must be wired in `ci.yml` AND the `Makefile`, self-vectored each run (`lint-lens-anchors` precedent) because the tree ships no violating fixture.
- Health-KV instance count: fail-CLOSED on unreadable; a stale crashed-instance entry refusing is asserted correct.
- **Refractor dossier (copied):** (1) a removal verdict's premises are the whole mechanism — a single-read verdict is sound only over an artifact never transiently absent; (2) new pipeline state without a declared lifetime — state table §5 is the record; (3) a soundness claim's stated reason is load-bearing; **a lifted refusal reveals the conjunct behind it, and a granted licence logs nothing** — prove the payoff by the POSITIVE verdict live; (4) an expansion sigil is fail-closed positive / fail-open negated; (5) a two-layer seam green at each layer and broken across it — interpose the real intervening step; (6) an upsert-only reprojection retracts nothing; (7) a SETTLED consumer has not finished — barrier on the effect; (8) fail-closed on delivery ≠ fail-closed on projection; (9) one latch guarding two states committing at different times; (10) an index read from one place and gated from another must agree about absence — `len(x)==0`, both vectors; (11) an authoring gate and its runtime resolver must agree — one shared predicate; (12) a liveness test must run the arm the consumer's `ProjectionKind` selects.
- **Standing checklist (template):** state needs a LIFETIME · every census/citation is a premise · a negative test needs its positive vector, every fix revert-proven (plumbing hardest) · removal needs transport + observer, a demoted mechanism enumerates every obligation · one deterministic key one writer · precedent may carry debt.

### 15.6 Adjacent finds (Phase 0)

None beyond the design's own §4.6 non-goals. Nothing filed.

### 15.7 Non-goals (drift fence)

§4.6 verbatim: the delivery-side consumer-filter narrowing; the untyped-hop refusal (own ✅ row); the
`WITH`-scope refusal (own 📋 row); `oneKeyAnswerSound`; multi-instance / HA Refractor (the gate refuses the
transition, it does not build the durable signal).

**Scope-diff gate:** every touch above traces to §1.3 / §11's four increments; no adjacent mechanism
substituted; dependencies both ways: Inc 1 → Inc 2 (conjuncts 1–2 assert Inc 1's edges), Inc 2 → Inc 3
(reachability). No unlisted load-bearing dependency found.

### 15.8 Checkpoint

*(amended per increment)* — **Inc 1 landed** (`cc42b5de`). **Inc 2 landed** (`0bac8278`). **Inc 3 landed**:
per-branch anchor indexes + the union under one per-event budget; the 7 multi-walk corpus rows flipped from
the no-branch-index refusal to ready (the payoff record in `TestCorpusPersonalDerivation`); the anchor-label
conjunct's uncovered disjunct pinned; the health RPC reports index readiness beside the licence. Review: full
cold pass (0 BLOCKING · 1 MAJOR · 8 MINOR), all closed. **Next: close** — cumulative cold pass over the whole
item, cycle `bin/refractor` + `bin/loupe` + `bin/bridge` from `main`, the §11 live verification, the R7
latency number, dossier classification, Done-log.
