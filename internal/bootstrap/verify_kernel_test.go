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
// behind: an `operator` role from a bootstrap epoch other than the one this
// kernel was seeded from, held by nobody, carrying `grants` live grantedBy
// edges. The freshly seeded kernel beside it supplies the current epoch's role,
// so the fixture is exactly the two-epoch bucket the detector is about.
func seedPriorEpochOperatorRole(ctx context.Context, t *testing.T, conn *substrate.Conn, grants int) string {
	t.Helper()
	roleID, err := substrate.NewNanoID()
	require.NoError(t, err)
	roleKey := substrate.VertexKey("role", roleID)

	roleVal, err := bootstrap.MakeVertexEnvelope(roleKey, "role", map[string]any{"protected": true})
	require.NoError(t, err)
	_, err = conn.KVPut(ctx, bootstrap.CoreKVBucket, roleKey, roleVal)
	require.NoError(t, err)

	cnKey := substrate.AspectKey(roleKey, "canonicalName")
	cnVal, err := bootstrap.MakeAspectEnvelope(cnKey, roleKey, "canonicalName", "canonicalName",
		map[string]any{"value": "operator"})
	require.NoError(t, err)
	_, err = conn.KVPut(ctx, bootstrap.CoreKVBucket, cnKey, cnVal)
	require.NoError(t, err)

	for range grants {
		permID, idErr := substrate.NewNanoID()
		require.NoError(t, idErr)
		linkKey := substrate.LinkKey("permission", permID, "grantedBy", "role", roleID)
		linkVal, linkErr := bootstrap.MakeLinkEnvelope(linkKey, substrate.VertexKey("permission", permID),
			roleKey, "grantedBy", "link.grantedBy", nil)
		require.NoError(t, linkErr)
		_, err = conn.KVPut(ctx, bootstrap.CoreKVBucket, linkKey, linkVal)
		require.NoError(t, err)
	}
	return roleKey
}

// TestVerifyKernel_StrandedEpochWithGrantsFails is where the false green ends:
// a prior epoch's operator role still conferring live grants is authority no
// identity can reach, so it moves this gate's exit status rather than sitting
// in the notices a 30-line output scrolls past.
func TestVerifyKernel_StrandedEpochWithGrantsFails(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn := seededKernelConn(ctx, t)

	roleKey := seedPriorEpochOperatorRole(ctx, t, conn, 2)

	failures, _ := bootstrap.VerifyKernel(ctx, conn)
	require.Condition(t, containsSubstring(failures, "STRANDED OPERATOR ROLE: "+roleKey))
	require.Condition(t, containsSubstring(failures, "2 live grant(s)"))
}

// TestVerifyKernel_StrandedEpochWithoutGrantsIsNotice is the other half of the
// §4 split: the same stranded role with nothing left to confer is dead weight,
// and dead weight does not redden a gate.
func TestVerifyKernel_StrandedEpochWithoutGrantsIsNotice(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn := seededKernelConn(ctx, t)

	roleKey := seedPriorEpochOperatorRole(ctx, t, conn, 0)

	failures, notices := bootstrap.VerifyKernel(ctx, conn)
	require.Empty(t, failures, "a stranded role conferring nothing must not fail the gate")
	require.Condition(t, containsSubstring(notices, "STRANDED OPERATOR ROLE: "+roleKey))
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
