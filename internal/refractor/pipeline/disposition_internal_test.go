package pipeline

// dispositionEvalErr coverage: the fan-out evaluate-stage error → Decision
// mapping (pipeline.go:876) and the two call sites (evalLinkFanOut,
// evalAspectFanOut) that route into it.

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	nats "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/jsstore"
	"github.com/operatinggraph/lattice/internal/refractor/failure"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/substrate"
)

var errDispositionBoom = errors.New("disposition: synthetic failure")

// TestDispositionEvalErr_FourTiers table-drives the four failure.Category →
// (Decision, error) mappings dispositionEvalErr implements. The three
// non-terminal rows need no NATS connection — retryConn/reporter stay nil.
func TestDispositionEvalErr_FourTiers(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		wantDec    substrate.Decision
		wantErrNil bool
	}{
		{
			name:       "infra classifies Nak + non-nil err (supervisor pauses)",
			err:        failure.Infrastructure(errDispositionBoom),
			wantDec:    substrate.Nak,
			wantErrNil: false,
		},
		{
			name:       "structural classifies Nak + non-nil err (supervisor pauses, no DLQ)",
			err:        failure.Structural(errDispositionBoom),
			wantDec:    substrate.Nak,
			wantErrNil: false,
		},
		{
			name:       "unclassified error defaults to transient: Nak + nil err (redelivery re-runs, no DLQ)",
			err:        errDispositionBoom,
			wantDec:    substrate.Nak,
			wantErrNil: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := &Pipeline{ruleID: "rule-disposition"}
			dec, err := p.dispositionEvalErr(context.Background(),
				substrate.Message{Subject: "$KV.CORE.vtx.agreement.X", Body: []byte("{}")},
				"vtx.agreement.X", "traversal", tc.err)
			require.Equal(t, tc.wantDec, dec)
			if tc.wantErrNil {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				require.Same(t, tc.err, err, "the pause path must preserve the original error identity")
			}
		})
	}
}

// TestDispositionEvalErr_Terminal_PublishesDLQAndAcks is the fourth tier: a
// Terminal-classified error publishes a DLQ entry and Acks (never Naks) so a
// permanently-bad entity does not wedge the lane. NATS-backed — short-skipped.
func TestDispositionEvalErr_Terminal_PublishesDLQAndAcks(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}
	conn, js := newDispositionConn(t)
	ctx := context.Background()

	const ruleID = "rule-disposition-terminal"
	p := &Pipeline{ruleID: ruleID, retryConn: conn}

	msg := substrate.Message{Subject: "$KV.CORE.vtx.agreement.X", Body: []byte(`{"key":"vtx.agreement.X"}`)}
	dec, err := p.dispositionEvalErr(ctx, msg, "vtx.agreement.X", "traversal",
		failure.Terminal(errDispositionBoom))
	require.NoError(t, err)
	require.Equal(t, substrate.Ack, dec)

	streamName := "REFRACTOR_DLQ_RULE-DISPOSITION-TERMINAL"
	var dlqMsg failure.DLQMessage
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		cons, cerr := js.OrderedConsumer(ctx, streamName, jetstream.OrderedConsumerConfig{})
		if cerr != nil {
			time.Sleep(50 * time.Millisecond)
			continue
		}
		batch, ferr := cons.Fetch(1, jetstream.FetchMaxWait(100*time.Millisecond))
		if ferr != nil {
			continue
		}
		got := false
		for m := range batch.Messages() {
			require.NoError(t, json.Unmarshal(m.Data(), &dlqMsg))
			got = true
		}
		if got {
			break
		}
	}
	require.Equal(t, "TERMINAL", dlqMsg.ErrorClass)
	require.Equal(t, "traversal", dlqMsg.FailedStage)
	require.Equal(t, "vtx.agreement.X", dlqMsg.EntityID)
}

// TestEvalLinkFanOut_RoutesEvaluateErrorIntoDispositionEvalErr pins the
// call-site wiring for evalLinkFanOut (pipeline.go:674): a forced evaluate
// error (engineKind Full, nil fullEngine — evaluateForEntryRaw's own explicit
// nil-check never runs on this call path, but executeFullForActor's type
// assertion on a nil CompiledRule interface fails cleanly) must route through
// dispositionEvalErr's transient mapping, not panic or bypass it.
func TestEvalLinkFanOut_RoutesEvaluateErrorIntoDispositionEvalErr(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}
	coreKV, adjKV, _ := newCollisionKVs(t)
	ctx := context.Background()

	const srcID = "WireLnkSrcAAAAAAAAAA"
	const dstID = "WireLnkDstAAAAAAAAAA"
	srcKey := "vtx.identity." + srcID
	writeCollisionVertex(t, coreKV, srcKey, "identity", map[string]any{"name": "src"})
	buildCollisionEdge(t, adjKV, "assignedTo", "identity", srcID, "identity", dstID)

	p := &Pipeline{
		ruleID:          "rule-wire-link",
		coreKV:          coreKV,
		adjKV:           adjKV,
		engineKind:      ruleengine.EngineFull,
		fullEngine:      nil, // forces the clean "expected *CompiledRule" error, not a panic
		actorEnumerator: NewActorEnumerator(adjKV, coreKV, "identity"),
	}

	linkKey := "lnk.identity." + srcID + ".assignedTo.identity." + dstID
	linkBody := []byte(`{"isDeleted":false}`)
	dec, err := p.evalLinkFanOut(ctx, substrate.Message{Subject: "$KV.CORE." + linkKey, Body: linkBody}, linkKey, false)
	require.NoError(t, err, "a transient-classified eval error naks with a nil error (redelivery re-runs)")
	require.Equal(t, substrate.Nak, dec)
}

// TestEvalAspectFanOut_RoutesEvaluateErrorIntoDispositionEvalErr pins the
// call-site wiring for evalAspectFanOut (pipeline.go:862): same forcing
// mechanism as the link fan-out sibling, seeded from a single actor vertex.
func TestEvalAspectFanOut_RoutesEvaluateErrorIntoDispositionEvalErr(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}
	coreKV, adjKV, _ := newCollisionKVs(t)
	ctx := context.Background()

	const actorID = "WireAspActorAAAAAAAA"
	actorKey := "vtx.identity." + actorID
	writeCollisionVertex(t, coreKV, actorKey, "identity", map[string]any{"name": "actor"})

	p := &Pipeline{
		ruleID:          "rule-wire-aspect",
		coreKV:          coreKV,
		adjKV:           adjKV,
		engineKind:      ruleengine.EngineFull,
		fullEngine:      nil,
		actorEnumerator: NewActorEnumerator(adjKV, coreKV, "identity"),
	}

	aspectKey := actorKey + ".name"
	dec, err := p.evalAspectFanOut(ctx, substrate.Message{Subject: "$KV.CORE." + aspectKey}, aspectKey)
	require.NoError(t, err)
	require.Equal(t, substrate.Nak, dec)
}

// newDispositionConn stands up an embedded NATS/JetStream server and returns
// a wrapped substrate.Conn plus the raw jetstream.JetStream handle (for
// reading back a published DLQ stream) — the terminal-DLQ test's sibling to
// newCollisionKVs, since dispositionEvalErr needs only a *substrate.Conn, not
// pre-provisioned KV buckets.
func newDispositionConn(t *testing.T) (*substrate.Conn, jetstream.JetStream) {
	t.Helper()
	opts := &natsserver.Options{
		JetStream: true,
		StoreDir:  jsstore.Dir(t),
		NoLog:     true,
		NoSigs:    true,
		Port:      natsserver.RANDOM_PORT,
	}
	s, err := natsserver.NewServer(opts)
	require.NoError(t, err)
	go s.Start()
	require.True(t, s.ReadyForConnections(5 * time.Second))

	nc, err := nats.Connect(s.ClientURL())
	require.NoError(t, err)
	t.Cleanup(func() {
		nc.Close()
		s.Shutdown()
	})

	js, err := jetstream.New(nc)
	require.NoError(t, err)
	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	return conn, js
}
