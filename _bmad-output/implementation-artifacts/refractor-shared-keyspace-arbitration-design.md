# Shared projection keyspaces — per-source arbitration + the keyspace guard (design)

**Status: 📐 awaiting-Andrew (ratification).** Author: Winston (Designer fire, 2026-07-27) ·
Lattice lane (Stream 2, [Refractor]). Backlog row: *"Two lenses sharing one IntoKey race per
column"* (`lattice.md` Component maintenance, ★★ M).
Adversarial pass **✅ RUN this fire** (independent read-only reviewer) — 3 blockers + 5 must-fixes
against the first draft; **all folded** (§9 lists them; §3 is the corrected shape, resequenced
A3a → A1 → A2 per the pass).

> **For Andrew — two lines.** Two lenses projecting one key is not an authoring accident to
> outlaw — the engine has no `UNION` (`visitor.go:72`), so N-lenses-into-one-keyspace is the
> platform's **only** union idiom, endorsed by Contract #6 ("multi-output patterns are additional
> Lenses") and by the ratified retraction design R2, which already shipped per-lens attribution
> (`Sources`) for exactly this overlap — *for retraction*. The **body** is still single-winner
> LWW, so the last lens erases the other's columns (`viaRole` blanks today). Recommendation:
> **complete R2's shape** — per-source bodies with a write-time-materialized merged `Data` in the
> Edge store — plus a **struct-derivable keyspace guard** (default-deny undeclared overlap), and
> an **18-tail audit** fixing the duplicate-row emissions 0.14.4-style cross-products create
> (the runtime collision guard is a structural no-op for all 18 nats-subject lenses today, and
> six lenses would terminal-fail the moment it is fixed — audit first, then fix the guard).
>
> **Fork (resolved, my recommendation):** arbitration point. **A = merge at the Edge store**
> (recommended); B = server-side column-union of the two catalog tails (buildable — the pass
> proved it end-to-end — and simpler *for the filed row alone*, but it needs the same tail fix
> anyway, hand-aligns O(paths²) columns forever, and **actively worsens the latent tasks-pair
> hazard**: `edgeTasksQueued` would start projecting `assignee = null` over the claimant's value
> — §7.7); C = server-side read-modify-write (rejected — violates overwrite-by-reprojection);
> D = forbid overlap (rejected — it is the only expressible union, ratified practice).
>
> **No frozen-contract change.** The `sharesKeyspaceWith` declaration is `LensSpec`-level; the
> Delta-envelope `Delete` gains an additive `lens` field (old clients ignore unknown fields —
> the R1 posture).

---

## 1. Problem + demand

`edgeCatalog` (service path) and `edgeCatalogRoles` (role path) both project
`manifest.op.<opId>` per actor — identical IntoKey `["__actor","ns","entityId"]`, same `ns`
literal, `entityId = nanoIdFromKey(op.key)` (`packages/edge-manifest/lenses.go:98-117`,
`:271-289`). Every write is a whole-body replace (`adapter/natssubject.go:237-263`; Edge store
`internal/edge/store/bolt.go:145-148` under LWW-by-revision). The roles tail RETURNs the service
tail's 24 columns **plus** `viaRole`/`viaRoleName` — whichever lens re-derives last wins the
body; `cmd/facet/web/app.js:775` groups by `viaRoleName`, so a multi-hat actor's grouping
collapses whenever `edgeCatalog` lands last. The 0.14.3/0.14.4 mitigations equalized
`viaServices` only; any column either tail adds re-opens the hole — there is no gate.

**The census (this fire) shows a class, not an instance** — all on the nats-subject transport:

| Overlap | Shape | State |
|---|---|---|
| `edgeCatalog` + `edgeCatalogRoles` → `manifest.op.<id>` | divergent (+2 cols) | the filed defect |
| `edgeTasks` + `edgeTasksQueued` → `manifest.task.<id>` | divergent (`assignee` vs `queuedRole`) | **latent**: the claim affordance is `!t.data.assignee` (`app.js:1084-1088`); row-disjointness rests on a *data* invariant holding across two *independent* CDC re-executions — a stale queued-lens write after a claim flips a claimed task back to claimable |
| `edgeEntitySessions` + `edgeInstructorSessions` → `manifest.ent.<id>` | byte-identical RETURNs | benign today, safe only by hand-maintained textual coincidence |

Three structural facts sharpen the demand (all verified; the third's blast radius comes from the
adversarial pass):

- **0.14.4 introduced an intra-lens collision**: the roles tail's aggregating
  `WITH op, role, collect(…)` groups by `(op, role)` (`executor.go:766-790`) — an actor holding
  two roles granting one op emits two rows with one IntoKey.
- **The runtime output-key collision guard is a structural no-op for all 18 nats-subject
  lenses**: `detectOutputKeyCollision` keys on `Keys["key"]` (`pipeline/evaluate.go:322`); a
  personal lens's keys are `{__actor, ns, entityId}` — the guard `continue`s on every row.
- **Fixing that guard naively would terminal-fail six live lenses, not one.** The pkgmgr walk
  prelude compiles every chain clause as `OPTIONAL MATCH` (`anchorwalk.go:71,:340`) and several
  tails drop path variables through a **non-DISTINCT `WITH`** (`executor.go:749-762` iterates
  bindings 1:1): `edgeCatalog` (two `tpl`s permitting one op), `edgeStaffPanes` (one pane offered
  to two held roles), `edgeEntityProviders`, `edgeEntityStudios`/`edgeStaffWorkOrders` and
  `edgeEntitySessions` (`containedIn*0..` yielding two containers) — each emits N rows per key
  today, silently flapping under LWW; under a working guard each becomes a Terminal DLQ for that
  actor. The audit precedes the guard (§3.3).
- *(Flagged, not built on:)* the 0.14.4 commit's stated mechanism ("a comprehension sees only
  walk-loaded edges") does not match the executor — comprehensions issue live adjacency reads
  (`executor.go:1411→:602`). The fix was coincidental; nothing here cites that explanation.

## 2. Grounding — why the platform shape is N lenses, one keyspace

- **The engine cannot union.** `UNION`/`UNWIND`/`CALL` rejected (`visitor.go:71-73,:146,:148`);
  `OPTIONAL MATCH` cross-products (`executor.go:930-935` names the inflation);
  `collect(A)+collect(B)` unions lists inside one row, structurally unable to yield one row per
  op-meta. The package states the forced split three times (`lenses.go:60-63`, `:696-698`,
  `:919-922`); `one-bill/lenses.go:12-25` is the canonical statement; Contract #6 codifies it.
- **The overlap is ratified practice with the retraction half already shipped.** R1+R2
  (personal-lens-retraction design, ✅ 2026-07-22): every upsert and keyset frame carries `lens`
  (`natssubject.go:257,:282` — hydrate bursts too, `hydrate.go:65`); the store keeps
  `Sources map[ruleID]revision` + `frameHW` + dead-lens prune. R2's §3.3 scopes the body out
  explicitly: "**body/`Revision` LWW as today**". This design is the body half of the same shape.
  One gap the pass found: **`Delete` does not carry `lens`** (`natssubject.go:332-338`), and
  deletes still fly on this transport (hydrate misses, `reprojectActors` missing-actor) —
  §3.2 closes it additively.
- **Disjointness elsewhere is convention, largely unenforced.** `capability-kv` coexists via
  structured `OutputKeyPattern` prefixes (`cap.roles.`, `cap.svc.`, `cap.ephemeral.`,
  `cap-read.<domain>.` — all struct fields); `weaver-targets` carries 14 lenses under
  `<targetId>.` prefixes (Contract #10 §10.2's own word: "disjoint") — **nothing checks targetId
  uniqueness across packages**; `actor_read_grants` is disjoint by `grant_source`, the one
  *enforced* boundary (`read_path_adapters.go:101-106`) — but **nothing rejects two producers
  declaring the same `grant_source`** (`GrantSource` is a struct field, `definition.go:835-844`,
  so the check is a five-line addition). `validateAll` runs nine checks
  (`definition.go:35-49`); none compares key spaces; the only cross-package check is
  canonicalName collision (`installer.go:618-640`).
- **What a build-time guard can and cannot see (adversarial finding):** `OutputKeyPattern`,
  `GrantSource`, `Bucket`, `SubjectPrefix`+`Stream`, `IntoKey`, and — for every pkgmgr
  walk-lens — `AnchorWalk.AnchorType` are **struct fields**. But `one-bill`'s two lenses carry
  **no IntoKey at all** (key = the cypher alias `t.key AS key`; disjointness lives in the MATCH
  labels), and `capabilityRoleIndex`'s `cap.role-by-operation.` prefix exists only in envelope
  code — pkgmgr has **no cypher machinery** (`Spec` passes verbatim into `lensSpecBody`,
  `build.go:478`). A guard whose comparable needs cypher literals would refuse both — making two
  shipped packages uninstallable. §3.2 therefore scopes v1 to struct-derivable comparables.

## 3. The shape (build order per the adversarial pass: A3a → A1 → A2; A3b last)

### 3.1 Fire A1 — per-source bodies + write-time-materialized merge (Edge store; completes R2)

`store.Entry` evolves: alongside `Sources[ruleID] = revision` (shipped), each source keeps its
**own body slice**. `Entry.Data` remains a **stored field** — the merged view, **re-materialized
at write time** whenever any slice changes: shallow column overlay in ascending per-source
revision order; a column only one source projects always survives. Because `Data` stays stored:

- **Every existing reader stays correct with zero changes** — the optimistic overlay
  (`overlay.go:116` returns `confirmed.Data`), `cmd/facet` feed/snapshot frames,
  `Host.Snapshot`, and the prune paths that read-and-`putEntry` whole entries
  (`bolt.go:230-263`, `:274-312`) all see the merged truth; re-putting an entry is harmless
  (slices are the source of truth; `Data` is derived-but-stored). This was the pass's must-fix
  #4: a read-time-synthesized merge had a corruption path through exactly those prune re-puts.
- **`Entry.Revision` = max over source revisions.** It advances on every applied slice write —
  including one that loses a shared-column overlay — so the overlay supersession test
  (`overlay.go:108`) keeps its meaning: pending intents retire against any visible change.
- **Notification semantics are redefined view-scoped — a breaking conformance change, owned
  explicitly** (the pass's blocker #1): `applied` becomes *"the materialized `Data` (or
  tombstone state) changed"*, and `ApplyKeySet`/`PruneDeadLensAttributions` report a pruned key
  whenever dropping a source's slice changed the merged view, not only when `Sources` empties.
  Without this, a slice write that loses body-LWW fires no `OnChange`/SSE and the browser
  renders a mirror that silently diverged. The three pinned conformance assertions
  (`storetest/conformance.go:409,:425,:480,:524`) and `TestManager_Handle_OnChangeFiresOnlyOnApplied`
  are **updated as part of this fire** — they pin the old body-LWW world; the new pins assert
  view-scoped semantics.
- **Retraction composes, now including deletes:** a keyset frame or lens-attributed delete drops
  that source's slice + attribution and re-materializes; `Sources` emptying tombstones (clearing
  `Data` — the R2 hygiene rule). A **lens-less** delete (legacy) keeps today's full-wipe
  semantics.
- **Overlay-order honesty** (pass must-fix #8): ascending per-source revision is a *best-effort
  freshness ranking*, not a proof — a hydrate burst deliberately under-stamps (its revision is a
  pre-captured floor, `hydrate.go:22-36`), so a shared column can transiently rank a hydrate
  slice below an older live delta. The *guarantee* is unique-column survival (`viaRole` never
  blanks again); shared columns converge because both lenses re-derive from the same graph —
  set-level for `viaServices`, whose two derivations are set-equal but not order-equal
  (comprehension vs `collect(DISTINCT …)`; the sole consumer is an `.includes()`,
  `app.js:748`).
- **Migration:** store schema version bump → purge + cold hydrate (R2's documented
  disposable-mirror posture; both engines + conformance harness).

**Server-side change (additive):** `Delete` gains `Lens: a.ruleID` (`natssubject.go:332-338`) so
the store can retract per-source; old clients ignore unknown fields (the R1 compatibility
posture, pinned in `sync.go:406-408`).

### 3.2 Fire A2 — the keyspace guard (default-deny undeclared overlap, struct-derivable v1)

**Comparable (v1 — only fields pkgmgr already holds):** container = `Bucket` /
table+`GrantSource` / (`Stream`, `SubjectPrefix`); pattern = `OutputKeyPattern` when present,
else (`IntoKey` shape + `AnchorWalk.AnchorType`) for walk-lenses. **No cypher parsing** — the
pass proved a literal-extractor is new machinery and its absence must not brick installs.

Rules, enforced installer-side (predict) + Refractor activation (authority — the `reloadpin`
pattern; later-arriving spec fails closed with a health error naming both lenses, incumbent
keeps serving):

1. **Provably disjoint → pass.** Different containers; disjoint `OutputKeyPattern` /
   `<targetId>.` literals (now checked **cross-package** — closing §10.2's unchecked
   convention); same container + same IntoKey shape but **different `AnchorWalk.AnchorType`**
   (key columns are `nanoIdFromKey(anchor.key)` — disjoint under the platform's standing
   NanoID-uniqueness assumption, the same one Contract #1 addressing rests on; stated as that
   assumption, not as structure).
2. **Same container + equal comparable, undeclared → refuse.** On the live tree that is exactly
   the three edge-manifest pairs (meta/meta, task/task, session/session) — the census's 3, no
   more (the nine `manifest.ent` lenses differ by AnchorType and auto-pass).
3. **Declared (`sharesKeyspaceWith`, mutual) → legal.** On **nats-subject** (attributed
   transport, post-A1) the declaration marks a sound merge. On **nats-kv/postgres** it is an
   author-asserted row-disjointness statement — formalizing exactly the hand assertion
   `one-bill/lenses.go:12-25` already makes in prose — with LWW as the consequence of a wrong
   assertion (today's behavior, now signed). Duplicate `GrantSource` across producers → refuse
   (no declaration — one source string is one producer's identity).
4. **Spec-only lenses (no IntoKey, no walk — `one-bill`, `capabilityRoleIndex`) are v1
   out-of-scope, by name**: their comparable is cypher-resident; refusing on indeterminacy
   would make both uninstallable (pass blocker #2). The named follow-on — a targeted
   literal-extractor (`"…" AS <keyColumn>` + anchor label) — is filed as the guard's v2 with
   these two as its consumers; until then they stay exactly as guarded as today (i.e. by
   comment).

**Migration-then-gate, no warn-first:** the three pairs gain declarations in the same fire
(edge-manifest bump); the tree is then clean and the gate lands blocking.

### 3.3 Fire A3 — the tails audit (A3a) then the runtime guard fix (A3b)

- **A3a (edge-manifest package fire):** audit all 18 tails for multi-row-per-key emissions and
  fix each — the roles tail aggregates roles per op as **a single
  `collect(DISTINCT {key: role.key, name: role.canonicalName.data.value}) AS viaRoles`** (map
  literal, the in-package precedent at `lenses.go:544` — two parallel collects would misalign
  when a role lacks a name, since `collect` drops nulls, `executor.go:945-955`); the
  cross-product tails (`edgeCatalog`, `edgeStaffPanes`, `edgeEntityProviders`,
  `edgeEntityStudios`, `edgeStaffWorkOrders`, `edgeEntitySessions`) get `WITH DISTINCT` /
  aggregation so one key = one row. `app.js` grouping consumes the `viaRoles` list (an op
  granted via two hats appears under each; the only grouping site is `app.js:769-784`).
- **A3b (platform, after A3a):** `detectOutputKeyCollision` builds the composite key from the
  adapter's `keyOrder` instead of `Keys["key"]` — the intra-lens duplicate-output guard starts
  firing for nats-subject lenses, now against a clean corpus. Ordering is load-bearing: the
  pass showed six lenses would terminal-fail per-actor if the guard landed first.

## 4. Contract surface

None frozen. Contract #6's "additional Lenses" policy is affirmed and given its missing
arbitration; #10 §10.2's "disjoint `<targetId>.` prefix" becomes checked; `sharesKeyspaceWith`
is `LensSpec`-level; the Delta `Delete.lens` field is wire-additive under the R1 unknown-field
posture. If ratification wants the declaration named in a contract, the natural home is one
line in #10 §10.2 — flagged, not staged (no frozen text is contradicted).

## 5. Reconciliation with the existing mental model

- **"Didn't R2 already solve same-key multi-lens?"** For retraction, yes; its own §3.3 left the
  body single-winner. Same store, same harness, same migration posture — this is the second
  half, plus the notification redefinition R2 didn't need (its refcount never changed the view).
- **"Why not one lens?"** The engine cannot express it (§2); the split is codified policy.
- **"Does this violate overwrite-by-reprojection?"** No — it holds **per source**: each lens
  still blindly overwrites its own slice; no read-modify-write enters the server write path.
  The merge is a client-side materialization at the mirror, the transport's existing
  materialization point.
- **"New state?"** Client-side only: per-source bodies where R2 already keeps per-source
  revisions. Server keeps zero new state.
- **In-flight overlap check:** the evaluation-consistency design (same fire day) touches the
  evaluate-emit seam; this touches store/guard — no shared mechanism. Both add a `validateAll`
  check (this one) / an executor memo (that one) — disjoint.

## 6. Alternatives considered

1. **Server-side read-modify-write column merge** — rejected: violates the binding
   overwrite-by-reprojection principle (a fabricated/stale column would survive reprojection);
   CAS loops on a hot shared key; server-side retraction of a dropped column is undecidable
   (upsert-only writers never see the old key).
2. **Engine `UNION`** — rejected: XL (grammar, visitor, executor, cardinality) for one consumer,
   against Contract #6's policy; does nothing for legitimately-separate pairs (tasks/queued).
3. **Key-splitting per lens + client join** — rejected: churns the manifest key contract every
   Edge consumer reads; re-implements the merge in every consumer instead of once at the store.
4. **Priority/ownership (one lens wins)** — rejected: loses `viaRole` permanently; a quieter
   clobber.
5. **Prohibition** (my own first instinct) — rejected by grounding: the idiom is the only
   expressible union and is ratified practice; the defect is missing arbitration + a missing
   gate.
6. **Byte-identical-RETURN exemption in the guard** — dropped: textual identity is the
   hand-maintained coincidence the census flags; under A1 a declared pair is sound regardless.
7. **Server-side column-union of the catalog tails** (the pass's strongest challenge — extend
   0.14.4 to *all* columns: the service tail gains the role `OPTIONAL MATCH`es, both tails emit
   byte-identical bodies, LWW becomes a no-op; **verified buildable**: `identity` is in the
   generated prelude's scope, OPTIONAL-MATCH null-restore preserves the row, the D1 gate is
   per-anchor and unaffected). Rejected as the *mechanism*, on three grounds the pass itself
   surfaced: it needs A3a's aggregation fix anyway (the added OPTIONAL MATCH cross-products);
   it hand-aligns O(paths²) columns forever — the sessions-pair "textual coincidence" restated
   at scale, with no gate when a column is added; and **it actively worsens the tasks pair** —
   `edgeTasksQueued` would start projecting `assignee = null`, an *active clobber* of the
   claimant's value where today there is only an omission (A1 turns that omission into
   soundness; column-union turns it into a new defect). Kept on record as the tactical fallback
   if A1's store work stalls: catalog-pair-only, explicitly temporary.

## 7. Test strategy

- **Store conformance (both engines):** unique-column survival across projection-order
  permutations; overlay order (staler slice landing after fresher leaves fresher's shared
  columns); **view-scoped `applied`** (a body-LWW-losing slice write that changes the merge
  reports applied + fires `OnChange`; one that doesn't change the merge reports not-applied);
  keyset frame dropping one source reports the key pruned iff the view changed; lens-attributed
  delete removes one slice; lens-less delete full-wipes; `Sources`-empty tombstone clears
  `Data` and every slice; dead-lens prune drops slices; schema-mismatch purge; `Revision` =
  max(sources) and overlay supersession retires against attribution-only view changes.
- **Guard vectors:** intra-package undeclared equal comparable refused naming both lenses;
  cross-package refusal at install; declared pair passes (both transports, both meanings);
  duplicate `GrantSource` refused; disjoint `OutputKeyPattern`/targetId auto-pass incl.
  cross-package; AnchorType-disjoint auto-pass; spec-only lenses (one-bill shape) untouched —
  install stays green; activation backstop fails the later spec closed, incumbent serving.
- **E2E (ephemeral stack + facet):** dual-hat actor — `manifest.op.*` rows carry `viaServices`
  *and* `viaRoles` regardless of projection order (drive both orders via targeted reprojects);
  claim beat under churn converges to non-claimable on the claimant's mirror (extends R2's §5
  vector); hydrate-then-live-delta shared-column convergence.
- **Regression:** full edge conformance + `make test-edge-idb-conformance` green.

## 8. Decomposition, risks

- **Fire A3a (S–M):** 18-tail audit + fixes + `app.js` viaRoles grouping (edge-manifest bump).
- **Fire A1 (M):** store schema + write-time merge + **notification redefinition + conformance
  re-pin** + `Delete.lens` (server, additive) + migration bump.
- **Fire A2 (M):** struct-derivable comparable + `validateLensKeySpaces` + installer sibling +
  activation backstop + `sharesKeyspaceWith` + GrantSource-dup refusal + the three
  edge-manifest declarations (migration first, gate blocking).
- **Fire A3b (XS, after A3a):** `detectOutputKeyCollision` keyOrder fix.
- **Risks:** the conformance re-pin is a deliberate semantic break of R2-era assertions — owned
  in one fire, never straddled; view-materialization cost per slice write (bounded: ≤ source
  count per key, in practice 2); guard v1's spec-only blind spot is *named* (one-bill,
  role-index) with the v2 extractor as its filed follow-on, not silently absorbed; the one-time
  hydration burst at schema bump (R2's rollout note applies).

## 9. Adversarial pass — run, findings folded

Independent read-only reviewer, this fire. Verdict on the first draft: *"directionally sound,
not sound as written"* — accepted; §3 was re-derived and resequenced:

- **#1 (blocker):** `applied`/`prunedKeys` are body-LWW-scoped and conformance-pinned — a
  merged view would change invisibly (no `OnChange`/SSE) → §3.1 redefines notification
  view-scoped and re-pins conformance in the same fire.
- **#2 (blocker):** the comparable is not derivable for spec-only lenses — as drafted,
  `one-bill` and `capabilityRoleIndex` became uninstallable → §3.2 scopes v1 to
  struct-derivable comparables; spec-only lenses named out-of-scope with the v2 extractor
  filed.
- **#3 (blocker):** the guard fix would terminal-fail six lenses (non-DISTINCT `WITH` over
  OPTIONAL-MATCH cross-products), not one → A3 split into A3a (18-tail audit) → A3b (guard),
  in that order.
- **#4:** read-time merge had a corruption path through prune re-puts + the overlay reading raw
  `Data` → write-time materialization; `Data` stays stored; `Revision` = max(sources).
- **#5:** two parallel `collect(DISTINCT …)`s misalign when a role lacks a name (collect drops
  nulls) → map-literal collect, precedent `lenses.go:544`.
- **#6:** the ancestor-prefix "pass" rule misread the sweep doc (the ancestor case is why the
  prefix *cannot* be trusted) → the rule is gone; v1's scope makes it unnecessary (kernel
  lenses are outside pkgmgr's guard; the four cap-prefix `OutputKeyPattern`s are pairwise
  disjoint literals).
- **#7:** `Delete` carries no `lens` and still flies on this transport — a lens-less delete
  would wipe the other source's slice → additive `Delete.lens` + per-source delete semantics;
  legacy lens-less deletes keep full-wipe.
- **#8:** hydrate's deliberately-floored revision breaks "ranks fresher graph state" →
  overlay order restated as best-effort; unique-column survival is the guarantee; shared-column
  convergence is set-level (`viaServices` orderings differ by construction).
- Census sharpened: `manifest.ent` is nine lenses (35 auto-pass pairs under AnchorType
  disjointness — resting on the platform's standing NanoID-uniqueness assumption, stated as
  such); `GrantSource` dup-check is a five-line addition, not machinery.
- **Strongest challenge:** the server-side column-union alternative — verified buildable by the
  pass, added as §6.7 with the explicit rejection (needs A3a anyway; O(paths²) hand alignment;
  actively worsens the tasks pair).
