package pipeline

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/adapter"
)

// recordingGrantSink captures the actors a pipeline announced grant changes
// for, and the entry token each announcement carried — the token is what scopes
// the consumer's publish, so a sink that recorded only the actor could not tell
// a scoped announcement from a whole-actor one.
type recordingGrantSink struct {
	actors  []string
	entries []string
}

func (s *recordingGrantSink) GrantChanged(actorKey, entryID string) {
	s.actors = append(s.actors, actorKey)
	s.entries = append(s.entries, entryID)
}

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
// cap-read.<actorType>.<actorId>.<anchorId> -> (vtx.<actorType>.<actorId>,
// <anchorId>) — the same two halves OutputDescriptor.AnchorEntryFromKey
// recovers in production.
func capReadAnchorFromKey(targetKey string) (actorKey, entryID string, ok bool) {
	const prefix = "cap-read."
	if len(targetKey) <= len(prefix) || targetKey[:len(prefix)] != prefix {
		return "", "", false
	}
	rest := targetKey[len(prefix):]
	// <type>.<id>.<anchor> — split the trailing entry token off.
	last := -1
	for i := len(rest) - 1; i >= 0; i-- {
		if rest[i] == '.' {
			last = i
			break
		}
	}
	if last < 0 {
		return "", "", false
	}
	return "vtx." + rest[:last], rest[last+1:], true
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

// TestGrantChangeAnnouncement_CarriesTheAnchorTheKeyNames pins the producer
// half of the delta publication's grant scope
// (personal-lens-delta-publication-design.md §4.3).
//
// The token this hands the sink is the whole difference between republishing
// one anchor's row and republishing the actor's entire set. It is asserted
// against the KEY's own trailing segment rather than against a constant, and
// rather than merely being non-empty: a producer that passed some other NanoID
// would scope the consumer's publish to a row the grant never touched, and the
// device would keep a stale one with nothing to say so.
func TestGrantChangeAnnouncement_CarriesTheAnchorTheKeyNames(t *testing.T) {
	t.Run("a per-entry key announces its trailing anchor", func(t *testing.T) {
		keys := []string{
			"cap-read.identity.Hj4kPmRtw9nbCxz5vQ2y.Zwq9PmRtw3nbCxz5vQ2y",
			"cap-read.identity.Hj4kPmRtw9nbCxz5vQ2y.Ywq9PmRtw3nbCxz5vQ2x",
			"cap-read.identity.Kx3TmZpq7RvwNsY2Hc9L.Nb7RvwKx3TmZpq2Hc9Ls",
		}
		adpt := &purgingTruncater{failAfter: -1, keys: keys}
		sink := &recordingGrantSink{}
		p := &Pipeline{ruleID: "cap-read-producer"}
		p.SetGrantChangeSink(sink, capReadAnchorFromKey)

		require.NoError(t, p.truncateTarget(context.Background(), adpt))

		want := make([]string, 0, len(keys))
		for _, k := range keys {
			want = append(want, k[strings.LastIndexByte(k, '.')+1:])
		}
		assert.Equal(t, want, sink.entries,
			"each announcement names the anchor its own key names, in the order the keys were purged")
	})

	t.Run("a lens whose keys name no anchor announces an empty token", func(t *testing.T) {
		// The doc-mode shape: one key per actor, so the inverse recovers an
		// actor and no anchor. The consumer reads the empty token as "the whole
		// actor moved", which is the only correct reading of a key that names
		// no single anchor.
		adpt := &purgingTruncater{failAfter: -1, keys: []string{"cap.roles.identity.Hj4kPmRtw9nbCxz5vQ2y"}}
		sink := &recordingGrantSink{}
		p := &Pipeline{ruleID: "doc-mode-producer"}
		p.SetGrantChangeSink(sink, func(targetKey string) (string, string, bool) {
			rest, ok := strings.CutPrefix(targetKey, "cap.roles.")
			if !ok {
				return "", "", false
			}
			return "vtx." + rest, "", true
		})

		require.NoError(t, p.truncateTarget(context.Background(), adpt))

		require.Equal(t, []string{"vtx.identity.Hj4kPmRtw9nbCxz5vQ2y"}, sink.actors)
		assert.Equal(t, []string{""}, sink.entries)
	})

	t.Run("an actor-level announcement names no anchor", func(t *testing.T) {
		// notifyActorGrantChange's caller holds the actor and no key at all —
		// an out-of-band shred against a target that derives no per-key
		// liveness — so it can name no anchor and must not invent one.
		sink := &recordingGrantSink{}
		p := &Pipeline{ruleID: "shredding-producer"}
		p.SetGrantChangeSink(sink, capReadAnchorFromKey)

		p.notifyActorGrantChange("vtx.identity.Hj4kPmRtw9nbCxz5vQ2y")

		require.Equal(t, []string{"vtx.identity.Hj4kPmRtw9nbCxz5vQ2y"}, sink.actors)
		assert.Equal(t, []string{""}, sink.entries,
			"a coarse announcement must widen the consumer's publish, never scope it to a guess")
	})
}
