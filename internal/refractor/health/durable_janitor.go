package health

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/operatinggraph/lattice/internal/refractor/subjects"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// defaultDurableJanitorGraceWindow is how long DurableJanitor waits after
// process start before its first sweep — long enough that a normal boot's
// pipeline activation has registered its lenses, so the sweep does no work it
// would only have to decide against.
const defaultDurableJanitorGraceWindow = 90 * time.Second

// defaultDurableJanitorTickInterval is the recurring cadence after the boot
// sweep. Deliberately slower than RegistryProbe's tick: the leaks this closes
// accrue at process-lifetime scale (a boot that missed a tombstone, a removal
// whose delete call failed), so a sweep per half hour drains them long before
// they matter.
const defaultDurableJanitorTickInterval = 30 * time.Minute

// janitorVertexProbe peeks at a `vtx.meta.<id>` envelope's soft-delete
// marker. Only `isDeleted` is read: it is the one field whose presence and
// value can license a deletion, and anything else the envelope says is a
// reason to leave the durable alone, not a reason to remove it.
type janitorVertexProbe struct {
	IsDeleted bool `json:"isDeleted"`
}

// DurableJanitor deletes per-lens durable JetStream consumers on the Core KV
// stream whose lens no longer exists.
//
// A lens's durable is normally reaped by the tombstone path (cmd/refractor's
// remover, which stops the pipeline and deletes the durable). That path needs
// two things the platform cannot guarantee: a tombstone event that actually
// reaches a running CoreKVSource, and a delete call that succeeds. Neither
// holds in general — a lens tombstoned while the Refractor is down replays
// from a KV history that may no longer carry the create the removal callback
// is gated on (lens.CoreKVSource fires it only for a rule it has loaded), and
// a failed delete is logged and abandoned. Either way the durable outlives
// its lens, holds its ack floor forever, and accumulates pending messages
// that read as lens lag in Loupe — the observable symptom, three of them
// measured live on the dev stack at ~11k phantom pending each.
//
// Deleting a durable is destructive and unrecoverable in the way that
// matters: the ack floor goes with it. So the sweep never reasons from a SET
// of live lenses. A set is only as trustworthy as the enumeration that built
// it, and a KV listing that comes back short — for any reason, at any layer,
// with or without an error — turns every lens it omitted into an apparent
// orphan. Instead each candidate durable is judged on its own authoritative
// read of the one key that decides it:
//
//   - `vtx.meta.<id>` reads back NOT-FOUND, or reads back with
//     `isDeleted: true` → the lens is gone, delete the durable;
//   - anything else — a live envelope, an unparseable one, a transient read
//     error → keep.
//
// A candidate is only reached at all if subjects.ParseLensDurable recognizes
// the name, which admits only what this Refractor's own LensDurable
// constructor produces (`refractor-adjacency` and
// `refractor-lens-source-<instance>-<nonce>` share the prefix and are
// rejected, as is every other component's consumer), and if the id is absent
// from both the running registry and alwaysKeep — the latter carrying the one
// lens that runs with no Core KV declaration at all, the env-gated bootstrap
// lens, whose id is NanoID-shaped and would otherwise read as an orphan.
//
// Refractor is a sanctioned direct Core-KV reader (platform binary, P5's one
// exception alongside Loupe).
type DurableJanitor struct {
	conn       *substrate.Conn
	bucket     string
	registered func() []string
	alwaysKeep []string

	graceWindow  time.Duration
	tickInterval time.Duration
	logger       *slog.Logger
}

// NewDurableJanitor constructs a janitor. registered must return the lens ids
// of the currently-started pipelines — the same function RegistryProbe takes.
// alwaysKeep names lens ids that legitimately run without a Core KV
// declaration. logger may be nil.
func NewDurableJanitor(
	conn *substrate.Conn,
	bucket string,
	registered func() []string,
	alwaysKeep []string,
	logger *slog.Logger,
) *DurableJanitor {
	if logger == nil {
		logger = slog.Default()
	}
	return &DurableJanitor{
		conn:         conn,
		bucket:       bucket,
		registered:   registered,
		alwaysKeep:   append([]string(nil), alwaysKeep...),
		graceWindow:  defaultDurableJanitorGraceWindow,
		tickInterval: defaultDurableJanitorTickInterval,
		logger:       logger,
	}
}

// Run blocks until ctx is cancelled: waits out the boot grace window, sweeps
// once, then sweeps again on every tick thereafter.
func (j *DurableJanitor) Run(ctx context.Context) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(j.graceWindow):
	}
	j.sweep(ctx)

	t := time.NewTicker(j.tickInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			j.sweep(ctx)
		}
	}
}

// sweep runs one pass and returns the durables it deleted.
func (j *DurableJanitor) sweep(ctx context.Context) []string {
	keep := make(map[string]struct{}, len(j.alwaysKeep))
	for _, id := range j.alwaysKeep {
		keep[id] = struct{}{}
	}
	for _, id := range j.registered() {
		keep[id] = struct{}{}
	}

	deleted, err := j.conn.DeleteOrphanDurables(ctx, j.bucket, func(name string) bool {
		id, ok := subjects.ParseLensDurable(name)
		if !ok {
			return false
		}
		if _, kept := keep[id]; kept {
			return false
		}
		return j.lensIsGone(ctx, id)
	}, j.logger)
	if err != nil {
		j.logger.Warn("orphan-durable janitor: sweep failed", "err", err)
		return nil
	}
	if len(deleted) > 0 {
		j.logger.Info("orphan-durable janitor: deleted durables with no live lens",
			"count", len(deleted), "durables", deleted)
	}
	return deleted
}

// lensIsGone reports whether `vtx.meta.<id>` positively says the lens no
// longer exists. Every other answer — including every failure to get one —
// is false, because this is the predicate that licenses a deletion.
func (j *DurableJanitor) lensIsGone(ctx context.Context, id string) bool {
	entry, err := j.conn.KVGet(ctx, j.bucket, "vtx.meta."+id)
	if err != nil {
		if errors.Is(err, substrate.ErrKeyNotFound) {
			return true
		}
		j.logger.Warn("orphan-durable janitor: lens vertex read failed, keeping durable",
			"lensId", id, "err", err)
		return false
	}
	var vp janitorVertexProbe
	if err := json.Unmarshal(entry.Value, &vp); err != nil {
		j.logger.Warn("orphan-durable janitor: lens vertex unparseable, keeping durable",
			"lensId", id, "err", err)
		return false
	}
	return vp.IsDeleted
}
