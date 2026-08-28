# Erasure as an orchestrated process — the Loom spine and the Weaver's convergent tail

**Status: ✅ Andrew-RATIFIED 2026-08-06** — build-ready as **two fires**, both in the **Lattice lane**
(§12, rewritten at ratification); the **§5.2 fork resolves to option B** (`StepSpec.Reads`). Authored
2026-08-06 as Andrew's redirect of `credential-binding-plane-lifecycle-design.md` Inc 1: erasure is an
orchestrated process, not a fatter op. **No contract change** (§14) — verified, and nothing staged.

## Ratification (Andrew, 2026-08-06)

**Ratified as designed, with the fork resolved and the decomposition rewritten.**

- **§5.2 fork → option B (`StepSpec.Reads`).** Option A's lazy `kv.Read` would put three new erasure-path
  ops on the losing side of a gate that is actively tightening toward blocking — the wrong footing for a
  legal obligation — while option B extends the shipped `userTaskReads` resolution rather than inventing
  one, and Loom's own comment already anticipates exactly it.
- **Two fires, not five, and the collapse closes a regression window.** The original order narrowed the op
  first on the argument that nothing regresses "that was ever guaranteed". True of the *guarantee*, but
  today's shred really does tombstone an ordinary person's index vertices and `boundTo` links, and the
  narrowing alone would stop doing that until the pattern landed. The cascade now leaves the op and arrives
  in the pattern in **one landing**, with the narrowing sequenced last inside Fire B.
- **Andrew's Loom-plus-Weaver call is vindicated by the DD pass, not merely accommodated.** Loom **cannot
  wait** — a false guard *skips* the step — and `loom.md` states "linear only, no branches, no loops, no
  fan-out. Conditional paths → Weaver." The unbounded convergent tail structurally cannot live in the
  pattern, so the Weaver half is forced rather than optional.
- **The convergence guarantee traces.** An independent probe read `gapSuppressed()` in full and confirmed
  the mechanism §7.2 depends on: merely *declaring* `inflight_<g>` in the row routes past the default
  retry budget of 3, so the sweep really can re-drive to zero rather than dying at three attempts.
- **Both first-of-kind risks land in Fire B and are honestly flagged rather than hedged**: there is **no
  Weaver-driven paged sweep anywhere in the codebase** (R1), and **no shipped Loom pattern uses a `systemOp`
  step at all** (which is also why §5.3's completion-correlation detail is unverifiable from the corpus and
  becomes Fire A's probe). Fire B therefore takes the full 3-layer adversarial review.
- **Option C keeps three preconditions, not two.** §9.2 claims its sequencing is "unchanged" from the
  credential-binding design's §7.4 while dropping that section's *behind the first configured external key
  source* gate. Treated as standing — `.idpBinding` cannot exist before an external key source — unless
  someone argues otherwise on the record.

> **DD pass, 2026-08-06.** An independent probe verified ~40 code claims against current `main`: **36 exact,
> 4 with citation drift, zero substantively false.** Both headline claims are exact to the line, including
> the verbatim comments quoted — the `identityindex` revive-on-next-write (so a shred's tombstone is
> deliberately non-terminal) and the `systemOp` read-free fork. §7.2's whole convergence guarantee traces
> correctly under close reading of `gapSuppressed()`: merely *declaring* `inflight_<g>` in the row routes
> past the default-3 budget, so the sweep really can re-drive to zero. Feasibility confirmed: `systemOp`
> exists as described, **zero shipped Loom pattern uses a `systemOp` step**, and **no Weaver-driven paged
> sweep exists anywhere** — so R1 and R5 are honestly flagged as novel rather than hedged. No premature
> adopter of any erasure name. Corrections folded:
>
> - **The §5.2 fork's precedent is real and exactly shaped.** `userTaskReads` (`internal/loom/engine.go`)
>   returns a Reads set templated off `inst.SubjectKey` and `submitUserTask` passes it into `buildOutbox`,
>   which `submitSystemOp` does not — so Option B extends a shipped pattern rather than inventing one. The
>   "a future systemOp that reads would set its own read-set here" comment is verbatim at the cited site.
> - **Citation drifts** (substance intact in every case): the guard-skip comment is at `engine.go:827`, not
>   `:825`; the "linkage is ownership, so this needs no decryption" phrase is at
>   `shred_identity_key.go:53` (near-variant at `:184`), not `:186-188`; `orchestration-base/opmetas.go`
>   returns **one** entry (`ClaimTask`), not zero, so §5.3's "deliberately empty" should read
>   "deliberately one person-facing op"; and ledger #7's `keyshredded/manager.go:253` is the wrong line —
>   `submitFinalization` is at `:458`, `handleKeyShredded` at `:296`.
> - **§9.2 silently drops a gating clause.** It says the option-C sequencing is *"unchanged from the
>   credential-binding design's §7.4"*, but §7.4 gates C **behind the first configured external key
>   source** as well. Either that clause is obsolete post-redirect — in which case say why, since
>   `.idpBinding` still cannot exist before an external key source — or it stands and C has **three**
>   preconditions, not two. Treated as standing until argued otherwise.

**Author:** Winston (Designer fire, 2026-08-06) · **Owners:** privacy-base, identity-domain, internal/loom, internal/weaver
**Supersedes:** `credential-binding-plane-lifecycle-design.md` §3 (Increment 1) and §7's A/B/C fork.
**Size: L** (5 increments; Inc 1 S, Inc 2 M, Inc 3 M, Inc 4 M–L, Inc 5 S). No frozen-contract change.

---

## For Andrew

**What it does.** `ShredIdentityKey` goes back to shredding the key — one mutation, one event, always,
for every person regardless of how connected they are. Everything it currently does *besides* that
(three paged link enumerations, tombstoning index vertices, `duplicateOf` pairs and `boundTo` edges)
becomes an **orchestrated erasure**: a Loom pattern for the ordered spine, a Weaver convergence target
for the unbounded tail. The thing that makes this more than a refactor is the **seal**: an
`vtx.identity.<id>.erasure` attestation aspect that can only be written by an op the Weaver dispatches
when the residue lens reads zero across every class *and* both async finalizations have landed. Today
the erasure emits a success event first and hopes; under this design the success signal is
structurally downstream of verified coverage.

**Your redirect is right, and the code is harsher than the brief said.** Three facts I verified this
fire that were not in evidence when Inc 1 was written. (1) The batch cap really can refuse a person:
`total_muts > 999` ⇒ `ShredBatchTooLarge` (`shred_identity_key.go:328-330`) — a right-to-erasure
obligation with a size limit on who qualifies. (2) Worse, **erasure has no write-path closure at all**:
`index_vertex_mutation` (`packages/identity-domain/ddls.go:582-595`) *deliberately revives* a
tombstoned `identityindex` on a CAS-guarded update, with a comment naming the shred as the reason it
must — so the next contact write for an erased person silently reopens the index the shred just
tombstoned. The erased set is not merely incompletely enumerated; it is **not closed**. (3) The
in-batch cascade's own stated justification is stale: the script argues erasing in-commit is *"the
only decrypt-free window that exists"* (`shred_identity_key.go:60-67`), but its own enumerations are
link walks — *"linkage IS ownership, so this needs no decryption"* (`:186-188`). Link walks work
identically after the DEK dies. **Nothing about the current coupling is load-bearing.**

**One genuine fork for you (§5.2), and it is a platform question, not a privacy one.** Loom's
`systemOp` step submits a payload hardcoded to `{subjectKey}` with **`ContextHint.Reads` nil**
(`internal/loom/engine.go:862-867`). Every erasure step op needs the identity root as a declared read
for its target-existence guard. So either (**A**) the step ops read lazily via `kv.Read` — which is
exactly the class-(b) debt CLAUDE.md forbids new scripts from taking on — or (**B**) Loom's `StepSpec`
learns a `Reads []string` field templated off the subject, a small, obviously-correct platform
increment that every future systemOp pattern needs anyway. **I recommend B**, and I have sized it as
its own increment rather than smuggling it into a privacy fire. There is no third option: today Loom
has no shipped systemOp step that reads anything, because the only two systemOps in existence are
event-only lifecycle ops (`packages/orchestration-base/loom_lifecycle.go:109-149`).

**"Plus Weaver for convergence if necessary" resolves to *necessary*, provably.** Loom is *"Linear
only — no branches, no loops, no fan-out"* (`docs/components/loom.md:57`), and a false guard **skips**
a step rather than waiting on it (`internal/loom/engine.go:825-827`, verbatim). So Loom structurally
cannot page a data-dependent link set, and cannot wait for the two async finalizations. Weaver's
re-projection cycle is the codebase's only iteration idiom, and one shipped mechanism makes it work
uncapped: a `directOp` gap that declares an `inflight_<g>` column and no `maxretries_<g>` has **no
retry cap** — *"no usable cap → the budget term never suppresses"* (`internal/weaver/evaluator.go:875-881`).
That is what lets a sweep re-drive until the residue count reaches zero instead of dying at the
default budget of 3.

**The honest size is L, and one increment is bigger than it looks.** `UnlinkCredential` cannot be
reused as the unbind step: it takes its target from `op.actor` (`packages/identity-domain/ddls.go:1495`),
carries a `last-credential` refusal (`:1544-1545`), and is granted `Scope: "self"` to `consumer` only
(`packages/identity-domain/permissions.go:95-99`). An orchestrator submits under its own service actor,
so the op would resolve to *Loom's* identity and fail every time. Inc 3 therefore writes a new
system-dispatchable unbind, and that is where the weight is. **Old option C becomes a step** rather
than a refusal (§9) — and I state plainly in §9.2 that erasure-completeness **is** the real revive
trigger for the shelved hard-delete verb, because a tombstoned
`lnk.identity.<credId>.boundTo.identity.<uId>` names both parties **in the key itself**, forever. I do
not design hard-delete here.

---

## 1. Grounding ledger

Every load-bearing claim, verified this fire by reading the cited lines. No build, no tests, no stack.

| # | Claim | Evidence | Verdict |
|---|---|---|---|
| 1 | `ShredIdentityKey` runs **three paged enumerations** in the shred's own commit | `packages/privacy-base/shred_identity_key.go:317-319`; helpers `:181-202` (`indexes`), `:207-230` (`duplicateOf`, both directions), `:235-265` (`boundTo`, both directions) | ✅ |
| 2 | Each enumeration is capped at 64 pages × 256 and **fails the whole op** past that | `:178-179`, `:202`, `:204-205`, `:222`, `:232-233`, `:249` | ✅ three more refusal modes beyond the cap |
| 3 | **The op can refuse to erase**: `total_muts = 1 + 2·idx + dup + bound`, `> 999` ⇒ `ShredBatchTooLarge` | `:328-330` | ✅ the structural argument |
| 4 | The op's mutation set is piiKey + per-index (vertex + link) + per-dup + per-bound | `:309-311`, `:332-342`, `:344-347`, `:349-351` | ✅ |
| 5 | A re-shred **resets the finalization cycle** — cycle semantics already exist | `:292-298` (`for stale in [...]: data.pop(...)`) | ✅ |
| 6 | The DDL already admits `RecordShredFinalization{identityKey, step}`; handler flips one boolean + `At` | `:93`, `:356-389`, flip at `:383-384` | ✅ the hand-rolled pattern |
| 7 | Two independent async actors submit it: privacy-worker after `Vault.ShredKey`; Refractor after nullify targets | `internal/privacyworker/manager.go:145-165`, `:198-221`; `internal/refractor/keyshredded/manager.go:79-81`, `:253` | ✅ |
| 8 | Both submit under the **primordial privacy service actor**, publish-then-ack, deterministic requestId | `privacyworker/manager.go:155-165`, `:207-208`; actor `internal/bootstrap/primordial.go:476` (`identity.system.privacy`) | ✅ |
| 9 | `shredStatus` lens projects one row per shredded identity with both progress booleans; **pure visibility, not a correctness mechanism** (its own words) | `packages/privacy-base/lenses.go:24-27`, spec `:79-89` | ✅ the attestation gap |
| 10 | Key destruction is **async, after commit** — the op records intent only | `shred_identity_key.go:20-26`; `privacyworker/manager.go:1-8` | ✅ ordering is already decoupled |
| 11 | **The in-batch rationale is stale**: enumerations are link walks needing no decryption, yet the doc claims in-commit is *"the only decrypt-free window that exists"* | claim `:60-67` vs. `:186-188` (*"linkage IS ownership, so this needs no decryption"*) | ✅ **decoupling is safe** |
| 12 | **The erased set is not closed**: a tombstoned `identityindex` is deliberately **revived** by the next write for the same contact | `packages/identity-domain/ddls.go:582-595`, revive at `:593-594`, reason comment `:582-590` | ✅ the sharpest finding |
| 13 | No identity-domain op gates any write on `piiKey.shredded` | grep `shredded` over `packages/identity-domain/*.go` → one comment (`ddls.go:582`), zero guards | ✅ write path is open |
| 14 | The enumerations **accept** a concurrent create slipping past — *"accepted, same posture as…"* | `shred_identity_key.go:192-196`, `:212-215`, `:239-242` | ✅ ledger #12's runtime twin |
| 15 | Loom is **linear only — no branches, no loops, no fan-out** | `docs/components/loom.md:57` | ✅ Weaver tail is forced |
| 16 | A **false guard SKIPS the step**, it does not wait | `internal/loom/engine.go:825-827` (verbatim: *"Guard false → skip this step (no task, no op, no token, no outbox)"*) | ✅ no "await finalization" step is expressible |
| 17 | Guard paths are only `subject.data.<f>` / `subject.<aspect>.data.<f>` — **no link walking** | `internal/loom/guard.go:403-426`; rationale `docs/contracts/10-orchestration-loom.md:150` | ✅ residue detection cannot be a guard |
| 18 | A `systemOp` step submits payload **hardcoded** to `{subjectKey}`, authContext target = the **pattern meta-key**, and **`Reads` nil** | `internal/loom/engine.go:861-867`; signature `internal/loom/actuator.go:134` | ✅ **the fork (§5.2)** |
| 19 | The step op is submitted under Loom's own service actor, required non-empty | `engine.go:867` (`e.cfg.ActorKey`), `:244-245`; actor `bootstrap/primordial.go:452` (`identity.system.loom`) | ✅ steps need `Scope:"any"` grants |
| 20 | Steps are one of exactly three kinds; a 4th fails the whole pattern load | `internal/loom/pattern.go:28-48`, validate `:190-239` | ✅ no `forEach` kind to add cheaply |
| 21 | A pattern is a `meta.loomPattern` meta-vertex + `.spec` aspect, installed by pkgmgr | `internal/pkgmgr/build.go:203-208`, classes `:19-22` | ✅ package-declared, CDC-loaded |
| 22 | A Loom **instance is operational state only** — no Core-KV vertex | `packages/orchestration-base/loom_lifecycle.go:24-26`; `internal/loom/state.go:26-30` | ✅ attestation cannot live on the instance |
| 23 | Lifecycle vocabulary is exactly `StartLoomPattern` / `CompletePattern` / `FailPattern`; all **event-only** | `packages/orchestration-base/loom_lifecycle.go:37-39`, scripts `:109-149` | ✅ no "define pattern" runtime op |
| 24 | A re-fired `StartLoomPattern` with the **same** instanceId on a terminal/in-flight instance is a **no-op Ack** | `internal/loom/engine.go:410-421` | ✅ answers "re-triggered mid-erasure" |
| 25 | A failed instance is recovered only by operator `RedriveInstance` (resumes at the recorded cursor) or a genuinely new gap episode | `internal/loom/control.go:281-357`; `internal/weaver/actuator.go:161-171` | ✅ |
| 26 | `RetryCount` is incremented but **never read** — an observability counter, not a policy knob (contract describes a re-submit branch the code does not have) | `engine.go:1096`; contract `docs/contracts/10-orchestration-loom.md:235-237` | ⚠️ **doc/code divergence, pre-existing** |
| 27 | Gap columns **must** be `missing_<gap>`, enforced at install | `internal/pkgmgr/orchestrationguard.go:16-19`, `:97-105` | ✅ |
| 28 | **A `directOp` gap declaring `inflight_<g>` and no `maxretries_<g>` is UNCAPPED**; a row-declared `maxretries_<g>` always beats the engine default of 3 | `internal/weaver/evaluator.go:875-881`, `:851` | ✅ **the convergence mechanism** |
| 29 | Weaver dispatches under **its own** fixed service actor; `authTarget` only fills `AuthContext.Target` | `internal/weaver/actuator.go:91`, `:101-102`; `internal/weaver/engine.go:308` | ✅ so must the erasure ops be `Scope:"any"` |
| 30 | `UnlinkCredential` takes its target from **`op.actor`**, refuses the **last credential**, and is `Scope:"self"` / `consumer` | `packages/identity-domain/ddls.go:1495`, `:1544-1545`; `packages/identity-domain/permissions.go:95-99` | ✅ **not reusable by an orchestrator** |
| 31 | `UnlinkCredential` is nonetheless the correct *shape*: index + `boundTo` + array rewrite + `identity.unbound` | `ddls.go:1553-1570` | ✅ copy the body, not the entry point |
| 32 | `identity.unbound` is the **one** fold that DELETEs a `credential-bindings` bucket row | `ddls.go:1567-1570`; materializer `internal/gateway/credential_bindings_materializer.go:143-161` | ✅ the Gateway seam's only retraction |
| 33 | `credentialindex` is `vtx.credentialindex.<hash(actorKey)>` carrying **plaintext** `{actorKey, identityKey, boundAt}` | key `ddls.go:764-765`; body `:597-610` | ✅ |
| 34 | **`credentialindex` has no link to its identity** — no `credentialindex` linkType DDL exists, unlike `identityindex`'s `indexes` | `identityindex`'s linkType `ddls.go:458-482`; no counterpart in `DDLs()` `:50-541`; bulk access is a raw keyspace prefix scan in a CLI, `cmd/lattice/identity/reconcile.go:153` | ✅ **option C's precondition, unchanged** |
| 35 | `boundTo` is `lnk.identity.<credId>.boundTo.identity.<ownerId>` — credential is source | `ddls.go:770-778`, `:510-512` | ✅ §1.1-correct |
| 36 | `privacy-base` declares **no** `LoomPatterns` and **no** `WeaverTargets` today | `packages/privacy-base/package.go:25-32` | ✅ greenfield |
| 37 | A convergence lens shaped *"count the live edges; zero means done"* is shipped | `packages/objects-base/lenses.go:99-113` (`OPTIONAL MATCH` + `count(owner.key)`) | ✅ the residue lens is expressible |
| 38 | **…and adjacency counting LAGS the commit** — objects-base moved to an atomically-maintained counter after a lag-driven data-loss bug | `packages/objects-base/lenses.go:60-74` | ✅ **the adversarial trap (§7.3)** |
| 39 | Weaver planner (`Goal`/`Actions`/`Pre`/`Effects`/`Cost`) can express ordering, and iterates one action per episode | `packages/lease-signing/renewal_targets.go:77-129`; specs `internal/pkgmgr/definition.go:387-401` | ✅ the Loom-less alternative, rejected §8.3 |
| 40 | **No precedent exists** for a Weaver-driven bounded batch/paged sweep; every shipped `directOp` is one op per one row | census of `packages/*/targets.go`; closest rejections at `packages/wellness-domain/ddls.go:352-368` | ⚠️ **this design is the first** |

**Not verified.** I ran no build, no tests and no stack (design-only fire). I did **not** verify the
completion-event correlation path end to end for a `systemOp` whose bound op emits an event in the
pattern's `completionDomains` — §5.3 flags the one detail the build must confirm before Inc 4 is
sized. Ledger #26 (Loom's unread `RetryCount`) and #40 are reported from a sub-agent's census that I
spot-checked at the cited lines but did not exhaustively re-run. I did not census out-of-repo
consumers of `shredStatus`.

---

## 2. What is wrong today, stated once

Erasure has four defects, and they are one defect wearing four hats: **the op is the whole process.**

1. **It can refuse.** Four independent refusal modes — the 999-mutation cap (#3) and three
   fanout-too-large failures (#2). A person with too many links cannot be erased at all.
2. **It cannot attest.** The event fires on commit. `shredStatus` says so itself: *"pure visibility
   … not a correctness mechanism"* (#9). Nothing anywhere says *"this person's erasure is complete."*
3. **It is not closed.** The write path revives a tombstoned index by design (#12), and the
   enumerations accept concurrent creates slipping past (#14). Erasure is a moment, not a state.
4. **It is unrepairable.** If an enumeration missed something, no mechanism will ever notice. This
   is exactly `retention-class-key-custody-design.md` §8.6's objection — *"a missed edge is a silent,
   permanent erasure failure with a success signal on it"* — and it lands on the **shipped** op, not
   just on the cascade §8.6 was rejecting.

Decomposition alone fixes (1). It is (2), (3) and (4) that require the orchestration, and (3) that
requires a write-path change nobody has proposed before.

---

## 3. The shape

```
"forget me"
   │
   ▼
 StartLoomPattern{identityErasure, subject=vtx.identity.<U>}      ← operator / app / Weaver
   │
   ├─ LOOM SPINE (ordered, once, durable instance, deadline-backstopped)
   │    step 1  ShredIdentityKey        → piiKey.shredded, privacy.keyShredded   [the legal obligation, first]
   │    step 2  SealIdentityForErasure  → identity.erasureRequested aspect        [closes the write path]
   │    step 3  UnbindIdentityCredentials → one bounded page of credential unbinds
   │    step 4  PurgeIdentityDedupFootprint → one bounded page of index/dup purges
   │
   ├─ ASYNC ATTESTATIONS (unchanged, already durable + idempotent)
   │    privacy.keyShredded ─┬→ privacyworker  → Vault.ShredKey → RecordShredFinalization{vaultKeyDestroyed}
   │                         └→ keyshredded    → nullify targets → RecordShredFinalization{projectionsNullified}
   │
   └─ WEAVER CONVERGENCE (uncapped, until zero)
        lens  identityErasureResidue  → one row per erasure-requested identity, per-class residue counts
        gaps  missing_credentialResidue → directOp UnbindIdentityCredentials   (next page)
              missing_dedupResidue      → directOp PurgeIdentityDedupFootprint (next page)
              missing_vaultDestruction  → surface  (a stuck async half, escalated not swept)
              missing_projectionNullify → surface
              missing_erasureSeal       → directOp SealIdentityErasure  ← ONLY when every other gap is closed
```

**The division of labour, and why it is not arbitrary.**

- **Loom owns the ordered spine** because the ordering is a legal obligation: key destruction is the
  primary duty and must be initiated first, structural cleanup follows. Loom gives that a durable
  instance with a cursor, a per-step deadline backstop, a terminal failed state, an operator
  `RedriveInstance`, and — decisively — a *declared, readable* order. A listener chain has an order
  too, but it is implicit in wiring and invisible to an auditor.
- **Weaver owns the convergent tail** because the tail is unbounded and data-dependent, and Loom
  provably cannot express it (#15, #16, #17, #20). Weaver's re-projection cycle is the codebase's
  only iteration idiom, and #28 is what makes it terminate on the right side.
- **The async attestations stay exactly where they are.** This is the point the brief asked me to get
  right: the design **generalizes** the hand-rolled pattern instance rather than running a second
  mechanism beside it. `RecordShredFinalization`, the two listeners and `shredStatus` are already
  durable, idempotent, cycle-aware (#5, #6, #7). What they lacked was an orchestrator that *reads*
  them. Under this design the residue lens reads both booleans as residue classes, so a stuck async
  half becomes a Weaver-surfaced issue instead of a row an operator has to notice. Loom does not
  wait on them because it cannot (#16) — and it does not need to: the **seal** waits on them.

---

## 4. The op's new scope

### 4.1 What `ShredIdentityKey` keeps

Exactly the key, and exactly the trigger:

- the `piiKey` envelope write — real envelope flipped to `shredded=true`, or the durable
  empty-`wrappedDEK` placeholder when the identity never received a sensitive write. **This stays,
  unchanged and load-bearing**: `shred_identity_key.go:28-40` explains why (the LocalBackend deny-list
  is per-process, so without the durable placeholder a post-restart sensitive write mints a fresh
  unshredded key). Nothing in decomposition touches that argument.
- `shreddedAt`, and the finalization-cycle reset (#5) — cycle semantics survive verbatim.
- the `privacy.keyShredded` event.
- the target-existence guard (`vertex_alive`, `:275-276`) and `parts_of` validation.

**Mutation count after Inc 1: exactly 1, always, for every identity in the corpus.** That is how the
cap is retired (§10).

### 4.2 What moves out, and why none of it may stay

All three enumerations and every mutation they drive move to steps. The brief asked me to be explicit
about whether any should stay. **None should, and the reason is uniform**: each enumeration carries
its own unbounded-fanout refusal (#2), so leaving even one in the op leaves the op able to refuse a
person. A cap retired for two of three classes is not retired.

The three specific arguments for keeping them, and why each fails:

- *"It is the only decrypt-free window."* **False** (#11). The enumerations are link walks —
  the script says so itself at `:186-188`. Links stay walkable after the DEK dies. This is the
  in-batch design's own stated justification and it does not survive reading.
- *"Atomicity — a partial erasure is worse than none."* Inverted. Today a partial erasure is
  *impossible to detect*; under this design partial is the **normal intermediate state**, it is
  visible in the residue lens, and it converges. Atomicity is only a virtue when the alternative is
  undetectable drift.
- *"Idempotence — re-shred finds nothing."* Preserved and strengthened: every step op is idempotent
  by tombstone-already-set, and the residue lens makes a re-run *provably* a no-op rather than
  assumedly one.

### 4.3 What is added to the op: nothing

`ShredIdentityKey` gains no payload field, no read, no event. It gets smaller. `RecordShredFinalization`
is untouched.

---

## 5. The Loom pattern

### 5.1 Declaration

Declared in `packages/privacy-base` (which ships neither patterns nor targets today, #36), as
`pkgmgr.LoomPatternSpec` (`internal/pkgmgr/definition.go:406-420`), installed as a
`meta.loomPattern` meta-vertex + `.spec` aspect (#21).

```
PatternID:         "identityErasure"
SubjectType:       "identity"
CompletionDomains: ["privacy"]
Steps: [
  1 {Kind:"systemOp", Operation:"ShredIdentityKey",
     Guard: {"not": {"equals": {"path":"subject.piiKey.data.shredded", "value": true}}}}
  2 {Kind:"systemOp", Operation:"SealIdentityForErasure",
     Guard: {"absent": "subject.erasureRequested.data.requestedAt"}}
  3 {Kind:"systemOp", Operation:"UnbindIdentityCredentials"}
  4 {Kind:"systemOp", Operation:"PurgeIdentityDedupFootprint"}
]
```

Every guard path is `subject.<aspect>.data.<field>` — the only shape the grammar admits (#17). Steps 3
and 4 are deliberately **guardless**: their idempotence is by-tombstone, and a guard that could not
see the link plane anyway (#17) would be theatre. Per `docs/contracts/10-orchestration-loom.md:381-385`
a guardless step's cost on total `loom-state` loss is one bounded, alerted re-run — for an
already-idempotent tombstone sweep, exactly the right trade.

**Step ordering is the legal obligation, and it is now declared rather than implied.** Step 1 first:
key destruction is the primary duty, and it also arms the async half. Step 2 second and this is the
new one — see §6. Steps 3–4 are structural cleanup, correctly downstream.

### 5.2 THE FORK — Loom's systemOp cannot declare reads

`submitSystemOp` builds `payload := {"subjectKey": inst.SubjectKey}`, targets `pattern.MetaKey`, and
calls `buildOutbox(..., nil, nil, nil)` — `reads`, `optionalReads`, `egressReads` all nil
(`internal/loom/engine.go:861-867`; signature `internal/loom/actuator.go:134`). Its own comment admits
the gap: *"A systemOp's bound op is read-free in the Phase-2 vocabulary … A future systemOp that reads
would set its own read-set here from the step's known target."* That future is this design.

Every step op above needs the identity root declared (the `vertex_alive` target-existence guard), and
steps 1 and 2 need `subject.piiKey` as an optional read.

**Option A — step ops read lazily via `kv.Read`.** No platform change. But an unannotated `kv.Read` in
a `packages/` script is precisely the class-(b) debt CLAUDE.md tells new scripts not to take on, and
`lint-conventions` warns on it today with the gate flipping to blocking. It would put the platform's
most correctness-critical op family on the wrong side of a gate we are actively sweeping toward.

**Option B (recommended) — `StepSpec.Reads []string` / `OptionalReads []string`, templated off the
subject.** Add the fields to `pkgmgr.StepSpec` (`definition.go:426-458`), carry them through
`loomPatternSpecBody` (`internal/pkgmgr/build.go:856-889`) and `loom.Step`
(`internal/loom/pattern.go:28-48`), validate them in `pattern.go:190-239` (systemOp-only; each entry
either the literal `subject` token or `subject.<aspect>`), and resolve them against `inst.SubjectKey`
in `submitSystemOp`. The `userTask` arm already does exactly this resolution
(`engine.go:928-978` + `userTaskReads`), so the shape is precedent, not invention.

**Recommendation: B.** It is small, it is the mechanism Loom's own comment anticipates, every future
systemOp pattern needs it, and A would trade a permanent lint exemption on the erasure path for a
week of schedule. Cost is not grounds to take the wrong shape.

**Andrew's call.** If you pick A, Inc 2 shrinks and the design still works — say so and I will restate
§5.1's steps with declared `# read-posture: (b)` annotations and file the debt row.

### 5.3 Authorization, and one detail the build must confirm

A systemOp is submitted under `identity.system.loom` (#19) with `AuthContext.Target` = the **pattern's
meta-key** (#18), not the subject. So each step op needs a `Scope: "any"` permission granted to the
loom service actor — the same posture `StartLoomPattern` already carries for Weaver
(`docs/contracts/10-orchestration-weaver.md:213-226`). This is a real widening and §11 R2 owns it:
these ops erase, and a `scope:any` grant on an erasure verb must be justified by the fact that the
*only* submitters are the two service actors, with no descriptor and no person-facing affordance
(`OpMetas` deliberately empty, per `packages/orchestration-base/opmetas.go:32-60`'s posture).

**Unverified (#, "Not verified").** Loom advances a step on a completion event correlated to the
pending token, over the pattern's `completionDomains`. I did not trace the correlation end to end for
a `systemOp` whose bound op emits its own domain event, because no shipped systemOp emits one (#23 —
both lifecycle ops are event-only with empty mutations). **Inc 2 must confirm this before Inc 4 is
sized**; if a systemOp advances on op-commit rather than a domain event, `CompletionDomains` may be
irrelevant here and the declaration simplifies. This does not change the architecture — only the
`CompletionDomains: ["privacy"]` line.

### 5.4 Step semantics

| Step | Op | Owner | Mutations (bounded) | Idempotent? | Emits |
|---|---|---|---|---|---|
| 1 | `ShredIdentityKey` | privacy-base | **1**, constant | yes — re-shred rewrites the envelope and resets the cycle (#5) | `privacy.keyShredded` |
| 2 | `SealIdentityForErasure` | privacy-base | **1**, constant | yes — unconditioned upsert of `.erasureRequested` | `privacy.erasureRequested` |
| 3 | `UnbindIdentityCredentials` | identity-domain | ≤ `2·PAGE + 1` | yes — already-tombstoned rows are skipped | one `identity.unbound` per credential unbound |
| 4 | `PurgeIdentityDedupFootprint` | privacy-base | ≤ `2·SWEEP_LIMIT` (see below) | yes — same | none |

`PAGE = 256`, matching the existing enumeration page limits (`shred_identity_key.go:178`, `:204`,
`:232`). **No step can exceed its bound**, so no step can reintroduce a refusal (§10).

**Amended by increment 3: the COMMIT bound is `SWEEP_LIMIT = 64`, not `PAGE`.** `PAGE` remains the
`kv.Links` read page, where it came from; a sweep's *mutation* count is a separate, smaller constant
because it sizes an atomic batch on every pass and `2·PAGE` mutations exceeds the 250ms Starlark
wall budget under load. Step 3's bound is therefore `≤ 2·SWEEP_LIMIT + 1 = 129`; step 4's should be
derived the same way when it is built. See §10 point 4.

**Derived by increment 4: step 4's bound is `2·SWEEP_LIMIT = 128`, one class per commit.** The three
classes do not cost the same — an `indexes` hit is **two** mutations (the index vertex *and* the
link) where a `duplicateOf` hit is one — so draining all three collectors in one commit reaches
`4·SWEEP_LIMIT`. Sweeping one class per commit, `indexes` → `duplicateOf` out → `duplicateOf` in,
caps any commit at the magnitude step 3 measured clean. This is why the original `≤ 3·PAGE` was
wrong in both directions: it under-counted the per-hit cost and over-counted the batch.

**Step 3 is new work, not a rename.** Per #30 `UnlinkCredential` is unusable by an orchestrator on
three counts. Step 3's op takes the owner from `subjectKey`, carries **no** last-credential guard (a
person being erased keeps no sign-in path — that is the point), and is granted `Scope:"any"` to the
service actors. It copies `UnlinkCredential`'s **body** (#31) — tombstone `credentialindex`, tombstone
`boundTo`, rewrite the `credentialBinding.credentials` array, emit `identity.unbound` so the Gateway
seam's one retraction fold fires (#32). That last emission is what makes this design realise old
option B's full intent (§9.1).

Reads: the `boundTo` enumeration is class **(e)** (bounded `kv.Links`, declared in
`ContextHint.Enumerations`) and each `credentialindex` is a **data-derived follow-up key off that
enumeration** — the same class-(e) posture the shipped script already declares at
`shred_identity_key.go:336-337`. No new read class is introduced.

### 5.5 Failure, partial failure, and a re-triggered "forget me"

- **A step's op is rejected or lost.** The per-step deadline probe reads the Processor's op-status
  responder: committed ⇒ advance; outbox still present ⇒ re-arm; neither ⇒ `fail()` — terminal
  (`internal/loom/engine.go:1190-1246`, `:1093-1115`). #26's doc/code divergence means there is **no
  automatic in-instance retry**; I do not rely on one.
- **A terminally failed instance does not strand the erasure.** This is the design's most important
  failure property: the Loom spine is an *accelerator*, not the guarantee. If step 3 dies, the
  residue lens still shows credential residue, the Weaver still dispatches `UnbindIdentityCredentials`
  every reconcile until it reaches zero (#28), and the seal still refuses to be written. **A dead
  spine degrades erasure from prompt to eventual, never from complete to incomplete.**
- **Operator recovery** is `RedriveInstance` at the recorded cursor (#25).
- **Re-triggered "forget me" mid-erasure.** Same `instanceId` ⇒ no-op Ack (#24) — correct, the
  erasure is already running. A genuinely new request ⇒ a new instance from cursor 0; step 1's guard
  is false (already shredded) so it **skips** (#16) — and this is where the seal's cycle semantics
  matter: the seal records `sealedForShreddedAt`, and the residue lens reopens `missing_erasureSeal`
  whenever `seal.sealedForShreddedAt <> piiKey.shreddedAt` (§7.2). A re-shred bumps `shreddedAt`,
  invalidating the old seal by field-diff without needing to tombstone it. The async cycle reset
  (#5) already re-drives both finalizations off the new event.
- **Re-shred after a completed erasure** behaves identically and converges to a fresh seal.

---

## 6. Closing the write path — the increment nobody proposed

**This is the finding that changes the design's shape.** Ledger #12: `index_vertex_mutation`
(`packages/identity-domain/ddls.go:582-595`) revives a tombstoned `identityindex` on purpose, and its
comment names the shred as the reason it must. Ledger #14: the enumerations accept concurrent creates
slipping past. Ledger #13: nothing gates a write on `shredded`.

So today the erased set is **open**: it can grow after erasure. Under the current op that is a latent
bug nobody can see. Under *this* design it would be fatal, for a precise reason — **a detector over an
open set cannot prove completeness.** If a `boundTo` can be created after the residue count reads
zero, then "zero" means "zero at projection time", and the seal would be exactly the
success-signal-on-a-silent-failure that §8.6 rejects. The convergence guarantee requires the residue
set to be **monotonically non-increasing** after step 2.

**Step 2, `SealIdentityForErasure`, is what makes it so.** It writes one aspect:

```
vtx.identity.<U>.erasureRequested   { requestedAt, shreddedAt }
```

and Inc 3 teaches every writer of the four erasable representations to fail closed against it:

| Writer | Gate | Positions gated |
|---|---|---|
| `ClaimIdentity` | reject | the claimed identity **and** the submitting credential |
| `CompleteCredentialLink` | reject | the target identity **and** the submitting credential |
| `ReconcileCredentialBinding` | reject — it already refuses a tombstoned index; this is the same judgement one step earlier | the owner **and** the credential |
| `CreateUnclaimedIdentity` | do not treat a sealed identity as a dedup **incumbent** — no `duplicateOf` may name it | the incumbent behind each contact hit |
| `MergeIdentity` (identity-hygiene) | reject | primary **and** secondary |

*(Corrected at build, increment 2. This table originally named the fifth row `index_vertex_mutation`
callers / "do not revive"; the revive is not the reachable hazard — the `duplicateOf` a **live** hit
produces is. It also named only the target position on the first three rows, which is narrower than what
`ShredIdentityKey` erases: its `collect_bound_to_links` enumerates both directions, so an erased identity
must be gated as the credential as well as the owner. See the increment-2 build note.)*

Each gate is one `optionalReads`-declared read of `subjectKey + ".erasureRequested"` — read-posture
class **(d)**, the same class `ShredIdentityKey` already declares for `piiKey`
(`shred_identity_key.go:286-288`). Fail-closed: absent aspect ⇒ not erased ⇒ allow (the aspect's
presence is the only signal, and it is only ever created, never removed).

**Why a separate aspect rather than reading `piiKey.shredded`.** Three reasons. (1) `piiKey.shredded`
means *the key is dead*; erasure-requested means *this person is being forgotten* — conflating them
would make every future retention-class key shred (`retention-class-key-custody-design.md`) accidentally
close a person's write path. (2) The seal needs `shreddedAt` copied for the cycle diff (§5.5) and
`piiKey` is privacy-base-owned while the gates live in identity-domain — an explicit contract aspect is
the cleaner seam. (3) It gives the residue lens a single, cheap anchor predicate (§7.1).

**Honest cost.** This is five gates across two packages and it is the largest single piece of Inc 3.
It is also non-negotiable: without it §7's convergence proves nothing.

---

## 7. Residue and convergence — the guarantee

### 7.1 The lens

`identityErasureResidue`, a new `privacy-base` lens into a new `privacy-erasure` NATS-KV bucket
(P5: this is a read model; Loupe and any operator tool read the bucket, never Core KV). One row per
**erasure-requested** identity — the anchor predicate, which is why §6's aspect exists.

```
MATCH (i:identity)
WHERE i.erasureRequested.data.requestedAt <> null
OPTIONAL MATCH (c)-[:boundTo]->(i)
OPTIONAL MATCH (x)-[:indexes]->(i)
WITH i.key AS entityKey,
     i.erasureRequested.data.shreddedAt        AS requestedForShreddedAt,
     i.erasure.data.sealedForShreddedAt        AS sealedForShreddedAt,
     i.piiKey.data.vaultKeyDestroyed           AS vaultKeyDestroyed,
     i.piiKey.data.projectionsNullified        AS projectionsNullified,
     count(c.key) AS boundResidue,
     count(x.key) AS indexResidue
RETURN
  entityKey AS key, entityKey,
  boundResidue, indexResidue,
  (boundResidue > 0)                 AS missing_credentialResidue,
  (indexResidue > 0)                 AS missing_dedupResidue,
  (vaultKeyDestroyed <> true)        AS missing_vaultDestruction,
  (projectionsNullified <> true)     AS missing_projectionNullify,
  (sealedForShreddedAt <> requestedForShreddedAt) AS missing_erasureSeal,
  256 AS maxretries_credentialResidue,
  256 AS maxretries_dedupResidue,
  ... AS violating
```

> **AMENDED 2026-08-24 — the sketch above projected `false AS inflight_<g>` for the residue gaps.**
> A constant-false marker suppresses nothing while making `gapSuppressed` decline the engine's default
> `directOp` retry budget, so those gaps ended up with no cap at all and `GapBudgetExhausted` structurally
> unreachable — Contract #10's "a loud stop, never a silent park" could not hold on this path. The shipped
> lens declares `maxretries_<g>` sized to the sweep's own reach instead, and declares no marker; the
> companion pair is now install-gated. See
> [weaver-gap-companion-pair-validation-design.md](weaver-gap-companion-pair-validation-design.md) §3.1.

Shape grounded in `packages/objects-base/lenses.go:99-113` (the shipped `OPTIONAL MATCH` +
`count(x.key)` residue idiom, #37) and `packages/lease-signing/lenses.go:646-647` (multi-count
aggregate rows). `= null` / `<> null`, never `IS NULL` — the engine's convention
(`objects-base/lenses.go:95-96`). Gap columns are `missing_<gap>` as enforced (#27).

`duplicateOf` residue folds into `missing_dedupResidue` and is swept by the same op; it gets no
separate count only because `duplicateOf` and `indexes` are cleared by one op and a second count would
buy nothing.

> **FALSIFIED by increment 4 — the lens increment must not build the spec above.** That justification
> rests on one commit clearing both classes. It does not: the sweep op takes **one class per commit**
> (the bound derived in §5.4), `indexes` first and `duplicateOf` only once `indexes` is exhausted. So
> a subject with 2 `indexes` links and 500 `duplicateOf` links has `indexResidue = 0` after the first
> pass — `missing_dedupResidue` closes, the Weaver stops dispatching, and 500 live `duplicateOf` links
> naming an erased person survive with a seal written over them. The Loom step is a single `systemOp`
> and does not cover the tail either. **The lens must count `duplicateOf` in both directions** — either
> folded into `dedupResidue` alongside `indexResidue`, or as its own `missing_duplicateResidue` gap
> dispatching the same op. This is now a **three-way** `OPTIONAL MATCH` fan-out on the dedup side
> alone, which is the R5 shape and inherits its `count(DISTINCT CASE WHEN …)` fallback. *(If the engine cannot carry three `OPTIONAL MATCH` fan-outs in one row without a
cartesian blow-up — the `count(DISTINCT CASE WHEN …)` idiom at `lease-signing/lenses.go:646` exists
precisely for that — the build collapses them into that form. Flagged, not assumed.)*

### 7.2 What the Weaver does

A `WeaverTargetSpec` in `privacy-base`, `TargetID: "identityErasureComplete"`, `LensRef:
"identityErasureResidue"`:

| Gap | Action | Effect |
|---|---|---|
| `missing_credentialResidue` | `directOp UnbindIdentityCredentials{subjectKey: row.entityKey}` | next page; count strictly decreases |
| `missing_dedupResidue` | `directOp PurgeIdentityDedupFootprint{subjectKey: row.entityKey}` | next page; count strictly decreases |
| `missing_vaultDestruction` | `surface` (issueCode `ErasureVaultKeyNotDestroyed`, severity `warning`) | a stuck async half is **escalated, not swept** — the Vault destruction has exactly one correct actor |
| `missing_projectionNullify` | `surface` (issueCode `ErasureProjectionsNotNullified`, `warning`) | ditto |

> **Both cells corrected at increment 7's build.** `critical` is not a valid `IssueSeverity` —
> `registry.go:643-646` accepts `warning` or `error` only, and a target carrying anything else is
> rejected at CDC load, so this table as first written would have installed **no** target at all and
> none of the five gaps would have dispatched. `error`, the obvious repair, is also wrong: these two
> gaps are open from the instant the row first projects on **every** erasure (the marker precedes both
> async halves by construction), and an `error` issue escalates the whole Weaver component to
> `unhealthy` — so the ordinary path would hold Weaver unhealthy for the whole normal in-flight window,
> against Contract #5 §5.2. The dotted issue codes are corrected for the same reason §5.5 gives:
> `code` is PascalCase, and these would have been the only exceptions in the system.
| `missing_erasureSeal` | `directOp SealIdentityForErasureComplete{subjectKey: row.entityKey}` | writes the attestation |

**Convergence terminates, and is uncapped, by construction.** Both sweep gaps declare
`inflight_<g>` (constant `false` — each sweep is a synchronous commit with no in-flight window) and
**no** `maxretries_<g>`, which per #28 means *"no usable cap → the budget term never suppresses"*:
the Weaver re-dispatches every reconcile pass until the gap closes. Termination is guaranteed because
(a) each dispatch tombstones ≥ 1 link, so the count strictly decreases, and (b) §6 makes the set
closed, so nothing replenishes it. Without §6 this loop could run forever; with it, it terminates in
`ceil(N/PAGE)` passes. **This is the first Weaver-driven paged sweep in the codebase (#40)** — I flag
it as novel rather than claiming precedent, and §11 R1 carries the risk.

**The seal is the guarantee.** `SealIdentityForErasureComplete` writes
`vtx.identity.<U>.erasure { sealedAt, sealedForShreddedAt, coverage: {credentials, indexes, duplicates} }`.
Its gap only opens when `sealedForShreddedAt <> requestedForShreddedAt`, and the Weaver only dispatches
a gap on a **violating** row — but the decisive property is inside the op, not the lens: **the seal op
re-verifies residue itself, in its own commit, and fails closed.** It runs the same bounded
enumerations, and if any returns a live link it `fail("ErasureIncomplete: …")`. The lens decides
*when to try*; the op decides *whether it is true*. That closes the adjacency-lag hole (§7.3) and it
is why an erasure cannot carry a success signal it has not earned.

### 7.3 The adversarial trap this design walked into, and how it gets out

`packages/objects-base/lenses.go:60-74` records a **data-loss bug** caused by exactly the pattern §7.1
uses: adjacency-based `count()` lags the commit, so a freshly-created edge reads as absent and the
lens made a premature irreversible decision. Applied here, the failure direction is the bad one: a
`boundTo` created concurrently with the sweep would be **invisible** to the count, the residue would
read zero, and the seal would be written over a live edge — a silent, permanent erasure failure with
a success signal on it, which is precisely §8.6's objection reproduced inside the design meant to
answer it.

Two mechanisms close it, and both are needed:

1. **§6 removes the create.** Once `.erasureRequested` exists, no writer creates a `boundTo`, a
   `credentialindex` or an `identityindex` for that identity. The lag-on-create case cannot arise
   because the create cannot happen. Lag-on-*delete* remains — and is benign: it over-reports
   residue, causing one wasted idempotent dispatch, which the uncapped budget absorbs.
2. **The seal op re-verifies in-commit** (§7.2). Even if the lens is stale in either direction, the
   attestation is written only by a script that has just walked the links itself inside the same
   atomic batch. The lens is the *scheduler*; the op is the *judge*.

Objects-base's own answer — an authoritative counter maintained atomically by every link writer — was
considered and rejected: it would require touching every `boundTo`/`indexes` writer to maintain a
count, which is strictly more invasive than §6's five fail-closed gates and buys less (a counter still
would not stop the resurrection at `ddls.go:593-594`).

### 7.4 Attestation — what an auditor reads

| Question | Read | Source |
|---|---|---|
| "Is this person's key dead?" | `shredStatus` row: `shredded`, `vaultKeyDestroyed`, `projectionsNullified` | **unchanged**, `packages/privacy-base/lenses.go:79-89` |
| "Is this person's erasure complete?" | `identityErasureResidue` row: all five `missing_*` false | new |
| "Prove it" | `vtx.identity.<U>.erasure.coverage` — what the seal op verified, at `sealedAt` | new |

**`shredStatus` is not duplicated and not changed.** It answers "how far has the *key* shred got" and
it does that well; its own doc correctly calls itself visibility rather than correctness (#9). The
residue lens answers a different question — "is the *person* erased" — and it *reads* `shredStatus`'s
two booleans as residue classes rather than re-deriving them. The relationship is: `shredStatus` is
the key's ledger, `identityErasureResidue` is the erasure's, and the second consumes the first. An
operator watching a stuck erasure now gets a Weaver-surfaced critical issue instead of having to scan
a lens for a row with a false boolean.

---

## 8. Alternatives considered

### 8.1 Keep the in-op cascade (i.e. ship `credential-binding-plane-lifecycle-design.md` Inc 1)

Rejected, and this is the one the redirect overturned. Inc 1 would have added a `credentialindex`
scrub and a Gateway-seam drop to a batch that already refuses at 999 mutations (#3) — making the
refusal *more* likely for exactly the well-connected people most likely to file an erasure request. It
inherits all four defects of §2, adds a fifth class to enumerate with no attestation for any of them,
and lands squarely under `retention-class-key-custody-design.md` §8.6's objection. Its one real
advantage — atomicity — is §4.2's inverted virtue.

### 8.2 The privacy-worker-plus-listener plane, without Loom

**Andrew chose Loom; this records why the alternative loses rather than omitting it.** The shape:
extend the existing event-driven plane — `privacy.keyShredded` already fans out to two durable,
idempotent, independently-retrying listeners (#7) — by adding a third that does the unbind sweep, a
fourth that does the dedup purge, chaining on new events. No new engine, no pattern, no Loom change,
and §5.2's fork does not arise.

It is a genuinely reasonable design and it is **the shape we already have**. That is the strongest
argument against it: the hand-rolled pattern instance the brief identifies —
`RecordShredFinalization`'s durable step state, two actors, a progress projection, cycle semantics
(#5, #6, #7) — is what this plane grows into when you add steps to it, and it grew there *without an
orchestrator* because listeners have no place to put one. Four concrete losses:

- **The order is invisible.** In a listener chain, "key destruction first, structural cleanup after"
  lives in who subscribes to which subject. An auditor cannot read the obligation; they must
  reconstruct it from wiring across three packages. Loom's `Steps` list **is** the declared order,
  installed as data, inspectable via `InspectInstance`.
- **No per-subject durable instance.** No cursor, no "where is this person's erasure", no terminal
  failed state, no `RedriveInstance` (#25). Today's answer to a stuck shred is *"re-submit
  `ShredIdentityKey`"* (`packages/privacy-base/lenses.go:40-42`) — re-running the whole thing because
  there is nothing that knows which part is stuck.
- **No off-stream backstop.** Loom arms a per-step deadline that probes op-status and distinguishes
  committed / not-yet-relayed / rejected (`engine.go:1190-1246`). A listener that Acks after a failed
  submit has lost the work with no timer watching.
- **Every new step is new Go in a platform binary.** A step here is a package-declared `StepSpec`
  (#21) loaded by CDC. Adding option C later (§9.1) is a data change plus one op; under 8.2 it is
  another listener in another binary.

What 8.2 gets right, and this design keeps: **the async attestations stay listener-driven** (§3).
Loom cannot wait on them (#16) and should not try.

### 8.3 Weaver-only, in goal/planner mode

Express the whole erasure as one `planned`-mode target with a `Goal` and an action catalog whose
`Pre` conditions encode the ordering — the shipped `renewalComplete` shape (#39). This nearly works,
and I took it seriously because it would make §5.2's fork disappear entirely.

It loses on three counts, all about what an erasure *is*. (1) The planner **searches** — it picks the
cheapest currently-eligible action whose effects advance the goal. A legal ordering obligation should
be *declared*, not discovered by cost-ascending search; "key destruction happens first" must not be a
consequence of `Cost: 1`. (2) There is no durable per-subject instance — the same loss as 8.2, plus
the erasure's progress would live only in `weaver-state` marks. (3) Decisively: budget exhaustion on a
planned target escalates to **Augur**, the AI reasoning tier (`internal/weaver/strategist.go:514-577`),
whose output is a model-proposed action. An LLM proposing the next move in a right-to-erasure is the
wrong tier by a wide margin, and opting out of Augur means falling back to a standing health issue —
i.e. re-inventing 8.2's "nobody is driving this".

**The hybrid is not a compromise between 8.2 and 8.3 — it is each mechanism doing the thing it is
good at**: Loom declares order over a bounded, once-per-subject sequence; Weaver converges an
unbounded, data-dependent, indefinitely-retried set. Neither can do the other's job (#15, #16, #20 vs
Loom; #39's search semantics vs Weaver).

### 8.4 Keep the cap, raise the number

Rejected in one line: any finite cap is a size limit on who may be forgotten, and `BatchTooLarge` at
step 8 is terminal on redelivery (the reason the pre-flight cap exists at all,
`shred_identity_key.go:321-327`). The answer to "some people are too connected to erase in one
commit" is not a bigger commit.

### 8.5 A `forEach` step kind in Loom

Rejected. `pattern.go:190-239` admits exactly three kinds and fails the whole pattern load on a
fourth (#20); `docs/components/loom.md:57` makes linearity a binding constraint, not an omission. A
fan-out step would also need per-item failure semantics, a cursor inside a step, and its own retry
policy — i.e. it would reinvent the Weaver's convergence loop inside Loom. The correct answer is the
one the contract already gives: *"Conditional paths → Weaver."*

---

## 9. What this unblocks, and what it does not

### 9.1 Old option B: realised in full

`credential-binding-plane-lifecycle-design.md` §7.2's option **B** — *"the shred fully unbinds every
credential: index scrub-tombstoned, edge tombstoned, operational seam dropped via `identity.unbound`"*
— is realised **exactly**, as step 3 (§5.4) plus its convergence gap. All three of B's representations
are retracted: the `credentialindex` vertex, the `boundTo` link, and the Gateway's `credential-bindings`
bucket row via the `identity.unbound` fold (#32). Representation 1 (`credentialBinding`, sensitive) is
rendered undecryptable rather than retracted — the same honest statement §3.3 made there, unchanged.

Two things B did not have and this does: it **cannot refuse** (§10), and it **attests** (§7.4).

### 9.2 Old option C: a step, behind two named preconditions

Option **C** — cascade key destruction to every bound credential identity, destroying `.idpBinding`,
the raw IdP `(iss, sub)` — becomes a **fifth Loom step** plus a sixth residue class. The categorical
objection dissolves for the reason the redirect states: §8.6 rejects an *unattested in-batch walk*
whose enumeration completeness is the guarantee. Under this design the walk is a step, its coverage is
in the seal, its misses are a residue class, and its repair is a Weaver dispatch. Incompleteness
becomes **detectable and repairable** — the platform's own detect-and-recover doctrine.

**Precondition (i) — `credentialindex` reachability, still open.** Verified unchanged (#34): no
`credentialindex` linkType DDL exists, and the only bulk access is a raw keyspace prefix scan in an
operator CLI (`cmd/lattice/identity/reconcile.go:153`). §7's residue lens therefore counts `boundTo`
edges and `identityindex` vertices — both link-reachable — but **cannot count orphaned
`credentialindex` vertices**, because a lens walks links and there is no link. Giving `credentialindex`
an `indexes`-style link to its owner (the §9 alternative in the credential-binding design) is what
makes reachability *structural*; until then C's coverage claim is shape-guaranteed. **This design does
not close (i)** and does not pretend to.

**Precondition (ii) — hard-delete. Yes, erasure-completeness is the real revive trigger. Say it
plainly.** `hard-delete-mutation-verb-design.md` is 🗄️ shelved for want of a keyspace-reclaim driver,
its filed driver (LIST growth) having evaporated. The driver it lacked is this one, and it is not
about storage at all: a tombstone is a **live NATS KV entry** whose key persists and is still
enumerated (`internal/substrate/kv.go:227` — *"ListKeysFiltered's IgnoreDeletes drops only NATS
hard-delete markers, which [the Processor never writes]"*; the hard-delete design §1.1 cites the same
line at its then-current number), and the key
`lnk.identity.<credId>.boundTo.identity.<uId>` **names both parties in the key itself**. So after a
"complete" erasure, the correlation *credential A belonged to person U* remains readable, forever, by
anyone who can list the keyspace — and Contract #3 §3.3 makes tombstones permanent by design. No sweep
this design can write will ever clear it, because there is no verb that removes a key. **Full
correlation erasure needs the `delete` verb.** That is a materially different driver from the one that
got the design shelved — a legal-obligation completeness gap, not a cosmetic reclaim — and it also
satisfies that design's own revive condition (1), since `hardDeletable: true` would be a per-DDL
structural opt-in on exactly the two link types an erasure clears. **I do not design hard-delete
here.** I record that its shelving rationale no longer covers this case, and recommend the board row's
revive trigger be re-dated to reference this design.

**Sequence: Inc 1–5 → precondition (i) → re-open C.** Unchanged from the credential-binding design's
§7.4, except that C is now a step to add rather than a cascade to argue about.

### 9.3 What is explicitly NOT in scope

- **Hard-delete** (§9.2) — named as C's precondition, not designed.
- **`credentialindex` reachability** (§9.2 (i)) — named, not designed.
- **Option C itself** — sequenced behind both.
- **Tombstoning the identity vertex.** A shred does not delete the account today
  (`credential-binding-plane-lifecycle-design.md` §7.1) and this design does not change that. Note the
  interaction: `shredStatus`'s scope note (`lenses.go:48-52`) says a tombstoned identity loses its row;
  the residue lens inherits the same property, and the same deferral.
- **Retention-class keys.** `retention-class-key-custody-design.md`'s second key with its own clock is
  orthogonal; §6's separate `.erasureRequested` aspect is deliberately chosen so a retention-class
  shred never trips a person's write-path gates.
- **Inc 3b** (`identityIdpBindingsRead`) and **Inc 2** of the credential-binding design — both
  ratified/deferred there, untouched here.
- **Erasure of object-store content, JetStream history, or Postgres lens targets** beyond what the
  existing `keyshredded` nullify targets already cover.

---

## 10. How the batch cap is retired

By construction, at every level:

1. **`ShredIdentityKey` becomes constant-size** — exactly 1 mutation (§4.1). Not "usually small":
   *constant*, for every identity in the corpus. The `total_muts` computation and the
   `ShredBatchTooLarge` fail are **deleted** (`shred_identity_key.go:321-330`), and so are the three
   `…FanoutTooLarge` failures (#2), because their enumerations leave with them.
2. **Every step op is bounded by a page**, not by the subject's connectivity: ≤ `2·SWEEP_LIMIT + 1`
   (step 3) and ≤ `2·SWEEP_LIMIT` (step 4), with `SWEEP_LIMIT = 64` (§5.4 as amended). Both are far under the 999 platform ceiling with
   room to spare, and — the load-bearing part — **the bound does not depend on any input**. A person
   with 10,000 credentials produces 20 identical bounded commits, not one refusal.
3. **The tail is covered by convergence, not by a bigger commit.** Each pass strictly decreases the
   residue (§7.2); §6 stops it being replenished; the uncapped budget (#28) keeps dispatching until
   zero.
4. **No step can reintroduce a refusal.** This is a checkable property, and Inc 5's test strategy
   makes it one: *no op reachable from the erasure pattern or the erasure target may compute a
   mutation count from an unbounded enumeration.* Each sweep retires **at most `SWEEP_LIMIT` live
   links**, a constant, so the `MAX_*_PAGES` fanout failures do not exist in the new ops.

   **Corrected by increment 3 — the mechanism, not the property.** This point originally specified
   the implementation as *"`kv.Links` with a single call and no page loop … the cursor lives in the
   world (the remaining live links), not in the script."* That is **false against this substrate**,
   and building it that way produces a silent stall rather than a refusal. A tombstone is a **soft
   delete**: the key stays in the keyspace and `kv.Links` keeps returning it with `isDeleted` set
   (`internal/processor/starlark_kv.go`'s `connLinkLister` skips only a *hard* delete — the
   shipped `kv.Links` LIST-cost row on the board says the same thing). So after one sweep the first
   page is entirely tombstones, and a cursor-less call finds zero live links and stalls there
   **forever**, at a non-zero residue, while the target re-dispatches a no-op. The cursor lives in
   the **keyspace**, which retains what the world has discarded.

   What the sweep ops actually do: **page on the READ until `SWEEP_LIMIT` LIVE links are in hand**,
   and cap the mutation count at that constant. The property this point exists to guarantee — a
   mutation count that cannot be grown by the subject's connectivity — holds unchanged; only the
   claim that one `kv.Links` call suffices was wrong. **Inc 5's lint rule must be written to the
   property, not to the literal "no page loop"** — a rule banning the page loop would ban the very
   construct convergence requires.

   Two consequences increment 3 measured and the sweep ops must respect:
   - **`SWEEP_LIMIT` is not the read page limit.** §5.4's `PAGE = 256` was taken from the *read*
     page sizes of ops that enumerate and commit once; in a sweep the same number sizes an **atomic
     batch on every pass**, and `2·256` mutations exceeds the Processor's **250ms Starlark wall
     budget** on a loaded host. An op that refuses by wall clock reintroduces this refusal through a
     second door. `SWEEP_LIMIT = 64` measured clean under the full `-p 4` suite; the cost is more
     passes, which the target already dispatches uncapped.
   - **The scan window is finite.** Paging past accumulated tombstones is bounded by
     `MAX_*_PAGES × page`, so a subject with more tombstoned links than that window ahead of its
     live ones cannot be reached. The sweep **fails loudly** there rather than returning empty: a
     silent stall under an uncapped re-dispatch is strictly worse than a surfaced stop. Reachable
     only past ~16k links in one direction — far beyond where today's in-shred cascade already
     refuses — and the durable fix is the shelved hard-delete verb or a per-relation epoch.

**Net: there is no input for which the platform can decline to erase a person — of the MUTATION
count, which is what this section bounds.**

**Corrected by increment 4 — the READ side carries a ceiling this section never bounded, and the
sweep builds its own way into it.** The enumeration is a stable ascending scan over the keyspace, and
a tombstone stays in it, so each pass drains the lexicographically-earliest live links and the
tombstone prefix ahead of them grows by `SWEEP_LIMIT` every time. A subject with more than
`MAX_*_PAGES × PAGE` links **on one relation** therefore converges normally until that prefix fills
the scan window, and then stops permanently at `ErasureResidueUnreachable` — with no pre-existing
tombstones required, which is what the previous wording ("only past ~16k *tombstoned* links, far
beyond where today's cascade refuses") got wrong. Two consequences follow, and neither is a reason
to prefer the un-decomposed shred, which refuses around **500** index links where this reaches ~16k:

- **The claim is about mutations.** A subject past the window cannot be fully erased by this
  machinery, and because the `indexes` collector runs first on every dispatch, its failure also
  blocks the two `duplicateOf` classes for that subject. The stop is loud and surfaced rather than a
  silent stall, which is the property that makes it repairable.
- **The per-pass READ cost grows with what has already been swept.** Each pass re-pages over the
  accumulated tombstones before reaching a live link, so a whole erasure costs `O(L²/SWEEP_LIMIT)`
  round trips and a single pass can approach the Processor's 250 ms Starlark wall well before the
  window ceiling — a refusal through the second door this section's own amendment warns about, on the
  read side rather than the write side.

**The durable fix is the shelved hard-delete mutation verb**, whose board row already records that a
tombstoned key persists and is still enumerated by `kv.Links`. A tombstone that leaves the keyspace
removes the ceiling and the rescan cost together. Increment 3 named that row as this design's first
real driver; increment 4 is the measurement that makes the driver concrete.

---

## 11. Risks

| # | Risk | Mitigation |
|---|---|---|
| **R1** | **First Weaver-driven paged sweep in the codebase** (#40). No precedent for a `directOp` that processes N rows and expects re-dispatch. If the uncapped-budget reading of `evaluator.go:875-881` is wrong in practice, sweeps stall at 3 passes. | Inc 4 proves it with an integration test at `N > 3·PAGE` before any other convergence work lands. Fallback is a lens-computed `maxretries_erasureresidue = ceil(N/PAGE) + 2` — grounded, since a row-declared cap always wins (#28) — at the cost of a baroque column. |
| **R2** | **`Scope:"any"` grants on erasure verbs** (§5.3). Three new ops that tombstone credentials, granted to service actors. | No `OpMeta` descriptor and no person-facing affordance. The grant IS to `operator` — grants attach to roles, not actor keys, so that is how a service actor is reached at all, and it reaches human operators with it; each op therefore fail-closes on the `erasureRequested` marker's class, conferring no authority a completed seal has not already exercised. Intended submitters are `identity.system.loom` and `identity.system.weaver`. `make verify-package-*` on both packages; the grant matrix diff is a review gate in its own right. |
| **R3** | **§6's write gates are a behaviour change on the hot claim path.** `ClaimIdentity` gains a read and a rejection. | The read is class-(d) `optionalReads` (one extra hydration key, absent for every non-erased identity). The rejection is unreachable for anyone not erased. Full `go test ./...` — a shared claim-path default reaches unedited consumers. |
| **R4** | **Ledger #26**: Loom's contract describes a retry branch the code lacks. A step that fails goes terminal with no retry. | Not relied upon (§5.5): a dead spine degrades to eventual, never to incomplete, because the Weaver tail is independent. Pre-existing divergence — worth its own board row, out of scope here. |
| **R5** | **Three `OPTIONAL MATCH` fan-outs in one lens row** may not project cleanly (§7.1). | Collapse to the `count(DISTINCT CASE WHEN …)` idiom (`lease-signing/lenses.go:646-647`). Proven by a `lens_cypher_test.go` parse test against the full engine before Inc 4 — the `packages/privacy-base/lens_cypher_test.go:65` precedent. |
| **R6** | **Erasure now spans four packages and two engines.** More moving parts than one op. | The parts are declared data (a pattern spec, a target spec, a lens) plus three ops. The seal (§7.2) means no combination of broken parts can produce a false completion — the failure mode is a stuck erasure, which is visible, escalated and repairable. That is the trade the whole design makes. |
| **R7** | **The seal op re-verifies with bounded enumerations** — if a subject's residue exceeds one page at seal time, the seal must refuse. | Correct and intended: it refuses, the residue gaps are still open, and the sweeps run again. The seal is the last gap to close because its own verification cannot pass until the others have. |

---

## 12. Increments — TWO fires (rewritten at ratification 2026-08-06)

Andrew's standing amendments: **one lane** (Lattice) and **fewer, larger fires**. The original five
increments collapse to two, and the collapse fixes a sequencing hazard the five-increment order created.

### Fire A — `StepSpec.Reads` in Loom (Lattice, S–M). Option B on the §5.2 fork.

`pkgmgr.StepSpec` gains `Reads`/`OptionalReads`; `loomPatternSpecBody`; `loom.Step`; `pattern.go`
validate; `submitSystemOp` resolution, mirroring the shipped `userTaskReads` resolution that
`submitUserTask` already threads into `buildOutbox`. Pattern load rejects a `Reads` entry on a
`userTask`/`externalTask` step. Plus the §5.3 correlation probe — **a systemOp step whose bound op emits a
domain event**, which is the detail the design could not verify from the corpus (no shipped pattern uses a
`systemOp` at all) and which sizes Fire B.

**Fork resolved to option B, not lazy `kv.Read`.** Option A would place three new erasure-path ops on the
losing side of a gate that is actively tightening toward blocking — the wrong footing for a legal
obligation — while option B extends a shipped resolution rather than inventing one, and Loom's own comment
already anticipates it (*"a future systemOp that reads would set its own read-set here"*).

Platform-only, no privacy surface, ships safely alone, and it is the cheap de-risking that tells Fire B its
size.

### Fire B — the whole erasure move, in one landing (Lattice, L–XL).

Old Increments 1, 3, 4 and 5 together. **The collapse is not tidiness — it closes a regression window.**
The five-increment order put the op-narrowing first, arguing it *"ships alone and is immediately correct:
the erasure becomes less complete than today but cannot refuse, and nothing regresses that was ever
guaranteed."* That is true about **guarantees** and glosses a **common-case behavioural regression**: today
a shred for an ordinary person really does tombstone their `identityindex` vertices, `duplicateOf` links and
`boundTo` links, and after the narrowing alone it would not — until the pattern lands. "It was never
guaranteed because it could refuse above 999 mutations" is a fair statement about the guarantee and a weak
one about what actually happens to the common case. So the cascade **leaves the op and arrives in the
pattern in one landing**.

Internal build order, chosen so erasure never does less than it does today:

1. **Write-path closure and the unbind primitive.** `SealIdentityForErasure` (privacy-base) writing
   `.erasureRequested`; the five fail-closed gates across identity-domain and identity-hygiene (§6);
   `UnbindIdentityCredentials` (identity-domain, §5.4) with its `Scope:"any"` grant. Purely additive —
   nothing is removed yet. This is the increment that makes §7's convergence mean anything.
2. **The pattern and the convergence.** `PurgeIdentityDedupFootprint`; the `identityErasureResidue` lens +
   `privacy-erasure` bucket; the `identityErasureComplete` weaverTarget; the `identityErasure` Loom
   pattern; the two `surface` gaps; `SealIdentityForErasureComplete` with its in-commit re-verification.
   R1's `N > 3·PAGE` proof lands here.
3. **Then narrow the op.** `ShredIdentityKey` drops the three enumerations, the four refusal modes and the
   mutation-count pre-flight — one mutation, one event — only once the pattern demonstrably performs the
   work the op is giving up. Update the DDL description, the stale in-commit rationale, and
   `identity-domain/ddls.go:1605-1612`'s comment.
4. **Operator surface and the standing gate.** A Loupe pane over `privacy-erasure` (P5: reads the bucket)
   showing per-identity residue and seal state; the `lint-conventions` rule for §10 point 4 — no op
   reachable from the erasure pattern or target may page an enumeration.

**One dependency to resolve rather than inherit.** The original §12 declared "Inc 3 depends on Inc 1", i.e.
the write-path closure depending on the narrowing. This order inverts that. If the dependency is real — if a
gate genuinely cannot be written against the un-narrowed op — the fire must say why and keep the
enumerations live until step 2 completes, rather than reordering silently. Do not assume either way from
this rewrite.

**Review depth: full 3-layer adversarial.** Fire B carries both of the design's first-of-kind risks — R1's
Weaver-driven **paged** sweep, for which the codebase has no precedent anywhere, and R5's three-way
`OPTIONAL MATCH` residue fan-out with a flagged possible engine limitation — plus a legal obligation and
five write-path gates.

**Not a fire: option C** (§9.2), behind its preconditions. Note the third one stands: the DD pass found
§9.2 claims the sequencing is "unchanged" from the credential-binding design's §7.4 while dropping that
section's *behind the first configured external key source* gate. Treated as standing, since `.idpBinding`
cannot exist before an external key source — so option C has **three** preconditions, not two, unless
someone argues otherwise on the record.

## 13. Test strategy

- **Inc 1** — `shred_identity_key_test.go`: mutation count is exactly 1 for an identity with 0, 1 and
  500 links; no `ShredBatchTooLarge` path remains reachable (assert the symbol is gone, not that it
  does not fire). Re-shred still resets the cycle (#5) and still writes the placeholder for a
  never-sensitive identity (`:299-306` — the restart-safety argument must survive the narrowing, and a
  test should say so).
- **Inc 2 (Fire A) — SHIPPED.** `internal/loom`: a systemOp step with declared reads produces an outbox
  record carrying the resolved `ContextHint.Reads`; a `subject.<aspect>` template resolves against
  `inst.SubjectKey`; pattern load rejects a `Reads` entry on a `userTask`/`externalTask` step. Plus the
  §5.3 correlation probe. Fire A added two guards its review earned: the template grammar's charset
  boundary, and the `subjectKey`-is-a-vertex confinement precondition.
  **Fire B inherits one test Fire A could not write**: the `TestUserTaskReads_CoverEndpoints` analogue
  for each step op it declares — the declared set must cover every key that op's DDL reads, or the op
  HydrationMisses in production. There is nothing to compare against until a pattern declares reads, so
  the guard lands with the declaration, one per step op in §5.4.
- **Inc 3** — the resurrection test, which is the one that matters: shred → seal → attempt the write
  that today revives the index (`ddls.go:593-594`) → assert rejection and assert **no live
  `identityindex`**. A **negative test can pass for the wrong reason**, so the positive vector runs
  first: the identical write on a non-erased identity must succeed. Same pair for `ClaimIdentity`,
  `CompleteCredentialLink`, `ReconcileCredentialBinding`, `MergeIdentity`.
  `UnbindIdentityCredentials`: unbinds the last credential (no `last-credential` refusal), emits one
  `identity.unbound` per credential, is a clean no-op on a second run, and — the P2/seam check — the
  `credential-bindings` bucket row is gone after the materializer folds.
- **Inc 4** — `lens_cypher_test.go` parses the residue spec on the full engine (R5), per
  `packages/privacy-base/lens_cypher_test.go:65`. Convergence integration at `N = 3·PAGE + 7`: assert
  the sweep is dispatched more than 3 times (R1) and the residue reaches zero. **The seal's
  fail-closed test is the headline**: force a live `boundTo` past a stale zero-residue row and assert
  `SealIdentityForErasureComplete` **refuses** — that single test is the design's guarantee in
  executable form. A stuck `vaultKeyDestroyed` surfaces a critical issue and **does not** seal.
- **Inc 5** — Loupe pane reads only `privacy-erasure` (P5 gate); the lint rule fires on a planted
  paged enumeration in an erasure-path op.
- **Cross-cutting** — full `go test ./...` on Inc 3 and Inc 4 (R3: a shared claim-path change reaches
  unedited consumers). `make verify-package-privacy-base` / `-identity-domain` on any DDL, permission
  or lens change. `make verify-kernel`. No fixed `time.Sleep`; `testutil.EnsurePrimordials(t)` for the
  service actors.

---

## 14. Contract surface

**None. Aim achieved.** Every mechanism is built to the shipped contracts:

- **#1** — no new key shape. `.erasureRequested` and `.erasure` are 4-segment
  `vtx.identity.<id>.<localName>` aspects; no new link type; `boundTo` direction untouched (#35).
- **#2 §2.5** — step ops declare reads (Inc 2) or, under §5.2 option A, annotate class-(b) debt. The
  enumerations stay class **(e)** with their data-derived follow-ups, the posture the shipped script
  already declares (`shred_identity_key.go:336-337`). The §6 gates are class **(d)** `optionalReads`,
  same as the shipped `piiKey` read (`:286-288`).
- **#3 §3.10/§3.11** — crypto-shred semantics unchanged; nothing here decrypts.
- **#10 §10.5/§10.8/§10.9** — the pattern is a declared linear step list; gap columns are
  `missing_<gap>` (#27); the Loom instance stays operational-only (#22) — which is *why* attestation
  needs its own Core-KV aspect rather than living on the instance.
- **#11 §11.4** — the Gateway seam is retracted through its own `identity.unbound` fold (#32), never
  written directly.

**P2/P5 compliance, stated explicitly.** Every step and every gap action **submits an operation** —
neither Loom nor Weaver writes Core KV; both publish to `ops.<lane>` and the Processor commits
(`internal/loom/actuator.go:84-123`, `internal/weaver/actuator.go:82-114`). The residue lens is a
**read model** in a package-owned NATS-KV bucket; Loupe reads the bucket, never Core KV. The **only**
engine Core-KV read in the whole design is Loom's guard evaluation
(`internal/loom/guard_eval.go:251`) — the one sanctioned engine read, and this design adds no other.

**On `credential-binding-plane-lifecycle-design.md` §6.2's unstaged Contract #3 sharpening.** It is
**not needed, not changed, and not made moot** by this design. That sentence obliges a script emitting
a link to validate its endpoints when the link feeds a completeness-claiming projection — a
**bind-path** duty that Inc 2 of that design discharges. This design emits no link; it only retracts.
It should still ride that Inc-2 build fire exactly as decided at ratification. *(Worth noting for
whoever holds it: this design gives that sentence a second, stronger instance — the residue lens is
the most completeness-claiming reader on the platform — so if anything the case for it is now
firmer.)*

**If a contract note is ever wanted here**, the honest candidate is a Contract #10 sentence saying a
`systemOp` step may declare a subject-templated read set (Inc 2). I recommend **not** writing it: #10
describes the step kinds without enumerating their fields, and `userTask`'s read set is already
undocumented there. Adding one field to a spec struct is not a contract event. **No PROPOSED text is
offered and no file under `docs/contracts/` was touched by this fire.**

---

## 15. Adversarial pass — run on the frozen draft, and what it changed

Four findings. Three changed the design; one changed only the claim.

**A1 — The residue lens could seal over a live edge (§7.3). Design changed.** I had written the lens
with `OPTIONAL MATCH` + `count()` and called it the guarantee. Reading
`packages/objects-base/lenses.go:60-74` — which records a real **data-loss bug** from exactly that
pattern — showed adjacency lags the commit, so a concurrently-created `boundTo` reads as absent, the
residue reads zero, and the seal lands on a live edge. That is §8.6's objection *reproduced inside the
design meant to answer it*, and it would have shipped. Two changes: **§6 (the write-path closure)
was promoted from a nice-to-have into a hard precondition of the convergence claim**, and **the seal
op now re-verifies in its own commit and fails closed** — the lens schedules, the op judges.

**A2 — The convergence loop would have died at three passes. Design changed.** The first draft assumed
the Weaver would re-dispatch a `directOp` indefinitely. It would not:
`defaultDirectOpRetryBudget = 3` (`internal/weaver/evaluator.go:851`) applies to any `directOp` gap
declaring neither `maxretries_<g>` nor `inflight_<g>`. An erasure needing four pages would have
silently suppressed at three and escalated. Fixed by declaring `inflight_<g>` on both sweep gaps
(#28) — and R1 now carries the residual risk plus a grounded fallback, because this reading is the
load-bearing mechanism of the whole tail and there is no precedent for it (#40).

**A3 — `UnlinkCredential` cannot be a step. Design changed, and Inc 3 grew.** The draft reused it,
which is what an unverified reading of the credential-binding design suggests. It resolves its target
from `op.actor` (`packages/identity-domain/ddls.go:1495`), refuses the last credential
(`:1544-1545`), and is granted `Scope:"self"` to `consumer` only
(`packages/identity-domain/permissions.go:95-99`) — so dispatched by Loom it would look up *Loom's*
credentials and fail `not-found` every time. A new system-dispatchable op was added and Inc 3 was
resized from S to M. **Verify precedent, do not copy it.**

**A4 — "The in-batch cascade must be atomic because it is the only decrypt-free window." Claim
changed.** I initially accepted the script's own stated rationale (`shred_identity_key.go:60-67`) and
was going to argue decomposition was safe *despite* it. Reading further, the same file says the
enumerations need no decryption at all — *"linkage IS ownership"* (`:186-188`). The rationale is
self-contradicting: link walks are unaffected by the DEK's death. Ledger #11 now records this, and
§4.2 uses it as the *first* reason the enumerations may leave, rather than a caveat. **Ground the
mechanism before accepting the premise — including the codebase's own premises.**

**One thing the pass did not resolve.** §5.3's completion-correlation detail stays unverified: no
shipped systemOp emits a domain event, so I could not confirm from existing code how a systemOp step
advances. It is flagged in the ledger, assigned to Inc 2, and cannot change the architecture — only
one line of the pattern declaration.

---

## Fire A build note (2026-08-07)

### Fire brief

**1. Scope (verbatim, §12 Fire A).** *"`pkgmgr.StepSpec` gains `Reads`/`OptionalReads`;
`loomPatternSpecBody`; `loom.Step`; `pattern.go` validate; `submitSystemOp` resolution, mirroring the
shipped `userTaskReads` resolution that `submitUserTask` already threads into `buildOutbox`. Pattern load
rejects a `Reads` entry on a `userTask`/`externalTask` step. Plus the §5.3 correlation probe."*
Green bar: `go test ./internal/loom/ ./internal/pkgmgr/` + the full gate set; the probe answered on the
record.

**2. Verified touch-list** (anchors re-checked live against `885f39be`; all four design citations hold,
two drifted by a few lines and are corrected here):

| Site | Anchor (live) | Design cited | Edit |
|---|---|---|---|
| `internal/pkgmgr/definition.go` | `type StepSpec struct` **:426** | `:426-458` ✅ | add `Reads`, `OptionalReads` |
| `internal/pkgmgr/build.go` | `loomPatternSpecBody` **:856** | `:856-889` ✅ | emit both, omit when empty |
| `internal/loom/pattern.go` | `type Step struct` **:28** | `:28-48` ✅ | add both with `omitempty` json tags |
| `internal/loom/pattern.go` | `validate()` **:190** | `:190-239` ✅ | systemOp-only + template-form check |
| `internal/loom/engine.go` | `submitSystemOp` **:857**, `buildOutbox(...nil, nil, nil)` **:867** | `:861-867` ▲ drift 4 lines | resolve + thread |
| `internal/loom/actuator.go` | `buildOutbox` **:134** | `:134` ✅ | no change — signature already carries both |
| `internal/pkgmgr/orchestrationguard.go` | `validateLoomPatterns` **:286** | not cited | mirror the rules (see below) |
| `internal/pkgmgr/capabilitymaterializer.go` | `StepArtifact` **:132**, `knownStepFields` **:456** | not cited | mirror the fields |

**Two sites the design's scope sentence does not name, and why they are in scope rather than drift.**
`validateLoomPatterns`' own doc comment states its contract: *"It mirrors the engine's validate() exactly
so an install never admits a pattern the engine would reject at CDC load."* A validation rule added to
`pattern.go` alone would break that lockstep and let a package install a pattern that then runs dark —
so the installer mirror is part of "pattern.go validate", not an addition to it. Likewise `StepArtifact`
is *"a field-for-field mirror of pkgmgr.StepSpec … reused verbatim"*, materialized by a direct
`StepSpec(s)` struct conversion (`capabilitymaterializer.go:641`): adding a field to `StepSpec` without
it does not compile. Both are consequences of the scope sentence, not extensions of it.

**3. Precedents to mirror.** `userTaskReads` (`engine.go:889`) + `userTaskOptionalReads` (`:909`) — the
derive-a-read-set-from-the-subject helper with its invariant recorded in the comment and pinned by a test
(`usertask_reads_internal_test.go`). Resolution threading: `submitUserTask` (`:965`) passes both sets into
`buildOutbox`. Validation style: `validate()`'s existing wrong-kind-field rejections
(`pattern.go:203-215`). Spec-body emission: `loomPatternSpecBody`'s emit-only-when-set arms
(`build.go:858-878`). Nothing here is greenfield.

**4. Increment order.**
1. `pkgmgr` — `StepSpec` fields + `loomPatternSpecBody` emission. Green: `go test ./internal/pkgmgr/`.
2. `loom` — `Step` fields, `validate()` rules, `systemOpReads`/`systemOpOptionalReads` + `submitSystemOp`
   threading. Green: `go test ./internal/loom/`.
3. Probe (§5.3) — answered by reading, recorded below. Green: the finding is on the record.
4. Gates: `go build ./...`, `make vet`, `golangci-lint run ./...`,
   `STRICT=1 go run ./scripts/lint-conventions.go`, `make verify-kernel`, `go test ./internal/loom/
   ./internal/pkgmgr/ ./internal/processor/`.

**5. In-scope gotchas.**
- **Contract #10 §10.5 freezes the step shape** — *"Step shape: `{ kind, operation, guard? }` for
  `userTask`/`systemOp`"* (`docs/contracts/10-orchestration-loom.md:55`). §14's "no contract change" is
  wrong on this one point: two new systemOp-only step fields extend a frozen shape. The edit is made in
  `main` **UNCOMMITTED** as the proposal; the code ships around it. *(Ratified + committed 2026-08-08,
  `1ad3f1c4`, trimmed to its normative core.)*
- Aspect keys stay 4-segment: `subject.<aspect>` resolves to `inst.SubjectKey + "." + aspect`, exactly the
  shape `userTaskOptionalReads` already builds (`subjectKey + ".availability"`).
- **A package version bump is required even though no package entity changes.**
  `scripts/lint-package-version.go` fires on any `internal/pkgmgr/` change reaching a package that
  declares `ReadGrantDomains`, because that tree compiles the GENERATED read-grant producer lens. The
  gate has no declaration escape hatch and the premise is not verifiable by eye, so `edge-manifest`
  bumps 0.15.1 → 0.15.2 (manifest.yaml + `package.go` — parity is test-pinned).
- No stack cycle needed for `pkgmgr`, but `internal/loom` ships in binaries beyond `bin/loom`; derive them
  mechanically at admit.

**6. Adjacent finds.** One, and it resolves to a Fire B obligation rather than a board row.
`submitSystemOp` has no equivalent of `userTaskReads`'s coverage test, so nothing pins a step-op's
declared set to what its DDL actually reads — the guard `TestUserTaskReads_CoverEndpoints` gives the
userTask arm. That guard **cannot be written until a pattern declares reads**: there is nothing to
compare a DDL against while the field is unused. So it is not a standalone row that would sit unpickable;
it is a test **Fire B owes**, one per step op it declares (§5.4's four), and it is named in §13 below.
Deliberately not filed: the §12 "one dependency to resolve rather than inherit" question is Fire B's,
not this fire's.

**7. Non-goals.** No `packages/privacy-base` or `identity-domain` change; no pattern declaration; no lens,
target or op; no narrowing of `ShredIdentityKey`. `egressReads` stays nil on the systemOp arm (no
external-plane step op exists) — Fire A adds only the two read classes §5.2 names.

**Scope-diff gate: clean.** Every touch traces to the scope sentence; the brief narrows nothing and
substitutes nothing. Declared dependency check both ways: Fire A declares no dependency on Fire B (correct
— it ships alone), and no unlisted dependency surfaced.

### §5.3 correlation probe — ANSWERED (the detail Fire A owed Fire B)

**Question.** *"Loom advances a step on a completion event correlated to the pending token, over the
pattern's `completionDomains`. I did not trace the correlation end to end for a `systemOp` whose bound op
emits its own domain event, because no shipped systemOp emits one. Inc 2 must confirm this before Inc 4 is
sized; if a systemOp advances on op-commit rather than a domain event, `CompletionDomains` may be
irrelevant here."*

**Answer: a systemOp advances on the domain event, and `CompletionDomains` is load-bearing — but only for
promptness, never for correctness.** Two paths, traced end to end:

1. **The event path (the normal one).** `submitSystemOp` sets `inst.PendingToken = deriveRequestID(...)`
   and submits the op under that same `requestId` (`engine.go:858-867`). The Processor stamps the emitted
   event's top-level `requestId` from the op envelope — `RequestID: env.RequestID`
   (`internal/processor/step7_events.go:83`, on the `Event` struct at `:18-22`). Loom's completion
   consumer parses that field into `eventBody.RequestID` (`engine.go:672`), `correlationKeys` tries it
   first (`:728`), and `handleCompletion` resolves `token.<requestId>` → advance (`:696-710`). So the
   correlation closes **iff** the bound op emits an event whose **domain is in `completionDomains`** —
   the consumer is only reconciled for those domains.
2. **The deadline backstop.** If no such event arrives — a mis-declared `completionDomains`, or a bound op
   that emits nothing at all — `onDeadline` takes the systemOp branch, where *the pending token IS the op
   requestId*, probes the Contract #4 tracker, finds the op committed, logs
   `"loom: completion recovered via deadline probe; check completionDomains"` and **advances**
   (`engine.go:1220-1229`). Exactly what `docs/contracts/10-orchestration-loom.md:191` promises: the
   deadline "backstops a mis-declared `completionDomains` → alert, never a silent wedge."

**Consequences for Fire B, stated so it does not have to re-derive them.**

- **`CompletionDomains: ["privacy"]` stays in §5.1, and it is not cosmetic.** Steps 1 and 2
  (`ShredIdentityKey`, `SealIdentityForErasure`) emit `privacy.*`, so `privacy` must be listed or every
  one of those steps advances one `StepTimeout` late with a WARN.
- **§5.4's step 3 forces a second domain.** `UnbindIdentityCredentials` emits `identity.unbound`, so the
  pattern completes on **two** domains — `CompletionDomains: ["privacy", "identity"]`, per the contract's
  own *"a pattern mixing … lists every domain it completes on"* (`:52-54`). §5.1's single-element
  declaration is **wrong as written**; Fire B must widen it.
- **§5.4's step 4 emits nothing** (`PurgeIdentityDedupFootprint`, "Emits: none"), so it **cannot** advance
  on an event and will *always* take the deadline path — a guaranteed per-instance `StepTimeout` stall plus
  a spurious "check completionDomains" WARN on the happy path. That is a real design defect this probe
  surfaced: either step 4 emits a `privacy.*` event (the cheap fix, and the one consistent with every other
  step), or it is the pattern's last step and the erasure's completion is deliberately deadline-paced.
  **Recommendation: give step 4 an event.** Fire B owns the change; it does not alter the architecture,
  only the op's emission and one line of §5.1.
- **Sizing verdict for Fire B: unchanged.** The mechanism the design assumed is the mechanism that exists.

### What Fire A shipped

`StepSpec.Reads`/`OptionalReads` (`internal/pkgmgr`), carried through `loomPatternSpecBody` into the
`meta.loomPattern` spec body, deserialized onto `loom.Step`, validated systemOp-only against the
`subject` / `subject.<aspect>` template grammar, and resolved against `inst.SubjectKey` in
`submitSystemOp` — the arm that has passed `nil, nil, nil` to `buildOutbox` since Phase 2.

**Contract edit prepared for Andrew (ratified + committed 2026-08-08, `1ad3f1c4`, trimmed to its
normative core):** `docs/contracts/10-orchestration-loom.md` §10.5 step
shape, extended with the two systemOp-only read fields, the localName constraint on the aspect segment,
and the `subjectKey`-names-a-vertex requirement the grammar's confinement rests on. Affected consumers:
`internal/loom` (Step, validate, submitSystemOp, the trigger guard), `internal/pkgmgr` (StepSpec, spec
body, validateLoomPatterns, StepArtifact) — all in this fire; no shipped pattern declares either field,
so every installed spec body is byte-identical and nothing in `packages/` needs reinstalling.

### What the adversarial pass changed (3-layer: blind hunter · edge-case hunter · acceptance audit)

Contract-adjacent + read-declaration plane, so the fire took the full depth despite being S–M. Two real
defects, both found independently by two hunters, both fixed before the merge rather than filed.

**D1 — the aspect segment was checked for "non-empty and dot-free", which is not the predicate the
rendered key must satisfy.** A NATS KV key's charset is narrower: `subject.*`, `subject.>`,
`subject.pii key`, `subject.pii/key` and any non-ASCII aspect passed BOTH validators, and the rendered
key then failed hydration with `ErrInvalidKey` — which is not the `ErrKeyNotFound` absence branch but a
hard error every redelivery reproduces, so the step wedges and the instance rides its deadline to a
failed terminal. This is precisely the "installs cleanly, then runs dark" outcome the two-validator
lockstep exists to prevent, arriving through a gap the lockstep did not cover. Fixed by holding the
aspect to the Contract #1 localName — and by EXPORTING `substrate.IsValidLocalName` rather than writing
a third regex, so the check is the same predicate `ParseAspectKey` applies to the finished key.

**D2 — the confinement claim rested on an unbound input.** "Subject-relative ⇒ reaches only the
subject's own keys" holds only if `subjectKey` names a vertex, and §10.9's trigger payload is
caller-supplied and was checked for non-emptiness alone (`engine.go` validates `instanceId` with
`IsValidNanoID` two lines away, and nothing equivalent for the subject). A caller passing
`vtx.identity.<id>.piiKey` would make the bare `subject` token name the aspect itself. `StartLoomPattern`
is granted only to `operator` at `scope:any`, so this was not an escalation for an unprivileged caller —
but the grammar this fire ships is what makes the property load-bearing, so the input is now bound to a
three-segment vertex shape at the point it enters, not re-derived by each consumer.

**Refuted / no action.** The blind hunter read the `edge-manifest` version bump as stray; it is required
by `lint-package-version` (the acceptance audit confirmed it, with precedent `7fb9f505`). Both hunters
raised the absence of a `MaxDeclaredReads` (1000) bound in the pattern validators: real, but the
Processor's envelope parse already rejects terminally and loudly, and no hand-authored pattern
approaches it — noted here rather than filed, since it is one line in the shared validator whenever a
generator ever emits step reads.

**Verified correct under attack** (worth recording so a later fire does not re-litigate): the two
validators are logically identical on every input either hunter could construct; `nil` vs `[]string{}`
collapses identically at every layer (`systemOpReads` → `omitempty` on `Step`/`outboxRecord` →
`actuator.go`'s `len(...) > 0` ContextHint gate), so a read-free lifecycle op's outbox record and
`ops.<lane>` envelope are byte-identical to today; an already-installed spec aspect with no `reads` key
loads and runs unchanged, and the pattern PIN round-trips the new fields, so a crash-recovered instance
keeps its declared reads. On the shelved "[Processor] A declared read is never scope-checked against the
operation" row: this change **rides** that gap and does not widen its reach (a DDL's `derive_reads`
already replaces the submitter's set wholesale, and any direct submitter can already name any key) — what
is new is the authoring surface, so whenever that row is built, the scope check must cover this path,
whose keys arrive on an op submitted under Loom's own service actor.

---

## Fire B build note — increment 1 (2026-08-07): the erasure marker

Fire B is L–XL and spans fires (§12's four internal steps). This is the first landing: **§6's marker
aspect and the op that writes it**. The five write-path gates and `UnbindIdentityCredentials` — the rest
of step 1 — are the next increment. The split follows the design's own dependency arrow (the gates read
the marker; the marker reads nothing) and it opens no regression window, because nothing here removes or
changes any existing behaviour. §12's regression argument was about narrowing the op FIRST, which is
step 3 and still sequenced last.

### Fire brief

**1. Scope (§12 Fire B step 1, narrowed to its first term).** `SealIdentityForErasure` (privacy-base)
writing `.erasureRequested`. Green bar: `go test ./packages/privacy-base/`, full `go test ./... -p 4`,
and the lint gate set.

**2. Verified touch-list** (anchors checked live against `c414a9d5`): a new
`packages/privacy-base/seal_identity_for_erasure.go`; `ddls.go`'s `DDLs()` (`:28`, tail `:79-80`);
`permissions.go`'s `Permissions()` (`:20`); `manifest.yaml` (`:2` version, `:11` ddls, `:27`
permissions); `package.go`'s `Version` (`:27`); `package_test.go`'s counts (`:19`, `:25`); a new test
file.

**3. Precedents mirrored.** `MarkExpired` (`packages/orchestration-base/mark_expired.go:189-227`) — the
one-aspect-one-event op shape. `RecordShredFinalization` (`shred_identity_key.go:356-390`) — the
fail-closed-on-unshredded precondition. `FreshnessExpiryAspectDDL` — an aspect-type DDL whose
`PermittedCommands` IS the write gate. `KeyShreddedEventDDL` — a registered event-type DDL.
`record_shred_finalization_test.go` — the test harness. Nothing here is greenfield.

**4. In-scope gotchas.** `lint-package-version` requires the manifest bump (`package.go` parity is pinned
by `TestPackage_ManifestMatchesDefinition`, not by the gate). **There is no
`make verify-package-privacy-base` target and no `scripts/verify-package-privacy-base.go`** — this
package has never had a live-install verify script, so the applicable gates are the generic ones
(`lint-conventions`, `lint-package-standard`, `lint-package-version`, `lint-lens-anchors`, `vet`,
`golangci-lint`) plus the package's own tests. Test fixture NanoIDs must avoid `I l O 0`.

**Scope-diff gate: clean, narrow-only** — every touch traces to §6; nothing substituted, nothing widened.

### Three decisions the design left to the build

- **The marker is its own aspect, not a flag on `piiKey`** — as §6 argued, and the build confirms the
  seam argument is the stronger one: `piiKey` is privacy-base-owned while the gates that consume the
  marker live in identity-domain and identity-hygiene, so the marker is a contract between packages.
- **The op's payload field is `subjectKey`, not `identityKey`.** Loom's `submitSystemOp` builds
  `{"subjectKey": inst.SubjectKey}` itself and a pattern cannot reshape it; the Weaver's `directOp` gaps
  in §7.2 dispatch the same field. See the obligation below.
- **It fail-closes on an unshredded identity** (`ErasureNotShredded`). §5.4 called the write an
  "unconditioned upsert" and the build kept that for the WRITE, but added a precondition on the READ: the
  seal copies `piiKey.shreddedAt` as the cycle discriminator §5.5 and §7.1 both depend on, and a null
  discriminator makes the residue lens's `sealedForShreddedAt <> requestedForShreddedAt` compare
  null-with-null and read as *sealed*. A seal with no shred behind it would therefore attest a completion
  it never earned — the exact failure §8.6 rejects. The same reasoning already governs
  `RecordShredFinalization`.
- **A re-seal preserves `requestedAt` and refreshes `shreddedAt`.** §5.4's "unconditioned upsert" would
  restamp both; the first request is the legally meaningful instant, and refreshing only the discriminator
  is what makes §5.5's "a re-shred invalidates the old seal by field-diff without tombstoning anything"
  actually true.

### An obligation this increment hands the pattern increment (step 2)

**`ShredIdentityKey` takes `identityKey`; a Loom `systemOp` step submits `{"subjectKey": …}`.** §5.1
declares step 1 as `systemOp ShredIdentityKey`, and as written that step submits a payload the op does
not understand — it would fail `InvalidArgument: identityKey: required` on every instance. The design
never named this. It is the pattern increment's to resolve (accept `subjectKey` as an alias on the shred
op, or teach the step kind to carry a payload field name); it is not resolvable here, because nothing
submits the shred as a step until the pattern exists. Recorded rather than filed: it lives inside this
same live item.

The other standing obligation is unchanged and still owed by whichever increment declares the pattern's
reads: the `TestUserTaskReads_CoverEndpoints` analogue per step op (§13, Inc 2), pinning a step op's
declared read-set to what its DDL actually reads.

### What increment 1 shipped

`erasureRequested` (aspectType, `PermittedCommands: [SealIdentityForErasure]`), `sealIdentityForErasure`
(vertexType) with its op, and `privacy.erasureRequested` (eventType) — plus the `scope:any` grant to
`operator`, the manifest/version bump (0.3.0 → 0.4.0) and 12 tests through the real commit path.

### What the adversarial pass changed (3-layer: blind hunter · edge-case hunter · acceptance audit)

Capability-plane change (a new grant, a new write-gate aspect), so it took the full depth despite being M.
Every finding below was fixed before the merge, not filed.

**A new refusal the design never anticipated — a merged-away identity.** `MergeIdentity` does not
tombstone the secondary: it writes `.state = merged` and `.mergedInto` and leaves the vertex alive, having
already tombstoned that identity's inbound `boundTo` edges and repointed its `identityindex` edges onto
the survivor. So §7.1's residue lens, anchored on a sealed secondary, counts **zero on its first
projection** while every credential and index representing that person lives on un-erased under the
survivor. That is §8.6's success-signal-on-silent-failure, reached by an ordinary ordering (merge, then an
erasure request naming the pre-merge identity). §6's gate table covers only the reverse ordering. The op
now refuses with `IdentityMerged` and names the survivor. **This belongs in §6's table as a sixth row.**

**Two false claims about the Weaver.** The op's doc asserted that the Weaver's `directOp` dispatches
`subjectKey` as an engine constraint — its params are author-declared (`internal/weaver/strategist.go`),
and it is §7.2's gap actions that choose the field. The permission Note named "the Weaver's erasure
target" as a submitter; **no ratified gap dispatches `SealIdentityForErasure`** (§7.2 dispatches
`SealIdentityForErasureComplete`). Both corrected.

**The `permittedCommands` claim was stronger than step 6 enforces.** A tombstone carries no document
(Contract #3 §3.3), so its class is empty and the DDL block is skipped entirely — no aspect-type DDL can
refuse a tombstone, and the declaration gates the **class**, not the key. Non-removal of the marker, which
§7's convergence rests on, is a convention held by code review. The comments now say so, matching what
`piiKey`'s own entry already admitted about the identical mechanism.

**Two tests passed for the wrong reason.** The re-seal test could not fail on the `requestedAt`
preservation it named — one hardcoded `submittedAt` made both seals stamp the same value, so deleting the
preservation branch left it green. And the wide-subject test seeded **one** link rather than 300, because
`testutil.GenReqID` is a pure function of its label. The fixture now asserts its own width, which
immediately caught a second collision: `GenReqID` silently drops characters outside the NanoID alphabet,
`'0'` among them, folding `Cr10` onto `Cr1`. Also added: the missing-`shreddedAt` arm, a non-identity
target, and name-pins for the three new DDLs and the new grant (`package_test.go` counted them but its
`wantDDLs`/`wantPerms` slices bounded their own loops, leaving the tail unpinned).

**Two findings recorded rather than fixed, because neither has an in-script fix.**

1. **The cycle discriminator is not OCC-protected.** `piiKey` is a class-(d) `optionalReads` served from
   the step-4 snapshot and this op does not mutate it, so nothing conditions the commit on the envelope
   still being what was read; a shred committing concurrently with a seal can persist the previous cycle's
   `shreddedAt`. Narrow (the pattern runs these sequentially) and self-announcing (a re-shred clears the
   finalization booleans, so the row surfaces as critical rather than attesting). A script cannot
   `expectedRevision` a key it does not mutate.
2. **§5.1's step-2 guard defeats the re-seal, and must change in the pattern increment.** The guard is
   `{"absent": "subject.erasureRequested.data.requestedAt"}`. On a second erasure for the same person the
   marker already exists, so **step 2 skips**, `shreddedAt` is never refreshed, and §7.1's
   `missing_erasureSeal = (sealedForShreddedAt <> requestedForShreddedAt)` evaluates old-versus-old =
   **false** — the new erasure reads as already attested on the previous cycle's evidence. Nothing else
   can refresh the marker: this op is the only writer of the class, and no Weaver gap dispatches it. The
   same hole opens for an out-of-band re-shred via Loupe's shred button with no re-seal. **Fire B step 2
   must drop that guard or key it on `shreddedAt`.**

**Verified correct under attack** (recorded so a later fire does not re-litigate): the Starlark guards are
fail-closed on every payload and envelope state either hunter could construct (`shredded` compared with
`!= True` is type-strict, so `"true"` and `1` are rejected); `live_data`'s attribute access cannot raise,
because `vertexDocToStarlark` always populates `isDeleted` and `data`; the double appearance of
`SealIdentityForErasure` in two DDLs' `PermittedCommands` is safe, since `commandIndexEligible` excludes
aspectType from the operationType→class reverse index, so Loom's class-less dispatch resolves uniquely;
both `events.privacy.*` consumers filter on the exact `keyShredded` subject, so the new event class breaks
nothing; and no golden file, bootstrap json or install script pins privacy-base's version.

---

## Fire B build note — increment 2 (2026-08-07): the write-path gates

Increment 1 shipped the marker. This is the half of §12 Fire B step 1 that reads it: **§6's fail-closed
gates**. `UnbindIdentityCredentials` — the remaining term of step 1 — is increment 3; the split follows
§6's own dependency arrow (a gate reads the marker; the unbind op does not) and opens no regression
window, because a gate only ever refuses a write that today succeeds against a sealed identity.

### Fire brief

**1. Scope (§12 Fire B step 1, second term).** The §6 gate table: every writer of an erasable
representation of a person reads `subjectKey + ".erasureRequested"` and fails closed. Green bar:
`go test ./packages/identity-domain/ ./packages/identity-hygiene/`, full `go test ./... -p 4`, and the
lint gate set.

**2. Verified touch-list** (anchors checked live against `a6fd4bff`). `packages/identity-domain/ddls.go`:
`derive_reads` (`:794`, its `ClaimIdentity`/`CompleteCredentialLink` arm `:826` and its
`ReconcileCredentialBinding` arm `:861`); the gate helper beside `enforce_not_merged` (`:628`);
`ClaimIdentity` (`:1142`), `CompleteCredentialLink` (`:1370`), `ReconcileCredentialBinding` (`:1578`).
`packages/identity-hygiene/ddls.go`: a new `derive_reads` and the gate in `execute`. Plus both packages'
`manifest.yaml` + `package.go` version and any `package_test.go` count, and a test file each.

**3. Precedents mirrored.** `read_merged_into` + `enforce_not_merged`
(`identity-domain/ddls.go:620-629`) — the read-an-aspect-then-refuse gate shape, already applied at four
sites. `derive_reads`' existing arms (`:826-875`) — the class-(g) derivation that spares every dispatcher.
`fail_claim` / `fail_link` / `fail_reconcile` — the typed refusal closures, whose outcome string is
internal (NFR-S6 reclassifies the wire code) so a specific outcome costs no enumeration oracle.
`seal_identity_for_erasure.go:376` — the `kv.Read` + `# read-posture: (d)` form for this exact aspect.

**4. In-scope gotchas.** The gate reads via `kv.Read`, not `state[…]`: a `state` lookup of an undeclared
key reads as absent, so a dispatcher that failed to declare would silently open the gate, while an
undeclared `kv.Read` falls through to a live GET (`internal/processor/starlark_kv.go:115-121`) and still
refuses. Declared-and-absent costs no round trip (`KnownAbsent`, `:111`). Every `kv.Read` in `packages/`
must carry a `# read-posture:` annotation — those findings are **blocking**, not advisory
(`scripts/lint-conventions.go:869`). identity-hygiene has no `derive_reads` today; adding one is a new
entrypoint for that package but a shipped platform mechanism.

**Scope-diff gate: narrow-only, with one row resolved rather than built** — see below. Nothing widened,
no adjacent mechanism substituted.

### §6's fifth row — the brief got this wrong, and the review caught it

The brief filed with this fire argued §6's fifth row (*`index_vertex_mutation` callers: **do not
revive** for an erasure-requested identity*) named a hazard that could not occur, and proposed building
four gates instead of five. **That was wrong.** All three review layers converged on it independently,
and the gate is built. The reasoning is recorded because the *shape* of the error is worth keeping:

- The brief's factual claims were all true. `index_vertex_mutation` does have exactly one caller, and
  the document it revives does always carry the submitting op's own newly-minted identity key, so a
  revive genuinely cannot re-create an `identityindex` naming a sealed identity.
- What it missed is that the revive is not the only thing `CreateUnclaimedIdentity` does with a contact
  hit. A **live** hit produces a `duplicateOf` link naming the incumbent, plus its match criteria in
  plaintext. If the incumbent is sealed, that link is a brand-new correlation to a person who asked to
  be forgotten, written after the seal. It needs no exotic sequence: the **name index always matches**,
  so an ordinary same-named walk-in registration during the convergence window does it.
- The brief's fallback argument — the sweep keeps running and `SealIdentityForErasureComplete`
  re-verifies in its own commit — is an argument about *eventual convergence*. §6 requires something
  strictly stronger: the residue set must be **monotonically non-increasing** after step 2. A new
  correlation link is a monotonicity break whether or not something later reaps it. Worse, the fallback
  leaned on an op that does not exist yet, and on that op enumerating `duplicateOf` — an obligation
  nothing had recorded.

**The lesson, stated for the next fire:** a gate row was dismissed by tracing the *mechanism the row
named* (`index_vertex_mutation`) rather than the *hazard the row was reaching for* (this op can write a
representation of a sealed identity). The row was mis-scoped, and mis-scoping is a reason to re-aim a
gate, never to drop it.

### What increment 2 built

Five gates, and one more link position than §6's table names:

| Writer | Refusal | Position |
|---|---|---|
| `ClaimIdentity` | `ClaimKeyInvalid: erased` (generic on the wire; Health KV carries the word) | target **and** actor |
| `CompleteCredentialLink` | `ClaimKeyInvalid: erased` | target **and** actor |
| `ReconcileCredentialBinding` | `CredentialReconcileRejected: erased` (verbatim on the wire) | owner **and** credential |
| `CreateUnclaimedIdentity` | none — the sealed incumbent is skipped as a dedup match | incumbent |
| `MergeIdentity` | `ErasedIdentity` | primary **and** secondary |

**Both link positions, not just the target.** The brief also argued the gate should read the target
only, since §7.1 counts `(c)-[:boundTo]->(i)` — inbound. That too was wrong, for a reason the erasure op
itself already records: `ShredIdentityKey`'s `collect_bound_to_links` enumerates `"in"` **and** `"out"`
(`shred_identity_key.go`), because an identity is the target of every credential bound to it and the
**source** when it is itself someone else's credential. Both directions are inside what the shred
erases, so gating only the inbound one left `credentialindex(U)` and `boundTo U→W` creatable for a
sealed U. Gating the actor is not a widening of ratified scope — it is matching the scope the erasure op
already has.

**Gate placement is load-bearing.** Each gate sits *below* the checks that authenticate the caller:
after the claim/link secret comparison, and after `not-bound`/`owner-mismatch` on the reconcile path.
Above them, a wrong-secret brute force against a sealed identity would have been diverted out of
`claim-attempts.invalid-key` — the counter an operator watches — into `claim-attempts.erased`; and
because `CredentialReconcileRejected` is **not** reclassified by the Processor (unlike
`ClaimKeyInvalid`), an early reconcile gate would have answered "is this identity sealed for erasure?"
for any key at all, contradicting that op's own permission Note.

**The marker's CLASS is checked, not just its key.** privacy-base records that its aspect-type DDL gates
the class rather than the key, so any package script can write some other class at
`<identity>.erasureRequested`. A presence-only gate would have let such a write shut a person's claim,
link, reconcile and merge paths permanently, with no op able to remove the marker.

**`derive_reads` validates the NanoID before deriving.** The Processor answers a malformed derived key
with `DeriveReadsInvalid` — a hydration fault raised *before* the operation's own validation — so
deriving straight off an unvalidated payload turns a clean `ClaimKeyInvalid: no-target` into an opaque
`HydrationFailed`, a distinguishable wire code on an NFR-S6-protected path. The same guard was extended
to the pre-existing `credential_bound_to_key` derivations, which had the identical latent hazard.

### Residuals — named, with their consumers

1. **The gate cannot be commit-conditioned.** None of these ops mutates `.erasureRequested`, and step 8
   conditions only mutation keys, so a seal committing between an op's step-4 hydration and its commit
   is not detected: the op commits and the erased set grows by one. A script cannot `expectedRevision` a
   key it does not mutate, and it may not write this key (only `SealIdentityForErasure` may). Same class
   as increment 1's recorded OCC finding. It is recoverable — the new records are residue the sweep
   reaps and the completion seal refuses over — but it means §7.3's "the create cannot happen" is not
   literally true as built. **Consumer: the fire that builds `SealIdentityForErasureComplete`**, whose
   in-commit re-verification is what actually closes this.
2. **`SealIdentityForErasureComplete` must enumerate `duplicateOf` in both directions.** §7.1 counts
   only `boundTo` and `indexes`; §7.2 says the seal "runs the same bounded enumerations" without
   naming them. **Consumer: the same fire** — if it enumerates only the two counted relations, a
   correlation link can survive the attestation.
3. **The gate is wider than the residue anchor.** The gate closes on a tombstoned or body-less marker;
   §7.1 anchors on a live `.erasureRequested.data.requestedAt`. Such a marker would shut the write path
   while producing no residue row — an identity with nothing to converge. Both states need a writer that
   does not exist. Kept deliberately fail-closed. **Consumer: the operator surface (§12 step 4)**, which
   is where a shut-but-unprojected identity would become visible.
4. **`ProvisionConsumerIdentity` re-grants `consumer` to a sealed identity** on its next authenticated
   touch. It writes none of the counted representations, so it is outside the residue model — but nobody
   had written that down. **Consumer: the fire that decides whether erasure should also revoke standing.**
5. **`RotateClaimKey` / `InitiateCredentialLink` / `RecordIdentityPII` do fail closed for a sealed
   identity — via the Vault, not this gate** (the seal requires a shredded `piiKey`, and the vault
   returns `ErrKeyShredded`). They reject with an opaque vault error rather than an erasure refusal.
   Recorded so a later fire does not assume they carry a §6 gate.
6. **`MaxDeclaredReads` boundary on `MergeIdentity`.** Two derived keys now count toward the 1000-key
   ceiling, so a merge that previously parsed at exactly 999–1000 declared keys now exceeds it. That is
   ~495 edges, far beyond anything real, and the script's own 999-mutation preflight is the binding
   constraint. Recorded, not fixed.
7. **Contract #2 §2.5's class-(g) rationale is now narrower than shipped usage.** It describes class (g)
   as existing for "a key a submitter cannot express"; `<identityKey>.erasureRequested` is trivially
   expressible and is derived for a different, stronger reason — *a gate a submitter can disable by
   omission is not a gate*. No normative clause is broken (the derivation is deterministic, grammar-valid,
   weakest-wins, within the ceiling), so no contract edit is staged; a one-sentence broadening of that
   rationale is the honest resolution whenever §2.5 is next opened.

### Health-KV surface

`claim-attempts.erased` is a new outcome in a documented bounded enum;
`docs/observability/health-kv-schema.md` is updated in the same change. It matters more than a usual
counter: NFR-S6 strips the outcome word from the reply, so Health KV is the **only** channel that
carries it. The tests assert the counter directly — without that, swapping `fail_claim("erased")` for
any existing outcome would have left every behavioural assertion green.

---

## Fire B build note — increment 3 (2026-08-07): the unbind primitive

Increment 1 shipped the marker, increment 2 the five gates that read it. This is the **last term of
§12 Fire B step 1**: `UnbindIdentityCredentials`, the op that takes the credential-plane half of the
cascade out of `ShredIdentityKey`'s commit. Step 1 is complete when this lands; §12 step 2 (the
pattern, the residue lens, the Weaver target, the completion seal) is the next fire.

### Fire brief

**1. Scope (§12 Fire B step 1, third term).** `UnbindIdentityCredentials` in `identity-domain` per
§5.4: owner from `subjectKey`, no last-credential guard, `Scope:"any"` to the service actors, copying
`UnlinkCredential`'s body — tombstone `credentialindex`, tombstone `boundTo`, rewrite the
`credentialBinding.credentials` array, emit `identity.unbound`. Green bar:
`go test ./packages/identity-domain/`, full `go test ./... -p 4`, and the lint gate set.

**2. Verified touch-list** (anchors checked live against `c5150d23`). New file
`packages/identity-domain/unbind_identity_credentials.go` (DDL + script, mirroring
`packages/privacy-base/seal_identity_for_erasure.go:223-398`); new
`unbind_identity_credentials_test.go`. Edited: `package.go` (DDL list + version),
`permissions.go` (the grant, after `UnlinkCredential` at `:95-99`), `manifest.yaml` (version,
`declares.ddls`, `declares.permissions`), `package_test.go` (the DDL/permission count pins at
`:48-59`). Read-only anchors: `ddls.go:829-830` (`credential_index_key`), `:835-843`
(`credential_bound_to_key`), `:1671-1761` (`UnlinkCredential`'s body), `:409-434`
(the `credentialBinding` aspect DDL — `permittedCommands` intentionally empty),
`:514-541` (`boundTo` linkType — same open posture), `shred_identity_key.go:231-266`
(`collect_bound_to_direction`/`collect_bound_to_links`, both directions),
`internal/gateway/credential_bindings_materializer.go:143-153` (the one DELETE fold).

**3. Precedents mirrored.** `seal_identity_for_erasure.go` — the whole file shape: a standalone
`meta.ddl.vertexType` DDL for an erasure-plane op, its own copies of `required_string` / `parts_of` /
`vertex_alive`, a `Scope:"any"` grant to `operator` carrying a `[no-op-meta: engine-op — …]`
exemption, no descriptor. `shred_identity_key.go`'s `collect_*_direction` — the class-(e) paged
`kv.Links` walk with its `# read-posture: (e) relation=… epoch=none (…)` annotation.
`UnlinkCredential`'s mutation triple and `identity.unbound` payload shape.

**4. In-scope gotchas.** `crypto.sha256NanoID` is a Processor builtin, so the index-key derivation
copies verbatim. Neither `boundTo`, `duplicateOf` nor `credentialBinding` declares
`permittedCommands`, and `credentialindex` has no vertexType DDL at all, so no write gate needs
widening for the new verb. `kv.Links` **must** carry a class-(e) annotation naming `relation=` and an
`epoch=` — `scripts/lint-conventions.go:869` makes those findings blocking, not advisory.

**Scope-diff gate: three resolutions, all narrow-or-match, none substituting an adjacent mechanism.**
Two of them are the interesting ones and are recorded below. Nothing widened.

### Three things the ratified §5.4 did not settle, and how the build settled them

**(a) Both link directions, because that is the scope the op being narrowed already has.**
§5.4 says the op "takes the owner from `subjectKey`", which reads as the inbound direction, and §7.1
counts only `(c)-[:boundTo]->(i)`. But `ShredIdentityKey` tombstones `boundTo` in **both** directions
today (`shred_identity_key.go:251-266`, and its own comment says why: an identity is the target of
every credential bound to it and the **source** when it is itself someone else's credential — a
merged-away identity folded into the survivor, or a Scenario-B identity later linked to another).
§12's build order exists precisely so "erasure never does less than it does today", so an
inbound-only unbind would make step 3's narrowing a behavioural regression. Covering both is not a
widening of ratified scope — it is matching the scope the op being narrowed already has, the same
resolution increment 2 reached for the gate's actor position.

**(b) The erased subject's OWN `credentialBinding` array is not rewritten — it cannot be, and it
does not need to be.** This is the finding that reshaped the increment, and it is a correction to
§5.4's "copies `UnlinkCredential`'s body" wherever that body touches the subject's own aspect.

`credentialBinding` is `Sensitive: true` (`ddls.go:409-434`). A sensitive aspect is decrypted on the
way in and encrypted on the way out under the anchoring identity's DEK, and
`internal/vault/local.go:381` refuses both for a shredded envelope — durably, off `envelope.Shredded`,
not merely the in-memory deny-list. This op's precondition is the seal, the seal's precondition is a
shredded `piiKey` (§6, increment 1), so **by construction the subject's DEK is always dead when this
op runs**. Reading that aspect — declared or via `kv.Read` — would fault in hydrate
(`sensitive_decrypt.go:225`), and every redelivery would reproduce it: the op would be unable to
erase anyone, which is the exact failure class this design exists to remove.

Applying increment 2's lesson — trace the *hazard* a clause reaches for, not the *mechanism* it
names — the hazard is **a live, readable list naming the erased person's credentials**. Three
surfaces carry one, and all three are closed:

| Surface | Readable after the shred? | Closed by |
|---|---|---|
| Subject's own `credentialBinding.credentials` | **No** — ciphertext under a destroyed DEK | the shred itself; that *is* crypto-erasure |
| `credential-bindings` KV bucket (`actorKey → {identityKey}`, plaintext, outside Core KV) | **Yes** | the `identity.unbound` emission — the plane's only row-set shrink |
| Owner `W`'s array, where the subject is itself `W`'s credential (outbound) | **Yes** — `W` is not erased, `W`'s DEK is alive | the array rewrite, applied to `W` |

So the array rewrite survives exactly where it is both possible and load-bearing, and the emission —
not the rewrite — is what closes the inbound side. That is also why §9.1 can claim this realises old
option B's full intent: the Gateway seam's retraction was always the part that mattered.

**(c) The op fail-closes unless the subject is sealed (`ErasureNotSealed`).** §5.4 states no
precondition and §5.1 makes the step guardless — but a guardless *step* is a statement about Loom
skipping, not about the *op's* own preconditions. Without one, a `Scope:"any"` grant to `operator`
is a bare "tombstone anyone's sign-in methods" verb held by five service actors. Requiring the
`.erasureRequested` marker gives the grant the same fail-closed justification
`SealIdentityForErasure`'s own permission Note rests on — *it confers no authority a completed seal
has not already exercised* — and costs the pattern nothing, since §5.1 orders the seal at step 2 and
this op at step 3. A refusal added is a narrowing, which the scope-diff gate permits.

The marker is read with the same class-check increment 2 built (`marker_closes_write_path`): the
document's `class` must be `erasureRequested`, so a foreign write at that key cannot arm this verb
any more than it can shut a person's claim path.

### The bound, and why one direction per invocation

§5.4 declares `≤ 2·PAGE + 1` mutations and §10 requires that **no step can reintroduce a refusal**.
A single page of one direction lands exactly on that bound:

- inbound page — 2 mutations per credential (its `credentialindex` + the link), plus at most one
  array rewrite: `2·PAGE + 1`;
- outbound page — the subject's own `credentialindex` once (the same key for every outbound link,
  deduped), plus one link tombstone and at most one owner-array rewrite per link: `2·PAGE + 1`.

Draining **both** directions in one commit would reach `4·PAGE + 2 = 1026` and trip step 8's
`BatchTooLarge` — a refusal, on exactly the well-connected person §2 says must never be refusable.
So the op takes **inbound first, and outbound only when inbound is exhausted**, one page, no loop,
no cap-failure branch. Convergence is not this op's job: §5.5's whole point is that the Loom spine is
an accelerator, and §7.2's Weaver tail re-dispatches until the residue reaches zero. A subject with
more than `PAGE` credentials gets one page from the pattern's step 3 and the rest from the tail.

### Residuals — named, with their consumers

1. **`StepSpec` has no `Enumerations` field.** Fire A added `Reads`/`OptionalReads`; a class-(e)
   enumeration is declared through `ContextHint.Enumerations`, which the Loom systemOp submit path
   cannot express. The declaration is metadata the Processor validates and otherwise ignores
   (`opwire.go:66-116`), so the walk executes correctly either way — but the pattern's step will
   submit this op with an undeclared enumeration, which is a posture gap, not a runtime one.
   **Consumer: the fire that declares the `identityErasure` pattern** — it either adds the field
   alongside Fire A's two or records the omission against Contract #2 §2.5.
2. **The §13 read-set coverage guard still has nothing to compare against.** Same state increment 1
   recorded: until a pattern declares this op's reads, the test helper's literal is the only thing
   pinning them. **Consumer: the pattern fire**, unchanged.
3. **A subject that is `W`'s *last* credential locks `W` out.** No last-credential guard, by §5.4's
   ruling, and correctly so — the credential is being erased, so leaving `W`'s array pointing at it
   would preserve a correlation to an erased person in exchange for a sign-in path that no longer
   authenticates. Recorded because it is a real operator-visible consequence with no surface that
   announces it. **Consumer: the operator surface (§12 step 4).**
4. **Same non-commit-conditioned window increment 2 recorded.** The seal is read, not mutated, so a
   seal committing between this op's hydration and its commit is not detected. Identical class,
   identical closure. **Consumer: the fire that builds `SealIdentityForErasureComplete`.**


### What increment 3 shipped

Shipped `d66cb731`. **§12 Fire B step 1 is complete**; step 2 (the pattern, the residue lens, the
Weaver target, the completion seal) is the next fire.

### What increment 3 built

`UnbindIdentityCredentials` in `identity-domain`, as its own `meta.ddl.vertexType` DDL
(`unbind_identity_credentials.go`, mirroring privacy-base's `seal_identity_for_erasure.go`), granted
`Scope:"any"` to `operator` with a `[no-op-meta: engine-op — …]` exemption and no descriptor. Package
`0.16.0 → 0.17.0`.

| Direction | Per link | Owner array | Emits |
|---|---|---|---|
| inbound (credentials bound to the subject) | tombstone `credentialindex(cred)` + the link | **not rewritten** — see below | `identity.unbound{identityKey: subject, actorKey: cred}` |
| outbound (the subject IS someone's credential) | tombstone `credentialindex(subject)` once + the link | **rewritten** — the owner's key is alive | `identity.unbound{identityKey: owner, actorKey: subject}` |

Inbound first, outbound only once inbound is exhausted: draining both in one commit would reach
`4·SWEEP+2` mutations, and the point of the decomposition is that no input grows the commit.

### Three corrections the build made to the ratified text

All three are recorded in the design body above, not only here — a build note nobody re-reads is not
where a falsified premise belongs.

1. **§10 point 4's implementation claim was false** — a tombstone is a soft delete, so the specified
   cursor-less `kv.Links` call stalls silently instead of converging. Body rewritten; the *property*
   it guarantees is unchanged, and **Inc 5's lint rule must be written to the property, not to the
   literal "no page loop"**.
2. **§5.4's `PAGE = 256` cannot size a commit** — `2·256` mutations exceeds the 250ms Starlark wall
   budget on a loaded host. Caught by the full `-p 4` suite; the package alone passed 3/3 quietly,
   which is exactly why the wide-blast-radius suite is the gate. `SWEEP_LIMIT = 64`.
3. **§5.4's "copies `UnlinkCredential`'s body" cannot include the subject's own array** —
   `credentialBinding` is sensitive, the seal's own precondition guarantees the subject's DEK is
   shredded, and `internal/vault/local.go` refuses decrypt and encrypt alike off
   `envelope.Shredded`. Tracing the *hazard* (a live readable list naming an erased person) rather
   than the *mechanism*: the subject's array is already erased by key destruction, the Gateway's
   `credential-bindings` bucket is the readable copy, and `identity.unbound` is its only retraction.

Plus one addition: **`ErasureNotSealed`**. §5.4 states no precondition, but without one a
`Scope:"any"` grant to `operator` is a bare "tombstone anyone's sign-in methods" verb held by five
service actors. Requiring the marker — checked by **class**, per increment 2 — costs the pattern
nothing (§5.1 orders the seal first) and lets the grant claim what the seal's own Note claims. A
refusal added is a narrowing.

### The convergence proof

`TestUnbindIdentityCredentials_WideSubject_ConvergesPastOnePage` seeds 301 inbound links and requires
every pass to **strictly decrease** the residue until it reaches zero. It was mutation-tested twice
against the single-page implementation §10 specified, and fails against it both times — which is what
makes it a proof rather than a claim. It asserts convergence rather than arithmetic, so tuning
`SWEEP_LIMIT` does not touch it.

Six siblings cover: the inbound sweep with the subject's aspect **revision-pinned unchanged** (the
load-bearing negative for correction 3); the last credential going with no refusal; `ErasureNotSealed`
and the foreign-marker-class refusal, each **paired with its own positive vector over one corpus**;
the ungranted-actor denial against a control holding the same role on the same lane; and idempotence,
asserted as an unchanged revision on an already-tombstoned key rather than as a bare re-accept.

### Residuals — named, with their consumers

1. **`StepSpec` cannot declare an enumeration.** Fire A added `Reads`/`OptionalReads`; a class-(e)
   walk is declared through `ContextHint.Enumerations`, which the systemOp submit path cannot
   express. Metadata only (`opwire.go:66-116` — the Processor validates the shape and otherwise
   ignores it), so the walk executes correctly; the declaration is what is missing. **Filed** as a
   Loom row. **Consumer: the pattern fire.**
2. **The scan window is finite** (§10 point 4, above). Past ~16k tombstoned links in one direction
   the sweep cannot reach the live ones and fails loudly. **Consumer: the shelved hard-delete
   mutation verb**, whose board row already records that a tombstoned key persists and is still
   enumerated by `kv.Links` — this is that row's first real driver.
3. **The op names no `primaryKey`.** The reply-constraint requires one to lie inside the write
   footprint, and every key this op writes belongs to a credential, a link, or another person's
   aspect — never the subject. Nothing needs one today. **Consumer: any future dispatcher that
   correlates on the reply rather than on the request id.**
4. **The §13 read-set coverage guard still has nothing to compare against** — unchanged from
   increment 1. **Consumer: the pattern fire.**
5. **An erased subject that was an owner's LAST credential locks that owner out.** Correct per
   §5.4's no-last-credential-guard ruling — the credential no longer authenticates either way, and
   leaving it named would preserve a correlation to an erased person — but it is an operator-visible
   consequence no surface announces. **Consumer: the operator surface (§12 step 4).**
6. **Same non-commit-conditioned window increments 1 and 2 recorded.** The marker is read, not
   mutated. **Consumer: the fire that builds `SealIdentityForErasureComplete`.**

## Fire B build note — increment 4 (2026-08-07): the dedup-plane sweep

### Fire brief

**Scope sentence, verbatim from §12 Fire B step 2's list:** `PurgeIdentityDedupFootprint`.

Step 2 names six deliverables — this op, the `identityErasureResidue` lens + `privacy-erasure`
bucket, the `identityErasureComplete` weaverTarget, the `identityErasure` pattern, the two `surface`
gaps, and `SealIdentityForErasureComplete`. This increment builds **only the op**, for the same
reason increment 3 built only `UnbindIdentityCredentials`: it is the second of the two sweeps the
target dispatches, and both must exist before a target that dispatches them can be written against
anything real. Every other step-2 deliverable is a **non-goal here**.

**Touch list (verified live).**

| File | What |
|---|---|
| `packages/privacy-base/purge_identity_dedup_footprint.go` | NEW — the `meta.ddl.vertexType` DDL + Starlark, mirroring `identity-domain/unbind_identity_credentials.go` |
| `packages/privacy-base/ddls.go:92-97` | add `PurgeIdentityDedupFootprintDDL()` to `DDLs()` |
| `packages/privacy-base/permissions.go:40-54` | `Scope:"any"` → `operator`, with the `[no-op-meta: engine-op — …]` exemption the seal's grant already carries |
| `packages/privacy-base/package.go:37` · `manifest.yaml:2` | `0.5.0 → 0.6.0` (an edit at the same version no-ops on install) |
| `packages/privacy-base/purge_identity_dedup_footprint_test.go` | NEW — the convergence proof + its siblings |

**Precedents to mirror, and the one to refuse.**

- **`unbind_identity_credentials.go` (inc 3) is the shape**, top to bottom: `collect_live_sweep`'s
  read-side paging to `SWEEP_LIMIT = 64` live links, the `ErasureResidueUnreachable` loud stop when
  the page budget is spent on nothing but tombstones, `marker_closes_write_path`'s **class** check,
  one direction per invocation, and no `primaryKey`.
- **`shred_identity_key.go:178-247` is where the three enumerations come from** — `indexes` inbound,
  `duplicateOf` both directions — including the read-posture `(e)` annotations, verbatim in intent.
- **Refuse the shred's tombstone idiom.** The shred writes
  `{"op":"update", …, isDeleted:True, data: <re-read body>}`, which costs a `kv.Read` per
  `identityindex` hit purely to carry the body forward. The `tombstone` verb already does that:
  `buildMutationValue` (`internal/processor/step8_commit.go:407-418`) seeds the document from the
  **prior** body and layers the script's document over it, so a bare
  `{"op":"tombstone","key":…}` preserves class and `data` and sets `isDeleted`. Inc 3 uses the verb;
  this op does too. It drops up to 64 hydrating reads per pass — real headroom against the wall
  budget that killed `PAGE = 256`.

**Increment order + the green check after each.**

1. The DDL + script + registration + grant + version bump → `go build ./...`.
2. The tests → `go test ./packages/privacy-base/ -count=1`.
3. Full-suite + gates → `go test ./... -p 4`, `make vet`, `golangci-lint run ./...`,
   `STRICT=1 go run ./scripts/lint-conventions.go`, `make verify-kernel`,
   `make verify-package-privacy-base`.

**In-scope gotchas, each already grounded.**

- **The mutation cost per class differs, and that sets the sweep order.** An `indexes` hit costs
  **2** mutations (the `vtx.identityindex.<hash>` vertex *and* the link); a `duplicateOf` hit costs
  **1**. Draining all three collectors in one commit reaches `2·SWEEP + 2·SWEEP = 256` mutations. So
  one class per commit, in the order `indexes` → `duplicateOf` out → `duplicateOf` in, capping any
  commit at `2·SWEEP_LIMIT = 128` — the same magnitude inc 3 measured clean at `2·SWEEP+1 = 129`.
- **Cross-package tombstoning of identity-domain's classes is already proven, and the verb makes the
  question moot.** `indexes` and `duplicateOf` both declare `PermittedCommands` **empty** on purpose
  (*"multi-writer, open posture"*, `identity-domain/ddls.go:466-500`), and `ShredIdentityKey` —
  privacy-base — tombstones all three classes today. Under the `tombstone` verb the point does not
  even arise: step 6 derives the class from the mutation **document**, and a document-less tombstone
  skips the DDL lookup entirely (`step6_validate.go:143-155`).
- **`duplicateOf` is enumerated in both directions** because the identity may be either side of the
  pair — the later-arriving identity that matched an incumbent (source) or the incumbent others
  matched against (target). Sweeping one direction would leave live pair evidence naming an erased
  person, which is the whole class this op exists to remove.
- **No events.** §5.4's table gives step 4 `Emits: none`, and it is right: the dedup footprint has
  no read-model seam of its own to retract, unlike the credential plane's Gateway bucket.

**Adjacent finds — filed now, not at ship.** None new. The three residuals this op inherits from inc
3 (the finite scan window, the missing `Enumerations` declaration on a `systemOp` step, the §13
read-set coverage guard with nothing to compare against) are already filed rows and are re-inherited
verbatim rather than re-filed.

**Non-goals.** The residue lens, the `privacy-erasure` bucket, the weaverTarget, the pattern, the
surface gaps, `SealIdentityForErasureComplete`, and narrowing `ShredIdentityKey` (step 3 — and the
shred keeps its enumerations until step 2 completes, per §12's build order, so this op and the shred
deliberately overlap for now).

### What increment 4 built

`PurgeIdentityDedupFootprint` in `privacy-base`, as its own `meta.ddl.vertexType` DDL
(`purge_identity_dedup_footprint.go`, mirroring `identity-domain/unbind_identity_credentials.go`),
granted `Scope:"any"` to `operator` with a `[no-op-meta: engine-op — …]` exemption and no
descriptor. Package `0.5.0 → 0.6.0`.

| Class | Direction | Per hit | Mutations/commit |
|---|---|---|---|
| `indexes` | in | tombstone the `vtx.identityindex.<hash>` source **and** the link | `2·SWEEP_LIMIT` |
| `duplicateOf` | out, then in | tombstone the link only — the other endpoint is a live person | `SWEEP_LIMIT` |

One class per commit, in that order. It emits nothing and names no `primaryKey`, and it refuses a
subject carrying no `erasureRequested` marker **of that class** (`ErasureNotSealed`).

The tombstones carry **no document**: `buildMutationValue` seeds a tombstone's document from the
prior body, and that prior comes from `readPriorDocuments`, a commit-time read independent of
`contextHint`. So class and `data` survive without the `kv.Read` per index vertex the shred spends —
up to 64 hydrating reads a pass, which is real headroom against the wall budget that sized
`SWEEP_LIMIT`.

### The confinement the bodyless tombstone removed, and had to get back

Dropping the document is what buys the reads, and it also drops the only thing that was checking
**what** gets destroyed. Three facts compose: the enumeration's server filter is
`lnk.*.*.indexes.identity.<subjectId>`, so the source **type** is a wildcard; `sourceVertex` is
derived faithfully from whatever the key says; and step 6 derives the class from the mutation
document, so a document-less tombstone skips DDL resolution and never consults `permittedCommands`
on the key being destroyed. Nothing downstream re-checks, and the `indexes` linkType ships
`permittedCommands` **empty** on purpose (*multi-writer, open posture*), so the platform enforces
nothing here either.

A link at `lnk.identity.<victim>.indexes.identity.<subject>` therefore made this op tombstone the
victim's identity root, under a `scope:any` grant. No shipped op creates a non-identityindex-sourced
`indexes` link, so it was safe by **accident of corpus shape**, not by an invariant — and the sibling
this op mirrors is structurally immune for a reason that did not carry over: it runs the source
through `crypto.sha256NanoID` into a derived `credentialindex` key, so a poisoned source can only
tombstone a key that does not exist.

`sweep_indexes` now tombstones the source only when it lies in the `vtx.identityindex.` keyspace. A
foreign source is **skipped, not fatal, and its link still goes** — the link is genuinely the
subject's inbound edge and removing it is what shrinks the residue, so convergence is unaffected;
refusing the whole sweep would let one planted link make a person unerasable, trading a destructive
failure for a fail-open one.

### Three corrections the build made to the ratified text

All three are recorded in the design body above, not only here.

1. **§7.1's residue predicate cannot ride on `indexResidue` alone.** Its justification — *"`duplicateOf`
   and `indexes` are cleared by one op"* — assumed one commit drains both. This build takes one class
   per commit, so a subject whose `indexes` links clear first closes the gap while its `duplicateOf`
   links are still live, and the seal would be written over them. **The lens increment must count
   `duplicateOf` in both directions.**
2. **§5.4 and §10 point 2's `≤ 3·PAGE` bound for step 4 was wrong in both directions** — it
   under-counted the per-hit cost (an `indexes` hit is two mutations) and over-counted the batch. The
   real bound is `2·SWEEP_LIMIT = 128`, held by sweeping one class per commit.
3. **§10's "no input for which the platform can decline to erase a person" is a statement about the
   MUTATION count only.** The read side has a ceiling nothing bounded, and the sweep builds its own
   way into it: the scan is stable and ascending and a tombstone stays in the keyspace, so the prefix
   ahead of the live links grows by `SWEEP_LIMIT` every pass until it fills the window. Past ~16k
   links on one relation the op stops permanently — loudly, which is the repairable failure — and the
   per-pass rescan makes the whole erasure `O(L²/SWEEP_LIMIT)` round trips, which can approach the
   250 ms wall well before the ceiling.

### The proofs

`TestPurgeIdentityDedupFootprint_WideSubject_ConvergesPastOnePage` seeds 300 owned index vertices —
**past the 256-key read page, which is the number that matters** — and requires every pass to strictly
decrease the residue to zero. Mutation-tested against the single-page implementation §10 originally
specified: it **stalls at 46 residue on pass 5**, exactly as the corrected §10 predicts. A 150-link
fixture passes against that same broken build, which is why the number is 300 and must not be
lowered. `…_DuplicateOfOnly_ConvergesPastOnePage` is the same proof for the other arm, which the
indexes test cannot reach.

`…_ForeignSourcedIndexesLink_SpareTheVertex` is the confinement proof, and it fails against the
unguarded build by tombstoning a bystander's identity root.

Seven siblings cover: the ordinary indexes sweep over a footprint `CreateUnclaimedIdentity` really
built; both `duplicateOf` directions on one subject; the cost ordering that sets the batch bound (a
build sweeping the classes together passes every other test and fails this one); `ErasureNotSealed`
and the foreign-marker-class refusal, each **paired with its own positive vector over one corpus**;
the tombstoned marker still arming the verb; absent and tombstoned subjects, likewise paired; the
ungranted-actor denial against a control holding the same role on the same lane; idempotence as an
unchanged revision rather than a bare re-accept; that a tombstone preserves class and `data`; and
that the op **emits nothing**.

### Residuals — named, with their consumers

1. **The lens must count `duplicateOf`** (correction 1). **Consumer: the residue-lens increment**, the
   next unit of §12 step 2.
2. **R1's dispatch-count proof is not in this increment.** §12 step 2 asks for `N > 3·PAGE` proving
   the sweep is dispatched more than three times, but R1's actual risk is the **Weaver's** retry
   budget suppressing at 3 — untestable until a target exists. These tests re-dispatch from the test
   body instead. **Consumer: the `identityErasureComplete` weaverTarget increment.**
3. **The read-side ceiling and the quadratic rescan** (correction 3). **Consumer: the shelved
   hard-delete mutation verb**, whose row already records that a tombstoned key persists and is still
   enumerated by `kv.Links`. Increment 3 named that row as this design's first driver; this is the
   measurement that makes it concrete, and it is a stronger driver than the ceiling alone.
4. **An erased person's still-live index vertex denies a live person their own.** While `A` is sealed
   but not yet swept, `C` registering with `A`'s email sees a live index hit, so no index vertex is
   created for `C` and — because the match is erased — no `duplicateOf` is recorded either. This op
   then tombstones the vertex, and the next registrant revives it pointing at themselves. `C` is left
   permanently absent from the dedup index. Originates in §6's gates; this increment is what makes it
   irreversible. **Consumer: the write-path gate that suppresses the index probe's hit for an
   erasure-sealed owner** — filed as its own board row.
5. **`ErasureResidueUnreachable`, `NotFound` on an absent subject via the enumeration path, and the
   live-read budget's worst case are untested.** The first needs a ~16k-link fixture. The budget's
   worst case is ~49k units against a 60k default — it fits, but the constant's own sizing comment
   does not yet name this op. **Consumer: the fire that raises `SWEEP_LIMIT`, either page constant, or
   the number of relations swept.**
6. **`UnbindIdentityCredentials`' Description carries the same absolute "can never refuse" sentence**
   this increment corrected in its own. A description-only edit still requires an `identity-domain`
   version bump, so it is deliberately not made here. **Consumer: the increment that narrows
   `ShredIdentityKey` (§12 step 3), which touches both packages.**
7. **There is no `make verify-package-privacy-base` target**, so the standard package gate does not
   exist for this package; `lint-package-standard`, `lint-package-version` and the package's own
   tests are what cover it. **Consumer: the fire that adds the target.**

---

## Fire B build note — increment 5 (2026-08-07): the residue lens

### Fire brief

**Scope sentence, verbatim from §12 Fire B step 2's list:** the `identityErasureResidue` lens +
`privacy-erasure` bucket.

Step 2's six deliverables are being built one per increment, for the reason increments 3 and 4
each gave: the pieces the `identityErasureComplete` weaverTarget dispatches must exist before a
target can be written against anything real. Increment 3 built `UnbindIdentityCredentials`,
increment 4 built `PurgeIdentityDedupFootprint`; this increment builds the lens whose gaps
schedule them both. **Non-goals:** the weaverTarget, the `identityErasure` Loom pattern, the two
`surface` gaps, `SealIdentityForErasureComplete`, and narrowing `ShredIdentityKey` (step 3).

**Touch list (verified live).**

| File | What |
|---|---|
| `packages/privacy-base/lenses.go` | `ErasureResidueBucket` const · the `identityErasureResidue` `LensSpec` · `identityErasureResidueSpec` |
| `packages/privacy-base/manifest.yaml` · `package.go` | the lens declaration · `0.6.0 → 0.7.0` |
| `packages/privacy-base/erasure_residue_lens_test.go` | NEW — the full-engine cypher proofs |
| `packages/privacy-base/package_test.go` | the structure pin: lens count, names, and now buckets |
| `internal/refractor/label_derivation_corpus_census_test.go` | the pinned narrowing verdict every shipped lens must carry |

**Precedents to mirror.** `privacy-base/lens_cypher_test.go:64-70` is the parse-and-execute harness
(`lenstest.KVs` + `full.New()`); `identity-domain/lens_cypher_test.go:90-101` is the adjacency edge
fixture, which writes BOTH directions of every link and is what lets one relation be counted from
either endpoint. `objects-base/lenses.go:96-112` is the `OPTIONAL MATCH` + `count()` + `missing_<gap>`
residue idiom; `lease-signing/lenses.go:645-647` is the `count(DISTINCT …)` form.

**Increment order + the green check after each.** (1) spec + registration + version bump →
`go build ./...`; (2) the tests → `go test ./packages/privacy-base/ -count=1`; (3) gates →
`go test ./... -p 4`, `make vet`, `golangci-lint run ./...`, all seven `scripts/lint-*.go`,
`make verify-kernel`.

**In-scope gotchas.**

- **The lens must count exactly what the two sweep ops enumerate — no more, no less.** An arm the
  lens omits is residue no gap reports, swept by an op the Weaver stops dispatching, under a seal
  written over it.
- **`.erasure` does not exist yet** — its DDL ships with `SealIdentityForErasureComplete`. It
  projects null, so `missing_erasureSeal` reads open, which is correct.
- **A `WITH` disqualifies the affected-anchor index** (`hopindex.go:86-90`), so link events reach
  this lens's anchors by BFS. §7.1's own spec carries a `WITH`, so this is not a new cost.

**Adjacent finds — filed now, not at ship.** None new; increments 3 and 4's residuals are
re-inherited verbatim rather than re-filed.

### What increment 5 built

`identityErasureResidue` in `privacy-base` (`lenses.go`), package `0.6.0 → 0.7.0`. One row per
erasure-requested identity, counting live residue in **five** directions and exposing the five
`missing_*` gaps, three `inflight_*` declarations and `violating` that §7.2's target dispatches on.

| Arm | Swept by | Count |
|---|---|---|
| `boundTo` inbound · outbound | `UnbindIdentityCredentials` | `boundInResidue` · `boundOutResidue` |
| `indexes` inbound | `PurgeIdentityDedupFootprint` | `indexResidue` |
| `duplicateOf` outbound · inbound | `PurgeIdentityDedupFootprint` | `duplicateOutResidue` · `duplicateInResidue` |

### Four corrections the build made to the ratified text

All four are recorded in the spec's own doc comment, not only here.

1. **§7.1 counted `boundTo` inbound only.** `UnbindIdentityCredentials` sweeps **both** directions
   (`unbind_identity_credentials.go`'s `sweep_inbound`/`sweep_outbound`) — the subject owns
   credentials *and* is itself someone else's. The outbound direction was residue no gap reported.
   This is the same class of error increment 4 found on the dedup side, on the plane it did not look at.
2. **§7.1's `privacy-erasure` bucket cannot coexist with §7.2's gap table, and this is the one that
   would have failed silently.** The Weaver consumes exactly ONE bucket — `WeaverTargetsBucket`,
   default `weaver-targets` — off `$KV.<bucket>.<targetId>.>`, resolving a target by row-key
   **prefix**. Nothing binds a `WeaverTargetSpec.LensRef` to its lens's bucket at install, so
   `identityErasureComplete` would have installed **green** and never dispatched a single gap: no
   error, no issue, no convergence. The lens therefore takes the shape all ~19 shipped convergence
   lenses have — `weaver-targets` + `ProjectionKind: actorAggregate` + an `Output` descriptor keyed
   `identityErasureComplete.{actorSuffix}` + a `{key: $actorKey}` anchor projecting
   `actorKey`/`entityKey`/`entityId`. **P5 is unaffected** (weaver-targets is a lens target like any
   other), and §7.4's "prove it" was always the identity's own `.erasure.coverage` aspect, not this row.
3. **§5.5's cycle discriminator is the LIVE `piiKey.shreddedAt`, and the marker's copy is not a
   substitute.** They look interchangeable because `SealIdentityForErasure` refreshes the marker —
   but only when it runs, and §5.1's ratified step-2 guard skips step 2 whenever the marker already
   carries a `requestedAt`, which on a re-triggered erasure it always does. A marker-diff therefore
   reads *equal* after a genuine re-shred of a completed erasure: the row goes quiet with cycle 2
   unattested and cycle 1's `sealedAt` sitting on it as though it were the answer.
4. **§7.2's "no `maxretries_<g>` ⇒ the budget term never suppresses" is wrong for a `directOp` gap.**
   `gapSuppressed` falls to the engine's `defaultDirectOpRetryBudget` unless the row declares
   `inflight_<g>` — by *presence*, not truth. The constant-`false` `inflight_*` columns are what
   actually make the re-dispatch uncapped, and the seal gap needs one too (§7.2 named only the two
   sweep gaps).

### Two shapes that would have made the row silently wrong at scale

Neither is a correctness bug in the ratified text; both are ways to write this cypher that pass every
functional test and fail in production, so each is pinned by a test that reds against the wrong form.

- **Five sibling `OPTIONAL MATCH` clauses in one stage** — §7.1's literal shape — make the engine
  build the arms' cross product: 64 credentials × 300 index vertices × 300 duplicate pairs reaches
  **5.76M bindings against the 1M cap and the evaluation is REFUSED**. No row, no gap, no dispatch,
  on exactly the well-connected people this design exists to be able to erase. Staging one arm per
  `WITH` collapses the bindings to one row per anchor between arms; the same subject projects in
  ~150ms. R5's named `count(DISTINCT CASE WHEN …)` fallback does **not** help — it fixes count
  inflation, not the binding cross-product — so the deviation from R5 is deliberate.
- **An arm written neighbour-first** (`(c)-[:boundTo]->(i)`) leads with an unbound, unlabeled node.
  `matchPath` seeds from a path's first node and takes the adjacency walk only when it is already
  bound; otherwise it falls to a whole-bucket `ListKeys` plus a point read per vertex. So each
  inbound arm would scan the entire corpus on every reprojection and, past ~1M vertices, refuse the
  evaluation — the same silent non-erasure, re-keyed on **deployment size**, which nothing about the
  subject bounds, landing on every erasure at once including the trivial ones. All 32 inbound arms in
  the shipped lens corpus are anchor-first; these would have been the only exceptions.

The lens is **broad** in the corpus label census, deliberately: its arms bind unlabeled nodes because
the sweep ops' `kv.Links` walks filter by relation and direction with **no type filter**, so labelling
an arm would count a subset of what the op sweeps.

### The proofs

Ten tests, of which five are mutation-verified against the specific wrong build they exist to catch:
folding `duplicateOf` into `indexes` (§7.1's falsified shape), dropping the `boundTo` outbound arm,
writing an arm neighbour-first, diffing the marker's stale `shreddedAt`, and deleting the residue
conjunction from the seal gate — each reds at least one test, several red two.

`…_ResidueFallsToZeroAsTheSweepsRun` is the convergence proof and walks both ops' **real** sweep
order, requiring that draining one direction or class does *not* close a folded gap. It crosses the
boundary the static tests cannot: a tombstoned link leaves adjacency outright
(`adjacency/builder.go`'s `upsertEdge` → `removeEdge`), which is the mechanism §7.2's "each dispatch
strictly decreases the count" rests on. `…_ArmsSeedFromTheAnchorNotTheCorpus` shrinks the binding cap
rather than growing the corpus, so 400 unrelated bystanders red a corpus-seeded arm in milliseconds.
`…_IsShapedAsAConvergenceLens` pins the weaver quartet so the row cannot be re-privatised.

### Residuals — named, with their consumers

1. **A hard-failing sweep now re-dispatches forever with no escalation.** §7.2's termination proof
   assumes every dispatch tombstones ≥ 1 link; increment 4 falsified that — both sweeps
   `fail("ErasureResidueUnreachable: …")` past ~16k tombstoned links on one relation. This
   increment's `inflight_<g>` declarations opt those gaps out of the default retry budget, and an
   ordinary `directOp` reclaim is never backed off, so `escalateExhaustedGap` can never fire.
   **Consumer: the `identityErasureComplete` weaverTarget increment**, which must declare a
   `maxretries_<g>` for that case or route the loud stop to a `surface` gap.
2. **§12 step 4 and §13 Inc 5 both name a Loupe pane over `privacy-erasure`, a bucket that no longer
   exists.** The operator surface reads the `identityErasureComplete.*` rows in `weaver-targets`
   instead. **Consumer: the operator-surface increment (§12 step 4).**
3. **A tombstoned `erasureRequested` marker drops the row while both sweep ops stay armed.** The ops
   are deliberately tombstone-blind on the marker (*"a gate that reopened on a tombstone would be the
   one failure mode a fail-closed guard may not have"*); the lens cannot be, because the engine
   resolves a tombstoned aspect to null. Non-removal of the marker is a convention held by review,
   not a platform-enforced invariant — no aspect-type DDL can refuse a tombstone. **Consumer: the
   increment that protects the marker, or the weaverTarget increment if it must tolerate the gap.**
4. **The lens counts live links to LIVE neighbours; the ops count live links.** `traverseRel` drops
   an edge whose endpoint does not resolve to a live vertex, so a live link to a tombstoned or absent
   neighbour is residue the op sweeps and the lens cannot see — a gap that closes early. No shipped
   writer produces that state today (both sweeps tombstone vertex and link in one commit; a merge
   leaves the secondary alive), so it holds by accident of corpus shape, not by construction. Same
   class: a `duplicateOf` **self-link** is invisible to the lens (`traverseRel` skips the anchor
   itself) and would be swept, though every write path refuses self-loops today. **Consumer:
   `SealIdentityForErasureComplete`, whose in-commit re-verification walks the links directly and is
   the backstop for both.**
5. **R1's `N > 3·PAGE` dispatch-count proof still has nothing to run against** — inherited unchanged
   from increment 4. **Consumer: the `identityErasureComplete` weaverTarget increment.**

Increments 3 and 4's other residuals (the finite scan window, the missing `Enumerations` declaration
on a `systemOp` step, the read-set coverage guard, the `UnbindIdentityCredentials` description edit,
the absent `make verify-package-privacy-base` target) are re-inherited verbatim rather than re-filed.

---

## Fire B build note — increment 6 (2026-08-07): the completion seal

### Fire brief

**Scope sentence, verbatim from §12 Fire B step 2's list:** `SealIdentityForErasureComplete` with its
in-commit re-verification.

**Why this increment is the op and not the `identityErasureComplete` weaverTarget, which §12 lists
first.** The target cannot ship before this op exists, and not for a tidiness reason. A `missing_<g>`
column that is true while the target's playbook declares no `Gaps` entry for it is neither an install
error nor silently ignored: `dispatchGap` raises a standing **`error`-severity** `GapWithoutPlaybook`
Health issue and Acks (`internal/weaver/evaluator.go:179-201`), which `aggregateStatus` escalates the
whole component to `unhealthy` on (`internal/weaver/health.go:362-386`). Increment 5's
`missing_erasureSeal` is a conjunction — all five residue counts zero **and** both async halves landed
**and** the field-diff (`lenses.go:296-303`), so it opens **the moment the first erasure converges**,
not at every request. Nothing else can ever close it: `.erasure` has no other producer. A target
shipped now would therefore install a permanent red on the Weaver at the first converged erasure —
later than "immediately", and no less permanent.

> **AMENDED 2026-08-28 — the severity half of this rationale no longer holds.**
> `GapWithoutPlaybook` was demoted `error` → `warning` at its raise site by the weaver decline-retry
> design's §8 ([weaver-decline-retry-substrate-native-design.md](weaver-decline-retry-substrate-native-design.md)),
> precisely because that code became *standing* under the Nak loop and a package-authoring typo
> would otherwise pin Weaver `unhealthy` while it dispatched normally for every other target. So an
> orphaned `missing_erasureSeal` column now yields a standing **`warning`** and a **`degraded`**
> component, not a permanent red. The **ordering** conclusion is unchanged and still right — the
> pieces a target dispatches should exist before the target that dispatches them, and a standing
> warning nobody can clear is still a defect to avoid — but this paragraph must not be read as
> "shipping the target early takes the component unhealthy". It no longer does.

Same ordering rationale increments 3 and
4 each gave: the pieces the target dispatches exist before the target that dispatches them. This is the
third and last of them, so the target increment that follows wires all five gaps against real ops.

**Non-goals:** the `identityErasureComplete` weaverTarget, the `identityErasure` Loom pattern, the two
`surface` gaps, narrowing `ShredIdentityKey` (§12 step 3), the operator surface (§12 step 4).

**Touch list (verified live).**

| File | What |
|---|---|
| `packages/privacy-base/seal_identity_for_erasure_complete.go` | NEW — the `.erasure` attestation aspect DDL · the op DDL + script · the `privacy.erasureCompleted` event DDL |
| `packages/privacy-base/ddls.go` | register the three new DDLs |
| `packages/privacy-base/permissions.go` | the `Scope:"any"` → `operator` grant |
| `packages/privacy-base/manifest.yaml` · `package.go` | the declarations · `0.7.0 → 0.8.0` |
| `packages/privacy-base/seal_identity_for_erasure_complete_test.go` | NEW — the guard matrix and the verification proofs |
| `packages/privacy-base/package_test.go` | the structure pins (DDLs 7→10, permissions 3→4) |

**Precedents to mirror.** `privacy-base/seal_identity_for_erasure.go` is the sibling in every respect —
aspect-DDL + op-DDL + event-DDL in one file, the `required_string`/`parts_of`/`vertex_alive`/`live_data`
helper copies Starlark's absent `load()` forces, and the `IdentityMerged` / `ErasureNotShredded`
fail-closed guards this op must repeat verbatim (a merged-away identity's residue is zero by
construction, so an attestation there attests nothing). `privacy-base/purge_identity_dedup_footprint.go`'s
`collect_live_sweep` is the paged `kv.Links` walk over a soft-delete substrate and the source of the
class-(e) read-posture annotations. `identity-domain/unbind_identity_credentials.go` owns the other two
arms.

**Increment order + the green check after each.** (1) the three DDLs + registration + version bump →
`go build ./...`; (2) the grant + structure pins → `go test ./packages/privacy-base/ -count=1`;
(3) the guard matrix and verification tests → same; (4) gates → `go test ./... -p 4`, `make vet`,
`golangci-lint run ./...`, all `scripts/lint-*.go`, `make verify-kernel`.

**In-scope gotchas.**

- **The verification walk must cover exactly the five arms the lens counts and the two sweeps clear** —
  `boundTo` inbound and outbound, `indexes` inbound, `duplicateOf` outbound and inbound. An arm the
  seal does not walk is an arm the attestation does not cover, and the seal is the last thing standing
  between a stale row and a false attestation.
- **The seal must re-verify the two ASYNC halves itself, not inherit them from gap ordering.** The
  lens's `missing_erasureSeal` only opens once `vaultKeyDestroyed` and `projectionsNullified` are both
  true, and `lenses.go:235-244` names that ordering as the guarantee *until an op re-verifies them* —
  and hands this increment the obligation by name. A guarantee that lives in a projection's column
  ordering is a guarantee that dies the first time a gap is dispatched out of order.
- **The live-read budget, not the sweep's ceiling, bounds what this op can verify.** `kv.Links` charges
  its clamped page LIMIT per page against a 60,000-unit budget
  (`internal/processor/live_read_budget.go:26`, `starlark_kv.go:204-224`), so five arms at the sweeps'
  own `64 × 256` would charge 81,920 and abort mid-walk. A **shared** page budget across the five arms
  rather than a per-arm one is what keeps the ordinary lopsided subject verifiable, since almost no
  identity is wide on all five.
- **A tombstone is still enumerated.** The walk pages the whole cursor to prove absence; it cannot stop
  at the first page, because one sweep's worth of tombstones fills it.

**Adjacent finds — filed now, not at ship.** One, for the weaverTarget increment that follows, so it is
not re-derived: **§7.2's `surface` gaps specify severity `critical`, which is not a valid
`IssueSeverity`.** `internal/weaver/registry.go:643-645` accepts `"warning"` or `"error"` only, and a
target carrying anything else is rejected at CDC load with a `TargetRejected` issue — the target would
never register at all. `"error"` is the intended tier. Increments 3, 4 and 5's residuals are
re-inherited verbatim rather than re-filed.

### What increment 6 built

`SealIdentityForErasureComplete` in `privacy-base`, package `0.7.0 → 0.8.0`, with the `.erasure`
attestation aspect DDL, the `privacy.erasureCompleted` event DDL, and the `Scope:"any"` → `operator`
grant the Weaver service actor reaches it through. The op walks five arms in its own atomic commit and
writes the attestation only if every one is clear.

| Arm | Cleared by | Contributes to |
|---|---|---|
| `boundTo` inbound · outbound | `UnbindIdentityCredentials` | `coverage.credentials` |
| `indexes` inbound | `PurgeIdentityDedupFootprint` | `coverage.indexes` |
| `duplicateOf` outbound · inbound | `PurgeIdentityDedupFootprint` | `coverage.duplicates` |

`coverage` counts the **tombstoned** links the walk passed on its way to proving no live one remains —
what was erased, per class. A residue count in an attestation is always zero and proves nothing.

### Five corrections the build made to the ratified text

1. **§7.2's `surface` gaps specify severity `critical`, which is not a valid `IssueSeverity`.**
   `registry.go:643-645` accepts `"warning"` or `"error"` only, and a target carrying anything else is
   **rejected at CDC load** with a `TargetRejected` issue — it would never register at all. The
   weaverTarget increment must use `"error"`.
2. **The two async halves are re-verified in the op, not inherited from the lens's gap ordering.** §7.1's
   spec opens `missing_erasureSeal` only after `vaultKeyDestroyed` and `projectionsNullified` are both
   true, and the lens's own comment named that ordering as the guarantee *until an op re-verifies them*.
   It is discharged here — because a `directOp` gap fires from a reconcile sweep as readily as from a
   fresh row, so a guarantee that lives in a projection's column ordering is not one.
3. **The merged-away gate keys on `.state`, read LIVE, not on `.mergedInto` read from `state[...]`.**
   §7.2 said nothing about the gate's mechanism and the sibling's shape was the obvious thing to copy;
   it is the wrong one for a fail-closed gate. A `state[...]` lookup of a key **no dispatcher declared**
   reads as ABSENT, so one missing declaration silently opens the gate — and this op's dispatcher does
   not exist yet, so nothing but a test literal pins the declaration today. `.state` is written for
   every identity; `.mergedInto` only for a merged one, so only `.state` can carry the signal.
4. **The prior attestation is read TOMBSTONE-BLIND.** No aspect-type DDL can refuse a tombstone (a
   tombstone carries no document, so step 6 never resolves the class), so any package script can remove
   this attestation. Recovery is the good part — the lens reopens the gap and this op rewrites it — but
   a live-only read would make that recovery silently restamp `sealedAt`, the one field here with legal
   meaning, to now.
5. **`privacy.erasureCompleted` is emitted once per erasure CYCLE, not per commit.** The op is
   idempotent and the Weaver re-dispatches a gap until it observes it close, so an unconditional
   emission announces the same completion every pass. A re-verification of an already-attested cycle
   rewrites coverage and stays quiet.

### The budget the design was measuring is not the budget that binds

§7.2 and §11 R1 reason about enumeration size. The op's live-read arithmetic is comfortable —
`160 × (1 + 256) = 41,120` of the 60,000-unit budget, which is why the page budget is **shared** across
the five arms rather than allotted per-arm (virtually no identity is wide on all five, so a shared
budget verifies the lopsided subject a per-arm split would refuse).

**The 250ms Starlark wall binds one to two orders of magnitude sooner.** `connLinkLister` issues one
`KVGet` per listed key and `KVListKeysFilter` re-enumerates each arm's whole matching key set on every
page call, so a walk over N links costs N sequential round trips plus a quadratic term in key names.
Proving *absence* means paging every arm to its cursor end, which makes this op strictly more expensive
than either sweep — both stop as soon as `SWEEP_LIMIT` live links are in hand — and it runs at the
moment a subject's tombstone count is maximal.

This is not theoretical. During this fire's full-suite run,
`TestUnbindIdentityCredentials_WideSubject_ConvergesPastOnePage` — the **existing** sweep, at 300 links —
failed with `ScriptTimeout: script exceeded wall budget 250ms` under parallel load, and passed in
isolation. The sibling already lives at the edge of this wall; the seal pages strictly further.

Consequence, stated because it is not what the design assumed: past the practical ceiling the op dies
as `ScriptTimeout`, not the named `ErasureVerificationUnreachable`. **Both are refusals and both are
fail-closed** — no attestation is written either way, which is the property that matters — but the loud,
erasure-specific stop is not the stop that fires, and CI cannot see it (`PROCESSOR_SCRIPT_WALL_MS=5000`
in both the Makefile and `ci.yml`, a 20× widening).

### The proofs

Fourteen tests. Five are mutation-verified against the specific wrong build they exist to catch: a
single-page walk, a dropped `boundTo` outbound arm, a live-only read of a tombstoned attestation, a
merged gate without the `.state` clause, and an unconditional completion event. Each reds only when its
own guard is removed.

`…_FindsALiveLinkPastAPageOfTombstones` is the one that separates a real verification from a plausible
one. A converged subject's enumeration is *all tombstones*, so a build that read one page and stopped
would attest every erasure it was asked about. The fixture is 300 tombstoned links — past the 256-key
read page — with the single live one keyed to sort last; mutation-verified, the single-page build
attests it while every other test in the file stays green.

`…_RefusesALiveCredentialInEitherDirection` is §13's named headline. `…_RefusesAnOutstandingAsyncHalf`
and `…_RequiresAShreddedEnvelope` sweep their fixtures clean first, so the fact under test is the only
refusable one — otherwise both would pass on a live `indexes` link and stay green against a build with
no async check at all.

### Residuals — named, with their consumers

1. **The verification walk's real ceiling is wall time, and past it the refusal is `ScriptTimeout`
   rather than the named stop.** Fail-closed, so no false attestation — but a wide subject is swept
   clean and never attested, with a diagnostic that names nothing about erasure. **Consumers: the
   `identityErasureComplete` weaverTarget increment**, which must not re-dispatch a `ScriptTimeout`
   forever (it inherits the same obligation increment 5's residual 1 named for the sweeps' hard stop),
   **and the shelved hard-delete mutation verb**, which dissolves the whole class — a tombstone that
   leaves the keyspace makes a converged subject's walk proportional to its LIVE links, which is zero.
   Filed as a board row.
2. **The verify ceiling is smaller than the two sweeps' combined ceiling.** Each sweep allots 64 pages
   × 256 *per relation-direction*; the seal allots 160 pages *shared across five arms*. So a subject
   inside every sweep's own per-arm ceiling can still exceed the seal's aggregate one. Same consumers,
   same durable fix; travels with residual 1 on one row.
3. **A `credentialindex` with no `boundTo` link is residue nothing walks.** The vertex carries its
   identity in its body and has no link to it, so no enumeration reaches one;
   `UnbindIdentityCredentials` tombstones each alongside its `boundTo`, which covers every one that
   *has* a link. §9.2(i) already records the class as named-not-designed. **Consumer: the attestation's
   own coverage claim** — filed as a board row.
4. **The walk contends no shared OCC key** (Contract #2 §2.5.1). What keeps a link from being created
   behind it is §6's write-path gates, not serialization: every creator of the three classes reads the
   marker and refuses a marked identity — verified across identity-domain and identity-hygiene during
   this fire's review. That is a property of the current corpus held by review, and it is now stated in
   the script rather than assumed. **Consumer: the next fire adding a writer of `boundTo`, `indexes` or
   `duplicateOf`.**
5. **`ErasureVerificationUnreachable` has no test.** Reaching it needs ~41,000 seeded links, which no
   package test can afford — and per residual 1 the path is unreachable in production anyway. It is the
   error text, not the mechanism, that is unproven. **Consumer: whichever fire lands the hard-delete
   verb**, after which the ceiling is reachable at a testable size.

Increments 3, 4 and 5's other residuals (the finite scan window, the missing `Enumerations` declaration
on a `systemOp` step, the read-set coverage guard, the absent `make verify-package-privacy-base`
target, the tombstoned-marker gap) are re-inherited verbatim rather than re-filed.

---

## Fire B build note — increment 7 (2026-08-07): the weaverTarget

> **Two of this brief's decisions were FALSIFIED by the build's own review — do not build from them.**
> **(a) The three `maxretries_<g>` columns do not ship, at any size.** Decision 2 below sizes them
> against a per-commit re-dispatch cadence the engine does not have; the measured cadence is one
> attempt per 30-minute mark lease, which makes a ceiling-sized cap inert and a reachable one
> terminally parking. **(b) The two `surface` gaps ship at `warning`, not `error`.** Decision 1 is
> right that §7.2's `critical` is invalid and would sink the whole target, and wrong that `error` is
> therefore the answer. Both are worked through in "Three corrections the build made to the ratified
> text" below, which supersedes this brief wherever they differ. Decisions 3 and 4 shipped as written.

### Fire brief

**Scope sentence, verbatim from §12 Fire B step 2's list:** the `identityErasureComplete` weaverTarget,
and the two `surface` gaps.

This is the increment increments 3–6 each deferred to. Every piece the target dispatches now exists:
`UnbindIdentityCredentials` (inc 3), `PurgeIdentityDedupFootprint` (inc 4), the
`identityErasureResidue` lens whose rows it reads (inc 5), and `SealIdentityForErasureComplete`
(inc 6). The target is what turns five projected `missing_*` columns into a convergence loop.

**Non-goals:** the `identityErasure` Loom pattern, narrowing `ShredIdentityKey` (§12 step 3), the
operator surface (§12 step 4), R1's `N > 3·PAGE` dispatch-count proof against a live stack.

**Touch list (verified live).**

| File | What |
|---|---|
| `packages/privacy-base/targets.go` | NEW — `WeaverTargets()`: `identityErasureComplete`, five gaps |
| `packages/privacy-base/lenses.go` | the three `maxretries_<g>` columns the cap term reads · `BodyColumns` |
| `packages/privacy-base/package.go` | `WeaverTargets:` field · `0.8.0 → 0.9.0` |
| `packages/privacy-base/manifest.yaml` | `declares.weaverTargets` · version |
| `packages/privacy-base/targets_test.go` | NEW — playbook↔lens column binding, the cap arithmetic, the severity pin |
| `packages/privacy-base/erasure_residue_lens_test.go` | the `maxretries_*` projections |
| `packages/privacy-base/package_test.go` | the structure pin: WeaverTargets 0 → 1 |

**Precedents to mirror.** `packages/orchestration-base/targets.go:26-49` is the only shipped package
carrying both a `surface` gap (`unroutedTasks`) and a `directOp` gap (`orphanedTaskGrants`);
`packages/lease-signing/targets.go:69-103` is the richest playbook (two `directOp` gaps with `Params`
and `Reads`). `packages/lease-signing/lens_unit_test.go:18-97` is the playbook↔lens column
cross-check — **it must not be copied verbatim**: it looks the lens up by
`l.CanonicalName == targetID`, and this package is the first where the two deliberately differ
(`identityErasureResidue` vs `identityErasureComplete`, `lenses.go:21-28`). Look it up by `LensRef`.

**Increment order + the green check after each.** (1) `targets.go` + registration + version bump →
`go build ./...`; (2) the `maxretries_*` lens columns → `go test ./packages/privacy-base/ -count=1`;
(3) the binding + cap tests + structure pins → same; (4) gates → `go test ./... -p 4`, `make vet`,
`golangci-lint run ./...`, all `scripts/lint-*.go`, `make verify-kernel`.

### Four things the ratified §7.2 did not settle, and how this brief settles them

**1. `IssueSeverity: "critical"` would reject the whole target — and `critical` is not the only thing
wrong with §7.2's issue codes.** Increment 6 already filed the severity half as an adjacent find:
`internal/weaver/registry.go:643-646` and `internal/pkgmgr/orchestrationguard.go:260-267` accept
`"warning"` or `"error"` and nothing else, and a target carrying anything else is **rejected at CDC
load** (`TargetRejected`) — it would never register, so all five gaps would be dead, not just the two
`surface` ones. The tier is `"error"`.

The `issueCode` half is new here. §7.2 specifies `erasure.vaultKeyNotDestroyed` /
`erasure.projectionsNotNullified`. **Contract #5 §5.5 documents the `code` field as PascalCase**
(`docs/contracts/05-health-kv.md:109-124`), and every code raised anywhere — the twelve engine-side
codes and the single shipped package one (`UnroutedTasks`) — follows it. Nothing validates the shape,
so a dotted code would install green and simply be the only non-conforming code in the system. The
codes are `ErasureVaultKeyNotDestroyed` and `ErasureProjectionsNotNullified`.

**2. The retry cap is this increment's obligation, and a constant is the only shape that can work.**
Increment 5's residual 1 and increment 6's residual 1 both name this increment as their consumer: a
sweep that hard-fails (`ErasureResidueUnreachable`), or a seal that dies on the 250ms wall, re-dispatches
forever with no escalation, because the lens's constant-`false` `inflight_<g>` columns opt those gaps out
of `defaultDirectOpRetryBudget` (`evaluator.go:886-920` — the fallback applies only to a `directOp` gap
that declares **no** `inflight_<g>`). The mechanism available is a `maxretries_<g>` row column.

A **residue-derived** cap inverts and must be refused: the dispatch count rises while a converging
subject's residue falls, so a cap scaled to current residue suppresses the sweep exactly as it
approaches zero. The cap has to be a constant, and the only defensible constant is **the sweep's own
reachable ceiling** — a bound a converging subject cannot reach, so the cap can only fire on a subject
the sweep has already stopped making progress on:

| Gap | Arms | Per-commit | Per-arm ceiling | Cap |
|---|---|---|---|---|
| `missing_credentialResidue` | `boundTo` in · out | `SWEEP_LIMIT` 64 | `64 × 256` = 16,384 | **512** |
| `missing_dedupResidue` | `indexes` in · `duplicateOf` out · in | 64 | 16,384 | **768** |
| `missing_erasureSeal` | — idempotent, one commit | — | — | **8** |

The two sweeps' ceilings are `MAX_BOUND_TO_PAGES × BOUND_TO_PAGE_LIMIT` and
`MAX_LINK_PAGES × LINK_PAGE_LIMIT` (`unbind_identity_credentials.go:131-134`,
`purge_identity_dedup_footprint.go:146-149`), and each takes **one direction or class per commit**, so
a subject that is reachable at all needs at most `ceil(16,384 / 64)` = 256 dispatches per arm. The seal
is different in kind: it is size-independent and idempotent, and its only legitimate re-dispatch cause
is lens-vs-commit lag, which a reprojection resolves — so its cap is small, and reaching it means the
seal is refusing something the lens cannot see (increment 5's residual 4).

Exhaustion is not silent: `escalateExhaustedGap` (`evaluator.go:922-1005`) raises a standing
`GapBudgetExhausted` Health issue. The `surface` gaps need no cap — a `surface` action is level-driven
and idempotent, and the fallback budget applies to `directOp` only.

**3. `Class` is omitted on all three `directOp` gaps, deliberately.** `plan.class` →
`opEnvelope.Class` exists only to disambiguate the Processor's `operationType → class` reverse index
(`internal/processor/ddl_cache.go:395-430`), which drops an operationType admitted by **two or more
vertexType DDLs** rather than guess. Corpus-wide, each of the three is admitted by exactly one
vertexType DDL — including `SealIdentityForErasureComplete`, whose second hit is the `erasure`
**aspectType** DDL that `commandIndexEligible` excludes. Pinning `Class` where it is not needed would
be a second place to keep in sync. The day a second vertexType DDL admits one of these, the dispatch
fails closed and loudly (`MissingClass`), so a test pins the non-ambiguity rather than the workaround.

**4. `UnbindIdentityCredentials` lives in `identity-domain`, and that is fine.** `Class` has nothing
to do with which package owns the DDL, and the Weaver submits under one fixed service actor
(`engine.go:40-42`) that the `Scope:"any"` → `operator` grants already reach. The cross-package
dispatch is a naming question only.

**In-scope gotchas.**

- **The gap column names in the playbook must match the lens's projected columns exactly** — a
  `missing_<g>` column true with no `Gaps` entry raises a standing **`error`**-severity
  `GapWithoutPlaybook` and takes the whole Weaver `degraded` (it escalated to `unhealthy` until the
  decline-retry design's §8 demoted the code to `warning`, 2026-08-28)
  (`evaluator.go:179-201`, `health.go:362-386`). This is the failure increment 6 declined to ship into.
- **Integer literals project, and land as the Weaver expects.** `256 AS x` parses to `Literal{int64}`
  (`ruleengine/full/visitor.go:662-672`), serializes as a bare JSON number, and `intColumn`
  (`evaluator.go:805-832`) accepts `float64`/`int`/`int64` — a string would be a `RowDataError`.
- **The dispatch count resets on gap close** (`clearClosedMarks`, `evaluator.go:645-719`), so a second
  erasure cycle starts on a fresh budget rather than a spent one. The cap is per open episode.
- **`LensRef` binds nothing at install** — the Weaver resolves a target by the `weaver-targets` row-key
  **prefix**, so `TargetID` must equal the lens's `OutputKeyPattern` prefix. Increment 5 already shaped
  the lens for this; a test pins it so neither side can drift alone.

**Adjacent finds — filed now, not at ship.** None new. Increments 3–6's residuals are re-inherited
verbatim rather than re-filed; increment 5's residual 1 and increment 6's residual 1 are **discharged
here** rather than inherited.

### What increment 7 built

`identityErasureComplete` in `privacy-base` (`targets.go`), package `0.8.0 → 0.9.0`. One
`WeaverTargetSpec` over the `identityErasureResidue` rows increment 5 has been projecting, binding all
five `missing_*` columns to an action. `LensRef` names the lens; the binding the Weaver actually
resolves through is the row-key prefix, which is why `TargetID` and the lens's canonical name are
allowed to differ.

| Gap | Action | What it dispatches |
|---|---|---|
| `missing_credentialResidue` | `directOp` | `UnbindIdentityCredentials{subjectKey}` |
| `missing_dedupResidue` | `directOp` | `PurgeIdentityDedupFootprint{subjectKey}` |
| `missing_vaultDestruction` | `surface` | `ErasureVaultKeyNotDestroyed`, `warning` |
| `missing_projectionNullify` | `surface` | `ErasureProjectionsNotNullified`, `warning` |
| `missing_erasureSeal` | `directOp` | `SealIdentityForErasureComplete{subjectKey}` |

No gap sets `Class`: each of the three operationTypes is admitted by exactly one **vertexType** DDL
corpus-wide, and `commandIndexEligible` excludes the `erasure` aspectType DDL that also lists
`SealIdentityForErasureComplete`. A test pins the non-ambiguity rather than pinning a workaround,
because the day it stops holding the dispatch fails closed and loudly (`MissingClass`).

### Three corrections the build made to the ratified text

1. **§7.2's `critical` severity would have sunk the whole target, and `error` — the obvious
   repair — is also wrong.** `registry.go:643-646` and `orchestrationguard.go:260-267` accept
   `warning` or `error` and nothing else; a target carrying anything else is **rejected at CDC load**,
   so all five gaps would have been dead, not just the two `surface` ones. That much increment 6
   already filed. What the build found is that `error` fails for a different reason: the residue row
   exists the moment `.erasureRequested` does — `SealIdentityForErasure` requires only that the shred
   committed — and both async halves are driven off the `privacy.keyShredded` that same commit emits,
   so **both gaps are open at the instant the row first projects, on every erasure.** `aggregateStatus`
   escalates the component to `unhealthy` on any `error` issue, so the ordinary path would hold the
   whole Weaver unhealthy for the whole normal in-flight window. Contract #5 §5.2 reserves that tier
   for "cannot fulfil the responsibility"; a pending async half in one subject's erasure is not that.
   `warning` matches the only other `surface` gap in the corpus. **§7.2's table is corrected below.**
2. **§7.2's dotted issue codes are the only non-PascalCase codes in the system.** Contract #5 §5.5
   documents `code` as PascalCase and all thirteen shipped codes follow it. Nothing validates the
   shape, which is precisely why it had to be decided rather than discovered — a dotted code installs
   green and is simply wrong forever. `ErasureVaultKeyNotDestroyed` / `ErasureProjectionsNotNullified`.
3. **No retry cap ships, and the reason is the next section.**

### The re-dispatch rate §7.2's termination proof does not price

§7.2 proves termination in `ceil(N/PAGE)` passes. That is true, and it is a statement about **passes**,
not about time. The build set out to size a `maxretries_<g>` cap — the mechanism increments 5 and 6
both named this increment as the consumer of — and sizing it required knowing what a pass costs.

A pass costs **thirty minutes**. The chain, verified rather than inferred:

- Dispatch 1 CAS-creates the gap's mark with a `defaultMarkLease` of 30 minutes
  (`reconciler.go:17`; `cmd/weaver/main.go` sets no override).
- The sweep commits, the lens reprojects, and that arrives as a **fresh** CDC delivery.
- `dispatchGap` computes `stale := found && !leaseLive(...) && staleMark(...)`
  (`evaluator.go:300`). The mark's lease is live, so `stale` is false — regardless of the
  constant-`false` `inflight_<g>` column, which only makes `staleMark`'s own term true.
- `fireEpisode` therefore takes the `found && !stale && !redelivered` branch: **the anti-storm drop**
  (`evaluator.go:465-470`).
- `releaseCompletedLeg` does not rescue it — it returns immediately for any gap with no `Goal`
  (`evaluator.go:743`), which is all three of these.

So the only surviving dispatch leg is the reconciler's lease-expiry reclaim, and the realized rate is
**64 links per 30 minutes**. `evaluator.go:295-299` states the `!leaseLive` term rules out "an
in-memory CDC round trip, milliseconds, not the mark's lease" — true for every gap shipped before this
one, because each closes on a single dispatch. **This target is the first to use re-dispatch as a
*progress* lane rather than a *failure-retry* lane, and the engine has no seam for that.** §7.2 called
the Weaver-driven paged sweep first-of-kind risk #40 and declined to claim precedent; this is that
risk, measured.

What it does to the cap: a bound sized to the sweeps' own reachable ceiling (256 dispatches per arm) is
a 21-to-32-day chain — inert, never reached. A bound small enough to fire, as the seal's would be, is
worse than none: `escalateExhaustedGap` alerts and **returns without touching the mark**
(`evaluator.go:949-957`), the mark TTL-expires, the reconciler enumerates marks rather than rows, and
`gapSuppressed` keeps suppressing on a dispatch-count key that only a gap-close resets. A
stuck-but-recoverable seal becomes permanently parked. Uncapped, it stays noisy and self-healing.

The residual's other named option — route the loud stop to a `surface` gap — is unavailable for a
reason that outlives the pacing: a `surface` gap fires on a `missing_<g>` column, and a hard-failing
sweep commits nothing, so it leaves every residue count exactly where it was. "Stuck" is "this count
stopped decreasing", which is a claim about history, and the row carries none.

**The obligation therefore stays open with corrected coordinates, and the increment ships without it.**
That is the honest reading of a residual whose named mechanism did not survive contact: keep the
coordinates, kill the framing. What the target still does is the thing that matters — a well-connected
person who cannot be erased **at all** today can be erased slowly.

### The proofs

Four tests. `…_EveryLensGapHasPlaybookEntry` is the one that earns its place and is
mutation-verified: a `missing_*` column the lens projects with no `Gaps` entry raises a standing
**`warning`**-severity `GapWithoutPlaybook` and takes the whole Weaver component `degraded`
(demoted from `error`/`unhealthy` by the decline-retry design's §8, 2026-08-28)
(`evaluator.go:179-201`, `health.go:362-386`) — the failure increment 6 explicitly declined to ship
into, now pinned from the other direction. `…_PlaybookColumnsMatchLens` walks the forward direction and
resolves the lens by `LensRef`, not by name-equals-`TargetID`; the two shipped copies of this check
elsewhere in the corpus assume those are the same string, and this package is the first where they
deliberately are not. `…_DispatchedOpsAreUnambiguous` pins the omitted `Class`.
`…_SurfaceGapsCarryWarningSeverityAndPascalCaseIssueCode` pins correction 1.

The three-layer adversarial pass (two independent deep layers plus an acceptance audit) is what
produced the pacing finding and the severity correction; both were reached independently by both deep
layers and then verified against the engine before being acted on.

### Residuals — named, with their consumers

1. **The convergence loop advances one sweep page per 30-minute mark lease.** An ordinary person
   converges in a few passes; a wide subject takes days. The engine has no seam for a gap whose
   re-dispatch *is* the progress — the anti-storm drop cannot distinguish "the episode is still in
   flight" from "the episode landed and the gap is still open because there is more to do".
   **Consumers: this target, and any future paged convergence loop.** The shape is an engine change
   (an episode whose effect has demonstrably landed should release its lease rather than wait it out),
   so it is Designer-lane, not a package fix. Filed as a board row.
2. **A stuck sweep or seal re-dispatches indefinitely with no escalation** — increment 5's residual 1
   and increment 6's residual 1, **still open**, with `maxretries_<g>` ruled out above. Expressing
   "stuck" needs either a per-entity attempt record or an elapsed-time predicate against the shred
   stamp; the row carries neither. **Consumer: the operator surface (§12 step 4)**, which reads the
   residue row directly and can show a count that has stopped moving without needing the loop to
   announce it. Filed as a board row.
3. **A `surface` gap's Health issue is keyed per `(target, column)` with no entity segment**
   (`issueKeyGap`, `evaluator.go:1068`). With two erasures in flight, whichever subject's async halves
   land first clears the issue raised for the one that is genuinely stuck. Benign for the aggregate
   `unroutedTasks` gap this key shape was written for; wrong for a per-subject gap. **Consumer: these
   two `surface` gaps, and every future per-entity `surface` gap.** Engine-level, pre-existing —
   filed as a board row.
4. **A Weaver `directOp` cannot declare a class-(e) bounded enumeration.** `GapActionSpec` has no
   `Enumerations` field and the Weaver's `contextHint` envelope carries only `Reads`/`OptionalReads`,
   so the five `kv.Links` walks these ops run are undeclarable by their dispatcher. The same gap the
   already-filed `systemOp` row names, on a second dispatcher. Metadata only — the walks execute
   correctly. **Consumer: the read-posture debt sweep's warn→block flip.** Folded into the existing
   row rather than filed twice.
5. **The gap can close early on residue the lens cannot see, and the seal is not the backstop it was
   named as.** Increment 5's residual 4 (a live link to a tombstoned neighbour; a `duplicateOf`
   self-link) closes the sweep gap, whereupon the seal opens, refuses `ErasureIncomplete` on its own
   in-commit walk, and — with the sweep gap now closed and only the lens able to reopen it — nothing
   ever dispatches the op that could clear the link. The seal correctly prevents a **false
   attestation**; it does not prevent a **stall**. Residual 4's consumer is corrected from
   "`SealIdentityForErasureComplete`" to the row filed for residual 2 above.

Increments 3–6's other residuals are re-inherited verbatim.

---

## Fire B build note — increment 8 (2026-08-07): the Loom pattern

### Fire brief

**Scope sentence (verbatim from §12 Fire B, internal build order step 2).** *"the `identityErasure`
Loom pattern"* — the last unbuilt artifact of step 2. Everything else step 2 named
(`PurgeIdentityDedupFootprint`, the `identityErasureResidue` lens + `privacy-erasure` bucket, the
`identityErasureComplete` weaverTarget, the two `surface` gaps, `SealIdentityForErasureComplete`)
shipped in increments 1–7.

**Scope-diff gate.** Narrow-only against §5.1 + §5.4, with three widenings the ratified text itself
mandates rather than the build inventing: the `CompletionDomains` correction Fire A's §5.3 probe
already recorded, the step-4 emission that same probe named as Fire B's to own, and the declared
read-sets Fire A shipped the mechanism for. No adjacent mechanism substituted. Step 3 of the build
order (narrowing `ShredIdentityKey`) is explicitly NOT in this increment.

**Verified touch-list** (`file:line` checked live):

- `packages/privacy-base/patterns.go` — NEW. Mirrors `packages/lease-signing/patterns.go:1-90` and
  `packages/capability-author/patterns.go:1-41`, the only two shipped `LoomPatterns()` declarations.
- `packages/privacy-base/package.go:34-43` — add `LoomPatterns: LoomPatterns()` (precedent
  `packages/lease-signing/package.go:110`).
- `packages/privacy-base/manifest.yaml` — add the `loomPatterns:` block (shape:
  `packages/lease-signing/manifest.yaml:153-161`); version bump, required by
  `scripts/lint-package-version.go:127-135`.
- `packages/privacy-base/purge_identity_dedup_footprint.go:309-327` — the emission (D3 below), plus
  the DDL description at `:64-120` and the file header at `:44`.
- `packages/privacy-base/ddls.go` — register the new event type; mirror
  `seal_identity_for_erasure.go:400-441`'s `ErasureRequestedEventDDL`.
- `packages/privacy-base/permissions.go:51-57` — correct the "deliberately NO grant" paragraph
  (D2 below).
- `packages/privacy-base/package_test.go:50-57` — structure pins (+1 event DDL, +1 loomPattern).
- `packages/privacy-base/patterns_test.go` — NEW drift-guard.

**Precedents to mirror.** `validateLoomPatterns` (`internal/pkgmgr/orchestrationguard.go:275-359`)
and the engine's `validate()` (`internal/loom/pattern.go`) run in lockstep — a spec that passes one
passes the other, which is what keeps an install from admitting a pattern the CDC load rejects.
Neither constrains a step's `Operation` to an op the same package declares, so step 3's
`UnbindIdentityCredentials` (identity-domain) is a legal cross-package reference.

**Increment order + runnable green checks.**

1. The event on step 4's op (`go test ./packages/privacy-base/`).
2. `patterns.go` + wiring + manifest (`go test ./packages/privacy-base/ ./internal/pkgmgr/`).
3. Full gates + a live install on the running stack.

### Four things §5.1 did not settle, and how this brief settles them

**D1 — `CompletionDomains` is `["privacy", "identity"]`, not §5.1's `["privacy"]`.** Not a new
finding: Fire A's §5.3 probe traced the correlation end to end and recorded that step 3
(`UnbindIdentityCredentials`) emits `identity.unbound`, so the pattern completes on two domains per
`docs/contracts/10-orchestration-loom.md:52-54`. This increment is where that correction lands in
code. The cost is that Loom's completion consumer is now reconciled for the whole `identity` domain;
an event carrying no live token is a lookup miss and an Ack (`engine.go:696-710`).

**D2 — step 1's grant already ships, from a different package, and that is the posture.**
`privacy-base/permissions.go:51-57` deliberately ships NO `ShredIdentityKey` grant, on the argument
that right-to-erasure is a deployment decision. That paragraph predates the pattern and reads today
as though step 1 were unauthorized. It is not: `packages/privacy-operator-grant/permissions.go:15-26`
grants `ShredIdentityKey` to `operator` at `scope:any`, and `identity.system.loom` reaches `operator`
via `holdsRole` — the same mechanical route every other step op's grant takes. The Makefile installs
that package (`Makefile:1084-1085`). So no grant is added here and the posture is preserved; what
changes is that privacy-base's comment now names the dependency, because a deployment that omits
`privacy-operator-grant` gets a pattern whose step 1 fails authorization while steps 2–4 are granted.
Filed as a residual rather than papered over.

**D3 — step 4 gets an event, per the §5.3 probe's recommendation.**
`PurgeIdentityDedupFootprint` emits nothing (`purge_identity_dedup_footprint.go:323-327`), and it is
the pattern's LAST step, so on every happy path the instance would ride its 60s `StepTimeout`
(`engine.go:148-149`) into the deadline probe, which advances correctly but logs
`"loom: completion recovered via deadline probe; check completionDomains"`
(`engine.go:1275-1278`) — a permanent misdiagnosis on a correctly-declared pattern. The op gains
`privacy.dedupFootprintSwept{identityKey, relation, purged, mutations}`, emitted **unconditionally**,
including on a pass that finds nothing: a step whose event is conditional on having found work cannot
advance the instance for a subject whose dedup footprint was always empty. The event is not a generic
lifecycle ping — it is the dedup plane's audit counterpart to the credential plane's
`identity.unbound`, recording per-pass what an erasure removed, which is what §7.4's attestation
reader wants and the only such record the dedup plane has (there is no read-model copy to retract, so
nothing else would ever emit one).

**D4 — the declared read-sets, derived per script rather than per DDL prose.** Fire A shipped the
mechanism; this is its first consumer. `Reads` carries the bare `subject` token for all four steps —
every one of them runs `vertex_alive(state, subjectKey)`, whose absence is a correctness error.
`OptionalReads` is derived from what each script reads that is absence-tolerant:

| Step | Op | `OptionalReads` | Why |
|---|---|---|---|
| 1 | `ShredIdentityKey` | `subject.piiKey` | `kv.Read` at `:288`, class (d) |
| 2 | `SealIdentityForErasure` | `subject.mergedInto`, `subject.piiKey`, `subject.erasureRequested` | `:356` reads `.mergedInto` **from `state`**, `:367`/`:382` `kv.Read` the other two |
| 3 | `UnbindIdentityCredentials` | `subject.erasureRequested` | `kv.Read` at `:359`, class (d) |
| 4 | `PurgeIdentityDedupFootprint` | `subject.erasureRequested` | `kv.Read` at `:306`, class (d) |

Step 2's `subject.mergedInto` is the one that is load-bearing rather than hygienic:
`read_aspect_value(state, ...)` returns `None` for an undeclared key exactly as it does for an absent
one, so an undeclared `.mergedInto` silently disarms the `IdentityMerged` refusal and lets the seal
anchor an erasure on a merged-away identity whose residue is zero by construction. Declaring it is
what makes that guard real for the Loom dispatcher. The class-(e) `kv.Links` walks in steps 1, 3 and
4 stay undeclarable — the already-filed `[Loom/Weaver] A dispatcher cannot declare its op's class-(e)
enumerations` row, unchanged by this increment.

### Non-goals

Narrowing `ShredIdentityKey` (build-order step 3), the operator surface and the `lint-conventions`
rule (step 4), and anything that would give the Weaver's `GapActionSpec` an `OptionalReads` field.

### What increment 8 built

`packages/privacy-base/patterns.go` — the `identityErasure` pattern: four `systemOp` steps, two
guarded, all four declaring their own read-sets; wired into the Definition, the manifest, and the
structure pins. `PurgeIdentityDedupFootprint` gained `privacy.dedupFootprintSwept` (registered
event-type DDL) and `UnbindIdentityCredentials` gained `identity.credentialsSwept`, both emitted
unconditionally. `ShredIdentityKey` now accepts `subjectKey` as an equivalent name for its subject.

### What the adversarial pass changed (3-layer: blind hunter · edge-case hunter · acceptance audit)

Erasure plane, `scope:any` verbs, a legal obligation — full depth. **Four real defects, two of them
found independently by both deep layers, all fixed before the merge rather than filed.** The pattern
as first written was dead on arrival; the brief did not catch that, and the brief is where it should
have been caught.

**D1 — every instance would have died at cursor 0, and the design had already said so.**
`ShredIdentityKey` names its subject `identityKey`; `submitSystemOp` builds
`{"subjectKey": inst.SubjectKey}` and a pattern cannot reshape it — the payload field is the engine's,
not the step's. So step 1 submits a payload the op rejects `InvalidArgument: identityKey: required`,
on every instance, and the failure is not contained: no shred, no seal, therefore **no
`erasureRequested` marker** — which is the residue lens's anchor predicate, so the Weaver's convergent
tail never receives a row either. Both the spine and the guarantee die together. Nothing would have
caught it: install validation checks step *structure*, never payload compatibility; the Processor does
not enforce `InputSchema`; and any integration test that pre-shreds its fixture skips step 1 on its
guard and never sends the payload at all. Increment 1's build note recorded this exact obligation and
handed it to this increment (*"It is the pattern increment's to resolve"*) — the Phase-0 brief read
§5 and the increment-7 note and missed a hand-off written three notes earlier. Fixed on the shred's
side (accept either name, refuse a disagreeing pair rather than pick a precedence — resolving a
disagreement means choosing which of two named people to irreversibly destroy a key for).

**D2 — the step-1 guard destroyed a key for the one subject class the design refuses outright.** A
merged-away identity keeps a live vertex; its credentials and indexes already moved to the survivor,
so `SealIdentityForErasure` refuses it `IdentityMerged`. But the seal is step 2. A step 1 guarded only
on `piiKey.data.shredded` runs first and shreds — irreversibly, via the async Vault destruction — for
a subject whose erasure the design says must not proceed at all. The guard gained an
`absent(subject.mergedInto.data.value)` conjunct, so the instance now fails at the seal with nothing
burned.

**D3 — the same guard wedged an identity forever, and the error message named the remedy it forbade.**
An envelope shredded before the finalization-cycle change carries `shredded=true` and **no**
`shreddedAt`. Guarded on `shredded` alone, step 1 skips it permanently — and step 2 then refuses
`ErasureNotShredded: … re-run ShredIdentityKey to restamp it`, which is precisely what the guard
prevents. A re-shred is idempotent, so the disjunct `absent(subject.piiKey.data.shreddedAt)` costs
nothing and is what unwedges the erasure.

**D4 — fixing step 4's emission alone bought nothing, because step 3 stalls first.** D3 of the brief
gave `PurgeIdentityDedupFootprint` an event so the pattern's last step would not ride a 60s deadline.
`UnbindIdentityCredentials` has the identical shape one step earlier: it emits `identity.unbound` **per
credential**, so a pass that unbinds nothing emits nothing — and on the ordinary path it unbinds
nothing, because the un-narrowed `ShredIdentityKey` already tombstoned the `boundTo` links in its own
commit. Step 3 is guardless, so it runs for every subject. The stall was not removed by the brief's
fix, only moved. `identity.credentialsSwept` is the pass-level record the step advances on, emitted
alongside the per-credential retractions rather than instead of them: the two answer different
questions, and only the pass-level one can answer "did this step run".

**Two claims corrected rather than defended.** The pattern doc asserted the §5.5 property — *"a dead
spine degrades an erasure from prompt to eventual, never from complete to incomplete"* — as though it
held throughout. It holds only from step 2 onward: the tail is anchored on the marker step 2 writes,
so a death at step 1 or 2 leaves no row and no dispatch. And there is a live way to reach that today —
step 1's op still carries its own unbounded in-commit cascade and refuses `ShredBatchTooLarge` above
999 mutations, so a well-connected person's erasure fails at cursor 0, which is the subject class the
paged sweeps exist to serve. Retiring that is build-order step 3 (narrowing the op), and until it
lands the failed instance is the only signal. Separately, a **completed** instance means the spine
ran, not that the person is erased: steps 3 and 4 sweep one page each and complete with residue
outstanding by design.

**Two superseded directives in the corpus, rewritten rather than left.**
`seal_identity_for_erasure.go` carried a bold *"The pattern's step-2 guard as ratified defeats this,
and must change"* — falsified by increment 5, which made the completeness test field-diff the LIVE
envelope (`sealedForShreddedAt <> piiKey.shreddedAt`) rather than the marker. Keeping the guard is
correct; the comment was instructing the next reader to break working code. The same file's
`identityKey`/`subjectKey` divergence note now records how the divergence closed.

**Test-quality findings, all fixed.** The read-set coverage guard compared the pattern step against a
third hand-written copy of the read-set rather than against the fixture's own `ContextHint`, so a
fixture drifting from the dispatcher would have kept it green — the literals are now shared helpers.
The emission test never asserted the `duplicateOf` branch's `relation` label, so hardcoding it to
`"indexes"` passed; a third pass covers it. One test comment narrated a change relative to a prior
state (the CLAUDE.md no-changelog rule) and was rewritten to describe what is true now.

**Refuted / verified-correct under attack**, recorded so a later fire does not re-litigate: the
`scope:any` authorization chain is sound end to end (a `scope:any` grant ignores `AuthContext.Target`
entirely, and `loom holdsRole operator` is seeded in the same atomic primordial batch, so the ordering
is guaranteed); the new events cannot loop, storm, or mis-advance an instance (Weaver targets are
lens/CDC-driven, not event-driven, and an event whose `requestId` is not a live token resolves
nothing); the explicit `targetKey` in the event data does exactly what it claims, since step 7
otherwise pairs event *i* with mutation *i* and would name an `identityindex` vertex on a swept pass
and nothing at all on an empty one; and the declared `optionalReads` really do land in `state`, which
is what makes step 2's `subject.mergedInto` declaration load-bearing rather than decorative.

### Residuals — named, with their consumers

1. **A well-connected person's erasure still fails at step 1**, because `ShredIdentityKey` keeps its
   unbounded in-commit cascade and its 999-mutation refusal until the narrowing lands. **Consumer: the
   narrowing itself (build-order step 3)**, which is already the item's next increment — not filed as
   a separate row.
2. **`privacy-operator-grant` is a precondition of the pattern, not an optional extra.** A deployment
   installing privacy-base without it gets a pattern whose steps 2–4 are authorized and whose first is
   not. Recorded in `permissions.go` at the grant-posture paragraph that used to read as though the
   absence were deliberate for all submitters. **Consumer: any non-default deployment**; no row —
   the posture is Andrew's standing decision and the doc now names the coupling.
3. **A step-op rejection reaches the operator 60s later as the generic `"step N deadline exceeded; op
   rejected or lost"`.** `ShredBatchTooLarge`, `IdentityMerged` and `ErasureNotShredded` are
   indistinguishable at the Loom terminal. **Consumer: the operator surface (§12 step 4)**, which
   reads the residue row and the instance together — folded into that step rather than filed twice.
4. **Loom's `identity` completion domain now runs a consumer over the whole `events.identity.>`
   subject.** No other shipped pattern needs a domain that broad. Bounded and correct — an unmatched
   event is one `loom-state` GET and an Ack — but it is the first pattern to pay it. **Consumer: the
   next pattern completing on a high-volume domain.** Noted, not filed.
5. Increments 3–7's other residuals are re-inherited verbatim.

---

## Fire B build note — increment 9 (2026-08-07): narrowing the shred

### Fire brief

**Scope sentence, verbatim from §12 Fire B's internal build order, step 3.** *"`ShredIdentityKey`
drops the three enumerations, the four refusal modes and the mutation-count pre-flight — one
mutation, one event — only once the pattern demonstrably performs the work the op is giving up.
Update the DDL description, the stale in-commit rationale, and `identity-domain/ddls.go`'s comment."*

**Scope-diff gate — the step-3 precondition, discharged arm by arm before anything is deleted.** The
step is conditional (*"only once the pattern demonstrably performs the work the op is giving up"*),
so the precondition is checked rather than assumed. The op's cascade covers five arms: `indexes`
inbound, `duplicateOf` outbound and inbound, `boundTo` inbound and outbound. Against the shipped
sweeps:

| Arm | Today, in `ShredIdentityKey` | After, in | Bound |
|---|---|---|---|
| `indexes` in (+ the `identityindex` vertex) | `collect_owned_indexes` | `PurgeIdentityDedupFootprint` (inc 4) | `2·64`/pass |
| `duplicateOf` out | `collect_duplicate_of_direction` | same | `2·64`/pass |
| `duplicateOf` in | same | same | `2·64`/pass |
| `boundTo` in (+ each credential's `credentialindex`) | `collect_bound_to_direction` | `UnbindIdentityCredentials` (inc 3) | `2·64+1`/pass |
| `boundTo` out (+ the subject's `credentialindex`) | same | same | `2·64+1`/pass |

Arm-for-arm identical, and the sweeps strictly exceed the op on two of them: they tombstone the
`credentialindex` vertices the shred deliberately left standing. Both are guarded on a live
`erasureRequested` marker of class `erasureRequested`, both are steps 3 and 4 of the shipped
`identityErasure` pattern, and the `identityErasureResidue` lens (inc 5) counts exactly these five
arms while the `identityErasureComplete` target (inc 7) re-dispatches until all five are zero. The
precondition holds. **Narrow-only: no adjacent mechanism substituted, nothing widened.**

**Verified touch-list** (`file:line` checked live).

| File | What |
|---|---|
| `packages/privacy-base/shred_identity_key.go:209-296` | delete the three collectors + their six page constants |
| `…:352-361` | delete `total_muts` and the `ShredBatchTooLarge` pre-flight |
| `…:363-382` | delete the three mutation loops |
| `…:50-71` · `:94-107` · `:122` | the doc comment, the DDL `Description`, the `identityKey` FieldDescription |
| `packages/privacy-base/shred_identity_key_test.go:123-133,145,801` | `shredEnumerations` and its two `ContextHint` uses |
| `packages/privacy-base/seal_identity_for_erasure_test.go:135` | `submitShredAt`, the second test dispatcher declaring the same enumerations |
| `…:544,583,613,748` | the four cascade proofs — inverted, not deleted (below) |
| `…:648,671` | `_Reshred_Idempotent` and the Gate-3 revive vector — re-anchored on the sweep |
| `packages/privacy-base/package.go` · `manifest.yaml` | version bump (`scripts/lint-package-version.go`) |
| `packages/privacy-base/patterns.go:44` · `purge_identity_dedup_footprint.go:17,170` · `purge_identity_dedup_footprint_test.go:153,521` | the "un-narrowed shred" rationale is now history |
| `packages/identity-domain/ddls.go:85,472,498,522,938,1352,1791,1836` | eight sites asserting the shred erases links |
| `packages/identity-domain/unbind_identity_credentials.go:13,389` · `_test.go:434,500` · `erasure_gate_test.go:157` · `credential_reconcile_test.go:89,235` | same class |
| `cmd/lattice/identity/reconcile.go:205` · `reconcile_test.go:97` | same class |
| `cmd/loupe/web/js/views/graph.js:1044-1053` | the now-dead `enumerations` declaration |

**Precedents to mirror.** `purge_identity_dedup_footprint.go` and `unbind_identity_credentials.go`
are the ops the work moves to and the authority for what the corrected comments must now say.
`markExpiredDDLScript` (orchestration-base) is the shape a one-mutation, one-event op returns to.

**Increment order + the runnable green check after each.** (1) the script narrowing + the three
doc surfaces + version bump → `go test ./packages/privacy-base/ -count=1`; (2) the test inversions →
same; (3) the corpus comment corrections → `go build ./... && go test ./packages/identity-domain/
./cmd/lattice/... -count=1`; (4) gates → `go test ./... -p 4`, `make vet`, `golangci-lint run ./...`,
every `scripts/lint-*.go`, `make verify-kernel`.

**In-scope gotchas.**

- **Invert the four cascade proofs; do not delete them.** A deleted test is indistinguishable from a
  test that never existed, and these four are the only live record of which arms the erasure covers.
  Each becomes its negative — the shred leaves the link **live** — plus the positive that the arm is
  swept by the op that now owns it. A negative test alone passes for the wrong reason if the fixture
  never built the link (`feedback_negative_test_false_pass`), so each keeps its existing
  precondition assert that the link starts live.
- **The Gate-3 revive vector (`_PostShredCreate_FreshIndexNoLinkToShredded`) is the one that must not
  weaken.** It proves a later create for the same contact revives a tombstoned index rather than
  silently skipping it — a real correctness property of the *create* path that happens to be staged
  by the shred. Re-stage it on `PurgeIdentityDedupFootprint` (seal the marker, sweep, then create);
  do not drop the vector because its staging changed.
- **`shredEnumerations` declared only `indexes` + `duplicateOf` while the script also enumerated
  `boundTo` both ways** — an undeclared class-(e) enumeration in the shipped test dispatcher and in
  Loupe's submit. It disappears with the collectors rather than needing a fix, so it is recorded
  here and not filed.
- **Do not touch the placeholder-envelope path or the finalization-cycle reset** (§4.1): both stay
  verbatim. The narrowing removes mutations 2..N, never mutation 1.

**Non-goals.** §12 step 4 in full: the operator surface, the `lint-conventions` rule for §10 point 4,
and — named explicitly because it is the interesting one — **repointing Loupe's Shred button at
`StartLoomPattern{identityErasure}`**. See the regression window below.

### The window this opens, named rather than discovered later

After this increment a **direct** `ShredIdentityKey` submit — which is what Loupe's Shred button and
the `console-operator` / `demo-operator` grants do — shreds the key and nothing else: no
`erasureRequested` marker, therefore no residue-lens row, therefore no convergent tail. The dedup
footprint of an identity erased that way stays live indefinitely. Only a subject taken through the
`identityErasure` pattern is actually erased.

This is not the cascade-window §12 collapsed the five increments to avoid — that one was *the op
narrowed before the pattern existed*, and the pattern shipped in increment 8, so the work arrived
before it left. This is the separate, smaller fact that **the operator's button has not been
repointed at the pattern yet**, which is §12 step 4's own scope. It is not deferred into prose: step 4
is this item's next increment and the board row's next step names it. The repoint is genuinely step
4's rather than a fifteen-line edit here, because the shred-proof panel
(`graph.js:showShredProof`) watches one synchronous commit and would have to become a four-step
progress surface over the instance and the residue row — UX work, in Loupe's own lane.


### What increment 9 built, and why it is NOT merged

The narrowing itself is built, gated and reviewed, and it is **parked on the branch
`fire/erasure-inc9-narrow-shred` (worktree `/Users/andrewsolgan/Documents/GitHub/lattice-wt-erasure-inc9`),
not merged.** `main` still carries the un-narrowed op.

`ShredIdentityKey` is reduced to one mutation and one event — the three collectors, their six page
constants, the three `…FanoutTooLarge` refusals, the `total_muts` pre-flight and `ShredBatchTooLarge`
are gone; §4.1's keeps (placeholder envelope, finalization-cycle reset, `shreddedAt`,
`privacy.keyShredded`, the `vertex_alive` guard, the whole `RecordShredFinalization` arm) are verbatim.
The four cascade proofs are inverted rather than deleted, the Gate-3 revive vector is re-staged on the
sweep, and ~20 corpus sites that named the shred as the eraser of `boundTo`/`indexes`/`duplicateOf` now
name the op that actually does it. `go build`, `go vet`, all eight `scripts/lint-*.go` and the touched
packages pass; `identity-domain`'s diff is provably documentation-only (comment-stripped, its
executable content is byte-identical, and every differing line in `ddls.go` is a quoted `Description`).

### The precondition this brief discharged, and the axis it discharged it on

§12 step 3 is conditional: *"only once the pattern demonstrably performs the work the op is giving
up."* The brief above discharges it **arm by arm on coverage** and concludes "the precondition holds."
Coverage was the wrong axis, and the 3-layer pass found it: **nothing can start the pattern.**
`"identityErasure"` occurs exactly once outside tests — `packages/privacy-base/patterns.go:89`, its own
declaration. No Weaver playbook names it (a pattern instance is started by a playbook `pattern` action,
`internal/weaver/strategist.go:149`), `cmd/lattice/loom` has no `start`, and Loupe has no erasure
surface at all — its only affordance is the Shred button, which submits the bare op. An installed
pattern is not an invokable one, and "demonstrably performs" is a claim about reachability, not about
whether the arms line up.

What that makes the narrowing, on the only path the product ships:

- **Every arm live, permanently.** A direct shred writes no `erasureRequested` marker, so the residue
  lens — anchored `WHERE i.erasureRequested.data.requestedAt <> null` — never projects a row, the
  `identityErasureComplete` target never dispatches, and both sweeps refuse `ErasureNotSealed` if
  invoked by hand. Re-submitting the shred repairs nothing, because the shred is the thing that now
  does nothing. The window between key destruction and link erasure goes from **zero** (one atomic
  commit) to **unbounded**.
- **The erased set GROWS.** Every §6 gate keys on the marker, not on `piiKey.shredded`
  (`identity-domain/ddls.go:641-661`, consumed at `:1086` by `CreateUnclaimedIdentity`'s
  `match_is_erased`). The shredded identity's `identityindex` now survives with no marker beside it, so
  it reads as a live, unerased dedup incumbent: an ordinary same-email walk-in mints
  `lnk.identity.<new>.duplicateOf.identity.<shredded>` and emits
  `identity.created{duplicate:true, matchedIdentityKeys:[<shreddedKey>]}` — a new, durable,
  decrypt-free correlation to a person whose key was already destroyed. `MergeIdentity` gates on the
  same marker, so a bare-shredded identity also stays mergeable, and a merge repoints its contact
  hashes onto a living survivor.
- **Loupe says the opposite.** The modal reads *"Its PII becomes unrecoverable everywhere"* and the
  proof panel prints an unqualified live-KV / JetStream / projections checklist. Nothing on any surface
  says the footprint is still standing.

So the brief's "window this opens, named rather than discovered later" understated it in kind, not
just in degree: it is not that the operator's button does less than the pattern, it is that the
button's path never converges and actively adds correlations. §12's build order exists to make sure
*"erasure never does less than it does today"*, and this would breach it. The work waits for a
trigger.

### Two further defects the pass found, both in shipped code and independent of the narrowing

- **A pre-narrowing shredded subject reaches a full completion attestation with its
  `credentialindex` vertices live.** The old shred tombstoned `boundTo` both directions and
  deliberately left each `vtx.credentialindex.<hash>` standing. Run the pattern over such a subject
  and `collect_live_sweep` finds zero live links, so `sweep_inbound` never runs and
  `sweep_outbound`'s subject-`credentialindex` tombstone is gated on `len(hits) > 0`; the residue
  lens counts neighbours reached over live links, so all five arms read zero, and
  `SealIdentityForErasureComplete` walks the same five relations and nothing else. Result: a written
  `.erasure` attestation with `violating=false` over N live vertices each mapping
  `sha256(raw sign-in id) → the erased person`. `seal_identity_for_erasure_complete.go:131-135`
  already names this class as uncoverable but frames it as hypothetical; the pre-narrowing shred is a
  concrete shipped producer of exactly that shape.
- **A `MergeIdentity` landing between steps 1 and 2 burns the key and wedges the subject.** Step 1's
  guard carries `absent(subject.mergedInto.data.value)` precisely so a merged-away subject fails at
  the seal *"with nothing burned"* — but the guard is evaluated before step 1 and cannot see a merge
  that commits after it. The key is destroyed, step 2 refuses `IdentityMerged`, `mergedInto` is never
  removed, so the seal refuses permanently, no marker is ever written, and both sweeps refuse
  forever. Today the shred at least erased the footprint in the same commit; after the narrowing step
  1 erases nothing, so containment rests entirely on the merge having moved the footprint.

### Checkpoint — what the resuming fire does

**Worktree:** `/Users/andrewsolgan/Documents/GitHub/lattice-wt-erasure-inc9`, branch
`fire/erasure-inc9-narrow-shred`, carrying the narrowing as one commit. Rebase before resuming, and
squash it into the merge commit rather than landing its WIP message.

**Both numbered prerequisites below are now discharged by increment 10** (the `lattice loom start`
trigger and the §6 gate's `piiKey.shredded` condition, both on `main`). They are left in place as the
record of what the narrowing was waiting on. What remains before the merge is the list under *"Also
outstanding on the branch"*.

**Done:** the narrowing, the test inversions, the Gate-3 re-staging, the corpus corrections, the two
version bumps.

**Must land in the SAME commit as the narrowing, or before it:**

1. **A trigger.** Erasure must be startable by the operator — `StartLoomPattern{patternRef:
   "identityErasure", subjectKey}` is already granted to `operator` at `scope:any`
   (`orchestration-base/loom_lifecycle.go:154-165`), so the missing piece is the surface, not the
   authority. This is §12 step 4's operator surface, and it is now a **prerequisite** of step 3
   rather than a follow-on.
2. **A decision on the bare-shred path**, which the trigger does not by itself close: either the §6
   gates also fire on `piiKey.shredded` (so a direct shred closes the write path immediately and the
   erased set cannot grow while the pattern is pending), or the direct submit is refused outright in
   favour of the pattern. The first is the smaller change and the better semantics; it widens §6's
   gate to a second condition and needs `derive_reads` to hydrate the hit's `piiKey`.

**Also outstanding on the branch, from the same review pass:**

- §13's Inc-1 obligations are undischarged: **no test asserts the shred commits exactly one
  mutation**, and none asserts `ShredBatchTooLarge` is gone as a *symbol*. The four inversions are
  pure negatives naming five specific keys — a reintroduced cascade over any unseeded relation passes
  all four. The claim `patterns.go` and `unbind_identity_credentials.go` now rest on ("writes exactly
  one mutation, always") is executable nowhere.
- The Gate-3 vector's second property is hollowed out: seeding the marker to stage the sweep also
  makes `match_is_erased` return true, so the *"must NOT flag a duplicateOf against the shredded
  identity"* assertion is now double-covered and cannot fail for the reason it was written. Tombstone
  the marker after the sweep and before the create.
- `_LeavesBoundToLinksLive_BothDirections` still seeds a bystander link but now asserts it against an
  op that mutates nothing, so the sweep-scoping assertion is vacuous; neither sweep's own test
  asserts that a link touching neither endpoint survives — and those ops hold `scope:any` and issue
  document-less tombstones.
- Eleven CLAUDE.md no-changelog phrasings in `shred_identity_key_test.go` (five comments, six
  `t.Fatalf` strings using "…'s job now").
- Stale claims the touch-list missed: `patterns.go:84` ("walks in steps **1**, 3 and 4"),
  `identity-domain/ddls.go:591-592` (`index_vertex_mutation`'s "tombstoned in-commit" rationale),
  `purge_identity_dedup_footprint.go:91-92` (a DDL **Description** still citing "the shred's own
  999-mutation size limit"), `:234`/`:259-262`, `unbind_identity_credentials.go:213`/`:231`, and
  `shred_identity_key_test.go:476-477`'s section header.
- A stray `purgeCapDocMissingGrant()` seed in the re-staged Gate-3 test that nothing submits as.

**Verified sound under attack**, so the resuming fire need not re-litigate: the five-arm coverage
itself (identical, and strictly larger on the two `credentialindex` arms); class-divergent links under
the bodyless tombstone; dangling neighbours; self-links; every other `ShredIdentityKey` caller, none of
which asserts the cascade; and the `contextHint.Enumerations` removals, which are metadata with no
runtime consumer.

## Fire B build note — increment 10 (2026-08-07): the trigger and the gate's second condition

### Fire brief

**Scope sentence.** The two items increment 9's checkpoint named as prerequisites of the narrowing —
*"a trigger: erasure must be startable by the operator"* and *"a decision on the bare-shred path"* —
built independently of the narrowing, which stays parked.

**Scope-diff gate.** Both land on the UN-narrowed op, so neither depends on increment 9 and neither
is a partial of it. The narrowing's own commit is unchanged and still withheld; this increment
removes two of the three reasons it was withheld. Narrow-only: no adjacent mechanism substituted.

**Verified touch-list** (`file:line` checked live).

| File | What |
|---|---|
| `cmd/lattice/loom/start.go` (new) | `lattice loom start <patternRef> --subject` — the operator trigger |
| `cmd/lattice/loom/start_test.go` (new) | resolution: by name, by key, tombstoned, unknown, ambiguous |
| `cmd/lattice/loom/loom.go:1-52` | package doc (two planes) + `AddCommand` |
| `packages/identity-domain/ddls.go:640-746` | `write_path_closed` gains the piiKey condition; `key_shredded_closes_write_path` |
| `…:931-943` | `erasure_gate_keys` derives both keys per position |
| `…:1123-1135` | `match_is_erased` routes through the shared gate |
| `packages/identity-hygiene/ddls.go:298-330` · `:403-445` | the same two conditions on the merge path |
| both packages' `package.go` · `manifest.yaml` | version bumps (0.18→0.19, 0.4→0.5) |

**Precedents mirrored.** `cmd/lattice/lens`'s meta-vertex scan + `canonicalName` reader for pattern
resolution; `internal/weaver/strategist.go:145-170` for pattern-as-`authContext.target` (§10.8);
identity-domain's own `marker_closes_write_path` for the class + tombstone posture.

**Non-goals.** The narrowing itself (still parked on `fire/erasure-inc9-narrow-shred`); Loupe's Shred
button repoint; the §13 Inc-1 obligations and the other branch residuals.

### The trigger, and why it is not `lattice op submit`

`StartLoomPattern` is granted to `operator` at `scope:any`, so authority was never missing —
reachability was. `"identityErasure"` occurred exactly once outside tests, at its own declaration: no
Weaver playbook named it, `cmd/lattice/loom` had no `start`, and Loupe has no erasure surface.

`lattice op submit --operation-type StartLoomPattern` can build the envelope by hand, and that is
precisely why a named verb was needed rather than a documented incantation. Two things a hand-rolled
submit gets wrong, both silently:

- **The pattern reference must be the meta-vertex key**, not the canonical name the operator knows.
- **That key must also be `authContext.target`** — per-pattern authorization anchors on the
  definition vertex (Contract #10 §10.8). A submit that omits it authorizes against nothing.

So `start` resolves the name against the installed `meta.loomPattern` vertices and stamps the
resolved key in both places. A `vtx.meta.<NanoID>` reference is **verified, not trusted**: a
reference naming a meta-vertex of some other class would authorize against the wrong vertex, and the
refusal that follows names neither the pattern nor the reason. A tombstoned pattern does not resolve
(Core KV holds logical deletes, so a class-only scan would start an instance of a retired pattern),
and an ambiguous name refuses with both keys rather than picking one.

`start` is the only verb in this group that goes through the **Processor** rather than
`lattice.ctrl.loom.*`: starting a pattern is a state change (P2), the rest inspect or steer the
running engine.

### The bare-shred decision: widen §6, and why that is the retroactive half

The checkpoint offered two shapes — widen the §6 gates to fire on `piiKey.shredded`, or refuse the
direct submit outright. This increment builds the **first**, and the reason is that it is the only
one of the two that reaches the subjects that already exist.

Refusing the direct submit closes the path *going forward*. It does nothing for the population the
operator Shred button has already produced: identities whose key is destroyed, carrying **no**
`erasureRequested` marker, because the marker is written by the pattern's seal and the pattern did
not exist. Every §6 gate keyed on the marker alone reads each of them as a live, unerased dedup
incumbent — so an ordinary same-contact walk-in mints
`lnk.identity.<new>.duplicateOf.identity.<shredded>` and an `identity.created` carrying
`matchedIdentityKeys`, and `MergeIdentity` repoints their contact hashes onto a living survivor.
Both are new, durable, decrypt-free correlations to a person whose key is already gone.

The condition is the **weaker fact deliberately**: shredded means no writer can produce a decryptable
representation of this person again, whichever way the gate goes. Of the two ways to be wrong,
refusing writes for a subject nobody is actively converging is recoverable; growing the erased set is
what §6 exists to prevent.

**The false-positive vector is the whole risk, and it is the sharp one.** Every identity that has
taken a sensitive write carries a `piiKey` envelope — in identity-domain, *every* identity, since the
claimKey is sensitive. A gate keyed on the envelope's presence rather than its `shredded` flag, or
blind to its class, would permanently refuse the claim, link, reconcile and merge paths of the entire
PII-bearing population with no op able to reopen them. Both packages therefore check class **and**
flag, and both carry a control test asserting a live unshredded envelope leaves the path open. Under
a presence-only mutation those controls redden along with the whole suite — which is the evidence
they are load-bearing rather than decorative.

Gate order is marker-first: the marker is what `derive_reads` hydrates, so a sealed subject costs no
round trip and the piiKey read happens only on the miss.

### Three things the build settled that the checkpoint did not

- **The gate spans two packages, not one.** `marker_closes_write_path` has four consumers, and two of
  them are the sweeps' `ErasureNotSealed` precondition (`unbind_identity_credentials.go:360`,
  `purge_identity_dedup_footprint.go:332`), not write-path gates. Those stay **marker-only**: they are
  pattern steps, and a bare shred must not authorize a hand-invoked sweep. The write-path gates are
  identity-domain's claim/link/reconcile/dedup and identity-hygiene's merge — both widened.
- **`derive_reads` doubles, and that is the cheap half.** Both gate keys are derived per position, so
  the claim/link/reconcile paths pay a snapshot lookup rather than a live GET. Only the dedup path's
  per-candidate follow-up is live, at most two reads per contact type, six in the worst case.
- **The gate helper is renamed to `write_path_closed`.** `erasure_requested` named one of its two
  conditions, and a gate that fires on a fact its own name denies is the kind of thing a later reader
  disarms by accident.

### Two fixture constraints the merge-path tests hit, recorded because they are not obvious

Seeding a `piiKey` envelope by hand is not a neutral act on the identity a merge writes **into**:

- The merge encrypts `primary.credentialBinding` at step 6.5, so a hand-built envelope with an empty
  `wrappedDEK` on the primary makes that step fail — the op is then refused by the Vault, not by the
  gate, and every assertion below it passes for the wrong reason.
- **Removing a seeded envelope does not restore the untouched state.** The KV key keeps its revision,
  and the encrypt path mints a fresh envelope with a create-only write, which then fails
  `RevisionConflict` with an empty `conflictingKey` — the shape the `[Processor] A RevisionConflict on
  an UNDECLARED key names nothing` row already describes.

So the primary-side case asserts the refusal only, taking its "this really was the gate" evidence
from the wire message, which names both the guard and the side; the paired accept-arm lives on the
secondary tests, where flipping the shredded flag back changes exactly one fact.

### The proofs

- Both packages' full suites green; `cmd/lattice/loom` green.
- **Mutation-tested three ways.** Dropping the piiKey condition reddens the three closure tests in
  each package and leaves the controls green. Making the condition presence-only (no class, no flag)
  reddens the controls **and** the whole pre-existing marker suite.
- `go build ./...`, `make vet`, `golangci-lint run ./...`, all eight `scripts/lint-*.go` under
  `STRICT=1`: clean.
- `go test ./... -p 4` reddens `clinic-domain` / `identity-domain` / `lease-signing` under load with
  `ScriptTimeout: script exceeded wall budget 250ms` and `nats: invalid key`. **Baselined at clean
  `main` in a throwaway worktree**: the same packages redden with an overlapping set of tests and the
  same count, including `TestUnbindIdentityCredentials_WideSubject_ConvergesPastOnePage`, which the
  `Package drive tests redden under full-suite load` row already names. Not this change.

### Residuals — named, with their consumers

- **The narrowing's third prerequisite is now the only one left.** With a trigger and a closed write
  path, `fire/erasure-inc9-narrow-shred` still carries the §13 Inc-1 obligations and the review
  residuals listed in increment 9's checkpoint. Consumer: the next fire on this item.
- **A bare shred still converges nothing.** The write path is closed for such a subject, but no marker
  means no residue row and no convergent tail — the footprint stands until an operator runs the
  pattern. Consumer: the pre-narrowing shredded corpus, already filed as
  *"A pre-narrowing shredded subject earns a clean attestation over live `credentialindex` vertices"*.
- **Loupe's Shred button and modal still say the opposite** (*"Its PII becomes unrecoverable
  everywhere"*). Repointing it at the pattern is §12 step 4 and Loupe-lane UX work; the CLI trigger is
  what makes the pattern reachable meanwhile. Consumer: the operator.

### The precedent the build copied, and the live stack that falsified it

`loom start` first read a pattern's name off a `.canonicalName` aspect, mirroring
how `lattice lens` names a meta-vertex. A loom pattern has no such aspect. The
unit tests seeded the aspect they expected and stayed green; the running stack
answered with six patterns, **every one of them listed by key and none by name**
— so name resolution, the reason the verb exists over `op submit`, was dead on
the only corpus that matters.

The authority is `internal/weaver/registry.go`'s `indexPattern`, which reads
`patternId` off the pattern's `.spec` aspect and registers the bare vertex id
alongside it. Resolution now mirrors that: three reference forms (patternId,
vertex NanoID, full key), the spec body unwrapped on the same `steps` sentinel.
After the fix the same probe answers `backgroundCheck, collectPayment,
onboarding, leaseDocument, capabilityAuthor, identityErasure`.

**Live proof, and its boundary.** Resolution is proven against the running stack
(above), and the two packages are diff-applied live (`identity-domain`
0.18→0.19, `identity-hygiene` 0.4→0.5, `updated=4` each), with `verify-kernel`
and Loupe re-cycled on the rebuilt binaries. The **submit** half is proven by
unit tests and by Weaver's own StartLoomPattern dispatch in the convergence
suites, not by a live run: starting `identityErasure` for real destroys a key
irreversibly, which is not something to do to shared dev state as a smoke test.
The first true end-to-end belongs with the narrowing's merge, on a subject
created for it.

## Fire B build note — increment 11 (2026-08-07): the narrowing lands

### Fire brief

**Scope sentence.** Discharge the list increment 9's checkpoint left under *"Also
outstanding on the branch"* — the §13 Inc-1 obligations plus five review residuals — and merge
`fire/erasure-inc9-narrow-shred`. The narrowing's own code is unchanged from the parked commit.

**Scope-diff gate.** Both of the checkpoint's numbered prerequisites were discharged by increment 10
(`lattice loom start`, the §6 gate's `piiKey.shredded` arm), so nothing here rebuilds them. The
rebase carried two conflicts in `identity-domain/ddls.go`, both comment-only: increment 10 renamed
`erasure_requested` → `write_path_closed` and widened the derive_reads comment to name the piiKey
envelope, while the branch was correcting the same comments' account of *which op* erases `boundTo`.
Resolved by keeping increment 10's mechanism and the branch's attribution. `git diff main..HEAD` over
`identity-domain/ddls.go`, filtered to non-comment non-string lines, is empty — no increment-10
behaviour was reverted.

**Non-goals.** Loupe's shred button is not repointed at the pattern (§12 step 4, Loupe's lane); the
first true end-to-end run of `identityErasure` against a live stack is not attempted here.

### What increment 11 built

**The §13 Inc-1 obligations, which were the claim resting on nothing.** The four cascade inversions
are pure negatives naming five specific keys, so a cascade over any relation they do not name passes
all four, and `patterns.go` / `unbind_identity_credentials.go` both rest on a claim — *"writes exactly
one mutation, always"* — that was executable nowhere. `shred_identity_key_cost_test.go` states the
property instead, against the installed DDL script through the real Starlark runner:

- exactly one mutation and one event at 0, 1 and 500 links, with a counting `LinkLister` asserted
  **never consulted**. That lister is the only channel by which a link count can reach the script
  (`internal/processor/starlark_kv.go:201-210` — a nil lister makes `kv.Links` fail), so the assertion
  is generic where the five negatives were per-relation;
- a source-level assertion that `ShredBatchTooLarge`, `FanoutTooLarge`, `kv.Links` and `total_muts`
  are gone as **symbols**. §13 asked for exactly this rather than "it does not fire", because a size
  refusal on a branch no test reaches is invisible to any behavioural assertion.

Mutation-tested: reintroducing a paged `indexes` cascade with a size refusal reddens all three
connectivity arms (0 links on the never-consulted assertion, 1 and 500 on the mutation count) and both
symbol assertions.

**A scoping assertion in each sweep's own test.** `UnbindIdentityCredentials` and
`PurgeIdentityDedupFootprint` both hold `scope:any` and issue document-less tombstones, so step 6
resolves no DDL for the key being destroyed: the enumeration's key filter is their whole confinement.
Each now seeds a link touching **neither** end of the subject and requires it to survive — a `boundTo`
between two other people, a `duplicateOf` between two others, and another person's own `indexes`
footprint. Mutation-tested by widening the `"in"` filter by one segment
(`lnk.*.*.<rel>.<type>.<id>` → `lnk.*.*.<rel>.>`): all three redden, and only those three.

The bystander in `_LeavesBoundToLinksLive_BothDirections` stays, with its claim corrected — against an
op that tombstones nothing it shows the fixture is a real corpus, and nothing more. The assertion that
can fail now lives with the op that does the tombstoning.

**The Gate-3 vector's second property, stated honestly rather than staged.** The checkpoint asked for
the marker to be tombstoned after the sweep so the duplicateOf refusal would rest on the
`piiKey.shredded` arm alone. It does not work, for two independent reasons the review found: a
**tombstoned** `erasureRequested` still closes the write path by design
(`identity-domain/ddls.go:728-742` — presence of the class is the signal, live or not), and the sweep
has already tombstoned the index, so `live_hit` drops the candidate before `match_is_erased` is
consulted at all. So the retraction is dropped and the test's doc comment says what the assertion is —
a shape check on the revive path — and points at where the gate is proven where it *can* fail, against
a live index and with a positive control:
identity-domain's `TestErasureGate_CreateUnclaimedIdentity_SkipsBareShreddedIncumbent`.

**Loupe's shred panel said the opposite of what the op does.** The bounds line now names the
decrypt-free footprint a bare shred leaves standing — contact-hash index vertices, `duplicateOf`
pairs, credential bindings — and points at `lattice loom start identityErasure --subject <identityKey>`.
Repointing the button itself is still §12 step 4.

**The corpus claims the touch-list missed:** the pattern's step list (`patterns.go` — the class-(e)
walks are in steps 3 and 4, not 1); `index_vertex_mutation`'s revive rationale, which cited an
in-commit tombstone; both sweeps' read-posture notes, which cited "the same posture privacy-base's
shred-time enumerations declare"; and the two ceiling comments that measured themselves against a
999-mutation pre-flight that no longer exists. Plus a stray `purgeCapDocMissingGrant()` seed nothing
submitted as, and eleven no-changelog phrasings.

`identity-domain` 0.19 → 0.20 (four DDL `Description`s and the sweep script's in-string comments),
`privacy-base` 0.10 → 0.11 (the `shredIdentityKey` script body, the purge `Description`).

### The precondition that was checked on the wrong axis, and what it cost

Increment 9's brief discharged §12 step 3's *"only once the pattern demonstrably performs the work the
op is giving up"* arm by arm on coverage, and its own 3-layer pass found coverage was the wrong axis:
nothing could **start** the pattern. That is the whole reason this took three fires rather than one —
the narrowing was correct and complete in increment 9 and still unlandable, because "demonstrably
performs" is a claim about reachability. The generalisable form: a precondition naming an outcome the
work must already produce is checked on the *dispatch* path first, not on the arms.

### The proofs

- `go build ./...`, `make vet`, `golangci-lint run ./...` (cache cleaned), all eight
  `scripts/lint-*.go` under `STRICT=1`, and `lint-package-version` with `DIFF_BASE=main`: clean.
  `gofmt -l` over every file this fire touched: clean.
- `packages/privacy-base`, `packages/identity-hygiene`, `cmd/lattice/...`: green.
- Three mutation tests, each reverted after: the reintroduced cascade, the widened `"in"` filter, and
  (from increment 10) the dropped piiKey condition.
- **`packages/identity-domain` reddens under its own package-level load, and so does clean `main`.**
  Three runs at this commit and three at `9d80900d` each redden a *rotating* membership —
  `TestUnbindIdentityCredentials_WideSubject_ConvergesPastOnePage`,
  `TestRecordPII_OperatorRoleOnSecondPage`, `TestErasureGate_CompleteCredentialLink_RejectsSealedIdentity`,
  `TestProvisionConsumerIdentity_UnknownConsumerRoleKey_Rejected` — with green runs interleaved on both
  sides. Not this change; it is the filed *Package drive tests redden under full-suite load* row, whose
  reach this fire widens from full-suite to single-package.

### Residuals — named, with their consumers

- **A bare shred leaves a live `identityindex` owned by an erased person, and the next walk-in on that
  contact gets no index of their own.** `CreateUnclaimedIdentity` writes an index vertex only when the
  hit is absent or tombstoned, so a live-but-erased incumbent means the newcomer is neither deduped
  (correct — the §6 gate skips it) nor indexed (wrong). Consumer: the second person to walk in on a
  bare-shredded contact. Already filed as *A sealed identity's live index vertex denies the next person
  their own*; this fire widens that row's reach from sealed to bare-shredded.
- **The pattern has never run end to end against a live stack.** Every arm is proven in package tests
  and the submit path by Weaver's own dispatch, but no subject has been taken through all four steps on
  the running stack, because doing so destroys a key irreversibly. Consumer: the first real erasure.
- **Loupe's Shred button still submits the bare op.** The panel now says so; repointing it is §12 step
  4 and Loupe-lane UX work. Consumer: the operator.

## Fire brief — CreateUnclaimedIdentity repoints a live-but-erased incumbent's identityindex (Steward, 2026-08-15)

**Scope sentence.** Extend `CreateUnclaimedIdentity`'s per-contact index-vertex mutation trigger to
also fire when the live hit's incumbent has its erasure write-path closed (sealed **or** bare
key-shredded), CAS-repointing the `identityindex` vertex to the new registrant and tombstoning the
stale `indexes` link to the erased incumbent — closing the residual named directly above ("a bare
shred leaves a live `identityindex` owned by an erased person, and the next walk-in on that contact
gets no index of their own"), the board row *[identity-domain] An erased identity's live index vertex
denies the next person their own* (`backlog/lattice.md`, ★★, S).

**Verified touch-list (live at fire start):**
- `packages/identity-domain/ddls.go:1189-1254` — `CreateUnclaimedIdentity`'s dedup-check block (builds
  `duplicate`/`matched` off `live_hit`/`match_is_erased`) and its mutation-build block (the
  `email`/`phone`/`name` `if hit == None or hit.isDeleted:` guards that decide whether to call
  `index_vertex_mutation` + create the `indexes` link).
- `packages/identity-domain/ddls.go:479-504` — the `indexes` link-type DDL's `Description`: currently
  lists only `CreateUnclaimedIdentity` (create), `MergeIdentity` (repoint), `PurgeIdentityDedupFootprint`
  (tombstone) as writers; add that `CreateUnclaimedIdentity` itself now also repoints, on the
  erased-incumbent branch.
- `packages/identity-domain/package.go:33` — `Version: "0.20.4"` → `"0.20.5"` (DDL script body changes;
  same-version edits no-op per package-authoring convention).
- `packages/identity-domain/erasure_gate_test.go:302-353, 656-708` —
  `TestErasureGate_CreateUnclaimedIdentity_SkipsSealedIncumbent` /
  `...SkipsBareShreddedIncumbent`: both must keep passing UNCHANGED (they assert `duplicateOf` absence,
  which this fire does not touch); add sibling tests asserting the `identityindex` vertex and `indexes`
  link now repoint to the new registrant in both the sealed and bare-shredded cases, and that the old
  link tombstones.

**Precedent to mirror.** `identity-hygiene/ddls.go:698-726`, `MergeIdentity`'s `idx_repoints` loop:
tombstones the old `indexes` link **unconditioned** (`op:update`, `isDeleted:true`, `data:{}`, no CAS —
safe there because the tombstone never lands alone; it is always in the same atomic batch as the
CAS-guarded repoint of the vertex it targets, so any concurrent-erasure race that invalidates one
invalidates the whole batch), then repoints the `identityindex` vertex, then creates the new link if not
already live. `index_vertex_mutation` (ddls.go:604-619) already implements the CAS-guarded vertex
repoint (`expectedRevision: existing.revision`) exactly as needed here — reuse it verbatim.

**In-scope gotchas:**
- `match_is_erased()` (ddls.go:1172-1184) does live, undeclared `kv.Read`s via `write_path_closed`
  (read-posture (e)) — currently invoked once per contact type in the dedup-check branch. Compute it
  ONCE per contact type into a local (e.g. `email_erased`) and reuse it in the mutation-build branch;
  calling it a second time doubles live reads on that path for no reason.
- The old `indexes` link key is a **deterministic derivation**
  (`"lnk." + index_key[len("vtx."):] + ".indexes.identity." + old_identity_id`, `old_identity_id` from
  `hit.data["identityKey"]` stripped of the `"vtx.identity."` prefix) — mirrors the exact string-building
  idiom already used two lines below for the NEW link key. No enumeration and no extra read needed for
  the tombstone itself; the invariant (identityindex vertex live ⇒ its current `indexes` out-link is
  live) holds because neither `SealIdentityForErasureComplete` nor `ShredIdentityKey` ever touches
  `identityindex` or `indexes` (grounding, this fire: `privacy-base/seal_identity_for_erasure.go`,
  `privacy-base/shred_identity_key.go` — neither writes either key class).
- Do **not** touch the `duplicateOf`-skip behavior — the two Skip*Incumbent tests above must pass
  byte-identical to today; this fire only adds the missing index repoint, never a fresh `duplicateOf`
  toward an erased incumbent (that would re-open exactly the hazard §6 closes).
- `packages/identity-domain` is a known ROTATING-membership package-level flake under parallel load,
  unrelated to any single change (this doc's own increment-9 build note, immediately above) — if a test
  outside this fire's diff reddens, re-run that single test alone once before concluding it's a real
  regression; never loosen an assertion to route around it.

**Non-goals.** The other two residuals named directly above (live-stack end-to-end erasure run; Loupe's
Shred-button UX) are explicitly out of scope for this fire.

**Adjacent finds filed to the board now:** none — this fire closes the already-filed row directly rather
than surfacing a new one.

**Review depth:** full 3-layer adversarial (security/PII-plane, identity dedup path), per steward
SKILL.md §4, regardless of S–M size.

## Fire brief — TombstoneOrphanedCredentialIndex, pre-narrowing shred residue cleanup (Steward, 2026-08-21)

**Scope sentence.** A new narrow, fail-closed identity-domain operation plus a CLI driver that
together tombstone a `credentialindex` vertex left standing by the pre-narrowing (pre-`54b3c8c7`)
`ShredIdentityKey`, which tombstoned `boundTo` both directions but deliberately left each credential's
index vertex live — the board row *[privacy-base] A pre-narrowing shredded subject earns a clean
attestation over live `credentialindex` rows* (`backlog/lattice.md`, ★★, S).

**The bug, precisely.** `SealIdentityForErasureCompleteDDL()`'s comment
(`packages/privacy-base/seal_identity_for_erasure_complete.go:131-135`) already names this residue
class as "NOT walkable ... invisible to the sweep and to this walk alike" but frames it as
hypothetical. It is not: any identity shredded via a bare `ShredIdentityKey` submit **before**
`54b3c8c7` landed (2026-08-07) has its `boundTo` links already tombstoned, so
`UnbindIdentityCredentials`'s `collect_live_sweep` (`packages/identity-domain/unbind_identity_credentials.go:192-234`)
finds zero live links in both directions and emits **no mutations at all** (`sweep_inbound`/
`sweep_outbound` are both hit-gated), and the five-arm residue lens + `SealIdentityForErasureComplete`'s
own re-walk read zero on every arm. The result: a `.erasure` attestation reading `violating=false` while
N `vtx.credentialindex.<hash>` vertices — plaintext `{actorKey, identityKey, boundAt}`, i.e.
`sha256(raw sign-in id) → the erased person` — are still live and readable. ~~**Population is capped**:
the producer (the pre-narrowing cascade) was deleted by `54b3c8c7`; every post-narrowing erasure runs
`UnbindIdentityCredentials`, which tombstones the index and the link together, so no new instance of
this shape can be created going forward. This is a cleanup of existing residue, not a recurring gap.~~
**The struck claim is FALSE — corrected 2026-08-21, see the close-out note at the end of this section.**
The population is not capped: `ReconcileCredentialBinding`'s own never-linked corpus becomes this exact
shape whenever one of its endpoints is later sealed or shredded, which is ongoing. The op as built is
framed as a general erased-endpoint / no-live-link residue verb, with the pre-narrowing corpus as its
currently-known instance. The scope stated below is also **half the class** — see the same note.

**Verified touch-list (live at fire start, from the Phase-0 scout + Winston's own reads).**
- `packages/identity-domain/ddls.go:686-720` — `write_path_closed(identity_key)`, the existing dual
  discriminator (live `erasureRequested` marker OR `piiKey.shredded`) already used by
  `ReconcileCredentialBinding` to refuse touching an erased identity's binding plane. The new op's
  own "is this genuinely erased" guard must read the SAME two keys the SAME way (live `kv.Read`, not
  `state[...]` — the guard's whole point is that an undeclared read still refuses).
- `packages/identity-domain/ddls.go:921-922` — `credential_index_key(actor_key)` =
  `"vtx.credentialindex." + crypto.sha256NanoID(actor_key)`.
- `packages/identity-domain/ddls.go:941-949` — `credential_bound_to_key(credential_actor_key,
  owner_identity_key)` = `"lnk.identity." + credential_id + ".boundTo.identity." + owner_id`
  (Contract #1 §1.1: credential is source, owner is target).
- `packages/identity-domain/ddls.go:1952-2075` — `ReconcileCredentialBinding`'s handler is the closest
  sibling and the precedent for "author-declares-intent": the payload names both `credentialActorKey`
  and `identityKey`, the op reads the index vertex the payload names and refuses `owner-mismatch` if
  its stored `identityKey` disagrees, and it already refuses outright (`fail_reconcile("erased")`) on
  either endpoint's `write_path_closed` — so `ReconcileCredentialBinding` never touches an erased
  identity's index, and the new op below is its non-overlapping opposite number (only ever touches an
  erased identity's index). No collision risk between the two ops on the same key.
- `packages/identity-domain/unbind_identity_credentials.go` (whole file) — the file-shape precedent to
  mirror exactly: standalone file, own DDL func + own script constant carrying its own copies of
  `required_string`/`parts_of`/`vertex_alive` (Starlark has no `load()`), `operator`-only
  `scope:any` grant, `[no-op-meta: engine-op]` (machinery, no client-facing descriptor), registered by
  appending its DDL func's return value inside `DDLs()` (`ddls.go:564`).
- `packages/identity-domain/permissions.go:100-106` — `UnbindIdentityCredentials`'s permission-vertex
  entry, the shape to mirror for the new op's grant (same file, same `Permissions()` function, append
  before `RevocationPermissions()...`).
- `packages/identity-domain/manifest.yaml` — hand-maintained (confirmed: no `make regenerate-manifest`
  target touches it), needs a new `ddls:` entry (`class: meta.ddl.vertexType`, no `opMetas:` entry —
  matches `UnbindIdentityCredentials`/`ReconcileCredentialBinding`, both absent from `opMetas:` as
  engine-ops), a new `permissions:` entry, and the description's op list extended. `package.go:33`
  `Version: "0.20.5"` → `"0.20.6"` (same-version edits no-op per package-authoring convention).
- `cmd/lattice/identity/reconcile.go` (whole file) — the CLI driver precedent: `credentialIndexPrefix`
  scan via `conn.KVListKeysPrefix`, per-key `KVGet` + classify (self-loop / already-OK / retracted /
  submit), `submitReconcile`'s `processor.OperationEnvelope` + `output.SubmitOp` shape (`RequestID` via
  `substrate.NewNanoID()`, `Lane: processor.LaneDefault`, `Class: "identity"`,
  `AuthContext: &processor.AuthContext{Target: owner}`), `--actor`/`--dry-run` flags, dual JSON/human
  output, non-zero exit on any `Rejected`.
- `cmd/lattice/identity/identity.go:38-48` — `NewCommand`'s `cmd.AddCommand(...)` registration list;
  append the new subcommand's constructor call.

**Precedent to mirror — the exact division of labor.** `ReconcileCredentialBinding` already proves the
inverse shape works: it is the "the link is missing, the owner is alive" repair verb and explicitly
refuses an erased owner. This fire's op is "the link is missing or retracted, the owner is erased"
repair verb, and must explicitly refuse a LIVE link (defer to the ordinary `UnbindIdentityCredentials`
sweep for that case) exactly as `ReconcileCredentialBinding` refuses an erased owner — two narrow,
non-overlapping carve-outs, same shape as the `0bb6daea`/Fire-28 "diffManifest gains a narrow,
well-commented carve-out right where a mutation is about to be emitted" precedent family, applied here
to an operation handler instead of a package installer.

**Increment order + runnable green check after each.**
1. `packages/identity-domain/tombstone_orphaned_credential_index.go` (new file) —
   `TombstoneOrphanedCredentialIndexDDL()` + `tombstoneOrphanedCredentialIndexDDLScript`, mirroring
   `unbind_identity_credentials.go`'s file shape. Payload `{credentialActorKey, identityKey}` (both
   required, `vtx.identity.<NanoID>`-shaped, self-loop refused). Script:
   - Derive `index_key = credential_index_key(credential_actor_key)`; refuse
     `CredentialIndexAlreadyClear` if absent or already tombstoned (idempotent-friendly for a
     re-driven CLI sweep).
   - Refuse `OwnerMismatch` unless the index vertex's own `data.identityKey == identityKey` **and**
     `data.actorKey == credentialActorKey` (author-declares-intent: the caller must already know the
     content it is asking to remove, never a blind key-only delete).
   - Refuse `NotErased` unless `identityKey`'s write path is closed — same two-key check as
     `write_path_closed` (live `erasureRequested` marker OR `piiKey.data.shredded == true`), read via
     `kv.Read`, never `state[...]`.
   - Refuse `StillBound` if `credential_bound_to_key(credentialActorKey, identityKey)` reads a LIVE
     (non-tombstoned) link — this is the guard that keeps the op narrow to the orphaned-residue shape;
     a live link means the ordinary sweep is the correct path, not this one.
   - On success: `mutations = [{"op": "tombstone", "key": index_key}]`; `events =
     [{"class": "identity.unbound", "data": {"identityKey": identityKey, "actorKey":
     credentialActorKey}}]` — reuse `identity.unbound` verbatim rather than minting a new event type:
     the outcome is semantically identical to what `UnbindIdentityCredentials`'s inbound sweep would
     have emitted for this exact pair had the link still existed to enumerate, and the Gateway's
     credential-bindings-bucket consumer already handles a redundant/idempotent `unbound` for a row
     that was never materialized there. `response = {"primaryKey": index_key}` (a vertex-root mutation
     key inside the op's own write footprint — mirrors `ReconcileCredentialBinding`'s
     `primaryKey: link_key`).
   Read-posture comments: `credentialActorKey`/`identityKey` derived keys are read via `state[...]`
   (declare `index_key` in ContextHint.Reads — deterministic, payload-derived, class (c)); the
   erasure-discriminator + boundTo-link reads are class (d) optionalReads, live, exactly as
   `write_path_closed`'s own comment explains (an undeclared read must still refuse, not silently
   pass).
   Register in `packages/identity-domain/ddls.go`'s `DDLs()` (append
   `TombstoneOrphanedCredentialIndexDDL()` beside `UnbindIdentityCredentialsDDL()`).
   Green check: `go build ./packages/identity-domain/...`.
2. `packages/identity-domain/permissions.go` — append the `TombstoneOrphanedCredentialIndex` grant
   (`scope: any`, `GrantsTo: []string{"operator"}`, `[no-op-meta: engine-op]`, note naming the narrow
   refusal conditions so the grant's own doc states what it cannot reach). `manifest.yaml` — add the
   `ddls:`/`permissions:` entries + version bump; `package.go:33` version bump to match.
   Green check: `go build ./... && STRICT=1 go run ./scripts/lint-package-version.go`.
3. Tests, `packages/identity-domain/tombstone_orphaned_credential_index_test.go`, mirroring
   `unbind_identity_credentials_test.go`'s harness (`CapabilityDoc` with/without the grant, real
   Processor commit path, not a script-only test): a live-orphan-tombstoned success case (link
   tombstoned, owner erased via bare `piiKey.shredded` — the actual historical shape); a second success
   case with owner erased via `erasureRequested` instead (full-pattern shape, for completeness even
   though `UnbindIdentityCredentials` would normally have already swept it); `NotErased` refusal on a
   live, unerased owner (the safety property that matters most — prove the op cannot touch a live
   person's index); `StillBound` refusal when the link is live; `OwnerMismatch` refusal;
   `CredentialIndexAlreadyClear` refusal (already-tombstoned); the ungranted-actor denial (mirror
   `unbindCapDocMissingGrant`'s pattern, granting some OTHER identity-domain op instead so the denial
   test cannot pass on the lane check alone).
   Green check: `go test ./packages/identity-domain/... -count=1`.
4. `cmd/lattice/identity/credential_residue.go` (new file) — `newSweepCredentialResidueCommand`,
   mirroring `reconcile.go`'s `newReconcileBindingsCommand` shape exactly (flags, output modes, exit
   code). Driver (`sweepCredentialResidue`, testable without cobra, mirrors `reconcileBindings`):
   scan `credentialIndexPrefix`; for each **live** entry, skip self-loop (`cred == owner`); read
   `boundToKey(cred, owner)` (reuse the existing unexported helper from `reconcile.go` — same package,
   same binary); if the link is **live** → `AlreadyOK`, skip (in scope for
   `UnbindIdentityCredentials`'s ordinary sweep, not this tool); if the link is **absent or
   tombstoned**, read the owner identity's erasure state (new small helper mirroring
   `write_path_closed`'s two-key check, issued as two extra `KVGet`s — this CLI has no live-read
   budget to respect, unlike the Starlark op) — if **not erased** → `NotErased`, skip (an absent link
   with a live owner is `ReconcileCredentialBinding`'s repair job, not this tool's; a tombstoned link
   with a live owner is the pre-existing "Retracted" shape `reconcile-bindings` already declines to
   touch — leave it exactly as untouched as that tool leaves it); else submit
   `TombstoneOrphanedCredentialIndex{credentialActorKey: cred, identityKey: owner}` (dry-run counts
   `Submitted` without submitting, mirrors `reconcileBindings`). Register in `identity.go`'s
   `NewCommand`.
   Green check: `go build ./cmd/lattice/... && go test ./cmd/lattice/identity/... -count=1`.
5. Full gates: `go build ./...`, `make vet`, `golangci-lint run ./...`, every `scripts/lint-*.go`,
   `go test ./... -p 4`, `make verify-kernel`.

**In-scope gotchas.**
- `write_path_closed`'s comment (`ddls.go:686-720`) is explicit that the LIVE `kv.Read` (not
  `state[...]`) IS the guard, not an optimization — an undeclared key reads ABSENT from `state[...]`,
  which would silently open a fail-closed gate. Copy the read shape, not just the boolean logic.
- `credential_index_key`/`credential_bound_to_key` must be copied into the new script verbatim
  (Starlark has no `load()` — every sibling script carries its own copies; this is normal here, not
  duplication to clean up).
- The op's `OutputSchema`/response must name `index_key` as `primaryKey`, not `identityKey` or
  `credentialActorKey` — the reply-constraint (`internal/processor/commit_path.go`) requires a
  script-named `primaryKey` to lie inside the operation's own write footprint, and `index_key` is the
  only key this op writes.
- Do not let the CLI driver's "erased" check reimplement `write_path_closed` loosely — it must check
  the exact same two keys (`erasureRequested` class-gated marker OR `piiKey.data.shredded == true`) so
  a candidate the driver selects is never rejected `NotErased` by the op itself.
- `packages/identity-domain` is a known ROTATING-membership package-level flake under parallel load
  (this doc's increment-9 build note, above) — one re-run of a single reddened test before concluding
  it's a real regression; never loosen an assertion to route around it.

**Non-goals.** Re-sealing or rewriting any existing `.erasure` attestation once the residue is cleared
— the attestation's `violating=false` claim becomes retroactively accurate the moment the live rows are
gone; no consumer needs `coverage.credentials` to reflect this historical adjustment. Discovering the
exact live count of affected subjects on any particular deployment — that is what `--dry-run` is for,
run by an operator, not something this fire computes or bounds further. Extending
`SealIdentityForErasureComplete`'s own walk to cover this class going forward — the class is provably
unwalkable in-Starlark (no link exists to enumerate from), ~~and the population is capped by
`54b3c8c7`, so there is no recurring gap to close in the op itself, only historical residue to clean up
once~~ (the capped-population half of that reason is false — see the close-out note; the walk is still
not the fix, because no link survives to enumerate from).

**Adjacent finds filed to the board now:** none expected — this fire closes the already-filed row
directly.

**Review depth:** full 3-layer adversarial (security/PII-plane: this mints a new tombstone-authority
operation over identity-adjacent data) — per steward SKILL.md §4, regardless of S size.

**Close-out note — 2026-08-21, post-review. Two claims above were falsified; the op as built is wider
than the brief that scoped it.** Recorded here rather than by rewriting the brief, so the brief still
reads as what was scoped and this note as what review found.

1. **The residue is bidirectional; the brief scoped only the inbound half.** `54b3c8c7^`'s
   `collect_bound_to_links` tombstoned `boundTo` in **both** directions ("in" + "out"), so a
   pre-narrowing shred left two residue shapes, not one:
   - *inbound* — the erased subject S is the link's target, i.e. the row's OWNER. The surviving index
     at `sha256(C)` reads `{actorKey: C, identityKey: S}`.
   - *outbound* — S is the link's **source**, i.e. S is itself a credential of some other identity O
     (a merged-away identity folded into its survivor as an implicit self-credential; a Scenario-B
     identity later linked to another). The surviving index at `sha256(S)` reads
     `{actorKey: S, identityKey: O}` — keyed by a derivative of the **destroyed** identity and naming
     S in the clear. Same leak, same plaintext, same erased person.

   The op's first cut gated `NotErased` on `write_path_closed(identityKey)` alone, so it accepted the
   inbound shape and refused the outbound one — which the CLI driver then silently skipped, since it
   classified on the same one-sided rule. **Fixed:** the discriminator is now symmetric —
   `write_path_closed(identityKey) OR write_path_closed(credentialActorKey)` — in the script
   (`tombstone_orphaned_credential_index.go`) and in the driver
   (`cmd/lattice/identity/credential_residue.go`). Nothing else about the op changed: still one
   mutation on `index_key` only, `OwnerMismatch`/`StillBound`/`CredentialIndexAlreadyClear` unchanged.
   Widening the gate does not widen what the verb can **reach**, because `OwnerMismatch` already forces
   the stored row to name both endpoints exactly as the payload does — no caller can invent a row whose
   `actorKey` is some erased identity in order to reach a live person's index.

2. **The population is not capped.** The brief's "no new instance of this shape can be created going
   forward" is false: the op also (correctly) accepts the *legacy-binding, link-never-existed* corpus —
   `ReconcileCredentialBinding`'s own population — the moment either of its endpoints is later sealed
   or shredded, and that keeps happening. **Fixed:** the DDL comment, the permission Note, the manifest
   description and the CLI `Long` no longer frame this as a one-shot cleanup; they state a general
   *erased-endpoint, no-live-link* residue verb whose **currently-known** instance is the pre-narrowing
   (2026-08-07 and earlier) population.

**Deliberately still out of scope — filed, not deferred.** The outbound arm leaves a stale phantom
entry in the LIVE owner's own `credentialBinding` array (the precedent for rewriting it is
`unbind_identity_credentials.go`'s `owner_binding_rewrite`). That is a **different** concern: data
hygiene in a live third party's sign-in-methods list, not a privacy leak of the erased person —
tombstoning the index alone already fully closes the plaintext-correlation leak this fire exists to
fix. Filed as its own row on `backlog/lattice.md` (Component maintenance, ★, XS–S).

## Fire brief — privacy-base verification tooling (Steward, 2026-08-21)

### Scope sentence

Give **privacy-base** the post-install assertion tooling every other core package already has, and run
the erasure spine end to end on a live stack for the first time: a `verify-package-privacy-base` Makefile
target + `scripts/verify-package-privacy-base.go` asserting the installed spine (13 DDLs, 4 lenses, the
`identityErasureComplete` weaver target, the `identityErasure` Loom pattern, 5 permissions + their grants,
the package vertex + manifest), wired into CI's `stack-gates` job; and a
`verify-claim-ceremony.go`-style one-shot that drives all four spine steps against a **disposable**
subject minted for the run.

Green bar: `make verify-package-privacy-base` passes against a live stack; the one-shot drives
`ShredIdentityKey → SealIdentityForErasure → UnbindIdentityCredentials → PurgeIdentityDedupFootprint`
on a throwaway identity and asserts each step's observable state; both are deterministic enough to gate CI.

### Why the erasure spine is the one package with no verifier

`make up` installs privacy-base as a dependency of the vertical packages, so it is present in every
stack CI builds — but no step asserts anything about it afterwards. A diff-apply that dropped a lens,
a permission, or the Loom pattern's step order would leave a stack that looks healthy and an erasure
that silently no longer runs to completion. Every sibling package (`rbac`, `identity`, `objects-base`,
`clinic-*`, `location-*`, `augur`, `lease-signing`) has a `verify-package-*` gate; the erasure plane,
which is the one whose failure is legally load-bearing, has none.

The second half exists because the spine has never actually been *run* on a live stack. Step 1 destroys
a key irreversibly, so exercising it demands a subject minted to be destroyed — which is precisely why
it had not been done, and why the one-shot mints its own rather than reusing a fixture identity.

### Verified touch-list (`file:line`, re-checked live this fire)

- `Makefile:443-450` (`verify-package-identity`) — the target shape to mirror: build `lattice-pkg`,
  `./bin/lattice-pkg install packages/<p>` under `NKEY_LATTICE_PKG`, then
  `go run ./scripts/verify-package-<p>.go` under `NKEY_LATTICE_CLI`. `Makefile:568-586`
  (`verify-package-lease-signing`) is the multi-dependency co-install shape.
- `scripts/verify-package-identity.go` + `scripts/pkgverify/verify.go` — the assertion harness
  (Core KV reads, OK/FAIL counters, exit 0/1).
- `scripts/verify-claim-ceremony.go` — the live-ceremony shape: mint a disposable subject, drive the
  ops, poll to convergence. Note its own board row: its 5s `waitForRoleGrant` deadline reads real
  unbounded latency as failure. **Poll to convergence here; do not copy the fixed deadline.**
- `packages/privacy-base/package.go:44-55` (`Definition`) + `ddls.go:55-130` (13 DDLs),
  `lenses.go` (4), `weavertargets.go` (1), `loompatterns.go` (1), `permissions.go` (5).
- `packages/privacy-base/manifest.yaml` — the declared set the verifier asserts against.
- `.github/workflows/ci.yml:213-277` (`stack-gates`) — where the new step lands.
- Spine ops: `shred_identity_key.go`, `seal_identity_for_erasure.go`,
  `purge_identity_dedup_footprint.go` (privacy-base); `UnbindIdentityCredentials` (identity-domain).

### Increment order + green checks

1. **Inc 1 (mechanical).** `scripts/verify-package-privacy-base.go` + the Makefile target.
   Green: `make verify-package-privacy-base` against a live stack.
2. **Inc 2 (mechanical).** CI `stack-gates` step, placed after `verify-package-identity`.
   Green: the target is order-independent given its own co-install.
3. **Inc 3 (posture-changing — first live destructive run).** The four-step one-shot on a disposable
   subject. Green: all four steps assert; re-runnable without manual cleanup.

### In-scope gotchas

- **Poll to convergence, never a fixed deadline** — the `verify-claim-ceremony` 5s-SLA defect is an open
  board row; do not reproduce it. No fixed `time.Sleep` for synchronization (CLAUDE.md).
- **The one-shot must mint its own subject.** Step 1 is irreversible; a run that erases a shared fixture
  identity poisons every later assertion on that stack.
- **Step 3 (`UnbindIdentityCredentials`) is identity-domain's**, so the one-shot's stack needs
  identity-domain installed — the target co-installs it, mirroring `verify-package-lease-signing`.
- **Steps 3 and 4 are sweeps** bounded at `2·SWEEP_LIMIT`; a subject with no credentials and no dedup
  footprint still commits them (idempotent, zero-row). Assert the commit, not a row count.
- P5/P2 hold: the verifier READS Core KV as a platform script (sanctioned — it is tooling, not a
  `cmd/<app>`), and every state change it makes goes through an op.

### Non-goals (the drift fence)

- **No change to the spine itself** — no op, DDL, lens, pattern, or permission edit. If the verifier
  finds the installed shape wrong, that is a finding to fix as its own unit, not a spec to relax.
- **No Vault backend change.** The one-shot destroys a disposable subject's key through the normal path.
- **No new `make down`/teardown semantics** — the one-shot leaves the stack usable.

### Built shape — closed (build note, 2026-08-21, one fire)

All three increments shipped. `verify-package-privacy-base` (140 assertions) asserts the installed
shape and runs in CI's `stack-gates` job; `verify-erasure-ceremony` (35 assertions) drives the spine
live and is **deliberately not wired into CI** — a green run is ~1s, but each stuck wait costs its full
convergence ceiling, so a red run is slow by construction, and the ceremony is destructive by nature.
Both were proven on a stack rebuilt from scratch, including the first-ever install of
`privacy-operator-grant`.

**Two forks resolved here, not deferred.**

*The ceremony drives the four ops directly rather than through Loom.* The pattern's declared shape —
four `systemOp` steps in order over `subjectType: identity` — is asserted statically by
`verify-package-privacy-base`, and proven to bite by inverting the expected order before landing. The
`stack-gates` job runs no orchestration tier, so routing the ceremony through Loom would mean running
`loom` + `weaver` and absorbing async step-advance convergence without covering any more of the ops
themselves. The split is therefore: the package gate owns the spine's *shape*, the ceremony owns its
*behaviour*. **What this leaves genuinely uncovered** is the pattern's own machinery — its guards, its
step-advance-on-domain-event correlation, and §5.2/§5.3's `StepSpec.Reads` question. Nothing runs
`identityErasure` as a pattern yet; that is a real gap, named here so a later fire does not mistake
"the four steps pass" for "the pattern works".

*Step 1 is reached through `privacy-operator-grant`, never a self-minted grant.* `ShredIdentityKey`
ships no grant from privacy-base because erasure is a deployment's consent decision, and the sanctioned
way to confer it is that separate package. A harness minting itself the permission would have defeated
the boundary rather than tested it; on a stack without the consent package, step 1 is correctly denied.

**What the first live run established.** The four steps work end to end, in order, first attempt — no
op rejected, no payload or `contextHint` surprise. The async half's first leg genuinely ran: the
privacy-worker consumed `privacy.keyShredded`, called `Vault.ShredKey`, and committed
`RecordShredFinalization{vaultKeyDestroyed}`. The key destruction is irreversible on a real stack, so
the disposable-subject rule is load-bearing rather than ceremonial. Step 4's one-class-per-commit design
is observable exactly as §5.4 derives it: pass 1 `indexes`, pass 2 `duplicateOf`, pass 3 `purged=0`.

**Observed but NOT a spine defect:** `refractor keyshredded: grant-table revoke failed
(privacy-critical, no retry) — relation "actor_read_grants" does not exist` fires once per shred on a
stack that never ran `provision-readpath` (which creates that table, and which shells through
`docker compose` and so could not run in this container). It is a provisioning gap in the verifying
environment, not a finding, and is recorded here so the next reader of these logs does not re-diagnose it.

---

## Fire brief — the erasure-residual batch: three filed residuals closed (Steward, 2026-08-23)

Three rows on `backlog/lattice.md` trace to this document's own increment build notes. They are built
as one batch because they share a subject — the erasure spine's declaration and observation surfaces —
not because they share code: A is a package script, B is a Weaver engine key, C is dispatcher plumbing
in Loom + Weaver + pkgmgr. Each is its own increment with its own green check.

### 1. Scope sentence (verbatim, per row)

- **A — [identity-domain] `TombstoneOrphanedCredentialIndex`'s outbound arm leaves a phantom entry in
  the LIVE owner's `credentialBinding` array.** *"The op retires the index vertex only. In the outbound
  shape the owner is alive, and their sign-in-methods array still lists the erased credential. Rewrite
  precedent: `unbind_identity_credentials.go`'s `owner_binding_rewrite`."*
- **B — [Weaver] A `surface` gap's Health issue carries no entity segment.** *"`issueKeyGap` keys per
  `(target, column)`, so with two erasures in flight the subject whose halves land first clears the
  issue raised for the stuck one. Wrong per-subject."*
- **C — [Loom/Weaver] A dispatcher cannot declare its op's class-(e) enumerations.** *"A `kv.Links` walk
  is declared through `ContextHint.Enumerations`, expressible by neither Loom `systemOp` submit nor
  Weaver `directOp` (`GapActionSpec` has no field)."* Consumers: `identityErasure`,
  `identityErasureComplete`.

**Green bar.** `go build ./...` · `make vet` · `golangci-lint run ./...` ·
`STRICT=1 go run ./scripts/lint-conventions.go` · `go run scripts/lint-board.go` ·
`go test ./packages/identity-domain/... ./packages/privacy-base/... ./internal/weaver/...
./internal/loom/... ./internal/pkgmgr/... ./internal/processor/...` with `POSTGRES_TEST_DSN` set
(REMOTE.md §3 — without it the Postgres-gated tests skip silently and the suite is falsely green).

### 2. Verified touch-list (`file:line`, checked live this fire)

**A — owner-array rewrite**

| File | What |
|---|---|
| `packages/identity-domain/tombstone_orphaned_credential_index.go:381` | the single `tombstone` mutation gains a sibling; the outbound arm rewrites the live owner's array |
| `…:132-134` · `:407-427` | `contextHint` — the owner's `.credentialBinding` becomes a declared read |
| `…:130` | `OutputSchema` `primaryKey` stays `index_key` (still in the write footprint — `internal/processor/commit_path.go:389-401`) |
| `packages/identity-domain/unbind_identity_credentials.go:279-339` | **the precedent, copied**: `owner_binding_rewrite` |
| `packages/identity-domain/ddls.go:423-449` | the `credentialBinding` aspect shape (`actorKey`/`boundAt` singular + `credentials[]`) |
| `packages/identity-domain/tombstone_orphaned_credential_index_test.go:337-363` | `OutboundResidue_Tombstoned` — today asserts the live owner's vertex is UNTOUCHED; that assertion inverts |
| `packages/identity-domain/package.go:33` · `manifest.yaml:2` | version bump (`scripts/lint-package-version.go`) |

**B — entity-segmented gap issue key**

| File | What |
|---|---|
| `internal/weaver/evaluator.go:1152` | `issueKeyGap(targetID, col)` — the key shape |
| `internal/weaver/evaluator.go:146-219` | `dispatchGap`; `entityID` is already a parameter and in scope at the raise (`:216`) |
| `internal/weaver/evaluator.go:749` | `clearClosedMarks` — the level-reconciled clear; must produce the identical key |
| `internal/weaver/reconciler.go:763` | `collapseReclaim` — the mark-close clear; same |
| `internal/weaver/evaluator.go:193,200,452,468,482,491,1039,1043` | the other eight call sites |
| `docs/observability/health-kv-schema.md:842-867` | the Weaver heartbeat section — a Health-emission change updates the schema doc in the SAME commit (SKILL §4) |

**C — dispatcher enumerations**

| File | What |
|---|---|
| `internal/processor/opwire/opwire.go:66-71` · `:112-116` | `ContextHint.Enumerations` + `EnumerationHint` — **already exist**; three construction sites, all operator tooling (`cmd/lattice/candidates/candidates.go:240`, `scripts/verify-erasure-ceremony.go:375,421`), none in a dispatcher |
| `internal/loom/pattern.go:51-62` | `Step`'s systemOp-only `Reads`/`OptionalReads` — gains `Enumerations` |
| `internal/loom/state.go:89-115` | `outboxRecord` — same |
| `internal/loom/actuator.go:38-42` · `:134` | the local `contextHint` mirror + `buildOutbox`'s signature |
| `internal/loom/engine.go:895` | the `buildOutbox` call |
| `internal/pkgmgr/definition.go:392-450` | `GapActionSpec` — gains `Enumerations` beside `Reads` (`:415`) |
| `internal/weaver/actuator.go:42-45` · `:82` | the local `contextHint` mirror + `submit`'s signature |
| `packages/privacy-base/patterns.go:153-162` | `identityErasure` steps 3+4 declare their walks |
| `packages/privacy-base/targets.go:82-89` · `:125-136` | `identityErasureComplete` gaps 1+2 declare theirs; the comment at `:82-89` that says this is inexpressible becomes false and is struck |

**Contract status — re-verified, and it is the finding that makes C buildable.**
`docs/contracts/02-operation-envelope.md:37` and `:211` **already define** `contextHint.enumerations`
as `{hub, relation, direction}[]`, and `opwire` already parses and shape-validates it. C adds no
contract text: `Step` and `GapActionSpec` are dispatcher input types, not contract shapes. **C is L2 —
not a frozen-contract proposal.** The gap is that nothing has ever *populated* the field.

**Citations that rotted.** The board row for B cites `evaluator.go:1068`; `issueKeyGap` is at
`evaluator.go:1152` today. The design's inc-7 residual 3 is otherwise accurate.

### 3. Precedents to mirror

- **A** → `unbind_identity_credentials.go:279-339` (`owner_binding_rewrite`), verbatim in shape: read the
  owner's `.piiKey` FIRST and return `None` on `shredded` (the array is already unreadable), then read
  `.credentialBinding`, filter the entry, promote-or-omit the singular `actorKey`/`boundAt` pair, and
  emit an `update` mutation. Its sibling `sweep_outbound` (`:255-277`) is the caller shape.
- **B** → `issueKeyEffect` (`evaluator.go:1157-1159`) is the house precedent for a THREE-segment issue
  key; `issueKeyOscillation` (`control.go:317-322`) for a multi-entity one. Not greenfield.
- **C** → `Reads`/`OptionalReads` are threaded end-to-end through both dispatchers already; `Enumerations`
  follows the identical path at every hop. The wire type it lands in (`opwire.EnumerationHint`) is shipped.

### 4. Increment order + runnable green checks

- **C1** — `Enumerations` through Loom (`pattern.go` → `state.go` → `actuator.go` → `engine.go`) and
  Weaver (`definition.go` → `actuator.go`). *Check:* `go build ./... && go test ./internal/loom/...
  ./internal/weaver/... ./internal/pkgmgr/...`
- **C2** — the two packages declare their five walks; strike the falsified `targets.go:82-89` comment.
  *Check:* `go test ./packages/privacy-base/... ./packages/identity-domain/... &&
  STRICT=1 go run ./scripts/lint-conventions.go`
- **B1** — entity-segment the gap issue key, raise and clear in lockstep; update the Health-KV schema doc.
  *Check:* `go test ./internal/weaver/... -run 'Gap|Surface|Mark'`
- **A1** — the outbound arm's owner-array rewrite + the inverted test assertion + version bump.
  *Check:* `go test ./packages/identity-domain/... -run Tombstone`
- **Close** — full green bar above, then the cumulative adversarial pass over the batch's whole diff.

### 5. In-scope gotchas

**This fire's own obligations.** A bumps `packages/identity-domain`'s version (`lint-package-version.go`).
B is a Health emission → `docs/observability/health-kv-schema.md` moves in the same commit (SKILL §4).
C adds a field to a persisted `outboxRecord` — `omitempty`, absent on every existing record, so old
records decode unchanged; state this rather than assume it. `packages/identity-domain` is a known
ROTATING-membership flake under parallel load (increment-9 build note): **one** re-run of a single
reddened test before concluding regression; never loosen an assertion.

**Standing checklist** (`agents/fire-brief-template.md`) — all six apply; #4 and #5 bind hardest here:
1. New state needs a LIFETIME, not a data structure.
2. Every census is a premise — re-run it live.
3. A negative test needs its positive vector proven first; prove each fix by reverting it.
4. **Removal needs a transport AND an observer; a demoted mechanism needs EVERY obligation enumerated.**
   B is exactly this shape: the issue key is a latch with a raise site and *two* clear sites, and a key
   changed at the raise but not at a clear leaks a permanently-stuck issue.
5. **One deterministic key, one writer.**
6. Precedent may carry debt — verify the mirrored pattern against the rule it claims to follow.

**Dossiers, copied in verbatim.**

`docs/components/weaver.md` (B, C1):
- *An `error`-severity Health issue must not fire on a self-healing condition* — an unreplayed pattern is
  replay lag, not a package bug, and the sweep reaches that branch on every restart.
- *A test that hand-seeds an engine's internal registry map pins the FALLBACK, not its name* — every sweep
  test seeded `patternMeta` bare, so each silently became a proof about unindexed patterns.
- *A restated cross-package constant needs a test that pins it* — Weaver may not import `internal/loom`,
  so step-kind strings are hand-copied and nothing failed if they drifted. **Directly live for C1**: the
  two `contextHint` mirrors in `loom/actuator.go` and `weaver/actuator.go` are hand-copies of
  `opwire.ContextHint`, and this fire adds a field to all three.
- *Classify by whitelist, not blacklist, when the vocabulary can grow.*
- *A gap class is decided by the dispatch's SHAPE, never by its action name.*

`docs/components/pkgmgr.md` (C1):
- *An injected dependency held in a nil-able field silently disables the gate it feeds* — second sighting
  in the thinner shape: the RULE was covered twelve ways and the line DELIVERING it was covered zero.
  **Live for C2**: a declared `Enumerations` that never reaches the envelope is precisely this shape.
- *canonicalName and the instance key segment are different namespaces.*

`docs/components/_packages.md` (A, C2):
- *A shared-vertex repoint needs a content-and-revision gate against EVERY other writer of that vertex,
  not just atomicity within its own batch.* **Live for A**: the owner's `.credentialBinding` has other
  writers (`CompleteCredentialLink`, `UnlinkCredential`, `MergeIdentity`).
- *A cross-package type guard must survive the migration window in BOTH directions.*
- *Census the CHECK, not the wrapper.*

`docs/components/processor.md` (C):
- *A read disposition the CLIENT declares is not a server policy* — the Processor validates
  `enumerations`' shape at parse and otherwise ignores it (`opwire.go:109-110`). C therefore buys
  **declaration completeness**, not enforcement; the brief must not claim otherwise.

### 6. Adjacent finds

- **[identity-domain] the link-less `credentialindex` row is two-thirds stale — corrected, not deferred.**
  Its premise *"residue nothing can walk"* is false: the operator CLI enumerates the class today by
  keyspace prefix (`cmd/lattice/identity/credential_residue.go:209`, `reconcile.go:153`, over
  `vtx.credentialindex.`), which is how `TombstoneOrphanedCredentialIndex` reaches it at all. And
  *"the attestation does not [name the class]"* is false as prose: the seal's own DDL text names the
  omission explicitly (`packages/privacy-base/seal_identity_for_erasure_complete.go:131-135`). What is
  genuinely open is what §9.2(i) says is open and says this design does not close: **no lens and no
  in-Starlark walk can reach the class, because no link survives to enumerate from** — so the residue
  lens cannot count it and the attestation's `coverage` cannot include it. Closing that structurally
  needs a `credentialindex → owner` link type plus a backfill of the live corpus, which §9.2(i) routes
  to the credential-binding design's §9 alternative. **Filed under out (2), designer pass**, with the
  premise corrected on the row: `📐 needs designer pass · no-pattern: a link making credentialindex
  owner-reachable`.
- **Enumerations is declared metadata, not enforcement.** After C, a script could still run an
  *undeclared* `kv.Links` walk and the Processor would not refuse it; only `lint-conventions`' source
  annotation catches that, and only in `packages/`. Not filed as a row — the enforcement point is the
  descriptor-pinned disposition Contract #2 §2.5 already names, and no consumer wants it today.
  Recorded here so a later reader does not read C as having closed enforcement.

### 7. Non-goals (the drift fence)

~~Not touched: the `unroutedTasks` aggregate gap's key shape (it is correctly aggregate — one issue per
target, not per entity — and B must leave its behavior identical)~~ **— struck, the premise was false
(2026-08-23, from B's build).** `unroutedTasks` is **per-task-row, not aggregate**: its lens sets
`KeyColumn: "entityId"` and anchors on the task (`packages/orchestration-base/lenses.go:66-80`), so
`BuildKey` emits one weaver-targets row per unrouted task. It therefore carried the identical collision
this item fixes — N unrouted tasks shared one latch, the `message` named whichever row was processed
last, `since` belonged to the first, and one task being claimed cleared the issue for all the rest.
Entity-segmentation fixes it rather than breaking it, and Contract #10's *"**a** task left unclaimed …
rolls **a** `UnroutedTasks` entry"* is only now true per task. The non-goal was written from the
gap's *action* (`surface`, shared with the erasure gaps) instead of its lens shape; B was told to
verify it rather than honour it, which is the only reason it did not ship as written.

Still not touched: enforcement of declared enumerations in the Processor; the `credentialindex`
reachability link (routed above); any change to `SealIdentityForErasureComplete`'s attestation shape;
the read-posture warn→block flip itself.

### Scope-diff gate — discharged

Every touch in part 2 traces to one of the three scope sentences in part 1; nothing widened, no adjacent
mechanism substituted. Dependencies re-verified both ways: C's *stated* dependency on a Contract #2
change is **refuted** — the contract already carries `enumerations` (`02-operation-envelope.md:37,211`)
— so C drops from L3-propose to L2 and no unlisted dependency appeared. A's stated precedent is live at
`unbind_identity_credentials.go:279-339`. B's cited line rotted (`1068` → `1152`) but the mechanism is
as described.

**The premise census that mattered was stated wrong, and the correction is recorded here rather than
quietly dropped (2026-08-23, from the increment-C cold review).** This gate claimed *"zero construction
sites for `ContextHint.Enumerations`, re-run live"*. There are **three** — `cmd/lattice/candidates/
candidates.go:240` (MergeIdentity's `assignedTo`/`indexes` walks, carrying a full read-posture comment)
and `scripts/verify-erasure-ceremony.go:375,421`. The count came from a Phase-0 scout and was folded in
without the lead re-running it, which is the standing checklist's item 2 (*"every census is a premise —
re-run any stated count live"*) failing on the one number this fire's scope argument rested on.

What survives the correction is the load-bearing claim, now stated exactly: **no DISPATCHER had ever
populated the field.** All three sites are operator tooling hand-rolling an envelope, which is why the
walks were undeclarable through Loom and Weaver and why the mechanism was still worth building.
`candidates.go:238-244` should have been cited as a live precedent — it uses a resolved literal hub and
corroborates the `out`/`in` semantics independently of the two engines' doc comments.
