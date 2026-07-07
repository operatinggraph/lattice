# Lifting Loupe — real operator auth for the console (design)

**Status:** ✅ **Andrew-ratified (2026-07-06).** Designer fire (Winston, 2026-07-06) · Loupe (Stream 3) +
Lattice lanes · **depends on** `real-actor-write-auth-e2e-design.md` Phase 1 (shared Fake IdP +
`up-full-capability`) · **reconciles with** `control-plane-capability-authz-design.md` (✅ CLOSED)

**🏗️ Build checkpoint (Loupe lane):** §7 item 3 (operator login gate) **SHIPPED** `19c1dd0` —
`requireOperator` wraps the whole mux (static UI + every `/api/*`), fail-closed, `LOUPE_DEV_AUTH` /
`LOUPE_JWT_PUBLIC_KEY` postures mirroring `cmd/loftspace-app/readauth.go`; verified live (unauthenticated
→ 401 on both the UI and the API, forged token → 401, dev-minted token → 200) + CI green. **Next:** §7
item 4 (op-submissions relay through the Gateway, replacing `adminActor` direct-stamp in
`op.go`/`server.go`/`objects.go`/`pkg.go`) — depends on the Lattice-lane operator-privilege decision (B:
the `consoleOperator` role package) landing so the relayed operator carries real scoped grants; items 5
(pkg-lifecycle gating) and 6 (e2e proof) follow.

> **Ratification (Andrew, 2026-07-06): B then C — both built, C not deferred.** The operator-privilege
> fork (§4) resolves to **build B first** (the scoped `consoleOperator` role; pkg-lifecycle stays a
> distinct root-admin path *in the interim*), **then build C** (per-role scoped privileged-lane grants —
> the real fix that retires the class-blind all-or-nothing operator-root and lets `consoleOperator` do
> pkg-install without full root). C is a committed Lattice-lane build sequenced after B, **not** a flagged
> follow-on. C's own design: **[scoped-privileged-lane-grants-design.md](scoped-privileged-lane-grants-design.md)**.
> Option A (operators = root) is rejected as analyzed.

---

## For Andrew

**What it does (two lines).** Makes Loupe authenticate a **real human operator** (login, not
localhost-trust) and submit its ops as that **verified operator through the Gateway** — instead of being
an auth-less console that stamps `bootstrap.BootstrapIdentityKey` (kernel root) on every write. It's the
**operator tier** of the real-actor-write-auth initiative; the *control-plane* half is already lifted
(`controlauth`, verified-operator JWT, CLOSED), so this closes the two remaining surfaces: the
**op-submission path** and the **console's own front door**.

**⚠️ A security-design finding you should see first (it frames the fork below).** Grounded in code
(`internal/bootstrap/system_actors.go:42-44` + `internal/capabilitykv/keys.go:70-77`): **any
`holdsRole→operator` identity is fully root-equivalent, class-blind** — a plain-`identity` human granted
`operator` gets the exact kernel anchor (all 4 privileged lanes + the 6 meta/install ops) a
`identity.system.*` service actor gets. There is **no scoped-operator tier** — the model cannot express
"this human may shred/revoke but not install packages or forge meta-vertices." This (a) means "make Alice
a Loupe operator" today silently makes Alice **kernel root**, and (b) **contradicts
`control-plane-capability-authz-design.md` §3.5's claim** that its seeded operator is "an *ordinary
(non-system) rbac actor*… projects to `cap.roles`" — as-built it's root-equivalent. Plus a **boot-snapshot
staleness** seam (`SystemActorKeys` is frozen at Processor boot: a post-boot operator is *under*-privileged
until restart; a de-operator'd one relies on the anchor-key disappearing, not the snapshot). Surfaced as a
standalone Lattice-lane finding row.

**The fork — what a "Loupe operator" should be (my recommendation; the call is yours):**

| Option | What | Trade-off |
|---|---|---|
| **A — operators are root-equivalent** (`holdsRole→operator`) | Everything works incl. the pkg-lifecycle tab. | Every human operator = kernel root; the Gateway now stamps root actors; adding an operator needs a Processor bounce (snapshot). Worst blast radius; doesn't fix the finding. |
| **B — scoped `consoleOperator` role now (RECOMMENDED)** | A narrow role granting only the **default-lane** console ops (shred/revoke/object) + the `ctrl.*` grants — **not** `holdsRole→operator`, so no anchor, no root. The **pkg-lifecycle tab stays a distinct, rarely-used root-admin path** (the existing bootstrap admin), not a routine operator function. | Least-privilege; fixes the finding for the operator identity; no boot-snapshot dependency (default-lane works post-boot). Cost: pkg-install isn't an ordinary-operator action (arguably correct — it's a deploy act). |
| **C — build scoped privileged-lane grants (the platform fix, flagged as follow-on)** | Let a role grant specific meta-lane ops (e.g. `InstallPackage`) *without* the full anchor — the `lenses.go:113-114` "per-actor lane scoping" refinement. | The proper long-term fix: lets even pkg-install be operator-scoped without root. A real Lattice-lane platform change (the anchor/lane model); bigger. Sequence after B. |

**Recommendation: B now + C flagged as the platform follow-on.** B gives Loupe a real least-privilege
operator for the routine console and fixes the "every operator is root" smell; C is the honest fix that
would later let the pkg tab also be operator-scoped. A is the path of least resistance and the one I'd
avoid — it's exactly the internet-facing-surface-gets-full-root shape the claim-flow narrow-role decision
rejected.

**Frozen-contract change: none** (verified §5). Reuses the shipped `internal/gateway/auth` +
`internal/controlauth` seams and the Fake IdP from the parent initiative.

---

## 1. Problem & intent — what's already lifted, and what isn't

Loupe is the admin/inspector console (`cmd/loupe`, binds `127.0.0.1:7777`, **auth-less, acts as admin** —
`main.go:9-12`). Three write/act surfaces; only one is lifted:

| Surface | Today | Status |
|---|---|---|
| **Control plane** (`lattice.ctrl.>` pause/resume → Weaver/Refractor) | Verified operator JWT via `controlauth` (`LOUPE_OPERATOR_ACTOR_TOKEN`, minted by `gateway dev-token`) | ✅ **already lifted** (`control-plane-capability-authz` Fire 1a+1b+1c+2, CLOSED) |
| **Op-submissions** (`core-operations` → Processor): `AttachObject`/`DetachObject`/`Install`/`Uninstall`/`Upgrade`/`RevokeActor`/`ShredIdentityKey` | Direct-submit stamped `s.adminActor` = **bootstrap root** (`op.go:36-39`, `server.go:559`, `pkg.go:479`) | ❌ **the gap** |
| **The console + reads** (direct Core-KV inspector, Health-KV, lens, vault-decrypt proxy) | **Auth-less; whoever is on localhost is root** (`main.go:9-12`) | ❌ **the gap** |

**Intent.** Two closures: (1) a human **operator login** so the console is entered by a *named,
authenticated* operator, not "anyone on 127.0.0.1"; (2) Loupe's op-submissions carry that **verified
operator** (Gateway-stamped) so they authorize by the operator's *real grants* under capability mode —
and Loupe loses its ability to stamp an arbitrary actor. Reads stay direct (the inspector's job is to see
everything — an authN gate, not RLS). This is the operator-tier proof that capability write-auth works for
the console, the sibling of the verticals' consumer/staff-tier proof.

---

## 2. Grounding — reuse-heavy (most of the seam exists)

- **`internal/controlauth`** (built, CLOSED) — verified-operator-JWT for the control plane, minted by
  `gateway dev-token`, verified at the control service. **Reuse its operator-JWT** as the same token
  Loupe presents elsewhere.
- **The seeded operator identity** — `control-authz` package seeds `vtx.identity.<operator>` +
  `LOUPE_OPERATOR_ACTOR_KEY` (`main.go:135-146`). A vehicle to reuse (re-scoped per the fork).
- **`internal/gateway/auth` + the Fake IdP** (parent initiative Phase 1) — the shared dev IdP both the
  Gateway and Loupe trust; the operator's token is issued there. **Loupe's read-auth gate reuses the
  `readauth.go` pattern** the verticals already proved (verify Bearer JWT → gate access).
- **The Gateway write path** (shipped, in `up-full`) — `POST /v1/operations` verify-and-stamp. Loupe's
  op-submissions relay the operator's JWT here.
- **The finding's mechanics** — `SystemActorKeys` (`system_actors.go:35-66`, class-blind topology),
  the boot snapshot (`cmd/processor/main.go:119-135`), the anchor vs `cap.roles` lanes
  (`internal/bootstrap/lenses.go:115,207`), `InstallPackage`→meta lane (`internal/pkgmgr/installer.go:311`),
  `Shred`/`Revoke`→default lane (`packages/privacy-operator-grant/permissions.go:15-26`,
  `packages/identity-domain/revocation.go:183-198`).

---

## 3. The shape

### 3.1 Operator login (the front door)

Loupe gains a read-auth gate mirroring `cmd/loftspace-app/readauth.go`: verify the operator's Bearer JWT
(from the shared Fake IdP) via `internal/gateway/auth`; **no valid operator token ⇒ no console**
(fail-closed, replacing the localhost-trust posture). Reads (`corekv`, `health`, `lens`, `vault` proxies)
run **as they do today** once the operator is authenticated — the inspector sees the whole graph; this is
an **authN gate** ("are you a logged-in operator?"), **not** RLS row-filtering (an inspector with RLS
wouldn't be an inspector). Loopback + dev-auth stays the local-dev posture (the same loopback refusal
`readauth.go:95` enforces); a non-loopback Loupe now *requires* a real operator IdP.

### 3.2 Op-submissions via the Gateway (BFF-relay)

Loupe relays the **operator's JWT** to the Gateway `POST /v1/operations` for every op — both
browser-initiated simple ops (revoke/shred/object) **and** backend-built ops (the pkg-lifecycle batch,
which Loupe's backend assembles via `pkgmgr.NewInstaller`). The Gateway verifies the token and stamps the
**verified operator**; Loupe **stops stamping `adminActor`** and loses the ability to forge an actor
(even a compromised Loupe can only act as operators whose tokens it currently holds, never arbitrary
root). Under capability mode the operator's **real grants** authorize each op.

- **Why BFF-relay, not the verticals' browser-direct** (a deliberate difference, my call): Loupe's
  backend legitimately *builds* some ops (the InstallPackage batch is assembled server-side from the
  package definition — the browser can't produce it). Relaying the operator's token covers backend-built
  and browser-built ops uniformly through one path, and still removes Loupe's forge-any-actor power (the
  Gateway stamps the token's subject, not Loupe's choice). The verticals are simple write proxies, so
  browser-direct fit them; Loupe isn't, so relay fits it. Both present a *verified* operator token to a
  *verifying* Gateway — the trust property is identical.
- **Verification points differ by transport, consistently:** op-submissions are verified **at the
  Gateway** (F2-A: the Processor trusts `env.Actor`); control-plane requests are verified **at the control
  service** (`controlauth`). Same operator token, two verifiers, because two transports (`core-operations`
  vs `lattice.ctrl.>`) — no contradiction, the architecture's existing split.

### 3.3 Control plane (already lifted — just share the token)

No change beyond feeding the **same Fake-IdP-issued operator token** into
`LOUPE_OPERATOR_ACTOR_TOKEN` that `controlauth` already verifies. One operator login → one token → used
for op-submissions (via the Gateway) and control-plane requests (via `controlauth`).

### 3.4 The operator's grants (the fork, §4) determine what the console can do

Under **B** (recommended): the `consoleOperator` role grants the **default-lane** ops (shred/revoke/object)
+ `ctrl.*`; the **pkg-lifecycle tab** is gated to a **distinct root-admin path** (the existing bootstrap
admin identity, used explicitly and rarely — a deploy act, not a routine console action), or hidden for
`consoleOperator`s. Under **A**: the operator is `holdsRole→operator` (root) and the pkg tab works as the
operator. Under **C**: a scoped meta-lane grant lets `consoleOperator` do pkg-install without full root.

---

## 4. The operator-privilege fork, designed through

The console's op set splits cleanly by lane (grounded §2):

- **Default-lane, operator-role-granted (`cap.roles` suffices, no anchor):** `ShredIdentityKey`,
  `RevokeActor`, `UnrevokeActor`, `AttachObject`/`DetachObject`. A scoped role covers these; works for a
  post-boot identity (no snapshot dependency).
- **Meta-lane, anchor-only (needs root-equivalence):** `InstallPackage`, `UninstallPackage`,
  `UpgradePackage`.

So the design choice is entirely about the meta-lane tier:

- **A — operators are root-equivalent.** Seed each operator `holdsRole→operator` before boot. Rejected as
  the default: makes every operator kernel root (contra the claim-flow narrow-role precedent), ties
  operator-add to a Processor bounce, and has the Gateway stamping root actors.
- **B (RECOMMENDED) — scoped `consoleOperator`, pkg-lifecycle stays a separate root-admin path.** Fixes
  the finding for the operator identity, least-privilege, no snapshot dependency. Cost: pkg-install isn't
  an ordinary-operator action — which is arguably *correct* (installing a package into a live platform is
  a deploy-tier act, not routine console use). If B is chosen, the **existing seeded operator identity is
  re-scoped** from `holdsRole→operator` (root) to the `consoleOperator` role — which also closes the §3.5
  drift.
- **C — build scoped privileged-lane grants (the platform follow-on).** The `lenses.go:113-114`
  "per-actor lane scoping" refinement: a role grants specific meta-lane ops without the whole anchor. This
  is the honest fix that would later let `consoleOperator` do pkg-install without root. A Lattice-lane
  platform change; flagged, sequenced after B.

**Recommendation: B now, C flagged.** Andrew decides the privilege posture; the rest of the design is
invariant to A/B/C (login + relay-through-Gateway are the same; only the operator's role vertex differs).

---

## 5. Contract surface — none

| Contract | Why untouched |
|---|---|
| #6 Capability KV | The operator's grants are ordinary `cap.roles` projections of a role's `grantedBy` links — package-level data (a `consoleOperator` role + permissions), no doc-shape change. Option C *would* touch the anchor/lane model — but C is a flagged follow-on, designed separately if chosen. |
| #2 / Gateway | Loupe presents a Bearer token to the shipped `/v1/operations`; no new field, no envelope change. |
| #7 Bootstrap | Re-scoping/seeding an operator identity is standard rbac data (or a bootstrap seed like the control-authz package already does). |
| #10 / control-plane | Unchanged — `controlauth` already verifies the operator JWT. |

---

## 6. Reconciliation with the existing mental model

- **Didn't we already lift Loupe?** The **control plane** — yes (`controlauth`, CLOSED). The
  **op-submission path** and the **console front door** — no; those still stamp root and trust localhost.
  This closes exactly those two.
- **Does this contradict a design-of-record?** It **surfaces a drift** in one:
  `control-plane-capability-authz-design.md` §3.5 states its seeded operator is "an ordinary (non-system)
  rbac actor… `cap.roles`," but the later class-blind `SystemActorKeys` (2026-07-03) made any
  `holdsRole→operator` identity root-equivalent — so as-built that operator is root, not ordinary. Flagged
  for Andrew (a §3.5 correction, or the B re-scope that makes the claim true again). Not silently edited —
  it's Andrew's ratified design.
- **New state?** Under B: one `consoleOperator` role + its permissions (rbac data), and Loupe's login
  gate + relay plumbing. No new lens/bucket/contract concept. The operator JWT is the parent initiative's
  Fake IdP token, reused.

---

## 7. Decomposition — cross-lane, sequenced behind real-actor-write-auth Phase 1

**Lattice lane:**
1. **The operator-privilege decision (the fork).** If **B**: define the `consoleOperator` role +
   permissions (a package like `control-authz`/`privacy-operator-grant`), re-scope the seeded operator
   identity off `holdsRole→operator`. File the **finding row** (class-blind operator-root + boot-snapshot
   staleness) regardless. **C** is a separate flagged design if Andrew wants scoped meta-lane grants.
2. **(If C, later)** the scoped privileged-lane grant capability.

**Loupe lane (Stream 3):**
3. **Operator login gate** — `readauth.go`-pattern Bearer verification over the shared Fake IdP; no token
   ⇒ no console; reads stay direct behind it.
4. **Op-submissions relay through the Gateway** — replace the `adminActor` direct-submit
   (`op.go`/`server.go`/`objects.go`/`pkg.go`) with a relay of the operator's JWT to `/v1/operations`;
   Loupe stops stamping an actor.
5. **Pkg-lifecycle tab gating** per the fork (B: distinct root-admin path / hidden for consoleOperators).
6. **The e2e** — under `up-full-capability`, a real operator logs in, shreds/revokes (allowed), and is
   **denied** a meta-lane op it lacks (B) — the operator-tier analog of the verticals' allow/deny proof.

Sequence behind the parent initiative's Phase 1 (the shared Fake IdP + `up-full-capability` must exist
first). The control-plane path needs no rebuild.

---

## 8. Self-adversarial pass (security plane — run, folded in)

- **The console front door must fail closed.** No valid operator token ⇒ 401/no console (never a silent
  localhost-admin fallback on a non-loopback bind). The loopback+dev-auth dev posture stays gated exactly
  as `readauth.go:95` enforces.
- **Loupe must lose forge-any-actor power.** Post-lift Loupe never stamps `env.Actor` — it relays the
  operator's token and the Gateway stamps the verified subject. Assert an op submitted while Loupe holds
  operator-A's token commits as A, and Loupe cannot cause an op to commit as any actor whose token it
  doesn't hold (the impersonation gate, mirroring the verticals' forged-actor vector).
- **The deny must be real (B).** The e2e must assert a `consoleOperator` is **denied** a meta-lane op
  (`LaneUnauthorized`/`AuthDenied`), not merely that allows pass — the same green-means-nothing trap.
- **The finding is not silently accepted.** "Every operator is kernel root" is filed as a Lattice finding;
  B (or C) is the fix, not a shrug. Boot-snapshot staleness: document that operator privilege changes
  need a Processor bounce until scoped-lane/dynamic routing lands (a known seam, not this design's to fix).
- **Reads stay inspector-wide by design, but behind authN.** An authenticated operator sees the whole
  graph (inspector) — that's intended and *not* a leak, because entry now requires a named operator token;
  the pre-lift state (any localhost process = full read) is strictly weaker.

No default-open, no forge-any-actor, no green-means-nothing test. A `bmad-party-mode` pass is the pre-build
gate (security-plane), run as the Designer-lane obligation before build-ready.

---

## 9. Test strategy

- **E2E (`up-full-capability`):** operator logs in via the Fake IdP → `ShredIdentityKey`/`RevokeActor`
  relayed through the Gateway **commit** as the verified operator; a meta-lane op is **denied** (B); the
  control-plane pause/resume still works with the same token. Assert Loupe stamps no actor of its own.
- **Unit:** the login gate (valid/expired/missing token; loopback refusal); the relay builds the Gateway
  request with the operator's Bearer token, never an actor field.
- **Regression:** loopback dev posture unchanged; the control-plane path byte-identical (already CLOSED).

---

## 10. Risks + alternatives

- **Rejected — keep Loupe direct-submitting as a "trusted single-identity" (like the engines), just swap
  root→operator key.** Loupe would still be *trusted to stamp* an actor (the impersonation hole); a
  compromised Loupe = operator (or root) impersonation. Relaying the verified token through the Gateway is
  the honest fix — Loupe asserts nothing. (The engines keep direct-submit because they're pre-provisioned
  service actors with no human in the loop; Loupe has a human whose identity must be *verified*, not
  *asserted by Loupe*.)
- **Rejected — Option A (operators = root) as the default.** Least effort, worst blast radius; see §4.
- **Residual — boot-snapshot staleness.** Operator privilege changes need a Processor bounce until scoped/
  dynamic routing lands. Documented, not this design's scope (it's the finding + Option C's territory).
- **Residual — the pkg-lifecycle tab under B.** Installing packages becomes a deliberate root-admin act,
  not a routine operator function. If that's too restrictive for the intended ops model, that's the signal
  to prioritize Option C (scoped meta-lane grants) — flagged.

---

## 11. Companion updates in this fire

- `_bmad-output/planning-artifacts/backlog/loupe.md` — a row for the Loupe-lane lift (login + op-relay +
  pkg gating), 📐 awaiting-Andrew, depends on real-actor-write-auth Phase 1.
- `_bmad-output/planning-artifacts/backlog/lattice.md` — a **finding row** (class-blind operator-root +
  no scoped-operator tier + boot-snapshot staleness; the Option B re-scope / Option C platform fix), and
  the operator-privilege decision as the Lattice-lane half of this design.
- `control-plane-capability-authz-design.md` §3.5 — **flagged, not edited**: its "ordinary non-system
  actor" claim is drift vs as-built; Andrew's call to correct it or resolve via the B re-scope (it's his
  ratified, closed design).
