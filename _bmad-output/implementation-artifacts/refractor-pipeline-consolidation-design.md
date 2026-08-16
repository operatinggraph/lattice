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
- **Inc 3 not started.** `pipeline.go` is 3024 lines post-Inc-2, still the largest file in the package.
  Next fire: census `grep -n "^func " pipeline.go` fresh (line numbers shift with every increment) and
  pick the next self-contained thematic group — the `ConsumerFilter`/`NarrowedFilterEligible`/
  `broadFilterReason` narrowing-decision cluster (~1624–1993 pre-Inc-2) and the `handle`/`evalLinkFanOut`/
  `evalPlainAspectReprojection`/`evalPlainLinkReprojection`/`evalAspectFanOut`/`dispositionEvalErr`
  CDC-dispatch cluster (~2430–2875 pre-Inc-2) are both large, roughly contiguous, and thematically
  coherent — either is a reasonable next cut. Same rule as Inc 2: membership is the named functions, not
  a forced contiguous span; re-verify live, don't trust these pre-Inc-3 line numbers.
