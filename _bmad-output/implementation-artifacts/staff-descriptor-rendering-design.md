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

## 15. Inc 3a outcome — clinic (partial), shipped `<pending>`

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
