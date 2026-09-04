package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/natsfixture"
	"github.com/operatinggraph/lattice/internal/refractor/adapter"
	"github.com/operatinggraph/lattice/internal/refractor/failure"
	"github.com/operatinggraph/lattice/internal/refractor/health"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/substrate"
)

const (
	syncStreamName    = "SYNC"
	syncSubjectPrefix = "lattice.sync.user"
	pipelineRuleID    = "personal-pipeline-rule"
)

// personalSyncFixture is one embedded NATS server carrying both halves of a
// personal lens: the Core/Adj KV buckets the pipeline reads and the SYNC stream
// its real NatsSubjectAdapter publishes onto. They share a server so a test can
// watch rows and frames arrive on the same wire the pipeline writes to, which
// is the only place the ordering between them is observable.
type personalSyncFixture struct {
	conn   *substrate.Conn
	js     jetstream.JetStream
	coreKV *substrate.KV
	adjKV  *substrate.KV
}

func newPersonalSyncFixture(t *testing.T) *personalSyncFixture {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}
	_, nc := natsfixture.Server(t)
	js, err := jetstream.New(nc)
	require.NoError(t, err)
	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)

	ctx := context.Background()
	open := func(bucket string) *substrate.KV {
		_, cerr := js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: bucket})
		require.NoError(t, cerr)
		kv, oerr := conn.OpenKV(ctx, bucket)
		require.NoError(t, oerr)
		return kv
	}
	return &personalSyncFixture{conn: conn, js: js, coreKV: open("CORE"), adjKV: open("ADJ")}
}

// newTarget builds the real personal transport over the fixture's connection,
// wrapped so the test can see how many frames were published and whether the
// per-row writes returned cleanly.
func (f *personalSyncFixture) newTarget(t *testing.T) *framingSubjectAdapter {
	t.Helper()
	inner, err := adapter.NewNatsSubjectAdapter(context.Background(), f.conn, pipelineRuleID, syncSubjectPrefix, syncStreamName,
		[]string{adapter.PersonalActorKeyField, "entityId"})
	require.NoError(t, err)
	return &framingSubjectAdapter{NatsSubjectAdapter: inner, conn: f.conn}
}

// deleteSyncStream removes the stream the personal target publishes onto, so
// every publish resolves to "no stream accepted this". It is the deterministic
// way to fail a pipeline: the async sends still succeed (nothing has been
// awaited yet), and the failure surfaces at the flush rather than per row.
func (f *personalSyncFixture) deleteSyncStream(t *testing.T) {
	t.Helper()
	require.NoError(t, f.js.DeleteStream(context.Background(), syncStreamName))
}

// envelopes reads up to want envelopes off one actor's SYNC subject, in stream
// order, with the stream sequence each landed at.
func (f *personalSyncFixture) envelopes(t *testing.T, actorID, durable string, want int) (ops []string, keys []string, seqs []uint64) {
	t.Helper()
	subject := syncSubjectPrefix + "." + actorID
	cons, err := f.js.CreateOrUpdateConsumer(context.Background(), syncStreamName, jetstream.ConsumerConfig{
		Durable:       durable,
		FilterSubject: subject,
		DeliverPolicy: jetstream.DeliverAllPolicy,
		AckPolicy:     jetstream.AckExplicitPolicy,
	})
	require.NoError(t, err)
	batch, err := cons.Fetch(want, jetstream.FetchMaxWait(5*time.Second))
	require.NoError(t, err)
	for m := range batch.Messages() {
		var env struct {
			Op  string `json:"op"`
			Key string `json:"key"`
		}
		require.NoError(t, json.Unmarshal(m.Data(), &env))
		md, mderr := m.Metadata()
		require.NoError(t, mderr)
		ops = append(ops, env.Op)
		keys = append(keys, env.Key)
		seqs = append(seqs, md.Sequence.Stream)
		_ = m.Ack()
	}
	return ops, keys, seqs
}

// framingSubjectAdapter is the real personal transport with two observations
// added: how many keyset frames were published, and whether each row write
// returned an error. Together they separate "the pipeline failed at its flush
// and the frame was withheld" from "the rows failed one by one", which look the
// same from the outside once both end in a Nak.
//
// NatsSubjectAdapter implements neither OutcomeUpserter nor OutcomeDeleter, so
// writeResults takes the plain Upsert/Delete this type overrides; every other
// capability — PublishPipelineOpener included — is promoted through the embedding
// untouched, which is exactly what the test needs the pipeline to find.
type framingSubjectAdapter struct {
	*adapter.NatsSubjectAdapter

	mu      sync.Mutex
	frames  int
	rows    int
	rowErrs int
	// rowPipe is the pipeline the caller opened through this adapter, and
	// pendingAtFrame how much of it was still un-awaited at the moment the
	// keyset frame went out. Zero is the whole claim of "flushed before the
	// frame": the rows are stored, not merely sent. Stream order alone cannot
	// show this — a frame published without any flush still lands after rows
	// already on the wire — so the pending count is what a short-circuited
	// flush fails.
	rowPipe        *substrate.PublishPipeline
	pendingAtFrame int
	// windowOverride, when non-zero, is the window the pipelines this adapter
	// hands out are opened with, so a test can make a write loop wider than its
	// own window and force an ack to be awaited mid-loop. conn is the same
	// connection the embedded adapter publishes on, which is what a pipeline
	// must be opened from.
	windowOverride int
	conn           *substrate.Conn
}

func (f *framingSubjectAdapter) NewPublishPipeline() *substrate.PublishPipeline {
	f.mu.Lock()
	window := f.windowOverride
	f.mu.Unlock()
	pl := f.conn.NewPublishPipeline(window)
	f.mu.Lock()
	f.rowPipe = pl
	f.mu.Unlock()
	return pl
}

func (f *framingSubjectAdapter) Upsert(ctx context.Context, keys, row map[string]any, seq uint64) error {
	err := f.NatsSubjectAdapter.Upsert(ctx, keys, row, seq)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rows++
	if err != nil {
		f.rowErrs++
	}
	return err
}

func (f *framingSubjectAdapter) PublishKeySet(ctx context.Context, actorID string, keys []map[string]any, revision uint64) error {
	f.mu.Lock()
	f.frames++
	if f.rowPipe != nil {
		f.pendingAtFrame = f.rowPipe.Pending()
	}
	f.mu.Unlock()
	return f.NatsSubjectAdapter.PublishKeySet(ctx, actorID, keys, revision)
}

func (f *framingSubjectAdapter) counts() (frames, rows, rowErrs int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.frames, f.rows, f.rowErrs
}

// pendingWhenFramed reports how many row publishes were still un-awaited when
// the keyset frame was published.
func (f *framingSubjectAdapter) pendingWhenFramed() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pendingAtFrame
}

// personalUpserts builds n upsert results for one actor, keyed the way the
// personal envelope keys them.
func personalUpserts(actorID string, n int) []ruleengine.EvalResult {
	results := make([]ruleengine.EvalResult, 0, n)
	for i := 0; i < n; i++ {
		entityID := fmt.Sprintf("lease.%02d", i)
		results = append(results, ruleengine.EvalResult{
			Keys: map[string]any{adapter.PersonalActorKeyField: actorID, "entityId": entityID},
			Row:  map[string]any{"anchor": entityID, "kind": "lease", "i": float64(i)},
		})
	}
	return results
}

// TestWriteResults_FlushesRowPipelineBeforeFrame pins the shape of the wire the
// Edge client reads: every row lands, in call order, and the keyset frame that
// claims to describe them sits at a HIGHER stream sequence than all of them.
func TestWriteResults_FlushesRowPipelineBeforeFrame(t *testing.T) {
	fx := newPersonalSyncFixture(t)
	target := fx.newTarget(t)
	p := newPersonalPipelineOn(t, fx.coreKV, fx.adjKV, target)
	ctx := context.Background()

	const n = 12
	results := personalUpserts(personalActorA, n)
	decision, err := p.writeResults(ctx, p.ruleState(), substrate.Message{Sequence: 42},
		substrate.VertexKey("identity", personalActorA), results,
		[]string{substrate.VertexKey("identity", personalActorA)}, ScopeAll())

	require.NoError(t, err)
	require.Equal(t, substrate.Ack, decision)

	frames, rows, rowErrs := target.counts()
	assert.Equal(t, n, rows)
	assert.Zero(t, rowErrs)
	assert.Equal(t, 1, frames)

	ops, keys, seqs := fx.envelopes(t, personalActorA, "beforeframe", n+1)
	require.Len(t, ops, n+1, "every row plus the frame must be on the actor's subject")
	for i := 0; i < n; i++ {
		assert.Equal(t, "upsert", ops[i])
		assert.Equal(t, fmt.Sprintf("lease.%02d", i), keys[i], "pipelined rows must keep their call order on the wire")
	}
	assert.Equal(t, "keyset", ops[n])
	for i := 0; i < n; i++ {
		assert.Greater(t, seqs[n], seqs[i],
			"the keyset frame must sit above every row it describes")
	}
	assert.Zero(t, target.pendingWhenFramed(),
		"every row's ack must be awaited BEFORE the frame — stream order alone would hold even with the flush short-circuited, so this is the assertion that pins it")
}

// TestWriteResults_FlushFailureNaks pins the disposition a pipelined write loop
// turns on: with the acks awaited once, a personal publish failure surfaces at
// the flush and NOT at any single row, so the flush is the only place that can
// withhold the frame and redeliver.
//
// Every row write returns cleanly here — that is the point, and the assertion
// on rowErrs is what proves the failure really did arrive at the flush rather
// than the loop. The frame must never be published: it would assert an
// authoritative key set for rows that are not on the wire, and the Edge client
// prunes against exactly that.
func TestWriteResults_FlushFailureNaks(t *testing.T) {
	fx := newPersonalSyncFixture(t)
	target := fx.newTarget(t)
	p := newPersonalPipelineOn(t, fx.coreKV, fx.adjKV, target)
	fx.deleteSyncStream(t)
	ctx := context.Background()

	results := personalUpserts(personalActorA, 6)
	decision, _ := p.writeResults(ctx, p.ruleState(), substrate.Message{Sequence: 42},
		substrate.VertexKey("identity", personalActorA), results,
		[]string{substrate.VertexKey("identity", personalActorA)}, ScopeAll())

	require.Equal(t, substrate.Nak, decision,
		"a write loop whose rows did not land must redeliver, never ack")
	frames, rows, rowErrs := target.counts()
	assert.Equal(t, 6, rows)
	assert.Zero(t, rowErrs, "with the acks pipelined, no individual row write fails — the flush is where it surfaces")
	assert.Zero(t, frames, "no keyset frame may describe rows that never landed")
}

// TestHydrate_PipelineFlushedBeforeFrame covers the cold bulk projection: a
// device's whole mirror published through one pipeline, flushed before the
// keyset frame and the terminal marker, and a failed flush surfacing as the
// hydrate's error with neither frame nor marker published.
func TestHydrate_PipelineFlushedBeforeFrame(t *testing.T) {
	fx := newPersonalSyncFixture(t)
	target := fx.newTarget(t)
	p := newPersonalPipelineOn(t, fx.coreKV, fx.adjKV, target)
	ctx := context.Background()
	const n = 5
	seedPersonalLeases(t, fx, personalActorA, n)

	high, err := p.Hydrate(ctx, personalActorA)
	require.NoError(t, err)
	require.NotZero(t, high)

	frames, rows, rowErrs := target.counts()
	assert.Equal(t, n, rows)
	assert.Zero(t, rowErrs)
	assert.Equal(t, 1, frames)

	ops, _, seqs := fx.envelopes(t, personalActorA, "hydrate", n+2)
	require.Len(t, ops, n+2, "every row, then the frame, then the terminal marker")
	for i := 0; i < n; i++ {
		assert.Equal(t, "upsert", ops[i])
		assert.Greater(t, seqs[n], seqs[i], "the frame must sit above every row the hydrate published")
	}
	assert.Zero(t, target.pendingWhenFramed(),
		"the cold mirror's acks must all be awaited before the frame claims to describe it")
	assert.Equal(t, "keyset", ops[n])
	assert.Equal(t, "hydrationComplete", ops[n+1])
}

// TestHydrate_FlushFailureIsTheHydrateError is the failure half of the cold
// path: the rows never landed, so the device must be told rather than handed a
// frame and a marker over an empty mirror.
func TestHydrate_FlushFailureIsTheHydrateError(t *testing.T) {
	fx := newPersonalSyncFixture(t)
	target := fx.newTarget(t)
	p := newPersonalPipelineOn(t, fx.coreKV, fx.adjKV, target)
	seedPersonalLeases(t, fx, personalActorA, 4)
	fx.deleteSyncStream(t)

	_, err := p.Hydrate(context.Background(), personalActorA)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "write", "a failed pipeline flush is a write failure of the hydrate")
	frames, _, rowErrs := target.counts()
	assert.Zero(t, rowErrs, "the rows returned cleanly; the flush is where the failure arrived")
	assert.Zero(t, frames, "no frame over a mirror that was never published")
}

// TestReprojectPersonalActor_PipelineFlushedBeforeFrame is the same contract on the
// grant-change path, whose frame is the retraction transport: publishing it over
// rows that never landed would tell the client to prune to a set the wire never
// carried.
func TestReprojectPersonalActor_PipelineFlushedBeforeFrame(t *testing.T) {
	fx := newPersonalSyncFixture(t)
	target := fx.newTarget(t)
	p := newPersonalPipelineOn(t, fx.coreKV, fx.adjKV, target)
	ctx := context.Background()
	const n = 5
	seedPersonalLeases(t, fx, personalActorA, n)

	require.NoError(t, p.ReprojectPersonalActor(ctx, personalActorA, ScopeAll()))

	frames, rows, rowErrs := target.counts()
	assert.Equal(t, n, rows)
	assert.Zero(t, rowErrs)
	assert.Equal(t, 1, frames)

	ops, _, seqs := fx.envelopes(t, personalActorA, "reproject", n+1)
	require.Len(t, ops, n+1)
	for i := 0; i < n; i++ {
		assert.Equal(t, "upsert", ops[i])
		assert.Greater(t, seqs[n], seqs[i], "the retracting frame must sit above every row it leaves standing")
	}
	assert.Zero(t, target.pendingWhenFramed(),
		"a retracting frame published over un-awaited rows would prune against a set the wire had not accepted")
	assert.Equal(t, "keyset", ops[n])
}

// TestReprojectPersonalActor_ScopeNoneFramesOverAnEmptyPipeline is the standing
// healer's ordinary pass on the same pipelined path: the row loop admits
// nothing, the opened pipeline is flushed empty, and the frame — the pass's
// whole output — still goes out.
func TestReprojectPersonalActor_ScopeNoneFramesOverAnEmptyPipeline(t *testing.T) {
	fx := newPersonalSyncFixture(t)
	target := fx.newTarget(t)
	p := newPersonalPipelineOn(t, fx.coreKV, fx.adjKV, target)
	ctx := context.Background()
	seedPersonalLeases(t, fx, personalActorA, 5)

	require.NoError(t, p.ReprojectPersonalActor(ctx, personalActorA, ScopeNone()))

	frames, rows, rowErrs := target.counts()
	assert.Zero(t, rows, "a frames-only pass writes no row")
	assert.Zero(t, rowErrs)
	assert.Equal(t, 1, frames)
	assert.Zero(t, target.pendingWhenFramed(),
		"the pipeline is opened and flushed even with nothing in it, so the frame's ordering guarantee is unchanged")

	ops, _, _ := fx.envelopes(t, personalActorA, "reproject-none", 1)
	require.Len(t, ops, 1)
	assert.Equal(t, "keyset", ops[0], "the frame is the only thing this pass puts on the wire")
}

// TestReprojectPersonalActor_FlushFailureWithholdsTheFrame is the failure half.
func TestReprojectPersonalActor_FlushFailureWithholdsTheFrame(t *testing.T) {
	fx := newPersonalSyncFixture(t)
	target := fx.newTarget(t)
	p := newPersonalPipelineOn(t, fx.coreKV, fx.adjKV, target)
	seedPersonalLeases(t, fx, personalActorA, 4)
	fx.deleteSyncStream(t)

	err := p.ReprojectPersonalActor(context.Background(), personalActorA, ScopeAll())

	require.Error(t, err)
	frames, _, rowErrs := target.counts()
	assert.Zero(t, rowErrs, "the rows returned cleanly; the flush is where the failure arrived")
	assert.Zero(t, frames, "a frame published over rows that never landed would retract against a set the wire never carried")
}

// TestWriteResults_AuditPipelineLandsEveryEntry pins that a pipelined audit
// trail is a complete one: one entry per committed row is on the lens's audit
// subject once writeResults returns.
func TestWriteResults_AuditPipelineLandsEveryEntry(t *testing.T) {
	fx := newPersonalSyncFixture(t)
	target := fx.newTarget(t)
	p := newPersonalPipelineOn(t, fx.coreKV, fx.adjKV, target)
	ctx := context.Background()
	require.NoError(t, health.EnsureAuditStream(ctx, fx.conn))
	p.SetAuditWriter(health.NewAuditWriter(fx.conn, pipelineRuleID))

	const n = 7
	decision, err := p.writeResults(ctx, p.ruleState(), substrate.Message{Sequence: 42},
		substrate.VertexKey("identity", personalActorA), personalUpserts(personalActorA, n), nil, ScopeAll())
	require.NoError(t, err)
	require.Equal(t, substrate.Ack, decision)

	stream, err := fx.js.Stream(ctx, health.AuditStreamName)
	require.NoError(t, err)
	info, err := stream.Info(ctx)
	require.NoError(t, err)
	assert.Equal(t, uint64(n), info.State.Msgs, "every committed row must still be audited")
}

// TestWriteResults_AuditFlushFailureStillAcks pins the audit trail's
// best-effort posture at the flush boundary: a lens whose audit stream is
// absent still processes its message. The two pipelines are separate for
// exactly this reason — folding the audit into the row pipeline would let a
// lost forensic entry redeliver rows that were written perfectly.
func TestWriteResults_AuditFlushFailureStillAcks(t *testing.T) {
	fx := newPersonalSyncFixture(t)
	target := fx.newTarget(t)
	p := newPersonalPipelineOn(t, fx.coreKV, fx.adjKV, target)
	ctx := context.Background()
	// No EnsureAuditStream: nothing accepts the audit subject.
	p.SetAuditWriter(health.NewAuditWriter(fx.conn, pipelineRuleID))

	const n = 4
	decision, err := p.writeResults(ctx, p.ruleState(), substrate.Message{Sequence: 42},
		substrate.VertexKey("identity", personalActorA), personalUpserts(personalActorA, n),
		[]string{substrate.VertexKey("identity", personalActorA)}, ScopeAll())

	require.NoError(t, err)
	require.Equal(t, substrate.Ack, decision, "an audit failure must never decide a message's fate")
	frames, _, rowErrs := target.counts()
	assert.Zero(t, rowErrs)
	assert.Equal(t, 1, frames, "the rows applied, so the frame is published as usual")

	ops, _, _ := fx.envelopes(t, personalActorA, "auditfail", n+1)
	assert.Len(t, ops, n+1, "every row and the frame still reached the wire")
}

// seedPersonalLeases writes the identity and n leases it holds, so the personal
// cypher (see newPersonalTestPipeline) projects n rows for that actor.
func seedPersonalLeases(t *testing.T, fx *personalSyncFixture, actorID string, n int) {
	t.Helper()
	putPersonalVertex(t, fx.coreKV, substrate.VertexKey("identity", actorID), "identity", nil)
	require.LessOrEqual(t, n, 9, "the fixture's lease ids vary in one NanoID-alphabet digit")
	for i := 0; i < n; i++ {
		// 20 chars from the limited NanoID alphabet (no 0, I, l or O).
		leaseID := fmt.Sprintf("LeaseAbcdeFghijKmnp"[:19]+"%d", i+1)
		putPersonalVertex(t, fx.coreKV, substrate.VertexKey("lease", leaseID), "lease",
			map[string]any{"id": fmt.Sprintf("lease.%02d", i)})
		buildCollisionEdge(t, fx.adjKV, "holds", "identity", actorID, "lease", leaseID)
	}
}

// restrictSyncSubjects narrows the SYNC stream to exactly the actors named, so
// a publish for any other actor finds no stream bound to its subject and
// resolves with ErrNoStreamResponse. It is how a test refuses ONE row of a
// write loop while the rest succeed — the shape a window await surfaces and a
// whole-stream deletion cannot produce.
func (f *personalSyncFixture) restrictSyncSubjects(t *testing.T, actorIDs ...string) {
	t.Helper()
	subjects := make([]string, 0, len(actorIDs))
	for _, a := range actorIDs {
		subjects = append(subjects, syncSubjectPrefix+"."+a)
	}
	stream, err := f.js.Stream(context.Background(), syncStreamName)
	require.NoError(t, err)
	cfg := stream.CachedInfo().Config
	cfg.Subjects = subjects
	_, err = f.js.UpdateStream(context.Background(), cfg)
	require.NoError(t, err)
}

// perActorUpserts builds one upsert result per actor, so each row publishes to
// its own subject and a stream that admits some subjects and not others can
// refuse an individual row.
func perActorUpserts(actorIDs []string) []ruleengine.EvalResult {
	results := make([]ruleengine.EvalResult, 0, len(actorIDs))
	for i, a := range actorIDs {
		results = append(results, ruleengine.EvalResult{
			Keys: map[string]any{adapter.PersonalActorKeyField: a, "entityId": fmt.Sprintf("lease.%02d", i)},
			Row:  map[string]any{"anchor": fmt.Sprintf("lease.%02d", i), "kind": "lease"},
		})
	}
	return results
}

// personalActorIDs returns n distinct 20-char actor ids from the limited NanoID
// alphabet.
func personalActorIDs(t *testing.T, n int) []string {
	t.Helper()
	require.LessOrEqual(t, n, 9, "the fixture's actor ids vary in one NanoID-alphabet digit")
	ids := make([]string, 0, n)
	for i := 0; i < n; i++ {
		ids = append(ids, fmt.Sprintf("ActorAbcdeFghijKmnp"[:19]+"%d", i+1))
	}
	return ids
}

// TestWriteResults_WindowAwaitFailureNaksAndRetriesNothing is the end-to-end
// guard on the error seam.
//
// The refused row is the FIRST one, and the window is smaller than the batch,
// so the failure resolves inside a later row's Add. If that Add handed the
// error back, writeResults would charge it to the row it was called for,
// classify it transient, capture THAT row on the retry queue, and then find a
// clean flush — the failed future having already been consumed — and ACK the
// message. Row one would be gone: never stored, never retried, never DLQ'd, and
// the keyset frame would assert it to the device anyway.
//
// So: Nak, no frame, nothing on the retry queue, and no per-row error anywhere
// (rowErrs == 0) — the failure belongs to the flush and only to the flush.
func TestWriteResults_WindowAwaitFailureNaksAndRetriesNothing(t *testing.T) {
	fx := newPersonalSyncFixture(t)
	target := fx.newTarget(t)
	p := newPersonalPipelineOn(t, fx.coreKV, fx.adjKV, target)
	ctx := context.Background()

	// A retry queue IS configured: without one every transient write error
	// Naks anyway, and the test could not tell a correct Nak from the
	// misattributed retry-then-ack this exists to refuse.
	queue := failure.NewRetryQueue()
	p.SetRetryQueue(queue, fx.conn, 3, time.Millisecond)

	actors := personalActorIDs(t, 6)
	// Every actor but the first is admitted, so exactly row 0 is refused.
	fx.restrictSyncSubjects(t, actors[1:]...)

	// A window below the row count forces the refused row's ack to be awaited
	// inside a later Add rather than at the flush.
	target.windowOverride = 2

	enumerated := make([]string, 0, len(actors))
	for _, a := range actors {
		enumerated = append(enumerated, substrate.VertexKey("identity", a))
	}
	decision, _ := p.writeResults(ctx, p.ruleState(), substrate.Message{Sequence: 42},
		substrate.VertexKey("identity", actors[0]), perActorUpserts(actors), enumerated, ScopeAll())

	require.Equal(t, substrate.Nak, decision,
		"a row that never landed must redeliver — acking it loses the row with no retry and no DLQ behind it")
	frames, rows, rowErrs := target.counts()
	assert.Equal(t, len(actors), rows)
	assert.Zero(t, rowErrs,
		"no individual row write may fail: the refused row's ack resolves inside a LATER row's Add, and that Add must not charge it to the row it was called for")
	assert.Zero(t, frames, "no frame may describe a set the wire refused part of")
	assert.Zero(t, queue.Len(),
		"nothing may be retried: a retry entry here would be for the wrong row, and its presence is what turns the Nak into an Ack")
}

// TestWriteResults_FlushFailureAuditsNothingAndDoesNotAdvanceTheClock is the
// second half of the same discipline, on the records rather than the wire.
//
// An audit entry's outputRowHash is a claim that a row landed, and
// lastProjectedAt is a claim that the target changed. Both are written from the
// write loop, where a pipelined row is only SENT — so emitting them there
// records rows a failed flush is about to redeliver, and leaves an operator
// reading a freshness clock ticking over a target that took nothing.
func TestWriteResults_FlushFailureAuditsNothingAndDoesNotAdvanceTheClock(t *testing.T) {
	fx := newPersonalSyncFixture(t)
	target := fx.newTarget(t)
	p := newPersonalPipelineOn(t, fx.coreKV, fx.adjKV, target)
	ctx := context.Background()
	require.NoError(t, health.EnsureAuditStream(ctx, fx.conn))
	p.SetAuditWriter(health.NewAuditWriter(fx.conn, pipelineRuleID))

	before := p.Progress().LastProjectedAt
	fx.deleteSyncStream(t)

	decision, _ := p.writeResults(ctx, p.ruleState(), substrate.Message{Sequence: 42},
		substrate.VertexKey("identity", personalActorA), personalUpserts(personalActorA, 5),
		[]string{substrate.VertexKey("identity", personalActorA)}, ScopeAll())

	require.Equal(t, substrate.Nak, decision)

	stream, err := fx.js.Stream(ctx, health.AuditStreamName)
	require.NoError(t, err)
	info, err := stream.Info(ctx)
	require.NoError(t, err)
	assert.Equal(t, uint64(0), info.State.Msgs,
		"a row that never landed must leave no audit entry claiming its hash")
	assert.Equal(t, before, p.Progress().LastProjectedAt,
		"the freshness clock must not advance over a target that took nothing")
}
