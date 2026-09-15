// Package semanticcontracts is the LoftSpace "Executable Paper" reference
// package (the semantic-contracts-executable-paper design): fixed/one-time,
// conditioned, judgment, recurring monthly (termed to a calendar-month period
// grid, or untermed), and prorated computational clauses, plus
// self-amendment, all riding the convergence machinery the platform already
// ships — no new engine, no Weaver runtime, no rounding UDF (proration
// computes in exact Starlark bignum integer arithmetic instead).
//
// It declares:
//
//   - The `clause` vertex type (DDL `clause`) — CreateClause mints
//     vtx.clause.<NanoID> (root data {} per D5) governing a lease: a .prose
//     aspect (the legal paragraph), a .terms aspect ({kind, conditioned,
//     amountCents?, period, validFrom?, validUntil?, basis?, rateCents?,
//     periodDays?, daysOccupied?}), and a .status aspect ({state}).
//     `kind=computational` (default) charges a ledger account (chargesTo
//     link) — `period` selects "oneTime" (default) or "monthly" (recurring),
//     a monthly clause may carry a term (validFrom/validUntil, both or
//     neither) it bills one calendar-month period at a time within, and the
//     amount is either a flat amountCents or, given
//     rateCents+periodDays+daysOccupied instead, a once-computed prorated
//     amount; `kind=judgment` assigns an inspector (requiresInspectionBy
//     link) instead, closed by a .clauseInspection aspect. Either kind may
//     carry an optional conditionedOn link (any live vertex, e.g. a pet
//     record) gating the charge on that vertex staying alive. Always writes
//     the governs link (clause→leaseapp). BackfillClauseTerm stamps the term
//     onto a monthly clause minted without one, from its lease's .tenancy.
//
//   - The `clauseSatisfaction` actorAggregate convergence lens (§10.2),
//     anchored on the clause: `missing_charge` is true while the clause
//     charges an account, its condition (if any) still holds, and — for a
//     oneTime clause — no transaction `authorizedBy` it exists yet, or — for
//     a monthly clause — a recorded lapse has reached its current period's
//     due date (.status.chargeValidUntil, or validFrom before the first
//     charge) and that period lies inside its term; `missing_inspection` is
//     true while the clause has an assigned inspector and no .inspection
//     aspect yet. A oneTime gap closes
//     and its row simply stops violating (the design's R3 v1 constraint — no
//     filter-retraction dependency); a monthly gap re-opens via the
//     projected `freshUntil` column arming Weaver's temporal lane, and stops
//     re-opening once the term is fully billed.
//
//   - The §10.8 playbook (meta.weaverTarget clauseSatisfaction) —
//     missing_charge → directOp(DebitAccount), row-templating the account to
//     charge, the clause to authorize against, and the clause's period;
//     missing_inspection → assignTask(InspectPremises) to the assigned
//     inspector.
//
//   - The `leaseRentSettlement` actorAggregate convergence lens + its own
//     meta.weaverTarget: the bootstrap ahead of clauseSatisfaction — every
//     approved, signed lease projects a row and needs an agreed rent
//     (missing_terms → directOp(BackfillLeaseTerms), lease-signing, backfilled
//     from the unit's own listed rent), then its ledger account opened
//     (missing_account → directOp(LoftspaceCreateAccount), mirroring
//     cafe-domain's tabSettlement idiom) and then a recurring monthly rent
//     clause minted for its CURRENT term (missing_clause →
//     directOp(CreateClause) with validFrom = the term's start, validUntil =
//     its end, and the term's dollar rent converted to integer cents in the
//     lens itself since Weaver's playbook params never compute, only
//     substitute) — after which clauseSatisfaction owns billing it. A signed
//     renewal records a new term on the lease, so the gap re-opens and mints
//     a second clause covering the renewed term at the renewal rent, the
//     original clause expiring by its own validUntil. Its fourth gap,
//     missing_term → directOp(BackfillClauseTerm), terms a monthly clause the
//     lease already has that was minted without a term (row-templating the
//     clause the lens's max() found and the lease) — lease-anchored because
//     the inbound governs walk resolves a clause from the link's source-type
//     segment, which is right on every key shape, while a legacy
//     `governs.lease.` key can never be walked outbound from the clause; the
//     op re-keys such a link as it terms the clause.
//
// loftspace-ledger's DebitAccount op accepts an optional clauseRef: when
// present it writes the lnk.transaction.authorizedBy.clause audit link and
// updates the clause's .status — completed for a oneTime clause, or
// chargeValidUntil re-armed for a monthly one (the next anniversary of
// validFrom for a termed clause) — the "why was I charged this?" chain of
// custody.
//
//   - SupersedeClause mints a replacement clause (CreateClause's shape, plus
//     clauseKey naming the amended one), writes the amends link (new
//     clause→amended clause), tombstones the amended clause's root
//     (anchor-tombstone retraction — its clauseSatisfaction row deletes),
//     and marks its .status superseded (audit).
//
// See _bmad-output/implementation-artifacts/semantic-contracts-executable-paper-design.md
// §3, §4.1, §7, §10, §13. Depends lease-signing (the leaseapp a clause
// governs) + loftspace-ledger (the account a clause charges + the
// DebitAccount op the playbook dispatches).
package semanticcontracts

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// Package is the static, install-time bundle.
var Package = pkgmgr.Definition{
	Name:    "semantic-contracts",
	Version: "0.5.1",
	Description: "LoftSpace 'Executable Paper' reference package (fixed/one-time, conditioned, judgment, " +
		"recurring monthly — termed to a calendar-month period grid or untermed — and prorated computational " +
		"clauses, plus self-amendment): the clause vertex type (CreateClause/SupersedeClause/BackfillClauseTerm, " +
		".prose/.terms/.status/.clauseInspection aspects, governs + chargesTo/requiresInspectionBy/conditionedOn/" +
		"amends links) + the clauseSatisfaction actorAggregate convergence lens (§10.2, " +
		"missing_charge/missing_inspection, freshUntil-armed recurring freshness on the term's period grid) + the " +
		"§10.8 playbook dispatching directOp(DebitAccount)/assignTask(InspectPremises) on the gaps + the " +
		"leaseRentSettlement actorAggregate lens/playbook bootstrapping an approved signed lease's agreed rent, " +
		"ledger account, then a recurring monthly rent clause for its current term, and terming any monthly " +
		"clause it has minted without one (missing_terms → directOp(BackfillLeaseTerms), missing_account → " +
		"directOp(LoftspaceCreateAccount), missing_clause → directOp(CreateClause) with the term, missing_term → " +
		"directOp(BackfillClauseTerm), dollars→cents conversion in the lens). Depends lease-signing + " +
		"loftspace-ledger.",
	Depends:       []string{"lease-signing", "loftspace-ledger"},
	DDLs:          DDLs(),
	Lenses:        Lenses(),
	Permissions:   Permissions(),
	WeaverTargets: WeaverTargets(),
	OpMetas:       OpMetas(),
}
