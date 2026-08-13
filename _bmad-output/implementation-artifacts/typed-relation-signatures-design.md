# Typed relation signatures — a relation declares its endpoint types, and the narrowing derivation can read them

**Status: 🗄️ HELD at ratification — Andrew, 2026-08-13.** The demand does not justify the platform
surface. The design's own census corrections shrank the payoff to **two** lenses — and both convert by a
**single-hop rewrite with zero platform machinery**, because every live containment wiring is
unit→building at depth 1 (`scripts/seed-showcase.go:316`, `scripts/seed-edge-demo.go:97`; the taxonomy
declares no intermediate level), making `containedIn*1..` semantically identical to `containedIn` on every
deployment. The `*0..` majority converts under neither shape (§9.5 needed the rejected §10.3 rule or
explicit labels regardless). The integrity half is a theoretical hole with one careful sole writer and no
incident. **The miss this hold teaches:** §10 priced five alternatives and never the demand-side one —
*rewrite the N consumers* — and with a census of 2, per-consumer wins.

**Replacement row filed:** verticals lane, LoftSpace — rewrite the two `*1..` lenses to single-hop (both
narrow under existing machinery); then assess the `*0..` `coveringLocations` family per lens.
**Contract edit reverted** (the staged §1.7 paragraph was withdrawn from the tree at hold).

**Revive triggers, any of:** (a) an intermediate containment level (`floor`/`room`) is declared in the
taxonomy — the moment fixed-depth rewrites silently under-deliver ancestor anchors (fail-closed direction,
but wrong); (b) a conversion census shows a varlength population per-lens rewrites cannot reach; (c) a
second relation needs endpoint typing as an *integrity* guarantee (a real second consumer of the gate).
The grounding below (censuses, mechanism pins, §3.3's state table, §5's verification discipline) remains
valid and is the starting point for the revive.

Designer fire, 2026-08-11 · Lattice lane
Board row: *Typed relation signatures — `containedIn: location→location`* (was ★★, L, Read-model / projection maturity)
Origin: [dynamic-type-taxonomy-design.md](dynamic-type-taxonomy-design.md) §14 Fire C item 12 (C5.12), extracted and filed 2026-08-10 · absorbs C4.8

---

## For Andrew

**What it does, in two lines.** A link-type DDL gains a declared **endpoint signature** —
`containedIn: location → location` — enforced fail-closed at commit by the Processor, so the relation's
endpoint types stop being a convention one package's Starlark happens to guard. The narrowing derivation
then reads the signature: a variable-length hop over a signed relation contributes its endpoint type's
taxonomy expansion to the lens's referenced-label set **instead of clearing exhaustiveness**, which is the
conjunct that today keeps every variable-length hop in the corpus on the broad Core-KV filter.

**Two numbers the item carried are wrong, and this fire corrects both.** (1) The "**25** variable-length hops"
in the row and in C5.12 counts grep *lines*; **14 are executable** and eleven are one doc-comment sentence
copy-pasted across nine files (§1). (2) C5.12's claim that walk-**generated** cypher carries no variable-length
hop is false — `anchorwalk.go:837` parses a `*range` and `edge-manifest` uses it, so the generated corpus is
part of the population and a source grep cannot see it (§1).

**The payoff, measured rather than asserted — and it is NOT the lens C5.12 named.** §9 walks it. Two lenses
convert (`landlordUnitsRead`, `applicantRosterRead` — both `Protected` Postgres RLS lenses projecting
`authz_anchors`, both blocked by nothing but the varlength hop, landing at 4 and 5 of the 8-label cap); a
third (`landlordLeaseApplicationsRead`) is a Phase-0 question, not a promise. All three of the corpus's `*1..`
hops are in auth-plane grant lenses, which is where projection latency matters most.
**`capabilityServiceAccess` does not convert, and must not be made to**: its `exLoc` node is deliberately
unlabeled as a *ratified security property* (`packages/service-location/lenses.go:135-147`, pinned by
`TestServiceLocationLens_PartialExpansionStillExcludes`), and an unlabeled non-re-reference node clears
exhaustiveness independently of any relation (`labels.go:119-127`). The board row names it as a consumer;
**§9.4 corrects the row.** This is the overclaim the taxonomy design's own §9.4 made once — caught here before
the shape hardened rather than at build time.

**One decision worth your eye, in §4.3.** A signature-derived label is priced at its *current closure* and can
never refuse an install; only an author-written `(l:location*)` can. Charging a lens author the worst-case
`LeafBudget` for a label they did not write would have refused `applicantRosterRead` — a lens that installs
fine today — for a narrowing it never asked for. The cost is that a converted lens may go silently broad when
a sixth location leaf is declared, which is the shipped degrade, not a new failure mode.

**No architectural fork.** §10 records the one real alternative (deriving the signature from live data instead
of declaring it) and why it is rejected — it is the "guarantee that holds by accident of corpus shape" failure
in its purest form.

**Frozen-contract change — one, staged UNCOMMITTED in `main`.**
`docs/contracts/01-addressing-and-envelope.md` §1.7 gains a **"Typed relation signatures"** paragraph, exactly
mirroring the shape the taxonomy design's **"Abstract vertex types"** paragraph already took in the same
section. It declares the `.endpoints` aspect on a `meta.ddl.linkType` meta-vertex, the commit-time gate, and
the tombstone exemption. Affected consumers: the Processor's step-6 validator (the new gate), `internal/pkgmgr`
(declaration + install-time corpus verification), and Refractor's narrowing derivation. §7 has the reasoning.

> **Tree note.** `docs/contracts/02-operation-envelope.md` is *already* dirty in `main` — it belongs to
> [script-live-read-round-trip-collapse-design.md](script-live-read-round-trip-collapse-design.md), a different
> awaiting-Andrew design. This fire touched only `01-addressing-and-envelope.md`. Two uncommitted contract
> edits now sit side by side; they are independent.

---

## 1. Problem

`ReferencedLabels` (`internal/refractor/ruleengine/full/labels.go`) reports the set of vertex-type labels a
compiled lens can bind, plus an `exhaustive` flag. When `exhaustive` is false the set is not authoritative and
the pipeline must treat every type as relevant — which takes the lens's Core-KV consumer to the broad
`$KV.<bucket>.>` filter and makes it evaluate every event in the bucket.

The clear is unconditional for any variable-length hop:

```go
// labels.go:135-138
for _, r := range p.Rels {
    ...
    if r.MinHops != 1 || r.MaxHops != 1 {
        // Variable-length: intermediate hops bind arbitrary types.
        exhaustive = false
    }
}
```

The comment is correct as the platform stands, and the reason is the absence of a declaration, not a property
of graphs. **A relation is a free string today**, gated by nothing but a character-class check:

| Where a relation could have been constrained | What actually happens | Cite |
|---|---|---|
| Key construction | charset only — `[a-z_][a-zA-Z0-9]*` | `internal/substrate/keys/keys.go:225-240`, called from `LinkKey` (`keys.go:60-66`) |
| Step-6 abstract-segment gate | parses the link key, checks **`t1`/`t2` only** — the relation segment is never read | `internal/processor/step6_validate.go:336-364` |
| Step-6 governing-DDL resolution | returns the permissive default for **every** link key (`vertexRootForResolve` yields `""`) | `internal/processor/step6_resolve_ddl.go:222-229` |
| `meta.ddl.linkType` DDL | exists and is used (`indexes`, `duplicateOf`, `boundTo`), but `DDLSpec` carries **no endpoint field at all** — the shape is narrated in `Description` prose and its `Script` is a stub that always `fail()`s | `packages/identity-domain/ddls.go:479-558`, `:584-587`; `internal/pkgmgr/definition.go:760-857` |

So `containedIn`'s endpoint discipline lives in exactly one place: `location-domain`'s `WireContainedIn`
script, which validates "BOTH endpoints are alive AND keyed with an admitted location type segment"
(`packages/location-domain/ddls.go:389`, `:428`) against a **hand-copied list**
(`LOCATION_TYPES = ["unit","building","property"]`) that now exists in **five** packages
(§2.2). Nothing stops any other op, in any package, writing `lnk.identity.<id>.containedIn.foo.<id>`.

**Two consequences, and the second is the one that pays.**

1. **Integrity.** The place graph's shape is enforced by one package's care, not by the platform. The write
   path admits a `containedIn` link between any two types.
2. **Narrowing.** Because nothing *declares* what a `containedIn` traverses, the derivation cannot know, so
   every lens carrying such a hop is broad. Exhaustiveness is a conjunct of **both** narrowing branches —
   the plain gate reads `rs.reprojectAll` (`pipeline.go:1273-1282`, `plainVertexRelevant`) and the actor-aware
   one reads it too (`pipeline.go:1306-1315`, `actorAwareNarrowingLabels`) — so no wiring choice routes
   around it.

**The census (executable, re-run at Phase 0) — and what the grep actually counts:**

```bash
# grep HITS (the figure the board row and C5.12 both carry)
grep -rn -oE "\[:[a-zA-Z]+\*[0-9]*\.\.[0-9]*\]" packages/ internal/bootstrap/ --include="*.go" | grep -v _test | wc -l
# EXECUTABLE hops — the same sweep with comment lines dropped
grep -rn -oE "\[:[a-zA-Z]+\*[0-9]*\.\.[0-9]*\]" packages/ internal/bootstrap/ --include="*.go" | grep -v _test \
  | while IFS=: read -r f l _; do sed -n "${l}p" "$f" | grep -qE '^\s*(//|#)' || echo "$f:$l"; done | wc -l
```

**25 hits; 14 executable.** The other **11 are one copy-pasted doc-comment sentence** repeated verbatim across
`clinic-domain/ddls.go`, `clinic-reminders/visitseries.go`, `cafe-ledger/scripts.go`,
`lease-signing/scripts.go`, `wellness-domain/ddls.go` (×3), `maintenance-domain/ddls.go`,
`cafe-domain/ddls.go`, plus two standalone doc comments — none of those files contains an executable hop at
all. The distinct relation set is `{containedIn}` either way: **one relation holds the entire variable-length
class.**

Executable, per file: `cafe-domain/lenses.go` 3 (`:182`, `:214`, `:393`) · `edge-manifest/lenses.go` 3
(`:278`, `:344`, `:426`) · `wellness-domain/lenses.go` 3 (`:256`, `:318`, `:430`) ·
`loftspace-domain/lenses.go` 2 (`:190`, `:213`) · `service-location/lenses.go` 2 (`:169`, `:171`) ·
`lease-signing/lenses.go` 1 (`:1042`).

> **This correction is the point, not a footnote.** The "25" propagated from C5.12 into the board row and into
> this design's first draft as a count of *hops*; it is a count of *lines mentioning hops*, and eleven of them
> are one sentence about the two lenses that really have them. A count that sizes work must name its unit and
> be re-derived a second way before anything rests on it. Nothing downstream changes — the relation is still
> `containedIn` and still exactly one — but the corpus this design reaches is 14 sites, not 25.

> **Census width — the glob is NOT the population, and here it under-reports.** Lens cypher is also
> **generated**: `internal/pkgmgr/anchorwalk.go`'s `ExpandReadGrantWalks` compiles each `AnchorWalk.Chain`
> string into *two* artifacts — the data lens's own `Spec` (`composeDataLensSpec`, `:457-474`) and an
> `actorAggregate` cap-read grant producer (`generateProducerSpec`/`collectBranch`, `:510-616`). Its
> relation parser **admits a variable-length range** — `// rel parses -[ [var] :type [*range] ]-> in either
> direction` (`anchorwalk.go:837`); the refusal at `:787-802` is on a *node-position* `*` sigil, a different
> guard. `edge-manifest` uses it: `:278` and `:344` carry `[:containedIn*0..]` in `domainStaff` chains, and
> `chainResidence` (`:426`) is referenced by five more `domainBase` walks, so one source declaration becomes
> many compiled copies that no source grep can see. **The conversion census in §12 therefore runs over the
> installed lens registry after walk expansion, never over a source glob** — and the generated corpus, not
> the hand-authored one, is where most of the reach lives.

---

## 2. What exists that this must extend (grounding)

### 2.1 The taxonomy, as shipped

| Piece | Where | What it does |
|---|---|---|
| Declaration | `DDLSpec.Abstract` / `.SubtypeOfRef` / `.LeafBudget` — `internal/pkgmgr/definition.go:796-823` | An abstract type and its parent edge are fields on the ordinary `DDLSpec`; there is no separate taxonomy spec |
| Materialization | `internal/pkgmgr/build.go:132-152` | root `vtx.meta.<id>` with `data.abstract`, a `.canonicalName` aspect, and `lnk.meta.<leaf>.subtypeOf.meta.<parent>` |
| Install-time resolution | `internal/pkgmgr/taxonomy.go:91` (`resolveTaxonomy`), `:428` (external target), `:794` (`walkTaxonomyNoCycle`, `maxTaxonomyDepth = 4`) | batch-local then installed-kernel; fail-closed via `ErrSubtypeOfRefUnresolved` |
| **Install-time corpus check** | `internal/pkgmgr/taxonomy.go:549` (`checkAbstractNoLiveInstances`) | refuses a newly-flipped `Abstract:true` DDL when live instances already exist — **the precedent §5 mirrors** |
| Read-side resolver | `internal/refractor/taxonomy/resolver.go` — `InstallSnapshot` (`:226`), `SetArmed` (`:321`), `Expand` (`:394`), three-tier `Status` (`:89-109`) | downward closure of the `subtypeOf` graph, two independent arming latches |
| Resolver's feed | `internal/refractor/lens/corekv_source.go:721-730` | subscribes `["vtx.meta.", "lnk.meta.*.subtypeOf.>"]` — Refractor **already watches subtypeOf links** |
| Write-side gates | `step6_validate.go:214-222` (abstract class), `:336-364` (abstract key segments) | both tombstone-exempt |
| Budget | `subjects.MaxNarrowedFilterLabels = 8` (`internal/refractor/subjects/subjects.go:186`); `leafBudgetDefault` (`internal/pkgmgr/taxonomy.go:35`); the refusal `K + Σ budget(e) ≤ 8` in `checkLensLabelCap` (`internal/pkgmgr/lenslabelcap.go:264`, `:282-324`), called from `installer.go:345` | an abstract type declaring no `LeafBudget` takes the whole cap |

The whole live taxonomy is **one abstract type**: `location`, `LeafBudget: 5`, leaves
`{unit, building, property}` (`packages/location-domain/ddls.go:111-168`). Its own doc comment already does
the arithmetic this design inherits: *K + 5 ≤ 8 leaves K ≤ 3*.

### 2.2 The hand-copied list this retires (C4.8, absorbed)

```bash
grep -rn "LOCATION_TYPES = " packages/ --include="*.go" | grep -v _test
```

Expected: **5** — `location-domain/ddls.go:283`, `service-location/ddls.go:215`,
`wellness-domain/ddls.go:1380`, `maintenance-domain/ddls.go:250`, `cafe-domain/ddls.go:1496`. All five hold
the identical literal `["unit", "building", "property"]`. The guards test the **key type segment**
(`if parts[1] not in LOCATION_TYPES`, `location-domain/ddls.go:389`; `if lt not in LOCATION_TYPES`,
`:297`/`:428` and the four copies) — the same axis a relation signature enforces, which is why the platform
gate can take the arm over.

A stale copy **refuses** a newly-declared leaf rather than admitting anything, so the debt is maintenance
surface, not a hole. It is nonetheless real: declare a `room` leaf and five packages must be edited before
the place graph accepts it, which is precisely the coupling the taxonomy exists to remove.

> **Boundary — this is NOT the `LEGACY_LOCATION_CLASS` row.** That constant has **9** declaration sites
> across 7 packages and widens guards on the **`class`** axis for pre-flip data; its retirement is its own
> board row (*Retire the `LEGACY_LOCATION_CLASS` widening*, ★★ S). Signatures are key-type-segment based and
> touch the class axis nowhere, so the two are independent and can land in either order.

### 2.3 The Processor's cache, and the one thing it cannot see

`DDLCache` (`internal/processor/ddl_cache.go`) is built by scanning **`vtx.meta.>`** (`commit_path.go:1068`,
`ddl_cache.go:19-20`) and maintained not by a CDC watch but by **self-invalidation at step 8** — the
Processor is the sole writer, so its own commits are the complete mutation stream:

```go
// step8_commit.go:315-335
if !strings.HasPrefix(m.Key, "vtx.meta.") { continue }
... c.DDLs.Invalidate(ctx, root)
```

`MetaVertexRef` therefore carries `Abstract` (read at `ddl_cache.go:586-596`, failing **closed to `true`** on
a non-bool) but **no `subtypeOf` information at all** — the edges are `lnk.meta.*`, outside the `vtx.meta.>`
scan and outside the step-8 prefix filter.

**This corrects C4.8's stated premise.** That disposition said the Starlark name-resolution problem "dissolves
at step 6, where the Processor already holds the taxonomy (`ddl_cache`)". The Processor holds the *abstract
markers*; it does not hold the *graph*. §4.2 is the honest cost that follows.

---

## 3. The shape

### 3.1 Declaration

`DDLSpec` gains one field, meaningful only for `Class == "meta.ddl.linkType"`:

```go
// EndpointSignature declares the vertex types a link relation's two endpoints
// may carry. Source/Target name a type by canonicalName; each may name a
// CONCRETE type or an ABSTRACT one, in which case the type's subtypeOf closure
// is the admitted set. Meaningful only for Class == "meta.ddl.linkType"; the
// zero value declares no signature, which is today's behaviour exactly.
type EndpointSignature struct {
    Source string
    Target string
}

// on DDLSpec:
Endpoints EndpointSignature
```

Materialized by `build.go` as a `.endpoints` aspect on the link-type meta-vertex, beside `.canonicalName` —
the same move `Abstract`/`SubtypeOfRef` make, one aspect per declared fact:

```
vtx.meta.<linkTypeId>.endpoints
  envelope: { class: "endpoints", ... }
  data: { source: "location", target: "location" }
```

Scope validation lands in `internal/pkgmgr/abstractscope.go` beside the existing `Abstract`/`SubtypeOfRef`
scope rules: `Endpoints` set with a non-`linkType` `Class` is an install error; each name must satisfy
`keys.IsValidTypeSegment`, must not be reserved (`keys.IsReservedTypeName`), and must resolve to a live
`meta.ddl.vertexType` meta-vertex — batch-local first, then the installed kernel, reusing
`resolveExternalSubtypeTarget`'s exact resolution discipline (`taxonomy.go:428`) so an unresolvable endpoint
fails the install closed rather than installing a signature nothing can check.

`containedIn` gets its own `meta.ddl.linkType` DDL in `location-domain` (it has none today — the corpus's only
link-type DDLs are identity-domain's three), carrying `Endpoints{Source: "location", Target: "location"}` and
the same declaration-only `fail()` script the shipped link-type DDLs use.

### 3.2 The commit gate

A new step-6 check, mirroring `validateAbstractKeySegments` position-for-position:

```go
// step6_validate.go — called from validateOne beside 2.5/2.6, tombstone-exempt
func (v *ValidatorImpl) validateRelationEndpointSignature(key string, kind substrate.KeyKind, rid string) error
```

- Applies to `KindLink` only. `substrate.ParseLinkKey(key)` yields `(t1, _, rel, t2, _)`.
- `v.DDLs.Lookup(rel)` — the **canonicalName index**, the same lookup the abstract-segment gate uses. The
  reference is honoured **only when `ref.Kind == "linkType"`**; any other kind, or an ambiguous name that
  `indexByCanonicalName` dropped, yields no signature (§8.2 explains why that asymmetry is safe and how the
  read side is held to the identical rule).
- No `.endpoints` declared ⇒ **no gate** — Contract #1 §1.5/§1.6's permissive default, unchanged.
- Declared ⇒ require `t1 ∈ closure(Source)` and `t2 ∈ closure(Target)`, where `closure(X)` is `{X}` for a
  concrete type and `X`'s concrete `subtypeOf` descendants for an abstract one. Failure is a
  `*DDLViolation{ViolatedConstraint: "relationEndpointSignature"}`.
- **Tombstone-exempt**, for the identical reason the two abstract gates are (`step6_validate.go:155-161`):
  removing a link that should never have existed is exactly the corrective action that must stay available
  once a signature is declared, and a tombstone can never create a violating link, so the set of live
  violating links only ever shrinks through that path.

The gate reads the **key**, never the document's `class`. A link's `class` is an arbitrary string its author
sets (`location-domain/ddls.go:53` happens to set `class=containedIn`) and `resolveGoverningDDL` returns the
permissive default for every link key anyway — a class-keyed gate would be evadable by construction, and the
narrowing's soundness rests on this gate.

### 3.3 The narrowing derivation

**The rule, stated once and applied uniformly:** *a signed relation constrains the vertex types that can
appear at each position it touches — its two endpoint node positions and, for a variable-length hop, every
intermediate node it traverses.*

Intermediates are the easier half and the one that matters most. In `traverseRel`
(`executor.go:1112` after the 2026-08-11 branch-group refactor `59441252`, which left `labels.go` and this
mechanism untouched) an intermediate is never matched against a node pattern — `nodeMatches`/`admit` run
against the pattern's `to` node only, and the walk itself follows adjacency edges filtered on
`e.Name != rel.Type` with the other endpoint reconstructed from `e.OtherType`. So an intermediate's type comes
**purely from link keys**, which is exactly what §3.2's gate constrains. Each intermediate is simultaneously
the target of hop *i* and the source of hop *i+1*, hence `closure(Source) ∩ closure(Target)`; for the
reflexive `location → location` signature that is `closure(location)`.

**The state table for the endpoint positions** (§2's one-clause-predicate reflex — this is written before the
predicate, and the predicate is written over it). For a hop `(a)-[:R*N..M]->(b)` with `R` signed `S → T`:

| Shape | `a` gains | `b` gains | Intermediates contribute | Why |
|---|---|---|---|---|
| `R` (no quantifier: `N=M=1`) | `closure(S)` | `closure(T)` | — (none exist) | the hop is traversed, so both ends are real endpoints of an `R` link |
| `R*N..M`, `N ≥ 1` | `closure(S)` | `closure(T)` | `closure(S) ∩ closure(T)` | at least one hop is always traversed |
| `R*0..M` | **nothing** | **nothing** | `closure(S) ∩ closure(T)` | the zero-length walk traverses no link, and then `b` *is* `a`, of whatever type `a` already was |
| `R` untyped (`r.Type == ""`) | nothing | nothing | — | no relation to look up; `exhaustive` clears as today |
| `R` typed but **unsigned** | nothing | nothing | — | `exhaustive` clears as today for a varlength hop; a single hop is unaffected |

`exhaustive` is cleared by the relationship arm **only** in the last two rows — a typed, signed
variable-length hop no longer clears it. The node arm (`labels.go:119-127`) is untouched: an unlabeled,
non-re-reference node still clears exhaustiveness, and a relation-derived label counts as a label at that
position for exactly that test.

The `R*0..M` row is deliberately the conservative one. A conditional rule ("`b` inherits `closure(T)` when
`a` is independently constrained to a subset of it") is *sound* and would convert more lenses, and it is
exactly the kind of clause whose scope this file has already been burned on — the `optionalLabeled`
four-clause rule was learned the hard way in this same function. It is not needed for the payoff (§9) and it
is recorded in §10.3 as a rejected alternative with the consumer that would reopen it.

**Where the rule lives — no new state, no new lifetime.** A relation-derived label is threaded into the
*existing* `labeledVars` / `optionalLabeled` / `carryLabeled` machinery rather than accumulated beside it.
A required `MATCH`'s signed hop contributes into `labeledVars` in pass 1 (alongside `collectVarsInto`); an
`OPTIONAL MATCH`'s contributes into `optionalLabeled` in pass 2, in clause order, so it can excuse only the
unlabeled sightings that follow it; both fold into `carryLabeled` at the `WITH` and `optionalLabeled` empties
there. **The scope discipline is the one already paid for**, which is the whole reason the rule is expressed
as "a signed hop labels its endpoint positions" rather than as a new set: there is no fourth question to get
wrong.

### 3.4 Threading the signatures to a pure-AST derivation

`ReferencedLabels` is a pure function over the AST; the signatures are runtime state. The existing solution to
exactly this problem is copy-on-write, and it is mirrored:

- `func WithRelationSignatures(cr *CompiledRule, sigs map[string]EndpointClosure) *CompiledRule` — a shallow
  copy carrying the resolved map, exactly as `WithLabelExpansion` does (`label_expansion.go:14-21`).
- `func (cr *CompiledRule) SignedRelationsNeeded() map[string]struct{}` — every relation the query traverses
  in a position where a signature would change the derivation, exactly as `ExpansionLabels()` reports the `*`
  labels (`label_expansion.go:32`).
- `LabelFacts` (`spec_labels.go:16-32`) gains a fourth field `SignedRelations`, so the install-time budget
  reader gets the same three-derivations-from-one-parse guarantee `SpecLabels` already provides. **A
  `LabelFacts` consumer that ignores the new field under-prices a lens that will narrow at runtime**, which
  is why §4.3 makes `checkLensLabelCap` a required part of the same fire rather than a follow-on.
- `pipeline.useFullEngineBranches` resolves each needed relation against the signature table + the taxonomy
  resolver and calls `WithRelationSignatures` — beside, and under the same conditions as, the `*`-expansion
  block at `pipeline.go:771-932`. An unresolvable or unsigned relation sets `exhaustive = false` and
  `blockNarrowing(health.FilterBroadReasonRelationUnsigned)`, a new value in the existing vocabulary
  (`internal/refractor/health/reporter.go:49-56`).

**Delivery axis only.** `WithLabelExpansion` changes the *matcher* — `nodeMatches` binds the expanded set —
and that is why its failure modes are so carefully staged (`pipeline.go:784-931`). `WithRelationSignatures`
changes **nothing the executor evaluates**: `traverseRel` walks whatever links exist, before and after. A
wrong signature therefore costs *delivery* (a missed event ⇒ a stale row, which on the auth plane is an
over-grant) and never costs *rows computed*. That is a strictly smaller blast radius than the taxonomy
expansion already carries, and it is why the gate (§3.2) and the corpus verification (§5) are the load-bearing
pieces rather than the derivation.

---

## 4. The three mechanisms the design must add, and their honest costs

### 4.1 A relation-signature table on both sides

Refractor's feed needs **no new subscription**: `.endpoints` is an aspect at `vtx.meta.<id>.endpoints`, already
inside `corekv_source.go:721`'s `"vtx.meta."` prefix. It needs a sibling snapshot type
(`RelationSnapshot{Relation, Source, Target}`) fed the same way `TypeSnapshot` is, and resolved through the
same `taxonomy.Resolver` so the closure and the arming latches are shared rather than duplicated.

### 4.2 The Processor's upward parent index

The gate needs "is `unit` a descendant of `location`", and §2.3 established the Processor cannot answer it.
Two additions, both narrow:

- `MetaVertexRef` gains `SubtypeOfParents []string` (canonicalNames), populated by a `lnk.meta.>` prefix list
  filtered to the `subtypeOf` relation segment, run in `DDLCache.Refresh` beside the existing `vtx.meta.>`
  scan. **Upward**, not downward: the gate asks "does this concrete type reach that ancestor", bounded by the
  same `maxTaxonomyDepth = 4` pkgmgr already enforces (`taxonomy.go:26`), so it is a ≤4-step walk per link
  mutation and no closure is materialized. Refractor keeps its downward resolver, where expansion is what is
  needed. Two directions of one graph, each built where it is used.
- `step8_commit.go:323`'s prefix filter widens from `vtx.meta.` to also admit `lnk.meta.`, and
  `metaMutationsPresent` (`step6_validate.go:525-530`) with it.

**State-lifetime table for the new index** (§2's obligation — a data structure without a rule is two
implementations):

| Boundary | Rule | Why |
|---|---|---|
| Created | in `Refresh`, from the `lnk.meta.>` list, in the same critical section that publishes `byName`/`byRoot` | one publication writes both, so the parent map and the name index cannot disagree about the same type |
| Read failure | **refuses the whole refresh**, identical to the `vtx.meta.>` arm (`ddl_cache.go:380-419`) | a partial parent graph fails the gate **open** (a missing edge reads as "not a descendant" ⇒ refuse) — which is fail-closed for *writes* but would refuse legitimate ones; the existing arm's reasoning ("a cache built from a partial scan must not become the quiet state") applies unchanged |
| Invalidated | on any committed `lnk.meta.*` mutation, via the widened step-8 hook | the Processor is the sole writer (P2), so its own commits are the complete stream — the same completeness the `vtx.meta.` hook already relies on |
| Tombstoned edge | a tombstoned `subtypeOf` link drops the parent, so a previously-admitted endpoint type stops being admitted; **already-written links are untouched** (the gate runs at mutation time only) | §5's verification is what covers the resulting live corpus, and §7 records the residual |
| Replay / restart | rebuilt by `Refresh` at construction; nothing survives a process | matches the existing cache exactly |
| Multi-instance | a second Processor's commits do **not** invalidate this one's index | **inherited, not introduced** — the `vtx.meta.` hook has the identical property. Single-instance today; the HA-NATS row (🚧 shelved) owns it |

### 4.3 The budget arithmetic

`checkLensLabelCap` (`lenslabelcap.go:264`) prices `K + Σ budget(e) ≤ 8`, where `K` is the concrete referenced
labels and `budget(e)` is each `*` expansion label's `LeafBudget` (abstract) or current closure (concrete),
and **refuses the install** when the total exceeds the cap.

**A signature-derived label is priced differently, and this is a real design decision the census forced.**
An author-written `(l:location*)` is a promise the *lens author* made, so charging them `LeafBudget` — the
type's declared headroom — and refusing the install is right: they opted in and can see the arithmetic. A
signature-derived label arrives from a **relation's** declaration, in another package, for a narrowing the
lens author never asked for. Charging them worst-case headroom would **refuse a lens that installs fine
today**, which is a strictly worse outcome than the broad filter the design exists to remove. So:

- signature-derived labels are counted at their **current resolved closure**, deduplicated into the union
  with `K` and with any `*` expansion set (a lens naming `(l:location*)` *and* walking a signed `containedIn`
  is charged for `location` once);
- exceeding the cap through signature-derived labels alone **never refuses the install** — the lens installs
  and simply does not narrow, via the runtime's already-shipped degrade at `pipeline.go:1783-1794`, with
  `FilterBroadReasonLabelCap` on its health card;
- only author-written `*` labels can still refuse.

The consequence is honest and worth stating: a lens that narrows today can go broad when a sixth location
leaf is declared, silently and correctly. That is the same latitude the shipped `LeafBudget` mechanism
already grants, pointed at the party that did not author the label.

The install-time reader must nonetheless learn the rule in the **same fire** as the derivation: one that did
not know about signatures would compute a different union than the runtime, and the two figures on the health
card and the install log would disagree about the same lens. §12's cap test pins the arithmetic on both sides.

---

## 5. The corpus verification — the part that is not optional

A signature is a claim about **every live link of that relation**. The gate covers mutations; it says nothing
about links written before the signature was declared. Refractor's narrowing consumes the claim, so a
pre-existing violator makes the narrowing unsound with no error anywhere.

**This is not hypothetical — it is the failure the taxonomy item just shipped and had to repair out of band.**
`checkAbstractNoLiveInstances` (`taxonomy.go:549`) gates a *newly-flipped* abstract type on having no live
instances, and the invariant was still unmet in production: 25 live location roots carried `class: "location"`
(taxonomy design §17.22 item 1), repaired by a direct KV rewrite on 2026-08-10. The check and the gate were
keyed on different predicates, so the check attested vacuously.

So, in the **same fire as the declaration**:

- `checkRelationSignatureNoViolatingLinks`, modelled on `checkAbstractNoLiveInstances`, runs when a package
  install/upgrade **declares or changes** an `Endpoints` signature. It enumerates live links of that relation
  and refuses the install if any endpoint pair fails the signature, naming the violators.
- **It evaluates the identical predicate the step-6 gate evaluates**, against the same closure, from a shared
  helper — one function, two call sites. That is the whole lesson of the C1.4 repair: a verification that
  re-derives the rule in its own words is a verification of a different rule.
- **Enumeration shape.** This is an *install-time* read by `pkgmgr` — a platform binary, so P5's inspector
  exception applies and it is not a write-path scan. It is bounded by the corpus of that one relation, and the
  existing installer census machinery (`scan`, `installer.go:221`, `:309`) is the pattern to extend. It is a
  read-only enumeration with no verdict to act on beyond refusing an install, which is the safest shape a
  sweep can have (§10.1).
- **Timing split** (§2's authoring-vs-apply reflex). The *declaration* is unconditional versioned policy; the
  *enumeration* runs at apply time against whichever environment is upgrading. Never key the refusal on the
  authoring environment's link count, or a link-free dev apply goes green and the first violator surfaces in
  prod.

For `containedIn` specifically, the expected result is **zero violators**: `location-domain` is the sole
writer (`WireContainedIn` / `UnwireContainedIn`), and its script already validates both endpoints' key type
segments against `LOCATION_TYPES`. That expectation is exactly what makes it a *verification* rather than a
migration — and it must be run, not assumed, because "the only writer is careful" is a claim about every op
that has ever run against every deployment.

---

## 6. Reconciliation with the existing mental model

**"Didn't we already handle this with the taxonomy?"** The taxonomy declares **vertex types** and their
subtype graph. It says nothing about **relations**: `ReferencedLabels`' varlength clear is about the types of
nodes reached *through a relation*, and no vertex-type declaration can answer that. The taxonomy design's
own C5 disposition says so — the narrowing justification was **struck** from that item and re-homed here.

**"Doesn't `ReferencedRelations` already do this?"** It answers the *other half* of the link key — which
relations a query walks — and explicitly does **not** lose exhaustiveness on a varlength hop, because
`traverseRel` re-applies `rel.Type` at every hop (`relations.go:15-22`). It has no concept of endpoint types.
The two derivations stay in lockstep over the same clause and expression shapes (`relations.go:30-36`), and
this design adds a position to that lockstep, not a third parallel walk.

**"Doesn't `HopIndex` need this too?"** `hopindex.go:455-460` rejects any pattern with a variable-length
relationship, for a different reason: the affected-anchor index must be *steppable backward*, and a varlength
walk cannot be. A signature does not make it steppable. That is the board row *"[Refractor] A plain lens's
neighbour event recomputes its whole row set"* (★★ L, needs designer pass), whose named pieces are a scan-root
terminus on `HopIndex` and a standing healer. **The two touch adjacent code and do not collide**: this design
changes which events are *delivered*; that one changes which anchors are *re-executed* once an event arrives.
Neither's fix substitutes for the other's.

**"Does this introduce new state we already keep somewhere?"** The subtype graph already exists in Core KV and
is already read by Refractor (`corekv_source.go:721`). §4.2 adds a **second reader** of the same links, in the
opposite direction, in the component that has to enforce the rule — not a second copy of the truth.

**"Is the direction convention affected?"** No. A signature declares which *types* may sit at each end;
Contract #1 §1.1's later-arriving-is-source rule decides which end is which, and the signature is written in
that same order (`containedIn: location → location` reads "child containedIn parent"). For an asymmetric
relation like `residesIn: identity → location`, the signature and the sentence test agree by construction.

---

## 7. Contract surface

**One change, to `docs/contracts/01-addressing-and-envelope.md` §1.7 — staged UNCOMMITTED in `main`.**

§1.7 currently says link-type DDLs "follow the same thin-meta-vertex shape … with `canonicalName` / `schema` /
`description` aspects; aspect-/link-type DDLs may carry `permittedCommands`." The added paragraph declares the
`.endpoints` aspect, the commit-time gate, and the tombstone exemption — and it takes the same form the
**"Abstract vertex types"** paragraph in the same section already took when the taxonomy design added it,
including the identical reasoning for the exemption.

It is an **addition to §1.5/§1.6's permissive default, not a contradiction** — the same relationship the
abstract-class gate documents at `step6_validate.go:203-207`. §1.5 covers "no DDL found"; here one is found and
carries a declared constraint.

**Nothing else is touched.** §1.1's link key shape is unchanged, and the substrate stays direction-agnostic
(`keys.LinkKey` still constructs in caller order and validates nothing) — the signature is enforced at commit,
where every write already passes (P2), not at key construction, where a helper could be routed around.

Affected consumers of the edit: `internal/processor` (the gate), `internal/pkgmgr` (declaration, scope
validation, corpus verification, budget), `internal/refractor` (the derivation). No package or vertical app is
required to change; an unsigned relation behaves exactly as it does today.

---

## 8. Risks

### 8.1 A wrong signature silently under-delivers

The one error mode with no recovery: a lens narrows on a signature the live corpus violates, never learns a
vertex changed, and on the auth plane keeps a grant that should have retracted. Mitigations are stacked and
each is independently sufficient for a *different* cause: the commit gate (future writes), the corpus
verification (past writes), the fail-closed default (an unsigned or unresolvable relation degrades to broad),
and the delivery-axis-only property (§3.4 — the matcher is untouched, so a mistake costs freshness rather than
correctness of the rows that *are* computed).

### 8.2 The two sides must derive the signature by the same rule

The Processor honours a signature only when the canonicalName resolves to a `linkType` meta-vertex; Refractor
must apply the **identical** predicate. A read side that honoured a signature the write side ignored would
narrow against an unenforced claim. The rule is therefore expressed once (relation name → `Kind == "linkType"`
→ `.endpoints`) and asserted from **both** sides by the same fixture in §12. Ambiguity — a canonicalName two
roots claim, which `indexByCanonicalName` drops — must read as *no signature* on both sides; on the write side
that means no gate (permissive, today's behaviour) and on the read side no narrowing (broad, today's
behaviour). Both stay safe, and they stay *the same*.

### 8.3 The cap has headroom, but not much

Under §4.3's closure pricing the two converting lenses land at **4** and **5** of the 8-label cap, because
`unit` and `building` appear in both the lens's own labels and `location`'s closure and the union dedupes
them. Two more location leaves (`room`, `floor` — exactly the growth `LeafBudget: 5` models) take them to 6
and 7; a sixth leaf takes the second one over. The degrade is silent and correct (§4.3), but it is worth
saying plainly rather than discovering at build: **this design converts lenses about places, and places are
what the abstract type grows.**

### 8.4 A tombstoned `subtypeOf` edge narrows the admitted set under live links

Retire a leaf and links already written with that endpoint type become violators the gate would now refuse —
but they are live and nothing sweeps them. The gate's tombstone exemption keeps the corrective path open, and
§5's verification refuses the *install* that would create the divergence. The residual — an operator
tombstoning a `subtypeOf` link directly rather than through a package upgrade — is recorded in §13 with its
consumer, not designed around here.

---

## 9. The payoff, traced conjunct by conjunct

The design's headline is a *value* claim, and §2's payoff reflex says to walk each named consumer through
**every** conjunct of the gate that decides the outcome, not only the conjunct being removed. The conjuncts
for "this lens narrows" are: full engine · exhaustive label set · label count ≤ 8 · (actor-aware only) the
five conjuncts of `actorAwareNarrowingLabels` (`pipeline.go:1306-1360`).

**The corpus splits cleanly on the quantifier**, and that split is the whole ledger. Of the 14 executable
hops, **3 are `*1..`** and 11 are `*0..` / `*0..7`. §3.3's conservative zero-length row gives a `*0..` hop's
endpoint positions nothing, so only the `*1..` sites can convert on the ratified scope — and all three of
them sit in `Protected: true` lenses projecting `authz_anchors`. That is not a coincidence worth glossing:
**an unbounded-upward containment walk is what a place-scoped read grant is made of.**

### 9.1 Converts: `landlordUnitsReadSpec` (`packages/loftspace-domain/lenses.go:202-213`)

```
MATCH (u:unit)<-[:manages]-(landlord:identity)
RETURN … [nanoIdFromKey(landlord.key)] + [(u)-[:containedIn*1..]->(b:building) | nanoIdFromKey(b.key)] AS authz_anchors
```

| Conjunct | Today | After |
|---|---|---|
| every node labelled or a re-reference | ✅ `u:unit`, `landlord:identity`, `b:building` | ✅ |
| expression shapes modelled | ✅ `FunctionCall`, `BinaryOp`, `PatternComprehension`, `PropertyAccess` all have arms (`labels.go:141-204`) | ✅ |
| no varlength clear | ❌ **`containedIn*1..`** — the sole blocker | ✅ signed, `N ≥ 1` row of §3.3's table |
| label count ≤ 8 | n/a (broad) | ✅ `{unit, identity, building} ∪ closure(location)` = `{unit, identity, building, property}` = **4** |

**Converts.** A `Protected: true` Postgres RLS lens projecting `authz_anchors` — an auth-plane read-grant
surface, which is where projection latency matters most.

### 9.2 Converts: `applicantRosterReadSpec` (`packages/loftspace-domain/lenses.go:182-192`)

`MATCH (i:identity) WHERE i.name.data.ct <> null`, with two pattern comprehensions, the second carrying
`(ub:unit)-[:containedIn*1..]->(b:building)`. Every node is labelled — `i:identity`, `la`/`lb:leaseapp`,
`ua`/`ub:unit`, `landlord:identity`, `b:building` — and the varlength hop is again the sole blocker. Union:
`{identity, leaseapp, unit, building} ∪ {unit, building, property}` = **5** ≤ 8. **Converts** — and only
because §4.3 prices signature-derived labels at their closure; the `LeafBudget` alternative would have put it
at 9 and refused the install of a lens that works today.

### 9.3 Candidate, Phase-0 conjunct walk owed: `landlordLeaseApplicationsRead` (`packages/lease-signing/lenses.go:1042`)

The same `[(u)-[:containedIn*1..]->(b:building) | …] AS authz_anchors` shape, but its spec is assembled from
`readinessOptionalMatch` + `readinessWithItems` and spans multiple `WITH` segments — so whether `u` is
label-constrained in the segment that reaches the hop is a question for the compiled AST, not for a reading of
the source string. **Not asserted here.** Fire 2's Phase 0 walks it and the conversion census records the
answer either way.

### 9.4 Does **not** convert: `capabilityServiceAccess` — and the board row is corrected

`packages/service-location/lenses.go:169-183`. Three independent blockers, and removing the varlength one
leaves two:

1. **`exLoc` is unlabeled — deliberately, as a ratified security property.** It sits inside
   `NOT (loc0)-[:containedIn*0..]->(exLoc)<-[:unavailableAt]-(svc)`; the lens's own comment (`:135-147`)
   explains that a label on a *negated* pattern can only remove exclusions, which **grants** access, and that
   under a partially-armed expansion a labelled `exLoc` would hand out a service at a place explicitly marked
   unavailable. `TestServiceLocationLens_PartialExpansionStillExcludes` pins it. An unlabeled,
   non-re-reference node clears exhaustiveness at `labels.go:119-127`, and **this design must not "fix" that
   by labelling it.**
2. The unlabeled `op` inside the `allowedOperations` pattern comprehension — the second blocker the C5.12
   disposition already named.
3. Both containment walks are `*0..`, so §3.3's conservative zero-length row gives their endpoint positions
   nothing even once the relation is signed.

**Row correction (part of this fire, per §2's "when the census comes back different, correct the row"):** the
board row's consumer list reads *"`capabilityServiceAccess` + the varlength corpus"*. The corrected row names
the two `authz_anchors` grant lenses as the converting consumers and drops `capabilityServiceAccess`, whose
own C5.12 note was already careful to claim only "removes the structural blocker" — the row was the looser
restatement.

### 9.5 The `*0..` remainder, and what it would take

Eleven executable hops are `*0..` or `*0..7`, in two families:

- **`coveringLocations` / `authz_anchors`** — cafe-domain `:182`/`:214`/`:393`, wellness-domain
  `:256`/`:318`/`:430` — shaped `[(l)-[:appliesToUnit]->(u)-[:containedIn*0..7]->(c) | c.key]`, whose `u` and
  `c` are unlabeled. Signing `appliesToUnit: leaseapp → unit` labels `u` by §3.3's single-hop row; `c` then
  needs either §10.3's rejected conditional rule or an explicit label.
- **The `edge-manifest` walk chains** — `:278`, `:344`, and `chainResidence` (`:426`, referenced by five
  `domainBase` walks) — which the walk expander compiles into both a data lens and a `cap-read.staff` /
  `cap-read.base` producer, so one declaration is many compiled lenses. Same `*0..` blocker, same remedy.

That is Fire 4 — **declaration-only**, mechanical once Fires 1–2 land, and sequenced behind a re-run of §12's
conversion census rather than promised here.

**So the honest ledger.** Two lenses convert on the ratified scope, a third is a Phase-0 question, all of them
`Protected` auth-plane grant lenses. A further ~11 hop sites — including the generated `cap-read` producers,
which a source grep does not even show — become reachable by declaration alone, on a census that will settle
it rather than on this document's assurance. And **the lens the item was originally justified by does not
convert at all, and must not be made to.**

---

## 10. Alternatives considered

### 10.1 Derive the signature from the live corpus instead of declaring it

Scan the live `containedIn` links, observe that every endpoint is a location, and let the derivation use that.
Rejected outright: this is §2's "a guarantee that holds by accident of the corpus's SHAPE" in its purest
form — the property would hold until the first op wrote a different pair, with **no error, no gate, and a
success signal on the operation that lost it**, and the loss lands on an auth-plane grant lens. A declared,
gated invariant is the only shape whose truth does not depend on what happens to be in the bucket.

### 10.2 A per-lens "assume-intermediates are type X" annotation

Considered and rejected in the C5.12 disposition, and the reasoning holds: unverified it is fail-open on the
auth plane, and verifying it needs the same write-path gate — at which point it *is* the signature, with the
declaration moved to the wrong place (per-lens rather than per-relation, so N lenses restate one fact and can
disagree).

### 10.3 A conditional zero-length rule

For `R*0..M` with a reflexive signature, `b`'s type is `closure(T) ∪ types(a)`, so when `a` is independently
constrained to a subset of `closure(T)` the target *is* constrained. Sound, and it would convert §9.5's family
without new declarations. **Rejected for this design**, on §2's one-clause-predicate discipline: it makes the
endpoint rule depend on another position's binding state, in the one function whose scope rules have already
produced a two-directional review break. Reopens when §12's conversion census shows a lens whose *only*
remaining blocker is this row — which is a mechanical re-run, not a judgement call.

### 10.4 Enforce at install time only, not at commit

Rejected. The threat is a runtime write of a violating link by any op in any package, and the narrowing's
soundness must hold against every writer. Per the enforcement-point rule — commit-time is for a **security
invariant** that must hold against a careless author regardless of path — this is squarely a commit-time gate.
Install-time verification (§5) is the *complementary* half that covers the population the gate cannot reach,
not a substitute for it.

### 10.5 Do nothing — keep the `LOCATION_TYPES` copies and stay broad

The use-what-we-have option, re-asked per §2's alternatives discipline: could a variant beat the
recommendation? It cannot, on either axis. On integrity, the copies guard exactly one op in one package while
the invariant they encode is relied on by every lens that walks the place graph. On narrowing, no arrangement
of hand-copied lists in Starlark can reach `ReferencedLabels`, which is a Go derivation over a compiled AST.

---

## 11. The lint gate (ships in Fire 2, blocking)

A lens author writing a new variable-length hop over an unsigned relation gets a silently broad lens — and
"broad" is the safe direction, which is exactly why nothing will ever tell them. Per the lint doctrine: the
gate that enforces a convention ships in the design that establishes it, and it does not classify — it
**default-denies the bare idiom and makes the author declare**.

`scripts/lint-conventions.go` gains a `packages/**` check: a cypher literal containing `[:<rel>*N..M]` whose
`<rel>` has no `meta.ddl.linkType` DDL with an `Endpoints` declaration — in the same package or in a declared
dependency — is a finding. The declaration is the escape hatch and it is cheap; forgetting it fails closed
(a finding, not a silently broad lens).

It ships **blocking**, not warn-first: Fire 1 signs `containedIn`, which is 25 of 25 hops, so the tree is
clean when the gate lands. A warn-first gate over a clean tree is precisely the fingers-crossed state the
gate exists to end.

---

## 12. Decomposition for the Steward

Each increment is independently shippable and green. Review depth is the Steward's sizing
(`agents/steward/SKILL.md` §4); the posture-changing ones are named.

**Fire 1 — declaration, gate, verification.** *Posture-changing (a new commit-time refusal + a new
install-time refusal) — full depth.*
`DDLSpec.Endpoints` + `EndpointSignature`; `build.go` materialization of the `.endpoints` aspect;
`abstractscope.go` scope + resolution rules; `manifest.go` block (mirroring the `SubtypeOf` field at `:50-52`
— note the taxonomy design's own §14 finding that `RetentionClasses` was omitted from `ManifestBlock` and
minted a board row: this design does **not** repeat it); `MetaVertexRef.SubtypeOfParents` + the `lnk.meta.>`
read in `Refresh` + the widened step-8/`metaMutationsPresent` prefixes; `validateRelationEndpointSignature`;
`checkRelationSignatureNoViolatingLinks` sharing one predicate helper with the gate; `containedIn`'s
`meta.ddl.linkType` DDL in `location-domain`. **No read-path change.**
Owns: the gate's state table tests (create/update/tombstone × concrete/abstract/unsigned/ambiguous endpoint);
the shared-predicate test asserting gate and verifier agree on the same fixtures; the parent-index
lifetime tests (refresh, invalidate on a `lnk.meta.` commit, refresh-refusal on a partial read); the
`.endpoints` round-trip through a real install.

**Fire 2 — the narrowing derivation.** *Posture-changing (auth-plane delivery) — full depth.*
`WithRelationSignatures` / `SignedRelationsNeeded` / `LabelFacts.SignedRelations`; the §3.3 rule inside
`ReferencedLabels`, threaded through the existing `labeledVars`/`optionalLabeled`/`carryLabeled` scopes;
`pipeline.useFullEngineBranches` resolution + `FilterBroadReasonRelationUnsigned`; the relation snapshot on
Refractor's existing meta feed; `checkLensLabelCap` pricing; **the lint gate (§11)**.
Owns: a §3.3 **state-table test** with one case per row, including the two `exhaustive`-clearing rows; the
two-sided predicate-agreement test (§8.2); the **conversion census test** — over the installed lens registry
(not a source glob), asserting exactly which lenses report exhaustive, pinning §9's ledger so a regression or
an unnoticed conversion both fail loudly; the cap test at `K + LeafBudget = 8` and at 9; an e2e proving a
converted lens's consumer carries narrowed filter subjects and still projects the same rows.

**Fire 3 — retire the `LOCATION_TYPES` type-membership arm** (C4.8). *Not posture-changing.*
The five copies (§2.2) shed the type arm and keep their aliveness checks; the platform gate is the authority
for the endpoint pair. Owns: the census test asserting zero `LOCATION_TYPES` declarations remain, and a
negative test proving a non-location endpoint is now refused by the Processor rather than by Starlark.
Sequenced behind Fire 1 — the gate must be live before the script guard is removed, or there is a window with
neither.

**Fire 4 — additional signatures** (deferred, declaration-only). Sign `appliesToUnit`, `availableAt`,
`residesIn`, `locatedAt`, `servedAt`, `worksAt`. **Named consumer:** the `coveringLocations` /
`authz_anchors` family of §9.5, *conditional on* Fire 2's conversion census showing them blocked by nothing
else. It is deferred rather than dropped because its value is mechanical once Fires 1–2 land, and deferred
rather than built because its payoff is a claim §12's own census will settle.

**Sizing the declaration surface — and why no fire migrates it wholesale.** The corpus has **~58 distinct
relation names across ~108 static link-creation sites**, spread over **34 local copies** of a
`make_link(...)` Starlark helper (there is no shared link primitive; `grep -rn "^def make_link(" packages/
--include="*.go" | grep -v _test | wc -l` → 34). Signing all of them is not this design's proposal and would
be a poor trade: an unsigned relation costs nothing (no gate, no narrowing — today's behaviour exactly), so
signatures are added where a consumer wants them, one relation at a time. Fires 1 and 4 together sign **seven**.

**One relation cannot be signed at all, and that is fine.** `packages/objects-base/ddls.go`'s
`AttachObject` family builds its link name from the **op payload** at runtime (`link_ensure_alive`, `:502`,
name via `required_string(p, …)`), so it is not statically enumerable. Under this design that is benign — no
declaration, no gate, no narrowing — and it stays benign as long as no one signs a relation that
`AttachObject` can also mint. If someone does, the gate applies to that op's writes too, which is the correct
outcome and not a special case. Fire 1's scope validation should say so in the `Endpoints` doc comment rather
than leaving a future author to discover it.

---

## 13. Residuals recorded, not designed around

- **An operator tombstoning a `subtypeOf` link directly** narrows a signature's admitted set under live links
  (§8.4). Consumer: the first leaf retirement outside a package upgrade. The gate's tombstone exemption keeps
  the corrective path open meanwhile.
- **Multi-instance Processor cache invalidation** (§4.2) — inherited from the existing `vtx.meta.` hook,
  owned by the HA-NATS row.
- **`HopIndex` still rejects every variable-length pattern** (`hopindex.go:455-460`) — a different mechanism,
  owned by the *plain lens's neighbour event* row (§6).

---

## 14. Pre-build gate

**None deferred.** This design self-flags no pre-build adversarial pass beyond the full-depth review Fires 1
and 2 already carry as posture-changing increments — flagging a gate creates the obligation to discharge it,
and there is no question here that a second pass would answer better than the state-table and census tests
Fire 2 already owns.
