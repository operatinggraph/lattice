package pipeline

import (
	"context"
	"log/slog"

	"github.com/operatinggraph/lattice/internal/refractor/adapter"
)

// GrantChangeSink receives the actor behind one D1 read-grant liveness
// transition this pipeline just wrote (personal-lens-grant-change-trigger-
// design.md §4.2).
//
// A Personal Lens decides every row against the read-grant projection
// (capabilityread.IsReadable), a projection produced by a DIFFERENT pipeline,
// read live, with no ordering between the two. So a personal row is a function
// of two inputs, and only one of them — the lens's own Core-KV subgraph — has
// ever driven a re-evaluation. This is the edge for the other one: the producer
// announces a grant landing or being withdrawn, and the consumer re-drives that
// one actor.
//
// actorKey is the full Contract #1 vertex key (vtx.identity.<id>), recovered
// from the written target key through the lens's own declared inverse
// (projection.OutputDescriptor.AnchorFromKey) — not the bare NanoID, and not
// the entry body's audit-only anchorType.
//
// Implementations must not block: this is called on the pipeline's consumer
// goroutine, inline with the write it describes.
type GrantChangeSink interface {
	GrantChanged(actorKey string)
}

// SetGrantChangeSink installs the read-grant change edge on this pipeline:
// sink receives every actor whose grant this lens's writes flip, and
// anchorFromKey is the lens's own target-key → anchor-vertex-key inversion
// (the same one the convergence sweep claims orphans with).
//
// Both must be non-nil or neither is installed — a sink with no inversion has
// nothing to name, and an inversion with no sink has nowhere to send it.
//
// Like SetGuarded and SetSweepPlan this must be called at construction time,
// before the pipeline starts writing; the fields are not safe to flip
// concurrently with writes. It is installed only for the lenses
// projection.InstallActorAggregate's classification admits — derived from the
// compiled plan and the output descriptor, never from a canonical-name list.
//
// A missing sink is fail-SLOW, never fail-open: the D1 gate itself is
// unchanged, so a pipeline with no sink costs latency (its consumers converge
// on the standing healer instead), never a grant honoured after revocation.
func (p *Pipeline) SetGrantChangeSink(sink GrantChangeSink, anchorFromKey func(targetKey string) (string, bool)) {
	if sink == nil || anchorFromKey == nil {
		return
	}
	p.grantSink = sink
	p.grantAnchorFromKey = anchorFromKey
}

// RecordGrantReprojectIssue raises the lens's Health fault for a grant-change
// reprojection that did not happen — a dropped signal or a failed re-evaluation.
// It forwards to the pipeline's own reporter, so the fault lands on the health
// entry of the lens that is actually degraded rather than on some process-level
// aggregate an operator would have to correlate back.
//
// A nil reporter (a directly-constructed pipeline, every harness) silently does
// nothing, the same posture every other reporter call on this type takes. The
// write failing is logged, not returned: the caller is a drain worker whose job
// is to keep draining, and it has nothing better to do with the error than what
// this does with it.
func (p *Pipeline) RecordGrantReprojectIssue(ctx context.Context, kind, detail string) {
	if p.reporter == nil {
		return
	}
	if err := p.reporter.RecordGrantReprojectIssue(ctx, kind, detail); err != nil {
		slog.Warn("pipeline: grant change: could not record the reprojection issue on health",
			"ruleId", p.ruleID, "kind", kind, "detail", detail, "err", err)
	}
}

// HasGrantChangeSink reports whether the read-grant change edge is wired on
// this pipeline. Its reader is the installer's own classification test: whether
// a lens announces grant changes is a security-plane posture decision, and the
// only way to observe it from outside is to ask.
func (p *Pipeline) HasGrantChangeSink() bool {
	return p.grantSink != nil && p.grantAnchorFromKey != nil
}

// notifyGrantChange routes one written target key to the grant-change sink,
// when the write actually changed the key's liveness.
//
// Only a transition signals. A guarded upsert rewrites an unchanged body on
// every evaluation to advance the watermark, and the auth plane's own
// convergence sweep re-verifies 25 actors a minute per producer — so signalling
// on writes rather than on transitions would drive up to ≈1,500 pointless
// cypher evaluations a minute across the personal plane, permanently, as an
// accidental coupling to another lens family's cadence.
//
// It is fail-closed on the inversion: AnchorFromKey reporting false means this
// lens does not own that key, so no signal is emitted and the standing healer
// covers it. That is the safe direction for a routing failure — a MISSING
// signal costs latency, whereas routing on the entry body's anchorType (which
// Contract #6 §6.14 makes audit-only) would deliver a retraction to the wrong
// lens, or to none, and read as a silent over-grant.
func (p *Pipeline) notifyGrantChange(targetKey string, transition adapter.GrantTransition) {
	if p.grantSink == nil || p.grantAnchorFromKey == nil || targetKey == "" {
		return
	}
	if transition != adapter.TransitionGranted && transition != adapter.TransitionRevoked {
		return
	}
	actorKey, ok := p.grantAnchorFromKey(targetKey)
	if !ok {
		// The install gate probes this inverse before wiring the sink
		// (projection.sweepEnrolment's KeyOwnershipRoundTrips), so reaching
		// here means a key this lens wrote does not invert through the pattern
		// it wrote it with — worth saying out loud rather than dropping.
		slog.Warn("pipeline: grant change: written key does not invert to an anchor — no signal emitted",
			"ruleId", p.ruleID, "key", targetKey)
		return
	}
	p.grantSink.GrantChanged(actorKey)
}

// truncateTarget clears the lens's rows and, on a lens carrying the
// grant-change edge, announces every grant the purge withdrew.
//
// Truncate is the one write path that never reaches the per-key guard: it lists
// its keys and Purges them directly, so no GrantTransition is produced for any
// row it clears. Left unhandled that would make the edge silent on the single
// operation that revokes the most at once — and it is not an exotic path. An
// operator's rebuild(truncate=true) takes it, and so does a MATCH hot-reload
// that NARROWS a producer's own cypher, which owes a truncating rebuild
// automatically (cmd/refractor/reload.go's matchShrank arm) and is precisely a
// revocation-shaped change.
//
// Keys purged before a mid-way failure are still announced: they are gone from
// the target whatever the error says, and a retraction nobody hears about is
// the over-grant direction.
func (p *Pipeline) truncateTarget(ctx context.Context, adpt adapter.Adapter) error {
	if ot, ok := adpt.(adapter.OutcomeTruncater); ok && p.grantSink != nil {
		keys, err := ot.TruncateWithKeys(ctx)
		for _, key := range keys {
			p.notifyGrantChange(key, adapter.TransitionRevoked)
		}
		return err
	}
	t, ok := adpt.(adapter.Truncater)
	if !ok {
		slog.Warn("pipeline: rebuild: truncate=true but adapter does not implement Truncater; skipping",
			"ruleId", p.ruleID)
		return nil
	}
	return t.Truncate(ctx)
}
