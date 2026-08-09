# Gateway claim-flow authz contradiction — identity self-provisioning (design)

**Status:** ✅ **Andrew-ratified (2026-07-06) — UN-SHELVED.** Designer fire (Winston, 2026-07-04) · Lattice
lane (Arch-review intake / Gateway Fire 4 re-grounding).

> **Ratification update (Andrew, 2026-07-06).** Ratified — Fork B (`identityProvisioner`, the narrow role).
> **The "shelve the build" recommendation is overridden:** the driver the shelf was waiting for now exists
> — Andrew's initiative to run the platform's own **capability write-auth** end-to-end through the Gateway
> with real, role-scoped users (retiring the load-bearing stub). This design's `ProvisionConsumerIdentity`
> is how a real consumer comes to exist with a real role grant, so it is on the **critical path** of that
> proof. Build is sequenced within `real-actor-write-auth-e2e-design.md`: **Phase 1** builds §3 minus the
> walk-in binding (a consumer that exists + acts as itself); **Phase 2** builds the walk-in binding
> (R1–R4). §11's "resolve at the Gateway" gains a shared-seam amendment (see the §11 note) because
> browser-direct sends reads to the app, not the Gateway.

---

## For Andrew

**What it does (two lines).** Grounds the `gateway-claim-flow-authz-contradiction` row precisely (the
Fire-4 "needs re-grounding" note in `gateway.md`), then resolves it: `CreateUnclaimedIdentity` is **not**
a gap (staff already reach it via Fire 1); `ClaimIdentity` **is** unreachable by anyone today, by anyone,
ever — not because of the Gateway, but because its `scope: self` permission requires the calling actor to
already hold `consumer`, and **nothing in the platform ever grants a fresh actor its first role**. The fix
is a new, narrowly-scoped op (`ProvisionConsumerIdentity`) the Gateway submits under its own bootstrap-seeded
system identity the first time it authenticates a not-yet-seen actor — closing the gap with the same
system-actor pattern already shipped for Loom/Weaver/objmgr/privacy, not a new auth primitive.

**The contradiction is real, but building "Fire 4" as originally conceived (an unauthenticated `POST
/v1/claim` door) would not have fixed it** — it targets the wrong layer (authentication) when the actual
blocker is authorization (no capability grant exists for a never-before-seen actor, authenticated or not).
I recommend **retiring** that plan (§3.4) rather than re-scoping it.

**Architectural fork — designed through, my recommendation; the call is yours:**

| Fork | Options | My recommendation |
|---|---|---|
| **The Gateway's new system-actor role** | **(A)** Seed the Gateway as a 6th `holdsRole → operator` system actor (Loom/Weaver/objmgr/privacy's exact shape) — zero new mechanism, reuses the shipped union-read verbatim. **(B)** A new, narrow `identityProvisioner` role granting *only* `ProvisionConsumerIdentity` — a few more moving pieces (new role, new permission, one manual one-time `AssignRole`). | **B.** Loom/Weaver/objmgr/privacy are triggered only by internal graph/schedule state — an external attacker has no direct path to their logic. The Gateway is triggered by **every single internet request**; stacking full `operator` (package install, meta-DDL mutation, every other package's operator-granted op) onto the one component that parses raw, unauthenticated HTTP bodies is a materially larger blast radius for a parsing/logic bug than the existing precedent ever had to accept. The narrow role costs a handful of extra lines and one documented ops step; full operator costs nothing today and a great deal the day the Gateway has a bug. |

**Should this be built now?** **Yes (as of 2026-07-06) — the driver arrived.** *(Original recommendation
was ratify-but-shelve; overridden by Andrew — see the ratification banner.)* My §2.5 finding was correct
about the *product* dimension: no vertical wants self-service signup yet. But it under-weighted a second,
equally-legitimate consumer — **the platform's own auth posture.** To run capability write-auth end-to-end
with real, role-scoped users (the driver, `real-actor-write-auth-e2e-design.md`), a real *consumer* must
exist with a real *role*, and `ProvisionConsumerIdentity` is exactly how one comes into being. That is the
same class of driver the system-actor Fire 2 already treated as valid ("retire the load-bearing stub").
So this is on the critical path of that proof — not dead scaffolding. Build is sequenced there (Phase 1
builds §3 minus the walk-in binding; Phase 2 the binding).

**Frozen-contract change: none.** No contract specifies *who* may call `CreateUnclaimedIdentity`/
`ClaimIdentity` (that's package-level `permissions.go`, unconstrained by Contract #9) or enumerates a
closed system-actor set (Contract #7 §7.2 explicitly anticipates more: *"Additional internal service actor
identities... are seeded by their respective stream's bootstrap procedures in Phase 2+, following the same
pattern"*). Build-to, not change.

---

## 1. Problem & intent

The board row states the tension correctly at a glance: claim ops must be reachable pre-auth, but
identity-domain role-gates both `CreateUnclaimedIdentity` (staff) and `ClaimIdentity` (`consumer`, self),
and an unclaimed identity holds no role. `gateway.md`'s own Fire-4 note (2026-07-03) already smelled
something was off and asked the right question without answering it: *"does 'consumer' get auto-granted
on Gateway-mediated first-JWT-use? is Fire 4 solving a real first-touch-signup gap, or is it redundant with
Fire 1?"* This design answers that question from the code, not from re-reading the original Gateway design's
own framing of itself.

**Why now.** The row **gates** the Gateway epic's Fire 4 (`gateway-external-trust-boundary-design.md` §3.3,
§8 item 4) — nothing else in that epic is blocked on it, but the Steward correctly refuses to build a fire
whose own premise is self-contradictory, and re-deriving this from scratch a second time would waste the
next fire that touches it.

---

## 2. Grounding — the contradiction, precisely

### 2.1 `CreateUnclaimedIdentity` is not a gap

`packages/identity-domain/permissions.go:20-25` grants it `scope: any` to `frontOfHouse`/`backOfHouse`/
`operator` — ordinary staff roles. `gateway.md` already notes correctly: *"both already route correctly
through Fire 1's authenticated `POST /v1/operations`"* — a logged-in staff member calling this today
works, full stop. There is no gap here to close; re-deriving an unauthenticated front for it would be
solving an already-solved problem.

### 2.2 `ClaimIdentity`'s `scope: self` is a hard, pre-Starlark gate

Contract #6 §6.7: *"`self` → require `authContext.target == actor`."* The code
(`internal/processor/step3_auth_capability.go:484-519`, `matchPlatformPermission`) implements this as a
**two-stage gate that runs entirely before the Starlark script**:

1. **Existence gate.** The loop at line 485 (`for i := range doc.PlatformPermissions`) only has anything
   to scan if the actor's capability doc (`cap.roles.<actor>`) **exists at all** — i.e., the actor already
   holds *some* role whose permissions include `ClaimIdentity`. Contract #6 §6.8 is explicit and
   deliberate: *"If Processor at step 3 fetches `cap.<actor>` and receives no document..., the operation is
   denied... **there is no anonymous/public capability fallback**"* — and this is structurally enforced, not
   just documented: the auth-registry builder actively rejects any dispatch predicate that would be
   unconditionally true (`step3_auth_matcher.go`'s `checkCoverageMatchesPredicate`), so an "anonymous op"
   dispatch path cannot even be registered by accident.
2. **Self-match gate.** Line 510: `if target != env.Actor` — a direct string equality against
   `authContext.Target`, no indirection.

**Neither gate is satisfiable by a first-touch actor.** `packages/identity-domain/ddls.go:442-527`
(`ClaimIdentity`'s Starlark body) confirms the *script* was written assuming exactly this — its only
actor-side check is a **negative** dedup (`op.actor` must not already be bound to a different identity,
lines 477-481); it never requires the actor to hold any pre-existing role. The permission grant sitting on
top of it (`scope: self, GrantsTo: [consumer]`) requires precisely the pre-existing standing the script was
designed not to need. (`permissions.go:14-16`'s own comment — *"scope enforcement happens in the Starlark
`ClaimIdentity` branch"* — is **factually wrong**; it happens in step 3, before the script ever runs. This
is likely how the contradiction shipped unnoticed — whoever wrote the grant believed the script was doing
the gating. Flagged as Increment 0, §9.)

### 2.3 Who ever grants `consumer`? Nobody, structurally, ever

The only op anywhere that creates a `holdsRole` link is `rbac-domain`'s `AssignRole`
(`packages/rbac-domain/ddls.go:328-336`), itself granted `scope: any` to `operator` only
(`packages/rbac-domain/permissions.go`, the uniform `mk(op)` helper applied to all 10 rbac ops). Grepping
every non-test caller of `AssignRole` across the repo turns up only CLI/admin/AI-agent tooling and the
package's own test fixtures — **no vertical, no engine, nothing calls it for `consumer`.** Neither
`CreateUnclaimedIdentity` nor `ClaimIdentity` emits a `holdsRole` mutation (confirmed by reading both
branches in full, `ddls.go:338-527`). So: **`AssignRole` is the only path to a `consumer` grant, it is
operator-only, and nothing exercises it for `consumer` today.** The sequencing is airtight and
one-directional — `AssignRole(consumer)` must already have run and converged **before** an actor can ever
pass `ClaimIdentity`'s existence gate. The claim does not create the role; the role must pre-exist.

### 2.4 The system-actor precedent — reusable for half the problem, not for `scope: self`

`system-actor-package-op-grants-design.md` (✅ ratified 2026-07-03) fixed exactly this class of problem for
Loom/Weaver/objmgr/privacy: seed a bootstrap identity, give it `holdsRole → operator`
(`internal/bootstrap/system_actors.go` graph-discovers this topology), and it authorizes package ops via
the normal operator-grant idiom. This transplants cleanly for a **`scope: any`** op — which is exactly why
`CreateUnclaimedIdentity` was never actually a problem (§2.1), and would be exactly how a Gateway system
actor could call it if a real driver ever needed the Gateway itself (rather than a logged-in staff member)
to create unclaimed records.

**It does not transplant for `ClaimIdentity`.** `scope: self` requires `env.Actor == authContext.Target`.
A Gateway system actor calling `ClaimIdentity` on behalf of someone else would have to submit with
`env.Actor` = its own key to pass the self-match — but then `ddls.go:477` (`actor_key = op.actor`) binds
the `credentialBinding` and the `credentialindex` dedup entry to **the Gateway's own key**, for every
claim, for every user. The second real user's claim would then fail the *existing*
"credential-already-bound" guard (`ddls.go:480-481`) against the Gateway's own first claim — the mechanism
mechanically collapses after exactly one claim, platform-wide. This is not a security nicety to relax; it
is a correctness wall. `ClaimIdentity` **must** be submitted with `env.Actor` = the real end-user's own
actor key, which means that actor must already exist and already hold `consumer` — which is the entire gap.

### 2.5 Is there a real consumer for self-service signup? No — checked, not assumed

- `packages/clinic-domain/permissions.go` and `packages/loftspace-domain/permissions.go`: **every** op in
  both reference verticals is granted `scope: any → operator` only. No patient, no applicant, no resident
  ever submits a write directly.
- `packages/lease-signing/permissions.go:17-22`, in its own words: *"No end-consumer submits a
  service-instance create in the demo, so the grants..."* — an explicit, already-written acknowledgment.
- The one shipped "consumer self-service" feature that exists — *Clinic patient/provider self-service
  reads* (Done-log, 2026-07-03, `3e05e2f`) — is a **read**-path mechanism (`cap-read.*` / RLS self-anchor,
  Contract #6 §6.14), which is a structurally different, already-solved problem: the read-path base lens
  grants a **self + primordial** read scope to *any existing identity vertex regardless of role*
  (§6.14: *"core base lens: self + primordial root scope only"*) — there is no role dependency on the read
  side at all. It does not need, and does not establish, a `consumer` role grant, so it sheds no light on
  (and is not blocked by) the write-side gap this design closes. Worth naming explicitly so the question
  "didn't we already solve consumer self-service?" has a precise, correct answer: *for reads, yes,
  differently; for writes, no, not yet, and here is why.*

**Conclusion: `ClaimIdentity` is dead code today, for every actor, in every existing or planned flow** —
not a Gateway problem, a platform-wide one that the Gateway epic happened to be the first to trip over.

---

## 3. The shape

### 3.1 New op: `ProvisionConsumerIdentity` (identity-domain)

A new operationType on the existing `identity` DDL (`packages/identity-domain/ddls.go`), mirroring
`CreateUnclaimedIdentity`'s shape (the submitting actor ≠ the identity being acted on) but doing two things
atomically that `CreateUnclaimedIdentity` deliberately does not:

```
payload:  { targetActorKey: "vtx.identity.<sub>" }   # the Gateway's own verified ActorID, not client input
script:
  if targetActorKey already exists in state:
      return { response: { primaryKey: targetActorKey } }     # idempotent no-op — the common case
  else:
      mutations = [
        create targetActorKey  (class=identity, root data {})
        create targetActorKey + ".state" = "claimed"           # cosmetic; not read by any existing code path
        create holdsRole link: targetActorKey -> consumer role
      ]
      events = [{"class": "identity.provisioned", "data": {"identityKey": targetActorKey}}]
      return { mutations, events, response: { primaryKey: targetActorKey } }
```

- **Idempotent by construction** (Processor-side declared-read check, the "Core-KV reads default to
  Processor-side" pattern) — the Gateway can call it liberally without coordinating with itself; a
  redundant call for an already-provisioned actor is a harmless no-op commit.
- **Key is deterministic, not minted.** Unlike `CreateUnclaimedIdentity` (`nanoid.new()`), the target key
  is the **exact** `ActorID` `internal/gateway/auth/auth.go:216` already computes for every future request
  from that actor (`IdentityKeyPrefix + sub`, verbatim, no re-derivation) — so there is exactly one key this
  op could ever write for a given JWT subject, and every subsequent op from that actor resolves the same
  capability doc this op causes to exist.
- **Validate, don't assume, the key shape.** `sub` comes from an external IdP Lattice does not control. The
  script should reject (fail-closed, `InvalidArgument`) a `targetActorKey` whose id segment isn't
  NanoID-shaped, rather than silently writing an unparseable key. This is consistent with, not a new
  constraint on top of, the existing `dev-token -sub <identityNanoID>` convention (`gateway.md`) — production
  IdP integrations already need an enrollment step that maps their own subject id to a Lattice-minted
  NanoID; this design does not solve that mapping (out of scope — flagged as an adjacent, pre-existing gap
  in `internal/gateway/auth`, not introduced here), it just refuses to silently corrupt a key if the mapping
  is missing.
- **No PII, no claim-key.** This op carries none of Contract #9's plaintext-never-enters-Lattice concerns —
  there is no secret involved, so the `claimKeyHash` machinery is untouched and irrelevant here.

### 3.2 New role: `identityProvisioner` (identity-domain)

A fourth role alongside the package's existing three (`packages/identity-domain/package.go:33-37`), added
the identical way:

```go
Roles: []pkgmgr.RoleSpec{
    {CanonicalName: "consumer", ...},
    {CanonicalName: "frontOfHouse", ...},
    {CanonicalName: "backOfHouse", ...},
    {CanonicalName: "identityProvisioner", Description: "System role for actors that provision bare consumer identities on first authenticated touch. Not a user-facing role."},
},
```

with one new `PermissionSpec`: `{OperationType: "ProvisionConsumerIdentity", Scope: "any", GrantsTo:
["identityProvisioner", "operator"]}` (operator included for parity with the package's other ops — lets a
human operator invoke it directly via CLI/Loupe if ever needed; costs nothing extra).

### 3.3 New bootstrap system identity: the Gateway

`internal/bootstrap/primordial.go` gains a sixth system identity, `bootstrap.GatewayIdentityKey`
(class `identity.system.gateway`), seeded exactly like `LoomIdentityKey`/`WeaverIdentityKey`/
`ObjmgrIdentityKey`/`PrivacyIdentityKey` (`primordial.go:401-425`) — **except it does *not* get the
primordial `holdsRole → operator` link** (§4's fork). Its role grant is `identityProvisioner`, which does
not exist until identity-domain installs, so it is wired by **one documented, one-time operator action**
post-install (`lattice identity assign-role` or equivalent over the already-shipped `AssignRole`) —
`pkgmgr.RoleSpec`/`PermissionSpec` have no primitive for "grant this role to a pre-existing external
identity" (confirmed: `internal/pkgmgr/definition.go:320-326,531-545` only wire new-permission→role and
role-vertex-creation, never actor→role for an actor the package didn't create), so this is correctly an
ops runbook step, not new pkgmgr machinery. Before that step runs, the Gateway's provisioning calls simply
deny (fail-closed, safe direction) — the identical, already-accepted residual described in
`system-actor-package-op-grants-design.md` §7.3 for the rbac-domain install-order CDC-lag window.

`cmd/gateway/main.go` wires `actorKey := bootstrap.GatewayIdentityKey` into a new internal submit path,
mirroring `cmd/object-store-manager/main.go:72` (`actorKey := bootstrap.ObjmgrIdentityKey`) verbatim.

### 3.4 Gateway-side flow — and retiring the unauthenticated door

`internal/gateway/gateway.go`'s `handleOperations` gains, after `Authenticate` succeeds and before
`s.submit` for the **client's** requested op: a check against a small in-memory set of actor keys already
known-provisioned. On a miss, submit `ProvisionConsumerIdentity{targetActorKey: actor.ActorID}` under the
Gateway's own `env.Actor` (`bootstrap.GatewayIdentityKey`), ~~tolerate the reply~~, add the actor to the
set, then proceed. **Superseded 2026-08-08 by
[credential-binding-plane-lifecycle-design.md](credential-binding-plane-lifecycle-design.md) §4.4:** the
write path no longer tolerates a failed pre-flight — it answers **503**, because the claim ops now refuse
an actor vertex that does not exist and that refusal is generic on the wire, so tolerating here surfaced
as "invalid claim key" and broke the ceremony out. `whoami` keeps the tolerant posture described here.
The set is a pure latency optimization (bounded/LRU; a false miss just
re-runs the idempotent op) — correctness never depends on it, and it starts empty on every restart by
design (a cold Gateway just re-provisions already-provisioned actors once, harmlessly).

**This retires the original Fire-4 plan** (an unauthenticated, rate-limited `POST /v1/claim` admitting only
`CreateUnclaimedIdentity`/`ClaimIdentity` by an op-name allow-list). Two independent reasons it should not
be built even in revised form:
- It would not have fixed anything — an unauthenticated front changes *who calls the HTTP endpoint*, not
  *whether the actor named in the resulting envelope holds a capability grant*. `ClaimIdentity`'s gate is
  the latter; an unauthenticated door and an authenticated one hit the exact same `AuthDenied` (§2.2).
- Building it would mean deliberately routing traffic to a path with no capability check backing it — the
  precise shape Contract #6 §6.8's registration-time guard exists to make structurally hard to introduce by
  accident. Once §3.1–§3.3 close the real gap, every claim-flow op reaches the Processor through the
  **already-authenticated** `POST /v1/operations` (Fire 1), like every other op — there is no longer a
  reason for a second, unauthenticated route to exist.
- The backlog row's second ask — *"the unauth surface should be package-declared data"* (make the allow-list
  configurable instead of hardcoded) — dissolves along with the door itself: there is no allow-list to
  generalize once there is no unauthenticated surface.

---

## 4. The fork, designed through: narrow role vs. full operator

**Option A — seed the Gateway as a 6th `holdsRole → operator` system actor**, reusing
`system-actor-package-op-grants-design.md`'s union-read verbatim. Zero new auth-plane code; `SystemActorKeys`
already graph-discovers any `holdsRole→operator` identity, so this is a one-line bootstrap addition.

**Option B (RECOMMENDED) — a new, narrow `identityProvisioner` role** granting *only*
`ProvisionConsumerIdentity`, per §3.2–§3.3.

**Why B despite the extra pieces.** Loom, Weaver, object-store-manager, and the privacy actor are triggered
exclusively by **internal** state: graph mutations, schedules, and CDC — an external attacker has no direct
network path to their decision logic; reaching it at all requires *already* being an authorized actor able
to produce the triggering graph state. The Gateway is categorically different: it is triggered by **every
unauthenticated HTTP request that reaches it**, by construction (that is its entire job as the trust
boundary). Granting it full `operator` — package install, meta-vertex mutation, and every other installed
package's operator-granted op, not just this one — means a parsing bug, a logic-confusion bug, or a future
dependency vulnerability in the one component that processes raw internet input is no longer bounded to
"can create bare consumer identities" but "can do anything any operator can do." The existing system-actor
precedent never had to weigh this trade-off, because none of its members are internet-facing. Option B's
cost is real but small (one role, one permission line, one documented ops step) against a meaningfully
smaller blast radius for the one component whose entire purpose is standing at the perimeter. I would not
make this same recommendation for an internal-only component — the fork is specifically about
internet-facing exposure, not a blanket "narrow roles are always better."

---

## 5. Contract surface

None. Verified, not assumed:

| Contract | Relevant section | Why untouched |
|---|---|---|
| #6 Capability KV | §6.1 (per-actor projection), §6.7 (scope enum) | A package adding a role/permission is ordinary package-level extension; no document-shape change. |
| #7 Primordial Bootstrap | §7.2 point 8 | Explicitly anticipates more system identities: *"seeded by their respective stream's bootstrap procedures in Phase 2+, following the same pattern"* — build-to. |
| #9 Identity Claim Flow | whole contract | Untouched — `CreateUnclaimedIdentity`/`ClaimIdentity` mechanics, the claim-secret hash flow, and the reply shape are unchanged. This design only changes *how an actor first becomes eligible to call `ClaimIdentity`*, which Contract #9 never specified (that's `permissions.go`, package-level). |

---

## 6. Reconciliation with the existing mental model

- **Didn't the system-actor design already solve "how does a service get authorized"?** Yes, for `scope:
  any` — reused directly for the (non-)problem in §2.1 and structurally available if a real driver ever
  needs the Gateway itself to call `CreateUnclaimedIdentity`. It does not solve `scope: self`, because
  `self` is defined as actor-equals-target, and the whole point of first-touch provisioning is that the
  target (a fresh consumer) doesn't equal any existing, capability-holding actor yet (§2.4).
- **Didn't the D1.3 self-anchor read lens already solve consumer self-service?** For **reads**, yes,
  completely independently of role-holding (§2.5). For **writes**, no — Capability KV's write path is
  100% role-driven, with no anonymous or self-anchor fallback (§6.8's structural guarantee). These are two
  different mechanisms solving two different problems; conflating them would have been the wrong precedent
  to reuse.
- **Does this introduce new state?** Yes, deliberately minimal: one new role vertex + its permission
  (`identity-domain`'s package install, the same install batch already creates 3 roles), one new bootstrap
  system identity (mirrors 4 existing ones), and — per provisioned actor — one identity vertex + one
  `holdsRole` link that would otherwise never exist. No new lens, no new KV bucket, no new contract-level
  concept.
- **Does this contradict a design-of-record?** No — it fills a gap Contract #7 §7.2 already anticipated
  (more system actors) and reuses a pattern `system-actor-package-op-grants-design.md` established four days
  prior, rather than inventing a parallel authorization mechanism.

---

## 7. Migration / compatibility, test strategy (for when this builds)

- **Backward-compatible by construction.** No existing op, role, or grant changes. `CreateUnclaimedIdentity`
  and `ClaimIdentity` are byte-identical; every existing (zero, per §2.5) caller is unaffected.
- **Deny→allow direction only**, same shape as the system-actor design: before the Gateway's
  `identityProvisioner` grant is wired (§3.3's manual step), `ProvisionConsumerIdentity` calls simply deny;
  after, they succeed. No path from working to broken.
- **Unit (`internal/processor`, `packages/identity-domain`):** fresh-actor provision → vertex + role link
  created, response carries `primaryKey`; re-provision of an already-existing actor → no-op, same response;
  malformed `targetActorKey` (bad prefix or non-NanoID id segment) → `InvalidArgument`, no mutation; the
  Gateway's own actor without the `identityProvisioner` grant yet → `AuthDenied` (proves the fail-closed
  pre-wiring window).
- **E2E (ephemeral stack):** a genuinely fresh JWT subject → Gateway auto-provisions → the **same** actor's
  next `ClaimIdentity{targetIdentityKey: <a staff-created unclaimed identity>, claimKey}` call, through the
  standard authenticated `/v1/operations` path, **commits** (proves the end-to-end gap is closed, not just
  the provisioning step in isolation). A second call from the same subject (cache-cold, simulating a Gateway
  restart) → still succeeds (idempotent no-op path), proving correctness never depended on the in-memory
  cache.
- **Adversarial:** a client-supplied `targetActorKey` in the *client's own* request body must have zero
  effect — the Gateway derives it solely from the verified JWT, never from client input (mirrors the
  existing forged-`actor`-never-wins gate, Gate-3 vector #14).

---

## 8. Risks + alternatives considered

- **Rejected — have the Gateway submit `ClaimIdentity` on the caller's behalf** (as its own actor).
  Mechanically broken, not just undesirable: `ddls.go:477` binds `credentialBinding.actorKey = op.actor`,
  so every claim would bind to the Gateway's own key, and the *existing* one-credential-one-identity guard
  (`ddls.go:480-481`) would reject every claim after the first, platform-wide (§2.4). Not a security
  trade-off to weigh — it cannot work for a second user.
- **Rejected — re-scope `ClaimIdentity`'s permission from `self` to `any`, keep `GrantsTo: [consumer]`.**
  Does not touch the actual gap: the actor still needs a pre-existing `consumer` grant to pass the
  existence gate regardless of scope kind (§2.2's first gate is independent of the second). This alternative
  optimizes the wrong gate.
- **Rejected — leave the contradiction dormant, fix only the docs/board, design nothing.** Would satisfy
  the letter of "don't build dead scaffolding" but not the spirit of "resolve every open question" (§0 of
  this skill) — the next fire that touches claim-flow would have to re-derive everything in §2 from
  scratch. A shelved, fully-designed mechanism costs one fire now and saves a repeat of this one later.
- **Residual, explicitly out of scope:** `internal/gateway/auth.Verify` does not validate that a JWT `sub`
  is NanoID-shaped before constructing `ActorID` (`auth.go:216`) — a pre-existing property of the
  already-shipped D1.2 verifier, independent of this design, that would also affect the D1.3 read boundary.
  §3.1's new op defends itself (fail-closed on a malformed key) but does not fix the upstream verifier. Worth
  its own small hardening item when a production IdP integration is actually being wired up; not bundled
  here to keep this fire scoped to the claim-flow contradiction.

---

## 9. Decomposition for the Steward — UN-SHELVED (Andrew, 2026-07-06)

**Verdict: build, sequenced within `real-actor-write-auth-e2e-design.md`.** *(Original: shelve until a
product driver files. Overridden — the auth-posture driver arrived; see the ratification banner + the
"Should this be built now?" block.)*

- **Increment 0 — the doc/comment fix (no design dependency, no code risk).** Fix the stale
  `permissions.go:14-16` comment (§2.2) so it no longer misattributes scope enforcement to the Starlark
  script. Trivial, one line.
- **Increment 1 — the provisioning mechanism (§3), minus the walk-in binding** → **Phase 1** of the
  driver. The new op + role + permission (identity-domain), the new bootstrap Gateway system identity
  (`internal/bootstrap`), the Gateway-side pre-flight + cache (`internal/gateway`), the one-time ops
  `AssignRole` step, tests per §7. This is what makes a real **consumer** exist with a real **role** so
  the capability-auth e2e can exercise a scoped actor. R2 (claim-time `consumer` grant on U) is part of
  the claim script and rides Phase 2 with the rest of the binding.
- **Increment 2 — the walk-in credential binding (§11 R1–R4)** → **Phase 2** of the driver. The
  `credential-bindings` materializer, the resolve-then-stamp seam (now a **shared** seam, §11 note), the
  claim-time role grant, `RotateClaimKey`, the `sub`→NanoID derivation.

The §3 mechanism is fully specified; Increment 1 is a clean Phase-1 fire the moment the driver's shared
dev-IdP + `up-full-capability` land alongside it.

---

## 10. Companion doc/board updates made in this fire

- `docs/components/gateway.md` — Fire 4 bullet rewritten: retired as originally conceived, points here.
- `_bmad-output/implementation-artifacts/gateway-external-trust-boundary-design.md` — §3.3 (identity-claim
  front) and §8 item 4 (Fire 4) rewritten in place (not banner-only) to strike the unauthenticated-door plan
  and point here, per the "a ratification revision must rewrite the body it supersedes" rule; §5's contract
  table row for #9 corrected to drop the retired `/v1/claim` reference.
- `_bmad-output/planning-artifacts/backlog/lattice.md` — this row and the Gateway epic row updated to
  `📐 awaiting-Andrew` with links here.

---

## 11. The real-app end-to-end scenario (added 2026-07-05, Andrew's ratification question)

*"How does consumer identity provisioning / creating / claiming work when Clinic and LoftSpace become
real apps, not demos?"* This section walks it end to end, grounded in the PRD's personas and journeys,
the brainstorm inventory, and the architecture's IdP decision. Working it through surfaced **three
refinements** to §3 (folded in at §11.5) — the shelved mechanism's shape survives, but the full picture
sharpens what `ProvisionConsumerIdentity` is *for* and adds the two missing pieces a real app needs.

### 11.0 The structural insight: two identity vertices per walked-in human — and who you act as

The claim flow, read closely, creates **two** identity vertices for one human in the staff-first path:

- **U — the business identity.** Staff-created (`CreateUnclaimedIdentity`), carries the name/email/phone
  aspects, the PII (`RecordIdentityPII` ssn/dob → Vault-encrypted, crypto-shreddable per PRD §358), and
  every business link — the lease application points at U, the patient record points at U, tasks are
  `assignedTo` U. This is the "own identity vertex, own lease, own service history" of the PRD's consumer
  persona (PRD L494).
- **A — the credential identity.** Born from the deployment's IdP (brainstorm #118: *"actor claim as a
  signed JWT keyed by Identity vertex"*; architecture: *"External IdP for actor signing keys — integration
  dependency"*, Auth0/Keycloak/etc., Lattice never owns the keys — the ratified F3). `auth.go` maps the
  verified JWT `sub` → `vtx.identity.<sub>` = A. Revocation (the kill-switch) targets A.

`ClaimIdentity` **binds** them: `credentialBinding{actorKey: A}` written on U, plus the
`vtx.credentialindex.<sha256NanoID(A)>` → `{identityKey: U}` lookup vertex (`ddls.go:497-511`). The
package's own comment says the quiet part: *"scope enforcement… one-credential-one-identity via
credentialindex"* — the index is a **credential → business-identity resolution table**, keyed by a
deterministic hash of the credential key for O(1) lookup. That only has a purpose if something *resolves*
through it per request. The design of record for a real app is therefore:

> **After claim, the person acts AS U.** Resolve credential → identity: verify JWT → A → look up the
> binding → stamp `env.Actor = U` (write path) / `lattice.actor_id = U` (read path). No binding → act as
> your own key (A).

> **Shared-seam amendment (2026-07-06, browser-direct topology).** This originally read "one seam, one
> component — the Gateway." That holds only if the Gateway is the *sole* external surface. Andrew ratified
> **browser-direct** (`real-actor-write-auth-e2e-design.md` §5): writes go browser→Gateway, but **reads go
> browser→app**. So the app's read boundary *also* needs to resolve A→U to set `lattice.actor_id = U`. The
> resolution is therefore a **shared lookup** both the Gateway (write stamp) and the app read boundaries
> consult — a small library over the `credential-bindings` materializer below, imported by both (they
> already share `internal/gateway/auth`). Same mechanism, relocated from "Gateway-private" to "shared
> seam." Phase-2 scope (Phase 1's consumer acts as itself — no binding, no resolution needed).

The alternative — the person acts as A forever and every downstream mechanism walks the binding — fails
concretely on shipped machinery: the ephemeral task-grant lens projects `cap.ephemeral.<assignee>`
(Contract #6 §6.6) off the `assignedTo` link, which points at **U** (that's where the lease application
lives). A person acting as A would never match a task grant projected for U; every package lens
(`cap-read.residence`, my-tasks, the SignLease §10.7 grant) would need binding-awareness bolted on — and
the binding is aspect *data* on U, not a link, so lenses can't even traverse to it from A. Resolving once
at the Gateway keeps the credential plane (A: JWT, revocation) cleanly separate from the business plane
(U: links, grants, PII) with exactly one component aware of the seam.

**Mechanism for the lookup (P5-clean, precedent-mirrored):** the Gateway already runs a durable
`events.gateway.>` materializer folding revocation events into its local `token-revocation` bucket. The
claim emits `identity.claimed{identityKey, actorKey}` (`ddls.go:514-517`) — a second materializer arm on
`events.identity.>` folds bindings into a local `credential-bindings` bucket the same way (works even when
Refractor is degraded, the same argument the revocation design made). CDC-lag posture identical to the
accepted M3 window: a claim on device 1 is visible to device 2's requests within the materializer lag;
until then the person acts as A and sees self-only data — deny-safe, self-healing.

**One carve-out:** `ClaimIdentity` itself always acts as the **raw credential identity** (no resolution) —
the one-credential-one-identity dedup keys off `hash(op.actor)` and must see the credential, and a
resolved actor would let an already-bound person chain-claim a second identity. Op-type-scoped skip at the
Gateway; one line, one test.

### 11.1 Scenario A — LoftSpace goes real: the walk-in applicant (staff-first; the Contract #9 flow)

*This is PRD Journey 3 (Sam processing the 12A application) meeting PRD Journey 1 (Maya's concierge) —
the missing bridge between them is exactly this flow.*

1. **Walk-in.** A prospect tours the building. Front-desk staff F (logged in through the same Gateway —
   F's IdP JWT → F's identity holds `frontOfHouse`) takes their name/email/phone.
2. **Staff creates U.** The staff app mints the claim secret `s` **client-side**, computes `sha256(s)`,
   and submits `CreateUnclaimedIdentity{name, email, phone, claimKeyHash}` through `POST /v1/operations`.
   Step-3: F holds `frontOfHouse` ✓ (the staff grant — never a gap, §2.1). U is created
   (`state=unclaimed`, `.claimKey` stores the hash). Lattice never sees `s` (Contract #9 Option C — the
   whole point of the client-side mint). The app hands `s` to the prospect out of band — a QR on the
   tour brochure encoding `{identityKey: U, claimSecret: s}` (U's key is an address, not a secret; `s`
   alone gates the claim).
3. **Business accrues on U.** F records the application: `RecordIdentityPII{ssn, dob}` (sensitive
   aspects on U → Vault), `CreateLeaseApplication{applicant: U, unit 12A}`. The Loom pattern runs
   (background check via the bridge, credit check), Sam approves — all of PRD Journey 3, unchanged, all
   anchored on U. The person has no login yet and doesn't need one.
4. **Self-signup at the IdP.** At home, the prospect downloads the resident app → "Create account" →
   the deployment's IdP (Auth0/Keycloak — architecture's integration dependency; Lattice is not
   involved). They come back with a JWT, `sub = S`.
5. **First touch → provision A — BARE.** Their first request hits the Gateway. Verify JWT → A
   (`vtx.identity.<S>`). No binding for A, no vertex A → the Gateway submits
   `ProvisionConsumerIdentity{targetActorKey: A}` under its own system identity (§3.3,
   `identityProvisioner` role): creates vertex A (root `data: {}`) + `holdsRole(A → consumer)` — **and
   nothing else. A carries no name/email/phone aspects.** The step-4 signup profile lives at the IdP
   (the account), not in Lattice; the Gateway only ever sees a JWT and must not write business PII from
   token claims. Post-claim there is exactly **one PII-bearing identity per human (U)** — no A-vs-U
   aspect divergence to own, and **crypto-shred covers U alone** in this scenario (A has no sensitive
   aspects to shred; "forget me" = shred U's DEK + delete the IdP account, an IdP-side concern +
   optionally tombstone A). Idempotent; cached. *Without this step the person is a dead end — no
   capability doc exists for A, and §6.8 has no anonymous fallback (the entire §2 contradiction).*
6. **Claim.** The app reads the claim link (§11.1a): submits `ClaimIdentity{targetIdentityKey: U,
   claimKey: s}` with `authContext.target = A`, through the same authenticated `/v1/operations`. Step-3:
   A holds `consumer` (step 5), `scope self`: target == actor ✓. Script: U unclaimed ✓, no existing
   binding for A ✓, `sha256(s)` matches constant-time ✓ → binds `credentialBinding{actorKey: A}` on U,
   `state → claimed`, tombstones `.claimKey`, writes the credentialindex entry, **grants
   `holdsRole(U → consumer)`** (refinement R2, §11.5 — U is about to become the acting identity and
   needs the role). The `identity.claimed` event flows to the Gateway's binding materializer.
7. **From now on: the person IS U.** Every subsequent request: JWT → A → binding → `env.Actor = U`.
   Maya's concierge journey (PRD Journey 1) now works verbatim: the AI traverses *U's* identity →
   lease → entitlements; `RenewLease` submits as U; the countersignature task `assignedTo` Sam; the
   §10.7 ephemeral SignLease grant projects `cap.ephemeral.U` and **matches**, because the actor is U.
   Reads: `GET /v1/<readmodel>` sets `lattice.actor_id = U`; `cap-read.residence.U` (D1.3) anchors
   their unit/lease rows through RLS. Revocation still targets A — the kill-switch cuts the credential,
   not the business history.

#### 11.1a The claim handoff — out-of-band by design (Andrew's step-6 question, 2026-07-05)

The app learns `{U, s}` from a channel that never transits Lattice — this is Contract #9 §9.2's design,
not an accident: *"The client retains the plaintext it minted; it is the single copy and the single
delivery channel (e.g. shown once to an operator, or handed to the end user out of band)."* Lattice
stores only the hash and structurally **cannot** deliver the secret. Concretely:

- At step 2 the staff app (client-side) composes a **claim link** after the create reply returns U:
  `https://resident.<deployment>/claim#k=<U's-NanoID>&s=<claimSecret>` — the **URL fragment** (`#`, not
  `?`) never leaves the browser in HTTP requests, so the secret cannot land in server/proxy logs.
- **Delivery**: a printed QR on the tour brochure / welcome letter (the walk-in handoff), or the staff
  FE sends the link by email/SMS **directly from the front-end** via the deployment's messaging service.
  U's NanoID is an address, not a secret — `s` alone gates the claim, and every failure collapses to
  `ClaimKeyInvalid` (NFR-S6 anti-enumeration), so a leaked link without `s` reveals nothing. No lookup
  endpoint exists or should: "find my unclaimed identity by email" would be an enumeration oracle.
- **The delivery channel must bypass Lattice's write path entirely.** Sending the claim email through a
  Lattice-orchestrated bridge/externalTask would put the plaintext `s` in an op payload on
  `core-operations` — violating Contract #9's core invariant (*"not in the core-operations stream"*).
  The FE → messaging-service hop carries it; **never build a "send claim email" Loom pattern.**
- **Lost brochure = re-issue, not recovery.** Staff cannot recover `s` (hash-only storage, single copy
  was client-side). Refinement R4 (§11.5): a staff-gated `RotateClaimKey{identityKey, claimKeyHash}` op
  — new client-minted secret, new hash overwriting `.claimKey`, valid only while `state == "unclaimed"`,
  same grant roles as `CreateUnclaimedIdentity`. Fails closed on a claimed/merged/tombstoned identity.

### 11.2 Scenario B — Clinic goes real: self-signup-first (no staff pre-creation)

1. A new patient finds the clinic online, creates an IdP account, opens the patient app. First touch →
   step 5 above: A is provisioned + `consumer`. **There is no U and no claim** — nothing pre-exists to
   claim. A *is* the business identity from day one.
2. The patient books: `CreateAppointment{patient: …}` — in a real Clinic this means the vertical adds
   consumer-scope grants for the self-service subset of its ops (today every clinic-domain op is
   `operator`-only — the §2.5 demo posture; the real-app delta is package `permissions.go` work, e.g.
   `CreateAppointment → consumer` with the Starlark script enforcing patient-self semantics, FR24's
   "operators define and assign role-scoped access for all actor types" made real).
3. Front desk later links the walk-in world in: `CreatePatient{identityKey: A}` — clinic-domain's
   `identifiedBy` link already takes a pre-existing identity key; it doesn't care whether that identity
   was staff-created or self-provisioned. The two onboarding paths converge on the same graph shape.
4. **When both paths happen for one human** (they self-signed-up *and* a receptionist created an
   unclaimed record at the front desk), the platform's existing answer applies: the two identities are
   duplicates, `identity-hygiene`'s `duplicateCandidates` lens surfaces them (email/phone match) and
   `MergeIdentity` folds one into the other (`state=merged`, `mergedInto`, aspect conflict resolution)
   — the merge machinery exists precisely because multiple identity vertices per human are inevitable.
   (That lens is currently inert over Vault-encrypted PII — the known `[identity-hygiene]` board item —
   but that's its own tracked gap, not new debt this flow creates.)

**Crypto-shred coverage across the scenarios (Andrew's step-5 question, 2026-07-05).** The invariant is
*"shred the discoverable identity-set of the human,"* not *"shred one vertex"*: Scenario A — U alone
(A is bare, §11.1 step 5; nothing sensitive to shred). Scenario B — A alone (it accreted the PII; the
`name`/`email`/`phone` aspect DDLs deliberately carry empty `permittedCommands`, so vertical ops are
sanctioned writers). The duplicate case above — **both**, until/after merge: a merged-loser vertex may
still carry PII aspects, so a forget-request walks the set via `mergedInto` + the credentialindex and
shreds each member's DEK. The set is always discoverable from any member; the forget flow must
enumerate it rather than assume a singleton.

### 11.3 What must change for demo → real (the honest delta list)

| # | Delta | Where | Status |
|---|---|---|---|
| 1 | Vertical FEs stop self-asserting `bootstrap.BootstrapIdentityKey` and submit through the Gateway with per-user JWTs | `cmd/loftspace-app`, `cmd/clinic-app` (`main.go` adminActor) | The demo posture; the Gateway write path (Fire 1) is built and waiting |
| 2 | Verticals grant consumer-scope ops (the self-service subset) | package `permissions.go` + scripts | The §2.5 finding — today everything is operator-only; this is ordinary package work, per-vertical product decisions |
| 3 | `ProvisionConsumerIdentity` + `identityProvisioner` + Gateway system identity | §3 of this design | Designed, shelved, ready |
| 4 | Claim-time `consumer` grant on U | `ClaimIdentity` script (refinement R2) | Folded into §3's build scope, §11.5 |
| 5 | Gateway credential→identity resolution (binding materializer + stamp-resolved-actor + claim-plane carve-out) | `internal/gateway` (refinement R1) | Folded into §3's build scope, §11.5 |
| 6 | Real-IdP `sub` → NanoID mapping | `internal/gateway/auth` (refinement R3) | Folded into §3's build scope, §11.5 |
| 7 | Prod reverse-proxy, real IdP JWKS config | `deploy/` | The Gateway design's Fire 5 (ops), unchanged |

Deltas 1–2 are vertical-lane product work regardless of this design; 3–6 are this design's build scope
when the driver files; 7 is already tracked. Nothing new is platform-blocked.

### 11.4 Refinement R3 spelled out: the `sub` → NanoID mapping

> **R3 is now its own full design + frozen contract (2026-07-10):
> `external-actor-authn-binding-design.md` + the staged `docs/contracts/11-external-actor-authn.md`.**
> That design realizes option (a) below with the refinements a full grounding surfaced — the
> derivation input is **length-framed** (`idpsub:<len(iss)>:<iss>:<sub>`, injection-proof, not a bare
> `iss + "|" + sub`), binding is a **per-trust-source mode** (`opaque` derives, the dev key stays
> `nanoid` passthrough) so dev tokens keep working, and the `opaque` **issuer pin is mandatory
> per-source** (a peer-issuer replay would otherwise cross-derive a peer user). The sketch below is
> retained as the origin of the decision; #11 is the authority.

Real IdP subjects are not NanoIDs (`auth0|507f1f77…`, `google-oauth2|1234…`), and Contract #1 §1.1 keys
must stay NanoID-shaped — §3.1 already refuses malformed keys but deferred the mapping. Two candidate
shapes; **recommendation: (a)**:

- **(a) Deterministic derivation at the Gateway:** `ActorID = vtx.identity.<sha256NanoID(iss + "|" + sub)>`.
  Zero IdP-vendor coupling, zero enrollment state, restart-safe, collision-resistant, and it mirrors the
  codebase's own idiom for exactly this job — the credentialindex key *is* `sha256NanoID(actorKey)`, and
  the dedup index keys are `sha256NanoID("email:"+email)`. The identity vertex stores the raw `iss`/`sub`
  as an aspect for audit. `iss` is included so two IdPs can't collide subjects.
- **(b) IdP-side enrollment:** a post-registration IdP action calls Lattice to mint the identity and
  writes the NanoID back into the token as a custom claim. Rejected as the default: per-IdP-vendor
  configuration, a stateful enrollment step that can fail out-of-band, and it makes token issuance depend
  on a Lattice round-trip — all to avoid a pure function. (a) degrades to (b)-compatible if a deployment
  insists (a custom claim, when present, wins).

### 11.5 Refinements folded into the shelved build scope (the design's §3, amended)

- **R1 — Gateway credential→identity resolution.** The `credential-bindings` materializer
  (`events.identity.>` → local bucket, mirroring the shipped revocation materializer), the
  resolve-then-stamp step on both write and read paths, and the `ClaimIdentity` raw-credential carve-out
  (§11.0). This is the piece that makes "the person acts as their business identity" real; without it the
  claim binds a credential nothing ever resolves through.
- **R2 — `ClaimIdentity` grants `consumer` to U atomically.** One `holdsRole` link mutation added to the
  claim script — the claim is the moment U becomes an acting identity, so the role rides the same commit
  (no window where the person acts as a role-less U). Note this makes the claim script emit a
  `holdsRole` link that rbac-domain's `AssignRole` normally owns — same-shape link, package-declared,
  `permittedCommands` on the link class is empty so no step-6 conflict; called out so ratification sees it.
- **R3 — the `sub` mapping** per §11.4(a), landing in `internal/gateway/auth` (the ActorID construction)
  so write path, read path, and revocation all derive identically.
- **R4 — `RotateClaimKey` (lost-secret re-issue).** Staff-gated (same grant roles as
  `CreateUnclaimedIdentity`), payload `{identityKey, claimKeyHash}` — overwrites `.claimKey` with a new
  client-minted hash, valid only while `state == "unclaimed"`, fails closed otherwise (§11.1a). Without
  it a lost brochure permanently strands an unclaimed identity (the hash is unrecoverable by design).

The dead-scaffolding verdict (§9) is **unchanged** — these refine what gets built when the driver files;
they do not create a reason to build sooner. The E2E in §7 gains one scenario: the full §11.1 walk-in
arc (staff-create → provision → claim → act-as-U task grant match).
