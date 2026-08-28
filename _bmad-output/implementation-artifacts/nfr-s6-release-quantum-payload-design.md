# NFR-S6 — delete the masking machinery and equalize the work instead

> **📐 awaiting-Andrew (ratification)** — *Winston (Designer fire, 2026-08-27; rewritten the same day
> after Andrew rejected the draft's direction).*
>
> **What it does, in two lines.** `ClaimIdentity` / `CompleteCredentialLink` rejections take
> measurably different times depending on *why* they failed, which is an identity-existence oracle.
> Today that is **masked**: 277 lines of engine code hold every rejection and release it on a 50 ms
> lattice. This design **deletes that machinery** and removes the timing difference at its source
> instead — where it actually lives, which is mostly the package's own script.
>
> **Net effect: −277 lines of engine code, −1 hardcoded op-name map, −3 metrics, −1 goroutine-per-reply
> deferral, −1 shutdown drain, −50 ms of latency on every claim rejection. No new mechanism anywhere.**
> The payload cap the first draft recommended, and the fork it put to you, both **dissolve** — with no
> deferral window there is nothing for a caller to price.
>
> **One decision is yours, and it is a data-sensitivity judgement** (§4.2): `.claimKey` is declared
> `Sensitive: true`, so it is encrypted at rest and its hydration costs a **Vault NATS round-trip** —
> except for an already-claimed identity, whose tombstoned `.claimKey` takes `decryptSensitiveDoc`'s
> `IsDeleted` arm and **skips the RPC entirely**. That skipped round-trip is almost certainly the
> dominant term in the measured 0.27–0.70 ms spread. It is not fixable in the script and it must not be
> fixed by decrypting a tombstone (`sensitive_decrypt.go:158-171` forbids that deliberately). The clean
> removal is to **stop declaring `.claimKey` sensitive**: it stores the sha256 of 256 bits of
> `crypto/rand` (`cmd/lattice/identity/identity.go:27-35`), which is not a secret encryption-at-rest
> protects. **My recommendation: drop the flag.** The counter-argument is real and it is why this is
> yours: nothing validates a *client's* secret entropy, so a client that hashes a weak PIN would lose
> defence-in-depth against an attacker who already reads Core KV.
>
> **If you keep the flag**, the residue survives and some masking with it — §7 says exactly how much
> and what the fallback is. The script equalization (§4.1) and most of the deletion still land.

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
| R2 | **`decryptSensitiveDoc`'s `IsDeleted` arm skips `readPiiKeyEnvelope` + `Vault.Decrypt`** for an already-claimed identity's tombstoned `.claimKey` | `sensitive_decrypt.go:154-171` | **Dominant.** Vault is a NATS micro service (`internal/vault/service.go:208`), so this is a whole RPC round-trip — the right order of magnitude to *be* the measured 0.27–0.70 ms spread. |

R2 is the one that matters and it is **not** fixable in the script: the script never sees it, because
it happens during hydration before the script runs.

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

## 4. The shape — three moves, all subtractive or package-side

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

### 4.2 Remove `Sensitive: true` from `.claimKey` — the decision for Andrew

R2 is the dominant term and there are only three ways to remove it:

| | | |
|---|---|---|
| **(a) Decrypt the tombstone anyway** | **Refused.** `sensitive_decrypt.go:158-171` forbids it in terms, and its reason is sound: the tombstone retains the body, so the flag is the only thing between a dead aspect and a live decrypt. Bending it to fix a timing channel would trade a real confidentiality rule for a statistical one. | ✗ |
| **(b) Stop tombstoning `.claimKey` on claim; overwrite the hash with a random value** | **Refused, same reason at one remove.** It keeps a claimed identity's claim aspect live and decryptable forever. Junk content does not make that posture better. | ✗ |
| **(c) Stop declaring `.claimKey` sensitive** | The aspect holds the **sha256 of 32 bytes of `crypto/rand`** (ledger row 10). Encryption-at-rest of that hash protects against nothing an attacker who already reads Core KV can do — the preimage is not recoverable. Drop the flag and `decryptSensitiveDoc` never runs for it: **both arms disappear, and with them the divergence.** | ✓ **recommended** |

**The counter-argument, stated because it is why this is yours and not mine.** The contract validates
the hash's *shape* — "lowercase hex `sha256`, validated for shape on create" (§9.4) — never its
preimage entropy. The CLI mints 256 bits; a different client could submit the hash of a four-digit
PIN, and for that client encryption-at-rest is real defence-in-depth against an attacker with Core-KV
read. So (c) trades a defence that only matters for a weak-secret client against the deletion of the
entire masking mechanism. **I recommend taking the trade** — the weak-secret client is already
catastrophically exposed by the oracle this design closes, and by brute-forcing the claim endpoint
directly — but it is a data-sensitivity call at your altitude, not a mechanism call at mine.

### 4.3 Delete the machinery

With §4.1 and §4.2 in place there is nothing left to mask:

- **`internal/processor/claim_reply_floor.go`** — the whole file, 277 lines: `claimReplyFloor`,
  `releaseAt`, `publishNoEarlierThan`, `Drain`, `DefaultClaimRejectionFloor`,
  `maxPendingDeferredReplies`, `claimFloorDropLogEvery`, `replyPublisher`.
- **`commit_path.go`** — `replyToNoEarlierThan`, `DrainClaimReplies`, the `receipt` parameter threaded
  through eleven rejection branches, and `Deps.ClaimRejectionFloor`.
- **`health.go`** — `ClaimFloorApplied`, `ClaimFloorLate`, `ClaimFloorDropped` and their three
  heartbeat fields.
- **`cmd/processor/main.go`** — the drain call and `claimReplyDrainBudget`.
- **`internal/testutil/pipeline.go`** — the floor wiring.
- **`nfrS6Operations`'s quantized-release half**, and the `receipt` stamp at `commit_path.go:222` if
  nothing else reads it.

**What survives, and why.** The **wire-shape collapse** is a Contract #9 §9.3 promise and stays. Two
of its three current supports are already universal, not membership-keyed: the script emits one code
itself (ledger row 11) and `classifyStepError` strips details from any `ClaimKeyInvalid` for every
operation (row 12). What the membership map still buys is the **step-4 fault** case — a hydrate or
decrypt failure before the script runs, which returns a bare error and classifies `InternalError`.
§7's measurement decides whether that residue needs the map at all; if it does, the map shrinks to
one behaviour from four, which is already most of what the original row asked for.

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

**D. Equalize the script only, keep the mask for R1+R2.** The honest fallback if Andrew keeps the
sensitive flag. It removes the largest and most numerous divergences and keeps 277 lines to hide one
RPC. **Not recommended**, but §7 quantifies what it would leave.

**E. Make the *engine* constant-time generically** — pad every rejection to a fixed budget. **Rejected**:
that is the masking mechanism again, generalized, applied to every operation in the platform. Strictly
more machinery than exists today.

**F. Remove the collapse too and let rejections answer honestly.** **Rejected**: it is a frozen
Contract #9 §9.3 promise, and unlike the quantum it is genuinely load-bearing — the wire code is the
cheapest possible oracle.

---

## 6. Reconciliation with the existing mental model

**"Didn't `624d445` decide this?"** It closed the oracle, correctly, with the tool available at the
time. Its own §19.10 recorded that quantization *"removes the escape branch but not the boundary."*
This design removes the boundary by removing the reason for it. That is not a reversal of `624d445`;
it is the version that did not need a deferral.

**"Is the script rewrite riskier than the mask?"** Different risk, and it is measurable in the same
harness. The mask's correctness rests on a timer; the equalized script's rests on both branches
executing the same instructions, which is a property a per-cause study tests directly — the same
n=3000 study that validated the mask (§7).

**"Does this introduce new state?"** No. It removes state: a goroutine per deferred reply, a pending
counter, a drop counter, a `WaitGroup`, three metrics, and a shutdown drain.

**"What about the payload lever?"** It exists only inside the deferral window. No window, no lever.
The board row's payload half closes as *dissolved*, not as *fixed*.

---

## 7. The measurement — the acceptance gate, and it can refute the design

The harness already exists in the shape `624d445` used, and `Deps.ClaimRejectionFloor` accepts a
**negative** value to disable the quantizer (ledger row 15) — which is exactly the posture needed.

**Run the n=3000 per-cause study with the floor disabled, at three points:**

| Point | What it tells us |
|---|---|
| **P0 — today, floor off** | Reproduces the baseline: monotone ordering, 0.27–0.70 ms spread. Confirms the harness still measures what it measured. |
| **P1 — after §4.1 (script equalized), floor off, flag kept** | Isolates R1+R2. The remaining spread **is** the engine-side residue, measured rather than argued. |
| **P2 — after §4.2 (flag dropped), floor off** | The acceptance test. **All three CIs must include zero**, as they did with the mask on. |

**Acceptance:** P2 clears ⇒ delete the machinery (§4.3). **P2 fails ⇒ the design is refuted and the
mask stays** — write up why, keep §4.1 (it is a strict improvement regardless), and the item closes as
*"equalization insufficient, masking retained."* That outcome is stated in advance as a success.

**P1 is what decides Andrew's question with data rather than argument.** If P1 already clears, the
sensitive flag never needs to move and §4.2 is moot — I would expect it not to, given R2 is an RPC,
but the measurement outranks my expectation.

---

## 8. Decomposition for the Steward

**One increment, gated on the measurement.** Phase 0 runs P0 and P1. Then, per Andrew's answer on
§4.2: run P2, and if it clears, land the script equalization, the flag removal, and the deletion
together. Splitting them would leave either a mask with nothing to mask or an equalized script still
paying 50 ms.

**Posture-changing — full review depth.** It removes a shipped security mechanism and changes an
aspect's sensitivity declaration.

**Package-version discipline:** `identity-domain`'s manifest version and its mirroring `Version`
constant must bump for both the script rewrite and the DDL flag change, or a running stack no-ops the
install (`DIFF_BASE=<base-sha> go run ./scripts/lint-package-version.go`).

---

## 9. Test strategy

| # | Proves | Shape |
|---|---|---|
| T1 | Every cause executes the same instructions | per-cause instruction/step-count assertion on the equalized script — not a timing test, a structural one |
| T2 | The sha256 and the constant-time compare run on **all** causes | including absent target and malformed payload, against the placeholder and dummy hash |
| T3 | No behavioural regression | every existing `ClaimIdentity` / `CompleteCredentialLink` outcome still produces its same Health-KV outcome word and the same generic wire reply |
| T4 | The equalized script still refuses everything it refused | one vector per accumulated outcome — 18 for claim, 15 for link |
| T5 | **P2**: all three CIs include zero, floor disabled | the n=3000 harness; the acceptance gate |
| T6 | A `.claimKey` that is no longer sensitive still cannot be read by an unauthorized actor | the read path's own authorization is unchanged — pin it, because removing an encryption flag invites the assumption that it was the access control |
| T7 | The pre-step-4 paths are unaffected | malformed / duplicate / auth-denied still answer with their real codes, as they already do (ledger row 14) |
| T8 | Nothing references the deleted symbols | build + `grep` census C1 returns empty |

**Mutation discipline:** T1's claim is structural, so the proof is to reintroduce one early return and
assert T5 reds. **Fixture discipline:** T5 must run under the concurrent-submission load the original
study used; an unloaded run passes vacuously.

---

## 10. Contract surface

**No frozen-contract change.** Contract #9 §9.3 promises the wire-shape collapse, which survives
untouched; it says nothing about timing, the quantum, or payload size (ledger row 16). §9.4's
invariants describe the secret's minting and the hash's shape — neither changes.

**One thing to note rather than change:** §9.4 does not say `.claimKey` is stored encrypted, so
dropping `Sensitive: true` falsifies no contract sentence. It does change an observable property of
the stored data, which is why §4.2 is a decision rather than an edit.

---

## 11. Corrections this design records

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

---

## 12. Executable censuses

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
