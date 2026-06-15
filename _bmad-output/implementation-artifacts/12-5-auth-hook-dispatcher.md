# Story 12.5: Generic step-3 auth-hook dispatcher (D-CONSUMER)

Status: review

<!-- Note: Validation is optional. Run validate-create-story for quality check before dev-story. -->

> **Security-critical — the auth hot path.** Full 3-layer adversarial review + Gate 2 (BLOCKED) + Gate 3 (DEFENDED) follow dev. They are not optional.

## Story

As the platform security owner,
I want step-3 to dispatch over a **data-configured registry of grant-matchers** instead of a hardcoded `switch` that names `task`/`service`/`platform`,
so that the *consumer* (read) side stops naming each grant type — symmetric to the now-data-driven projection (write) side — and a package can relocate its grant projection to a disjoint Capability-KV key with **zero core edits**.

## ⭐ Scope framing — READ THIS FIRST (the single most important thing to get right)

This story is a **behavior-preserving refactor of the step-3 read side** into a generic dispatcher. It does **not** decompose the god-cypher (that is 12.6/12.7), it does **not** add new keys to production (the seed registry reads the same `cap.ephemeral.<actor>` / `cap.<actor>` keys it reads today), and it does **not** change a single auth outcome.

**The four pins that, if gotten wrong, cost a whole story (these are party-review-pinned — see decision record finding #8):**

1. **Packages are DATA, not Go/plugins.** Do **NOT** design a Go-plugin / interface-registration mechanism where a package ships matcher code. Lattice packages are cypher + Starlark + manifest **data**. The model is: **core owns a FIXED set of matcher *kinds*** (the three existing logics stay in core Go), and a package **DECLARES, as install-time data**, which matcher kind authorizes its grant type, which disjoint Capability-KV key that path reads, and the field mapping. **Core owns the matcher *implementations*; data owns the *wiring*.**

2. **Single-GET hot path MUST be preserved via the one-key-per-path invariant.** Path selection happens **BEFORE** the read (exactly as today — `Authorize` chooses `authorizeTaskPath` vs `authorizeCapabilityPath` from `authContext` before any KV GET). Each path maps to **EXACTLY ONE** disjoint key → **exactly one KV GET per `Authorize` call**. **Two registry entries (two packages) contributing the same path is a config error**, surfaced loudly — the dispatcher **never** fans a single path into N reads. The denial-path `actorRoles` second read stays **off** the hot path.

3. **The three existing logics become the seed core matcher kinds, behavior IDENTICAL.** `matchEphemeralGrant` → `task` matcher kind, `matchServiceAccess` → `service` matcher kind, `matchPlatformPermission` → `platform` matcher kind. Re-express them through the registry with **byte-identical** auth outcomes (same `Decision.Authorized`, same `Code`, same `Reason`, same `ResolvedPermission.Path`, same single-GET key choice). This is a refactor, not a redesign.

4. **This is the auth hot path → a regression here is a live privilege-escalation bug.** Contract #6 is security-critical ("a bug here equals privilege escalation"). Build to the RATIFIED contracts (Contract #2 §2.8 + Contract #6 §6.4–§6.8, both already amended in the FROZEN docs — see References). Verification gates are mandatory and listed in the Testing section.

**What "done" looks like:** the `if taskSet { authorizeTaskPath } else { authorizeCapabilityPath(serviceSet) }` branch in `step3_auth_capability.go:148-166` and the three `match*` methods are reorganized so the **path → (matcher kind, key) selection is table-driven data**, not literal `if/switch` on `task`/`service`. The seed table is core-defined and reads the same keys; the extension point (a package declaring a new path→matcher-kind→key triple) exists and is exercised by a test. All existing auth tests + the bypass suite + the §6.4–§6.8 dispatch tests pass (fixtures may migrate; outcomes hold).

## Acceptance Criteria

> Backbone: `_bmad-output/planning-artifacts/epics/phase-2-epics.md` § "### Story 12.5". Build TO the RATIFIED Contract #2 §2.8 (auth-hook dispatcher, one-key-per-path) and Contract #6 §6.1/§6.4–§6.8 in `docs/contracts/` — both already amended. Amendment audit trail: `cmd/processor/CONTRACT-AMENDMENT-REQUEST.md` (D-CONSUMER, RATIFIED 2026-06-13).

**AC1 — Step-3 dispatch becomes a data-driven registry over matcher kinds.**
**Given** step-3's current hardcoded dispatch — `taskSet → matchEphemeralGrant` (reads `cap.ephemeral.<actor>`), `serviceSet → matchServiceAccess` (reads `cap.<actor>`), default → `matchPlatformPermission` (reads `cap.<actor>`) — in `internal/processor/step3_auth_capability.go:142-282`.
**When** step-3 is refactored into a **generic dispatcher** over a registry whose entries are **data**.
**Then** each registry entry binds: (1) an `authContext` **path predicate** that selects it; (2) a **core matcher kind** that authorizes it; (3) the **disjoint Capability-KV key derivation** that path reads (+ the field mapping the matcher kind needs).
**And** the dispatch is **table-driven** — there is no literal `switch`/`if` naming `task` or `service` in the dispatch decision. (The three matcher-kind *implementations* remain core Go; only the path→kind→key *wiring* is data.)

**AC2 — The three existing logics become the seed matcher kinds with IDENTICAL behavior.**
**Given** the registry from AC1.
**When** the seed registry is built at authorizer construction.
**Then** it contains exactly three core matcher kinds — `task` (ephemeral-grant), `service` (service-access), `platform` (platform-permission) — re-expressing `matchEphemeralGrant` / `matchServiceAccess` / `matchPlatformPermission` respectively.
**And** every existing auth outcome is preserved **byte-for-byte**: `Decision.Authorized`, `Decision.Code`, `Decision.Reason`, `ResolvedPermission.Path` (`"task"` / `"service"` / `"platform"`), the `Decision.Doc` threading on platform/service denials (for FR22 `actorRoles`), and the `Decision.Doc` NOT threaded on the task path (the ephemeral doc carries no roles). The full existing `step3_auth_capability_test.go` suite passes (fixtures may migrate; outcomes hold).
**And** the precedence order is preserved: task → service → platform (task auth wins over service, which wins over platform — Contract #2 §2.8). The `serviceSet && taskSet` mutual-exclusion early-denial (`AuthContextMismatch`, decided before any read) is preserved.

**AC3 — One-key-per-path invariant: exactly one KV GET per Authorize, before-read path selection.**
**Given** the dispatcher from AC1.
**When** any `Authorize` call runs.
**Then** path selection happens **before** the KV GET (from `authContext`, as today), the selected path derives **exactly one** Capability-KV key, and **exactly one** `KVGet` is issued.
**And** the task path reads **only** `cap.ephemeral.<actor>` with **no** `cap.<actor>` fallback — the existing `TestCapabilityAuthorizer_TaskPath_SingleGetNoFallback` assertion (`step3_auth_capability_test.go:323`, fake reader fails the test if it touches `cap.<actor>`) still passes.
**And** the denial-path `actorRoles` read stays **off** the hot path: a task-path no-match still denies with `AuthContextMismatch` (which the denial builder emits without `actorRoles`, so no second read), and platform/service denials still source `actorRoles` from the already-read `Decision.Doc` (no additional GET).

**AC4 — Two-entries-same-path is a CONFIG ERROR, never a fan-out.**
**Given** the registry can in principle accept package-declared entries.
**When** two registry entries select the **same** path predicate (e.g. two packages both claiming the platform path).
**Then** this is rejected as a **configuration error** — surfaced loudly at registry-build / install time (fail-closed: the authorizer construction returns an error OR the install is rejected; dev states which and why in Completion Notes). The dispatcher **never** resolves an ambiguous path by issuing N reads or merging N docs. **One path → one key → one GET** is structurally enforced, not merely conventional.
**And** a unit test proves the duplicate-path registry is rejected.

**AC5 — The package-data extension point exists and is exercised (without a core edit per package).**
**Given** the registry is data-driven (AC1) and core owns the matcher kinds (AC2).
**When** a registry entry is added that binds a **new path** → an **existing core matcher kind** → a **new disjoint key derivation** (the shape Story 12.6 will use: `rbac-domain` declaring its `cap.roles.<actor>` path against the `platform` matcher kind).
**Then** the dispatcher routes that path to the new key and runs the existing matcher kind against the doc read from that key — **with no edit to the matcher-kind implementations**. A test demonstrates this end-to-end on a fixture (a throwaway path/key declared as data, routed and matched correctly).
**Note:** this story does NOT add a production package entry or a new production key — it proves the *mechanism* so 12.6 is a pure package addition. Where the registry data physically lives (constructor-injected seed list, parsed from a manifest aspect, or a hybrid) is a dev decision; state it in Completion Notes and keep it consistent with how Refractor sources `projectionKind` lens data (12.3/12.4) where practical. (See Dev Notes → "Where does the registry data come from?")

**AC6 — No-entry = denial is preserved per matcher kind.**
**Given** Contract #6 §6.8 ("No Entry = No Access") and the soft-tombstone (12.1a).
**When** a path's disjoint key is absent (or a soft tombstone, `isDeleted:true`).
**Then** the matcher denies (no grants → no match) with the **same** code the existing path produces today: task path → `AuthContextMismatch` ("no ephemeral grant entry for actor"); capability path (service/platform) → `AuthDenied` ("NoCapabilityEntry"). Absence and tombstone are both denial; auth semantics unchanged.

**AC7 — Tests pass; fixtures migrate where the registry replaces the switch.**
**Given** the refactor.
**When** the test suites run.
**Then** the §6.4–§6.8 dispatch conformance tests + `step3_auth_capability_test.go` + the bypass suite all pass. Test fixtures/oracles **may** change where the registry replaces the literal `switch` internals, but the asserted **outcomes hold**. Specifically the Gate-3 Capability-Lens 4-attack-vector suite (`internal/bypass/capadv_*`) and the Gate-2 bypass suite (`internal/bypass/bypass_*`) pass with all vectors DEFENDED / BLOCKED.

**AC8 — Contract amendment is recorded (already ratified — confirm, do not re-raise).**
**Given** `cmd/processor/CONTRACT-AMENDMENT-REQUEST.md` (D-CONSUMER) is **RATIFIED 2026-06-13** and the forward notes are already folded into the FROZEN Contract #2 §2.8 and Contract #6 §6.1/§6.4–§6.8.
**When** the story lands.
**Then** the implementation conforms to those already-ratified contract texts (no new amendment is needed; the FROZEN contracts are built-to, not edited). If implementation reveals a genuine gap in the ratified text, raise it via a NEW entry in `cmd/processor/CONTRACT-AMENDMENT-REQUEST.md` — do **not** edit the FROZEN contract in-flight (CLAUDE.md frozen-contract discipline).

**AC9 — Verification gates pass (security plane).**
**Then** all gates in the Testing section pass: `go build ./...`, `make vet`, `golangci-lint run ./...`, `make verify-kernel`, `make test-bypass` (Gate 2, all BLOCKED), `make test-capability-adversarial` (Gate 3, all DEFENDED), and `go test ./internal/processor/...`.

## Tasks / Subtasks

- [x] **Task 1 — Model the registry (AC1, AC2).** (AC: 1, 2)
  - [x] Define a `matcherKind` abstraction in `internal/processor` (e.g. a func/interface taking `(env, doc, resolved) Decision`) with three core implementations wrapping the existing `matchEphemeralGrant` / `matchServiceAccess` / `matchPlatformPermission` bodies — moved, not rewritten.
  - [x] Define a registry entry: `{ pathPredicate(authContext) bool, kind matcherKind, keyDerivation(actor) (string, error) }`. The three seed entries: task → ephemeral kind → `ephemeralKeyFromActor`; service → service kind → `capabilityKeyFromActor`; platform → platform kind → `capabilityKeyFromActor`.
  - [x] Preserve the precedence (task → service → platform) in registry ordering / selection, and the `service && task` early-denial before any read.
- [x] **Task 2 — Rewrite `Authorize` as the generic dispatcher (AC1, AC3, AC6).** (AC: 1, 3, 6)
  - [x] `Authorize`: compute path from `authContext` → select the single matching registry entry → derive its single key → one `KVGet` → parse → run the entry's matcher kind. Keep the latency-ring defer.
  - [x] Preserve absent-key/tombstone → denial with the existing per-path codes (task → `AuthContextMismatch`; capability → `AuthDenied`/`NoCapabilityEntry`).
  - [x] Preserve `Decision.Doc` threading: set on platform/service denials (for FR22 `actorRoles`), NOT set on the task path; set `Decision.Resolved` on success only.
  - [x] Preserve infra-error pass-through (return error so commit path naks for redelivery) on non-`ErrKeyNotFound` read/parse failures.
- [x] **Task 3 — Duplicate-path config-error guard (AC4).** (AC: 4)
  - [x] At registry construction, reject two entries selecting the same path. Decide fail point (constructor error vs install rejection) and document why in Completion Notes.
  - [x] Unit test: duplicate-path registry → rejected.
- [x] **Task 4 — Package-data extension point + proof test (AC5).** (AC: 5)
  - [x] Make the registry extensible by data (constructor-injected entry list, or parsed from package install data — dev decision, stated in Completion Notes).
  - [x] Test: declare a throwaway entry (new path predicate + new key derivation, reusing the `platform` matcher kind), seed a doc at the new key, assert it routes + matches with no matcher-kind code change.
- [x] **Task 5 — Migrate existing tests; run dispatch conformance (AC2, AC7).** (AC: 2, 7)
  - [x] Run `go test ./internal/processor/...`; migrate fixtures where the registry replaces switch internals (outcomes hold byte-for-byte).
  - [x] Confirm `TestCapabilityAuthorizer_TaskPath_SingleGetNoFallback`, `_BothServiceAndTaskSet`, `_MissingEntry_NoCapabilityEntry`, and the platform/service/task matrix all pass unchanged in outcome.
- [x] **Task 6 — Code conventions sweep (house rules).** (AC: all)
  - [x] NO history/changelog comments (`// Story 12.5…`, `// was…`, `// previously…`, `// refactored from…`). Comments describe what the code does NOW. Key-shape conventions hold (`cap.<suffix>`, `cap.ephemeral.<suffix>`).
- [x] **Task 7 — Verification gates (AC9).** (AC: 9)
  - [x] `go build ./...`, `make vet`, `golangci-lint run ./...`, `make verify-kernel`.
  - [x] `make test-bypass` (Gate 2 — all BLOCKED), `make test-capability-adversarial` (Gate 3 — all DEFENDED), `go test ./internal/processor/...`.
  - [x] **CI-only-check lesson:** the dispatch reads keys; if any key SHAPE or registry-data SHAPE changes, `grep` `scripts/verify-*.go` and reproduce CI in its exact order before declaring done (a spec/key shape change has bitten prior Epic-12 stories at the CI-only verify step — see Dev Notes → "CI-only checks").

## Dev Notes

### The exact code being refactored (current state)

`internal/processor/step3_auth_capability.go` (read it in full before touching it):

- `Authorize` (line 142): derives `serviceSet`/`taskSet` from `env.AuthContext`, rejects `service && task` (early `AuthContextMismatch`, **before any read**), then branches `taskSet → authorizeTaskPath` else `authorizeCapabilityPath(serviceSet)`.
- `authorizeTaskPath` (line 172): derives `cap.ephemeral.<actor>` via `ephemeralKeyFromActor`, single `KVGet`, **no fallback**, runs `matchEphemeralGrant`. Absent key → `AuthContextMismatch`. Does NOT thread `doc` into the denial.
- `authorizeCapabilityPath` (line 221): derives `cap.<actor>` via `capabilityKeyFromActor`, single `KVGet`, runs `matchServiceAccess` (if `serviceSet`) else `matchPlatformPermission`. Absent key → `AuthDenied`/`NoCapabilityEntry`. Threads `doc` into denials (FR22 `actorRoles`).
- `matchEphemeralGrant` (284), `matchServiceAccess` (321), `matchPlatformPermission` (352): the three logics that become the seed matcher kinds. **Move their bodies verbatim** into the matcher-kind implementations — do not rewrite the matching logic.
- `capabilityKeyFromActor` (427), `ephemeralKeyFromActor` (443): the two key derivations. They become the seed entries' `keyDerivation` funcs.

**The refactor's essence:** the `taskSet/serviceSet` branch + the two `authorize*Path` helpers collapse into one generic `Authorize` that walks the registry: for each entry in precedence order, if its `pathPredicate(authContext)` matches, derive its key, GET once, parse, run its matcher kind, return. The seed registry encodes today's three paths against today's two keys — so production behavior is identical and only ONE GET ever fires.

### Where does the registry data come from? (AC5 decision space)

The contracts call the dispatch table "data," but **this story does not require wiring it to a live package manifest** — 12.6 does that. For 12.5 the acceptance gate is that the *mechanism* is data-shaped and extensible. Acceptable implementations (pick one, state it in Completion Notes):

1. **Constructor-injected seed list (leanest):** `NewCapabilityAuthorizer` builds the three core entries internally; an optional `extraEntries []registryEntry` param (or functional option) lets callers/tests add declared entries. AC5's proof test injects a throwaway entry. 12.6 would inject the `rbac-domain` entry at processor wiring time from install-time data it reads.
2. **Parsed-from-data:** read entries from a manifest/DDL aspect at processor startup (mirrors how Refractor sources `projectionKind` lens data in 12.3/12.4). Heavier; only do this if it's genuinely cleaner.

**Recommendation:** start with option 1 (constructor-injected). It satisfies "the wiring is data" (entries are values, not a hardcoded `switch`), keeps the change scoped to step-3, and gives 12.6 a clean seam. The key point AC1/AC5 enforce is **no literal `switch`/`if` naming `task`/`service` in the dispatch decision** and **a new path is added by adding an entry, not by editing core dispatch logic**.

### One-key-per-path — why it matters and how to keep it

Decomposing projections to disjoint keys (12.6/12.7) would naively make step-3 read multiple docs. The whole reason D-CONSUMER exists is to keep the auth hot path **single-GET**: because step-3 selects the path from `authContext` **before** reading, each path maps to exactly one key. The dispatcher must keep this structural — **never** loop over multiple entries issuing reads, **never** merge docs. Exactly one entry matches a given `authContext`; that entry's single key is read once. Two entries matching the same `authContext` is the AC4 config error.

The denial-path `actorRoles` second read is a separate concern and stays **off** the hot path: it only happens on a *capability-path denial*, sourced from the already-parsed `Decision.Doc` (no extra GET), and never on the task path.

### Behavior-preservation checklist (the things a regression would break)

- Precedence: task → service → platform.
- `service && task` set together → `AuthContextMismatch`, **before any read**.
- Task path: single GET of `cap.ephemeral.<actor>`, no `cap.<actor>` fallback; absent → `AuthContextMismatch`; no `doc` threaded into denial.
- Service path: `service not in serviceAccess` → `AuthContextMismatch`; service matched but op not allowed → `AuthDenied`.
- Platform path: `scope=any` allow; `scope=self` requires `target==actor` (`AuthContextMismatch` if target missing, `AuthDenied` if `target!=actor`); `scope=specific` → `AuthContextMismatch` (deny-stub, Phase 3); `scope=owned` → `AuthDenied` (`OwnershipScopeNotImplemented`); unknown scope → `AuthDenied`; no matching permission → `AuthDenied`.
- Ephemeral matcher: `taskKey==authContext.task` AND `operationType==env.OperationType` AND `target==authContext.target` AND `expiresAt > now` (unparseable `expiresAt` → skip with WARN, not a match).
- `ResolvedPermission.Path` set to `"task"`/`"service"`/`"platform"` on success; the matched-entry pointer (`EphemeralGrant` / `ServiceAccess`+`AllowedOperation` / `PlatformPermission`) set.
- `Decision.Resolved` set on success only; `Decision.Doc` set on capability-path denials only.
- Capability-path absent key → `AuthDenied`/`NoCapabilityEntry`; infra read/parse error → return Go error (commit path naks).

### Source tree components to touch

- `internal/processor/step3_auth_capability.go` — primary refactor target.
- `internal/processor/step3_auth_capability_test.go` — fixture migration (outcomes hold).
- `internal/processor/step3_auth.go` — `Authorizer` interface + `SelectAuthorizerArgs`/`SelectAuthorizerOpts` wiring (only if the constructor signature gains an entries param — keep the `Authorizer` interface unchanged).
- `internal/processor/operation_context.go` — `ResolvedPermission` (read-only reference; `Path` field semantics preserved).
- `internal/processor/step3_denial_response.go` — denial builder (read-only reference; confirm `actorRoles` sourcing unchanged).
- Possible new file: `internal/processor/step3_auth_matcher.go` (or similar) for the matcher-kind + registry types, to keep `step3_auth_capability.go` lean.

**DO NOT touch** anything under `internal/refractor/` (this is the read/consumer side only — the projection/write side is 12.3/12.4, already landed), and **DO NOT** add new production keys or decompose any cypher (12.6/12.7).

### CI-only checks (lesson from prior Epic-12 stories)

When a spec or KV-key SHAPE changes, the repo has **CI-only verify scripts** (`scripts/verify-*.go`, invoked via `make verify-kernel` and friends) that can fail even when `go test` passes. Story 12.4's follow-ups and Story 12.4's `verify-package-identity-hygiene` fix both tripped this. For 12.5: the seed registry reads the **same** keys, so this should be inert — but if you change any key derivation or registry-data shape, `grep scripts/verify-*.go` for the affected key/shape and reproduce CI in its exact order (`go build` → `make vet` → `golangci-lint` → `make verify-kernel` → Gate 2 → Gate 3) before declaring done.

### House rules (CLAUDE.md — non-negotiable)

- **NO history/changelog comments in code.** Never `// Story 12.5…`, `// was…`, `// previously…`, `// refactored from the switch…`, `// renamed from…`. Comments describe what the code does NOW for a reader who has no idea a change happened. (This is the single most-violated rule.)
- **Key-shape conventions (Contract #1).** Aspects 4-segment `vtx.<type>.<id>.<localName>`; the Capability-KV keys here are `cap.<actor-suffix>` and `cap.ephemeral.<actor-suffix>` (actor = `vtx.identity.<NanoID>` with `vtx.` dropped). Do not invent new key shapes in this story.
- **Frozen contracts** (Contract #2 §2.8, Contract #6) are build-to, never edit. Genuine gaps → new `cmd/processor/CONTRACT-AMENDMENT-REQUEST.md` entry, not an in-flight contract edit.
- **Sub-agents never commit/push/branch** — leave the change in the working tree for Winston to adjudicate.

### Testing standards summary

- Unit tier: `internal/processor/step3_auth_capability_test.go` (embedded/fake reader, fixed clock) — full path matrix + single-GET-no-fallback + mutual-exclusion + absent-key + extension-point + duplicate-path-rejection.
- Gate 2 (BLOCKED): `make test-bypass` → `go test ./internal/bypass/... ` — requires a running Docker stack (`make up`); exits 0 only when all 4 bypass categories are BLOCKED.
- Gate 3 (DEFENDED): `make test-capability-adversarial` → `go test ./internal/bypass/... -run TestCapAdv` + `-run TestGate3_Report` — exits 0 only when all 4 attack vectors are DEFENDED. The Gate-3 vectors (`capadv_cross_target_bleed`, `capadv_direct_kv_write`, `capadv_lens_def_mutation`, `capadv_projection_lag`, `capadv_projection_resurrection`, `capadv_rebuild_integrity`) exercise the real auth path — a dispatcher regression surfaces here.

### Project Structure Notes

- The refactor stays entirely within `internal/processor`. The `Authorizer` interface (`step3_auth.go`) and the commit-path call site stay unchanged — only `CapabilityAuthorizer`'s internals are reorganized. If the constructor gains an `extraEntries` param, thread it through `SelectAuthorizerOpts` → `SelectAuthorizerArgs` → `NewCapabilityAuthorizer` without changing the `Authorizer` interface.
- No conflict with project structure. This is the read-side mirror of the 12.3/12.4 write-side data-driven refactor; keeping the registry types in a small dedicated file matches how the projection plan lives in `internal/refractor/projection/`.

### References

- [Source: _bmad-output/planning-artifacts/epics/phase-2-epics.md#Story 12.5: Generic step-3 auth-hook dispatcher (D-CONSUMER)] — AC backbone + party-review pin.
- [Source: docs/decisions/projection-plane-decomposition.md#D-PROJECTION + D-CONSUMER] — the "consumer is what keeps it O(1)" rationale; finding #8 (data-not-plugins) + #9 (primordial composition, resolved in 12.6) + #12 (fixtures migrate).
- [Source: cmd/processor/CONTRACT-AMENDMENT-REQUEST.md#generic step-3 auth-hook dispatcher (Story 12.5, D-CONSUMER)] — RATIFIED 2026-06-13; the fixed-matcher-kind registry + one-key-per-path read model.
- [Source: docs/contracts/02-operation-envelope.md#2.8 Auth Context] — RATIFIED Phase-2 amendment: generic auth-hook dispatcher, one-key-per-path, precedence task→service→platform, forgery resistance.
- [Source: docs/contracts/06-capability-kv.md#6.1 Bucket and Key Pattern] — disjoint key spaces (`cap.<actor>`, `cap.ephemeral.<actor>`, future `cap.roles.<actor>` / `cap.svc.<actor>`); §6.4–§6.8 dispatch semantics; §6.8 no-entry=denial + soft-tombstone.
- [Source: internal/processor/step3_auth_capability.go:142-452] — the code being refactored.
- [Source: internal/processor/step3_auth.go:24-166] — `Decision`, `Authorizer`, `SelectAuthorizerArgs`.
- [Source: internal/processor/operation_context.go:8-26] — `ResolvedPermission` (Path semantics).
- [Source: internal/processor/envelope.go:41-46] — `AuthContext` struct.
- [Source: _bmad-output/implementation-artifacts/12-4-migrate-builtins-delete-switch.md] — sibling write-side data-driven refactor (DONE); pattern + the "delete the switch, drive from data" framing.

## Open Questions for Winston

1. **Registry data source (AC5).** Dev Notes recommend the leanest path — constructor-injected seed list with an optional `extraEntries` seam — deferring manifest-parsing to 12.6. Confirm this is the intended scope boundary (12.5 proves the mechanism; 12.6 wires the first real package entry), or whether you want 12.5 to already parse entries from install-time data.
2. **Duplicate-path fail point (AC4).** Reject at authorizer construction (constructor returns error) vs at install time. Construction-time is the lean default since 12.5 has no install path for entries yet; flagging in case you want the install-rejection seam stubbed now for 12.6/12.7.
3. **AC8 confirms no new amendment is needed** (the D-CONSUMER request is RATIFIED and folded into the FROZEN contracts). Confirming the dev agent should build-to those texts and only raise a NEW amendment entry if a genuine gap appears — not touch the FROZEN contract files.

## Dev Agent Record

### Agent Model Used

claude-opus-4-8 (Amelia / bmad-dev-story)

### Debug Log References

- `go build ./...` → PASS
- `make vet` → PASS (exit 0)
- `golangci-lint run ./...` → PASS (0 issues)
- `make verify-kernel` → PASS (ALL ASSERTIONS PASSED)
- `go test ./internal/processor/...` → PASS (`ok` both packages)
- `make test-bypass` (Gate 2) → PASS, exit 0, 4/4 BLOCKED
- `make test-capability-adversarial` (Gate 3) → PASS, exit 0, 6/6 cleared (5 DEFENDED, 1 ACCEPTED-WINDOW)

### Completion Notes List

**What was done.** Refactored step-3's hardcoded `taskSet/serviceSet`
branch + the two `authorize*Path` helpers into a single generic dispatcher
walking a data-shaped registry of `authEntry` values. The three existing
`match*` bodies are unchanged (moved behind thin `matcherKind` adapters, not
rewritten). New file `internal/processor/step3_auth_matcher.go` holds the
`matcherKind`/`authEntry` types, the seed-entry builders, and the
duplicate-path guard. `Authorize` now: mutual-exclusion early-deny → select the
single matching entry from authContext (before any read) → derive its one
disjoint key → one `KVGet` → parse → run the entry's matcher kind. One GET per
call is structural (one entry owns a path).

**Registry data source (Winston Q1 — Option 1, as adjudicated).**
Constructor-injected seed list with an optional `extraEntries ...authEntry`
variadic on `NewCapabilityAuthorizer`, threaded through
`SelectAuthorizerOpts.ExtraEntries` → `SelectAuthorizerArgs`. The three core
entries are built internally; packages add a path by injecting an entry value
(no `switch`, no core edit). 12.6 will inject the `rbac-domain` entry from
install-time data through this seam. No manifest parsing in this story.

**Duplicate-path fail point (Winston Q2 — construction-time, as adjudicated).**
`buildAuthRegistry` rejects a duplicate path *name* (and a missing
predicate/kind/key-derivation) by returning an error, so
`NewCapabilityAuthorizer`/`SelectAuthorizerArgs` fail closed at authorizer
construction. No install path for entries exists yet (12.6/12.7), so there is
nothing to reject at install time. The dispatcher never resolves ambiguity by
fanning into N reads — the guard makes one-key-per-path structural.

**Precedence + catch-all ordering (design note for review).** The platform seed
entry's predicate is always-true (it is the Phase-1 `else` branch), so it must
be evaluated LAST or it would shadow every package path. Registry assembly is
therefore: core specific entries (task → service) → package extras → platform
catch-all. This preserves task→service→platform precedence for the core paths
*and* lets a package add a new selective path (the AC5 shape) without platform
swallowing it. Extras may not reuse a core path name. The `service && task`
mutual-exclusion early-deny stays an explicit pre-walk check in `Authorize`
(otherwise the task entry would select it before the deny fires) — behavior
byte-identical to before.

**Behavior preservation (AC2/AC3/AC6).** Per-path absent-key codes
(task→AuthContextMismatch "no ephemeral grant entry for actor";
service/platform→AuthDenied "NoCapabilityEntry"), `Decision.Doc` threading
(capability paths on denial only, never task), `Decision.Resolved` on success
only, infra-error pass-through, and `ResolvedPermission.Path`
(`task`/`service`/`platform`) are all preserved. The full existing
`step3_auth_capability_test.go` matrix passes unchanged in outcome, including
`TaskPath_SingleGetNoFallback`, `BothServiceAndTaskSet`, and
`MissingEntry_NoCapabilityEntry`.

**Tests added.** `TestAuthRegistry_DuplicatePathRejected` (AC4 — duplicate path
→ construction error, nil authorizer) and
`TestAuthRegistry_ExtensionPoint_RoutesNewPath` (AC5 — a data-declared entry
binding a new path predicate + new disjoint key derivation, reusing the
`platform` matcher kind unchanged, routes and matches; single GET of the new
key).

**AC8 — contracts (Winston Q3 confirmed).** Built to the already-RATIFIED
FROZEN Contract #2 §2.8 + Contract #6 §6.1/§6.4–§6.8. No frozen contract
edited; no new amendment needed — the implementation conforms to the ratified
text (data-driven dispatch, one-key-per-path, task→service→platform
precedence). **No CONTRACT-AMENDMENT-REQUEST entry was added.**

**CI-only checks.** No key derivation or registry-data shape changed (seed
entries read the same `cap.<actor>` / `cap.ephemeral.<actor>` keys), so the
`scripts/verify-*.go` surface is inert here — confirmed by grep (no references
to the keys or new types). Full CI order reproduced locally and green.

**Uncertain / for review.** (1) The catch-all-last ordering is a deliberate
deviation from a naive "seeds-then-extras" append — it is required for AC5 to
work because platform's predicate is always-true; flagging for the adversarial
review to sanity-check that this can't let an extra entry shadow a core path
(it can't: extras sit between service and platform, and a name collision with
any core path is rejected). (2) `selectEntry` returns `nil` only if the
registry were built without the always-true platform entry — defensive
fail-closed branch that is unreachable via the public constructor.

### File List

- `internal/processor/step3_auth_matcher.go` (new) — matcherKind + authEntry registry, seed builders, duplicate-path guard.
- `internal/processor/step3_auth_capability.go` (modified) — `Authorize` rewritten as the generic dispatcher; `selectEntry` added; `authorize*Path` helpers removed; `NewCapabilityAuthorizer` now returns an error and takes `extraEntries ...authEntry`; `registry` field added.
- `internal/processor/step3_auth.go` (modified) — `SelectAuthorizerOpts.ExtraEntries` added; threaded into `NewCapabilityAuthorizer` call.
- `internal/processor/step3_auth_capability_test.go` (modified) — helper updated for the error return; added `TestAuthRegistry_DuplicatePathRejected` + `TestAuthRegistry_ExtensionPoint_RoutesNewPath`; `strings` import.
- `internal/processor/service_actor_auth_parity_test.go` (modified) — helper updated for the constructor error return.
- `internal/bypass/capadv_cross_target_bleed_test.go`, `internal/bypass/capadv_lens_def_mutation_test.go`, `internal/bypass/capadv_direct_kv_write_test.go`, `internal/bypass/capadv_projection_lag_test.go` (modified) — call sites updated for the constructor error return.
