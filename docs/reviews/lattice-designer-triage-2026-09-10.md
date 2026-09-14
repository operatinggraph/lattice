# Lattice designer triage — the two `📐 needs designer pass` rows (2026-09-10)

**2026-09-10, Winston (Andrew-directed session).** Mandate, verbatim: *"lattice backlog. Address all items
marked for designer pass."* Method: the [2026-08-27 pass](lattice-designer-triage-2026-08-27.md)'s and the
[verticals pass](verticals-designer-triage-2026-09-10.md)'s — every row's `no-pattern:` re-run against today's
tree, briefed to falsify; each candidate fix **run as a spike** in a detached scratch worktree against the
shipped census harnesses (never committed); the vendor premise read in the pinned server source
(`nats-server v2.14.0`, `go.mod:12`); one live read of the dev stack's `KV_core-kv` consumer report.

**Outcome: neither row needs the mechanism its `no-pattern:` names.** One dissolves into a **package edit
with an in-file precedent** (`📋 ready`, S). The other's filed mechanism has **zero payoff against the one
scenario the shipped invariant does not cover**; the structural close is a stale-evidence guard on the probe's
fail verdict, which reads state the substrate already stamps (`📋 ready`, S–M, re-sized ★). No architectural
fork, no frozen-contract change — the pass is Winston-adjudicated under the 2026-08-20 delegation (§4).

| Row | Filed `no-pattern:` | Verdict |
|---|---|---|
| [Refractor/edge-manifest] `edgeManifestReadGrants` strands `op` (§2) | per-walk anchor-variable scoping in `generateProducerSpec` | The two `op` walks belong to two **authorization bases**; move the own-task walk into its own `ReadGrantDomain` (the `domainProvider` precedent, same file) and the base producer indexes — spike-proven → **📋 ready · S · ★★** |
| [Loom] The deadline probe's evidence is shorter-lived than the wait it backstops (§3) | an armed/disarmed deadline fact on the instance record | A flag written at first probe cannot see a **late-minted** marker — the only scenario the `arm + window < TrackerTTL` invariant leaves open; the close is a **stale-evidence guard** (token epoch vs the evidence horizon) → **📋 ready · S–M · ★** |

## 1. The recurring cause, named once

Both rows were filed by a fire's **close pass** — by me, days apart — and each carried a claim the filer never
ran. The edge-manifest row said per-walk scoping was *"a real mechanism question rather than a rename"* because
`edgeCatalogTail` binds `op` and `role` by name; but the producer generator never emits the tail
([anchorwalk.go:548-580](../../internal/pkgmgr/anchorwalk.go)), so the tail's names are irrelevant to the
producer, and the *declaration* already has a partition axis (`GrantDomain`) that separates the two walks
without renaming anything. The Loom row was filed **after** its sibling design had already re-derived and
**declined** the identical `no-pattern:` (`loom-deadline-marker-provenance-design.md` §3 carrier 2, §11 row 4,
shipped `1982952e` three days earlier), and the filing's own caveat — *"held out of reach by
`MaxDeadlineArm`+marker window (≤ 2 h), not closed"* — names the bound that covers every **replay**; the one
delivery the bound does not cover is a marker **minted late**, which no flag written at first probe can see. A
row filed from a close pass gets the least scrutiny of anything on the board; brief its falsification as you
would a stranger's, and price the named mechanism against the one scenario the shipped bound leaves open.

## 2. [Refractor/edge-manifest] `edgeManifestReadGrants` strands `op`, bound over unrelated chains

**Filed (`1b9e7346`, the varlength Inc 2 close):** *"Three `edgeCatalog` walks anchor `op:meta` over different
chains and two land in the base producer, so a stage boundary strands a name no engine narrowing may re-admit.
The shared `edgeCatalogTail` binds `op`/`role` by name, so per-walk scoping is a mechanism, not a rename."* —
`no-pattern: per-walk anchor-variable scoping in generateProducerSpec`.

### 2.1 Grounding

- **The collision is real and the refusal is correct.** `domainBase` collects ten walks
  ([lenses.go:110-380](../../packages/edge-manifest/lenses.go)); two bind `op` to a `meta` over different
  chains — `(tpl)-[:permitsOperation]->(op:meta)` (`edgeCatalog` walk 0, `:133-141`) and
  `(task)-[:forOperation]->(op:meta)` (walk 2, `:151-159`). `generateProducerSpec` stages one `WITH … AS
  grantSliceN` per walk, so `op` is dropped at walk 0's boundary and re-bound at walk 2 over a different chain.
  `withScopeReject` refuses exactly that ([withscope.go:53](../../internal/refractor/ruleengine/full/withscope.go);
  the vector *"two walks binding one name over different relations are refused"*,
  [hopindex_test.go:725-748](../../internal/refractor/ruleengine/full/hopindex_test.go)) — merging the two
  occurrences would assert a hop no row walks, the under-approximation the auth plane must never take. The
  census pins the verdict: `"edgeManifestReadGrants": hopWithDropped`
  ([anchor_hopindex_corpus_census_test.go:139-150](../../internal/refractor/anchor_hopindex_corpus_census_test.go)).
- **The row's mechanism claim is false.** The generator renders each walk's clauses plus a `collect(…)` of the
  walk's `AnchorVar` ([anchorwalk.go:548-580](../../internal/pkgmgr/anchorwalk.go)) and **never the lens tail**;
  `edgeCatalogTail`'s `WITH op, role` lives only in the data lens (`composeDataLensSpec`, `:470-485`). What
  makes a *declaration* rename impossible is `parseWalks`' rule that every walk of one lens shares its
  `AnchorVar` (`:300-306`) — true, and beside the point: the collision is between two walks of one **domain**,
  and the domain is a declared partition of the producer (`ReadGrantDomainSpec`, `:49-62`: *"the §6.14 grouping
  and blast-radius unit … splitting reachability paths that not every actor has into their own domain"*).
- **The demand is measured, not hypothetical.** The base producer is the larger of the two `cap-read` auth-plane
  producers the BFS fallback re-executes per actor: ≈4 s per event with a 5.9k-event replay backlog
  ([authority-walk-wall-unit-cost-design.md §2.4](../../_bmad-output/implementation-artifacts/authority-walk-wall-unit-cost-design.md)),
  a 106 k backlog at the personal-lens cost fire's read
  ([personal-lens-whole-actor-cost-design.md §4.4](../../_bmad-output/implementation-artifacts/personal-lens-whole-actor-cost-design.md)),
  and a 41,963-document rebuild draining at 0.235/s — ~50 h
  ([capability-plane-rebuild-throughput-design.md](../../_bmad-output/implementation-artifacts/capability-plane-rebuild-throughput-design.md)).
  Today's live read (`nats consumer report KV_core-kv`, 139 consumers): every consumer at 0 unprocessed — a quiet
  host with no user traffic, which is a floor, not a refutation of the loaded-day figures.
- **The D1 reader unions every domain**, so a new slice changes no effective grant: `IsReadable` lists
  `cap-read.*.<actorSuffix>.<anchorId>` with the domain as a wildcard
  ([capabilityread.go:56-62](../../internal/refractor/capabilityread/capabilityread.go)); `IsRelevant` reads the
  row's `anchorType` audit field, never the domain
  ([interest.go:353](../../internal/refractor/personalinterest/interest.go)). The package's own test file states
  the doctrine: *"§6.14 unions slices, so a new slice costs nothing, while folding its branches into the base
  producer would multiply that lens's existing cross-product fan-out"*
  ([package_test.go:56-64](../../packages/edge-manifest/package_test.go)).
- **The own-task path is a distinct authorization basis.** `edgeCatalogTail`'s comment says so
  ([lenses.go:600-618](../../packages/edge-manifest/lenses.go)): a task-scoped submission *"is authorized
  independently of any standing role or service reachability — the actor's `cap.ephemeral.*` task grant, not a
  `cap.roles.*` permission"*. The residence chain and the held-role chain each have a domain; the task chain was
  lumped into base by accident of the fold (refractor-shared-keyspace-arbitration-design.md §13.7 (c)).

### 2.2 The spike — two candidate fixes, run

Detached worktrees at HEAD `d600377e`, the census harnesses run as-is (only the one moved pin patched so the
loop continues):

| | **A — domain split** (package) | **B — per-stage rename in the generator** (platform) |
|---|---|---|
| Edit | `domainTask = "edgeManifestTask"` + `ReadGrantDomains()` entry + walk 2's `GrantDomain` (3 lines, `lenses.go`) | `generateProducerSpec` renames every walk-bound name `<v>_w<N>` via the existing `render(rename)` (12 lines, `anchorwalk.go`) |
| `edgeManifestReadGrants` hop index | **`hopIndexed`** (was `hopWithDropped`) | **`hopIndexed`** |
| one-key verdict (`actor_onekey`) | `walk: actor type binds off-anchor` — the staff producer's own verdict, the benign direction | same |
| `ActorTypeBindsAnchorOnly` T8 pin (`pipeline`) | **passes** | **fails** — `edgeManifestStaffReadGrants`'s `identity` positions 3 → 5: renaming un-merges the re-opened chains the Inc 2 narrowing merged, so the index graph gains duplicate unlabeled positions and the derivation seeds each |
| `ruleengine/full` | 6 enumeration pins list the three producers by name | green |
| `pkgmgr` | green | 2 pins (the golden emission, `CollidingWalkVariablesAreStagedApart`) |
| `packages/edge-manifest` | 3 pins (19 → 20 lenses, `manifest.yaml`, the namespaced-keys map) | green |
| `lint-cap-read-producers` | 0 issues | 0 issues |
| Refractor census pins moved (lower bound; each harness stops at its first) | 8, all *"a new lens needs a verdict"* or *"the base producer's binding set shrank"* | 3, plus digest churn in every producer's variable names |

Both convert the base producer. B changes the shape of every generated producer's index graph — measured
on the staff producer, whose T8 pin exists precisely so a cypher change cannot re-arm a second mechanism
silently ([varlength design §6.4](../../_bmad-output/implementation-artifacts/varlength-anchor-derivation-design.md))
— and pays it on the platform side for a single consumer. A changes one package's partition.

### 2.3 Alternatives

| # | Alternative | Verdict |
|---|---|---|
| 1 | **Do not have this thing** — the BFS fallback is sound; leave the row | Rejected. "Slower, never wrong" is the posture the varlength design already rejected on this producer's measured cost (§2.1); the fix below is three lines |
| 2 | **A — a fourth `ReadGrantDomain` for the own-task walk** | **Adopted.** Precedent in the same file (`domainProvider`, persona-worlds Fire W0); the split states a true authorization boundary; the effective grant set is unchanged (union); task events now re-execute a one-walk producer instead of the ten-walk base |
| 3 | **B — rename every walk variable per stage** | Rejected: un-merges the narrowing's merged positions on every producer (staff `identity` positions 3 → 5), churns every producer's pins, and fixes a class with one member |
| 4 | **C — rename only the `AnchorVar` per stage** | Not adopted; **the recorded class fix.** Six lines in `generateProducerSpec`; adds no position (each anchor is already its own) and states a truth (each stage's anchor is that stage's binding). Revive trigger: a second package's generated producer pinned `hopWithDropped` on an anchor name bound over different chains |
| 5 | Widen the engine's narrowing to admit the two `op` occurrences | Refused on soundness — they are two bindings; merging asserts a hop no row walks (§2.1) |
| 6 | Group the two subset-related base walks (`edgeServices` ⊂ `edgeCatalog#0`) | Orthogonal cost item, already sequenced by the varlength design §13 behind a measured trigger; unchanged |

### 2.4 Verdict — `📋 ready · S · ★★`: the own-task op walk gets its own read-grant domain

**The increment (package, Steward-built, review depth per `agents/steward/SKILL.md` §4 — a `cap-read`
producer partition is on the auth plane; the adversarial layer applies):**

1. `packages/edge-manifest/lenses.go`: `domainTask = "edgeManifestTask"`; `ReadGrantDomains()` gains
   `{Name: domainTask}`; `edgeCatalog` walk 2 (`(identity)<-[:assignedTo]-(task:task)`,
   `(task)-[:forOperation]->(op:meta)`) declares `GrantDomain: domainTask`. `manifest.yaml` + the `Version`
   bump + the `Description`'s "three generated producers" sentence (`package.go:26-27`);
   `DIFF_BASE=<base> go run ./scripts/lint-package-version.go`.
2. Pins that move, each with its argument in the commit message (a row moving **to** indexable is the
   direction the harness says needs one): `anchor_hopindex_corpus_census_test.go` (base → `hopIndexed`; new
   `edgeManifestTaskReadGrants` → `hopIndexed`), `actor_onekey_corpus_census_test.go` (base →
   `walkMultiPosition`), `actor_walk_scope_corpus_census_test.go`, `branch_decomposition_corpus_census_pins_test.go`
   (base loses `g1/o2[op,task]`; the new producer joins the decomposing population),
   `auth_plane_narrowing_census_test.go`, `grouping_reduction_corpus_census_test.go`,
   `label_derivation_corpus_census_test.go` (new: `identity meta task`, exhaustive), `footprint_classifier_test.go`;
   `ruleengine/full`'s three producer-name lists (`branch_decomposition_equivalence_test.go:1195`,
   `grouping_equivalence_test.go:356`, `grouping_producer_gate_test.go:108`); `packages/edge-manifest/package_test.go`
   (`TestPackage_NineteenLenses` → twenty, `readGrantLensNames`).
3. **Coverage proof for the new domain** (owned here): `coverage_proof_test.go`'s resident world proves
   `edgeCatalog` branch 0 only (`:171-188`); add a task-world case proving branch 2's anchors are covered by
   `edgeManifestTaskReadGrants` — a task `assignedTo` the actor with a `forOperation` link, the op meta in the
   task slice and **not** in the base slice.
4. `docs/components/edge-manifest.md`: the domain tables (`:55-100`) gain the task domain and its producer row.
5. Phase 0 re-runs §2.5's censuses; a disagreement is a scope change.

**Non-goals (the drift fence):** no generator change (C is recorded, not built); no change to `edgeCatalog`'s
data-lens cypher or the tail; no touch to the Inc 2 narrowing, which keeps its only corpus consumer
(`edgeManifestStaffReadGrants`) and its 41-vector pin.

### 2.5 Executable censuses

```
# the collision, today (expect: hopWithDropped pinned, and the two `op` chains in domainBase)
grep -n '"edgeManifestReadGrants"' internal/refractor/anchor_hopindex_corpus_census_test.go   # → hopWithDropped
grep -n 'AnchorVar:   "op"' -B2 -A6 packages/edge-manifest/lenses.go | grep -c 'domainBase'   # → 2
# the reader unions domains (expect: the wildcard form)
grep -n 'KeyPrefix + "\*\."' internal/refractor/capabilityread/capabilityread.go               # → 1 hit
# after the edit (expect: base and task producers both "" = indexed)
go test ./internal/refractor/ -run TestCorpusAnchorHopIndex_PinnedConjuncts -count=1
```

### 2.6 Fire brief (build note, 2026-09-14) — Steward/Lattice, branch `claude/exciting-clarke-pccb08`

**1. Scope sentence (verbatim, §2.4).** *"The own-task op walk gets its own read-grant domain"* — `edgeCatalog`
walk 2 declares a fourth `ReadGrantDomain` (`edgeManifestTask`), so the base producer stops binding `op` over
two unrelated chains and indexes. **Green bar:** `edgeManifestReadGrants` moves `hopWithDropped` →
`hopIndexed` in `anchor_hopindex_corpus_census_test.go`; every moved census pin carries its argument; the new
domain carries its own coverage proof; `lint-cap-read-producers` 0 issues; full suite green with Postgres up.

**2. Verified touch-list** (`file:line` checked live at `1402660`):

| File | Anchor | Edit |
|---|---|---|
| `packages/edge-manifest/lenses.go` | `:443-448` const block; `:432-441` `ReadGrantDomains()`; `:151-159` walk 2 | add `domainTask = "edgeManifestTask"`, a fourth `ReadGrantDomainSpec`, walk 2's `GrantDomain` |
| `packages/edge-manifest/manifest.yaml` | `:2` version; `:105-107` last producer entry | bump; append `edgeManifestTaskReadGrants` (nats-kv/full) **last — the slice order is the manifest order** (`lenses.go:433-434`) |
| `packages/edge-manifest/package.go` | `:26` `Version`; `:26` Description "three generated producers (…)" | bump + four |
| `packages/edge-manifest/package_test.go` | `:62-66` `readGrantLensNames`; `:77-98` `TestPackage_NineteenLenses` | fourth name; 19 → 20 |
| `packages/edge-manifest/coverage_proof_test.go` | `:130-165` `emResidentWorld`; `:167-189` `TestManifestAnchorCoverage_ResidentWorld`; `:64-105` `grantedAnchorIDs`/`assertAnchorsCovered` | task-world case: branch 2's anchors covered by the task producer and **not** by base |
| `internal/refractor/anchor_hopindex_corpus_census_test.go` | `:148` | base → `hopIndexed`; new producer pin |
| `internal/refractor/actor_onekey_corpus_census_test.go` | `:120` (`walkIncompleteIndex`) | re-pin + new |
| `internal/refractor/actor_walk_scope_corpus_census_test.go` | `:141`, `:208` | re-pin + new |
| `internal/refractor/branch_decomposition_corpus_census_pins_test.go` | `:64` verdict, `:168` `decomposingCorpusLenses`, `:264` footprint map | re-pin + new in all three |
| `internal/refractor/auth_plane_narrowing_census_test.go` | `:334` `stayBroadCases` | re-pin + new |
| `internal/refractor/grouping_reduction_corpus_census_test.go` | `:137`, `:339` `armed` | re-pin + new |
| `internal/refractor/label_derivation_corpus_census_test.go` | `:270` | re-pin + new (`identity meta task`) |
| `internal/refractor/projection/footprint_classifier_test.go` | `:108-110` | new name |
| `internal/refractor/ruleengine/full/branch_decomposition_equivalence_test.go` | `:1211-1212` | new name |
| `internal/refractor/ruleengine/full/grouping_equivalence_test.go` | `:366` | check the `edgeManifestReadGrants` special-case still holds |
| `internal/refractor/ruleengine/full/grouping_producer_gate_test.go` | `:40`, `:129` | three → four |
| `docs/components/edge-manifest.md` | `:55-100` domain tables | a task-domain table + its producer row |

Rotted/corrected citations vs §2.4: the classifier pin is at `internal/refractor/projection/footprint_classifier_test.go`
(not `internal/refractor/`); `grouping_producer_gate_test.go`'s list is at `:40`, not `:108`;
`branch_decomposition_equivalence_test.go`'s at `:1211`, not `:1195`. `scripts/lint-cap-read-producers.go`
needs **no** entry — it never sees generated producers (its own `:43-51`).

**3. Precedents to mirror.** `domainProvider` (`lenses.go:448` + `ReadGrantDomains():439`, persona-worlds
Fire W0) is the shipped four-line shape for "a reachability path not every actor has gets its own slice",
and `edgeEntitySessions`' provider walk is the shipped "one walk of a multi-walk lens names a non-base
domain" precedent. Producer name derives from the domain (`anchorwalk.go:209-213`, `<Name>ReadGrants`), so
no `CanonicalName` override. Coverage-proof case mirrors `TestManifestAnchorCoverage_ResidentWorld`.

**4. Increment order.**
1. **Package split** — `lenses.go` + `manifest.yaml` + `package.go`, then `go test ./packages/edge-manifest/ -count=1`
   (expect `TestPackage_NineteenLenses` + `readGrantLensNames` to fail loudly = the split took).
2. **Package pins + docs** — `package_test.go`, `docs/components/edge-manifest.md`;
   `DIFF_BASE=origin/main go run ./scripts/lint-package-version.go`.
3. **Census pins** — driven by the harnesses, not by memory:
   `go test ./internal/refractor/ -count=1` then `./internal/refractor/projection/` and
   `./internal/refractor/ruleengine/full/`; each moved pin gets its argument in the commit message.
   `POSTGRES_TEST_DSN` must be exported (REMOTE.md §3) or `internal/refractor` is falsely green.
4. **Coverage proof** — the task-world case; revert-prove it by pointing walk 2 back at `domainBase`.
5. **Gates** — `go build ./...`, `make vet`, `golangci-lint run ./...`, `STRICT=1 go run ./scripts/lint-conventions.go`,
   `go run ./scripts/lint-cap-read-producers.go`, `go test ./... -p 4`.

**5. In-scope gotchas.**
- **Package version lockstep** — `manifest.yaml` **and** `package.go`'s `Version` both move or the change is
  invisible to a running stack (CLAUDE.md).
- **Domain order is the manifest order** (`lenses.go:433-434`) — append, never insert.
- **Refractor dossier, applicable entries** (`docs/components/refractor.md`): *a new per-lens analysis ships
  its corpus census in the same fire, reusing `forEachCorpusCypher`* — here inverted: the harnesses already
  enumerate from `pkgregistry`, so a new producer **auto-appears and must be pinned**, and a pin moved
  **toward** indexable needs its argument stated. *A soundness claim's stated REASON is load-bearing* — the
  claim "the effective grant set is unchanged" rests on the reader's wildcard (`capabilityread.go:56-62`,
  re-run live: 2 hits) and `IsRelevant` reading `anchorType`, not the domain; both re-verified.
- **Standing checklist #2** (every census is a premise): §2.5's three censuses re-run live at selection —
  `hopWithDropped` pinned ✓, two `op` walks in `domainBase` ✓, reader wildcard ✓.
- **Standing checklist #3** (a fix is proven by reverting it): increment 4's revert-proof.
- `docs/components/edge-manifest.md` carries **no** dossier section; nothing to copy from it.

**6. Adjacent finds.** None out of scope surfaced at Phase 0. The §2.3 alternative C (per-stage `AnchorVar`
rename in the generator) stays **recorded, not built** — its revive trigger is a second package's producer
pinned `hopWithDropped`, which this fire does not create.

**7. Non-goals (the drift fence).** No generator change; no change to `edgeCatalog`'s data-lens cypher or
`edgeCatalogTail`; no touch to the Inc 2 narrowing or its 41-vector pin; no change to the D1 reader; the
§2.3 #6 walk-grouping cost item stays sequenced where the varlength design §13 left it.

**Build note — SHIPPED `5c9912af` (2026-09-14), CI green (run 34809952836, 13/13).** The install was
observed on a live stack, which is the one thing no test proves: a native NATS + Postgres + Processor +
Refractor stack, `edge-manifest 0.17.16` installed over its dependency chain (`writeCount=128`),
`verify-package-edge-manifest` 89/89, and the generated producers enumerated out of Core KV — **four
`vtx.meta.*.canonicalName` producer vertices for four declared domains**, `edgeManifestTaskReadGrants`
among them. A new lens in an already-installed package is a `diffManifest` **create**, not the additive-apply
gap the board's Pkgmgr row describes (that row scopes `Apply`'s in-place branch, i.e. a capability proposal
carrying a partial Definition).

Deviations from the touch-list above, all narrowing or gap-closing, none widening the scope sentence:

- **The touch-list was incomplete in one direction the harnesses could not catch.** `composed_test.go`'s three
  producer enumerations are HAND-WRITTEN, so the fourth domain escaped all three while the package stayed
  green — including the realness-filter pin, whose absence would leave a placeholder-only
  `cap-read.edgeManifestTask.<actor>` document written forever. Two now derive their population from the
  compiled lens set; the third asserts its case list covers every generated producer. The lesson for part 2 of
  the next brief: a census site that *auto-enumerates* needs a pin moved, and one that *hand-lists* needs a
  name added — grep alone does not distinguish them, and only the second can fail silently.
- **The corpus could not reach the new domain.** `seedReadGrantCorpus` seeds `assignedTo` but no
  `forOperation`, so every `ruleengine/full` differential over the task producer compared two empty
  projections. Fixed with an op-per-task knob; the granting claim moved outside the armed filter, and
  `TestBranchDecomposition_GeneratedProducersProjectIdenticalRows` — which carried no content assertion at
  all — gained a productivity guard.
- **Two pins the §2.4 list did not foresee**, both earned by the base producer's flip to answered:
  `edge_manifest_base_staged_graph_test.go` (its staged-vs-unstaged graph and its withScopeVerdict consumers,
  mirroring the staff pair) and a T8 subtest in `pipeline/anchor_derivation_ranged_internal_test.go`, which
  the predicate can only evaluate now that the index is complete.
- **The producer gate's floor became an equality.** `stages >= 3` cannot hold for a one-walk domain; rather
  than lower it, the gate asserts the generated staging-clause count equals the domain's declared Walk count.
- **§2.2's prediction corrected:** the task case lands in `narrowingCases`, not `stayBroadCases` — the walk is
  labelled at every node, so it narrows.
- Truth fixes in files the list did not name: the hop-index census header (no row is refused any more), a
  shipped e2e header asserting the base producer "stays refused", the walk-scope argument's relations, and
  `docs/components/edge-manifest.md`'s own-task-path-deferred note.

**Scope-diff gate: PASS.** Every touch above traces to §2.4's four numbered steps (the docs row is step 4,
the census rows step 2, the coverage row step 3, the package rows step 1); nothing widens it. Declared
dependencies re-verified both ways: §2.4 names `lint-cap-read-producers` — verified **not** load-bearing
(generated producers are invisible to it), noted and dropped from the pin list but kept as a green check;
no unlisted dependency surfaced.

## 3. [Loom] The deadline probe's evidence is shorter-lived than the wait it backstops

**Filed (`cefa0390`, the marker-TTL fire's close):** *"The probe judges rejected-or-lost from the 24 h
tracker's absence while the wait is unbounded, so a `deadline.<id>` marker delivered past 24 h fails a healthy
parked instance."* — `no-pattern: an armed/disarmed deadline fact on the instance record · held out of reach by
MaxDeadlineArm+marker window (≤ 2 h), not closed`.

**Already declined once.** The provenance design re-derived this `no-pattern:` and rejected it
([§3 carrier 2, §11 row 4](../../_bmad-output/implementation-artifacts/loom-deadline-marker-provenance-design.md)):
*"cannot tell a replayed removal from an expiry when the instance is on a later armed step … a field on 12,351
records; needs record and key to agree across `rearmDeadline`"*. Its first objection is discharged by its own Inc 1
(removals are acked on provenance, never probed); the row was re-filed regardless, three days after that design
shipped, by a fire whose §3.2 chose its window *"precisely so that raising it cannot reach this gap"*.

### 3.1 Grounding — what the invariant proves, and the one delivery it cannot see

- **The shipped probe** ([engine.go:1310-1471](../../internal/loom/engine.go)): acts only on the server's
  `Nats-Marker-Reason: MaxAge` marker (provenance), only when `deadline.<id>` is absent (currency), and its
  `fail` is revision-conditioned (the write window). It reads rejected-or-lost from two absences — the
  `lattice.op.status` tracker (`processor.TrackerTTL = 24h`, [tracker.go:17-19](../../internal/processor/tracker.go))
  and the outbox record — in all three step kinds (`:1471`, `:1525`, `:1595`).
- **The invariant** `MaxDeadlineArm + LoomStateMarkerTTL < TrackerTTL` (1 h + 1 h < 24 h, 10× margin) is pinned
  by [platform_buckets_test.go:71-100](../../internal/bootstrap/platform_buckets_test.go), and `withDefaults`
  clamps both arms to the ceiling (`engine.go:195-209`). It proves: **a marker minted on time is consumed while
  the tracker lives**, replays included — a rebuilt `DeliverAll` durable replays only markers still in the
  stream, at most one window old ([platform-bucket-marker-ttl-design.md §5](../../_bmad-output/implementation-artifacts/platform-bucket-marker-ttl-design.md)).
- **What it cannot see: a marker minted late.** The marker is written when the server *processes* the expiry,
  not when the TTL falls due. In the pinned server, with subject-delete markers configured,
  `expireMsgsOnRecover` returns before expiring anything (`filestore.go:2521-2523`: *"If subject delete markers is
  configured, can't expire on recover"*), `recoverTTLState` rebuilds the timed hash wheel and schedules the
  age check at once (`:2148-2173`, `defer fs.resetAgeChk(0)`), and `expireMsgs` then drains every past-due
  entry, emitting a `MaxAge` marker for each subject it empties (`:6790-6905`, `handleRemovalOrSdm`). So after a
  server outage — or a host clock jump, which the live path (`ats.AccessTime()`) handles the same way — every
  past-due per-message TTL is processed **in the same pass, on the same clock**, in `KV_loom-state` and
  `KV_core-kv` alike. If the outage exceeds the tracker's remaining life, the deadline marker is minted and the
  tracker removed within milliseconds of each other; Loom's durable delivers the marker; the probe finds no
  tracker, no outbox ⇒ **`fail`** of an instance whose creation committed and whose human task is live.
- **The population.** Instances whose arm was live at outage start (≤ `CreateTaskTimeout`/`StepTimeout`, 60 s by
  default, ≤ 1 h clamped) and whose op had committed — plus any marker minted but unconsumed at outage start
  that the durable delivers before the stream expires it. Small, DR-scale (an outage longer than ~23 h), and
  nobody has observed one. But its outcome is a **false terminal with a misleading reason** (*"step N CreateTask
  rejected"*) on an instance whose task the human can still complete; the completion then lands on a terminal
  instance and is dropped. Recovery is `lattice loom redrive`, if anyone notices.
- **The filed mechanism retires none of it.** An armed/disarmed fact is written when Loom *learns* the creation
  committed, which is at the first probe (`onUserTaskDeadline` case 1, `:1497-1508`) — Loom consumes no
  task-created event (its consumers: the trigger, the completion domains, the outbox relay, the deadline;
  `engine.go:370-416`). In the late-minted case the first probe **is** the late one: no flag exists to read.
  The flag would short-circuit replays — which the invariant already bounds — and nothing else. Payoff: zero.
- **What the probe would need is the age of its own evidence.** The substrate stamps every KV entry
  ([`KVEntry.Timestamp`](../../internal/substrate/kv.go), `entry.Created()`, `kv.go:18-54`), and the pending
  step's token pointer `token.<pendingToken>` is written create-only in the step's own transition batch
  ([state.go:452-530](../../internal/loom/state.go)) and re-put by `redrive` (`tokenPutUnderRedriveCAS`,
  `:376-387`) — its timestamp is the **epoch of the current step**, untouched by re-arms (`rearmDeadline` writes
  only the deadline key, `:610-620`) and by any note the probe itself writes on the record. The horizon the
  evidence lives for is the op-status surface's own: `opstatus` is a leaf package (deps: `substrate` only;
  importers: `cmd/processor`, `cmd/lattice`, `bridge`, `gateway`, `loom`), the natural single home for the
  constant the Processor writes with and the probe compares against.

### 3.2 The shape — a stale-evidence guard on the fail verdict

**Rule.** *Absence is evidence only inside the evidence's lifetime.* When the probe reaches "tracker absent and
outbox absent", it reads the token pointer's timestamp; if `now − epoch ≥ opstatus.TrackerTTL`, the verdict is
**inconclusive**: Warn (the §10.6 alert carrier Loom already uses for "advanced and alerted",
`engine.go:1452`), a CAS-written `deadlineProbe` note on the record, Ack — the instance stays running with its
token. Otherwise the verdict is `fail`, exactly as today. The pattern-pin-missing `fail` stays unguarded (an
invariant break is evidence in itself).

**State table — every wake-up × every epoch age** (states as the provenance design §5 defines them):

| Wake-up | Epoch age | Tracker | Outbox | Today | After |
|---|---|---|---|---|---|
| on-time `MaxAge` (≤ arm + window ≤ 2 h) | < 24 h | present | — | committed → no-op / advance | same |
| on-time `MaxAge` | < 24 h | absent | present | re-arm | same |
| on-time `MaxAge` | < 24 h | absent | absent | **fail** (correct) | same |
| replayed `MaxAge` (rebuilt durable, ≤ 1 h old) | < 24 h | present | — | no-op | same |
| **late-minted `MaxAge`** (server outage / clock jump > tracker life) | **≥ 24 h** | absent (aged out) | absent | **false fail** — the row | **inconclusive**: Warn + note + Ack |
| late-minted `MaxAge`, op genuinely rejected before the outage | ≥ 24 h | absent (never written) | absent | fail (correct by luck) | inconclusive — the evidence cannot distinguish the two; the alert names both readings |
| re-arm chain > 24 h (relay down a day), then rejected | ≥ 24 h | absent | absent | fail | inconclusive — stated, accepted: the day-long relay outage is the loud fault |
| redriven step, late marker | reset by the re-put | as above | | | epoch restarts at redrive; a redrive re-submits, so the tracker is fresh |
| two replicas, late-minted | ≥ 24 h | absent | absent | A fails, B's CAS refused | A notes, B's CAS refused ⇒ Ack |
| redelivered marker after a note | ≥ 24 h | absent | absent | — | inconclusive again; note re-written at the new revision (idempotent, bounded by the window) |
| never-written token pointer for a running instance | — | | | (unreachable: same batch) | `probeFail("token pointer missing")` — an invariant break, mirrors the pin-missing arm |

**Lifetime of the note (`Instance.DeadlineProbe *probeNote{At, Reason}`):** created by the inconclusive
verdict (record-only CAS put at the probe's revision — **not** `transition`, whose no-TTL branch purges the
already-expired deadline key and would mint a stray marker); reset by `transition` whenever a new token is
written or the status leaves running (`advance`, `fail`) and by `RedriveInstance`; carried across restart on the
record; ordered by the CAS; surfaced by `InspectInstance` (`InstanceSummary`, [control.go:10-30](../../internal/loom/control.go),
which copies fields and must copy this one). An older binary ignores and drops it on its next write — a
downgrade clears a note, acceptable.

**Consumer table.**

| Reader | Change |
|---|---|
| `onDeadline` / `onUserTaskDeadline` / `onExternalTaskDeadline` (`engine.go:1471`, `:1525`, `:1595`) | the three rejected-or-lost `probeFail` calls route through one helper that reads the epoch and branches |
| `stateStore` | `tokenEpoch(ctx, token) (time.Time, error)` (one `KVGet` on the fail branch only); `noteDeadlineProbe(ctx, inst, revision)` |
| `transition` (`state.go:452`), `RedriveInstance` (`control.go`) | clear the note |
| `InstanceSummary` / Loupe's `#/flows` detail | carries the note (rendering is a Loupe follow-on, not this fire) |
| `processor.TrackerTTL` (`tracker.go:19`) | becomes an alias of `opstatus.TrackerTTL`; `bootstrap`'s invariant test reads it unchanged |
| `MaxDeadlineArm`'s comment (`engine.go:64-78`), `TestLoomStateMarkerTTL_FitsInsideTheTrackerLifetime`'s (`platform_buckets_test.go:87-91`), `docs/components/loom.md:351,363,587` | the *"past it, the probe fails a healthy instance"* / *"structural fix §11.2"* sentences are rewritten to the guard |

### 3.3 Alternatives

| # | Alternative | Verdict |
|---|---|---|
| 1 | **Do not build; document the DR residual + runbook** (after a day-long outage, redrive instances failed with the rejected reason at resume time) | Rejected, narrowly. The harm is a false terminal with a misleading reason on the orchestration plane, self-healing never, and every design in this lineage plus two code comments point at "the structural fix"; the guard is ~100 lines on existing seams. Imp ★ says how rare it is |
| 2 | **The row's mechanism** — an armed/disarmed fact on the record | Rejected (§3.1): written at first probe, it cannot see the late-minted first delivery; retires nothing the invariant does not |
| 3 | **RPC-side verdict** — the request carries `asOf`, the reply answers `beyondHorizon` | Rejected for now: one consumer. The bridge's `resultAlreadyLanded` ([dispatch.go:373-400](../../internal/bridge/dispatch.go)) also reads this surface, but its "not found past 24 h ⇒ re-dispatch" **is** Contract #4 §4.3's contracted horizon, not a hazard. Revive trigger: a second consumer that reads absence as a terminal verdict |
| 4 | **Raise `TrackerTTL`** to outlive any outage | Rejected — Contract #4 §4.3 fixes 24 h platform-wide (the provenance design's row 3) |
| 5 | **Loom `Config` field wired from `bootstrap`/`processor`** | Rejected: `loom` imports neither (`go list -deps`), and a duplicated constant enforced by a three-constant test is the Loom dossier's own entry; `opstatus` is the leaf both already import |
| 6 | **Epoch from the instance record's timestamp** (+ re-arm through `transition` so re-arms refresh it) | Rejected: the note write would refresh the same timestamp and a redelivery would then read a fresh epoch and **fail**; the token pointer is the step's epoch by construction |
| 7 | **A TTL'd Health-KV key per inconclusive instance** (the auth-trace shape) | Rejected: a standing fact needs a lifecycle, and clearing it at every transition is an unconditional purge on a usually-absent subject — the marker-minting hazard `deleteToken` documents (`state.go:631-645`) |
| 8 | **Nak / re-arm instead of alert** | Rejected: a Nak asks for evidence that cannot appear; a re-arm loops every arm with the same verdict |

### 3.4 Contract surface — Contract #10 §10.6 NEEDS AN EDIT (this section's original claim is falsified)

**Amended 2026-09-14 at build time, per the body-stays-true rule.** This section asserted that the guard
builds to §10.6 with no `docs/contracts/*` edit. **That is wrong, and it was wrong when ratified.** Two of
§10.6's clauses are *structural* promises about termination, not epistemic rules about evidence, and the guard
contradicts both; a third sentence below rested on an operator verb that does not exist. The original text is
struck and replaced here rather than annotated, because a builder reading it would have shipped against a
contract it violates.

§10.6's failure-detection clauses ([10-orchestration-loom.md](../../docs/contracts/10-orchestration-loom.md),
the "Failure detection" subsection): *"**A systemOp step is bounded end to end.** … the engine then
distinguishes, **by evidence**, and (a)… (b)… (c) a genuinely rejected/lost op fails the instance … with an
alert — never a silent wedge (FR29)"*; *"A rejected or lost creation fails the instance with an alert **instead
of parking forever**"*.

- **What the original argument got right.** "Distinguishes **by evidence**" is an epistemic rule, and the guard
  honours it: past the horizon the engine declines to establish (c)'s antecedent at all. Read as an enumeration
  of what the engine *knows*, (a)/(b)/(c) is not contradicted.
- **What it missed.** *"Bounded end to end"* and *"instead of parking forever"* are promises about the
  **outcome**, and past the horizon the instance genuinely parks. §3.2's own state table contains the row where
  (c)'s antecedent is TRUE — *"late-minted `MaxAge`, op genuinely rejected before the outage"* — and the
  shipped outcome for that row is the one those clauses forbid by name. Conceding it as "the one consequence a
  reader could observe" and then declining to edit the contract *was* the contradiction.
- **The sentence that was simply false.** *"the operator verb (`redrive`) is the same one a false fail needs
  today"* — `RedriveInstance` accepts only a `failed` instance, so no verb reaches the parked state. Widening it
  was built and then **withdrawn** in this fire (§3.8); the gap is real and filed.

**Resolution (2026-09-14).** The build shipped; the contract edit is **prepared as a proposal for Andrew, never
committed by the Steward** (CLAUDE.md; `agents/steward/REMOTE.md` §2 — a proposal branch off `main` whose diff
*is* the proposal, with `📐 PROPOSED — UNRATIFIED` banners). Its shape: qualify *"bounded end to end"* to the
lifetime of the evidence, add a **(d)** branch for an outcome the engine cannot distinguish (alerted and
recorded on the instance, which stays running on its token), and qualify the *"parking forever"* clause with
that same exception. (d) deliberately names no operator verb, because there is none to name and a contract
carries observable promises only.

### 3.5 Verdict — `📋 ready · S–M · ★`: the probe refuses to read absence as rejection past the evidence horizon

**One increment (Steward-built; posture-changing on the orchestration plane → the adversarial layer):**
`opstatus.TrackerTTL` + the `processor` alias; `tokenEpoch` / `noteDeadlineProbe` in `state.go`; the shared
rejected-or-lost helper in `engine.go` with the note field, its two clearing sites and the `InstanceSummary`
copy; the four comment/doc rewrites in §3.2's last row; a `docs/components/loom.md` dossier entry.

**Tests, each owned here:** (T1) an epoch older than the horizon ⇒ inconclusive — Warn, note at the read
revision, Ack, instance running with its token — driven by an **injected clock** on the engine, never a sleep
and never a backdated entry (the server stamps it); (T2) **mutation:** delete the comparison ⇒ T1 reds; (T3) a
fresh epoch ⇒ `fail` — the four shipped e2es with real 2 s expiries pass unchanged; (T4) the note is cleared by
advance, by fail, by redrive (three rows); (T5) `InspectInstance` carries it; (T6) two replicas: the second CAS
is refused ⇒ Ack (the provenance design's race-test shape); (T7) a redelivered marker after a note re-notes at
the new revision and fails nothing; (T8) the vendor premise: on the pinned server with markers configured, a
per-message TTL past due at recovery is expired **with** a `MaxAge` marker on the next age check — as a case of
the substrate's marker-provenance fixture if `natsfixture` can restart a file-backed server, else the
`filestore.go` cites above stand as the pin and the fixture gap is filed.

**Gates:** the standard set (`CLAUDE.md`); `internal/loom`, `internal/opstatus`, `internal/processor`,
`internal/bootstrap` under `go test`; `make test-lease-convergence` (Loom's tagged harness); no `packages/`
edit. **Live close:** cycle Loom (`pkill -x loom` + `make orchestration`), confirm `loom-deadline` at 0 pending
and the heartbeat healthy; the late-minted path has no live population to exercise and is pinned by T1/T8.

### 3.6 Executable censuses

```
# the invariant and both bounds (expect: 1h, 1h, 24h; the test present)
grep -n 'MaxDeadlineArm = \|LoomStateMarkerTTL = \|TrackerTTL = ' internal/loom/engine.go internal/bootstrap/platform_buckets.go internal/processor/tracker.go
grep -n 'func TestLoomStateMarkerTTL_FitsInsideTheTrackerLifetime' internal/bootstrap/platform_buckets_test.go
# the three unguarded rejected-or-lost verdicts (expect: 3 probeFail calls carrying "rejected")
grep -n 'probeFail(ctx, inst, .*rejected' internal/loom/engine.go
# opstatus is a leaf and loom already imports it (expect: substrate only; loom in the importer list)
go list -deps ./internal/opstatus | grep 'lattice/internal/' ; grep -rl '"github.com/operatinggraph/lattice/internal/opstatus"' internal/loom
# the vendor premise (pinned source; expect the early return under SubjectDeleteMarkerTTL > 0)
sed -n 2515,2523p $(go env GOMODCACHE)/github.com/nats-io/nats-server/v2@v2.14.0/server/filestore.go
```

### 3.7 Fire brief (build note, 2026-09-14) — Steward/Lattice, branch `claude/exciting-clarke-re5z2f`

**1. Scope sentence (verbatim, §3.5).** *"One increment (Steward-built; posture-changing on the orchestration
plane → the adversarial layer): `opstatus.TrackerTTL` + the `processor` alias; `tokenEpoch` /
`noteDeadlineProbe` in `state.go`; the shared rejected-or-lost helper in `engine.go` with the note field, its
two clearing sites and the `InstanceSummary` copy; the four comment/doc rewrites in §3.2's last row; a
`docs/components/loom.md` dossier entry."* Green bar = §3.5's gates + T1–T8.

**2. Verified touch-list** (every anchor re-read live on `418f200`; §3's own citations are leads and most
drifted — the live number is what binds).

| Site | Live anchor | What changes |
|---|---|---|
| `internal/opstatus/service.go` | `:27` (`Subject` const); package is 2 files, deps `substrate`(+`/keys`) only — the §3.6 leaf census **holds** | new `TrackerTTL = 24 * time.Hour` beside `Subject`, carrying the Contract #4 §4.3 provenance |
| `internal/processor/tracker.go` | `:19` (`const TrackerTTL = 24 * time.Hour`) | becomes `= opstatus.TrackerTTL`; call sites `step8_commit.go:387`, `step_interfaces.go:139` unchanged |
| `internal/loom/engine.go` | `:223-246` `Engine` struct · `:269-284` `NewEngine` | a `clock func() time.Time` field + an `e.now()` accessor |
| | `:64-78` `MaxDeadlineArm`'s comment | the *"past it, the probe reads a committed op's aged-out tracker as 'never committed' and fails a healthy instance"* sentence is rewritten to the guard |
| | `:1296-1304` `probeFail` | unchanged; the new helper wraps it |
| | `:1487-1489` (systemOp) · `:1539-1543` (CreateTask) · `:1610-1613` (instanceOp) | the three rejected-or-lost verdicts route through one helper |
| | `:1451-1455` (`pattern pin missing`) | **stays unguarded** — an invariant break is evidence in itself (§3.2) |
| `internal/loom/state.go` | `:144-152` `Instance` · `:136` `tokenKey` | `DeadlineProbe *probeNote` field |
| | `:606-719` `transition` (marshals at `:607`; the else-branch at `:707-713` purges the deadline key) | clears the note when a new token is written or the status leaves running — and is **not** the note's writer |
| | `:743-761` `redrive` (marshals at `:744`) | clears the note |
| | `:786-795` `deadlineArmed` · `:767-776` `outboxExists` | the one-KVGet precedent `tokenEpoch` copies |
| `internal/loom/control.go` | `:16-23` `InstanceSummary` · `:184-191` the field copy in `inspectResolved` · `:333-381` `RedriveInstance` | the summary carries the note; redrive's clear rides `redrive` |
| `internal/substrate/kv.go` | `:18-24` `KVEntry{Timestamp}` · `:37` `KVGet` · `:164` `KVUpdate(…, expectedRevision)` | read as-is — `KVUpdate` **is** the record-only CAS put §3.2 asks for |
| `internal/bootstrap/platform_buckets_test.go` | `:72-101` the invariant test + its *"belongs with the structural fix … §11.2 files"* comment | comment rewritten to the shipped guard; the assertions stand |
| `docs/components/loom.md` | dossier `:603-679`; entry 8 at `:647`, entry 11 at `:672-679` | a new entry; entry 11's *"would turn the deadline probe against healthy instances, silently"* sentence keeps its subject but names the guard |

**Rotted citations, corrected here** (part of the gate, not a footnote): §3.1/§3.2's `engine.go:1310-1471`,
`:1471`, `:1525`, `:1595`, `state.go:452-530`, `:376-387`, `:610-620`, `:631-645` and `control.go:10-30` have
all drifted (+15 to +190 lines); the live anchors are in the table. **`docs/components/loom.md:351,363,587`
names no such sentences** — the live text is dossier entry 11 (`:672-679`); the sibling sentence lives in
`platform-bucket-marker-ttl-design.md:26`. Both are in the rewrite set.

**Two premises the design implied that the tree does not have** — each resolved here, before the first edit:

- **There is no injected clock on the Engine.** T1 is specified as *"driven by an injected clock on the
  engine"*; `time.Now()` is called directly (`engine.go:160`, `state.go:692`, `:801`) and no seam exists. The
  fire **adds** it, mirroring `internal/weaver/engine.go:293-321` verbatim in shape (a `clock` field plus an
  `e.now()` accessor that tolerates the zero value, so a hand-built Engine keeps the wall clock). This narrows
  nothing and substitutes nothing; it is the seam the ratified test needs.
- ~~**`natsfixture` cannot restart a file-backed server**~~ — **true when this brief was compiled, FALSIFIED
  by this same run** (struck 2026-09-14, body-stays-true). The fixture gap was absorbed as the batch's own
  second unit rather than left as a find: `natsfixture.StartRestartableServer` /
  `RestartableServer.{Stop,Start,StoreDir}` gives the JetStream store to the *test* instead of to any one
  server (`b094aa2`), and **T8 is pinned live** by
  `TestKVMarkerProvenance_TTLPastDueAtRecoveryStillMintsAMaxAgeMarker` in
  `internal/substrate/kv_marker_provenance_test.go` — the stronger of §3.5's two options, not the
  `filestore.go`-cites fallback. A reader must not take this bullet as licence to skip the pin: it exists.

**3. Precedents to mirror.**

- `tokenEpoch` → `deadlineArmed` (`state.go:786`) / `outboxExists` (`:767`): one `KVGet`, `ErrKeyNotFound`
  turned into a typed answer rather than an error.
- `noteDeadlineProbe` → `redrive`'s revision-conditioned record write (`state.go:753`), rendered as a
  single-key `KVUpdate` (`substrate/kv.go:164`). **Not** `transition`: its no-deadline branch (`:707-713`)
  purges `deadline.<id>`, which on an already-expired key mints a stray marker — the hazard `deleteToken`
  documents at `:815-820`.
- The inconclusive verdict's Warn-and-Ack → `probeFail`'s drop (`engine.go:1296-1304`) and `fail`'s Warn
  carrier (`:1273`); the return is `nil` ⇒ Ack, never a Nak (§3.2, and `probeFail`'s own argument).
- Clearing the note inside the two persistence sites → the failed-index settle in `transition` (`:631-658`):
  settle the derived fact inside the batch that flips the status, never as a second write.
- The clock seam → `internal/weaver/engine.go:293-321`.

**4. Increment order** (one fire; each step's green check runnable).

1. `opstatus.TrackerTTL` + the `processor` alias. → `go build ./... && go test ./internal/opstatus/ ./internal/processor/ ./internal/bootstrap/ -count=1`
2. `Instance.DeadlineProbe` + `tokenEpoch` + `noteDeadlineProbe` + the two clearing sites. → `go test ./internal/loom/ -count=1`
3. The shared rejected-or-lost helper across the three verdicts + the clock seam + the `InstanceSummary` copy. → `go test ./internal/loom/ -count=1 -run 'Deadline|Probe|Redrive|Inspect'`
4. T1–T7 (T8 as the vendor-cite pin), each revert-proven. → `go test ./internal/loom/ -count=1`
5. Comment/doc rewrites + the dossier entry. → `STRICT=1 go run ./scripts/lint-conventions.go && go run ./scripts/lint-doc-orphan.go`
6. Full gate: `go build ./... && make vet && golangci-lint run ./... && go test ./... -p 4` (with `POSTGRES_TEST_DSN` up — without it `internal/refractor` is falsely green, `REMOTE.md` §3) `&& make test-lease-convergence`.

**5. In-scope gotchas.** No `packages/` edit ⇒ no manifest/`Version` bump (§3.5). No frozen-contract edit —
§3.4 builds to Contract #10 §10.6 as written; its one observable consequence is stated there for Andrew's one
look. `loom-state` removals are TTL'd purges, never DELs. The new field is additive JSON: an older binary
decodes and drops it, a downgrade clears a note (§3.2, accepted). **Dossier entries copied in verbatim** —
loom's entry 8 (*"a message on `deadline.>` is a delivery to a handler whose evidence outlives nothing it
backstops … when a probe's evidence has a shorter life than the wait it guards, its trigger set — and the
currency of what it read — are the first things to audit"*), entry 11 (*"a constant whose only enforcement is
a test of three constants is not enforced"* — binds the new `opstatus` constant: its enforcement must be the
computed invariant, not a restatement), entry 9 (*"a handler that acts on 'empty body' acts on every removal
shape … name the header, never the shape"*), entry 5 (*"`require`/`assert` inside a `require.Eventually`
predicate fails a passing test from a non-test goroutine"*), entry 1/6 (*"a `CreateOnly` write against a
subject that still carries a marker is refused … a guard must never depend on the marker's presence OR
absence"*), and entry 2 (*"a fixture that hand-seeds `loom-state` cannot reach the states a real transition
leaves behind"* — T1 seeds through `createInstance` + `transition`, never `putInstance`). The standing
checklist applies whole, #1 hardest: **the note is new state, so its state table is §3.2's, and every row of
it is a test** (created / reset at four boundaries / carried across restart / ordered by the CAS).

**6. Adjacent finds.** (a) ~~**`natsfixture` cannot restart a file-backed NATS server**~~ — **CLOSED by this
run**, absorbed as the batch's second unit (`b094aa2`), not filed. (b) §3.2's `docs/components/loom.md:351,363,587` and the `platform-bucket-marker-ttl-design.md:26` sentence
are stale *instructions* pointing at a "separate, unbuilt row" this fire closes — fixed in increment 5, in the
same commit (the body-stays-true rule).

**7. Non-goals.** Loupe's `#/flows` rendering of the note (§3.2's consumer table says so). The
`pattern pin missing` verdict. Raising `TrackerTTL` (§3.3 alt 4, refused — Contract #4 §4.3). An RPC-side
`asOf`/`beyondHorizon` verdict (§3.3 alt 3, one consumer). Any change to `rearmDeadline`'s unconditioned PUT
or to the marker-provenance admission gate.

**Scope-diff gate: PASS.** Every touch in part 2 traces to a clause of part 1; the two premise corrections
add a test seam and a doc-rewrite target and widen no mechanism; the one substitution candidate (writing the
note through `transition`) is refused on the stray-marker hazard, as §3.2 already required. §3.6's four
executable censuses re-run live: the three bounds read `1h / 1h / 24h`, the invariant test is present at
`platform_buckets_test.go:92`, `opstatus` is a leaf that `loom` already imports (`engine.go`) — all as
stated. The one census whose *form* differs: §3.6's `probeFail(ctx, inst, .*rejected` expects 3 and matches
**0**, because two of the three verdicts carry their reason through `fmt.Sprintf` on a separate line; the
three sites are `:1488`, `:1542`, `:1612` as designed — the count holds, the grep does not.

### 3.8 Close note — SHIPPED 2026-09-14, and what did not ship

**Shipped on `main`** (Steward/Lattice, branch `claude/exciting-clarke-re5z2f`): `b094aa2` the T8 fixture seam
+ live pin · `a66dd47` the guard · `c3c4825` the fix round · `27e5c76` the withdrawal below. CI green.

**The guard is the ratified design, whole.** All three rejected-or-lost verdicts date the step before reading
absence as rejection; the epoch is the `token.<pendingToken>` pointer's substrate stamp; past
`opstatus.TrackerTTL` the verdict is inconclusive (Warn + a CAS-written `deadlineProbe` note + Ack, instance
left running); a missing pointer stays a terminal. The horizon moved to `internal/opstatus` with `processor`
aliasing it.

**Withdrawn: the operator verb.** §3.4's false "the operator verb (`redrive`) is the same one a false fail
needs" was addressed mid-fire by widening `RedriveInstance` to accept a running instance carrying a note. The
cumulative close pass broke it twice, both proved, and it was reverted in `27e5c76`:

1. A running instance's **pattern pin is authoritative** (which is why `advance` and `onDeadline` read it), and
   the widened path re-pinned from the live source — so with the pattern edited since, the redrive resumed a
   **different step** than the one the instance was parked on. `errCursorOutOfRange` cannot see an insert or a
   same-length reorder. `resumeStepZero` is the engine's own precedent for resuming this exact state *from the
   pin*, and the widening did not use it.
2. Redriving a parked userTask re-submits `CreateTask` with the same taskId → its declared dedup arm commits
   with no mutations and no events → the Processor still writes a tracker → the next creation-deadline finds
   `trackerExists` true and takes the "committed, unbounded human wait" branch, **clearing the note and arming
   nothing**. An alerted, redrivable park becomes a silent, unredrivable one — strictly worse than the state
   this item exists to fix.

Also established: `transition`'s own safety argument for `tokenPutUnderRedriveCAS` ("after redrive's CAS this
instance has no live advancer — **it was terminal**") is a second, independent reason the parked state cannot
reuse that path. **The verb is filed as the one designer row** (`lattice.md`), with this proved catalogue and
the three candidate mechanisms: resume from the pin; a distinct verb for the parked state; re-arm on the
verdict (already refused by §3.3 alt 8). What ships today therefore parks with an alert and a record and **no
verb** — stated plainly in the alert text, `docs/components/loom.md`'s failure row and the dossier entry,
rather than papered over.

**Review classification** (four cold passes: state-lifecycle, contract-posture, test-inertness, cumulative
close). Two BLOCKING were **test-inertness**, both proved by a mutation that left the suite green: the systemOp
and instanceOp arms could be reverted to `probeFail` undetected (every case drove the userTask arm), and the
epoch's *source* was unpinned — the design's own §3.3-rejected alternative 6 passed everything, because the
test parked the injected clock at an absolute instant so both sides of the comparison moved together. Both are
now pinned, the second by staging the separating instant. One BLOCKING + one SHOULD-FIX were **design-gap**,
and both landed on the same seam: §3.4's contract claim, and the verb it asserted. Four were **brief-gap** —
`tombstone_sweep.go`'s `skipRunningDeadlineMarker` rationale, `platform_buckets.go`'s constant doc,
`TestWithDefaults_ClampsTheDeadlineArmBothWays`'s doc, and `loom.md`'s failure row — every one a statement
describing the harm this guard retired, none in the brief's rewrite set. The rest were convention (two
history-narrating comments, an alert whose first words were the verdict it contradicted). **No review
over-reach.**

Two dossier entries in `docs/components/loom.md`: the class itself (*date the evidence, not just the wait*),
and the withdrawal's lesson (*withholding a verdict is only kinder than a wrong one if some verb still reaches
the flow*). The cap held at 12 by retiring the TTL'd-purge entry into `lint-conventions`' `checkLoomStateDelete`,
which has been doing its catching since it shipped.

**Process note, for the fleet rather than for Loom.** A second scheduled Steward fire
(`claude/exciting-clarke-ulleji`) judged this branch idle at 16:10 UTC and rebuilt the same guard concurrently;
the remote build lock is void (`REMOTE.md` §4) and nothing prevented it. The row's take-over rule reads
**branch** idleness as **fire** idleness, and they diverge whenever a builder is mid-round and the last push is
hours old. That branch was left untouched; one genuine improvement was taken from it (one clock read per
verdict, now pinned with a stepping clock).

## 4. Adjudication

Per the 2026-08-20 delegation both verdicts are Winston-adjudicated: no architectural fork (a package partition
with an in-file precedent; a guard inside Loom's own probe reading a substrate stamp), no frozen-contract change
(§2 touches no contract; §3 builds to §10.6, with its one observable consequence stated in §3.4 for Andrew's
one look). Board rows rewritten in this commit; `STRICT=1 go run ./scripts/lint-board.go` green. The Steward
builds §2 and §3 as `📋 ready` units (S and S–M; §3 with the adversarial layer).

## 5. Falsification record

| Claim | How it was tested | Verdict |
|---|---|---|
| E1 — the producer generator never emits the lens tail, so `edgeCatalogTail`'s names cannot make the fix "a mechanism" | `generateProducerSpec` read end to end; `render(nil)` + `collectBranch` only | **holds** — the row's premise was false |
| E2 — a domain split converts the base producer under the shipped engine | spike A, `TestCorpusAnchorHopIndex_PinnedConjuncts` + eight sibling harnesses + `ruleengine/full`, `pipeline`, `projection`, `pkgmgr`, `packages/edge-manifest`, `lint-cap-read-producers` | **holds**; only enumeration pins move |
| E3 — a generator rename is the cleaner platform fix | spike B, same harnesses | **falsified**: T8 reds on the staff producer (positions 3 → 5); the narrowing's merge is what the rename undoes |
| E4 — the D1 reader unions domains | `capabilityread.go:62` wildcard; `IsRelevant` signature | **holds** |
| L1 — the shipped invariant covers every replay | `platform_buckets_test.go`, marker-TTL design §5, provenance design §5 | **holds** |
| L2 — the invariant covers a late-minted marker | pinned `filestore.go` (`expireMsgsOnRecover`, `recoverTTLState`, `expireMsgs`) | **falsified** — the residual is real, DR-scale, in the vendor's own code |
| L3 — the filed armed/disarmed fact closes the residual | Loom's consumer set (`engine.go:370-416`); the first-probe write site | **falsified** — payoff zero |
| L4 — the bridge is a second consumer with the same hazard | `dispatch.go:373-400` + Contract #4 §4.3 | **falsified** — its horizon is the contracted dedup horizon |
| L5 — an epoch exists without new state | `KVEntry.Timestamp`; the token pointer's write modes | **holds** |
| L6 — `loom` can read the horizon without a cycle or a duplicate | `go list -deps` on `loom`, `bootstrap`, `opstatus`; the importer list | **holds** via `opstatus` (processor aliases) |

Net: two rows filed by my own close passes, both with a claim I had not run. One dissolved to a package edit
the file already had a precedent for; the other's named mechanism would have shipped a field on every instance
record that closes nothing, while the actual residual sat one function away in the vendor's recovery path.
