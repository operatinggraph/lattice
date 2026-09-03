package adapter_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/adapter"
)

// readSyncEnvelopes consumes up to want messages off an actor's SYNC subject
// and returns them decoded, in stream order.
func readSyncEnvelopes(t *testing.T, js jetstream.JetStream, stream, subject, durable string, want int) []map[string]any {
	t.Helper()
	cons, err := js.CreateOrUpdateConsumer(context.Background(), stream, jetstream.ConsumerConfig{
		Durable:       durable,
		FilterSubject: subject,
		DeliverPolicy: jetstream.DeliverAllPolicy,
		AckPolicy:     jetstream.AckExplicitPolicy,
	})
	require.NoError(t, err)
	batch, err := cons.Fetch(want, jetstream.FetchMaxWait(5*time.Second))
	require.NoError(t, err)
	var out []map[string]any
	for m := range batch.Messages() {
		var env map[string]any
		require.NoError(t, json.Unmarshal(m.Data(), &env))
		out = append(out, env)
		_ = m.Ack()
	}
	return out
}

// syncSubjectDepth reports how many messages the SYNC stream currently holds on
// one actor's subject — the observable that separates "published" from
// "queued behind an ack nobody has awaited yet".
func syncSubjectDepth(t *testing.T, js jetstream.JetStream, stream, subject string) uint64 {
	t.Helper()
	s, err := js.Stream(context.Background(), stream)
	require.NoError(t, err)
	info, err := s.Info(context.Background(), jetstream.WithSubjectFilter(subject))
	require.NoError(t, err)
	return info.State.Subjects[subject]
}

// TestNatsSubjectAdapter_PipelinedUpsertsLandInOrderAfterFlush pins the adapter
// half of the pipelined write path: upserts published under a context carrying
// a pipeline reach the stream in call order, and are readable once it is
// flushed.
// Ordering is the property the personal wire depends on — the Edge client
// applies envelopes in stream order — so it is asserted over enough rows that
// a reordering could not hide.
func TestNatsSubjectAdapter_PipelinedUpsertsLandInOrderAfterFlush(t *testing.T) {
	conn, js := startSyncServer(t)
	ctx := context.Background()
	a, err := adapter.NewNatsSubjectAdapter(ctx, conn, "rule-pipelined", "lattice.sync.user", "SYNC", []string{adapter.PersonalActorKeyField, "entityId"})
	require.NoError(t, err)

	const n = 25
	pipe := a.NewPublishPipeline()
	writeCtx := adapter.WithPublishPipeline(ctx, pipe)
	for i := 0; i < n; i++ {
		keys := map[string]any{adapter.PersonalActorKeyField: "identityPipelined", "entityId": fmt.Sprintf("row.%02d", i)}
		require.NoError(t, a.Upsert(writeCtx, keys, map[string]any{"i": float64(i)}, uint64(100+i)))
	}
	require.NoError(t, pipe.Flush(ctx))

	envs := readSyncEnvelopes(t, js, "SYNC", "lattice.sync.user.identityPipelined", "pipelined", n)
	require.Len(t, envs, n)
	for i, env := range envs {
		assert.Equal(t, "upsert", env["op"])
		assert.Equal(t, fmt.Sprintf("row.%02d", i), env["key"], "pipelined upserts must land in call order")
		assert.Equal(t, float64(100+i), env["revision"])
		assert.Equal(t, "rule-pipelined", env["lens"])
	}
}

// TestNatsSubjectAdapter_NoPipelineOnCtxPublishesSynchronously pins the
// unpipelined path: with no pipeline installed the publish awaits its own ack,
// so
// the message is on the stream the instant Upsert returns. That is the property
// the keyset frame relies on, published as it is outside the row loop's
// context.
func TestNatsSubjectAdapter_NoPipelineOnCtxPublishesSynchronously(t *testing.T) {
	conn, js := startSyncServer(t)
	ctx := context.Background()
	a, err := adapter.NewNatsSubjectAdapter(ctx, conn, "rule-sync", "lattice.sync.user", "SYNC", []string{adapter.PersonalActorKeyField, "entityId"})
	require.NoError(t, err)

	const subject = "lattice.sync.user.identitySync"
	require.Equal(t, uint64(0), syncSubjectDepth(t, js, "SYNC", subject))
	require.NoError(t, a.Upsert(ctx, map[string]any{adapter.PersonalActorKeyField: "identitySync", "entityId": "e1"}, map[string]any{"v": 1.0}, 7))
	assert.Equal(t, uint64(1), syncSubjectDepth(t, js, "SYNC", subject),
		"with no pipeline on the ctx the row must be stored by the time Upsert returns")
}

// TestNatsSubjectAdapter_PipelineCarriesEveryEnvelopeKind proves the pipeline
// is attached at the single publish seam, not per envelope kind: a delete, a
// keyset frame and a hydration marker published under a pipelined context all
// join it and all land, in order, once it is flushed. It is the reason a caller
// must scope the pipelined context to its write loop — nothing about the
// frame's own envelope keeps it out of a pipeline it is handed.
func TestNatsSubjectAdapter_PipelineCarriesEveryEnvelopeKind(t *testing.T) {
	conn, js := startSyncServer(t)
	ctx := context.Background()
	a, err := adapter.NewNatsSubjectAdapter(ctx, conn, "rule-kinds", "lattice.sync.user", "SYNC", []string{adapter.PersonalActorKeyField, "entityId"})
	require.NoError(t, err)

	pipe := a.NewPublishPipeline()
	writeCtx := adapter.WithPublishPipeline(ctx, pipe)
	require.NoError(t, a.Upsert(writeCtx, map[string]any{adapter.PersonalActorKeyField: "identityKinds", "entityId": "e1"}, map[string]any{"v": 1.0}, 1))
	require.NoError(t, a.Delete(writeCtx, map[string]any{adapter.PersonalActorKeyField: "identityKinds", "entityId": "e2"}, 2))
	require.NoError(t, a.PublishKeySet(writeCtx, "identityKinds", []map[string]any{
		{adapter.PersonalActorKeyField: "identityKinds", "entityId": "e1"},
	}, 3))
	require.NoError(t, a.PublishHydrationComplete(writeCtx, "identityKinds", 4))

	require.NoError(t, pipe.Flush(ctx))

	const subject = "lattice.sync.user.identityKinds"
	assert.Equal(t, uint64(4), syncSubjectDepth(t, js, "SYNC", subject),
		"every envelope kind goes through the one pipelined publish seam")
	envs := readSyncEnvelopes(t, js, "SYNC", subject, "kinds", 4)
	require.Len(t, envs, 4)
	assert.Equal(t, "upsert", envs[0]["op"])
	assert.Equal(t, "delete", envs[1]["op"])
	assert.Equal(t, "keyset", envs[2]["op"])
	assert.Equal(t, "hydrationComplete", envs[3]["op"])
}

// TestNatsSubjectAdapter_PipelinedPublishDefersItsFailureToFlush is the
// assertion that a pipeline on the context is actually being USED rather than
// quietly ignored: with the backing stream gone, a pipelined Upsert still
// returns nil — nothing has been awaited yet — and the failure appears at the
// flush, while the same write with no pipeline on the context fails on the spot.
//
// It is the seam a pipelined caller's error handling turns on: a personal
// publish failure inside a pipeline names no single row, so the flush is the
// only place that can withhold the keyset frame.
func TestNatsSubjectAdapter_PipelinedPublishDefersItsFailureToFlush(t *testing.T) {
	conn, js := startSyncServer(t)
	ctx := context.Background()
	a, err := adapter.NewNatsSubjectAdapter(ctx, conn, "rule-defer", "lattice.sync.user", "SYNC", []string{adapter.PersonalActorKeyField, "entityId"})
	require.NoError(t, err)
	require.NoError(t, js.DeleteStream(ctx, "SYNC"))

	pipe := a.NewPublishPipeline()
	writeCtx := adapter.WithPublishPipeline(ctx, pipe)
	keys := map[string]any{adapter.PersonalActorKeyField: "identityDefer", "entityId": "e1"}
	require.NoError(t, a.Upsert(writeCtx, keys, map[string]any{"v": 1.0}, 1),
		"a pipelined publish has awaited nothing yet, so it cannot have failed yet")
	require.Error(t, pipe.Flush(ctx), "the flush is where the failure surfaces")

	require.Error(t, a.Upsert(ctx, keys, map[string]any{"v": 1.0}, 2),
		"with no pipeline on the ctx the same write fails on the spot")
}

// TestNatsSubjectAdapter_SatisfiesPublishPipelineOpener pins the capability the
// pipeline type-asserts on: without it every caller silently takes the per-row
// round trip and nothing fails.
func TestNatsSubjectAdapter_SatisfiesPublishPipelineOpener(t *testing.T) {
	var _ adapter.PublishPipelineOpener = (*adapter.NatsSubjectAdapter)(nil)
}
