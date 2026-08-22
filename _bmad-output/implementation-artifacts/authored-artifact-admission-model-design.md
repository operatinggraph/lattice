# Three admission holes let an authored artifact reach the auth plane — a consumer-derived admission model

**Status: 🗄️ SHELVED (Andrew, 2026-08-22) — no revive trigger.** Not built. The staged Contract #6 §6.1
edit is **reverted** from the working tree. Andrew's call in the ratify session: the AI-authoring feature
that makes these holes reachable is **dormant by default** — the model-backed capabilityAuthor adapter is
registered only when an operator sets `BRIDGE_CAPABILITY_AUTHOR=real` and deploys a model-runner
(`cmd/bridge/main.go:163-176`) — so in the default build every hole here is behind a feature nobody has
turned on, nothing is blocked on the row, and it was filed by the design process, not by demand.
Deliberately **no revive trigger**: this is not queued to auto-return when the feature is enabled; if
AI-authoring is ever promoted toward on-by-default, the admission surface is re-evaluated fresh at that
point (the design below is the record to start from, not a ratified plan to resume).

> **Note on Increment 2.** Its namespace-ownership machinery is the only part needing the Contract #6 edit,
> and DD found it primarily governs the **package** plane (a package-authored lens overwriting the kernel
> anchor) — the same non-root-installer threat as the **parked** `package-authority-minting-provenance`
> design (Inc 2 of which this folded in). Both share the "no distinct victim until `consoleOperator` is
> delegated below root" gating; neither is live today.

*Original design retained below for the record — do not build it.*

**Status (original): 📐 awaiting-Andrew (ratification).** · Designer fire 2026-08-22 (Winston, unattended).
**Adversarial pass: RUN (2026-08-22), two independent lenses — security-soundness and citation/census audit.
They returned 3 blockers, 7 majors and a page of citation corrections, and they RESHAPED this design**: one of
the four increments below did not exist in the draft, one was closing the wrong set, one had its disposition
reversed, and the draft's headline "hole 2 is falsified" was over-generalized from one artifact kind to six.
Findings + dispositions: §15. Every correction was re-verified against the pinned source before folding.
**Lane:** Lattice (Stream 2). **Board row:** *[capability-author] Three admission holes let an authored
artifact reach the auth plane* (★★★ / L).
**Filed by:** [capability-proposal-bundles-design.md](capability-proposal-bundles-design.md) §4.2, with
`no-pattern: consumer-derived admission model for authored artifacts`.
**Also carries:** [package-authority-minting-provenance-design.md](package-authority-minting-provenance-design.md)
**Increment 2**'s `bucketguard` reserved-key-pattern refusal, which that design's 2026-08-21 ratification
banner explicitly folded into this row rather than carrying itself.
**Contracts:** **Contract #6 §6.1 changed** — edit staged **UNCOMMITTED** in `main`. See §8.

---

## For Andrew (ratify in one look)

**What it does, in two lines.** Three shipped guards decide what an AI-authored capability artifact may do by
enumerating their governed set from a **declaration the platform happens to keep** — a bucket-registry flag,
the artifact *kind*, the installed *package manifests* — rather than from the **consumer that acts on the
projection**. Each set is narrower than the surface it claims to govern and fail-open at its edge. This design
re-derives all three from their consumers, adds the namespace ownership the authorization plane has never had
on any of its three arms, and closes the approve→apply window that makes every record-time check advisory.

**The one frozen-contract change: Contract #6 §6.1.** Staged uncommitted; the diff is the proposal. It makes
the `cap.*` family a **closed** registry of key space → owning producer, and makes §6.14's `cap-read.<domain>.*`
family **open but claimed** (a domain is owned by the package that declares it, refused to a second claimant —
exactly as a Weaver `targetId` is). Nothing else in `docs/contracts/*` changes.

**No architectural fork.** Two judgement calls I made rather than forwarded: (a) `orchestration-history` is
closed to authored lenses although its readers are display-only — forged flow history deceives the human
reviewer who is the *entire* control in this loop, and the exclusion costs nothing; (b) a write-time key
violation **drops the offending key and keeps the lens converging**, rather than pausing the lens (§5.3.4 —
the adversarial pass reversed my draft here, and the contract I am amending records why).

**Three corrections to the row as filed, all material.**

- **Hole 2 is falsified for the `lens` kind, and only for it.** An authored *lens* provably cannot decrypt:
  it cannot declare `SecureColumns`/`Protected`, and the decryptor installs only for those
  (`cmd/refractor/main.go:1703-1712`). My draft generalized that to the whole loop. It does not hold: two of
  the six enabled kinds carry live Starlark, and a script's lazy `kv.Read` of a sensitive aspect **decrypts to
  plaintext** (`starlark_kv.go:424-441` → `sensitive_decrypt.go:244-250`), which step 6's egress guard
  deliberately permits into an ordinary domain event (`step6_validate.go:152-160`, its own comment). That is a
  real PII-egress path and **this design does not close it** — it is a confidentiality gap on a different root
  cause, it overlaps the uncommitted [sensitive-aspect-class-integrity design](sensitive-aspect-class-integrity-design.md)
  in your tree, and it files as its own ★★★ row (§14). §For-Andrew of my draft said "there is no PII-egress
  hole"; that sentence was wrong and is withdrawn.
- **A fourth hole, not in the row: apply re-runs no kind-specific validation.** The openCypher parse, the
  grant artifact's `requesterHolds` escalation check, `containsSensitiveRefLiteral`, and every
  `unknown*Fields` scope-widening defense run at *record* and *approve* only — both of which consume a
  **client-asserted verdict**. This is now **Increment 0**, and it is the cheapest and largest win here.
- **`capability-kv` holds a sixth key family the row and my draft both missed.** The `cap-read.*` read-path
  authorization family (Contract #6 §6.14) shares the bucket, is **package-extensible by ratified design**,
  and is read by a **wildcard enumeration** — so any writer landing a matching key grants a read. My draft's
  "closed five-space registry" would have refused bootstrap's own lens and every read-grant producer.

**Size and sequencing.** L, four increments, each independently shippable and green. Increment 0 is the
largest security win per line of code; Increments 2 and 3 are the posture-changing ones.

---

## 1. Problem and intent

An AI-authored capability artifact is proposed by a model, deterministically validated, approved by a human,
and applied through the ordinary `InstallPackage`/`UpgradePackage` path
([ai-authored-capabilities-design.md](ai-authored-capabilities-design.md), ratified 2026-06-29; the
model-runner and the real `capabilityAuthor` adapter shipped in NL-1, `2df02bfd`, behind
`BRIDGE_CAPABILITY_AUTHOR=real`). The security argument of the whole loop is *the AI proposes; deterministic
validation, a human gate, and the kernel's guards govern.*

Every hole here is in "deterministic validation and the kernel's guards", and they are the same
**mis-derivation** repeated:

| Hole | Guard | Derives its governed set from | The set that actually matters |
|---|---|---|---|
| **H1** | `validateLensBuckets` (`bucketguard.go:52-66`) | platform-registry rows with `LensTarget:false` — a bucket-level deny-list | every bucket that **already has a producer**, platform or not |
| **H2** | `validateLensArtifact` (`capabilitymaterializer.go:873-891`) | the artifact **kind** — only `opMeta` gets `sensitiveReadErrors` | *scoped out*: falsified for `lens`, real for the Starlark kinds, different root cause (§14) |
| **H3** | `loadProtectedDispatchSets` (`authored_dispatch_scope.go:59-144`) | installed **package manifests**' `declaredKeys` | the kernel seed **∪** the protected packages — six ops live only in the former |
| **H4** | the six §5 kind validators | *(placement, not derivation)* — they run at record and approve, never at apply | the apply path, the only one not trusting a client-asserted verdict |

---

## 2. Grounding ledger

Every load-bearing fact pinned to the code that *does* the thing. Verified against the working tree at
`e29464fd` (= `HEAD`), then re-verified by two independent reviewers; the corrections they returned are
folded in and the rows they corrected are marked ✎.

| # | Fact | Citation |
|---|---|---|
| G1 | `validateLensBuckets` is a **deny-list** over `bootstrap.ReservedBuckets()` (every `LensTarget:false` row). Three rows are `LensTarget:true`; **any unregistered name also passes**, and the nats-kv adapter auto-creates whatever it is given. | `bucketguard.go:52-66`; `platform_buckets.go:31-143`; `cmd/refractor/main.go:1066` |
| G2 | `LensArtifactContent` exposes exactly `canonicalName/adapter/bucket/table/spec` — no `IntoKey`, `ProjectionKind`, `Output`, or `Source`. So `build.go` defaults `keyField=["key"]` and the emitted key is `fmt.Sprintf("%v", …)` of whatever the cypher binds to `key`: fully author-controlled. | `capabilitymaterializer.go:117-123`; `knownLensFields` `:740-746`; `build.go:534-543`; `natskv.go:150-160` |
| G3 | An authored lens is therefore **always plain nats-kv**: postgres needs a posture it cannot declare (`:162-164`), `nats-subject` needs `SubjectPrefix`/`Stream`/`__actor` (`:103-109`), `actorAggregate` needs an `Output` descriptor. ✎ `LensArtifactContent.Table` is consequently **dead surface**, and the type's doc comment ("or postgres", `:111-113`) is wrong. | `bucketguard.go:103-109`, `:162-164`; `output.go:86-92` |
| G4 | The only existing key confinement is `OutputDescriptor.KeyPrefix()`, populated solely for `actorAggregate`. ✎ On the plain path `KeyPrefix` is `""`, and `truncateKeys` with an empty prefix **lists and purges the whole bucket** on rebuild. | `output.go:287-299`; `driver.go:621-622`; `natskv.go:662-669` |
| G5 ✎ | `capability-kv` carries **two** families. The **`cap.*` write-auth family is five spaces**: `cap.<actorSuffix>`, `cap.roles.*`, `cap.ephemeral.*`, `cap.svc.*`, `cap.role-by-operation.*`. The **`cap-read[.<domain>].*` read-auth family shares the bucket and is package-extensible by ratified design.** The `cap.role-by-operation.` literal is produced by the platform envelope, not by the lens spec. | `capabilitykv/keys.go:15-34`; `capabilityenv/envelope.go:49`; `bootstrap/lenses.go:112`, `:220`; `anchorwalk.go:488-508`; Contract #6 §6.14 (`06-capability-kv.md:530-537`) |
| G6 ✎ | Step 3 selects one dispatch path, derives that path's key **list** (one key for an ordinary actor, **two** for a kernel-seeded system actor), reads and merges, and grants what it finds. Writing a key here **is** conferring authority. | `step3_auth_capability.go:183-259`, `:588-718`; `capabilitykv/keys.go:58-79` |
| G7 ✎ | The bucket has **at least eight** readers, not three: step 3; `controlauth.CapabilityKVChecker`; step-3's denial builder; `aiagent/traversal.go:170,174` (own inline logic); `gateway/rolesanchors:110-131`; `controlauth/preflight.go`; and the `cap-read` consumers — `refractor/capabilityread` and the `projection/personal` + `grantchange` family. | as cited |
| G8 ✎ | **Seven** package lenses target `capability-kv`, not four: the four declared, plus **three generated** for `edge-manifest` by `anchorwalk.go:136`. `capabilityRoleIndex` is the one *plain* lens — and ✎ its cypher returns the **bare** `operationType`; the `cap.role-by-operation.` prefix is prepended by a core-owned envelope, so its key is **not** author-controlled. | `rbac-domain/lenses.go:64-73`, `:112`; `capabilityenv/envelope.go:49,59`; `cmd/refractor/main.go:1556`, `:2275-2277`; `anchorwalk.go:136`, `:488-508` |
| G8b ✎ | `IsAuthPlane`'s **second arm** (`postgres && GrantTable`) covers **at least nine** more lenses. The whole auth-plane population is ≥18 across both arms, and two of them are `bootstrap.LensDefinition`, not `pkgmgr.LensSpec` — pkgmgr's install-time validator never sees those. | `plan.go:113-115`; `clinic-domain/lenses.go:469,486,532,553`; `service-location/lenses.go:70`; `demo-operator/package.go:91`; `console-operator/package.go:90`; `bootstrap/lenses.go:8-12`, `:310`, `:356` |
| G9 | A Weaver gap dispatches under a **fixed** actor set once at engine construction, never per-gap, on the lane from `WEAVER_LANE` (default `system`). | `weaver/engine.go:308`; `weaver/actuator.go:82-114`; `cmd/weaver/main.go:72,89,120` |
| G10 ✎ | Weaver's identity holds `holdsRole → operator` **from the primordial seed** (entry 10a), and the core anchor lens grants that role the six meta/package ops at lanes `[default, meta, urgent, system]`. A **bootstrap** fact, independent of any package. *(Cite the link, not the identity vertex's `note` prose.)* | `primordial.go:812-837`; `bootstrap/lenses.go:124`, `:141-146` |
| G11 | `loadProtectedDispatchSets` returns an **empty, admit-everything** set when no protected package is installed, while every read/parse failure in the same function fails closed. | `authored_dispatch_scope.go:104-110` vs `:63,78,90,94,128,141` |
| G12 ✎ | The six kernel-seeded operationTypes — `CreateMetaVertex`, `UpdateMetaVertex`, `TombstoneMetaVertex`, `InstallPackage`, `UninstallPackage`, `UpgradePackage` — are declared in **no** `packages/*/ddls.go`. The only package-side mention is a `PermissionSpec` (`console-operator/permissions.go:73`), which the loader explicitly does not classify. | `primordial.go:520-522`, `:605-640`; `authored_dispatch_scope.go:117-118`; census C5 |
| G13 | Apply runs `def.validateAll()` via `preflight` — which **includes** `validateLensBuckets` and runs `ExpandReadGrantWalks()` first, so it sees generated producers. Apply runs **no** kind-specific validator. | `definition.go:30-58`; `upgrade.go:205-222`; `apply.go:132`; `capabilityapply.go:333-437` |
| G14 ✎ | A sensitive aspect written **under a correct class by a trusted script** is encrypted at step 6.5 and Core KV holds no plaintext for it. Two carve-outs: step 6.5 runs only `if Vault != nil && DDLs != nil`, and what makes that true in production is the **startup KEK refusal**; and a script may lawfully decrypt via `kv.Read` and derive the value into a **non-sensitive** aspect, which step 6.5 (keyed on the *destination* DDL) stores as plaintext. | `step65_encrypt.go:13-22`; `commit_path.go:380`; `cmd/processor/main.go:385`; `starlark_kv.go:424-441`; `sensitive_decrypt.go:244-250`; `step6_validate.go:152-160` |
| G15 | `projection.IsAuthPlane(r)` already classifies "projects an authorization surface" from the **target**, never a name list, and is installed on the general activation path including the plain arm. | `projection/plan.go:98-115`; `cmd/refractor/main.go:1721-1726`, `:2289-2291` |
| G16 ✎ | An **unclassified** adapter error falls to the `CatTransient` default and **Naks forever while health reads active**. `CatStructural` pauses the rule with no DLQ; `CatTerminal` routes the message to DLQ and the lens keeps running. The classification reaches the pause path on the nats-kv write path via `results.go:84,101` → `supervisor_adapt.go:196-201`. | `failure/classify.go:186`, `:192-193`, `:20-29`; `pipeline/results.go:84,101` |
| G17 ✎ | `weaver-targets`' `targetId` guard is **cross-package collision detection over declared ids**, not write confinement — nothing compares a lens's `OutputKeyPattern` prefix to the `targetId` it claims, at install or at write. It is the right *shape* to port, and it is weaker than my draft claimed. | `installer.go:1734-1782`; grep for `OutputKeyPattern` over `internal/pkgmgr`, `internal/weaver`, `scripts` |
| G18 | `ReadGrantDomains` uniqueness is checked **within one Definition only** — `indexReadGrantDomains` has no cross-package claim. Two packages may both declare domain `roster` and interleave under `cap-read.roster.*`, which the wildcard reader admits. | `anchorwalk.go:144-158`, `:182-206` |
| G19 | The postgres arm has the same shape of hole: `GrantWriterAdapter.checkSource` **skips entirely** when the lens declared no `GrantSource`, so such a lens may write **any** `grant_source`, including another producer's. `GrantSource` is required only when `DiffRetraction` is set. | `read_path_adapters.go:19-26`, `:94-105`; `bucketguard.go:168-178` |
| G20 | `CapabilityApplyPlanForProposal` has exactly two callers, each immediately followed by `ApplyCapabilityPlan`, and a lint gate blocks routing a capability plan's Definition to `Installer.Apply`/`Upgrade`. **Apply is a genuine choke point** — no bypass found. | `cmd/lattice-pkg/main.go:553`; `cmd/loupe/review.go:735`; `scripts/lint-conventions.go:1144` |

**One comment corrected in passing.** `authored_dispatch_scope.go:36-38` claims the protected set is *"DERIVED
from the live catalog on every apply — never a hand-maintained list."* The derivation is from package
**manifests**, and G12 is exactly the population that difference loses. Increment 3 makes the sentence true.

---

## 3. The root cause, stated once

> Each guard enumerates its governed set from a **declaration source** — a registry flag, an artifact kind, a
> manifest — rather than from the **consumer** whose decision the projection feeds. A declaration source is a
> *proxy* for the consumer's input surface, and a proxy is narrower than the thing it stands for exactly where
> nobody looked.

The corrective principle, and the `no-pattern:` the row asked for:

> **Derive the governed set from the projection inputs of the consumers that decide the outcome, and make the
> derivation fail closed on an empty or unreadable source.**

The adversarial pass demonstrated the principle on the design itself: my draft's Increment 1 re-derived the
authored-lens bucket set from the **platform registry** — another declaration source — and was therefore
blind to every package- and app-owned bucket, which is where the `truncateKeys` wipe hazard (G4) lives.
Increment 1 below derives it from *"does this bucket already have a producer?"* instead.

---

## 4. Reconciliation with the existing mental model

*Didn't we already handle this?* Partly, in three places, each one step short.
`enforceAuthoredWeaverTargetScope` (NL-1) is the dispatch-side containment and the right shape — it derives
its set from the wrong source (H3). `checkWeaverTargetIDCollision` treats a bucket prefix as a package-claimed
namespace — but only as collision detection, never as write confinement (G17), so it is a precedent for the
*claim*, not for the enforcement. And `projection.IsAuthPlane` already answers "is this the auth plane?" from
the target rather than a name list — consulted at Refractor activation and **never at admission**.

*Does this duplicate or contradict an established pattern?* No — it removes an asymmetry. After Increment 2
all three arms of the authorization plane (`cap.*` KV, `cap-read.*` KV, `actor_read_grants` Postgres) have the
same ownership rule, instead of one being pattern-checked, one being checked within a single Definition, and
one being optional.

*Does this introduce new state?* One core-owned compile-time registry for the `cap.*` family (not new
*information* — it is §6.1's list, which today exists only as prose), and one install-recorded claim per
read-grant domain (mirroring the `targetId` claim that already exists). Notably, **no new `LensSpec` field**:
the key space is *derived* from what each arm already declares (§5.3.2). That is the single largest
simplification the adversarial pass produced, and it is what removes the migration, the version bumps, and the
retroactivity hazard my draft carried.

---

## 5. The shape

### 5.1 Increment 0 — apply re-runs the kind-specific validation (closes H4)

`CapabilityApplyPlanForProposal` calls `DefinitionForCapabilityArtifact` — a pure unmarshal-and-shape switch —
and then only `enforceAuthoredWeaverTargetScope`. It never calls `ValidateCapabilityArtifact` (G13). Both
earlier gates consume a **client-asserted verdict** (`ddls.go:796-802` type-checks the verdict; it does not
recompute it). So today the following are advisory against the actor who supplies the verdict:

- **`validateGrantArtifact`'s `requesterHolds` check** (`capabilitymaterializer.go:833-838`) — whose own
  comment calls it *"the property that makes this kind safe to enable at all… the exact privilege-escalation
  path §5 exists to close."*
- **`containsSensitiveRefLiteral`** (`:323`) — placed ahead of the kind switch precisely *"so it can never be
  missed by a future kind that forgets to call it."* Missed by apply.
- **Every `unknown*Fields` scope-widening defense** — at apply, `json.Unmarshal` silently drops the smuggled
  key, the exact outcome those checks exist to prevent.
- The openCypher parse and the opMeta sensitive-read check.

**The fix is to call the function that already exists**, from the apply path, with live inputs: the
requester's `HeldPermission` set and a live `SensitiveAspectResolver`. Both callers already construct both
(`cmd/loupe/review.go:519-538`, `:735`; `cmd/lattice-pkg/main.go:553`), so this is plumbing an existing
verdict computation to the one place that does not trust its caller — not new machinery.

Revalidating at apply also closes the **catalog-drift window** the reviewers surfaced: between approve and
apply there is no time bound, and an aspect's `sensitive` flag can change in between. The verdict that binds
should be computed against the catalog the artifact actually lands on.

*Why this is Increment 0 and not a follow-on:* it is the smallest change here and the one that makes every
other refusal in this design — and every refusal already shipped — actually bind.

### 5.2 Increment 1 — an authored lens may only target a bucket that has no other producer (closes H1)

**The derivation my draft got wrong.** The draft added a "does a platform consumer decide from this?" field to
the platform-bucket registry. That registry's own doc says *"a bucket absent here does not exist: it is never
provisioned, never guarded, never granted"* — but a **lens-declared** bucket is auto-created by the adapter
(`cmd/refractor/main.go:1066`), so every package- and app-owned read-model bucket is permanently absent from
it. The draft would have left them all open, and G4 makes that severe: an authored plain lens has an empty
`KeyPrefix`, so `truncateKeys` **purges the whole bucket** on rebuild — an authored lens could not merely
forge rows in a vertical app's read model, it could **wipe** the legitimate producer's.

**The consumer-derived rule.** An authored artifact's lens may target only a bucket that **no already-installed
lens writes, and that is not a platform bucket**. The set is enumerated at apply from the live lens catalog —
the meta roots of class `meta.lens` and their `.spec` aspects' `targetConfig.bucket` — which is the *same*
Core-KV scan `loadProtectedDispatchSets` already performs on this path, so it costs one extra field read on an
already-required pass. Platform buckets come from the existing registry as the second disjunct, covering
`LensTarget:false` rows an authored lens must also never name.

This closes, by construction and without an enumeration to maintain: `capability-kv`, `weaver-targets`,
`orchestration-history`, `my-tasks` (a **package-owned** bucket carrying the guarded `my-tasks.<actor>` key
that is in no platform registry — the case that proved the draft's derivation wrong), every vertical app's
read-model bucket, and every bucket that has not been invented yet.

**Enforcement point: apply**, beside `enforceAuthoredWeaverTargetScope`, whose doc comment already states why
that is the only non-bypassable point (G13, G20). Also run at record and approve for early console feedback;
the apply call is the one that binds.

**`orchestration-history` is included, and that is a judgement call.** Its readers are display-only (Loupe's
Flows tab and history timeline — verified, no authorization or dispatch decision reads it), so by the strict
letter of "has a producer" it is caught anyway; I note it because a reader may expect a carve-out. There is
none, and the reason is that the human reviewer *is* the control in this loop, so a surface that manufactures
the evidence they read is worth closing at zero cost.

**What the cost census can and cannot say.** Census C1 enumerates every `LensArtifactContent` in the tree —
**twelve** fixtures, all naming bespoke buckets or existing-refusal cases, zero legitimate shared-bucket uses.
That is a statement about the **fixture corpus**, not about model behaviour: there are no real AI-authored
artifacts in the repo. The claim this design makes is the narrower, honest one — *no shipped or tested
artifact is refused* — plus the structural argument that an authored lens is a business read model and a fresh
bucket is what it wants.

### 5.3 Increment 2 — namespace ownership on all three arms of the authorization plane

This is the increment that also discharges the authority-minting design's Increment 2. It governs the
**package** plane, not only the authored one, because that is where the live defect is: a package lens
declaring the literal core anchor pattern `cap.{actorSuffix}` installs cleanly today and would overwrite the
kernel's own root-grant projection for every system actor.

#### 5.3.1 Three arms, one missing primitive

`IsAuthPlane` is true for two targets, and the KV target carries two key families. All three have the same
gap, and all three already have somewhere the owner *could* be read from:

| Arm | Where the namespace is already declared | Confinement today |
|---|---|---|
| `cap.*` — KV write-auth | `OutputKeyPattern`'s literal prefix (`actorAggregate`); a core-owned envelope constant (`capabilityRoleIndex`) | **none** — `validateKeyPattern` checks the placeholder *vocabulary*, never the literal prefix, so `cap.{actorSuffix}` passes |
| `cap-read[.<domain>].*` — KV read-auth | `ReadGrantDomains[].Name`, from which the producer lens is generated | **within-Definition uniqueness only** (G18) — no cross-package claim |
| `actor_read_grants` — Postgres read-auth | `GrantSource` | **optional**; empty ⇒ `checkSource` skips ⇒ any source writable (G19) |

The missing primitive is one thing: **an owned namespace claim on the authorization plane.**

#### 5.3.2 Derive the namespace; do not add a field

My draft added a required `LensSpec.KeySpace`. The reviewers killed it on three counts and they are right: it
would be **inert** on the postgres arm (which writes rows, not keys, so the conjunct could never fire); it
could not reach bootstrap's two auth-plane lenses at all (they are `bootstrap.LensDefinition`, not
`pkgmgr.LensSpec`); and with the real population at ≥18 (G8, G8b) the "four one-line declarations, zero debt"
migration was fiction.

**Every arm already declares its namespace.** So:

- **`cap.*`** — the namespace is `OutputKeyPattern`'s literal prefix, or, for the operation-role-index shape,
  the core envelope's constant. Admission resolves that prefix against a compile-time registry in
  **`internal/capabilitykv`** — the consumer's own package, beside the derivations that read these keys:
  `cap.` → core · `cap.roles.` → rbac-domain · `cap.role-by-operation.` → rbac-domain *(prefix produced by the
  platform envelope, not the package)* · `cap.ephemeral.` → orchestration-base · `cap.svc.` → service-location.
  Longest-prefix resolution, so the bare `cap.` anchor does not swallow the four decomposed spaces.
- **`cap-read.<domain>.`** — the namespace is the declared domain. It is **claimed at install**, cross-package,
  exactly as a Weaver `targetId` is (G17's shape, with the write confinement G17 lacks). The bare
  `cap-read.<actor>` base space is core's. This keeps §6.14's package-extensibility intact, which the draft's
  closed registry destroyed.
- **`actor_read_grants`** — the namespace is `GrantSource`. Make it **required** on every `GrantTable` lens,
  not only when `DiffRetraction` is set, and delete `checkSource`'s empty-source skip (G19). This is the
  "an empty value that means something is probably fail-open" reflex firing on a real field.

Net: **no new declaration anywhere**, no package version bumps for the KV arm, and no retroactivity hazard —
an already-installed lens's namespace is derivable from the spec Core KV already holds.

#### 5.3.3 The shared auth-plane predicate, and the import constraint

Admission and activation must never disagree about which plane a lens is on. `projection.IsAuthPlane` takes
`*lens.Rule`, and a shared package naming that type would import `internal/refractor/lens` →
`ruleengine/full`, whose **in-package test files import `internal/pkgmgr`** — a genuine import cycle, and the
same constraint `bucketguard.go:11-16`'s `personalActorKeyField` mirror already records.

So the shared artifact is a pure predicate over **normalized primitives** —
`IsAuthPlane(target, bucket string, grantTable bool) bool` — in a leaf package, with one thin adapter per
side. The normalization is where the bug would live, not the body, so the test must cover it explicitly:
Refractor's input is `"nats_kv"` (underscore); pkgmgr's `LensSpec.Adapter` is `"nats-kv"` **or empty**
(`build.go:534` maps both); and bootstrap's own auth-plane lenses declare the **alias** `"capability"`,
translated only at `primordial.go:1173-1176`. A test over the four `packages/` lenses proves none of this —
it must include an empty-`Adapter` case and an alias case.

#### 5.3.4 Write-time enforcement, and its disposition — reversed from the draft

An install-time check cannot be sufficient for the `cap.*` arm: an `actorAggregate` key is rendered from a
template at projection time, and a plain lens's key is computed by the cypher (G2). So the adapter compares
the rendered key against the lens's resolved namespace before the write, for auth-plane lenses only.

**My draft made this a `CatStructural` pause and justified it with "absence is denial." That was wrong, and
the contract I am amending records why.** Contract #6 §6.14 documents the aggregate-roster incident verbatim:
an oversized document *"permanently froze that actor's grant set — revocations stopped landing, **fail-OPEN**."*
Absence is denial for a key never written; a **paused** lens leaves every previously written `cap.*` key in
place and step 3 grants whatever it finds. Since the key is rendered from vertex data, an out-of-space key
would be a **data-triggerable freeze of the authorization surface that preserves the attacker's existing
grants**.

**The disposition is therefore `CatTerminal` on the offending key only** — DLQ that message, alert at
auth-plane severity, and let the lens keep converging. The torn-surface argument I made applies to a lens
whose *legitimate* keys fail; it does not apply to one key that should never have been written, and dropping
it is precisely the outcome the guard wants. Leaving the error unclassified is the one unacceptable option:
it falls to `CatTransient` and **Naks forever while health reads active** (G16).

### 5.4 Increment 3 — the protected dispatch set derived from the kernel seed ∪ the protected packages

**The derivation gains a disjunct rather than changing source.** My draft said `loadProtectedDispatchSets`
*"stops reading package manifests"*; it cannot — the rule is *kernel-seeded **or** declared by a
platform-protected package*, and only manifests answer the second. The correct statement is that the function
gains the first disjunct, which it never had.

**The discriminator, named** (my draft left it unstated, which made §11's "preserve the fail-closed posture"
unenforceable): kernel-seeded is decided by **`bootstrap.PrimordialVertexKeys()`** — the exported kernel key
enumeration `verify-kernel` already uses as the source of truth. Not `data.protected`: that marker is
explicitly *"retired as a capability designator (anti-brick only, Fork A 2026-07-02)"*
(`primordial.go:687-688`), and re-purposing a retired designator for a security decision is how the next
reader inherits a wrong model. `pkgmgr` already imports `bootstrap`, so no new dependency. The six ops of G12
are then covered by construction, and the doc comment at `:36-38` becomes true.

**The empty-set fail-open becomes fail-closed, and its stated rationale is falsified.** The branch admits
everything when no protected package is installed, reasoning there is *"no operator role to escalate into"*.
G10 refutes it directly: the Weaver's `holdsRole → operator` edge and the anchor lens's six-op grant at the
`system` lane are **primordial**, present on a stack that has never run `install-packages`. So on exactly the
deployment the branch exists to accommodate, an authored `weaverTarget` whose gap dispatches `UpdateMetaVertex`
is admitted and dispatched under the Weaver's operator authority. It becomes a refusal naming
`make install-packages` as the remedy — consistent with every other failure path in the same function.

**Scope unchanged:** this does not widen to package-authored targets, which legitimately dispatch platform
operations. That is why the check lives in `authored_dispatch_scope.go` and not in the shared
`orchestrationguard`.

### 5.5 Hole 2 — falsified for the `lens` kind, re-opened for the Starlark kinds, and scoped out

**Falsified for `lens`, and pinned.** An authored lens cannot declare `SecureColumns`/`Protected` (G3), the
decryptor installs only for those (`cmd/refractor/main.go:1703-1712`), so it projects ciphertext. That
guarantee is held **by the shape of a type**, so it is made mechanism-dependent by pinning tests on
`LensArtifactContent`'s field set and the `knownLensFields` allow-list (`capabilitymaterializer.go:740-746` —
note `unknownLensFields` at `:753` is the *function*; the allow-list is `knownLensFields`), with failure
messages naming this section. The pin should also record that `Table` is now dead surface (G3) rather than
freezing an inert field silently.

**Not falsified for the Starlark-bearing kinds, and NOT closed here.** Two of the six enabled kinds carry a
live script. `validateVertexTypeDDLArtifact` runs required-fields plus a **purity** sandbox check and no
sensitivity check at all; and a script's lazy `kv.Read` of a sensitive aspect **decrypts to plaintext**
(`starlark_kv.go:424-441` → `sensitive_decrypt.go:244-250`), which step 6's egress guard deliberately permits
into an ordinary domain event — its own comment: *"an op emitting no `external.*` event may still decrypt and
derive a value into an ordinary domain event, today's DDL-trust surface, unchanged"*
(`step6_validate.go:152-160`). Step 6.5 keys encryption on the **destination** DDL, so the derived value lands
as plaintext, and an authored lens then projects it.

This is a real PII-egress path inside the authored loop. It is **out of scope here** on root cause, not on
convenience: this design is about *authorization*-plane namespace ownership, and that path is a
*confidentiality* failure in script trust — the same seam the uncommitted
[sensitive-aspect-class-integrity design](sensitive-aspect-class-integrity-design.md) in `main` is addressing
from the class-integrity side. It files as its own ★★★ row naming that design as partial cover (§14). What
matters for ratification is that **the row's hole 2 is not closed by this design and must not be read as
closed.**

**Overlap with the bundles design, called out.**
[capability-proposal-bundles-design.md](capability-proposal-bundles-design.md) §4.3 pins **three** things —
`LensArtifactContent`'s field set, `knownLensFields`, and `VertexTypeDDLArtifactContent`'s field set. This
design's pin is a strict **subset** (the first two), so it is not a clean either/or: **whichever lands first
ships its own pin, and the later one extends rather than replaces it.** The two designs also share seams in
`authored_dispatch_scope.go` and `capabilityapply.go`; both are `📐 awaiting-Andrew` and the second to build
should re-derive its Phase-0 touch-list against merged `main`.

---

## 6. State-lifetime table

| State | Created | Reset | Carried across | Ordered against | Notes |
|---|---|---|---|---|---|
| `cap.*` key-space registry (`internal/capabilitykv`) | compile-time constant table | n/a | n/a | n/a | Deliberately **not** runtime state: core-owned policy cannot be drifted by an install, a replay, or a half-applied upgrade |
| read-grant **domain claim** (per `<domain>`) | at install, from `ReadGrantDomains[].Name` | released when the owning package uninstalls | crash/replay: it is derived from the installed manifest, so it is whatever Core KV holds | the manifest's own CDC ordering — no separate token | Mirrors the `targetId` claim (`installer.go:1734-1782`), including its self-exclusion-by-version-independent-key rule for re-install/upgrade |
| `GrantSource` (per grant-table lens) | at package build | never — a change is a package upgrade | part of the lens `.spec` | the lens spec's own ordering | Becomes **required**; the empty case that today disables `checkSource` ceases to exist |
| *(no new runtime state)* | — | — | — | — | The write-time comparison is a pure function of the rendered key and the activated lens's resolved namespace; nothing accumulates across rows or evaluations |

**The one coupling worth stating:** adding a `cap.*` space requires a platform release, because the registry is
compile-time. That is deliberate — a registry a package could extend is forgeable by the actor it governs —
and it is bounded, because a new `cap.*` grant type is a Contract #6 §6.1 amendment anyway. It does **not**
apply to `cap-read.<domain>.`, which stays package-extensible per §6.14; that asymmetry is the whole point of
the two-tier shape.

---

## 7. Executable censuses

Every count this design relies on ships as the command that derives it. **Three of my draft's censuses were
constructed so they could not falsify their own claim** — C2's regex `cap\.` structurally cannot match
`cap-read.`, and C3's grep was scoped to `packages/` so it could not see the generated producers in
`internal/pkgmgr`. Both are corrected below; that failure is the reason C4 exists as a test rather than a
command.

| # | Claim | Command | Expected |
|---|---|---|---|
| C1 | No shipped or tested authored artifact targets a bucket with an existing producer | enumerate every `LensArtifactContent` literal tree-wide (**including `packages/**`** — the draft's list missed three there) and inspect each `Bucket` | **12** literals; all bespoke or existing-refusal fixtures; 0 legitimate collisions. *Corpus claim only — there are no real authored artifacts in the repo.* |
| C2 ✎ | The `capability-kv` key families are exactly `cap.*` (five spaces) and `cap-read[.<domain>].*` | `grep -rnE 'cap[.-][a-z-]+\.'` over `internal packages cmd docs`, **excluding `_test.go` and `docs/decisions/`** | five `cap.*` spaces + the `cap-read` family. The draft's `cap\.` regex returned three spurious test/doc-only prefixes (`cap.ext.`, `cap.system.`, `cap.other.`) and could not see `cap-read.` at all |
| C3 ✎ | The auth-plane lens population | `grep -rn '"capability-kv"'` **tree-wide** (not just `packages/`), plus every `GrantTable: true`, plus `bootstrap.LensDefinition` auth-plane rows | **≥18**: 7 package `capability-kv` lenses (4 declared + 3 generated for `edge-manifest`), 2 bootstrap KV lenses, ≥9 postgres grant-table lenses |
| C4 | Every installed auth-plane lens's emitted keys stay inside its resolved namespace | *(a test over live specs, not a command)* | 0 violations — **this is the pin that turns naming discipline into a checked invariant, and it is the test that would have caught this design's own C2/C3 error** |
| C5 | The six kernel-seeded ops are declared by no package manifest | `grep -rnE '(Create\|Update\|Tombstone)MetaVertex\|(Install\|Uninstall\|Upgrade)Package' packages/*/ddls.go packages/*/permissions.go` | zero DDL declarations; two prose comments in `capability-author/ddls.go`; one `PermissionSpec` in `console-operator/permissions.go:73` which the loader does not classify |
| C6 | After Increment 3 the protected set is a **superset** of today's | run both derivations over a seeded stack and diff | new ⊇ old, and new \ old ⊇ the six ops of G12 |
| C7 | No two installed packages claim the same read-grant domain | enumerate `ReadGrantDomains[].Name` across all installed manifests | 0 collisions today — the pin behind Increment 2's new claim |

C4, C6 and C7 gate correctness and are owned as **tests** by Increments 2, 3 and 2 respectively (§12).

---

## 8. Contract surface

**Contract #6 §6.1 — CHANGED. Edit staged uncommitted in `main`; the diff is the proposal.**

- **What:** a namespace-ownership clause. The **`cap.*` family is closed** — five spaces, each registered to
  one producer. The **`cap-read[.<domain>].*` family stays open and package-extensible per §6.14**, with the
  domain *claimed* at install and refused to a second claimant. Both directions fail closed; the write-time
  refusal is scoped to the offending key, with the fail-open reasoning of §5.3.4 stated inline. Authored
  artifacts may not target the bucket at all.
- **My first draft of this edit said "the key-space list above is CLOSED" full stop, and that contradicted
  §6.14 of the same file** — which ratified (2026-06-27) that *"each package projects its own domain's
  readable anchors"* into the same bucket. The adversarial pass caught it. The staged edit is the corrected
  two-tier version; an internally contradictory contract is not something you can ratify, and I would rather
  record the near-miss than quietly fix it.
- **Affected consumers:** none breaks. The five `cap.*` spaces register as-is with their current owners; the
  `cap-read` producers — bootstrap's base lens, and the generated per-domain producers — keep working under
  the domain claim; the postgres arm's `GrantSource` becomes required, which census C3 sizes and §9 migrates.

**Everything else is built to, not changed.** Contract #8 is untouched (no new op, no new lifecycle verb).
Contract #10 §10.8 is untouched — Increment 3 changes only how the *protected* set is derived. `internal/natsperm`
is not involved: nothing here introduces a `$JS.API.*` call, so the permission-envelope check has nothing to
report.

---

## 9. Migration and compatibility

- **Increment 0** changes no data and adds no field. Its compatibility question is the honest one: *does
  re-validating at apply refuse an artifact that record/approve admitted?* It can, and that is the point —
  when it does, the artifact was admitted on a verdict the apply-time catalog no longer supports. The
  refusal message must say which check failed and that re-proposal is the remedy, because a `review` state
  has no route from `approved` back.
- **Increment 1** is a pure narrowing; census C1 shows an empty refusal set over the tested corpus.
- **Increment 2** adds **no `LensSpec` field** (§5.3.2), so the KV arms need no package edits and no version
  bumps — the namespace is derived from specs already installed. The one real migration is the **postgres
  arm**: `GrantSource` becomes required on every `GrantTable` lens. Census C3 sizes that at ≥9 lenses across
  `clinic-domain`, `service-location`, `demo-operator`, `console-operator` and bootstrap; those package edits
  need version bumps and `verify-package-*`. The gate ships **blocking** because the migration lands in the
  same increment and leaves zero debt — but that claim now rests on a census that has been run, not on the
  draft's "four one-line declarations".
- **Increment 3** widens a refusal set; C6's diff answers the compatibility question mechanically. The
  operator-visible change is that applying an authored `weaverTarget` on a stack with no protected package
  now refuses instead of admitting.
- **Adoption cost:** no stack wipe. No kernel key count changes, no bootstrap version gate moves.

---

## 10. Alternatives considered

**A. Confine the authored lens's key by a required prefix rather than refusing the bucket (Inc 1).** Not
rejected outright — it *is* Increment 2, on the package plane where a legitimate producer exists. For the
authored plane it buys nothing: no authored artifact wants a shared bucket, so a prefix scheme would serve
zero demand, and it would have to be runtime-enforced where a refusal is static and total. **Refusing is both
smaller and stronger.**

**B. Require `actorAggregate` for any auth-plane lens and lean on `OutputKeyPattern`.** My first shape; G8
kills it — `capabilityRoleIndex` is a shipped plain lens on `capability-kv`, so the rule would refuse live
rbac-domain. It also would not have helped: `validateKeyPattern` checks the placeholder vocabulary, not the
literal prefix, so `cap.{actorSuffix}` passes it today. Recorded because it *looks* like maximal reuse of
existing machinery and is in fact a refusal of a shipped lens plus a guard that does not guard.

**C. Install-time reserved-pattern deny-list only (the authority-minting design's Inc 2 as drafted).**
Insufficient rather than wrong: it catches a declared colliding literal, which is all a static check can see,
and cannot see a rendered key at all. Increment 2 keeps its install-time half and adds the write-time half it
lacked; its fixture test (fires on `cap.{actorSuffix}`, does not fire on `cap.roles.{actorSuffix}`) is
inherited verbatim. *Could a variant beat the recommendation?* Only if the auth plane had no plain-lens and no
rendered-key producers — C3 shows it has both.

**D. Rewrite the consumers to validate key shape on read, instead of confining the write.** The demand-side
fix, mandatory to price whenever the consumer census is single-digit. My draft rejected it on the ground that
*"readers derive the key they read and never see a foreign key"* — **that ground is wrong**, and the
correction strengthens the conclusion rather than weakening it. It holds for the `cap.*` readers, which
derive-then-`KVGet`. It is false for the read-auth arm: `capabilityread` answers *"may this actor read this
anchor?"* with a **wildcard listing** (`cap-read.*.<actorSuffix>.<anchorId>` → `ListKeysFilter`,
`capabilityread.go:61,108`), so any writer landing a matching key grants the read. And the census is not
single-digit: at least eight readers (G7), one of them re-implementing the read inline. Confinement at the
one place all of them share is the right answer — and the wildcard reader is the reason it must be at the
**write**, since that reader has no way to distinguish a forged key from a legitimate one.

**E. Treat a stack with no protected packages as out of scope (leave H3's empty-set branch).** The branch's own
comment argues this; G10 falsifies the argument. Rejected on facts.

**F. Build a sensitivity gate on the authored lens's cypher (H2 as filed).** Rejected for the `lens` kind
because the premise is false (§5.5). Not rejected for the Starlark kinds — there it is real and filed (§14).

**G. Add a required `LensSpec.KeySpace` field (my draft's Increment 2).** Rejected by the adversarial pass and
by re-derivation: inert on the postgres arm, unreachable for bootstrap's two `LensDefinition` lenses, and a
≥18-lens migration with a retroactivity hazard (an installed lens with no declaration meets a write-time check
with nothing to compare). Deriving the namespace from what each arm already declares delivers the same
guarantee with no field, no migration on the KV arms, and no retroactivity.

---

## 11. Risks

- **The normalized auth-plane predicate (§5.3.3)** is the one place admission and activation could silently
  diverge, and the divergence lives in normalization, not the body. The test must include an empty-`Adapter`
  case and a `"capability"`-alias case; a test over the four `packages/` lenses proves nothing.
- **Increment 0 could refuse a legitimate approved artifact** if the catalog drifted between approve and
  apply. That is correct behaviour, but the failure mode is user-visible on a path with no route back from
  `approved` — hence the message requirement in §9.
- **The write-time comparison is on the hot projection path**, but it is one `strings.HasPrefix` for
  auth-plane lenses only. The increment measures rather than asserts this, per the quantify-with-a-bound rule.
- **Increment 3 reads more keys**, once per authored apply — a human-gated path. The existing fail-closed
  posture on a torn read must survive the rewrite verbatim (G11's *other* branches are correct).
- **Registry/corpus drift** — a sixth `cap.*` space added in code without a §6.1 edit, or vice versa.
  Census C2 ships as a test.
- **Domain-claim release on uninstall** (§6) is the one lifetime with a real edge: if a claim outlives its
  package, a legitimate re-install is refused. It mirrors the `targetId` guard's self-exclusion rule, which
  is the precedent to copy — including reading that guard's own doc comment before copying it.

---

## 12. Test strategy

Every test is **owned by a named increment**.

**Increment 0** — apply refuses an artifact whose grant names an op the requester does not hold (the
`requesterHolds` escalation, on the apply path, which no test covers today); apply refuses a `$sensitiveRef`
literal; apply refuses an artifact whose smuggled unknown field record-time caught; **a positive vector
first** — a legitimate approved artifact still applies unchanged; a mutation test inverting the re-validation
call proves the escalation lands.

**Increment 1** — an authored lens naming a bucket with an existing producer is refused, one case per class
(platform `capability-kv`; package-owned `my-tasks`; a vertical app read-model bucket); a bespoke bucket is
admitted; census C1 as a test; mutation test on the predicate.

**Increment 2** — the inherited fixture (refusal fires on `cap.{actorSuffix}`, not on `cap.roles.{actorSuffix}`);
a lens whose resolved namespace is unregistered is refused; a second package claiming a live read-grant domain
is refused (**census C7** as the pin); a `GrantTable` lens with no `GrantSource` is refused; the adapter
refuses an out-of-space rendered key **and classifies it `CatTerminal`** — the classification is asserted, not
just the error, because an unclassified error Naks forever (G16); the normalized predicate agrees across both
sides including the empty-`Adapter` and alias cases; **census C4 as the pinning test** over all ≥18 auth-plane
lenses across both arms.

**Increment 3** — each of the six kernel-seeded ops is in the protected set and a gap dispatching
`UpdateMetaVertex` is refused; the empty-catalog case **refuses** with a remedy-naming message; census C6 as a
test; **a mutation test restoring the fail-open** proves the escalation lands; E2E on an ephemeral stack —
apply an authored `weaverTarget` naming `UpdateMetaVertex`, assert the live refusal, then assert a legitimate
authored target still applies and converges.

**All increments:** `go build ./...`, `make vet`, `golangci-lint run ./...`, `make verify-kernel`, every
`scripts/lint-*.go` gate, and `verify-package-*` for the packages Increment 2 touches.

---

## 13. Decomposition for the Steward

| Inc | Scope | Independently shippable? | Posture-changing? |
|---|---|---|---|
| **0** | Re-run `ValidateCapabilityArtifact` at apply with live `HeldPermission` + `SensitiveAspectResolver`; refusal messaging | Yes | No — it makes existing checks bind; smallest change, largest security win |
| **1** | Bucket-has-a-producer derivation from the live lens catalog + platform registry; authored-artifact refusal at apply (+ record/approve for feedback); census C1 | Yes | No — pure narrowing |
| **2** | The `cap.*` registry in `internal/capabilitykv`; read-grant **domain claim** at install; `GrantSource` required + `checkSource` empty-skip removed; normalized auth-plane predicate; install-time resolution; **write-time adapter refusal + `CatTerminal`**; postgres-arm package migration + version bumps; censuses C2, C3, C4, C7 | Yes | **Yes** — new enforcement on the live projection path for all three auth-plane arms |
| **3** | `loadProtectedDispatchSets` gains the kernel-seed disjunct via `bootstrap.PrimordialVertexKeys()`; empty-set fail-open → fail-closed; doc-comment correction; censuses C5, C6 | Yes | **Yes** — closes a live escalation, flips an admit branch to refuse |

Review depth is the Steward's sizing (`agents/steward/SKILL.md` §4); Increments 2 and 3 are named
posture-changing, 0 and 1 are not.

**Sequencing.** 0 → 1 → 3 → 2 is my recommendation, and it is a recommendation rather than a constraint: 0 is
the cheapest and makes everything else bind; 3 closes the largest live escalation; 2 is the biggest build and
carries the only package migration. **Increment 2 must not be split** — its install-time half without the
write-time half is alternative C's rejected shape.

**Dead-scaffolding check.** Each increment realizes value on landing, against a shipped, env-enabled authoring
path (NL-1, `2df02bfd`): 0 and 1 and 3 refuse reachable escalations; 2 closes the package-plane
anchor-overwrite defect and the two read-auth ownership gaps independently of the authored plane. Nothing here
waits on a consumer that does not exist.

---

## 14. Residuals filed, not resolved here

Consolidated at filing — **two** rows, each naming a shared missing primitive rather than one row per symptom.

1. **★★★ — An AI-authored Starlark artifact can launder sensitive plaintext into a non-sensitive aspect.**
   The undeclared `kv.Read` seam decrypts (`starlark_kv.go:424-441` → `sensitive_decrypt.go:244-250`), step 6's
   egress guard deliberately permits derivation into an ordinary domain event (`step6_validate.go:152-160`),
   and step 6.5 keys encryption on the destination DDL — so the derived value is stored as plaintext and an
   authored lens projects it. `validateVertexTypeDDLArtifact` applies a purity check and no sensitivity check.
   Partial cover: the uncommitted [sensitive-aspect-class-integrity design](sensitive-aspect-class-integrity-design.md)
   addresses the class-integrity half. `no-pattern: a declared-read floor for AI-authored scripts, and a
   sensitivity gate on the Starlark-bearing artifact kinds`. **This is the row that carries hole 2; it must
   not be closed with this one.**
2. **★★ — `capability-kv` has at least eight readers, two of which re-implement the read inline.**
   `aiagent/traversal.go:170,174` and `gateway/rolesanchors` read the authorization surface outside
   `capabilitykv.ReadAndMerge`, and it is UNVERIFIED whether they apply the reserved-op / provenance / lane
   gates the Processor does. A reader-side question this design does not touch. Absorbs the observation that
   `containsSensitiveRefLiteral` is a raw substring scan, advisory by its own doc comment — same root cause, a
   syntactic proxy for a semantic property.

---

## 15. Adversarial pass — findings and dispositions

Two independent lenses, run cold against `e29464fd` while the draft was still warm.

| # | Finding | Disposition |
|---|---|---|
| **B1** | "Hole 2 is falsified" over-generalized from the `lens` kind to all six; three live paths reach sensitive plaintext; the design missed the sibling design **in its own working tree** that refutes it | **Accepted, reshaped.** §5.5 rewritten; G14 rescoped; §For-Andrew's sentence withdrawn; filed as residual row 1 |
| **B2** | `cap-read.*` is a sixth, package-extensible family in the same bucket; the staged contract edit contradicted §6.14; the closed registry would refuse bootstrap's own lens; **C2's regex and C3's grep were built so they could not see it** | **Accepted, reshaped.** Two-tier registry (§5.3.2); contract edit rewritten; C2/C3 corrected and the self-blinding censuses called out in §7 |
| **B3** | H4 was diagnosed, made load-bearing, and given no owner | **Accepted.** Now **Increment 0** (§5.1) |
| **M1** | The `IsAuthPlane` lift is an import cycle (`full`'s in-package tests import `pkgmgr`); the real risk is normalization, which the proposed test could not catch | **Accepted.** §5.3.3 rewritten; empty-`Adapter` + alias cases mandated |
| **M2** | Population is ≥18 not 4; `KeySpace` inert on the postgres arm; bootstrap lenses are not `LensSpec` | **Accepted — this killed the field.** §5.3.2 derives instead of declaring; alternative G records the rejection |
| **M3** | Pausing is fail-**open** for revocations, and Contract #6 §6.14 records that exact incident | **Accepted, disposition reversed** to `CatTerminal` on the offending key (§5.3.4) |
| **M4** | Increment 1's registry-derived set could never cover package/app buckets; `truncateKeys` with an empty prefix wipes a whole bucket | **Accepted, reshaped** to "no existing producer" (§5.2) |
| **M5** | Increment 3 cannot "stop reading manifests"; the kernel discriminator was unstated | **Accepted.** §5.4 names `bootstrap.PrimordialVertexKeys()` and rejects the retired `data.protected` |
| minors | G10/G12/G14/G16/G17 citations; `Table` dead surface; G7 undercount; C1 is a fixture claim | **All folded** into §2 (marked ✎) and §5.2 |
| survived | The `lens`-kind falsification itself; `CatStructural` really does reach the pause path; **apply is a genuine choke point with no bypass**; G12; G13; the `orchestration-history` judgement call | Unchanged |

---

## 16. Board row

```
| **[capability-author] Three admission holes let an authored artifact reach the auth plane** | … | ★★★ | L | 📐 awaiting-Andrew · [design](../../implementation-artifacts/authored-artifact-admission-model-design.md) |
```

The row's *What* is corrected in the same commit: hole 2 is falsified for the lens kind and re-filed as its
own row for the Starlark kinds, and the apply-time revalidation gap is named in its place.
