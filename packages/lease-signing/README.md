# lease-signing

The Loftspace lease-application convergence vertical (Epic 14 centerpiece),
grown into the full two-sided lease lifecycle: the **applicant** side
(apply → qualification profile → onboarding/bgcheck/payment convergence →
review terms → sign → withdraw) and the **landlord** side (approve/decline a
qualified application with an optional reason). It wires the prior bricks — the
`leaseapp` vertex type, the actorAggregate convergence lens, the §10.8 playbook,
the Loom `externalTask` patterns + their `instanceOp`/`replyOp`/`dispatchOp`
DDLs, and `SignLease` — into one installable package (currently **v0.10.0**).

## Inventory

| Kind | Name | Purpose |
|------|------|---------|
| DDL (vertex type) | `leaseapp` | The application vertex `vtx.leaseapp.<id>` (root `{}` — D5). Permitted commands: `CreateLeaseApplication`, `SignLease`, `WithdrawLeaseApplication`, `DecideLeaseApplication`, `SetApplicantProfile` (see **Operations**). |
| DDL (externalTask instanceOp) | `leaseServiceInstance` | `CreateLeaseServiceInstance` — mints the claim vertex `vtx.service.<handle>` (`.class` + `.family` aspects, `providedTo` link), emits `external.<adapter>`. |
| DDL (externalTask replyOp) | `leaseServiceReply` | `RecordLeaseServiceOutcome` — records the `.outcome` aspect from `{externalRef, result}`, emits `orchestration.externalTaskCompleted{externalRef}`. |
| DDL (externalTask dispatchOp) | `leaseServiceDispatch` | `RecordServiceDispatch` — records the pending (async) call in the `.dispatch` aspect (D5). |
| Lens (actorAggregate) | `leaseApplicationComplete` | One-row-per-anchor convergence lens → `weaver-targets` bucket, bare-NanoID key (§10.2). Projects the `missing_*`/`inflight_*`/`declined_*` gap state, the unit `.listing`/`.address` and applicant `.terms` (read-only review columns), the landlord `.decision` (`landlordDecision`/`declineReason`), `signedAt`, and the derived qualification signals (`incomeToRentMet`, `employmentVerified`, `referenceCount`, `hasCoApplicant`/`hasGuarantor`, `guarantorIncomeToRentMet`). |
| WeaverTarget (playbook) | `leaseApplicationComplete` | gap → remediation (§10.8). |
| LoomPattern | `backgroundCheck`, `collectPayment` | single `externalTask` step each, `completionDomains: ["orchestration"]`. |
| LoomPattern | `onboarding` | single `userTask` step (`RecordIdentityPII`), `completionDomains: ["orchestration"]`. |
| OpMetas | `SignLease`, `CreateLeaseServiceInstance`, `RecordLeaseServiceOutcome`, `RecordServiceDispatch` | `forOperation` resolution + discoverability. `RecordIdentityPII` is NOT here: identity-domain owns that op and declares its descriptor, and a second meta for one operationType would resolve last-writer-wins in the engines' flat index. |

Depends: `identity-domain`, `service-domain`, `orchestration-base`.

## Operations (the `leaseapp` vertex type)

All `leaseapp` commands are granted to `operator` (the trusted single-identity
model — read-path auth is deferred to D1). All relationships are **links**, no
keys-in-data (Contract #1); aspects hold business data (D5).

- **`CreateLeaseApplication{applicant, unit, [leaseAppId], [moveInDate, leaseTermMonths, requestedRent]}`** —
  mints the application + the `applicationFor` link (→ applicant) and the
  `appliesToUnit` link (→ unit), and a per-(applicant, unit) **guard LINK**
  `lnk.identity.<a>.appliedToUnit.unit.<u>` (`CreateOnly` = the duplicate guard;
  a second concurrent application for the same pair RevisionConflicts;
  revive-on-re-apply after withdrawal). Optional `.terms` aspect
  (`moveInDate`/`leaseTermMonths`/`requestedRent`). Replaces the former
  `unit.leaseApplications` key-list index aspect (a Contract #1 violation) with
  the guard link.
- **`SetApplicantProfile{leaseAppKey, unit, annualIncome, employmentStatus, …, hasCoApplicant?, hasGuarantor?, guarantor*?, coApplicant*?}`** —
  writes THREE aspects, split along the retention-class-key-custody-design.md
  §9.1 sensitivity boundary: the SENSITIVE `.profile` (class `applicantProfile`
  — Contract #1 §1.5 namespaces the class; the key stays `.profile`) holding
  **only** the applicant's own raw financials (income, employment, employer,
  guarantor relationship/income); the SENSITIVE `.underwritingParties` holding
  every THIRD-PARTY identifier (guarantor/co-applicant name/contact, and the
  applicant's own references, since a reference names someone else); and the
  NON-sensitive `.applicationSignals` holding the DERIVED landlord signals.
  Both sensitive aspects are **encrypted**, DEK-custodied on the package's own
  `underwritingRecord` retention class (`RetentionClasses`) rather than the
  applicant's identity, and **NEVER projected** by any lens. All three aspects
  are written in ONE UNCONDITIONED-upsert batch every submission — including
  `.underwritingParties` when it carries no field (an empty `{}`), so a
  re-submit that drops a prior guarantor/co-applicant/references clears them
  rather than leaving them stale. The op **derives** the landlord signals the
  lens projects (`incomeToRentMet` = gross monthly ≥ 3× the
  unit's listing rent, read on demand via `kv.Read`; `guarantorIncomeToRentMet`;
  `employmentVerified`; `referenceCount`; the co-applicant/guarantor booleans).
- **`SignLease{leaseAppKey}`** — writes the `.signature` aspect `{signedAt}`
  (`CreateOnly` — rejects an already-signed application), closing
  `missing_signature`.
- **`DecideLeaseApplication{leaseAppKey, decision, [reason]}`** — the **landlord**
  decision: writes the `.decision` aspect `{value (approved|declined), decidedAt,
  reason?}` (UNCONDITIONED upsert — a later decision overrides). `approved` opens
  `missing_listingLeased` (the unit leases via `directOp(SetListingStatus)`);
  `declined` is a terminal rejection (`declineReason` is projected for the
  applicant's declined banner + a fair-housing record).
- **`WithdrawLeaseApplication{leaseAppKey, unit, applicant}`** — soft-deletes the
  application (the convergence row drops from My Applications) and FREES the
  guard link so the applicant can re-apply to the same unit. Validates `unit`
  and `applicant` are the application's actual link targets (UnitMismatch /
  ApplicantMismatch).

## The externalTask seam (Contract #10 §10.5/§10.6)

An `externalTask` step is two waits in sequence: Loom submits the **`instanceOp`**
(`CreateLeaseServiceInstance`) carrying the bare handle it minted, parks on
`token.<handle>`, and disarms the creation-deadline once the instanceOp commits
(the bridge wait is then unbounded). The instanceOp:

1. prepends the package-chosen claim-vertex type → `vtx.service.<handle>`;
2. mints it template-less with a `.class` aspect (`service.<family>.instance`, the
   14.1 shape) and a `.family` aspect (the lens's discriminator — see below), plus
   the `providedTo` link to the applicant identity;
3. emits `external.<adapter>` off its own transactional outbox, body
   `{instanceKey, adapter, replyOp, params, externalRef, idempotencyKey}` — the
   shape the bridge's `externalEvent` reader consumes.

The bridge calls the adapter and posts the **`replyOp`**
(`RecordLeaseServiceOutcome`) with payload **`{externalRef, result}` only** — no
`status`, no `completedAt`. The replyOp:

1. reconstructs `vtx.service.<externalRef>`;
2. derives `status` + `completedAt` itself (see below);
3. writes the `.outcome` aspect `{status, completedAt, result}` (the 14.1 shape, D5);
4. **emits `orchestration.externalTaskCompleted{externalRef}`** — the uniform
   completion signal Loom correlates on (symmetric to `orchestration.taskCompleted`
   for a userTask). **Without this event the step never completes** (the deadline
   disarmed, the bridge reply carried no completion signal). This is why the
   patterns declare `completionDomains: ["orchestration"]`, not the replyOp's own
   domain.

The two wrapper DDLs are a **matched pair**: both choose `service` as the claim
type, both map the bare handle ↔ `vtx.service.<handle>`, and the replyOp echoes
the same bare handle as `externalRef`.

### Why wrapper DDLs (not 14.1's service ops)

14.1's `CreateServiceInstance` does not emit `external.<adapter>`, and
`RecordServiceOutcome` takes a full `instanceKey` + caller `status`/`completedAt`
and emits `service.outcomeRecorded` — not the `orchestration.externalTaskCompleted`
Loom needs, while the bridge supplies only `{externalRef, result}`. Reusing them
would require editing the DONE service-domain. The `.outcome` aspect **shape** is
reused (D5 fidelity); the ops are package-local.

## LOUD FLAG — the `status="completed"` demo simplification (Q2)

The bridge posts a reply **only on adapter success** (an adapter error is
Nak+retry, never a reply), and supplies no structured status. So the replyOp
**derives `status="completed"` on every reply**, stores `result` verbatim
(unparsed — the free-form adapter string is brittle to parse), and derives
`completedAt = time.rfc3339_utc(op.submittedAt)` (the bridge supplies no
timestamp). **A `failed` outcome has NO producer on the Phase-2 bridge path.**

**Phase-3 plug-in point:** when an adapter returns a structured pass/fail result
(or a bridge change threads `status` onto the reply payload), the replyOp reads it
instead of hard-coding `completed`, and the lens's `missing_*` predicate keys off
the real status.

## Freshness — bgcheck is freshness-gated, payment is ever-completed

The §10.2 model is `missing_bgcheck = NOT EXISTS(check WHERE date > now − window)`.
The lens ships the freshness **PREDICATE**, and the eager auto-reopen-at-expiry is
complete end-to-end: the temporal lane's fired `@at` submits a generic `MarkExpired`
op (the platform `freshnessMarker` DDL in **orchestration-base**) that re-touches the
application, reprojects the row with a fresh `$now`, and re-opens `missing_bgcheck`
the moment freshness lapses (the `internal/leaseconvergence` e2e proves the full
re-open → re-dispatch → re-converge chain across multiple freshness cycles).

- The replyOp stamps `validUntil = completedAt + bgcheckFreshnessWindow` onto the
  `.outcome` aspect (`time.rfc3339_add` — a pure, deterministic Starlark duration
  add; the op stays read-free). `bgcheckFreshnessWindow` is a named package
  constant: the production default `5m` (`freshness_window.go`), or a short window
  under `-tags leaseshortwindow` (`freshness_window_short.go`) so the e2e watches a
  lapse in bounded wall-clock. The replyOp is family-agnostic, so it stamps
  `validUntil` on **every** outcome; the value on a payment outcome is harmless and
  unused — the freshness rule lives in the lens cypher.
- The lens applies freshness to **bgcheck only**:
  `missing_bgcheck = NOT(a completed bgcheck whose window has not been recorded as
  lapsed)`. The lapse is a **fact on the check**, not a clock reading — the
  `freshnessExpiry` marker's `byTarget.backgroundCheckFreshness` entry, written by
  `MarkExpired` when that target's timer fires — so the predicate is
  `validUntil <> null AND NOT (marker >= validUntil)`. A **lapsed** bgcheck stops
  counting and the gap **re-opens** whenever the row is (re)evaluated: a stale
  background check IS a missing background check. **`missing_payment` is
  ever-completed** (a completed payment counts forever; its validUntil is ignored).
- The **negation** is what makes an unmarked check read fresh, and it is the opposite
  polarity from a gap test. `compareAny` answers false when either operand is nil, so
  a check no timer has fired on evaluates `null >= validUntil` = false and the
  negation counts it. The explicit `validUntil <> null` conjunct is the other half of
  that same nil-false: an outcome with no window at all would otherwise read
  permanently fresh.
- The freshness test lives **inside the count `CASE`** on the single
  `OPTIONAL MATCH (id)<-[:providedTo]-(inst:service)` fan-out — **one** providedTo
  match, **no** filtering `WHERE`. It binds every service neighbor and discriminates
  family + freshness inside the `CASE`, so a fully-filtered optional can never drop
  the anchor row. The `>=` on canonical-UTC RFC3339 strings is lexicographic =
  chronological.

**Eager auto-reopen-at-expiry — the `backgroundCheckFreshness` target.**
The timer lives on the **background-check instance**, because a fired `@at` marks the
row's own anchor and a check's freshness window belongs to the check. `backgroundCheckFreshness`
is a service-anchored convergence lens + target that projects one row per completed
check carrying `freshUntil = validUntil` while no lapse is recorded and `null` once one
is; the target declares **no gaps** and dispatches nothing — Weaver's row handler runs
the freshness bookkeeping leg on every delivery, before it reads any gap column, so an
empty playbook still gets a timer. When the `@at` fires, Weaver converts the firing into
`MarkExpired` on the instance, the marker records the instant under that target's key,
and — because the instance is a `providedTo` neighbour of the application —
`leaseApplicationComplete` reprojects and `missing_bgcheck` re-opens eagerly, not waiting
for an incidental CDC touch. `null`-once-lapsed is load-bearing: a past deadline
projected verbatim would re-arm an overdue `@at` on every delivery.

The same recorded fact is read by the two Postgres read models (through the shared
`readinessWithItems` fragment) and by `renewalComplete`'s `bgcheckValidUntil` aggregate.
None of those anchors could host the marker — they all reach the check across a
`providedTo` hop — which is exactly why the check hosts it itself.

The `bgcheckFreshnessWindow` is a **compile-time** constant baked into the replyOp DDL
script at package-init time (the value is interpolated into `leaseServiceReplyDDLScript`
by a package-level `var`, so it cannot be mutated at runtime). The production default
(`5m`) lives in `freshness_window.go`; the `test-lease-convergence` gate compiles the e2e
with `-tags leaseshortwindow` to substitute a short window (`freshness_window_short.go`)
it can watch lapse in bounded wall-clock.

The `TestLeaseApplicationComplete_PaymentInstanceNoBgcheck_NoDrop` rule-engine test guards
that the lens never drops the anchor in the payment-before-bgcheck window;
`TestBackgroundCheckFreshness_*` pin the instance lens's arming behaviour (deadline
projected when unlapsed, null once lapsed, re-armed when a later deadline outruns the
recorded instant, and untouched by another target's fire).

## Scalar convergence columns through the actorAggregate projection

The §10.2 convergence row carries **scalar** columns (`violating` / `missing_*`
bools, `entityKey` / `applicant` strings). The lens **cypher** produces them
correctly and is proven one-row-per-anchor at the rule-engine level. The
actorAggregate projection `EnvelopeFn` (`internal/refractor/projection/driver.go`)
projects each body column by the **shape** of its RETURN value: a list / `collect`
column is realness-filtered (the roster behavior — `my-tasks`,
`capabilityEphemeral`), and a **scalar** column projects **verbatim** so Weaver's
`boolColumn` reads a Go bool and the §10.8 `row.<col>` params resolve as strings
(Contract #6 §6.13 scalar-passthrough amendment, CAR E6). The 14.2 `keyColumn`
mechanism carries the bare-NanoID row key; together they make this convergence lens
projectable end-to-end through Refractor.

The lens declaration in this package is already pre-shaped for that path (keyColumn
set, scalar body columns named) and needs no change.

## Other notes

- **The type is lowercase `leaseapp`** — `leaseApp` (camelCase) is an invalid
  Contract #1 type segment. The epics / §10.2 / orchestration-base `vtx.leaseApp.*`
  strings are illustrative only. The `targetId` `leaseApplicationComplete` stays
  camelCase (it is a KV-key token, not a type segment).
- **Epics AC#3 is superseded.** The epics text ("each externalTask declares the
  replyOp's completion domain", advance-on-instanceOp-commit + deadline-as-backstop)
  was the first 13.1 ratification, corrected by the 13.6 follow-up. This package
  builds to the current Contract #10 §10.5/§10.6: `completionDomains: ["orchestration"]`,
  the replyOp emits `orchestration.externalTaskCompleted`, the deadline disarms on
  instanceOp commit.
- **The `.family` discriminator aspect.** The lens needs to bucket bgcheck vs
  payment. It cannot read the `.class` aspect via `inst.class.data.value` because the
  vertex envelope `class` field shadows the `.class` aspect on the projection read
  path. So the instanceOp writes a distinct `.family` aspect the lens reads as
  `inst.family.data.value` (the `.class` aspect is still written for 14.1 shape
  fidelity).
- **The bridge-driven e2e lives in `internal/leaseconvergence`** (the
  `test-lease-convergence` gate): it boots Processor + Refractor + Loom + Weaver + the
  live bridge in-process, installs the real chain, drives one lease application, and
  observes end-to-end convergence to a stable steady state, the FR58 at-most-once
  external effect, and D5 (outcome in aspect, root data minimal). The package's own
  `lens_cypher_test.go` proves the cypher at the rule-engine level with direct
  `.outcome` writes.
