# System-actor package-op grants under capability auth — design

**Status: ✅ Ratified 2026-07-03 (Andrew) — Option C, the union read.** · Designer (Winston) · 2026-07-02

**Ratification (2026-07-03, Andrew).**
- **Option C ratified as staged**: the system-actor platform path reads `cap.<actor>` ∪
  `cap.roles.<actor>` (concat `platformPermissions`, union `lanes`, both-absent → deny); the
  ordinary-actor platform path and every scoped path stay single-key. Options A/B stay rejected as
  analyzed (§7.1).
- **Designator vocabulary re-grounded at ratification DD**: the draft (and both contract hunks)
  described the system-actor set by the retired `data.protected` designator; the root-designation
  reconverge (Fork A, ratified 2026-07-02) had landed — `bootstrap.SystemActorKeys` and the anchor
  lens both designate by **`holdsRole → operator` topology** (Contract #7 §7.7). Folded throughout;
  the correction strengthens the design (routing set ≡ anchor projection set via the shared predicate).
- **Contract edits committed with this ratification**: Contract #2 §2.8 (bounded carve-out to
  one-key-per-path, core-internal; the package config-error guard unchanged) + Contract #6 §6.1 (the
  union description), both in §7.7 topology language.
- **2 fires stand as decomposed** (§8): Fire 1 the union read + unit matrix; Fire 2 stub-off e2e over
  the four engine paths + the dev-posture decision.

## What was ratified (one-look)

**What it does (two lines).** Under real capability auth (`LATTICE_AUTH_MODE=capability`), a kernel
system actor (Loom/Weaver/Bridge/objmgr/privacy) submitting an engine op — `MarkExpired`, `CreateTask`,
`DetachObject`, `RecordShredFinalization`, the Loom lifecycle ops — is **denied**: it reads the fixed
6-op `cap.<actor>` primordial anchor, which never names those package ops. The supply already exists
(each package grants the op to `operator`; every system actor holds `holdsRole→operator`; rbac-domain
*already projects* `cap.roles.<system-actor>` with the full set) — the only missing piece is that the
**reader** routes system actors to the anchor and never reads their `cap.roles` slice. The fix routes
the **system-actor platform path to read the anchor ∪ `cap.roles`** (union), so the package ops
authorize via the operator-grant idiom the packages already use, with **zero core op vocabulary** and
**no per-op core edit ever again**.

**The fork (anchor grant set vs cap.roles routing) — RESOLVED to a union read.** Widening the anchor's
literal op list (Option A) is rejected: it re-admits package vocabulary into core (undoing Epic-12) and
forces a core edit for every new engine op. Routing system actors to `cap.roles` *alone* (Option B) is
rejected: `cap.roles` grants only the `default` lane, but engine ops ride the **`system`** lane
(`DetachObject`/`RecordShredFinalization`) and admin DDL rides **`meta`** — those privileged lanes are
reserved to the anchor, so a roles-only read is lane-denied. **Recommendation (Option C): a union read
for the bounded system-actor set** (root `holdsRole → operator` topology, Contract #7 §7.7) — the
anchor is the *rbac-independent kernel floor* (privileged
lanes + the 6 bootstrap ops) and `cap.roles` is the *rbac-derived extension* (package ops → operator);
a system actor legitimately spans both, and they cannot be merged into one key without breaking the
§6.1 decomposition. Trade-off: **one extra KV GET, only for the ~5–7 fixed kernel actors, only on the
platform path** — the user hot path (ordinary actors → single `cap.roles` GET) is untouched.

**Frozen-contract change (committed with the ratification).** The union relaxes the "one-key-per-path"
invariant for exactly this one bounded path:
- **Contract #6 §6.1** — the "primordial identity → single `cap.<actor>` anchor" sentence, to describe
  the system-actor platform path as `cap.<actor>` **∪** `cap.roles.<actor>`.
- **Contract #2 §2.8** — the "one-key-per-path … never fans a single path into N reads" amendment, to
  carve out the bounded system-actor platform exception (ordinary + scoped paths stay single-key).

Both diffs are the proposal; nothing else in the design depends on wording you haven't seen.

---

## 1. Problem + intent

`make up` runs the Processor with `LATTICE_AUTH_MODE=stub` (allow-all). The **production default is
`capability`** (`cmd/processor/main.go`: "Default LATTICE_AUTH_MODE is `capability`"). So the platform
*cannot run its own orchestration under its own authorization model* — the moment the stub is removed,
every engine-submitted op is denied at commit step 3. This is a **production-readiness blocker for the
whole capability-auth plane**: the stub is a dev crutch, and today it is load-bearing.

The denied ops, and who submits them under which system actor / lane:

| Op | Submitter (system actor) | Lane | Source |
|---|---|---|---|
| `MarkExpired` | Weaver (`identity:weaver`) | temporal (`system`-class) | `internal/weaver/temporal.go` |
| `CreateTask` / `ClaimTask` / `ReAssignTask` / `CompleteTask` / `CancelTask` | Loom/Weaver (operator-equiv) | default/system | `internal/loom`, `internal/weaver` |
| `DetachObject` | object-store-manager | **`system`** | `internal/objectmanager/cascade.go:36` |
| `RecordShredFinalization` | privacy actor (`identity.system.privacy`) | **`system`** | `internal/refractor/keyshredded/manager.go:65` |

Grounded in the arch-review intake (2026-07-02) and the seven system identities seeded in
`internal/bootstrap/primordial.go`.

## 2. Grounding — what exists today (the supply is already there)

### 2.1 The anchor projects a fixed 6-op set + the 4 privileged lanes

`internal/bootstrap/lenses.go` `CapabilityLensDefinition` — the core `capability` lens — projects
`cap.<actor>` for every identity holding the primordial `operator` role via `holdsRole` (the Contract
#7 §7.7 root topology — the root-designation reconverge, ratified 2026-07-02 and landed; `data.protected`
is retired as a capability designator) with a **literal** grant set:

```
CreateMetaVertex · UpdateMetaVertex · TombstoneMetaVertex     (meta DDL)
InstallPackage   · UninstallPackage · UpgradePackage          (package lifecycle)
```

and `Lanes: ["default","meta","urgent","system"]` (the OutputDescriptor). This is the
**rbac-independent kernel floor** — it must work *before any package is installed* so a fresh kernel can
`InstallPackage` (including rbac-domain itself). That is exactly why it can never name package ops.

### 2.2 The packages already grant every engine op to `operator`

- `packages/orchestration-base/permissions.go` — `CreateTask`, `ClaimTask`, `ReAssignTask`,
  `CompleteTask`, `CancelTask`, `SetAvailability`, `MarkExpired`, the Loom lifecycle ops → `operator`.
- `packages/objects-base/permissions.go` — `AttachObject`, `DetachObject`, `TombstoneObject` → `operator`.
- `packages/privacy-base/permissions.go` — `RecordShredFinalization` → `operator`.

Each carries the same rationale in-code: *"posted by the <X> service actor, operator-equivalent
(holdsRole → operator), so it is granted to operator at scope:any — the same operator-grant idiom."*

### 2.3 rbac-domain already projects `cap.roles.<system-actor>` — and it's a permission-superset

`packages/rbac-domain/lenses.go` `capabilityRolesSpec` walks
`(identity)-[:holdsRole]->(role:role)<-[:grantedBy]-(perm:permission)` with **no `protected` filter**.
Every system actor holds `holdsRole → operator` (`primordial.go` entries 10a). The operator role is the
`grantedBy` target of **both** the core-seeded 6 meta/install permission vertices **and** every package
op above. So `cap.roles.<system-actor>` **already contains all 6 floor ops + every package op** — it is
a strict **superset of the anchor's `platformPermissions`**.

**But `cap.roles` is a `Lanes` *subset*.** Its OutputDescriptor sets `Lanes: ["default"]` (a static
baseline — "privileged lanes (meta/urgent/system) are reserved to the protected kernel actors via the
core `cap.<actor>` lens", `lenses.go:47`). So it grants the ops but not the privileged lanes.

### 2.4 The reader routes system actors to the anchor and never reads their roles slice

`internal/processor/step3_auth_matcher.go` `classAwarePlatformKey(systemActorKeys)`:

```go
return func(actor string) (string, error) {
    if _, isSystem := system[actor]; isSystem {
        return capabilityKeyFromActor(actor)      // cap.<actor>  — 6 ops, 4 lanes
    }
    return rolesKeyFromActor(actor)               // cap.roles.<actor> — pkg ops, default lane
}
```

Wired only when rbac-domain is installed (`cmd/processor/main.go` probes `IsPackageInstalled`;
`SelectAuthorizerArgs` sets `platformKeyDerivation = classAwarePlatformKey(...)` iff `RbacRolesActive`).
Lane auth reads `doc.Lanes` from **the single fetched doc**
(`step3_auth_capability.go:262`, `laneGranted(env.Lane, doc.Lanes)`).

**This is the entire gap: a routing choice, not missing grants.** The system actor's package-op grants
sit projected in `cap.roles.<system-actor>` and are never read; the anchor it *does* read carries only
the floor.

### 2.5 Didn't we already handle this? (pre-empting the mental-model question)

- *Isn't this what Epic-12's decomposition set up?* Yes — the supply side is complete: packages own
  their grants, rbac-domain projects them, the operator idiom is uniform. Epic-12 wired the
  **ordinary-actor** read to `cap.roles`. It left the **system-actor** read on the anchor (the floor),
  which was correct *for the floor ops* but silently drops the rbac-derived package ops. This design
  closes only the read-side routing.
- *Does this introduce new state?* No. No new lens, no new key, no producer change. `cap.roles.<system-actor>`
  is projected **today**. This is a reader-only change (plus the contract wording that described the
  reader).
- *Does it contradict a design-of-record?* It relaxes the "one-key-per-path" invariant (§2.8/§6.1) for
  one bounded path — that is the frozen-contract edit, called out explicitly, not smuggled.

## 3. The shape

### 3.1 Read path (P5-adjacent — this is the auth read, not an app read)

The **system-actor platform path** reads an **ordered key list** and merges the docs:

```
system actor (root topology): [ cap.<actor>  ,  cap.roles.<actor> ]   ← union (this design)
ordinary actor:             [ cap.roles.<actor> ]                     ← single GET (unchanged)
rbac-domain absent:         [ cap.<actor> ]                           ← floor only (unchanged)
```

**Merge semantics** (deny-closed by construction):

- `platformPermissions` = **concatenation** of every present doc's `platformPermissions`. The existing
  op matcher then scans the merged slice — an op is granted iff *some* slice grants it.
- `Lanes` = **union** of every present doc's `Lanes`. Because the anchor is the *only* source of the
  privileged lanes and is always present for a system actor, the privileged-lane authority is
  **unchanged**; `cap.roles` only ever adds `default`.
- **Absence handling** — a `KeyNotFound` on *one* member is treated as an **empty slice (skip)**, not a
  hard deny: a system actor with an anchor but no `cap.roles` (rbac-domain mid-install) is still validly
  the floor. If **all** members are absent → deny (`AuthDenied`) — fail-closed, mirroring §6.8.
- Provenance (`projectedFromRevisions`, the auth-trace) records both source keys.

The `task` and `service` paths are **untouched** — they stay single-key (they never involve a system
actor spanning two planes). The union is scoped to the platform path *and* only when the actor is in
`SystemActorKeys`.

### 3.2 Write path (P2)

**None.** No operations change. All grants are produced by existing lenses driven by existing links.
This design touches only the Processor's step-3 *read*.

### 3.3 Orchestration

None. No Loom pattern, no Weaver lens, no schedule. Pure reader routing.

### 3.4 Mechanism detail (the precedent mirrored)

The change lives in `internal/processor/step3_auth_matcher.go` + `step3_auth_capability.go`, mirroring
the **existing platform-entry key-derivation seam** rather than inventing a parallel path:

- `classAwarePlatformKey` returns a single key today. Generalize the platform entry to hold a
  **key-list derivation** (`func(actor string) ([]string, error)`) for the platform path only. For a
  system actor it returns `[cap.<actor>, cap.roles.<actor>]`; for an ordinary actor `[cap.roles.<actor>]`;
  the default (rbac-absent) derivation returns `[cap.<actor>]`. Every scoped path keeps its single-key
  derivation.
- The platform read loop GETs each key, parses, and folds into a merged `CapabilityDoc` (concat perms,
  union lanes, both-absent → the path's `absentKeyCode`). The **existing** lane gate + op matcher then
  run **unchanged** against the merged doc — no matcher-kind change, no new dispatch entry (so the §2.8
  overlap/one-key guard for *packages* is not tripped; the exception is core-internal to the platform
  path, not a package-contributed fan-out).

This keeps the model "path selection before the read, matcher after" intact; only the platform path's
read cardinality changes, and only for the system-actor set.

## 4. Contract surface

Two frozen edits, **committed with the ratification** (2026-07-03):

- **Contract #6 §6.1** (`docs/contracts/06-capability-kv.md`) — the decomposition paragraph's closing
  sentence ("*Step-3 preserves its single-GET hot path … each path reads exactly one disjoint key by
  actor class … primordial identity → the core `cap.<actor>` anchor, ordinary actor →
  `cap.roles.<actor>`*") is amended: the **system-actor platform path reads the anchor ∪
  `cap.roles.<actor>`** (floor + rbac-derived extension), while the ordinary-actor path and every scoped
  path stay single-key. The hot-path single-GET property is preserved *for ordinary actors* (the user
  path); the union is bounded to the fixed kernel-seeded actor set (engine/background ops).
- **Contract #2 §2.8** (`docs/contracts/02-operation-envelope.md`) — the Phase-2 "one-key-per-path
  invariant … never fans a single path into N reads" bullet gains a bounded carve-out: the
  **system-actor platform path** is the one path that reads two disjoint keys (the rbac-independent
  anchor floor + the rbac-derived `cap.roles` extension) and unions them; **package-contributed paths
  and the ordinary-actor platform path remain strictly one-key** (the overlap/config-error guard is
  unchanged). Rationale recorded: the two keys are architecturally distinct planes a system actor
  legitimately spans, not two producers of one grant type.

No document-*shape* change (§6.2–§6.14 untouched); the producer lenses are unchanged. This is a
read-routing amendment only.

## 5. Migration / compatibility

- **Backward-compatible by construction.** Ordinary actors: byte-identical behavior (single `cap.roles`
  GET). rbac-absent kernels: byte-identical (floor-only). Only the *system-actor* platform path gains a
  second read + merge — and only *adds* grants it should already have had, never removes one.
- **Deny→allow direction only.** The union can only *grant* an op that was previously denied; it can
  never newly deny (a superset of grants, a superset of lanes). No risk of breaking a currently-working
  path (all currently-working system-actor paths run under the stub today).
- **No rebuild / no reprojection.** No lens or key changes → nothing to reproject.

## 6. Test strategy

**Unit (`internal/processor`):**
1. System actor + `MarkExpired`/`DetachObject`/`RecordShredFinalization` on the `system` lane → **allow**
   (op from `cap.roles`, lane from the anchor). The core regression this fixes.
2. System actor + `InstallPackage` on the `meta` lane → **allow** (floor op + anchor lane) — proves the
   union didn't drop the floor.
3. System actor, `cap.roles` absent (rbac mid-install) → floor ops still allow, package op denies
   (graceful degradation, not a crash).
4. System actor, **both** keys absent → `AuthDenied` (fail-closed).
5. Ordinary actor → still a **single** GET to `cap.roles` (assert no anchor read), `default`-lane only.
6. Deny-closed: an op in *neither* slice → `AuthDenied`; the `system` lane for an actor whose merged
   `Lanes` lacks it → `LaneUnauthorized`.

**E2E (ephemeral stack, `LATTICE_AUTH_MODE=capability` — the real proof, Fire 2):** run the four engine
paths against the real Processor with the **stub off**:
- Weaver temporal `MarkExpired` (a freshness expiry firing);
- Loom `CreateTask` (a task-step pattern);
- objmgr GC `DetachObject` cascade (a tombstoned-owner sweep);
- privacyworker `RecordShredFinalization` (a shred finalization).
Each must commit (not `AuthDenied`). Reuse the existing `internal/leaseconvergence` / `internal/objectgc`
/ keyshredded harnesses, flipping their auth mode.

**Conformance:** the §6.1/§6.2 contract-conformance test is unaffected (no doc-shape change); add a
step-3 assertion that a system actor's merged platform grants include a package op.

## 7. Risks + alternatives

### 7.1 Alternatives considered

- **Option A — widen the anchor's literal op set** (add `MarkExpired`, `DetachObject`, … to
  `CapabilityLensDefinition`'s RETURN). **Rejected.** (1) Re-admits package op vocabulary into core's
  bootstrap cypher — the exact coupling Epic-12 spent a decomposition to remove. (2) Every new
  engine-submitted op would force a *core* edit + reseat the primordial bootstrap version. (3) It
  duplicates grants that already exist, correctly, in `cap.roles`. This is the "clever new mechanism"
  the alternatives discipline warns against when a simpler extension of existing state exists.
- **Option B — route system actors to `cap.roles` alone** (flip `classAwarePlatformKey` so system → roles,
  relying on it being a permission-superset). **Rejected — this was my first instinct and the lane
  wrinkle kills it.** `cap.roles` grants `Lanes:["default"]` only; `DetachObject`/`RecordShredFinalization`
  ride the **`system`** lane and admin DDL rides **`meta`**, so a roles-only read is `LaneUnauthorized`
  even though the op is present. Moving the privileged-lane grant into rbac-domain's descriptor (to fix
  that) would push a *core* security concern (privileged-lane grants) into a *package* — wrong owner, and
  the descriptor `Lanes` is a static baseline that can't be conditioned on the actor's root topology
  without a cypher change. The floor's lanes must stay anchor-owned; hence the union.
- **Could a variant of A or B beat the union?** A "merge on the producer side" variant — have a lens
  project a *combined* `cap.<system-actor>` carrying both floor lanes and package ops — was considered:
  it would restore single-GET. Rejected because it re-couples the rbac-independent floor to the
  rbac-derived package grants in one key: the combined doc could only be produced by a lens that walks
  the operator graph, which is *exactly* what must not run before rbac-domain exists (the floor must
  survive rbac-domain's absence). The union keeps the two planes in their correct producers and merges
  at read time — the only place a system actor's dual nature is actually resolved.

### 7.2 The single-GET-invariant relaxation (adversarial self-review — discharged inline)

This is a **security-plane** change, so I ran the fail-closed / forgery lenses against it rather than
deferring a gate:

- **Fail-closed default?** Yes. The union is a *grant* union of two independently deny-closed reads;
  omission (both absent) denies. A forgotten `cap.roles` projection degrades to the floor (fewer grants),
  never to more. There is no absence-grants-access path.
- **Can an attacker force the union to over-grant?** The union is entered **only** when the actor key is
  in `SystemActorKeys` — discovered from the graph by the **same predicate the anchor lens projects
  for**: a live `holdsRole → operator` link (Contract #7 §7.7 topology; tombstoned grants excluded —
  `internal/bootstrap/system_actors.go`). An external actor enters the set only by actually being
  granted the primordial operator role — root by definition, not an escalation — and a forged
  `protected:true` identity confers nothing (the designator is retired; the root-identity-designation
  Fork A capadv vector pins that). The Gateway strips/stamps the actor so the key is unforgeable. An
  ordinary actor never enters the union; it reads its single `cap.roles` key exactly as today.
- **Lane escalation?** No. The merged `Lanes` is `anchor.Lanes ∪ roles.Lanes`. `roles.Lanes` is the
  static `["default"]`; the only privileged-lane source is the anchor, which a non-system actor never
  reads. So no actor gains a privileged lane it couldn't already claim from an anchor it is entitled to.
- **`projectionSeq` / stale-replay resurrection?** Unchanged. The guard is per-key on the *producer*
  side (§6.2); reading two guarded keys does not weaken either's monotonic guard. A revoked package grant
  tombstones in `cap.roles` and the union simply stops seeing it (the soft-tombstone yields an empty
  slice → not merged).
- **Hot-path cost.** One extra GET, bounded to ~5–7 fixed kernel actors, on background/orchestration
  ops — never on the NFR-P3 user hot path (ordinary actors are single-GET). The invariant's *stated
  rationale* (user hot-path latency) is fully preserved; the relaxation is scoped precisely to where the
  rationale doesn't apply.

Conclusion: no default-open, no forgery, no lane escalation, no resurrection window. The adversarial pass
is **run and clean** — this design is build-ready on ratification (no deferred gate for the Steward).

### 7.3 Residual risks

- **CDC-lag window at rbac-domain install:** between rbac-domain's DDL commit and its first `cap.roles`
  projection, a system-actor *package* op would deny (floor still works). This is a transient
  *availability* dip in the safe direction (deny), self-healing on projection; engine submitters retry
  (Weaver re-fires, the GC cascade re-sweeps). Documented, not blocking. (The floor ops — Install/meta —
  are unaffected, so the install sequence itself never wedges.)
- **`RbacRolesActive` is a startup probe** (`cmd/processor/main.go`), so a running Processor decides
  routing once at boot — and `SystemActorKeys` is likewise boot-discovered: an identity granted
  `operator` *after* boot routes as ordinary until restart (its ops still authorize via `cap.roles`;
  only the privileged lanes wait — deny-safe). Acceptable: rbac-domain is installed once early; a
  long-running Processor sees a stable state. Making it dynamic is out of scope (a separate hot-reload
  concern, not this gap).

## 8. Decomposition for the Steward (2 fires, each shippable + green)

- **Fire 1 — the union read (the mechanism).** Generalize the platform-entry key derivation to a key
  *list* (system → `[anchor, roles]`, ordinary → `[roles]`, rbac-absent → `[anchor]`); add the
  merge-then-match in `step3_auth_capability.go` (concat perms, union lanes, both-absent → deny). Unit
  tests §6.1–6. Ordinary-actor path asserted unchanged (single GET). The §6.1 + §2.8 edits are
  committed. **Independently valuable:** system-actor package ops authorize under capability auth.
- **Fire 2 — prove it with the stub off (the readiness win).** Add ephemeral-stack e2e coverage running
  the four engine paths (Weaver `MarkExpired`, Loom `CreateTask`, objmgr `DetachObject`, privacyworker
  `RecordShredFinalization`) under `LATTICE_AUTH_MODE=capability`; assert each commits. Decide + document
  the dev posture: either flip `make up-full` to capability mode or add a `make up-full-capability`
  target so the platform's own orchestration is routinely exercised under real auth (retiring the stub as
  a *load-bearing* dev crutch). **Independently valuable:** the stub stops hiding a real auth gap.

No dead scaffolding — the consumers (the four engine paths) exist today; Fire 1 realizes value the moment
it lands (they authorize under capability auth), and Fire 2 proves it end-to-end.

## 9. Reconciliation with in-flight designs

- **`lane-authorization-enforcement-design.md`** (✅ ratified 2026-06-28) established `doc.Lanes` as the
  platform-path lane authority. This design is **consistent**: the merged `Lanes` keeps the anchor as
  the privileged-lane authority; no lane-auth semantics change.
- **`control-plane-capability-authz-design.md`** (✅ ratified, deprioritized behind D1) gates `ctrl.*`
  ops on the platform path. This design is **complementary** — a system actor submitting a `ctrl.*` op
  would benefit from the same union (control grants are operator-granted too). No collision; no shared
  edit.
- **`read-path-authorization-d1-design.md` / §6.14** — a *read*-path concern (`cap-read.*`), disjoint
  from this write-path step-3 change. Untouched.

No other awaiting-Andrew / designing doc touches `classAwarePlatformKey` / the platform-path routing.
