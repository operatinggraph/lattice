# Egress-read declaration authority — who may name a key the platform will carry out of the platform

**Status: 📐 awaiting-Andrew (ratification)** · Designer fire 2026-09-01 · Winston
**Frozen-contract edit staged UNCOMMITTED in `main`:** `docs/contracts/02-operation-envelope.md` — the §2.3
`contextHint` field row and the §2.5 class-(f) row. The diff is the proposal. **No architectural fork.**

**Board row:** *[Processor] `contextHint.egressReads` is submitter wire no authorization step inspects*
(filed ★★★ / M by `declared-path-reads-design.md` §For-Andrew (iii);
`no-pattern: authorization-time egress-declaration scoping`). **This fire re-derives the severity, the
prescription, and the payoff. Two of the three did not survive grounding — §1 and §10.1.**

**Censuses:** three independent read-only passes, run **concurrently with the drafting** and briefed to
**falsify** the row (reachability · exploitation chain + corpus · prior rulings). All three returned
material corrections; §11 records what each changed.

---

## For Andrew

**What this is, in three lines.** `contextHint.egressReads` is the only class of declared read whose data
the platform will decrypt and hand to an outside party — the commit-path guard says so in those words
(`step6_validate.go:153-159`). Nothing refuses a declaration: parse checks a count and a disjointness rule,
step 3 never reads `contextHint`, and hydration mints a MAC'd `$sensitiveRef` over **any** identity's aspect
with no ownership test. The proposal is one membership test at the step-4 head — **an egress declaration is
admitted only on an envelope submitted by a primordial platform engine** — plus deleting the parameter that
lets any Loom call site supply one.

**Why it is here and not Winston-adjudicated.** It edits a frozen contract, and the sentence it edits is a
**security claim that is false**. Staged uncommitted in `main`.

**Four things want your attention. None is a fork; three are corrections I owe you.**

**(i) The row's headline is wrong, and I filed it.** I wrote *"any actor with any non-NFR-S6 grant can name
any identity's sensitive aspect."* **No client wire struct in the tree has an `egressReads` field at all** —
not the Gateway (`gateway.go:404-426`, `:815-833`), not Facet, not the Edge browser host, not Loupe's op
builder, not `lattice op submit` — so `encoding/json` drops a caller's value before any Go code runs; and
the four vertical apps hold **no `ops.>` publish grant** (`natsperm/matrix.go:657-687`). A Gateway caller
cannot reach this channel at all.

**(ii) The exfiltration end is already closed by a shipped gate that nobody connected to this row —
including me when I filed it.** `scripts/lint-conventions.go`'s `checkPrimordialActorGuard`
(`:1730-1870`) **structurally requires** every `external.<adapter>` emission in a `packages/**` script to sit
inside its own `if op.actor != primordialActor["<engine>"]:` refusal, before the branch's first data access.
It is fail-closed, it refuses six near-miss spellings by name, and it binds all seven external-emitting ops
(3 Loom-relayed, 4 Weaver-dispatched). So the row's *"contained by a package-authored script guard, not a
platform rule"* is **half wrong**: the guard is package-authored and **an author cannot omit it**, which is
a platform rule in the only sense that matters. **This substantially deflates the design I set out to
write, and §10.1 states the reduced payoff honestly rather than restating the severity I filed.**

**(iii) What genuinely remains is one sentence long, and it is the kind of thing this platform has been
bitten by twice.** For a Gateway-authenticated caller, **the entire containment is that four wire structs
happen not to have a field.** Nothing refuses the declaration, and nothing gates adding the field — a
well-meant future change ("let clients declare egress reads for their own subject") opens the channel with
no gate anywhere raising a hand, because the lint governs *scripts* and the shipped closed set governs
*two operations*. That is a guarantee held by accident of corpus shape, and the fix costs one membership
test over a map already threaded to the same struct.

**(iv) What this does NOT defend against, stated plainly.** An `ops.>`-credential holder can set
`--actor` to anything (`cmd/lattice/op/op.go:158`), so they can claim Loom's actor and pass this
predicate — and can already do so for every authorization decision the platform makes. **This design does
not narrow that position and does not claim to.** It closes the *dispatcher* surface, not the
raw-publisher one; the raw-publisher position is `app-tier-transport-read-scope-design.md`'s shelved
territory.

**What I am NOT proposing.** Not `readScope` (shelved 2026-08-06 — §8.2 shows this is a different altitude
and does not invoke its revive trigger). No new envelope field, no wire change, no new declaration surface
on the DDL or the descriptor, no change to any crypto, MAC, custody or bridge rule, and no rule over
`contextHint.reads` — that class's harm is already caught by the step-6 guard on the egress plane, and
widening to cover it is exactly the shape you held.

---

## 1. The demand, clause by clause

The row, verbatim:

> **[Processor] `contextHint.egressReads` is submitter wire no authorization step inspects** — *"Any actor
> with any non-NFR-S6 grant can name any identity's sensitive aspect and get a MAC'd `$sensitiveRef`: parse
> checks count + disjointness only, step 3 never reads `contextHint`. The live consumer is contained by a
> package-authored script guard, not a platform rule."*
> `no-pattern: authorization-time egress-declaration scoping`

| Clause | Verdict | Evidence |
|---|---|---|
| *"parse checks count + disjointness only"* | **CONFIRMED** | `opwire.go:303-306` (the summed `MaxDeclaredReads` ceiling), `:315-335` (the reads/optionalReads ⊓ egressReads ambiguity check). Nothing else — not even key-grammar validation, which happens later at `step4_hydrate.go:280`. |
| *"step 3 never reads `contextHint`"* | **CONFIRMED** | `grep -n "ContextHint\|contextHint" internal/processor/step3_*.go` → **zero**. Step 3 matches on `{operationType, actor-derived capability doc, authContext, lane}`; `case "any"` (`step3_auth_capability.go:648-655`) runs the lane gate and returns authorized with no target or context test. |
| *"…and get a MAC'd `$sensitiveRef`"* over **any** identity's aspect | **CONFIRMED** | `step4_hydrate.go:372-416` hydrates every egress key with `decryptSensitiveDoc(..., egress=true, ...)`. The only gates before the mint are tombstone (`sensitive_decrypt.go:172-174`) and *holder **type** is `identity`* (`:381-392`). The MAC covers `{ref, requestId, ciphertext}` (`:238-271`) — **no actor/holder equality test anywhere.** |
| *"Any actor with any non-NFR-S6 grant"* | **REFUTED as filed** | §2.2. No client wire struct carries the field; the app tier holds no `ops.>` publish. The reachable position is an `ops.>`-credential holder on a hand-built client. |
| *"…contained by a package-authored script guard, not a platform rule"* | **HALF REFUTED, and this is the important one** | §2.6. The guard is package-authored **and lint-enforced platform-wide**, fail-closed, on the exact hazard shape, across all seven external-emitting ops. An author cannot omit it. |
| `no-pattern: authorization-time egress-declaration scoping` | **PARTLY DISSOLVED** | The primitive largely exists and is **not** at authorization time: `refuseUndeclaredContextHint` closes a declared set at the **step-4 head**, refuses `egressReads` unconditionally, and is gated to two operations (`nfr_s6_wire_shape.go:32-35`). §3 generalizes that one arm; §4 row 2 rejects the authorization-time reading on its merits. |

**The demand that survives, restated honestly:** *the declaration end of the platform's only exfiltration
transport has no admission rule at all, and the single fact containing it from the API boundary is the
absence of a field in four Go structs — which no gate protects.*

---

## 2. Grounding ledger

### 2.1 The channel, end to end

| Step | What happens to `contextHint.egressReads` | Cite |
|---|---|---|
| Parse | count-sum ≤ 1000; disjoint from `reads`/`optionalReads`. Nothing else. | `opwire.go:106`, `:303-306`, `:315-335` |
| Step 3 (authorize) | not read | `internal/processor/step3_*.go` — zero references |
| Step 4 head | **NFR-S6 ops only**: any non-empty `egressReads` refuses the operation | `step4_hydrate.go:229-233` → `descriptor_floor.go:511-543`, refusal at `:535-537` |
| Step 4 floor | a descriptor may mark an egress key `EgressAbsenceTolerant` — **absence** only; presence untouched, deliberately, since moving the key would swap a ref for plaintext | `descriptor_floor.go:78-99` |
| Step 4 hydrate | present ⇒ `decryptSensitiveDoc(egress=true)`; sensitive ⇒ `$sensitiveRef{ref, ciphertext, mac, field}`; non-sensitive ⇒ plain | `step4_hydrate.go:372-416`; `sensitive_decrypt.go:154-157`, `:238-271` |
| Step 4 gates | tombstoned ⇒ refuse; key-holder **type** ≠ `identity` ⇒ typed refusal. **No holder-identity test.** | `sensitive_decrypt.go:172-174`, `:381-392` |
| Step 6 validate | plaintext-sensitive + `external.*` event ⇒ **reject**. A `$sensitiveRef` is **admitted**. | `step6_validate.go:161-179` |
| Emission (script) | an `external.<adapter>` emission must sit inside a primordial-actor refusal — **lint-enforced** | `scripts/lint-conventions.go:1730-1870` |
| Bridge | every `$sensitiveRef` replaced by vendor-ready plaintext at egress | `internal/bridge/egress.go:60-113`, `dispatch.go:194-228` |

### 2.2 Reachability — the census that refutes the row's headline

Briefed to falsify. Per producer:

| Producer | Can a caller place an `egressReads` key? | Cite |
|---|---|---|
| Gateway `POST /v1/operations` | **No — the field does not exist on the wire struct.** `operationRequest` / `operationRequestContext` carry `Reads`, `OptionalReads`, `Enumerations`; the envelope literal sets those three. The Gateway also stamps the **verified** actor, never anything from the request. | `gateway.go:404-426`, `:815-833`, `:832`, `:786` |
| Facet `/api/enqueue` | No — same shape | `cmd/facet/server.go:152-169` |
| Edge browser host | No — same shape | `internal/edge/browser/host.go:337-369` |
| Loupe operator op builder | No — same shape | `cmd/loupe/op.go:54-67`, `:82-89` |
| `lattice op submit` | No `--egress-reads` flag — **but `--actor` is free-form** | `cmd/lattice/op/op.go:100-107`, `:158` |
| Vertical apps (LoftSpace/Clinic/Café/Wellness) | No — **hold no `ops.>` publish grant** | `natsperm/matrix.go:657-687` |
| **Loom actuator** | **Yes — the sole producer.** Keys are `subjectKey + "." + aspect`, parsed from an *installed pattern's* step params; actor is Loom's fixed service key. | `loom/engine.go:1085-1115`, `loom/externaltask_params.go:42-86`, `loom/actuator.go:103-119` |
| A raw NATS client under an `ops.>` credential | **Yes — arbitrary keys, and arbitrary `actor`.** | `natsperm/matrix.go:422-698` |

**So the containment is a wire-struct field-set.** Not one line refuses a caller's egress declaration
outside the two NFR-S6 ops; what refuses it is that no shipped client can express it, and **no gate
protects that property**. Adding an `EgressReads` field to `operationRequestContext` is a three-line,
plausible, well-intentioned change that opens the channel silently.

### 2.3 Why class (f) is a privilege, and the contract sentence is false

The founding rationale (`sensitive-param-egress-design.md` §3.1, ratified 2026-07-10):

> *"**It grants nothing.** `egressReads` is strictly less information than `reads` (refs instead of
> plaintext for sensitive keys, identical otherwise) — a submitter self-restriction, not a new authz
> surface. That is why it can be an open declaration like the other classes."*

Sound about the **script's** working set; unsound about the **outcome**:

- Class (a)/(d) plaintext + an `external.*` event ⇒ the operation is **rejected**
  (`step6_validate.go:161-179`), in the guard's own words: *"sensitive data may reach an external event
  only as a ref via `contextHint.egressReads`."*
- Class (f) ref + an `external.*` event ⇒ **admitted**, and the bridge substitutes plaintext at the vendor
  boundary (`bridge/egress.go:109`).

Class (f) is therefore **not** a weaker form of class (a). **It is the only class with an exfiltration
transport**, and the sentence licensing an open declaration is the sentence that gets it backwards. The
prose lives in a **frozen contract**, which is the worse direction: the next fire reads the contract.

### 2.4 The trust root already exists and is already threaded here

`PrimordialActors` is the bootstrap-seeded identity-key set of the trusted platform engines, keyed by
engine name (`commit_path.go:1186-1195`). It is already a field on the **Hydrator**
(`step4_hydrate.go:51-57`), already passed into `deriveReads` (`:235`), populated once at pipeline wiring
(`commit_path.go:1273`), and already surfaced to Starlark as `primordialActor` (`compiled_script.go:175`)
— where it exists for *precisely this purpose*, in its own words: *"an op whose `Scope:"any"` grant is
broader than its one-engine semantics pins `op.actor` against it."* Its unset behaviour is documented
fail-closed: a missing name binds the empty string, so the comparison rejects every real actor.

**The design is not "add a trust root". It is "let the Processor use the one already in its own struct."**

### 2.5 The shipped closed set, and why it reaches two operations

`refuseUndeclaredContextHint` is a complete, reviewed, fail-closed closed-set enforcement at the step-4
head. Its egress arm is not even a set test — it refuses **any** non-empty `egressReads`
(`descriptor_floor.go:535-537`). It is gated on `isNFRS6Operation` (`step4_hydrate.go:229`), a set of two
(`nfr_s6_wire_shape.go:32-35`), because **timing equalization** is what it was built for. Nothing about
the egress arm is specific to timing equalization.

**A caution that kills the obvious reuse.** The neighbouring `admits()` set deliberately honours
`{payload.<field>}` templates, and says why: *"Steering the payload field steers WHICH key is admitted,
never HOW MANY"* (`descriptor_floor.go:313-325`) — correct for a **count** control and a **bypass** for an
authorization control, because the submitter writes the payload. So "let the descriptor declare an egress
template and reuse `resolveAdmitted`" — the first shape a reader reaches for, and the one an independent
prior-art census ranked as the cheapest host — is **unsound**. §4 row 4.

### 2.6 The gate I missed when I filed the row

`scripts/lint-conventions.go`'s `checkPrimordialActorGuard` (`:1730-1870`) requires every
`"class": "external.` emission in a `packages/**` script to sit **directly inside its own
`if ot == "<Op>":` dispatch branch**, guarded by the canonical statement

```
if op.actor != primordialActor["<engine>"]:
    fail(...)
```

on its own non-comment line, with a `fail(` in its body, **positioned before the earlier of the branch's
first data access and its emission** — because *"a guard that runs after the subject has been read still
lets an arbitrary caller use the op as a read — and, for a sensitive aspect, a decrypt — oracle even when
nothing is sent."* The gate refuses six near-miss spellings by name (a mention in a comment, a dead branch,
a guard placed after the read, a body that does not refuse, a file-wide `primordialActor = {...}` shadow,
an emission in a helper), each pinned by its own self-test.

Two documented limitations that bound what it covers, both fail-closed and both relevant here:

- **It fires only for ops the package grants at `Scope:"any"`** (`packageOpScopeIsAny`). An external-emitting
  op granted at `"self"` or `"service"` is exempt. **Census: the exempt class is empty today** (§6.3) — all
  seven emitters are `Scope:"any"` — but that is a fact about the corpus, not about the gate.
- **It scans `packages/**`.** A Starlark script that never passes through the repo — an AI-authored
  capability artifact — is outside it, which is `authored-artifact-admission-model-design.md`'s shelved
  territory (dormant behind `BRIDGE_CAPABILITY_AUTHOR=real`), not this design's.

**This is the single most load-bearing correction in the fire.** The row I filed said the containment was a
package-authored guard; it is a package-authored guard the author cannot omit. The exfiltration end is
closed. What remains is the declaration end, and §10.1 prices it honestly.

---

## 3. The shape

**One sentence: an egress declaration is the platform speaking to itself, so only the platform's own engines
may make one — and no Loom call site should be *able* to make one by accident.**

### 3.1 Increment 1 — the admission predicate (Processor, step-4 head)

At the head of step 4's declaration block, immediately after `declaredReadsFromEnvelope` and **before** the
descriptor floor resolves:

```
if len(env.ContextHint.EgressReads) > 0 && !primordialEngineActor(env.Actor, h.PrimordialActors) {
        → *HydrationError{Code: "EgressDeclarationUnauthorized"}   // terminal, no key echoed
}
```

Four properties, each deliberate:

- **The admitted set is `PrimordialActors`, not a hardcoded `"loom"`.** The set is bootstrap-seeded,
  platform-owned, and named in no envelope field; adding a member is a kernel change with its own gates
  (`bootstrap/primordial.go`, `verify-kernel`). It also **matches the shipped guard corpus**: the seven
  external-emitting ops pin against `primordialActor["loom"]` *or* `primordialActor["weaver"]` (§6.2), so
  "a platform engine" is already this codebase's unit for the same rule, and hardcoding one engine name in
  the Processor would re-import the shape-dependence Increment 2 exists to remove.
  **Unset ⇒ empty ⇒ every actor refused**, which is the field's own documented direction, not a new mode.
- **It runs before the floor and before `derive_reads`, and its subject is `env.ContextHint` alone.** The
  floor's egress arm only marks absence-tolerance and adds no key; a derivation **cannot** produce an
  `egressReads` key at all (Contract #2 §2.5 class (g)). So the envelope's own list is the complete subject
  and nothing computed later can widen it.
- **The fault echoes no key**, mirroring `refuseUndeclaredContextHint`'s reasoning verbatim: the refused key
  is the submitter's own probe, and echoing it answers an existence question over someone else's graph. The
  Warn carries `operationType`, `requestId`, `actor` and the declaration count.
- **It is a strict shrink.** Every live egress declaration in the tree is Loom-relayed (§6.2), so no
  legitimate submission loses.

**What it binds, and what it does not.** It binds **who**. It binds **what** only transitively — the one
admitted producer builds keys as `subjectKey + "." + aspect` from an installed pattern
(`externaltask_params.go:42-86`), so content is subject-rooted by construction. **That is a guarantee held
by the shape of one function, and a shape-held guarantee must be made mechanism-dependent in the same
design.** That is Increment 2.

### 3.2 Increment 2 — remove the parameter, don't lint it (Loom)

`buildOutbox(..., reads, optionalReads []string, enumerations []Enumeration, egressReads []string)`
(`loom/actuator.go:143`) offers an egress list to **every** outbox call site, of which exactly one has any
business supplying it; the others pass `nil` by convention.

**Drop the `egressReads` parameter from `buildOutbox`.** The externalTask arm gets a dedicated constructor
taking `(subjectKey, params)` that calls `inferExternalTaskReads` itself, so the derivation and the outbox
construction become one thing that cannot be separated. Every other producer then *cannot* express an
egress declaration — not "does not", **cannot**.

A structural fail-closed rather than a lint, which is the preferred direction when one is available (a lint
would be the fallback if the parameter had a second legitimate supplier; it does not). It is a net
**reduction**: one parameter and its `nil` arguments removed.

### 3.3 What the two increments buy — traced against §2.6, not asserted

| Scenario | Today | After |
|---|---|---|
| A future `EgressReads` field added to a client wire struct (Gateway/Facet/Edge/Loupe) | channel opens silently — no gate anywhere is a subject for it | **the declaration is refused at the step-4 head**; the change becomes a no-op instead of a breach |
| A **new dispatcher** forwards a caller's egress declaration | hydration mints a ref for whatever key was named; the emission gate then stands between it and the wire | refused before the mint — the decrypt and the MAC never happen, and no audit record lands against an uninvolved identity |
| A **new Loom producer** supplies a non-subject-rooted egress list | possible — `buildOutbox` takes the list | impossible — no parameter to pass it through |
| An external-emitting op granted at `Scope:"self"`/`"service"` | **exempt from the lint gate** (`packageOpScopeIsAny`); nothing else refuses the declaration | the declaration is refused regardless of the op's scope — the predicate has no scope conjunct |
| An **`ops.>`-credential holder** forges `actor: <Loom>` | full access | **unchanged — this design does not defend against it** (For Andrew (iv)) |
| A caller declares a sensitive key under `reads` instead | plaintext into the script; **rejected** at step 6 if an `external.*` event is emitted | unchanged — out of scope (§8.2, §10.5) |

Rows 1 and 4 are the payoff. Rows 2 and 3 are hygiene. Row 5 is stated so the payoff is not overclaimed.

### 3.4 What is deliberately not removed

The seven package-authored actor pins **stay**, and the lint that enforces them stays. They guard the
*whole op branch* — an operator-role holder submitting `CreateLeaseDocInstance` at all is the harm they
name, not merely the egress read — so removing one on the strength of this design would delete a guard
whose reason this design does not cover. (The "remove the write you are replacing" rule is checked and
does **not** apply: this replaces nothing; it adds an independent conjunct one step earlier.)

---

## 4. Alternatives

**Row 1 is deletion, written first.**

| # | Alternative | Verdict |
|---|---|---|
| **1** | **Do not have this thing.** Leave the declaration channel open; containment stays "no client wire struct has the field" plus the shipped emission gate. | **The closest call in the table, and §10.1 argues it rather than dismissing it.** After §2.6 this is a defensible position: the exfiltration end is closed by a fail-closed lint, so the residual harm of an unauthorized declaration is a wasted decrypt and an audit record against an uninvolved identity — Andrew's own 2026-08-06 words for the sibling face, *"an audit smell, not a leak."* **Rejected on one ground only:** the property doing the containment at the API boundary is the absence of a field, and nothing gates adding it. |
| **1b** | **Delete `contextHint.egressReads` from the wire and re-derive it Processor-side** from the instanceOp's `payload.subjectKey` + `payload.params`, which the envelope already carries (`loom/engine.go:1085-1097`). | **Rejected on layering, and it would foreclose a pending design.** The Processor would have to know `orchestration-base`'s externalTask payload shape and re-implement `inferExternalTaskReads`' grammar — a package contract imported into the kernel, two copies of one parser to keep in lockstep. It also covers only externalTask instanceOps: `declared-path-reads-design.md`'s class (h) resolves egress keys the Processor computes from a declared path, which no re-derivation from `params` can produce. **The idea survives as Increment 2**, applied inside Loom where it costs nothing. |
| **2** | Read the row's prescription literally: scope the declaration **at step 3**. | **Rejected — wrong subject, wrong seam.** Step 3 has never inspected `contextHint` and must not start: a class-(g) derived key is not on the envelope, so a step-3 rule would govern a set that is not yet the set. The shipped closed-set precedent runs at the step-4 head *for that stated reason* (`step4_hydrate.go:216-228`) — after authorization, before the first Core KV GET. The `no-pattern:` was solution-shaped: it named the surface a *dispatch-time* answer would need. |
| **3** | Bind the egress key to the operation's **step-3-validated target** — "subject-binding", the shape `sensitive-ref-mac-provenance-design.md` §9 flagged for a future design. | **Rejected: the guard would be inert.** Step 3 validates a target on two auth paths only (`operation_context.go:46-58`) and **not** on `Scope:"any"` — which is the grant every one of the seven external-emitting ops holds (§6.2). A conjunct on a field the relevant arm never sets cannot fire, and would pass vacuously for every consumer it exists for. The flagged concern is real; this answers it by binding the declaring **actor** instead of the declared **target**. |
| **4** | Let the **descriptor** declare an egress template and reuse `resolveAdmitted` as an allow-list. *(The prior-art census ranked this the cheapest host.)* | **Rejected — the mechanism cannot be reshaped this way.** `admits()` deliberately honours `{payload.<field>}` templates because its job is a **count** control (`descriptor_floor.go:313-325`); as an authorization control that is a submitter-steerable admit set, which the same file refuses by name eleven lines earlier (*"An exclusion set the attacker can address is not a precedence rule, it is a bypass"*, `:107-114`). Making it non-steerable means adopting `resolveDescriptorRequired`'s stricter vocabulary — at which point every live egress consumer's payload-rooted template admits nothing and every submission faults. It is also the larger build: a new `OpDispatchSpec` field, install validation, a client vocabulary, a migration. |
| **5** | Revive `readScope` (Increments 2–3 of `declared-read-scope-authorization-design.md`) and cover all three classes. | **Rejected — this is the shape held on 2026-08-06 as disproportionate, and nothing here makes it proportionate.** Its revive trigger is *"a second unguarded payload-named sensitive read … evidence that per-op ownership guards do not scale."* Not fired: this is not a payload-named read, and §2.6 shows the per-op guards did not fail — they are lint-enforced. §8.2. |
| **6** | Guard at the **bridge**: refuse to unwrap a ref whose holder is unrelated to the event's subject. | **Rejected — the enforcement point does not follow the threat.** The harm is the mint, inside a committed operation whose reply carries the ref; a bridge refusal leaves a valid MAC'd ref in a committed event and in the requester's reply, and only stops the last hop. It is also more machinery (a relationship walk on the hot egress path) for a later, weaker gate. |
| **7** | Extend `checkPrimordialActorGuard` to require a guard on every op that *declares* an egress read. | **Rejected — the lint cannot see the envelope.** It scans `packages/**` scripts; a declaration lives on the wire, authored by a Go dispatcher the gate does not read. Extending it to `internal/`+`cmd/` dispatchers would be a second, weaker copy of Increment 1 that a non-repo producer escapes entirely. Where the Processor can refuse, a lint is the fallback, not the answer. |

**Re-asking the discipline question — could a variant of a rejected row beat the recommendation?** Row 1b
did, in part: its core move — *make the illegitimate declaration inexpressible rather than refused* — is
better than a check, and is adopted as Increment 2 where it costs nothing. Row 3's concern is adopted by a
different mechanism. Row 1 came closest to winning outright and is the honest fallback in §10.1.

**Running each rejection back against my own recommendation.** Row 4's objection is *"the admit set is
submitter-steerable"* — is mine? `PrimordialActors` is bootstrap-seeded and appears in no envelope field;
`env.Actor` is the value step 3 already authorizes on, so claiming an engine's actor is not a bypass of
this rule but a compromise of the whole authorization plane (For Andrew (iv) says so plainly rather than
hiding it). Row 6's objection is *"the enforcement point does not follow the threat"* — mine sits at the
mint. Row 3's objection is *"the guard cannot fire"* — mine's input (`env.Actor`) is required on every
envelope (`opwire.go:288-290`), so it is set on every arm. Row 7's objection is *"it cannot see the
envelope"* — mine reads the envelope directly.

---

## 5. State, lifetime, and the predicate table

### 5.1 New state

**None**, and this is the point of the shape rather than an omission. `PrimordialActors` already exists on
the Hydrator (`step4_hydrate.go:51-57`), is populated once at pipeline wiring (`commit_path.go:1273`), and
lives for the process lifetime. Increment 1 adds a read of it; Increment 2 removes a parameter. There is no
registry, cache, latch, watch, or accumulated set, so there is no lifetime table — stated so a reviewer can
check the claim rather than assume it.

### 5.2 The admission predicate, per state

| # | Envelope state | Outcome | Why |
|---|---|---|---|
| 1 | no `contextHint` | admit | nothing declared |
| 2 | `contextHint` present, `egressReads` empty/absent | admit | the rule has no subject |
| 3 | `egressReads` non-empty, actor ∈ `PrimordialActors` | admit | the platform speaking to itself; every live consumer is here |
| 4 | `egressReads` non-empty, actor ∉ `PrimordialActors` | **refuse, terminal, no key echoed** | the harm |
| 5 | `egressReads` non-empty, `PrimordialActors` nil/unset | **refuse** | the field's own documented fail-closed direction (`script_context.go:111-117`); a pipeline driving an egress op must wire it, exactly as one driving a primordial-pinned op already must |
| 6 | `egressReads` non-empty **and** the op is NFR-S6 | refuse (unchanged) | the existing wholesale refusal still runs and still wins; the two agree and neither masks the other |
| 7 | `egressReads` non-empty, actor is an engine, key names a **non-sensitive** aspect | admit | unchanged — a non-sensitive egress key hydrates like a plain read (`sensitive_decrypt.go:154-157`); no disposition changes |
| 8 | OCC retry of an admitted envelope | admit, identically | a pure function of the envelope and a process-lifetime map — no re-derivation, no drift across attempts |
| 9 | Loom relay redelivers the same instanceOp envelope | admit, identically | same inputs; the predicate holds no per-request state |

Row 5 is the migration's whole cost; §6.4 sizes it. Row 4's outcome is deliberately **refuse the
operation**, not *drop the key*: dropping would hand the script a silent `None` where its author declared a
required read, which is the failure the §2.5 authoring rule exists to prevent.

---

## 6. Executable censuses

Each ships as the command that derives it. **Phase 0 re-runs all four, briefed to falsify.**

### 6.1 Producers of a populated `egressReads` — expect exactly one

```sh
grep -rn "EgressReads:" --include='*.go' internal/ cmd/ packages/ scripts/ | grep -v '_test\.go'
```
Expected: `internal/loom/actuator.go` (the outbox record + the relay envelope) and
`internal/processor/derive_reads.go` (pass-through of what the envelope carried). **Nothing else.** A third
hit means Increment 2's structural removal has a second supplier and §3.2 must be re-derived before build.

### 6.2 Live consumers — expect both to be Loom instanceOps at `Scope:"any"`

```sh
grep -rn "egressReads" packages/ --include='*.go' | grep -v '_test\.go'
grep -rn 'if op\.actor != primordialActor\[' packages/ --include='*.go'
```
Expected consumers: `orchestration-base/external_params.go` (the shared resolver + its
`# read-posture: (f)` annotation) and `lease-signing/{leasedoc_scripts,scripts,patterns,lenses}.go`. Both
consuming ops (`CreateLeaseDocInstance`, `CreateLeaseServiceInstance`) are the `InstanceOp` of an
externalTask step (`lease-signing/patterns.go:38-60`), submitted with Loom's actor
(`loom/engine.go:1115`). Expected guard corpus: **seven** ops — three pinned to `primordialActor["loom"]`
(`CreateLeaseServiceInstance`, `CreateLeaseDocInstance`, `CreateAuthoringClaim`) and four to
`primordialActor["weaver"]` (`CreateAugurReasoningClaim`, `RecordAppointmentReminder`,
`RecordFollowUpReminder`, `RecordBookingReminder`). **A consumer that is not a Loom instanceOp falsifies
"strict shrink" and blocks Increment 1.**

### 6.3 The lint gate's exempt class — expect it to be empty, and prove it

```sh
# every packages/ script emitting an external.<adapter> event, and the Scope of the op it sits in
grep -rn '"class": "external\.' packages/ --include='*.go' | grep -v '_test\.go'
```
Expected: every emission's enclosing operation is granted at `Scope:"any"`, so
`checkPrimordialActorGuard`'s `packageOpScopeIsAny` conjunct never skips one. **If this census returns a
`Scope:"self"`/`"service"` emitter, the lint gate is exempting a live external emitter and this design's
row-4 payoff (§3.3) is not hypothetical — say so in the build note and re-derive the severity upward.**

### 6.4 The test migration — the honest size

```sh
grep -rln "EgressReads\|egressReads" --include='*_test.go' . | grep -v _bmad     # 17 files
grep -rn  "EgressReads:" --include='*_test.go' . | wc -l                          # 24 sites
```
Every test hydrating an egress key must now submit under a primordial actor **or** wire `PrimordialActors`
on the Hydrator it builds — predicate row 5. **The build must not satisfy this by giving the predicate a
test-only arm.** A `testutil` helper that wires the primordial map is the sanctioned route; Increment 1
owns adding it.

### 6.5 The refusal's own pin

Increment 1 ships a negative test (non-primordial actor ⇒ terminal fault, **and** no key reaches the
caller) **and its positive vector** (the Loom path still hydrates the ref) — so the negative cannot pass
vacuously, which is exactly how a prior fire's negative test passed for the wrong reason.

---

## 7. Contract surface

**Contract #2 `02-operation-envelope.md` — CHANGE, staged uncommitted.** Two rows, one promise:

- **§2.5 class (f).** Strike *"Strictly less information than (a) — a self-restriction, not a privilege"* —
  §2.3 shows it is false — and state the observable promise: an egress declaration is admitted only on an
  operation submitted by one of the platform's own orchestration engines; on any other operation it is
  refused, terminally, without naming the key.
- **§2.3 field table**, the `contextHint` row: the same one clause, so a reader of the field table is not
  told the class is open.

Both are at **promise altitude** — a wire shape and a refusal semantic, no internal names, no mechanism
narration, nothing a pure refactor could falsify. **Contract #10 §10.5 needs no edit:** it already
describes Loom as the party that declares template-inferred aspect keys under `egressReads`, which becomes
the rule rather than a description of the only current caller.

**Interaction with the other staged contract edit.** `declared-path-reads-design.md` (📐 awaiting-Andrew)
has its own uncommitted §2.5 edit adding class (h) in the same file. The two do not overlap textually
(class (h) is a new row plus the ceiling paragraph; this is the class-(f) row plus the field table), but
they land in one file — **whichever is ratified second rebases onto the first.** They agree substantively:
class-(h) keys are resolved by the Processor onto a **Loom-submitted** envelope, so this predicate admits
them with no special case (§8.3).

---

## 8. Reconciliation with the existing mental model

### 8.1 "Didn't we already close this?"

**Mostly, at the other end, and that is §2.6's correction.** Four things exist and here is exactly what
each covers. **`checkPrimordialActorGuard`** closes the *emission* end for every `packages/**` script whose
op is granted `Scope:"any"` — fail-closed, six near-misses refused by name. **`refuseUndeclaredContextHint`**
closes an egress declaration completely — for two operations, because it was built for timing equalization.
**The descriptor floor** touches `egressReads` only to make an *absence* tolerant; it never refuses
membership, and the file explains why moving an egress key would be the dangerous direction
(`descriptor_floor.go:78-99`). **Decision 4 of the shelved read-scope design** (`acecf6f9`) shipped the
`primordialActor` global and the seven per-op pins — the right instinct at step **5**, after hydration has
already minted the ref, as its own §12 says. **What none of them is a subject for: the declaration itself,
on the other 100-odd operations.**

### 8.2 "Isn't this the thing I held as disproportionate?"

No, and the difference is worth being precise about. `readScope` was a **general per-operation declared-key
allow-list** across all three read classes, declared on the DDL, checked at hydration *and* at the live
`kv.Read` seam, default-deny — sized L–XL, priced against a harm the ratify session showed was contained.
Its revive trigger is *a second unguarded payload-named sensitive read*, and §2.6 shows the opposite
happened: the per-op guards were **generalized into a lint**, so they scaled.

This is one membership test over a bootstrap-seeded set, on **one** class, with **no** new declaration
surface, sized S. The case for it is not "the guards did not scale" — they did — but "the declaration end
has no rule at all, and the thing holding it shut is the absence of a struct field."

### 8.3 "Does this collide with the design that filed the row?"

`declared-path-reads-design.md` Inc 2 adds a **narrower** egress channel — a Loom step declares an
unresolved path, the Processor resolves it and adds the terminal key to that execution's egress set. Every
class-(h) key therefore arrives on a **Loom-submitted** envelope, which this predicate admits. No mechanism
conflict; the two designs' security content is one sentence read from two ends — *only a surface the
platform itself resolved may name a key for egress* — Inc 2 saying it about the **root of a traversal**,
this about the **declaring actor**. Neither depends on the other, and the pair is stronger than either,
which is why this is a separate design rather than folded into one already awaiting signature.

**One residue named so the next fire does not re-derive it as new.** I considered a *content* rule — an
egress key must be an aspect of a vertex the same envelope declares under `reads`, which is exactly Loom's
construction and would bind content mechanically. **I am not proposing it: class (h)'s terminal key sits on
a linked vertex the envelope never declares**, so the rule would refuse the pending design's whole point.

### 8.4 "Does this introduce new state?"

No — §5.1.

---

## 9. Decomposition for the Steward

Two increments, each independently shippable and green. **Increment 1 is posture-changing** (a commit-path
security predicate) and takes the full review depth; **Increment 2 is a mechanical surface removal** and
takes the Steward's ordinary sizing (`agents/steward/SKILL.md` §4). **Increment 2 does not depend on
Increment 1 and may land first.**

### Increment 1 — the admission predicate (S, posture-changing)

- `primordialEngineActor(actor string, m map[string]string) bool` beside the existing helpers; membership
  over the map's **values**, empty-string-safe (an empty `actor` or an empty map value never matches).
- The call at the head of step 4's declaration block, before `applyDescriptorFloor`.
- `HydrationError{Code: "EgressDeclarationUnauthorized"}`, terminal, no key in `details`; Warn carries
  `operationType`, `requestId`, `actor`, `declaredCount`.
- **Owns:** §6.5's negative test **and its positive vector**; the §6.4 test migration plus the `testutil`
  helper that wires `PrimordialActors`; re-running §6.1–§6.3 at Phase 0.
- **Gates:** `go build ./...`, `make vet`, `golangci-lint run ./...`, `make verify-kernel`, all
  `scripts/lint-*.go`, the full `go test ./...` — **plus the build-tagged harnesses this reaches**, which
  `go test ./...` never compiles: enumerate with
  `grep -rl "^//go:build " --include=*_test.go internal/` and run the convergence set. **
  `internal/leaseconvergence/sensitive_param_egress_test.go` is mandatory** — it is the one place the
  Loom→Processor→bridge path executes whole, and a predicate that breaks the live egress path would
  otherwise ship green.
- **Contract:** the §7 edit rides the ratification commit, not this one.

### Increment 2 — remove the `egressReads` parameter from `buildOutbox` (XS)

- Drop the parameter; give the externalTask arm a constructor taking `(subjectKey, params)` that calls
  `inferExternalTaskReads` itself. The other call sites lose a `nil`.
- **Owns:** a test asserting a non-externalTask outbox record carries no egress list — true by construction
  after this increment, so the test's job is to **fail if the parameter comes back**.

### Dossiers the build brief must copy in

`docs/components/core.md` (Processor) and `docs/components/loom.md` — the "Review keeps catching" entries
for both, per `agents/fire-brief-template.md` part 5.

---

## 10. Risks, and the things I want disagreed with

**10.1 The proportionality argument, stated so you can reject it.** After §2.6 the payoff is smaller than
the row implied and I will not dress it up. The exfiltration end is closed by a fail-closed lint over every
`packages/**` script; the API end is closed by four wire structs not having a field. So the residual harm
of an unauthorized declaration **today** is a wasted Vault MAC and a decrypt logged against an uninvolved
identity — your own 2026-08-06 words for the sibling face, *"an audit smell, not a leak."* **The case for
building anyway rests on one claim:** the property doing the containment at the API boundary is a
struct-field omission, no gate protects it, and the change that breaks it (adding an `EgressReads` field to
a client request type so callers can declare egress reads for their own subject) is a plausible, three-line,
well-intentioned edit that would open the platform's only exfiltration transport with nothing raising a
hand. Against that, one membership test over a map already on the same struct. **If you disagree, the
disagreement is with that claim, and the honest fallback is Increment 2 alone** (XS, structural, no
contract edit) **plus the §7 correction to the false contract sentence, which is owed regardless of what
you decide about Increment 1.**

**10.2 The admitted set may be one member too wide.** I admit every `PrimordialActors` member, not
`"loom"`. Only Loom has a producer today, so the rule permits Weaver and any future engine to declare an
egress read that nothing writes. I chose the wider set because the shipped guard corpus already treats
`{loom, weaver}` as the engine unit for this exact rule (§6.2), and because hardcoding an engine name in
the Processor re-creates the shape-dependence Increment 2 exists to remove. **This is the one place I would
take a correction without argument.**

**10.3 The test migration is where the predicate can be quietly weakened.** 24 sites, and the tempting fix
at each is to relax the predicate for tests. §6.4 names the sanctioned route; the review must confirm the
predicate has no test-only arm.

**10.4 I filed the row I am correcting, and two of its three claims did not survive.** The severity, the
"any actor" clause, and the `no-pattern:` prescription were all mine. The board row is corrected as part of
this fire (★★★ → ★★, clause rewritten, `no-pattern:` retired). I record it here because the *pattern* — a
filed negative or a filed severity carrying the authority of a positive fact — is the thing to distrust,
including when I am the filer. The specific miss worth naming: **I asserted "contained by a package guard,
not a platform rule" without grepping `scripts/lint-*.go`**, and the gate that refutes it is 140 lines
long, ships its own self-tests, and cites the very design my sentence cited.

**10.5 Nothing here touches `contextHint.reads`.** A caller who can reach `ops.>` can still name a
stranger's sensitive aspect there and force a plaintext decrypt. On the egress plane that is caught by the
step-6 guard; otherwise it is `readScope`'s shelved territory. Saying so is deliberate — a design that
quietly widened to cover it would be the held shape wearing a new name.

**10.6 The AI-authoring path is out of scope and stays out.** A capability artifact's Starlark never passes
through `packages/**` lint, so §2.6's emission gate does not reach it. That is
`authored-artifact-admission-model-design.md`'s shelved row (dormant behind `BRIDGE_CAPABILITY_AUTHOR=real`,
no revive trigger, your call). Increment 1 *does* reach it — an authored artifact's op is not submitted by a
primordial engine — but I am not claiming that as a payoff, because the whole surface is dormant.

---

## 11. Adversarial pass

Three read-only censuses ran **concurrently with the drafting**, each briefed to falsify rather than
confirm. What each changed:

- **Reachability.** Refuted the row's headline: no client wire struct carries the field, the app tier holds
  no `ops.>` publish, and the Gateway stamps the verified actor. The severity, the framing and §10.1 were
  rewritten around this. It also surfaced the free-form `--actor` flag, which is why For Andrew (iv) says
  plainly what this design does **not** defend against instead of implying it does.
- **Exploitation chain + corpus.** Surfaced the shipped lint gate (§2.6) — **the correction that reshaped
  the whole design** — plus the seven-op guard corpus (3 Loom + 4 Weaver), which is what makes admitting the
  whole primordial set the *consistent* choice rather than a widening (§10.2).
- **Prior rulings.** Surfaced the founding "grants nothing" rationale, which §2.3 falsifies rather than
  ignores; `sensitive-ref-mac-provenance-design.md` §9's explicit flag of this gap (a green light, not a
  constraint); the shelved `readScope` and its revive trigger, addressed head-on in §8.2; and the descriptor
  floor as "the cheapest host", which §2.5 and §4 row 4 reject on the file's own anti-bypass reasoning.

**§2 reflexes walked against this draft, one at a time, before this section was written:**

| Reflex | Applied to |
|---|---|
| *an inherited refusal's REASON is a claim; the refutation is often in the same package* | my own filed row — refuted by `scripts/lint-conventions.go` (§2.6, §10.4) |
| *verify a mechanism can BE reshaped* | `resolveAdmitted` cannot be an authorization allow-list (§2.5, §4 row 4) |
| *a guard on a field only one path sets is inert* | subject-binding to a step-3-validated target — inert on `Scope:"any"`, which all seven emitters hold (§4 row 3) |
| *a reassuring negative gets the least scrutiny* | "only Loom produces this" and "the lint's exempt class is empty" — §6.1 and §6.3 ship the greps so Phase 0 re-derives both |
| *an EXCLUSION is a claim about another mechanism's coverage* | "the lint already covers it" — §2.6 opens the gate's own early returns and finds two: `packageOpScopeIsAny`, and `packages/**` scope |
| *a guarantee may hold by accident of shape* | twice — the content binding held by one function's shape (⇒ Increment 2), and the API containment held by four structs' field sets (⇒ §10.1) |
| *a payoff claim is a soundness claim — trace the consumer through every conjunct* | §3.3 is a traced table, not an assertion; rows 2/3 are labelled hygiene and row 5 states the non-defence |
| *a comment/contract may be affirmatively wrong about security* | Contract #2 §2.5 class (f)'s closing sentence (§2.3, §7) |
| *check the other in-flight designs, including the dirty tree* | `git status` first; the class-(h) edit is uncommitted in `main` — §7 and §8.3 reconcile with it |
| *the alternatives table's first row is deletion* | rows 1 and 1b, written first; 1b's core move became Increment 2, and row 1 is §10.1's honest fallback |
| *run each rejected alternative's objection back at your own recommendation* | §4 closing paragraph, all four |
| *a clear/write you are REPLACING must be removed* | checked; does **not** apply — the seven pins guard more than egress (§3.4) |
| *price the demand-side fix first* | the demand side is seven ops already pinned and lint-enforced; the case is the declaration end, not the emission end (§8.2, §10.1) |
| *build-tagged harnesses escape `go test ./...`* | §9 names `internal/leaseconvergence`'s egress e2e as mandatory |

**Two findings I state rather than resolve**, because each is a one-line decision the principal answers
faster than a census does: §10.1 (build Increment 1 at all, given the reduced payoff) and §10.2 (the width
of the admitted actor set).
