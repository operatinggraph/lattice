---
title: Story 4.4 Implementation Handoff Brief
story: 4.4 — Duplicate Identity Detection (FR3)
model_tier: Sonnet (locked)
token_budget: ~110K (estimate; for tracking only — not a halt threshold)
session: Fresh implementation session
architecture_lead: Winston
date: 2026-05-17
predecessor: Story 4.3 (Two-Phase Identity Claim, shipped at 677747c; progress update at 1bbf992)
---

# Story 4.4 — Duplicate Identity Detection (FR3): Handoff Brief

## Your Role

Replace the `ScanIdentityDuplicates` `NotYetImplemented` stub in the identity DDL Starlark script (shipped by 4.1) with a real implementation. An operator-tier actor submits one op that scans **all current identity vertices** (Phase 1 ≤500 per NFR-SC1), applies three match criteria, and flags pairs for human review by writing the `state` aspect (`claimed`/`unclaimed → flagged-for-review`) and a bidirectional `duplicateOf` aspect on each member. No automated merge ever — FR3 mandate.

Your work covers:

1. **Levenshtein builtin** — a new Starlark module `strings` with `strings.levenshtein_ratio(a, b) -> float` (and helper `strings.levenshtein(a, b) -> int`).
2. **Hydrator scan-prefix extension** — `contextHint.scanPrefix` field that the hydrator interprets as "load every vertex under this prefix into state." Phase 1 small enough (≤500 identities) that an in-memory fetch is acceptable.
3. **A new Starlark global helper `state.keys_with_prefix(prefix) -> list_of_keys`** so the script can enumerate the hydrated identities.
4. **`ScanIdentityDuplicates` script branch** — pairwise scan (O(N²) acceptable at N≤500), three criteria (Levenshtein name, exact lowercase email, exact phone), MutationBatch with state transitions + bidirectional `duplicateOf` aspects, EventList with `IdentityDuplicateCandidateFlagged` per pair, response detail with breakdown.
5. **Refractor `capabilityenv` extension** — when the identity's `state` aspect is `flagged-for-review`, inject `pendingReview: true` into the projected `cap.identity.<X>` envelope. Localized change (~30 LOC Go) — Phase 1 acceptable per AC's "their cap.<actorId> receives a pendingReview: true indicator" clause.
6. **Integration tests** — create 5 near-duplicates → scan → assert correct pairs + attribution + idempotency + claimed-actor `pendingReview` projection.

After 4.4 ships, Story 4.5 (operator-approved merge) closes Epic 4.

## 🔴 MANDATORY OPERATING RULES (READ FIRST)

- **No worktree.** Work directly in `/Users/andrewsolgan/Documents/GitHub/Lattice` against branch `main`. Verify with `pwd` at startup.
- **No commits, no pushes.** Stage your changes; DO NOT call `git commit` or `git push`. Winston commits + pushes after review.
- **Planning artifacts are read-only.** Drift → append to `cmd/processor/CONTRACT-AMENDMENT-REQUEST.md` and continue. Despite the AC's wording, do NOT edit `data-contracts.md`: Decision #15 below explains why (Winston correction — file is for cross-component contracts, not internal-to-DDL behavioral specs; the Starlark script + identity DDL `.description` aspect already document the algorithm).
- **Token budget is for tracking only, NOT a halt threshold.** Estimate ~110K. Record outer-telemetry actual at session close.
- **Halt and escalate** on stuck-loop patterns (re-attempting same fix 3+ times; immediate reverts; cycling between failed approaches; stuck on a test fail you can't reduce after two debug attempts).
- **Checkpoint every 8-10 tool calls OR after any deliverable OR after any file read >25KB.**
- **Model tier:** Sonnet only. Halt if Opus/Haiku.
- **Architecture binding:** Contract #1 §1.3 + §1.5 (envelopes + keys), Contract #3 (script return shape), Contract #6 §6.2 (cap doc shape — for the pendingReview injection), epics.md Story 4.4 (lines 1138-1170), FR3 (no automated merge), NFR-SC1 (Phase 1 ≤500 identities).
- **Lint watch:** golangci-lint v2 on CI flags unused helpers (4.2 + 4.3 both shipped unused helpers that broke CI). Before declaring done, run `golangci-lint run ./...` locally (binary at `/Users/andrewsolgan/go/bin/golangci-lint`). 0 issues = green.
- **Token tracker:** update Row 4.4 at session close with outer-telemetry actual.
- **Andrew has authorized autonomous proceed.**

## What's Already in Place (do NOT redo)

- **Identity DDL** (Story 4.1) — `permittedCommands` already includes `ScanIdentityDuplicates`. **No DDL surface change needed.**
- **Identity DDL script** at `internal/bootstrap/identity_ddl.go` — stub branch for `ScanIdentityDuplicates` returns `script_error("NotYetImplemented", "Story 4.4: ScanIdentityDuplicates")`. **Replace that branch.**
- **`ScanIdentityDuplicates` permission** (scope=any) granted to backOfHouse + operator (2 links). No new permission needed.
- **`crypto.sha256` / `sha256NanoID` / `constant_time_equal`** Starlark builtins exist (4.2 + 4.3). Levenshtein is a separate concern (string math, not crypto); put it in a new `strings` module per Decision #1 below.
- **`ScriptResult.ResponseDetail` + `OperationReply.Detail`** plumbing (4.2). Use for the operator-visible breakdown.
- **State machine helpers from 4.1** (`validate_state_transition`, `enforce_not_merged`) — useful for the per-target state mutation. The state machine already allows `claimed → flagged-for-review` and `unclaimed → flagged-for-review`. Re-entering `flagged-for-review` from `flagged-for-review` is rejected (same-state); the script must skip the state mutation in that case.
- **Refractor `capabilityenv` wrapper** at `internal/refractor/capabilityenv/` from Story 3.2a/b — builds the Contract #6 §6.2 envelope. **Extend it** to read the identity's state aspect and inject `pendingReview: true` when state == flagged-for-review. Decision #5 below.
- **Step 4 hydrator** at `internal/processor/step4_hydrate.go` — reads keys listed in `contextHint.Reads` (vertex + aspect keys); does NOT currently support prefix scans. **Extend it** to also read `contextHint.ScanPrefix` (string) and bulk-load all matching vertices via `substrate.KVListKeys` + per-key reads. Decision #2 below.
- **Starlark `state` global** at `internal/processor/starlark_runner.go` exposes per-key reads. **Add a method `state.keys_with_prefix(prefix) -> list`** so the script can enumerate what the hydrator loaded. Decision #3 below.
- **`substrate.KVListKeys`** exists (Story 1.7) for KV-bucket key listing. Use it in the hydrator extension.

Tree is clean at session start (commit `1bbf992` after `677747c`; verify-bootstrap 154 OK; test-bypass 4/4 BLOCKED; test-capability-adversarial 4/4 DEFENDED; full `go test ./... -p 1 -count=1` green; golangci-lint 0 issues).

## Story Scope (4.4)

### 1. New Starlark builtin module: `strings`

In `internal/processor/starlark_builtins.go`:
- Add `stringsModule()` constructor returning a struct with two methods:
  - `strings.levenshtein(a: string, b: string) -> int` — classical DP edit distance (replace/insert/delete cost 1 each). Pure / deterministic / O(len(a)*len(b)) time + O(min) space (rolling row).
  - `strings.levenshtein_ratio(a: string, b: string) -> float` — `1 - dist / max(len(a), len(b))`; returns `1.0` if both empty.
- Error on wrong arity / wrong type.

In `starlark_runner.go`:
- Expose the new module as global `strings`.

In `starlark_builtins_test.go`:
- Unit tests for `strings.levenshtein` (identical strings → 0; single substitution → 1; insertion → 1; classic "kitten/sitting" → 3) and `strings.levenshtein_ratio` (identity → 1.0; "smith"/"smyth" → ≥ 0.8). 4-6 cases.

**Decision context:** Sandbox principles allow pure deterministic functions. Levenshtein is pure and side-effect-free. No I/O, no time, no randomness.

### 2. Hydrator scan-prefix extension

In `internal/processor/script_context.go` (or wherever `ContextHint` is defined):
- Add a field `ScanPrefixes []string`. Optional / nil-or-empty skipped.

In `internal/processor/step4_hydrate.go`:
- After processing `contextHint.Reads` as today, **for each prefix in `contextHint.ScanPrefixes`**:
  - Validate the prefix is one of `vtx.identity.` or `lnk.identity.` (Phase 1 only ScanIdentityDuplicates uses this; reject other prefixes with `HydrationError` to limit blast radius).
  - Call `substrate.KVListKeys(ctx, "core-kv", prefix)`.
  - For each returned key, read the value via the existing path and add to the hydrated state map.
  - **Soft cap (per scan)**: if scan returns > 1000 keys, return `HydrationError("scan-too-large", count)` (NFR-SC1 says ≤500 identities; the link space is bounded by O(N²) but typical operation will be small; >1000 indicates either an attack or a Phase 2 cell that needs the deferred batch-streaming path).
  - For `vtx.identity.` prefix: retain only 3-segment vertex keys (skip any aspect keys) AND additionally load 4 hard-coded aspects per vertex (`.name`, `.email`, `.phone`, `.state`).
  - For `lnk.identity.` prefix: retain all 6-segment link keys (the script will filter to `duplicateOf` relations itself).

**Subtlety**: the script's per-identity work needs aspect reads, but pre-loading all aspects for all identities would be wasteful. **Decision (cost-aware)**: the hydrator extension only loads vertices via scan; the script will additionally enumerate keys and issue per-identity aspect reads via `state.read(key + ".name")` etc., which the hydrator must also resolve. **Two paths**:
  - (a) **Pre-load all `vtx.identity.<id>.{name,email,phone,state}` aspects** alongside the vertex when ScanPrefix is set. Simpler. Cost: 4N aspect reads at hydrate time. At N≤500 → 2000 reads; embedded NATS handles this in tens of ms. Acceptable.
  - (b) Lazy reads inside the script via a special builtin that calls back into Go. Invasive.

**Decision: option (a)**. The hydrator, when `ScanPrefix == "vtx.identity."`, loads each scanned vertex AND four aspects: `.name`, `.email`, `.phone`, `.state`. Hard-coded list (it's the only consumer of ScanPrefix in Phase 1). Document in the closing summary.

### 3. New Starlark `state` method: `state.keys_with_prefix(prefix) -> list`

In `starlark_runner.go` where the `state` global is built:
- Add a method that returns `[]starlark.String` containing the keys present in the hydrated state map that start with the given prefix.
- Error on wrong arity / wrong type.

This lets the script enumerate the identities the hydrator loaded.

### 4. The `ScanIdentityDuplicates` script branch

Replace the stub in `identityDDLScript`. The branch:

**Input** (optional override):
- `op.payload.levenshteinThreshold` — float, default 0.85. Reject values outside [0.0, 1.0] with `script_error("InvalidArgument", "levenshteinThreshold: out of [0,1]")`. Echo back in response detail for audit.

**`duplicateOf` is a LINK, not an aspect.** Aspects describe a single vertex (name, email, state); cross-vertex relationships are links. Per Contract #1 §1.5, link key shape is `lnk.<typeA>.<idA>.<relation>.<typeB>.<idB>` (6 segments). Link envelope data carries the relationship's payload — links DO have a `data` field. See §6.13 / Story 3.6 for the precedent (holdsRole, grantsPermission, reportsTo all use this pattern).

**Caller `contextHint`** (test fixture / Loom):
- `contextHint.scanPrefixes = ["vtx.identity.", "lnk.identity."]` (the hydrator now supports `[]string` per the §2 update below). Hydrator pre-loads all identity vertices + 4 aspects each + all `lnk.identity.*` link envelopes (so the script can do existence checks for prior `duplicateOf` pairs without round-trips).

**Inside the script:**
- `identity_keys = state.keys_with_prefix("vtx.identity.")` — filter to 3-segment vertex keys (skip aspect keys with extra `.` segments and skip any 6-segment link keys if they happen to share the prefix).
- Build a list of identity records: `[{key, name_norm, email_norm, phone_norm, state}, ...]` from the loaded aspects. Normalize:
  - name: lowercase + trim
  - email: lowercase + trim (skip empty)
  - phone: strip non-digit/non-`+` (skip empty)
  - state: pass-through; tombstoned / merged identities are SKIPPED from comparison (treat `merged` as out-of-scope; don't surface them as candidates).
- Pairwise compare (i < j by NanoID to avoid double-counting):
  - **Exact email match** (both non-empty + equal): pair flagged with criterion `exact-email`.
  - **Exact phone match** (both non-empty + equal): pair flagged with criterion `exact-phone`. If a pair already matched via email, append `exact-phone` to its criteria list (don't double-flag).
  - **Levenshtein name** (both non-empty): if `strings.levenshtein_ratio(a_name_norm, b_name_norm) >= threshold`, criterion `levenshtein-name` with the computed ratio as `confidence`.
- Result: a list of `{aKey, bKey, criteria: [...], confidence: <max ratio or 1.0 for exact>}` records.

**Link key shape** for each pair — single canonical link per pair (the relationship is symmetric):
- Extract NanoIDs from both vertex keys: `aId`, `bId` (the `<NanoID>` segment of `vtx.identity.<NanoID>`).
- Sort: `lowID, highID = sorted([aId, bId])` lexicographically.
- Link key: `"lnk.identity." + lowID + ".duplicateOf.identity." + highID`.
- Class: `duplicateOf`.
- Link envelope `data`: `{criteria: [...], confidence: <float>, scanRequestId: op.envelope.requestId, flaggedAt: op.envelope.observedAt}`.

**Idempotency** — for each candidate pair `(A, B)`:
- Compute the canonical link key as above.
- `state.read(linkKey)` — if non-`None` AND `.isDeleted != True`, SKIP this pair entirely (don't re-write the link, don't re-mutate state aspects).

**MutationBatch** for each non-skipped pair:
- **One** link create (NOT two aspects) at the canonical link key, with class `duplicateOf` and the data payload above.
- For each member of the pair whose current state is NOT `flagged-for-review`: emit an aspect mutation on `.state` setting `value: "flagged-for-review"`. SKIP the state mutation when the member is already flagged (same-state validator would reject) — track this count separately as `skippedAlreadyFlagged`.
  - **State transition validity**: the 4.1 script's `validate_state_transition` helper enforces transitions. The script must call it before issuing the `.state` mutation. Allowed: `unclaimed → flagged-for-review`, `claimed → flagged-for-review`. NOT allowed: `merged → flagged-for-review` (merged identities are filtered out of the candidate pool earlier; this is belt-and-braces).

**EventList** — one `IdentityDuplicateCandidateFlagged` event per flagged pair: data `{linkKey, aKey, bKey, criteria, confidence}`.

**Response detail** (via `ScriptResult.ResponseDetail`):
- `{totalScanned: N, candidatesFound: M, skippedExistingPairs: K, breakdown: {exact-email: x, exact-phone: y, levenshtein-name: z}, pairs: [{aKey, bKey, criteria, confidence}, ...], levenshteinThreshold: 0.85, scanRequestId: ...}`
- This is consumer-side / operator-visible. Plaintext fine (no PII secrets — names, emails, phones are in normal aspects).

**LOC target:** ~80 LOC for this branch (largest in the script). Total identityDDLScript (4.1 + 4.2 + 4.3 + 4.4) should stay <340 LOC. If you cross ~360 LOC, escalate before adding shared-library mechanism.

### 5. Refractor `capabilityenv` `pendingReview` injection

In `internal/refractor/capabilityenv/wrapper.go` (or whatever the file is named):
- During envelope build for `cap.identity.<X>`, read the identity's `vtx.identity.<X>.state` aspect via the existing pipeline state access (the wrapper today reads anchor + lens-def revisions for `projectedFromRevisions` — adding one more read is the same pattern).
- If state aspect's data `value == "flagged-for-review"`, inject top-level field `pendingReview: true` into the envelope. Else omit (do NOT write `pendingReview: false` — absence == false).
- Update Contract #6 §6.2 envelope shape in your data-contracts addendum to mention `pendingReview` as an optional boolean field.

**Why this is in scope** (despite Story 4.1's Decision #10 "no Refractor change"): the AC for 4.4 explicitly requires this projection. Decision #10 in 4.1 applied to that story's scope only. 4.4's AC binds the Refractor change.

**LOC target:** ~30 LOC including a test. Mirror the existing pattern in the wrapper.

### 6. Algorithm + threshold documentation (NO `data-contracts.md` edit)

The AC text says "algorithm, thresholds, normalization rules documented in `data-contracts.md` as addendum to identity DDL." **Do NOT add to `data-contracts.md`** (Decision #15 below). Instead, document the algorithm in two places that are already the right home:

1. **The identity DDL's `.description` aspect** (already emitted by Story 4.1 at bootstrap). **You do NOT modify the bootstrap-seeded description** — that would require a primordial-data migration. Instead, the script's own comments + the response detail's structure (criteria names, threshold echo) constitute the operator-visible algorithm spec.
2. **The Starlark script body** in `identity_ddl.go` carries a top-of-branch comment block (~10 lines) summarizing: three criteria, normalization rules, default threshold, link-based output, idempotency. The script IS the canonical spec.

This satisfies the AC's intent (operator visibility + audit) without polluting the cross-component contracts file. Note this decision in the closing summary.

### 7. Integration tests in `internal/processor/identity_scan_test.go` (NEW file)

Capability-mode wiring (mirror `identity_create_test.go` from 4.2 and `identity_claim_test.go` from 4.3). Seed an operator-role test actor.

- **`TestScanDuplicates_FindsAllCriteria`**: seed 5 identities: A+B (exact-email match), C+D (exact-phone match), E+F (levenshtein-name match, e.g. "Alice Smith" vs "Alyce Smyth"). Mix of `unclaimed` + `claimed` starting states. Submit `ScanIdentityDuplicates`. Assert response detail's `pairs[]` contains exactly 3 pairs with the expected `criteria`, all 6 identities transitioned to `flagged-for-review`, **3 `lnk.identity.<lo>.duplicateOf.identity.<hi>` link envelopes exist** with the correct class + data shape (criteria/confidence/scanRequestId/flaggedAt), `IdentityDuplicateCandidateFlagged` events per pair.
- **`TestScanDuplicates_RespectThreshold`**: seed 2 names with ratio ~0.7. Default threshold 0.85 → no pair. Override payload threshold to 0.5 → pair found. Assert threshold echoed in response detail.
- **`TestScanDuplicates_Idempotent`**: run scan twice with different requestIds. First flags pairs; second sees existing `duplicateOf` links via the hydrator's `lnk.identity.` scan and skips re-mutation. Assert response `skippedExistingPairs > 0` and `candidatesFound == 0` for the new pair count. Link envelope revisions unchanged after second run.
- **`TestScanDuplicates_SkipsMergedIdentities`**: seed 3 identities A+B+C where C has `state: "merged"`. A and C would match on email, but C is filtered out. Assert pair (A,B) is found if they match; (A,C) is not.
- **`TestScanDuplicates_ClaimedIdentity_StaysOperational`**: seed a claimed identity (with role grants) that matches another. Submit scan. After flag, the claimed identity's cap doc shows `pendingReview: true` AND retains its `platformPermissions[]`. Poll cap KV with 1s deadline for the projection. Also assert a follow-up op submitted as that identity passes step 3 (continues to operate).
- **`TestScanDuplicates_NonOperatorDenied`**: consumer-role actor → step 3 denies. The `ScanIdentityDuplicates` permission is operator+backOfHouse only.
- **`TestScanDuplicates_NoCandidates`**: seed 3 unique identities. Submit scan. Assert response `candidatesFound: 0`, breakdown all zeros, no mutations.

Plus the **Refractor wrapper unit test**: in `internal/refractor/capabilityenv/*_test.go`, add a focused test that builds the envelope for an identity whose state aspect is `flagged-for-review` and asserts `pendingReview: true`. And one for `unclaimed` asserting absence.

Total ~7 integration tests + 1-2 wrapper unit tests.

### 8. Verify-bootstrap

No new primordial entries in 4.4. The 154 OK count should remain 154.

### 9. Bypass + Gate 3 re-audit

Confirm `make test-bypass` (4/4 BLOCKED) and `make test-capability-adversarial` (4/4 DEFENDED) remain green. The new `strings` builtin is pure / deterministic — no I/O escape risk. If any gate flips, STOP and escalate.

**Out of scope:**
- Story 4.5 (operator-approved merge).
- Unicode normalization (Phase 2 — Phase 1 ASCII lowercase only).
- Full E.164 phone normalization (Phase 2 — Phase 1 strip non-digit/non-+).
- Phonetic / Soundex / Metaphone matching (Phase 2+).
- Async / streaming scan for >1000 identities (Phase 2 — Phase 1 in-memory at ≤500 per NFR-SC1).
- Per-operator scan history / audit trail beyond what envelope tracking already captures.
- Cap-doc `pendingReview` notifications to staff (read-only flag; UI consumes it).
- Anti-replay specifics beyond step-2 dedup.
- New buckets or new lenses.

**Hard escalation triggers (append to `cmd/processor/CONTRACT-AMENDMENT-REQUEST.md` then move on):**
- Hydrator's existing structure can't accept a `ScanPrefix` field without invasive change.
- `substrate.KVListKeys` doesn't filter to vertex-only or doesn't accept a prefix argument cleanly.
- `capabilityenv` wrapper's existing API doesn't expose aspect-read access for the projected identity (would require pipeline plumbing change).
- An integration test reveals a real auth perimeter regression.
- Bypass or Gate 3 vector flips from green.

## Architectural Decisions Already Made (Winston)

1. **Levenshtein in a new `strings` module** (not in `crypto` — semantically wrong). Pure / deterministic / O(N²) acceptable at Phase 1 scale. Unit tests cover the standard suite.

2. **Hydrator `ScanPrefix` extension**: hard-limited to `vtx.identity.` prefix in Phase 1. Other prefixes return `HydrationError("scan-prefix-not-supported")`. The hydrator also loads 4 hard-coded aspects per scanned vertex (`.name`, `.email`, `.phone`, `.state`) so the script doesn't need to issue 4N additional reads. This is a domain-specific optimization documented in the closing summary.

3. **`state.keys_with_prefix(prefix)`** is a script-side enumerator over the hydrated state. The hydrator doesn't expose an iterator; the script gets a snapshot list.

4. **Idempotency via `duplicateOf` aspect inspection** before each pair-write. The aspect's `partners` list grows append-only (cleanup is 4.5 merge-time or later). Re-scan with same partners → skip. Re-scan with new partner for same identity → append.

5. **Refractor `capabilityenv` extension is in scope** for 4.4. Story 4.1's "no Refractor change" was 4.1-specific. 4.4's AC requires `pendingReview` projection. Keep the wrapper change localized (~30 LOC); add a focused wrapper unit test.

6. **State mutation skipped when state is already `flagged-for-review`** to avoid same-state rejection by 4.1's state-machine validator. Track `skippedExistingPairs` and `skippedAlreadyFlagged` counts in response detail.

7. **Merged identities are filtered out** of the candidate pool entirely (not even considered for matching). The script must check `state == "merged"` before pairwise comparison. Tombstoned vertices (`isDeleted: true`) are similarly filtered.

8. **Operator threshold override** is a payload field `levenshteinThreshold`. Default 0.85. Audit: envelope captures the full payload, which includes the override.

9. **No new Health KV signal for scan attempts.** Stories 3.5's auth-trace and 3.3's claim-attempts cover their respective ops; the scan op is operator-tier and audit lives in the envelope + response detail. If Phase 2 wants observability, add it then.

10. **No new permission grants.** 4.1 already seeded `ScanIdentityDuplicates` permission + grants to backOfHouse + operator. Step 3 auth match by operationType.

11. **No `data-contracts.md` edit** (Winston correction to AC). See Decision #15.

12. **`pendingReview` projection latency** is bounded by NFR-P3 (Refractor reprojection ≤500ms). Test polls cap KV with a 1s deadline (Story 3.2b precedent). If flake, raise to 2s — do NOT lower below 1s without escalation.

13. **Integration tests use capability mode**, NOT stub mode. Mirror 4.1/4.2/4.3 patterns.

14. **No CI gate change.** All of: `make verify-bootstrap` (154 OK), `make test-bypass` (4/4 BLOCKED), `make test-capability-adversarial` (4/4 DEFENDED), full `go test ./... -p 1 -count=1`, `golangci-lint run ./...` 0 issues.

15. **No `data-contracts.md` edit, despite AC text** (Winston correction). The file is for cross-component interface contracts (envelope shapes, cap doc, operation envelope, JetStream subjects). Match-criteria thresholds and normalization rules are internal-to-the-identity-DDL behavioral specs — they belong with the script, not next to inter-component contracts. The AC's directive was reasonable in the abstract but `data-contracts.md` has accreted into a grab-bag pending a future cleanup; do not compound the problem. Document the algorithm via the script's own top-of-branch comment block + the response detail structure (which the operator sees). The identity DDL's bootstrap-seeded `.description` aspect already states the aspect schemas + intent; no migration needed.

16. **No new CONTRACT-AMENDMENT-REQUEST expected.** If one emerges, append + move on.

## Required Context — Read These Only

| File | Why |
|---|---|
| `_bmad-output/implementation-artifacts/story-4.3-handoff-brief.md` | Predecessor — most directly analogous template |
| `_bmad-output/implementation-artifacts/story-4.2-handoff-brief.md` | crypto builtin + ScriptResult.ResponseDetail pattern |
| `_bmad-output/implementation-artifacts/story-4.1-handoff-brief.md` | Identity DDL helpers + state-machine validator |
| `_bmad-output/planning-artifacts/data-contracts.md` Contract #1 §1.3 + §1.5 | Envelope + key shape |
| `_bmad-output/planning-artifacts/data-contracts.md` Contract #3 | MutationBatch + EventList + ScriptResult |
| `_bmad-output/planning-artifacts/data-contracts.md` Contract #6 §6.2 + §6.13 | Cap envelope shape + role/perm DDL precedent |
| `_bmad-output/planning-artifacts/epics.md` Story 4.4 (lines 1138-1170) | Your AC |
| `internal/bootstrap/identity_ddl.go` | **Edit this** — replace ScanIdentityDuplicates stub |
| `internal/processor/starlark_builtins.go` | **Edit this** — add stringsModule + levenshtein |
| `internal/processor/starlark_builtins_test.go` | Add unit tests |
| `internal/processor/starlark_runner.go` | **Edit this** — register `strings` global + add `state.keys_with_prefix` |
| `internal/processor/script_context.go` | **Edit this** — add `ContextHint.ScanPrefix` |
| `internal/processor/step4_hydrate.go` | **Edit this** — scan-prefix branch loading vertices + 4 aspects |
| `internal/refractor/capabilityenv/wrapper.go` (or equivalent) | **Edit this** — pendingReview projection |
| `internal/refractor/capabilityenv/*_test.go` | Add wrapper unit test |
| `internal/processor/identity_state_machine_test.go` | Fixture pattern from 4.1 |
| `internal/processor/identity_create_test.go` | Fixture pattern from 4.2 |
| `internal/processor/identity_claim_test.go` | Fixture pattern from 4.3 |
| `internal/substrate/kv.go` (or wherever KVListKeys lives) | Confirm signature |

**DO NOT read**: `lattice-architecture.md` (full), full `epics.md` beyond Story 4.4 + Epic 4 framing, full `data-contracts.md` beyond cited sections + your addendum target location, Materializer source, vendored ANTLR parser, Refractor source beyond capabilityenv, Stories 1.x/2.x/3.1-3.5 briefs, Capability Lens cypher.

## Suggested Sequence

**Phase A — Levenshtein builtin (target ~10K tokens):**
1. Add `stringsModule()` + levenshtein/levenshtein_ratio in `starlark_builtins.go`. Wire into runner globals. Unit tests.

**Phase B — Hydrator scan-prefix extension (target ~15K tokens):**
2. Add `ContextHint.ScanPrefix` field. Extend step 4 to scan + load vertices + 4 aspects per vertex when ScanPrefix is set. Add a small hydrator test.

**Phase C — `state.keys_with_prefix` script global (target ~5K tokens):**
3. Add the method to the `state` global in `starlark_runner.go`. Quick smoke test if convenient.

**Phase D — Script branch (target ~30K tokens):**
4. Replace `ScanIdentityDuplicates` stub in `identity_ddl.go`. Pairwise scan, three criteria, idempotency, state mutations, events, response detail.

**Phase E — Refractor wrapper extension (target ~15K tokens):**
5. Add `pendingReview` injection in capabilityenv wrapper. Add focused unit test.

**Phase F — Integration tests (target ~25K tokens):**
6. Write `internal/processor/identity_scan_test.go` with 7 tests. Iterate until all pass.

**Phase G — data-contracts.md §6.14 addendum (target ~5K tokens):**
7. Append the addendum. Keep tight (~60 lines).

**Phase H — Gates + closing (target ~10K tokens):**
8. Run all required gates locally; iterate until clean. Include `golangci-lint run ./...`.
9. Update token tracker Row 4.4.
10. Closing summary appended to brief as Deliverable #12.

## Required Verification

```bash
go build ./...
make vet
golangci-lint run ./...                      # 0 issues required
go test ./internal/processor/... -count=1
go test ./internal/refractor/... -count=1
make verify-bootstrap                        # 154 OK unchanged
make test-bypass                             # 4/4 BLOCKED
make test-capability-adversarial             # 4/4 DEFENDED
go test ./... -p 1 -count=1                  # all packages green
```

## Deliverables Checklist

1. ✅ `internal/processor/starlark_builtins.go` — `stringsModule` with levenshtein + levenshtein_ratio
2. ✅ `internal/processor/starlark_builtins_test.go` — unit tests
3. ✅ `internal/processor/starlark_runner.go` — `strings` global + `state.keys_with_prefix`
4. ✅ `internal/processor/script_context.go` — `ContextHint.ScanPrefix` field
5. ✅ `internal/processor/step4_hydrate.go` — scan-prefix branch (load vertices + 4 aspects)
6. ✅ `internal/refractor/capabilityenv/*.go` — pendingReview projection + wrapper unit test
7. ✅ `internal/bootstrap/identity_ddl.go` — real `ScanIdentityDuplicates` branch
8. ✅ `internal/processor/identity_scan_test.go` — 7 integration tests
9. ✅ All required gates green (including golangci-lint 0 issues)
10. ✅ Token tracker Row 4.4 updated with outer-telemetry actual
11. ✅ Closing summary appended to brief as Deliverable #11

## What 4.4 Is NOT

- **Not** the merge operation (Story 4.5).
- **Not** Unicode normalization or phonetic matching.
- **Not** automatic merge of any kind (FR3 explicit mandate).
- **Not** new permission seeding (4.1 covers).
- **Not** new Capability Lens cypher change (only the capabilityenv wrapper).
- **Not** Async / streaming scan at scale (Phase 2).
- **Not** Cross-cell coordination.

## Escalation

Append to `cmd/processor/CONTRACT-AMENDMENT-REQUEST.md` (and move on) for:
- Hydrator structural change blocks ScanPrefix extension
- `substrate.KVListKeys` API mismatch
- `capabilityenv` wrapper doesn't expose aspect-read access
- AC text disagrees with contract text in a non-trivial way

Halt entirely and surface to Winston for:
- Bypass or Gate 3 vector flips from green
- Stuck-loop pattern per operating rules
- Real auth perimeter regression in integration tests

## Closing

1. Verify all 12 deliverables
2. Run all required gates locally
3. Update token tracker Row 4.4
4. Closing summary as Deliverable #12

**DO NOT commit. DO NOT push.** Winston commits + pushes after review.

---

## Deliverable #11 — Closing Summary (Story 4.4, Continuation Session)

**Session date:** 2026-05-17 (continuation of the same-day first session)
**Model tier:** Sonnet (claude-sonnet-4-6) — both sessions

### What This Session Fixed

The first session implemented all Story 4.4 deliverables but used the wrong data model for `duplicateOf`: it wrote bidirectional *aspect* mutations (`vtx.identity.<id>.duplicateOf`) on each member of a pair. Per Contract #1 §1.5 and the corrected brief (Decision #15), cross-vertex relationships are encoded as *links*, not aspects. The continuation session replaced the aspect-based model with the canonical link-based model throughout.

### Changes Made in This Session

**`internal/processor/envelope.go`** — Renamed `ScanPrefix string` → `ScanPrefixes []string` (plural). Updated doc comment to document both `vtx.identity.` and `lnk.identity.` as allowed prefixes.

**`internal/processor/step4_hydrate.go`** — Extended `hydrateScanPrefix` to support both `vtx.identity.` and `lnk.identity.` prefixes. For `lnk.identity.`: collects 6-segment link keys (5 dots), applies the same 1000-key soft cap, loads each link envelope as-is (no aspect expansion). Removed `duplicateOf` from `identityScanAspects` (it is no longer an aspect). Updated the ScanPrefix → ScanPrefixes iteration loop.

**`internal/bootstrap/identity_ddl.go`** — Rewrote the `ScanIdentityDuplicates` Starlark branch:
- Added 10-line top-of-branch comment block per Decision #15: three criteria, normalization rules, default threshold 0.85, link-based output, idempotency via lnk.identity.* pre-load.
- Idempotency check: `state.read(linkKey)` on canonical `lnk.identity.<lowID>.duplicateOf.identity.<highID>` — skips pair if non-tombstoned link exists.
- Emits a single `create` mutation per pair at the canonical link key (class=`duplicateOf`, data={criteria, confidence, scanRequestId, flaggedAt}).
- Event payload includes `linkKey` field per brief §4.
- Removed the two-aspect (bidirectional) mutation pattern.

**`internal/processor/identity_scan_test.go`** — Replaced `ScanPrefix: "vtx.identity."` with `ScanPrefixes: []string{"vtx.identity.", "lnk.identity."}` in `publishScanOp` and the denial test. Replaced `readDuplicateOfPartners` / `partnerKeyInList` helpers with `canonicalLinkKey`, `readLinkEnvelope`, `linkEnvelopeData`, `linkCriteriaContains`. Updated all test assertions to check canonical link keys rather than aspect keys. `TestScanDuplicates_Idempotent` now asserts link envelope *revision* unchanged (not aspect revision).

### Algorithm Documentation (Decision #15)

Per Decision #15 the algorithm spec lives in two places already:
1. The 10-line top-of-branch comment block in `identity_ddl.go`'s `ScanIdentityDuplicates` branch.
2. The response detail structure (`criteria`, `confidence`, `levenshteinThreshold`, `scanRequestId`) which the operator sees on every scan reply.

No `data-contracts.md` edit was made. No `CONTRACT-AMENDMENT-REQUEST` was raised.

### Domain-Specific Optimization Note

The hydrator, when `ScanPrefixes` includes `"vtx.identity."`, loads each scanned vertex AND four aspects: `.name`, `.email`, `.phone`, `.state`. This is a hard-coded Phase 1 optimization (2000 reads max at N≤500 identities; embedded NATS handles this in tens of ms — NFR-SC1 acceptable). Adding `"lnk.identity."` to the same `ScanPrefixes` slice pre-loads all existing `duplicateOf` links so the script's idempotency check is a cheap in-memory dict lookup with no back-channel round-trips.

### Gate Results (All Green)

```
go build ./...                              PASS
make vet                                    PASS
golangci-lint run ./...                     0 issues
go test ./internal/processor/... -count=1  PASS (29s)
go test ./internal/refractor/... -count=1  PASS (all packages)
make verify-bootstrap                       154 OK (unchanged)
make test-bypass                            4/4 BLOCKED
make test-capability-adversarial            4/4 DEFENDED
go test ./... -p 1 -count=1                all packages green
```

### Deliverables Checklist (Final)

1. ✅ `internal/processor/starlark_builtins.go` — `stringsModule` with levenshtein + levenshtein_ratio (first session)
2. ✅ `internal/processor/starlark_builtins_test.go` — unit tests (first session)
3. ✅ `internal/processor/starlark_runner.go` — `strings` global + `state.keys_with_prefix` (first session)
4. ✅ `internal/processor/envelope.go` — `ContextHint.ScanPrefixes []string` (this session; renamed from ScanPrefix)
5. ✅ `internal/processor/step4_hydrate.go` — scan-prefix branch supporting both `vtx.identity.` and `lnk.identity.` (this session; first session had singular prefix support)
6. ✅ `internal/refractor/capabilityenv/envelope.go` + test — pendingReview projection (first session)
7. ✅ `internal/bootstrap/identity_ddl.go` — link-based `ScanIdentityDuplicates` branch with canonical link key model (this session; first session used wrong aspect model)
8. ✅ `internal/processor/identity_scan_test.go` — 7 integration tests asserting link envelopes (this session; first session had aspect-based assertions)
9. ✅ All required gates green (including golangci-lint 0 issues)
10. ✅ Token tracker Row 4.4 updated with two-session note
11. ✅ This closing summary
