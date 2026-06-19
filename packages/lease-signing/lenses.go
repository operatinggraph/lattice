package leasesigning

import "github.com/asolgan/lattice/internal/pkgmgr"

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
				BodyColumns:      []string{"violating", "missing_onboarding", "missing_bgcheck", "missing_payment", "missing_signature", "applicant", "entityKey", "freshUntil"},
				EmptyBehavior:    "delete",
				KeyColumn:        "entityId",
				Freshness:        "auto",
			},
		},
	}
}

// leaseApplicationCompleteSpec is the one-row-per-anchor convergence cypher.
//
// It anchors on the leaseapp candidate (a required MATCH), OPTIONAL-walks the
// applicationFor link to the applicant identity, and OPTIONAL-walks the
// applicant's providedTo service instances. Each gap is a per-anchor scalar:
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
// violating is the explicit OR of the four gaps (Contract #10 §10.2: violating
// is lens-projected, not an implicit OR; for this target the natural rule is
// "any gap → violating").
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
//   - The lens projects a single scalar freshUntil per anchor: the completed,
//     still-fresh bgcheck's validUntil. Weaver's temporal lane reads it
//     (freshUntilColumn) and schedules an @at one-shot at that instant; when the
//     timer fires it marks the row expired, the row reprojects, and the freshness
//     predicate re-opens missing_bgcheck the moment freshness lapses — eagerly,
//     not waiting for an incidental CDC touch.
//   - freshUntil is read by a DEDICATED family-filtered bgcheck OPTIONAL MATCH
//     (after the aggregation WITH) whose WHERE selects the completed, fresh
//     bgcheck. When no fresh bgcheck exists the WHERE filters every providedTo
//     neighbor and the executor null-restores the anchor with bg null, so
//     freshUntil projects as a genuine null (Weaver clears any standing @at — no
//     deadline to arm) and the anchor never drops. That null-restore is the
//     ruleengine/full executor.go applyMatch OPTIONAL MATCH ... WHERE semantics:
//     a fully-filtered optional preserves the source binding with nulls.
//   - The dedicated bgcheck match yields AT MOST ONE row per anchor: the vertical
//     dispatches at most one bgcheck per application (FR58), so at most one
//     completed-fresh bgcheck instance is providedTo the applicant. That keeps the
//     projection one-row-per-anchor (guardOutputKeyCollision stays satisfied).
//     The single no-WHERE providedTo fan above still drives the missing_* counts.
//
// '= null' (not IS NULL) is the full engine's null test (ruleengine/full
// executor.go equalsAny treats null = null as true and any value = null as
// false). Do not "correct" it to unsupported IS NULL.
const leaseApplicationCompleteSpec = `
MATCH (app:leaseapp {key: $actorKey})
OPTIONAL MATCH (app)-[:applicationFor]->(id:identity)
OPTIONAL MATCH (id)<-[:providedTo]-(inst:service)
WITH
  app.key AS entityKey,
  id      AS applicantNode,
  id.key  AS applicant,
  app.signature.data.signedAt AS signedAt,
  id.ssn.data.value AS ssnVal,
  count(DISTINCT CASE WHEN inst.family.data.value = 'backgroundCheck' AND inst.outcome.data.status = 'completed' AND inst.outcome.data.validUntil > $now THEN inst.key ELSE null END) AS freshBgComplete,
  count(DISTINCT CASE WHEN inst.family.data.value = 'payment' AND inst.outcome.data.status = 'completed' THEN inst.key ELSE null END) AS payComplete
OPTIONAL MATCH (applicantNode)<-[:providedTo]-(bg:service)
  WHERE bg.family.data.value = 'backgroundCheck' AND bg.outcome.data.status = 'completed' AND bg.outcome.data.validUntil > $now
RETURN
  entityKey AS actorKey,
  entityKey,
  applicant,
  bg.outcome.data.validUntil AS freshUntil,
  (ssnVal = null)        AS missing_onboarding,
  (freshBgComplete = 0)  AS missing_bgcheck,
  (payComplete = 0)      AS missing_payment,
  (signedAt = null)      AS missing_signature,
  ((ssnVal = null) OR (freshBgComplete = 0) OR (payComplete = 0) OR (signedAt = null)) AS violating
`
