package substrate

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

// sizedReader is a multi-get that refuses any request over maxItems with the
// over-size signature, standing in for the response-byte ceiling a caller
// cannot predict from a subject count: the refusal is deterministic, so
// retrying the same request never helps and only a smaller one can succeed.
type sizedReader struct {
	maxItems int
	requests []int
	fail     error
}

func (r *sizedReader) read(_ context.Context, items []string) (map[string]*KVEntry, error) {
	r.requests = append(r.requests, len(items))
	if r.fail != nil {
		return nil, r.fail
	}
	if len(items) > r.maxItems {
		return nil, fmt.Errorf("substrate: KV get-multi b: %d attempts: %w: short read",
			len(items), ErrDirectGetAttemptsExhausted)
	}
	entries := make(map[string]*KVEntry, len(items))
	for _, item := range items {
		entries[item] = &KVEntry{Key: item, Revision: 1}
	}
	return entries, nil
}

func items(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("k%03d", i)
	}
	return out
}

// collectVisited records every item ChunkedMultiGet handed back, so a test can
// assert nothing was lost or duplicated by the splitting.
func collectVisited(seen *[]string) func([]string, map[string]*KVEntry) error {
	return func(got []string, entries map[string]*KVEntry) error {
		for _, item := range got {
			if entries[item] == nil {
				return fmt.Errorf("item %q missing from its own request's entries", item)
			}
		}
		*seen = append(*seen, got...)
		return nil
	}
}

// TestChunkedMultiGet_ChunksByCount pins the ordinary path: requests of at most
// chunk items, in order, covering every item exactly once.
func TestChunkedMultiGet_ChunksByCount(t *testing.T) {
	for _, n := range []int{1, 2, 7, 8, 9, 24} {
		reader := &sizedReader{maxItems: 1 << 20}
		var seen []string
		require.NoError(t, ChunkedMultiGet(context.Background(), items(n), 8, 2, reader.read, collectVisited(&seen)))
		require.Equal(t, items(n), seen, "n=%d", n)

		want := make([]int, 0, 4)
		for left := n; left > 0; left -= 8 {
			if left < 8 {
				want = append(want, left)
				break
			}
			want = append(want, 8)
		}
		require.Equal(t, want, reader.requests, "n=%d", n)
	}
	require.NoError(t, ChunkedMultiGet(context.Background(), nil, 8, 2,
		func(context.Context, []string) (map[string]*KVEntry, error) {
			t.Fatal("an empty item set must read nothing")
			return nil, nil
		}, nil))
}

// TestChunkedMultiGet_SplitsAFailingRequest pins the adaptive half: a request
// the reader refuses is halved and each half retried, and the sequence of
// request sizes is exactly the binary descent — never a repeat of the size that
// just failed, since the ceiling is deterministic.
func TestChunkedMultiGet_SplitsAFailingRequest(t *testing.T) {
	reader := &sizedReader{maxItems: 2}
	var seen []string
	require.NoError(t, ChunkedMultiGet(context.Background(), items(8), 8, 1, reader.read, collectVisited(&seen)))

	require.Equal(t, items(8), seen, "every item must still land, in order")
	require.Equal(t, []int{8, 4, 2, 2, 4, 2, 2}, reader.requests,
		"one refused 8, then each half, then the halves that fit")
}

// TestChunkedMultiGet_FailureAtTheFloorPropagates pins the bound on splitting:
// an over-size failure that persists at the floor is the caller's error, not
// another split.
func TestChunkedMultiGet_FailureAtTheFloorPropagates(t *testing.T) {
	reader := &sizedReader{maxItems: 1}
	var seen []string
	err := ChunkedMultiGet(context.Background(), items(8), 8, 2, reader.read, collectVisited(&seen))

	require.ErrorIs(t, err, ErrDirectGetAttemptsExhausted)
	require.Empty(t, seen, "nothing is visited when nothing was read")
	require.Equal(t, []int{8, 4, 2}, reader.requests,
		"the descent stops at the floor and the first floor-sized failure is the answer")
}

// TestChunkedMultiGet_ANonSizeFailurePropagatesWithoutSplitting pins the gate on
// the descent. Only the over-size signature means "try a smaller one"; a
// stalled or refused read fails at every size, so splitting it would multiply
// one caller's wait by the depth of the descent — on a deadline-less context,
// seven timeouts where there should be one.
func TestChunkedMultiGet_ANonSizeFailurePropagatesWithoutSplitting(t *testing.T) {
	transport := errors.New("substrate: connection lost")
	reader := &sizedReader{maxItems: 1 << 20, fail: transport}
	var seen []string
	err := ChunkedMultiGet(context.Background(), items(1024), 1024, 16, reader.read, collectVisited(&seen))

	require.ErrorIs(t, err, transport)
	require.NotErrorIs(t, err, ErrDirectGetAttemptsExhausted)
	require.Empty(t, seen)
	require.Equal(t, []int{1024}, reader.requests,
		"one request, one failure — never a descent for an error no size fixes")
}

// TestChunkedMultiGet_CancelledContextStopsSplitting pins the short circuit:
// once the deadline is gone no smaller request can succeed, so the failure is
// returned rather than halved.
func TestChunkedMultiGet_CancelledContextStopsSplitting(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	reader := &sizedReader{maxItems: 2}
	err := ChunkedMultiGet(ctx, items(8), 8, 1, reader.read, collectVisited(new([]string)))

	require.Error(t, err)
	require.Equal(t, []int{8}, reader.requests, "a cancelled read is not retried smaller")
}

// TestChunkedMultiGet_AnItemMayBeSeveralSubjects pins the unit of splitting: an
// item is expanded into subjects by the READ, so a caller whose unit is a pair
// of keys that must be read together never has that pair split across two
// requests.
func TestChunkedMultiGet_AnItemMayBeSeveralSubjects(t *testing.T) {
	var requests [][]string
	read := func(_ context.Context, got []string) (map[string]*KVEntry, error) {
		subjects := make([]string, 0, 2*len(got))
		entries := make(map[string]*KVEntry, 2*len(got))
		for _, item := range got {
			for _, subject := range []string{"doc." + item, "mark." + item} {
				subjects = append(subjects, subject)
				entries[subject] = &KVEntry{Key: subject, Revision: 1}
			}
		}
		requests = append(requests, subjects)
		if len(subjects) > 4 {
			return nil, fmt.Errorf("substrate: KV get-multi b: %w", ErrDirectGetAttemptsExhausted)
		}
		return entries, nil
	}
	paired := func(got []string, entries map[string]*KVEntry) error {
		for _, item := range got {
			require.Contains(t, entries, "doc."+item)
			require.Contains(t, entries, "mark."+item)
		}
		return nil
	}
	require.NoError(t, ChunkedMultiGet(context.Background(), items(4), 4, 1, read, paired))
	for _, subjects := range requests {
		require.Zero(t, len(subjects)%2, "a pair is never torn across a request boundary")
	}
}
