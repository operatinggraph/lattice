package full

import (
	"testing"
)

// TestNanoIDFromVertexKey covers the fail-closed extraction the nanoIdFromKey
// UDF dispatch in evalFunctionCall delegates to (Contract #6 §6.14 opaque-match
// anchor representation, D1). Pure-Go unit test (no NATS) — runs under -short.
//
// The security-relevant invariant is fail-closed: only a well-formed
// vtx.<type>.<id> vertex key yields a NanoID; every other shape (aspect, link,
// truncated, empty segment) ERRORS so the auth-plane lens can never project a
// wrong anchor.
func TestNanoIDFromVertexKey(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{"plain vertex", "vtx.identity.Lk2Pn6mQrtwzKbcXvP3T", "Lk2Pn6mQrtwzKbcXvP3T", false},
		{"meta vertex", "vtx.meta.Qz7Rp2mNabc9DEFghijk", "Qz7Rp2mNabc9DEFghijk", false},
		{"nanoid with hyphen+underscore", "vtx.unit.a-B_c1d2e3", "a-B_c1d2e3", false},
		// Aspect key — four segments, also vtx-prefixed: must NOT extract the
		// localName as if it were a NanoID.
		{"aspect key rejected", "vtx.identity.Lk2Pn6mQrtwzKbcXvP3T.name", "", true},
		// Link key — wrong prefix + segment count.
		{"link key rejected", "lnk.identity.A.holdsRole.role.B", "", true},
		{"bare nanoid rejected", "Lk2Pn6mQrtwzKbcXvP3T", "", true},
		{"two segments rejected", "vtx.identity", "", true},
		{"wrong prefix rejected", "vrt.identity.Lk2Pn6mQrtwzKbcXvP3T", "", true},
		{"empty type rejected", "vtx..Lk2Pn6mQrtwzKbcXvP3T", "", true},
		{"empty id rejected", "vtx.identity.", "", true},
		{"empty string rejected", "", "", true},
		{"trailing dot rejected", "vtx.identity.id.", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := nanoIDFromVertexKey(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("nanoIDFromVertexKey(%q) = %q, want error", tt.in, got)
				}
				if got != "" {
					t.Fatalf("nanoIDFromVertexKey(%q) returned %q on error; fail-closed must yield empty", tt.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("nanoIDFromVertexKey(%q) unexpected error: %v", tt.in, err)
			}
			if got != tt.want {
				t.Fatalf("nanoIDFromVertexKey(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestParse_NanoIDFromKeyUDF asserts cypher referencing the new UDF parses
// cleanly (the cheap smoke test; execution is covered by the cap-read lens
// contract test under NATS).
func TestParse_NanoIDFromKeyUDF(t *testing.T) {
	parse(t, `MATCH (i:identity {key: $actorKey}) RETURN nanoIdFromKey(i.key) AS anchorId`)
}
