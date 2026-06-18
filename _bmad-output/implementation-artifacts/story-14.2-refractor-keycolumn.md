# Story 14.2 — Refractor actorAggregate explicit key-column (Contract §10.2 Option b)

**Status:** review
**Epic:** 14 — Loftspace Lease-Application Reference Vertical
**Tier:** Opus — a **small, surgical, opt-in** change to a **guarded engine** (`internal/refractor`). It is **engine-enabler only** — it ships **no package content** (14.4 ships the lease lens), **no Weaver change**, **no frozen-contract edit**. The risk is **not** size; it is that this is the **projection-plane / convergence engine**, and the change must be **byte-for-byte transparent** to every existing actorAggregate lens (the four built-ins + the proof lens) while adding one new branch. Review: **full 3-layer adversarial** (Blind Hunter / Edge Case Hunter / Acceptance Auditor) per `bmad-code-review` — a guarded-engine key-shape change is exactly what the three independent lenses catch (the Acceptance Auditor against the 2 ACs + §10.2 Option (b); the Edge Case Hunter on the project-vs-delete-path consistency + the bare-NanoID validation; Blind Hunter on the diff). Plus the gates in §8.
**Epic spec:** `_bmad-output/planning-artifacts/epics/phase-2-epics.md` → "Story 14.2: Refractor actorAggregate explicit key-column (Contract §10.2 Option b)" (lines ~688–701) + the Epic 14 framing (~662–670) + the build order (14.1, 14.2, 14.3 → 14.4 → 14.5). Read it for the user-story framing and the **two** ACs (verbatim in §1).
**Binding grounding (FROZEN — read, build TO, do NOT edit):**
- `docs/contracts/10-orchestration-surfaces.md` **§10.2** IN FULL (lines ~84–167) — the Weaver target-Lens output (D4). The load-bearing parts: the row-key shape **`<targetId>.<entityId>`** where the entity segment is the **bare NanoID** (lines ~98–103, ~105–118; the example key `leaseApplicationComplete.Lk2Pn6mQrtwzKbcXvP3T`), and the **2026-06-18 actorAggregate amendment, Option (b)** (lines ~120–131): "such a lens declares an explicit **key column** (the bare-NanoID `<entityId>`) that the actorAggregate `BuildKey` emits **instead of** its default `{actorSuffix}` (= `<type>.<id>`), so the row key stays `<targetId>.<entityId>` (bare NanoID) and Weaver's `splitRowKey` accepts it unchanged. The frozen §10.2 key + `splitRowKey` stay frozen; the change is localized to the Refractor Output-descriptor machinery Epic 12 introduced." **This is the spec. Build TO it.** The §10.2 key shape + `splitRowKey` MUST stay frozen.
- **Contract #1 §1.1** (`docs/contracts/01-key-shapes.md`) — vertex keys `vtx.<type>.<id>`; the `<id>` is a **NanoID over the canonical alphabet** (`substrate.IsValidNanoID`, length 20, **no dots**). The "bare-NanoID `<entityId>`" the key column emits is exactly this `<id>` slot — dot-free, so it cannot break `splitRowKey`'s single-dot split.
**Design of record (RATIFIED — read, do NOT edit):** `_bmad-output/planning-artifacts/sprint-change-proposal-2026-06-18.md` — the "External I/O Bridge" change proposal, **RATIFIED 2026-06-18 (Andrew)**. The M2 decision (lines 33, 38, 88, 148) is the exact problem + resolution this story implements: *"actorAggregate forces the row key to `{actorSuffix}` = `leaseApp.<id>` (type.id, no `IntoKey` escape — `internal/refractor/projection/output.go:157-162`, `driver.go:56`), but Weaver's `splitRowKey` **rejects** anything but a bare NanoID after the first `.` and **drops the row** (`internal/weaver/evaluator.go:508-518`) → the gap never converges."* The ratified resolution is **Option (b): "extend the actorAggregate projection to honor an explicit key column"** — "the FROZEN §10.2 key + Weaver `splitRowKey` stay untouched (the change lands in the Epic-12 Output-descriptor machinery)." (Line 38 names the exact source lines this story touches.)
**Grounding (the Epic-12 machinery you extend — read, do NOT edit the contract; the code is yours to change):**
- **Contract #6 §6.13** (`docs/contracts/06-capability-kv.md`) — the **Output descriptor** (the `projectionKind: actorAggregate` lens-definition aspects: `anchorType`, `outputKeyPattern`, `bodyColumns`, `emptyBehavior`, `realnessFilter`, `freshness`) Epic 12 ratified (CAR Request 5, `cmd/refractor/CONTRACT-AMENDMENT-REQUEST.md`). **You ADD an optional field** (`keyColumn`) to this descriptor's three in-tree mirrors (§2 Item A). Read §6.13 to confirm the descriptor is the right home and the field is additive/opt-in (a brand-new package lens that omits it behaves exactly as today).
**Depends on:** **13.1 (DONE — §10.2 amended)** — the contract carries the Option (b) clause this story builds to. **Epic 12 (DONE — Stories 12.3/12.4)** — the `projectionKind: actorAggregate` plan compiler + the Output-descriptor machinery (`internal/refractor/projection/{output,plan,driver,empty}.go`, the spec→Rule→descriptor plumbing, and the production proof `internal/refractor/refractor_package_actoraggregate_proof_e2e_test.go`). 14.2 **extends** that machinery; it does not re-architect it. **14.1 (DONE)** — the `service` instance + the `providedTo` link 14.4's lens reads; **14.2 is independent of 14.1** (14.1 ships no lens), but 14.2 is the engine capability 14.4's lens needs.
**Forward (note, do NOT build — §5):** **14.4** ships the `leaseApplicationComplete` actorAggregate lens (`AnchorType: leaseApp`, multi-hop `MATCH (app)-[:applicationFor]->(id), (id)<-[:providedTo]-(inst:service)`) that will **declare** this key column to emit the bare-NanoID `<entityId>` into `weaver-targets`, so the gap converges. **14.2 builds the engine mechanism + a test lens proving it; it does NOT build the lease lens.** **14.2 GATES 14.4** — the convergence lens must not be started until this is settled (change-proposal line 148).
**Workflow:** you are the DS (dev) sub-agent. Repo root, no worktree. Do **NOT** commit/push/branch. Do **NOT** edit frozen contracts (`docs/contracts/*`) or planning artifacts (`epics/*.md`, `lattice-architecture.md`, `MORPH-DEVIATIONS`, the data-contracts, the change proposal). You MAY add a `/docs` note if useful (not required). A genuine frozen-contract gap → `cmd/refractor/CONTRACT-AMENDMENT-REQUEST.md` + flag at the TOP of your closing summary; do **not** edit the contract. Leave all changes in the working tree for Winston.

> **TOP-OF-STORY FLAG:** **No CONTRACT-AMENDMENT-REQUEST is anticipated.** The contract work is already done: §10.2 was amended by 13.1 to carry Option (b) (the explicit key column emitting the bare-NanoID `<entityId>`), and §6.13 already frames the Output descriptor as the home for actor-aggregate projection options. This story implements the ratified Option (b) in the Epic-12 Output-descriptor machinery — it adds an **optional, opt-in** descriptor field and one `BuildKey` branch. The frozen surfaces (§10.2 row-key shape, `splitRowKey`, the §6.13 descriptor's existing fields) are shapes this change **conforms to / extends additively**, not edits. If you find a genuine gap (e.g. the descriptor cannot carry the field, or Weaver's `splitRowKey` does in fact need a change to accept the bare-NanoID key — it should not; §4 confirms it already accepts it), flag it; do not edit the contract.

---

## 0. THE HEADLINE — an actorAggregate lens whose ANCHOR *is* the candidate entity emits a BARE-NanoID key, and the DELETE PATH must agree (read first; it governs everything)

This is the one thing to get right. Two facts collide, and Option (b) reconciles them:

1. **Weaver's `weaver-targets` key is `<targetId>.<entityId>` with a BARE-NanoID `<entityId>`.** `splitRowKey` (`internal/weaver/evaluator.go:508-518`) splits on the **first dot**, then **requires `substrate.IsValidNanoID(entityID)`** — a value with a dot (like `leaseApp.<id>`) fails and the **row is silently dropped** → the gap never converges (the M2 defect, change-proposal line 38/88).
2. **Today's actorAggregate `BuildKey` emits `{actorSuffix}` = `<type>.<id>`** — it strips only the `vtx.` prefix (`output.go:157-162`), leaving the type segment in the key. For a convergence lens anchored on `leaseApp`, that produces `leaseApplicationComplete.leaseApp.<id>` — **two dots after the targetId**, which `splitRowKey` rejects.

**Option (b)'s resolution (BINDING):** the lens declares an **explicit key column** naming a RETURN alias that projects the **bare-NanoID `<entityId>`**; `BuildKey` substitutes **that** into the `{actorSuffix}` slot instead of the `<type>.<id>` actor suffix. The row key becomes `leaseApplicationComplete.<leaseAppId>` (one dot, bare NanoID) → `splitRowKey` accepts it unchanged. **The §10.2 key shape + `splitRowKey` are untouched** — the bare NanoID simply fills the `<entityId>` slot the contract already specifies.

**The load-bearing design tension you MUST resolve — `BuildKey` is on BOTH the project path AND the delete path:**

`projection.InstallActorAggregate` wires `desc.BuildKey` in **two** places (`driver.go`):
- **Project path** — `EnvelopeFn` calls `outKey := d.BuildKey(actorKey)` (`driver.go:56`) **with a RETURN row in hand**.
- **Delete path** — `p.SetActorDeleteKey(desc.BuildKey)` (`driver.go:159`); the pipeline's actor-disappearance paths call `d.BuildKey(actorKey)` with **ONLY the actorKey, NO row** (`pipeline/evaluate.go:89` the tombstone shortcut, and `:351` the `reprojectActors` missing-actor path).

So a key-column value sourced from the **row** is **unavailable on the delete path**. If you naively read the column from the row, an actor-disappearance Delete would compute a **different key** than the project upsert wrote → the tombstone/delete lands on the **wrong key** → a stale `violating` row is never retracted. **That is a convergence/correctness bug, and it is the #1 thing the Edge Case Hunter will probe.**

**The clean resolution (BINDING — adopt this; see §2 + Q1 for the precise rule):** for the §10.2 convergence case **the candidate entity IS the anchor** — `AnchorType: leaseApp`, one row per leaseApp, and the `<entityId>` is the leaseApp's **own bare id**. The bare NanoID is therefore **derivable from `actorKey` alone**: `substrate.ParseVertexKey(actorKey)` returns the bare `<id>` (validated as a NanoID; `internal/substrate/keys.go:94` → `splitVertexKey` requires exactly 3 segments + `IsValidNanoID`). So **the key column resolves to the anchor's id extracted from `actorKey`, NOT from a row field** — which makes the project path and the delete path compute the **identical** key (both have `actorKey`). This keeps `BuildKey`'s signature `BuildKey(actorKey string) string` **unchanged** and both call sites correct.

> **Why not source the column from the row?** Because the delete path has no row (above). Sourcing the bare id from `actorKey` (the anchor's own id) is the shape §10.2 Option (b) actually needs: the convergence lens's candidate **is** its anchor (a leaseApp row reprojects on a linked constituent flip via the BFS enumerator, but the *row* is still one-per-leaseApp keyed on the leaseApp). The "key column" is therefore best modeled as **"emit the anchor's bare id (the `<id>` of `actorKey`) instead of the full `<type>.<id>` suffix"** — an `actorKey`-derived transform, not a row-field lookup. **This is the recommended interpretation (Q1).** If a future lens genuinely needs a *row-field-sourced* key distinct from the anchor id, that is a harder change (the delete path would need the row) and is **out of scope** — flag it, do not build it.

Everything else (the descriptor field, the parse validation, the three spec mirrors, the tests) is scaffolding that lets this one transform land cleanly and opt-in.

---

## 1. The two ACs (verbatim) + adjudication

### The ACs (from `phase-2-epics.md` ~694–699)

> **Given** Refractor's Output descriptor (`internal/refractor/projection/output.go` `BuildKey`)
> **When** an actorAggregate lens declares an explicit **key column**
> **Then** `BuildKey` emits that bare-NanoID `<entityId>` **instead of** the default `{actorSuffix}` (= `<type>.<id>`), so the row key stays `<targetId>.<entityId>` and Weaver's `splitRowKey` accepts it unchanged
> **And** the **frozen §10.2 key + `splitRowKey` are untouched** (Option b); a non-`weaver-targets` actorAggregate lens is unaffected

### Adjudication — what each AC binds

- **AC #1 → §2 (the mechanism).** Add an **optional `keyColumn` field** to the Output descriptor (the lens-spec field, mirrored across the three in-tree representations — §2 Item A). When **present**, `BuildKey` substitutes the **bare-NanoID `<entityId>`** (the anchor's own id, derived from `actorKey` per §0/Q1) into the `{actorSuffix}` slot **instead of** the `<type>.<id>` actor suffix. The resulting `weaver-targets` row key is `<targetId>.<entityId>` (bare NanoID) → Weaver's `splitRowKey` round-trips it unchanged (§4). When **absent**, the existing `{actorSuffix}` default path is **byte-for-byte unchanged**.
- **AC #2 → §3 (frozen safety) + §4 (Weaver unchanged) + §6 (regression).** Option (b): the §10.2 row-key shape `<targetId>.<entityId>` and `splitRowKey` are **UNTOUCHED** — the bare NanoID is simply the `<entityId>` slot (a NanoID has no dots, so `splitRowKey`'s single-dot split is unaffected). **Confirm NO Weaver change** (§4). **Confirm NO frozen-contract edit** (top-of-story flag). A non-`weaver-targets` actorAggregate lens, an actorAggregate lens NOT declaring `keyColumn`, and a non-actorAggregate lens are **all unaffected** — proven by tests (§6) that the default `{actorSuffix}` path still produces `<type>.<id>` and the four built-in lenses + the proof lens are untouched.

### The two Epic-13/14 invariants on this AC (Andrew, 2026-06-18; epics ~579–581 — they apply to Epic 14 too)

- **(a) type-agnostic** — the **engines** stay type-blind; concrete types live in **packages**. For 14.2 this means: **the `keyColumn` mechanism must not special-case `leaseApp` or `service` or any concrete type.** It is a generic descriptor option that works for **any** actorAggregate lens whose anchor is its candidate entity. The 14.2 **test lens uses a non-lease, non-service anchor type** (e.g. the proof lens's pattern, or a fresh throwaway type) so a hardcoded type assumption would break the test. **No `leaseApp`/`service`/`weaver-targets` literal appears in `internal/refractor` engine code** (the mechanism keys off the descriptor field, never a bucket/type name) — a grep proves it (§6). (Note: `weaver-targets` does not currently appear in `internal/refractor` — keep it that way; the mechanism is bucket-agnostic. Contrast `AuthPlaneBucket = "capability-kv"` in `plan.go:32`, which is the *guard* classifier, not a key-shape switch — do **not** add a `weaver-targets` analog.)
- **(b) D5** — **not directly in play** here (this is a key-shape mechanism, not a data-placement decision). State this in the summary so a reviewer does not flag a "missing D5 assertion": 14.2 writes no vertex/aspect; it only changes how a projected document's *key* is rendered. The D5 outcome-in-aspect of the convergence target is 14.1's (the service instance) + 14.4's (the lens reads it); 14.2 is the key-shape enabler in between.

### Scope boundary

**In scope:**
1. **The `keyColumn` Output-descriptor field** (§2 Item A) — added to the **three** in-tree mirrors of the §6.13 descriptor: `internal/pkgmgr/definition.go` `OutputDescriptorSpec` (the package-facing spec 14.4 sets), `internal/refractor/lens/corekv_source.go` `OutputDescriptorSpec` (the JSON spec aspect shape), and `internal/refractor/projection/output.go` `OutputDescriptor` (the compiled descriptor) — plus the spec→aspect map in `internal/pkgmgr/build.go` (~310–314) and the spec→Rule plumbing in `corekv_source.go` (~389) and `projection.ParseOutputDescriptor` (output.go ~103–113).
2. **The `BuildKey` branch** (§2 Item B) — when `keyColumn` is set, emit the anchor's bare id (from `actorKey`) into the `{actorSuffix}` slot; when unset, the existing path unchanged. Make it **opt-in**, default byte-for-byte identical.
3. **Parse-time validation** (§2 Item C) — `ParseOutputDescriptor` validates the `keyColumn` declaration (it must name a column / the descriptor must still satisfy the existing `outputKeyPattern` `{actorSuffix}` rule — confirm the interaction in Q2) and rejects a malformed combination fail-closed (mirror the existing `validateKeyPattern` / `parseEmptyBehavior` rejections).
4. **The bare-NanoID disposition** (§2 Item D / Q3) — validate (or document the trusted expectation) that the emitted `<entityId>` is a bare NanoID with no dots. Since the recommended interpretation derives it from `actorKey` via `ParseVertexKey` (which **already** validates `IsValidNanoID`), the value is **guaranteed** bare — state this is the validation point.
5. **Tests** (§6) — a focused unit test on `BuildKey` (both branches) + a **production-path e2e** modeled on `refractor_package_actoraggregate_proof_e2e_test.go` proving (a) a `keyColumn` lens projects a `<targetId>.<bareNanoID>` key AND Weaver's `splitRowKey` round-trips it, and (b) the default `{actorSuffix}` path is unchanged + the built-ins are unaffected.

**Out of scope (do NOT build — later/other stories):**
- **The `leaseApplicationComplete` lease lens** + its multi-hop MATCH + its `weaver-targets` target → **Story 14.4**. 14.2 builds the **mechanism** and proves it with a **throwaway test lens** (a non-lease anchor type). Do **NOT** ship a `lease-signing` package, a `weaver-targets` lens, or any lease/service content.
- **Any change to Weaver** (`internal/weaver/*`) — AC #2 requires NONE. `splitRowKey` already accepts the bare-NanoID key (§4). Do **NOT** touch `splitRowKey`, `evaluator.go`, `temporal.go`, the watch, or the strategist.
- **Any change to the frozen §10.2 key shape or `splitRowKey`'s contract** — Option (b) is precisely "leave them frozen." No contract edit.
- **A row-field-sourced key distinct from the anchor id** (§0 "Why not source the column from the row") — the delete path has no row; this is a harder change and is not what §10.2 Option (b) needs. Flag it if a reviewer raises it; do not build it.
- **`StaticEmptyColumns` / `Lanes` / `ActorField` semantics changes** — untouched. The `weaver-targets` row's body columns (`violating`, `missing_*`, `applicant`, `freshUntil`, `entityKey`, `projectedAt`) are **14.4's** lens RETURN aliases / `bodyColumns`; 14.2 changes only the **key**, not the body.
- **The `freshUntil` → `@at` temporal wiring** (§10.2 / §10.4) — engine-recognized, but unrelated to the key shape. Not this story.
- **Postgres-target key handling** — the `keyColumn` is a `weaver-targets` (nats_kv) convergence concern; the actorAggregate path is nats_kv (the guard requires it, `driver.go:184-187`). Do not extend `keyColumn` to the Postgres adapter (no caller, and the bare-NanoID single-key shape is NATS-subject-driven).

---

## 2. The mechanism — item-by-item (DS builds to THIS)

The change has one logical move (emit the bare id when a `keyColumn` is declared) threaded through the descriptor's three representations + the parse + the `BuildKey` render. Mirror the **existing** field-handling for `RealnessFilter` / `Freshness` (the closest optional-string descriptor fields) at every layer.

### Item A — the `keyColumn` field across the three descriptor mirrors

The Output descriptor exists in **three** in-tree shapes; add `keyColumn` to each (and the two transforms between them), mirroring how `realnessFilter`/`freshness` already flow end-to-end:

1. **`internal/pkgmgr/definition.go` `OutputDescriptorSpec`** (~297–308) — the **package-facing** spec a Capability Package declares. Add `KeyColumn string \`json:"keyColumn,omitempty"\``. **This is the field 14.4 will set** on the `leaseApplicationComplete` lens. (Mirror `RealnessFilter string \`json:"realnessFilter,omitempty"\``.)
2. **`internal/pkgmgr/build.go`** (~310–314) — the installer maps the package spec → the JSON `spec["output"]` aspect written into Core KV. The current code does `if l.Output != nil { spec["output"] = l.Output }` (~313–314) — it serializes the whole struct, so **adding the json-tagged field is automatically carried** IF the lens-side struct also has it. **Confirm** the round-trip: `pkgmgr.OutputDescriptorSpec` → JSON → `lens.OutputDescriptorSpec`. (If `build.go` builds the map field-by-field for the descriptor, add the field there too — read ~270–320 to confirm whether it's whole-struct or field-by-field. Current read shows whole-struct assignment, so a json tag suffices — verify.)
3. **`internal/refractor/lens/corekv_source.go` `OutputDescriptorSpec`** (~103–126) — the shape `CoreKVSource` unmarshals the `output` aspect into, and `corekv_source.go:389` copies onto `lens.Rule.Output` (which is `*OutputDescriptorSpec`). Add `KeyColumn string \`json:"keyColumn,omitempty"\``. (Mirror `RealnessFilter` at ~108.)
4. **`internal/refractor/projection/output.go` `OutputDescriptor`** (~42–57) — the **compiled** descriptor `ParseOutputDescriptor` (~67–114) produces from `lens.OutputDescriptorSpec`. Add a `KeyColumn string` field and copy it in `ParseOutputDescriptor`'s returned struct (~103–113), after the existing-field validation in Item C.

> **Why all three:** the descriptor is data that travels package-spec → installer → Core KV aspect → CoreKVSource → Rule → compiled descriptor. Miss any layer and 14.4's declared `keyColumn` silently never reaches `BuildKey` (the dev would "implement" the feature and it would do nothing — a completion-lie trap the proof test (§6) catches because it installs through the **real** `InstallPackage` path).

### Item B — the `BuildKey` branch (`output.go:155-163`)

Current:
```go
func (d OutputDescriptor) BuildKey(actorKey string) string {
	suffix := actorKey
	if rest, ok := strings.CutPrefix(actorKey, substrate.VertexPrefix+"."); ok {
		suffix = rest
	}
	return strings.ReplaceAll(d.OutputKeyPattern, ActorSuffixPlaceholder, suffix)
}
```
- **When `d.KeyColumn == ""` (the default):** **leave this exactly as is.** `suffix = <type>.<id>` (vtx-stripped). Byte-for-byte unchanged for the four built-ins + the proof lens.
- **When `d.KeyColumn != ""`:** the substituted value is the **anchor's bare id** (the `<entityId>`). Derive it from `actorKey` via `substrate.ParseVertexKey(actorKey)` → the bare `<id>` (already NanoID-validated by `splitVertexKey`). Substitute **that** into `{actorSuffix}` instead of `<type>.<id>`. Concretely:
  ```go
  func (d OutputDescriptor) BuildKey(actorKey string) string {
      suffix := actorKey
      if rest, ok := strings.CutPrefix(actorKey, substrate.VertexPrefix+"."); ok {
          suffix = rest
      }
      if d.KeyColumn != "" {
          if _, id, ok := substrate.ParseVertexKey(actorKey); ok {
              suffix = id            // bare NanoID <entityId> — Option (b)
          }
          // else: actorKey is not a well-formed vertex key; fall through to the
          // vtx-stripped suffix. The EnvelopeFn (driver.go:48-54) already rejects
          // a non-vertex actorKey before BuildKey is reached on the project path,
          // so this branch only guards the delete path's already-validated key.
      }
      return strings.ReplaceAll(d.OutputKeyPattern, ActorSuffixPlaceholder, suffix)
  }
  ```
- **CRITICAL — both call sites stay correct.** Because the bare id is derived from `actorKey` (not a row), the **project path** (`driver.go:56`, has the row but uses `actorKey`) and the **delete path** (`driver.go:159` → `pipeline/evaluate.go:89,351`, has only `actorKey`) compute the **identical** key. **This is the resolution of the §0 tension — do not source the column from the row.** (See Q1 for the precise semantics + the alternative considered.)
- **`OutputKeyPattern` still contains `{actorSuffix}`.** The pattern grammar is unchanged: the lens still declares `outputKeyPattern: "leaseApplicationComplete.{actorSuffix}"`, and the `{actorSuffix}` placeholder is now filled with the bare id (when `keyColumn` is set) vs the `<type>.<id>` (when not). **Do NOT introduce a new placeholder** (e.g. `{entityId}`) — that would expand the constrained-placeholder set `validateKeyPattern` guards (output.go:128-153) and ripple into every descriptor. The opt-in is the **`keyColumn` field**, not a new placeholder. (Q2 records the alternative — a distinct placeholder — and why it's rejected.)

### Item C — parse-time validation (`ParseOutputDescriptor`, output.go:67-114)

- **Carry the field:** add `KeyColumn: spec.KeyColumn` to the returned `OutputDescriptor` (after the existing validations).
- **Validate the declaration is coherent (fail-closed, mirror the existing rejections):**
  - `keyColumn`, when set, must be a non-empty trimmed string (mirror the `bodyColumns[i]` empty-string check, output.go:83-87). A whitespace-only `keyColumn` is a rejection.
  - **Decide + state (Q2): is `keyColumn` REQUIRED to name a declared `bodyColumns` alias, or is it a marker flag?** Under the recommended `actorKey`-derived interpretation (§0/Item B), the value comes from `actorKey`, **not** a RETURN column — so `keyColumn` functions as a **marker** ("emit the bare anchor id, not the full suffix"). The natural names are `"entityId"` (the §10.2 vocabulary) by convention. **Recommendation: treat `keyColumn` as a marker whose value is documentation (the convention `entityId`), NOT a required `bodyColumns` member** — because the value is anchor-derived, requiring it to be a RETURN alias would be misleading (the lens need not RETURN it). **But** the lens MAY also RETURN it as a body column if the row wants `entityId` echoed (14.4 likely echoes `entityKey`, the full key, per §10.2 line ~103 — distinct from the key's bare id). Validate `keyColumn != ""` only; do **not** require it to be in `bodyColumns`. **State this disposition explicitly** (it is the most likely review flag — an Acceptance Auditor may expect `keyColumn ∈ bodyColumns`; explain why it is anchor-derived).
  - The `outputKeyPattern` `{actorSuffix}`-required rule (validateKeyPattern, output.go:149-151) **still applies** — the pattern must contain `{actorSuffix}` whether or not `keyColumn` is set (the placeholder is the substitution point; `keyColumn` only changes what fills it). Do **not** relax `validateKeyPattern`.
- **Fail-closed posture:** an invalid `keyColumn` returns an error from `ParseOutputDescriptor`, which `InstallActorAggregate` (driver.go:132-137) and `Compile` (plan.go:150-153) already turn into a refused registration (logged, lens not wired). This matches the existing descriptor-error handling — **reuse it, do not invent a new failure path.**

### Item D — the bare-NanoID guarantee (Q3 disposition)

§10.2 requires the `<entityId>` slot be a **bare NanoID** (no dots, else the `<targetId>.<entityId>` dot discipline + `splitRowKey` break). **Disposition (recommended): validate-at-derivation, which is FREE under the recommended interpretation.** Because `BuildKey` derives the value via `substrate.ParseVertexKey(actorKey)` (Item B), and `ParseVertexKey`/`splitVertexKey` (`keys.go:94`) **already require `IsValidNanoID(parts[2])`**, the emitted `<entityId>` is **guaranteed** a valid bare NanoID — there is no path to emit a dotted value from a well-formed `actorKey`. **State this as the validation point** (the guarantee is structural, inherited from Contract #1's vertex-key shape, not a new check). A defensive test (§6) asserts a `keyColumn` lens never emits a key with more than one dot after the targetId. (If you instead chose a row-field-sourced value — which §Out-of-scope forbids — you would owe an explicit `IsValidNanoID` validation on the projected value; record this in Q3 as the contingency.)

### Item E — what 14.2 does NOT change

No new placeholder (Item B), no Weaver change (§4), no `validateKeyPattern` relaxation, no `EnvelopeFn` body-column change, no guard/empty-behavior change, no Postgres-adapter change, no `splitRowKey` change, no frozen-contract edit. The change is: **one optional descriptor field (3 mirrors + 2 transforms) + one `BuildKey` branch + its parse validation + tests.** The smallest thing that makes Option (b) real and opt-in.

---

## 3. Frozen-contract safety (Option b) — the §10.2 key + `splitRowKey` are UNTOUCHED

This is half of AC #2. The proof that Option (b) holds:

- **The §10.2 row-key shape `<targetId>.<entityId>` is unchanged.** 14.2 does not alter the key *shape* — it changes what fills the `<entityId>` slot from `<type>.<id>` (the actorAggregate default, which was the M2 defect) to the **bare NanoID** the contract specifies (§10.2 lines ~98–103: "the entity segment is just the **NanoID**"). 14.2 makes the actorAggregate path **conform** to the frozen shape; it does not redefine it.
- **`splitRowKey` is unchanged** (`internal/weaver/evaluator.go:508-518`). It splits on the first dot and requires `IsValidNanoID(entityID)`. A bare-NanoID `<entityId>` has **no dots**, so the single-dot split yields exactly `(targetId, entityId)` and the NanoID check passes. **No Weaver edit** (§4).
- **NO frozen-contract edit.** §10.2 already carries the Option (b) clause (13.1, lines ~120–131); §6.13 already frames the descriptor. The `keyColumn` field is an **additive** descriptor option consistent with §6.13's "constrained Output descriptor" — it does not change any existing field's meaning. (If a reviewer argues `keyColumn` needs a §6.13 amendment to be a *named* descriptor field, the disposition is: §6.13 ratified the descriptor as the extension point for actor-aggregate projection options, and Option (b) in §10.2 explicitly says "such a lens declares an explicit key column … landing in the Epic-12 Output-descriptor machinery" — i.e. the contract already authorizes this field. **No CAR needed.** Record the reasoning in the summary; if Winston disagrees, a §6.13 clarification CAR is the path — but do not edit the contract in-flight.)
- **Opt-in = regression-safe (§6).** A lens not declaring `keyColumn` is byte-for-byte the old path. The four built-in lenses (`capability`, `capabilityEphemeral`, `myTasks`, `capabilityRoleIndex` — none of which target `weaver-targets`) and the proof lens are **untouched**.

---

## 4. Weaver's `splitRowKey` accepts the bare-NanoID key UNCHANGED (the AC requires NO Weaver change)

AC #2 requires that Weaver's `splitRowKey` round-trips the `<targetId>.<entityId>` key **with no change**. **Confirmed by reading the code — NO Weaver change is needed:**

`internal/weaver/evaluator.go:508-518`:
```go
func splitRowKey(key string) (targetID, entityID string, ok bool) {
	i := strings.IndexByte(key, '.')
	if i <= 0 {
		return "", "", false
	}
	targetID, entityID = key[:i], key[i+1:]
	if !substrate.IsValidNanoID(entityID) {
		return "", "", false
	}
	return targetID, entityID, true
}
```
- It splits on the **first** dot → `targetID = <targetId>`, `entityID = <everything after>`.
- It then requires `IsValidNanoID(entityID)`. A **bare NanoID** (20 chars, canonical alphabet, **no dots**) passes; a `<type>.<id>` value (which contains a dot) makes `entityID = "<type>.<id>"` → **`IsValidNanoID` fails** → `ok=false` → the row is dropped (the M2 defect Option (b) fixes by emitting the bare NanoID).
- **So once 14.2 makes the key `<targetId>.<bareNanoID>`, `splitRowKey` accepts it verbatim.** No edit to `splitRowKey`, `evaluator.go:23`, `temporal.go:195`, or any Weaver consumer.

**The §6 e2e MUST close the loop** by feeding the `keyColumn`-projected key through `splitRowKey` (call it directly in a Weaver-package test, or assert the projected key satisfies `IsValidNanoID(entityIdAfterFirstDot)`) — so the story *proves* the round-trip, not just asserts it. (See §6 test 3.)

---

## 5. Forward fit (note, do NOT build)

14.2 is the engine enabler; 14.4 consumes it (build order 14.1, 14.2, 14.3 → 14.4 → 14.5):

- **14.4 (leaseApp convergence lens + externalTask patterns)** — ships the `leaseApplicationComplete` **actorAggregate** lens: `AnchorType: leaseApp`, `MATCH (app)-[:applicationFor]->(id), (id)<-[:providedTo]-(inst:service)`, target `weaver-targets`, **declaring `keyColumn`** (value `entityId`) so `BuildKey` emits the bare leaseApp id → the row key is `leaseApplicationComplete.<leaseAppId>` → Weaver's `splitRowKey` accepts it → the gap converges. The lens RETURNs the §10.2 body columns (`violating`, `missing_onboarding`/`missing_bgcheck`/`missing_payment`/`missing_signature`, `applicant`, `entityKey`, `projectedAt`, optionally `freshUntil`). **14.2 builds the `keyColumn` mechanism; 14.4 declares it on the real lens.** 14.2's proof lens is a throwaway with a non-lease anchor — it proves the mechanism is type-agnostic.
- **14.5 (e2e + `test-lease-convergence` gate)** — drives the lease application to steady state; the `leaseApplicationComplete` row (keyed via 14.2's `keyColumn`) is what Weaver watches + acts on. 14.2 makes that key well-formed. Green here unblocks 13.5 (retire the nudge).

**The one design choice that matters for 14.4:** the `keyColumn` is **anchor-derived** (§0/Q1) — so 14.4's lens must be anchored on the **candidate entity** (`AnchorType: leaseApp`, one row per leaseApp). The §10.2 MATCH already is (the `app` is the anchor). If 14.4 ever needed a candidate ≠ anchor, that is the out-of-scope row-field-sourced key — flag it then; the recommended mechanism handles the §10.2 case 14.4 actually has.

---

## 6. Tests (the BuildKey branch + the production-path proof + the splitRowKey round-trip + the regression pins) — first-class

Mirror the existing projection tests: `internal/refractor/projection/plan_unit_test.go` (the `ParseOutputDescriptor` accept/reject pins + the `BuildKey` assertions, ~18–60) for the unit layer, and **`internal/refractor/refractor_package_actoraggregate_proof_e2e_test.go`** (the full `InstallPackage` → live `CoreKVSource` → `projection.InstallActorAggregate` → project/reproject path) for the production-path proof — **this is your e2e template.** Build the e2e in `internal/refractor/` (package `refractor_test`).

### Required tests

1. **`TestBuildKey_KeyColumn_EmitsBareNanoID`** (unit, `projection` package — mirror `plan_unit_test.go` ~18–37). A descriptor with `KeyColumn: "entityId"` and `OutputKeyPattern: "leaseApplicationComplete.{actorSuffix}"`: assert `BuildKey("vtx.leaseApp.Lk2Pn6mQrtwzKbcXvP3T") == "leaseApplicationComplete.Lk2Pn6mQrtwzKbcXvP3T"` (bare NanoID, **one dot** after the targetId). Use a real 20-char NanoID literal (copy the alphabet/length from an existing test, e.g. `plan_unit_test.go:34`).
2. **`TestBuildKey_DefaultSuffix_Unchanged`** (unit — THE regression pin for AC #2). The **same** descriptor with `KeyColumn: ""`: assert `BuildKey("vtx.identity.Hj4kPmRtw9nbCxz5vQ2y") == "cap.ephemeral.identity.Hj4kPmRtw9nbCxz5vQ2y"` (the existing `{actorSuffix}` = `<type>.<id>` shape, byte-for-byte the pre-14.2 behavior). This is the existing assertion at `plan_unit_test.go:34` — keep an equivalent so a regression is caught at the unit layer. Also assert that a descriptor parsed **without** `keyColumn` has `KeyColumn == ""` (the field defaults empty).
3. **`TestKeyColumn_SplitRowKeyRoundTrips`** (the §4 loop-closer — AC #1's "splitRowKey accepts it unchanged"). Either: (a) in a `internal/weaver` test, call `splitRowKey(<the 14.2-projected key>)` and assert `ok == true` with `targetID/entityID` split correctly; OR (b) since `splitRowKey` is unexported, assert in the projection/e2e test that the projected key, split on the first dot, has `substrate.IsValidNanoID(tail) == true` (the exact predicate `splitRowKey` applies) — and add a comment citing `evaluator.go:514`. **Prefer (a) if a thin Weaver-package test is cheap** (it tests the actual function); else (b) is a sound equivalent. **Also** add a contrast assertion: the **default** `{actorSuffix}` key (`leaseApplicationComplete.leaseApp.<id>`) **fails** `IsValidNanoID` on its tail — i.e. demonstrate *why* Option (b) is needed (the M2 defect) and that 14.2 fixes it.
4. **`TestParseOutputDescriptor_KeyColumn_AcceptAndReject`** (unit — mirror `plan_unit_test.go:39–60`). Accept: a descriptor with a non-empty `keyColumn` parses and carries `KeyColumn`. Reject: a whitespace-only `keyColumn` is rejected fail-closed (mirror `RejectsBadEmptyBehavior`). Assert the `{actorSuffix}`-required rule still fires when `keyColumn` is set (a pattern without `{actorSuffix}` is still rejected).
5. **`TestRefractor_KeyColumnLens_ProjectsBareNanoIDKey` (THE PRODUCTION-PATH PROOF — AC #1).** Model on `refractor_package_actoraggregate_proof_e2e_test.go` verbatim (the same `InstallPackage` → `CoreKVSource` → `projection.InstallActorAggregate` → fixture → `require.Eventually` shape). Differences:
   - The throwaway lens declares **`keyColumn: "entityId"`** + `outputKeyPattern: "<targetId>.{actorSuffix}"` (use a throwaway `<targetId>` like `"proofConvergence"` and a throwaway disjoint bucket like `"proof-convergence"` — **NOT** `weaver-targets`, **NOT** `leaseApplicationComplete`; the mechanism is bucket/target-agnostic).
   - The **anchor type is a non-lease, non-service type** (reuse the proof lens's `identity` anchor, or a fresh throwaway type) — invariant (a): the mechanism must not special-case `leaseApp`.
   - Assert the projected key is **`<targetId>.<bareNanoID>`** (the anchor's bare id, one dot after the targetId) — **NOT** `<targetId>.<type>.<id>`. Read the key back from the throwaway bucket and assert `substrate.IsValidNanoID(<tail after first dot>)` (the `splitRowKey` predicate, test 3).
   - **Reproject/invalidate**: as in the proof test, flip a constituent so the row reprojects, and (for an `emptyBehavior: delete` lens) confirm the **delete** lands on the **same** `<targetId>.<bareNanoID>` key (proving the §0 project-vs-delete-path consistency — this is the test that catches a row-sourced-key bug). The proof test's close-the-task → key-drops assertion (~277–293) is the model.
6. **`TestRefractor_DefaultActorAggregate_Unchanged` (regression — the built-ins + proof lens).** The existing proof test (`TestRefractor_PackageActorAggregateLens_ProjectsWithZeroCoreEdits`) already exercises a `keyColumn`-absent lens end-to-end and asserts `roster.identity.<id>` (`~252–253`). **It MUST still pass unchanged** — running it is the regression gate (it proves the default path is byte-for-byte intact). Do **not** modify it. If your `keyColumn` addition forces any edit to it, that is a regression — stop and fix the mechanism. (Optionally add a one-line assertion in a unit test that the four built-in capability descriptors, if constructed in a test, have `KeyColumn == ""`.)
7. **`TestKeyColumn_NoTypeLeakInEngine` (invariant a — type-agnostic, gate-asserted).** A repo-grep test (or a documented manual grep in the summary): assert the literals `leaseApp`, `weaver-targets`, `leaseApplicationComplete`, and `service` do **NOT** appear in the 14.2-touched `internal/refractor` engine files (`projection/output.go`, `projection/plan.go`, `projection/driver.go`, `lens/corekv_source.go`) as a key-shape switch. The mechanism keys off the **descriptor field**, never a type/bucket literal. **Prefer a test** so it stays enforced; a narrow grep (the four literals in the four files) avoids false-positives on unrelated mentions. (Note: `capability-kv` legitimately appears in `plan.go:32` as the *guard* classifier — that is the auth-plane fork, not a key-shape switch; your grep targets the four convergence literals, not `capability-kv`.)

### Test posture

The unit tests (`projection` package) are pure (no NATS). The production-path e2e uses **embedded NATS** (`natstest.RunServer`, exactly as the proof test ~96–127) — no Docker. It installs through the **real** `InstallPackage` path + the live `CoreKVSource` watch + the production `projection.InstallActorAggregate` — so the descriptor-field round-trip (Item A) is genuinely proven (a missed mirror layer fails here, not in review). Flake retry per Deviation 14 is allowed; a flake claim without a re-run is a drift signal. `go test ./internal/weaver/...` covers test 3(a) if you put the `splitRowKey` round-trip there.

---

## 7. Required reading (DS does the deep reads; do not expect them pre-loaded)

- **THE FILE YOU CHANGE (the headline):** `internal/refractor/projection/output.go` IN FULL — `OutputDescriptor` (the compiled struct, ~42–57), `ParseOutputDescriptor` (the validation + field-copy, ~67–114), `validateKeyPattern` (the constrained-placeholder rule — do NOT relax it, ~128–153), and **`BuildKey` (~155–163, the branch you add)**. This is the centerpiece.
- **THE TWO CALL SITES (the design tension):** `internal/refractor/projection/driver.go` — `EnvelopeFn` (the project path, `outKey := d.BuildKey(actorKey)` at ~56; note it has the row but keys off `actorKey`) and `InstallActorAggregate` (~124–174, where `p.SetActorDeleteKey(desc.BuildKey)` at ~159 wires the **delete** path). Then `internal/refractor/pipeline/evaluate.go` — the actor-disappearance Delete paths that call `actorDeleteKeyFor(actorKey)` → `BuildKey` with **only the actorKey, no row** (~88–95 the tombstone shortcut, ~346–358 the `reprojectActors` missing-actor path, ~398–406 `actorDeleteKeyFor`). **Read these to internalize why the key-column value must be `actorKey`-derived, not row-sourced (§0).**
- **THE THREE DESCRIPTOR MIRRORS (Item A):** `internal/pkgmgr/definition.go` `OutputDescriptorSpec` (~297–308, the package-facing field 14.4 sets) + `LensSpec.Output`/`ProjectionKind` (~277–284); `internal/pkgmgr/build.go` the spec→aspect map (~270–320, esp. the `spec["output"] = l.Output` at ~313–314 — confirm whole-struct vs field-by-field); `internal/refractor/lens/corekv_source.go` `OutputDescriptorSpec` (~96–126) + the spec→Rule copy (~389) + `LensSpec.ProjectionKind`/`Output` (~85–93). **All three must carry `keyColumn` or 14.4's declaration never reaches `BuildKey`.**
- **THE COMPILE/PLAN PATH (where a descriptor error becomes a refused registration):** `internal/refractor/projection/plan.go` — `Compile` (~134–203, calls `ParseOutputDescriptor` ~150–153), `IsActorAggregate` (~95–97), `IsAuthPlane` (~109–114, the `capability-kv` classifier — note it is NOT a key-shape switch), `RequiresGuard` (~99–107). `internal/refractor/projection/empty.go` (`EmptyAction`/`RequiresGuardedTombstone` — unchanged, but read to confirm `keyColumn` does not interact with the guard). `internal/refractor/projection/driver.go` `EnvelopeFn` empty-result delete (~72–83) uses `BuildKey` for the delete key — confirm it is consistent with your branch.
- **THE PROOF-TEST TEMPLATE:** `internal/refractor/refractor_package_actoraggregate_proof_e2e_test.go` IN FULL — the exact `InstallPackage` → `CoreKVSource` → `projection.InstallActorAggregate` → fixture → project/reproject harness your §6 test 5 mirrors (the embedded-NATS setup ~96–127, the install ~154–159, the live activation ~161–209, the project assertion ~255–272, the reproject/delete assertion ~274–293). And `internal/refractor/projection/plan_unit_test.go` (~18–60) for the unit accept/reject/`BuildKey` pins your §6 tests 1/2/4 mirror.
- **WEAVER'S `splitRowKey` (the round-trip you must NOT change):** `internal/weaver/evaluator.go` (~505–518 `splitRowKey`; ~23 + `temporal.go:195` the call sites). Confirm a bare-NanoID `<entityId>` passes and a `<type>.<id>` fails (the M2 defect). **No Weaver edit.**
- **FROZEN — Contract #10 §10.2** (`docs/contracts/10-orchestration-surfaces.md` ~84–167) — the row-key shape + the Option (b) amendment (~120–131). **FROZEN — Contract #1 §1.1** (`docs/contracts/01-key-shapes.md`) — the vertex-key/NanoID shape the bare `<entityId>` inhabits. Read; build TO; do NOT edit.
- **THE RATIFIED DESIGN:** `_bmad-output/planning-artifacts/sprint-change-proposal-2026-06-18.md` — the M2 decision (lines 33, 38, 88, 148) naming the exact source lines + Option (b). **§6.13** (`docs/contracts/06-capability-kv.md`) + `cmd/refractor/CONTRACT-AMENDMENT-REQUEST.md` Request 5 — the Output-descriptor ratification (the home for `keyColumn`). The Epic 14 framing + build order in `phase-2-epics.md` (~662–701).
- **SUBSTRATE KEY HELPERS:** `internal/substrate/keys.go` `ParseVertexKey`/`splitVertexKey` (~92–109 — the bare-id extraction + the `IsValidNanoID` guarantee) + `VertexPrefix` (~13); `internal/substrate/nanoid.go` `IsValidNanoID`/`NanoIDLength` (~15, ~90). These ground Item B + Item D.

---

## 8. Verification gates (run before handing back; record each + result in the closing summary)

- `go build ./...` — includes `internal/refractor`, `internal/pkgmgr`, `internal/weaver` (confirms the descriptor-field addition compiles across all three mirrors + the transforms).
- `make vet`
- `golangci-lint run ./...`
- `make verify-kernel` — **no kernel-topology change** is made (this is an engine descriptor field), but run it to prove no regression (the stack must come up; requires `make up`).
- **`go test ./internal/refractor/... -count=1`** — the unit `BuildKey`/`ParseOutputDescriptor` pins (§6 tests 1/2/4), the production-path proof (§6 test 5), the **untouched** existing proof test (§6 test 6 — the regression gate), and the type-agnostic grep (§6 test 7). **This is the story's centerpiece proof.**
- **`go test ./internal/weaver/... -count=1`** — the `splitRowKey` round-trip (§6 test 3, if placed here) + confirms NO Weaver regression (you added no Weaver code, but the bare-NanoID key the convergence path now receives must round-trip; if test 3 lives in the weaver package it runs here).
- **`go test ./internal/pkgmgr/... -count=1`** — the install seam still passes (the `keyColumn` field is additive to `OutputDescriptorSpec`; a package declaring it must install + round-trip the aspect).
- The full **3-layer adversarial review** is Winston's gate (Blind Hunter / Edge Case Hunter / Acceptance Auditor) per `bmad-code-review` — a guarded-engine (`internal/refractor`) key-shape change earns the full 3-layer (the change-proposal calls this the "gating decision"). The Acceptance Auditor checks the 2 ACs + §10.2 Option (b) + the "no Weaver change / no frozen edit" claims; the Edge Case Hunter probes the **project-vs-delete-path key consistency** (§0), the bare-NanoID guarantee (Item D), the malformed-`actorKey` fall-through, and the three-mirror round-trip; Blind Hunter on the diff. **Note it in your summary.**

**Gates this story does NOT need to run** (and why): `make test-bypass` (Gate 2) + `make test-capability-adversarial` (Gate 3) gate the **Core-KV write-isolation / convergence security plane**. 14.2 adds **no component, no Core-KV writer, no engine wiring** — it changes how an actorAggregate document's *key string* is rendered (a pure function of the descriptor + actorKey). There is no new bypass surface and no convergence-security change (the projection still writes through the same guarded nats_kv adapter; only the key string differs). They remain Winston's epic-level pre-commit gate, but 14.2's diff does not change what they assert. **If you are unsure whether the key-shape change touches convergence security** (it should not — a bare-NanoID key is *more* correct, not less guarded), say so explicitly in the summary so it can be overridden. Flake retry per Deviation 14 is allowed.

---

## 9. If too large / a split

This story is **small** (one optional descriptor field across 3 mirrors + 2 transforms + one `BuildKey` branch + parse validation + tests). It should land in one pass. **Do not split.** The natural (but unnecessary) seam, if the descriptor plumbing proves fiddly, would be 14.2a = the field + plumbing + the unit `BuildKey`/parse tests, 14.2b = the production-path e2e + the `splitRowKey` round-trip — but the e2e is the proof the plumbing actually works end-to-end (Item A's completion-lie trap), so do not land 14.2a without the e2e. Prefer the single pass.

---

## 10. Open Questions (assumptions made autonomously — Winston to confirm; none blocks dev)

These are the decisions taken while drafting (the create-story ran autonomously). Each carries a **recommendation**; the dev proceeds on the recommendation unless Winston overrides.

- **Q1 — The key-column value is ANCHOR-derived (from `actorKey`), not row-sourced.** RECOMMENDED + assumed throughout (§0, §2 Item B). The §10.2 convergence case has the candidate entity == the anchor (`AnchorType: leaseApp`, one row per leaseApp), so the bare `<entityId>` is the anchor's own id, extractable from `actorKey` via `ParseVertexKey`. This is the **only** interpretation that keeps the **delete path** (which has no row) consistent with the project path. **Confirm:** is there any near-term actorAggregate `weaver-targets` lens whose candidate ≠ anchor (which would need a row-sourced key + a delete-path row fetch — a materially larger change)? 14.4's `leaseApplicationComplete` is anchor==candidate, so the answer is expected "no." If "yes," this story's scope must widen (or that lens must be re-anchored). **Default: anchor-derived.**
- **Q2 — `keyColumn` is a MARKER (emit the bare anchor id), NOT a required `bodyColumns` member; and it reuses the existing `{actorSuffix}` placeholder (no new placeholder).** RECOMMENDED + assumed (§2 Item B/C). The value is anchor-derived (Q1), so requiring `keyColumn ∈ bodyColumns` would be misleading (the lens need not RETURN it). And introducing a distinct placeholder (e.g. `{entityId}`) would expand the constrained-placeholder set `validateKeyPattern` guards and ripple into every descriptor — rejected. **Confirm:** (a) `keyColumn` as a marker (value documents the `entityId` convention) vs requiring it name a RETURN alias; (b) reuse `{actorSuffix}` vs a new `{entityId}` placeholder. **Default: marker + reuse `{actorSuffix}`.** (An Acceptance Auditor may expect `keyColumn ∈ bodyColumns` — the disposition + rationale are stated in §2 Item C so the reviewer can adjudicate.)
- **Q3 — The bare-NanoID guarantee is STRUCTURAL (inherited from `ParseVertexKey`'s `IsValidNanoID`), not a new validation.** RECOMMENDED + assumed (§2 Item D). Because `BuildKey` derives the value from `actorKey` via `ParseVertexKey` (which already requires a valid NanoID), the emitted `<entityId>` cannot contain a dot — the guarantee is free. A defensive test asserts it (§6 test 3/5). **Confirm:** acceptable to rely on the structural guarantee (vs an explicit `IsValidNanoID` check on the emitted suffix)? If Q1 ever flips to row-sourced, an explicit validation becomes mandatory (recorded as the Q3 contingency). **Default: structural guarantee + defensive test.**
- **Q4 — No §6.13 CAR; the `keyColumn` field is authorized by the existing §10.2 Option (b) + §6.13 framing.** RECOMMENDED + assumed (§3 top-of-story flag). §10.2 says the lens "declares an explicit key column … landing in the Epic-12 Output-descriptor machinery," and §6.13 ratified the descriptor as the actor-aggregate extension point. So adding the field is implementing the ratified contract, not amending it. **Confirm:** Winston agrees no `cmd/refractor/CONTRACT-AMENDMENT-REQUEST.md` (§6.13 clarification) is warranted. If Winston wants the descriptor field *named* in §6.13, that is a clarification CAR (not an in-flight edit) — but the code can land against the already-amended §10.2. **Default: no CAR.**
- **Q5 — The throwaway proof lens uses a non-lease, non-service anchor + a throwaway `<targetId>`/bucket (NOT `weaver-targets`).** RECOMMENDED + assumed (§6 test 5). This keeps the proof type-agnostic (invariant a) and avoids coupling the engine test to the lease vertical. **Confirm:** acceptable to prove the mechanism on a throwaway (`proofConvergence` / `proof-convergence` bucket, `identity` or a fresh anchor) rather than a `weaver-targets`/`leaseApplicationComplete` fixture (which would drag in 14.4 content). **Default: throwaway.**

---

## Dev Agent Record

### Completion Notes

Implemented §10.2 Option (b) as a single opt-in transform threaded through the Epic-12
Output-descriptor machinery. The mechanism is exactly as the story specified — no re-litigation
of Q1–Q5; built to the binding recommendations.

**The mechanism as built:**
- `keyColumn` added to the three in-tree descriptor mirrors: `pkgmgr.OutputDescriptorSpec`
  (package-facing, the field 14.4 will set), `lens.OutputDescriptorSpec` (the JSON spec-aspect shape),
  and the compiled `projection.OutputDescriptor` (+ field-copy in `ParseOutputDescriptor`). The two
  whole-struct transforms (`build.go` `lensSpecBody` `spec["output"] = l.Output`; `corekv_source.go`
  `translateSpec` `Output: spec.Output`) carry the new json-tagged field automatically — confirmed by
  reading, and proven end-to-end by the production-path e2e (the round-trip assert
  `convRule.Output.KeyColumn == "entityId"` after a real `InstallPackage`).
- `BuildKey` branch (`output.go`): `KeyColumn == ""` → existing `{actorSuffix}` = `<type>.<id>` path
  **byte-for-byte unchanged**; `KeyColumn != ""` → substitutes the anchor's bare-NanoID `<entityId>`
  derived via `substrate.ParseVertexKey(actorKey)` into the `{actorSuffix}` slot → row key
  `<targetId>.<bareNanoID>`. Signature unchanged (`BuildKey(actorKey string) string`); both call sites
  pass `actorKey`.
- Parse validation (`ParseOutputDescriptor`): `keyColumn` is an opt-in **marker**; accepts absent,
  accepts a non-blank value, rejects whitespace-only fail-closed. NOT required to name a `bodyColumns`
  member (the value is anchor-derived, not a RETURN alias — Q2). `validateKeyPattern`'s
  `{actorSuffix}`-required rule is unchanged and still fires when `keyColumn` is set.

**Q1 delete-path proof (the load-bearing correctness claim):** the key value is `actorKey`-derived,
NOT row-sourced, so `BuildKey` computes the identical key on both wired call sites — the project path
(`driver.go:56`, has the row) and the actor-disappearance delete path (`driver.go:159` →
`SetActorDeleteKey` → `pipeline/evaluate.go:89,351`, has only `actorKey`). The production e2e asserts
the bare-NanoID key on **both** the project path AND the delete path (close-the-task →
emptyBehavior:delete retracts the row at the SAME `proofConvergence.<bareNanoID>` key). A row-sourced
key would diverge on the delete path; the test would catch it.

**Frozen surfaces UNTOUCHED:** no edit to `splitRowKey` (`internal/weaver/evaluator.go:508-518`), the
§10.2 row-key shape, or any Weaver code. The bare NanoID simply fills the `<entityId>` slot §10.2
already specifies. The default path is byte-for-byte unchanged (unit regression pin
`TestBuildKey_DefaultSuffix_Unchanged` + the untouched existing proof test). No frozen-contract edit;
no CONTRACT-AMENDMENT-REQUEST (Q4 — authorized by the already-amended §10.2 Option (b) + §6.13).

**Q3 bare-NanoID guarantee:** structural — `ParseVertexKey`/`splitVertexKey` already enforce
`IsValidNanoID`, so a well-formed `actorKey` yields a dot-free `<entityId>`. Asserted defensively
(unit + e2e: tail after first dot satisfies `IsValidNanoID`; exactly one dot).

**Q5 type-agnostic proof:** the e2e uses a throwaway non-lease anchor (`identity`) + throwaway target
prefix (`proofConvergence`) + disjoint bucket (`proof-convergence`) — NOT `weaver-targets`,
`leaseApplicationComplete`, `leaseApp`, or `service`. The grep test
(`TestKeyColumn_NoTypeLeakInEngine`) pins that none of those four convergence literals appear in the
four touched engine files (`projection/{output,plan,driver}.go`, `lens/corekv_source.go`).

**D5 note (invariant b):** not in play — 14.2 writes no vertex/aspect; it only changes how a projected
document's *key string* is rendered. The convergence target's outcome-in-aspect is 14.1's (service
instance) + 14.4's (the lens reads it).

**DEVIATION (flagged):** a **fourth** `OutputDescriptorSpec` mirror exists at
`internal/bootstrap/lenses.go:26` — the **primordial-lens seeding** path. The story named exactly
three mirrors and this one is correctly **out of scope**: it serves only the four kernel-seeded
built-in lenses (none use `keyColumn`), and 14.4's lease lens is package-delivered through
`pkgmgr.OutputDescriptorSpec`, not bootstrap-seeded. Adding `keyColumn` there would be dead code with
no caller. Left untouched deliberately. If a future *primordial* convergence lens ever needs it, that
mirror gains the field then.

**Gates not run (and why):** Gate 2 (`make test-bypass`) + Gate 3 (`make test-capability-adversarial`)
are not changed by this key-shape mechanism — 14.2 adds no component, no Core-KV writer, no engine
wiring; the projection still writes through the same guarded nats_kv adapter and only the key string
differs (a bare-NanoID key is *more* correct, not less guarded). No new bypass surface, no convergence-
security change. They remain Winston's epic-level pre-commit gate.

### File List

Modified:
- `internal/pkgmgr/definition.go` — `KeyColumn` added to `OutputDescriptorSpec` (package-facing mirror).
- `internal/refractor/lens/corekv_source.go` — `KeyColumn` added to `OutputDescriptorSpec` (spec-aspect mirror).
- `internal/refractor/projection/output.go` — `KeyColumn` on compiled `OutputDescriptor`; field-copy + blank-value validation in `ParseOutputDescriptor`; the `BuildKey` Option-(b) branch.
- `internal/refractor/projection/plan_unit_test.go` — unit tests 1/2/3(predicate)/4 + `substrate` import.

New:
- `internal/weaver/splitrowkey_internal_test.go` — test 3(a): the real `splitRowKey` accepts the bare-NanoID key, rejects the default `<type>.<id>` key (the M2 defect).
- `internal/refractor/refractor_keycolumn_convergence_e2e_test.go` — test 5: production-path proof (real `InstallPackage` → `CoreKVSource` → `projection.InstallActorAggregate`), bare-NanoID key on project AND delete (Q1).
- `internal/refractor/refractor_keycolumn_typeagnostic_test.go` — test 7: type-leak grep over the four touched engine files.

### Change Log

- 2026-06-18 — Story 14.2 implemented: §10.2 Option (b) `keyColumn` opt-in descriptor field + `BuildKey`
  anchor-derived bare-NanoID branch + parse validation + 7 tests. All gates green (`go build`,
  `make vet`, `golangci-lint`, `make verify-kernel`, `go test refractor/weaver/pkgmgr`). `splitRowKey` /
  §10.2 / Weaver untouched; default path byte-for-byte unchanged. Status → review.
