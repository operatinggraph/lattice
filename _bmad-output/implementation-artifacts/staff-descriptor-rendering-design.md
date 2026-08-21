# Staff descriptor rendering — the op catalog read model + the shared form renderer

**Status: 📐 awaiting-Andrew (ratification).** Designed 2026-08-20 (Winston, Andrew-directed
session). **Board row:** verticals lane · *Staff FEs render op forms from descriptors, not
hand-built JS* (★★★ XL). **Demand:** the 2026-08-20 audit
(`docs/reviews/vertical-app-descriptor-audit-2026-08-20.md`) — ~70 hardcoded submission sites /
~7,500 lines across the four vertical FEs re-implementing what `OpMetaSpec` already declares —
plus the codebase's own ask at `cmd/loftspace-app/web/app.js:76`: *"the generic
DDL-self-describing form needs an op-catalog read model — a Core-KV op-meta scan would violate P5
in a vertical app."* **Precondition:** the 15-op descriptor sweep (appOpDebt → empty), running as
its own fire.

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
pinned copies) are resolved below with grounded reasoning. Ratifying this unblocks Inc 1.

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
  deleting their map entries and the `app.js:76` lament. `SignRenewal`/`VerifyGuarantor` remain a
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
