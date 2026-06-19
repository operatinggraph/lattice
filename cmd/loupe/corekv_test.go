package main

import "testing"

func TestClassifyKey(t *testing.T) {
	tests := []struct {
		key  string
		want keyClass
	}{
		// 3-segment vertex root.
		{"vtx.identity.abc123", classVertex},
		{"vtx.role.xyz", classVertex},
		{"vtx.permission.p1", classVertex},
		// 4-segment aspect.
		{"vtx.identity.abc123.profile", classAspect},
		{"vtx.permission.p1.grantsCapability", classAspect},
		// meta-vertex root (3-segment) vs its aspect (4-segment).
		{"vtx.meta.m1", classMeta},
		{"vtx.meta.m1.canonicalName", classAspect},
		// 6-segment link.
		{"lnk.identity.abc123.holdsRole.role.r1", classLink},
		{"lnk.permission.p1.grantedBy.role.r1", classLink},
		// malformed shapes.
		{"lnk.too.short", classUnknown},
		{"vtx.identity.abc.def.ghi", classUnknown},
		{"random", classUnknown},
		{"", classUnknown},
	}
	for _, tt := range tests {
		if got := classifyKey(tt.key); got != tt.want {
			t.Errorf("classifyKey(%q) = %q, want %q", tt.key, got, tt.want)
		}
	}
}

func TestFilterAndClassify(t *testing.T) {
	keys := []string{
		"vtx.meta.m1",
		"vtx.meta.m1.canonicalName",
		"vtx.identity.a1",
		"lnk.identity.a1.holdsRole.role.r1",
		"vtx.role.r1",
	}

	// Prefix filter narrows the set.
	rows, trunc := filterAndClassify(keys, "vtx.meta.", 500)
	if trunc {
		t.Error("did not expect truncation")
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows for vtx.meta. prefix, want 2", len(rows))
	}
	if rows[0].Class != classMeta || rows[1].Class != classAspect {
		t.Errorf("classes = %q,%q want meta,aspect", rows[0].Class, rows[1].Class)
	}

	// Empty prefix matches everything.
	all, _ := filterAndClassify(keys, "", 500)
	if len(all) != len(keys) {
		t.Errorf("empty prefix got %d, want %d", len(all), len(keys))
	}

	// Limit caps and flags truncation.
	capped, trunc := filterAndClassify(keys, "", 2)
	if !trunc {
		t.Error("expected truncation at limit 2")
	}
	if len(capped) != 2 {
		t.Errorf("capped len = %d, want 2", len(capped))
	}
}
