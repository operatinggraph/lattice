package identityhygiene

import "github.com/asolgan/lattice/internal/pkgmgr"

// Lenses returns the package's Lens declarations. The single
// `duplicateCandidates` lens projects candidate pairs (lo < hi by key)
// and enumerates the secondary's incident inbound + outbound link vertex
// keys via `collect(DISTINCT ...)` so the operator CLI can construct
// `MergeIdentity{edges: ...}` without scanning the graph itself.
//
// Output bucket entry: `flagged.identity.<lo-NanoID>.identity.<hi-NanoID>`.
//
// Phase-1 bound: a secondary with > 999 incident edges still projects;
// the `MergeIdentity` script's pre-flight rejects with `MergeBatchTooLarge`.
func Lenses() []pkgmgr.LensSpec {
	return []pkgmgr.LensSpec{
		{
			CanonicalName: "duplicateCandidates",
			Class:         "meta.lens",
			Adapter:       "nats-kv",
			Bucket:        "duplicate-candidates",
			Engine:        "full",
			Spec:          duplicateCandidatesSpec,
		},
	}
}

// duplicateCandidatesSpec matches pairs of non-merged identities that
// share an exact email, an exact phone, or a Levenshtein-name ratio
// >= 0.85, and for each pair enumerates the secondary's (higher-keyed
// member) inbound + outbound link vertex keys via `collect(DISTINCT)`.
//
// The threshold is hard-coded at 0.85; operator-configurable thresholds
// are a future improvement via a Lens `parameters` aspect.
const duplicateCandidatesSpec = `MATCH (a:identity), (b:identity)
WHERE a.key < b.key
  AND a.state IN ['unclaimed', 'claimed']
  AND b.state IN ['unclaimed', 'claimed']
  AND (
    (a.email = b.email AND a.email IS NOT NULL)
    OR (a.phone = b.phone AND a.phone IS NOT NULL)
    OR levenshteinRatio(a.name, b.name) >= 0.85
  )
OPTIONAL MATCH (b)<-[inL]-()
OPTIONAL MATCH (b)-[outL]->()
RETURN a.key AS primaryKey,
       b.key AS secondaryKey,
       {name: a.name, email: a.email, phone: a.phone, state: a.state} AS primaryDetail,
       {name: b.name, email: b.email, phone: b.phone, state: b.state} AS secondaryDetail,
       CASE
         WHEN a.email = b.email THEN 'exact-email'
         WHEN a.phone = b.phone THEN 'exact-phone'
         ELSE 'levenshtein-name'
       END AS criterion,
       collect(DISTINCT inL.key) AS secondaryInboundEdges,
       collect(DISTINCT outL.key) AS secondaryOutboundEdges`
