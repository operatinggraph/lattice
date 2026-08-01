package full

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestSeedNodes_LabeledPrefixScanFindsOnlyThatType proves the label-scoped
// generic seed scan (executor.seedNodes) returns exactly the vertices of the
// pattern's own type, seeded alongside several other types plus an aspect:
//   - a vertex of a different type must not appear (the prefix bounds the
//     listing to the pattern's own type);
//   - an aspect key of the SAME type (vtx.<label>.<id>.<localName>) shares
//     the literal "vtx.<label>." string prefix ListKeysPrefix lists under,
//     but is 4 segments, not 3 — the ClassifyKey shape filter must still
//     drop it.
func TestSeedNodes_LabeledPrefixScanFindsOnlyThatType(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	_, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	alice := putVertex(t, reg, coreKV, "alice", "identity", nil)
	bob := putVertex(t, reg, coreKV, "bob", "identity", nil)
	putVertex(t, reg, coreKV, "admin", "role", nil)
	putVertex(t, reg, coreKV, "svc1", "service", nil)
	// Shares the "vtx.identity." string prefix with alice's vertex key but
	// is a 4-segment aspect key, not a vertex.
	putAspect(t, reg, coreKV, "alice", "name", map[string]any{"value": "Alice"})

	ex := newTestExecutor(nil, coreKV)
	refs, err := ex.seedNodes(binding{}, NodePattern{Label: "identity"})
	require.NoError(t, err)

	got := make(map[string]bool, len(refs))
	for _, r := range refs {
		got[r.key] = true
	}
	require.Lenf(t, refs, 2, "expected exactly the two identity vertices, got %v", got)
	require.True(t, got[alice], "missing %s among seeded refs %v", alice, got)
	require.True(t, got[bob], "missing %s among seeded refs %v", bob, got)
}

// TestSeedNodes_LabeledPatternNeverConsultsFullBucketListing proves a
// labeled seed scan lists only the "vtx.<label>." prefix and never falls
// back to (or additionally performs) a whole-bucket ListKeys.
//
// The proof uses a poison key: a vertex-shaped Core KV key of a DIFFERENT
// type whose stored body is not valid JSON. seedNodes' generic path
// unmarshals every candidate key's body (via fetchNode) before checking
// whether it matches the pattern's label, so any scan that enumerates this
// key fails with an unmarshal error. A prefix-scoped listing for an
// unrelated label never enumerates it — proven by the clean pass below —
// while the unlabeled whole-bucket path still does, which the second half
// of this test pins so the first half's clean pass is a real scoping proof
// rather than a vacuous one (i.e. that the poison key is actually reachable
// by SOME path).
func TestSeedNodes_LabeledPatternNeverConsultsFullBucketListing(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	_, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	alice := putVertex(t, reg, coreKV, "alice", "identity", nil)

	poisonKey := "vtx.role." + c1NanoID("poisonRole")
	_, err := coreKV.Put(context.Background(), poisonKey, []byte("not valid json"))
	require.NoError(t, err)

	ex := newTestExecutor(nil, coreKV)
	refs, err := ex.seedNodes(binding{}, NodePattern{Label: "identity"})
	require.NoError(t, err,
		"a labeled seed scan must never reach a same-bucket key of a different type")
	require.Len(t, refs, 1)
	require.Equal(t, alice, refs[0].key)

	ex2 := newTestExecutor(nil, coreKV)
	_, err = ex2.seedNodes(binding{}, NodePattern{})
	require.Error(t, err,
		"an unlabeled seed scan still lists the whole bucket and must trip over the poison key")
}
