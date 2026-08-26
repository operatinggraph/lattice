# Staff descriptor rendering — the op catalog read model + the shared form renderer

**Status: ✅ RATIFIED 2026-08-20 (Winston-adjudicated).** Designed 2026-08-20 (Winston,
Andrew-directed session); adjudicated under Andrew's same-day delegation — *"any designs without
architectural fork or contract change do not need my approval; Winston can adjudicate"* — which
this design satisfies: no architectural fork, no frozen-contract change (the For-Andrew block
below is the check that says so). Adversarial pass run and folded (§9); Inc-0 sweep outcome
folded (§10). Build-ready for the Steward. **Board row:** verticals lane · *Staff FEs render op forms from descriptors, not
hand-built JS* (★★★ XL). **Demand:** the 2026-08-20 audit
(`docs/reviews/vertical-app-descriptor-audit-2026-08-20.md`) — ~70 hardcoded submission sites /
~7,500 lines across the four vertical FEs re-implementing what `OpMetaSpec` already declares —
plus the codebase's own ask at `cmd/loftspace-app/web/app.js:76`: *"the generic
DDL-self-describing form needs an op-catalog read model — a Core-KV op-meta scan would violate P5
in a vertical app."* **Precondition:** the 15-op descriptor sweep — DONE (§10): 12 shipped, the
baseline holds 4 named residuals, each owed by a named increment below.

## For Andrew

Two mechanisms, both extensions of shipped machinery, no frozen-contract change:

1. **`opCatalog` — the first PLAIN descriptor lens** (edge-manifest package): one open NATS-KV
   read-model row per op-meta carrying the full descriptor vocabulary, keyed by `operationType`.
   The P5-legal successor to loftspace's `COMPLETIONS` map. Cypher expression forms are the ones
   `edgeCatalogTail` already executes on this engine; adapter/bucket shape is the
   `availableListings` idiom.
2. **A shared descriptor-form renderer** (`internal/descriptorform`, one `.mjs` module every app
   mounts): the THIRD renderer of the descriptor vocabulary, written to the build-to spec
   (`docs/components/edge-manifest.md`) and pinned by extending `lint-facet-renderer-drift` to
   three-way field-kind parity. Staff apps migrate surface-by-surface; the migration's ratchet
   gate (per-app op-literal ceilings, shrink-only) lands after the pilot proves the path.

No Andrew-altitude fork: the two mechanism forks (embedded registry vs lens; shared module vs
pinned copies) are resolved below with grounded reasoning. That check is what qualified this
design for Winston adjudication under the 2026-08-20 delegation (see Status); Inc 1 is unblocked.

## 1. Problem + intent

The supply side is done: S1 binds corpus-wide, §15 binds the app seam, and the descriptor sweep is
closing the last 15 ops. But no staff app can *consume* a descriptor: the only projection of the
vocabulary is `manifest.op.*` — a per-actor Personal lens on the `nats_subject` adapter, Facet's
transport. So every vertical FE transcribes descriptors by hand into JS (when it doesn't invent —
wellness's validation bounds, clinic's slot-grid mirror), and every package edit that changes an
op's shape breaks the apps only at runtime. Intent: make the descriptor the *single* source a
staff form renders from, so a package edit IS the UI edit — the pane covenant ("a new staff
workflow ships as a descriptor edit, zero app change", `packages/edge-manifest/panes.go`) extended
from read-surfaces to write-forms. This is brainstorm Stream 5 (#52/#55) landing on the staff
plane, the way `edge-manifest` landed it on the personal plane.

## 2. The shape

### 2.1 The op catalog lens (package work, edge-manifest)

A plain lens beside the five personal ones:

```
CanonicalName: "opCatalog"   Class: "meta.lens"   Adapter: "nats-kv"
Bucket: OpCatalogBucket = "op-catalog"            Engine: "full"
```

Cypher: flat `MATCH (op:meta) WHERE op.data.operationType <> null`, RETURN the descriptor columns
`edgeCatalogTail` already proves resolvable on this engine —
`op.presentation.data.{title,shortLabel,description,icon,tone,submitLabel,group}`,
`op.inputSchema.data.schema`, `op.fieldDescriptions.data.fieldDescriptions`,
`op.dispatch.data.{class,authContext,targetField,targetType,contextParams,reads}`, plus two the
personal catalog omits and the staff consumer needs: **`op.sensitive.data.value`** (the modal's
masking rule — aspect local name `sensitive`, data path `data.value`, per
`internal/pkgmgr/build.go:308-310`; `edgeCatalogTail` reads the same path at
`packages/edge-manifest/lenses.go:613`) and **`grantedToRoles`** — an **`OPTIONAL MATCH`** collect
over `(op)<-[:forOperation]-(perm:permission)-[:grantedBy]->(role:role)` for client-side
*visibility* filtering (never permission — §4.5 of the Facet design: the manifest affects
visibility, the Processor decides authority; both hops are the install-time edges
`internal/pkgmgr/build.go:387,404` mints — `lnk.permission.<id>.grantedBy.role.<id>` and
`lnk.permission.<id>.forOperation.meta.<opMetaID>`; note `role` is key type `vtx.role.*`, so the
label is `:role`, never `:meta`). OPTIONAL is load-bearing, not style: as a required continuation,
an op-meta with zero granting permissions yields **zero rows** — silently vanishing from the
catalog instead of degrading to a null-decorated row. The shipped precedent for the shape is
`packages/rbac-domain/lenses.go:92-93` (`OPTIONAL MATCH … collect(DISTINCT …)`), with the
null-restore semantics documented at `docs/components/refractor.md:360-372`.

Row keying: **`IntoKey: []string{"operationType"}`** — the plain-lens key mechanism
(`internal/pkgmgr/definition.go:1046-1053`, whose own doc example is exactly this; rendered at
`build.go:525-534`, consumed by `NatsKVAdapter.buildKey`, `internal/refractor/adapter/natskv.go:150-160`;
live precedent `capabilityRoleIndex`, `packages/rbac-domain/lenses.go:64-73`). NOT the
`Output`/BuildKey machinery, which is actorAggregate-only (`internal/refractor/projection/output.go:39-74`).
`operationType` is unique cross-package at install time (`internal/pkgmgr/installer.go:1116`), so
no key collisions.

Retraction — anchor == key source, **by requirement, not accident**: `operationType` is a ROOT
vertex field (`build.go:279-280`), which resolves read-free from the anchor body
(`ruleengine/full/values.go:23-30`) and survives the body-preserving tombstone
(`step8_commit.go:522-527`) — so the anchor-tombstone Delete
(`ruleengine/full/anchor_delete.go:40-135`, gated at `pipeline/evaluate.go:246-252`) removes the
row. **The cypher must NOT contain a `WITH` clause**: `anchorProjectionShape`
(`anchor_delete.go:179-183`) refuses any query carrying one, which silently disables both the
tombstone Delete and the filter-retraction presence check — and the tempting copy-paste source,
`edgeCatalogTail`, OPENS with `WITH op, role` (`lenses.go:588`). Copy its RETURN columns, never
its opener. (The `capabilityRoleIndex` precedent this design borrows its IntoKey from is itself
in the lingering class — its key column is not anchor-derived — so opCatalog's anchor-keyed shape
is deliberately safer than its precedent.)

Other key facts, checked: the Refractor auto-creates the bucket on lens load (`availableListings`
comment); rows carry no person data (S3/S8 clean by construction — descriptors are meta), so an
open bucket is correct; one lens, one bucket, no shared keyspace. A bare `{OperationType}` meta
projects a row with null presentation/schema — the renderer treats "no inputSchema" as *not
renderable* and never offers a form for it (fail-closed at the client, same posture as Facet's
unofferable-op gating).

**Envelope: no natsperm change needed** (checked, not assumed): the four app identities set no
`SubscribeAllow`, and the renderer defaults nil to subscribe-open (`internal/natsperm/matrix.go:587-590`,
the documented trusted-daemon stance at `:47-49`) with `$JS.API.>` already granted
(`matrix.go:466-481`); the Refractor's write side rides its blanket `"$KV.>"` `ExtraPubAllow`
(`matrix.go:302`, commented "including dynamically-named package buckets"). `loftspace-listings`
itself is absent from `bootstrap.PlatformBuckets()` and works today purely off these two blanket
grants — `op-catalog` rides the identical path.

**Reprojection cost line (accepted):** because the lens references `permission`/`role` labels, a
permission/role event reaches it via `evaluatePlainNeighbourEvent`'s unseeded whole-corpus rescan
(`pipeline/evaluate.go:274-285`) rather than an anchored seed. Permission/role writes happen at
package install/upgrade only, so the rescan is install-frequency, not steady-state; if that ever
changes, the narrowing licence (`anchor_derivation_plain.go`) is the named escape.

### 2.2 The shared renderer (FE work, one module)

`internal/descriptorform/form.mjs` (+ `//go:embed`, exported `http.FS`), mounted by each app's
`server.go` at `/shared/` beside its existing `//go:embed web` FileServer. Each app adds one thin
`/api/op-catalog` proxy handler (the `listings.go` KVListKeys/KVGet idiom) so the browser never
talks to NATS.

The module is a NEW implementation written to the vocabulary spec, not an import of Facet's:
`cmd/facet/web/app.js` (3,008 lines) couples its renderer to manifest frames, hat worlds, and the
covenant gate, and FORK-1 already runs two independent renderers held in step by
`lint-facet-renderer-drift`'s marker parity. The design extends that gate to **three-way** parity
(`form.mjs` gains a `jsMarkers` column per field kind: boolean, enum, money, date, date-time,
entity-ref). One deliberate narrowing: `entity-ref` in a staff app resolves against the CALLER's
context (below), not `manifest.ent` — the marker pins detection, not the resolution source.

Contract of the module (the whole API, deliberately small):

```
renderOpForm(descriptor, context, mount) -> { submit(): envelope | throws }
```

`context` is the staff app's explicit map — `{ target: <vertex key>, me: <identity key>,
workplace: <location key>, row: {...companion-row fields}, prefill: {...} }` — from which the
module resolves `Dispatch.TargetField/TargetType`, `contextParams`, and read templates in three
forms: `{payload.X}`, `{me.*}` anchors, and **`{context.<field>}`** — a field of the app-supplied
companion row, the staff analog of Facet's `{entity.<column>}` (`cmd/facet/web/app.js:2536-2561`).
The third form is load-bearing, not convenience: loftspace's `SignRenewal`/`VerifyGuarantor`
completions splice `renewalsRead` row fields (`leaseApp`, `tenant`) into composite link-key reads
(`app.js:117-145`, `:2153-2178`) — inexpressible from `{payload.*}`/`{me.*}` alone, and the reason
Inc 1's pilot migrates five of seven `COMPLETIONS` entries rather than seven (§7). Unresolvable
target/read ⇒ the form does not offer (fail-closed, the dispatch-target precedent `dda7ad98`).

**Anti-fallback rule (normative for the module):** target resolution NEVER substitutes
`context.me` (or any other key) for an unmatched `TargetType` — it degrades to not-offered. This
rule has a filed defect behind it, not taste: the old client fallback silently replaced an
unresolvable identity-typed target with the submitter's own identity, which for an operator would
have written a walk-in's SSN/DOB onto the operator's own vertex, create-only
(vertical-package-standard.md §8, "the client's identity targetType fallback"). Facet's current
shape — `selfAnchorKey` returning only from `m.selfAnchors`, `resolveTargetKey` refusing
otherwise (`app.js:2261-2264`, `:2642-2669`) — is the pattern to reproduce; since this module is
a fresh implementation, the rule is stated here so it cannot be re-derived wrong, and Inc 2's
tests pin it.

Validation comes from `InputSchema` (types, required, bounds, enums) — the wellness magic-number
class dies here. Envelope assembly (`operationType`, `class`, `payload`, `reads`,
`optionalReads`, authContext per `Dispatch.AuthContext`) mirrors `app.js:2536-2669`'s shipped
rules, including the wholeKey rule (a read template with an unsubstituted segment is dropped,
never sent malformed) and Facet's write-target-into-payload-before-substitution order
(`app.js:2748-2754` — what makes `{payload.<targetField>}.suffix` reads resolve).

### 2.3 What stays hand-built, on purpose

Ceremony surfaces (the `[no-op-meta:]` client-mechanism classes), file upload (`AttachObject`'s
derive-and-upload flow), and each app's *read* views/navigation. The covenant here is narrower
than Facet's: a staff app keeps its world knowledge; what it stops doing is re-declaring **op
shapes** the package already declares.

## 3. Alternatives considered

- **Compiled-in registry (no lens):** each app serves `/api/op-catalog` straight from
  `pkgregistry.All()` — zero platform surface, works offline. Rejected: it freezes descriptors
  into the binary, so F-004's whole point (edit a descriptor, `refresh-<vertical>`, watch the form
  change with no rebuild) is lost, and binary-vs-installed-world skew is exactly the stale-comment
  class the audit flagged (wellness's false "Charge has no op-meta"). The lens costs one spec +
  one test and keeps the running world authoritative.
- **Reuse the personal `manifest.op` catalog:** wrong transport (per-actor `nats_subject` frames
  need an edge engine per identity; a staff app server is not an edge node) and wrong scoping (the
  catalog is global data).
- **Per-app renderer copies pinned by digest (the S10 shape):** S10 pins because Starlark has no
  prelude; JS has modules. One implementation beats four pinned copies; the drift gate covers the
  cross-*language* copies that genuinely cannot merge.
- **Demand-side only (keep hand-building, transcribe-with-citation per the fe-engineer skill):**
  already the interim rule; it caps drift but not duplication, and the duplication grows with
  every feature across four apps. The census (~70 sites, all four apps, growing) clears the
  platform-mechanism bar that single-digit censuses don't.

## 4. State lifetime

The only new state is the bucket, and it lives the standard plain-lens lifecycle: rows projected
on `vtx.meta.>` CDC (create/update), retracted on op-meta tombstone (anchor-keyed), rebuilt on
lens-spec edit via hot-reload, bucket auto-created on lens load. The apps hold nothing: the proxy
reads per request (a 60s in-process TTL cache is permitted, never persisted). The renderer module
is stateless per invocation.

## 5. Migration + the ratchet

Surface-by-surface, deleting the hand-built form + its hand-built reads in the same diff that
swaps the surface to `renderOpForm`. The ratchet gate ships when the pilot proves the path
(Inc 4): `lint-app-op-descriptors` gains per-app **distinct-op-literal ceilings** pinned at the
then-measured counts — count above ceiling fails; count below fails too until the ceiling is
lowered in the same diff (the `guardHelperFloors` discipline, inverted) — so migration progress is
monotone and a new hand-wired op needs a reviewed ceiling edit. Until Inc 4 the gate's existing
per-app measure line is the visibility.

## 6. Test strategy (each test owned by a named increment)

- **Inc 1:** `lens_cypher_test.go` for `opCatalog` over a seeded topology (S6 binds anyway) —
  executes the flat MATCH, pins: an op-meta with full vocabulary projects every column; a bare
  meta projects null schema; a role-granted op carries its role names; an op with NO granting
  permission still projects (the OPTIONAL, B2); **and a tombstoned op-meta's row is DELETED from
  the bucket** — the retraction pin, mutated by inserting a `WITH` clause into the cypher and
  asserting the retraction test reds (the `anchorProjectionShape` trap, §2.1). Second mutation:
  drop the `operationType <> null` filter and assert non-op metas leak. Every mutation must be
  shown to fail. Plus the pilot's end-to-end: loftspace task modal renders `SetRenewalTerms`
  from catalog data.
- **Inc 2:** `form.mjs` node test tier (the Facet `.test.mjs` idiom): schema→field-kind table,
  envelope assembly per AuthContext, wholeKey drop, unresolvable-target refusal. The extended
  three-way drift gate is its own proof.
- **Inc 3:** per-app, the migrated surface's existing flow re-verified in-browser (fe-engineer
  verify rules) + the deleted-code diff.

## 7. Decomposition for the Steward (build order; Inc 1 is the pilot fire)

- **Inc 0 (precondition, running):** the 15-op descriptor sweep — the catalog is only as good as
  its rows. One coordination check at its adjudication: `SignRenewal`'s and `SetRenewalTerms`'s
  landlord path runs loftspace's `landlordLeg` (`app.js:2183-2184` → `{authContext:{target:
  <applicant>}}`), a shape that matches none of the four documented AuthContext kinds cleanly —
  verify the swept descriptors' `Dispatch.AuthContext` against `step3_auth_capability.go` before
  trusting them through a generic renderer (a wrong mapping is B4's defect class via another path).
- **Inc 1 — catalog + pilot (pkg + FE, posture-changing → full review depth):** `opCatalog` lens +
  bucket const + cypher test + loftspace `/api/op-catalog` proxy + the task-completion modal swaps
  its descriptor source from `COMPLETIONS` to catalog rows for the **five** expressible entries
  (`SignLease`, `RecordIdentityPII`, `SetRenewalTerms`, `CancelRenewal`, `ResolveWorkOrder`),
  deleting their map entries and the `app.js:76` lament. **This increment also ships
  `SignLease`'s full descriptor in lease-signing** (task voice, empty-properties schema — the
  form is a single confirm) — the pilot's own dependency: the sweep left it because its only app
  references are unquoted `COMPLETIONS` keys, now visible to the gate's object-key detector and
  baselined as `SignLease: lease-signing`; shipping it deletes that entry AND rewrites the
  Standard §6 *"Bare metas stay bare — SignLease…"* clause in place (the banner-rewrites-body
  rule), since §15 supersedes it for screen-wired ops. `SignRenewal`/`VerifyGuarantor` remain a
  two-entry residue map whose comment names the missing `{context.<field>}` template (§2.2) —
  deleted by Inc 3's loftspace fire, after Inc 2 ships the template. Green bar: the modal renders
  a renewal action from projected data on the running stack; a descriptor edit +
  `refresh-loftspace` changes the form with no app rebuild.
- **Inc 2 — the shared module (FE):** `internal/descriptorform` + `/shared/` mounts in all four
  apps + node tests (incl. the anti-fallback pin and `{context.<field>}` resolution) + the drift
  gate **restructured to N-way** — `lint-facet-renderer-drift.go`'s vocabulary table is a
  hardcoded two-renderer pairwise switch (`:41-45`, `:79-88`), so the third renderer means a
  per-renderer marker map + set-difference reporting, modest but not a constant edit. Loftspace's
  pilot modal moves onto the module (proving it against a surface that already works).
- **Inc 3 — per-app migration (FE, one fire per app):** clinic (biggest surface) → wellness →
  café → loftspace remainder. Each fire's brief lists the surfaces swapped and the lines deleted.
  Two surfaces are known non-migratable until their blockers clear, and stay hand-built with the
  gate's baseline as the ledger: **`CreateLocation`** (one op declared on three leaf DDLs — a
  single static `Dispatch.Class` cannot express the class choice, and an envelope without `class`
  is unconditionally rejected, `ddl_cache.go` `ClassForCommand`; Inc 3's clinic/loftspace briefs
  price the two honest fixes — a `Dispatch.ClassChoices` vocabulary field the renderer offers as
  an enum, vs per-leaf operationTypes — and build one), and **`AttachObject`/`DetachObject`**
  (inputs are the byte-plane upload response; the fix is an upload-ceremony affordance plus an
  owner-anchored attachments read surface, the `signInMethods`-pane precedent — not an exemption
  marker).
- **Inc 4 — the ratchet (gate):** per-app ceilings, mutation-tested.

Review depth: Inc 1 and Inc 4 are posture-changing (new read surface; new gate rule) — full pass.
Inc 2/3 are the Steward's sizing.

## 8. Reconciliation

*Didn't we handle this?* — supply-side yes (S1/§15); consumption existed only on the personal
plane. *Does this duplicate a pattern?* — it deliberately mirrors three: the pane covenant (UI as
descriptor data), `availableListings` (plain read-model shape), FORK-1's gate-pinned renderer
parity. *New state we already keep elsewhere?* — no; the bucket is the standard lens target and
the personal catalog keeps serving Facet unchanged. *Contract surface* — none frozen; the
vocabulary build-to spec (`docs/components/edge-manifest.md`) gains the catalog + staff-context
section in Inc 1, and FORK-1's freeze trigger (vocabulary completeness across renderers) now
counts three renderers, which this design treats as strengthening the eventual freeze, not
re-opening it.

## 9. Adversarial pass — RUN (2026-08-20), findings folded

One independent read-only pass against the frozen draft, briefed to falsify the eight load-bearing
mechanism claims. It returned **4 blockers + 3 corrections**, all folded above; recorded here so
the build starts from what was verified, not what was drafted:

- **B1** — the draft's "Inc 1 deletes COMPLETIONS" was false for `SignRenewal`/`VerifyGuarantor`
  (companion-row reads inexpressible in the draft's context contract) → §2.2 gained
  `{context.<field>}`; Inc 1 honestly migrates 5 of 7, Inc 3 finishes.
- **B2** — the role collect silently drops zero-permission ops unless `OPTIONAL MATCH` → stated
  normatively with the rbac precedent.
- **B3** — the draft asserted retraction while citing a precedent (`edgeCatalogTail`) whose `WITH`
  opener disables the whole anchor-delete path (`anchorProjectionShape` refuses `WITH`) → the
  no-`WITH` rule is stated, and Inc 1's tests gained the tombstone-retraction pin with the `WITH`
  mutation.
- **B4** — the fresh renderer implementation had no stated anti-fallback rule, the exact defect
  class the Standard §8 filed → normative rule + the Facet pattern citation in §2.2.
- Corrections: plain-lens keying is `IntoKey`, not `Output`/BuildKey (actorAggregate-only); the
  role label is `:role`, not `:meta`; the app NATS envelope needs **no** change (blanket grants
  verified at `matrix.go:290-303`, `:587-590`) — the draft's "verify at build" task dissolved into
  citations; the drift-gate extension is a bounded restructure, not a constant edit; and a
  permission/role event reaches this lens via the unseeded whole-corpus rescan — acceptable at
  install frequency, named escape if that changes.

## 10. Inc 0 outcome — the descriptor sweep (adjudicated 2026-08-20)

**12 of 15 shipped** as full descriptors (clinic-domain ×5, loftspace-domain ×3, loftspace-ledger,
lease-signing `SignRenewal`, wellness-domain ×2), five packages version-bumped in lockstep, all
gates green including the build-tagged lease-convergence suite run locally (lease-signing's
op-meta list moved). Adjudication highlights, each verified against the diff:

- **`SignRenewal` is `AuthContext: "task"`** — the tenant holds no standing grant and reaches the
  op only via the §10.7 ephemeral task grant; the descriptor names the descriptor-driven client's
  path (the RecordIdentityPII precedent). The §9 landlordLeg concern resolved cleanly: the
  landlord ops (`SetRenewalTerms`/`CancelRenewal`) already carry self-voice descriptors from the
  Standard's Inc 1, and loftspace's hand-built `landlordLeg` dispatch keeps working until Inc 3
  migrates it. `SignRenewal`'s reads are transcribed verbatim from the shipped completion
  dispatcher, and its formerly-bare `engineLegs` pin was updated, not loosened.
- **Three left in the shrink-only baseline with grounded reasons, deliberately not exempted:**
  `CreateLocation` (the three-leaf class-choice gap, §7 Inc 3), `AttachObject`/`DetachObject`
  (upload-response inputs / missing owner-anchored read surface — the honest fix is a surface,
  not a marker; the `UnlinkCredential`→`signInMethods`-pane precedent). Exemption codes are
  permanent ratified postures; these are debt with named fixes, so the baseline is their ledger.
- **`AssignUnitOwner` deliberately does NOT auto-fill `landlord` from the actor** — the op is
  operator-granted with no self-path constraint, and auto-filling would silently remove the
  operator's ability to assign a different manager (a script-permitted case).
- **`DebitAccount`'s slice deliberately omits `clauseRef`/`period`** (Weaver-only fields whose
  read template would hang an aspect suffix off an optional field — the checkReadTemplates
  refusal class).
- **The sweep exposed the gate's unquoted-object-key blind spot** (`SignLease:` as a JS map key
  was invisible to the quoted-literal scan). The gate now carries a keyed-op detector; `SignLease`
  is baselined and owed by Inc 1 (§7).

## 11. Inc 1 fire brief (Vertical Steward, committed before code)

**Scope sentence (verbatim from §7):** *opCatalog lens + bucket const + cypher test + loftspace
`/api/op-catalog` proxy + the task-completion modal swaps its descriptor source from `COMPLETIONS`
to catalog rows for the five expressible entries (`SignLease`, `RecordIdentityPII`,
`SetRenewalTerms`, `CancelRenewal`, `ResolveWorkOrder`), deleting their map entries and the
`app.js:76` lament. This increment also ships `SignLease`'s full descriptor in lease-signing …
rewrites the Standard §6 "Bare metas stay bare — SignLease…" clause in place. `SignRenewal`/
`VerifyGuarantor` remain a two-entry residue map … deleted by Inc 3's loftspace fire.*

**Pre-scout finding that bounds the fire (verified live, changes the increment's actual size):**
four of the five migrating entries **already carry a full `OpMetaSpec` descriptor**, independently
of this fire — `RecordIdentityPII` (`packages/identity-domain/opmetas.go:291-…`, task="task" voice
mislabel — actually AuthContext "task" per its own §10.7 task grant), `SetRenewalTerms` and
`CancelRenewal` (`packages/lease-signing/permissions.go:476-506`, `:550-576`, both
`AuthContext:"self"`, `TargetField/TargetType:"renewalKey"/"renewal"`), and `ResolveWorkOrder`
(`packages/maintenance-domain/permissions.go:64-93`, `AuthContext:"task"`,
`TargetField/TargetType:"workOrderKey"/"workorder"`) — all shipped by the Inc-0 sweep or an earlier
fire, not by this one. **The only net-new descriptor this fire ships is `SignLease`'s** (currently
bare — `packages/lease-signing/permissions.go:651`, just `{OperationType: "SignLease"}`, no
Presentation/InputSchema/Dispatch). `ResolveWorkOrder`'s shape above (task-voice, `AuthContext:
"task"`, single required target field, no other properties) is the precedent to mirror for
`SignLease`'s "single confirm" descriptor — `Dispatch.Class` must be `"leaseapp"` (COMPLETIONS'
own `klass`, `cmd/loftspace-app/web/app.js` COMPLETIONS.SignLease), `TargetField:"leaseAppKey"`,
`TargetType:"leaseapp"`, `Reads:["{payload.leaseAppKey}"]`, no optional reads, `InputSchema`
carrying only `leaseAppKey` (required), empty `FieldDescriptions` beyond it.

**Verified touch-list (file:line, live):**

1. `packages/edge-manifest/lenses.go` — append `opCatalog` as a NEW plain (`Adapter:"nats-kv"`)
   `LensSpec` entry in the `Lenses()` slice (currently 15 Personal/`nats-subject` entries, lines
   68-388; `LensSpec` supports both shapes in one struct per `internal/pkgmgr/definition.go:1030+`).
   Mirror `packages/rbac-domain/lenses.go:65-72` (`capabilityRoleIndex`) for the plain-lens
   shape/`IntoKey:["operationType"]`, but do **NOT** copy `edgeCatalogTail`'s `WITH op, role` opener
   (`packages/edge-manifest/lenses.go:588`) — `internal/refractor/ruleengine/full/anchor_delete.go:179-182`
   (`anchorProjectionShape`) type-asserts every clause and refuses wholesale on any `*With` node,
   which silently disables both the tombstone-delete AND the retraction pin. Copy
   `edgeCatalogTail`'s RETURN columns only (`lenses.go:596-613`, incl. `op.sensitive.data.value` at
   `:613`), plus the two it omits per §2.1: `grantedToRoles` via `OPTIONAL MATCH
   (op)<-[:forOperation]-(perm:permission)-[:grantedBy]->(role:role)` +
   `collect(DISTINCT role.canonicalName...)` — mirror `packages/rbac-domain/lenses.go:109-115`'s
   `collect(DISTINCT …)` shape, OPTIONAL is load-bearing (§2.1 B2: a required MATCH silently
   vanishes zero-permission ops). Add `OpCatalogBucket = "op-catalog"` following the
   `<Domain><Concept>Bucket` / kebab-case convention (`packages/clinic-domain/lenses.go:5-18`).
2. `packages/edge-manifest/lens_cypher_test.go` (new file, or append if one exists) — mirror
   `packages/rbac-domain/lens_cypher_test.go`'s harness shape for the seeded-topology proof, and
   `internal/refractor/ruleengine/full/anchor_delete_test.go` for the tombstone/`WITH`-rejection
   mechanics. Pins (§6): full-vocabulary row; bare-meta row (null schema); role-granted op carries
   role names; a zero-permission op still projects (the OPTIONAL, mutate to required MATCH and
   assert the row vanishes); **a tombstoned op-meta's row is DELETED** (mutate by inserting a
   `WITH` clause and assert the retraction assertion REDS — the `anchorProjectionShape` trap);
   second mutation: drop the `operationType <> null` filter and assert non-op metas leak. Every
   mutation must be shown to fail before being reverted. Plus the pilot end-to-end: loftspace task
   modal renders `SetRenewalTerms` from catalog data.
3. `packages/lease-signing/permissions.go:651` — replace the bare `{OperationType: "SignLease"}`
   with a full `OpMetaSpec` per the precedent above; bump `packages/lease-signing/package.go:85`
   (`Version: "0.31.0"` → next patch) per the standing package-edit-needs-version-bump rule.
4. `_bmad-output/implementation-artifacts/vertical-package-standard.md` §6 (lines ~208-212, "Bare
   metas stay bare") — remove `SignLease` from the bare-list prose and add a parenthetical exactly
   mirroring the existing `RecordIdentityPII` one two sentences later ("`SignLease` left this list
   in Inc 1 of the staff-descriptor-rendering fire: lease-signing now declares its descriptor").
5. `scripts/lint-app-op-descriptors.go:158-163` (`appOpDebt`) — **delete the `"SignLease": …`
   entry**: the gate fails an op that stops violating while still baselined (`:146-157` doc
   comment), so this line MUST come out in the same commit as #3, or CI reds.
6. `cmd/loftspace-app/listings.go` (mirror its `handleListings`, lines ~135-163: `KVListKeys` →
   per-key `KVGet` → JSON) — add `handleOpCatalog` reading `OpCatalogBucket`, keyed by
   `operationType`; register `inner.HandleFunc("/api/op-catalog", s.handleOpCatalog)` in
   `cmd/loftspace-app/server.go`'s `registerRoutes` (~line 75, alongside the other `/api/*` lines).
   No `/shared/` mount yet — that is Inc 2.
7. `cmd/loftspace-app/web/app.js` — delete the lament comment (~line 78-84) and the FIVE migrating
   `COMPLETIONS` entries (`SignLease`, `RecordIdentityPII`, `SetRenewalTerms`, `CancelRenewal`,
   `ResolveWorkOrder` — full current map at lines ~85-184, already captured verbatim by the scout
   report above), replacing the completion-modal code path (`openComplete` ~2074-2104,
   `submitComplete` ~2111-2150, `resolveTargetKey`/`selfAnchorKey` ~2261-2264/2642-2669) for those
   five operationTypes with rendering driven by the fetched `/api/op-catalog` row: schema-driven
   fields from `InputSchema`/`FieldDescriptions`/`Presentation`, envelope assembly from
   `Dispatch.{Class,AuthContext,TargetField,TargetType,Reads,OptionalReads}` with `{payload.X}` /
   `{me.*}` template substitution — reuse the SHIPPED `payload[desc.targetField] = target` write-
   before-substitution order (line 2124) and the wholeKey-drop rule already present in
   `submitComplete`. `SignRenewal`/`VerifyGuarantor` (`landlordLeg`/`extraFromRenewal` dispatch,
   lines ~2153-2184) are explicitly OUT of scope — they need the `{context.<field>}` template
   (§2.2), not built until Inc 2/3; leave their two `COMPLETIONS` entries + dispatch code
   untouched. **No shared `internal/descriptorform` module yet** (Inc 2) — this is a
   loftspace-local, inline consumer of the catalog rows; do not build the cross-app module early.
   **Anti-fallback rule is normative even at this small scale** (§2.2): if `Dispatch.TargetType`
   cannot be resolved from context, the op must not be offered — never substitute the actor's own
   key.
8. Docs build-to spec: `docs/components/edge-manifest.md` gains the `opCatalog` lens (§8
   reconciliation) — add it to the lens roster next to the existing 15 Personal lenses, one
   paragraph, per this file's own descriptive style.

**Non-goals (explicit, do not build):** the shared `internal/descriptorform` module (Inc 2); the
`{context.<field>}` template (Inc 2); `SignRenewal`/`VerifyGuarantor` migration (Inc 3); the N-way
`lint-facet-renderer-drift` restructure (Inc 2); the per-app ceiling ratchet gate (Inc 4); any other
vertical app's migration (Inc 3, clinic/wellness/café).

**Review depth:** Inc 1 is posture-changing (new read surface off Core-KV `vtx.meta.>`, first plain
lens in edge-manifest, new anchor-tombstone retraction pin) — full 3-layer adversarial pass before
admit, per §7.

**Gates:** `go build ./...`, `make vet`, `golangci-lint run ./...`,
`STRICT=1 go run ./scripts/lint-conventions.go`, `STRICT=1 go run ./scripts/lint-app-op-descriptors.go`,
`STRICT=1 go run ./scripts/lint-package-standard.go` (if it exists as a gate script — verify),
`go test ./packages/edge-manifest/... ./packages/lease-signing/... ./cmd/loftspace-app/...`.

**Verify:** headless — `go test`, then `curl localhost:7788/api/op-catalog` against the running
stack (reuse if up) asserting the JSON shape and that `SignLease`/`SetRenewalTerms`/etc. rows carry
schema + dispatch; in-browser only if a writable stack is up, one tab, closed when done.

## 12. Inc 1 outcome — shipped `c96b6ccb`

Built per §11's touch-list, unchanged in shape. Two findings surfaced mid-build and folded before
merge, both cheap because the pre-scout had already bounded the increment to one real net-new
descriptor:

- **`SignLease`/`RecordIdentityPII` move onto the task-grant `AuthContext` path** (catalog-driven
  dispatch sends `{authContext:{task,target}}`, where the old hand-built `COMPLETIONS` entries sent
  none). Adversarially verified as a FIX, not a regression: `SignLease` is granted to `operator`
  only (`packages/lease-signing/permissions.go`) and `RecordIdentityPII`'s DDL refuses a claimed
  identity without `authContextTarget == identity_key` — the old path was reachable only under an
  operator session, never a real applicant. The task-grant mechanism is not spoofable: `target` is
  pinned to the task's own `scopedTo` inside the actor's own ephemeral grant document, derived
  server-side from the gateway-verified identity, never client input.
- **Three defects the adversarial pass caught, all closed in the same fire** (no deferral row —
  each was inside the touched files): the `plain_scanroot_corpus_census_test.go` structural/
  per-event divergence exception was a bare lens-name allow-list — rewritten so the exception
  re-asks `AnchorProjectionKey` against a realistic tombstoned body per lens, so an aspect-keyed
  lens with genuinely-dead retraction can no longer reuse the same excuse; `SetRenewalTerms`'s
  `rentAmount` lost its client-side positive bound in the migration — restored as an InputSchema
  `minimum`; `dispatchVisibleWhen` was projected by the lens but silently dropped by the loftspace
  proxy (fail-open) — threaded through and treated as not-offerable by the FE, matching the
  descriptor's own unresolvable-condition contract.

**Not run — no live stack in this build environment (only `lattice-nats`/`lattice-postgres`
containers were up):** `make verify-kernel`, `make verify-package-{edge-manifest,lease-signing}`,
the `/api/op-catalog` shape curl, and an in-browser exercise of the applicant `SignLease`/
`RecordIdentityPII` task flow (the one behavioral change worth a human eyeball, since both move
paths). CI's `stack-gates` job independently installs + verifies both packages against a real
Docker stack and was green on `c96b6ccb` — owed anyway is the loftspace-app-specific in-browser
pass once a writable stack is up.

**Checkpoint for the next fire (Inc 2 — the shared module, §7):** no worktree held; Inc 1 landed
whole. Inc 2 builds `internal/descriptorform` (`form.mjs` + `//go:embed`), mounts it at `/shared/`
in all four apps, restructures `lint-facet-renderer-drift.go`'s hardcoded two-renderer table to
N-way, and moves loftspace's pilot modal from Inc 1's inline consumer onto the shared module. No
other Inc 1 residue: `SignRenewal`/`VerifyGuarantor` stay hand-built exactly as they were, per
design.

## 13. Inc 2 fire brief (Vertical Steward, committed before code)

**Scope sentence (verbatim from §7):** *`internal/descriptorform` + `/shared/` mounts in all four
apps + node tests (incl. the anti-fallback pin and `{context.<field>}` resolution) + the drift gate
restructured to N-way … Loftspace's pilot modal moves onto the module (proving it against a surface
that already works).*

**Resolved API (the design's §2.2 contract left the exact shapes open; pinned here so the build has
one unambiguous spec, not a re-derivation per file):**

```js
// internal/descriptorform/form.mjs
export function renderOpForm(catalogRow, context, mount) -> {
  descriptor: { title, submitLabel, sensitive, targetField, targetType },
  submit(): envelope   // throws on validation/anti-fallback failure; no network call
}
```

- `catalogRow` is the **raw `/api/op-catalog` row shape** (`operationType`, `presentation`,
  `inputSchema` as a JSON *string*, `fieldDescriptions`, `dispatch.{class,authContext,targetField,
  targetType,reads,optionalReads,visibleWhen}`, `sensitive`) — the module does its OWN
  `catalogDescriptor`-equivalent normalization internally (schema parse, `visibleWhen` fail-closed
  per loftspace `app.js:227-243`'s rule, no-inputSchema/no-class/no-targetField refusal). This
  removes the per-app normalization step Inc 1 left in `cmd/loftspace-app/web/app.js`
  (`catalogDescriptor`/`schemaFields`) — Inc 2 deletes it there, not duplicates it elsewhere.
- `context = { target, me, taskKey, workplace, row, prefill }` — `target` is the resolved subject
  key (the app already knows it: a task's `scopedTo`, or an explicit entity key for a non-task
  surface); `taskKey` is present only for a task-voice submission (Facet's `ctx.taskKey`,
  `app.js:2604-2615` `buildAuthContext` "task" case — needed to build `authContext:{task,target}`);
  `row`/`prefill` back `{context.<field>}` and pre-filled values. `me` is the signed-in identity key,
  used only if a future descriptor needs `{me.*}` — **not** as a target fallback (the anti-fallback
  rule, next).
- `mount` is the **fields container only** (Facet/loftspace's `#tc-fields` role) — the module never
  touches title/description/submit-button chrome; callers keep rendering those from the returned
  `descriptor` exactly as `openComplete` does today.
- `submit()` performs: required-field validation, numeric coercion (`Number.isNaN` guard, never
  invented bounds — schema's `min`/`max`/`step` only), **write-target-into-payload-before-
  substitution** (`payload[targetField] = target` first, mirroring `app.js:2748-2754` /
  loftspace `:2313`), template substitution over `{payload.X}` / `{me.*}` / `{taskKey}` (new: bare,
  for the `{task,target}` authContext shape — NOT a dotted `{context.<field>}` case) /
  `{context.<field>}` (new — reads `context.row[field]`, the loftspace `SignRenewal`/
  `VerifyGuarantor` companion-row need this migration does NOT touch, since those stay COMPLETIONS
  in Inc 2; the template form ships so Inc 3 can adopt it without a module change), **wholeKey drop**
  (Facet `:2582-2584` / loftspace `:329-345` — any read template with an unresolved segment is
  dropped, never sent malformed), and **authContext assembly** per `dispatch.authContext` — `"task"`
  → `{task:context.taskKey, target}` (throws if `context.taskKey` is falsy: the loftspace
  `desc.taskLeg && !task.taskKey` refusal, `app.js:2387-2390`, now enforced INSIDE the module since
  it owns authContext assembly), `"self"` → `{target: context.me}`, `"standing"`/anything else →
  `undefined`. Returns `{operationType, class, payload, reads, optionalReads, authContext}` —
  callers pass this straight to their existing `submitOp()`.
- **Anti-fallback rule (normative, tested):** `submit()` NEVER substitutes `context.me` (or any
  other context key) for an unresolved `targetField`/`targetType` — target resolution is `context.
  target` alone (already provided, already app-resolved) or the call throws. This is narrower than
  Facet's `resolveTargetKey` (which searches several context candidates) because descriptorform's
  callers resolve target themselves before calling `renderOpForm` — the module's OWN fallback
  surface is just "did the caller give me a target", so the rule collapses to: no `context.target`
  ⇒ `renderOpForm` itself returns `null` (mirroring loftspace's `openComplete` early-return,
  `app.js:2244-2247`) rather than rendering a form that can't submit.

**Verified touch-list (file:line, live, from the Phase-0 scout):**

1. **NEW `internal/descriptorform/form.mjs`** — the module above. Field-kind detection markers
   (boolean/enum/money/date/date-time/entity-ref) mirror Facet `app.js:2484-2507` line-for-line in
   *logic* (not copy-paste — a fresh implementation per design §2.2) so the drift gate's markers
   (below) can find matching literals. `entity-ref` renders as a plain text input in this module
   (staff apps have no entity-ref *picker* yet — out of scope; the marker only needs to exist for
   drift-gate parity, per design §2.2 "the marker pins detection, not the resolution source").
2. **NEW `internal/descriptorform/form.test.mjs`** — Facet's `.test.mjs` idiom
   (`cmd/facet/web/dispatch_target.test.mjs:1-333`: `node:test` + `vm.createContext`/
   `vm.runInContext`, sandbox `{console, document:{...}}`). Pins: schema→field-kind table (6 kinds),
   envelope assembly per each `authContext` kind, wholeKey drop (a template with one unresolved
   segment is dropped), unresolvable-target refusal (no `context.target` ⇒ `renderOpForm` returns
   `null`), task-voice submit throws when `context.taskKey` is missing, write-before-substitute
   order (a `{payload.<targetField>}.suffix` read resolves).
3. **NEW `internal/descriptorform/embed.go`** — `//go:embed form.mjs` + `var formFS embed.FS` +
   `func FS() http.FileSystem { return http.FS(formFS) }` (first internal package doing this — no
   in-repo precedent; mirror the four apps' `//go:embed web` + `embed.FS` shape at e.g.
   `cmd/loftspace-app/server.go:20-21`, just relocated + exported).
4. **`cmd/{loftspace,clinic,cafe,wellness}-app/server.go`** — each adds one
   `inner.Handle("/shared/", http.FileServer(descriptorform.FS()))` line beside its existing `/`
   FileServer mount in `registerRoutes()` (loftspace `:65-73`, clinic `:58-66`, cafe `:59-71`,
   wellness `:67-81`) + the new import.
5. **`cmd/loftspace-app/web/app.js`** — DELETE `catalogDescriptor` (`:228-268`), `schemaFields`
   (`:280-303`), `substituteReads` (`:325-348`) — all subsumed into `form.mjs`. Rewrite
   `openComplete`/`submitComplete` (`:2241-2441`) to: resolve `target` (unchanged,
   `task.scopedTo`), call the shared module (loaded from `/shared/form.mjs` via a dynamic `import()`
   or a `<script type="module">` — builder's call, mirror whichever pattern is less invasive against
   this file's existing non-module script tag) with `{target, taskKey: task.taskKey, row: null,
   prefill: null}`, render its returned `descriptor` into the existing title/desc/target/sensitive/
   submit-label DOM spans (unchanged from today), mount fields into `#tc-fields`, and on submit call
   `handle.submit()` → `submitOp()` with the returned envelope, keeping the existing
   `taskLeg`/non-taskLeg completion-and-reload logic (`:2412-2435`) — that part is app policy, not
   module concern, and stays in `app.js`. `descriptorFor` (`:199-202`) keeps its COMPLETIONS branch
   (SignRenewal/VerifyGuarantor, untouched) but its catalog branch now calls the module instead of
   `catalogDescriptor`.
6. **`scripts/lint-facet-renderer-drift.go`** — restructure `vocabMember` (`:41-45`) from
   `{name, jsMarkers, swiftMarker}` to `{name, markers map[string][]string}` (renderer-name →
   marker list), add a third renderer entry `formJS = "internal/descriptorform/form.mjs"` alongside
   `appJS`/`descriptorSwift` (`:30-33`), and replace the pairwise loop (`:69-89`) with: for each
   vocab member, compute the set of renderers missing at least one of their markers; if that set is
   neither empty nor all-renderers, report which renderer(s) lag. Existing two-way behavior must be
   unchanged for the boolean/enum/money/date/date-time/entity-ref markers already declared for
   `appJS`/`descriptorSwift` — this is additive (new renderer, new markers), not a rewrite of what
   already passes.
7. **Docs:** `docs/components/edge-manifest.md` — add the `internal/descriptorform` module + its
   `/shared/` mount convention next to Inc 1's `opCatalog` lens entry (§8 reconciliation, same file
   Inc 1 already touched).

**Non-goals (explicit, do not build):** `SignRenewal`/`VerifyGuarantor` migration (still Inc 3, still
COMPLETIONS, still untouched); clinic/wellness/café migration onto the module (Inc 3 — Inc 2 only
*mounts* `/shared/` in all four so Inc 3 has nothing platform-side left to add, but does not migrate
their existing hand-built forms); the per-app op-literal ceiling ratchet (Inc 4); an entity-ref
picker widget (still a plain text input everywhere).

**Review depth: FULL 3-layer adversarial before admit** — this is capability-plane-adjacent (the
anti-fallback / authContext-assembly rule is the exact defect class `vertical-package-standard.md`
§8 already filed once — a fresh implementation of that rule needs independent verification, not
size-based sizing) and it is the first internal package embedding a browser asset (`embed.go`
precedent other Lattice work will copy).

**Gates:** `go build ./...`, `make vet`, `golangci-lint run ./...`,
`STRICT=1 go run ./scripts/lint-conventions.go`, `node --check internal/descriptorform/form.mjs`,
`node --test internal/descriptorform/form.test.mjs`, `STRICT=1 go run ./scripts/lint-facet-renderer-drift.go`,
`go test ./cmd/loftspace-app/... ./cmd/clinic-app/... ./cmd/cafe-app/... ./cmd/wellness-app/...`.

**Verify:** headless — node tests, `go build`, then (if a writable stack is up) `curl` each app's
`/shared/form.mjs` for a 200 + the loftspace pilot's `SignLease`/`SetRenewalTerms` flow in one
reused browser tab, closed when done; otherwise note pending.

## 14. Inc 2 outcome — shipped `4cc3a3f1`

Built per §13, with a mandatory full adversarial pass (capability-plane-adjacent: the module owns
authContext assembly) run before admit, cold against the first build. It found the mount genuinely
broken — `http.Handle("/shared/", ...)` with no `http.StripPrefix` means every request resolves
against `shared/form.mjs`, not `form.mjs`, so all five migrated ops 404'd — plus the node suite
wasn't wired into CI, and five further regressions against the pre-migration behavior it was
supposed to preserve exactly: fail-open button enablement for bare/unsupported ops, an
unconditional `self`-authContext send (`landlordSubmit()`'s gate had no module-side equivalent),
silently-dropped `{actor}`/`:id` template forms with no replacement for a required-read failure,
lost CSS classing/label-input pairing, and no `targetType` assertion against the resolved target's
own key. All fixed in the same worktree by the original builder (resumed, not a fresh implementer)
and independently re-verified — live `curl` against a standalone `go run ./cmd/loftspace-app` (no
docker) proved an authenticated 200 serving the real module body; the node tier grew from 11 to 19
tests, closing the two vacuous ones the review caught (an entity-ref assertion indistinguishable
from its own removal; `{me}`/`{taskKey}` substitution with zero coverage) and adding the sharpest
anti-fallback vector directly: `renderOpForm(row, {me: X, target: undefined}, mount) === null` even
with `me` present.

**Not run — no live stack in this build environment** (only `lattice-nats`/`lattice-postgres`
containers were up, no app binaries running): an in-browser exercise of the loftspace pilot flow
against the real stack. CI's `stack-gates` job independently installs + verifies against a real
Docker stack; owed anyway once a writable stack is up.

**Known gap surfaced, not built (small, no live consumer today):** `cmd/loftspace-app/op_catalog.go`'s
proxy struct never carries `dispatch.contextParams` even though the `opCatalog` lens cypher projects
it — the Go proxy silently drops it before the browser ever sees it. No op migrated in Inc 1 or Inc 2
declares `ContextParams`, so this has zero live effect today, and wiring it through without a
consumer would be untestable dead code. **Inc 3's brief must re-check this before assuming
`{context.<field>}`-shaped ops are the only gap** — if a clinic/wellness/café op needing migration
declares `ContextParams`, the proxy needs the field added (mechanical) and `form.mjs` needs a
grounded decision on how it composes with the existing template forms (not mechanical — new
semantics, not a mirror of anything shipped).

**Checkpoint for the next fire (Inc 3 — per-app migration, §7):** no worktree held; Inc 2 landed
whole. Inc 3 migrates clinic (biggest surface) → wellness → café → loftspace's remaining
`SignRenewal`/`VerifyGuarantor` (now unblocked: `{context.<field>}` ships in this increment).
`CreateLocation` and `AttachObject`/`DetachObject` stay hand-built per §7's named blockers. Check
§14's `contextParams` gap against each app's migrating ops before assuming Inc 2's module handles
every read-template shape they'll need.

## 15. Inc 3a outcome — clinic (partial), shipped `a6be8314`

Migrated five of clinic's ops onto `internal/descriptorform`: **SetProviderProfile**, **CreateProvider**,
**AssignProviderSite** (both `CreateProvider`/`AssignProviderSite` have no `dispatch.targetField` at
all — a real gap Inc 1/2 never hit, since every op either touched was a task-voice completion against
an existing target; closed with a small, backward-compatible loosening: `normalizeCatalogRow` now
requires only `dispatch.class`, and `renderOpForm`/`submit()` only require/use `context.target` when
`dispatch.targetField` is present — 3 new regression tests in `form.test.mjs` cover both the new and
the unchanged old behavior), **ClinicDebitAccount** + **ClinicCreditAccount** (the self-authContext
pair — `context.me`/`context.selfVoice` wired from the app's own `patientIdentityKey()`/
`actingAsSelf()`, independently re-verified by a cold adversarial pass to be value-identical to the
pre-migration `{target: patientIdentityKey()}}` send, with zero self-grant reachable on the debit leg
by design, `packages/clinic-ledger/permissions.go:53`). `submitCatalogOp` (clinic's new
envelope-taking submit helper, mirroring loftspace's `submitOp(body, opts)`) gained the same
`isTransientAuthLag` bounded retry loftspace's own submit path already carries — the adversarial pass
caught its absence, a real reliability regression on the patient first-payment path (fails closed, not
a security hole, but real).

**Left hand-built — real blockers, not slices to avoid work, each closing a distinct future
increment:**
- **`RescheduleAppointment`/`CreateAppointment`** — both wrap a slot/duration-picker calendar UI; the
  vocabulary's raw `date-time` field would regress a working booking flow to two unaided timestamp
  inputs. No scheduling-widget field kind exists yet.
- **`SetProviderHours`/`SetProviderTimeOff`** — `windows`/`ranges` are `type: array` of objects; the
  vocabulary has no array field kind at all (nothing built for it anywhere, including Facet).
- **`RecordEncounter`** — clinical `summary`/`assessment`/`plan` are hand-rolled `<textarea>`s;
  migrated once, then reverted after review, because the vocabulary has no multiline text kind.
  Facet already has one (`app.js:2515`, keyed on `schema.maxLength > 120`) but `RecordEncounter`'s own
  schema in `packages/clinic-domain/opmetas.go` declares no `maxLength` on any of the three fields, so
  even mirroring Facet's exact rule needs a package-level schema edit too — bundle both in the same
  future increment. The follow-up-date visibility toggle (`hidden` until the checkbox is checked) is
  the same increment's to restore; the module has no per-field conditional visibility.
- **`CreateUnclaimedIdentity`** — an `OpCeremonySpec` mint-hash-then-reveal-once flow; the module has
  no ceremony support (Facet's own `ceremonyField` handling is the pattern to eventually mirror).
  **`CreatePatient`** rides the same exclusion: its own hand-built handler mints the identity first,
  then submits `CreatePatient` with the resulting key — a compound, two-op submission, not a 1:1
  op-to-form mapping the module's single-envelope contract can express as-is.
- **`StartVisitSeries`** — carries no `OpMetaSpec`/descriptor at all (only referenced from tests and
  `permissions.go`); needs an Inc-0-style descriptor sweep entry before it is even catalog-visible.
- **`RemoveProviderSite`** — invoked parameter-already-known from a list row's one-click "Remove"; no
  dedicated form exists to migrate, and mounting one would be a UX downgrade for no gain.
- **`ClinicCreateAccount`** — a single-field op with no user-facing form at all (auto-opened
  programmatically on a patient's first ledger transaction); same reasoning as `RemoveProviderSite`.
- **`CreateLocation`** / **`AttachObject`/`DetachObject`** — unchanged from §7's original blockers.

**Also found, accepted as-is (usability, not security — confirmed by adversarial pass):**
`AssignProviderSite`'s provider/site `<select>` pickers are gone — the schema declares both fields as
plain strings (no `x-entityRef`), so the module renders free-text `vtx.*` key inputs. No wrong-target
hazard: `clinicSiteAssignmentDDLScript` (`packages/clinic-domain/site.go:332-341`) validates shape,
type segment, and aliveness server-side, and the grant is operator-only unconfined. A future
entity-ref picker (still generally unbuilt anywhere in this module) fixes this for every op at once,
not just this one.

**Blast-radius note for the next engineer touching `internal/descriptorform`:** the `targetField`
loosening applies to loftspace/café/wellness too (one shared module). Swept every `OpDispatchSpec` in
`packages/` — zero ops currently declare `targetType` without `targetField`, and zero targetField-less
ops declare `authContext: "task"`, so loftspace's task-modal `canCompleteOp` gains no live-reachable
new offer from this change today; re-check both invariants if a future package adds either shape.

**Checkpoint for the next fire:** no worktree held once this lands. Inc 3 continues with **wellness**
next per §7's sequence (clinic's remaining ops are blocked on the five increments named above, not on
more clinic-specific migration work — pick them up as their own future increments once each
blocker's fix is designed, not folded silently into "more clinic").

## 16. Inc 3b outcome — wellness (partial), shipped `24c9e484`

Migrated four wellness-app ops onto `internal/descriptorform`: **CreateInstructor** (no
`dispatch.targetField`, mirrors clinic's `CreateProvider`), **SetInstructorProfile** (task-voice,
`targetField: instructorKey`, mirrors `SetProviderProfile`), **WellnessDebitAccount** +
**WellnessCreditAccount** (the standing-authContext ledger pair, `targetField: accountKey`,
mirroring clinic's `submitLedgerEntry` detached-mount pattern — a headless `renderOpForm` call whose
mount is never shown, since the visible amount/memo inputs stay put across both buttons and only the
envelope assembly changes). wellness-app gained the infra clinic already had: `op_catalog.go`
(verbatim port), `loadDescriptorform()` module caching, and `submitCatalogOp`.

**A cold adversarial pass, run against the first cut, found the build's own "fixed a pre-existing
bug" claim was itself a misdiagnosis one layer too shallow** — a genuinely useful catch, not a false
positive. The builder had found `SetInstructorProfile`'s `OpDispatchSpec` declared no `Reads` at all
and would fail every submission (`vertex_alive` on an absent `state` key), and patched it by adding
`Reads: []string{"{payload.instructorKey}"}` to the wellness-domain descriptor. Correct as far as it
went, but the review traced the actual server-side read-hydration path
(`internal/processor/step4_hydrate.go`) and `cmd/facet/web/app.js:2790-2804` and found: (a) Facet's
renderer has *always* auto-pushed a resolved `targetField` value, and the caller's own identity, onto
`reads` as a fallback — so this op was never actually broken for Facet, only for `form.mjs`, which
never grew the equivalent fallback design §2.2 says it "mirrors"; (b) a census of every `packages/`
`OpDispatchSpec` found **17 other ops across five packages** (clinic-domain, wellness-domain,
service-domain, clinic-reminders, orchestration-base) rely on the identical Facet-side fallback and
are silently broken the same way under `form.mjs` today — none of them touched by this fire. Fixed at
the right layer: `form.mjs`'s `submit()` gained the two-line fallback (targetField + `context.me`,
idempotent against an already-declared read), closing all 18 ops' gap at once with no per-package
edits; `SetInstructorProfile` keeps its own explicit `Reads` declaration too (the correct §2.5 posture
independent of the client-side safety net), with the descriptor's comment rewritten to drop the two
false claims the first cut's comment made. Five new `form.test.mjs` cases pin the fallback; four
existing tests updated to include the now-correctly-appended reads entries. The review also caught and
the same fire fixed: an unhandled `identityKey()` throw in the instructor-edit-open path (now
caught + toasted, matching the adjacent `loadDescriptorform()` failure handling), and the missing
`isTransientAuthLag` bounded retry on the billing path's open-account-then-post race (Inc 3a added the
identical retry for clinic's self-scoped pair; wellness's staff-standing pair had the same race,
unnoticed until this pass).

**Left hand-built — real blockers, each closing a distinct future increment, not slices to avoid
work:**
- **`CreateSession`/`CreateSessionSeries`/`ReassignSession`** — compute bespoke `optionalReads` via
  `slotCellKeys()`/`occurrenceCellKeys()`, enumerating every 15-minute cell a studio/instructor span
  covers for conflict-claim declaration. The module's read templates (`{payload.X}`/`{me.*}`/
  `{context.<field>}`) only substitute single keys; expressing a range-computed, variable-length read
  set is new module semantics with no shipped precedent, not a mirror of anything.
- **`TombstoneStudio`/`TombstoneSession`/`SetBookingAttendance`/`CreateBooking`/`JoinWaitlist`/
  `CancelBooking`** — one-click list-row or session/roster-card actions where every parameter is
  already known from the row (no dedicated form to migrate), the same category as Inc 3a's
  `RemoveProviderSite`; the booking trio additionally needs the same computed-reads machinery named
  above (seat/waitlist/slot claim keys, the double-book guard).
- **`CreateStudio`** — its `location` payload field is silently auto-derived from the caller's own
  `worksAt` anchor (`anchorKey("worksAt")`) and never rendered as a form field at all, even though the
  schema marks it required. The module's `context` map does carry a `workplace` key (§2.2) but no
  shipped precedent auto-fills a required schema field from it without ever offering an input — a
  genuinely new semantics question, flagged rather than guessed.
- **`WellnessCreateAccount`** — auto-opened programmatically inside `ensureLedgerAccount()`, no
  user-facing form, same reasoning as Inc 3a's `ClinicCreateAccount`.

**Checkpoint for the next fire:** no worktree held once this lands. Inc 3 continues with **café** next
per §7's sequence. The `form.mjs` targetField/`context.me` fallback fixed here benefits every future
increment (café, the rest of loftspace, and clinic's five remaining blockers once each is unblocked) —
no future fire needs to re-derive it.

**Filed, not built here (continuous-improvement, not this item's scope):** a `dispatch_reads_guard_test.go`-style
guard test (the idiom already exists in `packages/orchestration-base`, extracting each op's script-read
keys and asserting set-equality against its declared `Reads`/`OptionalReads`) would have caught the
original `SetInstructorProfile` gap directly and should be rolled out to the other four packages the
census found rely on the same client-side fallback.

**2026-08-22 grounding (Vertical Steward, before build):** picked up this row and scoped it before
touching code. `orchestration-base`'s guard works because each op's `if ot == "<Op>":` branch inlines its
own `required_string(p, "<field>")` → `vertex_alive(state, <var>)` pair, so a bounded-to-the-branch regex
sees every check. wellness-domain's `CreateBooking`/`JoinWaitlist` branches (ddls.go:3250, :3287) instead
delegate their whole read/validate sequence to a shared `prepare_booking_common(state, op, p)` helper
defined elsewhere in the script — the branch text itself contains no `vertex_alive` call to find. A
same-idiom port would silently pass (finding zero checks to compare) for exactly the ops that share
validation logic across a helper, which is the worst place for a claimed drift-guard to have a blind spot
(false confidence, not just incomplete coverage). wellness-domain's `Reads`/`OptionalReads` declarations
(opmetas.go) also mix three read *kinds* — vertex existence, aspect presence (`.status`, `.schedule`), and
link-based ownership probes (`lnk.*`, correctness-tolerant of absence by design) — where orchestration-base's
guard only ever checked the first kind. Re-filed `📐 needs designer pass` on the board:
the missing primitive is a script-flow-aware (not per-branch-regex) reads extractor that can follow a call
into a shared helper and classify vertex/aspect/link reads distinctly. Not built this fire; picked a
different ready row instead.

## 17. Inc 3c outcome — café (partial), shipped `3362aa8c`

Migrated two café-app ops onto `internal/descriptorform`: **VoidCharge** (POS amount-based void;
`AuthContext: "standing"`, `TargetField: tabKey`, a direct mirror of `SetInstructorProfile`'s
standing/targetField shape) and **CreditCafeAccount** — both legs, going one further than Inc 3a/3b's
own precedent. `CreditCafeAccount` is dual-grant (operator/frontOfHouse scope=any + resident scope=self,
`packages/cafe-ledger/opmetas.go`), and a cold adversarial pass on the first cut caught the build's own
code comment misreading that doc comment as ruling out driving the front-desk leg from the same
descriptor — it does not. `ClinicCreditAccount` (Inc 3a) already proves the identical dual-grant shape
drives both legs off one descriptor via `context.selfVoice` (true → self, false → the staff path's own
existing hand-built envelope shape, since `buildAuthContext` returns no `authContext` at all when
`selfVoice` is falsy). Both café legs now migrate: self-pay (`context.selfVoice: true`,
`context.me: identityKey()`) and front-desk record-payment (`context.selfVoice: false`, no
`context.me` — café has no cheap already-loaded lookup for "the lease's own resident identity" the way
clinic's `patientIdentityKey()` was, and it costs nothing to omit: `buildAuthContext` ignores `me`
whenever `selfVoice` is false, and its only other consumer, the module's targetField/`context.me` reads
fallback (Inc 3b), is inert here since `CreditCafeAccount` declares no `OptionalReads` probe that would
need it). cafe-app gained the same missing infra clinic-app/wellness-app already had:
`op_catalog.go` (byte-identical to wellness-app's own — no vertical-specific change needed at all, the
bucket is edge-manifest's shared `OpCatalogBucket`), `loadDescriptorform()` module caching, and
`submitCatalogOp`.

**Left hand-built — real blockers verified against the actual descriptors and `form.mjs`'s
`substituteTemplate`, not scope-avoidance:**
- **`OpenTab`** — declares no `dispatch.targetField` at all, only `ContextParams:
  {"leaseAppKey": "{me.leaseapp}"}` (its subject is a freshly-minted tab vertex, so there is no
  "vertex being viewed" to derive a target from). Neither `op_catalog.go`'s `opCatalogProjection` struct
  nor `form.mjs` carries or consumes a `contextParams` column at all — confirmed by the adversarial
  pass to render (not throw) a required raw-key text input asking a resident to type
  `vtx.leaseapp.<NanoID>` by hand, which is a real, if softer, blocker than the report's first-cut
  characterization.
- **`Charge`** (self path) **and `Settle`** — a genuinely new `form.mjs` gap, first exercised by café:
  both declare an `OptionalReads` self-ownership probe using the **typed** `{me.leaseapp:id}` template
  (e.g. `lnk.leaseapp.{me.leaseapp:id}.applicationFor.identity.{actor:id}`).
  `substituteTemplate` only resolves bare `{me}`/`{actor}`/`{payload.*}`/`{context.*}` and **throws**
  `"descriptorform: unrecognized read template …"` on anything else — confirmed by an existing,
  deliberately-failing-loud `form.test.mjs` case, not a bug to route around. Migrating either op today
  would ship a form that always errors. `Charge`'s descriptor is independently self-voice-only and
  omits `amountCents`/`description`, which the POS off-menu charge flow needs — a second, unrelated
  reason its staff paths stay hand-built regardless.
- **`CreateMenuItem`** — its required `locationKey` is silently derived from the staffer's own
  `worksAt` anchor (`workplaceLocationKey()`) and never rendered as a field at all, the identical
  new-semantics gap as Inc 3b's `CreateStudio` (§16).
- **`RetireMenuItem`** and the per-line "Void" button on each charge line — one-click list-row actions,
  parameter already known, no dedicated form; the `RemoveProviderSite`/`TombstoneStudio` category. The
  per-line void's `{tabKey, lineId}` payload shape isn't in `VoidCharge`'s own `InputSchema` at all
  (`tabKey`/`amountCents` only), so it couldn't ride the same migrated form even if it were otherwise
  one-click-exempt.
- **`CreateAccount`/`DebitAccount`** (`cafe-ledger`) — operator-only, auto-opened / Weaver-dispatched,
  no `OpMetaSpec` exists or should (the package's own doc comment: "neither is something a person
  decides to do"). Same category as Inc 3a's `ClinicCreateAccount` / Inc 3b's `WellnessCreateAccount`.

**Real bugs a cold adversarial pass caught on the first cut, fixed in this fire, not filed:**
- **A double-void money bug.** The migrated Void button re-enabled in a `finally`, but — unlike the
  deleted hand-built `#void-amount` input's own `input.value = ""` on success — never cleared the
  descriptor-owned amount field, so two clicks inside the `setTimeout(renderPos, 700)` re-render window
  resubmitted the byte-identical envelope; `VoidCharge`'s amount branch has no dedup/idempotency key and
  just subtracts again (clamped at zero). Fixed to match the adjacent per-line void button's own
  pattern: re-enable only in `catch`, never in `finally` — a successful void now stays disabled until
  the 700ms re-render mounts a fresh form.
- **Lost Enter-to-submit.** The migrated markup dropped the surrounding `<form>` in favor of a bare
  `<div>` + `type="button"`, silently breaking the ordinary POS "type amount, press Enter" gesture (and
  making the double-void above easier to hit by accident). Restored: `renderOpForm` never emits its own
  `<form>` wrapper, so wrapping the mount back in one and switching to a `submit` listener was safe.
- **Settle Tab rendering inert.** `renderPos` awaited the void form's catalog/module load BEFORE wiring
  `#settle-btn`'s own listener, leaving Settle visibly enabled but non-functional for the load's
  duration (and, on an overlapping re-render, able to double-wire). Reordered after Settle's listener
  and made fire-and-forget — `wireVoidChargeForm` captures its own DOM nodes before its first internal
  `await`, so it was already safe to detach.
- **A catalog-outage toast stomping a success toast.** Every `renderPos` — including the one after a
  successful Charge/Settle — re-toasted "void form unavailable" on a catalog outage, silently
  overwriting the just-shown green success toast 700ms later. Now rendered inline into the void mount
  instead of via the shared global toast.
- **A dropped confirmation amount.** The success toast lost `money(cents)` when the amount moved from a
  hand-tracked local variable to the submitted envelope's own payload; restored by reading it back off
  `envelope.payload.amountCents`.

**A cross-increment gap the adversarial pass surfaced live, fixed in this fire because it blocked
verifying this fire's own change:** none of `up-cafe`/`up-clinic`/`up-wellness`/`up-loftspace` provision
`edge-manifest` — confirmed against the actual running dev stack (`nats kv ls` against `NATS_URL`:
no `op-catalog` bucket exists). Every descriptor-driven form this whole design has shipped, including
the Inc 1 loftspace pilot, has been unverifiable-live on a stack brought up via any of the four
documented one-command targets since Inc 1 — a stack that happened to already carry `edge-manifest`
installed from an earlier hand-run `make install-edge-manifest`/`make up-facet` masked it. All four
targets now call `install-edge-manifest` (idempotent, same as every other `lattice-pkg install` call)
after their vertical's own `install-<x>` and before `provision-readpath`.

**Checkpoint for the next fire:** no worktree held once this lands. Inc 3 continues with **loftspace's
remaining `SignRenewal`/`VerifyGuarantor`** next per §7's sequence (the `{context.<field>}` template
Inc 2 shipped, per Inc 2's own checkpoint). Once loftspace's tail lands, Inc 3 is done except for the
named blockers each increment left behind (clinic's five, wellness's four, café's four above,
loftspace's `CreateLocation`/`AttachObject`/`DetachObject`) — each is its own future increment per §7,
not "more Inc 3."

## 18. Inc 3d fire brief (Vertical Steward, committed before code) — loftspace tail

**Scope sentence (verbatim from §7):** *loftspace's remaining `SignRenewal`/`VerifyGuarantor`* — the
last two ops in the `COMPLETIONS` residue map (`cmd/loftspace-app/web/app.js:113-143`), migrating
both onto `internal/descriptorform`, closing Inc 3 except for the named `CreateLocation`/
`AttachObject`/`DetachObject` blockers.

**The `{context.<field>}` READS template Inc 2 shipped is NOT what unblocks this** — re-verified
against the live descriptors (`packages/lease-signing/permissions.go:510-661`, shipped in Inc 0):
`SignRenewal`/`VerifyGuarantor`'s `Reads`/`OptionalReads` already resolve fine today via
`{payload.leaseApp}`/`{payload.applicant}`, because both fields are declared as **typed, required
`InputSchema` properties** — the actual blocker is that `leaseApp`/`applicant` must reach the
**payload** without the person typing them (both field descriptions say "resolved from the renewal's
own record, never typed"; the legacy hand-built path splices them in via `extraFromRenewal`, and
neither field is ever rendered). `context.row`/`{context.<field>}` has no reach into payload
construction at all in the shipped module — it only substitutes *read templates*.

**The real mechanism, grounded against an ALREADY-SHIPPED precedent, not invented:**
`pkgmgr.OpDispatchSpec.ContextParams` (`internal/pkgmgr/definition.go:704`, doc at :640-676) is
exactly "a schema field the client fills from context and never renders" — three packages already
declare it (`packages/cafe-domain/opmetas.go:92`, `packages/clinic-domain/opmetas.go:94`,
`packages/wellness-domain/opmetas.go`) — and **Facet's renderer already consumes it live**
(`cmd/facet/web/app.js` — `contextParams` excludes the field from `fieldNames` at :2419, resolves each
template via `substituteTemplate` (:2540-2564, including the `{entity.<column>}` form —
`form.mjs`'s own doc comment at :17-20 already calls `{context.<field>}` "the staff analog of Facet's
`{entity.<column>}`") and writes it into `payload` at submit (:2736-2747, `submitDescriptorForm`).
Adding `contextParams` support to `form.mjs` is a **mirror of Facet's shipped mechanism**, using the
`substituteTemplate` function `form.mjs` already has (:321-341) — not new invented semantics, and NOT
the same class of gap Inc 3a/3b left hand-built (`CreateStudio`/`CreateMenuItem`'s auto-derived
`location` fields have no Facet precedent to mirror at all; this one does).

**Fact 1 — mechanical Go gap, confirmed live:** the `opCatalog` lens cypher already projects
`dispatchContextParams` (`packages/edge-manifest/lenses.go:642,717` — identical to the
`personalManifest` lens Facet reads), but **none of the four apps' `opCatalogProjection` /
`opDispatch` structs carry it** (`cmd/{loftspace,clinic,wellness,cafe}-app/op_catalog.go` — byte-
identical files, confirmed by `diff`). Add `DispatchContextParams map[string]string
\`json:"dispatchContextParams"\`` to `opCatalogProjection`, `ContextParams map[string]string
\`json:"contextParams,omitempty"\`` to `opDispatch`, wire both in `toDescriptor()` and the "has a
dispatch" OR-condition — mirror `DispatchReads`/`Reads` exactly, in **all four** files (they must stay
byte-identical, per their own doc comments).

**Fact 2 — a live bug on the exact surface being migrated, fix in this fire (§4 "what a fire
discovers, this run fixes"):** `COMPLETIONS.SignRenewal` (`app.js:114-129`) declares neither
`taskLeg` nor `landlordLeg`. In `submitLegacyComplete` (`app.js:2413-2418`), `opts = desc.landlordLeg
? landlordSubmit() : desc.taskLeg ? {authContext:{task,target}} : undefined` — so for `SignRenewal`,
`opts` is **always `undefined`**, and `submitOp` (`app.js:553-561`) only sets `authContext` when
`opts.authContext` is truthy. `SignRenewal`'s **only** grant path is the §10.7 ephemeral task grant
(no scope=self, no standing role — `permissions.go:594-608`'s own comment, confirmed by
`TestPackage_TaskLegDescriptorsNameTheTaskPath`, `package_test.go:91-115`, whose own doc comment
already claims "the client reading these rows... is loftspace-app's task modal, which renders from
the descriptor" — aspirational today, true once this fire ships), and
`matchEphemeralGrant` (`internal/processor/step3_auth_capability.go:326-336`) denies outright on a
nil `env.AuthContext`. **`SignRenewal` is AuthDenied on every submission today**, both from the real
Tasks-tab entry and the synthetic renewal-card entry — nobody has signed a lease renewal through
loftspace-app's UI. Migrating onto the catalog module (`buildAuthContext("task", context)` already
builds `{task, target}` correctly) fixes this **once a genuine `taskKey` reaches it** — which the
synthetic card path does not today (next fact).

**Fact 3 — the synthetic renewal-card path needs a real task, not a fabricated one:**
`openRenewalAction` (`app.js:2694-2706`) builds a synthetic task with `taskKey: null`. `form.mjs`'s
`buildAuthContext` correctly throws `"This action can only be taken from its task."` on a falsy
`context.taskKey` (`form.mjs:397`) — the right fail-closed answer to a fabricated grant, but it means
the "Sign renewal" button on the renewal card would stop working post-migration unless it resolves
the tenant's **real** task first. `state.tasks` (populated by `loadTasks`, `app.js:1994-2016`) carries
`{taskKey, scopedTo, operationName, ...}` rows; `openRenewalAction`'s `SignRenewal` branch must look
up `state.tasks.find(t => t.operationName === "SignRenewal" && t.scopedTo === row.entityKey)` and use
its real `taskKey` — ensuring `state.tasks` is loaded first (a `loadTasks()`/quiet-equivalent call, or
reuse if already resident) — and refuse cleanly (no button, or a toast) if no matching real task is
found yet, never a null/fabricated key. `VerifyGuarantor`/`SetRenewalTerms`/`CancelRenewal` are
self-voice (`AuthContext:"self"`, landlord hat) and are UNAFFECTED — `completeTask` already no-ops on
a falsy key for those, this refusal is `SignRenewal`-only.

**Fact 4 — `context.row` wiring:** `openCatalogComplete` (`app.js:2280-2293`) hardcodes `row: null`.
Resolve the matching `state.renewals` row (fetch via the existing `loadRenewalsQuiet()` fallback,
mirroring `submitLegacyComplete`'s own lookup at `app.js:2378-2389`) whenever the catalog row's
`dispatch.class === "renewal"`, and pass it as `context.row`. Harmless for `SetRenewalTerms`/
`CancelRenewal` (they declare no `contextParams`, so the row goes unused).

**Increment order:**
1. **Go proxy fix (mechanical, all four apps):** wire `dispatchContextParams` through, per Fact 1.
2. **`form.mjs` capability (the one genuinely new-code step):** exclude `contextParams`-named fields
   from `fieldNames` (mirror the existing `targetField` exclusion at :435); after building the typed-
   field payload and BEFORE computing `reads`/`optionalReads`, resolve each `dispatch.contextParams`
   entry via the existing `substituteTemplate(template, context, payload)` and write it into `payload`
   — this ordering is load-bearing: `SignRenewal`/`VerifyGuarantor`'s `Reads` stay
   `{payload.leaseApp}`-shaped unchanged (Fact-check before "fixing" them to `{context.leaseApp}` —
   they must NOT change). A template that fails to resolve to a whole value for a **required**
   contextParams field must refuse the same way a required typed field does (`throw`, not a silent
   `undefined` payload key) — required vs. the `?`-suffix optional form (`definition.go:659-664`) is
   real vocabulary; build the optional form too only if grounding it is free (neither op here needs
   it — don't invent an untested branch for no live consumer).
3. **Package edit (`lease-signing`):** `SignRenewal`/`VerifyGuarantor` `Dispatch` gains
   `ContextParams: map[string]string{"leaseApp": "{context.leaseApp}", "applicant":
   "{context.applicant}"}`; `InputSchema` drops `leaseApp`/`applicant` from `properties` AND
   `required` (down to `["renewalKey"]` / `["renewalKey"]` — `VerifyGuarantor` keeps `method`
   optional, unaffected); `FieldDescriptions` drops the now-unrendered `leaseApp`/`applicant` entries.
   Version bump (`package.go:85`, currently `0.31.2`).
4. **FE wiring (`loftspace-app`):** Facts 3+4 — real-task lookup for the synthetic `SignRenewal` card
   button, `context.row` resolution in `openCatalogComplete`. Then **delete** `COMPLETIONS` entirely
   (it holds exactly these two entries — confirmed, `app.js:113-143`) along with `openLegacyComplete`,
   `submitLegacyComplete`, `renewsLinkKey`, `applicationForLinkKey`, and the `COMPLETIONS[...]`
   branches in `openComplete`/`submitComplete`/`descriptorFor` — dead code once both ops migrate, per
   house rules (no half-finished/backwards-compat scaffolding for code proven unused).
5. **Tests:** `internal/descriptorform/form.test.mjs` gains contextParams cases (required field
   auto-filled + excluded from rendered fields; a template that fails to resolve on a required
   contextParams field refuses) — `cmd/facet/web/descriptor_autofill.test.mjs` is a shape reference,
   not something to copy verbatim (different module, different context shape).

**Verify live (proves Fact 2's fix, not just the new code path):** reuse the running stack,
`refresh-loftspace` after the package bump, exercise `SignRenewal` as a signed-in tenant with an open
renewal cycle (both via the real Tasks-tab entry and the renewal-card button) and confirm it now
**succeeds** where it previously `AuthDenied`; exercise `VerifyGuarantor` as the landlord hat.

**Non-goals:** `CreateLocation`/`AttachObject`/`DetachObject` (named, unrelated blockers — untouched);
`SetRenewalTerms`/`CancelRenewal` (already migrated — touched only incidentally if `context.row`
wiring passes through their call site, behavior unchanged); the `?`-optional `contextParams` marker
(build only if free, per increment 2).

**Review depth:** posture-changing — new payload-population capability inside the shared,
authContext-adjacent module (`internal/descriptorform` owns `authContext` assembly) **plus** a live
authorization-bug fix. Full 3-layer adversarial pass mandatory before admit, cold reviewer, per the
Inc 2/3a/3b/3c precedent (§14/§15/§16/§17 each found real regressions this way).

## 19. Inc 3d outcome — loftspace tail, shipped `77aab58c`

Built per §18, with the mandatory full adversarial pass (cold, capability-plane-adjacent). The
builder caught and corrected §18's own grounding error before it shipped: `applicant`'s
`{context.<field>}` template must read `{context.tenant}`, not `{context.applicant}` — the renewals
row's real column name (`renewalsReadSpec`'s alias differs from the op's own payload field name).
Shipping §18 verbatim would have made `SignRenewal` refuse on every submission. The four
`op_catalog.go` files are not fully byte-identical (two comment paragraphs differ), contradicting
§18's Fact 1 claim; the struct region the fix touches is identical across all four, which is what
mattered.

The cold review found no blockers. One real-but-minor finding, fixed before merge: the shipped
`ContextParams` doc comment in `permissions.go` cited `renewalsReadSpec`'s RETURN aliases as the
`{context.<field>}` source — those are snake_case (`lease_app`) and never resolve; the actual source
is the client's own row shape (`renewals.go`'s JSON tags). Corrected in both descriptors, both citing
the client struct instead. Second finding (not fixed, not blocking): the new `contextParams`
exclusion in `form.mjs` is unconditional across the whole vocabulary, but only two of five template
forms are implemented (`{me.<type>}`/`{entity.<column>}` throw) — a live regression IF a future op
declaring one of those two forms is ever migrated onto this module. Census confirmed no such op is
migrated today. Flagged for whichever increment adopts the next `ContextParams`-bearing op.

CI's `convergence` job failed on push (`TestLeaseConvergence_BgcheckFreshness_EagerReopen`, an
unrelated background-check/PII-key-envelope freshness scenario, timing-window test under
`-tags leaseshortwindow`) — confirmed pre-existing host-timing flake, not a regression: the
immediately-prior commit's CI ran this same job green, this fire's diff never touches bgcheck
freshness/PII-envelope code, and a single re-run of just that job passed clean.

**Checkpoint for the next fire:** no worktree held once this lands. Inc 3 is DONE except for the
named per-app blockers left behind (clinic's five §15, wellness's four §16, café's four §17,
loftspace's `CreateLocation`/`AttachObject`/`DetachObject` — unaffected by this fire). Next up per §7:
**Inc 4, the ratchet** (per-app ceilings on the ungated `COMPLETIONS`-style debt, mutation-tested) —
posture-changing (new gate rule), full review depth.

census named. See `backlog/lattice.md`.

## 20. Inc 4 outcome — the ratchet, shipped `fc15558f`

Built per §5/§7: `lint-app-op-descriptors` gains `appOpCeilings`, pinning each vertical app's
distinct hardcoded-operationType-literal count exactly — above the pinned ceiling fails (a new
hand-wired op appeared while the catalog-driven renderer, live since Inc 1-3, could have described
it), below fails too (the `guardHelperFloors` discipline inverted from a `>=` floor to an `==` pin,
so migration progress that isn't recorded here reads as an amnesty), a missing app entry fails
(silent exemption), an orphaned entry fails (stale after a rename/removal).

The cold 3-layer adversarial pass (Blind Hunter / Edge-Case Hunter / Acceptance Auditor, mandatory
per §7's posture-changing call) found the definitive-context scan (`scanFile`) was line-local: a
pure reformat — a wrapped `submitOp(...)` call or a ternary split across `?`/`:` — could silently
drop an op from the count either direction, and Inc 4 turns that into a hard CI-red on an unrelated
diff, with the emitted message prescribing a permanent, wrong ceiling lowering. Fixed before ship:
`scanFile` now joins a still-open call/ternary across a bounded run of continuation lines (capped at
6) before extracting, with a `consumedThrough` guard against double-crediting the joined lines. That
fix is not cosmetic — it surfaced four `submitOp` calls in `clinic-app/web/app.js` (`CreatePatient`,
`SetAppointmentStatus`, `RescheduleAppointment`, `CreateBooking`) already wrapped in exactly this
shape and invisible to the pre-Inc-4 scanner; clinic's true baseline is **14** distinct ops, not the
naively-measured 10, independently re-verified live against the source before pinning. Also fixed: a
typo'd op literal no longer double-reports as both an R1 violation and a spurious ceiling delta (the
accounting moved to after the R1 `continue`), and the `appOpCeilings` doc comment no longer overclaims
"surface" when the pinned number is distinct op *literals* — a duplicate form referencing an
already-counted op does not move it, and the comment now says so plus discloses the scan's one honest
gap (a hoisted `const OP = "…"` is invisible to a line-local scan; the ceiling is a floor on hand-wiring
this scan can see, not a census).

Mutation-tested by hand, each verified to fire then reverted (no `scripts/*_test.go` exists for this
`//go:build ignore` corpus — `guardHelperFloors` carries the same precedent, §recorded there): ceiling
raised above true count, ceiling lowered below true count, entry deleted, orphaned entry added, a
wrapped `submitOp` call, a wrapped ternary (both arms), and a typo'd op literal — all seven behaved
exactly as designed. Baseline: `appOpCeilings = {cafe: 6, clinic: 14, loftspace: 20, wellness: 12}`,
`STRICT=1 go run ./scripts/lint-app-op-descriptors.go` reports `0 issues` clean.

CI step comments (`.github/workflows/ci.yml`) and the `Makefile` target doc both updated in the same
commit to mention the ratchet — the reviewer flagged both as stale (described only R1/R2/appOpDebt).

**Inc 4 closes staff-descriptor-rendering-design.md's §7 decomposition.** The design is DONE except
for Inc 3's four named, unaffected per-app residuals: clinic's five §15 blockers, wellness's four
§16, café's four §17, loftspace's `CreateLocation`/`AttachObject`/`DetachObject` (§7/§18) — each
tracked in its own `verticals.md` row, not this design doc's open work.

## 21. CreateLocation `Dispatch.ClassChoices` fire brief (Vertical Steward, committed before code)

Builds the first of §7's two named `CreateLocation`/`AttachObject`/`DetachObject` fixes — per §7's
own framing ("price the two honest fixes … and build one"), this fire ships `ClassChoices`; the
`AttachObject`/`DetachObject` upload-ceremony + owner-anchored read surface stays open debt, its
`appOpDebt` entry untouched.

**1. Scope sentence (verbatim, `verticals.md`).** "`CreateLocation`/`AttachObject`/`DetachObject`
stay hand-built with a named fix path unbuilt. Class-choice needs a `Dispatch.ClassChoices` enum
field; the attach pair needs an upload-ceremony affordance + owner-anchored read surface
(`signInMethods`-pane precedent). Baselined in `appOpDebt`." Green bar: `location-domain` ships a
full `pkgmgr.OpMetaSpec` for `CreateLocation` naming `Dispatch.ClassChoices: ["unit","building",
"property"]` (no static `Class`), the op-catalog lens + all four apps' `/api/op-catalog` proxy
project it, `internal/descriptorform/form.mjs` renders a class-choice op as an enum select and
resolves the picked choice to the envelope's `class` field at submit, `appOpDebt`'s
`"CreateLocation"` entry is deleted, and `STRICT=1 go run ./scripts/lint-app-op-descriptors.go`
reports 0 issues.

**2. Verified touch-list** (checked live 2026-08-23):

- `internal/pkgmgr/definition.go:751-762` — `OpDispatchSpec` struct: add `ClassChoices []string`
  field, documented alongside `Class` (line 752).
- `internal/pkgmgr/build.go:673-710` (`opDispatchBody`) — emit `body["classChoices"]` when
  non-empty, mirroring the existing `Reads`/`OptionalReads` `[]string`→`[]any` conversion
  (lines 694-700).
- `packages/edge-manifest/lenses.go:661` (`edgeCatalogTail`) and `:736` (`opCatalogSpec`) — add
  `op.dispatch.data.classChoices AS dispatchClassChoices` immediately after the existing
  `dispatchClass` line in both `RETURN` clauses (no `WITH`; §opCatalogSpec's own load-bearing-shape
  comment at lines 685-719 applies unchanged — copy the RETURN column, never the opener).
- `packages/edge-manifest/lens_cypher_test.go:596-604` (`emOpCatalogWorld`'s `fullOp` dispatch
  aspect fixture) — add a `"classChoices": []any{"x", "y"}` entry; `:681`
  (`TestOpCatalog_FullVocabularyOpProjectsEveryColumn`) — add
  `require.Equal(t, []any{"x","y"}, row["dispatchClassChoices"])` beside the existing
  `dispatchClass`/`dispatchReads` assertions, so the new column is mutation-tested through the same
  positive vector as the rest of the vocabulary (never left to a bare compile check).
- `cmd/{clinic,cafe,loftspace,wellness}-app/op_catalog.go:19-100` (all four, identical shape) —
  `opCatalogProjection`: add `DispatchClassChoices []string \`json:"dispatchClassChoices"\`` beside
  `DispatchClass` (line 34); `opDispatch`: add `ClassChoices []string
  \`json:"classChoices,omitempty"\`` beside `Class` (line 92); `toDescriptor()`'s dispatch-presence
  check (line 157-159) and construction (line 160-169): add `len(p.DispatchClassChoices) > 0` to the
  OR-chain and `ClassChoices: p.DispatchClassChoices` to the struct literal.
- `internal/descriptorform/form.mjs`:
  - `:253` (`normalizeCatalogRow`'s refusal) — change `if (!dispatch.class) return null;` to
    `if (!dispatch.class && !(dispatch.classChoices && dispatch.classChoices.length)) return null;`.
  - New: when `dispatch.classChoices` is set and `dispatch.class` is not, render one extra `<select>`
    control (mirroring the existing `enum` field-kind branch's option-building + `titleCase`
    labelling, `fieldKind`/`buildField` lines 60-70 and 127-234) ahead of the schema-driven fields,
    with a synthetic field id (e.g. `__classChoice`) excluded from the submitted `payload`.
  - `:536` (`submit()`'s envelope assembly) — resolve `class: dispatch.class || readClassChoice()`
    instead of the current bare `dispatch.class`.
- `internal/descriptorform/form.test.mjs` — add a node test asserting: (a) a row with `classChoices`
  and no `class` renders the select and is NOT refused by `normalizeCatalogRow`; (b) `submit()`
  sends the selected choice as `class`; (c) the anti-fallback rule still holds — a row with neither
  `class` nor `classChoices` is refused exactly as today (regression pin for the line-253 change).
- `packages/location-domain/opmetas.go` (new file, mirrors `packages/clinic-domain/opmetas.go`'s
  shape) — `func OpMetas() []pkgmgr.OpMetaSpec` returning one full `CreateLocation` entry:
  `Presentation.Title`, `InputSchema` reusing the existing `locationType`/`presentation` properties
  already defined at `ddls.go:168-170` (a `locationType` enum property is REQUIRED input, matching
  `LocationTypes` at `ddls.go:88`), `FieldDescriptions`, `Dispatch: {ClassChoices: LocationTypes,
  AuthContext: "standing"}` (operator-only per `permissions.go:28`'s `mk("CreateLocation")` — no
  consumer/self grant exists, so "standing" per the clinic-domain precedent's
  `SetProviderHours`/`SetProviderTimeOff` doc comment, lines ~53-57) — no `TargetField` (free-choice
  create, per form.mjs's own doc comment lines 32-38).
- `packages/location-domain/package.go:58` — wire `OpMetas: OpMetas()` into `Package`; version bump
  `0.3.2` → `0.4.0` (new install-time aspect, `docs/reference_package_edit_needs_version_bump`
  convention).
- `packages/location-domain/manifest.yaml` — add `declares.opMetas: [{operationType:
  CreateLocation}]` (minimal shape — mirrors `clinic-domain/manifest.yaml:273-286`, which the
  `VerifyAgainstDefinition` check does not deep-compare beyond `operationType`) and bump `version:
  0.3.2` → `0.4.0`.
- `packages/edge-manifest/package.go:26` — version bump `0.16.9` → `0.17.0` (lens cypher changed);
  `packages/edge-manifest/manifest.yaml` — matching bump.
- `packages/location-domain/opmetas_test.go` (new) — mirror `clinic-domain/opmetas_test.go`'s
  `TestOpMetas_DispatchClassMatchesOwningDDL` pattern, adapted: assert `CreateLocation`'s
  `Dispatch.Class == ""` (no static class — the whole point) and `Dispatch.ClassChoices` is exactly
  `LocationTypes` (`["unit","building","property"]`, order-insensitive), plus every choice names a
  DDL that actually admits `CreateLocation` in its `PermittedCommands` (cross-check against
  `DDLs()`, same shape as the mirrored test's `classForOp` map).
- `scripts/lint-app-op-descriptors.go:163-165` — delete the `"CreateLocation": "location-domain"`
  line from `appOpDebt` (shrink-only ledger; `fullDescriptor(CreateLocation)` now returns true, so
  the entry becomes a dangling-baseline failure if left in place).

**3. Precedents mirrored.**
`packages/clinic-domain/opmetas.go`'s `OpMetas()` shape + doc-comment convention (op-meta
authoring); its sibling `opmetas_test.go`'s `TestOpMetas_DispatchClassMatchesOwningDDL` (adapted for
the no-static-Class case); `internal/pkgmgr/build.go:694-700`'s `Reads`/`OptionalReads`
`[]string`→`[]any` aspect-body conversion (for `ClassChoices`); `form.mjs`'s existing `enum`
field-kind rendering (for the class-choice `<select>`); `edgeCatalogTail`/`opCatalogSpec`'s existing
column-pair-per-field pattern (add one column, both cyphers, no `WITH`). No greenfield: every touch
point extends an existing, shipped vocabulary column by exactly the same shape its siblings already
use.

**4. Increment order + green checks.**

1. `internal/pkgmgr` (`definition.go` + `build.go`) — `go build ./internal/pkgmgr/...` and
   `go test ./internal/pkgmgr/...`.
2. `packages/edge-manifest` (lenses.go cypher + lens_cypher_test.go) — `go test
   ./packages/edge-manifest/...` (mutation-tested via the new `dispatchClassChoices` assertion).
3. The four `cmd/*-app/op_catalog.go` projections — `go build ./cmd/... && go test ./cmd/...`.
4. `internal/descriptorform/form.mjs` + `form.test.mjs` — `node --test
   internal/descriptorform/form.test.mjs` (or the package's existing node-test invocation).
5. `packages/location-domain` (opmetas.go + package.go + manifest.yaml + opmetas_test.go) — `go
   test ./packages/location-domain/...`.
6. `scripts/lint-app-op-descriptors.go` baseline edit — `STRICT=1 go run
   ./scripts/lint-app-op-descriptors.go` reports `0 issues`.
7. Whole-repo gates: `go build ./...`, `make vet`, `golangci-lint run ./...`, `STRICT=1 go run
   ./scripts/lint-conventions.go`, `go run ./scripts/lint-package-version.go`, `go run
   ./scripts/lint-manifest-entity-type.go`.
8. If the shared stack is up: `make verify-package-location-domain` and `make
   verify-package-edge-manifest` live (skip with a note if no stack is running — self-contained unit
   coverage above already proves the mechanism).

**5. In-scope gotchas.**
Package edits need version bumps (both `location-domain` and `edge-manifest` — the lens cypher
changed, not just an app). `lint-package-version` gates this. `location-domain`'s
`TestPackage_ManifestMatchesDefinition` will fail the moment `OpMetas()` is wired into `Package`
without the matching `manifest.yaml` `opMetas:` block — add both in the same commit. The
`opCatalog` lens is a PLAIN nats-kv lens (no anchor/reachability walk) — a hot-reload via
`refresh-edge-manifest` (if the gate is exercised live) needs no bootstrap restart. Standing
checklist: (#2) the `LocationTypes` / `PermittedCommands` cross-check in the new
`opmetas_test.go` is itself a census — assert it against the live `DDLs()` return, never a copied
literal list, so a future fourth leaf type can't silently go undescribed.

**6. Adjacent finds.** None expected — this is a narrow vocabulary-threading fire through an
already-fully-mapped pipeline (all touch points above traced live before this brief was written).
Any find surfacing mid-build gets fixed in this fire if it touches the same mechanism, or routed per
steward §0/§4 (needs-Andrew / needs-designer-pass) otherwise — never filed as a bare residual.

**7. Non-goals.** `AttachObject`/`DetachObject`'s upload-ceremony + read-surface fix (stays baselined
in `appOpDebt`, unaffected). Migrating `loftspace-app`'s or `clinic-app`'s hand-wired `CreateLocation`
call sites (`app.js:3993`, `app.js:1008`) onto the descriptor-driven renderer — the gate only
requires the op carry a full descriptor, not that every app consume it via `form.mjs`; the hand-built
forms remain tolerated debt per `lint-app-op-descriptors`'s own `fullDescriptor` branch.
`descriptorform`'s broader 6-op vocabulary gap (array/multiline/ceremony field kinds, the separate
`📐 needs designer pass` backlog row) — `ClassChoices` is one narrow, already-scoped addition to an
existing enum-rendering path, not that redesign.

### Outcome — shipped `aa04a7a5`

Built exactly per the increment order above; all seven whole-repo gates plus the touched-package
test suites and `node --test internal/descriptorform/form.test.mjs` (37/37) green, independently
re-verified after merge (a concurrent Lattice-stream fire had advanced `main` by 121 files in the
interim — clean auto-merge, zero line-range overlap with this fire's touch-list; full `go test
./... -p 4` re-run green post-merge). `CreateLocation`'s `appOpDebt` entry is deleted;
`AttachObject`/`DetachObject` remain, tracked in `verticals.md` as their own row.

Three mechanically-required fixups outside the original touch-list, each a direct consequence of
adding one RETURN column to `opCatalogSpec`/`edgeCatalogTail` (not a scope widening): the
`composed_test.go` frozen golden-cypher mirror, `location-domain/package_test.go`'s
zero-op-metas scope pin (now asserts exactly one), and one reddened corpus-census pinned verdict
(`internal/refractor/grouping_reduction_corpus_census_test.go`'s `opCatalog` key string) — reviewed
against the live cypher before repinning, per
[[reference_corpus_census_pinned_verdicts]].

**Found, reviewed, no fix owed:** `internal/pkgmgr/capabilitymaterializer_starlark.go`'s
`OpDispatchArtifact` (the AI-authored-capability JSON vocabulary) does not expose `ClassChoices`.
This is not a gap this fire introduced or is on the hook for — the AI-authoring vocabulary is
*already*, deliberately, a narrower subset of the full human-authored `OpDispatchSpec` (it excludes
`VisibleWhen` too, added well before this fire with no corresponding AI-vocabulary extension). An
AI-authored package proposing a multi-DDL create is exactly the kind of judgment call
(sensitive-read-adjacent, needs a human to pick the DDL boundary) the existing narrower vocabulary
routes to human authoring by omission — consistent with, not a regression from, the standing
posture. No row filed.

## 22. AttachObject/DetachObject residual — decomposition + Inc-A outcome (Vertical Steward)

`verticals.md`'s own row for this residual quotes §7's original call almost verbatim: "the fix is
an upload-ceremony affordance plus an owner-anchored attachments read surface, the `signInMethods`-
pane precedent — not an exemption marker." Grounding before building found the row understated the
gap's two halves as one unit; they decompose cleanly and carry different risk, so this residual
builds as two increments, self-ratified per §0 (both are execution-level — mirroring an established
pattern, not a new architectural fork):

- **Inc-A (this fire) — extract the ceremony into a shared module.** `cmd/loftspace-app` already had
  a full, working, well-documented AttachObject/DetachObject client implementation (Fire 2b, #75) —
  crypto-derived Contract #4 requestId, envelope assembly, the browser-direct submit. `appOpDebt`'s
  own comment (`ddls.go` OpMetas(), pre-fire) named the real gap correctly: not that this mechanism
  is wrong, but that it existed exactly once, in one app, unavailable to café/clinic/wellness without
  each re-deriving ~100 lines of crypto/dedup logic by hand. That is a mechanical extraction, not new
  design — moving working, reviewed code into `internal/descriptorform/attachments.mjs` (mounted
  at `/shared/attachments.mjs`, the same pattern `form.mjs` already established) and migrating
  loftspace-app's two wrappers onto it, parameterizing only what genuinely differs per app (the
  upload transport, the submit path, the dedup namespace).
- **Inc-B (checkpoint, not started) — the owner-anchored lens.** `objectAttachments` (this package's
  existing lens) is anchored on the OBJECT, the wrong direction for a generic op-catalog `Dispatch`
  pointing DetachObject at "the attachments owned by X": every `AnchorType` in this codebase is a
  single fixed vertex label (`internal/pkgmgr/anchorwalk.go`), and AttachObject's targets are
  type-agnostic (D7) — identity, unit, and whatever else a future caller names. A single reversed-
  direction lens can't express that without either enumerating one lens per concrete owner type
  loftspace actually uses (small, mirrors `objectAttachments` almost exactly, just the anchor and
  walk direction swapped) or a genuinely type-agnostic anchor (an engine capability with no
  precedent — would need `lattice.md` filing under the no-paper-over rule, §2). Which shape to build
  is a real but bounded decision, deferred rather than guessed at under this fire's scope. Until it
  ships, DetachObject cannot carry a full `OpMetaSpec` `Dispatch` and stays a client-mechanism op —
  which Inc-A already makes an honest, shared one instead of a bespoke, undocumented one.

### Inc-A outcome — shipped (this commit)

Built exactly as scoped: `internal/descriptorform/attachments.mjs` (deriveNanoID — cross-checked
byte-for-byte against `internal/substrate`'s `TestDeriveNanoID_Golden` vectors, `EjraDYAJJPP3GXkv8ooM`
and `5CYJnWeWpVNco5MnqAH6`, both reproduced exactly — objectLinkKey, attachObject, detachObject) +
`attachments.test.mjs` (9 vectors: the golden cross-check, determinism/namespace isolation, envelope
assembly incl. the sensitive-fields fold, requestId retry-stability, rejected-reply throw/no-throw
per function). `embed.go` embeds both `.mjs` files under one `FS()`; `mount_test.go` gained a
same-mount serving proof for `attachments.mjs` mirroring the existing `form.mjs` one.
`cmd/loftspace-app/web/app.js`'s `attachObject`/`detachObject` become thin wrappers supplying this
app's own upload transport (`appPost("/api/objects", …)`) and submit path (`submitOp`) — every
existing call site (listing photos, the Documents tab, the PII upload ceremony) is unchanged, since
both wrappers kept their exact prior signature. `deriveNanoID`/`NANOID_ALPHABET`/`NANOID_LENGTH`/
`objectLinkKey` deleted from `app.js` (no longer duplicated).

**Gate mechanics, verified rather than assumed:** `lint-app-op-descriptors`'s R1/R2 scan is scoped to
`cmd/*-app` sources only (the same carve-out `form.mjs` already relies on) — moving the
`operationType: "AttachObject"`/`"DetachObject"` literals into `internal/descriptorform` took them
out of that scope, which STRICT-mode re-run confirmed mechanically: loftspace-app's measured distinct
op-literal count dropped from 20 to 18, and `appOpDebt`'s two entries became dangling ("no app still
violates — delete the entry"). Fixed in the same diff: `appOpCeilings["cmd/loftspace-app"]` lowered
20→18 (comment explains why, mirroring the `EndVisitSeries` precedent already in the map), both
`appOpDebt` entries deleted (map now empty), and `packages/objects-base/ddls.go`'s `OpMetas()`
comment amended in place (the falsified-claim-gets-amended-where-it-stands rule) to record Inc-A
shipped and point at Inc-B as the remaining gap — not a bare "fixed" claim with no citation.

Green bar: `go build ./...`, `make vet`, `golangci-lint run ./...` (repo-wide), `STRICT=1 go run
./scripts/lint-conventions.go`, `STRICT=1 go run ./scripts/lint-package-standard.go`, `STRICT=1 go run
./scripts/lint-app-op-descriptors.go` (0 issues, was 3 before the ceiling/debt fix), `go test
./internal/descriptorform/... ./packages/objects-base/... ./cmd/loftspace-app/...`, and `make
test-descriptorform` (46/46, node --test) all green. Review depth: lead review only (Steward's own
sizing per §7 — a mechanical extraction of already-shipped, already-reviewed logic, no new grant, no
new trust boundary; `operator`'s existing `AttachObject`/`DetachObject` scope:any is unchanged).

### Inc-B outcome — the self-scoped owner-anchored lens (Vertical Steward)

Grounded against loftspace's actual live `AttachObject` callers (wellness has none yet, `cmd/wellness-app`
never calls it): three owner types — `identity` (applicant document uploads), `leaseapp` (per-application
document uploads), `unit` (landlord listing photos) — each `object -[linkName]-> owner`. The shape decision:
**anchor stays on `object`** (not the owner), mirroring `identityCredentialBindingsRead`
(identity-domain/lenses.go), not a reversed owner-anchored actorAggregate — a flat Postgres row per
(object, owner, slot) triple is what an edge-manifest pane section needs (one row per dispatch-target
candidate), and `objects-base`'s own architecture rule ("it never learns concrete owner types") means a
lens naming a concrete owner type belongs in the owning vertical's package, not `objects-base` — the same
split `applicantRosterRead`/`landlordUnitsRead` already draw.

**Shipped this fire:** `objectIdentityAttachmentsRead` (`packages/loftspace-domain/lenses.go`) — the
self-view case only (`owner:identity`, unambiguous authz: an identity always sees its own documents).
`leaseapp` and `unit` are NOT shipped: each raises its own open authz question this fire's scope didn't
need to answer — does a landlord see an applicant's PII at decide-time (leaseapp), and are listing photos
RLS-scoped at all or public like `availableListings` (unit) — real product/security decisions, not
mechanical mirrors, so guessing at them here would have been exactly the risk the no-paper-over rule warns
against. `DetachObject`'s `OpMetaSpec.Dispatch` is **NOT wired** — see the Inc-C gap below, discovered while
grounding the wiring, which blocks it independent of how many owner-type lenses exist.

**A second, independent blocker (Inc-C): the Reads-template vocabulary can't express a type-agnostic link
key.** `DetachObject`'s declared read must include the tombstoned link's own key,
`lnk.object.<oid>.<linkName>.<ownerType>.<ownerId>` — Contract #2 §2.5's declared-reads discipline requires
it. `internal/processor/descriptor_floor.go`'s `expandDescriptorTemplate` resolves `{payload.<field>}` two
ways only: bare (`:id`) yields the Contract #1 id, whole yields the FULL `vtx.<type>.<id>` value (own dots
and all) — there is no operator that yields just `<type>`, the segment a link key needs mid-template. Every
existing `TargetField`/`Reads` precedent (wellness-domain's `ReassignSession`, clinic-domain's
appointment/provider ops) declares reads against a FIXED type, because every existing type-agnostic op
(`AttachObject`/`DetachObject`, D7) has stayed undispatchable until now — this is the first time the gap
was reached, not a known, deferred one. No established pattern to mirror exists for it (a candidate — adding
a redundant `ownerType` payload field solely so `{payload.ownerType}` can supply a literal segment, unused
by the DDL script itself — has no precedent either way). Flagged to the Lattice stream (not filed directly —
the Vertical Steward's run scope is `verticals.md` only) under `📐 needs designer pass · no-pattern:
type-agnostic link-key Reads-template segment`, since it is a Processor/descriptor-floor capability question
(an engine primitive), not a package-level pattern to extend, and it also gates `leaseapp`/`unit` even once
their own authz questions are answered.

**Checkpoint for the next fire (Inc-C):** no worktree held (docs-and-small-package-edit convention). Next
steps, independent of each other: (1) the Reads-template gap once it lands in `lattice.md`, Lattice-lane
work; (2) once answered, ground+ship the `leaseapp` and `unit` lenses (their own authz decisions, see
above) + wire `DetachObject`'s `Dispatch` against all three. `verticals.md`'s row carries the pointer.
