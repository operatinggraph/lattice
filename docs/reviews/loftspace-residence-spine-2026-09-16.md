# LoftSpace — an approved lease moves the tenant in (2026-09-16)

**Filed (PO, 2026-09-16, `16af6c9d`):** "`DecideLeaseApplication` records `.tenancy` and `EndTenancy` ends it, but neither
wires nor unwires `residesIn` — the residence spine only the seed and `operator`-only `WireResidesIn` write, the left edge
of `cap.svc.<actor>` and every whoami anchor. Live: Jordan Ellis and Priya Raman have none; seed-wired Riley and Sam reach
Maple Laundry, they don't. A convergence target wires at approval, unwires at `endedAt`." ★★ M, pkg. Winston-adjudicated
(implementation-level: every mechanism is package-owned and mirrors a shipped pattern — the cross-package Weaver `directOp`
under the service actor, the `tenancyEnd` gap shape, the `(e)` enumeration + explicit-CAS revive).

## Grounding

- **The spine and its readers.** `residesIn` is `lnk.identity.<id>.residesIn.unit.<unitId>` — the unit itself is the
  location (`seedTenant`, [seed-showcase.go:549-566](../../scripts/seed-showcase.go)); live: 2 links, both seed-wired.
  `capabilityServiceAccess` walks `(identity)-[:residesIn]->(loc0)-[:containedIn*0..]->(loc)<-[:availableAt]-(svc)`
  ([service-location/lenses.go:169](../../packages/service-location/lenses.go)); identity-domain's whoami and edge-manifest's
  `chainResidence` project the same link. Nothing lease-side writes it.
- **The ops.** `WireResidesIn{identity, location}` — Reads both endpoints, the deterministic link key as an OptionalRead;
  alive → no-op, absent → create, tombstoned → revive as an update ([ddls.go:325-344](../../packages/service-location/ddls.go)).
  A submitter that does NOT declare the link key "would emit a create for a tombstoned link, which cannot commit"
  (the create-once collision, [commit_path.go:463](../../internal/processor/commit_path.go)). `UnwireResidesIn{linkKey}` —
  Reads the link key; `UnknownLink` when absent or dead ([ddls.go:346-350](../../packages/service-location/ddls.go)). Both
  `operator`-granted, scope any ([permissions.go:24](../../packages/service-location/permissions.go)) — the grant Weaver's
  service actor holds, the way `SetListingStatus` is dispatched cross-package today
  ([targets.go:139](../../packages/lease-signing/targets.go), [tenancy_end_targets.go:52](../../packages/lease-signing/tenancy_end_targets.go)).
- **The rows that already know the applicant and the unit.** `leaseApplicationComplete` projects `applicant` (`id.key`)
  and `unitKey`, opens `missing_listingLeased` at approval ([lenses.go:1090-1202](../../packages/lease-signing/lenses.go));
  `tenancyEnd` projects `endedAt`, `unitKey` and the `otherLiveTenancyCount` guard that keeps a terminal row terminal
  ([tenancy_end_lenses.go:170-201](../../packages/lease-signing/tenancy_end_lenses.go)). A relationship variable's `.key`
  projects (`own.key AS instanceOfLink`, [lenses.go:1079](../../packages/lease-signing/lenses.go)); the full engine has no
  string concatenation ([augur/lenses.go:76](../../packages/augur/lenses.go)) and filters dead links on every read
  ([executor.go:1094](../../internal/refractor/ruleengine/full/executor.go)) — a lens can name a LIVE link's key, never a
  tombstoned one.
- **Weaver's declaration grammar.** `row.<col>` resolves any row column into Params / Reads; a nullable column in
  `OptionalReads` is dropped, in `Params`/`Reads` it is a dispatch refusal
  ([strategist.go:356-380](../../internal/weaver/strategist.go)); `Enumerations` declare a script's `(e)` walks
  ([definition.go:524](../../internal/pkgmgr/definition.go)). `kv.Links` pages carry tombstoned links with `.isDeleted`
  and `.revision` ([starlark_kv.go:308-317](../../internal/processor/starlark_kv.go)); a bare update off a page entry is
  the `lint-live-read-pinned-mutation` race, the pinned idiom is `expectedRevision: lk.revision`.
- **Install chains.** service-location is installed by `install-edge-manifest` and its own verify target, not by
  `install-loftspace` / `refresh-loftspace` / `verify-package-lease-signing` (Makefile 1274-1290, 1744-1760, 616-628); the
  e2e harness chain ends `service-domain → lease-signing` ([harness_test.go:385-398](../../internal/leaseconvergence/harness_test.go))
  and `drainUntilConverged` waits for `violating = false`. Live, the package is installed (`cap.svc.*` exists).
- **`ReassignLeaseUnit` moves a live application — an approved one included** ([scripts.go:1193](../../packages/lease-signing/scripts.go)).

## Decisions (Winston)

1. **Two leaseapp-anchored gaps, not an identity-anchored sweep.** The fact is the APPLICATION's: its approval confers the
   residence at its unit, its end releases it. An identity-anchored "release every residence with no live lease" would strip
   `seed-edge-demo`'s residents (wired with no leaseapp) and the showcase residents between `seedTenant` and
   `seedResidentTenancies` — residence is also an operator-provisioned fact. The two are one link on one key, so the
   lease's end releases the residence at ITS unit whoever wired it (Riley and Sam's seed-wired links go when their
   backfilled leases end; a reseed re-wires them) — accepted: a residence at a leased unit is the lease's.
2. **`missing_residence` on `leaseApplicationComplete`** — `(unitKey <> null) AND (applicant <> null) AND
   (landlordDecision = 'approved') AND (leaseEnd <> null) AND (tenancyEndedAt = null) AND (residenceCount = 0)`, where
   `residenceCount = count(DISTINCT res.key)` over the closed loop
   `OPTIONAL MATCH (app)-[:applicationFor]->(resId:identity)-[res:residesIn]->(resU:unit)<-[:appliesToUnit]-(app)` (fresh
   variables: a clause naming both `id` and `u` spans two sibling subtrees and the branch decomposer refuses the whole
   stage; the loop is its own group); joins `violating`. Dispatch:
   `directOp WireResidesIn{identity: row.applicant, location: row.unitKey}`, `Reads: [row.applicant, row.unitKey]`,
   `Enumerations: [{Hub: row.applicant, Relation: residesIn, Direction: out}]`. "At approval" is the PO's ask verbatim —
   a future-dated lease confers service access from approval, the same instant the listing flips to leased.
3. **`missing_residenceUnwired` on `tenancyEnd`** — `(endedAt <> null) AND (residenceLinkKey <> null) AND
   (sameApplicantLiveTenancyCount = 0)`, with `residenceLinkKey = res.key` (bare — the rel-binding gate does not
   recognise `max()`; one live link per (identity, unit) by the deterministic key) over the same closed loop as decision 2,
   and `sameApplicantLiveTenancyCount` the existing `otherLiveTenancyCount` CASE conjoined with `otherId.key <> null` over
   `OPTIONAL MATCH (other)-[:applicationFor]->(otherId:identity)<-[:applicationFor]-(app)` — `otherId` binds iff the other
   application's applicant is this one's. Dispatch: `directOp UnwireResidesIn{linkKey:
   row.residenceLinkKey}`, `Reads: [row.residenceLinkKey]`. The same-applicant guard is load-bearing exactly as the relist
   guard is: an ended application on a unit the same person re-leases would otherwise unwire what the new application's
   `missing_residence` re-wires, forever.
4. **`WireResidesIn` revives a tombstoned link its submitter did not declare** (service-location 0.5.4 → 0.6.0). When the link
   key is not in `state`, the script enumerates the identity's own `residesIn` links (`# read-posture: (e)`, one page,
   `RESIDES_IN_PAGE_LIMIT`), and a matching entry that is alive → no-op, dead → revive with `expectedRevision: lk.revision`;
   no entry → create as today. Why here: no lens can project a dead link's key (grounding), the Weaver composes no link
   keys, and a move-back-in to the same unit is the documented revive case — without this, the second approval's dispatch
   is a create-once collision on every redelivery. The page is listed whenever the snapshot lacks the key — undeclared
   OR declared-and-absent (a known-absent optional read is never in `state`) — so every first wire walks one page (≈51
   budget units of 60,000) and only a declared re-wire skips it; the walk is bounded at `RESIDES_IN_MAX_PAGES × RESIDES_IN_PAGE_LIMIT` (200)
   subjects; past it the script emits the create (an absent target commits, a tombstoned one conflicts by name) — a
   declared first wire is never refused by the walk. Every dispatcher declares the walk
   (`Enumerations` on the target; the seeds' and the verify script's `wireHint` for `residesIn`).
5. **service-location joins the LoftSpace chains** — `install-loftspace`, `refresh-loftspace`, `verify-package-lease-signing`
   (after service-domain, before lease-signing) and the e2e harness's `installChain`. A chain that installs a target
   dispatching an op installs the package that owns the op; the precedent (`SetListingStatus`) declares no `Depends` edge and
   neither does this. `install-cafe` / `install-wellness` install lease-signing without loftspace-domain or service-location
   today — the same dangling shape for `SetListingStatus`, left as is.
6. **Non-goals.** `ReassignLeaseUnit` of an approved lease leaves the OLD unit's residence wired (the new unit's opens
   `missing_residence`); it is an operator repair verb and the operator's `UnwireResidesIn` is the release — stated in the
   op's DDL prose, not automated (an identity-anchored release is decision 1's rejected shape); the mirror corner — an
   ENDED lease reassigned onto a unit the applicant is operator-wired to projects that link as `residenceLinkKey` and
   releases it — is the same accepted rule as decision 1's. `UnwireResidesIn` keeps its `UnknownLink` refusal on a dead
   link: in the projection-lag window a reclaim re-dispatch burns one refusal, then converges. No FE: whoami anchors and
   the service list render the spine from the identity-domain / service-location lenses already. No change to who may
   submit `WireResidesIn` by hand (operator only).

## Build

**Scope sentence.** A convergence target wires `residesIn` at approval and unwires it at `endedAt`; Jordan Ellis and
Priya Raman reach Maple Laundry live.

**Touch-list (verified 2026-09-16).** `packages/lease-signing/lenses.go:1090-1202` (leaseApplicationComplete) ·
`targets.go:139-150` (gap map) · `tenancy_end_lenses.go:170-201` · `tenancy_end_targets.go:40-56` · `package.go:117` +
`manifest.yaml` (0.42.0) · `lens_unit_test.go:20,268` (column↔gap bijection pins) · new lens/target tests beside
`tenancy_end_lens_test.go` · `packages/service-location/ddls.go:325-344,353-366` + `package.go:52` + `manifest.yaml` (0.6.0)
+ a revive test in `package_test.go` / `integration_test.go` · `internal/leaseconvergence/harness_test.go:385-398` +
`tenancy_end_convergence_test.go` (e2e: link alive after approval, tombstoned after the end) · `Makefile:1274-1290,
1744-1760, 616-628`.

**Precedents.** Gap + dispatch: `missing_relist` / `missing_listingLeased`. Guard: `otherLiveTenancyCount`. Link-key
column: `own.key AS instanceOfLink`. `(e)` enumeration + revive: `lease-signing/scripts.go:774` (annotation + page constant),
`identity-domain/ddls.go:2051` (`expectedRevision: lk.revision`). Enumerations declaration: `lease-signing/targets.go:148`.

**Increments.** (1) `tenancyEnd` lens + target + tests. (2) `leaseApplicationComplete` lens + target + tests; version
0.42.0. (3) service-location revive + test; version 0.6.0. (4) harness chain + e2e; Makefile chains. Green:
`go test ./packages/lease-signing/ ./packages/service-location/ ./internal/refractor/ -run 'Corpus|Census|.'`,
`make test-lease-convergence`, `STRICT=1` on `lint-conventions`, `lint-gap-column-declaration`, `lint-links-page-limit`,
`lint-live-read-pinned-mutation`, `lint-package-version`, `lint-lens-anchors`, `lint-seed-declared-reads`; then the live
rollout (`refresh-loftspace` + `reinstall-package` service-location, `lattice lens reproject` the approved anchors whose
rows do not violate today, `cap.svc.<jordan>` lists Maple Laundry).

**Gotchas (dossier, part 5).** *A dispatch declaration must name what the runtime binds* — every templated column is a
conjunct of its own gap (`applicant <> null`, `unitKey <> null`, `residenceLinkKey <> null`). *A consumer's exclusion is
grounded in the predicate that enforces it* — the same-applicant guard is `otherLiveTenancyCount`'s CASE verbatim plus the
identity equality. *For every ARM of the op pin the gap FALSE over the state it leaves* — wired → `missing_residence` false;
tombstoned → the OPTIONAL MATCH misses → `missing_residenceUnwired` false; a re-approval on the same unit → revive → false.
*A lens MATCH edit is a corpus edit* — run the refractor corpus census. *A mirror that drops a precedent's branch drops its
invariant* — the revive keeps `wire()`'s three-state table; the enumeration adds a fourth source of the same table, never a
new branch. Standing checklist items 1–6 (`agents/fire-brief-template.md`) walked.

**Build note (2026-09-17).** Two shape substitutions the engine forced, both provably equivalent: the residence walk
is a closed loop on the anchor, `(app)-[:applicationFor]->(resId:identity)-[res:residesIn]->(resU:unit)<-[:appliesToUnit]-(app)`
(a clause naming both `id` and `u` spans two sibling subtrees and the branch decomposer refuses the whole stage —
leaseApplicationComplete folded 6 → 0 until the loop rooted the walk as its own group: now 7); `residenceLinkKey` is
`res.key` bare (the rel-binding gate does not recognise `max()`), one live link per (identity, unit) by the deterministic
key; the same-applicant guard closes `(other)-[:applicationFor]->(otherId)<-[:applicationFor]-(app)`. Priced: `tenancyEnd`'s
CDC filter drops from relation-narrowed to label-narrowed (36 subjects > the budget; `leaseExpiry`'s posture). Found and
not this fire's: the `(applicant, unit)` guard link is freed only by withdraw and reassign, so a declined, lost or ended
applicant can never re-apply to that unit — `sameApplicantLiveTenancyCount` is reachable only through `ReassignLeaseUnit`
today; filed as its own row. `TestRenewalConvergence_ExternalLegReclaimsAfterAFailedCheck` reddened twice under the two
builders' concurrent suites, green alone (third sighting; 2026-09-13 was the second).

**Shipped `a8299552` (merge of `f8c52ba1`), live 2026-09-17.** `reinstall-package` both packages (service-location
0.5.4 → 0.6.0, lease-signing 0.41.2 → 0.42.0, no restart); the reactivation re-evaluated every retained row and Weaver wired
the five missing residences within a minute (Jordan Ellis's link `createdBy` Weaver's service actor at 07:24:55Z, 55 s
after the upgrade); the one un-wired approved application is tombstoned (withdrawn). `cap.svc.identity.<jordan>` now
lists the same five services as seed-wired Riley Chen. `lattice lens reproject Yn698BZWmaqJuBHuYn69 --actor-key …` takes
the lens's bare NanoID (a 5-token control subject), not its canonical name or `vtx.meta.` key. **Review classification
(three cold layers, no BLOCKING):** convention ×3 (prior-attempt narration in comments; README approved bullet; a test
comment's wrong mechanism), brief-gap ×2 (wrong-unit and tombstoned-link vectors; the relist e2e's steady-state leg
ungated on the new dispatch — a new gap column joins `violating`, so every e2e that waits on that row's convergence now
waits on the new dispatch), design-gap ×2 (decision 4's "declaring submitters skip the walk" — a snapshot cannot tell
declared-absent from undeclared, now a `_packages.md` dossier sighting; the unwire strips a seed-wired residence at a leased
unit — accepted into decision 1), implementation ×1 (single-page walk → bounded paging with the create fallback).

**Scope-diff.** Every touch traces to the scope sentence; the service-location revive is the one addition the ask does
not name, and it is what makes the wire re-runnable (decision 4) — an increment of the same mechanism, not an adjacent one.
