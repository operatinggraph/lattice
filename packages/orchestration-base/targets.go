package orchestrationbase

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// WeaverTargets returns the package's single meta.weaverTarget: unroutedTasks
// (Contract #10 §10.1 FR29 — "unrouted tasks surface; never silently
// dropped"). Its one gap, missing_claim, maps to the §10.8 `surface` action:
// no remediation is dispatched — the gap just raises/clears a named Health-KV
// issue (Contract #5 §5.5 issues[]) for as long as an open, role-queued task
// stays unclaimed past its own expiresAt. Manual intervention only;
// auto-escalation is an explicitly deferred follow-on (§10.1).
func WeaverTargets() []pkgmgr.WeaverTargetSpec {
	return []pkgmgr.WeaverTargetSpec{{
		TargetID: "unroutedTasks",
		LensRef:  "unroutedTasks",
		Gaps: map[string]pkgmgr.GapActionSpec{
			"missing_claim": {Action: "surface", IssueCode: "UnroutedTasks", IssueSeverity: "warning"},
		},
	}}
}
