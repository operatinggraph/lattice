# Read-grant single-source walk — one `AnchorWalk` declaration compiles both enumerations (dual-enumeration S2)

**Status: ✅ Andrew-ratified (2026-07-24) — build-ready as ONE L fire** · Designer fire 2026-07-23 ·
Lattice lane (backlog row: *Read-grant/lens dual-enumeration footgun*, S2)

**Ratification decisions.** Ratified as designed, with one change: the Fire 1 / Fire 2 split is
**collapsed into a single L fire** (§8) — the primitive's only consumer is the migration, so the
inert-compiler window is removed rather than merely bounded. No fork, no contract change. Session due
diligence re-verified every citation independently against code (the three producers, the
character-identical appointment chain at `lenses.go:774`/`:943`, the four-walk resident fan-out, the
audit-only `via`, the dormant `EmptyBehavior:"delete"`, and the S1 base the flip depends on) — no
drift; one stale neighbour pointer in §2 corrected (the `manifest.me` gap closed 2026-07-24,
`6aa4959c`).

## For Andrew (as presented at ratification)

Two lines: every non-self-anchored Personal lens states its actor→anchor reachability walk **twice**
— once in the data-lens cypher, once in a hand-authored `actorAggregate` cap-read producer — and drift
is silent (fail-closed row drops, the realized Fire-1 "only `manifest.me` ever reached a tenant" bug; or
an over-grant in the other direction). S2 adds one declarative `AnchorWalk` field to the existing
`pkgmgr.LensSpec`; pkgmgr **compiles both artifacts from it** (the data lens's reachability prefix + the
whole grant producer), so the walk exists exactly once — while the **runtime stays two independent
enumerations** (D1's fail-closed gate is untouched).

- **No architectural fork.** This applies the already-ratified D1/§6.14 plane; no Gateway / Vault /
  multi-cell / HA-NATS surface is touched.
- **No frozen-contract change.** Build-to: the compiled outputs are the already-frozen shapes —
  two distinct `meta.lens` vertices (Contract #6 §6.1 "additional Lenses, not Lens-internal
  complexity"), the §6.14 `cap-read.<domain>.<actorSuffix>` / `readableAnchors[]` doc, the §6.13
  `actorAggregate` Output descriptor. The `AnchorWalk` field lives on the internal Go
  `pkgmgr.LensSpec`, which is not a frozen shape.
- **Two deliberate observable changes** (both improvements, called out so they aren't mistaken for
  drift; both pinned by the §6 migration proof):
  1. Each grant entry's `via:[...]` is derived from the declared chain (full relation list, in
     order). Today's hand-typed `via` arrays are inconsistent (resident `service` says
     `['availableAt']`, omitting `residesIn`/`containedIn`; staff `workorder` spells the full
     chain). `via` is audit-only — `capabilityread.IsReadable` matches NanoID-to-NanoID and never
     reads it (`internal/refractor/capabilityread/capabilityread.go:33-35`).
  2. Generated producers declare the §6.13 realness filter on `readableAnchors`' `anchorId`, so
     `EmptyBehavior: "delete"` actually fires for a binding-less identity. Today none of the three
     producers declares one, so the driver's empty-delete path never runs
     (`internal/refractor/projection/driver.go:99`) and the all-OPTIONAL resident producer leaves a
     placeholder-only doc per binding-less identity. Post-S2 those docs are deleted — fewer
     per-row GETs in `IsReadable`'s union, no security change (a null `anchorId` never matched,
     `capabilityread.go:104-106`).
- **Adversarial pass:** run this fire, findings folded (§9) — this design's own pre-build gate is
  discharged.

## 1. Problem + intent

`packages/edge-manifest` ships 14 Personal (`nats-subject`) lenses; 13 are non-self-anchored — each
`RETURN <var>.key AS anchor` binds a vertex *other* than the recipient identity (service, meta, task,
session, provider, booking, workorder, appointment). Refractor's D1 gate
(`internal/refractor/projection/personal.go:147` → `capabilityread.IsReadable`) drops any projected row
whose anchor NanoID is absent from the actor's unioned `cap-read.*` slices — silently, by design
(fail-closed, Contract #6 §6.14). The grant side is three hand-authored `actorAggregate` producers
(`edgeManifestReadGrants` / `…StaffReadGrants` / `…ProviderReadGrants`,
`packages/edge-manifest/lenses.go:864-952`) whose `OPTIONAL MATCH` branches **restate the same walks**
— e.g. `(identity)<-[:identifiedBy]-(pr:provider)<-[:withProvider]-(appt:appointment)` appears
character-identical at lenses.go:774 (data) and lenses.go:943 (grant).

Hand-maintaining the same walk in two places is the footgun:

- **Drift → silent row drops.** Realized once already: Fire 1 shipped the five manifest lenses without
  the grant half, and only self-anchored `edgeIdentity` ever reached a live tenant
  (`scripts/lint-lens-anchors.go:1-30` records it as the motivating bug).
- **Drift → over-grant.** A producer branch wider than its lens's walk grants anchors the lens never
  projects — invisible to any user-facing symptom.
- Every new non-self lens pays a dual-author + dual-review tax, plus a third hand-typed copy (the
  `via` audit array).

**S1 (shipped)** detects drift: the `lint-lens-anchors` CI gate (structural: every projected anchor
kind has a producer branch, `ci.yml:112`) and the coverage testkit
(`packages/edge-manifest/coverage_proof_test.go`: projected anchors ⊆ granted anchors over three
seeded persona worlds, non-vacuous). **S2 (this design) prevents it by construction:** the walk is
declared once and both enumerations are compiled from it. S1's proofs are kept and re-aimed — they now
prove the *compiler*, not the author.

## 2. Grounding — the existing pattern this extends

- **The compile-point precedent is already in pkgmgr.** `internal/pkgmgr/build.go:273-314` fans one
  `PermissionSpec` into a permission vertex + a `grantedBy` link + a `forOperation` link;
  `lensSpecBody` (`build.go:366`) compiles each `LensSpec` into the stored `.spec` aspect.
  `internal/refractor/adapter/rls.go:114` derives a Protected table's DDL + RLS policy from the lens
  spec — the codebase's stated philosophy of "derive the second artifact so the two can't drift",
  applied there to schema, applied here to the grant walk.
- **The chain text is shared between the two sides — but not uniformly as an `OPTIONAL MATCH`
  prefix.** 8 of the 13 lenses carry the chain as head `MATCH` + `OPTIONAL MATCH` clauses whose text
  the producer branch repeats; **5 fuse the chain into a *required* `MATCH` with inline `WHERE`
  filters** (`edgeTasks` :477, `edgeInstances` :505, `edgeCatalogRoles` :635, `edgeTasksQueued`
  :680, `edgeStaffWorkOrders` :736). The compiler normalizes all 13 to one shape (§3.2), and the
  migration carries a per-lens row-set equivalence proof (§6) precisely because that normalization
  is a rewrite, not a copy.
- **Grant ⊇ projection is the correct asymmetry — and its blind spot is named.** Data-side `WHERE`
  filters (e.g. `edgeTasks`' `status = "open"`) narrow *presentation*, never reachability; the grant
  covers the anchor regardless. The invariant is anchors(projected) ⊆ grants, so business filters
  stay on the data side only — which is why the *walk* (not the full data cypher) is the correct
  shared source. The flip side: **D1 cannot catch a data-side filter lost in migration** (the
  anchors are granted either way) — that hazard is carried by the §6 per-lens equivalence proof,
  not by the gate.
- **Retraction + write guard (named precisely).** A producer's output is one
  `cap-read.<domain>.<actorSuffix>` doc per actor — a **single-row overwrite** (the
  `readableAnchors` column shrinks/grows in place). Retraction of an emptied slice is
  `EmptyBehavior: "delete"` **conditional on a declared realness filter** (§For-Andrew #2; the
  design generates one). There is no composite-key row-set to shrink, so no missing-Delete
  over-grant window. The target is the NATS-KV capability bucket — CAS-guarded, the same runtime
  path the hand-authored producers use today.
- **In-flight collision check: none.** The neighbouring 📐 design at the time of writing
  (`authcontext-target-validated-primitive-design.md`, ratified 2026-07-24) is write-path guard work on
  a disjoint seam. The nearest edge-manifest neighbour — the `manifest.me` me-row reconciliation gap —
  **closed on 2026-07-24** (`6aa4959c`: the self-anchored Personal-lens reprojection chain was proven
  sound and e2e-guarded; the filed "ordering-token" mechanism was disproven, so there was no server
  bug to fix). No concurrent-fire caution remains.

## 3. The shape

### 3.1 The declaration — `AnchorWalk` on the existing `LensSpec`

```go
// internal/pkgmgr/definition.go — new field on LensSpec (nats-subject + Personal only)
Walk *AnchorWalk

// AnchorWalk declares a Personal lens's actor→anchor reachability once.
// pkgmgr compiles it into BOTH the lens's reachability prefix and the
// owning grant domain's producer branch — the single source the D1 dual
// enumeration is authored from.
type AnchorWalk struct {
    // GrantDomain names the cap-read slice this walk's grant branch lands in
    // (Contract #6 §6.14 key space: cap-read.<GrantDomain>.<actorSuffix>).
    // Must match a declared ReadGrantDomain of the same Definition.
    GrantDomain string
    // AnchorType is the anchor vertex's label — the readableAnchors entry's
    // anchorType and the kind lint-lens-anchors checks.
    AnchorType string
    // AnchorVar is the chain variable bound to the anchor vertex. The lens's
    // Spec tail must RETURN <AnchorVar>.key AS anchor; the producer branch
    // collects nanoIdFromKey(<AnchorVar>.key).
    AnchorVar string
    // Chain is the walk as ordered pattern clauses. Each entry is a SINGLE
    // linear relationship pattern (≥1 relationship; no commas; every node
    // variable either bound by an earlier clause / "identity", or fresh and
    // connected within the clause). Compiled as one OPTIONAL MATCH per
    // clause — verbatim on the data side, var-renamed on the producer side.
    Chain []string
}
```

Example — `edgeProviderSchedule` after S2:

```go
{
    CanonicalName: "edgeProviderSchedule",
    Adapter: "nats-subject", Personal: true, Engine: "full", ...
    Walk: &pkgmgr.AnchorWalk{
        GrantDomain: "edgeManifestProvider",
        AnchorType:  "appointment",
        AnchorVar:   "appt",
        Chain: []string{
            "(identity)<-[:identifiedBy]-(pr:provider)<-[:withProvider]-(appt:appointment)",
        },
    },
    // Spec is now the TAIL only — business presentation, hand-authored as today:
    Spec: edgeProviderScheduleTail, // WITH appt, pr WHERE appt.key <> null RETURN appt.key AS anchor, ...
}
```

The package-level counterpart, on `Definition`:

```go
// ReadGrantDomains declares the cap-read producer slices this package owns.
// Every Walk.GrantDomain must name one; every declared domain must be named
// by ≥1 walk (no empty producer). One producer lens is generated per domain.
ReadGrantDomains []ReadGrantDomainSpec

type ReadGrantDomainSpec struct {
    Name string // the <domain> in cap-read.<domain>.<actorSuffix>
    // CanonicalName of the generated producer; empty defaults to <Name>ReadGrants.
    CanonicalName string
}
```

### 3.2 What pkgmgr compiles (the two artifacts)

Composition is **one exported, pure expansion pass on the `Definition`** (walks → composed data
specs + generated producer `LensSpec`s appended in `ReadGrantDomains` order), invoked before *every*
`validateAll` / `VerifyManifest` call site (the installer, `build.go Ops(...)`, and the AI-capability
materializer's four validation sites) — and exported so the testkit and lint v2 consume the same
composed output the runtime installs. It emits:

1. **The data lens**: stored spec =
   `MATCH (identity:identity {key: $actorKey})` + one `OPTIONAL MATCH <clause>` per `Chain` entry
   (verbatim, original var names — the hand-authored tail references them) + the `Spec` tail.
   **Normalization rule:** every chain clause compiles as `OPTIONAL MATCH`, including for the 5
   lenses that today fuse the chain into a *required* `MATCH` — degenerate unmatched rows are
   declined by the envelope (`personal.go:137-140`), so end behavior is equivalent, and any inline
   chain `WHERE` (e.g. `edgeTasks`' `status = "open"`) is **hoisted into the tail** during
   migration. Equivalence is not assumed: §6's per-lens row-set proof pins it. A Walk-less Personal
   lens (self-anchored `edgeIdentity`) compiles exactly as today.
2. **One generated producer `LensSpec` per `ReadGrantDomain`** — never hand-authored:
   - `CanonicalName: <Name>ReadGrants` (matching today's names, so migration is an in-place
     upgrade; lens vertex IDs are deterministic per canonical name), `Adapter: "nats-kv"`,
     `Engine: "full"`, `ProjectionKind: "actorAggregate"`,
   - `Output`: **field-for-field what the three hand producers declare today** — `AnchorType:
     "identity"`, `OutputKeyPattern: "cap-read.<Name>.{actorSuffix}"`, `BodyColumns:
     ["readableAnchors"]`, `EmptyBehavior: "delete"`, `Freshness: "auto"`, `Lanes: ["default"]` —
     **plus** the realness filter on `anchorId` so empty-delete actually fires (For-Andrew #2),
   - spec = the shared head + the member walks' `Chain` clauses + one generated
     `collect(DISTINCT {anchorType: '<AnchorType>', anchorId: nanoIdFromKey(<AnchorVar>.key),
     via: [<derived>]})` branch per walk, `+`-concatenated into `readableAnchors` — the exact
     multi-branch shape the engine is proven on (`edgeManifestReadGrantsSpec`'s doc comment,
     lenses.go:888-897).
   - **Deterministic shared-prefix factoring within a domain:** walks whose leading `Chain` entries
     are textually identical share those clauses (and their variable bindings) once; only the
     divergent suffixes get walk-scoped renames. This reproduces today's hand shape (the resident
     producer binds `residesIn→containedIn*` once for four branches, and `availableAt` once for
     services+catalog) and is what keeps the fan-out at today's level (§5). Non-identical clause
     text simply doesn't share — fail-soft: correctness never depends on factoring, only fan-out.
   - **Variable renaming is positional on the parsed pattern** (node/relationship variable
     positions), never textual regex — `wo`/`work` coexist with label `workorder` and relation
     `worksAt` in one clause today; a regex rename on the security artifact is not acceptable.
   - `via` = the relation names of the walk's full chain, in order (kills the third hand-typed
     copy; audit-only, For-Andrew #1).

Contract #6 §6.1's "one RETURN per lens; multi-output = additional Lenses" rule is honored by
construction: the single declaration still compiles to **two distinct lens vertices**.

### 3.3 Validation (fail-closed, at expansion time)

The expansion pass validates before emitting — the S1 lint logic promoted from advisory script to
hard package-build failure:

- Non-self Personal lens (`Adapter == "nats-subject" && Personal`, anchor var not `$actorKey`-bound)
  **must** declare `Walk` — absence is a build error, not a lint warning.
- `Walk.GrantDomain` must name a declared `ReadGrantDomain`; every declared domain needs ≥1 walk.
- **Each `Chain` entry is a single linear relationship pattern**: exactly one pattern (commas
  rejected), at least one relationship, and every node variable either bound by an earlier clause
  (or `identity`) or fresh-and-connected within the clause. This is the check that makes an
  unbound-scan branch — `"(identity)-[:worksAt]->(w), (t:task)"`, which would cross-product every
  task into every actor's grants via the executor's seed-by-scan path — *inexpressible*, not merely
  caught later.
- `AnchorVar` must be bound by the chain; `AnchorType` must equal the label the chain binds it with.
- The `Spec` tail must `RETURN <AnchorVar>.key AS anchor` and must **not rebind the anchor var**
  (no `AS <AnchorVar>` alias, no fresh `(<AnchorVar>:<label>)` binding in a tail MATCH). Tail
  enrichment matches off already-bound vars (e.g. `edgeInstructorSessions`' `atStudio` subtitle
  lookup) remain legal — an unlabeled var reuse *joins* the existing binding in this engine
  (`executor.go:621-624`), it cannot rebind.
- Layering: pkgmgr validates structurally (string/AST-level; it must not import
  `internal/refractor/lens` — the cycle documented at `definition.go:642`). Where a real parse is
  wanted, pkgmgr already has the injected `CypherParser` seam the capability materializer uses
  (`capabilitymaterializer.go:40-47`) — the expansion pass parses through it when wired, and the
  composed specs are additionally parse-proven in tests (§6 executes every compiled spec under
  `full.New()`) and at Refractor activation, which rejects unparseable specs.

Note the security direction of the tail rule: even if a tail evaded the structural check, the
producer is compiled **only from the Walk**, so a data-side widening trick projects anchors the grant
side never granted — and D1's gate drops them, fail-closed. The compiler makes the safe direction the
only expressible one.

### 3.4 What stays exactly as it is

- **Runtime: unchanged.** `personal.go`'s fan-out + `IsReadable`'s NanoID union gate, the
  CAS-guarded capability bucket, per-actor overwrite retraction — none of this is touched (the only
  doc-level deltas are the two called out For-Andrew). S2 unifies *authoring*; the runtime remains
  two independent CDC-driven enumerations (see §5).
- **Path A (Postgres RLS / `GrantTable` / `authz_anchors`) is out of scope — deliberately.** Its two
  sides are *different* walks meeting at the anchor vocabulary (row→anchor `authz_anchors`
  comprehension vs actor→anchor grant projection), not one walk duplicated — there is no shared
  fragment to single-source. Hand-authored `actorAggregate` producers also remain legal for slices
  with no lens counterpart (bootstrap's self-anchor base, clinic's `identifiedBy` identity slices).
  The primitive is opt-in per lens; the invariant "every non-self Personal lens declares a Walk" is
  what's enforced.

## 4. Reconciliation with the existing mental model

- *Didn't we already handle this?* — S1 handled **detection**: the lint proves anchor-kind coverage,
  the testkit proves NanoID coverage over seeded worlds. Both are after-the-fact checks on two
  hand-authored copies; every new lens still pays the dual-author tax, and only the seeded worlds are
  proven. S2 removes the second copy.
- *Does compiling the grant from the same source delete D1's security boundary?* — No, and this is
  the crux (the testkit's own header warns "deriving the grants from the data lenses … would make
  D1's gate a tautology"). What that warning forbids is deriving grants from the **data lens's full
  cypher** — S2 derives both from a **reviewed declaration**, which is different in the two ways that
  matter: (1) at **runtime** the two enumerations still execute independently (separate lens
  vertices, separate CDC re-executions, separate failure domains — a projection bug, stale slice, or
  injected row on one side is still caught by the other); (2) the gate still bounds the **whole**
  data lens — everything outside the declared Walk (the hand-authored tail) meets a grant side
  compiled only from the Walk. The dual *authoring* was never a real defense: the same author wrote
  both copies from the same mental model, so a wrong walk was always wrong twice. What dual authoring
  actually produced was accidental drift — S1's bug record proves it. The reviewed artifact narrows
  from "two cyphers that must agree" to "one declaration", which concentrates review where auth
  correctness actually lives (lattice-architecture.md:38). One residual named for completeness:
  `IsReadable` unions **all** of an actor's domain slices, so an anchor granted by another domain
  admits the row — true today, unchanged by S2.
- *Does this duplicate an established pattern?* — It **is** the established pattern, applied one seam
  over: `PermissionSpec` → vertex + 2 links; lens spec → RLS DDL; `OutputDescriptorSpec` → projection
  plan. No new engine capability, no new runtime component, no contract change.
- *New state?* — None. No new buckets, keys, or runtime state; one new declarative field compiled
  into the same stored artifacts.

## 5. Why not the alternatives

- **Lint-only forever (S1 status quo).** Detection, not prevention; dual-author tax on every lens;
  coverage proof only spans seeded worlds. Rejected — the backlog scoped S2 as compile-both precisely
  because S1 is the interim.
- **Infer the walk from the data lens's cypher (no new field).** Parse `RETURN … AS anchor`, extract
  the chain, generate the producer. Rejected: extracting reachability from arbitrary full-engine
  cypher (WITH, WHERE, comprehensions, CASE) is fragile parsing on the security plane — a wrong
  extraction silently rewrites grants; and it *would* be the tautology the testkit header forbids
  (grants derived from the full data cypher, business filters leaking into auth). The explicit
  declaration keeps the security artifact reviewable and the derivation trivial.
- **A richer graph-IR that also generates the data lens's RETURN/business columns.** Dead
  scaffolding: no consumer needs generated presentation cypher; the tail is exactly where lens
  authors need full expressiveness. The chain fragment is the minimal shared source.
- **Per-walk producers (13 slices) instead of per-domain (3).** Cleaner codegen, but multiplies
  per-actor `cap-read.*` docs ~4×, and `IsReadable` lists + unions every slice per projected row —
  a per-row read-path cost increase to save compiler work. Rejected; domains stay the §6.14 grouping
  and blast-radius unit.
- **Fully independent per-walk chains (no factoring).** The naive generation. Honest bound (the
  adversarial pass corrected an earlier undercount): in the resident domain **four** walks share the
  `residesIn→containedIn*` prefix and two also share the `availableAt` hop, so independent renaming
  multiplies intermediate rows by ≈ C³·T (C = containment-chain size 2–4, T = reachable templates,
  potentially tens) — 10⁴–10⁵ extra intermediate rows per identity per re-execution on the auth
  plane, on top of the cross-branch product the hand shape already has. Rejected in favor of the
  deterministic textual prefix factoring in §3.2, which reproduces today's fan-out exactly and stays
  single-source; if a domain ever measures hot beyond that, the remedy remains splitting the domain
  (the established §6.14 move — exactly why staff/provider are separate slices today).

## 6. Test strategy

- **Compiler golden tests** (`internal/pkgmgr`): walk → composed data spec; domain → generated
  producer spec (head, factored prefixes, positional renames, collect branches, derived `via`,
  field-for-field Output); a Walk-less Personal lens compiles unchanged.
- **Validation-failure tests**: missing Walk on a non-self Personal lens; undeclared domain; empty
  domain; multi-pattern / comma clause; disconnected clause; unbound chain head / anchor var;
  label↔AnchorType mismatch; tail rebinding the anchor var; tail missing `AS anchor`.
- **Migration equivalence (one-time, then retired), three assertions over the seeded persona
  worlds + one seeded *binding-less* identity:**
  1. **per-lens data row-set equality** — each of the 13 composed data specs produces exactly the
     rows its retired hand spec produced (this is the guard for the required→OPTIONAL conversion and
     the WHERE hoists — the hazard D1 structurally cannot catch, §2);
  2. **producer doc equality minus `via`** — whole `readableAnchors` docs (anchorId sets, types,
     Output-carried fields), not just anchorId sets;
  3. **written-key-set equality plus the realness delta** — the set of `cap-read.*` keys written
     matches today's, except the binding-less identity's placeholder docs are now deleted
     (For-Andrew #2, asserted deliberately).
- **The S1 testkit, unchanged in assertion, re-aimed:** `coverage_proof_test.go` keeps proving
  anchors ⊆ grants over resident/staff/provider worlds — now consuming the exported expansion pass
  (it currently references the spec constants by name, which the migration deletes) and executing
  every *compiled* spec under `full.New()` — which is also the composed-cypher parse proof.
- **`lint-lens-anchors` v2:** the gate's rule flips from "anchor kind has a matching hand-authored
  producer branch" to "non-self Personal lens declares a Walk" (AST-visible), consuming the same
  exported expansion pass. It must flip **in the same fire as the migration** — the current lint
  resolves producer spec *constants* (`lint-lens-anchors.go:248-255`); with generated producers its
  `providedKinds` set goes empty and every non-self Personal lens reds. No transitional dual rule
  (dead scaffolding; edge-manifest is the only Path-B package).
- **Gates:** `go build ./...`, `make vet`, `golangci-lint`, `go test ./...` (full suite — LensSpec is
  widely constructed), `make verify-package-edge-manifest`, `make verify-kernel`, all
  `scripts/lint-*.go`. Package version bump so live stacks pick the reinstall up.

## 7. Migration / compatibility

- Generated producers reuse today's `CanonicalName`s (`edgeManifestReadGrants` etc.) and output key
  patterns — an in-place §8.6 upgrade (lens vertex IDs are deterministic per canonical name,
  `installer.go:247`; spec-text change → the classifier's hot-swap-or-rebuild path; either is safe:
  the target is per-actor overwrite docs that reproject from Core KV).
- **Manifest ordering:** `VerifyManifest` compares lens canonical names *index-wise*
  (`manifest.go:120-144`), and today's producers sit interleaved at positions 14–16 of `Lenses()`.
  Generated producers are appended after the declared lenses in `ReadGrantDomains` order, and the
  migration step reorders the YAML manifest to match — a one-time, mechanical edit; no
  manifest-format change.
- `via` content and binding-less-identity doc deletion change as called out For-Andrew (both pinned
  by the §6 migration proof); no load-bearing consumer of either (verified,
  `capabilityread.go:33-35`, `:104-106`).
- Other packages: unaffected (no Personal lenses elsewhere — repo-verified; the AI-capability
  materializer cannot author Personal lenses (`capabilitymaterializer.go:70-76`), so no hidden
  migration surface; the new validation binds only `nats-subject && Personal` lenses).

## 8. Decomposition for the Steward

**ONE L fire** (ratified 2026-07-24 — the two-fire split is withdrawn, not merely permitted). The
primitive's only consumer is the edge-manifest migration, so they are coupled-ships-together per
fewer-larger-fires, and taking them separately would land a green-but-inert compiler for a steward
cadence with no value realized. Internal build order, each step leaving the tree green:

1. **The primitive.** `AnchorWalk` + `ReadGrantDomains` on `internal/pkgmgr/definition.go`; the
   exported expansion pass (composition + validation + prefix factoring + positional renaming) wired
   before every `validateAll`/`VerifyManifest` site; compiler golden + validation-failure tests.
2. **The migration.** edge-manifest's 13 lenses gain `Walk`s + tails (the 5 required-MATCH lenses'
   inline `WHERE`s hoisted per §3.2); the 3 hand-authored producer specs deleted; `ReadGrantDomains`
   declared; YAML manifest reordered (§7); the three-part migration equivalence test (then retired);
   testkit re-aimed at the expansion pass; package version bump.
3. **The lint flip.** `lint-lens-anchors` v2 — **hard-coupled to step 2 and non-optional**: the
   current gate resolves hand-authored spec constants (`lint-lens-anchors.go:248-255`), so once
   producers are generated it goes empty and would silently stop guarding every non-self lens. The
   flip is what keeps the anchors⊆grants invariant enforced against the *next* author, which is the
   whole point of S1+S2 (see [[feedback_lint_is_never_optional]]).
4. **Live-stack verify** per the run-full-stack loop.

## 9. Adversarial pass (pre-build gate — run this fire, findings folded)

Run 2026-07-23, this Designer fire: a read-only adversarial sub-agent grounded in
`lenses.go`, `pkgmgr/{definition,build,manifest,capabilitymaterializer}.go`,
`refractor/projection/{personal,driver,output}.go`, `ruleengine/full/{executor,visitor}.go`,
`capabilityread.go`, the S1 artifacts, and Contract #6 §6.1/§6.14. Material findings, all folded:

1. **BLOCKER — the "shared chain text" premise was false for 5 of 13 lenses** (chain fused into a
   *required* MATCH with inline WHERE), and the draft carried no equivalence proof for the
   recomposed *data* lenses — the one hazard D1 structurally cannot catch (a lost data-side filter
   is fully granted). Folded: §3.2's explicit required→OPTIONAL normalization + WHERE-hoist rule,
   §2's corrected premise, and §6's per-lens row-set equality as migration assertion #1.
2. **The draft's `EmptyBehavior: "delete"` claim was factually wrong** — the driver's empty-delete
   fires only with a declared realness filter (`driver.go:99`), which none of the three producers
   has; the generated all-OPTIONAL shape would otherwise mint placeholder docs for every identity.
   Folded: generated producers declare the realness filter on `anchorId` (For-Andrew #2), and §6
   asserts the written-key-set delta including a seeded binding-less identity.
3. **Chain-clause validation as drafted admitted a comma/multi-pattern clause** — an unbound scan
   that cross-products every vertex of a type into every actor's grants. Folded: §3.3's
   single-linear-pattern rule (commas rejected; connectivity required), making the attack
   inexpressible.
4. **The fan-out bound was undercounted** (~C³·T, not ≤16×; four resident walks share the residence
   prefix, two also share `availableAt`), which had propped up rejecting prefix factoring. Folded:
   §3.2 adopts deterministic textual prefix factoring (reproduces today's shape exactly); §5 records
   the honest bound against the naive alternative.
5. Smaller folds: generated `Output` must be field-for-field (`Freshness`/`Lanes` were dropped in
   the draft) with whole-doc migration comparison (§3.2/§6); `VerifyManifest` is index-wise, so the
   YAML reorder is named (§7); the expansion pass is one exported function before every
   validation site, consumed by testkit + lint v2 (§3.2/§6); producer var-renaming is positional on
   the parsed pattern, never regex (§3.2).

Survived attacks (recorded): the §4 tautology crux (runtime dual-enumeration + gate coverage of the
hand tail both stand); tail rebinding via unlabeled var reuse (the engine *joins* bound vars,
`executor.go:621-624`); in-place upgrade identity + the lint-v2 same-fire coupling (confirmed
necessary — the current lint reds otherwise); no hidden Personal-lens surface outside edge-manifest.
