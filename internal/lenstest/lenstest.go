// Package lenstest provides the fixture setup every package's lens_cypher_test.go
// rebuilds by hand: a fresh adjacency + core KV pair on a private embedded NATS
// server (internal/natsfixture.Server(t) — one instance per caller, so bucket
// names carry no cross-package meaning), and the deterministic NanoID
// derivation those fixtures seed vertex/link keys with.
package lenstest

import (
	"context"
	"testing"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/natsfixture"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// KVs returns fresh adjacency + core KV buckets on a private embedded NATS
// server, the pair every package's lens cypher tests build their world on.
func KVs(t *testing.T) (adjKV, coreKV *substrate.KV) {
	t.Helper()
	_, nc := natsfixture.Server(t)
	js, err := jetstream.New(nc)
	require.NoError(t, err)
	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	ctx := context.Background()
	_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "adj-cypher-test"})
	require.NoError(t, err)
	_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "core-cypher-test"})
	require.NoError(t, err)
	adjKV, err = conn.OpenKV(ctx, "adj-cypher-test")
	require.NoError(t, err)
	coreKV, err = conn.OpenKV(ctx, "core-cypher-test")
	require.NoError(t, err)
	return adjKV, coreKV
}

// NanoID returns a deterministic 20-char Contract #1 NanoID derived from a
// logical fixture name, so the same name always yields the same key across a
// test's whole world without a counter or randomness.
func NanoID(name string) string {
	alphabet := substrate.Alphabet
	var seed uint64 = 1469598103934665603
	for _, b := range []byte(name) {
		seed ^= uint64(b)
		seed *= 1099511628211
	}
	var out [20]byte
	for i := 0; i < 20; i++ {
		out[i] = alphabet[seed%uint64(len(alphabet))]
		seed = seed*1099511628211 + 0x9E3779B97F4A7C15
	}
	return string(out[:])
}
