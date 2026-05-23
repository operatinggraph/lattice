---
title: identity-hygiene Surface-B end-to-end tests — Handoff Brief
story: Phase-1 hygiene carry — close Story 4.6 Surface-B deferral
model_tier: Sonnet (locked)
token_budget: ~50K (tracked)
date: 2026-05-23
predecessor: 4.6 + 4.7 + 4.7-cleanup
---

# identity-hygiene Surface-B Tests — Handoff Brief

## Background

Story 4.6 §7 specified Surface B — 8 end-to-end tests for the
identity-hygiene package — but **deferred** them with the rationale
"no full Refractor + Processor + hygiene-package harness exists." The
4.7-cleanup round closed half of that gap by shipping
`internal/testutil/{install_phase1_packages.go, pipeline.go,
embedded_nats.go}` — the *Processor*-side harness. The Refractor-side
projection harness still isn't in testutil.

This brief authors the tests that the existing harness supports
cleanly, and explicitly defers the ones that would require a Refractor
projection fixture in testutil.

## 🔴 Mandatory operating rules

- `cd /Users/andrewsolgan/Documents/GitHub/Lattice`. No worktrees.
- No commits, no pushes. Winston commits.
- No edits to `_bmad-output/planning-artifacts/*`. CARs go to
  `cmd/processor/CONTRACT-AMENDMENT-REQUEST.md`.
- Model: Sonnet. Budget ~50K. Halt on stuck-loop only.

## Starting state

- HEAD: `5497a0d`. Tree clean.
- Pattern to follow: `packages/identity-domain/{create_test.go,
  claim_test.go, state_machine_test.go, testhelpers_test.go}` —
  these are the canonical examples of "package authors a Capability
  Package and ships end-to-end tests against the real Processor
  pipeline via testutil."

## What you're writing

A new file (or files) under `packages/identity-hygiene/` with
`package identityhygiene_test` (external test package) that
exercises the MergeIdentity flow end-to-end through the real
Processor pipeline. Tests should follow the same `setupTestEnv` /
`newCreatePipeline`-style helpers established by identity-domain
tests.

## Tests to author (IN SCOPE)

These all run against the Processor pipeline. The Refractor's
duplicateCandidates Lens is **NOT** exercised — tests seed the lens
output bucket entries directly (mirroring how identity-domain tests
seed CapabilityDoc via `testutil.SeedCapDoc` rather than running a
real Capability Lens projection).

Author these in `packages/identity-hygiene/merge_test.go`:

1. **TestMerge_HappyPath** — install packages; seed primary + secondary
   identities + 2 inbound links + 1 outbound link touching secondary;
   submit MergeIdentity with all 3 edge keys; assert: mutationCount
   matches, all 3 edges tombstoned, 3 new rewritten links created,
   `secondary.state = "merged"`, `secondary.mergedInto = primaryKey`,
   one IdentityMerged event published, ResponseDetail is commit-trace
   shape with no business data.

2. **TestMerge_EnumeratedEdgesFromLens** — same setup as HappyPath,
   but the test reads the *real* `duplicate-candidates` bucket entry
   (which the test seeds itself, simulating the lens output) and
   constructs the MergeIdentity envelope from `secondaryInboundEdges`
   + `secondaryOutboundEdges`. Asserts the merge succeeds. This is
   the realistic operator-CLI flow.

3. **TestMerge_RejectsFabricatedEdge** — submit MergeIdentity with
   one `edges` entry that is NOT a link envelope (e.g., a bare
   identity vertex key). Script must reject with `EdgeNotALink`. No
   mutations applied.

4. **TestMerge_RejectsEdgeNotTouchingSecondary** — submit
   MergeIdentity with an `edges` entry whose link endpoints are two
   unrelated identities (neither is `secondary`). Script rejects with
   `EdgeDoesNotTouchSecondary`. No mutations applied.

5. **TestMerge_RejectsTombstonedEdge** — submit MergeIdentity with
   an edge whose envelope has `isDeleted: true`. Script rejects with
   `EdgeNotFound`. No mutations applied.

6. **TestMerge_RejectsAlreadyMergedSecondary** — secondary's state is
   already `"merged"`. Submit MergeIdentity; script rejects via the
   `enforce_not_merged` guard in the identity DDL (or analogous logic
   in the hygiene script — verify which fires first by reading
   `packages/identity-hygiene/ddls.go`). No NFR-S6 leak: error
   message must NOT contain `mergedInto` value.

7. **TestMerge_PostMergeRedirect_FR4** — after a successful merge,
   submit a follow-up `UpdateIdentityState` op against the
   now-merged `secondary`. The identity DDL's `enforce_not_merged`
   guard returns the standard rejection (assert the error code; do
   not assert any leaked mergedInto detail per NFR-S6).

8. **TestMerge_NonOperatorActor_Denied** — submit MergeIdentity
   with a non-operator actor (e.g., a consumer-role identity seeded
   via identity-domain's PreInstall hook). Capability KV check at
   step 3 denies. Assert the reply's denial detail per Story 3.4
   shape.

## DEFERRED (out of scope this round)

The 4 Lens projection tests from the 4.6 brief §7 (Surface A in the
original numbering — `TestHygiene_LensProjection_ExactEmail`,
`_LevenshteinName`, `_SkipsMerged`, `_NFR_P3`) require a real
Refractor pipeline projecting the `duplicateCandidates` cypher rule.
The current testutil doesn't expose a Refractor projection fixture —
the closest analog is the manually-wired
`internal/refractor/refractor_capability_e2e_test.go`. Inventing a
fixture for the hygiene package would expand scope significantly.
**Skip these.** Note them in the closing summary as still-deferred,
with a one-liner pointing at what would be needed (a
`testutil.RefractorProjectionFixture` helper).

## Test fixture conventions

Mirror `packages/identity-domain/testhelpers_test.go`:
- `setupTestEnv(t)` returns `(ctx, *substrate.Conn)` — uses
  `testutil.SetupPackageTestEnv` (NATS + bootstrap + package install).
- Helper to construct a fresh identity vertex + write its initial
  aspects to Core KV.
- Helper to construct a link vertex envelope at
  `lnk.<typeA>.<idA>.<relation>.<typeB>.<idB>` and write it.
- Helper to seed a Capability Doc for the operator (so merge ops
  pass step-3 auth without a real Refractor running).

If two helpers are shared between merge_test.go and any other file
you add, factor them into `packages/identity-hygiene/testhelpers_test.go`.

## 🔴 Architectural-purity guardrails (binding)

These are the same rules as 4.6/4.7/2.4a/2.4b — non-negotiable.

- Don't touch the package's `ddls.go` / `lenses.go` / `permissions.go`.
  Those are canonical. If a test exposes a real bug, raise a CAR;
  don't fix the script.
- Don't touch any Processor or Refractor production code. The whole
  point is tests against the existing surface.
- The `MergeIdentity` script reads `edges` as a command parameter
  declared in `ContextHint.Reads`. Tests must construct envelopes
  with that shape — do NOT invent new ContextHint fields or scan
  patterns.
- Closing greps:
  ```
  grep -rn "AdjacencyReads\|LinkScans\|ScanPrefixes\|WithAdjacencyBucket\|AdjacencyForNode\|keys_with_prefix" internal/ cmd/ packages/   # zero operational hits
  ```

## Verification gates

- `go build ./...`
- `make vet`
- `golangci-lint run ./...` (0 issues)
- `go test ./packages/identity-hygiene/... -count=1` (all new tests pass)
- `go test ./... -p 1 -count=1` (no regressions; use `-p 1` per
  Deviation 14)
- Docker-dependent gates (`make verify-kernel`, `verify-package-*`,
  `test-bypass`, `test-capability-adversarial`) flagged for Winston/CI.

## Closing summary

Append to a NEW file
`_bmad-output/implementation-artifacts/identity-hygiene-surface-b-closing-summary.md`
with:
- Files added (full paths + LOC)
- Per-test list: name + one-line assertion summary
- The 4 deferred Lens-projection tests called out by name with the
  fixture gap explained
- Gate results
- Closing-grep result (verbatim)
- Any deviations from this brief + reason
- Token self-estimate

Then stop.

## Inputs to read first

- This brief
- `packages/identity-hygiene/{ddls.go, lenses.go, manifest.yaml, package.go, permissions.go}` (canonical reference for the package shape; do NOT modify)
- `packages/identity-domain/testhelpers_test.go` + `create_test.go` (canonical test pattern)
- `internal/testutil/{install_phase1_packages.go, pipeline.go, embedded_nats.go}` (harness surface)
- `packages/identity-hygiene/package_test.go` (existing structural test — confirm what's already covered so you don't duplicate)
