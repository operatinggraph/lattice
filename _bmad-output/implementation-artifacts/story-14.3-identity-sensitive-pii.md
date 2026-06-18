# Story 14.3 — Identity sensitive PII aspects (`ssn` / `dob`, `sensitive: true`, identity-anchored)

**Status:** done
**Epic:** 14 — Loftspace Lease-Application Reference Vertical
**Tier:** Opus — a **package-content + a thin pkgmgr-plumbing** change on the **security/privacy plane** (NFR Privacy / NFR-S3). It ships **no engine change** and (by design intent) **no step-6 validator change**. The risk is **not** size; it is that this is the **PII / crypto-shred boundary**, and the change must make `ssn`/`dob` *actually* `sensitive: true` **on the real install path** (not just in prose) so the **existing** MutationBatch validator anchors them to `identity` for free. Review: **full 3-layer adversarial** (Blind Hunter / Edge Case Hunter / Acceptance Auditor) per `bmad-code-review` — a sensitive-data-boundary change is exactly what three independent lenses catch (the Acceptance Auditor against the 2 ACs + arch Item 6 + PRD §358; the Edge Case Hunter on the install-path round-trip + the format-validation rejections + the "sensitive aspect on a non-identity vertex" negative; Blind Hunter on the diff). Plus the gates in §8.
**Epic spec:** `_bmad-output/planning-artifacts/epics/phase-2-epics.md` → "Story 14.3: Identity sensitive PII aspects" (lines ~703–716) + the Epic 14 framing (~662–671) + the build order (14.1, 14.2, 14.3 → 14.4 → 14.5). Read it for the user-story framing and the **two** ACs (verbatim in §1).
**Binding grounding (FROZEN / OWNED — read, build TO, do NOT edit):**
- **Arch Item 6 — Aspect-Level Sensitivity Boundary** (`_bmad-output/planning-artifacts/lattice-architecture.md` lines ~1007–1020). The load-bearing parts: entire aspect marked sensitive via DDL **`"sensitive": true` at the aspect-type level** (NOT property-level); a DDL meta-vertex *for an aspect type* includes `"sensitive": true`; step 6 (Validate MutationBatch) checks a sensitive aspect's target vertex **is an identity vertex**; crypto-shredding operates at the **aspect** level; "if some properties of an aspect are sensitive and others aren't, they should be separate aspects." **This is why `ssn` and `dob` are TWO separate aspect-types, not one `pii` aspect.** Planning artifact — do **not** edit it.
- **PRD §358** (`_bmad-output/planning-artifacts/prd.md` line ~358, the erasure-classification table). The first row is the exact 14.3 vocabulary: *"Applicant name, address, **government ID**, income documents → `sensitive: true` aspects → Key shredded → irrecoverable."* "Government ID" is the SSN; DOB is applicant PII in the same class. §366 ("DDL design contract"): *"the application must correctly designate fields … personal identifiers in sensitive aspects … The Loftspace reference app DDL exemplifies this contract."* **14.3 is that exemplar for `ssn`/`dob`.** Planning artifact — do **not** edit.
- **Contract #1 §1.1** (`docs/contracts/01-key-shapes.md`) — aspects are the 4-segment `vtx.identity.<id>.ssn` / `vtx.identity.<id>.dob` (CLAUDE.md house rule; never an `asp.*` prefix). The `<id>` is a NanoID. **FROZEN.**
**Grounding (the code you build on — read; the package code is yours to change, the engine/validator are NOT):**
- **The EXISTING enforcement you ride (do NOT change it):** `internal/processor/step6_validate.go` — `validateOne`, the `sensitiveAspectScope` constraint (~125–147): for an **aspect** mutation, if the aspect's DDL `ref.Sensitive` is true and `parentType != "identity"`, return `DDLViolation{ViolatedConstraint: "sensitiveAspectScope"}`. **This is generic — it keys off `ref.Sensitive` for the aspect's *class*, never a hardcoded `ssn`/`dob`/`name`.** Read it to confirm the invariant: 14.3 makes `ssn`/`dob` *carry* `ref.Sensitive == true`, and the existing rule anchors them for free.
- **How `ref.Sensitive` is populated (the plumbing you EXTEND):** `internal/processor/ddl_cache.go` (~228–245) — the DDL cache sets `ref.Sensitive` from a `<root>.sensitive` aspect on the DDL meta-vertex **or** from the meta-vertex root `data.sensitive` bool, keyed by the aspect's **canonicalName (= the aspect's `class`)**. So a sensitive aspect-type needs a **DDL meta-vertex named for that class** (`ssn`, `dob`) carrying `sensitive: true`.
- **The identity DDL you extend:** `packages/identity-domain/ddls.go` — the single `identity` `meta.ddl.vertexType` DDLSpec (Description ~23–31, InputSchema ~33–42, FieldDescription ~45–55, Examples ~56–69) + the Starlark `identityDDLScript` (~77–321; `execute` dispatch ~111). The script today writes aspects with `class: "name"`, `class: "email"`, `class: "phone"`, `class: "state"`, `class: "claimKey"`, `class: "credentialBinding"` (see `CreateUnclaimedIdentity` mutations ~192–220 and `ClaimIdentity` ~289–304). **You add a write path for `ssn`/`dob` aspects + the aspect-type DDLs that make them sensitive.**
- **The pkgmgr install machinery (Item A target):** `internal/pkgmgr/definition.go` `DDLSpec` (~197–230) and `internal/pkgmgr/build.go` (the DDL self-description validation ~74–88 and the DDL meta-vertex + aspect emission ~90–127; `class := d.Class` ~93–95 already honors a non-default `Class`). **`DDLSpec` has no `Sensitive` field and `build.go` emits no `.sensitive` aspect — that is the gap §0 explains.**
**Depends on:** **identity-domain (exists)** — the `identity` vertex type + DDL + state machine + install seam. **11.1a (DONE)** — the `InstallPackage` seam that lands the DDL meta-vertices + their aspects (the path the new aspect-type DDLs ride). **Independent of 14.1 / 14.2** (those are the `service` instance + the Refractor key-column; 14.3 touches neither).
**Forward (note, do NOT build — §5):** **14.4** ships the `leaseApplicationComplete` actorAggregate lens that **reads** these `ssn`/`dob` aspects as bgcheck inputs (and the `applicant` body column). **14.3 only needs them DECLARED + WRITABLE + sensitivity-ENFORCED + anchoring-PROVEN.** Do **NOT** build the lens, the `service` instance, the bgcheck `externalTask`, or any read-path.
**Workflow:** you are the DS (dev) sub-agent. Repo root, no worktree. Do **NOT** commit/push/branch. Do **NOT** edit frozen contracts (`docs/contracts/*`) or planning artifacts (`epics/*.md`, `lattice-architecture.md`, `prd.md`, `data-contracts`, the change proposals). New docs/notes go in the **package README** or `/docs`, never `_bmad-output/`. A genuine frozen-contract gap → `cmd/<area>/CONTRACT-AMENDMENT-REQUEST.md` + flag at the TOP of your closing summary; do **not** edit the contract. Leave all changes in the working tree for Winston.

> **TOP-OF-STORY FLAG — read before you start; it overturns one premise in the launch framing.**
> The launch framing says *"the identity DDL ALREADY declares sensitive aspects (name, email, phone …) — 14.3 just extends the proven pattern,"* and *"the design intent is zero validator change."* **The validator part is correct and holds (§3). The "already declared / proven" part is only HALF true on the real install path, and §0 is the most important thing in this brief.** The current "sensitive" declarations for name/email/phone live **only in the `identity` DDL's prose Description string** — they are **NOT** structured `sensitive: true` aspect-type DDLs. There is **no** `meta.ddl.aspectType` for `name`/`email`/`phone`/`ssn`/`dob` shipped by the package, `pkgmgr.DDLSpec` has **no `Sensitive` field**, and `build.go` writes **no `.sensitive` aspect**. The `sensitiveAspectScope` enforcement is exercised today **only by synthetic hand-seeded DDLs in `internal/processor` + `internal/bypass` tests** (`vtx.meta.email` with `data.sensitive=true`, `sensitiveNote`, `bypassnote`), **never by the shipped identity-domain package**. So to make `ssn`/`dob` *actually* `sensitive: true` on the real install path — which AC #2 ("the validator enforces identity-anchoring") **requires** — 14.3 must **ship `meta.ddl.aspectType` DDLs for `ssn` and `dob` carrying a sensitive flag**, which means **adding a `Sensitive` field to `pkgmgr.DDLSpec` + emitting a `.sensitive` aspect in `build.go`** (Item A). **This is additive, opt-in pkgmgr plumbing — NOT a validator change** (the validator already reads `ref.Sensitive`; §3). **No frozen contract changes; no CONTRACT-AMENDMENT-REQUEST is anticipated** (arch Item 6 already specifies the aspect-type `sensitive: true` shape; the contract is the data-contract for the meta-vertex, and a `.sensitive` aspect on a DDL meta-vertex is the shape the DDL cache *already reads*). If you find a genuine frozen-contract gap (e.g. the package-install contract `docs/contracts/08-package-install.md` forbids a non-vertexType DDL or a `.sensitive` aspect), flag it; do not edit the contract. **See Q1/Q6 for the disposition + the considered alternatives.**

---

## 0. THE HEADLINE — make `ssn`/`dob` *structurally* `sensitive: true` on the REAL install path, then the EXISTING validator anchors them for free (read first; it governs everything)

This is the one thing to get right. Three facts collide:

1. **The validator anchors by the aspect's CLASS, via `ref.Sensitive`.** `step6_validate.go:128-147`: for an aspect mutation, it does `ref, ok := v.DDLs.Lookup(class)` where `class` is the **aspect document's `class`** (e.g. `"ssn"`), then — only if `ref.Sensitive` — requires `parentType == "identity"`. The rule is **generic**: it never names `ssn`/`dob`/`name`. **An aspect is anchored iff its class's DDL says `sensitive: true`.**
2. **`ref.Sensitive` comes from a DDL meta-vertex named for the aspect's class.** `ddl_cache.go:228-245` reads `ref.Sensitive` from `<root>.sensitive` aspect **or** root `data.sensitive`. The DDL is found by `Lookup(class)` → canonicalName. So `ssn` needs a DDL whose **canonicalName is `ssn`** and which carries `sensitive: true`. Same for `dob`.
3. **The identity-domain package ships NO such DDL today.** It ships exactly one DDLSpec — `identity` (`meta.ddl.vertexType`). The aspects it writes (`class:"name"`, `class:"email"`, …) have **no DDL** → `Lookup` misses → **permissive default** (Contract #1 §1.5/§1.6) → **they are NOT enforced as sensitive on the real path.** The "name/email/phone are sensitive" claim is **prose in the `identity` Description string** + **synthetic test fixtures**, not shipped structure.

**The resolution (BINDING):** 14.3 ships **two `meta.ddl.aspectType` DDLs** — `ssn` and `dob` — each carrying **`sensitive: true`**, declared in `packages/identity-domain/ddls.go` and installed through the **real** `InstallPackage` path. Once installed, the DDL cache loads `ref.Sensitive == true` for classes `ssn` and `dob`; the identity DDL's Starlark writes the `ssn`/`dob` aspects with those classes onto `vtx.identity.<id>`; and the **existing, unchanged** `sensitiveAspectScope` rule (a) **permits** them on identity and (b) **rejects** them on any non-identity vertex — for free. **The validator is not touched.** The work is: a `Sensitive` field on `pkgmgr.DDLSpec` (the ONE structural gap), the `.sensitive` aspect emission in `build.go`, the two aspect-type DDLSpecs, the Starlark write op, format validation, and the tests that prove all four bullets of §1.

> **Why ship aspect-type DDLs (the structural path) rather than just adding prose to the `identity` Description (the "extend the proven pattern" reading)?** Because **prose is not enforced**. The launch framing's "extend the proven name/email/phone pattern" is satisfiable two ways: (a) the *cosmetic* way — add "ssn (sensitive), dob (sensitive)" to the `identity` DDL Description string, matching exactly what name/email/phone do today; or (b) the *structural* way — ship `sensitive: true` aspect-type DDLs so the validator actually anchors them. **AC #2 says "the MutationBatch sensitive-aspect validator enforces identity-anchoring" — that is only true under (b).** Doing (a) alone would be a **completion-lie**: `ssn`/`dob` would *look* sensitive in the docs but a sensitive `ssn` written onto a `vtx.lease.<id>` would **pass** the validator (permissive default), violating the privacy boundary the AC demands. **§6 test 4 (the negative on a non-identity vertex, through the real installed DDLs) is the trap that catches (a)-only.** Adopt (b). (This also retro-actively fixes the same latent gap for name/email/phone if you choose to ship their aspect-type DDLs too — **out of scope, see Q5**; 14.3 ships only `ssn`/`dob`.)

Everything else (the `pkgmgr.DDLSpec.Sensitive` field, the `build.go` `.sensitive` aspect, the Starlark `RecordIdentityPII` op, the format checks, the tests) is scaffolding that makes this one structural fact real and enforced.

---

## 1. The two ACs (verbatim) + adjudication

### The ACs (from `phase-2-epics.md` ~709–714)

> **Given** the identity domain
> **When** the package declares applicant SSN / DOB
> **Then** they are **separate `sensitive: true` aspect-types on the identity** (`vtx.identity.<id>.ssn`, `vtx.identity.<id>.dob`), extending the proven name/email/phone pattern
> **And** the MutationBatch sensitive-aspect validator enforces **identity-anchoring** (Contract Item 6)

### Adjudication — what each AC binds

- **AC #1 → §2 Items A–D (declare + write).** "Separate `sensitive: true` aspect-types" = **two** aspect-type DDLs (`ssn`, `dob`), each `sensitive: true` (arch Item 6: separate aspects, not one `pii` blob). "On the identity" = written as 4-segment aspects `vtx.identity.<id>.ssn` / `vtx.identity.<id>.dob` (D5: in **aspects**, not vertex-root `data`). "The package declares … SSN / DOB" = the `identity`-domain package, via a Starlark write op (§2 Item C, the new `RecordIdentityPII` op — see Q1), format-validates and writes them. "Extending the proven name/email/phone pattern" = same aspect shape (`class`/`vertexKey`/`localName`/`data.value`) the existing aspects use — **and** (the part the framing under-states, §0) the same *structural* `sensitive: true` declaration, which name/email/phone do **not** currently have on the install path but `ssn`/`dob` now will.
- **AC #2 → §3 (the validator, UNCHANGED) + §6 test 4 (the negative proof).** "The MutationBatch sensitive-aspect validator enforces identity-anchoring (Contract Item 6)" = the **existing** `sensitiveAspectScope` rule (`step6_validate.go:128-147`), with **zero change**, must (a) **permit** a sensitive `ssn`/`dob` on `vtx.identity.<id>` and (b) **reject** a sensitive `ssn`/`dob` on any non-identity vertex with `DDLViolation{ViolatedConstraint: "sensitiveAspectScope"}`. This holds **only because** 14.3 makes `ref.Sensitive == true` for `ssn`/`dob` (§0). **If you conclude the validator needs ANY change for `ssn`/`dob`, that is a RED FLAG — stop and call it out as an Open Question; the design intent is zero validator change (§3 / Q4).**

### The two Epic-13/14 invariants on this AC (Andrew, 2026-06-18; epics ~579–581 — they apply to Epic 14 too)

- **(a) type-agnostic engines.** Already proven in Epic 13 via a non-`service` fixture. For 14.3: the sensitivity **enforcement is generic in step 6** — it keys off `ref.Sensitive` for the aspect's class, with the **only** concrete-type coupling being the **"must be identity" rule, which IS the privacy boundary** (arch Item 6 specifies it). **Nothing new to prove engine-side, and you must NOT add any `ssn`/`dob`/`identity` literal to `internal/processor` or `internal/pkgmgr` engine code** (the `Sensitive` field + `.sensitive` aspect are generic; the concrete `ssn`/`dob` names live **only** in the `packages/identity-domain` content). State this in the summary so a reviewer does not flag a "type leak." (The `parentType != "identity"` check already in `step6_validate.go:138` is the pre-existing privacy boundary — you do not add it, you ride it.)
- **(b) D5 — directly in play.** The PII lands in **aspects** (`vtx.identity.<id>.ssn`, `.dob`), NOT in the identity vertex root `data` — this **IS** the D5 pattern (minimum data in vertex root, business/PII data in aspects). The identity vertex root `data` stays minimal (today it is `{}` — see `CreateUnclaimedIdentity` mutation ~193–194; the `RecordIdentityPII` op must **not** add anything to the vertex root). **§6 tests must assert `ssn`/`dob` are written as aspects AND the identity vertex root `data` stays unchanged (minimal).** This is the headline D5 assertion of the story.

### Scope boundary

**In scope:**
1. **`Sensitive` field on `pkgmgr.DDLSpec` + `.sensitive` aspect emission in `build.go`** (§2 Item A) — the ONE structural plumbing gap (§0). Additive, opt-in, default-`false`; a DDL that omits it behaves exactly as today (no `.sensitive` aspect emitted → `ref.Sensitive` stays false → permissive). Generic — no `ssn`/`dob` literal.
2. **Two `meta.ddl.aspectType` DDLSpecs — `ssn` and `dob`** (§2 Item B) — declared in `packages/identity-domain/ddls.go` `DDLs()`, each `Class: "meta.ddl.aspectType"`, `Sensitive: true`, with the required self-description fields (InputSchema/OutputSchema/FieldDescription/Examples — `build.go:74-88` requires them for *every* DDLSpec; see Q3 for the minimal-but-valid shape) and a minimal/no-op `Script` (an aspect-type DDL is a *declaration*, not an executable op handler — see Q3).
3. **A Starlark write op on the `identity` DDL** (§2 Item C) — a **new** `RecordIdentityPII` op (Q1) that writes `ssn` + `dob` sensitive aspects onto an **existing** `vtx.identity.<id>`, format-validated. Added to the `identity` DDL's `PermittedCommands`, the `execute` dispatch, the InputSchema/FieldDescription/Examples, and the Description.
4. **Format validation** (§2 Item D) — SSN and DOB validated in the Starlark op (fail-closed `InvalidArgument`, mirroring the existing `claimKeyHash`/`name` checks). SSN: 9 digits (accept `###-##-####` or `#########`, normalize to digits — Q2). DOB: ISO `YYYY-MM-DD` (Q2). Reject malformed.
5. **Tests** (§6) proving: (a) `ssn`/`dob` are `sensitive: true` (the installed DDL cache has `ref.Sensitive==true` for both classes); (b) identity-anchoring is enforced by the **existing** step-6 validator, **including a NEGATIVE test that a sensitive `ssn`/`dob` on a NON-identity vertex is rejected** with `sensitiveAspectScope`; (c) the write op writes them correctly as **aspects** (root `data` minimal — D5) + **rejects bad formats**; (d) the install-path round-trip (the `Sensitive` field reaches the DDL cache through real `InstallPackage`).
6. **README/DDL doc** — update `packages/identity-domain/README.md` (the DDL contents list + a note that `ssn`/`dob` are `sensitive: true` aspect-types per arch Item 6 / PRD §358). New doc → README or `/docs`, never `_bmad-output/`.

**Out of scope (do NOT build — later/other stories):**
- **NO Vault/KMS encryption or crypto-shredding wiring.** Phase 1 = the **`sensitive` MARKER only** (arch Item 6: the marker; Refractor's Secure Lens + Vault is a separate Phase-1/Phase-2+ concern, lattice-architecture Item 5 ~990–1005). Do **NOT** implement encryption, a `ShredKey` op, key management, or the Secure Lens adapter.
- **NO `serviceAccess` / `cap.svc` read-path auth.** Deferred to Phase 3 (charter; epics ~684). 14.3 declares + writes + anchors; it does not gate reads.
- **NO change to the step-6 validator** (`internal/processor/step6_validate.go`). AC #2 requires NONE — the rule is generic and already correct (§3). **A validator change is a red-flag Open Question (Q4).**
- **NO change to FROZEN contracts** (`docs/contracts/*`). A genuine gap → CONTRACT-AMENDMENT-REQUEST, not an edit. None anticipated (top-of-story flag).
- **NO retro-fit of `name`/`email`/`phone`/`claimKey`/`credentialBinding` to structured aspect-type DDLs.** The same latent gap (§0) applies to them, but fixing it is **out of scope** — 14.3 ships only `ssn`/`dob` (Q5). Flag the broader gap in the summary; do not widen the diff.
- **NO `leaseApp` / `service` / lease-signing content, NO convergence lens, NO bgcheck `externalTask`.** That is **14.4** (§5). 14.3 makes `ssn`/`dob` declared + writable + anchored; it does not read them.
- **NO new `OperationReply` fields, NO Processor pipeline change.** The `RecordIdentityPII` op rides the existing DDL-script → step-6 → commit path unchanged.

---

## 2. The mechanism — item-by-item (DS builds to THIS)

The change has one structural move (make `ssn`/`dob` carry `sensitive: true` on the install path) + one content move (a write op that produces those aspects, format-validated). Mirror the **existing** patterns at every layer: the existing aspect-DDL `sensitive` shape the DDL cache reads (`ddl_cache.go:228-245`), the existing `DDLSpec` field-handling in `build.go`, and the existing Starlark op structure (`CreateUnclaimedIdentity` / `ClaimIdentity`).

### Item A — the `Sensitive` field on `pkgmgr.DDLSpec` + the `.sensitive` aspect in `build.go` (the ONE structural gap)

This is the only change outside `packages/identity-domain`. It is generic, additive, opt-in.

1. **`internal/pkgmgr/definition.go` `DDLSpec`** (~197–230) — add a field:
   ```go
   // Sensitive marks an aspect-type DDL as carrying sensitive data
   // (arch Item 6). The Processor's step-6 validator anchors sensitive
   // aspects to identity vertices. Meaningful only for
   // Class == "meta.ddl.aspectType"; defaults false (non-sensitive).
   Sensitive bool
   ```
   Default `false` → existing DDLSpecs (all vertexType, none setting it) are byte-for-byte unchanged.
2. **`internal/pkgmgr/build.go`** (the DDL meta-vertex emission, ~90–127) — after the existing `.script` / self-description aspects, **conditionally** emit a `.sensitive` aspect when `d.Sensitive`:
   ```go
   if d.Sensitive {
       addCreate(ddlKey+".sensitive", docAspect(ddlKey, "sensitive", "sensitive",
           map[string]any{"value": true}))
   }
   ```
   Shape MUST match what `ddl_cache.go:229-237` reads: `<root>.sensitive` aspect with `data.value` (bool). Emit it **only when true** (mirrors the cache's "absent → false" fallback; keeps the install batch minimal for the common non-sensitive DDL). Confirm `docAspect` (build.go:440) produces the `{class, vertexKey, localName, isDeleted, data}` envelope the cache unmarshals.
3. **Self-description validation (`build.go:74-88`) applies to aspect-type DDLs too.** It requires `InputSchema`, `OutputSchema`, non-empty `FieldDescription`, non-empty `Examples` for **every** DDLSpec. The two aspect-type DDLSpecs (Item B) MUST satisfy these (Q3 gives the minimal-but-valid shape). **Do NOT relax `build.go`'s validation** to special-case aspect-type DDLs — provide valid self-description instead (it is documentation the platform surfaces; cheap to fill). If you judge the validation genuinely *should* differ for aspect-type DDLs (e.g. no script/schema), that is a design call — **record it as an Open Question, do not silently relax the gate** (Q3).

> **Why the field (not just root `data.sensitive`)?** `build.go` writes the meta-vertex root as `docVertex(class, nil)` — root `data` is `nil`/`{}` (build.go:97). The cache reads `ref.Sensitive` from `<root>.sensitive` aspect **first**, root `data.sensitive` second. Emitting the **aspect** is the install-path-faithful shape (the package's structured data travels as aspects, like every other DDL field). A `DDLSpec.Sensitive` bool → a `.sensitive` aspect is the clean, opt-in mirror.

### Item B — the two `meta.ddl.aspectType` DDLSpecs (`ssn`, `dob`)

In `packages/identity-domain/ddls.go` `DDLs()`, add **two** DDLSpecs alongside the existing `identity` one:
```go
{
    CanonicalName:     "ssn",
    Class:             "meta.ddl.aspectType",
    Sensitive:         true,
    PermittedCommands: []string{"RecordIdentityPII"}, // the op that writes it (Q1); see note
    Description:       "Applicant Social Security Number. Sensitive aspect-type (arch Item 6 / PRD §358): " +
        "stored as vtx.identity.<id>.ssn, sensitive=true, identity-anchored, crypto-shred unit. Written by RecordIdentityPII.",
    Script:            sensitiveAspectDDLScript, // minimal/no-op declaration script (Q3)
    InputSchema:       `{"type":"object","properties":{"ssn":{"type":"string","description":"9-digit SSN; ###-##-#### or #########."}}}`,
    OutputSchema:      `{"type":"object"}`,
    FieldDescription:  map[string]string{"ssn": "Applicant SSN, 9 digits, stored normalized as a sensitive aspect on the identity."},
    Examples:          []pkgmgr.ExampleSpec{{Name: "ssn aspect", Payload: map[string]any{"ssn": "123-45-6789"}, ExpectedOutcome: "Stored as sensitive vtx.identity.<id>.ssn; rejected on any non-identity vertex by step-6 sensitiveAspectScope."}},
},
// dob — same shape, canonicalName "dob", ISO YYYY-MM-DD.
```
- `Sensitive: true` is the load-bearing addition (Item A makes it reach the cache).
- `Class: "meta.ddl.aspectType"` — `build.go:93` honors a non-default `Class`; `ddl_cache.go` `deriveDDLKind` recognizes it.
- **`PermittedCommands` on an aspect-type DDL — confirm semantics (Q3).** The step-6 `permittedCommands` check (`step6_validate.go:113-123`) keys off the **mutation document's `class`** — for a `ssn` aspect the class is `ssn`, so its DDL's `permittedCommands` *would* gate which op may write a `ssn` aspect. Listing `RecordIdentityPII` there is **defensible** (it scopes who writes PII) and harmless (the identity DDL's own `RecordIdentityPII` op is the only writer). **But** confirm it does not over-constrain (e.g. if a future op legitimately writes `ssn`). **Recommendation: list `["RecordIdentityPII"]`** — it tightens the privacy boundary (only the sanctioned op writes PII). If you prefer the looser "declaration-only, no permittedCommands" shape (permissive), record it in Q3. Either way the **`sensitive`** enforcement (the AC) is independent of `permittedCommands`.
- The self-description fields satisfy `build.go:74-88` (Q3).

### Item C — the Starlark write op `RecordIdentityPII` on the `identity` DDL (Q1)

Add to `packages/identity-domain/ddls.go`:
- **`PermittedCommands`** (~18–22): append `"RecordIdentityPII"`.
- **Description** (~23–31): note the new op writes `ssn`/`dob` sensitive aspects onto an existing identity.
- **InputSchema** (~33–42): add `identityKey` (the target `vtx.identity.<NanoID>`), `ssn`, `dob` properties.
- **FieldDescription** (~45–55): add `ssn`, `dob`, and reuse/extend `identityKey`.
- **Examples** (~56–69): add a `RecordIdentityPII` example.
- **`execute` dispatch** (~111–320): add an `if ot == "RecordIdentityPII":` branch that:
  1. Reads `identityKey` (required, must `startswith("vtx.identity.")` — mirror `ClaimIdentity`'s target check ~245).
  2. **Known-key read** of the target vertex to confirm it exists + is not deleted (mirror `ClaimIdentity` ~248–250). The caller declares `identityKey` in `ContextHint.Reads` (known-key-reads-only rule — README ~38–42; note this in the op's doc).
  3. Format-validates `ssn` and `dob` (Item D) → `fail("InvalidArgument: …")` on bad input.
  4. Returns `mutations` that **create** (or update — Q1) the two aspects:
     ```
     {"op": "create", "key": identity_key + ".ssn",
      "document": {"class": "ssn", "vertexKey": identity_key, "localName": "ssn",
                   "isDeleted": False, "data": {"value": normalized_ssn}}}
     {"op": "create", "key": identity_key + ".dob",
      "document": {"class": "dob", "vertexKey": identity_key, "localName": "dob",
                   "isDeleted": False, "data": {"value": dob}}}
     ```
     `class` MUST be `ssn`/`dob` (so the validator's `Lookup(class)` finds the sensitive DDL). **The identity vertex root is NOT mutated** (D5 — root `data` stays minimal).
  5. Emits an event (mirror `identity.created` / `identity.claimed` shape) — e.g. `identity.piiRecorded` with `{identityKey}` (do **NOT** put `ssn`/`dob` plaintext in the event — events are not sensitive-aspect-scoped; keep PII out of the event payload). Returns `response: {"primaryKey": identity_key}`.

> **Q1 — NEW op vs. extend `CreateUnclaimedIdentity`.** **RECOMMENDED: a new `RecordIdentityPII` op** (assumed throughout). Rationale: (1) PII is captured at **lease-application time**, not identity-creation time (PRD §358: "Applicant … government ID" is application data); (2) **not every identity carries SSN/DOB** (an AI-agent identity, a staff identity — bloating `CreateUnclaimedIdentity` with optional PII muddies the create contract and the dedup-index logic); (3) the identity may be **claimed** before PII is recorded (the lease applicant flow). A dedicated op keeps `CreateUnclaimedIdentity` lean and the PII write auditable as its own operation. **`create` vs `update` for the aspects (Q1b):** recommend **`create`** (first-write); if the application can re-submit PII, the op needs `update` (or the caller declares the aspect keys in `Reads` and the script chooses create-vs-update by presence — mirror how `state` is `update`d). **Recommendation: `create` for the MVP** (one PII capture per application); note `update` as the re-submission contingency. **If extending `CreateUnclaimedIdentity` is genuinely leaner+faithful in Winston's judgment, the alternative is recorded here — but the recommended design is the lean dedicated op.**

### Item D — format validation (in the `RecordIdentityPII` Starlark op)

Fail-closed, mirroring the existing `claimKeyHash` hex check (~164–168) and `name` check (~131–136):
- **SSN (Q2):** accept `###-##-####` or `#########`; **strip non-digits**, require exactly **9 digits**, all `0`–`9`; store the **normalized 9-digit string** in `data.value`. Reject otherwise: `fail("InvalidArgument: ssn: must be 9 digits")`. (Do **not** validate SSN *allocation rules* — area/group/serial constraints — that is real-world brittleness beyond the demo; 9 digits is the format gate. Note this scope in a comment.)
- **DOB (Q2):** require ISO `YYYY-MM-DD` — length 10, positions 4 and 7 are `-`, the rest digits (a string-shape check; Starlark has no date lib — do a character check, do not attempt calendar validation like leap years). Reject otherwise: `fail("InvalidArgument: dob: must be ISO YYYY-MM-DD")`. Store verbatim in `data.value`.
- **Both required?** Recommend **both required** for `RecordIdentityPII` (it is the PII-capture op; a partial capture is an application-flow concern 14.4 owns). If a reviewer wants them independently optional, record it (Q2). **Recommendation: both required, non-empty.**

> **Q2 — format strictness.** SSN = 9-digit format only (no allocation rules); DOB = ISO `YYYY-MM-DD` string-shape only (no calendar validation). Both required. RECOMMENDED + assumed. These are demo-grade format gates, not production identity verification (that is the bgcheck `externalTask`'s job in 14.4). **Confirm** the strictness is acceptable; widen/narrow per Winston.

### Item E — what 14.3 does NOT change

No step-6 validator change (§3); no other Processor pipeline change; no engine (`internal/refractor`, `internal/weaver`, `internal/loom`) change; no Vault/encryption/shred; no `cap.svc` read-auth; no frozen-contract edit; no retro-fit of name/email/phone (Q5); no `OperationReply` field. The change is: **one `pkgmgr.DDLSpec` field + one conditional `.sensitive` aspect in `build.go` + two aspect-type DDLSpecs + one Starlark op (with format validation) in `packages/identity-domain/ddls.go` + tests + README.** The smallest thing that makes `ssn`/`dob` genuinely `sensitive: true` and identity-anchored on the real install path.

---

## 3. The step-6 validator is UNCHANGED — identity-anchoring is enforced for free (half of AC #2)

This is the design intent the launch framing names ("zero validator change"), and it **holds** — provided §0/Item A make `ref.Sensitive == true` for `ssn`/`dob`. The proof:

- **The rule is generic** (`step6_validate.go:128-147`): `if ref.Sensitive && kind == substrate.KindAspect { … if parentType != "identity" { return DDLViolation{sensitiveAspectScope} } }`. It names no concrete aspect type. It fires for **any** aspect whose class's DDL says sensitive — `ssn`/`dob` included, once installed.
- **It permits `ssn`/`dob` on identity** because `ParseAspectKey("vtx.identity.<id>.ssn")` yields `parentType == "identity"` → no violation. (Mirrors `TestValidate_SensitiveAspectOnIdentityAllowed` / `TestWriteScope_SensitiveAspectOnIdentityAccepted`.)
- **It rejects `ssn`/`dob` on a non-identity vertex** because `parentType != "identity"` → `DDLViolation{ViolatedConstraint: "sensitiveAspectScope"}`. (Mirrors `TestValidate_SensitiveAspectOnNonIdentityRejected` / `TestBypass4_SensitiveAspectOnNonIdentity`.) **§6 test 4 proves this for `ssn`/`dob` specifically, through the real installed DDLs.**
- **NO validator edit. NO frozen-contract edit.** Arch Item 6 already specifies "DDL `sensitive: true` at the aspect-type level … enforced by the MutationBatch validator" — 14.3 *uses* the mechanism, it does not redefine it.

> **Q4 — if the validator seems to need a change, STOP.** The design intent is zero validator change. The only way `ssn`/`dob` would *not* be anchored by the existing rule is if `ref.Sensitive` is false — which means Item A/B did not land the `.sensitive` aspect correctly (a §0 plumbing bug, not a validator gap). **A proposed validator change is a RED FLAG: surface it as a blocking Open Question with the exact reason, do not implement it.** (Expected outcome: no validator change needed.)

---

## 4. The install-path round-trip — the `Sensitive` field must REACH the DDL cache (the completion-lie trap)

AC #2's enforcement is real **only if** `d.Sensitive == true` on the DDLSpec actually becomes `ref.Sensitive == true` in the running DDL cache after a real `InstallPackage`. The travel path:

`pkgmgr.DDLSpec.Sensitive` (Item A.1) → `build.go` emits `<ddlKey>.sensitive` aspect with `data.value=true` (Item A.2) → the install MutationBatch commits it to Core KV → `DDLCache.Refresh`/load reads `<root>.sensitive` → `ref.Sensitive = true` (`ddl_cache.go:229-237`) → `validateOne` anchors the aspect.

**Miss any layer and `ssn`/`dob` silently install as non-sensitive** (the dev would "ship the feature" and a sensitive `ssn` on a `vtx.lease.<id>` would *pass* — the exact completion-lie §0 warns about). **§6 test 1 (DDL-cache `ref.Sensitive` after real install) + test 4 (the negative on a non-identity vertex through the real installed DDLs) are the traps that catch a missed layer** — they must install through the **real** `InstallPackage` / `DDLCache.Refresh` path, not a hand-seeded fixture. (Hand-seeded fixtures — like the existing `seedSensitiveAspectDDL` — prove the *validator*, not the *package*; 14.3's novelty is the package shipping the sensitive DDL, so the proof must be install-path.)

---

## 5. Forward fit (note, do NOT build)

14.3 declares + writes + anchors `ssn`/`dob`; 14.4 consumes them (build order 14.1, 14.2, 14.3 → 14.4 → 14.5):

- **14.4 (leaseApp convergence lens + externalTask patterns)** — the `leaseApplicationComplete` actorAggregate lens **reads** the `ssn`/`dob` aspects (+ name, etc.) as inputs to the bgcheck `externalTask` and the `applicant` body column, reprojecting on a linked-constituent change. **14.3 makes the aspects exist, sensitive, and anchored; 14.4 reads them.** The lens read-path does not change 14.3.
- **14.5 (e2e + `test-lease-convergence` gate)** — drives a full lease application; the `ssn`/`dob` PII is captured (via `RecordIdentityPII`), the bgcheck runs, convergence reaches steady state. 14.3 supplies the PII model that flow needs.

**The one design choice that matters for 14.4:** the aspects are written by the **identity-domain** package (which owns the `identity` vertex type + write-scope), **not** by a foreign `lease-signing` package. A foreign package cannot write sensitive aspects onto an identity it does not own (write-scope). So 14.4's lens **reads** `vtx.identity.<id>.ssn`; the **write** stays in identity-domain's `RecordIdentityPII` (this story). If 14.4 ever needs a *different* writer, that is a write-scope question for then; 14.3 puts the writer where the ownership boundary requires.

---

## 6. Tests (the sensitivity proof + the anchoring negative + the format rejections + the D5 assertion + the install round-trip) — first-class

Mirror the existing patterns: `internal/processor/step6_validate_test.go` + `write_scope_test.go` (the `sensitiveAspectScope` accept/reject pins) for the validator-behavior layer, `internal/bypass/bypass_ddl_schema_test.go` (`TestBypass4_*`) for the security-plane negative, and `packages/identity-domain/create_test.go` + `state_machine_test.go` for the **production-path** op tests (the real `InstallPackage` → Processor → commit → read-aspect-back harness via `testutil`). **The package tests are the centerpiece — they prove the shipped package, not a fixture.**

### Required tests

1. **`TestRecordPII_SSNDOBAreSensitive_AfterInstall` (THE sensitivity proof — AC #1 + §4).** Install the identity-domain package through the **real** `InstallPackage` path (mirror the existing package install harness in `create_test.go` / `testhelpers_test.go`). Build/refresh a `DDLCache` from the resulting Core KV and assert `cache.Lookup("ssn")` → `ref.Sensitive == true` **and** `cache.Lookup("dob")` → `ref.Sensitive == true`. This proves Item A's field reached the cache (the §4 round-trip; a missed mirror fails here). Contrast (optional but recommended): `cache.Lookup("state")` (a non-sensitive aspect class, if it has a DDL — likely a `Lookup` miss) is **not** sensitive.
2. **`TestRecordPII_WritesAspects_RootMinimal_D5` (the write op + D5 — AC #1 + invariant b).** Drive a `CreateUnclaimedIdentity` then a `RecordIdentityPII{identityKey, ssn:"123-45-6789", dob:"1990-01-15"}` through the production pipeline (mirror `create_test.go`'s `PublishOp`/`DriveOne`/`OutcomeAccepted`). Assert: (a) `vtx.identity.<id>.ssn` aspect exists with `data.value` == the normalized 9-digit SSN (`"123456789"`); (b) `vtx.identity.<id>.dob` aspect exists with `data.value == "1990-01-15"`; (c) **the identity vertex root `data` is unchanged/minimal** (`{}` — read `vtx.identity.<id>` and assert root `data` has no `ssn`/`dob`/PII). (c) is the **D5 assertion** — the headline of invariant (b).
3. **`TestRecordPII_RejectsBadFormats` (format validation — AC #1 / Item D).** Through the production pipeline, `RecordIdentityPII` with: a bad SSN (`"12-34"`, `"abcdefghi"`, `"1234567890"` (10 digits)) → `OutcomeRejected` (`InvalidArgument`); a bad DOB (`"1990/01/15"`, `"15-01-1990"`, `"not-a-date"`) → `OutcomeRejected`. Assert the aspect keys are **absent** from Core KV after rejection (mirror `TestWriteScope_E2E_ForbiddenOpRejectsWithNoMutation` ~295–302). Accept: the valid case (covered by test 2). Optionally a missing-`identityKey` / non-identity-prefix `identityKey` rejection.
4. **`TestRecordPII_SensitiveSSNOnNonIdentityRejected` (THE anchoring negative — AC #2 + §3 + the §0 completion-lie trap).** This is the load-bearing AC #2 proof. With the **real installed** `ssn` DDL (`ref.Sensitive==true`), run the **step-6 validator** against a `ScriptResult` whose mutation attaches a `class:"ssn"` aspect to a **non-identity** vertex (`vtx.lease.<id>.ssn` or `vtx.resource.<id>.ssn`) and assert `DDLViolation{ViolatedConstraint: "sensitiveAspectScope"}` (mirror `TestValidate_SensitiveAspectOnNonIdentityRejected` ~82–106 / `TestBypass4_SensitiveAspectOnNonIdentity`). **Crucially, the `ssn`/`dob` DDL must come from the real package install, not a hand-seeded `vtx.meta.ssn` fixture** — that is what proves the *package* (not the mechanism) enforces the boundary. Also assert the positive: a `class:"ssn"` aspect on `vtx.identity.<id>` **passes** (mirror `TestValidate_SensitiveAspectOnIdentityAllowed`). (If wiring the real-install DDL into a `processor`-package validator test is awkward across package boundaries, place this test in the `identity-domain` package or a `processor`-adjacent integration test that installs the package then runs the validator — Q7.)
5. **`TestRecordPII_DDLSpecValidationAndInstall` (Item A/B — the aspect-type DDLs install cleanly).** Assert the package's `Build`/`InstallPackage` succeeds with the two aspect-type DDLSpecs present (i.e. their self-description fields satisfy `build.go:74-88` — Q3), and that the install batch contains the `<ddlKey>.sensitive` aspects for `ssn`/`dob`. This pins Item A's `build.go` emission + Item B's valid DDLSpecs. (May be folded into test 1's setup.)
6. **Regression — the existing sensitive-aspect tests are UNTOUCHED.** `internal/processor/step6_validate_test.go` (`TestValidate_SensitiveAspect*`), `internal/processor/write_scope_test.go` (`TestWriteScope_SensitiveAspect*`), and `internal/bypass/bypass_ddl_schema_test.go` (`TestBypass4_*`) **must still pass unchanged** — they prove the generic mechanism 14.3 rides. Do **NOT** modify them. If your `pkgmgr.DDLSpec.Sensitive` addition forces any edit to them, that is a smell — the field is additive; stop and check. (The existing identity-domain `create_test.go` / `state_machine_test.go` must also still pass — `CreateUnclaimedIdentity` / `ClaimIdentity` are unchanged.)

### Test posture

The package tests (test 1/2/3/5) use the production `InstallPackage` → Processor → commit harness (`packages/identity-domain/testhelpers_test.go` + `internal/testutil`) — embedded NATS, no Docker — so the **install-path round-trip** (Item A → §4) is genuinely proven (a missed `.sensitive` emission fails test 1, not review). Test 4 (the anchoring negative) is the security-plane proof and **must** use real-installed DDLs (§0/§4). Flake retry per Deviation 14 is allowed; a flake claim without a re-run is a drift signal. No new bypass surface is added (14.3 ships package content + a generic opt-in pkgmgr field; the write rides the existing guarded commit path) — but **run Gate 2 + Gate 3** (§8) because this is the **sensitive-data plane** and the AC is a write-isolation/anchoring guarantee.

---

## 7. Required reading (DS does the deep reads; do not expect them pre-loaded)

- **THE ENFORCEMENT YOU RIDE (do NOT change):** `internal/processor/step6_validate.go` IN FULL — `validateOne` (~73–152), esp. the `permittedCommands` check (~113–123) and the **`sensitiveAspectScope`** block (~125–147). Internalize that it keys off `ref.Sensitive` for the aspect's **class** and the `parentType == "identity"` rule, with **no** concrete-type literal. **This is the rule 14.3 must NOT touch (§3 / Q4).**
- **THE PLUMBING YOU EXTEND (Item A):** `internal/processor/ddl_cache.go` — the `sensitive` read (~228–245), `CanonicalName` resolution (~176–210), `deriveDDLKind`. Confirm the `<root>.sensitive` aspect shape (`data.value` bool) your `build.go` emission must match. Then `internal/pkgmgr/definition.go` `DDLSpec` (~197–230, where the `Sensitive` field lands) and `internal/pkgmgr/build.go` — the self-description validation (~74–88, which applies to your aspect-type DDLs) and the DDL meta-vertex + aspect emission (~90–127, where the `.sensitive` aspect is conditionally added); `docVertex`/`docAspect` (~433–445).
- **THE PACKAGE YOU CHANGE (the headline content):** `packages/identity-domain/ddls.go` IN FULL — the `identity` DDLSpec (Description/InputSchema/FieldDescription/Examples) + the `identityDDLScript` `execute` dispatch (the `CreateUnclaimedIdentity` ~130–232 and `ClaimIdentity` ~234–319 branches are your templates for the new `RecordIdentityPII` branch + the format checks + the known-key target read + the aspect-create mutations). `packages/identity-domain/README.md` (update it). `packages/identity-domain/manifest.yaml` + `package.go` (confirm whether the two new DDLs need any manifest/declaredKeys touch — read to verify the install seam picks DDLs up from `DDLs()`).
- **THE TEST TEMPLATES:** `packages/identity-domain/create_test.go` (the production `InstallPackage` → `PublishOp` → `DriveOne(OutcomeAccepted)` → `readAspectData` harness — tests 2/3) + `packages/identity-domain/state_machine_test.go` + `packages/identity-domain/testhelpers_test.go` (the install harness — tests 1/5). `internal/processor/step6_validate_test.go` (`TestValidate_SensitiveAspect*`, the validator accept/reject pins — test 4) + `internal/processor/write_scope_test.go` (`seedSensitiveAspectDDL`, `TestWriteScope_SensitiveAspect*`). `internal/bypass/bypass_ddl_schema_test.go` (`TestBypass4_*`, `seedDDL4` — the security-plane negative shape, test 4).
- **THE GROUNDING (read; build TO; do NOT edit):** **arch Item 6** (`_bmad-output/planning-artifacts/lattice-architecture.md` ~1007–1020) — the aspect-level `sensitive: true` boundary. **PRD §354–§366** (`_bmad-output/planning-artifacts/prd.md`) — the erasure-classification table (§358: government ID = sensitive) + the DDL-design contract (§366: the Loftspace reference DDL exemplifies it). **Contract #1 §1.1** (`docs/contracts/01-key-shapes.md`) — the 4-segment aspect-key shape. **`docs/contracts/08-package-install.md`** — confirm the install seam admits a non-vertexType DDL + a `.sensitive` aspect (it should — DDLs install as meta-vertices + aspects generically; flag if it forbids non-vertexType DDLs).
- **HOUSE RULES:** `CLAUDE.md` — esp. NO history/changelog comments in code (no `// Story 14.3 …`, `// Previously …`, `// extends …`); aspect key-shape `vtx.identity.<id>.ssn`; new docs → README/`/docs`, not `_bmad-output/`.

---

## 8. Verification gates (run before handing back; record each + result in the closing summary)

- `go build ./...` — includes `internal/pkgmgr`, `internal/processor`, `packages/identity-domain` (confirms the `Sensitive` field + the two DDLSpecs compile).
- `make vet`
- `golangci-lint run ./...`
- `make verify-kernel` — **no kernel-topology change** is made (this is package content + a generic opt-in DDLSpec field), but run it to prove no regression (the stack must come up; requires `make up`).
- **`go test ./internal/processor/... -count=1`** — the **untouched** `sensitiveAspectScope` + `write_scope` pins (the generic mechanism 14.3 rides — the §6 test-6 regression gate). Plus test 4 if you place the anchoring negative here (Q7).
- **`go test ./packages/identity-domain/... -count=1`** — **the story's centerpiece:** the install-path sensitivity proof (test 1), the write-op + D5 assertion (test 2), the format rejections (test 3), the DDLSpec-install pin (test 5), and the untouched `CreateUnclaimedIdentity`/`ClaimIdentity` tests.
- **`go test ./internal/pkgmgr/... -count=1`** — the install seam still passes with the additive `Sensitive` field; the `.sensitive` aspect round-trips.
- **`make test-bypass` (Gate 2 — all BLOCKED)** — this is the **sensitive-data / write-isolation plane**; `TestBypass4_SensitiveAspectOnNonIdentity` is exactly the boundary 14.3 extends. Run it; confirm all BLOCKED (the new `ssn`/`dob` do not open a bypass — they ride the existing guarded path).
- **`make test-capability-adversarial` (Gate 3 — all DEFENDED)** — the capability/security plane; run it to confirm no regression (14.3 adds no read-path; expect all DEFENDED).
- The full **3-layer adversarial review** is Winston's gate (Blind Hunter / Edge Case Hunter / Acceptance Auditor) per `bmad-code-review` — a **security/privacy-plane** change earns the full 3-layer. The Acceptance Auditor checks the 2 ACs + arch Item 6 + PRD §358 + the "zero validator change / no frozen edit" claims; the Edge Case Hunter probes the **install-path round-trip** (§4), the **non-identity-vertex negative** (the §0 completion-lie), the **format-validation rejections + normalization** (Item D), the **D5 root-minimal** assertion, and the `permittedCommands`-on-aspect-DDL semantics (Q3); Blind Hunter on the diff. **Note it in your summary.**

**Why Gate 2 + Gate 3 DO run here (contrast 14.2, which skipped them):** 14.3 changes the **sensitive-data boundary** — the AC is precisely a write-isolation/anchoring guarantee, which is what Gate 2 asserts. Even though the *enforcement code* is unchanged, the *shipped sensitive aspect-types* are new, so the gates confirm the boundary holds for `ssn`/`dob` on the real plane. (If you judge a gate genuinely does not exercise the change, say so explicitly so it can be overridden — but default to running both.)

---

## 9. If too large / a split

This story is **small–medium** (one generic pkgmgr field + one conditional aspect emission + two aspect-type DDLSpecs + one Starlark op with format validation + tests + README). It should land in one pass. **Do not split.** The natural (but unnecessary) seam, if the pkgmgr plumbing proves fiddly, would be **14.3a** = the `pkgmgr.DDLSpec.Sensitive` field + the `build.go` `.sensitive` emission + the two aspect-type DDLSpecs + the install/sensitivity/anchoring tests (Items A/B + tests 1/4/5), **14.3b** = the `RecordIdentityPII` write op + format validation + the write/D5/format tests (Items C/D + tests 2/3). But the write op is what makes the aspects *exist* to be anchored, and the install proof is cheap — prefer the single pass. If split, land 14.3a first (it makes `ssn`/`dob` sensitive + anchored), then 14.3b (the writer).

---

## 10. Open Questions (assumptions made autonomously — Winston to confirm; Q1/Q3/Q5 are the load-bearing ones)

These are the decisions taken while drafting (the create-story ran autonomously). Each carries a **recommendation**; the dev proceeds on the recommendation unless Winston overrides. **Q1, Q3, and Q5 most warrant Winston's eye** (the op design, the aspect-type-DDL self-description shape, and the scope of the §0 latent-gap fix).

- **Q1 — A NEW `RecordIdentityPII` op (not extend `CreateUnclaimedIdentity`); aspects written with `op: create`.** RECOMMENDED + assumed (§2 Item C). PII is lease-application-time data, not every identity has it, and the identity may be claimed before PII capture — so a lean dedicated op is faithful to the privacy boundary + state machine. **Confirm:** (a) new op vs. extending create; (b) `create` (first-write) vs. `update` (re-submittable) for the aspects. **Default: new `RecordIdentityPII`, `op: create`.** (Re-submission → `update`; recorded as the contingency.)
- **Q2 — SSN = 9-digit format (no allocation rules); DOB = ISO `YYYY-MM-DD` string-shape (no calendar validation); both required.** RECOMMENDED + assumed (§2 Item D). Demo-grade format gates; real verification is 14.4's bgcheck. **Confirm** strictness + the both-required choice. **Default: as stated.**
- **Q3 — The two aspect-type DDLs carry valid self-description (InputSchema/OutputSchema/FieldDescription/Examples) + a minimal/no-op `Script`; `permittedCommands: ["RecordIdentityPII"]`. `build.go`'s self-description gate is NOT relaxed.** RECOMMENDED + assumed (§2 Item A.3/B). `build.go:74-88` requires self-description for *every* DDLSpec; rather than relax the gate for aspect-type DDLs, provide valid (if minimal) self-description. **Confirm:** (a) is a minimal no-op `Script` on an aspect-type DDL acceptable (an aspect-type DDL is a *declaration*, not an op handler — does the platform expect a script at all?); (b) `permittedCommands: ["RecordIdentityPII"]` (tighten — only the sanctioned op writes PII) vs. empty/permissive; (c) **should `build.go` instead skip the self-description requirement for `meta.ddl.aspectType` DDLs?** — if yes, that is a deliberate pkgmgr change, **raise it rather than silently relaxing**. **Default: valid minimal self-description + no-op script + permittedCommands `["RecordIdentityPII"]`; do not relax the gate.** (This is the most likely Acceptance-Auditor flag — the aspect-type-DDL shape is novel in this codebase; no package ships one today.)
- **Q4 — ZERO step-6 validator change.** RECOMMENDED + assumed (§3). The rule is generic; `ssn`/`dob` anchor for free once `ref.Sensitive` is true. **A proposed validator change is a RED FLAG — surface it as blocking, do not implement.** **Default + expected: no validator change.** (This is the launch framing's stated design intent; the brief confirms it holds.)
- **Q5 — 14.3 ships ONLY `ssn`/`dob`; the same latent gap for name/email/phone (sensitive in prose, not structurally) is NOT fixed here.** RECOMMENDED + assumed (§0 / scope-out). §0 reveals name/email/phone are "sensitive" only in the `identity` Description prose + test fixtures, not as shipped aspect-type DDLs — so they are **not** actually anchored on the install path today. Fixing that is a strictly larger, separable change (3 more aspect-type DDLs + possibly the create-op write classes) with its own review. **Confirm:** scope 14.3 to `ssn`/`dob` and **flag the name/email/phone latent gap as a follow-up** (a candidate spin-off chip / new story), vs. widen 14.3 to fix all five now. **Default: `ssn`/`dob` only; flag the rest.** (Winston: this is the one place the launch framing's "the proven pattern already exists" premise is materially off — the pattern is *documented* but not *enforced* for name/email/phone. 14.3 makes it genuinely enforced for `ssn`/`dob`; whether to back-fill the others is your call.)
- **Q6 — No CONTRACT-AMENDMENT-REQUEST.** RECOMMENDED + assumed (top-of-story flag). Arch Item 6 specifies the aspect-type `sensitive: true` shape + MutationBatch enforcement; the `pkgmgr.DDLSpec.Sensitive` field + `.sensitive` aspect are the in-tree implementation of that already-ratified shape (the DDL cache *already reads* `<root>.sensitive`). `docs/contracts/08-package-install.md` should admit a non-vertexType DDL generically. **Confirm:** Winston agrees no `cmd/<area>/CONTRACT-AMENDMENT-REQUEST.md` is warranted. If 08-package-install or another frozen contract forbids non-vertexType DDLs or a `.sensitive` aspect, that is the gap → a CAR (not an in-flight edit). **Default: no CAR; flag if a contract genuinely forbids the shape.**
- **Q7 — Test 4 (the anchoring negative) is placed where it can use real-installed DDLs.** RECOMMENDED + assumed (§6 test 4). The proof must use the **package-installed** `ssn`/`dob` DDLs (not a hand-seeded fixture) to prove the *package* enforces the boundary. If a cross-package `processor` test cannot cleanly consume a real install, place test 4 in the `identity-domain` package (install → refresh DDL cache → run `processor.NewValidator` against a non-identity-vertex `ssn` mutation) or a `processor`-adjacent integration test. **Default: wherever real-install + validator co-exist cleanly; do NOT fall back to a hand-seeded `vtx.meta.ssn` (that would prove the mechanism, not the package).**

---

## Dev Agent Record

### Completion Notes

Built exactly per Winston's binding adjudication (Items A–D + tests). The
headline (§0) holds: `ssn`/`dob` are now structurally `sensitive: true` on the
**real install path**, and the **unchanged** step-6 validator anchors them for
free. **Zero validator change** (Q4 expected outcome confirmed). **No
CONTRACT-AMENDMENT-REQUEST** raised — `docs/contracts/08-package-install.md`
admits non-vertexType DDLs + a `.sensitive` aspect generically (Q6).

**Item A (generic pkgmgr plumbing, opt-in, default-false):**
- `internal/pkgmgr/definition.go` — added `Sensitive bool` to `DDLSpec`.
- `internal/pkgmgr/build.go` — in the DDL loop, conditionally emit
  `ddlKey+".sensitive"` (`docAspect` with `data.value=true`) **only when**
  `d.Sensitive`. Non-sensitive DDLs emit nothing new (the regression pin in
  `build_test.go` proves it), so primordial-DDL key counts are unchanged →
  `verify-kernel` passes unchanged. No `ssn`/`dob`/`identity` literal in engine
  code (invariant a — type-agnostic). The read side (`ddl_cache.go:229`) was
  already present and untouched.

**Items B/C/D (package content — `packages/identity-domain/ddls.go`):**
- Two `meta.ddl.aspectType` DDLs — `ssn` and `dob` — `Sensitive: true`,
  `PermittedCommands: ["RecordIdentityPII"]`, valid minimal self-description,
  and a shared declaration-only `sensitiveAspectDDLScript` (fails closed; never
  dispatched, since no op carries `ssn`/`dob` as its operation class). The
  `build.go` self-description gate was **not** relaxed (Q3 default).
- New op **`RecordIdentityPII{identityKey, ssn, dob}`** on the `identity` DDL
  (Q1: new op, `op:"create"`, both required). Reads the target identity
  (known-key), fails if absent/tombstoned/`merged`, validates formats, writes
  `vtx.identity.<id>.ssn` (class `ssn`, value normalized to 9 digits) +
  `.dob` (class `dob`, ISO verbatim) as aspects, leaves the identity vertex
  **root `data` untouched** (D5), emits `identity.piiRecorded` carrying only the
  identity key (no PII plaintext).
- **Format gates (Item D):** ssn = strip `-`, require exactly 9 digits (reject
  non-9-digit / non-numeric), store normalized; dob = ISO `YYYY-MM-DD`
  string-shape (length 10, `-` at idx 4/7, rest digits), store verbatim. Both
  required, fail-closed `InvalidArgument`.

**Wiring:** added the `RecordIdentityPII` permission (grants → frontOfHouse,
backOfHouse, operator) in `permissions.go`; synced `manifest.yaml`
(`VerifyAgainstDefinition` cross-checks DDL/permission counts + names + grantsTo
— a drift would fail `lattice-pkg install`); updated `package.go` + `README.md`.

**`.mergedInto` deviation (the one design wrinkle):** the brief's Item C
suggested reading `.state`+`.mergedInto`. A freshly-created identity has **no**
`.mergedInto` aspect (only merge writes it), and a declared-but-absent
`ContextHint.Reads` key is a hard `HydrationMiss` (step4_hydrate.go:152). So
`RecordIdentityPII` declares only the identity vertex + `.state` in `Reads`; the
merged guard keys off `state == "merged"` (MergeIdentity sets state+mergedInto
together, so `state` is authoritative; `mergedInto` was only the error detail).
Recorded in the script comment + README.

**Tests (all green; production `InstallPackage`→Processor harness, embedded
NATS):**
- `internal/pkgmgr/build_test.go::TestBuildInstallBatch_SensitiveAspectEmittedOnlyWhenTrue`
  — `.sensitive` emitted iff `Sensitive:true` (opt-in regression pin).
- `packages/identity-domain/record_pii_test.go`:
  - test 1 `…_SSNDOBAreSensitive_AfterInstall` — `ref.Sensitive==true` for both
    classes via the real install round-trip (the §4 completion-lie trap).
  - test 2 `…_WritesAspects_RootMinimal_D5` — aspects written, ssn normalized,
    **identity root `data` stays `{}`** (the headline D5 assertion).
  - test 3 `…_RejectsBadFormats` (8 sub-cases) + `…_RejectsBadTarget` — rejected
    with no partial write.
  - test 4 `…_SensitiveSSNOnNonIdentityRejected` — **the AC #2 proof**: the
    **real installed** ssn/dob DDLs drive the **unchanged** step-6 validator;
    `ssn`/`dob` on a non-identity vertex → `sensitiveAspectScope`; on identity →
    passes. Uses the package-installed DDL (not a hand-seeded fixture).
  - test 5 `…_AspectTypeDDLsInstalled` — the `.sensitive` aspects landed in
    Core KV.
  - `package_test.go` updated to pin 3 DDLs / 4 ops + the ssn/dob sensitive
    aspect-types (the old `TestPackage_ThreeOps` 1-DDL/3-op assertion was
    correct to change — it pins the package's declared shape).
- **Regression:** the existing `step6_validate_test.go` / `write_scope_test.go`
  `sensitiveAspectScope` pins + `bypass_ddl_schema_test.go` `TestBypass4_*` pass
  **unmodified** (the generic mechanism 14.3 rides), as do the existing
  `create_test.go` / `state_machine_test.go` (Create/Claim unchanged).

**Scope held:** ssn/dob only (name/email/phone latent gap flagged, not fixed —
Q5); no Vault/KMS/crypto-shred (marker only); no serviceAccess/cap.svc; no
frozen-contract edit; no engine/validator change.

**Verification gates — all PASS:**
- `go build ./...` — PASS.
- `make vet` — PASS.
- `golangci-lint run ./...` — PASS (0 issues).
- `make verify-kernel` — PASS ("ALL ASSERTIONS PASSED"; kernel key counts
  unchanged — the `.sensitive` emission is conditional + package-only).
- `make test-bypass` (Gate 2) — PASS (4/4 BLOCKED; Bypass #4
  `sensitiveAspectScope` is the boundary 14.3 extends).
- `make test-capability-adversarial` (Gate 3) — PASS (6/6 cleared; 5 DEFENDED,
  1 ACCEPTED-WINDOW — the pre-existing baseline; 14.3 adds no read-path).
- `go test ./internal/pkgmgr/... ./internal/processor/... ./packages/identity-domain/...`
  — PASS (run with `-p 1`; under full parallelism a few unrelated tests flake on
  embedded-NATS store contention — confirmed infra-only, all pass serially;
  Deviation 14).

**Note for Winston:** running the two gate Make targets regenerated
`_bmad-output/implementation-artifacts/gate2-report.txt` + `gate3-report.txt`
(timestamp + commit-hash churn only; same BLOCKED/DEFENDED rows). Left in the
working tree for your call.

### File List

- `internal/pkgmgr/definition.go` (modified — `DDLSpec.Sensitive` field)
- `internal/pkgmgr/build.go` (modified — conditional `.sensitive` aspect emission)
- `internal/pkgmgr/build_test.go` (modified — Item A regression pin)
- `packages/identity-domain/ddls.go` (modified — `ssn`/`dob` aspect-type DDLs,
  declaration script, `RecordIdentityPII` op + format validation, identity DDL
  metadata)
- `packages/identity-domain/permissions.go` (modified — `RecordIdentityPII` grant)
- `packages/identity-domain/manifest.yaml` (modified — declares ssn/dob DDLs +
  RecordIdentityPII permission)
- `packages/identity-domain/package.go` (modified — doc comment)
- `packages/identity-domain/README.md` (modified — sensitive-PII section)
- `packages/identity-domain/package_test.go` (modified — DDL/op count pins +
  sensitive aspect-type assertions)
- `packages/identity-domain/testhelpers_test.go` (modified — staff cap doc
  grants `RecordIdentityPII`)
- `packages/identity-domain/record_pii_test.go` (new — tests 1–5 + bad-target)
- `_bmad-output/implementation-artifacts/gate2-report.txt`,
  `gate3-report.txt` (regenerated by the gate Make targets — timestamp churn)

### Change Log

- Added `pkgmgr.DDLSpec.Sensitive` (opt-in, default-false) + conditional
  `.sensitive` aspect emission in `build.go` so a package can declare a sensitive
  aspect-type DDL through the install path (generic; no type leak).
- Shipped `ssn` and `dob` as `meta.ddl.aspectType` `sensitive: true` DDLs in
  identity-domain, making the existing step-6 `sensitiveAspectScope` rule anchor
  them to identity vertices with no validator change.
- Added the `RecordIdentityPII` operation (format-validated ssn/dob, written as
  identity-anchored sensitive aspects; identity vertex root untouched per D5) +
  its permission/manifest/README wiring.
- Added install-path sensitivity, write/D5, format-rejection, and the
  non-identity anchoring-negative tests; updated the package shape pins.
