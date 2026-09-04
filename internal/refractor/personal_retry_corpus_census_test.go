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
// carries live is the FLOOR plus the tripwire.
//
// THE TRIPWIRE IS ONE HOP SHORT, AND THE SECOND TEST BELOW IS THAT HOP. Each row
// is judged on a lens.Rule that corpusLensRule assembles FIELD BY FIELD out of a
// pkgmgr.LensSpec. A retry surface added to LensSpec and not threaded through
// corpusLensRule therefore leaves every judged rule's Retry at its zero value,
// and this census stays green while shipped lenses author retries — a census
// whose rules are built by hand can only pin the fields the hand wrote. The
// second test watches the two type declarations that coupling runs through, so
// the day the surface opens the failure names the field instead.
//
// The floor is what stops the census passing by finding nothing. A corpus walk
// that enumerated zero personal lenses would agree with every claim made above.
//
// DERIVATION COMMAND (re-run this, do not trust a number in a build note):
//
//	go test ./internal/refractor/ -run TestCorpusPersonalRetry -count=1 -v
package refractor_test

import (
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/pkgmgr"
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

// TestCorpusPersonalRetry_TheAuthoringSurfaceCarriesNoRetry closes the census
// above at both ends of the declaration it judges.
//
// The census reads exactly two type declarations, through corpusLensRule's
// hand-written assembly:
//
//   - pkgmgr.LensSpec is what a package can author. A retry field added there is
//     a field corpusLensRule does not thread, so every judged rule keeps a zero
//     Retry and the census agrees that nothing declares one — while shipped
//     lenses do. The fix at that point is to thread the new field into
//     corpusLensRule (and Refractor's own translateSpec), not to delete this.
//   - lens.RetryConfig is what the refusal reads. PersonalLensRetryRefusal
//     judges MaxAttempts alone, so a second way to arm the queue added beside it
//     would be a retry the refusal cannot see — on a personal lens, a write
//     replayed at a stale ordering token and dropped by the client.
//
// Both are field-universe assertions in the shape
// TestRuleState_RoundTripCarriesEveryField uses: the set is discovered from the
// type at test time, so a new member fails by NAME until someone decides what it
// is, rather than passing silently as a member nobody enumerated.
func TestCorpusPersonalRetry_TheAuthoringSurfaceCarriesNoRetry(t *testing.T) {
	t.Run("pkgmgr.LensSpec declares no retry field", func(t *testing.T) {
		spec := reflect.TypeOf(pkgmgr.LensSpec{})
		require.Greater(t, spec.NumField(), 10,
			"only %d LensSpec fields were discovered — the walk has stopped finding the authoring struct, and a gate that finds nothing passes everything", spec.NumField())

		var retryish []string
		for i := 0; i < spec.NumField(); i++ {
			if strings.Contains(strings.ToLower(spec.Field(i).Name), "retry") {
				retryish = append(retryish, spec.Field(i).Name)
			}
		}
		sort.Strings(retryish)
		require.Emptyf(t, retryish,
			"pkgmgr.LensSpec now carries %v — a package can author a retry, and corpusLensRule does not thread it, "+
				"so the census above judges a zero Retry on every lens and would stay green while a shipped personal lens declared one. "+
				"Thread the field through corpusLensRule (and Refractor's translateSpec) before relaxing this.", retryish)
	})

	t.Run("lens.RetryConfig is the two fields the refusal was written against", func(t *testing.T) {
		cfg := reflect.TypeOf(lens.RetryConfig{})
		declared := make([]string, 0, cfg.NumField())
		for i := 0; i < cfg.NumField(); i++ {
			declared = append(declared, cfg.Field(i).Name)
		}
		sort.Strings(declared)
		require.Equalf(t, []string{"Backoff", "MaxAttempts"}, declared,
			"lens.RetryConfig's field set changed to %v. PersonalLensRetryRefusal arms on MaxAttempts alone; "+
				"a new field that can also enable the queue is a retry the refusal cannot see, and on a personal lens that is "+
				"a write replayed at a stale ordering token and dropped by the client. Teach the refusal the new field, then update this list.", declared)
	})
}
