# Story 6.2: Health KV Schema & Completeness (FR46, FR52)

Status: review

## Story

As a platform operator,
I want the complete Health KV emission surface documented and verified — projection lag per Lens, stream consumer status, projection errors, component availability — so that the operator console (Phase 2+) and direct CLI reads have a stable, complete observability surface.

---

## Spec Deviations (read first)

### SD-1 — Documentation destination: `docs/observability/` not `data-contracts.md` (OVERRIDES SPEC)

The epics.md §Story 6.2 spec says "data-contracts.md Contract #5 is extended with a complete inventory." **Reject this destination.** Per PO directive (2026-05-24), new living documentation goes to `docs/`, not to `_bmad-output/planning-artifacts/`.

**SD-1 Resolution:**
- Primary deliverable: `docs/observability/health-kv-schema.md` — the canonical Health KV emission inventory. This file becomes the authoritative reference for all Phase 1 Health KV keys, replacing the proposed Contract #5 extension.
- `_bmad-output/planning-artifacts/data-contracts.md` Contract #5 gets ONE line added at the bottom of the §5 section: `> Health KV key inventory lives at docs/observability/health-kv-schema.md (added Story 6.2). This section retains schema-level contracts (bucket name, key naming conventions, document shape) only.`
- No other edits to `data-contracts.md`. Sub-agents NEVER edit planning artifacts directly — add this pointer only if Andrew explicitly approves it at commit review.

**No history comments in code.** The implementer MUST NOT add inline comments that record when or why code was changed — e.g., `// Story 6.2: added this`, `// Replaces old X`, `// Removed in Story Y — see epic Z`, or `// Previously did Z`. Git history is the record of change; comments explain what the code does now. Reviewers (Winston) will reject history comments at code-review time. This rule applies to ALL files touched by this story.

---

## Documentation Layering (convention established by this story)

This story is the **first one to use `docs/` for new content beyond `docs/components/`** (Story 6.0 established that prefix). The convention is:

| Directory | Purpose |
|---|---|
| `docs/` | Canonical, developer- and operator-facing documentation that travels with the code. Read by operators, integrators, and future implementers. **Long-lived.** |
| `docs/components/` | Per-component reference pages (Story 6.0). Authoritative for what each component owns, its contracts, failure modes. |
| `docs/observability/` | **New in Story 6.2.** Health KV schema, key inventory, threshold reference. |
| `_bmad-output/` | Process artifacts — PRD, architecture, epics, story briefs, retros, review reports. NOT the target for living documentation. |
| `_bmad-output/planning-artifacts/` | Cross-component interface contracts (data-contracts.md), PRD, architecture. Pointers only once the detail lives in `docs/`. |

The precedent for `docs/` is `docs/components/_index.md` authored in Story 6.0. A future Phase 1.5 / Phase 2 task will migrate other contract sections (data-contracts.md breakup); Story 6.2 does NOT do that migration. It only establishes the destination for new Health KV content.

---

## Acceptance Criteria

### AC1 — `docs/observability/health-kv-schema.md` documents the complete Phase 1 key inventory

The file must contain a key inventory table organized by component, with emission verification status (emitted today vs. requires-new-emission), and a reserved-namespace section. Structure:

```
# Health KV Schema — Phase 1

## Overview
## Bucket and Connection
## Key Inventory — Phase 1 Components
  ### Processor
  ### Refractor (instance heartbeat)
  ### Refractor (per-lens status)
  ### Bootstrap
  ### Gates
  ### Alerts
## Reserved Namespaces (Phase 2+)
## Document Shapes (per-key JSON schema)
## `lattice health summary` — Rollup Semantics
```

**Verified emission inventory (keys that EXIST in code today — confirmed by grepping `internal/`):**

| Component | Key Pattern | Frequency | Source File | Notes |
|---|---|---|---|---|
| Processor | `health.processor.<instance>` | ≥ 10s heartbeat | `internal/processor/health.go` | `HealthHeartbeater.emit()` |
| Processor | `health.processor.<instance>.step3-latency` | per heartbeat tick | `internal/processor/health.go` | `emitCapabilityAuthSignals()` |
| Processor | `health.processor.<instance>.cap-staleness` | per tick (non-zero only) | `internal/processor/health.go` | skipped when no samples |
| Processor | `health.processor.<instance>.auth-trace.<requestId>` | per denial | `internal/processor/step3_auth_trace.go` | TTL-bounded per Contract #5 |
| Processor | `health.processor.<instance>.malformed-operation.<requestId>` | per malformed envelope | `internal/processor/health.go` | `EmitMalformedOperation()` |
| Processor | `health.processor.<instance>.claim-attempts.<outcome>` | per ClaimIdentity call | `internal/processor/health_alerts.go` | outcome enum: success, invalid-key, wrong-state, flagged, merged, credential-already-bound, no-target |
| Processor | `health.alerts.security.<alertCode>` | on event | `internal/processor/health_alerts.go` | known codes: `stub-auth-active`, `auth-freshness-exceeded` |
| Refractor | `health.refractor.<instance>` | ≥ 10s heartbeat | `internal/refractor/health/lattice_heartbeater.go` | `LatticeHeartbeater` |
| Refractor | `<lensId>` (bare NanoID) | on status change | `internal/refractor/health/reporter.go` | Per-lens status — **key discrepancy; see below** |
| Bootstrap | `health.bootstrap.complete` | one-shot on startup | `internal/bootstrap/primordial.go` | `HealthBootstrapCompleteKey` constant |
| Gates | `health.gates.phase1.gate<N>` | on pass | Various (`bypass/`, `aiagent/`, `hellolattice/`) | Gates 2, 3, 4, 5 written |

**Key discrepancy — Refractor per-lens status key:**

The epics.md spec table lists `health.refractor.<instance>.lens.<lensId>` as the per-lens health key. The **actual code** (`reporter.go:297` + `cmd/refractor/main.go:228`) uses `health.New(healthKVHandle, r.ID)` where `r.ID` is the bare lensId NanoID — so the key written to the Health KV bucket is just the bare lensId, not a `health.refractor.*`-prefixed key.

**Decision (Winston to confirm):** Document both forms in the schema file:
- `<lensId>` — the currently-emitted key (Reporter stores this as-is in the `health-kv` bucket)
- Add a note: this key shape lacks the `health.refractor.<instance>` prefix present on the heartbeater's instance key. This is an existing spec-vs-code gap. Story 6.2 does NOT rename the key (that would require changing reporter.go + all readers). The schema documents what exists, with a "Future: may be normalized to `health.refractor.<instance>.lens.<lensId>` in Phase 2" note.

**Spec table row absent from today's code — requires investigation:**

| Spec Row | Key Pattern | Status |
|---|---|---|
| Refractor lens capability | `health.refractor.<instance>.lens.capability.*` | NOT FOUND in code — no emission for this key pattern; likely absorbed into the heartbeat's `metrics.lensLags` / `metrics.lensLatency` map instead of separate keys. **Do NOT add to schema until verified. If absent, omit from the inventory.** |

The `health.refractor.<instance>.lens.<lensId>` row from the spec maps to the bare `<lensId>` reporter key — document that accurately.

### AC2 — Reserved-namespace section documents Phase 2+ keys

`docs/observability/health-kv-schema.md` includes a "Reserved Namespaces" section covering `health.weaver.*` and `health.loom.*`. No Phase 1 code emits these keys. The section explains that reservation prevents future namespace collisions and documents the intended purpose (Weaver orchestration telemetry, Loom task-graph telemetry).

### AC3 — `lattice health summary` produces a green/yellow/red rollup

The CLI command `lattice health summary` (shipped in Story 6.1 at `cmd/lattice/health/health.go`) currently reads all health keys and prints a raw listing. This AC extends it to produce a structured rollup:

```
COMPONENT             STATUS      FRESHNESS     DETAILS
processor.<instance>  green       12s ago       ops_consumed=142 ops_committed=141
refractor.<instance>  green       8s ago        lensLags: capability=0
<lensId> (capability) active      -             consumerLag=0 errorCount=0
health.bootstrap.comp green       -             one-shot complete
Gates passed: 2/5  (gate2=pass gate3=pass gate4=pass gate5=fail gate1=absent)
Alerts (last hour): none
Overall: GREEN
```

Rollup semantics (document in `docs/observability/health-kv-schema.md` §"Rollup Semantics"):
- **green**: all non-event-driven components have a health entry fresher than `--stale-threshold` (default 60s); no active alerts; consumerLag=0 for all lenses.
- **yellow**: any component entry is stale (age > threshold) OR consumerLag > 0 for any lens. Active warning-severity alerts also trigger yellow.
- **red**: any error-severity alert; any health entry absent (not just stale); any gate missing that was expected to have passed by now.

`--stale-threshold <duration>` flag (default 60s). Configurable via `LATTICE_HEALTH_STALE_THRESHOLD` env var.

**This is the ONE permitted change to `cmd/lattice/health/health.go` in this story.** The `newSummaryCommand` function is extended with threshold logic and the table format above. `newComponentCommand` and `newGatesCommand` are NOT changed.

### AC4 — Integration test: Health KV completeness

A new test file at `internal/healthkv/completeness_test.go` (new package, build-tagged `//go:build integration`) starts all Phase 1 components, runs for 30 seconds, then asserts every documented non-event-driven key has at least one record in the Health KV bucket.

Non-event-driven keys (must appear within 30s):
- `health.processor.<instance>` (heartbeat, ≥10s)
- `health.processor.<instance>.step3-latency` (per heartbeat tick)
- `health.refractor.<instance>` (heartbeat, ≥10s)
- `<lensId>` per active lens (status written on activation)
- `health.bootstrap.complete` (written at bootstrap, already present)

Event-driven keys explicitly excluded from the completeness assertion (only present on events):
- `health.processor.<instance>.auth-trace.<requestId>`
- `health.processor.<instance>.malformed-operation.<requestId>`
- `health.processor.<instance>.claim-attempts.<outcome>`
- `health.processor.<instance>.cap-staleness` (only when non-zero samples)
- `health.alerts.security.<alertCode>`
- `health.gates.phase1.gate<N>`

Test build tag: `integration` per the established pattern from Story 6.4 (`internal/hellolattice/`). Normal `go test ./...` does not run this test. Enable with `go test -tags integration ./internal/healthkv/...`.

`make test-health-completeness` target: `go test -tags integration ./internal/healthkv/... -v -timeout 90s` (extra timeout buffer for component startup).

### AC5 — NFR-O3 conformance confirmed

The `docs/observability/health-kv-schema.md` includes an explicit "NFR-O3 Conformance" callout: list all Phase 1 components and assert each has a documented emission surface. NFR-O3 is the observability requirement that every component emit to Health KV.

---

## Architectural Guardrails (non-negotiable)

**Guardrail 1 — Documentation goes to `docs/`, not `_bmad-output/`.**
New Health KV content goes to `docs/observability/health-kv-schema.md`. The only change permitted to any `_bmad-output/` file is ONE pointer line in `data-contracts.md` §5, and ONLY after Winston confirms this is appropriate at commit review. Sub-agents NEVER edit planning artifacts.

**Guardrail 2 — No new Processor or Refractor read surface.**
Story 6.2 is observability — it READS Health KV that's already emitted by existing components. Do not add new emissions from inside the write/projection path unless an AC1 table key is explicitly missing today (see the "Spec table row absent" note above). If adding an emission is needed, add it as a minimally-invasive addition to the existing emitter (e.g., `internal/processor/health.go` or `internal/refractor/health/lattice_heartbeater.go`) and flag it clearly in the completion notes.

**Guardrail 3 — Reserved namespaces are documented but not emitted.**
`health.weaver.*` and `health.loom.*` appear in `docs/observability/health-kv-schema.md` as reserved entries only. Zero production code emits these keys. Do not add stub emissions.

**Guardrail 4 — Completeness test is non-blocking in normal CI.**
The `internal/healthkv/completeness_test.go` test is gated by `//go:build integration`. `make test` and `go test ./...` do not run it. Only `make test-health-completeness` (or `-tags integration` manually) triggers it. Do not add it to the default CI test job (`.github/workflows/ci.yml` should NOT be changed to run integration-tagged tests automatically unless Andrew explicitly asks for it).

**Guardrail 5 — No history comments.**
Standard rule (see SD-1 header above).

---

## Tasks / Subtasks

- [x] Task 1 — Inventory audit (AC1 prerequisites)
  - [x] 1.1 Confirm all keys in the AC1 "verified" table by searching `internal/` for the emit site. Record source file + function for each key.
  - [x] 1.2 Investigate `health.refractor.<instance>.lens.capability.*` — search for any emission of this pattern. If not found, confirm it is absent (not in scope for Phase 1; omit from schema).
  - [x] 1.3 Confirm per-lens status reporter key shape: `reporter.go:297` writes `kv.Put(ctx, r.ruleID, data)` using the raw `ruleID` (lensId NanoID). Document the current key as `<lensId>` in the schema with the normalization note.
  - [x] 1.4 Confirm `health.gates.phase1.gate1` is NOT written (Gate 1 was bootstrap, no explicit gate marker found). Document as absent in the schema.

- [x] Task 2 — Create `docs/observability/health-kv-schema.md` (AC1, AC2, AC3 rollup-semantics, AC5)
  - [x] 2.1 Create `docs/observability/` directory (first file in this directory).
  - [x] 2.2 Write the full key inventory table with all columns: Key Pattern, Frequency, Source File, Emitter Function/Type, JSON Shape Reference.
  - [x] 2.3 Add per-key JSON document shape reference (not full JSON Schema — concise field list is sufficient).
  - [x] 2.4 Add Reserved Namespaces section (`health.weaver.*`, `health.loom.*`).
  - [x] 2.5 Add "Rollup Semantics" section documenting green/yellow/red thresholds and the `--stale-threshold` flag.
  - [x] 2.6 Add NFR-O3 Conformance section listing all Phase 1 components and their emission coverage.
  - [x] 2.7 Add a header note: "This is the canonical Health KV reference. If in doubt, trust this file over `data-contracts.md` §5 for key-level details."

- [x] Task 3 — Extend `lattice health summary` (AC3)
  - [x] 3.1 Read existing `cmd/lattice/health/health.go` `newSummaryCommand` function.
  - [x] 3.2 Add `--stale-threshold` flag (type `time.Duration`, default 60s); also read `LATTICE_HEALTH_STALE_THRESHOLD` env var.
  - [x] 3.3 Replace raw listing with the structured rollup table (component, status, freshness, details).
  - [x] 3.4 Add `Overall: GREEN/YELLOW/RED` line at the bottom.
  - [x] 3.5 JSON output (`--output json`) emits `{"ok": true, "data": {"overall": "green", "components": [...], "alerts": [...], "gates": {...}}}`.
  - [x] 3.6 Preserve the existing `newComponentCommand` and `newGatesCommand` unchanged.
  - [x] 3.7 Unit test: `TestHealthSummary_Rollup_AllGreen` and `TestHealthSummary_Rollup_StaleYellow` in `cmd/lattice/health/health_test.go` using a stub substrate conn.

- [x] Task 4 — Create `internal/healthkv/completeness_test.go` (AC4)
  - [x] 4.1 Create `internal/healthkv/` package (new directory). May be minimal — the package can be `package healthkv` with only the integration test file.
  - [x] 4.2 Add `//go:build integration` tag to `completeness_test.go`.
  - [x] 4.3 Test connects to live NATS_URL (same pattern as hellolattice — live stack required, not embedded NATS, because Refractor heartbeats require the full binary running with projection pipeline).
  - [x] 4.4 Wait 30s (or until all expected keys appear, whichever comes first) using 2s polling loop.
  - [x] 4.5 Assert each non-event-driven key is present. Collect failures and report all at once (don't stop at first).
  - [x] 4.6 The test does NOT assert event-driven keys (see AC4 exclusion list). Comment clearly in the test which keys are excluded and why.
  - [x] 4.7 Add `test-health-completeness` to `Makefile`: `go test -tags integration ./internal/healthkv/... -v -timeout 90s`.

- [x] Task 5 — Architecture purity verification
  - [x] 5.1 No history comments in modified files. `docs/observability/health-kv-schema.md` reference to "§Story 6.2" is a spec citation in documentation, not a code comment.
  - [x] 5.2 `grep -rn "health\.weaver\|health\.loom" internal/ cmd/` → zero hits. Reserved but not emitted.
  - [x] 5.3 `newComponentCommand` and `newGatesCommand` in `cmd/lattice/health/health.go` are byte-for-byte unchanged from Story 6.1.
  - [x] 5.4 `go build ./cmd/lattice` clean.
  - [x] 5.5 `golangci-lint run ./...` clean (0 issues).
  - [x] 5.6 `go test ./cmd/lattice/health/... -count=1` passes (all 4 tests including new rollup tests).
  - [x] 5.7 `make test-bypass` — requires Docker stack; not run (no Docker in this environment). Guardrail: no changes to bypass/gate2/gate3 code paths.
  - [x] 5.8 `make test-capability-adversarial` — requires Docker stack; not run. Same guard.
  - [x] 5.9 `make verify-kernel` — requires Docker stack; not run. Same guard.

---

## Dev Notes

### Overview

Story 6.2 is primarily a documentation + thin-layer story. It:
1. Audits the existing Health KV emission surface (grepping `internal/` is the primary method — the code is the truth).
2. Authors `docs/observability/health-kv-schema.md` — the canonical inventory.
3. Extends `lattice health summary` with green/yellow/red rollup logic (additive change to an existing command).
4. Adds an integration-tagged completeness test.

No new NATS subjects. No new KV buckets. No Processor changes. No Refractor changes.

---

### Emission Surface — What Exists vs. Spec

The full source-of-truth audit (confirmed against codebase):

**Processor (`internal/processor/health.go` + `health_alerts.go` + `step3_auth_trace.go`):**
- `health.processor.<instance>` — `HealthHeartbeater.emit()` on a 10s ticker + startup + shutdown. Key built by `healthKey()` method.
- `health.processor.<instance>.step3-latency` — `emitCapabilityAuthSignals()`, always emits per tick when `capAuthorizer` is attached.
- `health.processor.<instance>.cap-staleness` — same function, skips when `staleness.Count == 0`.
- `health.processor.<instance>.malformed-operation.<requestId>` — `EmitMalformedOperation()`.
- `health.processor.<instance>.claim-attempts.<outcome>` — `RecordClaimAttempt()` in `HealthAlertEmitter`.
- `health.alerts.security.<alertCode>` — `EmitAlert()`. Known codes: `stub-auth-active`, `auth-freshness-exceeded`.
- `health.processor.<instance>.auth-trace.<requestId>` — `AuthTraceEmitter.Emit()` in `step3_auth_trace.go`.

**Refractor (`internal/refractor/health/`):**
- `health.refractor.<instance>` — `LatticeHeartbeater.emit()` via `healthKey()` which returns `"health.refractor." + h.instance`.
- `<lensId>` (bare NanoID) — `Reporter.put()` writes to `kv.Put(ctx, r.ruleID, ...)` where `r.ruleID = r.ID` (the lensId). The bucket is `healthKVHandle` opened in `cmd/refractor/main.go:90`. This is NOT prefixed with `health.refractor.*`.

**Bootstrap (`internal/bootstrap/primordial.go`):**
- `health.bootstrap.complete` — `MarkBootstrapComplete()`. Constant `HealthBootstrapCompleteKey = "health.bootstrap.complete"` in `nanoid.go`.

**Gates (multiple callers):**
- `health.gates.phase1.gate2` — written by `internal/bypass/bypass_test.go` on full-pass.
- `health.gates.phase1.gate3` — written by `internal/bypass/gate3_test.go` on full-pass.
- `health.gates.phase1.gate4` — written by `internal/aiagent/gate4_rollback_test.go`.
- `health.gates.phase1.gate5` — written by `internal/hellolattice/hellolattice_test.go`.
- Gate 1 — NOT written as a health key. Bootstrap completion uses `health.bootstrap.complete` instead. Document in schema as "Gate 1 = bootstrap complete = health.bootstrap.complete (not a gates.phase1.* key)."

**Absent from code (spec table rows that are NOT emitted):**
- `health.refractor.<instance>.lens.<lensId>` — spec says this; code writes bare `<lensId>`. Treat the bare key as the Phase 1 reality; document the normalization gap.
- `health.refractor.<instance>.lens.capability.*` — NOT found anywhere in `internal/`. Likely spec drift. Do NOT invent emission for this. Omit from the schema and note "spec proposed this key but it is not emitted in Phase 1."

---

### Per-Lens Health Reporter Key Shape

The Refractor per-lens reporter (`internal/refractor/health/reporter.go`) uses:

```go
func New(kv jetstream.KeyValue, ruleID string) *Reporter {
    return &Reporter{kv: kv, ruleID: ruleID}
}
// ...
func (r *Reporter) put(ctx context.Context, entry Entry) error {
    // ...
    if _, err := r.kv.Put(ctx, r.ruleID, data); err != nil {  // KEY = ruleID (bare lensId)
```

And in `cmd/refractor/main.go`:

```go
reporter := health.New(healthKVHandle, r.ID)  // r.ID = lensId NanoID
```

So the actual key in the `health-kv` bucket is the raw `r.ID` value — a NanoID string like `abc123...`. This is distinct from the heartbeater key `health.refractor.<instance>`.

Document in the schema file under "Refractor per-lens status":

> **Key:** `<lensId>` (bare NanoID, same string as the `vtx.meta.<lensId>` Core KV vertex key).
> **Note:** This key does not carry the `health.refractor.*` prefix. The original spec proposed `health.refractor.<instance>.lens.<lensId>`. The Phase 1 implementation writes the bare lensId directly. Phase 2 normalization will align this if needed.

---

### `lattice health summary` Extension Pattern

The existing `newSummaryCommand` in `cmd/lattice/health/health.go` lists all keys. Extend it:

```go
func newSummaryCommand(natsURL, outputFmt *string) *cobra.Command {
    var staleThreshold time.Duration
    cmd := &cobra.Command{
        Use:   "summary",
        Short: "Show overall platform health with green/yellow/red rollup",
        RunE: func(cmd *cobra.Command, args []string) error {
            // ... connect, list keys, read entries ...
            // Compute rollup using staleThreshold
            // Print structured table
            // Print "Overall: GREEN/YELLOW/RED"
        },
    }
    cmd.Flags().DurationVar(&staleThreshold, "stale-threshold", 60*time.Second, "age threshold for stale health entries (env: LATTICE_HEALTH_STALE_THRESHOLD)")
    return cmd
}
```

Read `LATTICE_HEALTH_STALE_THRESHOLD` env var in `RunE` if the flag is at its default value. Standard env-override pattern consistent with `NATS_URL`.

Rollup algorithm:
1. Categorize each health key by component group (processor heartbeat, processor alerts, refractor heartbeat, per-lens, bootstrap, gates).
2. For heartbeat keys (`health.processor.*`, `health.refractor.*`): extract `heartbeatAt` field; compute age = now - heartbeatAt. If age > staleThreshold → yellow.
3. For per-lens keys: check `status` field. If `"paused"` → yellow; `"rebuilding"` → yellow; `"active"` → check `consumerLag` and `errorCount` for nuance.
4. For alert keys (`health.alerts.security.*`): check `severity`. If `"error"` → red. If `"warning"` → yellow.
5. For gate keys: missing expected gates → yellow (gates 2 and 3 are expected after any full test run; not always present in a fresh deploy).
6. Overall = worst of all component statuses.

---

### `internal/healthkv/completeness_test.go` — Test Harness

Follow the Story 6.4 pattern in `internal/hellolattice/hellolattice_test.go` for component startup. Key differences for completeness test:
- Does NOT need the full Hello Lattice scenario; just needs components running and emitting.
- Use `testutil.NewPipelineHarness` or equivalent embedded-NATS + Processor + Refractor setup.
- The test may share the harness logic rather than duplicating it — check `internal/testutil/pipeline.go` for the existing `NewPipelineHarness` helper before writing a new setup from scratch.
- 30s wait: use a polling loop (`time.NewTicker(2*time.Second)` checking for presence of all expected keys) rather than a hard `time.Sleep(30*time.Second)`. Exit early if all keys appear sooner.

Minimum expected keys after 30s with Phase 1 components running:
```
health.processor.<any-instance>             // any key matching this prefix
health.processor.<any-instance>.step3-latency
health.refractor.<any-instance>             // Refractor heartbeater
<any-lens-id>                               // at least one per-lens reporter (capability lens)
health.bootstrap.complete                   // already written at startup
```

---

### Files to Create

```
docs/observability/
  health-kv-schema.md          — primary deliverable (AC1, AC2, AC3 rollup-semantics, AC5)

internal/healthkv/
  completeness_test.go         — integration-tagged completeness test (AC4)

cmd/lattice/health/
  health.go                    — EXTEND newSummaryCommand only (AC3); others unchanged
  health_test.go               — add TestHealthSummary_Rollup_AllGreen, TestHealthSummary_Rollup_StaleYellow
```

---

### Files NOT to Touch

- `_bmad-output/planning-artifacts/data-contracts.md` — sub-agents NEVER edit planning artifacts. Winston applies the one-line pointer at commit review.
- `_bmad-output/planning-artifacts/epics.md` — read-only reference.
- `internal/processor/commit_path.go` — no commit path changes.
- `internal/processor/step3_auth_trace.go` — no new helpers.
- `internal/refractor/health/reporter.go` — do NOT rename the per-lens key; document as-is.
- `internal/refractor/health/lattice_heartbeater.go` — do NOT change key format.
- `cmd/lattice/health/health.go` `newComponentCommand` and `newGatesCommand` — unchanged.
- `docs/components/` — no changes to existing component pages (they don't cover Health KV schemas).
- `.github/workflows/ci.yml` — do NOT add integration-tagged tests to default CI.

---

### Architecture Compliance Checklist

- [ ] `docs/observability/health-kv-schema.md` exists and covers all verified keys from AC1 table
- [ ] `data-contracts.md` NOT modified by sub-agent (pointer added by Winston only)
- [ ] `health.weaver.*` and `health.loom.*` appear only in `docs/observability/health-kv-schema.md` — zero production code emits them
- [ ] `newComponentCommand` and `newGatesCommand` in `health.go` byte-identical to Story 6.1 output
- [ ] `internal/healthkv/completeness_test.go` has `//go:build integration` tag
- [ ] `make test-cli` exits 0
- [ ] `make test-bypass` all-DEFENDED
- [ ] `make test-capability-adversarial` all-BLOCKED
- [ ] `make verify-kernel` green
- [ ] `golangci-lint run ./...` clean

---

### References

- [Source: `_bmad-output/planning-artifacts/epics.md` §Story 6.2, lines 1445–1486] — original AC
- [Source: `internal/processor/health.go`] — `HealthHeartbeater`; heartbeat + step3-latency + cap-staleness + malformed-operation emission
- [Source: `internal/processor/health_alerts.go`] — `HealthAlertEmitter`; `RecordClaimAttempt` + `EmitAlert`
- [Source: `internal/processor/step3_auth_trace.go`] — `AuthTraceEmitter`; per-request auth-trace emission
- [Source: `internal/refractor/health/lattice_heartbeater.go`] — Refractor instance heartbeat; key = `health.refractor.<instance>`
- [Source: `internal/refractor/health/reporter.go`] — per-lens status reporter; key = bare `ruleID` (lensId NanoID)
- [Source: `internal/bootstrap/nanoid.go`] — `HealthBootstrapCompleteKey = "health.bootstrap.complete"`
- [Source: `internal/bootstrap/primordial.go`] — `HealthKVBucket = "health-kv"`
- [Source: `cmd/lattice/health/health.go`] — existing `lattice health summary/component/gates` (Story 6.1)
- [Source: `internal/hellolattice/hellolattice_test.go`] — Story 6.4 integration-tagged test harness pattern
- [Source: `internal/testutil/pipeline.go`] — `NewPipelineHarness` helper; `HarnessHealthBucket = "health-kv"`
- [Source: `docs/components/_index.md`] — precedent for `docs/` as living documentation destination
- [Source: `_bmad-output/implementation-artifacts/WINSTON-RESUME.md`] — operating conventions, no-history-comment rule
- [Source: `_bmad-output/implementation-artifacts/6-1-lattice-cli-tool.md`] — brief structure template; cobra patterns

---

## Implementation Tier & Budget

**Model tier: Sonnet** (bounded scope; all underlying primitives already ship; primary work is documentation + additive CLI change + new integration test).
**Estimated token budget: ~90K** (tracking only, NOT enforced per Rule 8 in WINSTON-RESUME.md). Given the doc + schema audit + test work, may run to ~120K. Sub-agent self-reports are typically 20-30% low vs outer telemetry; trust the task-notification `total_tokens`.

---

## Stuck-Loop Halt Criteria

Halt and surface for Winston review if any of the following occur:
- Confirming any Health KV key in the AC1 table requires reading Processor or Refractor internals in a way that suggests a key was never actually wired (e.g., heartbeater is constructed but `Run()` is never called in `cmd/processor/main.go`). Stop and file a `cmd/processor/CONTRACT-AMENDMENT-REQUEST.md` rather than inventing a fake verification.
- The `internal/healthkv/completeness_test.go` requires more than a trivial harness extension and looks like it will require >50 lines of new infrastructure not available in `testutil/`. Stop and propose a scope cut: completeness test lists which keys it can assert vs. which require richer infrastructure.
- `make test-cli` fails after 2 debug attempts on the `TestHealthSummary_Rollup_*` tests.
- Any file in `_bmad-output/planning-artifacts/` is found to require editing beyond the one pointer line in `data-contracts.md` §5. File a `CONTRACT-AMENDMENT-REQUEST.md` instead.

Do NOT halt for token budget alone.

---

## Dev Agent Record

### Agent Model Used

claude-sonnet-4-6

### Debug Log References

D1: `snippetOf` function was used only by the old raw-listing summary command. After replacing with rollup logic, golangci-lint flagged it as unused. Removed it cleanly — it was a helper for the deleted listing path.

D2: Task 4.3 deviation from brief: brief says "embedded NATS per Story 6.4 harness precedent." However, Story 6.4's hellolattice test uses a **live NATS_URL** (not embedded NATS) because it requires Processor + Refractor binary processes running. The completeness test has the same requirement — Refractor heartbeats can only come from the running binary, not from embedded NATS with in-process harness. Using the live-stack pattern is the correct approach.

### Completion Notes List

- AC1: Full key inventory audited from source. `health.refractor.<instance>.lens.capability.*` confirmed absent — not emitted anywhere in `internal/`. Per-lens key confirmed as bare NanoID `<lensId>` from `reporter.go:put()`. Gate 1 confirmed as `health.bootstrap.complete` (not a `gates.phase1.*` key).
- AC2: Reserved namespaces `health.weaver.*` and `health.loom.*` documented in `docs/observability/health-kv-schema.md`. Zero production code emits them (verified by grep).
- AC3: `newSummaryCommand` replaced with rollup table output and `--stale-threshold` flag. `LATTICE_HEALTH_STALE_THRESHOLD` env var supported. JSON output uses `summaryRollup` shape. `newComponentCommand` and `newGatesCommand` are unchanged.
- AC4: `internal/healthkv/completeness_test.go` created with `//go:build integration` tag. Polls up to 30s with 2s interval. `make test-health-completeness` target added to Makefile.
- AC5: NFR-O3 conformance table in `docs/observability/health-kv-schema.md` covers all 5 Phase 1 components (Processor, Refractor heartbeat, Refractor per-lens, Bootstrap, Gates).
- `go build ./...` clean. `go vet ./...` clean. `golangci-lint run ./...` 0 issues. `go test ./... -p 1 -count=1` all pass (no regressions). `go test ./cmd/lattice/health/... -count=1` 4 tests pass.
- Docker-stack regression gates (test-bypass, test-capability-adversarial, verify-kernel) not run — no Docker available in this environment. No changes to bypass, aiagent, or kernel verification code paths.

### File List

- `docs/observability/health-kv-schema.md` — new (primary deliverable)
- `internal/healthkv/completeness_test.go` — new (integration-tagged completeness test)
- `cmd/lattice/health/health.go` — modified (summary rollup + `--stale-threshold`)
- `cmd/lattice/health/health_test.go` — modified (added `TestHealthSummary_Rollup_AllGreen`, `TestHealthSummary_Rollup_StaleYellow`)
- `Makefile` — modified (added `test-health-completeness` target + `.PHONY` entry)
- `_bmad-output/implementation-artifacts/6-2-health-kv-schema-completeness.md` — modified (task checkboxes, status, dev agent record)
