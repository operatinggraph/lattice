---
title: identity-hygiene Surface-B end-to-end tests — Closing Summary
story: Phase-1 hygiene carry — close Story 4.6 Surface-B deferral
date: 2026-05-23
---

# Closing Summary

## Files Added

| File | LOC |
|------|-----|
| `packages/identity-hygiene/testhelpers_test.go` | 266 |
| `packages/identity-hygiene/merge_test.go` | 511 |

**Total new test LOC: 777**

## Per-Test List

All 8 tests authored in `packages/identity-hygiene/merge_test.go` (`package identityhygiene_test`):

1. **TestMerge_HappyPath** — Seeds primary + secondary identities + 3 link edges (2 inbound, 1 outbound). Submits MergeIdentity with all 3 edges declared. Asserts: OutcomeAccepted, secondary.state == "merged", secondary.mergedInto == primaryKey, all 3 original edges tombstoned, 3 new rekeyed edges created, IdentityMerged event in tracker.

2. **TestMerge_EnumeratedEdgesFromLens** — Seeds a `duplicate-candidates` bucket entry simulating Refractor output. Constructs MergeIdentity envelope from secondaryInboundEdges + secondaryOutboundEdges as the operator CLI would. Asserts: OutcomeAccepted, secondary.state == "merged", IdentityMerged event.

3. **TestMerge_RejectsFabricatedEdge** — Submits MergeIdentity with a bare identity vertex key (not a link) in the `edges` list. Asserts: OutcomeRejected (script fails EdgeNotALink), no state mutation.

4. **TestMerge_RejectsEdgeNotTouchingSecondary** — Submits MergeIdentity with an edge between two unrelated identities (neither is secondary). Asserts: OutcomeRejected (script fails EdgeDoesNotTouchSecondary), no state mutation.

5. **TestMerge_RejectsTombstonedEdge** — Seeds a link envelope with isDeleted=true, submits MergeIdentity referencing it. Asserts: OutcomeRejected (script fails EdgeNotFound), no state mutation.

6. **TestMerge_RejectsAlreadyMergedSecondary** — Seeds secondary with state="merged" pointing at another primary. Submits MergeIdentity. Asserts: OutcomeRejected (MergeStateRejected guard), no state mutation, mergedInto remains unchanged (NFR-S6: not mutated to new value).

7. **TestMerge_PostMergeRedirect_FR4** — Performs a successful merge (step 1), then submits UpdateIdentityState against the now-merged secondary on the same pipeline. Asserts: second op OutcomeRejected (identity DDL enforce_not_merged guard), secondary.state remains "merged".

8. **TestMerge_NonOperatorActor_Denied** — Submits MergeIdentity with a consumer-role actor that only has ClaimIdentity permission. Asserts: OutcomeRejected at step 3 (Capability KV auth denial), no state mutation.

## Deferred Lens-Projection Tests

The following 4 tests from the original Story 4.6 brief §7 are still deferred:

- **TestHygiene_LensProjection_ExactEmail** — requires the Refractor to project the `duplicateCandidates` cypher rule against a live Neo4j-equivalent graph with identities sharing exact email.
- **TestHygiene_LensProjection_LevenshteinName** — same; requires Levenshtein ratio computation over projected name aspects.
- **TestHygiene_LensProjection_SkipsMerged** — same; verifies merged identities are excluded from the cypher `WHERE state IN ['unclaimed', 'claimed']` filter.
- **TestHygiene_LensProjection_NFR_P3** — same; asserts projection latency is within NFR-P3 bounds.

**What is needed:** A `testutil.RefractorProjectionFixture` helper that spins up a Refractor pipeline with a real rule engine, projects the `duplicateCandidates` cypher rule against seeded Core KV identity data, and exposes the resulting `duplicate-candidates` bucket for assertion. The closest existing analog is `internal/refractor/refractor_capability_e2e_test.go`, but it tests the Capability lens projection — not graph-rule-based lenses. Wiring a graph-rule test fixture is significant scope.

## Gate Results

| Gate | Result |
|------|--------|
| `go build ./...` | PASS |
| `make vet` | PASS |
| `golangci-lint run ./...` | PASS (0 issues) |
| `go test ./packages/identity-hygiene/... -count=1` | PASS (12 tests, 8 new) |
| `go test ./... -p 1 -count=1` | PASS (all packages green; 1 intermittent flake in `internal/processor` pre-existing, confirmed absent on re-run) |

Docker-dependent gates (`make verify-kernel`, `verify-package-*`, `test-bypass`, `test-capability-adversarial`) flagged for Winston/CI.

## Closing Grep (verbatim)

```
$ grep -rn "AdjacencyReads\|LinkScans\|ScanPrefixes\|WithAdjacencyBucket\|AdjacencyForNode\|keys_with_prefix" internal/ cmd/ packages/
internal/processor/starlark_runner.go:372:// dict. Story 4.6 walk-back removed the `keys_with_prefix` custom
packages/rbac-domain/package_test.go:54:		"KVListKeys", "list_keys", "scan(", "keys_with_prefix",
packages/identity-domain/package_test.go:43:	for _, forbidden := range []string{"KVListKeys", "list_keys", "keys_with_prefix"} {
```

Zero operational hits. All matches are comments or test guard strings checking for forbidden patterns.

## Deviations from Brief

1. **`merge_test.go` vs `testhelpers_test.go` split** — The brief suggested the tests could be in "a file or files." I factored shared helpers into `testhelpers_test.go` (mirror of identity-domain's pattern) and put all 8 tests in `merge_test.go` as specified.

2. **`duplicate-candidates` KV bucket creation** — The `seedDuplicateCandidateEntry` helper in `testhelpers_test.go` calls `js.CreateOrUpdateKeyValue` to create the bucket on the fly. The pkgmgr installer writes the lens meta-vertex to core-kv but does NOT create the output bucket (that's the Refractor's job at runtime). This is the correct approach for tests simulating the operator-CLI flow without a live Refractor.

3. **TestMerge_PostMergeRedirect_FR4 pipeline sharing** — Used a single pipeline+consumer (`mpm1`) for both the merge op and the subsequent UpdateIdentityState op. Creating a second durable consumer that starts from the beginning would re-process the merge message (OutcomeDuplicate instead of Rejected for the update). Sharing the pipeline avoids this without any architectural compromise.

4. **TestMerge_RejectsAlreadyMergedSecondary NFR-S6 verification** — Cannot assert on error message text at the Processor level (no reply channel in DriveOne). NFR-S6 compliance is verified by asserting the secondary.mergedInto aspect remains unchanged (not mutated to the attempted-new primaryKey value). The script's error message `"MergeStateRejected: secondary state=merged"` (visible in WARN log) contains no mergedInto value, which is correct.

5. **NanoID enforcement discovery** — The first draft used 15-char hardcoded IDs; `substrate.ClassifyKey` returned `KindUnknown` for the resulting link keys, causing silent step-6 DDL violations. Fixed by replacing all hardcoded IDs with `testutil.GenReqID(label)` calls, which produce safe 20-char NanoID-alphabet strings.

6. **`Class: "identityHygiene"` on MergeIdentity envelopes** — The brief's example envelopes showed `Class: "identity"`, but the Hydrator resolves the DDL by the envelope's `Class` field against `DDLCache.Lookup(class)`. The identity-hygiene DDL has `CanonicalName: "identityHygiene"`, so MergeIdentity envelopes must carry `Class: "identityHygiene"`. The UpdateIdentityState op in test 7 correctly uses `Class: "identity"`.

## Token Self-Estimate

~38K tokens used (within the ~50K budget).
