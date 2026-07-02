// Package bespokecontracts is the LoftSpace "Executable Paper" reference
// package (Fire V1 of the bespoke-contracts-executable-paper design): the
// fixed/one-time computational clause archetype, riding the convergence
// machinery the platform already ships — no new engine, no Weaver runtime.
//
// It declares:
//
//   - The `clause` vertex type (DDL `clause`) — CreateClause mints
//     vtx.clause.<NanoID> (root data {} per D5) governing a lease and
//     charging a ledger account: a .prose aspect (the legal paragraph), a
//     .terms aspect ({kind: "computational", amountCents, period:
//     "oneTime"} — V1 covers the fixed one-time archetype only), and a
//     .status aspect ({state: "active"}). Writes the governs link
//     (clause→lease) and the chargesTo link (clause→account).
//
//   - The `clauseSatisfaction` actorAggregate convergence lens (§10.2),
//     anchored on the clause: `missing_charge` is true until a transaction
//     `authorizedBy` this clause exists — the shipped upsert-retraction
//     idiom (a charge closes the gap; a completed clause's row simply stops
//     violating, per the design's R3 v1 constraint — no filter-retraction
//     dependency).
//
//   - The §10.8 playbook (meta.weaverTarget clauseSatisfaction) —
//     missing_charge → directOp(DebitAccount), row-templating the account
//     to charge + the clause to authorize against.
//
// loftspace-ledger's DebitAccount op is extended (this fire) to accept an
// optional clauseRef: when present it writes the
// lnk.transaction.authorizedBy.clause audit link and marks the clause
// .status completed — the "why was I charged this?" chain of custody.
//
// See _bmad-output/implementation-artifacts/bespoke-contracts-executable-paper-design.md
// §3, §4.1, §10 (Fire V1). Depends lease-signing (the leaseapp a clause
// governs) + loftspace-ledger (the account a clause charges + the
// DebitAccount op the playbook dispatches).
package bespokecontracts

import "github.com/asolgan/lattice/internal/pkgmgr"

// Package is the static, install-time bundle.
var Package = pkgmgr.Definition{
	Name:    "bespoke-contracts",
	Version: "0.1.0",
	Description: "LoftSpace 'Executable Paper' reference package (Fire V1 — fixed/one-time computational clause): " +
		"the clause vertex type (CreateClause, .prose/.terms/.status aspects, governs + chargesTo links) + the " +
		"clauseSatisfaction actorAggregate convergence lens (§10.2, missing_charge = no authorizedBy transaction " +
		"yet) + the §10.8 playbook dispatching directOp(DebitAccount) on the gap. Depends lease-signing + " +
		"loftspace-ledger.",
	Depends:       []string{"lease-signing", "loftspace-ledger"},
	DDLs:          DDLs(),
	Lenses:        Lenses(),
	Permissions:   Permissions(),
	WeaverTargets: WeaverTargets(),
	OpMetas:       OpMetas(),
}
