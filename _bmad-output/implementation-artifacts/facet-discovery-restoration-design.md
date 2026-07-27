# Facet discovery restoration — the covenant, re-established structurally

**Status: DIRECTED (Andrew, 2026-07-27) — build immediately.** Not a ratification request: Andrew
ordered the drift stopped, the damage analyzed, and the fix executed with a lint gate so it cannot
recur. This doc is the damage analysis, the mechanism, and the fire brief.

## 1. What happened (damage analysis)

The founding covenant (edge-showcase-app-design.md §1): Facet's **entire hardcoded surface** is
(1) the OIDC/login flow, (2) two base URLs, (3) a fixed service-agnostic descriptor-vocabulary
interpreter. "A new service … appears in the app with **zero app change**."

A full audit of `cmd/facet` (all 20 .go files, all 22 web files) against the four ratified design
docs finds the covenant no longer holds. The erosion was **not rogue commits** — nearly every
violation traces to an individually-ratified widening ([APP] carve-outs in
facet-staff-worlds-design §3.4 and facet-entity-browse-design §9). Each fire was locally sanctioned;
no gate measured the aggregate. That is the process failure: ratification judged features, nothing
guarded the covenant.

### 1.1 The violation catalog

**Go host — one coherent cluster (11 findings), the staff worklist pane:**
- `staff.go` hardcodes three per-vertical row schemas (`staffApplicationRow` — LoftSpace lease
  review; `staffAppointmentRow`, `staffVisitSeriesRow` — clinic), three SQL consts naming lens
  tables (`read_landlord_lease_applications`, `read_clinic_appointments`, `read_visit_series`),
  a domain workflow predicate (`landlord_decision IS NULL`), a PHI column narrowing chosen in app
  code, clinic dispatch pairings (`vtx.patient` → `StartVisitSeries`, `vtx.visitseries` →
  `Pause/ResumeVisitSeries`), a fixed three-section response shape, and a vertical-naming error
  string. `server.go:85` registers the pane route.
- Adding any new vertical's staff workflow requires editing the app — the exact
  "every existing vertical FE hardcodes its world" posture §1 was written against.

**Web client — 9 violations in `app.js`:**
- `TYPE_LABELS` (app.js:32-51): per-type display map, 11+ vertical types, plus domain framing on
  platform types (`identity → "Resident"`, `provider → "Clinician"`, `session → "Class session"`).
- `RELATIONAL_SUFFIX` (app.js:150): `{leaseapp: "lease"}` display rule.
- `identityLabel` fallback `"Resident"` (app.js:163-167).
- `ICONS` map with vertical token names `laundry`, `wellness` (app.js:177-178).
- The worklist cluster (app.js:967-1170): three-section state shape, **three clinic operationType
  literals** (`StartVisitSeries`/`PauseVisitSeries`/`ResumeVisitSeries` looked up by name), three
  per-vertical row renderers with column vocabulary and workflow branching (`active` → Pause else
  Resume), per-vertical headings and empty copy.
- Four test files (`hat_worlds`, `staff_worklist`, `work_orders`, `display_label`) pin these
  behaviors as asserted.

**Gray, adjudicated KEEP (ratified, passes zero-app-change):** the identity-plane ceremony ops in
`credentials.go`/`claim.go` (`ClaimIdentity`, `Initiate/CompleteCredentialLink`,
`UnlinkCredential` — ESAD §7.1/§7.2 Inc 3; their undiscoverability is already board-filed as
vocabulary work), the `claimTask` affordance (FEB §6.1), the identity-spine relation names
(`worksAt`/`identifiedBy` — staff/persona designs), the `manifest.work` work-order archetype
(FSW F5 Inc 2), the `/Cents$/`+`x-format:"money"` widget rule (FUX §3.6), `read_identity_credentials`
(ESAD §7.2 Inc 3). These enter the lint allowlist **by citation**, so they stay visible debt, not
silent precedent.

**Clean:** `boot.mjs`, `edge-source.mjs`, `login.html`, `style.css`, the SwiftUI spike (zero domain
hits — the architecture works when honored), and the entire enqueue/feed transport (genuinely
pass-through descriptor-driven).

### 1.2 Why it matters

Facet is the showcase whose one-line pitch is "discover, don't hardcode." Today a new vertical gets
raw-segment labels while shipped verticals get curated ones; a new staff workflow requires editing
three layers of the app; three clinic op names are load-bearing strings in the renderer. The demo
still works — the THESIS is what broke.

## 2. The fix — move the knowledge, keep the security

The Protected-pane architecture (server-side, RLS as the signed-in actor, workplace-anchored
grants, never mirrored, PII off the SYNC plane) is **sound and untouched**. What moves is the app's
knowledge of *which* panes, tables, columns, labels, and ops exist. The designs' own remedy clause
governs every fix: *"add the lens/op-meta (data) or file the design — never widen the app."*

### 2.1 Pane descriptors (`manifest.pane.*`) — the flagship

A new row family on the SYNC plane, projected by `packages/edge-manifest`:

- **`edgeStaffPanes` lens** (nats-subject, Personal, identity-anchored via the staff spine walk —
  the `edgeStaffWorkOrders` pattern). One `manifest.pane.<paneId>` row per pane the identity's
  grant topology earns. Rows are pane METADATA only — titles, section descriptors — never PII, so
  they ride the mirror legitimately (D3 intact).
- **Descriptor shape** (flattened per the as-built convention; `sections` as a JSON string column,
  like `inputSchema`): per section `{id, title, emptyCopy, source: {table, columns:
  [{name, label, kind: text|datetime|date|money|badge|hidden, role: title|subtitle|meta|badge|target,
  valueLabels?, default?}], filter: {kind: none|isNull|utcDay, column}, orderBy: {column, dir,
  nullsLast, tieBreak}, limit}, dispatch: {targetColumn, targetType}}`.
- **The three current sections become one "worklist" pane descriptor authored in the package** —
  the lease-review column list, the clinic schedule's PHI narrowing, and the visit-series shape all
  move to package data, where domain knowledge legitimately lives. The PHI gate
  (`staff_test.go`'s banned-clinical-columns test) moves with it as a package test.
- **Host:** `staff.go` becomes a generic pane executor. `GET /api/pane?key=…`: acquire the session
  identity's engine, read `manifest.pane.<id>` from **its own mirror** (the host-side bbolt fed by
  the authenticated per-identity consumer — not from anything the browser sends), validate against
  a strict grammar (`^read_[a-z_]+$` table, `^[a-z_]+$` columns, fixed filter kinds, LIMIT ≤ 200),
  compile the SELECT with quoted identifiers, run it under RLS exactly as today
  (`set_config('lattice.actor_id', …)`, SELECT-only role), return generic
  `{sections: [{id, rows}]}`. Descriptor-compiled SQL is the same trust shape as Refractor
  compiling lens DDL — package data driving a confined query surface.
- **Client:** panes discovered from `rowsByNs("manifest.pane")`; the Worklist archetype renders
  sections from declared columns/kinds/roles; badges from `valueLabels`; dispatch targets flow
  `{targetColumn, targetType}` → the existing generic `opButton`/`resolveTargetKey` seam. The three
  op-name literals die: ops are offered by `dispatchTargetType` match, as everywhere else.
- **Conditional offering:** `OpDispatchSpec` gains optional `visibleWhen {field, equals}`
  (additive, per the vocabulary's own evolution rules; spec-doc-not-contract per FORK-1 B) —
  evaluated against the target row's columns. `PauseVisitSeries` declares
  `visibleWhen {active: true}`, `Resume` the inverse, in **clinic-domain op-meta data**. The
  client's active→Pause branch becomes generic descriptor evaluation, available to every future op.

### 2.2 Labels, icons, copy — data or neutral

- `TYPE_LABELS` deleted. Every lens that stamps an `entityType` literal also projects a
  `typeLabel` column (package-side, where the literal already legitimately lives); `manifest.me`
  anchors/bindings gain `typeLabel` the same way. The client builds its label ladder from observed
  rows at runtime; fallback stays `titleCase(segment)`. A new vertical now gets curated labels the
  same way shipped ones do — by projecting them.
- `RELATIONAL_SUFFIX` deleted; the task lens projects the composed scoped-label phrase.
- `identityLabel` fallback → neutral (`"You"`); `identity`/`provider`/`session` domain framings die
  with the map.
- `ICONS` stays (ratified: "icons are semantic tokens from a small fixed set; the client owns all
  pixels") but vertical token names are renamed semantic (`laundry`→`basket`, `wellness`→`lotus`),
  with the corresponding one-word updates in the two packages' presentation data.
- Vertical words scrubbed from copy ("No orders yet", "Nothing nearby to book yet") and from
  comments (several already stale — e.g. app.js:606-610 asserts instructor/serviceprovider chips
  are inert; they aren't since `2c41318b`). Comments describe the mechanism, not a vertical.

### 2.3 Tests

`staff_worklist`, `work_orders`, `hat_worlds`, `display_label` (.mjs) and `staff_test.go` rewrite
against the generic mechanisms — domain-shaped **fixture data stays** (fixtures are data; the gate
binds sources, not tests). The PHI-narrowing test moves to `packages/edge-manifest`.

## 3. The gate — `scripts/lint-facet-discovery.go`

Default-deny, per the lint house rule: the gate cannot judge intent, so the author declares.
Non-test sources under `cmd/facet` (Go + web, **comments included** — that is where stale vertical
facts accumulated):

- **R1 — key shapes:** `vtx.<type>` literals allowed only for `{identity, task, meta}` +
  `credentialindex` (cited to its board row).
- **R2 — SQL tables:** literal `FROM` targets allowed only `read_identity_credentials` (cited).
  The pane executor names no table — tables arrive as descriptor data.
- **R3 — vertical canary:** a word-boundary ban on unambiguous vertical vocabulary (clinic, cafe,
  wellness, loftspace, laundry, patient, visitseries, menuitem, leaseapp, lease, appointment,
  studio, booking, clinician, resident, applicant, tenant, landlord, + seed persona/place names).
- **R4 — op literals:** `operationType` string literals allowed only
  `{ClaimIdentity, InitiateCredentialLink, CompleteCredentialLink, UnlinkCredential, ClaimTask}`,
  each carrying its design citation in the allowlist.
- Allowlist entries live **in the gate source with a required citation string** — widening the
  surface means editing the gate in the same diff, which is the visibility this fire exists to
  create. `STRICT=1` in CI (`lint-build` job), Makefile target, listed with the other gates.

## 4. Build order (one fire, internal sequence)

1. `visibleWhen` on `OpDispatchSpec` + clinic-domain op-meta adoption (+ version bumps).
2. `edgeStaffPanes` lens + the worklist pane descriptor + `typeLabel` projections
   (+ edge-manifest version bump; package tests incl. the relocated PHI gate).
3. Host: generic pane executor replaces `staff.go`; route swap; tests.
4. Client: descriptor-driven worklist + label-ladder-from-data + icon/copy/comment scrub; .mjs
   test rewrites.
5. The lint gate + Makefile + CI wiring; run every `scripts/lint-*.go`.
6. Docs: FSW §3.4 and FEB §9 amended in-body to point here; ESAD §3.2 vocabulary note
   (`manifest.pane`, `visibleWhen`); board row + Done log.

Verification: `go build ./...`, `make vet`, `golangci-lint run ./...`, `make verify-kernel`,
`go test ./cmd/facet/... ./packages/edge-manifest/... ./packages/clinic-domain/... ./internal/pkgmgr/...`,
node .mjs suite, all lint gates STRICT, then full `go test ./... -p 4` (shared-vocabulary change —
full-suite rule), live check on the running stack (worklist renders from descriptor, Pause/Resume
still correct), CI green on the full SHA.

## 5. As-built (Winston, 2026-07-27 — SHIPPED, one commit)

Everything in §2–§3 shipped as designed, in build order §4, single code commit; amendments landed
in-body in FSW §3.4, FEB §9, ESAD §3.2, and `docs/components/edge-manifest.md`.

- **Vocabulary:** `pkgmgr.PaneSpec` (+ manifest `panes:` block + install emission `meta.pane` /
  `.paneDescriptor` / `offeredTo` links, ids `entityNanoID(pkg, "pane:<name>")`, roles resolved via
  the GrantsTo machinery) and `OpDispatchSpec.VisibleWhen {Field, Equals}` (emitted into
  `.dispatch`, projected as `dispatchVisibleWhen`). Both additive — an unadopted package installs
  byte-identical.
- **Package data:** `edge-manifest 0.14.0` — `edgeStaffPanes` lens (18th; `domainStaff` walk) +
  the `staffWorklist` descriptor (`panes.go`) + `typeLabel` on all 9 entity-stamped tails +
  `panes_test.go` (identifier grammar, the relocated clinical-column ban, the state-column pin).
  `clinic-reminders 0.7.0` — Pause/Resume declare `visibleWhen {active}`.
- **Host:** `staff.go` (285 lines, 3 row structs, 3 SQL consts) → `pane.go`, one generic executor:
  descriptor read from the session identity's own mirror (its presence IS the role gate),
  independent grammar revalidation, quoted-identifier SQL compile, RLS spine unchanged.
  `pane_test.go` compiles the REAL shipped descriptors against the ported pins (no
  actor/workplace predicate; state columns never filtered; half-open UTC day; null costs a field).
- **Client:** `TYPE_LABELS`/`RELATIONAL_SUFFIX`/"Resident"/vertical icon tokens deleted; worklist
  rendered from descriptors (roles/kinds/valueLabels; ops offered by `dispatchTargetType` match,
  gated by `opVisibleForRow` — fail-closed on a missing state field); comments de-verticalized.
  141 node vectors green, incl. the new `pane_render.test.mjs` (24).
- **Gate:** `scripts/lint-facet-discovery.go` — R1 key types / R2 SQL tables / R3 vertical canary
  (comments included) / R4 op literals, each allowlist entry citation-bearing; Makefile target +
  CI `lint-build` step, STRICT.
- **Verification:** full `go test ./... -p 4` exit 0; `go build`, vet, golangci-lint,
  `verify-kernel`, all six lint gates STRICT clean.
- **Recorded judgment calls:** a null state column now offers NEITHER half of a state pair at the
  client (host applies declared defaults before serving, so the shipped descriptor reproduces the
  old COALESCE behavior; a descriptor omitting a default fails closed — stricter than the old
  hand-rolled branch). Binding chips' type words fall back to `titleCase(type)` until
  `edgeIdentity`'s anchors project `typeLabel` (the mechanism exists; a lens-side edit when a
  design wants curated hat words — cosmetic, no row filed). Icon tokens `basket`/`lotus` replaced
  the two vertical-named seeds' tokens.

## 6. Live-walk follow-on (Winston, 2026-07-27 — same day, Andrew's signed-in findings)

A multi-hat walk (Sam Okafor) surfaced four root causes; three fixed and verified live, one
grounded and filed:

- **Picker path** (`bb9fe41a`): an op whose `dispatch.targetType` is unresolvable from context is
  still offerable when its target field declares an `x-entityRef` picker with mirror candidates —
  the form collects the target; the submit path keeps the picker value when context yields
  nothing. Verified live: `CreateAppointment` renders as a live button on a Clinician entity.
- **No label floors to a short id** (`bb9fe41a` + 0.14.2): `scopedLabel` names the viewer's own
  key with the me-row display name ("Sam Okafor", "You" pre-decrypt); `edgeServices` projects
  `resolvedViaLabel` — bound in the TAIL, because a walk-prefix intermediate projects its key but
  not its aspects (the engine finding this fire named; the `scopedUnit` precedent is the pattern).
  `panes_test.go` pins the pairing rule structurally (entityType→typeLabel,
  resolvedVia→resolvedViaLabel).
- **The zombie task**: an open task pointed `forOperation` at an op-meta a package move had
  tombstoned — undispatchable by construction, healed via operator `CancelTask`, and the lifecycle
  gap filed (`[Pkgmgr] An op-meta tombstone orphans the open tasks that reference it`).
- **The blanked service modal**: two racing lenses on one row key erased `viaServices` for
  multi-hat actors (`[Refractor] Two lenses sharing one IntoKey race per column`, filed). The
  0.14.4 mitigation makes both catalog tails project the join, but the roles tail's inbound
  `(op)<-[:permitsOperation]-(psvc)` collect — proven correct in the ruleengine's own store by
  `TestEdgeCatalogRoles_CarriesTheServiceJoin` — does not bind in the live projection (spec
  current in KV, Refractor cycled, cold hydrate): filed as
  `[Refractor] A tail expansion from a walk anchor binds in-memory but not in the live projection`.
  Until it closes, the modal lists no ops for multi-hat actors (the pre-existing state — not a
  regression) while the same ops offer normally from Nearby entities and tasks. The mitigation is
  in place so the modal heals automatically when the loader gap closes.

## 7. UX humanization pass (Winston, 2026-07-27 — Andrew's "make it make sense to a human")

A field-level audit of all 37 full descriptors (14 op-meta-declaring packages) found: **zero**
JSON-Schema titles corpus-wide (camelCase identifiers as labels — the "leaseAppKey" box), 13
key-shaped raw-text fields a human cannot fill, 10 hand-typed RFC3339 boxes, two
rendered-but-always-rejected JSON-array fields, and a money help text instructing a 100× error.
Shipped in one pass:

- **`{me.<type>?}` optional template modifier** (client + OpDispatchSpec doc): fill when
  resolvable, omit the param when not — never rendered, never offer-blocking. The vocabulary gap
  that forced CreateBooking's lease field to be a raw box (required-form would have hidden the op
  from non-residents). Applied: CreateBooking.leaseAppKey, RecordServiceOutcome.serviceprovider
  (its lint-package-standard readTemplateDebt entry retired with it).
- **Titles + humanized help on every rendered field** across ten packages; `format:"date-time"/
  "date"` on the RFC3339 fields with a datetime-local→RFC3339 submit conversion; `enumLabels`
  where tokens aren't words; the amountCents dollars help fixed; RecordIdentityPII's dob de-masked
  via `format:"date"`; a `prettifyFieldName` renderer floor so an untitled property can never
  render as a bare identifier again.
- **New auto-fill data**: a `patient` selfAnchor (identifiedBy walk, key-only, D3-safe) feeds
  `{me.patient}` on CreateAppointment — non-patients now get the honest degraded card instead of a
  form that can only be rejected; `studioKey` columns on both session lenses feed
  `{entity.studioKey}` on TombstoneSession. edge-manifest 0.14.5; twelve packages bumped total.
- **Left filed/deferred, named**: the SetProviderHours/SetProviderTimeOff array widget (rendered
  but unsubmittable — needs renderer vocabulary), SignLease/SignRenewal task-path descriptors, the
  Protected-pane-backed identity picker (D8 void), lease-signing's unit/leaseApp entity columns,
  RescheduleAppointment.patient (staff path via panes can't feed `{me.patient}`; stays titled
  free-text pending the pane-row context seam).
