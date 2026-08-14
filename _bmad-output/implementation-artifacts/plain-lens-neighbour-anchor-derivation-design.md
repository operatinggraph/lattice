# A plain lens's neighbour event derives its affected anchors

**Status: ✅ RATIFIED — 2026-08-13.** Presented to Andrew; the §9.1 fork was adjudicated by **Winston**
under the standing escalation doctrine (only frozen-contract / final-architecture forks are Andrew's —
this one is a Refractor licensing mechanism, auth plane excluded under both branches, fail-closed with an
alarm): **Option A — detection is a sufficient licence.** Narrowing is licensed on an enrolled Auditor +
the §5 conjuncts; Increment 5 (the `retained`-class repair) stays un-built behind
`lens-projection-divergence-audit-design.md` §8.1's own trigger (a sustained non-zero `retained` count,
which Inc 4 is what first produces). **Sequencing:** Incs 1+2 (one fire, shadow) buildable now; Inc 3+
behind the divergence design's Fire 2 (the Auditor — ratified, unbuilt, `📋 ready` on the board).
DD folded at ratification: `executor.go` pin drift from `59441252` noted in §15 (mechanisms re-verified
intact); Phase 0 re-pins against merged `main`.
Board row: *[Refractor] A plain lens's neighbour event
recomputes its whole row set* (★★, L) — `_bmad-output/planning-artifacts/backlog/lattice.md`.

**Frozen contracts: no change proposed.** Nothing here needs an edit to `docs/contracts/*`; §7 states why,
per section. (The two uncommitted contract edits in `main` at the time of writing belong to
`script-live-read-round-trip-collapse-design.md` and are untouched by this fire.)

---

## For Andrew

**What it does, in two lines.** A plain (non-actor-anchored) lens today answers *every* event on a
**neighbour** vertex by re-deriving its entire row set — a `vtx.<anchorType>.` prefix listing plus one Core-KV
GET per candidate, per event. This design gives `HopIndex` a **second terminus** (the plain arm's own scan
root, alongside the `$actorKey` anchor it already has), so the shipped affected-anchor walk can answer *which
anchors this neighbour event can move* and the pipeline runs that many seeded evaluations instead of one
whole-corpus rescan. For the named victim, `clinicProviders`, it is **N + 1 → 1**.

**The one fork — RESOLVED at ratification (Winston, 2026-08-13): Option A, detection is a sufficient
licence.** The question as originally framed: is DETECTION a sufficient licence to narrow, where the
actor-aware arm required REPAIR? Narrowing removes an *accidental* healer —
today every neighbour event re-evaluates the lens's whole row set, so a row that drifted gets silently
re-written the next time any neighbour moves. The ratified actor-aware precedent refuses to narrow a lens
with no **standing healer** (`anchor_derivation_mode.go:224-226` — *"A lens with no installed plan must not
lose the accident as well"*), and a plain lens **cannot have that healer**: `Reproject` is envelope-only
(`reproject.go:287-289`). What a plain lens can have is the **Auditor** of the already-ratified
`lens-projection-divergence-audit-design.md` — a standing per-row *verdict*, whose §8.1 deliberately rejects
repair.

- **Recommended, and what §4–§6 specify:** license the narrowing on an **enrolled Auditor** — a standing
  verdict rather than a standing repair — plus the fail-closed conjuncts in §5. A divergence on a
  narrowed lens is then *detected, named and alarmed* (`LensProjectionDiverged`) and remediated by the
  operator's existing `reproject` RPC / `Rebuild`. It is self-selecting: a lens the Auditor refuses is a lens
  that never narrows and keeps today's accident.
- **The alternative you may prefer: require repair first.** That means building the **`retained`-class**
  repair `lens-projection-divergence-audit-design.md` §8.1 *itself* deferred — *"Deferred with a named
  trigger: a plain lens reporting a sustained non-zero `retained` count."* §9.1 recommends **not**
  pre-building it, for one grounded reason: this design's own Increment 4 is what would first produce that
  counter's non-zero readings, so the trigger §8.1 wrote is downstream of this fire, not upstream of it.

**The auth plane is excluded in both branches, and stays excluded** (§5.3 — both arms of `IsAuthPlane`, not
just the `GrantTable` one). Such a lens's stale row can be an over-grant in *either* direction, so licensing
it needs a healer covering both — a larger
question than this design should settle, and one no variant of §8.1's deferred repair answers.

**Two numbers in the board row's source were wrong, and this fire corrects both** (§2): the population the
fix serves is **45 of 60** plain lenses, not 60 (15 are single-node and have no neighbour endpoint at all),
and of those **9 carry a variable-length hop** and can never be indexed, so **≤36** are addressable. The
"1-hop majority" holds against the exposed 45 (25/45), **not** against the plain corpus (25/60 = 42%).

---

## 1. Problem and intent

### 1.1 The mechanism, re-derived from code

A plain lens has no `{key: $actorKey}` position. Its evaluation is seeded only when the event vertex is of
its **anchor pattern's** own type (`seedAnchorFor`, `pipeline.go:1190-1204`, gated on
`rs.seedAnchorLabels`). Every other event — a neighbour vertex root, a neighbour aspect, or either endpoint
of a link — reaches `evaluateForEntry` with an empty seed (`evaluate.go:262-263`), and the full engine then
builds the anchor pattern's candidate set by **listing**:

```go
case n.Label != "":
    keys, err = ex.coreKV.ListKeysPrefix(ex.ctx, substrate.VertexPrefix+"."+n.Label+".")
```

`internal/refractor/ruleengine/full/executor.go:724-725`, followed by `ex.fetchNode(k)` and `nodeMatches`
**per key** (`:733-757`). So the cost of one neighbour event is *one prefix listing + one GET per vertex of
the anchor type + one binding row per survivor*, and it is paid again for the next event.

*(Citation correction: `auth-plane-projection-latency-design.md` §22.1 cites `full/executor.go:669` for this
listing path. Line 669 is now inside the `key`-property point-read fast path; the listing is `:724-725`
(`:717` for the `LabelExpand` variant). The mechanism is exactly as §22.1 described it — only the line
moved.)*

Three arms reach that seam and all three are covered by one fix, because all three converge on the single
`seedAnchorFor` call at `evaluate.go:262`:

| Arm | Entry | Today |
|---|---|---|
| `evalPlainLinkReprojection` (`pipeline.go:3108`) | **both** endpoint vertices, in turn | the anchor-side endpoint seeds; the neighbour-side endpoint rescans everything |
| `evalPlainAspectReprojection` (`pipeline.go:3073`) | the aspect's parent vertex | seeds iff the parent's type is the anchor label |
| the vertex-root arm | the event vertex | seeds iff its type is the anchor label |

That last column is deliberately weaker than "is the anchor": `seedAnchorFor` gates on `eventLabel ∈
rs.seedAnchorLabels` (`pipeline.go:1194`) — the **label**, never proof the vertex binds at the anchor
*position*. Where a lens binds the same label at the root and at a neighbour position, an anchor-labelled
event seeds today and reprojects only the rows where that vertex is the root, silently missing the rest.
`duplicateCandidates` (`MATCH (b:identity)-[:duplicateOf]->(a:identity)`) and
`identityCredentialBindingsRead` (`MATCH (c:identity)-[:boundTo]->(u:identity)`) are live instances. This is
a **pre-existing under-reprojection this design's own machinery fixes**, and Increment 4b builds the fix
rather than filing it (§4.4).

For `clinicProviders` (`packages/clinic-domain/lenses.go:605`, `MATCH (pr:provider) … OPTIONAL MATCH
(pr)-[:identifiedBy]->(id:identity)`), a link event `lnk.provider.X.identifiedBy.identity.Y` runs both
endpoints: the `provider` endpoint seeds one anchor, and the `identity` endpoint rescans **every provider
vertex** to re-derive rows that were, row for row, already produced by the first endpoint. A plain
`vtx.identity.Y` root or aspect event does the same rescan with no cheap sibling call at all.

### 1.2 The observed cost

The figure the board row carries — *1,325 events wedged `clinicProviders`* — comes from Increment 3's live
shadow counters on a running stack (`auth-plane-projection-latency-design.md` §19.2 / §22.1), not from
anything a corpus census can reproduce. It is **sourced, and not independently reproducible here**; the
independent census run for this design (§2) confirmed the *shape* it was measured on and could not confirm
the count. Increment 2 below re-measures it as its own first act, and the design does not otherwise rest on
the number.

### 1.3 Intent

Two things, and the second is not a bonus so much as the reason the first is worth doing carefully:

1. **Stop paying a whole-corpus rescan for a bounded change.** The pattern graph already knows which
   positions a changed element can bind and how far each sits from the terminus; the plain arm is the one
   consumer that cannot ask it.
2. **Close a retraction hole the rescan structurally cannot reach.** An unseeded neighbour evaluation runs
   the filter-retraction presence check with a **neighbour** entry, and `AnchorProjectionKey` answers
   `ok == false` for one (`evaluate.go:280-284`) — so no anchor is ever retracted on that path. Once the
   affected anchors are derived, each runs its own *seeded* evaluation with the presence check on an
   **anchor** entry, and an anchor that a link removal drops out of the matched set retracts. §6 treats this
   as its own behaviour class with its own tests, per §22.1's instruction that it "deserves a test rather
   than an argument."

---

## 2. Census — the population, corrected

Run independently of, and concurrently with, the drafting of this design, briefed to **falsify** the board
row's figures (`agents/designer/SKILL.md` §2, the 2026-08-11 reflex). It corrected two of four claims.

**The command.** A scratch test in `package refractor_test` reusing `forEachCorpusCypher`
(`internal/refractor/label_derivation_corpus_census_test.go:536` — it enumerates the **expanded** corpus, so
walk-generated lenses are counted and no source glob can under-approximate it), classifying each cypher by
`full.CompiledRule.AnchorHopIndex()` plus its pattern shape. **Increment 1 ships this as a permanent census
test** (§11), so the numbers below are re-derivable rather than quoted.

| Question | Row/§22.1 said | Measured | Verdict |
|---|---|---|---|
| Plain lenses (no `$actorKey` position) | 60 of 60 | **60** plain / 54 anchored / 114 total; the 54 match `corpusAnchorIndexVerdicts` exactly | ✅ confirmed |
| Population the fix serves | (implied 60) | **45** — 15 plain lenses are single-node and have **no neighbour endpoint** | ❌ corrected |
| "1-hop majority of the plain corpus" | majority | 25 of 45 exposed (56%); **25 of 60 plain (42%) — not a majority** | ❌ corrected (denominator) |
| Shapes an index can never step | — | **9** of 45 carry a variable-length hop; **zero** untyped hops anywhere in the plain corpus | ➕ new |

**Addressable ceiling: ≤ 36** (45 exposed − 9 variable-length). Their hop-distance profile from the anchor
pattern: **21 at distance 1**, 12 at 2, 2 at 3, 1 at 4.

> **Amended, Incs 1+2 build note (2026-08-13): the ceiling was not reached.** Increment 1's census test
> (`internal/refractor/plain_scanroot_corpus_census_test.go`), the executable form of this section, measured
> **33 addressable**, not 36 — 3 of the theoretical 36 refuse on the now-reachable ungrounded-pattern-head
> conjunct (§4.1's table), exactly the outcome the next paragraph flagged as possible before this fire ran.
> Distance profile: **20 at 1, 12 at 2, 1 at 3** (the single distance-4 lens is one of the three that now
> refuses). The **"Adapter split of the 36"** paragraph below and its 22/14 and 5-`GrantTable` breakdowns are
> therefore stale by population size; the census test, not this prose, is the number's live source going
> forward — re-derive the adapter split from it before relying on it for §5's licence (Increment 3's job).

The nine that stay on today's behaviour: `applicantRosterRead`, `cafeIdentitiesRead`, `cafeLeaseWorkplaces`,
`landlordLeaseApplicationsRead`, `landlordUnitsRead`, `menuCatalog`, `wellnessIdentitiesRead`,
`wellnessMembers`, `wellnessSessions`. **The in-flight typed-relation-signatures design does not rescue
them**, and says so itself: a signature makes a variable-length hop *deliverable-narrowable*, not
*steppable backward* (`typed-relation-signatures-design.md` §"Doesn't `HopIndex` need this too?"). The two
designs touch adjacent code and answer different questions — which events are **delivered** versus which
anchors are **re-executed** once one arrives. Neither substitutes for the other, and this design does not
claim their populations compose.

**Why ≤ and not =.** Two of the completeness conjuncts (`ground`, the WITH-scope walk) are *terminus-relative*
and are unmeasurable today, because the builder refuses at `anchor < 0` before it ever evaluates them for a
plain lens. Some of the 36 will refuse on grounding — a second `MATCH` headed by an unbound variable really
does re-scan a bucket and really does affect every anchor. **This design therefore does not predict the final
number**; Increment 1's census test reports it, and a refusal that turns out to dominate becomes a filed,
grounded follow-on rather than a guess made now. (The posture is copied deliberately from
`lens-projection-divergence-audit-design.md` §4.4, which declined to predict its own enrolment count.)

**Adapter split of the 36** — load-bearing for §5's licence, because Auditor enrolment requires
`adapter.RowReader` and only `NatsKVAdapter` implements it (`natskv.go`'s `var _ RowReader`; `PostgresAdapter`
has no `GetRow`): **22 `nats-kv`, 14 `postgres`** (`capabilityReadWildcardGrants` is declared
`Adapter: "postgres"` at `internal/bootstrap/lenses.go:274` — it is a kernel lens, not a third adapter).
Of the Postgres ones, **5 are `GrantTable:
true` auth-plane grant producers** (`patientIdentityReadGrants`, `providerIdentityReadGrants`,
`staffReadGrants`, `consoleOperatorReadGrants`, `demoOperatorReadGrants` — the other two of the corpus's seven
are single-node and out of scope) and 8 are Protected/RLS `*Read` lenses. Nothing here narrows them until either
`RowReader` reaches the Postgres adapter (the RLS `*Read` lenses) or a both-directions healer exists (the
auth-plane grant producers, which §5.3 excludes on their own conjunct, not on the adapter's accident).

---

## 3. Grounding ledger

Every row cites the code that **does** the thing, never a comment describing it.

| Fact | Where | Consequence for this design |
|---|---|---|
| `HopIndex.Anchor` is the `$actorKey` position; `-1` ⇒ `Incomplete = "no pattern position binds $actorKey"` | `full/hopindex.go:238-240`, `nodePinsActor:547-554` | A plain lens is refused before any other conjunct runs |
| `DeclaresActorAnchor()` **is** `Anchor >= 0`, and is 4b's install-completeness guard | `full/hopindex.go:309-314` | **Widening `Anchor` is forbidden** — it would mark all 60 plain lenses "declared actor-aware, no enumerator installed". §4.1 adds a *second index*, not a wider field |
| `Dist` is BFS from `Anchor` over **binding** hops only | `full/hopindex.go:328-361` | Transfers verbatim to a root terminus; the non-binding exclusion is what keeps a WHERE-NOT shortcut from faking nearness |
| `AnchorSideSeeds` pairs each hop's nearer end with the link endpoint sitting there | `full/hopindex.go:640-674` | Unchanged; it reads `Dist`, so it works on either terminus |
| `walkToAnchors` mints anchor keys as `vtx.<Labels[Anchor]>.<id>` and does not expand from a node reached at the terminus | `pipeline/anchor_derivation.go:180, 218-221` | The "pinned to one vertex" soundness argument transfers because a seeded evaluation pins the root the same way — `seedAnchorBinds` + `pointCandidate` (`full/executor.go:679-681`) |
| `derivationIndex` requires `p.actorEnumerator != nil` as its **first** conjunct | `pipeline/anchor_derivation.go:124` | A plain lens fails before the index is consulted; §4.2 adds a sibling entry point rather than relaxing this one |
| `derivationIndexForAct` adds `patternClosedOutput` and `sweeper != nil` | `pipeline/anchor_derivation_mode.go:212-229` | Both are unavailable to a plain lens — §5 replaces each with a derived equivalent |
| `patternClosedOutput` is set **only** by `projection.InstallActorAggregate` | `pipeline.go:339`, `projection/driver.go:411` | `false` for every plain lens; §5.1 derives the property instead of asserting it by install path |
| `Reproject` returns `ErrNotActorAggregate` with no envelope; it is the sweeper's only repair verb | `pipeline/reproject.go:287-289`, `pipeline/sweep.go:446` | **seedable ⇒ un-sweepable**: a plain lens cannot install the ratified healer |
| A plain lens's target is unguarded unless auth-plane | `projection/plan.go:94-96` (`RequiresGuard = AuthPlane \|\| RequiresGuardedTombstone()`), `:110-114` (`IsAuthPlane`) | An auth-plane plain lens (`GrantTable: true`) **is** guarded — inverting §8.1's ordering objection exactly where the stakes are highest |
| An **unguarded** NATS-KV target read-before-writes and skips the `Put` when the stored bytes are identical | `adapter/natskv.go:203-211` | Today's rescan is N target reads + N Core-KV GETs, and writes only the rows that differ. Its own doc names the cost centre: *"pays off exactly when an unanchored lens rewrites its full row set on a trigger that left this particular row's content unchanged"* |
| `nodeMatches` binds on the **key type** only — a body `class` is matched by property predicate, never by label | `full/executor.go:600-620` | A key-type-shaped derivation is **complete** for the binder, not merely a coverage-bounded approximation. (This is narrower than the divergence design's §4.3 coverage caveat, which cites the pre-taxonomy `nodeMatches`; the dynamic-type-taxonomy fire made a location's class its own key type.) |
| `seedAnchorFor` refuses when `diffRetraction` is on | `pipeline.go:1200-1202` | A per-anchor seeded row set would read to `applyDiffRetraction` as "every other anchor is gone" — the conjunct must be inherited, not re-derived |
| `AnchorProjectionKey` answers `ok == false` for a neighbour-typed entry | `pipeline/evaluate.go:280-284` | Today's neighbour rescan **cannot** retract; §6 |
| The link arm applies the triggering edge to `adjKV` **before** evaluating | `pipeline.go:3137-3143` | The walk's first hop never reads a stale edge for the event's own link |

---

## 4. The shape

### 4.1 Increment 1 — a second terminus on `HopIndex`, not a wider `Anchor`

`AnchorHopIndex()` keeps its exact meaning and every existing consumer is untouched. Add:

```go
// ScanRootHopIndex builds the same pattern graph with the plain arm's own
// terminus: the anchor PATTERN (anchorPattern(q) — the first MATCH clause's
// first node), the position a seeded plain evaluation pins to one vertex.
func (cr *CompiledRule) ScanRootHopIndex() HopIndex
```

It runs the **same builder** over the same clauses, differing in one place: which position is recorded as the
terminus. The anchored index notes it at `nodePinsActor`; the root index notes it at the anchor pattern's
node. Everything downstream — `Dist`, `AnchorSideSeeds`, `StepsFrom`, `PositionsBinding`, `admitsType`,
`walkToAnchors` — reads `HopIndex.Anchor` as *"the terminus"* and is reused **verbatim**. That is the whole
point of the shape: one index type, two termini, zero duplicated consumers, and no second independently
fallible derivation to keep in lockstep.

Refusal conjuncts, in the order the switch already evaluates them, with the two that are new to this terminus
marked:

| Conjunct | Reason string | Why |
|---|---|---|
| no anchor pattern, or it is **unlabeled** *(new)* | `"the anchor pattern position carries no label"` | An unlabeled root binds any type: one key prefix cannot be minted and `seedAnchorBinds` refuses it anyway (`executor.go:842-844`) |
| the root carries a `key` property *(new)* | `"the anchor pattern is pinned by its own key"` | Already a point read; there is no scan to remove |
| root carries the `*` sigil | reused verbatim | One literal `vtx.<label>.` prefix cannot be right for an expanded set — `hopindex.go:243-256` |
| untyped hop / variable-length hop | reused verbatim | Not steppable |
| WITH-scope reject | verdict reused, **justification re-derived** | `withscope.go:19-21` licenses a `WITH` dropping the anchor's variable because *"the anchor is the `$actorKey` PARAMETER rather than a row column"* — for a root terminus the anchor **is** a row column. The refusal still lands (a drop-then-re-reference is `withReReference`), but the reason must be re-stated, not inherited |
| ungrounded pattern head | reused verbatim, **now reachable** | `ground()`'s `b.anchor < 0` early-out (`hopindex.go:493-495`) currently swallows this conjunct for every plain lens; with a terminus it becomes the real, load-bearing refusal — a second `MATCH` headed by an unbound variable genuinely affects every anchor |

`b.multiAnchor` (`hopindex.go:241-242`) has no counterpart and becomes **dead** on this index: the root is
one position by construction. Say so in the reason vocabulary rather than leaving an arm nothing can reach.

**One ordering constraint the builder must honour.** `addPattern` walks a node's property-map expressions
through `addExpr` **before** the loop that creates the node positions (`hopindex.go:420-424`), and a
`PatternExpr` in that map reaches `ground()` while the terminus is still `-1`, refusing the index for the
wrong reason. The terminus must therefore be recorded from the first binding pattern's `Nodes[0]` **before**
any property expression is walked. Note this cannot be done by re-calling `position()` after `addPattern`
returns: `position()` is idempotent only for a *named* node and mints a fresh class for an unnamed one
(`hopindex.go:399-413`).

`Complete`/`Incomplete` on the returned value describe **this** index. Nothing reads them across the two.

**Cost.** One extra AST walk per rule publication (`useFullEngineBranches`), never per event — the same
budget `AnchorHopIndex()` already documents for itself (`hopindex.go:286-288`). Published onto `ruleState`
beside `anchorHops`, on the same multi-walk exclusion (`pipeline.go:978-1002`): a multi-walk lens has N
anchors and one terminus cannot speak for all of them.

**Value on its own:** the census test (§11) that reports the real addressable count, and the conjunct
vocabulary an operator reads in the health surface. It ships no behaviour change, which is why it pairs with
Increment 2 in one fire (§12) rather than standing alone as scaffolding.

### 4.2 Increment 2 — the plain arm asks, in shadow

One new entry point beside the three shipped ones, reusing `walkToAnchors` unchanged:

```go
// deriveAnchorsForPlain* mirror deriveAnchorsFor{Vertex,Aspect,Link}, reading
// rs.rootHops instead of rs.anchorHops, and are gated by plainDerivationIndex.
func (p *Pipeline) plainDerivationIndex(rs ruleState) (full.HopIndex, bool)
```

`plainDerivationIndex` replaces `derivationIndex`'s enumerator conjuncts with their plain equivalents:
`p.actorEnumerator == nil && p.envelopeFn == nil && p.multiEnvelopeFn == nil` (a plain pipeline), `len(branches) <= 1`,
`rs.rootHops.Complete`, `UnresolvedExpansionPosition() < 0`, and `!p.diffRetraction`. There is no
"anchor label == enumerator's actor type" conjunct to carry, because the terminus's label **is** the anchor
label the seed is built from — one derivation, so the two cannot disagree.

**The seam is `evaluate.go:262`, where `seedAnchorFor` already decides — and the derived anchors re-enter
through the entry point that already exists.** When `seedAnchorFor` returns a seed, nothing changes. When it
returns `""` for a neighbour entry and the derivation answers, the arm calls
**`evaluatePlainFromVertex(anchorKey, anchorLabel)` once per derived anchor** (`pipeline.go:3180-3195`) —
the same function the anchor-typed arms already use — instead of running the unseeded whole-corpus
evaluation. When the derivation declines, today's single unseeded evaluation runs, unchanged. **A derivation
bug degrades to current behaviour, never to silence** (`anchor_derivation_mode.go:115-118`).

Re-entering the shipped path rather than hand-rolling "K seeded evaluations" is what makes the change small,
and it settles five questions that would otherwise each be a decision:

- **The anchor's own body is fetched** — `evaluatePlainFromVertex` point-reads it (`fetchVertexProps`) and
  returns `(nil, nil)` for a missing or tombstoned vertex, which is the correct disposition for a key the
  walk minted from a typeless edge it deliberately kept (`anchor_derivation.go:238-263`): that anchor's row
  lifecycle belongs to the vertex-root path, not to this event.
- **`$projectedAt` and `$actorKey`** are then built from the anchor's own props, exactly as they are for an
  anchor-typed event today (`evaluate.go:588-597`) — no new provenance shape, and no new
  `ErrNoProvenanceTimestamp` surface.
- **The presence check runs per anchor, against that anchor's own results**, with an anchor entry — which is
  what §6 is about, and it is the shipped check at `evaluate.go:280-284`, not a new one.
- **`seedAnchorFor` seeds on re-entry** (the anchor *is* anchor-labelled), so no recursion is possible: the
  derivation fires only on the `""` branch.
- **`$now`** would still be a fresh instant per evaluation; §5 excludes `$now` lenses, and that exclusion is
  load-bearing *here* and not only for the Auditor.

**Three mechanical obligations the union creates.** (i) `dedupeKeyFor` dedupe must be hoisted into the
derived-path helper — today only the link arm dedupes (`pipeline.go:3164-3171`); the aspect and vertex arms
hand their slice straight to `writeResults` and would now carry K anchors' results. (ii) **Error
disposition:** first error aborts the whole event, matching the link arm's shipped behaviour
(`pipeline.go:3158-3163`); redelivery re-runs all K, which is idempotent. State the widening plainly — a
`Terminal` error from one derived anchor now DLQs an event that today would have written the others.
(iii) **Accounting:** `recordProjected` fires per result and `writeAudit` per written row, so both scale
with K; the measurement in §11 must read them in those units.

**Two caps, both fallbacks rather than truncations.** `DefaultDerivationReadCap` (2,000 adjacency documents)
applies unchanged. A second, new bound — `DefaultPlainDerivedAnchorCap`, proposed **64**, operator-settable —
falls back to the unseeded evaluation when the derived set is large. **Its unit is derived root vertices, not
projected rows**, and the two diverge: for a lens keyed on a neighbour variable, K root bindings can produce
one row. That makes it a bound on *work*, which is what it is for; the fire must report both distributions so
the number is set against the right one.

**Shadow first.** Increment 2 runs the derivation through the existing `affectedAnchors` mode switch in
`shadow`, so the BFS-equivalent (today's unseeded evaluation) still decides every event while the counters
report what acting *would* have done. That produces the two numbers Increment 4 needs and §1.2 could not
supply: the real per-lens event volume, and the derived-set size distribution. The plain and actor-aware
counters cannot mix — a pipeline is one lens and is either plain or actor-anchored — so no new counter state
is introduced.

**Worked payoff, traced through every conjunct of the gate** (the payoff-claim discipline —
`agents/designer/SKILL.md` §2, 2026-08-10):

`clinicProviders` — `nats-kv` adapter ✅ (so `RowReader` exists), not auth-plane ✅, single-branch ✅, no
`diffRetraction` ✅, no `$now`/`$projectedAt` ✅, not a Secure Lens ✅, root labeled `provider` ✅, one typed
fixed-length hop ✅, `OPTIONAL MATCH` headed by the already-grounded `pr` ✅. On
`lnk.provider.X.identifiedBy.identity.Y`, `AnchorSideSeeds` returns the **root-side** seed alone (`ds=0 ≤ dd=1`)
and `walkToAnchors` emits `vtx.provider.X` with **zero adjacency reads** — the entire second endpoint's work
collapses into a duplicate of the first's. On a bare `vtx.identity.Y` root or aspect event, the walk seeds at
position 1, reads **one** adjacency document, and returns that identity's providers. The headline consumer
passes every conjunct; it is not a lens the design merely hopes to serve.

### 4.3 Increment 3 — the licence (Option A)

Acting is licensed per lens by the conjunction in §5. It is evaluated **per event** off live pipeline fields,
never snapshotted at install — the rationale `seedAnchorFor` and `actorAwareNarrowingLabels` already state
(`pipeline.go:1290-1297`): activation installs components in stages, so a snapshot taken during installation
reads a later stage's component as absent, and for a *licence* that is the fail-open direction.

### 4.4 Increment 4 — flip to act; and 4b, the seeded branch's own gap

**4a.** Default `act` for licensed plain lenses, with the same three-mode operator knob
(`REFRACTOR_ANCHOR_DERIVATION`) governing both arms. Carries the measured before/after and the e2es in §11.
This is the posture-changing increment (§12).

**4b.** Route the **seeded** branch through the same derivation when the lens binds the anchor label at more
than one pattern position — the §1.1 gap. `PositionsBinding` already returns both positions
(`anchor_derivation.go:63-65`), so the machinery 4a builds answers it with no new mechanism, and the two live
consumers are named (`duplicateCandidates`, `identityCredentialBindingsRead`). Built rather than filed
because the consumer is nameable and the fix is a call-site change. Sequenced after 4a so the flip and this
correction are separately attributable in the measurement.

### 4.5 Increment 5 — the `retained`-class repair, deferred behind its own trigger (fork resolved: not pre-built)

Not built by default. §9.1 states the case and recommends against pre-building it: the trigger
`lens-projection-divergence-audit-design.md` §8.1 wrote for its own deferred repair — *a plain lens reporting
a sustained non-zero `retained` count* — is produced **by** Increment 4, not before it.

---

## 5. The licence, and what each conjunct replaces

The ratified `derivationIndexForAct` licence has two conjuncts a plain lens cannot satisfy. Each is replaced
by a **derived** equivalent rather than dropped.

### 5.1 `patternClosedOutput` → **per-anchor** closure, which is a stronger property than the Auditor needs

The first draft of this design reused `lens-projection-divergence-audit-design.md` §4.4's `auditEnrolment`
predicate wholesale. **That substitution is unsound, and the adversarial pass (§15, B2) is why this section
was rewritten.** `patternClosedOutput` is asserted only by `InstallActorAggregate` — i.e. only for lenses
whose anchor is pinned by `{key: $actorKey}` in *every* evaluation, where "the subgraph the pattern binds"
and "*this anchor's* subgraph" are the same set. For a plain lens they are not: the unseeded evaluation
ranges the root over the whole `vtx.<label>.` bucket (`executor.go:724-725`); the seeded one pins it to one
vertex (`executor.go:679-681` → `pointCandidate`). So the dependency class that matters here is not
"outside the pattern" but **inside the pattern and outside this anchor** — a `RETURN`/`WITH` whose grouping
key binds a non-root variable, or which aggregates across root bindings (`projectItems` groups by the
non-aggregating items, `executor.go:1219-1300`). A seeded evaluation then computes a *truncated* row.

`auditEnrolment` has no such conjunct, **by design**: the Auditor never writes, and §4.3 of that design says
an `AnchorProjectionKey`-`ok=false` lens *"is simply not checked in this direction."* A read-only predicate
that tolerates a gap is not a write licence.

**The conjunct, and it already exists:** `AnchorProjectionKey`'s **`ok` contract**
(`anchor_delete.go:53-60`) — no `WITH`, and every key column resolving read-free from the anchor binding to a
scalar, *"which holds exactly when the lens projects at most one row per anchor, keyed by the anchor."* That
is precisely per-anchor closure, it is already computed, already tested, and §6's Delete cannot fire without
it anyway. The licence takes it whole.

It is deliberately **sufficient rather than necessary**: a neighbour-keyed lens whose rows happen to be
partitionable by anchor is refused too, and keeps today's behaviour. Widening it needs a real partitionability
derivation, which is a separate design and not this one. The three `auditEnrolment` conjuncts that *do*
transfer — no `$now`/`$projectedAt` (`CompiledRule.ReferencesParam`, that design's §4.5 primitive), no secure
decryptor, not actor-aware — are carried alongside it, so the two predicates overlap without one standing in
for the other.

> **Build correction, 2026-08-14 (Increment 3).** The paragraph above under-specifies the conjunct: "no
> `WITH`, every key column resolving read-free from the anchor binding to a scalar" admits a lens whose key
> column is a **literal** (e.g. `RETURN 'all' AS key, collect(u.name) AS names`) — a literal resolves
> read-free from the anchor trivially, but such a lens still aggregates across every root binding, which is
> exactly the truncation hazard this section exists to exclude. Inc 3's adversarial review caught this against
> the shipped code, not against a live corpus lens. The as-built conjunct is `ProjectsOneRowPerAnchor`
> (`anchor_delete.go`): the closure half above (factored into `HasAnchorOnlyKeyColumns`, a structural-only
> variant needing no concrete event — the licence has no event to evaluate against) **AND** at least one key
> column must *identify* the anchor, not merely resolve from it — its own `.key`, or `nanoIdFromKey` over it,
> checked by name against an allowlist (an unrecognized function refuses, fail-closed; this is not a denylist
> of known-lossy functions). `capabilityRoleIndex`'s `collect(DISTINCT …)` is still excluded (the aggregate
> itself fails the closure half, unchanged); the literal-key shape is excluded by the added identification
> half. `clinicProviders` and every other `<anchor>.key`-keyed lens in the addressable set are unaffected —
> confirmed against the full corpus, no admitted lens's verdict moved. `AnchorProjectionKey`/`AnchorDeleteResult`
> themselves are unchanged (this conjunct is layered on top, in the licence, not folded into the shipped `ok`
> contract, so §6's Delete path keeps its current behaviour).

**A corpus shape proves the hazard is not theoretical.** `capabilityRoleIndexSpec`
(`packages/rbac-domain/lenses.go:97-105`) roots on `role`, keys on `perm.data.operationType`, and
`collect(DISTINCT …)`s across root bindings — `TestCapabilityRoleIndex_CollapsesRolesPerOperation` pins that
the correct row names *both* roles. It is excluded today only by `p.envelopeFn != nil`, and five more of the
addressable set only by `DiffRetraction` — **both unrelated conjuncts**. Nothing in the corpus is
mis-projected today; nothing was stopping the next cypher edit from making one, either.

### 5.2 `sweeper != nil` → an **enrolled Auditor**

The ratified conjunct means *"something standing will re-test this row."* For plain lenses the standing
thing that exists — and the only one that can — is the Auditor. So the conjunct becomes
`p.auditor != nil && auditor.Enrolled()`. Its properties:

- **Fail-closed and self-selecting.** A lens the Auditor refuses (no `RowReader`, Secure, `$now`) is a lens
  that never narrows, and it keeps today's accidental sweep. Nothing needs a second list.
- **It makes the trade explicit.** After narrowing, a diverged row is *detected, named, and alarmed*
  (`LensProjectionDiverged`) instead of *maybe* silently re-written by the next neighbour event. Given that
  the failure this whole area is recovering from was a twelve-day silence, visible-and-broken beats
  invisible-and-maybe-fixed.
- **It is a real dependency, not new scope.** The Auditor is Fire 2 of an already-ratified design, already on
  the board. §12 sequences behind it.

`adapter.RowReader` is required **twice over**: the Auditor's own enrolment needs it, and so does §6's
zero-row Delete probe. It is therefore a conjunct of the *mechanism*, not merely inherited from enrolment.

### 5.3 Auth-plane plain lenses do not narrow — and the conjunct must be made able to fire

An auth-plane lens projects an authorization surface, so a stale row can be an over-grant in either
direction. The actor-aware precedent required a repair-capable healer *proven end to end* before narrowing
that plane (Done-log `8013da3e`). This design gives detection only, so the licence excludes it.

**`IsAuthPlane` has two arms and the first draft named only one** (§15, B1): `nats_kv` into the
capability bucket, **or** `postgres` with `GrantTable` (`plan.go:110-116`). `capabilityRoleIndex`
(`packages/rbac-domain/lenses.go:60-67`) is a `nats-kv` capability-bucket lens and is in the first class. The
conjunct is written against `projection.IsAuthPlane(r)`, both arms, never against `GrantTable` alone.

**And `p.authPlane` cannot be read off the pipeline as things stand.** `SetAuthPlane` has exactly one
non-test caller — `projection/driver.go:357`, inside `InstallActorAggregate` — and `Compile` may only be
called for an actor-aggregate (`plan.go:118-120`). So on a plain pipeline the field is `false` **by
construction**, and `!p.authPlane` is a tautology: a guard that can never fire, with a test that would pass
vacuously. Increment 3 therefore **threads the value in**: `cmd/refractor/reload.go:259` already computes
`projection.IsAuthPlane(r)` into `entry.authPlane` and never hands it to the pipeline. The §11 licence test
installs through the real activation path, not a hand-set field — otherwise the test asserts the tautology
rather than the guard.

---

## 6. The new retraction class

Under today's behaviour a neighbour evaluation **cannot** retract: `AnchorProjectionKey` returns
`ok == false` for a neighbour-typed entry (`evaluate.go:280-284`). Because the derived path re-enters
`evaluatePlainFromVertex` per anchor (§4.2), the presence check runs with an **anchor** entry and that
anchor's own props, so an anchor a link removal drops out of the matched set now emits a `Delete`. **The
mechanism is the shipped check, unchanged; only who it runs for is new.**

**Its reach is exactly the licence's closure conjunct** (§5.1) — `AnchorProjectionKey`-`ok` lenses: no
`WITH`, one row per anchor, keyed by the anchor. That is not a caveat bolted on; it is the same predicate,
which is why B2's fix and B3's fix are one change.

**The first draft's exemplar was wrong and is withdrawn.** It named `wellnessMemberAccounts` as a lens this
closes. That lens is refused **twice** by the very predicate the retraction depends on
(`packages/wellness-ledger/lenses.go:326-333`): it carries `WITH DISTINCT id`, rejected wholesale at
`anchor_delete.go:75-79`, and every key column binds `id`/`a` rather than its anchor variable `bk`, rejected
at `:145-153`. Its anchor is `bk:booking` at distance **0**, not 2. **This design closes nothing for that
lens** — its board row stands untouched, and the two facts are related: the reason it has no retraction
transport is the reason it cannot be narrowed.

**A never-matched anchor must not manufacture a Delete.** `walkToAnchors` prunes on relation, direction and
far-end label only (`anchor_derivation.go:234-264`) — it models **no `WHERE`**. So a licensed lens with a
filtering `WHERE` (`clinicProviders`' own `WHERE pr.profile.data.fullName <> null`) derives anchors that
never projected a row, each of which would emit an unconditional `adpt.Delete` (`pipeline.go:3350`) — a KV
delete marker per never-matched anchor per neighbour event on the hard-delete path, and on `DeleteModeSoft`
a durably *created* tombstone for a row that never existed. This is the exact failure `zeroRowDeleteKey`
exists to prevent on the actor-aware arm (`evaluate.go:1241-1250`), closed there with a positive
`RowReader.GetRow` probe. **The derived path takes the same probe:** a `Delete` produced by a derived anchor
is dropped unless `GetRow` reports the row present. (Found by the adversarial pass, §15 M1; the first draft
asserted the opposite — that over-derivation costs only extra upserts.)

**Direction of risk, restated correctly.** A `Delete` is emitted per anchor, from that anchor's own
re-execution, never because the walk named it — but over-derivation reaches the Delete path, so it is gated
by the probe above rather than by argument. Under-derivation costs a missed retraction, which is why §5.3
holds the auth plane out. §11 pins both directions, and the probe gets its own test.

**Sizing withheld.** The first draft said "15 of the addressable 36". That counts lenses at distance ≥ 2, not
the link classes that are new: in a `root—m—n` pattern the `root—m` hop is anchor-incident and already
retracts today. The real figure is the intersection of (non-anchor-incident relations) and (closure-conjunct
lenses), and Increment 1's census reports it rather than this doc predicting it.

---

## 7. Contract surface

**No change.** Explicitly, per contract:

- **#6 (projection/lenses).** §6.2's ordering guard and its token are untouched: this design introduces no
  new write class and no new token. Every write it causes is the *same* CDC-path write the unseeded
  evaluation would have produced for that anchor, from the same evaluation code, at the same sequence.
  §6.14's RLS/protected-lens rules are untouched because §5.3 excludes that plane.
- **#1 (addressing).** The walk mints `vtx.<type>.<id>` keys exactly as `walkToAnchors` already does.
- **#2 (operations).** Read path only; nothing submits an operation.

**A convention worth questioning, flagged rather than worked around:** `patternClosedOutput` is a *boolean
asserted by one install path* for a property that is derivable from the compiled rule. §5.1 derives it for
the plain arm; converting the actor-aware arm to the same derivation would remove a field whose default is
deliberately the unsafe value. Not in scope here — filed as an observation for whoever next touches that
install path.

---

## 8. Reconciliation with the existing mental model

**"Didn't we already build the affected-anchor derivation?"** Yes — for the actor-aware arm, and the fire
that built it stopped at exactly this boundary and said so (`auth-plane-projection-latency-design.md` §22.1,
"4a-3 is not a wiring job"). Its two named blockers are what §4.1 and §5 answer: the terminus, and the
healer. The derivation, the walk, the caps, the mode switch, the shadow counters and the health surface are
all reused unchanged.

**"Doesn't this duplicate the divergence Auditor?"** No — it *depends* on it, and the dependency is
one-directional. The Auditor answers "is this row right"; this design answers "which rows can this event have
moved". Three of the Auditor's enrolment conjuncts are shared verbatim (no `$now`/`$projectedAt`, no secure
decryptor, not actor-aware) and its **enrolment is a licence conjunct** — but this design adds per-anchor
closure on top, because a write licence needs a property a read-only verdict does not (§5.1). The two
predicates overlap deliberately; neither stands in for the other.

**"Doesn't the `KVGetMulti` conversion make this unnecessary?"** No, and the two compose. The board's
*Convert the ~85-site `ListKeysPrefix`/list-then-get corpus to `KVGetMulti`* row names the rule engine's
anchor scans, and batching those GETs would cut the **constant** on the listing path. It does not touch the
**complexity**: `nodeMatches` and a binding row are still paid per candidate. The terminus removes the N
entirely where it applies; `KVGetMulti` improves the un-narrowable remainder — the 9 variable-length lenses
and every cap fallback. Both are worth having, in that order.

**"Does this introduce new state?"** One field: `ruleState.rootHops`, a derived `HopIndex` published by the
same copy-on-write publication that already publishes `anchorHops`. §10 is its lifetime table. The licence is
computed per event from existing fields and latches nothing.

**"Does it contradict the design-of-record?"** No. P5 is untouched (this is entirely inside Refractor's
read-model production). P2 is untouched (no writes to Core KV). The plain arm's fallback stays total, which
is the invariant the whole surrounding mechanism was built around.

---

## 9. Alternatives considered

### 9.1 Requiring repair before narrowing — the fork, corrected

**The first draft argued against a position §8.1 never held, and the adversarial pass (§15, M2) caught it.**
It proposed an *upsert-only* repair of `missing`/`stale`, "never `retained`", and claimed that answered
§8.1's three objections and would unlock the auth plane. Both halves were wrong. §8.1 levied its objections
at a **general** repair and then singled out the one variant it *would* entertain — *"A **narrow** one might:
repair restricted to the `retained` class… Deferred with a named trigger: a plain lens reporting a sustained
non-zero `retained` count."* And `retained` **is** the over-grant class §5.3 excludes the auth plane for, so
an upsert-only repair repairs neither thing that exclusion names.

**Restated correctly.** If Andrew wants repair before narrowing, the shape is §8.1's own deferred one — the
`retained` class, where the correct end state is an unambiguous Delete — and two of its three objections are
weaker than they read at ratification time:

1. *"The target is unguarded, so a repair write is last-writer-wins with no ordering token."* The accidental
   heal this design removes is itself such a write. The volume claim needs care, though: the unguarded
   NATS-KV upsert is **content-conditional** — it reads back and skips the `Put` when the bytes are identical
   (`natskv.go:203-211`) — so a neighbour event *evaluates* the whole corpus but *writes* only the rows that
   differ, which is the same volume a deliberate repair would produce. The conclusion (a deliberate repair is
   no racier than the accident) holds; the "thousands of writes a day" framing does not.
2. *"Lenses share buckets, so a repair writes into a keyspace it does not exclusively own."* That is an
   argument about the sweep's **whole-target diff** — enumerating target keys and deleting strays. A repair
   restricted to a key the audit has already proven `retained` for a specific anchor enumerates nothing.
3. *"The failure being fixed was a repair path that concealed a detection gap."* This one stands as written,
   and is answered only by ordering: publish the verdict, **then** repair, count repairs on their own axis.
   Fire 1's `Verdict` surface exists to make that expressible.

**And a fourth objection the first draft missed, in the direction that matters most.** On a *guarded* target
the repair is not merely safe — it can be **declined**. `guardedWrite` drops the write when
`storedSeq >= incomingSeq` and drops it outright when the caller supplies no sequence at all
(`natskv.go:317-320, 345-349`), and an Auditor pass holds no CDC message. `Reproject` solves this by
borrowing `p.Progress().LastAppliedSeq` (`reproject.go:318`) and must then publish `VerdictBlocked` for what
the guard declines (`reproject.go:463-476`). Any repair increment inherits both obligations. So the guarded
plane is *harder*, not easier — the inverse of what the first draft claimed.

**Recommendation: do not pre-build it.** §8.1's trigger is a sustained non-zero `retained` count, and
Increment 4 is what would first produce one. Building the repair now is the "authoritative machinery ahead of
the observation" shape the 2026-07-27 op-meta ratification rejected — and it would not change the auth-plane
exclusion either way.

### 9.2 Rejected: widen `Anchor` to mean "terminus" and let plain lenses set it

The cheapest-looking edit, and it breaks 4b: `DeclaresActorAnchor()` **is** `Anchor >= 0`
(`hopindex.go:309-314`) and is read as the lens's *declared projection kind* by `ConsumerFilter`. Setting
`Anchor` on a plain lens would report all 60 as "declared actor-aware with no enumerator installed" and send
every one of their consumer filters broad — the corpus-wide regression 4b exists to prevent. A second index
costs one AST walk per publication and cannot collide with that reading at all.

### 9.3 Rejected: skip the neighbour endpoint's evaluation when the sibling endpoint is anchor-typed

A tempting special case for the `clinicProviders` shape: if the *other* endpoint of the link is anchor-typed,
its seeded evaluation has already produced the rows, so drop the neighbour's rescan outright. It is a
one-conditional change and needs no index at all.

Rejected because it is **only** sound for a 1-hop pattern where the changed link *is* the pattern's own hop,
and nothing in the conditional can tell that case from a 2-hop lens where the neighbour endpoint reaches a
different set of anchors entirely — the conditional would drop real reprojections silently. It is the
derivation's answer, hand-approximated, with no fallback and no way to be wrong loudly. Where it is right,
the derivation returns the same answer with zero adjacency reads (§4.2).

### 9.4 Rejected: give plain lenses a `Reproject`, i.e. a real Sweeper

The direct reading of "a standing healer a plain lens can actually have". `Reproject`'s reconciliation model
is built around an envelope and a per-actor cap-shaped key (`reproject.go:287-310`); a plain lens has
neither, and the sweep's candidate selection, ownership proof and retraction arms would each need a plain
twin. That is a larger build than this whole design, it duplicates the Auditor's recompute, and Option B
reaches the same end state by adding one write to a pass that already recomputes every anchor. If Andrew
rules against Option B *and* wants repair, this becomes the shape — but not before.

### 9.5 Rejected: raise `DefaultDerivationReadCap` instead of adding a terminus

Not an alternative — the cap governs a walk that never runs for a plain lens, because the index is refused
before the walk. Recorded because "the cap is the problem" is the plausible-sounding first read of the
symptom.

---

## 10. State lifetime

The one new stateful thing, with the three answers a data structure alone does not carry:

| Boundary | `ruleState.rootHops` |
|---|---|
| **Created** | `useFullEngineBranches`, in the same derivation block that builds `anchorHops` and `seedAnchorLabels` (`pipeline.go:978-1002`), from the compiled rule alone |
| **Reset** | Unconditionally re-derived on **every** publication, including a hot reload that edits the MATCH — never carried forward. A lens edited from 2+ walks down to 1 gains it; the reverse clears it. This mirrors the "unconditional, not just the len>1 arm" rule the adjacent fields already carry (`pipeline.go:680-687`) |
| **Carried** | Read only off the `ruleState` snapshot threaded through the event, so a reload landing mid-event cannot show one gate the new graph and the next gate the old one |
| **Ordered** | Published under the single `ruleMu` copy-on-write write, after `WithLabelExpansion` has threaded the resolved taxonomy sets — the same order `anchorHops` is published in, since both must see the same expansion or their `admitsType` answers diverge |
| **Crash / replay** | Derived, not persisted: a restart rebuilds it from the compiled rule. Nothing to recover, nothing to reconcile |
| **Tombstone / upgrade** | A retired lens's pipeline is torn down whole; a rule swap replaces the field. No independent lifetime |

The **licence** (§5) deliberately introduces no state: it is a per-event read of live pipeline fields, so
there is no snapshot to go stale and no reset to forget.

---

## 11. Test strategy

Every test below is owned by a named increment in §12 — an unowned test is built by nobody.

**Engine (Inc 1).** `ScanRootHopIndex` conjunct tests mirroring `hopindex_test.go` and
`TestAnchorHopIndex_WithScope`: unlabeled root, `key`-pinned root, `*`-sigil root, untyped hop, var-length
hop, a `WITH` that drops the root's variable, and an ungrounded second `MATCH` — each asserting its **own**
reason string, and a default-deny test over the reason vocabulary (mirroring
`TestCorpusAnchorHopIndex_EveryReasonIsAKnownConjunct`).

**The census test (Inc 1), the executable form of §2.** `plain_scanroot_corpus_census_test.go`, pinning per
plain lens whether the root index is complete and which conjunct declined — the mechanical gate that reports
the true addressable count and refuses to let a lens *arrive* at indexable without someone judging it. It
carries the same "a row moving TO indexable is the direction that needs an argument" header the anchored
census already carries, and the same `checked > N` guard so an emptied enumeration cannot read as green.
**It pins the closure conjunct too** (`AnchorProjectionKey`-`ok` per plain lens), not only `HopIndex`
completeness: the edit that matters most — a cypher gaining a non-root grouping key — moves closure while
leaving completeness untouched, so a completeness-only census would stay green through it.

**Pipeline (Inc 2).** Derived-set correctness for a 1-hop lens (zero adjacency reads) and a 2-hop lens (the
walk crosses one document); the `diffRetraction`, multi-branch, and unresolved-expansion refusals; both cap
fallbacks returning today's evaluation; and a **differential** test asserting that for a sample of corpus
shapes, the union of the seeded evaluations equals the unseeded evaluation's rows for those anchors —
mirroring `anchor_derivation_differential_test.go`.

**A negative test needs a positive vector first.** Each refusal test is paired with a shape that *does*
narrow, so "refused" is never indistinguishable from "the harness never reached the code" — and the
differential test is mutation-checked: with the walk stubbed to return an empty set, it must fail.

**Licence tests (Inc 3), installed through the real activation path.** The auth-plane refusal must be
asserted on a pipeline built by activation, never by setting `p.authPlane` by hand — the field is `false` by
construction on a plain pipeline until Inc 3 threads it (§5.3), so a hand-set test asserts a tautology. One
case per `IsAuthPlane` arm (capability-bucket `nats-kv`; `postgres` + `GrantTable`). The closure refusal gets
a lens with a non-root grouping key and a positive twin that narrows.

**e2e (Inc 4).** (a) The `clinicProviders` shape: an `identifiedBy` link event reprojects only the affected
provider — asserted by the *revision* of a bystander provider's row not moving, not by timing. (b) The §6
retraction class: removing a non-anchor-incident link on a closure-conjunct lens retracts the anchor's row,
which today it does not. (c) The §6 **probe**: a `WHERE`-filtered anchor the walk derives but which never
projected a row produces **no** delete marker in the target bucket. (d) The licence: an un-enrolled lens
(no Auditor) provably keeps the whole-corpus rescan.

**Measurement (Inc 2 → Inc 4).** The shadow counters' before/after on the dev stack, reported in the build
note as a table: events observed, derived-set size distribution, cap-fallback rate, and the wall-clock delta
on the lens the row named. `DefaultPlainDerivedAnchorCap`'s value is justified from that distribution or
changed.

---

## 12. Decomposition for the Steward

Sequenced behind `lens-projection-divergence-audit-design.md` **Fire 2** (the Auditor), which Increment 3's
licence consumes. Increments 1–2 do not depend on it and may be built first.

| Inc | Content | Independently shippable | Posture-changing |
|---|---|---|---|
| **1 + 2** *(one fire)* | `ScanRootHopIndex` + its conjunct tests + the corpus census test (completeness **and** closure); the derived-anchor re-entry path, dedupe hoist, error disposition and both caps, wired in **shadow**. Ships the corrected census numbers and the measurement. | ✅ green + green suite; no behaviour change | No — shadow decides nothing |
| **3** | The licence (§5): closure, `RowReader`, `ReferencesParam`, secure, **and threading `projection.IsAuthPlane` onto the plain pipeline** so its conjunct can fire. Tests install through activation. Still not acting | ✅ | No |
| **4a** | Flip to `act`; §6's zero-row probe; the four e2es; the measured before/after | ✅ | **Yes** — full review depth |
| **4b** | The seeded branch's multi-position gap (§4.4), two named live lenses | ✅ | **Yes** — full review depth |
| **5** | The `retained`-class repair — **fork resolved: not pre-built**; revives on §8.1's own trigger (a sustained non-zero `retained` count) | ✅ | **Yes** — full review depth |

Increments 1+2 are deliberately one fire: Increment 1 alone realizes nothing but a census (the
dead-scaffolding test), while paired with the shadow consumer it produces the measurement the rest is
sequenced on.

**Review depth is the Steward's sizing** (`agents/steward/SKILL.md` §4); the table names which increments are
posture-changing and nothing more.

**File contention to check at Phase 0.** `full/executor.go` and `full/visitor.go` are touched by the ratified
`full-engine-independent-branch-decomposition-design.md` (Inc 2 first) and by the in-flight
`relationship-data-projection-design.md`. This design touches `full/hopindex.go` and
`pipeline/anchor_derivation*.go`, which neither of those touches — but the Steward should re-derive that
against merged `main` at admit rather than trusting this sentence.

---

## 13. Risks

| Risk | Direction | Mitigation |
|---|---|---|
| The walk under-derives (stale adjacency on a multi-hop path) and a row goes stale | The reason §5 exists | 1-hop lenses (21 of ≤36) read **no** adjacency at all and cannot be exposed to it; deeper ones are licensed only where an Auditor watches; the auth plane is excluded |
| The derived set is large and K seeded evaluations cost more than one rescan | Performance only | `DefaultPlainDerivedAnchorCap` falls back; the shadow measurement sets it |
| The new `Delete` class retracts a row it should not | Correctness | Per-anchor, from that anchor's own re-execution, never from the walk naming it; §11's e2e pins both directions |
| A lens *arrives* at indexable through an unrelated cypher edit and silently narrows | Silent | The census test gates arrival on **both** completeness and closure — a completeness-only gate would miss the grouping-key edit, which is the one that mis-projects rather than merely widening |
| A derived anchor the `WHERE` filters out manufactures a delete marker | Durable target growth | §6's `RowReader.GetRow` probe, mirroring `zeroRowDeleteKey`; its own e2e |
| The Auditor's enrolment predicate is later loosened, silently widening this licence | Silent, cross-design | The licence's own auth-plane conjunct is independent of enrolment; the coupling is stated in both docs and pinned by a test naming both |

---

## 14. Pre-build gate

This design self-flags **no** deferred gate. The adversarial pass §15 records was run **during** this fire and
its findings are folded in below; the design is build-ready — the §9.1 fork was resolved at ratification
(Option A; it gated Increment 5 alone, which stays deferred behind its own trigger).

## 15. Adversarial pass

Run during this fire, over the finished draft, briefed to break it. It returned three blocking and six
material findings; all are folded into the sections they touch, and the two the author had already found
independently are marked. **The engine half survived; the licence and the retraction did not, and both are
rewritten.**

| # | Finding | Where it landed |
|---|---|---|
| **B1** | `!p.authPlane` is **inert on a plain pipeline** — `SetAuthPlane`'s only non-test caller is `InstallActorAggregate` (`driver.go:357`), so the field is `false` by construction and the guard can never fire; its test would pass vacuously. Separately, `IsAuthPlane` has **two** arms and the draft named only the Postgres/`GrantTable` one (`capabilityRoleIndex` is in the other). | §5.3 rewritten; Inc 3 threads `projection.IsAuthPlane` in; §11 requires activation-path tests |
| **B2** | `auditEnrolment` is a **read-only** predicate and cannot serve as a **write** licence: it has no per-anchor closure conjunct, so a lens grouping on a non-root variable would be truncated by a seeded evaluation. `capabilityRoleIndex` is that shape and is excluded today only by an unrelated conjunct. *(Author had found the hazard; the pass found the corpus instance and the exact fix.)* | §5.1 rewritten around `AnchorProjectionKey`'s `ok` contract |
| **B3** | §6's presence check was **unspecified at the seam** (whose props, per-anchor or union, nil disposition), and its exemplar `wellnessMemberAccounts` is refused **twice** by the predicate §6 depends on. *(Author had found the exemplar error.)* | §4.2's re-entry shape answers the mechanism; §6's exemplar withdrawn |
| **M1** | Over-derivation reaches the **Delete** path: the walk models no `WHERE`, so a filtered anchor emits a delete marker per event — the failure `zeroRowDeleteKey` exists to prevent. The draft asserted the opposite. | §6 takes the `GetRow` probe; §13 gains the row |
| **M2** | The fork **misread §8.1**: that design entertained the `retained`-only repair and deferred it with a named trigger; the draft proposed upsert-only, argued against a position §8.1 never held, and wrongly claimed it unlocks the auth plane (`retained` **is** the over-grant class). | "For Andrew" and §9.1 rewritten |
| **M3** | The unguarded upsert is **content-conditional** (`natskv.go:203-211`), so the volume framing was wrong; and on a **guarded** target a repair can be *declined* for want of a sequence — the guarded plane is harder, not easier. | §9.1 objections 1 and 4 |
| **M4** | `seedAnchorFor` gates on the anchor **label**, not position — a pre-existing under-reprojection with two live lenses. | §1.1; built as Increment 4b rather than filed |
| **M5** | Five unspecified consequences of one event becoming K evaluations (`$projectedAt`, `$now`, dedupe on the aspect/vertex arms, error disposition, accounting units). | §4.2 — four are answered by the re-entry shape, three become explicit obligations |
| **M6** | Two sizing numbers in the wrong unit: the cap counts root vertices not rows, and "15 of 36" counts lenses not link classes. | §4.2 and §6; the second is withheld and handed to the census |
| **m1–m5** | `withScopeReject`'s justification does not transfer verbatim; the builder must record the terminus before walking property expressions; `multiAnchor` becomes dead; an adapter-count editorial slip. | §4.1 and §2 |

**What the pass tried to break and could not**, independently reproduced: the census (114/60/54, 15
single-node, the 9 named variable-length lenses, the 21/12/2/1 distance profile); the `nodeMatches`
key-type-completeness correction, including the traversal admission path (`executor.go:1005-1014`) the author
had not checked; `walkToAnchors`' non-expansion soundness under a root terminus, attacked with chained
same-label patterns, self-hops and equal-distance seeds; §9.2's rejection of widening `Anchor`; §10's
lifetime claims; and every conjunct of the `clinicProviders` payoff trace.

A separate mechanical pass verified all 30+ `file:line` citations against HEAD; two were corrected.

> **Pin drift, 2026-08-13 (ratification DD):** the branch-decomposition commit `59441252` landed in
> `full/executor.go` three hours after this design was saved, shifting its `executor.go` line pins by
> ~+126 (e.g. the whole-bucket listing seam `:724-725` → `:850`; `pointCandidate` `:679-681` → `:897`).
> The mechanisms were re-verified intact at the new locations; `hopindex.go`, `anchor_derivation*.go`,
> `reproject.go`, `plan.go` and `anchor_delete.go` pins are unmoved. The build's Phase 0 re-pins
> `executor.go` citations against merged `main`, exactly as §12's contention note already instructs.
(`plan.go:82` → `:94-96`, and a comment/code off-by-one).

---

### Incs 1+2 fire brief (build note, 2026-08-13)

**1. Scope sentence (verbatim, §12).** *"`ScanRootHopIndex` + its conjunct tests + the corpus census test
(completeness **and** closure); the derived-anchor re-entry path, dedupe hoist, error disposition and both
caps, wired in **shadow**. Ships the corrected census numbers and the measurement."* Green bar: ✅ green +
green suite; no behaviour change (shadow decides nothing).

**2. Verified touch-list (re-pinned against merged `main`, `f3df3fcc`, by a Phase-0 scout).** `hopindex.go`,
`pipeline.go`, `evaluate.go`, `anchor_derivation.go`, `anchor_derivation_mode.go`, `natskv.go`, `plan.go`,
`driver.go`, `reproject.go`, `sweep.go`, `anchor_delete.go` citations are all current within a few lines of
the design's own numbers, **except `full/executor.go`, which drifted further than the ratification DD's own
+126 note captured** — the DD's two named seams (`:850`, `:903`) are exactly right, but four more citations
this fire only *reads* (never edits) have moved further: `nodeMatches` `:600-620→:725-745` (+125),
`seedAnchorBinds` `:842-844→:966-986` (+122-142), traversal admission `:1005-1014→~:1149` (+135-144),
`projectItems` grouping `:1219-1300→:1373+` (+73-154). **None of these four are edit sites for Incs 1+2** —
`executor.go` is not touched by this fire at all; they are read-only citations in §3's grounding ledger and
§5.1's corpus-shape argument, both already-settled reasoning this fire does not re-derive. Noted so the
close-pass re-pin (§12's own instruction) isn't surprised twice. `evaluate.go`'s `evaluateForEntry` empty-seed
citation resolves to the function at line 76 (body content the design's `:262-263` describes is inside it,
not a second location — confirmed, not drift). Every citation `full/hopindex.go`, `anchor_derivation*.go`,
`natskv.go`, `plan.go`, `driver.go`, `reproject.go`, `sweep.go`, `anchor_delete.go`, `cmd/refractor/reload.go`
cite lines current as written. **No file contention:** working tree clean at scout time, `59441252` (the DD's
own branch-decomposition commit) is the only recent touch to any of these five files, and no other worktree
references them.

**3. Precedents to mirror.** `ScanRootHopIndex` mirrors `AnchorHopIndex()` (`hopindex.go:188`) — same builder,
different terminus, per §4.1. Conjunct tests mirror `hopindex_test.go` + `TestAnchorHopIndex_WithScope`
(exists, that file). The corpus census test mirrors `anchor_hopindex_corpus_census_test.go`'s
`corpusAnchorIndexVerdicts` (line 80, **not** `label_derivation_corpus_census_test.go` as an early design
reference implied — confirmed the census machinery itself, `forEachCorpusCypher`, lives at
`label_derivation_corpus_census_test.go:536` and is reused, not reimplemented per the component dossier's
standing rule below). The derived-anchor entry point mirrors the three shipped `deriveAnchorsFor{Vertex,
Aspect,Link}` call sites already in `pipeline.go` and re-enters `evaluatePlainFromVertex` unchanged (§4.2).
The differential test mirrors `anchor_derivation_differential_test.go` (exists).

**4. Increment order + green checks.**
- Inc 1: `go test ./internal/refractor/ruleengine/full/... -run HopIndex -count=1` (new conjunct tests) +
  `go test ./internal/refractor/... -run CorpusCensus -count=1` (new census test, checked > N guard).
- Inc 2: `go test ./internal/refractor/pipeline/... -run Plain -count=1` (derived-set correctness, refusals,
  cap fallbacks, differential test) — shadow only, so also assert no target-bucket write changes via the
  existing e2e harness at `-shadow` mode.
- Whole-fire: `go build ./...`, `make vet`, `golangci-lint run ./...`,
  `STRICT=1 go run ./scripts/lint-conventions.go`, `go test ./internal/refractor/...`.

**5. In-scope gotchas — standing checklist + dossier entries copied in.**
- **New state needs a LIFETIME** (checklist #1): `ruleState.rootHops` — §10 already tables it
  (created/reset/carried/ordered/crash/tombstone); the builder must not diverge from that table, only
  implement it.
- **Every census is a premise** (checklist #2): §2's numbers (114/60/54, 45 exposed, 9 var-length, ≤36
  addressable, 21/12/2/1 distance) are re-derived live by Inc 1's own census test, never hand-trusted.
- **A negative test needs its positive vector proven first** (checklist #3): every `ScanRootHopIndex` refusal
  test is paired with a shape that narrows (§11 already specifies this); the differential test is
  mutation-checked (walk stubbed empty ⇒ must fail).
- **Precedent may carry debt** (checklist #6): `AnchorHopIndex()` is the mirror source — verify its
  `ground()`/`addPattern` ordering constraint (§4.1's ordering note) transfers rather than assuming it does.
- Dossier (`docs/components/refractor.md`): **"New pipeline state without a declared lifetime… reset, carry,
  order it at replay, reconnect, tombstone, retry, or the review will"** — directly this fire's `rootHops`;
  §10's table is the answer, confirm the code matches it exactly. **"An upsert-only reprojection retracts
  nothing whose key drops out"** — Inc 2 ships no writes (shadow), but the retraction-class reasoning (§6) is
  read-adjacent to this fire's `AnchorProjectionKey`-`ok` conjunct; keep the closure predicate exactly as §5.1
  derives it, not a looser stand-in. **Standing rule**: the census test reuses `forEachCorpusCypher`
  verbatim — confirmed above, not reimplemented.

**6. Adjacent finds.** None beyond the executor.go pin-drift already absorbed into part 2 above (reading-only,
no behaviour consequence — not a defect, just a citation correction folded into this note rather than the
board).

**7. Non-goals.** Inc 3 (the licence — closure/RowReader/ReferencesParam/secure/auth-plane threading), Inc 4a
(flip to `act`), Inc 4b (seeded-branch multi-position fix), Inc 5 (`retained`-class repair, deferred behind
its own trigger per the ratified fork) are **out of scope this fire** — sequenced behind
`lens-projection-divergence-audit-design.md` Fire 2 per §12. This fire changes no write behaviour: shadow
counters only.

**Close.** `ScanRootHopIndex()` (`full/hopindex.go`), its conjunct + ordering-constraint tests, the corpus
census test (33 addressable, §2 amended above), `plainDerivationIndex` + the three plain derivation entry
points, and the derived-anchor seam wired through the existing `off`/`shadow`/`act` mode switch — with
`plainDerivationIndexForAct` unconditionally declining this fire, so acting is impossible by construction,
not by relying on the operator-facing `REFRACTOR_ANCHOR_DERIVATION` knob staying at its default. Full 3-layer
adversarial review (correctness, edge-case, capability-plane — L-size per admit rules) found **zero Blocking**
findings; the mechanism held under attack (ordering constraint, terminus-generic reuse across both indices,
shadow-decides-nothing, dedupe/error-disposition hoist, `rootHops` lifetime, cap orthogonality, no write
reachable from shadow). Material findings fixed before merge: the zero-behaviour-change test now covers `act`
(the mode that actually ships) alongside `off`/`shadow`; the shadow counters split `Declined` into
`NotReady`/`WalkDeclined`/`OverCap` and record the derived-set size even on cap overflow, so §11's measurement
is answerable from the counters instead of truncated at the cap; the differential test now compares against
the **unseeded** evaluation per §11's literal requirement, not just the seeded one; the census's
`hasNeighbour` no longer mis-buckets a hypothetical untyped-hop lens as single-node; both cap fallbacks are
tested, plus an explicit zero-adjacency-read proof for the 1-hop root-side seed case; the §4.1 ordering
constraint and the unnamed-root special case each gained a trip-wire regression test. Left unfixed and
flagged in code for Increment 3: a double-Secure-decrypt hazard on the derived re-entry path, unreachable this
fire (only reachable from `act`, which always declines) and excluded going forward by §5.1's "not a Secure
Lens" licence conjunct — Increment 3 must prove that exclusion actually prevents it before flipping to act.
**Operational note for whoever collects Inc 4's measurement:** `REFRACTOR_ANCHOR_DERIVATION` is one
process-wide knob shared with the actor-aware arm — setting it to `shadow` to observe the plain arm's counters
also flips every actor-aware pipeline (auth-plane included) out of `act` back to `shadow` for the duration.
Direction is safe (wider reprojection, never narrower — cannot over-grant) but is a real, if temporary,
posture change to a component this fire does not otherwise touch; do it deliberately, not by surprise.

---

### Increment 3 fire brief (build note, 2026-08-14)

**1. Scope sentence (verbatim, §12).** *"The licence (§5): closure, `RowReader`, `ReferencesParam`, secure,
and threading `projection.IsAuthPlane` onto the plain pipeline so its conjunct can fire. Tests install through
activation. Still not acting."* Green bar: ✅ green + green suite; no behaviour change.

**2. The load-bearing invariant this brief adds (resolving an ambiguity the design doc's prose leaves open).**
`builtinDerivationMode = DerivationModeAct` (`anchor_derivation_mode.go:105`) is the process **default**. If
`plainDerivationIndexForAct` is rewritten to *return* the licence's real verdict, a licensed lens starts
**acting** the moment this ships — directly contradicting §12's own classification of Inc 3 as
`Posture-changing: No` / "Still not acting", and turning an XS/S-review increment into an unreviewed
production behaviour flip. **Therefore: `plainDerivationIndexForAct` (`anchor_derivation_plain.go:143-145`)
keeps its unconditional `return full.HopIndex{}, false` this increment, unchanged.** Inc 3 builds the licence
as an independently-testable predicate (name/shape left to the builder, e.g. a method on `*Pipeline` taking
`rs ruleState` and returning `(licensed bool, refusal string)`) that **nothing yet calls from the act path** —
Inc 4a's own scope line ("Flip to act") is precisely the one-line change that makes
`plainDerivationIndexForAct` consult it. The existing test `TestPlainDerivationIndexForAct_AlwaysDeclinesThisFire`
(`anchor_derivation_plain_internal_test.go:344`) must keep passing unmodified in spirit (rename only if its
"ThisFire" wording is now inaccurate) — its assertion (`plainDerivationIndexForAct` always declines) is the
regression trip-wire for this exact invariant; a reviewer should treat any edit to it as a signal to re-check
the reasoning above, not wave it through.

**3. Verified touch-list (re-grounded against merged `main`, `c5321ba2`, by a Phase-0 scout + direct reading).**
- `internal/refractor/pipeline/anchor_derivation_plain.go:126-145` — `plainDerivationIndexForAct`'s doc comment
  (already correctly describes Inc 3's scope) and the new licence predicate, added alongside it. **Not** the
  function's return value (§2 above).
- `internal/refractor/pipeline/audit.go:838-927` — `auditEnrolment`, the precedent for three of the five
  conjuncts. Confirmed conjuncts + line numbers: no-authPlane refusal `:850-852` (parameter, not `p.authPlane`
  — the Auditor takes `authPlane bool` as an explicit arg, sidestepping the very gap this fire closes for the
  *write* licence); `RowReader` type-assert `:894-896`; secure-decryptor `:902-904`; `ReferencesParam` loop
  over `"now"`/`"projectedAt"`, both exhaustive+referenced checked `:916-923`. **Reuse this shape directly**
  (same package, same `rs`/`adpt` types) rather than re-deriving it independently — a hand-rolled second
  version of the same three checks is exactly the kind of drift the design's "already exists, already tested"
  framing means to avoid.
- `internal/refractor/pipeline/audit.go:205-301` — `Auditor` struct + `AuditStatus` (`:128` — read it, don't
  re-derive: `Enrolled bool` is a **field** on `AuditStatus`, reached via `p.Auditor().Status().Enrolled`,
  **not** an `Enrolled()` method — the design doc's `auditor.Enrolled()` shorthand does not exist verbatim in
  code and must not be typed as a method call). `p.Auditor()` returns `nil` when `InstallAudit` never ran —
  guard the nil case (`p.Auditor() != nil && p.Auditor().Status().Enrolled`).
- `internal/refractor/ruleengine/full/anchor_delete.go:61-181` — `AnchorProjectionKey`, the closure conjunct's
  source. **Open engineering decision, resolved here:** `AnchorProjectionKey` requires a concrete
  `eventKey`/`eventType`/`eventProps` (it evaluates key-column expressions against a live binding), so it
  cannot be called as-is from a `rs`-only, no-event context like the licence predicate. Its **first**
  structural half — no `WITH` clause (`:75-79`), anchor found + label match (`:95-109`), and every key
  column's expression passing `exprReferencesOnlyVariable(expr, anchorVar)` (`:145-152`, itself defined
  `:189-256`) — is decidable from the **compiled rule alone**, with no event data: `exprReferencesOnlyVariable`
  never inspects a value, only an expression's variable references. The **second** half (`:154-171`:
  `ex.evalExpr`, nil/isNode checks) is inherently per-event and is exactly what `AnchorDeleteResult`'s existing
  per-anchor call already guards defensively (falls through to a re-execute) — that half is Inc 4a/§6's
  concern (the zero-row probe sits beside it), not Inc 3's. **Build a structural-only helper** — e.g.
  `(cr *CompiledRule) hasAnchorOnlyKeyColumns() bool` in `full/anchor_delete.go`, factored so
  `AnchorProjectionKey` calls it internally for the no-WITH + anchor-only-key-column checks rather than
  duplicating them — and have the licence predicate call the structural helper, never `AnchorProjectionKey`
  itself. This is a genuine, if small, engine-side code change (not zero-touch reuse); flag it prominently in
  the PR description since "the conjunct already exists" (§5.1) undersells it slightly — the *checks* already
  exist, the *entry point that doesn't need an event* does not yet.
- `internal/refractor/ruleengine/full/params.go:32` — `(cr *CompiledRule) ReferencesParam(name string)
  (referenced, exhaustive bool)`. Confirmed signature; reuse verbatim, same non-exhaustive-is-a-refusal
  disposition as `auditEnrolment` (`:916-923`).
- `internal/refractor/projection/plan.go:110-115` — `IsAuthPlane(r *lens.Rule) bool`, both arms (`nats_kv` +
  `AuthPlaneBucket`; `postgres` + `GrantTable`). No change needed here; cited for the licence conjunct and the
  test in §5 below.
- `internal/refractor/pipeline/pipeline.go:367,2076-2078` — `authPlane bool` field + `SetAuthPlane(v bool)`.
  No change; the write site is added at the call site below.
- `cmd/refractor/main.go` — the `startPipeline` closure (begins `:1307`), the **single** activation path used
  both at boot and at hot-reload (confirmed: `reload.go` has no separate pipeline-construction code path of
  its own — `newPipelineEntry` at `reload.go:238` is a post-hoc metadata wrapper built from the already-active
  `p`, not a second constructor). **Add exactly one line**, `p.SetAuthPlane(projection.IsAuthPlane(r))`,
  placed after the `switch` block that installs actor-aggregate/personal-lens components (ends `:1659`) and
  before the `InstallAudit` call (`:1700`) — i.e. right beside the comment block at `:1694-1699` that already
  names this exact gap ("`Pipeline.authPlane` is set only by `InstallActorAggregate`..."). **Verified safe for
  the actor-aggregate case:** `InstallActorAggregate` already calls `p.SetAuthPlane(plan.AuthPlane)`
  (`driver.go:357`) where `plan.AuthPlane = IsAuthPlane(r)` (`plan.go` `Compile`, confirmed) — an unconditional
  second call with the identical value is a no-op re-assertion, not a behaviour change. **Verified inert for
  every existing reader:** `p.authPlane` has exactly one runtime reader today, `evaluate.go:512`
  (`(p.envelopeFn != nil || p.multiEnvelopeFn != nil) && p.authPlane && p.requiresFootprintValidation`) — its
  first conjunct is `false` for every plain lens (`envelopeFn`/`multiEnvelopeFn` are nil), so setting
  `p.authPlane` on a plain pipeline cannot change this function's answer. `ConsumerFilter`'s ordering
  constraint (`:1721-1734`, "must stay after every stage that installs a conjunct of the actor-aware
  eligibility predicate") does **not** name `authPlane` among its conjuncts — confirm this holds at build time
  by grepping `ConsumerFilter`'s own source for `authPlane` before finalizing placement, but nothing found in
  this scout's pass.
- **No file contention.** Working tree clean at `c5321ba2`; no other worktree touches
  `anchor_derivation_plain.go`, `audit.go`, `anchor_delete.go`, `pipeline.go`, or `main.go`'s `startPipeline`.

**4. Precedents to mirror.** `auditEnrolment` (§3 above) for the three carried-over conjuncts' exact shape and
disposition (fail-closed, reason string, non-exhaustive-is-a-refusal). `InstallActorAggregate`'s
`p.SetAuthPlane(plan.AuthPlane)` call (`driver.go:357`) for the new call site's shape. The existing
`TestAudit_*` suite (`audit_test.go`) for activation-installed test structure — Inc 3's auth-plane tests must
install through the **real** `startPipeline`/`InstallAudit`-equivalent activation path per §11, not a hand-set
`p.authPlane = true`, exactly as §5.3 and the design's adversarial-pass finding B1 require.

**5. Increment order + green checks.**
- Licence predicate + its five conjunct unit tests (closure structural helper, enrolled-Auditor, two
  `ReferencesParam` params, no-secure, not-auth-plane) — mirror `TestPlainDerivationIndex_Conjuncts`'s table
  shape (`anchor_derivation_plain_internal_test.go:266`). `go test ./internal/refractor/pipeline/... -run
  License -count=1` (or whatever name the builder gives the test group — state it in the commit).
- `hasAnchorOnlyKeyColumns` (or chosen name) unit tests in `full/anchor_delete_test.go`, mirroring the existing
  `AnchorProjectionKey` test table's shapes for the no-WITH and anchor-only-key-column cases, asserting the new
  helper and the now-refactored `AnchorProjectionKey` agree on every existing case (regression pin — mutation-
  test by construction since `AnchorProjectionKey`'s existing suite must stay green unmodified).
- Auth-plane threading: one activation-path test per `IsAuthPlane` arm (nats_kv capability bucket; postgres +
  GrantTable), asserting `p.authPlane` reads `true` after real activation — the tautology-vs-real-guard
  distinction §5.3/B1 calls out.
- Whole-fire: `go build ./...`, `make vet`, `golangci-lint run ./...`,
  `STRICT=1 go run ./scripts/lint-conventions.go`, `go test ./internal/refractor/...`.
- **Zero-behaviour-change regression:** `TestPlainDerivationIndexForAct_AlwaysDeclinesThisFire` (or its rename)
  and `TestEvaluatePlainNeighbourEvent_NoModeChangesTheResultThisFire`
  (`anchor_derivation_plain_internal_test.go:595`) both still pass unmodified in substance — §2's invariant,
  proven in code, not just argued.

**6. In-scope gotchas — standing checklist + dossier entries copied in.**
- **New state needs a LIFETIME** (checklist #1): this increment adds no new `ruleState`/`Pipeline` field beyond
  the licence predicate itself (a pure function of already-lifetimed state) — confirm the builder didn't
  introduce one; if it did, table it per §10's pattern before merging.
- **A soundness claim must cite the code that enforces it** (feedback memory): the closure conjunct's
  structural-vs-per-event split (§3 above) is exactly this class of claim — the PR/commit must cite
  `exprReferencesOnlyVariable` and the line range factored out, not just assert "closure is checked."
  **Read the MATCHER, not the AST**: the closure helper must literally walk the same `Expr` types
  `exprReferencesOnlyVariable` does; a re-derivation that "looks equivalent" but misses a case (e.g. a new
  `Expr` subtype added later) silently diverges from `AnchorProjectionKey`'s own behaviour — hence the "factor,
  don't duplicate" instruction in §3.
- **Precedent may carry debt** (checklist #6): `auditEnrolment`'s conjunct order is deliberate (cheap/no-alloc
  checks — `authArmed`, `authPlane` param, engine-kind — before the `ReferencesParam` walk); mirror the
  ordering, don't just mirror the individual checks in an arbitrary order.
- Dossier (`docs/components/refractor.md`): carry forward whatever entries the Incs 1+2 build note already
  copied (§13 risks table above is the authoritative current risk list for this design; re-read it before
  writing the licence predicate, particularly the auth-plane and closure rows).

**7. Adjacent finds.** None expected beyond the `hasAnchorOnlyKeyColumns` extraction itself, which is in-scope
per §3's reasoning (needed to build the licence at all, not an unrelated discovery) — if the builder finds the
extraction is materially riskier than sketched here (e.g. `exprReferencesOnlyVariable`'s recursion depends on
executor state this fire didn't expect), stop and reflag rather than forcing a shape that doesn't fit; this
brief's §3 is this fire's best-grounded guess, not gospel.

**8. Non-goals.** Inc 4a (flip to act, §6's zero-row probe, the four e2es, the measured before/after), Inc 4b
(seeded-branch multi-position fix), Inc 5 (`retained`-class repair) are **out of scope**. `plainDerivationIndexForAct`
returning anything other than `full.HopIndex{}, false` is out of scope — see §2's invariant.

**9. Close.** Built: `plainDerivationLicence` (the five-conjunct predicate — auth-plane, enrolled-and-
unsuppressed Auditor, full-engine rule, `RowReader`, no secure decryptor, `ReferencesParam`, and
`ProjectsOneRowPerAnchor` — see §5.1's build correction above), `p.SetAuthPlane` threaded onto every plain
lens at activation (`cmd/refractor/main.go`'s `installLensPlane`), and a `hotReloadRefusal` pin closing the
one live gap review found adjacent to this fire (an unguarded lens's INTO-only bucket edit could move it onto
or off the auth plane with none of `pipeline.authPlane`/`pipelineEntry.authPlane`/`Auditor.authPlane` re-
deriving — the last two are **live today**, independent of this design; fixed at the root as a refusal, not a
re-derivation, to avoid a data race on the unsynchronized `authPlane` field). Three cold adversarial passes
(correctness, edge-case, capability/security) plus one fix round plus one verification pass found zero
defects reaching production: `plainDerivationIndexForAct` still declines unconditionally and nothing calls
the new licence from any write path (`TestPlainDerivationLicence_DoesNotReachTheActGate` pins this).

**Two explicit preconditions for Inc 4a**, surfaced by the verification pass and not fixed here because
nothing consults the licence yet:
- **The enrolled-Auditor conjunct has no staleness clock.** It reads `Status().Enrolled` and
  `Status().Suppression`, both correctly live-not-latched, but not `LastPassAt`/`Interval` — so a lens whose
  audit will never successfully pass (killed post-activation, wedged mid-pass with no per-pass deadline) reads
  licensed for up to the pre-first-pass window and indefinitely once wedged. The heartbeat already derives
  `LensAuditStalled` off this same clock (`health/lattice_heartbeater.go`); Inc 4a's licence must consult it
  too before the licence can gate a write.
- **The activation-time `p.SetAuthPlane` call is proven reachable but not regression-pinned against
  `startPipeline` itself** (`cmd/refractor/main_test.go`'s `activateLens` re-derives the production sequence
  rather than calling `startPipeline`, which is not unit-testable as written) — deleting `installLensPlane`'s
  call site from `main.go` today breaks no test. Inc 4a's own e2e work should add the missing trip-wire, or
  this exclusion could silently regress to B1's original tautology.

---

### Increment 4a Part 1 (build note, 2026-08-14) — the staleness conjunct, and a decision on the second precondition

**Shipped, `4088d19a`.** `Auditor.Stale(now time.Time) (stale bool, elapsed time.Duration)`
(`internal/refractor/pipeline/audit.go`) reads `AuditStatus.LastPassAt` against `Interval()`, scaled by a new
`auditorStaleCycles = 10` (own constant, not shared with the heartbeat's `defaultCapabilitySweepStallCycles` —
same default value for operator-legible parity, deliberately independent because the two mechanisms' fail
directions differ: the heartbeat must not alarm on a fresh install, the licence must not license one). Wired
into `plainDerivationLicence` as a new conjunct in the auditor-health cluster, right after
`Enrolled`/`Suppression`. `plainDerivationIndexForAct` is untouched — still declines unconditionally; no write
path reaches the licence yet. Zero behaviour change, full test coverage (`TestAuditorStale`,
`TestPlainDerivationLicence_StaleAuditRefuses`, `TestPlainDerivationLicence_NeverAuditedRefuses`).

**The second precondition — decided, not fixed: accepted as a residual, not deferred.** `startPipeline`
(`cmd/refractor/main.go`) is a ~555-line closure capturing dozens of enclosing variables (confirmed by brace-
depth trace at build time) — exactly what its own neighbouring comment already says ("captures a whole booted
process... not callable from a test as written"). Extracting it into an independently testable, explicitly-
parameterized function is the correct fix in principle (Go's compiler makes the mechanical part safe — a
missed capture fails to build, not silently), but it is a large, blast-radius-wide refactor of Refractor's
single most critical activation path, done unattended, in a fire whose actual mandate is the act-flip and the
zero-row probe. That mismatch is disproportionate, so it was not attempted. The original precondition's own
wording ("Inc 4a's own e2e work should add the missing trip-wire, **or** this exclusion could silently
regress") already offered this as an accepted trade-off, not a mandate — this note exercises that "or"
explicitly rather than silently. Residual, named rather than hidden: a deletion of `installLensPlane(p, r)`
from inside `startPipeline` (`main.go:1690`) would not be caught by any test today (`TestInstallLensPlane` and
`TestActivationRecordsTheLensPlane`/`activateLens` both call `installLensPlane` directly, not through
`startPipeline`). Whoever next has cause to touch `startPipeline` for an unrelated reason should consider
extracting it then, when the blast radius is already open rather than freshly incurred.

### Increment 4a Part 2 — checkpoint (not started; hard-stopped before any edit)

**Why stopped here.** Four consecutive attempts to spawn the Part 2 builder failed on `API Error: 529
Overloaded` before making a single edit (worktree confirmed clean each time) — a platform-level infra
condition, not a task problem. Per the unattended-fire protocol this is a hard stop, not a pacing choice: no
code exists to review or commit, so there is nothing to leave half-built. The design below is fully resolved
and grounded against merged `main` at `4088d19a` — the next fire should build straight from it with a fresh
delta-scout to re-verify line numbers, not re-derive the design.

**Scope (design §12 Inc 4a, in full — Part 1 above already closed the two preconditions):** flip
`plainDerivationIndexForAct` to consult the licence; §6's zero-row `RowReader.GetRow` presence probe on the
derived re-entry path's Deletes; the four e2e tests (§11); the measured before/after (§11, via the live dev
stack's shadow counters — collect this AFTER Part 2 lands, comparing `derivShadow` stats pre/post-flip on
`bin/refractor`, cycled from `main`).

**Part 2a — the flip.**
```go
func (p *Pipeline) plainDerivationIndexForAct(rs ruleState) (full.HopIndex, bool) {
	idx, ready := p.plainDerivationIndex(rs)
	if !ready {
		return full.HopIndex{}, false
	}
	if licensed, _ := p.plainDerivationLicence(rs); !licensed {
		return full.HopIndex{}, false
	}
	return idx, true
}
```
Rewrite (don't delete) `TestPlainDerivationIndexForAct_AlwaysDeclinesThisFire` and
`TestPlainDerivationLicence_DoesNotReachTheActGate` (`anchor_derivation_plain_licence_internal_test.go`) — both
currently pin the OLD unconditional-decline invariant, which this flip makes false by design; give them real
positive/negative cases instead of deleting the coverage.

**`noteStaticPlainDerivationRefusal`** currently logs a hardcoded string that is wrong once licensed lenses
exist. Fix it to report the real reason: `plainDerivationIndex`'s own conjuncts first (mirror
`noteStaticDerivationRefusal`'s per-conjunct switch, `anchor_derivation_mode.go:181-205`, for the plain arm's
equivalents — plain pipeline shape / single branch / `rootHops` complete+resolved / no `diffRetraction`), else
`plainDerivationLicence`'s own returned refusal string directly (no need to re-derive a switch — it already
gives you the string). **Resolved design nuance, implement as stated, do not re-litigate:** keep the licence's
refusal in the SAME "static, dedup-by-reason-string, uncounted via `recordDerivationFellBack`" bucket
`plainDerivationIndex`'s refusal already uses, even though the licence's auditor-health conjuncts are genuinely
live per-event facts (unlike the actor-aware arm's install-time-fixed conjuncts) — counting every event during
an hours-long audit-stale window would drown `recordDerivationFellBack`'s signal (real per-event walk
failures/cap-overflows) with a repeated lens-level fact, the same "drown the ratio" problem
`noteStaticDerivationRefusal`'s own doc comment names. The existing dedup-by-changed-string mechanism already
re-logs when the reason changes, so this loses no operator visibility.

**Part 2b — the §6 probe.** `evaluatePlainDerivedAnchors` re-enters `evaluatePlainFromVertex` once per derived
anchor, which reaches `evaluateForEntryRaw`'s existing `AnchorProjectionKey`-based retraction check
(`internal/refractor/pipeline/evaluate.go`, the `resultsContainKeys` block) — correct and unchanged for a
genuine top-level anchor event, but a hazard for a WALK-derived anchor: `walkToAnchors` models no `WHERE`, so it
can derive an anchor that never had a live row, and the existing check would emit an unconditional `Delete` for
it — a spurious durable tombstone under soft-delete. Guard: in `evaluatePlainDerivedAnchors`, after each
`evaluatePlainFromVertex` call returns, before merging a `Delete: true` result into `combined`, probe
`adapter.RowReader.GetRow` (from `p.currentAdapter()`, guaranteed present by the licence's own `RowReader`
conjunct) for the Delete's key(s); drop the Delete silently (not an error) if the row is absent, keep it if
present. Mirror `zeroRowDeleteKey`'s pattern (`evaluate.go`, same file) without calling it directly (it is
`$actorKey`-scoped, not reusable here) — read its exact `GetRow` call shape and its own error disposition, and
match both; verify `EvalResult.Keys`' shape against `adapter.RowReader.GetRow`'s real signature before writing
this (the closure conjunct's `ProjectsOneRowPerAnchor` should mean exactly one key column always, but confirm
against the real types, don't assume). Add a regression test proving a genuine top-level anchor event's
existing Delete behaviour is unchanged by this probe — the single most important correctness pin in Part 2,
since misplacing the probe in `evaluate.go`'s general path would silently change retraction behaviour for
every plain lens, not just licensed ones.

**Part 2c — the four e2e tests (design §11 "e2e (Inc 4)"):** (a) `clinicProviders`-shaped narrowing — an
`identifiedBy` link event reprojects only the affected provider, asserted by a bystander provider's row
revision NOT moving; (b) §6 retraction — a non-anchor-incident link removal on a closure-conjunct lens now
retracts the anchor's row (new behaviour, today's code cannot); (c) §6 probe — a `WHERE`-filtered anchor the
walk derives but which never projected a row produces no delete marker; (d) licence gate — an un-enrolled lens
(no Auditor) still gets the whole-corpus rescan even with `act` mode on. Each needs a positive precedent pair
per house convention (a refusal test is worthless if the harness never reached the code). Find the closest
existing `*_e2e_test.go` in `internal/refractor/pipeline/` for harness precedent before writing a new file.

**Review depth for Part 2 (per §12/admit rules): full 3-layer adversarial** — this is the posture-changing
increment (a real write-path behaviour flip on live production data, even though the licence excludes the auth
plane). Part 1 above, being zero-behaviour-change, took a lead review only; a cumulative close-pass adversarial
review should also cover Part 1 + Part 2's combined diff before the item's Done-log entry, per the multi-fire
review-cadence rule.
