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

Two FE gates run per app under `go test`: each HTML-string escaper (café `escapeHtml`, wellness `esc`) is the
five-character map over `& < > " '` — a text-node helper (`div.textContent` → `innerHTML`) leaves both quotes alone
and is an attribute breakout — pinned by `web_escape_test.go` against the real embedded declaration (clinic and
LoftSpace build DOM nodes and carry no HTML-string escaper; an app that gains one copies the pin). And where an app
narrows its `?types=` fetch to a `KNOWN_CATALOG_OPS` literal (café, clinic, wellness; LoftSpace fetches the whole
catalog), `TestKnownCatalogOpsCoversEveryCacheRead` classifies every reference to the cache, the loaders and the
promise in the embedded `app.js` as a known non-read or a read whose op must be in the literal — an op read off
the catalog with no entry is never fetched and the form reports itself unavailable with every test green.

Build + launch: `make cycle-<vertical>` rebuilds `bin/<vertical>-app` and relaunches it against the running
stack (the Makefile is the authority; `make run-<vertical>-app` is foreground, human-only). A changed lens or
DDL lands live with `make reinstall-package PKG=packages/<pkg>` — no restart.

Role playbook: [`agents/fe-engineer/SKILL.md`](../../agents/fe-engineer/SKILL.md). Package side:
[Capability Packages](./_packages.md).

## Review keeps catching (dossier)

The recurring review-finding classes for the vertical FEs — fire briefs copy the applicable entries into
part 5 (`agents/fire-brief-template.md`), the item-close review appends new ones (`agents/steward/SKILL.md`
§4). **Capped at 12 one-liners**; an entry RETIRES when a lint/test gate mechanizes it.

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
  dispatches it and give each the courtesy its sibling already has (hide / `max` / prefill).
- **A count the FE promises for an op's effect must apply the op's own predicate, not a coarser key** — the
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
- **A released uniqueness guard invalidates every FE set that assumed it — "one live row per (a, b)" is a claim about
  the guard, not the data.** When an op stops tombstoning a row but still releases the guard that made the pair unique,
  the member can hold the kept row AND a fresh one; every `Map`/`Set` keyed on the pair (an own-status-by-session map,
  a "these bookers are seated" picker exclusion) then either shadows the live row or hides the person the desk is
  trying to serve. Minted: wellness `forfeited` booking (2026-09-13) — two sites, one caught cold. Check: when a script
  drops a tombstone, grep the FE for every collection keyed on the guard's pair and decide per site whether the kept
  status is skipped or ranked.
- **A source-scanning test that samples its consumers by regex is an open set — the first read shape the regexes
  don't name passes as "not a read"** — café's catalog-coverage test matched `cache.X` and `cache["X"]`, so
  `cache?.X`, `const { X } = cache`, `cache[kind]` and `loadOpCatalog().then(c => c.X)` all passed with X missing
  from the literal. Minted: wellness/clinic catalog mirror (2026-09-13), caught cold. Check: a scan over embedded
  source classifies EVERY occurrence of the identifier (and the loaders/promise that alias it) as a known non-read
  or a read, and fails at the line on anything else; prove it with a mutation battery, not a green run.
