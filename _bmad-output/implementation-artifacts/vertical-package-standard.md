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
hatches: an `[no-op-meta: <reason>]` permission Note for an op no human triggers (S1's own stated
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
  lifecycle ops are exempt but the exemption is stated in the permission Note. **Amended by Inc 5 (§8),
  flagged for Andrew:** a second exempt class exists — a **client-side ceremony** op, which a person *does*
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
descriptors, five stated `[no-op-meta:]` exemptions.**

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
