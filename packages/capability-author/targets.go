package capabilityauthor

import "github.com/asolgan/lattice/internal/pkgmgr"

// WeaverTargets returns the package's meta.weaverTarget playbook (Contract
// #10 §10.8) — the escalation dispatch (design §3.4). One gap,
// missing_authoring, dispatches SELF-ANCHORED: Subject is row.entityKey (the
// candidate's own echoed key, §10.2), not a neighbor-projected column — every
// weaver-targets row already echoes its own anchor key as entityKey, so this
// is the ordinary case; lease-signing's row.applicant is that package's own
// special neighbor-projected case, not the default shape to mirror.
func WeaverTargets() []pkgmgr.WeaverTargetSpec {
	return []pkgmgr.WeaverTargetSpec{{
		TargetID: "capabilityAuthorDispatch",
		LensRef:  "capabilityAuthorPending",
		Gaps: map[string]pkgmgr.GapActionSpec{
			"missing_authoring": {Action: "triggerLoom", Pattern: "capabilityAuthor", Subject: "row.entityKey"},
		},
	}}
}
