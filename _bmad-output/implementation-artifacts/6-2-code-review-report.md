# Code Review Report — Story 6.2: Health KV Schema & Completeness

**Diff reviewed:** Story 6.2 uncommitted working-tree changes (vs `HEAD` = Story 6.4)
**Reviewer:** bmad-code-review (Sonnet 4.6)
**Date:** 2026-05-24
**Spec file:** `_bmad-output/implementation-artifacts/6-2-health-kv-schema-completeness.md`

**Files in scope:**
- `docs/observability/health-kv-schema.md` — new, canonical Health KV emission inventory (AC1, AC2, AC5)
- `internal/healthkv/completeness_test.go` — new, `//go:build integration` completeness test (AC4)
- `cmd/lattice/health/health.go` — modified, rollup logic + `--stale-threshold` flag (AC3)
- `cmd/lattice/health/health_test.go` — modified, two new rollup unit tests (AC3)
- `Makefile` — modified, `test-health-completeness` target (AC4)

**Diff stats:** ~676 lines total (547 modified/existing, 129 new untracked files)

**Note:** Parallel subagent review is not available in this session. All three layers — Blind Hunter, Edge Case Hunter, Acceptance Auditor — were run sequentially inline with full project access.

---

## Summary

**1 MUST FIX item found.** The implementation is structurally sound: the schema doc accurately reflects code reality (including the bare-`<lensId>` key discrepancy and absent `capability.*` keys), all five ACs are substantially met, and all five guardrails pass cleanly. The single blocker is a carried-forward exit-code violation: the new `newSummaryCommand` uses `_ = output.PrintJSONError(...); return nil` for the `ConnectionError` path, which exits 0 in JSON mode — the same pattern that MF-1 in the Story 6.1 review identified but which the Option A fix (changing `PrintJSONError` to return `ErrJSONError`) did not eliminate for the `_ = ...; return nil` sites. Three SHOULD CONSIDER items cover an unsafe type assertion that will panic on malformed lag data, `null` vs `[]` in JSON array fields, and a doc/code label mismatch on the alerts line. Three NITs round out the report.

---

## 🔴 MUST FIX

### MF-1 — `newSummaryCommand` ConnectionError path exits 0 in JSON mode (`_ = PrintJSONError(...); return nil`)

**File:** `cmd/lattice/health/health.go:296-299`
**Review layer:** Blind Hunter + Acceptance Auditor

**What's wrong:**

The new `newSummaryCommand` added in Story 6.2 includes:

```go
if *outputFmt == "json" {
    _ = output.PrintJSONError("ConnectionError", err.Error())
    return nil   // ← exits 0
}
```

`output.PrintJSONError` now returns `ErrJSONError` (non-nil) — the Story 6.1 MF-1 Option A fix. However, the `_ = ...; return nil` pattern discards that return value and explicitly returns `nil` to cobra, so the command exits 0 even though it printed an error envelope to stdout. Only the `ListError` path on line 307 (`return output.PrintJSONError(...)`) is correctly non-zero.

**Consequence:** `lattice health summary --output json` on a connection failure writes `{"ok":false,"error":{"code":"ConnectionError",...}}` to stdout and exits 0. Scripts and pipelines checking exit codes will treat connection failures as success.

**Pre-existing in the unchanged commands:** The same pattern appears in `newComponentCommand` (lines 389-390) and `newGatesCommand` (lines 447-448) — both unchanged from Story 6.1. Those are pre-existing (carry-over from 6.1's unresolved MF-1). The Story 6.2 instance in `newSummaryCommand` is newly introduced code and must be fixed now.

**What to do:** Change lines 296-299 to:

```go
if *outputFmt == "json" {
    return output.PrintJSONError("ConnectionError", err.Error())
}
```

Remove the `_ = ...` discard and `return nil`. This matches the `ListError` path and all other correctly-fixed call sites. No other change needed — `PrintJSONError` already returns `ErrJSONError`.

**Which AC/Guardrail:** AC3 (the extended `lattice health summary` must produce correct output and exit codes); implicit exit-code contract inherited from AC2 of Story 6.1.

---

## 🟡 SHOULD CONSIDER

### SC-1 — Unsafe type assertion `lag.(float64)` — panics on malformed lensLag entry

**File:** `cmd/lattice/health/health.go:193`
**Review layer:** Blind Hunter + Edge Case Hunter

**What's wrong:**

```go
for lens, lag := range lags {
    parts = append(parts, fmt.Sprintf("%s=%.0f", lens, lag.(float64)))
}
```

`lag` is of type `any`. The assertion `lag.(float64)` (no comma-ok) panics with `interface conversion: interface {} is <T>, not float64` if any value in the `lensLags` map is not a `float64`. For well-formed data from the live Refractor heartbeater (JSON unmarshalled to `map[string]any`), all numbers are `float64` — so this is safe in normal operation. However, a malformed Health KV entry (corrupted document, debugging injection, future emitter change) will crash the CLI.

**What to do:** Use a comma-ok assertion:

```go
for lens, lag := range lags {
    lagF, _ := lag.(float64)
    parts = append(parts, fmt.Sprintf("%s=%.0f", lens, lagF))
}
```

This degrades gracefully (shows `0` for non-float entries) instead of panicking.

**Which AC/Guardrail:** General robustness; AC3 (`lattice health summary` must not panic on live data).

---

### SC-2 — `summaryRollup.Alerts` and `.Components` serialize as JSON `null`, not `[]`

**File:** `cmd/lattice/health/health.go:133, 267-270`
**Review layer:** Edge Case Hunter + Acceptance Auditor

**What's wrong:**

`alertMsgs` and `rows` are declared as `var` (nil slices) and returned directly in the `summaryRollup` struct:

```go
var rows []componentRow
var alertMsgs []string
// ...
return summaryRollup{
    Overall:    ...,
    Components: rows,       // nil slice → "null" in JSON
    Alerts:     alertMsgs,  // nil slice → "null" in JSON
    Gates:      gates,
}, overall
```

Go's `encoding/json` marshals nil slices as `null`, not `[]`. The brief (AC3) specifies the JSON shape as `{"ok": true, "data": {"overall": "green", "components": [...], "alerts": [...]}}` — consumers expecting arrays will see `null` on empty stacks.

**What to do:** Initialize both slices to empty (non-nil):

```go
rows := make([]componentRow, 0)
alertMsgs := make([]string, 0)
```

**Which AC/Guardrail:** AC3 (JSON output shape); consistent with the `gatesSummary.Gates` map which IS explicitly initialized as `map[string]any{}`.

---

### SC-3 — Schema doc and code disagree on the alerts output label: `"Alerts: none"` vs `"Alerts (last hour): none"`

**File:** `cmd/lattice/health/health.go:350`; `docs/observability/health-kv-schema.md:362`
**Review layer:** Acceptance Auditor

**What's wrong:**

The schema doc's "Table format" section (line 362) shows:
```
Alerts (last hour): none
```

The code prints (line 350):
```go
fmt.Println("Alerts: none")
```

Two sub-issues:
1. The label text differs: `"Alerts (last hour):"` vs `"Alerts:"`.
2. The rollup code does not filter alerts by any time window — it shows ALL alert keys regardless of age. So the `"last hour"` qualifier in the schema doc is inaccurate even if the label were aligned.

**What to do:** Either (a) align the code label to `"Alerts (last hour):"` and add age-based filtering for alert keys (consistent with the documented behavior), or (b) update the schema doc table format example to say `"Alerts:"` (removing the misleading `"last hour"` qualifier). Option (b) is lower-effort for Phase 1. The schema doc is the canonical reference so it should be accurate.

**Which AC/Guardrail:** AC1 (schema doc accuracy); AC3 (output format matches documented format).

---

## 🟢 NITS

### N-1 — `processor-event` and `refractor-event` keys are silently skipped in the rollup — not explicitly documented in the Rollup Semantics section

**File:** `cmd/lattice/health/health.go:94, 98`; `docs/observability/health-kv-schema.md` (Rollup Semantics)
**Review layer:** Blind Hunter

`classifyKey` returns `"processor-event"` and `"refractor-event"` for sub-component keys (e.g., `step3-latency`, `cap-staleness`, `malformed-operation.*`). These groups have no `case` in the `computeSummaryRollup` switch — they're silently ignored. This is correct behavior per spec (event-driven keys excluded from rollup). However, the schema doc's "Component rollup algorithm" section doesn't explicitly state that sub-component keys are excluded. A one-line note would prevent future confusion.

---

### N-2 — Completeness test's `<lensId>` assertion is over-broad: matches any non-`"health."`-prefixed key

**File:** `internal/healthkv/completeness_test.go:172-179`
**Review layer:** Edge Case Hunter

```go
present: func(ks map[string]struct{}) bool {
    for k := range ks {
        if !strings.HasPrefix(k, "health.") {
            return true   // any non-"health." key passes
        }
    }
    return false
},
```

Any key in the `health-kv` bucket that doesn't start with `"health."` will satisfy this check — including test artifacts, future non-Refractor keys, or any stale entry. The check doesn't confirm the key is actually a NanoID-shaped lens ID. This is acceptable for Phase 1 (only Refractor writes bare NanoID keys to this bucket), but is fragile if the bucket's key population expands. A comment noting this assumption would help.

---

### N-3 — `freshnessStr` uses `int64(d.Seconds())` — displays negative freshness as `0s ago` silently, not as a clock-skew indicator

**File:** `cmd/lattice/health/health.go:113-116`
**Review layer:** Edge Case Hunter

```go
func freshnessStr(t time.Time) string {
    d := time.Since(t).Round(time.Second)
    if d < 0 {
        d = 0
    }
    return fmt.Sprintf("%vs ago", int64(d.Seconds()))
}
```

If `heartbeatAt` is in the future (clock skew between Processor/Refractor host and CLI host), `d` is negative, is clamped to 0, and displayed as `"0s ago"`. The rollup then treats age = 0 as fresh (not stale). A future timestamp could mask a diverged clock. The clamping prevents a confusing negative display but silently hides the skew. A NIT: consider logging or noting `"<1s ago (clock skew?)"` for future timestamps.

---

## Architecture / NFR Compliance Sign-off

- **G1 — Doc in `docs/` not `_bmad-output/`:** PASS. Schema doc is at `docs/observability/health-kv-schema.md`. `_bmad-output/planning-artifacts/data-contracts.md` is untouched.
- **G2 — No new Processor/Refractor read surface:** PASS. `git diff HEAD -- internal/processor/ internal/refractor/` is empty. All `KVPut` calls in those packages are pre-existing. No new emissions were added.
- **G3 — Reserved namespaces docs-only:** PASS. `grep -rn "health\.weaver\|health\.loom" internal/ cmd/` returns zero hits. The reserved prefixes appear only in `docs/observability/health-kv-schema.md`.
- **G4 — Completeness test opt-in:** PASS. `//go:build integration` tag is the first line of `internal/healthkv/completeness_test.go`. Default `go test ./...` does not trigger it. `make test-health-completeness` uses `-tags integration`.
- **G5 — No history comments:** PASS. No `// Story 6.2`, `// Was`, `// Replaces`, `// Added by`, `// Previously` in any 6.2-touched file.
- **Schema-vs-code consistency:** PASS (with one pre-existing gap documented). `heartbeatAt` field confirmed in both `internal/processor/health.go` (line 33) and `internal/refractor/health/lattice_heartbeater.go` (line 21). Per-lens reporter fields (`ruleId`, `status`, `consumerLag`, `errorCount`, `pauseReason`, `lastError`, `activeSequence`, `ruleEngine`) confirmed against `reporter.go:28-40`. Bare `<lensId>` key shape confirmed from `reporter.go:put()`. `health.refractor.<instance>.lens.capability.*` confirmed absent from `internal/`. Gate 1 = `health.bootstrap.complete` confirmed. All documented emitter function names verified.
- **`newComponentCommand` and `newGatesCommand` unchanged:** PASS. Byte-for-byte identical to Story 6.1 commit `303160d`.
- **AC1 key inventory completeness:** PASS. All 11 Phase 1 key patterns from the brief's verified inventory are present in the schema doc. The `health.refractor.<instance>.lens.capability.*` absent-key note is documented correctly.
- **AC4 build tag + Makefile target:** PASS. Tag present. `make test-health-completeness` target uses `go test -tags integration ./internal/healthkv/... -v -timeout 90s` — matches brief spec exactly.
- **AC5 NFR-O3 conformance section:** PASS. Section present at `docs/observability/health-kv-schema.md:368-381`, covers all 5 Phase 1 components.
- **`go build ./...` / `go test ./...`:** Per dev agent record — clean (no Docker stack available to verify `make test-bypass`, `make test-capability-adversarial`, `make verify-kernel`; no changes to those code paths).

---

## Summary Table

| ID | Severity | Title | File(s) | AC/Guardrail |
|---|---|---|---|---|
| MF-1 | 🔴 MUST FIX | `newSummaryCommand` ConnectionError path exits 0 in JSON mode (`_ = PrintJSONError; return nil`) | `health/health.go:296-299` | AC3, exit-code contract |
| SC-1 | 🟡 SHOULD CONSIDER | Unsafe `lag.(float64)` type assertion — panics on malformed lensLag entry | `health/health.go:193` | AC3 |
| SC-2 | 🟡 SHOULD CONSIDER | `Alerts` and `Components` serialize as JSON `null` not `[]` when empty | `health/health.go:133, 267-270` | AC3 |
| SC-3 | 🟡 SHOULD CONSIDER | Schema doc and code disagree on alerts label: `"last hour"` qualifier absent from code and semantics | `health/health.go:350`, `health-kv-schema.md:362` | AC1, AC3 |
| N-1 | 🟢 NIT | `processor-event`/`refractor-event` silently skipped in rollup — not documented in Rollup Semantics section | `health/health.go:94, 98`, schema doc | — |
| N-2 | 🟢 NIT | Completeness test `<lensId>` check is over-broad (any non-`health.`-prefixed key passes) | `completeness_test.go:172-179` | — |
| N-3 | 🟢 NIT | Future `heartbeatAt` timestamps silently clamped to `"0s ago"` — masks clock skew | `health/health.go:113-116` | — |

---

Review complete: **1 MUST FIX, 3 SHOULD CONSIDER, 3 NITS**
