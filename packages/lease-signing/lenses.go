package leasesigning

import (
	"fmt"

	"github.com/operatinggraph/lattice/internal/pkgmgr"
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
	lenses := []pkgmgr.LensSpec{
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
				BodyColumns:      []string{"violating", "missing_onboarding", "missing_bgcheck", "missing_payment", "missing_signature", "missing_listingLeased", "missing_decision", "missing_manager", "missing_lossRecorded", "missing_leaseDoc", "missing_leaseDocAttach", "missing_residence", "applicantApproved", "landlordDecision", "landlordApproved", "landlordDeclined", "declineReason", "applicant", "entityKey", "signedAt", "inflight_bgcheck", "inflight_payment", "inflight_docGen", "inflight_onboarding", "inflight_signature", "declined_bgcheck", "declined_payment", "declined_docGen", "declined", "maxretries_bgcheck", "maxretries_payment", "unitKey", "unitAddress", "unitCity", "unitRegion", "unitRent", "unitCurrency", "unitBedrooms", "unitBathrooms", "unitLeaseTermMonths", "unitAvailableFrom", "unitStatus", "tenancyEndedAt", "termsMoveInDate", "termsLeaseTermMonths", "termsRequestedRent", "profileSubmitted", "incomeToRentMet", "employmentVerified", "referenceCount", "hasCoApplicant", "hasGuarantor", "guarantorIncomeToRentMet", "docStoreName", "docFilename", "docContentType", "docDigest", "docSize", "leaseDocAttached"},
				EmptyBehavior:    "delete",
				KeyColumn:        "entityId",
				Freshness:        "auto",
			},
		},
		{
			// applicantOnboarding — the IDENTITY-anchored companion convergence
			// target for the one piece of work in this package that is a property
			// of the PERSON rather than of an application: recording PII.
			//
			// leaseApplicationComplete anchors on the leaseapp, so an applicant
			// holding N applications projects N rows. Every other gap on that
			// target closes per-application (a signature, this application's
			// document, this unit's listing flip), but onboarding closes ONCE for
			// the person — `ssnVal` is the APPLICANT's own `.ssn` aspect, read
			// across the applicationFor hop. Dispatching from there mints one Loom
			// instance per row (the artifact id is derived from the row's own
			// entityId), so one applicant with four applications receives four
			// identical RecordIdentityPII cards. This lens moves the dispatch to
			// the granularity the work actually has: ONE ROW PER APPLICANT, so one
			// applicant is one gap is one task by construction, with no dedup
			// mechanism anywhere. leaseApplicationComplete keeps projecting
			// missing_onboarding — the application is still blocked on it, and the
			// applicant FE's stepper still reads the column — but declares it
			// `surface` there, dispatching nothing (targets.go).
			//
			// Same shared weaver-targets bucket as every other target this package
			// declares; the rows are namespaced by OutputKeyPattern, and the
			// targetId IS that prefix (the §10.2↔§10.8 binding). The anchor is the
			// identity, so the row key is the applicant's bare NanoID.
			CanonicalName:  "applicantOnboarding",
			Class:          "meta.lens",
			Adapter:        "nats-kv",
			Bucket:         "weaver-targets",
			Engine:         "full",
			Spec:           applicantOnboardingSpec,
			ProjectionKind: "actorAggregate",
			Output: &pkgmgr.OutputDescriptorSpec{
				AnchorType:       "identity",
				OutputKeyPattern: "applicantOnboarding.{actorSuffix}",
				BodyColumns:      []string{"violating", "missing_onboarding", "inflight_onboarding", "applicant", "entityKey"},
				EmptyBehavior:    "delete",
				KeyColumn:        "entityId",
				Freshness:        "auto",
			},
		},
		{
			// staleUserTasks — the TASK-anchored companion to
			// orchestration-base's orphanedTaskGrants: that sweep catches a task
			// whose granted OPERATION died out from under it; this one catches a
			// task whose own GAP already closed through some other route while the
			// task — still correctly granted, still perfectly actionable — sits
			// open forever, because nothing in Weaver itself ever revokes a
			// dispatched assignTask once its effect holds true
			// (internal/weaver's releaseCompletedLeg only deletes its own mark,
			// never touches the task artifact — Weaver's whole per-target state is
			// the mark, not the task). Each of this package's three userTask ops
			// closes on a write the ASSIGNEE normally makes through that very op,
			// but an applicant can hold several live applications, a landlord
			// several open renewals, and a mark-lease reclaim can strand a task
			// whose person-level fact (an .ssn write from a different
			// application's flow) was already satisfied elsewhere — leaving a
			// live task in an inbox for work already done.
			//
			// One row per open task; missing_cancellation is true exactly when
			// the task's own forOperation names one of the three ops below AND
			// the fact that op exists to produce is already present on the
			// task's scopedTo neighbor. Reuses orphanedTaskGrants' own directOp
			// CancelTask{taskKey} verbatim (targets.go) — no new op, no new
			// permission: CancelTask is already operator-granted platform-wide.
			CanonicalName:  "staleUserTasks",
			Class:          "meta.lens",
			Adapter:        "nats-kv",
			Bucket:         "weaver-targets",
			Engine:         "full",
			Spec:           staleUserTasksSpec,
			ProjectionKind: "actorAggregate",
			Output: &pkgmgr.OutputDescriptorSpec{
				AnchorType:       "task",
				OutputKeyPattern: "staleUserTasks.{actorSuffix}",
				BodyColumns:      []string{"violating", "missing_cancellation", "entityKey", "taskKey"},
				EmptyBehavior:    "delete",
				KeyColumn:        "entityId",
			},
		},
		{
			// backgroundCheckFreshness — the SERVICE-INSTANCE-anchored freshness
			// window. It projects no gap column and its target dispatches
			// nothing; what it exists for is the @at Weaver arms from freshUntil,
			// because a fired timer marks the row's own anchor and a background
			// check's window belongs to the check. The four readers of that
			// window in this package (readinessWithItems' freshBgComplete, spliced
			// into three lenses, plus renewalComplete's bgcheckValidUntil) all
			// reach the instance across a providedTo hop from a different anchor,
			// so they read the recorded lapse rather than hosting it.
			//
			// Same shared weaver-targets bucket as every other target here, rows
			// namespaced by OutputKeyPattern, with the targetId as that prefix.
			CanonicalName:  "backgroundCheckFreshness",
			Class:          "meta.lens",
			Adapter:        "nats-kv",
			Bucket:         "weaver-targets",
			Engine:         "full",
			Spec:           backgroundCheckFreshnessSpec,
			ProjectionKind: "actorAggregate",
			Output: &pkgmgr.OutputDescriptorSpec{
				AnchorType:       "service",
				OutputKeyPattern: "backgroundCheckFreshness.{actorSuffix}",
				BodyColumns:      []string{"violating", "entityKey", "freshUntil"},
				EmptyBehavior:    "delete",
				KeyColumn:        "entityId",
				Freshness:        "auto",
			},
		},
		{
			// supersededBackgroundChecks — the SERVICE-INSTANCE-anchored
			// convergence rule that retires a completed background check once a
			// later completed check on the same applicant, minted by this same
			// package, exists. See supersededBackgroundChecksSpec below for the
			// full projection contract; the row is violating-rows-only (an
			// anchor with no qualifying successor projects no row at all), so
			// the live population this rule bounds — checks that have already
			// been superseded — never carries a standing subject in
			// weaver-targets.
			//
			// Reprojection cost under the acting anchor derivation
			// (REFRACTOR_ANCHOR_DERIVATION=act, the built-in default): a service
			// or identity write reprojects the anchor and, through the providedTo
			// hops, the OTHER live instances of that one applicant (steady state
			// one to two — the population this rule keeps small). The successor
			// position reaches no shared meta position, so no completion fans out
			// past its own applicant; the only pass over the whole live owned
			// population is a package install rewriting this DDL's meta aspects
			// (spec comment below, design §4.1 (ii)/§11.1). Under `off`/`shadow`
			// or a declined derivation the ActorEnumerator BFS answers instead,
			// and it scopes by relation NAME only — instanceOf is in scope from
			// the anchor, so it crosses the meta to every owned instance.
			//
			// Same shared weaver-targets bucket as every other target here, rows
			// namespaced by OutputKeyPattern, with the targetId as that prefix.
			CanonicalName:  "supersededBackgroundChecks",
			Class:          "meta.lens",
			Adapter:        "nats-kv",
			Bucket:         "weaver-targets",
			Engine:         "full",
			Spec:           supersededBackgroundChecksSpec,
			ProjectionKind: "actorAggregate",
			Output: &pkgmgr.OutputDescriptorSpec{
				AnchorType:       "service",
				OutputKeyPattern: "supersededBackgroundChecks.{actorSuffix}",
				BodyColumns:      []string{"violating", "missing_retirement", "entityKey", "subjectKey", "supersededBy", "instanceOfLink"},
				EmptyBehavior:    "delete",
				KeyColumn:        "entityId",
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
			// applicationFor identity / appliesToUnit unit, plus the ANCHORED
			// executed-lease artifact's pointers (doc_store_name / doc_filename /
			// doc_content_type — projected only once the signedLease attachment
			// exists), which cmd/loftspace-app's GET /api/lease-document uses to
			// stream the document bytes under RLS.
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
				{Name: "unit_bedrooms", Type: "double precision"},
				{Name: "unit_bathrooms", Type: "double precision"},
				{Name: "unit_available_from", Type: "text"},
				{Name: "unit_lease_term_months", Type: "double precision"},
				{Name: "signed_at", Type: "text"},
				{Name: "landlord_decision", Type: "text"},
				{Name: "decline_reason", Type: "text"},
				{Name: "terms_move_in_date", Type: "text"},
				{Name: "terms_lease_term_months", Type: "double precision"},
				{Name: "terms_requested_rent", Type: "double precision"},
				{Name: "tenancy_lease_start", Type: "text"},
				{Name: "tenancy_lease_end", Type: "text"},
				{Name: "tenancy_term_start", Type: "text"},
				{Name: "tenancy_rent_amount", Type: "double precision"},
				{Name: "deposit_amount", Type: "double precision"},
				{Name: "tenancy_ended_at", Type: "text"},
				{Name: "notice_move_out_at", Type: "text"},
				{Name: "notice_given_at", Type: "text"},
				{Name: "notice_given_by", Type: "text"},
				{Name: "doc_store_name", Type: "text"},
				{Name: "doc_filename", Type: "text"},
				{Name: "doc_content_type", Type: "text"},
				{Name: "profile_submitted", Type: "boolean"},
				{Name: "income_to_rent_met", Type: "boolean"},
				{Name: "employment_verified", Type: "boolean"},
				{Name: "reference_count", Type: "double precision"},
				{Name: "has_co_applicant", Type: "boolean"},
				{Name: "has_guarantor", Type: "boolean"},
				{Name: "guarantor_income_to_rent_met", Type: "boolean"},
				{Name: "missing_onboarding", Type: "boolean"},
				{Name: "missing_bgcheck", Type: "boolean"},
				{Name: "missing_payment", Type: "boolean"},
				{Name: "missing_signature", Type: "boolean"},
				{Name: "missing_decision", Type: "boolean"},
				{Name: "inflight_bgcheck", Type: "boolean"},
				{Name: "inflight_payment", Type: "boolean"},
				{Name: "declined_bgcheck", Type: "boolean"},
				{Name: "declined_payment", Type: "boolean"},
				{Name: "declined", Type: "boolean"},
				{Name: "escalated_bgcheck", Type: "boolean"},
				{Name: "escalated_payment", Type: "boolean"},
				{Name: "lost_to_rival", Type: "boolean"},
			},
		},
		{
			// landlordLeaseApplicationsRead — the protected Postgres read model for
			// the LANDLORD-facing "applications to my units" view (D1.3 Increment 2,
			// the landlord/residence audience). The sibling of leaseApplicationsRead
			// above: same protected-by-default §6.14 posture, but anchored to the
			// managing LANDLORD instead of the applicant.
			//
			// THE RESIDENCE AUDIENCE NEEDS NO `cap-read.residence` GRANT LENS. The
			// row carries the managing landlord's bare NanoID as its authz_anchor,
			// and the primordial cap-read self-grant (bootstrap.capabilityReadGrants)
			// already grants every identity — a landlord is a vtx.identity — its OWN
			// NanoID. The §6.14 set-membership RLS policy makes a row visible iff the
			// reading actor holds a grant for ANY of its authz_anchors, so a landlord
			// L's session (lattice.actor_id = L's NanoID) sees exactly the rows
			// anchored to L = the applications to units L manages, and nobody else's.
			// No relationship-grant producer, and so no link-triggered reprojection
			// primitive (which the plain pipeline does not provide today).
			//
			// The lens is anchored on the LEASEAPP (a plain projection, like
			// leaseApplicationsRead), so it reprojects on the leaseapp's own vertex
			// CDC — which the plain pipeline already handles. It resolves the
			// managing landlord at projection time by walking the unit's `manages`
			// link (loftspace-domain's AssignUnitOwner mints
			// lnk.identity.<landlordID>.manages.unit.<unitID>, class "manages").
			//
			// Every MATCH is REQUIRED (not OPTIONAL): a leaseapp projects a
			// landlord-row only if it has an applicant, applies to a unit, AND that
			// unit has a managing landlord. A unit with no manager projects no row —
			// fail-closed (never a null/empty authz_anchor the array adapter would
			// choke on; never a row no landlord can read). A co-managed unit (more
			// than one `manages` link) fans the leaseapp out to one row PER landlord,
			// so the key is the composite (app_id, landlord_id): each (application,
			// landlord) pair is its own row, anchored to that one landlord — the
			// natural multi-owner shape, no collision.
			//
			// CAVEAT (documented; the low-priority eager-reprojection follow-up in
			// lattice.md): the landlord anchor is baked at leaseapp projection time,
			// so an ownership TRANSFER on a unit with live applications goes stale
			// until each leaseapp's next CDC touch (it self-heals on any subsequent
			// change to the application). This is the same staleness class the
			// applicant lens's unit display columns already accept.
			//
			// No unit_bedrooms / unit_bathrooms here (unlike leaseApplicationsRead's
			// D1.5 addition above): no landlord-facing view reads this model for
			// document rendering. unit_available_from / unit_lease_term_months ARE
			// here: the landlord's decide surface states the terms the approval
			// signs, and where the application's own .terms carries nothing the
			// listing is the term's source (DecideLeaseApplication's own fallback),
			// so the hint needs both halves of that chain.
			//
			// doc_store_name / doc_filename / doc_content_type mirror
			// leaseApplicationsRead's own doc-pointer columns exactly (same source
			// walk: (app)<-[:providedTo]-(docInst:service) for the completed docGen
			// outcome, (app)<-[:signedLease]-(leaseDocObj:object) for the attachment,
			// the same max(CASE …) gate requiring both). cmd/loftspace-app's GET
			// /api/lease-document reads the applicant-scoped model first and falls
			// back to THIS lens under the same actor when that read finds no row —
			// so whoever this lens already anchors (the managing landlord, and every
			// building-anchored actor the authz_anchors fan-out below admits) reads
			// the same pointers off the row they can already read, still under RLS.
			// The document renders nothing that is not already a column of that
			// row (tenant name, unit, rent, term, dates), so the pointer widens no
			// information class.
			//
			// The 7 applicant qualification-profile signal columns (D1.5 Rec C,
			// loftspace-d1.5-landlord-rls-decision-surface-design.md §4/§5) are
			// pure `app.applicationSignals.data.*` scalar hops off the already-anchored
			// leaseapp. `qualified` (D1.5 Rec-C remainder, §4 Option A) clones the
			// service-instance readiness aggregation via the readinessOptionalMatch
			// / readinessWithItems fragment shared with `leaseApplicationCompleteSpec`
			// — see the Spec doc comment below. Console retirement (moving
			// Approve/Decline itself onto this RLS surface) is a separate,
			// FE-consolidation follow-up, not required for this column to be correct.
			//
			// SECURE LENS (Contract #3 §3.10, Vault Phase B): applicant_name /
			// applicant_email / applicant_phone are the applicant identity's
			// sensitive contact aspects, decrypted at projection into this
			// RLS-protected table only — the landlord who manages the unit is the
			// authorized reader of the applicant's contact details. The cypher
			// RETURNs each aspect's ciphertext envelope whole (id.<aspect>.data);
			// the decryptor opens each under the holder the ciphertext names, which
			// is the applicant's own identity, not the landlord anchor. A
			// missing aspect projects null; a shredded applicant's columns project
			// null (right-to-erasure). The shred's piiKey CDC event triggers
			// re-evaluation of this lens, and because the anchor MATCH is
			// UNANCHORED (no {key: $actorKey}) the full engine re-scans every
			// leaseapp and re-projects every row with a fresh decrypt — so an
			// already-projected plaintext row scrubs to null even though the
			// identity is not this lens's anchor (pinned by
			// TestSecureLens_NeighborShredReprojectsAnchoredRows).
			// DiffRetraction: landlord_id is resolved by walking the
			// `manages` link off the matched unit, not off the leaseapp anchor —
			// AnchorProjectionKey can never derive this composite key
			// read-free (exprReferencesOnlyVariable rejects it structurally), so
			// a manages-unassign (or any other drop) needs the target-diff path.
			// The rows nonetheless PARTITION by the anchor — app_id identifies
			// the leaseapp, landlord_id binds the neighbour — so the lens seeds
			// on its own anchor's events, narrows on its neighbours', and diffs
			// within one leaseapp's partition rather than against the whole
			// table (PartitionsByAnchor, adapter.PartitionKeyLister).
			// The `qualified` WITH clause below does not touch this posture:
			// ValidateUnanchoredForDiffRetraction walks a lens's WITH/RETURN only
			// for `$actorKey` references, and this WITH references no parameter at
			// all — least of all `$actorKey`. Target-diff compares whole fresh-vs-stored
			// row-sets, not a per-row derivation — it is indifferent to whether
			// the cypher has a WITH. This lens was never eligible for anchor-self
			// retraction's WITH exclusion in the first place (that gap is about a
			// different mechanism); adding a WITH here neither triggers nor
			// interacts with it.
			CanonicalName:  "landlordLeaseApplicationsRead",
			Class:          "meta.lens",
			Adapter:        "postgres",
			Table:          "read_landlord_lease_applications",
			Engine:         "full",
			Spec:           landlordLeaseApplicationsReadSpec,
			Protected:      true,
			DiffRetraction: true,
			IntoKey:        []string{"app_id", "landlord_id"},
			Columns: []pkgmgr.PostgresColumn{
				{Name: "entity_key", Type: "text"},
				{Name: "applicant", Type: "text"},
				{Name: "landlord_key", Type: "text"},
				{Name: "unit_key", Type: "text"},
				{Name: "unit_address", Type: "text"},
				{Name: "unit_city", Type: "text"},
				{Name: "unit_region", Type: "text"},
				{Name: "unit_rent", Type: "double precision"},
				{Name: "unit_currency", Type: "text"},
				{Name: "unit_status", Type: "text"},
				{Name: "unit_available_from", Type: "text"},
				{Name: "unit_lease_term_months", Type: "double precision"},
				{Name: "signed_at", Type: "text"},
				{Name: "landlord_decision", Type: "text"},
				{Name: "decline_reason", Type: "text"},
				{Name: "terms_move_in_date", Type: "text"},
				{Name: "terms_lease_term_months", Type: "double precision"},
				{Name: "terms_requested_rent", Type: "double precision"},
				{Name: "tenancy_lease_start", Type: "text"},
				{Name: "tenancy_lease_end", Type: "text"},
				{Name: "tenancy_term_start", Type: "text"},
				{Name: "tenancy_rent_amount", Type: "double precision"},
				{Name: "deposit_amount", Type: "double precision"},
				{Name: "tenancy_ended_at", Type: "text"},
				{Name: "notice_move_out_at", Type: "text"},
				{Name: "notice_given_at", Type: "text"},
				{Name: "notice_given_by", Type: "text"},
				{Name: "doc_store_name", Type: "text"},
				{Name: "doc_filename", Type: "text"},
				{Name: "doc_content_type", Type: "text"},
				{Name: "profile_submitted", Type: "boolean"},
				{Name: "income_to_rent_met", Type: "boolean"},
				{Name: "employment_verified", Type: "boolean"},
				{Name: "reference_count", Type: "double precision"},
				{Name: "has_co_applicant", Type: "boolean"},
				{Name: "has_guarantor", Type: "boolean"},
				{Name: "guarantor_income_to_rent_met", Type: "boolean"},
				{Name: "applicant_name", Type: "text"},
				{Name: "applicant_email", Type: "text"},
				{Name: "applicant_phone", Type: "text"},
				{Name: "qualified", Type: "boolean"},
				{Name: "lost_to_rival", Type: "boolean"},
			},
			SecureColumns: []pkgmgr.SecureColumn{
				{Column: "applicant_name", HolderTypes: []string{"identity"}, Field: "value"},
				{Column: "applicant_email", HolderTypes: []string{"identity"}, Field: "value"},
				{Column: "applicant_phone", HolderTypes: []string{"identity"}, Field: "value"},
			},
		},
	}
	lenses = append(lenses, RenewalLenses()...)
	return append(lenses, TenancyEndLenses()...)
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
//     freshness (see FRESHNESS below); payment is ever-completed. missing_bgcheck
//     ALSO requires ssnVal <> null (onboarding already done): the backgroundCheck
//     pattern's params subject-template .name/.dob (sensitive-param-egress-design
//     §7, the live subject-PII adapter consumer), and Loom's egressReads
//     hydration is fail-closed like reads (missing key ⇒ HydrationMiss on
//     first touch, same deferred semantics as reads) — without
//     this gate Weaver would race missing_bgcheck against missing_onboarding
//     (both open on a fresh application) and could dispatch the pattern before
//     RecordIdentityPII ever wrote .dob, permanently HydrationMiss-rejecting the
//     instanceOp with no auto-retry (found + fixed building the Fire 2 e2e).
//   - missing_signature — the application has no .signature aspect. SignLease
//     writes it, flipping this false.
//
// All four applicant gaps ALSO require (unitKey <> null) AND ((unitStatus <>
// 'leased') OR (landlordDecision = 'approved')) AND (landlordDecision <>
// 'lost'). A null unitKey means the
// appliesToUnit target itself is tombstoned (the OPTIONAL MATCH drops it, so
// unitKey / unitStatus both project null): there is no unit left to lease, so
// none of the four remediations (RecordIdentityPII, a bgcheck, a payment,
// SignLease) is still wanted — the application is terminal-not-violating, the
// same shape as a landlord decline. This mirrors missing_listingLeased /
// missing_manager's own (unitKey <> null) gate below. A null unitStatus WITH a
// non-null unitKey is the distinct, still-open case — a live unit that has no
// .listing aspect yet — and `unitStatus <> 'leased'` evaluates true for it
// (full engine's `<>` — values.go equalsAny/evalBinary: nil <> "leased" is
// true), keeping the gaps reachable: the applicant may still end up leasing
// that unit once a listing exists.
//
// Independently, once this application's unit HAS leased TO SOMEONE ELSE
// (unitStatus = 'leased' AND this row's own landlordDecision is NOT
// 'approved'), none of the four remediations is still wanted either. Without
// the term a losing rival's still-open gaps kept re-dispatching
// RecordIdentityPII/SignLease (userTasks asking a real person for their SSN and
// a signature) against a unit they can no longer get, indefinitely, because
// nothing in the applicant's own journey ever closes those gaps once someone
// else wins the unit. The (landlordDecision = 'approved') escape hatch is
// load-bearing, not cosmetic: the WINNING applicant's own bgcheck can go STALE
// again after their unit has already leased (see FRESHNESS below), and the
// EAGER auto-reopen-at-expiry @at timer expects missing_bgcheck to reopen and
// Weaver to re-dispatch a fresh check for them — a bare (unitStatus <>
// 'leased') term would permanently wedge that gap closed the instant their own
// unit leases, breaking the reopen cycle for the one application the unit
// actually belongs to (caught live by TestLeaseConvergence_BgcheckFreshness_EagerReopen,
// which drives a full approve→lease→lapse→reopen cycle). A rival's own
// landlordDecision is null or 'declined', never 'approved' for THIS row, so the
// escape hatch never re-opens a rival's gaps once their unit is gone.
//
// The unitStatus term is a LIVE derivation over the unit's mutable listing,
// and it is only the first half of how a rival's loss is read. The unit going
// to someone else is a RECORDED FACT on the losing application: .decision =
// {value: 'lost', decidedAt}, a third terminal value of the landlord's own
// decision aspect, written by RecordApplicationLoss (scripts.go). The
// missing_lossRecorded gap is what drives that write — it opens on exactly
// the premise the live term reads, (unitKey <> null) AND (unitStatus =
// 'leased') AND (landlordDecision = null), and the target dispatches the
// directOp under Weaver's service actor (targets.go, the missing_tenancyEnded
// → EndTenancy shape). The (landlordDecision <> 'lost') conjunct on the four
// applicant gaps is what makes the recorded fact hold: when the winner's
// tenancy ends and missing_relist flips the unit back to 'available', the
// live term reads open again, but the recorded loss keeps every rival's gaps
// shut — a lost application is closed, never revived; a relist is a new
// opportunity the rival applies for afresh (Withdraw frees the guard link).
// The engine is two-valued, so `null <> 'lost'` is true (values.go) and an
// undecided application on an available unit keeps its gaps reachable.
// missing_decision needs no 'lost' conjunct: it requires landlordDecision =
// null, which 'lost' is not.
//
// violating is the explicit OR of the four applicant gaps PLUS missing_decision
// PLUS missing_listingLeased PLUS missing_lossRecorded PLUS missing_residence
// (Contract #10 §10.2: violating is lens-projected, not an implicit OR; for
// this target the rule is "any applicant gap OR a qualified-but-undecided
// application OR a landlord-approved-but-unleased unit OR a rival whose loss
// is not yet recorded OR an approved live term whose applicant is not wired
// to the unit → violating"). Folding missing_listingLeased into violating is
// load-bearing: Weaver skips all dispatch when violating=false, so the
// listing-flip directOp only fires while the row is violating — and the same
// holds for missing_lossRecorded's RecordApplicationLoss. missing_decision keeps a
// qualified-but-undecided application explicitly open (its work is not done until
// the landlord decides) WITHOUT dispatching anything — it maps to no playbook
// entry, so the row stays violating while no remediation fires; it too carries
// the (unitStatus <> 'leased') term (no approved-escape needed here: missing_decision
// already requires landlordDecision = null, which is mutually exclusive with
// landlordDecision = 'approved' on the same row), so a rival still awaiting
// their own decision stops being counted violating the moment the unit leases to
// someone else. A landlord-DECLINED application is terminal-not-violating: every
// violating term is false (the applicant gaps are closed, missing_decision is
// false because the decision is non-null, and missing_listingLeased is false
// because the decision is not 'approved'), so Weaver stops reconciling it — there
// is no work left to do (the FE reads the declined column for the terminal
// disposition). A rival application whose unit leases to someone else is
// violating for exactly one evaluation's worth of work — missing_lossRecorded
// — and reaches the same terminal-not-violating state once .decision = 'lost'
// is recorded: every applicant gap carries the 'lost' conjunct, missing_decision
// is false because the decision is non-null, missing_listingLeased is false
// because it is not 'approved', and missing_lossRecorded is false because it
// is no longer null. In the seconds before Weaver records it, the (unitStatus
// <> 'leased') term already holds the four gaps and missing_decision shut.
//
// ENDED TENANCY — the third terminal-not-violating shape, beside the decline
// and the recorded loss. The four applicant gaps AND missing_listingLeased each
// carry (tenancyEndedAt = null): once EndTenancy has recorded .tenancy.endedAt
// (the tenancyEnd target, tenancy_end_lenses.go), the application is a lease
// whose term is over, and none of its remediations is wanted any more. The
// conjunct is load-bearing on two of them. missing_listingLeased: the ended
// term's relist flips its unit back to 'available', and this row still reads
// approved + qualified + signed — without the conjunct that is exactly the
// listing-flip shape, so Weaver would re-lease the unit to the tenant who
// just left, and tenancyEnd would relist it again, forever. missing_bgcheck:
// the winning applicant's (landlordDecision = 'approved') escape hatch above
// keeps their bgcheck gap reopenable after their unit leases — the right
// posture while they live there, and exactly wrong once they have left: a
// bgcheck lapsing after the term ended would re-dispatch a vendor check on a
// former tenant. missing_decision needs no conjunct — it requires
// landlordDecision = null, and an ended tenancy is by construction approved.
// missing_manager / missing_leaseDoc / missing_leaseDocAttach are untouched:
// an unmanaged unit is still an operator's problem, and the executed lease
// document is a record of the term whether or not it has ended. tenancyEndedAt
// projects as a read-only column so a reader can tell this terminal shape
// from the other two.
//
// missing_residence — a landlord-approved, signed application whose term has
// not ended (leaseEnd <> null, tenancyEndedAt = null) but whose applicant
// carries no live residesIn link to the leased unit: (unitKey <> null) AND
// (applicant <> null) AND (landlordDecision = 'approved') AND (leaseEnd <>
// null) AND (tenancyEndedAt = null) AND (residenceCount = 0), where
// residenceCount is count(DISTINCT res.key) over OPTIONAL MATCH
// (app)-[:applicationFor]->(resId:identity)-[res:residesIn]->(resU:unit)
// <-[:appliesToUnit]-(app) — the applicant's own residesIn edge to THIS
// application's unit, never any other unit they might separately reside at.
// resId/resU are fresh variables closing back on the anchor `app` (already
// bound by the required MATCH) rather than naming `id`/`u`: a clause naming
// both `id` (owned by the applicationFor branch) and `u` (owned by the
// appliesToUnit branch) spans two sibling subtrees and is refused for the
// WHOLE stage's decomposition — every other fan here (docInst, mgr, onbTask,
// sigTask) rides on it too — which is the shape `(id)-[res:residesIn]->(u)`
// would take. Closing on the anchor instead roots a brand-new, independent
// group (a base variable carries no group ownership to cross into,
// ruleengine/full/branchgroups.go), and is structurally equivalent: `app`
// carries exactly one live applicationFor target and one live appliesToUnit
// target, so resId/resU can only bind to id/u respectively.
// → directOp WireResidesIn{identity: row.applicant, location: row.unitKey}
// (targets.go), the release its sibling missing_residenceUnwired on the
// tenancyEnd target performs at endedAt (tenancy_end_lenses.go). "At
// approval" (leaseEnd <> null the instant .tenancy is recorded, the same
// instant landlordDecision reads 'approved') is deliberate: the fact is the
// application's own, conferred the moment the term exists, not deferred to
// signing or to the unit's own listing flip. Once WireResidesIn commits, the
// gap stays shut for as long as residenceCount reads non-zero — ordinarily
// the life of the term, since nothing else tombstones the link before
// UnwireResidesIn does at endedAt. An operator's hand UnwireResidesIn run
// MID-term also zeroes residenceCount, and convergence reads that exactly
// like any other divergence: the gap re-opens on the next evaluation and
// Weaver re-wires it, tenancyEndedAt or not — the conjunct's own
// (tenancyEndedAt = null) term is what keeps re-wiring possible up to
// endedAt, not a one-time transition this gap only fires once.
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
//   - profileSubmitted (bool) — whether the applicant has recorded a .applicationSignals
//     aspect (submittedAt <> null). incomeToRentMet / employmentVerified / referenceCount /
//     hasCoApplicant / hasGuarantor / guarantorIncomeToRentMet are the DERIVED
//     qualification signals SetApplicantProfile computes (the engine has no arithmetic
//     / len, so the op derives them) and stores on .applicationSignals; the lens
//     projects them verbatim. The RAW financials (annualIncome, employerName, the
//     reference strings, and the guarantor's relationship/income) live on the SENSITIVE
//     .profile aspect, and the guarantor / co-applicant's OWN identifiers (names,
//     contacts) live on the SENSITIVE .underwritingParties aspect — both custodied on
//     the package's own underwritingRecord retention class (RetentionClasses) and
//     deliberately NOT projected by this lens (retention-class-key-custody-design.md
//     §9.1: step 6.5 encrypts a whole aspect's data map, so the derived signals must
//     live on their own non-sensitive aspect to stay plain-lens-readable). So a landlord
//     reads "income meets 3× rent / employed / N references / guarantor covers 3× rent"
//     without the raw figures or the third-party identities. All feed no gap predicate
//     (capture + surface, not a convergence gate) — a null .applicationSignals projects
//     null signal columns, leaving convergence untouched.
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
//     no timer has recorded THAT check's own window lapsing. The lapse is a FACT
//     on the instance — the freshnessExpiry marker's byTarget entry for the
//     backgroundCheckFreshness target — so the predicate is
//     NOT (inst.freshnessExpiry.data.byTarget.backgroundCheckFreshness >=
//     inst.outcome.data.validUntil): a comparison between two stored values,
//     with no clock in it. A LAPSED bgcheck stops counting and missing_bgcheck
//     re-opens whenever the row is (re)evaluated — a stale background check IS a
//     missing background check. The test lives inside the count CASE on the
//     single providedTo fan (no second match, no WHERE), so it cannot drop the
//     anchor. validUntil is computed by the replyOp as completedAt +
//     bgcheckFreshnessWindow (Starlark time.rfc3339_add — no clock read), the
//     §10.2 "the freshness rule lives in the cypher" convention. The `>=` on
//     these canonical-UTC RFC3339 strings is lexicographic = chronological
//     (ruleengine/full executor.go compareAny string branch).
//   - The NEGATION is what makes an unmarked instance read FRESH, and it is the
//     opposite polarity from a gap test. compareAny answers FALSE whenever
//     either operand is nil, for every ordering operator, so an instance no
//     timer has ever fired on evaluates (null >= validUntil) = false and
//     NOT(false) counts it — which is the default a never-lapsed check needs.
//     The explicit (validUntil <> null) conjunct is the other half of that same
//     nil-false: an outcome carrying no validUntil has no window to lapse, and
//     without the conjunct it would read permanently fresh instead of never
//     fresh.
//   - The window is armed on the INSTANCE, by this package's
//     backgroundCheckFreshness lens + target: that row carries the instance's
//     own freshUntil, Weaver's temporal lane schedules the @at from it, and the
//     fired timer's MarkExpired records the lapse on the instance — the entity
//     whose window actually lapsed. The instance is a providedTo neighbour of
//     this anchor, so the marker write reprojects this row and missing_bgcheck
//     re-opens at the lapse instant, eagerly, without waiting for an incidental
//     CDC touch and without this lens carrying a timer of its own.
//   - missing_payment is ever-completed: a completed payment counts forever,
//     validUntil ignored.
//
// DISPATCH SUPPRESSION — the per-gap inflight_<g> companion + maxretries_<g> cap.
//
//	inflight_<g> is a §10.2 BodyColumn Weaver reads as a dispatch-suppression
//	companion of the gap missing_<g> (the prefix-swap convention, like freshUntil):
//	while it is true Weaver does NOT (re-)dispatch the externalTask, but the gap
//	stays missing_<g>=true / violating — only re-dispatch is suppressed. The
//	service-backed ones are counted on the SAME single no-WHERE providedTo fan as
//	the missing_* counts, so they add no filtered optional that could drop the
//	anchor; the two human-paced ones fan off their own OPTIONAL task walks, which
//	cannot drop it either (the anchor is the required `app` match).
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
//	- inflight_onboarding / inflight_signature — the same contract for the two
//	  HUMAN-paced gaps, whose remediation is a user task rather than a vendor call:
//	  an open task (task.data.status = 'open') bound to that gap's own operation
//	  through forOperation — RecordIdentityPII for onboarding, SignLease for
//	  signature. Discriminating on op.data.operationType is what keeps them from
//	  over-suppressing: several gaps hang tasks off the same applicant, and an
//	  unrelated open task must not park a gap it cannot close. Without the
//	  companion these two never stop re-dispatching, because only a person can
//	  close them: the mark lease expires, the sweep reclaims, and Weaver re-fires a
//	  remediation whose task is already sitting open. The duplicate op collapses on
//	  the tracker, so nothing is written twice — but every reclaim still books an
//	  `__effect` dispatch into a window that cannot record a close until the person
//	  acts, which surfaces downstream as a false LensEffectMismatch.
//	  The onboarding companion anchors on the applicant identity, not the
//	  application: ssn is a property of the person, so one RecordIdentityPII closes
//	  the gap for every application they hold.
//	- maxretries_<g> — the per-gap retry cap, a CONSTANT integer column baked from
//	  retry_budget.go (maxBgcheckRetries / maxPaymentRetries) onto every row. The
//	  budget itself is NOT a lens predicate (a lifetime failed-count never resets on
//	  success): Weaver keeps a per-(target, entity, gap) dispatch-count in
//	  weaver-state, reads this cap off the row, and stops auto-dispatching once the
//	  count reaches it — the operator-visible "needs human escalation" terminal. The
//	  count is deleted when the gap closes, so a later renewal starts a fresh budget.
//	  Keeping the cap a package-owned column (like maxretries_payment's sibling
//	  columns, or unitStatus) leaves the policy in the package with no contract
//	  change.
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
//	  like declineReason / unitAddress. A verification-declined application keeps an
//	  applicant gap open so it is also violating (Weaver keeps reconciling — a retry
//	  may clear); a LANDLORD-declined application is terminal-not-violating (its
//	  applicant gaps are closed and the decision is non-null, so missing_decision and
//	  missing_listingLeased are both false) — declined here means "done, terminally
//	  rejected," no work remains. The lens cannot see Weaver's per-gap dispatch count,
//	  so a verification declined is "a rejection stands right now," not "retries are
//	  terminally exhausted"; while a retry is in flight inflight_<g> is true and the
//	  FE prefers that ("re-checking") over the standing-rejection read.
//
// ESCALATED DISPOSITION — the per-gap escalated_<g> column, the third leg beside
// inflight_<g> / declined_<g>. The lens still cannot see Weaver's per-gap
// dispatch count (declined's own caveat above), but once that count crosses its
// maxretries_<g> cap Weaver's WeaverTargets Augur block (retry_budget.go,
// packages/lease-signing/targets.go) escalates the gap by submitting
// CreateAugurReasoningClaim, which mints a vtx.augurproposal write-ahead of the
// AI-reasoning call and links it forCandidate to THIS leaseapp (packages/augur).
// escalated_<g> is presence-based like inflight_<g>: any augurproposal reached
// via the inbound forCandidate link whose TRUSTED .gap.gapColumn names this gap
// (packages/augur/lenses.go's augurProposalsSpec projects the same aspect for
// Loupe's review surface) sets it true, regardless of the proposal's review
// state — the existence of a claim IS the "escalated to Augur" signal the FE
// needs to stop rendering a stalled automated step as "To do" (renderApplicationCard's
// stepper, cmd/loftspace-app/web/app.js). A gap that closes (missing_<g> flips
// false) reads "done" regardless of escalated_<g> — stepState checks done first.
//
// EXECUTED-LEASE DOCUMENT — the docGen externalTask chain + the attach anchor.
//
//	The docGen claims are providedTo the LEASEAPP itself (the document is about
//	the application), so they ride their own OPTIONAL MATCH fan
//	((app)<-[:providedTo]-(docInst:service)) — distinct from the identity fan —
//	and the produced artifact's anchor is the signedLease link
//	((app)<-[:signedLease]-(leaseDocObj:object), the object→owner direction
//	objects-base AttachObject commits). Every use of both fans is aggregated
//	(count DISTINCT / max) inside the same grouping WITH, so the anchor stays
//	one-row (the cross product between fans is collapsed by DISTINCT on each
//	fan's own keys).
//
//	- missing_leaseDoc — signature present AND no completed docGen outcome AND
//	  none in flight AND none failed. Opens on signing; the playbook triggerLooms
//	  the leaseDocument pattern (the vendor renders + stores the bytes;
//	  RecordLeaseDocOutcome records the pointer-carrying .outcome, closing it).
//	  Folding declined_docGen into the gap (rather than leaving it open like
//	  missing_bgcheck) makes a FAILED render TERMINAL — no auto-retry; a
//	  re-generation is a fresh manual StartLoomPattern. Folding inflight in
//	  closes the gap while a future ASYNC vendor call is pending (the sync
//	  reference adapter never writes .dispatch, so the gap simply stays open for
//	  the milliseconds between claim and outcome — Weaver's dispatch mark + the
//	  claimId-stable Loom instanceId absorb any re-fire in that window).
//	- inflight_docGen / declined_docGen — the same presence-based pending test
//	  and standing-rejection disposition as the bgcheck/payment families
//	  (FE-facing; the gap formula above consumes them, so they are not Weaver
//	  suppression companions — the companion convention would name them
//	  inflight_leaseDoc, which no column uses).
//	- docStoreName / docFilename / docContentType / docDigest / docSize — the
//	  produced artifact's pointer set off the completed .outcome, the param
//	  columns the §10.8 AttachObject playbook templates (non-null exactly when a
//	  completed outcome exists, so a missing_leaseDocAttach dispatch never
//	  resolves a null param). max() over the completed fan: the pointers are
//	  deterministic per application (the vendor derives storeName from the
//	  leaseapp key and overwrites on re-render), so multiple completed claims
//	  carry the same values.
//	- missing_leaseDocAttach — a completed docGen outcome exists AND no
//	  signedLease attachment does. The playbook dispatches the generic
//	  directOp(AttachObject) anchoring the stored bytes to the application;
//	  the committed link reprojects this anchor (the link fan-out seeds from
//	  both endpoints) and the gap closes. A detached executed lease re-opens it
//	  — the self-healing re-attach.
//	- leaseDocAttached — the FE-facing presence signal for the anchored artifact.
//	- Both gaps fold into violating (Weaver dispatches only violating rows).
//
// '= null' (not IS NULL) is the full engine's null test (ruleengine/full
// executor.go equalsAny treats null = null as true and any value = null as
// false). Do not "correct" it to unsupported IS NULL.
//
// readinessOptionalMatch + readinessWithItems are the SHARED cypher pieces
// deriving applicant readiness — ssn-on-file (a presence test only; the ONE
// sanctioned sensitive read, never projected as a value), a fresh-completed
// background check, and a completed payment. ssnVal reads id.ssn.data (the
// WHOLE aspect body), never .data.value: step 6.5 replaces a sensitive
// aspect's entire `data` field with its ciphertext envelope ({ct, nonce,
// keyId}, no `value` key), so a .value hop resolves null for every real
// (encrypted) ssn and would silently strand every real application at
// missing_onboarding forever — only the fixture-only plaintext {value: ...}
// shape used by these lenses' own tests happened to mask it. `.data <> null`
// is presence-correct under both shapes. Both leaseApplicationCompleteSpec
// (the trusted convergence lens, source of truth) and
// landlordLeaseApplicationsReadSpec (the RLS-protected landlord lens, D1.5 Rec-C
// readiness clone) splice these in verbatim via fmt.Sprintf, so a readiness-rule
// change lands in ONE place instead of drifting between two hand-copied lenses —
// the divergence hazard decision-surface-design.md §4 Option A flags. Each
// consumer supplies its own final `qualified`/`applicantApproved` boolean
// (`(ssnVal <> null) AND (freshBgComplete > 0) AND (payComplete > 0) AND
// (<its own signedAt alias> <> null)`) since the signedAt alias name differs
// per lens; that AND term is intentionally NOT folded into the shared fragment.
const readinessOptionalMatch = `OPTIONAL MATCH (id)<-[:providedTo]-(inst:service)`

var readinessWithItems = `id.ssn.data AS ssnVal,
  count(DISTINCT CASE WHEN inst.class = 'service.backgroundCheck.instance' AND inst.outcome.data.status = 'completed' AND inst.outcome.data.validUntil <> null AND NOT (inst.freshnessExpiry.data.byTarget.` + BackgroundCheckFreshnessTarget + ` >= inst.outcome.data.validUntil) THEN inst.key ELSE null END) AS freshBgComplete,
  count(DISTINCT CASE WHEN inst.class = 'service.payment.instance' AND inst.outcome.data.status = 'completed' THEN inst.key ELSE null END) AS payComplete`

// backgroundCheckFreshnessSpec anchors on the background-check INSTANCE — the
// entity whose freshness window it is. It exists so that window has a timer of
// its own: Weaver's temporal lane arms an @at from a row's freshUntil, and the
// fired timer's MarkExpired carries the row's anchor as the entity to mark, so
// the marker can only land on the entity the row is anchored on. Every reader of
// bgcheck freshness in this package — readinessWithItems (spliced into
// leaseApplicationComplete, leaseApplicationsRead and
// landlordLeaseApplicationsRead) and renewalCompleteSpec's bgcheckValidUntil
// aggregate — resolves that window off a service instance reached across a
// providedTo hop, so none of them can host the marker; this lens is where the
// instance hosts it itself, and the four read the fact it records.
//
// The anchor MATCH filters on class + a completed outcome, so a payment or
// docGen instance (same `service` key type, different envelope class) and a
// dispatched-but-unanswered bgcheck project no row at all — EmptyBehavior
// "delete" removes any row a no-longer-qualifying instance left behind.
//
//   - freshUntil is the instance's own outcome.validUntil while no timer has
//     recorded this target's window lapsing, and null once one has. Null is what
//     stops the re-arm: a lapsed instance's past deadline would otherwise fire
//     immediately on every delivery, and there is nothing left to wait for —
//     the lapse is already recorded. The comparison is between two stored
//     values (the marker's byTarget entry for this target, and the deadline),
//     so the row is a pure function of the subgraph.
//   - violating is projected an explicit FALSE rather than omitted. Weaver reads
//     the column off the row body, and an absent column and a declared false are
//     different inputs; this target dispatches nothing, so the answer is always
//     false and it says so.
//
// The target declares no gaps (weaverTargets in targets.go): the freshness
// bookkeeping leg runs on every delivery, before any gap column is read, which
// is the whole reason a gap-less target is a coherent thing to declare.
var backgroundCheckFreshnessSpec = `
MATCH (inst:service {key: $actorKey})
  WHERE inst.class = 'service.backgroundCheck.instance' AND inst.outcome.data.status = 'completed'
WITH
  inst.key AS entityKey,
  inst.outcome.data.validUntil AS validUntil,
  inst.freshnessExpiry.data.byTarget.` + BackgroundCheckFreshnessTarget + ` AS lapsedAt
RETURN
  entityKey AS actorKey,
  entityKey,
  CASE WHEN lapsedAt >= validUntil THEN null ELSE validUntil END AS freshUntil,
  False AS violating
`

// supersededBackgroundChecksSpec anchors on the background-check INSTANCE, the
// same anchor backgroundCheckFreshnessSpec above uses, and projects VIOLATING
// ROWS ONLY: an anchor with no qualifying successor binds nothing in the
// second MATCH, so the WITH's aggregation runs over zero rows and the query
// emits nothing for it. There is no standing row per live check — only a
// superseded one ever shows up here, which is what keeps this lens's fan-out
// (§4.1 of the design) bounded by the population it exists to shrink rather
// than by every check this package has ever minted.
//
// The anchor MATCH is the op's own precondition on instanceKey, restated:
// completed, background-check class, owned (by instanceOf) by a meta vertex
// whose canonicalName is "leaseServiceInstance" — this DDL's own type
// authority, not merely a same-named one elsewhere.
//
// The second MATCH is the op's precondition on supersededBy: same class,
// completed, providedTo the same applicant, and either strictly later or tied
// at the same completedAt with the greater key.
//
// The successor's ownership is NOT bound as a hop, and that omission is
// load-bearing. Re-binding (newer)-[:instanceOf]->(m) would make the anchor's
// meta a pattern position reachable from the SUCCESSOR side, and affected-anchor
// derivation walks the position graph in both directions
// (internal/refractor/pipeline/anchor_derivation.go, full.HopIndex.StepsFrom):
// a single .outcome write would step successor → meta → every live inbound
// instanceOf edge of that meta, i.e. reproject EVERY live instance this package
// owns for one completion (O(N), with the derivation's read cap falling back to
// the BFS and the actor-set ceiling above it). The anchor's own (m) binding
// stays: own.key is a projected column, and the meta's position is reachable
// only from the anchor position, which derivation admits as an anchor and never
// expands. That narrowing is the ACTING derivation's answer
// (REFRACTOR_ANCHOR_DERIVATION=act, the built-in default): under `off`/`shadow`,
// or when the derivation declines (read cap, error), the ActorEnumerator BFS
// decides, and it is scoped by RELATION NAME only (actor_enumerator.go's
// neighborsInScope) — instanceOf is in the anchor's own scope entry, so the BFS
// still crosses the meta to every owned instance. The remaining fan under `act`
// is the package install rewriting its DDL metas' aspects (design §4.1 (ii),
// §11.1) — one pass over the live owned population per rewritten aspect, bounded
// by the population this rule shrinks.
//
// What the pattern does carry on the successor is a ZERO-HOP ownership SIGNAL:
// the .outcome aspect's own class. Only this package's leaseServiceOutcome
// aspectType DDL writes `leaseServiceOutcome` (RecordLeaseServiceOutcome, the
// sole writer), and the write gate resolves an aspect mutation's governing DDL
// by exact class — while service-domain's own RecordServiceOutcome, which admits
// the same `backgroundCheck` family and can mint a service.backgroundCheck.instance
// providedTo the same applicant, writes class `outcome` with {status, completedAt}
// and no validUntil. Without that conjunct a foreign instance with a greater key
// wins max(newer.key), the op refuses the row NotOwned, and the genuine
// retirement starves behind it forever — the row names a successor the op will
// never accept while the owned successor sits unnamed. With it, max() ranges over
// candidates carrying this package's own aspect, so an owned successor is never
// shadowed by a foreign one. The same conjunct rides the anchor side for
// symmetry: the aspect is already read there for `status`, so it costs nothing.
//
// The signal is not the proof. A minter that FORGED the aspect class would pass
// here (in production the write gate refuses it that aspect class, and nothing
// does), so ownership is proven by
// TombstoneSupersededLeaseServiceInstance instead, on BOTH submission paths, by
// a Contract #2 §2.5 class-(e) bounded enumeration of the successor's own
// outbound instanceOf relation (degree 1 by construction — CreateLeaseServiceInstance
// mints exactly one) against this DDL's metaKey, refusing NotOwned otherwise
// (scripts.go). Such a row is refused loudly and per-entity
// (GapBudgetExhausted) rather than retiring anything on a foreign check's word.
//
// completedAt is RecordLeaseServiceOutcome's rfc3339_utc stamp: fixed-width
// and zero-padded, so string comparison orders it identically to
// chronological order, at whole-second granularity — which is why the tie
// needs breaking at all (Weaver paces backgroundCheck dispatch at 2/s, so two
// replies for one applicant can commit in the same second). The tie-break is
// TEXTUALLY the same predicate TombstoneSupersededLeaseServiceInstance's own
// recency guard applies (scripts.go); a pinned row here that guard would
// reject is the drift detector for an edit that changes one side and not the
// other.
//
// supersededBy is max(newer.key): a deterministic member of the qualifying
// set of later, owned, completed siblings — not necessarily the newest, and
// the op accepts any member. instanceOfLink is own.key, the anchor's own
// instanceOf link key: the one read the op cannot derive from its payload
// alone (Contract #2 §2.5 class (g) covers the other six), so the lens hands
// it over as a projected column instead. The successor's own instanceOf link
// key cannot be projected the same way through this aggregation — a
// relationship variable used as the argument to max()/min() is refused at
// parse — so the op resolves it by its own bounded enumeration.
const supersededBackgroundChecksSpec = `
MATCH (inst:service {key: $actorKey})-[own:instanceOf]->(m:meta)
  WHERE inst.class = 'service.backgroundCheck.instance'
    AND inst.outcome.class = 'leaseServiceOutcome'
    AND inst.outcome.data.status = 'completed'
    AND m.canonicalName.data.value = 'leaseServiceInstance'
MATCH (inst)-[:providedTo]->(id:identity)<-[:providedTo]-(newer:service)
  WHERE newer.class = 'service.backgroundCheck.instance'
    AND newer.outcome.class = 'leaseServiceOutcome'
    AND newer.outcome.data.status = 'completed'
    AND ((newer.outcome.data.completedAt > inst.outcome.data.completedAt)
      OR ((newer.outcome.data.completedAt = inst.outcome.data.completedAt) AND (newer.key > inst.key)))
WITH inst.key AS entityKey, own.key AS instanceOfLink, id.key AS subjectKey, max(newer.key) AS supersededBy
RETURN entityKey AS actorKey, entityKey, subjectKey, instanceOfLink, supersededBy,
  True AS missing_retirement, True AS violating
`

// leaseApplicationCompleteSpec is built once at package init: the retry caps
// (maxBgcheckRetries / maxPaymentRetries) bake into the constant maxretries_<g>
// columns Weaver bounds its dispatch-count against, the §10.2 "the policy lives in
// the cypher" convention (same posture as bgcheckFreshnessWindow). The cypher
// carries no literal '%'.
var leaseApplicationCompleteSpec = fmt.Sprintf(`
MATCH (app:leaseapp {key: $actorKey})
OPTIONAL MATCH (app)-[:applicationFor]->(id:identity)
OPTIONAL MATCH (app)-[:appliesToUnit]->(u:unit)
OPTIONAL MATCH (app)-[:applicationFor]->(resId:identity)-[res:residesIn]->(resU:unit)<-[:appliesToUnit]-(app)
OPTIONAL MATCH (u)<-[:manages]-(mgr:identity)
OPTIONAL MATCH (app)<-[:providedTo]-(docInst:service)
OPTIONAL MATCH (app)<-[:signedLease]-(leaseDocObj:object)
OPTIONAL MATCH (app)<-[:scopedTo]-(sigTask:task)
  WHERE sigTask.data.status = 'open'
OPTIONAL MATCH (sigTask)-[:forOperation]->(sigOp:meta)
OPTIONAL MATCH (id)<-[:scopedTo]-(onbTask:task)
  WHERE onbTask.data.status = 'open'
OPTIONAL MATCH (onbTask)-[:forOperation]->(onbOp:meta)
%s
WITH
  app.key AS entityKey,
  id.key  AS applicant,
  app.signature.data.signedAt AS signedAt,
  app.decision.data.value AS landlordDecision,
  app.decision.data.reason AS declineReason,
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
  app.tenancy.data.endedAt  AS tenancyEndedAt,
  app.tenancy.data.leaseEnd AS leaseEnd,
  app.terms.data.moveInDate AS termsMoveInDate,
  app.terms.data.leaseTermMonths AS termsLeaseTermMonths,
  app.terms.data.requestedRent AS termsRequestedRent,
  app.applicationSignals.data.submittedAt AS profileSubmittedAt,
  app.applicationSignals.data.incomeToRentMet AS incomeToRentMet,
  app.applicationSignals.data.employmentVerified AS employmentVerified,
  app.applicationSignals.data.referenceCount AS referenceCount,
  app.applicationSignals.data.hasCoApplicant AS hasCoApplicant,
  app.applicationSignals.data.hasGuarantor AS hasGuarantor,
  app.applicationSignals.data.guarantorIncomeToRentMet AS guarantorIncomeToRentMet,
  %s,
  count(DISTINCT CASE WHEN inst.class = 'service.backgroundCheck.instance' AND inst.dispatch.data.vendorRef <> null AND inst.outcome.data.status = null THEN inst.key ELSE null END) AS bgInflight,
  count(DISTINCT CASE WHEN inst.class = 'service.payment.instance' AND inst.dispatch.data.vendorRef <> null AND inst.outcome.data.status = null THEN inst.key ELSE null END) AS payInflight,
  count(DISTINCT CASE WHEN inst.class = 'service.backgroundCheck.instance' AND inst.outcome.data.status = 'failed' THEN inst.key ELSE null END) AS bgFailed,
  count(DISTINCT CASE WHEN inst.class = 'service.payment.instance' AND inst.outcome.data.status = 'failed' THEN inst.key ELSE null END) AS payFailed,
  count(DISTINCT CASE WHEN docInst.class = 'service.docGen.instance' AND docInst.outcome.data.status = 'completed' THEN docInst.key ELSE null END) AS docGenComplete,
  count(DISTINCT CASE WHEN docInst.class = 'service.docGen.instance' AND docInst.dispatch.data.vendorRef <> null AND docInst.outcome.data.status = null THEN docInst.key ELSE null END) AS docGenInflight,
  count(DISTINCT CASE WHEN docInst.class = 'service.docGen.instance' AND docInst.outcome.data.status = 'failed' THEN docInst.key ELSE null END) AS docGenFailed,
  max(CASE WHEN docInst.class = 'service.docGen.instance' AND docInst.outcome.data.status = 'completed' THEN docInst.outcome.data.storeName ELSE null END) AS docStoreName,
  max(CASE WHEN docInst.class = 'service.docGen.instance' AND docInst.outcome.data.status = 'completed' THEN docInst.outcome.data.filename ELSE null END) AS docFilename,
  max(CASE WHEN docInst.class = 'service.docGen.instance' AND docInst.outcome.data.status = 'completed' THEN docInst.outcome.data.contentType ELSE null END) AS docContentType,
  max(CASE WHEN docInst.class = 'service.docGen.instance' AND docInst.outcome.data.status = 'completed' THEN docInst.outcome.data.digest ELSE null END) AS docDigest,
  max(CASE WHEN docInst.class = 'service.docGen.instance' AND docInst.outcome.data.status = 'completed' THEN docInst.outcome.data.size ELSE null END) AS docSize,
  count(DISTINCT CASE WHEN sigOp.data.operationType = 'SignLease' THEN sigTask.key ELSE null END) AS sigTaskOpen,
  count(DISTINCT CASE WHEN onbOp.data.operationType = 'RecordIdentityPII' THEN onbTask.key ELSE null END) AS onbTaskOpen,
  count(DISTINCT CASE WHEN leaseDocObj.key <> null THEN leaseDocObj.key ELSE null END) AS leaseDocAttachedCount,
  count(DISTINCT mgr.key) AS managerCount,
  count(DISTINCT res.key) AS residenceCount
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
  tenancyEndedAt,
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
  signedAt,
  landlordDecision,
  declineReason,
  ((unitKey <> null) AND (tenancyEndedAt = null) AND (ssnVal = null) AND ((unitStatus <> 'leased') OR (landlordDecision = 'approved')) AND (landlordDecision <> 'lost'))        AS missing_onboarding,
  ((unitKey <> null) AND (tenancyEndedAt = null) AND (ssnVal <> null) AND (freshBgComplete = 0) AND ((unitStatus <> 'leased') OR (landlordDecision = 'approved')) AND (landlordDecision <> 'lost'))  AS missing_bgcheck,
  ((unitKey <> null) AND (tenancyEndedAt = null) AND (payComplete = 0) AND ((unitStatus <> 'leased') OR (landlordDecision = 'approved')) AND (landlordDecision <> 'lost'))      AS missing_payment,
  ((unitKey <> null) AND (tenancyEndedAt = null) AND (signedAt = null) AND ((unitStatus <> 'leased') OR (landlordDecision = 'approved')) AND (landlordDecision <> 'lost'))      AS missing_signature,
  (bgInflight > 0)       AS inflight_bgcheck,
  (payInflight > 0)      AS inflight_payment,
  (docGenInflight > 0)   AS inflight_docGen,
  (onbTaskOpen > 0)      AS inflight_onboarding,
  (sigTaskOpen > 0)      AS inflight_signature,
  ((bgFailed > 0) AND (freshBgComplete = 0))  AS declined_bgcheck,
  ((payFailed > 0) AND (payComplete = 0))     AS declined_payment,
  ((docGenFailed > 0) AND (docGenComplete = 0)) AS declined_docGen,
  (((bgFailed > 0) AND (freshBgComplete = 0)) OR ((payFailed > 0) AND (payComplete = 0)) OR (landlordDecision = 'declined')) AS declined,
  (landlordDecision = 'approved') AS landlordApproved,
  (landlordDecision = 'declined') AS landlordDeclined,
  docStoreName,
  docFilename,
  docContentType,
  docDigest,
  docSize,
  (leaseDocAttachedCount > 0) AS leaseDocAttached,
  ((signedAt <> null) AND (docGenComplete = 0) AND (docGenInflight = 0) AND (docGenFailed = 0)) AS missing_leaseDoc,
  ((docGenComplete > 0) AND (leaseDocAttachedCount = 0)) AS missing_leaseDocAttach,
  ((ssnVal <> null) AND (freshBgComplete > 0) AND (payComplete > 0) AND (signedAt <> null)) AS applicantApproved,
  ((ssnVal <> null) AND (freshBgComplete > 0) AND (payComplete > 0) AND (signedAt <> null) AND (landlordDecision = null) AND (unitStatus <> 'leased')) AS missing_decision,
  ((unitKey <> null) AND (tenancyEndedAt = null) AND (ssnVal <> null) AND (freshBgComplete > 0) AND (payComplete > 0) AND (signedAt <> null) AND (landlordDecision = 'approved') AND (unitStatus <> null) AND (unitStatus <> 'leased')) AS missing_listingLeased,
  ((unitKey <> null) AND (landlordDecision = 'approved') AND (managerCount = 0)) AS missing_manager,
  ((unitKey <> null) AND (unitStatus = 'leased') AND (landlordDecision = null)) AS missing_lossRecorded,
  ((unitKey <> null) AND (applicant <> null) AND (landlordDecision = 'approved') AND (leaseEnd <> null) AND (tenancyEndedAt = null) AND (residenceCount = 0)) AS missing_residence,
  %d                     AS maxretries_bgcheck,
  %d                     AS maxretries_payment,
  (((unitKey <> null) AND (tenancyEndedAt = null) AND (ssnVal = null) AND ((unitStatus <> 'leased') OR (landlordDecision = 'approved')) AND (landlordDecision <> 'lost')) OR ((unitKey <> null) AND (tenancyEndedAt = null) AND (ssnVal <> null) AND (freshBgComplete = 0) AND ((unitStatus <> 'leased') OR (landlordDecision = 'approved')) AND (landlordDecision <> 'lost')) OR ((unitKey <> null) AND (tenancyEndedAt = null) AND (payComplete = 0) AND ((unitStatus <> 'leased') OR (landlordDecision = 'approved')) AND (landlordDecision <> 'lost')) OR ((unitKey <> null) AND (tenancyEndedAt = null) AND (signedAt = null) AND ((unitStatus <> 'leased') OR (landlordDecision = 'approved')) AND (landlordDecision <> 'lost')) OR ((ssnVal <> null) AND (freshBgComplete > 0) AND (payComplete > 0) AND (signedAt <> null) AND (landlordDecision = null) AND (unitStatus <> 'leased')) OR ((unitKey <> null) AND (tenancyEndedAt = null) AND (ssnVal <> null) AND (freshBgComplete > 0) AND (payComplete > 0) AND (signedAt <> null) AND (landlordDecision = 'approved') AND (unitStatus <> null) AND (unitStatus <> 'leased')) OR ((signedAt <> null) AND (docGenComplete = 0) AND (docGenInflight = 0) AND (docGenFailed = 0)) OR ((docGenComplete > 0) AND (leaseDocAttachedCount = 0)) OR ((unitKey <> null) AND (landlordDecision = 'approved') AND (managerCount = 0)) OR ((unitKey <> null) AND (unitStatus = 'leased') AND (landlordDecision = null)) OR ((unitKey <> null) AND (applicant <> null) AND (landlordDecision = 'approved') AND (leaseEnd <> null) AND (tenancyEndedAt = null) AND (residenceCount = 0))) AS violating
`, readinessOptionalMatch, readinessWithItems, maxBgcheckRetries, maxPaymentRetries)

// applicantOnboardingSpec is the identity-anchored onboarding convergence
// cypher — one row per applicant, whatever number of applications they hold.
//
// It anchors on the identity (a required MATCH, `{key: $actorKey}` — the
// identityAnchors shape), OPTIONAL-walks INBOUND to every application that
// names it (`(id)<-[:applicationFor]-(app:leaseapp)`, Contract #1 direction: the
// later-arriving leaseapp is the source), OPTIONAL-walks each application's unit,
// and OPTIONAL-walks the identity's own open user tasks.
//
//   - missing_onboarding — the applicant has recorded no PII (no `.ssn` aspect)
//     AND holds AT LEAST ONE application that still needs it. `ssnVal` reads
//     `id.ssn.data` (the WHOLE aspect body, never `.data.value`: step 6.5
//     replaces a sensitive aspect's entire `data` field with its ciphertext
//     envelope, so a `.value` hop resolves null for every real ssn — the same
//     presence-only read leaseApplicationCompleteSpec documents). The
//     per-application half is the SAME gate leaseApplicationCompleteSpec applies
//     to its four applicant gaps — `(unitKey <> null) AND (tenancyEndedAt =
//     null) AND ((unitStatus <> 'leased') OR (landlordDecision = 'approved'))
//     AND (landlordDecision <> 'lost')` — so an applicant whose only
//     applications are terminal (the unit tombstoned, leased to a rival, a
//     recorded loss, or a lease whose term has ended) stops
//     being asked for their SSN, exactly as the per-application target already
//     stops asking. It is evaluated INSIDE the count CASE, per application, so a
//     person with one live and one dead application still converges on the live
//     one.
//   - inflight_onboarding — the §10.3 dispatch-suppression companion, the same
//     predicate leaseApplicationCompleteSpec carries: an OPEN task bound through
//     forOperation to RecordIdentityPII. Here the task hangs off the ANCHOR
//     itself (the onboarding userTask has always been scoped to the applicant),
//     so no neighbour hop is needed. Discriminating on op.data.operationType is
//     what keeps an unrelated open task on the same person from parking a gap it
//     cannot close. No maxretries_onboarding companion: this is a userTask gap,
//     whose duplicate dispatch is prevented at the source by the claimId-stable
//     artifact id, and the installer's §10.3 companion-pair rule covers the
//     external-class actions only.
//   - violating is the single gap (this target has exactly one), spelled out
//     rather than implied — Contract #10 §10.2: violating is lens-projected.
//
// Both aggregates are `count(DISTINCT CASE WHEN … THEN <key> ELSE null END)`
// over OPTIONAL MATCHes carrying NO filtering WHERE beyond the task's own status
// — the fan-collapse idiom leaseApplicationCompleteSpec uses for its providedTo
// fan. A filtering WHERE that removes the only match collapses the upstream
// anchor to null in the grouped projection, and DISTINCT on each fan's own keys
// collapses the cross product between the application fan and the task fan, so
// the row count stays exactly one per identity (the §0.C
// guardOutputKeyCollision guard).
//
// entityKey and applicant are the same value here — the anchor IS the applicant
// — projected under both names so the row reads like every other convergence
// row: `applicant` is the §10.8 template the playbook's triggerLoom Subject
// names (row.applicant, unchanged from where it was moved), `entityKey` is the
// row's own entity for anything reading the target generically.
//
// STALENESS, deliberately accepted. The unit sits TWO hops from this anchor
// (identity ← leaseapp → unit), so a unit's `.listing.status` flip reprojects
// this row through the pipeline's pattern-directed anchor derivation
// (internal/refractor/pipeline/anchor_derivation.go walks the compiled pattern's
// own hops, and the ActorEnumerator BFS fallback is undirected) rather than
// through a one-hop neighbour rule. The gap's CLOSING fact — the applicant's own
// `.ssn` write — is zero hops from the anchor and needs none of that.
//
// '= null' (not IS NULL) is the full engine's null test.
const applicantOnboardingSpec = `
MATCH (id:identity {key: $actorKey})
OPTIONAL MATCH (id)<-[:applicationFor]-(app:leaseapp)
OPTIONAL MATCH (app)-[:appliesToUnit]->(u:unit)
OPTIONAL MATCH (id)<-[:scopedTo]-(onbTask:task)
  WHERE onbTask.data.status = 'open'
OPTIONAL MATCH (onbTask)-[:forOperation]->(onbOp:meta)
WITH
  id.key AS entityKey,
  id.ssn.data AS ssnVal,
  count(DISTINCT CASE WHEN (u.key <> null) AND (app.tenancy.data.endedAt = null) AND ((u.listing.data.status <> 'leased') OR (app.decision.data.value = 'approved')) AND (app.decision.data.value <> 'lost') THEN app.key ELSE null END) AS onboardingApps,
  count(DISTINCT CASE WHEN onbOp.data.operationType = 'RecordIdentityPII' THEN onbTask.key ELSE null END) AS onbTaskOpen
RETURN
  entityKey AS actorKey,
  entityKey,
  entityKey AS applicant,
  ((ssnVal = null) AND (onboardingApps > 0)) AS missing_onboarding,
  (onbTaskOpen > 0)                          AS inflight_onboarding,
  ((ssnVal = null) AND (onboardingApps > 0)) AS violating
`

// staleUserTasksSpec is the TASK-anchored convergence cypher for
// staleUserTasks (Lenses(), above): one row per open task, whatever operation
// granted it. The three OPTIONAL MATCHes are label-typed (identity / leaseapp
// / renewal), never a bare untyped node — a task's operationType pins exactly
// which vertex type its scopedTo neighbor is (RecordIdentityPII → identity,
// SignLease / SetApplicantProfile → leaseapp, SetRenewalTerms /
// VerifyGuarantor / SignRenewal → renewal), so at most one label's facts are
// ever non-null for a given row; the other OPTIONAL MATCHes simply fail to
// match and project null, the same label-gated idiom appliesToUnit/renews use
// elsewhere in this file.
//
// The six closure predicates are the SAME facts this package's other
// convergence lenses already read to flip their own missing_* columns:
//   - RecordIdentityPII:   id.ssn.data present (applicantOnboardingSpec's ssnVal)
//   - SignLease:           app.signature.data.signedAt present (leaseApplicationCompleteSpec's signedAt)
//   - SetApplicantProfile: app.applicationSignals.data.submittedAt present
//     (renewalComplete's signalsSubmittedAt — the applicant may submit the
//     profile from their own application card under their scope=self grant,
//     or an operator from the console, routes the §10.7 task-path
//     auto-complete never sees). Any open SetApplicantProfile task is
//     retired the instant a profile exists: the arm reads presence, not a
//     re-submission request, and no producer asks for one today.
//   - SetRenewalTerms:     rn.terms.data.setAt present (renewalComplete's termsSetAt goal column)
//   - VerifyGuarantor:     rn.guarantorVerification.data.verifiedAt present
//   - SignRenewal:         rn.renewalSignature.data.signedAt present — a
//     renewal episode re-minted while an earlier signing task is still open
//     (a revoke+enable, or a leg advance after the earlier task's grant) puts
//     two SignRenewal tasks in the tenant's inbox; whichever signs, the
//     other is obsolete. OR the renewed leaseapp (one hop, renewal-[:renews]->
//     leaseapp) carries a .notice or a .tenancy.endedAt: SignRenewal refuses
//     NoticeGiven / TenancyEnded off exactly those two facts (renewal_scripts.go)
//     and renewalComplete's open gate closes on the same two, so a signing
//     task minted before either landed is an offer the op will refuse.
//
// The status='open' gate excludes an already complete/cancelled task (nothing
// left to converge — orphanedTaskGrantsSpec's own reasoning, orchestration-base
// lenses.go); an operationType this package doesn't dispatch as a userTask (a
// wholly unrelated op scoped here by coincidence) matches none of the six arms
// and stays missing_cancellation false forever, never mistaken for a closed gap.
const staleUserTasksSpec = `
MATCH (t:task {key: $actorKey})
  WHERE t.data.status = 'open'
OPTIONAL MATCH (t)-[:forOperation]->(op:meta)
OPTIONAL MATCH (t)-[:scopedTo]->(onbIdentity:identity)
OPTIONAL MATCH (t)-[:scopedTo]->(sigApp:leaseapp)
OPTIONAL MATCH (t)-[:scopedTo]->(termsRenewal:renewal)
OPTIONAL MATCH (termsRenewal)-[:renews]->(renewedApp:leaseapp)
WITH
  t.key AS entityKey,
  op.data.operationType AS opType,
  onbIdentity.ssn.data AS onbSsn,
  sigApp.signature.data.signedAt AS sigSignedAt,
  sigApp.applicationSignals.data.submittedAt AS sigSignalsAt,
  termsRenewal.terms.data.setAt AS termsSetAt,
  termsRenewal.guarantorVerification.data.verifiedAt AS guarantorVerifiedAt,
  termsRenewal.renewalSignature.data.signedAt AS renewalSignedAt,
  renewedApp.notice.data.moveOutAt AS renewedNoticeAt,
  renewedApp.tenancy.data.endedAt AS renewedTenancyEndedAt
RETURN
  entityKey AS actorKey,
  entityKey,
  nanoIdFromKey(entityKey) AS entityId,
  entityKey AS taskKey,
  (((opType = 'RecordIdentityPII') AND (onbSsn <> null)) OR
   ((opType = 'SignLease') AND (sigSignedAt <> null)) OR
   ((opType = 'SetApplicantProfile') AND (sigSignalsAt <> null)) OR
   ((opType = 'SetRenewalTerms') AND (termsSetAt <> null)) OR
   ((opType = 'VerifyGuarantor') AND (guarantorVerifiedAt <> null)) OR
   ((opType = 'SignRenewal') AND ((renewalSignedAt <> null) OR (renewedNoticeAt <> null) OR (renewedTenancyEndedAt <> null)))) AS missing_cancellation,
  (((opType = 'RecordIdentityPII') AND (onbSsn <> null)) OR
   ((opType = 'SignLease') AND (sigSignedAt <> null)) OR
   ((opType = 'SetApplicantProfile') AND (sigSignalsAt <> null)) OR
   ((opType = 'SetRenewalTerms') AND (termsSetAt <> null)) OR
   ((opType = 'VerifyGuarantor') AND (guarantorVerifiedAt <> null)) OR
   ((opType = 'SignRenewal') AND ((renewalSignedAt <> null) OR (renewedNoticeAt <> null) OR (renewedTenancyEndedAt <> null)))) AS violating
`

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
//     same display columns leaseApplicationComplete projects.
//   - profile_submitted / income_to_rent_met / employment_verified /
//     reference_count / has_co_applicant / has_guarantor /
//     guarantor_income_to_rent_met mirror landlordLeaseApplicationsReadSpec's own
//     D1.5 addition: pure `app.applicationSignals.data.*` scalar hops, zero duplication.
//   - missing_onboarding / missing_bgcheck / missing_payment / missing_signature /
//     missing_decision / inflight_bgcheck / inflight_payment / declined_bgcheck /
//     declined_payment / declined are the applicant's own four-gate journey
//     state, the FE stepper reads (renderApplicationCard, loftspace-app/web/app.js).
//     Sourced the same way landlordLeaseApplicationsReadSpec's `qualified` column
//     is: the readinessOptionalMatch / readinessWithItems fragment shared with
//     leaseApplicationCompleteSpec (ssnVal / freshBgComplete / payComplete), plus
//     the per-gap inflight/failed counts over the same `inst` binding — the exact
//     bgInflight/payInflight/bgFailed/payFailed formulas leaseApplicationComplete
//     already runs. The service-instance convergence aggregate that drives
//     Weaver's own dispatch/orchestration (dispatch-count vs. maxretries_<gap>,
//     the "exhausted" state) stays Weaver-internal §10.2 state — not a lens
//     predicate (see readinessOptionalMatch's doc comment) — so this lens's
//     declined_<gap> reflects a terminal verification failure, never a
//     retry-budget exhaustion. Each of the four gaps plus missing_decision
//     also carries the same ((unitStatus <> 'leased') OR (landlordDecision =
//     'approved')) AND (landlordDecision <> 'lost') terms
//     leaseApplicationCompleteSpec's RETURN uses, so a losing rival's own
//     stepper stops rendering "To do" steps for a unit that has already leased
//     to someone else — and stays stopped after that unit relists, because the
//     loss is recorded on the application — matching what Weaver stops
//     dispatching, while the WINNING applicant's own stepper still reflects a
//     later bgcheck freshness lapse on their now-leased unit (the
//     landlordDecision='approved' escape hatch; see
//     leaseApplicationCompleteSpec's doc comment).
//   - lost_to_rival names that terminal state for the losing rival explicitly.
//     It reads the RECORDED fact first — .decision.value = 'lost', written by
//     RecordApplicationLoss once the unit leased to a sibling with no decision
//     on this application — so it holds across the unit's later relist (an
//     ended tenancy flips unitStatus back to 'available'; the rival stays
//     lost). It ORs in the live premise, (unitStatus = 'leased') AND
//     (landlordDecision = null), so the banner is honest in the seconds
//     between the unit leasing and Weaver recording the loss. A declined
//     application is not "lost" (it was decided) and an approved one is the
//     winner, so neither reads true here. The card's banner reads it ahead of
//     the stepper's closed gaps, which would otherwise render as "In review".
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
//   - notice_move_out_at / notice_given_at / notice_given_by — the lease's
//     recorded notice (.notice, written once by GiveNotice), the same aspect
//     the tenancyEnd lens ends the term on and SignRenewal refuses NoticeGiven
//     off; null on every lease without one. The card offers "Give notice" on
//     a live, signed tenancy with none and says "moving out <date>" once one
//     is recorded. Both application read lenses project the three columns.
//   - doc_store_name / doc_filename / doc_content_type — the ANCHORED
//     executed-lease artifact's pointers, the columns the app's GET
//     /api/lease-document streams by (ObjectGet under RLS). Projected ONLY when
//     the signedLease attachment exists: the max(CASE …) gate requires BOTH the
//     attachment object ((app)<-[:signedLease]-(leaseDocObj:object)) AND the
//     completed docGen outcome ((app)<-[:providedTo]-(docInst:service)) on the
//     same pre-aggregation combination row, so an un-anchored (still-converging)
//     document projects null and the GET answers "being generated." The
//     aggregation forces the lens's first WITH: every other RETURN column is
//     re-extracted as a WITH-passthrough alias first (the
//     landlordLeaseApplicationsRead precedent). The attach LINK event reprojects
//     this plain lens (the link fan-out seeds from both endpoints and the
//     leaseapp endpoint is the anchor), so the pointers land the moment the
//     artifact anchors.
//
// '= null' / '<> null' is the full engine's null test (not IS NULL); list
// literals + nanoIdFromKey in RETURN are the cap-read base lens's proven shape.
var leaseApplicationsReadSpec = fmt.Sprintf(`
MATCH (app:leaseapp)
MATCH (app)-[:applicationFor]->(id:identity)
OPTIONAL MATCH (app)-[:appliesToUnit]->(u:unit)
OPTIONAL MATCH (app)<-[:providedTo]-(docInst:service)
OPTIONAL MATCH (app)<-[:signedLease]-(leaseDocObj:object)
OPTIONAL MATCH (app)<-[:forCandidate]-(prop:augurproposal)
%s
WITH
  app.key                        AS entityKey,
  id.key                         AS applicantKey,
  u.key                          AS unitKey,
  u.address.data.line1           AS unitAddress,
  u.address.data.city            AS unitCity,
  u.address.data.region          AS unitRegion,
  u.listing.data.rentAmount      AS unitRent,
  u.listing.data.rentCurrency    AS unitCurrency,
  u.listing.data.status          AS unitStatus,
  u.listing.data.bedrooms        AS unitBedrooms,
  u.listing.data.bathrooms       AS unitBathrooms,
  u.listing.data.availableFrom   AS unitAvailableFrom,
  u.listing.data.leaseTermMonths AS unitLeaseTermMonths,
  app.signature.data.signedAt    AS signedAt,
  app.decision.data.value        AS landlordDecision,
  app.decision.data.reason       AS declineReason,
  app.terms.data.moveInDate      AS termsMoveInDate,
  app.terms.data.leaseTermMonths AS termsLeaseTermMonths,
  app.terms.data.requestedRent   AS termsRequestedRent,
  app.tenancy.data.leaseStart    AS tenancyLeaseStart,
  app.tenancy.data.leaseEnd      AS tenancyLeaseEnd,
  app.tenancy.data.termStart     AS tenancyTermStart,
  app.tenancy.data.rentAmount    AS tenancyRentAmount,
  app.tenancy.data.endedAt       AS tenancyEndedAt,
  app.deposit.data.amount        AS depositAmount,
  app.notice.data.moveOutAt      AS noticeMoveOutAt,
  app.notice.data.givenAt        AS noticeGivenAt,
  app.notice.data.givenBy        AS noticeGivenBy,
  (app.applicationSignals.data.submittedAt <> null)      AS profileSubmitted,
  app.applicationSignals.data.incomeToRentMet            AS incomeToRentMet,
  app.applicationSignals.data.employmentVerified         AS employmentVerified,
  app.applicationSignals.data.referenceCount             AS referenceCount,
  app.applicationSignals.data.hasCoApplicant             AS hasCoApplicant,
  app.applicationSignals.data.hasGuarantor               AS hasGuarantor,
  app.applicationSignals.data.guarantorIncomeToRentMet   AS guarantorIncomeToRentMet,
  max(CASE WHEN leaseDocObj.key <> null AND docInst.class = 'service.docGen.instance' AND docInst.outcome.data.status = 'completed' THEN docInst.outcome.data.storeName ELSE null END) AS docStoreName,
  max(CASE WHEN leaseDocObj.key <> null AND docInst.class = 'service.docGen.instance' AND docInst.outcome.data.status = 'completed' THEN docInst.outcome.data.filename ELSE null END) AS docFilename,
  max(CASE WHEN leaseDocObj.key <> null AND docInst.class = 'service.docGen.instance' AND docInst.outcome.data.status = 'completed' THEN docInst.outcome.data.contentType ELSE null END) AS docContentType,
  %s,
  count(DISTINCT CASE WHEN inst.class = 'service.backgroundCheck.instance' AND inst.dispatch.data.vendorRef <> null AND inst.outcome.data.status = null THEN inst.key ELSE null END) AS bgInflight,
  count(DISTINCT CASE WHEN inst.class = 'service.payment.instance' AND inst.dispatch.data.vendorRef <> null AND inst.outcome.data.status = null THEN inst.key ELSE null END) AS payInflight,
  count(DISTINCT CASE WHEN inst.class = 'service.backgroundCheck.instance' AND inst.outcome.data.status = 'failed' THEN inst.key ELSE null END) AS bgFailed,
  count(DISTINCT CASE WHEN inst.class = 'service.payment.instance' AND inst.outcome.data.status = 'failed' THEN inst.key ELSE null END) AS payFailed,
  count(DISTINCT CASE WHEN prop.gap.data.gapColumn = 'missing_bgcheck' THEN prop.key ELSE null END) AS bgEscalated,
  count(DISTINCT CASE WHEN prop.gap.data.gapColumn = 'missing_payment' THEN prop.key ELSE null END) AS payEscalated
RETURN
  nanoIdFromKey(entityKey)       AS app_id,
  entityKey                      AS entity_key,
  applicantKey                   AS applicant,
  unitKey                        AS unit_key,
  unitAddress                    AS unit_address,
  unitCity                       AS unit_city,
  unitRegion                     AS unit_region,
  unitRent                       AS unit_rent,
  unitCurrency                   AS unit_currency,
  unitStatus                     AS unit_status,
  unitBedrooms                   AS unit_bedrooms,
  unitBathrooms                  AS unit_bathrooms,
  unitAvailableFrom              AS unit_available_from,
  unitLeaseTermMonths            AS unit_lease_term_months,
  signedAt                       AS signed_at,
  landlordDecision               AS landlord_decision,
  declineReason                  AS decline_reason,
  termsMoveInDate                AS terms_move_in_date,
  termsLeaseTermMonths           AS terms_lease_term_months,
  termsRequestedRent             AS terms_requested_rent,
  tenancyLeaseStart              AS tenancy_lease_start,
  tenancyLeaseEnd                AS tenancy_lease_end,
  tenancyTermStart               AS tenancy_term_start,
  tenancyRentAmount              AS tenancy_rent_amount,
  tenancyEndedAt                 AS tenancy_ended_at,
  depositAmount                  AS deposit_amount,
  noticeMoveOutAt                AS notice_move_out_at,
  noticeGivenAt                  AS notice_given_at,
  noticeGivenBy                  AS notice_given_by,
  profileSubmitted                AS profile_submitted,
  incomeToRentMet                 AS income_to_rent_met,
  employmentVerified               AS employment_verified,
  referenceCount                   AS reference_count,
  hasCoApplicant                   AS has_co_applicant,
  hasGuarantor                     AS has_guarantor,
  guarantorIncomeToRentMet         AS guarantor_income_to_rent_met,
  docStoreName                   AS doc_store_name,
  docFilename                    AS doc_filename,
  docContentType                 AS doc_content_type,
  ((unitKey <> null) AND (tenancyEndedAt = null) AND (ssnVal = null) AND ((unitStatus <> 'leased') OR (landlordDecision = 'approved')) AND (landlordDecision <> 'lost'))                                  AS missing_onboarding,
  ((unitKey <> null) AND (tenancyEndedAt = null) AND (ssnVal <> null) AND (freshBgComplete = 0) AND ((unitStatus <> 'leased') OR (landlordDecision = 'approved')) AND (landlordDecision <> 'lost'))      AS missing_bgcheck,
  ((unitKey <> null) AND (tenancyEndedAt = null) AND (payComplete = 0) AND ((unitStatus <> 'leased') OR (landlordDecision = 'approved')) AND (landlordDecision <> 'lost'))                                 AS missing_payment,
  ((unitKey <> null) AND (tenancyEndedAt = null) AND (signedAt = null) AND ((unitStatus <> 'leased') OR (landlordDecision = 'approved')) AND (landlordDecision <> 'lost'))                                 AS missing_signature,
  ((ssnVal <> null) AND (freshBgComplete > 0) AND (payComplete > 0) AND (signedAt <> null) AND (landlordDecision = null) AND (unitStatus <> 'leased')) AS missing_decision,
  (bgInflight > 0)                                  AS inflight_bgcheck,
  (payInflight > 0)                                 AS inflight_payment,
  ((bgFailed > 0) AND (freshBgComplete = 0))        AS declined_bgcheck,
  ((payFailed > 0) AND (payComplete = 0))           AS declined_payment,
  (((bgFailed > 0) AND (freshBgComplete = 0)) OR ((payFailed > 0) AND (payComplete = 0)) OR (landlordDecision = 'declined')) AS declined,
  (bgEscalated > 0)                                 AS escalated_bgcheck,
  (payEscalated > 0)                                AS escalated_payment,
  ((landlordDecision = 'lost') OR ((unitKey <> null) AND (unitStatus = 'leased') AND (landlordDecision = null))) AS lost_to_rival,
  [nanoIdFromKey(applicantKey)]  AS authz_anchors
`, readinessOptionalMatch, readinessWithItems)

// landlordLeaseApplicationsReadSpec is the LANDLORD-facing protected Postgres
// read model's cypher (D1.3 Increment 2). Identical display surface to
// leaseApplicationsReadSpec, but anchored on the managing landlord rather than
// the applicant — every MATCH is REQUIRED so a row exists only for a well-formed
// application whose unit has a manager.
//
//   - It anchors on every leaseapp, requires the applicationFor walk to the
//     applicant identity (display) AND the appliesToUnit walk to the unit AND the
//     INBOUND `manages` walk from the unit to its managing landlord
//     ((u)<-[:manages]-(landlord:identity), the inbound-traversal form the
//     convergence lens already uses for providedTo). A leaseapp whose unit has no
//     manager projects NO row — fail-closed, no null anchor.
//   - app_id (the first IntoKey column) is the application's bare NanoID;
//     landlord_id (the second) is the managing landlord's bare NanoID. The
//     composite (app_id, landlord_id) keys the row so a co-managed unit's
//     leaseapp fans out to one row per landlord with no key collision.
//   - authz_anchors = [nanoIdFromKey(landlord.key)] — the managing-landlord
//     anchor. The primordial cap-read self-grant grants the landlord their own
//     NanoID, so the §6.14 set-membership RLS policy returns this row to the
//     managing landlord and to nobody else. landlord_key keeps the full
//     vtx.identity.<id> key as a display/scope body column.
//   - profile_submitted / income_to_rent_met / employment_verified /
//     reference_count / has_co_applicant / has_guarantor /
//     guarantor_income_to_rent_met (D1.5 Rec C, decision-surface-design.md
//     §4/§5) are pure scalar hops off app.applicationSignals.data.* — the SAME derived signals
//     leaseApplicationCompleteSpec projects.
//   - qualified (D1.5 Rec-C remainder, decision-surface-design.md §4 Option A —
//     the readiness clone) is the SAME formula leaseApplicationCompleteSpec's
//     applicantApproved derives, sharing the readinessOptionalMatch /
//     readinessWithItems cypher fragment with that lens so the two projections
//     cannot drift. This introduces the lens's first WITH/aggregation: every
//     other RETURN column is re-extracted as a WITH-passthrough alias first
//     (including the three map-valued secure envelope columns below — the full
//     engine's WITH carries a map value through a non-aggregating passthrough
//     unmodified, see executor.go applyWith/normalizeForKey, and the
//     Secure-Lens decryptor resolves a column purely by its RETURN alias name,
//     so it is indifferent to whether that alias is a direct RETURN hop or a
//     WITH-carried one). Approve is still gated by the trusted console's own
//     copy of this same formula (applicantApproved) — this column lets the RLS
//     surface show the SAME gate without a second, weaver-targets-sourced read.
//   - lost_to_rival is leaseApplicationsReadSpec's column of the same name (the
//     recorded .decision = 'lost', OR the unit has leased and this application
//     carries no decision): the landlord's row for a losing rival reads "unit
//     leased to another applicant" instead of "awaiting your decision" — there
//     is no decision left for it to await, and the recorded loss keeps it that
//     way once the unit relists. The unit walk is REQUIRED here, so no unitKey
//     guard is needed.
//   - applicant_name / applicant_email / applicant_phone are SECURE columns
//     (see the Lenses() declaration): each RETURNs the applicant identity's
//     sensitive aspect envelope whole (id.<aspect>.data — ciphertext at rest;
//     there is no plaintext `value` field to hop into), and the Secure-Lens
//     decryptor rewrites it to the decrypted `value` before the row reaches
//     the RLS-protected adapter. Unlike applicantRosterRead, NO WHERE keys on
//     ciphertext presence: an application from an identity missing an aspect
//     (or shredded) must still project a row — the contact columns are
//     display enrichment, never a row gate.
//   - doc_store_name / doc_filename / doc_content_type project the SAME
//     anchored executed-lease artifact leaseApplicationsRead carries, via the
//     identical walk + max(CASE …) gate (both the completed docGen outcome
//     off (app)<-[:providedTo]-(docInst:service) AND the signedLease
//     attachment off (app)<-[:signedLease]-(leaseDocObj:object) — see that
//     spec's own doc comment). Null until the document has anchored.
//
// '= null' / list literals + nanoIdFromKey in RETURN mirror leaseApplicationsRead.
var landlordLeaseApplicationsReadSpec = fmt.Sprintf(`
MATCH (app:leaseapp)
MATCH (app)-[:applicationFor]->(id:identity)
MATCH (app)-[:appliesToUnit]->(u:unit)
MATCH (u)<-[:manages]-(landlord:identity)
OPTIONAL MATCH (app)<-[:providedTo]-(docInst:service)
OPTIONAL MATCH (app)<-[:signedLease]-(leaseDocObj:object)
%s
WITH
  u,
  app.key                        AS entityKey,
  id.key                         AS applicantKey,
  landlord.key                   AS landlordKey,
  u.key                          AS unitKey,
  u.address.data.line1           AS unitAddress,
  u.address.data.city            AS unitCity,
  u.address.data.region          AS unitRegion,
  u.listing.data.rentAmount      AS unitRent,
  u.listing.data.rentCurrency    AS unitCurrency,
  u.listing.data.status          AS unitStatus,
  u.listing.data.availableFrom   AS unitAvailableFrom,
  u.listing.data.leaseTermMonths AS unitLeaseTermMonths,
  app.signature.data.signedAt    AS signedAt,
  app.decision.data.value        AS landlordDecision,
  app.decision.data.reason       AS declineReason,
  app.terms.data.moveInDate      AS termsMoveInDate,
  app.terms.data.leaseTermMonths AS termsLeaseTermMonths,
  app.terms.data.requestedRent   AS termsRequestedRent,
  app.tenancy.data.leaseStart    AS tenancyLeaseStart,
  app.tenancy.data.leaseEnd      AS tenancyLeaseEnd,
  app.tenancy.data.termStart     AS tenancyTermStart,
  app.tenancy.data.rentAmount    AS tenancyRentAmount,
  app.tenancy.data.endedAt       AS tenancyEndedAt,
  app.deposit.data.amount        AS depositAmount,
  app.notice.data.moveOutAt      AS noticeMoveOutAt,
  app.notice.data.givenAt        AS noticeGivenAt,
  app.notice.data.givenBy        AS noticeGivenBy,
  (app.applicationSignals.data.submittedAt <> null)      AS profileSubmitted,
  app.applicationSignals.data.incomeToRentMet            AS incomeToRentMet,
  app.applicationSignals.data.employmentVerified         AS employmentVerified,
  app.applicationSignals.data.referenceCount             AS referenceCount,
  app.applicationSignals.data.hasCoApplicant             AS hasCoApplicant,
  app.applicationSignals.data.hasGuarantor               AS hasGuarantor,
  app.applicationSignals.data.guarantorIncomeToRentMet   AS guarantorIncomeToRentMet,
  max(CASE WHEN leaseDocObj.key <> null AND docInst.class = 'service.docGen.instance' AND docInst.outcome.data.status = 'completed' THEN docInst.outcome.data.storeName ELSE null END) AS docStoreName,
  max(CASE WHEN leaseDocObj.key <> null AND docInst.class = 'service.docGen.instance' AND docInst.outcome.data.status = 'completed' THEN docInst.outcome.data.filename ELSE null END) AS docFilename,
  max(CASE WHEN leaseDocObj.key <> null AND docInst.class = 'service.docGen.instance' AND docInst.outcome.data.status = 'completed' THEN docInst.outcome.data.contentType ELSE null END) AS docContentType,
  id.name.data                   AS applicantNameEnv,
  id.email.data                  AS applicantEmailEnv,
  id.phone.data                  AS applicantPhoneEnv,
  %s
RETURN
  nanoIdFromKey(entityKey)       AS app_id,
  nanoIdFromKey(landlordKey)     AS landlord_id,
  entityKey                      AS entity_key,
  applicantKey                   AS applicant,
  landlordKey                    AS landlord_key,
  unitKey                        AS unit_key,
  unitAddress                    AS unit_address,
  unitCity                       AS unit_city,
  unitRegion                     AS unit_region,
  unitRent                       AS unit_rent,
  unitCurrency                   AS unit_currency,
  unitStatus                     AS unit_status,
  unitAvailableFrom              AS unit_available_from,
  unitLeaseTermMonths            AS unit_lease_term_months,
  signedAt                       AS signed_at,
  landlordDecision               AS landlord_decision,
  declineReason                  AS decline_reason,
  termsMoveInDate                AS terms_move_in_date,
  termsLeaseTermMonths           AS terms_lease_term_months,
  termsRequestedRent             AS terms_requested_rent,
  tenancyLeaseStart              AS tenancy_lease_start,
  tenancyLeaseEnd                AS tenancy_lease_end,
  tenancyTermStart               AS tenancy_term_start,
  tenancyRentAmount              AS tenancy_rent_amount,
  tenancyEndedAt                 AS tenancy_ended_at,
  depositAmount                  AS deposit_amount,
  noticeMoveOutAt                AS notice_move_out_at,
  noticeGivenAt                  AS notice_given_at,
  noticeGivenBy                  AS notice_given_by,
  profileSubmitted               AS profile_submitted,
  incomeToRentMet                AS income_to_rent_met,
  employmentVerified              AS employment_verified,
  referenceCount                  AS reference_count,
  hasCoApplicant                  AS has_co_applicant,
  hasGuarantor                    AS has_guarantor,
  guarantorIncomeToRentMet        AS guarantor_income_to_rent_met,
  docStoreName                    AS doc_store_name,
  docFilename                     AS doc_filename,
  docContentType                  AS doc_content_type,
  applicantNameEnv                AS applicant_name,
  applicantEmailEnv               AS applicant_email,
  applicantPhoneEnv               AS applicant_phone,
  [nanoIdFromKey(landlordKey)] + [(u)-[:containedIn]->(b:building) | nanoIdFromKey(b.key)] AS authz_anchors,
  ((ssnVal <> null) AND (freshBgComplete > 0) AND (payComplete > 0) AND (signedAt <> null)) AS qualified,
  ((landlordDecision = 'lost') OR ((unitStatus = 'leased') AND (landlordDecision = null))) AS lost_to_rival
`, readinessOptionalMatch, readinessWithItems)
