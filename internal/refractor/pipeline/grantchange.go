package pipeline

import (
	"context"
	"log/slog"
	"time"

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
// A nil reporter (a directly-constructed pipeline, every harness) silently
// succeeds, the same posture every other reporter call on this type takes.
//
// The write's error is both logged AND returned. The log is for the operator;
// the return is for the drain worker, which tracks what has actually been
// REPORTED rather than what it attempted — a lost Health write must not let an
// overflow count be cleared into silence.
func (p *Pipeline) RecordGrantReprojectIssue(ctx context.Context, kind, detail string) error {
	if p.reporter == nil {
		return nil
	}
	if err := p.reporter.RecordGrantReprojectIssue(ctx, kind, detail); err != nil {
		slog.Warn("pipeline: grant change: could not record the reprojection issue on health",
			"ruleId", p.ruleID, "kind", kind, "detail", detail, "err", err)
		return err
	}
	return nil
}

// RecordUnsanctionedGrantKeyRefusal raises the lens's Health fault for a write
// this lens attempted into the D1 read-grant namespace without being an
// installed read-grant producer (adapter.ErrUnsanctionedReadGrantKey).
//
// It routes to the lens's own health entry for the same reason every other
// fault here does: the lens that is misdeclared is the one an operator has to
// find, and a process-level counter would have to be correlated back to it.
//
// Raised once per LENS, and the distinction is load-bearing: the offending
// cypher renders the same key on every evaluation of every actor, so a fault
// per write would bury the entry it exists to raise — and the dedup lives here
// rather than on the adapter because an adapter is rebuilt on every INTO-only
// hot reload, so a once over there would re-arm on a package reinstall, which
// is precisely when an operator is reading the entry.
func (p *Pipeline) RecordUnsanctionedGrantKeyRefusal(ctx context.Context, key string) {
	p.unsanctionedGrantKeyOnce.Do(func() {
		slog.Error("pipeline: REFUSED a write into the reserved D1 read-grant namespace — this lens is not an installed read-grant producer, so no cap-read key it renders may land",
			"ruleId", p.ruleID, "key", key)
		if p.reporter == nil {
			return
		}
		if err := p.reporter.RecordGrantReprojectIssue(ctx, "unsanctioned-grant-key",
			"refused a write to "+key+": this lens is not an installed read-grant producer, so no key it renders in the reserved D1 namespace may land"); err != nil {
			slog.Warn("pipeline: could not record the unsanctioned read-grant key refusal on health",
				"ruleId", p.ruleID, "key", key, "err", err)
		}
	})
}

// SetPersonalSweepProgress records the personal convergence sweep's
// round-robin cursor, its last completed cycle, and the grant-change drain's
// queue depth on this lens's own health entry
// (personal-lens-grant-change-trigger-design.md §4.3).
//
// The sweep is one process-level walk shared by every personal lens, but the
// fact it publishes is per-lens: whether THIS lens's rows have a standing
// healer behind them, and how far behind the fast path is running. Routing it
// to the lens's own entry is what lets an operator answer that from the lens
// they are already looking at.
//
// A nil reporter (a directly-constructed pipeline, every harness) silently
// succeeds, the same posture every other reporter call on this type takes. The
// write's error is logged AND returned, so the sweeper can say which lens's
// observability it lost rather than dropping the failure.
func (p *Pipeline) SetPersonalSweepProgress(ctx context.Context, cursor string, cycleCompletedAt time.Time, queueDepth uint64) error {
	if p.reporter == nil {
		return nil
	}
	if err := p.reporter.SetPersonalSweepProgress(ctx, cursor, cycleCompletedAt, queueDepth); err != nil {
		slog.Warn("pipeline: grant change: could not record the personal sweep's progress on health",
			"ruleId", p.ruleID, "cursor", cursor, "err", err)
		return err
	}
	return nil
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
	p.notifyGrantChangeSignalled(targetKey, transition)
}

// notifyGrantChangeSignalled is notifyGrantChange plus whether a signal was
// actually emitted. The retraction paths read it: a per-key announcement that
// emitted nothing — an unclassified liveness, or a key the lens's own inverse
// does not claim — leaves the caller holding a revocation nobody heard, and the
// caller often still holds the actor by name.
func (p *Pipeline) notifyGrantChangeSignalled(targetKey string, transition adapter.GrantTransition) bool {
	if p.grantSink == nil || p.grantAnchorFromKey == nil || targetKey == "" {
		return false
	}
	switch transition {
	case adapter.TransitionGranted, adapter.TransitionRevoked:
	case adapter.TransitionUnknown:
		// "We did not look" is not "we looked and nothing changed", and the two
		// must not collapse into the same silent no-op just because neither
		// signals. A sequence-less guarded write returns before reading any
		// stored body, so this key's liveness is genuinely unclassified and the
		// standing healer — not this edge — is what covers it. Saying so is the
		// only way the distinction the type draws is observable at all.
		slog.Info("pipeline: grant change: write carried no ordering token, so its liveness is unclassified — no signal emitted; the convergence sweep covers this key",
			"ruleId", p.ruleID, "key", targetKey)
		return false
	default:
		return false
	}
	actorKey, ok := p.grantAnchorFromKey(targetKey)
	if !ok {
		// projection.IsReadGrantProducer probes this inverse against a
		// synthetic key of the lens's own pattern before wiring the sink, so a
		// well-formed key this lens wrote always inverts. Reaching here means
		// the key does not belong to this lens's pattern at all — the
		// fail-closed case the inversion exists for — which is worth saying out
		// loud rather than dropping in silence.
		slog.Warn("pipeline: grant change: written key does not invert to an anchor — no signal emitted",
			"ruleId", p.ruleID, "key", targetKey)
		return false
	}
	p.grantSink.GrantChanged(actorKey)
	return true
}

// notifyActorGrantChange announces a retraction for one actor whose keys this
// pipeline just removed, naming the actor directly instead of recovering it
// from a written key.
//
// It is the coarser sibling of notifyGrantChange, for the one caller that holds
// the actor already and cannot get a per-key liveness answer: an out-of-band
// shred against a target whose adapter does not derive GrantTransition
// (adapter.GrantTransitionDeriver). Such an adapter reports TransitionNone for
// every key it retracts, so routing the shred through notifyGrantChange would
// emit nothing at all and leave the consumer honouring grants the shred
// destroyed.
//
// Announcing per actor rather than per key is strictly safe and no coarser than
// the consumer needs: the sink's own entry point takes an actor
// (GrantChangeSink.GrantChanged), notifyGrantChange's inversion exists only to
// RECOVER one from a key, and the reprojection it triggers is per actor. The
// cost of the coarser signal is at most one extra reprojection of an actor
// whose keys were already gone.
//
// actorKey is the full Contract #1 vertex key, the same shape the inversion
// yields. The grantSink nil-guard is the same one notifyGrantChange keeps: only
// a read-grant producer carries a sink, and a lens with none has nobody to tell.
func (p *Pipeline) notifyActorGrantChange(actorKey string) {
	if p.grantSink == nil || actorKey == "" {
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
