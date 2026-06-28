# Lane Authorization Enforcement (Contract #2 §2.3) — design

**Status: 📐 awaiting-Andrew (ratification)** · Designer (Winston) · 2026-06-28
Backlog row: `planning-artifacts/backlog/lattice.md` → *Security & trust boundary* → "Lane authorization
enforcement (Contract #2 §2.3)". Surveyor-filed 2026-06-28 (grounded: `LaneUnauthorized` defined-but-never-emitted).

---

## For Andrew (one-look ratification)

**What it does (two lines).** Turns on the per-lane submission check Contract #2 §2.3 already mandates: step 3
rejects a submission to a lane the actor was not granted (`LaneUnauthorized`) — closing the gap that lets *any*
actor submit to `meta` / `urgent` / `system` today. The lane *grant* already has a home (`doc.lanes`, parsed but
never read); this design fills it with real grants and reads it.

**No frozen-contract change.** §2.3 fully specifies the behavior ("submitting to a lane is itself
capability-controlled … rejected at commit step 3 before any further processing"); §2.6 already enumerates
`LaneUnauthorized`. This is **build-to**, not change. No `docs/contracts/*` edit is staged.

**No architectural fork.** This is application-layer authZ at step 3, on the existing Capability-KV read-model.
It does not touch the Gateway / read-path-auth (D1) / NATS-account (transport) planes (it is the application-layer
sibling of the NATS write-restriction's transport-layer lane control — see §7).

**The one design decision I'm flagging** (within §2.3's latitude, *not* a contract change): the **service** and
**task** auth paths confer the **`default` lane only** (implicit), rather than reading a projected lane grant.
Rationale + the considered alternatives are in §5. My recommendation is the implicit-default rule; the alternative
(a projected lane grant on every path) costs an extra KV read per op for uniformity we don't need yet. **If you
prefer per-path projected lane grants, that flips Fire 1's scope** — say so and I'll re-cut it.

**Migration is order-dependent (not a fork, but load-bearing).** Grants must converge *before* enforcement turns
on, or the engines (Loom/Weaver/Bridge, which submit on `system`) and the install tooling (which submits on
`meta`) break. The decomposition (§8) puts grants in Fire 1 (dark, no behavior change) and enforcement in Fire 2.

---

## 1. Problem + intent

Contract #2 §2.3 reserves four lanes (`default` / `meta` / `urgent` / `system`) and states plainly:

> **Lane authorization:** Submitting to a lane is itself capability-controlled. The Capability Lens grants
> per-lane submission rights. Most actors hold `default` only. `meta` requires operator/admin capability.
> `urgent` requires explicit grant. `system` is reserved for internal service actors. A submission to a lane the
> actor lacks capability for is rejected at commit step 3 (auth check) before any further processing.

**This is entirely unenforced.** The plumbing exists but the check does not:

- `ErrCodeLaneUnauthorized` is **defined** (`internal/processor/envelope.go:72`), reserved in the contract
  (§2.6 error table, `02-operation-envelope.md:208`), and listed in the conformance valid-code set
  (`conformance_test.go:103`).
- `CapabilityDoc.Lanes []string` is **parsed** (`capability_doc.go:30`) on every Authorize call.
- The producer **projects** it: the core capability lens emits `lanes` (`bootstrap/lenses.go:69`), the projection
  driver writes it (`projection/driver.go:121`).
- …but **step 3 never reads `env.Lane` against `doc.Lanes`.** In `step3_auth_capability.go` the only mentions of
  lane are a log field (`step3_auth.go:98`); the `Authorize` hot path selects a dispatch entry, reads its one KV
  doc, and runs the `operationType` matcher — the lane is never consulted. `LaneUnauthorized` is never returned by
  any production path.

Net: **any actor can submit to any lane.** A non-operator can submit on `meta` (the serialized DDL lane), or
flood `urgent` / `system` to starve time-sensitive business ops and internal automation.

**Why it matters (the harm).** This is primarily a **resource-isolation / queue-fairness** control and
secondarily **defense-in-depth** on DDL serialization:

- The lanes exist *to* isolate priority classes (§2.3; arch line 63: "DDL mutations are serialized via `ops.meta.>`
  lane"; brainstorm #71/#72 lane subjects + consumer wiring; #114 fairness across lanes). Unenforced, the
  isolation is advisory — an actor defeats it by lying about its lane.
- A non-meta op submitted on `ops.meta.>` rides the serialized (concurrency-1) consumer, a DoS lever on the DDL
  path; `urgent`/`system` floods defeat the priority weighting the per-lane consumers exist to provide.
- It is **defense-in-depth**, not the sole guard, for privileged DDL: `CreateMetaVertex` / `InstallPackage` are
  *already* operationType-authorized (only operators hold those platform permissions), so an ordinary actor's
  *privileged op* on `meta` is already denied at the operationType matcher. Lane authZ adds the orthogonal control
  that even a *benign* op may not ride a lane the actor wasn't granted.

This is the application-layer half of "authorization actually denies"; the NATS-account write-restriction
(Andrew-ratified, Fire 1 shipped) is the transport-layer half (see §7).

## 2. Reconciliation with the existing model (pre-empting "didn't we already…?")

- **"Don't we already project lanes?"** Yes — the *field* and the *error code* exist; only the **check** and the
  **real grants** are missing. The core `cap.<actor>` doc carries `lanes` but hardcoded to `["default"]`
  (`bootstrap/lenses.go:69`) — wrong even for the system actors that submit on `system`. The rbac
  `cap.roles.<actor>` doc (ordinary actors) carries **no `lanes` field at all** (`rbac-domain/lenses.go` —
  `BodyColumns` is `["platformPermissions","roles"]`, no lanes). So this is **build-to §2.3**, not new shape.

- **"Doesn't the per-lane-consumers item cover this?"** No — that item (📐 awaiting-Andrew,
  `processor-per-lane-consumers-design.md`) is lane **routing / throughput / lag** (one `ConsumerSupervisor` per
  lane). This is lane **authorization** (who may submit to a lane). Disjoint concerns; they compose but neither
  depends on the other.

- **"Doesn't the NATS write-restriction cover this?"** No — that closes the **transport** plane (which NATS
  *connection* may publish to `ops.<lane>.>`). This closes the **application** plane (which *actor* — identified
  by `env.Actor`, not by connection — may submit to a lane). A trusted component connection (the bridge/engines)
  legitimately publishes to `ops.system.>` on behalf of a system actor; per-actor lane authZ still applies at
  step 3. See §7 for how the two planes relate.

- **"Does this introduce new state?"** No new vertices/aspects/links and no new ops. The grant lives in the
  Capability-KV lens projection (auth read-model) that already exists; the only state delta is *which values* the
  `lanes` array carries.

## 3. The shape

### 3.1 Lane authority = the actor's standing capability doc

A lane grant is a property of the **actor**, not of the auth dispatch path. The actor's *standing identity*
capability doc is the lane authority:

- **Platform path** — `cap.<actor>` (kernel-seeded protected system actors) or `cap.roles.<actor>` (ordinary
  actors, rbac-projected). The lane authority is `doc.lanes`, **already fetched** by the platform dispatch — zero
  extra read.
- **Service path** (`authContext.service` set → `cap.svc.<actor>`) and **Task path** (`authContext.task` set →
  `cap.ephemeral.<actor>`): these scoped grants confer the **`default` lane only** (implicit). They carry no lane
  grant and need none — a service-access or ephemeral-task grant is a *business-level* authorization with no
  reason to confer a privileged (`meta`/`urgent`/`system`) lane. A service/task op on a non-default lane is
  rejected `LaneUnauthorized` with **no extra read**. (This is the design decision flagged in *For Andrew*;
  alternatives in §5; extension path in §9.)

This is faithful to §2.3 ("most actors hold `default` only") and to the codebase's reality: **every non-default
lane in use today is on the platform path** —

| Submitter | Lane | Auth path | Doc | Grounding |
|---|---|---|---|---|
| Loom / Weaver / Bridge | `system` | platform | `cap.<actor>` (protected) | `loom/engine.go:142`, `weaver/engine.go:151`, `bridge/engine.go:127`; `actuator.go` sets only `authContext.Target` |
| `lattice-pkg install` / `lattice lens` (admin) | `meta` | platform | `cap.<actor>` (protected admin) | `pkgmgr/installer.go:313`, `cmd/lattice/lens/lens.go:145,217` |
| Loupe / verticals / ordinary business ops | `default` | platform / service | `cap.roles.<actor>` / `cap.svc.<actor>` | `cmd/loupe/op.go:38`, vertical apps |

There is **no** legitimate service-path or task-path submitter on a non-default lane today. The implicit-default
rule therefore loses nothing real.

### 3.2 The step-3 lane gate (write-path authZ, P2-adjacent)

`CapabilityAuthorizer.Authorize` (`step3_auth_capability.go`) gains a lane gate. The lane check runs **before the
`operationType` matcher** (§2.3 "before any further processing"):

1. The existing `service && task` mutual-exclusion check (unchanged).
2. `selectEntry(ac)` — pure function of `authContext`, no read (unchanged).
3. **Lane gate (new):**
   - If the selected entry is the **service** or **task** path: if `env.Lane != LaneDefault`, return
     `Decision{Authorized:false, Code: ErrCodeLaneUnauthorized}` **immediately** (no read — these paths grant
     `default` only).
   - If the **platform** path: proceed to the existing KV GET + parse, then **before** `matchPlatformPermission`,
     check `env.Lane ∈ doc.Lanes`. On a miss, return `LaneUnauthorized`. On a hit, fall through to the
     operationType matcher unchanged.
4. The `operationType` matcher (unchanged).

`LaneDefault` submissions are unaffected on the service/task paths (no read added); the platform path's single KV
GET is unchanged (it reads `doc.lanes` from the doc it already fetched). **The documented hot-path "exactly one KV
GET per Authorize call" invariant is preserved** (`step3_auth_capability.go:7`).

`StubAuthorizer` (AuthModeStub) bypasses the gate as it bypasses all auth — already a known, alarmed degraded mode
(`step3_auth.go:93`); no new exposure.

**Fail-closed.** An empty/absent `doc.lanes` on the platform path denies *every* lane (including `default`) —
correct fail-closed behavior. This makes lane-grant projection correctness load-bearing (auth correctness =
projection correctness, arch line 38); §6 + §8 ensure every platform-path actor's doc carries its lanes before
enforcement.

### 3.3 The grants (read-path, P5 — Capability-KV lens projection)

Lanes are projected by the same lenses that already project the actor's capabilities (Contract #6 §6.1
contribution model). **Reads = lenses; writes = ops** — no new write path; the grant is a projected value.

- **Core `cap.<actor>` (protected system actors)** — `CapabilityLensDefinition().Output.Lanes`
  (`bootstrap/lenses.go:69`) changes `["default"]` → `["default","meta","urgent","system"]`. The core cap lens
  projects a **uniform** root-grant set for *all* protected kernel actors (admin + Loom + Weaver + Bridge), which
  already share the full platform-permission set (`bootstrap/lenses.go:79-92`); granting them all four lanes is
  consistent with that existing "they are core kernel root" posture and is the minimal change. (Per-actor lane
  scoping — e.g. Loom needs only `system`+`default`, not `meta` — is a future refinement, §9; it is not an
  escalation because these actors already hold full root `platformPermissions`.)

- **rbac `cap.roles.<actor>` (ordinary actors)** — `capabilityRoles` lens
  (`rbac-domain/lenses.go`) gains `Output.Lanes: ["default"]` (a static baseline on the `OutputDescriptorSpec`,
  exactly like core's). Every ordinary role-holder thereby gets `default` — "most actors hold `default` only"
  (§2.3). **No cypher change** (the baseline is a static descriptor field, not a graph-derived collection),
  keeping Fire 1 low-risk. Role-derived *privileged* lanes for ordinary operators (operator role → `meta`/`urgent`)
  are deferred (§9) — no ordinary actor submits a non-default lane today (installs run as the primordial admin).

No change to `cap.svc.<actor>` (service-location) or `cap.ephemeral.<actor>` (orchestration-base) — those paths
are implicit-default (§3.1).

### 3.4 Orchestration

None. No Loom pattern / Weaver convergence lens / temporal lane / directOp. The change is a step-3 gate + two
static descriptor edits + their version bumps.

## 4. Contract surface

**No frozen-contract change.** §2.3 specifies the grant rubric and the step-3 rejection; §2.6 enumerates
`LaneUnauthorized`. The design *builds to* both. No `docs/contracts/*` edit is staged for this design.

The one §2.3-interpretation decision (service/task ⇒ implicit `default`) sits within the contract's latitude:
those paths *are* capability-controlled (non-default is denied); their grant is simply a fixed `{default}` rather
than a projected value. If Andrew reads §2.3's "the Capability Lens grants per-lane submission rights" as
*requiring* a projected grant on every path, that is the alternative in §5.2 (Fire 1 then projects lanes onto
`cap.svc`/`cap.ephemeral` or the gate reads the standing doc) — still no contract change, just a scope shift.

## 5. Alternatives considered

### 5.1 Recommended — implicit `default` for service/task; `doc.lanes` for platform (§3)

Zero extra KV reads; minimal projection churn (two static descriptor edits); semantically honest (non-default
lanes are standing-identity privileges); extensible (§9). **Recommended.**

### 5.2 Dedicated `cap.lanes.<actor>` lane-grant doc, read up front on every path

The textbook §6.1 decomposition: a disjoint lane-grant key, read once at the *start* of step 3 (matching §2.3
"before any further processing") uniformly for all paths, projected by core (system actors) + rbac (ordinary).

*Could a variant beat the recommendation?* It buys true per-path lane grants and a uniform check — but at the cost
of **+1 mandatory KV GET on every operation** (doubling auth-plane reads, breaking the documented one-GET
invariant). The benefit (service/task ops on non-default lanes) has **no consumer today**. If one ever appears,
the cleaner fix is a *targeted* standing-doc read on that path (§9), not a universal pre-read on all paths. So
even the variant loses now. **Rejected** (over-built for a non-existent need; performance regression on the hot
path).

### 5.3 Denormalize `lanes` onto all four docs (cap.svc / cap.ephemeral project `["default"]`)

Behaviorally identical to the recommendation (those paths would carry `["default"]`), but achieved with extra
projection writes across two more packages (service-location, orchestration-base) that today don't touch lanes.
Strictly more work and more cross-package coupling for the same behavior. **Rejected** (the implicit-default rule
*is* this, without the writes).

### 5.4 Force an `operationType → lane` binding (e.g. meta ops must ride `meta`)

Out of scope and contrary to §2.3: the contract controls an actor's *right to submit to a declared lane*, it does
not bind an op to a lane. The lane is the submitter's declaration, validated against grants. (A meta op submitted
on `default` is fine if the actor holds `default` and the op's `operationType` is authorized.) **Rejected** (not
what §2.3 says; would break installs that legitimately ride `meta` only because the admin chose it).

## 6. Migration / compatibility

The hazard is **enforcement before grants** → the engines and install tooling break the instant the gate goes
live. Ordering is therefore load-bearing:

1. **Grants converge first (Fire 1).** Core `cap.<actor>` and rbac `cap.roles.<actor>` carry their lanes; the gate
   is not yet present, so the projected lanes are *unread* — **zero behavior change**. On a fresh stack
   (CI / `make up`) the new descriptors install with the bootstrap/rbac version bumps. On a **live** stack the
   Refractor re-projects on the meta-lane DDL commit; the `make up` readiness gate already blocks on the
   admin/Loom/Weaver/Bridge `cap.*` projections existing (service-actors.md "Readiness gate"; Contract #7 §7.5),
   which now carry lanes.
2. **Enforcement turns on (Fire 2)**, gated on Fire 1 being live.

**F-004 dev-loop caveat.** Editing the rbac `capabilityRoles` descriptor at the *same* package version is silently
skipped on a long-lived dev stack (the known F-004 tax); a clean apply needs `make down && make up` (or the
`UpgradePackage` path once it ships). Bump the rbac package version so a fresh install picks it up; for live dev
stacks, rebuild. (Irrelevant to CI, which builds fresh.)

**rbac-absent degradation.** With rbac uninstalled, ordinary actors have no `cap.roles.<actor>` doc → platform
path denies by absence *today* (§6.8) — they can do nothing regardless of lane. Enforcement does not worsen this.
System actors (`cap.<actor>`) are core-seeded and unaffected by rbac's presence.

**Bootstrap version bump.** Changing `CapabilityLensDefinition().Output.Lanes` changes the seeded lens definition →
bootstrap version bump (the lens DDL is part of the primordial graph; `verify-kernel` / count-agreement tests
will need their expected values updated, as with prior lens-shape changes).

## 7. Relationship to the transport plane (NATS write-restriction)

The Andrew-ratified **NATS account-level write-restriction** (Fire 1 shipped, `75e9acc`) scopes which *NATS
connection* may publish to which subject — including, under its §3.2 permission matrix, who may publish to
`ops.<lane>.>`. These are **complementary, not redundant**:

- **Transport plane (NATS):** which *connection* (component credential) may publish `ops.system.>`. Coarse,
  per-component, at the door. A compromised non-engine connection can't even reach the subject.
- **Application plane (this design):** which *actor* (`env.Actor`, independent of connection) may submit to a lane.
  Fine, per-actor, at step 3. A trusted engine connection publishing `ops.system.>` on behalf of a *non*-system
  actor is still caught here.

They can ship independently (no ordering dependency). Defense-in-depth: transport authZ narrows *who can speak on
the wire*; lane authZ narrows *whose operations are honored*. Both are needed because `env.Actor` is not bound to
the connection (no envelope signature — service-actors.md; that binding is the deferred Gateway/signature work).

## 8. Decomposition for the Lattice Steward

Two order-dependent increments (each independently shippable + green). They *may* land in one shipment on a fresh
stack, but on a live upgrade Fire 1 must converge before Fire 2 — so they are sequenced.

### Fire 1 — Grants (dark; migration-safe; thorough-lead review)
- `bootstrap/lenses.go`: `CapabilityLensDefinition().Output.Lanes` → `["default","meta","urgent","system"]`;
  bootstrap version bump + update `verify-kernel`/count-agreement expectations.
- `rbac-domain/lenses.go`: `capabilityRoles` `Output.Lanes: ["default"]`; rbac package version bump.
- Producer tests: core cap lens emits the four lanes for a protected actor; `cap.roles` emits `["default"]` for an
  ordinary role-holder (extend the existing `refractor_capability_e2e_test.go` / `capability_lens_contract_test.go`
  assertions — they currently assert `ElementsMatch(["default"], lanes)`, which must flip for `cap.<actor>`).
- **No gate, no enforcement** — projected lanes are unread, so behavior is byte-identical. Independently green.
- *Is this dead scaffolding?* No — it is **migration ordering**, not speculative inert machinery: the grants are
  consumed by the very next increment, and the split exists solely so a live stack converges grants before the
  check. (On a fresh stack Fire 1+2 could even be one PR; the *order* is what's load-bearing.)

### Fire 2 — Enforcement + adversarial proof (security turn-on; full 3-layer review)
- `step3_auth_capability.go`: the lane gate (§3.2) — service/task ⇒ default-only (pre-read reject on non-default);
  platform ⇒ `env.Lane ∈ doc.Lanes` (post-parse, pre-matcher); emit `ErrCodeLaneUnauthorized`.
- Unit table: platform lane∈/∉ doc.Lanes; service+non-default → `LaneUnauthorized`; task+non-default →
  `LaneUnauthorized`; service/task+default → pass; stub bypass; empty-doc-lanes → deny default (fail-closed).
- **Gate-3 adversarial vector** (the backlog's explicit ask): an ordinary `default`-only actor submits an op on
  `system` / `meta` / `urgent` → **BLOCKED** with `LaneUnauthorized` (DEFENDED). Add to
  `make test-capability-adversarial`.
- e2e (ephemeral stack): a system actor submits on `system` and **succeeds** (post-grant); a `default`-only actor's
  `system`-lane submit is **rejected** `LaneUnauthorized`. Reuses the embedded-NATS + `jsstore.Dir(t)` pattern.
- Doc: retire the `service-actors.md` "## `system` lane — deferred" section (the deferral it tracks is now closed);
  note the live behavior in `docs/components/processor.md` (or wherever step-3 auth is documented).
- Gated on Fire 1 converged (verify the `cap.*` docs carry their lanes before flipping).

### (Optional) Fire 3 — role-derived privileged lanes for ordinary operators
Only if/when a *non*-protected operator must ride `meta`/`urgent` (today installs run as the primordial admin, so
this blocks nothing). Map the rbac operator role → `["default","meta","urgent"]` via the role's grant topology
(see §9 for the mechanism). Independent, additive.

## 9. Open questions — resolved

1. **Where does the lane grant live for service/task paths?** Implicit `default` (§3.1, §5.1). *Resolved:
   recommend implicit-default; flagged for Andrew (§ For Andrew).*
2. **One uniform pre-read vs. path-specific?** Path-specific, preserving the one-GET invariant (§3.2, §5.2 rejected).
   *Resolved.*
3. **Do system actors get per-actor lane scoping (Loom ≠ admin)?** No — uniform `["default","meta","urgent",
   "system"]` for all protected actors, consistent with their existing uniform root `platformPermissions`; not an
   escalation (§3.3). Per-actor scoping is a future refinement, naturally landing if/when the system-actor root
   grants are decomposed. *Resolved.*
4. **How do ordinary operators get `meta`?** Today via the **primordial admin** (`cap.<actor>`, full lanes) running
   installs — covered by Fire 1. A *non-protected* operator riding `meta` is deferred to the optional Fire 3
   (role→lane projection), as no such flow exists today. *Resolved (deferred, blocks nothing).*
5. **Lane-check ordering vs. operationType auth — info leak?** Lane-first (§2.3 "before any further processing").
   The minor oracle (distinguishing "lane not granted" from "op not authorized") is acceptable — lane grants are
   not secret, and it is no worse than today's per-code denial responses. *Resolved.*

*Extension path (for the future, not built):* if a service/task op ever legitimately needs a non-default lane,
add a *targeted* standing-doc read on that path (read `cap.roles.<actor>`/`cap.<actor>` for its lanes) — strictly
narrower than §5.2's universal pre-read, and only on the path that needs it.

## 10. Adversarial review (self-red-team; folded in)

A focused adversarial pass over the design (the security-plane substitute for a full party-mode in this unattended
fire; findings folded into §3/§6/§8 above):

1. **Ordering attack — enforcement before grants converge.** Live-stack hazard: flip the gate while a system
   actor's `cap.<actor>` still says `["default"]` → Loom/Weaver/Bridge denied `system` → orchestration deadlock
   (no result/dispatch ops commit). *Mitigation:* the Fire 1/Fire 2 split + the Fire 2 precondition (verify
   `cap.*` carry lanes) + the existing `make up` readiness gate on `cap.*` projections (§6).
2. **Self-route to the implicit-default path for escalation?** An attacker setting `authContext.service`/`.task` to
   reach the implicit-default path *still* can only submit `default` there (non-default is rejected pre-read). They
   cannot use path routing to *gain* a non-default lane. Routing to the platform path reads *their own*
   `cap.roles.<them>` → `default` only. **No escalation vector.** *Confirmed safe.*
3. **Fail-closed self-DoS.** A projection bug emitting empty `doc.lanes` for a real actor denies them every lane
   (incl. `default`). This is correct fail-closed, but elevates projection correctness to auth correctness
   (arch line 38). *Mitigation:* Fire 1 producer tests + the readiness gate; the failure is loud (every op
   `LaneUnauthorized`), not silent.
4. **Stub bypass.** AuthModeStub skips the gate (as it skips all auth). Already an alarmed, non-production mode
   (`stub-auth-active` Health alert). *No new exposure.*
5. **meta-lane install by a non-protected operator (deferred Fire 3).** Such an operator would be denied `meta` on
   `cap.roles` (default-only). *Today this blocks nothing* (installs run as the primordial admin). Flagged so the
   deferral is a known, bounded gap, not a surprise.
6. **Loose uniform grant to protected actors.** Granting Loom `meta`/`urgent` is wider than it needs. Not an
   escalation (it already holds full root `platformPermissions`), but noted as a tightening opportunity (§9 Q3).

## 11. Test strategy (summary)

- **Unit** (`step3_auth_capability_test.go`): the lane-gate table (§8 Fire 2). The producer tests (§8 Fire 1).
- **Conformance** (`conformance_test.go`): `LaneUnauthorized` is already in the valid-code set; add a behavioral
  assertion that a non-granted-lane submission yields it.
- **Gate 3** (`make test-capability-adversarial`): the new ungranted-lane vector → DEFENDED (§8 Fire 2).
- **e2e** (ephemeral stack): system-actor `system`-lane success + default-actor `system`-lane rejection (§8 Fire 2).
- **Standard gates:** `go build ./...`, `make vet`, `golangci-lint run ./...`, `make verify-kernel`,
  `make verify-package-*` (rbac + the version bumps), `make test-bypass` (Gate 2), the relevant `go test` packages.

---

### Grounding index (for the Steward)
- `internal/processor/step3_auth_capability.go` (the Authorize hot path; the one-GET invariant @ top), `step3_auth.go`
  (lane log field @ :98; AuthMode/Stub), `capability_doc.go:30` (parsed `Lanes`), `envelope.go:72`
  (`ErrCodeLaneUnauthorized`), `step3_denial_response.go` (denial builder).
- `internal/bootstrap/lenses.go:53-119` (core `CapabilityLensDefinition`; `Output.Lanes` @ :69).
- `packages/rbac-domain/lenses.go` (`capabilityRoles`; no lanes today).
- `internal/refractor/projection/driver.go:121` + `lens/corekv_source.go:123` (lanes emission).
- `docs/contracts/02-operation-envelope.md` §2.3 (lane authZ), §2.6 (`LaneUnauthorized` @ :208).
- `docs/components/service-actors.md` ("## `system` lane — deferred"; "Readiness gate").
- Submitters: `internal/{loom,weaver,bridge}/engine.go` (lane `"system"`), `internal/pkgmgr/installer.go:313` +
  `cmd/lattice/lens/lens.go:145,217` (lane `meta`), `cmd/loupe/op.go:38` (default).
