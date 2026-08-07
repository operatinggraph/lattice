# Erasure as an orchestrated process — the Loom spine and the Weaver's convergent tail

**Status: 📐 DRAFT awaiting Andrew — authored 2026-08-06 (Andrew's redirect of credential-binding-plane-lifecycle-design.md Inc 1: erasure is an orchestrated process, not a fatter op)**

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

| Writer | File | Gate |
|---|---|---|
| `ClaimIdentity` | `identity-domain/ddls.go:1142-1257` | reject `ErasedIdentity` |
| `CompleteCredentialLink` | `:1438-1439` | reject `ErasedIdentity` |
| `ReconcileCredentialBinding` | `:1578-1666` | reject — it already refuses a tombstoned index (`:1618-1619`); this is the same judgement one step earlier |
| `index_vertex_mutation` callers | `:582-595` | **do not revive** for an erasure-requested identity |
| `MergeIdentity` | `identity-hygiene/ddls.go` | reject an erasure-requested identity on either side |

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

## 12. Increments

Fewer, larger fires with a declared internal build order. Sizes are honest.

**Inc 1 — Narrow the op. Size S.**
`ShredIdentityKey` loses the three enumerations, all four refusal modes, and the mutation-count
pre-flight. One mutation, one event. Update the DDL description and the stale in-commit rationale
(#11). Update `packages/identity-domain/ddls.go:1605-1612`'s comment, which asserts the shred
tombstones only the link (the credential-binding design already noted it expires).
**Ships alone and is immediately correct**: the erasure becomes *less* complete than today but *cannot
refuse*, and nothing regresses that was ever guaranteed. Depends on nothing.

**Inc 2 — `StepSpec.Reads` in Loom. Size S–M. (Skipped if Andrew picks §5.2 option A.)**
`pkgmgr.StepSpec` + `loomPatternSpecBody` + `loom.Step` + `pattern.go` validate + `submitSystemOp`
resolution. Mirrors the shipped `userTaskReads` resolution. **Also confirms §5.3's unverified
completion-correlation detail** with a systemOp step whose op emits a domain event — that answer sizes
Inc 4. Platform work, no privacy surface.

**Inc 3 — Close the write path, and the unbind primitive. Size M.**
`SealIdentityForErasure` (privacy-base) writing `.erasureRequested`; five fail-closed gates across
identity-domain and identity-hygiene (§6); `UnbindIdentityCredentials` (identity-domain, §5.4) with its
`Scope:"any"` grant. **The largest correctness increment**, and the one that makes §7's convergence
mean anything. Depends on Inc 1.

**Inc 4 — The pattern and the convergence. Size M–L.**
`PurgeIdentityDedupFootprint`; the `identityErasureResidue` lens + `privacy-erasure` bucket; the
`identityErasureComplete` weaverTarget; the `identityErasure` Loom pattern; the two `surface` gaps;
`SealIdentityForErasureComplete` with in-commit re-verification. R1's `N > 3·PAGE` proof lands here,
first. Depends on Inc 2 + Inc 3.

**Inc 5 — Operator surface and the standing gate. Size S.**
A Loupe pane over `privacy-erasure` (P5: reads the bucket) showing per-identity residue and seal
state; a `lint-conventions` rule for §10 point 4 — no op reachable from the erasure pattern or target
may page an enumeration. Depends on Inc 4.

**Not an increment: option C** (§9.2), behind precondition (i).

---

## 13. Test strategy

- **Inc 1** — `shred_identity_key_test.go`: mutation count is exactly 1 for an identity with 0, 1 and
  500 links; no `ShredBatchTooLarge` path remains reachable (assert the symbol is gone, not that it
  does not fire). Re-shred still resets the cycle (#5) and still writes the placeholder for a
  never-sensitive identity (`:299-306` — the restart-safety argument must survive the narrowing, and a
  test should say so).
- **Inc 2** — `internal/loom`: a systemOp step with declared reads produces an outbox record carrying
  the resolved `ContextHint.Reads`; a `subject.<aspect>` template resolves against `inst.SubjectKey`;
  pattern load rejects a `Reads` entry on a `userTask`/`externalTask` step. Plus the §5.3 correlation
  probe.
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
