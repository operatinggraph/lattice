package loom

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// pinListingPattern is a one-step pattern whose completion domain is its
// subjectType, so two instances pinning two of these contribute two distinct
// domains to the reconcile union.
func pinListingPattern(subjectType string) *Pattern {
	return &Pattern{
		PatternID:   "pin-listing-" + subjectType,
		SubjectType: subjectType,
		Steps:       []Step{{Kind: StepKindSystemOp, Operation: "StepA"}},
	}
}

// recordingLister implements instanceLister over a real connection, recording
// every filter set handed to the complete resolution. It is how the two
// enumerating reads' server-side selection is asserted: the filters are the only
// place a widened or mis-narrowed enumeration shows, since both paths re-check or
// re-derive the key shape client-side and so return the right answer either way.
type recordingLister struct {
	inner    instanceLister
	resolved [][]string
}

func (r *recordingLister) KVGetMultiNoSnapshot(ctx context.Context, bucket string, keys []string) (map[string]*substrate.KVEntry, error) {
	r.resolved = append(r.resolved, append([]string(nil), keys...))
	return r.inner.KVGetMultiNoSnapshot(ctx, bucket, keys)
}

func (r *recordingLister) KVGetMulti(ctx context.Context, bucket string, keys []string) (map[string]*substrate.KVEntry, error) {
	return r.inner.KVGetMulti(ctx, bucket, keys)
}

// failingLister implements instanceLister by failing the resolution, for the
// fail-closed posture of both enumerating reads.
type failingLister struct {
	inner instanceLister
	err   error
}

func (f failingLister) KVGetMultiNoSnapshot(_ context.Context, _ string, _ []string) (map[string]*substrate.KVEntry, error) {
	return nil, f.err
}

func (f failingLister) KVGetMulti(ctx context.Context, bucket string, keys []string) (map[string]*substrate.KVEntry, error) {
	return f.inner.KVGetMulti(ctx, bucket, keys)
}

func bufferLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// TestPinListings_PinsAndCursorsCoexist asserts the two pin-family reads on a
// bucket that holds every neighbouring shape at once — three cursors, two live
// pins, one failed-index marker — and pins the SELECTION each one hands the
// server, which is the only observable difference between a family-scoped
// resolution and a wider one (both re-check the key shape, so both would return
// the same domains and the same count).
func TestPinListings_PinsAndCursorsCoexist(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	conn, ctx := newControlTestConn(t)
	s := newStateStore(conn, "loom-state")
	rec := &recordingLister{inner: conn}
	s.lister = rec
	r := &runningInstanceCounter{conn: conn, bucket: "loom-state", interval: defaultHeartbeatEvery}

	live := newCounterTestInstance(t)
	require.NoError(t, s.createInstance(ctx, live, pinListingPattern("widget")))

	poisoned := newCounterTestInstance(t)
	require.NoError(t, s.createInstance(ctx, poisoned, pinListingPattern("gadget")))
	// Corrupt that instance's cursor BODY, leaving its pin intact — the shape a
	// poisoned cursor really has in the bucket. Neither pin read may touch it.
	_, err := conn.KVPut(ctx, "loom-state", instanceKey(poisoned.InstanceID), []byte("{not json"))
	require.NoError(t, err)

	// A third instance driven to a real terminal: its pin is purged in the same
	// AtomicBatch production takes, and its cursor and failed index stay.
	terminal := newCounterTestInstance(t)
	require.NoError(t, s.createInstance(ctx, terminal, pinListingPattern("sprocket")))
	terminal.Status = StatusFailed
	require.NoError(t, s.transition(ctx, terminal, "", "", tokenCreateOnly, nil, 0, 0))

	// Precondition: every shape really is in the bucket, so "sees pins only" is a
	// claim about a discriminating read rather than about an empty keyspace.
	keys, err := conn.KVListKeys(ctx, "loom-state")
	require.NoError(t, err)
	var cursors, pins, failedMarkers int
	for _, k := range keys {
		switch {
		case isPatternPinKey(k):
			pins++
		case instanceIDFromSubKey(k, failedMarkerSuffix) != "":
			failedMarkers++
		case isInstanceRecordKey(k):
			cursors++
		}
	}
	require.Equal(t, 3, cursors, "precondition: three cursors stand in the bucket")
	require.Equal(t, 2, pins, "precondition: only the two live instances are pinned")
	require.Equal(t, 1, failedMarkers, "precondition: the failed instance is indexed")

	got, err := r.count(ctx)
	require.NoError(t, err)
	require.Equal(t, 2, got, "the counter sees the two pins, neither cursor nor failed index")

	var logged bytes.Buffer
	domains, err := s.pinnedDomains(ctx, bufferLogger(&logged))
	require.NoError(t, err)
	require.Equal(t, map[string]struct{}{"widget": {}, "gadget": {}}, domains,
		"the union is the two live pins' domains; the terminal instance's domain has drained")
	require.False(t, strings.Contains(logged.String(), "unparseable"),
		"the pin read must never deliver a cursor body to the decode: %s", logged.String())

	require.Equal(t, [][]string{{patternPinFilter}}, rec.resolved,
		"pinnedDomains resolves the pin family and nothing else, in one request")
}

// TestPinnedDomains_UnparseablePinSkippedAndTheRestReturned pins the asymmetric
// error posture's tolerant half: one poisoned pin is logged and excluded, and the
// other live instance's domain still reaches the reconcile union.
func TestPinnedDomains_UnparseablePinSkippedAndTheRestReturned(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	conn, ctx := newControlTestConn(t)
	s := newStateStore(conn, "loom-state")

	good := newCounterTestInstance(t)
	require.NoError(t, s.createInstance(ctx, good, pinListingPattern("widget")))
	bad := newCounterTestInstance(t)
	require.NoError(t, s.createInstance(ctx, bad, pinListingPattern("gadget")))
	_, err := conn.KVPut(ctx, "loom-state", patternPinKey(bad.InstanceID), []byte("{not json"))
	require.NoError(t, err)

	var logged bytes.Buffer
	domains, err := s.pinnedDomains(ctx, bufferLogger(&logged))
	require.NoError(t, err, "one poisoned pin must not fail the union")
	require.Equal(t, map[string]struct{}{"widget": {}}, domains)
	require.Contains(t, logged.String(), "pattern pin unparseable")
}

// TestPinnedDomains_ReadErrorIsHardPins the fail-closed half: an incomplete union
// would tear down consumers that live instances still need, so a failed read is an
// error and never an empty set.
func TestPinnedDomains_ReadErrorIsHard(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	conn, ctx := newControlTestConn(t)
	s := newStateStore(conn, "loom-state")
	sentinel := errors.New("resolution refused")
	s.lister = failingLister{inner: conn, err: sentinel}

	domains, err := s.pinnedDomains(ctx, bufferLogger(&bytes.Buffer{}))
	require.ErrorIs(t, err, sentinel)
	require.Nil(t, domains, "a partial union must never be returned")
}
