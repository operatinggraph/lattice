# NFR-S6 — remove the timing difference where it is made, and delete what then has no reason to exist

> **✅ RATIFIED — Andrew, 2026-08-27. Build-ready.** — *Winston (Designer fire, 2026-08-27. Third framing: the
> payload-cap draft was rejected by Andrew in favour of deletion; an adversarial grounding pass then
> falsified two load-bearing claims in the deletion draft. This version is what survives both.)*
>
> **Read this first — the headline changed, downward.** The previous version promised "delete 277
> lines, no new mechanism." That is **not available**, and the reason is worth more than the promise
> was:
>
> - **The file splits, it does not delete.** Four identifiers in `claim_reply_floor.go` are
>   *wire-shape*, not timing — `nfrS6Operations`, `isNFRS6Operation`, `claimRejectionMessage`,
>   `claimOutcomeInternalFault` — and they have surviving consumers, including one outside the reply
>   path entirely (`step4_hydrate.go:227`). **~190 of the 277 lines are deletable; the op-name map is
>   not.** Andrew's "de-hardcode" ask is not reachable by this route either, and §5 F says why that
>   is correct rather than a shortfall.
> - **One of the two engine gaps is not equalizable.** Absent-vs-claimed (~0.27 ms) is rooted in the
>   substrate: a `multi_last` response carries one message per **matched** subject, so an absent
>   target means four fewer messages, parses and decrypt entries. Equalizing it would need the engine
>   to fabricate a synthetic snapshot per operation — *more* per-operation coupling than the one-line
>   predicate the deletion was meant to remove.
>
> **What survived, and it is the better half.** The dominant gap is claimed-vs-wrong-key (~0.36 ms),
> and it **is** cleanly equalizable in the engine with no synthetic anything — see §4.2, and note it
> **withdraws the data-sensitivity decision the last version put to you.** You do not need to
> declassify `.claimKey`.
>
> **Andrew ratified the direction on 2026-08-27: the deletion is not optional, and the measurement is
> skipped.** So the shape is three moves, all unconditional:
>
> | | Move | Layer | Removes |
> |---|---|---|---|
> | 1 | Decrypt-and-discard on `decryptSensitiveDoc`'s `IsDeleted` arm, served from the step-4 snapshot | engine, ~10 lines | the ~0.36 ms dominant gap |
> | 2 | Equalize the script's 18 + 15 early returns | package, existing builtins | the script-half gaps |
> | 3 | **Delete the release quantum** (~190 lines) | engine, subtractive | 50 ms/rejection, 3 metrics, a goroutine-per-reply, a 1024-deferral bound, a shutdown drain |
>
> **What that knowingly accepts, recorded once so it is not lost** (§6.3): the absent-vs-claimed gap
> (~0.27 ms) is substrate-rooted and survives. It is a *statistical* channel — it needs averaging over
> many samples — against a **confirmation** oracle, not an enumeration one: the attacker must already
> hold a `vtx.identity.<NanoID>`, and that keyspace is ~2¹¹⁷ (58-char alphabet, 20 chars), so nothing
> here is brute-forceable. §8 states the threat model the whole item is priced against. The
> deterministic single-request oracle — the wire code — stays closed by Contract #9 §9.3, which is
> untouched.
>
> **Also newly known and unpriced anywhere:** `CompleteCredentialLink` has **never been measured**,
> and its profile is worse than `ClaimIdentity`'s — its descriptor declares a second sensitive aspect,
> so a claimed target pays **two** `readPiiKeyEnvelope` round trips where an unclaimed one pays zero
> (§6.1). And there is a **fourth** timing class nobody has counted: a shredded-key envelope makes
> `decryptSensitiveDoc` error mid-way, target-state-dependently, escaping as `InternalError` (§6.2).

**Board row:** `[Processor] The NFR-S6 tail` — ★★, M.
**Andrew, 2026-08-25:** *"de-hardcode by SIMPLIFYING — net reduction in lines, no new machinery."*
**Andrew, 2026-08-27:** *"Simplify, do not add new mechanisms, delete machinery that doesn't need to
exist."*
**The lint half of the original row is filed separately** — `[Tooling] internal/ hardcodes
package-owned operation names, undeclared`.

---

## 1. What NFR-S6 protects, and what the machinery is

`ClaimIdentity` redeems a one-time secret against an unclaimed identity. It is `scope: self` and
nothing rate-limits it, so anyone can probe it. Three failure causes are three different facts about
the graph — *no such identity*, *exists but already claimed*, *exists, unclaimed, wrong key* — and
leaking which one you hit is an enumeration oracle. Contract #9 §9.3 closes the wire channel: every
failure answers `ClaimKeyInvalid`, no details, specifics to Health KV only.

Timing is the channel the wire shape cannot close. Measured at n=3000
(`auth-plane-projection-latency-design.md` §19.9): the three causes separate by **0.27–0.70 ms**,
monotone, recoverable by averaging. `624d445` answered it by **masking** — `claimReplyFloor` holds
each rejection and publishes at `receipt + ceil(elapsed/Q)·Q`, `Q = 50 ms`.

That mask is what this design removes. It is 277 lines that exist to hide a difference, and the
difference is fixable where it is made.

---

## 2. Where the difference is actually made

**Overwhelmingly in the package's own Starlark script.** `packages/identity-domain/ddls.go`'s
`ClaimIdentity` branch is a cascade of early returns: **18** `fail_claim(...)` calls in strict order
(`:1447-1523`), and `CompleteCredentialLink` has **15** more (`:1700-1800`). An absent target exits at
the fourth; an already-claimed identity exits at the ninth; a wrong key runs to the eighteenth,
paying the `crypto.sha256` and the `crypto.constant_time_equal` on the way. Each early exit is
strictly less work than the next.

**The residue is engine-side, and it is two items:**

| # | Divergence | Where | Size |
|---|---|---|---|
| R1 | Hydrate response bodies — absent keys return nothing, present keys return documents | `step4_hydrate.go`'s batched `KVGetMulti` | Small. The **same three keys** are requested every time: the descriptor declares exactly `{payload.targetIdentityKey}`, `.state`, `.claimKey` as `OptionalReads` (`packages/identity-domain/opmetas.go:283-287`), and `c69aa4a4` closed the set. One request either way; only the response differs. |
| R2 | **`decryptSensitiveDoc`'s `IsDeleted` arm** skips `ciphertextFromData`, `vault.KeyHolder`, **`readPiiKeyEnvelope`**, `Decrypt`, the unmarshal and `markPlaintext`, for an already-claimed identity's tombstoned `.claimKey` | `sensitive_decrypt.go:154-171`, `:255-271` | **Dominant (~0.36 ms).** **Correction to the previous version: `Vault.Decrypt` is NOT the cost.** The Processor runs the in-process `*vault.LocalBackend` (`cmd/processor/main.go:395-412`); `vault.NewService` only *hosts* the RPC for other components, so a decrypt is a cached-DEK AES-GCM open — microseconds. The network hop is **`readPiiKeyEnvelope`'s `conn.KVGet`** (`sensitive_decrypt.go:307-308`). |

R2 is the one that matters and it is **not** fixable in the script: the script never sees it, because
it happens during hydration before the script runs. **R1 is not fixable anywhere** — see §6.3.

A free reduction rides along with R2's fix: `readPiiKeyEnvelope` re-reads `<target>.piiKey` live even
though the package's `derive_reads` already put that key in the step-4 snapshot
(`packages/identity-domain/ddls.go:1052`). The function's own doc calls it *"internal Processor
bookkeeping — never declared in a script's contextHint.reads"*, which is true of the *script's*
declaration and not of the snapshot's contents. Serving it from the snapshot removes the round trip
from **both** arms rather than adding it to one.

---

## 3. Grounding ledger

| # | Fact | Citation |
|---|---|---|
| 1 | The masking machinery: `Q = 50 ms`, `releaseAt = receipt + ceil(elapsed/Q)·Q`, goroutine-plus-timer per reply, 1024-deferral bound that **drops** on overflow, shutdown drain | `claim_reply_floor.go:33`, `:164-176`, `:224-238`, `:88`, `:203-223`, `:251-270` |
| 2 | 277 lines, referenced from `cmd/processor/main.go`, `commit_path.go`, `health.go`, `internal/testutil/pipeline.go` | `wc -l`; census C1 |
| 3 | The measured oracle: n=3000, OFF → monotone, 0.27–0.70 ms; ON → every CI includes zero, p50 spread 10.6 µs | `auth-plane-projection-latency-design.md:2510-2519` |
| 4 | **18** `fail_claim` early returns in `ClaimIdentity`; **15** in `CompleteCredentialLink` | `packages/identity-domain/ddls.go:1447-1523`, `:1700-1800`; census C2 |
| 5 | `crypto.sha256` and `crypto.constant_time_equal` are **already** sandbox builtins — the equalization needs no new primitive | `internal/processor/starlark_builtins.go:101-103` |
| 6 | The declared read set is three `{payload.*}` `OptionalReads`, identical for every cause | `packages/identity-domain/opmetas.go:283-287` |
| 7 | `.claimKey` is declared **sensitive** — encrypted at rest, hydration decrypts it | `packages/identity-domain/ddls.go:73`, `:394` |
| 8 | A tombstoned sensitive aspect **must not** be decrypted, and the rule is deliberate: *"the tombstone RETAINS the aspect's body … the deletion flag is the only thing standing between a dead aspect and a live decrypt"* | `sensitive_decrypt.go:158-171` |
| 9 | Vault is a NATS micro service — `Decrypt` is an RPC, not in-process | `internal/vault/service.go:208`, `:257` |
| 10 | The claim secret is 32 bytes of `crypto/rand`, hex-encoded; `.claimKey` stores its sha256 | `cmd/lattice/identity/identity.go:27-35`; Contract #9 §9.4 |
| 11 | The script already emits one uniform code — `fail("ClaimKeyInvalid: " + outcome)` — and the Processor reclassifies every such message | `ddls.go:1448`, `:1730`; `commit_path.go:1051-1076` |
| 12 | `classifyStepError` already strips details from **any** `ClaimKeyInvalid`, for **every** operation, NFR-S6 or not | `commit_path.go:1061-1063` |
| 13 | The membership map gates **four** behaviours across three call sites; the wire collapse and the quantized release share one `if` | `step4_hydrate.go:227`, `commit_path.go:1032`, `:1125-1138` |
| 14 | The map's *"EVERY rejection"* claim is literally false: step-1 malformed, step-2 duplicate, step-3 authorizer error and step-3 denial all answer un-collapsed and unquantized | `commit_path.go:241`, `:265`, `:275`, `:294` |
| 15 | `Deps.ClaimRejectionFloor` accepts a **negative** value to disable the quantizer — the posture the acceptance measurement needs | `commit_path.go:58-67` |
| 16 | Contract #9 §9.3 promises the wire-shape collapse only — no timing, no quantum, no payload bound | `docs/contracts/09-identity-claim-flow.md:58-59` |

---

## 4. The shape — three moves

### 4.1 Equalize the script (package work, no new primitive)

Replace the 18 + 15 early returns with one shape: **accumulate an outcome, never return early, fail
once at the end.** Every path then executes the same instructions:

- read all three hydrated documents unconditionally;
- record the first failing condition in a variable instead of calling `fail_claim`;
- **always** compute `crypto.sha256(claim_key_plaintext)` — against a fixed-length placeholder when
  the payload's key is missing or malformed;
- **always** call `crypto.constant_time_equal(submitted, stored)` — against a fixed dummy hash when
  no `.claimKey` document was hydrated;
- fail once, at the bottom, with the accumulated outcome word (which only Health KV ever sees).

Both builtins already exist (ledger row 5). This is a rewrite of one branch of one package script, and
it is where the difference is made, so it is where it should be removed.

**What it does not fix:** R1 and R2, which happen before the script runs.

### 4.2 Equalize R2 in the engine — decrypt and discard

**The previous version recommended declassifying `.claimKey`. That is withdrawn — it is unnecessary.**

The tombstone **retains the real ciphertext**: `sensitive_decrypt.go:158-172` scrubs `doc.Data` to
`{}` *after* the fact, and the rule it enforces is that a dead aspect must never **yield plaintext**,
not that it must never be decrypted. So the `IsDeleted` arm can perform the full decrypt and
**discard the result**, still scrubbing `Data` before the document is handed on. Nothing synthetic is
required, no data-sensitivity trade is required, and the confidentiality rule is honoured exactly as
written — what the caller receives is byte-identical to today.

Two obligations that ship with it, because "decrypt then throw away" is the kind of code a later
reader deletes as pointless:

- the discard is **stated in the code** as a timing-equalization requirement with a pointer to this
  design, not left as an unexplained call;
- a test asserts the plaintext never reaches `doc.Data`, `markPlaintext`, or an egress ref on the
  tombstoned path — i.e. that equalizing the *time* did not widen the *reach*.

Combined with the snapshot read above, R2 goes to zero and the engine's dominant gap is closed with
roughly ten lines and no new concept.

### 4.3 Delete the release quantum — a split, not a delete

With §4.1 and §4.2 in place the two largest gaps are closed and R1 is accepted (§6.3). The file
**splits**: ~190 of its 277 lines are timing machinery and go; the rest is wire-shape and survives
(§6.4).

- **`internal/processor/claim_reply_floor.go`** — the whole file, 277 lines: `claimReplyFloor`,
  `releaseAt`, `publishNoEarlierThan`, `Drain`, `DefaultClaimRejectionFloor`,
  `maxPendingDeferredReplies`, `claimFloorDropLogEvery`, `replyPublisher`.
- **`commit_path.go`** — `replyToNoEarlierThan`, `DrainClaimReplies`, the `receipt` parameter threaded
  through eleven rejection branches, and `Deps.ClaimRejectionFloor`.
- **`health.go`** — `ClaimFloorApplied`, `ClaimFloorLate`, `ClaimFloorDropped` and their three
  heartbeat fields.
- **`cmd/processor/main.go`** — the drain call and `claimReplyDrainBudget`.
- **`internal/testutil/pipeline.go`** — the floor wiring.
- **the `receipt` stamp** at `commit_path.go:222` and its thread through three signatures and
  fourteen call sites — it has no other consumer.

**What does NOT go: `nfrS6Operations` and `isNFRS6Operation`.** They keep two consumers that have
nothing to do with timing — the wire-shape collapse (`commit_path.go:1125`) and the closed
declared-read set (`step4_hydrate.go:227`). The edit at the collapse is one call:
`replyToNoEarlierThan(...)` → `replyTo(...)`. §6.4 and §6.5 carry the consequences.

**What survives, and why.** The **wire-shape collapse** is a Contract #9 §9.3 promise and stays. Two
of its three current supports are already universal, not membership-keyed: the script emits one code
itself (ledger row 11) and `classifyStepError` strips details from any `ClaimKeyInvalid` for every
operation (row 12). What the membership map still buys is the **step-4 fault** case — a hydrate or
decrypt failure before the script runs, which returns a bare error and classifies `InternalError`.
The map therefore survives, dropping from four consumers to two — which is most of what the original
row asked for, and §6.4 explains why the remainder is correct rather than a shortfall.

---

## 5. Alternatives considered

**A. Keep the mask; add a payload byte cap** — *the first draft's recommendation.* `env.Payload` is
decoded 2–4× inside the deferral window and unbounded below 1 MiB, so a caller can position the
quantum boundary. **Rejected**: it *adds* a mechanism (a cap, a constant, a refusal path, a lint rule)
to defend a mechanism that should not exist, adds a fourth consumer of the hardcoded map, and leaves
all 277 lines standing. It also carried a fork about where the cap could sit — and that fork exists
only because the window exists. Deleting the window deletes the fork.

**B. Release unconditionally at `receipt + Q`, drop past `Q`** — the triage's proposed fork.
**Rejected as false**: `P(drop)` is cause-dependent for exactly the reason `P(n=2)` was, so it renames
the channel rather than closing it — and it still needs the membership map.

**C. Do nothing.** **Rejected**: the two shipped code comments assert a closure the ratified design
explicitly declined to claim (*"a Bernoulli one, raising the cost by roughly two orders of magnitude
rather than removing it"*). Whatever else is decided, that correction is owed — and under this design
the comments are deleted with the code, which is the cleanest form of correcting them.

**D. Equalize the script only, keep the mask.** Removes the most numerous divergences and keeps 277
lines to hide the rest. **Rejected by Andrew on 2026-08-27** — the deletion is the point of the item,
and keeping a 277-line mask over a residue that needs averaging to exploit is the machinery-for-its-
own-sake this fire exists to remove.

**E. Make the *engine* constant-time generically** — pad every rejection to a fixed budget. **Rejected**:
that is the masking mechanism again, generalized, applied to every operation in the platform. Strictly
more machinery than exists today.

**F. Remove the collapse too and let rejections answer honestly.** **Rejected**: it is a frozen
Contract #9 §9.3 promise, and unlike the quantum it is genuinely load-bearing — the wire code is the
cheapest possible oracle.

---

## 6. What the adversarial pass falsified, and what it added

The previous version's two headline claims were wrong. Both corrections narrow the design, and both
are recorded here rather than quietly folded, because the next reader will otherwise inherit the
version I got wrong.

### 6.1 `CompleteCredentialLink` has never been measured, and its profile is worse

The n=3000 study covers `ClaimIdentity` only (`packages/identity-domain/claim_timing_probe_test.go`).
`CompleteCredentialLink`'s descriptor declares a **second** sensitive aspect,
`{target}.credentialBinding` (`opmetas.go:494-499`, `ddls.go:421-424`), and there is no envelope memo —
only `ddlResolutionMemo` is shared — so a *claimed* target pays **two** `readPiiKeyEnvelope` round
trips where an unclaimed one pays zero. Its cause profile is therefore different from the one that was
measured, and the mechanism has been covering it on the strength of the *other* operation's numbers.
§4.2's fix helps both. It is recorded here because the mechanism has been covering `CompleteCredentialLink` on the *other* operation's numbers, and after the deletion it is covered by §4.1/§4.2 on its own terms — which is a better basis than it ever had.

### 6.2 There is a fourth timing class, and equalizing three causes does not cover it

A **shredded** key envelope makes `checkAndDeriveDEK` return `ErrKeyShredded` mid-decrypt
(`internal/vault/local.go:399-401`), which escapes as a bare `fmt.Errorf` classified
`ErrCodeInternalError` (`commit_path.go:1071`). It is **target-state-dependent and reachable** — an
unclaimed identity whose key has been shredded has a live `.claimKey` ciphertext and a shredded
`.piiKey`, a population the identity-domain DDL's own comment says exists (`ddls.go:713-718`). This is
exactly the hole `claim_reply_floor.go:52-62` documents as the reason for keying on `operationType`
rather than on the error code, and **no amount of equalizing the three known causes closes it.** The
wire-shape collapse is what contains it — it answers `ClaimKeyInvalid` like everything else — which is
one more reason that half stays and a reason the map cannot be dissolved.

### 6.3 R1 is not equalizable, and trying would add coupling rather than remove it

An absent target means four fewer messages come back from `multi_last`
(`internal/substrate/kv_multi.go:338-360`), four fewer `parseVertexDoc` calls, three fewer
`decryptSensitiveDoc` entries. To equalize, the engine would have to fabricate a fixed-size snapshot
and a synthetic decrypt for keys that do not exist — which requires knowing, per operation, which
declared keys *would* have been sensitive. **That is more per-operation coupling than the one-line
predicate the deletion set out to remove**, and it argues Contract #9 §9.4's genericity invariant in
the wrong direction.

**So R1 is accepted, by decision rather than by analysis.** Priced honestly: it is ~0.27 ms of bias
under a ~17 ms loaded p99, so exploiting it means averaging many samples; and what it confirms is
whether an identity key the attacker **already holds** exists and is unclaimed (§8). The
alternative on the table was keeping 277 lines and 50 ms per rejection to mask it, which Andrew
declined on 2026-08-27. Recorded here so a future reader finds the trade rather than assuming the
channel was closed.

### 6.4 The file splits; the op-name map survives

`nfrS6Operations`, `isNFRS6Operation`, `claimRejectionMessage` and `claimOutcomeInternalFault` are
wire-shape, consumed at `commit_path.go:1032,1033,1125,1137` and `step4_hydrate.go:227`. They move to
a wire-shape file; ~190 lines of timing machinery go. **Andrew's original "de-hardcode" ask is
therefore not satisfied by this route** — and that is the right answer, not a shortfall: §5 F and §3.4
show the collapse is a frozen Contract #9 §9.3 promise whose remaining engine-side support is the map.
What the item *can* deliver is the map dropping from four consumers to two.

### 6.5 The closed declared-read set loses its stated justification

`refuseUndeclaredContextHint` (`step4_hydrate.go:227`, `descriptor_floor.go:419-509`) is justified
*entirely* by the release quantum: an unbounded declared set lets a caller price the work inside the
window (`descriptor_floor.go:429-437`). Delete the quantum and that reason evaporates — but the
refusal must **not** be deleted with it, because it is also what stops a caller padding an *equalized*
path back into inequality. **It needs an explicit re-derivation in the same increment**, not a
deletion and not a silent survival on a dead rationale. This is the guard-justification-unreachable
class, found in my own design by a reviewer rather than by me.

## 7. Reconciliation with the existing mental model

**"Didn't `624d445` decide this?"** It closed the oracle, correctly, with the tool available at the
time. Its own §19.10 recorded that quantization *"removes the escape branch but not the boundary."*
This design removes the boundary by removing the reason for it. That is not a reversal of `624d445`;
it is the version that did not need a deferral.

**"Is the script rewrite riskier than the mask?"** Different risk, and it is measurable in the same
harness. The mask's correctness rests on a timer; the equalized script's rests on both branches
executing the same instructions, which is a property a per-cause study tests directly — the same
n=3000 harness that validated the mask, which still ships (`packages/identity-domain/claim_timing_probe_test.go`) and defaults to floor-off — available to anyone who later wants the number, without gating this fire on it.

**"Does this introduce new state?"** No. It removes state: a goroutine per deferred reply, a pending
counter, a drop counter, a `WaitGroup`, three metrics, and a shutdown drain.

**"What about the payload lever?"** It exists only inside the deferral window. No window, no lever.
The board row's payload half closes as *dissolved*, not as *fixed*.

---

## 8. The threat model this is all priced against

Recorded because everything above and below is valued against it, and the previous versions of this
doc inherited *"NFR-S6 anti-enumeration"* as a settled premise without ever saying what it buys.

**It is a confirmation oracle, not an enumeration one.** An attacker must supply a
`targetIdentityKey` — a 20-character NanoID over a 58-character alphabet
(`internal/substrate/keys/nanoid.go:13`), so ~2¹¹⁷. The keyspace is not walkable. The oracle is only
useful over keys the attacker **already holds**: from a log, a URL, an export, or an insider's view
of a lens projection. What it then tells them is whether each key exists and whether it is still
*unclaimed* — i.e. which identities still have a live claim secret outstanding, worth phishing or
intercepting.

**Two channels, very different value per line:**

| | Wire code (Contract #9 §9.3) | Release timing |
|---|---|---|
| Oracle | **Deterministic — one probe, one bit** | Statistical — needs averaging over many samples |
| Cost to keep | ~14 lines: one `if`, a generic reply, a Warn log | 277 lines, a goroutine + timer per reply, a 1024 bound, a drop path, 3 metrics, a shutdown drain, 50 ms per rejection |

That asymmetry is the whole disposition. The collapse stays because it closes the strong channel for
almost nothing; the quantum goes because it closes the weak one for a great deal. §5 F's original
rejection of "delete the collapse too" cited the frozen contract, which is a statement about who
decides — this table is the actual argument, and it holds independently of the contract.

**No timing measurement gates this fire.** Andrew's call, 2026-08-27. The harness remains
(`packages/identity-domain/claim_timing_probe_test.go`, floor-off by default) for anyone who later
wants the residual number.

## 9. Decomposition for the Steward

**One increment, ungated.** Moves 1, 2 and 3 land together: splitting them would leave either a mask
with nothing to mask or an equalized script still paying 50 ms per rejection. §6.5's re-derivation of
the closed declared-read set ships in the same increment — it must not survive on the rationale the
deletion removes.

**Posture-changing — full review depth.** It removes a shipped security mechanism and changes an
aspect's sensitivity declaration.

**Package-version discipline:** `identity-domain`'s manifest version and its mirroring `Version`
constant must bump for both the script rewrite and the DDL flag change, or a running stack no-ops the
install (`DIFF_BASE=<base-sha> go run ./scripts/lint-package-version.go`).

---

## 10. Test strategy

| # | Proves | Shape |
|---|---|---|
| T1 | Every cause executes the same instructions | per-cause instruction/step-count assertion on the equalized script — not a timing test, a structural one |
| T2 | The sha256 and the constant-time compare run on **all** causes | including absent target and malformed payload, against the placeholder and dummy hash |
| T3 | No behavioural regression | every existing `ClaimIdentity` / `CompleteCredentialLink` outcome still produces its same Health-KV outcome word and the same generic wire reply |
| T4 | The equalized script still refuses everything it refused | one vector per accumulated outcome — 18 for claim, 15 for link |
| T6 | A `.claimKey` that is no longer sensitive still cannot be read by an unauthorized actor | the read path's own authorization is unchanged — pin it, because removing an encryption flag invites the assumption that it was the access control |
| T7 | The pre-step-4 paths are unaffected | malformed / duplicate / auth-denied still answer with their real codes, as they already do (ledger row 14) |
| T8 | Nothing references the deleted symbols | build + `grep` census C1 returns empty |

**Mutation discipline:** T1's claim is structural, so the proof is to reintroduce one early return and
assert T1 reds — a structural assertion, not a timing one, which is what makes it a gate rather than a
flaky benchmark.

---

## 11. Contract surface

**No frozen-contract change.** Contract #9 §9.3 promises the wire-shape collapse, which survives
untouched; it says nothing about timing, the quantum, or payload size (ledger row 16). §9.4's
invariants describe the secret's minting and the hash's shape — neither changes.

**One thing to note rather than change:** §9.4 does not say `.claimKey` is stored encrypted, so
dropping `Sensitive: true` falsifies no contract sentence. It does change an observable property of
the stored data, which is why §4.2 is a decision rather than an edit.

---

## 12. Corrections this design records

1. **The first draft answered half of Andrew's row** — it argued membership cannot dissolve and never
   considered removing the code. The alternatives table had no "delete the component" row, which is a
   check the designer skill already carries and I did not run.
2. **The payload cap and its fork dissolve** — both were artifacts of the window this design removes.
3. **The severity was over-stated at ★★★** — the per-cause delta is ~0.3 ms against a ~17 ms loaded
   p99, so the surviving channel is statistical, not one bit per request. Now ★★.
4. **The map's *"EVERY rejection"* doc is literally false** (ledger row 14) — four pre-step-4 paths
   answer un-collapsed today.
5. **Two of the collapse's three supports are already universal** (ledger rows 11–12), so the
   membership map buys less than its doc implies even before this design.
6. **`Vault.Decrypt` is in-process, not an RPC** — the previous version of this doc said otherwise and
   built its recommendation on it. The network hop is `readPiiKeyEnvelope`'s `KVGet` (§2).
7. **The declassification of `.claimKey` is withdrawn** — unnecessary once the tombstone's retained
   ciphertext is used (§4.2). The data-sensitivity question put to Andrew in the previous version is
   void.
8. **The file splits rather than deletes** (§6.4) — ~190 lines of the 277 are timing machinery; the
   op-name map survives for the wire collapse and the closed read set.
9. **R1 is accepted, not closed** (§6.3), and the measurement is skipped — both Andrew's decisions on
   2026-08-27, recorded rather than argued.

---

## 13. Executable censuses

**C1 — the deletion inventory.**
`grep -rln 'claimReplyFloor\|ClaimRejectionFloor\|publishNoEarlierThan\|replyToNoEarlierThan\|DrainClaimReplies\|ClaimFloor' --include='*.go' internal/ cmd/`
*Run this fire:* six files — `cmd/processor/main.go`, `internal/processor/{claim_reply_floor.go,
claim_reply_floor_test.go,commit_path.go,health.go}`, `internal/testutil/pipeline.go`. Must return
empty after the increment.

**C2 — the early-return count.**
`awk 'NR>=1446 && NR<=1545' packages/identity-domain/ddls.go | grep -c 'fail_claim('`
*Run this fire:* **18** for `ClaimIdentity`; **15** for `CompleteCredentialLink`. Must be **1** each
after §4.1 — a single terminal `fail`.

**C3 — sensitive-aspect blast radius.** Every reader of `.claimKey` and every gate keyed on its
sensitivity, before the flag moves:
`grep -rn 'claimKey' --include='*.go' packages/ internal/ | grep -v _test`. The flag change must not
be assumed to be local.
