package augur

import "github.com/asolgan/lattice/internal/pkgmgr"

// WeaverTargets returns the package's single meta.weaverTarget: augurDispatch —
// the Fire 2b pickup that closes the Augur loop's last hop. Its one gap column,
// missing_dispatch, maps to the new opt-in §10.8 `proposedOp` action: unlike the
// three static actions, proposedOp sources its op + params from the ROW (the
// row-carried proposedAction/proposedParams the augurDispatchPending lens
// projects from an approved proposal's TRUSTED .gap + model-authored .proposed
// aspects), materialising them into the existing buildPlan after a dispatch-time
// re-validation (internal/weaver, buildProposedOpPlan). No static Gaps fields are
// needed — the action alone selects the dynamic-op contract.
func WeaverTargets() []pkgmgr.WeaverTargetSpec {
	return []pkgmgr.WeaverTargetSpec{{
		TargetID: "augurDispatch",
		LensRef:  "augurDispatchPending",
		Gaps: map[string]pkgmgr.GapActionSpec{
			"missing_dispatch": {Action: "proposedOp"},
		},
	}}
}
