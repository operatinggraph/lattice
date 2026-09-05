package consumer

import (
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/natsfixture"
	"github.com/operatinggraph/lattice/internal/refractor/adjacency"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// TestProcessMsg_UnwritableKeyIsTerminal pins the classification rider: a
// NodeID that clears processMsg's own NATS-reserved-character screen
// (".", "*", ">", whitespace) can still be outside the narrower charset the
// jetstream KV client enforces client-side ([-/_=.a-zA-Z0-9]) — "!" is a
// legal NATS subject-token character but not a legal KV key character. Such
// a key can never become writable on redelivery, so processMsg must Term it
// rather than Nak it into an endless retry loop.
func TestProcessMsg_UnwritableKeyIsTerminal(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS JetStream")
	}
	ctx := context.Background()
	_, nc := natsfixture.Server(t)

	js, err := jetstream.New(nc)
	require.NoError(t, err)
	_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "adj-invalidkey-test"})
	require.NoError(t, err)

	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	adjKV, err := conn.OpenKV(ctx, "adj-invalidkey-test")
	require.NoError(t, err)

	b := NewBootstrapper(conn, "core-invalidkey-test", adjKV)

	evt := adjacency.CoreKVEvent{
		CoreKvKey: "core.e1", EdgeID: "e1", Name: "rel",
		Direction: "outbound", NodeID: "bad!node", OtherNodeID: "other",
	}
	body, err := json.Marshal(evt)
	require.NoError(t, err)

	decision := b.processMsg(ctx, substrate.Message{
		Subject: "$KV.core-invalidkey-test.edge.e1",
		Body:    body,
	})
	assert.Equal(t, substrate.Term, decision,
		"an unwritable NodeID must Term, never loop forever on Nak")
}

// The link-bridge path (processLinkEnvelope) carries the identical
// classification, but has no test of its own here: ParseLinkKey requires
// both endpoint segments to already be valid 20-char NanoIDs
// (keys.IsValidNanoID), and that alphabet (internal/substrate/keys/nanoid.go)
// is a strict subset of the jetstream KV client's own key charset — so a
// NodeID reaching adjacency.Build through the link bridge can never be the
// kind of malformed value this rider exists for. The classification is
// applied there for uniformity with the legacy path, not because it is
// reachable.

// TestAppliedSeq_IsAContiguousFloorNotAMaximum pins the cursor's arithmetic,
// and it is the difference between a number that answers the question and one
// that looks like it does.
//
// A Nak'd message is redelivered later while every sequence above it keeps
// retiring. A MAXIMUM would therefore sit at the stream head while the index is
// still missing the edge that one message carries — reporting caught-up during
// exactly the lag its reader consults it to detect, and doing so in the
// fail-OPEN direction (a retraction it should refuse would land). The floor
// stops one below the oldest thing still owed.
func TestAppliedSeq_IsAContiguousFloorNotAMaximum(t *testing.T) {
	b := &Bootstrapper{}
	require.Equal(t, uint64(0), b.AppliedSeq(),
		"a bootstrapper that has retired nothing has measured nothing")

	b.retire(10)
	require.Equal(t, uint64(10), b.AppliedSeq())
	b.retire(4)
	require.Equal(t, uint64(10), b.AppliedSeq(), "a redelivered older message must not walk the cursor back")
	b.retire(10)
	require.Equal(t, uint64(10), b.AppliedSeq(), "re-retiring the same sequence is not progress")

	t.Run("one owed message caps the floor below it", func(t *testing.T) {
		b := &Bootstrapper{}
		b.markOwed(100)
		for seq := uint64(101); seq <= 120; seq++ {
			b.retire(seq)
		}
		require.Equal(t, uint64(99), b.AppliedSeq(),
			"twenty retirements above an owed 100 do not make the index reflect 100")

		// The redelivery retires it, and the floor jumps to what was already
		// retired above it — no re-walk, no lost progress.
		b.retire(100)
		require.Equal(t, uint64(120), b.AppliedSeq())
	})

	t.Run("the lowest owed caps, whichever retires first", func(t *testing.T) {
		b := &Bootstrapper{}
		b.markOwed(50)
		b.markOwed(70)
		for seq := uint64(71); seq <= 90; seq++ {
			b.retire(seq)
		}
		require.Equal(t, uint64(49), b.AppliedSeq(), "the OLDEST debt is what bounds the floor")

		b.retire(50)
		require.Equal(t, uint64(69), b.AppliedSeq(),
			"clearing the lower debt exposes the higher one, which still caps the floor")

		b.retire(70)
		require.Equal(t, uint64(90), b.AppliedSeq())
	})

	t.Run("re-Nak'ing the same message is one debt, not two", func(t *testing.T) {
		b := &Bootstrapper{}
		b.markOwed(5)
		b.markOwed(5)
		b.retire(9)
		require.Equal(t, uint64(4), b.AppliedSeq())
		b.retire(5)
		require.Equal(t, uint64(9), b.AppliedSeq())
	})
}

// TestHandle_DispositionsDecideTheFloor pins which disposition does what. Ack
// and Term both RETIRE — the index will never do more with the message than it
// has (a Term discards one no consumer of this index could ever apply), so
// waiting for it would be waiting forever. A Nak leaves the message OWED, and
// the floor must stop below it.
func TestHandle_DispositionsDecideTheFloor(t *testing.T) {
	ctx := context.Background()

	t.Run("Ack retires", func(t *testing.T) {
		b := &Bootstrapper{ready: make(chan struct{})}
		// An empty body on a non-link key is a KV tombstone: acked and skipped.
		require.Equal(t, substrate.Ack, b.handle(ctx, substrate.Message{Subject: "node.x", Sequence: 7, NumPending: 1}))
		require.Equal(t, uint64(7), b.AppliedSeq())
	})

	t.Run("Term retires", func(t *testing.T) {
		b := &Bootstrapper{ready: make(chan struct{})}
		// A body no decoder can read is discarded outright — never retried, so
		// the index will never reflect it and the floor may pass it.
		require.Equal(t, substrate.Term, b.handle(ctx, substrate.Message{Subject: "node.x", Body: []byte("{not json"), Sequence: 9, NumPending: 1}))
		require.Equal(t, uint64(9), b.AppliedSeq())
	})

	t.Run("Nak owes, and caps the floor under later retirements", func(t *testing.T) {
		if testing.Short() {
			t.Skip("requires NATS JetStream")
		}
		// An adjacency bucket that has gone away under a live handle makes
		// adjacency.Build fail with a plain error, which processMsg Naks — the
		// message is still owed, so the index has not applied it.
		_, nc := natsfixture.Server(t)
		js, err := jetstream.New(nc)
		require.NoError(t, err)
		_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "adj-nak-cursor"})
		require.NoError(t, err)
		conn, err := substrate.Wrap(nc)
		require.NoError(t, err)
		adjKV, err := conn.OpenKV(ctx, "adj-nak-cursor")
		require.NoError(t, err)
		require.NoError(t, js.DeleteKeyValue(ctx, "adj-nak-cursor"))

		b := NewBootstrapper(conn, "core-nak-cursor", adjKV)
		evt := adjacency.CoreKVEvent{CoreKvKey: "core.e1", EdgeID: "e1", Name: "HAS_PARTY", Direction: "outbound", NodeID: "nodeA", OtherNodeID: "nodeB"}
		body, err := json.Marshal(evt)
		require.NoError(t, err)
		require.Equal(t, substrate.Nak, b.handle(ctx, substrate.Message{Subject: "node.e1", Body: body, Sequence: 11, NumPending: 1}))
		require.Equal(t, uint64(0), b.AppliedSeq(),
			"nothing has been retired, so the floor is still the never-measured reading")

		// Everything above it retires; the floor must not follow them past the
		// debt. This is the case a maximum gets wrong: it would answer 13.
		b.retire(12)
		b.retire(13)
		require.Equal(t, uint64(10), b.AppliedSeq(),
			"later retirements must not carry the cursor over a message the index still owes")
	})
}

// TestPollProgress_ReadsTheStreamHeadBeforeTheDrainedCheck pins an ORDER whose
// violation is unobservable from outside: both reads succeed either way, and the
// difference only shows on a commit landing between them — a window no caller
// can drive, since pollProgress owns both calls and exposes neither.
//
// The order is load-bearing. Head-then-check: a message committed in between is
// ABOVE the head this pass raises to, so the cursor is at worst one message
// stale, which refuses more and never less. Check-then-head: the head would
// include a commit the consumer was never asked about, and the cursor would
// claim the index reflects an edge it has never seen — fail-open, and worst at
// startup, which is the one moment this poll is what gives the cursor a value.
//
// Pinned by reading the source, because that is where the fact lives.
func TestPollProgress_ReadsTheStreamHeadBeforeTheDrainedCheck(t *testing.T) {
	src, err := os.ReadFile("bootstrap.go")
	require.NoError(t, err)

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "bootstrap.go", src, 0)
	require.NoError(t, err)

	var body *ast.BlockStmt
	for _, decl := range file.Decls {
		if fn, isFunc := decl.(*ast.FuncDecl); isFunc && fn.Name.Name == "pollProgress" {
			body = fn.Body
		}
	}
	require.NotNil(t, body, "pollProgress must exist for this pin to mean anything")

	headAt, checkAt := -1, -1
	ast.Inspect(body, func(n ast.Node) bool {
		call, isCall := n.(*ast.CallExpr)
		if !isCall {
			return true
		}
		sel, isSel := call.Fun.(*ast.SelectorExpr)
		if !isSel {
			return true
		}
		switch sel.Sel.Name {
		case "StreamLastSequence":
			if headAt < 0 {
				headAt = fset.Position(call.Pos()).Offset
			}
		case "ConsumerCaughtUp":
			if checkAt < 0 {
				checkAt = fset.Position(call.Pos()).Offset
			}
		}
		return true
	})

	require.GreaterOrEqual(t, headAt, 0, "pollProgress must read the stream head")
	require.GreaterOrEqual(t, checkAt, 0, "pollProgress must check that the consumer is drained")
	require.Less(t, headAt, checkAt,
		"pollProgress must read the stream head BEFORE checking that the consumer is drained: "+
			"reading it after would let a message committed between the two reads be claimed as applied")

	// And the head-read error must SKIP the tick, not fall through to it. The
	// first `continue` after the head read has to come before the drained
	// check: past that point the pass would go on to reconcile — retiring to a
	// head nobody read, and clearing the owed set on an observation nobody
	// made, which releases debts the index still owes.
	skipAt := -1
	ast.Inspect(body, func(n ast.Node) bool {
		br, isBranch := n.(*ast.BranchStmt)
		if !isBranch || br.Tok != token.CONTINUE {
			return true
		}
		at := fset.Position(br.Pos()).Offset
		if at > headAt && (skipAt < 0 || at < skipAt) {
			skipAt = at
		}
		return true
	})
	require.GreaterOrEqual(t, skipAt, 0, "pollProgress must be able to skip a tick")
	require.Less(t, skipAt, checkAt,
		"a failed head read must `continue` before the drained check, never fall through to it")
}

// TestPollProgress_ADrainedObservationReleasesAnOrphanedDebt is the standing
// poll's reconciliation, and the state that makes it necessary rather than
// tidy.
//
// A Nak'd sequence is owed until a REDELIVERY retires it, and some are never
// redelivered: the durable is deleted and recreated, the stream is purged, the
// message ages out. The floor then sits one below that orphan for the life of
// the process — every reader consulting the cursor refuses forever — and the
// owed set never shrinks. A drained durable owes nothing by definition, so a
// caught-up observation is the one moment the whole set can be cleared, which
// is both the release and the bound on the map.
func TestPollProgress_ADrainedObservationReleasesAnOrphanedDebt(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS JetStream")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, nc := natsfixture.Server(t)
	js, err := jetstream.New(nc)
	require.NoError(t, err)
	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)

	coreKV, err := js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "core-seq-orphan"})
	require.NoError(t, err)
	_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "adj-seq-orphan"})
	require.NoError(t, err)
	adjKV, err := conn.OpenKV(ctx, "adj-seq-orphan")
	require.NoError(t, err)

	for i := range 4 {
		evt := adjacency.CoreKVEvent{
			CoreKvKey:   fmt.Sprintf("core.o%d", i),
			EdgeID:      fmt.Sprintf("o%d", i),
			Name:        "HAS_PARTY",
			Direction:   "outbound",
			NodeID:      "nodeOrphan",
			OtherNodeID: fmt.Sprintf("other%d", i),
		}
		data, merr := json.Marshal(evt)
		require.NoError(t, merr)
		_, perr := coreKV.Put(ctx, "edge."+evt.EdgeID, data)
		require.NoError(t, perr)
	}
	last, err := conn.StreamLastSequence(ctx, "KV_core-seq-orphan")
	require.NoError(t, err)
	require.Equal(t, uint64(4), last)

	b := NewBootstrapper(conn, "core-seq-orphan", adjKV)
	go func() { _ = b.Run(ctx) }()
	select {
	case <-b.Ready():
	case <-ctx.Done():
		t.Fatal("timed out waiting for Ready")
	}
	require.Eventually(t, func() bool { return b.AppliedSeq() == last }, 20*time.Second, 25*time.Millisecond,
		"the baseline: a drained backlog leaves the cursor at the head")

	// The orphan. The consumer has drained, so NOTHING will be delivered again
	// — a debt recorded now is one no redelivery can ever retire, which is what
	// a Nak against a consumer that is then deleted and recreated leaves
	// behind. It pins the floor two below the head and would do so for the life
	// of the process.
	b.markOwed(2)
	require.Equal(t, uint64(1), b.AppliedSeq(),
		"the control: the debt really does pin the floor below the head")

	// The standing poll's next drained observation is what releases it —
	// which is why the poll cannot stop at Ready. Polled on the effect, never
	// slept through.
	require.Eventually(t, func() bool { return b.AppliedSeq() == last }, 20*time.Second, 25*time.Millisecond,
		"a drained durable owes nothing, so a drained observation must clear the orphaned debt and "+
			"restore the cursor to the head; got %d want %d", b.AppliedSeq(), last)

	b.seqMu.Lock()
	owed := len(b.owed)
	b.seqMu.Unlock()
	require.Zero(t, owed, "and the set is emptied, which is what bounds it")

	// A SECOND orphan, released the same way. One release could be the last act
	// of a poll that then stopped; two cannot. This is what pins the poll as
	// STANDING rather than one-shot — the difference between a cursor that
	// reconciles for the life of the process and one that reconciled once at
	// startup and can never recover again.
	b.markOwed(3)
	require.Equal(t, uint64(2), b.AppliedSeq(), "the control: the second debt pins the floor too")
	require.Eventually(t, func() bool { return b.AppliedSeq() == last }, 20*time.Second, 25*time.Millisecond,
		"the poll must still be running long after Ready; got %d want %d", b.AppliedSeq(), last)
}

// TestPollProgress_AHeadReadErrorReleasesNothingAndSignalsNothing pins the
// poll's posture on the one branch that must never proceed on a bad reading.
//
// A failed head read means the pass HAS no head. Falling through to the drained
// branch would retire to zero and — worse — clear the owed set on the strength
// of an observation nobody made, releasing debts the index still owes. So the
// tick is skipped whole: no cursor move, no release, no signal.
//
// The fixture is a bootstrapper pointed at a stream that does not exist, which
// is what every read against an unreachable one looks like. Its recovery half
// is not asserted here because it cannot be: the same missing stream that fails
// the head read also stops the durable being created, so nothing in this
// fixture can ever reach a drained observation. That Ready is NOT held hostage
// to the head read is proved where it is reachable — by the handler path in
// TestBootstrapper_ReadyAfterProcessingMessages, and by a later good tick in
// TestBootstrapper_AppliedSeq_TracksTheStreamHead's restart case — and the
// control-flow half is pinned in the order test below.
func TestPollProgress_AHeadReadErrorReleasesNothingAndSignalsNothing(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS JetStream")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, nc := natsfixture.Server(t)
	js, err := jetstream.New(nc)
	require.NoError(t, err)
	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)

	_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "adj-seq-headerr"})
	require.NoError(t, err)
	adjKV, err := conn.OpenKV(ctx, "adj-seq-headerr")
	require.NoError(t, err)

	b := NewBootstrapper(conn, "core-seq-headerr", adjKV)
	b.markOwed(2)
	go func() { _ = b.Run(ctx) }()

	// Across many failing ticks: nothing moves. A standing condition polled,
	// never a sleep waiting for one.
	require.Never(t, func() bool {
		b.seqMu.Lock()
		released := len(b.owed) == 0
		b.seqMu.Unlock()
		return b.AppliedSeq() != 0 || released || b.isReady()
	}, 2*time.Second, 25*time.Millisecond,
		"a head read that failed is not a head read: it moves no cursor, releases no debt, and signals nothing")
}
