package bootstrap_test

// VerifyKernel is the callable equivalent of `make verify-kernel` — the
// assertion set that catches a corrupted or partially-seeded kernel before
// anything downstream trusts it. It had no unit coverage of its own (only
// scripts/verify-kernel.go, run by hand against a live stack), so a defect
// in the assertions themselves could silently stop catching what it claims
// to catch. These tests seed a real kernel over an embedded server, confirm
// VerifyKernel passes it clean, then inject one defect at a time and pin
// the specific failure line each produces.

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
)

// seededKernelConn seeds a full primordial kernel over an embedded server
// and also plants the Health KV readiness signal that a live stack's
// refractor-stub writes once bootstrap completes — VerifyKernel checks for
// it (§5) even though the seeder itself never writes it.
func seededKernelConn(ctx context.Context, t *testing.T) *substrate.Conn {
	t.Helper()
	testutil.EnsurePrimordials(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	nc := startBootstrapNATS(t)
	seedFresh(t, nc, logger)
	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	_, err = conn.KVPut(ctx, bootstrap.HealthKVBucket, bootstrap.HealthBootstrapCompleteKey, []byte(`{"ready":true}`))
	require.NoError(t, err)
	return conn
}

// mutateEnvelope reads the envelope at key, applies fn to its decoded map,
// and writes it back — a realistic in-place corruption rather than a
// hand-built payload that might drift from the real envelope shape.
func mutateEnvelope(ctx context.Context, t *testing.T, conn *substrate.Conn, bucket, key string, fn func(env map[string]any)) {
	t.Helper()
	entry, err := conn.KVGet(ctx, bucket, key)
	require.NoError(t, err)
	var env map[string]any
	require.NoError(t, json.Unmarshal(entry.Value, &env))
	fn(env)
	raw, err := json.Marshal(env)
	require.NoError(t, err)
	_, err = conn.KVPut(ctx, bucket, key, raw)
	require.NoError(t, err)
}

func TestVerifyKernel_FreshlySeededPasses(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn := seededKernelConn(ctx, t)

	failures, _ := bootstrap.VerifyKernel(ctx, conn)
	require.Empty(t, failures, "a freshly seeded kernel must pass every assertion")
}

func TestVerifyKernel_DetectsMissingKey(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn := seededKernelConn(ctx, t)

	require.NoError(t, conn.KVPurge(ctx, bootstrap.CoreKVBucket, bootstrap.MetaRootKey))

	failures, _ := bootstrap.VerifyKernel(ctx, conn)
	require.NotEmpty(t, failures)
	require.Condition(t, containsSubstring(failures, "MISSING key: "+bootstrap.MetaRootKey))
}

func TestVerifyKernel_DetectsMissingAspect(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn := seededKernelConn(ctx, t)

	aspectKey := bootstrap.MetaRootKey + ".canonicalName"
	require.NoError(t, conn.KVPurge(ctx, bootstrap.CoreKVBucket, aspectKey))

	failures, _ := bootstrap.VerifyKernel(ctx, conn)
	require.NotEmpty(t, failures)
	require.Condition(t, containsSubstring(failures, "MISSING aspect: "+aspectKey))
}

func TestVerifyKernel_DetectsInvalidJSON(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn := seededKernelConn(ctx, t)

	_, err := conn.KVPut(ctx, bootstrap.CoreKVBucket, bootstrap.MetaRootKey, []byte("not json"))
	require.NoError(t, err)

	failures, _ := bootstrap.VerifyKernel(ctx, conn)
	require.NotEmpty(t, failures)
	require.Condition(t, containsSubstring(failures, "INVALID JSON for key "+bootstrap.MetaRootKey))
}

func TestVerifyKernel_DetectsTamperedIsDeleted(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn := seededKernelConn(ctx, t)

	mutateEnvelope(ctx, t, conn, bootstrap.CoreKVBucket, bootstrap.MetaRootKey, func(env map[string]any) {
		env["isDeleted"] = true
	})

	failures, _ := bootstrap.VerifyKernel(ctx, conn)
	require.NotEmpty(t, failures)
	require.Condition(t, containsSubstring(failures, "INVALID isDeleted for key "+bootstrap.MetaRootKey))
}

func TestVerifyKernel_DetectsWrongCreatedBy(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn := seededKernelConn(ctx, t)

	mutateEnvelope(ctx, t, conn, bootstrap.CoreKVBucket, bootstrap.MetaRootKey, func(env map[string]any) {
		env["createdBy"] = "vtx.identity.someimposter99999991"
	})

	failures, _ := bootstrap.VerifyKernel(ctx, conn)
	require.NotEmpty(t, failures)
	require.Condition(t, containsSubstring(failures, "WRONG createdBy for key "+bootstrap.MetaRootKey))
}

func TestVerifyKernel_DetectsAspectClassMismatch(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn := seededKernelConn(ctx, t)

	aspectKey := bootstrap.MetaRootKey + ".canonicalName"
	mutateEnvelope(ctx, t, conn, bootstrap.CoreKVBucket, aspectKey, func(env map[string]any) {
		env["class"] = "wrongClass"
	})

	failures, _ := bootstrap.VerifyKernel(ctx, conn)
	require.NotEmpty(t, failures)
	require.Condition(t, containsSubstring(failures, "CLASS MISMATCH for aspect "+aspectKey))
}

func TestVerifyKernel_DetectsStaleContent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn := seededKernelConn(ctx, t)

	// A stale script is present with a valid envelope — every presence/shape
	// assertion above this passes it clean. Only content comparison against
	// what this binary builds can see it: `make up`'s reuse short-circuit
	// calls VerifyKernel and must not treat a kernel like this as fresh.
	key := substrate.AspectKey(bootstrap.UpgradePackageDDLKey, "script")
	staleVal, err := bootstrap.MakeAspectEnvelope(key, bootstrap.UpgradePackageDDLKey, "script", "script",
		map[string]any{"source": "def execute(state, op):\n    fail(\"an older binary\")\n"})
	require.NoError(t, err)
	_, err = conn.KVPut(ctx, bootstrap.CoreKVBucket, key, staleVal)
	require.NoError(t, err)

	failures, _ := bootstrap.VerifyKernel(ctx, conn)
	require.NotEmpty(t, failures)
	require.Condition(t, containsSubstring(failures, "KERNEL ENTRY STALE: "+key))
}

func TestVerifyKernel_DetectsMissingHealthReadinessSignal(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn := seededKernelConn(ctx, t)

	require.NoError(t, conn.KVPurge(ctx, bootstrap.HealthKVBucket, bootstrap.HealthBootstrapCompleteKey))

	failures, _ := bootstrap.VerifyKernel(ctx, conn)
	require.NotEmpty(t, failures)
	require.Condition(t, containsSubstring(failures, "MISSING Health KV readiness signal"))
}

func TestVerifyKernel_DetectsMissingKVBucket(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn := seededKernelConn(ctx, t)

	require.NoError(t, conn.JetStream().DeleteKeyValue(ctx, bootstrap.LoomStateBucket))

	failures, _ := bootstrap.VerifyKernel(ctx, conn)
	require.NotEmpty(t, failures)
	require.Condition(t, containsSubstring(failures, "MISSING KV bucket: "+bootstrap.LoomStateBucket))
}

func TestVerifyKernel_ReportsKernelOrphanAsNoticeNotFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn := seededKernelConn(ctx, t)

	id, err := substrate.NewNanoID()
	require.NoError(t, err)
	orphan := substrate.VertexKey("meta", id)
	env, err := bootstrap.MakeVertexEnvelope(orphan, "meta.ddl", nil)
	require.NoError(t, err)
	_, err = conn.KVPut(ctx, bootstrap.CoreKVBucket, orphan, env)
	require.NoError(t, err)

	failures, notices := bootstrap.VerifyKernel(ctx, conn)
	require.Empty(t, failures, "a kernel orphan must never fail verify-kernel (D1 — informational only)")
	require.Condition(t, containsSubstring(notices, "KERNEL ENTITY ORPHANED: "+orphan))
}

// seedPriorEpochOperatorRole plants the residue an id-file rotation leaves
// behind: an `operator` role whose id this deployment's primordial table does
// not name, still held by that prior epoch's own admin identity — the
// create-only re-seed deletes nothing, so the identity and its holdsRole edge
// survive alongside the role — and carrying `grants` live grantedBy edges. Both
// ends of every edge are written, because the scan counts an edge only when the
// documents at both ends are live. The freshly seeded kernel beside it supplies
// the current epoch, so the fixture is the two-epoch bucket the detector is
// about. It returns the stranded role's key and its prior-epoch holder's key.
func seedPriorEpochOperatorRole(ctx context.Context, t *testing.T, conn *substrate.Conn, grants int) (roleKey, holderKey string) {
	t.Helper()
	put := func(key string, raw []byte, err error) {
		t.Helper()
		require.NoError(t, err)
		_, putErr := conn.KVPut(ctx, bootstrap.CoreKVBucket, key, raw)
		require.NoError(t, putErr)
	}
	nanoID := func() string {
		t.Helper()
		id, err := substrate.NewNanoID()
		require.NoError(t, err)
		return id
	}

	roleID := nanoID()
	roleKey = substrate.VertexKey("role", roleID)
	roleVal, roleErr := bootstrap.MakeVertexEnvelope(roleKey, "role", map[string]any{"protected": true})
	put(roleKey, roleVal, roleErr)

	cnKey := substrate.AspectKey(roleKey, "canonicalName")
	cnVal, cnErr := bootstrap.MakeAspectEnvelope(cnKey, roleKey, "canonicalName", "canonicalName",
		map[string]any{"value": "operator"})
	put(cnKey, cnVal, cnErr)

	priorAdminID := nanoID()
	holderKey = substrate.VertexKey("identity", priorAdminID)
	adminVal, adminErr := bootstrap.MakeVertexEnvelope(holderKey, "identity", nil)
	put(holderKey, adminVal, adminErr)

	holdsRoleKey := substrate.LinkKey("identity", priorAdminID, "holdsRole", "role", roleID)
	holdsRoleVal, holdsRoleErr := bootstrap.MakeLinkEnvelope(holdsRoleKey, holderKey, roleKey,
		"holdsRole", "link.holdsRole", nil)
	put(holdsRoleKey, holdsRoleVal, holdsRoleErr)

	for range grants {
		permID := nanoID()
		permKey := substrate.VertexKey("permission", permID)
		permVal, permErr := bootstrap.MakeVertexEnvelope(permKey, "permission",
			map[string]any{"operationType": "AttachObject"})
		put(permKey, permVal, permErr)

		linkKey := substrate.LinkKey("permission", permID, "grantedBy", "role", roleID)
		linkVal, linkErr := bootstrap.MakeLinkEnvelope(linkKey, permKey, roleKey,
			"grantedBy", "link.grantedBy", nil)
		put(linkKey, linkVal, linkErr)
	}
	return roleKey, holderKey
}

// seedPriorEpochCapabilityLens plants a stranded capability lens: a
// vtx.meta.* vertex of class meta.lens named by one of the four reserved
// canonicalNames, carrying cypherRule. divergedCypher seeds a rule that
// differs from the current definition's — the dangerous case — when true;
// the current, identical rule — the inert case — when false.
func seedPriorEpochCapabilityLens(ctx context.Context, t *testing.T, conn *substrate.Conn, canonicalName string, divergedCypher bool) (lensKey string) {
	t.Helper()
	put := func(key string, raw []byte, err error) {
		t.Helper()
		require.NoError(t, err)
		_, putErr := conn.KVPut(ctx, bootstrap.CoreKVBucket, key, raw)
		require.NoError(t, putErr)
	}
	lensID, err := substrate.NewNanoID()
	require.NoError(t, err)
	lensKey = substrate.VertexKey("meta", lensID)
	lensVal, lensErr := bootstrap.MakeVertexEnvelope(lensKey, "meta.lens", map[string]any{"protected": true})
	put(lensKey, lensVal, lensErr)

	cnKey := substrate.AspectKey(lensKey, "canonicalName")
	cnVal, cnErr := bootstrap.MakeAspectEnvelope(cnKey, lensKey, "canonicalName", "canonicalName",
		map[string]any{"value": canonicalName})
	put(cnKey, cnVal, cnErr)

	rule := "MATCH (identity:identity) WHERE identity.data.protected = true RETURN identity.id AS actor_id"
	if !divergedCypher {
		rule = bootstrap.CapabilityReadWildcardGrantsLensDefinition().CypherRule
	}
	crKey := substrate.AspectKey(lensKey, "cypherRule")
	crVal, crErr := bootstrap.MakeAspectEnvelope(crKey, lensKey, "cypherRule", "cypherRule",
		map[string]any{"rule": rule})
	put(crKey, crVal, crErr)
	return lensKey
}

// TestVerifyKernel_StrandedCapabilityLensReturnsNoFailures pins the same
// property TestVerifyKernel_StrandedEpochReturnsNoFailures pins for the role
// plane, over the lens plane: EVEN a cypher-diverged stranded lens — the
// dangerous case scripts/verify-kernel.go escalates to a failure — must
// never fail VerifyKernel itself, because `make up`'s FRESH oracle reads
// this function's exit code and cannot repair a protected, un-tombstonable
// lens by discarding and re-minting the id file — it would only strand a
// second epoch on top of the first.
func TestVerifyKernel_StrandedCapabilityLensReturnsNoFailures(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn := seededKernelConn(ctx, t)

	lensKey := seedPriorEpochCapabilityLens(ctx, t, conn, "capabilityReadWildcardGrants", true)

	failures, notices := bootstrap.VerifyKernel(ctx, conn)
	require.Empty(t, failures,
		"a stranded capability lens — even a cypher-diverged one — must never fail bootstrap verify")
	require.Condition(t, containsSubstring(notices, "STRANDED CAPABILITY LENS: "+lensKey))
}

// TestVerifyKernel_StrandedEpochReturnsNoFailures is the property `make up`
// depends on, and it must not regress silently.
//
// cmd/lattice/bootstrap exits 1 on any failure this function returns
// (bootstrap.go:134,165) and Makefile:202 uses that exit code as the freshness
// oracle. A failure here sends `make up` down the mismatch path, where
// `bootstrap probe-empty` exits 1 on a populated bucket and the recipe deletes
// lattice.bootstrap.json and mints a fresh primordial set (Makefile:211-214) —
// stranding the current epoch too, once per invocation, with this output
// discarded to /dev/null. Firing on the condition it detects would make the
// detector self-amplifying.
func TestVerifyKernel_StrandedEpochReturnsNoFailures(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn := seededKernelConn(ctx, t)

	roleKey, _ := seedPriorEpochOperatorRole(ctx, t, conn, 2)

	failures, notices := bootstrap.VerifyKernel(ctx, conn)
	require.Empty(t, failures,
		"a stranded operator role must never fail bootstrap verify — `make up` reads that exit code")
	require.Condition(t, containsSubstring(notices, "STRANDED OPERATOR ROLE: "+roleKey))
}

// TestVerifyKernel_StrandedEpochWithoutGrantsIsStillReported pins that the
// benign-looking half is not silence either: a role with holders and no grants
// is the state the wildcard read-grant lens projects root read for, so it is
// exactly the one that must not be dropped for having an empty grant list.
func TestVerifyKernel_StrandedEpochWithoutGrantsIsStillReported(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn := seededKernelConn(ctx, t)

	roleKey, _ := seedPriorEpochOperatorRole(ctx, t, conn, 0)

	failures, notices := bootstrap.VerifyKernel(ctx, conn)
	require.Empty(t, failures)
	require.Condition(t, containsSubstring(notices, "STRANDED OPERATOR ROLE: "+roleKey))
}

// TestVerifyKernel_StrandedEpochNoticeNamesItsHolders pins that the notice
// carries the keys an operator acts on. A bare count would send the reader to
// Loupe to discover what to revoke, and under the holder-keyed severity rule
// the holder keys ARE the actionable item — each names an edge to tombstone.
func TestVerifyKernel_StrandedEpochNoticeNamesItsHolders(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn := seededKernelConn(ctx, t)

	roleKey, holder := seedPriorEpochOperatorRole(ctx, t, conn, 1)

	_, notices := bootstrap.VerifyKernel(ctx, conn)
	require.Condition(t, containsSubstring(notices, holder),
		"the notice must name the holding identity, not just count it")
	require.Condition(t, containsSubstring(notices, roleKey))
}

func containsSubstring(haystack []string, want string) func() bool {
	return func() bool {
		for _, s := range haystack {
			if strings.Contains(s, want) {
				return true
			}
		}
		return false
	}
}

func TestInspectKernel_ReturnsSeededEntries(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn := seededKernelConn(ctx, t)

	entries, err := bootstrap.InspectKernel(ctx, conn)
	require.NoError(t, err)
	require.NotEmpty(t, entries)
	for _, e := range entries {
		require.False(t, e.Missing, "key %s: freshly seeded kernel must report every primordial key present", e.Key)
		require.NotNil(t, e.Doc, "key %s: present entry must decode a document", e.Key)
	}
}

func TestInspectKernel_ReportsMissingKey(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn := seededKernelConn(ctx, t)

	require.NoError(t, conn.KVPurge(ctx, bootstrap.CoreKVBucket, bootstrap.BootstrapIdentityKey))

	entries, err := bootstrap.InspectKernel(ctx, conn)
	require.NoError(t, err)
	found := false
	for _, e := range entries {
		if e.Key == bootstrap.BootstrapIdentityKey {
			found = true
			require.True(t, e.Missing)
			require.Nil(t, e.Doc)
		}
	}
	require.True(t, found, "InspectKernel must still report the missing key, marked Missing")
}
