# Runaway background-check re-runs × a broad consumer filter — `leaseApplicationComplete` cannot keep up

**Status: ✅ Winston-ratified — build-ready (2026-09-03).** No architectural fork, no contract change. Package work
(`packages/lease-signing`) built from the Lattice lane because the symptom is a platform one (a Refractor lens that
never drains). Raised by Andrew in chat (2026-09-03): "`leaseApplicationComplete` is projecting very slowly if at
all — new issue, or is the fix still not done?"

## 1. Answer, grounded

**New issue.** Neither shipped nor ratified work covers it: the ratified `WITH` closure row
(`with-alias-anchor-closure-design.md`) excludes actor-aggregate lenses three times over, and the hub-walk fix
(`1fca25cf`) named this lens and is active (`walkScoped: true` in its tallies) but only reduced its fallback rate.

**Live state (2026-09-03, shared dev stack):** durable `refractor-Yn698BZWmaqJuBHuYn69` on the broad filter
`$KV.core-kv.>`, 137,562 unprocessed, one ack in flight for 40+ s, `lensLatency` mean 1.0 s / p99 9.8 s,
`peakBindingRows 3637`, a rebuild "in flight" since 2026-08-26 that resumes on every restart (eight so far) and
suppresses the convergence sweep the whole time (`LensSweepStalled`), rows in `weaver-targets` up to two days stale.

**Root cause, two multiplied factors:**

1. **A runaway re-check loop.** `packages/lease-signing/freshness_window.go` sets the *production*
   `bgcheckFreshnessWindow` to `5m`. A completed background check lapses five minutes later, `MarkExpired` records
   the lapse, `leaseApplicationComplete`'s `freshBgComplete` drops to zero, `missing_bgcheck` re-opens, Weaver
   `triggerLoom backgroundCheck` mints a **new** service instance, the reply op stamps `validUntil = completedAt +
   5m`, and the cycle repeats — for every application that stays open, forever. The retry budget never exhausts,
   because every cycle is a *successful* completion followed by a fresh lapse. Census (Core KV, `lnk.service.*
   .providedTo.identity.*`): **12,281 of 12,289 `providedTo` links point at seven identities** —
   `identity.edu97ixj2CJB6auNi6L4` alone carries **3,637** `service.backgroundCheck.instance` vertices, minted
   continuously from 2026-07-31 to 2026-08-30 at roughly one every five to twelve minutes; `3637` is exactly the
   lens's `peakBindingRows`.
2. **A quadratic evaluation on a broad filter.** Every instance write fans out (`service → providedTo → identity →
   applicationFor → leaseapp`) to the application's row, and the readiness fragment `OPTIONAL MATCH
   (id)<-[:providedTo]-(inst:service)` then aggregates over all N instances of the applicant — N events × N-row
   aggregates. The lens also runs on the **broad** consumer filter (`filterBroadReason: non-exhaustive`): two
   `forOperation` targets in its cypher are unlabeled (`(sigOp)`, `(onbOp)`; six sibling sites in the corpus write
   `(op:meta)`), so every Core-KV write in the system is delivered to a handler that costs seconds. The 25 s CPU
   profile is I/O-bound (kevent + JSON decoding of thousands of KV reads per evaluation), not compute-bound.

## 2. Alternatives

| # | Option | Verdict |
|---|---|---|
| A | **Delete the loop's cause**: a realistic production window (30 days) — the re-check stops being a five-minute metronome | **Chosen** (one constant + README; the demo/e2e keep their own 25 s tag) |
| B | **Narrow the filter**: label the two `forOperation` targets `:meta` (and the same shape in `applicantOnboarding` / `staleUserTasks`) so the label set is exhaustive | **Chosen** (same package; the census pins flip to the derived mode) |
| C | Bound the readiness aggregate to the newest instance (`ORDER BY … LIMIT 1` in an OPTIONAL MATCH) | Rejected — the engine has no such clause; a lens-side bound also hides the growth instead of stopping it |
| D | Supersede a check when a newer one completes (tombstone the prior instance) | Not this fire — a Loom/pattern lifecycle decision with a real precedent (`SupersedeClause`, semantic-contracts) but a product question (is the check history a record?); revives if A leaves a nameable consumer wanting it |
| E | Platform: a rebuild that cannot drain must not suppress the sweep forever | Not this fire — with A+B the rebuild is expected to drain; re-measure first |
| F | Purge the 12k accumulated instances on the dev stack | **Andrew's call** — destructive on shared data; proposed in the report, not done |

A alone stops the growth; B alone halves the noise; together the lens receives only writes on its own labels and the
writes that arrive stop being a metronome. The 3,637-row aggregate stays until F or D — the per-event cost falls
only with fewer instances, which is why F is proposed alongside.

## 3. Mechanism

- `freshness_window.go`: `bgcheckFreshnessWindow = "720h"` (30 days), doc reworded (the value is the vendor's
  validity span; the runaway is the reason it is not minutes). README lines naming `5m` updated.
- `lenses.go`: `(sigOp:meta)`, `(onbOp:meta)` in `leaseApplicationCompleteSpec`; `(onbOp:meta)` in
  `applicantOnboardingSpec`; `(op:meta)` in `staleUserTasksSpec` — a `forOperation` target is always the op-meta
  vertex (`task forOperation meta`, Contract #1), so the label restricts nothing.
- Version bump `0.31.22 → 0.31.23` in `manifest.yaml` + `package.go`.
- Census pins (`internal/refractor/label_derivation_corpus_census_test.go`, and any sibling census whose verdict
  flips) re-pinned to the derived mode — read off the test's own failure, never guessed.

## 4. Fire brief (S)

Touch-list: `packages/lease-signing/freshness_window.go:23` · `lenses.go:859,862,1034,1074` · `manifest.yaml:2` ·
`package.go:92` · `README.md:146,195` · `internal/refractor/label_derivation_corpus_census_test.go:132,228,287`
(+ any other census pin the run flips). Green: `go test ./packages/lease-signing/ ./internal/refractor/ -run
'Census|Lens|Cypher' -count=1`, `DIFF_BASE=<base> go run ./scripts/lint-package-version.go`, lint-conventions,
lint-lens-anchors, lint-board. Review: lead review (S, package content). Live: `make reinstall-package
PKG=packages/lease-signing` on the running stack → the MATCH hot-reload's rebuild re-derives the filter
(`filterMode` narrowed) → no new instance minted after the last lapse → backlog trend re-measured.

## 5. Build note

**Shipped 2026-09-03 — `7e2ef6b2` (lease-signing 0.31.23), one worktree, lead review.** Census pins flipped for the
three lenses in three census files (label derivation `broad → narrowed-label`; actor one-key `walkMultiPosition →
oneKey`; walk scope `any:forOperation → meta:forOperation`), read off the tests' own verdicts.

**Live (shared dev stack, `make reinstall-package PKG=packages/lease-signing`):** `leaseApplicationComplete`
MATCH-hot-reloaded at 11:56:55, its rebuild re-derived the consumer filter to `narrowed-label` (7 labels, 21
subject filters), and the replay started at **67,104** messages instead of **137,562**. Drain rate measured over
three minutes: **~2 messages/min** (66,739 → 66,733) — the per-event cost is the 3,637-row readiness aggregate
on the seven runaway identities, and the narrowed replay's subjects are largely the runaway instances themselves
(36,826 `vtx.service` + 24,583 `instanceOf`/`providedTo` links). So A + B stop the growth and the noise; the
backlog and the suppressed sweep clear only once the accumulated instances are gone.

**Remaining, routed:** (F) purging the 12,281 accumulated `service.backgroundCheck.instance` vertices (all but the
newest per identity) is destructive on shared data and has **no sanctioned op** — `Tombstone*` commands exist for
patient / provider / appointment / location, none for a service instance — so it is proposed to Andrew, not
done. (D) the durable rule — a check superseded by its successor is tombstoned by the reply op, one live instance
per subject per pattern — is filed `📐` on the Lattice lane; `SupersedeClause` (semantic-contracts) is the shape
precedent, and whether a superseded check's history is a record is the product question the design must answer.
(E) a rebuild that cannot drain suppressing the sweep indefinitely stays unfiled until F/D land and the lens is
re-measured: with the instances gone the replay is expected to drain.
