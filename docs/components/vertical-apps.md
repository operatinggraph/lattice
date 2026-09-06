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
- **A server-side refusal added to one form leaves its sibling form a dead end** — the resident self-pay form
  hides at zero balance and carries `max`; the front-desk form beside it rendered whenever an account existed, so
  the new cap turned a mis-key into a raw `AuthDenied` toast where the sibling form never lets one be typed.
  Minted: café house-tab payment cap (2026-09-05). Check: when an op gains a server refusal, walk every form that
  dispatches it and give each the courtesy its sibling already has (hide / `max` / prefill).- **A count the FE promises for an op's effect must apply the op's own predicate, not a coarser key** — the
  roster's "Call off the remaining N classes" tallied upcoming occurrences per series while the op cancels
  only those still held at the confirmed studio, so one occurrence moved elsewhere made the button promise N
  and the op remove N−1, with the confirm dialog repeating the wrong number. Minted: wellness series call-off
  (2026-09-06), caught cold. Check: for every "N will happen" label, diff the FE's filter against the
  script's skip conditions conjunct by conjunct, and key the tally on every field the op confirms.
- **An op name in any `cmd/<app>` Go comment is a UI reference to `lint-app-op-descriptors`** — a
  rationale comment naming an exemption-less op (the instructor identity bind) reddened `lint-static` after
  a green local run, because the unit's local gate set omitted that lint. Minted: wellness staff-hats
  (2026-09-06). Check: run `lint-app-op-descriptors` on every `cmd/<app>` edit, comments included, and name
  an undescribed op by role, never by literal.
- **A server refusal added to a script leaves the OpMeta descriptor beside it asserting the old rule** — the DDL's
  InputSchema and the package's OpMeta are two declarations of one op; a descriptor-driven form then renders the
  field optional and the submit fails with no field-level guidance. Minted: wellness manual-charge memo (2026-09-06),
  caught cold. Check: for every new `fail(...)` on a param, grep the op's OpMeta InputSchema + FieldDescription and
  pin `required` in the opmetas test.
- **A new DOM write inside a generation-guarded async renderer bypasses the guard** — every existing append re-checks
  `generation !== rosterGeneration`; a note added later did not, so a class switched mid-fetch lands its note under
  the wrong roster. Minted: wellness studio-retired note (2026-09-06), caught cold. Check: any append after an
  `await` in a renderer that owns a generation token re-checks it first.
- **A transport throw after a destructive or secret-minting submit is narrated as "did not land"** — `api()` throws
  on any non-OK response, including a 5xx after the Processor committed, so a rotate/refund/tombstone toast that
  says "could not" may be wrong and the only copy of a minted secret is gone. Minted: LoftSpace RotateClaimKey
  (2026-09-06), caught cold. Check: the throw path of a ceremony or irreversible op says the write may have landed
  and what to do next, in the `withheld` vocabulary.
