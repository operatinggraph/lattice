# NFR-S6 — the release quantum is priced by the caller, and its soundness argument is written down twice and wrong

> **📐 awaiting-Andrew (ratification)** — *Winston (Designer fire, 2026-08-27).*
>
> **What it does, in two lines.** NFR-S6 releases every rejection of `ClaimIdentity` /
> `CompleteCredentialLink` at a quantized offset from receipt, so the per-cause service-time
> difference is not readable. The caller still prices work **inside** that window — `env.Payload` is
> decoded two to four times there and nothing bounds it below 1 MiB — so a caller can position the
> quantum boundary onto the per-cause delta and read the bias back statistically. This design bounds
> the payload for those operations, and corrects two shipped comments that assert a closure the
> mechanism does not have.
>
> **No frozen-contract change** (§10, with one reading of §9.3 flagged rather than decided).
> **One real architectural fork, and it is not the one the demand row proposed** (§4 shows that one is
> false):
>
> > **§3.1 — where the cap can sit, and what it costs.** The payload's **first** decode happens inside
> > `parseEnvelopeFromBody`, which runs *after* the receipt stamp and *before* `operationType` is
> > known. So a cap that is **class-scoped** cannot bound it, and a cap that bounds it cannot be
> > class-scoped. Three ways out, and the choice is yours because two of them cost something outside
> > this mechanism:
> >
> > **(A) Class-scoped cap immediately after parse.** Bounds decodes #2–#5 and the KV work; leaves
> > decode #1 (a scan-and-copy over ≤1 MiB) inside the window. **Sufficient iff `C₁·1 MiB ≤ Q/10`** —
> > §7 measures exactly this. Costs nothing else. **My recommendation, conditional on the
> > measurement.**
> > **(B) Tighten the universal pre-parse body cap.** Bounds decode #1 too, but it is universal: the
> > bound is floored by the largest legitimate operation in the system (`InstallPackage` carries a
> > whole package definition), so it cannot come near the value NFR-S6 wants. It closes the residual
> > only if the measurement says a 1 MiB-ish bound already suffices — in which case (A) alone did too.
> > **(C) Move the receipt stamp to after the parse.** The parse is target-*independent*, so the
> > window's stated invariant ("before any work whose duration depends on the target's state") still
> > holds — but the release then sits at a fixed offset from *parse completion* rather than from
> > *arrival*, and `claim_reply_floor.go:180-186` calls anchoring at arrival *"the entire mechanism."*
> > I am not willing to move that anchor on my own judgement.
> >
> > **If §7's measurement clears (A), there is no fork and I would have adjudicated this myself.** It
> > comes to you because the measurement could not be run in a doc-only fire and the fallback branches
> > are not mine to pick.
>
> **The design may also refute itself** (§7): if the measured cost cannot reach the quantum from any
> admissible payload, the vulnerability does not exist, the deliverable shrinks to the two comment
> corrections, and the row closes as refuted. That outcome is stated in advance as a success.

**Board row:** `[Processor] The NFR-S6 tail: payload prices the release quantum; membership is a
hardcoded list` — ★★★, M.
**Demand:** [lattice-designer-triage-2026-08-27.md §2](../../docs/reviews/lattice-designer-triage-2026-08-27.md).
**Predecessors:** `624d445` (claim-rejection timing oracle CLOSED) built the release quantum;
`c69aa4a4` (NFR-S6 declared-read set CLOSED) shut the declared-read padding lever.

---

## 1. What is actually wrong

NFR-S6 makes every rejection of `ClaimIdentity` and `CompleteCredentialLink` indistinguishable — in
**wire shape** (Contract #9 §9.3: *"All failure modes collapse to the generic `ClaimKeyInvalid` reply
code"*) and in **time**. The time half is `claimReplyFloor`: a rejection received at `receipt` and
finished at `done` is published at `releaseAt(receipt, done, Q)` — *"the first multiple of the quantum
after `receipt` that is not before `done`"* (`internal/processor/claim_reply_floor.go:143-176`), with
`Q = DefaultClaimRejectionFloor = 50ms` (`:32`).

The mechanism's soundness argument is stated in its own doc comment (`:157-162`):

> Under quantization there is no such escape hatch: the answer always lands on a lattice of fixed
> offsets from ARRIVAL (receipt+Q, receipt+2Q, …), so what a caller learns is only which quantum its
> request fell in — **a number it already controls by padding, and one that carries no information
> about the target.**

**The clause after the dash does not follow from the clause before it.** The quantum index is
`n = ceil(T/Q)` where `T = base(cause) + priced(caller)`. It is a function of *two* inputs. A caller
controlling one of them does not remove the other's influence — it lets the caller **position the
quantum boundary**. Choose padding so that `base(cause_A) + priced < Q` while
`base(cause_B) + priced > Q`, and `n` becomes a comparator on the cause. The floor's own doc
(`:14-21`) records the causes and their ordering: an absent target hydrates three missing keys; an
already-claimed one takes `decryptSensitiveDoc`'s `IsDeleted` arm; a wrong key pays both plus the
secret comparison — *"a few tenths of a millisecond, monotone in that order."*

**The same claim is written a second time**, on the metric that exists to watch the invariant
(`internal/processor/health.go:53-62`): *"Quantizing means that leaks nothing."* Two independent
statements of one false premise, in the two places a future author would look.

### 1.0 The platform already knows — the ratified design says so, and the code contradicts it

This is the part that reframes the item. `624d445`'s own design section
(`auth-plane-projection-latency-design.md` §19.10) records the residual **explicitly**:

> Quantization removes the escape branch but not the boundary. A submitter still prices the work
> inside the window … so padding can push a rejection across a quantum boundary, and the
> *probability* of crossing remains cause-dependent. That converts a continuous signal into a
> Bernoulli one, raising the cost by roughly two orders of magnitude rather than removing it.

And its 2026-08-25 amendment, and `c69aa4a4`'s §20, both name the payload lever by name — *"`payload`
bytes are decoded three times inside it … under no server-side `InputSchema` enforcement and no bound
below the Gateway's 1 MiB body cap."*

**So the finding is not that nobody knew. It is that the two doc comments a future author will
actually read assert the opposite of the ratified design** — `releaseAt`'s *"carries no information
about the target"* and `ClaimFloorLate`'s *"Quantizing means that leaks nothing"* both state a
closure the design that introduced them explicitly declined to claim. That divergence is the more
dangerous half: the design doc is where the honest statement lives and the code comment is where the
next fire will look. Correcting them is owed regardless of what else is decided.

### 1.1 The severity, priced honestly — the demand row over-states it

The demand row calls it *"one bit per request."* It is not. The per-cause delta is ~0.3 ms and the
**loaded** p99 of the rejection path is ~17 ms (`claim_reply_floor.go:22-31`, the constant's own
sizing measurement). A single request's quantum index is dominated by jitter, so no single reply
carries the bit.

What survives is **statistical**: at a fixed padding that puts the mean near a boundary,
`P(n = 2)` is a monotone function of `base(cause)`, so averaging the release index over many requests
recovers the same bias the un-quantized path leaked — the very attack the floor's doc describes as
*"extractable from a few dozen replies by averaging."* The quantum raises the sample count; it does
not close the channel. **The accurate claim is: quantization reduces the oracle's bandwidth and does
not eliminate it, and the mechanism's stated reason for believing otherwise is false.** That is
word-for-word §19.10's *"a Bernoulli one … roughly two orders of magnitude"* — so the demand row's
★★★ *"one bit per request"* is contradicted not by my reading but by the **ratified design of the
mechanism it is describing**. That is worth
fixing — a shipped soundness argument that is refutable by anyone who measures is exactly how a
correct guardrail gets deleted by a later fire — but it is a **narrower** finding than the row's
framing, and the board should say so.

### 1.2 Which lever is still open

The floor's doc and the metric's doc both name **declared reads** as the padding vector
(`opwire.MaxDeclaredReads` = 1000, *"all resolved inside the window"*). **That lever is already
shut** for these two operations: `c69aa4a4` added `refuseUndeclaredContextHint`, gated on
`isNFRS6Operation` at the step-4 head, *before* `derive_reads` and before the first Core KV GET
(`internal/processor/step4_hydrate.go:220-232`) — the declared set is closed to the descriptor's own
keys. **Both doc comments are stale about the vector as well as wrong about the conclusion.**

The lever that remains is the one nothing bounds: **`env.Payload`**. It is a separate envelope field
from `ContextHint` (`internal/processor/opwire/opwire.go:129-142`), it is caller-supplied, and the
work proportional to it — unmarshalling it, marshalling it into the Starlark sandbox, the script's own
handling — happens **after** `receipt`, which is stamped *"before any work whose duration depends on
the target's state"* (`claim_reply_floor.go:180-186`). Its only bound today is the NATS server's
`max_payload`, 1 MiB by default.

So: `T = base(cause) + C·P`, with `P` free up to ~1 MiB and `C` the payload-proportional cost per
byte. If `C · P_max` can reach `Q`, the caller can put the boundary anywhere. **§7's measurement is
what decides whether it can, and it is the one number this design turns on.**

---

## 2. Grounding ledger

| # | Fact | Citation |
|---|---|---|
| 1 | `Q = 50ms`, sized against **loaded** tail latency: ~9.5 ms mean, ~12 ms p90, **~17 ms p99** | `claim_reply_floor.go:10-32` |
| 2 | The per-cause work differences and their monotone order; *"a few tenths of a millisecond … extractable from a few dozen replies by averaging"* | `claim_reply_floor.go:14-21` |
| 3 | `releaseAt` = the first multiple of `Q` after `receipt` not before `done`; integer ceiling, `n ≥ 1` | `claim_reply_floor.go:143-176` |
| 4 | **The false claim, instance 1**: *"a number it already controls by padding, and one that carries no information about the target"* | `claim_reply_floor.go:157-162` |
| 5 | **The false claim, instance 2**: *"Quantizing means that leaks nothing"* — on the metric that exists to watch the invariant | `health.go:53-62` |
| 6 | `receipt` is stamped *"before any work whose duration depends on the target's state"*; anchoring there *"is the entire mechanism"* | `claim_reply_floor.go:180-186` |
| 7 | Membership is a hardcoded two-op map, keyed on `operationType` and deliberately **not** on the error code, with the two holes that keying-on-code left recorded | `claim_reply_floor.go:34-72` |
| 8 | **Four** gated behaviours across **three** call sites — the demand row says three behaviours: (a) the closed declared-read set `step4_hydrate.go:227`; (b) the Health-KV `internal-fault` outcome `commit_path.go:1032`; (c) the wire-shape collapse and (d) the quantized release, together at `commit_path.go:1125-1138` | as cited |
| 9 | The declared-read padding lever is **already closed** for NFR-S6 ops, at the step-4 head, before `derive_reads` and the first GET | `step4_hydrate.go:220-232`, `descriptor_floor.go:419` |
| 10 | `Payload` is `json.RawMessage`, a field separate from `ContextHint` | `opwire/opwire.go:129-142` |
| 11 | **No payload-specific bound exists.** The operative cap is the Gateway's whole-HTTP-body `maxBodyBytes = 1 << 20`; NATS `max_payload` (1 MiB default, read live) bounds the transport; step 8's value guard fires **after** the window. `env.Payload` itself carries no size annotation and no cap | `internal/gateway/gateway.go:39`, `:484-489`; `internal/substrate/batch.go:222-237`; `internal/processor/step8_commit.go:400`; `opwire/opwire.go:135` |
| 11a | `env.Payload` is decoded **three times** inside the window — the admitted-set compile, the `derive_reads` pre-pass, and the step-5 runner — so `C` is three decodes, not one | `auth-plane-projection-latency-design.md:2698-2699` |
| 11b | The ratified design **already records the residual**: padding pushes a rejection across a boundary, *"the probability of crossing remains cause-dependent … a Bernoulli one, raising the cost by roughly two orders of magnitude rather than removing it"*; its amendment and `c69aa4a4` §20 both name the payload lever | `auth-plane-projection-latency-design.md:2520-2525`, `:2530-2536`, `:2696-2701` |
| 11c | `624d445`'s measurement: **n=3000** per cause, mechanism OFF → monotone ordering, 0.27–0.70 ms separation; ON → every CI includes zero, p50 spread **10.6 µs**. It did **not** sweep payload size, which is why §7's measurement is the missing one | `auth-plane-projection-latency-design.md:2510-2519` |
| 12 | `maxPendingDeferredReplies = 1024`; overflow **drops** the reply (the caller times out), and dropping is argued as *"the fail-safe direction … an early answer restores the timing signal at exactly the moment an attacker is generating the load"* | `claim_reply_floor.go:88`, `:203-217` |
| 13 | `ClaimFloorLate` already counts releases beyond the first quantum and is documented as *"the operator's only way to know the anti-enumeration invariant is currently holding"* and *"the direct detector for the padding attack"* | `health.go:53-62`, `:323-325` |
| 14 | Contract #9 §9.3 promises the **wire-shape collapse only** — it says nothing about timing, the release quantum, or payload size | `docs/contracts/09-identity-claim-flow.md:58-59` |
| 15 | The hardcoded map is the fourth instance of a core-owned op-name map; the sibling's rule is *"core owns the policy; packages own only the assignment"* | `privilegedLaneAllowlist`, `reservedOperationTypes`, the Gateway's `rawCredentialCarveOut` — see census C3 |

---

## 3. The shape

### 3.1 A class-scoped payload cap, checked before the receipt stamp

For an operation in `nfrS6Operations`, refuse at admission when `len(env.Payload) > nfrS6PayloadCap`.

**Why this closes the channel rather than narrowing it.** With `C · nfrS6PayloadCap ≪ Q` and the
loaded p99 already at ~17 ms against `Q = 50 ms`, every rejection's `T` lands in the **first**
quantum. `releaseAt` then returns `receipt + Q` for every request, for every cause, unconditionally —
a constant. There is no index to read, no boundary to position, and the property holds without
anything in the reply depending on the target. **The cap is what makes the quantum's own soundness
argument true**; today that argument is asserted without it.

**Where the check goes — and the contradiction the first draft carried.** The draft said "before the
receipt stamp." That is impossible for a *class-scoped* check: `receipt` is stamped at
`commit_path.go:222`, **before** `parseEnvelopeFromBody` at `:226`, and `operationType` is not known
until the parse completes. The honest sequencing is:

| # | Payload-proportional work | Where | Bounded by a post-parse cap? |
|---|---|---|---|
| 1 | `ParseEnvelope`'s decode — a scan **and copy** of the raw payload (`Payload` is `json.RawMessage`, so it is not skipped) | `opwire.go:264-272`, after `receipt` | **No** — it is what produces the field the cap reads |
| 2 | `resolveClass`'s `json.Unmarshal` into a field map — skipped when the client sets `class`, **forced by omitting it** | `step4_hydrate.go:499-520` | Yes |
| 3 | `payloadMap()` → full generic decode, every field allocated (memoized per Hydrate) | `descriptor_floor.go:278-287` | Yes |
| 4 | The NFR-S6 closed-set gate itself — both descriptors are `{payload.*}`-rooted, so the gate **requires** the decode | `descriptor_floor.go:322-324` | Yes |
| 5 | Step 5's `operationEnvelopeToStarlark` — a **separate** generic decode plus per-field conversion; padding fields are converted, nothing rejects them | `starlark_runner.go:640-650` | Yes |

So the cap sits **immediately after the parse**, where `operationType` is first available and before
`resolveClass`. It bounds four of the five terms. The fifth is the fork in the banner.

**Why this refusal need not collapse to `ClaimKeyInvalid`, and there are four shipped precedents.**
The map's doc says *"EVERY rejection of the operation"* is collapsed and quantized. **That is
literally false today**, and usefully so: step-1 malformed (`commit_path.go:241`), step-2 duplicate
(`:265`), step-3 authorizer error (`:275`) and step-3 denial (`:294`) all answer with their real code
and details, no receipt argument and no quantum — because each is actor-scoped rather than
target-scoped. A payload-size refusal joins exactly that family, and the design **corrects the doc's
"EVERY"** to say what the code does and why. The collapse (Contract #9 §9.3) exists to hide
**target-dependent** causes. `len(env.Payload) > cap` is a pure
function of the caller's own bytes: it reads no target, hydrates nothing, and tells the caller only
something they already know. So it answers immediately, with its own distinct code, and carries no
information about any identity. **This is the whole argument for the exception and it is narrow by
construction**: the predicate must be exactly the length test and nothing else. A cap that ever
consulted the target — a per-identity limit, a descriptor lookup that could fail differently for a
missing target — would be a new oracle wearing a size check. T4 pins that the refusal is reachable
with no target in the graph at all.

### 3.2 Correct both statements of the false claim

`releaseAt`'s doc (`:157-162`) and `Metrics.ClaimFloorLate`'s doc (`health.go:53-62`) are rewritten in
the same increment: the quantum bounds the channel **only while every cause's work fits inside the
first quantum**, which is what the cap guarantees; and the padding vector they both name (declared
reads) was closed by `c69aa4a4`, leaving payload bytes as the lever this design shuts. A soundness
argument that is refutable by measurement is worse than none — the Refractor dossier's own entry, and
this is the Processor's instance of it.

### 3.3 `ClaimFloorLate` becomes the invariant's gate, not just its gauge

Its doc already calls it *"the operator's only way to know the anti-enumeration invariant is currently
holding"* and *"a steady zero means every rejection is landing on the first boundary."* Under the cap,
a non-zero `ClaimFloorLate` means a cause's work outran `Q` — i.e. the channel is open right now. So
the counter gains a Health issue at `warning` when its rate is non-zero over a window, naming the
invariant rather than the counter. This is a one-step extension of the metric's stated purpose, and
it is what stops the cap from silently becoming wrong when the rejection path slows down.

### 3.4 Membership stays hardcoded — the direction is falsified, with evidence

The demand's *"de-hardcode by simplifying; membership dissolves"* direction does not survive:

- **None of the four gated behaviours is safe universally.** The closed declared-read set applied to
  every operation admits nothing for an op with no descriptor and would refuse most of the corpus
  (~70 hand-built submission sites). The wire-shape collapse universally destroys every error message
  the platform has. The quantized release universally adds `Q` to every rejection in the system and
  overflows the shared 1024-deferral bound into silent drops (ledger row 12).
- **No existing declaration coincides with membership.** Ceremony and Sensitive are *anti*-correlated
  with the two members; `derive_reads` is per-DDL, the wrong granularity.
- **Package-declared membership is the fail-open direction.** It would be package-*withdrawable*,
  which is precisely what the fire that built this was defending against. The sibling maps' stated
  rule is *"core owns the policy; packages own only the assignment"* (ledger row 15).

**One correction to the row's supporting claim.** It calls the map *"the fourth instance of a ratified
core-owned op-name map."* Only two of the three siblings actually state that rule —
`privilegedLaneAllowlist` and `reservedOperationTypes`, which are each other's declared twins in one
file (`step3_auth_capability.go:419-481`). The Gateway's `rawCredentialCarveOut`
(`gateway.go:308-315`) is a Gateway-local **mechanical** carve-out ("every op whose script hashes
`op.actor`"), with a different justification shape. The pattern is a pair plus a look-alike, which is
weaker than "four instances" — and it still supports the conclusion, because the pair is the one that
states the ownership rule.

**And the look-alike is the more interesting datum, which this design records rather than fixes.**
`rawCredentialCarveOut`'s membership is **byte-identical** to `nfrS6Operations` — the same two
operations, maintained independently in two packages, with no shared constant and no gate tying them
together. **Nothing in the tree fails if one drifts from the other**, and a drift would silently
un-cover an enumeration oracle in exactly the way keying-on-error-code did (ledger row 7). A lint or a
pinning test asserting the two sets agree is a four-line gate; it ships in this increment, per the
standing rule that the gate enforcing a convention ships with the design that noticed it.

This design adds one consumer to the existing map rather than a fifth list. That is the whole resolution; §5 (A) prices the alternatives.

---

## 4. The demand row's proposed fork is false

The triage offered, as a fork for Andrew: *"release unconditionally at `receipt + Q`, fail-closed drop
past Q — the 'which quantum' channel disappears entirely, closing the payload axis and every future
axis with no cap and no membership question,"* trading availability on the interactive error path.

**Trace both branches against the named consumer before weighing them.** Under branch (b), release is
constant, so the *timing* channel is gone — and the caller now learns something else: whether it got a
reply at all. `P(drop)` is `P(T > Q)`, which is a function of `base(cause)` exactly as `P(n = 2)` was.
**The bit is not closed; it is renamed**, from the release index to the drop indicator, with the same
statistical extraction and the same padding lever positioning the same boundary. Branch (b) also does
**not** remove the membership question — something must still decide which operations get an
unconditional deferral, and applying it universally is the same refusal §3.4 already records.

And the branches are not independent: **with the cap, branch (b) is already true.** If every cause
lands in the first quantum then `releaseAt` returns `receipt + Q` unconditionally, which is what
branch (b) asks for — obtained by construction, with no drops and no availability trade at all.

So the fork collapses: both directions need the cap, and once it exists only one of them is
distinguishable from the other. This is the *"check whether the branches all need the same missing
primitive"* test, and they do. **There is no fork for Andrew here.**

---

## 5. Alternatives considered

**A. Universal payload cap, no membership.** *Rejected.* `InstallPackage` carries a whole package
definition; a cap tight enough to serve NFR-S6 refuses it. A cap loose enough for `InstallPackage`
(near `max_payload`) leaves `C·P` free to reach `Q` and closes nothing. The two requirements are
incompatible at one number, which is the reason membership exists.

**B. Per-op cap declared in the op-meta descriptor.** *Rejected as specified, admissible as a floor.*
A package-declared cap is package-withdrawable — the fail-open direction. A **core floor that a package
may only lower** is fail-closed and would be fine, but it still needs the core-owned set to know which
ops carry the floor, so it buys nothing this design needs and adds a descriptor field with a
resolution path inside the very window under discussion. *Could a variant beat it?* Only once a third
NFR-S6-class operation exists; the trigger is named in §8.

**C. Server-side `InputSchema` validation** instead of a byte cap. *Rejected here for the first time —
this refusal is **not** inherited.* Both prior fires only noted the absence of a validator and filed it
forward; the "explicitly refuse it" language originates in today's triage, so it is a proposal to
adjudicate rather than settled precedent, and this section is that adjudication. `InputSchema` is
declared per-op and per-DDL (`internal/pkgmgr/definition.go:641-645`, `:998-999`), stored as an
`.inputSchema` aspect at install time, and **nothing in `internal/processor` ever reads it** — the
module's only JSON-schema library is an indirect dependency no in-repo validator imports. *Rejected,
and the reason is the mechanism.* It introduces a schema-resolution dependency and a cache path on the rejection route, and
validation is itself `O(payload)` work performed **inside the window it is meant to protect** — it
prices the quantum with the same lever it is closing, and adds a second one (schema complexity). A
length test is O(1) and needs nothing resolved.

**D. Raise `Q`** so no plausible payload can cross it. *Rejected.* `Q` is already sized at 3× the
loaded p99 and adding latency to an interactive error path is the cost the constant's own doc balances.
It also does not close the channel — it moves the boundary, and the caller's padding follows it.

**E. Lower `max_payload` at the NATS server.** *Rejected as the mechanism, worth noting as context.*
It is global, so it is bounded below by the largest legitimate operation in the system
(`InstallPackage`), which is exactly the constraint that makes a class-scoped cap necessary.

**F. Do nothing; the residual channel is statistical and slow.** *Genuinely arguable, and rejected on
two grounds.* First, the two shipped comments assert a property the mechanism does not have, and a
future author will build on them — the correction is owed whatever else is decided. Second, this
endpoint is `scope: self` with nothing rate-limiting it (ledger row 12's own reasoning), so "needs many
samples" is not a bound an attacker feels. The **cheap** part of this design (a length test and two
comment rewrites) closes it; that is a different cost profile from the row's implied one.

---

## 6. Reconciliation with the existing mental model

**"Didn't `624d445` close this?"** It closed the direct read — the un-quantized path where per-cause
service time was the reply's latency. It introduced the quantum and, with it, the assumption this
design corrects. Its own sizing note anticipates the shape of the problem (*"an attacker can generate
the load that pushes latency past a small one"*) but treats load, not caller-priced work, as the lever.

**"Didn't `c69aa4a4` close the padding?"** It closed the **declared-read** padding, which is the lever
both surviving comments name. Payload bytes were not in its scope. The two fires compose, and this is
the third and — after the cap — the last caller-priced input inside the window.

**"Is the quantum still worth having once the cap exists?"** Yes, and the relationship is worth stating:
the **cap** guarantees every cause fits in the first quantum; the **quantum** is what makes the release
time constant given that. Neither is redundant. Remove the cap and the index becomes a comparator again;
remove the quantum and the raw per-cause service time is back.

**"Does this introduce new state?"** No. A constant, a length comparison, a Health issue derived from an
existing counter. No registry, no cache, no durable key, no lifetime table.

---

## 7. The measurement this design turns on

**One number decides the cap, and it was not measurable in a doc-only fire.** Phase 0 of the build
measures it and the design's threshold is stated in advance so the measurement can falsify it:

> **`C₁` — the cost of `ParseEnvelope`'s scan-and-copy alone, in ms per KiB**, and
> **`C₂₋₅` — the cost of the remaining two-to-four decodes plus the Starlark conversion pass.**
> Measured separately, because the fork in the banner turns entirely on which of them dominates:
> a post-parse cap bounds `C₂₋₅` and leaves `C₁` free up to the 1 MiB body cap.
>
> Measured on the rejection path, under the concurrent-submission load the `Q` constant was sized
> under (`claim_reply_floor.go:22-31`), sweeping `P` up to 1 MiB, and driving all three causes.
> `Deps.ClaimRejectionFloor` accepts a **negative** value to disable the quantizer outright
> (`commit_path.go:58-67`), which is the posture the measurement needs — the same knob `624d445`'s
> own n=3000 study used. **Force decode #2** by omitting `class` from the envelope, which the shipped
> submitters set but a hand-rolled one need not (`step4_hydrate.go:499-520`); the honest-client and
> hostile-client costs are different numbers and the cap must be sized against the hostile one.

**Acceptance, in two parts.**

1. **The fork resolves to (A)** iff `C₁ · 1 MiB ≤ Q/10` (5 ms against a 50 ms quantum) — the residual
   first decode cannot move the boundary, so a post-parse class-scoped cap is sufficient and nothing
   outside this mechanism has to change.
2. **The cap** is then the largest power-of-two KiB value satisfying `C₂₋₅ · cap ≤ Q/10`, floored at
   the largest legitimate `ClaimIdentity` / `CompleteCredentialLink` payload plus headroom (census C4).

If (1) fails, the fork is live and Andrew's answer decides between branches (B) and (C). The triage's estimate of 4–8 KiB is the expected answer, **not an input**. Its cited precedent does
not transfer cleanly and the design says so rather than borrowing it: identity-domain's
`MAX_CONTACT_INPUT = 4096` (`packages/identity-domain/ddls.go:876`) is a **per-field** bound on one
Starlark-normalized string, enforced in a **package script**, sized to bound `lower()+split()+join()`
work the sandbox's step ceiling does not count. This design's cap is a **whole-payload** bound in the
**Go layer**. Same word, different granularity and different enforcement layer — the number is a
sanity anchor, not a derivation. If the measurement says a different number, the measurement wins: if the measurement puts `C` low enough that even
`max_payload` cannot reach `Q/10`, then **the vulnerability does not exist and this design should not
be built** — write that finding up, correct the two comments, and close the row. That outcome is a
success, and stating it in advance is what keeps the measurement honest.

The measurement doubles as the arithmetic check the row's own claim needs: multiply `C` by `P_max` and
confirm the product reaches `Q`. If it does not, the row's mechanism cannot produce the row's symptom.

---

## 8. Decomposition for the Steward

**One increment.** The item is a constant, a length test at admission, two doc-comment corrections, and
a Health issue derived from an existing counter. Splitting it would ship the correction without the
fix or the fix without the argument.

Phase 0 runs §7's measurement and **stops** if `C · P_max < Q/10` — in that case the deliverable is the
two comment corrections plus a written finding, and the board row closes as refuted.

If Phase 0 resolves the fork to **(A)**: `nfrS6PayloadCap` sized from the measurement; the check
immediately after the parse (§3.1); the two doc rewrites plus the *"EVERY rejection"* correction
(§3.2, §3.1); the `ClaimFloorLate` Health issue (§3.3); the `rawCredentialCarveOut` agreement pin
(§3.4); tests T1–T11. If it resolves to **(B)** or **(C)**, the increment does not start — the branch
is Andrew's and the design returns to him with the numbers. **This is a
security-plane change on an authorization endpoint — posture-changing, and it warrants the full review
depth at the Steward's sizing.**

**Sequenced behind a named trigger:** the descriptor-declared cap floor (§5 B) — trigger: a third
operation joining `nfrS6Operations`.

---

## 9. Test strategy

| # | Proves | Shape |
|---|---|---|
| T1 | The cap refuses an oversized NFR-S6 payload, and admits one at the cap | boundary vectors at `cap` and `cap+1` |
| T2 | The refusal is **outside** the timed window | assert no `ClaimFloorApplied` increment and no deferral for an over-cap submission |
| T3 | The refusal is target-independent | identical refusal for an absent target, a claimed target and a wrong key — the three causes NFR-S6 hides |
| T4 | …and reachable with no target at all | over-cap submission against an empty graph refuses identically |
| T5 | Under the cap, every rejection lands in the first quantum | drive all three causes at the maximum admissible payload under the loaded profile; assert `ClaimFloorLate == 0` and equal release offsets |
| T6 | The cap does not leak by wire shape | an over-cap submission's reply code/message is fixed and carries no details |
| T7 | `ClaimFloorLate` raises the Health issue and retires on its own evidence | force a late release; assert raise, then clear |
| T8 | Non-NFR-S6 operations are unaffected | `InstallPackage` with a multi-hundred-KiB payload still admits |
| T9 | The cap binds the **hostile** client, not just the honest one | an over-cap submission that **omits `class`** (forcing decode #2) refuses identically to one that sets it |
| T10 | `nfrS6Operations` and the Gateway's `rawCredentialCarveOut` agree | a pin asserting set equality; adding to one without the other reds |
| T11 | The pre-step-4 rejection paths still answer un-collapsed | malformed, duplicate and auth-denied submissions of `ClaimIdentity` keep their real codes — the behaviour the corrected doc now describes |

**Mutation discipline:** T2's claim is about *where* the check sits, so the proof is a **move** — relocate
the check after the receipt stamp and assert T5 reds. **Fixture discipline:** T5 must run under the
concurrent-submission load the `Q` constant was sized under; an unloaded run passes vacuously, since the
unloaded worst case is ~2.9 ms.

---

## 10. Contract surface

**No frozen-contract change.** Contract #9 §9.3 promises the wire-shape collapse and nothing else — no
timing, no quantum, no payload bound (ledger row 14). The release quantum is an implementation choice
and stays one. The payload cap is a target-independent admission bound of the same class as the
transport's own `max_payload`, and Contract #2 §2.1 leaves envelope sizing to the transport.

**One adjacency, adjudicated rather than assumed:** §3.1's exception — a cap refusal answers with its
own code rather than collapsing to `ClaimKeyInvalid` — sits against §9.3's *"All failure modes collapse
to the generic `ClaimKeyInvalid` reply code."* Read literally, a size refusal is a failure mode. The
argument for the exception is that §9.3's purpose is anti-**enumeration** and a length test enumerates
nothing; the design keeps the exception to exactly that predicate (§3.1) and pins it with T3/T4. **If
Andrew reads §9.3 as unconditional, the alternative is to collapse the size refusal too** — the
security property is identical either way, and the cost is that an oversized claim is
indistinguishable from a wrong key, which is a real developer-experience regression on an interactive
endpoint. Flagged rather than decided unilaterally, because it is a reading of frozen prose.

**Component doc** (`docs/components/processor.md`, not frozen): the NFR-S6 section gains the cap and
the corrected argument.

---

## 11. Corrections this design records

1. **The demand row over-states the severity.** Not "one bit per request" — the per-cause delta
   (~0.3 ms) is far below the loaded p99 (~17 ms), so the channel is statistical, recovered by
   averaging the release index. Real, worth closing, narrower than filed (§1.1).
2. **The proposed fork is false** (§4): unconditional release with a drop past `Q` renames the channel
   into the drop indicator rather than closing it, and with the cap it is obtained for free.
3. **Four gated behaviours, not three** — the row misses the Health-KV `internal-fault` outcome
   (ledger row 8).
4. **Both surviving doc comments are stale about the vector**, not only wrong about the conclusion:
   they name declared-read padding, which `c69aa4a4` closed (§1.2).
5. **The design may refute itself at Phase 0** (§7), and that outcome is stated in advance as a
   success rather than discovered as a failure.
6. **The cited 4096-per-field precedent does not transfer** — per-field, package-script, work-bounding;
   this cap is whole-payload, Go-layer, time-bounding (§7).
7. **The "explicitly refuse `InputSchema`" refusal is not inherited** from either prior fire — it
   originates in today's triage and is adjudicated here for the first time (§5 C).
8. **`C` is three decodes, not one** (ledger row 11a), which the measurement must reflect.
9. **NFR-S6's requirement text is broader than the claim path** — *"Auth denial responses … do not
   expose internal permission graph structure beyond what the requesting actor requires"*
   (`_bmad-output/planning-artifacts/epics/index.md:129`). Contract #9 §9.3 is its claim-path
   instance, and this design stays inside that instance.

---

## 12. Executable censuses

**C1 — the gated behaviours.** `grep -rn 'isNFRS6Operation' --include='*.go' internal/ | grep -v _test`
— *run this fire:* the definition plus **three** call sites (`step4_hydrate.go:227`,
`commit_path.go:1032`, `commit_path.go:1125`) carrying four behaviours.

**C2 — existing payload bounds.** `grep -rn 'max_payload\|MaxPayload' --include='*.go' . | grep -v
vendor` — *run this fire:* NATS `max_payload`, 1 MiB by default, is the only bound on `env.Payload`;
`internal/substrate/batch.go:222-226` reads it live rather than hardcoding, so a production override
moves it.

**C3 — the sibling core-owned op-name maps** (pins §3.4's "fourth instance" claim):
`grep -rn 'privilegedLaneAllowlist\|reservedOperationTypes\|rawCredentialCarveOut' --include='*.go'
internal/ | grep -v _test`. Each is a scope line item for the "core owns the policy" argument.

**C4 — largest legitimate claim payload** (floors the cap). Enumerate the `ClaimIdentity` and
`CompleteCredentialLink` submission sites and record the largest payload any of them builds. Must be
run before the cap value is fixed; a cap below a live submitter's payload is an outage, not a defence.
