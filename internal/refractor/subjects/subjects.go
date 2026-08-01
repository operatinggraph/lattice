package subjects

import (
	"fmt"
	"sort"
	"strings"

	"github.com/operatinggraph/lattice/internal/substrate/keys"
)

// validateToken panics if s is empty or contains NATS-reserved characters
// (`.`, `*`, `>`) or whitespace. Call at the top of every builder function.
func validateToken(name, s string) {
	if s == "" {
		panic(fmt.Sprintf("subjects: %s must not be empty", name))
	}
	if strings.ContainsAny(s, ".*> \t\n\r") {
		panic(fmt.Sprintf("subjects: %s %q contains invalid NATS token character", name, s))
	}
}

// DLQ returns the NATS subject for the Refractor DLQ for the given lensId.
// Team segment removed per Deviation 4 (team is vestigial in the post-morph code).
func DLQ(lensID string) string {
	validateToken("lensID", lensID)
	return fmt.Sprintf("lattice.refractor.dlq.%s", lensID)
}

// Metrics returns the NATS subject for Refractor per-lens consumer lag metrics.
func Metrics(lensID string) string {
	validateToken("lensID", lensID)
	return fmt.Sprintf("lattice.refractor.metrics.%s", lensID)
}

// Audit returns the NATS subject a lens publishes its audit entries on.
func Audit(lensID string) string {
	validateToken("lensID", lensID)
	return fmt.Sprintf("lattice.refractor.audit.%s", lensID)
}

// AuditFilter returns the wildcard NATS subject covering every lens's audit
// subject — the subject filter for the single consolidated audit stream all
// lenses share.
func AuditFilter() string {
	return "lattice.refractor.audit.>"
}

func AdjKey(nodeID string) string {
	validateToken("nodeID", nodeID)
	return fmt.Sprintf("adj.%s", nodeID)
}

// PersonalSync returns the NATS subject for a Personal Lens's per-identity
// delta stream (personal-secure-lens-design.md Fire 1): prefix is the lens's
// configured subjectPrefix (a multi-segment convention, e.g.
// "lattice.sync.user" — not itself a single token) and actorID is the
// recipient identity, validated as a single subject token.
func PersonalSync(prefix, actorID string) string {
	if prefix == "" {
		panic("subjects: prefix must not be empty")
	}
	validateToken("actorID", actorID)
	return prefix + "." + actorID
}

// lensDurablePrefix is the common prefix of every durable JetStream consumer
// the Refractor creates on the Core KV stream. It is deliberately shared with
// names that are NOT per-lens durables — the adjacency bootstrapper's
// `refractor-adjacency` and the lens source's
// `refractor-lens-source-<instance>-<nonce>` — so the prefix alone never
// identifies a lens durable. ParseLensDurable is the discriminator.
const lensDurablePrefix = "refractor-"

// LensDurable returns the durable JetStream consumer name for the lens
// pipeline projecting ruleID. It is the single constructor of that name: the
// activation path and the reconciliation that deletes orphaned durables
// (health.DurableJanitor) must agree on it by construction, because a drift
// between "what we create" and "what we recognize as ours" would have the
// janitor delete a live lens's consumer.
//
// Unlike the subject builders above this does not validate ruleID, because a
// lens's rule ID is its spec body's `id` — author-supplied and checked only
// for non-emptiness (lens.translateSpec) — so a token violation here is
// ordinary bad input from one lens, not a programming error in the caller.
// Rejecting it by panic would take the whole process down over a single
// malformed spec; a name NATS refuses fails just that lens's activation, and
// ParseLensDurable declines to recognize it, so no reconciliation ever acts
// on it.
func LensDurable(ruleID string) string {
	return lensDurablePrefix + ruleID
}

// ParseLensDurable extracts the lens rule ID from a durable consumer name
// produced by LensDurable, reporting false for any name that is not one.
//
// The NanoID check is what makes the answer safe rather than merely
// prefix-shaped: a lens rule ID is the `vtx.meta.<NanoID>` id (Contract #1),
// so `refractor-adjacency` and `refractor-lens-source-<instance>-<nonce>`
// both fail it and are never mistaken for a lens's durable. Anything the
// Refractor did not create through LensDurable is likewise rejected, which is
// the direction that matters: the caller deleting on a true answer must never
// get one for a consumer it does not own.
func ParseLensDurable(name string) (string, bool) {
	id, ok := strings.CutPrefix(name, lensDurablePrefix)
	if !ok || !keys.IsValidNanoID(id) {
		return "", false
	}
	return id, true
}

// CoreKVStream returns the JetStream stream name for the given NATS KV bucket.
// NATS convention: KV bucket "foo" is backed by stream "KV_foo".
func CoreKVStream(bucket string) string {
	return "KV_" + bucket
}

// CoreKVFilter returns the JetStream filter subject that covers all entries in
// the given NATS KV bucket. Used when creating consumers on the Core KV stream.
func CoreKVFilter(bucket string) string {
	return "$KV." + bucket + ".>"
}

// CoreKVVertexFilter returns the JetStream filter subject that covers every
// vertex-root AND aspect key of the given label in the given NATS KV bucket
// ($KV.<bucket>.vtx.<label>.>): an aspect key is its owner vertex's key plus
// one segment (Contract #1's vtx.<type>.<id>.<localName> shape), so the same
// wildcard tail catches both.
func CoreKVVertexFilter(bucket, label string) string {
	validateToken("label", label)
	return "$KV." + bucket + ".vtx." + label + ".>"
}

// CoreKVLinkSourceFilter returns the JetStream filter subject that covers
// every link key whose SOURCE type is label ($KV.<bucket>.lnk.<label>.>) —
// Contract #1's lnk.<typeA>.<idA>.<relation>.<typeB>.<idB> shape with typeA
// pinned.
func CoreKVLinkSourceFilter(bucket, label string) string {
	validateToken("label", label)
	return "$KV." + bucket + ".lnk." + label + ".>"
}

// CoreKVLinkTargetFilter returns the JetStream filter subject that covers
// every link key whose TARGET type is label ($KV.<bucket>.lnk.*.*.*.<label>.>)
// — the same six-segment link shape with typeB pinned and typeA/idA/relation
// wildcarded.
func CoreKVLinkTargetFilter(bucket, label string) string {
	validateToken("label", label)
	return "$KV." + bucket + ".lnk.*.*.*." + label + ".>"
}

// CoreKVNarrowedFilters returns the deduped, deterministically-ordered set of
// JetStream filter subjects that together cover every Core KV key a plain
// full-engine lens's referenced labels can affect: for each label, its vertex
// form (CoreKVVertexFilter) plus its link-source and link-target forms
// (CoreKVLinkSourceFilter / CoreKVLinkTargetFilter). Duplicate labels are
// collapsed before building, so the returned slice is safe to pass straight
// into substrate.ConsumerSpec.FilterSubjects.
//
// The forms can legally overlap ACROSS labels on one consumer: a link from
// label L1 to label L2 matches both L1's source form and L2's target form,
// and JetStream delivers it once regardless of how many filters in the set
// match — a valid union, not the SUBSET-shaped pair the server rejects
// (nats-server v2.14.0 server/consumer.go's subjectIsSubsetMatch, :882, used
// by the overlap check at :882-884). No two forms produced by this function,
// for any labels (equal or distinct), are ever a subset of one another: every
// form's fixed-position token (vtx vs lnk at segment 3, or the label itself)
// differs from every other form's at some token position neither side
// wildcards, which is exactly what defeats subset matching — see
// subjects_test.go's pairwise proof.
func CoreKVNarrowedFilters(bucket string, labels []string) []string {
	deduped := make(map[string]struct{}, len(labels))
	sortedLabels := make([]string, 0, len(labels))
	for _, l := range labels {
		if _, dup := deduped[l]; dup {
			continue
		}
		deduped[l] = struct{}{}
		sortedLabels = append(sortedLabels, l)
	}
	sort.Strings(sortedLabels)

	out := make([]string, 0, len(sortedLabels)*3)
	for _, l := range sortedLabels {
		out = append(out, CoreKVVertexFilter(bucket, l), CoreKVLinkSourceFilter(bucket, l), CoreKVLinkTargetFilter(bucket, l))
	}
	return out
}
