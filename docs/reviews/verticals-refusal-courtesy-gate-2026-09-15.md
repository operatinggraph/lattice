# Vertical FEs dossier — the sibling-form class goes to a gate (2026-09-15)

The third promotion pass over the `docs/components/vertical-apps.md` "Review keeps catching" dossier. The
**sibling-form class** — *a server-side refusal added to one form leaves its sibling form a dead end* — has been
seen three times (café house-tab payment cap 2026-09-05; wellness guest picker vs member picker 2026-09-13; café
`ItemUnavailable` 2026-09-15, where the third form was in ANOTHER client: Facet's descriptor-driven self-order,
fed by edge-manifest's `edgeEntityMenuItems`), each caught cold, and `agents/steward/SKILL.md` §4 owes a gate at
two. Its prescribed check — "when an op gains a server refusal, walk every form that dispatches it and give each
the courtesy its sibling already has; the census spans every client that dispatches the op" — is a census a lint
can force at exactly the moment it is skipped: when a script gains a `fail(` code, or a client gains a dispatch
site.

## Fire brief

**Scope sentence.** Promote the sibling-form class to a CI gate, `scripts/lint-refusal-courtesy.go`: for every
op that a package script can refuse with a *state* code and that is dispatched from two or more sites across the
clients (the four vertical FEs' `app.js` + Facet's descriptor form), every dispatch site carries a declaration
of the courtesy it gives that code — or an honest `none`/`unreachable` with its reason — and the declaration
stays true (a code the script can no longer raise, or an op the site no longer names, fails). Author the
declarations across the four FEs and the described ops' `opmetas.go` entries, **fix every sibling the census
finds without the courtesy its sibling already has**, wire the gate into `lint-static`, the Makefile and
`docs/components/lint-gates.md`, and retire the dossier entry.

**Why this shape (grounded live at `5912d907`).**

- *An op's refusal codes are derivable.* Every package script dispatches on `if ot == "X":` / `op.operationType
  == "X"` literal chains (all 12 `ddls.go` corpora; no dict dispatch, no `in [...]` beyond what
  `lint-opmeta-required-fields.go:250-316`'s `walkDispatch`/`opNames`/`isOpTypeExpr` already recognise — copy
  those three with provenance, a `//go:build ignore` main cannot be imported). Every `fail(` in the corpus takes a
  string literal first argument, or a `+` concatenation whose leftmost operand is one, of the shape `"<Code>: …"`
  (489 calls, 0 non-literal). The op's code set = every `fail(` code reachable from its dispatch block through the
  transitive closure of top-level `def` calls (refusals live in helpers: café `require_no_credit_hold` →
  `CreditHold`, `require_open_status` → `TabNotOpen`, `require_menu_item_price` → `ItemUnavailable`).
- *The exempt family is the codes whose courtesy is not per-form.* `InvalidArgument` (payload shape — the field's
  own validation, bound by `lint-opmeta-required-fields`), `WrongClass` (a site binds one class), `AuthDenied` /
  `PermissionDenied` (the hat the whole surface renders under), `NotFound` / `Unknown[A-Z]*` / `NotA[A-Z]*` (a
  dead or mistyped key — the read-model rows a site offers filter `isDeleted`). Everything else is a **state**
  code and is governed. The boundary is stated in the gate's header; widening it is a gate edit, not a
  declaration.
- *A dispatch site is derivable.* In a vertical `app.js` (read through goja's parser, the `lint-ceremony-throw-
  path.go:56-100` shape, with its `parseable` rewrite), a site is a **top-level function (or top-level
  statement)** that names a registered op: a string literal equal to the op name on a line matching
  `lint-app-op-descriptors.go:309`'s `submissionContext` (`operationType:` / `submitOp(` / `opOrThrow(`) or passed
  as a call argument, or a member access whose property is the op name (`opCatalogCache.CreditCafeAccount`,
  `catalog["X"]`); the `KNOWN_CATALOG_OPS` array literal and comments are not sites. **Facet is a site for every
  op whose `OpMetaSpec.Dispatch` is non-nil**: `edgeOpCatalog` projects every op meta (`packages/edge-manifest/
  lenses.go:727`, `WHERE op.data.operationType <> null`), the renderer resolves the row at dispatch (`cmd/facet/
  web/app.js:1762`), and the only per-state courtesy Facet's generic form can give is a column the entity lens
  projects and `entityRefCandidates` drops on (`cmd/facet/web/app.js:3021`, `available !== false`).
- *The declaration binds where the courtesy lives.* JS: `// refusal-courtesy: <Op>/<Code>: <verb> — <clause>`
  inside the site function's span (or on the comment block directly above its header). Go: `// refusal-
  courtesy(facet): <Code>: <verb> — <clause>` inside the op's `OpMetaSpec` composite literal in the owning
  package's `opmetas.go` (go/ast: a comment whose position lies between the literal's braces). Verbs are a closed
  vocabulary: `hide` (not rendered in the state) · `disable` · `drop` (the picker omits the row) · `cap` (an input
  bound) · `prefill` · `confirm` (asks first; the refusal is then the answer) · `none — <why no courtesy>` ·
  `unreachable — <why this site never reaches the branch>` · `see <fn>` (JS only: this function delegates to
  `<fn>`'s declaration; `<fn>` must be a site declaring the code). The clause is mandatory for every verb — it
  names the gate/column/filter, so a reviewer can open it.
- *The gate is default-deny and stays true.* For each governed (op, code): a site with no declaration FAILS at
  the function header; a declaration naming a code the op cannot raise FAILS (stale); a declaration in a function
  that names no such op FAILS; a `see <fn>` to a non-site or non-declaring function FAILS; an unknown verb or an
  empty clause FAILS. An op whose site count (JS sites + Facet) is 1 has no sibling and is ungoverned — the
  second site added later is what arms it. `STRICT=1` exits non-zero; unset warns; `--list` prints the governed
  census (op · code · script line · sites); `--selftest` runs the mutation battery (the
  `lint-derive-reads-bare-vector.go:787` shape).

**Touch-list (verified live).**

- `scripts/lint-refusal-courtesy.go` — NEW.
- `cmd/{cafe,clinic,loftspace,wellness}-app/web/app.js` — declarations at every governed site; the fixes the
  census finds.
- `packages/{cafe-domain,cafe-ledger,clinic-domain,clinic-ledger,loftspace-domain,loftspace-ledger,
  wellness-domain,wellness-ledger,identity-domain,lease-signing,service-location}/opmetas.go` — `(facet)`
  declarations on described governed ops (only those packages whose ops the FEs dispatch; the census decides).
  A `packages/` **comment-only** edit is not a semantic change — no version bump (`lint-package-version` reads
  the manifest/DDL bodies; confirm with `DIFF_BASE=5912d907 go run ./scripts/lint-package-version.go`).
- `.github/workflows/ci.yml` (`lint-static`, after `lint-seed-declared-reads`, ~line 585) · `Makefile`
  (`.PHONY` line 176 + a target beside `lint-seed-declared-reads:` 2535) · `docs/components/lint-gates.md` (one
  table row) · `docs/components/vertical-apps.md` (retire the entry into the walk-half statement).
- `scripts/lint-app-op-descriptors.go` `appOpCeilings` (215-291) — only if a fix adds a hand-wired op literal.

**Precedents to mirror.** `lint-opmeta-required-fields.go` (dispatch walk, self-test) ·
`lint-live-read-pinned-mutation.go` (`# occ:` declared escape, Starlark corpus over `pkgregistry.All()`) ·
`lint-ceremony-throw-path.go` (goja parse of `app.js`, function spans) · `lint-seed-declared-reads.go` (go/ast
over descriptors) · `lint-markup-escaping.go:140` (JS `// <family>:` declaration regex).

**Increment order.** (1) the gate + self-test, run `--list` on `main` for the census; (2) declarations per FE +
`(facet)` per described op, each site's courtesy verified by opening it, siblings without their sibling's
courtesy FIXED (goja-pinned where the precedent pins); (3) wiring + registry + dossier retire; (4) cold review
over the whole diff; gates.

**Gotchas.** A `fail(` inside a nested `def` belongs to the enclosing top-level def's closure. A helper shared by
several ops over-attributes a code to an op that never reaches the branch — `unreachable — <why>` is the honest
declaration, not a silent exemption. `KNOWN_CATALOG_OPS` names ops the file never dispatches by literal
elsewhere — those are dispatched through `opCatalogCache.X` member reads, which ARE sites. goja does not parse
the dynamic `import()` — reuse `parseable`. Comments are not in goja's AST — scan raw lines for the declaration
and map line → enclosing function span from the AST.

**Standing checklist, part 5.** (2) every census is a premise — the `--list` output is the population, re-run
after every fix; (3) a negative test needs its positive vector — every FAIL shape in the battery has a passing
twin; (6) precedent may carry debt — `submissionContext` counts comments, this gate must not.

**Non-goals.** No new courtesy mechanism in Facet beyond the `available` column pattern; no OpMetaSpec schema
field; no descriptor-driven migration of hand-built forms; no change to `lint-app-op-descriptors`' R1/R2.

## Census (`--list` at build, base `32df5620`)

**Population (before the review's recode).** 83 scripts examined; **49 governed ops · 180 (op, code) pairs · 62 JS sites · 49 Facet sites**;
every described op (117) was found in a dispatch block; 0 unmodelled `fail(` sites once parameter-carried codes
were resolved (the brief's "0 non-literal" count was wrong — `claim_cell(…, conflict_code, …)` and
`require_unclaimed_identity(…, code)` carry the code as a parameter, hiding `SlotConflict`, `PatientDoubleBook`,
`StudioConflict`, `InstructorConflict`, `BookerConflict`, `IdentityNotUnclaimed` — exactly the sibling shape — until
the closure bound the literal at the call site). Per client: café 13 sites · clinic 18 · loftspace 18 · wellness 13.
Declaration lines authored: 271 (`none` 126 · `hide` 64 · `unreachable` 45 · `cap` 14 · `drop` 10 · `disable` 7 ·
`see` 5 · `prefill` 0 · `confirm` 0; a line may group several codes). Two refinements the census forced on the gate: an op's codes are scoped to
the DESCRIBING package's scripts (`DebitAccount` is dispatched by both ledgers; loftspace-app must not inherit
cafe-ledger's codes), and Facet's verbs gained `cap` (the generic form renders `min=`/`max=` from the schema).

**Siblings found without their sibling's courtesy — fixed in the fire.**

| Op / code | Site without the courtesy | The sibling that had it | Fix |
|---|---|---|---|
| `OpenTab` / `LeaseNotApproved` | café `renderResident` (self-service Open Tab) | POS `fillLeaseSelect` disables the lease | `residentOpenTabAllowed` hides the button behind an "awaiting landlord approval" panel; goja-pinned |
| `OpenTab` / `TenancyEnded` | café `renderResident` — the state was not resident-readable (`/api/frontdesk-lease-details` is staff-only) | POS picker disables on `leaseEnd` | `cafeLeaseWorkplaces` projects `leaseEnd` (cafe-domain 0.15.0), joined server-side onto `/api/residents`; the same gate hides on the UTC calendar day; goja-pinned incl. the boundary |
| `SetBookingAttendance` / `InvalidState` (waitlisted) | wellness `rosterCard` offered Attended / No-show on a waitlisted row | the `forfeited` exclusion beside it | gate is `markable && !forfeited && !waitlisted`; goja-pinned |
| `SignRenewal` / `TenancyEnded` | loftspace `renderRenewalCard` — `renewalsReadSpec` did not carry the tenancy end | the applications surface's `decisionOffered` on `tenancyEndedAt` | `renewalsReadSpec` projects `tenancyEndedAt` (lease-signing 0.39.0); `renewalReady` requires it unset and the card says "Lease ended …"; goja-pinned |
| `VerifyGuarantor` · `SignRenewal` / `ApplicantMismatch`, `LeaseAppMismatch` (Facet) | Facet offered both ops and would have submitted the literal `{context.leaseApp}` — the staff app's row vocabulary, which `substituteTemplate` has no case for | the staff catalog form fills them from its row | `opButton` refuses to offer an op whose contextParam head it cannot resolve (`unrecognisedContextTemplate`); node-pinned |

**The cold review's two blockers, both the gate's own blind spots — fixed in the fire.**

| Finding | What it hid | Fix |
|---|---|---|
| The gate exempted its own founding sighting: every ledger's payment cap was coded `AuthDenied` ("no outstanding balance to pay", "a payment of $X exceeds…"), which the exempt family reads as the hat | the 2026-09-05 café cap, in all four verticals | recoded to state codes `NoBalanceToPay` / `PaymentExceedsBalance` / `WriteOffExceedsBalance` (cafe-ledger 0.6.3, clinic-ledger 0.5.3, loftspace-ledger 0.7.2, wellness-ledger 0.2.24); the census closes at 52 ops · 203 pairs · 84 JS sites · 52 Facet sites · 326 declaration lines; declared at every site; wellness's own-payment field gained `max` (its café/loftspace siblings had it); clinic's patient own-payment leg gained `selfPayCapMessage` before dispatch (goja-pinned) |
| A generic dispatcher is invisible to the site scanner: loftspace's inbox completes a task by `descriptorFor(task.operationName)` — a third site for `SignRenewal` the census counted as two | after `EndTenancy` the renewal card hid Sign (this fire's fix) while the inbox still offered Complete → raw `TenancyEnded`; `RenewalNotOpen` after `CancelRenewal` the same | gate rule: a function resolving a catalog row from a non-literal (`descriptorFor(x)`, `…atalog[x]`) must carry `// refusal-courtesy-dispatches: Op, …` and is then a site for each (4 live: `openComplete`, `submitComplete`, `openRenewalAction`, wellness `submitBillingEntry`); `taskDisposition` closes a renewal-class task whose row is ended / no longer open (`renewalTaskStale`, goja-pinned) and `openCatalogComplete` re-checks before mounting |

Also from the review: unmodelled `fail(` sites are now findings (a format-string leftmost literal is unmodelled, not
exempt); Facet's head match is exact (`{actorKey}` is not `{actor}`) and an optional marker does not soften a foreign
head; five clauses corrected to the verb their mechanism is (`disable` not `hide` for `cancelDisabled`; `cap` where
the schema bounds the control; `hide` via `unrecognisedContextTemplate` for `{context.*}` ops); the refund form's
amount control gained `max` so its `cap` clause is true.

Also: `CreateLeaseApplication`'s `leaseTermMonths` / `requestedRent` gained `"minimum":1` (the hand-built form had
`min="1"`; Facet's generic form now renders the same bound) — the `cap` Facet's declaration names.

**Declared `none` — no client can read the state (honest, not a gap between siblings).** Cross-actor races
(`DuplicateApplication`, `BookerConflict`, `AccountAlreadyExists`, `OpenTabAlreadyExists`), clock-relative codes a
lens cannot project without `$now` (`SessionInPast`, `SessionStarted`, `ScheduleInPast`), and codes on state no
read model projects (`RefundExceedsPaid`'s `cashCents`; a renewal's signature / guarantor state on Facet's row).

**Facet gap that is a design question, not a lens column.** Facet's entity-ref picker has ONE op-agnostic
courtesy column (`available`). `CreateBooking` refuses `SessionFull` while `JoinWaitlist` REQUIRES a full session,
so a per-op candidate filter is needed to give either op its courtesy without breaking the other — a descriptor
vocabulary extension (FORK-1 territory), filed as a `📐` row on the board with the absent pattern named.

**Review classification (close pass).** design-gap ×2 in the gate itself, both caught cold (an exempt family is a
claim about the corpus's coding discipline — the corpus falsified it; a site census over literals is a claim that
every dispatcher names its op — the generic dispatcher didn't) → the dossier's new entry; brief-gap ×3 (the "0
non-literal" premise; the site-of-`/api/residents` premise — it reads lease-signing's `leaseApplicationComplete`;
a lens-column edit's proof list omitted `internal/refractor`'s corpus census, which the retired `_packages.md`
class names); design-gap ×1 (Facet per-op candidate filter, filed); convention ×3 (a builder's DIFF_BASE proof of
"no version bump needed" compared committed ranges and missed its own uncommitted edit — the local-mode run is the
proof; four history-narrating comments; line-number pointers in clauses stale within the same diff — clauses name
functions). Residual stated: wellness's `renderMyBalance` cap is DOM-bound and unpinned, as café's own is.
