// Install-flow integration test for the location-domain Capability Package.
//
// Proves the SL.1 install guarantees:
//
//  1. Co-installs cleanly alongside identity-domain (which itself depends on
//     rbac-domain) — none of the four location DDL canonical names (the
//     abstract `location` plus the concrete unit/building/property leaves)
//     collides with identity-domain's `identity` DDL or any of its aspect-type
//     DDLs, and both packages' declared keys land in one keyspace.
//  2. The five location permission vertices + their grantedBy→operator links
//     commit, and a concrete leaf class becomes usable on the same running
//     Processor (in-commit DDL-cache invalidation, no restart).
//  3. The TAXONOMY lands: the abstract `location` meta carries
//     data.abstract=true and no script/permittedCommands aspects, and each
//     concrete leaf carries a live `lnk.meta.<leafId>.subtypeOf.meta.<locId>`
//     edge (dynamic-type-taxonomy-design.md §3.2/§3.3/§3.5).
package locationdomain_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/operatinggraph/lattice/internal/aiagent"
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
//   - every DDL (identity + the four location types) is present with a distinct
//     canonical name;
//   - location-domain's five ops each have a permission vertex granted to operator;
//   - the abstract `location` type and its three subtypeOf edges landed;
//   - a just-declared concrete leaf class is usable (a CreateLocation commits) on
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

	inst := testutil.NewInstaller(conn, bootstrap.BootstrapIdentityKey)
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
	// several aspect-type DDLs; location-domain declares four vertex-type DDLs
	// (abstract `location` + unit/building/property). The
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
	locRef, ok := cache.Lookup("location")
	if !ok {
		t.Fatal("location DDL class not resolvable after co-install")
	}
	if !locRef.Abstract {
		t.Fatal("the location DDL must resolve as ABSTRACT — an abstract type names no instance")
	}
	if len(locRef.PermittedCommands) != 0 {
		t.Fatalf("the abstract location DDL must admit no operationType, got %v", locRef.PermittedCommands)
	}
	for _, leaf := range locationdomain.LocationTypes {
		leafRef, ok := cache.Lookup(leaf)
		if !ok {
			t.Fatalf("concrete leaf DDL class %q not resolvable after co-install", leaf)
		}
		if leafRef.Abstract {
			t.Fatalf("concrete leaf %q must not resolve as abstract", leaf)
		}
		if len(leafRef.PermittedCommands) != 5 {
			t.Fatalf("concrete leaf %q permittedCommands = %v, want the five location ops", leaf, leafRef.PermittedCommands)
		}
	}

	// The taxonomy edges landed in location-domain's own install batch: one
	// live `lnk.meta.<leafId>.subtypeOf.meta.<locationId>` per concrete leaf.
	assertSubtypeOfEdgesLanded(t, ctx, conn)

	// The abstract type declares NEITHER a script NOR a permittedCommands
	// aspect — emitting either, even empty, would make it read as concrete to
	// anything walking the aspect list.
	abstractKey := "vtx.meta." + pkgmgr.DDLID("location-domain", "location")
	for _, absent := range []string{".script", ".permittedCommands"} {
		if _, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, abstractKey+absent); err == nil {
			t.Fatalf("the abstract location DDL must not carry a %s aspect", absent)
		}
	}

	// The five location permission vertices each landed, granted to operator.
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
		Class:         "property",
		Payload:       json.RawMessage(`{"locationType":"property"}`),
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	if _, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, processor.TrackerKey(reqID)); err != nil {
		t.Fatalf("CreateLocation on the just-installed class did not commit (no tracker): %v", err)
	}
}

// assertSubtypeOfEdgesLanded checks the three taxonomy edges location-domain's
// own install batch emits: leaf -> abstract, six segments, `meta` on both
// sides, alive (dynamic-type-taxonomy-design.md §3.3).
func assertSubtypeOfEdgesLanded(t *testing.T, ctx context.Context, conn *substrate.Conn) {
	t.Helper()
	abstractID := pkgmgr.DDLID("location-domain", "location")
	for _, leaf := range locationdomain.LocationTypes {
		linkKey := "lnk.meta." + pkgmgr.DDLID("location-domain", leaf) + ".subtypeOf.meta." + abstractID
		entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, linkKey)
		if err != nil {
			t.Fatalf("subtypeOf edge for leaf %q is absent (%s): %v", leaf, linkKey, err)
		}
		var doc struct {
			Class        string `json:"class"`
			IsDeleted    bool   `json:"isDeleted"`
			SourceVertex string `json:"sourceVertex"`
			TargetVertex string `json:"targetVertex"`
		}
		if err := json.Unmarshal(entry.Value, &doc); err != nil {
			t.Fatalf("unmarshal subtypeOf edge %s: %v", linkKey, err)
		}
		if doc.IsDeleted {
			t.Fatalf("subtypeOf edge %s is tombstoned", linkKey)
		}
		if doc.Class != "subtypeOf" {
			t.Fatalf("subtypeOf edge %s class = %q, want subtypeOf", linkKey, doc.Class)
		}
		// Direction (Contract #1 §1.1): the later-arriving LEAF is the source,
		// the pre-existing abstract the target.
		if want := "vtx.meta." + pkgmgr.DDLID("location-domain", leaf); doc.SourceVertex != want {
			t.Fatalf("subtypeOf edge %s source = %q, want the leaf %q", linkKey, doc.SourceVertex, want)
		}
		if want := "vtx.meta." + abstractID; doc.TargetVertex != want {
			t.Fatalf("subtypeOf edge %s target = %q, want the abstract %q", linkKey, doc.TargetVertex, want)
		}
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

// TestInstallFlow_AbstractLocationRefusedAsInstance drives the step-6
// fail-closed gates against the REAL installed location-domain, not a
// hand-seeded fixture: the abstract type this package now ships is what arms
// them. It is the shipped-corpus counterpart of
// internal/processor's step6_validate_abstract_test.go, which proves the gates
// over synthetic DDLs.
//
// Four vectors, and the pair matters. A create keyed vtx.location.<id> and a
// create carrying class "location" are both refused; a TOMBSTONE of the same
// key is exempt (removing an instance is the corrective path a live concrete
// type flipped to abstract needs, and a tombstone can never create one); and a
// concrete leaf write is accepted, so the refusals are the abstract talking
// and not the validator refusing everything.
func TestInstallFlow_AbstractLocationRefusedAsInstance(t *testing.T) {
	url := testutil.StartEmbeddedNATS(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	t.Cleanup(cancel)
	conn, err := substrate.Connect(ctx, substrate.ConnectOpts{URL: url, Name: "loc-abstract-gate"})
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
	inst := testutil.NewInstaller(conn, bootstrap.BootstrapIdentityKey)
	inst.RoleIDs = map[string]string{"operator": bootstrap.RoleOperatorID}
	if _, err := inst.Install(ctx, rbacdomain.Package); err != nil {
		stop()
		t.Fatalf("install rbac-domain: %v", err)
	}
	if _, err := inst.Install(ctx, locationdomain.Package); err != nil {
		stop()
		t.Fatalf("install location-domain: %v", err)
	}
	stop()

	cache := processor.NewDDLCache(conn, testutil.HarnessCoreBucket, testutil.TestLogger())
	if err := cache.Refresh(ctx); err != nil {
		t.Fatalf("cache refresh: %v", err)
	}
	v := processor.NewValidator(cache, conn, testutil.HarnessCoreBucket, testutil.TestLogger())
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("abstractGate"),
		Lane:          processor.LaneDefault,
		OperationType: "CreateLocation",
		Actor:         bootstrap.BootstrapIdentityKey,
		Class:         "unit",
	}
	const victim = "vtx.location.LDabstractHJKMNPQRST"

	validate := func(m processor.MutationOp) error {
		return v.Validate(ctx, env, processor.ScriptResult{Mutations: []processor.MutationOp{m}}, processor.HydratedState{})
	}
	// refusedBy asserts the mutation is rejected AND names WHICH gate rejected
	// it. Asserting only "an error came back" would let a malformed key
	// literal — one char outside the NanoID alphabet is enough — pass this
	// test on a keyPattern rejection having never reached the abstract gates
	// at all, which is the whole thing these vectors exist to exercise.
	refusedBy := func(constraint string, m processor.MutationOp) {
		t.Helper()
		err := validate(m)
		if err == nil {
			t.Fatalf("%s: the mutation must be refused", constraint)
		}
		var violation *processor.DDLViolation
		if !errors.As(err, &violation) {
			t.Fatalf("%s: want a *DDLViolation, got %T: %v", constraint, err, err)
		}
		if violation.ViolatedConstraint != constraint {
			t.Fatalf("refused by %q, want %q — the rejection must come from the gate under test: %v",
				violation.ViolatedConstraint, constraint, err)
		}
	}

	// 1. A create keyed with the abstract type segment.
	refusedBy("abstractTypeSegment", processor.MutationOp{
		Op: "create", Key: victim,
		Document: map[string]any{"class": "unit", "isDeleted": false, "data": map[string]any{}},
	})

	// 2. A create whose CLASS is the abstract type, on a perfectly good key.
	refusedBy("abstractClass", processor.MutationOp{
		Op: "create", Key: "vtx.unit.LDabstractcLsHJKMNPQ",
		Document: map[string]any{"class": "location", "isDeleted": false, "data": map[string]any{}},
	})

	// 3. A TOMBSTONE of the same abstract-typed key is exempt.
	if err := validate(processor.MutationOp{Op: "tombstone", Key: victim}); err != nil {
		t.Fatalf("a tombstone of an abstract-typed key must stay possible (the corrective path): %v", err)
	}

	// 4. The positive vector: the concrete leaf write the ops actually make.
	if err := validate(processor.MutationOp{
		Op: "create", Key: "vtx.unit.LDconcreteHJKMNPQRST",
		Document: map[string]any{"class": "unit", "isDeleted": false, "data": map[string]any{}},
	}); err != nil {
		t.Fatalf("a concrete leaf write must be accepted: %v", err)
	}
}

// legacyConcreteLocationPackage is the 0.2.x shape this package shipped before
// the taxonomy: ONE concrete `location` vertexType DDL carrying the script and
// all five permittedCommands, governing all three key types through a
// locationType payload discriminator. It exists so a test can drive the real
// upgrade rather than assert the destination shape a fresh install produces.
func legacyConcreteLocationPackage() pkgmgr.Definition {
	return pkgmgr.Definition{
		Name:        "location-domain",
		Version:     "0.2.3",
		Description: "Spatial base domain (pre-taxonomy shape).",
		Depends:     []string{},
		DDLs: []pkgmgr.DDLSpec{{
			CanonicalName:     "location",
			Class:             "meta.ddl.vertexType",
			PermittedCommands: []string{"CreateLocation", "TombstoneLocation", "WireContainedIn", "UnwireContainedIn", "SetLocationPresentation"},
			Description:       "Location domain DDL (pre-taxonomy).",
			Script:            "def execute(state, op):\n    fail(\"legacy location script\")\n",
			InputSchema:       `{"type":"object","properties":{}}`,
			OutputSchema:      `{"type":"object","properties":{}}`,
			FieldDescription:  map[string]string{"locationType": "unit|building|property"},
			Examples: []pkgmgr.ExampleSpec{{
				Name:            "CreateLocation",
				Payload:         map[string]any{"locationType": "unit"},
				ExpectedOutcome: "Mints a location.",
			}},
		}},
		Permissions: locationdomain.Permissions(),
	}
}

// TestUpgrade_ConcreteLocationToAbstract drives the transition every
// already-installed cell goes through: the 0.2.x CONCRETE `location`
// DDL upgraded in place to the four-meta abstract form.
//
// The destination differs from a fresh install in a way no other test reaches,
// and it is the difference that bites. A DDL keeps its meta-vertex NanoID
// across versions (Contract #8 §8.1), so abstract `location` REUSES the
// concrete one's key; pkgmgr's diff emits TOMBSTONES for the `.script` and
// `.permittedCommands` aspects the new version stops declaring; and a
// tombstone retains the prior document whole, flipping only isDeleted
// (processor step8_commit). The old script and all five commands are therefore
// still sitting in Core KV, byte-for-byte, behind an isDeleted flag.
//
// So "an abstract type declares neither" is not a property of what is stored —
// it is a property every READER has to enforce. This test asserts the storage
// fact and then asserts each reader's disposition against it.
func TestUpgrade_ConcreteLocationToAbstract(t *testing.T) {
	url := testutil.StartEmbeddedNATS(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	t.Cleanup(cancel)
	conn, err := substrate.Connect(ctx, substrate.ConnectOpts{URL: url, Name: "loc-upgrade-abstract"})
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
	inst := testutil.NewInstaller(conn, bootstrap.BootstrapIdentityKey)
	inst.RoleIDs = map[string]string{"operator": bootstrap.RoleOperatorID}
	if _, err := inst.Install(ctx, rbacdomain.Package); err != nil {
		stop()
		t.Fatalf("install rbac-domain: %v", err)
	}
	if _, err := inst.Install(ctx, legacyConcreteLocationPackage()); err != nil {
		stop()
		t.Fatalf("install the pre-taxonomy location-domain: %v", err)
	}

	abstractKey := "vtx.meta." + pkgmgr.DDLID("location-domain", "location")
	// Precondition: the concrete shape really is what is installed, or the
	// upgrade below proves nothing.
	for _, asp := range []string{".script", ".permittedCommands"} {
		if live, err := aspectIsLive(ctx, conn, abstractKey+asp); err != nil || !live {
			stop()
			t.Fatalf("precondition: %s must be live before the upgrade (live=%v err=%v)", abstractKey+asp, live, err)
		}
	}

	if _, err := inst.Upgrade(ctx, locationdomain.Package); err != nil {
		stop()
		t.Fatalf("upgrade location-domain to the taxonomy shape: %v", err)
	}
	stop()

	// The meta-vertex key is REUSED, not re-minted — the premise of everything
	// below.
	if _, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, abstractKey); err != nil {
		t.Fatalf("the abstract location meta must reuse the concrete one's key %s: %v", abstractKey, err)
	}

	// STORAGE: both aspects still exist and still carry their old content.
	// This is the fact every reader has to cope with, asserted rather than
	// assumed — if a future pkgmgr change starts hard-deleting instead, this
	// is the line that says the hazard is gone.
	for _, asp := range []string{".script", ".permittedCommands"} {
		entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, abstractKey+asp)
		if err != nil {
			t.Fatalf("%s: the upgrade tombstones rather than removes, so the key must still resolve: %v", abstractKey+asp, err)
		}
		var env struct {
			IsDeleted bool `json:"isDeleted"`
		}
		if err := json.Unmarshal(entry.Value, &env); err != nil {
			t.Fatalf("unmarshal %s: %v", abstractKey+asp, err)
		}
		if !env.IsDeleted {
			t.Fatalf("%s must be tombstoned after the upgrade, not live", abstractKey+asp)
		}
	}

	// READER 1 — the Processor's DDL cache. This is the load-bearing one: a
	// ref carrying Abstract:true alongside the old script and all five
	// commands is the exact inversion of the invariant the marker asserts.
	cache := processor.NewDDLCache(conn, testutil.HarnessCoreBucket, testutil.TestLogger())
	if err := cache.Refresh(ctx); err != nil {
		t.Fatalf("cache refresh: %v", err)
	}
	ref, ok := cache.Lookup("location")
	if !ok {
		t.Fatal("the location DDL must still resolve after the upgrade")
	}
	if !ref.Abstract {
		t.Fatal("the upgraded location DDL must resolve as abstract")
	}
	if len(ref.PermittedCommands) != 0 {
		t.Errorf("permittedCommands read through a TOMBSTONE: got %v, want none — a tombstoned declaration must read as absent", ref.PermittedCommands)
	}
	if ref.ScriptSource != "" {
		t.Errorf("script read through a TOMBSTONE: got %d bytes, want none — a tombstoned declaration must read as absent", len(ref.ScriptSource))
	}
	// The concrete leaves are unaffected: they carry the live script + commands.
	for _, leaf := range locationdomain.LocationTypes {
		leafRef, ok := cache.Lookup(leaf)
		if !ok {
			t.Fatalf("concrete leaf %q must resolve after the upgrade", leaf)
		}
		if leafRef.ScriptSource == "" || len(leafRef.PermittedCommands) != 5 {
			t.Errorf("leaf %q lost its live declarations: script=%d bytes commands=%v", leaf, len(leafRef.ScriptSource), leafRef.PermittedCommands)
		}
	}

	// READER 2 — the cold-start agent traversal. On this path KVGet SUCCEEDS
	// (the tombstoned key resolves), so an exemption keyed only on a missing
	// key never fires and the read fails on an abstract type that is correctly
	// declared.
	tr := aiagent.NewTraverser(conn, testutil.HarnessCoreBucket, testutil.HarnessCapBucket, nil)
	aspects, err := tr.ReadDDLAspects(ctx, abstractKey)
	if err != nil {
		t.Fatalf("cold-start traversal of an upgraded abstract DDL must succeed: %v", err)
	}
	if !aspects.Abstract {
		t.Error("traversal must report the upgraded DDL as abstract")
	}
	if aspects.Script != "" {
		t.Errorf("traversal returned a tombstoned script (%d bytes); it must read as withdrawn", len(aspects.Script))
	}
	if len(aspects.PermittedCommands) != 0 {
		t.Errorf("traversal returned tombstoned permittedCommands %v; they must read as withdrawn", aspects.PermittedCommands)
	}

	// READER 3 — the predicate `make verify-package-location-domain` applies.
	// The script is a //go:build ignore main and cannot be imported, so its
	// rule is restated here against the same keys: an aspect an abstract type
	// must not declare is acceptable ABSENT or TOMBSTONED, never LIVE. A gate
	// demanding an absent key fails on every upgraded cell, which is what this
	// pins.
	for _, asp := range []string{".script", ".permittedCommands"} {
		live, err := aspectIsLive(ctx, conn, abstractKey+asp)
		if err != nil {
			t.Fatalf("read %s: %v", abstractKey+asp, err)
		}
		if live {
			t.Errorf("%s is live on an abstract type — the verify gate must fail this", abstractKey+asp)
		}
	}
}

// aspectIsLive reports whether an aspect key resolves AND is not tombstoned.
// A missing key returns (false, nil): absent and withdrawn are the two shapes
// of "not declared", and callers that care about the difference read the
// envelope themselves.
func aspectIsLive(ctx context.Context, conn *substrate.Conn, key string) (bool, error) {
	entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, key)
	if err != nil {
		if errors.Is(err, substrate.ErrKeyNotFound) {
			return false, nil
		}
		return false, err
	}
	var env struct {
		IsDeleted bool `json:"isDeleted"`
	}
	if err := json.Unmarshal(entry.Value, &env); err != nil {
		return false, err
	}
	return !env.IsDeleted, nil
}
