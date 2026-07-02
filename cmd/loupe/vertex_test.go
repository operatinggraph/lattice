package main

import (
	"testing"
)

func TestLinkForVertex(t *testing.T) {
	const link = "lnk.identity.Y.holdsRole.role.X"

	t.Run("vertex is target → in", func(t *testing.T) {
		lr, ok := linkForVertex(link, "vtx.role.X")
		if !ok {
			t.Fatal("expected match")
		}
		if lr.Direction != "in" || lr.Relation != "holdsRole" || lr.OtherKey != "vtx.identity.Y" || lr.OtherType != "identity" {
			t.Errorf("got %+v", lr)
		}
	})

	t.Run("vertex is source → out", func(t *testing.T) {
		lr, ok := linkForVertex(link, "vtx.identity.Y")
		if !ok {
			t.Fatal("expected match")
		}
		if lr.Direction != "out" || lr.OtherKey != "vtx.role.X" || lr.OtherType != "role" {
			t.Errorf("got %+v", lr)
		}
	})

	t.Run("unrelated vertex → no match", func(t *testing.T) {
		if _, ok := linkForVertex(link, "vtx.role.Z"); ok {
			t.Error("expected no match")
		}
	})

	t.Run("malformed link key → no match", func(t *testing.T) {
		if _, ok := linkForVertex("lnk.identity.Y.holdsRole.role", "vtx.role.X"); ok {
			t.Error("expected no match for 5-segment key")
		}
	})
}

func TestBuildVertexList(t *testing.T) {
	store := map[string][]byte{
		"vtx.role.R1":               []byte(`{"class":"role","isDeleted":false,"data":{"protected":true}}`),
		"vtx.role.R1.canonicalName": []byte(`{"data":{"value":"operator"}}`),
		"vtx.package.P1":            []byte(`{"class":"package","data":{"name":"rbac-domain","version":"0.1.0"}}`),
		"vtx.op.O1":                 []byte(`{"class":"op","data":{"operationType":"CreateRole"}}`),
		"vtx.identity.I1":           []byte(`{"class":"identity","isDeleted":true,"data":{"note":"long note"}}`),
		"vtx.meta.M1":               []byte(`{"class":"meta.ddl.vertexType","data":{}}`),
		"vtx.meta.M1.canonicalName": []byte(`{"data":{"value":"rbac"}}`),
		// non-roots that must be excluded from the vertex list:
		"vtx.role.R1.description":           []byte(`{"data":{"value":"d"}}`),
		"lnk.identity.I1.holdsRole.role.R1": []byte(`{}`),
	}
	get := func(k string) ([]byte, bool) { b, ok := store[k]; return b, ok }
	keys := []string{
		"vtx.role.R1", "vtx.role.R1.canonicalName", "vtx.role.R1.description",
		"vtx.package.P1", "vtx.op.O1", "vtx.identity.I1",
		"vtx.meta.M1", "vtx.meta.M1.canonicalName",
		"lnk.identity.I1.holdsRole.role.R1",
	}

	all := vertexQuery{Limit: 100, IncludeDeleted: true}
	list := buildVertexList(keys, get, all)
	if list.Truncated {
		t.Error("did not expect truncation")
	}
	byKey := map[string]vertexRow{}
	for _, r := range list.Rows {
		byKey[r.Key] = r
	}
	if len(list.Rows) != 5 || list.Total != 5 {
		t.Fatalf("got %d vertices total %d, want 5 (R1,P1,O1,I1,M1); %+v", len(list.Rows), list.Total, list.Rows)
	}
	if byKey["vtx.role.R1"].Label != "operator" {
		t.Errorf("role label = %q, want operator (from .canonicalName fallback)", byKey["vtx.role.R1"].Label)
	}
	if byKey["vtx.package.P1"].Label != "rbac-domain" {
		t.Errorf("package label = %q, want rbac-domain", byKey["vtx.package.P1"].Label)
	}
	if byKey["vtx.op.O1"].Label != "CreateRole" {
		t.Errorf("op label = %q, want CreateRole (operationType)", byKey["vtx.op.O1"].Label)
	}
	if byKey["vtx.meta.M1"].Label != "rbac" || byKey["vtx.meta.M1"].Type != "meta" {
		t.Errorf("meta row = %+v, want label=rbac type=meta", byKey["vtx.meta.M1"])
	}
	i1 := byKey["vtx.identity.I1"]
	if i1.Label != "" || !i1.IsDeleted {
		t.Errorf("identity row = %+v, want empty label + isDeleted", i1)
	}
	wantFacets := map[string]int{"role": 1, "package": 1, "op": 1, "identity": 1, "meta": 1}
	for typ, n := range wantFacets {
		if list.Facets[typ] != n {
			t.Errorf("facets[%s] = %d, want %d", typ, list.Facets[typ], n)
		}
	}

	t.Run("deleted excluded by default", func(t *testing.T) {
		list := buildVertexList(keys, get, vertexQuery{Limit: 100})
		if list.Total != 4 {
			t.Errorf("total = %d, want 4 (I1 tombstone hidden)", list.Total)
		}
		if _, ok := list.Facets["identity"]; ok {
			t.Errorf("facets include identity = %v, want absent (only member deleted)", list.Facets)
		}
	})

	t.Run("type facet filters rows, not facet counts", func(t *testing.T) {
		list := buildVertexList(keys, get, vertexQuery{Type: "role", Limit: 100, IncludeDeleted: true})
		if len(list.Rows) != 1 || list.Rows[0].Key != "vtx.role.R1" || list.Total != 1 {
			t.Errorf("type filter = %+v total %d", list.Rows, list.Total)
		}
		if list.Facets["package"] != 1 || list.Facets["meta"] != 1 {
			t.Errorf("facets under type filter = %v, want all types counted", list.Facets)
		}
	})

	t.Run("q matches label and key, case-insensitive", func(t *testing.T) {
		list := buildVertexList(keys, get, vertexQuery{Q: "OPERATOR", Limit: 100})
		if len(list.Rows) != 1 || list.Rows[0].Key != "vtx.role.R1" {
			t.Errorf("q=OPERATOR = %+v, want the operator role (label match)", list.Rows)
		}
		list = buildVertexList(keys, get, vertexQuery{Q: "vtx.op.", Limit: 100})
		if len(list.Rows) != 1 || list.Rows[0].Key != "vtx.op.O1" {
			t.Errorf("q=vtx.op. = %+v, want the op tracker (key match)", list.Rows)
		}
	})

	t.Run("prefix escape hatch filters", func(t *testing.T) {
		list := buildVertexList(keys, get, vertexQuery{Prefix: "vtx.role.", Limit: 100})
		if len(list.Rows) != 1 || list.Rows[0].Key != "vtx.role.R1" {
			t.Errorf("prefix filter = %+v", list.Rows)
		}
	})

	t.Run("offset pages and truncated is honest", func(t *testing.T) {
		page1 := buildVertexList(keys, get, vertexQuery{Limit: 2, IncludeDeleted: true})
		if len(page1.Rows) != 2 || !page1.Truncated || page1.Total != 5 {
			t.Fatalf("page1 = %d rows truncated=%v total=%d", len(page1.Rows), page1.Truncated, page1.Total)
		}
		// Windows are lexicographically stable regardless of input key order.
		if page1.Rows[0].Key != "vtx.identity.I1" || page1.Rows[1].Key != "vtx.meta.M1" {
			t.Errorf("page1 = %+v, want sorted [vtx.identity.I1 vtx.meta.M1]", page1.Rows)
		}
		page3 := buildVertexList(keys, get, vertexQuery{Offset: 4, Limit: 2, IncludeDeleted: true})
		if len(page3.Rows) != 1 || page3.Truncated {
			t.Errorf("last page = %d rows truncated=%v, want 1 rows not truncated", len(page3.Rows), page3.Truncated)
		}
		if page1.Rows[0].Key == page3.Rows[0].Key {
			t.Error("offset did not advance the window")
		}
	})
}

func TestBuildVertexDetail(t *testing.T) {
	root := []byte(`{"class":"role","isDeleted":false,"data":{"protected":true}}`)
	allKeys := []string{
		"vtx.role.R1",
		"vtx.role.R1.canonicalName",
		"vtx.role.R1.description",
		"vtx.role.R1.deep.nested",              // 5-seg → not a direct aspect, excluded
		"lnk.identity.I1.holdsRole.role.R1",    // in
		"lnk.permission.P9.grantedBy.role.R1",  // in
		"lnk.role.R1.governs.identity.Z",       // out
		"lnk.identity.I2.holdsRole.role.OTHER", // unrelated
	}

	vd := buildVertexDetail("vtx.role.R1", root, 7, allKeys)

	if vd.Class != "role" || vd.Revision != 7 || vd.IsDeleted {
		t.Errorf("header = class %q rev %d deleted %v", vd.Class, vd.Revision, vd.IsDeleted)
	}
	if len(vd.Aspects) != 2 || vd.Aspects[0].LocalName != "canonicalName" || vd.Aspects[1].LocalName != "description" {
		t.Errorf("aspects = %+v, want [canonicalName description]", vd.Aspects)
	}
	if len(vd.Links) != 3 {
		t.Fatalf("links = %d, want 3; %+v", len(vd.Links), vd.Links)
	}
	// sorted by relation then otherKey (lexicographic): governs, grantedBy, holdsRole.
	if vd.Links[0].Relation != "governs" || vd.Links[0].Direction != "out" || vd.Links[0].OtherKey != "vtx.identity.Z" {
		t.Errorf("link[0] = %+v", vd.Links[0])
	}
	if vd.Links[1].Relation != "grantedBy" || vd.Links[1].Direction != "in" || vd.Links[1].OtherKey != "vtx.permission.P9" {
		t.Errorf("link[1] = %+v", vd.Links[1])
	}
	if vd.Links[2].Relation != "holdsRole" || vd.Links[2].Direction != "in" {
		t.Errorf("link[2] = %+v", vd.Links[2])
	}
}
