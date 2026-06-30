# AI-authored capabilities — a Lattice-aware agent proposes packages (lenses / grants / targets / patterns / DDL) through deterministic validation + human review + F-004 rollback — design

**Status: ✅ Andrew-ratified (2026-06-29)** — Decision 1 = A (human-in-the-loop always; B design-only), Decision 2 = phased (Starlark gated behind ⑥'s sandbox + a separate ratification); no frozen-contract change; build-sequenced behind the (shipped) Augur; first fire = the complete lens-kind loop. See the *Ratified* block.
**Component:** cross-cutting — a new `capability-author` package + a new bridge adapter + the Processor's F-004 install/upgrade ops (apply) + Loupe (review) · reuses the **Augur** propose→validate→gate→apply skeleton
**Backlog row:** Lattice lane → *AI-native → AI-authored capabilities* (★★–★★★, L) — the marquee AI vision; **the Augur (✅ ratified) is its bounded, de-risking first step**, and this is the tier above it.
**Author:** Winston (Designer fire, 2026-06-29)

---

## For Andrew (ratify in one look)

**What it does, in two lines.** A Lattice-aware agent turns a capability *request* ("I need a lens
that lists active providers by specialty"; "grant the front-desk role the `RescheduleAppointment`
permission") into a **proposed package artifact** — a lens cypher, a capability grant, a Weaver target,
a Loom pattern, (later) a DDL script — recorded as a `vtx.capabilityProposal` vertex, **deterministically
validated** (the *same* `pkgmgr.validateAll` + openCypher parser + lint-conventions + a sandbox dry-run
the human authoring path already runs), and **applied only after a human approves** — via the existing
**F-004 `InstallPackage`/`UpgradePackage` op submitted under the approving *operator's* identity**, with
F-004 upgrade/uninstall as the rollback. The AI **proposes**; deterministic validation + a human gate +
the kernel's step-8 protected-key guard **govern**; the AI never holds the authoring capability and the
Processor stays the sole writer (P2 intact). This is the Augur pattern (AI proposes → validate → human
gate → Processor writes) lifted from *arranging existing ops* to *authoring new package capabilities*.

**Frozen-contract change: NONE anticipated** (same restraint as the Augur design, and for the same
reasons). The proposal vertex + its ops are **package DDL**; the `capabilityAuthor` adapter + its
envelope are **package/bridge data** (§10.5 — the `external` domain is ordinary); the apply path is the
**already-ratified F-004 `InstallPackage`/`UpgradePackage` op** (Contract #8 §8.1/§8.6, committed); the
AI's `RequestCapabilityAuthoring` grant + the operator's existing `InstallPackage`/`UpgradePackage` grant
are ordinary rbac-domain capability data. I checked the one place a contract touch *could* hide — whether
the kernel needs a new server-side "validate-only" op to give AI artifacts kernel-grade pre-validation —
and resolved it **NO** (§5): the existing F-004 op's step-8 guard is the **authoritative** validation gate
at apply, and the human approves before that; the pre-apply pass is advisory defense-in-depth. So this
fire stages **no** uncommitted contract edit. (I have left the unrelated in-flight
`docs/contracts/06-capability-kv.md` working-tree edit untouched.)

**Two decisions for your call (both designed through, recommendations given).**

1. **The authoring autonomy boundary — may an AI-authored capability *ever* apply without a human?**
   - **Option A — human-in-the-loop always (RECOMMENDED, and I recommend it as the *standing posture*,
     not just the first step).** Every proposal waits for an explicit operator `approve` before the
     `InstallPackage`/`UpgradePackage` op is submitted. The AI authors *executable capabilities* (a lens
     that could over-project protected data; a grant that could widen authority) — categorically higher
     blast radius than Augur's op-arrangement, and capability authoring is **low-frequency / high-
     consequence**, so removing the human buys little and risks much.
   - **Option B — confidence/kind-gated auto-apply** (designed in §8 Fire 5, **recommend NOT building**).
     A per-kind allow-list + confidence floor + mandatory validation could let the *lowest*-risk kind (a
     purely-additive grant *within* an existing role's already-held scope) auto-apply. I designed the
     hook for completeness but **recommend it stay unbuilt** until a long operating record exists — this
     is stronger than my Augur recommendation (where B was "build dark"): here B is "design-only, don't
     scaffold." **Your call:** ratify A as the standing posture (rec), or signal that B is worth a future
     fire.

2. **The artifact-kind boundary — what may an AI author, and what waits?** (a scope fork, designed
   through). **Recommendation — phase by deterministic-validatability:** author **lenses** (cypher —
   parseable + sandbox-projectable), **capability grants** (roles/permissions/links — fully schema-
   validated by Contract #6 + the kernel), and **declarative Weaver targets + Loom patterns** (§10.2/§10.8
   JSON — `validateAll`-covered) **first**; **gate Starlark-bearing artifacts** (`vertexType`/`opMeta`
   `.script` — open-ended generated *code* that runs on the write path) behind **both** the verified-pure
   Starlark sandbox (the separate 📋 *Starlark guards* backlog item) **and** a separate ratification.
   Rationale: the first set is fully *statically* validatable without executing AI-generated code; Starlark
   is not (it executes), so it must wait for the sandbox that the platform already plans to build.

**The AI-actor-authority question (#125) is *already resolved* — I honor it, it is not a fork.**
`lattice-architecture.md` Item 4 (lines 973–986): *"AI agents are regular identity vertices subject to
the same Capability Lens authorization as human actors; there is no privileged 'AI actor' class or
bypass,"* naming `identity.ai.<purpose>.<id>`, capability-scoped narrower than operators. So the AI holds
**only** a `RequestCapabilityAuthoring` grant; the privileged `InstallPackage`/`UpgradePackage` authority
stays with the human operator who approves. No special actor class, no bypass.

Everything else is resolved in the body. Nothing here blocks the **Lattice Steward** except your
ratification + the two decisions above.

**Ratified (Andrew, 2026-06-29).** **Decision 1 = Option A** — human-in-the-loop **always**, the standing posture;
**Option B (auto-apply) stays design-only, not built.** **Decision 2 = phased by deterministic-validatability** —
author **lens / grant / weaverTarget / loomPattern** first; **gate Starlark-bearing DDL** (`vertexType`/`opMeta`
`.script`) behind **⑥'s verified-pure `internal/starlarksandbox` + a separate ratification.** **No frozen-contract
change.** **Build-sequenced behind the Augur** (✅ ratified + SHIPPED — the precursor is in `main`).
**Decomposition collapsed (fewer-larger-fires):** the **first fire is the COMPLETE lens-kind loop** — propose →
validate → human-approve → operator-applies (F-004) → live + F-004-revertible — not a propose-only half-loop; the
**grant** kind is the fast-follow. **⑥ linkage:** the gated Starlark fire is ⑥'s shared-sandbox **first
consumer**, so the `internal/starlarksandbox` extraction lands *with* that fire.

---

## 1. Problem & intent

**The vision.** The brainstorming inventory names the **AI Handshake protocol** —
*"Context Hydration → Proposed Intent → Validation → optional dual-link Human-in-Loop approval"*
(brainstorm #592, Stream 4 owns it) — and frames Weaver's evaluator as **tiered intelligence**
(L1 cypher / L2 Starlark / L3 AI, #375). The feature backlog carries the capstone as
*AI-native → AI-authored capabilities* (★★–★★★, L): *"A Lattice-aware agent proposes DDL / Starlark /
lenses / workflows through human review + deterministic validation + rollback-friendly contracts. Marquee
AI vision."* The backlog explicitly marks **the Augur (✅ ratified, build-now) as its bounded, de-risking
first step.** The Augur's own design says so (augur-design.md §1): L3 *"establishes exactly that pattern —
AI proposes, deterministic validation + human review govern — at the smallest safe surface (one stuck
convergence gap), before the platform trusts an agent to author capabilities,"* and lists authoring
*"DDL / Starlark / lenses"* as its **explicit non-goal** — *"that is the larger 'AI-authored capabilities'
item this de-risks."* **This design is that larger item.**

**What changes from Augur (the one-sentence delta).** Augur lets the AI **arrange existing ops** from a
closed catalog (`{triggerLoom, assignTask, directOp}`) to close a *convergence gap*. AI-authored
capabilities lets the AI **author new package artifacts** (lenses / grants / targets / patterns / DDL) to
*extend the platform's vocabulary* — a strictly larger and more dangerous surface (generated executable
artifacts, not a selection from a fixed menu), which is exactly why it inherits Augur's *entire* safety
skeleton and adds a richer deterministic-validation core.

**The concrete capability today (and why the platform is ready).** A new capability is, by architectural
decision #10 (minimal-core + everything-is-a-package), **a package** — a manifest + declarative artifacts
(a lens `.spec` cypher, a grant's role/permission/link tuples, a Weaver target spec, a Loom pattern spec,
a DDL `.script`). Authoring one today is a *human* loop: write the package, `lattice-pkg install --dry-run`
to preview the delta, install, and `uninstall`/`upgrade` to revert (F-004 / FR53). Everything an AI needs
to participate in that loop **already ships**:

- the **deterministic validators** (`internal/pkgmgr` `validateAll`: canonical-name + permission-identity
  uniqueness, lens bucket/adapter guards, Weaver-target + Loom-pattern + op-meta + gap-action validators),
- the **openCypher parser** (Refractor) to statically validate a lens spec,
- the **lint-conventions** gates (P5 lens-read-path, P7 class-discriminator) the build already runs,
- the **F-004 apply machinery** (`InstallPackage`/`UpgradePackage` ops, version-independent keys,
  `--dry-run` preview, `uninstall` revert — Contract #8 §8.1/§8.6, ratified + committed),
- the **kernel step-8 protected-key guard** (a package *cannot* mutate primordial/protected DDLs or
  substrate surfaces — `_packages.md` "What a package CANNOT do"), the **authoritative** apply-time deny,
- the **Augur skeleton** (propose via bridge → record proposal vertex → human-review op → apply), and
- the **AI-actor model** (Item 4: `identity.ai.*`, capability-scoped, no bypass; self-discovery via graph
  traversal — `5-2-cold-start-ai-agent-traversal.md`).

So this feature, like Augur, is **overwhelmingly composition of shipped + ratified machinery**. The only
genuinely-new platform surfaces are (a) the **proposal vertex + ops** (package DDL), (b) the
**`capabilityAuthor` bridge adapter** (bridge data), and (c) one new deterministic component — the
**capability materializer** (declarative artifact → an install/upgrade write-set), the data-driven analog
of a package's build-time Go `Definition`.

**Non-goals (explicitly out).** This feature does **not**: let an AI mutate the **primordial/kernel
seed** (protected by the step-8 guard; kernel changes need a bootstrap, `_packages.md` known-limitations —
AI authoring targets the *package* plane only); grant the AI the `InstallPackage`/`UpgradePackage`
capability (Item 4 — the AI proposes, the human applies); make the AI call an LLM in-process (the call
goes through the bridge, like Augur); author **Starlark** in the initial fires (gated, §8 Fire 4); or
auto-apply without a human (Option A is the standing posture; §For-Andrew #1).

---

## 2. Why this is well-grounded (every mechanism it needs already ships or is ratified)

| AI-authored capabilities needs | Already shipped / ratified | Reused how |
|---|---|---|
| An outbound call to the reasoning model | **The bridge** + the **Augur** path | A new `external.capabilityAuthor` **adapter** (package/bridge data), dispatched via `triggerLoom → externalTask` exactly like the `augur` adapter. Weaver/Loom never call an LLM directly. |
| To record the model's artifact as durable, auditable state | **The op core + transactional outbox** (Processor, P2); the **Augur proposal-vertex pattern** | `RecordCapabilityProposal` (the externalTask `replyOp`) creates `vtx.capabilityProposal.<NanoID>` — an op, never a direct write. |
| Deterministic validation of the artifact | **`pkgmgr.validateAll`** + the **openCypher parser** + **lint-conventions** + the **kernel step-8 protected-key guard** | The materializer runs the proposed artifact through `validateAll` (+ parser for lens specs) at record + approve time (advisory); the **F-004 op's step-8 guard is the authoritative deny at apply** (§5). |
| To turn a declarative artifact into an install write-set | **`pkgmgr.Installer.buildManifestBatch`** (F-004) — assembles a Definition's write-set | A new **capability materializer** builds the *same* write-set shape from the proposal's declarative artifact (the data-driven analog of a Go `Definition`). |
| To apply the artifact through the privileged DDL path | **F-004 `InstallPackage` / `UpgradePackage` ops** (Contract #8 §8.1/§8.6, ratified) | An approved proposal is applied by submitting the existing op **under the approving operator's identity** — no new apply path, no new kernel op. |
| To revert (rollback-friendly contracts / FR53) | **F-004 `upgrade`/`uninstall`** + version-independent keys; **FR53** (revert via compensating op, no downtime) | An applied AI-authored package is upgraded/uninstalled exactly like a human-authored one. Rollback is *already* the platform's revert story. |
| Operators to review proposals | **Lens read-models (P5) + Loupe** (the inspector) | A `capability-proposals` review lens (package DDL) projects pending proposals + their validation report + dry-run delta; Loupe renders + acts. |
| The action/artifact catalog the model authors within | **DDL self-description** (`inputSchema`/`fieldDescription`/`examples`) + the **installed lens/target/pattern shapes** | The model reasons over the same self-description surface Loupe renders op-forms from, projected as a read-model lens (the `capability-author-context` lens). |
| AI as a capability-scoped, non-privileged actor | **Architecture Item 4** (`identity.ai.*`, no bypass) + **FR53 self-discovery** | The AI identity holds only `RequestCapabilityAuthoring`; everything it proposes is gated by the human + the kernel. |

**Invariants honored (checked explicitly):**
- **P1** — a proposal is auditable, queryable business/meta state → a Core KV vertex
  `vtx.capabilityProposal.<NanoID>`. The in-flight reasoning *call* is operational → the bridge claim
  (outside Core KV). The applied capability is ordinary package state (vertices/aspects/links).
- **P2** — every mutation (proposal create, review flip, the apply install/upgrade) is an op via the
  Processor. The AI's bridge call writes nothing to Core KV; the materializer produces an op, not a write.
- **P5** — operators read proposals via the `capability-proposals` lens; Loupe (sole inspector) may read
  Core KV. No vertical app scans Core KV.
- **P7 / Contract #1** — proposal vertex `vtx.capabilityProposal.<NanoID>`; 4-seg aspects; 6-seg links
  reading "source relation target" with the later-arriving proposal as source (§3.1); an AI-authored
  artifact must itself obey P5/P7 — checked by the **lint-conventions gates at validation time** (so the
  AI cannot author a P5-violating lens or a P7 shadow-class aspect and have it pass).
- **"The AI gains no new authority"** — Item 4 + the human-submits-the-apply rule: the applied op runs
  under the *operator's* capability, and the step-8 guard means even an approved adversarial artifact
  cannot touch a protected root or escape the operator's own scope.

---

## 3. The shape

### 3.1 Data model — the proposal vertex (package DDL, `capability-author`)

A proposal is a first-class, auditable, queryable artifact → a Core KV vertex (P1). Key shape per
Contract #1 (the proposal arrives after the requester + any target package, so it is the **source** of
every link; names pass the sentence test):

```
vtx.capabilityProposal.<NanoID>
  vtx.capabilityProposal.<id>.request    { requesterId, intent, contextRef }   # what was asked
  vtx.capabilityProposal.<id>.artifact   { kind, content }                     # the proposed capability (constrained)
       # kind ∈ {lens, grant, weaverTarget, loomPattern, vertexTypeDDL, opMeta}
       # content = the declarative payload for that kind (cypher spec / grant tuples / target spec / …)
  vtx.capabilityProposal.<id>.target     { mode, packageName, baseVersion, newVersion }
       # mode ∈ {newPackage, upgradeExisting}  — newPackage→InstallPackage; upgradeExisting→UpgradePackage
  vtx.capabilityProposal.<id>.rationale  { text }                              # the model's reasoning (audit)
  vtx.capabilityProposal.<id>.confidence { score }                            # 0..1 self-reported
  vtx.capabilityProposal.<id>.validation { state, report, deltaPreview, checkedAt }
       # state ∈ {valid, invalid}; report = per-validator results; deltaPreview = the dry-run create/update/tombstone delta
  vtx.capabilityProposal.<id>.provenance { model, promptHash, catalogHash, reasonedAt }
  vtx.capabilityProposal.<id>.review     { state, reviewedAt, appliedAt, appliedByOp }
       # state ∈ {pending, approved, rejected, applied, invalid, superseded}

lnk.capabilityProposal.<id>.requestedBy.identity.<requesterId>   # proposal requestedBy requester
lnk.capabilityProposal.<id>.reviewedBy.identity.<reviewerId>     # proposal reviewedBy reviewer (on review)
lnk.capabilityProposal.<id>.appliedAs.package.<packageId>        # proposal appliedAs package (on apply)
```

`review.state` lifecycle (each transition is an op; terminal states auditable):

```
            ┌──────────── reject ───────────► rejected
pending ────┤
            ├── approve ──► approved ── apply (InstallPackage/UpgradePackage) ──► applied
            └── (validator fails at record time) ──────────────────────────────► invalid
   (a newer proposal for the same (requester, intent-key)) ─────────────────────► supersedes the older ──► superseded
```

### 3.2 The artifact-kind taxonomy (the deterministic-validatability spine — and the Starlark gate)

The kinds are deliberately ordered by *how completely they can be validated without executing
AI-generated code* — this ordering **is** the fire decomposition (§8) and the §For-Andrew #2 boundary:

| Kind | Content (declarative) | Deterministic validation | Risk | Fire |
|---|---|---|---|---|
| **lens** | a Refractor lens meta-vertex: `lensRef`, target bucket/adapter, the openCypher `.spec` | `validateLensBuckets`/`validateLensAdapters` + **the openCypher parser** (statically parse the spec) + **the P5 lint gate** (target ≠ Core KV) + a **sandbox dry-run projection** over a sample (§5) | Low — pure projection; a bad lens over-projects but **D1/RLS** is the read-auth boundary, not the lens | 1 |
| **grant** | roles / permissions / `grantedBy`/`holdsRole` links (Contract #6 shape) | full Contract #6 schema validation + `validatePermissionIdentityUniqueness` + the **scope check** (the grant cannot exceed the *requesting operator's* own held scope — §5) | Low–Med — widens authority, but bounded by the operator's scope + the human gate | 2 |
| **weaverTarget** | a `meta.weaverTarget` spec (§10.2/§10.8 — `gaps`, `lensRef`, templates) | `validateWeaverTargets` + `validateGapAction` (every gap action resolves; templates parse) | Med — drives convergence dispatch, but only of already-installed ops | 3 |
| **loomPattern** | a `meta.pattern` spec (§10-pattern shape — declarative steps over installed ops) | `validateLoomPatterns` (every step op resolves; no Starlark) | Med — orchestrates installed ops | 3 |
| **vertexTypeDDL / opMeta** | a DDL meta-vertex carrying a **Starlark `.script`** | static + `validateOpMetas` + **a verified-pure Starlark sandbox dry-run** | **High — generated executable code on the write path** | **4 (GATED)** |

**The crux (and §For-Andrew #2):** the first four kinds are **fully statically validatable** — the
artifact is declarative data (cypher parsed not executed; grants are graph tuples; targets/patterns are
schema-checked specs). The fifth kind carries a **Starlark `.script`** that *executes* on the write path,
so it cannot be validated by static analysis alone — it needs the **verified-pure Starlark sandbox** that
is *already a separate backlog item* ("Starlark guards (Loom)", 📋, *"needs a verified-pure sandbox"*).
**AI-authored Starlark is therefore sequenced behind that sandbox + a separate ratification** — exactly
the "build-sequenced behind a prerequisite design" pattern the backlog uses for Vault/D1/Personal-Lens.

### 3.3 Write path (P2) — the new package ops

| Op | Submitted by | Effect (Starlark DDL) |
|---|---|---|
| `RequestCapabilityAuthoring` | a human operator **or** an `identity.ai.*` agent (holds the narrow `RequestCapabilityAuthoring` grant) | Records the request intent + context ref and **dispatches the reasoning escalation** (§3.4). Does **not** author anything itself. |
| `RecordCapabilityProposal` | the bridge's `replyOp` (the `capabilityAuthor` externalTask result) | **Deterministic validation gate** (§5): run the proposed artifact through the materializer's `validateAll` + parser + lint + a sandbox dry-run; then create `vtx.capabilityProposal.<id>` with `review.state = pending` + `validation.state = valid`, **or** `pending`-blocked-as `invalid` if validation fails (stored with the report, never applicable). `id` is **deterministic** from `(requesterId, intentKey, episode)` so a redelivered reply collapses (Contract #4 tracker + `CreateOnly` backstop). |
| `ReviewCapabilityProposal` | a human operator (Loupe / `lattice` CLI), capability-authorized | Flip `review.state` `pending → approved \| rejected`; write `reviewedBy` + `reviewedAt`. **Re-runs the deterministic validator on approve** (the live catalog/registry can drift between propose and approve) → `invalid` if it no longer validates. |
| `InstallPackage` / `UpgradePackage` *(existing F-004 op — NOT new)* | **the approving operator** (holds the privileged grant; the AI never does) | The materializer builds the write-set from the approved artifact and the operator submits the **existing** op; the **kernel step-8 guard** is the authoritative deny (protected-key, schema, capability). On success the committer stamps `review.state → applied` + the `appliedAs` link + `appliedByOp`. |

All new op types are **package DDL** under `ops.meta.>` — the generic Processor accepts them; no contract
change (ops are package data).

### 3.4 The escalation — `triggerLoom → externalTask → bridge` (mirrors Augur §3.3 exactly)

```
RequestCapabilityAuthoring committed (requester holds the grant)
  → its DDL fires triggerLoom of the capabilityAuthor pattern (a primordial pattern) whose body is an externalTask:
       { kind: externalTask, adapter: "capabilityAuthor",
         params: { requesterId, intent, contextRef },
         replyOp: "RecordCapabilityProposal", instanceOp: "CreateAuthoringClaim" }
  → the instanceOp commits the claim vertex (FR58 "visible claim before the call"), emits
       external.capabilityAuthor, the Loom engine PARKS  (standard externalTask machinery, §10.6)
  → the bridge's capabilityAuthor adapter:
       (a) reads the AUTHORING CATALOG from a read-model lens (the installed lens/target/pattern/op
           self-description — inputSchema / fieldDescription / examples — filtered to the enabled
           artifact kinds, §3.2 / §For-Andrew #2);
       (b) hydrates the request context (the intent + a bounded subgraph named by contextRef);
       (c) calls Claude with a STRUCTURED-OUTPUT schema constrained to {kind ∈ enabled-kinds,
           content ∈ that kind's artifact schema, target, rationale, confidence};
       (d) returns that object as the externalTask Result.
  → the bridge posts replyOp RecordCapabilityProposal → the Processor runs the §5 validator + creates
       the vtx.capabilityProposal vertex (pending/valid or invalid) → orchestration.externalTaskCompleted
       closes the Loom instance.
```

**Why the bridge, not an in-process LLM client** — identical to Augur §3.3: keeps Weaver/Loom pure
(imports only `substrate/*`), reuses durable-claim / idempotency / recovery, gives the call FR58
idempotency (`idempotencyKey = instanceKey` → at most one billed model call per authoring episode under
redelivery). The call is **synchronous** (seconds) → the bridge's `Adapter.Execute` path suffices.

### 3.5 The apply path (the loop closes — Fire 2 onward)

An approved proposal is applied by the **capability materializer**: it reads the proposal's declarative
`artifact.content`, assembles the *same* write-set shape `Installer.buildManifestBatch` produces (for a
`newPackage`) or the diff `Installer.Upgrade` produces (for `upgradeExisting`), and the **approving
operator** submits the existing **F-004 `InstallPackage`/`UpgradePackage` op**. Three properties make this
safe and Weaver-consistent:

1. **The AI never holds the authoring authority** — the op runs under the *operator's* identity (Item 4).
2. **The kernel step-8 protected-key guard is the authoritative, independent backstop** — even an
   approved adversarial artifact cannot mutate a protected/primordial root or escape the operator's scope
   (the *same* guard that already protects every human package install).
3. **Rollback is free (FR53 / F-004)** — an applied AI-authored package is `upgrade`/`uninstall`-able
   exactly like a human-authored one; version-independent keys (§8.1) keep cross-version refs intact.

This keeps the Processor the **sole writer** (P2), adds **no new apply path or kernel op**, and makes an
applied AI-authored capability **indistinguishable downstream from a hand-authored package** — the exact
safety property we want (mirrors Augur §3.4's "indistinguishable from a hand-authored playbook entry").

### 3.6 The reasoning model

The `capabilityAuthor` adapter is **model-pluggable** (adapter config, not a contract). Default
**`claude-opus-4-8`** — authoring is intelligence-sensitive + low-frequency, so reasoning quality
dominates (same rationale as Augur's opus default; sonnet an explicit opt-in). Structured output is
enforced via the Messages API tool-use / `output_config.format` so the model **cannot** return a `kind`
outside the enabled set or `content` outside that kind's artifact schema — the schema is the *first* line
of the §5 boundary, not the only one. `provenance.promptHash`/`catalogHash` record exactly what was
reasoned over (audit + stale-proposal detection). Per the `claude-api` reference: adaptive thinking +
`effort: high`/`xhigh`; handle `stop_reason:"refusal"` → store the proposal `invalid`, never crash.

---

## 4. Orchestration precedent mirrored

Nothing novel in the control plane — every mechanism is a named, shipped/ratified precedent:

- **`triggerLoom → externalTask → bridge`** — the post-13.1 external-remediation path (§10.5/§10.6/§10.8);
  the Augur reuse. The reasoning call is one more externalTask adapter.
- **Deterministic `replyOp` → vertex create** — the externalTask result-op pattern (bridge.md; §10.6);
  `RecordCapabilityProposal` is the `replyOp`, like Augur's `RecordProposal`.
- **Anti-storm mark + OCC + lease + reconciler-sweep reclaim** — standard externalTask machinery; a
  crashed/lost authoring call is reclaimed + re-asked at lease expiry, idempotent on the bridge key.
- **F-004 `InstallPackage`/`UpgradePackage` + `--dry-run` + version-independent keys** — the apply +
  preview + rollback machinery (Contract #8 §8.1/§8.6).
- **`pkgmgr.validateAll` + openCypher parser + lint-conventions** — the human authoring path's validators,
  reused verbatim by the materializer.
- **Lens read-model + DDL self-description** — the authoring catalog the model reasons over (the same
  surface Loupe + Augur use). No new self-description machinery.
- **Capability-authorized human op** — `ReviewCapabilityProposal` is an ordinary capability-checked op,
  like any operator mutation.

---

## 5. The deterministic validation boundary (the safety core)

The load-bearing safety mechanism — **the AI never produces a capability that wasn't deterministically
validated, and never one that a human didn't approve.** Validation runs at **four** points (defense in
depth; the first three mirror Augur §5, the fourth is the kernel backstop that makes this *categorically
safe even for authored code*):

1. **At reasoning time (schema constraint).** Structured output forces `kind ∈ enabled-kinds` and
   `content` conforming to that kind's artifact schema. The model *cannot* emit a free-form artifact or a
   disabled kind (Starlark stays off the menu until Fire 4).
2. **At record time (`RecordCapabilityProposal` DDL → the materializer).** Before a proposal is stored
   `pending`, the materializer **deterministically validates the artifact without applying it**:
   - run `pkgmgr.validateAll` over the materialized Definition (canonical-name + permission-identity
     uniqueness, lens bucket/adapter, Weaver-target/Loom-pattern/op-meta/gap-action);
   - **parse** a lens `.spec` with the openCypher parser (reject unparseable cypher);
   - run the **lint-conventions** checks (P5 — a lens target must not be Core KV; P7 — no shadow-class
     aspect) so the AI cannot author a convention-violating capability that passes;
   - **scope check** — a `grant` artifact's conferred authority must be a **subset of the requesting
     operator's own held scope** (an AI request routed by operator X cannot mint a grant X couldn't mint
     by hand); a `lens`/`target`'s anchors must be within the requester's domain;
   - **sandbox dry-run** — compute the F-004 create/update/tombstone delta (the `--dry-run` path, *submits
     nothing*) and, for a lens, a **sample projection in a throwaway Refractor sandbox** to confirm the
     cypher executes + shapes rows (no Core KV write, no real target write).

   Fail any check → the proposal is stored `invalid` with the per-check report (auditable), never
   `pending`, never applicable. The report + delta preview are projected to the review lens for the human.
3. **At approve time (`ReviewCapabilityProposal`).** Re-run step 2 against the **live** catalog/registry
   (drift between propose → approve) before allowing `approved` → `invalid` if it no longer validates.
4. **At apply time (the kernel — the authoritative, independent backstop).** The approved artifact is
   applied via the **existing F-004 `InstallPackage`/`UpgradePackage` op**, whose **step-8 protected-key
   guard** is the *authoritative* deny: it rejects any write to a protected/primordial/auth root or
   substrate surface, validates every aspect schema, and enforces the **submitting operator's** capability
   — *independently of* steps 1–3. So even a maximally-adversarial artifact that somehow passed 1–3 and a
   human approved can only land what an ordinary operator package install can land — **the AI gains no new
   authority**, only the ability to *propose* a package the operator could already author by hand.

**Why no new kernel "validate-only" op (the one contract-touch I checked and rejected).** It is tempting
to add a server-side `ValidatePackage` op so AI artifacts get *kernel-grade* validation pre-apply. But the
kernel's authoritative gate (step 4) *already* runs at apply, behind the human gate — so a pre-apply
kernel pass would only move the deny earlier, not make anything safer, while adding a frozen-contract op.
The client-side `validateAll` + parser + lint + dry-run (step 2) catches the overwhelming majority
advisory-style; the human reviews the report; the kernel is the authoritative backstop at apply. **Net:
no new kernel op, no frozen-contract change** — defense-in-depth without contract surface.

**Blast radius:** under the recommended Option A (human-in-the-loop always), a bad proposal **cannot
apply** without a human approving it *and* the kernel guard passing it. Under the (recommend-against)
Option B auto-apply, it is bounded to the opted-in lowest-risk kind, fully validated, audited, and
F-004-reversible.

---

## 6. Contract surface

**No frozen-contract change anticipated** (same restraint + reasons as the Augur design):
- `vtx.capabilityProposal` + `RequestCapabilityAuthoring`/`RecordCapabilityProposal`/
  `ReviewCapabilityProposal` are **package DDL** — Contract #1 governs the *key shapes* (honored); a
  specific vertex type / op is package data.
- The `external.capabilityAuthor` adapter + its envelope are **package/bridge data** — §10.5 (the
  `external` domain is ordinary; no Processor allowlist, no Contract #3 amendment). A new adapter is
  bridge-registry config, exactly like `backgroundCheck`/`augur`.
- The apply path is the **already-ratified F-004 `InstallPackage`/`UpgradePackage` op** (Contract #8
  §8.1/§8.6, committed). No change.
- The `capability-proposals` review lens + the `capability-author-context` catalog lens are **package
  DDL** (Refractor targets).
- The AI's `RequestCapabilityAuthoring` grant + the operator's existing `InstallPackage`/`UpgradePackage`
  grant are **rbac-domain capability data** (Contract #6 shape, honored).
- New Health metrics (`authoringRequests`, `proposalsPending`, `proposalsApplied`, `proposalsInvalid`,
  `proposalsRejected`) are **author-discretion** under Contract #5 §5.4.

I checked the one place a contract touch could hide (a server-side validate-only op) and **rejected it**
(§5). **This fire stages no uncommitted contract edit.** If, at build, the Steward finds a genuine gap
(e.g. a need to *reserve* the `capabilityProposal` vertex class or the `ops.meta` authoring ops in a
contract), that is a small additive amendment to flag then — but the design does not require one.

---

## 7. Migration / compatibility & test strategy

**Migration.** Purely additive. Bootstrap gains one primordial package (`capability-author`: the proposal
type + the three ops + the two lenses + the `capabilityAuthor` pattern + the adapter registration),
bumping the bootstrap version like every prior package add. The bridge gains one adapter. The `identity.ai.*`
authoring-agent identity + its narrow `RequestCapabilityAuthoring` grant are seeded (or installed by an
rbac-domain package). No data migration; **zero behavior change** for anything until an operator/agent
submits a `RequestCapabilityAuthoring`.

**Test strategy** (each fire ships green; mirrors the Weaver/Augur e2e style):
- **Unit — the materializer + validator (§5) table.** Every reject class per kind → `invalid`
  (unparseable lens cypher; a P5-violating Core-KV-target lens; a P7 shadow-class aspect; a grant
  exceeding the operator's scope; a Weaver target with an unresolved gap action; a Loom step naming an
  uninstalled op; a `kind` outside the enabled set; a Starlark artifact before Fire 4); every accept
  class → `pending`/`valid` with the correct delta preview. Proposal-id determinism (redelivered reply
  collapses, no duplicate).
- **A faked `capabilityAuthor` adapter** (test-only, deterministic — the `Fake*` bridge-adapter pattern):
  returns a canned `{kind, content, target, confidence}` so the whole request → reason → record → review
  → apply loop runs against the **real** Processor + bridge + materializer + F-004 ops on the ephemeral
  stack, with **no real model call in CI**.
- **E2e (ephemeral stack) — the happy path per kind.** `RequestCapabilityAuthoring(intent=lens X)` →
  a `pending`/`valid` proposal with a dry-run delta lands in the `capability-proposals` lens →
  `ReviewCapabilityProposal{approve}` → operator applies via `InstallPackage` → the lens is live + queryable
  → the proposal flips `applied`. A second e2e applies a `grant` and confirms the new permission is
  enforced. A third applies an `upgradeExisting` and confirms the F-004 diff lands + the proposal is
  F-004-revertible (`uninstall`/`upgrade` back).
- **Adversarial — the Gate-3-style "DEFENDED" assertion for the authoring surface.** A faked adapter that
  returns a malicious artifact — a lens targeting **Core KV** (P5 escape); a grant exceeding the operator's
  scope; an artifact whose materialized write-set touches a **protected/primordial** key; a Starlark
  artifact before Fire 4 — is caught at the §5 validator (`invalid`) and, if it somehow reached apply, at
  the **kernel step-8 guard**; it **never** applies. Plus: an `identity.ai.*` agent that tries to submit
  `InstallPackage` *directly* (bypassing the human) is **capability-denied** at the Processor (Item 4 — the
  AI never holds that grant).

**Review.** This is a **substantial, cross-cutting AI-surface** design touching the capability-authoring
plane. Per the Designer mandate I ran a **focused self-adversarial pass** (§9) for this unattended fire
(matching the Augur design's precedent — `bmad-party-mode` is heavyweight + the sub-agent-no-commit
constraint makes it awkward unattended). **Strongly recommend the Steward run the full 3-layer adversarial
review on every fire at build** (capability/security plane), with explicit attention to the §5 validation
boundary (esp. the scope check + the P5/P7 lint at validation time), the apply-time kernel backstop, and —
when Fire 4 lands — the Starlark sandbox. A `bmad-party-mode` pass on the **scope-check + apply-authority
boundary** before Fire 2 is also recommended.

---

## 8. Decomposition for the Steward (fire-by-fire, each independently shippable + green)

Sequenced by the §3.2 deterministic-validatability spine. **Build-sequenced behind the Augur** (which
proves the propose→validate→gate→apply skeleton on the smaller op-arrangement surface first) — the design
is ratifiable now and shelved like Vault/Personal-Lens were behind their drivers.

**Ratified collapse (Andrew, 2026-06-29, fewer-larger-fires):** Fires 1+2 below combine into **one first fire =
the COMPLETE lens-kind loop** (capture + validate + review + the operator-submitted F-004 apply + the `applied`
flip), so the first unit delivers a *usable, revertible* capability rather than a propose-only half-loop; the
**grant** kind is the fast-follow (its §5 scope-check + the party-mode pass still apply). Fires 3 (orchestration
kinds) and 4 (Starlark, gated on ⑥'s sandbox + a separate ratification) unchanged; Fire 5 (auto-apply) stays
**design-only, not built** (Decision 1 = A).

- **Fire 1 — Authoring capture + lens kind (no apply).** The `capability-author` package (proposal type +
  `RequestCapabilityAuthoring` + `RecordCapabilityProposal` ops + the `capability-proposals` review lens +
  the `capability-author-context` catalog lens + the `capabilityAuthor` externalTask pattern) + the
  `capabilityAuthor` bridge adapter (with a `FakeCapabilityAuthor` for CI) + the **capability materializer**
  for the **lens** kind + the §5 record-time validator (parser + lint + sandbox projection). **Ships value
  alone:** a request becomes a deterministically-validated, human-reviewable `pending` lens proposal with a
  dry-run delta surfaced in Loupe. Zero apply. *(The bulk; L.)* — **full 3-layer review.**
- **Fire 2 — Approval → apply loop (lens + grant kinds).** `ReviewCapabilityProposal` op + the
  approved-proposal projection + the **operator-submitted F-004 apply** (`InstallPackage`/`UpgradePackage`
  under the operator's identity) + the `applied` flip + the **grant** kind in the materializer (with the
  §5 scope check) + a Loupe/CLI review-and-apply affordance. **Closes the loop:** AI proposes → human
  approves → operator applies → the capability is live + F-004-revertible. *(M.)* — **full 3-layer review
  (auth/capability plane); party-mode on the scope-check boundary first.**
- **Fire 3 — Declarative orchestration kinds.** The **weaverTarget** + **loomPattern** kinds in the
  materializer (`validateWeaverTargets`/`validateGapAction`/`validateLoomPatterns`). **Ships:** an AI can
  author convergence targets + orchestration patterns over already-installed ops. *(M.)* — full 3-layer.
- **Fire 4 — Starlark-bearing kinds (GATED on the verified-pure Starlark sandbox + a separate
  ratification).** The **vertexTypeDDL/opMeta** kinds, validated by static checks + `validateOpMetas` + the
  verified-pure Starlark sandbox dry-run. **Ships dark / sequenced** behind the "Starlark guards" backlog
  item *and* Andrew's sign-off on AI-authored executable code. *(M–L.)* — full 3-layer + the sandbox's own
  review.
- **Fire 5 — Autonomy dial (designed, recommend NOT building; gated on Andrew).** A per-kind auto-apply
  allow-list + confidence floor for the lowest-risk kind only (a purely-additive grant within the
  operator's held scope). **Recommend it stay unbuilt** (§For-Andrew #1). *(S; design-only unless ratified.)*

---

## 9. Risks & alternatives (self-adversarial pass)

| Risk | Mitigation |
|---|---|
| **The model authors a harmful capability** (a lens that over-projects PII; a grant that widens authority; an artifact touching a protected root). | The §5 four-point boundary: schema constraint → record-time `validateAll`+parser+lint+**scope-check**+sandbox dry-run → approve-time re-validate → **the kernel step-8 protected-key guard at apply** (independent, authoritative). Under Option A it *cannot* apply without a human. The AI gains **no new authority** (Item 4 — it holds only `RequestCapabilityAuthoring`; the apply op runs under the operator). Adversarial test proves DEFENDED. |
| **A lens over-projects protected data** (a read-path leak). | A lens is *pure projection*; the **read-auth boundary is D1/RLS**, not the lens (D1 §6.14 — protected-by-default; a `protected:true` model must target Postgres-RLS). The §5 P5 lint forbids a Core-KV-target lens; an AI-authored protected lens still lands behind D1's read enforcement. (Noted as the reason lens is the *lowest*-risk kind despite "projecting data".) |
| **Generated Starlark executes arbitrary logic on the write path.** | Starlark authoring is **gated** (Fire 4) behind the verified-pure Starlark sandbox (a separate planned item) **and** a separate ratification — it is *out of the initial scope* precisely because it can't be statically validated. The first four kinds carry no executable AI code. |
| **Cost / runaway authoring storm.** | The escalation is a standard externalTask → anti-storm mark + bridge `idempotencyKey` ⇒ **at most one billed model call per authoring episode**. `RequestCapabilityAuthoring` is a capability-gated op (not auto-fired by CDC). Health metric `authoringRequests` makes spend operator-visible. |
| **Stale proposal (catalog/registry drifts between propose → approve → apply).** | `provenance.catalogHash` records what was reasoned over; **re-validation at approve and the authoritative kernel guard at apply** fail-closed if the artifact no longer resolves/validates. A newer proposal for the same `(requester, intentKey)` **supersedes** the older. |
| **Non-determinism / replay.** | The model call sits behind the bridge's `requestId` + `idempotencyKey`; `RecordCapabilityProposal` collapses on a deterministic proposal id ⇒ redelivery never duplicates. The *artifact content* is non-deterministic (LLM) but **inert until validated + approved + applied**, so non-determinism never reaches state unreviewed. |
| **An AI agent submits the apply op directly (bypassing the human).** | Item 4 — the `identity.ai.*` agent does **not** hold `InstallPackage`/`UpgradePackage`; the Processor capability-denies it at step 3. The apply is **only** submittable by an operator who holds the grant. Adversarial test asserts the denial. |
| **The materializer is itself a new trusted component (a bug here is a new surface).** | The materializer is **deterministic, AI-free**, and runs the *same* `validateAll`/parser/lint the human path runs; it produces an op (P2), never a write. It is heavily unit-tested (§7) and its output is gated by the kernel guard regardless. |
| **AI-authoring becomes a crutch (operators stop authoring real packages thoughtfully).** | It is a *human-gated proposal* tool, not an autonomous author; every capability still passes human review. A recurring low-quality-proposal pattern is a Health-visible signal (a future Lamplighter rule). |

**Alternatives considered:**
- **AI emits whole package Go source** — rejected: Go is *build-time* assembly code (it never runs at
  op-time, `_packages.md`); the runtime artifacts are declarative (cypher/Starlark/specs). Authoring
  declarative *content* (materialized deterministically) is both safer (statically validatable) and truer
  to the model (the AI authors the capability, not the build harness).
- **Give the AI the `InstallPackage` capability directly** — rejected: violates Item 4 (no privileged AI
  actor) and collapses the human gate. The AI proposes; the operator applies.
- **A bespoke AI-authoring apply path** — rejected: the F-004 op + step-8 guard is the *authoritative*
  package-authoring gate already; a second path would duplicate it and weaken the "indistinguishable from
  a hand-authored package" safety property.
- **A new server-side validate-only kernel op** — rejected (§5): the kernel guard already runs at apply
  behind the human gate; a pre-apply kernel pass adds contract surface without adding safety.
- **In-process LLM client (no bridge)** — rejected (Augur §3.3): violates "engines never reach an external
  system", re-implements durable-claim/idempotency the bridge owns.
- **Auto-apply from day one** — rejected: the autonomy boundary is Andrew's call; human-in-the-loop is the
  safe default and authoring is higher-stakes than Augur's op-arrangement (§For-Andrew #1).

---

## 10. Open questions — resolved

- **Is the AI a privileged actor that can author capabilities?** → **No.** Architecture Item 4: ordinary
  `identity.ai.*`, capability-scoped, no bypass. It holds only `RequestCapabilityAuthoring`; the human
  operator applies. (Honored, not re-opened.)
- **Where does the LLM call live?** → The **bridge** (a `capabilityAuthor` adapter), dispatched via
  `triggerLoom → externalTask` — like Augur. Not in-process. (§3.4)
- **What does the AI actually author?** → **Declarative artifact content** (lens cypher / grant tuples /
  Weaver-target + Loom-pattern specs / [gated] Starlark), materialized into an install/upgrade write-set
  by a deterministic, AI-free **materializer**. Not Go source. (§3.2/§3.5)
- **How is the AI prevented from doing harm?** → The §5 four-point boundary culminating in the
  **authoritative kernel step-8 guard at apply**, behind a **human gate**; the AI gains no new authority.
- **How does an approved proposal get applied?** → The **operator** submits the existing **F-004
  `InstallPackage`/`UpgradePackage` op**; no new apply path or kernel op. (§3.5)
- **How is it rolled back?** → **F-004 `upgrade`/`uninstall`** + version-independent keys (FR53 — revert
  via compensating op, no downtime). (§2, §3.5)
- **Which kinds first, and why is Starlark special?** → lens → grant → weaverTarget/loomPattern first
  (fully statically validatable); **Starlark gated** behind the verified-pure sandbox (separate item) +
  ratification (executes on the write path). (§3.2, §8, §For-Andrew #2)
- **Sync or async LLM call?** → **Sync** (the bridge's `Adapter.Execute`); seconds. (§3.4)
- **Which model?** → Model-pluggable adapter; default `claude-opus-4-8` (authoring is intelligence-
  sensitive), sonnet opt-in. Adapter config, not a contract/fork. (§3.6)
- **Frozen-contract change?** → **None anticipated** (§6); the one candidate (a validate-only kernel op)
  is rejected.
- **The autonomy boundary (auto-apply)?** → **Designed (Fire 5) but recommend NOT building**; human-in-
  the-loop is the standing posture. The one autonomy call for Andrew. (§For-Andrew #1)

---

## 11. What lands where

| Path | Change |
|---|---|
| `packages/capability-author/` *(new package)* | the `vtx.capabilityProposal` type DDL; `RequestCapabilityAuthoring`/`RecordCapabilityProposal`/`ReviewCapabilityProposal` ops; the `capability-proposals` review lens; the `capability-author-context` catalog lens; the `capabilityAuthor` externalTask pattern; capability grants (operator → `ReviewCapabilityProposal` + the existing `InstallPackage`/`UpgradePackage`; the `identity.ai.*` agent → `RequestCapabilityAuthoring` only) |
| `internal/pkgmgr/` *(new: materializer)* | the deterministic **capability materializer** (declarative artifact `kind`+`content` → an install/upgrade write-set, reusing `buildManifestBatch`/`Upgrade` + `validateAll` + the openCypher parser + the §5 scope check); per-kind validators wired to the existing `validate*` family |
| `internal/bridge/` (+ registry) | the `capabilityAuthor` adapter (real Claude client, structured output) + a deterministic `FakeCapabilityAuthor` for CI |
| `cmd/loupe/` + `cmd/lattice/` | the proposal review + apply surface (list / inspect report+delta / approve / reject / apply) — reads the `capability-proposals` lens; apply submits the F-004 op under the operator |
| `internal/bootstrap/` | one primordial package add (`capability-author`) + the authoring-agent identity + grant; bootstrap version bump |
| (no change) | **no `docs/contracts/*` edit** — proposal/ops = package DDL, adapter = bridge data, apply = ratified F-004 ops (§6) |
| tests | materializer + §5 validator unit table (per kind, every reject/accept class); faked-adapter e2e (request→record→review→apply per kind); adversarial malicious-artifact DEFENDED (P5-escape lens / over-scope grant / protected-root touch / pre-Fire-4 Starlark / AI-submits-apply-directly) |

---

*Designer fire — Winston. This design is complete and resolved; it awaits Andrew's ratification (and the
two decisions in the For-Andrew block — the authoring autonomy posture, and the artifact-kind/Starlark
boundary) before the Lattice Steward builds it fire-by-fire, build-sequenced behind the Augur.*
