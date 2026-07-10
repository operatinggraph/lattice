# Design — Adapter read-seam: subject-templated externalTask params

**Status: ✅ Andrew-ratified (2026-06-28)** · Author: Winston (Designer fire) · 2026-06-28
Backlog row: *External-I/O maturity → Adapter read-seam / richer params* (★★, S–M),
`_bmad-output/planning-artifacts/backlog/lattice.md`.

> **RATIFICATION DECISION (Andrew, 2026-06-28) — flipped to Mechanism 2 (flavor 2a).** The original draft
> chose **Mechanism 1** (Loom engine-side point-reads — Loom reads Core KV to resolve the templates). Andrew
> directed **Mechanism 2**: **Loom performs NO Core KV reads.** It *infers* the aspect read-set from the
> `subject.*` templates (pure string parsing — Context Hinting) and declares those keys in the instanceOp's
> `ContextHint.Reads`; the **Processor JIT-hydrates** them; the **instanceOp DDL resolves** the templates
> from hydrated `state` via §2.5 `kv.Read` — **the same Processor-side read mechanism §8 uses for two-hop
> reads.** Rationale: keep Core-KV reads inside the Processor; **Loom's guard-evaluation stays the lone
> Core-KV-read exception.** Bonus: the param fields come from the instanceOp's **OCC-hydrated commit
> snapshot** (strictly stronger than Mechanism 1's submit-time point-reads). **Flavor 2a** = resolution via
> a **shared `orchestration-base` Starlark resolver helper** (write once, call per adapter), *not* a new
> Processor-generic surface (2b, rejected as over-built for one consumer). The §10.5 narrowing + the PII
> handling (Fire 3 / Vault) are **unchanged**. Sections 2.2–2.4, 3, 9, 11 below are written to Mechanism 2;
> the Mechanism-1 prose is retained only where it explains *why* the flip is sound.

---

## For Andrew

**What it does (two lines).** Today a bridge adapter only ever receives the *static* params a Loom
pattern hard-codes (`{family: backgroundCheck}`) — a real vendor that needs the applicant's legal
name / DOB / SSN has no path to them, even though those fields live one aspect-read away on the subject
vertex. This makes Loom **resolve `subject.<aspect>.data.<field>` param templates** (the same grammar
guards already use) against the subject at instanceOp-submit time, so an adapter gets the real subject
fields — staying on the write path, keeping PII off the read-path, no new step field, no new primitive.

**This is a drift fix, not a new feature.** §10.5 (line 549) **already mandates** externalTask `params`
be *"row/subject templates resolved per the §10.5/§10.8 templating rule."* The engine never honored it —
it passes `step.Params` through **verbatim** (`internal/loom/engine.go:997`, *"no engine-side template
resolution"*) and hydrates only `[subjectKey]` (`engine.go:1009`). So the contract is right and the code
is behind it.

**The one fork (resolved, not deferred): subject read-seam, NOT richer projection columns.** §6 walks
both. The projection-columns option is rejected because it pushes PII (SSN/DOB) into a read-model row (a
D1 / Vault leak — the exact thing the lease-signing discipline avoids: *"only the DERIVED
booleans/counts reach the read model"*, `scripts.go:389`), needs a **frozen `triggerLoom` shape change**,
and only works on the Weaver-originated path. The read-seam stays on the write path (P2), uses §2.5
known-key reads (no scans, no new primitive), and reuses the already-frozen guard-path grammar.

**Frozen-contract change — ✅ ratified (Andrew, 2026-06-28) and committed with this ratification.**
`docs/contracts/10-orchestration-surfaces.md` §10.5 (lines 549–550): the externalTask `params`
description is narrowed from *"row/subject templates"* to **"subject templates"** because the
`row.<column>` half is **unimplementable on the Loom write path** (the violation row is a read-model
projection the write path must not read, and the frozen `triggerLoom{pattern, subject}` action carries
no row to Loom). The edit is the proposal; I have **not** staged a code-bearing change to any frozen
doc beyond this one line-pair. Affected consumers: none today (no externalTask params uses `row.<column>`
— the only live params is the literal `{family}`); the Weaver actuator's own `row.<column>` resolution
(§10.8) is untouched.

**The sensitive-PII (SSN/DOB) intersection is designed through, build-sequenced behind Vault.** §7. The
Vault design (`vault-crypto-shredding-design.md`, lines 12–13) already names *"a bridge adapter sending
PII to a vendor"* as a Phase-B consumer. Fire 3 specifies the composition (a sensitive-ref in params,
bridge-side Vault-unwrap so plaintext SSN never touches the event log) but is **not built** until the
ratified Vault feature lands. Fires 1–2 carry the **non-sensitive / plaintext-for-now** fields the
codebase already treats as plaintext.

**No architectural fork** (no Gateway / read-path-auth / Vault / multi-cell / HA decision is forced by
Fires 1–2; Fire 3 inherits Vault's already-ratified sequencing).

---

## 1. Problem & intent

### 1.1 The grounded gap

An `externalTask` Loom step dispatches an idempotent external call through the bridge. The adapter
receives a `bridge.Request{IdempotencyKey, Operation, Subject, Params}` (`internal/bridge/adapter.go:15`).
`Params` originates as the `params` field of the `external.<adapter>` event the instanceOp DDL emits.

Tracing the live path end-to-end:

1. The Loom step declares static params — the reference vertical's only use is
   `Params: map[string]any{"family": "backgroundCheck"}` (`packages/lease-signing/patterns.go:38`).
2. Loom passes them through **verbatim** — `engine.go:997`: *"params is opaque pass-through (no
   engine-side template resolution — a Loom step's params are concrete pattern data)."* It hydrates only
   the bare subject root for the instanceOp: `reads := []string{inst.SubjectKey}` (`engine.go:1009`).
3. The instanceOp DDL re-emits them into the event almost verbatim —
   `event_data["params"] = {"family": fam}` (`packages/lease-signing/scripts.go:636`).
4. The bridge hands them to the adapter.

Net: an adapter can only ever see what the **pattern author hard-coded** plus the opaque correlation
token. The applicant's **legal name, DOB, SSN, current address** — everything a real background-check or
payment vendor actually needs — live as **aspects on the subject identity vertex**
(`vtx.identity.<id>.demographics`, `.ssn`, …) and **never reach the adapter**. The lease-signing code
says so directly: the demographics aspect holds `fullName` / `employerName` *"in the Core-KV aspect
plaintext-for-now … only the DERIVED booleans/counts reach the read model"* (`scripts.go:386–389`).

This is the backlog row: *"Adapters can only use what the target-lens row projects; a vendor needing
extra subject fields (SSN / DOB) has no fetch path."*

### 1.2 The contract already wants this

The frozen step shape's prose (§10.5, `docs/contracts/10-orchestration-surfaces.md:549`):

> `params` are **row/subject templates resolved per the §10.5/§10.8 templating rule**

So the gap is an **implementation-vs-frozen-contract drift**: the contract specifies templated params, the
engine does opaque pass-through. The intent already exists; the design's job is to (a) implement the
**subject** half correctly, (b) resolve the `row` half (it is unimplementable on the write path — §6.3),
and (c) design through the sensitive-PII case (§7).

### 1.3 Vision provenance

Brainstorm #48 — *"External adapter framework (the Weaver 'Nudges out' to APIs like payments)"* — and the
Loom utility libraries (*"Background Check, Stripe handshake"*, brainstorm line 374). A background check
is the canonical example: brainstorm #258 lists *"Unique identity (email/SSN)"* as a first-class
modeling concern. An adapter framework that can't pass the subject's identifying fields to the vendor is
the framework half-built.

---

## 2. The shape (what gets built)

### 2.1 The seam: subject-path param templates, resolved at submit time

Reuse the **already-frozen guard-path grammar** (§10.5:586–588) for param values:

```
subject.data.<field>            → the subject vertex root's <field>
subject.<aspect>.data.<field>   → the <field> of the subject's <aspect> aspect
<anything else>                 → a literal, passed through verbatim
```

A Loom step's `params` (today `json.RawMessage`, opaque pass-through) becomes a map whose **string
values matching `subject.*`** are resolved against the subject; every other value (literal string,
number, bool, nested object) passes through unchanged. This is the exact literal-vs-token model §10.8
already defines for the Weaver actuator (*"either a literal … or the token"*, §10.8:956) — only the token
vocabulary differs (`subject.<path>` here vs `row.<column>` there), because the resolution context
differs (the live subject vertex here vs the violation row there).

Example — the lease-signing backgroundCheck step (Fire 2):

```go
Params: map[string]any{
    "family":    "backgroundCheck",            // literal — unchanged
    "fullName":  "subject.demographics.data.fullName",
    "dob":       "subject.demographics.data.dob",
},
```

resolves to (what the adapter receives):

```json
{ "family": "backgroundCheck", "fullName": "Dana Lopez", "dob": "1991-04-02" }
```

### 2.2 Where resolution happens — the instanceOp DDL, from Loom-declared hydrated reads (Mechanism 2 / 2a)

Resolution happens **inside the instanceOp DDL on the Processor side**, from the op's JIT-hydrated working
set — **not** in the Loom engine. The split of responsibilities:

1. **Loom — declare, don't read (Context Hinting).** At externalTask submit time Loom walks the `params`
   map and, for each `subject.<aspect>.data.<field>` value, `parseGuardPath`s the token to extract the
   **known aspect key** `subjectKey + "." + <aspect>` (and `subjectKey` itself for `subject.data.*`). This
   is **pure string parsing — Loom performs no Core KV read.** The extracted keys are added to the
   instanceOp's `ContextHint.Reads` (today just `[]string{inst.SubjectKey}`, `engine.go:1009`); the
   **unresolved** templates are passed through as the op `params` (the existing opaque pass-through at
   `engine.go:997` is preserved verbatim — Loom never substitutes a value).
2. **Processor — JIT-hydrate.** The Processor hydrates the declared `reads[]` into the op's working-set
   `state` exactly as it does for every op (arch line 44, JIT Hydration). No new Processor surface.
3. **instanceOp DDL — resolve from `state` (flavor 2a).** A **shared `orchestration-base` Starlark resolver
   helper** (write once, called by each externalTask instanceOp DDL where it already assembles the event
   `params`, `scripts.go:636`) walks the template map and substitutes each `subject.*` token with the value
   read from hydrated `state` via §2.5 `kv.Read` — **the same Processor-side `kv.Read` mechanism §8 uses for
   two-hop/linked-vertex reads.** A template resolving **null/absent is a data error** (surface, fail the
   dispatch loudly — the §10.8 discipline + the FR29 never-silently-drop posture), enforced in the helper.

**Why Mechanism 2 (Andrew's directive, 2026-06-28).**

- **Core-KV reads stay inside the Processor.** Loom does not read Core KV for this feature — it only
  *declares* the read-set. **Loom's guard evaluation remains the lone sanctioned Core-KV-read exception**
  outside the Processor; this seam does not widen that surface. (This is the architectural reason; it
  overrides the original draft's "zero DDL churn / reuse the Go resolver" convenience case.)
- **Strictly stronger consistency.** The param fields are read from the instanceOp's **OCC-hydrated commit
  snapshot** — the same revisions the op commits against — not a separate, marginally-stale submit-time
  point-read. (Mechanism 1's snapshot was *acceptable*; Mechanism 2's is *strictly better*, and removes the
  §8 "stale snapshot" caveat entirely.)
- **One read mechanism.** Direct-subject params and the §8 two-hop linked-vertex reads both resolve via the
  instanceOp DDL's `kv.Read` over hydrated, known keys — a single Processor-side path, not two.

**The cost (accepted).** Template resolution moves from Loom's tested Go resolver into the instanceOp DDL
Starlark. Mitigated by **flavor 2a** — a single shared `orchestration-base` Starlark resolver helper, tested
once, called per adapter — so it is *not* per-DDL boilerplate, and it lands exactly where the instanceOp DDL
already builds the event `params`. (Flavor **2b**, a Processor-generic `subject.*` param resolver, was
rejected: zero DDL churn but a new platform surface for a single consumer today — over-built.) The keys
hydrated per externalTask are bounded by the number of `subject.*` templates the step declares — the same
working-set cost the op already pays for `subjectKey`.

### 2.3 Read path / write path / invariants

- **P2 (write path).** The resolution rides the **write path** and reads happen **inside the Processor**:
  Loom submits the instanceOp declaring the aspect keys in `ContextHint.Reads`; the **Processor** hydrates
  them from **Core KV** (the subject's aspects, never a lens read-model) and the instanceOp **DDL** reads
  them via §2.5 `kv.Read`. **Loom itself reads no Core KV** (it only declares the read-set). The Processor
  stays the sole writer; no direct KV writes.
- **Core-KV reads stay in the Processor (Andrew's invariant, 2026-06-28).** This seam adds **no** Core-KV
  read outside the Processor. Loom's **guard evaluation** remains the single sanctioned exception (a
  platform binary reading Core KV on the write path); param resolution does **not** join it — it is a
  Processor-side hydrated read, like every other DDL `kv.Read`.
- **P5 (read path) is *not* used and *not* violated.** The subject aspects are read on the
  **write/orchestration path** (Processor JIT hydration + DDL `kv.Read`), not as an application query of a
  lens projection. The P5 lens-only rule governs vertical apps' query surface, not the engine's write-ahead
  reads.
- **No-scans invariant preserved.** Every read is a **known-key point-read** — the subject key is in
  hand, and `subjectKey + "." + <aspect>` is a derivable known key. No prefix scan, no read-model read,
  no new primitive. (Contrast the separate *op-time bounded link-enumeration* design, which deliberately
  relaxes no-scans for a capped link prefix — this design does **not** need that; it reads single known
  aspect keys.)
- **Contract #1 key shapes.** Aspect keys are the canonical 4-segment `vtx.<type>.<id>.<localName>` —
  exactly what `parseGuardPath` + the resolver already build. Nothing new minted.
- **D5.** Unchanged — the instanceOp still writes minimal root + the outcome in aspects; this design only
  changes what the *event* carries, not the claim vertex's shape.

### 2.4 Vertices / aspects / links / ops / lenses touched

| Surface | Change |
|---|---|
| **Loom `Step`** (`internal/loom/pattern.go:28`) | **No new field.** `Params json.RawMessage` already exists; the aspect read-set is *inferred* from the `subject.*` tokens in it. |
| **Loom engine** (`submitExternalTask`, `engine.go:983`/`:1009`) | **Declare, don't read.** `parseGuardPath` each `subject.*` param token → add the known aspect key to the instanceOp's `ContextHint.Reads` (today just `subjectKey`). **No Core KV read in Loom**; the opaque `params` pass-through (`engine.go:997`) is preserved — templates flow through unresolved. |
| **`orchestration-base` shared resolver helper** (Starlark, NEW — flavor 2a) | A `resolve_subject_params(params, state)` helper that substitutes each `subject.*` token from the op's JIT-hydrated `state` via §2.5 `kv.Read`; null/absent ⇒ loud data error. Written once, called by each externalTask instanceOp DDL. |
| **instanceOp DDL** (e.g. `lease-signing/scripts.go:636`) | Call the shared helper to resolve `params` from hydrated `state` **before** emitting the `external.<adapter>` event. The replyOp DDL is unchanged. |
| **Guard grammar** (`guard.go` `parseGuardPath`) | Reused for the token grammar (Loom-side inference); no grammar change. |
| **Lenses / read-models** | **None.** Deliberately — the whole point is to keep PII off the read path. |
| **`vtx.*` / `lnk.*` / new ops** | **None.** No new vertices, links, or operations. |

---

## 3. The flow (end-to-end, Fire 1 + 2)

```
Weaver detects gap → triggerLoom(pattern, subject=identity)        [unchanged]
  Loom pattern step: externalTask{ adapter:"backgroundCheck",
      params:{family:"backgroundCheck", fullName:"subject.demographics.data.fullName", ...},
      instanceOp:"CreateLeaseServiceInstance", replyOp:"RecordLeaseServiceOutcome" }
  Loom.submitExternalTask:                                         [Mechanism 2 — Loom reads NO Core KV]
    inferReads(step.Params): parse subject.* tokens (string-only)  [NEW — Fire 1]
       → ContextHint.Reads = [subjectKey, subjectKey.demographics]
    submit instanceOp { params: {family, fullName:"subject.demographics.data.fullName"} UNRESOLVED,
                        reads: [...] }                             [params pass through opaque]
  PROCESSOR: JIT-hydrate reads[] into state; run instanceOp DDL:
    resolve_subject_params(params, state):  (shared orch-base helper) [NEW — Fire 1]
       "family"   → literal "backgroundCheck"
       "fullName" → state[vtx.identity.<id>.demographics].data.fullName → "Dana Lopez"  (§2.5 kv.Read)
       (null/absent ⇒ data error: fail dispatch loudly)
    → emit external.backgroundCheck{ params={family, fullName, dob} concrete } (mints claim vertex)
  BRIDGE: consume external.backgroundCheck → Request.Params = {family, fullName, dob}
    → FakeBackgroundCheck.Execute reads req.Params["fullName"]     [Fire 2 asserts receipt]
    → replyOp → outcome aspect → orchestration.externalTaskCompleted → Loom advances  [unchanged]
```

Nothing downstream of the resolution changes — the bridge, the event envelope (`params` is already a
free-form map, *"package + bridge data, not a contract amendment"*, `bridge.md:8`), the replyOp, and the
completion correlation are all untouched.

---

## 4. Contract surface (change-vs-build-to)

| Contract § | Build-to or change? | Detail |
|---|---|---|
| **#10 §10.5 — externalTask `params`** (line 549–550) | **CHANGE — ✅ ratified + committed (2026-06-28)** | Narrow *"row/subject templates resolved per the §10.5/§10.8 templating rule"* → *"**subject** templates (the §10.5 guard-path grammar), resolved at instanceOp-submit time"*, with a note that `row.<column>` templating is the **Weaver actuator's** (§10.8) and is not reachable on the Loom write path. See §6.3 for why. |
| **#10 §10.5 — guard-path grammar** (line 586–588) | **build-to** | The param grammar **is** the guard grammar; no change. |
| **#10 §10.8 — templating** (line 954–960) | **build-to** | The literal-vs-token model is reused verbatim; the `row.<column>` token stays the Weaver actuator's. Untouched. |
| **#10 §10.6 — externalTask completion / async** | **build-to** | Untouched — resolution is upstream of the event; completion/async unchanged. |
| **#2 §2.5 — known-key reads** | **build-to** | The aspect point-reads are known-key. |
| **#1 §1.1 — key shapes** | **build-to** | 4-segment aspect keys, built by the existing resolver. |
| **Bridge `external.<adapter>` envelope** (`bridge.md`, package data) | **build-to** | `params` is already a free-form map; no shape change. |
| **#3 §3.10 / Vault** | **build-to (Fire 3 only)** | Fire 3 composes on the ratified Vault feature; no new contract beyond Vault's own staged §3.10. |

The staged §10.5 edit is the **only** frozen-contract touch, and it is a **narrowing/clarification of a
prose line within the already-frozen step shape** — no field added or removed, the `params` key and the
guard grammar both pre-exist. It is left UNCOMMITTED in `main` per house rules; the diff is the proposal.

---

## 5. Migration & test strategy

### 5.1 Migration

**None required.** The change is **backward-compatible by construction**: a param value that is *not* a
`subject.*` token is a literal and passes through exactly as today. Every shipped pattern uses only
literals (`{family}`), so behavior is byte-identical until a pattern opts in to a `subject.*` template.
No bootstrap version bump, no kernel-count change, no re-install.

### 5.2 Tests

- **Fire 1a (Loom read-set inference):** unit table — `subject.data.<field>` → declares `subjectKey`;
  `subject.<aspect>.data.<field>` → declares `subjectKey.<aspect>`; a literal/non-`subject.` value declares
  nothing; mixed map → the de-duplicated union of keys; the declared `ContextHint.Reads` is exactly the keys
  the templates need (no over/under-declaration). **Assert Loom issues no Core KV read** on this path.
- **Fire 1b (the shared `orchestration-base` resolver helper):** Starlark unit test — literal pass-through;
  root-path + aspect-path resolution from a hydrated `state`; null/absent ⇒ loud data error (not a
  malformed dispatch); a numeric/bool literal untouched. Reuse the meta-pipeline harness the DDL tests use
  (`jsstore.Dir(t)` per the CI-parallelism rule).
- **Fire 2 (consumer, end-to-end):** the lease-signing backgroundCheck externalTask declares
  `subject.demographics.data.*` templates; an ephemeral-stack convergence test asserts the
  `FakeBackgroundCheck` adapter **received** the resolved fields (extend the adapter to record its last
  `Request.Params` for the assertion) and the loop still converges green. This is the **today-consumer** —
  Fire 2 is not dead scaffolding.
- **Drift guard:** the lease-signing read-set drift-guard test (`engine.go:1008`) **must be updated** — the
  instanceOp `reads[]` now legitimately **grows** by the inferred aspect keys (Mechanism 2). Update the
  guard's expected read-set to the subject root **plus** the aspects the step's templates declare; a
  mismatch is then a real drift (a template added without its read, or vice-versa) — which is exactly the
  fail-closed property worth pinning.

### 5.3 Verification gates

`go build ./...`, `make vet`, `golangci-lint run ./...`, `go test ./internal/loom/...`,
`go test ./packages/lease-signing/...`, and the lease-convergence e2e gate (Fire 2). No kernel/package
verify-* change (no DDL/permission/key touched).

---

## 6. The fork: read-seam vs richer projection columns (resolved)

The backlog frames the decision as *"richer projection columns vs. an adapter read seam."* Both walked;
the read-seam wins decisively.

### 6.1 Option A — subject read-seam (CHOSEN)

Resolve `subject.<path>` templates against the live subject on the write path (§2). **Pros:** PII stays
off the read-path; write-path-clean (P2, §2.5 known-key reads, no scans); no new step field; no new
primitive; reuses frozen grammar + tested resolver; backward-compatible; works on **both** the
Weaver-originated and the directly-started pattern paths (the subject is always in hand). **Cons:** only
reaches the **subject + its aspects** (single hop) — a field on a *linked* vertex (e.g. a payment needing
the unit's address) is not directly templatable (§8 addresses this).

### 6.2 Option B — richer projection columns (REJECTED)

Project the needed fields into the Weaver target lens row and carry them to Loom. **Rejected — three
independent killers:**

1. **PII-in-read-model leak.** SSN/DOB in a `weaver-targets` row is exactly the D1 / Vault exposure the
   codebase deliberately avoids (*"only the DERIVED booleans/counts reach the read model"*,
   `scripts.go:389`). It would make the read-path-auth (D1) and Vault problems strictly worse.
2. **Frozen-contract change to `triggerLoom`.** The action shape is `triggerLoom{pattern, subject}`
   (§10.8:1083) — it carries no params/row. Carrying row columns to Loom needs a frozen §10.8 change,
   a heavier contract touch than the §10.5 narrowing Option A needs.
3. **Doesn't cover the direct-start path.** A pattern started by `StartLoomPattern` without a Weaver row
   has no row to template from; Option A's subject is always present.

### 6.3 Why the contract's `row.<column>` half is unimplementable on the write path

§10.5:549 says *"row/subject"*. The `row.<column>` half cannot be honored for externalTask params:

- By the time the externalTask instanceOp runs, the **violation row is gone** from Loom's view — Loom
  holds only the `subjectKey` (`engine.go:993`). Re-reading the row means reading the **`weaver-targets`
  read-model** from the write path — forbidden (the write path must not read the lagging read-model; the
  op-time-bounded-link design states this invariant explicitly, and it is P5/P2 doctrine).
- The only way to *carry* the row to Loom is through `triggerLoom`, whose frozen shape carries no row
  (§6.2 #2).

Hence the §10.5 narrowing: externalTask params are **subject** templates. `row.<column>` remains the
Weaver actuator's own resolution (§10.8) — where the row genuinely *is* in hand — for `subject`/`assignee`
/`target` selection. The contract conflated two resolution contexts under one prose line; the design
disentangles them.

---

## 7. The Vault / sensitive-PII intersection (Fire 3 — designed, build behind Vault)

A background check's most important fields — **SSN, DOB** — are precisely the `sensitive: true` aspects
the ratified Vault feature encrypts at rest (`vault-crypto-shredding-design.md` §3.10). Two consequences
the design must honor:

1. **Once Vault lands, a sensitive aspect is ciphertext at rest.** A naive `subject.ssn.data.value`
   resolution (the DDL's hydrated `kv.Read`) would resolve to **ciphertext** — useless to the vendor.
2. **Baking plaintext SSN into the event/claim plane is itself an exposure.** Fires 1–2 resolve params
   into the `external.<adapter>` **event** (which lands in the durable `events.external.>` stream) — fine
   for plaintext-for-now fields, **not** fine for SSN once we have a Vault to protect it.

> **⛔ SUPERSEDED (2026-07-10) — this section's mechanism sketch is replaced by
> [`sensitive-param-egress-design.md`](sensitive-param-egress-design.md) (📐 awaiting-Andrew).** The
> sketch below predated the shipped Vault and was wrong on two mechanics the successor grounds: hydration
> returns **plaintext** (decrypt-on-read is unconditional), so the "resolves not to the value" behavior
> needs a real Processor-side primitive (`contextHint.egressReads`, ref-if-sensitive hydration); and a
> ref must **not** carry key material or a frozen envelope — the successor's ref is
> `{ref, ciphertext, field}` with the bridge fetching the **live** envelope (a frozen envelope defeats
> crypto-shred across a Vault restart). Direction retained: the bridge **is** the unwrap point (the
> §11-3 fork, resolved there), plaintext exposure stays bounded to the in-memory adapter call, and the
> per-identity DEK is reused. Build sequencing, fire decomposition, and this row's remaining scope now
> live in the successor design (its Fires 1–2 subsume this design's Fires 2–3).

> **🚧 GROUNDING FINDING (2026-07-06, Lattice Steward fire) — Fire 2 is now BLOCKED, not just
> "not yet picked up."** Vault has since shipped (2026-07-05, `vault-crypto-shredding-design.md`) and
> **every identity-anchored PII aspect in the current schema is `sensitive: true`**
> (`packages/identity-domain/ddls.go`: `.name`, `.email`, `.phone`, `.ssn`, `.dob` — there is no
> `.demographics` aspect at all; the design's Fire 2 example was illustrative, not grounded in the
> as-built schema). Confirmed by reading `internal/processor/step4_hydrate.go` +
> `sensitive_decrypt.go`: decrypt-on-read is a **Processor commit-path middleware applied per-key to
> ANY op's `kv.Read`/`contextHint.reads`**, not scoped to the writing op — so `resolve_subject_params`'s
> `kv.Read` on `.ssn`/`.dob` would return real **plaintext** (not ciphertext, contrary to this
> section's original "would resolve to ciphertext" concern) and bake it directly into the
> `external.<adapter>` event, landing **plaintext SSN/DOB in the durable `events.external.>` stream** —
> exactly the exposure Fire 3 exists to prevent, now live risk since Vault shipped, not a future one.
> There is **no non-sensitive identity field left to safely demo Fire 2's "today-consumer" with**.
> Building Fire 3's sensitive-ref envelope (§7) requires a **Starlark-exposed sensitivity-detection
> primitive that does not exist today** (nothing lets the `orchestration-base` resolver helper tell "this
> aspect's DDL is `sensitive: true`" apart from a plain one — decrypt-on-read deliberately hides that
> fact from Starlark, `sensitive_decrypt.go`: *"Starlark never sees the envelope, only the decrypted
> plaintext"*) — this is a **genuinely new mechanism, not an established pattern to extend**, so it
> routes to the **Designer** (`lattice-designer`), not an inline Steward build. **Do not build Fire 2
> against `.ssn`/`.dob` as currently speced** until Fire 3's primitive is designed and ratified — flag
> this row `🚧 blocked-on:` a Designer fire for "Starlark sensitivity-detection primitive (adapter
> read-seam Fire 3)" on the board. **→ RESOLVED (2026-07-10): the Designer fire ran —
> [`sensitive-param-egress-design.md`](sensitive-param-egress-design.md) (📐 awaiting-Andrew) designs the
> primitive (as a Processor-side `egressReads` hydration disposition, deliberately *not* a
> Starlark-visible sensitivity check) and subsumes Fires 2–3 of this design.**

---

## 8. Risks, alternatives, adjacencies

- **Single-hop only (subject + its aspects).** A param needing a field on a *linked* vertex (a payment
  needing the unit's listing address, not the applicant's) is not directly templatable by `subject.*`.
  **Mitigation / delineation — now unified under Mechanism 2:** the linked-vertex case uses the **same
  instanceOp-DDL `kv.Read`** this design uses for the direct subject params — (a) a known linked key is
  read on-demand by the DDL (the lease-signing CreateLeaseApplication script already does exactly this for
  the unit's rent, `scripts.go:425`), and (b) enumerating a *set* of neighbors is the **op-time bounded
  link-enumeration** design (already 📐 awaiting-Andrew). This design scopes to the **subject + its
  aspects** — the common case (the subject of an externalTask *is* the entity being checked/charged) and
  the no-scans-clean case — but shares one read mechanism with the two-hop work, not a separate one.
- **Stale snapshot — dissolved.** Under Mechanism 2 the param fields are read from the instanceOp's
  **OCC-hydrated commit snapshot** (the revisions the op commits against), so there is no submit-time
  staleness gap. (This was Mechanism 1's only consistency caveat; choosing Mechanism 2 removes it.)
- **Data-error loudness.** A `subject.*` template resolving null/absent must **fail the dispatch loudly**
  (a config/data error surfaced via the externalTask deadline backstop + a log), never silently send a
  call with a missing field — the §10.8 *"null reference = data error"* discipline, and the FR29
  never-silently-drop posture.
- **Adjacency, not overlap.** Distinct from: *structured adapter result* (✅ done — `Outcome`/`Result`),
  *async result-return* (✅ done — `Dispatch`/`Poll`/schedule lane), and the *op-time bounded
  link-enumeration* primitive (separate design). This row is solely the **inbound** param/read path.

---

## 9. Fire-by-fire decomposition (for the Lattice Steward)

Each fire is independently shippable + green.

| Fire | Scope | Review depth | Contract |
|---|---|---|---|
| **Fire 1 — the templating engine (Mechanism 2 / 2a)** | (i) Loom-side **read-set inference**: `parseGuardPath` each `subject.*` param token → add the known aspect key to the instanceOp's `ContextHint.Reads` (`engine.go:1009`); **no Core KV read in Loom**, `params` stays opaque pass-through. (ii) The shared **`orchestration-base` Starlark resolver helper** (`resolve_subject_params(params, state)`) substituting tokens from JIT-hydrated `state` via §2.5 `kv.Read`; null/absent ⇒ loud data error. Unit table (§5.2) on both the inference and the helper. Backward-compatible (literals untouched). | **Full review** — write/orchestration path + a frozen-contract-prose narrowing; param-resolution correctness + data-error loudness matter, and the read happens in the DDL/Processor (verify Loom takes no Core KV read for this path). | Build-to §10.5 grammar + §2.5 known-key reads; the §10.5 prose narrowing is **committed with this ratification**. |
| **Fire 2 — the lease-signing consumer (today-consumer)** | Add `subject.demographics.data.*` templates to the backgroundCheck externalTask step (`patterns.go`); extend `FakeBackgroundCheck` to record its received `Request.Params`; an ephemeral-stack convergence e2e asserts receipt + green convergence. | Thorough lead — package data + a test-only adapter change over a Fire-1 mechanism already reviewed. | None. |
| **Fire 3 — sensitive-PII via Vault (designed, NOT built now)** | Sensitive-ref param envelope + bridge-side Vault-unwrap (§7). | Full 3-layer (PII-egress, security-plane). | Composes on Vault §3.10 (already staged); a Phase-B consumer note added uncommitted **only when greenlit**. **Build behind the ratified Vault feature.** |

**Recommended pre-build pass:** a short adversarial/party (`bmad-party-mode`) check on Fire 1's
**data-error boundary** (a null/absent `subject.*` must hard-fail in the shared helper, never send a blank
field to a vendor) and the **read-set inference** (Loom must declare *exactly* the aspect keys the templates
need, so the Processor hydrates them — an under-declared read makes the DDL resolution fail closed, which is
the safe direction but should be tested). Under Mechanism 2 the snapshot is OCC-strong, so the
submit-time-staleness concern is dissolved. Folded-in self-review below.

---

## 10. Self-adversarial pass (folded in)

- **"Why the instanceOp DDL `kv.Read`, not engine-side resolution?"** *(Resolved by Andrew → Mechanism 2.)*
  Engine-side resolution (the original draft) would have Loom read Core KV — widening Loom's read surface
  beyond guard evaluation. The chosen approach keeps Core-KV reads **in the Processor** (Loom only declares
  `ContextHint.Reads`), so guard-eval stays the lone exception, and the param fields come from the op's
  OCC-hydrated snapshot. The DDL-churn cost is absorbed by a **shared `orchestration-base` helper** (not
  per-script). The single chokepoint where Fire 3 swaps plaintext for a sensitive-ref is now that helper.
- **"Does this break the bridge's type-agnosticism?"** No. The bridge still treats `Params` as an opaque
  free-form map; only its *contents* are richer. The bridge never parses the claim vertex type.
- **"Is the §10.5 edit really necessary, or can I implement subject-only silently?"** Implementing
  subject-only while the contract says *"row/subject"* leaves the code narrower than the frozen prose —
  a drift in the other direction. House rules: stage the actual edit, flag it. The edit is the honest
  record of what's implementable.
- **"What about a pattern that already ships a `row.<column>` param?"** None does (grep: the only live
  externalTask params is `{family}`, a literal). The narrowing breaks no consumer.
- **"PII in the event log even for plaintext-now fields?"** Yes, Fires 1–2 put plaintext name/DOB in the
  `events.external.>` stream — but those fields are *already* plaintext in Core KV aspects today
  (`scripts.go:386`), and the event stream is internal/trusted-infra. Fire 3 closes the sensitive subset
  the moment Vault gives us a key to protect it. Net exposure does not increase over today's posture.

---

## 11. Open ratification items (Andrew's call)

1. **The §10.5 narrowing** — `row/subject` → `subject` for externalTask params (§4, §6.3). ✅ **Ratified
   (Andrew, 2026-06-28) — committed with this ratification.**
2. **Mechanism 1 vs 2** — ✅ **Resolved (Andrew, 2026-06-28): Mechanism 2, flavor 2a** (Loom declares
   `ContextHint.Reads`, the instanceOp DDL resolves from JIT-hydrated `state` via §2.5 `kv.Read`; Loom reads
   no Core KV; guard-eval stays the lone Core-KV-read exception; OCC-strong snapshot). See §2.2 + the
   header decision banner.
3. **Fire 3 sequencing** — confirm Fire 3 (sensitive-PII) builds **behind** the ratified Vault feature,
   and that the bridge is the correct unwrap point (vs routing through a Phase-B Secure Lens). The Vault
   design names the bridge as a PII-egress consumer but frames its access via the Secure Lens (read
   path); this design argues the externalTask params path is write-path, so a **bridge-side unwrap of a
   sensitive-ref** is the cleaner composition — §7. Andrew's call on which.

---

*Designed by Winston (Designer fire, 2026-06-28). Awaiting Andrew's ratification. The Lattice Steward
builds Fires 1–2 once ratified; Fire 3 builds after the Vault feature lands.*
