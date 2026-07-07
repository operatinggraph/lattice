// Package cafedomain is the Café vertical's POS/session domain (Café Inc 2,
// verticals.md): a short-lived house-tab session (OpenTab/Charge/Settle)
// against a resident lease, settled onto cafe-ledger's append-only
// cafeaccount/cafetransaction house-tab ledger (Café Inc 1).
//
// It declares:
//
//   - The `tab` vertex type (DDL `tab`) — OpenTab mints vtx.tab.<NanoID>
//     (root data {} per D5) + a .status aspect {value: open, totalCents: 0,
//     openedAt, leaseAppKey} + the openFor link (tab→leaseapp). Charge
//     OCC-conditions an accumulate onto .status.totalCents (the
//     providerSlotClaim precedent: a real accumulator must not lose a
//     concurrent update, unlike an idempotent status flip). Settle
//     OCC-conditions the close (.status.value → settled, settledAt
//     stamped).
//
//   - The `tabStatus` aspect type (DDL `tabStatus`) — the step-6 write gate
//     for .status, written by the tab vertexType DDL's own script.
//
//   - The `cafeTabSettlement` actorAggregate convergence lens (§10.2),
//     anchored on tab: `missing_account` is true while a settled,
//     positive-total tab's lease has no café-ledger account yet;
//     `missing_charge` is true once the account exists but no
//     cafetransaction `settles` this tab.
//
//   - The §10.8 playbook (meta.weaverTarget cafeTabSettlement) —
//     missing_account → directOp(CreateAccount) (cafe-ledger, opens the
//     account on first use); missing_charge → directOp(DebitAccount)
//     (cafe-ledger, posts the settled total with a tabRef back-link).
//
// cafe-ledger's DebitAccount is extended (this fire) with an optional
// tabRef: when present it writes the lnk.cafetransaction.settles.tab audit
// link the cafeTabSettlement lens's missing_charge gate reads — the
// bespoke-contracts clauseRef precedent, additive and byte-for-byte
// unaffected for a plain human-submitted DebitAccount.
//
// See _bmad-output/implementation-artifacts/cafe-ledger-design.md's "Next"
// section (Inc 2). Depends lease-signing (the leaseapp a tab is opened
// against) + cafe-ledger (the account/transaction ops the playbook
// dispatches).
//
// Inc 2's thin FE (POS→tab · front-desk open-tabs · resident house-tab) and
// Inc 3's one-bill composition lens are NOT this increment — this ships the
// domain + Weaver wiring only (Café row, verticals.md).
package cafedomain

import "github.com/asolgan/lattice/internal/pkgmgr"

// Package is the static, install-time bundle.
var Package = pkgmgr.Definition{
	Name:    "cafe-domain",
	Version: "0.1.0",
	Description: "Café house-tab POS session domain: the tab vertex type (OpenTab/Charge/Settle, OCC-conditioned " +
		"running total) + the tabStatus aspect type + the cafeTabSettlement actorAggregate convergence lens " +
		"(missing_account/missing_charge) + the §10.8 playbook dispatching directOp(CreateAccount)/" +
		"directOp(DebitAccount) (cafe-ledger) to post a settled tab onto the resident's house-tab account. " +
		"Depends lease-signing + cafe-ledger.",
	Depends:       []string{"lease-signing", "cafe-ledger"},
	DDLs:          DDLs(),
	Lenses:        Lenses(),
	Permissions:   Permissions(),
	WeaverTargets: WeaverTargets(),
}
