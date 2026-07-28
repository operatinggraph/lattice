package capabilityread_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/natsfixture"
	"github.com/operatinggraph/lattice/internal/refractor/capabilityread"
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
	_, err = conn.JetStream().CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "capability-kv"})
	require.NoError(t, err)

	kv, err := conn.OpenKV(ctx, "capability-kv")
	require.NoError(t, err)
	return kv
}

func putPerAnchorEntry(t *testing.T, kv *substrate.KV, key string, isDeleted bool) {
	t.Helper()
	body := struct {
		IsDeleted bool `json:"isDeleted"`
	}{IsDeleted: isDeleted}
	raw, err := json.Marshal(body)
	require.NoError(t, err)
	_, err = kv.Put(context.Background(), key, raw)
	require.NoError(t, err)
}

func TestIsReadable_PerAnchorBaseKey_Grants(t *testing.T) {
	kv := newTestKV(t)
	putPerAnchorEntry(t, kv, "cap-read.identity.A1.unitNano1", false)

	readable, err := capabilityread.IsReadable(context.Background(), kv, "identity", "A1", "unitNano1")
	require.NoError(t, err)
	require.True(t, readable, "a live per-anchor base key must grant")

	readable, err = capabilityread.IsReadable(context.Background(), kv, "identity", "A1", "unitNano2")
	require.NoError(t, err)
	require.False(t, readable, "an anchor with no per-anchor key must deny")
}

func TestIsReadable_PerAnchorDomainKey_Grants(t *testing.T) {
	kv := newTestKV(t)
	putPerAnchorEntry(t, kv, "cap-read.residence.identity.A1.leaseNano1", false)

	readable, err := capabilityread.IsReadable(context.Background(), kv, "identity", "A1", "leaseNano1")
	require.NoError(t, err)
	require.True(t, readable, "a live per-anchor domain key must grant on its own")
}

// TestIsReadable_PerAnchorKey_TombstonedDenies pins that a soft-tombstoned
// per-anchor key reads as absent, the same posture the legacy aggregate
// document's isDeleted flag takes — a departed producer or genuine
// revocation must never be misread as a live grant.
func TestIsReadable_PerAnchorKey_TombstonedDenies(t *testing.T) {
	kv := newTestKV(t)
	putPerAnchorEntry(t, kv, "cap-read.identity.A1.unitNano1", true)

	readable, err := capabilityread.IsReadable(context.Background(), kv, "identity", "A1", "unitNano1")
	require.NoError(t, err)
	require.False(t, readable, "a soft-tombstoned per-anchor key must be treated as absent")
}

// TestIsReadable_UnionsAcrossPerAnchorDomains pins that the base per-anchor
// key and any number of domain per-anchor keys grant independently for the
// same actor — the reader never requires a single producer to hold every
// anchor.
func TestIsReadable_UnionsAcrossPerAnchorDomains(t *testing.T) {
	kv := newTestKV(t)
	putPerAnchorEntry(t, kv, "cap-read.identity.A1.selfNano1", false)
	putPerAnchorEntry(t, kv, "cap-read.residence.identity.A1.unitNano1", false)
	putPerAnchorEntry(t, kv, "cap-read.clinic.identity.A1.patientNano1", false)

	for _, anchor := range []string{"selfNano1", "unitNano1", "patientNano1"} {
		readable, err := capabilityread.IsReadable(context.Background(), kv, "identity", "A1", anchor)
		require.NoError(t, err)
		require.True(t, readable, "anchor %q must be granted via the union of all per-anchor keys", anchor)
	}
}

func TestIsReadable_RejectsSubjectMetacharactersInAnchorID(t *testing.T) {
	kv := newTestKV(t)
	for _, bad := range []string{"a.b", "a*b", "a>b"} {
		_, err := capabilityread.IsReadable(context.Background(), kv, "identity", "A1", bad)
		require.Error(t, err, "anchorID %q containing a NATS subject metacharacter must be rejected", bad)
	}
}

// TestIsReadable_WildcardAnchorKey_UnwritableAtTheStorageLayer pins §8.1's
// wildcard-anchor non-admission for the per-anchor shape: since anchor
// identity now lives in the KEY (not an arbitrary JSON body field, per the
// legacy shape's WildcardAnchor concern), a literal "*" anchor-id segment
// can never even be written — the NATS-KV client itself refuses the key as
// malformed, before any admission logic runs. This is a stronger closure
// than the legacy shape had (exact-string comparison against a body field
// that could hold any string): the vulnerable state is unconstructable, not
// merely unadmitted.
func TestIsReadable_WildcardAnchorKey_UnwritableAtTheStorageLayer(t *testing.T) {
	kv := newTestKV(t)
	_, err := kv.Put(context.Background(), "cap-read.identity.A1.*", []byte(`{"isDeleted":false}`))
	require.Error(t, err, "a per-anchor key with a literal \"*\" anchor-id segment must be unwritable")
	require.True(t, substrate.IsInvalidKeyError(err), "the rejection must be the NATS-KV invalid-key class, not some other failure: %v", err)
}

func TestIsReadable_NoEntry_DeniesFailClosed(t *testing.T) {
	kv := newTestKV(t)
	readable, err := capabilityread.IsReadable(context.Background(), kv, "identity", "A1", "unitNano1")
	require.NoError(t, err)
	require.False(t, readable, "no cap-read entry at all must deny")
}

func TestIsReadable_RejectsEmptyActorFields(t *testing.T) {
	kv := newTestKV(t)

	_, err := capabilityread.IsReadable(context.Background(), kv, "", "A1", "unitNano1")
	require.Error(t, err, "empty actorType must be rejected, not silently denied")

	_, err = capabilityread.IsReadable(context.Background(), kv, "identity", "", "unitNano1")
	require.Error(t, err, "empty actorID must be rejected, not silently denied")
}

func TestIsReadable_RejectsSubjectMetacharactersInActorFields(t *testing.T) {
	kv := newTestKV(t)

	for _, bad := range []string{"a.b", "a*b", "a>b"} {
		_, err := capabilityread.IsReadable(context.Background(), kv, bad, "A1", "unitNano1")
		require.Error(t, err, "actorType %q containing a NATS subject metacharacter must be rejected", bad)

		_, err = capabilityread.IsReadable(context.Background(), kv, "identity", bad, "unitNano1")
		require.Error(t, err, "actorID %q containing a NATS subject metacharacter must be rejected", bad)
	}
}

func TestIsReadable_DoesNotLeakAcrossActors(t *testing.T) {
	kv := newTestKV(t)
	putPerAnchorEntry(t, kv, "cap-read.identity.B1.unitNanoB", false)

	readable, err := capabilityread.IsReadable(context.Background(), kv, "identity", "A1", "unitNanoB")
	require.NoError(t, err)
	require.False(t, readable, "actor A1 must not inherit actor B1's grants")
}

// TestIsReadable_MalformedJSON_PropagatesError pins the fail-closed *error*
// arm on an unparseable "cap-read.*" key: a producer bug (or hand-edited KV
// state) that leaves non-JSON bytes at the key must surface as an error, not
// a silent deny — a caller distinguishing "denied" from "the gate itself is
// broken" needs this to actually error.
func TestIsReadable_MalformedJSON_PropagatesError(t *testing.T) {
	kv := newTestKV(t)
	_, err := kv.Put(context.Background(), "cap-read.identity.A1.unitNano1", []byte("not-json"))
	require.NoError(t, err, "raw Put bypasses putPerAnchorEntry's marshal so the stored bytes are genuinely malformed")

	readable, err := capabilityread.IsReadable(context.Background(), kv, "identity", "A1", "unitNano1")
	require.Error(t, err, "an unparseable cap-read key must error, not silently deny")
	require.False(t, readable)
	require.Contains(t, err.Error(), "unmarshal")
	require.Contains(t, err.Error(), "cap-read.identity.A1.unitNano1")
}

// TestIsReadable_KVFailure_PropagatesError pins the fail-closed *error* arm:
// a KV op failure (here, an already-canceled context, which every Get/
// ListKeysFilter call surfaces via ctx.Err()) must surface as an error, not
// the silent "not found" a swallowed error would produce — whichever of the
// two union sources (per-anchor base/domain) IsReadable checks first.
func TestIsReadable_KVFailure_PropagatesError(t *testing.T) {
	kv := newTestKV(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	readable, err := capabilityread.IsReadable(ctx, kv, "identity", "A1", "unitNano1")
	require.Error(t, err, "a KV failure must error, not silently deny as if absent")
	require.False(t, readable)
	require.Contains(t, err.Error(), "capabilityread:")
}
