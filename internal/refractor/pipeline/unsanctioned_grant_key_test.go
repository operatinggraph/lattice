package pipeline

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/adapter"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// TestPipeline_BindsTheUnsanctionedKeyReporterToEveryAdapterItTakes pins the
// binding that a hot reload would otherwise drop.
//
// An INTO-only reload builds a FRESH adapter and swaps it in through
// HotReloadInto. An adapter that arrived without its reporter would refuse
// writes into a log line with no health fault behind it — invisible on exactly
// the lens an operator would be looking at. The pipeline is the only object
// spanning construction and the swap, so it owns the binding.
func TestPipeline_BindsTheUnsanctionedKeyReporterToEveryAdapterItTakes(t *testing.T) {
	ctx := context.Background()
	first := newMultiEntryTargetAdapter(t)
	p, err := New("lens-unsanctioned", "nats_kv", "core-kv", nil, nil, first, nil)
	require.NoError(t, err)

	// Construction bound it: an unlicensed write reaches the pipeline's own
	// dedup rather than dropping on the floor.
	require.True(t, first.HasUnsanctionedGrantKeyReporter(),
		"pipeline.New must bind the reporter to the adapter it is constructed with")
	require.ErrorIs(t, first.Upsert(ctx, map[string]any{"key": "cap-read.x.identity.Hj4kPmRtw9nbCxz5vQ2y.Kx3TmZpq7RvwNsY2Hc9L"},
		map[string]any{"v": 1}, 1), adapter.ErrUnsanctionedReadGrantKey)

	// And so does the replacement an INTO-only reload swaps in — the case that
	// is easy to lose, because the reload path never touches the pipeline's
	// constructor.
	replacement := newMultiEntryTargetAdapter(t)
	require.False(t, replacement.ReadGrantWriter(), "a freshly built adapter is unlicensed until the rule licenses it")
	require.False(t, replacement.HasUnsanctionedGrantKeyReporter(), "sanity: it arrives unbound")
	require.NoError(t, p.HotReloadInto(replacement))
	require.True(t, replacement.HasUnsanctionedGrantKeyReporter(),
		"HotReloadInto must bind the reporter to the replacement, or a reloaded lens refuses writes with no health fault behind them")
	require.ErrorIs(t, replacement.Upsert(ctx, map[string]any{"key": "cap-read.x.identity.Hj4kPmRtw9nbCxz5vQ2y.Kx3TmZpq7RvwNsY2Hc9L"},
		map[string]any{"v": 1}, 1), adapter.ErrUnsanctionedReadGrantKey)
}

// TestWriteResults_UnsanctionedKeyIsTerminalAheadOfFailClosed pins the ordering
// decision.
//
// FailClosed exists so a retraction that did not take effect cannot be masked
// by a sibling's fresh upsert landing: the batch is redelivered instead. That
// reasoning assumes the failure MIGHT NOT recur. The namespace refusal always
// does — the lens's own declaration renders the same key on every redelivery —
// so a perEntry retraction carrying FailClosed would Nak the lens into a
// permanent redelivery loop against a misconfiguration no retry can fix.
//
// Nothing is masked by acking instead: the guard refuses that lens's writes in
// BOTH directions, so there is no sibling upsert to land ahead of the retraction
// it refused. The sibling here proves exactly that — it is refused too.
func TestWriteResults_UnsanctionedKeyIsTerminalAheadOfFailClosed(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}
	ctx := context.Background()
	adpt := newMultiEntryTargetAdapter(t)
	require.False(t, adpt.ReadGrantWriter(), "the fixture models a lens the installer never licensed")

	p := &Pipeline{ruleID: "rule-unsanctioned-failclosed", adpt: adpt}

	const grantKey = "cap-read.billing.identity.Hj4kPmRtw9nbCxz5vQ2y.Kx3TmZpq7RvwNsY2Hc9L"
	results := []ruleengine.EvalResult{
		{Delete: true, Keys: map[string]any{"key": grantKey}, FailClosed: true},
		{Keys: map[string]any{"key": grantKey + ".sibling"}, Row: map[string]any{"v": 1}},
	}
	decision, err := p.writeResults(ctx, p.ruleState(), substrate.Message{Sequence: 42}, "vtx.identity.Hj4kPmRtw9nbCxz5vQ2y", results, nil)

	require.Equal(t, substrate.Ack, decision,
		"a FailClosed retraction refused by the namespace guard must ACK: redelivering it spins the lens forever against a rule redelivery cannot change")
	require.NoError(t, err,
		"and it must not surface as a redelivery reason")

	// The sibling upsert the FailClosed rule exists to hold back was refused by
	// the same guard, which is why acking masks nothing.
	_, live, gerr := adpt.GetRow(ctx, map[string]any{"key": grantKey + ".sibling"})
	require.NoError(t, gerr)
	require.False(t, live, "the sibling write into the same namespace is refused too — there is nothing for the ack to mask")
}
