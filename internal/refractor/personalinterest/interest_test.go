package personalinterest_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/natsfixture"
	"github.com/operatinggraph/lattice/internal/refractor/personalinterest"
	"github.com/operatinggraph/lattice/internal/substrate"
)

func newTestKV(t *testing.T) *substrate.KV {
	t.Helper()
	s := natsfixture.StartServer(t)

	nc := natsfixture.Connect(t, s.ClientURL())
	t.Cleanup(nc.Close)

	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	t.Cleanup(conn.Close)

	ctx := context.Background()
	_, err = conn.JetStream().CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "personal-lens-interest"})
	require.NoError(t, err)

	kv, err := conn.OpenKV(ctx, "personal-lens-interest")
	require.NoError(t, err)
	return kv
}

func TestIsRelevant_NoRegistration_AdmitsEverything(t *testing.T) {
	kv := newTestKV(t)
	ctx := context.Background()

	relevant, err := personalinterest.IsRelevant(ctx, kv, "identityA", "lease", "lease.1")
	require.NoError(t, err)
	require.True(t, relevant, "no registered device must default to admit-all")
}

func TestIsRelevant_EmptyFilterRegistration_AdmitsEverything(t *testing.T) {
	kv := newTestKV(t)
	ctx := context.Background()

	require.NoError(t, personalinterest.Register(ctx, kv, "identityA", "deviceX", nil, nil, time.Now().UTC().Format(time.RFC3339)))

	relevant, err := personalinterest.IsRelevant(ctx, kv, "identityA", "lease", "lease.1")
	require.NoError(t, err)
	require.True(t, relevant, "a registration with no declared types/anchors must admit everything")
}

func TestIsRelevant_TypeMatch(t *testing.T) {
	kv := newTestKV(t)
	ctx := context.Background()

	require.NoError(t, personalinterest.Register(ctx, kv, "identityA", "deviceX", []string{"lease"}, nil, time.Now().UTC().Format(time.RFC3339)))

	relevant, err := personalinterest.IsRelevant(ctx, kv, "identityA", "lease", "lease.1")
	require.NoError(t, err)
	require.True(t, relevant)

	relevant, err = personalinterest.IsRelevant(ctx, kv, "identityA", "payment", "payment.1")
	require.NoError(t, err)
	require.False(t, relevant, "a declared type filter must exclude a non-matching anchor type")
}

func TestIsRelevant_AnchorMatch(t *testing.T) {
	kv := newTestKV(t)
	ctx := context.Background()

	require.NoError(t, personalinterest.Register(ctx, kv, "identityA", "deviceX", nil, []string{"lease.1"}, time.Now().UTC().Format(time.RFC3339)))

	relevant, err := personalinterest.IsRelevant(ctx, kv, "identityA", "lease", "lease.1")
	require.NoError(t, err)
	require.True(t, relevant)

	relevant, err = personalinterest.IsRelevant(ctx, kv, "identityA", "lease", "lease.2")
	require.NoError(t, err)
	require.False(t, relevant, "a declared anchor filter must exclude a non-matching anchor id")
}

func TestIsRelevant_MultiDevice_AnyMatchAdmits(t *testing.T) {
	kv := newTestKV(t)
	ctx := context.Background()

	require.NoError(t, personalinterest.Register(ctx, kv, "identityA", "deviceX", []string{"payment"}, nil, time.Now().UTC().Format(time.RFC3339)))
	require.NoError(t, personalinterest.Register(ctx, kv, "identityA", "deviceY", []string{"lease"}, nil, time.Now().UTC().Format(time.RFC3339)))

	relevant, err := personalinterest.IsRelevant(ctx, kv, "identityA", "lease", "lease.1")
	require.NoError(t, err)
	require.True(t, relevant, "any one device's filter admitting the delta must admit it")
}

func TestDeregister_RemovesRegistration(t *testing.T) {
	kv := newTestKV(t)
	ctx := context.Background()

	require.NoError(t, personalinterest.Register(ctx, kv, "identityA", "deviceX", []string{"payment"}, nil, time.Now().UTC().Format(time.RFC3339)))
	relevant, err := personalinterest.IsRelevant(ctx, kv, "identityA", "lease", "lease.1")
	require.NoError(t, err)
	require.False(t, relevant)

	require.NoError(t, personalinterest.Deregister(ctx, kv, "identityA", "deviceX"))

	relevant, err = personalinterest.IsRelevant(ctx, kv, "identityA", "lease", "lease.1")
	require.NoError(t, err)
	require.True(t, relevant, "deregistering the only device must revert to admit-all")
}

func TestDeregister_AbsentDevice_NoError(t *testing.T) {
	kv := newTestKV(t)
	require.NoError(t, personalinterest.Deregister(context.Background(), kv, "identityA", "deviceX"))
}

func TestRegister_MissingIdentityOrDevice_Errors(t *testing.T) {
	kv := newTestKV(t)
	ctx := context.Background()

	require.Error(t, personalinterest.Register(ctx, kv, "", "deviceX", nil, nil, time.Now().UTC().Format(time.RFC3339)))
	require.Error(t, personalinterest.Register(ctx, kv, "identityA", "", nil, nil, time.Now().UTC().Format(time.RFC3339)))
}

func TestSetRevisionCursor_NewDevice_CreatesCursorOnlyDoc(t *testing.T) {
	kv := newTestKV(t)
	ctx := context.Background()

	created, err := personalinterest.SetRevisionCursor(ctx, kv, "identityA", "deviceX", 10500,
		time.Now().UTC().Format(time.RFC3339))
	require.NoError(t, err)
	// Reported, not incidental: the row this created carries no types and no
	// anchors, which IsRelevant reads as admit-everything — so the caller has
	// to be able to tell a CREATE from an update and announce the widening on
	// the Interest Set change edge.
	require.True(t, created, "hydrating an unregistered device CREATES its registration")

	key, err := personalinterest.Key("identityA", "deviceX")
	require.NoError(t, err)
	entry, err := kv.Get(ctx, key)
	require.NoError(t, err)
	var doc map[string]any
	require.NoError(t, json.Unmarshal(entry.Value, &doc))
	require.Equal(t, float64(10500), doc["revisionCursor"])
}

func TestSetRevisionCursor_PreservesExistingFilter(t *testing.T) {
	kv := newTestKV(t)
	ctx := context.Background()

	require.NoError(t, personalinterest.Register(ctx, kv, "identityA", "deviceX", []string{"lease"}, nil, time.Now().UTC().Format(time.RFC3339)))
	created, err := personalinterest.SetRevisionCursor(ctx, kv, "identityA", "deviceX", 20000,
		time.Now().UTC().Format(time.RFC3339))
	require.NoError(t, err)
	require.False(t, created, "an existing registration is UPDATED, and an update touches no filter — announcing it would drive a reprojection of an identity whose interest did not move")

	// The Interest Set filter must survive the cursor update — a hydrate call
	// must not silently revert a device to admit-all.
	relevant, err := personalinterest.IsRelevant(ctx, kv, "identityA", "payment", "payment.1")
	require.NoError(t, err)
	require.False(t, relevant, "the pre-existing type filter must survive a revision-cursor update")

	key, err := personalinterest.Key("identityA", "deviceX")
	require.NoError(t, err)
	entry, err := kv.Get(ctx, key)
	require.NoError(t, err)
	var doc map[string]any
	require.NoError(t, json.Unmarshal(entry.Value, &doc))
	require.Equal(t, float64(20000), doc["revisionCursor"])
}

func TestSetRevisionCursor_ConcurrentCallers_NeitherUpdateIsLost(t *testing.T) {
	kv := newTestKV(t)
	ctx := context.Background()
	now := time.Now().UTC().Format(time.RFC3339)

	require.NoError(t, personalinterest.Register(ctx, kv, "identityA", "deviceX", []string{"lease"}, nil, now))

	// Two concurrent cursor-record calls for the SAME device (e.g. a hydrate
	// racing another hydrate, or a register racing a hydrate) must both
	// survive via the CAS retry loop — a plain Get-then-Put would let the
	// second Put silently clobber the first's revision.
	const n = 8
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		go func(rev uint64) {
			_, cerr := personalinterest.SetRevisionCursor(ctx, kv, "identityA", "deviceX", rev, now)
			errs <- cerr
		}(uint64(1000 + i))
	}
	for i := 0; i < n; i++ {
		require.NoError(t, <-errs)
	}

	// Whichever call's write landed last, the filter set by Register at the
	// start must still be intact — no update was lost outright, only raced.
	relevant, err := personalinterest.IsRelevant(ctx, kv, "identityA", "payment", "payment.1")
	require.NoError(t, err)
	require.False(t, relevant, "the type filter must survive concurrent cursor updates")

	key, err := personalinterest.Key("identityA", "deviceX")
	require.NoError(t, err)
	entry, err := kv.Get(ctx, key)
	require.NoError(t, err)
	var doc map[string]any
	require.NoError(t, json.Unmarshal(entry.Value, &doc))
	cursor, ok := doc["revisionCursor"].(float64)
	require.True(t, ok)
	require.GreaterOrEqual(t, cursor, float64(1000))
	require.Less(t, cursor, float64(1000+n))
}

func TestSetRevisionCursor_MissingIdentityOrDevice_Errors(t *testing.T) {
	kv := newTestKV(t)
	ctx := context.Background()

	_, err := personalinterest.SetRevisionCursor(ctx, kv, "", "deviceX", 1, time.Now().UTC().Format(time.RFC3339))
	require.Error(t, err)
	_, err = personalinterest.SetRevisionCursor(ctx, kv, "identityA", "", 1, time.Now().UTC().Format(time.RFC3339))
	require.Error(t, err)
}

func TestIsRelevant_ScopedToIdentityPrefix(t *testing.T) {
	kv := newTestKV(t)
	ctx := context.Background()

	// A registration for a DIFFERENT identity that happens to share a
	// deviceId suffix must not leak into identityA's prefix scan.
	require.NoError(t, personalinterest.Register(ctx, kv, "identityB", "deviceX", []string{"payment"}, nil, time.Now().UTC().Format(time.RFC3339)))

	relevant, err := personalinterest.IsRelevant(ctx, kv, "identityA", "lease", "lease.1")
	require.NoError(t, err)
	require.True(t, relevant, "identityA has no registration of its own and must default to admit-all")
}

// TestParseKey_IsTheExactInverseOfKey pins ParseKey against the constructor
// that writes every row, rather than against a hand-written expectation: the
// two are a matched pair, and a reconciler that deletes on the strength of a
// parsed key is only as sound as that pairing.
func TestParseKey_IsTheExactInverseOfKey(t *testing.T) {
	for _, tc := range []struct{ identityID, deviceID string }{
		{"AAAAAAAAAAAAAAAAAAAA", "phone-1"},
		// The device half may itself carry dots — the split is on the FIRST
		// dot, so only the identity half is required to be dot-free.
		{"AAAAAAAAAAAAAAAAAAAA", "chrome.macos.1"},
		{"MQsmTTAgNkngkdEjQz9L", "BHrdHRUWXPkLiukEvK9e"},
	} {
		key, err := personalinterest.Key(tc.identityID, tc.deviceID)
		require.NoError(t, err)
		gotID, gotDev, ok := personalinterest.ParseKey(key)
		require.True(t, ok, "key %q built by Key must parse", key)
		require.Equal(t, tc.identityID, gotID)
		require.Equal(t, tc.deviceID, gotDev)
	}
}

// TestParseKey_FailsClosedOnAnythingKeyCannotHaveWritten: a key that does not
// have the shape Key produces is not this package's, and must be reported as
// unparseable rather than split into some best-effort pair.
func TestParseKey_FailsClosedOnAnythingKeyCannotHaveWritten(t *testing.T) {
	for _, bad := range []string{"", "nodot", ".leading", "trailing.", "."} {
		_, _, ok := personalinterest.ParseKey(bad)
		require.False(t, ok, "key %q must not parse", bad)
	}
}

// TestParsedRegisteredAt_ReadsWhatRegisterWrote closes the loop the same way:
// the accessor is exercised against a document Register actually produced, so
// it cannot drift from the wire shape it reads.
func TestParsedRegisteredAt_ReadsWhatRegisterWrote(t *testing.T) {
	kv := newTestKV(t)
	ctx := context.Background()

	want := time.Now().UTC().Truncate(time.Second)
	require.NoError(t, personalinterest.Register(ctx, kv, "identityA", "deviceX", nil, nil, want.Format(time.RFC3339)))

	key, err := personalinterest.Key("identityA", "deviceX")
	require.NoError(t, err)
	entry, err := kv.Get(ctx, key)
	require.NoError(t, err)

	got, err := personalinterest.ParsedRegisteredAt(entry.Value)
	require.NoError(t, err)
	require.True(t, want.Equal(got), "ParsedRegisteredAt = %v, want %v", got, want)
}

// TestParsedRegisteredAt_ErrorsRatherThanGuessing: a caller aging a
// registration must be told the document did not answer, never handed a zero
// time that would read as "registered at the epoch" and age past any grace.
func TestParsedRegisteredAt_ErrorsRatherThanGuessing(t *testing.T) {
	for name, body := range map[string][]byte{
		"not json":            []byte("{not json"),
		"no registeredAt":     []byte(`{"types":["lease"]}`),
		"empty registeredAt":  []byte(`{"registeredAt":""}`),
		"unparseable instant": []byte(`{"registeredAt":"yesterday"}`),
	} {
		_, err := personalinterest.ParsedRegisteredAt(body)
		require.Error(t, err, "case %q must error", name)
	}
}

// TestRegisteredAtWireTagIsPinned. Every accessor in this package reads
// registeredAt through the same struct tag it writes, so renaming the tag
// keeps the round trip green while silently breaking every OTHER reader of
// the bucket — Loupe's fleet roster, and the reconciler that ages a
// registration before removing it. Assert the bytes on the wire.
func TestRegisteredAtWireTagIsPinned(t *testing.T) {
	kv := newTestKV(t)
	ctx := context.Background()

	at := time.Now().UTC().Format(time.RFC3339)
	require.NoError(t, personalinterest.Register(ctx, kv, "identityA", "deviceX", nil, nil, at))

	key, err := personalinterest.Key("identityA", "deviceX")
	require.NoError(t, err)
	entry, err := kv.Get(ctx, key)
	require.NoError(t, err)

	require.Contains(t, string(entry.Value), `"registeredAt":"`+at+`"`,
		"the on-wire field name and value must be exactly this — other components read this document without this package's struct")
}

// TestDeregisterRevision_RefusesASupersededRevision pins the optimistic
// concurrency the reconciler relies on: it READS a registration to decide the
// removal is warranted, so a device that re-registers in between must survive.
func TestDeregisterRevision_RefusesASupersededRevision(t *testing.T) {
	kv := newTestKV(t)
	ctx := context.Background()

	at := time.Now().UTC().Format(time.RFC3339)
	require.NoError(t, personalinterest.Register(ctx, kv, "identityA", "deviceX", nil, nil, at))
	key, err := personalinterest.Key("identityA", "deviceX")
	require.NoError(t, err)
	entry, err := kv.Get(ctx, key)
	require.NoError(t, err)

	// The device re-registers under the reader.
	require.NoError(t, personalinterest.Register(ctx, kv, "identityA", "deviceX", []string{"lease"}, nil, at))

	err = personalinterest.DeregisterRevision(ctx, kv, "identityA", "deviceX", entry.Revision)
	require.ErrorIs(t, err, substrate.ErrRevisionConflict)
	_, err = kv.Get(ctx, key)
	require.NoError(t, err, "the re-registered document must survive a delete conditioned on the superseded revision")

	// At the current revision it goes.
	entry, err = kv.Get(ctx, key)
	require.NoError(t, err)
	require.NoError(t, personalinterest.DeregisterRevision(ctx, kv, "identityA", "deviceX", entry.Revision))
	_, err = kv.Get(ctx, key)
	require.ErrorIs(t, err, substrate.ErrKeyNotFound)
}
