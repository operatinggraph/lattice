# Shared projection keyspaces — eliminate the N-writers shape: multi-walk lenses + engine UNION + the disjoint-key guard (design)

**Status: ✅ Andrew-ratified (2026-07-27) — shape redirected at ratification.** Andrew's two
questions falsified the first draft's frame: (1) the UNION "XL" sizing was reflex, not derivation
— the vendored grammar **already parses UNION**; and (2) *no case in the census requires two
lenses writing the same target and key* — every pair is an artifact of removable causes. The
ratified shape therefore **removes the causes and enforces the policy** (same target ⇒ disjoint
keys, **no declared-overlap escape hatch**). The first draft's per-source-body merge (old Fire A1)
and `sharesKeyspaceWith` declaration are **WITHDRAWN** — they were arbitration built *for* the
workaround. Body rewritten to the surviving shape below (banner-rewrites-body rule); the
first-draft adversarial pass **✅ RUN** — its surviving findings shaped §3.3/§3.4 (§9 records the
disposition of all findings, including the withdrawn ones).
Author: Winston (Designer fires, 2026-07-27 · 2026-07-29) · Lattice lane (Stream 2, [Refractor] /
[Pkgmgr]). Backlog row: *"Two lenses sharing one IntoKey race per column"*.

**§13 — the composition re-decision — ✅ Andrew-ratified (2026-07-29).** U1's *compilation* was
wrong three increments running (§10–§12); §13 replaces it (one query per walk, merged by output
key) and rewrites §3.1's compilation bullet and §2.3's catalog/tasks factoring in place. The
ratified shape, the census verdict, Fires U2/G/A3 and the no-escape-hatch policy are **unchanged**.
**Build §13.7's order, not §3.1's compilation bullet** — §13.9 carries the worked cypher for both
shapes.

> **Ratified shape (Andrew, 2026-07-27), two lines.** Same-target-same-key multi-writer is not a
> pattern to arbitrate — it is a shape to **eliminate**: (U1) pkgmgr lenses gain **multiple
> walks** (the one-walk-one-grant-domain coupling is what forced every multi-path entitlement
> into N lenses), and the three edge-manifest pairs unify into single lenses; (U2) the engine
> gains **UNION** (grammar already parsed; visitor/executor work, branch-capped, fail-closed) for
> genuine row-set unions — first consumer `one-bill`; (G) the keyspace guard then enforces
> **disjoint keys per target with zero sanctioned exceptions**. Cross-target composition stays
> the sanctioned union-at-the-reader shape (client join / SQL join). The 18-tail audit (A3a) and
> the runtime collision-guard repair (A3b) survive from the first draft unchanged.

---

## 1. Problem + demand

`edgeCatalog` (service path) and `edgeCatalogRoles` (role path) both project
`manifest.op.<opId>` per actor — identical IntoKey, same `ns`, `entityId = nanoIdFromKey(op.key)`
(`packages/edge-manifest/lenses.go:98-117`, `:271-289`). Every write is a whole-body replace
under LWW-by-revision (`adapter/natssubject.go:237-263`; `internal/edge/store/bolt.go:145-148`).
The roles tail RETURNs the service tail's 24 columns **plus** `viaRole`/`viaRoleName` — whichever
lens re-derives last wins the body; `cmd/facet/web/app.js:775` groups by `viaRoleName`, so a
multi-hat actor's grouping collapses whenever `edgeCatalog` lands last. The 0.14.3/0.14.4
mitigations equalized `viaServices` only.

**The census** — three same-key pairs, all edge-manifest, all on the nats-subject transport:
catalog (divergent columns — the filed defect), tasks (`edgeTasks`/`edgeTasksQueued`, divergent
`assignee` vs `queuedRole` — latent: the claim affordance is `!t.data.assignee`,
`app.js:1084-1088`, and row-disjointness rests on a data invariant holding across two independent
CDC re-executions), sessions (`edgeEntitySessions`/`edgeInstructorSessions`, byte-identical
RETURNs — safe only by hand-maintained textual coincidence).

**The reframe that ratification forced:** the first draft asked *"what exists in this shape?"*
and, finding the overlap widespread and R2-attributed, designed arbitration for it. Andrew's
question was *"what **requires** this shape?"* — and the answer is **nothing** (§2.3). The demand
is therefore not a merge rule; it is the removal of two platform limitations plus an enforced
policy.

Two structural facts stand regardless of the reframe (first-draft adversarial pass, verified):

- **0.14.4 introduced an intra-lens collision**: the roles tail's aggregating
  `WITH op, role, collect(…)` groups by `(op, role)` (`executor.go:766-790`) — an actor holding
  two roles granting one op emits two rows with one IntoKey.
- **The runtime output-key collision guard is a structural no-op for all 18 nats-subject
  lenses** (`detectOutputKeyCollision` keys on `Keys["key"]`, `pipeline/evaluate.go:322`; a
  personal lens's keys are `{__actor, ns, entityId}`) — and fixing it naively would
  terminal-fail **six** live lenses whose tails drop path variables through a non-DISTINCT
  `WITH` over OPTIONAL-MATCH cross-products (`edgeCatalog`, `edgeStaffPanes`,
  `edgeEntityProviders`, `edgeEntityStudios`, `edgeStaffWorkOrders`, `edgeEntitySessions`).
  The audit precedes the guard fix (§3.4).

## 2. Grounding — the two removable causes, and why nothing requires the overlap

### 2.1 Cause 1: one walk, one grant domain, per lens

`LensSpec.Walk *AnchorWalk` (`internal/pkgmgr/definition.go:770-780`) declares a Personal lens's
actor→anchor reachability **once**, and each walk names exactly one `GrantDomain`
(`anchorwalk.go:29`; validated against the package's `ReadGrantDomains`, `anchorwalk.go:231`).
A multi-path entitlement — the same op reachable via residence (`domainBase`) *and* via held
role (`domainStaff`) — therefore **cannot be one lens today**, and every such case became a
sibling lens writing the same keys to keep one reader-visible document. That coupling, not the
engine and not authoring taste, created the census.

The per-domain grant *producers* are already decoupled from projection-lens count:
`ExpandReadGrantWalks` generates "exactly one actorAggregate producer lens per domain … compiled
from the walks it grants" (`definition.go:190-196`) — so a lens carrying N walks across N
domains changes nothing about producer generation.

### 2.2 Cause 2: UNION is rejected at the visitor, not missing from the grammar

The vendored `Cypher.g4` is full openCypher: the generated lexer/parser already carry
`CypherLexerUNION = 46` and `OC_UnionContext`/`EnterOC_Union`
(`ruleengine/full/cypher/cypher_lexer.go:898`, `cypher_base_listener.go:47`). The visitor simply
refuses the parsed node (`visitor.go:71-73`, `AllOC_Union()` → fail). The first draft's "XL"
sizing was asserted, not derived — **the grammar work is zero**, and the remaining work is
bounded (§3.2). Meanwhile the construct authors use *instead* — extra `OPTIONAL MATCH`es in one
query — **cross-products** (the inflation `executor.go:930-935` warns about), while UNION is
**additive** (sum of branch costs). The expressible workaround is the expensive one; the missing
feature is the cheap one.

`one-bill/lenses.go:12-25` is the standing demand: two lenses into one bucket, self-described as
a UNION workaround, disjoint only by vertex-type prefixes in the key values.

### 2.3 Nothing requires same-target-same-key (the census, decomposed)

| Pair | What it actually is | The requirement-free factoring |
|---|---|---|
| Sessions | Byte-identical bodies; only reachability/grant domain differs | **One projection lens, two walks** (U1). The split was pure vocabulary artifact |
| Catalog | An enrichment: one row per op, columns from two paths | **One lens**, ~~both paths as OPTIONAL MATCH + `collect(DISTINCT …)` aggregation~~ → **one query per walk, rows merged by output key** (corrected by §13.4: aggregation absorbs a cross-product's duplicates, it does not turn a join into a union). U1 supplies the two grant domains |
| Tasks | State-disjoint rows (`assignedTo` xor `queuedFor`) | **One lens, two walks** (~~two chains in one query~~ — same §13.4 correction), branch-specific nullable columns (`assignee` null when queued, `queuedRole` null when assigned) resolved as walk-owned at merge; one evaluation observes one state (per-key memo + the ratified evaluation-consistency design's edge memo) |

Cross-target composition (Postgres SQL join; Edge client joining two key namespaces) remains the
sanctioned reader-side union — Andrew's model, unchanged. **Within one target, disjoint keys,
no exceptions.** R2's shipped `Sources` attribution stays: it serves *retraction* correctness
(and carries the migration — a retired sibling lens's attributions prune at the next hydrate via
the R2 dead-lens lens-set); it is not extended with body slices.

### 2.4 What a build-time guard can see (first-draft pass, stands)

`OutputKeyPattern`, `GrantSource`, `Bucket`, `SubjectPrefix`+`Stream`, `IntoKey`, and
`AnchorWalk.AnchorType` are struct fields. `one-bill`'s keys and `capabilityRoleIndex`'s
`cap.role-by-operation.` prefix live only in cypher/envelope code — a guard comparable must not
require a cypher extractor (the first draft's did, and would have made both uninstallable).
`validateAll` (`definition.go:35-49`) runs nine checks, none comparing key spaces; the only
cross-package check is canonicalName collision (`installer.go:618-640`); nothing checks
`weaver-targets` targetId uniqueness cross-package; nothing rejects duplicate `GrantSource`
(a struct field — a five-line check).

## 3. The shape (build order: U1 (+A3a) → G → A3b; U2 independent, any time)

### 3.1 Fire U1 — multi-walk lenses + the pair unification (pkgmgr + edge-manifest)

`LensSpec.Walk *AnchorWalk` → **`Walks []AnchorWalk`** (singular field kept as sugar or
migrated mechanically — the fire's call). Semantics:

- Every walk names its own `GrantDomain` (each validated against the package's
  `ReadGrantDomains`, as today) and its own chains; **all walks in one lens must share the
  lens's anchor type/var** — a lens still projects one entity kind (Contract #6's one-RETURN
  -shape policy), fail-closed at expansion.
- **Compilation — SUPERSEDED by §13.2. Do not build the text struck below.**
  ~~The compiled prelude is the actor head + **every** chain as `OPTIONAL MATCH`; the anchor is
  reachable via *any* walk.~~ False: the engine composes clauses as a **nested-loop join**, so a
  second walk can only filter the first walk's anchors (shared var) or cross-product with them
  (renamed + `coalesce`) — either way an anchor reachable *only* via walk 2 is dropped. Proved by
  increments 1–3 (§10, §11, §12); the analysis is §13.1. **Each walk compiles as its own query
  with the lens's shared tail, and Refractor merges the branches' rows by output key** (§13.2).
  The tail still aggregates per path with `collect(DISTINCT …)` — the discipline `1b9852f2` made
  sound and the A3a audit enforces corpus-wide.
- Per-domain grant producers: unchanged (already per-domain, §2.1).

**Unification (same fire, edge-manifest release):** catalog pair → one `edgeCatalog` (walks:
residence/`domainBase` + heldRoles/`domainStaff`; columns: the 24 + `viaRoles` as a
`collect(DISTINCT {key, name})` **map-literal** collect — two parallel lists would misalign when
a role lacks a name, since `collect` drops nulls, `executor.go:945-955`; in-package precedent
`lenses.go:544`; `app.js:769-784` consumes the list). Tasks pair → one `edgeTasks` (two chains;
`assignee`/`queuedRole` nullable). Sessions pair → one `edgeEntitySessions` (two walks,
identical body). The retired siblings' mirror attributions prune at hydrate (R2 lens-set); rows
re-assert under the surviving lens's ruleID with no store schema change.

### 3.2 Fire U2 — engine UNION (plain lenses; branch-capped, fail-closed)

The visitor accepts `OC_Union`; a `CompiledRule` carries N branch queries.

- **Execution:** branches evaluate independently through the existing single-query machinery;
  row sets concatenate; `UNION` (vs `UNION ALL`) dedupes by canonical-JSON row equality (the
  presence-check's existing comparator). Cost is additive by construction.
- **Fail-closed activation constraints:** identical RETURN alias lists across branches (the
  standard UNION column contract — also what keeps `KeyColumns` uniform); branch count ≤ 4;
  every branch independently passes every existing validation (key-column resolution,
  convergence no-filtering-WHERE, DiffRetraction unanchoredness — per branch).
- **Reactivity/retraction surface, per branch:** `ReferencedLabels` = union of branches
  (type-relevance skip stays conservative); anchor-tombstone and filter-retraction read-free key
  derivation select the branch whose anchor label matches the event vertex — label ambiguity
  falls through to a re-execute, the existing safe fallback.
- **Scope v1:** plain (non-Personal) lenses. A Personal tail carrying UNION is refused with
  "declare `Walks` instead" — U1 is the personal-transport shape; UNION serves row-set unions.
- **First consumer:** `one-bill` collapses to one lens (its own doc comment is the filed
  demand); the pattern unblocks every future heterogeneous-anchor read model that today would
  become sibling lenses.

### 3.3 Fire G — the disjoint-key guard (no escape hatch)

Andrew's policy, enforced: **same container ⇒ disjoint key patterns.** Comparable = the
struct-derivable v1 set (§2.4): container (`Bucket` / table+`GrantSource` /
`Stream`+`SubjectPrefix`) × pattern (`OutputKeyPattern` | `AnchorWalk.AnchorType`+`IntoKey`).

- **Equal comparables → refuse. No declaration vocabulary** — with U1/U2 shipped there is no
  legitimate overlap left to declare, so the escape hatch the first draft designed
  (`sharesKeyspaceWith`) is withdrawn; an escape hatch would preserve the shape U1/U2 exist to
  remove.
- Disjoint `OutputKeyPattern`/`<targetId>.` literals pass — now checked **cross-package**
  (closing Contract #10 §10.2's unchecked "disjoint" convention); AnchorType-disjoint
  IntoKey-shape twins pass (resting, explicitly, on the platform's standing NanoID-uniqueness
  assumption — the same one Contract #1 addressing rests on). Duplicate `GrantSource` across
  producers → refuse.
- **Placement:** intra-package in `validateAll` (`validateLensKeySpaces`); cross-package beside
  `checkCanonicalNameCollision` in the installer; activation backstop in Refractor (the
  `reloadpin` pattern — installer predicts, Refractor is authority; the later-arriving spec
  fails closed naming both lenses, the incumbent keeps serving).
- **Spec-only lenses** (no IntoKey, no walk): `one-bill` exits the class via U2; the named
  residual is `capabilityRoleIndex` (prefix lives in envelope code; kernel-adjacent and
  stable) — out of v1 scope by name, not silently.
  - **The precondition that escape rests on (Andrew, 2026-07-29): the branches must be
    co-owned by ONE package.** A Lens spec belongs to exactly one package and no mechanism
    lets two packages contribute branches to one spec — so U2's reach is bounded by
    construction to what a single package already authors. `one-bill` qualifies: it is a
    dedicated composition package owning no vertex types, links or permissions, declaring
    both ledgers as dependencies and matching both their class labels in its own cypher
    (`packages/one-bill/package.go:1-20`); the ledgers write their own separate buckets
    (`loftspace-ledger-history`, `cafe-ledger-history`) and neither writes this one. **The
    unresolved shape is a spec-only overlap whose branches are genuinely cross-package:**
    the guard cannot see its keys (they live in cypher — §2.4 forbids a cypher extractor
    comparable) and U2 cannot merge it (no cross-package spec). None exists today; if one
    is ever proposed, the answer is a composition package (the `one-bill` shape) or
    struct-visible disjoint keys — not a guard exception.
- **Sequencing — migration then gate, no warn-first:** U1's unification empties the collision
  census; the gate lands blocking against a clean tree.

### 3.4 Fire A3 — the tails audit (A3a), then the runtime guard repair (A3b) — unchanged

- **A3a:** audit all 18 nats-subject tails for multi-row-per-key emissions; fix each with
  `WITH DISTINCT`/aggregation (the six known offenders in §1; the catalog fix arrives via U1's
  unification). Rides the U1 edge-manifest release.
- **A3b (after A3a):** `detectOutputKeyCollision` builds the composite key from the adapter's
  `keyOrder` instead of `Keys["key"]` — the intra-lens duplicate-output guard starts firing for
  nats-subject lenses, against a clean corpus. Post-U1 it is also the backstop that keeps the
  unified lenses honest (one key, one row).

## 4. Contract surface

None frozen. `Walks` is `LensSpec`-level (pkgmgr component surface); UNION is engine-level and
**consistent with** Contract #6's one-RETURN-shape policy (identical aliases across branches =
one shape; the policy's "multi-output patterns are additional Lenses" continues to govern
*different-shape* outputs); #10 §10.2's "disjoint `<targetId>.` prefix" becomes checked. If
ratification archaeology ever wants the disjoint-key rule in contract text, the natural home is
one line in #10 §10.2 — flagged, not staged.

## 5. Reconciliation with the existing mental model

- **"Didn't R2 already handle same-key multi-lens?"** R2 shipped per-lens *retraction*
  attribution because the corpus **had** the overlap — correctness for what existed, not an
  endorsement of the shape as a target state. The first draft mis-read it as "ratified
  practice"; the ratified end-state is that the overlap disappears, and R2's machinery both
  stays (retraction, generally) and carries the migration (dead-lens prune).
- **"Why not the client join?"** It remains the sanctioned **cross-target** shape (SQL join;
  two key namespaces joined at the reader). Within one target it is unnecessary once U1/U2
  exist — and a within-target join convention would just be the overlap wearing reader-side
  clothes.
- **"Does UNION break the anchored-reactivity model?"** No — each branch is an ordinary
  anchored/unanchored query with the existing per-branch derivations; ambiguity degrades to
  re-execution, never to a wrong Delete (§3.2).
- **New state?** None. No Edge-store schema change, no notification-semantics change, no
  conformance re-pin — the first draft's largest costs, withdrawn with the shape that needed
  them.
- **In-flight overlap check:** the ratified evaluation-consistency design touches the
  evaluate-emit seam; this touches vocabulary (pkgmgr), the visitor/executor, and
  `validateAll` — disjoint mechanisms; U1's one-evaluation-one-state argument for the tasks
  lens *uses* that design's edge memo, a dependency called out in §3.1's table, not a collision.

## 6. Alternatives considered

1. **Per-source bodies + merged read view at the Edge store** (the first draft's Fire A1) —
   **withdrawn at ratification**: arbitration for a workaround nothing requires; its real costs
   (view-scoped notification redefinition, conformance re-pin, store schema purge) bought
   convergence for a shape the platform now removes. The pass's blocker list against it (§9)
   stands as the record.
2. **`sharesKeyspaceWith` declared overlap** — withdrawn with it; an escape hatch preserves the
   shape and becomes the precedent every future package copies.
3. **Server-side read-modify-write merge** — rejected: violates the binding
   overwrite-by-reprojection principle; CAS churn; server-side retraction of a dropped column
   is undecidable.
4. **Column-union of the two catalog tails** (both tails RETURN the superset) — subsumed: U1's
   unification *is* the column union done once, in one lens, with no cross-lens alignment to
   maintain; the standalone version also worsens the tasks pair (`assignee = null` becomes an
   active clobber).
5. **Key-splitting + client join within one target** — unnecessary post-U1/U2; kept as the
   cross-target composition shape only.
6. **Prohibition without cause-removal** (the first draft's first instinct) — rejected: would
   have stranded live pairs the vocabulary forced into existence. Remove the cause, then
   prohibit.

## 7. Test strategy

- **U1:** expansion vectors (multi-walk prelude distribution; same-anchor-type enforcement
  fail-closed; per-domain producers byte-identical to today's for single-walk lenses);
  unification e2e — a dual-hat actor's `manifest.op.*` row carries `viaServices` **and**
  `viaRoles` with **one writer** (assert adapter call log: exactly one lens writes the key);
  claim beat — one lens, one pipeline, task flips claimable→mine with no cross-pipeline race;
  D1 gates: base-domain-only actor sees residence-path ops only (grant sets unchanged per
  domain).
- **U2:** visitor accepts UNION/UNION ALL; alias-mismatch and branch-cap refusals fail-closed;
  dedup semantics; per-branch tombstone/filter-retraction derivation (event on branch-B's
  anchor label retracts branch-B's row; ambiguous label re-executes); `one-bill` collapse e2e
  (both transaction kinds project; a tombstone of either retracts its row).
- **G:** equal-comparable refusal naming both lenses (intra- and cross-package); targetId dup
  refusal cross-package; `GrantSource` dup refusal; AnchorType-disjoint pass; activation
  backstop (later spec fails closed, incumbent serves); `one-bill` post-U2 installs clean.
- **A3a/A3b:** per-tail one-key-one-row vectors; the repaired guard fires on an injected
  duplicate (and stays silent corpus-wide).
- **Regression:** full edge conformance + `make test-edge-idb-conformance` green — untouched by
  design (no store change).

## 8. Decomposition, risks

- **Fire U1 (M):** pkgmgr `Walks` + expansion + the edge-manifest unification release (+ A3a
  audit of the remaining tails riding it).
- **Fire U2 (M):** engine UNION (visitor + executor + per-branch reactivity + validations),
  plain lenses, `one-bill` collapse as the consumer.
- **Fire G (S–M, after U1):** `validateLensKeySpaces` + installer sibling + activation backstop
  + targetId/GrantSource checks. No declaration surface.
- **Fire A3b (XS, after A3a).**
- **Risks:** multi-walk prelude cardinality (OPTIONAL chains multiply pre-aggregation — bounded
  by the DISTINCT-aggregation discipline A3a enforces; degrees are small in practice); UNION's
  per-branch retraction derivations are the subtle surface (mitigated by the conservative
  re-execute fallback and per-branch reuse of shipped logic); the unification changes three
  lens ruleIDs in one release (mirror healing via R2 hydrate prune — the rollout note: deploy
  server first, mirrors heal on next hydrate); `capabilityRoleIndex` stays outside the guard's
  v1 comparable, by name.

## 9. Review record — adversarial pass + ratification redirect

**First-draft adversarial pass (independent, read-only): ✅ RUN.** Its findings against the
then-proposed store merge — view-invisible changes (`applied`/`prunedKeys` body-LWW-scoped and
conformance-pinned), the `Entry.Data`/`Revision` seam corruption path, lens-less `Delete`,
hydrate's floored revisions — are **moot for the build** (that fire is withdrawn) but stand as
the recorded reasons the merge was heavier than drafted. Findings that survive into this shape:
the guard comparable must be struct-derivable (its cypher-extractor version made `one-bill` +
`capabilityRoleIndex` uninstallable — now §2.4/§3.3); the map-literal collect for `viaRoles`
(parallel collects misalign on dropped nulls — now §3.1); the six-lens blast radius ordering
A3a before A3b (now §1/§3.4); the column-union alternative and its tasks-pair refutation (now
§6.4).

**Ratification redirect (Andrew, 2026-07-27):** (1) *"Do not reject [UNION] just on the basis
of 'I need to write some code'"* — grounded: the grammar already parses it; the visitor is the
only refusal; honest size M with the §3.2 constraints. The first draft's "does nothing for
legitimately-separate pairs (tasks/queued)" rejection line was wrong as grounds — the pairs'
separateness was itself an artifact of the one-walk-one-lens coupling, not of the engine.
(2) *"What is the case where we must have multiple lenses writing to the same target and same
key?"* — none exists (§2.3); the overlap was workaround, not requirement, and the design's job
was to remove its causes, not arbitrate its symptoms. Standing rule reaffirmed: **long-term
value decides; size never rejects the right shape** ([[feedback_no_expedient_wrong_longterm_options]]).

## 10. Build note — Fire U1 increment 1 (Winston, Lattice Steward fire, 2026-07-28)

**Shipped:** `pkgmgr.LensSpec.Walk *AnchorWalk` → `Walks []AnchorWalk` (§3.1's primitive). All 17
non-self-anchored edge-manifest lenses migrated mechanically to a one-element `Walks` slice —
byte-identical compiled output, proven by the existing `TestEdgeManifest_Fire2_E2E_*` e2e and the
full `anchorwalk_test.go` suite (all passing unchanged). `ExpandReadGrantWalks` now loops
`parseWalks` per lens: every walk in one lens must resolve to the same `AnchorType`/`AnchorVar`
(fail-closed — a lens has one hand-authored tail, so it can `RETURN` exactly one anchor variable),
and no walk may bind a variable an earlier walk in the same lens already bound (fail-closed,
excluding a walk's own legitimate multi-clause reuse of its own bindings). `composeDataLensSpec`
concatenates every walk's `OPTIONAL MATCH` clauses ahead of the shared tail, in declaration order.

**Correction to §3.1 (found building, not fixed — real composition work remains):** §3.1's "the
compiled prelude is the actor head + every chain as OPTIONAL MATCH; the anchor is reachable via
any walk" does **not** hold for the DATA lens as literally specified once a second walk shares the
first's `AnchorVar` (which every walk in a lens must, by the rule above — there is no other case).
Grounded directly in the engine (`internal/refractor/ruleengine/full/executor.go`): `matchPath`'s
first-node check (:306-326) and `traverseRel`'s destination check (:668-679) both treat an
**already-bound variable — including one already null-bound by an earlier failed OPTIONAL MATCH —
as a same-node join constraint on re-reference**, never a fresh scan. Concretely: if walk 1's path
to `op` fails for an actor (residence-only lens with no role standing), `op` is null-bound; walk
2's OPTIONAL MATCH then sees `op` already present (even though null) and refuses to rebind it via
its own (role) path — that actor's role-only-reachable `op` rows never appear. The producer side
was already immune (`generateProducerSpec` renames colliding variables and unions per-branch
`collect(DISTINCT …)`); the DATA side's "verbatim" choice was not.

**Consequence:** `parseWalks`' variable-disjointness guard, added for the (still real) case of two
walks' *unrelated* variables accidentally colliding, has the side effect of rejecting **every**
2+-walk lens today, since the shared `AnchorVar` always re-collides. This is the correct,
fail-closed posture until the composition is designed — not a bug to route around. **Named
residual, filed:** the edge-manifest catalog/tasks/sessions unification (this fire's other named
deliverable) and the A3a tails audit **cannot proceed** until a follow-up increment designs the
actual multi-path-to-one-anchor composition — the leading candidate is renaming each walk's own
introduced variables on the data side too (mirroring the producer), with the tail rewritten to
combine the renamed candidates (needs `coalesce`-equivalent support in
`internal/refractor/ruleengine/full` — unverified whether it exists) rather than one shared
literal `RETURN <var>.key AS anchor`. Filed as [[project_refractor_shared_keyspace_u1_composition]]
— **Board:** `lattice.md`'s shared-keyspace row stays `🏗️ building`, next step "design the
multi-path anchor composition (data-side rename + coalesce tail)."

**Gates green:** `go build ./...`, `make vet`, `golangci-lint run ./...` (0 issues, full repo),
`STRICT=1 lint-conventions.go`, `lint-package-standard.go`, `lint-package-version.go` (edge-manifest
bumped 0.14.8→0.14.9), `lint-lens-anchors.go` (updated for the field rename), `lint-facet-discovery.go`,
full `go test ./... -p 4` (0 failures). `lint-lens-anchors.go` itself needed the same `Walk`→`Walks`
AST-field update (it statically parses lens declarations, independent of the compiler).

## 11. Build note — Fire U1 increment 2, the multi-path anchor composition (Winston, Lattice Steward fire, 2026-07-28)

**Shipped the composition increment 1's residual named:** a 2+-walk lens sharing one `AnchorVar` now
compiles and executes correctly, closing the fail-closed guard increment 1 left in place.

**Engine (`internal/refractor/ruleengine/full/executor.go`):** added a `coalesce(a, b, …)` function to
`evalFunctionCall` — returns the first argument that is not Cypher NULL, checked via a new `isNullBound`
helper that recognizes both a genuine nil interface and a node variable's OPTIONAL-MATCH null sentinel
(`(*nodeRef)(nil)`, which a bare `v == nil` misses since a typed nil pointer boxed in `any` is itself a
non-nil interface value). No grammar or visitor change was needed — `coalesce(...)` already parsed as a
generic function call; only the executor's dispatch switch was missing the case (confirmed by grounding:
the closest existing construct, `*CaseExpr`'s first-truthy-wins evaluation, sits right next to where the
new `case` landed).

**Pkgmgr (`internal/pkgmgr/anchorwalk.go`):** `composeDataLensSpec` now branches on walk count. A single
walk compiles exactly as before (byte-identical). Two or more walks: each walk's copy of the shared
`AnchorVar` is renamed to a walk-scoped name (`scopedVarName`, the same helper `generateProducerSpec`
already used) before its `OPTIONAL MATCH` clauses are emitted, then one `WITH coalesce(<scoped candidates>)
AS <AnchorVar>, <every other variable any walk bound>` clause folds them back to the declared name before
the shared tail runs. `parseWalks`' cross-walk collision guard now exempts the shared `AnchorVar` itself
(every walk binds it by construction) while still rejecting a collision on any OTHER variable name — that
part of the fail-closed posture is unchanged and still real (two independently-authored walks accidentally
reusing an unrelated variable name would still silently join).

**Why WITH-rebind, not a textual tail rewrite:** the design doc's leading candidate; confirmed as the
right shape by reading the executor rather than assuming — `applyWith` (`executor.go:708`) *replaces* the
binding set with only the projected items (no `WITH *`), so every variable the tail needs had to be
explicitly carried forward, but a `*nodeRef` value survives a `WITH … AS` projection intact (bindings are
`map[string]any`, and `resolveProperty`/further pattern-matching operate on whatever `*nodeRef` a name
holds, regardless of whether a `MATCH` or a `WITH` most recently bound it) — so the tail's own `<v>.key AS
anchor` and any further traversal off `<v>` keep working unmodified. Also confirmed the two existing
plain-lens retraction mechanisms that inspect compiled query shape (`AnchorProjectionKey` /
`AnchorDeleteResult`, `internal/refractor/ruleengine/full/anchor_delete.go`) are gated on
`actorEnumerator == nil` in `internal/refractor/pipeline/evaluate.go` — i.e. they never run for Personal
(actor-per-key) lenses, which is the only class `Walks` applies to — so their wholesale "reject any query
containing a WITH clause" rule (written for plain lenses, which never use WITH) does not apply here.

**Proof, not just compilation:** `TestExpandReadGrantWalks_MultiWalkLensComposesCoalescedAnchor`
(`internal/pkgmgr/anchorwalk_test.go`, replacing the increment-1 test that pinned rejection) asserts the
exact composed spec string. `TestExec_CoalesceMultiWalkAnchorComposition`
(`internal/refractor/ruleengine/full/executor_test.go`) goes further and actually *executes* the composed
query shape against a real embedded-NATS-backed KV with three identities — reachable via walk 0 only, walk
1 only, and neither — proving the previously-broken case (an actor reachable only via the second walk)
now projects the right anchor, and that reaching neither leaves one null-anchor row (not zero rows, and not
an error). `TestExec_CoalesceScalar` pins the plain scalar case.

**Consequence / what's still open:** the collision guard's remaining exemption boundary (OTHER variables
still can't collide across walks — unchanged) and the disjoint-key guard (§3.3, Fire G) are unaffected by
this increment. What this increment unblocks: the edge-manifest catalog/tasks/sessions unification (§3.1's
other named deliverable) and the A3a tails audit can now proceed — **not done in this fire** (out of scope
for one bounded increment: each pair needs its own tail rewrite, a package version bump, D1-gate regression
tests, and for catalog specifically the `viaRoles` map-literal `collect(DISTINCT …)` §3.1 already flagged).
**Board:** `lattice.md`'s shared-keyspace row moves next step to "edge-manifest catalog/tasks/sessions
unification (§3.1) + A3a tails audit," composition itself no longer blocking.

**Gates green:** `go build ./...`, `make vet`, `golangci-lint run ./...` (0 issues, full repo), `STRICT=1
lint-conventions.go`, `lint-package-standard.go`, `lint-package-version.go` (no packages/ content
changed), `lint-lens-anchors.go`, `lint-facet-discovery.go`, full `go test ./... -p 4` (0 failures).

## 12. Correction to §3.1 (increment 3 attempt, halted) — the coalesce shape silently drops rows when both walks multi-match (Winston, Lattice Steward fire, 2026-07-28)

**What was attempted:** the sessions pair (§3.1's simplest unification — byte-identical bodies) —
folding `edgeInstructorSessions` into `edgeEntitySessions` as its second `AnchorWalk`, per increment
2's shipped composition primitive. Lens edit, manifest/package/doc/verify-script updates, and test
rewrites were drafted in a worktree and got as far as a clean `go build`.

**What grounding the mechanism before shipping found:** increment 2's own regression test
(`TestExec_CoalesceMultiWalkAnchorComposition`) only exercises three actors each reachable via **at
most one** walk (direct-only, team-only, neither) — it never seeds an actor whose **both** walks
independently multi-match. That case behaves differently, and wrongly. A scratch executor-level
repro (same query shape, one actor with 2 real matches on walk 0 **and** 1 real match on walk 1)
returns **2 rows total, both showing a walk-0 anchor** — the walk-1 anchor never appears, in any row,
with no error.

**Root cause:** `composeDataLensSpec` concatenates every walk's `OPTIONAL MATCH` clauses
*sequentially* into one query (§3.1's "the compiled prelude is ... every chain as OPTIONAL MATCH").
`applyMatch` (`internal/refractor/ruleengine/full/executor.go:149-208`) expands **every** binding
from the prior clause through the next one — a nested-loop join, not a union — so two walks that
each depend only on the actor (not on each other's output) cross-product: N matches on walk 0 × M
matches on walk 1 → N×M rows. `coalesce(scoped_w0, scoped_w1, …)` (§11's rebind) then picks
whichever branch is non-null **first** on each cross-joined row — since both branches are genuinely
non-null on every one of those N×M rows once both walks have any real match, the query never emits
a row where the *other* branch's value is the one selected. The walk-1 anchors are not
duplicated — they are **dropped**, silently, with zero errors or empty-result signal. This is a
different (and worse) failure shape than increment 1's bug: that one *refused* a reachable anchor
outright (loud — a lens failed the anchor-coverage gate or a e2e); this one produces a
plausible-looking, non-empty result set that is simply short rows, which no existing gate catches.

**Scope of impact — not a security bug.** The read-grant **producer** side
(`generateProducerSpec`) shares the same cross-product but is immune: its
`collect(DISTINCT {anchorType, anchorId, via}) + collect(DISTINCT {...})` aggregates *within* each
branch's own `collect`, and `DISTINCT` absorbs the duplication — every real anchor from both walks
still lands in `readableAnchors`. So `cap-read.*` grants stay correct; only the **data lens's row
projection** (browse rows a Facet client renders) silently under-projects. No live victim today —
**zero shipped packages currently declare a 2+-walk lens** (this fire would have been the first
consumer) — but it blocks **all three** of §3.1's planned pair unifications, not just sessions:
catalog (an op reachable via *both* a permitted service *and* a held role — plausible for any
multi-hat staff actor) and tasks (an actor with both directly-assigned *and* role-queued tasks) have
the identical shape — two walks, either capable of multiple real matches for one actor.

**Not fixed in this fire — this is design work, not a build increment.** The coalesce shape is only
correct when at most one match is structurally guaranteed per walk per actor, which does not hold
for any of the three census pairs. Two directions, neither of them a quick patch:

1. **Real per-branch UNION for Personal-lens walk composition** — revisit §3.2's "a Personal tail
   carrying UNION is refused, declare Walks instead" scope-down; Personal lenses may need the same
   branch-independent evaluation Fire U2 designs for plain lenses, not merely a different vocabulary
   surface for declaring the same broken compilation.
2. **N independently-executed queries, one per walk, rows unioned+deduped by the caller** —
   compile each walk as its own query (as the OLD sibling-lens shape did), but have Refractor's own
   lens-evaluation loop merge their result ROWS into one target, instead of pkgmgr merging them into
   one QUERY. Avoids grammar/visitor work entirely; the open question is whether this reintroduces
   the divergent-column race U1 exists to eliminate for a row reached by both walks (sessions is
   immune — byte-identical bodies — catalog/tasks are the cases that motivated U1 in the first
   place, so this direction needs to show it still merges their columns correctly, not just their
   keys).

Neither is a same-fire fix; this needs a design pass (flagged for `lattice-designer`), not another
Steward build increment. **Board:** `lattice.md`'s shared-keyspace row moves to `🚧 blocked-on`
this correction — *not* `🏗️ building` a pair — until §12 resolves. The sessions-pair worktree was
discarded (clean `go build`, but shipping it would have silently dropped a dual-hat actor's
instructor-led sessions); no code from this fire landed.

---

## 13. The composition re-decision — §12's fork is false; both roads need one missing primitive

**Status: ✅ Andrew-ratified (2026-07-29)** — approved as recommended: N compiled queries per lens
(Option 2), the merge as the missing primitive, U2 unchanged and independent. Worked cypher for
both shapes folded in at §13.9 on ratification. Author: Winston (Designer fire, 2026-07-29), at
Andrew's direction after §12 halted increment 3 and flagged `lattice-designer`. Nothing from
increments 1–2 is withdrawn; §3.1's *compilation* text is rewritten in place (below), and §2.3's
catalog/tasks factoring is corrected — both rested on the same false premise §12 disproved.

### For Andrew (one-look ratification block)

**What it does (two lines).** A multi-walk Personal lens stops compiling to **one query with N
`OPTIONAL MATCH` chains** and starts compiling to **N queries, one per walk — each byte-identical
to the sibling lens that ships and works today** — which Refractor evaluates independently and
**merges by output key** before the write path. The anchor set becomes the union of the walks'
anchor sets (§12's dropped rows), and each RETURN column is taken from the walk that owns it.

**The one thing to understand before ratifying.** §12 offers a fork — *real UNION for Personal
lenses* vs *N queries merged by the caller* — and it is a **false fork**: both roads need the same
thing, and neither road is the thing. UNION **concatenates** row sets; it does not **merge** them.
Run the catalog pair through UNION and an op reachable via both a service and a role emits **two
rows under one IntoKey** — one carrying `viaServices`, one carrying `viaRoles` — which is precisely
the intra-lens duplicate §3.4's A3b guard exists to catch and precisely the last-writer-wins column
flap this whole row was filed for. The missing primitive is not a union operator; it is a
**deterministic per-key row merge**. Once that is built, *where* the N branches execute is a
plumbing choice, and the cheap plumbing wins.

**Fork — where the N branches execute. RESOLVED: N compiled queries per lens (Option 2).**
- **Option 2 (recommended).** Each walk compiles as its own complete query with the lens's shared
  tail. Zero grammar, zero visitor, zero new engine construct. Each branch is *exactly the query
  shape that ships today as a sibling lens*, so the evaluation half is proven by the running
  system and only the merge is new. Cost goes from **multiplicative to additive** — it deletes the
  N×M cross-product §2.2 already names as the expensive workaround.
- **Option 1 (rejected).** UNION inside one compiled rule for Personal lenses would **reverse
  §3.2's ratified scope-down** ("a Personal tail carrying UNION is refused — declare `Walks`
  instead"), add visitor/executor surface, *and still need the merge*. Worse on every axis.

**I was wrong last turn and am correcting it.** I suggested building **Fire U2 first** so Option 1
would become a scope-widening of existing machinery. Grounding killed it: U2's UNION serves
*genuine row-set unions* — `one-bill`'s heterogeneous anchors with **disjoint keys and no merge**.
Personal walk composition is an *anchor-set union with a per-key column merge*. Different problem,
different primitive. **U2 stays exactly as ratified, independent, buildable any time, unblocked and
unblocking.** No sequencing dependency in either direction.

**Frozen-contract change: NONE.** No author-facing vocabulary change either — `Walks` is unchanged;
only what it compiles to changes. Package authors write what they write today.

### 13.1 Why no single query can express this (the premise §3.1 and §2.3 both assumed)

The engine's clause composition is a **nested-loop join**: `applyMatch`
(`internal/refractor/ruleengine/full/executor.go:169-208`) expands *every* binding from the prior
clause through the next. Two walks that depend only on the actor therefore cross-product. Given
walk 0 reaching ops {A, B} and walk 1 reaching {B, C}, the correct answer is **three rows**
(A service-only, B both, C role-only), and no single-query shape produces it:

| Shape | Result | Why |
|---|---|---|
| Shared anchor var (inc 1, §3.1 as written) | A, B — **C lost** | A re-referenced variable is a same-node join constraint, never a fresh scan (`matchPath` :306-326, `traverseRel` :668-679). Walk 1 can only *filter* walk 0's anchors. **Loud** — fails anchor coverage. |
| Per-walk rename + `coalesce` (inc 2, shipped) | A, B — **C lost** | 2×2 cross-product; both branches non-null on every row once both walks match anything, so `coalesce` returns branch 0 every time. **Silent** — plausible non-empty rows, just short. This is §12. |
| `UNWIND (collect(w0) + collect(w1))` | would work | `UNWIND` is explicitly refused at the visitor (`visitor.go:145-146`), so this is engine work of the same order as UNION — and still leaves the provenance tail to re-derive per anchor. Rejected. |
| Real UNION branches | A, B, B, C | Correct anchor set, **two rows for B**. Concatenation, not merge. §13's premise. |

The producer side is untouched and stays correct: `generateProducerSpec` emits
`collect(DISTINCT {…}) + collect(DISTINCT {…})` per branch in one RETURN, so the cross-product's
duplicates are absorbed by `DISTINCT` and every real anchor from every walk lands in
`readableAnchors`. **`cap-read.*` grants were never affected by any of this** — the defect is
confined to the data lens's row projection. Do not "fix" the producer; its cross-product is a cost
note (§13.7), not a correctness one.

### 13.2 The shape — per-walk compilation + a merge at the result seam

**Compilation (pkgmgr, `composeDataLensSpec`).** Single walk: unchanged, byte-identical, all 17
shipped lenses keep their exact spec string. Two or more walks: emit **one spec per walk** — the
walk's own `OPTIONAL MATCH` chains plus **the lens's shared tail** — replacing increment 2's
rename+`coalesce` fold. `LensSpec` carries N specs where it carried one.

The tail sharing is not a new constraint to enforce: `composeDataLensSpec` already writes
`pws[0].tail` for every walk (`internal/pkgmgr/anchorwalk.go:494-508`), so **an identical RETURN
alias list across branches is structural, not a rule an author can violate.** That is what makes
the merge deterministic.

**Evaluation (Refractor).** The pipeline's compiled rule becomes a slice; a Personal lens's
per-actor evaluation runs each branch through the existing single-query machinery and merges the
combined result set before it reaches the envelope/write path — the same seam
`multiEntryRetractions` already post-processes at (`pipeline/evaluate.go`, the `reprojectActors`
→ `executeFullForActor` return). Branch evaluation is independent: no shared bindings, no
cross-product, cost additive.

**The merge rule — no arbitration, by construction.** For rows sharing one output key, each RETURN
column is resolved by **which walk's variables it derives from**, classified at compile time:

- **Walk-owned** — the column's expression references variables introduced by exactly one walk
  (`viaServices`, `viaRoles`, `assignee`, `queuedRole`). Value taken from that walk's branch; the
  other branches carry its empty/null by construction.
- **Anchor-derived** — the column references only the shared anchor (the 24 catalog columns:
  `op.data.*`). Every branch computes it from the same vertex, so every branch **must** agree;
  disagreement is a real defect and **fails the evaluation loudly** rather than picking a winner.
- **Unclassifiable** — the column cannot be attributed to one walk or to the anchor alone. The
  lens is **refused at expansion time** (install), never merged at runtime.

This is emphatically **not** the withdrawn per-source body merge. That one arbitrated between two
independently-authored lenses with genuinely divergent bodies. This one operates **inside one
lens**, across **its own declared walks**, through **one shared tail**, and **forbids conflict
instead of resolving it** — the same no-escape-hatch posture §3.3 enforces at the keyspace level.

**Reuse, don't reinvent:** the binding-provenance walk this classification needs is the same AST
analysis ratified for the auth-plane conjunct classifier
([evaluation-consistency §13.3](refractor-evaluation-consistency-design.md)) — one helper over
`full.CompiledRule.Query`, two callers. Whichever fire lands second reuses it; neither blocks the
other.

### 13.3 Fail-closed posture

Every uncertainty refuses at **install**, where an author can act on it — never at runtime, where
it would be a silent short row (the §12 failure shape this exists to end):

- A column that cannot be attributed to one walk or to the anchor → expansion refuses the lens,
  naming the column and the lens.
- Branch RETURN alias lists that are not identical → refuse. (Structurally impossible today; the
  check makes it impossible after a future refactor too.)
- Anchor-derived columns disagreeing across branches at runtime → a typed evaluation failure on
  the existing transient/DLQ channels, never a silent pick.
- Single-walk lenses take none of this: one rule, no merge, no classification, no new failure mode.

`parseWalks`' cross-walk variable-collision guard loses its reason under per-walk compilation
(separate queries cannot accidentally join), but **relaxing it is not required by this design** and
should not ride along silently — if a build increment relaxes it, that is its own decision to state.

### 13.4 What this unblocks, and what it corrects

Unblocks **all three** §3.1 pair unifications — every one of them has the two-walks-either-of-which-
multi-matches shape, so all three were blocked, not just sessions:

| Pair | Blocked because | After |
|---|---|---|
| Sessions | A dual-hat actor's instructor-led sessions silently dropped (§12's halted attempt) | Byte-identical bodies; merge is a no-op beyond key union |
| **Catalog** (the filed defect) | An op reachable only via a held role never projects | `viaServices` walk-owned by walk 0, `viaRoles` by walk 1, 24 columns anchor-derived |
| Tasks | An actor with both directly-assigned and role-queued tasks loses one set | `assignee`/`queuedRole` walk-owned, nullable exactly as ratified |

**§2.3's census factoring is corrected in place** for catalog and tasks: "both paths as OPTIONAL
MATCH + `collect(DISTINCT …)` aggregation" is true of the *producer* and false of the *data lens* —
aggregation absorbs a cross-product's duplicates, it does not turn a join into a union. The
one-lens conclusion stands; the compilation named to reach it does not.

### 13.5 Alternatives considered

1. **UNION for Personal lenses (§12 direction 1)** — rejected in the For-Andrew block: reverses a
   ratified scope-down, adds engine surface, still needs the merge.
2. **Add `UNWIND` to the engine** — the one single-query shape that would work. Rejected: visitor +
   executor work comparable to UNION, and the provenance tail would have to re-traverse per anchor
   to rebuild `viaServices`/`viaRoles`, which is the cross-product back again by another door.
3. **Keep sibling lenses; fix only the runtime collision guard (A3b) to detect the flap** — rejected:
   detection is not correction, and it leaves the N-writers shape this whole design exists to
   eliminate, with the guard terminal-failing six live lenses (§1).
4. **Merge in the adapter instead of the pipeline** — rejected: the adapter sees one row at a time
   and has no notion of an evaluation's result set; merging there would mean read-modify-write per
   row against a live target, reintroducing a race in the layer that currently has none.
5. **Require authors to hand-write one query per pair** — rejected: that *is* today's sibling-lens
   shape, i.e. the defect.

### 13.6 Test strategy

- **The §12 repro is the acceptance test, and it is written first, red.** One actor, walk 0 with
  ≥2 real matches, walk 1 with ≥1 real match on a *different* anchor; assert the union of anchors
  projects, not branch 0's subset. Executor-level (the scratch repro §12 already describes) and
  pipeline-level (assert on the adapter call log, not final state — a passing projection would also
  pass if the merge never ran).
- **Merge units:** walk-owned column takes its owner's value; anchor-derived agreement; anchor-derived
  *disagreement* raises rather than picks; unclassifiable column refused at expansion, naming it.
- **Single-walk byte-identical regression** across all 17 shipped lenses — the increment-1 property
  that must survive (its existing spec-string assertions are the guard).
- **Per-pair D1-gate regressions** in the unification increments: a multi-hat actor sees the union
  of both paths with correct `viaRoles` grouping (`app.js:769-784` is the live consumer); the
  retired sibling's mirror attributions prune at hydrate under R2's dead-lens set.
- **Producer non-regression:** `cap-read.*` output byte-identical before/after — the producer path
  is untouched and must be pinned as such.

### 13.7 Risks, residuals, sequencing

- **The merge is new correctness surface on the read path.** Bounded: Personal data rows only; the
  grant/producer plane is untouched (§13.1), so a merge bug is a wrong browse row, never a wrong
  grant. Stated so the blast radius is not overestimated either.
- **Cost:** N branch evaluations replace one N×M cross-product — additive instead of multiplicative,
  the same argument §2.2 already makes for UNION over stacked `OPTIONAL MATCH`. Expected cheaper at
  every real fan-out; measured, not assumed, in the first unification increment.
- **The producer's own cross-product survives** (correct but wasteful, `executor.go:930-935`'s
  inflation warning). Not this fire's scope; named here so it is not rediscovered as a bug.
- **Sequencing unchanged from §3:** this replaces U1's composition step. **U2 is independent and
  stays so.** Fire G (the disjoint-key guard) still lands *after* the unifications empty the census,
  blocking, no warn-first — unchanged.
- **Build order:** (a) the composition primitive + merge + classifier, no package changes;
  (b) sessions; (c) catalog — the filed defect; (d) tasks. Each of (b)–(d) is a package release with
  its own version bump, verify-script and D1-gate updates. (a) is one fire; (b)–(d) are one fire each
  or one combined release, the Steward's call on the release mechanics — but **not** (a) combined
  with any of them.

### 13.8 Adversarial pass

Run in-fire and self-directed (no independent reviewer available; stated plainly). Probes:

1. *"Does UNION actually solve it?"* — the probe that collapsed the fork. It fixes the anchor set and
   leaves two rows under one key for the catalog pair. Both §12 directions inherit the merge
   requirement; only one also inherits engine work.
2. *"Is the merge the withdrawn per-source arbitration wearing a new hat?"* — no, and the difference
   is checkable: one lens, one author, one shared tail (`anchorwalk.go:494-508`), and conflict is
   **refused** rather than resolved. If it arbitrated, it would be the withdrawn shape.
3. *"Is the producer really immune, or is that inherited from §12 unverified?"* — verified: both
   branches' `collect(DISTINCT …)` sit in one RETURN, so cross-product duplicates collapse and every
   walk's anchors survive. Grants were never wrong.
4. *"Is sessions really blocked, given byte-identical bodies?"* — yes. Identical *bodies* do not
   help when the *anchor set* is truncated; a dual-hat actor loses instructor-led sessions. All three
   pairs were blocked, which §12 states for sessions and this design generalizes.
5. *"Does this strand increment 2's shipped work?"* — `coalesce()` stays (a legitimate general engine
   function, independently tested). Only `composeDataLensSpec`'s multi-walk branch is replaced, and
   its test asserts a spec string that is supposed to change.

### 13.9 Worked examples — the two shapes side by side

Folded in at Andrew's request (2026-07-29) as a build aid. Both are **real shipped lenses**, quoted
from the tree, not illustrative inventions. The first is **Fire U2's** (§3.2) and appears here only
because the contrast is what collapsed §12's fork: *disjoint keys → concatenate; overlapping keys
with per-branch columns → merge.*

#### A. Where UNION is the whole answer — `one-bill` (Fire U2, plain lenses)

Two lenses into one bucket today, because the engine rejects UNION —
`packages/one-bill/lenses.go:12-25` states the reason in its own doc comment:

```cypher
-- oneBillRentEntries
MATCH (t:transaction)
MATCH (t)-[:postedTo]->(a:account)
MATCH (a)-[:heldFor]->(l:leaseapp)
RETURN t.key AS key, t.key AS transactionKey, a.key AS accountKey, l.key AS leaseAppKey,
       t.entry.data.type AS type, t.entry.data.amountCents AS amountCents,
       t.entry.data.memo AS memo, t.entry.data.postedAt AS postedAt, 'rent' AS source

-- oneBillCafeEntries — identical RETURN aliases, different anchor classes
MATCH (t:cafetransaction)
MATCH (t)-[:postedTo]->(a:cafeaccount)
MATCH (a)-[:heldFor]->(l:leaseapp)
RETURN t.key AS key, …same aliases…, 'cafe' AS source
```

Under U2 these collapse to **one lens**, the two bodies joined by `UNION`. §3.2's fail-closed
activation constraint (identical RETURN alias lists across branches) is already satisfied by hand
here, so the collapse is mechanical.

**Why concatenation suffices and no merge is needed:** the branches produce **disjoint keys** —
`vtx.transaction.<id>` vs `vtx.cafetransaction.<id>` — so no row is ever produced twice. **This is
the property the catalog pair does not have, and it is the entire line between the two demands.**

**Precondition (§3.3's residual):** both branches are authored by **one** package. `one-bill` is a
dedicated composition package owning no vertex types, links or permissions, declaring both ledgers
as dependencies and matching both their class labels in its own cypher; the ledgers write their own
separate buckets. A Lens spec belongs to exactly one package, so U2 can only ever union what one
package already authors — see §3.3 for the cross-package case.

#### B. Where a merge is required — `edgeCatalog` (this design, Personal lenses)

**Today:** two lenses, two walks, two hand-synchronised tails, one `manifest.op.<id>` key space —
`edgeCatalog` (`domainBase`: residence → `availableAt` template → `permitsOperation`) and
`edgeCatalogRoles` (`domainStaff`: `holdsRole` → `grantedBy` permission → `forOperation`). Whichever
re-derives last wins the whole body, so `app.js:775`'s `viaRoleName` grouping collapses for a
multi-hat actor whenever `edgeCatalog` lands last (§1).

**Unified declaration** — one lens, two walks, **one tail**:

```go
CanonicalName: "edgeCatalog",  Personal: true,  IntoKey: []string{"__actor","ns","entityId"},
Walks: []pkgmgr.AnchorWalk{
  {GrantDomain: domainBase,  AnchorType: "meta", AnchorVar: "op",
   Chain: []string{chainResidence, chainAvailableTemplates,
                   "(tpl)-[:permitsOperation]->(op:meta)"}},
  {GrantDomain: domainStaff, AnchorType: "meta", AnchorVar: "op",
   Chain: []string{chainHeldRoles,
                   "(role)<-[:grantedBy]-(perm:permission)-[:forOperation]->(op:meta)"}},
},
Spec: edgeCatalogTail,   // ONE shared tail — §13.2's determinism rests on this
```

**What §13.2 compiles it to — two complete queries**, each `anchorWalkHead` + that walk's chains +
the shared tail:

```cypher
-- branch 0 (domainBase)
MATCH (identity:identity {key: $actorKey})
OPTIONAL MATCH (identity)-[:residesIn]->(home)-[:containedIn*0..]->(container)
OPTIONAL MATCH (container)<-[:availableAt]-(tpl:service)
OPTIONAL MATCH (tpl)-[:permitsOperation]->(op:meta)
WITH op
WHERE op.key <> null
RETURN
  op.key AS anchor,
  "manifest.op" AS ns,
  nanoIdFromKey(op.key) AS entityId,
  op.key AS opMetaKey,
  op.data.operationType AS operationType,
  op.presentation.data.title AS title,
  op.presentation.data.shortLabel AS shortLabel,
  op.presentation.data.description AS description,
  op.presentation.data.icon AS icon,
  op.presentation.data.tone AS tone,
  op.presentation.data.submitLabel AS submitLabel,
  op.presentation.data.group AS group,
  op.inputSchema.data.schema AS inputSchema,
  op.fieldDescriptions.data.fieldDescriptions AS fieldDescriptions,
  op.dispatch.data.class AS dispatchClass,
  op.dispatch.data.authContext AS dispatchAuthContext,
  op.dispatch.data.targetField AS dispatchTargetField,
  op.dispatch.data.targetType AS dispatchTargetType,
  op.dispatch.data.contextParams AS dispatchContextParams,
  op.dispatch.data.reads AS dispatchReads,
  op.dispatch.data.optionalReads AS dispatchOptionalReads,
  op.dispatch.data.visibleWhen AS dispatchVisibleWhen,
  op.sensitive.data.value AS sensitive,
  [(op)<-[:permitsOperation]-(svc:service) | svc.key] AS viaServices,
  null AS viaRoles
```

```cypher
-- branch 1 (domainStaff)
MATCH (identity:identity {key: $actorKey})
OPTIONAL MATCH (identity)-[:holdsRole]->(role:role)
OPTIONAL MATCH (role)<-[:grantedBy]-(perm:permission)-[:forOperation]->(op:meta)
WITH op, role
WHERE op.key <> null
RETURN
  …the same 24 columns, byte-identical, through `viaServices`…,
  collect(DISTINCT {key: role.key, name: role.canonicalName.data.value}) AS viaRoles
```

**Column classification (§13.2's merge rule) for this pair:**

| Columns | Class | Merge behaviour |
|---|---|---|
| `anchor`, `entityId`, `opMetaKey`, `operationType`, the 7 `op.presentation.*`, `inputSchema`, `fieldDescriptions`, the 8 `op.dispatch.*`, `sensitive` (23) | anchor-derived (`ns` is a constant) | Both branches read the same `op` vertex ⇒ must agree; disagreement raises, never picks |
| `viaServices` | **anchor-derived** — a pattern comprehension off `op` alone, not off `tpl` | Identical in both branches **by construction**. This is exactly what 0.14.3/0.14.4 hand-maintained across two tails (§1); one shared tail makes it structural |
| `viaRoles` | **walk-owned by walk 1** — `role` is introduced by walk 1 only | Taken from branch 1; branch 0 carries null |

**Result per key:**

| Op reachable via | Row |
|---|---|
| services only | branch 0 as-is, `viaRoles` empty |
| **roles only** | branch 1 as-is — **the row §12's `coalesce` silently dropped** |
| both | one row: the 24 anchor-derived columns agree, `viaRoles` from branch 1 |

**One mechanic left to the build increment, deliberately:** whether a non-owning branch projects
`null AS viaRoles` (as written) or omits the column and lets the merge supply it. Both satisfy
§13.3's identical-alias-list requirement; it is mechanics, not shape. `null` parses as a `Literal`
today (the `<> null` idiom is used throughout these tails), so the written form needs no engine
change — confirm before relying on it.

## 14. Build note — build order (a): the composition primitive + merge + classifier (Lattice Steward, 2026-07-29, `69119818`)

**Shipped, no package changes** (per §13.7's build order — sessions/catalog/tasks (b)–(d) are
separate, unbuilt fires):

- **pkgmgr composition** (`internal/pkgmgr/anchorwalk.go`): `composeDataLensSpec` now returns
  `[]string` — a single walk still yields exactly the byte-identical one-element slice it always
  did; two or more walks yield one query PER WALK (head + that walk's own OPTIONAL MATCH clauses,
  **unrenamed** + the shared tail, unchanged), replacing increment 2's scoped-rename+`coalesce`
  fold entirely. `LensSpec` gained `SpecBranches []string` (`Spec` stays empty for a multi-walk
  lens); `build.go` marshals `cypherBranches` (new, `omitempty`) alongside the existing
  `cypherRule`.
- **Wire + compile** (`internal/refractor/lens`): `LensSpec.CypherBranches []string` (mutually
  exclusive with `CypherRule`); `translateSpec` compiles each branch independently via the
  existing `SelectForLens` engine-selection path (no engine change) and refuses if any branch
  resolves to a different engine than branch 0. `Rule.CompiledBranches []ruleengine.CompiledRule`
  carries the N results; `Rule.CompiledRule` stays `= CompiledBranches[0]` so every existing
  single-field consumer (key-column threading, `AnchorDeleteResult`/`AnchorProjectionKey` — both
  dead code on a Personal lens's actor-enumerator path anyway) keeps working unchanged.
- **Pipeline execution + merge** (`internal/refractor/pipeline`): `Pipeline.fullCRBranches`
  (nil for every non-multi-walk lens). `executeFullForActorOnce` now calls
  `executeBranches`, which runs each branch's `ExecuteWithFootprint` independently per actor,
  **merges the raw `ProjectionResult` rows by output Key**, and unions the footprints —
  strictly BEFORE the existing envelope/collision-guard/`multiEntryRetractions` post-processing,
  which is otherwise untouched. The merge (`branchmerge.go`) is **uniform, not ownership-typed**:
  for a key shared by 2+ branches, every column's value is "the single distinct non-null value
  found across the sharing rows" — a walk-owned column naturally has at most one non-null value by
  construction (parseWalks' cross-walk variable-disjointness guard), an anchor-derived column is
  naturally identical in every branch, and either shape merges correctly with the SAME code path.
  Two distinct non-null values fails the actor's evaluation loudly (a typed error propagated up
  through the normal error-return path — no new failure channel). This is a **simplification from
  §13.2's text**, not a different behavior: the design describes classifying columns to decide
  *how* to merge; the shipped merge doesn't need the classification to decide that at runtime,
  because "coalesce non-null, error on disagreement" is what both classes reduce to.
- **Classifier** (`internal/refractor/ruleengine/full`): `CollectVariableRefs` (new file,
  `bindings.go`) is a fresh AST-walk over `full.Expr` — the **same technique**
  `hasMultiBindingConjunctUnit` (evaluation-consistency §13.3, `internal/refractor/projection`)
  already uses, not a shared function (each package's classifier now evolves against its own
  fail-closed default independently, as its own doc comment states). `ClassifyBranchReturnColumns`
  (new file, `branchplan.go`) classifies every RETURN column of N compiled branches as
  anchor-derived or walk-owned-by-exactly-one, refusing (naming the column) when a column mixes
  two walks' variables or is otherwise unclassifiable. It needs **no explicit actor/anchor input**:
  the "common" variable set is derived as the intersection of every branch's own MATCH-bound
  variables — exact because parseWalks already forbids two walks from binding the same non-anchor
  name, so only the fixed actor binding and the lens's one shared AnchorVar can appear in every
  branch.

**Deviation from §13.3's text — WHERE the refusal fires.** §13.3 says "expansion refuses the
lens (install)," read at design time as pkgmgr's `ExpandReadGrantWalks`. Building it there turned
out to need `internal/pkgmgr` to import `internal/refractor/ruleengine/full` in **production**
code — which cycles: `full`'s own test corpus (`parse_test.go`) imports a real package
(`packages/identity-hygiene`) that imports `pkgmgr`, closing the loop back through `full`. The
classifier is called instead from **Refractor's `translateSpec`**, at lens-compile time (the
moment a multi-walk spec is first dispatched — package install for a live stack, or any
`ExpandReadGrantWalks`-consuming test that goes through real compilation). This is a **later**
gate than originally intended (a live CDC-watch compile error, not a `go build`/`go test` failure
inside pkgmgr itself), but still strictly before any row is ever merged — no unclassifiable lens
can reach runtime. `internal/pkgmgr/anchorwalk_test.go`'s composition tests no longer assert the
refusal (moved to `internal/refractor/lens/branchspec_translate_test.go`, which now owns it) —
`composeDataLensSpec`'s doc comment states why. Future increments (b)–(d), and anyone touching
this seam, should verify their new multi-walk lens compiles cleanly against a real Refractor
before assuming pkgmgr alone would have caught an unclassifiable column.

**Tests:** `full` package — `CollectVariableRefs` unit tests (variable ref, property chain,
literal/parameter-contribute-nothing, binary-op union, unrecognised-form fail-closed) +
`ClassifyBranchReturnColumns` (walk-owned/anchor-derived agreement, mixed-walk-vars refused,
mismatched alias lists refused, <2 branches refused). `pkgmgr` — the Fire U1 multi-walk
composition test rewritten for the N-branch shape (byte-for-byte per branch, no `coalesce`).
`internal/refractor/lens` — `translateSpec` compiles N branches independently, `CypherRule`/
`CypherBranches` mutual exclusivity, a branch parse error names its index, the mixed-walk-column
refusal (moved from pkgmgr, above). `internal/refractor/pipeline` — **the §12 repro is the
acceptance test, and it is the load-bearing one**: one actor, walk 0 reaching 2 real anchors
(via a service), walk 1 reaching 1 real anchor on a genuinely different anchor (via a role);
asserts the merged result set carries all 3, where the withdrawn `coalesce` shape (§12) silently
dropped the role-only anchor. Plus: a single-branch lens is provably unaffected (no merge
machinery engages), and the merge's own anchor-derived-disagreement backstop fails closed
(exercised directly against `mergeBranchRows`, independent of the compile-time classifier).

**Gates:** `go build ./...`, `make vet`, `golangci-lint run` (the four touched packages + `full`),
`STRICT=1 lint-conventions.go`, full `go test ./... -p 4` (113 packages green — wide blast radius:
touches `ruleengine/full`, `pkgmgr`, `refractor/lens`, `refractor/pipeline`, `cmd/refractor`).
No `packages/**` DDL touched, so no `make verify-package-*` run.

**Residuals, honestly named:**
- **(b)–(d) are unbuilt.** Sessions, catalog (the originally-filed defect), and tasks each still
  need their own fire: convert the pair of sibling lenses to one multi-`Walks` lens and retire the
  sibling, per §13.4/§13.7's build order. This primitive is necessary but not sufficient for any
  of the three — no live lens uses `SpecBranches` yet.
- **`translateSpec`'s per-branch engine-selection loop** (`corekv_source.go`) calls
  `defaultRegistry.SelectForLens` once per extra branch with a synthetic `ID` suffix
  (`"<id>#branch<N>"`) purely for engine-selection logging/attempted-engines bookkeeping — cosmetic,
  not a correctness concern; a future refactor could thread the real branch index through more
  cleanly if `AttemptedEngines`-per-branch ever needs surfacing.
- **The producer's own cross-product is untouched** (§13.1/§13.7 — already named, unaffected by
  this fire; `cap-read.*` grants were never wrong and stay that way).

## 15. Build note — build order (b): sessions unification (Lattice Steward, 2026-07-29)

`packages/edge-manifest`'s `edgeEntitySessions` (residence-chain path, `domainBase`) and
`edgeInstructorSessions` (own-instructor path, `domainProvider`) gain a single lens: a second
`AnchorWalk` on `edgeEntitySessions`, `edgeInstructorSessions` retired. This is the retry of §12's
halted increment-3 attempt, now against (a)'s corrected N-branch-merge primitive instead of the
withdrawn coalesce shape.

- **Lens declaration** (`lenses.go`): `edgeEntitySessions.Walks` gains a second entry —
  `GrantDomain: domainProvider, AnchorType: "session", AnchorVar: "sess", Chain:
  ["(identity)<-[:identifiedBy]-(instr:instructor)<-[:ledBy]-(sess:session)"]` — verbatim the
  former `edgeInstructorSessions` walk. `parseWalks`' cross-walk disjointness guard passes cleanly:
  walk 0 (domainBase) binds `{home, container, studio}`, walk 1 (domainProvider) binds `{instr}`,
  no overlap.
- **Shared tail** (`edgeSessionsTail`, replacing both `edgeEntitySessionsTail` and
  `edgeInstructorSessionsTail`): carries BOTH bridging clauses unconditionally —
  `OPTIONAL MATCH (sess)-[:atStudio]->(studio:studio)` and
  `OPTIONAL MATCH (sess)-[:ledBy]->(instr:instructor)` — rather than one clause per walk. Each
  compiled branch re-derives whichever counterpart var its own walk didn't bind; for the var its
  walk DID bind, the tail's OPTIONAL MATCH is a harmless re-match (Cypher's already-bound-variable-
  is-a-join-constraint semantics, `executor.go`'s "constrained-target case" — confirmed against the
  executor before writing the tail, not assumed). This keeps the tail single and
  branch-order-independent, rather than requiring per-walk tail variants.
- **Consumers updated:** `package.go`/`manifest.yaml` (seventeen lenses, description + version
  0.14.9→0.14.10), `docs/components/edge-manifest.md`, `scripts/verify-package-edge-manifest.go`
  (`emExpectedLenses` drops the entry; the `cypherRule`-literal check now falls back to
  `cypherBranches` when `cypherRule` is empty — the first live consumer of that field). No FE
  change: both retired and surviving lenses already wrote the identical `manifest.ent.<sessionId>`
  key, so `app.js` never distinguished them by lens name.
- **Tests:** package-level helpers gained `emComposedSpecBranch` (one branch by index) and
  `emSpecTexts` (every branch, for lens-wide structural checks) alongside the existing
  single-spec `emComposedSpec`. The two pre-existing per-lens session tests
  (`lens_cypher_test.go`) now select branch 0 / branch 1 of the merged lens instead of two
  separate lenses; row-shape assertions are unchanged (byte-identical RETURN, as designed). The
  provider-persona coverage test (`coverage_proof_test.go`) exercises branch 1 in place of the
  retired lens's spec.
- **Live verification:** `make reinstall-package PKG=packages/edge-manifest` diff-applied onto
  the running dev stack (fromVersion 0.14.8→0.14.10); `go run ./scripts/verify-package-edge-manifest.go`
  passed 86/86 assertions, confirming the installed `edgeEntitySessions` aspect carries
  `cypherBranches` with both branches' `manifest.ent` literal. Refractor's live `pipeline: processed`
  log for the lens's own ruleId shows zero errors across the reinstall's CDC cascade — the first
  real install of a `SpecBranches` lens, proving (a)'s primitive end-to-end, not just in unit tests.

**Gates:** `go build ./...`, `make vet`, `golangci-lint run` (`packages/edge-manifest`, `scripts`),
`STRICT=1 lint-conventions.go`, `lint-package-standard.go`, `lint-package-version.go`,
`lint-lens-anchors.go`, `lint-facet-discovery.go`, full `go test ./... -p 4`, plus the live
`verify-package-edge-manifest` run above.

**CI caught a real gap the local suite and the live-stack check both missed:**
`internal/refractor/edge_manifest_fire1_e2e_test.go`'s `activateEdgeManifestLenses` hand-constructs
each `lens.LensSpec` from the composed `pkgmgr.LensSpec` (its own embedded-NATS harness, not the
installer path `make reinstall-package` exercises) and only ever copied `CypherRule: ls.Spec` —
for `edgeEntitySessions` that field is now empty (`ls.SpecBranches` carries it), so `translateSpec`
correctly refused with "cypherRule required" and the test hung to its 20s deadline before failing.
Neither the package-level tests (which go through `pkgmgr.ExpandReadGrantWalks` +
`emComposedSpecBranch` directly) nor the live `reinstall-package` verification (which goes through
the real installer's `build.go` marshal) exercise this SPECIFIC hand-rolled activation helper — it
is `internal/refractor`'s own fixture, a second, independent place a `pkgmgr.LensSpec` gets copied
into a `lens.LensSpec`. Fixed by also copying `CypherBranches: ls.SpecBranches` (`502bc421`). Worth
naming for (c)/(d): grep for other hand-rolled `lens.LensSpec{...}` fixtures before assuming the
package + live-install checks are exhaustive — they were not, here.

**Residuals, honestly named:**
- **(c) catalog and (d) tasks remain unbuilt** — the filed defect (catalog) and the tasks pair,
  same build order, per §13.7. Catalog's columns are NOT byte-identical (divergent `viaServices`/
  `viaRoles`), so its merge exercises the classifier's mixed-walk-column refusal path this
  increment's byte-identical case never touched — the next fire should ground that path against a
  real compile, not assume it from the unit tests in (a).

## 16. Build note — build order (c): catalog unification (Lattice Steward, 2026-07-29)

`packages/edge-manifest`'s `edgeCatalog` (service-`permitsOperation` path, `domainBase`) and
`edgeCatalogRoles` (held-role `grantedBy`→`forOperation` path, `domainStaff`) gain a single lens: a
second `AnchorWalk` on `edgeCatalog`, `edgeCatalogRoles` retired. This is (b)'s residual — the one
build (a)'s unit tests never exercised: a real, divergent-column, walk-owned merge.

- **A real classifier gap, found by grounding before writing the tail** (not assumed from (a)'s
  unit tests, per the residual above). `ClassifyBranchReturnColumns` (`internal/refractor/
  ruleengine/full/branchplan.go`) walked a `viaServices` pattern comprehension
  (`[(op)<-[:permitsOperation]-(svc:service) | svc.key]`, already shipped in `edgeCatalogTail`
  pre-merge) and added the comprehension's own freshly-matched local node (`svc`) to the column's
  external-dependency set alongside the real dependency (`op`) — `svc` is bound by no branch's own
  top-level `MATCH`/`OPTIONAL MATCH` clauses (it exists only inside the comprehension), so the
  ownership loop found no owning branch and refused the column as ambiguous. Fixed: a referenced
  name bound by NO branch at all cannot be a walk's provenance var (every real walk-bound name
  appears in at least that walk's `branchOwn`), so it is now skipped — the same treatment
  `CollectVariableRefs` already gives a `*Literal`/`*ParameterRef` leaf — rather than forcing
  `ambiguous = true`. Verified empirically before touching the package (a throwaway probe test
  against the real classifier reproduced the refusal, then confirmed the fix), and pinned as a
  permanent regression test (`TestClassifyBranchReturnColumns_PatternComprehensionLocalVarIsNotADependency`).
- **The merge shape:** `viaServices` stays exactly as shipped — a pattern comprehension off `op`
  alone — now correctly classified `ColumnAnchorDerived` (both branches compute it identically by
  construction, no per-branch variant needed). `viaRole`/`viaRoleName` (folded in from the retired
  `edgeCatalogRolesTail`, kept as the existing SCALAR column names rather than switching to a
  `collect`-based `viaRoles` list) are `ColumnWalkOwned` by the role branch: `role` is bound only by
  that branch's own chain, so the service branch's copy of the shared tail references an unbound
  `role` and evaluates it null by the executor's ordinary `VariableRef`/`PropertyAccess` nil-on-
  unbound behavior (`full/executor.go`'s `evalExpr`/`resolveProperty`/`propertyOf` — verified by
  reading, not assumed) — no per-branch tail variant, no special-cased null literal. This is a
  **deliberate departure from §13.9's illustrative worked example**, which sketched a
  `collect(DISTINCT {key, name})`-based `viaRoles` list: that shape aggregates over whatever rows
  exist before the `RETURN`, and for the service branch (where `role` is never matched by anything,
  not even an `OPTIONAL MATCH`) it does not evaluate to an empty list — it evaluates to one
  degenerate `{key:null,name:null}` entry per pre-aggregation row, which is not the same shape as a
  clean `null`/`[]`. The scalar form sidesteps this entirely (no aggregation, no grouping, no
  fan-out risk) and matches the FE's existing contract byte-for-byte (`app.js`'s
  `o.data.viaRoleName || o.data.viaRole` and `o.data.viaServices` reads) — §13.9's own closing note
  named the exact shape as "mechanics, not shape... left to the build increment, deliberately."
- **The "harmless re-match" trick (b) used for `studio`/`instr` does NOT apply here.** Restating the
  role-reachability chain as a shared-tail `OPTIONAL MATCH` off `identity`/`op` (both common vars)
  would make `role` bound in every branch, reclassifying `viaRole`/`viaRoleName` as anchor-derived
  instead of walk-owned — collapsing exactly the merge surface (b)'s residual said this fire should
  exercise for real. Kept walk-owned on purpose, sourced from the role Walk's own pre-existing
  chain.
- **Consumers updated:** `package.go`/`manifest.yaml` (sixteen lenses, description + version
  0.14.10→0.14.11), `docs/components/edge-manifest.md`, the four `packages/edge-manifest/*_test.go`
  files that referenced the retired lens by name (`composed_test.go`, `coverage_proof_test.go`,
  `lens_cypher_test.go`, `package_test.go` — each switched to `emComposedSpecBranch(t, "edgeCatalog",
  N)`, the same helper (b) built). `TestManifestAnchorCoverage_ResidentWorld`'s pre-existing
  `emComposedSpec(t, "edgeCatalog")` call (unrelated to this fire's own new assertions) would have
  broken the moment `edgeCatalog` went multi-walk — found by running the suite, not by inspection;
  worth naming for (d): grep every bare `emComposedSpec(t, "<name>")` call, not just the ones the
  fire's own diff touches, before assuming a lens's conversion is consumer-complete.
- **Live verification:** `make cycle-refractor` (rebuilds `bin/refractor` from `main`, relaunches
  against the running stack — this fire also touched the shared `ruleengine/full` classifier, so
  the running Refractor was stale on more than the package), then `make reinstall-package
  PKG=packages/edge-manifest` diff-applied onto the running dev stack (fromVersion
  0.14.10→0.14.11, 6 tombstoned / 6 updated); `go run ./scripts/verify-package-edge-manifest.go`
  passed 87/87 assertions, confirming the installed `edgeCatalog` aspect carries `cypherBranches`
  with BOTH branches projecting `manifest.op`. Refractor's log shows zero `ERROR`/panic entries in
  the reinstall's CDC-cascade window. `make cycle-loupe` also rebuilt/relaunched (reaches both
  touched packages via `go list -deps`).

**Gates:** `go build ./...`, `make vet`, `golangci-lint run` (`internal/refractor/ruleengine/full`,
`packages/edge-manifest`), `STRICT=1 lint-conventions.go`, `lint-package-standard.go`,
`lint-package-version.go`, `lint-lens-anchors.go`, `lint-facet-discovery.go`, full `go test ./...
-p 4` (wide blast radius — a shared classifier in `ruleengine/full` reaches every consumer of
multi-walk lenses, not just edge-manifest), plus the live `verify-package-edge-manifest` run above.

**Residuals, honestly named:**
- **(d) tasks remains unbuilt** — same build order, per §13.7: `edgeTasks`/`edgeTasksQueued` fold
  the same way, `assignee`/`queuedRole` walk-owned exactly as ratified. No new primitive needed —
  (a)'s composition + merge + (c)'s classifier fix together are sufficient; (d) is pure application
  of the now-proven pattern.
- **The classifier fix widens what compiles, which is the intended direction** (fail-CLOSED stays
  intact — an expression referencing a name bound in exactly one branch, or in more than one, is
  still refused exactly as before; only a name bound in NO branch changed from "refuse" to "ignore,"
  and such a name can only arise from an expression-local binding form like a pattern comprehension
  or a future list/map comprehension, never from a real cross-walk variable). Worth a second pass
  if a future engine feature introduces a NEW way to bind a name without a top-level `MATCH` clause
  — the fix's reasoning (`owner == -1` ⟺ expression-local) would need re-grounding against that
  feature, not assumed to still hold.
