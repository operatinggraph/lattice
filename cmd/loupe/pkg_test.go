package main

import (
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/operatinggraph/lattice/internal/pkgmgr"
)

// pkgStore builds a getter over a literal envelope map.
func pkgStore(store map[string][]byte) kvGetter {
	return func(key string) ([]byte, bool) { b, ok := store[key]; return b, ok }
}

func TestComputePackage(t *testing.T) {
	store := map[string][]byte{
		"vtx.package.pkg99999999999999999": []byte(`{"class":"package","createdAt":"2026-07-01T00:00:00Z","data":{}}`),
		"vtx.package.pkg99999999999999999.manifest": []byte(`{"class":"manifest","data":{
			"name":"demo-domain","version":"1.2.0","description":"a demo",
			"declaredKeys":[
				"vtx.meta.ddL99999999999999999",
				"vtx.meta.ddL99999999999999999.canonicalName",
				"vtx.meta.ddL99999999999999999.script",
				"vtx.meta.asp99999999999999999",
				"vtx.meta.asp99999999999999999.canonicalName",
				"vtx.meta.opm99999999999999999",
				"vtx.meta.Lens9999999999999999",
				"vtx.meta.Lens9999999999999999.canonicalName",
				"vtx.meta.wvt99999999999999999",
				"vtx.role.roLe9999999999999999",
				"vtx.role.roLe9999999999999999.canonicalName",
				"vtx.roleindex.ri0000000000000",
				"vtx.permission.perm0000000000",
				"lnk.permission.perm0000000000.grantedBy.role.roLe9999999999999999",
				"vtx.meta.gone9999999999999999",
				"vtx.orphanaspectparent.x0000!bad",
				"vtx.meta.orphanparent99999999.detail"
			]}}`),
		"vtx.meta.ddL99999999999999999":                                     []byte(`{"class":"meta.ddl.vertexType","data":{}}`),
		"vtx.meta.ddL99999999999999999.canonicalName":                       []byte(`{"data":{"value":"booking"}}`),
		"vtx.meta.asp99999999999999999":                                     []byte(`{"class":"meta.ddl.aspectType","data":{}}`),
		"vtx.meta.asp99999999999999999.canonicalName":                       []byte(`{"data":{"value":"contactInfo"}}`),
		"vtx.meta.opm99999999999999999":                                     []byte(`{"class":"meta.ddl.vertexType","data":{"operationType":"CreateBooking"}}`),
		"vtx.meta.Lens9999999999999999":                                     []byte(`{"class":"meta.lens","data":{}}`),
		"vtx.meta.Lens9999999999999999.canonicalName":                       []byte(`{"data":{"value":"bookings-by-day"}}`),
		"vtx.meta.wvt99999999999999999":                                     []byte(`{"class":"meta.weaverTarget","data":{}}`),
		"vtx.role.roLe9999999999999999":                                     []byte(`{"class":"role","data":{}}`),
		"vtx.role.roLe9999999999999999.canonicalName":                       []byte(`{"data":{"value":"receptionist"}}`),
		"vtx.roleindex.ri0000000000000":                                     []byte(`{"class":"roleindex","data":{}}`),
		"vtx.permission.perm0000000000":                                     []byte(`{"class":"permission","data":{"name":"booking.create"}}`),
		"lnk.permission.perm0000000000.grantedBy.role.roLe9999999999999999": []byte(`{"class":"grantedBy","data":{}}`),
		// vtx.meta.gone9999999999999999 intentionally absent (uninstalled remnant).
	}
	got := computePackage("vtx.package.pkg99999999999999999", pkgStore(store))

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
	if len(lenses) != 1 || lenses[0].Name != "bookings-by-day" || lenses[0].LensID != "Lens9999999999999999" {
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

// TestUninstallErrorResponse pins the uninstall handler's whole error contract
// without a NATS harness: the status AND the body, including the fields the
// confirm modal re-prompts from.
//
// The fields are the point. A console that had to regex lens names out of the
// refusal prose would break the first time the message was reworded — and the
// operator would be left with a package that cannot be uninstalled from the
// UI at all. So the refusal's own Unattested entries are asserted here, key by
// key, alongside the 409.
func TestUninstallErrorResponse(t *testing.T) {
	refusal := &pkgmgr.UndeclaredSecureLensErasureError{
		PackageName: "demo-domain",
		Unattested: []pkgmgr.UninstallSecureColumnErasure{
			{Key: "vtx.meta.aaaaaaaaaaaaaaaaaaaa.spec", Lens: "clinicEncountersRead", Columns: []string{"summary"}, Holders: []string{"retentionclass"}},
			{Key: "vtx.meta.bbbbbbbbbbbbbbbbbbbb.spec", Lens: "", Columns: nil, Declared: 2, Holders: []string{"identity"}},
		},
	}
	// Wrapped, as every production path returns them.
	status, body := uninstallErrorResponse(fmt.Errorf("uninstall demo-domain: %w", refusal))
	if status != http.StatusConflict {
		t.Errorf("status = %d, want %d — an unattested erasure fails identically on every retry", status, http.StatusConflict)
	}
	if _, ok := body["error"].(string); !ok {
		t.Errorf("body carries no error string, so it does not match writeError's shape: %+v", body)
	}
	lenses, ok := body["unattestedSecureLenses"].([]map[string]any)
	if !ok || len(lenses) != 2 {
		t.Fatalf("unattestedSecureLenses = %+v, want both refused lenses as data", body["unattestedSecureLenses"])
	}
	if lenses[0]["lens"] != "clinicEncountersRead" || lenses[0]["key"] != "vtx.meta.aaaaaaaaaaaaaaaaaaaa.spec" {
		t.Errorf("unattestedSecureLenses[0] = %+v, want the lens named with its spec key", lenses[0])
	}
	if cols, _ := lenses[0]["columns"].([]string); !slices.Equal(cols, []string{"summary"}) {
		t.Errorf("unattestedSecureLenses[0].columns = %v, want [summary] — the operator attests ABOUT these", lenses[0]["columns"])
	}
	if holders, _ := lenses[0]["holderTypes"].([]string); !slices.Equal(holders, []string{"retentionclass"}) {
		t.Errorf("unattestedSecureLenses[0].holderTypes = %v, want [retentionclass]", lenses[0]["holderTypes"])
	}
	// A lens with no nameable columns renders as an empty array, never null:
	// the console iterates these fields without a per-field guard. Its declared
	// count is what gives the modal a subject to show at all.
	if cols, ok := lenses[1]["columns"].([]string); !ok || len(cols) != 0 {
		t.Errorf("unattestedSecureLenses[1].columns = %#v, want an empty array rather than null", lenses[1]["columns"])
	}
	if lenses[1]["declaredColumns"] != 2 {
		t.Errorf("unattestedSecureLenses[1].declaredColumns = %v, want 2 — without it the modal shows an empty subject",
			lenses[1]["declaredColumns"])
	}

	// A malformed attestation is a bad REQUEST, not a package-state conflict and
	// not a substrate fault. The console can build one, and 502 would tell the
	// operator to retry a body that will be rejected identically every time.
	badStatus, badBody := uninstallErrorResponse(fmt.Errorf("uninstall demo-domain: %w", pkgmgr.ErrInvalidUninstallOptions))
	if badStatus != http.StatusBadRequest {
		t.Errorf("a malformed attestation = %d, want %d", badStatus, http.StatusBadRequest)
	}
	if _, has := badBody["unattestedSecureLenses"]; has {
		t.Errorf("a malformed-options refusal names no lenses to attest: %+v", badBody)
	}

	// The uninstall's own not-installed refusal reaches this helper wrapped, so
	// the 409 row in TestPackageApplyStatus actually covers the uninstall path.
	if got, _ := uninstallErrorResponse(fmt.Errorf("uninstall: %w", pkgmgr.ErrNotInstalled)); got != http.StatusConflict {
		t.Errorf("ErrNotInstalled from uninstall = %d, want %d", got, http.StatusConflict)
	}

	// Anything else keeps the plain shape and the 502 the UI retries.
	plainStatus, plainBody := uninstallErrorResponse(errors.New("nats: no responders available"))
	if plainStatus != http.StatusBadGateway {
		t.Errorf("an unclassified failure must stay %d, got %d", http.StatusBadGateway, plainStatus)
	}
	if _, has := plainBody["unattestedSecureLenses"]; has {
		t.Errorf("a non-refusal must not carry unattestedSecureLenses: %+v", plainBody)
	}
	if plainBody["error"] != "nats: no responders available" {
		t.Errorf("plain body = %+v, want writeError's {error} shape verbatim", plainBody)
	}
}

// TestPackageApplyStatus pins the classification every package-verb call site
// shares. The 502 default reads, in this UI, as "the substrate is unreachable,
// retry" — so every deterministic package-state conflict has to be 409 instead,
// or the operator is told to wait out a condition that is permanent.
// ErrDeclaredKeysOccupied is exactly that shape: an install refused because its
// own keys are already committed fails identically on every retry. So is
// ErrUndeclaredSecureLensErasure, which fails forever until an operator supplies
// the attestation, and ErrUninstallConflict, whose re-run is an operator's
// decision about a changed package rather than a transient to retry blind.
func TestPackageApplyStatus(t *testing.T) {
	conflicts := []error{
		pkgmgr.ErrNotInstalled,
		pkgmgr.ErrCanonicalNameCollision,
		pkgmgr.ErrDeclaredKeysOccupied,
		pkgmgr.ErrUndeclaredSecureLensErasure,
		pkgmgr.ErrUninstallConflict,
	}
	for _, base := range conflicts {
		// Wrapped, as every production path returns them.
		wrapped := fmt.Errorf("apply demo-domain: %w", base)
		if got := packageApplyStatus(wrapped); got != http.StatusConflict {
			t.Errorf("packageApplyStatus(%v) = %d, want %d", base, got, http.StatusConflict)
		}
	}
	if got := packageApplyStatus(errors.New("nats: no responders available")); got != http.StatusBadGateway {
		t.Errorf("an unclassified failure must stay %d, got %d", http.StatusBadGateway, got)
	}
}
