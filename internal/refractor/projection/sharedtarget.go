package projection

import (
	"fmt"
	"sort"
	"strings"

	"github.com/operatinggraph/lattice/internal/refractor/lens"
)

// natsKVTarget is the Rule-side spelling of the NATS-KV target. Only that
// target's adapter has the whole-bucket listing the rule below exists for: a
// Postgres protected read model is a table of its own, and the shared
// actor_read_grants table's listing is already confined to the lens's declared
// grant_source by GrantWriterAdapter.ListKeys, which refuses to list at all
// without one.
const natsKVTarget = "nats_kv"

// SiblingLens is one ALREADY-ACTIVATED lens on the target a newcomer is
// joining, described by what its RUNNING pipeline does rather than by what its
// latest spec says.
//
// Every field is a running fact for the same reason: the hazard is what the
// sibling's NEXT EVENT will list and write, and a spec the pipeline has not
// applied answers a different question. A hot reload that would change any of
// them is refused rather than applied (cmd/refractor's hotReloadRefusal), which
// is what keeps these three readable off the registry at all.
type SiblingLens struct {
	// CanonicalName is what a refusal names the collision by.
	CanonicalName string
	// DiffRetraction reports whether the running pipeline diffs its target's
	// key set on every event.
	DiffRetraction bool
	// DiffRetractionPrefix is the prefix that diff is scoped to, "" when it
	// lists the whole target. Whether a prefix COULD be derived for this lens
	// says nothing about what its diff lists; only the installed value does.
	DiffRetractionPrefix string
	// Output is the §6.13 descriptor the running pipeline was installed from —
	// the only declaration of where a non-diffing sibling's own rows live.
	Output *lens.OutputDescriptorSpec
}

// DiffRetractionPrefix returns the key prefix a lens's own rows share in its
// target, and whether one is derivable at all.
//
// It reads the §6.13 Output descriptor through the same parser activation reads
// it with, so a descriptor this process would refuse cannot yield a prefix here.
// A lens with no descriptor has no prefix — its key layout is the cypher's, and
// nothing declares which leading token belongs to this lens.
//
// The prefix SCOPES a listing; it does not prove ownership on its own
// (OutputDescriptor.KeyPrefix's own doc: `cap.` admits `cap.roles.`). What makes
// it sufficient is the disjointness test in SharedTargetDiffRefusal, which
// refuses a bucket whose lenses' prefixes nest.
func DiffRetractionPrefix(r *lens.Rule) (string, bool) {
	return outputKeyPrefix(r.Output)
}

func outputKeyPrefix(spec *lens.OutputDescriptorSpec) (string, bool) {
	if spec == nil {
		return "", false
	}
	desc, err := ParseOutputDescriptor(spec)
	if err != nil {
		return "", false
	}
	return desc.KeyPrefix()
}

// SharedTargetDiffRefusal decides whether a lens may activate against a NATS-KV
// bucket, and the key prefix its own target diff must be scoped to.
//
// The hazard is one-sided and total: NatsKVAdapter.ListKeys enumerates the whole
// bucket and its key mapping filters only by SEGMENT COUNT, keeping a
// single-column key verbatim, so an unscoped diff on a shared bucket reads every
// sibling's key as a row this lens no longer produces and appends a Delete for
// it. Nothing downstream can recover those rows: the sibling that owns them will
// not re-project until its own next event.
//
// Three rules, and together they are what make the verdict independent of which
// lens loaded first:
//
//   - SCOPING IS UNCONDITIONAL. Any DiffRetraction lens with a derivable prefix
//     is scoped to it, siblings or not. Deriving the prefix only for a bucket
//     that already holds a sibling is what leaves a diff lens loading FIRST on
//     an empty bucket listing the whole of it forever — nothing re-scopes a
//     running pipeline when the sibling arrives.
//   - A LIVE UNSCOPED DIFF REFUSES THE NEWCOMER. The question asked of a sibling
//     is what its diff LISTS (SiblingLens.DiffRetractionPrefix, the installed
//     value), never what its rule would admit: a sibling whose diff enumerates
//     the whole bucket would delete this lens's rows on its next event. Refusing
//     the newcomer is the only disposition available here — the sibling is
//     running, and taking it down from another lens's activation path would be a
//     repair nobody asked for.
//   - PREFIXES MUST BE PROVABLY DISJOINT. On a bucket a diff reads at all, every
//     lens's key space must be a prefix no other lens's prefix contains:
//     KeyPrefix admits `cap.` for a lens whose keys are `cap.roles.*`, so two
//     NESTING prefixes are one diff listing the other's rows. A lens with no
//     derivable prefix leaves the disjointness unprovable, which is the same
//     refusal — "we cannot tell where its rows are" read as "not where ours are"
//     is the fail-open direction.
//
// A Postgres target is out of scope by construction rather than by omission —
// see natsKVTarget.
func SharedTargetDiffRefusal(r *lens.Rule, siblings []SiblingLens) (prefix string, refusal string) {
	if r.Into.Target != natsKVTarget {
		return "", ""
	}
	// keySpace is where THIS lens's rows live — its descriptor's prefix, diff or
	// not — and is what the disjointness rule below tests; prefix is the scoping
	// handed back to the caller, which only a diffing pipeline installs. Reading
	// the key space for a non-diff newcomer is what keeps the verdict symmetric:
	// a plain lens joining a scoped diff's bucket is admitted on the same proof
	// the diff would have made had the plain lens loaded first.
	keySpace, located := outputKeyPrefix(r.Output)
	if r.Into.DiffRetraction && located {
		prefix = keySpace
	}
	if len(siblings) == 0 {
		// The bucket is this lens's alone, so the whole listing IS its own rows
		// and there is no other key space to be disjoint from. The prefix above
		// still travels: it is what keeps the diff scoped once a sibling lands.
		return prefix, ""
	}

	for _, s := range siblings {
		if s.DiffRetraction && s.DiffRetractionPrefix == "" {
			return "", fmt.Sprintf(
				"%q already writes %q with an unscoped target-diff retraction, so its next event would retract every row this lens writes there",
				s.CanonicalName, r.Into.Bucket)
		}
	}

	// Disjointness is owed only on a bucket a diff actually reads. Lenses that
	// merely share a bucket and never list it can key their rows however they
	// like — nothing enumerates the bucket to mistake one lens's row for
	// another's.
	diffPresent := r.Into.DiffRetraction
	for _, s := range siblings {
		if s.DiffRetraction {
			diffPresent = true
		}
	}
	if !diffPresent {
		return prefix, ""
	}

	if !located {
		if r.Into.DiffRetraction {
			return "", fmt.Sprintf(
				"it declares target-diff retraction into %q, which %s already writes, and it declares no output key pattern the diff could be scoped by — "+
					"an unscoped listing of a shared bucket would retract its siblings' rows on this lens's first event",
				r.Into.Bucket, strings.Join(siblingNames(siblings), ", "))
		}
		return "", fmt.Sprintf(
			"%s already writes %q with a target-diff retraction scoped to its own key prefix, and this lens declares no output key pattern its own rows can be located by — "+
				"nothing can establish that the sibling's listing excludes them",
			strings.Join(diffSiblingNames(siblings), ", "), r.Into.Bucket)
	}

	for _, s := range siblings {
		sp, ok := siblingKeySpace(s)
		if !ok {
			return "", fmt.Sprintf(
				"it writes %q under the key prefix %q, and the sibling %q there declares no output key pattern its own rows can be located by — "+
					"nothing can establish that the bucket's target-diff listing keeps the two apart",
				r.Into.Bucket, keySpace, s.CanonicalName)
		}
		if strings.HasPrefix(keySpace, sp) || strings.HasPrefix(sp, keySpace) {
			return "", fmt.Sprintf(
				"its key prefix %q in %q nests with %q's %q, so one lens's target-diff listing enumerates the other's rows",
				keySpace, r.Into.Bucket, s.CanonicalName, sp)
		}
	}
	return prefix, ""
}

// siblingKeySpace is where a sibling's own rows live in the shared bucket.
//
// For a DiffRetraction sibling it is the INSTALLED scoping, which is both what
// its diff lists and where it writes; for every other sibling the descriptor is
// the only declaration of it there is.
func siblingKeySpace(s SiblingLens) (string, bool) {
	if s.DiffRetraction {
		return s.DiffRetractionPrefix, s.DiffRetractionPrefix != ""
	}
	return outputKeyPrefix(s.Output)
}

// siblingNames renders a sibling set for a refusal message, so an operator reads
// which lenses collide rather than only that some do. Sorted, because the
// registry the set is read from has no order and a refusal an operator compares
// across restarts must read the same each time.
func siblingNames(siblings []SiblingLens) []string {
	out := make([]string, 0, len(siblings))
	for _, s := range siblings {
		out = append(out, s.CanonicalName)
	}
	sort.Strings(out)
	return out
}

// diffSiblingNames is siblingNames narrowed to the siblings carrying a diff —
// the ones whose listing is the hazard a refusal about them names.
func diffSiblingNames(siblings []SiblingLens) []string {
	out := make([]string, 0, len(siblings))
	for _, s := range siblings {
		if s.DiffRetraction {
			out = append(out, s.CanonicalName)
		}
	}
	sort.Strings(out)
	return out
}
