// Personal-lens retry census —
// personal-lens-delta-publication-design.md §4.2.
//
// A personal lens must not declare a retry queue: the queue replays a captured
// row at its ORIGINAL, lower ordering token, which lands below whatever keyset
// frame a later event has since published, and the client drops it for any key
// it does not already hold attributed at that revision. The write the queue
// exists to rescue is lost rather than retried.
//
// projection.InstallPersonalLens refuses such a lens. This census asks the same
// predicate — projection.PersonalLensRetryRefusal, the runtime symbol the
// install calls, never a restatement of it — of every personal lens the shipped
// corpus installs, so the refusal is pinned as excluding NONE of them.
//
// WHAT IS AND IS NOT PINNED HERE. The rule each row is judged on is the one
// corpusLensRule builds, which mirrors Refractor's own translateSpec: it carries
// the declaration a package can author. Today no package can author a retry at
// all — pkgmgr's LensSpec has no retry field and translateSpec threads none — so
// every row's refusal is empty for a structural reason, and what this census
// carries live is the FLOOR plus the tripwire: the day a retry surface is added
// to the authoring path, an edge-manifest lens taking it turns this red here
// rather than turning into a silently-lost write on a device.
//
// The floor is what stops the census passing by finding nothing. A corpus walk
// that enumerated zero personal lenses would agree with every claim made above.
//
// DERIVATION COMMAND (re-run this, do not trust a number in a build note):
//
//	go test ./internal/refractor/ -run TestCorpusPersonalRetry -count=1 -v
package refractor_test

import (
	"sort"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/lens"
	"github.com/operatinggraph/lattice/internal/refractor/projection"
)

// minCorpusPersonalLenses is the floor on how many DISTINCT personal lenses the
// walk must reach. The shipped edge manifest carries sixteen
// (corpusPersonalDerivationVerdicts enumerates their nineteen cyphers); the
// floor sits below that so adding or retiring one lens does not move it, while
// a walk that stopped reaching the manifest fails.
const minCorpusPersonalLenses = 12

func TestCorpusPersonalRetry_NoPersonalLensDeclaresARetryQueue(t *testing.T) {
	refusals := map[string]string{}
	forEachCorpusCypher(t, func(name, _ string, rule *lens.Rule, _, declaredPersonal bool) {
		if !declaredPersonal {
			return
		}
		refusals[lensNameOf(name)] = projection.PersonalLensRetryRefusal(rule)
	})

	require.GreaterOrEqualf(t, len(refusals), minCorpusPersonalLenses,
		"the walk reached only %d personal lenses — a census that finds none agrees with everything", len(refusals))

	var refused []string
	for name, reason := range refusals {
		if reason != "" {
			refused = append(refused, name+": "+reason)
		}
	}
	sort.Strings(refused)
	require.Emptyf(t, refused,
		"these personal lenses would be REFUSED at install by the retry gate:\n%v", refused)
}
