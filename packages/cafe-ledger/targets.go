package cafeledger

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// WeaverTargets returns the package's meta.weaverTarget playbook (Contract #10
// §10.8). TargetID == the cafeArrearsReminders lens's OutputKeyPattern prefix
// (the §10.2↔§10.8 binding); LensRef resolves to that lens's in-batch NanoID at
// install.
//
// Three gaps, one op:
//
//   - missing_evaluation → directOp(EvaluateCafeArrears) over the account. The
//     op recomputes the FIFO-oldest open charge, rewrites .arrears, and — where
//     the recomputed due date has passed and nothing has gone out for it — fires
//     the notification. Whichever of the ways the gap opened (never evaluated,
//     marked stale by a partial payment, a timer fired at a due date nothing
//     was reminded for, or a historyTooLong flag recorded under a smaller
//     replay budget than the current one), the remediation is the same
//     recomputation.
//   - missing_replay_a / missing_replay_b → the same directOp. A history longer
//     than one page of the op's postedTo replay is consumed one page per
//     dispatch, and the checkpoint each page leaves on .arrears.replay
//     alternates its phase; the lens projects one gap per phase so each page
//     closes the gap that dispatched it and opens the other (the Gaps comment
//     below). The op reads the checkpoint, not the column.
//
// directOp, not a Loom pattern: a reminder is a single op, no multi-step
// externalTask flow — the same shape wellness-reminders' and clinic-reminders'
// reminder targets use. The bridge's notification send hangs off the op's own
// transactional outbox, not off a pattern.
//
// Params{accountKey: row.entityKey} routes the candidate account into the
// op's payload. The lease it is held for is NOT routed through Params: the
// row's own leaseAppKey column is OPTIONAL (an account with no live heldFor
// lease still projects a row, with a null leaseAppKey — lenses.go), and the
// strategist refuses to dispatch any row whose Params reference a null
// column (internal/weaver/strategist.go), which silently starves the gap
// forever for exactly the accounts most worth aging. The op instead resolves
// the lease itself, live, off the account's own heldFor out-link — the same
// state the row's column merely projects — so evaluation never depends on
// which shape the row happens to be in. leaseAppKey stays a projected column
// here purely for operator observability (the weaver-targets read model).
//
// Reads[row.entityKey] routes the account ROOT (the liveness guard's hydration).
// OptionalReads[row.entityKey.arrears] routes the account's own arrears state —
// absence-tolerant because no account carries the aspect until something opens
// an episode on it, and a required read's absence would HydrationMiss the very
// first evaluation of each one. That declaration is what auto-conditions the
// op's own .arrears write on the revision it was hydrated at (Contract #3 §3.2);
// the account DDL's derive_reads returns the same key whatever a dispatcher
// declares, so this states the read set and that guarantees it.
//
// Enumerations declares the bounded postedTo replay the op runs to recompute
// the head — the walk itself is nameable up front (the hub is the row's own
// account), the per-transaction .entry reads it discovers are not, which is
// exactly the class-(e) split CreditCafeAccount's own backfill replay declares
// itself under (opmetas.go). It also declares the heldFor walk lease_for_account
// runs to resolve the notification's lease live — likewise nameable up front off
// the same hub, and read-drift-checked exactly like postedTo.
func WeaverTargets() []pkgmgr.WeaverTargetSpec {
	return []pkgmgr.WeaverTargetSpec{
		{
			TargetID: CafeArrearsRemindersTarget,
			Description: "A resident who owes money on their café house tab past the net term is reminded once, " +
				"about the charge that has actually been sitting unpaid the longest. Paying it off ends the " +
				"episode; running a new tab up starts a fresh one.",
			LensRef: CafeArrearsRemindersTarget,
			// Three gaps, one op. missing_evaluation opens on an account that
			// needs evaluating (never evaluated, stale, or a recorded lapse at
			// its due date) and dispatches the FIRST page of the op's postedTo
			// replay. A history longer than one page leaves a checkpoint on
			// .arrears.replay whose phase flips on every page, and the lens
			// projects one gap per phase: the page written under phase a closes
			// missing_replay_a and opens missing_replay_b, whose dispatch
			// writes phase a again, and so on until the finalize page writes no
			// checkpoint and every gap is false. Each gap episode is therefore
			// exactly one dispatch — its mark and dispatch count are cleared by
			// the page it dispatched — so the engine's default retry budget
			// stands and a REJECTED page (a wall breach) is reclaimed up to
			// that budget, then parked loud under GapBudgetExhausted. The op
			// itself does not know which gap dispatched it: it reads the
			// checkpoint and continues, or starts. The three entries are
			// identical in every field; they differ only in which column
			// dispatches them.
			Gaps: map[string]pkgmgr.GapActionSpec{
				"missing_evaluation": arrearsEvaluationGap(),
				"missing_replay_a":   arrearsEvaluationGap(),
				"missing_replay_b":   arrearsEvaluationGap(),
			},
		},
	}
}

// arrearsEvaluationGap is the directOp(EvaluateCafeArrears) every gap on the
// cafeArrearsReminders target dispatches — the same params, reads and
// enumerations whichever column opened, because the op reads its own
// checkpoint to decide where in the replay it is.
func arrearsEvaluationGap() pkgmgr.GapActionSpec {
	return pkgmgr.GapActionSpec{
		Action:    "directOp",
		Operation: arrearsOp,
		// EvaluateCafeArrears is unique to this package's cafeaccount
		// vertexType DDL today, but pinned regardless — the defensive shape
		// cafe-domain's own directOps use, and the ledger operationType
		// namespace is global (permissions.go).
		Class:         "cafeaccount",
		Params:        map[string]string{"accountKey": "row.entityKey"},
		Reads:         []string{"row.entityKey"},
		OptionalReads: []string{"row.entityKey.arrears"},
		Enumerations: []pkgmgr.EnumerationSpec{
			{Hub: "row.entityKey", Relation: "postedTo", Direction: "in"},
			{Hub: "row.entityKey", Relation: "heldFor", Direction: "out"},
		},
	}
}
