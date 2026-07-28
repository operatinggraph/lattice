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

func putReadDoc(t *testing.T, kv *substrate.KV, key string, isDeleted bool, anchors ...[2]string) {
	t.Helper()
	type readableAnchor struct {
		AnchorType string `json:"anchorType"`
		AnchorID   string `json:"anchorId"`
	}
	body := struct {
		IsDeleted       bool             `json:"isDeleted"`
		ReadableAnchors []readableAnchor `json:"readableAnchors"`
	}{IsDeleted: isDeleted}
	for _, a := range anchors {
		body.ReadableAnchors = append(body.ReadableAnchors, readableAnchor{AnchorType: a[0], AnchorID: a[1]})
	}
	raw, err := json.Marshal(body)
	require.NoError(t, err)
	_, err = kv.Put(context.Background(), key, raw)
	require.NoError(t, err)
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

// TestIsReadable_MigrationCoexistence_UnionsBothShapes pins the §6 dual-read
// migration window: one domain already flipped to per-anchor keys, a sibling
// domain still on the legacy aggregate-document shape, for the SAME actor.
// Both must independently grant — the reader never requires every producer
// to be on the same shape before it can serve either.
func TestIsReadable_MigrationCoexistence_UnionsBothShapes(t *testing.T) {
	kv := newTestKV(t)
	putPerAnchorEntry(t, kv, "cap-read.residence.identity.A1.unitNano1", false)
	putReadDoc(t, kv, "cap-read.clinic.identity.A1", false, [2]string{"patient", "patientNano1"})

	readable, err := capabilityread.IsReadable(context.Background(), kv, "identity", "A1", "unitNano1")
	require.NoError(t, err)
	require.True(t, readable, "the flipped domain's per-anchor key must grant")

	readable, err = capabilityread.IsReadable(context.Background(), kv, "identity", "A1", "patientNano1")
	require.NoError(t, err)
	require.True(t, readable, "the not-yet-flipped domain's legacy document must still grant")
}

// TestIsReadable_MigrationCoexistence_LegacyParentTombstonedRevocationWins
// pins the write-path invariant §4.2/§6 guarantees (proven at the pipeline
// level by TestExecuteFullForActor_MultiEnvelopeFn_Retraction_LegacyParentTombstoned):
// the same evaluation that writes an actor's fresh per-anchor keys also
// guard-tombstones its legacy parent document, so once a domain's legacy doc
// is tombstoned, an anchor that dropped out of the fresh set (absent per-
// anchor key) can never be resurrected by a stale legacy read — revocation
// wins, it does not fall back to whatever the retired document used to say.
func TestIsReadable_MigrationCoexistence_LegacyParentTombstonedRevocationWins(t *testing.T) {
	kv := newTestKV(t)
	// The legacy document is tombstoned (the post-flip evaluation retired it)
	// even though it still carries the now-revoked anchor in its stale body —
	// §6.8's retained-watermark convention, mirroring
	// TestIsReadable_TombstonedSlice_DeniesAsAbsent.
	putReadDoc(t, kv, "cap-read.residence.identity.A1", true, [2]string{"unit", "unitNano1"})
	// No per-anchor key exists for unitNano1 — it was not in the fresh set.

	readable, err := capabilityread.IsReadable(context.Background(), kv, "identity", "A1", "unitNano1")
	require.NoError(t, err)
	require.False(t, readable, "a revoked anchor must not be resurrected by its retired legacy document")
}

func TestIsReadable_RejectsSubjectMetacharactersInAnchorID(t *testing.T) {
	kv := newTestKV(t)
	for _, bad := range []string{"a.b", "a*b", "a>b"} {
		_, err := capabilityread.IsReadable(context.Background(), kv, "identity", "A1", bad)
		require.Error(t, err, "anchorID %q containing a NATS subject metacharacter must be rejected", bad)
	}
}

// TestIsReadable_WildcardAnchorEntry_NeverAdmitsAConcreteRequest pins §8.1's
// wildcard-anchor non-admission: a stored anchor entry literally equal to "*"
// (the Postgres-only WildcardAnchor escape hatch has no NATS-KV projection,
// but a hand-seeded or buggy producer could still write one) must never be
// treated as matching every concrete anchor — admission is always exact
// string equality, never wildcard semantics.
func TestIsReadable_WildcardAnchorEntry_NeverAdmitsAConcreteRequest(t *testing.T) {
	kv := newTestKV(t)
	putReadDoc(t, kv, "cap-read.identity.A1", false, [2]string{"identity", "*"})

	readable, err := capabilityread.IsReadable(context.Background(), kv, "identity", "A1", "unitNano1")
	require.NoError(t, err)
	require.False(t, readable, "a stored literal \"*\" anchor entry must not admit a concrete, unrelated anchorID request")
}

func TestIsReadable_NoEntry_DeniesFailClosed(t *testing.T) {
	kv := newTestKV(t)
	readable, err := capabilityread.IsReadable(context.Background(), kv, "identity", "A1", "unitNano1")
	require.NoError(t, err)
	require.False(t, readable, "no cap-read entry at all must deny")
}

func TestIsReadable_BaseSlice_Grants(t *testing.T) {
	kv := newTestKV(t)
	putReadDoc(t, kv, "cap-read.identity.A1", false, [2]string{"unit", "unitNano1"})

	readable, err := capabilityread.IsReadable(context.Background(), kv, "identity", "A1", "unitNano1")
	require.NoError(t, err)
	require.True(t, readable)

	readable, err = capabilityread.IsReadable(context.Background(), kv, "identity", "A1", "unitNano2")
	require.NoError(t, err)
	require.False(t, readable, "an anchor absent from the granted set must deny")
}

func TestIsReadable_DomainSlice_Grants(t *testing.T) {
	kv := newTestKV(t)
	putReadDoc(t, kv, "cap-read.residence.identity.A1", false, [2]string{"lease", "leaseNano1"})

	readable, err := capabilityread.IsReadable(context.Background(), kv, "identity", "A1", "leaseNano1")
	require.NoError(t, err)
	require.True(t, readable, "a domain-specific slice must grant on its own, without a base slice present")
}

func TestIsReadable_UnionsAcrossSlices(t *testing.T) {
	kv := newTestKV(t)
	putReadDoc(t, kv, "cap-read.identity.A1", false, [2]string{"identity", "selfNano1"})
	putReadDoc(t, kv, "cap-read.residence.identity.A1", false, [2]string{"unit", "unitNano1"})
	putReadDoc(t, kv, "cap-read.clinic.identity.A1", false, [2]string{"patient", "patientNano1"})

	for _, anchor := range []string{"selfNano1", "unitNano1", "patientNano1"} {
		readable, err := capabilityread.IsReadable(context.Background(), kv, "identity", "A1", anchor)
		require.NoError(t, err)
		require.True(t, readable, "anchor %q must be granted via the union of all slices", anchor)
	}
}

// TestIsReadable_TombstonedSlice_DeniesAsAbsent seeds a NON-empty
// readableAnchors alongside isDeleted:true (a producer that soft-deletes
// without also clearing the array, §6.8's retained-watermark convention) —
// pins that the isDeleted check short-circuits before the anchor-match loop,
// so a future reordering of the two checks would fail this test rather than
// silently leaking a stale grant.
func TestIsReadable_TombstonedSlice_DeniesAsAbsent(t *testing.T) {
	kv := newTestKV(t)
	putReadDoc(t, kv, "cap-read.residence.identity.A1", true, [2]string{"unit", "unitNano1"})

	readable, err := capabilityread.IsReadable(context.Background(), kv, "identity", "A1", "unitNano1")
	require.NoError(t, err)
	require.False(t, readable, "a soft-tombstoned (isDeleted:true) slice must be treated as absent, even carrying a non-empty readableAnchors")
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
	putReadDoc(t, kv, "cap-read.identity.B1", false, [2]string{"unit", "unitNanoB"})

	readable, err := capabilityread.IsReadable(context.Background(), kv, "identity", "A1", "unitNanoB")
	require.NoError(t, err)
	require.False(t, readable, "actor A1 must not inherit actor B1's grants")
}

// TestIsReadable_MalformedJSON_PropagatesError pins the fail-closed *error*
// arm on an unparseable "cap-read.*" document: a producer bug (or hand-edited
// KV state) that leaves non-JSON bytes at the key must surface as an error,
// not a silent deny — a caller distinguishing "denied" from "the gate itself
// is broken" needs this to actually error.
func TestIsReadable_MalformedJSON_PropagatesError(t *testing.T) {
	kv := newTestKV(t)
	_, err := kv.Put(context.Background(), "cap-read.identity.A1", []byte("not-json"))
	require.NoError(t, err, "raw Put bypasses putReadDoc's marshal so the stored bytes are genuinely malformed")

	readable, err := capabilityread.IsReadable(context.Background(), kv, "identity", "A1", "unitNano1")
	require.Error(t, err, "an unparseable cap-read document must error, not silently deny")
	require.False(t, readable)
	require.Contains(t, err.Error(), "unmarshal")
	require.Contains(t, err.Error(), "cap-read.identity.A1")
}

// TestIsReadable_KVFailure_PropagatesError pins the fail-closed *error* arm:
// a KV op failure (here, an already-canceled context, which every Get/
// ListKeysFilter call surfaces via ctx.Err()) must surface as an error, not
// the silent "not found" a swallowed error would produce — whichever of the
// four union sources (per-anchor base/domain, legacy base/domain) IsReadable
// checks first.
func TestIsReadable_KVFailure_PropagatesError(t *testing.T) {
	kv := newTestKV(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	readable, err := capabilityread.IsReadable(ctx, kv, "identity", "A1", "unitNano1")
	require.Error(t, err, "a KV failure must error, not silently deny as if absent")
	require.False(t, readable)
	require.Contains(t, err.Error(), "capabilityread:")
}
