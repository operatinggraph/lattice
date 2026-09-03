package adapter_test

import (
	"context"
	"errors"
	"testing"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/natsfixture"
	"github.com/operatinggraph/lattice/internal/refractor/adapter"
	"github.com/operatinggraph/lattice/internal/refractor/failure"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// newGrantNamespaceAdapter builds a KV adapter over a fresh embedded-NATS
// bucket, keyed on the four columns a plain lens would RETURN to render a
// five-token D1 grant key by hand: domain, actor type, actor id, anchor id.
//
// Four columns rather than one, because that is the actual exploit shape: the
// key is a JOIN of RETURN values, so no single column ever equals "cap-read."
// and nothing about the lens's declaration names the namespace at all.
func newGrantNamespaceAdapter(t *testing.T) (*adapter.NatsKVAdapter, *substrate.KV, context.Context) {
	t.Helper()
	_, nc := natsfixture.Server(t)
	js, err := jetstream.New(nc)
	require.NoError(t, err)
	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	ctx := context.Background()
	_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "CAPKVGUARD"})
	require.NoError(t, err)
	kv, err := conn.OpenKV(ctx, "CAPKVGUARD")
	require.NoError(t, err)
	a, err := adapter.New(kv, []string{"d", "atype", "aid", "anchor"}, adapter.DeleteModeHard)
	require.NoError(t, err)
	return a, kv, ctx
}

// newGrantNamespaceAdapterOn builds a SECOND adapter over an existing bucket,
// so a test can model two lenses sharing capability-kv — which is the situation
// the truncate skip exists for.
func newGrantNamespaceAdapterOn(t *testing.T, kv *substrate.KV) (*adapter.NatsKVAdapter, *substrate.KV, context.Context) {
	t.Helper()
	a, err := adapter.New(kv, []string{"d", "atype", "aid", "anchor"}, adapter.DeleteModeHard)
	require.NoError(t, err)
	return a, kv, context.Background()
}

// grantKeys is the row a hand-rolled producer's RETURN would project.
func grantKeys() map[string]any {
	return map[string]any{
		"d":      "cap-read.billing",
		"atype":  "identity",
		"aid":    "Hj4kPmRtw9nbCxz5vQ2y",
		"anchor": "Kx3TmZpq7RvwNsY2Hc9L",
	}
}

// TestNatsKV_UnlicensedLensCannotMintAReadGrant is the runtime half of the
// producer closure (personal-lens-derivation-licence-design.md §4.3b).
//
// Both declaration-level checks — the registration refusal and the authoring
// gate — read a lens's DECLARED output key space, which is the §6.13
// descriptor. A plain nats_kv lens has none: it renders its key by joining
// RETURN column values, so a cypher returning the literal 'cap-read.billing'
// into its first key column writes `cap-read.billing.identity.<actor>.<anchor>`
// — a key capabilityread's wildcard reader matches as a LIVE GRANT — while
// every declaration-level check sees a lens that declares nothing.
//
// So the refusal has to happen where the key exists, which is the adapter that
// rendered it. Fail closed: no grant lands.
func TestNatsKV_UnlicensedLensCannotMintAReadGrant(t *testing.T) {
	a, kv, ctx := newGrantNamespaceAdapter(t)

	err := a.Upsert(ctx, grantKeys(), map[string]any{"readableAnchors": []any{}}, 1)
	require.Error(t, err, "an unlicensed lens must not be able to write the cap-read namespace")
	require.ErrorIs(t, err, adapter.ErrUnsanctionedReadGrantKey)

	// TERMINAL, not transient: the lens's own declaration renders this key on
	// every evaluation, so redelivery would spin against a permanent
	// misconfiguration forever.
	require.Equal(t, failure.CatTerminal, failure.Classify(err),
		"the refusal must be terminal — a transient classification would Nak the same key back for redelivery indefinitely")

	// The direction that actually matters: nothing landed, so the D1 gate's
	// wildcard listing finds no grant.
	_, gerr := kv.Get(ctx, "cap-read.billing.identity.Hj4kPmRtw9nbCxz5vQ2y.Kx3TmZpq7RvwNsY2Hc9L")
	require.True(t, errors.Is(gerr, substrate.ErrKeyNotFound),
		"the refused write must leave no key behind — a grant that lands and is merely logged about is still a grant")

	// The retraction direction is guarded too. An unlicensed lens must not be
	// able to tombstone a key in the namespace either: a soft tombstone is a
	// write, and on a shared bucket it would let one lens retract another's.
	derr := a.Delete(ctx, grantKeys(), 1)
	require.ErrorIs(t, derr, adapter.ErrUnsanctionedReadGrantKey,
		"the delete path renders the same key and must refuse it the same way")
}

// TestNatsKV_LicensedProducerWritesTheNamespace is the positive vector, and it
// carries as much weight as the refusal: a guard that closed the namespace to
// its own sanctioned producers would close it by taking the read-auth plane
// down at boot.
func TestNatsKV_LicensedProducerWritesTheNamespace(t *testing.T) {
	a, kv, ctx := newGrantNamespaceAdapter(t)
	a.SetReadGrantWriter(true)

	require.NoError(t, a.Upsert(ctx, grantKeys(), map[string]any{"readableAnchors": []any{}}, 1))
	entry, err := kv.Get(ctx, "cap-read.billing.identity.Hj4kPmRtw9nbCxz5vQ2y.Kx3TmZpq7RvwNsY2Hc9L")
	require.NoError(t, err)
	require.NotNil(t, entry)

	require.NoError(t, a.Delete(ctx, grantKeys(), 2))
}

// TestNatsKV_UnlicensedLensWritesEveryOtherNamespace pins the guard's scope: it
// governs the D1 namespace and nothing else. A guard that refused more would be
// a bucket-wide write refusal wearing a security argument.
func TestNatsKV_UnlicensedLensWritesEveryOtherNamespace(t *testing.T) {
	a, kv, ctx := newGrantNamespaceAdapter(t)

	keys := map[string]any{"d": "cap", "atype": "roles", "aid": "identity", "anchor": "Hj4kPmRtw9nbCxz5vQ2y"}
	require.NoError(t, a.Upsert(ctx, keys, map[string]any{"roles": []any{}}, 1),
		"the write plane's own cap.roles.* namespace is not this guard's business — the Processor consumes it synchronously at commit")

	entry, err := kv.Get(ctx, "cap.roles.identity.Hj4kPmRtw9nbCxz5vQ2y")
	require.NoError(t, err)
	require.NotNil(t, entry)
}

// TestNatsKV_UnsanctionedKeyReportsEveryRefusal pins where the dedup does NOT
// live.
//
// The health fault must be raised once per LENS, but an adapter is not a lens:
// a fresh one is built for every INTO-only hot reload, so a once on this type
// would re-arm on a package reinstall — precisely when an operator is reading
// the entry. The adapter therefore reports every refusal and the PIPELINE,
// which outlives its adapters, owns the dedup
// (Pipeline.RecordUnsanctionedGrantKeyRefusal).
func TestNatsKV_UnsanctionedKeyReportsEveryRefusal(t *testing.T) {
	a, _, ctx := newGrantNamespaceAdapter(t)
	var reported []string
	a.SetUnsanctionedGrantKeyReporter(func(_ context.Context, key string) {
		reported = append(reported, key)
	})

	for i := 0; i < 3; i++ {
		require.Error(t, a.Upsert(ctx, grantKeys(), map[string]any{}, uint64(i+1)))
	}
	require.Error(t, a.Delete(ctx, grantKeys(), 9))

	require.Len(t, reported, 4,
		"the adapter reports every refusal — deduplicating here would reset the count on each hot reload")
	require.Equal(t, "cap-read.billing.identity.Hj4kPmRtw9nbCxz5vQ2y.Kx3TmZpq7RvwNsY2Hc9L", reported[0])
}

// TestNatsKV_UnlicensedTruncateSkipsTheReadGrantNamespace closes the write path
// buildKey cannot guard.
//
// Truncate never renders a key: it LISTS the bucket and Purges what it finds,
// so the namespace refusal has to be applied to the listing instead. The
// exposure is concrete rather than theoretical — projection.ApplyTruncateScope
// derives a key prefix only for an actor-aggregate lens, so a descriptor-less
// plain lens sharing capability-kv has NO prefix, and its truncating rebuild
// (grantchange's own convergence rebuild takes this path) would purge the whole
// bucket, every sanctioned producer's grants included.
func TestNatsKV_UnlicensedTruncateSkipsTheReadGrantNamespace(t *testing.T) {
	a, kv, ctx := newGrantNamespaceAdapter(t)
	// No SetKeyPrefix: the unscoped shape a plain lens on a shared bucket has.

	// A sanctioned producer's grant, written by a licensed sibling.
	writer, _, _ := newGrantNamespaceAdapterOn(t, kv)
	writer.SetReadGrantWriter(true)
	require.NoError(t, writer.Upsert(ctx, grantKeys(), map[string]any{"readableAnchors": []any{}}, 1))

	// The unlicensed lens's own row, in the same bucket.
	own := map[string]any{"d": "myLens", "atype": "identity", "aid": "Hj4kPmRtw9nbCxz5vQ2y", "anchor": "Kx3TmZpq7RvwNsY2Hc9L"}
	require.NoError(t, a.Upsert(ctx, own, map[string]any{"v": 1}, 1))

	var reported []string
	a.SetUnsanctionedGrantKeyReporter(func(_ context.Context, key string) { reported = append(reported, key) })

	require.NoError(t, a.Truncate(ctx))

	_, err := kv.Get(ctx, "cap-read.billing.identity.Hj4kPmRtw9nbCxz5vQ2y.Kx3TmZpq7RvwNsY2Hc9L")
	require.NoError(t, err,
		"an unlicensed lens's rebuild must leave the D1 namespace alone — purging it would wipe another lens's grants under cover of a rebuild")
	_, err = kv.Get(ctx, "myLens.identity.Hj4kPmRtw9nbCxz5vQ2y.Kx3TmZpq7RvwNsY2Hc9L")
	require.True(t, errors.Is(err, substrate.ErrKeyNotFound),
		"its OWN rows must still be purged — skipping the namespace is not a licence to skip the rebuild")

	require.Len(t, reported, 1,
		"the skip is loud: it reaches the lens's own health entry through the same reporter the write refusal uses")
}

// TestNatsKV_LicensedTruncatePurgesTheNamespace is the positive vector: a real
// producer's rebuild must still clear its own grants, or a truncating rebuild
// of the auth plane would silently become a no-op.
func TestNatsKV_LicensedTruncatePurgesTheNamespace(t *testing.T) {
	a, kv, ctx := newGrantNamespaceAdapter(t)
	a.SetReadGrantWriter(true)
	require.NoError(t, a.Upsert(ctx, grantKeys(), map[string]any{"readableAnchors": []any{}}, 1))

	require.NoError(t, a.Truncate(ctx))

	_, err := kv.Get(ctx, "cap-read.billing.identity.Hj4kPmRtw9nbCxz5vQ2y.Kx3TmZpq7RvwNsY2Hc9L")
	require.True(t, errors.Is(err, substrate.ErrKeyNotFound),
		"a licensed producer's truncate clears its own namespace rows")
}
