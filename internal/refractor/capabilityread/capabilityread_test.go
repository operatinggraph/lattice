package capabilityread_test

import (
	"context"
	"encoding/json"
	"testing"

	natsserver "github.com/nats-io/nats-server/v2/server"
	natstest "github.com/nats-io/nats-server/v2/test"
	nats "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/jsstore"
	"github.com/operatinggraph/lattice/internal/refractor/capabilityread"
	"github.com/operatinggraph/lattice/internal/substrate"
)

func newTestKV(t *testing.T) *substrate.KV {
	t.Helper()
	opts := &natsserver.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: jsstore.Dir(t)}
	s := natstest.RunServer(opts)
	t.Cleanup(s.Shutdown)

	nc, err := nats.Connect(s.ClientURL())
	require.NoError(t, err)
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

// TestIsReadable_ListKeysFailure_PropagatesError pins the fail-closed *error*
// arm on the domain-slice discovery list: a KV-list failure (here, an
// already-canceled context — ListKeysFilter checks ctx.Err() after
// enumerating, substrate/kv.go:282) must surface as an error, not the silent
// "no domain slices" a swallowed error would produce.
func TestIsReadable_ListKeysFailure_PropagatesError(t *testing.T) {
	kv := newTestKV(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	readable, err := capabilityread.IsReadable(ctx, kv, "identity", "A1", "unitNano1")
	require.Error(t, err, "a list-keys failure must error, not silently deny as if no domain slices existed")
	require.False(t, readable)
	require.Contains(t, err.Error(), "list domain slices")
}
