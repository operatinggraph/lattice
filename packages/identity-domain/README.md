# identity-domain Capability Package

Identity vertex creation, claim, and state-machine management.

## Contents

- DDL `identity` (class `meta.ddl.vertexType`) handling 4 operations:
  - `CreateUnclaimedIdentity` (grants → frontOfHouse, backOfHouse, operator)
  - `UpdateIdentityState`     (grants → operator)
  - `ClaimIdentity` (scope=self, grants → consumer)
  - `RotateClaimKey` (grants → frontOfHouse, backOfHouse, operator — staff re-issue of a lost claim secret, R4)
  - `RecordIdentityPII` (grants → frontOfHouse, backOfHouse, operator)
- DDLs `ssn` and `dob` (class `meta.ddl.aspectType`, `sensitive: true`) —
  the two applicant-PII aspect types (see "Sensitive PII aspects" below).
- 4 permission vertices + `grantedBy` link grants
- 3 user-facing roles (consumer, frontOfHouse, backOfHouse) — role vertex
  + canonicalName/description aspects + a `vtx.roleindex.*` entry each —
  declared in the package definition and created in the install batch.

## Sensitive PII aspects (`ssn` / `dob`)

`ssn` and `dob` are declared as **separate** `meta.ddl.aspectType` DDLs, each
carrying `sensitive: true` (lattice-architecture Item 6 / PRD §358 —
"government ID … → `sensitive: true` aspects"; separate aspects, not one `pii`
blob, because crypto-shredding operates at the aspect level). Because they are
structurally `sensitive: true`, the Processor's step-6 MutationBatch validator
anchors them: a `ssn`/`dob` aspect may attach **only** to a `vtx.identity.<id>`
vertex (NFR-S3) — a sensitive PII aspect on any non-identity vertex is rejected
with `sensitiveAspectScope`. No validator change is needed; the rule keys off
the aspect class's DDL `sensitive` flag.

`RecordIdentityPII{identityKey, ssn, dob}` writes them onto an **existing**
identity (one that is not tombstoned or `merged`):

- `ssn` — 9 digits; any hyphens are accepted and stripped (placement is not
  enforced); stored normalized to 9 digits in `vtx.identity.<id>.ssn`
  (`data.value`). Format gate only — SSN allocation rules are not checked (the
  bgcheck `externalTask`, a later story, verifies the identity).
- `dob` — ISO `YYYY-MM-DD`, validated as a real calendar date (month 1–12, day
  within the month, Feb 29 only in leap years); stored verbatim in
  `vtx.identity.<id>.dob` (`data.value`). The date is not bounded against the
  current day (the deterministic script sandbox has no clock).

Both are required; a malformed value is rejected (`InvalidArgument`) with no
partial write. The PII lives in **aspects**, never the identity vertex root
`data` (D5). The `identity.piiRecorded` event carries only the identity key —
no SSN/DOB plaintext. Phase 1 ships the `sensitive` **marker** only;
encryption / crypto-shredding (Refractor's Secure Lens + Vault) is a separate
concern.

The `ssn`/`dob` aspect-type DDLs declare `permittedCommands: ["RecordIdentityPII"]`,
so a future crypto-shred op that tombstones an `ssn`/`dob` aspect must use a
**document-less tombstone** (no `document`, hence `class == ""`, which makes
step-6 skip the DDL `permittedCommands` check — the same shape ClaimIdentity uses
to tombstone `.claimKey`), or the shred op must be added to each aspect type's
`permittedCommands`.

> Note: `name` / `email` / `phone` are described as sensitive in the `identity`
> DDL's prose but are **not** yet shipped as structural `sensitive: true`
> aspect-type DDLs, so they are not enforced as identity-anchored on the
> install path. `ssn` / `dob` are the first PII aspect types to be genuinely
> enforced; back-filling the others is a follow-up.

## State machine

`unclaimed → claimed` via UpdateIdentityState. The `merged` state is
set only by the identity-hygiene package's MergeIdentity script.

## Install

    lattice-pkg install packages/identity-domain

Depends on `rbac-domain` (dependency warning logged; install order is the
operator's responsibility).

The install is ONE atomic commit routed through the Processor as an
`InstallPackage` op (Story 1.5.5): the 3 role vertices + their aspects +
`vtx.roleindex.*` entries, the DDL meta-vertices + aspects (`identity`, `ssn`,
`dob`), the package manifest, the permission vertices, and their grantedBy link
grants. Everything (roles included) lands in the manifest's `declaredKeys`, so
uninstall reclaims it all (closes F-001). Deterministic NanoIDs make re-install
idempotent. See `docs/contracts/08-package-install.md`.

## Architectural notes

- All script reads are known-key only. Duplicate-detection index
  lookups use `crypto.sha256NanoID` to produce stable index keys; the
  caller declares them in `ContextHint.Reads`.
- The DDL script handles CreateUnclaimedIdentity, UpdateIdentityState,
  ClaimIdentity, and RecordIdentityPII with known-key reads only.
  `RecordIdentityPII` declares the target identity vertex + its `.state`
  aspect in `ContextHint.Reads` (the merged guard keys off `state == "merged"`,
  so `.mergedInto` need not be hydrated).
