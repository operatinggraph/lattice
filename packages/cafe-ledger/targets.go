package cafeledger

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// WeaverTargets returns the package's meta.weaverTarget playbook (Contract #10
// §10.8). TargetID == the cafeArrearsReminders lens's OutputKeyPattern prefix
// (the §10.2↔§10.8 binding); LensRef resolves to that lens's in-batch NanoID at
// install.
//
// The single gap → remediation:
//
//   - missing_evaluation → directOp(EvaluateCafeArrears) over the account. The
//     op recomputes the FIFO-oldest open charge, rewrites .arrears, and — where
//     the recomputed due date has passed and nothing has gone out for it — fires
//     the notification. Whichever of the three ways the gap opened (never
//     evaluated, marked stale by a partial payment, or a timer fired at a due
//     date nothing was reminded for), the remediation is the same recomputation,
//     which is why this target carries ONE gap rather than three.
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
			Gaps: map[string]pkgmgr.GapActionSpec{
				"missing_evaluation": {
					Action:    "directOp",
					Operation: arrearsOp,
					// EvaluateCafeArrears is unique to this package's cafeaccount
					// vertexType DDL today, but pinned regardless — the defensive
					// shape cafe-domain's own directOps use, and the ledger
					// operationType namespace is global (permissions.go).
					Class:         "cafeaccount",
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
