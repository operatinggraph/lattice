# Story 14.4 — leaseApp convergence lens + externalTask patterns + signing (the new `lease-signing` package)

**Status:** review — package complete + 3-layer reviewed + fix-forward applied (read-free replyOp, result off the projection plane); end-to-end convergence row BLOCKED on CAR E6 (actorAggregate scalar-body projection), pending planning-lead ratification. Not `done` until E6 lands.
**Epic:** 14 — Loftspace Lease-Application Reference Vertical
**Tier:** Opus — the **Epic 14 integration centerpiece**. A **brand-new installable package** (`packages/lease-signing/`, none exists today — you create it) that wires together every prior brick: the `leaseapp` vertex type, the `leaseApplicationComplete` **actorAggregate** convergence lens (riding 14.2's keyColumn), the Weaver **playbook** (§10.8), the **`externalTask`** patterns (bgcheck + payment, riding 13.2's Loom step) with their **`instanceOp`/`replyOp` DDLs**, the `onboarding` `triggerLoom` pattern, and the `missing_signature` → `assignTask` wiring. The risk is **not** any single mechanism (each is shipped + tested upstream) — it is **getting the seams exactly right**: the §10.5/§10.6 externalTask completion wiring (epics AC#3 is **stale** — §0.A), the chip-#2 one-row-per-anchor guard (will **fail closed** if violated — §0.C), the bridge's `{externalRef, result}`-only reply payload (the `replyOp` DDL must reconstruct everything else — §0.B), and the lowercase `leaseapp` type (§0.D). Review: **full 3-layer adversarial** (Blind Hunter / Edge Case Hunter / Acceptance Auditor) per `bmad-code-review`. Plus the gates in §8.
**Epic spec:** `_bmad-output/planning-artifacts/epics/phase-2-epics.md` → "Story 14.4: leaseApp convergence lens + externalTask patterns + signing" (lines ~718–733) + the Epic 14 framing (~662–671) + the build order (14.1, 14.2, 14.3 → **14.4** → 14.5). Read it for the user-story framing and the four ACs (verbatim in §1). **Note: epics AC#3 (line ~730) is SUPERSEDED — build to the contract, not the epics text (§0.A).**
**Binding grounding (FROZEN / OWNED — read, build TO, do NOT edit):**
- **Contract #10 §10.2 / §10.5 / §10.6 / §10.8** (`docs/contracts/10-orchestration-surfaces.md`) — the orchestration surfaces. §10.2 (the `weaver-targets` row + the **actorAggregate keyColumn** amendment, lines ~120–131); §10.5 (the loomPattern + the **`externalTask`** step shape + `completionDomains`, lines ~412–533); §10.6 (step completion & correlation — the **externalTask creation-deadline disarm** + `payload.externalRef` correlation, lines ~537–670, esp. the §10.6 table row ~555 and the "externalTask creation path" ~633–670); §10.8 (the **playbook** target + action contracts + `triggerLoom`-of-externalTask, lines ~787–894). These were **amended 2026-06-18 (13.1 + the externalTask-deadline follow-up)**; the revision-history rows at the bottom (~979–980) are the source of truth for the externalTask completion model. **FROZEN — build to them.**
- **Contract #1 §1.1** (`docs/contracts/01-key-shapes.md`) — aspects 4-segment `vtx.<type>.<id>.<local>`; links 6-segment `lnk.<typeA>.<idA>.<relation>.<typeB>.<idB>` reading "source <relation> target"; the **later-arriving vertex is the source** (CLAUDE.md house rule). **FROZEN.**
- **D5 — task/service DDL data placement (LOCKED)** (`_bmad-output/planning-artifacts/lattice-architecture.md` ~1167) — minimum data in the vertex root, descriptive/business data in **aspects**. The service instance's external outcome lives in the **`.outcome` aspect** (14.1 shipped it); the lens **reads that aspect**, not root data. Planning artifact — do **not** edit.
**Grounding (the code you build ON — read; the package code is yours to author, the engines/installer are NOT):**
- **14.1 service-domain** (`packages/service-domain/ddls.go`, `package.go`, `service_instance_test.go`, `type_agnostic_test.go`) — the `service` DDL: `class: "service.<x>.template"|"service.<x>.instance"` (`<x>` ∈ {backgroundCheck, payment}); links `availableAt`/`providedBy`/`instanceOf`/`providedTo`; ops `CreateServiceTemplate`/`CreateServiceInstance`/`RecordServiceOutcome`; the **`.outcome` aspect** `{status ∈ {completed,failed}, completedAt (canonical-UTC RFC3339)}`; the **caller-supplied bare-NanoID `instanceId` seam** on `CreateServiceInstance` (the write-ahead handle path). It declares `OpMetas` for `CreateServiceInstance` + `RecordServiceOutcome`. **READ §0.B — service-domain's ops are NOT the externalTask instanceOp/replyOp as-is; you ship thin wrapper ops in lease-signing.**
- **14.2 keyColumn** (`internal/refractor/projection/output.go` `OutputDescriptor.KeyColumn` + `BuildKey` ~50–202; `internal/refractor/lens/corekv_source.go` ~111–116; `internal/pkgmgr/definition.go` `OutputDescriptorSpec.KeyColumn` ~359) + the **end-to-end example you mirror**: `internal/refractor/refractor_keycolumn_convergence_e2e_test.go` (a throwaway actorAggregate keyColumn lens, the `proofConvergenceSpec` cypher ~56–63 + the `OutputDescriptorSpec{…, KeyColumn:"entityId"}` ~79–88). This is the **exact** lensSpec shape your `leaseApplicationComplete` lens copies.
- **13.2 Loom externalTask** (`internal/loom/engine.go` `submitExternalTask` ~954–990 + `onExternalTaskDeadline` ~1267–1302; `internal/loom/pattern.go` the `Step`/`externalTask` shape ~28–48 + `validate` ~181–230) — the engine submits `instanceOp` with payload `{instanceKey (the bare handle), subjectKey, adapter, replyOp, params?}` and parks on `token.<handle>`; it correlates completion on **`payload.externalRef`**. The engine **hardcodes no vertex type** (invariant a) — your `instanceOp` DDL prepends the type. `internal/pkgmgr/orchestrationguard.go` — the **install-time** pattern/target/op-meta validation (the rules your declarations must pass; §3).
- **The bridge** (`internal/bridge/dispatch.go` `handleExternal` ~86–181, `actuator.go` `submit` ~55–76, `token.go` `deriveReplyRequestID` ~26–27, `fake_background_check.go`) — consumes `events.external.<adapter>`, calls the adapter idempotently, and **posts the `replyOp` with payload `{externalRef: <handle>, result: "<adapter Detail string>"}` ONLY** — no `status`, no `completedAt`. **This is the §0.B gap that shapes the whole `replyOp` DDL.** `requestId = deriveReplyRequestID(instanceKey)` (FR58 determinism).
- **The package surfaces** (`internal/pkgmgr/definition.go`) — `Definition` already supports `DDLs`, `Lenses`, `Permissions`, `Roles`, **`WeaverTargets` (`WeaverTargetSpec`/`GapActionSpec`)**, **`LoomPatterns` (`LoomPatternSpec`/`StepSpec` — `StepSpec` already carries `Adapter`/`Params`/`ReplyOp`/`InstanceOp` for externalTask)**, **`OpMetas`**. The plumbing to **declare** a convergence lens + a playbook + externalTask patterns **already exists** — 14.4 is package *content*, not installer plumbing. (Confirm: no `internal/pkgmgr` change is anticipated — §0.E / Q9.)
- **The 14.1 canonicalName-uniqueness validator** (`internal/pkgmgr/definition.go` `validateCanonicalNameUniqueness` ~31–59) — **every** DDL/lens/op-meta canonicalName this package declares must be **globally unique** across its own union (and must not collide with an already-installed package's). The install **fails closed** on a collision. Pick names that won't clash (§0.F).
- **The reference package the whole package mirrors** (`packages/service-domain/` + `packages/orchestration-base/`) — the `package.go` `var Package = pkgmgr.Definition{…}` shape, the `manifest.yaml` shape (`VerifyAgainstDefinition` cross-checks DDL/permission/opMeta counts + names + grantsTo — a drift fails `lattice-pkg install`), the Starlark DDL idioms (`make_vtx`/`make_aspect`/`make_link`/`required_string`/`parts_of`/`vertex_alive`/known-key reads), and the **test harness** (`testutil.SetupPackageTestEnv` / `RunMetaInstallPipeline` / `CapabilityPipeline` / `SeedCapDoc`; embedded NATS, no Docker).
**Depends on:** **14.1 + 14.2 + 14.3 + 13.2** (all DONE, CI green). 14.1 = the `service` instance + `.outcome` aspect + instanceId seam. 14.2 = the actorAggregate keyColumn. 14.3 = the identity `ssn`/`dob` (+ name/email/phone) sensitive aspects the lens reads as applicant data. 13.2 = the Loom `externalTask` step. **Also leans on** 13.4 (the bridge — its `{externalRef, result}` reply shape is the §0.B constraint, even though 14.4 does **not** drive the live bridge — §0.G).
**Forward (note, do NOT build — §5):** **14.5** ships the bridge-driven e2e convergence harness + the `test-lease-convergence` CI gate + the FR58 double-act proof through the live bridge. **14.4's tests use direct outcome-aspect writes (AC #4) — NOT the live bridge.** **13.5** (retire Weaver's nudge) is unblocked only after 14.5 is green; 14.4 must use **`triggerLoom` of an externalTask pattern** for external remediation, **never a `nudge` action** (the AC #4 carry in §10.8; epics ~658).
**Workflow:** you are the DS (dev) sub-agent. Repo root, no worktree. Do **NOT** commit/push/branch. Do **NOT** edit frozen contracts (`docs/contracts/*`) or planning artifacts (`epics/*.md`, `lattice-architecture.md`, `prd.md`, the change proposals). New docs/notes go in the **package README** (`packages/lease-signing/README.md`) or `/docs`, never `_bmad-output/`. A genuine frozen-contract gap → `cmd/<area>/CONTRACT-AMENDMENT-REQUEST.md` + flag at the TOP of your closing summary; do **not** edit the contract. Leave all changes in the working tree for Winston.

> **TOP-OF-STORY FLAGS — read before you start. There are FIVE binding overrides; they govern the whole story.**
>
> 1. **Epics AC#3 is STALE (§0.A).** Line ~730 says *"each `externalTask` pattern declares the **`replyOp`'s completion domain**"* — that is **WRONG**, the model it describes (advance-on-instanceOp-commit + the deadline as a completion backstop) was the *first* 13.1 ratification and was **corrected by the 13.6 follow-up** (committed; Contract #10 §10.5/§10.6 + revision-history rows ~979–980). **Build to the contract:** each externalTask's **`replyOp` DDL emits `orchestration.externalTaskCompleted{externalRef}`**, the patterns declare **`completionDomains: ["orchestration"]`** (NOT the replyOp's own domain), and the creation-deadline **DISARMS** on instanceOp-commit (it is NOT a completion backstop). **Cite the current §10.5/§10.6 text in your work; explicitly flag epics line 730 as superseded** (do not edit the epics file).
> 2. **The bridge posts `{externalRef, result}` ONLY (§0.B).** It supplies **no `status`, no `completedAt`** — only `payload.externalRef` (the bare handle) + `result` (a free-form adapter `Detail` string). So the **`replyOp` DDL cannot be 14.1's `RecordServiceOutcome` as-is** (which *requires* `instanceKey` (full key), `status`, and `completedAt` as caller payload). Your `replyOp` DDL **reconstructs** `vtx.service.<externalRef>`, **derives** `status` + `completedAt` itself, writes the `.outcome` aspect, **and** emits `orchestration.externalTaskCompleted`. This is the single most underspecified seam — see Q1/Q2.
> 3. **The lens MUST be one-row-per-anchor or it FAILS CLOSED (§0.C).** The keyColumn makes the row key anchor-derived; a multi-row-per-anchor actorAggregate now trips `guardOutputKeyCollision` (`internal/refractor/pipeline/evaluate.go` ~239–273) → **Terminal → DLQ + Health**. The multi-hop cypher MUST `collect(DISTINCT …)` down to **exactly one row per `leaseapp` anchor**. This is not a style preference — it is a hard runtime guard.
> 4. **The type is lowercase `leaseapp` (§0.D).** `leaseApp` (camelCase) is an **invalid** Contract #1 type segment (`[a-z][a-z0-9]*`). The epics/§10.2/§10.8 and `orchestration-base`'s illustrative `vtx.leaseApp.<…>` strings are **ILLUSTRATIVE only** — the real vertex type is **`leaseapp`**. (The `targetId` `leaseApplicationComplete` stays camelCase — it is a KV-key token `[A-Za-z0-9_-]+`, not a type segment.)
> 5. **14.4 does NOT build 14.5 (§0.G / §5).** Ship the package + tests that prove the lens/ops/patterns via **direct outcome-aspect writes**. Do **NOT** build the live-bridge e2e, the `test-lease-convergence` gate, or the FR58 double-act proof — those are 14.5.

---

## 0. THE HEADLINE — wire FIVE shipped bricks into one convergent package, getting each SEAM exactly right (read first; it governs everything)

14.4 invents almost no new mechanism — every brick (actorAggregate keyColumn, Loom externalTask, the bridge, the service instance + outcome aspect, the sensitive identity aspects) is shipped and tested upstream. **The story IS the seams.** Get these five right and the package converges; get any one wrong and it fails closed (the guard), silently mis-projects (the cypher), or never completes (the externalTask wiring).

### 0.A — The externalTask completion wiring (epics AC#3 is stale; build to §10.5/§10.6)

The epics text (line ~730) describes the **superseded** model. The **contract truth** (§10.5 ~498–508, §10.6 ~555 + ~633–670; revision rows ~979–980):

- The externalTask step is `{ kind: "externalTask", adapter, params, replyOp, instanceOp }`.
- Loom submits **`instanceOp`** with payload `{instanceKey: <bare handle>, subjectKey, adapter, replyOp, params?}` and **parks on `token.<handle>`**.
- The **`instanceOp` DDL** (a) mints the **claim vertex** `vtx.service.<handle>` (it **prepends** the package-chosen type `service` to the bare handle — the engine never names a type) and (b) **emits the `external.<adapter>` event** via *its own transactional outbox*.
- The bridge calls the adapter and posts **`replyOp`** with `payload.externalRef = <handle>`.
- The **`replyOp` DDL** (a) records the external outcome as the **`.outcome` aspect** on `vtx.service.<handle>` (D5) **and** (b) **emits `orchestration.externalTaskCompleted` carrying `payload.externalRef = <handle>`** — the uniform orchestration-domain completion signal Loom correlates on (symmetric to `orchestration.taskCompleted{taskKey}` for a userTask).
- The externalTask patterns therefore declare **`completionDomains: ["orchestration"]`** (NOT `["service"]`, NOT the replyOp's own domain). Loom's existing `loom-orchestration` consumer advances them.
- The creation-deadline **DISARMS** on `instanceOp` commit → **unbounded** wait for the bridge reply (it **never advances the cursor**). A **rejected/lost `instanceOp`** still → `FailPattern` (FR29; the engine's `onExternalTaskDeadline` already does this — your DDLs just need to be rejectable-on-bad-input).

**The completion event is emitted by the purpose-built `replyOp`**, not platform-injected (the way `taskCompleted` is for a userTask's oblivious bound op) — because the `replyOp` is a deliberate result op. **You must emit it explicitly** in the `replyOp` DDL's `events`. **If you forget it, the externalTask never completes** (the deadline disarmed; the bridge reply landed but carried no completion signal) — a silent wedge the load-time warn (`externalTaskCompletionUnobservable`) does *not* catch (that warn only fires if `completionDomains` omits `orchestration`, which yours won't). §6 test 4 is the trap.

### 0.B — The bridge reply is `{externalRef, result}` ONLY — the replyOp DDL fills the rest

`internal/bridge/dispatch.go:164-167` posts the replyOp payload as exactly:
```go
payload := map[string]any{ "externalRef": ev.externalRefValue(), "result": result.Detail }
```
`result.Detail` is the adapter's free-form string (e.g. `FakeBackgroundCheck` returns `"background-check cleared for <subject>"`). **There is no `status` field and no `completedAt` field.** Consequences for your `replyOp` DDL:

1. **It reconstructs the claim-vertex key** from the bare handle: `inst_key = "vtx.service." + externalRef`. (The handle is a bare NanoID, type-free; the instanceOp chose `service` as the type, so the replyOp must re-prepend the *same* type — they are a matched pair in your package.)
2. **It derives `status` itself.** The bridge gives only a free-form `result` string. For the Phase-2 demo the Fake adapters always succeed, so the pragmatic rule is **`status = "completed"`** whenever a `replyOp` arrives (the bridge only posts a reply on adapter success — an adapter *error* is Nak+retry, never a reply). A `failed` outcome has **no producer on the bridge path** in Phase 2. **Recommendation: the replyOp DDL sets `status = "completed"`** and stores `result` verbatim (for provenance / a future structured-result adapter). **Confirm this — it is the load-bearing demo simplification (Q2).** Do NOT try to parse the free-form `result` string for pass/fail (brittle; the real verification is the adapter's job in Phase 3).
3. **It derives `completedAt` itself** — use the op's own `op.submittedAt` (the bridge's `SubmittedAt` on the reply envelope), normalized to canonical-UTC RFC3339 via `time.rfc3339_utc(…)` (the same normalization 14.1's `RecordServiceOutcome` uses), so the downstream freshness predicate (`completedAt + window > now`) is a sound lexical compare. (Q2.)
4. **It writes the `.outcome` aspect** in the **exact shape 14.1 ships** — `vtx.service.<handle>.outcome` class `outcome` `{status, completedAt}` — so the convergence lens reads one aspect shape regardless of who wrote it.
5. **It emits `orchestration.externalTaskCompleted{externalRef}`** (§0.A).

**Why a NEW wrapper op, not 14.1's `RecordServiceOutcome` (Q1):** `RecordServiceOutcome` (a) requires `instanceKey` as a **full** `vtx.service.<id>` key (the bridge supplies a bare handle as `externalRef`), (b) requires caller-supplied `status` + `completedAt` (the bridge supplies neither), and (c) emits `service.outcomeRecorded`, **not** `orchestration.externalTaskCompleted` — so it would **never complete the Loom step**. Reusing it is impossible without changing service-domain (a frozen-as-shipped dependency this story must not edit). **So 14.4 ships its own `replyOp` DDL** (and its own `instanceOp` DDL) in `lease-signing`. The `.outcome` aspect *shape* is reused (D5 fidelity); the *ops* are package-local. (See Q1 for the considered alternative — extending service-domain — and why the lean local-op design wins.)

### 0.C — The lens MUST be one-row-per-anchor (chip-#2 guard fails closed)

The §10.2 Option-b keyColumn (14.2) makes the row key **`<targetId>.<entityId>`** where `<entityId>` is the **bare-NanoID `leaseapp` anchor** id (derived from `actorKey` by `BuildKey`). Because the key is anchor-derived, the lens **must** emit **exactly one row per anchor** — otherwise two rows collide on the same output key and `guardOutputKeyCollision` (`internal/refractor/pipeline/evaluate.go:239-273`) returns `failure.Terminal` → **DLQ + Health alert**. The multi-hop cypher
```
MATCH (app:leaseapp {key: $actorKey})
OPTIONAL MATCH (app)-[:applicationFor]->(id:identity)
OPTIONAL MATCH (id)<-[:providedTo]-(inst:service)
…
RETURN app.key AS actorKey, collect(DISTINCT {…}) AS …
```
**MUST `collect(DISTINCT …)` every fan-out** (the identity's aspects, the service instances) into aggregate columns under the single `app` anchor row, exactly as `proofConvergenceSpec` (the 14.2 e2e) and `myTasksSpec`/`capabilityEphemeralSpec` (orchestration-base) do. **Never `RETURN inst.key` un-aggregated** (that would emit one row per instance → collision). The `violating` / `missing_*` / `applicant` columns are scalar per-anchor computations (booleans / a single applicant key); the multi-instance fan-out (e.g. two service instances for one app) collapses via `collect`/`EXISTS`/aggregation in the predicate. **§6 test 1 must assert exactly one row per leaseapp.** (See §2 Item C for the cypher design.)

### 0.D — The type is lowercase `leaseapp`

Contract #1 type segments are `[a-z][a-z0-9]*`. `leaseApp` (camelCase) is **invalid** — it would be rejected by key-shape validation. Every illustrative `vtx.leaseApp.<…>` in the epics, §10.2 (~111), and `orchestration-base`'s old `scopedTo` example (~86) is **non-normative**. **Use `leaseapp`** for the vertex type (`vtx.leaseapp.<id>`, `lnk.leaseapp.<id>.applicationFor.identity.<id>`, the lens `AnchorType: "leaseapp"`). The **`targetId`** stays `leaseApplicationComplete` (it is a `weaver-targets` key prefix, validated as `[A-Za-z0-9_-]+`, not a type segment — camelCase is fine there and matches §10.2's example key ~107).

### 0.E — Scope: package CONTENT only (no installer/engine change anticipated)

Everything 14.4 needs already exists in the installer + engines (the `Definition` fields, the externalTask step, the keyColumn, the install-time validators). **14.4 ships package content** — DDLs, a lens, a playbook, patterns, op-metas, permissions, a manifest, a README, and tests. **No `internal/pkgmgr`, `internal/loom`, `internal/refractor`, `internal/weaver`, or `internal/bridge` change is anticipated.** If you find you *need* an engine/installer change, that is a **RED FLAG** — surface it as a blocking Open Question (Q9); the brief's expectation is zero core change (the upstream stories built the seams precisely so this package is pure content).

### 0.F — Every canonicalName must be globally unique

`validateCanonicalNameUniqueness` fails the install closed if two DDLs/lenses/op-metas share a canonicalName (within this package OR colliding with an installed package). The package declares **many** named meta-vertices — pick distinct, collision-proof names. Recommended set (all new, none used by identity/service/orchestration-base/rbac): DDL `leaseapp`; the externalTask wrapper DDLs `leaseServiceInstance` + `leaseServiceReply` (Q1) — **NOT** `service`/`CreateServiceInstance`/`RecordServiceOutcome` (those are service-domain's; reusing the names would collide); lens `leaseApplicationComplete`; op-metas for each externalTask `instanceOp`/`replyOp` + the `SignLease` op (assignTask target) + `StartLoomPattern` is already an op-meta? (No — confirm; orchestration-base ships the `loomLifecycle` DDL with `StartLoomPattern` as a PermittedCommand but **no `OpMeta`** for it; the playbook's `triggerLoom` does not need a `forOperation` op-meta — only `assignTask`'s operation does, §3). Patterns `backgroundCheck` / `collectPayment` / `onboarding` (these are `PatternID`s, a separate namespace from canonicalNames, but keep them distinct anyway). Target `leaseApplicationComplete` (a `TargetID`, separate namespace). **Run the uniqueness check in your head against the installed set before finalizing names.**

### 0.G — 14.4 tests use DIRECT outcome-aspect writes, NOT the live bridge

AC #4: the lens is testable via **direct writes of the instance's `.outcome` aspect** — it does **not** serialize behind the bridge. Your lens/convergence tests drive `CreateServiceInstance` (or your `leaseServiceInstance` instanceOp) then **directly submit the `.outcome` aspect write** (via your `replyOp` op with a synthetic `{externalRef, result}` payload, OR via 14.1's `RecordServiceOutcome` for a pure lens-input test) and assert the lens reprojects. The **live bridge round-trip** (publish `external.*` → bridge → reply) is **14.5**. This keeps 14.4's tests fast (no bridge process) and decoupled. (See §6.)

Everything else (the `leaseapp` DDL + ops, the lens cypher + Output descriptor, the playbook, the three patterns, the two wrapper DDLs, the manifest, the README, the tests) is scaffolding that makes these seven facts real.

---

## 1. The four ACs (verbatim) + adjudication

### The ACs (from `phase-2-epics.md` ~726–731)

> **Given** the redesigned `lease-signing` package
> **When** installed
> **Then** `leaseApplicationComplete` is an **`actorAggregate`** lens (`AnchorType: leaseApp`, multi-hop `MATCH (app)-[:applicationFor]->(id), (id)<-[:providedTo]-(inst:service)`) reading **identity aspects + the service instance's outcome aspect**, emitting the bare-NanoID key via 14.2's key column, reprojecting on any linked-constituent change
> **And** the playbook remediates external gaps via **`triggerLoom`** of a pattern containing an **`externalTask`** (bgcheck, payment); `missing_signature` → `assignTask`; `missing_onboarding` → `triggerLoom(onboarding)`
> **And** each `externalTask` pattern declares the **`replyOp`'s completion domain** in `completionDomains` (else the step only completes via the deadline backstop) *(← **STALE; see §0.A — build to §10.5/§10.6: `completionDomains: ["orchestration"]` + the replyOp emits `orchestration.externalTaskCompleted`; the deadline DISARMS, it is not a completion backstop**)*
> **And** the lens is testable via **direct writes of the instance's outcome aspect** (does not serialize behind the bridge)

### Adjudication — what each AC binds

- **AC #1 → §2 Items B+C (the `leaseapp` type + ops, and the convergence lens).** "actorAggregate lens" = a `LensSpec` with `ProjectionKind: "actorAggregate"` + an `Output` descriptor (§2 Item C). "`AnchorType: leaseApp`" → **`AnchorType: "leaseapp"`** (§0.D). "multi-hop `MATCH … applicationFor … providedTo`" = the cypher walks app→identity→service-instance, **one-row-per-anchor via `collect(DISTINCT)`** (§0.C). "reading identity aspects + the service instance's outcome aspect" = the cypher reads `vtx.identity.<id>.{name,email,ssn,dob,…}` (14.3) for the applicant param + `vtx.service.<id>.outcome` (14.1) for the bgcheck/payment freshness. "emitting the bare-NanoID key via 14.2's key column" = `Output.KeyColumn` set (§0.C). "reprojecting on any linked-constituent change" = the actorAggregate adjacency-reprojection (14.2 machinery — you declare it, the engine does it).
- **AC #2 → §2 Item D (the playbook).** "playbook remediates external gaps via `triggerLoom` of a pattern containing an `externalTask`" = the `WeaverTargetSpec.Gaps` for `missing_bgcheck` + `missing_payment` are `{action: "triggerLoom", pattern: "backgroundCheck"|"collectPayment", subject: "row.applicant"}`, and those patterns' bodies contain an `externalTask` step (§2 Item E). "`missing_signature` → `assignTask`" = `{action: "assignTask", operation: "SignLease", assignee: "row.applicant", target: "row.entityKey"}`. "`missing_onboarding` → `triggerLoom(onboarding)`" = `{action: "triggerLoom", pattern: "onboarding", subject: "row.applicant"}`. **NO `nudge` action** (retired — §10.8 ~833; the AC #4 carry epics ~658).
- **AC #3 → §2 Item E + §0.A (the externalTask completion wiring — built to the CONTRACT, not the stale epics text).** Each externalTask pattern declares `completionDomains: ["orchestration"]`; its `replyOp` DDL emits `orchestration.externalTaskCompleted{externalRef}`; the creation-deadline disarms on instanceOp-commit. **§6 test 4 proves the completion event is emitted with the correct `externalRef`.**
- **AC #4 → §6 (direct-write testability).** The lens + ops are tested via **direct `.outcome` aspect writes** (§0.G), not the live bridge. **§6 test 1/2/3 do this.**

### The two Epic-13/14 invariants on these ACs (Andrew; epics ~579–581 — they apply to Epic 14)

- **(a) type-agnostic engines — ALREADY PROVEN; NOT re-proven here.** Epic 13 proved the engines/bridge are type-blind via a **non-`service` fixture** (`vtx.widget.<id>`). 14.4 is the **real vertical** using **real types** (`service`/`leaseapp`/`identity`), so it does **not** re-prove engine type-agnosticism — it *consumes* the proven generality. **Note this in your summary** so a reviewer does not flag the absence of a non-service fixture here. Corollary: **no `leaseapp`/`service` literal may leak into `internal/*` engine code** (your concrete types live ONLY in `packages/lease-signing` content) — mirror `service-domain/type_agnostic_test.go` with a `leaseapp`-string-absent-from-internal assertion (§6 test 7) to keep the boundary honest.
- **(b) D5 — directly in play.** The service instance's external outcome lives in the **`.outcome` aspect** (14.1 shipped it; your `replyOp` writes the same shape); the `leaseapp` vertex root `data` stays **minimal**; the lens **reads the aspect**, never fat root data. **§6 tests assert the `.outcome` aspect carries status+completedAt AND the `leaseapp`/`service` root `data` stays minimal.**

### Scope boundary

**In scope:**
1. **A new `packages/lease-signing/` package** — `package.go` (`var Package = pkgmgr.Definition{…}`), `manifest.yaml`, `ddls.go`, `lenses.go`, `patterns.go` (the LoomPatternSpecs), `targets.go` (the WeaverTargetSpec) [file split is the author's call — mirror service-domain/orchestration-base], `permissions.go`, `README.md`. (§2.)
2. **The `leaseapp` vertex-type DDL + a `CreateLeaseApplication` op** that mints `vtx.leaseapp.<id>` (root data minimal — D5) + writes the `applicationFor` link to the applicant identity, and (per the model — Q4) ties the application to its service instances via the `providedTo` links the service instances already carry (the lens walks `applicationFor` then `providedTo`). (§2 Item B.)
3. **The `leaseApplicationComplete` actorAggregate convergence lens** — `AnchorType: "leaseapp"`, the multi-hop one-row-per-anchor cypher (§0.C), the `Output` descriptor with `KeyColumn` set (14.2), projecting `entityKey` / `violating` / `missing_onboarding` / `missing_bgcheck` / `missing_payment` / `missing_signature` / `applicant` (the §10.2 column conventions). (§2 Item C.)
4. **The `meta.weaverTarget` playbook** — `TargetID: "leaseApplicationComplete"`, `LensRef: "leaseApplicationComplete"`, the four `Gaps` (bgcheck/payment → `triggerLoom` externalTask patterns; onboarding → `triggerLoom`; signature → `assignTask SignLease`). (§2 Item D.)
5. **The three `meta.loomPattern`s** — `backgroundCheck` + `collectPayment` (each `subjectType: "identity"`, a single `externalTask` step, `completionDomains: ["orchestration"]`) and `onboarding` (`subjectType: "identity"`, userTask steps, `completionDomains: ["orchestration"]`). (§2 Item E.)
6. **The two externalTask wrapper DDLs** — `leaseServiceInstance` (the `instanceOp`: mints `vtx.service.<handle>` + emits `external.<adapter>`) and `leaseServiceReply` (the `replyOp`: records the `.outcome` aspect from `{externalRef, result}` + emits `orchestration.externalTaskCompleted{externalRef}`). (§2 Item F — the §0.A/§0.B centerpiece.)
7. **The `SignLease` op** (the `assignTask` target) + its op-meta + permission. A minimal op that records the applicant's signature on the leaseapp (so `missing_signature` can close). (§2 Item G.)
8. **Op-metas** for every op a step/playbook binds via `forOperation`: the two externalTask `instanceOp`/`replyOp` (the engine resolves these by name at submit, but declare op-metas for discoverability + the manifest cross-check), and `SignLease` (the `assignTask` target — **required**, §3). (§2 Item H.)
9. **Permissions** — grant `CreateLeaseApplication`, the two externalTask ops, and `SignLease` to the right roles (operator for the orchestrator-submitted ops; the applicant role for `SignLease` if a user submits it via the task grant — Q6). (§2 Item I.)
10. **`manifest.yaml`** mirroring `VerifyAgainstDefinition`'s cross-check (DDL/lens/permission/opMeta/weaverTarget/loomPattern counts + names + grantsTo). (§2 Item J.)
11. **Tests** (§6) — install round-trip; the lens projects one row per anchor with the right gap columns (direct `.outcome` writes — AC #4); the externalTask instanceOp mints the claim vertex + emits `external.<adapter>`; the replyOp records the outcome aspect + emits `orchestration.externalTaskCompleted` (the §0.A trap); the playbook validates at install; D5 root-minimal assertions; the `leaseapp`-absent-from-internal invariant-a assertion.
12. **`packages/lease-signing/README.md`** — the package's DDL/lens/pattern/target inventory + the externalTask seam explanation (§0.A/§0.B) + the lowercase-`leaseapp` note + the AC#3-superseded note. New doc → README, never `_bmad-output/`.

**Out of scope (do NOT build — 14.5 / other stories):**
- **NO live-bridge e2e, NO `test-lease-convergence` gate, NO FR58 double-act-through-the-bridge proof.** That is **14.5** (§0.G / §5). 14.4 proves the seams via **direct outcome-aspect writes**.
- **NO `nudge` action** (retired — §10.8). External remediation is `triggerLoom` of an externalTask pattern (the AC #4 carry, epics ~658).
- **NO engine/installer change** (`internal/pkgmgr`, `internal/loom`, `internal/refractor`, `internal/weaver`, `internal/bridge`) — package content only (§0.E). A needed change is a RED-FLAG Open Question (Q9).
- **NO change to 14.1 service-domain, 14.2 refractor, 14.3 identity-domain, 13.2 loom** — they are DONE dependencies. Your package *consumes* them; if you find a gap, flag it (Q1/Q8), do not edit them.
- **NO Vault/KMS/crypto-shred** — 14.3's sensitive marker is the boundary; 14.4 only *reads* the sensitive aspects. No encryption.
- **NO `serviceAccess` / `cap.svc` read-path auth** — Phase-3-deferred (14.1 AC; charter). The `weaver-targets` bucket is internal operational state, off the read-path (§10.2 ~156).
- **NO non-service fixture / type-agnosticism re-proof** — done in Epic 13 (invariant a; §1 above).
- **NO Postgres read-model of the leaseapp** — the lens projects to the `weaver-targets` NATS-KV bucket only (the convergence detection plane). A Phase-3 Postgres read-path is orthogonal (§10.2 ~158).

---

## 2. The mechanism — item-by-item (DS builds to THIS)

Author the package mirroring `service-domain` + `orchestration-base` at every layer: the `package.go`/`manifest.yaml` shape, the Starlark DDL idioms (known-key reads, `make_vtx`/`make_aspect`/`make_link`, `parts_of`/`vertex_alive`/`required_string`), the actorAggregate lens shape (`proofConvergenceSpec` + `myTasksSpec`), the LoomPatternSpec/WeaverTargetSpec/OpMetaSpec declarations, and the test harness (`testutil.*`).

### Item A — package skeleton (`packages/lease-signing/`)

`package.go`:
```go
package leasesigning

import "github.com/asolgan/lattice/internal/pkgmgr"

var Package = pkgmgr.Definition{
    Name:          "lease-signing",
    Version:       "0.1.0",
    Description:   "Loftspace lease-application convergence vertical: the leaseapp vertex type + CreateLeaseApplication, the leaseApplicationComplete actorAggregate convergence lens (§10.2 keyColumn), the §10.8 playbook (triggerLoom externalTask for bgcheck/payment, assignTask SignLease, triggerLoom onboarding), the externalTask instanceOp/replyOp wrapper DDLs, and SignLease. Depends identity-domain + service-domain + orchestration-base.",
    Depends:       []string{"identity-domain", "service-domain", "orchestration-base"},
    DDLs:          DDLs(),          // leaseapp, leaseServiceInstance, leaseServiceReply, signLease (the SignLease DDL)
    Lenses:        Lenses(),        // leaseApplicationComplete
    Permissions:   Permissions(),
    WeaverTargets: WeaverTargets(), // leaseApplicationComplete target
    LoomPatterns:  LoomPatterns(),  // backgroundCheck, collectPayment, onboarding
    OpMetas:       OpMetas(),       // the instanceOp/replyOp + SignLease (+ CreateLeaseApplication if anything binds it)
}
```
Split the content across `ddls.go` / `lenses.go` / `patterns.go` / `targets.go` / `permissions.go` as the author prefers (mirror service-domain's per-file split). **Depends order matters for nothing at runtime** but list all three so `lattice-pkg` warns on a missing prereq.

### Item B — the `leaseapp` vertex-type DDL + `CreateLeaseApplication`

A `meta.ddl.vertexType` DDL, canonicalName **`leaseapp`** (§0.D), PermittedCommands `["CreateLeaseApplication"]` (+ `SignLease` if you co-locate it on this DDL — Q3). Shape (D5 — root minimal):
```
vtx.leaseapp.<id>                         root data = {}   (D5; the status/gaps are LENS-COMPUTED, not stored)
lnk.leaseapp.<id>.applicationFor.identity.<applicantId>   # the application's applicant (later-arriving leaseapp = source)
vtx.leaseapp.<id>.signature  (aspect, written by SignLease — Q3)   { signedAt }  # the signature fact
```
`CreateLeaseApplication{ applicant: "vtx.identity.<id>", leaseAppId?: <bare NanoID> }`:
- Validates `applicant` is a live `vtx.identity.<id>` (no-orphan, FR29 — mirror service-domain's `vertex_alive` + `parts_of`).
- Mints `vtx.leaseapp.<leaseAppId|minted>` with **root data `{}`** (D5) + the `applicationFor` link.
- Accepts an optional caller-supplied bare-NanoID `leaseAppId` (the write-ahead seam, mirroring service-domain's `instanceId` — useful for the e2e in 14.5; harmless here).
- Emits `leaseapp.applicationCreated{leaseAppKey, applicant}`.
- Returns `{primaryKey: leaseapp_key}`.

> **The `applicationFor` link direction** (Contract #1 §1.1, sentence test): "leaseapp **applicationFor** identity" — the **leaseapp is the later-arriving source**, the identity pre-exists = target. ✓ Reads as "this application is *for* this applicant." The lens walks `(app)-[:applicationFor]->(id)`.

### Item C — the `leaseApplicationComplete` actorAggregate convergence lens

In `lenses.go`, **mirror `proofConvergenceSpec` (the 14.2 e2e) exactly** for the keyColumn shape:
```go
{
    CanonicalName:  "leaseApplicationComplete",
    Class:          "meta.lens",
    Adapter:        "nats-kv",
    Bucket:         "weaver-targets",          // the shared primordial convergence bucket (§10.2)
    Engine:         "full",
    Spec:           leaseApplicationCompleteSpec,
    ProjectionKind: "actorAggregate",
    Output: &pkgmgr.OutputDescriptorSpec{
        AnchorType:       "leaseapp",            // §0.D — lowercase
        OutputKeyPattern: "leaseApplicationComplete.{actorSuffix}",   // <targetId>.<entityId>
        BodyColumns:      []string{"violating", "missing_onboarding", "missing_bgcheck", "missing_payment", "missing_signature", "applicant", "entityKey"},
        EmptyBehavior:    "delete",              // or "skip"/"emptyDoc" — Q5; "delete" matches §10.2 retraction
        KeyColumn:        "entityId",            // §10.2 Option (b) — the bare-NanoID key (14.2)
        Freshness:        "auto",
        // RealnessFilter: leave UNSET — see note below (this is NOT a collect-of-rows-with-a-realness-field lens)
    },
},
```
The cypher (**one-row-per-anchor — §0.C**), reading identity aspects (14.3) + the service-instance `.outcome` aspect (14.1):
```
MATCH (app:leaseapp {key: $actorKey})
OPTIONAL MATCH (app)-[:applicationFor]->(id:identity)
OPTIONAL MATCH (id)<-[:providedTo]-(inst:service)
RETURN
  app.key AS actorKey,
  app.key AS entityKey,
  id.key  AS applicant,
  // gap predicates — scalar per-anchor; aggregate the instance fan-out with EXISTS/collect:
  (NOT <onboarding-complete predicate over id's aspects>)                       AS missing_onboarding,
  (NOT <a recent satisfying bgcheck instance exists for id>)                    AS missing_bgcheck,
  (NOT <a recent satisfying payment instance exists for id>)                    AS missing_payment,
  (NOT EXISTS(app.signature))                                                   AS missing_signature,
  <violating = OR of the above, lens-decided>                                   AS violating
```
- **Each `RETURN` column is a single scalar per the `app` anchor** — so the row count is one per `leaseapp`. The instance fan-out (`inst`) is consumed **inside** the gap predicates via `EXISTS(...)` / aggregation, **never** returned as separate rows. If the cypher grammar can't express a sub-aggregate inline, compute it with a `WITH app, id, collect(inst) AS insts` carry then a single `RETURN` — **the engine's full rule set supports `collect`/`WITH`/`EXISTS`; confirm the exact predicate forms against `internal/refractor/ruleengine/full` and the 14.2/orchestration-base cyphers (Q7).**
- **`missing_bgcheck` / `missing_payment` freshness** = "no service instance of the right family with a `.outcome.status = "completed"` and `.outcome.completedAt + window > now`" (§10.2 ~166 freshness-in-the-cypher; the `$now` param is available, as `capabilityEphemeralSpec` uses it). **The window is a literal in the cypher** (e.g. 30 days) — the freshness rule lives in the lens, not the engine (§10.2). **Discriminating bgcheck-vs-payment** = the instance's `.class` aspect value (`service.backgroundCheck.instance` / `service.payment.instance`) — the cypher reads `inst`'s `.class` aspect to bucket the gap (Q7).
- **`missing_onboarding`** = a predicate over the **identity's** aspects (e.g. name/email/phone present per the onboarding pattern's steps — Q7 for the exact predicate; align it with the `onboarding` pattern's userTask steps so closing onboarding flips this false).
- **`missing_signature`** = `NOT EXISTS(app.signature)` (the aspect `SignLease` writes — Item G).
- **`violating`** = the lens-decided OR (§10.2 ~141 — `violating` is lens-projected, *not* an implicit OR; but for this target the natural rule is "any gap true → violating"; **make it explicit in the RETURN**).
- **`applicant`** = `id.key` — the param column the playbook's `subject: "row.applicant"` / `assignee: "row.applicant"` templates reference (§10.8 ~843). **The lens MUST project every column the playbook names** (§10.2 ~146): `applicant` (for the 3 triggerLoom/assignTask subjects) + `entityKey` (for `missing_signature`'s `target: "row.entityKey"`). **Cross-check the playbook (Item D) ↔ the lens columns — a `row.<col>` with no column is a runtime data error.**
- **RealnessFilter note:** `proofConvergenceSpec`/`myTasksSpec` set `RealnessFilter: "taskKey"` because they `collect` a *list of sub-rows* and must drop null-collect artifacts. **This lens does not collect a list into a body column** — it projects scalars per anchor. So `RealnessFilter` is likely **unset** here (the realness concern is moot — there is always exactly one real `leaseapp` row). **Confirm against the actorAggregate plan (Q5/Q7):** if the descriptor *requires* a realness field for the empty-actor delete path, set it to a column that is null only when the anchor is gone (e.g. `entityKey`). **The Edge Case Hunter will probe the actor-disappearance delete path** (the leaseapp tombstoned → the row retracts) — make sure `EmptyBehavior` + `BuildKey`'s delete-path key (anchor-derived, §0.C) retract the row cleanly.

### Item D — the `meta.weaverTarget` playbook

In `targets.go`:
```go
func WeaverTargets() []pkgmgr.WeaverTargetSpec {
    return []pkgmgr.WeaverTargetSpec{{
        TargetID: "leaseApplicationComplete",          // == the lens OutputKeyPattern prefix (§10.2↔§10.8 binding)
        LensRef:  "leaseApplicationComplete",          // resolved to the lens's in-batch NanoID by the installer
        Gaps: map[string]pkgmgr.GapActionSpec{
            "missing_onboarding": {Action: "triggerLoom", Pattern: "onboarding",      Subject: "row.applicant"},
            "missing_bgcheck":    {Action: "triggerLoom", Pattern: "backgroundCheck", Subject: "row.applicant"},
            "missing_payment":    {Action: "triggerLoom", Pattern: "collectPayment",  Subject: "row.applicant"},
            "missing_signature":  {Action: "assignTask",  Operation: "SignLease", Assignee: "row.applicant", Target: "row.entityKey"},
        },
    }}
}
```
- **`TargetID` MUST equal the lens `OutputKeyPattern` prefix** (`leaseApplicationComplete`) — the §10.2↔§10.8 binding (~810). The installer validates `TargetID` is a single KV token (`[A-Za-z0-9_-]+`) and unique (`orchestrationguard.go` ~60–75).
- **Every `Gaps` key is `missing_<gap>`** and **must be a column the lens projects** (the installer validates the `missing_` convention; the engine alerts on a `missing_*: true` row column with no `gaps` entry — §10.8 ~819). The four gap keys here bind exactly to the four `missing_*` lens columns.
- **No `nudge`** (retired — §10.8). External remediation = `triggerLoom` of an externalTask pattern.
- `validateGapAction` requires: triggerLoom → `Pattern`+`Subject`; assignTask → `Operation`+`Assignee`+`Target`. All present above.

### Item E — the three `meta.loomPattern`s (the externalTask centerpiece)

In `patterns.go`:
```go
func LoomPatterns() []pkgmgr.LoomPatternSpec {
    return []pkgmgr.LoomPatternSpec{
        {
            PatternID:         "backgroundCheck",
            SubjectType:       "identity",                 // subject = the applicant identity (the triggerLoom subject)
            CompletionDomains: []string{"orchestration"},  // §0.A — externalTask completes on orchestration.externalTaskCompleted
            Steps: []pkgmgr.StepSpec{{
                Kind:       "externalTask",
                Adapter:    "backgroundCheck",             // a registered bridge adapter (the FakeBackgroundCheck in 13.4)
                InstanceOp: "CreateLeaseServiceInstance",  // Item F — mints vtx.service.<handle> (family backgroundCheck) + emits external.backgroundCheck
                ReplyOp:    "RecordLeaseServiceOutcome",   // Item F — records .outcome + emits orchestration.externalTaskCompleted
                Params:     map[string]any{"family": "backgroundCheck"},  // opaque pass-through to the instanceOp (Q4 — how the instanceOp learns the family + the template + the applicant)
            }},
        },
        {
            PatternID:         "collectPayment",
            SubjectType:       "identity",
            CompletionDomains: []string{"orchestration"},
            Steps: []pkgmgr.StepSpec{{
                Kind:       "externalTask",
                Adapter:    "stripe",                       // the FakeStripe adapter (13.4)
                InstanceOp: "CreateLeaseServiceInstance",
                ReplyOp:    "RecordLeaseServiceOutcome",
                Params:     map[string]any{"family": "payment"},
            }},
        },
        {
            PatternID:         "onboarding",
            SubjectType:       "identity",
            CompletionDomains: []string{"orchestration"},   // §10.5 ~451 — an all-userTask pattern over identity completes on orchestration
            Steps: []pkgmgr.StepSpec{
                {Kind: "userTask", Operation: "SetName",  Guard: map[string]any{"absent": "subject.name.data.value"}},
                {Kind: "userTask", Operation: "SetPhone", Guard: map[string]any{"absent": "subject.phone.data.value"}},
                // ... align steps with the missing_onboarding lens predicate (Q7) and the identity-domain ops that exist
            },
        },
    }
}
```
- **`completionDomains: ["orchestration"]` on ALL THREE** — the externalTask patterns complete via `orchestration.externalTaskCompleted` (§0.A); the onboarding userTask pattern completes via `orchestration.taskCompleted` (§10.5 ~451). **Omitting `orchestration` would make the step uncompletable** and trips the engine's load-time `externalTaskCompletionUnobservable`/`userTaskCompletionUnobservable` warn (`internal/loom/pattern.go` ~138–165).
- **The externalTask step's `Operation` MUST be empty** (the installer rejects `operation` on an externalTask — `orchestrationguard.go` ~180–185); its `Adapter`/`InstanceOp`/`ReplyOp` are required.
- **`onboarding` userTask steps' `Operation`s must be real identity-domain ops with op-metas** (the engine resolves `forOperation` live — `submitUserTask` ~895). Check which `Set*` ops identity-domain ships; if the onboarding steps reference ops that don't exist, either use the ops that do or declare op-metas. **Align the onboarding steps with `missing_onboarding`'s lens predicate** so completing onboarding actually flips the gap false (Q7).
- **The `Adapter` names (`backgroundCheck`, `stripe`) must match registered bridge adapters.** 13.4 moved `FakeBackgroundCheck` + `FakeStripe` into the bridge; confirm their registered names (`cmd/bridge/main.go` / the bridge registry) and use them verbatim — but note **14.4 does not run the bridge** (§0.G), so this only matters for 14.5; still, get the names right now.

### Item F — the externalTask wrapper DDLs (`instanceOp` + `replyOp`) — the §0.A/§0.B centerpiece

Two `meta.ddl.vertexType` DDLs in `ddls.go`. **These are the heart of the story.**

**`leaseServiceInstance` DDL** — PermittedCommands `["CreateLeaseServiceInstance"]`. The op Loom submits for an externalTask. Payload (from `submitExternalTask`, engine.go ~960–972): `{instanceKey: <bare handle>, subjectKey: <vtx.identity.<id>>, adapter, replyOp, params: {family}}`. It must:
1. Read `instanceKey` (the bare handle — validate it carries no dots/wildcards, mirror service-domain's `bare_nanoid_or_mint` shape but it's *required* here, not minted).
2. **Prepend the type**: `inst_key = "vtx.service." + instanceKey`. (The package chooses `service` as the claim-vertex type — D5/§10.5 ~480; this matches the `.outcome` aspect shape the lens reads.)
3. Read `params.family` (backgroundCheck|payment) + `subjectKey` (the applicant identity).
4. **Mint the claim vertex** `vtx.service.<handle>` exactly as 14.1's `CreateServiceInstance` does — root data `{}` (D5), `.class` aspect `service.<family>.instance`, the `providedTo` link to the applicant identity (so the lens's `(id)<-[:providedTo]-(inst)` hop finds it). **(Q4: the template / `instanceOf` link — a bgcheck/payment instance in 14.1 requires a live template via `instanceOf`. Either (a) the package seeds the templates at install and the instanceOp links to them, or (b) the externalTask instance is template-less for the demo. RECOMMENDED: seed two templates at install (one per family) and link — keeps the model faithful to 14.1's no-orphan invariant. CONFIRM — this is the one model question that touches install-time seeding, Q4.)**
5. **Emit the `external.<adapter>` event** via this op's transactional outbox: `events: [{class: "external." + adapter, data: {instanceKey: <bare handle>, adapter, replyOp, params, externalRef: <bare handle>, idempotencyKey: <bare handle>}}]`. **The event class is `external.<adapter>` (an ordinary domain — no Contract #3 change, §10.5 ~483); the body shape matches what the bridge's `externalEvent` reads (`internal/bridge/dispatch.go` ~26–44): `instanceKey`/`adapter`/`replyOp`/`params`/`externalRef`/`idempotencyKey`.** Carry the `adapter`/`replyOp` from the op payload (Loom passed them in). **This is the event that drives the bridge in 14.5 — get the body shape exactly right now (cross-check `externalEvent`).**
6. Return `{primaryKey: inst_key}`.

> **Why mint `vtx.service.<handle>` and not `vtx.leaseapp.<handle>` or a new type?** The lens reads the **service** `.outcome` aspect across `providedTo` (AC #1: "the service instance's outcome aspect"), and 14.1 already defines that aspect shape on `vtx.service.*`. So the claim vertex IS a service instance (D5/§10.5 ~480: "the lease demo uses `service.<x>.instance`"). The instanceOp re-mints the 14.1 instance shape with a caller-supplied handle. (Reusing 14.1's `CreateServiceInstance` op directly is tempting but it does **not** emit the `external.<adapter>` event — so you ship the wrapper. The *vertex shape* is identical; the *op* adds the event. Q1.)

**`leaseServiceReply` DDL** — PermittedCommands `["RecordLeaseServiceOutcome"]`. The op the **bridge** submits as `replyOp`. Payload (from `actuator.submit`, dispatch.go ~164–167): `{externalRef: <bare handle>, result: "<adapter Detail string>"}`. It must:
1. Read `externalRef` (the bare handle — required).
2. **Reconstruct** `inst_key = "vtx.service." + externalRef`.
3. Read `result` (the free-form string — store for provenance; do NOT parse for status — §0.B/Q2).
4. **Derive `status = "completed"`** (§0.B/Q2 — the bridge only replies on success; `failed` has no Phase-2 producer).
5. **Derive `completedAt = time.rfc3339_utc(op.submittedAt)`** (§0.B — the op's own timestamp; the bridge supplies no completedAt).
6. Validate the instance is alive + is a service instance + has no `.outcome` yet (mirror 14.1's `RecordServiceOutcome` guards — `vertex_alive`, `.class` ends-in `.instance`, the `.outcome`-CreateOnly once-only guarantee). **The caller (bridge) does NOT list the `.outcome` key in Reads** (it's a new write) — rely on the CreateOnly conflict for the once-only guarantee + the deterministic-requestId collapse (the bridge's `deriveReplyRequestID` makes a redelivered reply collapse on the Contract #4 tracker — FR58). **Note: the bridge submits with `authContext` omitted (dispatch.go ~52–54) under the root-equivalent bridge actor — confirm the replyOp DDL needs no `authContext.target` (the bridge actor authorizes regardless; Q6).**
7. **Write the `.outcome` aspect** `vtx.service.<handle>.outcome` class `outcome` `{status, completedAt}` (the **exact 14.1 shape**) + the OCC-guarded root touch (mirror 14.1; data stays `{}` — D5). Optionally store `result` on the aspect for provenance (`{status, completedAt, result}`) — **but keep `status`/`completedAt` as the lens-read fields**; the lens reads `.outcome.status`/`.outcome.completedAt` (Q2).
8. **Emit BOTH events** (§0.A): `orchestration.externalTaskCompleted{externalRef: <bare handle>}` (the completion signal Loom correlates — **load-bearing; without it the step never completes**) **and** optionally `service.outcomeRecorded` (provenance, mirroring 14.1 — harmless). **The `externalRef` in the orchestration event is the BARE handle** (Loom parks on `token.<handle>` and correlates `payload.externalRef`; the engine's `correlationKeys` reads `payload.externalRef` — engine.go ~722–734). **Do NOT put the full `vtx.service.<handle>` key as externalRef** — Loom minted the bare handle and parks on it (§10.6 ~563).
9. Return `{primaryKey: inst_key}`.

> **The two DDLs are a matched pair: both choose `service` as the claim-vertex type, both speak the bare handle ↔ `vtx.service.<handle>` mapping, and the replyOp's `externalRef` echo is the same bare handle the instanceOp received.** Keep them adjacent in `ddls.go` with a comment explaining the bridge-roundtrip contract (no history comment — describe the *current* contract: "instanceOp mints `vtx.service.<handle>` + emits `external.<adapter>`; the bridge replies; replyOp records `.outcome` + emits `orchestration.externalTaskCompleted`").

### Item G — the `SignLease` op (the `assignTask` target)

A DDL op (co-locate on the `leaseapp` DDL, or a small `signLease` DDL — Q3) PermittedCommands include `SignLease`. `SignLease{ leaseAppKey: "vtx.leaseapp.<id>", … }`:
- Validates the leaseapp is alive.
- Writes the **`vtx.leaseapp.<id>.signature` aspect** `{signedAt: time.rfc3339_utc(op.submittedAt)}` (D5 — the signature is a fact in an aspect; root stays minimal). **This flips `missing_signature` false** (the lens reads `EXISTS(app.signature)`).
- Emits `leaseapp.leaseSigned{leaseAppKey}`.
- **`SignLease` is the `assignTask` target** — so when Weaver detects `missing_signature`, it `CreateTask`s a task `forOperation → SignLease`, `assignedTo → applicant`, `scopedTo → the leaseapp` (§10.8 ~830). The applicant performs `SignLease` (authorized by the ephemeral grant — §10.7), which **auto-completes the task** (commit-path injection, §10.6 ~680) **and** flips `missing_signature` false. **`SignLease` MUST have an op-meta** (the `forOperation` resolution target — §3) and a permission (Q6: granted to the applicant role, or task-grant-only).

### Item H — op-metas

`OpMetas()` declares an `OpMetaSpec{OperationType: …}` for every op a step/playbook binds via `forOperation`, plus the externalTask ops for discoverability:
- **`SignLease`** — **REQUIRED** (the `assignTask` operation; the engine resolves `forOperation` to its op-meta when creating the task — `submitUserTask`/the Weaver Actuator). Missing → the task can't bind. **This is the one op-meta whose absence breaks the playbook.**
- **`CreateLeaseServiceInstance`** + **`RecordLeaseServiceOutcome`** — the engine resolves the externalTask `instanceOp`/`replyOp` by **operation name at submit** (not via a `forOperation` link), so an op-meta is **not strictly required for the engine** — but declare them anyway for discoverability + the manifest cross-check (mirroring service-domain declaring op-metas for its instanceOp/replyOp). (Q8 — confirm the engine doesn't require an op-meta for instanceOp/replyOp resolution; reading `submitExternalTask` it builds the op from the step's `InstanceOp` string directly, so no op-meta lookup — declare them for hygiene, not necessity.)
- `CreateLeaseApplication` — only needs an op-meta if something binds it via `forOperation` (nothing does — it's submitted by the installer/test directly). Optional.

### Item I — permissions

`Permissions()` grants (mirror service-domain/orchestration-base — scope `any` for orchestrator-submitted ops, granted to `operator`):
- `CreateLeaseApplication` → operator (the installer/test submits it).
- `CreateLeaseServiceInstance` → operator (Loom's `identity:loom` actor is operator-equivalent — submits the instanceOp via the relay).
- `RecordLeaseServiceOutcome` → operator (the bridge's `identity.system.bridge` actor is operator-equivalent — submits the replyOp).
- `SignLease` → **the applicant** path: granted via the **ephemeral task grant** (§10.7), so the *base* permission can be scope `self`/granted to a user-facing role, OR rely purely on the task grant. **Q6: confirm whether `SignLease` needs a standing permission grant at all, or whether the `assignTask` ephemeral grant is the sole authorization** (mirroring how `orchestration-base` handles task-granted ops). RECOMMENDED: a `self`-scoped or role-granted `SignLease` permission so a user can sign, *and* the task grant scopes it to the specific leaseapp.

### Item J — `manifest.yaml`

Mirror `service-domain/manifest.yaml`. `VerifyAgainstDefinition` cross-checks the manifest's `declares` block against the `Definition` — **DDL canonicalNames + classes, permission operationTypes + scopes + grantsTo, opMeta operationTypes, and (confirm) weaverTarget targetIds + loomPattern patternIds + lens canonicalNames.** A drift fails `lattice-pkg install`. List **every** declared meta-vertex. **Read `VerifyAgainstDefinition` (`internal/pkgmgr/`) to get the exact manifest schema for weaverTargets/loomPatterns/lenses** (service-domain's manifest has none of those, so its manifest is not a complete template for this richer package — check what the verifier expects, Q10).

---

## 3. Install-time validation the package must pass (the installer is UNCHANGED)

The installer (`internal/pkgmgr/orchestrationguard.go` + `build.go` + `definition.go`) validates the declarations **before any KV write**, fail-closed and pure. Your declarations must pass all of these (they are the contract you build to — do NOT relax them):

- **`validateCanonicalNameUniqueness`** (~31–59) — no duplicate canonicalName across DDLs/lenses/op-metas (§0.F). Globally unique vs. installed packages too.
- **`validateWeaverTargets`** (~58–90) — `TargetID` non-empty + single KV token + locally unique; every `gaps` key matches `missing_<gap>` + is a single KV token; no reserved `expectedRevision` param; `validateGapAction` (each action's required fields present).
- **`validateLoomPatterns`** (~145–215) — `PatternID` non-empty + locally unique; `SubjectType` non-empty; ≥1 step; each step kind ∈ {systemOp,userTask,externalTask} with **each kind's shape enforced exactly** (externalTask requires adapter/instanceOp/replyOp + forbids operation; userTask/systemOp require operation + forbid adapter/instanceOp/replyOp/params).
- **`validateOpMetas`** (~217+) — each OperationType non-empty + single token + locally unique.
- **`build.go` DDL self-description** — every `DDLSpec` requires `InputSchema`, `OutputSchema`, non-empty `FieldDescription`, non-empty `Examples` (the same gate 14.3 satisfied for its aspect-type DDLs). **Provide valid self-description for all four DDLs** (`leaseapp`, `leaseServiceInstance`, `leaseServiceReply`, and the `SignLease` DDL if separate). Do NOT relax the gate.
- **`LensRef` resolution** (`build.go` `resolveLensRef`) — the target's `LensRef` must resolve to a declared lens's NanoID (or a literal NanoID for an already-installed lens). `leaseApplicationComplete` is declared in this same package, so it resolves in-batch.

**None of these requires an installer change.** If a declaration can't pass a validator without changing the validator, that is a RED FLAG (Q9) — re-shape the declaration, do not edit the installer.

---

## 4. The completion-lie traps (what "looks done" but isn't)

Three ways this package can install + look complete but be silently broken — the §6 tests target each:

1. **The externalTask never completes (the §0.A trap).** If `leaseServiceReply` records the `.outcome` aspect but **forgets to emit `orchestration.externalTaskCompleted`**, the bridge reply commits, the outcome aspect lands, the lens even reprojects `missing_bgcheck` false — but **Loom's step never advances** (it's parked on `token.<handle>`, the deadline disarmed, and no completion event carried `payload.externalRef`). The pattern wedges; `CompletePattern` never fires. This is invisible in a lens-only test. **§6 test 4 asserts the replyOp emits `orchestration.externalTaskCompleted` with the bare-handle `externalRef`** — the only test that catches it.
2. **The lens fails closed (the §0.C trap).** If the cypher fans out (returns one row per service instance instead of `collect`ing to one row per anchor), `guardOutputKeyCollision` → Terminal → DLQ. The lens "installs" but **projects nothing** (every projection terminates). **§6 test 1 asserts exactly one row per `leaseapp` anchor** under a multi-instance fixture (a leaseapp whose applicant has ≥2 service instances) — the case that fans out.
3. **A `row.<col>` the playbook names that the lens doesn't project (the §10.2↔§10.8 seam).** If the playbook references `row.applicant` / `row.entityKey` but the lens omits the `applicant` / `entityKey` column, Weaver hits a null-template **data error** at dispatch (a malformed remediation — §10.8 ~843). **§6 test 6 (or a static assertion) cross-checks every `row.<col>` in the playbook against the lens `BodyColumns`.**

---

## 5. Forward fit (note, do NOT build)

14.4 ships the package + proves the seams via direct writes; **14.5** drives it end-to-end through the live bridge:

- **14.5 (e2e + `test-lease-convergence` gate)** — installs `lease-signing` on a minimal core, creates a fresh lease application with all gaps violating, runs orchestration (Weaver → `triggerLoom` onboarding + bgcheck/payment `externalTask` → **the live bridge** → `replyOp` reprojects → temporal freshness → sign task), and a **drain-then-assert** harness observes `violating` flip false and **stay** false. It proves the **retried external call does not double-act** (FR58 through the bridge) and asserts the outcome lives in an aspect (D5, gate-enforced). **A new `test-lease-convergence` CI gate** is added. **Green here unblocks 13.5** (retire the nudge). **14.4 does NOT build any of this** — its tests stop at direct-write convergence (§0.G).
- **The one design choice that matters for 14.5:** the `external.<adapter>` event body shape (Item F.5) **must exactly match** the bridge's `externalEvent` reader, and the `replyOp`'s `externalRef` echo + `deriveReplyRequestID` collapse **must** make a redelivered bridge reply idempotent. **Get these right in 14.4** (cross-check `internal/bridge/dispatch.go`); a shape mismatch surfaces only in 14.5's live e2e, far from here. (§6 test 3/4 assert the event body shape against the bridge reader's fields without running the bridge.)

---

## 6. Tests (the convergence proof + the externalTask-completion trap + the D5 assertions + the install round-trip) — first-class

Mirror the **production `InstallPackage` → Processor → commit → read-back** harness in `packages/service-domain/service_instance_test.go` + `packages/identity-domain/record_pii_test.go` (`testutil.SetupPackageTestEnv` / `RunMetaInstallPipeline` / `CapabilityPipeline` / `SeedCapDoc`; embedded NATS, no Docker), and the **Refractor projection** harness in `internal/refractor/refractor_keycolumn_convergence_e2e_test.go` (install a keyColumn lens → drive the source → assert the `weaver-targets` row) for the lens layer. **The package tests are the centerpiece — they prove the shipped package, not a fixture.** Install the dependency chain (rbac + identity + orchestration-base + **service-domain** + **lease-signing**).

### Required tests

1. **`TestLeaseApplicationComplete_ProjectsOneRowPerAnchor` (the convergence lens — AC #1 + §0.C; AC #4 direct-write).** Install the chain; create an applicant identity (+ its 14.3 aspects), `CreateLeaseApplication`, and **≥2 service instances** for that applicant (one bgcheck, one payment) via `CreateServiceInstance` (or your instanceOp). Drive the `leaseApplicationComplete` lens through the live Refractor pipeline (mirror the 14.2 e2e). Assert: (a) **exactly one** `weaver-targets` row under `leaseApplicationComplete.<leaseAppId>` (the bare-NanoID key — §0.C; the multi-instance fixture is the fan-out case the guard would trip); (b) the row carries `entityKey == vtx.leaseapp.<id>`, `applicant == vtx.identity.<id>`, and the four `missing_*` columns + `violating` with the expected values for the all-gaps-open state; (c) **the row key is the bare NanoID** (no `leaseapp.` type prefix — the keyColumn shape).
2. **`TestLeaseApplicationComplete_OutcomeAspectFlipsGap_DirectWrite` (freshness reprojection — AC #1 + AC #4 + D5).** From the all-gaps-open state, **directly write** a bgcheck instance's `.outcome` aspect `{status: "completed", completedAt: <now>}` (via your `RecordLeaseServiceOutcome` replyOp op with a synthetic `{externalRef, result}` payload — **AC #4: no live bridge**). Assert the lens **reprojects** `missing_bgcheck` → false (the linked-constituent reprojection — AC #1). Assert the `.outcome` aspect carries status+completedAt **and** the `vtx.service.<id>` / `vtx.leaseapp.<id>` root `data` stays minimal (**D5** — invariant b). (Optionally assert a *stale* outcome — `completedAt` older than the window — leaves `missing_bgcheck` true, exercising the freshness predicate.)
3. **`TestLeaseServiceInstance_MintsClaimVertex_EmitsExternalEvent` (the instanceOp — §0.B/Item F).** Submit `CreateLeaseServiceInstance{instanceKey: <bare handle>, subjectKey: <applicant>, adapter: "backgroundCheck", replyOp: "RecordLeaseServiceOutcome", params: {family: "backgroundCheck"}}` through the pipeline. Assert: (a) `vtx.service.<handle>` is minted (root data `{}` — D5) with `.class = service.backgroundCheck.instance` + the `providedTo` link to the applicant; (b) an `external.backgroundCheck` event was emitted whose **payload matches the bridge's `externalEvent` shape** (`instanceKey`/`adapter`/`replyOp`/`externalRef`/`idempotencyKey` all == the bare handle) — read the committed `…events` outbox aspect or the published event (mirror how service-domain tests assert emitted events).
4. **`TestLeaseServiceReply_RecordsOutcome_EmitsExternalTaskCompleted` (the replyOp — THE §0.A trap; AC #3).** Pre-create a `vtx.service.<handle>` instance. Submit `RecordLeaseServiceOutcome{externalRef: <handle>, result: "background-check cleared"}` (the **bridge's** payload shape — §0.B). Assert: (a) the `.outcome` aspect `vtx.service.<handle>.outcome` is written with `status == "completed"` + a canonical-UTC `completedAt` (derived from the op, §0.B); (b) **the op emits `orchestration.externalTaskCompleted` carrying `payload.externalRef == <handle>` (the BARE handle, not the full key)** — **this is the load-bearing AC #3 assertion; without the event the externalTask never completes (§0.A/§4 trap #1)**; (c) the `leaseapp`/`service` root `data` stays minimal (D5); (d) a **second** `RecordLeaseServiceOutcome` for the same handle is rejected (the once-only `.outcome` CreateOnly guard) — the FR58 redelivery defense at the DDL layer.
5. **`TestLeaseSigning_InstallRoundTrip_PlaybookAndPatternsValidate` (install — §3).** Assert `InstallPackage(lease-signing)` succeeds with the lens + the weaverTarget + the three loomPatterns + the four DDLs + the op-metas present, and that the install batch contains the expected meta-vertices (the lens `meta.lens`, the target `meta.weaverTarget` + `.spec`, the patterns `meta.loomPattern` + `.spec`). This pins the §3 validators (a malformed playbook/pattern fails here, not at CDC load). (May fold into test 1's setup.)
6. **`TestLeaseSigning_PlaybookColumnsMatchLens` (the §10.2↔§10.8 seam — §4 trap #3).** A **static/unit** assertion (no pipeline): every `row.<col>` token in the `WeaverTargets()` playbook (`applicant`, `entityKey`) is a member of the `Lenses()[leaseApplicationComplete].Output.BodyColumns`, and every `gaps` key is a `missing_*` column the lens projects. Catches a drift between the playbook and the lens cheaply.
7. **`TestLeaseAppType_AbsentFromCore` (invariant a — mirror `service-domain/type_agnostic_test.go`).** Walk `internal/` and assert the `leaseapp` class string + the `lease-signing` op names do **not** appear in `internal/*` engine code (the concrete types live ONLY in the package; invariant a — §1). (Narrow the grep like service-domain's, to avoid false positives on the word "lease" in comments.)
8. **`TestSignLease_FlipsMissingSignature` (Item G — AC #2 closure).** `CreateLeaseApplication`, assert `missing_signature` true; submit `SignLease{leaseAppKey}`; assert the `.signature` aspect is written (D5) and the lens reprojects `missing_signature` → false. (Proves the `assignTask` target op closes its gap.)
9. **Regression — the dependency packages' tests are UNTOUCHED.** `packages/service-domain/...`, `packages/identity-domain/...`, `packages/orchestration-base/...`, `internal/loom/...`, `internal/refractor/...`, `internal/pkgmgr/...` must still pass unchanged (14.4 ships package content + uses existing seams; it changes none of them). If your work forces an edit to any of these, that is a smell — stop and check (it likely means you reached for an engine/installer change — Q9).

### Test posture

The package + lens tests use the production `InstallPackage` → Processor → commit harness + the live Refractor pipeline (embedded NATS, no Docker) — so the **install round-trip + the projection are genuinely proven** (a missed event emission fails test 3/4, not review; a fan-out cypher fails test 1). **AC #4 is honored throughout — all outcome writes are direct (`RecordLeaseServiceOutcome` with a synthetic payload), never via a live bridge process** (that is 14.5). Flake retry per Deviation 14 is allowed; a flake claim without a re-run is a drift signal. **Run Gate 2 + Gate 3** (§8) — this package authors orchestration content on the security/projection plane (a new vertex type, new ops, a convergence lens, a playbook), and the gates confirm no bypass/capability regression.

---

## 7. Required reading (DS does the deep reads; do not expect them pre-loaded)

- **THE SEAMS (read these first — they govern §0):**
  - `internal/bridge/dispatch.go` (`handleExternal` ~86–181, `externalEvent` ~26–44) + `actuator.go` (`submit` ~55–76) + `token.go` (`deriveReplyRequestID`) — **the §0.B reply shape `{externalRef, result}` + the `external.<adapter>` event body the instanceOp must produce.** Internalize that the bridge supplies no status/completedAt and reads `externalEvent.{instanceKey,adapter,replyOp,externalRef,idempotencyKey}`.
  - `internal/loom/engine.go` `submitExternalTask` (~954–990) + `onExternalTaskDeadline` (~1267–1302) + `correlationKeys` (~722–734) + `pattern.go` (`Step` ~28–48, `validate` ~181–230, `externalTaskCompletionUnobservable` ~155–165) — **the instanceOp payload Loom sends, the bare-handle parking, the `payload.externalRef` correlation, the deadline-disarm.**
  - **Contract #10 §10.5 (~412–533) + §10.6 (~537–670) + §10.2 (~84–167) + §10.8 (~787–894)** IN FULL — the externalTask completion model (§0.A), the keyColumn (§0.C), the playbook (Item D). **Plus the revision-history rows ~979–980** — the source of truth for the externalTask deadline+completion correction (epics AC#3 supersession).
- **THE DEPENDENCIES YOU CONSUME (read; do NOT edit):**
  - `packages/service-domain/ddls.go` IN FULL — the `service` instance shape, the `.outcome` aspect, the `instanceId` seam, the `CreateServiceInstance`/`RecordServiceOutcome` guards (your wrapper DDLs mirror these). `package.go` + `manifest.yaml` + `service_instance_test.go` + `type_agnostic_test.go` (the test + invariant-a templates).
  - `internal/refractor/refractor_keycolumn_convergence_e2e_test.go` — **the exact actorAggregate keyColumn lens shape** (`proofConvergenceSpec` + the `OutputDescriptorSpec{…, KeyColumn:"entityId"}`) your lens copies. `internal/refractor/projection/output.go` (`KeyColumn`/`BuildKey` ~50–202) + `internal/refractor/pipeline/evaluate.go` (`guardOutputKeyCollision` ~239–273 — the §0.C fail-closed guard).
  - `packages/orchestration-base/lenses.go` (`myTasksSpec`/`capabilityEphemeralSpec` — `collect(DISTINCT)`/`$now`/`OPTIONAL MATCH` idioms) + `ddls.go` (`taskDDLScript` — the Starlark idioms) + `loom_lifecycle.go` (`StartLoomPattern`/the lifecycle DDL — what `triggerLoom` submits) + `manifest.yaml`.
  - `packages/identity-domain/ddls.go` + `README.md` — the `Set*` ops the onboarding userTask pattern binds + the `name`/`email`/`phone`/`ssn`/`dob` aspects the lens reads (14.3).
- **THE INSTALLER CONTRACT (read; do NOT edit):** `internal/pkgmgr/definition.go` (`Definition`/`WeaverTargetSpec`/`GapActionSpec`/`LoomPatternSpec`/`StepSpec`/`OpMetaSpec`/`OutputDescriptorSpec` + `validateCanonicalNameUniqueness`) + `internal/pkgmgr/orchestrationguard.go` (the §3 validators) + `internal/pkgmgr/build.go` (the DDL self-description gate + `resolveLensRef` + the meta-vertex emission) + `VerifyAgainstDefinition` (the manifest cross-check — Item J/Q10).
- **THE ENGINE THAT CONSUMES THE PLAYBOOK (read for the dispatch-time contract; do NOT edit):** `internal/weaver/strategist.go` (`buildPlan` — how `row.<col>` templates resolve + the action dispatch) + the Weaver registry (how `meta.weaverTarget` loads). Confirms the `applicant`/`entityKey` columns the playbook needs.
- **THE GROUNDING (read; build TO; do NOT edit):** Contract #1 §1.1 (`docs/contracts/01-key-shapes.md` — the lowercase type segment + the link sentence rule); **D5** (`_bmad-output/planning-artifacts/lattice-architecture.md` ~1167); the epics §14 (`phase-2-epics.md` ~662–733) — but **AC#3 is stale (§0.A)**.
- **HOUSE RULES:** `CLAUDE.md` — esp. NO history/changelog comments in code (no `// Story 14.4 …`, `// Previously …`, `// replaces RecordServiceOutcome …`); aspect key-shape `vtx.leaseapp.<id>.signature` / `vtx.service.<id>.outcome`; link sentence test (`leaseapp applicationFor identity`); new docs → README/`/docs`, not `_bmad-output/`.

---

## 8. Verification gates (run before handing back; record each + result in the closing summary)

- `go build ./...` — includes `packages/lease-signing` + the deps it imports.
- `make vet`
- `golangci-lint run ./...`
- `make verify-kernel` — **no kernel-topology change** is made (this is a package, not a primordial bucket/identity), but run it to prove no regression (the stack must come up; requires `make up`). (`weaver-targets` is already primordial — §10.2; the package projects into it, it does not create it.)
- **`go test ./packages/lease-signing/... -count=1`** — **the story's centerpiece:** the convergence-lens one-row-per-anchor proof (test 1), the direct-write reprojection + D5 (test 2), the instanceOp claim-vertex + external-event proof (test 3), **the replyOp outcome + `orchestration.externalTaskCompleted` proof (test 4 — the §0.A trap)**, the install round-trip (test 5), the playbook↔lens column check (test 6), the invariant-a type-absence (test 7), the SignLease gap-closure (test 8).
- **`go test ./packages/service-domain/... ./packages/identity-domain/... ./packages/orchestration-base/... -count=1`** — the dependency packages' tests **still pass unchanged** (regression — §6 test 9).
- **`go test ./internal/refractor/... ./internal/loom/... ./internal/pkgmgr/... -count=1`** — the engines/installer the package rides are **untouched** and still pass (regression). A failure here means you reached for a core change (Q9).
- **`make test-bypass` (Gate 2 — all BLOCKED)** — this package authors a new vertex type + ops + a convergence lens on the projection/security plane; run it to confirm the new ops/lens open no bypass (they ride the existing guarded commit + projection paths). Expect all BLOCKED.
- **`make test-capability-adversarial` (Gate 3 — all DEFENDED)** — the capability plane; the new ops carry permissions + the `SignLease` task-grant path. Run it to confirm no capability regression. Expect all DEFENDED.
- **NOT in scope:** the `test-lease-convergence` gate (that gate is **created in 14.5**) — do not add or run it here.
- The full **3-layer adversarial review** is Winston's gate (Blind Hunter / Edge Case Hunter / Acceptance Auditor) per `bmad-code-review` — the Epic-14 integration centerpiece earns the full 3-layer. The **Acceptance Auditor** checks all four ACs + the §0.A epics-supersession (the replyOp emits `orchestration.externalTaskCompleted`, `completionDomains: ["orchestration"]`) + the §10.2↔§10.8 column seam + the D5 root-minimal claim; the **Edge Case Hunter** probes the **one-row-per-anchor guard** (a multi-instance fixture — §0.C), the **actor-disappearance delete path** (leaseapp tombstoned → row retracts), the **once-only `.outcome`** (FR58 redelivery), the **bare-handle vs full-key `externalRef`** (§0.B), the **freshness window edge** (stale outcome leaves the gap true), and the **null-`row.<col>`** data error; **Blind Hunter** on the diff. **Note it in your summary.**

**Why Gate 2 + Gate 3 run here:** the package introduces a new vertex type, four ops, a convergence lens, and a playbook on the projection/capability plane — the gates confirm the new surface holds the bypass/capability boundary. (If you judge a gate genuinely does not exercise the change, say so explicitly so it can be overridden — but default to running both.)

---

## 9. If too large / a split

This story is **medium–large** for a single package (four DDLs + a lens + a playbook + three patterns + op-metas + permissions + a manifest + a README + ~9 tests), but it is **one coherent vertical** and the seams are interdependent (the lens columns bind the playbook; the instanceOp/replyOp are a matched pair; the patterns reference both). **Prefer the single pass.** The natural (but unnecessary) seam, if the externalTask wrapper DDLs prove fiddly, would be **14.4a** = the `leaseapp` type + `CreateLeaseApplication` + the convergence lens + `SignLease` + the `assignTask`/`triggerLoom(onboarding)` playbook gaps + tests 1/2/7/8 (the **detection + the non-external remediation** — provable entirely via direct writes), **14.4b** = the two externalTask wrapper DDLs + the bgcheck/payment patterns + those two playbook gaps + tests 3/4/5/6 (the **external remediation seam** — the §0.A/§0.B centerpiece). But the playbook is one vertex (all four gaps together) and the install round-trip proves the whole declaration set at once, so the split adds coordination cost for little gain. **If split, land 14.4a first** (it makes the lens + the non-external convergence real), then 14.4b (the external arms). **Do not split the lens from its playbook** (the column seam, §4 trap #3, must be tested together).

---

## 10. Open Questions (assumptions made autonomously — Winston to confirm; Q1/Q2/Q4/Q7 are the load-bearing ones)

These are the decisions taken while drafting (the create-story ran autonomously). Each carries a **recommendation**; the dev proceeds on the recommendation unless Winston overrides. **Q1, Q2, Q4, and Q7 most warrant Winston's eye** — they are the places the existing code/contract under-specifies the seam.

- **Q1 — the externalTask `instanceOp`/`replyOp` are NEW package-local wrapper DDLs (`CreateLeaseServiceInstance` / `RecordLeaseServiceOutcome`), NOT 14.1's `CreateServiceInstance` / `RecordServiceOutcome`.** RECOMMENDED + assumed (§0.B / Item F). **Why:** 14.1's `CreateServiceInstance` does **not** emit the `external.<adapter>` event, and 14.1's `RecordServiceOutcome` (a) takes a full `vtx.service.<id>` `instanceKey` (the bridge supplies a bare `externalRef`), (b) requires caller `status`+`completedAt` (the bridge supplies neither), and (c) emits `service.outcomeRecorded`, **not** `orchestration.externalTaskCompleted` — so it would never complete the Loom step. Reusing them would require editing service-domain (a frozen-as-shipped dependency this story must not edit). **The considered alternative** — *extend* service-domain's ops to optionally emit the external/completion events — is rejected: it couples the generic service domain to the externalTask completion protocol (a Loom concern), violating the layering, and edits a DONE story. **Default: ship the two wrapper DDLs in `lease-signing`, reusing the 14.1 `.outcome` aspect *shape* (D5 fidelity).** **Confirm Winston agrees the wrapper-op design is correct (vs. a service-domain amendment).**
- **Q2 — the `replyOp` derives `status = "completed"` always + `completedAt = op.submittedAt`; it stores `result` verbatim and does NOT parse it for pass/fail.** RECOMMENDED + assumed (§0.B). The bridge posts only `{externalRef, result}` (a free-form string) and only on adapter *success* (an adapter error is Nak+retry, never a reply), so a `failed` outcome has **no producer** on the Phase-2 bridge path. Parsing the free-form `result` for pass/fail is brittle (the real verification is the adapter's Phase-3 job). **Confirm:** (a) `status = "completed"` on every reply is the right demo simplification (vs. threading a structured status from the adapter — a bridge/adapter change out of scope here); (b) `completedAt = op.submittedAt` (vs. a bridge-supplied timestamp — the bridge supplies none). **Default: `status="completed"`, `completedAt=op.submittedAt`, `result` stored for provenance.** (This is the one place the demo deliberately simplifies; flag it loudly in the README + summary so 14.5/Phase-3 knows where `failed` would plug in.)
- **Q3 — `SignLease` co-locates on the `leaseapp` DDL (one DDL, two ops `CreateLeaseApplication` + `SignLease`); the signature is a `vtx.leaseapp.<id>.signature` aspect.** RECOMMENDED + assumed (Item G). Keeps the leaseapp's two ops together (mirrors how identity-domain co-locates ops on one DDL). **Confirm:** (a) co-locate vs. a separate `signLease` DDL; (b) the signature as an aspect (D5 — recommended) vs. a root scalar. **Default: co-located, aspect.**
- **Q4 — the externalTask claim vertex is a `service.<family>.instance` linked `providedTo` the applicant; the two service *templates* (backgroundCheck, payment) are seeded at install and the instanceOp links the instance `instanceOf` the right template.** RECOMMENDED + assumed (Item F.4). **Why:** 14.1's `CreateServiceInstance` requires a live template (the no-orphan `instanceOf` invariant); to keep the wrapper instanceOp faithful to that shape, the package must seed templates. **The alternative** — a template-less externalTask instance (relax the no-orphan link for the demo) — is simpler but diverges from 14.1's model and the lens's `(id)<-[:providedTo]-(inst)` hop still works either way. **Confirm:** (a) seed two templates at install (recommended — faithful) vs. template-less instances; (b) **HOW the instanceOp learns which template + the applicant** — the Loom step's `params: {family}` carries the family, but the **applicant** is the pattern *subject* (`subjectKey` in the instanceOp payload) and the **template** must be resolved from the family (either a known-key read of a seeded-template registry, or the instanceOp mints template-less). **This is the one model question that touches install-time seeding — Winston should weigh in.** **Default: seed two templates; instanceOp links instanceOf the family's template + providedTo the subject identity.** *(If template-seeding-at-install is awkward in the package model, the template-less variant is the fallback — note it.)*
- **Q5 — the lens `EmptyBehavior: "delete"` + likely **no** `RealnessFilter`.** RECOMMENDED + assumed (§2 Item C). §10.2 retraction is "true entity deletion → row deleted" (~163), so `delete` matches. The lens projects scalars per anchor (not a collect-of-sub-rows), so the `RealnessFilter` (which drops null-collect artifacts) is likely moot. **Confirm:** (a) `delete` vs `skip`/`emptyDoc` for the empty/gone-actor case; (b) whether the actorAggregate plan *requires* a `RealnessFilter` for the delete path (if so, set it to `entityKey`). **Default: `delete`, no realnessFilter — but verify against the actorAggregate plan + the delete-path test (test 1's retraction leg).**
- **Q6 — `SignLease` authorization: a standing permission (scope `self` or granted to a user-facing role) PLUS the `assignTask` ephemeral grant scoping it to the specific leaseapp; the bridge-submitted `replyOp` needs no `authContext.target`.** RECOMMENDED + assumed (Item I). **Confirm:** (a) does `SignLease` need a standing permission, or is the ephemeral task grant the sole authorization (mirror how orchestration-base handles task-granted ops); (b) confirm the bridge's root-equivalent actor authorizes `RecordLeaseServiceOutcome` with no target (the bridge omits authContext — dispatch.go ~52). **Default: a `self`/role `SignLease` permission + the task grant; replyOp needs no target.**
- **Q7 — the exact lens predicate forms (`missing_onboarding` over identity aspects; `missing_bgcheck`/`missing_payment` discriminated by the instance `.class` + a freshness window; `violating` = OR of gaps) are author-resolved against the `full` rule engine's grammar.** RECOMMENDED but **load-bearing + under-specified** (§2 Item C). The cypher must express: a per-anchor scalar gap for each family (reading the instance `.class` aspect to bucket bgcheck-vs-payment + a `completedAt + window > now` freshness compare), an onboarding predicate over identity aspects **aligned with the onboarding pattern's userTask steps** (so closing onboarding flips the gap), and the one-row-per-anchor `collect`/`WITH`/`EXISTS` aggregation (§0.C). **Confirm:** the dev must validate the exact predicate forms against `internal/refractor/ruleengine/full` + the 14.2/orchestration-base cyphers (what `EXISTS`/`collect`/`WITH`/aspect-navigation the engine supports) — **this is where the lens is genuinely under-specified by the existing code and most likely to need iteration.** **Default: the §2 Item C skeleton; the dev fills the predicate bodies against the engine grammar and pins them with test 1/2.**
- **Q8 — op-metas are declared for `CreateLeaseServiceInstance` / `RecordLeaseServiceOutcome` for hygiene, though the engine resolves the externalTask `instanceOp`/`replyOp` by name (not via `forOperation`), so they are not strictly required.** RECOMMENDED + assumed (Item H). `SignLease`'s op-meta **is** required (the `assignTask` `forOperation` target). **Confirm:** the engine indeed builds the externalTask ops from the step strings directly (reading `submitExternalTask` — no op-meta lookup), so the instanceOp/replyOp op-metas are discoverability-only. **Default: declare all three op-metas; only `SignLease`'s is functionally required.**
- **Q9 — ZERO engine/installer change.** RECOMMENDED + assumed (§0.E). Every seam exists upstream. **A proposed `internal/pkgmgr`/`internal/loom`/`internal/refractor`/`internal/weaver`/`internal/bridge` change is a RED FLAG — surface it as blocking, do not implement.** **Default + expected: package content only.** (If a genuine gap exists — e.g. the installer can't express a needed declaration — it is a CONTRACT-AMENDMENT-REQUEST or a core-story spin-off, flagged at the top of the summary, not an in-flight edit.)
- **Q10 — the `manifest.yaml` declares the full richer set (lens + weaverTarget + loomPatterns + opMetas + permissions + DDLs) per `VerifyAgainstDefinition`.** RECOMMENDED + assumed (Item J). service-domain's manifest has no lens/target/pattern, so it is not a complete template. **Confirm:** read `VerifyAgainstDefinition` to get the exact manifest schema for weaverTargets/loomPatterns/lenses (which fields it cross-checks) and mirror it. If the verifier does **not** cross-check those richer kinds, the manifest only needs the kinds it does check (DDLs/permissions/opMetas) — but declare them anyway for documentation. **Default: full declaration matching whatever `VerifyAgainstDefinition` enforces.**

---

## Dev Agent Record

### Agent Model Used

Amelia (dev) — claude-opus-4-8.

### Debug Log References

- Lens cypher one-row-per-anchor + gap-bool design validated empirically against
  `internal/refractor/ruleengine/full` (throwaway tests, removed) before authoring:
  the working shape folds family + completed into
  `count(DISTINCT CASE WHEN … THEN inst.key ELSE null END)` so the OPTIONAL MATCH
  carries no filtering WHERE (a filtering WHERE that removes the only match
  collapses the anchor to null in the grouped projection — the documented
  `myTasks` behavior). Each gap bool is `count = 0`; `entityKey`/`applicant` stay
  non-null even with gaps open.
- Scalar-column projection blocker confirmed empirically (throwaway e2e through the
  live `InstallActorAggregate` pipeline, removed): a scalar `BodyColumn` projects
  as `[]` through the actorAggregate `EnvelopeFn` realness filter.
- Two install-validation collisions found + fixed during integration: (1) the claim
  vertex cannot use class `service` (service-domain's `service` DDL restricts that
  class's permittedCommands) → mint with class `leaseServiceInstance`, keep key type
  `service`; (2) the replyOp's OCC root-touch on the `leaseServiceInstance`-class
  vertex requires `RecordLeaseServiceOutcome` in that DDL's permittedCommands (the
  step-6 gate is keyed by the mutated vertex's class).

### Completion Notes List

**Built (package content only — zero `internal/**` change):** `packages/lease-signing/`
— the `leaseapp` vertex DDL (`CreateLeaseApplication` + `SignLease`), the two
externalTask wrapper DDLs (`CreateLeaseServiceInstance` instanceOp /
`RecordLeaseServiceOutcome` replyOp), the `leaseApplicationComplete` actorAggregate
convergence lens, the §10.8 playbook, the three loomPatterns
(backgroundCheck/collectPayment/onboarding), op-metas, permissions, manifest, README.
Registered the package (+ its prereqs orchestration-base/service-domain) in
`cmd/lattice-pkg/main.go` (a `cmd/` registry edit, not `internal/`).

**Winston's adjudications honored:** Q1 (two package-local wrapper DDLs); Q2
(replyOp derives `status="completed"` + `completedAt=time.rfc3339_utc(op.submittedAt)`,
stores `result` verbatim — flagged loudly in README + here); **Q4 stayed
TEMPLATE-LESS** (no `instanceOf`; the lens hops `providedTo` and buckets family via a
`.family` aspect — no gate required the template link); Q7 (cypher filled against the
`full` grammar, pinned one-row-per-anchor with a multi-instance fixture); Q3/Q5/Q6/Q8/Q10
on defaults.

**replyOp completion wiring (§0.A trap):** the replyOp emits
`orchestration.externalTaskCompleted{externalRef: <bare handle>}` (the bare handle, not
the full vtx key) AND records the `.outcome` aspect; the patterns declare
`completionDomains:["orchestration"]`. Test 4 asserts the event with the bare handle +
the once-only `.outcome` CreateOnly guard.

**One-row-per-anchor cypher:** required MATCH on the `leaseapp` anchor; OPTIONAL
`applicationFor`→identity and `providedTo`→service (NO filtering WHERE on the optionals);
`WITH … count(DISTINCT CASE WHEN <family + completed> THEN inst.key ELSE null END) AS …`;
RETURN turns each count into a strict bool (`= 0`) — Weaver's `boolColumn` needs a Go
bool. `violating` = explicit OR of the four gaps.

**🚩 Q9 RED FLAG (BLOCKING — surfaced, NOT implemented):** the §10.2
convergence-lens-as-actorAggregate (E5, ratified+applied via 14.2) closed the **key**
seam but NOT the scalar **body** seam. The actorAggregate projection `EnvelopeFn`
(`internal/refractor/projection/output.go` + `driver.go`) realness-filters **every**
`BodyColumn` to a list — a scalar `violating`/`missing_*`/`entityKey`/`applicant`
projects as `[]`, which Weaver's `boolColumn` cannot read. Confirmed empirically against
the live projection pipeline. No package-only workaround exists (a plain lens keeps
scalars but loses the linked-constituent reprojection that is the whole point of AC#1).
Needs a Refractor change (a per-column scalar/passthrough mode on the Output descriptor).
**Filed as `cmd/refractor/CONTRACT-AMENDMENT-REQUEST.md` Request E6**; the lens
declaration here is correct + ready for the moment it lands. The lens **cypher** is
proven one-row-per-anchor + gap-flipping at the rule-engine level (tests 1/2/8).

**Other deliberate notes:** (a) freshness is "a completed outcome exists", NOT a rolling
`completedAt+window>now` window — the `full` engine has no date arithmetic, the projection
supplies only `$now`, and the Starlark sandbox has no duration-add for the replyOp to
precompute an `expiresAt`; flagged in README as a Phase-3 refinement. (b) the claim vertex
is key type `service` (so the lens anchors on it + reads the 14.1 `.outcome` shape) but
class `leaseServiceInstance` (to avoid service-domain's `service`-class permittedCommands);
the `.family` aspect is the lens's bgcheck/payment discriminator because the vertex envelope
`class` field shadows the `.class` aspect on the projection read path. (c) onboarding binds
the real `RecordIdentityPII` op (lease-signing declares its op-meta); `missing_onboarding` =
the applicant has no `.ssn` aspect. (d) invariant-a (type-agnostic engines) is consumed, not
re-proven (Epic 13 did that); test 7 keeps `leaseapp`/op tokens out of `internal/`.

**Gates (all green):** `go build ./...` OK; `make vet` OK; `golangci-lint run` 0 issues;
`go test ./packages/lease-signing/...` 12/12 PASS (the §6 tests 1–8 + negative paths);
regression `go test ./internal/pkgmgr ./internal/loom ./internal/refractor/...
./packages/{service-domain,identity-domain,orchestration-base}` all PASS; fresh stack
`make down && make up && make verify-kernel` PASS; **live install of `packages/lease-signing`
through `./bin/lattice-pkg install` committed clean (writeCount=55, no canonicalName
collision against the shared kernel)**; `make verify-package-identity` (70 OK) +
`make verify-package-identity-hygiene` (31 OK) PASS with lease-signing co-installed (no
cross-package regression); Gate 2 `make test-bypass` PASS; Gate 3
`make test-capability-adversarial` PASS (6/6, 5 DEFENDED + 1 ACCEPTED-WINDOW).

**Remaining open question for Winston:** the E6 scalar-column Refractor gap (above) — the
convergence lens cannot project a Weaver-readable row until E6 lands. Everything else (the
externalTask seams, the install round-trip, the lens cypher, the playbook) is complete and
proven.

### File List

- `packages/lease-signing/package.go` (new)
- `packages/lease-signing/ddls.go` (new)
- `packages/lease-signing/scripts.go` (new)
- `packages/lease-signing/lenses.go` (new)
- `packages/lease-signing/targets.go` (new)
- `packages/lease-signing/patterns.go` (new)
- `packages/lease-signing/permissions.go` (new)
- `packages/lease-signing/manifest.yaml` (new)
- `packages/lease-signing/README.md` (new)
- `packages/lease-signing/lease_signing_test.go` (new)
- `packages/lease-signing/lens_unit_test.go` (new)
- `packages/lease-signing/lens_cypher_test.go` (new)
- `cmd/lattice-pkg/main.go` (modified — registered lease-signing + orchestration-base + service-domain)
- `cmd/refractor/CONTRACT-AMENDMENT-REQUEST.md` (modified — added Request E6: scalar convergence columns)
