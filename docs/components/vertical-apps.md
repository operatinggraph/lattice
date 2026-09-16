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

Retired: *a server-side refusal added to one form leaves its sibling form a dead end* — `lint-refusal-courtesy`
(three sightings: café house-tab payment cap 2026-09-05; wellness guest picker vs member picker 2026-09-13; café
`ItemUnavailable` reaching Facet through edge-manifest's entity-ref picker 2026-09-15 — the third form was in
ANOTHER client, so a grep of the app's own dispatch sites missed it). The gate derives each op's state refusals
from the describing package's scripts and every dispatch site across the clients (an FE function naming the op +
Facet's descriptor form for any described op), and fails a site with no `// refusal-courtesy: Op/Code: verb — why`
(opmetas.go carries the `(facet)` line) the moment a script gains a code or a client gains a site; the 2026-09-15
census (49 ops · 180 pairs · 111 sites, [census](../reviews/verticals-refusal-courtesy-gate-2026-09-15.md)) found
four siblings without their sibling's courtesy and fixed them. The half that stays a walk: the gate proves a
declaration EXISTS and is not stale, never that the named `hide`/`drop`/`cap` fires in the state — a reviewer
opens the mechanism the clause names; and a `none — the state is not projected` is the pointer to the lens
column the next fire adds (the third sighting's fix), not a settled verdict.

- **A staff-leg descriptor context that passes no `me` silently drops every `{actor}` enumeration the
  descriptor declares** — `substituteEnumerations` discards an entry whose hub substitutes to empty, so
  the envelope reaches the Processor with the walk undeclared while the test harness (which declares it)
  stays green. Minted: café `CreditCafeAccount` + `RefundCafeCharge` front-desk legs (2026-09-05). Check:
  every `renderOpForm` context whose descriptor carries a `{actor}` enumeration passes `me`, and the
  comment beside it names the enumeration, not only `buildAuthContext`. Second sighting (LoftSpace `GiveNotice`,
  2026-09-15, caught cold): a hand-built submit declared the actor-keyed `applicationFor` OptionalRead on the tenant
  hat only while the script reads it on both — diff the FE's `reads`/`optionalReads` against the descriptor PER HAT.
- **A count the FE promises for an op's effect must apply the op's own predicate, not a coarser key** — the
  roster's "Call off the remaining N classes" tallied upcoming occurrences per series while the op cancels
  only those still held at the confirmed studio, so one occurrence moved elsewhere made the button promise N
  and the op remove N−1, with the confirm dialog repeating the wrong number. Minted: wellness series call-off
  (2026-09-06), caught cold. Check: for every "N will happen" label, diff the FE's filter against the
  script's skip conditions conjunct by conjunct, and key the tally on every field the op confirms. **Second
  sighting (clinic follow-ups, 2026-09-14), in the other direction:** the lens was re-derived FROM the FE's
  predicate and dropped the lower bound the FE carried (`g.startsAt > f.startsAt`), so a `followUpDate` set before
  the visit read as addressed in the graph and outstanding on the card — caught cold. When one rule lives in two
  languages, pin BOTH copies over one fixture set that includes every boundary either copy carries (the
  `followup_addressed_test.go` goja pin + the lens's cypher pins). **Third sighting (clinic visit picker,
  2026-09-14, caught cold), narrowing the other way:** a courtesy filter (`startsAt <= now`) tighter than the op's
  predicate excluded the primary flow — a checked-in visit carries no clock. Not gate-able generically; the mandated
  shape is the goja pin over a fixture holding every non-terminal status the op admits (`ledger_ui_test.go`).
  **Fourth sighting (LoftSpace deposit strip, 2026-09-15, caught cold):** "incl. security deposit $D" applied the
  ledger's custody predicate (charged − returned) to a payment figure — a partial payment read as a whole deposit owed;
  now "of which up to $min(balance, held)". **Fifth (café counter payment, 2026-09-16, caught cold):** the app's
  `Posted` boolean enumerated two of the lens's three gap columns after a third was added — a derived FE boolean over
  gap columns lists every gap the lens's `violating` OR-s; and a button that pays "the total" sent the rendered
  total, so a stale card became a silent partial — the op now refuses a mismatch and the card re-renders. **Sixth
  (café house tab limit, 2026-09-16, caught cold):** the desk's policy panel read only the workplace's OWN row and
  said "residents may self-order any total" while the op binds the tightest policy on the whole chain — a panel
  that names a rule states the op's composition, not one input to it; the read now returns the covered chains and
  the sentence names the tighter policy that binds.
- **An op name in any `cmd/<app>` Go comment is a UI reference to `lint-app-op-descriptors`** — a
  rationale comment naming an exemption-less op (the instructor identity bind) reddened `lint-static` after
  a green local run, because the unit's local gate set omitted that lint. Minted: wellness staff-hats
  (2026-09-06). Check: run `lint-app-op-descriptors` on every `cmd/<app>` edit, comments included, and name
  an undescribed op by role, never by literal.
- **A transport throw after a destructive submit is narrated as "did not land"** — `api()` throws on any
  non-OK response, including a 5xx after the Processor committed, so a cancel / end-series / ledger-entry toast that
  says "could not" may be wrong and the desk retries a write that landed (a double charge, a double waive). Minted:
  LoftSpace RotateClaimKey (2026-09-06). Every ceremony catch and every generic descriptor-form dispatcher (the
  `revealCeremonySecret` callers) is gated (`lint-ceremony-throw-path`); the hand-built irreversible submits the
  2026-09-13 census left (clinic `setStatus` / `endSeries` / `submitRemoveProviderSite`, LoftSpace
  `unlinkCredential` / `withdrawApplication` / `decideApplication`) carry the `sent` / `confirmed` staging since
  2026-09-14, and the gate still does not reach a hand-built submit. Check: the throw path of any NEW hand-built
  irreversible submit stages `sent` / `confirmed` (the `submitCorrectStatus` shape) and says the write may have
  landed and what to check, in the `withheld` vocabulary; a money-moving op's catch never invites a bare retry.
- **A released uniqueness guard invalidates every FE set that assumed it — "one live row per (a, b)" is a claim about
  the guard, not the data.** When an op stops tombstoning a row but still releases the guard that made the pair unique,
  the member can hold the kept row AND a fresh one; every `Map`/`Set` keyed on the pair (an own-status-by-session map,
  a "these bookers are seated" picker exclusion) then either shadows the live row or hides the person the desk is
  trying to serve. Minted: wellness `forfeited` booking (2026-09-13) — two sites, one caught cold. Check: when a script
  drops a tombstone, grep the FE for every collection keyed on the guard's pair and decide per site whether the kept
  status is skipped or ranked.
- **A value reaches markup unescaped because the escaper's callers were never censused** — a test of `esc()` proves
  nothing about who calls it; four café panels wrote `e.message` raw into the empty-state div, the front-desk card a
  cross-package class name, and LoftSpace's Browse card a landlord-typed `rentCurrency` through `money()` — a stored
  XSS every applicant rendered. Minted: café/LoftSpace (2026-09-13 census). Gated for string-building sinks
  (`lint-markup-escaping`); what remains is the DOM path: a `textContent`/`value` write is safe, an `el.setAttribute
  ("href"/"src"/"on*", v)` or `new Function` is not. Check: grep the diff for `setAttribute(` / `href =` / `src =` /
  `insertAdjacentHTML` and prove each value's origin.
- **A source-scanning test that samples its consumers by regex is an open set — the first read shape the regexes
  don't name passes as "not a read"** — café's catalog-coverage test matched `cache.X` and `cache["X"]`, so
  `cache?.X`, `const { X } = cache`, `cache[kind]` and `loadOpCatalog().then(c => c.X)` all passed with X missing
  from the literal. Minted: wellness/clinic catalog mirror (2026-09-13), caught cold. Check: a scan over embedded
  source classifies EVERY occurrence of the identifier (and the loaders/promise that alias it) as a known non-read
  or a read, and fails at the line on anything else; prove it with a mutation battery, not a green run.

- **A "the person can also do it from X" claim — in a package comment, a lens rationale, or a card's hint — is a
  claim about X's RENDER GATE in this row's state, and a "usually within N" promise is a claim about the mechanism's
  actual order.** The renewal chain's `submitProfile` leg cited the apply-flow profile form as the tenant's second
  route; that form hides once the landlord approves, and every renewing tenant is approved. The card's hint promised
  the profile task "within a minute" while equal-cost legs order by ref, so `setTerms` runs first. Minted: LoftSpace
  refused-signature (2026-09-13), both caught cold. Check: for every route a comment or hint names, open that
  surface's `if (…) card.append(…)` gate with the row's live state; for every timing promise, name the leg order or
  lease that bounds it, or cut the number.
- **A local calendar-day window built as midnight + 24 h is wrong twice a year** — the fall-back day is 25 hours, so
  a tab settled in its last hour falls into neither day's panel; the spring day is 23, so the window borrows the next
  day's first hour. Minted: café Today panel (2026-09-14), caught cold. Check: a day window ends at
  `new Date(y, m, d + 1)`, never `start + 86400000`, and the goja pin runs under a DST zone (`time.Local` swapped for
  the test) with a 23:30 fixture on the change day.
- **Two courtesy surfaces for one refusal name the same instant in different zones** — the picker rendered a
  midnight-UTC term end with `toLocaleDateString()` (the day before, west of Greenwich) while the script's refusal
  sliced the UTC stamp, so the desk read "ended 6/30" and was then told "ended on 2026-07-01". Minted: café
  `TenancyEnded` (2026-09-14), caught cold. Check: when an FE label and a script `fail(` text name the same recorded
  stamp, render both from the same slice of it, and say which zone it is. Second sighting (LoftSpace lease terms, 2026-09-14,
  caught cold): the recorded lease rendered by its UTC date beside a pre-approval ask rendered through
  `toLocaleDateString` — the same instant, a day apart. Censused 2026-09-14 across all four FEs (39 locale-rendering
  sites, each traced to its producer): every date-only fact — LoftSpace's lease / listing / period stamps, café's
  `leaseEnd` — renders by its UTC slice, and every local-rendered value is a real instant; clinic and wellness
  project no date-only column. Mechanized as the mandated pin shape, not a gate (no producer-side type marks a
  column date-only, so the set is not derivable): `cmd/loftspace-app/lease_term_ui_test.go` evaluates the shipped
  helpers under a pinned `time.Local = America/Los_Angeles` with a positive vector proving the zone reaches goja's
  Date; an app that gains a date-only column adds its helper to that shape. Third sighting (clinic `amendedAt`,
  2026-09-16, caught cold): a real instant rendered by its UTC slice on the card and locale in the modal, beside a
  locale `documentedAt` on the same line — the inverse error; an instant renders locale everywhere.
- **A shared refusal-message map serves every hat whose form routes through it — a sentence in the patient's voice
  reaches the desk.** `friendlyBookingRejection` is read by `submitBook` and `submitReschedule` under both hats, and the
  `SelfBookingLimit` text told a refused front-desk mover "You already have an open visit … the front desk can book
  more". Minted: clinic self-booking cap (2026-09-16), caught cold. Check: for every new branch in a shared message
  map, list the hats that reach it and write the sentence hat-neutral (the row's subject, never "you"), or thread the
  hat.
- **A new terminal state on a row is a census of every status switch and render gate in the app, not of the banner
  that named it** — `ended` reached the applicant banner and the terms panel, and missed `applicationStatus` (the
  by-unit console read an ended tenant as "Approved — leasing" and dealt it a ledger panel), the decide gate
  (Approve/Decline re-offered on an ended row as a silent no-op) and the search chip. Minted: LoftSpace
  (2026-09-14), three sites caught cold. Check: grep the app for every `status`/`DISPOSITION`/`case "approved"`
  switch and every `if (a.qualified` / `landlordApproved` gate, and decide the new state's arm at each. Second
  sighting (LoftSpace `lostToRival`, 2026-09-14, caught cold): the state reached the banner and the landlord row
  and missed the inbox — a task scoped to a lost application stayed "Complete" for the grant's 30 days, refused
  every time — and the stepper drew four done steps beneath the lost banner. The census now includes every
  surface keyed by `scopedTo`/`entityKey` to the row (the inbox, the documents list), not only the row's own
  card. Third sighting (LoftSpace recorded `lost`, 2026-09-14, caught cold): the boolean stayed true across the
  relist, and two surfaces that had hidden the decision on the unit's status alone re-offered it — the landlord
  search never selected `lost_to_rival` (undefined is falsy) and the by-unit console's `applicationStatus` had no
  `lost` arm (a lost row ranked "best match"). Mechanized for the search half: `search_columns_test.go` pins that
  every boolean column of the landlord read lens is selected by `searchLandlordColumns`. The switch half stays a
  mandated test row per new terminal value (`TestApplicationStatus`) plus the goja pin per new terminal boolean
  covering the banner, the disposition chip, `taskDisposition` and `decisionOffered` (the `rival_task_ui_test.go`
  shape).
