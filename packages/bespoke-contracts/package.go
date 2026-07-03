// Package bespokecontracts is the LoftSpace "Executable Paper" reference
// package (Fires V1-V4 of the bespoke-contracts-executable-paper design):
// fixed/one-time, conditioned, judgment, recurring monthly, and prorated
// computational clauses, plus self-amendment (Fire V4), all riding the
// convergence machinery the platform already ships — no new engine, no
// Weaver runtime, no rounding UDF (Fire V3 proration computes in exact
// Starlark bignum integer arithmetic instead).
//
// It declares:
//
//   - The `clause` vertex type (DDL `clause`) — CreateClause mints
//     vtx.clause.<NanoID> (root data {} per D5) governing a lease: a .prose
//     aspect (the legal paragraph), a .terms aspect ({kind, conditioned,
//     amountCents?, period, basis?, rateCents?, periodDays?, daysOccupied?}),
//     and a .status aspect ({state}). `kind=computational` (default, Fire V1)
//     charges a ledger account (chargesTo link) — `period` selects "oneTime"
//     (default) or "monthly" (Fire V3 recurring), and the amount is either a
//     flat amountCents or, given rateCents+periodDays+daysOccupied instead
//     (Fire V3), a once-computed prorated amount; `kind=judgment` (Fire V2)
//     assigns an inspector (requiresInspectionBy link) instead, closed by a
//     .clauseInspection aspect. Either kind may carry an optional
//     conditionedOn link (any live vertex, e.g. a pet record) gating the
//     charge on that vertex staying alive. Always writes the governs link
//     (clause→lease).
//
//   - The `clauseSatisfaction` actorAggregate convergence lens (§10.2),
//     anchored on the clause: `missing_charge` is true while the clause
//     charges an account, its condition (if any) still holds, and — for a
//     oneTime clause — no transaction `authorizedBy` it exists yet, or — for
//     a monthly clause (Fire V3) — its .status.chargeValidUntil has lapsed
//     (the lease-signing bgcheck-freshness pattern); `missing_inspection` is
//     true while the clause has an assigned inspector and no .inspection
//     aspect yet. A oneTime gap closes and its row simply stops violating
//     (the design's R3 v1 constraint — no filter-retraction dependency); a
//     monthly gap re-opens via the projected `freshUntil` column arming
//     Weaver's temporal lane.
//
//   - The §10.8 playbook (meta.weaverTarget clauseSatisfaction) —
//     missing_charge → directOp(DebitAccount), row-templating the account to
//     charge, the clause to authorize against, and (Fire V3) the clause's
//     period; missing_inspection → assignTask(InspectPremises) to the
//     assigned inspector.
//
// loftspace-ledger's DebitAccount op is extended (Fire V1) to accept an
// optional clauseRef: when present it writes the
// lnk.transaction.authorizedBy.clause audit link and updates the clause's
// .status — completed for a oneTime clause, or chargeValidUntil re-armed for
// a monthly one (Fire V3) — the "why was I charged this?" chain of custody.
//
//   - SupersedeClause (Fire V4) mints a replacement clause (CreateClause's
//     shape, plus clauseKey naming the amended one), writes the amends link
//     (new clause→amended clause), tombstones the amended clause's root
//     (anchor-tombstone retraction — its clauseSatisfaction row deletes),
//     and marks its .status superseded (audit).
//
// See _bmad-output/implementation-artifacts/bespoke-contracts-executable-paper-design.md
// §3, §4.1, §7, §10 (Fires V1-V4). Depends lease-signing (the leaseapp a
// clause governs) + loftspace-ledger (the account a clause charges + the
// DebitAccount op the playbook dispatches).
package bespokecontracts

import "github.com/asolgan/lattice/internal/pkgmgr"

// Package is the static, install-time bundle.
var Package = pkgmgr.Definition{
	Name:    "bespoke-contracts",
	Version: "0.4.0",
	Description: "LoftSpace 'Executable Paper' reference package (Fires V1-V4 — fixed/one-time, conditioned, " +
		"judgment, recurring monthly, and prorated computational clauses, plus self-amendment): the clause vertex " +
		"type (CreateClause/SupersedeClause, .prose/.terms/.status/.clauseInspection aspects, governs + " +
		"chargesTo/requiresInspectionBy/conditionedOn/amends links) + the clauseSatisfaction actorAggregate " +
		"convergence lens (§10.2, missing_charge/missing_inspection, freshUntil-armed recurring freshness) + the " +
		"§10.8 playbook dispatching directOp(DebitAccount)/assignTask(InspectPremises) on the gaps. Depends " +
		"lease-signing + loftspace-ledger.",
	Depends:       []string{"lease-signing", "loftspace-ledger"},
	DDLs:          DDLs(),
	Lenses:        Lenses(),
	Permissions:   Permissions(),
	WeaverTargets: WeaverTargets(),
	OpMetas:       OpMetas(),
}
