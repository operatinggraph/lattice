package main

import (
	"context"
	"log/slog"
	"strings"
	"sync"

	"github.com/operatinggraph/lattice/internal/refractor/health"
	"github.com/operatinggraph/lattice/internal/refractor/lens"
	"github.com/operatinggraph/lattice/internal/refractor/pipeline"
	"github.com/operatinggraph/lattice/internal/refractor/projection"
)

// The activation-time half of the business plane's neighbour-retraction rule
// (secure-plain-lens-retraction-and-audit-design.md §4.4), plus the shared-target
// scoping the target diff needs before it can be one of the rule's answers.
//
// Both live here rather than inline in startPipeline because both are decided
// from the LENS REGISTRY, which is this process's alone: pkgmgr validates one
// package Definition at a time and cannot see a bucket another package shares,
// and a pipeline sees only its own target. Everything below is written over its
// inputs rather than over closure-local state, so a vector is testable without a
// live registry mutex — mirroring capReadShredTargets and grantTableShredRevokers
// above them.
//
// The shared-target RULE itself lives in internal/refractor/projection, because
// the corpus census in internal/refractor holds the installed corpus to it and a
// census restating a rule agrees with a broken one. What stays here is the part
// that needs the registry: which lenses are already live on this target.

// isPlainProjectionLens reports whether r projects through the PLAIN path — no
// envelope (actor-aggregate or operation-role-index) and no Personal fan-out.
//
// It is threadsKeyColumns' own question asked under the name this gate needs it
// by, rather than a second copy of the predicate: the set of lenses whose key
// columns are RETURN aliases is exactly the set whose rows the plain arm writes,
// presence-checks and retracts, and the two must never disagree about a lens.
func isPlainProjectionLens(r *lens.Rule) bool { return threadsKeyColumns(r) }

// refuseLens records an activation refusal the way a refused lens's reason has
// to reach an operator: the local log line, and the lens's own Health-KV entry.
//
// The entry is the record. A lens that never activates has no heartbeat status
// of its own — the heartbeat reads the registry, and a refused lens never
// reaches it — so without this the only account of why a read model stopped
// being written is a line in a process log nobody is tailing.
func refuseLens(ctx context.Context, logger *slog.Logger, reporter *health.Reporter, r *lens.Rule, guard, reason string) {
	msg := guard + " REFUSED activation: " + reason
	logger.Error(msg, "lensId", r.ID, "canonicalName", r.CanonicalName)
	if reporter == nil {
		return
	}
	if err := reporter.RecordError(ctx, msg); err != nil {
		logger.Error("could not record the activation refusal on the lens's health entry",
			"lensId", r.ID, "err", err)
	}
}

// siblingLensOf describes one registry entry the way the shared-target rule
// reads a sibling: by what its RUNNING pipeline does.
//
// Every field comes from the entry or the pipeline rather than from
// entry.rule.Into. The two can disagree — an INTO-only reload applies a new
// target without touching the compiled rule the entry carries, and a MATCH
// reload swaps that rule without applying the INTO fields it happens to arrive
// with — and the question here is what the sibling's next event will list and
// write, which only the running values answer.
func siblingLensOf(entry *pipelineEntry) projection.SiblingLens {
	return projection.SiblingLens{
		CanonicalName:        entry.canonicalName,
		DiffRetraction:       entry.pipeline.DiffRetraction(),
		DiffRetractionPrefix: entry.pipeline.DiffRetractionPrefix(),
		Output:               entry.output,
	}
}

// registeredSiblingsOnTarget returns every ALREADY-ACTIVATED lens writing the
// same target as r, excluding the lens itself.
//
// "Target" is the pair (target kind, bucket) rather than the bucket alone: a
// bucket name means nothing across adapter kinds, and only the NATS-KV arm has
// the whole-bucket listing this scoping exists for.
//
// entry.target and entry.bucket are read here and written by an INTO-only hot
// reload (reload.go), on CoreKVSource's single dispatch goroutine — the one that
// also drives every activation — so they need no lock of their own. The mutex is
// the registry map's, which the heartbeat's own provider closures also take. The
// pipeline's diff-retraction flag and prefix are bound before Run and never
// swapped, which is what lets them be read here at all: a reload that would
// change either is refused rather than applied (hotReloadRefusal).
func registeredSiblingsOnTarget(mu *sync.Mutex, registry map[string]*pipelineEntry, selfID, target, bucket string) []projection.SiblingLens {
	if target != natsKVTarget {
		return nil
	}
	mu.Lock()
	defer mu.Unlock()
	var out []projection.SiblingLens
	for id, entry := range registry {
		if id == selfID || entry.pipeline == nil {
			continue
		}
		if entry.target == target && entry.bucket == bucket {
			out = append(out, siblingLensOf(entry))
		}
	}
	return out
}

// admitRetractionTransport runs both activation-time retraction guards, in
// startPipeline's own order, and reports whether the lens may go on activating.
//
// It IS the gate: a caller that reaches a false here must not register the lens,
// and every refusal it returns has already been recorded on the lens's health
// entry by refuseLens. The two guards live behind one call rather than inline in
// startPipeline so the sequence a lens is actually admitted by is the sequence a
// test can drive — a gate whose only caller is a closure inside main() is a gate
// no test can hold to its own effects.
//
// The order is load-bearing. The shared-target check runs first because it is
// what installs the target-diff scoping, and the transport gate reads the
// scoped diff as the lens's T2 answer: asked the other way round, a lens whose
// only transport is a scoped diff would be refused for carrying none.
func admitRetractionTransport(
	ctx context.Context,
	logger *slog.Logger,
	reporter *health.Reporter,
	r *lens.Rule,
	p *pipeline.Pipeline,
	siblings []projection.SiblingLens,
) bool {
	// Shared-target scoping for the target diff
	// (secure-plain-lens-retraction-and-audit-design.md §3.3, §4.4). A NATS-KV
	// bucket can hold several lenses' rows, and the diff's listing enumerates
	// the WHOLE bucket — for a single-column key with no segment filter at all
	// — so on a shared bucket it reads every sibling's key as a row this lens no
	// longer produces and Deletes it.
	//
	// The question is decided HERE and nowhere else because here is the only
	// place that knows the answer: pkgmgr validates one package Definition at a
	// time and cannot see a bucket another package shares, and the pipeline sees
	// only its own target. The registry is that knowledge.
	prefix, refusal := projection.SharedTargetDiffRefusal(r, siblings)
	if refusal != "" {
		refuseLens(ctx, logger, reporter, r, "shared-target diff retraction", refusal)
		return false
	}
	if prefix != "" {
		if err := p.SetDiffRetractionPrefix(prefix); err != nil {
			refuseLens(ctx, logger, reporter, r, "shared-target diff retraction", err.Error())
			return false
		}
		logger.Info("diff retraction scoped to this lens's own key prefix",
			"lensId", r.ID, "bucket", r.Into.Bucket, "prefix", prefix)
	}

	// The partition-scoped target diff
	// (anchor-partitioned-plain-lens-retraction-design.md §3.3). A lens whose
	// rows PARTITION by their anchor — one key column identifying it, the others
	// bound to neighbours the walk reached — may seed on its anchor's events and
	// diff within that anchor's partition, instead of rescanning the corpus and
	// listing the whole target on every event.
	//
	// It is armed HERE, after the scoping above, because the NATS-KV partition
	// listing runs under the diff's own prefix: arming first would arm a listing
	// wider than the one it inherits. The plane comes from
	// projection.IsAuthPlane, the one canonical derivation, rather than off the
	// pipeline — installLensPlane records it several stages later, and a
	// conjunct that depends on whether an earlier stage ran reads as satisfied
	// for a lens it must refuse.
	//
	// A refusal is a partition-only BUSINESS lens whose target cannot scope a
	// listing to one anchor: it would seed per anchor with nothing able to scope
	// its diff. The disposition is the DiffRetraction guard's — the lens does
	// not activate — for the same reason: dark is the safe end of half-armed.
	// Every other lens is simply not armed, keeps today's whole diff, and
	// returns no error.
	//
	// The auth plane is excluded HERE as well as inside SetPartitionRetraction,
	// and the redundancy is §3.7's point rather than an oversight: the three
	// grant tables are held out by THREE independent exclusions, so no single
	// edit re-arms them. This is the third — the gate simply does not offer the
	// transport to that plane — beside the setter's own plane conjunct and
	// GrantWriterAdapter implementing no adapter.PartitionKeyLister. The plane
	// is still passed to the setter rather than relied on being unreachable, so
	// a future caller that skips this guard is refused by the setter too.
	if !projection.IsAuthPlane(r) {
		if err := p.SetPartitionRetraction(projection.IsAuthPlane(r)); err != nil {
			refuseLens(ctx, logger, reporter, r, "partition-scoped diff retraction", err.Error())
			return false
		}
	}
	if p.PartitionRetraction() {
		logger.Info("diff retraction scoped to the anchors an evaluation covers",
			"lensId", r.ID, "canonicalName", r.CanonicalName)
	}

	// The business plane's retraction-transport gate
	// (secure-plain-lens-retraction-and-audit-design.md §4.4). A plain lens whose
	// row EXISTENCE depends on a required neighbour can be orphaned by an event
	// that names no anchor — a neighbour vertex tombstoned, a link two hops out
	// removed, a neighbour's aspect flipping a WHERE — and the anchor-self
	// presence check structurally cannot reach that row. Such a lens must carry
	// a transport that can: the licensed neighbour-anchor derivation (T1) or a
	// target diff it owns (T2).
	//
	// Scoped to the BUSINESS plane, the same boundary the divergence audit draws
	// and for the same reason: an auth-plane verdict belongs to the plane that
	// has a code, a severity ladder and an escalation for it, and the derivation
	// licence refuses that plane outright, so T1 is not available there to
	// satisfy the rule with. The auth-plane members that carry no transport are
	// named debt, pinned by name in internal/refractor's retraction-transport
	// corpus census.
	//
	// The disposition is the DiffRetraction guard's: the lens does not activate.
	// Dark is the safe end — a lens that silently keeps orphaned rows on a
	// Protected table serves them under stale authorization anchors, and after an
	// erasure serves plaintext no re-projection reaches.
	if isPlainProjectionLens(r) && !projection.IsAuthPlane(r) {
		if refusal := retractionTransportRefusal(p, r); refusal != "" {
			refuseLens(ctx, logger, reporter, r, "neighbour-retraction transport", refusal)
			return false
		}
	}
	return true
}

// retractionTransportRefusal reports why a business-plane plain lens may not
// activate, "" when it may.
//
// The verdict comes from the pipeline's own derivation
// (pipeline.PlainRetractionTransport), which the heartbeat publishes as
// `retractionTransport` for the same lens — one derivation, so the gate and the
// field an operator reads can never describe different postures. The plane is
// passed to it from projection.IsAuthPlane, the one canonical derivation, rather
// than read back off the pipeline: installLensPlane records it several stages
// later, and a conjunct that depends on whether an earlier stage happened to run
// reads as satisfied for the lens it must refuse.
//
// A lens that does not depend on a neighbour needs no transport and passes with
// none. A lens the predicate declines to classify — one whose compiled rule is
// not a full-engine rule, which expresses matching without a pattern graph —
// is outside the rule the design states, and passes for that reason rather than
// by accident: it has no MATCH shape a neighbour dependency could be read off.
func retractionTransportRefusal(p *pipeline.Pipeline, r *lens.Rule) string {
	v := p.PlainRetractionTransport(projection.IsAuthPlane(r))
	if !v.Classified {
		return ""
	}
	if !v.Exhaustive {
		return "whether a neighbour event can drop its rows could not be derived from its query shape, and an underivable answer " +
			"read as \"no\" is how a lens with no retraction transport activates silently"
	}
	if !v.DependsOnNeighbour {
		return ""
	}
	if v.Transport != pipeline.RetractionTransportNone {
		return ""
	}
	return "its row existence depends on a neighbour (" + strings.Join(v.Reasons, "; ") + ") and it carries no retraction transport: " +
		v.Refusal + ". A neighbour event that drops one of its rows names no anchor, so the row is never retracted — " +
		"give the lens a shape the derivation licence admits, or declare target-diff retraction on a target it owns"
}
