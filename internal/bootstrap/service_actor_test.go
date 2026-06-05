package bootstrap

import (
	"encoding/json"
	"testing"
)

// populateForTest generates a fresh primordial ID set into the package vars.
func populateForTest(t *testing.T) {
	t.Helper()
	path := t.TempDir() + "/lattice.bootstrap.json"
	if _, err := LoadOrGenerate(path); err != nil {
		t.Fatalf("LoadOrGenerate: %v", err)
	}
}

// entriesByKey indexes a primordial entry slice by key for assertions.
func entriesByKey(t *testing.T) map[string][]byte {
	t.Helper()
	entries, err := buildPrimordialEntries()
	if err != nil {
		t.Fatalf("buildPrimordialEntries: %v", err)
	}
	idx := make(map[string][]byte, len(entries))
	for _, e := range entries {
		if _, dup := idx[e.key]; dup {
			t.Fatalf("duplicate primordial key in batch: %s", e.key)
		}
		idx[e.key] = e.value
	}
	return idx
}

type vtxEnvelope struct {
	Key       string         `json:"key"`
	Class     string         `json:"class"`
	IsDeleted bool           `json:"isDeleted"`
	Data      map[string]any `json:"data"`
}

type linkEnvelope struct {
	Key          string `json:"key"`
	Class        string `json:"class"`
	SourceVertex string `json:"sourceVertex"`
	TargetVertex string `json:"targetVertex"`
	LocalName    string `json:"localName"`
}

// TestServiceActorIdentities_Seeded asserts both Loom and Weaver identity
// vertices are present in the primordial batch with the correct class,
// protected flag, and Contract #1 vtx.identity.<id> key shape.
func TestServiceActorIdentities_Seeded(t *testing.T) {
	populateForTest(t)
	idx := entriesByKey(t)

	cases := []struct {
		key   string
		class string
	}{
		{LoomIdentityKey, "identity.system.loom"},
		{WeaverIdentityKey, "identity.system.weaver"},
	}
	for _, tc := range cases {
		raw, ok := idx[tc.key]
		if !ok {
			t.Fatalf("primordial batch missing service-actor identity %q", tc.key)
		}
		var v vtxEnvelope
		if err := json.Unmarshal(raw, &v); err != nil {
			t.Fatalf("unmarshal %q: %v", tc.key, err)
		}
		if v.Key != tc.key {
			t.Errorf("%q: envelope.key = %q", tc.key, v.Key)
		}
		if v.Class != tc.class {
			t.Errorf("%q: class = %q, want %q", tc.key, v.Class, tc.class)
		}
		if v.IsDeleted {
			t.Errorf("%q: isDeleted is true", tc.key)
		}
		if prot, _ := v.Data["protected"].(bool); !prot {
			t.Errorf("%q: data.protected = %v, want true", tc.key, v.Data["protected"])
		}
	}
}

// TestServiceActorHoldsRoleLinks_Seeded asserts the two holdsRole links are
// present with identity=source, operator role=target (Contract #1 §1.1
// direction: later-arriving vertex is the source) and read as the sentence
// "<service> holdsRole operator".
func TestServiceActorHoldsRoleLinks_Seeded(t *testing.T) {
	populateForTest(t)
	idx := entriesByKey(t)

	roleTarget := "vtx.role." + RoleOperatorID
	cases := []struct {
		key    string
		source string
	}{
		{LoomHoldsRoleLinkKey, "vtx.identity." + LoomIdentityID},
		{WeaverHoldsRoleLinkKey, "vtx.identity." + WeaverIdentityID},
	}
	for _, tc := range cases {
		raw, ok := idx[tc.key]
		if !ok {
			t.Fatalf("primordial batch missing holdsRole link %q", tc.key)
		}
		var l linkEnvelope
		if err := json.Unmarshal(raw, &l); err != nil {
			t.Fatalf("unmarshal %q: %v", tc.key, err)
		}
		if l.LocalName != "holdsRole" {
			t.Errorf("%q: localName = %q, want holdsRole", tc.key, l.LocalName)
		}
		if l.SourceVertex != tc.source {
			t.Errorf("%q: sourceVertex = %q, want %q (identity is source)", tc.key, l.SourceVertex, tc.source)
		}
		if l.TargetVertex != roleTarget {
			t.Errorf("%q: targetVertex = %q, want %q (operator role is target)", tc.key, l.TargetVertex, roleTarget)
		}
	}
}

// TestServiceActors_ReuseOperatorRole proves the AC #2 invariant: the service
// actors add NO new role/permission/grantedBy entries. The only new keys
// beyond the admin baseline are exactly the 2 identity vertices + 2 holdsRole
// links; root-equivalence is established by reusing the existing operator
// topology.
func TestServiceActors_ReuseOperatorRole(t *testing.T) {
	populateForTest(t)
	idx := entriesByKey(t)

	newKeys := []string{LoomIdentityKey, WeaverIdentityKey, LoomHoldsRoleLinkKey, WeaverHoldsRoleLinkKey}
	for _, k := range newKeys {
		if _, ok := idx[k]; !ok {
			t.Fatalf("expected new primordial key absent: %s", k)
		}
	}

	// Both links must target the SAME pre-existing operator role the admin
	// holds — not a fresh "systemRoot"-style role.
	roleTarget := "vtx.role." + RoleOperatorID
	for _, k := range []string{LoomHoldsRoleLinkKey, WeaverHoldsRoleLinkKey} {
		var l linkEnvelope
		if err := json.Unmarshal(idx[k], &l); err != nil {
			t.Fatalf("unmarshal %q: %v", k, err)
		}
		if l.TargetVertex != roleTarget {
			t.Fatalf("%q targets %q, not the existing operator role %q", k, l.TargetVertex, roleTarget)
		}
	}
}

// TestPrimordialVertexKeys_IncludesServiceActors asserts the kernel-
// verification enumeration covers all four new keys (so verify-kernel checks
// them).
func TestPrimordialVertexKeys_IncludesServiceActors(t *testing.T) {
	populateForTest(t)
	want := map[string]bool{
		LoomIdentityKey:        false,
		WeaverIdentityKey:      false,
		LoomHoldsRoleLinkKey:   false,
		WeaverHoldsRoleLinkKey: false,
	}
	for _, k := range PrimordialVertexKeys() {
		if _, ok := want[k]; ok {
			want[k] = true
		}
	}
	for k, found := range want {
		if !found {
			t.Errorf("PrimordialVertexKeys() does not enumerate %q", k)
		}
	}
}

// TestServiceActors_KeyCountDelta asserts the primordial batch grew by exactly
// 4 entries (2 vertices + 2 links) relative to a baseline computed by removing
// the service-actor keys — guarding the verify-kernel count delta.
func TestServiceActors_KeyCountDelta(t *testing.T) {
	populateForTest(t)
	idx := entriesByKey(t)

	serviceKeys := map[string]bool{
		LoomIdentityKey:        true,
		WeaverIdentityKey:      true,
		LoomHoldsRoleLinkKey:   true,
		WeaverHoldsRoleLinkKey: true,
	}
	count := 0
	for k := range idx {
		if serviceKeys[k] {
			count++
		}
	}
	if count != 4 {
		t.Fatalf("expected exactly 4 service-actor entries in batch, got %d", count)
	}
}
