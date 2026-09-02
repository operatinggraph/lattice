# Untyped-hop anchor derivation — lifting `AnchorHopIndex`'s third false refusal

**Status: ✅ ANDREW-RATIFIED 2026-09-01** — build-ready; the Lattice Steward builds it in the
increment order of §13. · Designer fire 2026-09-01 · Stream 2 (Lattice) ·
board row: *[Refractor] Three lenses are underivable — an untyped `-[r]->` matches any relation*

---

## 0. For Andrew

**What it does, in two lines.** `objectLiveness` and `objectAttachments` are refused by the
affected-anchor derivation because their pattern carries `OPTIONAL MATCH (o)-[r]->(owner)` — an
**untyped** relationship. The refusal's stated reason ("cannot be indexed by relation name") is the
same *false-reason* class the variable-length refusal already turned out to be on 2026-08-28: the
walk never indexes **by** relation name, it filters fetched adjacency entries **on** it, and the
filter already has a "no evidence ⇒ keep" discipline for the far end's label. This design lifts the
refusal by admitting a **wildcard hop** (`Rel == ""`), and — separately and first — **deletes** the
hop from `objectLiveness`, where its output is already discarded.

**Fork check: none.** No architectural fork. Every decision here is mechanism-level (which conjunct,
which predicate, which increment order) and is resolved in the body with grounded reasoning, per §3's
rule that a mechanism-level fork is not forwarded.

**Frozen-contract check: none.** No `docs/contracts/*` section is touched. `AnchorHopIndex` is an
internal Refractor derivation; the cypher surface, the lens declaration surface (Contract #10 §10.2),
and every wire shape are unchanged. Nothing in this design is staged uncommitted.

**Why it was flagged for Andrew** (ratified 2026-09-01; kept because it is what the builder must
carry into the fire): the thing being lifted is a **fail-closed completeness conjunct**
whose false answers are, in the mechanism's own words, *"a revocation that never fires."* The
predicate is shared by the auth plane. §6 evaluates every one of its six consumers against a
wildcard hop **and** against Increment 1's zero-hop index, and finds no consumer whose answer moves in
the unsound direction — but three of them do move, which is a posture change even where it is a sound
one, and the class of change is one you have wanted to see. Under the 2026-08-20 delegation this would
otherwise be Winston-adjudicated; the adversarial pass in §12 has been run and folded — two blockers
and five majors, all real, all reshaping the document — so it is build-ready on ratification.

**Two corrections to the board row you should see** (§1.2): the row says *two* lenses; the corpus
carries **three** untyped-hop lenses, the third a **Protected/RLS** plain lens the row does not name.
And the row's `no-pattern:` tag names two things — *"an untyped relationship **at an unlabeled
position**"* — of which the second half is **already built**: an unlabeled position is admitted by
`stepAdmitsFarEnd` and by `PositionsBinding` today.

---

## 1. Problem

### 1.1 The demand, as filed

> **[Refractor] Two `objects-base` lenses are underivable — an untyped hop at an unlabeled
> position.** `objectLiveness`/`objectAttachments` bind `OPTIONAL MATCH (o)-[r]->(owner)` by design
> (objects attach over several relations), so `AnchorHopIndex` refuses them and every neighbour event
> runs the relation-blind walk; 40k backlogged each. Not the personal-lens licence's territory — both
> are actorAggregate plain lenses already asserting pattern closure.
> `📐 needs designer pass · no-pattern: anchor derivation across an untyped relationship at an
> unlabeled position`

Its origin is `refractor-hub-walk-and-periodic-load-design.md`'s §8 residual table, the live
2026-09-01 measurement (`nats consumer report KV_core-kv`, 24–25 backlogged consumers / 1.2 M
messages).

### 1.2 What grounding corrected in it

| Row's claim | Verdict | Where |
|---|---|---|
| "Two `objects-base` lenses" | **Three lenses**, one outside `objects-base` and on the security plane | §2.1 |
| "an untyped relationship **at an unlabeled position**" (the `no-pattern:` prescription) | **Half already built** — the unlabeled-position half is shipped; only the untyped-*relation* half is missing | §3.3 |
| "`AnchorHopIndex` refuses them" | True, and it is the **sole** refusal for both — every other conjunct passes | §2.2 |
| "every neighbour event runs the relation-blind walk" | True, and it is **two** mechanisms firing on one cause (the derivation declines *and* the walk scope goes nil) | §3.4 |
| "40k backlogged each" | A **snapshot**, not a steady-state property, and its attribution has already moved once. Not usable as the payoff without a re-measure | §11.1 |

The `no-pattern:` correction is the one that matters for filing discipline: a row's prescription names
the primitive **a particular solution shape** would need, and this one named two primitives where one
was already in the tree. The row is corrected as part of this fire.

---

## 2. Grounding ledger — the censuses, run in this fire

Every count below ships as the command that derives it and the result it returned on
`33902f3e` (2026-09-01; the censuses were run at `e4b4cdef` and the one commit between them is
docs-only, so no count moves), so the build's Phase-0 re-runs them mechanically. An independent census was
briefed **to falsify** these numbers and run concurrently with the drafting; where it disagreed with
the row, the row lost.

### 2.1 C1 — the untyped-hop corpus is exactly three lenses, and none is generated

```bash
grep -rnE '<?-+\[[A-Za-z_]*\]-+>?' --include="*.go" packages/ internal/ | grep -v '_test.go' | grep -vE '\-\[[A-Za-z_]*:'
```

Enumerated **by the declaration** — every `const …Spec = ` cypher literal reachable from a
`pkgmgr.LensSpec`, across all sibling files, not by a `packages/*/lenses.go` glob — plus every cypher
literal under `internal/`. Match written for the **family** (any bracket body with no `:`), including
`--`, `<-[r]-`, `-[r*..]->`, and relationship patterns in `WHERE NOT (…)` / `RETURN` comprehensions,
not only in `MATCH`.

| Lens | Site | Pattern | Index that sees it | Refused today on |
|---|---|---|---|---|
| `objectLiveness` | `packages/objects-base/lenses.go:105` | `OPTIONAL MATCH (o)-[r]->(owner)` | `AnchorHopIndex` (actorAggregate, `$actorKey` on `o`) | `hopUntypedHop` |
| `objectAttachments` | `packages/objects-base/lenses.go:164` | `OPTIONAL MATCH (o)-[r]->(owner)` | `AnchorHopIndex` | `hopUntypedHop` |
| `objectIdentityAttachmentsRead` | `packages/loftspace-domain/lenses.go:295` | `MATCH (o:object)-[r]->(owner:identity)` | `ScanRootHopIndex` (plain arm — **no `$actorKey`**) | `rootUntypedHop` |

**Generated lenses contribute zero, structurally — not merely unsearched.** `anchorwalk.go`'s
`(*patScanner).rel` refuses to *parse* a chain hop without a colon-typed relation
(`internal/pkgmgr/anchorwalk.go:1083-1087`: *"a chain hop must name its relation type … an untyped hop
would traverse every relation and grant more than the walk declares"*). That is a package-**build**
error, so no walk-expanded lens can carry one. That refusal is a security rule about *grant* breadth
and this design does not touch it (§8, alternative 5).

**Primordials contribute zero.** Both relationship patterns in `internal/bootstrap/lenses.go` are
`-[:holdsRole]->`.

**Multi-type alternation is not in this class.** `-[:a|b]->` is refused at parse
(`ruleengine/full/visitor.go:329-337`, *"a relationship pattern must name at most ONE type"*), so it
never reaches `hopindex.go` as `Type == ""`.

Both platform corpus-census tests independently pin the same three:
`anchor_hopindex_corpus_census_test.go:140-141` and `plain_scanroot_corpus_census_test.go:154`.

### 2.2 C2 — the untyped hop is the SOLE refusal for all three

The pinned verdict alone does **not** establish this: `rejectOnce` records the *first* reason and the
MATCH clauses are walked before `WITH`/`RETURN`, so an untyped hop **masks** any later
unmodelled-expression rejection — precisely the masking that hid the two `cap-read.edgeManifest*`
producers behind the `WITH`-scope refusal. So each conjunct was evaluated by hand:

| Conjunct (`hopindex.go`) | `objectLiveness` | `objectAttachments` | `objectIdentityAttachmentsRead` |
|---|---|---|---|
| anchor bound / root labeled | ✅ one `{key:$actorKey}` on `o` | ✅ | ✅ root `o` labeled `object`, not key-pinned |
| `multiAnchor` | ✅ no | ✅ no | n/a (`ScanRootHopIndex` never checks it) |
| `*` sigil on terminus | ✅ none | ✅ none | ✅ none |
| more rels than node gaps | ✅ no | ✅ no | ✅ no |
| `MinHops > 1` | ✅ no ranged hop | ✅ | ✅ |
| **untyped hop** | ❌ **refuses** | ❌ **refuses** | ❌ **refuses** |
| unmodelled `Expr` (`addExpr` default-deny) | ✅ `count(…)` is `FunctionCall`, `(liveLinks = 0)` is `BinaryOp`, aliases are `VariableRef` — all modelled (`hopindex.go:888-931`) | ✅ `collect(DISTINCT {…})` is `FunctionCall`⊃`MapLiteral`⊃`PropertyAccess`/`FunctionCall(type(r))` — all modelled | ✅ `nanoIdFromKey(…)`, `type(r)`, `[…]` are `FunctionCall`/`ListLiteral` |
| `withScopeReject` | ✅ `""` — the WITH's items are all computed aliases, none colliding with a node var (`withCarries`); `RETURN` re-references only carried aliases, never the dropped `o`/`r`/`owner` | ✅ same shape | ✅ no `WITH` clause at all |
| `ungrounded` | ✅ both patterns headed by `o`, which **is** the terminus | ✅ | ✅ |

So lifting the untyped-hop arm makes all three indexes `Complete`, with nothing else standing behind
it. That is the *whole* claim; §6 asks the separate question of what each consumer then does.

### 2.3 C3 — every reader of `HopIndex.Complete`

```bash
grep -rn "\.Complete\b" --include="*.go" internal/refractor/ | grep -v '_test.go'
```

Six non-test readers (plus the two assignment sites). Each is evaluated in §6.

### 2.4 C4 — every reader of `PatternHop.Rel` / `PatternStep.Rel`

```bash
grep -rn "\.Rel\b" --include="*.go" internal/refractor/pipeline/ internal/refractor/ruleengine/full/hopindex.go | grep -v '_test.go'
```

Four non-comment sites, which is why §4.2's edit list is exactly four items and can be claimed
exhaustive rather than representative: `walkscope.go:267,280,285,288,296` (**already**
wildcard-aware), `anchor_derivation.go:413` `edgeTakesStep` (**edit**),
`hopindex.go:995` `AnchorSideSeeds` (**edit**), `hopindex.go:1031` `StepsFrom` (copies `h.Rel`
verbatim — **no edit**). Nothing else in the tree reads the field.

### 2.5 C5 — every writer of a `lnk.object.*` link

```bash
grep -rn 'lnk\.object\.' --include="*.go" packages/ internal/ cmd/ | grep -v '_test.go'
```

**One writer:** `packages/objects-base/ddls.go` — `attach_object` (`:553` create, `:694` the replace
leg's tombstone of the prior object's link) and `detach_object` (`:724` tombstone). Every other hit is
a doc comment or a **key reconstruction for a submit/read**: `internal/objectmanager/cascade.go`
enumerates `lnk.object.>` on an owner tombstone and submits `DetachObject` (it writes nothing itself);
`cmd/loupe/objects.go:67` and `cmd/loftspace-app/objects.go:77` rebuild the key to submit
`DetachObject`. This is the census Increment 1 rests on (§4.1).

---

## 3. The mechanism today, and why its stated reason is false

### 3.1 What refuses

`internal/refractor/ruleengine/full/hopindex.go:720-724`:

```go
case r.Type == "":
    // An untyped hop matches any relation, so it cannot be indexed by
    // relation name — the arm ReferencedRelations fails exhaustiveness on,
    // for the same reason.
    b.rejectOnce("pattern carries an untyped relationship")
```

The reason is restated as conjunct 2 of the completeness predicate in
`auth-plane-projection-latency-design.md` §16.2.

### 3.2 Why it is false — the walk does not index by relation name

The consumer never keys a map by `Rel`. It reads a vertex's **whole** adjacency document and applies
the pattern's evidence as a **filter**:

- `anchor_derivation.go:212-226` — `edgesOf(id)` calls `adjacency.Neighbors(ctx, …, id)`, which is
  relation-agnostic and memoised per walk. The relation never reaches the I/O.
- `anchor_derivation.go:409-411` — `edgeTakesStep(step, e)` is
  `e.Name == step.Rel && DirectionMatches(…)`. This is the *only* place the relation is consulted,
  and it is a predicate over an already-fetched slice.
- `anchor_derivation.go:433-443` — `stepAdmitsFarEnd` already carries the exact discipline a wildcard
  needs, in the neighbouring dimension: `step.ToLabel == "" || otherType == "" || otherType ==
  step.ToLabel`. Its own doc states the rule — *"cannot confirm the label must widen the set, not
  narrow it."*

An untyped hop is the same sentence about the relation dimension: *cannot confirm the relation ⇒
admit*. The mechanism to express it exists; only the arm that would set `Rel = ""` is missing.

This is the third time a conjunct of this predicate has been refused on a reason that a sibling in
the same package falsifies. Conjunct 3 (variable-length) was lifted on 2026-08-28 because
`rel_traverse.go` steps exactly the hop `hopindex.go` said could not be stepped. Conjunct 2 is the
same shape.

### 3.3 The `no-pattern:` tag's second half is already built

The row prescribes *"anchor derivation across an untyped relationship **at an unlabeled position**."*
An unlabeled position is already a first-class case:

- `PositionsBinding` (`hopindex.go:943-956`) — *"An UNLABELED position matches any type, which is what
  lets the derivation reach lenses whose label set is not exhaustive."*
- `stepAdmitsFarEnd` — `step.ToLabel == ""` returns `true`.
- `walkScope.wildcard` (`walkscope.go:96-104`) and `walkScopeRefusalUntypedHopUnlabeled`
  (`walkscope.go:264-272`) — the scope type **already models an untyped hop at an unlabeled position**,
  with its own refusal constant. Its `addIndex` doc says those arms are *"reachable only from a
  HopIndex built directly (the unit tests do)"* — dead scaffolding that this design makes live, which
  is the strongest available evidence that the wildcard hop is the shape the surrounding code was
  written to expect.

So the missing primitive is exactly one thing: **a hop that matches any relation**. Designing the
unlabeled-position half would have been building a second copy of shipped code.

### 3.4 What the refusal costs today — two mechanisms, one cause

For `objectLiveness` / `objectAttachments` (`actorType = "object"`, from
`InstallActorAggregate`'s `SetActorEnumerator(…, desc.AnchorType)`, `projection/driver.go:494`):

1. **The derivation declines.** `derivationIndex` (`anchor_derivation.go:137-151`) refuses on
   `!rs.anchorHops.Complete`, so `affectedAnchors` takes `enumerate(true)` — the `ActorEnumerator`
   BFS: undirected, depth-10, actor-cap 10 000, then a cypher re-execute per anchor it reached.
2. **The BFS is not even scoped.** `deriveWalkScope` (`walkscope.go:225-232`) refuses the same
   incomplete index, so `walkScope` is nil, which `walkscope.go:44-46` documents as *"allows
   everything"* — the pre-2026-09-01 **relation-blind** walk. Even had the index been readable, the
   untyped hop at the unlabeled `(owner)` would return `walkScopeRefusalUntypedHopUnlabeled` and
   still yield nil. The derivation is the only arm that can help these two.

And because `(owner)` is unlabeled, `ReferencedLabels` is non-exhaustive → `reprojectAll` → the
consumer takes the **broad** `$KV.core-kv.>` filter, so both lenses receive *every* Core-KV event and
pay a whole-component BFS for each.

Every act-gate conjunct other than `Complete` already holds for both lenses — traced end to end,
because a payoff claim is a soundness claim:

| Conjunct | Site | Holds? |
|---|---|---|
| `p.actorEnumerator != nil` | `anchor_derivation.go:138` | ✅ `driver.go:494` |
| `anchorHops.Complete` | `:141` | ❌ **the subject of this design** |
| `UnresolvedExpansionPosition() < 0` | `:144` | ✅ no `*` in either cypher |
| `Labels[Anchor] == actorType` | `:147` | ✅ `object` == `desc.AnchorType` |
| `p.patternClosedOutput` | `anchor_derivation_mode.go:240` | ✅ `driver.go:502` sets it for every actorAggregate |
| `p.sweeper != nil` | `:205-207` | ✅ `sweepEnrolment` passes: `OutputKeyPattern` `objectLiveness.{actorSuffix}` yields a prefix, round-trips through `AnchorFromKey`, and the `nats-kv` adapter is a `PrefixKeyLister` |
| mode is `act` | `anchor_derivation_mode.go:110` | ✅ `builtinDerivationMode = DerivationModeAct` |

---

## 4. The shape

Two increments, deliberately in this order: the first is package work that deletes a hop nobody
reads, and it removes most of the measured load with **zero platform surface**; the second is the
platform lift, sized against what remains.

### 4.1 Increment 1 — delete `objectLiveness`'s `OPTIONAL MATCH` (package, `objects-base`)

The alternatives table's first row, applied to the hop itself: *what does the world look like without
this thing?*

The hop is retained for three stated jobs (`packages/objects-base/lenses.go:14-22, 74-84`). Each was
re-derived rather than inherited:

1. **"collapse the link fan to exactly one row per anchor (the §0.C guard)."** Circular. Without the
   `OPTIONAL MATCH` there is no fan: `MATCH (o:object {key:$actorKey})` binds exactly one row, so the
   guard is satisfied by construction rather than by an aggregate.
2. **"drive the actorAggregate reprojection on any link create/tombstone."** Redundant, and the lens's
   own comment already says so — *"every attach/detach also rewrites the object vertex, so the anchor
   reprojects from the vertex CDC regardless."* Verified in code, not taken from the comment:
   `attach_object` writes the object vertex on all three arms (mint `ddls.go:650`, revive `:658`, live
   OCC-touch `:676`) and on the replace leg for the **prior** object (`:699`); `detach_object` writes
   it (`:731`). C5 (§2.5) establishes those are the only writers of a `lnk.object.*` key, and
   `objectmanager`'s owner-tombstone cascade reaches the link only by submitting `DetachObject`.
3. **`liveOwners`.** Neither returned, nor a body column, nor read by the orphan decision — the lens
   comment states this outright, and the `RETURN` list confirms it.

**The edit** is the deletion of one clause and one `WITH` item:

```
 MATCH (o:object {key: $actorKey})
-OPTIONAL MATCH (o)-[r]->(owner)
 WITH
   o.key AS entityKey,
   o.data.linkEpoch AS linkEpoch,
   o.data.liveLinks AS liveLinks,
-  o.content.data.storeName AS storeName,
-  count(owner.key) AS liveOwners
+  o.content.data.storeName AS storeName
 RETURN …unchanged…
```

**Payoff — a consumer-filter collapse, not just a cheaper walk.** With no relationship in the pattern:

- `ReferencedLabels` = `{object}`, **exhaustive** ⇒ `reprojectAll` false.
- `ReferencedRelations` returns `({}, exhaustive = true)` — `relations.go:42` seeds `exhaustive = true`
  and only `r.Type == ""` clears it, so a pattern with no relationship keeps it.
- `actorAwareNarrowingLabels` (`rulestate.go:301-333`) then passes every conjunct: full engine,
  not `reprojectAll`, `patternClosedOutput`, `sweeper != nil`, and `object ∈ reprojectLabels`.
- `ConsumerFilter` (`filter.go:356-367`) takes the **relation-narrowed** branch —
  `1 × (1 + 2×0) = 1 ≤ maxNarrowedFilterSubjects` — and
  `subjects.CoreKVRelationNarrowedFilters(bucket, ["object"], [])` emits exactly **one** subject: the
  `object` vertex form. **Not fail-open** (`subjects.go:297-311` loops over the relation list; an
  empty list emits no link subject at all, rather than rendering a wildcard — the empty-list hazard
  was checked explicitly).

The lens goes from receiving every Core-KV event to receiving only `vtx.object.*` (which, per
`filter.go:44-46`, already covers that type's 4-segment aspects). That is the 40 k backlog's delivery
side, deleted.

**Why dropping link delivery is sound.** The cypher binds no relationship, so
`linkRelationReactsTo` (`rulestate.go:241-250`) would return false for every relation anyway — the
server withholds exactly what the client arm already skips, which is the invariant
`NarrowedFilterEligible` is built on. The row's only input is `o.data.liveLinks`, and every link
mutation co-writes it (§4.1 item 2). Note precisely what this does **not** widen: the lens's
correctness already rests entirely on `liveLinks`, because `liveOwners` is discarded today. A future
op that mutated a `lnk.object.*` link without maintaining `liveLinks` would produce a wrong
`missing_owner` **today**, with or without this change. Increment 1 gives up an *incidental*
reprojection, which is exactly the trade `§4.2`'s standing-healer conjunct prices — and
`objectLiveness` is sweep-enrolled.

**What is NOT freed — stated because the draft claimed it and was wrong.** Footprint validation does
not run on this lens at all: `needsFootprintValidation` (`pipeline/evaluate.go:534`) requires
`p.authPlane`, which `projection.IsAuthPlane` (`projection/plan.go:110-113`) grants only to the
`capability-kv` bucket or a grant-table lens, while `objectLiveness` writes `weaver-targets`
(`packages/objects-base/lenses.go:29`). The untyped hop's fail-closed revision-comparison fallback
(`refractor-evaluation-consistency-design.md` §13.4) is inert here, so Increment 1 frees nothing on
that axis. The payoff is the consumer-filter collapse and the three gates in §6.1 — nothing else.

### 4.2 Increment 2 — the wildcard hop (platform, `internal/refractor`)

`objectAttachments` cannot take Increment 1's route (§8, alternative 2): `type(r)` **is** the
attachment slot and `r.data.filename` **is** the uploaded name — facts about the edge that no vertex
field can hold — and the slot vocabulary is caller-chosen at `AttachObject` (`valid_link_name`: any
`[a-z][a-zA-Z0-9]*`), so no closed relation set exists to name.

Four edits, three of them one line:

1. **`hopindex.go` `addPattern`** — replace the `case r.Type == "":` rejection with a hop carrying
   `Rel: ""`, folded into the existing fixed/ranged arms so a `-[r*1..3]->` untyped hop is still
   recorded as ranged. The comment states what `Rel == ""` means and that the walk reads it as
   *admit-any*, mirroring `stepAdmitsFarEnd`'s label sentence.
2. **`hopindex.go` `AnchorSideSeeds`** (`:995`) — `if h.Rel != rel { continue }` becomes
   `if h.Rel != "" && h.Rel != rel { continue }`. A wildcard hop is a candidate for every relation.
3. **`anchor_derivation.go` `edgeTakesStep`** (`:409-411`) — `(step.Rel == "" || e.Name == step.Rel)
   && DirectionMatches(…)`. Direction is still honoured: `(o)-[r]->(owner)` is directed, and an
   untyped hop says nothing about the arrow.
4. **Nothing in `walkscope.go`** — `addIndex` already branches on `h.Rel == ""` into `wildcard` and
   `walkScopeRefusalUntypedHopUnlabeled`. Its doc comment claiming those arms are reachable only from
   a directly-built index is the one line that changes there.

`StepsFrom` needs no edit: it copies `h.Rel` into `PatternStep.Rel` verbatim, and `edgeDirFor` is
relation-independent.

**Soundness.** The derivation's invariant is that the derived anchor set is a **superset** of the
truly-affected anchors; under-approximation is the only forbidden direction. A wildcard hop only ever
*admits* edges a typed hop would have rejected, so it moves the set in the safe direction.

**And the proposed predicate is the executor's own, character for character.** `rel_traverse.go:76`
— the single per-node edge loop every hop (fixed and ranged alike) goes through — reads:

```go
if rel.Type != "" && e.Name != rel.Type {
    continue
}
if !adjacency.DirectionMatches(e.Direction, rel.Direction.String()) {
    continue
}
```

Edit 3 makes `edgeTakesStep` the same two conjuncts. The derivation is not being taught a new,
independently-fallible rule about untyped relations; it is being brought into agreement with the
matcher that decides what the executor actually binds — which is the standard this codebase holds
every "can only be reached via" claim to. (The neighbouring
`recordEdgeSelector` — `executor.go:846-869` — states the same semantics in prose: *"An untyped
selector (`rel.Type == ""`) consumes every edge on the node regardless of type."*)

**One caveat the matcher argument does not cover, and it belongs in the invariant rather than in a
footnote.** Recording the hop also adds an edge to the binding graph `distances()` walks
(`hopindex.go:532-549`), and `AnchorSideSeeds`' `consider` drops the endpoint whose distance is the
*larger* (`:987-992`). Adding an edge can only shorten distances, so in a graph of three or more
positions a wildcard hop incident to the anchor could push one endpoint's `Dist` below another's and
drop a seed a hopless index would have kept — and because `*Match` covers `OPTIONAL MATCH`
(`ast.go:50-53`) and `addPattern(p, true)` records it as `Binding: true`, the shortening edge need
not exist in the data. **Not reachable on today's corpus**: all three untyped-hop lenses are a single
hop over a two-position graph, where `consider(0,1)` gives `ds = 0 ≤ dd = 1` and always seeds the
anchor. But Increment 2 licenses the shape corpus-wide. So the invariant is stated as a **bound, not
a blanket**: *a wildcard hop is sound where it is the only hop between the anchor and a position it is
incident to*; anything wider needs the seed-both-endpoints treatment `consider`'s `ds < 0 || dd < 0`
arm already gives an incomparable pair. §10's census edit is what keeps the corpus inside the bound —
which is exactly why the skip it removes must not be replaced with a weaker one (§10, and the M3
finding in §12).

The two places an empty derived set could still be read as a licence to skip are both correct:

- **Link events.** `AnchorSideSeeds` returns empty only when neither endpoint's *type* is admitted by
  the hop's positions. For `objectAttachments` (`(o:object)-[]->(owner)`, `owner` unlabeled) a link
  seeds iff its source type is `object`; a `lnk.identity.X.holdsRole.role.Y` derives nothing, and the
  executor could not have bound it either — `o` must be an `object`. Correct skip.
- **Vertex/aspect events.** `PositionsBinding` returns the unlabeled position for **every** type, so a
  wildcard-hop lens with any unlabeled position can never derive empty on a vertex event. There is no
  new skip surface at all.

**Cost.** For `objectAttachments`, seeded at the unlabeled `(owner)` position by an arbitrary vertex
event: **one** memoised `adjacency.Neighbors` read of that vertex, then admit its `object`-typed
neighbours (`stepAdmitsFarEnd` prunes on `ToLabel == "object"`) — each of which is at the anchor
position and terminates without expanding. That replaces an undirected depth-10 BFS over the whole
reachable component.

**What is bounded, and what is not — the draft over-claimed here.** `DefaultDerivationReadCap`
(2 000) bounds **I/O only** (`anchor_derivation.go:212-226`); nothing bounds `anchors`. Because
`objectAttachments`' `Labels` are `["object", ""]`, `PositionsBinding` returns the unlabeled position
for *every* vertex type, so every Core-KV event seeds pos 1, and one adjacency read of a hub owner
admits one anchor per attached object — each a full cypher re-execute. The arm this replaces has an
explicit ceiling the derivation has no counterpart for: `DefaultActorMaxSet = 10_000`
(`actor_enumerator.go:97`), which **errors** at the cap rather than truncating (`:271`). This is not
a regression — the BFS reaches a superset of the same anchors and pays far more I/O to do it — but on
a high-degree owner the per-event *evaluation* count is roughly unchanged, and only the walk gets
cheaper. That matters because §11.1 already concedes the 40 k number may have expired: the honest
claim for Increment 2 is **I/O per event**, not reprojections per event.

**Blast radius is bounded by C1 (§2.1), C3 (§6) and C4 (§2.4):** exactly one lens's behaviour changes
(`objectAttachments`; two, if Increment 1 is not taken first), plus one plain-arm index that flips
`Complete` while every consumer of it refuses for an unrelated reason.

---

## 5. State-lifetime table

**None.** Neither increment introduces a registry, cache, latch, watch or accumulated set.
`PatternHop.Rel` is an existing field of an existing value type whose lifetime is already fixed: the
index is derived once per rule publication in `useFullEngineBranches` and published on `ruleState`
under `ruleMu` (`ruleinstall.go:433`), never mutated after publication, and re-derived unconditionally
on hot reload. The empty string is a new **value** in that field, not new state. This section exists
to say so explicitly rather than to leave the absence to inference.

---

## 6. Blast radius — every reader of `Complete`

C3 (§2.3) returns six non-test readers. **The two increments hand them two DIFFERENT index shapes**,
and the draft evaluated only one of them: Increment 2 produces an index `Complete` with a **wildcard**
hop (§6.2), while Increment 1 produces one `Complete` with **no hops at all** (§6.1). A zero-hop index
is not a special case of a wildcard-hop index, and three readers move on it.

### 6.1 Increment 1 — the zero-hop index. It IS posture-changing, in three places.

All three are sound — with no relationship in the pattern, no neighbour of any kind can move an
object's row — but the design must declare them, because §13 sizes the review off this paragraph.

| Reader | Today | After Increment 1 | Sound because |
|---|---|---|---|
| `derivationIndex` (`anchor_derivation.go:141`) | not ready ⇒ enumerator BFS | ready ⇒ the derivation acts | the anchor is pinned by `{key:$actorKey}` and there is nothing to walk; the derived set is the event's own object or empty |
| `ActorTypeBindsAnchorOnly` (`actor_enumerator.go:415-421`) → `oneKeyAnswerSound` | `false` (index incomplete) | **`true`** — one position, and it is the anchor. The lens gains **the one-key answer, the narrowest answer in the system** | `PositionsBinding("object")` is exactly `{Anchor}`, so an `object` event can only be its own anchor; `p.sweeper != nil` holds (§3.4) |
| `deriveWalkScope` (`walkscope.go:226`) | nil scope = **allows everything** (relation-blind) | a non-nil **empty** scope = **allows nothing** | no pattern path exists, so following no relation still reaches every anchor the pattern can reach — the scope's own soundness argument, with `k = 0` |

The middle row is the one that needed saying: the draft's §6 concluded that reader "cannot move for
any lens" — true of a wildcard hop, which changes `Hops` and not `Labels`, and **false of deleting a
position**. And the third row is why `actor_walk_scope_corpus_census_test.go` pins the empty string
rather than `scopeNil` afterwards (§10).

### 6.2 Increment 2 — the wildcard-hop index

Each reader evaluated on an index that is `Complete` **because** the untyped-hop arm was lifted:

| # | Reader | What it does with `Complete` | Verdict under a wildcard hop |
|---|---|---|---|
| 1 | `anchor_derivation.go:141` `derivationIndex` (`anchorHops`) | Gates the actor-aware derivation | **The intended change.** `objectAttachments` (and `objectLiveness` if Inc 1 is skipped) begins deriving. Sound per §4.2. |
| 2 | `anchor_derivation_plain.go:118` `plainDerivationIndex` (`rootHops`) | Gates the plain-arm derivation | **No live consumer.** The one plain untyped-hop lens, `objectIdentityAttachmentsRead`, declares `DiffRetraction: true` (`loftspace-domain/lenses.go:171` — that file carries a **second** `DiffRetraction: true` at `:112`, so cite the line, not the flag) and `plainDerivationIndex` refuses on `p.diffRetraction` (`:123`) — an independent gate — though **not a permanent one**: `DiffRetraction` is a *package declaration*, pinned by no lint, so an edit dropping it would silently arm the plain derivation on a Protected/RLS lens carrying a wildcard hop. Read it as "for as long as the package declares it", and note that after Increment 2 the `plain_scanroot` census row is the only thing standing there. `shadowPlainDerivation` and `seedMultiPosition` both route through the same function, so neither reaches it either. **The auth-plane exposure of Increment 2 on the plain arm is nil**, and it does not rest on `plainDerivationLicence`'s `p.authPlane` conjunct at all. |
| 3 | `walkscope.go:226` `deriveWalkScope` | Refuses a scope from an incomplete index | Now reaches `addIndex`, which **already** handles `h.Rel == ""`: `wildcard[t]` for a labeled incident position, `walkScopeRefusalUntypedHopUnlabeled` for an unlabeled one. `objectAttachments` hits the latter ⇒ nil scope ⇒ relation-blind BFS — **unchanged from today**, and only ever reached on the derivation's read-cap fallback. Consistent, not contradictory: the scope narrows a walk, the derivation replaces it. |
| 4 | `actor_enumerator.go:413` `ActorTypeBindsAnchorOnly` (via `oneKeyAnswerSound`) | Licenses the **one-key answer** — the narrowest answer in the system | Reads `Labels` and `Anchor` only; `Hops` never enters it. A wildcard hop cannot move its verdict for any lens. For `objectAttachments` specifically it returns **false**: `PositionsBinding("object")` = `{0, 1}` (pos 1 is unlabeled and admits every type), which is correct — an object may be attached to another object. |
| 5 | `anchor_derivation_mode.go:208` `noteStaticDerivationRefusal` | Chooses a **log reason** | Reason string only; the untyped reason simply stops being emitted. |
| 6 | `anchor_derivation_plain.go:880` `noteStaticPlainDerivationRefusal` | Log reason only | Same. |

**The dead-scaffolding direction is inverted here, and worth stating:** reader 3 is scaffolding that
has been dead since it shipped. Increment 2 does not build inert machinery — it makes existing inert
machinery reachable, which is the strongest evidence available that the wildcard hop is the shape the
surrounding code was designed against.

---

## 7. Reconciliation with the existing mental model

**"Didn't we already handle this?"** Partly, and the parts are worth naming. The *unlabeled-position*
half is shipped (§3.3). The *variable-length* half of the same predicate was lifted on 2026-08-28 by
`varlength-anchor-derivation-design.md`, on an identically false reason. The *relation-scoped walk*
(`walkscope.go`, 2026-09-01) helps every lens whose hops are typed and explicitly cannot help these —
`walkScopeRefusalUntypedHopUnlabeled` is that admission, written down at the time. What was never
built is a hop that matches any relation.

**Does it duplicate or contradict an established pattern?** No. It extends the same
`PatternHop`/`PatternStep` pair the varlength lift extended, in the same two consumer functions, and
reuses `stepAdmitsFarEnd`'s existing "cannot confirm ⇒ widen" rule verbatim in the relation
dimension. It contradicts one shipped sentence — `auth-plane-projection-latency-design.md` §16.2
conjunct 2 — which §9 amends in place, mirroring that document's own 2026-08-28 amendment of
conjunct 3.

**Does it introduce new state?** No (§5).

**Does it collide with an in-flight design?** Checked against the committed docs **and against the
dirty tree**, since a staged contract edit is the newest thing in flight and invisible to `git log`.
`git status` at fire time carries four uncommitted contract edits — `02-operation-envelope.md`
(declared paths, class (h)), `10-orchestration-loom.md`, `10-orchestration-substrate.md`,
`10-orchestration-weaver.md` — all belonging to Processor/Loom/Weaver designs. None touches Refractor
derivation. Two Refractor designs are adjacent and disjoint:

- **`personal-lens-derivation-licence-design.md`** (📐 awaiting-Andrew) — §4.5 names these two lenses a
  **non-goal** and routes them to their own row, which is this one. Its subject is `p.patternClosedOutput`
  and the personal plane's licence conjuncts, not `Complete`.
- **`refractor-hub-walk-and-periodic-load-design.md` §9** — 🏗️ **building right now**
  (`owner: steward-hub-read-scope`, admitted `33902f3e` while this design was being drafted). It
  scopes `fetchEdges`' marked-hub read to the hop's relation and takes the selector-scoped footprint
  with it. **Adjacent but disjoint**: it edits the *executor's* read path
  (`ruleengine/full/executor.go`, `adjacency.NeighborsByRelation`); this design edits `hopindex.go`
  and `anchor_derivation.go` and touches neither. The one contact point is §3.2's citation of
  `edgesOf` → `adjacency.Neighbors` in the **derivation's** walk, which is a different call site from
  the executor's `fetchEdges` and is not in that fire's touch-list. If that fire lands first, re-read
  §3.2's citation before quoting it — the *derivation's* walk deliberately reads the whole adjacency
  document and filters, and a reader who has just learned the executor no longer does could
  reasonably think this design is describing stale behaviour.
- **`varlength-anchor-derivation-design.md` §13 Inc 2** (📋 ready) — narrows the **`WITH`-scope**
  refusal (`withReject`) to a structural-identity whitelist. A different conjunct of the same switch,
  a different builder path (`withscope.go`), no shared line. The two can land in either order; if
  Inc 2 lands first, the corpus census tables this design edits will already have moved, and the
  builder should re-run C1/C2 rather than apply the tables as written.

**Contract prose.** No `docs/contracts/*` sentence describes `AnchorHopIndex`, `Complete`, or the
consumer-filter derivation — correctly, since all of it is mechanism a pure refactor could falsify.
Nothing to amend.

---

## 8. Alternatives considered

**Row 1 — do not have this thing: keep the refusal, delete nothing, build nothing.**
The world without either increment is the measured one: three lenses on the broad filter, two of them
paying an unscoped depth-10 BFS per Core-KV event, ~40 k messages backlogged each at the 2026-09-01
sample. The cost of *keeping* the refusal is not merely those two lenses' latency — it is that the
next lens written with an untyped hop inherits the same cliff invisibly, and that `walkscope.go`'s
untyped-hop arms stay unreachable code. Nothing forbids removing the refusal: it guards no invariant
(§4.2 soundness), and the walk it feeds already implements the discipline a wildcard needs. **Priced
and rejected.**

**2 — Type `objectAttachments`' relationship (demand-side, no platform change).** The mandatory
alternative whenever the consumer census is single-digit, and here the census is **one lens**. It
fails on the data, not on effort: `type(r)` is a returned column whose values are the attachment
*slots*, chosen per call by `AttachObject`'s `linkName` argument under an open grammar
(`valid_link_name`), so there is no set to enumerate; and `r.data.filename` is a property of the edge
that, per the lens's own comment, no vertex field can carry (*"one object attached to one owner under
two slots is two links and one object vertex"*). Naming the relation would change what the lens
projects. **Rejected on expressibility**, and it is the reason Increment 2 exists at all.

**3 — Split `objectAttachments` into per-relation typed lenses.** Requires `objects-base` to learn
concrete slot names, which `package.go` states it must never do (*"it never learns concrete owner
types"*), and multiplies the lens corpus by the open slot vocabulary. **Rejected.**

**4 — Do Increment 1 only, and accept `objectAttachments`' backlog.** Tempting, because Increment 1
carries no platform surface and removes the larger share of the load. But the objection that kills
alternative 1 applies unchanged to the residue, and the residue is the lens the vertical apps actually
read (LoftSpace's Documents tab). Running each rejected alternative's objection back against the
recommendation: alternative 2 was rejected for changing what a lens projects — Increment 2 changes
what no lens projects, and Increment 1 changes only a column already proven unread. **Rejected as the
whole answer; retained as the first increment.**

**5 — Also relax `anchorwalk.go`'s untyped-hop parse refusal for generated lenses.** Explicitly
**not** proposed. That refusal's stated reason is about *grant breadth* — *"an untyped hop would
traverse every relation and grant more than the walk declares"* — which is a security claim about
what a generated read-grant lens confers, not a claim about whether a hop can be walked. It is true
and should stand. Naming it here so the next reader does not mistake this design for a licence to
touch it.

**6 — Widen the walk instead: seed both endpoints for any link on a lens with an untyped hop.** A
cheaper-looking lift that skips `edgeTakesStep`. Rejected: it fixes only the *link* arm, leaves the
vertex arm (which is where the 40 k lives, since every Core-KV event reaches these lenses) unimproved,
and leaves `Complete` false so `walkscope.go` and the log reasons stay wrong. It buys a fraction of
the payoff for two thirds of the edit.

---

## 9. Contract + document surface

- **`docs/contracts/*`** — **untouched.** No sentence there describes this mechanism.
- **`_bmad-output/implementation-artifacts/auth-plane-projection-latency-design.md` §16.2 conjunct 2**
  — carries the false reason. It gets an **amendment note in place**, in the exact form conjunct 3
  received on 2026-08-28 (`> **AMENDED 2026-09-01 — conjunct 2 above is falsified.** …`), pointing at
  this document. A banner without a body rewrite is what let the `hasBooking` inversion ship two days
  after its withdrawal; the note is written into the conjunct itself, not above the section.
- **`docs/components/refractor.md`** — smaller than it looks, and the difference matters. `:762`'s
  walk-scope paragraph (*"… or an untyped relationship at an unlabeled position"*) stays **true**
  after Increment 2: that sentence is about the *walk scope*, which still refuses. What changes is
  only that the `Incomplete` case beside it stops being the arm that catches these lenses first. `:571`
  (`type(r)` for an untyped hop) is unaffected. The edit is one added sentence saying the anchor
  derivation now walks an untyped hop while the scope still cannot — the two arms disagree by design,
  and a reader who does not know that will read the next census table as a bug.
- **`packages/objects-base/lenses.go`** — the `objectLiveness` doc comment's three-job justification
  for the hop is deleted with the hop (Increment 1), not left standing over a removed clause.
- **`internal/refractor/plain_scanroot_corpus_census_test.go:250-256`** — its comment asserts *"The
  live corpus happens to carry zero untyped-hop plain lenses today"*, which the pin **two lines below
  it** already falsifies (`objectIdentityAttachmentsRead`). Stale since that lens shipped. Corrected
  in Increment 2 whatever else is decided — a comment that reassures wrongly is how the next fire
  inherits a false census.
- **Package version bumps** — Increment 1 edits `packages/objects-base` content, so the manifest
  version **and** the mirroring `Version` constant must move
  (`DIFF_BASE=<base> go run ./scripts/lint-package-version.go`). Increment 2 is `internal/` only and
  bumps nothing.

---

## 10. Test strategy

Every test below is **owned by a named increment**; none is left unowned.

### Increment 1 (`packages/objects-base`)

- **`lens_cypher_test.go` — the existing `objectLiveness` cases run unchanged** against the de-hopped
  spec, including the dead-target and zero-link vectors. Their passing *is* the proof that no returned
  column depended on the hop.
- **`TestObjectLiveness_NamedRelationshipDoesNotMoveTheProjectedRow` (`:516`) is retired with the
  clause it pins** — it compares `-[r]->` against `-[]->`, and neither exists afterwards. Deleting a
  test whose subject is gone is correct; silently leaving it comparing two spellings of a removed
  clause is not.
- **New — `TestObjectLiveness_LinkMutationCoWritesTheHydratedObjectVertex`.** Drives `AttachObject`
  (mint / revive / live arms), `AttachObject` with `replaceOid`, and `DetachObject` through the real
  DDL, asserting that every batch mutating a `lnk.object.*` key **whose object vertex is in the
  batch's read set** also writes `vtx.object.<oid>`. **The invariant is that conditional, not the
  unconditional one the draft prescribed:** `detach_object`'s vertex write is gated on
  `present(state, obj_key)` and the replace leg's on `present(state, old_obj_key)`, and the object
  vertex arrives via `optionalReads` (`ddls.go:554`) — absence-tolerant by contract — while the
  replace leg's `old_obj_key` is not in `derive_reads` at all and comes from the caller's
  `contextHint`. An unconditional assertion is red on the `present == False` vector, and a builder
  handed a red test weakens it rather than narrowing it. The soundness argument is unaffected: an
  un-hydrated object vertex has its `liveLinks` left unchanged too, so the lens's sole input is
  exactly as (in)accurate as it is today.
- **New — `TestObjectLiveness_ConsumerFilterIsRelationNarrowed`.** Asserts the installed lens's
  `ConsumerFilter()` returns `FilterModeNarrowedRelation` with exactly one subject. Assert on the
  **filter decision**, not on a projection: a projection assertion would pass identically if the
  narrowing never happened.

### Increment 2 (`internal/refractor`)

- **`full` unit — `TestAnchorHopIndex_UntypedHopIsAWildcard`:** an index over
  `(a:object {key:$actorKey})-[r]->(b)` is `Complete`, carries one hop with `Rel == ""`, and
  `AnchorSideSeeds("object", <any relation>, "identity")` seeds the anchor side. Plus the negative
  vector that gives the positive one meaning: `AnchorSideSeeds("identity", …, "role")` is **empty**,
  so the wildcard admits by relation and still discriminates by type.
- **`pipeline` unit — `TestWalkToAnchors_WildcardStepFollowsEveryRelation`:** a seeded walk across a
  `Rel == ""` step admits neighbours over two different relation names and still prunes on the far
  end's label. **Mutation-proven:** revert `edgeTakesStep` to bare equality and the test must go red,
  or it is asserting nothing.
- **Corpus census, in the same fire (the component's standing rule).** **NINE** pinned tables and
  tests move — the draft named three, and a `go test ./...` that passes with the other six unedited
  does not exist, so this list is the increment's real size. Every one must be *edited*, not skipped:
  - `anchor_hopindex_corpus_census_test.go` — `objectLiveness` / `objectAttachments` leave the
    `hopUntypedHop` rows (to `""` / complete); `hopUntypedHop` is struck from
    `TestCorpusAnchorHopIndex_EveryReasonIsAKnownConjunct`'s vocabulary, since the reason string no
    longer exists and a default-deny list carrying a dead constant admits nothing but confusion.
  - `TestCorpusAnchorHopIndex_CompleteIndexHoldsEveryReferencedRelation` — its `if !exhaustive
    { return }` skip (`:245-247`) and the comment justifying it (*"an untyped or variable-length hop,
    and both refuse the index on their own conjunct"*) become **false**. **DELETE the skip and leave
    the per-relation `Contains` loop unconditional.** Do **not** replace it with "a non-exhaustive
    lens passes iff its index carries a wildcard hop" — that rule is unsound and the draft proposed
    it. `ReferencedRelations` clears `exhaustive` on *any* untyped hop while still returning every
    **typed** relation it found (`relations.go:38-42`), so
    `MATCH (a:X {key:$actorKey})-[:foo]->(b) OPTIONAL MATCH (a)-[r]->(c)` returns
    `({foo}, exhaustive=false)`; the wildcard-presence rule would pass it without ever checking
    `foo` against `indexed`, reinstating verbatim the hazard the test names — an unindexed relation
    on a Complete index makes `AnchorSideSeeds` answer empty, which the derivation reads as "no
    anchor can change" and **skips**. A `Rel == ""` hop is a distinct key in `indexed` and covers no
    named relation, so the unconditional loop is both correct and the thing that keeps the corpus
    inside §4.2's one-hop soundness bound.
  - `plain_scanroot_corpus_census_test.go` — `objectIdentityAttachmentsRead`'s `rootUntypedHop` pin
    moves to complete; `rootUntypedHop` leaves the known-reason list; the stale comment (§9) is fixed.
  - **`actor_walk_scope_corpus_census_test.go` — the fourth table, and the one easiest to miss.**
    `corpusActorWalkScopeRefusals` today pins `objectLiveness` and `objectAttachments` as
    `"a branch's pattern graph is incomplete"` (`:174-175`). After Increment 2 both are still
    `scopeNil`, but for a **different published reason** —
    `walkScopeRefusalUntypedHopUnlabeled` — and `TestCorpusActorWalkScope_EveryRefusalIsKnown`
    matches the vocabulary by **equality**, so an un-updated table fails there rather than silently
    passing. **After Increment 1, `objectLiveness` leaves `scopeNil` entirely**: with no hops at all
    `addIndex` folds in nothing, so `corpusActorWalkScopeDigests` pins the **empty string**, not
    `scopeNil = "nil"` (`:69`). An empty digest is a real, distinct state — "a scope that follows no
    relation from any type" — and it is correct: no pattern path exists, so following nothing reaches
    every anchor the pattern can reach. It must be pinned as `""` deliberately, with that sentence
    beside it, or the next reader will read the blank as an omission and "fix" it.
  - `actor_onekey_corpus_census_test.go:137-138` — both lenses are pinned `walkIncompleteIndex`.
    Increment 1 moves `objectLiveness` to the **one-key** verdict (§6.1); Increment 2 moves
    `objectAttachments` to multi-position.
  - `rel_projection_corpus_census_test.go:50-52, 122-124` — pins `"objectLiveness": "r:*[]"` **and**
    `TestCorpusRelBindings_BindingLensesAreTheKnownPopulation`'s closed population list
    `{objectAttachments, objectIdentityAttachmentsRead, objectLiveness}`. Increment 1 removes
    `objectLiveness` from the population outright — a guaranteed red on an unedited table.
  - `label_derivation_corpus_census_test.go:286` — `"objectLiveness": {broad, "object", modeBroad}`
    becomes `{narrow, "object", modeRelation}`. This is the pin that *proves* Increment 1's headline
    payoff, so it is an assertion to move deliberately, not a table to patch.
  - `grouping_reduction_corpus_census_test.go:150` — `"objectLiveness"`'s grouping key
    `key(entityKey linkEpoch liveLinks storeName) p!liveLinks` is derived from the aggregate
    Increment 1 deletes.
  - `branch_decomposition_corpus_census_pins_test.go:94, 248` — the
    `multiplicity-sensitive-aggregator` unit and `"objectLiveness": false` both describe the `count()`
    that goes away.
  - `ruleengine/full/hopindex_test.go:954` — the unit vector named *"untyped relationship —
    objectAttachments' shape"* pins the very refusal Increment 2 lifts; it inverts into the new
    wildcard assertion rather than being deleted.
- **Ephemeral-stack e2e — `TestObjectAttachments_DerivationActsOnANeighbourEvent`:** attach an object
  to an identity, mutate an unrelated aspect of that identity, and assert the lens's
  act-mode tally move `Acted` and not `FellBack` (`derivShadow.stats`,
  `anchor_derivation_shadow.go:303/312`), and that the projected row converges. Assert on the
  **tally**, not only on convergence — convergence would also pass under the BFS, which is what makes
  a convergence-only assertion a false pass.

### Gates

`go build ./...` · `make vet` · `golangci-lint run ./...` · `make verify-kernel` ·
`go test ./internal/refractor/... ./packages/objects-base/... ./packages/loftspace-domain/...` ·
every `scripts/lint-*.go` · `DIFF_BASE=… go run ./scripts/lint-package-version.go` (Increment 1) ·
the build-tagged harnesses reachable from these changes
(`grep -rl "^//go:build " --include=*_test.go internal/`).

**`make test-object-gc` is REQUIRED for Increment 1 and the draft steered away from it.**
`internal/objectgc/objectgc_test.go` is `//go:build objectgc`, so `go test ./...` never compiles it —
and it is the *only* end-to-end harness of exactly the loop Increment 1 edits: `objectLiveness` →
`weaver-targets` → Weaver `directOp(TombstoneObject)` → the manager's byte reclaim. It runs in CI
(`.github/workflows/ci.yml:389` → `Makefile:1895`). A green local tree with this gate unrun is the
precise shape of "a green tree is not evidence CI will pass"; the `*-convergence` tags the draft named
do not reach it. `.github/workflows/ci.yml` is the authority for the rest.

---

## 11. Measurement, acceptance, and what must be re-derived first

### 11.1 The 40 k figure is a premise, not a payoff

The row's *"40 k backlogged each"* is a single live snapshot from the 2026-09-01 incident
(`nats consumer report KV_core-kv`, 24–25 backlogged consumers / 1.2 M messages), recorded in
`refractor-hub-walk-and-periodic-load-design.md` §8. Two things about it are not established:

- **Its units are pending CDC messages on that lens's Core-KV consumer**, not events per second and
  not a steady state.
- **Its attribution has already moved once** (that same doc corrected these two lenses out of the
  personal-lens row on the same day), and the bulk of the incident's backlog was attributed to a
  *different* root cause — the enumerator's descriptor-hub fan-out — whose fix shipped in the same
  fire (`1fca25cf` and siblings) with these two lenses **never re-measured afterwards**.

**So the fire's Phase 0 re-runs the measurement before sizing anything**, and the design's payoff
claim is the *mechanism* (a whole-component BFS per event becomes one adjacency read; a broad filter
becomes one subject), not the number. If the post-fix backlog on these two consumers has already
drained, Increment 2's justification narrows to the per-event cost and the standing cliff for the next
untyped-hop author — still sufficient, but Andrew should see that honestly rather than ratify a
headline that may have expired.

### 11.2 Acceptance

| Signal | Where | Expected |
|---|---|---|
| `pipeline: anchor derivation cannot act on this lens` naming the untyped reason | refractor log | **gone** for all three lenses |
| `acted` vs `fellBack` on `objectAttachments` | the act-mode summary line (`logActSummaryIfDue`, `anchor_derivation_shadow.go:303/312`) | acted ≫ fellBack; `walkScoped` still **false**, which is correct — the scope and the derivation are independent arms and only the derivation moves here |
| `objectLiveness` filter mode | its health entry | `FilterModeNarrowedRelation`, `LabelCount: 1` |
| Consumer backlog, both lenses | `nats consumer report KV_core-kv` | strictly falling vs. the Phase-0 re-measure |
| Projected rows | `weaver-targets` | byte-identical before/after for both lenses |

### 11.3 Risks

- **An over-narrow consumer filter is unrecoverable by revert** (`filter.go`'s own warning: a
  JetStream filter update never rewinds the cursor). Increment 1 narrows a filter, so its recovery
  path is `Pipeline.Rebuild` or the convergence sweep — both installed on this lens. The increment
  must not ship without the sweep-enrolment assertion in
  `TestObjectLiveness_ConsumerFilterIsRelationNarrowed`.
- **`REFRACTOR_ANCHOR_DERIVATION=off` is the rollback for Increment 2**, and it routes to the
  enumerator, which since 2026-09-01 is itself scoped — `REFRACTOR_WALK_SCOPE=off` is the second lever
  needed to reach pre-§5.1 behaviour. Both are existing knobs; Increment 2 adds none and the build
  note should re-verify the documented pair.
- **Increment order matters for the census tables**, not for correctness: taking Increment 1 first
  removes `objectLiveness` from Increment 2's beneficiary list, so the census edits differ depending
  on order. Take them in the order given.

---

## 12. Adversarial pass

Run in this fire (§3's requirement for a cross-cutting design), against the finished draft. It
returned **two blockers and five majors**, every one of which changed the document. They are listed
rather than silently folded, because what they have in common is the lesson: the draft reasoned
carefully about the *mechanism it was adding* and loosely about *everything that reads the thing it
was adding it to*.

**B1 — the census blast radius was under-counted by a factor of three, and a required CI gate was
steered away from.** The draft named three pinned tables; **nine** move
(`actor_onekey`, `rel_projection` — which pins a *closed population list* Increment 1 removes a member
from — `label_derivation`, `grouping_reduction`, `branch_decomposition` ×2, `hopindex_test`, plus the
three named). And `make test-object-gc` — build-tagged `objectgc`, invisible to `go test ./...`, and
the only end-to-end harness of the exact loop Increment 1 edits — was outside the gate list, while the
`*-convergence` tags the draft did name do not reach it. §10.

**B2 — "Increment 1 is not posture-changing on the derivation" was false three times over.** The
draft's §6 evaluated every `Complete` reader against a *wildcard-hop* index and then applied the
verdict to Increment 1, whose index has **no hops at all** — a different shape. Deleting a pattern
position moves `PositionsBinding`, so `objectLiveness` gains the **one-key answer**; `derivationIndex`
arms; and the walk scope flips from nil-allows-everything to empty-allows-nothing. All three are
sound; none was declared, and §13 sized the review off the sentence that denied them. §6 is now split
into 6.1 (zero-hop) and 6.2 (wildcard).

**M3 — the replacement census rule the draft proposed was itself unsound.** *"A non-exhaustive lens
passes iff its index carries a hop with `Rel == \"\"`"* would admit a lens carrying a wildcard hop
**and** a separate typed relation the index failed to record — reinstating verbatim the skipped
reprojection the test exists to catch. The right edit is to delete the skip and leave the loop
unconditional. A weaker gate proposed *in the same design that widens the population the gate
governs* is the worst available outcome, and the draft nearly shipped it.

**M4 — a payoff line that does not exist.** "Freed from footprint validation" was inferred from the
evaluation-consistency design rather than from `needsFootprintValidation`, which requires
`p.authPlane`; `objectLiveness` writes `weaver-targets`, so validation never ran on it. Struck.

**M5 — the prescribed test pinned an invariant the DDL does not hold.** Both vertex co-writes are
gated on `present(state, …)` and the object vertex arrives through `optionalReads`, so an
unconditional assertion is red on a legitimate vector — and a builder handed a red prescription
weakens it rather than narrowing it. Narrowed to the hydrated case; the soundness argument is
unaffected.

**M6 — the soundness argument read the matcher and not the index.** Recording the hop also shortens
`distances()`, and `AnchorSideSeeds`' `consider` drops the farther endpoint, so on a ≥3-position graph
a wildcard hop could drop a seed. Not reachable on today's two-position corpus — but Increment 2
licenses the shape corpus-wide, *and M3 would have removed the census that catches it*. The invariant
is now stated as a bound, not a blanket. The two findings compound, which is exactly why they were
found by the same pass and not by the checklist.

**M7 — a bound that bounds something else.** `DefaultDerivationReadCap` caps **I/O**, not the anchor
set; the arm being replaced has an explicit `DefaultActorMaxSet` ceiling that errors, and the
derivation has no counterpart. Increment 2's honest claim is I/O per event, not reprojections per
event — which matters because §11.1 already concedes the headline number may have expired.

**m8** — `DiffRetraction` is a package declaration pinned by no lint, so "permanent gate" was too
strong (§6.2 row 2). **m9** — four file:line citations had drifted; corrected.

**Claims that survived the attack**, stated because a clean verdict on a hard claim is worth as much
as a finding: C1's three-lens census re-run verbatim; §2.2's hand-evaluation of `withScopeReject`;
the empty-relation-list fail-open check (`subjects.go:297-313` loops, it does not wildcard);
`CoreKVVertexFilter` covering the `.content` aspect; `actorAwareNarrowingLabels` conjunct by conjunct
including the secure-holder loop after `:333` (vacuous — `SetSecureDecryptor` is called only under
`len(Into.SecureColumns) > 0`); the filter arithmetic (`1 ≤ 24` subjects, `1 ≤ 8` labels); the
soundness of dropping link delivery; the `.Complete` and `.Hops` censuses being complete; that
removing the `OPTIONAL MATCH` moves neither the projected row nor `actorKey` nor any column the Weaver
target reads; and that Increment 2 realises value for `objectAttachments` despite `reprojectAll`,
since `affectedAnchors` is reached unconditionally and `reprojectAll` costs the broad filter, not the
derivation.

---

## 13. Decomposition for the Steward

**Increment 1 — de-hop `objectLiveness`** *(**M**, not S; the cypher edit is two lines and the census
is the increment)*
Edit `objectLivenessSpec` + its doc comment; delete the retired pin test; add the two new tests (§10);
move the **six** pinned corpus tables Increment 1 touches (§10 — `actor_onekey`, `rel_projection`'s
value pin *and* its closed population list, `label_derivation`, `grouping_reduction`,
`branch_decomposition` ×2, `actor_walk_scope`); bump the `objects-base` manifest version and the
mirroring `Version` constant; run **`make test-object-gc`**. Independently
shippable and green; realises its whole payoff (the consumer-filter collapse) alone, with no platform
dependency. **Posture-changing on three axes at once** (§6.1) — it arms the derivation, hands the lens
the one-key answer, and turns a nil walk scope into an empty one — *and* it narrows a consumer filter,
whose recovery is a rebuild rather than a revert (§11.3). Full review pass, and the reviewer's brief
should name §6.1's table, not only the filter.

**Increment 2 — the wildcard hop** *(S–M; platform)*
The four edits of §4.2; the amendment note in `auth-plane-projection-latency-design.md` §16.2; the two
new units (the pipeline one mutation-proven); the remaining corpus-census edits — `anchor_hopindex`'s
two tables, `plain_scanroot`, `actor_walk_scope`'s refusal strings, `hopindex_test.go:954`, and the
**unconditional** rewrite of `CompleteIndexHoldsEveryReferencedRelation` (§10, and M3 in §12: do not
substitute the wildcard-presence rule) — plus the stale-comment fix and
`docs/components/refractor.md`. **Posture-changing** — it lifts a fail-closed conjunct on a predicate
the auth plane shares — so it takes the full review pass, and the reviewer's brief should name §6.2's
table and §4.2's `distances()` bound as the things to attack.

Review depth beyond those two calls is the Steward's sizing (`agents/steward/SKILL.md` §4); there is
no blanket every-increment-full-depth clause here.
