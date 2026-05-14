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
status: Open — awaits Winston adjudication
severity: Medium (workable today; needs alignment before Story 1.7/1.10)

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
