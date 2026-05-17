---
title: Story 4.2 Implementation Handoff Brief
story: 4.2 — Staff Creates Unclaimed Identity (FR1)
model_tier: Sonnet (locked)
token_budget: ~90K (estimate; for tracking only — not a halt threshold)
session: Fresh implementation session
architecture_lead: Winston
date: 2026-05-17
predecessor: Story 4.1 (Identity Domain DDL & State Machine, shipped at 3cb5a06; progress update at 2273898)
---

# Story 4.2 — Staff Creates Unclaimed Identity (FR1): Handoff Brief

## Your Role

Replace the `CreateUnclaimedIdentity` `NotYetImplemented` stub in the identity DDL Starlark script (shipped by 4.1) with a real implementation that:

1. Validates required fields (`name` mandatory; at least one of `email`/`phone`).
2. Generates a deterministic NanoID claim key, stores its SHA-256 hash in the `claimKey` aspect, and returns the plaintext claim key in the response (the staff actor delivers it out-of-band to the prospective claimant).
3. Performs **synchronous exact-match duplicate detection** on email/phone via SHA-256-hashed index vertices; on a hit, the new identity is seeded with `state: "flagged-for-review"` and the response carries `possibleDuplicateFlag: true`.
4. Writes the new `vtx.identity.<NanoID>` vertex + `name`/`email`/`phone`/`state`/`claimKey` aspects + index vertices in one atomic batch via the standard step-8 path.
5. Emits an `IdentityCreated` event (and an `IdentityFlaggedForReview` event when the duplicate path fires).
6. Honors idempotency: same `requestId` resubmission produces identical identity key + identical claim key (already guaranteed by deterministic NanoID + step-2 dedup; verified by test).

After 4.2 ships, Story 4.3 (Two-Phase Identity Claim) will use the same SHA-256 hash comparison to validate submitted plaintext claim keys.

## 🔴 MANDATORY OPERATING RULES (READ FIRST)

- **No worktree.** Work directly in `/Users/andrewsolgan/Documents/GitHub/Lattice` against branch `main`. Verify with `pwd` at startup.
- **No commits, no pushes.** Stage your changes; DO NOT call `git commit` or `git push`. Winston commits + pushes after review.
- **Planning artifacts are read-only.** Drift → append to `cmd/processor/CONTRACT-AMENDMENT-REQUEST.md` and continue.
- **Token budget is for tracking only, NOT a halt threshold.** Estimate ~90K. Record outer-telemetry actual at session close.
- **Halt and escalate** on stuck-loop patterns (re-attempting after 3+ failures; immediate reverts; cycling between failed approaches; stuck on a test fail you can't reduce after two debug attempts).
- **Checkpoint every 8-10 tool calls OR after any deliverable OR after any file read >25KB.**
- **Model tier:** Sonnet only. Halt if Opus/Haiku.
- **Architecture binding:** Contract #1 §1.3 + §1.5 (envelopes + keys), Contract #3 (script return shape), epics.md Story 4.2 (lines 1082-1104), FR1, FR7, NFR-S6 (PII leak prevention).
- **Token tracker:** update Row 4.2 at session close with outer-telemetry actual.
- **Andrew has authorized autonomous proceed.**

## What's Already in Place (do NOT redo)

- **Identity DDL** at `vtx.meta.<IdentityDDLID>` (Story 4.1, commit `3cb5a06`). `permittedCommands` already lists `CreateUnclaimedIdentity` — no DDL surface change needed.
- **Identity DDL script** at `internal/bootstrap/identity_ddl.go`'s `identityDDLScript` constant. Stub branch for `CreateUnclaimedIdentity` returns `script_error("NotYetImplemented", "Story 4.2: CreateUnclaimedIdentity")`. **Replace that branch with your real implementation.** Keep the existing helpers (`validate_state_transition`, `enforce_not_merged`, `read_state`, `read_merged_into`) — they don't apply here but they're shared with other branches.
- **CreateUnclaimedIdentity permission** (`vtx.permission.<CreateUnclaimedIdentityID>`, scope=any) granted to frontOfHouse/backOfHouse/operator (3 links). Capability Lens projects this; an actor holding any of those roles passes step 3.
- **Step 4 hydration** (`step4_hydrate.go`): reads aspects requested via `op.contextHint.aspects`. Pre-existence checks (`state.read("vtx.identityIndex.<hash>")`) require the hydrator to populate `state` with those keys. **You need to request them via `contextHint`** (see Decision #4 below).
- **Step 5 sandbox** (`starlark_runner.go` + `starlark_builtins.go`): globals `state`, `op`, `ddl`, `nanoid`. No `time`, `os`, `load`, `http`, `open`. **You will add `crypto.sha256(s) -> hex_string`** per Decision #2.
- **`nanoid.new()`** is deterministic per `requestId` (sha256-seeded PCG); takes no arguments; returns a 20-char base57 string (~117 bits entropy — adequate for one-time-use claim keys per Decision #1).
- **`state.read(key)`** returns the hydrated document or `None`. Use to check for index-vertex existence.
- **Step 8 commit** uses substrate atomic batch — `MutationBatch` from the script becomes one all-or-nothing JetStream publish.
- **Step 9 event publication** publishes EventList entries to `events.<event-type>` subject; the script declares `{eventType, data}`.

Tree is clean at session start (commit `2273898` after `3cb5a06`; verify-bootstrap 154 OK; test-bypass 4/4 BLOCKED; test-capability-adversarial 4/4 DEFENDED; full `go test ./... -p 1 -count=1` green).

## Story Scope (4.2)

### 1. New Starlark sandbox builtin: `crypto.sha256(s)`

In `internal/processor/starlark_builtins.go`:
- Add a `cryptoModule()` constructor returning a Starlark struct with one method: `sha256(s) -> string`.
- Input: a single Starlark string (UTF-8 bytes).
- Output: lowercase hex-encoded SHA-256 digest (64 chars).
- Error on wrong arity / wrong type — match the pattern of `nanoid.new()`'s arity check.
- Pure function — no PRNG seed, no state. The hash builtin is deterministic by definition.

In `internal/processor/starlark_runner.go` (or wherever the sandbox globals are assembled):
- Expose the new module as global `crypto`.

In `internal/processor/starlark_builtins_test.go` (or wherever the existing test file is — check the test file structure):
- Add a focused unit test: `crypto.sha256("hello")` returns the known hex digest `2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824`.
- Test wrong arity rejected; non-string rejected.

**Decision context:** Sandbox principles (Story 1.6) forbid I/O / time / os / http. A pure hash function is side-effect-free + deterministic, consistent with those principles. The builtin's purpose is to store hashes of sensitive tokens in Core KV without leaking plaintext. Story 4.3 will use the same builtin to hash incoming plaintext for comparison.

### 2. Index vertex class: `identityIndex`

A new vertex class `identityIndex` is introduced for synchronous exact-match duplicate detection. **No DDL meta-vertex** for this class (Phase 1 minimal — only the user-facing identity class has full DDL; index vertices are internal infrastructure). Step 6 validator passes index-vertex writes because the operation's primary DDL is `identity` and `permittedCommands` is keyed by operationType, not by target key class. Confirm by reading `step6_validate.go` — if it rejects writes to `vtx.identityIndex.*` because there's no DDL, **escalate** (this would be a step-6 gap, not a 4.2 scope item).

Index vertex shape:
- Key: `vtx.identityIndex.<sha-prefix>` where `<sha-prefix>` is the first 20 hex chars of `sha256("email:" + normalized_email)` or `sha256("phone:" + normalized_phone)`. The prefix-typed key (`email:` / `phone:`) prevents accidental collision across contact types.
- Class: `identityIndex`
- Data: `{contactType: "email"|"phone", identityKey: "vtx.identity.<NewID>"}`

**Why a 20-char SHA prefix:** Contract #1 §1.5 requires the third vertex segment to be a NanoID (22 chars typical). A hex prefix of the same length is acceptable (still 3-segment, no dots in the third segment). 20 hex chars = 80 bits of collision resistance — adequate for an index across millions of identities. If `substrate.ClassifyKey` rejects hex chars in the third segment, fall back to NanoID-keyed index entries with the SHA hex as the **data** field and a separate lookup mechanism (escalate if you hit this — would require a contract amendment).

Normalization:
- Email: trim + lowercase + reject empty.
- Phone: strip non-digit and non-`+` chars; reject empty. (Full E.164 normalization is a Phase 2 carry — the description aspect on the identity DDL already mentions E.164 normalization as the goal; for 4.2, simple stripping is sufficient.)

### 3. The `CreateUnclaimedIdentity` script branch

Replace the stub in `identityDDLScript`. The branch must:

**Input validation:**
- `op.payload.name` is a non-empty string ≤ 200 chars → else `script_error("InvalidArgument", "name: required, maxLen 200")`.
- At least one of `op.payload.email` / `op.payload.phone` is a non-empty string → else `script_error("InvalidArgument", "email or phone: at least one required")`.
- Normalize email (trim+lowercase) and phone (strip non-digit/non-+). If both empty after normalization → same error.

**Lookup pre-existing index vertices** (via hydrated state — see Decision #4 for contextHint):
- For each present contact (email and/or phone), compute the index key.
- `state.read(indexKey)` → if non-`None` AND `.isDeleted != True`, this is a duplicate.
- `duplicate = (email_hit is not None) or (phone_hit is not None)`.

**Compute new identity key + claim key:**
- `identity_id = nanoid.new()` → deterministic per requestId.
- `identity_key = "vtx.identity." + identity_id`.
- `claim_key_plaintext = nanoid.new()` → second deterministic NanoID. **Order matters** — `nanoid.new()` has an internal counter, so two successive calls yield two distinct IDs. Document the call order in a Starlark comment.
- `claim_key_hash = crypto.sha256(claim_key_plaintext)`.

**Build MutationBatch:**
- Vertex create: `identity_key`, class `identity`, data `{}` (aspects carry the payload).
- Aspect create `identity_key + ".name"`, data `{value: name_normalized}`.
- Aspect create `identity_key + ".email"`, data `{value: email_normalized}` — only if email present.
- Aspect create `identity_key + ".phone"`, data `{value: phone_normalized}` — only if phone present.
- Aspect create `identity_key + ".state"`, data `{value: "flagged-for-review" if duplicate else "unclaimed"}`.
- Aspect create `identity_key + ".claimKey"`, data `{hash: claim_key_hash, algo: "sha256"}` — **the aspect stores the HASH, never plaintext**.
- Index vertex create for each present contact: key `vtx.identityIndex.<prefix>`, class `identityIndex`, data `{contactType, identityKey: identity_key}`.

**EventList:**
- `IdentityCreated` with data `{identityKey, state, duplicate}`.
- If duplicate: also `IdentityFlaggedForReview` with data `{identityKey, reason: "duplicate-contact"}`.

**Response:**
- The script's return is `(MutationBatch, EventList)` per Contract #3. The **plaintext claim key** must travel back to the caller. **Decision #5 below** specifies the mechanism: a top-level `responseDetail` field on the script return, surfaced by the commit-path reply builder.

**Script LOC target:** The branch should fit in ~40 LOC. If you cross 60 LOC, factor a helper. Total script (including 4.1's helpers + stubs) should remain <180 LOC. If it crosses 200 LOC, escalate before adding a shared-library mechanism.

### 4. Hydration: contextHint extension

The hydrator (step 4) reads `op.contextHint.aspects` and `op.contextHint.vertices` (check the actual struct in `script_context.go` — field names may vary). For 4.2:
- The op envelope's `contextHint` requests reads of `vtx.identityIndex.<email-prefix>` and `vtx.identityIndex.<phone-prefix>` based on the payload.
- **Problem:** the index keys depend on the payload (you need to compute the hash before knowing what to read). The hydrator runs BEFORE step 5; how does it know which keys to fetch?
- **Resolution:** the **caller** (test fixture or Loom in real deployment) computes the prefixes and populates `contextHint.vertices` with the precomputed index keys. The Starlark script trusts these populated reads. If the caller forgets, the script falls back to "no duplicate" (acceptable Phase 1 — duplicate detection is best-effort, full coverage is 4.4's batch job).
- **Alternative resolution** (cleaner): teach step 4 to evaluate a small expression in `contextHint` like `{aspects: [], vertices: ["vtx.identityIndex." + sha256_prefix(email), ...]}`. **Out of scope for 4.2** — that's a hydrator enhancement. Document the caller-precomputes approach as the Phase 1 path.

**Decision summary:** test fixtures and downstream callers populate `contextHint.vertices` with the precomputed index keys; the script reads via `state.read`. If absent (caller didn't populate), the duplicate check evaluates to "no duplicate" and no flag fires. Document in closing summary as a Phase 1 simplification.

### 5. Reply-builder: surface plaintext claim key

The Processor's commit path produces a reply envelope (see `internal/processor/reply.go`). Today the reply contains decision/revisions/etc. (Story 1.7+1.8). 4.2 adds a way for the script to attach a structured detail map to the success reply.

Approach:
- Extend `ScriptResult` (in `script_context.go`) with an optional `ResponseDetail map[string]any` field.
- The Starlark script returns it as a top-level field alongside MutationBatch + EventList — e.g., the script-result struct gains a `response` slot. **Check the actual ScriptResult / return-tuple shape in code** — the script may currently return `(mutations, events)` and you need to extend to `(mutations, events, response)` or use a struct-style return.
- The reply builder reads `ScriptResult.ResponseDetail` and includes it in the reply envelope as a `detail` field (or whatever the existing field is — Story 3.4 added `details` for denial; mirror that shape for success).
- For 4.2, the script populates `responseDetail = {identityKey, claimKey: claim_key_plaintext, possibleDuplicateFlag: duplicate}`.

**NFR-S6 / NFR-S7 (no PII in logs):** the reply envelope's `detail` map MUST NOT be auto-logged. Audit the reply path's logging to confirm; if `detail` is logged, add a redaction or scrub it before logging. Document in closing summary.

### 6. Integration tests

In `internal/processor/identity_create_test.go` (NEW file; mirror the capability-mode wiring used in 4.1's `identity_state_machine_test.go`):

- **`TestCreateUnclaimed_Success`**: staff actor (operator role) submits `CreateUnclaimedIdentity` with name+email+phone; assert step 8 commit, vertex exists, all 5 aspects (name/email/phone/state=unclaimed/claimKey-hash) exist, both index vertices exist, response detail contains plaintext claimKey + identityKey + possibleDuplicateFlag=False, event IdentityCreated published.
- **`TestCreateUnclaimed_MissingName_Rejected`**: payload `{email}` (no name) → `InvalidArgument` from script.
- **`TestCreateUnclaimed_MissingBothContacts_Rejected`**: payload `{name}` only → `InvalidArgument`.
- **`TestCreateUnclaimed_NormalizesEmailLowercase`**: payload `{name, email: "Foo@BAR.com"}` → email aspect stored as `foo@bar.com`; index key uses normalized form.
- **`TestCreateUnclaimed_DuplicateEmail_FlagsForReview`**: pre-seed identity A with email `x@y.com`; pre-seed index vertex; second create with same email → response `possibleDuplicateFlag: true`, new identity's state aspect == `flagged-for-review`, `IdentityFlaggedForReview` event published.
- **`TestCreateUnclaimed_NonStaffActor_Denied`**: consumer-role actor → step 3 denies with `OperationNotPermitted` (Story 3.4 denial shape, reason `MissingPermission` or similar). The Capability Lens already excludes consumer from `CreateUnclaimedIdentity` per 4.1's grant matrix.
- **`TestCreateUnclaimed_Idempotent`**: submit same requestId twice → second call returns same identity key + same claim key plaintext (step-2 dedup short-circuit AND deterministic NanoID confirm both layers).
- **`TestCreateUnclaimed_ClaimKeyAspectStoresHashOnly`**: after a successful create, read `identity.claimKey` aspect directly via substrate; assert `data.hash` is 64 hex chars + `data.algo == "sha256"`; assert plaintext does NOT appear anywhere in the aspect envelope JSON. NFR-S6 proof.

Total ~7-8 tests. Fixture setup is heavy but each test is short.

### 7. Verify-bootstrap

No new primordial entries in 4.2. The 154 OK count should remain 154. If your changes inadvertently bump it (e.g., you added a primordial test fixture), escalate.

### 8. Bypass-suite re-audit

Confirm `make test-bypass` (4/4 BLOCKED) and `make test-capability-adversarial` (4/4 DEFENDED) remain green. The new `crypto.sha256` builtin slightly enlarges the sandbox surface — Bypass #3 (Starlark I/O escape) test should still report BLOCKED since `crypto.sha256` is not an I/O escape. If it flips, STOP and escalate.

**Out of scope:**
- Story 4.3 (ClaimIdentity) — hash comparison + state transition to `claimed`.
- Story 4.4 (Levenshtein duplicate detection) — batch-only, separate Refractor or Loom job.
- Story 4.5 (Approved merge) — operator-driven merge with `mergedInto` aspect write.
- Tombstone semantics for index vertices (they accumulate; cleanup is 4.5 or later).
- Full E.164 phone normalization (4.2 ships simple strip; full normalization is Phase 2).
- `claimKey` read-restriction enforcement at the aspect read layer (Phase 1 path: store hash only, never plaintext; future Personal/Secure Lens may add per-aspect read auth).
- Refractor / Capability Lens changes (none).
- `responseDetail`-style reply extension beyond what 4.2 needs (don't over-design — narrow to what's needed for plaintext claim key delivery).
- Per-aspect logging redaction beyond what NFR-S6 audit reveals.

**Hard escalation triggers (append to `cmd/processor/CONTRACT-AMENDMENT-REQUEST.md` then move on):**
- Step 6 validator rejects `vtx.identityIndex.*` writes for lack of DDL (would require step-6 gap fix or new DDL).
- `substrate.ClassifyKey` rejects hex chars in the third segment of `vtx.identityIndex.<hex-prefix>` (would require key-shape rework).
- `ScriptResult`/`script_context.go` cannot accept a structured response field without major surface change (suggest a narrower mechanism in the CAR).
- Hydrator's `contextHint` shape cannot accept pre-computed index keys (would require hydrator change).
- Bypass or Gate 3 vector flips from green.

## Architectural Decisions Already Made (Winston)

1. **ClaimKey generation uses `nanoid.new()` (single call) for ~117 bits entropy.** AC text "32-byte" is brief-imprecision. NanoID-style base57 token is sufficient for an out-of-band one-time-use claim token. Document in closing summary; Phase 2 hardening = wire to crypto/rand for true CSPRNG.

2. **ClaimKey aspect stores SHA-256 hash, not plaintext.** AC's "read-restriction" wording (aspect read-restricted to issuing op + ClaimIdentity validator) is brief-imprecision. Phase 1 has no per-aspect read auth; the equivalent semantic is "the aspect doesn't contain plaintext at all." Story 4.3 hashes submitted plaintext + compares to the stored hash. This is the standard secure-token pattern. Document in closing summary as the resolution to the AC's read-restriction clause.

3. **New `crypto.sha256(s)` Starlark builtin** is in scope for 4.2. Pure / deterministic / side-effect-free — does NOT violate sandbox principles. No other crypto primitives are introduced (no random_bytes, no hmac, no symmetric). Story 4.3 reuses the same builtin.

4. **Synchronous duplicate detection via `vtx.identityIndex.<hex-prefix>` vertices.** SHA-256 prefix (first 20 hex chars = 80 bits) as the key segment. Class `identityIndex`. Per-contact-type prefixing (`email:` / `phone:`) in the hash input to prevent cross-contact collision. No DDL meta-vertex for `identityIndex` (Phase 1 minimal — index vertices are infrastructure, not user-facing). Index lookup happens via hydrator-populated state reads (caller pre-computes the keys in `contextHint`). Full Levenshtein fuzzy detection remains 4.4 batch-only.

5. **Plaintext claim key surfaces via a new `responseDetail` field on `ScriptResult`** + reply builder. The reply envelope's existing `detail` field (Story 3.4 added it for denials) is the carrier. Audit NFR-S6 / NFR-S7 logging paths so `detail` is not logged.

6. **Caller responsibility for `contextHint`**: tests and Loom-side callers precompute the index-key hashes and populate `contextHint.vertices`. The hydrator does NOT auto-derive them. If the caller omits them, the script reads `None` and proceeds with `duplicate=False` (acceptable Phase 1 — best-effort detection; 4.4 batch is the safety net). Document in closing summary.

7. **Phone normalization is "strip non-digit/non-+"** for 4.2. Full E.164 (country-code inference, regional handling) is a Phase 2 carry.

8. **Idempotency proof comes from two layers**: (a) Step 2 dedup tracker (Stories 1.5/1.7) short-circuits exact-requestId resubmits, and (b) deterministic NanoID seeded from `sha256(requestId)` guarantees same key on retries before tracker write. Test covers (a) on the resubmit case.

9. **Step 6 validator already permits the op** via `permittedCommands: [..., "CreateUnclaimedIdentity", ...]` (4.1 seeded). Step 6 keys validation off the operation type's parent DDL (the `identity` DDL declares the op) — confirm by reading the code at `step6_validate.go`. If step 6 actually requires the operation's targetKey to match a DDL-permitted prefix per-op, escalate (that would be a 4.1 gap to remediate).

10. **No new permissions or grant links.** 4.1 already seeded `CreateUnclaimedIdentity` permission + grants to fr/bh/op roles. Step 3 authorizer reads `cap.<actor>.platformPermissions[]` and matches operationType.

11. **No Refractor change.** Capability Lens does not project identityIndex or claim-key data; it only cares about role/permission/grantsPermission/holdsRole/reportsTo and (via 3.2b) task→assignedTo→identity ephemeral grants. Identity creation is invisible to the Capability Lens.

12. **Test fixtures use capability mode** (Story 3.3 wiring), NOT stub mode. Mirror 4.1's `identity_state_machine_test.go` for the operator-actor seeding pattern.

13. **CI gate**: after your changes, CI must go green. All of: `make verify-bootstrap` (154 OK unchanged), `make test-bypass` (4/4 BLOCKED), `make test-capability-adversarial` (4/4 DEFENDED), full `go test ./... -p 1 -count=1`.

14. **No new CONTRACT-AMENDMENT-REQUEST expected.** If one emerges, append + move on.

## Required Context — Read These Only

| File | Why |
|---|---|
| `_bmad-output/implementation-artifacts/story-4.1-handoff-brief.md` | Predecessor — most directly analogous template + identity DDL seeding |
| `_bmad-output/planning-artifacts/data-contracts.md` Contract #1 §1.3 + §1.5 | Envelope + key shape |
| `_bmad-output/planning-artifacts/data-contracts.md` Contract #3 | MutationBatch + EventList + ScriptResult return shape |
| `_bmad-output/planning-artifacts/epics.md` Story 4.2 (lines 1082-1104) | Your AC |
| `internal/bootstrap/identity_ddl.go` | **Edit this** — replace CreateUnclaimedIdentity stub branch |
| `internal/processor/starlark_builtins.go` | **Edit this** — add cryptoModule + sha256 builtin |
| `internal/processor/starlark_runner.go` | Where sandbox globals are wired — register `crypto` |
| `internal/processor/script_context.go` | ScriptResult shape — possibly extend with ResponseDetail |
| `internal/processor/step4_hydrate.go` | contextHint shape; how state.read keys are populated |
| `internal/processor/step5_execute.go` | How ScriptResult flows to commit path |
| `internal/processor/step6_validate.go` | Confirm permittedCommands gate; confirm no per-key-class block on identityIndex writes |
| `internal/processor/reply.go` | Reply envelope shape; where to surface ResponseDetail in success replies |
| `internal/processor/identity_state_machine_test.go` | Test fixture pattern from 4.1 |
| `internal/processor/integration_test.go` | Capability-mode wiring pattern |
| `internal/processor/role_mgmt_integration_test.go` | Capability-mode wiring pattern from 3.6 (alternative reference) |
| `internal/substrate/keys.go` (or wherever ClassifyKey lives) | Quick check: hex chars allowed in 3rd segment? |

**DO NOT read**: `lattice-architecture.md` (full), full `epics.md` beyond Story 4.2 + Epic 4 framing, full `data-contracts.md` beyond cited sections, Materializer source, vendored ANTLR parser, Refractor source, Stories 1.x/2.x/3.1-3.5 briefs, Capability Lens cypher.

## Suggested Sequence

**Phase A — Sandbox builtin (target ~10K tokens):**
1. Add `crypto.sha256` builtin in `starlark_builtins.go`. Wire into globals in `starlark_runner.go`. Add unit test for the known digest.

**Phase B — ScriptResult extension (target ~10K tokens):**
2. Read `script_context.go` + `step5_execute.go` + `reply.go`. Determine the minimum change to thread a `responseDetail` map from script return → ScriptResult → success reply.
3. Implement the extension. Keep narrow.

**Phase C — Identity script branch (target ~20K tokens):**
4. Replace `CreateUnclaimedIdentity` stub in `identity_ddl.go`'s `identityDDLScript`. Implement input validation, normalization, index lookup, mutation batch, event list, response detail.
5. Local Starlark parse-roundtrip via existing `starlark_runner` (no full pipeline yet).

**Phase D — Integration tests (target ~30K tokens):**
6. Write `internal/processor/identity_create_test.go` with the ~7-8 tests. Iterate until all pass.
7. Verify the duplicate-flag path against pre-seeded index vertices.

**Phase E — Gates + closing (target ~10K tokens):**
8. Run all required gates locally; iterate until clean.
9. Update token tracker Row 4.2.
10. Closing summary appended to brief as Deliverable #11.

## Required Verification

```bash
go build ./...
make vet
go test ./internal/processor/... -count=1
make verify-bootstrap                        # expect 154 OK unchanged
make test-bypass                             # 4/4 BLOCKED
make test-capability-adversarial             # 4/4 DEFENDED
go test ./... -p 1 -count=1                  # all packages green
```

## Deliverables Checklist

1. ✅ `internal/processor/starlark_builtins.go` — cryptoModule + sha256
2. ✅ `internal/processor/starlark_runner.go` — `crypto` global registered
3. ✅ `internal/processor/starlark_builtins_test.go` (or wherever) — sha256 unit test
4. ✅ `internal/processor/script_context.go` + `step5_execute.go` + `reply.go` — minimal `responseDetail` plumbing
5. ✅ `internal/bootstrap/identity_ddl.go` — `CreateUnclaimedIdentity` branch real implementation
6. ✅ `internal/processor/identity_create_test.go` — 7-8 integration tests
7. ✅ All local gates green
8. ✅ `make verify-bootstrap` 154 OK (unchanged)
9. ✅ `make test-bypass` 4/4 BLOCKED
10. ✅ `make test-capability-adversarial` 4/4 DEFENDED
11. ✅ Token tracker Row 4.2 updated with outer-telemetry actual
12. ✅ Closing summary appended to brief as Deliverable #12

## What 4.2 Is NOT

- **Not** ClaimIdentity (4.3) — no hash comparison on submission yet.
- **Not** Levenshtein fuzzy duplicate detection (4.4 batch).
- **Not** Merge (4.5).
- **Not** per-aspect read-restriction enforcement — hash-only-storage is the Phase 1 equivalent.
- **Not** crypto/rand-backed claim key — deterministic NanoID is acceptable for Phase 1.
- **Not** index-vertex tombstone or cleanup — accumulates; later story handles.
- **Not** full E.164 phone normalization — simple strip suffices.
- **Not** any Refractor or Capability Lens change.

## Escalation

Append to `cmd/processor/CONTRACT-AMENDMENT-REQUEST.md` (and move on) for:
- Step 6 validator rejects `vtx.identityIndex.*` writes
- `ClassifyKey` rejects hex prefix
- `ScriptResult` cannot accept structured response without major surface change
- Hydrator `contextHint` shape blocks pre-computed index reads
- AC text disagrees with contract text in a non-trivial way

Halt entirely and surface to Winston for:
- Bypass or Gate 3 vector flips from green
- Stuck-loop pattern per operating rules
- Sandbox builtin addition seems to expand attack surface in unexpected ways

## Closing

1. Verify all 12 deliverables
2. Run all required gates locally
3. Update token tracker Row 4.2
4. Closing summary as Deliverable #12

**DO NOT commit. DO NOT push.** Winston commits + pushes after review.
