# Design — Adapter read-seam: subject-templated externalTask params

**Status: 📐 awaiting-Andrew (ratification)** · Author: Winston (Designer fire) · 2026-06-28
Backlog row: *External-I/O maturity → Adapter read-seam / richer params* (★★, S–M),
`_bmad-output/planning-artifacts/backlog/lattice.md`.

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

**Frozen-contract change — staged UNCOMMITTED in `main`, NOT in this fire's commit.**
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

### 2.2 Where resolution happens — the engine, reusing the guard resolver

Resolution happens **in the Loom engine at externalTask submit time**, reusing the existing
`parseGuardPath` (`internal/loom/guard.go:357`) + `guardResolver.resolve` (`internal/loom/guard_eval.go:112`)
machinery — the same point-read path the guard evaluator already runs against the subject. A new
`resolveParams(ctx, conn, coreKVBucket, subjectKey, rawParams) (map[string]any, error)` walks the params
map; for each `subject.*` value it `parseGuardPath` + `resolve`s and substitutes the concrete value; a
template that resolves **null/absent is a data error** (surface, do not dispatch a malformed call — the
exact §10.8 discipline). The resolved concrete map replaces the opaque pass-through in
`submitExternalTask` (`engine.go:989–1001`).

**Why engine-side point-reads, not OCC-hydrated DDL reads (Mechanism 1 vs 2).** The alternative is to add
the inferred aspect keys to the instanceOp's `reads[]` (`engine.go:1009`) and resolve the templates inside
each instanceOp DDL from hydrated `state`. Rejected for v1 because:

- **Snapshot semantics are correct here** and match the guard model. An external call sends *"what was
  true when we dispatched"*; the subject's identity fields (name/DOB) are stable. Guards already accept
  submit-time point-read semantics; params should too. OCC on the *subject root* (which the instanceOp
  already takes for its no-orphan check) still protects the claim-creation invariant; the param fields
  need no independent revision guard.
- **Zero DDL churn.** Every instanceOp DDL stays unchanged (`params` is still opaque to the DDL — it
  re-emits the now-concrete map). Mechanism 2 would force a resolution helper into every instanceOp
  script.
- **Reuses proven code** — the guard resolver is already tested against this exact grammar
  (`guard_test.go`).

The cost (a few extra point-reads per externalTask submission, bounded by the number of `subject.*`
templates the step declares) is identical in shape to what the guard evaluator already pays per step.

### 2.3 Read path / write path / invariants

- **P2 (write path).** The resolution rides the **write path**: Loom submits the instanceOp (an op
  through the Processor); the param values are read from **Core KV** (the subject's aspects), never from a
  lens read-model. The Processor stays the sole writer; no direct KV writes.
- **P5 (read path) is *not* used and *not* violated.** The subject aspects are read as **Core-KV
  point-reads on the write/orchestration path** (the same plane guards read on), not as an application
  query of a lens projection. Loom is a platform binary, not a `cmd/<app>` vertical — the P5 lens-only
  rule governs vertical apps' query surface, not the orchestration engine's write-ahead reads.
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
| **Loom `Step`** (`internal/loom/pattern.go:28`) | **No new field.** `Params json.RawMessage` already exists; reads are *inferred* from the `subject.*` tokens in it. |
| **Loom engine** (`submitExternalTask`, `engine.go:983`) | Resolve `step.Params` via the new `resolveParams` before building the event payload. |
| **Guard resolver** (`guard.go` / `guard_eval.go`) | Reused as-is (`parseGuardPath`, `guardResolver.resolve`); possibly a tiny refactor to share the resolver constructor. No grammar change. |
| **instanceOp / replyOp DDLs** | **Unchanged** — params stays opaque to the DDL; it re-emits the now-concrete map. |
| **Lenses / read-models** | **None.** Deliberately — the whole point is to keep PII off the read path. |
| **`vtx.*` / `lnk.*` / new ops** | **None.** No new vertices, links, or operations. |

---

## 3. The flow (end-to-end, Fire 1 + 2)

```
Weaver detects gap → triggerLoom(pattern, subject=identity)        [unchanged]
  Loom pattern step: externalTask{ adapter:"backgroundCheck",
      params:{family:"backgroundCheck", fullName:"subject.demographics.data.fullName", ...},
      instanceOp:"CreateLeaseServiceInstance", replyOp:"RecordLeaseServiceOutcome" }
  Loom.submitExternalTask:
    resolveParams(subjectKey, step.Params):                        [NEW — Fire 1]
       "family"   → literal "backgroundCheck"
       "fullName" → point-read vtx.identity.<id>.demographics → .data.fullName → "Dana Lopez"
       (null/absent ⇒ data error: do not dispatch)
    → event payload params = {family, fullName, dob}  (concrete)
    submit instanceOp (mints claim vertex + emits external.backgroundCheck{params})  [unchanged]
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
| **#10 §10.5 — externalTask `params`** (line 549–550) | **CHANGE — staged UNCOMMITTED** | Narrow *"row/subject templates resolved per the §10.5/§10.8 templating rule"* → *"**subject** templates (the §10.5 guard-path grammar), resolved at instanceOp-submit time"*, with a note that `row.<column>` templating is the **Weaver actuator's** (§10.8) and is not reachable on the Loom write path. See §6.3 for why. |
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

- **Fire 1 (engine):** unit table on `resolveParams` — literal pass-through; root-path resolution
  (`subject.data.<field>`); aspect-path resolution (`subject.<aspect>.data.<field>`); null/absent ⇒ data
  error (not a malformed dispatch); mixed literal+template map; a non-`subject.` string left untouched; a
  numeric/bool literal untouched. Reuse the embedded-NATS fixture the guard tests use (`jsstore.Dir(t)`
  per the CI-parallelism rule).
- **Fire 2 (consumer, end-to-end):** the lease-signing backgroundCheck externalTask declares
  `subject.demographics.data.*` templates; an ephemeral-stack convergence test asserts the
  `FakeBackgroundCheck` adapter **received** the resolved fields (extend the adapter to record its last
  `Request.Params` for the assertion) and the loop still converges green. This is the **today-consumer** —
  Fire 2 is not dead scaffolding.
- **Drift guard:** the lease-signing package's existing read-set drift-guard test (`engine.go:1008`
  references it) continues to pass — the instanceOp `reads[]` is unchanged (resolution is engine-side
  point-reads, not op reads), so no drift-guard update is needed.

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
   point-read (Fire 1's mechanism) would resolve to **ciphertext** — useless to the vendor.
2. **Baking plaintext SSN into the event/claim plane is itself an exposure.** Fire 1 resolves params
   into the `external.<adapter>` **event** (which lands in the durable `events.external.>` stream) — fine
   for plaintext-for-now fields, **not** fine for SSN once we have a Vault to protect it.

**Fire 3 design (composes on Vault, builds after it):**

- A `subject.<aspect>.data.<field>` template whose aspect is `sensitive: true` resolves **not** to the
  value but to a **sensitive-ref envelope** — `{ ref: "vtx.identity.<id>.ssn", field: "value",
  keyId: "<identity-DEK-id>" }` — carried in the event params. **Plaintext never enters the event log or
  the claim vertex.**
- The **bridge** is the unwrap point — it is the platform's trusted PII-egress boundary, and the Vault
  design already names *"a bridge adapter sending PII to a vendor"* as a Phase-B consumer
  (`vault-crypto-shredding-design.md:12–13`). Just before the adapter call, the bridge resolves any
  sensitive-ref in `Request.Params` via the Vault (the same envelope-unwrap the Secure Lens uses on the
  read path) and hands the adapter plaintext for the single outbound call.
- This keeps the **Processor sole-writer** invariant (no new write path), keeps **plaintext exposure
  bounded to the bridge's in-memory call** (not the durable event/claim plane), and reuses Vault's
  per-identity DEK (no new key hierarchy).

**Sequencing.** Fire 3 is **build-sequenced behind the ratified Vault feature** (itself behind D1). Until
Vault ships, sensitive fields either (a) stay out of adapter params, or (b) flow as the
**plaintext-for-now** values the codebase already treats as plaintext (`scripts.go:386`) — no regression
vs today, where they don't flow at all. Fire 3's design is on the shelf; the Steward builds it when Vault
lands. A small note will be added to `vault-crypto-shredding-design.md` Phase B's consumer list (the
bridge-egress unwrap point) — **uncommitted, for Andrew**, only if/when Fire 3 is greenlit; this design
does not stage it now (Vault's §3.10 is the governing contract and is already staged).

---

## 8. Risks, alternatives, adjacencies

- **Single-hop only (subject + its aspects).** A param needing a field on a *linked* vertex (a payment
  needing the unit's listing address, not the applicant's) is not directly templatable by `subject.*`.
  **Mitigation / delineation:** that is genuinely separate work and **already has homes** — (a) the
  instanceOp DDL can do an on-demand §2.5 `kv.Read` of a known linked key (the lease-signing
  CreateLeaseApplication script already does exactly this for the unit's rent, `scripts.go:425`), and (b)
  enumerating a *set* of neighbors is the **op-time bounded link-enumeration** design (already
  📐 awaiting-Andrew). This design deliberately scopes to the **subject + its aspects** — the common case
  (the subject of an externalTask *is* the entity being checked/charged) and the no-scans-clean case.
- **Stale snapshot.** Submit-time point-reads can be marginally stale vs the instanceOp commit. Immaterial
  for identity PII (stable); consistent with the guard model. If a future field needs OCC-strong
  consistency, Mechanism 2 (DDL-side hydrated reads) is the upgrade — but no current need.
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
| **Fire 1 — the templating engine** | `resolveParams` in `internal/loom` (reuse `parseGuardPath` + `guardResolver`); wire it into `submitExternalTask`; unit table (§5.2). Backward-compatible (literals untouched). | **Full review** — it touches the write/orchestration path and a frozen-contract-prose narrowing; not security-plane but param-resolution correctness + the data-error loudness matter. (Lead may scale to thorough-lead if Fire 1 lands literal-only-safe and the resolver is a pure reuse — state it explicitly.) | Build-to §10.5 grammar; the §10.5 prose narrowing is **staged uncommitted** for Andrew — do **not** build Fire 1 until ratified (it implements the narrowed semantics). |
| **Fire 2 — the lease-signing consumer (today-consumer)** | Add `subject.demographics.data.*` templates to the backgroundCheck externalTask step (`patterns.go`); extend `FakeBackgroundCheck` to record its received `Request.Params`; an ephemeral-stack convergence e2e asserts receipt + green convergence. | Thorough lead — package data + a test-only adapter change over a Fire-1 mechanism already reviewed. | None. |
| **Fire 3 — sensitive-PII via Vault (designed, NOT built now)** | Sensitive-ref param envelope + bridge-side Vault-unwrap (§7). | Full 3-layer (PII-egress, security-plane). | Composes on Vault §3.10 (already staged); a Phase-B consumer note added uncommitted **only when greenlit**. **Build behind the ratified Vault feature.** |

**Recommended pre-build pass:** a short adversarial/party (`bmad-party-mode`) check on Fire 1's
**data-error boundary** (a null `subject.*` must hard-fail, never send a blank field to a vendor) and the
**snapshot-consistency** claim, before Fire 1 builds. Folded-in self-review below.

---

## 10. Self-adversarial pass (folded in)

- **"Isn't this just letting the DDL `kv.Read` the aspect?"** It could — but `kv.Read` in the instanceOp
  DDL would (a) require editing every instanceOp script, (b) put the resolution grammar in Starlark
  instead of the typed engine, and (c) still bake plaintext into the event. Engine-side resolution is
  fewer moving parts and is the single chokepoint where Fire 3 later swaps plaintext for a sensitive-ref.
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

1. **The §10.5 narrowing** — `row/subject` → `subject` for externalTask params (§4, §6.3). Staged
   uncommitted; ratify or adjust.
2. **Mechanism 1 vs 2** — engine-side submit-time point-reads (recommended) vs OCC-hydrated DDL reads
   (§2.2). Recommendation: Mechanism 1.
3. **Fire 3 sequencing** — confirm Fire 3 (sensitive-PII) builds **behind** the ratified Vault feature,
   and that the bridge is the correct unwrap point (vs routing through a Phase-B Secure Lens). The Vault
   design names the bridge as a PII-egress consumer but frames its access via the Secure Lens (read
   path); this design argues the externalTask params path is write-path, so a **bridge-side unwrap of a
   sensitive-ref** is the cleaner composition — §7. Andrew's call on which.

---

*Designed by Winston (Designer fire, 2026-06-28). Awaiting Andrew's ratification. The Lattice Steward
builds Fires 1–2 once ratified; Fire 3 builds after the Vault feature lands.*
