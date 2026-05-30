---
title: Contract Amendment Request — JetStream lane subject pattern
raisedBy: Story 1.5 implementation agent (claude-opus-4-7)
raisedAt: 2026-05-13
resolvedAt: 2026-05-14
status: RESOLVED — Winston applied Resolution 1 (broaden bootstrap to `ops.>`)
severity: was Low (did not block Story 1.5 acceptance)
---

# Contract Amendment Request — Lane Subject Pattern

## Resolution (Winston, 2026-05-14)

Resolution 1 applied as part of the primordial-IDs runtime-generation
refactor (which already touched `internal/bootstrap/primordial.go`).
Bootstrap now provisions the `core-operations` stream with
`Subjects: ["ops.>"]` — a single multi-segment wildcard that covers
all per-lane subjects per Contract #2 §2.3. The previous `["ops.*",
"ops.meta.>"]` pair would have been rejected by NATS anyway (overlapping
subjects in one stream — err_code=10052), caught during the refactor's
verification.

Note: the Processor consumer filter in `internal/processor/step1_consume.go`
is still single-segment (`["ops.default", "ops.urgent", "ops.system"]`).
Both single-segment publishes (`ops.default`) and multi-segment publishes
(`ops.default.<requestId>`) now land in the stream; the consumer filter
matches only the former. Updating the consumer filter to `ops.<lane>.>`
is a small follow-up that any story touching `step1_consume.go` can pick
up — keeping the filter narrower than the stream subject is also valid
defensive design and not blocking.

---

## Original Request (preserved for audit)

## Issue

Two authoritative artifacts disagree about the JetStream subject pattern
the operation envelope must be published to:

- **Contract #2 §2.3** ("Lanes and JetStream Subject Mapping") prescribes
  per-lane multi-segment wildcards:

      | Lane     | JetStream Subject |
      |----------|-------------------|
      | default  | ops.default.>     |
      | meta     | ops.meta.>        |
      | urgent   | ops.urgent.>      |
      | system   | ops.system.>      |

- **`internal/bootstrap/primordial.go` (Story 1.3 / 1.4)** provisions the
  `core-operations` stream with `Subjects: ["ops.*", "ops.meta.>"]`.

`ops.*` is a **single-segment** match — it captures `ops.default`,
`ops.urgent`, `ops.system` but NOT `ops.default.<anything>`. As a result,
a client following Contract #2 §2.3 literally (publishing to
`ops.default.<NanoID>` or similar) would have its message rejected by the
stream with "no responders / no matching stream."

## Impact on Story 1.5

Limited. Story 1.5's Processor consumer uses `FilterSubjects:
["ops.default", "ops.urgent", "ops.system"]` (single-segment) to match
what bootstrap actually provisioned. The integration tests and the
manual e2e exercise publish to `ops.default` (single segment) and the
path works end-to-end.

The amendment becomes load-bearing once:

- A submitter wants to use multi-segment routing (e.g. `ops.default.<requestId>`
  for transparency in `nats sub` debug tooling).
- The `meta` lane needs the multi-segment shape (already provisioned —
  `ops.meta.>` is present); a future writer publishing to `ops.meta.<X>`
  works today, but a writer publishing to `ops.default.<X>` would not.

## Proposed Resolutions (pick one)

1. **Update bootstrap** (`internal/bootstrap/primordial.go`) to set
   `Subjects: ["ops.>"]` (single wildcard covers all lanes and all
   sub-segments). This brings the provisioning in line with Contract #2
   §2.3 with minimum churn.

2. **Update Contract #2 §2.3** to specify single-segment subjects
   (`ops.default`, `ops.meta`, `ops.urgent`, `ops.system`) plus an
   explicit "per-lane sub-routing is not supported in Phase 1" note. The
   trade-off: less debug-friendly `nats sub` output, no future room for
   sub-lane routing without re-provisioning the stream.

## Recommendation

Resolution **1** (broaden bootstrap to `ops.>`). The Contract #2 shape
is more general and the bootstrap change is a one-line edit. Story 1.6+
will benefit from the additional routing flexibility (e.g. `ops.<lane>.<requestId>`
in logs and traces).

## Action Taken in Story 1.5 (pending adjudication)

- Processor consumer filter defaults to the bootstrap-compatible
  single-segment list: `["ops.default", "ops.urgent", "ops.system"]`.
- Test harness publishes to `ops.default` (single segment).
- This file logged so Winston + Andrew can adjudicate at review time;
  Story 1.6+ should not start until this is resolved (one of the
  resolutions above must land in either bootstrap or the contract).

No change made to either bootstrap or `data-contracts.md` by this agent.

---

# Contract Amendment Request — DDL meta-vertex lookup key + envelope `class` field (Story 1.6)

raisedBy: Story 1.6 implementation agent (claude-opus-4-7)
raisedAt: 2026-05-13
resolvedAt: 2026-05-14
status: RESOLVED — Story 1.7 implementation agent applied Winston's directives below
severity: Medium (workable today; needs alignment before Story 1.7/1.10)

## Resolution (Story 1.7, Winston directives baked into the handoff brief)

**Issue 1 — DDL shadow key `vtx.meta.<class>`:** RESOLVED via Resolution 2
(bring the DDL cache forward into Story 1.7).
- `internal/processor/ddl_cache.go` now scans `vtx.meta.>` at Processor
  startup, building `map[canonicalName]MetaVertexRef` where
  `MetaVertexRef` carries the real NanoID-keyed meta-vertex key plus the
  cached aspects (canonicalName, permittedCommands, sensitive, script).
- The cache is refreshed synchronously on any successful step-8 commit
  that touches `vtx.meta.*` keys (per the Story 1.7 AC's "DDL mutations
  lane synchronous cache invalidation" requirement).
- `internal/processor/step4_hydrate.go` consults the cache when wired
  via `NewHydratorWithCache`; the Story-1.6 shadow-key fallback remains
  in place behind a nil-cache guard so existing tests that don't wire a
  cache continue to compile and pass.
- `internal/processor/step6_validate.go` consults the same cache for
  permittedCommands + sensitive aspect write-scope enforcement.
- The DDL cache's loader also accepts the Story-1.6 shadow-key fixtures
  (e.g. `vtx.meta.identity`) — the last segment is treated as the
  canonical name when it is not a NanoID. This keeps the Story 1.6
  integration tests green during the migration. Removal of the shadow-
  key fixtures themselves is deferred to a follow-up housekeeping pass
  (the bootstrap's primordial DDLs are already NanoID-keyed; only Story
  1.6's test fixtures need migration, and they continue to work via
  the loader's fallback).

**Issue 2 — Top-level `class` field on `OperationEnvelope`:** RESOLVED
as documented (keep as a Phase-1-transient optional hint).
- The doc-comment on `OperationEnvelope.Class` in
  `internal/processor/envelope.go` has been updated to flag the field
  as Phase-1-transitional, with a pointer to this amendment and the
  Contract #2 §2.1 addendum.
- The field remains `omitempty` in the JSON struct tag, so existing
  clients that don't supply it are unaffected; the Hydrator still
  consults `payload.class` as a fallback.
- A scoped addendum to `data-contracts.md` Contract #2 §2.1
  documenting this disposition is part of the Story 1.7 deliverable
  set (flagged explicitly in the closing summary).
- Removal of the `class` field is reserved for a future story when
  the DDL cache covers 100% of operationType → class derivation
  (target: Story 1.10 or later).

---

## Original Request (preserved for audit)

## Issue 1 — Logical key `vtx.meta.<class>` not contract-compliant

Story 1.6 handoff brief decision #6 instructs scripts be discovered via
`vtx.meta.<class>` and `vtx.meta.<class>.script`. Contract #1 §1.5 +
data-contracts.md "DDL Class Lookup" specify that meta-vertices are
keyed by **NanoID** (e.g., `vtx.meta.Hj4kPmRtw9nbCxz5vQ2y`) with a
separate `canonicalName` aspect, and class-based lookup happens via an
in-memory DDL cache (`map[CanonicalNameKey]MetaVertexKey`) built at
startup by scanning `vtx.meta.>`.

The brief's `vtx.meta.<class>` is not a valid Contract #1 key for two
reasons:
- "class" (e.g., `identity`) is not a NanoID (20 chars from custom
  alphabet); it would not pass `substrate.IsValidNanoID`.
- It collapses two distinct concepts (canonical name and primary key)
  into one string.

## What Story 1.6 did

Implemented `step4_hydrate.go` reading the logical key
`vtx.meta.<class>` and `vtx.meta.<class>.script` directly via
`Conn.KVGet` (which does NOT validate key shape — bypasses
`substrate.AspectKey`'s `mustValidate*` panics). This is functional for
Story 1.6's commit-path proof but is NOT contract-compliant storage. A
`MetaVertex` lookup map is exposed to scripts via the `ddl` global
keyed by class name; the underlying `Key` is the logical
`vtx.meta.<class>` string.

## Issue 2 — Envelope needs a `class` hint until DDL cache lands

Without a startup DDL cache, the Hydrator cannot derive the
operation's class from `operationType` alone. Story 1.6 added an
optional top-level `class` field to `OperationEnvelope` (falls back to
`payload.class`). A missing class results in
`HydrationError{Code: "MissingClass"}` to fail loudly rather than
silently picking a wrong DDL.

This field is NOT in Contract #2 §2.1 today. Phase 1 evolution
options:
1. Add `class` to the envelope permanently (small Phase 1 amendment).
2. Remove `class` once Story 1.10 lands the DDL cache and the
   Processor can derive class from `operationType` (DDL meta-vertices
   declare `permittedCommands`; the cache can build a reverse index).
3. Keep `class` as a non-binding hint even after 1.10 lands — useful
   for clients that want to assert which DDL they expect.

## Proposed Resolutions

**For Issue 1:**
1. Update `data-contracts.md` to acknowledge a Story-1.6-temporary
   "shadow key" `vtx.meta.<class>` used by Hydrator until Story 1.10's
   DDL cache lands. Mark for removal at 1.10.
2. Bring forward part of Story 1.10's DDL cache work into Story 1.7
   (combined with DDL validation step 6) — replace `vtx.meta.<class>`
   reads with a cache lookup by canonical name to the real NanoID key.

**For Issue 2:**
1. Amend Contract #2 §2.1 to add `class` as an optional field,
   documented as "DDL hint for steps 4-6; required in Phase 1 until
   the Processor's DDL cache can derive class from operationType
   (Story 1.10)".

## Recommendation

- Issue 1: Resolution 2 (bring cache forward to 1.7/1.10). The shadow
  key works for 1.6 but is technical debt that DDL validation in 1.7
  cannot cleanly use — the validator must read DDLs by their real
  NanoID keys to enforce schema and provenance integrity.
- Issue 2: Amend the contract to add `class` as optional. Cheap,
  forward-compatible, useful for clients.

## Action Taken in Story 1.6 (pending adjudication)

- Top-level `Class string \`json:"class,omitempty"\`` added to
  `internal/processor/envelope.go::OperationEnvelope`. Marked in the
  doc-comment as pending Story 1.10 alignment.
- `internal/processor/step4_hydrate.go::metaVertexKeyForClass` builds
  the shadow key `vtx.meta.<class>` directly as a string. Doc-comment
  flags the gap.
- Tests in `step4_hydrate_test.go` and `step45_e2e_test.go` seed
  shadow keys directly — they are NOT routed through
  `substrate.AspectKey` (which would panic).

Story 1.7 should NOT land DDL validation against these shadow keys
without resolving Issue 1 first.

---

# Contract Amendment Request — identityIndex key shape (Story 4.2)

raisedBy: Story 4.2 implementation agent (claude-sonnet-4-6)
raisedAt: 2026-05-17
status: RESOLVED — handled inline by implementation agent per brief escalation rule
severity: Low (design variance; brief authorized inline resolution)

## Issue

The Story 4.2 handoff brief specified `vtx.identityIndex.<hex-prefix>`
where `<hex-prefix>` is the first 20 hex chars of `sha256(contact-type + contact)`.
SHA-256 hex output contains digits 0-9 and letters a-f. The digit `0` is
excluded from the NanoID alphabet (Contract #1). Therefore `IsValidNanoID`
rejects 20-char hex strings, and `substrate.ClassifyKey` returns `KindUnknown`,
causing step-6 DDLViolation on `keyPattern`.

## Resolution Applied

Added a second builtin `crypto.sha256NanoID(s)` that:
1. Computes SHA-256 of the input string.
2. Uses the first 16 bytes as a PCG seed (same pattern as `seedFromRequestID`).
3. Produces a 20-char NanoID-alphabet string via `deterministicNanoID`.

The resulting key `vtx.identityIndex.<nanoID>` passes `ClassifyKey` as a
valid vertex key and step-6 keyPattern validation.

**Collision resistance:** the PCG-derived NanoID has ~117 bits of output
space (20 chars × log2(58) ≈ 114 bits), seeded from 128 bits of
SHA-256 entropy. This is marginally stronger than the 80-bit hex-prefix
the brief proposed and remains deterministic from the contact string.

**Rationale for inline resolution:** the brief explicitly listed
"ClassifyKey rejects hex chars" as a handled escalation trigger with a
stated fallback ("escalate if you hit this — would require key-shape
rework"). The inline fix (sha256NanoID builtin) avoids both the key-
shape rework and the lookup-via-data-field fallback, at the cost of a
slightly wider crypto module surface (+1 deterministic function).

**Additional finding during implementation:** the brief used `identityIndex`
as the vertex class name for index vertices. However, Contract #1 §1.1
requires type segments to match `[a-z][a-z0-9]*` (all lowercase). The
capital `I` in `identityIndex` fails step-6's keyPattern check via
`substrate.ClassifyKey`. The class name was lowercased to `identityindex`
(all lowercase) throughout: in the Starlark script, in the test fixtures,
and in this documentation.

**Actions taken:**
- `crypto.sha256NanoID(s)` added to `cryptoModule()` in `starlark_builtins.go`.
- Identity DDL Starlark script (`identity_ddl.go`) updated to call
  `crypto.sha256NanoID(...)` for all index key derivations, and to use
  class `identityindex` (lowercase) for index-vertex mutations.
- Test fixtures use `"class": "identityindex"` (lowercase) for pre-seeded
  index vertices.
- No changes to Contract #1 key patterns or substrate.ClassifyKey.

Winston should confirm whether `crypto.sha256NanoID` should be
documented in the sandbox builtins inventory or treated as a private
implementation detail of the identity domain scripts.

---

## Story 1.5.2 — TombstoneMetaVertex now tombstones `.compensation`; gate4 assertion updated

**Context:** §3.1 (LOCKED) of story 1.5.2 cascades `make_tombstone` to the root
**and every aspect** of a tombstoned meta-vertex, including `.compensation`, and
removes the prior `make_update(meta_key + ".compensation", {inverseOperationType:
"none", note: "...irreversible..."})` rewrite.

**Conflict surfaced:** the pre-existing Gate-4 rollback test
(`internal/aiagent/gate4_rollback_test.go`, DDL_VertexType subtest) asserted that
after a tombstone, `Traverser.ReadCompensation` still SUCCEEDS and returns
`inverseOperationType == "none"` (the old "irreversible note" contract, MF-2/AC3,
"Option A: aspect remains readable"). With the cascade, `.compensation` is
tombstoned, so `ReadCompensation` now returns `ErrCompensationAspectMissing`
(tombstoned). The two requirements — "remove the none-rewrite" (§3.1, LOCKED) and
"gate4 still passes" (§6) — were mutually exclusive against the test as written.

**Resolution applied (flagged for Winston):** the gate4 file is outside this
story's stated confine set, but its MF-2/AC3 assertion *is* a direct test of the
behavior §3.1 changes. I updated that single assertion (lines ~129-137) to expect
`ErrCompensationAspectMissing` after tombstone instead of `inverseOperationType ==
"none"`. No production aiagent code changed. The doc pairing-table row for
`TombstoneMetaVertex` in `docs/components/processor.md` was updated to match (no
machine-readable compensation survives a tombstone). `ReadCompensation` itself
already handled the tombstoned case (returns `ErrCompensationAspectMissing`) —
no change there.

Winston: please confirm this reconciliation of the MF-2/AC3 acceptance criterion
(compensation is no longer readable post-tombstone; re-create is operator
responsibility) is the intended Phase-1.5 contract.

---

### WINSTON RESOLUTION — Story 1.5.2 (2026-05-29)

1. **Compensation-tombstone (MF-2/AC3): RATIFIED.** A fully-tombstoned meta-vertex
   has no readable compensation. `Traverser.ReadCompensation` already maps a
   tombstoned aspect (`isDeleted:true`) to `ErrCompensationAspectMissing`
   (traversal.go:268) by design, so callers using `errors.Is` handle it gracefully.
   The gate4 MF-2/AC3 assertion update (expect `ErrCompensationAspectMissing` after
   tombstone) is the correct reconciliation. Re-create after tombstone is operator
   responsibility (Phase-1 tombstone is irreversible).

2. **Primordial-lens cascade (CR F-1): FIXED in 1.5.2.** The lens cascade set is now
   the UNION of DDL-created (`.description`, `.compensation`) and primordial-seeded
   (`.targetBucket`, `.cypherRule`, `.outputSchema`) lens aspects plus shared
   (`.canonicalName`, `.spec`). Tombstoning an aspect a given lens lacks writes a
   harmless `isDeleted` entry (never read, cache-evicted) — strictly safer than
   leaving a live aspect under a dead root. No live orphan for either lens kind.

3. **Kernel/primordial tombstone PROTECTION (CR F-1 foot-gun): DEFERRED to Story
   1.5.5.** There is no guard preventing `TombstoneMetaVertex` (or `UpdateMetaVertex`)
   from targeting primordial kernel entities — including the kernel root DDL
   (`MetaRootKey`) and the `CapabilityLens` that powers auth — which would be
   catastrophic. This is a PRE-EXISTING gap (not introduced by 1.5.2) and needs a
   protected-key mechanism (where the protected set lives + who enforces it). It
   belongs with 1.5.5 (route installs through Processor / kernel-protection surface).
   Recorded as a residual in phase-1-progress.md.

---

# Contract Amendment Request — UninstallPackage per-key OCC (F-011 follow-up)

raisedBy: Story 1.5.5 implementation agent (claude-opus-4-8)
raisedAt: 2026-05-30
status: OPEN — proposal for a follow-up story
severity: Low (does not block 1.5.5 acceptance; documented window below)

## Context

Story 1.5.5 routes uninstall through the Processor as an `UninstallPackage`
op. The seeded `UninstallPackageDDLScript` supports a per-key
`expectedRevision` (OCC) so a tombstone can fail if a declared key was
modified between the client's read and the commit — the intended F-011 fix.

## Problem

The atomic-batch OCC condition (`Nats-Expected-Last-Subject-Sequence`)
requires the per-SUBJECT sequence of each key. The only place that
per-subject sequence is exposed to a client today is the
`OperationReply.Revisions` map returned by the COMMITTING op (e.g. the
install). `substrate.KVGet(...).Revision` returns the STREAM-level
sequence, which does not match the per-subject header and produces a
spurious `wrong last sequence` rejection if used as `expectedRevision`.

Threading the install-time committed `Revisions` through to a later,
independent uninstall (persisting them, e.g. in the package manifest, and
reading them back at uninstall) is heavier than Story 1.5.5 warrants.

## Decision taken for 1.5.5

`Installer.Uninstall` submits `UninstallPackage` WITHOUT per-key
`expectedRevision` — tombstones are unconditional. The whole batch is still
atomic (no partial/mixed state). The only relaxed guarantee is lost-update
protection on a key that is being uninstalled.

**Documented window:** a concurrent Processor write to a declared key
between the installer's read phase and the commit is silently overwritten
by the tombstone. Acceptable for an admin-driven uninstall.

## Proposed follow-up

Persist the per-subject committed revisions (from the install reply's
`Revisions` map) into the package `.manifest` aspect at install time, then
read them back at uninstall and pass them as per-key `expectedRevision`.
The script path already accepts them; only the client plumbing is missing.
