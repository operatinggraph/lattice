# A pattern label names a vertex key type — retiring the body-`class` binding fallback

**Status: ✅ Andrew-ratified 2026-08-06** — both increments, one fire; build-ready. Designer fire
2026-08-02 · owner: Refractor (rule engine) · Size **S** · Imp **★★★**

## Ratification (Andrew, 2026-08-06)

**Ratified as designed: delete the body `class`/`label` fallback, and fold the OPTIONAL/negated-`WHERE`
derivation hole in as Increment 2.** Two findings from the session strengthen the case beyond what the body
argues, and both are worth carrying into the fire:

- **The fallback is not merely unsound-in-principle, it is unused.** Every pattern label in every shipped
  lens (all of `packages/*/lenses.go` plus the kernel lenses) was enumerated: **34 distinct labels, every
  one of them a real vertex key type** — `account`, `appointment`, `building`, `identity`, `meta`, `unit`,
  `role`, `task`, and so on. Nothing labels a node `:location`. And the second fallback branch is **dead
  code**: no `packages/**` vertex body carries a `label` property at all (the only `"label"` occurrences are
  a pane spec's *column* label and an unrelated subject-token validator). So Increment 1 removes
  dead-and-dangerous code rather than changing live behavior.
- **The conflation is already in the vocabulary of the lenses that decide read access.** clinic-domain's
  read-grant lenses reason in comments like *"only ever matches class=identity, so it does NOT grant a
  patient (class=patient) its own anchor"* (`packages/clinic-domain/lenses.go:120-121`, `:293-303`) —
  describing a **key-type** match as a class match, in security-critical design notes. Making a label mean
  the address is what lets that vocabulary be precise.

**The judgment call in §8.3 is settled, and the other way round from the sketch.** The removed capability
("any location" via a shared class discriminator) is real demand, and Andrew wants it — but neither the
label form nor §8.3's **label disjunction** is the right mechanism, and §8.3 is superseded rather than
merely unbuilt. It resolves as a **dynamic type taxonomy**: a `specializes` link between the *type meta
vertices*, so a new leaf (`room`, `hallway`) can be declared **by a different package** and picked up by
any lens labelling the abstract type, with no lens edit and no redeploy. That is its own design
(`dynamic-type-taxonomy-design.md`, filed this session), and it **depends on this fire**: label resolution
must have exactly one authority, so an abstract label may expand against the declared taxonomy only once
the body `class` is out of the resolution path. Do not build §8.3's disjunction.

**The location-domain question is absorbed there too, not filed as a rename.** Andrew's first instinct was
dotted classes (`location.unit`), then collapsing the three key types into one `vtx.location`. Both were
set aside on architectural grounds, not cost (cost is explicitly not a criterion): the key type is the only
thing that can be a *subscription* filter — the Core-KV subject is derived from the key and NATS filters on
subject tokens, so a body field can never narrow delivery. Collapsing the types would therefore lose
per-type narrowing permanently. Under the taxonomy the leaf types stay as they are, `location` becomes a
declared abstract type, the shared bare class is dropped, and `loftspace-domain:373`'s `cls != "location"`
guard becomes a taxonomy check.

**No frozen-contract change; nothing staged uncommitted.** Contract #1 §1's existing sentence — the type
segment is a coarse routing category, fine-grained classification lives in `class` — is what this brings
the engine into line with.

## For Andrew

**What it does.** `nodeMatches` is the only place in the platform that lets a cypher pattern label bind a
vertex whose **key type** is not that label — it also accepts a vertex whose *body* `class`/`label` field
equals the label. Every other mechanism that reads a label (seed scan, event seeding, anchor retraction, the
D1/auth-plane narrowing derivation, the designed divergence audit) treats a label as the Contract #1 **key
type**. This design **removes the body fallback** so the binder agrees with the five consumers that already
assume it, which makes the shipped narrowing sound instead of accidentally-sound. It also fixes the one
remaining derivation-side under-approximation (the OPTIONAL/`WHERE`-label row) in the same fire.

**No architectural fork.** **No frozen-contract change** — Contract #1 §1 already says the type segment is
*"a coarse routing/filtering category"* and *"fine-grained classification lives in the document's `class`
field"*; this brings the engine into line with that sentence rather than amending it. Nothing is staged
uncommitted.

**The one judgment call you may want to weigh in on** (not a fork — I have decided it, and it is cheap to
reverse): this **removes an unused expressive capability** — a lens can no longer write `MATCH (l:location)`
to match sibling key types (`unit`/`building`/`property`) by their shared class discriminator. Two censuses
show **zero** lenses use it; the equivalent `MATCH (l {class: "location"})` property form still works in
both seed and traversal position, where the label form is *already broken in seed position* and already
invisible to retraction and event seeding (§4.2); and the sound way to express the shared-discriminator
need if it ever becomes real is a **label disjunction**, designed here and deliberately **not built** (no
consumer — §8.3).

**One scope note.** The design also folds in the board's sibling `📋 ready` row (the OPTIONAL/`WHERE`-label
derivation gap) as Increment 2 — same hazard class, same file, same full-suite gate — and **rejects the
one-line fix that row carries**, which would cost 9 lenses their narrowing. That row is repointed at this
doc in the same commit so a Steward cannot pick up the superseded shape.

---

## 1. Problem + intent

The board row (`backlog/lattice.md`, ★★★, `📐 needs-design`) reads:

> `nodeMatches` (executor.go:555-573) binds a vertex whose key type ∉ the label set when its body `class`
> equals the label, and the narrowing gates decide on that set. location-domain gives
> `vtx.unit`/`building`/`property` the class `location`; nothing labels `:location` yet. Consumer: the Inc 1
> gate, which narrows 4 auth lenses.

It arrives from `auth-plane-projection-latency-design.md` §13.4, which specified this as *Increment 0b* — a
`packages/**` lint default-denying a vertex body whose bare-token `class` differs from its key type — then
**falsified its own premise at build time** (the tree is not zero-debt: `location-domain` writes
`class: "location"` on three key types deliberately, and the class is written from a Starlark *variable* that
a line-based lint cannot evaluate). §13.4 re-scoped the mechanism back to the Designer with a proposed new
enforcement point: *"the unsound step is a lens pattern label that names a class-only token … statically
decidable from the cypher plus the installed type vocabulary."*

**This design rejects that framing too, for a better reason than the first one failed.** Neither the class
write nor the label token needs a *gate*, because the divergence they create is not a policy question — it
is one binder disagreeing with five consumers. Close the divergence in the binder and there is nothing left
to enforce.

**Why it is ★★★ now rather than latent.** §14.8 records the exposure honestly: Increment 1 shipped
(`7dafd097` is 0a; Inc 1 landed the same day) and **arms the client-side relevance gate for 17 lenses,
4 of them on the auth plane** (`capabilityRoles`, kernel `capability`, kernel `capabilityRead`, the
generated `edgeManifestProviderReadGrants`). On a read-model lens an unsound narrowing is a stale row; on
these four it is **a grant that never updates and never retracts** — the over-grant direction. There is no
live victim today (§3), but the invariant that keeps it that way is currently unwritten, unenforced, and one
`MATCH (l:location)` away from being false.

## 2. Grounding ledger (verified `file:line`, this fire)

Every row was opened in this fire; each cites the code that *does* the thing, never a comment describing it.

| Claim | Evidence |
|---|---|
| A label binds by key type, **then** body `class`, **then** body `label` | `ruleengine/full/executor.go:555-573` (`nodeMatches`) |
| `props` is the whole stored document, so `class` is a top-level readable field | `executor.go:800-822` (`readNode`: `json.Unmarshal` into `props`, then `props["key"] = key`) |
| A **labeled seed scan** is key-type-only — prefix `vtx.<label>.` **and** `KindVertex` | `executor.go:653-670` |
| **Event seeding** is key-type-only — `ParseVertexKey(seedKey).vtype == n.Label` | `executor.go:766-775` (`seedAnchorBinds`) |
| **Anchor retraction / D2 seeding eligibility** is key-type-only | `anchor_delete.go:247-256` (`AnchorLabel`), `:263-275` (`anchorPattern`) |
| The **narrowing derivation** treats the label set as a set of vertex types | `ruleengine/full/labels.go` (`ReferencedLabels`), consumed by `pipeline.go:437`/`:461` (`plainReprojectLabels`), `:546` (`plainVertexRelevant`), `:586` (`ActorAwareNarrowingLabels`), `:698-718` (`NarrowedFilterEligible`), `:720-730` (`ConsumerFilter`) |
| **Relationship** matching has no body fallback (the generalization probe) | `executor.go:890`, `:958` — `e.Name != rel.Type`, adjacency edge name, exact |
| `nodeMatches` call sites — all four | `executor.go:480` (bound variable), `:687` (seed scan), `:716` (`pointCandidate`, a `{key: …}` point read), `:920` (traversal target) |
| A **required** labeled MATCH prunes an already-bound variable in *both* positions | `executor.go:473-494` (head node), `:1017-1036` (`traverseRel`, constrained target: `ex.key != n.key → continue`) |
| OPTIONAL MATCH **null-binds** its new variables, so a later unlabeled re-reference on a null binding drops the row | `executor.go:311-373` (`applyMatch`), `:412-433` (`nullBindNewVars`), `:448-450`, `:474-479` (null binding cannot extend) |
| Pattern-expression / comprehension bindings **never escape** into the row | `executor.go:1643-1649` (`existsAsPredicate`), `:1616-1639` (`evalPatternComprehension`) — both discard what `matchPath` returns |
| A legacy adjacency edge with **no `OtherType`** yields a bare NodeID as the neighbour key | `executor.go:964-971`; `ParseVertexKey` requires 3 segments + a valid NanoID (`substrate/keys/keys.go:127-136`); the shape is still branched on at `pipeline/actor_enumerator.go:173-175` |
| `Match.Optional` exists on the AST, so "required vs optional" is decidable in the derivation | `ast.go:50-56` |
| Contract #1 puts coarse routing in the **type segment** and fine-grained classification in **`class`** | `docs/contracts/01-addressing-and-envelope.md:15`, `:77` |
| The class-only-token corpus is exactly `location`, written from a Starlark **variable** | `packages/location-domain/ddls.go:181-187` (`LOCATION_TYPES`/`LOCATION_CLASS`), `:318` (`make_vtx(loc_key, LOCATION_CLASS, {})`) |
| `loftspace-domain`'s `class=location` guard is **op-side Starlark**, not a cypher label | `packages/loftspace-domain/ddls.go:29-31,54,64,86` — DDL prose + `kv.Read`-then-check; no lens involved |
| The designed divergence audit already had to publish an under-coverage caveat **because of this fallback** | `lens-projection-divergence-audit-design.md:303-309` (`auditCoverageBasis: "key-type"`), `:610-616` |

### 3. Census — who actually uses the body fallback

Run this fire, independently of §13.4's (per *"a row's 'no live consumer' is a hypothesis"* — I re-ran it
rather than inheriting it), and **re-run a second time at review over every `pkgmgr.LensSpec` in
`packages/**`, not just `packages/*/lenses.go`** — the first sweep's file glob missed specs that live in
sibling files (`lease-signing/renewal_lenses.go`, `clinic-reminders/{visitseries,followups,pastdue}.go`,
`console-operator/package.go`, `loftspace-domain/ownership.go`, `cafe-domain/targets.go`). The corrected
scope is `packages/**` + `internal/bootstrap/lenses.go` + the lenses `internal/pkgmgr` **generates**:

- **35 distinct node labels** are used in the installed corpus (the 33 the first sweep found, plus `renewal`
  and `visitseries` from the missed files). **Every one is a real `vtx.<type>` key type**
  (`vtx.renewal.<id>` — `lease-signing/renewal_ddls.go:45`; `vtx.visitseries.<id>`). No anonymous labeled
  node (`(:label)`) exists anywhere. Generated lenses are covered: `anchorwalk.go:72`'s required head is
  `(…:identity {key: $actorKey})` and every walk-chain label is a declared vertex type.
- **Bare-token classes that differ from their key type: exactly one — `location`**, on
  `vtx.unit`/`vtx.building`/`vtx.property` (the only key whose *type segment* itself comes from a variable,
  `location-domain/ddls.go:315`). Every other `make_vtx` class and every Go-side `docVertex` class
  (`pkgmgr/build.go:72,78,105,154,196,205,231,274,312,346`) is either equal to its key type segment or
  **dotted** (`service.<fam>.template|instance`, `identity.system.*`, `meta.lens`, `augur.review`, …) and
  therefore cannot collide with a bare label token.
- **`location` is never used as a pattern label** by any lens. It appears as a label only in two AST-only
  tests that never reach the executor (`lens/branchspec_translate_test.go:69`,
  `projection/footprint_classifier_test.go:130,142`).
- **Nothing writes a *root-level* body `label` field** — the third branch of `nodeMatches` is dead in the
  corpus and in every fixture. The one fixture that writes `label`
  (`pipeline/branchmerge_test.go:184-185`) nests it under `data` (`output_collision_test.go:334-340`), so
  `ref.props["label"]` never sees it; the cypher reads it as `role.data.label`.
- **Two fixtures model a key-type/class divergence; neither binds through it.**
  `ruleengine/full/service_actor_class_test.go`'s `putRawVertex` writes the *dotted* `identity.system.*` and
  is matched by the key-type label `:identity`. `ruleengine/full/service_location_lens_test.go:114-115,
  153-154, 198-199, 225, 261, 307-309, 341-342` writes the **bare** `location` class on `vtx.unit`/
  `vtx.building` — the exact divergence Inc 1 removes — but the lens it drives
  (`packages/service-location/lenses.go:133-145`) binds `(loc0)`/`(loc)`/`(exLoc)` **unlabeled**, so nothing
  matches `:location`. Both are named because a census that discloses one of two divergence fixtures while
  asserting a negative is the shape this section exists to avoid.
- **All 15 labeled `{key: …}` point-read patterns bind `$actorKey`, a vertex key** — so §4.1's aspect-key
  concern at `:716` has no live instance either.

So the fallback has **zero live consumers**, on either plane, in product code or fixtures. Per the
*ratified-practice ≠ required-practice* reflex: what exists in the corpus is not what requires the shape,
and here the census is empty on both counts.

**One residual the removal carries, named rather than assumed away.** `executor.go:964-971` supports a
legacy adjacency edge with **no `OtherType`**, whose neighbour key is a bare NodeID rather than a
3-segment `vtx.<type>.<id>`. `ParseVertexKey` refuses it (`substrate/keys/keys.go:127-136`), so such a
neighbour can satisfy a **labeled** traversal target *only* through the class fallback — after Inc 1 it can
never bind one. No fixture exercises it, every `adjacency.EdgeEntry` construction in the tree sets
`OtherType`, and production sets it from the parsed link key (`pipeline/evaluate.go:774-778`) — but
`pipeline/actor_enumerator.go:173-175` still branches on the shape, so it is a residual to state, not a
break. If a legacy-edge corpus is ever found, its neighbours must be repaired to Contract #1 keys; they were
never bindable by any other mechanism in §2's ledger either.

## 4. The shape

### 4.1 Increment 1 — a label is the key type, full stop

`nodeMatches` drops the `class` and `label` body branches. A pattern label matches iff
`substrate.ParseVertexKey(ref.key)` succeeds and yields that type; an empty label still matches anything.

That is the entire behavioral change. It is a **structural** fail-closed rather than a lint: after it, no
author *can* construct the unsound shape, so there is no convention left for a gate to enforce — which is
the ordering §2 of the Designer skill asks for (*"prefer a structural fail-closed over a lint that only
catches it later"*). What ships alongside is a **pinned regression test** naming why the fallback must not
come back, so a future agent restoring "helpfully" fails a test rather than silently re-opening the hole.

Three call sites are affected in the direction that matters:

| Call site | Today | After |
|---|---|---|
| `:687` seed scan | unreachable for a class-only label — the scan already lists `vtx.<label>.` and requires `KindVertex` | unchanged |
| `:480` bound-variable re-check, `:920` traversal target | admits a neighbour whose key type ≠ label | binds by key type only |
| `:716` `pointCandidate` (a `{key: …}` point read) | admits any fetched document whose body class matches, including a 4-segment aspect key | binds only a well-formed `vtx.<label>.<id>` |

The `:716` narrowing is worth naming because it is the one that could in principle bind a **non-vertex**
document: a lens writing `MATCH (a:canonicalName {key: <aspectKey>})` binds today via the aspect body's
`class`. No lens does (§3 — every label is a vertex type), and an aspect is read through
`resolveProperty`'s point-read (`node.<aspect>.data.<field>`), never bound as a node pattern.

### 4.2 The asymmetry this removes (the argument that decides it)

Today a class-only label behaves **differently in seed position than in traversal position**:

```
MATCH (l:location)                        → seeds `vtx.location.` → ZERO keys → the lens yields nothing
MATCH (u:unit)-[:containedIn]->(l:location) → traverses adjacency, fetches vtx.building.<id>,
                                              nodeMatches accepts it on body class → binds
```

So the fallback does not give the corpus a working "shared discriminator" idiom — it gives it a *half*-working
one whose behavior depends on where in the pattern the label appears. Removing it makes the engine consistent
with itself, and consistent with the four mechanisms in §2's ledger that already read a label as a key type.

**The expressivity is not lost, it moves one character:** `MATCH (l {class: "location"})` works in **both**
positions today (`propsAllMatch:589` reads `ref.props[k]` off the whole unmarshalled document —
`readNode:800-822`; the seed path takes the unlabeled whole-bucket scan at `:657`/`:673` and the traversal
path calls `propsAllMatch` in `admit()` at `:923`). No validator rejects it: `visitor.go:373`'s
`visitPropertiesMap` accepts an arbitrary map, and nothing in `refractor/lens/**` or `pkgmgr/**` constrains
`NodePattern.Properties` (`"key"` is the only special-cased key anywhere, `executor.go:619`, `:770`).

**But the property form is not a drop-in in every position, and the design does not claim it is.** Being
unlabeled costs three things beyond narrowing, all of them real:

| Lost | Where |
|---|---|
| `exhaustive` — hence both the client relevance gate and the narrowed server filter | `labels.go` unlabeled branch; `pipeline.go:698-718`, `:720-730` |
| **Anchor event-seeding and anchor-tombstone retraction** if used in the *anchor* position | `anchor_delete.go:252` returns `ok=false` for an unlabeled anchor |
| A Personal lens's self-anchor check | `scripts/lint-lens-anchors.go:68` — `isSelfAnchored` requires a label |

So the property form is the right migration for a **traversal** node (the only position the fallback ever
worked in) and is *not* an option for an anchor. That is a boundary, not a regression: an anchor bound by
class was already broken in seed position (above) and already invisible to retraction and event seeding
(§2's ledger), so there was never a working class-anchored lens to migrate.

### 4.3 Increment 2 — the derivation's last under-approximation (folded in, not a separate fire)

The board carries a sibling row: *"An OPTIONAL-MATCH-only or negated-`WHERE` label still excuses an earlier
unlabeled sighting"* (★★, S, `📋 ready`), filed by the same adversarial pass (§13.5). It is the **derivation**
face of the same hazard Increment 1 closes on the **binder** face: both make `exhaustive = true` claim more
than the executor delivers, both are load-bearing for exactly the same 17 narrowed lenses, and both need the
same full-suite gate. Per *fewer, larger fires*, they ship as one fire with an internal order rather than
two fires racing on `ruleengine/full`.

**The row's suggested fix over-approximates and should not be built as filed.** It reads *"collect pass-1
labels from required MATCH patterns only"*. That is sound, but §13.5's own probe measured the cost: **9
lenses** label a variable on its *first* sighting inside an OPTIONAL MATCH — a genuine **binding** position,
not a re-reference — and the blunt rule would drop all 9 to a broad filter, undoing narrowing D1 already
ships for them.

**The precise rule, derived from what actually constrains a surviving binding:**

| Label position | Constrains? | Why (code) |
|---|---|---|
| a **required** `MATCH` pattern, anywhere in the segment | **yes**, backward and forward | a required match on an already-bound variable applies `nodeMatches` and *drops* non-matching bindings (`executor.go:474-490`), so it prunes a whole-bucket seed down to that type |
| an **OPTIONAL** `MATCH` pattern | **yes, from that clause onward only** | the whole path binds as a unit or all its new variables bind null (`applyMatch:310-360`); it never prunes an *earlier* binding, and a later unlabeled re-reference on a null binding drops the row (`:474-479`) |
| a `WHERE` / pattern-comprehension expression | **no** | a pattern expression is an existence predicate; its variables do not survive into the row, so a later `MATCH (b)` is a fresh whole-bucket seed |

**The implementation rule, stated completely** — the first draft said only "an order-accumulated set" and
*both* adversarial passes broke it on the `WITH` boundary, in opposite directions. All four clauses below
are load-bearing:

1. `labeledVars` keeps its segment-global forward-looking collection, but **only from `!m.Optional`
   patterns**. `collectVarsExpr` stops contributing to it entirely.
2. A second set, `optionalLabeled`, is **order-accumulated within a segment** as pass 2 walks that segment's
   clauses, adding an OPTIONAL clause's own labels *before* judging that clause's own unlabeled nodes
   (sound — an OPTIONAL path binds as a unit or null-binds every new variable).
3. **`optionalLabeled` resets at each segment start.** Without this it is unsound, and the counter-example
   is not hypothetical in shape:
   ```
   MATCH (a:identity {key: $actorKey})
   OPTIONAL MATCH (a)-[:holdsRole]->(role:role)
   WITH a                                      -- role is DROPPED
   MATCH (role)-[:grantedBy]->(perm:permission)
   ```
   `role` is unbound after the `WITH`, so `MATCH (role)` re-seeds through the whole-bucket scan
   (`executor.go:657`) and binds any type — a segment-global `optionalLabeled` would still excuse it and
   report `exhaustive = true`. This is the same trap Inc 0a closed for `labeledVars`, one set later.
4. **`carryLabeled` must consider `labeledVars ∪ optionalLabeled`,** not `labeledVars` alone
   (`labels.go:68-85` today reads only the latter). Sound by clause 2's own argument: a variable the `WITH`
   carries as a bare ref keeps its `*nodeRef`, so downstream it is either that label's type or null, and a
   null binding cannot extend (`executor.go:474-479`). **Necessary**, because `pkgmgr` compiles *every*
   walk-chain clause as `OPTIONAL MATCH` under a single required head (`anchorwalk.go:453-473`, `:72`), so
   omitting it would drop narrowing for exactly the generated lenses this design's ★★★ argument is about.
   Live instance: `edgeCatalog` labels `role`/`perm`/`op:meta` in OPTIONAL clauses
   (`packages/edge-manifest/lenses.go:436`, `:117-121`), carries them at `:588` (`WITH op, role`), then
   re-references `op` unlabeled in a pattern comprehension at `:614`.

The ordering established by Inc 0a still governs: the closing `WITH`'s **items** are judged in the
pre-`WITH` scope, then the carry lands (and `optionalLabeled` resets), then the `WITH`'s own `WHERE` is
judged in the carried scope.

Pass 2's `addExpr` still **adds** `WHERE`/comprehension labels to the returned `labels` set, which is safe:
`exhaustive` is set solely by `addPattern`'s unlabeled and variable-length branches, independent of what is
in `labels`, and every consumer treats `labels` as an inclusion set (`pipeline.go:519-528`, `:546-555`,
`:641-653`, `:720-730`) — widening only ever admits more events.

Dropping `collectVarsExpr` from pass 1 is not merely conservative, it **fixes a live under-approximation**:
a pattern-expression binding provably never escapes into the row (`executor.go:1643-1649`, `:1616-1639`
both discard what `matchPath` returns), so a later `MATCH (b)` really is a fresh whole-bucket seed.

Net corpus effect: the first-sighting-in-OPTIONAL lenses keep their narrowing (including across a `WITH`,
per clause 4); the unsound excuse-an-earlier-sighting shape (0 live instances, per §13.5's swept detector
over all 101 specs) becomes `exhaustive = false`. The four auth-plane lenses are unaffected either way —
`capabilityRoles` (`packages/rbac-domain/lenses.go:80-92`) and the kernel `capability` lens
(`internal/bootstrap/lenses.go:135-148`) have no `WITH`, and the generated producer
(`anchorwalk.go:535-559`) carries only `identity`, labeled in the required head.

## 5. Read path / write path / orchestration

Nothing moves. This is entirely inside the Refractor's full rule engine on the **read** path (P5 — lenses
remain the only application query surface). No operation, no DDL, no lens spec, no key shape, no
orchestration. **P2** is untouched (no Core-KV write changes). **Contract #1** key shapes are what the
change aligns *to*.

## 6. Reconciliation with the existing mental model

- ***"Didn't we already handle this?"*** — Increment 0 of the auth-plane design was supposed to. It shipped
  **0a only** (`7dafd097`, the `WITH`-scoping fix); 0b was falsified and re-scoped here (§14.8 records the
  deviation rather than hiding it). What shipped hardened the *derivation's* `WITH` scoping; the *binder*
  was never touched.
- ***"Does this contradict an established pattern?"*** — the opposite: it removes the one dissenter. Four
  shipped mechanisms plus one designed one already read a label as a key type (§2 ledger). The
  divergence-audit design had to add `auditCoverageBasis: "key-type"` purely to disclaim this fallback
  (`:303-309`); after this fire that caveat describes a boundary that no longer exists. **I am not editing
  that design in this fire** — it is `📐 awaiting-Andrew` and the caveat is *correct as written today*; the
  Steward folds it when that design's own build reaches §4.3, and the pointer is recorded here so it is not
  re-discovered.
- ***"Does this remove a documented capability?"*** — `location-domain/ddls.go:28-33` describes the class as
  *"the shared discriminator a downstream cypher rule guards on"*. That downstream cypher rule was never
  written; the guard that exists is **op-side Starlark** in `loftspace-domain` (`kv.Read` the unit, reject a
  non-`location` class), which this change does not touch. The comment is aspirational, and Increment 1
  truths it up to name the property form.
- ***"Does this introduce new state?"*** — no. Increment 1 deletes code; Increment 2 adds one local set to a
  pure AST function.

## 7. Contract surface

**No change to any frozen contract.** Contract #1 §1 already states the split this design enforces:
*"`<type>` … is a coarse routing/filtering category. Fine-grained classification lives in the document's
`class` field."* (`01-addressing-and-envelope.md:15`.) A pattern label is a routing category; the binder was
reading it as classification. Nothing in `docs/contracts/*` specifies cypher label resolution — the rule
engine's matching semantics are engine-internal and documented in `docs/components/refractor.md`, which
Increment 1 updates.

## 8. Risks + alternatives

### 8.1 Risks

| Risk | Assessment |
|---|---|
| A lens silently loses rows | Requires a lens whose label is a class-only token. **Zero** in the corpus (§3), on two independent censuses. The failure direction is **fewer** rows — under-grant on the security plane, fail-closed. |
| Already-projected rows go stale rather than retract | Only reachable if a lens *did* bind by class. None does, so no row's derivation changes and nothing needs retracting. Named explicitly because *"overwrite-by-reprojection retracts it"* is exactly the assumption that has burned this lane: had a lens been affected, its shrink would be a **row-set shrink**, not a single-row overwrite, and would need the anchor-tombstone/filter-retraction path — not an upsert. The census is what makes the question moot, not the mechanism. |
| An out-of-tree / operator-authored lens uses the fallback | Possible in principle. It would fail loudly at authoring (zero rows) rather than wrongly, and the migration is a one-character edit to the property form. |
| Increment 2 changes a live lens's classification | By construction it can only move a lens from `exhaustive=true` to `false` (broad filter, more work, never less). The 9 OPTIONAL-first-sighting lenses are the ones the precise rule exists to keep; a census test pins each verdict. |
| Collision with `full-engine-independent-branch-decomposition` | **Semantic, not textual — sequence explicitly.** Inc 2's soundness rests on an OPTIONAL clause sharing one threaded binding stream with the clauses around it (`executor.go:311-373`). That design groups a stage's OPTIONAL clauses into **independent branch groups** evaluated separately and folded (`full-engine-independent-branch-decomposition-design.md:106`, `:179-180`, `:272`), which changes *which clauses share a binding stream* — i.e. whether a later unlabeled node is a re-reference at all. **This design should land first** (it is S and closes a live security-plane soundness gap; that one is L and unratified). If decomposition lands first instead, Inc 2's order-accumulation must be re-derived **per branch group** before it is built. |
| Textual conflict with `full-engine-grouping-key-reduction` | Same file, different function (`projectItems`/`normalizeForKey`). No semantic overlap; sequence by whoever lands first. |

### 8.2 Alternatives considered

- **A vocabulary gate on the lens label (§13.4's re-scope proposal) — rejected.** The *general* form needs an
  authoritative set of vertex key types, and there is none: types are minted by Starlark string construction
  (`location-domain/ddls.go:315`, `"vtx." + lt + "." + loc_id`), so a static lint cannot enumerate them, and
  a runtime check ("no vertices of this type exist") is a false-positive machine on a fresh install where a
  type legitimately has zero members. **Being precise about what *is* decidable, since the general argument
  over-reaches:** a *narrow* check — a pattern label equal to a bare-token `class` **literal** written
  anywhere in `packages/**` — is statically decidable, would have caught `location`, and has a ready host in
  `scripts/lint-lens-anchors.go` (which already parses every `packages/**` lens spec). It is still the wrong
  build: it leaves the binder able to bind by class and merely tries to keep lenses away from it, catches
  only the *literal* half of a corpus whose classes are variables, and after Inc 1 has nothing left to
  protect. Recorded so the option is rejected on its merits rather than on an argument that is too broad.
- **A lint on the write side (the original 0b) — already falsified** by the fire that proposed it: the class
  is a Starlark variable, so a literal-pair regex reports the tree clean while missing the one real
  violation in it.
- **Keep the fallback; make `ReferencedLabels` non-exhaustive when any label *might* be class-bound —
  rejected.** Undecidable from the AST, and the conservative version ("any label could be a class") disables
  narrowing everywhere, which is the whole shipped D1/auth-plane investment.
- **Remove only the `label` branch, keep `class` — rejected.** `class` is the branch with the real in-tree
  divergence; `label` is dead everywhere. Keeping `class` keeps the defect.
- **Could a variant of the property form beat the recommendation?** Asked per the alternatives discipline:
  yes, one — a **label disjunction** (`MATCH (l:unit|building|property)`), which expresses the shared
  discriminator *and* keeps the label set exhaustive, because `ReferencedLabels` already accumulates a
  **set**. That is strictly better than both the class fallback and the property form. It is **not built**
  (§8.3).

### 8.3 The reserved extension, deliberately not built

Label disjunction is the sound answer to "match sibling types by a shared discriminator". Building it now
fails the dead-scaffolding test outright: **no consumer exists** — no lens matches on `location`, no filed
row asks for one, and `visitor.go:248` takes a single label token so it is grammar + visitor + derivation
work for an empty demand. Recorded here so that when the need appears it is built as a label set (exhaustive,
narrowable) rather than by restoring the body fallback (unsound). Until then the property form serves, at the
cost of a broad filter.

## 9. Test strategy

- **`nodeMatches` unit (new, pinning):** a vertex `vtx.building.<id>` with body `class: "location"` does
  **not** bind `(l:location)`; the same vertex **does** bind `(l:building)`; a body `label` field binds
  nothing. Written to **fail against the pre-change binder** so it pins the fix, not the shape. Its comment
  states the **invariant** ("a pattern label is the Contract #1 key type") and names the consumers that
  depend on it (§2 ledger) — it must **not** narrate the removed branch, which would be changelog-in-code
  (CLAUDE.md's most-violated rule).
- **Traversal e2e:** the `unit -[:containedIn]-> building` shape with a `:location`-labeled target yields
  zero rows after the change (and yielded rows before) — the one behavior that actually moves.
- **`pointCandidate`:** a `{key: <aspectKey>}` point read with a label binds nothing.
- **Increment 2 derivation units:** the three positions in §4.3's table, each asserted for both `exhaustive`
  and the label set; the earlier-unlabeled-then-OPTIONAL-label shape must flip to `exhaustive = false`
  (confirmed failing against the un-fixed derivation), and the first-sighting-in-OPTIONAL shape must stay
  `true`.
- **Corpus census test:** extend `internal/refractor/auth_plane_narrowing_census_test.go`'s pattern — drive
  the **shipped** cyphers and pin each lens's `(labels, exhaustive)` verdict, so any future cypher edit that
  moves a verdict fails in a test rather than in Capability KV. **It must expand read-grant walks first.**
  The existing helper reads raw `def.Lenses[].Spec` off the `pkgregistry` snapshot
  (`auth_plane_narrowing_census_test.go:126-137`) and `ExpandReadGrantWalks` runs only at
  `pkgmgr/manifest.go:123`, `upgrade.go:138`, `definition.go:31` — so the generated
  `edgeManifestProviderReadGrants` is *absent* from what that helper enumerates today (it even carries a
  `t.Skipf` escape at `:102-104`). Since the walk-composed form is precisely where the OPTIONAL-only labels
  live, a census built on the un-expanded helper would pin the wrong artifact and miss the entire class
  Inc 2 exists for. Must cover `edgeCatalog` and the OPTIONAL-first-sighting lenses by name.
- **Gates:** `go build ./...` · `make vet` · `golangci-lint run ./...` (cache-cleaned) · `make verify-kernel`
  · all six `scripts/lint-*.go` under `STRICT=1` · the **full `go test ./... -p 4`**. The full suite is
  required, not optional: this changes a matcher every lens in the corpus consumes (the
  *wide-blast-radius default* rule).

## 10. Decomposition for the Steward

**One fire, two increments, in order.** Each is independently green; the fire is not split because both
touch `ruleengine/full` and both need the full-suite gate.

1. **Inc 1 — binder.** Delete the `class`/`label` branches in `nodeMatches`; rewrite its doc comment to state
   *"a label is the Contract #1 key type"*. Truth up two comments — the
   `location-domain/ddls.go:28-33` one (name the property form as the guard idiom, and note the real guard
   is op-side Starlark), and the unlabeled-scan comment at **`executor.go:673-679`**. **The `KindUnknown`
   admission itself must STAY**: that arm is reached only when `n.Label == ""`, and `nodeMatches` returns
   true at `:556` for an empty label regardless of body — so the admission never had anything to do with the
   class fallback and is not made impossible by this change. Only its *justification* ("carrying a matching
   body class/label property") is wrong. Deleting the admission would silently narrow every unlabeled
   pattern's candidate set. Add the pinning units + traversal e2e. Update
   `docs/components/refractor.md` §Type-relevance skip.
2. **Inc 2 — derivation.** `labels.go`: restrict `labeledVars` to `!m.Optional` patterns, drop
   `collectVarsExpr` from pass 1, add the segment-local order-accumulated `optionalLabeled` set, and union it
   into `carryLabeled` — **all four clauses of §4.3, none optional**. Add the derivation units + the
   walk-expanding corpus census test.

**Review depth:** capability-plane change ⇒ full 3-layer adversarial before admit, regardless of size.

**Sequencing:** ahead of `full-engine-independent-branch-decomposition` (§8.1) — or that design's branch
grouping forces Inc 2's rule to be re-derived per branch group.

**Board follow-through.** The sibling row *"An OPTIONAL-MATCH-only or negated-WHERE label still excuses an
earlier unlabeled sighting"* is folded here as Inc 2 and its row is repointed at this design **now, not on
ship** — it currently sits `📋 ready` carrying the prescription §4.3 rejects (*"collect pass-1 labels from
required MATCH patterns only"*), and the Steward selects from `📋 ready`, so leaving it would let a fire
build the over-approximating shape while racing this design on `labels.go`. On ship,
`auth-plane-projection-latency-design.md` §14.8's recorded deviation is discharged — say so in the commit,
not in a board cell.

## 11. Pre-build gates

The adversarial pass this design self-flags is **run and recorded in §12** — it is not left open for the
Steward. No other gate is deferred. **This design stages nothing uncommitted** and proposes no contract
edit. (The tree does carry one unrelated uncommitted file — a prior fire's build note on
`auth-plane-projection-latency-design.md` — which this fire does not touch.)

## 12. Adversarial review (run this fire, findings folded)

Two independent read-only reviewers, orthogonal lenses: **soundness** (refute every claim from the matcher
and the derivation) and **blast-radius + house rules**. Neither could break the design's core: both
independently re-ran the census from scratch and confirmed the fallback has **zero live consumers**, and
both confirmed the retraction question is moot *because of* the census rather than because a transport was
assumed. Findings folded:

1. **The Increment 2 rule was under-specified at the `WITH` boundary — both passes broke it, in opposite
   directions.** As drafted ("an order-accumulated set") the natural implementation mirrors `labeledVars`
   and declares it once outside the segment loop, which is **unsound** (a dropped OPTIONAL-labeled variable
   re-seeds whole-bucket while still being excused); the other reading resets per segment but leaves
   `carryLabeled` reading `labeledVars` alone, which **regresses narrowing** for every walk-generated lens,
   because `pkgmgr` compiles walk chains entirely as `OPTIONAL MATCH`. → §4.3 now specifies all four clauses,
   with `edgeCatalog` as the named live instance and the counter-example spelled out. This was the finding
   that reshaped the increment.
2. **The census file glob was too narrow** — `packages/*/lenses.go` misses `LensSpec`s in sibling files;
   two more labels exist (`renewal`, `visitseries`). Both are key types, so the conclusion survives, but the
   claim to be an exhaustive independent sweep did not. → §3 restated over every `pkgmgr.LensSpec` in
   `packages/**` plus the generated lenses.
3. **§3 disclosed one of two divergence fixtures.** `service_location_lens_test.go` writes the bare
   `location` class on `vtx.unit`/`vtx.building` — the exact divergence — and was omitted while a negative
   was being asserted. It binds unlabeled, so the conclusion holds. → both fixtures now named.
4. **The decomposition instruction rested on a false statement.** The draft called the `KindUnknown`
   admission "impossible after this change"; it is in the *unlabeled* arm and must stay. A builder following
   the draft could have deleted live behavior. → §10 step 1 rewritten.
5. **A grounding-ledger row was stale** (`NarrowedFilterEligible` cited at `pipeline.go:543-567`; live at
   `:698-718`) despite the ledger's own "every row opened this fire" warrant. → §2 corrected and widened to
   name all five consumers of the label set.
6. **§4.2's "strictly better on every axis" was an internal contradiction** — the property form also
   forfeits anchor event-seeding, anchor-tombstone retraction, and `lint-lens-anchors.go`'s self-anchor
   check. → replaced with the explicit cost table and the honest scope ("the right migration for a traversal
   node, not an option for an anchor").
7. **The mandated census test could not see the artifact it was meant to pin** — the `pkgregistry` snapshot
   holds un-expanded specs, so the generated producer named in the ★★★ argument is absent from it. → §9 now
   requires walk expansion first.
8. **§8.2's rejection of a vocabulary gate over-generalized** — the narrow "label equals a bare-token class
   literal" check *is* decidable and has a host. Rejected on its merits instead. Both reviewers independently
   agreed no gate is required here: the structural fail-closed satisfies the lint doctrine.
9. **Board handling** — the sibling row must be repointed *now*, not on ship, or the Steward can select it
   from `📋 ready` and build the shape §4.3 rejects. → §10 corrected; done in this fire's commit.

Also recorded (verified, no change needed): required-MATCH pruning holds in **both** positions
(`executor.go:473-494` and `traverseRel:1017-1036` — the second closes a gap rather than opening one);
pattern-expression bindings provably never escape, so dropping `collectVarsExpr` from pass 1 fixes a live
under-approximation rather than merely being conservative; widening the `labels` set can never make
`exhaustive` wrongly true; and the four auth-plane lenses' verdicts are unmoved by Inc 2.
