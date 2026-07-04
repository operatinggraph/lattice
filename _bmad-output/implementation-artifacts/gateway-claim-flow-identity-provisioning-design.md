# Gateway claim-flow authz contradiction — identity self-provisioning (design)

**Status:** 📐 **awaiting-Andrew (ratification)** · Designer fire (Winston, 2026-07-04) · Lattice lane
(Arch-review intake / Gateway Fire 4 re-grounding)

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

**Should this be built now?** **No — ratify the design, shelve the build.** Grounding (§2.5) found **zero**
current or planned consumer for self-service signup: every op in both reference verticals
(`clinic-domain`, `loftspace-domain`) is granted to `operator` only, and `lease-signing/permissions.go`
already says outright *"no end-consumer submits a service-instance create in the demo."* Building the
provisioning mechanism now is exactly the dead-scaffolding pattern this skill flags — a real consumer
(a vertical that actually wants self-service consumer signup) doesn't exist yet. What ships **now** (this
fire, doc-only) is the grounding + the corrected docs/board; what ships **when a real driver appears** is
§3's mechanism, fully designed and ready to build in one Steward fire.

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
Gateway's own `env.Actor` (`bootstrap.GatewayIdentityKey`), tolerate the reply, add the actor to the set,
then proceed exactly as today. The set is a pure latency optimization (bounded/LRU; a false miss just
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

## 9. Dead-scaffolding verdict + decomposition for the Steward

**Verdict: shelve.** Per §2.5, the consumer for this mechanism (a vertical that needs true self-service
consumer signup, no staff intermediary) does not exist in either reference vertical today. Ratify the
design; do not build §3 until a real driver files (a vertical backlog item asking for self-service
signup, or Andrew greenlighting it directly).

- **Increment 0 — ship now, doc/comment fixes only (no design dependency, no code risk).** Fix the stale
  `permissions.go:14-16` comment (§2.2) so it no longer misattributes scope enforcement to the Starlark
  script. Trivial, one line, unrelated to whether §3 ever builds.
- **Increment 1 — the mechanism (§3), build only once a real consumer files.** One Steward fire: the new op
  + role + permission (identity-domain), the new bootstrap identity (`internal/bootstrap`), the Gateway-side
  pre-flight + cache (`internal/gateway`), the one-time ops `AssignRole` step, tests per §7. Independently
  shippable, independently valuable the moment it lands (unblocks whatever vertical triggered it) — not
  dead scaffolding at that point because the consumer will exist by construction of being the trigger.

No dead code shipped by this fire itself — this fire is doc-only (this design + the companion doc/board
edits in §10); §3 is fully specified and ready, not built.

---

## 10. Companion doc/board updates made in this fire

- `docs/components/gateway.md` — Fire 4 bullet rewritten: retired as originally conceived, points here.
- `_bmad-output/implementation-artifacts/gateway-external-trust-boundary-design.md` — §3.3 (identity-claim
  front) and §8 item 4 (Fire 4) rewritten in place (not banner-only) to strike the unauthenticated-door plan
  and point here, per the "a ratification revision must rewrite the body it supersedes" rule; §5's contract
  table row for #9 corrected to drop the retired `/v1/claim` reference.
- `_bmad-output/planning-artifacts/backlog/lattice.md` — this row and the Gateway epic row updated to
  `📐 awaiting-Andrew` with links here.
