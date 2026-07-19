package pkgmgr

import "testing"

func mutation(key, cypher string) installMutation {
	return installMutation{
		Op:       "update",
		Key:      key,
		Document: map[string]any{"class": "spec", "data": map[string]any{"cypherRule": cypher}},
	}
}

// TestContentRequestID_SameVersionEditIsNotADuplicate is the regression this
// exists for. `make reinstall-package` diff-applies an EDITED package at the
// SAME version, so fromVersion == toVersion and a requestId derived from
// name+version alone is byte-identical on every run. The Processor dedups on
// requestId at step 2, so the first same-version reinstall applied and every
// later one was discarded — while the CLI still logged "upgrade committed",
// because ReplyStatusDuplicate is treated as success. A package's DDL or lens
// spec then silently stopped tracking its source.
func TestContentRequestID_SameVersionEditIsNotADuplicate(t *testing.T) {
	before := []installMutation{mutation("vtx.meta.abc.spec", "RETURN a AS x")}
	after := []installMutation{mutation("vtx.meta.abc.spec", "RETURN a AS x, b AS y")}

	idBefore, err := contentRequestID("edge-manifest", "0.3.0->0.3.0", "upgrade-op", before)
	if err != nil {
		t.Fatalf("contentRequestID(before): %v", err)
	}
	idAfter, err := contentRequestID("edge-manifest", "0.3.0->0.3.0", "upgrade-op", after)
	if err != nil {
		t.Fatalf("contentRequestID(after): %v", err)
	}
	if idBefore == idAfter {
		t.Fatalf("a same-version edit must not reuse the requestId of the previous content (both %q) — the Processor would dedup it away", idBefore)
	}
}

// TestContentRequestID_IdenticalContentStillDedups pins the property the
// determinism exists for: re-submitting the SAME work must still collapse to
// one op. The fix must not trade idempotency for freshness.
func TestContentRequestID_IdenticalContentStillDedups(t *testing.T) {
	muts := []installMutation{mutation("vtx.meta.abc.spec", "RETURN a AS x")}

	first, err := contentRequestID("edge-manifest", "0.3.0->0.3.0", "upgrade-op", muts)
	if err != nil {
		t.Fatalf("contentRequestID(first): %v", err)
	}
	second, err := contentRequestID("edge-manifest", "0.3.0->0.3.0", "upgrade-op", muts)
	if err != nil {
		t.Fatalf("contentRequestID(second): %v", err)
	}
	if first != second {
		t.Fatalf("identical content must yield an identical requestId, got %q and %q", first, second)
	}
}

// TestContentRequestID_ScopesStayIndependent: the package name, the version
// scope and the op tag each still separate one op from another, so an install
// and an upgrade carrying the same mutations never collide.
func TestContentRequestID_ScopesStayIndependent(t *testing.T) {
	muts := []installMutation{mutation("vtx.meta.abc.spec", "RETURN a AS x")}

	base, err := contentRequestID("edge-manifest", "0.3.0", "install-op", muts)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, pkg, scope, tag string }{
		{"different package", "other-pkg", "0.3.0", "install-op"},
		{"different version scope", "edge-manifest", "0.4.0", "install-op"},
		{"different op tag", "edge-manifest", "0.3.0", "upgrade-op"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := contentRequestID(tc.pkg, tc.scope, tc.tag, muts)
			if err != nil {
				t.Fatal(err)
			}
			if got == base {
				t.Fatalf("%s must yield a distinct requestId, both %q", tc.name, base)
			}
		})
	}
}

// TestMutationsDigest_OrderSensitive: the digest covers the batch as emitted.
// Two batches with the same members in a different order are different ops —
// the installer emits a deterministic order, so a reordering is a real change.
func TestMutationsDigest_OrderSensitive(t *testing.T) {
	a := mutation("vtx.meta.a.spec", "RETURN a")
	b := mutation("vtx.meta.b.spec", "RETURN b")

	ab, err := mutationsDigest([]installMutation{a, b})
	if err != nil {
		t.Fatal(err)
	}
	ba, err := mutationsDigest([]installMutation{b, a})
	if err != nil {
		t.Fatal(err)
	}
	if ab == ba {
		t.Fatal("digest must distinguish batch order")
	}
}
