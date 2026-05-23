# Code Review Report — Story 5.2: Cold-Start AI Agent Traversal & Operation Submission

**Commit reviewed:** `3c29b9a` (Story 5.2 as shipped)
**Reviewer:** bmad-code-review (Sonnet 4.6)
**Date:** 2026-05-23
**Spec file:** `_bmad-output/implementation-artifacts/5-2-cold-start-ai-agent-traversal.md`

**Files in scope:**
- `internal/aiagent/traversal.go` (new)
- `internal/aiagent/traversal_test.go` (new)
- `internal/aiagent/fr19_northstar_test.go` (new)
- `internal/testutil/pipeline.go` (modified)

**Note on subagent parallelism:** This review was performed inline (no parallel subagent session support). All three layers — Blind Hunter, Edge Case Hunter, Acceptance Auditor — were run sequentially with full project access.

---

## Summary

**Zero MUST FIX items found.** The implementation is architecturally clean, NFR-S10 is preserved by design, and the core traversal logic mirrors the DDL cache's own canonicalName lookup precisely. Two SHOULD CONSIDER items and four NITS are documented below.

---

## 🔴 MUST FIX

None.

---

## 🟡 SHOULD CONSIDER

### SC-1 — Missing ddlKey cross-check: test does not assert DiscoverDDL returned the same key the Processor wrote

**File:** `internal/aiagent/fr19_northstar_test.go`, around line 139–146
**Review layer:** Acceptance Auditor

**What's wrong:**

The story spec (AC4) states: "the traverser returns the same key the seeder got." The north-star test proves the agent finds *a* DDL with the right canonical name, but it never asserts that `ddlKey` equals the meta-vertex key committed by the `CreateMetaVertex` operation.

The `tracker.Data["mutationKeys"]` slice is available on the seeder tracker (already read at line 93–104). The primary mutation key for a `CreateMetaVertex` operation is the new `vtx.meta.<NanoID>` key. Comparing `ddlKey` against `mutationKeys[0]` would tighten the test and prove the traversal hit exactly the seeded vertex rather than any pre-existing meta-vertex that happened to match.

Today's test relies on the uniqueness of the timestamp-seeded canonical name for correctness — it is true that no pre-existing vertex can match `"NorthStarOp<milliseconds>"` in a fresh embedded-NATS instance. But the spec's intent is an explicit assertion.

**What to do:** After `DiscoverDDL` returns `ddlKey`, extract `mutationKeys` from the seeder's tracker and assert `ddlKey == mutationKeys[0]`. This is Actionable.

**Which AC it would violate:** AC4 (north-star integration test: "purely from the graph... returns the same key the seeder got").

---

### SC-2 — ErrAspectMissing wraps underlying KV error with `%v` instead of `%w`, losing the error chain

**File:** `internal/aiagent/traversal.go`, lines 180, 193, 206, 219, 234
**Review layer:** Blind Hunter

**What's wrong:**

In `ReadDDLAspects`, every KV-not-found error is formatted as:
```go
return nil, fmt.Errorf("%w: description at %s: %v", ErrAspectMissing, ddlKey, err)
```

The `%v` verb for `err` includes the error message in the string but does NOT wrap `err` in the error chain. This means:
- `errors.Is(returnedErr, substrate.ErrKeyNotFound)` returns `false`
- `errors.As(returnedErr, &kvErr)` cannot find the substrate error

By contrast, `ReadCapability` uses `%w` for the substrate error, so callers can inspect whether the underlying failure was a KV miss versus a network error.

For a client library where callers may want to distinguish "aspect genuinely absent" (expected during schema migration) from "KV lookup failed" (transient), losing the chain is a latent usability issue.

**What to do:** Change `%v` to `%w` on each of the five `ErrAspectMissing` wrapping lines, or document explicitly in the godoc that callers cannot unwrap further than `ErrAspectMissing`. The simplest fix:
```go
return nil, fmt.Errorf("%w: description at %s: %w", ErrAspectMissing, ddlKey, err)
```
Go 1.20+ supports multiple `%w` verbs in a single `Errorf` call. This is Actionable.

---

## 🟢 NITS

**N-1** — `internal/aiagent/fr19_northstar_test.go`, `buildPayloadFromSchema` godoc comment says "Unknown types fall back to the JSON `null` literal" but the actual code calls `t.Fatalf` for unknown types. The comment is stale from an earlier iteration. Update the godoc to match the implementation.

**N-2** — `internal/aiagent/traversal.go` line 108: the godoc for `ReadCapability` says "Returns ErrKeyNotFound (wrapped)" — it is `substrate.ErrKeyNotFound`, not the package's own error. The doc could be more precise: "Returns `substrate.ErrKeyNotFound` (wrapped in an `aiagent:` prefix error) when no capability entry exists."

**N-3** — `internal/aiagent/traversal_test.go`, `setupUnitEnv`: the `ctx, conn` return values are both used and the pattern is fine, but the test creates KV buckets WITHOUT `LimitMarkerTTL: time.Second` that `ProvisionHarness` uses for production harnesses. No impact on correctness in these unit tests (buckets hold no TTL-sensitive data), but the two harness setups have diverged in a way future authors might not notice.

**N-4** — `internal/aiagent/fr19_northstar_test.go`, `buildCapDoc` helper hardcodes `Roles: []string{bootstrap.RoleOperatorKey}` for every actor. The seeder actor (`NorthStarSeedrID00001`) using `RoleOperatorKey` is correct, but the AI agent actor (`NorthStarAgentID00001`) probably should not carry the operator role. This has no behavioral impact in the current test (authorization is permission-based not role-based in these tests), but it makes the test semantics slightly misleading — an AI agent is granted operator-level roles. Worth trimming to `[]string{}` for the agent's cap doc.

---

## Architecture / NFR Compliance Sign-off

- **NFR-S10 (same Processor path):** Verified. `grep -rn "isAIActor|AIAgentBypass|ai-actor-special-case" internal/processor/` returns zero hits. The `TestFR19_NFR_S10_SameProcessorPath` negative test confirms unauthorized agent ops are rejected at step 3. Pass.
- **No new ContextHint fields:** Verified. `OperationEnvelope.ContextHint` struct is untouched. Pass.
- **No adjacency reads in Traverser:** Verified. `traversal.go` uses only `KVGet` and `KVListKeys`. No adjacency bucket reads. Pass.
- **Capability KV is the only lens-output read:** Verified. `ReadCapability` reads `cap.identity.*` from Capability KV (the one contract-defined exception per Contract #6 §6.2). No other lens buckets accessed. Pass.
- **NFR-S6 (no business data in error messages):** The traversal package is client-side and returns errors to the calling agent (which already knows its own actorID, the operationType it's searching for, and the ddlKey it passed in). No business data leak to third parties. Pass.
- **DDL aspect field shapes:** All five aspect field paths (`data.text`, `data.schema`, `data.schema`, `data.fieldDescriptions`, `data.examples`) verified against the CreateMetaVertex DDL script in `internal/bootstrap/meta_ddl.go`. Consistent.
- **testutil.PipelineConfig.FilterSubjects change:** Backward-compatible (defaults to `[]string{"ops.default"}` when empty). Pass.

---

Review complete: 0 MUST FIX, 2 SHOULD CONSIDER, 4 NITS
