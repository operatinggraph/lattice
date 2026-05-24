# Code Review Report — Story 6.4: "Hello Lattice" Reference Implementation

**Staged diff reviewed:** Story 6.4 staged changes (pre-commit)
**Reviewer:** bmad-code-review (Sonnet 4.6)
**Date:** 2026-05-24
**Spec file:** `_bmad-output/implementation-artifacts/6-4-hello-lattice-reference-impl.md`

**Files in scope:**
- `README.md` — root tutorial (new)
- `examples/hello-lattice/book-ddl.yaml` — book DDL reference payload (new)
- `examples/hello-lattice/books-lens.yaml` — books Lens reference payload (new)
- `examples/hello-lattice/ai-agent.go` — standalone AI agent program (new)
- `examples/hello-lattice/Makefile` — demo target (new)
- `examples/hello-lattice/README.md` — examples directory index (new)
- `internal/hellolattice/hellolattice_test.go` — Gate 5 integration test (new, build-tagged `integration`)
- `internal/bootstrap/meta_ddl.go` — SD-1: meta.lens spec accepts JSON string (modified)
- `internal/processor/starlark_runner.go` — SD-1: Starlark `json` module added (modified)
- `internal/aiagent/gate4_rollback_test.go` — lens payload now includes `targetConfig` (modified)
- `internal/bootstrap/self_description_e2e_test.go` — meta.lens test updated for new spec contract (modified)
- `Makefile` — `test-hello-lattice` target added (modified)
- `.github/workflows/ci.yml` — Gate 5 CI step added (modified)
- `_bmad-output/planning-artifacts/gate5-external-tester-report.md` — external tester template (new)

**Note:** Parallel subagent review is not available in this session. All three layers — Blind Hunter, Edge Case Hunter, Acceptance Auditor — were run sequentially inline with full project access.

---

## Summary

**2 MUST FIX items found, 5 SHOULD CONSIDER, 4 NITS.**

The SD-1 fix (`meta_ddl.go` + `starlark_runner.go`) is architecturally clean and the integration test structure is well-organised. The guardrails are broadly respected. However, two correctness bugs require attention before this can be considered done:

1. **MF-1** — The `book` DDL script (all four artifacts: README inline payload, `book-ddl.yaml`, `hellolattice_test.go`, and `examples/hello-lattice/Makefile`) creates book vertices with no top-level `"key"` field in the document, but the Lens `cypherRule` projects `b.key AS book_id`. The simple evaluator resolves `b.key` via `props["key"]` which is absent → `book_id` column is always `NULL`. This silently breaks Milestone 4 (Postgres projection) and Milestone 5 (AI agent Postgres assertion). The fix is one line in the DDL script mutation.

2. **MF-2** — `seedCapDocForAgent` in the integration test bypasses the Refractor projection path and writes a synthetic capability doc to `capability-kv` as a fallback when Refractor doesn't reproject within 500ms. The seeded doc includes `CreateUnclaimedIdentity` which was never granted — but more critically, if this fallback path is taken, Milestone 5 passes even when Refractor projection is completely broken. The test should fail when Refractor doesn't project within the NFR-P3 window, not silently substitute.

---

## 🔴 MUST FIX

### MF-1 — `book` DDL script creates document without `"key"` field — `b.key AS book_id` projection always produces NULL

**Files:** `examples/hello-lattice/book-ddl.yaml:41-48`, `README.md:107` (inline JSON), `internal/hellolattice/hellolattice_test.go:53-69` (`bookDDLScript` constant), `examples/hello-lattice/Makefile:55`
**Review layer:** Blind Hunter + Edge Case Hunter

**What's wrong:**

The book DDL script (identical across all four artifacts) stores:

```python
mutations = [
    {"op": "create", "key": book_key,
     "document": {"class": "book", "isDeleted": False,
                  "title": title,
                  "data": {"title": title}}},
]
```

The document has no top-level `"key"` field. The `books` Lens `cypherRule` is:

```
MATCH (b:book) RETURN b.key AS book_id, b.title AS title
```

The simple evaluator resolves `b.key` via `props[col.Property]` where `col.Property = "key"` (`internal/refractor/ruleengine/simple/evaluator.go:177`). The pipeline feeds the raw unmarshalled document as `Properties` without injecting the CoreKV key (`pipeline.go:688-713`). Since the document has no `"key"` field, `props["key"]` is absent, `val` stays `nil`, and the `book_id` column is `NULL`.

`book_id` is declared as the Postgres table key (`"key":["book_id"]` in `targetConfig`). A `NULL` key column silently breaks every upsert. Milestone 4 will write NULL-keyed rows (or fail the upsert uniqueness constraint); Milestone 5's Postgres poll will find the row only by title, masking the bad key.

This is a data correctness bug that voids the core milestone 4 + 5 assertions.

**What to do:**

Add `"key": book_key` to the document in every copy of the DDL script:

```python
{"op": "create", "key": book_key,
 "document": {"class": "book", "isDeleted": False,
              "key": book_key,          # ← add this
              "title": title,
              "data": {"title": title}}},
```

This change must be applied consistently in **all four** locations:
- `examples/hello-lattice/book-ddl.yaml` (the `script:` YAML block)
- `README.md` (the Milestone 2 inline `--payload` JSON, the escaped `\"key\":\"vtx.book...\"` in the script string)
- `internal/hellolattice/hellolattice_test.go` (the `bookDDLScript` constant at line 53)
- `examples/hello-lattice/Makefile` (the `--payload` JSON string in the `milestone-2` target)

**Which AC/Guardrail:** AC4 (Postgres projection produces correct rows), AC5 (AI agent Postgres assertion).

---

### MF-2 — `seedCapDocForAgent` fallback bypasses Refractor projection — Milestone 5 can pass with a broken Refractor

**File:** `internal/hellolattice/hellolattice_test.go:429-436`, `668-686`
**Review layer:** Edge Case Hunter + Acceptance Auditor

**What's wrong:**

`TestHelloLattice_Milestone5_AITraversal` polls for the agent's capability doc for 500ms (NFR-P3). If the Refractor hasn't projected within 500ms, the test calls `seedCapDocForAgent` which directly writes a synthetic `CapabilityDoc` to `capability-kv` via `KVPut` — bypassing the entire Refractor projection pipeline:

```go
if time.Now().After(capDeadline) {
    t.Logf("WARNING: capability doc for agent not reprojected within 500ms — seeding manually for test continuation")
    seedCapDocForAgent(t, ctx, agentKey, agentID)
    break
}
```

Two problems:

1. **False pass on broken Refractor:** If Refractor is down, slow, or misconfigured, `seedCapDocForAgent` allows the traversal logic in steps 6-12 to succeed. The test logs a warning but does not call `t.Fatal` or `t.Error`. Milestone 5 passes despite Refractor never having projected anything.

2. **Seeded doc is incorrect:** `seedCapDocForAgent` (line 674) includes `CreateUnclaimedIdentity` in `PlatformPermissions`. The test only grants `CreateBook` to the agent. The seeded doc is a superset of actual permissions — it doesn't reflect the real RBAC state.

**What to do:**

Remove the fallback path entirely. The 500ms deadline poll should fail the test if NFR-P3 is violated:

```go
for {
    cap, capErr = tr.ReadCapability(ctx, agentID)
    if capErr == nil {
        for _, p := range cap.PlatformPermissions {
            if p.OperationType == "CreateBook" {
                goto capFound
            }
        }
    }
    if time.Now().After(capDeadline) {
        t.Fatalf("NFR-P3 violated: capability doc for agent %s not reprojected within 500ms; "+
            "check that Refractor is running (make up)", agentKey)
    }
    time.Sleep(10 * time.Millisecond)
}
capFound:
```

Delete `seedCapDocForAgent` and its call site. The test purpose is to validate the live system; a fallback that substitutes for Refractor contradicts AC5.

**Which AC/Guardrail:** AC5 (AI traversal uses real projected capability doc), G5 (no fixed sleeps — the fallback is a disguised circumvention of the bounded-poll NFR).

---

## 🟡 SHOULD CONSIDER

### SC-1 — `TestMain` defers `conn.Close()` before `os.Exit()` — defer is skipped, connection leaks

**File:** `internal/hellolattice/hellolattice_test.go:119-122`
**Review layer:** Blind Hunter

**What's wrong:**

```go
defer conn.Close()   // line 119
code := m.Run()
os.Exit(code)        // line 122 — os.Exit skips all defers
```

`os.Exit` does not run deferred calls. `harnessConn` is never closed; the NATS connection leaks. In practice the OS cleans up on process exit, but this violates the Go testing idiom and suppresseses any close-path error logging.

**What to do:**

```go
code := m.Run()
conn.Close()   // explicit close before os.Exit
os.Exit(code)
```

**Which AC/Guardrail:** General test hygiene.

---

### SC-2 — `examples/hello-lattice/Makefile` milestone-4 uses `@sleep 5` — fixed sleep, G5 violation

**File:** `examples/hello-lattice/Makefile:72`
**Review layer:** Blind Hunter + Acceptance Auditor

**What's wrong:**

The `milestone-4` demo target contains:

```makefile
@echo "--- Waiting for Refractor to project (up to 5 seconds)..."
@sleep 5
@echo "--- Query books table:"
$(LATTICE) query postgres "SELECT * FROM books"
```

This uses a fixed 5-second sleep, directly violating Guardrail 5 (no fixed sleeps). For a demo target this is less critical than in a test, but the brief's rule applies to all files the story touches. Additionally, `make demo` is listed as AC6's "live demo" artifact. If the Refractor is fast (NFR-P3 ≤ 500ms), the 5-second sleep is unnecessary; if the Refractor is slow (CI overload, config problem), the query may still return 0 rows and the demo fails silently.

**What to do:**

Replace the fixed sleep with a bounded poll using `lattice lens lag`:

```makefile
@echo "--- Waiting for Refractor to project (polling lens lag)..."
@for i in 1 2 3 4 5 6 7 8 9 10; do \
    lag=$$(NATS_URL=$(NATS_URL) $(LATTICE) lens lag --output json | jq -r '.lag // 1'); \
    if [ "$$lag" = "0" ]; then break; fi; \
    sleep 1; \
done
```

Or, simpler: `lattice lens lag` already polls. Even an explicit bounded loop is acceptable. The key is surfacing failure rather than hiding it behind a sleep.

**Which AC/Guardrail:** G5 (no fixed sleeps).

---

### SC-3 — `lattice bootstrap inspect --format key` does not exist — used in `examples/hello-lattice/Makefile` and `examples/hello-lattice/README.md`

**Files:** `examples/hello-lattice/Makefile:11,47`, `examples/hello-lattice/README.md:21`
**Review layer:** Acceptance Auditor + Blind Hunter

**What's wrong:**

Three locations direct the user to:

```console
export BOOTSTRAP_ACTOR_KEY=$(lattice bootstrap inspect --format key)
```

`lattice bootstrap inspect` has no `--format` flag (`cmd/lattice/bootstrap/bootstrap.go` has no such flag). This command will fail with "unknown flag: --format" when a user follows the Quick Start in `examples/hello-lattice/README.md` or reads the Makefile help. The `make demo` prerequisite check also references this broken command in its error message.

**What to do:**

Replace with the correct incantation from the root `README.md` tutorial:

```console
# Get bootstrap actor key:
export BOOTSTRAP_ACTOR_KEY=$(lattice graph keys vtx.identity. | head -1)
# or read it from bootstrap inspect output:
lattice bootstrap inspect
```

Update all three locations. If `--format key` is a desirable shorthand, file it as a Story 6.x enhancement to `cmd/lattice/bootstrap/bootstrap.go` and note it in this report rather than blocking Gate 5.

**Which AC/Guardrail:** AC6 (live demo runs without documentation gaps).

---

### SC-4 — `TestHelloLattice_Milestone1_Setup` uses direct KVGet not `exec.Command` — deviates from AC1 spec assertion

**File:** `internal/hellolattice/hellolattice_test.go:127-161`
**Review layer:** Acceptance Auditor

**What's wrong:**

AC1 specifies: _"Integration test assertion (milestone 1): `TestHelloLattice_Milestone1_Setup` — runs `lattice health gates` via `exec.Command`; asserts exit code 0 and at least gates 1-4 listed."_

The implementation does not use `exec.Command`. It reads the Health KV bucket directly via `harnessConn.KVGet`. While the outcome is equivalent (both verify gates 1-4 are present and passed), the spec's intent was to exercise the `lattice` binary end-to-end for milestone 1 — confirming the binary is built and wired, not just that the KV data exists.

**What to do:**

This is a close call. The direct-KV approach is more robust (no binary build dependency) and tests the same invariant. However, if the brief's AC1 assertion is taken literally, add an `exec.Command` call to run `lattice health gates` and assert exit 0 + output contains at least `gate1` through `gate4`. Given the integration test already requires the full `make up` stack, the binary should be present.

**Which AC/Guardrail:** AC1 (integration test assertion literal compliance).

---

### SC-5 — `make test-hello-lattice` has no `-timeout` flag — default 10-min Go test timeout may be exceeded

**File:** `Makefile:149-152`
**Review layer:** Edge Case Hunter

**What's wrong:**

`testCtx()` grants each milestone test a 5-minute context. The test suite has 6 tests (Milestones 1-5 + WriteGate5Marker). Serialised (`-p 1`), worst-case context exhaustion would be ~30 minutes. Go's default test timeout is 10 minutes. If any milestone test is slow (e.g., Refractor startup lag, Postgres cold start), the suite can be killed by Go's built-in test deadline with `panic: test timed out after 10m0s` — which is less informative than a per-test assertion failure.

**What to do:**

Add `-timeout 30m` to the `go test` invocation:

```makefile
go test -tags integration ./internal/hellolattice/... -v -p 1 -count=1 -timeout 30m
```

The CI step (`ci.yml`) inherits this from `make test-hello-lattice` and needs no separate change.

**Which AC/Guardrail:** AC7 (timing assertions remain meaningful rather than being pre-empted by a global timeout kill).

---

## 🟢 NITS

### N-1 — `book-ddl.yaml` and `books-lens.yaml` header comments reference non-existent `.json` files

**Files:** `examples/hello-lattice/book-ddl.yaml:8`, `examples/hello-lattice/books-lens.yaml:8`
**Review layer:** Blind Hunter

Both YAML file headers suggest:

```
--payload @examples/hello-lattice/book-ddl.json
--payload @examples/hello-lattice/books-lens.json
```

No `.json` files exist and there is no `convert-examples` target. The correct invocation uses the inline `--payload` flag with JSON content (as shown in the root README tutorial). Update the header comments to reference the correct usage pattern (inline `--payload`) or remove the `@file` hint entirely since the `.json` files are never created.

---

### N-2 — `TestHelloLattice_WriteGate5Marker` opens a second NATS connection unnecessarily

**File:** `internal/hellolattice/hellolattice_test.go:554-595`
**Review layer:** Blind Hunter

`TestHelloLattice_WriteGate5Marker` creates its own `substrate.Connect` call (`conn` at line 564) rather than using the suite-level `harnessConn`. The gate5 marker write is a single KVPut; there is no reason not to reuse `harnessConn`. This adds unnecessary connection overhead and introduces a second NATS connection lifecycle that must succeed for the marker to be written.

Use `harnessConn.KVPut(...)` directly with its own context.

---

### N-3 — `make test-hello-lattice` hardcodes `POSTGRES_URL` — overrides user's environment

**File:** `Makefile:151`
**Review layer:** Blind Hunter

```makefile
POSTGRES_URL=postgres://lattice:lattice_dev@localhost:5432/lattice?sslmode=disable \
```

This hardcoded value overrides any `POSTGRES_URL` the user has set in their environment. CI and local default-port users are unaffected, but anyone running a non-standard Postgres config must edit the Makefile rather than just exporting `POSTGRES_URL`. Change to:

```makefile
POSTGRES_URL?=postgres://lattice:lattice_dev@localhost:5432/lattice?sslmode=disable
```

and reference it as:

```makefile
POSTGRES_URL=$(POSTGRES_URL) \
```

---

### N-4 — Pre-existing history comments in `internal/processor/starlark_runner.go` (not from 6.4)

**File:** `internal/processor/starlark_runner.go:61,168,219,296`
**Review layer:** Blind Hunter

Lines 61 (`Story 4.2`), 168 (`Story 4.3`), 219 (`Story 4.2`), 296 (`Story 5.3`) contain story-reference history comments. The 6.4 diff correctly removed one `// Story 4.6 walk-back` comment and did not introduce new ones. The remaining four pre-date this story. Not actionable here but flagged for a follow-on cleanup pass.

---

## Architecture / NFR Compliance Sign-off

- **G1 — No CLI changes:** PASS. `git diff --staged -- cmd/lattice/` returns empty. No CLI files modified.

- **G2 — No Processor/Refractor changes (except SD-1):** PASS WITH REASONING. `internal/processor/starlark_runner.go` was modified to add `starlarkjson.Module` as the `"json"` global. This is the explicitly-authorised SD-1 supporting change. `json.decode` / `json.encode` are deterministic, pure, side-effect-free, and bounded to the string domain — they do not enable I/O, network access, or non-determinism. The module is not used by any script other than `MetaRootDDLScript`'s `meta.lens` branch. No other Processor or Refractor files were modified. Verdict: acceptable as SD-1 contract-completeness fix, not a guardrail violation.

- **G3 — Book DDL not primordial:** PASS. `internal/bootstrap/primordial.go` and `internal/bootstrap/nanoid.go` are unchanged. The book DDL is submitted at tutorial runtime via `CreateMetaVertex`.

- **G4 — No inline permissions:** PASS. No new `PermXxxKey` constants. No new identity-domain or rbac-domain grants in any non-test file. The integration test's `CreatePermission` + `AssignRole` flow is the correct operational path.

- **G5 — Deterministic assertions, bounded poll:** PARTIAL PASS. The three `time.Sleep(10 * time.Millisecond)` calls in `hellolattice_test.go` (lines 347, 434, 543) are inside bounded-deadline poll loops — correct. **FAIL** for `examples/hello-lattice/Makefile:72` which uses `@sleep 5` as a fixed wait before the Postgres query (SC-2).

- **No new `OperationReply` fields:** PASS. `internal/processor/envelope.go` `OperationReply` struct unchanged.

- **No new `ContextHint` fields:** PASS. `ContextHint` struct unchanged.

- **No `HelloLattice` / `hellolattice` / `hello-lattice` refs in `internal/processor/` or `internal/refractor/`:** PASS. Confirmed by `grep -rn` across both packages (zero hits in non-test production code).

- **meta.lens spec contract — bilateral consistency:** PASS. `MetaRootDDLScript`'s `meta.lens` branch now decodes the `spec` JSON string via `json.decode(spec_str)` and stores `spec_obj` verbatim as the aspect's `data` value. `CoreKVSource.dispatchSpec` unmarshals the `data` field as a `LensSpec` struct with `cypherRule`, `targetType`, `targetConfig` fields — which are now present in the aspect data. The SD-1 fix closes the `source`/`cypherRule` naming gap.

- **Tutorial commands match shipped 6.1 CLI flags:** PASS for `--payload`. The root `README.md` tutorial correctly uses `--payload '...'` inline JSON throughout. The `examples/hello-lattice/*.yaml` header comments reference `--payload @file.json` (pointing at non-existent `.json` files, flagged as N-1) but these are comments only, not runnable commands. The `examples/hello-lattice/Makefile` demo targets use `--payload '...'` inline — correct.

- **gate5 template file exists:** PASS. `_bmad-output/planning-artifacts/gate5-external-tester-report.md` is present as an empty template with the correct structure.

- **`make test-rollback` (5.3 regression) not broken by meta.lens contract change:** PASS. `internal/aiagent/gate4_rollback_test.go` is updated to include `targetConfig` in the lens payload, satisfying the new `required` check. The updated spec `{"id":"rollback-test-lens","canonicalName":"...","targetType":"nats_kv","targetConfig":{"bucket":"capability-kv","key":["key"]},"cypherRule":"...","engine":"simple"}` passes all three required-field checks (`cypherRule`, `targetType`, `targetConfig`).

- **Build-tag `integration` on `hellolattice_test.go` properly excludes from default `go test`:** PASS. `//go:build integration` is present on line 1. `go test ./...` (without `-tags integration`) will skip the package. The `Run all tests` CI step does not use `-tags integration`, confirmed.

- **No history comments in any 6.4-introduced file:** PASS. Grep across `internal/hellolattice/`, `examples/hello-lattice/`, and the staged diff for `internal/bootstrap/meta_ddl.go` + `internal/processor/starlark_runner.go` returns zero matches for `Story 6.4`, `Was missing`, `Added by`, `Previously did`, `Replaces`. The 6.4 diff also removed the pre-existing `// Story 5.3: .compensation aspect for lens vertices.` comment from `meta_ddl.go`.

---

## Summary Table

| ID | Severity | Title | File(s) | AC/Guardrail |
|---|---|---|---|---|
| MF-1 | 🔴 MUST FIX | Book DDL has no `"key"` field — `b.key AS book_id` projects NULL, breaking Postgres key column | `book-ddl.yaml`, `README.md`, `hellolattice_test.go`, examples `Makefile` | AC4, AC5 |
| MF-2 | 🔴 MUST FIX | `seedCapDocForAgent` fallback lets Milestone 5 pass with broken Refractor — false test pass | `hellolattice_test.go:429-436,668-686` | AC5, G5 |
| SC-1 | 🟡 SHOULD CONSIDER | `TestMain` defers `conn.Close()` before `os.Exit()` — defer skipped, connection leaks | `hellolattice_test.go:119-122` | — |
| SC-2 | 🟡 SHOULD CONSIDER | `examples/hello-lattice/Makefile` milestone-4 uses `@sleep 5` — fixed sleep, G5 violation | `examples/hello-lattice/Makefile:72` | G5 |
| SC-3 | 🟡 SHOULD CONSIDER | `lattice bootstrap inspect --format key` flag does not exist — broken demo quick start | `examples/Makefile:11,47`, `examples/README.md:21` | AC6 |
| SC-4 | 🟡 SHOULD CONSIDER | Milestone 1 test uses direct KVGet not `exec.Command` as AC1 spec requires | `hellolattice_test.go:127-161` | AC1 |
| SC-5 | 🟡 SHOULD CONSIDER | `make test-hello-lattice` has no `-timeout` flag — default 10-min Go timeout may fire | `Makefile:152` | AC7 |
| N-1 | 🟢 NIT | `book-ddl.yaml` / `books-lens.yaml` comments reference non-existent `.json` files | `examples/hello-lattice/*.yaml:8` | — |
| N-2 | 🟢 NIT | `WriteGate5Marker` opens second NATS connection; should reuse `harnessConn` | `hellolattice_test.go:554-595` | — |
| N-3 | 🟢 NIT | `make test-hello-lattice` hardcodes `POSTGRES_URL`, overriding user environment | `Makefile:151` | — |
| N-4 | 🟢 NIT | Pre-existing `// Story N.N` history comments in `starlark_runner.go` (not 6.4) | `internal/processor/starlark_runner.go:61,168,219,296` | — |

---

Review complete: **2 MUST FIX, 5 SHOULD CONSIDER, 4 NITS**
