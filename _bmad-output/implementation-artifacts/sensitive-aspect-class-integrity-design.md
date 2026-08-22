# A sensitive aspect's key must decide its class — not the mutation's own word for it

**Status: ❌ REJECTED (Andrew, 2026-08-22) — on a foundational architecture invariant, not a detail.**
The mechanism this design is built on — making a **key segment** (`localName` + `anchorType`) *decide or
constrain* a document's `class` and therefore its sensitivity — **inverts the platform's source-of-truth
posture: the document is authoritative for an entity's type and sensitivity, via its `class` field.
Period.** That posture was established very early and is not up for relitigation; a design may not move type
or sensitivity authority off the document and onto the key. The whole shape (Inc 1's reverse guard, Inc 2's
`(anchorType, localName)→class` binding) is refused on that ground. Not "shelved pending a consumer" —
rejected on invariant.

**What is *not* a defect, restated correctly under the posture.** The runtime behavior the row called a
"fail-open" — an omitted or unresolvable `class` → no DDL → no encryption — is **correct**. A document that
does not declare a sensitive `class` *is not sensitive*; the platform faithfully stored what the document
declared. A writer who wants encryption declares the sensitive class; one who omits it has a **script bug**
in their own package, caught by that package's tests — not a platform hole the Processor should close by
second-guessing the document from its key. There is no arm where a document declares a resolvable sensitive
class and is nonetheless stored plaintext (DD confirmed: all three "arms" are the document declaring itself
non-sensitive). So there is no posture-independent fail-open, and **no code change is warranted.**

**The one real residual — filed separately, and it *reinforces* the posture.** The *committed* Contract #1
§1.5 "Default class" clause promises the Processor defaults an omitted `class` **from the key's local name**.
That clause is (a) unimplemented (census §3.4: the only occurrence is the sentence itself) and (b) itself a
statement of *key-decides-class* — it contradicts document-is-source-of-truth. It should be **deleted** (a
frozen-contract doc fix, Andrew's commit), not replaced with a new key-binding rule. Board row for that is
`🔭 flag-for-Andrew`. The contract edit this design staged is **reverted** in full.

*Original framing below, retained for the record — do not build it.*

**Components (as originally scoped):** Processor (`internal/processor/{step6_validate,step65_encrypt,ddl_cache,health}.go`),
pkgmgr (`internal/pkgmgr/{definition,build,manifest}.go` + a new validator), `cmd/lattice/identity`.
**Filed by:** [ddl-cache-invalidation-fault-signal-design.md](ddl-cache-invalidation-fault-signal-design.md) §1,
whose security review found this arm and correctly refused to close it in-line.

---

## For Andrew

**What it does.** Today a Starlark script decides both *where* a sensitive aspect is written
(`vtx.identity.<id>.ssn`) and *what it calls itself* (`class: "ssn"`), and the Processor trusts only the
second. Omit or misstate `class` and the platform commits PII/PHI as **plaintext** while every gate reports
success — and `ShredIdentityKey` then destroys a DEK that never protected anything, so the erasure
attestation is true and the data is still legible. This design makes the **aspect key** the authority: a
sensitive aspect DDL declares the `(anchorType, localName)` address it governs, the Processor builds the
reverse index, and a mutation landing on that address whose `class` is not the governing class is
**refused** at step 6.

**Frozen-contract change (the one thing needing your signature).** Two clauses in
`docs/contracts/01-addressing-and-envelope.md`, edit staged uncommitted:

1. **§1.5 "Default class" is fiction.** It promises that an omitted `class` defaults to the aspect's local
   name. **Nothing implements it** — the only occurrence of the rule in the repo is the contract sentence
   itself (census §3.4). Worse, the platform's own package corpus deliberately does the opposite:
   `packages/clinic-domain/ddls.go:26-33` namespaces aspect *classes* (`providerProfile`) precisely so the
   *local names* can stay generic (`.profile`), because §1.5 makes canonicalNames globally unique. Five
   distinct DDLs across four packages write `.profile`. Implementing the clause as written would bind
   DDLs — and their `permittedCommands` — to writes that pass today, retroactively, corpus-wide. The edit
   **corrects the clause to what the platform does** and points at the new binding rule instead.
2. **§1.6 "Consequences for sensitive aspects"** gains the key-binding declaration and the integrity rule.

**No fork.** Enforcement point, index shape, and declaration surface are all resolved in §5–§7 with
grounded reasoning; none is product, architecture, or scope altitude.

**One judgement call worth your eye (not a fork, but it is the design's cost).** Increment 2 makes an
undeclared key binding a **fail-closed install refusal** for a sensitive DDL. Three shipped package DDLs
must add one line each. That is deliberate per the lint doctrine — a convention with no gate is the next
author's default mistake — but it is a new refusal on the package-authoring path, and you have rejected
"a lot of build for not much gain" before. §5.3 argues why the smaller authorship-only shape does not
close this one.

---

## 1. The defect, precisely

`internal/processor/step65_encrypt.go:59-62`:

```go
class, _ := m.Document["class"].(string)
if class == "" {
    continue
}
```

and `internal/processor/step6_validate.go:250-260`:

```go
if v, ok := m.Document["class"].(string); ok { class = v }
...
if class != "" {
    if ref, ok := v.resolveGoverningDDL(ctx, class, m.Key, kind, result, state); ok {
```

Both steps take the mutation document's self-reported `class` as the sole input to governing-DDL
resolution. `internal/processor/starlark_runner.go:350` copies the whole `document` sub-dict verbatim out
of the script's return value (`starlarkDictToGoMap(dd)`) — `class` is neither validated, defaulted, nor
cross-checked against anything. The aspect's own key is parsed at `step6_validate.go:302`
(`substrate.ParseAspectKey`) and its **localName return value is explicitly discarded**
(`_, parentType, _, _, ok := ...`); only the parent *type* segment is used, by
`validateSensitiveCustody` (`step6_validate.go:326-368`).

So three mutation shapes at a genuinely sensitive address commit plaintext, and none of them errors:

| # | The mutation | Resolution | Outcome today |
|---|---|---|---|
| A | `key: vtx.identity.<id>.ssn`, **no `class` field** | step 6 skips the whole DDL block; step 6.5 `continue`s | plaintext at rest, no signal |
| B | same key, `class: "note"` (no such DDL) | `DDLs.Lookup` miss → the aspect chain-walk terminal must be a **vertexType** DDL (`step6_resolve_ddl.go:298-305`), so no aspect DDL is ever reached and `ref.Sensitive` is false | plaintext at rest, no signal |
| C | same key, `class: "appointmentStatus"` (a real, non-sensitive DDL) | resolves cleanly; `!ref.Sensitive` → `continue` | plaintext at rest, no signal |

`ddl-cache-invalidation-fault-signal-design.md` §1 already retracted the claim that this is Contract #1
§1.6's sanctioned permissive default: §1.6 covers *a class with no DDL declared* and *undeclared aspects*.
Neither describes a mutation that **misstates** `class` for a key whose governing DDL is sensitive.

### 1.1 Why this is worse than "the data is unencrypted"

Four consequences, each cited:

1. **Crypto-shredding is defeated, and reports success.** Erasure destroys the DEK
   (`internal/vault/local.go:385`, `packages/privacy-base`'s `ShredIdentityKey`); the ciphertext stays and
   becomes unrecoverable. An aspect that was **never ciphertext** is untouched by that operation. The
   erasure seal, the residue lens, and the attestation all converge green over legible PII. This is the
   harm class this design exists for.
2. **The external-egress guard silently disengages.** `sensitiveReadTracker.markPlaintext`
   (`internal/processor/sensitive_decrypt.go:35-43`) is only reached on the sensitive branch of
   `decryptSensitiveDoc` (`:154-157`, `if !ok || !ref.Sensitive { return nil }`). A plaintext aspect whose
   class says non-sensitive never marks, so step 6's `external.*` emission guard cannot fire — the PII may
   be handed to an outbound event as an ordinary value.
3. **Decrypt-on-read conceals it.** The same `!ok || !ref.Sensitive` early return serves the plaintext
   `data` map to the script untouched. (The *opposite* pairing — plaintext at rest with a class that says
   sensitive — fails **closed**: `vault.KeyHolder` on an empty `KeyID` returns `ErrInvalidEnvelope`
   (`internal/vault/keyholder.go:35-37`) and the read errors. So the only reachable steady state is the
   silent one.)
4. **Projection is mixed, and today's corpus is lucky.** A **Secure Lens** fails closed on plaintext:
   `SecureDecryptor.decryptColumn` refuses a value that is not a ciphertext envelope map
   (`internal/refractor/pipeline/secure.go:190-202`, `failure.Terminal`) → the column nulls and a
   `SecureRedaction` is recorded. A **plain** lens has no such check — it projects whatever the cypher
   returns. **This is latent, not live:** the one plain-lens cypher that touches a sensitive aspect,
   `readinessWithItems`' `id.ssn.data AS ssnVal` (`packages/lease-signing/lenses.go:648`), uses `ssnVal`
   **only inside boolean predicates** (`:745-774`, `:912-916`, `:1043`) and never returns it as a column,
   so no live plain lens would carry the plaintext into a read model today. It would take one lens edit.

### 1.2 What holds it together today, and why that is not a mechanism

Every one of the twelve live sensitive-aspect writers passes a **string literal** for both the key's local
name and the class, side by side, in the same call (§3.2). It works because the same author wrote both.
That is an authorship convention with no enforcement anywhere — the census found **zero** comparisons of a
localName against a class anywhere in the repo (§3.3) — which is exactly the *guarantee that holds by
accident of the corpus's shape* this design has to convert into a mechanism. The live driver that makes
"the author gets it right" no longer sufficient is
[ai-authored-capabilities-design.md](ai-authored-capabilities-design.md): a capability proposal materializes
DDL scripts at **runtime**, outside any package source a linter can read.

---

## 2. Non-goals

- **No change to Contract #1 §1.6's permissive default for undeclared aspects.** An aspect at an address
  no sensitive DDL claims is untouched by this design, in every arm.
- **No re-encryption / migration of data already at rest.** A write-path guard cannot reach a committed
  plaintext aspect. §8 ships a **read-only detector** instead; the remediation verb is not designed here.
- **No change to the sensitive/custody *anchoring* rule** (`validateSensitiveCustody`). This design adds an
  orthogonal check; the anchoring rule keeps its exact current semantics.
- **No change to how the class is chosen for the *operation*** (`step4_hydrate.go`'s `resolveClass` /
  `ClassForCommand`). That is script selection, a different trust question.
- **Uninstall / DDL-retirement semantics for a key binding** follow the existing `.sensitive` posture
  verbatim (§6.3); no new retirement verb.

---

## 3. Grounding ledger (executable censuses)

Every count below ships as the command that derives it. Phase-0 of the build **re-runs each one** and
compares; a divergence is a scope finding, not a rounding error.

### 3.1 The sensitive-aspect population — N = 12, of which 2 have localName ≠ canonicalName

```bash
grep -rn "Sensitive: *true" packages/ | grep -v _test
```

Expected: 12 `pkgmgr.DDLSpec` hits plus **two classifiable non-hits** —
`packages/identity-domain/opmetas.go:321` is `OpMetaSpec.Sensitive`
(`internal/pkgmgr/definition.go:538`, a UI masked-entry flag that reaches no key), and the
`internal/pkgmgr/*_test.go` literals are the pkgmgr validators' own fixtures.

| # | Declaration | CanonicalName | Custody | Written at | localName | == name? |
|---|---|---|---|---|---|---|
| 1–9 | `packages/identity-domain/ddls.go:257,280,303,327,351,375,399,423,451` | `ssn`, `dob`, `name`, `email`, `phone`, `claimKey`, `linkKey`, `credentialBinding`, `idpBinding` | identity (zero value) | `vtx.identity.<id>.<name>` | same | **yes, all 9** |
| 10 | `packages/lease-signing/ddls.go:322` | `applicantProfile` | retentionClass `underwritingRecord` | `vtx.leaseapp.<id>.profile` | `profile` | **NO** |
| 11 | `packages/lease-signing/ddls.go:384` | `underwritingParties` | retentionClass `underwritingRecord` | `vtx.leaseapp.<id>.underwritingParties` | same | yes |
| 12 | `packages/clinic-domain/ddls.go:944` (`encounterAspectDDL`, `:45`) | `appointmentEncounter` | retentionClass `clinicalRecord` | `vtx.appointment.<id>.encounter` | `encounter` | **NO** |

**The split is exactly the custody split**: all nine identity-custody DDLs have localName ==
canonicalName; two of the three retention-class ones do not. §5.2 is built on that fact and §7.1 pins it.

### 3.2 The writer sites — key and class are two independent literals

```bash
grep -rn 'make_aspect[a-z_]*([a-zA-Z_0-9]*, *"profile",' packages/ | grep -v _test
```

`packages/lease-signing/scripts.go:1102-1103` is the clearest specimen — the two shapes side by side in
one statement:

```python
mutations = [make_aspect_upsert(app_key, "profile", "applicantProfile", profile_data),
             make_aspect_upsert(app_key, "underwritingParties", "underwritingParties", underwriting_parties_data)]
```

`make_aspect_upsert(vtx_key, local_name, cls, data)` (e.g. `packages/clinic-domain/ddls.go:1039-1045`)
takes `local_name` and `cls` as **structurally independent parameters** and never derives one from the
other. `packages/clinic-domain/ddls.go:3004` writes `("encounter", "appointmentEncounter")` the same way.
The identity-domain writers use inline dict literals
(`packages/identity-domain/ddls.go:2198-2202` for `.ssn`/`.dob`; `:1263-1287` for `.name`/`.email`/
`.phone`/`.claimKey`; `:1439`, `:1709`, `:1562`) with the same independence.

### 3.3 No class↔localName comparison exists anywhere

```bash
grep -rEn 'localName *[!=]= *class|class *[!=]= *localName|ClassForLocalName|classFromLocalName' internal/ cmd/ packages/
```

Expected: **zero**. The only two sites that read `ParseAspectKey`'s localName at all compare it to a
hardcoded constant — `step6_validate.go:483-486` (`"canonicalName"`, the reserved-meta-name gate) and
`internal/refractor/lens/emit_ddl.go:47-48` (`"spec"`). `MetaVertexRef`
(`internal/processor/ddl_cache.go:27-79`) has no localName field; `DDLSpec`
(`internal/pkgmgr/definition.go:768+`) has none either.

`packages/identity-domain/record_pii_test.go:680-716` is the pin that proves the *existing* rule is
anchor-type-only: a mutation that is **self-consistent** (`class:"ssn"`, localName `ssn`) is still
rejected — because it landed on `vtx.lease.<id>.ssn` and `validateSensitiveCustody` checks the parent
type. Nothing checks the pairing.

### 3.4 Contract #1 §1.5's "Default class" clause is implemented nowhere

```bash
grep -rn 'Default class\|default class\|implicit class\|defaultClass' internal/ cmd/ packages/ docs/
grep -rEn 'Document\["class"\] *=' internal/ cmd/
```

Expected: exactly one hit, the contract sentence at
`docs/contracts/01-addressing-and-envelope.md:154`; zero assignments. This is the
*committed-but-unimplemented clause is fail-open* shape — the contract promises a defaulting the code does
not perform, and every reader of the contract (a package author, an AI capability proposer, a future
designer) is entitled to rely on it.

### 3.5 localName collisions — a localName alone cannot name a class

```bash
for n in profile status schedule; do grep -rn "make_aspect[a-z_]*([a-zA-Z_0-9]*, *\"$n\", *\"[A-Za-z]*\"" packages/ | grep -v _test; done
```

| localName | distinct classes | citations |
|---|---|---|
| `.profile` | **5** — `providerProfile`, **`applicantProfile` (SENSITIVE)**, `instructorProfile`, `serviceProviderProfile`, `studioProfile` | `clinic-domain/ddls.go:1577`, `lease-signing/scripts.go:1102`, `wellness-domain/ddls.go:3944`, `service-domain/ddls.go:793`, `wellness-domain/ddls.go:1637` |
| `.status` | 3 — `appointmentStatus`, `bookingStatus`, `tabStatus` | `clinic-domain/ddls.go:2814`, `wellness-domain/ddls.go:3422`, `cafe-domain/ddls.go:1144` |
| `.schedule` | 2 — `appointmentSchedule`, `sessionSchedule` | `clinic-domain/ddls.go:2701`, `wellness-domain/ddls.go:2559` |

`.profile` is the decisive one: it is shared by a **sensitive** DDL on `leaseapp` and four non-sensitive
ones on other vertex types. **A reverse index keyed on localName alone would refuse four shipped,
correct, non-sensitive writes.** The binding must be qualified by the anchor vertex type. This is not a
hypothetical: it is the live corpus, and `packages/clinic-domain/ddls.go:26-33` states it as intended
platform doctrine, not debt.

### 3.6 No live writer is non-conformant at any of the nine derived addresses

```bash
for n in ssn dob name email phone claimKey linkKey credentialBinding idpBinding; do
  echo "== .$n"; grep -rn "+ *\"\.$n\"" packages/ internal/ cmd/ | grep -v _test; done
```

Expected: every hit is `packages/identity-domain`, `packages/identity-hygiene`,
`packages/clinic-domain/ddls.go:1187` (`make_aspect_upsert(identity_key, "name", "name", …)` — class-
conformant), or a read/contextHint site (`internal/identityceremony/contexthint.go:67,95`). **Zero
non-conformant writers.** This is the census that decides whether Increment 1 can ship blocking, and it
is a *reassuring negative* — so it is written here as a command, not a claim. If Phase-0 finds a hit, that
writer is a **live victim**, not an obstacle: fix it in the same fire and say so.

### 3.7 The aspectType DDL population

```bash
grep -rn 'Class: *"meta.ddl.aspectType"' packages/ | grep -v _test | wc -l
```

Expected ≈ **76**. Only the 12 sensitive ones are governed by this design at Increment 2; the other ~64
may declare a binding but are not required to (§5.4).

---

## 4. Reconciliation with the existing mental model

**"Didn't we already handle this?"** Three mechanisms look adjacent and none covers it:

- `validateSensitiveCustody` (`step6_validate.go:326-368`) checks the aspect's **parent vertex type**, and
  only *after* the class has already resolved to a sensitive DDL. A class that resolves to nothing, or to
  a non-sensitive DDL, never reaches it. It is fail-closed **within** the sensitive path and silent
  **about** entry to it.
- `degradedCacheRefusal` (`step65_encrypt.go:188-206`) closes the *cache-staleness* arm — the DDL is
  declared sensitive and this process cannot see it. Here the cache is perfectly healthy; the mutation
  simply asked the wrong question.
- `SecureDecryptor` (`secure.go:190-202`) refuses plaintext in a declared secure column. It is a
  **per-lens** declaration, independent of the DDL (as `ddl_cache.go:731-740` states), so it protects only
  what a Secure Lens projects, and only on the read side.

**"Does this duplicate or contradict an established pattern?"** No — it *completes* one. The platform
already treats the write-path key as authoritative for the anchor **type** segment
(`validateAbstractKeySegments`, `validateSensitiveCustody`, `validateReservedTypeRegistration`). This
design extends the same authority to the **localName** segment, for the one population where getting it
wrong is a confidentiality failure.

**"Does this introduce new state — do we already keep it somewhere?"** One new derived index on
`DDLCache`, whose lifetime is identical to `byCommand`'s and rebuilt by the same two functions (§6.4). One
new declaration on `DDLSpec`, emitted as one more meta-vertex aspect beside `.sensitive` and `.custody`
(`build.go:174-191`) — the shape shipped end-to-end four days ago for `weaverTarget.description`
(`8f49c13b`).

**"Is a parallel design touching this seam?"** It was — it landed while this design was being written; see §12.

---

## 5. The shape

### 5.1 The rule, in one sentence

> An aspect DDL may declare the **key address** it governs — a `localName` plus the set of **anchor vertex
> types** it may attach to. For a **sensitive** aspect DDL the declaration is **required**, and it becomes
> exclusive: a mutation landing on a declared sensitive address must carry that DDL's class, and a
> mutation carrying that DDL's class must land on that address.

Two directions, deliberately separate because they have different fail modes:

- **Reverse (exclusivity) — the security rule.** If a mutation's key `(anchorType, localName)` is claimed
  by a sensitive aspect DDL `S`, the mutation's `class` must be exactly `S.CanonicalName`. Otherwise
  refuse. This is the arm that closes A, B and C in §1.
- **Forward (conformance) — the hygiene rule.** If a mutation's `class` resolves *by exact lookup* to an
  aspect DDL `D` that **declares** a binding, the key's localName must equal `D`'s and the key's anchor
  type must be in `D`'s anchor set. Otherwise refuse. This catches a sensitive class written to an address
  no reader will ever look at — data encrypted under a holder nothing decrypts.

The forward rule fires **only on a declared binding**, never on a derived one (§5.2). That asymmetry is
the design's fail-safety: a *derived* binding can only ever add protection; it can never refuse a write
that is already class-conformant.

### 5.2 Where a binding comes from: derived first, declared second

| Source | Applies to | Contributes to reverse index | Drives forward refusal |
|---|---|---|---|
| **Derived** — `localName := CanonicalName`, `anchorTypes := ["identity"]` | a sensitive aspect DDL with custody kind `""`/`identity` and **no** `.keyBinding` aspect | **yes** | **no** |
| **Declared** — the `.keyBinding` aspect | any aspect DDL that carries one | yes (if sensitive) | yes |

The derived rule is not a convention guess: `validateSensitiveCustody`
(`step6_validate.go:328-338`) **already refuses** an identity-custody sensitive aspect on any parent type
other than `identity`, so `["identity"]` is the enforced anchor set, not an assumption. And §3.1 verifies
that all nine identity-custody DDLs have `localName == CanonicalName` — measured, not assumed.

**Why this matters:** the derived rule covers **9 of the 12** live sensitive DDLs **the moment the binary
ships**, with no package edit, no version bump, and no reinstall. That dissolves most of the
*a container default is not retroactive* hazard: the population an install-time declaration cannot reach
is the already-installed one, and here nine twelfths of it needs no install at all. The remaining three
(all retention-class custody, whose anchor set is genuinely unknowable without a declaration —
`step6_validate.go:340-343`: *"Any anchor is permitted"*) are covered once their package is upgraded, and
§8 makes that gap **observable** rather than silent.

### 5.3 Enforcement point: step 6, with a step-6.5 mirror

**Primary refusal in step 6** (`validateOne`), as a `*DDLViolation` with
`ViolatedConstraint: "aspectClassBinding"`. It is a write-gate constraint, it gets the
`ErrCodeDDLViolation` reply the submitter can act on, and it sits in the same per-mutation loop as the
three existing key-derived checks. `validateReservedTypeRegistration` (`step6_validate.go:471-501`) is the
shape precedent: parse via `substrate.ParseAspectKey`, compare a key segment against an expected string,
return a `*DDLViolation`.

**Mirror assertion in step 6.5**, beside `degradedCacheRefusal` and gated the same way
(`kind == substrate.KindAspect`). Not ceremony: `Invalidate` is called from step 8 of *other* operations,
so the cache can change **between** the two steps — the reason `keyHolderFor`'s failure is already a hard
error at `step65_encrypt.go:110-118` rather than a trusted step-6 invariant. The mirror must run **before**
`step65_encrypt.go:59-62`'s `if class == "" { continue }`, or arm A walks straight past it.

**Why commit-time and not authorship-time only.** The enforcement-point rule says a commit-time guard is
for a security invariant that must hold against a hostile or careless author *regardless of path*, and
that lifecycle hygiene belongs to the authoring tool. This is the former on both counts: the failure is
silent, it is not operator-recoverable (plaintext PII, once read, is out; and an erasure over it is
attested), and there is a live authoring path a linter cannot see —
`ai-authored-capabilities-design.md`'s runtime-materialized DDL scripts. The counter-consideration is
real and is honoured: the guard is **one map lookup on a key step 6 has already parsed**, zero reads, and
the *declaration* half is placed at authorship time exactly as the rule prescribes.

### 5.4 What is deliberately NOT governed

- **A non-sensitive aspect DDL may declare a binding, but need not.** Requiring all 77 would be a
  76-line migration for a hygiene check on a population where a mismatch costs nothing. If one declares,
  the forward rule applies to it — opt-in strictness.
- **An undeclared aspect** (no DDL at all) is untouched — Contract #1 §1.6 unchanged.
- **A later sensitive DDL declaring a binding at an address an existing plain aspect already uses** will
  refuse those plain writes. That is **correct semantics, not a regression**: declaring an address
  sensitive is precisely the statement that a plaintext write there must not commit. The install-time
  validator refuses a *sensitive-vs-sensitive* collision (§7.2); a sensitive-vs-plain collision is
  surfaced by the detector (§8.1), not silently tolerated.

---

## 6. Data model and mechanism, hop by hop

### 6.1 The declaration (`internal/pkgmgr/definition.go`)

```go
// AspectKeyBinding declares the aspect key address a business write to this
// DDL's class must use: vtx.<anchorType>.<id>.<localName>.
type AspectKeyBinding struct {
    // LocalName is the aspect key's 4th segment. Empty defaults to the DDL's
    // CanonicalName.
    LocalName string
    // AnchorTypes is the set of parent vertex types the aspect may attach to.
    // Empty is legal only for custody kind identity, where it resolves to
    // ["identity"] — the set validateSensitiveCustody already enforces.
    AnchorTypes []string
}
```

added to `DDLSpec` as `KeyBinding AspectKeyBinding` beside `Sensitive` / `Custody`.

The three migrations, one line each:

| Package | DDL | Declaration |
|---|---|---|
| `packages/lease-signing/ddls.go:322` | `applicantProfile` | `KeyBinding: pkgmgr.AspectKeyBinding{LocalName: "profile", AnchorTypes: []string{"leaseapp"}}` |
| `packages/lease-signing/ddls.go:384` | `underwritingParties` | `KeyBinding: pkgmgr.AspectKeyBinding{AnchorTypes: []string{"leaseapp"}}` |
| `packages/clinic-domain/ddls.go:944` | `appointmentEncounter` | `KeyBinding: pkgmgr.AspectKeyBinding{LocalName: "encounter", AnchorTypes: []string{"appointment"}}` |

The nine identity-domain DDLs declare explicitly too (Increment 2), so the source is unambiguous even
though the derived rule would already cover them; that is what makes the runtime fallback purely a
compatibility path for already-installed deployments rather than the load-bearing mechanism.

### 6.2 The transport (installer → Core KV → cache)

One conditional aspect emission in `internal/pkgmgr/build.go`, immediately after the `.custody` block
(`:186-191`), mirroring it exactly:

```go
if b, ok := resolveKeyBinding(d); ok {
    addCreate(ddlKey+".keyBinding", docAspect(ddlKey, "keyBinding", "keyBinding",
        map[string]any{"localName": b.LocalName, "anchorTypes": b.AnchorTypes}))
}
```

`resolveKeyBinding` materializes the defaults (`LocalName` ← `CanonicalName`; `AnchorTypes` ←
`["identity"]` for identity custody) so the committed aspect is always fully explicit — the same posture
`.custody` takes in carrying the *resolved* holder key rather than the class name, for the same reason:
the commit path must not re-derive anything.

An in-place upgrade needs no new code: `diffManifest` (`internal/pkgmgr/upgrade.go:378-519`) partitions by
key set, so a newly-emitted aspect key is an ordinary create and a withdrawn one an ordinary tombstone.
This is the identical end-to-end path `weaverTarget.description` shipped on (`8f49c13b`).

### 6.3 The reader (`internal/processor/ddl_cache.go` `loadMetaVertex`)

A `.keyBinding` reader beside the `.sensitive` reader (`:762-802`), and it takes **`.sensitive`'s
tombstone posture, not `.custody`'s**:

> A tombstoned `.keyBinding` is read as **LIVE**. Three of the four sibling readers
> (`permittedCommands`, `custody`, `script`) read a tombstone as absent because staying live
> **over-grants**. This one is the mirror image, exactly as `.sensitive` is: dropping a binding
> **removes a refusal**, so honouring the withdrawal is the over-exposing direction. A package that
> genuinely means to move an aspect's address changes the declaration (an update, which *is* honoured);
> it does not withdraw it. Logged at WARN when tombstoned-but-present, same as `.sensitive`.

`MetaVertexRef` gains `KeyBinding AspectKeyBinding` (zero value = no declared binding). An unparseable
`.keyBinding` **poisons the entry** the way an unparseable `.custody` does (`:824-830`): the binding
resolves to a sentinel no mutation can satisfy, so every write to that class is refused loudly with
nothing at rest — rather than zeroing (which would silently disarm the guard).

### 6.4 The index (`DDLCache`) — state lifetime table

`bySensitiveAspectAddress map[aspectAddress]string`, where
`aspectAddress struct{ AnchorType, LocalName string }` and the value is the governing canonicalName.
Built by a new `buildBySensitiveAddress(byName, logger)`, called from exactly the two sites
`buildByCommand` is (`ddl_cache.go:489` in `Refresh`, `:1047` in `Invalidate`).

| State | Created | Reset | Carried across | Ordered relative to |
|---|---|---|---|---|
| `bySensitiveAspectAddress` | empty map at `NewDDLCache` (mirrors `byCommand`, `:384`) | Rebuilt **wholesale from `byName`** on every `Refresh` and every `Invalidate` — never upserted, so a withdrawn or re-addressed binding cannot leave a stale entry (the retraction-transport rule: a shrinking row-set needs a rebuild, not an overwrite) | In-process only; a restart rebuilds from Core KV via `Refresh`. No persistence, no replay ordering | Written under the same `mu` as `byName`/`byCommand`, in the same critical section, immediately after `indexByCanonicalName`. Read under `RLock` by step 6 and step 6.5 |
| `MetaVertexRef.KeyBinding` | `loadMetaVertex`, per root | Replaced wholesale when the root reloads | Same lifetime as `Sensitive`/`CustodyKind` on the same ref | Set before the ref enters `byRoot`; the index derives from `byName`, so it is always consistent with what `Lookup` answers |

**Ambiguity fails CLOSED, and differs from `byCommand` on purpose.** `buildByCommand`
(`ddl_cache.go:1167-1224`) *drops* an operationType two DDLs claim — correct for a dispatch index whose
consumer then demands an explicit class. For a **security** index, dropping the ambiguous entry removes
the guard. So: two sensitive DDLs claiming one address is refused **at install** (§7.2), making the
runtime case unreachable; if it is nonetheless observed at load (a hand-seeded or mid-migration bucket),
`buildBySensitiveAddress` keeps a **poison** entry that refuses every class at that address and logs at
WARN. Borrowing the index shape from `byCommand` does not mean borrowing its tolerance.

### 6.5 The two checks

```go
// step6_validate.go, inside validateOne, BEFORE the `if class != ""` block.
func (v *ValidatorImpl) validateAspectClassBinding(m MutationOp, kind substrate.KeyKind, class, rid string) *DDLViolation
```

```go
// step65_encrypt.go, beside degradedCacheRefusal, called BEFORE the class=="" skip.
func aspectBindingRefusal(cache *DDLCache, mutationKey, class string) error
```

Both gated on `kind == substrate.KindAspect` (a vertex or link key has no localName to bind, and the
existing `keyHolderFor` gate already refuses a non-aspect carrying an aspect class).

---

## 7. The state table (every shape, decided)

For an aspect mutation at `K = vtx.T.<id>.L` with declared class `C`; `S` = the sensitive DDL whose
binding claims `(T, L)`, if any; `D` = the DDL `C` resolves to by **exact** `Lookup`.

| # | Op / document | `C` | `(T,L)` claimed by `S`? | Today | New | Why |
|---|---|---|---|---|---|---|
| 1 | `tombstone` | — | yes | skipped | **allow** | no data to encrypt; retracting a sensitive aspect is legal and the body-preserving tombstone is already the erasure model's business |
| 2 | `create`/`update`, `Document == nil` | — | yes | skipped (`step65_encrypt.go:57`) | **allow** | `parseMutations` permits a bodyless create; there is no plaintext. Refusing would invent a refusal for a shape nothing writes |
| 3 | `create`/`update` | `""` | yes | **plaintext** | **REFUSE** (reverse) | §1 arm A |
| 4 | `create`/`update` | `S` | yes | encrypt | **allow**, unchanged | conformant — the 12 shipped writers |
| 5 | `create`/`update` | resolves to a non-sensitive `D` | yes | **plaintext** | **REFUSE** (reverse) | §1 arm C |
| 6 | `create`/`update` | resolves to nothing | yes | **plaintext** | **REFUSE** (reverse) | §1 arm B |
| 7 | `create`/`update` | `S`, but `S`'s binding is **derived** | no (`(T,L)` unclaimed) | encrypt | **allow**, unchanged | derived bindings are reverse-only (§5.2); this is the identity-domain compatibility path |
| 8 | `create`/`update` | `D` **declares** a binding, `L` ≠ `D.LocalName` | no | encrypt (if sensitive) | **REFUSE** (forward) | ciphertext at an address no declared reader resolves |
| 9 | `create`/`update` | `D` **declares** a binding, `L` matches, `T` ∉ `D.AnchorTypes` | no | encrypt / permissive | **REFUSE** (forward) | the anchor set is the retention-class DDLs' only anchor bound (§5.2) |
| 10 | `create`/`update` | anything | no, and `C` declares nothing | permissive default | **unchanged** | §1.6 untouched |
| 11 | vertex or link key carrying `C = S` | `S` | n/a | `keyHolderFor` refuses → skip | **unchanged** | existing gate, `step65_encrypt.go:221-224` |
| 12 | `create`/`update` at a **poisoned** address (§6.4) or with a poisoned `KeyBinding` (§6.3) | any | — | n/a | **REFUSE** | undecidable ⇒ nothing at rest |

Rows 1, 2, 7 and 10 are the ones that make this safe to ship blocking: every allow is either "no plaintext
exists" or "today's behaviour, unchanged".

---

## 8. Observability: making the uncovered set visible

A write-time guard cannot reach data already at rest, and cannot reach a class whose package has not been
upgraded. Both gaps get a **reader**, because an unobserved gap is indistinguishable from no gap.

### 8.1 Runtime: an unbound-sensitive-DDL health issue

`DDLCache` exposes `UnboundSensitiveClasses() []string` — every `Sensitive` aspect DDL for which neither a
declared nor a derived binding resolved (in practice: retention-class custody, `.keyBinding` absent).
`internal/processor/health.go` raises `SensitiveClassUnbound` from `reconcileIssues`, mirroring
`DDLCacheDegraded` (`health.go:340-345`) verbatim — the same `active[...] = activeIssue{...}` shape, wired
from the `ddlCache` field already attached at `:118`/`:192`.

Severity **`warning`**, not `error`: the guard is inert for that class, which is today's behaviour, not a
new functional degradation — unlike `DDLCacheDegraded`, which actively rejects legitimate writes. The
message names the class and the remedy (upgrade the owning package).

### 8.2 One-shot: a read-only plaintext audit

`lattice identity audit-sensitive-aspects` — a sibling of `sweep-credential-residue`
(`cmd/lattice/identity/credential_residue.go:75`), same shape and the same read-only posture. For every
address in the reverse index it enumerates the live aspects and reports any whose `data` is **not** a
ciphertext envelope (no `keyId` — the same discriminator `vault.KeyHolder` uses,
`internal/vault/keyholder.go:35-37`). Reports; does not remediate. It answers the one question the write
guard cannot: *is there already a victim?* The expected answer is zero — §3.6 finds no non-conformant
writer — and the point of the tool is that "expected zero" is then **measured** before an erasure
attestation leans on it.

The remediation verb (re-encrypt in place, or tombstone-and-rewrite) is deliberately **not** designed
here: it is a decision about existing at-rest data, and it should be designed against a non-empty finding,
not a hypothetical one.

---

## 9. Contract surface

Both edits are to `docs/contracts/01-addressing-and-envelope.md`, staged **UNCOMMITTED** in `main`.

**§1.5 — "Default class".** Today: *"If a write submission omits the `class` field, the Processor uses the
entity's local name … as the implicit class."* Nothing implements it (§3.4) and the corpus deliberately
contradicts it (§3.5). The edit replaces the promise with the behaviour — an omitted `class` resolves no
governing DDL and takes the §1.6 permissive default — and points at the §1.6 binding rule for the
sensitive case. **This is a correction of a fail-open contract clause, not a new constraint.**

**§1.6 — "Consequences for sensitive aspects".** The edit adds: an aspect-type DDL may declare a key
binding `(localName, anchorTypes)`; for a sensitive aspect DDL it is required and exclusive; a mutation on
a bound address whose class is not the governing class is rejected at commit step 6; identity-custody
DDLs whose declaration predates this clause resolve a derived binding from their canonicalName and the
`identity` anchor the custody rule already enforces.

No other contract is touched. Contract #3 §3.10 (step 6.5's encrypt disposition) is **unchanged** — this
design changes what reaches step 6.5, not what step 6.5 does.

---

## 10. Test strategy

Every test below is owned by a named increment (§13); none is left unowned.

| Test | Proves | Owner |
|---|---|---|
| `TestStep6_SensitiveAddress_RefusesEmptyClass` | arm A (row 3) | Inc 1 |
| `TestStep6_SensitiveAddress_RefusesUnresolvableClass` / `…_RefusesNonSensitiveClass` | arms B, C (rows 5, 6) | Inc 1 |
| `TestStep6_SensitiveAddress_AllowsGoverningClass` | the **positive vector** — row 4 does not regress | Inc 1 |
| `TestStep65_SensitiveAddress_RefusesBeforeEmptyClassSkip` | the mirror runs before `:59-62`; **mutation-verified** by reverting the ordering | Inc 1 |
| `TestStep6_DerivedBinding_DoesNotDriveForwardRefusal` | row 7 — a derived binding cannot refuse a conformant write | Inc 1 |
| `TestDDLCache_SensitiveAddressIndex_RebuiltOnInvalidate` | §6.4's wholesale rebuild: a re-addressed binding leaves no stale entry | Inc 1 |
| `TestDDLCache_SensitiveAddressIndex_AmbiguityPoisons` | §6.4's fail-closed ambiguity, distinct from `byCommand`'s drop | Inc 1 |
| **Census pin** `TestSensitiveDDLs_AddressCensus` | §3.1's table — 12 DDLs, 9 identity-custody with localName == canonicalName. Fails when a 13th lands undeclared | Inc 1 |
| `TestPkgmgr_SensitiveDDL_RequiresKeyBinding` + a passing declared case | §7.2's install refusal, with its positive vector | Inc 2 |
| `TestPkgmgr_SensitiveAddressCollision_Refused` | two sensitive DDLs on one address | Inc 2 |
| `TestBuild_EmitsKeyBindingAspect` (shape mirror of `build_test.go:523`'s `.sensitive` pin) | the transport's first hop | Inc 2 |
| `TestDDLCache_KeyBindingTombstone_ReadAsLive` / `…_UnparseablePoisons` | §6.3's two postures | Inc 2 |
| `TestUpgrade_AddsKeyBindingAspectInPlace` | the migration reaches an installed package (mirrors `TestUpgrade_PreservesRetentionClassHolderOnRemoval`) | Inc 2 |
| `TestHealth_SensitiveClassUnbound` | §8.1 raises and clears | Inc 3 |
| **e2e, ephemeral stack** — install clinic-domain, `RecordEncounter` with the class omitted ⇒ rejected; with the class ⇒ committed as ciphertext | the whole spine, live | Inc 2 |

The mutation-verification obligation on `TestStep65_…RefusesBeforeEmptyClassSkip` is not optional: a
guard placed after the `continue` compiles, passes a naive test, and protects nothing.

---

## 11. Alternatives considered

**A1 — a lint gate over `packages/**` only.** Cheapest: a `scripts/lint-*.go` rule that a
`make_aspect*(key, local, cls, …)` call's `cls` matches the sensitive DDL's declared address. **Rejected
as the whole answer**: it binds only package *source*, and the driver for this design is
`ai-authored-capabilities-design.md`'s runtime-materialized DDL scripts, which no source linter can read.
It is, however, kept as Inc 4 — cheap, author-time, and it catches the mistake before an install is
attempted.

**A2 — forward-only, using the existing `byName` index and no declaration at all.** "If `Lookup(L)` finds
a sensitive DDL, `class` must be `L`." Zero new state, zero contract, zero migration — and it covers 9 of
12. **Rejected as the whole answer, adopted as the core of Increment 1**: §3.1 shows the two uncovered
DDLs are `appointmentEncounter` and `applicantProfile`, i.e. the clinical record and the underwriting
record — the highest-harm half of the corpus. A guarantee that covers everything except the PHI is not the
guarantee.

**A3 — ignore `class` for sensitivity entirely; resolve it from the key.** Cleaner in the abstract, and
the integrity check makes the two equivalent for the sensitive population. **Rejected**: `class` also
drives `permittedCommands`, the abstract-class gate, and the `instanceOf` chain resolution
(`step6_resolve_ddl.go:277-315`), so removing it as the resolution input is a far wider change than the
defect warrants — and for aspects the chain terminal must be a **vertexType** DDL, so the two inputs
already agree everywhere it matters.

**A4 — a reverse index keyed on localName alone (no anchor types).** Simplest index. **Rejected by the
corpus, not by taste**: §3.5 shows `.profile` is written by five distinct DDLs, one sensitive, four not.
A localName-only index refuses four shipped, correct writes. This is the alternative whose own objection I
then ran back against the recommendation: does *my* shape break a shipped consumer by another route? §3.6
is that check, and §7 rows 7/10 are its answer — every allow-arm is either "no plaintext" or "today's
behaviour".

**A5 — implement Contract #1 §1.5's "Default class" clause.** It would close arm A for free. **Rejected**:
defaulting an omitted class to the localName newly binds DDLs — and their `permittedCommands` — to writes
that pass today, retroactively, across all 77 aspect DDLs and every undeclared aspect in the platform. It
is a corpus-wide behaviour change with no demand, in service of a clause whose premise (localName ≈ class)
the corpus explicitly rejects. Correcting the clause is the honest move.

**A6 — the demand-side fix: rewrite the 12 writers.** The mandatory alternative whenever the consumer
census is single-digit. **Rejected, and this is the one case where it genuinely loses**: all 12 writers are
*already correct*. There is nothing to rewrite. The demand is not "fix these twelve" but "bind the
thirteenth", and the thirteenth may not be written by a human.

**A7 — a Processor commit-guard only, no authoring-time declaration.** Would need the binding derived
entirely from the corpus, which §3.5 proves impossible for retention-class custody. Not viable.

---

## 12. Collision with in-flight work — discharged: `retention-class-key-custody` §30 landed

`retention-class-key-custody-design.md` §30 **landed** (`f793bc55`, ~15 minutes after this design's first
commit): `ManifestBlock.RetentionClasses` is live at `internal/pkgmgr/manifest.go:33` with the count check
at `:185` and the identity loop at `:258`, and both `packages/clinic-domain/manifest.yaml` and
`packages/lease-signing/manifest.yaml` carry `retentionClasses:` blocks — the same struct and the same two
manifest files this design's Increment 2 touches.

**Consequence: no sequencing stamp is needed.** This design's Increment 2 builds on §30's landed shape;
Increment 1 has **no** pkgmgr surface and was never coupled to it.

No other in-flight design touches this seam: `package-authority-minting-provenance-design.md` reads step 6
only to observe that `class:"permission"` resolves no DDL (`:93-95`), which this design does not change
(a `vtx.permission.*` key is a **vertex**, row 11).

---

## 13. Decomposition for the Steward

**Increment 1 — the derived reverse guard. Posture-changing (a new fail-closed refusal point).**
`DDLCache.bySensitiveAspectAddress` built from `byName` for identity-custody sensitive DDLs;
`validateAspectClassBinding` in step 6; `aspectBindingRefusal` in step 6.5, placed before the
`class == ""` skip. **No pkgmgr surface, no contract dependency, no package edit.** Closes §1 arms A/B/C
for 9 of the 12 live sensitive classes on every existing deployment. Phase-0 **must** re-run §3.6 first
and treat any hit as a live victim to fix in-fire.
Green: `go test ./internal/processor/...`, plus the full gate set.

**Increment 2 — the declaration. Posture-changing (a new fail-closed install refusal).** `DDLSpec.KeyBinding`
+ `resolveKeyBinding`; the `.keyBinding` emission in `build.go`; the `loadMetaVertex` reader with the
`.sensitive` tombstone posture; `validateAspectKeyBinding` appended to `Definition.validateAll()`
(`definition.go:30-55`, beside `validateSensitiveClassScope` / `validateCustodyScope`); the
`ManifestBlock` DDL sub-field + comparison (the `Abstract`/`SubtypeOf` shape, `manifest.go:167-186`); the
12 declarations + 3 package version bumps; the forward conformance rule turned on for declared bindings.
**Sequences behind §30 Inc 2 (§12).** Ratified contract edits commit with this increment.
Green: `go test ./internal/pkgmgr/... ./internal/processor/...`, `verify-package-identity`,
`go run ./scripts/lint-package-version.go`, plus the full gate set.

**Increment 3 — observability. Mechanical.** `UnboundSensitiveClasses` + the `SensitiveClassUnbound`
health issue (§8.1); `lattice identity audit-sensitive-aspects` (§8.2).
Green: `go test ./internal/processor/... ./cmd/lattice/...`.

**Increment 4 — the lint gate. Mechanical.** A `packages/**` rule that a `Sensitive: true` `DDLSpec`
declares `KeyBinding.LocalName`, and that retention-class custody declares `AnchorTypes` — the
default-deny-the-bare-idiom shape the `# read-posture:` convention established
(`scripts/lint-conventions.go:132/:317/:493`). Ships **blocking**, not warn-first: Increment 2's migration
leaves zero debt, and a warn-first gate over a clean tree is the fingers-crossed state the increment
exists to end.
Green: `go run ./scripts/lint-package-standard.go` (or `lint-conventions.go`, wherever the DDLSpec walk
already lives).

Review depth is the Steward's sizing (`agents/steward/SKILL.md` §4); Increments 1 and 2 are the
posture-changing ones.

---

## 14. Risks

- **A false refusal takes out a live write path.** Mitigated structurally: derived bindings are
  reverse-only (§5.2, row 7), the anchor-type qualification is what keeps `.profile`'s four non-sensitive
  writers legal (§3.5), and §3.6 is a mandatory Phase-0 census whose non-empty result changes the fire.
- **Deploy ordering.** Increment 1 needs no package change, so a new binary against old packages is safe.
  Increment 2's declarations are additive (a new aspect key), and a Processor that predates the reader
  simply ignores it — neither direction breaks.
- **The uncovered three.** Between Increment 1 and Increment 2's rollout, `appointmentEncounter`,
  `applicantProfile` and `underwritingParties` remain unguarded. §8.1 makes that state **named and
  visible** for its whole duration rather than assumed away.
- **Ambiguity at load.** §6.4's poison entry converts an impossible-by-install state into a loud refusal
  rather than a silent index drop.
