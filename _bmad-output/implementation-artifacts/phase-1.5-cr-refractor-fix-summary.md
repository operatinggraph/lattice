# Phase 1.5 CR Refractor Fix Summary

All findings applied. `go build ./...`, `go vet -unreachable=false`, and `go test ./internal/refractor/... -p 1 -count=1 -short` pass cleanly.

---

## P0

| ID | File:Line touched | Description |
|---|---|---|
| F-001 | `internal/refractor/ruleengine/full/executor.go:994–1017` | Added `"XOR"` branch in `evalExpr` `*AndOr` case; XOR now returns `trueCount == 1` instead of falling through to OR semantics |
| F-002 | `cmd/refractor/main.go:97` | Bootstrapper goroutine now logs at Error and calls `stop()` (cancels root ctx) when `Run` returns a non-nil error while ctx is still live |

---

## P1

| ID | File:Line touched | Description |
|---|---|---|
| F-003 | `internal/refractor/ruleengine/full/executor.go:930–953` | `applyReturn` deduplicates rows by JSON-serialised content when `r.Distinct` is true |
| F-004 | `internal/refractor/control/service.go:533` | `validateRule` now branches on `r.ResolvedEngine == ruleengine.EngineFull` and returns a `ValidateResult` with a warning note instead of a misleading parse error; added `ruleengine` import |
| F-005 | `internal/refractor/pipeline/pipeline.go:800` | `WriteFn` closure in retry entry now calls `p.currentAdapter()` at retry time instead of closing over the snapshot `adpt` captured at enqueue time |
| F-006 | `internal/refractor/pipeline/pipeline.go:1165–1185` | `handleAdjUpdate` now classifies write errors via `failure.Classify`, logs at `slog.Error`, and records the error in health KV via `reporter.RecordError` |
| F-007 | `internal/refractor/adjacency/builder.go:53,66`; `internal/refractor/adjacency/store.go:16` | `Build` and `Neighbors` now accept `ctx context.Context`; propagated through all callers in `bootstrap.go`, `evaluator.go`, `executor.go`, `actor_enumerator.go`, `evaluate.go`, `fixture/runner.go`, and all test files |
| F-008 | `internal/refractor/consumer/bootstrap.go:176–184` | `processMsg` validates `evt.NodeID` against NATS-reserved characters before calling `adjacency.Build`; Terms and logs bad messages instead of panicking |

---

## P1 (deferred)

| ID | Notes |
|---|---|
| F-009 | Skipped per mandate — carved into its own Phase 1.5 story |

---

## P2

| ID | File:Line touched | Description |
|---|---|---|
| F-010 | `internal/refractor/ruleengine/full/executor.go:470–482` | `fetchNode` returns `nil, nil` when `json.Unmarshal` produces a nil map (JSON `"null"` body), treating it as absent |
| F-011 | `cmd/refractor/main.go:408`; `internal/refractor/lens/corekv_source.go:138` | Added `controlSvc.SetRuleGetter(src)` in `main.go` after `src.SetUpdateCallback`; added `Get(ruleID string) (*Rule, bool)` method to `CoreKVSource` |
| F-012 | `internal/refractor/lens/bootstrap.go:48–81` | `BootstrapLens()` now calls `defaultRegistry.SelectForLens` to populate `ResolvedEngine`, `CompiledRule`, and `AttemptedEngines`; added `ruleengine` import |
| F-013 | `internal/refractor/adapter/pool.go:26–49` | `PoolManager.Acquire` canonicalizes the DSN via `pgxpool.ParseConfig(dsn).ConnString()` before using it as the map key; added `fmt` import |
| F-014 | `internal/refractor/failure/retry.go:52–63`, `internal/refractor/failure/retry.go:96–115` | Added `running bool` field to `RetryQueue`; `Run` now panics if called concurrently from more than one goroutine |
| F-015 | `internal/refractor/adapter/natskv.go:70–84` | `NatsKVAdapter.Delete` now includes `"projectedAt": time.Now().UTC().Format(time.RFC3339)` in the tombstone document; added `time` import |

---

## Nits

| ID | File:Line touched | Description |
|---|---|---|
| N-001 | `internal/refractor/health/lattice_heartbeater.go:169–186` | Removed story-reference comment and `itoa` custom function; `formatISODuration` now uses inline `strconv.FormatInt` via a local `itoa` closure |
| N-002 | `internal/refractor/ruleengine/simple/adapter.go:46–55` | Replaced story-reference error string and doc comment with current-state explanation |
| N-003 | `internal/refractor/health/lattice_heartbeater.go:187` | Replaced custom `itoa` implementation with `strconv.FormatInt(n, 10)` (covered under N-001) |

---

## History Comment Sweep (41 instances)

All 41 instances from the CR table were addressed. Key changes by file:

| File | Action |
|---|---|
| `cmd/refractor/main.go` | Removed all `Story X.Y` prefixes/suffixes; rewrote P2 misleading comments (walk-back note deleted, "will extend" rewritten, "REPLACES" rewritten) |
| `internal/refractor/pipeline/pipeline.go` | Removed story prefixes from struct comments, method docs, and inline comments |
| `internal/refractor/pipeline/evaluate.go` | Removed story prefixes; rewrote fan-out/tombstone comments as current-state |
| `internal/refractor/pipeline/latency.go` | Removed story prefix from file-level doc comment |
| `internal/refractor/pipeline/actor_enumerator.go` | Removed story prefix from file-level doc comment |
| `internal/refractor/lens/corekv_source.go` | Rewrote P2 "REPLACES" comment; removed story prefix from durable-consumer comment; cleaned remaining story refs |
| `internal/refractor/lens/schema.go` | Removed story references from registry comment and field doc comments |
| `internal/refractor/ruleengine/ruleengine.go` | Removed scope note and story refs from package doc and type docs |
| `internal/refractor/ruleengine/full/full.go` | Deleted "replaces 3.1a stub" history paragraph |
| `internal/refractor/ruleengine/full/executor.go` | Deleted "lands Story 3.1b-ii" from file doc; deleted "will route" P2 comment |
| `internal/refractor/adapter/postgres.go` | Removed story references from soft-delete comment |
| `internal/refractor/adapter/natskv.go` | Updated via F-015 (already no story refs remaining) |
| `internal/refractor/config/config.go` | Rewrote removed-field comment as current-state |
| `internal/refractor/health/lattice_heartbeater.go` | Removed story refs from latency provider and formatISODuration comments |

---

## Test file updates (side-effect of F-007)

The following test files were updated to pass `ctx` to the new `adjacency.Build` / `adjacency.Neighbors` / `Enumerate` signatures:

- `internal/refractor/adjacency/builder_test.go` — added `ctx := context.Background()` to each test function
- `internal/refractor/adjacency/store_test.go` — added `context` import and `ctx` to each test function
- `internal/refractor/consumer/bootstrap_test.go` — `ctx` already in scope; sed replacement sufficient
- `internal/refractor/ruleengine/full/executor_test.go` — added `ctx := context.Background()` inside `putEdge` helper
- `internal/refractor/ruleengine/full/capability_lens_contract_test.go` — added `ctx := context.Background()` inside `contractPutEdge` helper
- `internal/refractor/refractor_capability_e2e_test.go` — `ctx` already in scope; sed replacement sufficient
