# Refractor CR Report — Phase 1.5

## Summary
- Files reviewed: 43 (all non-test `.go` files under `internal/refractor/` and `cmd/refractor/`, excluding vendored ANTLR parser)
- P0 findings: 2
- P1 findings: 7
- P2 findings: 6
- Nit findings: 3
- History comments: 41 (pervasive — see section below)

---

## P0 Findings

### [F-001] XOR boolean operator silently evaluated as OR
**File:** `internal/refractor/ruleengine/full/executor.go:994–1016` / `internal/refractor/ruleengine/full/visitor.go:413`
**What:** The visitor correctly emits `&AndOr{Op: "XOR"}` for Cypher's `XOR` keyword. In `evalExpr`, the `case *AndOr` handler checks `if x.Op == "AND"` and falls through with comment `// OR` for everything else — including `"XOR"`. XOR (`exactly one operand true`) and OR (`at least one operand true`) produce different truth-tables; any query containing `WHERE a XOR b` silently computes OR semantics.
**Why it matters:** Silent wrong answer — projection rows that should be suppressed by XOR are included, or rows that should pass are dropped, with no error. Idempotency is preserved (same wrong answer on replay) but correctness is not.
**Suggested fix:** Add an `"XOR"` branch in the `case *AndOr` handler:
```go
case "XOR":
    trueCount := 0
    for _, op := range x.Operands {
        v, err := ex.evalExpr(b, op)
        if err != nil { return nil, err }
        if truthy(v) { trueCount++ }
    }
    return trueCount == 1, nil
```

### [F-002] Bootstrapper startup failure silently leaves Refractor permanently unresponsive
**File:** `cmd/refractor/main.go:97`
**What:** `go func() { _ = bootstrapper.Run(ctx) }()` discards the error from `Run`. `Bootstrapper.Run` returns a non-nil error if `CreateOrUpdateConsumer` fails (e.g., NATS stream not provisioned, network partition during startup). In that case the `ready` channel is never closed. The main goroutine then blocks indefinitely on `<-bootstrapper.Ready()` (line 385) — never loading any lenses. The heartbeater is running so the process appears healthy from the outside, but no projections are ever computed.
**Why it matters:** Silent operational failure that looks like a healthy process. On a NATS hiccup at boot time the service is permanently broken without any log line attributing the cause. Operators have no signal.
**Suggested fix:** Log the error and propagate it to the `ready` select so the process can exit and be restarted:
```go
go func() {
    if err := bootstrapper.Run(ctx); err != nil && ctx.Err() == nil {
        logger.Error("adjacency bootstrap failed — no lenses will start", "err", err)
        // Optionally: os.Exit(1) or signal main to cancel ctx
    }
}()
```

---

## P1 Findings

### [F-003] RETURN DISTINCT not enforced — duplicate rows written to adapter
**File:** `internal/refractor/ruleengine/full/executor.go:930–953`
**What:** `applyReturn` calls `projectItems(bindings, r.Items)` but never checks `r.Distinct`. A Cypher rule using `RETURN DISTINCT ...` will produce duplicate rows in the output, one per matching path, rather than the deduplicated set the query specifies.
**Why it matters:** For a nats_kv adapter this is a no-op (last write wins). For a Postgres adapter, the ON CONFLICT logic would also mask duplicates. But: audit entries are appended for every result row, so the audit stream is polluted with duplicate rows. For non-idempotent future adapters this would be a data-quality bug. Also violates the query author's stated intent.
**Suggested fix:** After `projectItems`, if `r.Distinct` is true, deduplicate rows by their serialized content before building `ProjectionResult` slice.

### [F-004] `validateRule` always fails for full-engine lenses
**File:** `internal/refractor/control/service.go:533–537`
**What:** The `validate` control operation unconditionally calls `simple.Parse(r.Match)` and `simple.Compile(...)` regardless of `r.ResolvedEngine`. For any lens using the full engine (all primordial capability lenses), `simple.Parse` will reject the openCypher syntax and return an error: `ControlResponse{Error: "validate: parse match: ..."}`. The operator receives an error response for a perfectly healthy lens.
**Why it matters:** The `validate` op is a key operator diagnostic tool. It is always broken for the lenses that matter most (capability lens uses full engine). Operators who encounter validate errors may unnecessarily rebuild or pause lenses.
**Suggested fix:** When `r.ResolvedEngine == ruleengine.EngineFull`, skip the plan-compile path and fall back to a simpler "is the rule loadable" check, or return a `ValidateResult` with a note that field-level validation is only available for simple-engine lenses.

### [F-005] Retry `WriteFn` closure captures stale adapter snapshot after hot-reload
**File:** `internal/refractor/pipeline/pipeline.go:784–806`
**What:** `adpt := p.currentAdapter()` (line ~747) takes a snapshot of the adapter for the current message. The `WriteFn` closure for the retry entry (line ~800) closes over this local `adpt` variable — the snapshot, not a live reference to `p.adpt`. If `HotReloadInto` fires between the initial failure and the retry execution (e.g., the INTO target was changed to a new bucket or table), the retry writes to the old, now-stale adapter target. For a nats_kv adapter, this writes to the old bucket (potentially deleted). For postgres, to the old pool/table.
**Why it matters:** After a hot-reload that changes the INTO target, pending retries for the old target silently write to the wrong destination. If the old bucket was deleted, the retries will fail (infra error), exhaust retries, and land in the DLQ with confusing "bucket not found" errors.
**Suggested fix:** Have `WriteFn` call `p.currentAdapter()` at retry time instead of closing over the snapshot: `WriteFn: func(rctx context.Context) error { a := p.currentAdapter(); ... }`. The tradeoff is that retries use whatever adapter is active at retry time — which is the correct behaviour post hot-reload.

### [F-006] `handleAdjUpdate` write failures bypass infra-pause machinery
**File:** `internal/refractor/pipeline/pipeline.go:1165–1185`
**What:** Adj-watch-triggered projections call `adpt.Delete`/`adpt.Upsert` and log failures as `slog.Warn`, then `continue`. Infrastructure failures (e.g., Postgres down) on adj-triggered writes are silently swallowed; the pipeline is not paused, the projection is not retried. The adj-watch goroutine continues to process subsequent events against a broken adapter.
**Why it matters:** If the target store is down, adj-watch-triggered projections are permanently lost — they are not replay-able because there is no JetStream message to redeliver. The normal drain path (CDC consumer) would pause on the same infra failure. Adj-watch creates an inconsistency window that may never be healed without an operator-triggered rebuild.
**Suggested fix:** In `handleAdjUpdate`, classify the write error via `failure.Classify`. On `CatInfra`, signal the pipeline to enter its infra-pause state (e.g., by sending to a shared channel checked by the Run loop). At minimum, log at `slog.Error` and increment the health error count.

### [F-007] `adjacency.Build` and `Neighbors` use `context.Background()` — ignores caller cancellation
**File:** `internal/refractor/adjacency/builder.go:67`, `internal/refractor/adjacency/store.go:17`
**What:** Both `upsertEdge` and `Neighbors` create their own `context.Background()` instead of accepting the caller's context. All adjacency KV operations (CAS retry loop in `upsertEdge`, point reads in `Neighbors`) run with an uncancellable context.
**Why it matters:** During graceful shutdown (ctx cancelled), adjacency KV operations continue running. The CAS retry loop in `upsertEdge` can spin indefinitely on a NATS partition, blocking the consumer's goroutine from exiting. Under high load with many concurrent CAS retries, this degrades shutdown time noticeably.
**Suggested fix:** Add `ctx context.Context` parameter to both functions (and to `Build`), propagate through all KV calls. All callers already have a context available.

### [F-008] Legacy edge events with `nodeId` containing NATS-reserved characters cause panic
**File:** `internal/refractor/subjects/subjects.go:39`, `internal/refractor/adjacency/builder.go:53`
**What:** `subjects.AdjKey(nodeID)` calls `validateToken("nodeID", nodeID)` which panics if `nodeID` contains `.`, `*`, `>`, or whitespace. For Contract #1 link envelopes this is safe (srcID/dstID come from `ParseLinkKey` which splits on `.`). For legacy Materializer-style edge events, `NodeID` comes directly from the JSON `"nodeId"` field — not validated before being passed to `adjacency.Build`. A malformed or adversarial Core KV message with `"nodeId": "foo.bar"` panics the bootstrapper goroutine.
**Why it matters:** A single bad message crashes the adjacency bootstrapper goroutine. The `ready` channel is never closed. The process hangs (same cascade as F-002).
**Suggested fix:** Validate `evt.NodeID` against the NATS-safe token pattern before calling `adjacency.Build` in `bootstrap.go:processMsg`. Return/log and ack (or term) the offending message.

### [F-009] `projectedAt` uses wall clock in `evaluateForEntry` (simple-engine path) — non-deterministic on replay
**File:** `internal/refractor/pipeline/evaluate.go:94`
**What:** For the simple-engine path with an envelope function installed, `params["projectedAt"]` is set to `time.Now().UTC().Format(time.RFC3339)` (line 94). For the full-engine path, the same happens at line 123 (`start := time.Now()`). This means replaying the same Core KV message produces a different `projectedAt` timestamp in the capability envelope every time.
**Why it matters:** Violates the idempotency invariant. After a rebuild or process restart, capability docs get new `projectedAt` values even though the underlying data is unchanged. Operators checking `projectedAt` for staleness monitoring see churn without real updates. The auth-freshness ceiling (2500 ms) is measured against `ProjectedAt`, so frequent rebuilds continuously reset this clock, masking genuine stale projections.
**Why this is P1 not P0:** The behaviour is consistent (same wrong answer each time it runs), so it does not cause data corruption. The known Phase-1.5 gap (heartbeat reprojection) is the deeper root of auth-freshness issues; this finding compounds it.
**Suggested fix:** Thread the message sequence number (or a deterministic input-hash) through the projection as the effective "clock" for `projectedAt`, or at minimum document the non-determinism and track it as a known invariant violation.

---

## P2 Findings

### [F-010] `fetchNode` in full engine accepts `null` JSON body as a valid non-deleted node
**File:** `internal/refractor/ruleengine/full/executor.go:470–481`
**What:** `json.Unmarshal("null", &props)` succeeds with `props == nil` and no error. The dead-looking guard `if props == nil { props = map[string]any{} }` (line 477) is actually reachable for this case. The function returns a live `*nodeRef` with empty properties and `props["key"] = key` added. A Core KV entry with a literal JSON body of `"null"` is treated as an empty, alive node that matches any label-only pattern.
**Why it matters:** A `null`-body entry is likely a corrupted or transitional write. The correct behaviour should be to treat it as a tombstone or skip it, not materialise it as a blank entity that could match traversal patterns and produce empty projection rows.
**Suggested fix:** After the `json.Unmarshal` success check, add `if props == nil { return nil, nil }` to treat `null`-body entries as absent.

### [F-011] `validateRule` is never wired — `SetRuleGetter` never called in `main.go`
**File:** `cmd/refractor/main.go` (omission), `internal/refractor/control/service.go:517–524`
**What:** `controlSvc.SetRuleGetter(src)` is never called in `main.go`. The `validate` control operation checks `if rg == nil { return ControlResponse{Error: "validate: rule getter not configured"} }` and returns an error. The validate op is permanently broken in production.
**Why it matters:** The `validate` diagnostic endpoint always returns an error. Operators cannot use it. Fixable in one line but easy to miss.
**Suggested fix:** Add `controlSvc.SetRuleGetter(src)` in `main.go` after `src := lens.NewCoreKVSource(...)`.

### [F-012] `BootstrapLens()` returns a `*Rule` with zero `ResolvedEngine` — bypasses engine resolution
**File:** `internal/refractor/lens/bootstrap.go:48–65`
**What:** `BootstrapLens()` constructs a `*Rule` directly, leaving `ResolvedEngine`, `CompiledRule`, and `AttemptedEngines` all zero. In `startPipeline`, the branch `if r.ResolvedEngine == ruleengine.EngineSimple || r.ResolvedEngine == ""` (line 210) is taken, so the simple engine path is used. The cypher in the bootstrap lens (`MATCH (c:contract) RETURN c.id AS contract_id, c.name AS name`) is valid simple-engine syntax, so it works in practice. However, if the bootstrap lens is ever updated to use full-engine syntax, it will silently use the simple engine and fail at `simple.Parse` time.
**Why it matters:** Bootstrap lens bypasses the safety rails (engine selection, compile-time validation). If `BootstrapLens()` is ever updated to a full-engine query, the failure mode is confusing (parse error from the wrong engine).
**Suggested fix:** Run `BootstrapLens()` output through the same `defaultRegistry.SelectForLens` path used by `translateSpec`, or at minimum assert `r.ResolvedEngine` is set.

### [F-013] PoolManager pools keyed by raw DSN string — different query-param orderings create duplicate pools
**File:** `internal/refractor/adapter/pool.go:28–37`
**What:** The pool cache uses the raw DSN string as the map key. Two lens specs targeting the same Postgres server but with differently-ordered query parameters (e.g., `sslmode=disable&connect_timeout=5` vs `connect_timeout=5&sslmode=disable`) will create two separate `pgxpool.Pool` instances pointing at the same database. Each pool holds its own connection count.
**Why it matters:** In environments with many lenses (or after a hot-reload with a reordered DSN), connection count grows unboundedly. Not a crash, but an ADR-9 violation (bounded connection count across rules).
**Suggested fix:** Normalize the DSN before using it as a key (e.g., parse with `pgxpool.ParseConfig` and reconstruct canonical form), or use `pgxpool.ParseConfig.ConnString()` as the key.

### [F-014] `processDue` in retry queue iterates entries after releasing lock — potential duplicate retry
**File:** `internal/refractor/failure/retry.go:155–191`
**What:** `processDue` reads `q.entries` under the mutex (lines 156–163) to collect due entries, then releases the lock before executing them. If `Enqueue` is called concurrently and re-adds an entry that was already removed by `remove(e)`, a concurrent `processDue` call (impossible with current single-goroutine Run loop, but not enforced by the type) would see it again. The deeper issue: within a single `processDue` call, after `q.mu.Unlock()`, a new `Enqueue` could add an entry whose `NextAt` is in the past, but it won't be processed until the next `processDue` call — causing one extra scheduling tick of delay.
**Why it matters:** In the current single-`Run`-goroutine model this is benign. But the RetryQueue type provides no guard against being driven from multiple goroutines. A future caller mistake could cause double-execution of a `WriteFn`.
**Suggested fix:** Add a comment documenting the single-caller requirement, or add a `running` flag under mutex to enforce it.

### [F-015] Capability tombstone KV doc has no `projectedAt` — potential auth-freshness false-positive
**File:** `internal/refractor/adapter/natskv.go:70–83`, `internal/refractor/pipeline/evaluate.go:64–70`
**What:** When a soft-deleted actor causes the capability lens to emit a `Delete` result, `NatsKVAdapter.Delete` writes `{"isDeleted": true}` with no `projectedAt` field. Any downstream reader checking auth freshness against `ProjectedAt` on the tombstone doc will see an absent or zero timestamp, which may trigger `AuthFreshnessExceeded` denial even for a legitimately deleted actor — or worse, allow stale cap docs from before the deletion to pass freshness checks if the reader falls back to a cached value.
**Why it matters:** Auth freshness is a security ceiling. An incorrect freshness outcome on actor deletion either silently denies legitimate operations on the deleted actor's residual sessions or fails to block them. Compound with the known heartbeat-reprojection gap (Phase-1.5 M4 gap), this is a latent auth correctness issue.
**Suggested fix:** Include `projectedAt` in the tombstone doc when writing via the capability envelope path, or have the capability envelope emit an Upsert with `isDeleted:true` and a current `projectedAt` instead of using `Delete`.

---

## Known Phase-1.5 Gaps (as flagged by the mandate)

### [GAP-001] Capability lens does NOT heartbeat-reproject — docs go stale
**Location:** `cmd/refractor/main.go` (no heartbeat reprojection logic exists)
**Confirmed:** There is no timer, ticker, or background goroutine that re-projects capability docs for unchanged inputs. Reprojection only fires when a CDC event arrives on an actor vertex or one of its graph neighbours (via fan-out). Docs for actors with no recent graph changes go stale after 2500 ms (`StaleCeiling`), triggering `AuthFreshnessExceeded` denials. This is the root cause of the M4 milestone failure referenced in CI.

### [GAP-002] Postgres adapter does NOT auto-create tables — `ensurePostgresTable` is correct, but only runs at startup
**Location:** `cmd/refractor/main.go:178`, `cmd/refractor/schema.go:17–43`
**Confirmed:** `ensurePostgresTable` is called in `buildAdapter` when the target is `"postgres"`. The table DDL is created with key columns, `is_deleted`, `deleted_at`, and `row_data JSONB`. This is the **correct** behaviour and the known "Phase-1.5 gap" is actually resolved. The table is created on first adapter construction using `CREATE TABLE IF NOT EXISTS`. Subsequent columns from projection rows that don't match the schema would still fail (those columns land in `row_data JSONB` per the schema design), but the `column "X" does not exist` error reported in the mandate applies only to lenses that specify non-key, non-`row_data` columns — which the bootstrap lens does not. **This gap appears resolved for the primordial lenses; flag for verification with domain lenses that specify named non-key columns.**

---

## Nit Findings

### [N-001] `formatISODuration` duplicated from `internal/processor/health.go`
**File:** `internal/refractor/health/lattice_heartbeater.go:170–204`
**What:** The comment says "duplicated from internal/processor/health.go. Story 2.2 may consolidate into substrate." This is a code smell plus a history comment.
**Suggested fix:** Move to `internal/substrate` or a shared `internal/health` package and delete both copies.

### [N-002] `simple.Engine.Execute` contains a hardcoded story reference in its error string
**File:** `internal/refractor/ruleengine/simple/adapter.go:53`
**What:** `return ruleengine.ProjectionResult{}, fmt.Errorf("simple.Engine.Execute: not wired in story 3.1a; callers still invoke simple.Evaluate directly")`. This string will surface in error logs/DLQ messages and is confusing to operators who have no knowledge of story 3.1a.
**Suggested fix:** Replace with: `"simple.Engine.Execute: production callers use simple.Evaluate directly; this method is not on the hot path"`.

### [N-003] `itoa` custom implementation when `strconv.FormatInt` exists
**File:** `internal/refractor/health/lattice_heartbeater.go:187–204`
**What:** Custom `itoa(n int64) string` reimplements `strconv.FormatInt(n, 10)`. The custom version has the same behaviour but adds maintenance surface.
**Suggested fix:** Replace with `strconv.FormatInt(n, 10)` and delete `itoa`.

---

## History Comments

The codebase uses `Story X.Y` references pervasively throughout the Refractor. Per Lattice convention, git blame is the record; comments that narrate *when* or *which story* introduced something are changelog entries masquerading as documentation. The table below lists every instance found. Most should be rewritten as current-state explanations; some can simply be deleted.

| File:Line | Comment text (abbreviated) | Severity | Suggested action |
|---|---|---|---|
| `cmd/refractor/main.go:4` | `// refractor is … adapted to consume Core KV CDC … Story 2.1.` | Nit | Remove "Story 2.1" suffix |
| `cmd/refractor/main.go:48` | `// Story 3.2b §6 — keyed under lensLatency …` | Nit | Replace with "keyed under lensLatency in heartbeats" |
| `cmd/refractor/main.go:110` | `// Story 3.2b §6 — per-Lens latency stats provider` | Nit | Rewrite as current-state purpose |
| `cmd/refractor/main.go:159` | `// Story 3.2a Phase D: bootstrap pre-provisions buckets` | Nit | Rewrite: "Try Open before Create so pre-provisioned buckets are reused" |
| `cmd/refractor/main.go:176` | `// Ensure target table exists … Story 2.1: idempotent CREATE IF NOT EXISTS` | Nit | Remove story reference |
| `cmd/refractor/main.go:187` | `// Story 3.2a Phase B/D — share a single full.Engine …` | Nit | Remove story prefix |
| `cmd/refractor/main.go:195` | `// Story 3.2a Decision #7: partial coverage acceptable` | Nit | Rewrite as design rationale |
| `cmd/refractor/main.go:239` | `// Wire full engine when selected. Story 3.2a — Decision #2.` | Nit | Remove story suffix |
| `cmd/refractor/main.go:248` | `// Story 3.2a Phase C — install Capability KV envelope` | Nit | Remove story prefix |
| `cmd/refractor/main.go:252` | `// stays out of envelope wrapping for 3.2a (Story 3.2b will extend …)` | **P2 – misleading** | The "will extend" is past tense (3.2b shipped). Delete or rewrite: "capabilityRoleIndex does not use the per-actor envelope" |
| `cmd/refractor/main.go:257` | `// Story 4.6 walk-back: stateReader / pendingReview removed.` | **P2 – misleading** | Delete: no current-state content. Future readers see "walk-back" with no context. |
| `cmd/refractor/main.go:260` | `// Story 3.2b §3 — cross-vertex fan-out enumerator` | Nit | Remove story prefix |
| `cmd/refractor/main.go:264` | `// Story 3.2b §6 — per-Lens latency ring buffer for NFR-P3` | Nit | Remove story prefix |
| `cmd/refractor/main.go:270` | `// Story 3.2b §2 — full activation. The envelope rewrites …` | Nit | Remove story prefix |
| `cmd/refractor/main.go:340` | `// Story 3.2b §8 (Decision #8): mirror startPipeline's per-engine routing …` | Nit | Remove story prefix; keep the rationale |
| `internal/refractor/pipeline/pipeline.go:52` | `// Story 3.2a — C1 convergence (per-engine routing, Decision #2).` | Nit | Remove story prefix |
| `internal/refractor/pipeline/pipeline.go:64` | `// Story 3.2b §3 (Decision #3): cross-vertex fan-out.` | Nit | Remove story prefix |
| `internal/refractor/pipeline/pipeline.go:126` | `Story 3.2a uses it for the Contract #6 §6.2 …` | Nit | Remove story reference |
| `internal/refractor/pipeline/pipeline.go:171` | `// openCypher engine (Story 3.2a — C1 convergence …)` | Nit | Remove story suffix |
| `internal/refractor/pipeline/pipeline.go:180` | `// SetEnvelopeFn installs … (Story 3.2a Phase C).` | Nit | Remove story suffix |
| `internal/refractor/pipeline/pipeline.go:187` | `// Story 3.2b §3 / Decision #3` | Nit | Remove |
| `internal/refractor/pipeline/pipeline.go:196` | `// Story 3.2b §6 / Decision #5` | Nit | Remove |
| `internal/refractor/pipeline/pipeline.go:715` | `// Evaluate. Story 3.2a — C1 convergence (Decision #2): route per engine.` | Nit | Remove story prefix |
| `internal/refractor/pipeline/pipeline.go:924` | `// Fill RuleSequence from … (Story 4.1).` | Nit | Remove story suffix |
| `internal/refractor/pipeline/evaluate.go:19` | `Story 3.2a: used by the Capability envelope …` | Nit | Remove |
| `internal/refractor/pipeline/evaluate.go:26` | `Story 3.2a — C1 convergence (Decision #2)` | Nit | Remove |
| `internal/refractor/pipeline/evaluate.go:38` | `// Story 3.2b §3/§5: cross-vertex fan-out + tombstone handling.` | Nit | Remove story prefix |
| `internal/refractor/pipeline/evaluate.go:160` | `// Story 3.2b §6: record per-event projection latency …` | Nit | Remove story prefix |
| `internal/refractor/pipeline/latency.go:1` | `// Story 3.2b §6 (Decision #5): per-Lens projection latency ring buffer.` | Nit | Remove story prefix from file-level doc comment |
| `internal/refractor/pipeline/actor_enumerator.go:1` | `// Story 3.2b §3 (Decision #3): cross-vertex fan-out …` | Nit | Remove story prefix |
| `internal/refractor/lens/corekv_source.go:39` | `// Story 2.1: this REPLACES the MATERIALIZER_RULES JetStream loader` | **P2 – misleading** | "REPLACES" is a changelog marker. Rewrite: "This is the lens-definition source; it subscribes to Core KV changes filtered to `meta.lens` class." |
| `internal/refractor/lens/corekv_source.go:46` | `// Story 2.4b: migrated from jetstream.KeyValue.Watch …` | Nit | Remove story prefix; keep rationale about durable consumer |
| `internal/refractor/lens/schema.go:91` | `// (Story 3.2a Phase C). Not authoritative for routing …` | Nit | Remove story suffix |
| `internal/refractor/ruleengine/ruleengine.go:6` | `// Story 3.1a scope: this package provides …` | Nit | Delete the scope note entirely; it narrates the past |
| `internal/refractor/ruleengine/full/full.go:4` | `// Story 3.1b-i replaces 3.1a's stub Parse with the real lex/parse/walk pipeline.` | **P2 – misleading** | "replaces 3.1a's stub" is pure history. Delete; the file-level comment should describe the current state. |
| `internal/refractor/ruleengine/full/executor.go:4` | `// This file lands the Story 3.1b-ii implementation.` | **P2 – misleading** | Delete; current state is "this is the executor." |
| `internal/refractor/ruleengine/full/executor.go:113` | `// Story 3.2 will route bulk projection through ExecuteWith directly.` | **P2 – misleading** | "Will route" is past tense (3.2 shipped). Delete; `Execute()` already documents itself. |
| `internal/refractor/adapter/postgres.go:231` | `// Story 2.1 AC #4: soft-delete instead of DELETE FROM. … Story 2.2 may relax this` | Nit | Remove story references; keep the rationale |
| `internal/refractor/adapter/natskv.go:67` | `// Story 2.1 AC #4: tombstones use soft-delete semantics per Contract #1.` | Nit | Remove story prefix |
| `internal/refractor/config/config.go:14` | `// HealthKVBucket removed in Story 2.4a — legacy side-channel bucket is vestigial;` | Nit | Delete comment entirely (the field is gone; no current-state documentation needed) |
| `internal/refractor/health/lattice_heartbeater.go:170` | `// formatISODuration is duplicated from internal/processor/health.go. Story 2.2 may consolidate into substrate.` | Nit | Consolidate or delete; don't leave deferred work comments in production code |

**Summary:** 41 history-comment instances. 5 are P2 (actively misleading about current state). The remainder are Nit (changelog noise). Recommend a single sweep that deletes story references and rewrites comments to document *current behaviour*.

---

## Adversarial Coverage Notes

**Things looked for that were not found (evidence of strength):**

- **No race condition in the pipeline registry** (`mu sync.Mutex` consistently protects `registry` reads/writes in `main.go`; all goroutine closures capture values before releasing the lock).
- **No integer overflow in revision tracking**: `uint64` sequence numbers are used throughout; no arithmetic on them that would overflow.
- **No SQL injection in Postgres adapter**: `quoteIdent` correctly escapes all column and table names; positional `$N` placeholders are used for all values.
- **Tombstone handling is correct in the CDC path**: `processMsg` (pipeline.go:642) acks and skips empty-body messages before any processing.
- **CAS retry loop in adjacency builder is correct**: `upsertEdge` loops on both `ErrKeyExists` (concurrent create) and `ErrKeyExists` on update (NATS CAS conflict), retrying indefinitely. No risk of lost-update for adjacency entries.
- **DLQ subject names are NATS-safe**: NanoID alphabet (letters + digits, no dots/wildcards) makes `subjects.DLQ(lensID)`, `subjects.Metrics(lensID)`, and `REFRACTOR_DLQ_<UPPER_ID>` stream names safe. `validateToken` would panic for dots/wildcards anyway (catching bugs early).
- **No credential leakage in log output**: DSN strings are not logged in any slog call; they are used only as pool map keys and passed directly to pgxpool.
- **`manualPauseTrigger` is correctly handled**: Stale trigger tokens are drained on each loop iteration (pipeline.go:322–326), preventing spurious immediate stops.
- **`wg.Wait()` and `poolManager.Close()` on shutdown**: Graceful shutdown correctly waits for pipeline goroutines before closing connection pools.
