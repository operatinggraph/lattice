package identityhygiene

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// Lenses returns the package's Lens declarations. The single
// `duplicateCandidates` lens is a minimal, PII-free projection over the
// `duplicateOf` link identity-domain's CreateUnclaimedIdentity records
// (dedup-over-encrypted-pii-design.md §3.3): a row exists only for a
// flagged pair, keyed by the pair's NanoIDs. No PII, no engine-side
// matching — the match already happened at write time over plaintext the
// script legitimately held.
//
// Output bucket entry: `<primaryId>.<secondaryId>`. The CLI (a platform
// binary, the sanctioned Core-KV-read exception per CLAUDE.md P5) fetches
// the pair's `duplicateOf` link doc for criteria display and enumerates
// the secondary's live edges directly via bounded KVListKeys — the lens
// carries no edge columns (§3.3).
func Lenses() []pkgmgr.LensSpec {
	return []pkgmgr.LensSpec{
		{
			CanonicalName:  "duplicateCandidates",
			Class:          "meta.lens",
			Adapter:        "nats-kv",
			Bucket:         "duplicate-candidates",
			Engine:         "full",
			Spec:           duplicateCandidatesSpec,
			IntoKey:        []string{"primaryId", "secondaryId"},
			DiffRetraction: true,
		},
	}
}

// duplicateCandidatesSpec matches every live `duplicateOf` pair between two
// non-merged identities. Both pattern nodes are labeled and there are no
// anonymous nodes, so ReferencedLabels is exhaustive and the lens
// reprojects on identity-vertex events only, riding the shipped plain-lens
// aspect/link freshness transport. State filters are spelled as `=`/`OR`
// equality (not `IN` — silently dropped at parse-translation, §1.1-3) over
// the hydrated state aspect envelope (`a.state.data.value`, not the bare
// `a.state` property-sugar form, which yields the whole envelope map, not
// the scalar — §1.1-4). The output key is dot-free bare NanoIDs
// (`nanoIdFromKey`), required for DiffRetraction's segment-count check.
const duplicateCandidatesSpec = `MATCH (b:identity)-[:duplicateOf]->(a:identity)
WHERE (a.state.data.value = 'unclaimed' OR a.state.data.value = 'claimed')
  AND (b.state.data.value = 'unclaimed' OR b.state.data.value = 'claimed')
RETURN nanoIdFromKey(a.key) AS primaryId,
       nanoIdFromKey(b.key) AS secondaryId,
       a.key AS primaryKey,
       b.key AS secondaryKey`
