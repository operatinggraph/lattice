# The Vertical Package Standard — what a reference package looks like, knowing what we know

**Status: ✅ ANDREW-RATIFIED 2026-07-24** — drafted 2026-07-23 (Winston; directed in-session: "if it
makes sense to redesign all vertical packages knowing what we know today, let's do that").
**Ratified as written**, with three ratification-session corrections folded (§1 census numbers marked as a
pinned snapshot — verify scripts are 11 at HEAD, `backOfHouse` is four packages; §3.1's clinic fire struck
as SHIPPED `6b1c667c`; S3/S8 scoped so the open-KV name ban targets *subject* persons and admits the
deliberately-published provider directory). The §3 converge-vs-rewrite nomination is ratified as
non-binding guidance — each fire's Phase-0 brief still decides. **A `lint-package-standard` gate over the
mechanically-checkable subset (S1/S6/S7) is blocking in CI** (`scripts/lint-package-standard.go`) —
normative text alone does not bind future authors; the gate is what does. It runs over
`internal/pkgregistry`, so an unregistered package cannot escape it, and it admits two explicit escape
hatches: an `[no-op-meta: <code> — <reason>]` permission Note, whose code comes from a closed
vocabulary naming the missing mechanism, for an op no human triggers or none can yet describe (S1's own stated
exemption), and a shrink-only debt baseline enumerating the gaps §3 is scheduled to close — an entry
that stops violating fails the gate too, so the baseline cannot become a permanent amnesty.
**Convergence CLOSED 2026-07-26** (`dc396cb3`, Inc 6): all four §3 routes discharged and **both** the S1 and
S6 baselines are empty, so S1/S6/S7 bind over all 29 registered packages with zero exemptions. What no gate
measures — S2–S5/S8 — is carried by the census row in the verticals lane, and the remaining named tails are
their own rows there. The `readTemplateDebt` baseline (2 entries) is a separate row, not Standard debt.
**Extends:** [persona-worlds-design.md](persona-worlds-design.md) (archetype ladder §3; W1–W4 build to this
standard). **Contracts:** builds to #1/#2/#6; **Frozen-contract change: NONE.**
**Grounds in:** a 15-package census @ `fda8019c` (per-package scorecards in the census transcript; the
§1 numbers are its synthesis **as of that pin** — the corpus moves, so read them as the motivating
snapshot, not as current counts; two have already drifted, noted inline).

## 1. Why, and why now

The vertical packages are the reference corpus — every future package author copies them, so their debt
compounds by imitation. The census: **89 ops, 24 op-metas (27% resolvable, 12% fully renderable)** — 65 ops
are invisible to discovery, three of them consumer-invocable; verify scripts existed for 5/15 packages and
structure pins for 6/15, inversely correlated with size (lease-signing, the largest at ~5,000 LOC, has
neither) — *verify scripts are 11 at HEAD, so that gap is closing on its own*; the Starlark guard stdlib is
copy-pasted per package and drifting (~40 verbatim lines ×5 for the workplace binder alone); the
read-boundary tier is chosen ad hoc — clinic projected patient `fullName` into open NATS-KV buckets while
wellness deliberately projects bare keys (*since fixed — see §3.1*); `backOfHouse` is granted by four
packages (two verticals + two base); provider/landlord held zero write grants anywhere (*persona-worlds
W0/W1 has since closed this — provider now holds real grants*). One genuinely good number: **read-posture
debt is zero** — the class-(b) sweep completed and the lint gate is blocking. The Standard writes down the
target; the convergence program (§3) brings each package to it.

## 2. The Standard (normative)

A conforming vertical package satisfies every rule; each names its idiom source — mirror it, don't
greenfield.

- **S1 — Every user-facing op is self-describing.** A full `OpMetaSpec` (Presentation + InputSchema +
  FieldDescriptions + Dispatch with `TargetType`) for every op a human may trigger; the audience-slice
  schema may be narrower than the DDL's merged schema (clinic `CreateAppointment` idiom). Engine/reply/
  lifecycle ops are exempt but the exemption is stated in the permission Note. **Amended by Inc 5 (§8);
  the amendment is ratified as part of
  [client-ceremony-op-descriptors-design.md](client-ceremony-op-descriptors-design.md) (Andrew,
  2026-08-02), which narrows the class from identity-domain's five ops to two** (the link-ceremony pair —
  the other three become descriptor-driven via `derive_reads` + mint-and-reveal): a second exempt class
  exists — a **client-side ceremony** op, which a person *does*
  trigger but no rendered form can submit, because the client must mint a secret and keep the plaintext,
  submit as a different actor than it authenticated as, or name a key nothing projects. The exemption is
  admissible only with the *specific* mechanical reason in the Note (identity-domain's five are the worked
  examples), never a bare "not renderable" — a descriptor is a promise a client can build a valid envelope
  from these fields, and shipping one that cannot be honoured is worse than the debt. Bare `{OperationType}` metas
  exist only for orchestration `forOperation` resolution; a package never declares a meta for an op another
  package owns (the lease-signing `RecordIdentityPII` shadow hazard — collision checking is per-package
  only). *Idiom: `packages/clinic-domain/opmetas.go`; structural-auth variant: service-domain
  `RequestService`.*
- **S2 — Archetype-complete grants, documented.** The package's `permissions.go` opens with the grant-matrix
  doc block (identity-domain format) and covers every archetype the vertical serves (consumer / front /
  back / provider / operator), each grant's Note naming its in-script binder. Assigned work prefers
  **task-scoped authority over standing grants**, pinned by a test (maintenance `ResolveWorkOrder` idiom).
- **S3 — The read boundary is tiered by data, not habit.** Any person-identifying column ⇒ Protected
  Postgres (+ SecureColumns where contact-grade) with GrantTable producers (Path A); rows consumed on the
  Personal-lens plane ⇒ the cap-read slice ships in lockstep with its lens (Path B; the Fire-1 rule). Open
  NATS-KV buckets carry **bare keys only for the people the rows are *about*** — a patient, resident,
  tenant or applicant is projected by opaque key, never by name (wellness's keys-only comment becomes this
  rule). The **one** admissible exception is a **deliberately-published directory** of people acting in a
  public service capacity (clinic's `providerName`/`providerSpecialty`, which the booking UI renders):
  permitted only when the lens comment states the publication decision explicitly, so the next reader can
  tell a decision from an oversight. *Idioms: clinic (Path A stack, plus `lenses.go:406-411` for both
  halves of this rule), service-location (Path B + `staffReadGrants`).*
- **S4 — The five guard idioms are canonical text.** Workplace binder (`require_workplace` +
  `workplace_exempt`), identifiedBy self binder, `actor_holds_operator` exemption, CreateOnly claim
  aspects/links (cross-package-safe local names — the ledger collision test), slot-cell grid claims. Until
  a shared prelude mechanism exists (deferred, §4), the maintenance-domain text is the canonical copy —
  byte-identical, annotated; a package inventing a sixth guard shape flags it for the Standard first.
- **S5 — Read posture stays clean.** Every `kv.Read`/`kv.Links` carries its `# read-posture:` class; the
  blocking lint is the floor, the declared-reads doctrine (Contract #2 §2.5) the ceiling. (Corpus-wide
  debt: zero — hold the line.)
- **S6 — The verification floor scales with the package.** Every package: manifest↔definition drift test
  (universal today) **plus** structure pins (DDL/op/permission-tuple/lens counts + load-bearing script
  strings — loftspace `ScriptGuards` idiom) **plus** a `lens_cypher_test.go` executing every lens over a
  seeded topology (corpus-wide as of Inc 6 — both S6 baselines are empty, so the rule binds with no
  exemptions and a new lens ships with a test that executes it). Packages with
  stack-observable invariants add a `verify-package-<x>` script + make target (*idiom:
  service-location's suite*). Platform gap, named: `VerifyAgainstDefinition` does not cover Roles — filed
  with the convergence program.
- **S7 — Manifest hygiene.** Version bumps on any content change (lint-enforced); `grantsTo` lists mirrored
  field-by-field; roles listed in the manifest once pkgmgr covers them (S6 gap).
- **S8 — D3 everywhere, not just SYNC.** No person names on Personal-lens rows (established) **and** no
  subject-person names in open-KV read models (new, from S3, with S3's published-directory exception) —
  display names for the people a row is *about* come from the Protected/Vault planes.
- **S10 — A policy-free guard helper has ONE implementation, corpus-wide — and its BOUNDS have one
  value.** *The enforcement half of S4, which already asks for "byte-identical" guard text but, being
  normative prose, did not bind anyone.* Starlark guard helpers are
  pasted per *script* (there is no prelude yet), so the corpus holds many copies of each and a fix applied
  to one silently leaves the rest defective. Where a helper carries **no package-specific policy** —
  `worksAt_covers`, `workplace_exempt`, `actor_holds_operator`: plain arguments in, a question about the
  graph out — every copy must have identical *code*, **indentation included** (comments are free to differ;
  each package explains itself in terms of its own resolver). Indentation is hashed because it is semantics
  in Starlark, and a statement at the wrong nesting depth is exactly how such a walk drifts. The
  module-level **bounds** those bodies read (`ROLE_PAGE_LIMIT`, `WORKPLACE_PARENT_PAGE_LIMIT`,
  `WORKPLACE_MAX_DEPTH`, `WORKPLACE_MAX_NODES`) must each hold ONE value corpus-wide, because a body
  references its bounds by name and a digest cannot see 50 quietly becoming 10. Lint-enforced over
  `packages/**` non-test sources; the message names every copy, so the next author is told how many sites
  the fix has to reach, and finding too FEW copies is itself an error rather than a silent pass.
  **Helpers that *do* encode policy are out of scope** and must not be forced identical: `require_workplace`
  legitimately varies (clinic-reminders is operator-exempt-only because its ops have no consumer self path;
  maintenance-domain factors the non-exempt half into `enforce_workplace`). A genuinely divergent variant
  gets a **different name**, not an exemption. *Origin: `worksAt_covers` followed only the last
  `containedIn` parent per level while the read-side lenses unioned every branch — and the lane row naming
  the defect named three packages while the corpus held nine copies across seven (§11).*

## 3. The convergence program

Converge-vs-rewrite is decided **per package, in each fire's Phase-0 brief, against this Standard** — a
rewrite is only cheaper when the package is small and far from conformance (wellness, café are the likely
candidates; clinic and lease-signing converge incrementally). Routing:

1. ~~**Now (security-adjacent, ahead of the program):** clinic `fullName` off the open-KV lenses.~~
   **DONE — shipped `6b1c667c` (2026-07-24), hours after this doc was drafted.** `clinicPatients` /
   `clinicAppointments` are key-only; patient names live solely on the Protected, RLS-scoped lenses
   (`packages/clinic-domain/lenses.go:496-500`, `:406-408`). **Do not "re-do" this fire:** the
   `providerName`/`providerSpecialty` columns that remain on the open appointments lens are the
   **deliberately-public provider directory the booking UI renders** (`lenses.go:409-411`), not
   residual debt — stripping them would break booking. S3/S8's "no person names in open KV" therefore
   reads with its intended scope: *no names of the people the data is **about*** (patients, residents,
   applicants). A published service directory is a different data class and stays.
2. **W1–W4 (persona-worlds)** carry their vertical's package to conformance as part of the rework — their
   briefs cite this Standard rule-by-rule (S1 discoverability lands with the sign-in-first FE anyway: an
   invisible op can't be offered honestly).
   **This route CLOSED without discharging its S6 obligation.** Persona-worlds closed 2026-07-25 with
   café's and wellness's structure pins never landed — no commit touches either `package_test.go` but the
   module-path rename — and no residual row was filed for them. Their S6 debt is rerouted to the §3.3 sweep.
3. **One sweep fire** for the non-FE packages (ledgers, front-desk, one-bill, location, service-location,
   maintenance, lease-signing): S1 metas, S6 pins/cypher tests, S7 hygiene — mechanical against the census
   scorecards.
   **Widened at build time** (2026-07-25, §6) to **every package the gate's debt baseline holds an entry
   for**: `semantic-contracts` (a LoftSpace vertical package the enumeration simply omitted — its own
   `package.go` names it as one), `cafe-domain` + `wellness-domain` (§3.2's undischarged route, above), and
   `augur` + `capability-author` + `privacy-base` (platform-tier, outside §1's vertical scope — but the gate
   runs corpus-wide over `pkgregistry`, so their entries had no route to ever close, which is exactly the
   permanent amnesty the ratification banner forbids). None was defensibly excluded on merit, and a structure
   pin is ~15 mechanical lines, so none is left behind.
4. **identity-domain** conforms last (it is the idiom source; its own gap is S1 — the four consumer
   credential ops have no metas).

## 4. Deferred, named

Shared Starlark prelude (platform mechanism; consumer: S4's ×5 drift — files to the lattice lane when the
sweep proves the text stable); pkgmgr `VerifyAgainstDefinition` Roles coverage (S6); attendance/roster
domain for wellness (persona-worlds W3).

S3's lockstep footgun is **no longer deferred** — the lattice lane has it covered end to end: the
structural `lint-lens-anchors` gate + coverage testkit shipped 2026-07-24 (`68ffc584`, `385c26a7`), and
the single-declaration codegen was **ratified 2026-07-24** as one L fire
([design](read-grant-single-source-walk-design.md)). A package author's S3 Path-B obligation is therefore
enforced by CI today and will be compiled away when that fire lands.

## 5. Reconciliation + non-goals

Nothing here re-litigates persona-worlds — the Standard is the *quality bar* the already-ratified reworks
build to, plus a sweep for the packages those fires don't touch. No frozen-contract text changes; no
big-bang rewrite mandate (per-fire choice); no new platform mechanisms (the two named gaps route to the
lattice lane on their own merits); Loupe untouched.

## 6. §3.3 sweep — Inc 1 build note (Winston, 2026-07-25)

**Scope sentence (§3.3, verbatim):** *"One sweep fire for the non-FE packages … S1 metas, S6 pins/cypher
tests, S7 hygiene — mechanical against the census scorecards."* **Inc 1 = the S1 descriptors** for the
lease-signing / loftspace-domain self-service surface. Grounded at `0289dab8`.

**Work-list is the gate's own baseline, not a re-derivation.** `scripts/lint-package-standard.go` carries
`s1Debt` / `s6Debt` shrink-only; those entries *are* the convergence program's remaining surface.

**Inc 1 closes 7 `s1Debt` entries + one whole board row.** The verticals-lane row *"Op-metas still declare
`authContext: standing` for the five landlord ops"* is **the same edit** as the lease-signing S1 debt —
writing a full `OpMetaSpec` *requires* naming `Dispatch.AuthContext`. Built as one increment (coupled work =
one fire), so the row closes here rather than re-touching the same file later.

| Op | Payload (per its DDL) | Grants | `AuthContext` |
|---|---|---|---|
| `CreateLeaseApplication` | `{applicant, unit, moveInDate?, leaseTermMonths?, requestedRent?}` | operator any · consumer self | `self` |
| `WithdrawLeaseApplication` | `{leaseAppKey, unit, applicant}` | operator any · consumer self | `self` |
| `SetApplicantProfile` | `{leaseAppKey, unit, annualIncome, employmentStatus, …guarantor/co-applicant}` | operator any · consumer self | `self` |
| `DecideLeaseApplication` | `{leaseAppKey, decision, reason?, unit?}` | operator+frontOfHouse any · consumer self | **`standing` → `self`** |
| `SetRenewalTerms` | `{renewalKey, rentAmount, termMonths}` | consumer self | `self` |
| `VerifyGuarantor` | `{renewalKey, leaseApp, applicant, method?}` | consumer self | `self` |
| `CancelRenewal` | `{renewalKey, reason?}` | consumer self | `self` |
| `SetListingStatus` (loftspace-domain) | `{unit, status}` | consumer self (only grant) | `self` |

**Precedent, verified not assumed:** `packages/clinic-domain/opmetas.go:44-135`. Clinic's three dual-grant
ops (staff scope=any **and** consumer scope=self) each carry **one** meta with `AuthContext: "self"`, written
in the self voice. That settles the dual-grant question without inventing anything: the descriptor names the
**self** path, because a staff FE hardcodes its own dispatch while a descriptor-driven client cannot infer
the self path. Full-vocabulary idiom (Reads / OptionalReads / `:id` link fragments) is
`lease-signing/permissions.go:209-252`; field semantics `internal/pkgmgr/definition.go:518-545`.

**Bare metas stay bare.** `SignLease`, `RecordIdentityPII`, the service/doc instance+outcome ops and
`SignRenewal` exist for orchestration `forOperation` resolution only (S1) — the doc block at
`lease-signing/permissions.go:176-196` states why. Not upgraded. *(`RecordIdentityPII` left this list in
Inc 5: identity-domain owns that op and now declares its descriptor, so lease-signing's bare shadow was
deleted rather than kept bare — §8.)*

**Increments.** Inc 1 — the eight descriptors above + delete the 7 closed `s1Debt` entries + manifest bumps.
Inc 2 — S6 `lens_cypher_test.go` for `cafe-ledger` + `loftspace-ledger`. Inc 3 — S6 structure pins for all
**eight** debt-listed packages (§3.3 as widened); `privacy-base` has no `package_test.go` at all and gets one.

**Gotchas.** Package edit ⇒ manifest version bump or the install no-ops on a running stack. `grantsTo`
mirrored field-by-field (S7 runs corpus-wide, so a miss reds CI, not just an install). A descriptor is **not**
an authorization change — the scripts' owner guards already bind these ops; do not loosen a guard to make a
descriptor "work". `SetApplicantProfile`'s raw financials are deliberately never projected — the descriptor
describes INPUT fields and must not imply they read back.

**Non-goals.** front-desk / one-bill / location-domain / service-location (later increments); identity-domain
(§3.4, last); Facet-side rendering; any guard or permission-semantics change.

**Inc 2 (shipped `849ebbd2`).** `cafe-ledger` + `loftspace-ledger` get a `lens_cypher_test.go` executing both
business lenses over a seeded topology. Each lens's comment made a claim nothing held: ledgerHistory's
postedTo/heldFor hops are REQUIRED (a dangling transaction must project *nothing*, else a reader summing
amountCents per account drops rows), leaseAccounts anchors on the LEASE so a never-charged lease still gets a
row with a null accountKey, and loftspace's authorizedBy hop is OPTIONAL. Both directions are covered so an
empty result cannot pass for the wrong reason. Idiom: `clinic-ledger/lens_cypher_test.go` — but note theirs is
an *anchored convergence* lens taking an `actorKey`, while these are non-anchored business lenses where the
engine enumerates its own roots.

**Inc 3 (shipped `5dbdd84e`).** Structure pins for all eight debt-listed packages; `s6Debt["structure-pins"]`
is now an **empty map**, so the rule binds with no exemptions. Two things worth carrying forward:

- **The gate greps for the literal `len(Package.`**, so a pin written in an external `_test` package (where it
  must read `augur.Package.…`) does **not** register. `augur` and `capability-author` therefore get their pins
  in a new *internal* test file. Any future package with an external test package hits this.
- **A pin earns its keep only where a count would miss the defect.** Permissions are pinned as (op, scope)
  PAIRS because a permission *is* its pair (Contract #8 §8.1) — losing a scope=self row removes self-service
  while every count still matches. lease-signing pins `Protected` per lens (losing it moves identity-bearing
  rows onto an open surface); wellness pins its five CreateOnly claim aspects by name (no lens reaches them, so
  dropping one silently re-admits double-booking rather than breaking a read).

**Sweep state after Inc 3:** S7 is clean corpus-wide; `structure-pins` is empty. Remaining debt is **16 S1**
entries and **5 lens-cypher** (`augur`, `console-operator`, `demo-operator`, `identity-domain`, `rbac-domain`) —
most of it §3.2/§3.4 territory rather than §3.3's, with identity-domain conforming last by design.

**Adjacent find, filed:** the census covered **15** packages but `git ls-tree fda8019c packages/` shows **29**
existed at that same pin, and §3's routing was written "mechanical against the census scorecards" — so 14
packages were never routed by anything. The six swept above are only the subset the *gate* happens to hold
debt for. A census-derived program cannot see what the census missed, so "the Standard is converged" will be
false in a way no gate reports. Re-running the scorecard census across all 29 is filed as its own lane row.

## 7. §3.3 sweep — Inc 4 build note (Winston, 2026-07-26)

**Scope-diff gate.** §3.3's scope sentence is *"S1 metas, S6 pins/cypher tests, S7 hygiene"*. Inc 4 takes the
**S1 metas** half and closes **every remaining `s1Debt` entry outside identity-domain** — 9 of the 16. This is
narrow-only: no adjacent mechanism is substituted, no guard or permission semantics move. identity-domain's
remaining **7** stay by design (§3.4 has it conform last, it being the idiom source); the **5** `lens-cypher`
entries are a later increment.

| Package | Op(s) | `AuthContext` | Target |
|---|---|---|---|
| `cafe-domain` | `Charge` (closes **both** the `any` + `self` rows) | `self` | `tabKey` → `tab` |
| `cafe-domain` | `VoidCharge` | `standing` | `tabKey` → `tab` |
| `clinic-domain` | `CreatePatient` | `standing` | — (mints a patient) |
| `clinic-reminders` | `StartVisitSeries` | `standing` | `patientKey` → `patient` |
| `clinic-reminders` | `PauseVisitSeries`, `ResumeVisitSeries` | `standing` | `seriesKey` → `visitseries` |
| `orchestration-base` | `ClaimTask` | `standing` | `taskKey` → `task` |
| `wellness-domain` | `CreateSession` | `standing` | `studio` → `studio` |

**`Charge` is one meta in the self voice, closing two debt rows.** Inc 1 settled the dual-grant question
against `packages/clinic-domain/opmetas.go:44-135`: an op granted both scope=any (staff) and scope=self
(consumer) carries **one** descriptor written in the **self** voice, because a staff FE hardcodes its own
dispatch while a descriptor-driven client cannot infer the self path. The self slice therefore names
`tabKey` + `menuItemKey` and **omits `amountCents`** — `ddls.go:672-676` branches on
`op.authContextTarget` and derives the amount from the menuItem's own `.price`, so a self-submitted
`amountCents` is never read. Describing it would be a lie about the input.

The existing doc comment at `cafe-domain/opmetas.go:5-10` asserts *"Charge has no op-meta: it stays
operator-only (permissions.go), so it is never Facet-reachable."* That is **false at HEAD** —
`permissions.go:58-62` grants `Charge` scope=self to `consumer`. The comment is rewritten to describe the
package as it now is.

**`AuthContext: "standing"` is the honest value for the other seven.** All are staff-standing grants
(`operator` + `frontOfHouse`/`backOfHouse`) whose authority is a role, not a relationship to the target —
`OpDispatchSpec.AuthContext`'s own fourth case (`definition.go:521-530`: *populate none of the envelope's
authContext fields*). Idiom: clinic's `SetProviderHours`/`SetProviderTimeOff`.

**Reads are declared only where the script's own read is grounded, never speculatively.** The
`TargetField` fallback already declares the target VERTEX but not its aspects (`cafe-domain/opmetas.go`
Settle comment), and clinic's five metas declare no `Reads` at all. So: `Charge` declares
`{payload.tabKey}.status` (`require_open_status`) and `{payload.menuItemKey}` + `.price` — the script
*names* that contract, failing with *"caller must declare … in contextHint.reads"* (`ddls.go:562-563`);
`StartVisitSeries` declares `{payload.patientKey}` + `{payload.providerKey}`, which its own field docs call
mandatory (`visitseries.go:91-96`). Everything the scripts reach through a **class-(e) bounded
enumeration or a data-derived follow-up** — `ClaimTask`'s `kv.Links(queuedFor)` → `holdsRole` read
(`ddls.go:419-449`), `require_workplace`'s site walk, the visit-series guard's prior-`.series`/`.paused`
reads — is **deliberately undeclared**: those keys are unknowable to the caller in advance, which is
exactly why the read posture sanctions them live.

**`orchestration-base` gains its first `OpMetas`.** Its `Package` has no such field today
(`package.go:46-55`), so the field, the `OpMetas()` function, and a `manifest.yaml` `opMetas:` block all
land together. Verified not assumed: **no** package declares a `ClaimTask` meta anywhere, so this is not
the cross-package shadow the S1 rule warns about — orchestration-base owns the op's DDL.

**`clinic-reminders` upgrades bare→full in place.** `visitSeriesOpMetas()` (`visitseries.go:845-852`)
already returns four bare `{OperationType}` entries; three become full descriptors and
`AdvanceVisitSeries` **stays bare** — it is engine-driven (Weaver re-arms the series), carries no debt
entry, and no human triggers it. Count and order are unchanged, so `clinic-reminders/manifest.yaml`
needs no edit.

**Gotchas.** A manifest's `opMetas:` is an **order-matched list of `operationType`** verified
field-by-field (`internal/pkgmgr/manifest.go:141,189-193`), so an added meta needs a manifest entry **in
the same position**. `cafe-domain/package_test.go:49` pins `len(Package.OpMetas)` at 2 → 4. Package edit
⇒ version bump in **both** `package.go` and `manifest.yaml` or the install no-ops on a running stack. A
descriptor is not an authorization change: every one of these ops keeps its existing guard, and no guard
is loosened to make a descriptor "work".

**Non-goals.** identity-domain (§3.4, conforms last); the 5 `lens-cypher` entries; `AdvanceVisitSeries`
and the other bare orchestration metas; any FE work to render these descriptors; any guard, grant, or
permission-semantics change.

**Shipped `e27e7da0`.** All nine landed; `s1Debt` is now identity-domain's seven alone. Three things came
out of the build that the brief did not anticipate:

- **The vocabulary had a live hole, and writing these found it.** A template is *substituted*, not
  evaluated, so hanging an aspect suffix off an **optional** field leaves the literal text behind when the
  field is absent: `{payload.identityKey}.patientClaim` becomes `.patientClaim`. NATS rejects that as
  malformed rather than reporting it absent, and `step4_hydrate` turns anything that is not
  key-not-found into a **rejected operation** — so registering a patient with no identity, the ordinary
  path, would have failed on a key naming no field and no op. Two fixes, because either alone leaves the
  hole open for someone: Facet declares a key only when every dot-separated segment survived substitution
  (`wholeKey`, applied to the required half too), and `lint-package-standard` refuses a descriptor that
  builds a key around a field nothing guarantees is present — not required, not a `contextParam`, not the
  `targetField`. Normative text would not have bound the next author; the gate does.
- **That rule found six instances already shipped**, in `lease-signing`'s `DecideLeaseApplication` and
  `service-domain`'s `RecordServiceOutcome`, where the wrapped field is supplied on one branch and not the
  other (`unit` on a first approve but not a decline). They are **baselined shrink-only**, not guessed at:
  deciding between guaranteeing the field and declaring the read bare is a semantics call in each owning
  package. The runtime fix means they now cost a worse diagnostic rather than a rejection.
- **Complete metadata is not a rendered button.** A client must also resolve `TargetType` against
  something it projects, and Facet's context carries no `tab`, `patient`, `visitseries`, `task`, or
  `studio` entity — so eight of the nine degrade rather than offer. `ClaimTask` is one line from working
  (`ctx.taskKey` is populated but absent from `resolveTargetKey`'s candidates). Filed as its own row; the
  descriptors are what make closing it possible at all, and the comments no longer claim otherwise.

**Remaining `s1Debt`: 7**, all identity-domain (§3.4, conforming last by design). **`lens-cypher`: 5**,
unchanged.

## 8. §3.4 — identity-domain conforms: Inc 5 build note (Winston, 2026-07-26)

**Scope-diff gate.** §3.4's scope sentence is *"identity-domain conforms last (it is the idiom source; its
own gap is S1 — the four consumer credential ops have no metas)."* Inc 5 takes exactly that: the **7**
remaining `s1Debt` entries, all identity-domain. The baseline is now **empty**, so S1 binds corpus-wide with
no exemptions — the milestone Inc 3 reached for `structure-pins`. Narrow-only: no guard, grant, or
permission semantics move; the **5** `lens-cypher` entries (identity-domain's own included) are a later
increment. §3.4 says *four* credential ops because that was the census's read; the gate's baseline is the
authority and held **seven**.

**Shipped `52711a5a` — but NOT as seven descriptors, and the difference is the finding.** The brief planned
a full `OpMetaSpec` for all seven. An adversarial review of the drafted descriptors against the scripts and
the client resolver disproved that plan for five of them, before anything merged. What shipped: **two
descriptors, three stated `[no-op-meta:]` exemptions.**

A descriptor is a machine-readable **promise** that a client holding only those fields can build a valid
Contract #2 envelope. Five identity ops cannot honour it, because their submission is a client-side
**ceremony** rather than a filled form. Each reason was verified in code, not assumed:

- **`CreateUnclaimedIdentity` / `RotateClaimKey` / `InitiateCredentialLink` — the caller MINTS a secret.**
  The client generates the plaintext, keeps it to hand over, and submits only its sha256. A rendered form
  cannot mint a preimage, and the schemas carry no shape guard, so a hand-typed 64-hex string is *accepted*:
  `CreateUnclaimedIdentity` mints a person who can never claim their identity, and
  `InitiateCredentialLink`'s unconditioned overwrite (`ddls.go:1063`) silently disarms a pending link.
- **`CreateUnclaimedIdentity` additionally cannot declare its dedup probes.** The
  `vtx.identityindex.<sha256(email|phone|name)>` keys are sha256-derived and the template vocabulary
  substitutes rather than computes. Undeclared, `name_hit` is *always* `None`, so the script always emits a
  CreateOnly index write (`ddls.go:753-758`, `:548-550`) — a second registration sharing a normalized name is
  **rejected with RevisionConflict**, and `absentConditionedCreates` will not retry it
  (`commit_path.go:598-606`). The live CLI declares them for exactly this reason
  (`cmd/lattice/identity/identity.go:103,257-260`).
- **`CompleteCredentialLink` is submitted as a different actor than the client authenticated as.** The
  Gateway's raw-credential carve-out sets `resolvedActor` to the raw credential (`gateway.go:503-505`), while
  a `self` authContext names the *resolved* business identity — so step 3 denies `target != actor`
  (`step3_auth_capability.go:540-545`) in exactly the multi-credential case the op exists for. Its
  credentialindex is load-bearing too, unlike ClaimIdentity's: `UnlinkCredential` tombstones that key
  (`ddls.go:1260`), and the revive needs the hydrated revision (`ddls.go:552-565`), so undeclared means a
  previously unlinked credential could **never be re-linked**.
- **`UnlinkCredential`'s one input is a key nothing projects.** Bound credentials come from a protected-lens
  read (`cmd/facet/credentials.go:106`), not `manifest.ent` rows, so no context can fill the field and the
  descriptor reduces to asking a person to hand-type a `vtx.identity.<NanoID>`.

**This corrects a claim the brief made.** The brief asserted the two sha256-derived probe keys "cost a worse
diagnostic, not a weaker guard." That is true **only** for `ClaimIdentity`, whose actor is a fresh credential
with no prior index entry. It is false for the create path (a rejection, above) and false for
`CompleteCredentialLink` (an unrevivable tombstone, above). The comments now say so per-op.

**The two that DID ship.**

| Op | `AuthContext` | TargetField/Type | Declared reads |
|---|---|---|---|
| `ClaimIdentity` | `self` | **none, deliberately** | `{payload.targetIdentityKey}` + `.state` + `.claimKey` |
| `RecordIdentityPII` | **`task`** | `identityKey` → `identity` | `{payload.identityKey}` + `.state` |

**`RecordIdentityPII` is `task`, not the `standing` the brief planned.** Its descriptor-driven caller is the
**applicant** submitting lease-signing's onboarding userTask (`patterns.go:73`) — a `consumer`, who holds no
standing grant for this op at all (`permissions.go:62-66` grants only frontOfHouse/backOfHouse/operator). A
`standing` descriptor sends **no authContext** (`app.js:1592`), and step 3's ephemeral-grant match needs
`{task, target}` (`step3_auth_capability.go:326-334`) — it would be refused every time. The script agrees:
its unclaimed-only confinement is exempted only when `op.authTargetValidated` is set (`ddls.go:1341`), which
is precisely what the task path sets. This is Inc 1's dual-grant rule applied unchanged — the descriptor
names the path the descriptor-driven client walks, because the staff FE hardcodes its own.

**No descriptor lets a client resolve an `identity` target it does not have.** `resolveTargetKey` does not
degrade on that type — it falls back to `me().identityKey` (`app.js:1638-1641`), so an unresolvable target is
**silently replaced by the submitter's own identity** and the degrade gate never fires. For an operator
actor, whose `actor_holds_operator` exemption skips the confinement, a mis-targeted `RecordIdentityPII` would
have written a walk-in's SSN/DOB onto the operator's own identity — create-only, so not even correctable.
`RecordIdentityPII` may name the type only because its task `scopedTo` IS an identity key and matches by type
first (`app.js:1635-1637`); `ClaimIdentity` names none, since its target arrives with the invitation. A test
pins both halves. **The fallback itself is a client defect, filed as its own row** — the gate cannot see it,
because `checkReadTemplates` treats a `targetField` as guaranteed present and a wrongly-resolved target is
still non-empty.

**`Sensitive` is per-OP, and that granularity is the residual.** A client masks every field it renders and
the masked input drops any prefilled value, so the flag is only honest when *all* rendered fields are secret.
That holds for `RecordIdentityPII` (its targetField is filtered out, leaving ssn+dob) and it is the corpus's
first use of the field. It does **not** hold for `ClaimIdentity`, which renders a one-time secret beside a
transcribed vertex key — masking would make the key unenterable, so the secret is echoed. Filed as a row.

**lease-signing's bare `RecordIdentityPII` shadow is deleted in the same commit.** S1 forbids a package
declaring a meta for an op another package owns, and lease-signing's own comment named the precondition —
*"identity-domain ships the op but no op-meta for it"* — that this increment discharges. The collision is not
cosmetic: Loom and Weaver both index op-metas into a flat `operationType → vtx.meta.<id>` map off the
corpus-wide `vtx.meta.>` CDC (`loom/source.go:298-309`, `weaver/registry.go:1129-1140`), so two metas resolve
last-writer-wins. Verified safe: op-meta ids are content-derived, not positional (`installer.go:251`), so
removing a middle entry dangles nothing; lease-signing declares no `RecordIdentityPII` *permission*, so no
`forOperation` link is lost; `validateEffects` does not key on it; and the `leaseApplicationComplete` lens
joins on `onbOp.data.operationType` (`lenses.go:672`), the same string on either vertex.

**The refresh targets gained identity-domain, and that is not incidental.** `refresh-{loftspace,cafe,wellness}`
each `install --force packages/lease-signing` and never mentioned identity-domain. Upgrading lease-signing
alone **tombstones** the old meta (`upgrade.go`) while the replacement is still absent, leaving
`RecordIdentityPII` with no op-meta anywhere — `loom/engine.go:936-940` then NAKs the trigger message
indefinitely and the onboarding userTask is never created. All three now refresh identity-domain first, in
dependency order.

**Named residuals, each filed as a row (same docs commit).** (a) the client's `identity` targetType fallback
substitutes the submitter instead of degrading — consumer: any future surface offering a `dispatchClass:
"identity"` op; (b) `Sensitive` has no per-field granularity — consumer: `ClaimIdentity`'s secret, echoed
today; (c) the five exempted ops stay undiscoverable to a descriptor-driven client, which is a **vocabulary**
gap (no client-side minting step, no expression for a derived key) rather than package debt — consumer:
Facet's own credential surfaces, which hardcode all five today. **Adjacent find, not filed as debt:**
`checkCanonicalNameCollision` (`installer.go:581-600`) puts every `OperationType` in its declared-name set but
only compares against `canonicalName` aspects, which `build.go` never emits for op-metas — so the guard could
not have caught the original double-meta and cannot catch a future one. Named here because it is the *class*
the shadow deletion only fixes an instance of.

**Landing it on the running stack surfaced a kernel bug, and only this fire could have.** This is the
first change in the corpus to REMOVE an op-meta, so it is the first to make a package upgrade emit a
**tombstone**. identity-domain went 0.8.1 → 0.9.0 cleanly (its two descriptors are live and correct,
`authContext` `self` and `task`). lease-signing's upgrade was **rejected**: *"mutation requires a document
dict: vtx.meta.EUay…"*, which is `opMeta:RecordIdentityPII`. The planner is right — `--dry-run` reports
`op=tombstone`, `tombstoned=1` — and current source is right: `install_ddl.go`'s UpgradePackage script takes
the `if mop == "tombstone"` branch *before* the document check (landed `6b68fde4`, 2026-07-22). The script
actually **installed** in Core KV has no such branch. Root cause, verified: `primordial.go:316-323` skips the
whole primordial batch when the tracker key exists, and seeds with `CreateOnly` — so **kernel DDLs are
written once and never updated**, and any Core KV seeded before that commit can never apply a key-removing
upgrade. Filed to the Lattice lane as its own item (a versioned kernel-seed migration); **not** worked around
by wiping the shared stack. Live consequence until then: two `RecordIdentityPII` metas coexist — the full one
and lease-signing's bare shadow — resolving last-writer-wins, harmless because both name the same op.

**Remaining `s1Debt`: 0.** **`lens-cypher`: 5** (`augur`, `console-operator`, `demo-operator`,
`identity-domain`, `rbac-domain`), unchanged — the next increment.

## 9. §3.3 — the last five lens-cypher packages: Inc 6 fire brief (Winston, 2026-07-26)

**Scope sentence (verbatim, S6).** *"a `lens_cypher_test.go` executing every lens over a seeded topology"*
— for the five packages `s6Debt["lens-cypher-test"]` still exempts. **Scope-diff gate:** narrow-only. No
lens spec, adapter, output descriptor, grant, permission or DDL changes; the deliverable is five new
`_test.go` files plus the five baseline deletions. If a test proves a lens *wrong*, that is a finding to
file, not a spec edit inside this increment.

**Touch-list (verified live).** 9 lenses across 5 packages:

| Package | Lens | Shape | What only execution can prove |
|---|---|---|---|
| console-operator | `consoleOperatorReadGrants` (`package.go:64-70`) | GrantTable/postgres | the `canonicalName` WHERE actually filters — a broken filter hands the read-side **wildcard** grant to every role-holder; `nanoIdFromKey` yields the bare NanoID RLS matches on, not the full key |
| demo-operator | `demoOperatorReadGrants` (`package.go:65-71`) | GrantTable/postgres | same, for the read-only demo boundary |
| rbac-domain | `capabilityRoles` (`lenses.go:80-91`) | actorAggregate | the `$actorKey` anchor keeps another actor's permissions out; a role-less actor yields the **single degenerate all-null entry** `RealnessFilter`+`EmptyBehavior:delete` are premised on |
| rbac-domain | `capabilityRoleIndex` (`lenses.go:97-103`) | op-aggregate | two roles granting one op collapse to one row carrying both names |
| identity-domain | `identityIndexHint` (`lenses.go:126-129`) | flat | one row per live index vertex, carrying only hash-key + identityKey |
| identity-domain | `identityCredentialsRead` (`lenses.go:110-117`) | Protected + SecureColumn | `WHERE credentialBinding.data <> null` is fail-closed, and `authz_anchors` is the identity's **own** NanoID — the whole RLS self-anchor |
| identity-domain | `identityAnchors` (`lenses.go:158-173`) | actorAggregate | the **inverse** `<-[:identifiedBy]-` walk, container stamping, and the degenerate-entry premise |
| augur | `augurProposals` (`lenses.go:134-151`) | flat | a claim still in flight (no `.proposed`/`.review`) projects null model columns rather than dropping |
| augur | `augurDispatchPending` (`lenses.go:95-107`) | actorAggregate | `violating` is **default-deny**: true only for `reviewState = "approved"`, false for pending/rejected/**null** |

**Precedent to mirror:** `packages/edge-manifest/lens_cypher_test.go` (fixture with `vtx`/`aspect`/`edge`/
`project`, embedded NATS via `jsstore.Dir(t)`, deterministic 20-char NanoIDs, `full.New()` +
`ExecuteWith`). Each package keeps its own copy of the ~40-line harness — 19 files already do, and a shared
helper introduced for five files only would create a second idiom. *(Adjacent find, not filed as debt: that
harness is duplicated 19× and would be a clean `internal/lenstest` extraction as a single corpus-wide sweep,
which is a different item from this one.)*

**Increment order + green check:** one package at a time, `go test ./packages/<pkg>/` after each; then
delete that package's `s6Debt` entry and re-run `STRICT=1 go run ./scripts/lint-package-standard.go` —
the gate's stale-entry check makes the deletion self-verifying (an entry left behind after the file lands
fails the gate just as loudly as a missing file).

**Gotchas in scope:** the `full` engine's equality is `=`, not `==`, and compares null false rather than
erroring; an actor-aggregate spec needs `$actorKey` in `Parameters`; `augurDispatchPending`'s `violating`
is a *column*, so the test asserts on the projected value, not on row presence.

**Non-goals:** the `Output` descriptor's own wrapping (BuildKey / RealnessFilter / EmptyBehavior) is engine
machinery with its own tests in `internal/refractor` — these tests drive the **cypher**, exactly as the 19
precedents do. No `verify-package-*` script, no version bumps (no installed content changes).

### Inc 6 build note — shipped `dc396cb3`

Both S6 baselines are now **empty**, the milestone S1 reached in Inc 5: the rule binds corpus-wide with no
exemptions, so a new lens ships with a test that executes it or reds the gate. Nine lenses, five files, no
spec/grant/permission/DDL movement — the scope-diff gate held.

**Three things the brief got wrong, each corrected against the engine rather than around it.**

1. **The degenerate-entry premise is real, but not where the brief looked.** `capabilityRoles`' role-less
   actor does project one row — and its two collects behave *differently* on the same null binding:
   `collect(DISTINCT {map literal})` yields the all-null entry, `collect(DISTINCT role.key)` yields `[]`.
   The first assertion written asserted `[null]` for `roles` and failed. That asymmetry is load-bearing, not
   trivia: `RealnessFilter` names a field *inside* `platformPermissions` precisely because pointed at
   `roles` it would find nothing to filter and could never mark the row unreal — and `EnvelopeFn`
   (`projection/driver.go:99-107`) only emits the retracting `ErrDeleteProjection` when nothing is real. It
   is pinned now, in both directions.
2. **identity-domain was never actually uncovered.** `lenses_internal_test.go` already drives
   `identityAnchorsSpec` relation-by-relation (the Osei provider-binding regression, the landlord `manages`
   hat, its negative). The S6 rule keys on the **filename** `lens_cypher_test.go`, so a package with real
   executed-cypher coverage under any other name reads as debt. The new file therefore adds only what was
   genuinely missing — all four walks *concatenated into one row*, and the all-degenerate empty case — and
   the duplicate inverse-walk test drafted for it was dropped before commit rather than shipped as
   redundancy. **The gate's filename key is a proxy for the property, and it can be wrong in both
   directions**: it credits an empty `lens_cypher_test.go` and discredits coverage living elsewhere.
   Left as-is (a filename is what makes the rule mechanical and unarguable), named here so the next reader
   does not mistake the baseline for a measurement.
3. **`<>` against a null is not the mirror of `=`.** Inverting `augurDispatchPending`'s approved test to
   `<> "approved"` made a **claim still in flight dispatch** — the absent review state compares true under
   `<>` where it compares false under `=`. The spec's default-deny therefore depends on the *positive*
   phrasing, not merely on comparing against the right string.

**Mutation-checked, not assumed.** Two spec mutations were run against the new tests before commit —
relaxing the console-operator role filter to match any role, and the `<>` inversion above. Each reds the new
tests (3 and 4 respectively). A test file that ships green without ever being shown to fail is the same
unmeasured claim S6 exists to end.

**The two operator packages seed each other's role deliberately.** `consoleOperatorReadGrants` and
`demoOperatorReadGrants` are byte-near-identical, differing only in a role name and a `grant_source`, and
each emits `anchor_id '*'`. The realistic defect is not a malformed cypher but a copy-paste that leaves the
sibling's role name in place — so each world seeds a holder of the *other* role and asserts it does not
project. `actor_id` is pinned bare for the same class of reason: a full `vtx.identity.<id>` there denies
every row through RLS while the grant table still looks populated.

**Nothing to rebuild (MERGED ≠ RUNNING).** The fire touched five `_test.go` files and one `scripts/` gate;
no `_test.go` file is compiled into a `cmd/` binary and `lint-package-standard.go` is a CI-invoked tool, so
no running binary is stale as a result. `make verify-kernel` is not runnable from a worktree (it needs the
main checkout's generated `lattice.bootstrap.json`) and has no bearing here — no kernel seed moved.

**Named residual, filed as a row:** the ~40-line embedded-NATS harness is now duplicated **24×** across the
corpus's cypher tests. Extracting it to an `internal/lenstest` helper is a single mechanical sweep, but
doing it for five files only would have created a second idiom, so it stays a corpus-wide item with a named
consumer: the next package to declare a lens, which today copies 40 lines to satisfy S6.

**Residual swept — shipped `c6294007`.** Diffing all 24 (by then) copies showed only the KV-bucket-setup and
NanoID-derivation halves were byte-identical modulo the local function name — the vtx/aspect/edge/project
fixture layer had already diverged per package (some packages skip it entirely for domain-specific builders
like objects-base's `object`/`content`/`link`), so only the truly universal pair moved to
`internal/lenstest.KVs` / `.NanoID`. 940 lines removed across 26 files (24 `lens_cypher_test.go` + 2 files
with a cross-file call site), no fixture-layer behavior changed.

## 11. S10 — one implementation per policy-free guard helper (build note, 2026-07-26)

Added with the `worksAt_covers` multi-parent fix (facet-staff-worlds-design.md §10), which is also the
evidence for it: a confinement defect was filed against three packages and lived in **nine copies across
seven**. The gate is corpus-wide in `scripts/lint-package-standard.go` (`checkS10`), alongside S9, because
the copies do not line up with packages at all — wellness-domain alone holds three, one per DDL script.

Two decisions worth keeping:

- **It compares code, not text.** Each copy's comment block legitimately names its own caller
  (`require_workplace(resolve(x), …)` vs `require_workplace(account_unit(x), …)`), so pinning whole bodies
  would fail on prose that carries no behaviour. Comments and blank lines are stripped; the statements must
  agree.
- **The helper list was measured, then RE-measured after a reviewer called the measurement wrong — and it
  was.** `require_workplace` (3 distinct bodies) is genuinely, documentedly divergent, and excluding it is
  correct. `actor_holds_operator` was excluded on the same reasoning, recorded as "identity-domain and
  service-domain resolve the operator differently" — **which was false.** It was inferred from a digest count
  without reading the bodies. All twelve are the identical `holdsRole` → `.canonicalName == "operator"` walk;
  the only difference was five spellings of one page-limit constant, every one of them `50`. Since
  `actor_holds_operator` *is* the operator escape — the single branch that clears every workplace guard in
  the corpus — it was the worst possible thing to exempt, and the exemption came with a recorded reason that
  would have stopped the next reader from re-checking. The constants are now one name and the helper is
  pinned. **An exclusion justified by an unverified premise is worse than no gate at all.**

The rule's escape hatch is renaming, not an exemption baseline: a variant that genuinely differs is a
different function and should say so, which is exactly what maintenance-domain's `enforce_workplace`
already does.

Two more holes closed in the same fire, both found by review rather than by the author:

- **The digest was indentation-blind**, which in an indentation-significant language is semantics-blind. Two
  bodies differing only in nesting depth hashed alike — and a statement at the wrong depth is *precisely* how
  the walk this rule protects went wrong. Depth is now hashed as a normalized leading-whitespace count, so a
  tabs-vs-spaces reindent is not flagged but a nesting change is.
- **The bounds were outside the digest.** A body names its limits by constant, so `ROLE_PAGE_LIMIT = 50`
  becoming `10` in one script weakened a guard without changing any line the digest compared. Each named
  bound now has to hold one value corpus-wide.

Each of the three classes is mutation-tested: diverge one body, re-indent one line, or lower one constant,
and the gate names it. The first version of this rule caught only the first of those three.

## 12. S10 — `parts_of` joins the pin: fire brief (Winston, 2026-07-26)

**Scope sentence.** Converge all 31 copies of the vertex-key parser `parts_of` onto one implementation and
add it to `sharedGuardHelpers`, so the corpus's most-copied helper is pinned like the other three.

`parts_of` is where a key stops being a string and becomes a typed vertex reference. Every guard downstream
of it — `worksAt_covers`'s `actor_id`, every `require_live_typed`, every deterministic link key a script
builds — is spending the type and id this function hands back. It is copied more times than the three
already-pinned helpers combined, and it was measured at **7 distinct implementations across 31 copies**.

**One of the seven is a real defect, not prose drift.**

| Variant | Copies | Divergence | Reachable? |
|---|---|---|---|
| majority | 21 | — | — |
| `orchestration-base/ddls.go:234` | 1 | `len(parts) < 3` where every other copy has `!= 3` | **yes** |
| `clinic-reminders` ×3 | 3 | drops the `want_type != ""` guard (fails closed) | no caller passes `""` |
| `cafe-domain` ×2 | 2 | drops the empty-type-segment check | no caller passes `""` |
| `objects-base`, `service-domain` | 2 | calls `split_key(key)` for `key.split(".")` | identical — `split_key` has one implementation |
| `orchestration-base/mark_expired.go`, `capability-author` | 2 | two-argument: no `want_type` at all | by construction |

`< 3` **accepts a key with four or more segments and silently truncates it**. An **aspect** key
(`vtx.leaseapp.<id>.terms` — four segments, Contract #1) is accepted, truncated to `("leaseapp", <id>)`,
and `CreateTask` builds a `scopedTo` link out of the truncation — a link to a vertex the submitter never
named. Nothing else in the corpus is this lax; the checked shape is exactly what Contract #1 §1.1 already
requires, so this narrows to the contract rather than changing it. No 4-segment key reaches these fields
from any live producer (`internal/weaver/strategist.go:229` and `internal/loom/engine.go:951` pass vertex
roots; `cmd/loupe/tasks*.go`, `cmd/loftspace-app`, `cmd/facet` all build 3-segment keys), so the tightening
breaks no caller.

**The arity test is the whole guard — a first draft of this brief got that wrong and the correction is the
point.** It claimed the defect needed `< 3` *combined with* an empty `want_type`, and that `ddls.go:274`
was the corpus's only untyped caller. Both halves are false: `want_type` is compared against `parts[1]`,
which a four-segment aspect key still fills correctly, so `parts_of("vtx.meta.<id>.canonicalName",
"forOperation", "meta")` passes the type check — **all eleven callers of that script were exposed, not one**
— and thirteen call sites across seven packages passed an empty `want_type` at the time of writing. Two
independent reviewers caught it. This is the same failure the S10 note (§11) already records against
`actor_holds_operator`: a premise inferred from a digest rather than read off the bodies.

**The two-argument copies converge rather than get renamed.** S10's escape hatch is renaming, but that is
for a variant that carries *different policy*; these two carry the same policy with an argument missing.
`capability-author` uses the returned type (`requester_type`, `package_type`) and `mark_expired` discards
both — i.e. both want precisely `want_type == ""`, which the majority implementation already expresses.
Converging them means one function corpus-wide instead of two names for one idea, and it closes
`capability-author`'s dropped empty-type check on the way (`requester_type` could come back `""` today).

**Touch list** (verified live). Bodies: `orchestration-base/ddls.go:234`, `orchestration-base/mark_expired.go:159`,
`capability-author/ddls.go:312`, `cafe-domain/ddls.go:375` + `:923`, `clinic-reminders/ddls.go:148` +
`followups.go:144` + `visitseries.go:367`, `objects-base/ddls.go:329`, `service-domain/ddls.go:805`.
Call sites gaining a `""` argument: `mark_expired.go:194`, `capability-author/ddls.go:361` + `:547` + `:620`.
Gate: `scripts/lint-package-standard.go` `sharedGuardHelpers`. Six packages change scripts and so take a
**version bump** — a same-version edit no-ops on a running stack.

**Increment order.** (1) converge the five 3-argument variants; (2) converge the two 2-argument copies and
their four call sites; (3) add `parts_of` to the pin and watch it go green; (4) version-bump the six
packages. Green check after each: `STRICT=1 go run ./scripts/lint-package-standard.go`, then
`go test ./packages/...`.

**Non-goals.** `require_workplace` (9 copies, 3 variants) stays unpinned and stays on the board row: two of
its three variants are load-bearing — `maintenance-domain` delegates to the deliberately-renamed
`enforce_workplace`, and `clinic-reminders/visitseries.go` omits the `op.authTargetValidated` short-circuit,
which is *stricter*, not weaker. Adding that short-circuit to make a digest agree would weaken a guard to
satisfy a linter, which is the wrong direction; deciding it needs the scope=self grant analysis this fire
does not do. The shared-prelude mechanism itself also stays out of scope — pinning is the ratified pattern
(S10) and does not preclude a prelude later, which the gate's own error message already anticipates.

### §12 build note — shipped `c35eb3be`

All 31 copies agree and `parts_of` is in `sharedGuardHelpers`. Three things changed beyond the brief, each
because review found the brief's shape insufficient rather than wrong:

- **The canonical body gained an empty-id check.** The brief converged on the *majority* body, which
  accepts `vtx.<type>.` and returns `id == ""`. That fails closed everywhere today — every call site either
  gates liveness first or builds a deterministic key that misses and denies, and `step6_validate` rejects
  any mutation whose key is not a canonical NanoID (`internal/substrate/keys/keys.go:75`). But the corpus
  already held the stricter shape under a different name (`vertex_parts` in `clinic-domain/site.go:247` and
  `loftspace-domain/ownership.go:136` both reject the empty id), so pinning the laxer of two shapes the
  codebase already had would have made that laxity authoritative and left ~160 call sites each responsible
  for noticing. It costs nothing: no legitimate caller can pass an empty id, because such a vertex cannot
  exist.
- **S10 now digests the `def` line.** The pin compared bodies only, so
  `def parts_of(key, name, want_type=""):` hashed identically to the strict form while every call in that
  script could omit the argument and skip the type check — exactly the state the two two-argument copies
  were in. A body is only pinned if its signature is. All four pinned helpers already had one spelling
  each, so this bound with no reconciliation.
- **The one behavioral fix shipped with tests.** `TestCreateTask_ExtraSegmentKey_Rejected` and
  `TestCreateTask_EmptyIdSegment_Rejected` (`packages/orchestration-base/task_script_test.go`) were run
  against the pre-fix body and both fail there, so they discriminate rather than merely pass.

Each rule is mutation-tested as §11 requires: reverting one body to `< 3` and adding a default argument to
one signature are each named by the gate, at the exact site.

**Named residuals** (folded into the lane's prelude row, not dropped):

- **`require_workplace` stays unpinned**, as the brief scoped. Unchanged.
- **The rename hatch is already exercised, and the renamed copies drift.** `vertex_parts` exists with
  **two different signatures under one name** — 3-arg in `clinic-domain/site.go:247` and
  `loftspace-domain/ownership.go:136`, 2-arg in `service-location/ddls.go:213` — plus `unit_parts(key)` in
  `loftspace-domain/ddls.go:305`. All four are the same validator; none is reachable by S10, because S10
  keys on the name. Consumer: the next author who copies whichever one they find first.
- **Two gate-coverage gaps, neither live.** `packageSources()` skips `_test.go`, and embedded Starlark
  already lives in one (`packages/orchestration-base/external_params_test.go:20`); no pinned helper is
  defined in a test file today. And `minGuardHelperCopies = 2` is a flat floor, so 29 of 31 copies could
  disappear silently. Both are gate-design calls, deliberately not made late in a build fire.

## 13. S10 — the workplace guard joins the pin, and the rename hatch closes: fire brief (Winston, 2026-07-26)

**Scope sentence.** Close every remaining escape from the S10 pin: pin `require_workplace` +
`enforce_workplace` by adopting maintenance-domain's two-function factoring corpus-wide, delete the four
renamed copies of `parts_of` (`vertex_parts` under two signatures, `typed_vertex_parts`, `unit_parts`), and make
the three gate-design calls §12 deliberately deferred — per-helper copy floors, `_test.go` in scan, and a
digest-alias rule that catches the next copy-under-a-new-name.

### 13.1 `require_workplace` — the factoring already in the corpus is the answer

§12 left it unpinned because two of its three variants are load-bearing, and recorded that resolving it
"needs the scope=self grant analysis this fire does not do." It does not, and that framing was the block.
Read as bodies rather than as policy, the nine copies are **two functions, not three variants**:

| Shape | Copies | Body |
|---|---|---|
| A — inline | 7 | `if op.authTargetValidated: return` · operator · walk |
| B — factored | 1 (`maintenance-domain/ddls.go:391` + `:409`) | `require_workplace` = validated-target short-circuit → `enforce_workplace`; `enforce_workplace` = operator · walk |
| C — strict | 1 (`clinic-reminders/visitseries.go:552`) | operator · walk |

**C *is* B's `enforce_workplace`, byte for byte** — the same statements, under a name that claims to be
something it is not. And A is B inlined: `validated → operator → walk` in both, same order, same outcome.
So the convergence weakens nothing and needs no grant analysis. §11's escape hatch — *a variant that
genuinely differs is a different function and should say so* — is the mechanism, and maintenance-domain
already built it; the work is adopting it, not designing it.

- The 7 shape-A copies split into the two-function form. Behaviour identical.
- `clinic-reminders/visitseries.go` renames its copy to `enforce_workplace` and updates its two call sites
  (`:595`, `:677`). It gains no `require_workplace`; the self-exemption it deliberately omits stays omitted,
  now stated by the name instead of by a comment the gate cannot read.
- `maintenance-domain` is already canonical and does not change.
- Both names enter `sharedGuardHelpers`.

The prize is that a documented divergence becomes **structure the gate can see**. §11 records the cost of
the alternative: an exclusion carrying a recorded reason stops the next reader from re-checking, and one of
those reasons was already false.

### 13.2 The rename hatch — four definitions, three names, one validator

Every one is `parts_of` with an argument or a return value missing. None carries different policy, so
§11's rename hatch does not license them; converging is the same call §12 made for the two-argument copies.
(§12's residual note also listed `loftspace-domain/ownership.go:136` — that copy is already `parts_of`;
the same fire converged it. Re-measured live rather than inherited.)

| Alias | Site | Equivalent | Call sites |
|---|---|---|---|
| `vertex_parts(key, name, want_type)` → id | `clinic-domain/site.go:247` | `_, id = parts_of(k, n, t)` | `:284`, `:286`, `:309`, `:311` |
| `vertex_parts(key, name)` → (type, id) | `service-location/ddls.go:213` | `parts_of(k, n, "")` | `:330`, `:345`, `:360`, `:374` |
| `typed_vertex_parts(key, name, want_type)` | `service-location/ddls.go:223` | `parts_of(k, n, t)` | `:329`, `:344`, `:359`, `:373`, `:387`, `:388` |
| `unit_parts(key)` → id | `loftspace-domain/ddls.go:305` | `_, id = parts_of(k, "unit", "unit")` | `:335`, `:376`, `:403`, `:431` |

`clinic-domain/site.go` and `loftspace-domain/ddls.go` hold no `parts_of` of their own (each Starlark
script has its own module scope), so both gain the canonical copy. Converging service-location's pair
**tightens** it — its `vertex_parts` skips the empty-id check `parts_of` shipped in §12 — which is the same
direction §12 took and breaks no caller (an empty-id vertex cannot exist). No test asserts on any of the
four error strings, verified corpus-wide.

**Deliberately out of scope, and named rather than dropped:** `loftspace-domain/ddls.go:332`
(`require_manages`) hand-rolls the same three-segment test inline, and it fails with `AuthDenied` rather
than `InvalidArgument` — a different error class on a path where a non-identity actor is a denial, not a
bad argument. Converging it would change an authorization failure's shape; that is a decision, not a
sweep, and it belongs to whoever revisits the ownership probe.

### 13.3 The three gate-design calls

- **Per-helper copy floors.** `minGuardHelperCopies = 2` becomes a per-helper map pinned at the measured
  count, `>=` not `==`: disappearance is what the floor exists to catch, and a new package legitimately
  adding a copy should not have to edit the gate. Mirrors S6's structure-pins, where the pin *is* the
  measurement and lowering it is a deliberate reviewed line.
- **`_test.go` enters the S10 scan.** Embedded Starlark already lives in a test file, and a test fixture
  defining a drifted copy of a pinned helper proves a body that does not exist. No test defines one today,
  so it binds with zero reconciliation — which is exactly when to add it.
- **A digest-alias rule.** After the convergence the remaining hatch is verbatim: copy a pinned helper,
  rename it. Any top-level `def` whose statement digest equals a pinned helper's — under a different name —
  is flagged. An identical body under two names is never legitimate, so this carries no baseline.

**A broader "vertex-key parsing outside `parts_of`" rule was considered and rejected.** A structural
heuristic (`split(".")` + an arity test + `"vtx"`) fires today on ~10 sites across `rbac-domain`,
`location-domain`, `privacy-base`, `identity-hygiene`, `augur` and the walk loops — packages this fire has
not read. A gate that ships with a ten-site baseline teaches the next author that the baseline is where
their code goes. Filed as its own row instead, with those packages named.

### 13.4 Increments + green checks

1. `require_workplace` / `enforce_workplace` factoring across 7 packages; both names into `sharedGuardHelpers`.
2. The three aliases deleted, 18 call sites rewritten, `parts_of` added to the two scripts lacking it.
3. Gate: per-helper floors, `_test.go` in scan, digest-alias rule — each mutation-tested per §11.
4. Version bumps for every package whose scripts changed (a same-version edit no-ops on a running stack).

After each: `STRICT=1 go run ./scripts/lint-package-standard.go` · `go test ./packages/...`. Full gates
before admit: `go build ./...`, `make vet`, `golangci-lint run ./...`,
`STRICT=1 go run ./scripts/lint-conventions.go`, `STRICT=1 go run ./scripts/lint-package-version.go`.

**Non-goals.** The shared-prelude mechanism (pinning is the ratified pattern). Any change to what a guard
*decides* — this fire moves statements between functions and renames aliases; every authorization outcome
is identical before and after, and that invariant is the review's job to falsify.

### §13 build note

Six helpers are now pinned (was four), the aliases are gone, and the three gate-design calls are made.
`sharedGuardHelpers` holds `worksAt_covers`, `workplace_exempt`, `actor_holds_operator`, `parts_of`,
`require_workplace`, `enforce_workplace`; `parts_of` is at 35 copies, `enforce_workplace` at 9.

Three things beyond the brief, each because building it surfaced a shape the brief had not:

- **The floor map needed a missing-entry rule of its own.** `guardHelperFloors[helper]` on an absent key
  returns 0, and `total < 0` is never true — so a helper added to `sharedGuardHelpers` without a floor
  would be pinned for BODY and unbounded for COUNT, silently. That is the same class of silent-pass the
  flat floor was replacing. A pinned helper with no floor entry is now itself an issue.
- **The alias rule compares a second digest, not the first one.** `starlarkFuncSite.code` is a sha256 of
  the normalized statements *including the def line* — deliberately, since §12 — so a rename cannot be
  detected by string-substituting the name into a hash. Sites now carry `anon`, the same digest with the
  function's own name replaced by a placeholder. `code` stays name-bearing so the pin itself still cannot
  be satisfied by renaming; `anon` is what the alias rule reads.
- **One test asserted the guard by its error message.** `loftspace-domain`'s `TestPackage_ScriptGuards`
  pinned the literal `vtx.unit.<NanoID>`, which `unit_parts` had in its message and `parts_of` composes
  from `want_type`. The assertion now pins the CALL — `parts_of(unit, "unit", "unit")` — because the
  message-substring form would have passed just as well with `want_type` dropped, which is the guard.

**Adversarial review changed the gate in four ways, one of them blocking.**

- **A floor of 0 — or a deleted map entry — disarmed all three rules at once.** Go returns 0 for a
  missing `int` key, so `total < 0` never fires; the helper then reached `pinned[helper]` with an empty
  digest set, skipped the body pin via `len(bodies) < 2`, and matched nothing in the alias rule. One line
  reading as routine maintenance (`"parts_of": 0,`) would have re-opened exactly the hole this fire
  closes, on the file that exists to prevent it. The flat floor it replaced could not be switched off,
  because it was an unconditional constant. A pinned helper now needs a floor of at least 2 — the floor
  the floor itself has.
- **The indent digest measured style, not structure.** It hashed a raw leading-whitespace *count*, so a
  whole-body 4-space-to-2-space reindent — a pure style edit — read as a behaviour change, AND carried a
  verbatim copy of a pinned helper past the alias rule under a new name. Widths are now ranked into
  levels: the narrowest distinct indent in a body is 0, the next 1. A body re-indented uniformly keeps
  its digest; a statement moved into or out of a block does not. This also makes the comment above it
  true, which it was not — a rune count is not tab/space invariant.
- **`guardConstantRe` failed OPEN on `WORKPLACE_MAX_DEPTH=4`.** It required a literal `" = "`, and
  Starlark does not. Deleting two spaces made the assignment invisible to the constant check while the
  helper bodies still named the bound by constant, so no digest changed either — a one-keystroke edit
  narrowing the workplace walk in one script with nothing to see it. The pattern now tolerates and trims
  the whitespace, which also stops `= 8` and `=  8` reporting as two values.
- **`pinned` was doing two jobs.** It answered both "is this name spoken for" and "what digests may it
  match", so a helper whose floor failed lost its name-guard and became an alias candidate against the
  other five. Harmless today — no two pinned helpers share a body — but the coupling was unintended.
  Split into `isPinned` (names, always populated) and `pinned` (digests, only for a trusted corpus).

Review also caught a false statement in the gate's own rationale — it claimed *two* of the aliases had
dropped a check, where only `service-location`'s untyped `vertex_parts` had (the other two omit only the
standalone empty-TYPE test, which their `want_type` comparison subsumes). It is corrected in the comment
and in the runtime message. §11 and §12 each record this same failure — a premise inferred rather than
read — and this is its third instance in the same file; the count is stated now precisely so the next
reader does not have to re-derive it.

All new rules are mutation-tested as §11 requires, each named at the exact site: deleting one
`parts_of` copy trips the floor (`found 34 … expected at least 35`); zeroing or deleting its floor entry
trips the usable-floor rule; pasting `parts_of` under a new name trips the alias rule, and so does pasting
it re-indented; a drifted copy in a `_test.go` fixture trips the pin, which it could not before; a
one-line nesting change is still caught while a uniform reindent is not; and `WORKPLACE_MAX_DEPTH=4`
without spaces is now caught, where it was previously invisible.

**Named residuals** (filed as rows, not left in prose):

- **Vertex-key parsing still happens outside `parts_of`, in bodies too different to digest-match.**
  `loftspace-domain/ddls.go`'s `require_manages` inlines the three-segment test and fails `AuthDenied`
  rather than `InvalidArgument`; `rbac-domain`, `location-domain`, `privacy-base`, `identity-hygiene` and
  `augur` hold ~10 more. §13.3 records why a structural gate for these was rejected rather than shipped
  with a ten-site baseline. Consumer: the next author who copies one of those instead of `parts_of`.
- **The alias rule raises the cost of a rename; it does not make one impossible.** It is exact-digest by
  design, so renaming a parameter or inserting a no-op still evades it (a uniform reindent no longer
  does). What it buys is that the historical escape — a verbatim paste under a new name, which is what
  all four aliases were — is no longer free. `require_workplace` and `workplace_exempt` have a second
  gate in `lint-conventions.go`'s `checkWorkplaceExempt`, which derives its helper set from the script
  text and is therefore name-independent; `parts_of`, `worksAt_covers`, `actor_holds_operator` and
  `enforce_workplace` do not. Consumer: whoever next wants a divergent copy badly enough to obfuscate it.
- **`checkGuardConstants` has no floor of its own.** `len(byVal) < 2 → continue` is the same
  compare-nothing shape the helper floors now close, left unclosed on the constants half: if a constant
  vanished from every script at once it would pass silently. Partial deletion fails loudly at runtime
  (the bodies name their bounds, so the script raises), which is why this was not treated as blocking.
  Consumer: a future sweep that moves the bounds somewhere else.

## 14. The 29-package census, re-run (Winston, 2026-07-27)

**§3's adjacent-find discharged.** The original census (§1) scored 15 of 29 registered packages; S1/S6/S7
now bind corpus-wide with zero exemptions (verified green: `lint-package-standard: 0 issues across 29
packages`), so the remaining unmeasured dimensions were S2 (grant-matrix doc block), S3 (read-boundary
tiering), S4 (guard-idiom canonicity), S5 (read-posture annotation), and S8 (D3 everywhere). Four read-only
scouts each audited a disjoint ~7-package slice against all five; two of their flagged findings did not
survive ground-truth verification and are recorded here so the next sweep doesn't re-raise them:

- **clinic-domain S3/S8 — false positive.** A scout matched the wrong lens (the Postgres
  `clinicProviderReadGrants` GrantTable producer) instead of the actual open-KV `clinicAppointments`
  projection, which already carries the required exception comment at `lenses.go:409-411` ("deliberately-
  public directory"). clinic-domain is clean.
- **identity-domain S2 — false positive.** A scout read the doc block's per-op `Note` prose as the
  binder-naming requirement; the package's `permissions.go:7-17` in fact opens with the literal tabular
  `Grant matrix:` block §2 asks for. identity-domain remains the S2 idiom source.
- **S4's canonical-text half is no longer a manual-audit dimension.** `S10` (§11-§13) has since made
  guard-idiom byte-identity a blocking, corpus-wide lint gate — every package the scouts found using
  `require_workplace` / `workplace_exempt` / `actor_holds_operator` / CreateOnly / slot-cell idioms is
  already covered by it, verified green above. What S4 asked for manually, S10 now asks for mechanically;
  future census re-runs can skip it.
- **S3/S5/S8 — clean corpus-wide.** No open-KV lens projects a subject-person's name/contact past an
  opaque key anywhere in the 29; every `kv.Read`/`kv.Links` in the corpus carries a `# read-posture:`
  annotation. Both dimensions can be marked closed rather than re-audited each sweep, absent a new package.

**One genuine, corpus-wide gap survives verification: S2's tabular format is not universal.**
identity-domain's `Grant matrix:` header (op → archetypes, one line each) is the canonical form S2 names,
but roughly a third of the corpus documents grants as narrative prose instead — same information,
different shape, harder to scan and impossible to lint mechanically as long as it stays prose. Verified
directly (not from scout claims alone): `control-authz`, `demo-operator`, `identity-hygiene`,
`lease-signing`, `location-domain`, `loftspace-domain`, `loftspace-ledger`, `maintenance-domain`,
`objects-base`, `orchestration-base` all open `permissions.go` with prose rather than a `Grant matrix:`
table. Filed as its own row (below) rather than fixed in this fire — it is real work (10 files) beyond
what an audit fire should absorb, mechanical, and no different in kind from the S1/S6/S7 sweeps §3 already
ran.

**Census closed as re-run; not re-opened per-fire.** The next package added to the corpus is what should
trigger the next spot-check, not a calendar re-audit — S1/S6/S7/S10 already re-check themselves every CI
run.
