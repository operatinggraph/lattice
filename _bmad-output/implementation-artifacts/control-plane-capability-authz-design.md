# Control-plane Capability authorization (FR30) — design

**Status: ✅ Andrew-ratified (2026-06-27).** Author: Winston (Designer fire, 2026-06-27).

> **Ratification decisions (Andrew, 2026-06-27) — these supersede the three-option fork below:**
> 1. **Sequencing: deferred behind D1.** D1 (read-path auth) is higher priority (a larger surface) and
>    builds the shared `internal/gateway/auth` JWT seam. The control plane stays at its current posture
>    in the interim — **acceptable because #1 (NATS account write restriction, ratified) already restricts
>    `lattice.ctrl.>` publish to trusted operator/component connections**, so the residual is "allow-all
>    *among already-trusted operators*," not a bus-open hole.
> 2. **Build = Path A *end-to-end*, not the phased two-Fire split.** Ship the capability gate **+
>    verified-JWT actor in one shipment**, reusing D1's `internal/gateway/auth` seam. The original
>    "self-asserted-header-now (Fire 1) / verified-JWT-later (Fire 2)" phasing is **dropped** — we never
>    ship the forgeable-self-asserted-header interim. The JWT seam is a **shared primitive** (D1 builds it
>    first by priority; this work reuses it — no added wait, and it's interchangeable if D1 ever slips).
>    Fire 1c (the `lattice.ctrl.>` subject restriction) is already delivered by #1.
> 3. **`operationType` naming: dotted `ctrl.<comp>.<verb>`** (mirrors the control subject taxonomy) +
>    **Contract #6 §6.4 touched up** to relax the "PascalCase" prescription (it was unenforced — the
>    matcher is exact string-equality — and Andrew judged it silly) and reserve the `ctrl.*` namespace.
>    The §6.4 edit is staged **uncommitted** in `main` alongside the D1 §6.14 edit.
Backlog row: `planning-artifacts/backlog/lattice.md` → *Security & trust boundary → Control-plane
Capability authorization (FR30)* (★★, M). Surveyor-filed (2026-06-27 Weaver survey). An adversarial
review ran as part of this fire; its findings (the scope-enum reality, the class-aware key routing,
the parity-overclaim, the fail-open default) are folded into §3–§8 below. Grounds in
`lattice-architecture.md` (internal-service-actor model, the control-plane communication pattern, the
Gateway decision, D1), Contract #6 (Capability KV §6.4 platform permissions, §6.7 scope semantics),
the three control packages (`internal/{weaver,refractor,loom}/control`), the write-path matcher
(`internal/processor/step3_auth_capability.go`), the rbac-domain grant projection
(`packages/rbac-domain/lenses.go`), and the sibling **Read-path authorization (D1)** design (the
actor-identity-on-the-bus precedent).

---

## For Andrew (one-look ratification block)

**What it does, in two lines.** All three component control planes — Weaver
(`lattice.ctrl.weaver.*`), Refractor (`lattice.ctrl.refractor.*`), Loom (`lattice.ctrl.loom.*`) —
ship a `StubCapabilityChecker` that **allow-alls and never extracts an actor**
(`Authorize(ctx, "", op, id)`), so **any connection on the bus can `disable`/`revoke` a Weaver
target, `delete`/`rebuild` a Refractor lens, or `pause` a Loom consumer.** This closes that hole:
the control op becomes an ordinary **§6.4 platform permission** (`operationType: "ctrl.<comp>.<verb>"`),
the request carries a **`Lattice-Actor` header**, and a real `CapabilityKVChecker` denies any actor
not *granted* the control permission.

**The honest security claim (read this — the review forced a correction).** Fire 1 brings the control
plane from *allow-all* to *capability-checked against a **self-asserted** actor header*. That is **not
authentication** — a bus-resident attacker can forge the header. So Fire 1's capability gate is
defensible **only behind a trusted bus**, and this design makes the **NATS-account subject
restriction on `lattice.ctrl.>` a mandatory companion** (not the "optional" it was first drafted as):
*no deployment enables control enforcement without either the account restriction (Fire 1c) or
verified-actor (Fire 2).* The capability gate's real value now is **least-privilege among already-
trusted operators + an audit-able intent** (which operator did what); cryptographic *who-are-you*
arrives in Fire 2 by reusing **D1 increment 1's `internal/gateway/auth`**, lifting the control plane
and the read path to verified-actor in one shared seam.

**The one fork — your call.** *How is the control actor authenticated?* (§3.4)
- **Path A — self-asserted header now (+ mandatory account restriction), verified-JWT with D1
  (my recommendation).** Ships least-privilege + audit now behind the trusted-bus floor; full
  verification rides D1.2. The actor model stays the single `Lattice-Actor`/Identity-vertex model the
  write path + D1 already use.
- **Path B — NATS NKey/account-native identity as the *primary*.** Unforgeable at the substrate, but
  account-granular (can't express "alice may pause Loom but not revoke Weaver") and forks a *second*
  actor-identity source from the write path's. Rejected as primary; its subject-restriction half is
  adopted as Fire 1c.
- **Path C — hold the whole feature until D1.** Leaves the platform's *most* permissive surface
  allow-all for the entire D1 epic. Rejected.

**Frozen-contract change: NONE.** A control op is a `{operationType: "ctrl.<comp>.<verb>", scope}`
§6.4 platform permission — a new *grant value* in an existing *shape*. **v1 uses `scope: "any"`
(blanket per-verb grants) only** — deliberately, because the write-path matcher's `specific`
(per-target) scope is unimplemented (§6.7, Phase-1 → deny); per-target control scoping is **deferred**
(it would be the moment `specific` is implemented, a separate contract-touching step). No
`docs/contracts/*` edit is staged — which also avoids colliding with the two *other* fires' uncommitted
contract edits already in `main` (§6.14 D1, §10 `l3`).

---

## 1. Problem & intent

**The gap (FR30, security plane).** The 2026-06-27 Weaver survey confirmed what the three control
packages state in their own comments: each ships a `StubCapabilityChecker` that allow-alls — and the
handlers call it with **no actor at all**:

```go
// internal/weaver/control/service.go:211 (refractor + loom identical)
if err := s.capability.Authorize(ctx, "", op, targetID); err != nil { ... }
```

The actor argument is the empty string everywhere, because the CLI issues
`conn.NATS().RequestWithContext(ctx, subject, nil)` (`cmd/lattice/weaver/weaver.go:63`) with a **nil
payload and no header.** So even swapping in a real checker has nothing to check. The effect: **any
process that can publish to `lattice.ctrl.>` can `disable`/`revoke` every Weaver target, `delete` or
`rebuild` a Refractor lens, or `pause` a Loom completion consumer** — operations that halt
remediation, drop a read model, or stall in-flight workflows. The `CapabilityChecker` interface was
built for exactly this swap (`control/capability.go`: "*the interface lives here so the control service
can be swapped to a real checker without touching handler bodies*"); this design fills it.

**The intent.** `lattice-architecture.md` makes the control plane a first-class communication pattern
that "*all components implement*" (synchronous request-reply services, line 108) and makes ReBAC via
the Capability Lens the authorization model for every actor that isn't a root-equivalent internal
service actor (lines 93, 980). FR30 shipped its *mechanism* (list/disable/enable/revoke +
pause/resume + rebuild/replay/delete) but stubbed its *authorization* — the doc labels it "Full
Capability-KV integration … is Epic-3 work" (`docs/components/weaver.md` "Capability authorization").
This design is that integration, done once across all three planes.

**The principle that makes it cheap.** The write path already solved O(1) authorization: project
`actor → roles → permissions` into Capability KV (`cap.roles.<actor>` carrying `platformPermissions[]`),
and check a single GET. A control op is *also* "a standing operation permission not scoped to a
service" — the textbook **§6.4 platform permission.** So the control plane reuses the same projection,
the same KV doc, and the same *read + key-routing* — it adds a *grant value* (`ctrl.weaver.disable`),
not a new auth model. (It does **not** reuse the write-path *matcher* — see §2(a)/§3.3.)

---

## 2. Grounding — the patterns this extends (and the two places the first draft got the code wrong)

**(a) Write-path platform-permission authorization — what is reusable and what is NOT.**
- The **`capabilityRoles`** lens (`packages/rbac-domain/lenses.go`) walks
  `identity -[:holdsRole]-> role <-[:grantedBy]- permission` and projects, to
  `cap.roles.<actorSuffix>` in `capability-kv`, each grant as `{operationType, scope}` read off
  `perm.data.operationType` / `perm.data.scope`. **Reusable verbatim** — the new control grants are
  just more `permission` vertices; the lens projects them with no change.
- **Key routing is class-aware, not a fallback.** `classAwarePlatformKey`
  (`step3_auth_matcher.go:160`) routes **kernel-seeded system actors** (admin/Loom/Weaver, the
  `SystemActorKeys` set) to read `cap.<actor>` (the primordial anchor) and **every other actor** to
  read `cap.roles.<actor>` — and *only* that key, gated by `RbacRolesActive`. There is **no
  "try roles then fall back to core."** A control checker MUST reuse this exact routing (and be handed
  `SystemActorKeys` + `RbacRolesActive`), or it will read the wrong key — see §3.3/§3.5.
- **The scope matcher does NOT support `*` or a literal target id.** `matchPlatformPermission`
  (`step3_auth_capability.go:366`) switches on the §6.7 scope enum: **`any`** (unconditional allow),
  **`self`** (target==actor), **`specific`** (*"Phase-1 platform path doesn't carry the specific-target
  list" → `AuthContextMismatch` (deny)*, `:395`), **`owned`** (deny). So the **only** scope that grants
  a platform op today is **`any`**. v1 control grants therefore use `scope: "any"` (blanket per-verb)
  and the control checker matches on **operationType only**; per-target scoping waits for `specific`.

**(b) The actor's *origin* today (the honest trust baseline the fork turns on).** The write path's
`OperationEnvelope.Actor` is a **required, client-set** field — the CLI's `--actor` flag defaulting to
the credential file's `actorKey` (`cmd/lattice/op/op.go`). It is **not cryptographically verified**:
the Gateway that would stamp a verified `Lattice-Actor` header (`lattice-architecture.md` Translator,
line 596) **is unbuilt — there is no `internal/gateway` directory.** *But* (this is the review's
steelman, §3.4): the write-path actor flows through the **Processor — the sole Core-KV writer (P2)**,
a single chokepoint with lane/dedup/projection-seq second-order constraints; the control plane is **N
independent NATS responders** trusting a client MIME header with *no* connection binding and *no*
downstream chokepoint. They are **not** equally exposed. Hence the mandatory account-restriction floor
(§3.4, Fire 1c).

**(c) The three control planes — the real ops (corrected).** All three are `nats-io/nats.go/micro`
responders with the same shape (`Service{engine, capability CapabilityChecker, logger}`; handlers call
`s.capability.Authorize(ctx, "", op, id)` before dispatch). **The three `CapabilityChecker` interface
signatures are identical** — `Authorize(ctx context.Context, actor, op, id string) error` (the third
param is named `targetID`/`lensID`/`name`, but Go interface satisfaction is by signature, so one
concrete type satisfies all three). Their **actual op sets** (read from the `service.go` files):

| Component | Op (subject) | Kind | `operationType` |
|---|---|---|---|
| Weaver | `list` (exact) | read | `ctrl.weaver.read` |
| Weaver | `<targetId>.disable` / `.enable` / `.revoke` | mutate | `ctrl.weaver.{disable,enable,revoke}` |
| Loom | `list`, `consumers` (exact) | read | `ctrl.loom.read` |
| Loom | `<name>.inspect` | read | `ctrl.loom.read` |
| Loom | `<name>.pause` / `.resume` | mutate | `ctrl.loom.{pause,resume}` |
| Refractor | `<lensId>.health`, `.validate` | read | `ctrl.refractor.read` |
| Refractor | `<lensId>.rebuild` (incl. `truncate=true`), `.pause`, `.resume`, **`.delete`** | mutate | `ctrl.refractor.{rebuild,pause,resume,delete}` |

`supportedOps = {health, validate, rebuild, pause, resume, delete}` for Refractor
(`internal/refractor/control/service.go:296`) — **no `inspect`** (that is Loom's). `delete` (drops a
lens) and `revoke` (immediate target cleanup) are the **highest-blast-radius** mutations and warrant
their own grants in any non-blanket operator role.

**(d) The actor-identity-on-the-bus precedent (D1).** The Read-path authorization design
(`read-path-authorization-d1-design.md` §3.4) designs the **read-actor authentication seam**: a signed
`Lattice-Actor` JWT (brainstorm #118 — signed by an external IdP, keyed to the Identity vertex, verified
by the boundary), `internal/gateway/auth` (verify → `actor_id`) + `internal/gateway/revocation`
(token-revocation KV). The control plane is the **command analog of the same seam** — same JWT, same
verifier, imperative surface. Fire 2 composes with it rather than re-inventing it.

**Invariants honored.** P2 — no write-path change; control ops stay imperative request-reply and never
write Core KV. P5 — the checker *reads* the `capability-kv` lens projection, never Core KV (an
auth-plane read, exactly like the Processor's step-3 read). Contract #1 — new `permission`/`role`
vertices + `grantedBy`/`holdsRole` links are standard 4-seg/6-seg shapes. "No entry = no access"
(§6.8) — a missing header or missing capability doc **denies**.

---

## 3. The shape

**(3.1)** carry the actor; **(3.2)** the op→capability vocabulary; **(3.3)** the control checker;
**(3.4)** the authentication fork + the mandatory trust floor; **(3.5)** the grants + the operator
identity; **(3.6)** the CLI + Loupe clients.

### 3.1 Carry the actor: a `Lattice-Actor` request header

`micro.Request` exposes `Headers()` (NATS `micro.Headers`, present from nats.go ≥ v1.x). The control
client sets `Lattice-Actor: <actorKey>` (the full `vtx.identity.<id>` key, matching the write-path
`OperationEnvelope.Actor` value and the architecture's header naming). The service extracts it once per
handler:

```go
const HeaderActor = "Lattice-Actor"
func actorFromRequest(req micro.Request) string {
    if h := req.Headers(); h != nil { return h.Get(HeaderActor) }  // "" when absent/empty
    return ""
}
```

The extracted value replaces the `""` passed to `Authorize`. Under `capability` mode the checker treats
`actor == ""` as a **fail-closed deny** (no anonymous control); under `stub` mode it logs + allows
(dev/test parity with the Processor stub). The CLI's `RequestWithContext(ctx, subject, nil)` becomes a
header-bearing `RequestMsgWithContext(ctx, &nats.Msg{Subject: subj, Header: ...})` (§3.6). A test must
assert the header survives the micro request→reply round-trip, not just the server-side getter.

### 3.2 The op→capability vocabulary (no contract change; v1 = operationType-granularity)

A control op maps to a §6.4 platform permission **`{operationType: "ctrl.<component>.<verb>",
scope: "any"}`**. The verb space per component is the table in §2(c): the three **read** ops collapse
to `ctrl.<comp>.read` (they reveal topology — gated, see R3), each **mutation** is its own verb
(`disable`/`enable`/`revoke`/`pause`/`resume`/`rebuild`/`delete`). The checker derives nothing
ambiguous from the wire: each component's `Service` is constructed with a **fixed `component` constant**
and an explicit **`op → {verb, read|mutate}` table** (owned in `controlauth`), so the `op` token the
handler already passes is resolved against that table — no subject-shape parsing, no cross-component
collision (Loom's consumer-`pause` and Refractor's lens-`pause` are distinct because `component` is
fixed at construction).

**Scope is `any` in v1** (the only working platform scope, §2(a)). Per-target control grants
(`disable target-A but not target-B`) are **deferred** until the write-path `specific` scope is
implemented — at which point a control grant becomes `{operationType: "ctrl.weaver.disable",
scope: "specific", targets: [...]}` and the checker matches `id ∈ targets`. The v1 operator role is
therefore **all-targets-of-the-verbs-it-holds**; finer roles are a clean future extension, no contract
change beyond `specific` itself.

### 3.3 The control checker (`internal/controlauth`)

A new package provides one concrete type satisfying all three `CapabilityChecker` interfaces. It
**shares the read + parse + class-routing** with the Processor (one source of truth for *how you read
an actor's grants*) but **owns its own simple matcher** (it cannot reuse the Processor matcher — that
matcher has no `ctrl.*` semantics and its `specific` scope denies; §2(a)):

```go
type CapabilityKVChecker struct {
    component string                 // "weaver" | "loom" | "refractor" — fixed at construction
    ops       map[string]opMeta      // op token → {verb, read bool}
    reader    capabilitykv.Reader    // shared read+route (see below)
    mode      AuthMode               // "capability" (DEFAULT) | "stub"
    alerts    AuthAlertEmitter       // health.alerts.security.stub-control-active
    logger    *slog.Logger
}

func (c *CapabilityKVChecker) Authorize(ctx context.Context, actor, op, id string) error {
    if c.mode == AuthModeStub { c.logAndPeriodicAlert(...); return nil }   // dev/test only
    if actor == "" { return ErrNoActor }                                   // fail closed (§6.8)
    meta, ok := c.ops[op]
    if !ok { return ErrUnknownControlOp }                                  // fail closed
    doc, err := c.reader.Get(ctx, actor)                                   // class-routed read
    if err != nil { return fmt.Errorf("control authz read: %w", err) }     // infra error → DENY
    if doc == nil { return ErrNoCapabilityEntry }                          // absence → DENY (§6.8)
    want := "ctrl." + c.component + "." + meta.verb
    for _, p := range doc.PlatformPermissions {
        if p.OperationType == want && p.Scope == "any" { return nil }      // v1: any-scope only
    }
    return ErrControlDenied
}
```

- **Shared read + routing.** Fire 1a factors the §6.2 doc parse (`ParseCapabilityDoc`) **and** the
  class-aware key router (`classAwarePlatformKey` + the `cap.roles.<actor>` / `cap.<actor>` selection)
  into a thin **`internal/capabilitykv`** package, imported by both `internal/processor` and
  `internal/controlauth`. The `controlauth` checker is constructed with the same `SystemActorKeys` +
  `RbacRolesActive` inputs the Processor receives (threaded from `cmd/{weaver,refractor,loom}`), so it
  reads the *same key the Processor would* for any given actor. **This is the one source of truth the
  first draft over-claimed** — it is the read+route, not the matcher. The matcher above is
  control-specific and independently unit-tested. (`internal/capabilitykv` is a leaf package — it
  imports `substrate` + `encoding/json` only, no `processor` types — so no import cycle.)
- **Every non-allow path denies.** `stub`-in-prod is the *only* allow-without-grant path, and it is
  loud (per-call warn + periodic `stub-control-active` health alert) and not the default (below).
  `actor==""`, unknown op, infra read error, nil doc, and scope/operationType miss **all deny** — there
  is no fail-open branch.
- **`capability` is the DEFAULT mode** (mirroring the Processor; the first draft's `stub`-default was a
  fail-open hole). To prevent a flag-day lockout, **Fire 1b ships the `control-authz` grants package in
  the same change** and the checker performs a **startup grant-presence preflight**: if `capability`
  mode is selected but the operator grant is unresolvable, it logs a **loud startup alert** and stays
  fail-closed (deny) rather than silently locking out or silently allowing. One knob, `LATTICE_AUTH_MODE`
  (no second `CTRL` knob — the asymmetry is unjustified); a deployment that runs the Processor in
  `capability` runs the control plane in `capability` too, and the bundled grants make that safe.

### 3.4 The authentication fork + the mandatory trust floor

The checker answers *"is this actor allowed?"* — not *"is this really that actor?"* (authentication,
the fork). §2(b)'s steelman is decisive: a self-asserted `Lattice-Actor` header on N un-chokepointed
responders is **spoofable by any bus-resident process**, and *more* exposed than the write path. So:

- **Path A (recommended) — self-asserted header now, behind a MANDATORY trust floor, verified with D1.**
  Fire 1b's capability gate ships **only together with Fire 1c** (restrict `lattice.ctrl.>` to the
  operator NATS account — the *NATS account-level write restriction* sibling item), so the spoofable
  header is reachable **only from already-trusted operator connections**. Within that floor, the gate
  delivers **least-privilege among operators + an auditable intent** (which operator ran `delete`),
  which is real value the bus-trust floor alone does not give. Fire 2 then grafts **signature
  verification** by reusing **D1 increment 1's `internal/gateway/auth`** (verify JWT → `actor_id` +
  token-revocation), making the actor *verified* on the control plane and the read path in one seam.
  The actor model stays the single `Lattice-Actor`/Identity-vertex model.
- **Path B — NATS NKey/account identity as the primary.** Unforgeable, but account-granular (one
  operator account ≠ per-operator least-privilege) and a *divergent* second actor source. Its
  subject-restriction half is exactly Fire 1c; its identity half is rejected as primary.
- **Path C — hold for D1.** Leaves the strictly-weakest surface allow-all for the whole D1 epic.
  Rejected.

**The fork is *when verification lands*, not *what the checker does* — §3.1–§3.3 are identical under
all three. The non-negotiable (independent of the fork) is the trust floor: control enforcement ships
with the account restriction or with verified-actor, never with a bare self-asserted header on an open
bus.**

### 3.5 The grants + the operator identity (`control-authz` package)

A small **`control-authz` package** declares, as install-time graph data:
- a **`permission`** vertex per `ctrl.<component>.<verb>` operationType (with `data.scope: "any"`);
- an **`operator`** role `grantedBy` those permissions;
- a **seeded `operator` identity** — `vtx.identity.<operator>` — that `holdsRole operator`. Crucially
  this is an **ordinary (non-system) rbac actor**, so its grants project to `cap.roles.<operator>`,
  the key the class-router (§2(a)) reads for non-system actors. (The first draft's plan to grant the
  *platform service actors* Loom/Weaver was a lockout trap — those are **system** actors that read
  `cap.<actor>`, which the rbac lens does not project. They are also **not control-plane callers**:
  the engines never invoke each other's `lattice.ctrl.*`; only the operator (CLI/Loupe) does. So they
  need no control grant.)

The `capabilityRoles` lens projects the operator's grants into `cap.roles.<operator>` with **no new
lens and no Refractor change** — the existing projection picks up the new `permission` vertices
automatically. That is the whole payoff of the §6.4 reuse. Finer roles (a read-only operator, a
"pause-but-not-delete" operator) are additional roles + permission subsets — pure data, no code.

### 3.6 Clients — CLI + Loupe attach the operator actor (net-new plumbing, acknowledged)

This is **not** a one-line swap — the `weaver`/`loom`/`refractor` CLI command groups have **no `--actor`
plumbing today** (only `op submit` does), and Loupe has **no configured actor key**:

- **CLI** (`cmd/lattice/{weaver,loom,refractor}`): add an `--actor` flag (default: credential
  `actorKey`, reusing the `op submit` credential machinery) to each command group, and change the
  request helper from `RequestWithContext(ctx, subj, nil)` to a header-bearing
  `RequestMsgWithContext(ctx, &nats.Msg{Subject: subj, Header: nats.Header{"Lattice-Actor": {actor}}})`.
  Under `capability` mode a control command with no resolvable actor gets a clean `ErrNoActor` reply.
- **Loupe** (`cmd/loupe`, `controlRequest`): add a **config value `operatorActorKey`** (the seeded
  `vtx.identity.<operator>` from §3.5) that the control proxy stamps on every outbound control request
  — the command analog of D1's "Loupe is the privileged all-access read-actor." Loupe stays the
  trusted single-identity console; its identity is now *explicit on the wire* and *granted*, instead of
  anonymous.

---

## 4. Contract surface

| Contract | Change vs build-to | Why |
|---|---|---|
| **#6 §6.4** (platform permissions) + **§6.7** (scope) | **build-to (no edit)** | A control op is `{operationType: "ctrl.<comp>.<verb>", scope: "any"}` — an existing shape, an existing (working) scope value. **The "no contract change" claim holds *only* because v1 is `any`-scope.** Per-target control would require implementing §6.7 `specific` (a contract-touching behavior change) — explicitly deferred. |
| **#6 §6.1** (contribution model) | **build-to** | `control-authz` contributes `permission`/`role`/`identity` vertices + grant links exactly as rbac-domain does; `capabilityRoles` projects them unchanged. |
| **#10** (orchestration surfaces) | **build-to** | §10 defines the control *subjects* and engine semantics, not their authz; nothing there changes. |
| **#1** (addressing) | **build-to** | New vertices/links are standard 4-seg/6-seg shapes. |

**No `docs/contracts/*` edit is staged** — confirming the Surveyor's read and avoiding collision with
the two other designers' uncommitted edits in `main` (§6.14 D1, §10 `l3`).

---

## 5. Migration, compatibility, test strategy

**Migration / compatibility.**
- **`capability` default + bundled grants + fail-closed-loud preflight.** Unlike the first draft (which
  defaulted to allow-all `stub`), the control plane defaults to `capability` — but Fire 1b ships the
  `control-authz` grants package *in the same change*, so the operator grant projects before any
  enforcement bites. A `capability`-mode startup that cannot resolve the operator grant emits a loud
  alert and **denies** (never silently allows, never silently locks out without a signal).
- **Header increment boundary.** Fire 1a ships the CLI/Loupe header plumbing **before** Fire 1b turns on
  enforcement, so by the time `capability` mode denies anonymous control, every first-party client
  already stamps the actor. A third-party client without the header is correctly denied under
  `capability` (and allowed-with-alert under `stub`).
- **No data migration.** Grants are additive vertices installed by a package; uninstalling retracts
  them via the §6.8 soft-tombstone.

**Test strategy.**
- **Unit (`controlauth`):** table — `actor==""` → deny; unknown op → deny; nil doc → deny; infra read
  error → deny (never fail-open); `stub` mode → allow + alert; doc with matching
  `ctrl.weaver.disable`/`scope:any` → allow; doc with a *different-component* grant → deny; doc with a
  *read* grant requesting a *mutate* op → deny. Per-component `op→{verb,read}` table coverage. Header
  extraction (present/absent/empty). The `internal/capabilitykv` factoring is proven byte-identical for
  the Processor by re-running its existing step-3 auth tests unchanged.
- **Ephemeral-stack e2e (one per component, extending the existing control e2e):** seed the `operator`
  identity (granted `ctrl.<comp>.*`) and an `intruder` identity (no control grant), **projected through
  the real `capabilityRoles` lens** (not a hand-written doc); assert the operator's mutation succeeds,
  the intruder's is denied with `ErrControlDenied`, and an anonymous (no-header) request is denied under
  `capability`.
- **Gate-3 adversarial** (`make test-capability-adversarial`): a **control-plane bypass vector** — an
  un-granted actor and an anonymous request attempting `disable`/`revoke`/`delete`/`pause` must be
  **DEFENDED**. Because the control read targets the *same* projection-guarded `cap.roles.<actor>` doc
  the write path reads, instant revocation rides the same CDC window as the write path (a stale *read*
  is bounded by Refractor lag, identical to write-path Capability KV); the test asserts a removed
  operator grant stops control access after CDC converges. (The control plane has no `projectionSeq` of
  its own — it inherits the lens's projection-ordering guard at the source, not a control-side guard.)

---

## 6. Risks & alternatives

**Risks.**
- **R1 — self-asserted actor is spoofable (the central risk).** A bus-resident attacker can forge
  `Lattice-Actor`. *Mitigation (now non-optional):* control enforcement ships **only** behind the
  Fire 1c NATS-account subject restriction (trusted-bus floor) or Fire 2 verified-actor. The honest
  claim of Fire 1 is *least-privilege + audit among trusted operators*, **not** authentication.
- **R2 — flag-day lockout.** Enforcing before grants exist locks out Loupe/operators. *Mitigation:*
  `capability` default + the `control-authz` grants bundled into Fire 1b + the fail-closed-loud
  startup preflight + the seeded **ordinary** operator identity (so grants reach `cap.roles.<operator>`
  via the class-router — the lockout trap the review caught is designed out).
- **R3 — read ops gated.** `list`/`consumers`/`inspect`/`health`/`validate` reveal topology, so they
  *are* gated behind `ctrl.<comp>.read`. *Decision (not deferred):* the operator role holds `read` by
  default, so friction is zero for the intended user; an ungranted actor can't even enumerate targets.
- **R4 — projection correctness = auth correctness.** A grant-DDL bug = a bypass. *Mitigation:* the e2e
  projects through the *real* lens, the Gate-3 vector attacks it, and fail-closed (absence → deny) means
  a *missing* grant fails safe.
- **R5 — three planes drift / wrong-key read.** *Mitigation:* one shared `controlauth` type satisfies
  all three interfaces; the shared `internal/capabilitykv` read+route (handed the same
  `SystemActorKeys`/`RbacRolesActive` as the Processor) guarantees the control checker reads the same
  key the Processor would; a per-`cmd/` wiring test asserts `capability` is selected and an un-granted
  actor is denied, so a forgotten wire-up fails CI.

**Alternatives considered.**
- **A separate "control authorization" KV plane** — rejected: duplicates the projection/read/revocation
  the write path owns and splits the operator's grants across two stores.
- **Per-handler inline checks** (each component reads Capability KV itself) — rejected: re-implements
  the read+route three times; `controlauth` keeps one source.
- **Reuse the Processor *matcher*** (the first draft's claim) — **rejected as infeasible**: that matcher
  has no `ctrl.*`/`any`-for-control semantics and its `specific` scope denies; `controlauth` owns a
  simple operationType matcher and reuses only the read+route.
- **NATS-account identity as primary** (Path B) — rejected as primary (coarse + forks the actor model);
  its subject-restriction is adopted as the mandatory Fire 1c.
- **Hold for D1** (Path C) — rejected: leaves the weakest surface allow-all for the whole D1 epic.

---

## 7. Decomposition for the Steward (fire-by-fire, each independently shippable + green)

1. **Fire 1a — actor on the wire (no enforcement change).** Add `internal/capabilitykv` (factor the
   §6.2 doc parser **and** the class-aware key router out of `internal/processor`; both import it —
   Processor behavior proven byte-identical by its existing step-3 auth tests). Add the `Lattice-Actor`
   header constant + `actorFromRequest` to the three control services and thread the extracted actor
   into `Authorize` (replacing the `""`). Add `--actor` plumbing to the three CLI command groups + the
   `operatorActorKey` config + stamping to Loupe's control proxy. **Stub still allow-alls** → zero
   behavior change; everything green. *Shippable: identity now travels the wire; nothing enforced yet.*

2. **Fire 1b — the checker + grants (enforcement) — ships *with* Fire 1c.** Add
   `internal/controlauth.CapabilityKVChecker` (component-bound, op→verb table, `any`-scope match,
   fail-closed everywhere, `stub` mode + `stub-control-active` alert, startup grant preflight). Wire it
   into `cmd/{weaver,refractor,loom}` with `capability` as default. Add the **`control-authz` package**
   (the `ctrl.*` permissions, the `operator` role, the seeded ordinary `operator` identity). Unit tables
   + one e2e per component (operator allowed / intruder denied / anonymous denied, via the real lens) +
   the Gate-3 control bypass vector. **Bundled with Fire 1c** (below) — they are one shippable security
   unit; capability enforcement is not enabled in any environment without the trust floor.

3. **Fire 1c — NATS-account subject restriction (the mandatory trust floor).** Restrict
   `lattice.ctrl.>` to the operator NATS account (the *NATS account-level write restriction* sibling
   item). *Substrate config + a deployment note; ships as the security floor under Fire 1b's
   self-asserted header.* (Promoted from "optional" — see R1.)

4. **Fire 2 — verified actor (rides D1 increment 1).** Once `internal/gateway/auth` exists (D1.2),
   require a **signed** `Lattice-Actor` JWT on control requests: verify → `actor_id` + token-revocation
   check, *before* the capability read. Lifts the control plane to verified-actor, sharing D1's trust
   model; Gate-3 gains a forged-token control vector. *Gated on D1.2; shippable the moment it lands —
   and the point at which Fire 1c's account restriction may be relaxed if desired.*

**Deferred (recorded, not in scope):** per-target control scope (needs §6.7 `specific` implemented —
contract-touching); the full internet-facing Gateway (NGINX/Envoy/IdP); per-operator *human* identity
provisioning (the internal-service-actor provisioning mechanism, "decide in Stream 3"); finer operator
roles (read-only / pause-but-not-delete — trivial data extensions, no demand yet).

---

## 8. Adversarial review — findings folded in

An adversarial pass ran against the first draft and materially corrected it (this version):
- **The scope model.** The draft claimed it reused the write-path matcher for `*`/per-target scope;
  the real matcher (`step3_auth_capability.go:366`) has **no `*`** and its `specific` scope **denies**.
  → v1 is `scope:"any"` operationType-granularity with a **control-owned** matcher; per-target deferred.
- **The key routing.** The draft's "GET `cap.roles`, fallback `cap.<actor>`" was wrong — routing is
  **class-aware** (system actors read `cap.<actor>`, others read `cap.roles.<actor>`). → the shared
  `internal/capabilitykv` carries the *router*, threaded with `SystemActorKeys`/`RbacRolesActive`; the
  operator is a **seeded ordinary actor** so its grant reaches the key it reads (lockout trap removed).
- **The "reuse the matcher / byte-identical refactor" overclaim.** → reframed: share **read+route**
  only; own a simple control matcher.
- **The real op tables.** Corrected per `service.go` (Refractor has `delete`, no `inspect`; Loom has
  `inspect`/`consumers`).
- **"Write-path parity" as a security claim.** Overstated — the control plane is N un-chokepointed
  responders, not the single-Processor write path. → reframed to *least-privilege + audit behind a
  mandatory trust floor*; the NATS-account restriction is **promoted from optional to mandatory**.
- **Fail-open default.** The draft defaulted control to allow-all `stub`. → `capability` is the default
  (mirroring the Processor), with bundled grants + a fail-closed-loud startup preflight; one knob.
- **Client plumbing.** The "one-line swap" was wrong (no `--actor` on the control CLIs, no Loupe actor
  key). → §3.6 + Fire 1a scope it as net-new plumbing.

Residual items worth a second look at build time: whether the Gate-3 control vector adequately covers
the stale-read revocation axis (§5), and whether the operator role should ship pre-split into
read-only vs mutate from day one (R3 says no demand yet — revisit if an operator's blast radius
concerns Andrew).

---

**Fire 2 SHIPPED (2026-07-05).** All three control planes (Weaver/Loom/Refractor) now verify a signed
actor JWT — reusing `internal/gateway/auth.Authenticator` (D1's Verifier + revocation kill-switch) —
before the capability read, via a new `internal/controlauth.ActorVerifier`/`ResolveActor` seam
(`controlauth.WireActorVerifierFromEnv` builds one per control-plane binary from
`LATTICE_CONTROL_JWT_*` env vars, mirroring Gateway's own trust-root loader — factored out as
`internal/gateway/auth.LoadTrustedKeys` for both to share, Gateway's own bring-up untouched). A
`nil`/unconfigured verifier keeps Fire 1a/1b/1c's self-asserted-header posture byte-identical — no
flag day; JWT mode is opt-in per deployment. CLI (`--actor-token`) and Loupe
(`LOUPE_OPERATOR_ACTOR_TOKEN`) gain the companion client-side token flag alongside the existing raw
`--actor`/`LOUPE_OPERATOR_ACTOR_KEY`, token taking precedence when both are set. 3-layer adversarial
review ran; two independent passes converged on the same real gap — an explicitly-configured
`LATTICE_CONTROL_JWT_KEYS_DIR` that scanned to zero `<kid>.pem` files silently fell back to Fire 1's
unverified mode instead of erroring (indistinguishable from "never configured") — fixed in
`LoadTrustedKeys` (a configured-but-empty dir is now a hard error), along with a related `kid`
collision between a scanned key and the reserved dev key. No contract change (no wire-shape change —
the `Lattice-Actor` header carries a JWT instead of a raw key when verified mode is on; the `Authorize`
signature is untouched). Deferred, as scoped: per-operator human identity provisioning (Stream 3).
