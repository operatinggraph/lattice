# Story 13.2 — Loom `externalTask` step kind

**Status:** review
**Epic:** 13 — External I/O Bridge (orchestration core)
**Tier:** Opus — guarded engine (`internal/loom`), security-plane-adjacent; net-new step kind + a 3rd correlation key + two-op dispatch. Review: full 3-layer adversarial + `make verify-kernel`.
**Epic spec:** `_bmad-output/planning-artifacts/epics/phase-2-epics.md` → "Story 13.2: Loom `externalTask` step kind" (lines ~588–603). Read it for the user-story framing and the four ACs.
**Binding grounding (FROZEN — read, do not redefine):** `docs/contracts/10-orchestration-surfaces.md` §10.5 (the `externalTask` step shape, lines ~470–497) + §10.6 (the `externalTask` correlation row line ~544, the 3rd `payload.externalRef` key lines ~546–555, the deadline+probe lines ~557–591, crash-safety invariants lines ~674–697). Contract #1 §1.1 (key shapes). D3 (Loom mechanics), D5 (data placement).
**Design of record (RATIFIED):** `_bmad-output/planning-artifacts/sprint-change-proposal-2026-06-18.md` — decisions B1 (the 3rd correlation key), M4 (FR58 determinism, a 13.4 concern), m7 (the event rides the instanceOp outbox, not Loom), task E.2 (this story). Component docs: `docs/components/loom.md` → "External steps (`externalTask`)" (lines ~186–214); `docs/components/bridge.md` (the `external.<adapter>` envelope + end-to-end flow — the downstream consumer this story emits for).
**Depends on:** 13.1 (DONE — the surface is frozen). Parallels 13.3 (bridge service actor). The real `instanceOp`/`replyOp`/`external.<adapter>` DDLs land in **14.4**; in 13.2 they are **test fixtures** (§ Scope).
**Workflow:** you are the DS (dev) sub-agent. Repo root, no worktree. Do **NOT** commit/push/branch. Do **NOT** edit frozen contracts (`docs/contracts/*`) or planning artifacts (`epics/*.md`, `lattice-architecture.md`). You MAY edit `/docs/components/*` (loom.md only, if code drifts from the page). A genuine frozen-contract gap → `cmd/loom/CONTRACT-AMENDMENT-REQUEST.md` + flag at the TOP of your closing summary; do **not** edit the contract. Leave all changes in the working tree for Winston.

---

## 0. THE instanceKey CRUX — RESOLVED, NO AMENDMENT (read this first; it governs everything below)

> **TOP-OF-STORY FLAG:** No CONTRACT-AMENDMENT-REQUEST is required. The frozen contract is satisfiable literally. **The one cross-story seam to confirm is recorded in Open Questions Q1** (it does not block this story's build — the engine side is fully determined here). The resolved mechanism below is BINDING for the dev.

### 0.1 The tension (three frozen facts that look contradictory)

1. **§10.6 (table row line ~544; correlation prose line ~552):** describes `instanceKey` / `externalRef` as **"the full `vtx.<type>.<id>` key Loom minted (any package-chosen claim-vertex type)"**, parked on as `token.<instanceKey>`, echoed back as `payload.externalRef`.
2. **§10.5 (frozen step shape, line ~475):** the externalTask step is **exactly** `{ kind, adapter, params, replyOp, instanceOp }` — there is **NO claim-type field**. The engine is never told what type the claim vertex is.
3. **The engine cannot discover the claim type.** Op meta-vertices expose **only** `data.operationType` (`internal/loom/source.go` `opMetaProbe`/`indexOpMeta`/`opMetaKey`, lines ~262–308) — there is no created-vertex-type field Loom can read. Invariant (a) (Andrew, 2026-06-18) also forbids hardcoding a claim-vertex type, and 13.2 proves type-agnosticism against a **non-`service` fixture type**.

If Loom had to mint the *full* `vtx.<type>.<id>` key, it would need the type — which it cannot have without either (b) a 6th step field (a frozen change) or (c) reading a vertex-type off op-meta (which doesn't exist). Either route is forbidden. So a naive reading of fact #1 is impossible.

### 0.2 The reconciliation (what the contract ACTUALLY says, read precisely)

§10.5 line ~486–488 is decisive and removes the contradiction:

> "The engine **then PARKS** on `token.<instanceKey>` (§10.6) — the instance key it **mints write-ahead** and **passes to `instanceOp` as a caller-supplied id** (**exactly as it supplies `CreateTask`'s deterministic `taskId`** write-ahead, §10.6 invariant 1)."

`CreateTask`'s `taskId` is a **bare NanoID**: Loom mints the bare id, passes it in the `payload.taskId` field, and the `task` DDL prepends `vtx.task.` to form the key. The userTask token Loom parks on is `vtx.task.<taskId>` — but the *minted, caller-supplied seam* is the bare `taskId`. The contract says `instanceKey` works **exactly** the same way.

**Therefore (BINDING resolution):**

- **What Loom mints:** a **bare deterministic NanoID** — call it the *instance handle* — minted from `(instanceId, cursor)` via a **new namespace** in `deriveID` (e.g. `deriveInstanceID`), so a crash-retry re-mints the **same** handle and the re-submitted `instanceOp` collapses on the Contract #4 `vtx.op.<opRequestId>` tracker (crash-safety invariant 1; exactly the `deriveTaskID` pattern). The handle is **type-free** — Loom never knows or forms the claim-vertex type.
- **What Loom parks on:** `token.<handle>` (the bare NanoID handle). This is the §10.6 `token.<instanceKey>` pointer; the pending token persisted on the instance is the bare handle.
- **How it reaches `instanceOp`:** as a **caller-supplied id field in the `instanceOp` payload** (the verbatim-id seam, exactly like `CreateTask`'s `taskId`). The fixture `instanceOp` DDL reads that id and **prepends its own package-chosen type** to form `vtx.<fixtureType>.<handle>` — the claim-vertex key. **The type lives entirely in package DDL; the engine supplies only the bare id.** Use a neutral payload field name for this id (see § 1, item 2 — `instanceKey` is the natural name and matches the bridge envelope field; confirm in Q1).
- **How `externalRef` correlates back:** the bridge (13.4) echoes the handle back as `payload.externalRef`; Loom's `correlationKeys` GETs `token.<payload.externalRef>` → resolves the live pointer → advances. **`externalRef` carries the bare handle**, the same value Loom parked on.

### 0.3 Plain statement (required by the brief): bare handle, NOT the full `vtx.<type>.<id>`

**The token Loom controls and parks on, and the value of `payload.externalRef`, is the BARE NanoID handle — not the full `vtx.<type>.<id>` claim-vertex key.** The claim-vertex *key* is `vtx.<type>.<handle>`, formed by the `instanceOp` DDL (package data). Reconciliation with the §10.6 "full `vtx.<type>.<id>`" wording: that wording describes the *claim vertex's key*; the *correlation token* §10.6 also calls `externalRef` is the part Loom can mint and control without knowing the type — the bare handle. This is the ONLY reading consistent with all three frozen facts (0.1) **and** the explicit "caller-supplied id, exactly as `taskId`" instruction (0.2). It needs **no amendment**.

> **Disjointness check (do this — it is an AC):** a bare NanoID handle lives over the canonical Lattice alphabet (no dot — `internal/loom/token.go` `deriveID`), so it can **never** carry the `vtx.task.` prefix `isUserTaskToken` keys on (engine.go line ~961). The externalTask token is therefore disjoint from both the userTask token-namespace (`vtx.task.*`) AND the systemOp namespace — and on a deadline it routes to the **systemOp-style probe** (tracker GET on the instanceOp's op tracker), **not** the userTask creation-probe. See § 1 item 4.

### 0.4 Why the bridge-side `externalRef = "<instanceKey>"` doc text is not a contradiction

`docs/components/bridge.md` shows `externalRef: "<instanceKey>"` and the envelope comment `instanceKey: "vtx.<type>.<id>"`. That doc is the **13.4 bridge** target, not a frozen contract, and it post-dates the type-agnostic refinement (Appendix, 2026-06-18). The bridge treats `externalRef`/`instanceKey` as an **opaque token** (bridge.md "vertex-type-agnostic … never parses the type segment"); whether the opaque token is a bare NanoID or a dotted key is immaterial to the bridge's correctness (deterministic result-op `requestId` + adapter `idempotencyKey`). **13.2 emits a bare handle as `instanceKey`/`externalRef`; 13.4 must consume it as the opaque token it is.** This is the only cross-story seam — Q1 records it so the 13.4 author keys off the same value. It does not change anything the engine does in 13.2.

---

## 1. ADJUDICATION — what 13.2 delivers (DS builds to THIS)

### Scope boundary

**In scope (engine + validation parity + a fixture-driven e2e proof):**
1. A third step kind `externalTask` in `internal/loom/pattern.go` — the frozen shape `{ kind, adapter, params, replyOp, instanceOp }` — taught to `Step` + `validate()`.
2. A `submitExternalTask` branch in `submitStep` (engine.go) that builds the `instanceOp` outbox record (caller-supplied id = the minted bare handle + `adapter`/`params`/`replyOp`), write-aheads `token.<handle>`, and arms the **bounded per-step deadline** — reusing the existing `transition`/outbox/deadline spine.
3. `payload.externalRef` as the **third** correlation key in `correlationKeys`; `eventBody` gains `payload.externalRef`.
4. The deadline backstop: an externalTask token routes to the **systemOp-style** probe in `onDeadline` (tracker present → advance+alert; outbox present → re-arm; neither → `FailPattern` + alert), **NOT** the userTask creation-probe.
5. Validation parity in the SECOND site: `internal/pkgmgr/orchestrationguard.go` `validateLoomPatterns()` — teach it `externalTask` + its required fields.
6. An engine e2e test proving **park → external event emitted → replyOp → advance** end-to-end with a **non-`service` fixture claim type** (e.g. `vtx.widget.<id>`), invariant (a) proven not asserted; plus the FR29 deadline-backstop cases; plus a D5 gate-assertion (invariant b).

**Out of scope (do NOT build — later stories):**
- The **real** `instanceOp`/`replyOp` DDLs + the `external.<adapter>` event-type DDL → **Story 14.4** (`lease-signing` / service domain). In 13.2 these are **TEST FIXTURES** only.
- The **bridge** component (`internal/bridge/`, the adapter registry, the FR58 crash/retry proof) → **13.4**. 13.2 does not build or import a bridge; the fixture stands in for it.
- The **bridge service actor** (`identity.system.bridge`, verify-kernel count) → **13.3**.
- Any Processor/bootstrap change. `external` is an ordinary domain under the open `<domain>.<eventName>` model (sprint-change-proposal §4B item 1, DROPPED; m7) — **no Processor allowlist edit, no new primordial bucket/stream, no verify-kernel count change in 13.2.**

### Item-by-item

**Item 1 — `externalTask` step kind + the frozen shape (`pattern.go`).**
- Add `StepKindExternalTask = "externalTask"` to the kind constants (engine.go line ~13 in pattern.go).
- Extend `Step` (pattern.go lines ~20–24) with the externalTask fields. The frozen shape is `{ kind, adapter, params, replyOp, instanceOp }` — `kind`/`operation`/`guard` already exist; `operation` is **unused** by externalTask (its op vocabulary is `instanceOp`/`replyOp`). Add: `Adapter string`, `Params json.RawMessage` (free-form templates, opaque to the engine — pass through), `ReplyOp string`, `InstanceOp string`. Use `json` tags `adapter`/`params`/`replyOp`/`instanceOp` matching the contract exactly.
- `validate()` (pattern.go lines ~121–143): accept `externalTask` as a third valid kind; **require `adapter`, `instanceOp`, `replyOp` non-empty; `params` optional**. Reject malformed wholesale (the same doctrine as an unknown `kind` — "a half-understood pattern never partially executes"). A **guard on an externalTask step is permitted** (same `parseGuard` path — the existing guard-parse loop already runs for any kind with a non-empty `Guard`; keep it). For a `systemOp`/`userTask` step, `operation` stays required; for `externalTask`, `operation` is NOT required (do not require it). Do not require the externalTask fields on the other two kinds.

**Item 2 — Two-op-shaped dispatch (`submitExternalTask` in engine.go).**
`submitStep` (engine.go lines ~816–822) currently dispatches userTask vs systemOp. Add an `externalTask` branch routing to a new `submitExternalTask`. It mirrors `submitSystemOp` (the bounded-deadline path) with these differences:
- **Mint the bare handle:** `handle := deriveInstanceID(inst.InstanceID, inst.Cursor)` (new derivation, § item below). `token := handle`; `inst.PendingToken = token`.
- **The instanceOp's own op requestId:** `opRequestID := deriveRequestID(inst.InstanceID, inst.Cursor)` — the submission idempotency handle, disjoint from the handle by namespace (exactly as `submitUserTask` keeps the CreateTask requestId disjoint from the taskId).
- **Build the instanceOp outbox payload** carrying everything the fixture instanceOp DDL needs to (a) mint the claim vertex with the caller-supplied id and (b) emit `external.<adapter>` via its own transactional outbox:
  ```
  payload := map[string]any{
      "instanceKey": handle,        // caller-supplied id; the DDL prepends its package-chosen type → vtx.<type>.<handle>
      "subjectKey":  inst.SubjectKey,
      "adapter":     step.Adapter,
      "params":      <step.Params, passed through verbatim>,   // see note
      "replyOp":     step.ReplyOp,
  }
  ```
  - The payload field name for the handle is `instanceKey` (matches the bridge envelope's `instanceKey` field; Q1 confirms the seam). Document in a code comment that this value is the **bare handle**, type-free, and the DDL forms the key.
  - `params` is `step.Params` (a `json.RawMessage`); the engine passes it through opaquely — it does NOT resolve templates (template resolution against row/subject is a §10.8 Weaver/playbook concern; for a Loom step the params are already concrete in the pattern fixture). If `step.Params` is empty, omit it or pass an empty object; pick the cleaner option and note it. Do not invent a templating engine in Loom.
- **Target / authContext:** mirror `submitSystemOp` — `target := "vtx.meta." + pattern.PatternID` (the per-pattern auth anchor, §10.5). The instanceOp is a normal op the relay submits; its authContext.target is the pattern meta-vertex, same as a systemOp step.
- **Build the outbox record** via `buildOutbox(opRequestID, step.InstanceOp, payload, target, e.cfg.Lane, e.cfg.ActorKey)` — `step.InstanceOp` is the operationType submitted.
- **Write-ahead via the existing spine:** `e.state.transition(ctx, inst, token, oldToken, ob, e.cfg.StepTimeout)`. This arms the **bounded per-step deadline** (`StepTimeout`, same as systemOp — an externalTask park is NOT an unbounded human wait; the bridge is a machine completer, so a never-arriving reply must trip the backstop). Do **not** use `CreateTaskTimeout` (that is the userTask-creation bound).
- Log a `loom externalTask write-ahead` line (instanceId, cursor, adapter, instanceOp, replyOp, the handle).
- **Loom stays substrate-only:** the `external.<adapter>` event is emitted by the **instanceOp DDL's** transactional outbox (m7) — Loom never holds a NATS handle and never publishes the external event. The relay just submits the instanceOp like any other op.

**Item — `deriveInstanceID` (token.go).** Add a third deterministic derivation alongside `deriveRequestID`/`deriveTaskID`, with a **distinct namespace** (e.g. `deriveID("instance:", instanceID, cursor)`). Comment it like the others: it is the externalTask write-ahead handle (the bare instance handle Loom parks on AND the caller-supplied id passed to `instanceOp`), crash-collapsing so a re-submitted instanceOp dedups on the Contract #4 tracker; it is a bare NanoID (dot-free) so it is namespace-disjoint from the `vtx.task.` userTask token and the systemOp requestId. Confirm the namespace prefix differs from `""` (requestId) and `"task:"` (taskId) so the same `(instanceId, cursor)` yields three distinct ids.

**Item 3 — Correlation (`eventBody` + `correlationKeys` in engine.go).**
- `eventBody` (lines ~653–658): add `ExternalRef string` under the `Payload` struct (`json:"externalRef"`). The struct then carries top-level `requestId` (systemOp), `payload.taskKey` (userTask), `payload.externalRef` (externalTask).
- `correlationKeys` (lines ~703–712): append `ev.Payload.ExternalRef` as the **third** key, de-duplicated against the prior two (mirror the `!= ev.RequestID` guard; also guard `!= ev.Payload.TaskKey`). Order: requestId, taskKey, externalRef. **At most one live pointer resolves** (tokens are unique handles) — the existing loop in `handleCompletion` already tries each key and stops at the first live pointer; no change needed there beyond the extra key. Update the `correlationKeys` doc comment to describe all three keys and the single-live-pointer invariant (do NOT write a history/changelog comment — describe the present behavior).
- **Idempotency preserved:** the `token.<handle>` pointer's *presence* is the guard (handleCompletion → resolveToken → advance; a redelivered reply for an already-advanced instance finds no live pointer → drop/ack). No change to that mechanism — it is token-shape-agnostic.

**Item 4 — Deadline / failure backstop (FR29; `onDeadline` in engine.go lines ~998–1053).**
The externalTask token is a bare handle, so `isUserTaskToken(token)` is **false** → `onDeadline` falls through to the **systemOp branch** (the `trackerExists` / `outboxExists` / fail ladder, lines ~1027–1052) **with no code change required there** — but you MUST prove this by test and confirm the routing:
- **tracker present** (`vtx.op.<handle's-opRequestId>` exists) → the instanceOp committed; its `external`/completion event was missed (mis-declared completionDomains / lost reply) → **advance + alert** (the existing "completion recovered via deadline probe" path).
  - ⚠️ **Subtle correctness point — VERIFY:** the systemOp deadline branch probes the tracker keyed by **`token`** (the pending token). For systemOp, the pending token IS the op requestId, so `trackerExists(token)` probes the right tracker. For externalTask, the pending token is the **handle**, but the instanceOp's tracker is keyed by **`opRequestID = deriveRequestID(...)`**, NOT the handle. So `trackerExists(handle)` would probe the WRONG key (a tracker that never exists) and `outboxExists(handle)` would probe the wrong outbox key (the relay wrote `outbox.<opRequestID>`, not `outbox.<handle>`). **This means externalTask CANNOT blindly reuse the systemOp branch as-is** — it must probe the **instanceOp's op requestId** (re-derivable as `deriveRequestID(inst.InstanceID, inst.Cursor)`), exactly as `onUserTaskDeadline` re-derives `deriveRequestID` for the CreateTask op (lines ~1088). **Add an `onExternalTaskDeadline` (or parameterize the probe key) so the tracker/outbox probes use the instanceOp's op requestId, and the advance uses the pending `handle` token.** This is the userTask-deadline pattern (probe the *op's* tracker/outbox; act on the *pending* token), minus the task-vertex existence read — an externalTask park has no "created vs human-wait" split, so there is no disarm-and-go-unbounded branch; it is a pure bounded wait like systemOp. Route it from `onDeadline` by detecting the externalTask token. **Decide the routing discriminator:** `isUserTaskToken` distinguishes userTask (`vtx.task.*`) from "everything else"; you now need to distinguish systemOp (bare requestId) from externalTask (bare handle) — both bare NanoIDs. Options: (i) read the **pinned pattern** in `onDeadline` and inspect `pattern.Steps[inst.Cursor].Kind` (authoritative; the pin is always present for a running instance — `getPinnedPattern`); (ii) persist the step kind on the `Instance` record. **Prefer (i)** (no new persisted field; the pin read is already the pattern of record in `advance`). Whichever you choose, the discriminator must be crash-safe and CAS-on-running like the existing handler. Document the choice.
- **tracker absent, `outbox.<opRequestId>` present** → the relay has not delivered the instanceOp yet → **re-arm** `deadline` (fresh `StepTimeout`); do not fail.
- **tracker absent, outbox absent** → the instanceOp was **rejected/lost** → **`fail`** the instance (atomic batch deletes `token.<handle>` + the deadline) → `FailPattern` lifecycle op → **Health alert** (the `e.logger.Warn` "instance failed" line + the FailPattern event; Health surfacing is via the existing fail path — no new Health code in 13.2). This is the FR29 "never a silent wedge" guarantee, mirroring the systemOp path.
- Every branch re-reads `instance` and is CAS-on-running (the `advance`/`fail` paths verify the pending token) — a redelivered marker / second replica is a no-op. Keep that invariant.

**Item 5 — Validation parity (the SECOND site: `internal/pkgmgr/orchestrationguard.go`).**
`validateLoomPatterns()` (lines ~149–177) currently **rejects** any kind other than `systemOp`/`userTask` (line ~167) — so today an `externalTask` pattern fails install. Teach it `externalTask`:
- Add `stepKindExternalTask = "externalTask"` to the re-stated kind constants (line ~30–33).
- In the step loop (lines ~166–174): accept `externalTask`; for it, require `Adapter`, `InstanceOp`, `ReplyOp` non-empty (`params` optional), mirroring the engine's `validate()`. For `systemOp`/`userTask`, keep requiring `Operation` (do NOT require `Operation` for `externalTask` — its op vocabulary is instanceOp/replyOp).
- This requires the pkgmgr **StepSpec** to carry the new fields. Extend `internal/pkgmgr/definition.go` `StepSpec` (lines ~136–148) with `Adapter`, `Params map[string]any` (author-friendly map, like `Guard`), `ReplyOp`, `InstanceOp` (exported Go fields; document each, no history comment). And extend `internal/pkgmgr/build.go` `loomPatternSpecBody` (lines ~395–416) to emit `adapter`/`params`/`replyOp`/`instanceOp` into the step map **when present** (omit when empty, like `guard`), so the installed `meta.loomPattern` body round-trips into the engine `Step`. **Both validation sites and the body builder must agree on the field names** (`adapter`/`params`/`replyOp`/`instanceOp`).
- **Guardrail (the brief's "reject wholesale"):** an `externalTask` step missing any required field, or any unknown kind, rejects the whole install (fail-closed, pure, before any KV write) — the existing posture. Do not partially accept.

**Item 6 — D5 invariant (b), gate-asserted.** The fixture `replyOp` DDL records the external outcome as **aspect(s)** on the claim vertex; the claim vertex root `data` stays minimal (at most a lifecycle scalar). **This is a TEST/GATE assertion in 13.2** (the real DDL is 14.4): the e2e's fixture replyOp must write the outcome to an **aspect** key (`vtx.<fixtureType>.<handle>.<aspectName>`) — NOT to the vertex root `data` — and the test must **assert** that (a) the outcome aspect exists with the outcome fields, and (b) the claim-vertex root `data` is minimal / does not carry the outcome fields. Treat this as a first-class assertion ("invariant b is gate-asserted"), not an incidental fixture detail. (The engine itself does not touch the claim vertex — D5 is enforced by where the fixture DDL writes — but the brief requires 13.2 to *prove* it, so the fixture must model the correct shape and the test must check it.)

### Invariant (a) — type-agnostic, PROVEN not asserted (the headline AC)

The engine e2e MUST drive the full loop with a claim vertex of a **non-`service`** type — use `vtx.widget.<handle>` (or any dot-free fixture type ≠ `service`). The test passes **only** because the engine never names a type: it mints the bare handle, the fixture instanceOp DDL chooses `widget`, and the bridge-fixture echoes the handle back. If anyone later hardcodes a type in the engine, the `widget` fixture breaks. **Do not** add a `service` fixture — the non-`service` type is the proof. (Andrew, 2026-06-18: "proven by a non-`service` fixture vertex type, not asserted.")

---

## 2. Required reading (DS does the deep reads; do not expect them pre-loaded)

- **FROZEN:** `docs/contracts/10-orchestration-surfaces.md` §10.5 (lines ~412–522, esp. the `externalTask` block ~470–497) + §10.6 (lines ~526–697, esp. the externalTask row ~544, the externalRef key ~546–555, the deadline+probe ~557–591, crash-safety invariants ~674–697). Contract #1 §1.1 for key shapes.
- `docs/components/loom.md` → "External steps (`externalTask`)" (~186–214) + "Execution loop"/"State & crash-safety"/"Failure modes" for the spine you are reusing. `docs/components/bridge.md` (the downstream consumer — the `external.<adapter>` envelope you emit toward, the determinism it relies on).
- `_bmad-output/planning-artifacts/sprint-change-proposal-2026-06-18.md` — Section 0 ratified decisions (B1/M4/m7), §3 the target flow, §4B/§4D the scope split (13.2 vs 13.4 vs 14.4).
- **The engine you are extending (read IN FULL):**
  - `internal/loom/pattern.go` — `Step`, `Pattern`, `validate()`, the kind constants.
  - `internal/loom/engine.go` — `submitStep`/`submitSystemOp`/`submitUserTask` (~816–900), `advance` (~720–760), `handleCompletion`/`correlationKeys`/`eventBody` (~640–712), `onDeadline`/`onUserTaskDeadline`/`trackerExists`/`taskVertexExists` (~998–1146), `isUserTaskToken`/`userTaskTokenPrefix` (~958–966), `Config.StepTimeout`/`CreateTaskTimeout`.
  - `internal/loom/state.go` — `transition` (~261–345; the spine you reuse), `outboxExists`/`rearmDeadline`/`disarmDeadline` (~347–401), `Instance` (~50–62).
  - `internal/loom/token.go` — `deriveRequestID`/`deriveTaskID`/`deriveID` (the namespacing pattern; add `deriveInstanceID`).
  - `internal/loom/actuator.go` — `buildOutbox` (~108–123), the relay (~70–106; for context — no change).
  - `internal/loom/source.go` — `opMetaProbe`/`indexOpMeta`/`opMetaKey` (~262–308) — **proof** that op-meta exposes only `data.operationType` (the §0 crux); no change.
- **The SECOND validation site:** `internal/pkgmgr/orchestrationguard.go` `validateLoomPatterns()` (~149–177) + the kind constants (~30–33); `internal/pkgmgr/definition.go` `StepSpec`/`LoomPatternSpec` (~115–148); `internal/pkgmgr/build.go` `loomPatternSpecBody` (~390–416).
- **Test patterns:** `internal/loom/loom_e2e_test.go` IN FULL — the embedded-NATS `provision`, the `fakeProcessor` (~93–287: how systemOp commits emit a business event, how CreateTask mints a task vertex + tracker, how rejectOps models the off-stream failed terminal, the exactly-once `submitted` counter, the `gate` for mid-flight control). This fixture is your model for the instanceOp/replyOp/external fixture. `internal/loom/onboarding_e2e_test.go` (a full userTask flow), `internal/loom/export_test.go` (test seams). `internal/pkgmgr/orchestrationguard_test.go` (~200–270: the validation-table tests you mirror for externalTask).

---

## 3. Test plan (concrete — count delivered tests from the diff)

**Engine unit / structural (`internal/loom`):**
- `pattern.go validate()`: an `externalTask` step with all of `adapter`/`instanceOp`/`replyOp` → valid; missing each (one per case) → rejected; `params` absent → valid; a guard on an externalTask step → valid (parses); an unknown kind → still rejected.
- `token.go`: `deriveInstanceID(i,c)` is deterministic; the three derivations (`deriveRequestID`/`deriveTaskID`/`deriveInstanceID`) for the same `(i,c)` are **distinct**; the handle is a valid bare NanoID (dot-free) → `isUserTaskToken(handle)` is **false**.
- `correlationKeys`: an event with `payload.externalRef` set yields the externalRef key; de-dup when externalRef == requestId or == taskKey; ordering requestId, taskKey, externalRef.

**Engine e2e (embedded NATS + `fakeProcessor`, the headline proofs):**
- **The full loop (invariant a):** a 1-step (or 2-step: collect → externalTask) pattern over a fixture subject; the fakeProcessor's instanceOp branch mints `vtx.widget.<instanceKey-from-payload>` (a **non-`service`** type) + writes the Contract #4 tracker; then (gated) a replyOp branch records the outcome **as an aspect** on `vtx.widget.<handle>` and emits a completion event carrying `payload.externalRef = <handle>`; assert Loom **advances** (cursor moves / pattern completes). **The engine names no type** — the test proves it.
- **D5 (invariant b), gate-asserted:** after the replyOp fixture commits, assert the outcome lives in an **aspect** (`vtx.widget.<handle>.<aspect>`) and the claim-vertex root `data` is minimal (does not carry the outcome fields).
- **Idempotency:** redeliver the replyOp completion event → no second advance (pointer-presence guard); the instanceOp submitted exactly once under a crash-retry (re-derived handle/opRequestId collapse on the tracker — assert the `submitted`/createdInstances counter == 1).
- **FR29 deadline backstops (mirror the systemOp tests):**
  - instanceOp **rejected** (fixture `rejectOps` contains the instanceOp type → no tracker, no event) → deadline fires → probe finds no tracker + no outbox → **`FailPattern`** emitted (assert the patternFailed event / status=failed). Never a silent wedge.
  - instanceOp **committed but reply never arrives** (tracker present, no completion event — gate the reply forever) → deadline fires → probe finds the **instanceOp's tracker** present → **advance + alert** (assert the "completion recovered via deadline probe" warn + cursor advance). **This is the test that proves the probe keys off the instanceOp op-requestId, not the handle** (§ item 4's subtle point).
  - instanceOp **not yet relayed** (outbox record still present, tracker absent — pause the relay) → deadline fires → **re-arm**, not fail.
- **Token routing:** assert an externalTask deadline does NOT route to the userTask creation-probe (no spurious task-vertex read / no CreateTask-rejected fail path); it routes to the externalTask/systemOp-style probe.

**pkgmgr validation parity (`internal/pkgmgr`):**
- `validateLoomPatterns`: an `externalTask` step with `adapter`/`instanceOp`/`replyOp` → valid install; missing each → rejected (one case each); the body builder (`loomPatternSpecBody`) emits the four fields and round-trips into an engine `Step` that `validate()`s. A `systemOp` step missing `operation` still rejected; an `externalTask` step does NOT require `operation`.

If you judge the story too large for one safe pass, halt and report a split proposal (e.g. 13.2a = pattern.go + token.go + engine submit/correlation + unit tests; 13.2b = the deadline-probe re-keying + the full fixture e2e + pkgmgr parity) rather than landing a half-wired step kind. But the pieces are tightly coupled (a step kind with no completion path is dead), so a single pass is preferred if feasible.

---

## 4. Verification gates (run before handing back; record each + result in the closing summary)

- `go build ./...`
- `make vet`
- `golangci-lint run ./...`
- `make verify-kernel` (Loom is a guarded engine; the externalTask change touches engine wiring — run it even though 13.2 makes no kernel-topology change, to prove no regression)
- `go test ./internal/loom/... ./internal/pkgmgr/... -count=1`
- The full 3-layer adversarial review is Winston's gate (Blind Hunter / Edge Case Hunter / Acceptance Auditor) per `bmad-code-review` — note in your summary that the change is guarded-engine and warrants it.

Flake retry per Deviation 14 is allowed; a flake claim without a re-run is a drift signal. The docker stack is not required for the loom/pkgmgr unit+e2e packages (they use embedded NATS — see `loom_e2e_test.go` `startNATS`).

---

## 5. Closing summary (DS appends when done)

Deliverables vs § 1 checklist; exact files changed (`git status`); test count (from diff); every gate + result (anything not run + why); the resolved instanceKey mechanism in one paragraph (or the CAR you filed + why); any deviation; any new Open Question. **Do NOT commit.**

### DS closing summary (Amelia, 2026-06-18)

**No CONTRACT-AMENDMENT-REQUEST filed.** The frozen contract is satisfiable literally via the §0 bare-handle resolution; the engine side was fully determined by the brief.

**Deliverables vs § 1 checklist — all six items delivered:**
1. ✅ `externalTask` step kind + frozen shape `{kind, adapter, params, replyOp, instanceOp}` taught to `Step` + `validate()` (`pattern.go`). systemOp/userTask require `operation`; externalTask requires `adapter`/`instanceOp`/`replyOp` (params optional), does NOT require `operation`; guards permitted on any kind; unknown kind / missing required field rejected wholesale.
2. ✅ `submitExternalTask` (`engine.go`): mints the bare handle via `deriveInstanceID`, parks on `token.<handle>`, builds the instanceOp outbox (caller-supplied `instanceKey`=bare handle + `subjectKey`/`adapter`/`replyOp`; `params` passed through verbatim, omitted when empty), arms the bounded `StepTimeout` via the existing `transition` spine, logs `loom externalTask write-ahead`. `deriveInstanceID` added to `token.go` with the `"instance:"` namespace (distinct from `""`/`"task:"`).
3. ✅ `payload.externalRef` is the third correlation key (`eventBody` + `correlationKeys`), ordered requestId→taskKey→externalRef, de-duped against the prior two.
4. ✅ Deadline backstop: `onDeadline` routes externalTask to a new `onExternalTaskDeadline` (discriminator = the pinned pattern's step kind at `inst.Cursor`, with `errPatternPinMissing` handled exactly like `advance`). The probe keys off the **instanceOp's own requestId** (`deriveRequestID`), NOT the parked handle — the #1 correctness trap — and acts (advance/fail) on the pending handle. tracker present→advance+alert; outbox present→re-arm; neither→fail+FailPattern.
5. ✅ Validation parity in the second site: `orchestrationguard.go validateLoomPatterns()` teaches `externalTask`; `definition.go StepSpec` extended (`Adapter`/`Params`/`ReplyOp`/`InstanceOp`); `build.go loomPatternSpecBody` emits the four fields (and `operation`) only when set. Field names agree across all sites.
6. ✅ Invariant (a) PROVEN by a **non-`service`** fixture claim type (`vtx.widget.<handle>`) — the engine names no type. Invariant (b)/D5 gate-asserted: the fixture replyOp writes the outcome to an **aspect** (`vtx.widget.<handle>.outcome`), the e2e asserts the aspect carries the outcome fields AND the claim-vertex root `data` does not.

**Files changed (`git status --short`):**
```
 M internal/loom/engine.go
 M internal/loom/export_test.go
 M internal/loom/loom_e2e_test.go
 M internal/loom/pattern.go
 M internal/loom/pattern_test.go
 M internal/loom/token.go
 M internal/loom/token_test.go
 M internal/pkgmgr/build.go
 M internal/pkgmgr/definition.go
 M internal/pkgmgr/orchestration_emit_test.go
 M internal/pkgmgr/orchestrationguard.go
 M internal/pkgmgr/orchestrationguard_test.go
?? internal/loom/correlation_test.go
?? internal/loom/external_e2e_test.go
```
(Production: `engine.go`, `pattern.go`, `token.go`, `definition.go`, `orchestrationguard.go`, `build.go`. No frozen contract or `planning-artifacts/*` edited. `docs/components/loom.md` left unedited — its externalTask section already describes the present code behavior, no drift.)

**Test count (from diff):** 14 new top-level test functions — engine unit: `TestCorrelationKeys` (6 subcases), `TestDeriveInstanceID_DeterministicValidAndDisjoint`, +8 externalTask subcases in the existing `validate()` table; engine e2e: `TestExternalE2E_RunsToCompletion` (invariant a + b + idempotency), `TestExternalE2E_RejectedInstanceOpFails`, `TestExternalE2E_CommittedNoReplyAdvancesViaProbe` (the probe-keys-off-instanceOp-requestId proof), `TestExternalE2E_NotYetRelayedRearms`; pkgmgr parity: 6 `TestValidateLoomPatterns_External*`/`SystemOpStillRequiresOperation` + `TestEmit_LoomPattern_ExternalTaskRoundTripsThroughEngineParse`.

**Gates (all run in foreground, all PASS):**
- `go build ./...` — clean.
- `make vet` — clean.
- `golangci-lint run ./...` — 0 issues.
- `make verify-kernel` — ALL ASSERTIONS PASSED (no kernel-topology regression; 13.2 makes none).
- `go test ./internal/loom/... ./internal/pkgmgr/... -count=1` — both `ok` (loom ~50s, pkgmgr <1s).

**Resolved instanceKey mechanism (one paragraph):** Loom mints a **bare deterministic NanoID handle** via `deriveInstanceID(instanceId, cursor)` (a new `"instance:"` namespace, disjoint from `deriveRequestID`'s `""` and `deriveTaskID`'s `"task:"`), parks on `token.<handle>` (the §10.6 `token.<instanceKey>` pointer), and passes the bare handle to the instanceOp as the caller-supplied `instanceKey` payload field — exactly the verbatim-id seam `CreateTask`'s `taskId` uses. The instanceOp DDL (a 14.4/test fixture) prepends its own package-chosen type to form `vtx.<type>.<handle>`; **the engine never names or forms a claim-vertex type** (proven by the non-`service` `widget` fixture). The instanceOp's own submission requestId is the disjoint `deriveRequestID(instanceId, cursor)`, so a crash-retry re-mints the same handle and the re-submitted instanceOp collapses on the Contract #4 `vtx.op.<opRequestId>` tracker. The bridge (13.4) echoes the handle back as `payload.externalRef`; Loom's third correlation key resolves `token.<handle>` → instance and advances. On a deadline, `onExternalTaskDeadline` re-derives the instanceOp requestId for the tracker/outbox probe (NOT the handle — probing `vtx.op.<handle>`/`outbox.<handle>` would read keys that never exist and always false-fail a healthy instance) while advancing/failing on the pending handle.

**Deviations from the story:** None of substance. Two minor, story-sanctioned choices: (a) `Step.Operation` json tag is now `omitempty` and `loomPatternSpecBody` emits `operation` only when set (so an externalTask step round-trips with no spurious empty `operation`); the existing userTask/systemOp round-trip is unaffected (their Operation is set). (b) Added a test-only `ResumeForTest` seam to `export_test.go` (mirroring the existing `PauseForTest`) so the re-arm e2e can pause the relay, let a deadline fire with the instanceOp undelivered, then resume — proving the probe re-arms rather than fails.

**New Open Questions:** None beyond the brief's Q1–Q3 (already recorded; none block this story). Q1 (the cross-story `externalRef` value seam) is the one item for the 13.4/14.4 authors to align to: this story emits and parks on the **bare handle** as `instanceKey`/`externalRef`; 13.4 must consume it as the opaque token it is, and 14.4's instanceOp DDL must read the bare `instanceKey` and prepend its type.

---

## Open Questions (saved for Winston / Andrew — none block the 13.2 engine build)

**Q1 — The cross-story `externalRef` value seam (13.2 ↔ 13.4 ↔ 14.4).** 13.2 mints and parks on a **bare NanoID handle** and emits it as the caller-supplied `instanceKey`; the bridge (13.4) must echo back **that same bare handle** as `payload.externalRef`, and the `instanceOp` DDL (14.4) must form the claim-vertex key as `vtx.<type>.<handle>`. `docs/components/bridge.md` currently shows `externalRef: "<instanceKey>"` with the envelope comment `instanceKey: "vtx.<type>.<id>"` — read literally, that suggests the *full key* is the token. **The §0 resolution (bare handle) is the only reading consistent with the frozen step shape + the engine's type-blindness + the "caller-supplied id like taskId" instruction**, and it is what 13.2 implements. Confirm: (a) 13.4's bridge keys its deterministic result-op `requestId` off the **bare handle** value it receives in the event's `instanceKey`/`idempotencyKey`/`externalRef` (it treats it as opaque, so this is fine either way), and (b) 14.4's `instanceOp` DDL reads the caller-supplied `instanceKey` (bare) and prepends its type. If the planning intent is instead that Loom mints the *full* `vtx.<type>.<id>`, that requires a 6th step field (claim-type) → a frozen-contract amendment — flagged here, not done. **Recommendation:** keep the bare-handle resolution (no amendment); have the 13.4/14.4 authors align to it. This does not change anything 13.2 builds.

**Q2 — `params` resolution boundary.** §10.5 says `externalTask.params` "are row/subject templates resolved per the §10.5/§10.8 templating rule." Row/subject templating is a **Weaver playbook** concern (`row.<field>`); a **Loom step's** params are concrete pattern data. 13.2 passes `step.Params` through **opaquely** (the engine resolves no templates) — the fixture supplies concrete params. Confirm no Loom-side template resolution is expected in Phase 2 (the bridge/adapter consumes `params` as given). If subject-relative templating IS wanted in a Loom externalTask param later, that is a separate, additive story. (Assumption taken; does not block — the engine treats params as an opaque pass-through.)

**Q3 — externalTask + an unbounded wait?** 13.2 arms the **bounded** `StepTimeout` on an externalTask park (the bridge is a machine completer; a never-arriving reply must trip FR29). This matches the §10.6 "exactly like a systemOp" wording. Confirmed by the contract; recorded only because the *userTask* park is unbounded and a reader might expect symmetry — the asymmetry is correct (human vs machine completer).
