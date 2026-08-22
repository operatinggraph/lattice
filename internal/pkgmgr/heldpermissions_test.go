package pkgmgr

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/operatinggraph/lattice/internal/capabilitykv"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// fakeCapabilityReader is a scripted HeldPermissionReader: key -> raw doc
// bytes, or a canned error.
type fakeCapabilityReader struct {
	docs map[string][]byte
	errs map[string]error
}

func (f *fakeCapabilityReader) KVGet(_ context.Context, _ string, key string) (*substrate.KVEntry, error) {
	if err, ok := f.errs[key]; ok {
		return nil, err
	}
	raw, ok := f.docs[key]
	if !ok {
		return nil, substrate.ErrKeyNotFound
	}
	return &substrate.KVEntry{Key: key, Value: raw}, nil
}

func capDocBytes(t *testing.T, perms ...capabilitykv.PlatformPermission) []byte {
	t.Helper()
	raw, err := json.Marshal(capabilitykv.Doc{PlatformPermissions: perms})
	if err != nil {
		t.Fatalf("marshal capability doc: %v", err)
	}
	return raw
}

// TestReadHeldPermissions_OrdinaryActorAnchorDocIsNotHeld: an actor outside
// the system set holds only what cap.roles.<rest> projects, so a permission
// sitting in its cap.<rest> anchor doc must not count as held — every
// permission over-reported here is a permission a grant proposal may confer.
// The roles-key permission proves the read reaches the actor at all.
func TestReadHeldPermissions_OrdinaryActorAnchorDocIsNotHeld(t *testing.T) {
	const actor = "vtx.identity.ordinaryActorHJKMNPQ"
	reader := &fakeCapabilityReader{docs: map[string][]byte{
		"cap.identity.ordinaryActorHJKMNPQ": capDocBytes(t,
			capabilitykv.PlatformPermission{OperationType: "AnchorOnlyOperation", Scope: "any"}),
		"cap.roles.identity.ordinaryActorHJKMNPQ": capDocBytes(t,
			capabilitykv.PlatformPermission{OperationType: "CreateTask", Scope: "any", Origin: "package"}),
	}}

	held, err := ReadHeldPermissions(context.Background(), reader, []string{"vtx.identity.someSystemActrHJKMNP"}, actor)
	if err != nil {
		t.Fatalf("ReadHeldPermissions: %v", err)
	}
	want := []HeldPermission{{OperationType: "CreateTask", Scope: "any", Origin: "package"}}
	if len(held) != 1 || held[0] != want[0] {
		t.Fatalf("held = %+v, want exactly %+v (the anchor doc is not the ordinary actor's)", held, want)
	}
}

// TestReadHeldPermissions_SystemActorUnionsBothKeys is the positive vector for
// the anchor read: for an actor IN the system set the anchor doc IS part of
// the grant set, so the test above asserts a routing decision rather than a
// key this function simply never reads.
func TestReadHeldPermissions_SystemActorUnionsBothKeys(t *testing.T) {
	const actor = "vtx.identity.systemActorHJKMNPQRS"
	reader := &fakeCapabilityReader{docs: map[string][]byte{
		"cap.identity.systemActorHJKMNPQRS": capDocBytes(t,
			capabilitykv.PlatformPermission{OperationType: "InstallPackage", Scope: "any"}),
		"cap.roles.identity.systemActorHJKMNPQRS": capDocBytes(t,
			capabilitykv.PlatformPermission{OperationType: "CreateTask", Scope: "self"}),
	}}

	held, err := ReadHeldPermissions(context.Background(), reader, []string{actor}, actor)
	if err != nil {
		t.Fatalf("ReadHeldPermissions: %v", err)
	}
	if len(held) != 2 {
		t.Fatalf("held = %+v, want both the anchor and the roles projection", held)
	}
	seen := map[HeldPermission]bool{}
	for _, h := range held {
		seen[h] = true
	}
	for _, want := range []HeldPermission{
		{OperationType: "InstallPackage", Scope: "any"},
		{OperationType: "CreateTask", Scope: "self"},
	} {
		if !seen[want] {
			t.Errorf("held is missing %+v: %+v", want, held)
		}
	}
}

// TestReadHeldPermissions_OriginTravelsWithTheEntry — an entry step 3 would
// refuse must be recognizable as such here, which it only is while origin
// rides along.
func TestReadHeldPermissions_OriginTravelsWithTheEntry(t *testing.T) {
	const actor = "vtx.identity.originActorHJKMNPQRS"
	reader := &fakeCapabilityReader{docs: map[string][]byte{
		"cap.roles.identity.originActorHJKMNPQRS": capDocBytes(t,
			capabilitykv.PlatformPermission{OperationType: "GrantPermission", Scope: "any", Origin: "runtime"},
			capabilitykv.PlatformPermission{OperationType: "CreateTask", Scope: "any", Origin: "package"}),
	}}

	held, err := ReadHeldPermissions(context.Background(), reader, nil, actor)
	if err != nil {
		t.Fatalf("ReadHeldPermissions: %v", err)
	}
	if len(held) != 2 || held[0].Origin != "runtime" || held[1].Origin != "package" {
		t.Fatalf("held = %+v, want each entry's origin preserved in order", held)
	}
}

func TestReadHeldPermissions_AbsentKeysAreEmptyNotError(t *testing.T) {
	held, err := ReadHeldPermissions(context.Background(), &fakeCapabilityReader{}, nil, "vtx.identity.neverGrantedHJKMNPQR")
	if err != nil {
		t.Fatalf("ReadHeldPermissions: %v", err)
	}
	if len(held) != 0 {
		t.Fatalf("held = %+v, want none — an absent projection is deny-closed, not an error", held)
	}
}

// TestReadHeldPermissions_ReadErrorPropagates — a substrate failure must not
// read as "holds nothing": that would silently invalidate a legitimate
// proposal, and the caller cannot tell it from a real empty projection.
func TestReadHeldPermissions_ReadErrorPropagates(t *testing.T) {
	boom := errors.New("nats unavailable")
	reader := &fakeCapabilityReader{errs: map[string]error{
		"cap.roles.identity.readErrActorHJKMNPQR": boom,
	}}
	if _, err := ReadHeldPermissions(context.Background(), reader, nil, "vtx.identity.readErrActorHJKMNPQR"); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want it to wrap %v", err, boom)
	}
}

func TestReadHeldPermissions_MalformedActorErrors(t *testing.T) {
	if _, err := ReadHeldPermissions(context.Background(), &fakeCapabilityReader{}, nil, "identity.noVtxPrefixHJKMNPQRS"); err == nil {
		t.Fatal("expected an error for an actor lacking the vtx. prefix")
	}
}

// The real wiring passes *substrate.Conn straight in.
var _ HeldPermissionReader = (*substrate.Conn)(nil)
