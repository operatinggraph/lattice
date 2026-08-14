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

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
	rbacdomain "github.com/operatinggraph/lattice/packages/rbac-domain"
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

// operatorCapDoc builds the operator's cap doc from the package's OWN
// PermissionSpecs — the same set `capabilityRoles` projects from the installed
// permission vertices and their `grantedBy` links.
//
// It is derived rather than listed because a hand-written list is a second
// source of truth for the grant set, and the direction it fails in is the
// dangerous one: an op the package does NOT grant would still appear here, so
// every denial test in this suite would exercise an actor the real projection
// never produces and pass while the platform was wide open. Withdrawing a
// PermissionSpec must show up at step 3 in this suite with no fixture edit.
func operatorCapDoc() *processor.CapabilityDoc {
	perms := make([]processor.PlatformPermission, 0, len(rbacdomain.Package.Permissions))
	for _, p := range rbacdomain.Package.Permissions {
		perms = append(perms, processor.PlatformPermission{
			OperationType: p.OperationType,
			Scope:         p.Scope,
		})
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
