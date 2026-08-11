# Full-engine independent-branch decomposition — sibling OPTIONAL branches fold, they do not multiply

**Status: ✅ RATIFIED 2026-08-06 (Winston, under delegated authority) — fork resolved to the ENGINE option**
· Designer fire, Winston, 2026-08-02

## Ratification (Winston, 2026-08-06 — delegated by Andrew)

Andrew delegated this class of decision in the ratify session: *"Winston can ratify — do what is right long
term, do NOT make decisions based on how many lines of code need to be changed."*

**The fork resolves to the engine change, not the authoring convention + gate.** Three reasons, none of
them about cost:

1. **Independent branches are semantically independent, so their cost should be additive.** A cross product
   over sibling OPTIONAL branches computes a combinatorial object no consumer asked for and no projection
   reads. That is an engine defect, not a tuning parameter.
2. **A convention would push an engine deficiency onto every future lens author** — and the platform
   itself would be its first violator, since the generated read-grant producers emit staged multi-branch
   shapes. A rule the platform's own code generator breaks is not a rule.
3. **A gate cannot express the constraint.** Branch independence is a semantic property of the branch set,
   not a syntactic property of one clause, so a lint over cypher text could only approximate it — and an
   approximate gate on a correctness-shaped property is the fingers-crossed state the label-binding fire
   exists to end.

**Increment 2 (`peakBindingRows` observability) ships FIRST within this design**, ahead of Increment 1.
Not as a gate on the decision — the decision is made — but because it is the acceptance instrument for
this fire *and* for the grouping-key sibling, whose headline measurement turns out to have no traceable
provenance anywhere in the repo. §2's own "~730 MB" is an honest extrapolation to the 1M-row cap rather
than a second measurement, and nothing in the engine measures peak rows today (`maxBindings` is a cap that
errors, with no counter). Building the instrument first turns both fires' acceptance criteria from
extrapolation into observation.

**Increment 3 (streaming the binding set) stays shelved** with its existing revive trigger — Increment 2
is what makes that trigger observable, which is the design's own §9-C reasoning and remains right.

**Sequencing.** `lens-label-key-type-binding` → `full-engine-grouping-key-reduction` → **this design**.
The label fire **shipped 2026-08-07**, which turns its sequencing note into a live obligation on this
build: its Increment 2 accumulates an `OPTIONAL MATCH`'s labels **per path**, sound only because a clause's
paths are threaded into one binding stream (`executor.matchPatterns`). Branch grouping changes exactly
which clauses share that stream, so **this build must re-derive `ReferencedLabels`' optional-label scope
per branch group** — a later unlabeled node in another group is no longer a re-reference at all, and
`internal/refractor/label_derivation_corpus_census_test.go` pins every lens's verdict, so a regression
surfaces there. The grouping-key fire precedes this one by both docs' agreement (§8), so this design's
`projectItems` edit lands on a loop that no longer re-renders carried accumulators.
Owning component: **Refractor** (`internal/refractor/ruleengine/full`)
Board row: `backlog/lattice.md` → *[Refractor] The executor still materializes the whole binding set*
(★★, L, previously `📋 designer · no live consumer`). **That row's "no live consumer" is wrong** — §2
names fourteen live lenses, two of them on the security plane. This design answers the row; §9 keeps the
row's own stated remedy (streaming) shelved with a trigger that Inc 2 makes observable.

---

## For Andrew

**What it does, in two lines.** A stage of independent `OPTIONAL MATCH` branches off one anchor is
evaluated today as their **cross product** — `capabilityEphemeral` materializes
`|directTasks| × |delegatedTasks| × |queuedTasks|` binding rows to build three `collect(DISTINCT …)`
lists that each read exactly one branch. A compile-time pass proves which branches are *foldable*
(their variables reach the projection only through a multiplicity-insensitive aggregator over that one
branch), and the executor evaluates those against the anchor set **separately**, folding each into its
own aggregator. Peak rows fall from the product of the branches to the **largest single branch**; the
projected rows are identical, element for element and in the same order.

- **This is the fix that already shipped once, as an authoring workaround.**
  `read-grant-single-source-walk-design.md` §12 (2026-08-01) rewrote the *generated* read-grant producers
  into one hand-staged `WITH` per walk after a live 1,000,001-row cross product refused the evaluation and
  stopped a whole domain's read grants from refreshing. That fixed the generator. The same mechanism sits
  unfixed in **fourteen hand-authored lenses** (§2), including `capabilityEphemeral` (ephemeral task
  grants — a security-plane document) and `myTasks` (every operator's task inbox).

- **THE FORK — where the fix belongs (my recommendation: the engine).**
  - **(A) Engine decomposition — recommended.** One compile-time analysis + a contained executor change.
    Fixes all fourteen at once, needs no package version bumps, no cypher rewrites of live security
    lenses, no new authoring convention, and therefore **no lint gate** — there is nothing for the next
    author to forget.
  - **(B) Authoring convention — hand-stage each lens into `WITH` folds, plus a lint gate that
    default-denies the flat shape.** This is what §12 did for the generator. It is proven and needs no
    engine work, but it rewrites six live lenses (two security-plane) one cypher at a time, bumps six
    package versions, and — per the lint doctrine — must ship a gate, which cannot classify a safe
    two-branch stage from an unsafe one and so would demand a declaration on nearly every lens in the
    corpus.
  - **Why (A).** Eight natural `OPTIONAL MATCH` branches off one identity is *correct, idiomatic Cypher*.
    Making every author hand-stage it enshrines an engine limitation in the authoring surface — the same
    move the NanoID/RLS-anchor lesson rejected: **a missing engine primitive is debt to add, not a
    workaround to bake into the convention.** (B) remains a clean fallback and (A) does not invalidate the
    §12 staging already in the tree.

- **Fail-safe default.** The analysis is computed at `Parse` and stored on the shared, never-mutated
  `CompiledRule`. Unproven, unrecognised, or *any* multiplicity-sensitive aggregator anywhere in the stage
  ⇒ **no decomposition** ⇒ today's code path exactly. `clauseSatisfaction` (§4.4) is a live lens the rule
  refuses, and it must stay refused: its `count(t.key)` is non-DISTINCT and today counts the product.

- **No architectural fork of the named kind** (Gateway / D1 / Vault / multi-cell / HA-NATS untouched).
  **No frozen-contract change** — Contract #6 states no `MATCH`/`WITH`/grouping evaluation semantics;
  nothing is staged uncommitted. Inc 2 adds one field to `docs/health-kv-schema.md`, which is not frozen.

- **In-flight neighbour, named.** `full-engine-grouping-key-reduction-design.md` (📐 awaiting-Andrew,
  same day) edits `projectItems` in the same file. They **compose and do not collide** (§8) — that design
  removes a per-row cost inside a stage, this one removes rows from the stage. Either may ratify first;
  §8 states the one merge conflict and which side owns it.

- **Adversarial pass: run this fire, findings folded (§10).** Four findings reshaped the rule — one killed
  the draft's per-group precondition, one found the fold tree cannot be fed per-branch without routing,
  and one found the whole-group verdict would have delivered nothing on the corpus's widest stage. This
  design's own pre-build gate is discharged; nothing is deferred to the Steward.

---

## 1. Problem + intent

The vault states the engine's cost promise: anchor-first evaluation *"ensures the evaluation cost is
proportional to the change, not the whole graph"* (`Obsidian Vault/Lattice/Lens and Refractor/The
Refractor.md:24`). A stage of sibling `OPTIONAL MATCH` branches violates it in a way that is proportional
to neither: **the product of the branches' fan-outs**, to build lists that each depend on one branch.

The executor threads one `[]binding` slice through the clause list
(`executor.go:239-259`). `applyMatch` (`:311`) expands *every* inbound row through the clause's patterns;
`matchPatterns` (`:439`) and `matchPath` (`:465`) do the same per pattern and per relationship hop. So N
sibling `OPTIONAL MATCH` clauses off one anchor produce `Π fan-out_i` rows before the projection sees
them. `checkBindings` (`:172`) refuses — never truncates — past `defaultMaxBindings = 1_000_000`
(`full.go:21`), and each surviving row is a `map[string]any` (`:54`); commit `5527e0e2` measured
**78.5 MB live heap at 102,400 rows** after the aggregate-fold fix, so the cap admits roughly **730 MB**
resident for one evaluation.

`aggregate.go:81-95` already names the mechanism in a code comment, in the course of explaining why
DISTINCT binds on the aggregator *call*:

> *"…because the branches of such a query are independent OPTIONAL MATCHes whose bindings are their cross
> product, each branch's list would then be inflated by the product of all the others' cardinalities."*

The engine knows. It just pays it.

**The realized failure.** `read-grant-single-source-walk-design.md` §12: on the live cell,
`edgeManifestReadGrants`' nine base walks reached **1,000,001 binding rows on a single
`lnk.session.*.atLocation.*` event**. The cap refused the evaluation, it redelivered, and the whole
domain's read grants stopped refreshing — which, for a fail-closed read gate, degrades into rows silently
dropped as the graph moves on. The fix rewrote the *generator* (`internal/pkgmgr/anchorwalk.go`) to emit
one `WITH … collect(DISTINCT …) AS grantSliceN` stage per walk.

**The intent of this design is the generalization probe that fire did not run.** The reported root cause
named the instance the harness observed — a generated producer. The *mechanism* is "sibling OPTIONAL
branches multiply", and it reaches every hand-authored lens too.

## 2. The live consumers (the generalization probe)

Every cypher literal under `packages/**` and `internal/bootstrap/**` was partitioned into stages at each
`WITH`, and each stage's `OPTIONAL MATCH` clauses grouped into *branch groups* (a group = a clause whose
head variable is the stage anchor, plus every later clause that continues off a variable that group
introduced). Stages with two or more sibling groups, worst first:

| Lens | File | Sibling groups in one stage | What multiplies |
|---|---|---|---|
| `edgeIdentitySpec` | `packages/edge-manifest/lenses.go:499` | 8 | roles × residence(+container) × workplace(+container) × leaseapp × provider × instructor × serviceprovider × patient |
| `capabilityEphemeralSpec` | `packages/orchestration-base/lenses.go:358` | 3 | direct tasks × delegated (reportsTo) tasks × role-queued tasks |
| `identityAnchorsSpec` | `packages/identity-domain/lenses.go:158` | 4 | residence × workplace × managed × identifiedBy |
| `leaseApplicationCompleteSpec` | `packages/lease-signing/lenses.go:619` | 5 | applicant × unit × docGen instances × signed-lease objects × signature tasks × onboarding tasks × service instances |
| `myTasksSpec` | `packages/orchestration-base/lenses.go:295` | 2 | direct open tasks × role-queued open tasks |
| `renewalCompleteSpec` | `packages/lease-signing/renewal_lenses.go:198` | 5 | — |
| `clauseSatisfactionSpec` | `packages/semantic-contracts/lenses.go:106` | 4 | **refused by the rule** (§4.4) |
| `edgeTasksTail`, `wellnessBookingsSpec`, `leaseExpirySpec`, `leaseApplicationsReadSpec`, `clinicAppointmentsSpec`, `tabSettlementSpec`, `edgeEntityBookingsTail` | — | 2–3 each | — |

**The two that matter most.**

`capabilityEphemeralSpec` is the **FR56 ephemeral-grant document** — `ProjectionKind: "actorAggregate"`,
`Engine: "full"` (`lenses.go:31-37`). Its three branches are independent by construction:

```
MATCH (identity:identity {key: $actorKey})
OPTIONAL MATCH (identity)<-[:assignedTo]-(task:task)  WHERE task.data.expiresAt > $now
OPTIONAL MATCH (task)-[:forOperation]->(op)
OPTIONAL MATCH (task)-[:scopedTo]->(tgt)
OPTIONAL MATCH (identity)<-[:reportsTo]-(report:identity)<-[:assignedTo]-(task2:task) WHERE …
OPTIONAL MATCH (task2)-[:forOperation]->(op2)
OPTIONAL MATCH (task2)-[:scopedTo]->(tgt2)
OPTIONAL MATCH (identity)-[:holdsRole]->(role:role)<-[:queuedFor]-(task3:task) WHERE …
OPTIONAL MATCH (task3)-[:forOperation]->(op3)
OPTIONAL MATCH (task3)-[:scopedTo]->(tgt3)
RETURN identity.key AS actorKey,
       collect(DISTINCT {…task…}) + collect(DISTINCT {…task2…}) + collect(DISTINCT {…task3…}) AS ephemeralGrants
```

A manager who holds a queued role is the worst case, and it is not exotic: 40 direct grants × 30 reports'
tasks × 300 queued tasks is **360,000 rows** for a document that lists ~370 grants. The role queue is a
*queue* — the branch nobody bounds. At the cap the evaluation is refused, the grant document stops
refreshing, and the actor's ephemeral grants freeze at whatever they were: on the security plane, a
**revocation that never lands**, which is the over-grant direction.

`myTasksSpec` (`:295`) is the same shape with two branches and is the task inbox every operator sees.

**Not measured live.** No production capture exists for these fourteen; the 1,000,001-row measurement is
the generated producer's. What is grounded is the *mechanism*, character-for-character, and the fact that
one instance of it already fired. Inc 2 makes the rest measurable rather than argued.

## 3. Reconciliation with the existing mental model

- ***Didn't we already fix this?*** — For **generated** read-grant producers, yes: §12's staging, shipped
  `99720950`, proven by `read_grant_producer_staging_test.go` plus 180 randomized comparisons. That fix
  lives in `internal/pkgmgr/anchorwalk.go` and reaches only cyphers pkgmgr emits. Every cypher a human
  typed is untouched.
- ***Isn't the binding cap the answer?*** — The cap is a runaway backstop that **refuses**, deliberately
  (`executor.go:166-171`: a truncated binding set writes a silently wrong row). Refusal is the correct
  behaviour and it is also the outage. The cap bounds the damage; it does not remove the product.
- ***Doesn't this duplicate the grouping-key design?*** — No. That design removes a **per-row** cost
  (`rows_k × Σ|slice_j|` grouping-key renderings, `executor.go:1129`); this one removes **rows**. §8.
- ***Does it introduce new state?*** — Only a compile-time analysis on `CompiledRule`, the same seam and
  the same fail-safe posture the grouping-key design uses. No runtime state, no KV, no contract.
- ***Is the flat form's multiplicity ever intended?*** — Yes, in exactly one live place, and the rule
  refuses to touch it: §4.4.

## 4. The shape

No vertices, aspects, links, lenses or ops change. Read path (P5) and write path (P2) are untouched: this
is the evaluation of an already-installed lens rule, called from `projection/plan.go:52`.

### 4.1 Branch groups

Within one **stage** (the clauses between two `WITH`s, or between the last `WITH` and `RETURN`):

- The **base** is the required `MATCH` clause(s) and the variables carried in from the previous stage.
- A **branch group** is one `OPTIONAL MATCH` clause whose first pattern node binds a base variable, plus
  every later `OPTIONAL MATCH` clause in the stage whose first pattern node binds a variable that group
  introduced. (`(task)-[:forOperation]->(op)` continues the `task` group; `(identity)-[:holdsRole]->…`
  starts a new one.)
- Groups are disjoint by construction, and every optional clause lands in exactly one. A group is a
  **tree** of clauses, not a list — `leaseApplicationComplete`'s `id` group carries `inst` and
  `onbTask`(→`onbOp`) as sibling subtrees hanging off `id`.
- A group's **pinned frontier** is the set of its clauses whose variables are referenced by a
  non-aggregating projection item (§4.3(1)), together with every clause on the path from the group root to
  one of them. Everything below the frontier is a **foldable candidate subtree** judged on its own. This
  matters on the widest stage in the corpus: `id.key AS applicant` pins `id`, but `inst` and `onbTask`
  hang below it and fold, which is where that lens's fan-out actually lives.

### 4.2 The global precondition (checked first, refuses the whole stage)

Decomposition applies to a stage **only if every aggregating call in that stage's projection is
multiplicity-insensitive**:

| Aggregator | Multiplicity-insensitive? |
|---|---|
| `collect(DISTINCT x)` | ✅ — set-valued, and first-occurrence order is preserved (§4.5) |
| `count(DISTINCT x)` | ✅ |
| `max(x)` / `min(x)` | ✅ — extremum of a multiset = extremum of its set |
| `collect(x)` | ❌ — the list length *is* the multiplicity |
| `count(x)` | ❌ — the count *is* the multiplicity |
| anything else | ❌ — `newAggFold` rejects it at runtime anyway (`aggregate.go:51`) |

The check is global, not per group, because an unreferenced branch still inflates *other* branches'
multiplicity-sensitive aggregators; folding it away would change their values. One non-DISTINCT
`collect`/`count` anywhere in the stage ⇒ the whole stage keeps today's path.

### 4.3 Foldability, per group

Given the precondition holds, a branch group **G** — or, when G is pinned, a candidate subtree below its
frontier — is *foldable* iff all of:

1. **No non-aggregating projection item references its variables.** A non-aggregating item is a grouping
   term (`executor.go:1118-1130`); a row per binding is then the *intended* output cardinality. A group
   whose root is pinned this way is not discarded — only its pinned frontier stays in the product, and
   each subtree below it is judged from (2).
2. **Every aggregating call that references G references no other group** — the condition binds at the
   **call**, not the item. `collect(DISTINCT A_g1) + collect(DISTINCT B_g2)` is one item and two calls,
   and it is the shape every read-grant producer and both orchestration lenses take
   (`aggregate.go:57-66` builds a `binOpFold` over two `callFold`s).
3. **No `WHERE` or later clause outside G references G's variables together with another group's.** A
   `WHERE` attached to G's own clauses is fine — `WHERE task.data.expiresAt > $now` reads only `task`.
4. **The reference walk returned no unknown form.** `CollectVariableRefs` (`bindings.go:17`) already
   reports `unknown` for any expression shape it does not recognise, fail-closed by design; this analysis
   is its second consumer, on the same terms.

Non-foldable groups — and pinned frontiers — stay in the product exactly as today. A stage may mix:
`leaseApplicationComplete` keeps `id` and `u` in the product (both are read by non-aggregating items:
`id.key AS applicant`, `u.listing.data.rentAmount AS unitRent`) while `docInst`, `leaseDocObj`, `sigTask`
(→`sigOp`), and the two subtrees hanging below `id` — `inst` and `onbTask`(→`onbOp`) — all fold. `id` and
`u` are 0-or-1 by domain, so the surviving product is the service/task fan-out reduced from a product to a
sum.

### 4.4 The refusal case is live and must stay refused

`clauseSatisfactionSpec` (`packages/semantic-contracts/lenses.go:106`) has four sibling groups and
`count(t.key) AS chargeCount` at `:122` — **non-DISTINCT**. Today that counts
`|t| × |a| × |cond| × |insp|`, which is correct only because `a`/`cond`/`insp` are 0-or-1 by domain. The
engine cannot know that at compile time, so §4.2 refuses the stage wholesale and the lens behaves
byte-identically. This is the fail-closed default doing its job on a real lens, not a hypothetical.

### 4.5 What the executor does

Today (`executor.go:239-259`) each clause rewrites one `bindings` slice. The change:

1. `ExecuteWithFootprint` applies the base clauses and the **non-foldable** optional clauses as now,
   producing `base []binding`.
2. Foldable groups are carried to the stage's projection as `deferred []*branchGroup` rather than applied.
3. `projectItems` computes each base row's grouping key from the non-aggregating items — which by §4.3(1)
   reference no deferred group — creates or reuses the group accumulator, then for each deferred group
   **G**: expands that one base row through G's clauses (`matchPatterns`, unchanged) and adds each
   expansion **only to the folds that belong to G**.
4. Folds that reference no deferred group (base-only aggregators) receive the base row once.

Fold routing is the one new piece of bookkeeping: `newAggFold` (`aggregate.go:29`) builds the tree
mirroring the expression, so each leaf `callFold` is stamped with its owning group (or "base") from the
same analysis, and `add` descends only into the leaves for the group being folded. Without it, folding
group *g1* would evaluate *g2*'s `collect` on a row where *g2*'s variables are unbound.

Peak resident rows per stage become `|base| + max_G |base × G|` instead of `|base| × Π_G |G|`.
`checkBindings` continues to guard each expansion, so the cap now bounds the largest branch — which is
what the board row asked "streaming" to deliver.

### 4.6 What does not change

- **The read surface.** Every node and adjacency read is memoized for the evaluation's life
  (`executor.go:98`, `:110`) and `footprint()` (`:280`) is built from those memos, so re-walking a shared
  prefix replays memo hits and the certified footprint is the same **set** of keys and revisions. This is
  the identical mechanism argument §12 made for staging.
- **OPTIONAL semantics.** The null-preserving fallback and `OPTIONAL … WHERE` handling
  (`executor.go:318-366`, `nullBindNewVars` `:412`) run per source binding today and per base row under
  decomposition — the same code, the same rows.
- **Order.** `callFold` dedupes by `normalizeForKey` with **first occurrence winning**
  (`aggregate.go:90-118`). In the product, a value from group G first occurs in G's own iteration order
  within the first row of every other group; under decomposition it occurs in G's own iteration order.
  Identical lists, not merely identical sets.
- **Zero-rows-in, zero-rows-out** (`executor.go:1176-1189`), `RealnessFilter`, `EmptyBehavior`,
  composite keys, the simple engine, and every projection-side classifier.

## 5. Contract surface

**None.** Contract #6 (Lens/Refractor) declares lens shapes, projection kinds, output descriptors and the
§6.14 grant document — no `MATCH`/`WITH`/aggregation evaluation semantics. Contract #1 key shapes are
untouched. Nothing is staged uncommitted for ratification.

Inc 2 adds `peakBindingRows` to the lens entry documented in `docs/health-kv-schema.md` — a docs file,
not a frozen contract, and additive.

## 6. Migration / compatibility

There is nothing to migrate. No package changes, no version bumps, no reinstall: the analysis runs at
`Parse`, which every lens already passes through at activation and hot-reload. A lens that decomposes
projects the same rows it did the evaluation before; a lens that does not decompose runs the code it runs
today. Rollback is a one-line disable of the analysis (return "nothing foldable").

The `WITH`-staged generated producers (§12) keep working unchanged — a staged stage simply has one group
per stage and nothing to fold. **Un-staging the generator is explicitly not proposed here**; the staged
form is shipped, proven and cheap, and re-flattening it would trade a proof for a proof.

## 7. Test strategy

The load-bearing assertion is **equivalence**, in both directions, because a divergence in a grant
document changes who can read what.

- **Differential harness (unit, `full`)** — for each spec, execute the real lens cypher with decomposition
  **on** and **off** against one seeded corpus and assert the `[]ProjectionResult` are **deeply equal,
  including list order**. Not "equal as sets" — §4.5 claims order preservation, so the test must pin it.
  Seeded with the five §2 specs plus `clauseSatisfaction` (which must report *no* decomposition).
- **Randomized adversarial corpora** — mirror the §12 review pass: ≥60 randomized actor-rooted corpora ×
  the §2 specs, with the same adversarial sharing (a workplace equal to a residence, multi-parent
  `containedIn`, zero-hop `*0..`, randomly-empty branches, a report who is also a role-holder), asserting
  deep equality every time.
- **Analysis unit tests** — one per §4.2/§4.3 clause, each proving the *refusal*: a non-DISTINCT `count`
  anywhere; a non-aggregating item over a branch; one aggregator call spanning two groups; a cross-group
  `WHERE`; an `unknown` from `CollectVariableRefs`.
- **Cap behaviour** — the `capabilityEphemeral` shape at `WithMaxBindings(50_000)` over a corpus whose
  product exceeds it: refused before, succeeds after, same rows as an uncapped flat run. This is the
  `read_grant_producer_staging_test.go` pattern, which already proves the analogous claim for the
  generated producer.
- **Pinned census** — the projection-side classifiers (`hasMultiBindingConjunctUnit`,
  `footprint_classifier.go`) read the AST, not the executor's row sets, so they are untouched; a census
  row for each §2 spec asserts their verdicts are unchanged. (§12 shipped a classifier regression this
  way; the census is cheap insurance.)
- **e2e** — the ephemeral-stack run already exercises `capabilityEphemeral` and `myTasks`; no new e2e is
  warranted, and none would add signal the differential harness does not.

## 8. The in-flight neighbour: `full-engine-grouping-key-reduction-design.md`

Both designs are mine, both dated 2026-08-02, both edit `projectItems`. They are **complementary, not
competing**:

- The grouping-key design removes the `rows_k × Σ_{j<k}|slice_j|` grouping-key rendering term inside a
  stage — a **per-row** cost, measured at 3.3 s / 7.2 GB *allocated* (cumulative allocation, not resident
  heap; that design corrects the row's framing itself).
- This design removes **rows** from the stage. Since the grouping-key term is linear in `rows_k`, cutting
  rows cuts that term too — they multiply favourably.

**The one interaction to sequence.** Both add a compile-time analysis to `CompiledRule` and both touch
`projectItems`' grouping loop. Whichever lands second rebases onto the other; the conflict is textual, in
about twenty lines, and neither changes the other's semantics. **Recommended order: the grouping-key
design first** — it is the smaller change, it is already at 📐, and it leaves this design's `projectItems`
edit landing on a loop that no longer re-renders carried accumulators, which is the loop this design adds
a second caller to.

Two related rows stay where they are: `RETURN DISTINCT`'s `json.Marshal` dedup (`executor.go:1231`) is
filed separately and untouched here; the auth-plane latency design's Inc 0–3 work on Refractor's
*consumer* filters, not the engine, and does not intersect.

## 9. Risks + alternatives

**Recommended: (A) engine decomposition.** Above.

**(B) Hand-stage each lens + a lint gate.** The §12 shape, applied by hand. Rejected as the primary, kept
as the fallback: it rewrites six live cyphers, two of them security-plane documents, each needing its own
equivalence proof and a package version bump; and per the lint doctrine it must ship a gate in the same
design. That gate cannot distinguish a safe two-sibling stage (`(task)-[:forOperation]->(op)` +
`(task)-[:scopedTo]->(tgt)`, both 0-or-1) from an unsafe one without exactly the semantic analysis (A)
builds — so it would default-deny and demand a declaration on nearly every lens in the corpus, and
rubber-stamped declarations are the fingers-crossed state a gate exists to end. **Could a variant of (B)
beat (A)?** The best variant is "(A)'s analysis, exposed as a lint that fails a package whose lens has
non-foldable siblings." That is strictly (A) plus a gate — and once (A) folds them, there is nothing left
to gate. It reduces to (A).

**(C) Streaming / lazy binding expansion — the board row's own stated remedy. Deferred, with a
trigger.** Replacing `[]binding` with an iterator removes the ceiling rather than lowering it, and it is
the only answer for a *single* branch whose own fan-out exceeds the cap. That case has **no live
consumer**: after (A), every §2 lens peaks at its largest single branch, and the one corpus that ever hit
the cap is closed. Per the dead-scaffolding test the design stays on the shelf and the build is sequenced
behind a real consumer — **trigger: Inc 2 reports a single branch group's peak rows within an order of
magnitude of the cap, or a cap refusal whose stage has one group.** The board row is updated to point
here and to keep that trigger.

**(D) Raise `defaultMaxBindings`.** Trades a refusal for 730 MB+ of resident heap on a shared host. It
treats the symptom in the more dangerous direction and is not proposed.

**(E) A cost-based evaluation governor** (estimate the product, refuse early with a better message). It
does not make any lens work; it makes the failure prettier. The grouping-key design declined the same
mechanism on the same grounds.

**Risks.**

- **A soundness bug is a silently wrong grant document.** This is the real risk and it is why §4.2 is a
  *global* refusal, §4.3 binds at the aggregator call, and §7's assertion is deep equality in both
  directions over randomized corpora rather than a spot check. The fail-safe direction is always "do not
  decompose".
- **Executor complexity.** `projectItems` gains a second row source. Mitigated by the analysis being a
  pure function over the AST with no runtime state, and by decomposition being off unless proven.
- **A future AST node.** `CollectVariableRefs` already returns `unknown` for unrecognised shapes and this
  analysis inherits it; a new node type degrades to "no decomposition", never to a wrong one.

## 10. Adversarial pass (run this fire — findings folded)

Walking §2's reflex list against the draft, one at a time:

1. **"Verify the mechanism can BE reshaped."** The first draft said "evaluate the branch separately and
   fold it" without opening `aggregate.go`. It must be opened: `newAggFold` builds a **tree** mirroring
   the expression (`:56-66`), so `collect(DISTINCT A) + collect(DISTINCT B)` is one `binOpFold` over two
   `callFold`s. Adding a *g1* row to the tree would evaluate *g2*'s `collect` on a row where *g2* is
   unbound. **Fix folded in:** per-leaf group stamping and routed `add` (§4.5). Without this the design
   would have handed the Steward a fire whose first act is discovering the fold tree cannot be fed
   per-branch.
2. **The per-group condition was not sufficient.** The draft made multiplicity-insensitivity a *per
   group* test. An **unreferenced** branch group inflates *other* groups' non-DISTINCT aggregators, so
   folding it away silently changes their values. **Fix folded in:** §4.2 is a global stage-level
   precondition. This is what makes `clauseSatisfaction` (§4.4) refuse as a whole rather than
   half-decompose.
3. **`max`/`min` are multiplicity-insensitive and must not be excluded.** A first pass wrote
   "DISTINCT-only". `leaseApplicationCompleteSpec` uses non-DISTINCT `max()` over branch variables
   (`lease-signing/lenses.go:667-675`); excluding them would have refused decomposition on the widest
   stage in the corpus for no correctness reason.
4. **A whole-group verdict silently discarded the widest stage in the corpus.** The draft judged
   foldability per *group*. `leaseApplicationComplete`'s `id` group is pinned by `id.key AS applicant` —
   but `inst` (the readiness service instances) and `onbTask`(→`onbOp`) hang **below** `id`, and that is
   where the fan-out actually lives. A whole-group verdict would have declared the corpus's widest stage
   non-foldable and delivered nothing there. **Fix folded in:** the **pinned frontier** (§4.1) — a group
   is a tree, only the path to a non-aggregating reference stays in the product, and each subtree below it
   is judged on its own. Found by reading that lens's actual RETURN columns rather than its branch count.
5. **"Order is preserved" was an assumption.** Checked: `aggregate.go:90` — dedupe by `normalizeForKey`,
   first occurrence wins, "the result order stays the binding order". In the product the first occurrence
   of a *G*-value falls in *G*'s own order, so decomposition preserves the list exactly. §7 pins deep
   equality rather than set equality on the strength of it.
6. **"A container-level default is not retroactive."** No inheritance here — the analysis is recomputed at
   every `Parse`, and every lens re-parses at activation and hot-reload. No pre-existing population is out
   of reach.
7. **"A handed-down measurement's units are a premise."** The 78.5 MB / 102,400 rows figure is **peak live
   heap** (commit `5527e0e2`), so the 730 MB extrapolation is resident memory and the safety framing holds
   — unlike the neighbouring row's 7.2 GB, which is cumulative allocation. Checked rather than assumed.
8. **"A root cause names the instance something was asserting on."** This design *is* that probe, run on
   §12's fix. §2 enumerates every consumer of the mechanism, not the one that broke a gate.
9. **"Check the other in-flight designs."** Done — §8. The grouping-key design touches the same function;
   they compose, and the order is stated rather than left to collide.
10. **"A lint gate is never optional."** Applies only when a design establishes a **convention**. (A)
   establishes none, which is §9's argument for it over (B). If Andrew flips to (B), the gate ships in the
   same design as a required fire — not as defense-in-depth.
11. **"Ground the failure mechanism in code."** The cap, the refusal-not-truncation posture, the memoized
    read surface, the null-preserving OPTIONAL semantics, and the fold tree were each read before they
    became premises. The one thing **not** grounded in a live measurement is the §2 lenses' actual
    fan-out; §2 says so plainly and Inc 2 exists to close it.

## 11. Decomposition for the Steward

**Inc 1 — the analysis + decomposed evaluation (M–L).** `branchgroups.go` in `ruleengine/full`: stage
partition, branch grouping and the pinned frontier (§4.1), the global precondition (§4.2), foldability
per group and per candidate subtree (§4.3), computed
at `Parse` and stored on the shared `CompiledRule`. Executor: deferred groups, routed fold `add`, per-leaf
group stamping (§4.5). Tests: the full §7 differential harness, the randomized corpora, the refusal unit
tests, the cap test, the pinned census. Independently shippable and green; nothing else depends on it.

**Inc 2 — peak-rows observability (S).** Carry the evaluation's peak binding rows (and, per stage, the
count of groups that did **not** decompose, with the §4.2/§4.3 clause that refused) out of the executor
alongside the footprint; log it at evaluation and surface `peakBindingRows` on the lens's Health KV entry
beside `projectionLag` (`health/lattice_heartbeater.go:233-241`, `:1160`). Consumers, both named: an
operator diagnosing a cap refusal (the `edgeManifestReadGrants` incident had to be reconstructed by hand),
and the **trigger** that would revive the deferred streaming work (§9-C). Add the field to
`docs/health-kv-schema.md`.

**Inc 3 — streaming / lazy binding expansion. NOT BUILT.** Design shelved per §9-C with its trigger.
The board row for it points here.

~~Build order is Inc 1 → Inc 2.~~ **Superseded by the ratification banner (2026-08-06), applied here
2026-08-11: build order is Inc 2 → Inc 1.** The banner ships Inc 2 first because it is the *acceptance
instrument* for Inc 1 — without it, Inc 1's headline claim ("peak rows fall from the product to the largest
single branch") is an extrapolation with nothing in the engine measuring peak rows. Inc 1 alone still closes
the defect; Inc 2 first is what turns the acceptance criterion from argument into observation.

---

## 12. Independent-branch decomposition fire brief (build note, 2026-08-11)

Compiled Phase-0 by the Lattice Steward from three read-only scouts, before the first edit. One brief per
ITEM; resumes run a delta-scout instead.

### 1. Scope sentence (verbatim, ratified)

> A stage of independent `OPTIONAL MATCH` branches off one anchor is evaluated today as their **cross
> product**. A compile-time pass proves which branches are *foldable* (their variables reach the projection
> only through a multiplicity-insensitive aggregator over that one branch), and the executor evaluates those
> against the anchor set **separately**, folding each into its own aggregator. Peak rows fall from the
> product of the branches to the **largest single branch**; the projected rows are identical, element for
> element and in the same order.

**Green bar:** every existing `ruleengine/full`, `projection`, `pipeline` and corpus-census test green, plus
the §7 suite (differential deep-equality both directions, randomized corpora, refusal units, cap test, new
corpus census). `clauseSatisfaction` must report **no** decomposition.

**Build order — Inc 2 → Inc 1** (ratification banner; §11's body line said the reverse and is struck above).
Inc 3 (streaming) is NOT built.

### 2. Verified touch-list (`file:line` re-checked live 2026-08-11)

The design's citations were written 2026-08-02; **the engine has had 12 commits since** and most anchors
moved. Verified positions:

| What | Design cites | **Actual** | Status |
|---|---|---|---|
| clause loop threading `[]binding` | `executor.go:239-259` | **`:278-295`** | moved |
| `applyMatch` | `:311` | **`:347`** (null-preserving fallback `:356-401`) | moved |
| `matchPatterns` | `:439` | **`:475`** | moved |
| `matchPath` | `:465` | **`:501`** | moved |
| `checkBindings` + refuse-not-truncate comment | `:172` / `:166-171` | **`:206`** / **`:200-205`** | moved |
| `nullBindNewVars` | `:412` | **`:448`** | moved |
| `footprint()` | `:280` | **`:316`** | moved |
| node / edge memos | `:98`, `:110` | **`:110`** (`nodes`), **`:122-123`** (`edges`,`edgeRevisions`) | moved |
| `projectItems` | — | **`:1254`**; grouping-key loop **`:1305-1325`** | — |
| zero-rows-in-zero-rows-out | `:1176-1189` | **`:1372-1386`** | moved |
| `RETURN DISTINCT` dedup | `:1231` | **`:1431-1442`** | moved |
| `ExecuteWithFootprint` | — | **`:245-250`** | — |
| `newAggFold` | `aggregate.go:29`,`:51`,`:56-66` | **`:29`**; `callFold` **`:94-101`**; `binOpFold` **`:149-153`** | ✅ |
| first-occurrence dedupe | `aggregate.go:90-118` | **`:111-116`** | narrowed |
| the cross-product comment | `aggregate.go:81-95` | **`:80-88`** | ✅ |
| `defaultMaxBindings` | `full.go:21` | **`:21`** | ✅ |
| `Parse` (sole `*CompiledRule` ctor) | — | **`full.go:58`**, analysis embedded **`:118-122`** | — |
| `CollectVariableRefs` | `bindings.go:17` | **`:17`** | ✅ |
| `CompiledRule` struct | — | **`ast.go:252-287`** | — |

**Lens specs (§2's census):**

| Lens | Design cites | **Actual** |
|---|---|---|
| `edgeIdentitySpec` | `edge-manifest/lenses.go:499` | **`:499`** ✅ |
| `capabilityEphemeralSpec` | `orchestration-base/lenses.go:358` | **`:358`** ✅ |
| `myTasksSpec` | `orchestration-base/lenses.go:295` | **`:295`** ✅ |
| `identityAnchorsSpec` | `identity-domain/lenses.go:158` | **`:220`** ROTTED |
| `leaseApplicationCompleteSpec` | `lease-signing/lenses.go:619` | **`:657`**; non-DISTINCT `max()` **`:706-710`,`:715`** (cited `:667-675`) ROTTED |
| `renewalCompleteSpec` | `lease-signing/renewal_lenses.go:198` | **`:211`** ROTTED |
| `clauseSatisfactionSpec` | `semantic-contracts/lenses.go:106` | **`:203`**; `count(t.key)` **`:219`** (cited `:122`) ROTTED |

**Inc 2 targets — the design's health citations rotted WHOLESALE.** `health/lattice_heartbeater.go:233-241`
is a block of issue-code constants and `:1160` is a lag-streak helper; neither has anything to do with
`projectionLag`. The real surface:

| What | **Actual** |
|---|---|
| per-lens Health entry struct | **`internal/refractor/health/healthwire/healthwire.go:102-212`** (type `Entry`) |
| `projectionLag` field | **`healthwire.go:122`** |
| the setter that writes it | **`health/reporter.go:581`** (`SetProjectionProgress`) |
| **the precedent to mirror** (engine-observed counter → per-lens Entry) | **`health/reporter.go:513`** (`RecordSecureRedactions`), `:474`/`:492` (eval-drift) |
| rolling per-evaluation buffer precedent | **`pipeline/latency.go`** (whole file; `LatencyRingBuffer`, rolling, reads do not clear) |
| where an evaluation records its per-event stat today | **`pipeline/evaluate.go:383-389`** (`latencyBuf.Record(time.Since(start))`) |
| schema doc (REAL path) | **`docs/observability/health-kv-schema.md`** — per-lens entry `:898-918`, `projectionLag` at `:912`. The design's `docs/health-kv-schema.md` does not exist. |

### 3. Precedents to mirror

- **The compile-time-analysis-on-`CompiledRule` seam** — `grouping.go` (402 lines, landed `029ef85b`):
  `analyseGroupingRedundancy` at `:117`, result stored on the unexported `CompiledRule.groupingRedundant`
  (`ast.go:277-286`, "written once by Parse … never mutated — a compiled rule is shared across concurrent
  evaluations"), consumed in `projectItems` at `:1317`, and exposed for tests/diagnostics via the public
  `CompiledRule.GroupingReduction()` (`grouping.go:159-174`) returning `[]GroupingClauseReduction`. **The new
  analysis copies this seam exactly**, including the public diagnostic accessor — the census test needs it.
- **Inc 2 counter transport** — `RecordSecureRedactions` (`reporter.go:513`): pipeline-observed value →
  read-modify-write onto the per-lens `Entry`. Rolling-window semantics from `pipeline/latency.go`.
- **Differential harness** — `read_grant_producer_staging_test.go:258-298`
  (`TestReadGrantProducer_StagedMatchesFlatAnchorSet`), corpus builder `seedEdgeManifestReadGrantCorpus`
  `:73-148`.
- **Randomized corpora** — `grouping_equivalence_test.go`: `readGrantCorpusShape` `:24-46`,
  `randomCorpusShape` `:78-107`, `seedReadGrantCorpus` `:112-277`, driver
  `TestGroupingReduction_RandomizedCorporaDifferential` `:417-450`. Deterministic seeding via
  `rand.NewSource(int64(i)+1)` — keep that (CLAUDE.md determinism).
- **Corpus census** — `grouping_reduction_corpus_census_test.go` (freshest, `029ef85b`): verdict map
  `:63-178`, derivation `:225-254`, population invariant `:304-313`. Enumerator `forEachCorpusCypher`
  (`label_derivation_corpus_census_test.go:536-592`) — expands read-grant walks *and* iterates
  `SpecBranches`, so per-branch cyphers are named `"name#N"`.
- **Test fixture** — `executor_test.go`: `startExecKVs` `:25-45`, `newFixtureRegistry` `:57-63`, `putVertex`
  `:88-104`, `putEdge` `:108-128`, deterministic `c1NanoID` `:69-82`.

**Two places the precedent is WEAKER than §7 requires — build to §7, not to the precedent:**
1. The staging test asserts **set equality** (`canonicalAnchorSet`/`diffAnchorSets`, `:217-255`). §4.5 claims
   **order preservation**, so the new harness must assert deep equality **including list order**.
2. The randomized driver runs **6** corpora. §7 requires **≥60**.

### 4. Increment order (each with its runnable green check)

**Inc 2 — peak-rows observability (S).** Carry peak binding rows (and per stage, the count of groups that did
not decompose + the refusing §4.2/§4.3 clause) out of the executor alongside the footprint; surface
`peakBindingRows` on the per-lens Health entry; document it.

```bash
go test ./internal/refractor/ruleengine/full/... ./internal/refractor/pipeline/... ./internal/refractor/health/... -count=1
```

**Inc 1 — the analysis + decomposed evaluation (M–L).** `branchgroups.go`: stage partition, branch grouping,
pinned frontier (§4.1), global precondition (§4.2), per-group + per-subtree foldability (§4.3), computed at
`Parse`, stored on `CompiledRule`, with a public diagnostic accessor. Executor: deferred groups, routed fold
`add`, per-leaf group stamping (§4.5). Then the full §7 suite.

```bash
go test ./internal/refractor/... -count=1
go test ./internal/refractor/ -run 'Corpus|Census' -count=1     # label + grouping + new branch census
go build ./... && make vet && golangci-lint run ./... && STRICT=1 go run ./scripts/lint-conventions.go
```

### 5. In-scope gotchas

- **`branchgroups.go` vs the existing `branchplan.go`.** `branchplan.go` (210 lines) already owns the word
  "branch" for a **different** thing — a multi-walk Personal lens's N independently-compiled *spec* branches
  (`ColumnOwnership`, `ClassifyBranchReturnColumns`, shared-keyspace-arbitration §13.2). `pipeline/branchmerge.go`
  is the same sense. The new file's "branch group" means **sibling `OPTIONAL MATCH` clauses in one stage**.
  The doc comment must disambiguate explicitly or the next reader conflates them.
- **The label-derivation obligation is live.** The label fire's Inc 2 accumulates an `OPTIONAL MATCH`'s labels
  **per path**, sound only because a clause's paths thread into one binding stream. Branch grouping changes
  which clauses share that stream ⇒ **re-derive `ReferencedLabels`' optional-label scope per branch group**.
  `label_derivation_corpus_census_test.go` pins 114+ per-lens `(labels, exhaustive, filterMode)` verdicts — a
  regression surfaces there, and a *changed* verdict must be adjudicated, never re-pinned to make it green.
- **Peak rows is new state and needs a LIFETIME** (standing checklist #1). An all-time monotonic max is pinned
  forever by one spike and is useless as a gauge. Decided: **rolling window**, mirroring `LatencyRingBuffer`
  (rolling, reads do not clear); report the max over the window. Write the state table — created / reset /
  carried at restart, rebuild, pause/resume, replay — before writing the struct.
- **`CompiledRule` is shared across concurrent evaluations** (`ast.go:277-286`). The analysis is written once
  at `Parse` and never mutated. Peak-rows accumulation is per-*evaluation* state and must NOT live there.
- **Fail-safe direction is always "do not decompose."** `CollectVariableRefs` returning `unknown`, an
  unrecognised aggregator, a cross-group `WHERE` ⇒ today's path exactly.
- **A new per-lens analysis ships its corpus census in the same fire** (refractor dossier standing rule):
  enumerate via `forEachCorpusCypher` through the *real* analysis — never a grep of cypher text, never a
  reimplementation of the predicate (it would agree with a broken gate) — pin the per-lens verdict, assert the
  population is exactly these names **with a floor on the count** so an empty enumeration cannot read as a
  table of unchanged rows.
- **Refractor dossier, copied in:** *(a)* **a meta sweep multiplies `Rebuild`** — not tripped here (no fan-out
  over the lens set; this fire adds no rebuild path). *(b)* **new pipeline state without a declared lifetime**
  (registry / latch / armed flag) — reset, carry and order it at replay, reconnect, tombstone and retry, or the
  review will. **Directly tripped by Inc 2's peak buffer** — see the lifetime gotcha above.
- **Standing checklist:** #1 lifetime (Inc 2 buffer). #2 every census is a premise — the §2 "fourteen lenses"
  number is pinned by the shipped census, not by the doc. #3 a negative test needs its positive vector proven
  first — the `clauseSatisfaction` refusal test must be shown to fail if the refusal is removed. #4 removal
  needs a transport and an observer. #6 precedent may carry debt — see the two §7-vs-precedent gaps above.
- **MERGED ≠ RUNNING.** The full stack is up (`bin/refractor` live). Derive affected binaries mechanically
  from `internal/refractor/ruleengine/full` + `internal/refractor/health` and cycle them from `main`.

### 6. Adjacent finds (this run's batch, or one of the two outs)

- **Done-log SHA `f63c6d57` is not on `main`** — the grouping-key fire's pre-rebase SHA; the landed commit is
  `029ef85b`. Audited every SHA in `lattice.md`: this is the only one. **Fixed in this brief's commit** (not
  filed).
- **§11's build-order line contradicted the ratification banner** and had never been rewritten. **Struck and
  amended in this brief's commit** (not filed).
- **Design citation rot is systemic in this doc** (the whole §2 lens table and every health anchor). Corrected
  in part 2 above rather than filed — the brief is now the build's map of record.

### 7. Non-goals (the drift fence)

- **Inc 3 (streaming / lazy binding expansion)** — shelved per §9-C with its trigger; Inc 2 is what makes the
  trigger observable.
- **Un-staging the generated read-grant producers** (`internal/pkgmgr/anchorwalk.go`) — explicitly not
  proposed (§6); re-flattening would trade a proof for a proof.
- **Raising `defaultMaxBindings`** (§9-D) and **a cost-based governor** (§9-E) — both rejected.
- **Rewriting any lens cypher.** Option (B) is the rejected fork; no package changes, no version bumps, no
  reinstall (§6).
- **`RETURN DISTINCT`'s dedup** (`executor.go:1431-1442`) and the auth-plane consumer-filter work — filed
  separately, untouched here (§8).

### 8. Scope-diff gate — PASSED

Parts 2–4 diffed item-by-item against part 1. Every touch traces to the scope sentence; the brief **narrows**
(Inc 3 excluded by the design itself) and widens nowhere. No adjacent mechanism substituted: the fix is the
engine decomposition (fork A), not the authoring convention (fork B).

**Declared dependencies re-verified both ways.** The design sequences
`lens-label-key-type-binding` → `full-engine-grouping-key-reduction` → this. Label fire shipped 2026-08-07;
grouping-key shipped **`029ef85b`** (2026-08-11) — `grouping.go` is present and `projectItems` no longer
re-renders carried accumulators, which is the loop this fire adds a second caller to. **Both load-bearing,
both satisfied.** No unlisted dependency found.

**Census premise re-run live.** Coarse independent scan of every backtick cypher literal under `packages/**`
and `internal/bootstrap/**` (134 literals): **32** have ≥2 `OPTIONAL MATCH` in one `WITH`-stage — an upper
bound on the design's **14 with ≥2 branch *groups***, and consistent with it (chained clauses collapse into
one group: `capabilityEphemeral` has 9 optionals but 3 groups). The premise is not falsified, but the doc's 14
is **not pinned by anything executable yet** — the shipped corpus census is the authority and must assert the
population by name with a count floor.
