# Refractor — anchor derivation across a variable-length hop, and the second refusal behind it

> **✅ RATIFIED — Andrew, 2026-08-27** (ratification session; DD re-verified by Winston in-session).
>
> **What it does, in two lines.** A lens whose cypher carries a variable-length hop is refused by
> `AnchorHopIndex`, so every CDC event on it falls back to an undirected adjacency BFS plus **one full
> cypher re-execution per actor the BFS reaches**. That is the throughput cost starving the auth plane.
> This design lifts the refusal — the executor already walks a ranged hop soundly; only the derivation
> refuses to — and then fixes the **second** refusal that sits behind it on the two lenses the live
> symptom actually names.
>
> **The demand row named one of two blockers, and the payoff it implies is not the payoff.** A
> conjunct trace against every one of the sixteen varlength lenses found that lifting the varlength
> refusal alone converts **one anchored lens and four plain ones** — and **neither of the two lenses in
> the live evidence**. `edgeManifestReadGrants` and `edgeManifestStaffReadGrants` are held by a
> *different* static conjunct (a `WITH` drops `container`/`home`/`role` and the next generated stage
> re-references it); five more are held by `patternClosedOutput`, a property of the Personal-lens
> **kind** that no cypher edit can reach; two never reach the derivation at all because they are
> multi-walk. §6 is the per-lens table. **Both blockers are in scope here** — fixing one and leaving
> the starvation would not be the item.
>
> **Decisions recorded at ratification:**
>
> 1. **Sequencing — Increment 1 ships first; Increment 2 is HELD until Increment 1 has run in anger.**
>    §13's ask is resolved in favour of the recommendation. `withScopeReject`'s posture is deliberately
>    conservative (*"over-refusing costs one BFS fallback, and under-refusing costs a grant that
>    outlives its revocation"*), and Increment 2 moves a `cap-read.edgeManifest.*` producer **onto** the
>    acted-upon path — the direction the corpus census itself calls *"the direction that needs an
>    argument."* The increments are code-independent (Inc 2 depends on Inc 1 only for the census
>    baseline), Inc 1 already relieves part of the live starvation (`capabilityServiceAccess` is one of
>    the two rebuilding lenses), and the wait buys a live measurement before the auth plane's
>    read-grant producers change paths. **Inc 2's revive trigger: Increment 1 shipped and observed live
>    (C4 re-derived after), with no derivation-soundness regression.**
> 2. **No architectural fork.** Every mechanism choice is resolved in the body on grounded cost.
> 3. **No frozen-contract change** (§11) — nothing to commit alongside; the derived set's superset
>    invariant and the fallback are unchanged.
>
> **Ratification-session DD (in addition to the two adversarial passes this doc already folded).** The
> load-bearing citations were re-opened and hold: the refusal and its stated reason
> (`hopindex.go:622-627`); the executor's ranged walk and its clamp (`rel_traverse.go:11-16`,
> `executor.go:29`); the read cap's *"fallback trigger, not a truncation"* contract
> (`anchor_derivation.go:25-33`); the completeness switch ordering varlength before `withReject`
> (`hopindex.go:243-278`), which is why the census masks the second blocker; `withScopeReject`'s posture
> quote (`withscope.go:1-26`); the generator's 1,000,001-row cross-product rationale (`anchorwalk.go`)
> and the `UNWIND` refusal (`visitor.go:146`) behind §10 F. The **retracted-precedent** finding is
> confirmed: `390b2cb8` deleted `internal/refractor/ruleengine/simple/` entirely, so the triage's cited
> precedent is a retracted claim in a deleted subsystem. Both census harnesses were **run green** in
> session (`TestCorpusAnchorHopIndex_PinnedConjuncts`, `TestScanRootCorpusCensus`), so §12 C1's counts
> are test-pinned, not asserted. `8dea6284` is confirmed as the prior cypher-rewrite precedent §10 D
> prices on both sides.
>
> **Standing obligation carried into the build:** Phase 0 of Increment 1 runs census **C3** and **stops
> if its expectation fails** — §6's payoff table and the increment split are derived from it. C4's live
> figures are a build-time re-derivation, before and after.

**Author:** Winston (Designer fire, 2026-08-27).
**Board row:** `[Refractor] A varlength-hop lens re-evaluates every actor per CDC event, starving the
auth plane` — ★★★. **Size: M–L → L.**
**Demand:** [lattice-designer-triage-2026-08-27.md §6](../../docs/reviews/lattice-designer-triage-2026-08-27.md).
**Parent:** `auth-plane-projection-latency-design.md`, which built `AnchorHopIndex` and left the
varlength population on the BFS by design.

---

## 1. Problem

`AnchorHopIndex` answers *"which anchors can this CDC event affect"* by walking the compiled pattern
graph backward from the changed element against live adjacency. When it can answer, a link mutation
costs a handful of relation-filtered adjacency reads. When it refuses, `affectedAnchors` falls back to
`ActorEnumerator.Enumerate` — an undirected adjacency BFS to depth 10, capped at 10,000 actors, and
**one full cypher re-execution and write per actor the BFS reaches**
(`pipeline/anchor_derivation_mode.go:119-175`; `pipeline/actor_enumerator.go:134-245`;
`pipeline/evaluate.go:849-916`). The parent design states the cost in its own opening: *"a booking, a
listing, a task all pay full price on the auth plane, and a single `holdsRole` link create re-projects
every co-holder of that role."*

One of the refusals is a variable-length hop (`hopindex.go:622-627`):

> `case r.MinHops != 1 || r.MaxHops != 1:` … "The intermediate NODES are the problem here, not the
> relation: a walk crossing a variable-length hop cannot be stepped hop-by-hop, because the number of
> adjacency reads between the two bound positions is not known from the pattern."

**That reason is refuted by the engine's own executor.** `traverseRel`
(`ruleengine/full/rel_traverse.go:11-123`) walks exactly this hop at evaluation time: a frontier BFS
from the bound node, hop 1..`maxHops`, each hop reading real adjacency filtered by `rel.Type` and
direction, per-path cycle dedup, admitting an endpoint at every hop `>= minHops`; an open `*0..`
(`MaxHops == -1`) or any range over 10 is clamped to `maxVarLengthHops = 10` (`rel_traverse.go:14-16`,
`executor.go:29`). The number of reads is not known from the pattern *as a single number* — it is a
**range**, and a range is walkable. The derivation refuses a shape its own executor traverses.

Live evidence (inherited from the triage's Health-KV read, 2026-08-27 20:53, and a **build-time
re-derivation obligation**, §12 C4): `edgeManifestReadGrants` and `capabilityServiceAccess` both
rebuild-in-flight with sweeps suppressed 15h16m and ~19 lenses lagging 8k–65k.

---

## 2. Grounding ledger

| # | Fact | Citation |
|---|---|---|
| 1 | The varlength refusal, and its stated reason | `ruleengine/full/hopindex.go:622-627` |
| 2 | The executor **does** walk a ranged hop: frontier BFS, per-hop relation+direction filter, per-path cycle dedup, admit at every hop ≥ `minHops` | `ruleengine/full/rel_traverse.go:11-123` |
| 3 | An open `*0..` or a range > 10 is clamped **per ranged hop pattern** to `maxVarLengthHops = 10` — *not* a whole-query bound | `rel_traverse.go:14-16`, `executor.go:29` |
| 4 | Refusal ⇒ `enumerate()` ⇒ undirected BFS to `DefaultActorMaxDepth = 10`, `DefaultActorMaxSet = 10_000`, **plus a cypher re-execution per reached actor** | `pipeline/anchor_derivation_mode.go:119-175`, `actor_enumerator.go:91-125`, `:134-245`, `evaluate.go:849-916` |
| 5 | The derivation walk's budget is `DefaultDerivationReadCap = 2_000` **adjacency documents**, and its own doc comment says it is *"a FALLBACK trigger, not a truncation: a walk that hits it returns `ok == false` and the caller runs the BFS. Truncating instead would silently return a subset, which is the one failure this whole unit exists to avoid."* | `pipeline/anchor_derivation.go:25-33`, `:171-280`, `errDerivationTooWide` |
| 6 | `DefaultActorMaxDepth` belongs to the **fallback BFS**, not to the derivation walk — the triage's proposed "bounded expansion under the existing `DefaultActorMaxDepth` cap" mis-locates the budget | `actor_enumerator.go:91-93`, `:196` vs `anchor_derivation.go:33` |
| 7 | `StepsFrom` prunes an edge whose far end's `OtherType` mismatches the pattern's `ToLabel`; `walkToAnchors` applies it at every step | `hopindex.go:869-895`, `anchor_derivation.go:253-264` |
| 8 | `traverseRel` applies `nodeMatches` **only at admission** (`hop >= minHops`) and extends the frontier unconditionally — intermediates are never filtered by the terminal pattern | `rel_traverse.go:100-116` |
| 9 | `Dist` has exactly **one** production consumer, `AnchorSideSeeds`' `consider`; its `ds < 0 \|\| dd < 0` branch seeds **both** endpoints, documented as *"Seeding both only widens the derived set, which is the safe direction"* | `hopindex.go:239`, `:349`, `:806-854` |
| 10 | `patternClosedOutput` is set by **`projection.InstallActorAggregate` only**; its doc records why a Personal Lens can never have it (*"consults two inputs outside the compiled pattern — the D1 read gate `cap-read.<domain>.<actor>` and the Interest Set"*) | `pipeline/pipeline.go:342-354`, `projection/driver.go:502` |
| 11 | `derivationIndexForAct` additionally requires `p.sweeper != nil`, never set on a Personal lens | `anchor_derivation_mode.go:219-229`, `projection/driver.go:311-326` |
| 12 | A multi-walk lens gets **no** `HopIndex` at all (`len(branches) <= 1`), so its census row is static-only | `pipeline/ruleinstall.go:411`, `:433`, `:443` |
| 13 | `withScopeReject` refuses *"a WITH dropped `%s` and a later clause re-references it"*, and its posture is stated: *"over-refusing costs one BFS fallback, and under-refusing costs a grant that outlives its revocation"* | `ruleengine/full/withscope.go:1-26`, `:68-78`, `:173-175` |
| 14 | The completeness switch orders `b.reject` (varlength) **before** `withReject`, so a lens with both reports varlength and the WITH refusal is masked | `hopindex.go:243-278` |
| 15 | `generateProducerSpec` emits **one stage per walk**, each closing with `WITH identity, <prior slices>, collect(...) AS grantSliceN`; four base walks re-open `chainResidence`, so `home`/`container` are dropped at one stage and re-bound at the next | `internal/pkgmgr/anchorwalk.go:548-583`, golden shape at `anchorwalk_test.go:103-116`, `chainResidence` at `packages/edge-manifest/lenses.go:468` |
| 16 | The staging exists to bound peak fan-out — nine base walks fused reached a **1,000,001-row cross product** | `anchorwalk.go:530-547` |
| 17 | The corpus census is executable and in-tree: **10 anchored** rows pinned `hopVarLengthHop` of 56 pinned, **6 plain** pinned `rootVarLengthHop` | `anchor_hopindex_corpus_census_test.go:80-137`, `plain_scanroot_corpus_census_test.go:109-172` |
| 18 | The **only** relation carrying a varlength hop anywhere in the corpus is `containedIn` | census C1, §12 |
| 19 | `ReferencedLabels` clears `exhaustive` on any varlength hop (*"intermediate hops bind arbitrary types"*), which governs **delivery** narrowing — a different axis this design does not change | `ruleengine/full/labels.go:135-138` |
| 20 | `capabilityServiceAccess`'s `exLoc` is deliberately unlabelled, and the reason is a ratified security property recorded in the lens's own comment and pinned by a test | `packages/service-location/lenses.go:130-147`, `ruleengine/full/service_location_lens_test.go:495-520` |
| 21 | Three lenses were converted **off** `containedIn*1..` to a single fixed hop on 2026-08-22, and the commit's own comment records that this was sound only because every live site wires `unit → building` directly, with an explicit warning that a property tier *"reintroduces a real chain and must revisit this lens"* | `8dea6284`, `packages/loftspace-domain/lenses.go:236-246` |

---

## 3. What the demand row got wrong, and what it got right

**Right:** the count. The two in-tree census harnesses — which run the *real* analysis rather than a
grep, per the Refractor dossier's standing rule — pin exactly **10 anchored + 6 plain** (ledger row
17). A text grep over-counts (21–26 raw hits) because it catches DDL and Starlark contexts and a
doc-comment sentence copy-pasted across nine files.

**Wrong, three ways, each material:**

1. **"Bounded per-hop-count expansion under the existing `DefaultActorMaxDepth` cap."** That cap
   belongs to the fallback BFS (ledger row 6). The derivation walk's budget is a *read* cap, and it is
   already exactly the right one — see §7.1.
2. **"The projection-plan compiler already lists bounded varlength as covered — the one ratified
   precedent nobody connected to `HopIndex`."** That precedent **does not exist**. The claim traces to
   Story 12.3's Dev Notes, whose *own code review* overturned it — *"Variable-length hops are compiled
   to a single 1-hop step and not flagged … ⇒ `Covered:false` (fail-closed for auth) … it doesn't
   today → reject"* — and the entire subsystem was later deleted as dead scaffolding (`390b2cb8`;
   `internal/refractor/ruleengine/simple/` is gone). It is a retracted claim in a retired subsystem,
   not an unconnected precedent. **The real precedent is `traverseRel`** (ledger row 2): same package,
   same relation semantics, shipped and tested, and it does exactly the walk this design needs.
3. **The payoff.** §6.

---

## 4. The shape — Increment 1: a ranged step in the pattern walk

### 4.1 The index

`PatternHop` gains `Min, Max int`. `addPattern`'s varlength arm stops refusing and instead records the
range; `hopindex.go:622-627`'s `rejectOnce` is replaced by the hop append. `PatternStep` carries the
range through `StepsFrom`, and the reverse reading (`h.To == pos`) carries the same range with the
direction already flipped by `edgeDirFor` — a bounded reachability relation is symmetric, so *"nodes
reaching Y in ≤k forward steps"* is exactly *"nodes reachable from Y in ≤k reverse steps."*

**The open range keeps the executor's clamp, applied per hop, never globally.** `Max < 0` or
`Max > maxVarLengthHops` clamps to `maxVarLengthHops`, matching `rel_traverse.go:14-16` exactly. This
is the soundness argument and it must be stated precisely: **the derivation is complete with respect
to what the executor will evaluate, not with respect to the graph.** An anchor whose path crosses more
than `maxVarLengthHops` of a ranged hop cannot produce a row, because the executor's own walk stops
there — so a derivation that stops there misses nothing that could have changed. Sharing the constant
is not a convenience; it is the invariant. A test asserts the two sites read the same constant.

### 4.2 The walk

`walkToAnchors` (`anchor_derivation.go:171-280`) handles a ranged step by an inner bounded closure
instead of a single edge move. Standing on `cur.id` at `cur.pos`, for a step with range `[min, max]`:

- if `min == 0`, enqueue `(step.ToPos, cur.id)` — the zero-hop admission, mirroring
  `rel_traverse.go:49-56`;
- expand a frontier over `step.Rel`/`step.EdgeDir` for hops `1..max`, reading through the existing
  `edgesOf` closure;
- at each hop `>= min`, enqueue the reached node at `step.ToPos`;
- carry a **closure-local** visited set so a cycle in the relation terminates.

**Three things this must not do**, each an under-approximation and therefore the unsound direction:

1. **It must not apply the terminal label to intermediates.** `StepsFrom`'s `ToLabel`/`ToExpanded`
   prune (ledger row 7) is correct at the *admission* node and wrong at every intermediate —
   `traverseRel` applies `nodeMatches` only at admission (ledger row 8). Pruning intermediates would
   drop paths the executor walks, i.e. drop anchors, i.e. drop a revocation.
   **This bug is invisible to the corpus** (§9), so it needs a hand-built fixture and a mutation test,
   not a census row.
2. **It must not clamp globally.** The clamp is per ranged hop pattern. `capabilityServiceAccess`'s
   positive path is `residesIn` + `containedIn(≤10)` + `availableAt` = up to **12** graph hops; a
   global depth-10 budget would under-approximate.
3. **It must not treat the closure as equivalent to `traverseRel`.** `traverseRel` carries a
   **per-path** `seen` set and enumerates paths; `walkToAnchors` carries a **global** `visited` keyed
   by `(pos, id)`. The derivation is therefore a **superset** in reachability, which is what the
   invariant needs (`anchor_derivation.go:9-13`). The design claims the superset, not equivalence.

**No new budget.** Every read in the closure goes through `edgesOf`, so `DefaultDerivationReadCap`
governs it and a breach returns `ok == false` ⇒ the BFS (ledger row 5). That is the correct failure
mode and it is already shipped and doc-commented: a walk that gets too wide degrades to today's
behaviour rather than returning a subset. It is also what bounds the genuinely expensive direction —
`edge-manifest`'s `(work)<-[:containedIn*0..]-(place)` walks *down* a containment tree, where the
fan-out is the descendant set rather than the ~1 ancestor chain.

### 4.3 `Dist`

A ranged hop's distance is an interval, so it must not make either endpoint appear **provably nearer**
— `AnchorSideSeeds` drops the far endpoint's seed when one side is strictly nearer. A position
reachable only across a ranged binding hop therefore takes the existing *incomparable* sentinel, whose
branch seeds **both** endpoints and is documented as the safe, widening direction (ledger row 9).
`Dist` has one production consumer, so the blast radius is that one call site.

Two things the increment owes because of it, neither optional: the sentinel currently means *"no
binding path to the anchor"* (`hopindex.go:59-65`), so **the field doc must distinguish the two
meanings** or the next reader misreads a genuine non-binding position; and
`hopindex_test.go:513`'s `Dist[s.Pos] >= 0` assertion for every seeded position has to move. A ranged
hop written inside a `WHERE` or a comprehension is `Binding: false` and already contributes no
distance, so only ranged hops in a `MATCH` are affected.

---

## 5. The shape — Increment 2: the refusal behind the refusal

The two lenses in the live evidence are `edgeManifestReadGrants` and `edgeManifestStaffReadGrants`.
Increment 1 does **not** convert them. They are generated by `generateProducerSpec`, which emits one
stage per walk closing with `WITH identity, <prior slices>, collect(…) AS grantSliceN`; four base
walks re-open `chainResidence`, so `home` and `container` are dropped at stage 0's boundary and
re-bound at stage 1's first clause (ledger row 15). `withScopeReject` refuses that (ledger row 13),
and the completeness switch orders the varlength refusal first, which is why the census reports
varlength and the WITH refusal is masked (ledger row 14).

`withScopeReject`'s doc names two distinct hazards behind one refusal:

> Where the name **heads** a pattern, `executor.matchPath` seeds it through `seedNodes`' whole-bucket
> scan; where it **merely appears again**, `hopIndexBuilder.position` merges two unrelated executor
> bindings into a single pattern position and the graph gains a hop that no single row ever walks.

The generated producer is in the **second** case and in its narrowest form: the re-binding clause is
headed by `identity`, which the `WITH` **carried**, and the re-bound occurrence is the *tail* of a
pattern that is **textually identical** to the earlier one — the generator emits the same
`chainResidence` string. So the merge adds no hop that was not already in the graph; it adds the same
hops twice.

**Increment 2 narrows the refusal to exactly that case:** a dropped name re-bound at a **non-head**
position of a pattern whose head is a **carried** variable, where the re-binding pattern is
**structurally identical** (same relation types, directions, ranges and labels along the path from the
carried head) to the pattern that bound it before, merges into the same position and adds no spurious
hop. Everything else keeps today's refusal.

**Why this is sound, stated against the direction that matters.** The refusal exists because a merged
position can make the graph *richer*, and a richer graph is safe for the walk itself (more hops ⇒ more
derived anchors ⇒ a superset) but not automatically safe for `AnchorSideSeeds`, where a spurious hop
could shift a `Dist` comparison and drop a seed. Under the structural-identity condition no hop is
added that was not already present, so no distance changes, so no seed is dropped. §7.3 carries the
argument in full and T9/T10 pin it.

**The alternative — change the generator instead — is priced and rejected in §10 (F).** In short: the
staged collect exists to stop nine walks multiplying into a 1,000,001-row cross product (ledger row
16), and grouping walks by shared chain prefix to avoid the drop reintroduces exactly the fan-out the
staging was built to bound. The engine-side narrowing changes no generated cypher and no row count.

---

## 6. The payoff, traced per lens

The claim *"this converts the varlength population"* is false, and the conjunct trace is how that was
found rather than discovered at build time. Making the index `Complete` is necessary and not
sufficient: `derivationIndexForAct` requires `patternClosedOutput` **and** `p.sweeper != nil` (ledger
rows 10–11), and a multi-walk lens gets no index at all (row 12).

### 6.1 The 10 anchored

| Lens | Kind | After Inc 1 | After Inc 1+2 |
|---|---|---|---|
| **capabilityServiceAccess** | actorAggregate | **converts** | converts |
| **edgeManifestReadGrants** | actorAggregate (generated) | still refused — `withReject` | **converts** |
| **edgeManifestStaffReadGrants** | actorAggregate (generated) | still refused — `withReject` | **converts** |
| edgeServices | Personal | refused — `patternClosedOutput`, `sweeper == nil` | unchanged |
| edgeEntityProviders | Personal | idem | unchanged |
| edgeEntityStudios | Personal | idem | unchanged |
| edgeEntityMenuItems | Personal | idem | unchanged |
| edgeStaffWorkOrders | Personal | idem | unchanged |
| edgeCatalog#0 | Personal, 3 walks | never reaches the derivation — `len(branches) > 1` | unchanged |
| edgeEntitySessions#0 | Personal, 2 walks | never reaches the derivation | unchanged |

**Five of the ten are held by a property of the Personal-lens *kind*, which no cypher edit can
reach** — the D1 read gate and the Interest Set are inputs outside the compiled pattern, so the
pattern's closure is genuinely not the whole story for those rows. Saying so is more useful than
leaving them looking one edit away. Two more are structurally out of scope until multi-walk lenses get
an index at all, which is a different item.

### 6.2 The 6 plain

Four convert and act — `cafeLeaseWorkplaces`, `menuCatalog`, `wellnessMembers`, `wellnessSessions`.
Two — `cafeIdentitiesRead`, `wellnessIdentitiesRead` — become `Complete` but are permanently held by
the plain **licence**'s `secureDecryptor == nil` conjunct, because both declare `SecureColumns`
(`packages/cafe-domain/lenses.go:132`, `packages/wellness-domain/lenses.go:137`). The plain arm does
**write** (`builtinDerivationMode` is `act`); "detect-only" in that file refers to the divergence
auditor behind the licence, not to the arm.

**A behavioural change rides along and must be built and tested deliberately, not discovered.** Making
those four `Complete` also flips `seedMultiPosition` (`anchor_derivation_plain.go:153-159`) true for
their anchor type, because a ranged chain's intermediate nodes are unlabeled and `PositionsBinding`
admits an unlabeled position for every type (`hopindex.go:167-184`). Every seeded anchor-typed event
then routes through `evaluateSeededMultiPosition` instead of the narrow single-seed call — a real hot-path
change, independent of whether the derivation then answers. T7 pins it.

### 6.3 What the payoff actually is

**Increment 1:** one auth-plane `cap.svc.*` producer + four plain business lenses off the BFS.
**Increment 2:** the two `cap-read.edgeManifest*` producers — the lenses in the live evidence.

Honest and narrow. The value is not breadth; it is that `capabilityServiceAccess` and the two
read-grant producers are authorization surfaces whose re-execution cost is what suppresses the sweep
and lags nineteen co-tenant lenses.

### 6.4 One narrowing that does *not* fire, checked rather than assumed

`ActorTypeBindsAnchorOnly` (`actor_enumerator.go:337-343`) also reads `Complete` and licenses a
one-key answer under a *different*, weaker conjunct set that omits `patternClosedOutput`. Making these
lenses `Complete` therefore arms a second mechanism. It does not in fact flip for any of the ten —
`capabilityServiceAccess`'s `PositionsBinding("identity")` returns three positions (`identity`, plus
the unlabeled `exLoc` and `op`, which `admitsType` admits for any type), failing `len(positions) == 1`;
the Personal lenses fail `sweeper != nil` first. **Stated because it is not free**, and pinned by T8 so
a later cypher edit cannot arm it silently.

---

## 7. Soundness

### 7.1 The budget, and why no new one is added

The ranged closure reads through `edgesOf`, so `DefaultDerivationReadCap = 2_000` bounds it and a
breach is `ok == false` ⇒ BFS. The design adds **no** cap, no depth budget and no truncation. This
matters because the corpus's expensive direction is real: `(work)<-[:containedIn*0..]-(place)` walks
*down* a containment tree. On a deep or wide tree the read cap, not the pattern, decides fallback —
and falling back is today's behaviour, so the worst case is "no better than now," never "wrong."

### 7.2 Polarity — the constraint this design must not break

The Refractor dossier's own entry: *"An expansion sigil is fail-CLOSED in a positive pattern and
fail-OPEN in a negated one — constraining the binder inside `NOT (...)` removes exclusions, i.e.
grants."* `capabilityServiceAccess`'s `exLoc` is deliberately unlabelled for exactly this reason
(ledger row 20), and its exclusion arm carries one of the two ranged hops.

**This design touches no label and no binder.** It changes only whether the *derivation* can walk a
hop the executor already walks. And the direction is structurally safe in both polarities: an
incomplete walk never narrows — it returns `ok == false` and the BFS runs. There is no path by which a
derived set is used while known to be partial. T5 pins the negated arm specifically: an event on a
node reachable **only** through the exclusion walk must derive the actor, because missing it leaves an
excluded service granted.

**A trap this creates for the next author, worth naming now.** Once bounded ranges are indexable,
"bound your `*0..` to gain indexing" becomes an attractive package edit. On a *positive* arm that edit
is fail-closed (a too-shallow bound drops a service). On a *negated* arm it is **fail-open** (a
too-shallow bound drops an exclusion, granting access). Same edit, opposite directions — a new
instance of the dossier's polarity class, one level up from the label sigil. The design does not need
that edit (§4.1 keeps the executor's clamp, so an open range is already indexable), and Increment 1
ships a `lint-lens-anchors` rule refusing a **narrowing range bound inside a negated pattern**, per the
lint doctrine: the gate that enforces a convention ships with the design that creates the temptation.

### 7.3 Increment 2's narrowing

The condition is: a dropped name re-bound at a non-head position of a pattern whose head is carried,
where the path from the carried head to that name is structurally identical to the path that bound it
before. Under it, `position()` merges two occurrences whose incident hops are already identical, so
`Hops` gains only duplicates. `Dist` is computed from `Hops`, so no distance changes; `AnchorSideSeeds`
is `Dist`'s only consumer, so no seed is dropped. The walk's reachability is unchanged, and
`StepsFrom` deduplicates identical steps.

Where any part of that fails — the head is not carried, the name heads a pattern, the path differs in
a relation type, a direction, a range or a label — the refusal stands verbatim. The narrowing is a
whitelist over a proven-identical shape, not a classifier, which is what keeps it on the safe side of
`withScopeReject`'s stated posture.

---

## 8. Reconciliation with the existing mental model

**"Didn't three prior designs already decide varlength can't be stepped?"** They did, and they all
gave the same reason. `auth-plane-projection-latency-design.md` §16.2: *"No variable-length hop
anywhere in the graph. The intermediate nodes are the problem, not the relation: a back-chain crossing
one cannot be walked hop-by-hop."* `typed-relation-signatures-design.md` §6: *"a signature does not
make it steppable."* `plain-lens-neighbour-anchor-derivation-design.md` repeats it. **The reason is
wrong, and the refutation is in the same package**: `traverseRel` steps it, forward, with a clamp;
the reverse walk is the same operation with the direction flipped. What none of the three had was a
reason to re-open it — the parent design shipped the mechanism and correctly left the population on
the sound-but-slow path; the live starvation is the driver that did not exist then.

**"Doesn't this duplicate typed relation signatures?"** No, and the split is the one that design drew
itself: it changes which events are **delivered**; this changes which anchors are **re-executed** once
an event arrives. Neither substitutes for the other. Concretely, `ReferencedLabels` still clears
`exhaustive` on a varlength hop (ledger row 19), so these lenses keep the broad delivery filter after
this design. That is the other half of the cost and it is not fixed here.

**"Why not just rewrite the cyphers?"** §10 (D) — and note it was **tried**: `8dea6284` converted three
lenses off `containedIn*1..` on 2026-08-22. Its own comment records the limit: it was sound only
because every live site wires `unit → building` directly, and it warns that a property tier
*"reintroduces a real chain and must revisit this lens."* The remaining population is the one where
transitivity is the semantics, not an accident.

**"Does this introduce new state?"** No. No registry, no cache, no latch, no durable key — the change
is two fields on `PatternHop`/`PatternStep`, an inner loop in `walkToAnchors`, and a narrowed predicate.
There is no state-lifetime table because there is no new state; that absence is itself the claim, and
T11 pins that the walk holds nothing across events.

---

## 9. The bug the corpus cannot catch

§4.2's first hazard — pruning intermediates by the terminal label — would under-approximate, and
**every one of the seventeen ranged-hop instances in the corpus is blind to it**: sixteen have an
unlabeled far end (so `ToLabel == ""` and nothing prunes), and the seventeenth
(`capabilityServiceAccess`'s `(loc0)→(loc)`) has both ends on the same `location*` expanded set with
intermediates that are all locations. A corpus-driven test would pass with the bug in.

This is the *"a guarantee that holds by accident of the corpus's shape"* class, and the answer is not a
census. Increment 1 ships a **hand-built fixture** — a ranged hop whose terminal label differs from its
intermediates' types — plus a mutation test asserting that adding the intermediate prune reds it (T3).
The same reasoning applies to the `min == 0` admission and the closure's cycle guard: both need
synthetic graphs, because the corpus's containment trees are acyclic and shallow.

---

## 10. Alternatives considered

**A. Leave it — the BFS is sound.** *Rejected by the live symptom*, which is the whole demand: a
15h-suppressed sweep and nineteen lagging lenses. "Slower, never wrong" stops being acceptable when the
slowness suppresses the verification that catches wrongness.

**B. Cap the derivation walk's depth with a new budget** (the triage's shape, under
`DefaultActorMaxDepth`). *Rejected.* It mis-locates the cap (ledger row 6), and a depth cap that
truncates would return a subset — the one failure `anchor_derivation.go:25-33` exists to refuse. The
existing read cap already gives the right shape with the right failure mode.

**C. Require packages to declare a bounded range** (`*0..` → `*0..N`) to gain indexing. *Rejected, and
it was the draft's first shape.* It is unnecessary — the executor already clamps an open range, so
sharing that clamp is exact — and it is actively harmful: it makes "bound your range" an attractive
edit, which on a negated arm is a fail-open security change (§7.2). Building a mechanism whose adoption
path is a security footgun is worse than the mechanism's absence.

**D. Rewrite the N cyphers off varlength** — the demand-breadth alternative, mandatory here because the
converting population is single-digit. *Rejected on semantics, with precedent on both sides.* It was
done for three lenses (ledger row 21) and worked because their walk was incidentally depth-1. The
remaining ones are different: `capabilityServiceAccess`'s transitive ancestry is Contract #6 §6.10
item 2 ("transitive availability") and collapsing it changes what the lens means; the two producers'
chain is `chainResidence`, the residence-reachability definition for the entire Facet manifest.
*Could a variant beat the recommendation?* A **denormalized transitive link** (`containedInRoot`)
would make every walk single-hop — but it needs a producer, a retraction path on re-parenting, and it
is a data-model change across four packages to avoid a two-field engine change. It also does not touch
Increment 2's blocker.

**E. Fix delivery instead** (typed relation signatures, so these lenses stop receiving every event).
*Not an alternative — the other axis*, shelved with its own revive trigger, and it does not reduce
re-executions once an event does arrive.

**F. Change the generator instead of `withScopeReject`** (Increment 2's alternative) — group walks by
shared chain prefix so `container` is never dropped. *Rejected on fan-out.* The staged collect exists
because nine fused walks reached a 1,000,001-row cross product (ledger row 16); grouping walks that
share `chainResidence` puts their tails back in one segment, which is the multiplication the staging
was built to prevent. Carrying `container` across a `WITH` instead would require re-expanding a
collected list, and the engine refuses `UNWIND` (`ruleengine/full/visitor.go:146`), so that form is
inexpressible. The engine-side narrowing changes no generated cypher and no row count. *Could a variant
beat it?* Grouping only the **two** walks whose tails are in a subset relation (`edgeServices` and
`edgeCatalog#1`, where the second extends the first by one hop) is genuinely safe and would remove one
of the four re-openings — but it removes one, not all four, so the WITH refusal stands and Increment 2
is still required. Worth doing later as a cost reduction, not as this design.

**G. Detect-only — measure how often the derivation would answer, ship nothing.** *Rejected.* The
census harnesses already give the static answer per lens, and the runtime question (how often the read
cap fires) is answered by the metrics Increment 1 ships anyway.

---

## 11. Contract surface

**No frozen-contract change.** This design changes which anchors Refractor re-executes after an event —
an internal performance property with no observable promise attached. Contract #6's capability-KV
envelope, its freshness semantics, and the read-set certificate are untouched; the derived set's
invariant (a superset of the affected anchors) is unchanged, and the fallback is unchanged.

Contract #6 §6.10 item 2's **transitive availability** is *relied on* by §10 (D)'s rejection but not
modified — that is the clause that makes collapsing `capabilityServiceAccess`'s walk a semantic change.

**Component doc** (`docs/components/refractor.md`, not frozen, committed with the build): the Rule
engine section gains the ranged-step walk; the `What's deferred` table's varlength line is updated;
the dossier gains the polarity entry from §7.2.

---

## 12. Executable censuses

**C1 — the varlength population.** Not a grep. Run the two in-tree harnesses, which drive every
installed cypher through the real analysis:
```
go test ./internal/refractor/ -run 'TestCorpusAnchorHopIndex_PinnedConjuncts|TestScanRootCorpusCensus' -count=1
```
*Run this fire:* 56 anchored rows pinned — 44 `hopIndexed`, **10 `hopVarLengthHop`**, 2
`hopUntypedHop`; **6** plain rows pinned `rootVarLengthHop`. Unit: **installed lens declarations after
walk expansion**, not source-file hop sites — a source grep reports 14 (or 25 uncorrected) and misses
the two generated producers entirely, which is where the live symptom lives.

**C2 — the relation set.** The design's claim that `containedIn` is the only relation carrying a ranged
hop anywhere:
```
grep -rnE '\-\[[^]]*\*[0-9]*\.\.[0-9]*[^]]*\]' --include='*.go' packages/ internal/pkgmgr/ | grep -v '//'
```
*Run this fire:* every executable hit is `containedIn`. Two hits in `internal/pkgmgr/anchorwalk.go`
are a doc comment and a negative test naming the **untyped** form `-[*0..3]` as inadmissible — the walk
grammar hard-refuses an untyped hop (`anchorwalk.go:1085-1090`), so no generated lens can carry one.
An earlier read of mine counted that comment as an executable site; it is not.

**C3 — the second refusal (Phase 0 of Increment 1 must run this).** The census reports the *first*
declining conjunct and the switch orders varlength before `withReject` (ledger row 14), so today's
table cannot show what is behind it. With the varlength arm disabled locally, re-run C1 and record the
new conjunct per lens. **Expected:** `edgeManifestReadGrants` and `edgeManifestStaffReadGrants` move to
`hopWithDropped`; `capabilityServiceAccess` moves to `hopIndexed`; the six plain move to indexed. **If
that expectation is wrong, §6's payoff table is wrong and the increment split has to be re-derived
before any code lands.**

**C4 — the live symptom.** Against a running stack, re-read Health KV for the capability pipeline's
rebuild/suppression state and the per-lens lag. The "15h16m suppressed, ~19 lenses lagging" figure is
**inherited from the triage's read and is a hypothesis about a state that may have moved.** It is the
acceptance measurement for Increment 2, so it must be re-derived before and after.

**C5 — registration sites** for the two new `PatternHop` fields and the shared clamp:
```
grep -rn 'maxVarLengthHops\|MinHops\|MaxHops' --include='*.go' internal/refractor/ | grep -v _test
```
Every hit is a scope line item — the AST, the visitor, `labels.go`'s exhaustiveness clear,
`relations.go`, `rel_traverse.go`, and the two census harnesses.

---

## 13. Decomposition for the Steward

**Increment 1 — the ranged step** (engine; converts `capabilityServiceAccess` + 4 plain lenses).
**Posture-changing: this is the one that warrants the full review pass.** Phase 0 runs census C3 and
stops if its expectation fails. `PatternHop`/`PatternStep` gain the range; the varlength `rejectOnce`
becomes a hop append; `walkToAnchors` gains the bounded closure with the zero-hop admission, the
closure-local cycle guard, and **no intermediate label prune**; the clamp is shared with
`rel_traverse.go` via one constant; `Dist` takes the incomparable sentinel across a ranged binding hop,
with its field doc split and `hopindex_test.go:513` moved. Census rows move in
`anchor_hopindex_corpus_census_test.go` and `plain_scanroot_corpus_census_test.go` — **a row moving to
indexed is the direction those harnesses say needs an argument, so each moved row gets one in the
commit message.** The `lint-lens-anchors` rule from §7.2 ships here. Metrics: the existing
`recordDerivationFellBack` counter, plus a ranged-closure read count so the read cap's firing rate is
visible. Tests T1–T8, T11.

**Increment 2 — the WITH narrowing** (engine; converts the two `cap-read.edgeManifest*` producers —
the live symptom). Depends on Increment 1 only for the census baseline, not for code. `withScopeReject`
gains the structural-identity whitelist (§5, §7.3). Tests T9, T10, T12. **HELD at ratification
(Andrew, 2026-08-27) — revive trigger: Increment 1 shipped and observed live (C4 re-derived after)
with no derivation-soundness regression.** The hold is deliberate and its reason is the security
posture, not capacity: this increment narrows a conservative refusal and moves a `cap-read.*` producer
onto the acted-upon path, so it buys a live measurement first. The Steward does not pull it until the
trigger is met.

**Explicitly out of scope, with the conjunct that holds each** (not deferred work — unreachable work):
the five Personal lenses (`patternClosedOutput`, a property of the lens kind); the two multi-walk
lenses (`len(branches) > 1`, a separate item if it is ever wanted); the two `SecureColumns` plain
lenses (`secureDecryptor == nil` in the plain licence). Naming the conjunct is what stops a later fire
re-filing them as one edit away.

**Sequenced behind a named trigger:** grouping the two subset-related base walks in
`generateProducerSpec` (§10 F's variant) — trigger: a measured cost from the redundant `chainResidence`
re-openings after Increment 2 lands.

---

## 14. Test strategy

| # | Proves | Shape | Inc |
|---|---|---|---|
| T1 | A ranged hop is indexed and the walk reaches anchors across it | synthetic 3-deep chain; assert the derived set equals the BFS's | 1 |
| T2 | `min == 0` admits the standing node itself | `*0..2` chain, assert depth-0 admission | 1 |
| T3 | **Intermediates are not pruned by the terminal label** | hand-built fixture whose ranged hop's terminal label differs from its intermediates' types; **mutation test**: adding the intermediate prune must red it | 1 |
| T4 | The clamp is shared, and applied **per hop, not per query** | `*0..` walked to exactly `maxVarLengthHops`; a 12-graph-hop pattern (residesIn + containedIn≤10 + availableAt) still derives | 1 |
| T5 | The **negated** arm derives | an event on a node reachable only through `capabilityServiceAccess`'s exclusion walk derives the actor — missing it leaves an excluded service granted | 1 |
| T6 | The read cap falls back rather than truncating | `SetAnchorDerivationReadCap(small)`, assert `ok == false` and the BFS runs; assert the derived set is never a strict subset | 1 |
| T7 | `seedMultiPosition` flips for the four converting plain lenses, and the multi-position path is correct | assert the flip explicitly, then equality against the single-seed result | 1 |
| T8 | `ActorTypeBindsAnchorOnly` does **not** arm for any converted lens | per-lens pin; a later cypher edit that would arm it fails here | 1 |
| T9 | The WITH narrowing admits the generated producer shape | the `anchorwalk_test.go` golden cypher indexes; assert the derived set equals the BFS's | 2 |
| T10 | The WITH narrowing refuses everything else | one vector each: head-position rebind, non-carried head, a path differing in relation type / direction / range / label | 2 |
| T11 | The walk holds nothing across events | two consecutive derivations on disjoint graphs; assert no carry | 1 |
| T12 | e2e: a `containedIn` link create reprojects only the affected actor for `edgeManifestReadGrants` | extend the capability e2e harness; assert the re-execution count | 2 |
| T13 | Corpus censuses move exactly as C3 predicted, and no other row moves | the two harnesses, with the argument per moved row | 1, 2 |

**Fixture discipline:** §9's classes need synthetic graphs — the corpus's containment trees are
acyclic, shallow, and same-typed, so they cannot exercise the intermediate-prune bug, the cycle guard,
or a differing terminal label. **Mutation discipline:** T3 and T6 are mutation proofs, not assertions;
where the claim is about *where* the label check sits, the proof is a **move**, not a revert.

---

## 15. Risks

| Risk | Direction | Mitigation |
|---|---|---|
| The ranged closure under-approximates and drops a revocation | **over-grant, the worst direction** | §4.2's three prohibitions; T3's mutation proof; the derived set is a superset by construction and an incomplete walk returns `ok == false` rather than a subset |
| A deep or wide containment tree makes the walk expensive | cost | The existing 2,000-doc read cap fires and the BFS runs — today's behaviour, never worse; the new metric makes the firing rate visible |
| A ranged hop shifts a `Dist` comparison and drops a seed | under-approximation | The incomparable sentinel seeds both endpoints (ledger row 9); one production consumer; T-level pin on the seed set |
| Increment 2 narrows a security refusal | over-grant | Structural-identity whitelist, not a classifier (§7.3); T10's one-vector-per-difference table; held per the banner |
| The four plain lenses' `seedMultiPosition` flip changes the hot path | behaviour change | T7 asserts the flip explicitly and pins equality with the single-seed result |
| A package "bounds its range to gain indexing" and fails open on a negated arm | over-grant | Not needed by this design (§4.1 keeps the clamp); the `lint-lens-anchors` rule ships in Increment 1 |
| `capabilityServiceAccess`'s expansion resolves to a present-but-empty set | derives nothing, reads as "no anchor changes" | Newly reachable once this lens converts; `ruleinstall.go:339-346` already warns and degrades the filter — the increment adds an explicit refusal rather than an empty derivation |
| The live figure has moved since the triage's read | the payoff is mis-sized | C4 is a build-time re-derivation, before and after |

---

## 16. Dossier entries this design is built against

From `docs/components/refractor.md` § *Review keeps catching*:

- *An expansion sigil is fail-CLOSED in a positive pattern and fail-OPEN in a negated one.* → §7.2:
  no label or binder is touched; T5 pins the negated arm; the range-bound variant of the same class is
  named and gated by the new lint rule.
- *A soundness claim's stated REASON is load-bearing, and a reason measurement can falsify is worse
  than none.* → §1 and §3 (2): the shipped refusal's stated reason is refuted by `traverseRel`, and the
  triage's cited precedent is a retracted claim in a deleted subsystem. Both are corrected in the doc
  the next reader will ground in.
- *New pipeline state without a declared lifetime.* → §8: no new state, and T11 pins the absence.
- *A removal verdict's premises are the whole mechanism — check the PROBED ARTIFACT.* → §6's per-lens
  conjunct trace, which is the same discipline pointed at a *conversion* verdict.
- *A standing rule: a new per-lens analysis ships its corpus census in the same fire, reusing
  `forEachCorpusCypher`.* → the censuses already exist; this design **moves rows in them** and §13
  requires an argument per moved row, per those harnesses' own instruction.
- *A two-layer seam can be green at each layer and broken across it.* → T12 runs the real intervening
  sequence end to end rather than asserting at each layer.

---

## 17. Increment 1 fire brief (build note, 2026-08-28)

Compiled at selection by two read-only scouts (`haiku`) + the lead's own Phase-0 census run. Branch:
`claude/great-lamport-kxa2su`.

### 17.0 Phase 0 — census C3, RUN, expectation HOLDS

C1 baseline green at head (`TestCorpusAnchorHopIndex_PinnedConjuncts`, `TestScanRootCorpusCensus`).
C3 probe: the varlength arm (`hopindex.go:622-627`) replaced by the hop append, both harnesses re-run
through a throwaway probe that prints every corpus lens's declining conjunct. Measured, against §12 C3's
stated expectation:

| Expected by §12 C3 | Measured |
|---|---|
| `edgeManifestReadGrants` → `hopWithDropped` | ✅ "a WITH dropped \`container\` and a later clause re-references it" |
| `edgeManifestStaffReadGrants` → `hopWithDropped` | ✅ "a WITH dropped \`role\` and a later clause re-references it" |
| `capabilityServiceAccess` → `hopIndexed` | ✅ indexed |
| the six plain rows → indexed | ✅ `cafeIdentitiesRead`, `cafeLeaseWorkplaces`, `menuCatalog`, `wellnessIdentitiesRead`, `wellnessMembers`, `wellnessSessions` all indexed |

The seven anchored rows §12 C3 does not name — the five Personal lenses and the two multi-walk ones —
also move to `hopIndexed`, which §6.1 already predicts and explains: the census pins the **index's**
conjunct, while what holds those seven is a *downstream* conjunct (`patternClosedOutput` /
`sweeper != nil` / `len(branches) <= 1`) that no cypher edit and no index change reaches. Their census
rows therefore move while their behaviour does not, and that is the argument each moved row carries.
No new row moves anywhere else in either census, and no row moves in the unexpected direction.

**Gate verdict: the expectation holds; §6's payoff table stands; the increment split is unchanged.**

### 17.1 Scope sentence (verbatim, §13)

> **Increment 1 — the ranged step** (engine; converts `capabilityServiceAccess` + 4 plain lenses).
> `PatternHop`/`PatternStep` gain the range; the varlength `rejectOnce` becomes a hop append;
> `walkToAnchors` gains the bounded closure with the zero-hop admission, the closure-local cycle guard,
> and **no intermediate label prune**; the clamp is shared with `rel_traverse.go` via one constant;
> `Dist` takes the incomparable sentinel across a ranged binding hop. The `lint-lens-anchors` rule from
> §7.2 ships here. Metrics: the existing `recordDerivationFellBack` counter, plus a ranged-closure read
> count. Tests T1–T8, T11, T13.

### 17.2 Verified touch-list (`file:line` re-checked live at compile time)

| File | Anchor | What changes |
|---|---|---|
| `ruleengine/full/hopindex.go` | `:616-630` `addPattern`'s rel switch | the varlength `rejectOnce` becomes a clamped hop append |
| " | `:38-51` `PatternHop` | `Min`, `Max` fields (clamped at build; `Max >= Min >= 0`) |
| " | `:900-915` `PatternStep` | `Min`, `Max` carried through |
| " | `:869-895` `StepsFrom` | `step()` carries the range, direction already flipped by `edgeDirFor` |
| " | `:59-66` `Dist` field doc | the sentinel's two meanings split |
| " | `:451-483` `distances()` | ranged binding hops contribute no distance; see 17.6 (a) |
| `ruleengine/full/executor.go` | `:26-29` `maxVarLengthHops = 10` | the one clamp constant, now read by the builder too |
| `ruleengine/full/rel_traverse.go` | `:12-19` | unchanged — it is the precedent, not a touch site |
| `pipeline/anchor_derivation.go` | `:214-276` `walkToAnchors` step loop | the bounded ranged closure |
| `pipeline/anchor_derivation_shadow.go` | `:267-302` | `RangedReads` tally + its log attr |
| `scripts/lint-lens-anchors.go` | `:72-100` `main`, `checkPackage` | the §7.2 negated-narrowing-bound rule |
| `internal/refractor/anchor_hopindex_corpus_census_test.go` | `:90-113` pin table | 10 rows move off `hopVarLengthHop` |
| `internal/refractor/plain_scanroot_corpus_census_test.go` | `:109-172` pin table | 6 rows move off `rootVarLengthHop` |
| `docs/components/refractor.md` | Rule engine §; What's deferred; dossier | ranged-step walk, deferred line, §7.2 polarity entry |

Citations that **rotted**: none. Every `file:line` in §2's ledger resolved to the quoted text.

### 17.3 Precedents to mirror

- The walk itself → `ruleengine/full/rel_traverse.go:11-123` (`traverseRel`): the clamp at `:14-16`, the
  zero-hop admission at `:48-56`, `nodeMatches` **only at admission** at `:38-46`/`:100-116`.
- Read budgeting → the existing `edgesOf` closure, `pipeline/anchor_derivation.go:198-212`; the fallback
  translation at `:227-232`.
- The tally → `recordDerivationActed` / `recordDerivationFellBack`,
  `pipeline/anchor_derivation_shadow.go:267-287`, and `logActSummaryIfDue`'s conditional attr at `:289-302`.
- The lint rule shape → `scripts/lint-lens-anchors.go`'s own `checkPackage` + `STRICT` exit gate.
- Census pin moves → the two harnesses' own instruction text (a move TO indexed needs an argument).

Nothing here is greenfield.

### 17.4 Increment order, each with its runnable green check

1. **Index** — `PatternHop`/`PatternStep` ranges, the clamped hop append, `StepsFrom`, `distances()`, the
   `Dist` doc split. → `go test ./internal/refractor/ruleengine/full/ -count=1`
2. **Walk** — the bounded ranged closure in `walkToAnchors`. → `go test ./internal/refractor/pipeline/ -count=1`
3. **Tally** — `RangedReads`. → same as 2.
4. **Lint** — the negated-narrowing-bound rule. → `STRICT=1 go run ./scripts/lint-lens-anchors.go`
5. **Tests T1–T8, T11** + the census pin moves (T13). →
   `go test ./internal/refractor/... -count=1`
6. **Docs** — `docs/components/refractor.md`.

Fire green bar: `go build ./...` · `make vet` · `golangci-lint run ./...` ·
`STRICT=1 go run ./scripts/lint-conventions.go` · `STRICT=1 go run ./scripts/lint-lens-anchors.go` ·
`go test ./internal/refractor/... -count=1` · `go test ./... -p 4` **with `POSTGRES_TEST_DSN` set**
(`agents/steward/REMOTE.md` §3 — without it the gated refractor corpus tests skip and the tree is falsely
green) · `make test-lease-convergence` (the convergence job's refractor arm).

### 17.5 In-scope gotchas + the standing checklist

The design's own §16 dossier entries bind (polarity fail-open under negation; a soundness claim's stated
REASON is load-bearing; no new state without a declared lifetime; check the probed artifact; a new per-lens
analysis ships its corpus census; a two-layer seam green at each layer). Beyond them, for this fire:

- **`labels.go:135-138` is NOT in scope.** `ReferencedLabels` keeps clearing `exhaustive` on a varlength
  hop; that governs delivery narrowing, a different axis (§8).
- **`relbinding.go:158,165-166`** already carries `varLength` + min/max for the executor's own binding — do
  not re-derive it, and do not couple to it.
- **No package/manifest version bump** — this fire edits no `packages/` content.
- **Build-tagged harnesses:** this change adds no method to an engine/service interface, so the tagged
  suites are not reached; the convergence targets still run because they exercise the refractor pipeline.
- Standing checklist: (1) **no new state** — the closure's visited set is per-step and dies with the call,
  and T11 pins it; (2) **every census is a premise** — C1 and C3 were re-run live above, not quoted;
  (3) **a negative test needs its positive vector first** — T3 and T6 are mutation proofs, and the T3
  mutation (adding the intermediate prune) is planted and restored in a scratch copy, never in the tree
  the lead commits from; (4) removal/replacement — nothing is removed; (5) one writer — n/a;
  (6) **precedent may carry debt** — `traverseRel`'s per-path `seen` differs deliberately from the walk's
  global `visited`; the derivation claims a **superset**, not equivalence (§4.2).

### 17.6 Deviations from the ratified body, decided at build time

**(a) `Dist` — the sentinel is applied to every position a ranged hop can reach, not only to positions
reachable *only* across one.** §4.3's literal rule ("a position reachable **only** across a ranged binding
hop takes the sentinel") leaves a hole the build closes: a position with *both* a fixed binding path of
length L and a ranged path whose true length may be shorter would keep the finite L, which **over-states**
its distance — and `AnchorSideSeeds`' `consider` drops the endpoint whose distance is larger, so an
over-stated distance can drop a seed. That is the under-approximating direction this whole unit refuses.
`distances()` therefore computes exact distances over **fixed** binding hops and then returns the
incomparable sentinel for every non-anchor position reachable from the anchor over binding hops **via at
least one ranged hop**. Over-poisoning only seeds both endpoints, which ledger row 9 records as the safe,
widening direction. §4.3's text is amended in place accordingly.

**(b) `hopindex_test.go:513`'s `Dist[s.Pos] >= 0` assertion does not move.** §4.3 predicted it would have
to. Measured: its fixture carries no ranged hop, so no position in it is poisoned and the assertion holds
verbatim. A ranged-hop equivalent is added beside it instead of moving it.

**(c) The clamp is applied at index-build time, in `full`.** §4.1 requires the derivation to share
`rel_traverse.go`'s clamp. Clamping inside `addPattern` (so `PatternHop.Max` is always concrete and
`Min >= 0`) puts the single constant read at one site in one package rather than exporting it to
`pipeline`; the shared-constant test asserts an open range's stored `Max` equals what `traverseRel` clamps
to. Same invariant, one reader.

### 17.7 Adjacent finds

None outside the fire's own mechanism at compile time. Anything the build or the reviews surface is fixed
in this run per `agents/steward/SKILL.md` §4, not filed.

### 17.8 Non-goals (the drift fence)

Increment 2 (`withScopeReject`'s structural-identity whitelist) — **HELD at ratification**, revive trigger
unmet. `ReferencedLabels`' exhaustiveness clear. The generator (`internal/pkgmgr/anchorwalk.go`). Any
cypher edit in `packages/`. The five Personal lenses, the two multi-walk lenses and the two `SecureColumns`
plain lenses (§13's named conjuncts — unreachable work, not deferred work).
