package wellnessledger

import (
	"fmt"

	"github.com/operatinggraph/lattice/internal/pkgmgr"
)

// LedgerHistoryBucket is the NATS-KV read model the ledgerHistory lens projects
// into. It is the **P5 query surface** for "what charges/payments has this
// member had": the billing-history FE reads THIS projected bucket (one entry
// per transaction, keyed by the transaction key), never Core KV
// (lattice-architecture.md P5 — lenses are the only application query surface).
// The Refractor auto-creates the bucket on lens load.
const LedgerHistoryBucket = "wellness-ledger-history"

// MemberAccountsBucket is the NATS-KV read model the wellnessMemberAccounts
// lens projects into — one row per member IDENTITY that has ever booked
// (whether or not a ledger account has been opened yet), carrying the
// account's key when one exists. Since the account carries its own
// independently-minted NanoID (never derived from the identity's), the FE
// cannot compute an account key by string manipulation the way it once
// could — this lens is the P5 query surface for "does this member have a
// ledger account, and what is its key."
const MemberAccountsBucket = "wellness-member-accounts"

// NoShowSettlementTarget is the §10.8 TargetID == the wellnessNoShowSettlement
// lens's OutputKeyPattern prefix — the §10.2↔§10.8 binding Weaver reads.
const NoShowSettlementTarget = "wellnessNoShowSettlement"

// ClassPriceSettlementTarget is the §10.8 TargetID == the
// wellnessClassPriceSettlement lens's OutputKeyPattern prefix — the
// §10.2↔§10.8 binding Weaver reads. A separate target from
// NoShowSettlementTarget: a class price is owed regardless of attendance
// outcome (unconditional on booking .status), whereas the no-show fee gates
// on status='noShow' — two independent gaps over the same booking, converged
// by two independent settlesClassPrice/settles links so neither's count()
// sees the other's transaction.
const ClassPriceSettlementTarget = "wellnessClassPriceSettlement"

// Lenses returns the package's Lens declarations: wellnessLedgerHistory (one
// row per posted transaction, flattening the .entry aspect + the account/
// identity it posted to into a query-optimized read-model row — the FE
// derives a running balance client-side by summing amountCents, positive for
// debit, negative for credit, over rows for a given identityKey/accountKey;
// the ledger itself never stores a mutable running total), wellnessMemberAccounts
// (the member -> account key lookup, since the account key is no longer
// derivable), wellnessNoShowSettlement (the missing_charge convergence
// lens targets.go's WeaverTargets dispatches WellnessDebitAccount over), and
// wellnessClassPriceSettlement (the missing_price_charge convergence lens —
// the OTHER wellness billing gap, a class's booking price, converged
// unconditionally on attendance rather than gated on a noShow). Prefixed
// like the package's DDLs (ddls.go): a Lens canonicalName is global across
// every installed package, and loftspace-ledger already owns the bare
// `ledgerHistory` name.
func Lenses() []pkgmgr.LensSpec {
	return []pkgmgr.LensSpec{
		{
			CanonicalName: "wellnessLedgerHistory",
			Class:         "meta.lens",
			Adapter:       "nats-kv",
			Bucket:        LedgerHistoryBucket,
			Engine:        "full",
			Spec:          ledgerHistorySpec,
		},
		{
			CanonicalName: "wellnessMemberAccounts",
			Class:         "meta.lens",
			Adapter:       "nats-kv",
			Bucket:        MemberAccountsBucket,
			Engine:        "full",
			Spec:          memberAccountsSpec,
		},
		{
			CanonicalName:  NoShowSettlementTarget,
			Class:          "meta.lens",
			Adapter:        "nats-kv",
			Bucket:         "weaver-targets",
			Engine:         "full",
			Spec:           noShowSettlementSpec,
			ProjectionKind: "actorAggregate",
			Output: &pkgmgr.OutputDescriptorSpec{
				AnchorType:       "booking",
				OutputKeyPattern: NoShowSettlementTarget + ".{actorSuffix}",
				BodyColumns:      []string{"violating", "missing_charge", "entityKey", "bookingKey", "identityKey", "accountKey", "feeCents", "status", "maxretries_charge"},
				EmptyBehavior:    "delete",
				KeyColumn:        "entityId",
				Freshness:        "auto",
			},
		},
		{
			CanonicalName:  ClassPriceSettlementTarget,
			Class:          "meta.lens",
			Adapter:        "nats-kv",
			Bucket:         "weaver-targets",
			Engine:         "full",
			Spec:           classPriceSettlementSpec,
			ProjectionKind: "actorAggregate",
			Output: &pkgmgr.OutputDescriptorSpec{
				AnchorType:       "booking",
				OutputKeyPattern: ClassPriceSettlementTarget + ".{actorSuffix}",
				BodyColumns:      []string{"violating", "missing_price_charge", "entityKey", "bookingKey", "identityKey", "accountKey", "priceCents", "sessionName", "maxretries_price"},
				EmptyBehavior:    "delete",
				KeyColumn:        "entityId",
				Freshness:        "auto",
			},
		},
	}
}

// noShowSettlementSpec is the one-row-per-booking convergence cypher: a
// noShow booking carrying a positive noShowFeeCents needs its charge posted
// onto the booker's wellness-ledger account, once. missing_charge only (no
// missing_account gap, mirroring clinic-ledger's identical rationale): the
// booking's booker must already have a wellnessaccount, matching wellness'
// existing (front-desk-driven) billing assumption.
//
//   - `missing_charge` — the booking is a noShow, carries a fee, the booker
//     has a ledger account, and no wellnesstransaction `settles` this
//     booking yet (count(tx.key) collapses the fan to a single existence
//     check — the objectLiveness/clauseSatisfaction idiom, same as
//     clinic-ledger's clinicNoShowSettlement). Weaver dispatches
//     WellnessDebitAccount{accountKey, amountCents, bookingRef} (targets.go) — the
//     bookingRef extension writes the settles audit link this OPTIONAL
//     MATCH walks, so once posted the gap converges and stays converged
//     (noShow carries forward once SetBookingAttendance sets it, and a
//     re-mark to attended is the mirror correction — see targets.go's doc
//     comment on that edge).
//
// A booking whose booker has no wellnessaccount yet never violates
// (accountKey null); one with no noShowFeeCents (a noShow set before this
// lens existed) never violates either — both are non-goals for v1, not a
// gap this lens is meant to converge.
// noShowSettlementSpec is built once at package init: the retry cap
// (maxChargeRetries) bakes into the constant maxretries_charge column, the
// §10.2 "the policy lives in the cypher" convention lease-signing's
// leaseApplicationCompleteSpec established. The cypher carries no literal '%'.
var noShowSettlementSpec = fmt.Sprintf(`MATCH (bk:booking {key: $actorKey})
MATCH (bk)-[:bookedBy]->(id:identity)
OPTIONAL MATCH (id)<-[:heldFor]-(a:wellnessaccount)
OPTIONAL MATCH (bk)<-[:settles]-(tx:wellnesstransaction)
WITH
  bk.key AS entityKey,
  bk.status.data.value AS status,
  bk.status.data.noShowFeeCents AS feeCents,
  id.key AS identityKey,
  a.key AS accountKey,
  count(tx.key) AS txCount
RETURN
  entityKey AS actorKey,
  entityKey,
  entityKey AS bookingKey,
  identityKey,
  accountKey,
  feeCents,
  status,
  ((status = 'noShow') AND (feeCents <> null) AND (feeCents > 0) AND (accountKey <> null) AND (txCount = 0)) AS missing_charge,
  ((status = 'noShow') AND (feeCents <> null) AND (feeCents > 0) AND (accountKey <> null) AND (txCount = 0)) AS violating,
  %d AS maxretries_charge
`, maxChargeRetries)

// classPriceSettlementSpec is the one-row-per-booking convergence cypher for
// the OTHER wellness billing gap: a session carrying a positive priceCents
// needs its class-price charge posted onto the booker's wellness-ledger
// account, once — regardless of attendance outcome. Unlike
// noShowSettlementSpec, this is UNCONDITIONAL on booking .status: a class
// price is owed for booking the seat, not for showing up or not, so there is
// no status filter here at all (the mirror omission of noShowSettlementSpec's
// `status = 'noShow'` clause).
//
//   - `missing_price_charge` — the booking's session carries a priceCents > 0,
//     the booker has a ledger account, and no wellnesstransaction
//     `settlesClassPrice` this booking yet (count(tx.key) collapses the fan to
//     a single existence check, the same objectLiveness/clauseSatisfaction
//     idiom noShowSettlementSpec uses). Weaver dispatches
//     WellnessDebitAccount{accountKey, amountCents, priceBookingRef} (targets.go) —
//     the priceBookingRef extension writes the settlesClassPrice audit link
//     this OPTIONAL MATCH walks (a DISTINCT relation from settles, so this
//     lens's count() never sees a no-show-fee transaction and vice versa), so
//     once posted the gap converges and stays converged.
//
// `MATCH (bk:booking {key: $actorKey})` alone (no isDeleted clause) is enough
// to exclude a cancelled/soft-deleted booking — the full engine's Core-KV
// reads filter isDeleted per Contract #1 (executor.go), so a tombstoned
// booking simply stops matching, mirroring noShowSettlementSpec's identical
// MATCH. A booking whose session carries no priceCents (or 0 — a free class)
// never violates (priceCents null/0); one whose booker has no
// wellnessaccount yet never violates either (accountKey null) — same
// non-goals as noShowSettlementSpec, not gaps this lens converges.
// classPriceSettlementSpec is built once at package init: the retry cap
// (maxPriceChargeRetries) bakes into the constant maxretries_price column,
// the same §10.2 "the policy lives in the cypher" convention
// noShowSettlementSpec follows. The cypher carries no literal '%'.
var classPriceSettlementSpec = fmt.Sprintf(`MATCH (bk:booking {key: $actorKey})
MATCH (bk)-[:forSession]->(se:session)
MATCH (bk)-[:bookedBy]->(id:identity)
OPTIONAL MATCH (id)<-[:heldFor]-(a:wellnessaccount)
OPTIONAL MATCH (bk)<-[:settlesClassPrice]-(tx:wellnesstransaction)
WITH
  bk.key AS entityKey,
  se.schedule.data.priceCents AS priceCents,
  se.schedule.data.name AS sessionName,
  id.key AS identityKey,
  a.key AS accountKey,
  count(tx.key) AS txCount
RETURN
  entityKey AS actorKey,
  entityKey,
  entityKey AS bookingKey,
  identityKey,
  accountKey,
  priceCents,
  sessionName,
  ((priceCents <> null) AND (priceCents > 0) AND (accountKey <> null) AND (txCount = 0)) AS missing_price_charge,
  ((priceCents <> null) AND (priceCents > 0) AND (accountKey <> null) AND (txCount = 0)) AS violating,
  %d AS maxretries_price
`, maxPriceChargeRetries)

// ledgerHistorySpec projects one row per transaction, walking postedTo to the
// account and heldFor to the identity so the FE can filter/group by
// identityKey with no extra hop. Every MATCH is REQUIRED (not OPTIONAL): a
// transaction projects a row only when it is genuinely posted to a live
// account held for a live identity (the normal shape every WellnessDebitAccount/
// WellnessCreditAccount commit produces). The per-row key is the transaction key (the
// IntoKey default), so the read model is keyed by vtx.wellnesstransaction.<id>;
// transactionKey repeats it in the body for the reader.
const ledgerHistorySpec = `MATCH (t:wellnesstransaction)
MATCH (t)-[:postedTo]->(a:wellnessaccount)
MATCH (a)-[:heldFor]->(id:identity)
RETURN
  t.key AS key,
  t.key AS transactionKey,
  a.key AS accountKey,
  id.key AS identityKey,
  t.entry.data.type AS type,
  t.entry.data.amountCents AS amountCents,
  t.entry.data.memo AS memo,
  t.entry.data.postedAt AS postedAt`

// memberAccountsSpec projects one row per identity that has ever BOOKED
// (walked via booking's bookedBy link, DISTINCT'd — a member with many
// bookings still gets exactly one row) — not one row per platform identity,
// which would scan every LoftSpace tenant / Clinic patient / Café resident
// regardless of whether they ever touched Wellness. A member with no ledger
// account yet still gets a row (accountKey null), which is exactly the "has
// this member opened an account" query the FE needs before its first-ever
// charge or payment. OPTIONAL MATCH: the heldFor hop legitimately has no
// match for a member who has never had a charge/payment.
const memberAccountsSpec = `MATCH (bk:booking)-[:bookedBy]->(id:identity)
WITH DISTINCT id
OPTIONAL MATCH (id)<-[:heldFor]-(a:wellnessaccount)
RETURN
  id.key AS key,
  id.key AS identityKey,
  a.key AS accountKey`
