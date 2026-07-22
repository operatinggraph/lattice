// Install-flow integration test for the location-domain Capability Package.
//
// Proves the SL.1 install guarantees:
//
//  1. Co-installs cleanly alongside identity-domain (which itself depends on
//     rbac-domain) — the `location` DDL canonical name does NOT collide with
//     identity-domain's `identity` DDL or any of its aspect-type DDLs, and both
//     packages' declared keys land in one keyspace.
//  2. The four location permission vertices + their grantedBy→operator links
//     commit, and the `location` class becomes usable on the same running
//     Processor (in-commit DDL-cache invalidation, no restart).
package locationdomain_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/pkgmgr"
	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
	identitydomain "github.com/operatinggraph/lattice/packages/identity-domain"
	locationdomain "github.com/operatinggraph/lattice/packages/location-domain"
	rbacdomain "github.com/operatinggraph/lattice/packages/rbac-domain"
)

// TestInstallFlow_CoInstallWithIdentityDomain installs rbac-domain,
// identity-domain, and location-domain through the real meta-install pipeline
// onto one keyspace, then asserts:
//   - both DDLs (identity + location) are present with distinct canonical names;
//   - location-domain's four ops each have a permission vertex granted to operator;
//   - the just-declared `location` class is usable (a CreateLocation commits) on
//     the same Processor with no restart.
func TestInstallFlow_CoInstallWithIdentityDomain(t *testing.T) {
	url := testutil.StartEmbeddedNATS(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	t.Cleanup(cancel)
	conn, err := substrate.Connect(ctx, substrate.ConnectOpts{URL: url, Name: "loc-install-flow"})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(conn.Close)
	testutil.ProvisionHarness(t, ctx, conn)

	testutil.EnsurePrimordials(t)
	seeder, err := bootstrap.NewSeeder(conn.NATS(), testutil.TestLogger())
	if err != nil {
		t.Fatalf("bootstrap.NewSeeder: %v", err)
	}
	if err := seeder.SeedPrimordial(ctx); err != nil {
		t.Fatalf("bootstrap.SeedPrimordial: %v", err)
	}

	stop := testutil.RunMetaInstallPipeline(t, ctx, conn)
	defer stop()

	inst := pkgmgr.NewInstaller(conn, bootstrap.BootstrapIdentityKey)
	inst.RoleIDs = map[string]string{"operator": bootstrap.RoleOperatorID}

	if _, err := inst.Install(ctx, rbacdomain.Package); err != nil {
		t.Fatalf("install rbac-domain: %v", err)
	}
	idRes, err := inst.Install(ctx, identitydomain.Package)
	if err != nil {
		t.Fatalf("install identity-domain: %v", err)
	}
	locRes, err := inst.Install(ctx, locationdomain.Package)
	if err != nil {
		t.Fatalf("install location-domain: %v", err)
	}
	if len(locRes.DeclaredKeys) == 0 {
		t.Fatal("location-domain install declared no keys")
	}

	// No canonical-name collision: identity-domain declares an `identity` DDL +
	// several aspect-type DDLs; location-domain declares a `location` DDL. The
	// two declared-key sets must be disjoint (a collision would have made the
	// second install clobber or fail).
	idKeys := map[string]struct{}{}
	for _, k := range idRes.DeclaredKeys {
		idKeys[k] = struct{}{}
	}
	for _, k := range locRes.DeclaredKeys {
		if _, dup := idKeys[k]; dup {
			t.Fatalf("declared-key collision between identity-domain and location-domain: %s", k)
		}
	}

	// Both DDL classes resolve in a freshly-refreshed cache, by their distinct
	// canonical names.
	cache := processor.NewDDLCache(conn, testutil.HarnessCoreBucket, testutil.TestLogger())
	if err := cache.Refresh(ctx); err != nil {
		t.Fatalf("cache refresh: %v", err)
	}
	if _, ok := cache.Lookup("identity"); !ok {
		t.Fatal("identity DDL class not resolvable after co-install")
	}
	if _, ok := cache.Lookup("location"); !ok {
		t.Fatal("location DDL class not resolvable after co-install")
	}

	// The four location permission vertices each landed, granted to operator.
	assertLocationPermissionsLanded(t, ctx, conn)

	// The `location` class is usable on the same Processor — submit a
	// CreateLocation and assert it commits (a tracker materializes).
	capDoc := staffCapDoc()
	testutil.SeedCapDoc(t, ctx, conn, capDoc)
	cp, cons := newLocationPipeline(t, ctx, conn, "install-flow-create")
	reqID := testutil.GenReqID("ifCreate001")
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreateLocation",
		Actor:         locStaffActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "location",
		Payload:       json.RawMessage(`{"locationType":"property"}`),
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	if _, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, processor.TrackerKey(reqID)); err != nil {
		t.Fatalf("CreateLocation on the just-installed class did not commit (no tracker): %v", err)
	}
}

// assertLocationPermissionsLanded scans Core KV for a permission vertex per
// location op + its grantedBy→operator link.
func assertLocationPermissionsLanded(t *testing.T, ctx context.Context, conn *substrate.Conn) {
	t.Helper()
	operatorRoleID := bootstrap.RoleOperatorID
	if operatorRoleID == "" {
		t.Fatal("bootstrap.RoleOperatorID is empty; cannot verify grant links")
	}
	keys, err := conn.KVListKeys(ctx, testutil.HarnessCoreBucket)
	if err != nil {
		t.Fatalf("list keys: %v", err)
	}
	permIDByOp := map[string]string{}
	for _, key := range keys {
		if !strings.HasPrefix(key, "vtx.permission.") || strings.Count(key, ".") != 2 {
			continue
		}
		entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, key)
		if err != nil {
			continue
		}
		var doc map[string]any
		if json.Unmarshal(entry.Value, &doc) != nil {
			continue
		}
		if del, _ := doc["isDeleted"].(bool); del {
			continue
		}
		data, _ := doc["data"].(map[string]any)
		opType, _ := data["operationType"].(string)
		for _, op := range locationOps {
			if opType == op {
				permIDByOp[op] = strings.TrimPrefix(key, "vtx.permission.")
			}
		}
	}
	for _, op := range locationOps {
		permID, ok := permIDByOp[op]
		if !ok {
			t.Fatalf("location permission vertex for %q not found", op)
		}
		lnk := "lnk.permission." + permID + ".grantedBy.role." + operatorRoleID
		entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, lnk)
		if err != nil {
			t.Fatalf("grantedBy link for %q missing at %s: %v", op, lnk, err)
		}
		var lnkDoc map[string]any
		_ = json.Unmarshal(entry.Value, &lnkDoc)
		if del, _ := lnkDoc["isDeleted"].(bool); del {
			t.Fatalf("grantedBy link for %q is tombstoned", op)
		}
	}
}
