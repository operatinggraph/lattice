# Fire brief — the executed lease finally names its tenant

**Board row:** `verticals.md` — "The executed lease still doesn't name its tenant" (LoftSpace, pkg, ★★, S).
**Steward:** Vertical Steward, unattended fire, 2026-08-27.

## Scope sentence (verbatim from the board)

`/api/lease-document` renders `Tenant: vtx.identity.edu97ix…` — the applicant's real name is never
assembled (`doc.TenantName`). Fix: snapshot `TenantName` onto the leaseapp at `SignLease`, read via the
shipped `subject.<aspect>` egress path — same shape as the landlord party (`d46ab947`), which resolves a
**non-sensitive** key live; tenant NAME is sensitive, so it needs the declared-egress path landlord didn't.

## Grounding (verified live, this fire)

- **Render path already complete and unchanged:** `internal/bridge/docgen_adapter.go:42` already declares
  `TenantName string \`json:"tenantName"\`` and `:230-237` already renders it with a bare-key fallback. No
  bridge-side work needed — only the emitted event's `params.doc.tenantName` needs to start arriving.
- **Why a live read is currently rejected:** `packages/lease-signing/leasedoc_scripts.go:3-29` (comment) +
  `:221-234` (code) — `CreateLeaseDocInstance`'s DDL assembles `doc{}` itself via `live_link_target` +
  `kv.Read`, which cannot declare a link-discovered sensitive aspect under `contextHint.egressReads`
  (sensitive-param-egress-design.md §3.6's emission guard rejects it structurally). The landlord fix
  (`d46ab947`) worked around this because a bare identity **key** isn't sensitive; a real **name** is.
- **The actual shipped egress mechanism** (`packages/orchestration-base/external_params.go`): a Loom
  pattern's `Params` may template `"subject.<aspect>.data.<field>"`; Loom's `inferExternalTaskReads`
  (`internal/loom/externaltask_params.go`) declares that aspect key under `contextHint.egressReads` (not
  `reads`) when it's the SUBJECT's own key (known at dispatch time — unlike a link-discovered key). The DDL
  script must call `resolve_subject_params(params, subject_key)` (the shared Starlark helper) to resolve it;
  a sensitive aspect resolves to a `$sensitiveRef` marker, decrypted only at the bridge's egress boundary —
  **never plaintext in a Core KV event**.
  - Live precedent already using exactly this, in THIS package: `packages/lease-signing/patterns.go:52`
    (`backgroundCheck` pattern, `Params: {"name": "subject.name.data.value", "dob": "subject.dob.data.value"}`,
    subject = the **identity**) and `packages/lease-signing/scripts.go:1120-1134,1274-1281`
    (`leaseServiceInstanceDDLScript` — prepends `orchestrationbase.ResolveSubjectParamsHelper`, calls
    `resolve_subject_params(raw_params, subject_key)`, embeds the resolved dict straight into
    `event_data["params"]`). **This is the pattern to mirror**, with `subject_key` = the **leaseapp** (the
    `leaseDocument` pattern is already `SubjectType: "leaseapp"`, `patterns.go:78-88`).
- **Why the aspect must live on the LEASEAPP, not the identity, and why default custody won't work:**
  `internal/processor/step6_validate.go:326-334` — `CustodyKind` `""`/`identity` hard-requires the aspect's
  parent vertex type be literally `"identity"` (`parentType != "identity"` → `DDLViolation
  sensitiveAspectScope`). A leaseapp-anchored sensitive aspect MUST declare
  `Custody: {Kind: CustodyKindRetentionClass, RetentionClass: "<name>"}`.
  - **Live precedent already shipped in THIS SAME package**: `packages/lease-signing/ddls.go:319-362`
    (`profileAspectDDL`, `.profile` on `vtx.leaseapp.*`, `Sensitive: true`,
    `Custody: {Kind: CustodyKindRetentionClass, RetentionClass: underwritingRecordRetentionClass}`) +
    `packages/lease-signing/retention.go:14-` (`RetentionClasses()`, `Policy:
    RetentionPolicyEraseOnExpiry`, `RetentionPeriod: "P7Y"`). Rationale there: "a landlord's underwriting
    decision is a business record that outlives the applicant's erasure request... after ShredIdentityKey
    the record is still readable, pseudonymized." **The identical rationale applies to an executed lease's
    tenant name** — it's a signed legal document, not a live PII projection.
  - **Do NOT reuse `underwritingRecordRetentionClass`** — its `RetentionClasses()` Description
    enumerates ONLY `.profile`/`.underwritingParties` (financial-qualification data); mixing an unrelated
    obligation into it violates the same population-separation discipline that class's own doc argues for
    (§8.7). Declare a **new** retention class (e.g. `executedLeaseRecord`), same `EraseOnExpiry` / `P7Y`
    shape, scoped Description naming just `.tenantName`.
- **SignLease's current shape** (`packages/lease-signing/scripts.go:654-680`): payload is `{leaseAppKey}`
  only — no applicant resolution today. Needs a NEW live read: `kv.Links(app_key, "applicationFor", "out")`
  (an (e)-class bounded enumeration, annotated `# read-posture: (e)`, mirroring
  `leasedoc_scripts.go:94-106`'s `live_link_target` — that exact helper isn't in this script's string
  constant (`leaseAppDDLScript`, a separate Starlark blob) and must be added there, or inlined), then a
  follow-up `kv.Read(applicant_key + ".name")` (sanctioned as the (e)-enumeration's own follow-up read, no
  separate declaration needed, same class as `leasedoc_scripts.go:124-133`'s `aspect_data`).
  - **Data shape**: identity's `.name` aspect stores `{"value": <name>}` (confirmed:
    `packages/identity-domain/ddls.go:1265`, and matches `subject.name.data.value`'s grammar). Write
    `.tenantName` on the leaseapp with the SAME shape: `{"value": <name>}` (so `subject.tenantName.data.value`
    resolves correctly downstream).
  - **Absent-name degrade**: if the applicant identity has no live `.name` (rare — required at
    `CreateUnclaimedIdentity`, but a defensive absence-check still applies, mirroring the optional-guarantor-field
    pattern at `scripts.go` `SetApplicantProfile`), skip writing `.tenantName` — the doc render already
    degrades to the bare key.
- **New aspect-type DDL** (mirror `profileAspectDDL`/`underwritingPartiesAspectDDL` at `ddls.go:319-405`
  almost verbatim): `CanonicalName: "tenantName"`, `Class: "meta.ddl.aspectType"`, `Sensitive: true`,
  `PermittedCommands: []string{"SignLease", "<the backfill op below>"}`,
  `Custody: {Kind: pkgmgr.CustodyKindRetentionClass, RetentionClass: <new class>}`,
  `Script: aspectDeclarationOnlyScript`. Register it in `DDLs()` (`ddls.go:60-74`).
- **Loom pattern change** (`packages/lease-signing/patterns.go:78-88`, the `leaseDocument` pattern): add
  `"tenantName": "subject.tenantName.data.value"` to its `Params` map alongside `"family": "docGen"`.
- **`leaseDocInstanceDDLScript` change** (`leasedoc_scripts.go`): prepend
  `orchestrationbase.ResolveSubjectParamsHelper` (mirror `scripts.go:1133-1134`'s exact concatenation
  shape), call `resolved_params = resolve_subject_params(p.params, subject_key)` inside the
  `CreateLeaseDocInstance` branch, and set `doc["tenantName"] = resolved_params["tenantName"]` **only when
  applicant != None** (the existing `if applicant != None:` block at `:222-233` — the `$sensitiveRef` marker
  or plain value flows straight into `doc{}`, matching how `leaseServiceInstanceDDLScript` embeds
  `resolved_params` directly). Update the file's own top-of-file doc comment (`:3-29`) — it currently states
  flatly that tenant/landlord names are "deliberately NOT read"; that claim is falsified for tenantName by
  this fire and must be corrected in the same commit (steward guardrail: a falsified documented claim is
  amended where it stands).

## The absence problem — backfill is REQUIRED, not optional

`resolve_subject_params`'s `_resolve_subject_token` (`external_params.go:76-88`) **fails loudly**
(`fail("MissingSubjectData: ...")`) if `subject_key + ".tenantName"` is absent. The instant `tenantName` is
added to the `leaseDocument` pattern's `Params`, **every already-SIGNED leaseapp lacking a `.tenantName`
snapshot will hard-fail `CreateLeaseDocInstance`** the next time Loom dispatches docGen for it — including
the in-flight `missing_leaseDocAttach` convergence work noted 🏗️ on the board (permission fix landed
2026-08-27, Weaver's reclaim pending). **Before wiring the Loom pattern change live, add a one-time
operator-only backfill op** (mirror `packages/clinic-domain/ddls.go:1295` `BackfillPatientRegistration` —
`PermittedCommands: [..., "BackfillTenantNameSnapshot"]`, CreateOnly write, idempotent, resolves the
applicant via the SAME `applicationFor` link walk as SignLease) that snapshots `.tenantName` on every live
**signed** leaseapp missing it. Query the live count first (`make up` stack is already running —
NATS + Postgres containers `lattice-nats`/`lattice-postgres` confirmed up this fire) via the
`leaseApplicationComplete` lens (`signedAt <> null`) before sizing the backfill; if the count is 0, still
ship the backfill OP (idempotent, future-proof against any signed-but-unsnapshotted edge case) but skip a
live-stack backfill RUN.

## Increment order

1. `retention.go` — new retention class.
2. `ddls.go` — new `tenantNameAspectDDL()`, registered in `DDLs()`.
3. `scripts.go` — `SignLease` branch: resolve applicant, write `.tenantName`. New
   `BackfillTenantNameSnapshot` op (mirror `BackfillPatientRegistration`'s shape) + its permission grant in
   `permissions.go`.
4. `patterns.go` — `leaseDocument` pattern's `Params` gains `tenantName`.
5. `leasedoc_scripts.go` — prepend the resolve helper, wire `doc["tenantName"]`, correct the stale doc
   comment.
6. Bump `manifest.yaml` + `package.go` version (package-content edit gate).
7. Tests: a `SignLease` test asserting `.tenantName` is written + encrypted; a
   `CreateLeaseDocInstance` test asserting `doc.tenantName` resolves (mirror
   `TestLeaseDocInstance_ManagedUnit_ResolvesLandlordKey` shape, `leasedoc_ops_test.go:191-`, but through the
   sensitive-egress path — assert the emitted event's `params.doc.tenantName` carries the real value
   end-to-end, i.e. decrypted at the same boundary the bridge would decrypt it).
8. Run the backfill live (if count > 0) once merged + package version bumped and installed.

## Gotchas

- `Custody` is legal ONLY on `Class: "meta.ddl.aspectType"` (`custodyscope.go`) — don't set it anywhere else.
- The new retention class's `CanonicalName` must be unique within the package (`validateRetentionClasses`).
- `$sensitiveRef` must reach `doc{}` UNMODIFIED (no string coercion / concatenation) — the bridge's
  post-decrypt extraction depends on the marker's exact shape (`external_params.go:81-85`).
- `lint-conventions` P2/P5 don't apply here (no Core-KV-read-from-app, no direct KV write outside an op) —
  this is pure package/op work.

## Non-goals

- No change to the landlord-key resolution (`d46ab947`) — already correct, non-sensitive, untouched.
- No change to `internal/bridge/docgen_adapter.go` — already wired for `TenantName`.
- No change to the crypto-shred / retention-class DESTRUCTION mechanism itself (declarative only, per
  existing `underwritingRecord` precedent — "RetentionPeriod is DECLARATIVE: no automatic expiry timer
  exists yet").
