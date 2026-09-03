package subjects

import (
	"fmt"
	"sort"
	"strings"

	"github.com/operatinggraph/lattice/internal/substrate/keys"
)

// ValidToken reports whether s can be spelled as one NATS subject token: it is
// non-empty and free of the reserved characters (`.`, `*`, `>`) and whitespace.
// It is the predicate form of the rule the builders below enforce, for a caller
// holding an id from elsewhere that would rather skip it than be panicked at.
func ValidToken(s string) bool {
	return s != "" && !strings.ContainsAny(s, ".*> \t\n\r")
}

// validateToken panics if s is empty or contains NATS-reserved characters
// (`.`, `*`, `>`) or whitespace. Call at the top of every builder function.
func validateToken(name, s string) {
	if s == "" {
		panic(fmt.Sprintf("subjects: %s must not be empty", name))
	}
	if !ValidToken(s) {
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

// AdjMarkKey returns the adjacency KV key carrying nodeID's overflow latch:
// the marker saying this node's edge set is too large to keep in its AdjKey
// document, so its edge reads enumerate Core KV's link keyspace instead. The
// mark's PRESENCE is the whole signal; its body is an operator breadcrumb.
//
// It deliberately lives in the same bucket as AdjKey but under its own first
// segment. Sharing the bucket keeps the mark and the document readable in one
// batched request (the read must be atomic, or a node latching between two
// sequential reads returns the just-emptied document as authoritative); the
// distinct segment keeps it out of every `adj.`-prefixed scan and, more
// importantly, out of sight of a binary that predates the latch — such a
// binary rewrites the emptied document harmlessly and can never un-mark the
// node, which a sentinel value inside the document itself would let it do.
func AdjMarkKey(nodeID string) string {
	validateToken("nodeID", nodeID)
	return fmt.Sprintf("adjmark.%s", nodeID)
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

// edgeSyncDurablePrefix is the fixed prefix of every Edge device's SYNC
// durable JetStream consumer name.
const edgeSyncDurablePrefix = "edge-sync-"

// EdgeSyncDurable returns the durable JetStream consumer name a device's
// Personal-Lens delta feed binds to on the SYNC stream
// (personal-secure-lens-design.md Fire 1). The name is STABLE per
// identity+device — not per-boot-nonce — so a device keeps exactly one
// durable across restarts and resumes from its own ack floor instead of
// replaying the whole retained stream.
//
// The format is load-bearing beyond this package: the Gateway's NATS
// auth-callout grants exactly this name's consumer subjects
// (internal/gateway/natsauth's PermissionsFor), so every constructor of this
// name — the Edge node's own sync.Manager, Loupe's fleet inspector, and
// Refractor's own reconciliation — must derive it here rather than
// re-spelling it, or a drift between "what was created" and "what is
// recognized" strands a live device's durable or misattributes a dead one.
func EdgeSyncDurable(identityID, deviceID string) string {
	return edgeSyncDurablePrefix + identityID + "-" + deviceID
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

// MaxNarrowedFilterLabels caps how many distinct vertex-type labels a narrowed
// Core KV consumer filter may be derived from. Each label expands to three
// filter-subject forms (CoreKVNarrowedFilters), so this bounds how large the
// FilterSubjects slice JetStream evaluates per delivered message gets before
// the broad `$KV.<bucket>.>` filter — simpler, and just as fail-safe — is the
// better choice.
//
// It lives here, beside the builder whose output it bounds, because TWO
// packages must agree on it and neither owns the other:
// internal/refractor/pipeline decides at runtime whether a lens narrows, and
// internal/pkgmgr refuses at install time a lens whose worst-case expanded
// label count would cross it (dynamic-type-taxonomy-design.md §10.2 — an
// abstract type's LeafBudget defaults to exactly this value). Two constants
// tied only by a comment drift the moment either side moves, and the drift is
// silent in the direction that matters: an install-time gate computing against
// a stale cap either refuses lenses the runtime would have narrowed, or waves
// through lenses it would not.
const MaxNarrowedFilterLabels = 8

// MaxNarrowedFilterSubjects caps the TOTAL filter-subject count of a
// relation-narrowed set, whose size is |labels| x (1 + 2|relations|) and so is
// no longer bounded by MaxNarrowedFilterLabels alone. It is set to exactly the
// relation-blind ceiling (MaxNarrowedFilterLabels x 3, the width
// CoreKVNarrowedFilters emits), so no lens that narrows by label can stop
// narrowing because the relation dimension was added: a lens over budget here
// falls back to the relation-blind narrowed set, and only a lens over the LABEL
// budget falls all the way back to the broad filter.
const MaxNarrowedFilterSubjects = MaxNarrowedFilterLabels * 3

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
	sortedLabels := dedupeSorted(labels)

	out := make([]string, 0, len(sortedLabels)*3)
	for _, l := range sortedLabels {
		out = append(out, CoreKVVertexFilter(bucket, l), CoreKVLinkSourceFilter(bucket, l), CoreKVLinkTargetFilter(bucket, l))
	}
	return out
}

// CoreKVLinkSourceRelationFilter returns the JetStream filter subject that
// covers every link key whose SOURCE type is label AND whose relation is
// relation ($KV.<bucket>.lnk.<label>.*.<relation>.>) — Contract #1's
// lnk.<typeA>.<idA>.<relation>.<typeB>.<idB> shape with typeA and relation both
// pinned and idA wildcarded (a NanoID is exactly one token). The trailing `>`
// covers typeB.idB and any tail beyond it.
func CoreKVLinkSourceRelationFilter(bucket, label, relation string) string {
	validateToken("label", label)
	validateToken("relation", relation)
	return "$KV." + bucket + ".lnk." + label + ".*." + relation + ".>"
}

// CoreKVLinkTargetRelationFilter returns the JetStream filter subject that
// covers every link key whose TARGET type is label AND whose relation is
// relation ($KV.<bucket>.lnk.*.*.<relation>.<label>.>) — the same six-segment
// link shape with relation and typeB pinned and typeA/idA wildcarded.
func CoreKVLinkTargetRelationFilter(bucket, label, relation string) string {
	validateToken("label", label)
	validateToken("relation", relation)
	return "$KV." + bucket + ".lnk.*.*." + relation + "." + label + ".>"
}

// CoreKVRelationNarrowedFilters returns the deduped, deterministically-ordered
// set of JetStream filter subjects for a lens whose referenced RELATIONS are
// known exhaustively as well as its labels: for each label, its vertex form,
// plus — for each relation — that (label, relation) pair's source and target
// link forms.
//
// This is CoreKVNarrowedFilters with the link forms' relation segment pinned
// instead of wildcarded. That segment is the one that decides whether a link can
// appear in the lens's traversals at all: the relation-blind forms select on
// "one endpoint has this type", which for a hub type like identity is close to
// the whole link keyspace, and the lens then pays a full re-execute for every
// link on that hub whatever the relation.
//
// An EMPTY relations slice is meaningful and returns the vertex forms ALONE: a
// query with no relationship pattern (MATCH (p:patient) … RETURN p.key) cannot
// be affected by any link, so it subscribes to none. A caller that does not know
// the relation set exhaustively must call CoreKVNarrowedFilters instead — never
// this one with an empty slice.
//
// The pairwise-non-subset property CoreKVNarrowedFilters' doc argues for holds
// here for the same reason, and is proved the same way (subjects_test.go,
// against a reimplementation of nats-server's own isSubsetMatch): any two forms
// differ at some token position neither side wildcards — `vtx` vs `lnk` at
// segment 3, the label at segment 4 or 6, the relation at segment 6, and the
// source form's trailing `>` sits where the target form carries a literal
// label, which subset matching refuses in both directions.
func CoreKVRelationNarrowedFilters(bucket string, labels, relations []string) []string {
	sortedLabels := dedupeSorted(labels)
	sortedRelations := dedupeSorted(relations)

	out := make([]string, 0, len(sortedLabels)*(1+2*len(sortedRelations)))
	for _, l := range sortedLabels {
		out = append(out, CoreKVVertexFilter(bucket, l))
		for _, r := range sortedRelations {
			out = append(out,
				CoreKVLinkSourceRelationFilter(bucket, l, r),
				CoreKVLinkTargetRelationFilter(bucket, l, r))
		}
	}
	return out
}

// dedupeSorted collapses duplicates and returns the remainder in a stable
// order, so a filter set derived from a map's iteration order is byte-identical
// on every derivation — which is what lets activation and Rebuild each
// recompute it independently and agree.
func dedupeSorted(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if _, dup := seen[s]; dup {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}
