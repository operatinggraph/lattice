package loftspaceledger

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// WeaverTargets returns the package's meta.weaverTarget playbook (Contract
// #10 §10.8): ArrearsRemindersTarget's three gaps → ONE remediation — the
// wellness-ledger arrears mechanism (itself the cafe-ledger one) applied to
// the rent ledger, which likewise stores no balance, plus clinic-ledger's
// resumable-replay phase gaps (verticals-arrears-resumable-replay-2026-09-16.md).
//
//   - missing_evaluation → directOp(EvaluateLoftspaceArrears) over the
//     account. The op recomputes the FIFO-oldest open charge, rewrites
//     .arrears, and — where the recomputed reminder instant (the head's own
//     recorded due date plus the package's grace) has passed and nothing has
//     gone out for it — fires the notification. Whichever of the ways the
//     gap opened (never evaluated, marked stale by a posted entry, a timer
//     fired at a reminder instant nothing was reminded for, or a
//     historyTooLong flag recorded under a smaller replay budget than the
//     current one), the remediation is the same recomputation.
//   - missing_replay_a / missing_replay_b → the same directOp. A history
//     longer than one page of the op's postedTo replay is consumed one page
//     per dispatch, and the checkpoint each page leaves on .arrears.replay
//     alternates its phase; the lens projects one gap per phase so each page
//     closes the gap that dispatched it and opens the other (lenses.go). The
//     op reads the checkpoint, not the column.
//
// Params{accountKey: row.entityKey} names only the anchor's own key: the
// lease and the tenant the reminder addresses are NOT routed through Params —
// an optional-hop column is null on every row where the hop misses, and the
// strategist refuses to dispatch any row whose Params reference a null column
// (internal/weaver/strategist.go), which would silently starve exactly the
// accounts most worth aging. The op resolves both itself, live, off the
// account's own heldFor out-link and the lease's own applicationFor out-link.
//
// Reads[row.entityKey] routes the account ROOT (the liveness guard's
// hydration); OptionalReads[row.entityKey.arrears] routes the account's own
// arrears state — absence-tolerant because no account carries the aspect
// until an evaluation has run on it, and a required read's absence would
// HydrationMiss the very first evaluation of each one. That declaration is
// what auto-conditions the op's own .arrears write on the revision it was
// hydrated at (Contract #3 §3.2); the account DDL's derive_reads returns the
// same key whatever a dispatcher declares, so this states the read set and
// that guarantees it. Enumerations declares the bounded postedTo replay the
// op runs to recompute the head and the heldFor walk that starts the tenant
// resolution — both nameable up front off the row's own account; the
// per-transaction .entry reads, the lease root read and the lease's
// applicationFor hop the walk discovers are not, which is exactly the
// class-(e) split. The three entries are identical in every field; they
// differ only in which column dispatches them, so each gap episode is
// exactly one dispatch (its mark and dispatch count are cleared by the page
// it dispatched) and the engine's default retry budget stands.
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
				"missing_evaluation": arrearsEvaluationGap(),
				"missing_replay_a":   arrearsEvaluationGap(),
				"missing_replay_b":   arrearsEvaluationGap(),
			},
		},
	}
}

// arrearsEvaluationGap is the directOp(EvaluateLoftspaceArrears) every gap on
// the loftspaceArrearsReminders target dispatches — the same params, reads and
// enumerations whichever column opened, because the op reads its own
// checkpoint to decide where in the replay it is.
func arrearsEvaluationGap() pkgmgr.GapActionSpec {
	return pkgmgr.GapActionSpec{
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
	}
}
