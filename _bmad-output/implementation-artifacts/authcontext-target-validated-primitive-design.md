# `op.authTargetValidated` — a validated-target primitive that closes the forgeable-`authContext.target` bypass per-op

**Status: ✅ SHIPPED (both fires + both residuals, 2026-07-24) — item closed; §11 + §12 + §13 are the as-built record.** Supersedes the FALSIFIED + REVERTED platform-blank
approach in [`authcontext-target-forgery-platform-fix-design.md`](authcontext-target-forgery-platform-fix-design.md)
(that doc's §Falsified stands as the record of why one blanket rule cannot work). This design keeps the
vulnerability's per-op nature but pays down the shared root with **one small, additive platform primitive**
the guards adopt, instead of five bespoke script patches.

---

## Ratification record (Andrew, 2026-07-24)

**What it does, in two lines.** Adds a wire-immune, script-visible boolean `op.authTargetValidated` — true
iff step-3 *validated* `authContext.target` (scope=self requires `target == actor`; a task grant requires
`ephemeralGrant.target == authContext.target`), false for scope=any / service (where target is an
unchecked, forgeable hint). The four still-vulnerable packages (cafe / wellness / maintenance /
lease-signing) rekey their workplace-confinement exemption from the forgeable `authContextTarget != ""` onto
`op.authTargetValidated`, closing the bypass **without** breaking maintenance's legitimate task path.

**The four decisions taken at ratification:**

1. **Exposure mechanism = the `json:"-"` envelope field** (`AuthTargetValidated bool` on
   `opwire.OperationEnvelope`), set in `commit_path.go` after `Authorize`. Unforgeable (dropped on unmarshal,
   overwritten post-auth) with no interface churn. The `Executor.Execute` signature widening (§5.3) is
   **rejected** — cleaner separation does not pay for touching every executor + test double.
2. **identity-domain idiom C = (A), tighten.** `RecordIdentityPII` rekeys onto the primitive and
   `TestRecordPII_TaskScopedNotConfinedToUnclaimed` is rewritten to submit the *real* task grant. The
   unclaimed-only confinement is otherwise defeated by any front-desk staff, which is not a policy we want
   standing. This is now **in build scope** (Fire 2), not a flagged option.
3. **The lint gate is MANDATORY, not an optional follow-on** (Andrew: *"Lint is how agents are actually
   forced to do the right thing. Everything else is 'fingers-crossed'."*). A migration that clears the debt
   without closing the door leaves the next agent free to reintroduce the forgeable idiom, and the codebase
   has no other mechanism that would catch it. §5.5's "it cannot classify idiom A vs idiom B" objection is
   **withdrawn and resolved**: the gate does not classify — it **default-denies the bare idiom and requires
   the author to declare the safe shape**, mirroring the shipped `# read-posture:` annotation convention
   (`scripts/lint-conventions.go:132/317/493`). Fail-closed, structural, no semantic analysis. See §5.5 + Fire 2.
4. **Fires 1+2 collapse into one fire** (fewer-larger-fires: the primitive's only consumer is the migration —
   coupled, ships together). Final plan is two fires, §8.

**No frozen-contract change.** The `op.*` Starlark surface is not a frozen table (verified §4), so
`op.authTargetValidated` is additive build-to. The one *additive documentation* sentence in Contract #2 §2.8
is **accepted and folded into Fire 1's scope** — it keeps the script-author surface discoverable; nothing is
staged uncommitted.

---

## 1. Problem + intent

The [reverted platform-blank design](authcontext-target-forgery-platform-fix-design.md) established the
vulnerability and why a single processor change cannot fix it. Recap, grounded:

- The Gateway forwards the client's `authContext.target` verbatim (`gateway.go:753`) into
  `op.AuthContext.Target`, exposed to every script as `op.authContextTarget` (`starlark_runner.go:432`).
- Step 3 authorizes a **platform `scope=any`** grant **without inspecting target**
  (`step3_auth_capability.go` `matchPlatformPermission` "any" case returns Authorized immediately). Only
  two paths validate target: **scope=self** (`matchPlatformPermission` "self": `target != actor` is a hard
  denial, `step3_auth_capability.go:524`) and **task/ephemeralGrant** (`matchEphemeralGrant`:
  `g.Target != ac.Target` ⇒ continue, `:346`). Service (`matchServiceAccess`) never reads target.
- So a script that keys a self/workplace **exemption** on `authContextTarget != ""` trusts a field any
  scope=any holder can forge. clinic-domain was hardened in W1 Inc 2a (rekeyed onto
  `authContextTarget == op.actor`, `ddls.go:1672/1698`); the other four packages stayed open.

**Why the blanket blank was falsified** (kept from the prior doc): `RecordIdentityPII` is submitted by a
scope=any front-desk actor with `authContext.target = <the onboarding identity>` (≠ actor) as a *legitimate
signal*, indistinguishable at the platform level from a forged clinic-booking target. Blanking target on
every non-self path broke it (`TestRecordPII_TaskScopedNotConfinedToUnclaimed` went red). **The distinction
is per-op semantic** — does *this* op's guard trust target as a security boundary or a routing hint? — so the
platform cannot decide it alone.

**The intent of this design:** the platform *can* still contribute the one fact every guard needs and none
can compute for itself — **was this target validated by the auth path, or is it an unchecked hint?** That
single bit lets each guard make its own correct decision, replaces the forgeable `!= ""` idiom with an
unforgeable one, and — critically — does *not* blank anything, so a legitimately-forwarded target
(identity-onboarding, self-service ownership binds) is still visible to the scripts that want it.

## 2. Grounding — the two forgeable idioms and the one that is safe

Reading all `authContextTarget` sites across the five packages (grep of `packages/**/*.go`), the field is
used in **three** distinct shapes. Only the first is a bypass.

### Idiom A — the workplace-confinement EXEMPTION (the vulnerability)

`workplace_exempt()` / `require_workplace()` short-circuit confinement when `authContextTarget != ""`:

```python
def workplace_exempt():
    return op.authContextTarget != "" or actor_holds_operator(op.actor)   # FORGEABLE
```

A scope=any staff caller forging any non-empty target skips workplace confinement entirely. Sites:

| Package | Op(s) gated by idiom A | Legitimate non-operator paths that reach the exemption |
|---|---|---|
| cafe-domain | `OpenTab` (`ddls.go:577`), `Charge` (`:670`), `VoidCharge` (`:720`), `Settle` (`:754`) | resident **scope=self** self-order/self-settle (`target == actor`) |
| wellness-domain | `CreateSession` (`ddls.go:1046`) | staff-only; no self path (a session is staff-created) |
| maintenance-domain | `ReportIssue` (`ddls.go:407`), `ResolveWorkOrder` (`:460`) | **`ResolveWorkOrder` is TASK-bound** — role-queue claimant submits `authContext = {Task, Target: workOrderKey}`, `Target ≠ actor` (`integration_test.go:289`) |
| lease-signing | `DecideLeaseApplication` (`scripts.go:508`) | landlord/operator-only; no self path |

**The decisive fact** (`integration_test.go:289`): maintenance `ResolveWorkOrder`'s legitimate task path
carries `Target = workOrderKey`, which is neither empty nor equal to the actor. clinic's shipped
`authContextTarget == op.actor` fix **would deny that path**. A per-package copy of the clinic fix is
therefore *incorrect* for maintenance — the correct predicate must admit BOTH a self target (`== actor`) and
a validated task target (`== the grant's target`), which is exactly "the auth path validated it."

**A fourth shape — cafe `Charge`'s `is_self` branch selector** (`ddls.go:655`,
`is_self = op.authContextTarget != ""`) selects the amount source (menu catalog vs. caller `amountCents`)
and gates the idiom-B ownership proof. It is **not** a confinement exemption and is **not exploitable**: a
scope=any forger setting a target is pushed onto the catalog-price branch (cannot inject an amount), still
faces the idiom-B ownership proof, and — post-migration — is still workplace-confined (idiom A now denies
them). It is left on `authContextTarget` **intentionally** (its job is "did the caller declare a self target
at all", not "is that target validated"), documented in §3.4 so a future maintainer does not read the
mixed keying as an oversight.

### Idiom B — the self-service OWNERSHIP binding (safe — a forged target only hurts the forger)

```python
if op.authContextTarget != "":
    # derive target identity, require it OWNS the resource (applicationFor / bookedBy link)
    ...
    if application_for == None or application_for.isDeleted:
        fail("AuthDenied: a resident may only settle their own tab")
```

Sites: cafe `OpenTab`/`Charge`/`Settle` (`:590/:678/:762`), wellness `CreateBooking`/`CancelBooking`
(`ddls.go:1291/1370`), lease-signing `CreateLeaseApplication` (`scripts.go:383`). Here a non-empty target
triggers a **stricter** proof (the target must own the resource via a graph link), which fails closed. A
scope=any forger setting a target gains nothing — they must name the real owner, and even then they are still
bound by idiom A's confinement (which this design fixes). **Idiom B needs no change**; §6 covers why touching
it would be churn (and mildly *weakens* the forger's burden, not the platform's).

### Idiom C — identity-domain's unclaimed-only gate (the falsifying case; §3.5)

```python
if op.authContextTarget == "" and current_state != "unclaimed" and not actor_holds_operator(op.actor):
    fail(...)   # target PRESENCE exempts from the unclaimed-only confinement
```

Same forgeable shape as idiom A (presence exempts), but on a **state-machine** confinement, not a workplace
one — and it is the op the reverted blank broke. Handled as a flagged per-op verdict, §3.5, not shipped.

## 3. The shape

### 3.1 The primitive

Compute, immediately after a successful step-3 `Authorize`, whether the resolved path validated target:

```
authTargetValidated(rp *ResolvedPermission) bool =
    rp != nil && (
        rp.Path == "task"                                          // matchEphemeralGrant proved g.Target == ac.Target
        || (rp.Path == "platform" && rp.PlatformPermission != nil  // matchPlatformPermission "self":
             && rp.PlatformPermission.Scope == "self")             //   proved target == actor
    )
```

Everything else — `scope=any`, service, the stub authorizer (`rp == nil`) — is **false** (fail-closed:
absence of a validated target is "not validated", never "trusted"). This mirrors the shipped auth model
exactly; it invents no new judgment, it merely *surfaces* the judgment step 3 already made
(`ResolvedPermission` is in `operation_context.go:12`; `Scope` on `PlatformPermission`).

### 3.2 Read path (P5) — unchanged

No lens, no read-model. The primitive is a pure function of the step-3 decision, computed in-process. Nothing
is projected or queried.

### 3.3 Write path (P2) — the exposure mechanism

`commit_path.go` already holds `resolvedPermission` after step 3 (`:265`) and threads it into
`commitPipeline` (`:276/:303`). Set the derived bool on the envelope there, before execution:

- **Recommended:** add `AuthTargetValidated bool \`json:"-"\`` to `opwire.OperationEnvelope`
  (`opwire/opwire.go:93`; `processor.OperationEnvelope` is a type alias, `envelope.go:37`, so the processor
  package sets it directly). `json:"-"` makes it **unforgeable** — dropped on unmarshal, so a client's
  `"authTargetValidated":true` never lands; and `commit_path` overwrites it unconditionally after auth
  regardless. `operationEnvelopeToStarlark` (`starlark_runner.go:435`) adds
  `"authTargetValidated": starlarklib.Bool(op.AuthTargetValidated)`.
- This mirrors the accepted pattern of the reverted design, which *mutated* `env.AuthContext.Target`
  post-auth — setting a `json:"-"` bool is strictly cleaner than mutating a wire field, and it never blanks
  a legitimately-forwarded target (the falsification root cause).

The value is transient (per-op, in-process), never persisted, never re-served — so there is no
projection/retraction concern.

### 3.4 The guard migration (idiom A → the primitive)

Each of the four packages' `workplace_exempt()` and `require_workplace()` rekeys the exemption:

```python
def workplace_exempt():
    return op.authTargetValidated or actor_holds_operator(op.actor)

def require_workplace(location_keys, what):
    if op.authTargetValidated:      # was: op.authContextTarget != ""
        return
    if actor_holds_operator(op.actor):
        return
    ... worksAt walk ...
```

Path-by-path correctness (the reason this is uniformly right where `== op.actor` is not):

| Caller | `authTargetValidated` | Exempt from workplace? | Correct? |
|---|---|---|---|
| resident **scope=self** self-order (`target == actor`) | **true** | yes | ✓ a consumer holds no worksAt link; **idiom B then binds ownership to the resource** |
| maintenance **task** claimant (`ResolveWorkOrder`, `target = workOrderKey`) | **true** | yes **only if `target == payload.workOrderKey`** (see §3.4.1) | ✓ *once the resource bind is added* — see below |
| **scope=any** staff, forged target | **false** | no → confined | ✓ the bypass is closed |
| **scope=any** operator (root), no authContext | false | yes (via `actor_holds_operator`) | ✓ operator exemption is unchanged |
| non-operator staff, scope=any, no target | false | no → confined | ✓ staff bound to their building, unchanged |

Note `workplace_exempt()` is the cheap pre-gate; `require_workplace()` re-checks (a site that forgets the
pre-gate is still correct, only slower) — both must move together, exactly as clinic did.

#### 3.4.1 The validated-target exemption is sound only when the target is BOUND to the acted-on resource

The adversarial pass (§ closing block) caught a defect in an earlier draft of this table: `authTargetValidated`
proves the target was *validated*, **not** that the validated target is the resource the op writes. For the
**self-capable** ops (cafe self-order/settle, wellness self-book) that binding exists downstream — idiom B
requires the validated identity to OWN the resource (the `applicationFor` / `bookedBy` link), so a validated
self target that doesn't own the tab/booking fails closed. But for **maintenance `ResolveWorkOrder`** there is
**no idiom-B ownership probe** — the work order is resolved from `payload.workOrderKey`
(`ddls.go:426`), while the validated target is the grant's `scopedTo` (`ac.Target`), and **nothing binds the
two**. `matchEphemeralGrant` proves only `g.Target == ac.Target` (`step3_auth_capability.go:346`); the Gateway
forwards `authContext` and `payload` as independent client fields (`gateway.go:752-753`). So a tech holding a
legit grant for work order **WO-A** can submit `authContext={Task,Target:WO-A}` (validated) with
`payload={workOrderKey: WO-B}` and resolve **WO-B** — a work order at a building they do not work at.
(This gap is *pre-existing*: today's `authContextTarget != ""` exempts identically. The migration must not
merely preserve it.)

**Fix (part of Fire 2 for maintenance):** on the resource-scoped path, the exemption must bind the validated
target to the acted-on resource. `ResolveWorkOrder` stops trusting a bare `workplace_exempt()` and instead:

```python
# The validated-target exemption must name THIS work order: a task grant is
# scopedTo a specific work order (ac.Target), and a claimant must not
# substitute a different one via payload.workOrderKey.
if not actor_holds_operator(op.actor):
    if not (op.authTargetValidated and op.authContextTarget == wkey):
        require_workplace([workorder_location(wkey)], "ResolveWorkOrder on " + wkey)
```

The general principle, stated for the Steward and any future confined op: **a validated-target exemption is
sound iff the validated target is bound to the resource the op acts on** — by idiom B (self-capable ops) or by
an explicit `authContextTarget == <resource key>` check (resource-scoped task ops). `ReportIssue` needs
neither: it is scope=any-standing only (no self/task path, `permissions.go:116`), so `authTargetValidated` is
never legitimately true and a scope=any forger cannot make it true — the plain `workplace_exempt()` migration
is correct there.

### 3.5 identity-domain `RecordIdentityPII` (idiom C) — RATIFIED (A): tighten, in scope

The consistent fix is `if not op.authTargetValidated and current_state != "unclaimed" and not
actor_holds_operator(...)`. Under it: the real onboarding **userTask** (assignee == subject == actor,
task path, validated) stays exempt; a **scope=any front-desk** forger (unvalidated) is confined to unclaimed.
But `TestRecordPII_TaskScopedNotConfinedToUnclaimed` (`record_pii_test.go:378`) submits **front-desk +
`{Target: claimedKey}` with no `Task`** — resolving via scope=any (unvalidated) — and asserts **accepted**.
The consistent fix reds that test; making it green means rewriting it to submit the *real* task path
(`{Task, Target}` matching a seeded `ephemeralGrant`). That is a genuine behavior tightening, and §Falsified
records that front-desk-with-target *was* treated as legitimate. **Ratified (A):** tighten. The behavior
change is intended — a front-desk actor who needs to record PII on a claimed identity must carry the real
task grant, not a self-declared target. Builds in **Fire 2**; the four named packages do not depend on it.

## 4. Contract surface

- **Contract #2 §2.8** (`docs/contracts/02-operation-envelope.md:257-312`) defines `authContext` and the
  step-3 precedence. The **`op.*` Starlark surface is NOT a frozen table** there (verified: the contract
  documents `authContext.target` semantics and uses `op.actor`/`op.payload` in *prose examples* only —
  `03-mutation-batch-event-list.md:118/125` — there is no enumerated, frozen op-field list). Adding
  `op.authTargetValidated` is therefore **build-to, additive, non-breaking** — the same class as the existing
  `op.authContextTarget`/`op.authContextService` fields, which were added without a contract amendment.
- **Ratified — in Fire 1's scope:** one additive sentence in §2.8 documenting the derived field, so the
  script-author-facing surface stays discoverable. This is a doc addition, not a change to any frozen
  invariant, so it lands *with* the build rather than as a staged pre-ratification edit.

## 5. Alternatives considered

1. **Copy clinic's `authContextTarget == op.actor` to the four packages** (the naive "simplest extension").
   **Rejected — incorrect for maintenance.** `ResolveWorkOrder`'s task claimant carries `target =
   workOrderKey ≠ actor` (`integration_test.go:289`), so `== op.actor` denies the legitimate role-queue path.
   `workplace_exempt()` is shared by `ReportIssue` (self-capable) and `ResolveWorkOrder` (task), so no single
   `== actor` predicate is correct even within one package. The primitive is the *smallest* predicate that is
   correct across self AND task; it is a simpler total than five bespoke per-op conditionals.
2. **Platform blank of an unvalidated target** (the reverted approach). **Rejected — falsified.** Blanking
   destroys the legitimately-forwarded target identity-onboarding and idiom B rely on. Surfacing a *validated*
   bit is strictly more information-preserving than *removing* the field: it fixes the exemption bypass while
   leaving every legitimate reader intact.
3. **Widen `Executor.Execute(ctx, env, state, authTargetValidated bool)`.** A cleaner separation (no field on
   the wire struct), but changes the `Executor` interface (`step_interfaces.go:25`) and every implementation +
   test double. **REJECTED at ratification** — the separation-of-concerns gain does not pay for the blast
   radius; the `json:"-"` field is equally unforgeable.
4. **Expose `op.authContextValidatedTarget` (the validated key string, "" when unvalidated)** instead of a
   bool. Rejected as over-carrying: every idiom-A site needs only "was it validated", and idiom B already
   reads the raw `authContextTarget` for its own ownership derivation (which is safe). A bool is the minimum
   the use needs — carrying the string invites a new site to trust it as an address it isn't
   (representation-follows-use).
5. **A `lint-conventions` rule over every `op.authContextTarget` site.** ~~Filed as an optional follow-on.~~
   **RATIFIED AS MANDATORY (Fire 2)** — not an alternative to the primitive but its required companion. The
   earlier objection ("a detector, not the fix; it cannot tell exemption `!= ""` from ownership-binding
   `!= ""` without semantic analysis") is **withdrawn**: it assumed the gate must *classify* the site. It
   must not. The gate **default-denies every bare `op.authContextTarget` comparison in `packages/**` and
   requires the author to declare which safe shape it is** — exactly the shipped `# read-posture:` pattern,
   where `lint-conventions` scans `packages/**` non-test Go files (Starlark lives in Go string literals),
   matches an annotation regex, and fails the unannotated case (`scripts/lint-conventions.go:132`, `:317`,
   `:493`). Declaring is cheap; forgetting fails closed. See §8 Fire 2 for the annotation vocabulary.

   The reasoning that makes this mandatory rather than nice-to-have: a migration clears *today's* debt, but
   the forgeable idiom is what an agent writes by default when adding the next confined op — and nothing else
   in the toolchain would catch it. Clearing the debt without closing the door means re-litigating this
   ★★★ security row the next time someone writes a guard.

**Dead-scaffolding test:** the primitive has five immediate consumers (all idiom-A sites) the moment it
lands; it realizes value before any future dependency. Not scaffolding.

## 6. Reconciliation with the existing mental model

- *Didn't we already fix this?* clinic did, in-package, with `== op.actor` — which happens to be correct
  *there* (clinic has no task-bound workplace op, so self is the only validated non-operator path). The four
  remaining packages include one (maintenance) where `== op.actor` is *wrong*, which is why they were left
  and why the fix is a primitive, not a copy.
- *Does this duplicate/contradict a shipped pattern?* No — it generalizes clinic's fix to its correct form.
  clinic *may* later migrate onto the primitive for consistency, but that is out of scope here — and it is
  a **tightening, not a no-op**: `== op.actor` is itself forgeable (a scope=any holder setting
  `target = <their own actor key>` satisfies the equality and skips `require_workplace`), where
  `authTargetValidated` would be false for that caller. clinic is not currently exposed by it — all three
  call sites are backstopped by a mandatory `identifiedBy` ownership probe that confines the forger to
  patients bound to their own identity, i.e. the legitimate self-book — but the two predicates do **not**
  agree on every path, and a migration must be scoped as a behavior change rather than a cleanup.
- *New state?* None persisted. The bool is derived from the step-3 decision the Processor already computes;
  it lives only for the op's execution.

## 7. Open questions — all closed at ratification

- **Exposure mechanism (fork):** **DECIDED — the `json:"-"` envelope field.** `Execute` signature widening
  rejected (§5.3).
- **identity-domain idiom C:** **DECIDED — (A) tighten**, in Fire 2 (§3.5).
- **Is the lint gate optional?** **DECIDED — no, mandatory** (§5.5, Fire 2). Lint is the only mechanism that
  binds *future* authors; everything else relies on each agent re-deriving the hazard.
- **Does exempting the maintenance task claimant from workplace confinement over-grant?** *It does today, and
  a naive migration would preserve the gap* — the adversarial pass (CONFIRMED) proved `ac.Target` (the grant's
  `scopedTo`, validated by `matchEphemeralGrant`) is **never bound** to `payload.workOrderKey` (the resolved
  resource), so a claimant with a legit grant for WO-A can resolve a WO-B elsewhere. §3.4.1 closes it: Fire 2
  adds `authContextTarget == wkey` to `ResolveWorkOrder`'s exemption, binding the validated target to the acted-
  on work order. With that bind, the exemption is genuinely scoped; without it, it is not. This design ships the
  bind — it does not certify the exemption as safe on the grant alone.
- **Stub-authorizer path:** `rp == nil` ⇒ `authTargetValidated == false`. Stub tests that relied on `!= ""`
  exemption behavior must set an explicit path or assert confinement; enumerated in §8 test strategy. Fail-
  closed is correct (a stub makes no security claim).

## 8. Migration, test strategy, decomposition

**No data migration** — no stored shape changes. A package version bump per touched package (the guard script
is package DDL) so warm stacks pick up the new script ([[reference_package_edit_needs_version_bump]]).

**Security proof, colocated:**
- *Processor* (`internal/processor`): exhaustive unit test on `authTargetValidated` over every
  `(Path, Scope)` shape (platform/self→true, platform/any→false, task→true, service→false, nil→false); a
  commit-path test asserting the envelope bool is set true for a scope=self `target==actor` and a matching
  task grant, false for a scope=any forged target and a service path.
- *Each of the four packages*: a **positive** vector (the legitimate self/task/operator path still succeeds)
  paired with the **negative** forgery vector (scope=any + forged target is now confined/denied) — the
  negative must fail for the *right reason* ([[feedback_negative_test_false_pass]]): assert the positive
  sibling passes first, and that denial is the workplace `AuthDenied`, not an unrelated reject. maintenance
  MUST include the **task-path positive** (`{Task, Target: workOrderKey}` still exempt) — the regression the
  whole primitive exists to protect.
- *Outcome residual* (`internal/bypass`): a forged-target scope=any vector across at least one migrated op,
  proving the bypass is closed end-to-end.
- **Full `go test ./...`** before commit — this changes a script-visible envelope field consumed by
  `packages/*` suites; the reverted attempt reddened a package suite that internal/processor + review both
  passed ([[feedback_local_test_scope_must_include_script_consumers]], [[feedback_full_suite_for_wide_default_change]]).

**Fire decomposition for the Steward** — **two fires** as ratified (the original Fires 1+2 collapsed: the
primitive's only consumer is the migration, so they are coupled-ships-together per fewer-larger-fires).

- **Fire 1 — the primitive + the four-package migration** (internal build order: primitive first, then the
  guards, so the tree is green at each step but lands as one commit).
  1. Add `authTargetValidated(rp)` + the `json:"-"` envelope field on `opwire.OperationEnvelope`; set it in
     `commit_path.go` after step 3 — **once, outside the commit retry loop** (`:303` re-executes without
     re-auth, per the §10 implementation note); expose `op.authTargetValidated` in `starlark_runner.go`.
  2. Rekey `workplace_exempt()`/`require_workplace()` in cafe / wellness / maintenance / lease-signing onto
     `op.authTargetValidated`; **add the §3.4.1 resource bind (`authContextTarget == wkey`) to maintenance
     `ResolveWorkOrder`**; leave cafe `Charge`'s `is_self` (`ddls.go:655`) on `authContextTarget` with a
     one-line "intentionally not a confinement exemption" comment; version-bump each package.
  3. Add the additive Contract #2 §2.8 sentence documenting `op.authTargetValidated` (§4).
  - Tests: the processor `(Path, Scope)` matrix + commit-path assertions; per-package positive+negative
    vectors incl. **(i)** the maintenance task-path positive (grant target == payload work order → still
    exempt) and **(ii)** the task-path *substitution negative* (grant WO-A, payload WO-B, actor not at
    WO-B's building → DENIED — the §3.4.1 regression test); the `internal/bypass` residual.
  - This is the fire that closes the ★★★ row.

- **Fire 2 — identity-domain tighten + the MANDATORY lint gate** (this order is forced: the gate cannot go
  green while a bare idiom remains, and idiom C must not be annotated "safe" since §3.5 ratified it unsafe).
  1. Rekey `RecordIdentityPII` idiom C onto the primitive; rewrite
     `TestRecordPII_TaskScopedNotConfinedToUnclaimed` to submit the real task path (`{Task, Target}` against
     a seeded `ephemeralGrant`); version-bump identity-domain.
  2. Extend `scripts/lint-conventions.go` with an `authcontext-target` rule mirroring `# read-posture:`
     (`:132` regex, `:317` packages-scoped scan, `:493` finding shape): **every `op.authContextTarget`
     comparison in a `packages/**` non-test file must carry an annotation declaring its shape** —
     `# authcontext-target: (ownership)` for the idiom-B stricter-proof binding, `# authcontext-target:
     (selector) <why>` for cafe `Charge`'s branch selector, `# authcontext-target: (resource-bind)` for the
     §3.4.1 `== <resourceKey>` form. **An unannotated comparison, and any bare `!= ""` / `== ""` in an
     exemption position, is an ERROR** — the correct spelling of an exemption is `op.authTargetValidated`.
     Land it **blocking from day one** (not warn-first): Fire 1 + step 1 above leave zero unannotated sites,
     so there is no debt window to phase out, and a warn-first gate is precisely the "fingers-crossed" state
     this fire exists to end.
  3. Annotate the surviving safe sites (cafe/wellness/lease-signing idiom B, cafe `Charge` selector) and
     assert the gate reds on a reintroduced bare exemption (a fixture-based lint self-test).

## 9. Risks

- **A migrated site that ALSO has a self path via idiom B** (cafe/lease-signing): the two guards are
  complementary and independent (idiom A confines the standing/staff path; idiom B binds the self path's
  ownership). Rekeying idiom A does not touch idiom B, and the §3.4 table shows the self path stays exempt
  from A (validated) while still ownership-checked by B. No interaction regression — but the per-package tests
  assert both.
- **Fail-closed edge (pre-existing, unchanged):** an actor holding BOTH scope=any and scope=self for one
  operationType, self-acting, matches scope=any first (first-match, `matchPlatformPermission`) →
  `authTargetValidated == false` → confined on the operator path rather than exempted as self. This is
  fail-closed (the platform authorized it *as an operator*), identical to the reverted design's noted edge;
  the match order, not this change, decides it.
- **Whetstone/CI:** touches a widely-driven envelope path; the full-suite gate (§8) is mandatory, and the
  package version bumps must land or warm stacks run the old forgeable script silently
  ([[feedback_merged_is_not_running]], [[reference_package_edit_needs_version_bump]]).

---

## 10. Adversarial pre-build gate — DISCHARGED (this fire)

A focused adversarial review (independent reviewer, re-verifying every citation against code) ran on this
design. Verdicts:

- **CONFIRMED — maintenance task exemption mis-certified as scoped.** An earlier draft's §3.4/§7 claimed the
  `ResolveWorkOrder` task exemption was "scopedTo that exact work order." False: `ac.Target` (validated grant
  target) is never bound to `payload.workOrderKey` (`ddls.go:426`, `gateway.go:752-753`) — a grant for WO-A
  resolves a WO-B elsewhere. **Folded in:** §3.4.1 adds the `authContextTarget == wkey` resource bind to Fire
  2, the §3.4 table row is corrected, §7 restated honestly, and a substitution-negative regression test added
  to §8. The design now *ships the bind* rather than certifying the grant alone.
- **CONFIRMED (minor) — cafe `Charge` `is_self` is a fourth `authContextTarget` shape** the §2 tables omitted;
  verified **not exploitable** (a forger is pushed onto the stricter branch). **Folded in:** documented in §2
  + §3.4 as intentionally left, with a Fire-2 comment so the mixed keying is not read as an oversight.
- **SOUND, could not break:** the primitive formula (every step-3 path walked — platform any/self/specific,
  service, task, stub — no forgeable-true, no validated-false); idiom B at every site (forged target only
  forces a stricter proof); the `json:"-"` exposure (dropped on unmarshal on every wire path, overwritten
  post-auth); and the migration's denial-safety (`ReportIssue`/`CreateSession`/`VoidCharge`/
  `DecideLeaseApplication` are never self/task-submitted, no consumer scope=self grant on any staff op).
  One implementation note carried into Fire 1: the envelope bool MUST be set once **before** the commit retry
  loop (`commit_path.go:303` re-executes without re-auth) — a stale `false` would be fail-closed, but the set
  belongs outside the loop as §3.3 specifies.

With these folded in, the design went to ratification and is now **build-ready**: Andrew took the exposure
fork (envelope field), the identity-domain §3.5 verdict (A, tighten), the fire collapse, and made the lint
gate mandatory. Ratification-session due diligence re-verified every load-bearing citation independently
against code — `gateway.go` target forwarding, `step3_auth_capability.go:498/:505/:346`, the four still-
unmigrated packages, clinic's shipped `== op.actor` fix, and maintenance `integration_test.go:289`'s
`Target: workOrderKey` — all confirmed, no drift.

---

## 11. Fire 1 build note — SHIPPED

Scope built, item-by-item against §8's Fire-1 list: the primitive (`authTargetValidated(rp)` in
`operation_context.go`, the `json:"-"` envelope field, the single pre-retry-loop stamp in `commit_path.go`,
`op.authTargetValidated` in `starlark_runner.go`), the four-package guard migration, the §3.4.1 maintenance
resource bind, the cafe `Charge` selector comment, four package version bumps (+ their `manifest.yaml`
twins, which the `TestPackage_ManifestMatchesDefinition` gate checks), and the additive Contract #2 §2.8
paragraph. No frozen-contract invariant changed — the §2.8 addition documents a derived, non-input field,
which §4 ratified as in-scope for this fire.

**Three grounding corrections found during the build**, all folded into what shipped:

1. **`require_workplace` cannot host the resource bind.** After the migration its first line is
   `if op.authTargetValidated: return` — so `ResolveWorkOrder` calling it *after* failing the
   `authContextTarget == wkey` check would be re-exempted by the very bit the check just rejected, and
   §3.4.1's bind would be dead code. maintenance-domain therefore splits the helper: `require_workplace`
   keeps the validated-target exemption and delegates to a new **`enforce_workplace`**, which is the
   worksAt walk with only the operator escape. `ResolveWorkOrder` calls `enforce_workplace` directly.
   The other three packages need no split (no resource-scoped task op) and keep the two-line rekey.

2. **§3.1's formula is too weak on the task path — an empty grant target validates nothing.**
   `matchEphemeralGrant`'s check is an *inequality skip* (`g.Target != ac.Target` ⇒ continue), so a grant
   projected with an **empty** target matches an authContext that carries none: `"" != ""` is false. Under
   a literal "`Path == "task"` ⇒ true" the caller gets `authTargetValidated == true` with
   `authContextTarget == ""` — an exemption *weaker than the presence test it replaces*, turning a
   pre-migration denial into an exemption. The shipped formula therefore requires the matched grant to
   name a target: `rp.EphemeralGrant != nil && rp.EphemeralGrant.Target != ""`. (scope=self needs no such
   clause — `matchPlatformPermission` denies an absent target outright before resolving.) Reachability is
   latent rather than demonstrated — `CreateTask` requires `scopedTo` and writes it in the same atomic
   batch — but `capabilityEphemeralSpec`'s `OPTIONAL MATCH (task)-[:scopedTo]->(tgt)` projects
   `target: null` if that link or its target is tombstoned while the task is still unexpired, and
   `EphemeralGrant.Target` unmarshals `null` to `""`. Found by the adversarial pass; pinned by a
   matrix case, a nil-grant case, and an end-to-end vector through the real authorizer.

3. **The exploitable op per package is the one WITHOUT an idiom-B ownership proof.** §2's table lists the
   idiom-A-gated ops, but on the ops that also carry an ownership binding (cafe `OpenTab`/`Charge`/`Settle`,
   wellness `CreateBooking`/`CancelBooking`, lease-signing `Create`/`Withdraw`/`SetApplicantProfile`) a
   forged target is independently denied by that second guard — so a forgery vector there proves nothing
   about the confinement fix. The genuinely exploitable ops are the staff-only ones: cafe **`VoidCharge`**,
   wellness **`CreateSession`**, lease-signing **`DecideLeaseApplication`**, maintenance
   **`ReportIssue`/`ResolveWorkOrder`**. The per-package vectors are written against those.

**Non-vacuity proven, not assumed.** Every negative was run against a temporarily-reverted guard and
observed to **fail** (`accepted`, i.e. the live bypass) before being run green against the fix — cafe
`VoidCharge`, wellness `CreateSession`, lease-signing `DecideLeaseApplication`, and separately the §3.4.1
bind (reverting just the bind makes the WO-A-grant-resolves-WO-B substitution succeed). Each negative is
paired with a positive sibling asserted first.

**Tests:** `internal/processor/auth_target_validated_test.go` (the full (Path, Scope) matrix incl.
`specific`/`owned`/nil-permission/unknown-path fail-closed cases, the same matrix driven through the real
`CapabilityAuthorizer`, wire-immunity both directions, and the Starlark exposure);
`internal/bypass/capadv_forged_authcontext_target_test.go` (the outcome residual: wire-asserted bit dropped,
fabricated target resolves platform/any, genuine scope=self control); and a forgery vector per package.
Gates: `go build`, `make vet`, `golangci-lint` (0 issues), `lint-conventions`, `lint-lens-anchors`,
`lint-package-version`, `make verify-kernel`, and the **full `go test ./...`** the §8 note requires.

**Three-layer adversarial gate (security change ⇒ full depth) — findings folded in.** Independent
bypass-hunt, regression-hunt, and test-quality passes ran against the built tree before admit.

- *Bypass hunt* — **could not refute** the headline claim; found the empty-grant-target hole above
  (fixed), and confirmed the field is unforgeable (one non-test write site repo-wide, neither
  client-controlled struct carries it, no alternate codec, no stale-true retry/redelivery path) and that
  `enforce_workplace` correctly lacks the escape. It also falsified §6's claim that clinic's
  `== op.actor` and the primitive agree on every path — §6 is corrected above.
- *Regression hunt* — **no false-deny** in any shipped path. It traced the maintenance task path
  end-to-end and confirmed Facet fills `payload.workOrderKey` from the same `ctx.scopedTo` string it puts
  in `authContext.target`, so the §3.4.1 bind holds for the real claimant.
- *Test-quality hunt* — confirmed all six package vectors reject for the intended workplace `AuthDenied`
  (verified by reading each `ScriptError`), then found three gaps, all fixed: the `internal/bypass`
  residual was **vacuous** (it asserted only pre-existing resolution shape, so it survived a full revert
  of the migration) and now drives the real `CapabilityAuthorizer` through the real commit path,
  asserting the bit the executor observes; **no commit-path stamping test existed** at all, so the one
  line wiring the primitive to every consumer was covered only incidentally from another package — now
  covered colocated, including the "stable across an OCC retry" property; and the maintenance
  **task-path positive did not isolate** (its work order sat at the tech's own building, so it passed on
  the worksAt walk) — both work orders now sit outside the tech's workplace, making the validated-target
  exemption the only thing that can admit it.

Every one of those fixes was itself checked by reverting the mechanism under test and observing the new
assertion go red.

---

## 12. Fire 2 build note — SHIPPED (item closes)

§8's Fire-2 list built as ratified: identity-domain idiom C rekeyed onto the primitive,
`TestRecordPII_TaskScopedNotConfinedToUnclaimed` rewritten onto a real seeded `ephemeralGrant`, the
mandatory blocking `authcontext-target` rule landed with zero debt, and every surviving safe site
annotated. Four things the build settled beyond that list:

1. **§3.5's rekey was not sufficient on its own — `RecordIdentityPII` needed the §3.4.1 resource bind
   too.** The adversarial pass proved the op is the one migrated site with **neither** an idiom-B
   ownership probe **nor** a bind: `identity_key` comes from `payload.identityKey` while the validated
   target is the grant's `scopedTo`, and nothing joins them. lease-signing's onboarding pattern mints a
   `RecordIdentityPII` userTask `scopedTo` the applicant's own identity, so the applicant — a *consumer*
   holding no standing `RecordIdentityPII` grant — could pair that legitimate grant with a payload naming
   any other claimed identity and write `.ssn`/`.dob` onto them. Pre-existing (the presence test was
   defeated identically), but §3.4.1 says the migration must not merely preserve it. Shipped as
   `resource_bound = op.authTargetValidated and op.authContextTarget == identity_key`, mirroring
   maintenance `ResolveWorkOrder`. Pinned by a payload-substitution vector proven non-vacuous.

2. **The gate's helper set is DERIVED, not listed.** Anchoring on the literal `workplace_exempt()` left
   `require_workplace()` — which Fire 1 made an exemption in its own right (§11 correction 1) — ungated,
   and let any package invent its own differently-named helper to escape. The linter instead reads each
   file for `def`s whose body consults `op.authTargetValidated`/`op.authContextTarget` (to a fixpoint,
   so a helper calling a helper counts) and gates calls to those. That immediately surfaced one genuinely
   uncovered site: lease-signing `DecideLeaseApplication`'s inner `require_workplace`, whose pre-gate
   annotation sat outside the window behind a `kv.Links` enumeration.

3. **clinic is declared, not migrated, and not copyable.** §6 scoped clinic's `== op.actor` migration out
   as a behaviour change, so the gate cannot simply deny its two sites — and a generic annotation would
   have blessed the forgeable idiom for every future author. `(legacy-self-exempt)` is therefore admitted
   **only** in the files `authCtxTargetLegacyFiles` names: declaring it means editing the linter, and a
   new guard has no legacy to declare. The migration is filed as its own row.

4. **A `(payload-bind)` shape exists because two sites were mis-declared without it.** lease-signing
   `CreateLeaseApplication` and wellness `CreateBooking` compare the target to a payload field on a
   CREATE, where no owning link exists yet to probe — sound, but not `(ownership)` as the gate defines
   it. Left undistinguished, the next author copies `(ownership)` onto a site where the difference bites.

**What the gates do NOT claim** — *the annotation-window half of this paragraph is SUPERSEDED by §13,
which closed it; read it as the Fire-2 record, not as current behaviour.* Stated so a green run is not
over-read, and shared with the `# read-posture:` convention they mirror: an annotation covers the
following `readPostureWindow` lines, so a reference inserted *into* an annotated block inherits that
block's declaration; and both gates are fail-closed against *forgetting* to declare, not against
*mis-declaring* — only `(resource-bind)` and `(legacy-self-exempt)` carry a structural check. Tightening
the window is filed as its own row.

**Non-vacuity proven, not assumed.** Each negative was run against a temporarily-reverted mechanism and
observed to **accept** before being run green against the fix — the forged standing target (the idiom-C
bypass) and the payload substitution (the §3.4.1 bind) separately. The positive task-path vector is
asserted first, and an unforged control on the *same* auth path as the forgery pins that a rejection is
the confinement guard rather than a step-3 denial. A fixture self-test runs on every non-hook invocation
(so CI's strict run covers it with no new wiring), names the exact finding each case expects so a case
cannot pass by tripping a different rule, and pins that both gates are **blocking, not advisory** —
the one property a silent `warn: true` flip would have disarmed while everything else stayed green.

**Three-layer adversarial gate (security change ⇒ full depth).** Independent bypass and test-quality
passes ran against the built tree before admit; every finding above except (4) came from them. The bypass
pass additionally **could not** find a false-deny — no shipped client submits `RecordIdentityPII` with a
target and no task/self grant — and could not break the primitive, the rekeyed forgery vector, or the
gate on the shapes it does cover. Gates: `go build`, `make vet`, `golangci-lint` (0 issues), all four
`lint-*` scripts, and the full `go test ./...`.

## 13. The two residuals §12 filed — SHIPPED (2026-07-24)

Both of §12's "what the gates do NOT claim" residuals are closed, in one fire.

**The annotation window is gone.** All three annotation conventions — `# read-posture:`,
`# authcontext-target:`, `# workplace-exempt:` — resolved a declaration by scanning the preceding 8 raw
lines, so a declaration silently reached whatever sat below it. Coverage is now bound to the annotated
**statement**: the line the annotation trails, or the first code line beneath its comment block (a blank
line ends the block, so a detached annotation covers nothing), plus that statement's own indentation
block. `annotationSpans` computes it once per file per kind, and a nested annotation wins over an
enclosing one. What this does **not** claim: a reference inserted *inside* the annotated guard still
inherits its declaration — that is the declaration's own scope, and the one place inheritance is meant.

The tightening surfaced **12 sites whose declaration did not sit on its own call** — nine where a key
assignment separated the annotation from its read, two where a second sibling read shared one
declaration (clinic's `.tenancy` probe, clinic-reminders' `.paused` probe), one where an early-return
guard did. Every one was a legitimately-declared read whose annotation had drifted; each is fixed by
moving the declaration onto its own call or giving the second read its own, never by widening the rule.
No class was changed.

**clinic migrated, and `(legacy-self-exempt)` no longer exists.** §6 scoped clinic's `== op.actor`
exemption as a behaviour change rather than a cleanup, and it is: `workplace_exempt()` /
`require_workplace()` now key on `op.authTargetValidated`. The admitted set loses exactly one path — a
scope=any staff caller naming *its own actor key* as `authContext.target`, which satisfied the equality
and skipped workplace confinement. `TestFrontDesk_ForgedTargetCannotSkipConfinement` gained that vector
in a form only confinement can reject (the caller books a patient it genuinely **owns**, so the
`identifiedBy` probe passes), plus a same-building control proving the rejection is the workplace guard;
run against the reverted guard it is **Accepted**, so the over-grant was real, not theoretical. Clinic
mints no task for these ops, and the exemption stays backstopped at all three call sites by the
`(ownership-bound)` `identifiedBy` probe, so the task path a future `CreateTask` could open is bound too.
With no legacy left anywhere, the `(legacy-self-exempt)` shape and its file allow-list are **deleted** —
a self-equality declaration is now an unknown shape in every file, including the one that carried it.

**Live posture unchanged for every well-behaved caller.** `cmd/clinic-app` stamps `authContext.target`
only on the consumer self-service path (`asSelf`); staff submits carry none. Both keep the outcome they
had. Gates: `go build`, `make vet`, `golangci-lint` (0 issues), all four `lint-*` scripts, and the full
`go test ./...`; clinic-domain 0.26.3→0.27.0, and the three comment-moved packages patch-bumped so the
edits reach a running stack.
