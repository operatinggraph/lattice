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
// NOTE — see README "Known seam: scalar convergence columns". The §10.2
// convergence row carries SCALAR columns (violating / missing_* bools,
// entityKey / applicant strings), but the actorAggregate projection
// EnvelopeFn realness-filters every BodyColumn to a LIST (it was built for the
// roster lenses my-tasks / capabilityEphemeral). A scalar column projects as
// [] through that path today — Weaver's boolColumn cannot read it. This lens
// declaration is correct and its cypher is proven one-row-per-anchor at the
// rule-engine level; the bucket round-trip of scalar columns needs a Refractor
// projection change (flagged, not made here — Q9).
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
				BodyColumns:      []string{"violating", "missing_onboarding", "missing_bgcheck", "missing_payment", "missing_signature", "applicant", "entityKey"},
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
//   - missing_bgcheck / missing_payment — no completed service instance of that
//     family providedTo the applicant. The family is discriminated by the
//     instance's .family aspect (read as a distinct aspect because the vertex
//     envelope `class` field shadows the .class aspect on the read path); the
//     completed test reads the .outcome aspect status. The replyOp writing the
//     .outcome aspect flips the matching gap false.
//   - missing_signature — the application has no .signature aspect. SignLease
//     writes it, flipping this false.
//
// violating is the explicit OR of the four gaps (Contract #10 §10.2: violating
// is lens-projected, not an implicit OR; for this target the natural rule is
// "any gap → violating").
//
// applicant + entityKey are the param columns the §10.8 playbook templates name
// (row.applicant, row.entityKey). They stay non-null even when gaps are open
// because no OPTIONAL MATCH carries a filtering WHERE.
//
// FRESHNESS (Phase-2): "a completed outcome exists" — NOT a rolling
// completedAt+window > now window. The full rule engine has no date arithmetic
// and the actorAggregate projection supplies only $now/$projectedAt (no window
// param), and the Starlark sandbox has no duration-add for the replyOp to
// precompute an expiresAt — so the rolling window is a Phase-3 refinement (see
// README). The replyOp records completedAt for provenance and that future use.
const leaseApplicationCompleteSpec = `
MATCH (app:leaseapp {key: $actorKey})
OPTIONAL MATCH (app)-[:applicationFor]->(id:identity)
OPTIONAL MATCH (id)<-[:providedTo]-(inst:service)
WITH
  app.key AS entityKey,
  id.key  AS applicant,
  app.signature.data.signedAt AS signedAt,
  id.ssn.data.value AS ssnVal,
  count(DISTINCT CASE WHEN inst.family.data.value = 'backgroundCheck' AND inst.outcome.data.status = 'completed' THEN inst.key ELSE null END) AS bgComplete,
  count(DISTINCT CASE WHEN inst.family.data.value = 'payment' AND inst.outcome.data.status = 'completed' THEN inst.key ELSE null END) AS payComplete
RETURN
  entityKey AS actorKey,
  entityKey,
  applicant,
  // The full engine's grammar has no IS NULL; '= null' is its null test
  // (ruleengine/full executor.go equalsAny treats null = null as true and any
  // value = null as false). Do not "correct" it to unsupported IS NULL.
  (ssnVal = null)    AS missing_onboarding,
  (bgComplete = 0)   AS missing_bgcheck,
  (payComplete = 0)  AS missing_payment,
  (signedAt = null)  AS missing_signature,
  ((ssnVal = null) OR (bgComplete = 0) OR (payComplete = 0) OR (signedAt = null)) AS violating
`
