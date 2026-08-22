package capabilitykv

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// fakeReader is a scripted KVGetter: key -> raw doc bytes, or a canned error.
type fakeReader struct {
	docs map[string][]byte
	errs map[string]error
	// read records every key requested, in order, so a test can assert which
	// keys the routing derived — the routing IS the behavior under test.
	read []string
}

func (f *fakeReader) KVGet(_ context.Context, _ string, key string) (*substrate.KVEntry, error) {
	f.read = append(f.read, key)
	if err, ok := f.errs[key]; ok {
		return nil, err
	}
	raw, ok := f.docs[key]
	if !ok {
		return nil, substrate.ErrKeyNotFound
	}
	return &substrate.KVEntry{Key: key, Value: raw}, nil
}

// docWithPermission renders a minimal Capability KV doc carrying one platform
// permission, self-identifying through its own `key` field so a merged result
// can be traced back to the projection it came from.
func docWithPermission(t *testing.T, key, operationType string) []byte {
	t.Helper()
	raw, err := json.Marshal(Doc{
		Key:                 key,
		PlatformPermissions: []PlatformPermission{{OperationType: operationType, Scope: "any"}},
	})
	if err != nil {
		t.Fatalf("marshal doc: %v", err)
	}
	return raw
}

func hasPermission(doc *Doc, operationType string) bool {
	for _, p := range doc.PlatformPermissions {
		if p.OperationType == operationType {
			return true
		}
	}
	return false
}

// TestReadPlatformDoc_SystemActorReadsBothKeys is the positive vector for the
// union read: an actor in the system set contributes from the core anchor AND
// the roles projection.
func TestReadPlatformDoc_SystemActorReadsBothKeys(t *testing.T) {
	const actor = "vtx.identity.SystemActorHJKMNPQRS"
	reader := &fakeReader{docs: map[string][]byte{
		"cap.identity.SystemActorHJKMNPQRS":       docWithPermission(t, "cap.identity.SystemActorHJKMNPQRS", "InstallPackage"),
		"cap.roles.identity.SystemActorHJKMNPQRS": docWithPermission(t, "cap.roles.identity.SystemActorHJKMNPQRS", "CreateBook"),
	}}

	doc, key, err := ReadPlatformDoc(context.Background(), reader, "capability-kv", []string{actor}, actor)
	if err != nil {
		t.Fatalf("ReadPlatformDoc: %v", err)
	}
	if doc == nil {
		t.Fatal("system actor with two present keys returned no doc")
	}
	if !hasPermission(doc, "InstallPackage") || !hasPermission(doc, "CreateBook") {
		t.Fatalf("system actor lost a source's grants: %+v", doc.PlatformPermissions)
	}
	want := "cap.identity.SystemActorHJKMNPQRS+cap.roles.identity.SystemActorHJKMNPQRS"
	if key != want {
		t.Errorf("present-key trace: got %q, want %q", key, want)
	}
	// The anchor is read FIRST, which is what makes it MergeDocs' base and so
	// the source of the identity/provenance scalars a caller's auth trace
	// records. Reversing the derivation order would flip those to the roles
	// projection's with nothing else failing, so the outcome is asserted here
	// rather than left to the ordering's author.
	if doc.Key != "cap.identity.SystemActorHJKMNPQRS" {
		t.Errorf("merged doc key = %q, want the anchor's — the anchor must be the merge base", doc.Key)
	}
}

// TestReadPlatformDoc_OrdinaryActorReadsRolesKeyOnly is the point of the
// helper: an ordinary actor's cap.<rest> anchor doc, if one exists, is NOT
// part of its grant set — step 3 would not honor it, so no other reader may.
func TestReadPlatformDoc_OrdinaryActorReadsRolesKeyOnly(t *testing.T) {
	const actor = "vtx.identity.EverydayActrHJKMNPQR"
	reader := &fakeReader{docs: map[string][]byte{
		// The anchor doc exists and carries a permission that appears nowhere
		// else; if the routing reads it, this permission shows up.
		"cap.identity.EverydayActrHJKMNPQR":       docWithPermission(t, "cap.identity.EverydayActrHJKMNPQR", "AnchorOnlyOperation"),
		"cap.roles.identity.EverydayActrHJKMNPQR": docWithPermission(t, "cap.roles.identity.EverydayActrHJKMNPQR", "CreateBook"),
	}}

	doc, key, err := ReadPlatformDoc(context.Background(), reader, "capability-kv",
		[]string{"vtx.identity.SecondSystemActrHJKM"}, actor)
	if err != nil {
		t.Fatalf("ReadPlatformDoc: %v", err)
	}
	if doc == nil {
		t.Fatal("ordinary actor with a present roles key returned no doc")
	}
	if hasPermission(doc, "AnchorOnlyOperation") {
		t.Errorf("ordinary actor picked up the cap.<rest> anchor doc: %+v", doc.PlatformPermissions)
	}
	if !hasPermission(doc, "CreateBook") {
		t.Errorf("ordinary actor lost its roles-key grants: %+v", doc.PlatformPermissions)
	}
	if key != "cap.roles.identity.EverydayActrHJKMNPQR" {
		t.Errorf("present-key trace: got %q, want the roles key alone", key)
	}
	for _, k := range reader.read {
		if k == "cap.identity.EverydayActrHJKMNPQR" {
			t.Errorf("ordinary actor GET the anchor key %q; routing must not derive it", k)
		}
	}
}

func TestReadPlatformDoc_AbsentEverywhereIsNilDoc(t *testing.T) {
	const actor = "vtx.identity.NoProjectionHJKMNPQR"
	reader := &fakeReader{}
	doc, key, err := ReadPlatformDoc(context.Background(), reader, "capability-kv", nil, actor)
	if err != nil {
		t.Fatalf("ReadPlatformDoc: %v", err)
	}
	if doc != nil || key != "" {
		t.Fatalf("absent projection: got (%+v, %q), want (nil, \"\")", doc, key)
	}
}

func TestReadPlatformDoc_MalformedActorErrors(t *testing.T) {
	if _, _, err := ReadPlatformDoc(context.Background(), &fakeReader{}, "capability-kv", nil, "not-a-vertex-key"); err == nil {
		t.Fatal("expected an error for an actor lacking the vtx. prefix")
	}
}

// TestReadPlatformDoc_ReadErrorPropagates — a real read failure is never a
// degraded grant set.
func TestReadPlatformDoc_ReadErrorPropagates(t *testing.T) {
	const actor = "vtx.identity.ReadErrActorHJKMNPQR"
	boom := errors.New("nats unavailable")
	reader := &fakeReader{errs: map[string]error{
		"cap.roles.identity.ReadErrActorHJKMNPQR": boom,
	}}
	if _, _, err := ReadPlatformDoc(context.Background(), reader, "capability-kv", nil, actor); !errors.Is(err, boom) {
		t.Fatalf("read error: got %v, want it to wrap %v", err, boom)
	}
}

// TestMergeDocs_TotalOverDocShape drives the merge from a fully-populated
// `extra` against a minimal `base` and asserts per-field that nothing from
// extra is dropped.
//
// It also holds the shape itself: a new slice/map field added to Doc is
// caught twice over — once by the reflective "extra populates every field"
// guard (the literal below must set it) and once by the reflective "every
// collection field grows" guard (MergeDocs must fold it), so a field added to
// Doc and forgotten in MergeDocs fails here rather than silently dropping a
// grant at some future caller.
func TestMergeDocs_TotalOverDocShape(t *testing.T) {
	base := &Doc{
		Key:                    "cap.identity.BaseActorHJKMNPQRSTU",
		Actor:                  "vtx.identity.BaseActorHJKMNPQRSTU",
		Version:                "1.0",
		ProjectedAt:            "2026-08-22T00:00:00Z",
		ProjectedFromRevisions: map[string]uint64{"baseSource": 1},
		Lanes:                  []string{"default"},
		PlatformPermissions:    []PlatformPermission{{OperationType: "BaseOp", Scope: "any"}},
		Roles:                  []string{"vtx.role.baseRoRankHJKMNPQRST"},
	}
	extra := &Doc{
		Key:                    "cap.roles.identity.BaseActorHJKMNPQRSTU",
		Actor:                  "vtx.identity.BaseActorHJKMNPQRSTU",
		Version:                "1.0",
		ProjectedAt:            "2026-08-22T01:00:00Z",
		ProjectedFromRevisions: map[string]uint64{"extraSource": 7},
		Lanes:                  []string{"urgent"},
		PlatformPermissions: []PlatformPermission{
			{OperationType: "ExtraOp", Scope: "self", Lanes: []string{"urgent"}, Origin: "package"},
		},
		ServiceAccess: []ServiceAccessEntry{{
			Service:           "vtx.service.extraServiceHJKMNPQR",
			ResolvedVia:       []string{"lnk.identity.BaseActorHJKMNPQRSTU.holdsRole.role.baseRoRankHJKMNPQRST"},
			AllowedOperations: []AllowedOperation{{OperationType: "ExtraServiceOp"}},
		}},
		EphemeralGrants: []EphemeralGrant{{
			Source:        "task",
			TaskKey:       "vtx.task.extraTaskHJKMNPQRSTU",
			OperationType: "ExtraTaskOp",
			Target:        "vtx.book.extraTargetHJKMNPQRS",
			ExpiresAt:     "2026-08-23T00:00:00Z",
		}},
		Roles: []string{"vtx.role.extraRoRankHJKMNPQRS"},
	}

	assertEveryFieldPopulated(t, extra)

	merged := MergeDocs(base, extra)

	assertEveryCollectionGrew(t, base, merged)

	if !hasPermission(merged, "ExtraOp") || !hasPermission(merged, "BaseOp") {
		t.Errorf("platformPermissions: got %+v, want both sources", merged.PlatformPermissions)
	}
	for _, p := range merged.PlatformPermissions {
		if p.OperationType == "ExtraOp" && (p.Origin != "package" || p.Scope != "self" || len(p.Lanes) != 1) {
			t.Errorf("platformPermission entry lost its fields: %+v", p)
		}
	}
	if len(merged.ServiceAccess) != 1 || merged.ServiceAccess[0].Service != "vtx.service.extraServiceHJKMNPQR" {
		t.Errorf("serviceAccess: got %+v, want extra's entry", merged.ServiceAccess)
	}
	if len(merged.ServiceAccess) == 1 {
		if len(merged.ServiceAccess[0].AllowedOperations) != 1 || len(merged.ServiceAccess[0].ResolvedVia) != 1 {
			t.Errorf("serviceAccess entry lost its fields: %+v", merged.ServiceAccess[0])
		}
	}
	if len(merged.EphemeralGrants) != 1 || merged.EphemeralGrants[0].OperationType != "ExtraTaskOp" {
		t.Errorf("ephemeralGrants: got %+v, want extra's entry", merged.EphemeralGrants)
	}
	if len(merged.Lanes) != 2 {
		t.Errorf("lanes: got %v, want the union of both", merged.Lanes)
	}
	if len(merged.Roles) != 2 {
		t.Errorf("roles: got %v, want the union of both", merged.Roles)
	}
	if merged.ProjectedFromRevisions["extraSource"] != 7 || merged.ProjectedFromRevisions["baseSource"] != 1 {
		t.Errorf("projectedFromRevisions: got %v, want both sources", merged.ProjectedFromRevisions)
	}
	// The identity/provenance scalars name a projection, not a grant: they
	// stay base's.
	if merged.Key != base.Key || merged.Actor != base.Actor ||
		merged.Version != base.Version || merged.ProjectedAt != base.ProjectedAt {
		t.Errorf("identity scalars changed: %+v", merged)
	}
	// The revision map is copied, never aliased: a write through the merged
	// doc must not reach base's map.
	merged.ProjectedFromRevisions["writtenThroughMerged"] = 99
	if _, aliased := base.ProjectedFromRevisions["writtenThroughMerged"]; aliased {
		t.Error("MergeDocs aliased base.ProjectedFromRevisions instead of copying it")
	}

	// base is never mutated.
	if len(base.PlatformPermissions) != 1 || len(base.ServiceAccess) != 0 ||
		len(base.EphemeralGrants) != 0 || len(base.Lanes) != 1 || len(base.Roles) != 1 {
		t.Errorf("MergeDocs mutated base: %+v", base)
	}
}

// TestMergeDocs_RevisionMapNeverAliased covers the branch the totality
// fixture cannot reach: when extra carries no revisions there is nothing to
// fold, and the cheap thing to do is keep base's map — which hands the caller
// a doc whose provenance map is base's own, so a write through either is a
// write through both.
func TestMergeDocs_RevisionMapNeverAliased(t *testing.T) {
	base := &Doc{ProjectedFromRevisions: map[string]uint64{"baseSource": 1}}
	extra := &Doc{}

	merged := MergeDocs(base, extra)

	if merged.ProjectedFromRevisions["baseSource"] != 1 {
		t.Fatalf("projectedFromRevisions = %v, want base's entry carried over", merged.ProjectedFromRevisions)
	}
	merged.ProjectedFromRevisions["writtenThroughMerged"] = 2
	if _, aliased := base.ProjectedFromRevisions["writtenThroughMerged"]; aliased {
		t.Error("merged doc aliases base.ProjectedFromRevisions")
	}
}

// TestMergeDocs_NoRevisionsAnywhereStaysNil: neither side carrying revisions
// leaves the field absent rather than fabricating an empty map, so an
// auth-trace consumer can still tell "no provenance recorded" from "recorded
// nothing".
func TestMergeDocs_NoRevisionsAnywhereStaysNil(t *testing.T) {
	merged := MergeDocs(&Doc{}, &Doc{})
	if merged.ProjectedFromRevisions != nil {
		t.Errorf("projectedFromRevisions = %v, want nil when neither doc carries any", merged.ProjectedFromRevisions)
	}
}

// assertEveryFieldPopulated fails when any field of the Doc under test is
// still its zero value — the totality test's premise is that `extra` carries
// something in EVERY field, so a field added to Doc lands here first.
func assertEveryFieldPopulated(t *testing.T, doc *Doc) {
	t.Helper()
	v := reflect.ValueOf(*doc)
	for i := 0; i < v.NumField(); i++ {
		if v.Field(i).IsZero() {
			t.Fatalf("Doc.%s is unpopulated in the merge-totality fixture — populate it and assert MergeDocs folds it",
				v.Type().Field(i).Name)
		}
	}
}

// assertEveryCollectionGrew fails when a slice or map field of the merged doc
// is no larger than base's. Every collection field of `extra` is populated
// with entries base does not carry, so a field MergeDocs forgets to fold shows
// up here as one that did not grow.
func assertEveryCollectionGrew(t *testing.T, base, merged *Doc) {
	t.Helper()
	baseVal, mergedVal := reflect.ValueOf(*base), reflect.ValueOf(*merged)
	for i := 0; i < baseVal.NumField(); i++ {
		switch baseVal.Field(i).Kind() {
		case reflect.Slice, reflect.Map:
			if mergedVal.Field(i).Len() <= baseVal.Field(i).Len() {
				t.Errorf("MergeDocs dropped extra's Doc.%s: merged has %d entries, base has %d",
					baseVal.Type().Field(i).Name, mergedVal.Field(i).Len(), baseVal.Field(i).Len())
			}
		default:
		}
	}
}
