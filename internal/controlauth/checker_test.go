package controlauth

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// fakeReader is a scripted capabilitykv.KVGetter: bucket+key -> raw doc
// bytes, or a canned error.
type fakeReader struct {
	docs map[string][]byte // key -> raw JSON
	errs map[string]error  // key -> error to return instead
}

func (f *fakeReader) KVGet(_ context.Context, _ string, key string) (*substrate.KVEntry, error) {
	if err, ok := f.errs[key]; ok {
		return nil, err
	}
	raw, ok := f.docs[key]
	if !ok {
		return nil, substrate.ErrKeyNotFound
	}
	return &substrate.KVEntry{Key: key, Value: raw}, nil
}

// fakeAlerts records EmitAlert calls.
type fakeAlerts struct {
	calls []string
}

func (f *fakeAlerts) EmitAlert(_ context.Context, code string, _ map[string]any) {
	f.calls = append(f.calls, code)
}

func docBytes(t *testing.T, platformPermissions ...map[string]string) []byte {
	t.Helper()
	perms := make([]map[string]string, 0, len(platformPermissions))
	perms = append(perms, platformPermissions...)
	body := map[string]any{"platformPermissions": perms}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal fixture doc: %v", err)
	}
	return raw
}

func newTestChecker(reader *fakeReader, mode AuthMode, alerts *fakeAlerts) *CapabilityKVChecker {
	var a AuthAlertEmitter
	if alerts != nil {
		a = alerts
	}
	// rbacRolesActive=true, no system actors — every actor routes to
	// cap.roles.<actor> alone (the ordinary-actor path the seeded
	// control-operator identity takes).
	return NewCapabilityKVChecker("weaver", WeaverOps, reader, "capability-kv", nil, true, mode, a, nil)
}

func TestAuthorize_EmptyActorDenies(t *testing.T) {
	c := newTestChecker(&fakeReader{}, AuthModeCapability, nil)
	if err := c.Authorize(context.Background(), "", "list", ""); !errors.Is(err, ErrNoActor) {
		t.Fatalf("got %v, want ErrNoActor", err)
	}
}

func TestAuthorize_UnknownOpDenies(t *testing.T) {
	c := newTestChecker(&fakeReader{}, AuthModeCapability, nil)
	if err := c.Authorize(context.Background(), "vtx.identity.OP", "nonsense", ""); !errors.Is(err, ErrUnknownControlOp) {
		t.Fatalf("got %v, want ErrUnknownControlOp", err)
	}
}

func TestAuthorize_NilDocDenies(t *testing.T) {
	c := newTestChecker(&fakeReader{docs: map[string][]byte{}}, AuthModeCapability, nil)
	if err := c.Authorize(context.Background(), "vtx.identity.OP", "list", ""); !errors.Is(err, ErrNoCapabilityEntry) {
		t.Fatalf("got %v, want ErrNoCapabilityEntry", err)
	}
}

func TestAuthorize_InfraReadErrorNeverFailsOpen(t *testing.T) {
	boom := errors.New("kv unavailable")
	reader := &fakeReader{errs: map[string]error{"cap.roles.identity.OP": boom}}
	c := newTestChecker(reader, AuthModeCapability, nil)
	err := c.Authorize(context.Background(), "vtx.identity.OP", "list", "")
	if err == nil {
		t.Fatal("infra error must not be swallowed into an allow")
	}
	if errors.Is(err, ErrControlDenied) || errors.Is(err, ErrNoCapabilityEntry) {
		t.Fatalf("infra error must not present as a denial code: %v", err)
	}
}

func TestAuthorize_MatchingGrantAllows(t *testing.T) {
	reader := &fakeReader{docs: map[string][]byte{
		"cap.roles.identity.OP": docBytes(t, map[string]string{"operationType": "ctrl.weaver.disable", "scope": "any"}),
	}}
	c := newTestChecker(reader, AuthModeCapability, nil)
	if err := c.Authorize(context.Background(), "vtx.identity.OP", "disable", "t1"); err != nil {
		t.Fatalf("expected allow, got %v", err)
	}
}

func TestAuthorize_DifferentComponentGrantDenies(t *testing.T) {
	reader := &fakeReader{docs: map[string][]byte{
		// A refractor grant must not authorize a weaver op.
		"cap.roles.identity.OP": docBytes(t, map[string]string{"operationType": "ctrl.refractor.disable", "scope": "any"}),
	}}
	c := newTestChecker(reader, AuthModeCapability, nil)
	if err := c.Authorize(context.Background(), "vtx.identity.OP", "disable", "t1"); !errors.Is(err, ErrControlDenied) {
		t.Fatalf("got %v, want ErrControlDenied", err)
	}
}

func TestAuthorize_ReadGrantDoesNotAuthorizeMutate(t *testing.T) {
	reader := &fakeReader{docs: map[string][]byte{
		"cap.roles.identity.OP": docBytes(t, map[string]string{"operationType": "ctrl.weaver.read", "scope": "any"}),
	}}
	c := newTestChecker(reader, AuthModeCapability, nil)
	if err := c.Authorize(context.Background(), "vtx.identity.OP", "disable", "t1"); !errors.Is(err, ErrControlDenied) {
		t.Fatalf("a read-only grant must not authorize a mutate op: got %v", err)
	}
	// But the read op itself is allowed.
	if err := c.Authorize(context.Background(), "vtx.identity.OP", "list", ""); err != nil {
		t.Fatalf("expected the read grant to authorize the read op, got %v", err)
	}
}

func TestAuthorize_NonAnyScopeDenies(t *testing.T) {
	reader := &fakeReader{docs: map[string][]byte{
		"cap.roles.identity.OP": docBytes(t, map[string]string{"operationType": "ctrl.weaver.disable", "scope": "self"}),
	}}
	c := newTestChecker(reader, AuthModeCapability, nil)
	if err := c.Authorize(context.Background(), "vtx.identity.OP", "disable", "t1"); !errors.Is(err, ErrControlDenied) {
		t.Fatalf("v1 only matches scope=any: got %v", err)
	}
}

func TestAuthorize_StubModeAllowsAndAlertsPeriodically(t *testing.T) {
	alerts := &fakeAlerts{}
	c := newTestChecker(&fakeReader{}, AuthModeStub, alerts)
	if err := c.Authorize(context.Background(), "", "disable", "t1"); err != nil {
		t.Fatalf("stub mode must allow-all, got %v", err)
	}
	if len(alerts.calls) != 1 || alerts.calls[0] != "stub-control-active" {
		t.Fatalf("expected one stub-control-active alert on the first call, got %v", alerts.calls)
	}
	// Calls 2..999 must not alert again (every-Nth suppression).
	for i := 0; i < 998; i++ {
		if err := c.Authorize(context.Background(), "", "disable", "t1"); err != nil {
			t.Fatalf("stub mode call %d: %v", i, err)
		}
	}
	if len(alerts.calls) != 1 {
		t.Fatalf("expected suppression between the 1st and 1000th call, got %d alerts", len(alerts.calls))
	}
	// The 1000th call re-alerts.
	if err := c.Authorize(context.Background(), "", "disable", "t1"); err != nil {
		t.Fatalf("stub mode call 1000: %v", err)
	}
	if len(alerts.calls) != 2 {
		t.Fatalf("expected a second alert at the 1000th call, got %d alerts", len(alerts.calls))
	}
}

func TestPerComponentOpTables_ReadVsMutateCoverage(t *testing.T) {
	cases := []struct {
		component string
		ops       map[string]OpMeta
		wantRead  []string
		wantWrite []string
	}{
		{"weaver", WeaverOps, []string{"list"}, []string{"disable", "enable", "revoke", "resetConfidence"}},
		{"loom", LoomOps, []string{"list", "consumers", "inspect"}, []string{"pause", "resume"}},
		{"refractor", RefractorOps, []string{"health", "validate", "syncgap"}, []string{"rebuild", "pause", "resume", "delete", "register", "deregister", "hydrate", "sessionkey"}},
	}
	for _, tc := range cases {
		for _, op := range tc.wantRead {
			meta, ok := tc.ops[op]
			if !ok || !meta.Read {
				t.Errorf("%s op %q: want present+read, got ok=%v meta=%+v", tc.component, op, ok, meta)
			}
		}
		for _, op := range tc.wantWrite {
			meta, ok := tc.ops[op]
			if !ok || meta.Read {
				t.Errorf("%s op %q: want present+mutate, got ok=%v meta=%+v", tc.component, op, ok, meta)
			}
		}
	}
}
