# Story 7.3 — Service-actor bootstrap provisioning (`identity:loom` + `identity:weaver`)

Status: review

**Tier:** Opus (primordial bootstrap + the security plane). This seeds two new **root-equivalent** actor identities into the kernel and asserts they pass commit-path step-3 auth *identically to a human actor*. A mistake here either (a) silently grants root to the wrong topology, or (b) introduces a service-actor special-case branch in auth that the adversarial suite is designed to forbid. Treat the auth-parity assertion as the crux.
**Epic spec:** `_bmad-output/planning-artifacts/epics/phase-2-epics.md` → "### Story 7.3: Service-actor bootstrap provisioning" (line ~49). Read it for the user-story framing and the exact AC.
**Binding grounding (FROZEN — read these, do NOT redefine or edit):**
- `docs/contracts/07-primordial-bootstrap.md` — §7.1 (bootstrap establishes topology; the Capability Lens does the rest — **no direct-seeded `cap.*` entries**), §7.2 inventory item **7 & 8** (system identity vertices + their `holdsRole` topology links — **item 7's parenthetical explicitly says "Additional internal service actor identities for Loom, Weaver, etc. are seeded … following the same pattern"**), §7.3 (`lattice.bootstrap.json` stable-reference config), §7.4 (idempotent re-run = op-tracker-present skip), §7.5 (readiness gate), §7.7 (write order).
- `docs/contracts/06-capability-kv.md` — §6.4 `platformPermissions[]` (`scope: "any"` = the root-equivalent shape), §6.8 **"No Entry = No Access"** ("The Capability Lens must produce a projection for every identity that may submit operations, **including … internal service actors**"), §6.10 #4 (role specialization → `platformPermissions[]`).
- `docs/contracts/02-operation-envelope.md` — §2.8 `authContext` dispatch (task → service → platform precedence; a service actor with no `authContext` takes the **platform path** = `cap.<actor>.platformPermissions[]`), §2.3 lanes (the `system` lane is "reserved for internal service actors").
- **Architecture grounding — arch §92 (`lattice-architecture.md` line 93, "Internal service actor model"):** *"Loom, Weaver, and admin tools operate within the trust boundary with their own internal service actor identities at **root-level access**. They submit ops directly to the ledger (**bypassing Gateway**), using **pre-provisioned signing keys**. These are `Lattice-Actor: identity:<service_name>` with **root-equivalent capabilities**."* Plus line 1170: *"**Service-actor vertices** (Loom/Weaver/Refractor) are provisioned at **bootstrap (primordial)** with root-equivalent capability; `class` on root."* And line 984: *"Internal service actors … operate at root-equivalent capability within the trust boundary. AI agents are NOT internal service actors."*
**Depends on:** Contract #7 (the primordial bootstrap path you extend); Story 1.4 (the `make up` seeder / `internal/bootstrap`); Epic 3 (the Capability Lens cypher that projects `holdsRole → operator` topology into root-equivalent `platformPermissions`); Story 4.7 (the minimized kernel — admin identity + operator role + meta/install permissions are the **template** you copy). Does NOT depend on 7.1/7.2 (the task substrate); this is a pure bootstrap/identity story.
**Workflow:** the DS is a sub-agent. Repo root, no worktree. Do **NOT** commit/push or branch. Do **NOT** edit planning artifacts (`epics/*.md`, `lattice-architecture.md`, `MORPH-DEVIATIONS.md`) or **FROZEN** contracts (`docs/contracts/*`). You MAY edit `/docs/components/*`. A genuine contract gap → file `cmd/bootstrap/CONTRACT-AMENDMENT-REQUEST.md` and continue with a different deliverable; do not edit the frozen shape in place.

---

## 0. ADJUDICATION — Winston build target. DS builds to THIS.

### 0.0 What this story delivers (scope boundary)

Seed two new internal service-actor identities into the **existing** primordial bootstrap inventory so the engines (Epic 8 Loom, Epic 9 Weaver) can submit ops as authenticated root-equivalent actors. **In scope:**

1. **Two new identity vertices** — `vtx.identity.<loomId>` and `vtx.identity.<weaverId>` — seeded by `internal/bootstrap` through the **same `buildPrimordialEntries()` atomic batch** that seeds the admin identity today. `class: "identity.system.loom"` / `"identity.system.weaver"` (arch §92 / line 1170: "`class` on root"; mirror the existing admin's `class: "identity"` + `protected: true` shape, specializing the class). Marked `protected: true` (§3.4 — a package uninstall must never tombstone a kernel service actor, exactly as the admin identity is protected).
2. **Root-equivalent capability via topology, NOT a new mechanism** — one `holdsRole` link per service actor to the **existing** `operator` root role: `lnk.identity.<loomId>.holdsRole.role.<RoleOperatorID>` and the weaver equivalent. The operator role already carries the `scope:"any"` permissions (CreateMetaVertex/Update/Tombstone/Install/Uninstall) via `grantedBy` links; the Capability Lens cypher already projects `holdsRole → operator → grantedBy → permission` into `platformPermissions[].scope:"any"` (Contract #7 §7.7, Contract #6 §6.10 #4). **So "root-equivalent" = "holds the operator root role," nothing more.** You add **identity vertices + holdsRole links only** — you do NOT add new roles, new permissions, new grantedBy links, or a new cypher branch.
3. **Stable references** — the two new NanoIDs are generated at first `make up` and persisted to `lattice.bootstrap.json` (Contract #7 §7.3) so post-restart code can resolve "the loom identity" without a class query. Extend `PrimordialIDsRaw` / the bootstrap config struct + the package-level `Loom*`/`Weaver*` key vars (mirror `BootstrapIdentityID`/`Key`).
4. **Idempotence + kernel-count + readiness coherence** — the new entries ride the **existing** op-tracker-present idempotent skip (Contract #7 §7.4 — no new idempotence path); the new keys are added to `PrimordialVertexKeys()` so `verify-kernel` enumerates them; the kernel-count comment + `scripts/verify-kernel.go` expected count are bumped by exactly the number of new entries; the readiness gate (Contract #7 §7.5) is extended to additionally confirm the **loom and weaver `cap.*` projections** exist before declaring ready (so the engines can't start before their auth projection lands).
5. **Auth-parity proof (the crux)** — a test submitting an op with `Actor: identity:weaver` (and `identity:loom`) that takes the **platform path** at step-3 (`authContext.task` and `authContext.service` both unset) is **authorized by the exact same `authorizeCapabilityPath` code as a human actor** — reading `cap.identity.<weaverId>`, scanning `platformPermissions[]`, matching `scope:"any"`. **No service-actor branch is added to `internal/processor/step3_auth_capability.go` (or anywhere in step-3).**

**OUT of scope (do NOT build — later work):** the Loom/Weaver engines themselves (`internal/loom`, `internal/weaver` — Epics 8/9); the `system`-lane consumer (Contract #2 §2.3 — the engines bind it later); the schedule stream (Story 7.4); any *cryptographic* signing-key verification at the Processor (**see A2 + Open Question 1 — none exists in Phase 2; do NOT invent one**); a Gateway (there is no Gateway in Phase 2). A `Refractor` service-actor identity (arch line 1170 lists it) is **not** in this AC — seed only `loom` + `weaver` (the AC names exactly those two); note Refractor as an Open Question if you think it belongs.

### 0.1 A1 — Extend the EXISTING primordial path; do not fork a parallel provisioning mechanism (AC #2, binding)

This is the part the AC most explicitly constrains: *"their provisioning **extends** the existing primordial bootstrap path (does not introduce a parallel actor-provisioning mechanism)."* Concretely:

- The two identities + two `holdsRole` links are appended to the **same `buildPrimordialEntries()` slice** that the admin identity, operator role, and meta-permissions already populate (`internal/bootstrap/primordial.go`, the section numbered "2. Primordial admin identity" + "10. Primordial admin → operator holdsRole link"). They land in the **same single `substrate.AtomicBatch`** (`SeedPrimordial`) — either the whole primordial set lands or none of it does. **Do NOT** add a second seeder function, a separate batch, a post-bootstrap op, or a package-install path for these. Mirror the admin identity's construction line-for-line (`MakeVertexEnvelope` for the vertex, `MakeLinkEnvelope` for the holdsRole link), specializing only the `class` and the NanoID.
- **Write order (Contract #7 §7.7):** identities go with the other identities (after the op tracker, before/with the meta DDLs is fine — match where the admin identity sits); the `holdsRole` links go with the other links at the **end** of the batch (after the role + permissions + grantedBy links exist, alongside `BootstrapHoldsRoleLinkKey`). The batch is `CreateOnly: true` per entry — the new keys must not pre-exist.
- **No direct `cap.*` write (Contract #7 §7.1, §7.2 "Direct Capability KV writes from `make up`: None"):** you seed **graph topology only**. The loom/weaver `cap.identity.<id>` docs are produced by the **Capability Lens projecting their `holdsRole → operator` topology** once Refractor runs — exactly as the admin's `cap.*` is produced. If you find yourself writing a `cap.*` key from the seeder, you have violated §7.1 — stop. (This is also why the readiness gate, A4, must *poll for* the projection rather than assume it.)

### 0.2 A2 — "Pre-provisioned signing keys": there is NO Processor-side signature check in Phase 2 — ground it, do not invent crypto (AC #1, binding constraint)

The AC says the identities exist "with **pre-provisioned signing keys**." Reading the actual code, the truth you must build to:

- **The Processor authorizes purely on `env.Actor` (a string) → `cap.identity.<id>` lookup.** `OperationEnvelope` (`internal/processor/envelope.go`) has **no `signature` field**; step-3 (`step3_auth_capability.go`) does **no cryptographic verification**. There is **no Gateway** in Phase 2 (`internal/gateway` does not exist) — arch §92 says service actors *bypass* the Gateway anyway. So at the **commit-path auth boundary**, a service actor authenticates exactly the way a human does: by *being* `identity:weaver` in the `actor` field and *having* a `cap.identity.<weaverId>` projection. **There is no signing key to verify at step-3.** That is precisely why AC #3's "identically to a human actor" is *achievable with zero new code* — both go through `cap.<actor>`.
- **What "pre-provisioned signing key" therefore means in Phase 2:** the *transport/submission* credential the engine uses to authenticate to **NATS** when it publishes to `ops.system.>` (the NATS account/nkey/creds the `internal/loom`/`internal/weaver` process holds). That is a **deployment/transport concern** (NATS creds), **not** a graph-vertex aspect and **not** a Processor check. Arch §92's "bypassing Gateway, using pre-provisioned signing keys" describes how the engine *gets its op onto the stream*, which the trust-boundary (mTLS + NATS account, arch "Authentication & Security") secures — it is **explicitly a deferred Stream-3 decision** (arch lines 285, 325: *"Internal service actor key provisioning mechanism (config file, boot-time generation, Vault-stored) — Deferred to Stream 3"*).
- **DECISION (build to this):** **Do NOT** add a cryptographic signing-key aspect to the identity vertex, **do NOT** add a `signature` field to the envelope, **do NOT** add any signature verification to step-3. The "pre-provisioned signing key" for Phase 2 is satisfied by the engine process holding NATS credentials at the transport layer; the graph contribution of this story is the **identity vertex + root-role topology** that makes the actor *authorizable* once its op is on the stream. **If** you judge a placeholder credential-binding aspect on the service identity is warranted for forward-compat (so Epic 8/9 has a documented hook), make it a **non-load-bearing, documented placeholder** and flag it in the closing summary — but the default is **seed no key material** and record the transport-creds reality as the grounding. **See Open Question 1 — this is the one judgment call Winston must confirm.**

### 0.3 A3 — Root-equivalent capability = the operator role, projected (AC #1; Contract #6 §6.4, §6.10 #4)

- "Root-equivalent" resolves, in `platformPermissions[]` terms, to **the set of `{operationType, scope:"any"}` entries the operator role grants** — i.e. whatever the admin identity's `cap.identity.<adminId>.platformPermissions[]` contains today (CreateMetaVertex/UpdateMetaVertex/TombstoneMetaVertex/InstallPackage/UninstallPackage, all `scope:"any"`), because loom/weaver hold the **same** operator role via the **same** `holdsRole` edge. There is no separate "root role" in the current kernel — the **`operator` role IS the root-equivalent role** (it holds the only `scope:"any"` permissions). **Reuse it.** Do not author a distinct `systemRoot` role (Contract #7 §7.2 item 6 describes a `systemRoot`-*or-similar* convention; the 4.7-minimized kernel realizes that convention as `operator`. Confirm by reading `primordial.go` — the operator role + its `scope:"any"` permissions are the realized root role).
- **Why no special-casing is needed (Contract #7 §7.7 final line):** *"The rule is uniform across system and non-system identities; root capability is established by graph topology, not by class-based special-casing."* The cypher already emits `platformPermissions` for **any** identity holding the operator role. loom/weaver get root-equivalent caps **for free** the instant their `holdsRole` link projects. You write **no cypher change**. If you find yourself editing the capability cypher (in `primordial.go`'s lens definition) to recognize `identity.system.loom`, you have violated this — the `class` must NOT gate capability.
- **Verify the projection is real, not assumed:** add an E2E (docker stack, Refractor running) that asserts `cap.identity.<loomId>` and `cap.identity.<weaverId>` materialize with the **same** `platformPermissions[]` set as `cap.identity.<adminId>` (the root-equivalence assertion). This is the proof that topology → projection works for the new actors.

### 0.4 A4 — Idempotence, kernel count, and readiness gate (AC #1, "When the platform starts … Then … exist"; Contract #7 §7.4/§7.5/§7.7)

- **Idempotence (§7.4):** the new entries inherit the existing op-tracker-present skip in `SeedPrimordial` (`if kv.Get(BootstrapOpKey) == nil → skip batch`) and the per-key concurrent-bootstrap fallback (`seedPrimordialPerKey`). **Add nothing** to the idempotence machinery — the new keys are just more `CreateOnly` ops in the existing batch, so a re-run is already a no-op for them. Add a test that re-running `SeedPrimordial` does not duplicate the loom/weaver vertices/links.
- **Kernel count (`verify-kernel`):** update the kernel-composition doc comment in `primordial.go` (the "Total ≈ 69 Core KV entries" block) and `scripts/verify-kernel.go`'s expected count by **exactly the number of new entries** (2 identity vertices + 2 holdsRole links = **+4**; the count becomes ~73 — verify the live count and state the exact delta). Add the 4 new keys to `PrimordialVertexKeys()` (`internal/bootstrap/nanoid.go`) so the kernel-verification gate enumerates them. **The kernel-count delta must be stated precisely in the closing summary and must match the gate.**
- **Readiness gate (§7.5) — note the live divergence from the contract:** Contract #7 §7.5 *specifies* the gate blocks until the bootstrap identity's `cap.*` projection lands. The **live implementation** (`internal/bootstrap/primordial.go` `WaitForBootstrapComplete`) currently polls **only** the Health-KV `health.bootstrap.complete` marker — it does **NOT** poll any per-actor `cap.*` key today. So there is no existing per-actor `cap.*` poll to "add a key to"; you are choosing between two faithful options:
  - **(a) Minimal, contract-divergence-preserving:** leave `WaitForBootstrapComplete` as-is (the Health-KV marker already gates `make up`, and the loom/weaver topology lands in the *same atomic batch* as everything else, so if bootstrap completed, the topology is present). The `cap.*` projections then materialize asynchronously via Refractor (as the admin's does today). This keeps 7.3 a pure-topology change and matches current behavior — but does **not** guarantee the engines' `cap.*` exists at `make up` exit.
  - **(b) Faithful-to-§7.5:** extend the readiness poll to additionally require `cap.identity.<loomId>` + `cap.identity.<weaverId>` (and, to actually honor §7.5, the bootstrap identity's `cap.*` too — closing the live divergence). This guarantees the engines can't start before their auth projection exists, at the cost of a real new poll against Capability KV.
  - **Recommendation: (b)** — the AC's intent ("so the engines can submit ops as authenticated actors") is best served by the engine's `cap.*` being ready at `make up` exit; and it's the chance to align the live gate with §7.5. **Keep the same configurable timeout + a clear error naming the missing key.** If (b) risks a hang or is non-trivial, fall back to (a) and **state the choice + rationale in the closing summary** (and flag the §7.5 divergence as a follow-up). Either way: **state exactly what you did and which option you chose.**

### 0.5 A5 — Auth parity: NO service-actor branch in step-3 (AC #3, the security-plane crux; binding)

*"An op submitted with `Lattice-Actor: identity:weaver` passes commit-path step-3 auth **identically to a human actor** (no special-case auth branch for service actors)."* Build to this literally:

- **Zero changes to `internal/processor/step3_auth_capability.go`** (or `step3_auth.go`, `step3_denial_response.go`). A service actor with no `authContext` flows through `Authorize → authorizeCapabilityPath → matchPlatformPermission`, reading `cap.identity.<weaverId>`, scanning `platformPermissions[]`, matching `scope:"any"` — **the identical path** a human operator takes. If step-3 needs *any* edit to make this work, something is wrong with the topology, not the auth code — fix the topology.
- **The proof test (the AC's literal assertion):** an op with `Actor: vtx.identity.<weaverId>`, no `authContext`, on an operation the operator role grants (`scope:"any"`, e.g. `CreateMetaVertex` or whatever the kernel grants) → **Authorized**, via the platform path, with **no** stub/special-case marker. Add the symmetric loom case. Assert the `Decision` came from `matchPlatformPermission` (the human path), not from any new code. **A diff that touches step-3 logic to accommodate service actors fails this AC.**
- **Adversarial coherence (Gate 3 must stay green):** seeding two new root-equivalent actors widens the root-cap surface. The capability-adversarial suite (`make test-capability-adversarial`) and bypass suite (`make test-bypass`) MUST stay green — prove that (a) the new actors get root **only via topology** (a non-loom identity with `class: "identity.system.loom"` but NO `holdsRole` link gets NO root cap — the §7.7 "class doesn't grant access" invariant, already a bypass test case for the bootstrap identity; add the loom/weaver analogue), and (b) the new actors do **not** open a cross-target/cross-actor bleed. If you add a Gate-2/Gate-3 test, give a one-line faithful-migration justification; do not weaken an existing one.

### 0.6 A6 — Key-shape, class, and no-history-comment conventions (Contract #1; CLAUDE.md)

- Identity vertices: `vtx.identity.<NanoID>` (4-segment is for aspects; vertices are 3-segment `vtx.<type>.<id>` — match the admin identity's key shape exactly). `class: "identity.system.loom"` / `"identity.system.weaver"` (arch §92's `identity.system.<...>` family; the admin is `identity` / the contract names `identity.system.bootstrap` + `identity.system.processor` — follow the `identity.system.*` convention).
- `holdsRole` links: 6-segment `lnk.identity.<loomId>.holdsRole.role.<RoleOperatorID>`. **Direction (Contract #1 §1.1): the later-arriving vertex is the source.** The role pre-exists; the identity arrives later → **identity = source, role = target**; reads "identity holdsRole role." This is the **identical direction** as the existing `BootstrapHoldsRoleLinkKey` (`vtx.identity.<adminId>` source → `vtx.role.<operatorId>` target) — copy it. Run the sentence test: "loom holdsRole operator" ✓.
- **No history/changelog comments (CLAUDE.md, most-violated rule).** Every comment describes what the code does **now**. **Never** `// Story 7.3 …`, `// added loom/weaver …`, `// was: …`, `// now also seeds …`, `// new in Phase 2 …`. git blame is the record. Contract refs (`// Contract #7 §7.2`, `// arch §92`) are fine; change-narration is not. The 7.2 CR had to strip such comments — do not reintroduce them.

### 0.7 Gates (all must pass before handing back)

`go build ./...` · `make vet` · `golangci-lint run ./...` · **`make verify-kernel`** (THE central gate here — the kernel composition changed; the count + key enumeration must match exactly) · `make test-bypass` (Gate 2, all BLOCKED — the new root actors must not open a bypass) · **`make test-capability-adversarial`** (Gate 3, all DEFENDED — seeding root-equivalent actors is a security-plane change; the suite MUST prove the new actors get root only via topology and open no bleed) · `go test ./internal/bootstrap/... ./internal/processor/... -count=1` · the Capability-Lens E2E in `internal/refractor/` (the new actors' `cap.*` projections must materialize — docker stack up, NATS `nats://localhost:4222`, Postgres DSN per the Makefile). **A full `make down && make up` cycle is the truest test of the readiness-gate + kernel-count change — run it if the stack supports it and report the result.** The docker stack is currently UP. Flake retry per Deviation 14 allowed; a flake claim without a re-run is a drift signal.

---

## 1. Story (user-facing)

As the **platform operator**,
I want **Loom and Weaver provisioned as internal service-actor identities at bootstrap**,
so that **the engines can submit operations as authenticated root-equivalent actors.**

## 2. Acceptance Criteria (faithful to the epic AC, line ~49)

1. **Given** primordial bootstrap (Contract #7), **When** the platform starts, **Then** `identity:loom` and `identity:weaver` service-actor vertices exist with **pre-provisioned signing keys** (Phase-2 reality: transport-level NATS credentials; the graph contribution is the identity vertex + root-role topology — see A2) and **root-equivalent capability** (their `cap.*` projection carries the operator role's `scope:"any"` `platformPermissions[]`, arch §92).
2. **And** their provisioning **extends the existing primordial bootstrap path** — the two identities + two `holdsRole` links ride the **same `buildPrimordialEntries()` atomic batch** that seeds the admin identity; **NO** parallel actor-provisioning mechanism (no second seeder, no post-bootstrap op, no package install, no direct `cap.*` write).
3. **And** an op submitted with `Lattice-Actor: identity:weaver` (and `identity:loom`) passes commit-path step-3 auth **identically to a human actor** — through `authorizeCapabilityPath → matchPlatformPermission` reading `cap.identity.<id>` — with **NO special-case auth branch for service actors** in step-3.
4. **And** (idempotence/readiness, Contract #7 §7.4/§7.5) re-running bootstrap does **not** duplicate the service actors, the kernel-verification gate enumerates the new keys with an updated count, and the `make up` readiness gate additionally confirms the loom + weaver `cap.*` projections before declaring ready.

## 3. Tasks / Subtasks

- [x] **T1 — Seed the two service-actor identity vertices** (AC #1, #2; A1, A6)
  - [x] Add `LoomIdentityID/Key` + `WeaverIdentityID/Key` package vars (`internal/bootstrap/nanoid.go`), mirroring `BootstrapIdentityID/Key`; generate their NanoIDs in the same place the admin NanoID is generated.
  - [x] In `buildPrimordialEntries()` (`primordial.go`), `add(...)` two `MakeVertexEnvelope` identity vertices: `class: "identity.system.loom"` / `"identity.system.weaver"`, `protected: true`, a present-tense note (no history comment). Place them with the admin identity in write order (§7.7).
- [x] **T2 — Seed the two `holdsRole → operator` links** (AC #1; A3, A6)
  - [x] `add(...)` two `MakeLinkEnvelope` holdsRole links (identity=source, operator role=target; copy `BootstrapHoldsRoleLinkKey`'s construction) at the end of the batch alongside the admin holdsRole link.
  - [x] Confirm **no** new role/permission/grantedBy/cypher edit is needed — root-equivalence comes entirely from the existing operator topology.
- [x] **T3 — Stable references + kernel enumeration + count** (AC #4; A4)
  - [x] Add `loomIdentity`/`weaverIdentity` to `PrimordialIDsRaw` + `BootstrapFile` JSON (`lattice.bootstrap.json`); wire generation + load. (Bootstrap-file version bumped 5 → 6.)
  - [x] Add the 4 new keys (2 vertices + 2 links) to `PrimordialVertexKeys()`.
  - [x] Bump the kernel-composition doc comment in `primordial.go` + the expected count in `scripts/verify-kernel.go` by exactly +4 (top-level keys 24 → 28; verify-kernel OK lines 85 → 89).
- [x] **T4 — Readiness gate** (AC #4; A4) — chose **option (b)**: `WaitForBootstrapComplete` now also polls `cap.identity.<adminId>`/`<loomId>`/`<weaverId>` with the same timeout + named-missing-key error, aligning the live gate with §7.5. Two-phase `make up` (seed pass defers the gate via `BOOTSTRAP_SKIP_READY_WAIT`, Refractor starts, second idempotent pass runs the gate) avoids the deadlock that a single-pass cap.* wait would cause.
- [x] **T5 — Idempotence test** (AC #4; A4): re-running `SeedPrimordial` is a no-op for the new entries (no duplicate vertices/links); the op-tracker-present skip and per-key fallback both cover them. Confirmed live (second `make up` idempotent).
- [x] **T6 — Auth-parity + projection + adversarial tests** (AC #1, #3; A3, A5) + all gates (§0.7) green.
  - [x] **Projection E2E:** `cap.identity.<loomId>`/`<weaverId>` materialize with the **same** `platformPermissions[]` set as `cap.identity.<adminId>` (root-equivalence proof).
  - [x] **Auth-parity:** an op with `Actor: <weaverId>` (and `<loomId>`), no `authContext`, on an operator-granted `scope:"any"` op → Authorized via `matchPlatformPermission` (the human path); **asserted zero step-3 code changed**.
  - [x] **Adversarial (§7.7 "class doesn't grant"):** an identity with `class: "identity.system.loom"` but NO `holdsRole` link gets NO root cap. Gate 2 + Gate 3 stay green.

## 4. Dev Notes

### Where things live (read these first — DS does the deep reads)
- **The path you extend:** `internal/bootstrap/primordial.go` — `buildPrimordialEntries()` is the single source of the primordial batch. Read the admin-identity block (`// 2. Primordial admin identity`, ~line 297), the operator-role + permissions blocks (~lines 459–521), the `grantedBy` links (~lines 523–545), and the **admin `holdsRole` link** (`// 10. Primordial admin → operator holdsRole link`, ~line 547) — that block is your **exact template** for T2. `SeedPrimordial` (~line 195) is the atomic-batch + idempotent-skip path; `seedPrimordialPerKey` (~line 250) is the concurrent fallback. The kernel-composition doc comment is ~lines 163–187 (the "Total ≈ 69" you bump).
- **Key/ID vars + config:** `internal/bootstrap/nanoid.go` — `BootstrapIdentityID/Key` (~lines 70–71) is your var template; `PrimordialIDsRaw` (~line 132, the `lattice.bootstrap.json` shape) + `BootstrapFile` (~line 171) take the new fields; `PrimordialVertexKeys()` (~line 444) is the `verify-kernel` enumeration you extend; generation/load is in the same file (`raw.BootstrapIdentity → BootstrapIdentityID`, ~lines 259/378, and `BootstrapIdentityKey = "vtx.identity." + BootstrapIdentityID`, ~line 413).
- **Kernel gate:** `scripts/verify-kernel.go` — the "~69 entries" expected count (~lines 7/23) and the `primordialKeys` loop (~line 89) that reads `PrimordialVertexKeys()`. Bump the count by your exact delta.
- **Readiness gate:** `cmd/bootstrap/main.go` (~lines 115–128, `WaitForBootstrapComplete`) + `internal/bootstrap/primordial.go` (`MarkBootstrapComplete` / `WaitForBootstrapComplete`, ~lines 857–880). Find the cap-projection poll (Contract #7 §7.5 says the gate blocks on the bootstrap identity's `cap.*`); extend it to the two new keys. If the live gate only checks the Health-KV marker + Refractor health (not a per-actor `cap.*` key), extend *that* — report which.
- **Auth (DO NOT EDIT):** `internal/processor/step3_auth_capability.go` — `Authorize` (~line 142) dispatches task/service/platform; `authorizeCapabilityPath` (~line 221) reads `cap.<actor>`; `matchPlatformPermission` (~line 352) scans `platformPermissions[]` for `scope:"any"`. `capabilityKeyFromActor` (~line 424) maps `vtx.identity.<id>` → `cap.identity.<id>`. The service actor takes this **unchanged** path. The envelope (`internal/processor/envelope.go`, `OperationEnvelope` ~line 50) has **no signature field** — confirm.
- **Capability cypher (DO NOT EDIT to add a class branch):** the bootstrap `capability` Lens definition lives in `primordial.go` (the lens-seeding block) / `internal/bootstrap/lenses.go`. It already walks `holdsRole → operator → grantedBy → permission` → `platformPermissions[].scope:"any"` uniformly (Contract #7 §7.7). loom/weaver project for free.
- **Adversarial template:** `internal/bypass/*` — the bootstrap-identity "class doesn't grant access" test (Contract #7 §7.7 / §7.2 test list) is your template for the loom/weaver analogue (`internal/bypass/capadv_*` for the Gate-3 vectors).

### The exact shapes you write (4 new Core KV entries)
```
vtx.identity.<loomId>     class="identity.system.loom"     protected=true    (root data: scalars only)
vtx.identity.<weaverId>   class="identity.system.weaver"   protected=true
lnk.identity.<loomId>.holdsRole.role.<RoleOperatorID>      (identity source → operator target)
lnk.identity.<weaverId>.holdsRole.role.<RoleOperatorID>
```
Everything else (operator role, scope:"any" permissions, grantedBy links, the capability cypher) **already exists** — you reuse it. That reuse is the whole point of AC #2.

### Why "no signing-key crypto" is correct, not a gap (A2 grounding)
The Processor never verifies a signature — auth is `actor` string → `cap.<actor>` projection. There is no Gateway in Phase 2; arch §92 says service actors *bypass* it. The "pre-provisioned signing key" is the engine's **NATS transport credential** (the account/nkey it uses to publish to `ops.system.>`), an arch-deferred-to-Stream-3 deployment concern — **not** a graph aspect and **not** a step-3 check. This story's graph job is the identity + root-role topology; the transport credential is Epic 8/9 + deployment wiring. (See Open Question 1 for the one confirmation.)

### Project Structure Notes
- This is a **pure `internal/bootstrap` + `cmd/bootstrap` + `scripts/verify-kernel.go` change**, plus **tests** in `internal/bootstrap`, `internal/processor`, `internal/bypass`, and an E2E in `internal/refractor`. **No package change** (`packages/*` untouched — these are kernel actors, not package data). **No `internal/processor` *production* change** (A5 — auth-parity is proven by tests, not by editing step-3). **No capability-cypher change** (A3). If your diff touches `step3_*.go` production code or the capability cypher, re-read A3/A5 — that's a design error.
- Dependency direction unchanged: nothing in `internal/processor` learns about loom/weaver; they are just two more identities in the uniform topology.

### References
- [Source: docs/contracts/07-primordial-bootstrap.md#7.2] — inventory item 7 ("Additional internal service actor identities for Loom, Weaver … seeded … following the same pattern") + item 8 (holdsRole topology links); §7.1 (topology only, no direct cap writes); §7.4 (idempotent re-run); §7.5 (readiness gate); §7.7 (write order; "class doesn't grant — topology does"; uniform cypher).
- [Source: docs/contracts/06-capability-kv.md#6.4] — `platformPermissions[]` `scope:"any"` = root-equivalent; §6.8 "No Entry = No Access" (Lens MUST project for internal service actors); §6.10 #4 (role → platformPermissions).
- [Source: docs/contracts/02-operation-envelope.md#2.8] — `authContext` platform path (task/service unset → `platformPermissions[]`); §2.3 (`system` lane reserved for service actors — see Open Question 2).
- [Source: _bmad-output/planning-artifacts/lattice-architecture.md#§92] — line 93 internal service actor model (root-level, bypass Gateway, pre-provisioned signing keys, `Lattice-Actor: identity:<service>`); line 1170 (service-actor vertices provisioned at bootstrap, `class` on root); line 984 (root-equivalent within trust boundary; AI agents are NOT service actors); lines 285/325 (key-provisioning mechanism deferred to Stream 3).
- [Source: internal/bootstrap/primordial.go] — `buildPrimordialEntries()`, the admin identity + admin holdsRole link blocks (the templates), `SeedPrimordial` idempotence.
- [Source: internal/bootstrap/nanoid.go] — `BootstrapIdentityID/Key` var pattern, `PrimordialIDsRaw`/`BootstrapFile` config, `PrimordialVertexKeys()`.
- [Source: internal/processor/step3_auth_capability.go] — `authorizeCapabilityPath`/`matchPlatformPermission` (the human path service actors share unchanged).

## 5. Test plan (concrete — count delivered tests from the diff)

- **Seeding (unit, `internal/bootstrap`):** `buildPrimordialEntries()` includes the 2 identity vertices (correct `class`, `protected:true`, correct key shape) + the 2 holdsRole links (correct 6-segment key, identity=source/operator=target direction). `PrimordialVertexKeys()` includes all 4. The kernel count matches `verify-kernel`'s expectation.
- **Idempotence:** re-running `SeedPrimordial` after the op tracker exists is a no-op (no duplicate loom/weaver entries); the per-key concurrent fallback skips existing loom/weaver keys.
- **Projection E2E (`internal/refractor`, docker):** after bootstrap + Refractor projection, `cap.identity.<loomId>` and `cap.identity.<weaverId>` exist and carry the **same** `platformPermissions[]` (`scope:"any"`) set as `cap.identity.<adminId>` (root-equivalence).
- **Auth parity (the AC crux, `internal/processor`):** an op with `Actor:<weaverId>`, no `authContext`, on an operator-granted op → `Decision.Authorized==true` via `matchPlatformPermission` (the platform/human path), not a stub or service branch. Symmetric for `<loomId>`. **Assert no step-3 production code changed** (the diff for `step3_*.go` production files is empty).
- **Adversarial / Gate coherence:** an identity with `class:"identity.system.loom"` but **no** `holdsRole` link gets **no** root cap (class ≠ capability; §7.7). `make test-bypass` (Gate 2, all BLOCKED) and `make test-capability-adversarial` (Gate 3, all DEFENDED) stay green — flag any security-test touch with a one-line faithful-migration justification.
- **Full-cycle (if stack supports):** `make down && make up` reaches ready state (the readiness gate now waits on the loom/weaver `cap.*` projections); `verify-kernel` passes with the new count. Report the result.
- **Gates:** every gate in §0.7 with its result (anything not run + why).

If you judge the readiness-gate change risks blocking `make up` (e.g. the existing gate doesn't poll per-actor `cap.*` and adding it is non-trivial), **halt and propose a split** (7.3a = seed identities + topology + kernel count/idempotence; 7.3b = readiness-gate extension) rather than landing a `make up` that hangs.

## 6. Closing summary (DS appends when done)

**Deliverables vs §0:** All in-scope items delivered. Two `identity.system.loom` / `.weaver`
vertices + two `holdsRole → operator` links seeded into the existing `buildPrimordialEntries()`
atomic batch (AC #2, no parallel mechanism). Root-equivalence = the operator role reused via
`holdsRole`; no new role/permission/grantedBy/cypher/step-3 (A1/A3). No direct `cap.*` writes —
projections come from the Refractor (A1).

**Kernel-count delta (exact):** top-level `PrimordialVertexKeys()` 24 → **28** (+2 identities,
+2 holdsRole links); `PrimordialVertexKeyCount` const 25 → **28** (the old 25 was pre-existing
drift of +1; corrected to the true count). `verify-kernel` OK lines ≈85 → **89**. Bootstrap-file
version 5 → **6**. All stated in code comments.

**Readiness-gate mechanism (ruling #4 / option b):** `WaitForBootstrapComplete` now blocks until
the Health-KV marker AND `cap.identity.<adminId>` + `<loomId>` + `<weaverId>` all exist, with the
same configurable ctx timeout and a named-missing-key error (times out cleanly, never hangs).
Because those projections are produced by the Refractor — which `make up` starts after seeding —
`make up` runs the bootstrap binary twice: a seed pass (`BOOTSTRAP_SKIP_READY_WAIT=1`, no wait),
then Refractor starts, then an idempotent second pass runs the gate. Live `make down && make up`
reached ready with `capProjections=3`; a second `make up` stayed idempotent (seeding skipped,
gate still passes, no duplicate actors).

**A2 signing-key decision:** **seed no key material** (Winston ruling #1) — the Processor does no
signature verification, there is no envelope `signature` field, and no Gateway; the "pre-provisioned
signing key" is the engine's NATS transport credential, deferred to Stream 3. Documented in
`primordial.go` + `docs/components/service-actors.md` (satisfied as transport creds, not dropped).

**`step3_*.go` + capability cypher byte-for-byte unchanged (AC #3 / A3+A5):** confirmed via
`git diff --stat internal/processor/step3_auth*.go internal/processor/step3_denial_response.go
internal/bootstrap/lenses.go` → empty. The §92 no-special-case invariant holds: loom/weaver pass
step-3 through the identical `authorizeCapabilityPath → matchPlatformPermission` platform path as a
human (proven by `service_actor_auth_parity_test.go`, asserting `Resolved.Path == "platform"`).

**`class`-non-gating proof (ruling #5):** No path gates on `class == "identity"`. The full engine's
`nodeMatches` resolves the `:identity` label from the **key type-segment first**; the actor
enumerator + `cap.*` envelope wrapper anchor on `ParseVertexKey`. Proven by
`internal/refractor/ruleengine/full/service_actor_class_test.go` (loom class WITH topology → operator
`scope:any` perms; same class WITHOUT topology → zero real perms) and the projection E2E
(`internal/refractor/service_actor_projection_e2e_test.go`: loom/weaver `cap.*` carry the SAME
`platformPermissions` set as admin's) + the auth-parity test (step-3 authorizes them identically).
No code was found gating on exact `class == "identity"` — nothing to flag.

**`system`-lane deferral (ruling #2):** noted in `docs/components/service-actors.md` — when lane
enforcement lands, the service-actor projection must include the `system` lane. No cypher/envelope
edit made (A3).

**Tests added (10 new test functions across 5 files):** 5 bootstrap unit (vertices/links/
enumeration/reuse/delta), 2 bootstrap embedded-NATS (idempotence + readiness-gate gating/timeout),
3 processor auth-parity (loom+weaver+human parity, identical-decision, class-no-cap denial),
1 refractor projection E2E (root-equivalence), 1 cypher-level class-non-gating.

**Gates (all run, all pass):**
- `go build ./...` — OK.
- `make vet` — OK.
- `golangci-lint run ./...` — **0 issues**.
- `make verify-kernel` — ALL ASSERTIONS PASSED (28 top-level keys; loom/weaver vertices + 3
  holdsRole links enumerated).
- `make test-bypass` (Gate 2) — **PASSED, 4/4 BLOCKED** (unchanged).
- `make test-capability-adversarial` (Gate 3) — **PASSED, 4/4 cleared (3 DEFENDED + 1
  ACCEPTED-WINDOW)** (unchanged — new root actors opened no bypass/bleed).
- `go test ./internal/bootstrap/... ./internal/processor/... ./internal/refractor/...` — all pass
  (one Deviation-14 `internal/processor/outbox` flake, green on single retry).
- Full `make down && make up` cycle — reached ready with the cap.* readiness gate; second `make up`
  idempotent.

**Gate-2/Gate-3 test changes:** none. The 4-vector rosters and report logic are untouched; the
class-non-gating adversarial proof lives in standalone tests (processor + ruleengine/full), not in
the gate roster — so no faithful-migration justification is needed. The regenerated
`gate2-report.txt`/`gate3-report.txt` (timestamp/commit churn only) were reverted.

**CAR filed:** none (no contract gap; built to frozen §7.1/§7.2/§7.5/§7.7).
**Deviations:** Deviation 14 flake on `internal/processor/outbox` (retried once, green). Pre-existing
`PrimordialVertexKeyCount` drift (+1) corrected to the true count.

## Dev Agent Record

### Agent Model Used
claude-opus-4-8 (Amelia, BMad senior dev)

### Debug Log References

- `internal/processor/outbox` hit the documented Deviation-14 infra flake (`meta.inf.tmp` /
  "no responders") on the first full-package run; passed cleanly on the single retry. Unrelated
  to this change (no outbox code touched).

### Completion Notes List

- Seeded Loom + Weaver identity vertices + their `holdsRole → operator` links into the EXISTING
  `buildPrimordialEntries()` atomic batch — no parallel provisioning mechanism (AC #2).
- Root-equivalence is the operator role reused via `holdsRole`; **no** new role / permission /
  grantedBy link / cypher branch / step-3 code. `internal/processor/step3_*.go` and
  `internal/bootstrap/lenses.go` (the capability cypher) are byte-for-byte unchanged (verified
  via `git diff --stat` → empty).
- A2 signing-key decision: **seed no key material** (Winston ruling #1). Documented in code +
  `docs/components/service-actors.md` that "pre-provisioned signing keys" = NATS transport creds
  deferred to Stream 3, not dropped.
- `system`-lane deferral (ruling #2) and the `class`-non-gating proof (ruling #5) documented in
  `docs/components/service-actors.md`. No code gates on `class == "identity"`: the full engine's
  `nodeMatches` resolves the `:identity` label from the key type-segment first, and the actor
  enumerator + envelope wrapper anchor on `ParseVertexKey`. Proven by
  `internal/refractor/ruleengine/full/service_actor_class_test.go` (loom class WITH topology →
  root caps; same class WITHOUT topology → zero perms).
- Readiness gate (ruling #4 / option b): `WaitForBootstrapComplete` now blocks on admin + loom +
  weaver `cap.*` with preserved timeout semantics. `make up` reordered into two bootstrap passes
  so the cap.* wait runs after Refractor is up (avoids deadlock). Live `make down && make up`
  reached ready with `capProjections=3`; second `make up` stayed idempotent.

### File List

- `internal/bootstrap/nanoid.go` (modified) — Loom/Weaver ID+Key vars, `PrimordialIDsRaw` +
  `BootstrapFile` v6 fields, generation/load/validation, derived link keys,
  `PrimordialVertexKeys()` + `PrimordialVertexKeyCount` (24→28).
- `internal/bootstrap/primordial.go` (modified) — two identity vertices + two `holdsRole` links
  in `buildPrimordialEntries()`; kernel-composition doc comment (≈69→≈73); `WaitForBootstrapComplete`
  extended to poll the three `cap.*` projections + `capabilityKeyForIdentity` helper.
- `cmd/bootstrap/main.go` (modified) — `BOOTSTRAP_SKIP_READY_WAIT` two-phase readiness phasing.
- `scripts/verify-kernel.go` (modified) — kernel-set doc comment (~69→~73 OK lines; +2 identities,
  +2 holdsRole links). Enumeration is dynamic via `PrimordialVertexKeys()`.
- `Makefile` (modified) — `up` two-phase bootstrap (seed pass + readiness pass with Refractor in
  between); `verify-kernel` expected-count comment corrected (≈33→≈89 OK lines).
- `docs/components/service-actors.md` (new) — service-actor provisioning, class-non-gating,
  signing-key interpretation, `system`-lane deferral, readiness-gate phasing.
- `internal/bootstrap/service_actor_test.go` (new) — vertex/link shape + direction, key
  enumeration, reuse-operator-role, +4 delta (unit).
- `internal/bootstrap/service_actor_e2e_test.go` (new) — `SeedPrimordial` idempotence +
  `WaitForBootstrapComplete` cap.* gating/timeout (embedded NATS).
- `internal/processor/service_actor_auth_parity_test.go` (new) — loom/weaver authorize identically
  to a human via `matchPlatformPermission`; class-alone-no-cap denial.
- `internal/refractor/service_actor_projection_e2e_test.go` (new) — loom/weaver `cap.*` project
  the same `platformPermissions` set as admin (root-equivalence, docker-style embedded NATS).
- `internal/refractor/ruleengine/full/service_actor_class_test.go` (new) — cypher-level §7.7
  proof: non-plain class + topology → root caps; class alone → none.

---

## Winston's Adjudication (all five RESOLVED — DS builds to these)

1. **Signing key → SEED NO KEY MATERIAL. ACCEPTED.** No signing-key aspect on the identity vertex, no envelope signature, no step-3 crypto. The auth model is `actor`-string → `cap.<actor>`; there is no envelope `signature` field and no Gateway in Phase 2. Arch §92's "pre-provisioned signing key" is the engine process's **NATS transport credential** for `ops.system.>`, deferred to Stream 3 / deployment (arch lines 285/325). Seeding unused crypto would be load-bearing-looking dead auth material — a smell. **Requirement:** the story/code must DOCUMENT this interpretation explicitly (the AC's "signing keys" is satisfied as transport-level creds wired in Epic 8/9 + deployment, NOT dropped), and note that when envelope-signature verification is ever added, these actors get key material at that time. Do not leave a silent gap.

2. **`system` lane → DEFER. ACCEPTED.** Out of scope for 7.3. Do NOT touch the capability cypher/envelope to grant the `system` lane (A3 forbids security-plane cypher edits, and `LaneUnauthorized` is unenforced live). **Requirement:** add a one-line note in the story that whenever lane enforcement lands, the service actors' projection must include the `system` lane — so this doesn't get lost.

3. **Refractor/Processor service identity → LOOM + WEAVER ONLY. ACCEPTED.** Build exactly to the AC. Loom and Weaver are the op-submitters; Processor/Refractor don't submit ops as actors. Additional service identities are a separate additive follow-up if a real need appears — do not widen this story.

4. **Readiness gate → OPTION (b): CLOSE IT HERE. ACCEPTED.** Extend `WaitForBootstrapComplete` to poll the `cap.*` projection of the bootstrap identity AND `identity:loom` / `identity:weaver` (aligning with frozen Contract #7 §7.5, which the live Health-KV-only poll currently violates). Rationale: 7.3's core guarantee is "engines authenticatable at startup" — if `make up` returns ready before their `cap.*` lands, the guarantee is hollow. This is the natural and correct place to fix the §7.5 divergence. **Requirements:** preserve the existing timeout / failure semantics (a missing projection must time out cleanly, never hang indefinitely beyond the current bound); poll the same way §7.5 intends; and TEST it — `make up` readiness must block until all three `cap.*` keys exist, and a second `make up` must stay idempotent (no duplicate actors, gate still passes). Flag loudly if the Refractor-projection dependency makes the gate flaky — but the fix is in-scope.

5. **`class` shape → `identity.system.loom` / `identity.system.weaver`. ACCEPTED** (faithful to Contract #7 §7.2 + arch §92; the `system` sub-class is the documented service-actor marker). **HARD REQUIREMENT — prove the non-gating invariant:** because the admin identity is plain `class: "identity"`, the DS MUST verify that NO projection or auth path gates on `class == "identity"` exactly. The capability cypher + actor enumerator anchor on the **vertex-type key segment** (`vtx.identity.<id>`) and on `holdsRole → operator`, NOT on the `class` field, and §7.7 says class must not gate capability — so the non-plain class must still (a) project a `cap.identity.<id>` doc identical in capability to the admin's, and (b) pass commit-path step-3 auth identically to a human actor. Prove BOTH with an E2E + keep Gate 2/3 green. If anything is found to gate on exact `class == "identity"`, that is itself a finding — flag it to Winston rather than silently falling back to plain `identity`.

---

## Open Questions (for Winston)

1. **"Pre-provisioned signing key" — seed no key material vs a placeholder credential aspect (the one real judgment call).** The Processor does **no** signature verification (no `signature` envelope field, no Gateway in Phase 2; auth is `actor` string → `cap.<actor>`). Arch §92's "pre-provisioned signing keys" is the engine's **NATS transport credential** for publishing to `ops.system.>` — an arch-explicitly-**deferred-to-Stream-3** deployment concern (arch lines 285/325), not a graph aspect or a step-3 check. So this story can satisfy AC #1's "with pre-provisioned signing keys" by seeding **only the identity vertex + root-role topology** and grounding the key as transport-level (the engine process holds NATS creds, wired in Epic 8/9 + deployment). **Recommendation: seed NO cryptographic key material on the identity vertex** (no signing-key aspect, no envelope signature, no step-3 verification) — it would be unused load-bearing-looking crypto that the adversarial suite can't exercise and that contradicts "deferred to Stream 3." The alternative (a documented non-load-bearing `credentialBinding` placeholder aspect on the service identity for Epic 8/9 forward-compat) is acceptable **only** if explicitly marked placeholder. **Confirm: seed-no-key-material.**

2. **`system` lane authorization in the projection.** Contract #2 §2.3 reserves the `system` lane for internal service actors, but the live capability projection hardcodes `lanes: ["default"]` for **every** actor (`internal/refractor/capabilityenv/envelope.go` `DefaultLanes`), and `LaneUnauthorized` is **defined but not enforced** in the live commit path (step-3 only emits AuthDenied/AuthContextMismatch). So in Phase 2 a service actor on the `system` lane is **not** blocked — but its projection nominally says `["default"]`, which is technically the wrong lane grant for a service actor. **Recommendation:** out of scope for 7.3 (the engines bind the `system` consumer in Epic 8/9; lane enforcement is a separate, unimplemented concern) — but flagging because the moment lane enforcement lands, the service actors' projection must include `system`. If Winston wants the projection correct-by-construction now, granting the `system` lane to operator-role holders is a small cypher/envelope change — but it touches the security plane and the capability cypher (which A3 forbids editing), so I recommend deferring with a note rather than expanding 7.3's blast radius.

3. **A `Refractor` service-actor identity.** Arch line 1170 lists "(Loom/Weaver/**Refractor**)" as service-actor vertices, and Contract #7 §7.2 names a `identity.system.processor` that the 4.7-minimized kernel does **not** currently seed (the kernel has only the admin identity). The 7.3 AC names **exactly `loom` + `weaver`**, so I scoped to those two. **Recommendation:** seed only loom + weaver per the AC; if Winston wants processor/refractor service identities too (for symmetric provenance / future internal ops), that's a tiny additive follow-up (same pattern, +2 more) — but I won't widen this AC unilaterally. Confirm loom+weaver only.

4. **Readiness gate diverges from Contract #7 §7.5 (live).** §7.5 specifies `make up` blocks until the bootstrap identity's `cap.*` projection lands; the live `WaitForBootstrapComplete` polls **only** the Health-KV `health.bootstrap.complete` marker (no `cap.*` poll). This is a pre-existing implementation/contract gap, not introduced by 7.3, but 7.3's "engines must be authenticatable at startup" intent is the natural place to close it (A4 option b adds the loom/weaver — and bootstrap — `cap.*` poll). **Recommendation:** take A4 option (b) and align the gate with §7.5 here; if Winston prefers to keep 7.3 pure-topology, take option (a) and I'll flag the §7.5 divergence as a standalone follow-up (it's a latent gap regardless of 7.3). Confirm (b).

5. **`class` value precision.** I specced `identity.system.loom` / `identity.system.weaver` (the `identity.system.*` family from Contract #7 §7.2's `identity.system.bootstrap`/`identity.system.processor` and arch §92's `identity:<service_name>`). The current admin identity is plain `class: "identity"` (the contract's `identity.system.bootstrap` was simplified in the 4.7 minimization). **Recommendation:** use `identity.system.loom`/`identity.system.weaver` (faithful to §7.2 + arch §92, and the `system` sub-class is the documented service-actor marker — even though `class` must NOT gate capability per §7.7). Confirm the `identity.system.*` class shape over plain `identity`.

---

## Winston — Code Review Adjudication (3-layer adversarial, 2026-06-05)

Three parallel review layers ran (Blind Hunter / Edge Case Hunter / Acceptance Auditor). **The security-plane crux was unanimously clean:** topology grants exactly operator-root to exactly the two intended keys; auth parity holds with **zero diff** to `step3_*.go` and the capability cypher (the no-special-case invariant is proven structurally, not just by a test); class non-gating proven; atomic-batch seeding + idempotence solid; both actors `protected`; no history/changelog comments; contract-conformant (§7.1/§7.4/§7.5/§7.7, §6.8). Triage of findings:

- **MUST-FIX — applied. Flaky readiness-gate test (Auditor, blocker):** `TestWaitForBootstrapComplete_BlocksOnServiceActorCapProjections` flaked ~40%. Root cause was twofold: (1) tight phase-2 budget (1500ms → widened to 10s), and (2) the new fail-fast `classify` (Blind #2 fix) treated a transient embedded-NATS request timeout (`nats.ErrTimeout`/`ErrNoResponders`, *not* `context.DeadlineExceeded`) as a fatal error, breaking the phase's `"timed out"` assertion. Fix: a readiness gate must wait *through* transient NATS blips, so those sentinels now keep polling (a persistent failure still surfaces as a clean timeout). **Verified 20/20 deterministic.**
- **MUST-FIX — applied. Readiness-gate bypass via ambient env var (Blind #1, HIGH):** `BOOTSTRAP_SKIP_READY_WAIT` (any non-empty value skipped the gate with a success exit) was an auth-readiness bypass an exported env var could silently trigger on a real `make up`. Converted to an explicit per-invocation `-skip-ready-wait` CLI flag carried **only** by the Makefile seed pass; the wait pass honors no ambient signal, and skipping now logs at WARN.
- **Fixed. Warm-rerun double daemon (Edge):** `make up` started Refractor/Processor with `&` but only `make down` killed them → a warm `make up` left duplicates racing the CDC stream. Added `pkill` for both at the top of `up:`.
- **Fixed. `checkAll` transport-error handling (Blind #2):** a non-NotFound Get error no longer burns the full timeout masquerading as a missing projection — `classify` fails fast on genuinely unexpected errors while keep-polling on NotFound / context / transient-NATS.
- **Fixed. Dead `PrimordialVertexKeyCount` (Edge/Auditor):** wired into `verify-kernel` as `len(PrimordialVertexKeys()) == PrimordialVertexKeyCount` so the kernel count can't silently re-drift.
- **Fixed. `data.note` prose (Blind #5):** the seeded service-actor `note` fields are plain functional descriptions (no arch-§ narration on a security vertex).
- **Accepted by-design — documented. Version-bump upgrade model (Edge):** a bootstrap-file version bump requires `make down && make up` (full teardown/reseed; `make down` wipes ephemeral volumes + the JSON, so no admin duplication and new NanoIDs). Consistent with all prior bumps; no in-place migration in Phase 2. Made explicit in the `checkVersion` error string and `docs/components/service-actors.md`.
- **Accepted-risk — noted. Gate checks key *presence*, not non-empty `platformPermissions` (Blind #3, LOW):** because the identity + its `holdsRole` link commit in the *same atomic batch*, an empty-perms transient projection is implausible in production (the link is present whenever the actor projects). Not strengthened to avoid adding doc-body parsing to the gate.
- **Accepted — noted. Auth-parity unit test uses hand-built docs (Blind #4, LOW):** the real projection→auth chain is covered by `service_actor_projection_e2e_test.go` (asserts loom/weaver `platformPermissions` == admin's); the unit test proves the authorizer doesn't special-case the actor string. Coverage is adequate across the two.

**Verification gates (run by Winston, all green):** `go build ./...`, `make vet`, `golangci-lint run ./...` (0 issues), `make verify-kernel` (28 keys), `make test-bypass` (Gate 2 — 4/4 BLOCKED), `make test-capability-adversarial` (Gate 3 — 4/4: 3 DEFENDED, 1 ACCEPTED-WINDOW = pre-existing projection-lag vector), and `go test ./internal/bootstrap/... ./internal/processor/ ./internal/refractor/...` (all pass; readiness-gate test 20/20 deterministic).
