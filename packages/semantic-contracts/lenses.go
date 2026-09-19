package semanticcontracts

import (
	"fmt"

	"github.com/operatinggraph/lattice/internal/pkgmgr"
)

// ClauseSatisfactionTarget is the §10.8 TargetID == the clauseSatisfaction
// lens's OutputKeyPattern prefix — the §10.2↔§10.8 binding Weaver reads.
const ClauseSatisfactionTarget = "clauseSatisfaction"

// LeaseRentSettlementTarget is the §10.8 TargetID == the leaseRentSettlement
// lens's OutputKeyPattern prefix. It closes the bootstrap gap ahead of
// clauseSatisfaction: a signed, approved lease with an agreed rent never had
// anything mint its ledger account or its recurring rent clause (verticals.md
// "A signed lease never bills its rent") — clauseSatisfaction only ever
// converges a clause that already exists. This target opens the account,
// then the clause, mirroring cafe-domain's tabSettlement missing_account →
// directOp(CreateAccount) idiom; once the clause exists, clauseSatisfaction
// (above) owns billing it forever, so this target never dispatches
// DebitAccount itself.
const LeaseRentSettlementTarget = "leaseRentSettlement"

// Lenses returns the package's Lens declarations: `clauseSatisfaction` (§10.2
// actorAggregate covering every archetype — fixed/one-time, conditioned,
// judgment, recurring monthly (termed or not), and prorated computational
// clauses) and `leaseRentSettlement` (the lease → account → clause bootstrap
// chain feeding it, one rent clause per lease term).
func Lenses() []pkgmgr.LensSpec {
	return []pkgmgr.LensSpec{
		{
			CanonicalName:  ClauseSatisfactionTarget,
			Class:          "meta.lens",
			Adapter:        "nats-kv",
			Bucket:         "weaver-targets",
			Engine:         "full",
			Spec:           clauseSatisfactionSpec,
			ProjectionKind: "actorAggregate",
			Output: &pkgmgr.OutputDescriptorSpec{
				AnchorType:       "clause",
				OutputKeyPattern: ClauseSatisfactionTarget + ".{actorSuffix}",
				BodyColumns: []string{"violating", "missing_charge", "missing_inspection", "entityKey", "clauseKey",
					"accountKey", "amountCents", "inspectorKey", "period", "chargeValidUntil", "validFrom", "validUntil", "freshUntil"},
				EmptyBehavior: "delete",
				KeyColumn:     "entityId",
				Freshness:     "auto",
			},
		},
		{
			CanonicalName:  LeaseRentSettlementTarget,
			Class:          "meta.lens",
			Adapter:        "nats-kv",
			Bucket:         "weaver-targets",
			Engine:         "full",
			Spec:           leaseRentSettlementSpec,
			ProjectionKind: "actorAggregate",
			Output: &pkgmgr.OutputDescriptorSpec{
				AnchorType:       "leaseapp",
				OutputKeyPattern: LeaseRentSettlementTarget + ".{actorSuffix}",
				BodyColumns: []string{"violating", "missing_terms", "missing_account", "missing_clause", "missing_term", "missing_termShortened", "missing_deposit", "missing_depositReturn", "missing_lateFeeClause", "missing_lateFeeAmendment", "entityKey", "leaseAppKey", "accountKey",
					"leaseStart", "termStart", "leaseEnd", "termRentCents", "untermedClauseKey", "overrunClauseKey", "moveOutAt",
					"depositAmount", "depositCents", "endedAt", "depositClauseCount", "depositClauseKey",
					"lateFeeCents", "lateFeeClauseCount", "lateFeeClauseKey", "lateFeeClauseCents"},
				EmptyBehavior: "delete",
				KeyColumn:     "entityId",
				Freshness:     "auto",
			},
		},
	}
}

// leaseRentSettlementSpec is the one-row-per-lease bootstrap cypher: every
// approved, signed lease application (DecideLeaseApplication's own
// approve-readiness floor already requires the signature, scripts.go)
// projects a row and needs an agreed rent, then a ledger account, then a
// recurring monthly rent clause for its CURRENT term — and any monthly clause
// it already has minted without a term gets one, a recorded security deposit
// is minted as a one-time clause and returned once the tenancy ends, a
// recorded late-fee term is minted as a perArrearsEpisode clause and amended
// when the term changes — in its
// gap columns: `missing_account`/`missing_clause` mirror cafe-domain's tabSettlement
// missing_account → missing_charge shape exactly (lenses.go), except the
// second gap here mints a CLAUSE, not a charge, because rent's actual
// recurring billing is clauseSatisfaction's job (above) once the clause
// exists:
//
//   - `missing_terms` — requestedRent (leaseapp .terms aspect,
//     CreateLeaseApplication) is null: an application that skipped
//     moveInDate, or one created before requestedRent was captured, has an
//     approved lease with no agreed rent to bill. Weaver dispatches
//     BackfillLeaseTerms{leaseAppKey} (lease-signing, this target's own
//     targets.go), which upserts requestedRent from the unit's own listed
//     rent.
//   - `missing_account` — requestedRent is present and
//     l.ledgerAccount.data.accountKey is null (loftspace-ledger's guard
//     aspect, the same property this package's own DebitAccount pathway
//     reads — no link walk needed, mirroring cafe-domain's
//     l.cafeLedgerAccount.data.accountKey read). Weaver dispatches
//     LoftspaceCreateAccount{leaseAppKey} (loftspace-ledger, targets.go).
//   - `missing_clause` — requestedRent, the account and the lease's
//     .tenancy (leaseStart + leaseEnd, DecideLeaseApplication's first-approve
//     stamp) all exist, and no LIVE unconditioned monthly clause governs
//     this lease's CURRENT term yet: none whose .terms.validFrom equals
//     termStart, and none still untermed (count(DISTINCT CASE WHEN ...)
//     collapses each fan to a single existence check, the
//     clauseSatisfaction/objectLiveness idiom — lease-signing's own
//     freshBgComplete/payComplete columns use the identical
//     count(DISTINCT CASE WHEN ... THEN key ELSE null END) shape). The
//     period=monthly + conditioned<>true filter is deliberate, not
//     incidental: it is what lets this gate distinguish the auto-minted rent
//     clause from any OTHER clause a landlord might separately install on the
//     same lease (a one-time move-in fee, a conditioned pet fee) — those must
//     never suppress rent billing. Weaver dispatches
//     CreateClause{leaseAppKey, accountKey, amountCents: termRentCents,
//     period: "monthly", validFrom: termStart, validUntil: leaseEnd, prose:
//     <literal>} (this package).
//   - `missing_term` — the lease's .tenancy (leaseStart + leaseEnd) exists
//     and an unconditioned monthly clause governing it carries no term
//     (.terms.validFrom null): untermedClauseKey is that clause's key —
//     max(CASE WHEN ... THEN c.key ELSE null END), max() skipping nulls (the
//     clinic-ledger `max(tx.key)` dispatch-param precedent), so with several
//     untermed clauses one is termed per pass and the gap re-opens for the
//     next. Weaver dispatches BackfillClauseTerm{clauseKey: untermedClauseKey,
//     leaseAppKey} (this package), which stamps validFrom/validUntil from the
//     tenancy, moves the recorded due date onto the term's anniversary grid,
//     and re-keys a legacy `governs.lease.` link to `governs.leaseapp.`.
//     This gap lives HERE, on the lease anchor, because the inbound governs
//     walk resolves the clause from the link's source-type segment, which is
//     `clause` on both key shapes; the clause-anchored lens could never walk
//     a legacy link outbound (see clauseSatisfactionSpec). missing_term and
//     missing_clause are mutually exclusive by construction: an untermed
//     clause makes untermedClauseCount non-zero, which holds missing_clause
//     shut, and missing_clause requires untermedClauseCount = 0, which makes
//     untermedClauseKey null.
//   - `missing_termShortened` — the tenant has given notice (l.notice.data.
//     moveOutAt, LoftSpace's "a tenant gives notice, a lease ends early"
//     design) and a termed, unconditioned monthly clause governing this
//     lease runs past that date (validUntil > moveOutAt): overrunClauseKey is
//     that clause's key, the same max(CASE WHEN ... THEN c.key ELSE null END)
//     one-per-pass shape as untermedClauseKey above, so several overrunning
//     clauses (the original term's plus a renewal's) are shortened one at a
//     time and the gap re-opens for the next. Weaver dispatches
//     ShortenClauseTerm{clauseKey: overrunClauseKey, leaseAppKey} (this
//     package), which caps validUntil at max(moveOutAt, validFrom) and
//     completes the clause once its recorded due reaches the new validUntil.
//     Gated on validFrom <> null (a termed clause only — an untermed one is
//     missing_term's job first) and moveOutAt <> null (no notice, nothing to
//     shorten to). termClauseCount above still counts a clause this gap has
//     already shortened (its validFrom still equals termStart), so shortening
//     never re-opens missing_clause and mints a duplicate.
//     `AND validUntil > validFrom AND status.state <> 'completed'` keep a
//     clause the shortening has already reached out of candidacy: a term
//     collapsed to validFrom (validUntil == validFrom) still runs past a
//     moveOutAt that predates the term's own start, and a clause the script
//     has marked completed can still carry a validUntil past moveOutAt — both
//     satisfy every OTHER conjunct forever, so without this pair the row would
//     re-select the same clause on every mark-lease reclaim with nothing left
//     to change. Excluding either shape is also what makes the max()
//     one-per-pass selection actually reach every overrunning clause on a
//     lease: once the picked one drops out of candidacy, its sibling (a
//     renewal's clause overrunning the same notice, say) is the sole remaining
//     max() and is selected on the very next pass, never starved behind a
//     candidate that no longer needs picking.
//   - `missing_deposit` — the lease records a security deposit
//     (l.deposit.data.amount, the figure DecideLeaseApplication copied from
//     the unit's listing at approval — recorded at the event, so a listing
//     edited afterwards never re-prices what this lease owes), the ledger
//     account exists, and no clause governing this lease carries
//     .terms.purpose = 'deposit' (depositClauseCount, the same
//     count(DISTINCT CASE WHEN ... THEN c.key ELSE null END) existence check
//     termClauseCount is). `purpose` is the shape filter: a deposit clause
//     is a oneTime computational clause like any other one-time fee (a
//     lockout charge, a prorated amenity fee), so period alone cannot tell
//     it apart, and prose is free text a landlord-installed clause could
//     repeat — the recorded token is what a lens reads as the fact of what
//     the clause is for, the role period=monthly + conditioned<>true plays
//     for rent. depositClauseCount counts EVERY purpose=deposit clause, not
//     only the oneTime computational shape the return gap and ReturnDeposit
//     require: mint_clause refuses that token on any other shape, so a
//     clause tagged deposit that is not one can only be seeded or legacy
//     data — and the money-conservative reading of such a clause is "the
//     landlord has already installed a deposit", which holds the mint shut
//     (nothing is charged twice) and leaves the return visibly never
//     opening, rather than minting a second deposit beside it. Weaver
//     dispatches CreateClause{leaseAppKey, accountKey, amountCents:
//     depositCents, period: "oneTime", purpose: "deposit", prose: <literal>}
//     (this package); the oneTime archetype then bills it at once through
//     clauseSatisfaction → DebitAccount, which marks the clause completed.
//     Waits on the account exactly as missing_clause does (accountKey <>
//     null), never opens once the tenancy has ended (endedAt = null — a
//     deposit first minted after the move-out would be billed and refunded
//     in one breath, a round trip through the ledger for nothing), and is
//     independent of missing_clause: a lease with a rent clause and no
//     deposit clause opens only this gap, and a deposit clause never counts
//     toward termClauseCount (it is not monthly). Closes on the first mint —
//     one clause per lease, and a lease with no .deposit never opens it.
//     depositCents is the lens's own ×100 conversion of the recorded dollar
//     amount, CASE-guarded exactly like termRentCents below; mint_clause
//     coerces the float it arrives as to whole cents.
//   - `missing_depositReturn` — the tenancy has ended (l.tenancy.data.endedAt,
//     EndTenancy's recorded fact — the instant every consumer of a move-out
//     keys on, never the notice's intention or the term's scheduled end) and
//     a deposit clause governing this lease is CHARGED: depositClauseKey is
//     max(CASE WHEN purpose = 'deposit' AND period = 'oneTime' AND kind =
//     'computational' AND status.state = 'completed' THEN c.key ELSE null
//     END), the one-per-pass shape untermedClauseKey uses — two charged
//     deposit clauses on one lease (an amendment that re-minted one) are
//     returned one per pass, the second becoming the sole max() once the
//     first is marked returned. The archetype conjuncts are the same pin
//     ReturnDeposit's NotADeposit refusal carries: `completed` is the
//     charged conjunct because it is the state DebitAccount's own one-time
//     write leaves the clause in once the authorizing debit has posted, but
//     a MONTHLY clause reaches completed too — on its final period, after N
//     charges — and refunding one period's amount for it would be neither a
//     deposit nor a refund of what was collected. So a deposit minted but
//     not yet billed (still `active`) is never returned before it is
//     charged, a clause of any other shape is never a candidate, and a
//     clause ReturnDeposit has already marked `returned` drops out of the
//     CASE, which is what closes the gap: over every state ReturnDeposit
//     leaves (the credit, the idempotent no-op on an already-returned
//     clause), the column reads false. The accountKey <> null conjunct is what lets the
//     dispatch template row.accountKey (Weaver refuses a null param) — a
//     charged clause implies the account, but the gate states it rather
//     than relies on it. Weaver dispatches ReturnDeposit{leaseAppKey,
//     clauseKey: depositClauseKey, accountKey} (loftspace-ledger), which
//     posts the deposit back as a credit authorizedBy the clause and marks
//     it returned.
//   - `missing_lateFeeClause` — the lease records a late-fee term
//     (l.lateFee.data.amountCents, SetLateFee's stamp — the landlord's own
//     recorded intent, the way .deposit is DecideLeaseApplication's), the
//     ledger account exists, the tenancy has not ended, and no LIVE clause
//     governing this lease carries .terms.purpose = 'lateFee' with
//     .status.state = 'active' (lateFeeClauseCount, the depositClauseCount
//     shape narrowed to the active state: a superseded fee clause is
//     tombstoned and its status marked superseded, and the replacement is
//     the one that counts). Weaver dispatches CreateClause{leaseAppKey,
//     accountKey, amountCents: lateFeeCents, period: "perArrearsEpisode",
//     purpose: "lateFee", prose: <literal>} (this package). lateFeeCents is
//     the recorded integer-cents figure itself — SetLateFee refuses anything
//     but a positive integer — so no ×100 conversion sits between the term
//     and the clause. Closes on the mint; the clause then stays active for
//     the life of the lease (clauseSatisfaction bills period = oneTime and
//     monthly alone, so the fee is never charged at mint) and
//     loftspace-ledger's arrears evaluation posts its amount once per
//     arrears episode.
//   - `missing_lateFeeAmendment` — an active lateFee clause governs the
//     lease (lateFeeClauseKey, the max(CASE …) one-per-pass shape) and its
//     recorded amount (lateFeeClauseCents, the same CASE over
//     c.terms.data.amountCents) disagrees with the lease's current term.
//     Weaver dispatches SupersedeClause{clauseKey: lateFeeClauseKey,
//     leaseAppKey, accountKey, amountCents: lateFeeCents, period:
//     "perArrearsEpisode", prose} (this package), which tombstones the
//     amended clause and mints the replacement at the new amount — after
//     which the amended clause drops out of the active CASE, the
//     replacement's amount agrees, and both gaps read false. The gap's own
//     lateFeeClauseKey <> null conjunct is what lets the dispatch template
//     the key off the optional governs walk; accountKey <> null states the
//     account the replacement charges (the mint's own conjunct, restated so
//     the dispatch never templates a null); lateFeeClauseCount = 1 holds
//     the amendment shut when an operator hand-mint has left TWO active fee
//     clauses on one lease — max() would name one of them and the supersede
//     would leave the other, so the row stays visibly open-but-quiet for an
//     operator instead of ping-ponging (the arrears evaluation bills the
//     greatest key, the same max, meanwhile). A fee term set after this
//     episode's reminder went out bills from the next episode: the
//     evaluation reads the clause live on the send commit alone.
//
// The term the rent clause covers is the lease's CURRENT one. termStart is
// l.tenancy.data.termStart — the renewed term's start, which SignRenewal
// records as the previous leaseEnd — or, for a lease never renewed,
// leaseStart; leaseEnd is the current term's end either way. A signed
// renewal therefore opens missing_clause again (the original clause's
// validFrom is the old start, not termStart), and the clause it mints covers
// [old leaseEnd, new leaseEnd) at the renewal rent — the original clause
// stays live and expires by its own validUntil, so two clauses govern one
// lease, each converging alone. The untermed count is the migration guard:
// a legacy clause minted before terms existed carries no validFrom, and
// counting it keeps this gap shut until this lens's own missing_term
// has stamped its term, so a renewal can never double-cover a
// period. The gate's leaseStart <> null / leaseEnd <> null conjuncts are what
// let every templated param be non-null whenever it dispatches (Weaver
// refuses a row.<col> param that resolves null).
//
// `missing_account` and `missing_clause` each carry the `requestedRent <>
// null` conjunct so neither ever dispatches while the rent is still missing
// — the missing_terms remediation runs first, alone, and only once it
// converges do the account/clause gaps see a non-null requestedRent and open.
//
// termRent is the current term's rent in DOLLARS: l.tenancy.data.rentAmount
// when a renewal has recorded one, else requestedRent — a plain dollar
// figure, like every other rent-shaped field in LoftSpace
// (unit.listing.rentAmount, cmd/loftspace-app/web/app.js's "$"+rentAmount
// display) — but every ledger amount (CreateClause's amountCents,
// DebitAccount's amountCents) is integer CENTS. The ×100 conversion has to
// happen here, in the lens (the full engine's arithmetic BinaryOp,
// executor.go numericOp) — Weaver's GapActionSpec Params only ever
// substitute a row column verbatim or a literal (strategist.go resolveParam),
// never compute one — so termRentCents is the only column the missing_clause
// dispatch may template as amountCents; templating a raw dollar column would
// underbill by 100x. The conversion is guarded by a CASE WHEN: the full
// engine's numericOp errors on a nil operand rather than propagating null
// (unlike arithmetic in openCypher proper), so a bare `termRent * 100` would
// fail evaluation of the whole row — not just the column — for every
// missing_terms lease; the CASE WHEN keeps termRentCents null on that row
// instead, and the missing_clause conjunct above means CreateClause is never
// dispatched against a null amountCents in any case.
//
// Null table (equalsAny: null = null is TRUE, null <> x is TRUE; compareAny
// fails closed on nil): a lease with no .tenancy has termStart null, so an
// untermed clause's null validFrom counts as ITS term clause — harmless,
// since missing_clause already requires leaseStart <> null and never opens
// on such a lease; and missing_term requires the same, so an untermed clause
// on a tenancy-less lease stays on the untermed cadence, which is
// clauseSatisfaction's own rule for it.
const leaseRentSettlementSpec = `MATCH (l:leaseapp {key: $actorKey})
OPTIONAL MATCH (l)<-[:governs]-(c:clause)
WITH
  l.key AS entityKey,
  l.decision.data.value AS decision,
  l.terms.data.requestedRent AS requestedRent,
  l.ledgerAccount.data.accountKey AS accountKey,
  l.tenancy.data.leaseStart AS leaseStart,
  l.tenancy.data.leaseEnd AS leaseEnd,
  coalesce(l.tenancy.data.termStart, l.tenancy.data.leaseStart) AS termStart,
  coalesce(l.tenancy.data.rentAmount, l.terms.data.requestedRent) AS termRent,
  l.notice.data.moveOutAt AS moveOutAt,
  l.deposit.data.amount AS depositAmount,
  l.tenancy.data.endedAt AS endedAt,
  l.lateFee.data.amountCents AS lateFeeCents,
  count(DISTINCT CASE WHEN (c.terms.data.purpose = 'lateFee') AND (c.status.data.state = 'active') THEN c.key ELSE null END) AS lateFeeClauseCount,
  max(CASE WHEN (c.terms.data.purpose = 'lateFee') AND (c.status.data.state = 'active') THEN c.key ELSE null END) AS lateFeeClauseKey,
  max(CASE WHEN (c.terms.data.purpose = 'lateFee') AND (c.status.data.state = 'active') THEN c.terms.data.amountCents ELSE null END) AS lateFeeClauseCents,
  count(DISTINCT CASE WHEN (c.terms.data.purpose = 'deposit') THEN c.key ELSE null END) AS depositClauseCount,
  max(CASE WHEN (c.terms.data.purpose = 'deposit') AND (c.terms.data.period = 'oneTime') AND (c.terms.data.kind = 'computational') AND (c.status.data.state = 'completed') THEN c.key ELSE null END) AS depositClauseKey,
  count(DISTINCT CASE WHEN (c.terms.data.period = 'monthly') AND (c.terms.data.conditioned <> true) AND (c.terms.data.validFrom = coalesce(l.tenancy.data.termStart, l.tenancy.data.leaseStart)) THEN c.key ELSE null END) AS termClauseCount,
  count(DISTINCT CASE WHEN (c.terms.data.period = 'monthly') AND (c.terms.data.conditioned <> true) AND (c.terms.data.validFrom = null) THEN c.key ELSE null END) AS untermedClauseCount,
  max(CASE WHEN (c.terms.data.period = 'monthly') AND (c.terms.data.conditioned <> true) AND (c.terms.data.validFrom = null) THEN c.key ELSE null END) AS untermedClauseKey,
  max(CASE WHEN (c.terms.data.period = 'monthly') AND (c.terms.data.conditioned <> true) AND (c.terms.data.validFrom <> null) AND (l.notice.data.moveOutAt <> null) AND (c.terms.data.validUntil > l.notice.data.moveOutAt) AND (c.terms.data.validUntil > c.terms.data.validFrom) AND (c.status.data.state <> 'completed') THEN c.key ELSE null END) AS overrunClauseKey
WHERE (decision = 'approved')
RETURN
  entityKey AS actorKey,
  entityKey,
  entityKey AS leaseAppKey,
  accountKey,
  leaseStart,
  termStart,
  leaseEnd,
  untermedClauseKey,
  overrunClauseKey,
  moveOutAt,
  depositAmount,
  endedAt,
  depositClauseCount,
  depositClauseKey,
  lateFeeCents,
  lateFeeClauseCount,
  lateFeeClauseKey,
  lateFeeClauseCents,
  (CASE WHEN termRent = null THEN null ELSE (termRent * 100) END) AS termRentCents,
  (CASE WHEN depositAmount = null THEN null ELSE (depositAmount * 100) END) AS depositCents,
  (requestedRent = null) AS missing_terms,
  ((requestedRent <> null) AND (accountKey = null)) AS missing_account,
  ((requestedRent <> null) AND (accountKey <> null) AND (leaseStart <> null) AND (leaseEnd <> null) AND (termClauseCount = 0) AND (untermedClauseCount = 0)) AS missing_clause,
  ((untermedClauseKey <> null) AND (leaseStart <> null) AND (leaseEnd <> null)) AS missing_term,
  ((overrunClauseKey <> null)) AS missing_termShortened,
  ((accountKey <> null) AND (depositAmount <> null) AND (endedAt = null) AND (depositClauseCount = 0)) AS missing_deposit,
  ((accountKey <> null) AND (endedAt <> null) AND (depositClauseKey <> null)) AS missing_depositReturn,
  ((accountKey <> null) AND (lateFeeCents <> null) AND (endedAt = null) AND (lateFeeClauseCount = 0)) AS missing_lateFeeClause,
  ((accountKey <> null) AND (lateFeeClauseKey <> null) AND (lateFeeClauseCount = 1) AND (lateFeeCents <> null) AND (lateFeeClauseCents <> lateFeeCents)) AS missing_lateFeeAmendment,
  ((requestedRent = null) OR ((requestedRent <> null) AND (accountKey = null)) OR ((requestedRent <> null) AND (accountKey <> null) AND (leaseStart <> null) AND (leaseEnd <> null) AND (termClauseCount = 0) AND (untermedClauseCount = 0)) OR ((untermedClauseKey <> null) AND (leaseStart <> null) AND (leaseEnd <> null)) OR ((overrunClauseKey <> null)) OR ((accountKey <> null) AND (depositAmount <> null) AND (endedAt = null) AND (depositClauseCount = 0)) OR ((accountKey <> null) AND (endedAt <> null) AND (depositClauseKey <> null)) OR ((accountKey <> null) AND (lateFeeCents <> null) AND (endedAt = null) AND (lateFeeClauseCount = 0)) OR ((accountKey <> null) AND (lateFeeClauseKey <> null) AND (lateFeeClauseCount = 1) AND (lateFeeCents <> null) AND (lateFeeClauseCents <> lateFeeCents))) AS violating
`

// clauseSatisfactionSpec is the one-row-per-clause satisfaction cypher (§3.2
// of the design). Two independent gaps, never both live on the same clause
// (CreateClause writes exactly one of accountKey/amountCents (computational)
// or an inspector link (judgment)):
//
//   - `missing_charge` — true while the clause charges an account, is either
//     unconditioned or its conditionedOn target is still live, and a charge
//     is due: for a oneTime clause, no transaction `authorizedBy` it exists
//     yet (count(t.key) collapses the fan to a single existence check); for
//     a monthly clause, the term rule below.
//     "Conditioned" is a `terms.conditioned` data flag set at CreateClause
//     time (not inferred from link/target liveness — a tombstoned
//     conditionedOn TARGET makes condKey resolve null exactly like "never
//     conditioned" would, so only an explicit flag can tell them apart; the
//     flag is true only when CreateClause received a conditionedOnKey). The
//     gate reads `conditioned <> true`, not `conditioned = false`: a clause
//     whose `.terms` aspect has no `conditioned` key at all resolves
//     `conditioned` to null — `null = false` is false (equalsAny only equals
//     nil to nil), which would wrongly collapse the whole OR to false and
//     permanently suppress the charge for every such clause. `<> true`
//     correctly treats both `false` and absent (null) as "not conditioned."
//   - `missing_inspection` — true while the clause has an assigned inspector
//     (judgment) and no .inspection aspect has been written yet.
//
// A monthly clause minted without a term is termed by leaseRentSettlement's
// missing_term gap (above), not here: this lens is anchored on the clause,
// and a clause reaches its lease only through its own outbound governs link,
// whose key's target-type segment is what the adjacency index rebuilds the
// far endpoint from — so the legacy links spelled `governs.lease.` can be
// walked from the lease side (source type `clause`, correct) but never from
// the clause side. The lease-anchored lens walks the inbound hop that works
// on both key shapes.
//
// Null comparisons use the shipped `= null` / `<> null` idiom (lease-signing
// precedent), not `IS NULL`/`IS NOT NULL`: this grammar's
// oC_StringListNullOperatorExpression visitor deliberately passes those
// suffixes through unevaluated (full/visitor.go), so `IS NOT NULL` silently
// no-ops to the bare operand rather than a boolean. Every null-tested column
// here is itself a `.key`/aspect PROPERTY access (never a bare MATCH node
// variable): resolveProperty converts an unmatched OPTIONAL MATCH node's
// typed-nil `*nodeRef` to a clean interface nil via a direct pointer check,
// so `= null`/`<> null` sees a real nil — a bare node variable would still be
// a non-nil interface (Go's typed-nil-in-interface trap) and compare unequal
// to null even when unmatched.
//
// Deliberately does NOT gate the oneTime archetype on `.status.data.state =
// 'active'`: per the design's R3, a status-flip that removes the anchor from
// a WHERE-filtered match is the deferred negative/filter-retraction primitive
// (Fires 1+2 shipped the plain-lens retraction transport 2026-07-02, but
// wiring it into actorAggregate lenses like this one is a later target-diff
// increment — see the design's R3 v1 constraint). A oneTime
// clause instead relies purely on the upsert-safe signal — once the
// authorizing transaction exists, the gap flips false and STAYS false (the
// row lingers non-violating, which is harmless).
//
// The one-time arm bills period = 'oneTime' and nothing else — never
// "whatever is not monthly": the lens bills only the periods it understands.
// A perArrearsEpisode clause (the late fee, purpose=lateFee) is billed by
// loftspace-ledger's arrears evaluation once per arrears episode, and an arm
// written as period <> 'monthly' would bill it here at mint, at chargeCount =
// 0, before any rent was ever late. Pinned by TestClauseSatisfaction_
// PerArrearsEpisode_NeverBilledHere.
//
// The monthly rule (`period` is c.terms.data.period, always present — every
// CreateClause stamps it; period='oneTime' takes the chargeCount=0 check
// above, period='monthly' takes this arm, any other period neither):
//
//   - Every operand is stored graph data, never a clock reading. The term is
//     the clause's own .terms.validFrom/validUntil (both or neither). The
//     recorded due date is c.status.data.chargeValidUntil — DebitAccount
//     re-stamps it on every recurring charge: the next anniversary of
//     validFrom for a termed clause, postedAt + ~30d for an untermed one —
//     which is why a monthly clause's .status aspect is NOT purely audit like
//     the oneTime case (see clauseStatusAspectTypeDDL). The lapse is a FACT on
//     the clause: when the @at this lens arms fires, MarkExpired records the
//     scheduled instant in the clause's freshnessExpiry marker under this
//     target's own key, and `lapsedAt` is that entry. So the row is a pure
//     function of the subgraph, and two projections at different wall-clock
//     instants over the same graph agree.
//   - `periodStart` is the due date whose lapse bills the period
//     [periodStart, periodStart + 1 month): chargeValidUntil when one is
//     recorded, else validFrom (a termed clause never charged is due at its
//     start). It is computed in the aggregating WITH as a per-anchor scalar
//     beside the two columns it derives from.
//   - The charge is DUE when a recorded lapse reaches periodStart
//     (`lapsedAt >= periodStart`; MarkExpired records the scheduled instant,
//     so the start lapse holds with equality) — or, the untermed arm, when
//     neither a due date nor a term exists (`chargeValidUntil = null AND
//     validFrom = null`: an untermed clause never charged is due at once —
//     the untermed cadence, unchanged for a clause nothing terms). compareAny
//     answers false when either operand is nil, so a clause no timer has
//     fired on reads unlapsed, and a termed clause with no recorded due waits
//     for the lapse at validFrom rather than charging on the null.
//   - AND the period lies INSIDE the term: `validUntil = null OR periodStart
//     < validUntil`. A clause whose recorded due has reached validUntil has
//     billed its last period; a lapse recorded at or after that bills
//     nothing, and the row simply stops violating.
//   - `freshUntil` arms Weaver's temporal lane (internal/weaver/temporal.go)
//     the same way lease-signing's bgcheck does: while no recorded lapse
//     reaches periodStart and periodStart is inside the term, freshUntil
//     projects periodStart so an @at fires right when it lapses (nothing else
//     would CDC-trigger a re-read at that moment); once the lapse is recorded
//     (or for a oneTime clause, always, or once the term is fully billed)
//     freshUntil is null — no timer armed. A periodStart already in the past
//     is projected VERBATIM, so the overdue @at fires at once and records the
//     lapse that opens the gap — this is how a not-yet-started termed clause
//     arms its first charge at validFrom, and how a legacy clause
//     BackfillClauseTerm re-armed at a past anniversary bills its current
//     period at once.
//   - Proration needs NO lens change at all: a prorated clause's amountCents
//     was computed ONCE by CreateClause (exact Starlark bignum integer
//     arithmetic, ddls.go) and stored like any flat fee, so it flows through
//     the existing oneTime chargeCount=0 gate unchanged.
//
// validFrom/validUntil and periodStart ride through the aggregating WITH as
// per-anchor scalars (one .terms and one .status aspect per clause), so the
// row's grouping key widens only by values every row of a group already
// agrees on, and the non-DISTINCT count(t.key) product is untouched.
//
// Built with fmt.Sprintf so the target id comes from the constant the
// WeaverTargetSpec uses, which puts this Spec out of lint-lens-anchors'
// static reach; its advisory asks for a hand check for a narrowing range
// bound inside a NEGATED pattern, and there is none — the cypher has no
// negated relationship pattern at all, only scalar NOT comparisons.
var clauseSatisfactionSpec = fmt.Sprintf(`
MATCH (c:clause {key: $actorKey})
OPTIONAL MATCH (c)-[:chargesTo]->(a:account)
OPTIONAL MATCH (c)-[:conditionedOn]->(cond)
OPTIONAL MATCH (c)-[:requiresInspectionBy]->(insp:identity)
OPTIONAL MATCH (c)<-[:authorizedBy]-(t:transaction)
WITH
  c.key AS entityKey,
  a.key AS accountKey,
  cond.key AS condKey,
  insp.key AS inspectorKey,
  c.terms.data.amountCents AS amountCents,
  c.terms.data.conditioned AS conditioned,
  c.terms.data.period AS period,
  c.terms.data.validFrom AS validFrom,
  c.terms.data.validUntil AS validUntil,
  c.status.data.chargeValidUntil AS chargeValidUntil,
  CASE WHEN c.status.data.chargeValidUntil = null THEN c.terms.data.validFrom ELSE c.status.data.chargeValidUntil END AS periodStart,
  c.freshnessExpiry.data.byTarget.%[1]s AS lapsedAt,
  c.inspection.data.completed AS inspectionCompleted,
  count(t.key) AS chargeCount
RETURN
  entityKey AS actorKey,
  entityKey,
  entityKey AS clauseKey,
  accountKey,
  amountCents,
  inspectorKey,
  period,
  chargeValidUntil,
  validFrom,
  validUntil,
  ((accountKey <> null) AND ((conditioned <> true) OR (condKey <> null)) AND
   (((period = 'oneTime') AND (chargeCount = 0))
    OR ((period = 'monthly')
        AND (((chargeValidUntil = null) AND (validFrom = null)) OR (lapsedAt >= periodStart))
        AND ((validUntil = null) OR (periodStart < validUntil))))
  ) AS missing_charge,
  ((inspectorKey <> null) AND (inspectionCompleted = null)) AS missing_inspection,
  CASE WHEN (period = 'monthly') AND (periodStart <> null) AND NOT (lapsedAt >= periodStart)
            AND ((validUntil = null) OR (periodStart < validUntil))
       THEN periodStart ELSE null END AS freshUntil,
  (
    ((accountKey <> null) AND ((conditioned <> true) OR (condKey <> null)) AND
     (((period = 'oneTime') AND (chargeCount = 0))
      OR ((period = 'monthly')
          AND (((chargeValidUntil = null) AND (validFrom = null)) OR (lapsedAt >= periodStart))
          AND ((validUntil = null) OR (periodStart < validUntil)))))
    OR ((inspectorKey <> null) AND (inspectionCompleted = null))
  ) AS violating
`, ClauseSatisfactionTarget)
