package pipeline

import (
	"context"
	"errors"
	"testing"
	"time"

	nats "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/asolgan/lattice/internal/refractor/ruleengine/simple"
	"github.com/asolgan/lattice/internal/refractor/failure"
	"github.com/asolgan/lattice/internal/refractor/health"
	"github.com/asolgan/lattice/internal/substrate"
)

// recordingReporter records the sequence of status writes the sink issues.
type recordingReporter struct {
	writes []string
}

func (r *recordingReporter) SetActive(context.Context) error {
	r.writes = append(r.writes, "active")
	return nil
}

func (r *recordingReporter) SetPaused(_ context.Context, reason, _ string) error {
	r.writes = append(r.writes, "paused:"+reason)
	return nil
}

func (r *recordingReporter) SetRebuilding(context.Context) error {
	r.writes = append(r.writes, "rebuilding")
	return nil
}

func (r *recordingReporter) GetStatus(context.Context) (health.Entry, error) {
	return health.Entry{Status: "active"}, nil
}

// TestHealthSink_SetActive_NoRebuild verifies the plain path: no rebuild in
// flight → SetActive writes "active".
func TestHealthSink_SetActive_NoRebuild(t *testing.T) {
	rec := &recordingReporter{}
	sink := &healthSink{reporter: rec, rebuildInFlight: func() bool { return false }}

	require.NoError(t, sink.SetActive(context.Background()))
	assert.Equal(t, []string{"active"}, rec.writes)
}

// TestHealthSink_SetActive_RebuildInFlight verifies that a supervisor
// active-persist during an in-flight rebuild (probe recovery mid-rescan)
// re-persists "rebuilding" and never writes a premature "active".
func TestHealthSink_SetActive_RebuildInFlight(t *testing.T) {
	rec := &recordingReporter{}
	sink := &healthSink{reporter: rec, rebuildInFlight: func() bool { return true }}

	require.NoError(t, sink.SetActive(context.Background()))
	assert.Equal(t, []string{"rebuilding"}, rec.writes)
}

// TestHealthSink_SetActive_RebuildCompletesDuringWrite verifies the
// double-check: when the rebuild watcher clears the flag concurrently with the
// sink's rebuilding write, the sink falls through to "active" so the entry is
// not left "rebuilding" with no watcher remaining to clear it.
func TestHealthSink_SetActive_RebuildCompletesDuringWrite(t *testing.T) {
	rec := &recordingReporter{}
	calls := 0
	sink := &healthSink{reporter: rec, rebuildInFlight: func() bool {
		calls++
		return calls == 1 // in flight on the first check, cleared on the re-check
	}}

	require.NoError(t, sink.SetActive(context.Background()))
	assert.Equal(t, []string{"rebuilding", "active"}, rec.writes)
}

// keyedAdapter fails Upsert/Delete with the error configured for the result's
// "k" key value; keys without an entry succeed.
type keyedAdapter struct {
	errs map[string]error
}

func (a *keyedAdapter) write(keys map[string]any) error {
	k, _ := keys["k"].(string)
	return a.errs[k]
}

func (a *keyedAdapter) Upsert(_ context.Context, keys map[string]any, _ map[string]any, _ uint64) error {
	return a.write(keys)
}
func (a *keyedAdapter) Delete(_ context.Context, keys map[string]any, _ uint64) error {
	return a.write(keys)
}
func (a *keyedAdapter) Probe(context.Context) error                         { return nil }
func (a *keyedAdapter) Close() error                                        { return nil }

// TestWriteResults_NoRetryEnqueueWhileBatchLeftPending verifies that a batch
// whose early result fails transient and whose later result fails infra leaves
// the message pending WITHOUT enqueuing the transient result: redelivery
// re-runs the whole batch, so an eager enqueue would add a duplicate
// retry-queue entry on every pause/resume cycle.
func TestWriteResults_NoRetryEnqueueWhileBatchLeftPending(t *testing.T) {
	ad := &keyedAdapter{errs: map[string]error{
		"a": errors.New("transient write failure"), // CatTransient
		"b": nats.ErrConnectionClosed,              // CatInfra
	}}
	rq := failure.NewRetryQueue()

	p, err := New("rule-dedup", "nats_kv", nil, "CORE", nil, nil, ad, nil)
	require.NoError(t, err)
	p.SetRetryQueue(rq, nil, 3, time.Millisecond)

	ctx := context.Background()
	msg := substrate.Message{Subject: "$KV.CORE.vtx.agreement.x", Body: []byte(`{"id":"x"}`)}
	results := []simple.EvalResult{
		{Keys: map[string]any{"k": "a"}, Row: map[string]any{"k": "a"}},
		{Keys: map[string]any{"k": "b"}, Row: map[string]any{"k": "b"}},
	}

	// First delivery: infra on "b" leaves the message pending; "a" must NOT
	// have been enqueued.
	dec, werr := p.writeResults(ctx, msg, "vtx.agreement.x", results)
	assert.Equal(t, substrate.Nak, dec)
	require.Error(t, werr)
	assert.Equal(t, 0, rq.Len(), "no retry entry while the message is left pending")

	// Redelivery while the infra failure persists (each pause/resume cycle):
	// still no accumulation.
	dec, werr = p.writeResults(ctx, msg, "vtx.agreement.x", results)
	assert.Equal(t, substrate.Nak, dec)
	require.Error(t, werr)
	assert.Equal(t, 0, rq.Len(), "redelivery must not accumulate retry entries")

	// Infra recovers; the transient failure persists → the batch disposes via
	// the retry queue exactly once and the message is acked.
	delete(ad.errs, "b")
	dec, werr = p.writeResults(ctx, msg, "vtx.agreement.x", results)
	assert.Equal(t, substrate.Ack, dec)
	require.NoError(t, werr)
	assert.Equal(t, 1, rq.Len(), "exactly one retry entry once the batch disposes")
}
