# Vertical apps — `cmd/{loftspace,clinic,cafe,wellness}-app`

The four reference verticals' front-ends: a Go binary per vertical serving a vanilla-JS page from
`go:embed`, with handlers that read **lens read-model targets only** (architecture P5 — Core KV is Loupe's
alone; `lint-conventions`' P5 gate fails any other `cmd/<app>` that names the Core-KV bucket) and mutate
state only by submitting operations to the Gateway (P2). Each app runs a sign-in-first session posture
(`<APP>_DEV_AUTH=1` enables the demo minter behind `/api/dev-login`), resolves the caller's hats from
`/v1/actor`, and confines what a staffer sees to the workplaces they `worksAt`. Person-triggered ops are
rendered from their package descriptors (`internal/descriptorform`, fetched through `/api/op-catalog?types=`)
rather than hand-built forms wherever a descriptor exists; `scripts/lint-app-op-descriptors.go` refuses an
op literal no package describes and only ever shrinks its hand-built baseline.

Build + launch: `make cycle-<vertical>` rebuilds `bin/<vertical>-app` and relaunches it against the running
stack (the Makefile is the authority; `make run-<vertical>-app` is foreground, human-only). A changed lens or
DDL lands live with `make reinstall-package PKG=packages/<pkg>` — no restart.

Role playbook: [`agents/fe-engineer/SKILL.md`](../../agents/fe-engineer/SKILL.md). Package side:
[Capability Packages](./_packages.md).

## Review keeps catching (dossier)

The recurring review-finding classes for the vertical FEs — fire briefs copy the applicable entries into
part 5 (`agents/fire-brief-template.md`), the item-close review appends new ones (`agents/steward/SKILL.md`
§4). **Capped at 12 one-liners**; an entry RETIRES when a lint/test gate mechanizes it.

- **An HTML escaper built on a text node leaves both quote characters alone, so the first free-text value
  interpolated into a quoted attribute is an attribute breakout** — `div.textContent = s; div.innerHTML`
  escapes `& < >` only; every prior `data-*="' + esc(...)` site carried server-minted keys, so the defect
  was latent until a memo reached one. Minted: café refund (2026-09-05) — a `data-memo` built from the
  settled tab's memo, which is staff-typed off-menu text. Check: the escaper must be a map over `& < > " '`
  (café's `escapeHtml` is, pinned by a goja test over the embedded source); wellness's `esc()` is still the
  text-node shape and safe only because nothing free-text reaches its attributes — mechanize on the second
  sighting.
- **A descriptor-driven form's known-catalog list is hand-maintained, and an op missing from it fails as
  "unavailable" with every test green** — the list is what `?types=` point-reads, so a new
  `opCatalogCache.<Op>` read with no list entry never finds its row. Minted: café `RefundCafeCharge`
  (2026-09-05), caught in lead review. Check: a test that derives the list from every `opCatalogCache`
  read in the embedded app.js (`cmd/cafe-app/op_catalog_test.go`); clinic and wellness still keep theirs
  by hand — promote to a `scripts/lint-*.go` gate on the second sighting.
- **A staff-leg descriptor context that passes no `me` silently drops every `{actor}` enumeration the
  descriptor declares** — `substituteEnumerations` discards an entry whose hub substitutes to empty, so
  the envelope reaches the Processor with the walk undeclared while the test harness (which declares it)
  stays green. Minted: café `CreditCafeAccount` + `RefundCafeCharge` front-desk legs (2026-09-05). Check:
  every `renderOpForm` context whose descriptor carries a `{actor}` enumeration passes `me`, and the
  comment beside it names the enumeration, not only `buildAuthContext`.
