package semanticcontracts

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// WeaverTargets returns the package's meta.weaverTarget playbook (Contract
// #10 §10.8). Two independent gaps → remediation:
//
//   - missing_charge → directOp(DebitAccount) over the charged account. No
//     Target: DebitAccount grants operator/scope=any (loftspace-ledger
//     permissions.go), the same objects-base / cafe-domain precedent (no
//     authContext.target dependency) — every payload field the script
//     requires goes directly in Params (Target only ever sets
//     AuthContext.Target for auth-path scoping, it is NEVER merged into the
//     op payload). Params route the charged account (row.accountKey), the
//     computed amount (row.amountCents, type-preserved — resolveParam
//     returns the row value verbatim), the authorizing clause (row.clauseKey,
//     the clauseRef param loftspace-ledger's DebitAccount reads), and (Fire
//     V3) row.period — DebitAccount branches on period="monthly" to re-arm
//     the clause's chargeValidUntil instead of completing it — into the op's
//     payload; Reads routes the account + clause keys — and the clause's own
//     .terms aspect (row.clauseKey.terms), so loftspace-ledger's DebitAccount
//     can derive the authoritative amountCents and the term from the clause
//     instead of trusting this row-templated copy — into ContextHint.Reads
//     so the Processor hydrates them; OptionalReads routes the clause's
//     .status (row.clauseKey.status), the recorded due date DebitAccount
//     walks the anniversary grid from — absence-tolerant because a clause
//     never charged has no due date yet. The `directOp`-must-be-literal guard is
//     satisfied — DebitAccount is a literal operation name, only params/reads
//     are row-templated (the objectLiveness → TombstoneObject / appointment
//     Reminders → RecordAppointmentReminder precedent, granted to operator,
//     which Weaver's service actor holds).
//   - missing_inspection → assignTask(InspectPremises) to the assigned
//     inspector (row.inspectorKey), scoped to the clause (row.clauseKey) —
//     the same shape as lease-signing's missing_signature → assignTask
//     SignLease. Opens a stable-id Task; the inspector completes it by
//     submitting InspectPremises, which the clause DDL's own script handles
//     (mirrors SignLease acting on its own leaseapp).
//
// Every row.<col> template is a clauseSatisfaction BodyColumn — the
// §10.2↔§10.8 column seam, cross-checked by
// TestSemanticContracts_PlaybookColumnsMatchLens.
//
// leaseRentSettlement's own playbook (lenses.go) is the bootstrap ahead of
// this one — four gaps, the first three mirroring cafe-domain's tabSettlement
// missing_account → directOp(CreateAccount) shape:
//
//   - missing_terms → directOp(BackfillLeaseTerms) (lease-signing) — the
//     lens's only gate opens the account/clause gaps once requestedRent is
//     non-null, so this gap converges before either of them ever fires.
//     Grants operator unconfined (lease-signing permissions.go), the same
//     no-authContext.target idiom every directOp in this file uses.
//     Reads the application; OptionalReads its own .terms aspect
//     (row.leaseAppKey.terms) — the script branches on the aspect's absence
//     rather than requiring it (Contract #2 §2.5).
//   - missing_account → directOp(LoftspaceCreateAccount) (loftspace-ledger).
//     Grants operator unconfined (loftspace-ledger permissions.go), the same
//     no-authContext.target idiom every directOp in this file uses.
//   - missing_clause → directOp(CreateClause) (this package) — accountKey
//     comes from THIS SAME row (the lens only opens missing_clause once
//     missing_account has already converged, lenses.go), amountCents from
//     termRentCents (the lens's own ×100 conversion of the current term's
//     rent — never a raw dollar column), the term from termStart/leaseEnd
//     (the lease's current term, anchor-own .tenancy columns the gap
//     requires non-null), period + prose are literals (no "row." prefix, so
//     resolveParam passes them through verbatim, strategist.go).
//   - missing_term → directOp(BackfillClauseTerm) (this package) — an
//     untermed monthly clause governing a lease that has a .tenancy. Params
//     route that clause (row.untermedClauseKey, the lens's max() over the
//     governs walk — non-null whenever the gap is open, which is what the
//     gap's own conjunct states) and the lease; Reads routes the clause, its
//     .terms (the op adds the term to it), the lease and its .tenancy (the
//     term's source, required — the gap only opens when it is present);
//     OptionalReads the clause's .status, whose recorded due date the op
//     moves onto the term's grid; Enumerations declares the op's one bounded
//     walk, the clause's own outbound governs links (degree 1 by
//     construction), which it re-keys when spelled with the legacy
//     `governs.lease.` target segment.
//   - missing_termShortened → directOp(ShortenClauseTerm) (this package) — a
//     termed monthly clause running past a recorded notice's moveOutAt.
//     Params route that clause (row.overrunClauseKey, the lens's max() over
//     the same governs fan — non-null whenever the gap is open) and the
//     lease; Reads routes the clause, its .terms (the op shortens it), the
//     lease and its .notice (the moveOutAt the op reads, required — the gap
//     only opens when it is present); OptionalReads the clause's .status,
//     whose recorded due date decides whether the shortened term is already
//     fully billed; Enumerations declares the op's one bounded walk, the
//     clause's own outbound governs links, which it reads to verify the
//     clause governs the lease the dispatch named.
//
// Cross-checked by TestSemanticContracts_LeaseRentSettlementColumnsMatchLens.
func WeaverTargets() []pkgmgr.WeaverTargetSpec {
	return []pkgmgr.WeaverTargetSpec{
		{
			TargetID: ClauseSatisfactionTarget,
			Description: "Every contract clause is honored: a clause that charges an account is billed, monthly " +
				"clauses each calendar-month period of their term, and a clause requiring an inspection has one " +
				"assigned to its named inspector.",
			LensRef: ClauseSatisfactionTarget,
			Gaps: map[string]pkgmgr.GapActionSpec{
				"missing_charge": {
					Action:    "directOp",
					Operation: "DebitAccount",
					// DebitAccount is claimed by 4 installed ledger DDLs — pin the
					// loftspace-ledger vertexType DDL this target dispatches to, or the
					// Processor's operationType→class reverse index fails closed
					// (MissingClass).
					Class:         "transaction",
					Params:        map[string]string{"accountKey": "row.accountKey", "amountCents": "row.amountCents", "clauseRef": "row.clauseKey", "period": "row.period"},
					Reads:         []string{"row.accountKey", "row.clauseKey", "row.clauseKey.terms"},
					OptionalReads: []string{"row.clauseKey.status"},
				},
				"missing_inspection": {
					Action:    "assignTask",
					Operation: "InspectPremises",
					Assignee:  "row.inspectorKey",
					Target:    "row.clauseKey",
				},
			},
		},
		{
			TargetID: LeaseRentSettlementTarget,
			Description: "An approved lease has an agreed rent, a ledger account, and a recurring monthly rent " +
				"clause covering its current term, and every monthly clause it has carries its term. A missing " +
				"agreed rent is backfilled from the unit's listed rent; whichever of the account/clause is then " +
				"still missing is created; a monthly clause minted without a term has one stamped from the " +
				"lease's tenancy — so a signed lease actually bills its rent, for exactly its term, and a signed " +
				"renewal mints its own clause for the renewed term.",
			LensRef: LeaseRentSettlementTarget,
			Gaps: map[string]pkgmgr.GapActionSpec{
				"missing_terms": {
					Action:    "directOp",
					Operation: "BackfillLeaseTerms",
					// BackfillLeaseTerms is claimed by lease-signing's own leaseapp
					// vertexType DDL (the same pin-regardless-of-ambiguity idiom every
					// directOp in this file uses).
					Class:         "leaseapp",
					Params:        map[string]string{"leaseAppKey": "row.leaseAppKey"},
					Reads:         []string{"row.leaseAppKey"},
					OptionalReads: []string{"row.leaseAppKey.terms"},
				},
				"missing_account": {
					Action:    "directOp",
					Operation: "LoftspaceCreateAccount",
					// LoftspaceCreateAccount is claimed by loftspace-ledger's own
					// vertexType DDL (the 4-installed-ledger-DDLs reverse-index trap
					// above applies to every directOp in this file).
					Class:  "account",
					Params: map[string]string{"leaseAppKey": "row.leaseAppKey"},
					Reads:  []string{"row.leaseAppKey"},
				},
				"missing_clause": {
					Action:    "directOp",
					Operation: "CreateClause",
					Class:     "clause",
					Params: map[string]string{
						"leaseAppKey": "row.leaseAppKey",
						"accountKey":  "row.accountKey",
						"amountCents": "row.termRentCents",
						"period":      "monthly",
						"validFrom":   "row.termStart",
						"validUntil":  "row.leaseEnd",
						"prose":       "Monthly rent per the signed lease agreement.",
					},
					Reads: []string{"row.leaseAppKey", "row.accountKey"},
				},
				"missing_term": {
					Action:        "directOp",
					Operation:     "BackfillClauseTerm",
					Class:         "clause",
					Params:        map[string]string{"clauseKey": "row.untermedClauseKey", "leaseAppKey": "row.leaseAppKey"},
					Reads:         []string{"row.untermedClauseKey", "row.untermedClauseKey.terms", "row.leaseAppKey", "row.leaseAppKey.tenancy"},
					OptionalReads: []string{"row.untermedClauseKey.status"},
					Enumerations: []pkgmgr.EnumerationSpec{
						{Hub: "row.untermedClauseKey", Relation: "governs", Direction: "out"},
					},
				},
				"missing_termShortened": {
					Action:        "directOp",
					Operation:     "ShortenClauseTerm",
					Class:         "clause",
					Params:        map[string]string{"clauseKey": "row.overrunClauseKey", "leaseAppKey": "row.entityKey"},
					Reads:         []string{"row.overrunClauseKey", "row.overrunClauseKey.terms", "row.entityKey", "row.entityKey.notice"},
					OptionalReads: []string{"row.overrunClauseKey.status"},
					Target:        "row.overrunClauseKey",
					Enumerations: []pkgmgr.EnumerationSpec{
						{Hub: "row.overrunClauseKey", Relation: "governs", Direction: "out"},
					},
				},
			},
		},
	}
}
