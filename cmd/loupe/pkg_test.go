package main

import (
	"context"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	natstest "github.com/nats-io/nats-server/v2/test"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/asolgan/lattice/internal/bootstrap"
	"github.com/asolgan/lattice/internal/jsstore"
	"github.com/asolgan/lattice/internal/pkgmgr"
	"github.com/asolgan/lattice/internal/substrate"
)

// pkgStore builds a getter over a literal envelope map.
func pkgStore(store map[string][]byte) kvGetter {
	return func(key string) ([]byte, bool) { b, ok := store[key]; return b, ok }
}

func TestComputePackage(t *testing.T) {
	store := map[string][]byte{
		"vtx.package.pkg00000000000000000": []byte(`{"class":"package","createdAt":"2026-07-01T00:00:00Z","data":{}}`),
		"vtx.package.pkg00000000000000000.manifest": []byte(`{"class":"manifest","data":{
			"name":"demo-domain","version":"1.2.0","description":"a demo",
			"declaredKeys":[
				"vtx.meta.ddl00000000000000000",
				"vtx.meta.ddl00000000000000000.canonicalName",
				"vtx.meta.ddl00000000000000000.script",
				"vtx.meta.asp00000000000000000",
				"vtx.meta.asp00000000000000000.canonicalName",
				"vtx.meta.opm00000000000000000",
				"vtx.meta.lens0000000000000000",
				"vtx.meta.lens0000000000000000.canonicalName",
				"vtx.meta.wvt00000000000000000",
				"vtx.role.role0000000000000000",
				"vtx.role.role0000000000000000.canonicalName",
				"vtx.roleindex.ri0000000000000",
				"vtx.permission.perm0000000000",
				"lnk.permission.perm0000000000.grantedBy.role.role0000000000000000",
				"vtx.meta.gone0000000000000000",
				"vtx.orphanaspectparent.x0000!bad",
				"vtx.meta.orphanparent00000000.detail"
			]}}`),
		"vtx.meta.ddl00000000000000000":               []byte(`{"class":"meta.ddl.vertexType","data":{}}`),
		"vtx.meta.ddl00000000000000000.canonicalName": []byte(`{"data":{"value":"booking"}}`),
		"vtx.meta.asp00000000000000000":               []byte(`{"class":"meta.ddl.aspectType","data":{}}`),
		"vtx.meta.asp00000000000000000.canonicalName": []byte(`{"data":{"value":"contactInfo"}}`),
		"vtx.meta.opm00000000000000000":               []byte(`{"class":"meta.ddl.vertexType","data":{"operationType":"CreateBooking"}}`),
		"vtx.meta.lens0000000000000000":               []byte(`{"class":"meta.lens","data":{}}`),
		"vtx.meta.lens0000000000000000.canonicalName": []byte(`{"data":{"value":"bookings-by-day"}}`),
		"vtx.meta.wvt00000000000000000":               []byte(`{"class":"meta.weaverTarget","data":{}}`),
		"vtx.role.role0000000000000000":               []byte(`{"class":"role","data":{}}`),
		"vtx.role.role0000000000000000.canonicalName": []byte(`{"data":{"value":"receptionist"}}`),
		"vtx.roleindex.ri0000000000000":               []byte(`{"class":"roleindex","data":{}}`),
		"vtx.permission.perm0000000000":               []byte(`{"class":"permission","data":{"name":"booking.create"}}`),
		"lnk.permission.perm0000000000.grantedBy.role.role0000000000000000": []byte(`{"class":"grantedBy","data":{}}`),
		// vtx.meta.gone0000000000000000 intentionally absent (uninstalled remnant).
	}
	got := computePackage("vtx.package.pkg00000000000000000", pkgStore(store))

	if got["error"] != nil {
		t.Fatalf("unexpected error: %v", got["error"])
	}
	if got["name"] != "demo-domain" || got["version"] != "1.2.0" {
		t.Errorf("name/version = %v/%v", got["name"], got["version"])
	}
	if got["installedAt"] != "2026-07-01T00:00:00Z" {
		t.Errorf("installedAt = %v", got["installedAt"])
	}
	if got["declaredCount"] != 17 {
		t.Errorf("declaredCount = %v, want 17", got["declaredCount"])
	}

	sections := got["sections"].([]map[string]any)
	byKind := map[string][]pkgItem{}
	order := []string{}
	for _, s := range sections {
		kind := s["kind"].(string)
		order = append(order, kind)
		byKind[kind] = s["items"].([]pkgItem)
	}

	// Section order follows pkgSectionOrder with empty sections omitted.
	wantOrder := []string{"entities", "aspects", "operations", "lenses", "orchestration", "roles", "permissions", "grants", "other"}
	if strings.Join(order, ",") != strings.Join(wantOrder, ",") {
		t.Errorf("section order = %v, want %v", order, wantOrder)
	}

	ent := byKind["entities"]
	if len(ent) != 1 || ent[0].Name != "booking" || ent[0].Aspects != 2 {
		t.Errorf("entities = %+v, want one 'booking' with 2 aspects", ent)
	}
	if asp := byKind["aspects"]; len(asp) != 1 || asp[0].Name != "contactInfo" {
		t.Errorf("aspects = %+v", asp)
	}
	// The op-meta shares the entity DDL class; operationType on the vertex
	// data is what routes it to operations.
	if ops := byKind["operations"]; len(ops) != 1 || ops[0].Name != "CreateBooking" {
		t.Errorf("operations = %+v", ops)
	}
	lenses := byKind["lenses"]
	if len(lenses) != 1 || lenses[0].Name != "bookings-by-day" || lenses[0].LensID != "lens0000000000000000" {
		t.Errorf("lenses = %+v", lenses)
	}
	if orch := byKind["orchestration"]; len(orch) != 1 {
		t.Errorf("orchestration = %+v", orch)
	}
	// role + roleindex both land in roles.
	if roles := byKind["roles"]; len(roles) != 2 {
		t.Errorf("roles = %+v, want 2", roles)
	}
	if perms := byKind["permissions"]; len(perms) != 1 || perms[0].Name != "booking.create" {
		t.Errorf("permissions = %+v", perms)
	}
	if grants := byKind["grants"]; len(grants) != 1 || grants[0].Key[:4] != "lnk." {
		t.Errorf("grants = %+v", grants)
	}

	// A declared key that no longer resolves stays visible as unresolved; the
	// unreadable stray vertex and the orphan aspect (parent not declared)
	// land in "other" too — nothing silently dropped.
	other := byKind["other"]
	if len(other) != 3 {
		t.Fatalf("other = %+v, want 3 (missing root, stray vertex, orphan aspect)", other)
	}
	for _, it := range other {
		if it.Found {
			t.Errorf("other item %s unexpectedly found", it.Key)
		}
	}
	if got["unresolved"] != 3 {
		t.Errorf("unresolved = %v, want 3", got["unresolved"])
	}
}

func TestComputePackageMissing(t *testing.T) {
	got := computePackage("vtx.package.nope", pkgStore(map[string][]byte{}))
	if got["error"] == nil {
		t.Fatal("want error for a missing package vertex")
	}
	// Manifest-less package vertex: an error too (nothing to resolve).
	store := map[string][]byte{"vtx.package.bare": []byte(`{"class":"package","data":{}}`)}
	got = computePackage("vtx.package.bare", pkgStore(store))
	if got["error"] == nil {
		t.Fatal("want error for a manifest-less package vertex")
	}
}

func TestManifestFromUpload(t *testing.T) {
	fh := func(name string) *multipart.FileHeader { return &multipart.FileHeader{Filename: name} }

	if _, err := manifestFromUpload(nil); err == nil {
		t.Error("empty upload must error")
	}
	if got, err := manifestFromUpload([]*multipart.FileHeader{fh("whatever.yaml")}); err != nil || got.Filename != "whatever.yaml" {
		t.Errorf("single file: got %v, err %v", got, err)
	}
	got, err := manifestFromUpload([]*multipart.FileHeader{fh("README.md"), fh("Manifest.YAML")})
	if err != nil || got.Filename != "Manifest.YAML" {
		t.Errorf("named manifest must win case-insensitively: got %v, err %v", got, err)
	}
	if _, err := manifestFromUpload([]*multipart.FileHeader{fh("a.yaml"), fh("b.yaml")}); err == nil {
		t.Error("ambiguous multi-file upload must error")
	}
}

func TestApplyReplyShape(t *testing.T) {
	res := &pkgmgr.ApplyResult{
		PackageName: "demo-domain",
		Action:      "upgrade",
		FromVersion: "1.0.0",
		ToVersion:   "1.1.0",
		Created:     2,
		Updated:     1,
		DryRun:      true,
		CreatedKeys: []string{"vtx.meta.a", "vtx.meta.b"},
		UpdatedKeys: []string{"vtx.meta.c"},
	}
	got := applyReply(res)
	if got["action"] != "upgrade" || got["dryRun"] != true {
		t.Errorf("applyReply = %+v", got)
	}
	if keys := got["createdKeys"].([]string); len(keys) != 2 {
		t.Errorf("createdKeys = %v", keys)
	}
}

func TestPackageRegistryMirrorsLatticePkg(t *testing.T) {
	// Every package directory the repo ships (a packages/<dir>/manifest.yaml)
	// must have a registry row — a gap means Loupe can list but not install a
	// package lattice-pkg can. And each row's key must match its definition's
	// name (the manifest lookup key).
	for name := range packageRegistry {
		if packageRegistry[name].Name != name {
			t.Errorf("registry key %q maps to definition named %q", name, packageRegistry[name].Name)
		}
	}
	dirs, err := os.ReadDir("../../packages")
	if err != nil {
		t.Fatalf("read packages dir: %v", err)
	}
	shipped := 0
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		manifest, err := pkgmgr.ParseManifest("../../packages/" + d.Name() + "/manifest.yaml")
		if err != nil {
			continue // not a package dir (no parsable manifest)
		}
		shipped++
		if _, ok := packageRegistry[manifest.Name]; !ok {
			t.Errorf("packages/%s (manifest name %q) is missing from Loupe's registry — add the row here and in cmd/lattice-pkg", d.Name(), manifest.Name)
		}
	}
	if shipped == 0 {
		t.Fatal("no shipped package manifests found — the ../../packages scan is broken")
	}
}

// TestRequireRootAdmin pins the loupe-operator-auth-lift-design.md §4
// mechanism B safety property directly (packagesApply/handlePackagesUninstall
// must reject a non-root-equivalent operator before ever reaching
// s.adminActor's direct-submit): a scoped identity holding no holdsRole
// link is denied 403, a root-equivalent identity (holdsRole -> operator,
// bootstrap.SystemActorKeys's own discovery predicate, Contract #7 §7.7) is
// allowed through. Uses a real embedded NATS + Core KV, not a mock, since
// SystemActorKeys does its own bounded KVListKeys/KVGet scan — mirrors
// internal/bootstrap's own TestSystemActorKeys_DiscoversByOperatorTopology
// fixture shape, scoped down to what this handler-level test needs.
func TestRequireRootAdmin(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	opts := &natsserver.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: jsstore.Dir(t)}
	ns := natstest.RunServer(opts)
	t.Cleanup(ns.Shutdown)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	conn, err := substrate.Connect(ctx, substrate.ConnectOpts{URL: ns.ClientURL(), Name: "loupe-test"})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(conn.Close)
	if _, err := conn.JetStream().CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: bootstrap.CoreKVBucket}); err != nil {
		t.Fatalf("create core-kv bucket: %v", err)
	}

	// SystemActorKeys reads the package-level bootstrap.RoleOperatorID
	// (normally populated by bootstrap.Load against a real bootstrap.json) —
	// set+restore it directly rather than pulling in the full bootstrap
	// fixture, since this test only needs ONE stable role NanoID to link
	// against.
	testRoleOperatorID, err := substrate.NewNanoID()
	if err != nil {
		t.Fatalf("new nanoid: %v", err)
	}
	origRoleID := bootstrap.RoleOperatorID
	bootstrap.RoleOperatorID = testRoleOperatorID
	t.Cleanup(func() { bootstrap.RoleOperatorID = origRoleID })
	roleKey := substrate.VertexKey("role", testRoleOperatorID)

	rootID, err := substrate.NewNanoID()
	if err != nil {
		t.Fatalf("new nanoid: %v", err)
	}
	rootKey := substrate.VertexKey("identity", rootID)
	linkKey := substrate.LinkKey("identity", rootID, "holdsRole", "role", testRoleOperatorID)
	// SystemActorKeys only parses isDeleted off the link's value (it matches
	// the holder purely from the KEY shape) — a minimal literal body is enough,
	// sidestepping MakeLinkEnvelope's unrelated BootstrapIdentityKey/
	// BootstrapOpKey package-state dependency this test has no reason to fake.
	linkBody := []byte(`{"key":"` + linkKey + `","sourceVertex":"` + rootKey + `","targetVertex":"` + roleKey + `","localName":"holdsRole","isDeleted":false}`)
	if _, err := conn.KVPut(ctx, bootstrap.CoreKVBucket, linkKey, linkBody); err != nil {
		t.Fatalf("put holdsRole link: %v", err)
	}

	nonRootID, err := substrate.NewNanoID()
	if err != nil {
		t.Fatalf("new nanoid: %v", err)
	}
	nonRootKey := substrate.VertexKey("identity", nonRootID) // deliberately no holdsRole link

	tests := []struct {
		name        string
		operatorKey string
		wantAllowed bool
		wantStatus  int
	}{
		{"root-equivalent operator allowed through", rootKey, true, 0},
		{"scoped (non-root) operator denied", nonRootKey, false, http.StatusForbidden},
		{"unconfigured operator denied", "", false, http.StatusBadGateway},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := &server{
				conn:             conn,
				operatorActorKey: tc.operatorKey,
				logger:           slog.New(slog.NewTextHandler(io.Discard, nil)),
				natsTimeout:      5 * time.Second,
			}
			req := httptest.NewRequest(http.MethodPost, "/api/packages/install", nil)
			rec := httptest.NewRecorder()
			got := s.requireRootAdmin(rec, req, conn)
			if got != tc.wantAllowed {
				t.Errorf("requireRootAdmin = %v, want %v (body: %s)", got, tc.wantAllowed, rec.Body.String())
			}
			if !tc.wantAllowed && rec.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
		})
	}
}
