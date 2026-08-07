# Full-engine grouping-key reduction — a carried accumulator is not a grouping term

**Status: ✅ RATIFIED 2026-08-06 (Winston, under delegated authority)** · Designer fire, Winston, 2026-08-02

## Ratification (Winston, 2026-08-06 — delegated by Andrew)

Andrew delegated this class of decision in the ratify session: *"Winston can ratify — do what is right long
term, do NOT make decisions based on how many lines of code need to be changed."* Ratified on the
**mechanism**, not on the magnitude.

**Why it is right independent of the measurement.** A staging `WITH`'s carried accumulators are
**functionally determined** by the term the earlier stage grouped on, so re-rendering them into the
grouping key once per binding row recomputes a provably identical value per row. That is the engine doing
work whose result the grouping key already fixes — redundant by construction, not merely expensive today.
The size of the waste decides *when* to build it; it does not decide whether the shape is right.

**The headline measurement is unsourced, and the fire must produce its own.** §1 quotes *3.3 s / 7.2 GB
alloc at 5,009 anchors* from the board row, attributing it to `read-grant-single-source-walk-design.md`
§12 — **that section contains no such figures**, and no benchmark, commit message or other doc in the repo
carries them. §1's correction of the *units* (cumulative allocation is not resident heap; the row cap is
orthogonal to this cost) stands and is the right instinct, but it corrects the interpretation of a number
whose provenance does not exist. **Do not cite it as the acceptance criterion.** The fire establishes its
own before/after, and the instrument for it is the sibling design's Increment 2 (`peakBindingRows` plus
per-stage timing) — see the sequencing note below.

**Sequencing.** Build order across the engine cluster is
`lens-label-key-type-binding` → **this design** → `full-engine-independent-branch-decomposition`. The
label fire comes first because branch decomposition changes which clauses share a binding stream, which
the label design's Increment 2 soundness argument rests on; this design comes before branch decomposition
by both docs' own agreement (§8 there), so the `projectItems` edit lands on a loop that no longer
re-renders carried accumulators. Within the cluster, the observability increment ships early enough to
give this fire its before-number.

**Increment 2 (`WITH DISTINCT` honoured) is ratified with Increment 1.** `applyWith` ignores `w.Distinct`
today, and `wellness-ledger` is still the corpus's only `WITH DISTINCT` — a silently-ignored keyword is a
correctness defect regardless of how few authors have reached for it.
Owning component: **Refractor** (`internal/refractor/ruleengine/full`)
Board row: `backlog/lattice.md` → *[Refractor] A staging `WITH`'s carried accumulators are stringified
into the grouping key per row* — one row, both increments (§11). A separate residual row for
`applyReturn`'s `json.Marshal` DISTINCT is filed by this fire (§12).

---

## For Andrew

**What it does, in two lines.** A staged read-grant producer's `WITH identity, grantSlice0, …,
collect(…) AS grantSliceN` re-renders **every already-collected anchor map into a grouping-key string on
every binding row** — `rows_k × Σ_{j<k}|slice_j|` full renderings, measured at 3.3 s / 7.2 GB allocated
for one evaluation while the binding set sat at 0.3 % of its cap. The carried accumulators are
*functionally determined* by the actor the earlier stage already grouped on, so they contribute nothing to
the partition: a compile-time pass proves that and drops them from the key, leaving `identity` alone. The
projected rows are bit-identical; only the key string the engine builds internally changes.

- **No architectural fork.**
- **No frozen-contract change.** Contract #6 states no `WITH`/grouping semantics; nothing is staged
  uncommitted.
- **The security-plane consumer is immune to the failure mode by construction, not by care** (§4.4). The
  bad outcome of a wrong redundancy decision would be *merging two groups* — on a read-grant producer, an
  over-grant. But every generated producer's head is `MATCH (identity:identity {key: $actorKey})`
  (`pkgmgr/anchorwalk.go:72`), so an evaluation binds **exactly one** actor and every staging `WITH` has
  **exactly one group** whatever the key is. There is no second group to merge into. The general
  correctness argument (§4.1) still has to hold for every other lens; this is why the one lens class where
  a mistake would be a security defect cannot express the mistake.
- **Fail-safe default.** The analysis is a `map[Clause][]bool` computed at `Parse`. Absent or
  unproven ⇒ no item is dropped ⇒ today's behaviour exactly. Every "not proven" branch falls through to
  the current code path.
- **Adversarial pass: run this fire, findings folded (§10).** This design's own pre-build gate is
  discharged; nothing is deferred to the Steward. One finding reshaped the rule (§10.1 — the
  bare-`VariableRef`-only restriction turns out to be load-bearing for a second reason), and one killed
  the leading alternative (§9.2).
- **Inc 2 is a correctness gap found while grounding, not scope drift:** `With.Distinct` is set by the
  visitor (`visitor.go:172`) and **never read** by `applyWith`. `WITH DISTINCT` is silently a no-op today.
  It lives in the twenty lines Inc 1 edits, has one live consumer, and is ~10 lines to close. Folding it
  in rather than filing it follows the fewer-larger-fires rule; if you would rather it were its own row,
  say so and it splits cleanly.

---

## 1. Problem + intent

The vault's Refractor subdoc states the engine's cost promise directly: *anchor-first evaluation …
"ensures the evaluation cost is proportional to the change, not the whole graph"*
(`Obsidian Vault/Lattice/Lens and Refractor/The Refractor.md:24`). The defect here is a term that is
proportional to neither the change nor the graph, but to **the size of the result already accumulated,
multiplied by the row count of the stage still running**.

`read-grant-single-source-walk-design.md` §12 (2026-08-01) replaced the generated read-grant producer's
flat single-row-set emission with **one stage per walk** — the fix for a 1,000,001-row cross product that
had stopped a whole domain's read grants from refreshing. Staging works: peak rows fell to the largest
single walk's fan-out. The review pass that shipped it measured what remained and filed it as a row:

> `projectItems` normalizes every non-aggregating item per binding row, so a generated producer's stage
> *k* pays `rows_k × Σ|slice_j|` full renderings of already-collected anchor maps — measured **3.3 s /
> 7.2 GB alloc at 5,009 anchors** while peak rows sat at **0.3 % of the cap**.

**Two corrections to that framing, from reading the code rather than the row** (§2 grounding reflex):

1. **7.2 GB is cumulative *allocation*, not resident heap.** The fragments are transient strings; the
   process was never near an OOM. The harm is CPU, GC pressure, and therefore **throughput** — not memory
   safety. Any argument that the binding cap "should have caught this" is aimed at the wrong quantity, and
   §9.5 declines to build a cost-based governor on that basis.
2. **The row cap is not merely a loose bound here — it is measuring a different thing.** 0.3 % of the cap
   at 3.3 s is not a cap that needs tightening; it is a cap that is orthogonal to this cost. Stated so it
   does not get "fixed" by lowering `REFRACTOR_MAX_BINDINGS`, which would refuse legitimate evaluations
   and still not touch this term.

**Why throughput on this lens matters.** `edgeManifestReadGrants` and its two siblings are **auth-plane
actorAggregate** lenses producing the `cap-read.*` slices that Refractor's D1 gate consults before
projecting any Personal-lens row (`projection/personal.go` → `capabilityread.IsReadable`). The gate is
**fail-closed**: an anchor absent from the actor's slice means the row is silently dropped. A producer
that takes seconds per event falls behind under ordinary write volume, and a stale slice degrades exactly
into "rows silently disappear as the graph moves on" — the same user-visible failure §12 exists to remove,
reached by a third route (not the cap, not drift: throughput).

**Intent:** remove the row multiplier from the grouping key, and change nothing else.

## 2. Grounding ledger (verified `file:line`, this fire)

Every row cites the code that **does** the thing, never a comment describing it.

| Fact | Where |
|---|---|
| The grouping path renders **every** non-aggregating item per binding row: `keyParts = append(keyParts, fmt.Sprintf("%d=%v", i, normalizeForKey(v)))` | `ruleengine/full/executor.go:1129` |
| `normalizeForKey` on a `[]any` walks and renders **every element**, recursing into each map's sorted keys | `executor.go:1905-1912`, `:1891-1904` |
| …but a `*nodeRef` renders as a single length-prefixed key token — O(1) in the node's size | `executor.go:1913-1918` |
| The generated producer emits `WITH <actor>, <prior slices>, collect(DISTINCT {…}) AS grantSliceN` per walk | `pkgmgr/anchorwalk.go:549-559` |
| The producer's head binds **one** actor: `MATCH (identity:identity {key: $actorKey})` | `pkgmgr/anchorwalk.go:72` |
| The producer's `RETURN` is `actor.key AS actorKey, grantSlice0 + … AS readableAnchors` | `pkgmgr/anchorwalk.go:562-566` |
| …which contains **no** `FunctionCall`, so `containsAggregator` is false and `RETURN` takes the cheap non-grouping path | `executor.go:1205-1216`, `:1083-1100` |
| A projected value is written to the group row from the **first** row that created the group; later rows only fold aggregates | `executor.go:1133-1150`, `:1151-1158` |
| A pattern's first node, when its variable is already bound, **filters** — `heads = []binding{b}`, no rebind | `executor.go:474-494` |
| A traversal's destination variable, when already bound, must arrive at the same node or the row is dropped — no rebind | `executor.go:1023-1030` |
| `seedNodes` assigns a variable only on the branch where it was **not** bound (`heads == nil`) | `executor.go:496-511` |
| `nullBindNewVars` assigns only names **not already present** | `executor.go:412-433` |
| `cloneBinding` copies every name forward | `executor.go:1653-1659` |
| Exhaustive census of every write into a `binding` in the engine: `:420`, `:428`, `:505`, `:1033`, `:1095`, `:1656` (+ the fresh `g.row` at `:1134`). **No site overwrites an already-bound name.** | `executor.go` (grep of all binding assignments) |
| `projectItems` rebuilds each output binding from the projection **aliases alone** — an unprojected variable does not survive a `WITH` | `executor.go:1089-1097`, `:1133-1137` |
| `Parse` is the **only** production constructor of `*CompiledRule` | `ruleengine/full/full.go:108`; grep of `CompiledRule{` outside `_test.go` |
| `CompiledRule.KeyColumns` is already assigned **after** `Parse`, at activation — the struct is not treated as parse-immutable | `ast.go:250-257`; `executor.go:230` |
| `normalizeForKey`'s rendering is explicitly declared in-memory-only: "never persisted, never compared across processes" | `executor.go:1849-1853` |
| `collect(DISTINCT …)` dedupes by `normalizeForKey` **per collected element** — a separate, per-element consumer of the same function | `aggregate.go:111-117` |
| `applyReturn`'s `DISTINCT` dedupes by `json.Marshal`, not by `normalizeForKey` — a third, independent path | `executor.go:1227-1239` |
| `newAggFold` on a `BinaryOp` requires **both** operands to be aggregates; a bare `VariableRef` operand is `"unsupported aggregate expression"` | `aggregate.go:57-68` |
| `With.Distinct` is set by the visitor… | `visitor.go:172` |
| …and `applyWith` never reads it — `WITH DISTINCT` is a silent no-op | `executor.go:1042-1061` |
| `Return.Distinct` is set and **is** honoured | `visitor.go:189`; `executor.go:1227` |
| The only `WITH DISTINCT` in the corpus | `packages/wellness-ledger/lenses.go:250` |
| Per-evaluation latency is already recorded for the heartbeat aggregator — the signal to observe this exists | `pipeline/evaluate.go:240-252` |
| Pin: `nats-server v2.14.0` — untouched by this design (no substrate surface) | `go.mod:10` |

## 3. The mechanism, exactly

At staging `WITH` number *k* the clause's items are:

```
WITH identity, grantSlice0, …, grantSlice(k-1),        ← non-aggregating
     collect(DISTINCT {anchorType:…, anchorId:…, via:[…]}) AS grantSliceK   ← aggregating
```

`projectItems` takes the grouping branch (one item aggregates), and for **each of the `rows_k` binding
rows** produced by walk *k*'s chain it evaluates and renders every non-aggregating item
(`executor.go:1120-1131`). `identity` is a `*nodeRef` → one token. Each `grantSliceJ` is a `[]any` of
three-field maps (one of which is itself a `[]any` of relation names) → `normalizeForKey` walks the whole
list, sorts each map's keys, and appends a length-prefixed token per leaf.

Total renderings across the producer:

```
Σ_k ( rows_k × Σ_{j<k} |slice_j| )
```

Every one of them is discarded: the string is joined, used as a map key, and never read again. And the
value is **the same value in every row** — stage *k−1* collapsed to one row per actor, so all `rows_k`
rows descend from a single binding and share the identical slice header.

The three generated producers are the **entire** census of this shape. Every other multi-`WITH` lens in
the corpus (14 in `packages/edge-manifest/lenses.go`) carries only `*nodeRef` values, which render in
O(1) — so there is no second victim, and the generalization probe (§2 reflex: *the mechanism, not the
instance*) closes with a bounded answer rather than a hopeful one.

## 4. The shape — functional-dependence key reduction

### 4.1 The claim, and its preconditions

> **Claim.** Let `W_j` be a projecting clause with non-aggregating alias set `K_j` and aggregating alias
> set `A_j`. Let `W_k` (k > j) be a later projecting clause. If an alias `a ∈ A_j` appears in `W_k` as a
> **bare carry** — a `ProjectionItem` whose `Expr` is `*VariableRef{Name: a}` and whose effective alias is
> also `a` — and every alias of `W_j`'s *effective* key is likewise a bare carry in `W_k`, then removing
> `a` from `W_k`'s grouping key leaves the partition of `W_k`'s input rows **unchanged**.

*Why.* `W_j` emits one row per group, so `a` is a function of `K_j`. Between `W_j` and `W_k` a binding can
be **added to**, **filtered**, or **dropped at a `WITH`** — it is never **overwritten**: the exhaustive
binding-assignment census in §2 shows the only six write sites, and each either creates a fresh binding,
copies one, or is guarded by an already-bound check (`executor.go:474-494`, `:1023-1030`) that filters
instead of rebinding. So every row reaching `W_k` inherits its `(K_j, a)` pair unchanged from exactly one
`W_j` output row, and `a = f(K_j)`. Since all of `K_j` is retained in `W_k`'s key, `a` adds no
discriminating power. Dropping it can neither split nor merge a group. ∎

The claim's three preconditions, each pinned to code and to a test the fire adds:

| Precondition | Code | Test added |
|---|---|---|
| A bound variable is filtered, never rebound | `executor.go:474-494`, `:1023-1030`, and the §2 census | `TestExec_BoundVariableFiltersNeverRebinds` — a later `MATCH` re-referencing a bound variable both matches and mismatches; the value is unchanged in both outcomes |
| An unprojected variable does not survive a `WITH` (so the analysis need only reason about projected aliases) | `executor.go:1089-1097` | covered by the existing `auth-plane` Inc-0 work; asserted again here as a named property |
| A group's non-aggregating values come from the group's first row | `executor.go:1133-1137` | `TestProjectItems_RedundantItemProjectsFirstRowValue` |

### 4.2 The analysis

A single left-to-right pass over `Query.Clauses`, carrying two alias sets:

- `key` — aliases currently in the **effective** grouping key;
- `det` — aliases **functionally determined** by `key`.

For each projecting clause `C` with items `I`:

```
nonAgg   = { i : !containsAggregator(I[i].Expr) }
agg      = { i :  containsAggregator(I[i].Expr) }
carried  = { alias(i) : i ∈ nonAgg, I[i].Expr is *VariableRef{Name: alias(i)} }

if duplicate aliases in I            → redundant(C) = ∅; key = aliases(nonAgg); det = aliases(agg)   // fail-closed
else if key ⊄ carried                → redundant(C) = ∅; key = aliases(nonAgg); det = aliases(agg)   // fail-closed
else
    redundant(C) = { i ∈ nonAgg : alias(i) ∈ det ∩ carried }
    key = aliases(nonAgg) \ aliases(redundant(C))
    det = aliases(redundant(C)) ∪ aliases(agg)
```

`Match` clauses are skipped — they cannot rebind (§4.1), so they cannot invalidate a dependence.

**Inductive soundness.** `key ∩ det = ∅` by construction. `key_old ⊆ carried` and
`key_old ∩ redundant = ∅`, so `key_old ⊆ key_new` — every alias previously determined by `key_old` stays
determined by `key_new`, and each newly aggregated alias is determined by `C`'s actual grouping key, which
equals `key_new` because only determined items were dropped.

**Worked on the real generated producer** (14 declared walks across three domains,
`packages/edge-manifest/lenses.go:390-399`):

| Clause | items (non-agg / agg) | `carried` ⊇ `key`? | redundant | `key` after | `det` after |
|---|---|---|---|---|---|
| `W₀` | `identity` / `collect(…) AS grantSlice0` | ∅ ⊆ ✓ | ∅ | `{identity}` | `{grantSlice0}` |
| `W₁` | `identity, grantSlice0` / `… AS grantSlice1` | ✓ | `{grantSlice0}` | `{identity}` | `{grantSlice0, grantSlice1}` |
| `W₂` | `identity, grantSlice0, grantSlice1` / `… AS grantSlice2` | ✓ | `{grantSlice0, grantSlice1}` | `{identity}` | `{…0,1,2}` |
| … | … | ✓ | all prior slices | `{identity}` | … |
| `RETURN` | `identity.key AS actorKey`, `grantSlice0 + … AS readableAnchors` | — | — (no aggregator ⇒ non-grouping branch) | — | — |

**Effective key at every staging `WITH`: `{identity}`.** Accumulator renderings: **zero**.

### 4.3 Where it lives, and what carries it

Concretely traced end to end — no "resolved from context":

1. `full.go:108` — `Parse` returns `&CompiledRule{Query: v.query, groupingRedundant: analyseGroupingRedundancy(v.query)}`. Computed once, at the only production constructor; never mutated afterwards, so the shared compiled rule stays immutable at execute time (the constraint `read-grant-single-source-walk-design.md` §12 records for the footprint classifier's resolver).
2. `ast.go` — a new unexported field `groupingRedundant map[Clause][]bool` on `CompiledRule`. Unexported, so a hand-built test rule (`&full.CompiledRule{Query: q}`) gets `nil`.
3. `executor.go:225-237` — the executor copies the map into a field alongside `keyColumns`.
4. `applyWith` / `applyReturn` — each already holds its clause pointer, so each passes `ex.redundantFor(w)` (nil-safe) as a third argument to `projectItems`.
5. `projectItems(bindings, items, redundant []bool)` — in the grouping loop, an item with `redundant[i]` still **evaluates** (`groupVals[i] = v`, so the value is projected into the group row) but **skips the `keyParts` append**.

A new file `ruleengine/full/grouping.go` holds `analyseGroupingRedundancy`, mirroring how `labels.go`
holds `ReferencedLabels` — a compile-time analysis over the AST, in its own file, next to the executor
that consumes it.

### 4.4 What deliberately does **not** change

- **`evalExpr` still runs per row for a redundant item.** Only the *rendering* is skipped. This is a
  design constraint, not an oversight: an expression's evaluation is what populates the node/edge memos
  that `ex.footprint()` certifies (`executor.go:280-298`). Skipping evaluation would shrink the read
  surface a validating auth-plane caller compares after evaluation (`pipeline/evaluate.go:351-393`) and
  turn a footprint match into a mismatch. A bare `VariableRef` reads nothing, so the cost is a map lookup
  — and the footprint stays **bit-identical**.
- **Group order.** Groups are ordered by first appearance (`executor.go:1149`). The partition is
  unchanged, so first-appearance order — and therefore `collect` element order and output row order — is
  unchanged.
- **The key string is internal.** `normalizeForKey`'s own doc pins it as never persisted and never
  compared across processes (`executor.go:1849-1853`), so a shorter key has no external surface.
- **Item indices stay in the key fragments** (`fmt.Sprintf("%d=%v", i, …)`). Because each fragment is
  index-tagged, omitting item *i* cannot make two different item sets render alike; injectivity over the
  retained items is preserved without any change to `normalizeForKey`.
- **The three generated producers' rows.** Byte-identical output is the load-bearing assertion (§8).
- **The security plane cannot express the failure mode.** A wrong redundancy decision merges groups; on a
  read-grant producer that is an over-grant. Every generated producer binds exactly one actor
  (`anchorwalk.go:72`), so each staging `WITH` has exactly one group with or without the reduction. This
  is a structural fail-closed, in the sense §2 asks for — not a lint, not care.

## 5. Increment 2 — `WITH DISTINCT` is parsed and silently dropped

`visitor.go:172` sets `With.Distinct`; `applyWith` (`executor.go:1042-1061`) never reads it.
`Return.Distinct` is honoured (`executor.go:1227`), so the gap is asymmetric and invisible.

**Live consumer.** `packages/wellness-ledger/lenses.go:250`, `memberAccountsSpec`:

```
MATCH (bk:booking)-[:bookedBy]->(id:identity)
WITH DISTINCT id
OPTIONAL MATCH (id)<-[:heldFor]-(a:wellnessaccount)
RETURN id.key AS key, id.key AS identityKey, a.key AS accountKey
```

The lens's own comment states the intent — *"a member with many bookings still gets exactly one row"*.
Today it emits **one row per booking**, all identical, all keyed `id.key`. The rows are equal, so the
outcome is *N* redundant identical writes rather than wrong data (and `0ee30f6f`'s byte-identical-upsert
skip absorbs most of them), which is why nothing has surfaced. The latent risk is the one that matters:
**a future `WITH DISTINCT` feeding a `collect()` or `count()` would over-count silently**, because
de-duplication that the author reasonably believes happened did not.

**Fix.** In `applyWith`, when `w.Distinct`, de-duplicate the projected rows by the same
`normalizeForKey`-over-the-row-map identity the grouping path already uses, first occurrence wins —
mirroring `applyReturn`'s DISTINCT in behaviour while using the engine's **injective** renderer rather
than `applyReturn`'s `json.Marshal` (§12 residual). Ordering after `projectItems`, before the `WHERE`
filter, matches Cypher (`WITH DISTINCT … WHERE …` filters the distinct set).

**Interaction with Inc 1: none.** DISTINCT applies to the *output* rows of `projectItems`; Inc 1 changes
only how the *input* rows are partitioned. On a clause that both aggregates and declares DISTINCT, the
aggregation already collapses to one row per group and DISTINCT over distinct group rows is a no-op —
correct in either order.

## 6. Reconciliation with the existing mental model

**"Didn't we already fix the read-grant producer's cost?"** §12 fixed the **row count** (a 1,000,001-row
cross product → the largest single walk's fan-out). It did not touch the **per-row work**, and the
per-row work turned out to scale with the *accumulated result*, which staging is precisely what created:
before staging there were no carried accumulators, because everything collected in one `RETURN`. This is
therefore a cost §12 introduced while removing a larger one — worth saying plainly rather than filing as
an unrelated residual.

**"Doesn't the binding cap bound this?"** No, and §1 explains why the two quantities are orthogonal.
0.3 % of the cap at 3.3 s is the evidence.

**"Does this duplicate the ratified auth-plane latency design?"** No, and they compose. That design
(`auth-plane-projection-latency-design.md`, ✅ 2026-08-01) attacks `intake × enumerate × reproject` — how
*many* evaluations run. This attacks what **one** evaluation costs, on a lens family that design's
Increments 1–3 will keep feeding. Neither changes the other's terms.

**Where the two designs touch the same file, and why they do not collide.** That design's Increment 0
hardens `ReferencedLabels`, whose unsoundness is that `projectItems` **rebuilds each binding from the
projection aliases alone**, so a variable dropped at a `WITH` re-seeds through an unlabeled scan
(`executor.go:1085-1098`, `:654-679`). This design changes **which items contribute to the grouping key**
and changes **no alias that any clause projects** — the set of surviving bindings after every `WITH` is
identical. `ReferencedLabels`' input is therefore untouched, in both the shipped and the Increment-0
form. Build order between the two is free.

**"Does it disturb the projection-divergence audit?"** `lens-projection-divergence-audit-design.md`
(📐 awaiting-Andrew) recomputes a row and compares it to the projected one. This design's whole contract
is that recomputed rows are **bit-identical** before and after, which is exactly the property that audit
depends on — and §8's equivalence test is what proves it.

**"Does this introduce new state?"** One immutable, parse-time-derived `map[Clause][]bool` per compiled
rule — the same kind of state `KeyColumns` and `ReferencedLabels` already are. No runtime state, no KV, no
config, no environment variable.

**"Is this a lens/package problem rather than a platform one?"** No. P5 says a missing read-model is
package work; this is not a missing projection but the **engine's execution of an existing one**. And the
generator-side variant of the fix is not expressible without an engine change anyway (§9.2), which
settles the classification rather than assuming it.

## 7. Contract surface

**None.** No `docs/contracts/*` section states `WITH`/grouping semantics, the shape of a projected row, or
any engine-internal key encoding — checked across all 16 contract docs. Contract #6 §6.13/§6.14 fix the
*output* shape of an actorAggregate read-grant producer (`cap-read.<domain>.<actorSuffix>`,
`readableAnchors[]`), and that output is unchanged by construction. **Build to them; nothing staged
uncommitted.**

## 8. Test strategy + the gate that binds the next change

Three layers, all deterministic, none timing-based:

1. **Equivalence over the real generated producers (the load-bearing assertion).** In `package full`, the
   analysis map is settable, so the same parsed rule can be run twice — once with the map as computed,
   once with it forced `nil` (today's path) — over the seeded corpus that
   `read_grant_producer_staging_test.go` already builds, asserting the `[]ProjectionResult` are **equal in
   order and content**, for all three real domains. This mirrors the precedent that fire established
   (`TestReadGrantProducer_StagedMatchesFlatAnchorSet` compares two emissions over one corpus) rather than
   inventing a new proof style.
2. **Randomized differential.** The same on/off comparison over N randomized actor-rooted corpora ×
   the three domains, with the adversarial shapes §12's review already exercises (shared prefixes,
   multi-parent `containedIn`, zero-hop `*0..`, randomly-empty branches), plus randomly-generated small
   multi-`WITH` queries to exercise the analysis's fail-closed branches (renamed carries, duplicate
   aliases, a non-carry expression for a key alias).
3. **The structural gate — this is what binds the next change.** A test asserting that for each of the
   three generated producers, `analyseGroupingRedundancy` yields an **effective grouping key of exactly
   `{identity}` at every staging `WITH`**. It is a pure function of the generated cypher: it fails the
   moment `generateProducerSpec` emits a shape the analysis cannot prove (a renamed carry, an extra
   non-determined column), *and* the moment the analysis regresses. A timing or allocation assertion would
   be the flaky, weaker version of this; the structural form is deterministic and states the actual
   invariant.

   Per the lint doctrine, the gate ships **in the same fire** as the change, not as a follow-on. It is a
   test rather than a `scripts/lint-*.go` gate because the invariant binds the **engine and the
   generator**, both of which live in Go under test — there is no authoring surface for a package author
   to get wrong, so there is nothing for a `packages/**` scan to default-deny.

4. **Inc 2:** a `WITH DISTINCT` unit test (duplicates collapse; `WHERE` applies to the distinct set;
   DISTINCT before an aggregate does not double-count), plus a `memberAccountsSpec` lens test asserting
   one row per member over a corpus with several bookings per member.

**Measurement, reported not asserted.** A `Benchmark` over the staged producer at ~2 k and ~5 k anchors,
run quiet, with the before/after `ns/op` and `B/op` recorded in the build note. Bounding the claim
honestly: what is removed is the `rows_k ×` multiplier. What remains — the walk traversal, the
`Σ_k |slice_k|` per-element `collect(DISTINCT)` signatures (`aggregate.go:111-117`), and the adapter write
— is unchanged. The measured 3.3 s is an upper bound on the saving, not a promise that the evaluation
becomes free.

**Migration / compatibility.** None. No stored data, no contract, no lens re-authoring, no package version
bump — the change is confined to the engine binary. `lint-package-version`'s generator-content rule
(§12's gate-gap fix, which treats a change to `internal/pkgmgr/anchorwalk.go` as content for every
walk-declaring package) is **not** triggered: this fire does not touch `anchorwalk.go`. Worth stating
explicitly so the fire does not bump `edge-manifest` for no reason.

## 9. Alternatives considered

### 9.1 Per-item value memo (cache the rendered fragment by value identity)

Keep a one-entry-per-item cache of `(last value, last fragment)`; on a pointer-identical value reuse the
fragment. Fully general and needs no analysis.

**Rejected — twice over.** (a) Soundness rests on *"no container held in a binding is ever mutated
in place"* — a whole-package invariant across every expression evaluator, unenforceable and untestable,
against a theorem about two guarded assignment sites that §2's census closes exhaustively. (b) It does not
even fix the measured case: the row key is `strings.Join` of `fmt.Sprintf("%d=%v", i, frag)`, so the huge
fragment is still **concatenated into a per-row string**, keeping `rows_k × Σ|slice_j|` *bytes* of
allocation. Making it work needs a second mechanism (interning fragments to small ids) on top. Two
mechanisms where one suffices, guarding a weaker invariant.

### 9.2 Fix the generator instead of the engine

Have `generateProducerSpec` avoid carrying accumulators — e.g. `WITH identity, acc + collect(DISTINCT {…})
AS acc`, so the accumulator is inside an *aggregating* item and never enters the key at all.

**Rejected, and the reason is a mechanism I opened rather than assumed** (§2's "verify it can BE
reshaped"): `newAggFold` on a `BinaryOp` calls `newAggFold` on **both** operands, and a bare `VariableRef`
operand returns `"unsupported aggregate expression"` (`aggregate.go:57-68`). The engine cannot execute the
shape. Making it work means teaching `binOpFold` to accept a constant-per-group non-aggregate operand —
*more* engine surface than §4, plus a generator change, plus re-opening the
`hasMultiBindingConjunctUnit` classification that §12 records as the landmine staging already tripped
once. Strictly worse on every axis. (Cypher also does not permit an item to reference its own alias, so
the shape is dubious on the language side too.)

Note this alternative was the one that looked like "the simplest extension of what already exists" —
the generator is a smaller, newer surface than the executor. Reading `aggregate.go` is what settled it.

### 9.3 Hash the key instead of rendering it injectively

Replace the string key with a 64/128-bit hash, or hash the large fragments only.

**Rejected on the security plane.** `normalizeForKey`'s doc is explicit that a collision "silently merges
two groups… data loss with no error anywhere" (`executor.go:1854-1859`), and this function is the
grouping identity for lenses whose output **is** the authorization surface. Replacing a proved injectivity
with a collision-probability argument is the wrong direction, at any width. §4 keeps the exact renderer
and simply stops calling it on terms that cannot discriminate.

### 9.4 Do nothing / raise `REFRACTOR_MAX_BINDINGS`

**Rejected.** The cap is orthogonal (§1); raising or lowering it changes nothing here. Doing nothing
leaves a seconds-per-event term on a serially-consumed auth-plane lens whose staleness is a fail-closed
read gate silently dropping rows.

### 9.5 A cost-based evaluation governor (bound work, not rows)

Tempting framing: the cap counts rows, cost is `rows × value size`, so govern on cost.

**Deferred, and honestly.** After §4 the observed instance is gone, and the 7.2 GB was cumulative
allocation rather than resident heap (§1), so the "the process was at risk" premise that would justify
authoritative machinery does not survive grounding. Per the dead-scaffolding test, an increment with no
consumer and a weakened premise is not built. **Named trigger to revive:** a lens whose *peak resident*
heap during one evaluation is observed to threaten the Refractor process — measurable today, since
per-evaluation latency is already recorded (`pipeline/evaluate.go:240-252`).

### 9.6 Generalize the reduction beyond bare carries

The analysis could, in principle, prove functional dependence for non-carry expressions (a
`PropertyAccess` on a key variable, a deterministic function of key items).

**Deferred.** The census in §3 is complete: outside the three generated producers, every multi-`WITH` lens
carries only `*nodeRef` values, which render in O(1). There is no consumer for the generalization, and the
bare-carry restriction is load-bearing for a second, independent reason (§10.1). **Named trigger:** a lens
carrying a large non-`nodeRef`, non-carry value across a grouping `WITH`.

## 10. Adversarial pass — run this fire, findings folded

Run as a structured walk of the Designer skill §2 reflex list against the draft, one at a time, per the
2026-08-01 lesson that a freshly-added reflex still gets missed when it is recalled rather than executed.
Findings that changed the design:

### 10.1 The bare-`VariableRef` restriction is load-bearing for a *second* reason (reshaped §4.2)

The draft justified restricting redundancy to bare carries purely by the theorem. The adversarial question
was: *what diagnostic property does the current per-row rendering provide that dropping it removes?* The
`nodes` memo's own doc (`executor.go:82-94`) answers it — a non-repeatable read makes one column yield two
values, **which splits a group**, which surfaces as two rows sharing an output key and the pipeline's
collision guard failing the actor closed. That split is how a repeatable-read violation *becomes visible*.
Dropping an item from the key would mask it.

It does not bite, and the reason is the restriction: a redundant item is by definition a bare
`VariableRef`, whose value is a **map lookup on the binding**, not a KV read. There is no read to be
non-repeatable. Every item that actually reads Core KV stays in the key and keeps its diagnostic power.
Generalizing to `PropertyAccess` (§9.6) would break this — which is now recorded as a reason, not just a
scope note.

### 10.2 The footprint must stay bit-identical (added the §4.4 constraint)

The obvious "optimization" is to skip evaluating a redundant item entirely, since its value is only needed
once. That would change the evaluation's **read-surface footprint**, which auth-plane lenses compare
against KV after evaluation (`pipeline/evaluate.go:351-393`) — turning a match into a spurious drift
retry, on exactly the lens family this design is for. §4.4 now states "evaluate, don't render" as a
constraint with its reason, so a builder cannot reach for the wrong shortcut.

### 10.3 The `key ⊄ carried` fail-closed branch was missing an alias-rename case

The first draft compared alias sets by *name presence*, which admits `WITH identity AS ident, …` — where
the name survives but the binding is re-aliased and the dependence chain is broken. §4.2 now requires the
carry to be `*VariableRef{Name: a}` **with effective alias `a`**, and treats duplicate aliases in one
clause as fail-closed. Both branches fall through to today's behaviour.

### 10.4 Corrections to the board row's own framing, from code (§1)

"7.2 GB alloc" is cumulative allocation, not resident heap; and "0.3 % of the cap" is not a loose bound
but an orthogonal quantity. Both were stated in §1 rather than carried forward, because either reading
would have justified building §9.5 as authoritative machinery on a premise that does not survive
grounding.

### 10.5 Checked against every other in-flight design touching this file

`auth-plane-projection-latency-design.md` (✅ ratified) and `lens-projection-divergence-audit-design.md`
(📐) both cite `ruleengine/full/executor.go`. Neither touches the grouping key; both depend on properties
this design preserves exactly (§6). `structural-pause-recovery-design.md`,
`edge-cold-signin-delivery-position-design.md` and `client-ceremony-op-descriptors-design.md` do not
reference the engine at all. No consolidation is needed and no build order is forced.

**Reflexes checked and found not to apply:** Processor-side reads (no new reads); invariant-bending
workaround (none); retraction transport (output is bit-identical, no row-set shrink); removed-component
obligations (nothing removed — `normalizeForKey` is a pure function with no side effect, verified at
`executor.go:1865-1928`); recompute-comparability params (both sides of the equivalence test run
in-process on one corpus with identical parameters); auto-recovery clocks (no loop introduced).

## 11. Decomposition for the Steward

Two increments, each independently shippable and green. **Sequence 1 → 2** for review clarity only; they
touch adjacent code but are independent, and Inc 2 could ship first.

**Increment 1 — grouping-key reduction (the fix).** `ruleengine/full/grouping.go`
(`analyseGroupingRedundancy`), the `CompiledRule` field wired at `full.go:108`, the executor field, the
third parameter on `projectItems`, and the `keyParts` skip. Tests: the three-domain equivalence run, the
randomized differential, the two precondition tests (§4.1), and **the structural gate** (§8.3). Benchmark
recorded in the build note. *Green criterion:* every existing `ruleengine/full`, `projection`, `pipeline`
and `packages/*` lens test unchanged and passing — the design's contract is that no projected row moves.

**Increment 2 — honour `WITH DISTINCT`.** The `applyWith` de-duplication (§5), its unit tests, and the
`memberAccountsSpec` one-row-per-member test. *Green criterion:* `packages/wellness-ledger` tests pass with
the new expectation; no other lens changes behaviour (nothing else in the corpus uses `WITH DISTINCT`).

**Gates:** `go build ./...`, `make vet`, `golangci-lint run ./...`, `make verify-kernel`, every
`scripts/lint-*.go` gate, and `go test ./...` — a change inside the shared rule engine has a wide blast
radius, so the full suite is required, not the touched packages.

## 12. Residuals — named, with their triggers

- **`applyReturn`'s `DISTINCT` dedupes by `json.Marshal`, not by `normalizeForKey`** (`executor.go:1231`).
  The marshal error is discarded, and a `*nodeRef` has no exported fields, so two rows differing only in a
  node-valued column render alike and one is dropped. No lens returns a bare node column today (every
  `RETURN` item is a `.key` or an aspect field), so there is no live victim — but it is the same class of
  defect Inc 2 closes, in the sibling function. **Filed as a board row by this fire.**
- **The binding set is still materialized in full** — the existing separate board row. Unchanged by this
  design and still without a live consumer; §4 removes a per-row cost, not the peak-set ceiling. No reason
  to fold it in here.
- **§9.5 (cost-based governor) and §9.6 (generalized dependence)** — deferred with the named triggers
  above, per the dead-scaffolding test.
