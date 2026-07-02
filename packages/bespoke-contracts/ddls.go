package bespokecontracts

import "github.com/asolgan/lattice/internal/pkgmgr"

// DDLs returns the package's DDL meta-vertex declarations: `clause`
// (CreateClause) plus its three aspect-type declarations (clauseProse,
// clauseTerms, clauseStatus). clauseStatus permits DebitAccount too — the
// cross-package write loftspace-ledger's DebitAccount makes to mark a
// fixed/one-time clause completed (the objectLiveness → TombstoneObject
// precedent: a package's aspect DDL lists every op, in any package, that
// legitimately writes it).
func DDLs() []pkgmgr.DDLSpec {
	return []pkgmgr.DDLSpec{
		clauseDDL(),
		clauseProseAspectTypeDDL(),
		clauseTermsAspectTypeDDL(),
		clauseStatusAspectTypeDDL(),
	}
}

func clauseDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     "clause",
		Class:             "meta.ddl.vertexType",
		PermittedCommands: []string{"CreateClause"},
		Description: "Bespoke-contract clause DDL (Fire V1 — fixed/one-time computational archetype). Vertex " +
			"shape: vtx.clause.<NanoID>, class=clause, root data = {} (minimal, D5 — the provision text and terms " +
			"are aspects). CreateClause{leaseAppKey, accountKey, prose, amountCents} mints the clause under a " +
			"fresh NanoID, requiring the lease and the account both be live (no-orphan invariant). Writes the " +
			"governs link (clause→lease, the state this provision governs) and the chargesTo link " +
			"(clause→account, the ledger account it debits) — the clause is the later-arriving vertex on both, so " +
			"it is the source (Contract #1 §1.1). The .terms aspect fixes kind=computational, period=oneTime for " +
			"this fire; conditioned/judgment/recurring/proration archetypes are later increments (V2/V3 of the " +
			"design).",
		Script: clauseDDLScript,
		InputSchema: `{"type":"object","properties":` +
			`{"leaseAppKey":{"type":"string","description":"vtx.leaseapp.<NanoID> this clause governs (required, validated alive)."},` +
			`"accountKey":{"type":"string","description":"vtx.account.<NanoID> this clause charges (required, validated alive)."},` +
			`"prose":{"type":"string","description":"The human-readable provision text (the legal paragraph the signer agreed to); required, non-empty."},` +
			`"amountCents":{"type":"number","description":"The fixed one-time charge amount in integer cents; required, must be > 0."}},` +
			`"required":["leaseAppKey","accountKey","prose","amountCents"]}`,
		OutputSchema: `{"type":"object","properties":` +
			`{"primaryKey":{"type":"string","description":"vtx.clause.<NanoID> of the created clause (the operation's principal key)."}}}`,
		FieldDescription: map[string]string{
			"leaseAppKey": "Full vtx.leaseapp.<NanoID> key of the lease this clause governs. CreateClause validates it is alive and writes the governs link (clause→lease).",
			"accountKey":  "Full vtx.account.<NanoID> key of the ledger account this clause charges. CreateClause validates it is alive and writes the chargesTo link (clause→account); the account key also flows into the clauseSatisfaction lens as the directOp target.",
			"prose":       "The legal paragraph a signer agreed to. Stored verbatim on the .prose aspect; never interpreted — the machine terms are the separate .terms aspect.",
			"amountCents": "The fixed one-time charge amount in integer cents; required, must be a positive number. Stored on the .terms aspect and flows type-preserved into the DebitAccount directOp's amountCents param when the clause is unsatisfied.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name: "CreateClause — a one-time lockout fee",
				Payload: map[string]any{
					"leaseAppKey": "vtx.leaseapp.<NanoID>",
					"accountKey":  "vtx.account.<NanoID>",
					"prose":       "Tenant agrees to a $45 lockout fee for each after-hours lockout assistance request.",
					"amountCents": 4500,
				},
				ExpectedOutcome: "Validates the lease and account are alive. Atomically commits vtx.clause.<freshNanoID> " +
					"(root data {} — D5) + .prose{text} + .terms{kind:computational, amountCents:4500, period:oneTime} + " +
					".status{state:active} + the governs link (clause→lease) + the chargesTo link (clause→account). " +
					"Emits clause.created{clauseKey, leaseAppKey, accountKey, amountCents}. Returns primaryKey. The " +
					"clauseSatisfaction lens immediately projects the clause as violating (missing_charge=true, no " +
					"authorizedBy transaction yet); Weaver dispatches directOp(DebitAccount) to close the gap.",
			},
		},
	}
}

func clauseProseAspectTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     "clauseProse",
		Class:             "meta.ddl.aspectType",
		PermittedCommands: []string{"CreateClause"},
		Description: "The clause's legal-paragraph text. Stored as vtx.clause.<NanoID>.prose (class clauseProse) " +
			"= {text}. Non-sensitive. Written exactly once by CreateClause, atomically alongside the clause vertex " +
			"it belongs to. Declaration-only: no op handler of its own.",
		Script:       aspectDeclarationOnlyScript,
		InputSchema:  `{"type":"object","properties":{"text":{"type":"string"}}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"text": "The human-readable provision text, verbatim from the CreateClause payload.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:            "clause prose aspect",
				Payload:         map[string]any{"text": "Tenant agrees to a $45 lockout fee for each after-hours lockout assistance request."},
				ExpectedOutcome: "Stored as vtx.clause.<NanoID>.prose; created once by CreateClause alongside the clause vertex.",
			},
		},
	}
}

func clauseTermsAspectTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     "clauseTerms",
		Class:             "meta.ddl.aspectType",
		PermittedCommands: []string{"CreateClause"},
		Description: "The clause's machine terms — what 'fulfillment' means digitally. Stored as " +
			"vtx.clause.<NanoID>.terms (class clauseTerms) = {kind, amountCents, period}. Non-sensitive. Fire V1 " +
			"fixes kind=\"computational\" and period=\"oneTime\" (the fixed/one-time archetype); conditioned " +
			"(conditionedOn link), judgment (kind=\"judgment\"), recurring (period beyond oneTime), and prorated " +
			"(rateCents/basis) terms are later increments. Written exactly once by CreateClause, atomically " +
			"alongside the clause vertex it belongs to. Declaration-only: no op handler of its own.",
		Script:       aspectDeclarationOnlyScript,
		InputSchema:  `{"type":"object","properties":{"kind":{"type":"string"},"amountCents":{"type":"number"},"period":{"type":"string"}}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"kind":        "Fire V1: always \"computational\" (auto-debit). \"judgment\" (open-a-Task) is a later increment.",
			"amountCents": "The fixed charge amount in integer cents, verbatim from the CreateClause payload.",
			"period":      "Fire V1: always \"oneTime\". Recurring periods are a later increment (Fire V3).",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:            "clause terms aspect — fixed one-time",
				Payload:         map[string]any{"kind": "computational", "amountCents": 4500, "period": "oneTime"},
				ExpectedOutcome: "Stored as vtx.clause.<NanoID>.terms; created once by CreateClause alongside the clause vertex.",
			},
		},
	}
}

func clauseStatusAspectTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName: "clauseStatus",
		Class:         "meta.ddl.aspectType",
		// DebitAccount (loftspace-ledger) marks a fixed/one-time clause completed
		// once it posts the authorizing charge — a cross-package write, the
		// objectLiveness → TombstoneObject precedent.
		PermittedCommands: []string{"CreateClause", "DebitAccount"},
		Description: "The clause's lifecycle state. Stored as vtx.clause.<NanoID>.status (class clauseStatus) = " +
			"{state, completedAt?}, state ∈ {active, completed, superseded}. Non-sensitive. Created active by " +
			"CreateClause; updated to completed by loftspace-ledger's DebitAccount when it posts the authorizing " +
			"charge for a fixed/one-time clause (an UNCONDITIONED update — this status is audit/display bookkeeping, " +
			"not the convergence gate itself, which the clauseSatisfaction lens derives from the authorizedBy " +
			"transaction link, not this aspect — see the design's R3).",
		Script:       aspectDeclarationOnlyScript,
		InputSchema:  `{"type":"object","properties":{"state":{"type":"string"},"completedAt":{"type":"string"}}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"state":       "active (CreateClause) or completed (DebitAccount, fixed/one-time clauses).",
			"completedAt": "RFC3339 timestamp DebitAccount stamps when it marks the clause completed. Absent while active.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:            "clause status aspect — completed by a debit",
				Payload:         map[string]any{"state": "completed", "completedAt": "2026-07-02T12:00:00Z"},
				ExpectedOutcome: "Updated (op:update, unconditioned) by DebitAccount when clauseRef names this clause.",
			},
		},
	}
}
