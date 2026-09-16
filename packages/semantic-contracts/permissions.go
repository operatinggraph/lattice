package semanticcontracts

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// OpMetas declares the op-meta vertices that make ops forOperation-resolvable.
//
//   - CreateClause, SupersedeClause, BackfillClauseTerm — declared for
//     uniform discoverability (functionally optional; none is ever an
//     assignTask target itself).
//   - InspectPremises — REQUIRED: the assignTask operation the §10.8
//     playbook's missing_inspection gap binds; the Weaver Actuator resolves
//     forOperation to its op-meta when it creates the remediation Task
//     (the SignLease precedent). Its absence would break the gap.
//   - ShortenClauseTerm carries a full descriptor (the lease-signing
//     EndTenancy/RecordApplicationLoss shape: Presentation + InputSchema +
//     Dispatch) rather than a bare declaration — it is, like those two, a
//     Weaver-service-actor directOp with a by-hand operator escape hatch (the
//     CLI under the primordial admin), and the descriptor is what lets that
//     escape hatch and any other descriptor-driven submitter declare the
//     same reads the leaseRentSettlement playbook's missing_termShortened
//     gap routes (targets.go): the clause and its .terms (REQUIRED — the gap
//     only opens on an already-termed clause), the lease and its .notice
//     (REQUIRED — the gap only opens once one is recorded), and the clause's
//     .status (OPTIONAL — absence-tolerant, same as BackfillClauseTerm's).
func OpMetas() []pkgmgr.OpMetaSpec {
	return []pkgmgr.OpMetaSpec{
		{OperationType: "CreateClause"},
		{OperationType: "InspectPremises"},
		{OperationType: "SupersedeClause"},
		{OperationType: "BackfillClauseTerm"},
		{
			OperationType: "ShortenClauseTerm",
			Presentation: &pkgmgr.OpPresentationSpec{
				Title:       "Shorten a rent clause to a recorded move-out",
				ShortLabel:  "Shorten term",
				Description: "Cap a termed rent clause's end date at a tenant's recorded early move-out. Refused on an untermed clause or a lease with no recorded notice; a no-op once the term already ends at or before the move-out.",
				Icon:        "clipboard",
				Tone:        "primary",
				SubmitLabel: "Shorten term",
				Group:       "Operator repairs",
			},
			InputSchema: `{"type":"object","properties":` +
				`{"clauseKey":{"type":"string","x-entityRef":"clause","description":"vtx.clause.<NanoID> of the already-termed monthly clause to shorten."},` +
				`"leaseAppKey":{"type":"string","x-entityRef":"leaseapp","description":"vtx.leaseapp.<NanoID> of the lease that recorded the move-out."}},` +
				`"required":["clauseKey","leaseAppKey"]}`,
			// refusal-courtesy(facet): NoNotice, NotTermed, InvalidState: none — no VisibleWhen or entity lens column projects a clause's term state or a lease's recorded notice; Facet offers Shorten term on every clause/lease row regardless.
			FieldDescriptions: map[string]string{
				"clauseKey":   "The termed monthly clause being shortened. Its .terms.validUntil is capped at max(the lease's .notice.moveOutAt, validFrom); a clause with no term yet is refused (NotTermed).",
				"leaseAppKey": "The lease whose .notice.moveOutAt is the shortening target. A lease with no recorded notice is refused (NoNotice); the clause must govern this exact lease (InvalidState otherwise).",
			},
			Dispatch: &pkgmgr.OpDispatchSpec{
				Class:       "clause",
				AuthContext: "standing",
				TargetField: "clauseKey",
				TargetType:  "clause",
				Reads: []string{
					"{payload.clauseKey}",
					"{payload.clauseKey}.terms",
					"{payload.leaseAppKey}",
					"{payload.leaseAppKey}.notice",
				},
				OptionalReads: []string{
					"{payload.clauseKey}.status",
				},
				Enumerations: []pkgmgr.EnumerationSpec{
					{Hub: "{payload.clauseKey}", Relation: "governs", Direction: "out"},
				},
			},
		},
	}
}

// Permissions returns the package's permission vertices + grants. The
// trusted-tool app submits CreateClause when a landlord/operator installs a
// bespoke provision on a signed lease — the same operator-grant idiom
// loftspace-ledger's LoftspaceCreateAccount uses. SupersedeClause (self-amendment),
// BackfillClauseTerm and ShortenClauseTerm (the leaseRentSettlement
// playbook's missing_term / missing_termShortened directOps, submitted by
// Weaver's service actor, which holds operator) are the same operator-grant
// idiom.
func Permissions() []pkgmgr.PermissionSpec {
	return []pkgmgr.PermissionSpec{
		{
			OperationType: "CreateClause",
			Scope:         "any",
			Note:          "Grants the operator the right to submit CreateClause (installs a semantic-contract clause governing a lease and charging a ledger account, or a judgment clause assigning an inspector).",
			GrantsTo:      []string{"operator"},
		},
		{
			OperationType: "InspectPremises",
			Scope:         "any",
			Note:          "Grants the operator the right to submit InspectPremises; the assigned inspector performs it via the ephemeral task grant (§10.7) — same operator model as SignLease.",
			GrantsTo:      []string{"operator"},
		},
		{
			OperationType: "SupersedeClause",
			Scope:         "any",
			Note:          "Grants the operator the right to submit SupersedeClause (Fire V4 self-amendment: replaces a clause with a new one, tombstoning the amended clause and linking the replacement).",
			GrantsTo:      []string{"operator"},
		},
		{
			OperationType: "BackfillClauseTerm",
			Scope:         "any",
			Note:          "Grants the operator the right to submit BackfillClauseTerm (stamps an untermed monthly clause's validFrom/validUntil from its lease's tenancy and moves its recorded due date onto the term's anniversary grid) — the leaseRentSettlement playbook's missing_term directOp, which Weaver's service actor submits.",
			GrantsTo:      []string{"operator"},
		},
		{
			OperationType: "ShortenClauseTerm",
			Scope:         "any",
			Note:          "Grants the operator the right to submit ShortenClauseTerm (caps an already-termed monthly clause's validUntil at a lease's recorded early move-out and completes it once its recorded due date reaches that cap) — the leaseRentSettlement playbook's missing_termShortened directOp, which Weaver's service actor submits; an operator may also run it by hand.",
			GrantsTo:      []string{"operator"},
		},
	}
}
