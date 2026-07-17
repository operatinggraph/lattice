package keys

import (
	"strings"
	"testing"
)

const (
	testNanoID1 = "Hj4kPmRtw9nbCxz5vQ2y"
	testNanoID2 = "St6mP3qBn4rT8wYxK7Vc"
	testNanoID3 = "Lk2Pn6mQrtwzKbcXvP3T"
)

func TestVertexKey(t *testing.T) {
	got := VertexKey("identity", testNanoID1)
	want := "vtx.identity." + testNanoID1
	if got != want {
		t.Fatalf("VertexKey = %q, want %q", got, want)
	}
	if ClassifyKey(got) != KindVertex {
		t.Fatalf("ClassifyKey(%q) != KindVertex", got)
	}
}

func TestAspectKey(t *testing.T) {
	vtx := VertexKey("identity", testNanoID1)
	got := AspectKey(vtx, "email")
	want := vtx + ".email"
	if got != want {
		t.Fatalf("AspectKey = %q, want %q", got, want)
	}
	if ClassifyKey(got) != KindAspect {
		t.Fatalf("ClassifyKey(%q) != KindAspect", got)
	}
}

func TestLinkKey(t *testing.T) {
	got := LinkKey("lease", testNanoID3, "heldBy", "identity", testNanoID1)
	want := "lnk.lease." + testNanoID3 + ".heldBy.identity." + testNanoID1
	if got != want {
		t.Fatalf("LinkKey = %q, want %q", got, want)
	}
	if ClassifyKey(got) != KindLink {
		t.Fatalf("ClassifyKey(%q) != KindLink", got)
	}
}

func TestKeyBuilders_PanicOnInvalid(t *testing.T) {
	cases := []struct {
		name string
		fn   func()
	}{
		{"vertex bad type uppercase", func() { VertexKey("Identity", testNanoID1) }},
		{"vertex bad type dot", func() { VertexKey("ident.ity", testNanoID1) }},
		{"vertex bad id forbidden char", func() { VertexKey("identity", "IHj4kPmRtw9nbCxz5vQ2") }},
		{"vertex bad id short", func() { VertexKey("identity", "abc") }},
		{"aspect bad parent", func() { AspectKey("not-a-vertex-key", "email") }},
		{"aspect bad localName uppercase first", func() { AspectKey(VertexKey("identity", testNanoID1), "Email") }},
		{"aspect bad localName dot", func() { AspectKey(VertexKey("identity", testNanoID1), "em.ail") }},
		{"link bad linkName", func() { LinkKey("lease", testNanoID3, "Held-By", "identity", testNanoID1) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r == nil {
					t.Fatalf("expected panic for %q, got none", tc.name)
				}
			}()
			tc.fn()
		})
	}
}

func TestClassifyKey(t *testing.T) {
	cases := []struct {
		key  string
		want KeyKind
	}{
		{VertexKey("identity", testNanoID1), KindVertex},
		{AspectKey(VertexKey("identity", testNanoID1), "email"), KindAspect},
		{LinkKey("lease", testNanoID3, "heldBy", "identity", testNanoID1), KindLink},
		{"vtx.identity.too.many.segments", KindUnknown},
		{"foo.bar.baz", KindUnknown},
		{"vtx.Identity." + testNanoID1, KindUnknown}, // uppercase type
		{"vtx.identity.bad-id-format!!", KindUnknown},
	}
	for _, tc := range cases {
		if got := ClassifyKey(tc.key); got != tc.want {
			t.Fatalf("ClassifyKey(%q) = %v, want %v", tc.key, got, tc.want)
		}
	}
}

func TestParsers(t *testing.T) {
	vtx := VertexKey("identity", testNanoID1)
	asp := AspectKey(vtx, "email")
	lnk := LinkKey("lease", testNanoID3, "heldBy", "identity", testNanoID1)

	if vt, id, ok := ParseVertexKey(vtx); !ok || vt != "identity" || id != testNanoID1 {
		t.Fatalf("ParseVertexKey = %q,%q,%v", vt, id, ok)
	}
	if vk, vt, id, ln, ok := ParseAspectKey(asp); !ok || vk != vtx || vt != "identity" || id != testNanoID1 || ln != "email" {
		t.Fatalf("ParseAspectKey = %q,%q,%q,%q,%v", vk, vt, id, ln, ok)
	}
	if t1, i1, ln, t2, i2, ok := ParseLinkKey(lnk); !ok || t1 != "lease" || i1 != testNanoID3 || ln != "heldBy" || t2 != "identity" || i2 != testNanoID1 {
		t.Fatalf("ParseLinkKey = %q,%q,%q,%q,%q,%v", t1, i1, ln, t2, i2, ok)
	}
	if _, _, ok := ParseVertexKey(asp); ok {
		t.Fatalf("ParseVertexKey on aspect key should fail")
	}
}

func TestUnderscoreLocalNameAccepted(t *testing.T) {
	// Substrate accepts underscore-prefixed local names so platform-generated
	// system metadata can flow through. Reservation enforcement (Contract #1
	// §1.4) is the Processor's responsibility at commit step 6.
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("AspectKey should accept underscore-prefixed name, got panic: %v", r)
		}
	}()
	k := AspectKey(VertexKey("identity", testNanoID1), "_systemMeta")
	if !strings.HasSuffix(k, "._systemMeta") {
		t.Fatalf("unexpected aspect key: %q", k)
	}
}
