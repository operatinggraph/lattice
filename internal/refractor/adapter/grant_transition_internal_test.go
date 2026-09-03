package adapter

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// transitionStore is a kvStore fake that scripts exactly what guardedWrite's
// liveness classification reads: whether the key exists, what body it holds,
// and whether the write itself fails. It records the bytes written so a case
// can assert the outgoing body as well as the verdict.
type transitionStore struct {
	stored    []byte
	absent    bool
	getErr    error
	createErr error
	updateErr error

	written []byte
	writes  int
}

func (s *transitionStore) Get(ctx context.Context, key string) (*substrate.KVEntry, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	if s.absent {
		return nil, substrate.ErrKeyNotFound
	}
	return &substrate.KVEntry{Value: s.stored, Revision: 7}, nil
}

func (s *transitionStore) Create(ctx context.Context, key string, value []byte) (uint64, error) {
	s.writes++
	if s.createErr != nil {
		return 0, s.createErr
	}
	s.written = append([]byte(nil), value...)
	return 1, nil
}

func (s *transitionStore) Update(ctx context.Context, key string, value []byte, expectedRevision uint64) (uint64, error) {
	s.writes++
	if s.updateErr != nil {
		return 0, s.updateErr
	}
	s.written = append([]byte(nil), value...)
	return expectedRevision + 1, nil
}

func (s *transitionStore) Put(ctx context.Context, key string, value []byte) (uint64, error) {
	panic("unused by grant-transition tests")
}
func (s *transitionStore) Delete(ctx context.Context, key string) error {
	panic("unused by grant-transition tests")
}
func (s *transitionStore) ListKeys(ctx context.Context) ([]string, error) {
	panic("unused by grant-transition tests")
}
func (s *transitionStore) ListKeysPrefix(ctx context.Context, prefix string) ([]string, error) {
	panic("unused by grant-transition tests")
}
func (s *transitionStore) Purge(ctx context.Context, key string) error {
	panic("unused by grant-transition tests")
}
func (s *transitionStore) Status(ctx context.Context) error {
	panic("unused by grant-transition tests")
}

const liveBody = `{"projectionSeq":1,"anchorId":"Kx3TmZpq7RvwNsY2Hc9L"}`
const tombstoneBody = `{"projectionSeq":1,"isDeleted":true}`

// TestGuardedWrite_LivenessTransitionTable walks every row of the D1
// grant-change design's transition table (§4.2). The rows that matter are the
// three that produce a signal and — far more so — the six that must NOT: a
// consumer that reprojects on a watermark advance costs 15 cypher evaluations
// per swept actor per minute forever, and a consumer that misses a revocation
// keeps honouring a withdrawn grant.
func TestGuardedWrite_LivenessTransitionTable(t *testing.T) {
	cases := []struct {
		name    string
		store   *transitionStore
		row     map[string]any
		seq     uint64
		delete  bool
		verdict guardVerdict
		want    GrantTransition
		writes  int
	}{
		{
			name:    "absent + live = a grant lands",
			store:   &transitionStore{absent: true},
			row:     map[string]any{"anchorId": "Kx3TmZpq7RvwNsY2Hc9L"},
			seq:     5,
			verdict: guardCommitted,
			want:    TransitionGranted,
			writes:  1,
		},
		{
			name:    "live + tombstone = a grant is revoked",
			store:   &transitionStore{stored: []byte(liveBody)},
			seq:     5,
			delete:  true,
			verdict: guardCommitted,
			want:    TransitionRevoked,
			writes:  1,
		},
		{
			name:    "tombstone + live = a re-grant",
			store:   &transitionStore{stored: []byte(tombstoneBody)},
			row:     map[string]any{"anchorId": "Kx3TmZpq7RvwNsY2Hc9L"},
			seq:     5,
			verdict: guardCommitted,
			want:    TransitionGranted,
			writes:  1,
		},
		{
			// The decisive negative: the guarded path rewrites an unchanged
			// body on every evaluation to advance the watermark, and that is
			// what a bucket watcher cannot tell apart from a real flip.
			name:    "live + live = watermark advance only, no transition",
			store:   &transitionStore{stored: []byte(liveBody)},
			row:     map[string]any{"anchorId": "Kx3TmZpq7RvwNsY2Hc9L"},
			seq:     5,
			verdict: guardCommitted,
			want:    TransitionNone,
			writes:  1,
		},
		{
			name:    "tombstone + tombstone = no transition",
			store:   &transitionStore{stored: []byte(tombstoneBody)},
			seq:     5,
			delete:  true,
			verdict: guardCommitted,
			want:    TransitionNone,
			writes:  1,
		},
		{
			// Reachable because deleteRow routes every retraction through the
			// guard whether or not the key exists. Nothing was granted here,
			// so nothing was revoked.
			name:    "absent + tombstone = nothing was ever granted",
			store:   &transitionStore{absent: true},
			seq:     5,
			delete:  true,
			verdict: guardCommitted,
			want:    TransitionNone,
			writes:  1,
		},
		{
			name:    "declined by watermark = no write, no transition",
			store:   &transitionStore{stored: []byte(liveBody)},
			row:     map[string]any{"anchorId": "Kx3TmZpq7RvwNsY2Hc9L"},
			seq:     1, // ties the stored projectionSeq
			verdict: guardDeclinedByWatermark,
			want:    TransitionNone,
			writes:  0,
		},
		{
			// Returns before any Get, so there is no stored body to compare —
			// "unknown", never "nothing changed".
			name:    "sequence-less drop = unknown, not none",
			store:   &transitionStore{stored: []byte(liveBody)},
			row:     map[string]any{"anchorId": "Kx3TmZpq7RvwNsY2Hc9L"},
			seq:     0,
			verdict: guardDroppedNoToken,
			want:    TransitionUnknown,
			writes:  0,
		},
		{
			// An unparseable stored body cannot prove that nothing changed, so
			// it classifies in whichever direction the incoming body points.
			// One-sided on purpose: an extra reprojection costs CPU, a missed
			// revocation costs a live grant.
			name:    "unparseable stored body + tombstone = revoked, never silently none",
			store:   &transitionStore{stored: []byte(`not json`)},
			seq:     5,
			delete:  true,
			verdict: guardCommitted,
			want:    TransitionRevoked,
			writes:  1,
		},
		{
			name:    "unparseable stored body + live = granted, never silently none",
			store:   &transitionStore{stored: []byte(`not json`)},
			row:     map[string]any{"anchorId": "Kx3TmZpq7RvwNsY2Hc9L"},
			seq:     5,
			verdict: guardCommitted,
			want:    TransitionGranted,
			writes:  1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := &NatsKVAdapter{kv: tc.store, keyOrder: []string{"key"}, guarded: true}

			out, err := a.guardedWrite(context.Background(), "cap-read.dom.identity.A.B", tc.row, tc.seq, tc.delete)

			require.NoError(t, err)
			assert.Equal(t, tc.verdict, out.verdict, "watermark verdict")
			assert.Equal(t, tc.want, out.transition, "liveness transition")
			assert.Equal(t, tc.writes, tc.store.writes, "store writes attempted")
		})
	}
}

// TestGuardedWrite_ErrorExitsClassifyAsNoTransition covers the five error
// returns. Classification is err-first for a reason worth restating: the error
// exits all report the zero guardVerdict (guardCommitted), so a caller reading
// the transition without checking the error first would see every failure as a
// committed non-transition — and a failed revocation reported as "nothing
// changed" is the over-grant direction.
func TestGuardedWrite_ErrorExitsClassifyAsNoTransition(t *testing.T) {
	boom := errors.New("boom")
	cases := []struct {
		name  string
		store *transitionStore
		row   map[string]any
		want  string
	}{
		{
			name:  "marshal of the outgoing body fails",
			store: &transitionStore{absent: true},
			row:   map[string]any{"v": make(chan int)},
			want:  "marshal",
		},
		{
			name:  "the stored-entry read fails for a reason other than absence",
			store: &transitionStore{getErr: boom},
			want:  "get",
		},
		{
			name:  "the create fails for a reason other than a revision conflict",
			store: &transitionStore{absent: true, createErr: boom},
			want:  "create",
		},
		{
			name:  "the update fails for a reason other than a revision conflict",
			store: &transitionStore{stored: []byte(liveBody), updateErr: boom},
			want:  "update",
		},
		{
			name:  "the CAS loop exhausts its attempt budget",
			store: &transitionStore{absent: true, createErr: substrate.ErrRevisionConflict},
			want:  "revision conflict not resolved",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := &NatsKVAdapter{kv: tc.store, keyOrder: []string{"key"}, guarded: true}
			row := tc.row
			if row == nil {
				row = map[string]any{"anchorId": "Kx3TmZpq7RvwNsY2Hc9L"}
			}

			out, err := a.guardedWrite(context.Background(), "cap-read.dom.identity.A.B", row, 5, false)

			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
			assert.Equal(t, TransitionNone, out.transition,
				"an error exit must never report a transition")
		})
	}
}

// TestGuardedOutcomes_CarryKeyAndTransition pins the two fields the write path
// routes onward: the key the adapter itself rendered (never re-derived at the
// call site) and the transition, which must survive the trip through
// UpsertOutcome/DeleteOutcome independently of Wrote.
func TestGuardedOutcomes_CarryKeyAndTransition(t *testing.T) {
	t.Run("upsert reports the granted transition beside an unconditional Wrote", func(t *testing.T) {
		store := &transitionStore{absent: true}
		// readGrantWriter: these cases model a real cap-read PRODUCER — the
		// key they render is a D1 grant key — so the fixture has to carry the
		// licence the installer grants such a lens. Without it the namespace
		// guard refuses the write before any transition is derived, which is
		// the correct answer for a lens that is NOT a producer and the wrong
		// fixture for one that is.
		a := &NatsKVAdapter{kv: store, keyOrder: []string{"key"}, guarded: true, readGrantWriter: true}

		out, err := a.upsert(context.Background(),
			map[string]any{"key": "cap-read.dom.identity.A.B"}, map[string]any{"v": 1}, 5)

		require.NoError(t, err)
		assert.True(t, out.Wrote, "the guarded path claims Wrote unconditionally")
		assert.Equal(t, "cap-read.dom.identity.A.B", out.Key)
		assert.Equal(t, TransitionGranted, out.Transition)
	})

	t.Run("a watermark-declined upsert still claims Wrote but reports no transition", func(t *testing.T) {
		store := &transitionStore{stored: []byte(liveBody)}
		// readGrantWriter: these cases model a real cap-read PRODUCER — the
		// key they render is a D1 grant key — so the fixture has to carry the
		// licence the installer grants such a lens. Without it the namespace
		// guard refuses the write before any transition is derived, which is
		// the correct answer for a lens that is NOT a producer and the wrong
		// fixture for one that is.
		a := &NatsKVAdapter{kv: store, keyOrder: []string{"key"}, guarded: true, readGrantWriter: true}

		out, err := a.upsert(context.Background(),
			map[string]any{"key": "cap-read.dom.identity.A.B"}, map[string]any{"v": 1}, 1)

		require.NoError(t, err)
		assert.True(t, out.Wrote, "Wrote is not the transition and must not be read as one")
		assert.True(t, out.DeclinedByWatermark)
		assert.Equal(t, TransitionNone, out.Transition)
	})

	t.Run("delete reports the revoked transition", func(t *testing.T) {
		store := &transitionStore{stored: []byte(liveBody)}
		// readGrantWriter: these cases model a real cap-read PRODUCER — the
		// key they render is a D1 grant key — so the fixture has to carry the
		// licence the installer grants such a lens. Without it the namespace
		// guard refuses the write before any transition is derived, which is
		// the correct answer for a lens that is NOT a producer and the wrong
		// fixture for one that is.
		a := &NatsKVAdapter{kv: store, keyOrder: []string{"key"}, guarded: true, readGrantWriter: true}

		out, err := a.deleteRow(context.Background(),
			map[string]any{"key": "cap-read.dom.identity.A.B"}, 5)

		require.NoError(t, err)
		assert.True(t, out.Wrote)
		assert.Equal(t, "cap-read.dom.identity.A.B", out.Key)
		assert.Equal(t, TransitionRevoked, out.Transition)
	})

	t.Run("an unguarded adapter never claims a transition", func(t *testing.T) {
		store := &getErrStore{getErr: errors.New("no identity probe")}
		a := &NatsKVAdapter{kv: store, keyOrder: []string{"key"}}

		out, err := a.upsert(context.Background(),
			map[string]any{"key": "biz.row"}, map[string]any{"v": 1}, 0)

		require.NoError(t, err)
		assert.Equal(t, "biz.row", out.Key)
		assert.Equal(t, TransitionNone, out.Transition,
			"an unguarded adapter reads no stored liveness, so it can claim none")
	})
}
