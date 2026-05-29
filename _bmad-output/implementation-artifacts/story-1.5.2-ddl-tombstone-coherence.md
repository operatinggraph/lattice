# Story 1.5.2 — DDL tombstone coherence (M6)

**Phase 1.5 (Hardening Block) · Wave A · Sequenced AFTER 1.5.3 (shared `meta_ddl.go`, now landed)**
**Tier:** Opus
**Author:** Winston · **Date:** 2026-05-29
**Sources:** Bootstrap/Kernel CR **F-007**, Processor CR **P2-002**, Gate 5 **B3**

---

## 🔴 MANDATORY OPERATING RULES (READ FIRST)

1. **Work in the repo root** `/Users/andrewsolgan/Documents/GitHub/Lattice`. No worktrees.
2. **Do NOT commit or push.** Leave changes in the working tree for Winston.
3. **Do NOT edit planning artifacts** (`_bmad-output/planning-artifacts/*`). Contract questions → append to `cmd/processor/CONTRACT-AMENDMENT-REQUEST.md` and continue elsewhere. Doc updates land in `docs/`.
4. **No history comments** (`// Story 1.5.2`, `// was`, `// Replaces`). Comments describe current behavior only.
5. **Halt and escalate** (append blocker to `CONTRACT-AMENDMENT-REQUEST.md`) on any stuck loop: re-attempting the same op after 3+ failures, immediate reverts, re-reading the same files for an absent answer, cycling between two approaches, or an unresolved test failure after 2 genuine debug attempts. Token budget is tracked, NOT enforced.
6. **Append a closing summary** to §8 of THIS file when done.
7. **Confine edits** to: the `TombstoneMetaVertex` branch of `internal/bootstrap/meta_ddl.go`, `internal/processor/ddl_cache.go` (`loadMetaVertex`), optionally `internal/processor/step8_commit.go` (invalidation-loop dedup), their tests, and the contract doc. **Do NOT touch the `UpdateMetaVertex` or `CreateMetaVertex` branches** (1.5.3 just shipped Update; don't disturb it).

---

## 1. Goal

Close the M6/B3 DDL tombstone-coherence gap, which has two halves:

- **Cascade (Bootstrap F-007):** `TombstoneMetaVertex` today tombstones only the root `vtx.meta.<id>` key and rewrites `.compensation` to an "irreversible" note. All content aspects (`.description`, `.script`, `.canonicalName`, …) stay **live forever** — orphaned, with no GC path, and an inconsistent root-dead/aspects-alive state.
- **Cache eviction (Processor P2-002, Gate 5 B3):** after a tombstone commits, the Processor calls `DDLs.Invalidate`, which calls `loadMetaVertex`. `loadMetaVertex` does **not** check `isDeleted`, so a tombstoned DDL is re-loaded and stays cached indefinitely — operations on a tombstoned class keep getting hydrated/executed instead of `NoDDLForClass`.

Fix both so a tombstone fully removes the meta-vertex from Core KV (all keys) and from the DDL cache.

---

## 2. Required context — read these ONLY

- `internal/bootstrap/meta_ddl.go` — the whole file. Edit **only** the `TombstoneMetaVertex` branch (~lines 360–390 post-1.5.3). Note the `getattr(root, "class")` class-detection pattern the `UpdateMetaVertex` branch already established (`class` is a Starlark reserved word) — reuse it.
- `internal/processor/ddl_cache.go` — `loadMetaVertex` (~150–255) and `Invalidate` (already normalizes aspect keys → root via `parts[:3]`).
- `internal/processor/step8_commit.go` — the post-commit invalidation loop (~155–173, the `hasMetaVertexMutation` → `DDLs.Invalidate` block).
- Existing DDL-cache + tombstone tests: `grep -rln "TombstoneMetaVertex\|loadMetaVertex\|Invalidate" --include="*_test.go" .`. Extend them; match harness style.
- Contract/components doc: `docs/components/processor.md` (the meta-DDL section 1.5.3 just updated) — add the tombstone-cascade + cache-eviction behavior.

Do NOT read large planning artifacts.

---

## 3. Design decisions (LOCKED by Winston)

### 3.1 Cascade tombstone (Bootstrap `meta_ddl.go`)

In the `TombstoneMetaVertex` branch:

- Detect the class from the hydrated root: `target_class = getattr(root, "class")` (guarded `hasattr`), `is_lens = target_class == "meta.lens"`.
- Emit `make_tombstone` for the **root** key **and every aspect key** of the class:
  - **DDL classes** (`meta.ddl.*`): `.canonicalName, .permittedCommands, .description, .script, .inputSchema, .outputSchema, .fieldDescription, .examples, .compensation`
  - **`meta.lens`**: `.canonicalName, .description, .spec, .compensation`
- **`.compensation` is tombstoned too** (not rewritten to an "irreversible" note). No Go code reads `.compensation` from Core KV post-commit — the compensating-op contract is resolved client-side from the forward op's reply (Guardrail 1) — so tombstoning it breaks nothing and yields a fully-coherent delete. Remove the current `make_update(meta_key + ".compensation", {...irreversible...})` mutation.
- Aspect tombstones are **unconditional** (no prior-value reads needed; `make_tombstone` writes `isDeleted=true` regardless). The branch still reads only the root key (for liveness + class) — no new `ContextHint.Reads` requirement for aspects.
- **`expectedRevision` / `force`:** unchanged — apply `expectedRevision` to `mutations[0]` (the **root** tombstone) only; `force` bypasses. Aspect tombstones never carry a revision assertion (independent sequences).
- Keep the existing `vertex_alive(state, meta_key)` guard (`UnknownMetaVertex` if already dead/absent) and the `MetaVertexTombstoned` event.

### 3.2 Cache eviction (`ddl_cache.go` `loadMetaVertex`)

- Add an `IsDeleted bool \`json:"isDeleted"\`` field to the `rootDoc` struct.
- **Immediately after** unmarshaling `rootDoc` (before any aspect reads / canonicalName resolution), if `rootDoc.IsDeleted` is true → `return ref, false, nil`. This makes `Invalidate` delete the entry from `byName`/`byMetaPK` and not re-insert it, and makes any direct `loadMetaVertex` of a tombstoned vertex report absent.

### 3.3 (Optional nicety) step8 invalidation dedup

The cascade emits ~9 `vtx.meta.<id>.*` mutations that all normalize to the same root in `Invalidate`, so the current loop calls `Invalidate` ~9× redundantly per tombstone. Optionally dedup: collect the distinct root keys (`strings.Join(strings.Split(m.Key,".")[:3],".")`) from the `vtx.meta.*` mutations into a set, and `Invalidate` each root once. Correctness does not depend on this (Invalidate is idempotent) — do it only if clean; skip if it adds risk.

---

## 4. Out of scope (do NOT touch)

- `UpdateMetaVertex` / `CreateMetaVertex` branches.
- Routing installs through the Processor → Story 1.5.5.
- Conformance freeze → Story 1.5.7.
- Re-enabling Hello Lattice M4–M6 / flipping Gate 5 → Story 1.5.6 (this story is a *dependency* of it; do not flip the gate marker here).
- A background GC sweep for already-orphaned aspects from pre-fix tombstones — not in scope; note it as a residual if you wish.

---

## 5. Verification gates (run all; paste tails into §8). NOTE: between local full-suite runs, `make down && make up` to avoid shared-NATS cross-test contamination (Deviation 14).

```
go build ./...
make vet
golangci-lint run ./...
make down && make up && make verify-kernel
go test ./internal/bootstrap/... -count=1
go test ./internal/processor/... -p 1 -count=1
go test ./... -p 1 -count=1
make test-bypass
make test-capability-adversarial
```

If a different package flakes on a repeated full-suite run, re-run that package in isolation (Deviation 14 cross-package contamination); per-package green is authoritative locally, CI (clean stack) is the final gate.

## 6. Deliverables checklist

- [ ] `TombstoneMetaVertex` cascades `make_tombstone` to the root + all class-appropriate aspect keys (DDL set and lens set); `.compensation` tombstoned; old irreversible-note `make_update` removed.
- [ ] `expectedRevision`→root only + `force` preserved; `vertex_alive` guard + `MetaVertexTombstoned` event intact.
- [ ] `loadMetaVertex` evicts on `rootDoc.isDeleted == true` (returns absent before aspect reads).
- [ ] (Optional) step8 invalidation loop dedups roots.
- [ ] Tests: cascade mutation set for DDL + lens; expectedRevision/force; cache eviction — after a `TombstoneMetaVertex` commit the class no longer resolves (`Lookup`/`LookupByMetaKey` report absent) and a follow-up op on the class is not hydrated.
- [ ] `docs/components/processor.md` documents tombstone cascade + cache eviction.
- [ ] All §5 gates green; the pre-existing aiagent Gate-4 rollback test still passes.

## 7. Notes

This story is a prerequisite for Story 1.5.6 (Gate 5 full pass). It does not itself flip any gate marker.

## 8. Closing summary (sub-agent fills in)

_(append here)_
