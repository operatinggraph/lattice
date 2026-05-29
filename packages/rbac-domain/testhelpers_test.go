// Shared test helpers for the rbac-domain Capability Package's external
// test suite.
//
// External test package, real install, real Capability authorizer,
// seeded staff + consumer cap docs.
package rbacdomain_test

import (
	"context"
	"testing"
	"time"

	"github.com/asolgan/lattice/internal/bootstrap"
	"github.com/asolgan/lattice/internal/processor"
	"github.com/asolgan/lattice/internal/substrate"
	"github.com/asolgan/lattice/internal/testutil"
)

// Test actor NanoIDs. 20 chars, substrate.Alphabet only.
const (
	rmOperatorActorID  = "RmPActrXzBbCdEfGHJKM" // 20 chars
	rmConsumerActorID  = "RmCnsrXzBbCdEfGHJKMN" // 20 chars
	rmOperatorActorKey = "vtx.identity." + rmOperatorActorID
	rmConsumerActorKey = "vtx.identity." + rmConsumerActorID
	rmOperatorCapKey   = "cap.identity." + rmOperatorActorID
	rmConsumerCapKey   = "cap.identity." + rmConsumerActorID

	// Target role NanoID used by AssignRole / RevokeRole tests. Production
	// would mint this via CreateRole; tests use a fixed ID and pre-seed
	// the role vertex via the rbac DDL itself in each test.
	rmTargetRoleID  = "RmTrgtRReXzBbCdEfGhi"
	rmTargetRoleKey = "vtx.role." + rmTargetRoleID
)

// operatorCapDoc builds a cap doc granting all 10 rbac operations.
func operatorCapDoc() *processor.CapabilityDoc {
	perms := []processor.PlatformPermission{
		{OperationType: "CreateRole", Scope: "any"},
		{OperationType: "UpdateRole", Scope: "any"},
		{OperationType: "TombstoneRole", Scope: "any"},
		{OperationType: "CreatePermission", Scope: "any"},
		{OperationType: "UpdatePermission", Scope: "any"},
		{OperationType: "TombstonePermission", Scope: "any"},
		{OperationType: "AssignRole", Scope: "any"},
		{OperationType: "RevokeRole", Scope: "any"},
		{OperationType: "GrantPermission", Scope: "any"},
		{OperationType: "RevokePermission", Scope: "any"},
	}
	now := time.Now().UTC()
	return &processor.CapabilityDoc{
		Key:                    rmOperatorCapKey,
		Actor:                  rmOperatorActorKey,
		Version:                "1.0",
		ProjectedAt:            now.Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{rmOperatorActorKey: 1},
		Lanes:                  []string{"default"},
		PlatformPermissions:    perms,
		ServiceAccess:          []processor.ServiceAccessEntry{},
		EphemeralGrants:        []processor.EphemeralGrant{},
		Roles:                  []string{bootstrap.RoleOperatorKey},
	}
}

// consumerCapDoc builds a cap doc with no rbac permissions.
func consumerCapDoc() *processor.CapabilityDoc {
	now := time.Now().UTC()
	return &processor.CapabilityDoc{
		Key:                    rmConsumerCapKey,
		Actor:                  rmConsumerActorKey,
		Version:                "1.0",
		ProjectedAt:            now.Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{rmConsumerActorKey: 1},
		Lanes:                  []string{"default"},
		PlatformPermissions:    []processor.PlatformPermission{},
		ServiceAccess:          []processor.ServiceAccessEntry{},
		EphemeralGrants:        []processor.EphemeralGrant{},
		Roles:                  []string{"vtx.role.consumer"},
	}
}

// setupTestEnv assembles the standard rbac-domain test environment.
func setupTestEnv(t *testing.T) (context.Context, *substrate.Conn) {
	t.Helper()
	ctx, conn := testutil.SetupPackageTestEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, operatorCapDoc())
	testutil.SeedCapDoc(t, ctx, conn, consumerCapDoc())
	return ctx, conn
}
