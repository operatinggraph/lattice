package loftspaceledger

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// WeaverTargets returns the package's meta.weaverTarget playbook (Contract
// #10 §10.8): ArrearsRemindersTarget's single missing_evaluation gap — the
// wellness-ledger arrears mechanism (itself the cafe-ledger one) applied to
// the rent ledger, which likewise stores no balance.
//
//   - ArrearsRemindersTarget's missing_evaluation → directOp
//     (EvaluateLoftspaceArrears) over the account. The op recomputes the
//     FIFO-oldest open charge, rewrites .arrears, and — where the recomputed
//     reminder instant (the head's own recorded due date plus the package's
//     grace) has passed and nothing has gone out for it — fires the
//     notification. Whichever of the three ways the gap opened (never
//     evaluated, marked stale by a posted entry, or a timer fired at a
//     reminder instant nothing was reminded for), the remediation is the same
//     recomputation, which is why this target carries ONE gap rather than
//     three. Params{accountKey: row.entityKey} names only the anchor's own
//     key: the lease and the tenant the reminder addresses are NOT routed
//     through Params — an optional-hop column is null on every row where the
//     hop misses, and the strategist refuses to dispatch any row whose Params
//     reference a null column (internal/weaver/strategist.go), which would
//     silently starve exactly the accounts most worth aging. The op resolves
//     both itself, live, off the account's own heldFor out-link and the
//     lease's own applicationFor out-link.
//     Reads[row.entityKey] routes the account ROOT (the liveness guard's
//     hydration); OptionalReads[row.entityKey.arrears] routes the account's
//     own arrears state — absence-tolerant because no account carries the
//     aspect until an evaluation has run on it, and a required read's
//     absence would HydrationMiss the very first evaluation of each one.
//     That declaration is what auto-conditions the op's own .arrears write on
//     the revision it was hydrated at (Contract #3 §3.2); the account DDL's
//     derive_reads returns the same key whatever a dispatcher declares, so
//     this states the read set and that guarantees it. Enumerations declares
//     the bounded postedTo replay the op runs to recompute the head and the
//     heldFor walk that starts the tenant resolution — both nameable up front
//     off the row's own account; the per-transaction .entry reads, the lease
//     root read and the lease's applicationFor hop the walk discovers are
//     not, which is exactly the class-(e) split.
//
// The transaction ops (DebitAccount / LoftspaceRecordCharge / CreditAccount)
// are dispatched by no target of this package — semantic-contracts'
// clauseSatisfaction playbook dispatches DebitAccount — and every posted entry
// marks the account's recorded arrears state stale (post_entry, scripts.go),
// a bare update auto-conditioned only for a key the dispatch hydrated. The
// transaction DDL's own derive_reads guarantees the key whatever a dispatcher
// declares; opmetas.go's OptionalReads document it for the two person-facing
// ops.
func WeaverTargets() []pkgmgr.WeaverTargetSpec {
	return []pkgmgr.WeaverTargetSpec{
		{
			TargetID: ArrearsRemindersTarget,
			Description: "A tenant who owes rent on their lease account past the grace after its due date is reminded once, " +
				"about the charge that has actually been sitting unpaid the longest. Paying it off ends the " +
				"episode; a new charge after that starts a fresh one.",
			LensRef: ArrearsRemindersTarget,
			Gaps: map[string]pkgmgr.GapActionSpec{
				"missing_evaluation": {
					Action:    "directOp",
					Operation: arrearsOp,
					// EvaluateLoftspaceArrears is unique to this package's
					// account vertexType DDL, but pinned regardless — the
					// defensive shape every settlement target in the sibling
					// ledgers uses, and the operationType namespace is global
					// (permissions.go).
					Class:         "account",
					Params:        map[string]string{"accountKey": "row.entityKey"},
					Reads:         []string{"row.entityKey"},
					OptionalReads: []string{"row.entityKey.arrears"},
					Enumerations: []pkgmgr.EnumerationSpec{
						{Hub: "row.entityKey", Relation: "postedTo", Direction: "in"},
						{Hub: "row.entityKey", Relation: "heldFor", Direction: "out"},
					},
				},
			},
		},
	}
}
