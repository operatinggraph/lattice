package health

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/refractor/personalinterest"
	"github.com/operatinggraph/lattice/internal/refractor/subjects"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// defaultInterestReconcilerGraceWindow is how long after process start
// InterestReconciler waits before its first sweep. Same role as
// DurableJanitor's: a boot that has not finished activating its pipelines has
// nothing useful to say yet.
const defaultInterestReconcilerGraceWindow = 90 * time.Second

// defaultInterestReconcilerTickInterval is the recurring cadence after the
// boot sweep, and it is load-bearing rather than cosmetic: it is also the
// spacing between the two consecutive absences a deletion requires, so it
// must stay far longer than an attach's delete-then-create window (one RTT).
// Half an hour is six orders of magnitude of margin.
const defaultInterestReconcilerTickInterval = 30 * time.Minute

// interestRegistrationGrace is the minimum age a registration must have
// before it can be removed, measured from its registeredAt.
//
// It covers the ordering inside a single attach: the Manager registers
// BEFORE it attaches (hydrate on a cold start or gap, the warm-resume branch
// of ensureFresh otherwise), and the attach itself deletes the durable before
// recreating it. So a registration written seconds ago belongs to a device
// that is mid-attach, and its durable's absence says nothing.
//
// It is NOT what protects a live device across the wider window. Two things
// do, and both are needed because registeredAt only moves when a device
// actually attaches: the two-consecutive-absence rule below, and the durable
// itself once the attach completes. Tuning this constant is not a way to
// protect live devices, and lengthening it only delays reaping real orphans.
const interestRegistrationGrace = 1 * time.Hour

// InterestReconciler removes per-device Personal Lens Interest Set
// registrations whose device's SYNC durable is gone.
//
// A registration is normally removed by the clean sign-out path (cmd/facet's
// purge, which deletes the durable and deregisters on the same connection).
// That path needs a live NATS connection minted for the identity being
// purged, which it cannot get when the credential was revoked (the auth
// callout correctly refuses) and never gets at all when the host crashed. The
// row then outlives its device permanently: the purge destroyed the local
// mirror the device id lived in, so the next sign-in mints a fresh device id
// and nothing can ever name the old key again. It widens IsRelevant's push
// filter (a dead device with an empty filter admits everything), pads Loupe's
// fleet roster, and grows the bucket without bound.
//
// The verdict is per-registration and authoritative, never a set difference:
// each key is judged on its own read of the artifact that decides it, the
// device's durable consumer on the SYNC stream. All of the following must
// hold before a row is removed:
//
//   - the durable read back ErrConsumerNotFound in THIS sweep;
//   - and in the PREVIOUS one;
//   - and the registration parses, with a registeredAt older than
//     interestRegistrationGrace;
//   - and the process has seen only one SYNC stream;
//   - and the registration is still at the revision that was read.
//
// Anything else keeps. Every failure to get an answer resolves to keep,
// because this is the predicate that licenses a deletion.
//
// The two-absence rule is what makes the probe safe, and it is where this
// departs from the DurableJanitor it otherwise mirrors. That janitor reads a
// `vtx.meta.<id>` KV entry, which is never transiently absent. A SYNC durable
// IS: both hosts delete it before recreating it on every single attach
// (natstransport.RunDurableConsumer and the browser shell's attach do this
// because the server refuses to change a consumer's DeliverPolicy or
// OptStartSeq in place), so ErrConsumerNotFound is momentarily TRUE for a
// perfectly live device. One probe cannot distinguish that from an orphan; two
// probes a tick apart can, because the window is one round trip and the tick
// is half an hour.
//
// It also depends on the SYNC stream carrying
// ConsumerLimits.InactiveThreshold (adapter.SyncConsumerInactiveThreshold)
// and on every pre-policy consumer having been backfilled to it. Without
// that, "durable absent" means only "somebody deleted it" rather than "this
// device stopped existing", and the probe would be inferring liveness from an
// artifact with no lifetime of its own.
type InterestReconciler struct {
	conn       *substrate.Conn
	kv         *substrate.KV
	syncStream string
	witness    *SyncStreamWitness

	// absentLastSweep is the set of registration keys whose durable this
	// reconciler positively observed missing on its previous sweep — the
	// first of the two strikes a deletion needs.
	//
	// LIFETIME: per reconciler, in memory only. Created empty, so a fresh
	// process simply spends one extra tick before its first deletion.
	// REPLACED wholesale at the end of every sweep by that sweep's own
	// observations, which is what clears a key the moment a durable is seen
	// present again — and also what drops a key whose probe merely ERRORED,
	// since an error is not an observation of absence and must not be
	// allowed to stand in for one. A key deleted from the bucket falls out on
	// its own, never being listed again. Nothing here is persisted: it is
	// evidence about a window in time, and evidence that outlived its window
	// would be worse than none.
	absentLastSweep map[string]struct{}

	// interestChanged is the Interest Set's change edge, named with the
	// identity behind every registration this reconciler removes.
	//
	// A removal WIDENS what personalinterest.IsRelevant admits for that
	// identity — absence of any registration admits everything — and a personal
	// lens reads that answer live at evaluation time. Without the edge the
	// widening reaches the identity's rows only when the personal convergence
	// sweep next comes round, which on a large population is hours.
	//
	// LIFETIME: set once by the host at wiring time, before Run; never mutated,
	// never reset, process-lifetime. nil is today's behaviour.
	//
	// A bare func rather than a shared sink interface: control.Service takes the
	// same edge and internal/refractor/control imports this package, so a type
	// declared in either is unusable by the other. The host supplies one closure
	// to both.
	interestChanged func(identityID string)

	graceWindow  time.Duration
	tickInterval time.Duration
	registration time.Duration
	logger       *slog.Logger
}

// NewInterestReconciler constructs a reconciler over kv (the
// personal-lens-interest bucket) probing durables on syncStream — the same
// stream name the Personal Lens's own nats_subject adapter publishes to,
// never a literal. witness is the process-wide record of which SYNC streams
// have been seen; it must be the same instance every reconciler and the
// activation path share, or the ambiguity it exists to catch is invisible.
// logger may be nil.
func NewInterestReconciler(
	conn *substrate.Conn,
	kv *substrate.KV,
	syncStream string,
	witness *SyncStreamWitness,
	logger *slog.Logger,
) *InterestReconciler {
	if logger == nil {
		logger = slog.Default()
	}
	if witness == nil {
		witness = NewSyncStreamWitness()
		witness.Observe(syncStream)
	}
	r := &InterestReconciler{
		conn:            conn,
		kv:              kv,
		syncStream:      syncStream,
		witness:         witness,
		absentLastSweep: map[string]struct{}{},
		graceWindow:     defaultInterestReconcilerGraceWindow,
		tickInterval:    defaultInterestReconcilerTickInterval,
		registration:    interestRegistrationGrace,
		logger:          logger,
	}
	// Counted UNARMED from construction, not from the first SetInterestChangeSink
	// call. A reconciler nobody ever hands a sink to would otherwise never reach
	// the census at all — the one shape the census exists to catch — and a
	// personal lens's narrowing licence would keep resting on a fourth Interest
	// Set writer that announces nothing.
	noteInterestReconcilerSink(r, false)
	return r
}

// SetInterestChangeSink installs the Interest Set's change edge: fn is called
// with the identity id of every registration a sweep actually removed. nil
// leaves the sweep announcing nothing.
//
// Called after the conditional delete has landed, one identity at a time, on
// the sweep goroutine. fn must not block — the shipped implementation enqueues
// onto the grant-change reprojector's coalescing dirty set.
//
// Set at wiring time, before Run: this is not safe to flip concurrently with a
// sweep in flight, the same posture every other constructor-time field on this
// type takes.
//
// It also updates this process's census of unarmed reconcilers, which the
// personal derivation licence's conjunct 2 reads live — see
// InterestReconcilersWithoutSink for why a reconciler is counted from the moment
// it is constructed rather than from the moment it runs.
func (r *InterestReconciler) SetInterestChangeSink(fn func(identityID string)) {
	r.interestChanged = fn
	noteInterestReconcilerSink(r, fn != nil)
}

// InterestChangeSinkInstalled reports whether this reconciler's orphan reap
// announces on the Interest Set change edge.
//
// Its reap WIDENS what personalinterest.IsRelevant admits for the identity whose
// registration it removed (absence admits everything), and a personal lens reads
// that answer live — so it is the FOURTH writer of the Interest Set, beside the
// control plane's register, deregister and hydrate arms. A licence that asserted
// only the first three would be asserting three quarters of its own conjunct.
func (r *InterestReconciler) InterestChangeSinkInstalled() bool {
	return r.interestChanged != nil
}

// announceInterestChange routes one identity to the change edge, if one is wired.
func (r *InterestReconciler) announceInterestChange(identityID string) {
	if r.interestChanged == nil || identityID == "" {
		return
	}
	r.interestChanged(identityID)
}

// Run blocks until ctx is cancelled: waits out the boot grace window, sweeps
// once, then sweeps again on every tick thereafter.
func (r *InterestReconciler) Run(ctx context.Context) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(r.graceWindow):
	}
	r.sweep(ctx)

	t := time.NewTicker(r.tickInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.sweep(ctx)
		}
	}
}

// sweep runs one pass and returns the registration keys it deregistered,
// sorted.
func (r *InterestReconciler) sweep(ctx context.Context) []string {
	if streams := r.witness.Ambiguous(); streams != nil {
		// One bucket, no stream dimension in its keys, and this reconciler can
		// only vouch for one stream: every device on the others would read as
		// durable-less. Stop deleting entirely rather than delete the wrong
		// half of a fleet.
		r.logger.Error("interest reconciler: Personal Lenses target more than one stream "+
			"("+strings.Join(streams, ", ")+"); the Interest Set bucket carries no stream "+
			"dimension, so no registration can be attributed to a stream and none will be removed",
			"probing", r.syncStream, "streams", streams)
		return nil
	}

	keys, err := r.kv.ListKeys(ctx)
	if err != nil {
		// Leave absentLastSweep as it was: a listing that never happened is
		// not evidence that anything became present, and discarding the prior
		// strike would only cost an extra tick.
		r.logger.Warn("interest reconciler: list registrations failed", "bucket", r.kv.Bucket(), "err", err)
		return nil
	}
	sort.Strings(keys)

	absentNow := make(map[string]struct{})
	var removed []string
	for _, key := range keys {
		identityID, deviceID, ok := personalinterest.ParseKey(key)
		if !ok {
			r.logger.Warn("interest reconciler: unparseable registration key, keeping", "key", key)
			continue
		}
		if !r.durableAbsent(ctx, key, identityID, deviceID) {
			continue
		}
		absentNow[key] = struct{}{}
		if _, twice := r.absentLastSweep[key]; !twice {
			// First strike. Both hosts delete this durable before recreating
			// it on every attach, so a single absence is as consistent with a
			// live device mid-attach as with an orphan.
			continue
		}
		revision, ok := r.registrationIsStale(ctx, key)
		if !ok {
			continue
		}
		if err := personalinterest.DeregisterRevision(ctx, r.kv, identityID, deviceID, revision); err != nil {
			if errors.Is(err, substrate.ErrRevisionConflict) {
				// The device re-registered between the read and the delete —
				// it is alive and mid-attach, which is exactly what the
				// conditional delete is here to notice.
				r.logger.Info("interest reconciler: registration changed under the sweep, keeping", "key", key)
				continue
			}
			r.logger.Warn("interest reconciler: deregister failed", "key", key, "err", err)
			continue
		}
		removed = append(removed, key)
		// After the delete has landed, never before: the widening this
		// announces is the state the delete just created, and a reprojection
		// driven off the pre-delete registration would decide the identity's
		// rows against interest that no longer exists.
		r.announceInterestChange(identityID)
	}
	r.absentLastSweep = absentNow

	if len(removed) > 0 {
		r.logger.Info("interest reconciler: removed registrations whose SYNC durable has expired",
			"count", len(removed), "keys", removed)
	}
	return removed
}

// durableAbsent reports whether the device's SYNC durable positively read
// back as not-found. Only jetstream.ErrConsumerNotFound is an observation of
// absence; every other outcome — a present durable, a transient error, an
// unreadable stream — is false, because this is the first conjunct of the
// predicate that licenses a deletion.
func (r *InterestReconciler) durableAbsent(ctx context.Context, key, identityID, deviceID string) bool {
	durable := subjects.EdgeSyncDurable(identityID, deviceID)
	switch _, err := r.conn.JetStream().Consumer(ctx, r.syncStream, durable); {
	case err == nil:
		return false
	case !errors.Is(err, jetstream.ErrConsumerNotFound):
		r.logger.Warn("interest reconciler: durable lookup failed, keeping registration",
			"key", key, "durable", durable, "err", err)
		return false
	}
	return true
}

// registrationIsStale reads the registration and reports its revision when it
// is older than the birth-race grace. The revision is what the deletion is
// then conditioned on, so the read that justified the removal and the removal
// itself refer to the same document.
func (r *InterestReconciler) registrationIsStale(ctx context.Context, key string) (revision uint64, stale bool) {
	entry, err := r.kv.Get(ctx, key)
	if err != nil {
		if errors.Is(err, substrate.ErrKeyNotFound) {
			// The row went between the listing and now — most likely the
			// clean sign-out path this same design wired up. Nothing to
			// remove and nothing wrong.
			return 0, false
		}
		r.logger.Warn("interest reconciler: registration read failed, keeping", "key", key, "err", err)
		return 0, false
	}
	registeredAt, err := personalinterest.ParsedRegisteredAt(entry.Value)
	if err != nil {
		r.logger.Warn("interest reconciler: registration unreadable, keeping", "key", key, "err", err)
		return 0, false
	}
	if time.Since(registeredAt) <= r.registration {
		return 0, false
	}
	return entry.Revision, true
}
