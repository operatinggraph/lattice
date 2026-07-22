package objectsbase

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// WeaverTargets returns the package's meta.weaverTarget playbook (Contract #10
// §10.8) — the v1b GC's Loop A (orphan → reclaim). TargetID == the
// objectLiveness lens's OutputKeyPattern prefix (the §10.2↔§10.8 binding);
// LensRef resolves to that lens's in-batch NanoID at install.
//
// The single gap `missing_owner` (an orphaned object — zero live links)
// dispatches directOp(TombstoneObject): a direct op submission (NO Loom) that
//
//   - templates the object key + its linkEpoch from the lens row
//     (row.entityKey / row.linkEpoch), and
//   - routes the object key into the op's ContextHint.Reads (row.entityKey) so
//     TombstoneObject hydrates the object vertex for its epoch-CAS + self-OCC.
//
// Weaver also auto-injects the candidate's row revision as expectedRevision;
// TombstoneObject ignores it (the row revision is not the vertex revision) and
// relies on the linkEpoch CAS + the hydrated-revision self-OCC instead (§20).
// Every templated value (row.entityKey, row.linkEpoch) is an objectLiveness
// BodyColumn — the §10.2↔§10.8 column seam.
func WeaverTargets() []pkgmgr.WeaverTargetSpec {
	return []pkgmgr.WeaverTargetSpec{{
		TargetID: "objectLiveness",
		LensRef:  "objectLiveness",
		Gaps: map[string]pkgmgr.GapActionSpec{
			"missing_owner": {
				Action:    "directOp",
				Operation: "TombstoneObject",
				Params: map[string]string{
					"objectKey":     "row.entityKey",
					"expectedEpoch": "row.linkEpoch",
					"storeName":     "row.storeName",
				},
				Reads: []string{"row.entityKey"},
			},
		},
	}}
}
