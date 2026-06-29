package leasesigning

import (
	"fmt"

	"github.com/asolgan/lattice/internal/pkgmgr"
)

// Lenses returns the package's Lens declarations: the single
// `leaseApplicationComplete` actorAggregate convergence lens (Contract #10
// §10.2). It is anchored on the leaseapp candidate and reprojects on a change
// to any LINKED constituent (the applicant identity's aspects, a providedTo
// service instance's outcome aspect) — the actorAggregate adjacency
// reprojection, which a plain nats_kv projection would miss. It emits the
// bare-NanoID convergence key via 14.2's keyColumn so the row key stays
// <targetId>.<entityId> and Weaver's splitRowKey accepts it.
//
// The lens is ONE ROW PER ANCHOR (Contract #10 §10.2 + the chip-#2 guard
// guardOutputKeyCollision, which fails the projection closed on a multi-row
// anchor). The service-instance fan-out is collapsed inside the aggregator:
// each family's fresh-completed instances are counted with
// count(DISTINCT CASE WHEN <family + completed> THEN inst.key ELSE null END),
// so the OPTIONAL MATCH carries no filtering WHERE (a filtering WHERE that
// removes the only match collapses the upstream anchor to null in the grouped
// projection — the documented full-engine grouping behavior) and the row count
// stays exactly one per leaseapp even with several instances.
//
// Bucket: the shared primordial weaver-targets convergence bucket (§10.2).
//
// The §10.2 convergence row carries SCALAR columns (violating / missing_* bools,
// entityKey / applicant strings). The actorAggregate projection EnvelopeFn
// projects each body column by the shape of its RETURN value: a list / collect
// column is realness-filtered (the roster behavior — my-tasks /
// capabilityEphemeral), and a scalar column projects verbatim so Weaver's
// boolColumn reads a Go bool and the §10.8 row.<col> params resolve as strings
// (Contract #6 §6.13 scalar-passthrough amendment). With 14.2's keyColumn (the
// bare-NanoID row key) the row is Weaver-readable end-to-end.
func Lenses() []pkgmgr.LensSpec {
	return []pkgmgr.LensSpec{
		{
			CanonicalName:  "leaseApplicationComplete",
			Class:          "meta.lens",
			Adapter:        "nats-kv",
			Bucket:         "weaver-targets",
			Engine:         "full",
			Spec:           leaseApplicationCompleteSpec,
			ProjectionKind: "actorAggregate",
			Output: &pkgmgr.OutputDescriptorSpec{
				AnchorType:       "leaseapp",
				OutputKeyPattern: "leaseApplicationComplete.{actorSuffix}",
				BodyColumns:      []string{"violating", "missing_onboarding", "missing_bgcheck", "missing_payment", "missing_signature", "missing_listingLeased", "missing_decision", "applicantApproved", "landlordDecision", "landlordApproved", "landlordDeclined", "declineReason", "applicant", "entityKey", "freshUntil", "signedAt", "inflight_bgcheck", "inflight_payment", "declined_bgcheck", "declined_payment", "declined", "maxretries_bgcheck", "maxretries_payment", "unitKey", "unitAddress", "unitCity", "unitRegion", "unitRent", "unitCurrency", "unitBedrooms", "unitBathrooms", "unitLeaseTermMonths", "unitAvailableFrom", "unitStatus", "termsMoveInDate", "termsLeaseTermMonths", "termsRequestedRent", "profileSubmitted", "incomeToRentMet", "employmentVerified", "referenceCount", "hasCoApplicant", "hasGuarantor", "guarantorIncomeToRentMet"},
				EmptyBehavior:    "delete",
				KeyColumn:        "entityId",
				Freshness:        "auto",
			},
		},
		{
			// leaseApplicationsRead — the protected Postgres read model for the
			// applicant-facing "My Applications" view (D1.3 Fire 2, the
			// applicant-self milestone). Contract #6 §6.14: protected-by-default,
			// one authz_anchors set of bare-NanoID match tokens per row, RLS
			// returning only rows the reading actor is granted.
			//
			// This REPLACES the read-path leak loftspace-app's handleApplications
			// has today — it lists the §10.2 weaver-targets bucket via KVListKeys
			// and filters client-side on a forgeable `?applicant=` param (any
			// caller reads every application). Here RLS does the scoping in the
			// database: an actor's session sets lattice.actor_id (from a VERIFIED
			// JWT, Fire 3), and the set-membership policy returns only rows whose
			// authz_anchors intersect the actor's granted anchors.
			//
			// authz_anchors = [nanoIdFromKey(applicant identity key)] — the
			// applicant-self anchor. The shipped base cap-read.<actor> self-anchor
			// (D1.1) grants each applicant their own NanoID, so RLS matches
			// applicant=A's rows for A's session and nobody else's (the headline:
			// A sees only A's applications). The LANDLORD/residence audience (a
			// second anchor for a unit owner/manager) is a LATER increment: it
			// needs a cap-read.residence grant slice AND a landlord→unit ownership
			// link that loftspace-domain does not model yet, so the milestone
			// projects the single applicant anchor only.
			//
			// Adapter postgres + Protected: Refractor provisions the RLS table
			// (FORCE ROW LEVEL SECURITY + the policy) from Columns at activation
			// (Fire 1) and adds authz_anchors text[] + projection_seq. DSN is left
			// empty: Refractor resolves it from REFRACTOR_PG_DSN at activation, so
			// the package declares posture + columns, not a deployment connection
			// string. Plain (non-actorAggregate) projection: one row per leaseapp,
			// keyed by the application's bare NanoID; the convergence/gap state
			// stays in the leaseApplicationComplete actorAggregate lens above
			// (Weaver-internal §10.2 orchestration state) — this read model carries
			// the application's own identity + display scalars (unit, terms,
			// signature, landlord decision), the hops off the leaseapp and its
			// applicationFor identity / appliesToUnit unit.
			CanonicalName: "leaseApplicationsRead",
			Class:         "meta.lens",
			Adapter:       "postgres",
			Table:         "read_lease_applications",
			Engine:        "full",
			Spec:          leaseApplicationsReadSpec,
			Protected:     true,
			IntoKey:       []string{"app_id"},
			Columns: []pkgmgr.PostgresColumn{
				{Name: "entity_key", Type: "text"},
				{Name: "applicant", Type: "text"},
				{Name: "unit_key", Type: "text"},
				{Name: "unit_address", Type: "text"},
				{Name: "unit_city", Type: "text"},
				{Name: "unit_region", Type: "text"},
				{Name: "unit_rent", Type: "double precision"},
				{Name: "unit_currency", Type: "text"},
				{Name: "unit_status", Type: "text"},
				{Name: "signed_at", Type: "text"},
				{Name: "landlord_decision", Type: "text"},
				{Name: "decline_reason", Type: "text"},
				{Name: "terms_move_in_date", Type: "text"},
				{Name: "terms_lease_term_months", Type: "double precision"},
				{Name: "terms_requested_rent", Type: "double precision"},
			},
		},
	}
}

// leaseApplicationCompleteSpec is the one-row-per-anchor convergence cypher.
//
// It anchors on the leaseapp candidate (a required MATCH), OPTIONAL-walks the
// applicationFor link to the applicant identity, OPTIONAL-walks the appliesToUnit
// link to the leased unit, and OPTIONAL-walks the applicant's providedTo service
// instances. Each gap is a per-anchor scalar:
//
//   - missing_onboarding — the applicant has not recorded PII (no .ssn aspect).
//     RecordIdentityPII (the onboarding pattern's userTask) writes .ssn/.dob,
//     flipping this false.
//   - missing_bgcheck / missing_payment — keyed on a completed service instance
//     of that family providedTo the applicant. The family is discriminated by the
//     instance's .family aspect (read as a distinct aspect because the vertex
//     envelope `class` field shadows the .class aspect on the read path); the
//     completed test reads the .outcome aspect status. The replyOp writing the
//     .outcome aspect flips the matching gap false. bgcheck additionally requires
//     freshness (see FRESHNESS below); payment is ever-completed.
//   - missing_signature — the application has no .signature aspect. SignLease
//     writes it, flipping this false.
//
// violating is the explicit OR of the four applicant gaps PLUS missing_decision
// PLUS missing_listingLeased (Contract #10 §10.2: violating is lens-projected, not
// an implicit OR; for this target the rule is "any applicant gap OR a
// qualified-but-undecided application OR a landlord-approved-but-unleased unit →
// violating"). Folding missing_listingLeased into violating is load-bearing:
// Weaver skips all dispatch when violating=false, so the listing-flip directOp
// only fires while the row is violating. missing_decision keeps a
// qualified-but-undecided application explicitly open (its work is not done until
// the landlord decides) WITHOUT dispatching anything — it maps to no playbook
// entry, so the row stays violating while no remediation fires. A landlord-DECLINED
// application is terminal-not-violating: every violating term is false (the
// applicant gaps are closed, missing_decision is false because the decision is
// non-null, and missing_listingLeased is false because the decision is not
// 'approved'), so Weaver stops reconciling it — there is no work left to do (the
// FE reads the declined column for the terminal disposition).
//
// unitKey / unitAddress / unitRent / unitStatus are columns carried from the
// appliesToUnit walk (the unit's key, its .address.line1, its .listing.rentAmount
// + .listing.status — aspect-hops off the live node, read inside the aggregating
// WITH so they survive the grouping). They answer "applying to lease Unit X at
// $Y/mo" for the operator / applicant FE; unitStatus drives the listing-leased
// convergence below. The richer informational set — unitCity / unitRegion (the
// rest of .address), unitCurrency / unitBedrooms / unitBathrooms /
// unitLeaseTermMonths / unitAvailableFrom (the rest of the listing economics),
// and termsMoveInDate / termsLeaseTermMonths / termsRequestedRent (the
// applicant's own requested .terms, written only when moveInDate was supplied at
// CreateLeaseApplication) — projects the full "lease terms you are agreeing to"
// so the applicant FE can render a terms-review panel before signing. These are
// pure read-only scalar projections: none feeds violating / a gap predicate, so
// the convergence logic is untouched (a null .terms simply projects null terms
// columns). `unit` is required at CreateLeaseApplication, so there is no
// missing_unit gap (§3 D5). appliesToUnit is 0..1, so these stay scalar and
// one-row-per-anchor holds.
//
// signedAt (the .signature aspect's signedAt) is projected as a read-only scalar
// alongside missing_signature: it carries the execution date the applicant FE
// stamps onto the produced signed-lease artifact (the deterministic, idempotently
// attached executed-lease document) and renders as "Signed on <date>". Like the
// terms columns it feeds no gap predicate (missing_signature already derives from
// signedAt = null) — a null projects null.
//
// APPLICANT QUALIFICATION PROFILE — the derived signals the landlord decides on.
//
//   - profileSubmitted (bool) — whether the applicant has recorded a .profile aspect
//     (submittedAt <> null). incomeToRentMet / employmentVerified / referenceCount /
//     hasCoApplicant / hasGuarantor / guarantorIncomeToRentMet are the DERIVED
//     qualification signals SetApplicantProfile computes (the engine has no arithmetic
//     / len, so the op derives them) and stores; the lens projects them verbatim. The
//     RAW financials (annualIncome, employerName, the reference strings, and the
//     guarantor / co-applicant detail — names, contacts, the guarantor's raw income)
//     are deliberately NOT projected — they live in the Core-KV .profile aspect
//     plaintext-for-now (the .ssn / .demographics discipline) and the deferred Vault
//     plane owns their encryption + a raw-financial display. So a landlord reads
//     "income meets 3× rent / employed / N references / guarantor covers 3× rent"
//     without the raw figures. All feed no gap predicate (capture + surface, not a
//     convergence gate) — a null .profile projects null signal columns, leaving
//     convergence untouched.
//
// LANDLORD-GATED LISTING-LEASED CONVERGENCE — the human decision gates the lease.
//
//   - landlordDecision (string, informational) — the raw .decision aspect value
//     DecideLeaseApplication writes ('approved' | 'declined' | null). landlordApproved
//     ≡ (landlordDecision = 'approved'), landlordDeclined ≡ (landlordDecision =
//     'declined'). The FE renders the disposition off these.
//   - declineReason (string, informational) — the optional free-text rationale a
//     landlord supplies with a decline (the raw .decision.reason aspect value; null
//     for an approve or a reasonless decline). The applicant FE surfaces it on the
//     declined banner ("Application declined: <reason>") so a decline carries
//     feedback rather than a bare rejection. Only meaningful when landlordDeclined.
//   - applicantApproved (informational bool) is true once all four APPLICANT gaps
//     are closed (ssn recorded, a fresh bgcheck, a completed payment, a signature)
//     — De Morgan of the four missing_* (the engine has no RETURN-alias
//     cross-reference, so it re-derives from the WITH values, like violating). Its
//     meaning is "qualified, pending the landlord decision" — readiness, not the
//     leasing decision. The applicant FE moves its "complete" signal to
//     landlordApproved (+ leased), reading "qualified — awaiting landlord review"
//     while applicantApproved holds with no decision yet.
//   - missing_decision opens when an application is qualified (all four applicant
//     gaps closed) AND the landlord has not decided (landlordDecision = null). It is
//     the explicit "qualified, awaiting landlord decision" state. It maps to NO
//     playbook entry (no externalTask/userTask/directOp), so it keeps the row
//     violating without dispatching anything, and closes the moment the landlord
//     decides (approve or decline). It closes the race the auto-flip-on-readiness
//     had: nothing leases until a human approves.
//   - missing_listingLeased requires BOTH applicant-readiness AND the landlord's
//     approval — a unit leases only when the applicant is qualified (all four
//     applicant gaps closed) AND the landlord has approved. It opens when a qualified,
//     landlord-APPROVED application's unit exists, has a listing, and is not yet leased
//     ((unitKey <> null) AND (the four applicant conjuncts) AND (landlordDecision =
//     'approved') AND (unitStatus <> null) AND (unitStatus <> 'leased')). Keeping the
//     four applicant conjuncts is load-bearing safety: a landlord who approves before
//     the applicant qualifies — OR a bgcheck that goes STALE after approval but before
//     the flip fires — must NOT lease the unit to an unqualified applicant. The
//     freshness predicate re-opens missing_bgcheck on a stale check, which drops
//     freshBgComplete to 0 and so closes missing_listingLeased until a fresh bgcheck
//     restores readiness. The (unitStatus <> null) term requires a listing to exist (a
//     unit with none is not transitionable — SetListingStatus would reject NoListing),
//     closing the dispatch-thrash hazard. Weaver dispatches directOp(SetListingStatus
//     status=leased) (§10.8 playbook); the op flips the unit's .listing.status, the
//     unit (an appliesToUnit neighbor) reprojects this anchor, unitStatus becomes
//     'leased', and the gap closes. A landlord-declined, undecided, or
//     not-yet-qualified application never opens this gap. A multi-applicant race
//     self-resolves: the first qualified+landlord-approved application to converge
//     leases the unit, then every other application's (unitStatus <> 'leased') is
//     false → no re-dispatch, no double-transition (a landlord approving two
//     applicants for one unit is absorbed by the unit-lease idempotency).
//
// applicant + entityKey are the param columns the §10.8 playbook templates name
// (row.applicant, row.entityKey). They stay non-null even when gaps are open
// because the single providedTo OPTIONAL MATCH carries NO filtering WHERE: it
// binds every service neighbor and the family/freshness discrimination happens
// inside the count CASE, so no row is ever dropped to null by a fully-filtered
// optional.
//
// FRESHNESS (the freshness PREDICATE — bgcheck-only; payment ever-completed).
//
//   - missing_bgcheck counts a completed bgcheck toward convergence ONLY while
//     its op-stamped validUntil is still in the future
//     (inst.outcome.data.validUntil > $now). A STALE bgcheck (validUntil ≤ $now)
//     stops counting and missing_bgcheck re-opens whenever the row is
//     (re)evaluated — a stale background check IS a missing background check. The
//     freshness test lives inside the count CASE on the single providedTo fan
//     (no second match, no WHERE), so it cannot drop the anchor. validUntil is
//     computed by the replyOp as completedAt + bgcheckFreshnessWindow (Starlark
//     time.rfc3339_add — no clock read), the §10.2 "the freshness rule lives in
//     the cypher" convention. The `>` on these canonical-UTC RFC3339 strings is
//     lexicographic = chronological (ruleengine/full executor.go compareAny
//     string branch); $now is the projection-supplied param (Refractor's
//     executeFullForActor sets params["now"] = time.Now().UTC().Format(time.RFC3339)).
//   - missing_payment is ever-completed: a completed payment counts forever,
//     validUntil ignored.
//
// EAGER auto-reopen-at-expiry — the §10.2 freshUntil column.
//
//   - The lens projects a single scalar freshUntil per anchor: the LATEST
//     validUntil among the applicant's completed, still-fresh bgchecks. Weaver's
//     temporal lane reads it (freshUntilColumn) and schedules an @at one-shot at
//     that instant; when the timer fires it marks the row expired, the row
//     reprojects, and the freshness predicate re-opens missing_bgcheck the moment
//     freshness lapses — eagerly, not waiting for an incidental CDC touch.
//   - freshUntil is a max() aggregator on the SAME single no-WHERE providedTo fan
//     that drives the missing_* counts — max(validUntil) over the completed-fresh
//     bgcheck CASE, folded inside the aggregation WITH. So it is aggregated, not
//     re-expanded: an applicant with N completed-fresh bgchecks (multiple
//     applications on one identity, or accumulated freshness re-dispatches —
//     providedTo is on the identity, not the application) yields exactly one row,
//     not N (guardOutputKeyCollision stays satisfied — no separate, unaggregated
//     match to multiply the anchor). When no fresh bgcheck exists every CASE is
//     null and max() folds to null, so freshUntil projects as a genuine null
//     (Weaver clears any standing @at — no deadline to arm) and the anchor never
//     drops. Picking the LATEST (max, not min/first) is required: the @at re-open
//     timer must not fire while a later-expiring fresh bgcheck still counts toward
//     missing_bgcheck. max() over canonical-UTC RFC3339 strings is lexicographic =
//     chronological (ruleengine/full executor.go reduceExtreme → compareAny).
//
// DISPATCH SUPPRESSION — the per-gap inflight_<g> companion + maxretries_<g> cap.
//
//	inflight_<g> is a §10.2 BodyColumn Weaver reads as a dispatch-suppression
//	companion of the gap missing_<g> (the prefix-swap convention, like freshUntil):
//	while it is true Weaver does NOT (re-)dispatch the externalTask, but the gap
//	stays missing_<g>=true / violating — only re-dispatch is suppressed. It is
//	counted on the SAME single no-WHERE providedTo fan as the missing_* counts, so
//	it adds no filtered optional that could drop the anchor.
//
//	- inflight_<g> — a call of that family is legitimately in flight: a service
//	  instance with a .dispatch marker present (inst.dispatch.data.vendorRef <>
//	  null — the bridge wrote .dispatch on a Pending Execute, and vendorRef is true
//	  iff the .dispatch aspect exists) and NO .outcome yet (status = null — the
//	  create-only outcome has not landed). The predicate is presence-based, not
//	  deadline-bounded: an in-flight call is one whose dispatch landed and whose
//	  outcome has not, regardless of its give-up horizon. A dead/slow bridge that
//	  never posts the timeout outcome therefore keeps inflight_<g>=true rather than
//	  flipping it false at the deadline — closing the double-dispatch window where
//	  Weaver would re-call the vendor while the original call is still pending.
//	  Re-dispatch resumes only when the call resolves: a failed outcome lands
//	  (status != null) → inflight_<g> false → Weaver dispatches a fresh call
//	  (a new claim vertex / vendorRef — never a silent resubmit of the same one).
//	- maxretries_<g> — the per-gap retry cap, a CONSTANT integer column baked from
//	  retry_budget.go (maxBgcheckRetries / maxPaymentRetries) onto every row. The
//	  budget itself is NOT a lens predicate (a lifetime failed-count never resets on
//	  success): Weaver keeps a per-(target, entity, gap) dispatch-count in
//	  weaver-state, reads this cap off the row, and stops auto-dispatching once the
//	  count reaches it — the operator-visible "needs human escalation" terminal. The
//	  count is deleted when the gap closes, so a later renewal starts a fresh budget.
//	  Keeping the cap a package-owned column (like freshUntil) leaves the policy in
//	  the package with no contract change.
//	  The two HUMAN userTask gaps (onboarding, signature) need NO maxretries_<g>
//	  cap: duplicate userTask dispatch is now prevented at the source by the §10.3
//	  GENERAL fix — Weaver derives the userTask's identity from the mark's stable
//	  per-open-episode claimId (assignTask's taskId / triggerLoom's Loom
//	  instanceId), so a mark-lease reclaim re-dispatches the SAME id and the
//	  Processor/Loom collapses it on the existing task/instance (the CreateTask
//	  kv.Read no-op + CreateOnly). This SUPERSEDES the interim create-once cap
//	  (maxretries_onboarding/_signature = 1) that previously held the line — that
//	  cap was create-once-FOREVER (never re-created a lost task) and per-package;
//	  the general fix is reopen-correct, self-healing, and general.
//
// DECLINED DISPOSITION — the per-family declined_<g> column + the top-level declined.
//
//	A FAILED outcome (inst.outcome.data.status = 'failed' — a definitive business
//	rejection, distinct from a transient error) keeps the gap missing_<g> open the
//	same as a never-run check, so without a dedicated column a declined application
//	is indistinguishable from one still "in progress" — it reads as blocked
//	forever. declined_<g> is the honest terminal disposition the operator / applicant
//	FE renders instead:
//
//	- declined_bgcheck — a failed bgcheck instance exists AND no completed-fresh
//	  bgcheck supersedes it ((bgFailed > 0) AND (freshBgComplete = 0)). A later
//	  retry that clears (Weaver re-dispatches a FRESH instance on a failed outcome,
//	  see inflight_<g>) flips declined_bgcheck back to false — the disposition
//	  tracks the CURRENT verdict, not a historical one.
//	- declined_payment — symmetric on the payment family ((payFailed > 0) AND
//	  (payComplete = 0)); payment is ever-completed so no freshness term.
//	- declined — the OR of declined_bgcheck, declined_payment, AND a landlord
//	  decline (landlordDecision = 'declined'): the application carries at least one
//	  standing rejection — a failed verification OR a landlord's explicit decline. It
//	  is a presentation column the FE renders the terminal "declined" banner from,
//	  like freshUntil / unitAddress. A verification-declined application keeps an
//	  applicant gap open so it is also violating (Weaver keeps reconciling — a retry
//	  may clear); a LANDLORD-declined application is terminal-not-violating (its
//	  applicant gaps are closed and the decision is non-null, so missing_decision and
//	  missing_listingLeased are both false) — declined here means "done, terminally
//	  rejected," no work remains. The lens cannot see Weaver's per-gap dispatch count,
//	  so a verification declined is "a rejection stands right now," not "retries are
//	  terminally exhausted"; while a retry is in flight inflight_<g> is true and the
//	  FE prefers that ("re-checking") over the standing-rejection read.
//
// '= null' (not IS NULL) is the full engine's null test (ruleengine/full
// executor.go equalsAny treats null = null as true and any value = null as
// false). Do not "correct" it to unsupported IS NULL.
//
// leaseApplicationCompleteSpec is built once at package init: the retry caps
// (maxBgcheckRetries / maxPaymentRetries) bake into the constant maxretries_<g>
// columns Weaver bounds its dispatch-count against, the §10.2 "the policy lives in
// the cypher" convention (same posture as bgcheckFreshnessWindow). The cypher
// carries no literal '%'.
var leaseApplicationCompleteSpec = fmt.Sprintf(`
MATCH (app:leaseapp {key: $actorKey})
OPTIONAL MATCH (app)-[:applicationFor]->(id:identity)
OPTIONAL MATCH (app)-[:appliesToUnit]->(u:unit)
OPTIONAL MATCH (id)<-[:providedTo]-(inst:service)
WITH
  app.key AS entityKey,
  id.key  AS applicant,
  app.signature.data.signedAt AS signedAt,
  app.decision.data.value AS landlordDecision,
  app.decision.data.reason AS declineReason,
  id.ssn.data.value AS ssnVal,
  u.key                     AS unitKey,
  u.address.data.line1      AS unitAddress,
  u.address.data.city       AS unitCity,
  u.address.data.region     AS unitRegion,
  u.listing.data.rentAmount AS unitRent,
  u.listing.data.rentCurrency AS unitCurrency,
  u.listing.data.bedrooms   AS unitBedrooms,
  u.listing.data.bathrooms  AS unitBathrooms,
  u.listing.data.leaseTermMonths AS unitLeaseTermMonths,
  u.listing.data.availableFrom AS unitAvailableFrom,
  u.listing.data.status     AS unitStatus,
  app.terms.data.moveInDate AS termsMoveInDate,
  app.terms.data.leaseTermMonths AS termsLeaseTermMonths,
  app.terms.data.requestedRent AS termsRequestedRent,
  app.profile.data.submittedAt AS profileSubmittedAt,
  app.profile.data.incomeToRentMet AS incomeToRentMet,
  app.profile.data.employmentVerified AS employmentVerified,
  app.profile.data.referenceCount AS referenceCount,
  app.profile.data.hasCoApplicant AS hasCoApplicant,
  app.profile.data.hasGuarantor AS hasGuarantor,
  app.profile.data.guarantorIncomeToRentMet AS guarantorIncomeToRentMet,
  count(DISTINCT CASE WHEN inst.family.data.value = 'backgroundCheck' AND inst.outcome.data.status = 'completed' AND inst.outcome.data.validUntil > $now THEN inst.key ELSE null END) AS freshBgComplete,
  count(DISTINCT CASE WHEN inst.family.data.value = 'payment' AND inst.outcome.data.status = 'completed' THEN inst.key ELSE null END) AS payComplete,
  count(DISTINCT CASE WHEN inst.family.data.value = 'backgroundCheck' AND inst.dispatch.data.vendorRef <> null AND inst.outcome.data.status = null THEN inst.key ELSE null END) AS bgInflight,
  count(DISTINCT CASE WHEN inst.family.data.value = 'payment' AND inst.dispatch.data.vendorRef <> null AND inst.outcome.data.status = null THEN inst.key ELSE null END) AS payInflight,
  count(DISTINCT CASE WHEN inst.family.data.value = 'backgroundCheck' AND inst.outcome.data.status = 'failed' THEN inst.key ELSE null END) AS bgFailed,
  count(DISTINCT CASE WHEN inst.family.data.value = 'payment' AND inst.outcome.data.status = 'failed' THEN inst.key ELSE null END) AS payFailed,
  max(CASE WHEN inst.family.data.value = 'backgroundCheck' AND inst.outcome.data.status = 'completed' AND inst.outcome.data.validUntil > $now THEN inst.outcome.data.validUntil ELSE null END) AS freshUntil
RETURN
  entityKey AS actorKey,
  entityKey,
  applicant,
  unitKey,
  unitAddress,
  unitCity,
  unitRegion,
  unitRent,
  unitCurrency,
  unitBedrooms,
  unitBathrooms,
  unitLeaseTermMonths,
  unitAvailableFrom,
  unitStatus,
  termsMoveInDate,
  termsLeaseTermMonths,
  termsRequestedRent,
  incomeToRentMet,
  employmentVerified,
  referenceCount,
  hasCoApplicant,
  hasGuarantor,
  guarantorIncomeToRentMet,
  (profileSubmittedAt <> null) AS profileSubmitted,
  freshUntil,
  signedAt,
  landlordDecision,
  declineReason,
  (ssnVal = null)        AS missing_onboarding,
  (freshBgComplete = 0)  AS missing_bgcheck,
  (payComplete = 0)      AS missing_payment,
  (signedAt = null)      AS missing_signature,
  (bgInflight > 0)       AS inflight_bgcheck,
  (payInflight > 0)      AS inflight_payment,
  ((bgFailed > 0) AND (freshBgComplete = 0))  AS declined_bgcheck,
  ((payFailed > 0) AND (payComplete = 0))     AS declined_payment,
  (((bgFailed > 0) AND (freshBgComplete = 0)) OR ((payFailed > 0) AND (payComplete = 0)) OR (landlordDecision = 'declined')) AS declined,
  (landlordDecision = 'approved') AS landlordApproved,
  (landlordDecision = 'declined') AS landlordDeclined,
  ((ssnVal <> null) AND (freshBgComplete > 0) AND (payComplete > 0) AND (signedAt <> null)) AS applicantApproved,
  ((ssnVal <> null) AND (freshBgComplete > 0) AND (payComplete > 0) AND (signedAt <> null) AND (landlordDecision = null)) AS missing_decision,
  ((unitKey <> null) AND (ssnVal <> null) AND (freshBgComplete > 0) AND (payComplete > 0) AND (signedAt <> null) AND (landlordDecision = 'approved') AND (unitStatus <> null) AND (unitStatus <> 'leased')) AS missing_listingLeased,
  %d                     AS maxretries_bgcheck,
  %d                     AS maxretries_payment,
  ((ssnVal = null) OR (freshBgComplete = 0) OR (payComplete = 0) OR (signedAt = null) OR ((ssnVal <> null) AND (freshBgComplete > 0) AND (payComplete > 0) AND (signedAt <> null) AND (landlordDecision = null)) OR ((unitKey <> null) AND (ssnVal <> null) AND (freshBgComplete > 0) AND (payComplete > 0) AND (signedAt <> null) AND (landlordDecision = 'approved') AND (unitStatus <> null) AND (unitStatus <> 'leased'))) AS violating
`, maxBgcheckRetries, maxPaymentRetries)

// leaseApplicationsReadSpec is the protected Postgres read model's cypher (D1.3
// Fire 2). A plain one-row-per-leaseapp projection: it anchors on every leaseapp,
// OPTIONAL-walks the applicationFor link to the applicant identity and the
// appliesToUnit link to the leased unit, and projects the application's own
// identity + display scalars plus the §6.14 authz_anchors set.
//
//   - app_id (the IntoKey) is the application's bare NanoID (nanoIdFromKey on the
//     full leaseapp key), the §6.14 bare-NanoID convention; entity_key keeps the
//     full vtx.leaseapp.<id> key as a body column for the FE.
//   - applicant is the applicant identity's full key (a display/scope value);
//     unit_* / terms_* / signed_at / landlord_decision / decline_reason are the
//     pure scalar hops off the unit's .address/.listing, the applicant's own
//     requested .terms, and the application's .signature/.decision aspects — the
//     same display columns leaseApplicationComplete projects, minus the
//     service-instance convergence aggregate (that stays Weaver-internal §10.2
//     state; D1.5 may roll the gap state onto a protected model later).
//   - authz_anchors = [nanoIdFromKey(id.key)] — the applicant-self anchor only
//     (the milestone). applicationFor is a REQUIRED MATCH (not OPTIONAL): a
//     leaseapp with no applicant link projects NO row, so the read model holds
//     only well-formed applications and every row's authz_anchors carries exactly
//     one real applicant NanoID — never a null/empty set the adapter would choke
//     on, and never a row no anchor protects. (A leaseapp is always minted WITH
//     its applicationFor link at CreateLeaseApplication, so this excludes only a
//     transient pre-link window or a malformed shell — both of which correctly
//     stay out of the read model until they are well-formed.) The
//     landlord/residence anchor is a later increment (needs cap-read.residence +
//     a landlord→unit ownership link loftspace-domain does not model yet).
//
// '= null' / '<> null' is the full engine's null test (not IS NULL); list
// literals + nanoIdFromKey in RETURN are the cap-read base lens's proven shape.
const leaseApplicationsReadSpec = `
MATCH (app:leaseapp)
MATCH (app)-[:applicationFor]->(id:identity)
OPTIONAL MATCH (app)-[:appliesToUnit]->(u:unit)
RETURN
  nanoIdFromKey(app.key)         AS app_id,
  app.key                        AS entity_key,
  id.key                         AS applicant,
  u.key                          AS unit_key,
  u.address.data.line1           AS unit_address,
  u.address.data.city            AS unit_city,
  u.address.data.region          AS unit_region,
  u.listing.data.rentAmount      AS unit_rent,
  u.listing.data.rentCurrency    AS unit_currency,
  u.listing.data.status          AS unit_status,
  app.signature.data.signedAt    AS signed_at,
  app.decision.data.value        AS landlord_decision,
  app.decision.data.reason       AS decline_reason,
  app.terms.data.moveInDate      AS terms_move_in_date,
  app.terms.data.leaseTermMonths AS terms_lease_term_months,
  app.terms.data.requestedRent   AS terms_requested_rent,
  [nanoIdFromKey(id.key)]        AS authz_anchors
`
