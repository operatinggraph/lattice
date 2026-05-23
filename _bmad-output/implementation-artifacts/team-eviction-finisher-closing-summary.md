# Team Field Eviction Finisher — Closing Summary

**Story context:** Story 2.4b (team-eviction-finisher)
**Date:** 2026-05-23
**HEAD at start:** 2563e10 (clean tree)

---

## Files Modified

### Operational code
| File | Change |
|------|--------|
| `internal/refractor/lens/schema.go` | Deleted `Team string \`yaml:"team"\`` from `Rule` struct |
| `internal/refractor/lens/corekv_source.go` | Removed `Team: "lattice"` from Lens constructor |
| `internal/refractor/lens/bootstrap.go` | Removed `Team: "lattice"` from `BootstrapLens()` |
| `internal/refractor/health/reporter.go` | Stripped `team` from `Entry` struct, `Reporter` struct, `New()`, all log pairs, all `entry.Team=` assignments |
| `internal/refractor/health/lag_poller.go` | Stripped `Team` from `LagMetric` struct, `team` from `LagPoller` struct, `team` param from `NewLagPoller()` |
| `internal/refractor/pipeline/pipeline.go` | Stripped `team` from `Pipeline` struct, `New()` signature, all ~25 log pairs, `Team: p.team` in `RetryEntry` construction |
| `internal/refractor/failure/dlq.go` | Removed `team` parameter from `Publish()` signature (was unused since 2.4a) |
| `internal/refractor/failure/retry.go` | Removed `Team string` from `RetryEntry` struct; updated `escalateToDLQ` call to `Publish()` |
| `internal/refractor/control/service.go` | Removed `Team string \`json:"team,omitempty"\`` from `ControlRequest` |
| `internal/refractor/fixture/schema.go` | Removed `Team string` from `FixtureRule` struct; removed `team is required` validation |
| `cmd/refractor/main.go` | Updated `health.New(...)`, `pipeline.New(...)`, `health.NewLagPoller(...)` calls to drop team arg |

### Test files
| File | Change |
|------|--------|
| `internal/refractor/health/reporter_test.go` | Dropped team arg from all `health.New(...)` calls; removed `entry.Team` assertions |
| `internal/refractor/health/lag_poller_test.go` | Dropped team arg from `health.New(...)` and `health.NewLagPoller(...)` calls; removed `m.Team` assertion |
| `internal/refractor/pipeline/pipeline_test.go` | Dropped team arg from all `pipeline.New(...)` and `health.NewLagPoller(...)` calls; removed `const team` variable |
| `internal/refractor/control/service_test.go` | Dropped team arg from `health.New(...)` calls; removed `resp.Team` assertion; removed `Team: req.Team` from `ControlRequest` literal; removed `Team: "test-team"` from `validateTestLens` |
| `internal/refractor/failure/retry_test.go` | Removed `Team: "team-a"` from all `RetryEntry` struct literals |
| `internal/refractor/failure/dlq_test.go` | Dropped team arg from `failure.Publish(...)` call |
| `internal/refractor/fixture/schema_test.go` | Removed `fix.Rule.Team` assertion; removed `team: test-team` from all inline YAML strings |
| `internal/refractor/lens/schema_test.go` | Removed `r.Team` assertion from `TestParse_ValidRule`; updated `TestParse_NoTeam_Accepted` comment; removed `team:` lines from all YAML fixtures in tests |
| `internal/refractor/lens/update_test.go` | Removed `team: hot-team` from all YAML fixtures in tests |
| `internal/refractor/refractor_e2e_test.go` | Dropped team arg from `pipeline.New(...)` call |
| `internal/refractor/refractor_capability_e2e_test.go` | Dropped team arg from `pipeline.New(...)` call |
| `internal/refractor/refractor_capability_multi_e2e_test.go` | Dropped team args from two `pipeline.New(...)` calls |

---

## Constructor Signatures Changed

| Function | Before | After |
|----------|--------|-------|
| `health.New` | `(kv, ruleID, team string) *Reporter` | `(kv, ruleID string) *Reporter` |
| `health.NewLagPoller` | `(nc, consumer, reporter, ruleID, team string) *LagPoller` | `(nc, consumer, reporter, ruleID string) *LagPoller` |
| `pipeline.New` | `(ruleID, team, adapterName string, ...) *Pipeline` | `(ruleID, adapterName string, ...) *Pipeline` |
| `failure.Publish` | `(ctx, js, team, ruleID string, msg) error` | `(ctx, js, ruleID string, msg) error` |

---

## JSON-Shape Changes

### `health.Entry` (written to health KV)
- **Before:** `{"ruleId":"...","team":"...","status":"...","pauseReason":...,...}`
- **After:** `{"ruleId":"...","status":"...","pauseReason":...,...}`
- Field `"team"` removed from every health KV write path.

### `health.LagMetric` (published to `lattice.refractor.metrics.<lensId>`)
- **Before:** `{"ruleId":"...","team":"...","consumerLag":...,"timestamp":"..."}`
- **After:** `{"ruleId":"...","consumerLag":...,"timestamp":"..."}`
- Field `"team"` removed.

### `control.ControlRequest` (NATS control endpoint request body)
- **Before:** `{"op":"...","ruleId":"...","team":"...","truncate":...}`
- **After:** `{"op":"...","ruleId":"...","truncate":...}`
- Field `"team"` removed.

### `failure.RetryEntry` (internal struct, not serialized)
- `Team string` field removed.

---

## Test Updates Required

All test updates were made in this story. Summary:
- ~30 `health.New(kv, id, "team-xxx")` → `health.New(kv, id)` call sites
- ~25 `pipeline.New(id, "team-xxx", adapter, ...)` → `pipeline.New(id, adapter, ...)` call sites
- ~5 `health.NewLagPoller(..., id, "team-xxx")` → `health.NewLagPoller(..., id)` call sites
- 1 `failure.Publish(..., "team-xxx", ruleID, msg)` → `failure.Publish(..., ruleID, msg)` call site
- All `entry.Team`, `resp.Team`, `m.Team` assertion lines removed
- All `team: <value>` YAML metadata lines removed from inline YAML test fixtures
- `Team:` struct literal fields removed from RetryEntry, lens.Rule, and ControlRequest test usages

---

## Gate Results

| Gate | Result |
|------|--------|
| `go build ./...` | PASS |
| `make vet` | PASS |
| `golangci-lint run ./...` | PASS (0 issues) |
| `go test ./internal/refractor/... ./cmd/refractor/... -p 1 -count=1` | PASS (17/17 packages) |
| Docker-dependent gates (Postgres integration) | Flagged for Winston/CI — not run locally |

---

## Closing Grep Results

### Grep 1: Adjacency/scan legacy references
```
grep -rn "AdjacencyReads|LinkScans|ScanPrefixes|WithAdjacencyBucket|AdjacencyForNode|keys_with_prefix" internal/ cmd/ packages/
```
Result: Zero operational hits (only comments in `processor/starlark_runner.go` and absence-check assertions in `packages/*/package_test.go`).

### Grep 2: Team references
```
grep -rn "\bteam\b|\"team\"|\.Team\b|Team:|Team \+|Team string" internal/refractor/ cmd/refractor/
```
Result:
```
internal/refractor/adapter/natskv.go:24:// in which key values are concatenated...["team_id","agreement_id"]...
internal/refractor/adapter/natskv_test.go:94:	keys := map[string]any{"team_id": "team-001", ...}
internal/refractor/adapter/natskv_test.go:100:	entry, err := kv.Get(..., "team-001/abc123")
internal/refractor/lens/schema_test.go:52:	// team field is no longer part of the Rule struct (Story 2.4b).
internal/refractor/lens/schema_test.go:53:	// YAML with or without a team key must parse successfully...
internal/refractor/lens/schema_test.go:118:match: MATCH (a:agreement) RETURN a.team AS team_id, a.id AS agreement_id
internal/refractor/subjects/subjects.go:20:// Team segment removed per Deviation 4...
```
**Zero operational hits.** All remaining matches are:
- Comments documenting the eviction
- `team_id` used as a KV key-component field name in natskv adapter tests (unrelated to the `Team` concept)
- Cypher graph property `a.team` in a Lens match query test fixture (graph data property, not the struct field)
- `subjects.go` historical documentation comment

---

## Deviations

None. All items in scope were completed as specified.

### Notes on scope boundary decisions:
1. `failure.Publish()` `team` parameter was also evicted (it was already unused since 2.4a — the comment said "retained for call-site compatibility"). This is consistent with the eviction intent.
2. `control.ControlRequest.Team` was also evicted (field was declared but never read in the service logic — dead code).
3. `fixture.FixtureRule.Team` and its validation were evicted as the struct mirrors `lens.Rule` and the `team` field no longer exists there.
4. YAML `team:` keys in test fixture strings were removed to satisfy the closing grep. These were silently ignored by the YAML parser anyway (NFR22 unknown-field tolerance).

---

## Token Self-Estimate

~28K tokens consumed.
