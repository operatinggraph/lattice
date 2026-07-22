# Story 1.5.3 — UpdateMetaVertex expansion

**Phase 1.5 (Hardening Block) · Wave A · Sequenced BEFORE 1.5.2 (shared `meta_ddl.go`)**
**Tier:** Opus
**Author:** Winston · **Date:** 2026-05-29
**Source:** Bootstrap/Kernel CR **F-002** (`phase-1.5-cr-bootstrap-kernel.md`)

---

## 🔴 MANDATORY OPERATING RULES (READ FIRST)

1. **Work in the repo root** `/Users/andrewsolgan/Documents/GitHub/Lattice`. No worktrees.
2. **Do NOT commit or push.** Leave changes in the working tree for Winston.
3. **Do NOT edit planning artifacts** (`_bmad-output/planning-artifacts/*`). Contract questions → append to `cmd/processor/CONTRACT-AMENDMENT-REQUEST.md` and continue elsewhere. Doc updates land in `docs/`.
4. **No history comments** (`// Story 1.5.3`, `// was`, `// Replaces`). Comments describe current behavior only.
5. **Halt and escalate** (append blocker to `CONTRACT-AMENDMENT-REQUEST.md`) on any stuck loop: re-attempting the same op after 3+ failures, immediate reverts, re-reading the same files for an absent answer, cycling between two approaches, or an unresolved test failure after 2 genuine debug attempts. Token budget is tracked, NOT enforced.
6. **Append a closing summary** to §8 of THIS file when done.
7. **Do NOT touch the `TombstoneMetaVertex` branch.** Story 1.5.2 owns it next (aspect-cascade + cache eviction). Confine your edits to the `UpdateMetaVertex` branch + its tests + docs.

---

## 1. Goal

`UpdateMetaVertex` (the `meta_ddl.go` Starlark `MetaRootDDLScript`) today mutates only `.description` (and `.compensation`). Worse, it **blanks `.description` to `""` when the field is absent**. A DDL/Lens script bug therefore requires a `TombstoneMetaVertex` + `CreateMetaVertex` cycle, which mints a **new** `metaKey` and breaks every caller holding the old key.

Extend `UpdateMetaVertex` to hot-fix any self-description aspect in place, **preserving `metaKey` identity**, mutating **only the fields present** in the payload.

---

## 2. Required context — read these ONLY

- `internal/bootstrap/meta_ddl.go` — the whole file. Your edits are confined to the `UpdateMetaVertex` branch (lines ~206–244) plus its doc block.
- Existing meta-DDL behavior tests. Find them: `grep -rln "UpdateMetaVertex" --include="*_test.go" .` (likely under `internal/processor/` and/or `packages/`). Extend the existing UpdateMetaVertex test(s); match their harness style (they install/seed then submit a `UpdateMetaVertex` op and assert Core KV aspect state).
- `docs/contracts/` — locate the meta-DDL / DDL self-description contract page (`grep -rln "UpdateMetaVertex\|self-description aspect" docs/`). Update it to document the new updatable-field set + metaKey-stability guarantee.

Do NOT read large planning artifacts.

---

## 3. Design decisions (LOCKED by Winston)

### 3.1 Updatable field set

For **DDL** meta-vertices (`meta.ddl.*`), these aspects become updatable (each optional; mutate only if present):

| payload field | aspect key suffix | aspect data shape | validation |
|---|---|---|---|
| `description` | `.description` | `{"text": v}` | non-empty string |
| `script` | `.script` | `{"source": v}` | non-empty string |
| `permittedCommands` | `.permittedCommands` | `{"commands": v}` | list of strings |
| `inputSchema` | `.inputSchema` | `{"schema": v}` | non-empty string |
| `outputSchema` | `.outputSchema` | `{"schema": v}` | non-empty string |
| `fieldDescription` | `.fieldDescription` | `{"fieldDescriptions": v}` | dict |
| `examples` | `.examples` | `{"examples": v}` | list |

For **lens** meta-vertices (`meta.lens`), `description` and `spec` are updatable. `spec` is validated exactly as the Create-lens branch does (`json.decode`, require `cypherRule`/`targetType`/`targetConfig`), stored as `.spec` (`lensSpec` class) verbatim.

- **`canonicalName` is immutable** — it is the stable logical identity. If a caller includes it in the payload, **ignore it** (do not mutate, do not fail). Document this.
- **`compensation` is script-managed** — never directly settable by the caller.
- The validation for these optional fields must reuse the same helpers/shapes the Create branch uses so Update and Create stay shape-identical. Use type checks consistent with `required_string`/`required_list`/`required_dict` but applied conditionally (field optional).

### 3.2 Mutate only fields present — and reject empty updates

- Build the mutation list dynamically: for each updatable field, `if hasattr(p, field)` → validate → append `make_update(meta_key + suffix, data)`.
- **If no updatable field is present, `fail("InvalidArgument: UpdateMetaVertex: no updatable fields provided")`.** (This also fixes the current latent description-blanking bug — never write `""` for an absent field.)
- `metaKey` is read from the payload and reused verbatim; **never** call `nanoid.new()` in this branch. The vertex root key and `canonicalName` are untouched, so identity is preserved by construction.

### 3.3 Compensation captures prior values of changed fields

The `.compensation` aspect must let a rollback restore the pre-update state of **exactly the fields this op changed**:

- For each field being updated, read its **prior** value from `state` (the existing aspect doc's `.data`), mirroring how the current code reads `prior_desc` from `meta_key + ".description"`. Guard every read (key may be absent / malformed → treat as missing).
- Emit one `make_update(meta_key + ".compensation", {...})` whose `inverseOperationType` is `"UpdateMetaVertex"` and whose `payloadTemplate` is `{"metaKey": meta_key, <field>: prior_value, ...}` containing **only the fields being changed** (plus `metaKey`). `revisionTemplate` stays `{}` (unchanged from current Update compensation).
- The caller MUST declare every read aspect key (`meta_key + ".<field>"` for each field it intends to update) in `ContextHint.Reads`. Make your test harness declare these. Document the requirement on the contract page.

### 3.4 expectedRevision (OCC) — deterministic single-aspect assertion

`expectedRevision` (+ `force` bypass) is preserved. Since an Update may now touch any subset of aspects and each aspect has its own NATS revision sequence, apply the assertion to **one** deterministic aspect:

- Define the canonical field order: `description, script, permittedCommands, inputSchema, outputSchema, fieldDescription, examples, spec`.
- Apply `expectedRevision` to the `make_update` of the **first present field** in that order (its `["expectedRevision"]`). Never apply it to `.compensation` (independent sequence — would cause spurious conflicts, same rationale as the current code's comment).
- Multi-aspect atomic OCC is a known Phase-2 limitation — note it in a code comment (current behavior, not history) and on the contract page.

---

## 4. Out of scope (do NOT touch)

- `TombstoneMetaVertex` branch / aspect-cascade / DDL cache eviction → **Story 1.5.2** (next).
- `CreateMetaVertex` branch (only *reuse* its validation shapes; don't change it).
- Routing installs through the Processor → Story 1.5.5.
- Conformance freeze → Story 1.5.7.

---

## 5. Verification gates (run all; paste tails into §8)

```
go build ./...
make vet
golangci-lint run ./...
make up && make verify-kernel
go test ./internal/processor/... -p 1 -count=1
go test ./... -p 1 -count=1
make test-bypass
make test-capability-adversarial
```

Flake-retry per Deviation 14 allowed (re-run once); a flake claim without re-run is a drift signal.

## 6. Deliverables checklist

- [ ] `UpdateMetaVertex` mutates any present subset of {description, script, permittedCommands, inputSchema, outputSchema, fieldDescription, examples} for DDL classes and {description, spec} for lens; only present fields; reuses Create's validation shapes.
- [ ] Absent fields are never blanked; empty-update payload is rejected.
- [ ] `canonicalName` ignored if supplied; `metaKey` preserved (no `nanoid.new()` in this branch).
- [ ] `.compensation` payloadTemplate captures prior values of exactly the changed fields.
- [ ] `expectedRevision` applied to the first-present-field mutation in canonical order; `force` bypass intact.
- [ ] Tests: per-field updates preserve untouched aspects; multi-field; empty-update rejection; canonicalName-immutability; metaKey stability; compensation prior-value capture; expectedRevision happy + conflict.
- [ ] Contract doc updated (updatable set, metaKey stability, ContextHint.Reads requirement, OCC limitation).
- [ ] All §5 gates green.

## 7. Notes

The `meta_ddl.go` LOC target was ≈200; it's at 273. This story will grow it — that's expected and fine; the LOC note is stale guidance, not a constraint.

## 8. Closing summary (sub-agent fills in)

### Deliverables checklist (vs §6)

- [x] `UpdateMetaVertex` mutates any present subset of {description, script, permittedCommands, inputSchema, outputSchema, fieldDescription, examples} for DDL classes and {description, spec} for lens; only present fields; reuses Create's validation shapes (non-empty-string / list / dict; lens `spec` reuses the Create-branch `json.decode` + cypherRule/targetType/targetConfig checks). Class is selected from the hydrated vertex root's `class` field (`is_lens = class == "meta.lens"`).
- [x] Absent fields are never blanked (no `""` fallback); empty-update payload rejected with `InvalidArgument: UpdateMetaVertex: no updatable fields provided`.
- [x] `canonicalName` ignored if supplied (no `.canonicalName` mutation, no error; canonicalName-only payload → empty-update rejection); `metaKey` preserved — read verbatim, no `nanoid.new()` in this branch, all mutation keys rooted at the original metaKey.
- [x] `.compensation` payloadTemplate captures prior values of exactly the changed fields (read from `state`, guarded → `null` when absent/malformed). `spec` prior value re-encoded via `json.encode` to a JSON string. `revisionTemplate` stays `{}`.
- [x] `expectedRevision` applied to the first-present-field mutation in canonical order (`description, script, permittedCommands, inputSchema, outputSchema, fieldDescription, examples, spec`); never to `.compensation`; `force: true` bypass intact; non-integer rejected.
- [x] Tests: per-field updates preserve untouched aspects (exactly 2 mutations: field + compensation); list/dict fields; multi-field; empty-update rejection; canonicalName-immutability; metaKey stability; compensation prior-value capture (incl. missing-prior → null and lens spec re-encode); expectedRevision happy + force-bypass + non-integer-conflict; unknown-vertex rejection; lens spec+description + lens spec-missing-cypherRule rejection.
- [x] Contract doc updated: `docs/components/processor.md` — new "### `UpdateMetaVertex` field set" subsection (updatable set table, metaKey stability, canonicalName immutability, empty-update rejection, `ContextHint.Reads` requirement, OCC single-aspect/Phase-2 limitation) + pairing-table row reworded.
- [x] All §5 gates green.

### Files touched

- `internal/bootstrap/meta_ddl.go` — rewrote the `UpdateMetaVertex` branch only (dynamic per-field mutation list via nested `add_string_field`/`add_list_field`/`add_dict_field` + guarded `prior_data_field`; lens `spec` handled inline reusing Create validation); refreshed the top doc block to describe current Update behavior. `CreateMetaVertex` and `TombstoneMetaVertex` branches untouched.
- `internal/bootstrap/update_metavertex_test.go` — NEW. Unit tests driving the Starlark Update branch directly via `processor.NewStarlarkRunner` with hydrated prior `state` (same harness style as `self_description_e2e_test.go`).
- `docs/components/processor.md` — contract documentation (this is the meta-DDL/compensation contract page found via grep; `docs/contracts/*` had no UpdateMetaVertex page).

The pre-existing `internal/aiagent/gate4_rollback_test.go` UpdateMetaVertex round-trip continues to pass unchanged, validating the description compensation path end-to-end through NATS.

### Gate tails (§5)

```
$ go build ./...
(no output — success)

$ make vet
==> go vet ./... (excluding vendored ANTLR parsers)
EXIT:0

$ golangci-lint run ./...
0 issues.

$ make up && make verify-kernel
Lattice ready — primordial bootstrap complete
...
verify-kernel: ALL ASSERTIONS PASSED

$ go test ./internal/processor/... -p 1 -count=1
ok  	github.com/operatinggraph/lattice/internal/processor	19.979s

$ go test ./... -p 1 -count=1
ok  internal/bootstrap 0.389s · ok internal/aiagent 2.681s · ok internal/processor 19.849s
ok internal/bypass · ok internal/substrate · ok packages/* · ok cmd/lattice/*
(all packages ok / [no test files]; no FAIL)

$ make test-bypass
PHASE 1 GATE 3: PASSED (4/4 DEFENDED)
ok  	github.com/operatinggraph/lattice/internal/bypass	3.305s

$ make test-capability-adversarial
PHASE 1 GATE 3: PASSED (4/4 DEFENDED)
ok  	github.com/operatinggraph/lattice/internal/bypass	0.342s
```

No flake re-runs were needed (all gates passed first attempt).

### Deviations / CARs

- None. No contract amendments required; no stuck loops; no `CONTRACT-AMENDMENT-REQUEST.md` entries.
- Note for Winston: `processor.log` (untracked) and the modified gate2/gate3 report artifacts in the working tree are pre-existing / generated by gate runs, not part of this story's source edits.
