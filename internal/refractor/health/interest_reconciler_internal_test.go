// InterestReconciler — internal test package so tests can call sweep directly
// rather than waiting out the production cadence (90s grace + 30min tick),
// and reach the grace/two-strike state the birth-race cases turn on.
package health

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/natsfixture"
	"github.com/operatinggraph/lattice/internal/refractor/personalinterest"
	"github.com/operatinggraph/lattice/internal/refractor/subjects"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/substrate/keys"
)

const reconcilerSyncStream = "SYNC"
const reconcilerInterestBucket = "personal-lens-interest"

// Device ids are valid 20-character NanoIDs over internal/substrate/keys'
// alphabet — no 0, I, l or O — because that is what the platform mints and a
// fixture that could never occur proves less than one that could.
const (
	reconcilerIdentity      = "AbCdEfGhJkMnPqRsTuVw"
	reconcilerLiveDevice    = "LiveDevice1234567xyz"
	reconcilerGoneDevice    = "GoneDevice1234567xyz"
	reconcilerBornDevice    = "BornDevice1234567xyz"
	reconcilerBadDocDevice  = "BadDocDevice12345xyz"
	reconcilerBadTimeDevice = "BadTimeDevice1234xyz"
)

// TestReconcilerFixtureIdsAreRealNanoIDs makes the fixture earn its shape
// rather than asserting it in a comment: an identity or device id the
// platform could never mint is a fixture that proves less than one it could.
func TestReconcilerFixtureIdsAreRealNanoIDs(t *testing.T) {
	for _, id := range []string{
		reconcilerIdentity, reconcilerLiveDevice, reconcilerGoneDevice,
		reconcilerBornDevice, reconcilerBadDocDevice, reconcilerBadTimeDevice,
	} {
		require.True(t, keys.IsValidNanoID(id), "fixture id %q is not a valid 20-char NanoID", id)
	}
}

// newInterestReconcilerFixture provisions the two artifacts the reconciler
// spans: the personal-lens-interest bucket it enumerates, and the SYNC stream
// it probes durables on.
func newInterestReconcilerFixture(t *testing.T) (*substrate.Conn, *substrate.KV, context.Context) {
	t.Helper()
	s := natsfixture.StartServer(t)
	nc := natsfixture.Connect(t, s.ClientURL())
	t.Cleanup(nc.Close)
	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	t.Cleanup(conn.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)

	_, err = conn.JetStream().CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: reconcilerInterestBucket})
	require.NoError(t, err)
	kv, err := conn.OpenKV(ctx, reconcilerInterestBucket)
	require.NoError(t, err)

	require.NoError(t, conn.EnsureStream(ctx, substrate.StreamSpec{
		Name:     reconcilerSyncStream,
		Subjects: []string{"lattice.sync.user.>"},
	}))
	return conn, kv, ctx
}

// newTestReconciler builds a reconciler whose witness has seen exactly the
// stream it probes — the unambiguous single-stream fleet.
func newTestReconciler(conn *substrate.Conn, kv *substrate.KV, stream string, logger *slog.Logger) *InterestReconciler {
	w := NewSyncStreamWitness()
	w.Observe(stream)
	return NewInterestReconciler(conn, kv, stream, w, logger)
}

// makeSyncDurable creates a device's SYNC durable the way internal/edge/sync
// does — through the shared subjects constructor, never a hand-spelled name.
func makeSyncDurable(ctx context.Context, t *testing.T, conn *substrate.Conn, identityID, deviceID string) {
	t.Helper()
	_, err := conn.JetStream().CreateOrUpdateConsumer(ctx, reconcilerSyncStream, jetstream.ConsumerConfig{
		Durable:       subjects.EdgeSyncDurable(identityID, deviceID),
		FilterSubject: "lattice.sync.user." + identityID,
		AckPolicy:     jetstream.AckExplicitPolicy,
	})
	require.NoError(t, err)
}

func registrationExists(ctx context.Context, t *testing.T, kv *substrate.KV, key string) bool {
	t.Helper()
	_, err := kv.Get(ctx, key)
	return err == nil
}

// TestInterestReconciler_RemovesOnlyRegistrationsWhoseDurableIsGone is the
// load-bearing table: exactly one shape — durable absent across TWO sweeps,
// registration older than the birth-race grace — may remove a row, and every
// other shape on the same bucket must survive the same passes.
func TestInterestReconciler_RemovesOnlyRegistrationsWhoseDurableIsGone(t *testing.T) {
	conn, kv, ctx := newInterestReconcilerFixture(t)

	old := time.Now().UTC().Add(-24 * time.Hour).Format(time.RFC3339)
	fresh := time.Now().UTC().Format(time.RFC3339)

	// Durable present: attached, or merely idle between attaches.
	require.NoError(t, personalinterest.Register(ctx, kv, reconcilerIdentity, reconcilerLiveDevice, nil, nil, old))
	makeSyncDurable(ctx, t, conn, reconcilerIdentity, reconcilerLiveDevice)

	// Durable absent, registration past the grace: the one removable shape.
	require.NoError(t, personalinterest.Register(ctx, kv, reconcilerIdentity, reconcilerGoneDevice, nil, nil, old))

	// Durable absent, registration inside the grace: mid-attach, since the
	// Manager registers before it attaches and the attach deletes the durable
	// before recreating it.
	require.NoError(t, personalinterest.Register(ctx, kv, reconcilerIdentity, reconcilerBornDevice, nil, nil, fresh))

	// A document that does not parse: no answer, so no licence to delete.
	_, err := kv.Put(ctx, reconcilerIdentity+"."+reconcilerBadDocDevice, []byte("{not json"))
	require.NoError(t, err)

	// A parseable document whose registeredAt does not parse: same.
	_, err = kv.Put(ctx, reconcilerIdentity+"."+reconcilerBadTimeDevice, []byte(`{"registeredAt":"yesterday"}`))
	require.NoError(t, err)

	// A key that this package's Key constructor could never have written.
	_, err = kv.Put(ctx, "nodot", []byte(`{"registeredAt":"`+old+`"}`))
	require.NoError(t, err)

	reasons := &reasonRecorder{}
	r := newTestReconciler(conn, kv, reconcilerSyncStream, slog.New(reasons))

	// FIRST sweep: nothing may go. Every absence here is a first strike, and a
	// first strike is indistinguishable from a live device mid-attach.
	require.Empty(t, r.sweep(ctx),
		"no registration may be removed on a single observed absence — an attach deletes the durable before recreating it")
	require.True(t, registrationExists(ctx, t, kv, reconcilerIdentity+"."+reconcilerGoneDevice),
		"even the genuine orphan survives its first sweep")

	removed := r.sweep(ctx)
	require.ElementsMatch(t, []string{reconcilerIdentity + "." + reconcilerGoneDevice}, removed,
		"only a registration absent across two sweeps AND past the grace may be removed")

	require.True(t, registrationExists(ctx, t, kv, reconcilerIdentity+"."+reconcilerLiveDevice),
		"a device whose durable still exists must keep its registration")
	require.True(t, registrationExists(ctx, t, kv, reconcilerIdentity+"."+reconcilerBornDevice),
		"a device inside the birth-race grace must keep its registration")
	require.True(t, registrationExists(ctx, t, kv, reconcilerIdentity+"."+reconcilerBadDocDevice),
		"an unparseable document is not a verdict")
	require.True(t, registrationExists(ctx, t, kv, reconcilerIdentity+"."+reconcilerBadTimeDevice),
		"an unparseable registeredAt is not a verdict")
	require.True(t, registrationExists(ctx, t, kv, "nodot"),
		"a key this package did not write must be left alone entirely")
	require.False(t, registrationExists(ctx, t, kv, reconcilerIdentity+"."+reconcilerGoneDevice))

	// Surviving is not enough for the rows kept by a guard: the malformed key
	// would survive anyway (Deregister's own Key() check refuses the empty ids
	// a failed parse yields) and the live device would survive anyway (the
	// fall-through also keeps). Both would then be kept for the wrong reason,
	// having paid for probes and reads and been diagnosed as failures. Assert
	// the reasons, which is the only thing that tells the cases apart.
	msgs := reasons.messages()
	require.Contains(t, msgs, "interest reconciler: unparseable registration key, keeping")
	require.NotContains(t, msgs, "interest reconciler: deregister failed")
	require.NotContains(t, msgs, "interest reconciler: durable lookup failed, keeping registration",
		"a live device's durable reads back fine; a lookup-failure warning here means the present-durable arm stopped firing")
}

// TestInterestReconciler_LiveDeviceSurvivesTheAttachItselfDropsItsDurable is
// the case the first draft of this reconciler got wrong. Both hosts DELETE the
// durable before recreating it on every attach, so ErrConsumerNotFound is
// transiently true for a device that is perfectly alive — and registeredAt
// does not save it, because a device up for a day carries one long past any
// grace. Only the two-strike rule does.
func TestInterestReconciler_LiveDeviceSurvivesTheAttachItselfDropsItsDurable(t *testing.T) {
	conn, kv, ctx := newInterestReconcilerFixture(t)

	old := time.Now().UTC().Add(-24 * time.Hour).Format(time.RFC3339)
	require.NoError(t, personalinterest.Register(ctx, kv, reconcilerIdentity, reconcilerLiveDevice, nil, nil, old))
	makeSyncDurable(ctx, t, conn, reconcilerIdentity, reconcilerLiveDevice)

	r := newTestReconciler(conn, kv, reconcilerSyncStream, quietLogger())
	key := reconcilerIdentity + "." + reconcilerLiveDevice

	require.Empty(t, r.sweep(ctx), "sweep 1: durable present")

	// The device re-attaches: RunDurableConsumer's unconditional delete lands,
	// and a sweep catches the window before the create.
	require.NoError(t, conn.JetStream().DeleteConsumer(ctx, reconcilerSyncStream,
		subjects.EdgeSyncDurable(reconcilerIdentity, reconcilerLiveDevice)))
	require.Empty(t, r.sweep(ctx), "sweep 2: mid-attach absence is a first strike, never a verdict")
	require.True(t, registrationExists(ctx, t, kv, key))

	// The attach completes.
	makeSyncDurable(ctx, t, conn, reconcilerIdentity, reconcilerLiveDevice)
	require.Empty(t, r.sweep(ctx), "sweep 3: durable back")
	require.True(t, registrationExists(ctx, t, kv, key))

	// And the strike must have been CLEARED, not merely unused: a later
	// single absence still cannot delete.
	require.NoError(t, conn.JetStream().DeleteConsumer(ctx, reconcilerSyncStream,
		subjects.EdgeSyncDurable(reconcilerIdentity, reconcilerLiveDevice)))
	require.Empty(t, r.sweep(ctx),
		"sweep 4: a present observation must clear the prior strike, so this absence starts over")
	require.True(t, registrationExists(ctx, t, kv, key))
}

// TestInterestReconciler_ProbeErrorDoesNotCountAsAnAbsence: an error is not an
// observation. A sweep that could not reach the server must neither delete on
// the spot nor bank a strike a later sweep can spend.
func TestInterestReconciler_ProbeErrorDoesNotCountAsAnAbsence(t *testing.T) {
	conn, kv, ctx := newInterestReconcilerFixture(t)

	old := time.Now().UTC().Add(-24 * time.Hour).Format(time.RFC3339)
	require.NoError(t, personalinterest.Register(ctx, kv, reconcilerIdentity, reconcilerGoneDevice, nil, nil, old))
	key := reconcilerIdentity + "." + reconcilerGoneDevice

	// A real absence banks the first strike.
	good := newTestReconciler(conn, kv, reconcilerSyncStream, quietLogger())
	require.Empty(t, good.sweep(ctx))

	// Now the probe itself breaks: the stream cannot be read at all. That must
	// not spend the banked strike, and must not bank another.
	good.syncStream = "NO-SUCH-STREAM"
	require.Empty(t, good.sweep(ctx), "an unreadable probe must remove nothing")
	require.True(t, registrationExists(ctx, t, kv, key))

	good.syncStream = reconcilerSyncStream
	require.Empty(t, good.sweep(ctx),
		"the erroring sweep must have cleared the strike, so this absence is a first strike again")
	require.True(t, registrationExists(ctx, t, kv, key))

	require.ElementsMatch(t, []string{key}, good.sweep(ctx),
		"two consecutive real absences then remove it")
}

// TestInterestReconciler_AmbiguousStreamsStopEveryDeletion. The Interest Set
// bucket is one bucket whose keys carry no stream dimension, so a reconciler
// bound to stream A sees every stream-B device as durable-less. Loupe already
// refuses to render a fleet verdict on this input; a component that DELETES
// must refuse at least as hard.
func TestInterestReconciler_AmbiguousStreamsStopEveryDeletion(t *testing.T) {
	conn, kv, ctx := newInterestReconcilerFixture(t)

	old := time.Now().UTC().Add(-24 * time.Hour).Format(time.RFC3339)
	require.NoError(t, personalinterest.Register(ctx, kv, reconcilerIdentity, reconcilerGoneDevice, nil, nil, old))
	key := reconcilerIdentity + "." + reconcilerGoneDevice

	witness := NewSyncStreamWitness()
	witness.Observe(reconcilerSyncStream)
	reasons := &reasonRecorder{}
	r := NewInterestReconciler(conn, kv, reconcilerSyncStream, witness, slog.New(reasons))

	// Two sweeps would ordinarily be enough to remove this row.
	require.Empty(t, r.sweep(ctx))

	// A second Personal Lens activates against a different stream — a hot
	// reload moving Into.Stream does exactly this to a running reconciler.
	witness.Observe("SYNC-TENANT-B")

	require.Empty(t, r.sweep(ctx), "an ambiguous fleet must remove nothing")
	require.True(t, registrationExists(ctx, t, kv, key))
	require.True(t, strings.Contains(strings.Join(reasons.messages(), "\n"), "more than one stream"),
		"the refusal must be logged loudly, naming the streams")
	require.True(t, strings.Contains(strings.Join(reasons.messages(), "\n"), "SYNC-TENANT-B"))
}

// TestInterestReconciler_ReRegistrationUnderTheSweepIsNotDeleted: the read
// that justified the removal and the removal itself must refer to the same
// document. A device that re-registers in between is alive.
func TestInterestReconciler_ReRegistrationUnderTheSweepIsNotDeleted(t *testing.T) {
	conn, kv, ctx := newInterestReconcilerFixture(t)

	old := time.Now().UTC().Add(-24 * time.Hour).Format(time.RFC3339)
	require.NoError(t, personalinterest.Register(ctx, kv, reconcilerIdentity, reconcilerGoneDevice, nil, nil, old))
	key := reconcilerIdentity + "." + reconcilerGoneDevice

	r := newTestReconciler(conn, kv, reconcilerSyncStream, quietLogger())
	require.Empty(t, r.sweep(ctx), "first strike")

	// Stand in for the race by driving the conditional delete directly at a
	// revision the document has already moved past — which is precisely what
	// a Register landing between registrationIsStale and DeregisterRevision
	// produces.
	entry, err := kv.Get(ctx, key)
	require.NoError(t, err)
	stale := entry.Revision
	require.NoError(t, personalinterest.Register(ctx, kv, reconcilerIdentity, reconcilerGoneDevice, nil, nil,
		time.Now().UTC().Format(time.RFC3339)))

	err = personalinterest.DeregisterRevision(ctx, kv, reconcilerIdentity, reconcilerGoneDevice, stale)
	require.ErrorIs(t, err, substrate.ErrRevisionConflict,
		"a delete conditioned on a superseded revision must be refused, not silently applied")
	require.True(t, registrationExists(ctx, t, kv, key))

	// And end to end: the re-registration is now fresh, so the grace keeps it
	// anyway on the next sweep.
	require.Empty(t, r.sweep(ctx))
	require.True(t, registrationExists(ctx, t, kv, key))
}

// TestInterestReconciler_RowDeletedUnderTheSweepIsNotAnError: the clean
// sign-out path this same design wired up removes the registration itself,
// which can land between this sweep's listing and its read. That is the
// EXPECTED outcome of a working system, not a fault, so it must not be
// reported as a failed read.
func TestInterestReconciler_RowDeletedUnderTheSweepIsNotAnError(t *testing.T) {
	conn, kv, ctx := newInterestReconcilerFixture(t)

	old := time.Now().UTC().Add(-24 * time.Hour).Format(time.RFC3339)
	require.NoError(t, personalinterest.Register(ctx, kv, reconcilerIdentity, reconcilerGoneDevice, nil, nil, old))

	reasons := &reasonRecorder{}
	r := newTestReconciler(conn, kv, reconcilerSyncStream, slog.New(reasons))
	require.Empty(t, r.sweep(ctx), "first strike")

	// The sign-out purge wins the race.
	require.NoError(t, personalinterest.Deregister(ctx, kv, reconcilerIdentity, reconcilerGoneDevice))

	require.Empty(t, r.sweep(ctx), "a row that is already gone is nothing to remove")

	// The listing no longer carries the key at all, so drive the read the
	// sweep would have done: this is the branch, and reaching it through
	// sweep() alone is impossible because ListKeys and the per-key Get cannot
	// be made to disagree from outside.
	revision, stale := r.registrationIsStale(ctx, reconcilerIdentity+"."+reconcilerGoneDevice)
	require.False(t, stale, "a key that is already gone is not a stale registration to remove")
	require.Zero(t, revision)
	require.NotContains(t, reasons.messages(), "interest reconciler: registration read failed, keeping",
		"a key that vanished between the listing and the read is the clean sign-out path working, not a broken read")
}

// TestInterestReconciler_StaleCheckReturnsTheLiveRevision. The deletion is
// conditioned on a revision, and the whole value of that is that it is the
// revision of the document the staleness verdict was READ from — a stale or
// invented one would either refuse every deletion or, worse, be defeated by
// passing something else. Pin the value against the entry itself.
func TestInterestReconciler_StaleCheckReturnsTheLiveRevision(t *testing.T) {
	conn, kv, ctx := newInterestReconcilerFixture(t)

	old := time.Now().UTC().Add(-24 * time.Hour).Format(time.RFC3339)
	require.NoError(t, personalinterest.Register(ctx, kv, reconcilerIdentity, reconcilerGoneDevice, nil, nil, old))
	key := reconcilerIdentity + "." + reconcilerGoneDevice
	// Rewrite it so the revision is not 1 and a hardcoded value cannot pass.
	require.NoError(t, personalinterest.Register(ctx, kv, reconcilerIdentity, reconcilerGoneDevice, []string{"lease"}, nil, old))

	entry, err := kv.Get(ctx, key)
	require.NoError(t, err)
	require.Greater(t, entry.Revision, uint64(1))

	r := newTestReconciler(conn, kv, reconcilerSyncStream, quietLogger())
	revision, stale := r.registrationIsStale(ctx, key)
	require.True(t, stale, "a registration a day old is past the birth-race grace")
	require.Equal(t, entry.Revision, revision,
		"the deletion must be conditioned on the revision the staleness verdict was read from")

	// And that revision is the one that actually authorises the delete.
	require.NoError(t, personalinterest.DeregisterRevision(ctx, kv, reconcilerIdentity, reconcilerGoneDevice, revision))
	_, err = kv.Get(ctx, key)
	require.ErrorIs(t, err, substrate.ErrKeyNotFound)
}

// TestInterestReconciler_SeamAcrossKVAndJetStream drives the REAL intervening
// sequence rather than each layer in isolation: the registration is written
// the way hydrate() writes it (personal.register → personalinterest.Register),
// the durable is created the way sync's RunDurableConsumer creates it, and
// only then is the durable removed the way the server's InactiveThreshold
// removes it. Each side is trivially green on its own; what this asserts is
// that the name the reconciler derives from the KV key is the same name that
// was actually created on the stream.
func TestInterestReconciler_SeamAcrossKVAndJetStream(t *testing.T) {
	conn, kv, ctx := newInterestReconcilerFixture(t)

	const identity = "MQsmTTAgNkngkdEjQz9L"
	const device = "BHrdHRUWXPkLiukEvK9e"
	key := identity + "." + device
	registeredAt := time.Now().UTC().Add(-24 * time.Hour).Format(time.RFC3339)

	require.NoError(t, personalinterest.Register(ctx, kv, identity, device, []string{"lease"}, nil, registeredAt))
	makeSyncDurable(ctx, t, conn, identity, device)

	r := newTestReconciler(conn, kv, reconcilerSyncStream, quietLogger())

	require.Empty(t, r.sweep(ctx), "with the durable live the registration must survive")
	require.Empty(t, r.sweep(ctx), "and it must still survive a second pass")
	require.True(t, registrationExists(ctx, t, kv, key))

	// The durable expires (here: deleted outright, which is what the server's
	// deleteNotActive does at InactiveThreshold).
	require.NoError(t, conn.JetStream().DeleteConsumer(ctx, reconcilerSyncStream,
		subjects.EdgeSyncDurable(identity, device)))

	require.Empty(t, r.sweep(ctx), "one absence is not yet a verdict")
	require.ElementsMatch(t, []string{key}, r.sweep(ctx),
		"once the durable has stayed gone across two sweeps the registration follows it")
	require.False(t, registrationExists(ctx, t, kv, key))
}

// TestSyncStreamWitness_ObserveAndAmbiguity pins the guard cmd/refractor uses
// for both its once-per-stream boot work and its ambiguity refusal — logic
// that would otherwise live inline in main() with no test at all.
func TestSyncStreamWitness_ObserveAndAmbiguity(t *testing.T) {
	w := NewSyncStreamWitness()
	require.Nil(t, w.Ambiguous(), "an empty witness is not ambiguous")

	require.True(t, w.Observe("SYNC"), "the first sighting of a stream is the one that runs the boot work")
	require.False(t, w.Observe("SYNC"), "a repeat sighting — another personal-lens rule, or a hot reload — must not re-run it")
	require.Nil(t, w.Ambiguous(), "one stream is unambiguous however many rules target it")

	require.True(t, w.Observe("SYNC-B"))
	require.Equal(t, []string{"SYNC", "SYNC-B"}, w.Ambiguous(),
		"a second distinct stream makes every stream-less registration key unattributable")
	require.False(t, w.Observe("SYNC-B"))
	require.Equal(t, []string{"SYNC", "SYNC-B"}, w.Ambiguous(), "ambiguity never clears")
}

// reasonRecorder captures the reason each kept registration was kept. A sweep
// that keeps a row for the wrong reason is indistinguishable from one that
// keeps it for the right one by looking at the bucket alone.
type reasonRecorder struct {
	mu   sync.Mutex
	msgs []string
}

func (h *reasonRecorder) Enabled(context.Context, slog.Level) bool { return true }
func (h *reasonRecorder) WithAttrs([]slog.Attr) slog.Handler       { return h }
func (h *reasonRecorder) WithGroup(string) slog.Handler            { return h }
func (h *reasonRecorder) Handle(_ context.Context, rec slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.msgs = append(h.msgs, rec.Message)
	return nil
}
func (h *reasonRecorder) messages() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.msgs...)
}
