package pipeline

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/adapter"
)

// recordingGrantSink captures the actors a pipeline announced grant changes for.
type recordingGrantSink struct{ actors []string }

func (s *recordingGrantSink) GrantChanged(actorKey string) { s.actors = append(s.actors, actorKey) }

// purgingTruncater is an OutcomeTruncater over a fixed key list, standing in
// for the real NatsKVAdapter's prefix-scoped purge.
type purgingTruncater struct {
	keys      []string
	failAfter int // -1 disables; otherwise fail once this many keys are purged
	purged    []string
}

func (a *purgingTruncater) Upsert(context.Context, map[string]any, map[string]any, uint64) error {
	return nil
}
func (a *purgingTruncater) Delete(context.Context, map[string]any, uint64) error { return nil }
func (a *purgingTruncater) Probe(context.Context) error                          { return nil }
func (a *purgingTruncater) Close() error                                         { return nil }
func (a *purgingTruncater) Truncate(ctx context.Context) error {
	_, err := a.TruncateWithKeys(ctx)
	return err
}

func (a *purgingTruncater) TruncateWithKeys(context.Context) ([]string, error) {
	for i, k := range a.keys {
		if a.failAfter >= 0 && i >= a.failAfter {
			return a.purged, errors.New("injected: purge failed partway")
		}
		a.purged = append(a.purged, k)
	}
	return a.purged, nil
}

// plainTruncater implements only Truncater — the pre-existing shape, kept to
// prove the new arm degrades rather than refusing.
type plainTruncater struct{ called bool }

func (a *plainTruncater) Upsert(context.Context, map[string]any, map[string]any, uint64) error {
	return nil
}
func (a *plainTruncater) Delete(context.Context, map[string]any, uint64) error { return nil }
func (a *plainTruncater) Probe(context.Context) error                          { return nil }
func (a *plainTruncater) Close() error                                         { return nil }
func (a *plainTruncater) Truncate(context.Context) error                       { a.called = true; return nil }

// capReadAnchorFromKey inverts the shipped cap-read per-entry key shape:
// cap-read.<actorType>.<actorId>.<anchorId> -> vtx.<actorType>.<actorId>.
func capReadAnchorFromKey(targetKey string) (string, bool) {
	const prefix = "cap-read."
	if len(targetKey) <= len(prefix) || targetKey[:len(prefix)] != prefix {
		return "", false
	}
	rest := targetKey[len(prefix):]
	// <type>.<id>.<anchor> — strip the trailing entry token.
	last := -1
	for i := len(rest) - 1; i >= 0; i-- {
		if rest[i] == '.' {
			last = i
			break
		}
	}
	if last < 0 {
		return "", false
	}
	return "vtx." + rest[:last], true
}

// TestTruncateTarget_EnqueuesEveryPurgedActor is T8 of
// personal-lens-grant-change-trigger-design.md §10.
//
// Truncate is the one write path that never reaches the per-key guard: it lists
// its keys and purges them directly, so no GrantTransition exists for any row it
// clears. Without this arm the edge would go silent on the single operation that
// revokes the most at once.
//
// The path is not operator-only. A rebuild(truncate=true) takes it, and so does
// a MATCH hot-reload that NARROWS a producer's own cypher: cmd/refractor's
// matchShrank arm sets taxRebuildTruncate, the scheduler drives
// rebuild(truncate=true), and rebuild's truncate branch is the SOLE call site of
// truncateTarget — so both triggers arrive here and neither can bypass it.
func TestTruncateTarget_EnqueuesEveryPurgedActor(t *testing.T) {
	actorA := "vtx.identity.Hj4kPmRtw9nbCxz5vQ2y"
	actorB := "vtx.identity.Kx3TmZpq7RvwNsY2Hc9L"

	t.Run("every purged key's actor is announced", func(t *testing.T) {
		adpt := &purgingTruncater{failAfter: -1, keys: []string{
			"cap-read.identity.Hj4kPmRtw9nbCxz5vQ2y.Zwq9PmRtw3nbCxz5vQ2y",
			"cap-read.identity.Hj4kPmRtw9nbCxz5vQ2y.Ywq9PmRtw3nbCxz5vQ2x",
			"cap-read.identity.Kx3TmZpq7RvwNsY2Hc9L.Zwq9PmRtw3nbCxz5vQ2y",
		}}
		sink := &recordingGrantSink{}
		p := &Pipeline{ruleID: "cap-read-producer"}
		p.SetGrantChangeSink(sink, capReadAnchorFromKey)

		require.NoError(t, p.truncateTarget(context.Background(), adpt))

		assert.Equal(t, []string{actorA, actorA, actorB}, sink.actors,
			"a bulk purge is a bulk revocation; every actor it touched is owed a re-evaluation")
	})

	t.Run("keys purged before a mid-way failure are still announced", func(t *testing.T) {
		adpt := &purgingTruncater{failAfter: 1, keys: []string{
			"cap-read.identity.Hj4kPmRtw9nbCxz5vQ2y.Zwq9PmRtw3nbCxz5vQ2y",
			"cap-read.identity.Kx3TmZpq7RvwNsY2Hc9L.Zwq9PmRtw3nbCxz5vQ2y",
		}}
		sink := &recordingGrantSink{}
		p := &Pipeline{ruleID: "cap-read-producer"}
		p.SetGrantChangeSink(sink, capReadAnchorFromKey)

		err := p.truncateTarget(context.Background(), adpt)

		require.Error(t, err)
		assert.Equal(t, []string{actorA}, sink.actors,
			"those rows are gone whatever the error says; a retraction nobody hears about is the over-grant direction")
	})

	t.Run("a key this lens does not own announces nothing", func(t *testing.T) {
		adpt := &purgingTruncater{failAfter: -1, keys: []string{"cap.roles.identity.Hj4kPmRtw9nbCxz5vQ2y"}}
		sink := &recordingGrantSink{}
		p := &Pipeline{ruleID: "cap-read-producer"}
		p.SetGrantChangeSink(sink, capReadAnchorFromKey)

		require.NoError(t, p.truncateTarget(context.Background(), adpt))

		assert.Empty(t, sink.actors, "the inversion is fail-closed: a key it cannot claim produces no signal")
	})

	t.Run("a lens with no edge truncates through the plain path", func(t *testing.T) {
		adpt := &plainTruncater{}
		p := &Pipeline{ruleID: "ordinary-lens"}

		require.NoError(t, p.truncateTarget(context.Background(), adpt))

		assert.True(t, adpt.called, "an unwired lens keeps the behavior it had")
	})

	t.Run("an adapter that cannot truncate is skipped, not an error", func(t *testing.T) {
		p := &Pipeline{ruleID: "no-truncater"}
		require.NoError(t, p.truncateTarget(context.Background(), noFrameTarget{}))
	})
}

// TestNatsKVAdapter_SatisfiesOutcomeTruncater pins that the shipped adapter
// takes the announcing arm above rather than the plain fallback — the arm is
// selected by a type assertion, so a lost interface would silently degrade
// every truncating rebuild back to no signal.
func TestNatsKVAdapter_SatisfiesOutcomeTruncater(t *testing.T) {
	var a any = &adapter.NatsKVAdapter{}
	_, ok := a.(adapter.OutcomeTruncater)
	assert.True(t, ok, "NatsKVAdapter must report the keys its truncate purged")
}
