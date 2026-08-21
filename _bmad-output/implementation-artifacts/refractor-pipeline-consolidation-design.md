# Refractor `pipeline.go` consolidation — behavior-frozen file split

Board row: `_bmad-output/planning-artifacts/backlog/lattice.md` § Refinements & ops, "[Refractor] A
behavior-frozen consolidation pass". No prior design doc existed for this item; this doc is that record,
created at first build per the steward's Phase-0 rule.

## Scope (verbatim, this fire)

`internal/refractor/pipeline/pipeline.go` is 3932 lines — a god-file. This fire moves the **rebuild
lifecycle** method group into its own file in the same package, with **zero behavior change**: no logic
edits, no signature changes, no renames. Pure code motion, verified by an unmodified `go test
./internal/refractor/...` passing byte-identical.

## Ground (census, 2026-08-16)

- `pipeline.go` is 3932 lines, ~90 methods on `*Pipeline`. `grep -n "^func " pipeline.go` shows a
  self-contained rebuild-lifecycle group at **lines 2469–2981** (~512 lines): `abandonRebuild`,
  `beginRebuild`, `endRebuild`, `currentRebuildSignal`, `RebuildAndWait`, `waitRebuildSignal`, `Rebuild`,
  `rebuildWithSignal`, `resolveTruncate`, `rebuild`, `resumeInterruptedRebuild`, `watchRebuildCompletion`,
  `recordRebuildProgress`, `RebuildProgress` — plus the `rebuildSignal` type (`pipeline.go:2522`), which
  belongs with this group and is referenced from `pipeline.go:486` (`rebuildWatch *rebuildSignal` field on
  `Pipeline` — stays in `pipeline.go`, same package, no import needed either way).
- Considered folding duplicated test-fixture helpers (`newDeleteKeyKV`/`newCollisionKVs`/`newAuditKVs`,
  `vertexBody`/`seedVertexBody`/`putBody`/`aspectBody`) across the ~40 `pipeline/*_test.go` files instead —
  censused first. Rejected as the first increment: the bodies are near-duplicate, not identical (different
  bucket sets per caller), so folding them risks a subtle test-fixture behavior drift for ~80–120 LOC of
  gain. Lower value, higher risk than a pure production-code file split. Left as a future increment, not a
  filed row (any future consolidation fire can pick it up from this doc).
- No frozen contract, no architectural fork, no design decision beyond "which lines move where" — this is
  implementation-level per steward §0, decided here.

## Increment order

1. **Move the rebuild-lifecycle group** (`pipeline.go:2469-2981` + the `rebuildSignal` type) into a new
   file `internal/refractor/pipeline/rebuild.go`, same package, own import block (`goimports`-derived).
   Verify: `go build ./...`, `gofmt -l`, `go vet ./internal/refractor/...`,
   `golangci-lint run ./internal/refractor/...`, `go test ./internal/refractor/...` — all must be identical
   to pre-move (no new failures, no skipped tests).
2. *(stretch, same fire if Inc 1 lands clean)* Move the **result-writing** group
   (`writeResults`, `enqueueRetry`, `enqueueActorReprojectRetry`, `publishTerminalDLQ`, `writeAudit` —
   roughly `pipeline.go:3441-3915` pre-Inc-1 line numbers, re-verified live after Inc 1 shifts them) into
   `internal/refractor/pipeline/results.go`. Same verification.

## Non-goals

- No logic changes, no renamed identifiers, no test-helper deduplication (see Ground above), no split of
  the remaining ~3000-line `pipeline.go` beyond what's listed — that's future increments of this same item,
  scoped by whoever picks it up next against this doc.

## Build note

- **Inc 1: shipped `2653b88e`.** The live boundary was `pipeline.go:2428-2985` (558 lines, not
  2469-2981) — the group's overview doc-comment sits directly above `abandonRebuild`'s own doc comment
  with no blank line, so it's syntactically one comment block; moved as part of the unit rather than split
  mid-thought. Nothing non-rebuild-related was interleaved. `pipeline.go` 3932→3372 lines; new
  `rebuild.go` 575 lines. Zero logic changes — `gofmt`/`go vet`/`golangci-lint`/`lint-conventions`/
  `go test ./internal/refractor/...` all green, no `_test.go` file needed changes. CI green
  (`31939876682`).
- **Inc 2: shipped `75f838b5`.** Re-scoped 2026-08-16 (option b): move the 5 named result-writing methods as two
  non-contiguous cuts, leave `Pause`/`awaitStarted`/`Resume`/`RemoveConsumer`/`Delete`/`DeleteAllForActor`
  in `pipeline.go`. Re-verified live at `2653b88e` (Inc 1's landed shape): `writeResults` (2881–3053),
  `enqueueRetry` (3055–3093), `enqueueActorReprojectRetry` (3095–3160) are contiguous; then the
  control-plane group `Pause`…`DeleteAllForActor` (3162–3304) sits before `publishTerminalDLQ`
  (3306–3350) and `writeAudit` (3352–3372). The group's *membership* was always the 5 named methods, not
  "whatever is between two line numbers" — moving them as two cuts into one target file is still pure code
  motion; it does not force `Pause`/`awaitStarted` into a file named for a theme they don't belong to,
  which option (a) would. Rejected (c) — a different group — as strictly less value for the same
  file-split goal this item exists to make progress on. Zero logic changes — `gofmt`/`go vet`/
  `golangci-lint`/`lint-conventions`/`go test ./internal/refractor/pipeline/...` all green, no `_test.go`
  file needed changes. `pipeline.go` 3372→3024 lines; new `results.go` 362 lines. CI green
  (`31942573259`).
- **Inc 3: shipped `f90cc686`.** Picked the narrowing-decision cluster over the CDC-dispatch cluster —
  smaller, more self-contained, lower complications risk. Re-verified live at `75f838b5` (Inc 2's landed
  shape, `pipeline.go:1624-1993`): `NarrowedFilterEligible`, `narrowedFilterEligible`, `type
  FilterDecision`, `broadFilterReason`, `registrationFailedDecision`, `RecordFilterDecision`,
  `ConsumerFilter`, `ConsumerFilterLabels` moved verbatim into new `internal/refractor/pipeline/filter.go`,
  same package, no import churn for the same-package callers (`rebuild.go` calls `ConsumerFilter`/
  `RecordFilterDecision`/`registrationFailedDecision`). Zero logic changes — `gofmt`/`go vet`/
  `golangci-lint`/`lint-conventions` all green; `go test ./internal/refractor/pipeline/...` 30/30 pass.
  `pipeline.go` 3023→2629 lines; new `filter.go` 404 lines. CI green (`31946411898`).
  (`go test ./internal/refractor/...` as a whole hit an unrelated pre-existing timeout in
  `ruleengine/full`'s `TestBranchDecomposition_RandomizedCorporaDifferential` — no dependency edge from
  this diff onto that package; matches the already-tracked, Whetstone-owned "suite reddens under parallel
  load" board row, not a regression from this move.)
- **Inc 4: shipped `3a3124ba`.** The CDC-dispatch cluster, moved into new
  `internal/refractor/pipeline/dispatch.go` as **two cuts**. Re-verified live at `28dd09f5` (Inc 3's landed
  shape): cut A `pipeline.go:2033-2335` (`handleTracked`, `handle`, `evalLinkFanOut`,
  `evalPlainAspectReprojection`, `evalPlainLinkReprojection`), cut B `pipeline.go:2372-2437`
  (`evalAspectFanOut`, `dispositionEvalErr`). Two cuts because `evaluatePlainFromVertex` (2337-2356) and
  `dedupeKeyFor` (2358-2370) sit physically between them and are **also called from
  `anchor_derivation_plain.go`** — they belong with the plain-derivation machinery, so they stayed.
  `supersededRule` stayed for the same reason (`results.go`, `audit.go`, `reproject.go` all call it: a
  cross-cutting rule-generation guard, not dispatch-private). Zero logic changes, **proven not asserted** —
  `pipeline.go`'s diff has **zero added lines**, and the 370 moved lines are **byte-identical** to the two
  source ranges; the only non-cut deletions are the `strings` + `adjacency` imports, which move to the new
  file. `gofmt`/`go vet`/`golangci-lint`/`STRICT=1 lint-conventions`/`lint-lens-anchors`/
  `lint-manifest-entity-type`/`lint-package-standard`/`lint-package-version` all green; no `_test.go` needed
  changes. `pipeline.go` 2629→2238 lines (3932 before Inc 1); new `dispatch.go` 386 lines.
- **Inc 4 also repaired an Inc 2 regression.** Inc 2 moved `writeResults` to `results.go` but left its
  18-line doc comment behind at `pipeline.go:2439-2456`, where — with no blank line between them — it had
  welded onto `supersededRule`'s own doc block. Net: `writeResults` undocumented, `supersededRule`
  misdocumented by 18 lines describing a different function, with the compiler, `gofmt`, `golangci-lint`
  and every test blind to it. Restored above `writeResults`, verbatim.
- **Standing check for every future increment** (Inc 1 already logged a near-miss of this class): a doc
  comment sits directly above its declaration with **no blank line**, so a cut taken at the `func` line
  silently orphans it. After each cut assert (a) every moved declaration still carries its doc comment in
  the NEW file, and (b) no comment block left behind opens by naming a declaration that no longer lives
  there. Match a comment block's **FIRST line only** — a naive grep over whole blocks false-positives on
  docs that merely *mention* another function (2 of 3 hits when this was first run were exactly that:
  `AuditOptions`' doc naming `AuthPlane`, `registrationFailedDecision`'s naming
  `registerWithFilterFallback`). Checked at Inc 4: `rebuild` (Inc 1) and `ConsumerFilter` (Inc 3) are
  undocumented in their new files, but git shows both were already undocumented **before** their moves —
  pre-existing, not regressions.
- **Test-parallelism note.** Inc 3 saw `ruleengine/full`'s `TestBranchDecomposition_RandomizedCorporaDifferential`
  time out on a whole-tree run. At Inc 4 `go test ./internal/refractor/... -count=1 -p 2 -parallel 4` passed
  the **whole tree in 88s with zero failures**. A bare `go test` defaults `-p` to `NumCPU` (8 here), running
  8 concurrent embedded-NATS test binaries; capping to `-p 2` held total test-binary RSS at ~128 MB. Use the
  capped form on this box — it is a host-contention signature, not a code defect (Whetstone-owned board row).
- **Inc 5: shipped `06874635` — the FINAL increment (Andrew, 2026-08-20: "make it the last one").** Both
  remaining candidates taken in one fire, as two cuts re-verified live at `a7c94ef3`: `pipeline.go:651-1114`
  → new `ruleinstall.go` (`UseFullEngine`, `UseFullEngineBranches`, `var narrowingBlockRank`,
  `narrowingBlockRankOf`, `UseFullEngineBranchesForReDerivation`, `useFullEngineBranches`,
  `sortedLabelList`, `labelsWithoutExpansion` — installing/compiling a rule + taxonomy label expansion);
  `pipeline.go:1116-1560` → new `rulestate.go` (`type ruleState` through `LinkEventRelevant`, 17 decls — the
  compiled-rule snapshot and the event-relevance predicates derived from it, keeping the type with its own
  methods). Cut the LATER range first so the earlier range's numbers could not shift. Zero logic changes,
  proven not asserted: `pipeline.go`'s diff has **zero added lines**, and both moved bodies are
  **byte-identical** (464 + 445 lines). Every other deletion accounted for: the now-unused `sort` import, and
  a net **−2** blank lines where the two cuts left three consecutive separators that `gofmt` collapses to one
  (HEAD 132 blanks − 26 in-cut = 106 vs 104 actual). `pipeline.go` **2238→1326**.
  `narrowingBlockRank` is a package-level `var` that moved: confirmed read only inside `narrowingBlockRankOf`
  (moved with it), never at package-init time, so initialization order is unaffected.
- **Item CLOSED after Inc 5. Final state: `pipeline.go` 3932 → 1326 lines (−66%) across five increments,
  every one behavior-frozen and byte-identity-proven**, into `rebuild.go` · `results.go` · `filter.go` ·
  `dispatch.go` · `ruleinstall.go` · `rulestate.go`. What remains in `pipeline.go` is the irreducible core:
  the ~470-line `Pipeline` struct, `New`, the setter/accessor surface, `Run`, and consumer lifecycle.
  *(Superseded 2026-08-20: the close after Inc 5 left `executor.go` and the test fold untaken and said so.
  Andrew reopened the item — "finish the job that item was created for, do not defer" — and the two
  remaining halves shipped as Inc 6 below. The row's whole scope is now done.)*

- **Inc 6: shipped `603eae2c` + `515cb8e7` — the two halves the Inc-5 close had left untaken.**

  **(a) `executor.go`, `603eae2c`.** Four cuts into `seed_nodes.go` (candidate-set discovery + anchor
  seeding), `rel_traverse.go` (variable-length hop expansion), `expr_eval.go` (expression evaluation +
  function dispatch), `values.go` (property resolution, value coercion/comparison, normalized-key
  serialization). Taken in **descending** line order so earlier ranges could not shift. `executor` /
  `nodeRef` / `binding` and all three package-level declarations stay put — same package, so every moved
  method compiles unchanged. `executor.go` **2426 → 1407**. Zero added lines; three of the four files
  byte-identical to their source ranges, `values.go` identical as a line multiset (it carried a
  pre-existing orphaned doc comment, repaired in place — see below).

  **(b) The test fold, `515cb8e7`.** Four helpers in `package pipeline` each booted an embedded NATS
  server and opened a bucket set with structurally identical bodies; all four now delegate to one variadic
  `newTestKVs(t, buckets...)`. Names and signatures unchanged, so **none of the ~57 call sites moved**
  (`newCollisionKVs` alone has 49).

  **The Ground section's rejection of this fold was wrong, and specifically wrong.** It argued the bodies
  were "near-duplicate, not identical (different bucket sets per caller)" — but the bucket set is exactly
  the parameterizable part. The real difference was elsewhere and nobody had noticed it: `newCollisionKVs`
  and `newActorEnumeratorAdjKV` **lacked the `testing.Short()` guard** their two siblings carried. They
  inherit it now, deliberately — all four are NATS-backed, so its absence was an oversight, and nothing in
  `ci.yml` or the `Makefile` runs `-short`, so no CI or local gate path changes. Proven, not assumed:
  `go test ./internal/refractor/pipeline/ -short` now SKIPs every newly-guarded family cleanly. No
  parameter was added to preserve the missing guard — a parameter whose only purpose is to keep an
  oversight alive is not a fold.

- **Inc 6 also mechanized this campaign's own recurring defect: `f60565cf`.** The standing doc-comment
  check recorded at Inc 4 failed a second time — `executor.go`'s `propertyOf` doc welded above
  `resolveProperty`, exactly the Inc-2 `writeResults` shape. Twice-seen ⇒ mechanized (SKILL.md §4), so the
  check is now **`scripts/lint-doc-orphan.go`**, wired into `ci.yml` under STRICT and as
  `make lint-doc-orphan`. Turning it on found **nine** genuine orphans across pkgmgr, refractor, testutil,
  loftspace-app and loupe — all nine repaired as pure relocations (per-file non-blank line multiset
  unchanged, so no code moved). The first-line-only rule that Inc 4's note insisted on is what makes it
  usable: prototyped over whole comment blocks it was 66% false positives. `lint-package-version` took a
  matching narrowing — it now skips a `internal/pkgmgr/` file whose change is provably comment-only,
  compared as comment-free ASTs rather than diff lines (that directory emits Cypher, where `//` also opens
  a comment, so a raw-string line would fool any line-based test). Mutation-tested: a real code change
  there still fails the gate.

- **FINAL STATE — the row's whole scope, done.** `pipeline.go` **3932 → 1326** (−66%) and `executor.go`
  **2426 → 1407** (−42%), across six increments, every one behavior-frozen and proven rather than asserted.
  Twelve files now carry what two god-files did. The test fold removed the four duplicated fixture bodies.
  Remaining largest hand-written file in Refractor is `health/lattice_heartbeater.go` (2487) — untouched by
  this item and never in its scope; `cypher_parser.go` (22K) is generated. **No successor row filed**: the
  scope this row named is complete, and the board does not grow by reviewing. The durable output beyond the
  line counts is the method — cut on verified blank-line boundaries, descending order, prove zero added
  lines and a byte-identical or multiset-identical body — plus the gate that now enforces its one recurring
  failure mode automatically.
