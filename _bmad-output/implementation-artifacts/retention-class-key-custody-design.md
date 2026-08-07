# Retention-class key custody — a sensitive aspect's DEK belongs to a key holder, not to a person

**Status: 📐 DRAFT awaiting Andrew — authored 2026-08-06 (redirect of subject-anchored-sensitive-aspects-design.md, held by Andrew same session)**

Backlog row: `planning-artifacts/backlog/lattice.md` → *Privacy / Vault* → **"[Vault] Sensitive aspects are
identity-anchored, so retained records have no home"** (★★★, L). Grounds in the ratified
[vault-crypto-shredding design](vault-crypto-shredding-design.md), Contract #1 §1.6 + Contract #3 §3.10,
`lattice-architecture.md` Items 5 & 6, and the [held design](subject-anchored-sensitive-aspects-design.md)
whose surviving findings (whole-`data` encryption forces an aspect split; the shred must be *delivered*;
platform → delivery → consumer ordering) are carried forward here.

---

## For Andrew (one-look ratification block)

**What it does.** Today a `sensitive: true` aspect may attach **only** to a `vtx.identity`
(`step6_validate.go:186`), so every sensitive-but-not-person-scoped record sits in Core KV as plaintext.
This design separates the aspect's **anchor** from its **key holder**, and makes the holder a function of
the aspect's own resolved DDL class. `identity` is one holder kind (policy **erase-on-request** — today's
model, byte-identical). A **retention class** — a package-declared `vtx.retentionclass.<NanoID>` owned by
the data controller — is the other (policy **erase-on-expiry**). Shred granularity follows the erasure
boundary because destroying the key *is* the erasure. `ShredIdentityKey` on a patient therefore leaves the
clinical note **readable**, while every direct identifier of that patient becomes unrecoverable: the record
is **pseudonymized**, which is the acceptance criterion (§6.4).

**What changed vs the held design.** Custody is no longer the data subject's erasable key, so the held
design's headline property (*"an aspect governed by a shredded subject is unrecoverable"*) is gone — it was
the defect for this data class. Consequently: **no `subjectKey` envelope field** (nothing is caller-supplied,
so no attacker-controlled field is introduced and no name collides with lease-signing's shipped
`subjectKey` payload field); **no custody-immutability rule** (the ciphertext already names its own key, so
re-classifying a record cannot orphan it); **no second per-identity DEK**; **no ref-MAC v2** (the MAC
already covers `ct.KeyID`, `refmac.go:24`). The read side gets *smaller*, not bigger.

**The read side is the substance (§6), and it is a subtraction.** Every encrypted aspect in Core KV already
records which key encrypted it — `Ciphertext{CT, Nonce, KeyID}`, `local.go:242`, `KeyID` set from the
envelope minted for that holder at `local.go:223`. **Five** read paths ignore it and re-derive custody from
outside the ciphertext (`sensitive_decrypt.go:162`, `secure.go:130`, `service.go:478`, `egress.go:174`,
`cmd/loupe/vault.go:147`). All five switch to trusting `ct.KeyID` — which is safe *because the same string
is the AEAD associated data* (`local.go:238`/`:252`), so a substituted `keyId` fails the GCM tag rather
than opening the record under another key. That removes `SecureColumn.IdentityKeyColumn` as a requirement
(`corekv_source.go:877-879`) and removes the whole class of "the declared column's RETURN alias was never
projected" runtime failures.

**Three forks need your call (§10).** (F1) Does the identity holder kind keep its in-band piiKey-CDC scrub,
or move to the same rebuild-driven delivery a class holder needs? I recommend **keep** — and §6.3 *derives*
why the in-band path is sound for identity and cannot be for a class holder, rather than asserting it.
(F2) A per-row secure-column custody failure today kills the whole batch (`evaluate.go:81-83`); I want it to
project **null + alarm** instead of `Terminal`, which changes shipped behavior on the security plane for six
live secure lenses. (F3) `lattice-architecture.md:1019` ("crypto-shredding destroys the identity's key → all
sensitive aspects for that identity become irrecoverable") becomes false for a retained class, and that file
is planning-lead-owned — I have not touched it.

**Two corrections to the demand as filed.** (i) The board row says three retained records "sit plaintext in
Core KV." True for **two** — clinic `.encounter` (`clinic-domain/ddls.go:896`) and lease-signing's income
`.profile` (`scripts.go:1013-1056`). The background-check `outcome` aspect stores **no sensitive payload at
all** today (`{status, completedAt, validUntil}`, `lease-signing/ddls.go:473-480`) and is *heavily projected*
in six lens sites — it is a prospective retention class, not a live leak (§9.3). (ii) Clinic has a
**separate, pre-existing erasure hole this design deliberately does not close**:
`vtx.patient.<id>.demographics` = `{fullName}`, non-sensitive by declaration
(`clinic-domain/ddls.go:595-601`), so a patient's name survives `ShredIdentityKey` in plaintext. The right
fix is to move the name onto the identity where it belongs, not to invent a third custody kind (§9.5) —
which is also *why* your redirect is coherent: the held design was solving the erasable-off-identity case,
which has a simpler answer, while the retained case genuinely cannot move.

**Recommendation.** Ratify Increments 1–2 as the platform (1 is provable and green with zero consumers;
2 gates every vertical), then 3 and 4 as Verticals work. Period bucketing stays deferred and §7.3 verifies
your claim that deferring it costs no migration — with three named conditions that must hold for that to
stay true.

---

## 1. Grounding ledger (every load-bearing fact, verified in code)

| # | Claim | Evidence | Verdict |
|---|---|---|---|
| 1 | The Vault takes an **arbitrary string**, not an identity — `CreateIdentityKey(ctx, identityKey string)` uses it as AEAD associated data and as `Envelope.KeyID` | `internal/vault/local.go:203`, `:217`, `:221-228` | ✅ no interface change needed for a non-identity holder |
| 2 | Encrypt/Decrypt bind the same string as AAD | `local.go:238`, `:252` | ✅ |
| 3 | **The stored ciphertext is self-describing** — it records which key encrypted it | `local.go:242` (`Ciphertext{CT, Nonce, KeyID: envelope.KeyID}`), `:223` | ✅ |
| 4 | The identity coupling is NOT in the Vault — it is three Processor checks plus the op vocabulary | `step6_validate.go:186`, `step65_encrypt.go:68-73`, `sensitive_decrypt.go:162-163` | ✅ |
| 5 | Read path 1 (Processor) re-derives custody from the anchor's host segment | `sensitive_decrypt.go:162` → `:217` `readPiiKeyEnvelope(…, vertexKey)` | ✅ ignores `ct.KeyID` |
| 6 | Read path 2 (Refractor) takes custody from a **required** projected column | `secure.go:130`, `:137`; requirement at `lens/corekv_source.go:877-879` | ✅ ignores `ct.KeyID` |
| 7 | Read path 3 (egress responder) derives custody from `ParseAspectKey(in.Ref)` and refuses non-identity | `internal/vault/service.go:478-482`, decrypt at `:499` | ✅ |
| 8 | Read path 4 (bridge) same, then fetches the envelope off the **identity-only** `piiKeyEnvelope` lens | `bridge/egress.go:174-178`, `:183`, `:239`; lens spec `privacy-base/lenses.go:113` (`MATCH (i:identity)`) | ✅ |
| 9 | Read path 5 (Loupe Reveal) splits the aspect key, refuses non-identity, reads `identityKey+".piiKey"` — **while already requiring `ct.KeyID` to be non-empty** | `cmd/loupe/vault.go:147-153`, `:176`; `:112` | ✅ the field is validated and then unused |
| 10 | The ref MAC **already covers `ct.KeyID`** | `internal/vault/refmac.go:20-29`, keyId appended at `:24`; purpose `sensitive-ref/v1` at `:12` | ✅ no MAC version bump needed |
| 11 | Shred → scrub-to-null already exists and is correct | `secure.go:155-159` (`ErrKeyShredded` → `row[col] = nil`, keep the row) | ✅ |
| 12 | A **soft-deleted** key aspect is treated as absent → `Terminal` | `secure.go:199-211` | ✅ fail-closed today |
| 13 | `ShredKey`'s in-memory deny-list is **per-process**; the Refractor holds its **own** LocalBackend from the same KEK | `local.go:51-53`, `:265-272`; `cmd/refractor/main.go:1286-1301`; `internal/privacyworker/manager.go:11-22` | ✅ so the Refractor's scrub depends entirely on the **durable** `envelope.Shredded` flag (`local.go:381`) |
| 14 | Step 6.5 encrypts the **whole `data` map** | `step65_encrypt.go:83-101` | ✅ held-design §4.5 stands (§9.1) |
| 15 | Three shipped clinic lenses project operational fields out of `.encounter.data` | `clinic-domain/lenses.go:463-465`, `:646-648`, `:681-683` | ✅ |
| 16 | `ReferencedLabels()` collects labels from **node patterns only** — a property chain contributes none | `ruleengine/full/labels.go:110` (sole writer of the map, inside `for _, n := range p.Nodes`), `:122-160` (`PropertyAccess` recurses only into `Target`; no `VariableRef` case) | ✅ |
| 17 | `NarrowedFilterEligible` has **no secure-decryptor conjunct** | `pipeline/pipeline.go:903-913`, the three conjuncts at `:908-910` | ✅ |
| 18 | The plain-aspect arm Ack-drops an event whose parent type is not in the label set | `pipeline.go:1769-1772`; predicate `plainReactsTo` at `:683-691` | ✅ |
| 19 | `ConsumerFilter` narrows to `$KV.<bucket>.vtx.<label>.>` per label — which covers that label's aspects too | `pipeline.go:920-951`; `internal/refractor/subjects/subjects.go:124-128`, `:170-178` | ✅ |
| 20 | `secureIdentityKeyType` is consulted **only** on a path a Secure Lens cannot take | const `pipeline.go:763`; sole consult `:822-826` inside `actorAwareNarrowingLabels`, which returns early at `:789-791` when `actorEnumerator == nil`; secure+projectionKind refused at `corekv_source.go:858-860` | ✅ |
| 21 | A non-seeded plain lens reprojection recomputes **all** rows | `pipeline.go:666-679` (`seedAnchorFor`), call site + comment `evaluate.go:166-175` | ✅ |
| 22 | A per-row secure-column failure **discards the whole result set** | `secure.go:98-110` (returns on first error) → `evaluate.go:81-83` (`return nil, nil, err`) | ✅ combined with #21 this is a whole-lens stall |
| 23 | No SweepPlan for a Secure Lens — but **the stated reason in the code is stale** | install site `projection/driver.go:435-440` inside `InstallActorAggregate` (`:336`), gated by `sweepEnrolment` (`:309-324`), NOT by `authPlane` (which only picks the interval, `:294-299`); stale doc at `pipeline/sweep.go:29-33` | ✅ conclusion holds, reason corrected |
| 24 | `keyshredded` **explicitly excuses** secure lenses, on a premise this design invalidates | `keyshredded/manager.go:31-37` | ✅ |
| 25 | `NullifyRow` keys on the row's own identity column, so it cannot reach a class-custodied row | `keyshredded/manager.go:357-358`; `NullifyTarget` at `:107-111`; impl `control/service.go:612-620` | ✅ |
| 26 | `NullifyRow` is documented as **best-effort/transient** — a deleted row can reappear from a later CDC delivery | `keyshredded/manager.go:333-350` | ✅ argues for rebuild over delete (§6.3) |
| 27 | A rebuild **re-runs the SecureDecryptor** for every row, through the single choke point | `evaluate.go:81-83` (+ explicit sites `pipeline.go:1745`, `:1929`); `Rebuild` at `pipeline.go:1329-1447` | ✅ the erasure mechanism |
| 28 | A rebuild does **not** delete rows unless `--truncate`; Postgres targets are unguarded | `pipeline.go:1394-1406`, guard check `:1385-1393`; `adapter/postgres.go:355-357`, `:360-366`; CLI flag `cmd/lattice/lens/control.go:157` | ✅ upsert-only is what we want |
| 29 | Rebuild is **not programmatically reachable** — `rebuildRule` is unexported | `control/service.go:865-878` unexported, vs exported `PauseRule` `:597` / `NullifyRow` `:612` / `NullifyActor` `:627`; CLI publisher `cmd/lattice/lens/control.go:129-160`; verb `internal/controlauth/ops.go:58` | ✅ real Increment-2 work |
| 30 | `validateSecureColumns` requires `identityKeyColumn`, and validates it against **declared table columns** only — never against a cypher alias or a bound node | `corekv_source.go:848-903`; required `:877-879`; table-column check `:895-900`; non-protected refused `:852-854`; `projectionKind` refused `:858-860` | ✅ |
| 31 | No lens spec has any declared type-dependency / `reactsTo` field | `LensSpec` `corekv_source.go:134-161`; `OutputDescriptorSpec` `:170-210` (its `AnchorType` is actor-aggregate only) | ✅ so a declared holder type is a new, first-of-its-kind mechanism |
| 32 | A **vertex type segment is `[a-z][a-z0-9]*`** — no camelCase; the id segment must be a valid 20-char NanoID | `internal/substrate/keys/keys.go:141-155`, `:71-91` (`:75`) | ✅ the holder type must be `retentionclass`, matching `leaseapp`/`clinicaccount`/`augurproposal` |
| 33 | Package installs already mint **deterministic, version-independent** NanoIDs from a salt, with exported helpers | `pkgmgr/installer.go:329-331` (`entityNanoID`), `:339-341` (`RoleID`), `:349-351` (`LensID`), `:366-376` (`nanoIDFromSalt`) | ✅ the holder key needs no new ID mechanism |
| 34 | The DDL's `Sensitive` flag reaches the Processor as a **reserved aspect on the DDL meta-vertex**, emitted only when true | `pkgmgr/definition.go:752-759`; emit `pkgmgr/build.go:135-143`; read `processor/ddl_cache.go:258-275` into `MetaVertexRef` `:24-58` | ✅ the same seam carries a `custody` declaration |
| 35 | `Sensitive: true` is install-time refused on any class but `meta.ddl.aspectType` | `pkgmgr/sensitivescope.go:22-38` | ✅ |
| 36 | **All nine** shipped sensitive aspects anchor on `vtx.identity` | `identity-domain/ddls.go:238`, `:261`, `:284`, `:308`, `:332`, `:356`, `:380`, `:404`, `:432` | ✅ zero exceptions |
| 37 | **No** tenant / org / clinic / controller vertex type exists anywhere | census of 55 vertexType DDLs across `packages/`; `clinicSite` owns no vertex type (`clinic-domain/site.go:12-15`, writes onto `vtx.building`) | ✅ the holder must be package-minted now |
| 38 | Six shipped secure lenses, all `IdentityKeyColumn: "identity_key"` | `clinic-domain/lenses.go:279-282`, `identity-domain/lenses.go:66-68`, `loftspace-domain/lenses.go:81`, `lease-signing/lenses.go:274`, `wellness-domain/lenses.go:137`, `cafe-domain/lenses.go:132` | ✅ migration surface is 6 sites |
| 39 | The architecture-of-record **already anticipated** non-identity anchoring — and its shred claim is what changes | `lattice-architecture.md:1017` ("or linked to one via a defined anchoring pattern"), `:1019`, `:1022` (aspect-level rationale) | ✅ F3 |
| 40 | The wholesale decrypt RPC carries **no actor and no purpose**; authorization is transport-only (natsperm) | `vault/service.go:74-78` (`DecryptRequest`); grants `natsperm/matrix.go:297` (loupe), `:219` (bridge, decryptref only) | ✅ the REVEAL axis (§6.5) |

**Not verified / explicitly out of scope.** I did not run any build or test (design-only fire). I did not
verify whether any *out-of-repo* consumer reads `SecureColumn.IdentityKeyColumn`. I did not verify
`clinic-domain`'s `.demographics` beyond its declared `{fullName}` schema — whether real seed data carries
more is a Verticals question. I found **no** existing non-CLI publisher of
`lattice.ctrl.refractor.<lensId>.rebuild` (searched `internal/`, `cmd/`) and treat that as absence of one.

---

## 2. Problem & intent

**The gap.** `step6_validate.go:176-195` rejects a sensitive aspect whose parent vertex type is not
`identity`. The rule is correct for what it was built for — all nine shipped sensitive aspects are
person-scoped and single-valued on the person (ledger #36). It is wrong for a record that describes an
**event** rather than a person, and it has no expressible alternative, so three packages wrote the same
comment instead of the aspect: *"plaintext-for-now … the deferred Vault plane owns encryption"*
(`lease-signing/ddls.go:149`, `:110`, `:157`; `clinic-domain/ddls.go:885-895`).

**The deeper gap, which the held design got backwards.** Making that data sensitive is not enough; the
*key lifetime* has to be right. A clinical encounter and a background-check result carry a retention
obligation that **outlives a data subject's erasure request**. Keying them to the subject's erasable DEK
means honoring erasure destroys what the controller is legally required to keep. Retention is not an
add-on to a person-keyed model — it is the fact that decides custody.

**Intent.** Make key custody a declared property of the data class, so that:

1. destroying a key IS an erasure, at exactly the granularity the erasure boundary has;
2. the *person's* erasure and the *record's* expiry are two different keys with two different clocks;
3. nothing about custody is supplied by a caller or discovered by a graph walk, so no new
   attacker-controlled field and no write-path enumeration appears;
4. the read paths get simpler, because the ciphertext already knows its own key.

---

## 3. The holder model

### 3.1 Key holders and policies

A **key holder** is a vertex whose `.piiKey` aspect carries the wrapped DEK. Two kinds:

| Kind | Holder vertex | Policy | Erased by | Clock |
|---|---|---|---|---|
| `identity` | the aspect's own parent, `vtx.identity.<NanoID>` | **erase-on-request** | `ShredIdentityKey` | the data subject's request |
| `retentionClass` | a package-declared `vtx.retentionclass.<NanoID>` | **erase-on-expiry** | `ShredRetentionClassKey` | the controller's retention schedule |

`identity` is **exactly today's model** — same aspect key, same lazy mint, same op, same guarantee. The
kind is not a new mode for it; it is the name for what it already does.

**Why the holder is a vertex and the key aspect keeps the localName `piiKey`.** The custody *address* is
`<holderKey> + ".piiKey"` — a pure string concatenation the read paths already perform
(`sensitive_decrypt.go:242`, `secure.go:195`, `cmd/loupe/vault.go:176`). Because the ciphertext carries
`keyId` (ledger #3), the ciphertext **is** a complete, self-describing custody address. A per-kind aspect
name (`retentionKey`, …) would reintroduce holder-kind dispatch on every read path — the exact coupling
this design removes. The residual is honest: `piiKey`'s DDL description
(`privacy-base/ddls.go:35-43`) must be re-derived to say "a key holder's DEK envelope", not "per-identity".

**Why `retentionclass`, all lowercase.** A vertex type segment is `[a-z][a-z0-9]*` (ledger #32) — camelCase
is not addressable. This matches the shipped corpus (`leaseapp`, `clinicaccount`, `augurproposal`,
`credentialindex`). Prose says "retention class"; the key says `vtx.retentionclass.<NanoID>`.

**Why not a deployment-wide key.** Zero expiry granularity — one key means the only erasure is "erase
everything." That is disk encryption, which is the substrate's job, not a privacy plane.

**Why not per-controller-only.** Necessary but insufficient: it cannot expire one aged-out record. Which is
also, honestly, the limit of **Increment 1**: an un-periodized class key is exactly a per-controller key
scoped to one data class. Increment 1 delivers *custody* (the record survives the subject's erasure);
per-record expiry arrives with period bucketing (§7.3). The doc does not claim otherwise.

### 3.2 Declaration — custody is a function of the resolved DDL, nothing else

Two additions to `pkgmgr` (ledger #34 is the seam; both materialize as reserved aspects on the DDL
meta-vertex and load into `MetaVertexRef`):

```go
// pkgmgr.DDLSpec — aspectType only, refused elsewhere by validateSensitiveClassScope's sibling check
Sensitive bool         // unchanged
Custody   CustodySpec  // NEW, optional. Zero value == Kind "identity" == today's behavior exactly.

type CustodySpec struct {
    Kind           string // "identity" (default) | "retentionClass"
    RetentionClass string // REQUIRED iff Kind == "retentionClass": canonicalName of a class the
                          // SAME package declares. Empty otherwise.
}

// pkgmgr.Definition gains, alongside DDLs/Lenses/Permissions/Roles:
type RetentionClassSpec struct {
    CanonicalName   string // e.g. "clinicalRecord"
    Description     string // what this class is and why it is retained
    Policy          string // "eraseOnExpiry" — the only kind in Increment 1
    RetentionPeriod string // ISO-8601 duration. DECLARATIVE in Increment 1: no automatic expiry
                           // exists yet, so this is the controller's stated schedule, not a timer.
}
```

The install batch mints, for each declared class:
`vtx.retentionclass.<entityNanoID(pkg, "retention:"+canonicalName)>` (root, class `retentionclass`) plus
a `.retentionPolicy` aspect carrying `{canonicalName, policy, retentionPeriod, description}` — the same
deterministic-ID mechanism roles and lenses already use (ledger #33), with an exported
`pkgmgr.RetentionClassID(pkg, canonicalName)` mirroring `RoleID`/`LensID`. The holder key is therefore
**known at build time**, embedded into the aspectType DDL's `.custody` aspect, and reaches step 6.5 as a
field on the already-resolved `MetaVertexRef` — **zero new read on the write path**.

**Install-time validation (fail-closed, all four).**
1. `Custody.Kind` must be `""`, `"identity"`, or `"retentionClass"` — anything else rejects the install.
2. `Kind == "retentionClass"` requires a non-empty `RetentionClass` naming a class **this package
   declares** (cross-package holder references are refused; a class is a controller-owned declaration, not
   a shared handle).
3. `Custody` on a non-`aspectType` DDL rejects, mirroring `sensitivescope.go:22-38`.
4. `Custody.Kind == "retentionClass"` on a DDL with `Sensitive: false` rejects — declaring custody for
   data that is never encrypted is a declaration with no meaning, and silently ignoring it is how a package
   comes to believe it has a retention posture it does not have.

### 3.3 Why not caller-supplied, and why not traversed

- **Caller-supplied** (the held design's `subjectKey`) adds an attacker-controlled field to the plane whose
  existing sites deliberately eliminate one (`service.go:455-457`'s own comment: *"the caller no longer
  supplies one — one less attacker-controlled field"*), and collides with lease-signing's shipped
  `subjectKey` payload field (`lease-signing/ddls.go:286`, a *required* payload key).
- **Traversed** (a DDL-declared relation the Processor walks at commit) needs a prefix enumeration on the
  **write path**, which the no-scans invariant forbids and which `derive_reads` (class (g), pure, KV-free)
  cannot supply either.
- **DDL-resolved** costs nothing: step 6.5 already holds the resolved ref (`step65_encrypt.go:64`).

---

## 4. Write path (P2 — the Processor is the sole Core-KV writer)

```
step 4 (hydrate)  → decrypt-on-read resolves custody from ct.KeyID (§6.1). No holder-kind branch.
step 5 (execute)  → unchanged. Starlark sees plaintext, returns plaintext. No new script surface.
step 6 (validate) → sensitiveAspectScope becomes CONDITIONAL ON THE DECLARED CUSTODY KIND (§4.1).
step 6.5 (encrypt)→ holder = the resolved DDL's custody (§4.2). Lazy key mint into the same batch,
                    the ensureIdentityKey shape unchanged.
step 8 (commit)   → unchanged. Nothing new rides the document; the ciphertext already carries keyId.
```

### 4.1 Step 6's rule

For a mutation whose resolved DDL is `Sensitive` and whose kind is an aspect, keyed on the DDL's declared
`Custody.Kind`:

1. **`identity` (including absent/zero)** → parent vertex type must be `identity`, else
   `DDLViolation{sensitiveAspectScope}` with today's message. **Byte-identical to current behavior.**
2. **`retentionClass`** → the parent may be any vertex type. The declared holder key must parse as a
   3-segment vertex key of type `retentionclass` (a structural check on a value the *install* produced, not
   on caller input). Nothing else is checked, because nothing else can vary per-mutation.
3. Anything else → `DDLViolation{sensitiveAspectScope}`.

**This is the fail-closed default in one sentence:** a package that flips `Sensitive: true` on an
appointment-anchored aspect and forgets to declare custody gets today's rejection, not plaintext at rest.

**Two pre-existing fail-open paths this design must close, because it is what puts PHI behind them** (both
carried from the held design's adversarial pass and re-verified here):

- **A class-less mutation bypasses both gates.** Step 6's DDL block is `if class != ""`
  (`step6_validate.go:156`) and step 6.5 `continue`s on an empty class (`step65_encrypt.go:59-62`). A
  script emitting an aspect `update` with `data` but no `class` commits **plaintext** silently. Increment 1
  rejects a class-less create/update aspect mutation after a census confirming no shipped script relies on
  it.
- **Steps 6 and 6.5 can disagree about sensitivity** via the shared `LiveReads` budget: step 6 runs first,
  can spend the last of the budget, resolve `Sensitive` and pass; step 6.5's identical call then hits the
  budget error and **fails open** to the permissive default, skipping encryption. Increment 1 makes a
  budget-exhausted resolution a **hard error** in step 6.5. (Low likelihood at the shipped budget; wrong
  direction, so not left as a note.)

What the held design needed and this one does **not**: no subject parse, no hydrated-set membership check,
no shredded-subject check at step 6, no custody-immutability rule, no same-batch dedup rule, no
`VertexDoc` field, no Starlark projection. All of those existed to police a caller-supplied pointer.

### 4.2 Step 6.5's holder resolution

`step65_encrypt.go:68-73` currently does `ParseAspectKey(m.Key)` and `continue`s when the parent is not an
identity. It becomes:

```
holder := ref.Custody.HolderKey     // "" for kind identity
if holder == "" { holder, _, _, _, ok = ParseAspectKey(m.Key) }   // today's derivation, unchanged
```

Then `ensureKeyHolderKey(ctx, holder, &extra)` — `ensureIdentityKey` renamed and otherwise unchanged:
read `holder + ".piiKey"`, and on absence mint via `Vault.CreateIdentityKey(ctx, holder)` and append the
`create` to the **same atomic batch** (`step65_encrypt.go:112-144`). The envelope cache is already keyed by
the holder string (`:41`, `:74-82`), so a batch touching many appointments under one class mints once.

**The two-writers hazard is already solved.** Two concurrent batches writing the first sensitive aspect
under one class both read "absent" and both emit `create` on the same deterministic key. That is exactly
the piiKey case: `encryptSensitiveMutations` returns `mintedPiiKey=true` (`:104`) and the commit pipeline's
OCC retry treats it as an independent retry-eligible signal (`:30-38`). The mechanism is reused verbatim —
this is not a new race, it is the same race with a different holder string.

**A property to record, not a defect.** Step 6.5 appends a `create` on a key the operation neither declared
nor named, on a vertex root that is not the aspect's parent — a widening of the shipped behavior (which
already appends on the parent identity). No shipped permission check is keyed on the committed write
footprint; anything that later audits it must expect a `vtx.retentionclass.*` key in a batch whose
operation names only appointments.

### 4.3 Destruction — `ShredRetentionClassKey`

Mirrors `ShredIdentityKey` exactly (`privacy-base/shred_identity_key.go:267-311`), in `privacy-base`:

- Payload `{retentionClassKey}`; refuses unless the holder vertex is alive.
- Reads `holder + ".piiKey"` — `# read-posture: (d)`, declared in `contextHint.optionalReads` by every
  dispatcher, same annotation the shred script already carries (`shred_identity_key.go:286-288`).
- Writes `shredded: true` + `shreddedAt` on the existing envelope, or a durable
  **empty-`wrappedDEK` placeholder** when none exists — for the identical reason: the LocalBackend's
  deny-list is in-memory (`local.go:51-53`), so a later write must not mint a fresh, unshredded key.
- Emits `privacy.retentionClassKeyShredded`.
- Is admitted by `piiKey`'s `permittedCommands` (`privacy-base/ddls.go:34`), which must gain it — the gate
  that keeps key aspects writable only by privacy-base ops.

**The durable flag is load-bearing, not belt-and-braces.** The Refractor holds its **own** LocalBackend
(ledger #13), so it never observes the Processor's in-memory shred. Its scrub works only because
`readPiiKeyEnvelope` reads the live aspect and `checkAndDeriveDEK` honors `envelope.Shredded`
(`local.go:381`). A destruction that only called `Vault.ShredKey` would be invisible to every projection.

An async worker (co-located with the Processor for the reason `privacyworker/manager.go:11-22` gives —
the deny-list and DEK cache are per-instance) consumes the event, calls `Vault.ShredKey(holderKey)`, and
records finalization. §6.3 adds the projection half.

### 4.4 Observability

`privacy-base` gains a `retentionKeyStatus` lens — one flat row per retention-class holder
(`canonicalName`, `policy`, `retentionPeriod`, `shredded`, `shreddedAt`, `vaultKeyDestroyed`,
`projectionsRebuilt`), the operator analog of `shredStatus` (`privacy-base/lenses.go:79-89`). I chose a new
lens over widening `piiKeyEnvelope`, whose `MATCH (i:identity)` (`:113`) has two live consumers — the
bridge (`egress.go:239`) and loftspace-app's blob path — that must not change shape here.

---

## 5. Contract surface

**Aim met, with one honest exception.** Contract #1's **envelope (§1.3) is unchanged** — no new field.
But Contract #1 **§1.6's** one-sentence consequence *"Sensitive-aspect anchoring (must attach to
identity-anchored vertex)"* (`docs/contracts/01-addressing-and-envelope.md:180`) becomes false and must be
replaced. Claiming "Contract #1 unchanged" would be an overclaim; the envelope is unchanged.

| Contract | § | Change |
|---|---|---|
| **#1 Addressing & envelope** | §1.3 aspect envelope | **build-to — UNCHANGED** (the design's point) |
| **#1 Addressing & envelope** | **§1.6 sensitive-aspect consequence, line 180** | **CHANGE — one sentence** |
| **#3 MutationBatch** | **§3.10** | **CHANGE — custody + retention/erasure semantics (below)** |
| #3 MutationBatch | §3.11 sensitive blobs | **one cross-reference touch** — `:273`/`:277` say "the same per-identity key" / "the governing identity's §3.10 DEK". Blobs stay identity-custodied; the words become "the §3.10 key holder (an identity, for a blob)". |
| #2 Operation envelope | §2.5 `reads`/`optionalReads` | build-to — the holder's key aspect is an ordinary `optionalReads`, class (d) |
| #5 Health KV | §5.4 `vault_calls_total` | build-to |
| #7 Primordial bootstrap | reserved `sensitive` aspect type | build-to — `custody` joins it as a reserved DDL aspect |

### 5.1 Proposed replacement text (INSIDE this doc only — `docs/contracts/*` untouched)

**Contract #1 §1.6, replacing line 180's sentence:**

> **Consequences for sensitive aspects (PRD Item 6):** a sensitive aspect's **key custody** is declared by
> its aspect-type DDL (`custody.kind`), and the anchoring rule follows the declared kind: custody kind
> `identity` (the default when undeclared) requires the aspect to attach to an `identity` vertex; custody
> kind `retentionClass` permits any anchor and custodies the DEK on the declared retention-class holder.
> Undeclared aspects have no enforced sensitivity. Operators handling sensitive data must register a DDL
> with the sensitive flag, and — for data whose retention obligation outlives a data subject's erasure
> request — with a retention-class custody declaration.

**Contract #3 §3.10 — precisely what is replaced.** These spans go (`docs/contracts/03-mutation-batch-event-list.md`):

- `:204-206` — *"destroying the per-identity key renders the ciphertext … permanently unrecoverable."*
- `:215-219` — commit-path step 3's *"with the anchoring identity's data-encryption key (DEK)"* and
  *"If the anchoring identity has no `vtx.identity.<id>.piiKey`"*.
- `:223-224` — the *"**Key custody.** The per-identity DEK …"* opening.
- `:233` — *"`ShredIdentityKey` destroys the DEK, after which no consumer can decrypt."*
- `:252-253` — the Live-envelope rule's *"the identity's key envelope"*.
- `:261-262` — the Ref-provenance rule's *"derives the identity from the authenticated `ref`"*.

Everything else in §3.10 — the commit-path placement, the external-egress guard and its
consumption-not-decryption rationale, the whole Live-envelope rationale, the ref-provenance MAC binding —
stands verbatim.

Replacement text:

> **Key custody.** A sensitive aspect's `data` is encrypted under a **key holder's** data-encryption key
> (DEK). The holder is a function of the aspect's **resolved aspect-type DDL** — never supplied by the
> caller and never discovered by graph traversal. Two holder kinds exist:
>
> - **`identity`** (the default when undeclared) — the holder is the aspect's own anchoring identity.
>   Policy: **erase-on-request**. Its DEK is destroyed by `ShredIdentityKey`.
> - **`retentionClass`** — the holder is a controller-declared retention-class vertex
>   (`vtx.retentionclass.<NanoID>`), named by the DDL's `custody.retentionClass`. Policy:
>   **erase-on-expiry**. Its DEK is destroyed by `ShredRetentionClassKey`, on the controller's retention
>   schedule, not on a data subject's request.
>
> Every holder references only its **wrapped** DEK, from `<holderKey>.piiKey`, satisfying "key material
> never in Core KV". If the resolved holder has no `piiKey`, the Processor lazily provisions one and adds it
> to the **same** atomic batch. Encryption is non-deterministic (random nonce) and compatible with
> last-writer-wins-by-revision and `requestId` idempotency.
>
> **The ciphertext names its own key.** The stored envelope is `{ ct, nonce, keyId }`, where `keyId` is the
> holder's vertex key and is bound as AEAD associated data. **Every decrypt resolves custody from `keyId`,
> never by re-deriving it from the aspect's anchor or from a projected column.** A substituted `keyId`
> therefore fails authenticated decryption rather than opening the record under another holder's key. A
> consequence: **re-classifying a data class cannot orphan already-committed records** — each keeps the key
> it was written under.
>
> **Retention and erasure.** Destroying a key IS the erasure, at exactly the granularity the key has. The
> two kinds are two clocks: `ShredIdentityKey` destroys a person's DEK and makes every aspect custodied by
> that person unrecoverable, while a record custodied by a retention class **survives** — the record
> becomes pseudonymized (retained, with its subject's direct identifiers unrecoverable) rather than erased.
> A retained record must not duplicate its subject's direct identifiers, or the subject's erasure is
> defeated by that duplication. Conversely `ShredRetentionClassKey` makes every record in that class
> unrecoverable regardless of any subject's erasure state.
>
> **Erasure must reach the read models.** A key destruction is not complete when the key is destroyed; it
> is complete when no projected read model still holds the plaintext. A Secure Lens projects `null` for a
> column whose holder key is destroyed, and that null is **stable under reprojection** (a later
> re-evaluation re-attempts the decrypt and fails the same way), so re-projecting the affected lenses is
> convergent. Where the holder is not a vertex the lens binds — which is always true for a retention class
> — the platform must **re-project** the lenses whose secure columns declare that holder type; a lazy
> next-event scrub is not an erasure guarantee, because nothing enumerates or attests it.
>
> **Live-envelope rule.** A Vault-decrypt consumer resolves the **key holder's** envelope from the current
> `piiKey` state (the aspect, or its lens projection) **at decrypt time — never from a stored or carried
> copy**. […rationale unchanged…]
>
> **Ref-provenance rule.** […unchanged through the MAC binding…] The ref-verified decrypt RPC recomputes
> the MAC before any decryption; because the MAC covers the ciphertext's `keyId`, custody is authenticated
> rather than re-derived. A sensitive-ref for a **non-`identity`** holder is **refused** at hydration until
> the external-egress key-envelope read path covers non-identity holders (its envelope lens is
> identity-only today); the refusal is typed and loud, never a silent pass-through of raw ciphertext.
>
> **Reveal.** A decrypt request carrying no actor and no declared purpose is **denied** for a
> non-`identity` holder. A retention-class record has no data subject whose grant scopes its disclosure, so
> the wholesale trusted-tool decrypt RPC — which carries neither actor nor purpose — is not an authorization
> path for it. The sanctioned read path is a read-path-authorized **Secure Lens**, where custody answers
> "can this be decrypted at all" and the Protected/RLS/grant plane answers "which actor sees this row."

---

## 6. Read path — the design's substance

Andrew's live question. This section is deliberately the longest, and the change is mostly **subtraction**.

### 6.1 Every decrypt trusts the ciphertext's `keyId`

Five sites (ledger #5–#9) resolve custody from outside the ciphertext while the ciphertext states it. All
five become one shape:

```
holder := ct.KeyID
if ClassifyKey(holder) != KindVertex { fail closed — never fall back to the anchor }
envelope := read(holder + ".piiKey")      // live, per the Live-envelope rule
plaintext := Vault.Decrypt(ctx, holder, envelope, ct)
```

**Why this is safe, precisely.** `keyId` is not merely metadata — it is the AEAD **associated data** the
ciphertext was sealed under (`local.go:238`, `:252`; envelope minted with the same string at `:217`,
`:223`). Substituting `keyId` to point at a holder the attacker controls means: read that holder's real
envelope, unwrap that holder's real DEK, then open a ciphertext sealed under a *different* DEK with a
*different* AAD — the GCM tag fails. `ct.KeyID` is self-authenticating for the decrypt path. This is the
whole reason the read side shrinks instead of growing.

**What it removes:**

- **`SecureColumn.IdentityKeyColumn` as a requirement** (`corekv_source.go:877-879`). With it goes the
  failure mode the held design found: `identityKeyColumn` is validated against declared **table columns**
  only (`:895-900`) and nothing checks the cypher actually RETURNs that alias, so a lens declaring the
  column but omitting the RETURN item activates cleanly and then fails per-row at runtime. That class of
  bug ceases to exist.
- **Any custody-immutability rule.** The held design needed one because an update that merely *omitted*
  `subjectKey` would strip custody and orphan committed ciphertext. Here custody is a property of the
  ciphertext, written at the same instant, by the same code, in the same batch. Re-classifying an aspect
  type (identity → retention class, or one class to another) leaves every already-committed record
  readable under its own `keyId`. **Nothing can orphan a record.**
- **The custody-derivation branch in `sensitive_decrypt.go:162-172`** — the branch that today marks a
  non-identity-anchored sensitive body plaintext-readable and returns **without decrypting**, handing the
  script a raw ciphertext map. Unreachable today because step 6 refuses the write; Increment 1 makes it
  reachable, so it must go, not be preserved.
- **`secureIdentityKeyType`** (`pipeline.go:763`), whose justification rests on step 6's rule admitting no
  other parent — false after Increment 1. It is consulted only on a path a Secure Lens structurally cannot
  take (ledger #20), so Increment 2 deletes it rather than "re-deriving" it. A guard that is still correct
  for a reason that has expired is a trap for the next reader.

**What must stay fail-closed (each one an explicit rule):**

1. `ct.KeyID` absent, or not a well-formed 3-segment vertex key → **refuse**. Never fall back to the
   anchor: a silent fallback is a path back to the derivation being removed, and it would make a malformed
   record readable under the wrong holder.
2. `ct.KeyID`'s **type segment not in the column's declared `holderTypes`** (§6.2) → refuse.
3. The holder's `piiKey` **absent** → the ciphertext can never be opened. Fail closed (§6.2 decides
   whether that is `null` or `Terminal`).
4. The holder's `piiKey` **soft-deleted** → treated as absent, exactly as today (`secure.go:199-211`). A
   destruction writes `shredded: true`, never a tombstone; a tombstoned key aspect is an invariant
   violation, and must not open ciphertext.
5. `envelope.Shredded` **or** the backend deny-list → `ErrKeyShredded` → project `null`, keep the row
   (`secure.go:155-159`, unchanged). This is the erasure outcome, not a failure.
6. **A per-row custody failure must not poison the batch** — §6.2.

### 6.2 `SecureColumn` gains a declared holder-type dependency (and loses `IdentityKeyColumn`)

```go
type SecureColumn struct {
    Column      string   `json:"column"`       // unchanged: the ciphertext envelope column
    HolderTypes []string `json:"holderTypes"`  // NEW, REQUIRED non-empty: the vertex types that may
                                              // hold this column's custody, e.g. ["identity"] or
                                              // ["retentionclass"]
    Field       string   `json:"field,omitempty"` // unchanged
}
```

`IdentityKeyColumn` is **removed**, not deprecated — a field the decryptor no longer reads is exactly the
vestigial trap this codebase forbids. Six shipped lenses migrate `IdentityKeyColumn: "identity_key"` →
`HolderTypes: ["identity"]` (ledger #38); they may keep projecting `identity_key` as an ordinary display
column, and none needs a cypher change. **This is a mechanical call I adjudicated rather than a fork.**

`HolderTypes` is **required and non-empty**, validated in the existing fail-closed
`validateSecureColumns` (`corekv_source.go:848-903`) — same posture `IdentityKeyColumn` has today, so no
lens can silently escape the enumeration below. Each entry must be a valid type segment.

**`HolderTypes` is not defense-in-depth; it is the enumeration key.** §6.3's erasure mechanism must answer
"which lenses' rows depend on holder type T?" without parsing compiled cypher — the mechanism
`keyshredded/manager.go:17-21` explicitly rejects (*"opaque compiled cypher, not a declared field"*). It is
also the first declared type-dependency any lens spec has ever carried (ledger #31), which is worth saying
out loud: it is a new kind of declaration, chosen because the alternative is inference.

**Why a list.** Because re-classification is legal and non-migrating (§6.1): after a class change, one
column legitimately carries `keyId`s of two holder types across rows. Extending the list is the lens
author's explicit act, and until they do it the un-listed rows fail closed per-row.

**Per-row failure semantics (this is fork F2).** Today `Apply` returns on the first error
(`secure.go:98-110`) and `evaluateForEntry` discards the entire result set (`evaluate.go:81-83`) → the
whole message DLQs. Combined with the fact that a non-seeded plain lens reprojection recomputes **all**
rows (ledger #21), **one bad row permanently blocks every other row of that lens from ever updating —
including a later erasure scrub.** Under a class holder that is not a hypothetical: any pre-flip document,
or any row whose holder type is not yet listed, is such a row.

My ruling: **every secure-column resolution failure projects `null` for that column, keeps the row, and
raises the privacy-critical failure tier (health + log + counter); nothing discards the result set.**
Reasoning: a `Terminal` means the row is never written, so a **previously projected plaintext row survives**
— which on this plane is the wrong direction. `null` can never over-reveal. The cost is that a defect
becomes visually indistinguishable from a legitimate shred, which is exactly what
`secure.go:170-178` chose to avoid for the missing-`Field` case; the compensation is that the alarm is
loud and privacy-tiered (the same tier `keyshredded` already raises, which pauses the lens). **This changes
shipped behavior for six live secure lenses, so it is Andrew's call, not mine.**

### 6.3 How a key DESTRUCTION reaches the read models

**The problem, stated exactly.** A destruction commits a write to `<holderKey>.piiKey`. For a Secure Lens
to scrub, either that CDC event must reach the lens's pipeline, or something must re-project the lens.

**Why the CDC path cannot be the guarantee for a class holder.** Delivery is decided by
`plainReprojectLabels`, derived from `CompiledRule.ReferencedLabels()`, which collects labels from **node
patterns only** (`labels.go:110`; `PropertyAccess` recurses only into its target, `:122-160`). Two gates
then drop the event independently:

- **Gate A — the consumer never subscribes.** `NarrowedFilterEligible` gates on exactly three conjuncts
  (`pipeline.go:908-910`) and has **no secure-decryptor conjunct**; `ConsumerFilter` emits
  `$KV.<bucket>.vtx.<label>.>` per label (`subjects/subjects.go:124-128`, `:170-178`), which covers that
  label's aspects. `vtx.retentionclass.<H>.piiKey` matches only `vtx.retentionclass.>`, absent from the set
  for a lens labelled `{appointment, provider}`.
- **Gate B — the plain aspect arm Ack-drops it.** `evalPlainAspectReprojection` returns `Ack` unless
  `plainReactsTo(parentType)` (`pipeline.go:1769-1772`, `:683-691`), and `retentionclass` is not in the
  label set.

**And no fallback catches it.** There is no SweepPlan for a Secure Lens — it is a plain projection lens and
never reaches `InstallActorAggregate` (`driver.go:336`, `:435-440`); note the stated reason in
`pipeline/sweep.go:29-33` ("auth-plane only") is **stale**, the real gate is `sweepEnrolment`
(`driver.go:309-324`). And `keyshredded` **explicitly excuses** secure lenses
(`manager.go:31-37`) on the strength of the piiKey-CDC reprojection this section just showed does not
arrive — so that excuse becomes unsound the moment custody is not a bound node. It could not be retrofitted
as written either: `NullifyRow` keys on the row's own identity column (`manager.go:357-358`), which an
appointment-keyed row does not have.

**Failure scenario, concretely.** `clinicEncountersRead` (protected, secure,
`MATCH (a:appointment)-[:withProvider]->(pr:provider)`) projects a decrypted clinical note into Postgres.
`ShredRetentionClassKey` destroys the class DEK; the CDC event fires; JetStream never delivers it; no
sweep; no nullify target. **The row keeps the full plaintext note forever while the destruction reports
success** — strictly worse than today's un-projected plaintext. This is why Increment 2 gates every
vertical.

**Why a lazy next-reprojection scrub is not sufficient, even though it works.** It genuinely does work:
the null is convergent, because a later reprojection re-attempts the decrypt and fails identically
(`secure.go:155-159`), so the scrubbed value is stable — unlike a `NullifyRow` delete, which
`manager.go:333-350` itself documents can be undone by a later CDC delivery. But an erasure obligation is a
statement about a **point in time**: after the destruction completes, no read model holds the plaintext.
Lazy scrubbing gives no bound on when, no enumeration of what is outstanding, and nothing to attest
against. A class-level holder has **no per-row anchor to walk back from**, so there is not even a
per-identity target list to iterate. And the delivery it relies on is exactly the pair of gates above,
whose correctness depends on a label set being right at the moment of the event. Liveness is not a
guarantee.

**The mechanism: a scoped rebuild, driven by the destruction event.**

1. `privacy.retentionClassKeyShredded` is consumed inside the Refractor — the same process and the same
   event-consumer shape `keyshredded` already has (`manager.go`), which is where the lens registry lives.
2. **Enumerate**: every *active* lens with a secure column whose `HolderTypes` contains the destroyed
   holder's type. Declared metadata, not cypher inference.
3. **Rebuild each**, via a new **exported** `control.Service.RebuildRule` — today `rebuildRule` is
   unexported (`control/service.go:865-878`) while `PauseRule`/`NullifyRow`/`NullifyActor` are exported for
   exactly this kind of in-process caller (ledger #29). This is the one genuinely new piece of plumbing.
4. **Attest**: a rebuild returns `{Started: true}` immediately (`service.go:872-876`) and completion is
   observable when `watchRebuildCompletion` flips the lens back to active at zero lag
   (`pipeline.go:1435-1440`). The worker records `projectionsRebuilt` on the holder's `piiKey` once every
   affected lens is back — the analog of `projectionsNullified`, surfaced by §4.4's lens.

**Why a rebuild is the right instrument, verified rather than assumed:**

- It **re-runs the SecureDecryptor for every row**, through the single choke point every plain-lens
  evaluation path flows through (`evaluate.go:81-83`, plus `pipeline.go:1745`, `:1929`). A destroyed key →
  `ErrKeyShredded` → `null`. (Ledger #27.)
- It **needs no truncate and deletes nothing** (`pipeline.go:1394-1406`; Postgres targets are unguarded,
  `adapter/postgres.go:355-357`). We want upsert-with-null, not deletion — the row is *retained*, only its
  plaintext is gone. Correct semantics, and no `TRUNCATE` blast radius.
- It is **custody-shape-independent**: the rebuild re-delivers the last value of every key matching the
  lens's own filter (`DeliverLastPerSubject`), so it re-evaluates every row whether or not the holder type
  is in `reprojectLabels`. It therefore does **not** inherit the label-narrowing blindness that broke the
  CDC path — which is precisely the property that makes it a guarantee rather than a hope.
- Its cost is bounded by rarity: a class-key destruction is an operator-scheduled, per-class event.

**Over-rebuild, not under-rebuild.** The enumeration is by holder *type*, not holder *instance*, so
destroying one class's key rebuilds lenses that also carry other classes' rows. That is the fail-closed
direction and the event is rare; narrowing it would require per-instance declaration and buys nothing.

**Two risks in the rebuild path, named.** (i) Concurrent destructions could launch two rebuilds of one lens
(`rebuildRule` spawns a goroutine per request) — Increment 2 serializes/debounces per lens. (ii) A rebuild
recomputes `ConsumerFilter()` fresh (`pipeline.go:1415`), so a spec whose `HolderTypes` changed also
re-derives its steady-state filter — desirable, but it means a rebuild is the *only* thing that picks up
such a change, which the increment must state.

**Fork F1 — does the identity kind move to this mechanism too?** My recommendation: **no**, and here is the
*derivation* rather than the observation. For custody kind `identity`, the holder vertex **is the vertex
hosting the ciphertext** — an identity-anchored sensitive aspect is reachable in cypher only through a node
bound to that identity. So either the lens binds `(id:identity)` and `identity` enters the label set
(`labels.go:110`), or it binds an unlabeled node, which clears `exhaustive` and sets `reprojectAll`. **In
both branches the lens reacts.** The in-band guarantee for the identity kind is therefore *derived from the
key shape*, not an accident of the current corpus — which is exactly what the class kind cannot have, since
its holder is not the ciphertext's host and nothing forces the lens to bind it. Keeping the in-band path
means no latency regression for six shipped lenses; the cost is two delivery mechanisms in the Refractor,
each with a written-down reason. If Andrew prefers one mechanism, route both through the rebuild and
withdraw `keyshredded`'s secure-lens excuse entirely — heavier, uniform, and also defensible.

### 6.4 Custody and authorization are two planes — and the end state after an erasure

**Custody** answers *"can this be decrypted at all?"* — a property of key material, with exactly two
answers, identical for every actor. **Authorization** answers *"which actor sees this row?"* — D1,
Protected lens, RLS, `secureColumns`, the grant table. They compose but never substitute: no grant can open
a destroyed key, and an intact key confers no visibility.

**The acceptance criterion.** After `ShredIdentityKey(vtx.identity.<P>)` where P is a patient with a
recorded encounter:

| | Before | After |
|---|---|---|
| `vtx.identity.<P>.name/.email/.phone/.ssn` (custody `identity`) | readable | **unrecoverable**; Secure Lens columns `null` |
| `vtx.appointment.<A>.encounter` PHI (custody `retentionClass`) | readable | **still readable** — the clinical record survives |
| The link `vtx.patient.<Q>` → `vtx.identity.<P>` | present | present, but resolves to nothing readable |
| Net | identified clinical record | **pseudonymized retained record** |

That is the criterion: *the retained record survives while its subject's PII is unrecoverable.*

**Two obligations that fall out of it, and they are not optional:**

1. **A retained record must not duplicate the subject's direct identifiers.** If the encounter note's
   `summary` contains the patient's name, the erasure is defeated by duplication and the whole plane is
   theatre. This is a Verticals obligation per retained class, and it belongs in each class's declared
   `description`.
2. **Clinic has a live instance of exactly that failure, pre-existing this design.**
   `vtx.patient.<id>.demographics` = `{fullName}`, explicitly non-sensitive
   (`clinic-domain/ddls.go:595-601`), so a patient's name survives `ShredIdentityKey` in plaintext today.
   This design **deliberately provides no home for it** (§9.5): the name belongs on the identity's already-
   sensitive `.name` aspect, and moving it is the fix. Increment 3 either does that or the acceptance
   criterion is not met for clinic — Andrew's scoping call (§10).

### 6.5 The REVEAL axis — a decrypt with no actor and no purpose is denied

The wholesale trusted-tool RPC carries **neither an actor nor a purpose**: `DecryptRequest` is
`{identityKey, envelope, ciphertext}` (`vault/service.go:74-78`), and authorization is entirely
transport-level natsperm (`matrix.go:297` grants Loupe; nothing else holds it). For an identity-custodied
aspect that is coherent under the trusted-tool posture — there is a data subject, and an operator revealing
their PII is an accountable act against a named person. **For a retention class there is no data subject at
all**, possibly by design (they exercised erasure), so there is nobody whose grant scopes the disclosure.

**Increment 1's posture: refuse, structurally.**

- Loupe's Reveal already refuses a non-identity anchor (`cmd/loupe/vault.go:149-153`). That refusal stops
  being incidental and becomes load-bearing — but its **reason must be re-derived**: it currently claims
  *"a sensitive aspect's DEK is always custodied by its anchoring identity (Contract #1 §1.6)"*, which
  becomes false. It becomes a keyId-based refusal: *the ciphertext's holder is not an identity; a
  retained-class reveal requires a declared purpose, which this console does not carry.*
- The **Secure Lens is the sanctioned read path**, and it already carries actor authorization (Protected +
  RLS + grant table). So the refusal is not a gap: the real consumer — a provider reading their patient's
  note — is served, with authorization on the plane that has an actor.
- The Refractor's decryptor calls `vault.Decrypt` **in-process** (`secure.go:153`), not over the RPC, so
  refusing on the RPC does not touch it.

**The purpose-scoped surface, specified and deferred, default-deny by construction.** When a consumer
exists (an operator reveal of a retained record, or an audit export), it is a **new** subject
`lattice.vault.decryptretained` taking `{ciphertext, envelope, purpose, actor}`, with:

- `purpose` **required non-empty**, matched against the holder's `.retentionPolicy.permittedPurposes`;
- the actor authorized for that purpose on the authorization plane, not inside the Vault (the Vault holds
  no actor state and should not start);
- **no natsperm grant to any component by default** — so the surface is deny-by-construction until a
  component is explicitly granted, which is a reviewable diff rather than a runtime check.

Deferring it is not "we'll get to authorization later": the refusal above is the fail-closed default, and
the sanctioned path is authorized today.

---

## 7. Retention & expiry semantics

### 7.1 What Increment 1 actually delivers

- A retained record is **encrypted at rest** under a controller-owned key, and **survives** any data
  subject's erasure.
- The class's `retentionPeriod` is **declarative** — the controller's stated schedule, surfaced by
  §4.4's lens. There is no timer.
- Destruction is **operator-invoked** (`ShredRetentionClassKey`) and destroys the **whole class**.

### 7.2 The limitation, stated plainly

An un-periodized class key cannot expire one aged-out record. So Increment 1's destruction op is, in a live
deployment, only safely usable once the entire class has aged out. It is not dead scaffolding — it is the
only thing that makes the erasure end-to-end provable (§11's acceptance test) and the read-side delivery
testable — but the doc does not pretend the erase-on-expiry policy is fully mechanized. Naming this is the
point of §10's F-notes.

### 7.3 Verifying Andrew's deferral claim

> *"Because the ciphertext is self-describing, records written under an un-periodized holder keep their own
> `keyId`, so introducing periodized holders later needs no re-keying migration."*

**Verified true, conditional on three things — and the third is a rule this design must state now:**

1. **The read path resolves custody from `ct.KeyID`** (§6.1). Without that, an old record would be looked
   up under the *current* holder and fail. This is Increment 1/2 work, so the condition is met by
   construction, not by luck.
2. **The holder's key aspect is not destroyed.** Trivially: a live old key still opens old records.
3. **Periodization must vary the holder's *id*, never its *type*.** A periodized holder must remain
   `vtx.retentionclass.<derived(class, period)>`. If periodization introduced a new *type*, every
   `HolderTypes` list would silently stop covering the old rows and they would fail closed per-row. This is
   a **binding constraint on the deferred increment**, recorded here so it is not rediscovered as a bug.

Given those, periodization is purely additive: step 6.5 starts resolving a different holder id (derived
from the declared base plus a clock bucket, minted lazily in-batch exactly as §4.2 already does), and every
prior record keeps decrypting. No migration, no re-keying, no dual-read window.

---

## 8. Alternatives considered

**8.1 Do nothing (keep `.encounter` / `.profile` plaintext, rely on access control).** The status quo.
Cannot satisfy erasure on an immutable ledger — the premise of the whole Vault plane, already rejected once
in `vault-crypto-shredding-design.md`. Also leaves PHI in JetStream history forever. Rejected.

**8.2 One deployment-wide key.** Zero expiry granularity: the only erasure is "erase everything." That is
disk encryption, which belongs to the substrate, not to a privacy plane. Rejected (Andrew, in session).

**8.3 A per-controller key only (no data-class dimension).** Necessary but insufficient — it cannot expire
one aged-out record, and it merges data classes with different retention schedules under one key, so the
shortest schedule effectively governs all of them. Rejected. Note Increment 1's un-periodized class key is
*this alternative scoped to one class* (§7.2) — honest, and the reason periodization is a named follow-on
rather than a nice-to-have.

**8.4 Keep the held design (per-identity custody via a caller-supplied `subjectKey`).** Its headline
property is the defect for this data class: honoring an erasure destroys the record the controller must
keep. It also adds an attacker-controlled field to a plane whose sites deliberately removed one
(`vault/service.go:455-457`), collides with lease-signing's shipped `subjectKey` payload field
(`lease-signing/ddls.go:286`), needs a Contract #1 envelope change, needs a custody-immutability rule with
three enforcement holes, and needs `VertexDoc` + Starlark plumbing — all of it to police a pointer this
design does not have. Held by Andrew, 2026-08-06.

**8.5 Held-design Increment 4's second per-identity DEK (`retainedPiiKey`).** Half-sees the problem and
answers it in the wrong place: the record's key lifetime still hangs off the *person*, so a
`ShredRetainedKey` is per-person, and a controller cannot expire a data class. It also doubles the
per-identity key count for a distinction that is not about the person at all. Rejected.

**8.6 Per-vertex custody + a shred cascade** (give the host vertex its own key; make `ShredIdentityKey`
cascade). The cascade must **enumerate** "vertices subject to this identity" on the erasure path, and that
enumeration's completeness *is* the erasure guarantee: a missed edge is a silent, permanent erasure failure
with a success signal on it. Trades an unconditional guarantee (one key destruction, no enumeration) for a
fallible one. Rejected — and note it does not dodge §6.3 either.

**8.7 A third custody kind for erasable-but-off-identity data** (custody = a subject identity reached from
the anchor). This is the held design's mechanism narrowed to the case where it is actually right. There
*is* a live instance: `patientDemographics.fullName` (§6.4). Rejected anyway, because the holder would be
**per-row**, not DDL-resolvable, which reopens every problem §3.3 closes (caller-supplied or traversed).
The better answer for that data is data placement: a person's name belongs on the person's own
already-sensitive `.name` aspect. Deliberately out of scope — and named, because that is the honest
boundary of this design (§9.5).

**8.8 Bind the holder as a node in every secure lens's MATCH** (instead of a declared `HolderTypes`).
Rejected: a convention where a mechanism belongs — the guarantee would hold only while every future author
remembered, with a silent, shape-dependent erasure failure as the penalty, and no gate able to tell a
correct lens from a broken one. Often unexpressible too: the holder may be reachable by no path the lens's
own query needs. `HolderTypes` is validated, required, and machine-enumerable.

**8.9 Extend the external-egress ref plane now.** The MAC objection is **gone** — it already covers
`ct.KeyID` (`refmac.go:24`), so switching `service.go:478` and `egress.go:174` to `ct.KeyID` introduces no
attacker-controlled field and needs **no `sensitive-ref/v2`**. What remains is narrower and real: the
bridge fetches the envelope off `piiKeyEnvelope`, whose spec is `MATCH (i:identity)`
(`privacy-base/lenses.go:113`), so a class holder has no row. Extending means widening that lens (or adding
a sibling) with live consumers on it. Census: `grep -rn "egressReads" packages/` → `orchestration-base`
(the helper) and `lease-signing`, and every aspect it egresses is identity-anchored. **Live consumers of a
retained-class egress ref: zero.** Deferred with the work precisely sized (§11 Increment 5a) rather than
shipped inert.

---

## 9. Risks, and the claims I checked rather than assumed

| Risk / claim | Disposition |
|---|---|
| "The Vault needs a new interface for a non-identity holder" | **False** — it takes an arbitrary string (`local.go:203`, ledger #1). Only the parameter *name* is a misnomer; Increment 1 renames it, because leaving `identityKey` on a retention-class call is the stale-comment trap this codebase forbids. |
| Trusting `ct.KeyID` lets an attacker redirect custody | **Cannot** — `keyId` is the AEAD AAD (`local.go:238`, `:252`), so substitution fails the GCM tag. Requires direct Core-KV write (already outside P2) even to attempt. |
| …but a substituted `keyId` pointing at a **shredded** holder returns `ErrKeyShredded` **quietly** (null, not error) | **Real, and the only quiet case.** Closed by `HolderTypes` (§6.2): the type must be declared. Residual: an attacker with direct Core-KV write could forge an erasure-looking null within a declared type. Requires a P2 violation to reach. |
| Flipping an aspect to sensitive is transparent to plain lenses | **False for field-level projection** — step 6.5 encrypts the whole `data` map (`step65_encrypt.go:83-101`) and three shipped clinic lenses project operational fields out of `.encounter.data` (`lenses.go:463-465`, `:646-648`, `:681-683`). They would go null, green, with no error. The aspect must be **split** (§9.1). |
| The destruction scrubs a projected class-custodied row | **False without Increment 2** — two delivery gates drop the event and no fallback catches it (§6.3). This is the gate on every vertical. |
| A per-row custody failure is contained | **False today** — it discards the whole result set (`evaluate.go:81-83`) and a non-seeded plain lens recomputes all rows (ledger #21), so one bad row stalls the lens indefinitely. Fixed in Increment 2 (fork F2). |
| The Refractor observes a `Vault.ShredKey` | **False** — it holds its own LocalBackend (ledger #13). The scrub depends entirely on the durable `shredded: true` write, which is why §4.3 mirrors `ShredIdentityKey`'s placeholder discipline rather than "just calling ShredKey". |
| A class-less mutation bypasses the sensitivity gate | Pre-existing; closed in Increment 1 (§4.1) because this design is what puts PHI behind it. |
| Steps 6 and 6.5 disagree via the shared live-read budget, failing open to plaintext | Closed in Increment 1 — budget exhaustion becomes a hard error in 6.5 (§4.1). |
| Custody silently moves or is stripped on update | **Cannot** — custody is a property of the ciphertext, written in the same batch by the same code (§6.1). The held design's rule is unnecessary. |
| Re-classifying a data class orphans committed records | **Cannot** — each keeps its own `keyId` (§6.1). Requires the lens author to extend `HolderTypes`; until then those rows fail closed per-row. |
| A retained record outlives the erasure it should have honored | **By design, and it is the point.** The safeguard is the §6.4 obligation: a retained record must not duplicate the subject's identifiers. Clinic violates it today via `.demographics` (§6.4, §9.5). |
| Two concurrent first-writes under one class collide on the deterministic holder key | Already solved — `mintedPiiKey` → OCC retry (`step65_encrypt.go:30-38`, `:104`). |
| Concurrent rebuilds of one lens | Named risk; Increment 2 serializes per lens (§6.3). |
| `retentionClass` as a camelCase vertex type | **Would not build** — type segments are `[a-z][a-z0-9]*` (ledger #32). The type is `retentionclass`. |
| Increment 1's destruction op is unusable in production | **True and stated** (§7.2). It is the platform primitive plus the erasure test vector; per-record expiry needs periodization. |

### 9.1 The aspect split is required, and it generalizes past clinic

Step 6.5 encrypts the **entire `data` map**, a property inherited from the ratified aspect-level-granularity
decision (`lattice-architecture.md:1022`: *"If some properties of an aspect are sensitive and others aren't,
they should be separate aspects"*), not something this design chooses. So **every non-sensitive field
sharing a sensitive aspect's `data` becomes unreadable to every plain lens.**

- **Clinic `.encounter`** is exactly that shape: `{summary, assessment, plan, documentedAt,
  followUpRequested, followUpDate}` (`clinic-domain/ddls.go:909-911`), with the operational half projected
  by three shipped lenses. Nor is it recoverable by declaring a secure column: `validateSecureColumns`
  refuses `secureColumns` on a non-protected lens (`corekv_source.go:852-854`), and `clinicAppointments`
  is unprotected.
- **Lease `.profile`** is the same shape *and worse*: it mixes retained financial facts (`annualIncome`,
  `guarantorAnnualIncome`, `incomeToRentMet`) with **third parties' direct identifiers**
  (`guarantorName`, `coApplicantName`, `coApplicantContact` — `scripts.go:1039-1056`) and with plainly
  operational flags (`employmentVerified`, `referenceCount`, `submittedAt`, `:1013-1022`). Those three
  populations have three different custody answers: retained, **erasable on the third party's own
  identity**, and not sensitive at all. So `.profile` needs a **three-way** split, and the identifier third
  needs §8.7's data-placement answer, not a custody kind.
- **`leaseServiceOutcome`** projects `status`/`validUntil` in six lens sites
  (`lease-signing/lenses.go:646`, `:696`, `:698`, `:712`, `:873`, `:875`;
  `renewal_lenses.go:217`) — so if a background-check *result detail* is ever stored, it must be a new
  sibling aspect, never a flag on this one.

**The generalized rule, which every future flip owes:** before setting `Sensitive: true` on an existing
aspect, census every lens that projects any field of it, and split until the sensitive aspect's `data`
contains nothing a plain lens reads.

### 9.2 Correction: lease-signing's `.profile` has no aspect-type DDL at all

The demand as filed implies flipping a flag. It cannot: `.profile` is written by the **`leaseapp`
vertexType** DDL's script (`scripts.go:1060`), and no aspectType DDL exists for class `profile`
(`lease-signing/ddls.go:68-71` is the vertexType; lines 120-170 are its `InputSchema`/`FieldDescription`).
Sensitivity and custody are **aspectType-DDL properties** (`sensitivescope.go:22-38`), so Increment 4 must
**author a new aspectType DDL** for the split sensitive aspect, with `permittedCommands`, before anything
can be marked sensitive. That is a real size difference from clinic's flip.

### 9.3 Correction: the background-check `outcome` is a prospective class, not a live leak

`leaseServiceOutcome` = `{status ∈ {completed, failed}, completedAt, validUntil}`
(`lease-signing/ddls.go:473-480`), and `service-domain`'s generic `.outcome` = `{status, completedAt}`
(`service-domain/ddls.go:1317-1318`, with **no** aspectType DDL at all). Neither stores an adverse-action
detail, a report body, or any raw result. So the board row's "three retained-class records sit plaintext in
Core KV" is true for two. The background check is the *policy* example Andrew named — a class whose
retention obligation is real — and it becomes a live instance only when a package stores the detail. Filed
as a deferred trigger (§11 Increment 5d), not as work.

### 9.4 Migration & compatibility

**Increment 1 is additive and backward-compatible for every shipped sensitive aspect.** All nine
identity-domain aspects take branch 1 of §4.1 with an absent `Custody`, and behave byte-identically.

**Flipping an existing aspect to sensitive is NOT retroactive — and that is the sharp edge.** Step 6.5
encrypts on write; already-committed aspects stay plaintext. On the **read** path a pre-existing plaintext
body now resolves as sensitive, `ciphertextFromData` parses it into a `Ciphertext` with an empty `CT`, and
`Vault.Decrypt` fails — so the operation that touches it **fails**. The pre-existing population does not
degrade quietly; it bricks the reading op. On the projection side it is §6.2's per-row population.

**Ruling:** a **full-stack reset** at each vertical increment's delivery boundary (`make down && make
up-full`), mirroring the Vault plane's own delivery boundary. Nothing runs in production; NATS and Postgres
are ephemeral by design. **No migrate-encrypt path is built.** A prod-era deployment adopting sensitivity
after accumulating data needs one; that is a follow-on when such a deployment exists. §6.2's per-row
containment lands regardless — "the data is clean" is not a mechanism.

### 9.5 What this design deliberately does not do

It provides **no home for erasable sensitive data anchored off an identity**. Custody kind `identity`
requires the aspect to anchor on the identity, so such data must **move onto the identity**, where an
already-sensitive aspect exists. The one live instance is `patientDemographics.fullName`
(`clinic-domain/ddls.go:595-601`). Scoping that move is §10's third fork.

This is also the cleanest statement of why the redirect is right: the held design's `subjectKey` was a
mechanism for the case that has a *simpler* answer (data placement), while the case that genuinely cannot
move — a record whose retention outlives its subject — needed a different kind of key entirely.

### 9.6 Seams named and left closed

- **Multi-tenancy.** A retention class is *the controller's* declaration; today there is exactly one
  controller per deployment and no tenant/org vertex exists at all (ledger #37). The seam: when tenancy
  lands, a retention class becomes tenant-scoped (holder id derived from `(tenant, class)` — the same
  id-varies/type-fixed rule as §7.3's periodization). **The fork is not opened here.**
- **Production KMS backend.** `ShredKey` on the local backend is a deny-list refusal, not key destruction
  (`local.go:65-71`). Unchanged by this design and equally true of both holder kinds.
- **Contract #3 §3.11 blobs** stay identity-custodied. A retained *blob* (a scanned clinical document) is a
  real future demand and would reuse this holder model, but §3.11's caller-supplied `governingIdentity`
  makes it a separate design. Not opened.

---

## 10. Forks needing Andrew

**F1 — one delivery mechanism or two?** Keep the identity kind's in-band piiKey-CDC scrub (§6.3's
derivation shows it is sound *by key shape*, not by corpus accident) and add the rebuild path only for
non-identity holders — **my recommendation**, no regression for six shipped lenses, cost is two mechanisms.
Or route both through the rebuild and withdraw `keyshredded`'s secure-lens excuse (`manager.go:31-37`)
entirely — uniform, heavier, one mechanism to reason about.

**F2 — per-row secure-column failure: `null` + privacy-tier alarm, or keep `Terminal`?** I recommend
`null`, because `Terminal` leaves a previously projected **plaintext** row in place (§6.2). It changes
shipped behavior on the security plane for six live secure lenses, so it is yours. The alternative that
splits the difference — `null` for custody failures, `Terminal` for ciphertext-shape failures — is
available but gives two rules where one would do.

**F3 — planning-artifact and scope calls I cannot make.**
(a) `lattice-architecture.md:1019` (*"crypto-shredding destroys the identity's key → all sensitive aspects
for that identity become irrecoverable"*) becomes false for a retained class. That file is planning-lead
owned and I have not touched it; Item 6 needs an amendment.
(b) Is moving `patientDemographics.fullName` onto the identity in scope for Increment 3? Without it,
clinic does not meet §6.4's acceptance criterion — the patient's name survives their erasure in plaintext.
(c) The board row's "three retained-class records sit plaintext" should read two-plus-one-prospective
(§9.3); I have not edited the board.

---

## 11. Decomposition (each independently shippable and green)

**Increment 1 — the custody primitive (Lattice lane, L).** `CustodySpec` + `RetentionClassSpec` on
`pkgmgr`, their reserved DDL aspects and `MetaVertexRef` fields, the four install-time validations, the
`retentionclass` vertex type + `.retentionPolicy`, `pkgmgr.RetentionClassID`; step 6's conditional
`sensitiveAspectScope` plus the class-less and budget-exhaustion closures; step 6.5's holder resolution and
`ensureKeyHolderKey`; **the Processor read path switched to `ct.KeyID`** (`sensitive_decrypt.go`), the
non-identity silent branch deleted, and the typed refusal for a non-identity holder under `egressReads`;
the `Vault` interface parameter rename `identityKey` → `keyHolderKey` with every doc comment re-derived;
`ShredRetentionClassKey` + `privacy.retentionClassKeyShredded` + `piiKey`'s `permittedCommands`; the
retention-class shred worker; the `retentionKeyStatus` lens. Contract #1 §1.6 + Contract #3 §3.10 edits
staged **UNCOMMITTED** as the proposal diff.
**Acceptance test (the criterion, provable with zero lenses):** write a retained aspect on an appointment;
`ShredIdentityKey` the patient; assert the patient's `.name` fails `ErrKeyShredded` **and** the encounter
still decrypts; then `ShredRetentionClassKey` and assert the encounter fails too.
Negative test per rule: absent/malformed `keyId`; `keyId` naming a holder with no key aspect; soft-deleted
key aspect; `retentionClass` custody declared with `Sensitive: false`; cross-package class reference;
class-less mutation; non-identity `egressReads`. Security plane → full 3-layer review.

**Increment 2 — Refractor read path + erasure delivery (Lattice lane, M–L). GATES 3 and 4.**
`SecureColumn.HolderTypes` replacing `IdentityKeyColumn`, with validation; the decryptor trusting
`ct.KeyID`; per-row failure containment (F2); `secureIdentityKeyType` deleted; exported
`control.Service.RebuildRule` (serialized per lens); the destruction-event consumer with the
`HolderTypes` enumeration and the `projectionsRebuilt` attestation; the six shipped lens migrations;
`keyshredded/manager.go:31-37`'s excuse and `pipeline/sweep.go:29-33`'s stale reason re-derived.
**Test that pins the guarantee:** a secure lens anchored on a **non-identity** type with class custody,
driven through the real `handle()` path, projects the note; a real `ShredRetentionClassKey` scrubs it to
null; and a *second* rebuild is idempotent. Plus: one un-listed-holder-type row does not stop any other row
from updating. Security plane → full 3-layer review.

**Increment 3 — clinic encounter PHI (Verticals lane, M).** Split `.encounter` into the sensitive PHI
aspect (`Sensitive: true`, `custody.kind: retentionClass`, class `clinicalRecord`) and a non-sensitive
operational sibling; re-point the three shipped lenses at the sibling (§9.1); declare the retention class;
`clinicEncountersRead` protected Secure Lens with `HolderTypes: ["retentionclass"]`; the §6.4 obligation
written into the class description. Plus F3(b) if Andrew scopes it in. **Delivery boundary: full-stack
reset.**

**Increment 4 — lease-signing income (Verticals lane, M–L).** Author the aspectType DDL that does not exist
(§9.2); three-way split of `.profile` (§9.1); the retained financial aspect takes a `underwritingRecord`
retention class; the third-party identifiers move to their own identities (§8.7) or the increment states
why not. **Delivery boundary: full-stack reset.**

**Increment 5 — deferred, each behind a named trigger.**
(a) **Retained-class egress refs** — switch `service.go:478` / `egress.go:174` to `ct.KeyID` (no MAC v2) and
widen the envelope read path past `MATCH (i:identity)`. Trigger: the first package needing to egress a
retained record.
(b) **Period bucketing** — holder id derived from `(class, period)`, minted in-batch; constrained by
§7.3's *id-varies, type-fixed* rule. Trigger: a real statutory clock, or the first class approaching expiry.
(c) **Purpose-gated retained reveal** — `lattice.vault.decryptretained`, default-deny by natsperm (§6.5).
Trigger: an operator reveal or audit export of a retained record.
(d) **Background-check result detail** — a new sibling aspect under a retention class. Trigger: a package
storing the detail (§9.3).

---

## 12. Adversarial pass (run against my own draft; what it changed)

1. **My draft wrote the holder type as `retentionClass`.** It would not have built: a vertex type segment
   is `[a-z][a-z0-9]*` (`keys/keys.go:141-155`). Corrected to `retentionclass` throughout, with the
   convention grounded in the shipped corpus. *Caught by checking the validator instead of the prose.*
2. **My draft claimed "Contract #1 unchanged."** False: §1.6's sensitive-anchoring consequence
   (`01-addressing-and-envelope.md:180`) becomes wrong. The **envelope** is unchanged, which is the property
   Andrew asked for; §5 now says exactly that and proposes replacement text. *An overclaim in the direction
   that makes ratification easier is the worst kind.*
3. **My draft used a scoped rebuild AND label injection.** Once I verified that a rebuild is
   custody-shape-independent (it re-delivers over the lens's own filter, `pipeline.go:1415` + ledger #27),
   the label injection stopped being needed for the guarantee — so it would have been machinery whose
   guarantee I was not claiming. Dropped, and §6.3 now says which property makes the rebuild sufficient.
4. **My draft asserted the identity kind's in-band scrub "works today."** That is a statement about the
   corpus. §6.3 now **derives** it from the key shape (an identity-anchored ciphertext is reachable only via
   a node bound to that identity, so the lens either labels it or clears `exhaustive`), and shows the same
   derivation fails for a class holder. That is what turns F1 from a preference into a decision with a
   reason.
5. **My draft treated per-row containment as a nice-to-have.** Combining `evaluate.go:81-83` with
   `seedAnchorFor`'s all-rows recompute (ledger #21) makes one bad row a **permanent whole-lens stall that
   also blocks the erasure scrub**. Promoted to a blocking Increment-2 item and to fork F2.
6. **My draft said the egress refusal was "unnecessary, the MAC covers keyId."** Half right. The MAC does
   remove the cryptographic objection and kills the held design's `sensitive-ref/v2`. But the bridge's
   envelope source is the identity-only `piiKeyEnvelope` lens (`privacy-base/lenses.go:113`), so the
   refusal is **narrower, not unnecessary**. §8.9 now sizes exactly what lifting it costs.
7. **My draft accepted "clinic meets the acceptance criterion."** It does not:
   `patientDemographics.fullName` is plaintext and non-sensitive (`clinic-domain/ddls.go:595-601`), so a
   patient's name survives their own erasure. Added as §6.4 obligation 2, §9.5, and fork F3(b) — and it
   forced §8.7, which is what clarified the boundary of this design.
8. **My draft repeated the brief's "three live plaintext instances."** Two. `leaseServiceOutcome` stores no
   sensitive payload and is heavily projected (§9.3). Corrected, and flagged as a board-row correction I
   cannot make myself.
9. **My draft said lease-signing "flips a flag."** There is no aspectType DDL for `.profile` at all
   (§9.2) — Increment 4 must author one. And the aspect mixes **three** custody populations, not two, so it
   needs a three-way split. Increment 4 resized.
10. **My draft trusted `ct.KeyID` with no type check** and called `HolderTypes` defense-in-depth. Two
    corrections: it is the **enumeration key** §6.3 cannot work without (cypher is not a declared field,
    `keyshredded/manager.go:17-21`), and without it a substituted `keyId` pointing at a *shredded* holder
    produces the one **quiet** wrong outcome (a forged erasure), since `ErrKeyShredded` short-circuits
    before any tag check. Both now in §6.1 rule 2 and §9.
11. **My draft let the retention-class shred call `Vault.ShredKey` and stop.** The Refractor holds its
    **own** LocalBackend (`cmd/refractor/main.go:1286-1301`), so it would never have seen it. §4.3 now
    mirrors `ShredIdentityKey`'s durable-flag-and-placeholder discipline as a load-bearing requirement, not
    a copied pattern.
12. **My draft left `retentionPeriod` sounding like a timer.** It is declarative in Increment 1, and an
    un-periodized class key cannot expire one record — which makes Increment 1's destruction op unusable in
    a live deployment (§7.2). Stated rather than implied, and §7.3 adds the *id-varies, type-fixed* rule
    that periodization must obey for Andrew's no-migration claim to keep holding.

---

*Designer fire, 2026-08-06. Awaiting Andrew's ratification. **No contract edit is staged** — the proposed
Contract #1 §1.6 and Contract #3 §3.10 text lives in §5.1 of this document only, and will be staged as the
proposal diff on ratification.*
