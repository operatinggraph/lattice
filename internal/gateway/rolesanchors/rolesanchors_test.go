package rolesanchors

import (
	"context"
	"errors"
	"testing"

	"github.com/operatinggraph/lattice/internal/capabilitykv"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// *substrate.KV must satisfy the anchors read surface Resolve needs — the
// real wiring (Conn.OpenKV → Resolver) is a compile-time guarantee, so the
// unit tests below can use a fake without drifting from production.
var _ kvGetter = (*substrate.KV)(nil)

// *substrate.Conn must satisfy the roles read surface
// capabilitykv.ReadAndMerge needs — the real wiring (passing conn straight
// to New) is a compile-time guarantee.
var _ capabilitykv.KVGetter = (*substrate.Conn)(nil)

const testActorKey = "vtx.identity.Hj4kPmRtw9nbCxz5vQ2y"

type fakeCapabilityReader struct {
	entry     *substrate.KVEntry
	err       error
	gotBucket string
	gotKey    string
}

func (f *fakeCapabilityReader) KVGet(_ context.Context, bucket, key string) (*substrate.KVEntry, error) {
	f.gotBucket = bucket
	f.gotKey = key
	return f.entry, f.err
}

type fakeAnchorsKV struct {
	entry  *substrate.KVEntry
	err    error
	gotKey string
}

func (f *fakeAnchorsKV) Get(_ context.Context, key string) (*substrate.KVEntry, error) {
	f.gotKey = key
	return f.entry, f.err
}

// TestResolve_RolesAndAnchorsPresent proves the doc-parse round-trip for
// both halves and the exact keys each read targets: cap.roles.<suffix> in
// the caller-supplied capability bucket, anchors.<suffix> in the
// identity-anchors bucket.
func TestResolve_RolesAndAnchorsPresent(t *testing.T) {
	capReader := &fakeCapabilityReader{entry: &substrate.KVEntry{
		Value: []byte(`{"roles":["vtx.role.frontOfHouse00000001"]}`),
	}}
	anchorsKV := &fakeAnchorsKV{entry: &substrate.KVEntry{
		Value: []byte(`{"anchors":[{"key":"vtx.location.workplace00000001","name":"Building A","container":"","containerName":"","relation":"worksAt"}]}`),
	}}
	r := New(capReader, "capability-kv", anchorsKV, nil)

	roles, anchors := r.Resolve(context.Background(), testActorKey)

	if len(roles) != 1 || roles[0] != "vtx.role.frontOfHouse00000001" {
		t.Errorf("roles = %v, want [vtx.role.frontOfHouse00000001]", roles)
	}
	if len(anchors) != 1 || anchors[0].Key != "vtx.location.workplace00000001" || anchors[0].Relation != "worksAt" {
		t.Errorf("anchors = %+v, want one worksAt anchor", anchors)
	}
	if capReader.gotBucket != "capability-kv" {
		t.Errorf("capability bucket = %q, want capability-kv", capReader.gotBucket)
	}
	wantRolesKey := "cap.roles.identity.Hj4kPmRtw9nbCxz5vQ2y"
	if capReader.gotKey != wantRolesKey {
		t.Errorf("capability key = %q, want %q", capReader.gotKey, wantRolesKey)
	}
	wantAnchorsKey := "anchors.identity.Hj4kPmRtw9nbCxz5vQ2y"
	if anchorsKV.gotKey != wantAnchorsKey {
		t.Errorf("anchors key = %q, want %q", anchorsKV.gotKey, wantAnchorsKey)
	}
}

// TestResolve_AbsentDegradesToEmpty proves a KeyNotFound on both reads (a
// fresh identity with no capability doc yet and no anchors projected)
// degrades to empty results, not an error.
func TestResolve_AbsentDegradesToEmpty(t *testing.T) {
	capReader := &fakeCapabilityReader{err: substrate.ErrKeyNotFound}
	anchorsKV := &fakeAnchorsKV{err: substrate.ErrKeyNotFound}
	r := New(capReader, "capability-kv", anchorsKV, nil)

	roles, anchors := r.Resolve(context.Background(), testActorKey)
	if roles != nil {
		t.Errorf("roles = %v, want nil (absent doc)", roles)
	}
	if anchors != nil {
		t.Errorf("anchors = %v, want nil (absent doc)", anchors)
	}
}

// TestResolve_TransportErrorDegradesToEmpty proves a non-NotFound transport
// error on both reads also degrades to empty results rather than
// propagating — whoami must never fail because a resolver's read failed.
func TestResolve_TransportErrorDegradesToEmpty(t *testing.T) {
	capReader := &fakeCapabilityReader{err: errors.New("connection refused")}
	anchorsKV := &fakeAnchorsKV{err: errors.New("connection refused")}
	r := New(capReader, "capability-kv", anchorsKV, nil)

	roles, anchors := r.Resolve(context.Background(), testActorKey)
	if roles != nil {
		t.Errorf("roles = %v, want nil on transport error", roles)
	}
	if anchors != nil {
		t.Errorf("anchors = %v, want nil on transport error", anchors)
	}
}

// TestResolve_NilAnchorsKV proves an unconfigured anchors handle (the
// identityAnchors lens hasn't activated yet) degrades that half alone —
// roles still resolve normally.
func TestResolve_NilAnchorsKV(t *testing.T) {
	capReader := &fakeCapabilityReader{entry: &substrate.KVEntry{
		Value: []byte(`{"roles":["vtx.role.consumer0000000001"]}`),
	}}
	r := New(capReader, "capability-kv", nil, nil)

	roles, anchors := r.Resolve(context.Background(), testActorKey)
	if len(roles) != 1 {
		t.Errorf("roles = %v, want one role", roles)
	}
	if anchors != nil {
		t.Errorf("anchors = %v, want nil (no anchors bucket configured)", anchors)
	}
}

// TestResolve_MalformedAnchorsDoc proves a malformed anchors document
// degrades to nil rather than propagating a parse error.
func TestResolve_MalformedAnchorsDoc(t *testing.T) {
	capReader := &fakeCapabilityReader{err: substrate.ErrKeyNotFound}
	anchorsKV := &fakeAnchorsKV{entry: &substrate.KVEntry{Value: []byte(`not-json`)}}
	r := New(capReader, "capability-kv", anchorsKV, nil)

	_, anchors := r.Resolve(context.Background(), testActorKey)
	if anchors != nil {
		t.Errorf("anchors = %v, want nil on malformed document", anchors)
	}
}
