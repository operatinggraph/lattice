# Subject-anchored sensitive aspects + a retention posture — design

**Status: 📐 awaiting-Andrew (ratification).** Author: Winston (Designer fire, 2026-08-02).

Backlog row: `planning-artifacts/backlog/lattice.md` → *Privacy / Vault* → **"[Vault] Sensitive aspects are
identity-anchored, so event-scoped PHI has no home"** (★★★, L). Grounds in the ratified
[vault-crypto-shredding design](vault-crypto-shredding-design.md) (the plane this extends), Contract #1
§1.3 (the aspect envelope) + Contract #3 §3.10 (sensitive-aspect encryption at rest), the Obsidian
*Brainstorm PII and Crypto-Shredding* subdoc, `lattice-architecture.md` Items 5 & 6, and the named
consumer: `packages/clinic-domain`'s `.encounter` aspect.

---

## For Andrew (one-look ratification block)

**What it does, in two lines.** Today a `sensitive: true` aspect may attach **only** to a
`vtx.identity` — so clinic's `.encounter` (the clinical note: summary / assessment / plan) sits in Core
KV as **plaintext PHI**, unprojectable and un-shreddable, because it belongs to an *appointment*, not to
a person. This design **separates the aspect's ANCHOR from its key CUSTODY**: the aspect may anchor on
any vertex, while the DEK that encrypts it stays **per-identity, exactly as today** — named by a new
`subjectKey` field the mutation carries. No new key hierarchy, no shred cascade, no enumeration.

**No architectural fork.** I looked for one and there isn't a real one — §9.1 covers the alternative I
expected to be a fork (per-vertex key custody + a shred cascade) and why it collapses: custody must stay
identity-scoped because *only an identity can exercise a right to be forgotten*, which is the vision's
own framing (`asp.<vtxId>.pii_key` — "the key is unique to the `Identity` vertex"). What remains is a
plumbing choice that settles on cost.

**Frozen-contract changes (two, staged UNCOMMITTED in `main` as the proposal-diff).**
1. **Contract #1 §1.3** — the aspect envelope gains one optional field, **`subjectKey`**.
2. **Contract #3 §3.10** — "the anchoring identity's DEK" becomes "the **governing subject's** DEK",
   plus the anchor-vs-custody rule, the fail-closed default, and the egress refusal.

**Read this part before ratifying — the adversarial pass changed the design's shape, not just its
details (§10).** My draft claimed the shred needed *no new machinery*. **That was wrong, and wrong in
the dangerous direction.** A Secure Lens is scrubbed on shred only because the `piiKey` CDC event
reaches it — and delivery is decided by `ReferencedLabels`, which collects labels from **node patterns
only** (`ruleengine/full/labels.go:31-43`). Every shipped Secure Lens binds the identity as a *node*
(`OPTIONAL MATCH (p)-[:identifiedBy]->(id:identity)`), so `identity` is in its label set by accident of
shape. This design expresses custody as a *property chain* (`appt.encounter.subjectKey`), which
contributes **no label** — so the shred event is dropped twice over: the JetStream consumer never
subscribes to `vtx.identity.>` (`NarrowedFilterEligible`, `pipeline.go:698`, has no secure-lens
conjunct) and the plain aspect arm ack-drops it (`plainReactsTo`, `pipeline.go:519-528`). **A projected,
decrypted clinical note would survive erasure indefinitely, with the erasure reporting success** — a
strictly worse outcome than today's un-projected plaintext. Closing it is now **Increment 2**, and
clinic (Increment 3) is sequenced behind it. Both gates verified in code; see §5.

**The other thing worth your attention:** flipping `.encounter` to `sensitive: true` would silently
break three shipped lenses. Step 6.5 encrypts the **whole `data` map** (`step65_encrypt.go:83-101`),
and `.encounter.data` mixes PHI with the *operational* signals (`documentedAt`, `followUpRequested`,
`followUpDate`) that `clinicAppointments`, `clinicAppointmentsRead` and `providerAppointmentsRead`
already project. They would all go null, green, with no error. Increment 3 therefore **splits the
aspect** — which is exactly what the ratified aspect-level-granularity decision already prescribes
("some fields sensitive, others not ⇒ split into separate aspects"), so this is the existing rule
applied, not a new one.

**Recommendation:** ratify all five increments as one delivery in intent. Increments 1–2 are the
platform; 3 is the consumer that justifies it; 4 is the retention posture a clinical record legally
requires; 5 is deferred behind named triggers. **Do not let clinic carry real PHI until 4 has landed**
(§8.3).

---

## 1. Problem & intent

**The gap.** `internal/processor/step6_validate.go:176-195` rejects a mutation whose resolved DDL is
`Sensitive` when the aspect's parent vertex type is not `identity`
(`DDLViolation{sensitiveAspectScope}`). The rule is generic and correct for what it was built for — all
seven of identity-domain's sensitive aspects (`name`, `email`, `phone`, `ssn`, `dob`, `claimKey`,
`credentialBinding`) are person-scoped and single-valued on the person.

Clinical data is neither. `packages/clinic-domain`'s `.encounter`
(`vtx.appointment.<id>.encounter`, class `appointmentEncounter`) carries `summary` / `assessment` /
`plan` — raw PHI — and there is **one per visit**. It cannot be an identity aspect: it would collide on
every subsequent visit (an aspect key is unique per `(vertex, localName)`), and it does not describe the
person, it describes an event. The package took the only option left, and its own DDL says so:

> "…RAW clinical PHI — captured plaintext-for-now under the trusted-tool posture … NEVER projected into
> a read model (the deferred Vault plane owns display)."

Three consequences:

1. **Plaintext PHI at rest** in Core KV *and* in JetStream history, forever.
2. **Unprojectable** — the clinical record has no UI at all; only `documentedAt` / `followUpRequested`
   reach a read model.
3. **Outlives a crypto-shred** — `ShredIdentityKey` destroys the patient's contact PII and leaves the
   clinical note fully readable. The right-to-erasure claim is silently partial, in a way the Vault
   design's own §2.6 honesty rule would not accept.

`packages/clinic-domain/README.md` lists this as the deferred item the vertical *forces*: "its own
right-to-be-forgotten + display stay gated on a future Vault extension." **This is that extension.**

**Intent.** Give event-scoped PII a home on the existing Vault plane, changing as little of it as
possible — and add the retention posture a clinical record requires, which `ShredIdentityKey`'s
all-or-nothing shred cannot express.

## 2. Reconciliation with the existing mental model

*Didn't we already handle this?* Partly — and the part that already works is what keeps this design
small. `pipeline.SecureDecryptor` resolves key custody from `SecureColumn.IdentityKeyColumn`, "the
RETURN alias carrying the OWNING identity's vertex key" — an **arbitrary** alias, not the anchor.
`clinicPatientsRead` already exercises it across a link: the row anchors on `vtx.patient` and decrypts
contact PII off the patient's *optional* `identifiedBy` identity. So the Refractor's **decryptor** never
assumed the aspect's parent is the key owner.

But — the correction the adversarial pass forced — the Refractor's **delivery** layer does assume it,
transitively: a shred only scrubs a row because the `piiKey` event is judged relevant, and that judgment
runs off node labels. Today every secure lens happens to bind `:identity` as a node. So the honest
statement is: *the decryptor is anchor-agnostic; the event-delivery path is not, and nobody had to
notice until now.* §5 makes it explicit rather than transitive.

*Does this contradict the architecture's design-of-record?* No — it is the design-of-record's own
framing. The Obsidian subdoc introduces the vault aspect as **`asp.<vtxId>.pii_key`** — a *generic
vertex* — and separately fixes custody: "The key is unique to the `Identity` vertex." Anchor-free,
custody-identity-scoped is what the vision said; identity-anchoring is a Phase-1 simplification that
fell out of resolving custody *by* the anchor, which Contract #3 §3.10 records verbatim as "the
anchoring identity's DEK".

*Is there precedent for anchor≠custody already in the platform?* **Yes, and it is stronger than the
one I first cited.** The blob plane (Contract #3 §3.11) already separates them on a non-identity vertex:
`vtx.object.<oid>.content` carries an explicitly caller-supplied `governingIdentity`
(`packages/objects-base/lenses.go:160`) under whose DEK the per-object CEK is wrapped. This design is
the aspect-plane analogue of a shape the object plane has shipped since the Vault's Fire 6.

*Does it introduce new state, and do we keep that state anywhere?* One field, `subjectKey`. We do not
keep it today: the analogous pointer, `vertexKey`, points at the *host*, which is precisely what stops
being the subject. Its lifetime is specified in §4.4 rather than left as "carried along."

*Is another in-flight design solving this?* No. Checked every design touching `piiKey` /
`sensitiveAspectScope` / `SecureColumn`: [dedup-over-encrypted-pii](dedup-over-encrypted-pii-design.md)
(✅ ratified — blind-index equality, different problem), [sensitive-param-egress](sensitive-param-egress-design.md)
(✅ ratified — the plane this design deliberately *declines* to extend, §6.2), and
[auth-plane-projection-latency](auth-plane-projection-latency-design.md), whose §14.x reasons about
`secureIdentityKeyType` — the one live overlap, addressed in §5.3.

## 3. What forces the current shape (the census that sizes this design)

Per *"ratified practice ≠ required practice"*: the question is not what exists under the anchoring rule,
it is **what requires it**. It is load-bearing at **six** sites, doing **three** distinct jobs:

| # | Site | Job |
|---|---|---|
| 1 | `processor/step6_validate.go:186` | **Custody derivation** — refuse what step 6.5 could not key |
| 2 | `processor/step65_encrypt.go:69` | **Custody derivation** — `ParseAspectKey` → the DEK owner |
| 3 | `processor/sensitive_decrypt.go:163` | **Custody derivation** — same, read side |
| 4 | `refractor/pipeline/pipeline.go:557` (`secureIdentityKeyType`) | **Narrowing conjunct** — but on a path a secure lens cannot take (§5.3) |
| 5 | `vault/service.go:479` (`handleDecryptRef`) | **Attacker-field elimination**, egress ref plane |
| 6 | `bridge/egress.go:175` (`resolveSensitiveRef`) | **Attacker-field elimination**, client side |

Sites 1–3 are pure custody plumbing — a `subjectKey` field states what the key shape was implying, and
they become small changes.

**Sites 5–6 are a silent obligation the anchoring rule was carrying.** They are *not* custody plumbing:
the egress path derives the identity **server-side from the ref key** on purpose —
`service.go`'s comment: *"deriving identityKey from it server-side (the caller no longer supplies one —
one less attacker-controlled field)"*. Freeing the anchor removes that property. §6.2 refuses rather
than extends.

**Site 4 is a decoy, and §5.3 explains why** — the real delivery guard is elsewhere, which is the
finding that reshaped this design.

## 4. The shape (Processor)

### 4.1 Data model (Contract #1 key-shapes)

No new vertex types, no new key shapes, **no new key hierarchy**.

- **The sensitive aspect** anchors where its data belongs — `vtx.appointment.<aid>.encounter`, a
  4-segment aspect key, unchanged.
- **`subjectKey`** — a new **optional top-level field of the aspect envelope** (Contract #1 §1.3),
  sibling to `vertexKey` / `localName`, holding the **full vertex key** `vtx.identity.<NanoID>` of the
  governing data subject.
- **Custody is unchanged**: one DEK per identity, referenced from `vtx.identity.<id>.piiKey`.

**Why the full key, not a bare NanoID.** Per *representation follows use*: this value is
**dereferenced** — every consumer computes `subjectKey + ".piiKey"` and point-reads it
(`sensitive_decrypt.go:242`, `pipeline/secure.go:195`). It is an address, not an opaque match token.
(The opposite call from the RLS anchor, which *is* a match token and correctly carries a bare NanoID —
same word, different job.)

**Where the value survives, and where it does not — corrected after review.** My draft claimed "no
allowlist at either end." That is true at two of the three ends and **false at the third**:

- **Write → Core KV: survives.** `step8_commit.go:419` copies every `m.Document` key verbatim; there is
  no envelope allowlist. `preserveImmutableFields` (`:457-475`) touches only the three named
  `immutableEnvelopeFields` and cannot drop it. A tombstone copies the whole prior body first
  (`:414-418`).
- **Core KV → lens cypher: survives.** `executor.go:800-821` (`readNode`) unmarshals the **whole**
  stored JSON into `nodeRef.props`; `resolveProperty` (`:1693-1700`) returns an aspect's whole props map
  on the first hop and `propertyOf` (`:1718-1719`) indexes it on the second. `appt.encounter.subjectKey`
  therefore resolves with **zero rule-engine change** — confirmed through the parser too
  (`visitor.go:551-561` compiles an arbitrary-length property-lookup run into nested `PropertyAccess`).
- **Core KV → Processor script context: DROPPED.** `VertexDoc` is a fixed Go struct
  (`script_context.go:154-169`) and `parseVertexDoc` (`step4_hydrate.go:442-452`) is a plain
  `json.Unmarshal` into it, so `subjectKey` is discarded on hydration; `vertexDocToStarlark`
  (`starlark_runner.go:623-638`) builds a fixed `StringDict`, so a script cannot read it back either.
  **Increment 1 must add the `VertexDoc` field and its Starlark projection** — without them
  `decryptSensitiveDoc` has nothing to resolve custody from, and §4.4's "resupply on update" would
  demand a value the script cannot see.

### 4.2 Write path (P2 — operations only)

```
step 4 (hydrate)  → decrypt-on-read resolves custody from doc.SubjectKey when present,
                    else (identity-anchored) from the parent segment as today.
step 5 (execute)  → unchanged. Starlark sees plaintext, returns plaintext.
step 6 (validate) → sensitiveAspectScope becomes a SUBJECT rule. Absence = REJECT.
step 6.5 (encrypt)→ custody = subjectKey when present, else the parent. piiKey minting,
                    the shared-batch create and the OCC retry signal are unchanged.
step 8 (commit)   → unchanged (subjectKey rides the document like any other field).
```

**Step 6's new rule.** For a mutation whose resolved DDL is `Sensitive` and whose kind is an aspect:

1. Parent type is `identity` → **accept** (today's path, byte-identical).
2. Otherwise `subjectKey` must be present, parse as a well-formed `vtx.identity.<NanoID>`, and be
   **present, alive and `class: identity` in the hydrated working set**
   (`state.Context.Hydrated[subjectKey]`).
3. The subject must not be **shredded** — see below.
4. Anything else → `DDLViolation{sensitiveAspectScope}`.

**Omission denies.** An author who writes a PHI aspect on an appointment and forgets `subjectKey` does
not get plaintext at rest; they get a commit-time rejection naming the constraint.

**Why hydrated-set membership.** It costs nothing (the map is in hand — no new read) and removes the
fabricated-subject case structurally: the subject must have been declared in `contextHint.reads` and
found alive at the step-4 snapshot. The platform guarantees the subject *exists and is an identity*; the
**package** guarantees it is the *right* one by validating the graph path. Same division `CreatePatient`
already runs for its optional `identityKey`.

**The shredded-subject rule (review finding #5).** `ShredIdentityKey` never tombstones the identity —
it writes `piiKey` with `isDeleted:false` and `data.shredded:true`
(`packages/privacy-base/shred_identity_key.go:270-272`). So a shredded subject passes checks 1–2, and
step 6.5's `ensureIdentityKey` (`step65_encrypt.go:114-123`) — unlike the Refractor's copy, which does
check (`secure.go:206-209`) — reads the envelope blind, `Vault.Encrypt` returns `ErrKeyShredded`
(`vault/local.go:381-383`), and the operation dies with a generic `"step encrypt failed"`
(`commit_path.go:380-382`). **Failure scenario:** a patient exercises erasure; a provider later
documents a visit that already happened; `RecordEncounter` fails permanently with an opaque internal
error and the record Increment 4 exists to *retain* can never be written. Step 6 therefore declares the
subject's `piiKey` under `optionalReads` and rejects a shredded subject with the typed constraint. (In
Increment 4 an `erasable`-shredded subject is still writable under the **retained** DEK — the check is
per-custody, not per-identity.)

**Two pre-existing fail-open paths this design must close, because it is what puts PHI behind them:**

- **A class-less mutation bypasses both gates** (review finding #3). Step 6's DDL block is
  `if class != ""` (`step6_validate.go:145-156`) and step 6.5 `continue`s on an empty class
  (`step65_encrypt.go:59-62`); `parseMutations` does not require `class`
  (`starlark_runner.go:349-359`). A script emitting an `update` on `vtx.appointment.X.encounter` with
  a document carrying `data` but no `class` commits **plaintext PHI**, silently. Increment 1 rejects a
  class-less create/update aspect mutation, after a census confirming no shipped script relies on it.
- **Step 6 and step 6.5 can disagree about sensitivity** (review finding #4). Both charge the same
  `LiveReads` budget (`step6_resolve_ddl.go:262`, `:378`) and step 6 runs first. For a class resolvable
  only via the instanceOf chain, step 6 can spend the last of the budget, see `Sensitive`, and pass —
  then step 6.5's identical call hits `errInstanceOfLiveReadBudgetExceeded`, which **fails open** to the
  permissive default (`:263-268`) and skips encryption. Plaintext, silently. Increment 1 makes a
  budget-exhausted resolution a **hard error** in step 6.5 rather than a permissive miss. (Low
  likelihood at a 60 000 budget; wrong direction, so it is not left as a note.)

**What the clinic package does (a worked example, because "resolved from the row" is not a mechanism).**
`RecordEncounter(appointmentKey, subjectIdentityKey)`: the caller supplies the subject; the script
declares `contextHint.reads = [appointmentKey, patientKey, lnk.patient.<pid>.identifiedBy.identity.<iid>, subjectIdentityKey]`
— **every one an exact key**, because once the caller names both `<pid>` and `<iid>` the 6-segment link
key is fully determined (Contract #1 §1.1, `patient identifiedBy identity`). The script refuses unless
all four hydrate alive, then writes the mutation with `subjectKey = subjectIdentityKey`. No scan, no
`kv.Links`, no adjacency read — the write-path no-scans invariant is untouched.

**The consequence clinic must absorb:** `CreatePatient`'s `identityKey` is **optional** today, so a
patient can exist with no linked identity — and such a patient has no possible DEK owner, so
`RecordEncounter` fails closed. Clinic must make the link **required for any patient whose encounters
are recorded**. Defensible on its own terms: a person who cannot be identified cannot exercise erasure
against the record either. Verticals-lane work, called out in §11.

### 4.3 Read path (P5 — lens projections)

- **Processor (step 4).** `decryptSensitiveDoc` gains the same custody resolution — plus the
  `VertexDoc` field from §4.1 without which it has nothing to read. Note the branch it replaces is
  currently *silent*: for a non-identity parent the function marks the body plaintext-readable and
  returns **without decrypting** (`sensitive_decrypt.go:162-172`), handing the script a raw ciphertext
  map. Unreachable today because step 6 refuses the write; Increment 1 makes it reachable, so it must
  become a real decrypt with a loud error on unresolvable custody.
- **Refractor Secure Lens — decryptor unchanged, delivery changed (§5).** The lens RETURNs the envelope
  whole as today. **Corrected wording after review:** `IdentityKeyColumn` is validated against declared
  **table columns**, not cypher aliases (`lens/corekv_source.go:890-900`), so the lens does
  `RETURN appt.encounter.subjectKey AS subject_key` and declares `subject_key` in `targetConfig.columns`.
  And the activation check is weaker than my draft claimed: nothing at activation verifies the cypher
  actually returns that alias — a lens declaring the column but omitting the RETURN item activates
  cleanly and then fails per-row at runtime.
- **Default (plain) lenses** copy ciphertext as-is — but see §4.5, which is where that guarantee is
  narrower than it sounds.

### 4.4 The lifetime of `subjectKey` (specified, not implied)

A new piece of state beside an existing one needs a rule at every boundary the existing one has:

- **create** — required (§4.2); immutable from here.
- **update** — **required, and must equal the prior document's `subjectKey`.** Two reasons, and the
  first is load-bearing rather than belt-and-braces: `buildMutationValue` layers `prior` **only** for a
  tombstone (`step8_commit.go:414-421`) and `preserveImmutableFields` does not carry `subjectKey`, so an
  update that merely *omits* it **strips custody**, orphaning committed ciphertext. Second, an update
  replaces `data` wholesale, so re-encrypting under a *different* subject is cryptographically coherent
  — and that is the hazard: silently moving custody moves a record out from under a pending erasure, on
  the security plane, in the over-retain direction. Custody is immutable for the life of an aspect key;
  genuine re-subjecting is a tombstone plus a new aspect.
- **tombstone** — no document needed; the prior body is copied wholesale, so `subjectKey` and the
  ciphertext are preserved together.

**Three gaps the review found in the enforcement, all folded (finding #6):**
1. **Absent prior ⇒ allow.** An `update` over an absent key materially creates it
   (`step8_commit.go:399-400`), so the check must permit "no prior" — stated explicitly, and a *read
   fault* on the prior fails closed rather than being treated as absent.
2. **The point-read is not the serializer.** For an aspect never hydrated, `ExpectedRevision` is nil and
   step 8 conditions on a revision read *later* than step 6.5's, so a concurrent custody change can land
   in the window. **Rule: a subject-anchored sensitive aspect being updated must be a declared read**, so
   the step-4 revision is the OCC condition and the point-read is only a diagnostic.
3. **Same-batch bypass.** Nothing dedups mutation keys within a batch; two updates of one aspect key both
   read the same committed prior. **Rule: a sensitive aspect key appears at most once per batch.**

Also worth stating plainly: after Increment 1, step 6.5 silently appends a `create` on
`vtx.identity.<subject>.piiKey` — a vertex root the operation neither declared nor named — **after**
step 6 has run. Today that root is always the aspect's own parent. No shipped permission check is keyed
on the write footprint (verified), so this is a property to record, not a defect; anything that later
audits the committed key set must expect it.

### 4.5 Whole-`data` encryption: the aspect must not mix PHI with projected signals

Step 6.5 encrypts the **entire `data` map** (`step65_encrypt.go:83-101`) — a design property inherited
from the ratified aspect-level-granularity decision, not something this design chooses. So **any
non-sensitive field sharing a sensitive aspect's `data` becomes unreadable to every plain lens.**

`.encounter.data` is exactly that shape: `{summary, assessment?, plan?, documentedAt,
followUpRequested, followUpDate?}` (`clinic-domain/ddls.go:902`), and three shipped lenses project the
operational half out of it — `clinicAppointments` (`lenses.go:463-465`, unprotected),
`clinicAppointmentsRead` (`:646-648`, protected) and `providerAppointmentsRead` (`:681-683`, protected).
Flipping `sensitive: true` would null `documentedAt` / `followUpRequested` / `followUpDate` across all
three, **green, with no error** — the package tests pin spec text, not runtime values. Nor is it
recoverable by declaring a Secure column: `validateSecureColumns` refuses `secureColumns` on a
non-protected lens (`lens/corekv_source.go:852-853`), and `clinicAppointments` is unprotected.

**Resolution:** Increment 3 splits the aspect — `.encounter` keeps the PHI and becomes sensitive; the
operational signals move to a sibling non-sensitive aspect the three lenses read instead. This is the
ratified rule applied ("some fields sensitive, others not ⇒ split into separate aspects"), and it
generalises: **§4.3's "plain lenses copy ciphertext as-is, ciphertext-safe by construction" holds for a
lens copying the whole aspect, and is false for field-level projection.** Any future package flipping an
existing aspect to sensitive owes this same census of its projected fields.

## 5. The shred must be *delivered* — the Refractor half (Increment 2)

**My draft said "Orchestration: none — `ShredIdentityKey` reaches a subject-anchored aspect with no new
machinery at all." That claim is false.** It is true of the *cryptography* (destroying the DEK does make
every ciphertext under it unrecoverable, wherever anchored) and false of the *projection surface*, which
is where the plaintext actually lives.

### 5.1 The two gates that drop the shred event

A shred commits a write to `vtx.identity.<I>.piiKey`. For a Secure Lens to scrub its decrypted row, that
CDC event must reach the lens's pipeline. Both gates key off `plainReprojectLabels`, derived from
`CompiledRule.ReferencedLabels()` — which collects labels from **node patterns only**
(`ruleengine/full/labels.go:31-43`; the `collectVars` closure walks `p.Nodes`). A custody expressed as
`appt.encounter.subjectKey` compiles to a nested `PropertyAccess` (`visitor.go:551-561`) and contributes
**no label**.

- **Gate A — the consumer never subscribes.** `NarrowedFilterEligible` (`pipeline.go:698-703`) gates
  only on `actorEnumerator != nil || engineKind != EngineFull || plainReprojectAll` — **there is no
  secure-decryptor conjunct.** `ConsumerFilter` then emits `$KV.<bucket>.vtx.<label>.>` per label
  (`subjects.go:170-187`), and `vtx.identity.<I>.piiKey` matches only `vtx.identity.>`, which is not in
  the set for a lens labelled `{appointment, provider}`.
- **Gate B — the plain aspect arm ack-drops it.** `evalPlainAspectReprojection` returns early unless
  `plainReactsTo(parentType)` (`pipeline.go:519-528`), and `identity` is not in the label set.

**Why nobody hit this before:** every shipped Secure Lens binds the identity as a node — e.g.
`clinicPatientsRead`'s `OPTIONAL MATCH (p)-[:identifiedBy]->(id:identity)` (`lenses.go:590-603`), whose
own comment states the dependency: *"The shred's piiKey CDC event re-scans this UNANCHORED lens."* That
structural coupling is what makes plain narrowing safe today. This design removes it.

**No fallback catches it.** There is no sweep for a plain lens (`sweep.go:29-33` installs a SweepPlan
only for an auth-plane actor-aggregate lens). The `keyshredded` nullification listener explicitly
excuses secure lenses (`keyshredded/manager.go:31-33`: *"A SECURE lens needs no entry here: its
piiKey-CDC-triggered reprojection already scrubs…"*) — a justification this finding invalidates — and
could not be retrofitted as written, since `NullifyRow` keys on the row's own identity column
(`manager.go:354`), which an appointment-keyed row does not have.

**Failure scenario.** `clinicEncountersRead` (protected, secure, `MATCH (a:appointment)-[:withProvider]->(pr:provider)`)
projects the decrypted note into Postgres. `ShredIdentityKey(vtx.identity.I)` destroys the DEK; the CDC
event fires; JetStream never delivers it; no sweep; no nullify target. **The row keeps the full plaintext
clinical note forever**, while the erasure reports success. Strictly worse than today, where the
un-projected note is at least only plaintext at rest.

**Aggravating property:** it is silently shape-dependent. A lens with an unlabeled node gets
`plainReprojectAll = true` and works by accident; adding a label makes the guarantee vanish with no
signal.

### 5.2 What Increment 2 builds

**Custody labels join the referenced set.** When a pipeline has a `secureDecryptor`, the vertex types
hosting key custody (`identity` today; plus whatever hosts `retainedPiiKey` after Increment 4) are
injected into `plainReprojectLabels` before `NarrowedFilterEligible` and `plainReactsTo` consult it — or,
if that proves awkward against the hot-reload path, the pipeline forces `plainReprojectAll` for a secure
lens. The narrow variant is preferable (a secure lens should not re-scan on every event) and both are
fail-closed; the fire picks on measurement, since both gates must agree and `ConsumerFilter` is already
required to be recomputed on `Rebuild` (`pipeline.go` doc comment).

**Custody-absence stops being a whole-batch kill (finding F2).** `secure.go:130-135` makes a non-null
ciphertext with an empty identity-key column a `failure.Terminal`, `Apply` returns on the first error
(`:104-106`), and `evaluateForEntry` discards the entire result set (`evaluate.go:81-83`) → the whole
message DLQs. Today the branch is effectively unreachable because `identity_key` comes from a bound
node and nulls in lockstep with the ciphertext. Binding custody to an *aspect field* breaks the lockstep
in one direction — aspect present, `subjectKey` absent — and that population exists (any pre-flip
document, §8). For a non-seeded plain lens the reprojection recomputes **all** rows
(`seedAnchorFor`, `pipeline.go:502-513`), so **one legacy document would permanently block every other
patient's row from ever updating, including a later shred scrub.** Increment 2 makes an
unresolvable-custody row a per-row refusal that does not poison its batch. (§8's reset ruling removes
the legacy population; the blast radius is closed anyway, because "the data is clean" is not a
mechanism.)

### 5.3 `secureIdentityKeyType` — I audited the wrong guard

`pipeline.go:557-570` invites exactly this design to re-derive it: *"whoever lifts that ban owns
re-deriving this type rather than inheriting it."* My draft did so and concluded the constant still
holds. **The conclusion is right and the audit was beside the point:** `secureIdentityKeyType` is
consulted only inside `ActorAwareNarrowingLabels` (`:621-625`), i.e. only when `actorEnumerator != nil`
— and `validateSecureColumns` rejects any `projectionKind != ""` (`lens/corekv_source.go:858-860`), so
**a Secure Lens is always a plain lens** and can never take that path. I re-derived the one conjunct
structurally unreachable for secure lenses and left the two live gates unexamined.

Increment 2 therefore **re-homes the constant**: the "vertex type hosting key custody" becomes one
shared fact consulted by the actor-aware path *and* by §5.2's plain-lens custody injection, and its
comment stops resting on "step 6's `sensitiveAspectScope` admits no other parent" (false after
Increment 1). A guard that is still correct for a reason that has expired is a trap for the next reader.

## 6. Contract surface

| Contract | § | Change vs build-to |
|---|---|---|
| **#1 Addressing & envelope** | **§1.3 aspect envelope — new optional `subjectKey`** | **CHANGE** — staged uncommitted |
| **#3 MutationBatch** | **§3.10 — governing-subject custody, the anchor-vs-custody rule, the fail-closed default, the egress refusal** | **CHANGE** — staged uncommitted |
| #3 MutationBatch | §3.11 sensitive blobs | build-to — unchanged (§6.1) |
| #2 Operation envelope | §2.5 `reads` / `optionalReads` | build-to — the subject and its piiKey are ordinary declared reads |
| #5 Health KV | §5.4 `vault_calls_total` | build-to |
| #7 Primordial bootstrap | reserved `sensitive` aspect type | build-to |

### 6.1 Why §3.11 (blobs) needs no change — verified

§3.11 wraps a per-object CEK under "the **governing identity's** §3.10 DEK". The governing identity is
an explicitly caller-supplied field on `.content.data` (`packages/objects-base/lenses.go:160`), and
`.content` is not itself a `sensitive: true` aspect, so `step6_validate.go:176-195` never applies to it.
The blob plane already separates anchor from custody on a non-identity vertex — checked rather than
assumed, and promoted in §2 to the precedent this design mirrors.

### 6.2 The egress plane: an explicit refusal, not an extension

Per §3, `vault/service.go:479` and `bridge/egress.go:175` derive custody from the ref key's host segment
**by design**, so the caller cannot supply it. A subject-anchored aspect has no identity there.

**Increment 1's posture: refuse.** A non-identity-anchored sensitive aspect declared under
`contextHint.egressReads` is rejected at hydration with a typed error — rather than falling through the
current silent branch, which (verified: `sensitive_decrypt.go:162-172` returns *before* the ref-minting
branch, and with `egress == true` skips `markPlaintext`) hands the script an unmarked raw ciphertext map.
Contract #3 §3.10's ref-provenance paragraph gains a sentence saying so. **Event-scoped PII cannot leave
via the bridge until Increment 5.**

**Why refuse rather than extend now.** Lifting it means binding the subject into the ref MAC
(`RefMACPurpose` `sensitive-ref/v1` → `v2`, a frozen constant) or having the responder point-read the
aspect to derive the subject server-side. Both defensible; neither has a consumer. The census (§7.3),
enumerated by `egressReads` **declaration** across `packages/`, finds `lease-signing` as the only live
declarer and every aspect it egresses identity-anchored. Building MAC-v2 now would be inert machinery
guarding a case no package can construct.

## 7. Risks, and the claims I checked rather than assumed

| Risk / claim | Disposition |
|---|---|
| "A top-level envelope field survives write→read→cypher" | **Half true — corrected (§4.1).** Survives to Core KV and to the rule engine; **dropped** by `VertexDoc`/Starlark on the Processor read path. That gap is Increment-1 work, not an assumption. |
| The shred scrubs a projected subject-anchored row | **False as drafted — §5.** Two delivery gates drop the event; Increment 2 closes them and gates Increment 3. |
| Flipping an aspect to sensitive is transparent to plain lenses | **False for field-level projection — §4.5.** Three shipped clinic lenses would null silently. |
| A script names a subject that is not the real data subject | Platform: live, declared, `class=identity`, not shredded. Package: validates the graph path. Residual is the standing trusted-DDL posture. |
| Custody silently moves, or is stripped, on update | Refused (§4.4) — and `preserveImmutableFields` not carrying it is what makes the rule load-bearing. |
| Wrong-subject ciphertext decrypts under another identity | Cannot: `identityKey` is AEAD associated data (`vault/local.go:238`, `:252`), so mismatched custody fails the GCM tag. |
| A shredded subject | Typed rejection at step 6, not an opaque encrypt failure (§4.2). |
| Sensitivity resolved inconsistently between steps 6 and 6.5 | Budget exhaustion becomes a hard error in 6.5 (§4.2). |
| A class-less mutation bypasses the gate | Closed in Increment 1 (§4.2). |
| Egress ref custody becomes attacker-supplied | Not reached — refused (§6.2). |
| A pre-existing plaintext aspect after the flip | **Bricks the reading op**, not merely stays plaintext (§8). Reset ruling. |
| Increment 3 makes clinical notes erasable when law says retain | Real — the reason Increment 4 exists (§8.3). |

### 7.3 The egress census (enumerated by declaration, not by glob)

`grep -rn "egressReads" packages/` → `orchestration-base` (the mechanism's own helper) and
`lease-signing` (`patterns.go`, `leasedoc_scripts.go`, `scripts.go`, `lenses.go`). `internal/loom`
infers egress reads from `inst.SubjectKey` — the pattern's subject vertex. Every sensitive aspect in
that corpus is identity-anchored. **Live consumers of a subject-anchored egress ref: zero.** Stated as
the census it is, so a future increment re-runs it rather than inheriting the conclusion.

## 8. Migration & compatibility

**Increment 1 is additive and backward-compatible for every shipped sensitive aspect.** All seven
identity-domain aspects take branch 1 of the §4.2 rule and behave byte-identically; `subjectKey` is
absent from every existing document and is consulted only on the non-identity branch.

**Flipping `.encounter` to `sensitive: true` is NOT retroactive — and that is the sharp edge.** Step
6.5 encrypts **on write**; already-committed aspects stay plaintext. The reflex is to write "the
pre-existing rows stay plaintext until rewritten" and move on. That is wrong: on the **read** path a
pre-existing plaintext body now resolves as sensitive, `ciphertextFromData` parses it into a
`Ciphertext` with an empty `CT`, and `Vault.Decrypt` fails — so `decryptSensitiveDoc` errors and **the
operation fails**. The pre-existing population does not degrade quietly; it breaks the op that touches
it. (And on the projection side it is §5.2's batch-poisoning population.)

**Ruling (mirroring the Vault delivery boundary, `vault-crypto-shredding-design.md` build-start
addendum, Andrew 2026-07-02):** a **full-stack reset** at the Increment-3 delivery boundary — `make down
&& make up-full`. Nothing runs in production; NATS and Postgres are ephemeral by design
(`docker-compose.yml`, no named volumes). **No migrate-encrypt path is built.** A prod-era deployment
adopting sensitivity after accumulating data needs one; that is a follow-on when such a deployment
exists.

### 8.3 The interim posture between Increment 3 and Increment 4

After Increment 3 and before Increment 4, an encounter note is encrypted, projectable and **erasable on
shred** — so `ShredIdentityKey` on a patient destroys their clinical record. Correct for
right-to-be-forgotten, wrong for statutory retention. **Recommendation: treat Increments 1–4 as one
delivery in intent** (each ships green independently, but clinic should not carry real PHI until 4 has
landed). Same reasoning that made the ratified Vault posture sequence Phase A + Phase B together: a
half-done privacy plane strands the consumer that justifies it.

## 9. Alternatives considered

### 9.1 Per-vertex key custody + a shred cascade — the fork that isn't

Give the *host* vertex its own `piiKey` (the subdoc's literal `asp.<vtxId>.pii_key`) and make
`ShredIdentityKey` cascade to every vertex whose PII belongs to that identity.

**Rejected, and rejecting it is what collapses the apparent fork.** The cascade must *enumerate*
"vertices subject to this identity" — a link walk, on the erasure path, whose completeness **is** the
erasure guarantee. A missed edge is a silent, permanent right-to-erasure failure with a success signal
on it. Today's guarantee is a single key destruction with no enumeration anywhere; this trades an
unconditional guarantee for a fallible one, adds machinery, and buys nothing `subjectKey` does not. It
also is not what the vision asked for: the same subdoc that writes `asp.<vtxId>.pii_key` fixes custody
in the next line — *"The key is unique to the `Identity` vertex."*

Note this alternative does **not** dodge §5: a cascade would still have to reach the projection surface,
so the delivery gap is a property of the problem, not of the chosen mechanism.

### 9.2 A sibling pointer aspect (`vtx.appointment.<id>.encounterSubject`)

Carry the subject in a separate non-sensitive aspect instead of an envelope field. Avoids the Contract
#1 edit — its whole appeal. **Rejected:** two keys where one field does; a reader must join them; and
nothing binds the pointer to the ciphertext, so a later op can re-point custody independently of the data
it governs (breaking decryption at best, defeating §4.4 at worst). Avoiding an *honest* contract edit by
splitting one fact across two keys is the contortion the "question whether the convention deserves to
exist" rule exists to prevent. **Worth noting it would not have dodged §5 either** — a sibling aspect
labelled nothing is just as invisible to `ReferencedLabels`.

### 9.3 Resolve the subject by traversal at commit time

Have the DDL declare a relation (`subjectVia: "forPatient"`) and let step 6.5 walk it. **Rejected:** the
walk needs `lnk.appointment.<aid>.forPatient.patient.*` — a prefix enumeration on the **write path**,
which the no-scans invariant forbids, and which `derive_reads` (class (g), pure, KV-free) cannot supply
either. An exact declared read is strictly simpler and strictly safer.

### 9.4 Bind the subject as a node in every secure lens's MATCH (instead of §5.2)

Require a subject-anchored secure lens to `MATCH (subj:identity {key: …})` so `identity` lands in the
label set the way it does today. **Rejected:** it is a *convention* where a *mechanism* belongs — the
guarantee would hold only while every future author remembered, with a silent, shape-dependent erasure
failure as the penalty for forgetting, and no gate able to tell a correct lens from a broken one. §5.2's
injection is fail-closed by construction. (It is also often unexpressible: the subject may not be
reachable by any path the lens's own query needs.)

### 9.5 Extend the ref MAC now (subject-bound `sensitive-ref/v2`)

§6.2. Viable, no consumer, deferred to Increment 5 rather than shipped inert.

### 9.6 Keep `.encounter` plaintext and rely on access control

The status quo. Cannot satisfy right-to-erasure on an immutable ledger — the premise of the whole Vault
plane, already rejected once at `vault-crypto-shredding-design.md`'s "Considered and REJECTED — pre-Vault
plaintext contact projection".

## 10. Adversarial pass (run, folded, closed)

Two independent read-only reviewers ran against the draft (write/commit path; read/projection/shred
path). **The pass changed the design's shape** — it added an increment and re-ordered the consumer
behind it. No pre-build gate is left open by this design. What it moved:

1. **CRITICAL — the shred is never delivered to a subject-anchored secure lens.** My "no new machinery"
   claim was false in the dangerous direction: decrypted PHI would survive erasure with a success
   signal. Now §5, and **Increment 2**, gating clinic. Both gates independently verified
   (`labels.go:31-43`, `pipeline.go:698-703`, `pipeline.go:519-528`).
2. **HIGH — flipping `.encounter` to sensitive silently nulls three shipped lenses**, because step 6.5
   encrypts the whole `data` map and the aspect mixes PHI with projected operational signals. Now §4.5;
   Increment 3 splits the aspect.
3. **HIGH — `subjectKey` is invisible on the Processor read path.** My §7 "no allowlist at either end"
   was false at the third end: `VertexDoc` is a fixed struct and `vertexDocToStarlark` a fixed dict. Now
   §4.1; the field + projection are Increment-1 work.
4. **HIGH — a class-less mutation bypasses both the sensitivity gate and encryption** — pre-existing,
   but this design is what puts PHI behind it. Closed in Increment 1 (§4.2).
5. **MEDIUM — steps 6 and 6.5 can disagree via the shared live-read budget**, failing open to plaintext
   (§4.2). **MEDIUM — a shredded subject passes the gate and bricks the write** with an opaque error
   (§4.2). **MEDIUM — the §4.4 immutability check had three holes** (absent prior, the OCC serializer,
   same-batch dedup) — all three folded into §4.4.
6. **HIGH — custody-absence would DLQ a whole batch**, and for a non-seeded plain lens one legacy
   document would permanently block every other row (§5.2). **MEDIUM — `identityKeyColumn` validates
   against table columns, not cypher aliases**, and nothing at activation checks the RETURN alias
   exists (§4.3). **LOW — §4.4's tombstone rationale named no live reader**: a tombstoned aspect is
   dropped by `readNode` (`executor.go:817-819`) and refused by the Processor
   (`sensitive_decrypt.go:152-160`), so preservation is harmless but the stated reason was
   unsubstantiated — corrected to say what it actually does.

Both reviewers independently confirmed what does hold: `subjectKey` survives the write path verbatim
(`preserveImmutableFields` cannot touch it), `appt.encounter.subjectKey` resolves with zero rule-engine
change (parser and executor both), there is no two-subject or OCC-retry hazard in the piiKey mint path,
the AEAD binding makes wrong-subject decryption impossible, and §3.11 needs no change.

## 11. Decomposition for the Steward (each independently shippable + green)

**Increment 1 — Processor custody (Lattice lane, M–L).** Contract #1 §1.3 + Contract #3 §3.10 (both
staged uncommitted). `VertexDoc.SubjectKey` + Starlark projection (§4.1); step 6's subject rule incl.
the shredded-subject and class-less refusals (§4.2); step 6.5 custody, immutability and the
budget-exhaustion hard error (§4.2, §4.4); step 4 decrypt custody + the loud-error residual (§4.3); the
egress refusal (§6.2). Tests: the positive round-trip, plus a negative per rule — missing / malformed /
undeclared / dead / non-identity / shredded subject, changed-on-update, omitted-on-update, class-less
mutation, same-batch duplicate, egress refusal — each asserting the typed constraint. Security-plane:
full 3-layer review. Ships green with no package consuming it; a complete, provable platform primitive,
not dead scaffolding.

**Increment 2 — Refractor custody delivery (Lattice lane, M). Gates Increment 3.** Custody labels join
the referenced set for a secure lens, across both `NarrowedFilterEligible`/`ConsumerFilter` and
`plainReactsTo`/`evalPlainAspectReprojection` (§5.2); per-row custody-absence stops poisoning the batch
(§5.2); `secureIdentityKeyType` re-homed out of `ActorAwareNarrowingLabels` with its justification
re-derived (§5.3). Test that pins the guarantee: a secure lens anchored on a **non-identity** type, with
custody via a property chain, scrubs to null on a real `ShredIdentityKey` — driven through the real
`handle()` path, mirroring `TestSecureLens_NeighborShredReprojectsAnchoredRows`. Security-plane: full
3-layer review.

**Increment 3 — clinic encounter PHI (Verticals lane, M).** Split `.encounter` into the sensitive PHI
aspect + a non-sensitive operational sibling, and re-point the three lenses at the sibling (§4.5);
`.encounter` gains `sensitive: true`; `RecordEncounter` takes `subjectIdentityKey` and declares the four
exact-key reads (§4.2); `CreatePatient`'s `identityKey` becomes required for encounter-recording
patients; a `clinicEncountersRead` protected Secure Lens projects the note to the treating provider
(`RETURN appt.encounter.subjectKey AS subject_key`, §4.3). **Delivery boundary: full-stack reset** (§8).

**Increment 4 — the retention posture (Lattice lane, M–L).** `retention: "erasable" | "retained"` on the
aspect-type DDL, defaulting `erasable` (the fail-closed direction: forgetting the marker **over-erases**,
which is loud and bounded, where a default-retained silently under-erases). A second per-identity DEK
`vtx.identity.<id>.retainedPiiKey` — same `vault.Envelope`, same lazy-mint-in-batch pattern, shipped by
`privacy-base`; **no Vault interface change and no new key hierarchy**, just another envelope under the
same KEK. `ShredIdentityKey` destroys only the erasable DEK and stamps `retainedPiiKey.retainUntil` (the
clock starts at the erasure *request* — the legally meaningful moment). `ShredRetainedKey`,
operator-invoked. `SecureColumn.custody: "erasable" | "retained"` selecting the aspect
`readPiiKeyEnvelope` reads, validated in the existing fail-closed `validateSecureColumns`, and joined to
§5.2's custody-label set. `shredStatus` surfaces `retainUntil`. Clinic's `.encounter` flips to
`retained`. Security-plane: full 3-layer review.

**Increment 5 — deferred, each behind a named trigger.** (a) Subject-bound ref MAC
(`sensitive-ref/v2`) — trigger: the first package needing to egress event-scoped PII. (b) Automatic
retained-key destruction at `retainUntil` via the `@at` lane — trigger: a real statutory clock, or the
first window approaching expiry. Neither is built before its consumer exists; a schedule fired years out
is untested at that horizon and nothing is near expiry.

---

*Designer fire, 2026-08-02. Awaiting Andrew's ratification. The Contract #1 §1.3 and Contract #3 §3.10
edits are staged **UNCOMMITTED** in `main` — the diff is the proposal.*
