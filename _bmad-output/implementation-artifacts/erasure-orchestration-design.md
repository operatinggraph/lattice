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
| 4 | `PurgeIdentityDedupFootprint` | privacy-base | ≤ `3·PAGE` | yes — same | none |

`PAGE = 256`, matching the existing enumeration page limits (`shred_identity_key.go:178`, `:204`,
`:232`). **No step can exceed its bound**, so no step can reintroduce a refusal (§10).

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
  false AS inflight_credentialResidue,
  false AS inflight_dedupResidue,
  ... AS violating
```

Shape grounded in `packages/objects-base/lenses.go:99-113` (the shipped `OPTIONAL MATCH` +
`count(x.key)` residue idiom, #37) and `packages/lease-signing/lenses.go:646-647` (multi-count
aggregate rows). `= null` / `<> null`, never `IS NULL` — the engine's convention
(`objects-base/lenses.go:95-96`). Gap columns are `missing_<gap>` as enforced (#27).

`duplicateOf` residue folds into `missing_dedupResidue` and is swept by the same op; it gets no
separate count only because `duplicateOf` and `indexes` are cleared by one op and a second count would
buy nothing. *(If the engine cannot carry three `OPTIONAL MATCH` fan-outs in one row without a
cartesian blow-up — the `count(DISTINCT CASE WHEN …)` idiom at `lease-signing/lenses.go:646` exists
precisely for that — the build collapses them into that form. Flagged, not assumed.)*

### 7.2 What the Weaver does

A `WeaverTargetSpec` in `privacy-base`, `TargetID: "identityErasureComplete"`, `LensRef:
"identityErasureResidue"`:

| Gap | Action | Effect |
|---|---|---|
| `missing_credentialResidue` | `directOp UnbindIdentityCredentials{subjectKey: row.entityKey}` | next page; count strictly decreases |
| `missing_dedupResidue` | `directOp PurgeIdentityDedupFootprint{subjectKey: row.entityKey}` | next page; count strictly decreases |
| `missing_vaultDestruction` | `surface` (issueCode `erasure.vaultKeyNotDestroyed`, severity `critical`) | a stuck async half is **escalated, not swept** — the Vault destruction has exactly one correct actor |
| `missing_projectionNullify` | `surface` (issueCode `erasure.projectionsNotNullified`, `critical`) | ditto |
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
2. **Every step op is bounded by a page**, not by the subject's connectivity: ≤ `2·PAGE + 1` (step 3)
   and ≤ `3·PAGE` (step 4), with `PAGE = 256` (§5.4). Both are far under the 999 platform ceiling with
   room to spare, and — the load-bearing part — **the bound does not depend on any input**. A person
   with 10,000 credentials produces 20 identical bounded commits, not one refusal.
3. **The tail is covered by convergence, not by a bigger commit.** Each pass strictly decreases the
   residue (§7.2); §6 stops it being replenished; the uncapped budget (#28) keeps dispatching until
   zero.
4. **No step can reintroduce a refusal.** This is a checkable property, and Inc 5's test strategy
   makes it one: *no op reachable from the erasure pattern or the erasure target may compute a
   mutation count from an unbounded enumeration.* Each sweep enumerates **exactly one page** —
   `kv.Links` with a single call and no page loop — so the `MAX_*_PAGES` construct that produces the
   fanout failures does not exist in the new ops at all. The cursor lives in the *world* (the
   remaining live links), not in the script.

**Net: there is no input for which the platform can decline to erase a person.**

---

## 11. Risks

| # | Risk | Mitigation |
|---|---|---|
| **R1** | **First Weaver-driven paged sweep in the codebase** (#40). No precedent for a `directOp` that processes N rows and expects re-dispatch. If the uncapped-budget reading of `evaluator.go:875-881` is wrong in practice, sweeps stall at 3 passes. | Inc 4 proves it with an integration test at `N > 3·PAGE` before any other convergence work lands. Fallback is a lens-computed `maxretries_erasureresidue = ceil(N/PAGE) + 2` — grounded, since a row-declared cap always wins (#28) — at the cost of a baroque column. |
| **R2** | **`Scope:"any"` grants on erasure verbs** (§5.3). Three new ops that tombstone credentials, granted to service actors. | No `OpMeta` descriptor, no person-facing affordance, no operator grant. Submitters are `identity.system.loom` and `identity.system.weaver` only. `make verify-package-*` on both packages; the grant matrix diff is a review gate in its own right. |
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
  `main` **UNCOMMITTED** as the proposal; the code ships around it.
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

**Contract edit prepared, UNCOMMITTED, for Andrew:** `docs/contracts/10-orchestration-loom.md` §10.5 step
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
