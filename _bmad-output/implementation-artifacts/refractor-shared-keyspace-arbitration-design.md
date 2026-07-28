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
Author: Winston (Designer fire, 2026-07-27) · Lattice lane (Stream 2, [Refractor] / [Pkgmgr]).
Backlog row: *"Two lenses sharing one IntoKey race per column"*.

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
| Catalog | An enrichment: one row per op, columns from two paths | **One lens**: both paths as OPTIONAL MATCH + `collect(DISTINCT …)` aggregation — expressible since `1b9852f2` fixed composed-collect dedup; U1 supplies the two grant domains |
| Tasks | State-disjoint rows (`assignedTo` xor `queuedFor`) | **One lens, two chains**, branch-specific nullable columns (`assignee` null when queued, `queuedRole` null when assigned); one evaluation observes one state (per-key memo + the ratified evaluation-consistency design's edge memo) |

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
- The compiled prelude is the actor head + **every** chain as `OPTIONAL MATCH`; the anchor is
  reachable via *any* walk; the existing null-skip/realness handling covers path-less rows. The
  tail aggregates per path with `collect(DISTINCT …)` — the discipline `1b9852f2` made sound
  and the A3a audit enforces corpus-wide.
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
