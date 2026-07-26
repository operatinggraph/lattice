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
**Board row:** [verticals lane](../planning-artifacts/backlog/verticals.md) *Vertical Package Standard*.
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
  lifecycle ops are exempt but the exemption is stated in the permission Note. Bare `{OperationType}` metas
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
  seeded topology (cafe-ledger/loftspace-ledger business lenses currently have none). Packages with
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
`lease-signing/permissions.go:176-196` states why. Not upgraded.

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
