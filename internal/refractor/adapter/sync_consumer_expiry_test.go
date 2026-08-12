package adapter_test

import (
	"context"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/adapter"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// TestEnsureSyncStream_NewDurableInheritsConsumerInactiveThreshold proves the
// SYNC stream's ConsumerLimits.InactiveThreshold (Inc 1,
// edge-sync-orphan-expiry-design.md §5) is stamped into a durable that
// declares no threshold of its own — nats-server 2.14 server/consumer.go:
// 662-666's inheritance rule, exercised against the real embedded server
// rather than assumed.
func TestEnsureSyncStream_NewDurableInheritsConsumerInactiveThreshold(t *testing.T) {
	conn, js := startSyncServer(t)
	_, err := adapter.NewNatsSubjectAdapter(context.Background(), conn, "rule-1", "lattice.sync.user", "SYNC", []string{adapter.PersonalActorKeyField})
	require.NoError(t, err)

	cons, err := js.CreateOrUpdateConsumer(context.Background(), "SYNC", jetstream.ConsumerConfig{
		Durable:       "edge-sync-identityA-deviceA",
		FilterSubject: "lattice.sync.user.identityA",
		AckPolicy:     jetstream.AckExplicitPolicy,
		// InactiveThreshold deliberately unset: the case under test.
	})
	require.NoError(t, err)

	info, err := cons.Info(context.Background())
	require.NoError(t, err)
	assert.Equal(t, adapter.SyncConsumerInactiveThreshold, info.Config.InactiveThreshold,
		"a durable declaring no threshold must inherit the stream's ConsumerLimits.InactiveThreshold")
}

// TestEnsureSyncStream_ExcessThresholdRefused proves the same stream limit is
// a ceiling: nats-server 2.14 server/consumer.go:843-844 refuses a durable
// that asks for a longer InactiveThreshold than the stream allows.
func TestEnsureSyncStream_ExcessThresholdRefused(t *testing.T) {
	conn, js := startSyncServer(t)
	_, err := adapter.NewNatsSubjectAdapter(context.Background(), conn, "rule-1", "lattice.sync.user", "SYNC", []string{adapter.PersonalActorKeyField})
	require.NoError(t, err)

	_, err = js.CreateOrUpdateConsumer(context.Background(), "SYNC", jetstream.ConsumerConfig{
		Durable:           "edge-sync-identityA-deviceB",
		FilterSubject:     "lattice.sync.user.identityA",
		AckPolicy:         jetstream.AckExplicitPolicy,
		InactiveThreshold: 48 * time.Hour,
	})
	require.Error(t, err, "a durable asking for longer than the stream ceiling must be refused")

	var apiErr *jetstream.APIError
	require.ErrorAs(t, err, &apiErr, "the refusal must be a JetStream API error, not a transport failure")
	// nats-server v2.14.0 server/jetstream_errors_generated.go:189 —
	// JSConsumerInactiveThresholdExcess = 10153.
	assert.EqualValues(t, 10153, apiErr.ErrorCode, "must be refused specifically for the excess threshold, not some other cause")
}

// TestSyncConsumerInactiveThreshold_OutlivesTheStreamsOwnRetention pins the
// RELATION the value exists to hold, not the arithmetic that produces it.
// Every other assertion in this file compares the observed threshold against
// the constant, so the constant can be mutated to anything — including a
// NEGATIVE margin, yielding a threshold SHORTER than MaxAge — and stay green.
//
// A threshold below the stream's retention horizon reaps durables whose ack
// floor could still have delivered something a fresh consumer would not,
// which is exactly the property edge-sync-orphan-expiry-design.md §7 argues
// makes this reaping free. MaxAge is read back from the server rather than
// from the sibling constant, so the relation is checked against what the
// stream actually enforces.
func TestSyncConsumerInactiveThreshold_OutlivesTheStreamsOwnRetention(t *testing.T) {
	conn, js := startSyncServer(t)
	ctx := context.Background()

	_, err := adapter.NewNatsSubjectAdapter(ctx, conn, "rule-1", "lattice.sync.user", "SYNC", []string{adapter.PersonalActorKeyField})
	require.NoError(t, err)

	s, err := js.Stream(ctx, "SYNC")
	require.NoError(t, err)
	cfg := s.CachedInfo().Config

	require.Positive(t, cfg.MaxAge, "precondition: the SYNC stream must bound retention by age")
	assert.Greater(t, cfg.ConsumerLimits.InactiveThreshold, cfg.MaxAge,
		"a durable must outlive the stream's own retention horizon, or the reap destroys an ack floor that could still have delivered something")

	assert.EqualValues(t, adapter.SyncConsumerMaxAckPending, cfg.ConsumerLimits.MaxAckPending,
		"the MaxAckPending ceiling must reach the stream; without it the InactiveThreshold adoption is refused on any populated stream")
	assert.EqualValues(t, 1000, cfg.ConsumerLimits.MaxAckPending,
		"the ceiling must stay at the server's own JsDefaultMaxAckPending, or existing consumers start exceeding it")
}

// TestEnsureSyncStream_BrowserShellThresholdAcceptedUnchanged proves the
// browser shell's own 30-minute InactiveThreshold (internal/edge/browser/
// shell/shell.mjs:49) sits comfortably under the ceiling and is accepted
// verbatim, not silently widened or narrowed.
func TestEnsureSyncStream_BrowserShellThresholdAcceptedUnchanged(t *testing.T) {
	conn, js := startSyncServer(t)
	_, err := adapter.NewNatsSubjectAdapter(context.Background(), conn, "rule-1", "lattice.sync.user", "SYNC", []string{adapter.PersonalActorKeyField})
	require.NoError(t, err)

	const browserShellThreshold = 30 * time.Minute
	cons, err := js.CreateOrUpdateConsumer(context.Background(), "SYNC", jetstream.ConsumerConfig{
		Durable:           "edge-sync-identityA-deviceC",
		FilterSubject:     "lattice.sync.user.identityA",
		AckPolicy:         jetstream.AckExplicitPolicy,
		InactiveThreshold: browserShellThreshold,
	})
	require.NoError(t, err, "the browser shell's 30-minute threshold must stay under the ceiling")

	info, err := cons.Info(context.Background())
	require.NoError(t, err)
	assert.Equal(t, browserShellThreshold, info.Config.InactiveThreshold,
		"an explicit threshold under the ceiling must be accepted unchanged, not overwritten by the stream default")
}

// TestEnsureSyncStream_AppendBranchSetsConsumerInactiveThreshold drives
// ensureSyncStream's subject-APPEND branch — a stream that does not yet
// exist, so subjectPrefix's wildcard is appended to a nil existing-subjects
// list — and asserts that branch alone sets ConsumerLimits.InactiveThreshold.
// Both branches must set the limit independently: missing either would leave
// the policy off on whichever restart path takes it.
func TestEnsureSyncStream_AppendBranchSetsConsumerInactiveThreshold(t *testing.T) {
	conn, js := startSyncServer(t)

	_, err := adapter.NewNatsSubjectAdapter(context.Background(), conn, "rule-1", "lattice.sync.user", "SYNC-APPEND", []string{adapter.PersonalActorKeyField})
	require.NoError(t, err)

	s, err := js.Stream(context.Background(), "SYNC-APPEND")
	require.NoError(t, err)
	assert.Equal(t, adapter.SyncConsumerInactiveThreshold, s.CachedInfo().Config.ConsumerLimits.InactiveThreshold,
		"the subject-append branch (brand-new stream) must set ConsumerLimits.InactiveThreshold")
}

// TestEnsureSyncStream_SubjectAlreadyPresentBranchSetsConsumerInactiveThreshold
// drives ensureSyncStream's OTHER branch: a stream that already carries
// subjectPrefix's wildcard subject. The stream is pre-created directly
// (bypassing NewNatsSubjectAdapter) with NO ConsumerInactiveThreshold, so the
// only thing that can have set it afterward is this branch's own
// EnsureStream call — isolating it from the append branch tested above.
func TestEnsureSyncStream_SubjectAlreadyPresentBranchSetsConsumerInactiveThreshold(t *testing.T) {
	conn, js := startSyncServer(t)

	require.NoError(t, conn.EnsureStream(context.Background(), substrate.StreamSpec{
		Name:              "SYNC-PRESENT",
		Subjects:          []string{"lattice.sync.user.>"},
		MaxAge:            24 * time.Hour,
		MaxMsgsPerSubject: 10_000,
		// ConsumerInactiveThreshold deliberately zero: simulates a stream
		// that predates Inc 1.
	}))
	pre, err := js.Stream(context.Background(), "SYNC-PRESENT")
	require.NoError(t, err)
	require.Zero(t, pre.CachedInfo().Config.ConsumerLimits.InactiveThreshold, "precondition: the stream must start with no policy")

	_, err = adapter.NewNatsSubjectAdapter(context.Background(), conn, "rule-1", "lattice.sync.user", "SYNC-PRESENT", []string{adapter.PersonalActorKeyField})
	require.NoError(t, err)

	s, err := js.Stream(context.Background(), "SYNC-PRESENT")
	require.NoError(t, err)
	assert.Equal(t, adapter.SyncConsumerInactiveThreshold, s.CachedInfo().Config.ConsumerLimits.InactiveThreshold,
		"the subject-already-present branch must itself set ConsumerLimits.InactiveThreshold")
}

// TestEnsureSyncStream_AdoptsThePolicyOnAStreamThatAlreadyHasConsumers is the
// live-upgrade case, and the one every other test in this file structurally
// cannot reach: a SYNC stream provisioned before the policy existed, carrying
// the durables of devices that have already attached.
//
// Adopting a consumer limit makes nats-server re-validate every existing
// consumer against BOTH limits, and it compares a zero MaxAckPending limit as
// an allowance of zero (2.14 server/stream.go:2433-2434) instead of guarding
// it with `> 0` the way the create path does (:842). Every explicit-ack
// consumer carries JsDefaultMaxAckPending=1000, so an InactiveThreshold-only
// adoption is refused with "change to limits violates consumers" — the stream
// keeps no policy at all, ensureSyncStream returns an error, and the Personal
// Lens never activates. Nothing about this is visible on a fresh stream.
func TestEnsureSyncStream_AdoptsThePolicyOnAStreamThatAlreadyHasConsumers(t *testing.T) {
	conn, js := startSyncServer(t)
	ctx := context.Background()

	require.NoError(t, conn.EnsureStream(ctx, substrate.StreamSpec{
		Name:              "SYNC-POPULATED",
		Subjects:          []string{"lattice.sync.user.>"},
		MaxAge:            24 * time.Hour,
		MaxMsgsPerSubject: 10_000,
		// No consumer limits: the pre-Inc-1 stream.
	}))
	_, err := js.CreateOrUpdateConsumer(ctx, "SYNC-POPULATED", jetstream.ConsumerConfig{
		Durable:       "edge-sync-identityA-deviceA",
		FilterSubject: "lattice.sync.user.identityA",
		AckPolicy:     jetstream.AckExplicitPolicy,
	})
	require.NoError(t, err, "precondition: a device attached before the policy existed")

	_, err = adapter.NewNatsSubjectAdapter(ctx, conn, "rule-1", "lattice.sync.user", "SYNC-POPULATED", []string{adapter.PersonalActorKeyField})
	require.NoError(t, err, "the adapter must come up against a stream that already carries a device's durable")

	s, err := js.Stream(ctx, "SYNC-POPULATED")
	require.NoError(t, err)
	assert.Equal(t, adapter.SyncConsumerInactiveThreshold, s.CachedInfo().Config.ConsumerLimits.InactiveThreshold,
		"the policy must actually land on a populated stream, not merely fail to error")

	// The pre-existing consumer keeps its zero threshold — the limit does not
	// retro-apply (server/stream.go:2433-2441 only rejects consumers that
	// EXCEED it), which is the entire reason substrate's backfill exists.
	cons, err := js.Consumer(ctx, "SYNC-POPULATED", "edge-sync-identityA-deviceA")
	require.NoError(t, err)
	info, err := cons.Info(ctx)
	require.NoError(t, err)
	assert.Zero(t, info.Config.InactiveThreshold,
		"a consumer predating the policy must NOT inherit it — Inc 2's backfill is what reaches these")
}
