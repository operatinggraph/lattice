# Retention-class key custody — a sensitive aspect's DEK belongs to a key holder, not to a person

**Status: ✅ Andrew-RATIFIED 2026-08-06** — build-ready as **two fires**, both in the **Lattice lane**
(§11, rewritten at ratification). Authored 2026-08-06 as the redirect of
`subject-anchored-sensitive-aspects-design.md`, held by Andrew the same session.

## Ratification (Andrew, 2026-08-06)

**Ratified, with the three forks resolved and two structural amendments.**

- **F1 — keep both delivery mechanisms.** The identity kind keeps its in-band `piiKey`-CDC scrub; the
  rebuild path is added only for non-identity holders. §6.3 *derives* why in-band is sound for an identity
  (the key aspect hangs off the vertex the lens already binds) and why a class holder has no per-row anchor
  to make it work — so this is specialization with a proof, not two mechanisms by drift, and it regresses
  none of the six shipped secure lenses.
- **F2 — `null` + privacy-tier alarm, not `Terminal`.** `Terminal` leaves a previously projected
  **plaintext** row standing, which is the wrong failure direction on this plane, and today a per-row
  failure discards the whole result set so one bad row stalls the lens *including a later erasure scrub*.
  The alarm preserves the loudness `Terminal` existed for. The split rule (null for custody, `Terminal` for
  ciphertext shape) is rejected: a ciphertext-shape failure is equally a reason to redact rather than
  freeze.
- **F3 — (a)** `lattice-architecture.md:1019` is planning-lead-owned and is **not** touched by this design;
  Item 6 needs an amendment routed separately. **(b)** Moving `patientDemographics.fullName` onto the
  identity is **in scope** — without it the clinic consumer ships a "pseudonymized record" claim that is
  false with the name sitting in plaintext beside it, and §6.4 is this design's own acceptance criterion.
  **(c)** already applied; the board row reads two live plus one prospective.

**Amendment 1 — everything stays in the Lattice lane.** The original §11 routed Increments 3 and 4 to the
Verticals lane. Andrew: *"keep everything under lattice backlog, do not split parts into verticals to avoid
ping-pong or stale 'blocked by'."* One item, one lane, one owner, one row — including the clinic FE surface
for the note, which the verticals board previously carried as a `🚧 blocked-on` row that is now struck and
absorbed here.

**Amendment 2 — fewer, larger fires.** Four increments collapse to two, per the standing rule. §11 below is
rewritten; the original four-increment text is superseded, not merely re-labelled.

**Contract edits committed at ratification** (Contract #1 §1.6; Contract #3 §3.10's six spans and §3.11's
cross-references), each carrying a transitional note, since the runtime arrives with Fire 1 and a
present-tense clause with nothing behind it is fail-open. §5.1's quoted text is what landed.

**Corrections folded at ratification** (independent DD pass over this doc's ledger — 13 of 14 priority
claims verified exact, and the substance of every one of them held):

- **`refmac.go`'s `ct.KeyID` append is line 25, not 24** (line 24 appends `requestID`). The MAC does cover
  `keyId`; only the pointer was off.
- **Ledger #38 overclaims on one of the six secure lenses.** Not all use `IdentityKeyColumn:
  "identity_key"` — lease-signing's `landlordLeaseApplicationsRead` uses `"applicant"`. The count of six is
  confirmed and they are: `clinicPatientsRead`, `identityCredentialsRead`, `applicantRosterRead`,
  `landlordLeaseApplicationsRead`, `wellnessIdentitiesRead`, `cafeIdentitiesRead`. Fire 1's migration
  surface is unchanged, but a builder must not assume a uniform column name.
- **Every `clinic-domain` line citation in the ledger is stale by dozens of lines** — commit `dbe9e65e`
  ("appointments finally carry a site") landed the same evening, *after* this design was authored, adding 86
  lines to `ddls.go` and 68 to `lenses.go`. `encounterAspectTypeDDL()` now starts at :906, and the three
  lens sites projecting the operational triad are at :496-498, :714-716, :749-751. The substance re-verified
  true at the new locations. This is parallel-fire base skew inside a single session; Fire 2 re-derives
  rather than trusting any pointer here.
- **`rebuildRule` is reachable today, just not programmatically.** Beyond the CLI, Loupe's operator-control
  proxy also allow-lists `"rebuild"` (`cmd/loupe/control.go:64-69`) — a second *human-driven* path. The
  design's substantive point stands: there is no automated in-process caller, which is what Fire 1 needs
  when it exports `RebuildRule`.
- **No premature adopter exists** — `retentionclass`, `HolderTypes` and `custody`-as-declared-field return
  zero hits across `internal/`, `packages/` and `cmd/`. Fully greenfield.
- **No collision with `erasure-orchestration-design.md`** and no forced ordering either way: the shared
  touch points (`privacy-base`'s manifest, `pkgmgr`'s spec structs) are additive in different sections, and
  the erasure design deliberately keys its write-path gates off a separate `.erasureRequested` aspect rather
  than `piiKey.shredded`, precisely so a retention-class shred cannot freeze a person's writes. The one
  build-hygiene caution: this design mirrors only `shred_identity_key.go:267-311` into a sibling script —
  the exact slice the erasure design says it keeps unchanged — so mirror that slice, never the whole file.

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

**Amended 2026-08-08 (Fire 2 item 1): the census is repo-wide, not package-wide.** The clinic census above
listed three lenses — all three in `clinic-domain` — and missed a fourth consumer in a *different* package:
`clinic-reminders`' `followUpRemindersSpec` (`followups.go:274-285`) reads
`a.encounter.data.followUpRequested` / `.followUpDate` in five places, including the `freshUntil` CASE that
arms a Weaver `@at` timer and both gap predicates. An aspect's declaring package does not own its readers.
Census **every package**, and every WHERE / CASE / predicate, not just RETURN clauses.

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

## 11. Decomposition — TWO fires, both in the Lattice lane (rewritten at ratification 2026-08-06)

Andrew's amendments: everything stays in the **Lattice lane** (no Verticals split — one item, one lane, one
owner, no cross-lane `blocked-on` to go stale), and **fewer, larger fires**. The original four increments
collapse to two; that text is superseded.

### Fire 1 — the custody platform (Lattice, L–XL). Green with zero consumers.

Old Increments 1 and 2 as one fire. They are coupled by their own acceptance criterion: Increment 1's
primitive is unobservable until a read model can project under a class holder, and Increment 2's delivery
guarantee is meaningless without the primitive. Internal build order:

1. **Custody vocabulary + write path.** `CustodySpec` + `RetentionClassSpec` on `pkgmgr`, their reserved
   DDL aspects and `MetaVertexRef` fields, the four install-time validations, the `retentionclass` vertex
   type (**note the key segment is `retentionclass` — `[a-z][a-z0-9]*`, so no camelCase**) +
   `.retentionPolicy`, `pkgmgr.RetentionClassID`; step 6's conditional `sensitiveAspectScope` plus the
   class-less and budget-exhaustion closures; step 6.5's holder resolution and `ensureKeyHolderKey`.
2. **The read path switches to `ct.KeyID`** — all five sites, the non-identity silent branch deleted, the
   typed refusal for a non-identity holder under `egressReads`; the `Vault` interface parameter rename
   `identityKey` → `keyHolderKey` with every doc comment re-derived.
3. **Erasure vocabulary + delivery.** `ShredRetentionClassKey` + `privacy.retentionClassKeyShredded` +
   `piiKey`'s `permittedCommands`; the retention-class shred worker; the `retentionKeyStatus` lens;
   `SecureColumn.HolderTypes` replacing `IdentityKeyColumn` with validation; **per-row failure projects
   `null` + a privacy-tier alarm** (F2); `secureIdentityKeyType` deleted; **`control.Service.RebuildRule`
   exported** (serialized per lens); the destruction-event consumer with the `HolderTypes` enumeration and
   the `projectionsRebuilt` attestation; the six shipped lens migrations; `keyshredded/manager.go:31-37`'s
   excuse and `pipeline/sweep.go:29-33`'s stale reason re-derived.

**Acceptance test (the criterion, provable with zero lenses):** write a retained aspect on an appointment;
`ShredIdentityKey` the patient; assert the patient's `.name` fails `ErrKeyShredded` **and** the encounter
still decrypts; then `ShredRetentionClassKey` and assert the encounter fails too. **The guarantee test:** a
secure lens anchored on a **non-identity** type with class custody, driven through the real `handle()`
path, projects the note; a real `ShredRetentionClassKey` scrubs it to null; a second rebuild is idempotent;
and one un-listed-holder-type row does not stop any other row from updating. Negative test per rule:
absent/malformed `keyId`; `keyId` naming a holder with no key aspect; soft-deleted key aspect;
`retentionClass` custody declared with `Sensitive: false`; cross-package class reference; class-less
mutation; non-identity `egressReads`. Security plane → full 3-layer adversarial review.

### Fire 2 — the consumers (Lattice, M–L). One full-stack reset.

Old Increments 3 and 4 plus F3(b) plus the clinic FE surface. Collapsed because they share the custody
vocabulary, the identical aspect-split mechanic, and — decisively — **one delivery boundary**: each needs a
full-stack reset, so shipping them together costs one reset instead of two. Internal order:

1. **Clinic.** Split `.encounter` into the sensitive PHI aspect (`Sensitive: true`,
   `custody.kind: retentionClass`, class `clinicalRecord`) and a non-sensitive operational sibling;
   re-point the three shipped lenses at the sibling (§9.1); declare the retention class;
   `clinicEncountersRead` protected Secure Lens with `HolderTypes: ["retentionclass"]`; the §6.4 obligation
   written into the class description. **Move `patientDemographics.fullName` onto the identity** (F3(b)) —
   without it §6.4's acceptance criterion is unmet, since the patient's name would survive their erasure in
   plaintext. **Plus the FE surface** that renders the note to the treating provider, absorbed from the
   struck verticals row.
2. **Lease-signing.** Author the aspectType DDL that does not exist (§9.2 — `.profile` is written inline by
   the `leaseapp` vertexType script); three-way split of `.profile` (§9.1); the retained financial aspect
   takes an `underwritingRecord` retention class; the third-party identifiers (`guarantorName`,
   `coApplicantName`, `coApplicantContact`) move to their own identities (§8.7) or the fire states why not.

### Deferred tail — each behind a named trigger

(a) **Retained-class egress refs** — widen the envelope read path past `MATCH (i:identity)` and lift the
two identity-only holder refusals (the Processor's, at ref-authoring; the bridge's, at unwrap). The holder
resolution itself already switched in item 2, and the MAC needs no v2 — it covers `keyId` already. **One
thing this tail must also settle:** the identity-only rule lives in the Processor and the bridge, not at the
crypto boundary, so `vault.Service.handleDecryptRef` will serve a class-held record given a valid MAC. That
is unreachable today (the Processor refuses to mint that MAC), but it means the rule's enforcement points
move together or not at all. Trigger: the first package needing to egress a retained record. (b) **Period bucketing** — holder id derived from `(class, period)`, minted in-batch, constrained by
§7.3's *id-varies, type-fixed* rule. Trigger: a real statutory clock, or the first class approaching expiry.
(c) **Purpose-gated retained reveal** — `lattice.vault.decryptretained`, default-deny by natsperm (§6.5).
Trigger: an operator reveal or audit export of a retained record. (d) **Background-check result detail** — a
new sibling aspect under a retention class. Trigger: a package storing the detail (§9.3).

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

*Designer fire, 2026-08-06; ratified by Andrew the same day (see the block at the top). The Contract #1 §1.6
and Contract #3 §3.10 edits §5.1 proposed **are committed**, each carrying a transitional note that says the
runtime arrives with Fire 1 — so a present-tense clause never stands with nothing behind it. Building the
transitional clauses out is what discharges them.*

---

## 13. Fire 1 item 1 fire brief (build note, 2026-08-07)

**Scope cut, stated up front.** §11 scopes **Fire 1** as three coupled internal items (L–XL). This fire takes
**item 1 only** — the custody vocabulary + write path — and leaves items 2 and 3 to later fires behind a 🏗️
checkpoint (§14). Item 1 is the largest unit that ships green with its own provable acceptance criterion; item
2 (the five decrypt sites) turned out to be a security-plane trust change in its own right, needing a reorder
at two sites and a per-site divergence test that no existing fixture covers (see §13.6). Splitting there is a
seam, not timidity: nothing declares custody until Fire 2, so item 1 lands with zero live consumers either way.

### 13.1 Scope sentence (verbatim, §11 Fire 1 item 1)

> **Custody vocabulary + write path.** `CustodySpec` + `RetentionClassSpec` on `pkgmgr`, their reserved DDL
> aspects and `MetaVertexRef` fields, the four install-time validations, the `retentionclass` vertex type
> (**note the key segment is `retentionclass` — `[a-z][a-z0-9]*`, so no camelCase**) + `.retentionPolicy`,
> `pkgmgr.RetentionClassID`; step 6's conditional `sensitiveAspectScope` plus the class-less and
> budget-exhaustion closures; step 6.5's holder resolution and `ensureKeyHolderKey`.

### 13.2 Verified touch-list (every anchor re-checked live at `c340d127`)

| File:line | What |
|---|---|
| `internal/pkgmgr/definition.go:746-799` | `DDLSpec` — add `Custody CustodySpec` after `Sensitive` (:772) |
| `internal/pkgmgr/definition.go:123-226` | `Definition` — add `RetentionClasses []RetentionClassSpec` |
| `internal/pkgmgr/definition.go:35-46` | `validateAll`'s slice — append after `validateSensitiveClassScope` (:43) |
| `internal/pkgmgr/custodyscope.go` (NEW) | the four install-time validations |
| `internal/pkgmgr/installer.go:329-351` | `RetentionClassID`, mirroring `RoleID` (:339) / `LensID` (:349) |
| `internal/pkgmgr/installer.go:199-207`, `:271` | mint the class NanoIDs; thread them into `buildInstallBatch` |
| `internal/pkgmgr/build.go:52-57` | `buildInstallBatch` signature — add `retentionClassIDs []string` |
| `internal/pkgmgr/build.go:70-80` | after the Role loop: `vtx.retentionclass.<id>` + `.retentionPolicy` |
| `internal/pkgmgr/build.go:135-143` | after the `.sensitive` emit: the conditional `.custody` emit |
| `internal/processor/ddl_cache.go:24-58`, `:258-275` | `MetaVertexRef` custody fields + the `.custody` load |
| `internal/processor/step6_validate.go:176-195` | `sensitiveAspectScope` becomes conditional on the kind |
| `internal/processor/step6_validate.go:156` | the class-less closure |
| `internal/processor/step65_encrypt.go:59-73`, `:107-144` | holder resolution; `ensureIdentityKey` → `ensureKeyHolderKey` |
| `internal/processor/step6_resolve_ddl.go:261-269`, `:378` | surface the live-read fault so 6.5 can hard-error |

**Two design citations rotted and are corrected here.** Ledger #34's `definition.go:752-759` names the wrong
span — those are `Class`/`PermittedCommands`/`Description` field comments; the real emit seam is
`build.go:135-143`. Ledger #10's `refmac.go:24` is off by one in the other direction than the ratification
note assumed: `keyId` is appended at **:25**, `requestID` at :24 — the ratification correction is right.

### 13.3 Precedents to mirror

- **`Custody` as a reserved DDL aspect** → `DDLSpec.Sensitive` end to end: field `definition.go:765-772`,
  validator `sensitivescope.go:22-38`, conditional emit `build.go:135-143`, cache load `ddl_cache.go:258-275`.
  There is **no reserved-aspect registry** — this is the third hand-written instance of that pattern.
- **`CustodySpec` serialized into an aspect body** → `OpMetaSpec.Ceremony` (`definition.go:521`) with
  `opCeremonyBody` (`build.go:671-683`). `CustodySpec` is a **value**, not a pointer (its zero value is
  meaningful = `identity`), so the emit guard is `d.Custody.Kind != ""`, not a nil check.
- **The holder root + aspect** → the Role loop, `build.go:70-80` (`docVertex` :897, `docAspect` :904).
  **No `roleindex` analog**: class references are same-package-only, so nothing looks one up cross-package.
- **`RetentionClassID`** → `RoleID`/`LensID` verbatim, tag `"retention:"+canonicalName`.
- **Gotcha carried from the precedent:** `sensitivescope.go`'s empty-`Class` fallback is `opMetaClass`
  (`= "meta.ddl.vertexType"`, `build.go:23`), matching `buildInstallBatch`'s own default. Do **not** default
  an unset `Class` to `aspectType` in the new validator — that silently skips the check it should run.

### 13.4 Increment order + green checks

1. **pkgmgr vocabulary + install batch** — `CustodySpec`, `RetentionClassSpec`, `RetentionClassID`, the ID
   minting, the holder root + `.retentionPolicy`, the `.custody` emit.
   `go test ./internal/pkgmgr/`
2. **The four install-time validations** — `custodyscope.go` + the `validateAll` wiring.
   `go test ./internal/pkgmgr/`
3. **`MetaVertexRef` + the `.custody` cache load.** `go test ./internal/processor/ -run DDLCache`
4. **Step 6's conditional `sensitiveAspectScope`.** `go test ./internal/processor/ -run 'Step6|Validate'`
5. **Step 6.5's holder resolution + `ensureKeyHolderKey`.** `go test ./internal/processor/ -run Encrypt`
6. **The two fail-open closures** — budget-exhaustion hard error in 6.5; the class-less rejection.
   `go test ./internal/processor/`

Full bar: `go build ./...` · `make vet` · `golangci-lint run ./...` · `STRICT=1 go run ./scripts/lint-conventions.go`
· `go test ./internal/pkgmgr/ ./internal/processor/ ./internal/vault/` · `make verify-kernel`.

### 13.5 In-scope gotchas

- Vertex type segment is `[a-z][a-z0-9]*` (`keys/keys.go:141-155`) — the **key** segment is
  `retentionclass`; the declared **kind** string stays camelCase `"retentionClass"`. Two different strings.
- `Vault` needs **no interface change**: `identityKey` is an arbitrary AAD string (`local.go:203`, `:218`,
  `:238`), `Envelope.KeyID = identityKey` (:223), `Ciphertext.KeyID = envelope.KeyID` (:242). Step 6.5 may
  pass a `vtx.retentionclass.<id>` into the `identityKey` parameter slot today; the **rename** is item 2's.
- Step 6's own budget fail-open **stays** — `TestResolveGoverningDDL_LiveReadBudgetExhaustedFailsOpen`
  asserts it and must keep passing. Only **step 6.5** hard-errors, so the fault must be surfaced through a
  new fault-aware resolver variant rather than by changing `resolveGoverningDDL`'s behavior.
- Step 6.5 is gated on `cp.deps.Vault != nil && cp.deps.DDLs != nil` (`commit_path.go:378`).
- `TestEncryptSensitiveMutations_NonIdentityAnchor_PassesThrough` asserts the behavior this fire changes —
  **re-aim it, don't delete it**: a non-identity anchor with no custody declaration still passes through
  (step 6 rejected it upstream); one *with* `retentionClass` custody must now encrypt under the class holder.

### 13.6 Adjacent finds (filed or deliberately not, now)

- **No `ct.KeyID`/anchor divergence exists anywhere today** — checked, because item 2's whole trust switch
  rests on it. `MergeIdentity`'s `aspectConflictResolution` (`identity-hygiene/ddls.go:788-799`) reads the
  secondary's **decrypted plaintext** `data["value"]`, requires it be a non-empty *string*, and emits a fresh
  plaintext mutation on the primary, which step 6.5 re-encrypts under the primary's own key. It never copies
  a ciphertext between anchors. Not filed — a verified absence, recorded so item 2 need not re-derive it.
- **No test fixture at any of the five decrypt sites constructs a ciphertext whose `KeyID` differs from the
  independently-derived custody key.** So today's suites cannot distinguish "trusts `ct.KeyID`" from "trusts
  the anchor/column/ref" — they are always equal in every fixture. Not filed separately: it is item 2's own
  green bar, and §14's checkpoint names it as a required deliverable of that fire.
- **`privacy-base`'s `piiKey` DDL description still says "per-identity"** and its `permittedCommands` will
  need `ShredRetentionClassKey`. Deliberately **not** done here so privacy-base takes **one** version bump in
  item 3 rather than two; no package declares custody until Fire 2, so nothing mints a class-held `piiKey`
  in the interim. Named in §14 as an item-3 deliverable, not a free-floating residual.

### 13.7 Non-goals (the drift fence)

- **Item 2** — the five decrypt sites, the non-identity silent branch's deletion, the typed `egressReads`
  refusal, the `identityKey` → `keyHolderKey` rename.
- **Item 3** — `ShredRetentionClassKey`, `privacy.retentionClassKeyShredded`, the shred worker, the
  `retentionKeyStatus` lens, `SecureColumn.HolderTypes` replacing `IdentityKeyColumn`, the F2 null+alarm
  change, `secureIdentityKeyType`'s deletion, `RebuildRule`'s export, the destruction-event consumer, and the
  six shipped lens migrations.
- **Fire 2 entirely** — the clinic and lease-signing consumers, the aspect splits, `patientDemographics.fullName`.
- **The deferred tail** (§11) — retained-class egress refs, period bucketing, purpose-gated retained reveal.
- `lattice-architecture.md:1019` — planning-lead-owned (F3(a)), routed separately. Not touched.

## 14. Multi-fire checkpoint (live)

**FIRE 1 IS COMPLETE.** Items 1, 2, 3a, 3b-i and 3b-ii are all merged to `main`; the
`fire/retention-class-3b-ii` worktree has served its purpose and can be removed. `custodyscope.go` rule 5 is
lifted, so `retentionClass` custody is installable end to end: declared at install, written on the sensitive
path, resolved at every decrypt site, destroyed by `ShredRetentionClassKey`, and delivered to the read models
with a `projectionsRebuilt` attestation that only fires when every lens declaring the destroyed holder type
has provably drained a rebuild.

Item 3b-ii took three rounds of adversarial review to land, each closing the crop the previous one
introduced (§19 → §21, §22 → §23, §24 → §24.7). Do not re-litigate any of them; the closed findings and the
things confirmed sound are recorded there.

**FIRE 2 IS IN FLIGHT. Item 1 (Clinic) Inc A is merged (`ae9c6411`); the live checkpoint is §26.4.**
Fire 1 built the mechanism and deliberately shipped no user of it (§13.7). Clinic `.encounter` is now a
retained record under a `clinicalRecord` holder; lease-signing's income `.profile` (Fire 2 item 2) is still
plaintext, and `patientDemographics.fullName` still survives its subject's erasure (Inc C).
**Landing shape: each increment lands on `main`** — safe because nothing reads `.encounter` until Inc B adds
the first lens, so no boundary leaves a half-wired read path. There is therefore **no persistent worktree to
resume into**: Inc A's is removed, and the next fire opens a fresh one from `main`.

**Done — item 1 (2026-08-07).** Custody vocabulary + write path. `CustodySpec`/`RetentionClassSpec`, the
`retentionclass` holder + `.retentionPolicy`, `RetentionClassID`/`RetentionClassKey`, six install
validations, the `.custody` aspect carrying the RESOLVED holder key, `MetaVertexRef.CustodyKind`/
`CustodyHolderKey`, step 6's conditional anchoring rule, step 6.5's holder resolution +
`ensureKeyHolderKey`, and the shared-live-read-budget fail-open (step 6.5 now errors on an empty
resolution that followed a fault; step 6 keeps its permissive fail-open unchanged).

Adversarial review then closed four more fail-opens the first pass introduced or inherited: a sensitive
aspect with no resolvable holder is an error rather than a silent plaintext commit; step 6.5's kind gate
now matches step 6's, so neither acts on a mutation the other never checked; an unrecognized custody kind
no longer falls through to the identity derivation; and a malformed `.custody` body poisons the class
(loaded, sensitive, unwritable) rather than degrading to identity custody or vanishing the DDL — both of
which fail open, in opposite directions. A tombstoned `.custody` now reads as absent, so revoking a
declaration revokes it.

**`retentionClass` custody is REFUSED AT INSTALL** until item 2 lands (`pkgmgr/custodyscope.go`, rule 5).
The write path custodies on the class while every decrypt site still resolves the holder from the anchor,
so a record written today would be write-only. Item 2's first act is deleting that gate.

**Deliberately NOT in item 1:** the class-less-mutation closure. §4.1 scoped it "after a census confirming
no shipped script relies on it"; the census found two that do — identity-domain's `make_update`
(`UpdateIdentityState`, called by the seed scripts) and the kernel's `meta_ddl.go` `make_update`. Filed as
a lattice row with both prerequisites named.

**Done — item 2 (2026-08-08).** The read path. All five decrypt sites resolve the holder through
`vault.KeyHolder(ct)`; the non-identity silent branch is deleted; the egress arm gains a typed refusal
naming the holder type, mirrored at the bridge; the `Vault` parameter is `keyHolderKey` at all 7 methods
plus `OpenWithSessionKey`, with every doc comment re-derived (the RPC structs' Go fields rename to
`KeyHolderKey`, their `json:"identityKey"` tags deliberately frozen).

Two guards moved rather than vanished. `LocalBackend` labels a ciphertext with the holder its DEK was
actually derived under (not `envelope.KeyID`, which nothing validates), and refuses a labelled ciphertext
decrypted under a different holder — an unlabelled one is exempt, because `objectcrypto` drops the label by
design. That check is what keeps the invariant structural instead of resident in five call sites.

**The install gate is RE-AIMED, not lifted** (§15.5). Its original cause — a class-custodied record is
write-only — dies here, but `ShredRetentionClassKey` is item 3, so lifting it now would license retained
PHI under an undestroyable DEK. Item 3 lifts it. **`HolderTypes` (§6.1 fail-closed rule 2) is also item 3**,
so a secure column currently accepts any well-formed holder; §6.1's AAD argument bounds that, but the
enumeration key §6.3 needs does not exist yet.

Adversarial review (opus, security plane) returned no blocker: the AAD and DEK-selection strings never
diverge; a submitter cannot influence `keyId` on the write path (`step65_encrypt.go`'s holder comes only
from the installed DDL); `IdentityKeyColumn` was never an RLS input, so decoupling custody from it cannot
move a row into another actor's visibility; the MAC covers the field the holder is read from and is verified
before any decrypt. Availability improved — the old terminal condition was reachable by a lens author
declaring a column their cypher never RETURNs, and that class is gone. Two residuals it named are filed as
rows; one caveat is recorded below.

**Caveat, recorded so a later fire does not over-read the claim.** "keyId is self-authenticating" covers the
aspect-ciphertext path only. The object-CEK path (`objectcrypto.EncodeWrappedCEK`) deliberately drops the
label and supplies its holder out of band, so `WrapKey`/`UnwrapKey` still take an externally chosen holder.

**Then — item 2 detail, for reference.** Switch all five decrypt sites to `ct.KeyID`
(`processor/sensitive_decrypt.go:162`+`:217`, `refractor/pipeline/secure.go:130`+`:137`,
`vault/service.go:478`+`:499`, `bridge/egress.go:174`+`:183`, `cmd/loupe/vault.go:148`+`:176`); delete the
non-identity silent branch that today hands raw ciphertext to a script (`sensitive_decrypt.go:163-171`); add
the typed non-identity refusal under `egressReads`; rename the `Vault` interface's `identityKey` parameter to
`keyHolderKey` at all 7 methods + `local.go`'s 8 sites. **Two sites need a reorder, not a substitution:**
`sensitive_decrypt.go` parses the ciphertext at `:221`, *after* the anchor gate at `:162`, and
`cmd/loupe/vault.go` computes its custody handle at `:148`, *before* it fetches the aspect at `:158-172` — in
both, the guard must move after the ciphertext is in hand, which changes failure ordering. **Required green
bar:** a per-site test constructing a `ct.KeyID` that *diverges* from the anchor/lens column — no existing
fixture does, so today's suites cannot tell the switch took effect. Security plane → full 3-layer adversarial
review. `SecureColumn.IdentityKeyColumn` stays required-but-unread through item 2 (item 3 removes it), so the
six shipped lenses need no migration yet — but `TestSecureDecryptor_MissingIdentityKeyIsTerminal` must be
re-aimed when the decryptor stops reading the column.

**Done — item 3a (2026-08-08).** Erasure vocabulary + destruction. `ShredRetentionClassKey` +
`RecordRetentionClassShredFinalization` on a new `shredRetentionClassKey` DDL,
`privacy.retentionClassKeyShredded`, `piiKey`'s `permittedCommands` + a re-derived description covering
both holder kinds, a second durable consumer in `internal/privacyworker` (its own durable, so one kind's
stuck destruction cannot park the other's), and the `retentionKeyStatus` lens. privacy-base 0.11.0 →
0.12.0.

Three deliberate divergences from the mirror, each with its reason in the commit: **no `subjectKey` alias**
(no pattern binds this op, so an alias would be vocabulary with no submitter); **a separate finalization
verb** (`RecordShredFinalization` validates a `vtx.identity` subject and its steps are the identity plane's
— `projectionsNullified` keys on a row's own identity column, which a class-custodied row does not have);
**no grant** (this is the widest-blast-radius verb in the package — it destroys every record a class holds,
for subjects who never asked for anything — so `operator`/scope:any by default would make it the most
casually reachable one; the finalization sibling IS granted, because recording a committed destruction
confers no authority to start one).

**The install gate STILL stays shut**, and its remaining cause is now the last one: a class-key destruction
does not reach the read models. §6.3's derivation is why — an identity-custodied row scrubs in-band because
the key aspect hangs off the vertex the lens already binds, and a class holder is not the ciphertext's host,
so nothing forces a lens to react. Lifting the gate here would license retained PHI whose erasure no
projection would honor, which is strictly worse than today's un-projected plaintext. Item 3b lifts it.

**Done — item 3b-i (2026-08-08).** The declaration. `SecureColumn.HolderTypes` replaces
`IdentityKeyColumn` at all five sites, required non-empty and each entry a Contract #1 type segment
(`keys.IsValidTypeSegment`, newly exported alongside `IsValidLocalName` for the same reason). It is enforced
on arrival rather than left inert: a ciphertext whose `vault.KeyHolderType` is not in the column's list is
refused as a `failure.Terminal`, which no live corpus can reach — rule 5 has never let a non-identity DEK be
minted, and all six migrated lenses declare `["identity"]`. `secureIdentityKeyType` is gone, its narrowing
conjunct now looping the declared holder types. `secureAliasNames` drops the alias it no longer consumes;
`secureColumnsEqual` compares field-wise plus `slices.Equal`. Seven package version bumps — six for the
migration, `edge-manifest` because `lint-package-version` demands one of any `pkgmgr` change reaching a
generated read-grant producer lens.

Adversarial review (opus security plane + edge-case + acceptance) closed two fail-opens the first pass left.
An empty `HolderTypes` satisfied the re-aimed conjunct **vacuously** — the loop body never runs — so
`NewSecureDecryptor` now refuses one rather than delegating the invariant to a validator three packages
away, and the positive narrowing test that had been passing on an empty decryptor now carries columns. It
also caught the re-aimed comment **overclaiming**: reaching that conjunct needs an `actorEnumerator`, which
a secure lens can never have (`secureColumns` is refused on any non-empty `projectionKind`), so every
shipped secure lens takes the plain branch, which carries no holder-type conjunct at all. The comment now
says so, and names the install gate as what actually contains the exposure meanwhile.

The wire field renames, so a spec installed from an older package version carries `identityKeyColumn` and no
`holderTypes`, fails the required-non-empty rule, and the lens **refuses to load** rather than loading with
an allow-list matching nothing — verified through the real parse path, and surfaced by
`health/registry_probe.go`'s declared-but-not-registered reconciliation. The diff-apply is the migration:
reinstall, then cycle.

**Then — item 3b-ii, the delivery half.** Per-row failure projects `null` + a privacy-tier alarm (F2 — note
`failure.CatPrivacyCritical` exists and is classified but has **no** generic routing in `dispositionEvalErr`,
so this fire either extends that routing or reuses `keyshredded`'s manual pattern: `failure.PrivacyCritical`
decorates the log line while the pause is a separate explicit `Control.PauseRule` call);
**`control.Service.RebuildRule` exported** (serialized per lens — `rebuildRule` at `control/service.go:870-883`
spawns a goroutine per request with no cross-caller lock, `Pipeline.Rebuild` stores `rebuildInFlight`
unconditionally rather than by CAS, and there are already two unserialized callers, the control RPC and
`cmd/refractor/reload.go:372-378`); the destruction-event consumer with the `HolderTypes` enumeration and the
`projectionsRebuilt` attestation — which also **lifts the `FailedPrecondition: step projectionsRebuilt has no
producer yet` guard** already shipped in `shred_retention_class_key.go`, and needs a per-lens holder-type
enumeration over `cmd/refractor`'s registry mirroring `capReadShredTargets`;
`keyshredded/manager.go:31-37`'s excuse and `pipeline/sweep.go:29-33`'s stale reason re-derived;
**and lifting `custodyscope.go` rule 5.**

**Superseded — item 3 as one unit.** `ShredRetentionClassKey` mirroring
`shred_identity_key.go:267-311` (**that slice only** — the erasure design keeps the rest unchanged);
`privacy.retentionClassKeyShredded`; the retention-class shred worker; the `retentionKeyStatus` lens;
`SecureColumn.HolderTypes` replacing `IdentityKeyColumn` with validation; per-row failure projects `null` +
a privacy-tier alarm (F2); `secureIdentityKeyType` deleted; `control.Service.RebuildRule` exported
(serialized per lens); the destruction-event consumer with its `projectionsRebuilt` attestation; the six
shipped lens migrations; `keyshredded/manager.go:31-37`'s excuse and `pipeline/sweep.go:29-33`'s stale
reason re-derived. **Plus the one package edit deferred out of item 1:** `privacy-base`'s `piiKey` DDL
description ("per-identity" → "a key holder's DEK envelope") and its `permittedCommands` gaining
`ShredRetentionClassKey` — bundled here so privacy-base takes one version bump, not two.

## 15. Fire 1 item 2 fire brief (build note, 2026-08-07)

### 15.1 Scope sentence (verbatim, §11 Fire 1 item 2)

> **The read path switches to `ct.KeyID`** — all five sites, the non-identity silent branch deleted, the
> typed refusal for a non-identity holder under `egressReads`; the `Vault` interface parameter rename
> `identityKey` → `keyHolderKey` with every doc comment re-derived.

### 15.2 Verified touch-list (every anchor re-checked live at `1d5ca961`)

| File:line | What |
|---|---|
| `internal/processor/sensitive_decrypt.go:162-172` | delete the non-identity silent branch |
| `internal/processor/sensitive_decrypt.go:173-204` | egress arm — the typed non-identity refusal |
| `internal/processor/sensitive_decrypt.go:217-225` | reorder: parse ct **before** the envelope read; holder ← `ct.KeyID` |
| `internal/refractor/pipeline/secure.go:130-137` | holder ← `ct.KeyID` (ct already parsed at `:125`, no reorder) |
| `internal/refractor/pipeline/secure.go:153` | `Decrypt` under the resolved holder |
| `internal/vault/service.go:478`, `:499` | `DecryptRef` — holder ← `in.Ciphertext.KeyID`; `in.Ref` keeps its shape check |
| `internal/bridge/egress.go:174-183`, `:201` | holder ← `marker.Ciphertext.KeyID`; typed refusal naming deferred tail (a) |
| `cmd/loupe/vault.go:147-149`, `:170`, `:176`, `:193` | reorder: fetch aspect → parse ct → holder ← `ct.KeyID` → fetch `<holder>.piiKey` |
| `internal/vault/vault.go:63`, `:75`–`:132` | 7 interface methods + `OpenWithSessionKey` — param rename + doc comments |
| `internal/vault/local.go:52-53`, `:203-296`, `:373-401` | impl params, AAD sites, cache-map comments |
| `internal/vault/vaultwire/vaultwire.go:91-105` | `OpenWithSessionKey` param + doc |
| `internal/pkgmgr/custodyscope.go` rule 5 | **re-aim, do not delete** — see §15.5 |

**Two design citations corrected.** §14 lists `bridge/egress.go` and `vault/service.go` under "switch to
`ct.KeyID`" while the deferred tail (a) says the *egress* sites switch later. Both are right at different
granularities and the brief resolves it: **the holder resolution switches at all five sites now**; what tail
(a) defers is *widening the bridge's envelope source* past the identity-only `piiKeyEnvelope` lens
(`privacy-base/lenses.go`). Until that lands a non-identity holder is refused at egress — typed, at the mint
point and again at the bridge — rather than failing as an unexplained missing envelope. Second: `secure.go`
needs **no** reorder (§14 named two reorder sites; only `sensitive_decrypt.go` and `cmd/loupe/vault.go`
qualify — `secure.go` already parses the ciphertext at `:125`, ahead of the column read at `:130`).

### 15.3 Precedents to mirror

- **The uniform shape** → §6.1's four lines. `substrate.ClassifyKey(holder) == KindVertex` is the
  well-formedness gate (`substrate/keys.go:56`); `substrate.ParseVertexKey` (`:59`) yields the type segment
  the egress refusals test.
- **Fail-closed classification** → `secure.go:199-211`'s soft-deleted-piiKey treatment: absent and
  tombstoned are the same answer, and neither falls back.
- **Typed permanent failure at the bridge** → `permanentEgressFailure` (`egress.go:175-178`), already the
  shape for a malformed ref.
- **Why the switch is safe** → `local.go`'s AAD binding (`:238`, `:252`) against the envelope minted with
  the same string (`:217`, `:223`): a substituted `keyId` fails the GCM tag. `ct.KeyID` self-authenticates.

### 15.4 Increment order + green checks

1. **`Vault` param rename + doc comments** (mechanical, no behavior). `go test ./internal/vault/`
2. **`sensitive_decrypt.go`** — delete the silent branch, reorder, holder ← `ct.KeyID`, egress refusal.
   `go test ./internal/processor/`
3. **`secure.go`** — holder ← `ct.KeyID`; re-aim `TestSecureDecryptor_MissingIdentityKeyIsTerminal`.
   `go test ./internal/refractor/...`
4. **`vault/service.go` + `bridge/egress.go` + `cmd/loupe/vault.go`.**
   `go test ./internal/vault/ ./internal/bridge/ ./cmd/loupe/`
5. **The divergence bar** — a per-site test whose `ct.KeyID` differs from the anchor / lens column, plus the
   negative vectors (absent `keyId`, malformed `keyId`, holder with no `piiKey`, tombstoned key aspect).

Full bar: `go build ./...` · `make vet` · `golangci-lint run ./...` ·
`STRICT=1 go run ./scripts/lint-conventions.go` · `STRICT=1 go run ./scripts/lint-board.go` ·
`go test ./internal/vault/ ./internal/processor/ ./internal/refractor/... ./internal/bridge/ ./internal/pkgmgr/ ./cmd/loupe/` ·
`make verify-kernel`.

### 15.5 In-scope gotchas

- **The install gate is RE-AIMED, not deleted** (the one deliberate divergence from §14's plan, decided
  here). §14 said "item 2's first act is deleting that gate," reasoning that the gate's stated cause — a
  class-custodied record is *write-only* — is exactly what item 2 removes. That cause does die. But a second
  one is born the moment the record becomes readable: **item 3 owns `ShredRetentionClassKey`**, so between
  item 2 and item 3 a package could custody retained PHI on a class DEK that **no verb can ever destroy** —
  a retention class whose whole purpose is a destroyable key. Lifting the gate on the death of its *first*
  reason would ship the hazard the gate exists for. The gate's message and comment are re-derived to name
  the remaining cause; item 3 lifts it. No consumer is delayed: nothing declares custody until Fire 2, which
  follows item 3.
- **`ct.KeyID` is not available where `v == nil`.** With no Vault wired, step 6.5 never encrypted, so the
  aspect body is plaintext and carries no `keyId`. The non-egress arm already returns early on `v == nil`
  (`:214`); the **egress** arm must keep authoring its unauthenticated marker in that deployment
  (`TestEgressReads_SensitiveKey_HydratesAsRef` has no Vault) — so the refusal is gated on a *parseable*
  ciphertext, and an unparseable body stays an error only where it already was (`v != nil`, `:189-192`).
- **`SecureColumn.IdentityKeyColumn` stays declared-and-required, and becomes unread.** Its removal plus
  `HolderTypes` is item 3, so the six shipped lenses need no migration in this fire.
  `TestSecureDecryptor_MissingIdentityKeyIsTerminal` asserts the behavior this fire deletes — **re-aim it**
  to the new terminal condition (a ciphertext whose `keyId` is absent or malformed), don't delete it.
- **`HolderTypes` (§6.1 fail-closed rule 2) is item 3.** In this fire a column accepts any well-formed
  holder. That is not a confidentiality widening — §6.1's AAD argument is what bounds it — but it does mean
  the *enumeration* key §6.3 needs does not exist yet. Stated so item 3 does not read this fire as
  already-covered.
- **RPC wire structs keep their JSON tags.** `DecryptRequest`/`WrapKeyRequest`/`UnwrapKeyRequest`/
  `IssueSessionKeyRequest` rename their **Go** fields to `KeyHolderKey` (they now carry a
  `vtx.retentionclass.*` on the loupe path, so the old name would be a vestigial trap) while the
  `json:"identityKey"` tags stay frozen — renaming a live NATS RPC field is a wire change with no bearing on
  this fire's guarantee, and the scope-diff gate is narrow-only.
- **The MAC is unaffected.** `RefMACInput(ref, requestID, ct)` covers the whole ciphertext including its
  `keyId`, so switching the *holder* source needs no MAC v2 (§8.9 / tail (a) already say so).

### 15.6 Adjacent finds (filed or deliberately not, now)

- **`secureIdentityKeyType` (`pipeline.go`) survives this fire.** §6.1 lists its deletion, §11 places it in
  item 3, and it is consulted only on a path a Secure Lens structurally cannot take (ledger #20). Not filed
  — it is a named item-3 deliverable, recorded here so item 3 does not treat it as done.
- **The uncommitted Contract #1 §1.6 transitional note narrows again with this fire** and is updated in
  `main`, still uncommitted, as part of the same standing proposal — not a new one.

### 15.7 Non-goals (the drift fence)

- **Item 3** — everything §14 lists under it, unchanged.
- **Fire 2** — the clinic and lease-signing consumers.
- **Deferred tail (a)** — widening `piiKeyEnvelope` past `MATCH (i:identity)` so a retained record can
  egress. This fire *refuses* that case; it does not enable it.
- The `json:"identityKey"` wire tags (§15.5).

## 16. Fire 1 item 3a fire brief (build note, 2026-08-08)

**Item 3 splits in two, at a green boundary.** §14's item 3 lists twelve deliverables spanning two
different planes: a **destruction** half (an op, an event, an async key-destroyer, an operator lens) and a
**delivery** half (`HolderTypes`, per-row `null`+alarm, the exported rebuild, the Refractor destruction
consumer, six lens migrations). The destruction half is provable and green with zero consumers and changes
no shipped behavior; the delivery half rewrites the failure posture of six live secure lenses. This fire
builds **3a, the destruction half**; **3b** is the delivery half and lifts the install gate. The split is a
narrowing of §14's item 3, not a re-scoping: every deliverable stays, in the same order §14 gives them, and
the gate that makes the whole thing safe (`custodyscope.go` rule 5) stays shut across the boundary — so at
no point does a package custody retained PHI on a DEK whose destruction cannot reach a read model.

### 16.1 Scope sentence (narrowed from §11 Fire 1 item 3 / §14)

> **Erasure vocabulary + destruction.** `ShredRetentionClassKey` + `privacy.retentionClassKeyShredded` +
> `piiKey`'s `permittedCommands`; the retention-class shred worker; the `retentionKeyStatus` lens. **Plus
> the one package edit deferred out of item 1:** `privacy-base`'s `piiKey` DDL description ("per-identity" →
> "a key holder's DEK envelope") and its `permittedCommands` gaining the new verbs — bundled here so
> privacy-base takes one version bump, not two.

**Green bar (the acceptance criterion, provable with zero lenses and zero installed classes):** seed a live
`vtx.retentionclass.<NanoID>` holder; `ShredRetentionClassKey` marks `<holder>.piiKey` `shredded=true`
(writing the durable empty-`wrappedDEK` placeholder when the class never received a sensitive write) and
emits `privacy.retentionClassKeyShredded`; the worker consumes it, calls `Vault.ShredKey(holderKey)`, and
records `vaultKeyDestroyed`; a subsequent `Decrypt` under that holder fails `ErrKeyShredded`. Negative
vectors: an absent/tombstoned holder; a holder key of the wrong vertex type; a finalization with no prior
shred; a finalization naming an unknown step.

### 16.2 Verified touch-list (every anchor re-checked live at `9a3ffb9e`)

**Create** — `packages/privacy-base/shred_retention_class_key.go` (+ `_test.go`).

| Site | Anchor | Edit |
|---|---|---|
| `packages/privacy-base/ddls.go:46-95` | `piiKey` DDL; `PermittedCommands` :51; description :52-60 | add both verbs; re-derive "per-identity" wording |
| `packages/privacy-base/package.go:41-50` | `Version: "0.11.0"` :43 | bump; register the two new DDLs |
| `packages/privacy-base/lenses.go:11,63-71` | `ShredStatusBucket` :11; `Lenses()` :63 | new bucket const + `retentionKeyStatus` entry |
| `internal/privacyworker/manager.go:46-61,118-126` | consts; `Run` drives ONE consumer | second durable consumer, same `Config` |
| `cmd/processor/main.go:251-264` | `privacyworker.New` wiring | run the second consumer |
| `docs/contracts/01-addressing-and-envelope.md:180` | transitional note | narrow again — superseded: the transitional was deleted outright at ratification (`79d984a5`), the gate having lifted |

### 16.3 Precedents to mirror

- **The op + event DDL** → `shred_identity_key.go` — the `ShredIdentityKey` arm (`:200-243`), the
  `RecordShredFinalization` arm (`:245-278`, the slice §14 names), and `KeyShreddedEventDDL()`
  (`:283-313`). **That file only**, per the ratification banner's build-hygiene caution — the erasure design
  keeps the rest of the spine unchanged.
- **The durable placeholder** → `shred_identity_key.go:228-235`. Load-bearing for the identical reason:
  `LocalBackend.shredded` is in-memory (`vault/local.go:53`), so a skipped mutation would let a post-restart
  sensitive write mint a fresh, unshredded class DEK.
- **The worker** → `internal/privacyworker/manager.go:132-221` — same publish-then-Ack cascade idiom, same
  `substrate.DeriveNanoID` requestId keyed on the triggering event's sequence, same `NakWithDelay` on a
  failed `ShredKey` (a shred is never silently dropped).
- **The lens** → `lenses.go:109-123` (`shredStatusSpec`), the null-safe `node.<aspect>.data.<field>` form so
  a not-yet-recorded step projects null rather than failing.
- **Permissions** → `permissions.go`: `ShredRetentionClassKey` ships **NO grant**, exactly as
  `ShredIdentityKey` does — destroying a retention class is the data controller's decision, and its grant
  posture belongs to the deployment. The finalization verb takes the operator/scope:any grant
  `RecordShredFinalization` carries, for the same reason (the submitter is a service actor, and grants
  attach to roles).

### 16.4 Increment order + green checks

1. **Op + event DDL + piiKey admission + version bump.**
   `go test ./packages/privacy-base/ -run 'RetentionClass'`
2. **The worker's second consumer + `cmd/processor` wiring.**
   `go test ./internal/privacyworker/`
3. **The `retentionKeyStatus` lens.**
   `go test ./packages/privacy-base/ -run 'Lens|Cypher'`
4. **Gates.** `go build ./...` · `make vet` · `golangci-lint run ./...` ·
   `STRICT=1 go run ./scripts/lint-conventions.go` · `go test ./packages/privacy-base/ ./internal/privacyworker/ ./internal/pkgmgr/ ./internal/processor/`

### 16.5 In-scope gotchas

- **`packages/privacy-base` is RED at clean `main` under package-level load, and it is not this fire.**
  `TestPurgeIdentityDedupFootprint_{WideSubject,DuplicateOfOnly}_ConvergesPastOnePage` fail
  `outcome mismatch: got "rejected" want "accepted"` when the whole package runs, pass in isolation, and
  pass whole-package under `PROCESSOR_SCRIPT_WALL_MS=5000` — measured three ways on a quiet host at
  `9a3ffb9e` in a clean worktree. That is the filed row *"[privacy-base] The erasure walk's real ceiling is
  wall time"* reproducing locally: the 250ms Starlark wall binds before the walk's own named stop. CI sets
  the 5s wall, which is why CI is green. **Run this package's gate with `PROCESSOR_SCRIPT_WALL_MS=5000`**
  and do not read those two failures as this fire's.
- **A retention class cannot be installed, so the tests seed the holder directly.** `custodyscope.go` rule 5
  still refuses `retentionClass` custody at install and this fire does **not** lift it (3b does). The op
  requires only a live holder vertex, so the acceptance criterion is provable by seeding
  `vtx.retentionclass.<NanoID>` — which is what keeps 3a green with zero consumers.
- **The contract note's stated reason goes stale the moment this fire lands.** Contract #1 §1.6's
  transitional note currently says `retentionClass` is refused at install "for one remaining reason:
  `ShredRetentionClassKey` does not exist." After 3a it exists and the gate is still shut — for the
  *delivery* reason. The note is re-derived in `main`, **still uncommitted**, as part of the same standing
  proposal, not a new one.
- **The holder's key aspect is `<holderKey>.piiKey`, type-agnostic already** — step 6.5's
  `ensureKeyHolderKey` (`step65_encrypt.go:178-216`) keys off whatever holder `keyHolderFor` resolved, so
  the class path needs no new aspect shape.
- **`privacy-base` has no `verify-package-*` target** (already a filed row). Not added here.

### 16.6 Adjacent finds

- **`internal/refractor/failure`'s `CatPrivacyCritical` has no generic pipeline routing** — it is defined
  and classified (`classify.go:30-35`, `:115-124`) but `dispositionEvalErr` (`pipeline.go:2115-2137`) never
  branches on it; only `keyshredded/manager.go` acts on the tier, by hand. Not filed: 3b's per-row
  `null`+alarm is precisely the fire that must either extend that routing or reuse the manual pattern, and
  it is named here so 3b does not read the tier as already wired.
- **`secureColumnsEqual` (`cmd/refractor/main.go:1366-1376`) compares `lens.SecureColumn` with `!=`**, so
  adding `HolderTypes []string` breaks the hot-reload refusal path (`reload.go:84`) at compile time. Not
  filed — it is a 3b touch-list entry, recorded so 3b does not discover it mid-build.
- **Three parallel `SecureColumn` definitions** (`pipeline/secure.go:33`, `lens/corekv_source.go:283`,
  `pkgmgr/definition.go:1092`) with two independent validation mirrors (`corekv_source.go:838-903`,
  `bucketguard.go:106-190`). Not filed — 3b must change all five together, recorded here as its map.

### 16.7 Non-goals (the drift fence)

- **3b** — `SecureColumn.HolderTypes` replacing `IdentityKeyColumn` + its validation; per-row custody
  failure projecting `null` + the privacy-tier alarm (F2); `secureIdentityKeyType`'s deletion;
  `control.Service.RebuildRule` exported + serialized per lens; the Refractor destruction-event consumer
  with the `HolderTypes` enumeration and the `projectionsRebuilt` attestation; the six shipped lens
  migrations; `keyshredded/manager.go:31-37`'s excuse and `pipeline/sweep.go:29-33`'s stale reason
  re-derived; **lifting `custodyscope.go` rule 5.**
- **Fire 2** — the clinic and lease-signing consumers.
- **Deferred tail (a)** — widening `piiKeyEnvelope` past `MATCH (i:identity)`.
- The two `PurgeIdentityDedupFootprint` wall-clock failures (§16.5) — an already-filed row, not this fire.

## 17. Fire 1 item 3b-i fire brief (build note, 2026-08-08)

**Item 3b splits in two, at the same kind of green boundary 3a used.** §14's item 3b lists eight
deliverables. Four of them are the **declaration** — `HolderTypes` replacing `IdentityKeyColumn` across the
five sites, `secureIdentityKeyType`'s deletion, the six shipped lens migrations, and the `secureColumnsEqual`
slice-compare the field forces — and they change no shipped projection behavior, because item 2 already made
the decryptor resolve custody from `ct.KeyID` and left `IdentityKeyColumn` required-but-unread
(`secure.go:25-32`, proven by `TestSecureDecryptor_AbsentIdentityKeyColumnStillDecrypts`). The other four are
the **delivery** — F2's per-row `null`+alarm, the exported serialized `RebuildRule`, the destruction-event
consumer with its `projectionsRebuilt` attestation, and the two stale rationales — and they rewrite the
failure posture of six live secure lenses. This fire builds **3b-i, the declaration**; **3b-ii** is delivery
and lifts the install gate. Narrowing, not re-scoping: every §14 deliverable survives, in §14's order, and
`custodyscope.go` rule 5 stays shut across the boundary.

### 17.1 Scope sentence (narrowed from §14 item 3b)

> **`SecureColumn.HolderTypes` replaces `IdentityKeyColumn`** at all five sites
> (`pipeline/secure.go`, `lens/corekv_source.go`, `pkgmgr/definition.go` + the two validation mirrors
> `corekv_source.go` and `bucketguard.go`), required non-empty and enforced per-row at decrypt time;
> `secureIdentityKeyType` deleted, its narrowing conjunct re-aimed at the declared holder types; the six
> shipped lens migrations (**not uniform** — lease-signing's is `IdentityKeyColumn: "applicant"`, three
> entries); `secureColumnsEqual`'s slice compare.

**Enforcement lands with the declaration, not after it.** §14 leaves `HolderTypes` inert until the delivery
half; that would ship a declared field no code reads, which this codebase forbids. It is instead enforced
here — a ciphertext whose `vault.KeyHolderType` is not in the column's `HolderTypes` is refused — and the
refusal is a `failure.Terminal`, matching every neighbouring custody refusal in `decryptColumn` today.
**This cannot fire in any live corpus:** rule 5 refuses `retentionClass` custody at install, so step 6.5 has
never minted a non-identity DEK, so every ciphertext in existence is identity-held and every migrated lens
declares `["identity"]`. 3b-ii changes *how* that refusal fails (F2: `null` + alarm, per-row), not *whether*
— which is the correct order, since converting a refusal that does not exist yet is not a boundary.

### 17.2 Verified touch-list (every anchor re-checked live at `c120a7d0`)

| Site | Anchor | Edit |
|---|---|---|
| `internal/refractor/pipeline/secure.go:16-37` | `SecureColumn`; doc :25-32 | `HolderTypes []string \`json:"holderTypes"\``; re-derive the doc |
| `internal/refractor/pipeline/secure.go:119-145` | `decryptColumn`, after `vault.KeyHolder` :142 | Terminal unless `KeyHolderType` ∈ `col.HolderTypes` |
| `internal/refractor/pipeline/secure.go:92-104` | `Apply`'s failure-posture doc | add the unlisted-holder row |
| `internal/refractor/pipeline/pipeline.go:781-794`, `:851-857` | `secureIdentityKeyType` const + its conjunct | delete the const; conjunct loops the decryptor's declared holder types |
| `internal/refractor/lens/corekv_source.go:283-287`, `:877-900` | wire struct + `validateSecureColumns` | field swap; required-non-empty + per-entry type-segment validity |
| `internal/pkgmgr/definition.go:1088-1096` | `pkgmgr.SecureColumn` | field swap + doc |
| `internal/pkgmgr/bucketguard.go:155-189` | the validation mirror | same two rules, same wording shape |
| `internal/pkgmgr/build.go:471-483` | `targetConfig["secureColumns"]` marshal | `"identityKeyColumn"` → `"holderTypes"` |
| `cmd/refractor/main.go:1025-1029` | `lens.SecureColumn` → `pipeline.SecureColumn` | carry `HolderTypes` (copy the slice) |
| `cmd/refractor/main.go:1378-1396` | `secureAliasNames` | drop `IdentityKeyColumn` from the alias set |
| `cmd/refractor/main.go:1364-1376` | `secureColumnsEqual` | `!=` → field-wise + `slices.Equal` |
| `packages/clinic-domain/lenses.go:312-315` | `clinicPatientsRead`, 2 entries | `HolderTypes: []string{"identity"}` + version bump |
| `packages/identity-domain/lenses.go:66-68` | `identityCredentialsRead`, 1 | same |
| `packages/loftspace-domain/lenses.go:81-83` | `applicantRosterRead`, 1 | same |
| `packages/lease-signing/lenses.go:274-278` | `landlordLeaseApplicationsRead`, 3, col `applicant` | same |
| `packages/wellness-domain/lenses.go:137-139` | `wellnessIdentitiesRead`, 1 | same |
| `packages/cafe-domain/lenses.go:132-134` | `cafeIdentitiesRead`, 1 | same |

### 17.3 Precedents to mirror

- **The validation wording + fail-closed posture** → the `IdentityKeyColumn` rules being replaced
  (`corekv_source.go:877-879` required; `bucketguard.go:162-165` its mirror). The two mirrors must stay
  textually parallel — they already are, and a builder editing one and not the other is the standing hazard.
- **A type-segment validity check** → the same rule `pkgmgr` already applies to a vertex type elsewhere;
  `[a-z][a-z0-9]*` per `keys.go:141-155`, the convention §12.1 already corrected this design on.
- **The per-row custody refusal** → `decryptColumn`'s existing neighbours (`secure.go:144`, `:153-154`,
  `:172`): a `failure.Terminal` naming the column, the holder, and what was expected.
- **A slice-bearing equality helper** → `slices.Equal` on the field, the ordinary Go form; the surrounding
  comment already says the comparison is order-sensitive because the spec is authored, which stays true.

### 17.4 Increment order + green checks

1. **The three structs + both validation mirrors + the marshal.**
   `go test ./internal/refractor/lens/ ./internal/pkgmgr/`
2. **The decryptor's enforcement + `secureIdentityKeyType`'s re-aim.**
   `go test ./internal/refractor/pipeline/`
3. **`cmd/refractor`'s three helpers.** `go build ./... && go test ./cmd/refractor/`
4. **The six lens migrations + version bumps.** `go test ./packages/...`
5. **Gates.** `go build ./...` · `make vet` · `golangci-lint run ./...` ·
   `STRICT=1 go run ./scripts/lint-conventions.go` · `STRICT=1 go run ./scripts/lint-board.go` ·
   `go test ./internal/refractor/... ./internal/pkgmgr/ ./cmd/refractor/ ./packages/...`

### 17.5 In-scope gotchas

- **The wire field renames, so an installed lens spec goes stale.** `build.go` marshals the spec into the
  package's `vtx.meta.*` aspect, and `corekv_source.go` parses it back. After this lands, a lens installed
  from an older package version carries `identityKeyColumn` and no `holderTypes`, which the new required
  rule refuses — the lens fails to load rather than loading unvalidated. That is the fail-closed direction
  and the reason all six packages take a version bump in this same commit: the diff-apply is the migration.
  **On the running stack the order is reinstall-then-cycle**, never cycle-then-reinstall.
- **`secureAliasNames` shrinks, so a lens may stop being forced to RETURN a column it still declares.**
  The alias set exists to make the executor project the columns the decryptor needs; the identity-key column
  is no longer one of them. Every migrated lens keeps RETURNing its `identity_key`/`applicant` alias as an
  ordinary projected column — none of the six needs a cypher change (§6.2, re-verified against all six
  RETURN clauses) — but the *requirement* is gone, which is the point: §14 item 2 named "the declared
  column's RETURN alias was never projected" as a whole class of runtime failure this removes.
- **`packages/privacy-base` is RED at clean `main` under package-level load** — the two
  `TestPurgeIdentityDedupFootprint_*_ConvergesPastOnePage` wall-clock failures (§16.5, an already-filed
  row). Run the packages gate with `PROCESSOR_SCRIPT_WALL_MS=5000` and do not read them as this fire's.
- **The narrowing conjunct is dead code today and must stay fail-closed anyway.** `pipeline.go:851-857`
  guards a secure + actor-aggregate combination translate-time validation already refuses
  (`TestTranslateSpec_SecureColumns_ActorAggregateRejected`). Re-aim it, do not drop it: whoever lifts that
  ban owns re-deriving the guard, which is exactly what the const's own comment says.
- **`vault.KeyHolderType` already exists** (`vault/keyholder.go:54-60`, shipped in item 2) — the
  enforcement needs no new parser.

### 17.6 Adjacent finds

- **`vault-crypto-shredding-design.md:612` describes the `{column, identityKeyColumn, field}` shape.** Not
  edited: it is a shipped design doc recording what that fire built, and this design supersedes it in the
  ordinary way. Noted so a later reader does not read it as live drift.
- **`internal/refractor/failure`'s `CatPrivacyCritical` still has no generic routing** in
  `dispositionEvalErr` (`pipeline.go:2115-2137`); only `keyshredded/manager.go:379-388` acts on the tier, and
  it does so by hand — `failure.PrivacyCritical(err)` decorates the log line while the pause is a separate
  explicit `Control.PauseRule` call. Carried forward from §16.6 unchanged: 3b-ii is the fire that must either
  extend that routing or reuse the manual pattern.
- **`Pipeline.Rebuild` has no serialization gate.** `rebuildInFlight` is `Store(true)` unconditionally
  (`pipeline.go:1510`), not a CAS — only `resumeInterruptedRebuild` uses a CAS (`:1641`), and that guards a
  different thing. Not filed: it is 3b-ii's own "serialized per lens" deliverable, recorded so 3b-ii does not
  discover it mid-build.

### 17.7 Non-goals (the drift fence)

- **3b-ii** — F2's per-row `null` + privacy-tier alarm; `control.Service.RebuildRule` exported + serialized
  per lens; the Refractor destruction-event consumer with the `HolderTypes` enumeration and the
  `projectionsRebuilt` attestation (and lifting the `FailedPrecondition: step projectionsRebuilt has no
  producer yet` guard in `shred_retention_class_key.go`); `keyshredded/manager.go:31-37`'s excuse and
  `pipeline/sweep.go:29-33`'s stale reason re-derived; **lifting `custodyscope.go` rule 5.**
- **Fire 2** — the clinic and lease-signing consumers.
- Any cypher change to the six migrated lenses — none needs one.

## 18. Fire 1 item 3b-ii fire brief (build note, 2026-08-08)

Compiled by the Lattice Steward at `3c5142b8` from five read-only scout passes, before any edit. This is
the **delivery** half of item 3b, and the last increment of Fire 1: after it, a class-key destruction
reaches the read models, so both the `projectionsRebuilt` guard and the `custodyscope` install gate lift.

### 18.1 Scope sentence (verbatim, §14 "Then — item 3b-ii, the delivery half")

> Per-row failure projects `null` + a privacy-tier alarm (F2); **`control.Service.RebuildRule` exported**
> (serialized per lens); the destruction-event consumer with the `HolderTypes` enumeration and the
> `projectionsRebuilt` attestation — which also **lifts the `FailedPrecondition: step projectionsRebuilt has
> no producer yet` guard** already shipped in `shred_retention_class_key.go`, and needs a per-lens
> holder-type enumeration over `cmd/refractor`'s registry mirroring `capReadShredTargets`;
> `keyshredded/manager.go:31-37`'s excuse and `pipeline/sweep.go:29-33`'s stale reason re-derived;
> **and lifting `custodyscope.go` rule 5.**

**Scope-diff gate — brief vs ratified scope, item by item.** Seven named deliverables, all seven in the
brief, none substituted. Two derivations, neither a widening:

- **"privacy-tier alarm" is built as counter + Health-KV field + heartbeat issue + log**, because §6.2's
  ruling names the tier as "(health + log + counter)". A counter nothing reads is a mute measurement, so
  the heartbeater rule that surfaces it is part of the alarm, not an addition to it. `docs/observability/
  health-kv-schema.md` moves in the same commit (the Health-emission rule).
- **The alarm does NOT pause the lens.** §6.2's parenthetical enumerates health + log + counter; the pause
  appears only as a description of the tier `keyshredded` raises. Pausing on a per-row failure would
  reinstate the whole-lens stall F2 exists to kill (§6.2: "one bad row permanently blocks every other row
  of that lens from ever updating — including a later erasure scrub"). Redact-then-continue, alarm loudly.

### 18.2 Verified touch-list (every anchor re-checked live at `3c5142b8`)

**F2 — per-row null + alarm**
- `internal/refractor/pipeline/secure.go:144-156` — `Apply` returns on the first `decryptColumn` error
  (`:151`), discarding the rest of the result set. This is the abort F2 removes.
- `internal/refractor/pipeline/secure.go:158-250` — `decryptColumn`'s seven `failure.Terminal` returns
  (`:167`, `:173`, `:183`, `:190`, `:201`, `:220`, `:225`, `:233`) each become a per-row redaction.
  `:213-216` (`ErrKeyShredded` → `nil`) is **not** a failure and keeps its silent path.
- `internal/refractor/pipeline/secure.go:227-235` — the missing-`Field` case, which §6.2 names explicitly
  as the precedent F2 overrides.
- `internal/refractor/pipeline/secure.go:131-143` — `Apply`'s failure-posture doc comment, which states
  the Terminal posture and must be re-derived rather than left asserting the old contract.
- `internal/refractor/pipeline/evaluate.go:81-85` (`applySecureDecrypt`), plus its two other callers
  `pipeline.go:1906` and `pipeline.go:2090`.
- `internal/refractor/pipeline/secure.go:77-85` + `:89` — the `SecureDecryptor` struct and
  `NewSecureDecryptor`; it holds `vault`, `coreKV`, `columns`, `calls` and **no logger or reporter**.
- `internal/refractor/health/reporter.go:323-335` (`RecordEvalDriftRetry`) — the counter method to mirror.
- `internal/refractor/health/healthwire/healthwire.go:38-106` — the `Entry` struct the counter field joins.
- `internal/refractor/health/lattice_heartbeater.go` — the reader that turns a counter into an issue.
- `docs/observability/health-kv-schema.md:478-492` — the per-lens issue-kind list.

**Serialized rebuild**
- `internal/refractor/control/service.go:870-883` — unexported `rebuildRule`, spawns a bare goroutine.
- `internal/refractor/control/service.go:795` — the `"rebuild"` RPC dispatch, which must stay async
  (`{Started:true}`).
- `internal/refractor/control/service.go:584/602/617/632` — `ResumeRule`/`PauseRule`/`NullifyRow`/
  `NullifyActor`, the exported-sibling shape to mirror (lock → look up → unlock → call).
- `internal/refractor/pipeline/pipeline.go:1507-1511` — `Rebuild` stores `rebuildInFlight`
  **unconditionally**, not by CAS.
- `internal/refractor/pipeline/pipeline.go:1642` — `resumeInterruptedRebuild` already CASes, so the atomic
  is used both ways today; `:1282` `RebuildInFlight()` is **already exported** (the completion predicate
  §6.3 step 4 needs exists).
- `cmd/refractor/reload.go:372-378` — the second unserialized caller. It holds no control service
  (`reloader` at `:255`), which is why serialization goes in `Pipeline.Rebuild`, not only in the exported
  control method.

**Destruction consumer + enumeration + attestation**
- `internal/refractor/keyshredded/manager.go` — the whole consumer shape to mirror: `Config` `:127-158`,
  `Manager` `:161-168`, `New` `:239`, subscribe `:274-282`, handler `:296-429`, finalization submit.
- `internal/refractor/keyshredded/manager.go:70-74` — `FilterSubject` + `DefaultDurable`
  (`refractor-keyshredded`). Taken names: `privacy-worker`, `privacy-worker-retention`,
  `refractor-keyshredded`.
- `internal/privacyworker/manager.go:55-68` — `RetentionClassKeyShreddedFilterSubject`
  (`events.privacy.retentionClassKeyShredded`) and `DefaultRetentionClassDurable`.
- `internal/privacyworker/manager.go:335-372` — `submitFinalization`: the op envelope shape, the
  `contextHint.reads` of `<holder>.piiKey`, the derived request id.
- `cmd/refractor/main.go:78-118` — `pipelineEntry`, whose `secureColumns []lens.SecureColumn` (`:117`) is
  the declared metadata the enumeration reads.
- `cmd/refractor/main.go:131-150` — `capReadShredTargets`, the pure-over-registry precedent, and
  `isPerEntryCapReadOutput` `:126-129` factored out of it for the same testability reason.
- `cmd/refractor/main.go:389-398` (construct), `:435-448` (`SetTargetLister` under the registry `mu` at
  `:427`), `:450-454` (run) — the wiring to mirror.
- `internal/refractor/pipeline/secure.go:116-129` — `SecureDecryptor.HolderTypes()`, whose doc comment
  already states its purpose is "does this lens react to holder type T?".

**Guard + gate lifts**
- `packages/privacy-base/shred_retention_class_key.go:236-246` — the `projectionsRebuilt` refusal.
- `packages/privacy-base/shred_retention_class_key.go:42-57`, `:95`, `:100` — the three doc/schema spans
  that assert "no producer yet" and must be re-derived with the guard.
- `packages/privacy-base/lenses.go:165-166` — `retentionKeyStatus`'s "has no producer until item 3b".
- `packages/privacy-base/package.go:48` — version `0.12.0`, needs a bump.
- `internal/pkgmgr/custodyscope.go:76-99` — rule 5, whose own comment names this fire as its remover
  ("REMOVE THIS with the rebuild-driven delivery increment").
- `internal/pkgmgr/custodyscope_test.go:35-53` — the test asserting "not installable yet"; it must be
  re-aimed to assert the declaration now INSTALLS, not deleted.

**Stale reasons to re-derive**
- `internal/refractor/keyshredded/manager.go:31-37` — the secure-lens excuse.
- `internal/refractor/pipeline/sweep.go:18-27` — the "auth-plane only" reason; the real gate is
  `sweepEnrolment` (`driver.go:301-323`), a three-conjunct structural refusal.

### 18.3 Precedents to mirror

| New thing | Mirror | Why this one |
|---|---|---|
| Destruction consumer | `keyshredded.Manager` | Same process, same stream, same durable-consumer shape, same finalization-submit tail; §6.3 step 1 names it. |
| Registry enumeration | `capReadShredTargets` (`main.go:141`) | Pure over `registry`, caller holds the lock, re-evaluated per event so a newly-installed lens is picked up without a restart. |
| Counter + health field | `RecordEvalDriftRetry` / `EvalDriftRetries` | The one existing per-pipeline cumulative counter whose only job is to make a silent in-band behaviour observable. |
| Exported control method | `PauseRule` / `NullifyRow` | Lock → look up → unlock → call; already exported "for exactly this kind of in-process caller" (ledger #29). |
| Finalization submit | `privacyworker.submitFinalization:335-372` | Already submits `RecordRetentionClassShredFinalization`, so the second step reuses a proven envelope + contextHint. |

### 18.4 Increment order + green checks

1. **F2 per-row redaction.** `decryptColumn` returns a typed redaction instead of `failure.Terminal`;
   `Apply` records it and continues; the pipeline logs `failure.PrivacyCritical` + bumps the counter.
   Green: new per-branch tests asserting the column is `null`, the row is written, sibling rows still
   project, and the counter moved. `go test ./internal/refractor/...`.
2. **Health surface.** Counter field on `Entry`, `Reporter.RecordSecureRedaction`, heartbeater issue kind,
   `health-kv-schema.md` in the same commit. Green: `go test ./internal/refractor/health/...`.
3. **Serialized rebuild.** `Pipeline.Rebuild` takes a per-pipeline rebuild lock (structural — covers the
   reloader caller that cannot reach the control service); `control.Service.RebuildRule(ctx, ruleID,
   truncate) error` exported and run to completion; the RPC arm keeps returning `{Started:true}` by
   calling it in a goroutine. Green: a test firing two concurrent rebuilds and asserting they serialize.
4. **Destruction consumer.** New `internal/refractor/classkeyshredded` package + wiring in
   `cmd/refractor/main.go`; enumerate by holder type over the registry; rebuild each to completion; submit
   `RecordRetentionClassShredFinalization{step:"projectionsRebuilt"}`. Green: package tests over a fake
   control service, plus a pure test of the enumeration.
5. **Guard + gate lifts + version bump.** `projectionsRebuilt` accepted; rule 5 deleted; both tests
   re-aimed; privacy-base `0.12.0 → 0.13.0`. Green: `go test ./packages/privacy-base/... ./internal/pkgmgr/...`.
6. **Full gates.** `go build ./...`, `make vet`, `golangci-lint run ./...`, `STRICT=1 go run
   ./scripts/lint-conventions.go`, `STRICT=1 go run ./scripts/lint-board.go`, the other `scripts/lint-*.go`
   gates, `go test ./...`.

### 18.5 In-scope gotchas

- **The ordering question is settled, and settled by the flag, not by luck.** A rebuild driven by the
  destruction event decrypts against `envelope.Shredded`, which `ShredRetentionClassKey` writes to
  `<holder>.piiKey` **synchronously on the same commit that emits the event**
  (`shred_retention_class_key.go:196-223`), and `SecureDecryptor.readPiiKeyEnvelope` (`secure.go:252-271`)
  reads that document straight off Core KV. `local.go:391-401` refuses on `b.shredded[holder] || envelope.Shredded`.
  So the rebuild does **not** race `privacyworker`'s async `Vault.ShredKey` — the in-memory deny-list is a
  redundant second path, not a precondition. Do not add a wait-for-privacyworker handshake.
- **A rebuild already in flight must not be counted as this destruction's rebuild.** It may have started
  before the shred flag landed, so its rows can carry plaintext. The serialization must **queue**, not
  drop: wait for the in-flight one to drain, then start a fresh one. A CAS-and-skip here would be
  fail-open, which is the opposite of what this whole increment is for.
- **`Apply` mutates rows in place and F2 keeps the row.** A redacted column must be set to `nil`
  explicitly — leaving the ciphertext in the row would project ciphertext into a plaintext column, the one
  thing `:131-143`'s posture comment promises can never happen.
- **`ErrKeyShredded` is not a failure.** It already projects `null` and must not raise the alarm, or every
  legitimate erasure becomes an alert and the signal is worthless.
- **Zero matching lenses is a valid outcome, and must still attest.** If no active lens declares the
  destroyed holder's type, nothing holds the plaintext and `projectionsRebuilt` is vacuously true — attest
  immediately rather than leaving the erasure permanently unfinalized.
- **Over-rebuild is the ratified direction** (§6.3): enumerate by holder *type*, not holder *instance*.
- **The wire field on `Entry` is a Health-KV schema change**, so `docs/observability/health-kv-schema.md`
  moves in the same commit — that is what keeps the change L2-safe.
- **`lint-package-version`** demands a version bump for a `pkgmgr` change reaching a generated read-grant
  producer lens; item 3b-i needed seven bumps for this reason. Re-run the gate rather than predicting it.

### 18.6 Adjacent finds (filed now, not carried)

- **`dispositionEvalErr` has no `CatPrivacyCritical` arm** (`pipeline.go:2116-2138`): the category is
  defined and classified but falls through to the default `Nak`. Today that is unreachable, because the
  category is only ever constructed inside `keyshredded` (`manager.go:382`, `:399`), which handles its own
  pause and never returns the error into the pipeline's disposition path. This fire keeps it unreachable
  (F2's alarm does not return a `PrivacyCritical` error up the evaluation path — it redacts and continues),
  so the gap stays latent rather than being closed here. **Filed as a board row** naming the consumer: the
  first caller that wraps an evaluation-path error as `PrivacyCritical`.
- **`capReadShredTargets` skips hand-authored Postgres GrantTable cap-read producers** (no `Output`
  descriptor) — already named in its own doc comment as a distinct gap; not this fire's, and unchanged by
  it. Not filed anew; the comment carries it.

### 18.7 Non-goals (the drift fence)

- **No Fire 2 consumers.** No clinic `.encounter` split, no lease-signing `.profile` split, no
  `patientDemographics.fullName` move, no FE. Lifting rule 5 makes those *possible*; it does not start them.
- **No retained-class egress.** Deferred tail (a) stays shut; the two identity-only holder refusals stay.
- **No period bucketing, no purpose-gated retained reveal, no background-check detail** — tails (b)–(d).
- **No change to the identity kind's in-band scrub.** F1 ratified two mechanisms; `keyshredded`'s
  behaviour is untouched and only its *comment* is re-derived.
- **No cypher change to the six migrated lenses.** None needs one.
- **No new sweep plan for secure lenses.** `sweep.go`'s stale *reason* is corrected; the *gate*
  (`sweepEnrolment`) is not re-aimed.

## 19. Item 3b-ii — built, reviewed, HELD (2026-08-08)

**State: `fire/retention-class-3b-ii` @ `abe2e798`, in the worktree named in §14. NOT merged; `main` carries
none of it.** The increment is functionally complete against §18's scope and every gate that ran was green —
`go build`, `make vet`, `golangci-lint` (0 issues), all seven `scripts/lint-*.go`, and the package tests for
each area. It is held because the mandated 3-layer adversarial review (security plane) returned blockers that
are **real fail-opens in the erasure guarantee**, and an erasure attestation that can be wrong is worse than
one that does not exist yet. Two independent reviewers (opus) converged on most of them, which is why they
are recorded as findings rather than hypotheses.

**Do not re-derive these.** Each is grounded at a file:line in that worktree.

### 19.1 Blockers — must close before merge

**B1 — a cold-boot empty registry attests with zero rebuilds.** `classKeyShredded.Run` is launched at
`cmd/refractor/main.go:499` but the lens registry is not populated until `:1169`, behind
`bootstrapper.Ready()` and `src.Start(ctx)`. `holderTypeRebuildTargets` over an empty map returns nil →
`allClean` stays true → `projectionsRebuilt` is submitted → **Ack**, so no redelivery. A restart with a
pending destruction, or a first-ever boot (the durable is `DeliverAllPolicy`, so the whole subject history
replays), attests a complete erasure while every declaring lens keeps serving plaintext. The identity
consumer already solves this and **says so** at `main.go:390-398` — a static floor target makes an
unloaded registry hit `ErrRuleNotRegistered` → Nak, and that comment explicitly names the vacuous-Ack
regression "this fire must not reintroduce". `classkeyshredded` has no floor and no readiness gate, and
`TestHandle_NoDeclaringLensStillAttests` currently *enshrines* the vacuous attestation. The legitimate case
("no live lens declares this type") and the dangerous one ("the registry has not loaded") are
indistinguishable as written; they must be told apart by an explicit readiness signal, not by the target
count.

**B2 — the completion signal is clobberable, so the wait can return over a still-running rescan.**
`waitRebuildDrained` polls `rebuildInFlight`, a single pipeline-wide `atomic.Bool` that
`abandonRebuild` (`pipeline.go:1492`) clears unconditionally as its first statement. `rebuildSerial`
serializes `RebuildAndWait` against itself only — the other two callers use plain `Rebuild` and bypass it:
`cmd/refractor/reload.go:373` (the MATCH hot-reloader) and `control/service.go:906` (the operator rebuild
op). So a concurrent hot-reload or operator rebuild that fails anywhere after its own `Store(true)` clears
the flag out from under a waiting caller, which then returns nil and attests. **The commit message on
`abe2e798` states the serialization covers the hot-reloader because it lives in `Pipeline` — that claim is
false and must be corrected with the fix.** The durable fix is a per-rebuild completion signal (a channel
created by the rebuild that launched it, closed by its own watcher) rather than a shared flag; polling
`OutstandingForConsumer` directly is the weaker alternative and still needs a stability check, since a
transient zero is observable between `supervisor.Reset` and the replay landing.

**B3 — an unbounded wait wedges the whole consumer, and the handler creates the condition itself.**
`waitRebuildDrained` has no deadline, and `rebuildInFlight` never clears for a **paused** lens: `Rebuild`'s
`supervisor.Reset` only requests a reopen and does not clear a pause, so the pump stays parked, outstanding
never reaches zero, and the watcher loops forever. `handleClassKeyShredded` calls this **synchronously**
inside the message handler, and `substrate`'s durable loop is strictly serial — so one paused declaring lens
stops every subsequent class-key destruction for the life of the process. It is self-inflicted: the handler
calls `PauseRule` on a failed rebuild (`manager.go:256`), and the next event enumerates that same lens and
wedges. Needs a bounded wait whose expiry is a failure (no attestation), not a hang. Related and bounded by
the same fix: `RunDurableConsumer` sets no `AckWait`, so JetStream's 30s default redelivers during any
rebuild longer than 30s — the normal case at these lens sizes — amplifying one destruction into repeated
full rescans.

### 19.2 Majors — close with the blockers

**M1 — the rebuild erases the counter that is its own evidence.** `Reporter.SetActive` / `SetRebuilding` /
`SetPaused` build a fresh `Entry` carrying forward only `ConsumerLag`, `ErrorCount`, `SweepCursor`,
`SweepReconciled`. `SecureRedactions` is not in that list, so **every status transition zeroes it** — and
`Rebuild` calls `SetRebuilding` on the way in and `SetActive` on the way out, so this fire's own rebuild
clears the counter at both ends of itself. The `LensSecureRedaction` issue then goes quiet while the
unresolvable nulls are still being served, which is exactly the delta-signal failure the counter was made
cumulative to avoid. (`EvalDriftRetries`/`EvalDriftRequeues` carry the same pre-existing bug; the new
counter inherited the pattern rather than introducing it.)

**M2 — `projectionsRebuilt` is forgeable, and the comment that replaced the guard asserts a property no
code enforces.** The lifted script guard was the only thing preventing an attestation with no rebuild. The
replacement text claims each step "is written by the identity.system.privacy service actor after its own
irreversible work landed, so neither can attest to work that never ran" — but `execute` never reads
`op.actor`, and the verb is `Scope:"any"`, `GrantsTo:["operator"]` (`permissions.go:99-105`). Any operator
can stamp it one second after the destruction commits; the only precondition checked is that the piiKey is
shredded, which is true by construction at that moment. `op.actor` IS available to these scripts
(`clinic-domain/ddls.go:1375` uses it). Either pin the actor for the finalization steps or withdraw the
comment's guarantee — as written the comment is what stops the next reader from looking. **Note the same
exposure already exists for `vaultKeyDestroyed`,** which predates this fire, so the fix should cover both
steps rather than only the new one.

**M3 — a shutdown mid-rebuild loses the destruction permanently.** `context.Canceled` from `RebuildRule` is
not `ErrRuleNotRegistered`, so it takes the "real failure" arm and falls through to `return substrate.Ack`
(`manager.go:287`). A rolling deploy during a rebuild therefore acks the event, advances the durable cursor,
and the destruction is never redelivered: no lens is ever rebuilt, `projectionsRebuilt` is never recorded,
and nothing remains to drive it. The handler needs an explicit `ctx.Err()` check returning Nak.

**M4 — a known redaction count is lost to an unrelated observation fault.**
`snap.SecureRedactions = st.SecureRedactions` sits at `cmd/refractor/main.go:733`, *after* the
`pipeline.Pending()` early-`continue` at `:722`. In that branch `st` was read successfully and carries the
count, but the snapshot ships zeroed — so a NATS blip reading the consumer's pending count silently
downgrades the highest-ranked alert in the system to `unreadable`/warning. `lattice_heartbeater.go:1281`
`continue`s before the redaction block at `:1302`, so both sites need the fix. (Skipping on a failed
`GetStatus` is correct — there the count genuinely is unknown.) This contradicts the doctrine stated 30
lines above it at `main.go:681-686`, which reads the sweep verdicts *before* the reporter for this exact
reason.

### 19.3 Minors

- The handler logs `"attested", allClean` while the submit is gated on `allClean && ActorKey != ""`, so the
  documented attestation-disabled configuration logs `attested=true` having published nothing
  (`manager.go:275` vs `:283`).
- `applySecureDecrypt` alarms on redactions accumulated before a non-Terminal error, justified by a comment
  saying "those nulls land whether or not this evaluation completes". They do not: all three call sites
  discard the result set on error. The `>0` verdict stays correct (a genuine defect was seen) but the count
  an operator sizes exposure from inflates on every redelivery.

### 19.4 What the review CONFIRMED sound — do not re-litigate

- **The ordering claim holds, and by derivation rather than luck.** `local.go:399` tests
  `envelope.Shredded` *before* the `dekCache` lookup at `:408`, so a cached DEK cannot open a shredded
  holder; `secure.go:288` point-reads `<holder>.piiKey` on every `decryptColumn`, so no decryptor holds a
  stale envelope. The Refractor's own `LocalBackend` never learns of the privacy-worker's `ShredKey`, which
  is precisely why the durable flag written on the shred's own commit is load-bearing — and it is honoured.
  No handshake with the privacy-worker is needed.
- **F2's redaction leaks nothing.** All seven Terminal returns in `decryptColumn` are reached before the
  column is written, and `Apply` nulls the column for every one of them; the classification keys off
  `errors.As` wrapper detection, so no data value can move an error between categories. Ciphertext can never
  survive into a plaintext column. One forward caveat worth keeping: `LocalBackend.Decrypt` is pure
  in-memory, so if a network-backed KMS backend ever lands, `secure.go:252` would convert a transport blip
  into a redaction — the exact posture this increment rules out.
- **The `custodyscope` rule-5 removal is sound.** Neither reviewer could construct a declaration that now
  installs but should not; the else-arm restructure is equivalent to the old rule 6 for every reachable
  input, and rules 1–4 are untouched.
- **The enumeration is sound.** No nil registry entries are reachable, empty `HolderTypes` is refused at
  construction, duplicate rule IDs are impossible, and `allClean` is correctly false on the
  budget-exhaustion give-up path.

### 19.5 Verification still owed at merge

The full `go test ./... -p 4` for this branch has **not** been run clean, and the one run that completed
exited **1 with nine failing packages**. It ran concurrent with a 15-minute `golangci-lint` and two opus
review agents, so it is not a result — but it is not dismissible either, and the earlier reading of it
recorded here was wrong in a way worth correcting rather than quietly fixing.

- **Eight `packages/*`** (cafe-domain, clinic-domain, identity-domain, lease-signing, loftspace-domain,
  privacy-base, semantic-contracts, service-domain) failed with `ScriptTimeout: script exceeded wall budget
  250ms` at package INSTALL — the already-filed load-flake signature, in packages this fire never touched.
  Consistent with host contention.
- **`internal/refractor` also failed, and NOT with that signature.**
  `TestRefractor_CapabilityLens_RealClaimIdentityOp_E2E` and its `_WithEphemeralConsumer_E2E` sibling failed
  on a 25s convergence assertion (`refractor_claim_batch_real_op_e2e_test.go:292`): `cap.roles.<target>`
  never gained the consumer-role grant from the real `ClaimIdentity` op's own `holdsRole` write. The test's
  own message calls itself a captured minimal repro of a defect "specific to the real script's own mutation
  encoding", so it may well be a known-open pin rather than a regression — **but that is a hypothesis, and
  it is in a tree this fire edited.**

**The decisive check the next fire must run FIRST, before anything else:** those two tests against
**clean `main`** (the main checkout carries no code from this fire — docs only — so it is a true baseline).
If they fail there too, they are pre-existing and the eight package failures are contention. If they pass
there and fail on the branch, this increment has a regression the review did not find, and that outranks
every item in §19.1. A reason this fire's changes are *unlikely* to be the cause — `applySecureDecrypt`
returns immediately when no decryptor is installed, and these are plain auth-plane lenses with no secure
columns — is an argument for the hypothesis, not a substitute for running it.

**Verdict (run 2026-08-08, quiet host, detached worktree at `de1000fd` vs the branch): NOT a regression —
the sibling is a pre-existing flake at clean `main`.** `_E2E` passes on both. `_WithEphemeralConsumer_E2E`
fails **2 of 4** runs at clean `main` and **2 of 3** on the branch: comparable rates, and a failure at
`main` is by construction not caused by code `main` does not carry. The eight `packages/*` failures were
contention, as suspected. What this did surface is a real intermittent at head — the ephemeral-consumer
sibling is genuinely non-deterministic, filed to `lattice.md` for the Whetstone under the standing rule
(tighten, never loosen). §19.1's blockers are therefore the whole remaining gate.

## 20. Item 3b-ii — review-closure fire brief (build note, 2026-08-08)

Compiled from §19, which is itself the scout report: every finding is already grounded at a `file:line` in
the `fire/retention-class-3b-ii` worktree, so this brief re-verifies anchors and fixes the build order
rather than re-deriving the findings. Line numbers below are at `2c6cad86` (the increment rebased onto
`de1000fd`).

### 20.1 Scope sentence

Close every finding §19.1–§19.3 raised against item 3b-ii — three blockers, four majors, two minors — so the
increment's erasure attestation cannot be true-by-vacuity, clobbered, or wedged, and then merge it. Nothing
else: no new capability, no widening of what 3b-ii delivers.

### 20.2 Verified touch-list

| Finding | Anchor | Fix |
|---|---|---|
| B1 | `cmd/refractor/main.go:488-503` (launch), `:1267` (`src.Start`), `classkeyshredded/manager.go:222-288` | An explicit registry-readiness gate, supplied by `health.RegistryProbe`. |
| B2 | `pipeline/pipeline.go:335` (`rebuildInFlight`), `:1489` (`abandonRebuild`), `:1530` (`RebuildAndWait`), `:1551` (`waitRebuildDrained`) | Per-rebuild completion channel, closed by that rebuild's own watcher. |
| B3 | `pipeline/pipeline.go:1551`, `classkeyshredded/manager.go:231`, `substrate/consumer.go:139-148` | Bounded wait + an explicit `AckWait` on the durable. |
| M1 | `health/reporter.go:110-125`, `:164-178`, `:206-220` | Carry the cumulative counters across every status transition. |
| M2 | `packages/privacy-base/shred_retention_class_key.go:232-240`, `permissions.go:99-105`, `internal/processor/starlark_runner.go:661` (`op.actor`), `:626` (`.class`) | Pin the finalization actor to the privacy service actor, in-script. |
| M3 | `classkeyshredded/manager.go:250-261`, `:287` | `ctx.Err()` → Nak before the "real failure" arm. |
| M4 | `cmd/refractor/main.go:717-733`, `health/lattice_heartbeater.go:1260-1281` | Read the redaction count before the observation that can fail. |
| Minors | `classkeyshredded/manager.go:275`/`:283`; `pipeline/evaluate.go:94-104` | Log what was actually done; count only nulls that were actually written. |

### 20.3 Precedents to mirror

- **Readiness, not a target count** — `cmd/refractor/main.go:390-398`'s static floor for the identity half.
  A retention-class floor cannot exist (no lens is forced to bind a class), so the same intent is served by
  an explicit signal instead: `health.RegistryProbe` already computes declared-vs-registered, which is the
  exact question, and a nil signal reads as NOT ready so the boot window is fail-closed by construction.
- **Per-rebuild signal** — `watchRebuildCompletion`'s existing `defer` ownership: the goroutine that watches
  a rebuild is the thing that ends it. The channel just makes that ownership addressable.
- **Actor-class check** — `state[key].class` is the same projection `kv.Read` returns
  (`starlark_runner.go:618-637`), so the guard reads like every other in-script envelope check.

### 20.4 Increment order + green checks

1. `substrate` `AckWait` + `pipeline` per-rebuild signal + bounded `RebuildAndWait` (B2, B3) →
   `go test ./internal/substrate/ ./internal/refractor/pipeline/`.
2. `RegistryProbe.ReconcileNow` + the readiness gate + `ctx.Err()` arm + the log fix (B1, M3, minor 1) →
   `go test ./internal/refractor/health/ ./internal/refractor/classkeyshredded/ ./internal/refractor/control/`.
3. Counter carry-forward + read-before-observe + count-only-what-lands (M1, M4, minor 2) →
   `go test ./internal/refractor/health/ ./internal/refractor/pipeline/ ./cmd/refractor/`.
4. Actor pin (M2) + `privacy-base` version bump → `go test ./packages/privacy-base/ ./internal/privacyworker/`.
5. Full gates + a quiet `go test ./... -p 4` judged against the §19.5 baseline.

### 20.5 In-scope gotchas

- The bound in B3 must apply to the **wait**, never to the rebuild: passing a deadline-bearing context into
  `Rebuild` would also kill `watchRebuildCompletion`, latching `rebuilding` and suppressing the sweep — the
  precise failure `abandonRebuild`'s doc comment was written about.
- M2's guard needs the actor key in `contextHint.reads` at **every** submitter of the verb, or the op
  fails closed on a legitimate submit. Both submitters are ours; a missed one is a live outage, not a test
  failure, so change them in the same commit as the script.
- The actor pin narrows "any operator" to "the privacy service actor". It does **not** close ops-lane actor
  impersonation, which is a platform-wide seam and not this fire's; say so in the comment rather than
  restating a guarantee the code does not give — that overclaim is what M2 was.

### 20.6 Adjacent finds (filed now)

- `TestRefractor_CapabilityLens_RealClaimIdentityOp_WithEphemeralConsumer_E2E` is a genuine intermittent at
  clean `main` (2/4 failures, §19.5) — filed to `lattice.md` for the Whetstone.
- `RecordShredFinalization` (the identity-plane verb) carries M2's exposure identically. It lives in the
  same package and takes the same three-line guard, so it is fixed here rather than filed — leaving one of
  two sibling verbs pinned would be an asymmetry the next reader has to rediscover.
- `EvalDriftRetries` / `EvalDriftRequeues` are zeroed by the same status transitions as `SecureRedactions`
  (M1). Same field list, same one-line fix, so they are carried forward here too.

### 20.7 Non-goals

Read-path auth, the ops-lane impersonation seam, Fire 2's consumers (the clinic PHI aspect), and any change
to what 3b-ii projects. No test is loosened to accommodate the §19.5 flake.

## 21. Item 3b-ii — review closed (2026-08-08)

Every finding in §19.1–§19.3 is closed in code. What each fix actually changed, and the two places the
review's own prescription was adjusted after grounding it:

**B1 — readiness is now an explicit signal, not an inference from the target count.** `classkeyshredded`
takes a `SetRegistryReady` check and gates the WHOLE delivery on it, not merely the empty-target case: an
incomplete registry yields a **short** target set, so attesting off a partial enumeration is the same fail-open
as attesting off an empty one. The signal is `health.RegistryProbe.ReconcileNow`, a new on-demand form of the
reconciliation the probe already runs on a tick — it asks **Core KV**, the platform's persistent lens registry,
rather than the in-process map whose incompleteness is the hazard, so it answers the question the identity
half's static floor answers by proxy. A floor cannot be built here: nothing obliges any lens to bind a
retention class, so no lens is guaranteed to exist to hold it. **Absence of the check reads as NOT ready**,
which covers the whole boot window (it is wired after the lens source starts, and the durable is
`DeliverAll`). Past a bounded redelivery budget it delivers to what IS registered and withholds the
attestation, leaving the operator's row visibly in-flight — the honest state when the registry itself is the
thing that is wrong. `TestHandle_NoDeclaringLensStillAttests` keeps its behaviour and gains its precondition:
the vacuous attestation is correct **only** because the registry is ready.

**B2 — a per-rebuild completion channel replaces the shared flag.** `Rebuild` installs a channel that its own
watcher closes (`beginRebuild`/`endRebuild`, with `endRebuild` clearing the slot only if it still holds *its*
channel, so a slow finisher cannot retract a newer rebuild's signal). `RebuildAndWait` waits on the channel it
was handed, so neither the MATCH hot-reloader nor the operator rebuild op can end another caller's wait by
failing. **The `abe2e798` commit message's claim that `rebuildSerial` covers the hot-reloader is withdrawn:
it serializes `RebuildAndWait` callers against each other and nothing else**, which is precisely why the wait
had to stop reading shared state. `rebuildInFlight` survives unchanged as the health sink's status hint — it
was never a completion signal, and the fix is to stop asking it to be one.

**B3 — the wait is bounded; the rebuild is not.** `RebuildAndWait` takes a wait budget and returns
`ErrRebuildWaitTimeout`, which an attesting caller must read as "not known to be rebuilt". The budget
deliberately does **not** ride on the rebuild's context: cancelling `Rebuild` would also kill
`watchRebuildCompletion`, latching `rebuilding` and, through `Sweeper.suppressed`, retiring the convergence
sweep for the life of the process — the exact failure `abandonRebuild` exists to prevent. The consumer's
durable also sets `AckWait` (new on `substrate.DurableConsumerConfig`) above its own upper bound, since
JetStream's 30s default redelivers during any rebuild longer than 30s — the normal case at these lens sizes,
with each redelivery re-running every rebuild.

**M1** — `SetActive`/`SetPaused`/`SetRebuilding` now carry `SecureRedactions` **and** `EvalDriftRetries` /
`EvalDriftRequeues` forward. The pre-existing pair is fixed with the new counter rather than filed: it is the
same field list and the same one-line omission, and leaving two of three zeroed is a trap for the next reader.

**M2 — the finalization actor is pinned in-script, and the overclaiming comment is gone.** Both steps of
`RecordRetentionClassShredFinalization` — and, for the same reason and in the same package, both steps of
`RecordShredFinalization` — now require `op.actor` to resolve to a live `identity.system.privacy` vertex,
declared by every submitter in `contextHint.reads` (an undeclared actor fails closed). Two things worth
recording: `class` is a **reserved word** in `go.starlark.net`, so the check reads `getattr(doc, "class")` —
`doc.class` is a parse error, which would have surfaced at package install, not at build; and the guarantee is
deliberately stated narrowly, because it bounds **which identity** may attest, not **who may publish under
that identity**. Ops-lane actor impersonation is a platform seam and is unchanged. Saying so is the point:
the comment that claimed the stronger property is what M2 was.

**M3** — a `context.Canceled` from `RebuildRule` now Naks instead of falling through to the "real failure"
arm, so a rolling deploy redelivers the destruction rather than acking past it with no lens rebuilt and
nothing left to redrive it. It also stops blaming the lens: the old path paused it.

**M4** — the redaction count is read off the successful status read **before** the pending-count read that can
fail on its own, at both the provider (`cmd/refractor`) and the heartbeater, so a NATS blip observing one
input no longer erases a fact already in hand about another. The heartbeater's unreadable branch now evaluates
the redaction alert too, and carries the count in its metric.

**Minors** — the handler logs `attested` as what it actually did (`allClean && ActorKey != ""`), so the
documented attestation-disabled configuration stops logging a submit that never happened; and the redaction
COUNTER advances only when the evaluation completes, since all three callers discard the result set on error
and the redelivery would otherwise re-add the same redactions on every retry. The log stays unconditional — a
refusal that happened is worth seeing whether or not its row landed.

Two adjacent things this fire also carries, each named in §20.6: the identity-plane finalization verb (M2's
sibling, fixed here rather than filed) and the drift-counter pair (M1's). The package-test harness now routes
finalization records over the **system** lane, which is where their real submitters publish — the previous
urgent-lane submit was a fixture divergence the actor pin surfaced.

## 22. Item 3b-ii — the closure reviewed, and HELD AGAIN (2026-08-08)

**State: `fire/retention-class-3b-ii` @ `a83340d8` (§21's closure, rebased onto `731041bb`), in the worktree
named in §14. STILL NOT merged; `main` carries none of it.** Every gate that ran was green — `go build`,
`make vet`, `golangci-lint` (0 issues), all eight `scripts/lint-*.go`, and the targeted package tests of
§20.4. The **acceptance layer confirms every §19.1–§19.3 finding is genuinely closed**, each by a
non-tautological test, with **no scope drift** past §20.1 and all three §20.5 gotchas honoured; §21's
narrative was checked claim-by-claim against the code and holds.

It is held because the two adversarial layers (opus, security plane + edge-case) returned a **second crop of
findings, introduced by the fix itself** — and the most serious of them **defeat the very findings they were
closing**. Two independent reviewers converged on most; the two marked *verified* were re-grounded by Winston
directly rather than taken on report. **Do not re-derive these** — each is at a `file:line` in that worktree.

### 22.1 Blockers — must close before merge

**C1 — a cancelled watcher closes the completion signal, so the wait returns success and M3 never fires.
(Both layers; VERIFIED.)** `watchRebuildCompletion`'s `case <-ctx.Done(): return` runs its
`defer p.endRebuild(done)` (`pipeline.go:1825-1832`), which **closes** `done` having never observed
`outstanding == 0`. `waitRebuildSignal` (`:1635-1648`) then has both `<-done` and `<-waitCtx.Done()` ready and
Go picks uniformly at random, so on shutdown `RebuildAndWait` returns **nil** about half the time for a rescan
that was killed mid-drain. `classkeyshredded/manager.go:328-330` takes `if err == nil { continue }`, so M3's
`ctx.Err()` Nak arm at `:336` — which lives *inside* the `err != nil` branch — is never reached, `allClean`
stays true, and the handler attests. Correctness then rests only on the cancelled-ctx publish failing. This is
M3 re-opened by B3's own machinery. The wait must not read a closed channel as success while `ctx.Err() != nil`.

**C2 — `ErrRebuildWaitTimeout` falls through to the pause arm, and a paused lens can never drain a rebuild.**
`manager.go:327` → `:336` (not ctx) → `:341` (not `ErrRuleNotRegistered`) → **`:355-365` pause the lens**. The
one error class meaning "the rebuild is fine, I stopped waiting" is indistinguishable from "target
unreachable". A lens whose rescan legitimately exceeds `DefaultRebuildWait` is therefore paused — and the
pause is self-perpetuating by this file's own grounding at `:94-98` (`supervisor.Reset` requests a reopen
without clearing a pause), so every later destruction burns the full budget, times out, and re-pauses it while
the lens serves its pre-destruction rows. B3's wedge is replaced, not removed. Compounding: the budget is
**shared across both waits** (`pipeline.go:1613-1628`), so a concurrent hot-reload rebuild can consume it and
`RebuildAndWait` returns the sentinel **without ever starting a rebuild** — and the handler then pauses a lens
it never touched.

**C3 — a submitter was missed, and it is build-tagged, so no default gate catches it. (VERIFIED.)**
`internal/systemactorcapability/systemactorcapability_test.go:425` submits `RecordShredFinalization` as
`bootstrap.PrivacyIdentityKey` with `reads = []string{piiKeyKey}` — **the actor vertex is not declared**, so
M2's guard fails it closed and `require.Equal(Accepted)` at `:428` fails. The file carries
`//go:build systemactorcapability` (`:1`), so `go test ./...` never compiles it: **`make
test-system-actor-capability` is red right now, silently**, and it is not in `.github/workflows`. The
production submitters are complete (`privacyworker/manager.go:371`, `keyshredded/manager.go:487`,
`classkeyshredded/manager.go:443`); this is the standing "build-tagged tests escape the default gates" rule.

**C4 — the two redelivery budgets share one counter, so the not-registered budget is dead. (Both layers.)**
`maxNotReadyDeliveries` (`:303`) and `maxNotRegisteredDeliveries` (`:346`) both test `msg.NumDelivered`
against 20, and the readiness arm runs first. A boot that spends 20 deliveries on readiness leaves a lens
hitting `ErrRuleNotRegistered` with **zero** retries — it goes straight to the privacy-critical give-up arm.
The comment at `:82-87` asserts the two are separate because they answer different questions; they are not.
The window is real: `registry[r.ID] = entry` (`main.go:1175`) precedes `RegisterRebuilder` (`:1187`), so
"ready" can be true while a rebuilder is not yet registered.

**C5 — the on-demand reconcile publishes a false `LensRegistryIncomplete` inside the boot grace window.**
`ReconcileNow` stores into the shared `p.missing` (`registry_probe.go:180-182`), which is exactly what
`hb.RegistryReconciliationProvider = registryProbe.Missing` reads (`main.go:1296`). `Missing`'s contract at
`:103-106` is explicit that it must be empty before the first check completes, and the 60s grace window
(`:15-21`) exists for that reason. Calling it from the handler on every delivery stamps the not-yet-registered
set into `p.missing` well inside that window. `ReconcileNow` should answer without publishing.

### 22.2 Majors — close with the blockers

- **C6 — `abandonRebuild` still clobbers a newer rebuild's shared state** (`pipeline.go:1501-1512`). The
  per-rebuild-ownership doctrine reached the channel but not `rebuildInFlight` or the `SetActive` write, both
  of which are still unconditional. An older rebuild failing under a newer one's live rescan therefore
  un-suppresses the convergence sweep (`sweep.go:395` reads `RebuildInFlight()`) and marks the lens active —
  the exact condition the suppression exists to prevent. Needs the same identity check `endRebuild` uses.
- **C7 — a nil reporter makes `RebuildAndWait` return "rebuilt" immediately** (`pipeline.go:1760-1769`): the
  no-watcher branch closes `done` while the rescan is still running. Not reachable through `cmd/refractor`
  today, but reachable by construction — and `control/service.go:102-112` claims the `Rebuilder` interface
  exists precisely so this cannot happen. `ErrRebuildWaitTimeout` is the honest answer for a rebuild with no
  observable end.
- **C8 — the M1 carry-forward missed five fields, one of them permanently** (`health/reporter.go:105-131`,
  `:173-192`, `:219-238`). `ProjectionLag`, `LagProgressAt`, `AckPending`, `AckFloorProgressAt` and
  `LastProjectedAt` are still zeroed by the three status writers — while `SetProjectionProgress:459-475`
  explicitly refuses to blank four of them, for the reason M1 states. The LagPoller restores four within ~5s;
  **`LastProjectedAt` it does not**, because it only writes a non-zero value, so a `SetActive` at activation
  erases the lens's last-projection timestamp for good. Same class as M1, same one-line-per-field fix.
- **C9 — M4's read-ordering doctrine reached one field out of three** (`cmd/refractor/main.go:635-646` and
  `:723-735`). `st.PauseReason` and `st.LastError` are still assigned *after* the failable `Pending()` read, in
  **both** providers, so a NATS blip still discards a pause reason and a last-error already in hand — and
  Loupe's fault conjunct keys off a live `LastError`. The commit's own argument applies verbatim.
- **C10 — `endRebuild`'s `select/default/close` sits outside `rebuildWatchMu`** (`pipeline.go:1553-1564`), so
  "closes done exactly once" is asserted rather than enforced; two enders would double-close and panic.
  Unreachable today, and moving the close inside the lock costs nothing.
- **C11 — `AckWait` is sized against one lens, not the handler, and not the prefetch buffer**
  (`manager.go:99-104`, `substrate/consumer.go:105-113`). `RebuildWait` is **per target** inside the loop at
  `:327`, so the handler's upper bound is N × 30min and **N ≥ 5 exceeds the 2h constant** the comment claims it
  is above. Worse, `RunDurableConsumer` drives `cons.Messages()` with no opts, so nats.go's
  `DefaultMaxMessages = 500` prefetches: `AckWait` runs from **delivery into that buffer**, not from handler
  entry, so on the cold-boot `DeliverAll` replay this fix is written for, a queued message is redelivered
  regardless of the value — and each redelivery re-runs every rebuild and burns `NumDelivered`, tripping C4.

### 22.3 Design-level — decide, then close

- **C12 — past the readiness budget the event is Acked with no redrive path** (`manager.go:308-316`, `:396`).
  The un-registered lenses are not in `targets` at all (the lister reads the registry), so they are never
  rebuilt, and the durable cursor has moved past the destruction. Once the registry recovers nothing
  re-triggers either the missed rebuilds or the attestation; the "visibly in-flight row" is an alert, not a
  redrive. **The fork:** Ack-and-alarm (today, loses the destruction) vs. Nak indefinitely (fail-closed, but
  wedges the consumer — the shape the filed privacy-base escalation row already describes).
- **C13 — readiness is corpus-global, so one unactivatable lens permanently withholds every attestation.**
  `declaredLensIDs` counts **every** non-deleted `meta.lens` vertex and deliberately counts a lens whose
  `.spec` fetch fails as declared. Any single lens that never registers — bad spec, failed activation, another
  deployment's — makes `readyErr` non-nil forever, so every destruction for every holder type burns the budget
  and then Acks without attesting. The signal is not scoped to lenses declaring the destroyed holder type,
  which is the question actually being asked.

### 22.4 Minors

- `packages/privacy-base/shred_identity_key.go:109` still says `RecordShredFinalization` "is read-free and
  checks the piiKey via `kv.Read`" — wrong on both counts since §21, and a submitter following it fails closed.
  Same stale text in `docs/components/privacyworker.md:72` and `docs/components/refractor.md`.
- `health/reporter.go:106-108`'s warn still names only `ErrorCount`/`ConsumerLag` as reset; it now also zeroes
  the counters M1 just made load-bearing.
- `manager.go:380-386` (submit failure) is the one Nak arm with no `NumDelivered` bound, and `MaxDeliver` is
  unset, so a persistent publish failure loops forever re-running every rebuild.
- `ReconcileNow` is a full `vtx.meta.*` scan with two `KVGet`s per meta vertex, run synchronously per delivery
  and repeated on all 20 readiness Naks.
- Test gaps the acceptance layer named: no negative test for the identity-plane sibling's actor pin (a revert
  of `shred_identity_key.go`'s guard alone would go undetected); `keyshredded`'s own `ContextHint.Reads` fix is
  unasserted (`TestHandleKeyShredded_CleanPath_SubmitsFinalization` never inspects `ContextHint`); the
  `substrate` `AckWait` plumbing has no direct test.

### 22.5 Verification still owed at merge

The full `go test ./... -p 4` on this branch is **still not run clean, and this fire's attempt is not a
result** — it collapsed across ~10 `packages/*` plus `internal/refractor` while the host sat at **11.4 GB of
12.2 GB swap with ~47 MB of free pages**, the documented contention condition, and was killed rather than left
to thrash. The three `internal/refractor` capability-lens E2E failures it recorded
(`RealClaimIdentityOp_E2E`, `_WithEphemeralConsumer_E2E`, `TwoActorClaimCeremony_MultiActorRace_E2E`) are
**not** re-judged by it: §19.5 already established the ephemeral sibling as a pre-existing intermittent at
clean `main` (2/4), filed for the Whetstone, but the other two failed only in this thrashing run and have no
quiet-host reading on either side. **The next fire must re-run those three on a quiet host, branch vs. clean
`main`, before reading anything into them** — and must not treat this fire's suite log as a baseline.

## 23. Item 3b-ii — the second crop closed (2026-08-08)

Every finding in §22.1–§22.4 is closed on `fire/retention-class-3b-ii` @ `961ddcd2`. Gates green: `go build`,
`make vet`, `golangci-lint` (0 issues), all eight `scripts/lint-*.go`, `make test-system-actor-capability`
(the build-tagged gate C3 found red), and the touched packages. What changed, and where the fix went past
the report:

**C1 (with C7, and M3 restored) — completion is a fact a rebuild records, not a channel closing.** A rebuild
now carries a `rebuildSignal{done, drained}`; `drained` is set **only** by the watcher that observed
`outstanding == 0`, and every other exit — cancelled watcher, abandoned rescan, a lens with no reporter and
so no watcher at all — closes `done` without it. The waiter reads the flag; the channel only says "stop
waiting". That removes the 50/50 select outcome at its source rather than re-ordering the select, and it
answers C7 in the same stroke: a rebuild nobody can watch reports `ErrRebuildNotDrained` instead of success.
M3's `ctx.Err()` arm lives inside the error branch the race was skipping, so it fires again. **One thing the
fix had to decide that the report did not raise:** `RebuildAndWait`'s *first* wait — waiting OUT a
pre-existing rebuild — asks a weaker question than its second. All it needs is that the prior rebuild has
**ended**; how it ended is that rebuild's business, and treating an abandoned one as a blocker would let an
unrelated failure permanently block every attesting caller. It tolerates `ErrRebuildNotDrained`; the second
wait does not.

**C2 — the two "not the lens's fault" classes get their own arm, ahead of the pause.**
`control.ErrRebuildWaitTimeout` and `control.ErrRebuildNotDrained` live on the Rebuilder contract, because
telling them from a rebuild failure is the *caller's* decision and getting it wrong is self-perpetuating: a
paused lens cannot drain a rebuild, so pausing a merely-slow one guarantees the next destruction times out
and re-pauses it. The attestation is still withheld; the lens is left running.

**C3 — the missed submitter, and the gate that could not see it.** `systemactorcapability`'s e2e now declares
the actor. `make test-system-actor-capability` passes. §20.5's own gotcha said a missed submitter is a live
outage rather than a test failure; the build tag is why review, not CI, is what caught it.

**C4 — the budgets are staged, not equal.** They share one `NumDelivered` by construction, so
`maxNotRegisteredDeliveries` now *starts* where `maxNotReadyDeliveries` ends (and the submit-failure Nak,
previously unbounded, is a third stage). The comment claiming independence is gone.

**C5 — `ReconcileNow` answers without publishing.** `reconcile` is the pure computation; only the scheduled
`check` stores into the set `Missing()` exposes, so the boot grace window keeps suppressing exactly the
false positive it was built for.

**C13 — readiness is scoped to the holder type, which is the question actually being asked.**
`ReconcileNowForHolderType` keeps only lenses whose spec declares the destroyed holder type in a Secure-Lens
column (read from `targetConfig.secureColumns[].holderTypes` — the author's own statement, not an inference
over compiled cypher). Corpus-global readiness let any single unactivatable lens anywhere withhold every
attestation for every holder type forever. Fail-closed at the edge: a spec that cannot be read or parsed has
**unknown** holder types and is kept, because unknown must never resolve to "not relevant".

**C12 — Ack-and-alarm stands, and the reason is a redrive path that already exists.** Naking forever would
wedge a strictly serial consumer on one holder and stop every later destruction — the failure B3 exists to
prevent. What makes the Ack safe is that a destruction is **operator-redrivable**: re-submitting
`ShredRetentionClassKey` for the same holder is idempotent, clears the prior cycle's finalization progress
(§4.3) and re-emits the event. The give-up log now names that action rather than only the fault.

**C11 — two bugs, not one.** `RebuildWait` is per target, so the handler's real bound was N × 30 min; a
single `HandlerBudget` now spans every rebuild of one destruction, and a target reached with nothing left is
skipped without attesting. And `AckWait` alone could not have worked: its clock runs from delivery into
nats.go's 500-message prefetch buffer, not from handler entry, so on the cold-boot `DeliverAll` replay this
fix was written for a queued destruction was redelivered before its own handler ran. `MaxPrefetch` (new on
`substrate.DurableConsumerConfig`, set to 1 here) is the other half.

**C6, C8, C9 — the same lesson three times: a doctrine that reached one site and not its siblings.**
`abandonRebuild` checks ownership before clearing pipeline-wide state (an older rebuild's failure was
un-suppressing the convergence sweep under a newer live rescan); the three status writers carry the five
projection-progress fields they were still zeroing — `LastProjectedAt` **permanently**, since the LagPoller
only ever writes a non-zero value; and both health providers transfer every field they already hold before
the pending read that can fail. C10's close moved inside the mutex.

**Minors** — the stale "read-free / checks the piiKey via kv.Read" text is corrected in the DDL field
description, `docs/components/privacyworker.md` and `docs/components/refractor.md`; the reporter's
could-not-read warning names what it now actually resets. Every test gap the acceptance layer named is
filled: the identity-plane actor pin has its own negative vector, `keyshredded`'s declared reads are
asserted, and `AckWait`/`MaxPrefetch` have direct plumbing tests (including that a zero value leaves
JetStream's default alone).

### 23.1 §22.5's owed verification — answered

Run on a quiet host, `internal/refractor` alone, two runs each side: **clean `main` fails 2 of 2 and the
branch fails 2 of 2**, with membership rotating across `RealClaimIdentityOp_E2E`,
`_WithEphemeralConsumer_E2E` and `_TwoActorClaimCeremony_MultiActorRace_E2E` — the same 25s convergence
assertion each time. A failure at `main` cannot be caused by code `main` does not carry, so the whole
claim-ceremony family is a **pre-existing intermittent at head**, not a reading on this increment. The
`lattice.md` row is widened from the single ephemeral sibling to the family. `internal/loom`'s
`TestSupervisor_RemovedDomainPauseDoesNotResurrectOnReAdd`, which failed once in the thrashing run, passes
**5/5** quiet on the branch and 3/3 at `main` — contention. `TestRefractor_E2E_P99` fails on both sides too,
but it asserts a 500ms p99 budget on a host sitting at ~11 GB of swap, which is the documented contention
condition rather than a defect worth a row.

## 24. Item 3b-ii — the third crop, and the CI gate it was hiding (2026-08-08)

The §23 closure was reviewed by three layers (security plane, edge-case, acceptance) against the
`4cfe1284..3b67472d` delta. The acceptance layer confirms **12 of 13** §22 findings genuinely closed by
non-tautological tests with **no scope drift**; §23's narrative holds claim-by-claim. Two blockers and one
major survived, all three converged on by more than one layer, and the first would have turned `main` red.

### 24.1 Blockers — closed

**B1 — C3's class recurred in a SECOND build-tagged e2e, and that one is in CI.**
`internal/cryptoshred/cryptoshred_test.go` carries `//go:build cryptoshred`, so `go test ./...` never
compiles it, but `make test-crypto-shred` runs in `.github/workflows/ci.yml`. Its harness seeds the privacy
service actor's **capability doc** and never its Core-KV **vertex**; once the submitters began declaring the
actor as a hydrated read, that declared-absent key fails the op closed —
`HydrationMiss: missingKey=vtx.identity.CSprivacyActKMNPQRST`, reproduced here before the fix. §22's C3 was
diagnosed as one file's bug; it was a class, and the sweep was aimed at the instance. The seeding mirrors
`record_shred_finalization_test.go`'s, and the gate is green.

**B2 — the holder-type filter resolved "cannot read a declaration" to NOT RELEVANT in one shape.**
`declaresHolderType` returned false when no `targetConfig` decoded, and `specReadable` only covered a spec
that failed to *parse*. A spec that parses as JSON but carries no target config — the shape `translateSpec`
itself rejects, so exactly the lens that never registers — was therefore dropped from the relevant set,
leaving readiness clean and the target set empty, which attests vacuously over rows that still hold the
plaintext. C13's narrowing turned a fail-closed corpus-global check into a fail-open one for that shape.
It is now `mayHoldHolderType`, which answers **unknown as yes** and says so at the top: no decoded
targetConfig, or a secure column with an empty `holderTypes` list (which `NewSecureDecryptor` refuses, so
that lens cannot activate either). A decoded targetConfig with no secure columns at all stays a genuine
no — a plain lens holds no ciphertext, and skipping those is what the narrowing is for. `specReadable`
disappears from the call site because it is now redundant rather than because it stopped mattering: an
unreadable spec leaves the probe zero-valued, and a zero probe is one of the unknown shapes.

### 24.2 The major — C6 was closed on the path that almost never runs

The per-rebuild-ownership doctrine reached `abandonRebuild` and stopped there. `watchRebuildCompletion` —
the path every rebuild takes — still cleared `rebuildInFlight` unconditionally in both its deferred exit and
its drained arm, so a watcher cancelled at shutdown, or one returning after a newer rebuild had begun,
un-suppressed the convergence sweep under a live rescan: verbatim the condition C6 was filed for. And
`abandonRebuild`'s own check was a TOCTOU — it took the lock twice, and the release it now precedes is what
frees a waiter to start the successor in between.

Both die the same way: **ownership, the flag and the release are one decision, so they happen in one hold of
`rebuildWatchMu`.** `endRebuild` tests ownership, clears the flag if it owns, closes the channel, and returns
what it found; `ownsRebuild` is gone. Clearing before the release is what makes the ordering total — a waiter
woken by the close must take that same lock in `beginRebuild` before it can set the flag again, so the
successor's `Store(true)` can no longer land before the predecessor's `Store(false)`.

**Accepted residual, recorded rather than papered over:** the health STATUS write is remote I/O and stays
outside the lock, so an older goroutine's `SetActive` can still land after a newer rebuild's
`SetRebuilding`. It is transient — the newer rebuild writes its own status when it drains — and the
load-bearing half, the flag the sweep reads, is ordered. Holding the mutex across a NATS round-trip to close
a cosmetic window is the worse trade.

### 24.3 Also closed

- **The probe's tests used a spec shape production never writes.** Both new holder-type tests wrote
  `targetConfig` at the top level, while every package-installed lens stores the `make_aspect` envelope with
  the spec under `data`. The `Data.TargetConfig` branch — the only one a live secure lens takes — had zero
  coverage, so a regression dropping it would make every secure lens read as declaring nothing. There is now
  an envelope-shaped fixture and tests for the live shape, plus B2's regression vector.
- **A test that could not catch its own revert.** `MaxPrefetch` is a client-side pull option and never
  appears in `ConsumerInfo`, so the test named for it asserted only `AckWait`. It is split: `AckWait` keeps
  the server-side assertion, and the cap is now asserted behaviourally — with one handler parked, the rest of
  the backlog must still be waiting on the server. Mutation-verified: with the cap disabled it reports
  `NumAckPending=3 NumPending=0` and fails.
- The stale `pipeline.ErrRebuildWaitTimeout` reference in the file that now defines the sentinel itself, and
  two comments narrating prior state rather than describing present behaviour (the CLAUDE.md rule).

### 24.4 Deliberately NOT closed, with the reason

- **§22.4's `ReconcileNow` cost** (a full `vtx.meta.*` scan per delivery). §23 claimed every §22.4 minor was
  closed; this one was never touched, and it should not be: freshness is the entire point of the readiness
  check, and a cached answer would report "ready" off a stale registry — the exact failure the check exists
  to prevent. A destruction is a rare event, so the scan is not on any hot path.
- **`ReconcileNow` has no production caller.** Kept as the unscoped form the narrowed one delegates to, and
  exercised by tests as the contrast that proves the narrowing is a narrowing.
- **The submit stage's delivery ceiling and the wait budget shared across `RebuildAndWait`'s two waits.**
  Both are fail-closed (they withhold the attestation) and both are bounded; neither is worth new state here.

### 24.5 Verification

Green on the branch: `go build`, `make vet`, `golangci-lint`, all eight `scripts/lint-*.go`,
`make test-crypto-shred`, `make test-system-actor-capability`, and
`internal/refractor/{pipeline,health,classkeyshredded,control,keyshredded}` + `internal/substrate` +
`internal/pkgmgr` + `packages/privacy-base`.

**`packages/privacy-base`'s six failures on this host are the 250ms Starlark wall, not this branch.** Each is
either `ScriptTimeout: script exceeded wall budget 250ms` at `InstallPackage`, or an op rejected for the same
reason — on a host sitting at ~12 GB of 13.3 GB swap. CI runs the suite with `PROCESSOR_SCRIPT_WALL_MS=5000`
precisely because of this, and the failing shape is the ★★★ row already filed
(*a class-(e) enumeration has no budget of its own — the 250ms wall binds first*). §23.1's finding stands
unchanged: the `internal/refractor` claim-ceremony family is a pre-existing intermittent at head.

### 24.6 Residual filed, not fixed — the oracle describes the lens NOW

Both halves of "which lenses must this destruction reach" answer from the lens's **current** declaration:
the readiness probe reads `targetConfig.secureColumns[].holderTypes` off the spec in Core KV, and the target
lister reads the running pipeline's `secureColumns`. The plaintext in a target store, though, was written
under whatever declaration was live **then**. So a package upgrade that narrows a lens's holder types — from
`["identity","retentionclass"]` to `["identity"]`, with a clean activation and no malformed spec anywhere —
puts its pre-upgrade ciphertext rows outside every destruction that follows: the target set is empty,
readiness is clean, and `manager.go`'s "an empty target set is vacuously clean and MUST still attest" fires
over rows that still render. The comment is right about the case it was written for (no lens ever declared
this holder) and now also covers one it was not (a lens that stopped declaring it).

Not fixed here because the fix is not a guard: it needs a record of what a lens's columns USED to hold, or a
sweep at the moment a declaration narrows — the same shape as the narrowing-healer question §4.2 already
raises for label sets. Filed to `lattice.md`, consumer named.

### 24.7 The closure's own re-review — no blockers, and what it corrected

The §24 delta went back through one focused adversarial pass (concurrency + security plane), because the two
prior closures each introduced a defect in exactly this code. **No blockers.** It confirmed the successor
race is genuinely closed — no lost `Store(true)`, no flag stuck true, no double-close, no waiter blocked
forever, no goroutine leak — and that both new test families pin distinct production lines. Six things were
worth fixing, and one of them is the kind this project cares about most:

- **The `endRebuild` doc asserted a guarantee the code does not give.** Ownership is `rebuildWatch == sig`,
  which names the most recently **begun** rebuild, not the set of rescans still **running**. `Rebuild` is
  fire-and-forget and skips `rebuildSerial`, so a second caller can begin and then abandon a rebuild while an
  earlier watcher is still polling a live rescan — and that abandon legitimately owns the flag. The ordering
  fix closes the *successor* race, not that one. The behaviour is pre-existing and bounded (the sweep is a
  healer; the attestation path reads `drained`, never this flag), but a comment telling the next reader the
  ordering is closed is what stops them looking. The claim is now scoped to what it earns, and names what
  closing the rest would take.
- **The probe's parse-success guard was free, and dropping it made correctness rest on `encoding/json`'s
  partial-decode behaviour.** A type error does not stop decoding, so a probe can be left holding a
  `targetConfig` that says "not this holder" while the field the error landed on is the one that would have
  said otherwise. Re-added: only a spec that decoded cleanly may exclude a lens.
- **Precedence between the two spec levels replaced with a union**, matching the `isEventStream` sibling —
  the loader picks between them by a different test (a top-level `cypherRule`), so a body carrying both would
  be read here from one level and projected there from the other. No producible spec has both; the cost is
  nothing and the divergence goes away by construction.
- **Both new ownership gates in `watchRebuildCompletion` would have survived their own revert.** No test
  drove that function at all. `TestEndRebuild_ANonOwnerReleasesItsWaitersWithoutClearingTheFlag` puts the
  test at the source every exit funnels through, rather than at any one caller.
- A comment of this fire's own making narrated a prior change ("the same defect the abandon path **was fixed
  for**") — the CLAUDE.md rule, caught on the fire that was correcting two others for it.
- The prefetch test nested a 5s poll inside the shared 10s connection context, which on a loaded host fails
  on `ctx` instead of reporting the counts. The poll owns its deadline now.

---

## 25. Fire 2 item 1 (Clinic) fire brief (build note, 2026-08-08)

Fire 1 is complete (§14). This is the **first consumer** of the custody plane: clinic's `.encounter` PHI
stops being plaintext in Core KV and becomes a retained record custodied on a `clinicalRecord` key holder.

### 25.1 Scope sentence (verbatim, §11 Fire 2 item 1)

> **Clinic.** Split `.encounter` into the sensitive PHI aspect (`Sensitive: true`,
> `custody.kind: retentionClass`, class `clinicalRecord`) and a non-sensitive operational sibling;
> re-point the three shipped lenses at the sibling (§9.1); declare the retention class;
> `clinicEncountersRead` protected Secure Lens with `HolderTypes: ["retentionclass"]`; the §6.4 obligation
> written into the class description. **Move `patientDemographics.fullName` onto the identity** (F3(b)) —
> without it §6.4's acceptance criterion is unmet, since the patient's name would survive their erasure in
> plaintext. **Plus the FE surface** that renders the note to the treating provider, absorbed from the
> struck verticals row.

### 25.2 Scope-diff gate — one correction to the ratified scope, narrowing nothing

**The scope sentence says "the three shipped lenses". There are FOUR consumer sites, across TWO packages.**
`packages/clinic-reminders/followups.go:274-285` (`followUpRemindersSpec`) reads
`a.encounter.data.followUpRequested` and `a.encounter.data.followUpDate` in five places — a RETURN pair, a
`freshUntil` CASE, and both the `missing_followup_reminder` and `violating` predicates. It is a *different
package* from the three clinic-domain lenses §9.1 censused. Leaving it un-re-pointed would silently disarm
every follow-up reminder (the predicates would compare against nulls), so it is in scope by the §9.1 rule
itself: *"before setting `Sensitive: true` on an existing aspect, census every lens that projects any field
of it."* The census was one package too narrow. Scope widens by one file; nothing is substituted or dropped.

### 25.3 Verified touch-list (every anchor re-checked live at `9fa06e10`)

**packages/clinic-domain**
- `ddls.go:921-954` — `appointmentEncounter` aspectType DDL. Today `Sensitive` unset, `PermittedCommands:
  ["RecordEncounter"]`, `InputSchema` carrying all six fields, `FieldDescription` for all six, one Example.
- `ddls.go:2824-2879` — `RecordEncounter` inside `patientDDLScript`'s sibling appointment vertexType script.
  Builds one `enc` map (`:2851-2871`) and upserts it at `:2875`:
  `make_aspect_upsert(appt_key, "encounter", "appointmentEncounter", enc)`.
- `lenses.go:476-498` — `clinicAppointmentsSpec` (unprotected NATS-KV): `a.encounter.data.documentedAt`,
  `.followUpRequested`, `.followUpDate`.
- `lenses.go:693-719` — `clinicAppointmentsReadSpec` (protected PG, patient-anchored): same three, aliased
  `documented_at` / `follow_up_requested` / `follow_up_date`.
- `lenses.go:728-753` — `providerAppointmentsReadSpec` (protected PG, provider-anchored): same three.
- `lenses.go:169-197` / `:222-250` — the two protected lens Go literals whose `Columns` carry
  `documented_at` / `follow_up_requested` / `follow_up_date`.
- `package.go:120-131` — `pkgmgr.Definition` literal. Sets Name/Depends/Version/Description/DDLs/Lenses/
  Permissions/WeaverTargets/OpMetas. **No `RetentionClasses` field today** (nil).
- `manifest.yaml:2` — version `0.28.19`.

**packages/clinic-reminders**
- `followups.go:274-285` — `followUpRemindersSpec`, five `a.encounter.data.*` references (see §25.2).
- `manifest.yaml` — version bump owed alongside.

**Platform API (shipped by Fire 1; re-verified, do not re-derive)**
- `internal/pkgmgr/definition.go:168` — `Definition.RetentionClasses []RetentionClassSpec`.
- `internal/pkgmgr/definition.go:783` / `:791` — `DDLSpec.Sensitive` / `DDLSpec.Custody`.
- `internal/pkgmgr/definition.go:836,840,845` — `CustodyKindRetentionClass = "retentionClass"`,
  `RetentionClassVertexType = "retentionclass"`, `RetentionPolicyEraseOnExpiry = "eraseOnExpiry"`.
- `internal/pkgmgr/definition.go:853-886` — `CustodySpec` / `RetentionClassSpec`.
- `internal/pkgmgr/custodyscope.go` — the install validations. **Rule 5 is NOT an availability gate**: it
  rejects `Kind: identity` that also names a class. `retentionClass` custody is installable end to end.
- `internal/processor/step6_validate.go:197-244` — the conditional anchoring rule. A `retentionClass`-
  custodied sensitive aspect may anchor on a NON-identity vertex; an undeclared-custody one may not.
- `internal/refractor/lens/corekv_source.go:839-906` — `validateSecureColumns`: protected-only, no
  grantTable, plain projection only, column must be declared, not an `IntoKey` column, no reserved names
  (`authz_anchors` / `projection_seq` / `is_deleted` / `deleted_at`), `HolderTypes` non-empty and each a
  Contract #1 type segment.

**The platform's own fixtures already pin this exact key shape** — `internal/processor/custody_test.go:113`
uses `vtx.appointment.<id>.encounter` as *the* retention-class positive vector, and
`internal/pkgmgr/custodyscope_test.go:237` uses `clinicalRecord` + `P7Y`. This fire makes the shipped
fixture real.

### 25.4 Precedents to mirror

- **Secure column on a protected lens:** `packages/clinic-domain/lenses.go:296-315` — `clinicPatientsRead`
  already projects `id.email.data AS email` / `id.phone.data AS phone` with
  `SecureColumns: [{Column: "email", HolderTypes: ["identity"], Field: "value"}, …]`. The cypher projects
  the *whole aspect `data` map* (the ciphertext envelope) and `Field` names the key inside the decrypted
  plaintext. A three-field PHI aspect therefore aliases `a.encounter.data` three times with three `Field`s.
  Second precedent: `packages/loftspace-domain/lenses.go:67-84`.
- **Provider-anchored protected lens:** `providerAppointmentsRead` (`lenses.go:222-250` +
  `:728-753`) — REQUIRED `withProvider` anchor walk, `authz_anchors = [nanoIdFromKey(pr.key)]`, and the
  existing `clinicProviderReadGrants` producer already grants it. `clinicEncountersRead` reuses that anchor
  and that grant producer verbatim; no new grant producer is needed.
- **Declaration-only aspect DDL:** `aspectDeclarationOnlyScript`, used by `appointmentEncounter` itself.

### 25.5 Increment order + green checks

**Inc A — the split + the custody declaration.**
`.encounter` KEEPS its localName and class and becomes the SENSITIVE half (`Sensitive: true`,
`Custody{Kind: retentionClass, RetentionClass: "clinicalRecord"}`), `data = {summary, assessment?, plan?}`.
A NEW `.documentation` aspect (class `appointmentDocumentation`, non-sensitive) takes
`{documentedAt, followUpRequested, followUpDate?}`. `RecordEncounter` writes both. All four consumer sites
re-point their operational reads to `.documentation`. `clinic-domain` declares the `clinicalRecord`
retention class with the §6.4 obligation in its description. Both package versions bump.
*Green:* `go test ./packages/clinic-domain/... ./packages/clinic-reminders/... ./internal/pkgmgr/...`.

**Inc B — `clinicEncountersRead`.** A protected Postgres Secure Lens, provider-anchored, projecting
`summary` / `assessment` / `plan` through `SecureColumns` with `HolderTypes: ["retentionclass"]`.
*Green:* `go test ./packages/clinic-domain/...`, `make verify-package-clinic` if it exists, plus the
lens-parse path.

**Inc C — `patientDemographics.fullName` → the identity** (F3(b)). Not in this fire; see §25.8.

**Inc D — the FE surface** rendering the note to the treating provider. Not in this fire; see §25.8.

*Whole-fire gates:* `go build ./...`, `make vet`, `golangci-lint run ./...`,
`STRICT=1 go run ./scripts/lint-conventions.go`, `STRICT=1 go run ./scripts/lint-board.go`,
`go run ./scripts/lint-package-version.go`.

### 25.6 In-scope gotchas

1. **`.encounter` keeps the name; the SIBLING is new.** The §6.4 acceptance table names
   `vtx.appointment.<A>.encounter` as the *PHI, custody `retentionClass`* row, and three shipped platform
   test fixtures pin that key. Inverting it (operational keeps `.encounter`) would falsify all of them.
2. **`followUpDate` is load-bearing for a Weaver temporal lane.** `followUpRemindersSpec`'s `freshUntil`
   arms an `@at` timer at it. Re-pointing must preserve the null-safety of every one of the five
   references, including inside the `CASE` and both boolean predicates.
3. **`documentedAt` is the "visit documented" presence signal** the FE renders (`app.js:3573-3585`,
   `encounterSummary`). It moves to `.documentation`; the FE reads it from the read model, not the aspect,
   so no FE change is owed in Inc A — the column names are unchanged.
4. **Pre-split corpus.** Existing `.encounter` rows on a running stack hold all six fields *plaintext*
   under a now-sensitive DDL. Their operational fields project null (the `.documentation` aspect does not
   exist for them) until re-documented, and the new secure lens's per-row failure path (Fire 1 item 3b-ii)
   projects null + a privacy-tier alarm rather than wedging. §11 budgets "one full-stack reset" for exactly
   this; a reset is Andrew's to run and is NOT this fire's to force on a shared stack.
5. **The op writes two aspects in one batch.** Step 6 validates per mutation and step 6.5 encrypts only the
   sensitive one; the non-sensitive sibling passes through untouched.
6. **`clinicalRecord`'s DEK is minted once per batch, on the holder** — never on the appointment
   (`custody_test.go`'s two assertions). Nothing in this fire may mint a key on an anchor.

### 25.7 Non-goals (the drift fence)

Lease-signing (Fire 2 item 2). Any change to the erasure plane, the shred workers, or `ShredIdentityKey`.
Widening the egress path to retained refs (deferred tail (a)). Period bucketing (tail (b)). A purpose-gated
retained reveal (tail (c)). Any change to the three grant producers or to RLS.

### 25.8 Adjacent finds — filed now, not carried

- **`clinic-reminders` was outside §9.1's census** (§25.2). Recorded here rather than filed, because this
  fire fixes it. The *generalized* lesson — a sensitivity flip must census every package, not every lens in
  the declaring package — is amended into §9.1 by this commit.
- **Inc C and Inc D remain open** and are what keeps item 1 from closing: without Inc C the §6.4 acceptance
  criterion is unmet for clinic (the patient's `fullName` survives their erasure in plaintext on
  `vtx.patient.<id>.demographics`). Inc C carries two live obstacles the ratified text does not mention,
  both verified at `9fa06e10` and both real design work rather than a mechanical move:
  (i) the `identifiedBy` patient→identity link is **OPTIONAL** (`ddls.go:1087-1092` — wired only when
  `CreatePatient` is given an `identityKey`), so a patient with no identity would have no name at all, and
  `clinicPatientsReadSpec`'s ghost-vertex filter `WHERE p.demographics.data.fullName <> null`
  (`lenses.go:658-659`) would drop every such patient from the roster; and (ii) **no operation writes
  `.name` on an existing identity** — `identity-domain` writes it only at `CreateUnclaimedIdentity`
  (`ddls.go:1176-1178`), so `CreatePatient`'s `fullName` has nowhere to land for an already-provisioned
  identity. These are named in the checkpoint as Inc C's first two questions.

---

## 26. Fire 2 item 1 (Clinic) — Inc A built and reviewed (2026-08-08, `ae9c6411`)

### 26.1 What landed

The split, the custody declaration, and the four re-points, exactly as §25.5 Inc A scoped them.
`.encounter` keeps its localName and class and is now `Sensitive: true` with
`Custody{retentionClass, "clinicalRecord"}`, holding `{summary, assessment?, plan?}`. A new
`.documentation` aspect (class `appointmentDocumentation`, non-sensitive, same `PermittedCommands`) holds
`{documentedAt, followUpRequested, followUpDate?}`. `RecordEncounter` writes both in one batch — one
`required_string(p, "summary")` failure point, before either dict is built, so no path writes one aspect
without the other. `clinic-domain` declares the class in a new `retention.go` (`RetentionClasses()`), 0.29.0;
`clinic-reminders` 0.8.0.

`verify-package-clinic-domain.go` gained the `appointmentDocumentation` DDL check and, more importantly, an
assertion over the whole custody chain: the holder vertex, its `.retentionPolicy` body, `.sensitive`, and a
`.custody` whose `holderKey` names that same holder. A diff-apply that installs the DDLs but drops the holder
would otherwise install clean and then fail every `RecordEncounter` at commit.

### 26.2 The scope correction, restated as a finding

§9.1's census was package-scoped and missed `clinic-reminders`' `followUpRemindersSpec`, which reads the
follow-up fields in **five** places — two RETURN aliases, the `freshUntil` CASE that arms a Weaver `@at`
timer, and both gap predicates. §9.1 is amended in place (2026-08-08): the census is repo-wide and covers
WHERE / CASE / predicates, not just RETURN clauses.

### 26.3 Review (3-layer: opus security plane · edge-case · acceptance) — disposition

**Fixed in-fire, because this fire wrote it.** The `clinicalRecord` Description claimed the record survives
"as a PSEUDONYMIZED retained record". It does not: `vtx.patient.<id>.demographics.fullName` is plaintext,
outside the identity `ShredIdentityKey` reaches, and projects as `patient_name` onto the same read-model rows
that carry the visit — so a shredded patient's retained record is still identified. That Description is
projected into the **unprotected** `retentionKeyStatus` lens, i.e. it is the operator's compliance surface,
so the overclaim would have been the record. It now states the gap and names Inc C as what closes it. The
same claim in `package.go`'s doc comment, and the stale "right-to-be-forgotten … remain deferred" paragraph
in `manifest.yaml`, were corrected with it, along with README drift in both packages.

**Filed, because the mechanism is platform-side and pre-existing.** Step 6.5 `continue`s past every mutation
it cannot adjudicate, and there are **two** such arms, not one. The board already carried the empty-`class`
arm; the sibling is `step65_encrypt.go:83`'s `if !ok || !ref.Sensitive`, where a DDL absent from the cache
**with no live-read fault** reads as "not sensitive" and commits plaintext. The function's own comment
defends the *faulted* empty resolution against exactly this and leaves the unfaulted miss falling through.
Reachable via `ddl_cache.go`'s warn-and-skip on a transient KV read error (`Refresh`) and
`step8_commit.go:335`'s warn-only `Invalidate` failure. The existing row is widened to name both arms and
its consumer marked SHIPPED. Also filed: `internal/pkgmgr/manifest.go`'s `ManifestBlock` cross-checks every
declared entity except `RetentionClasses`, so clinic-domain now mints a holder its own manifest never
declares.

**Reconciled, not filed.** The "renamed or uninstalled retention class strands its DEK" row acquires its
first live subject — clinic-domain's `clinicalRecord` — and its row now says so. Its mechanism is unchanged:
`<holder>.piiKey` is not in `declaredKeys`, so uninstall tombstones the holder and leaves the DEK live and
undestroyable, with `ShredRetentionClassKey` refusing on the tombstoned holder.

**Deliberately not filed — covered by §11's budgeted reset.** An `.encounter` written before this declaration
holds all six fields plaintext under what is now a sensitive DDL. Nothing re-encrypts it, so
`ShredRetentionClassKey` does not reach it, and its appointment reads as undocumented (no `.documentation`
sibling) until a provider re-documents — which heals it, since the upsert is unconditioned. §11 budgets one
full-stack reset for exactly this, the demo box resets nightly, and no non-resettable deployment exists. The
class Description now states the limit rather than leaving it to the design doc. **If a deployment ever
outlives a reset, this becomes a real row — it is a premise, not a permanent property.**

**Confirmed sound, so a later fire does not re-litigate it.** The custody wiring itself: install-time
`validateCustodyScope` accepts this declaration and rejects each of its malformed neighbours; commit-time,
a non-identity anchor is permitted *only* for kind `retentionClass` and only when the install-resolved
holder key parses as `vtx.retentionclass.<NanoID>`; `keyHolderFor` has no fallback to identity derivation;
a shredded `.piiKey` fails closed *before* the empty-WrappedDEK check, so there is no key-resurrection path;
the DEK is minted once per batch on the holder and never on the anchor. No lens, FE, Weaver target, Loom
pattern, bridge adapter or notification path reads `.encounter` content, and the emitted
`clinic.appointmentEncounterRecorded` event carries only `{appointmentKey}` and has no consumer anywhere.
Nothing PHI-derived reaches the `doc` dict — no length, hash, substring or content-dependent branch. Both
protected lenses' `authz_anchors` expressions are byte-identical across the re-point, so no row moved into
or out of any actor's visibility.

### 26.4 Checkpoint — what remains of item 1

Increments land on `main` individually (§14), so a resuming fire opens a **fresh** worktree from `main`
rather than reattaching to one — Inc A's is removed and its branch deleted.

- **Inc B — `clinicEncountersRead`.** A protected Postgres Secure Lens, provider-anchored, projecting
  `summary` / `assessment` / `plan` via `SecureColumns` with `HolderTypes: ["retentionclass"]`. Mirror
  `providerAppointmentsRead` for the REQUIRED `withProvider` anchor walk and
  `authz_anchors = [nanoIdFromKey(pr.key)]` — its `clinicProviderReadGrants` producer already grants that
  anchor, so no new producer is owed. Mirror `clinicPatientsRead` (`lenses.go:296-315`) for the secure-column
  mechanics: the cypher projects the whole aspect `data` map and `Field` names the key inside the decrypted
  plaintext, so three PHI fields means aliasing `a.encounter.data` three times with three `Field`s.
  `validateSecureColumns` requires protected, no grantTable, plain projection, declared columns, non-`IntoKey`
  columns, no reserved names.
- **Inc C — `patientDemographics.fullName` onto the identity** (F3(b)). This is what makes §6.4's acceptance
  criterion true for clinic, and it is not the mechanical move the ratified text implies. Two obstacles,
  both verified live: (i) `identifiedBy` is **OPTIONAL** (`ddls.go:1087-1092`), so a patient with no identity
  would have no name at all, and `clinicPatientsReadSpec`'s ghost-vertex filter
  `WHERE p.demographics.data.fullName <> null` (`lenses.go:658-659`) would drop every such patient from the
  roster — the filter needs a new subject as much as the name needs a new home; (ii) **no operation writes
  `.name` on an existing identity** — `identity-domain` writes it only at `CreateUnclaimedIdentity`
  (`ddls.go:1176-1178`), so `CreatePatient`'s `fullName` has nowhere to land for an already-provisioned
  identity. Four lens sites read `fullName` today: `clinicPatients` (filter), `clinicPatientsRead` (filter +
  `name`), `clinicAppointmentsRead` and `providerAppointmentsRead` (`patient_name`). The last three can take
  it as a secure column off the linked identity, exactly as they already take `email`/`phone`; the first is
  an unprotected NATS-KV lens and **cannot** — a plain lens cannot project a sensitive aspect — so
  `clinicPatients` loses the name outright and its consumers must be censused before the move, not after.
- **Inc D — the FE surface** rendering the decrypted note to the treating provider (`cmd/clinic-app/web`,
  the existing encounter modal at `index.html:437-469` writes but cannot read back). Depends on Inc B.

### 26.5 Proven on the running stack (2026-08-08, after `make refresh-clinic`)

Package tests prove the write path in process; this is the same claim against the live stack, because a
declaration that installs is not the same as a record that encrypts.

`make verify-package-clinic-domain` — 370 assertions, all passing, including the new chain: the
`clinicalRecord` holder vertex exists (`vtx.retentionclass.7hRMjLYwg6WSpaXj7hRM`), its `.retentionPolicy`
declares `clinicalRecord` / `eraseOnExpiry`, `appointmentEncounter`'s `.sensitive` is `true`, and its
`.custody` names that holder.

A real `RecordEncounter` submitted through `ops.default` with a sentinel PHI string committed three keys:
`.encounter`, `.documentation`, and **`vtx.retentionclass.<H>.piiKey`** — the DEK minted on the holder.
Reading them back:

- `.encounter.data` = `{ct, nonce, keyId}` with `keyId = vtx.retentionclass.7hRMjLYwg6WSpaXj7hRM`. The
  ciphertext names the retention class, not the patient's identity, which is the entire point.
- `.documentation.data` = `{documentedAt, followUpRequested, followUpDate}` in plain text, and nothing else.
- **No `.piiKey` on the appointment and none on the patient** — custody did not fall back to the anchor or
  to an identity.
- The sentinel string appears in no key under either the appointment or the patient subtree.
- `read_clinic_appointments` projects `documented_at` / `follow_up_requested` / `follow_up_date` for that
  appointment, so the re-point resolves through Refractor against the real `.documentation` aspect.

The stack carries one proof appointment (patient "Retention Proof") — it held no clinic data before this.
