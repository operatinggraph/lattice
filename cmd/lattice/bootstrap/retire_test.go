package bootstrap

// Integration coverage for linkIsLive + submitRevocation against a real,
// running Processor (mirrors cmd/lattice/op/op_test.go's setupOpEnv shape —
// testutil.SetupPackageTestEnv alone stands up no live ops.default consumer,
// so a business-op submission needs its own testutil.CapabilityPipeline
// pumped inline). This is the piece PlanStrandedEpochRetirement's own unit
// tests (internal/bootstrap) cannot reach, since that package stays free of
// any NATS dependency. An ordinary role/permission exercises the exact same
// RevokeRole/RevokePermission dispatch path a stranded operator role would;
// nothing here needs a role literally named "operator" — CreateRole's own
// reserved-name guard would refuse minting a second one, by design, even in
// a test.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/cmd/lattice/output"
	internalbootstrap "github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
)

func coreKV(t *testing.T, ctx context.Context, conn *substrate.Conn) jetstream.KeyValue {
	t.Helper()
	kv, err := conn.JetStream().KeyValue(ctx, internalbootstrap.CoreKVBucket)
	require.NoError(t, err)
	return kv
}

// setupRetireEnv seeds a capability document granting the bootstrap admin
// identity the RBAC ops this file's tests submit, then drives a live
// CommitPath consumer on ops.default inline — the same shape
// cmd/lattice/op/op_test.go's setupOpEnv uses for testing a real
// submit-and-wait-for-reply round trip.
func setupRetireEnv(t *testing.T, durable string) (context.Context, *substrate.Conn, string) {
	t.Helper()
	ctx, conn := testutil.SetupPackageTestEnv(t)

	actorKey := substrate.VertexKey("identity", internalbootstrap.BootstrapIdentityID)
	now := time.Now().UTC()
	testutil.SeedCapDoc(t, ctx, conn, &processor.CapabilityDoc{
		Key:                    "cap.identity." + internalbootstrap.BootstrapIdentityID,
		Actor:                  actorKey,
		Version:                "1.0",
		ProjectedAt:            now.Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{actorKey: 1},
		Lanes:                  []string{"default"},
		PlatformPermissions: []processor.PlatformPermission{
			{OperationType: "CreateRole", Scope: "any"},
			{OperationType: "AssignRole", Scope: "any"},
			{OperationType: "RevokeRole", Scope: "any"},
			{OperationType: "CreatePermission", Scope: "any"},
			{OperationType: "GrantPermission", Scope: "any"},
			{OperationType: "RevokePermission", Scope: "any"},
		},
		ServiceAccess:   []processor.ServiceAccessEntry{},
		EphemeralGrants: []processor.EphemeralGrant{},
		Roles:           []string{internalbootstrap.RoleOperatorKey},
	})

	cp, cons := testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{
		Durable:        durable,
		Instance:       durable,
		FilterSubjects: []string{"ops.default"},
	})
	cc, err := cons.Consume(func(m jetstream.Msg) { cp.HandleMessage(ctx, m) })
	require.NoError(t, err)
	t.Cleanup(cc.Stop)

	return ctx, conn, actorKey
}

// submitRBAC submits opType with payload as actorKey and fails the test on
// any rejection.
// submitRBAC submits opType with payload as actorKey, declaring reads exactly
// as packages/rbac-domain/ddls.go's own doc comment specifies per op type
// (Contract #2 §2.5's declared-read posture — an undeclared key an op script
// reads is a correctness error, not a style preference). CreateRole /
// CreatePermission need no declared reads. AssignRole / GrantPermission read
// both endpoint vertices — required, since this helper's callers always
// create them first — via `reads`, plus the deterministic link key via
// `optionalReads`: grant_link's idempotent-grant check reads that key
// specifically to learn whether it is absent (first grant), tombstoned
// (revive), or alive (no-op), so its own absence must never be a hydration
// error — the read-before-create/dedup case CLAUDE.md's OptionalReads exists
// for. Fails the test on any rejection.
func submitRBAC(t *testing.T, ctx context.Context, conn *substrate.Conn, actorKey, opType string, payload map[string]any, reads, optionalReads []string) *processor.OperationReply {
	t.Helper()
	body, err := json.Marshal(payload)
	require.NoError(t, err)
	requestID, err := substrate.NewNanoID()
	require.NoError(t, err)
	env := processor.OperationEnvelope{
		RequestID:     requestID,
		Lane:          processor.LaneDefault,
		OperationType: opType,
		Actor:         actorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Payload:       body,
	}
	if len(reads) > 0 || len(optionalReads) > 0 {
		env.ContextHint = &processor.ContextHint{Reads: reads, OptionalReads: optionalReads}
	}
	ctx, cancel := context.WithTimeout(ctx, retireOpReplyTimeout)
	defer cancel()
	reply, err := output.SubmitOp(ctx, conn, &env)
	require.NoError(t, err)
	require.NotEqual(t, processor.ReplyStatusRejected, reply.Status, "rejected: %+v", reply.Error)
	return reply
}

func TestSubmitRevocation_RevokesALiveRoleAssignmentThroughTheRealProcessor(t *testing.T) {
	ctx, conn, actorKey := setupRetireEnv(t, "retire-test-role")
	kv := coreKV(t, ctx, conn)

	roleReply := submitRBAC(t, ctx, conn, actorKey, "CreateRole", map[string]any{"name": "retire-test-role"}, nil, nil)
	roleKey := roleReply.PrimaryKey
	_, roleID, ok := substrate.ParseVertexKey(roleKey)
	require.True(t, ok)
	linkKey := substrate.LinkKey("identity", internalbootstrap.BootstrapIdentityID, "holdsRole", "role", roleID)
	submitRBAC(t, ctx, conn, actorKey, "AssignRole", map[string]any{"actorKey": actorKey, "roleKey": roleKey},
		[]string{actorKey, roleKey}, []string{linkKey})

	live, err := linkIsLive(ctx, kv, linkKey)
	require.NoError(t, err)
	require.True(t, live, "AssignRole must have created a live holdsRole edge")

	op := internalbootstrap.RevocationOp{
		OperationType: "RevokeRole",
		Payload:       map[string]any{"actorKey": actorKey, "roleKey": roleKey},
		LinkKey:       linkKey,
	}
	require.NoError(t, submitRevocation(conn, actorKey, op))

	live, err = linkIsLive(ctx, kv, linkKey)
	require.NoError(t, err)
	require.False(t, live, "submitRevocation must have tombstoned the holdsRole edge")
}

func TestSubmitRevocation_RevokesALiveGrantThroughTheRealProcessor(t *testing.T) {
	ctx, conn, actorKey := setupRetireEnv(t, "retire-test-grant")
	kv := coreKV(t, ctx, conn)

	roleReply := submitRBAC(t, ctx, conn, actorKey, "CreateRole", map[string]any{"name": "retire-test-role-2"}, nil, nil)
	roleKey := roleReply.PrimaryKey
	permReply := submitRBAC(t, ctx, conn, actorKey, "CreatePermission", map[string]any{"operationType": "SomeTestOp", "scope": "any"}, nil, nil)
	permKey := permReply.PrimaryKey

	_, roleID, ok := substrate.ParseVertexKey(roleKey)
	require.True(t, ok)
	_, permID, ok := substrate.ParseVertexKey(permKey)
	require.True(t, ok)
	linkKey := substrate.LinkKey("permission", permID, "grantedBy", "role", roleID)
	submitRBAC(t, ctx, conn, actorKey, "GrantPermission", map[string]any{"permKey": permKey, "roleKey": roleKey},
		[]string{permKey, roleKey}, []string{linkKey})

	live, err := linkIsLive(ctx, kv, linkKey)
	require.NoError(t, err)
	require.True(t, live, "GrantPermission must have created a live grantedBy edge")

	op := internalbootstrap.RevocationOp{
		OperationType: "RevokePermission",
		Payload:       map[string]any{"permKey": permKey, "roleKey": roleKey},
		LinkKey:       linkKey,
	}
	require.NoError(t, submitRevocation(conn, actorKey, op))

	live, err = linkIsLive(ctx, kv, linkKey)
	require.NoError(t, err)
	require.False(t, live, "submitRevocation must have tombstoned the grantedBy edge")

	name, ok, err := internalbootstrap.PermissionDeclaredBy(ctx, kv, permKey)
	require.NoError(t, err)
	require.False(t, ok, "a runtime-minted permission carries no declaredBy: %q", name)
}
